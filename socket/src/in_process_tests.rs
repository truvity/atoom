//! A router called in the same process, with no socket (`in_process`, record 0009). The test service speaks `Refusal` both ways, as
//! the tests of the call context do, so that the socket crate needs no port: the handler of `See` answers what it saw (the id of the
//! request, the id of the task-local, the milliseconds left, and whether it knows a peer). The generated clients of the ports over
//! the same helper are tested in the drivers (`store/memory/tests/in_process.rs`).

use std::path::Path;
use std::time::{Duration, Instant};

use atoom_common::atoom::common::v1alpha1::{Refusal, RefusalView};
use atoom_common::refusal;
use connectrpc::client::{CallOptions, call_unary};
use connectrpc::error::ErrorCode;
use connectrpc::interceptor::{
    Interceptor, Next, NextStream, PayloadStream, StreamRequest, StreamResponse, UnaryRequest,
    UnaryResponse, async_trait,
};
use connectrpc::{ConnectError, RequestContext, Response, Router, Spec, StreamType, handler_fn};

use super::call::{self, Call, HEADER_CORRELATION};
use super::*;

const SEE: Spec = Spec::client("/atoom.test.v1.Process/See", StreamType::Unary);
const RELAY: Spec = Spec::client("/atoom.test.v1.Process/Relay", StreamType::Unary);
const SLOW: Spec = Spec::client("/atoom.test.v1.Process/Slow", StreamType::Unary);
const FAIL: Spec = Spec::client("/atoom.test.v1.Process/Fail", StreamType::Unary);

/// What the handler of `See` saw.
#[derive(Debug, PartialEq)]
struct Seen {
    /// The id in the request extensions.
    id: Option<String>,
    /// The id of the task-local.
    ambient: Option<String>,
    /// The milliseconds left of the deadline the handler holds.
    left_ms: Option<u128>,
    /// Whether the call had a peer.
    peer: bool,
}

fn answer(ctx: &RequestContext) -> Refusal {
    let id = call::correlation_of(ctx.extensions());
    let left = ctx.time_remaining().map(|d| d.as_millis());
    Refusal {
        reason: id.unwrap_or_else(|| "none".to_owned()),
        field_path: format!(
            "{}|{}|{}",
            call::correlation().unwrap_or_else(|| "none".to_owned()),
            left.map_or_else(|| "none".to_owned(), |l| l.to_string()),
            peer(ctx.extensions()).is_some()
        ),
        ..Refusal::default()
    }
}

fn seen(r: &Refusal) -> Seen {
    let mut parts = r.field_path.split('|');
    let (ambient, left, peer) = (
        parts.next().unwrap(),
        parts.next().unwrap(),
        parts.next().unwrap(),
    );
    let some = |s: &str| (s != "none").then(|| s.to_owned());
    Seen {
        id: some(&r.reason),
        ambient: some(ambient),
        left_ms: left.parse().ok(),
        peer: peer == "true",
    }
}

/// The router of the test service. `Relay` calls `See` on `next` with a client that has no deadline and no id of its own.
fn router(next: Option<Client>) -> Router {
    Router::new()
        .route(
            "atoom.test.v1.Process",
            "See",
            handler_fn(|ctx: RequestContext, _r: Refusal| {
                let a = answer(&ctx);
                async move { Response::ok(a) }
            }),
        )
        .route(
            "atoom.test.v1.Process",
            "Slow",
            handler_fn(|_ctx: RequestContext, _r: Refusal| async move {
                tokio::time::sleep(Duration::from_secs(2)).await;
                Response::ok(Refusal::default())
            }),
        )
        .route(
            "atoom.test.v1.Process",
            "Fail",
            handler_fn(|_ctx: RequestContext, _r: Refusal| async move {
                Err::<Response<Refusal>, ConnectError>(refusal::new(
                    ErrorCode::NotFound,
                    "absent",
                    "name",
                ))
            }),
        )
        .route(
            "atoom.test.v1.Process",
            "Relay",
            handler_fn(move |_ctx: RequestContext, _r: Refusal| {
                let next = next.clone();
                async move {
                    let next = next.expect("a relay has a next hop");
                    Response::ok(ask(&next, SEE, CallOptions::default()).await?)
                }
            }),
        )
}

async fn ask(c: &Client, spec: Spec, opts: CallOptions) -> Result<Refusal, ConnectError> {
    call_unary::<_, Refusal, RefusalView>(
        &c.transport(),
        &c.config(),
        spec,
        Refusal::default(),
        opts,
    )
    .await
    .map(|r| r.into_view().to_owned_message())
}

async fn see(c: &Client) -> Seen {
    seen(&ask(c, SEE, CallOptions::default()).await.unwrap())
}

