//! The deadline and the correlation id of a call: sent by the client, read by the server, handed down by a handler. The messages of the
//! test service are `Refusal`, so that the socket crate needs no port: the handler answers what it saw, in `reason` (the id) and
//! `field_path` (the id read from the task-local, then the milliseconds left, or `none`).

use std::path::{Path, PathBuf};
use std::time::Duration;

use atoom_common::atoom::common::v1alpha1::{Refusal, RefusalView};
use connectrpc::client::{CallOptions, call_unary};
use connectrpc::error::ErrorCode;
use connectrpc::{ConnectError, Response, Router, Spec, StreamType, handler_fn};

use super::call::{self, Call, HEADER_CORRELATION, valid_id};
use super::*;

const SEE: Spec = Spec::client("/atoom.test.v1.Call/See", StreamType::Unary);
const RELAY: Spec = Spec::client("/atoom.test.v1.Call/Relay", StreamType::Unary);
const SLOW: Spec = Spec::client("/atoom.test.v1.Call/Slow", StreamType::Unary);

/// What the handler of `See` saw.
#[derive(Debug, PartialEq)]
struct Seen {
    /// The id in the request extensions.
    id: Option<String>,
    /// The id of the task-local.
    ambient: Option<String>,
    /// The milliseconds left of the deadline the handler holds.
    left_ms: Option<u128>,
}

fn answer(ctx: &connectrpc::RequestContext) -> Refusal {
    let id = call::correlation_of(ctx.extensions());
    let left = ctx.time_remaining().map(|d| d.as_millis());
    Refusal {
        reason: id.unwrap_or_else(|| "none".to_owned()),
        field_path: format!(
            "{}|{}",
            call::correlation().unwrap_or_else(|| "none".to_owned()),
            left.map_or_else(|| "none".to_owned(), |l| l.to_string())
        ),
        ..Refusal::default()
    }
}

fn seen(r: &Refusal) -> Seen {
    let (ambient, left) = r.field_path.split_once('|').unwrap();
    let some = |s: &str| (s != "none").then(|| s.to_owned());
    Seen {
        id: some(&r.reason),
        ambient: some(ambient),
        left_ms: left.parse().ok(),
    }
}

