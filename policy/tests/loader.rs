//! The loading rules over a source served in-process (the Go twin is loader_test.go): the cold start, the swap, the last good set,
//! the sequence and the refresh.

#![allow(refining_impl_trait)] // the generated trait returns `impl Encodable`; a concrete type is fine

use std::sync::{Arc, Mutex};
use std::time::Duration;

use atoom_policy::cedar_policy::{Context, Entities};
use atoom_policy::{
    Class, DEFAULT_KIND, DEFAULT_NAME, LoadError, Loader, Options, Outcome, Pass, Publisher, uid,
};
use atoom_source::atoom::source::v1alpha1::{
    CapabilitiesRequest, CapabilitiesResponse, ReadRequest, ReadResponse, SourceService,
    SourceServiceExt, SourceWatchRequest, SourceWatchResponse, Watch,
};
use aws_lc_rs::rand::SystemRandom;
use aws_lc_rs::signature::{ECDSA_P384_SHA384_FIXED_SIGNING, EcdsaKeyPair, KeyPair as _};
use base64::Engine as _;
use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use connectrpc::error::ErrorCode;
use connectrpc::{
    ConnectError, RequestContext, Response, Router, ServiceRequest, ServiceResult, ServiceStream,
};
use tokio::sync::broadcast;

struct State {
    doc: Option<Vec<u8>>,
    version: u64,
    down: bool,
    reads: usize,
}

/// A source with one name: a document the test replaces, a version label per document, and a watch that says when it changed.
#[derive(Clone)]
struct Fake {
    watch: Watch,
    poll_after: Option<Duration>,
    state: Arc<Mutex<State>>,
    changed: broadcast::Sender<()>,
}

impl Fake {
    fn new(watch: Watch) -> Self {
        Self {
            watch,
            poll_after: None,
            state: Arc::new(Mutex::new(State {
                doc: None,
                version: 0,
                down: false,
                reads: 0,
            })),
            changed: broadcast::channel(64).0,
        }
    }

    fn set(&self, doc: Vec<u8>) {
        {
            let mut s = self.state.lock().unwrap();
            s.doc = Some(doc);
            s.version += 1;
        }
        let _ = self.changed.send(());
    }

    fn down(&self, down: bool) {
        self.state.lock().unwrap().down = down;
    }

    fn reads(&self) -> usize {
        self.state.lock().unwrap().reads
    }

    fn client(&self) -> atoom_socket::Client {
        let router: Router = Arc::new(self.clone()).register(Router::new());
        atoom_socket::in_process(router)
    }
}

impl SourceService for Fake {
    async fn capabilities(
        &self,
        _ctx: RequestContext,
        _r: ServiceRequest<'_, CapabilitiesRequest>,
    ) -> ServiceResult<CapabilitiesResponse> {
        Response::ok(CapabilitiesResponse {
            watch: self.watch.into(),
            ..CapabilitiesResponse::default()
        })
    }

    async fn read(
        &self,
        _ctx: RequestContext,
        r: ServiceRequest<'_, ReadRequest>,
    ) -> ServiceResult<ReadResponse> {
        let mut s = self.state.lock().unwrap();
        s.reads += 1;
        if s.down {
            return Err(ConnectError::new(ErrorCode::Unavailable, "down"));
        }
        let Some(doc) = s.doc.clone().filter(|_| r.name == DEFAULT_NAME) else {
            return Err(ConnectError::new(ErrorCode::NotFound, "no such name"));
        };
        let label = format!("v{}", s.version);
        let mut resp = ReadResponse {
            version: label.clone(),
            ..ReadResponse::default()
        };
        if let Some(d) = self.poll_after {
            resp.poll_after = buffa_types::google::protobuf::Duration {
                seconds: 0,
                nanos: i32::try_from(d.as_nanos()).unwrap(),
                ..Default::default()
            }
            .into();
        }
        if r.if_none_match == label {
            resp.unchanged = true;
        } else {
            resp.data = doc;
        }
        Response::ok(resp)
    }