fn with_id(id: &str) -> CallOptions {
    CallOptions::default().with_header(
        http::HeaderName::from_static(HEADER_CORRELATION),
        http::HeaderValue::from_str(id).unwrap(),
    )
}

#[tokio::test]
async fn a_call_reaches_the_handler_and_the_answer_comes_back_with_no_socket() {
    let c = in_process(router(None));
    assert_eq!(c.socket(), None, "a client of a process has no socket");
    let got = see(&c).await;
    assert_eq!(got.id, None);
    assert_eq!(got.ambient, None);
    // The default deadline of a call that carries none is the server's: 30 seconds.
    let left = got.left_ms.expect("the default deadline");
    assert!((29_000..=30_000).contains(&left), "{left} ms left");
}

#[tokio::test]
async fn the_error_code_and_the_refusal_reason_are_kept() {
    let c = in_process(router(None));
    let err = ask(&c, FAIL, CallOptions::default())
        .await
        .expect_err("the handler refuses");
    assert_eq!(err.code, ErrorCode::NotFound, "{err:?}");
    assert_eq!(refusal::reason(&err).as_deref(), Some("absent"));
    // A method that is not served is `unimplemented`, as on a socket.
    let missing = Spec::client("/atoom.test.v1.Process/Nothing", StreamType::Unary);
    let err = ask(&c, missing, CallOptions::default())
        .await
        .expect_err("not served");
    assert_eq!(err.code, ErrorCode::Unimplemented, "{err:?}");
}

#[tokio::test]
async fn the_id_and_the_deadline_of_the_scope_reach_the_handler() {
    let c = in_process(router(None));
    let scoped = Call::new()
        .with_correlation("req-42")
        .with_timeout(Duration::from_secs(5));
    let got = seen(
        &call::scope(scoped, ask(&c, SEE, CallOptions::default()))
            .await
            .unwrap(),
    );
    assert_eq!(got.id.as_deref(), Some("req-42"), "the extension");
    assert_eq!(got.ambient.as_deref(), Some("req-42"), "the task-local");
    let left = got.left_ms.expect("the deadline of the scope");
    assert!((3_000..=5_000).contains(&left), "{left} ms left of 5000");
}

#[tokio::test]
async fn a_handler_hands_the_id_and_what_is_left_of_the_deadline_to_the_call_it_makes() {
    // The second hop is a router in process too, and the first hop's client has no deadline and no id of its own.
    let second = in_process(router(None));
    let first = in_process(router(Some(second)));
    let opts = with_id("chain-1").with_timeout(Duration::from_secs(4));
    let got = seen(&ask(&first, RELAY, opts).await.unwrap());
    assert_eq!(got.id.as_deref(), Some("chain-1"));
    assert_eq!(got.ambient.as_deref(), Some("chain-1"));
    let left = got.left_ms.expect("the second hop has a deadline");
    assert!(
        (1..=4_000).contains(&left),
        "what is left of 4000, not the default of 30000: {left}"
    );
}

#[tokio::test]
async fn a_callee_never_extends_the_deadline_of_its_caller() {
    let c = in_process(router(None));
    let scoped = Call::new().with_timeout(Duration::from_secs(5));
    let longer = call::scope(
        scoped.clone(),
        ask(
            &c,
            SEE,
            CallOptions::default().with_timeout(Duration::from_secs(60)),
        ),
    )
    .await
    .unwrap();
    assert!(seen(&longer).left_ms.unwrap() <= 5_000, "extended");
    let shorter = call::scope(
        scoped,
        ask(
            &c,
            SEE,
            CallOptions::default().with_timeout(Duration::from_millis(500)),
        ),
    )
    .await
    .unwrap();
    assert!(seen(&shorter).left_ms.unwrap() <= 500, "not kept");
}

#[tokio::test]
async fn an_invalid_id_is_dropped() {
    let c = in_process(router(None));
    let r = ask(&c, SEE, with_id("not an id")).await.unwrap();
    assert_eq!(seen(&r).id, None);
    assert_eq!(seen(&r).ambient, None);
    let forced = Call {
        correlation: Some("a b".into()),
        deadline: None,
    };
    let r = call::scope(forced, ask(&c, SEE, CallOptions::default()))
        .await
        .unwrap();
    assert_eq!(seen(&r).id, None, "the client sent a bad id");
}

#[tokio::test]
async fn the_deadline_of_the_scope_cancels_the_handler() {
    let c = in_process(router(None));
    let started = Instant::now();
    // The handler waits two seconds; the caller allows 300 ms.
    let err = call::scope(
        Call::new().with_timeout(Duration::from_millis(300)),
        ask(&c, SLOW, CallOptions::default()),
    )
    .await
    .expect_err("the deadline passed");
    assert_eq!(err.code, ErrorCode::DeadlineExceeded, "{err:?}");
    assert!(started.elapsed() < Duration::from_millis(1_500));
}

