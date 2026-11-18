//! The source-port suite: the cases every driver behind `atoom.source.v1alpha1.SourceService` must pass. The suite speaks the contract
//! and nothing else, over a client the caller dials. It reads and never writes, so it can run against a driver that serves real
//! documents or secrets.

use std::time::Duration;

use anyhow::{Context, bail, ensure};
use atoom_common::refusal;
use atoom_socket::{Client, Transport};
use atoom_source::atoom::source::v1alpha1::{
    CapabilitiesRequest, ReadRequest, ReadResponse, SourceServiceClient, SourceWatchRequest, Watch,
};
use connectrpc::ConnectError;
use connectrpc::error::ErrorCode;

use crate::{Case, run_case};

type Rpc = SourceServiceClient<Transport>;

/// How long a stream that has nothing to say is watched for a message: longer than any driver looks again (the file driver every
/// 200 ms).
const SILENT: Duration = Duration::from_millis(1_200);
/// How long a call that must answer is given.
const ANSWERS: Duration = Duration::from_secs(10);

/// What the caller put in the driver's backend: a name the driver serves and the exact bytes it must return for it.
#[derive(Clone, Debug)]
pub struct Fixture {
    /// A name the driver serves. It must be a name the driver's backend accepts (a kebab-case document name for the file driver, a
    /// credential name for the credential driver).
    pub name: String,
    /// The bytes the driver must return for the name.
    pub data: Vec<u8>,
}

/// Runs every case against the driver behind `client` and returns their results. The caller decides what a failure means
/// ([`crate::report`] fails on any failed case and on none run).
pub async fn source_suite(client: Client, fixture: &Fixture) -> Vec<Case> {
    let s = Suite::new(client, fixture);
    vec![
        run_case("capabilities", s.capabilities()).await,
        run_case("read_gives_the_bytes_and_a_label", s.read_bytes()).await,
        run_case("read_if_none_match", s.if_none_match()).await,
        run_case("absent_name", s.absent()).await,
        run_case("malformed_names", s.malformed()).await,
        run_case("watch_follows_the_capability", s.watch()).await,
    ]
}

struct Suite<'a> {
    client: Client,
    fixture: &'a Fixture,
    /// A name of this run that no document has: lower-case letters, digits and `-`, so that every driver takes it as a name.
    absent: String,
}

impl<'a> Suite<'a> {
    fn new(client: Client, fixture: &'a Fixture) -> Self {
        let nanos = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map_or(0, |d| d.as_nanos());
        Self {
            client,
            fixture,
            absent: format!("conformance-absent-{:x}{:x}", nanos, std::process::id()),
        }
    }

    fn rpc(&self) -> Rpc {
        SourceServiceClient::new(self.client.transport(), self.client.config())
    }

    async fn read(&self, name: &str, held: &str) -> Result<ReadResponse, ConnectError> {
        let r = tokio::time::timeout(
            ANSWERS,
            self.rpc().read(ReadRequest {
                name: name.to_owned(),
                if_none_match: held.to_owned(),
                ..ReadRequest::default()
            }),
        )
        .await
        .map_err(|_| ConnectError::new(ErrorCode::DeadlineExceeded, "no answer to a read"))??;
        Ok(r.into_owned())
    }

    async fn watch_known(&self) -> anyhow::Result<Watch> {
        let caps = tokio::time::timeout(
            ANSWERS,
            self.rpc().capabilities(CapabilitiesRequest::default()),
        )
        .await
        .context("no answer to Capabilities")?
        .context("Capabilities")?
        .into_owned();
        match caps.watch.as_known() {
            Some(w) if w != Watch::WATCH_UNSPECIFIED => Ok(w),
            other => bail!("the capability watch is {other:?}, want NONE, NOTIFY or POLL"),
        }
    }

    async fn capabilities(&self) -> anyhow::Result<()> {
        self.watch_known().await.map(|_| ())
    }

    async fn read_bytes(&self) -> anyhow::Result<()> {
        let f = self.fixture;
        let first = self.read(&f.name, "").await.context("read the fixture")?;
        ensure!(
            !first.unchanged,
            "a read that holds nothing was answered unchanged"
        );
        ensure!(
            first.data == f.data,
            "the bytes are not the bytes of the fixture"
        );
        ensure!(!first.version.is_empty(), "the label is empty");
        let again = self
            .read(&f.name, "")
            .await
            .context("read the fixture again")?;
        ensure!(again.version == first.version, "one content, two labels");
        ensure!(again.data == f.data, "the bytes changed between two reads");
        Ok(())
    }