    async fn watch(
        &self,
        _ctx: RequestContext,
        r: ServiceRequest<'_, SourceWatchRequest>,
    ) -> ServiceResult<ServiceStream<SourceWatchResponse>> {
        let name = r.name.to_owned();
        let rx = self.changed.subscribe();
        Response::stream_ok(futures::stream::unfold(
            (rx, name),
            |(mut rx, name)| async move {
                match rx.recv().await {
                    Ok(()) | Err(broadcast::error::RecvError::Lagged(_)) => {
                        let item = SourceWatchResponse {
                            name: name.clone(),
                            ..SourceWatchResponse::default()
                        };
                        Some((Ok(item), (rx, name)))
                    }
                    Err(broadcast::error::RecvError::Closed) => None,
                }
            },
        ))
    }
}

/// Signs documents with a key made for the test and thrown away after it.
struct Signer {
    kid: String,
    key: EcdsaKeyPair,
}

impl Signer {
    fn new(kid: &str) -> Self {
        Self {
            kid: kid.to_owned(),
            key: EcdsaKeyPair::generate(&ECDSA_P384_SHA384_FIXED_SIGNING).unwrap(),
        }
    }

    fn publisher(&self, co_signs: &[&str]) -> Publisher {
        Publisher::from_point(
            &self.kid,
            self.key.public_key().as_ref(),
            co_signs.iter().map(|s| (*s).to_owned()).collect(),
        )
        .unwrap()
    }

    fn signature(&self, seq: u64, payload64: &str) -> serde_json::Value {
        let header =
            serde_json::json!({"alg": "ES384", "kid": self.kid, "seq": seq, "kind": DEFAULT_KIND});
        let protected = URL_SAFE_NO_PAD.encode(serde_json::to_vec(&header).unwrap());
        let sig = self
            .key
            .sign(
                &SystemRandom::new(),
                format!("{protected}.{payload64}").as_bytes(),
            )
            .unwrap();
        serde_json::json!({"protected": protected, "signature": URL_SAFE_NO_PAD.encode(sig.as_ref())})
    }
}

fn sign(seq: u64, payload: &[u8], by: &[&Signer]) -> Vec<u8> {
    let p64 = URL_SAFE_NO_PAD.encode(payload);
    let sigs: Vec<_> = by.iter().map(|s| s.signature(seq, &p64)).collect();
    serde_json::to_vec(&serde_json::json!({"payload": p64, "signatures": sigs})).unwrap()
}

const SCHEMA: &str = r#"entity User; entity Route; action "GET" appliesTo { principal: [User], resource: [Route] };"#;

fn bundle(policies: &[(&str, &str)]) -> Vec<u8> {
    let p: serde_json::Map<String, serde_json::Value> = policies
        .iter()
        .map(|(k, v)| ((*k).to_owned(), serde_json::Value::from(*v)))
        .collect();
    serde_json::to_vec(&serde_json::json!({"schema": SCHEMA, "policies": p})).unwrap()
}

/// What a refused reload must have said.
type Check = fn(&LoadError) -> bool;

const PERMIT: &[(&str, &str)] = &[("all", "permit (principal, action, resource);")];
const FORBID: &[(&str, &str)] = &[("none", "forbid (principal, action, resource);")];

fn sha384(b: &[u8]) -> String {
    atoom_policy::hex(aws_lc_rs::digest::digest(&aws_lc_rs::digest::SHA384, b).as_ref())
}

fn allowed(l: &Loader) -> bool {
    let set = l.current();
    let d = set
        .decide(
            uid("User", "alice").unwrap(),
            uid("Action", "GET").unwrap(),
            uid("Route", "home").unwrap(),
            Context::empty(),
            &set.entities(Vec::new()).unwrap(),
        )
        .unwrap();
    assert_eq!(d.digest, set.digest());
    d.allow
}

fn options(f: &Fake, publishers: Vec<Publisher>) -> Options {
    Options::new(f.client(), publishers)
}