#[tokio::test]
async fn the_default_deadline_is_the_hosts_to_set() {
    let cfg = InProcessConfig::new().with_default_deadline(Duration::from_millis(200));
    let c = in_process_with(router(None), cfg);
    let err = ask(&c, SLOW, CallOptions::default())
        .await
        .expect_err("the default deadline passed");
    assert_eq!(err.code, ErrorCode::DeadlineExceeded, "{err:?}");
}

/// A host rule that admits a call only when it names who it is: the shape of a rule that a host writes in place of the socket guard.
struct NeedsId;

#[async_trait]
impl Interceptor for NeedsId {
    async fn intercept_unary(
        &self,
        req: UnaryRequest,
        next: Next<'_>,
    ) -> Result<UnaryResponse, ConnectError> {
        if req.ctx.headers().get("x-caller").is_none() {
            return Err(refusal::new(ErrorCode::PermissionDenied, "caller", ""));
        }
        next.run(req).await
    }

    async fn intercept_streaming(
        &self,
        req: StreamRequest,
        inbound: PayloadStream,
        next: NextStream<'_>,
    ) -> Result<StreamResponse, ConnectError> {
        if req.ctx.headers().get("x-caller").is_none() {
            return Err(refusal::new(ErrorCode::PermissionDenied, "caller", ""));
        }
        next.run(req, inbound).await
    }
}

#[tokio::test]
async fn there_is_no_peer_and_the_hosts_interceptor_decides_who_may_call() {
    let c = in_process_with(
        router(None),
        InProcessConfig::new().with_interceptor(NeedsId),
    );
    let err = ask(&c, SEE, CallOptions::default())
        .await
        .expect_err("the host refused");
    assert_eq!(err.code, ErrorCode::PermissionDenied, "{err:?}");
    assert_eq!(refusal::reason(&err).as_deref(), Some("caller"));
    let named = CallOptions::default().with_header(
        http::HeaderName::from_static("x-caller"),
        http::HeaderValue::from_static("session"),
    );
    let got = seen(&ask(&c, SEE, named).await.unwrap());
    // Admitted, and still no peer: the kernel has nothing to say about a caller in the same process.
    assert!(!got.peer);
}

/// What a consumer's configuration does: one value says a socket or a process, and the rest of the code is the same.
fn connect(socket: Option<&Path>, router: Router) -> Client {
    match socket {
        Some(s) => dial(s),
        None => in_process(router),
    }
}

#[tokio::test]
async fn a_socket_and_a_process_are_one_client_chosen_by_configuration() {
    let dir = tempfile::tempdir().unwrap();
    let socket = dir.path().join("p.sock");
    let (stop, stopped) = tokio::sync::oneshot::channel::<()>();
    let server = tokio::spawn(serve(ServerConfig::new(&socket), router(None), async {
        let _ = stopped.await;
    }));
    for _ in 0..100 {
        if socket.exists() {
            break;
        }
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
    let scoped = || {
        Call::new()
            .with_correlation("same-1")
            .with_timeout(Duration::from_secs(5))
    };
    let mut answers = Vec::new();
    for c in [
        connect(Some(&socket), router(None)),
        connect(None, router(None)),
    ] {
        let r = call::scope(scoped(), ask(&c, SEE, CallOptions::default()))
            .await
            .unwrap();
        let got = seen(&r);
        assert_eq!(got.id.as_deref(), Some("same-1"));
        assert_eq!(got.ambient.as_deref(), Some("same-1"));
        assert!(got.left_ms.unwrap() <= 5_000);
        let err = ask(&c, FAIL, CallOptions::default()).await.unwrap_err();
        answers.push((err.code, refusal::reason(&err), got.peer));
    }
    // The same error on both; the one difference is the peer, which only a socket has.
    assert_eq!(answers[0].0, answers[1].0);
    assert_eq!(answers[0].1, answers[1].1);
    assert_eq!(
        answers.iter().map(|a| a.2).collect::<Vec<_>>(),
        [true, false]
    );
    stop.send(()).unwrap();
    server.await.unwrap().unwrap();
}

/// The cost of one call in process, for the record (`cargo test --release -p atoom-socket the_cost -- --nocapture`). The bound is
/// loose on purpose: it catches an accident (a connection per call, a copy of the router), not a slow machine or a debug build.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn the_cost_of_a_call_in_process_is_small() {
    let c = in_process(router(None));
    for _ in 0..200 {
        ask(&c, SEE, CallOptions::default()).await.unwrap();
    }
    let n: u32 = 2000;
    let t = Instant::now();
    for _ in 0..n {
        ask(&c, SEE, CallOptions::default()).await.unwrap();
    }
    let per = t.elapsed() / n;
    println!("in-process call: {per:?} per call over {n} calls");
    assert!(per < Duration::from_millis(5), "{per:?} per call");
}
