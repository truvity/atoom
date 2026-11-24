//! The loader (record 0019, "The loading rules"): the cold start, the atomic swap, the last good set and the refresh over the source
//! port.

use std::fmt;
use std::sync::{Arc, RwLock};
use std::time::{Duration, Instant};

use atoom_socket::{Client, Transport};
use atoom_source::atoom::source::v1alpha1::{
    CapabilitiesRequest, ReadRequest, SourceServiceClient, SourceWatchRequest, Watch,
};
use connectrpc::ConnectError;

use crate::bundle::Fault;
use crate::document::{Publisher, Refused, Sequence, sequence, verify};
use crate::set::Set;

/// The name the bundle is read by through the source port (the file driver serves it from `policy-bundle.jws`).
pub const DEFAULT_NAME: &str = "policy-bundle";
/// The kind the protected header must name.
pub const DEFAULT_KIND: &str = "PolicyBundle";
/// The least time between two reads of the source by [`Loader::run`].
pub const DEFAULT_MIN_INTERVAL: Duration = Duration::from_secs(1);
/// How often [`Loader::run`] reads a POLL source that gives no `poll_after`, and a NOTIFY source while its watch cannot be opened.
pub const DEFAULT_POLL_EVERY: Duration = Duration::from_secs(30);
/// The bound of one call to the source.
const READ_TIMEOUT: Duration = Duration::from_secs(10);

/// A hook the host gives to hear of the start and of every reload that is not [`Pass::Unchanged`], for its audit record.
pub type OnReload = Arc<dyn Fn(&Outcome) + Send + Sync>;

/// What a host gives the loader.
#[derive(Clone)]
pub struct Options {
    /// The client of the source port that serves the bundle.
    pub source: Client,
    /// The name the bundle is read by.
    pub name: String,
    /// The kind the protected header must name. A host whose bundles a second party co-signs names the kind that party's key
    /// co-signs.
    pub kind: String,
    /// The pinned keys. At least one has no co-signs.
    pub publishers: Vec<Publisher>,
    /// The least time between two reads by [`Loader::run`].
    pub min_interval: Duration,
    /// See [`DEFAULT_POLL_EVERY`].
    pub poll_every: Duration,
    /// Told the outcome of the start and of every reload that is not unchanged.
    pub on_reload: Option<OnReload>,
}

impl Options {
    /// The options with the defaults: the name `policy-bundle`, the kind `PolicyBundle`, a minimum interval of a second.
    #[must_use]
    pub fn new(source: Client, publishers: Vec<Publisher>) -> Self {
        Self {
            source,
            name: DEFAULT_NAME.to_owned(),
            kind: DEFAULT_KIND.to_owned(),
            publishers,
            min_interval: DEFAULT_MIN_INTERVAL,
            poll_every: DEFAULT_POLL_EVERY,
            on_reload: None,
        }
    }
}

/// What one pass of the loader did.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Pass {
    /// A new set is in force.
    Applied,
    /// The source has the version in force, or a document with the seq and digest in force.
    Unchanged,
    /// The document or the bundle was refused; the set in force stays.
    Rejected,
    /// The source could not be read; the set in force stays.
    Unavailable,
}

/// Why a pass did not apply a set.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum LoadError {
    /// The source could not be read, or answered unchanged with nothing in force.
    Unavailable(String),
    /// The signed document was refused (steps 1 to 4).
    Document(Refused),
    /// A lower seq than the one in force (step 5).
    Older,
    /// The same seq with another payload (step 5).
    Conflict,
    /// The bundle was refused.
    Bundle(Fault),
}

impl fmt::Display for LoadError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Unavailable(why) => write!(f, "the source could not be read: {why}"),
            Self::Document(r) => r.fmt(f),
            Self::Older => {
                f.write_str("signed document: step 5: a lower seq than the one in force")
            }
            Self::Conflict => {
                f.write_str("signed document: step 5: the same seq with another payload")
            }
            Self::Bundle(b) => b.fmt(f),
        }
    }
}