/// A server with `See` (answers what it saw), `Relay` (calls `See` on `next` with a client that has no deadline and no id of its own)
/// and `Slow` (waits two seconds).
async fn up(
    socket: PathBuf,
    allow: Allow,
    next: Option<PathBuf>,
) -> (
    tokio::sync::oneshot::Sender<()>,
    tokio::task::JoinHandle<anyhow::Result<()>>,
) {
    let router = Router::new()
        .route(
            "atoom.test.v1.Call",
            "See",
            handler_fn(|ctx: connectrpc::RequestContext, _r: Refusal| {
                let a = answer(&ctx);
                async move { Response::ok(a) }
            }),
        )
        .route(
            "atoom.test.v1.Call",
            "Slow",
            handler_fn(|_ctx: connectrpc::RequestContext, _r: Refusal| async move {
                tokio::time::sleep(Duration::from_secs(2)).await;
                Response::ok(Refusal::default())
            }),
        )
        .route(
            "atoom.test.v1.Call",
            "Relay",
            handler_fn(move |_ctx: connectrpc::RequestContext, _r: Refusal| {
                let next = next.clone();
                async move {
                    let c = dial(next.as_deref().expect("a relay has a next hop"));
                    let r = see(&c, CallOptions::default()).await?;
                    Response::ok(r)
                }
            }),
        );
    let mut cfg = ServerConfig::new(&socket);
    cfg.allow = allow;
    let (stop, stopped) = tokio::sync::oneshot::channel::<()>();
    let task = tokio::spawn(serve(cfg, router, async {
        let _ = stopped.await;
    }));
    for _ in 0..100 {
        if socket.exists() {
            break;
        }
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
    (stop, task)
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

async fn see(c: &Client, opts: CallOptions) -> Result<Refusal, ConnectError> {
    ask(c, SEE, opts).await
}

fn socket(dir: &tempfile::TempDir, name: &str) -> PathBuf {
    dir.path().join(name)
}

async fn stop(
    s: (
        tokio::sync::oneshot::Sender<()>,
        tokio::task::JoinHandle<anyhow::Result<()>>,
    ),
) {
    s.0.send(()).unwrap();
    s.1.await.unwrap().unwrap();
}

fn with_id(id: &str) -> CallOptions {
    CallOptions::default().with_header(
        http::HeaderName::from_static(HEADER_CORRELATION),
        http::HeaderValue::from_str(id).unwrap(),
    )
}

#[tokio::test]
async fn a_call_outside_a_scope_carries_nothing_and_the_server_gives_it_its_default_deadline() {
    let dir = tempfile::tempdir().unwrap();
    let s = up(socket(&dir, "a.sock"), Allow::SameUid, None).await;
    let r = see(&dial(&socket(&dir, "a.sock")), CallOptions::default())
        .await
        .unwrap();
    let got = seen(&r);
    assert_eq!(got.id, None);
    assert_eq!(got.ambient, None);
    let left = got.left_ms.expect("the default deadline of the server");
    assert!((29_000..=30_000).contains(&left), "{left} ms left");
    stop(s).await;
}

#[tokio::test]
async fn the_client_sends_the_id_and_the_deadline_left_of_its_scope() {
    let dir = tempfile::tempdir().unwrap();
    let s = up(socket(&dir, "a.sock"), Allow::SameUid, None).await;
    let c = dial(&socket(&dir, "a.sock"));
    let scoped = Call::new()
        .with_correlation("req-42")
        .with_timeout(Duration::from_secs(5));
    let r = call::scope(scoped, see(&c, CallOptions::default()))
        .await
        .unwrap();
    let got = seen(&r);
    assert_eq!(got.id.as_deref(), Some("req-42"), "the extension");
    assert_eq!(got.ambient.as_deref(), Some("req-42"), "the task-local");
    let left = got.left_ms.expect("the deadline of the scope");
    assert!((3_000..=5_000).contains(&left), "{left} ms left of 5000");
    stop(s).await;
}

#[tokio::test]
async fn a_handler_hands_the_id_and_what_is_left_of_the_deadline_to_the_call_it_makes() {
    let dir = tempfile::tempdir().unwrap();
    let b = up(socket(&dir, "b.sock"), Allow::SameUid, None).await;
    let a = up(
        socket(&dir, "a.sock"),
        Allow::SameUid,
        Some(socket(&dir, "b.sock")),
    )
    .await;
    let c = dial(&socket(&dir, "a.sock"));
    // The caller of the first hop says an id and a deadline on the wire, as another process would.
    let opts = with_id("chain-1").with_timeout(Duration::from_secs(4));
    let r = ask(&c, RELAY, opts).await.unwrap();
    let got = seen(&r);
    assert_eq!(got.id.as_deref(), Some("chain-1"));
    assert_eq!(got.ambient.as_deref(), Some("chain-1"));
    let left = got.left_ms.expect("the second hop has a deadline");
    assert!(
        (1..=4_000).contains(&left),
        "the second hop has what is left of 4000, not more and not the server's default of 30000: {left}"
    );
    stop(a).await;
    stop(b).await;
}

#[tokio::test]
async fn a_server_that_admits_any_uid_hands_the_call_down_too() {
    let dir = tempfile::tempdir().unwrap();
    let s = up(socket(&dir, "a.sock"), Allow::Any, None).await;
    let c = dial(&socket(&dir, "a.sock"));
    let r = see(&c, with_id("any-1").with_timeout(Duration::from_secs(3)))
        .await
        .unwrap();
    let got = seen(&r);
    assert_eq!(got.ambient.as_deref(), Some("any-1"));
    assert!(got.left_ms.unwrap() <= 3_000);
    stop(s).await;
}

#[tokio::test]
async fn a_callee_never_extends_the_deadline_of_its_caller() {
    let dir = tempfile::tempdir().unwrap();
    let s = up(socket(&dir, "a.sock"), Allow::SameUid, None).await;
    let c = dial(&socket(&dir, "a.sock"));
    let scoped = Call::new().with_timeout(Duration::from_secs(5));
    // A longer timeout of the call itself does not extend the scope's.
    let longer = call::scope(
        scoped.clone(),
        see(
            &c,
            CallOptions::default().with_timeout(Duration::from_secs(60)),
        ),
    )
    .await
    .unwrap();
    assert!(seen(&longer).left_ms.unwrap() <= 5_000, "extended");
    // A shorter one stays.
    let shorter = call::scope(
        scoped,
        see(
            &c,
            CallOptions::default().with_timeout(Duration::from_millis(500)),
        ),
    )
    .await
    .unwrap();
    assert!(seen(&shorter).left_ms.unwrap() <= 500, "not kept");
    stop(s).await;
}

#[tokio::test]
async fn an_invalid_id_is_dropped_by_the_server_and_by_the_client() {
    let dir = tempfile::tempdir().unwrap();
    let s = up(socket(&dir, "a.sock"), Allow::SameUid, None).await;
    let c = dial(&socket(&dir, "a.sock"));
    // On the wire: a header value with a space is not an id.
    let r = see(&c, with_id("not an id")).await.unwrap();
    assert_eq!(seen(&r).id, None, "the server read a bad id");
    assert_eq!(seen(&r).ambient, None);
    // In a scope: the id is refused when it is built, and the struct field is checked again when the call goes out.
    assert_eq!(Call::new().with_correlation("a b").correlation, None);
    let forced = Call {
        correlation: Some("a b".into()),
        deadline: None,
    };
    let r = call::scope(forced, see(&c, CallOptions::default()))
        .await
        .unwrap();
    assert_eq!(seen(&r).id, None, "the client sent a bad id");
    // Too long: 65 characters.
    let long = "x".repeat(65);
    let r = see(&c, with_id(&long)).await.unwrap();
    assert_eq!(seen(&r).id, None);
    // The longest valid one.
    let longest = "x".repeat(64);
    let r = see(&c, with_id(&longest)).await.unwrap();
    assert_eq!(seen(&r).id.as_deref(), Some(longest.as_str()));
    stop(s).await;
}

#[test]
fn an_id_is_one_to_sixty_four_characters_of_letters_digits_dash_and_underscore() {
    assert!(valid_id("a"));
    assert!(valid_id("0f8fad5b-d9cb-469f-a165-70867728950e"));
    assert!(valid_id("Req_1-A"));
    assert!(!valid_id(""));
    assert!(!valid_id(&"x".repeat(65)));
    for bad in ["a b", "a/b", "a.b", "a\nb", "é", "a:b"] {
        assert!(!valid_id(bad), "{bad:?}");
    }
}

#[tokio::test]
async fn the_server_honours_the_deadline_of_the_caller() {
    let dir = tempfile::tempdir().unwrap();
    let s = up(socket(&dir, "a.sock"), Allow::SameUid, None).await;
    let c = dial(&socket(&dir, "a.sock"));
    let started = std::time::Instant::now();
    // The handler waits two seconds; the caller allows 300 ms, through its scope.
    let err = call::scope(
        Call::new().with_timeout(Duration::from_millis(300)),
        ask(&c, SLOW, CallOptions::default()),
    )
    .await
    .expect_err("the deadline passed");
    assert_eq!(err.code, ErrorCode::DeadlineExceeded, "{err:?}");
    assert!(started.elapsed() < Duration::from_millis(1_500));
    stop(s).await;
}

#[tokio::test]
async fn a_refused_caller_is_refused_before_its_headers_are_read() {
    let dir = tempfile::tempdir().unwrap();
    let me = rustix::process::geteuid().as_raw();
    let s = up(socket(&dir, "a.sock"), Allow::Uids(vec![me + 1]), None).await;
    let c = dial(&socket(&dir, "a.sock"));
    let err = see(&c, with_id("refused-1")).await.expect_err("refused");
    assert_eq!(err.code, ErrorCode::PermissionDenied);
    stop(s).await;
}

#[tokio::test]
async fn outside_a_handler_and_a_scope_nothing_is_current() {
    assert_eq!(call::current(), Call::new());
    assert_eq!(call::correlation(), None);
    let inside = call::scope(Call::new().with_correlation("x-1"), async {
        call::correlation()
    })
    .await;
    assert_eq!(inside.as_deref(), Some("x-1"));
    let _ = Path::new("");
}