#[tokio::test]
async fn a_cold_start_without_a_valid_set_is_refused() {
    let publisher = Signer::new("publisher-1");
    let stranger = Signer::new("publisher-1");
    let cases: Vec<(&str, Option<Vec<u8>>)> = vec![
        ("no document", None),
        (
            "a key that is not pinned",
            Some(sign(1, &bundle(PERMIT), &[&stranger])),
        ),
        (
            "a bundle that does not compile",
            Some(sign(1, &bundle(&[("bad", "permit (")]), &[&publisher])),
        ),
        ("not a signed document", Some(bundle(PERMIT))),
    ];
    for (name, doc) in cases {
        let f = Fake::new(Watch::NONE);
        if let Some(d) = doc {
            f.set(d);
        }
        let got = Loader::start(options(&f, vec![publisher.publisher(&[])])).await;
        assert!(got.is_err(), "{name}: started");
    }
}

#[tokio::test]
async fn the_reload_rules() {
    let publisher = Signer::new("publisher-1");
    let f = Fake::new(Watch::NONE);
    let first = bundle(PERMIT);
    f.set(sign(1, &first, &[&publisher]));
    let seen: Arc<Mutex<Vec<Outcome>>> = Arc::default();
    let mut opts = options(&f, vec![publisher.publisher(&[])]);
    let s2 = seen.clone();
    opts.on_reload = Some(Arc::new(move |o: &Outcome| {
        s2.lock().unwrap().push(o.clone())
    }));
    let l = Loader::start(opts).await.unwrap();
    assert_eq!(l.current().digest(), sha384(&first));
    assert_eq!(l.current().seq(), 1);
    assert!(allowed(&l));

    assert_eq!(l.reload().await.pass, Pass::Unchanged, "the same version");

    let second = bundle(FORBID);
    f.set(sign(2, &second, &[&publisher]));
    let out = l.reload().await;
    assert_eq!(
        (out.pass, out.seq, out.digest.as_str()),
        (Pass::Applied, 2, sha384(&second).as_str())
    );
    assert!(!allowed(&l));

    let refused: Vec<(&str, Vec<u8>, Check)> = vec![
        ("an older seq", sign(1, &first, &[&publisher]), |e| {
            *e == LoadError::Older
        }),
        (
            "the same seq with another payload",
            sign(2, &first, &[&publisher]),
            |e| *e == LoadError::Conflict,
        ),
        (
            "a signature by another key",
            sign(3, &first, &[&Signer::new("publisher-1")]),
            |e| matches!(e, LoadError::Document(r) if r.step == 4),
        ),
        (
            "a bundle that fails validation",
            sign(
                3,
                &bundle(&[(
                    "x",
                    "permit (principal, action, resource) when { principal.nope };",
                )]),
                &[&publisher],
            ),
            |e| matches!(e, LoadError::Bundle(f) if f.class == Class::Validation && f.id == "x"),
        ),
    ];
    for (name, doc, want) in refused {
        f.set(doc);
        let out = l.reload().await;
        assert_eq!(out.pass, Pass::Rejected, "{name}");
        assert!(want(out.error.as_ref().unwrap()), "{name}: {:?}", out.error);
        assert_eq!(
            (out.seq, out.digest.as_str()),
            (2, sha384(&second).as_str()),
            "{name}: the set in force changed"
        );
        assert!(!allowed(&l));
    }

    f.set(sign(2, &second, &[&publisher]));
    assert_eq!(
        l.reload().await.pass,
        Pass::Unchanged,
        "the same document again"
    );

    f.down(true);
    let out = l.reload().await;
    assert_eq!((out.pass, out.seq), (Pass::Unavailable, 2));
    f.down(false);

    let got: Vec<Pass> = seen.lock().unwrap().iter().map(|o| o.pass).collect();
    assert_eq!(
        got,
        vec![
            Pass::Applied,
            Pass::Applied,
            Pass::Rejected,
            Pass::Rejected,
            Pass::Rejected,
            Pass::Rejected,
            Pass::Unavailable
        ]
    );
}