impl std::error::Error for LoadError {}

/// The result of one pass, the digest and seq of the set in force after it, and the error of a rejected or unavailable pass.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Outcome {
    pub pass: Pass,
    pub digest: String,
    pub seq: u64,
    pub error: Option<LoadError>,
}

/// What one pass keeps between passes.
struct Held {
    version: String,
    digest: [u8; 48],
    poll_after: Option<Duration>,
    last: Option<Instant>,
}

impl Held {
    fn new() -> Self {
        Self {
            version: String::new(),
            digest: [0; 48],
            poll_after: None,
            last: None,
        }
    }
}

/// Keeps the set of one bundle in force, by the loading rules of record 0019.
pub struct Loader {
    opts: Options,
    current: RwLock<Arc<Set>>,
    held: tokio::sync::Mutex<Held>,
}

impl fmt::Debug for Loader {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Loader")
            .field("name", &self.opts.name)
            .field("current", &*self.current())
            .finish_non_exhaustive()
    }
}

fn rpc(c: &Client) -> SourceServiceClient<Transport> {
    SourceServiceClient::new(c.transport(), c.config())
}

/// The result of a pass, before it is reported.
enum Step {
    Apply(Arc<Set>),
    Unchanged,
    Fail(Pass, LoadError),
}

async fn pass(opts: &Options, held: &mut Held, current: Option<&Arc<Set>>) -> Step {
    let read = tokio::time::timeout(
        READ_TIMEOUT,
        rpc(&opts.source).read(ReadRequest {
            name: opts.name.clone(),
            if_none_match: held.version.clone(),
            ..ReadRequest::default()
        }),
    )
    .await;
    let r = match read {
        Err(_) => {
            return Step::Fail(
                Pass::Unavailable,
                LoadError::Unavailable("no answer".into()),
            );
        }
        Ok(Err(e)) => {
            return Step::Fail(Pass::Unavailable, LoadError::Unavailable(e.to_string()));
        }
        Ok(Ok(r)) => r.into_owned(),
    };
    if let Some(d) = r.poll_after.as_option() {
        held.poll_after = u64::try_from(d.seconds)
            .ok()
            .map(|s| Duration::new(s, u32::try_from(d.nanos).unwrap_or(0)));
    }
    if r.unchanged {
        return if current.is_some() {
            Step::Unchanged
        } else {
            Step::Fail(
                Pass::Unavailable,
                LoadError::Unavailable(
                    "the source answered unchanged with nothing in force".into(),
                ),
            )
        };
    }
    let v = match verify(&r.data, &opts.kind, &opts.publishers) {
        Ok(v) => v,
        Err(e) => return Step::Fail(Pass::Rejected, LoadError::Document(e)),
    };
    match sequence(current.map(|c| (c.seq(), &held.digest)), &v) {
        Sequence::Same => {
            held.version = r.version;
            return Step::Unchanged;
        }
        Sequence::Older => return Step::Fail(Pass::Rejected, LoadError::Older),
        Sequence::Conflict => return Step::Fail(Pass::Rejected, LoadError::Conflict),
        Sequence::Newer => {}
    }
    match Set::from_verified(&v) {
        Ok(s) => {
            held.version = r.version;
            held.digest = v.digest;
            Step::Apply(Arc::new(s))
        }
        Err(f) => Step::Fail(Pass::Rejected, LoadError::Bundle(f)),
    }
}

impl Loader {
    /// Reads, verifies and compiles the bundle once: the cold start.
    ///
    /// # Errors
    ///
    /// The reason when there is no valid set: a host does not start without one.
    pub async fn start(opts: Options) -> Result<Self, LoadError> {
        let mut held = Held::new();
        let set = match pass(&opts, &mut held, None).await {
            Step::Apply(s) => s,
            Step::Fail(_, e) => return Err(e),
            Step::Unchanged => {
                return Err(LoadError::Unavailable(
                    "the source answered unchanged with nothing in force".into(),
                ));
            }
        };
        if let Some(hook) = &opts.on_reload {
            hook(&Outcome {
                pass: Pass::Applied,
                digest: set.digest().to_owned(),
                seq: set.seq(),
                error: None,
            });
        }
        Ok(Self {
            opts,
            current: RwLock::new(set),
            held: tokio::sync::Mutex::new(held),
        })
    }