    async fn if_none_match(&self) -> anyhow::Result<()> {
        let f = self.fixture;
        let held = self
            .read(&f.name, "")
            .await
            .context("read the fixture")?
            .version;
        let same = self
            .read(&f.name, &held)
            .await
            .context("read at the held label")?;
        ensure!(
            same.unchanged,
            "a read at the label held was not answered unchanged"
        );
        ensure!(same.data.is_empty(), "an unchanged answer carries bytes");
        ensure!(
            same.version == held,
            "an unchanged answer changed the label"
        );
        let other = self
            .read(&f.name, "conformance-not-the-label")
            .await
            .context("read at a label that is not held")?;
        ensure!(
            !other.unchanged && other.data == f.data,
            "a read at another label did not get the bytes"
        );
        Ok(())
    }

    async fn absent(&self) -> anyhow::Result<()> {
        expect_refusal(
            self.read(&self.absent, "").await,
            ErrorCode::NotFound,
            "absent",
        )
    }

    async fn malformed(&self) -> anyhow::Result<()> {
        for bad in ["", "../x", "a/b", "a\0b"] {
            expect_refusal(
                self.read(bad, "").await,
                ErrorCode::InvalidArgument,
                "schema",
            )
            .with_context(|| format!("the name {bad:?}"))?;
        }
        Ok(())
    }

    async fn watch(&self) -> anyhow::Result<()> {
        let f = self.fixture;
        match self.watch_known().await? {
            Watch::NONE => {
                let outcome = self.watch_first(&f.name).await;
                match outcome {
                    Err(e) if e.code == ErrorCode::Unimplemented => Ok(()),
                    other => bail!(
                        "a source that cannot watch answered Watch with {other:?}, want unimplemented"
                    ),
                }
            }
            Watch::NOTIFY => {
                // Nothing changes: nothing is said (the stream is open, or the call would have failed).
                match self.watch_first(&f.name).await {
                    Err(e) if e.code == ErrorCode::DeadlineExceeded => {}
                    other => bail!(
                        "a watch of a name that does not change answered {other:?}, want silence"
                    ),
                }
                expect_refusal(
                    self.watch_first("../x").await.map(|_| ()),
                    ErrorCode::InvalidArgument,
                    "schema",
                )
                .context("a watch of a name the source cannot hold")
            }
            Watch::POLL => {
                let r = self.read(&f.name, "").await.context("read the fixture")?;
                ensure!(
                    r.poll_after
                        .as_option()
                        .is_some_and(|d| d.seconds > 0 || d.nanos > 0),
                    "a source that is polled did not say in poll_after when to ask again"
                );
                Ok(())
            }
            Watch::WATCH_UNSPECIFIED => bail!("the capability watch is not set"),
        }
    }

    /// Opens a watch of `name` and waits for its first message: the message, or the error of the call or of the stream. Silence for
    /// [`SILENT`] is a `deadline_exceeded` error made here (an open stream that said nothing).
    async fn watch_first(&self, name: &str) -> Result<String, ConnectError> {
        let mut stream = self
            .rpc()
            .watch(SourceWatchRequest {
                name: name.to_owned(),
                ..SourceWatchRequest::default()
            })
            .await?;
        match tokio::time::timeout(SILENT, stream.message()).await {
            Err(_) => Err(ConnectError::new(
                ErrorCode::DeadlineExceeded,
                "the stream said nothing",
            )),
            Ok(Err(e)) => Err(e),
            Ok(Ok(None)) => Err(ConnectError::new(ErrorCode::Aborted, "the stream ended")),
            Ok(Ok(Some(m))) => Ok(m.to_owned_message().name),
        }
    }
}

/// Checks that `res` is the refusal `code` with `reason` and the field path `name`.
fn expect_refusal<T: std::fmt::Debug>(
    res: Result<T, ConnectError>,
    code: ErrorCode,
    reason: &str,
) -> anyhow::Result<()> {
    let err = match res {
        Ok(v) => bail!("want the refusal {code:?}/{reason}, got success: {v:?}"),
        Err(e) => e,
    };
    let r = refusal::detail(&err);
    ensure!(
        err.code == code && r.as_ref().is_some_and(|r| r.reason == reason),
        "want {code:?}/{reason}, got {:?}/{:?}",
        err.code,
        r.as_ref().map(|r| r.reason.as_str())
    );
    let path = r.map(|r| r.field_path).unwrap_or_default();
    ensure!(path == "name", "want the field path \"name\", got {path:?}");
    Ok(())
}