#[tokio::test]
async fn a_co_signer_must_sign() {
    let publisher = Signer::new("publisher-1");
    let party = Signer::new("party-1");
    let pins = vec![publisher.publisher(&[]), party.publisher(&[DEFAULT_KIND])];
    let f = Fake::new(Watch::NONE);
    f.set(sign(1, &bundle(PERMIT), &[&publisher]));
    assert!(Loader::start(options(&f, pins.clone())).await.is_err());
    f.set(sign(1, &bundle(PERMIT), &[&publisher, &party]));
    assert!(Loader::start(options(&f, pins)).await.is_ok());
}

async fn wait_for(what: &str, cond: impl Fn() -> bool) {
    for _ in 0..1000 {
        if cond() {
            return;
        }
        tokio::time::sleep(Duration::from_millis(5)).await;
    }
    panic!("timed out waiting for {what}");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn run_follows_the_source() {
    for watch in [Watch::NOTIFY, Watch::POLL] {
        let publisher = Signer::new("publisher-1");
        let mut f = Fake::new(watch);
        f.poll_after = Some(Duration::from_millis(10));
        f.set(sign(1, &bundle(PERMIT), &[&publisher]));
        let mut opts = options(&f, vec![publisher.publisher(&[])]);
        opts.min_interval = Duration::from_millis(10);
        let l = Arc::new(Loader::start(opts).await.unwrap());
        let runner = {
            let l = l.clone();
            tokio::spawn(async move { l.run().await })
        };
        let decider = {
            let l = l.clone();
            tokio::spawn(async move {
                for _ in 0..2000 {
                    let _ = allowed(&l);
                    tokio::task::yield_now().await;
                }
            })
        };
        f.set(sign(2, &bundle(FORBID), &[&publisher]));
        wait_for("the newer set", || l.current().seq() == 2).await;
        f.set(sign(3, &bundle(&[("bad", "permit (")]), &[&publisher]));
        f.set(sign(4, &bundle(PERMIT), &[&publisher]));
        wait_for("the set after a refused one", || l.current().seq() == 4).await;
        decider.await.unwrap();
        runner.abort();
    }
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn run_keeps_the_minimum_interval() {
    let publisher = Signer::new("publisher-1");
    let f = Fake::new(Watch::NOTIFY);
    f.set(sign(1, &bundle(PERMIT), &[&publisher]));
    let mut opts = options(&f, vec![publisher.publisher(&[])]);
    opts.min_interval = Duration::from_millis(200);
    let l = Arc::new(Loader::start(opts).await.unwrap());
    let runner = {
        let l = l.clone();
        tokio::spawn(async move { l.run().await })
    };
    tokio::time::sleep(Duration::from_millis(50)).await;
    let before = f.reads();
    for i in 0..10 {
        f.set(sign(2 + i, &bundle(PERMIT), &[&publisher]));
    }
    tokio::time::sleep(Duration::from_millis(500)).await;
    let n = f.reads() - before;
    assert!(
        n <= 5,
        "{n} reads in 500 ms at a minimum interval of 200 ms"
    );
    runner.abort();
}

#[tokio::test]
async fn run_returns_for_a_source_that_never_changes() {
    let publisher = Signer::new("publisher-1");
    let f = Fake::new(Watch::NONE);
    f.set(sign(1, &bundle(PERMIT), &[&publisher]));
    let l = Loader::start(options(&f, vec![publisher.publisher(&[])]))
        .await
        .unwrap();
    l.run().await.unwrap();
}

#[test]
fn entities_built_by_the_set_hold_the_actions() {
    let set =
        atoom_policy::Set::compile(&atoom_policy::Bundle::parse(&bundle(PERMIT)).unwrap()).unwrap();
    let e: Entities = set.entities(Vec::new()).unwrap();
    assert!(e.get(&uid("Action", "GET").unwrap()).is_some());
}