    /// The set in force.
    #[must_use]
    pub fn current(&self) -> Arc<Set> {
        self.current
            .read()
            .unwrap_or_else(std::sync::PoisonError::into_inner)
            .clone()
    }

    /// One pass: read the source with the version label in force, verify, compare the seq, compile, and swap the set in, or keep the
    /// set in force. [`Loader::run`] calls it.
    pub async fn reload(&self) -> Outcome {
        let mut held = self.held.lock().await;
        let current = self.current();
        let (p, error) = match pass(&self.opts, &mut held, Some(&current)).await {
            Step::Apply(s) => {
                *self
                    .current
                    .write()
                    .unwrap_or_else(std::sync::PoisonError::into_inner) = s;
                (Pass::Applied, None)
            }
            Step::Unchanged => (Pass::Unchanged, None),
            Step::Fail(p, e) => (p, Some(e)),
        };
        let now = self.current();
        let out = Outcome {
            pass: p,
            digest: now.digest().to_owned(),
            seq: now.seq(),
            error,
        };
        if p != Pass::Unchanged
            && let Some(hook) = &self.opts.on_reload
        {
            hook(&out);
        }
        out
    }

    /// Waits until the minimum interval has passed since the last read by `run`, then reloads.
    async fn throttled(&self) {
        let wait = {
            let mut held = self.held.lock().await;
            let now = Instant::now();
            let wait = held.last.map_or(Duration::ZERO, |l| {
                (l + self.opts.min_interval).saturating_duration_since(now)
            });
            held.last = Some(now + wait);
            wait
        };
        if !wait.is_zero() {
            tokio::time::sleep(wait).await;
        }
        self.reload().await;
    }

    /// Keeps the set current, as the source says it can: NOTIFY, a watch of the name and a read after each message; POLL, a read after
    /// `poll_after`; NONE, nothing (a Lambda layer does not change), and it returns at once. Two reads are at least the minimum interval
    /// apart. A failed read or a refused document keeps the set in force. Otherwise it runs until its future is dropped.
    ///
    /// # Errors
    ///
    /// The error of the `Capabilities` call.
    pub async fn run(&self) -> Result<(), ConnectError> {
        let caps = rpc(&self.opts.source)
            .capabilities(CapabilitiesRequest::default())
            .await?
            .into_owned();
        match caps.watch.as_known() {
            Some(Watch::NONE) => Ok(()),
            Some(Watch::NOTIFY) => {
                self.run_watch().await;
                Ok(())
            }
            _ => {
                self.run_poll().await;
                Ok(())
            }
        }
    }

    async fn run_poll(&self) {
        loop {
            let after = self.held.lock().await.poll_after;
            let wait = after
                .filter(|d| !d.is_zero())
                .unwrap_or(self.opts.poll_every)
                .max(self.opts.min_interval);
            tokio::time::sleep(wait).await;
            self.throttled().await;
        }
    }

    async fn run_watch(&self) {
        loop {
            let opened = rpc(&self.opts.source)
                .watch(SourceWatchRequest {
                    name: self.opts.name.clone(),
                    ..SourceWatchRequest::default()
                })
                .await;
            let Ok(mut stream) = opened else {
                // The watch cannot be opened: read on a timer until it can.
                tokio::time::sleep(self.opts.poll_every).await;
                self.throttled().await;
                continue;
            };
            // A change may have come between the last read and the opening of the watch.
            self.throttled().await;
            while let Ok(Some(_)) = stream.message().await {
                self.throttled().await;
            }
            tokio::time::sleep(self.opts.min_interval).await;
        }
    }
}
