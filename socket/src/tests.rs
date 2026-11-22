use std::os::unix::fs::{MetadataExt, PermissionsExt};
use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::Duration;

use atoom_common::atoom::common::v1alpha1::{Refusal, RefusalView};
use atoom_common::refusal;
use connectrpc::client::{CallOptions, call_unary};
use connectrpc::error::ErrorCode;
use connectrpc::{ConnectError, Response, Router, Spec, StreamType, handler_fn};

use super::*;

async fn up(
    cfg: ServerConfig,
) -> (
    tokio::sync::oneshot::Sender<()>,
    tokio::task::JoinHandle<anyhow::Result<()>>,
) {
    let socket = cfg.socket.clone();
    let (stop, stopped) = tokio::sync::oneshot::channel::<()>();
    let task = tokio::spawn(serve(cfg, Router::new(), async {
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

#[tokio::test]
async fn the_socket_is_made_with_the_asked_mode_and_removed_at_the_end() {
    let dir = tempfile::tempdir().unwrap();
    let socket = dir.path().join("s.sock");
    let (stop, task) = up(ServerConfig::new(&socket)).await;
    let mode = std::fs::metadata(&socket).unwrap().permissions().mode() & 0o777;
    assert_eq!(mode, 0o600);
    stop.send(()).unwrap();
    task.await.unwrap().unwrap();
    assert!(
        !socket.exists(),
        "the socket file stays after the server stopped"
    );
}

#[tokio::test]
async fn a_live_listener_is_never_taken_over() {
    let dir = tempfile::tempdir().unwrap();
    let socket = dir.path().join("s.sock");
    let (stop, task) = up(ServerConfig::new(&socket)).await;
    let err = serve(ServerConfig::new(&socket), Router::new(), async {})
        .await
        .unwrap_err();
    assert!(format!("{err:#}").contains("a live listener"), "{err:#}");
    stop.send(()).unwrap();
    task.await.unwrap().unwrap();
}

#[tokio::test]
async fn a_stale_socket_is_replaced() {
    let dir = tempfile::tempdir().unwrap();
    let socket = dir.path().join("s.sock");
    // A socket file nobody listens on: bound, then the listener dropped without removing the file.
    drop(std::os::unix::net::UnixListener::bind(&socket).unwrap());
    assert!(socket.exists());
    let (stop, task) = up(ServerConfig::new(&socket)).await;
    stop.send(()).unwrap();
    task.await.unwrap().unwrap();
}

#[tokio::test]
async fn a_symlink_or_a_plain_file_in_the_way_is_refused() {
    let dir = tempfile::tempdir().unwrap();
    let link = dir.path().join("link.sock");
    std::os::unix::fs::symlink(dir.path().join("elsewhere"), &link).unwrap();
    let err = serve(ServerConfig::new(&link), Router::new(), async {})
        .await
        .unwrap_err();
    assert!(format!("{err:#}").contains("symlink"), "{err:#}");

    let file = dir.path().join("plain");
    std::fs::write(&file, b"x").unwrap();
    let err = serve(ServerConfig::new(&file), Router::new(), async {})
        .await
        .unwrap_err();
    assert!(format!("{err:#}").contains("not a socket"), "{err:#}");
}

#[test]
fn dial_does_not_need_a_server() {
    let c = dial(std::path::Path::new("/nonexistent/atoom.sock"));
    assert_eq!(
        c.socket(),
        Some(std::path::Path::new("/nonexistent/atoom.sock"))
    );
}

// The guard. The messages of the test service are `Refusal` (the one message every atoom library has), so that the socket crate
// needs no port: the handler answers who it saw, in `reason` (the uid) and `field_path` (the pid, or empty).

const WHO: Spec = Spec::client("/atoom.test.v1.Who/Ask", StreamType::Unary);

fn me() -> u32 {
    rustix::process::geteuid().as_raw()
}

/// The router of the test service, and the number of calls that reached its handler.
fn who_router() -> (Router, Arc<AtomicUsize>) {
    let calls = Arc::new(AtomicUsize::new(0));
    let seen = calls.clone();
    let router = Router::new().route(
        "atoom.test.v1.Who",
        "Ask",
        handler_fn(move |ctx: connectrpc::RequestContext, _req: Refusal| {
            seen.fetch_add(1, Ordering::SeqCst);
            let caller = peer(ctx.extensions()).copied();
            async move {
                Response::ok(Refusal {
                    reason: caller.map_or_else(|| "none".to_owned(), |p| p.uid.to_string()),
                    field_path: caller
                        .and_then(|p| p.pid)
                        .map(|p| p.to_string())
                        .unwrap_or_default(),
                    ..Refusal::default()
                })
            }
        }),
    );
    (router, calls)
}

struct Running {
    client: Client,
    calls: Arc<AtomicUsize>,
    stop: tokio::sync::oneshot::Sender<()>,
    task: tokio::task::JoinHandle<anyhow::Result<()>>,
}

impl Running {
    async fn ask(&self) -> Result<Refusal, ConnectError> {
        call_unary::<_, Refusal, RefusalView>(
            &self.client.transport(),
            &self.client.config(),
            WHO,
            Refusal::default(),
            CallOptions::default(),
        )
        .await
        .map(|r| r.into_view().to_owned_message())
    }

    async fn stop(self) {
        self.stop.send(()).unwrap();
        self.task.await.unwrap().unwrap();
    }
}

async fn start(cfg: ServerConfig) -> Running {
    let socket = cfg.socket.clone();
    let (router, calls) = who_router();
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
    Running {
        client: dial(&socket),
        calls,
        stop,
        task,
    }
}

fn config(dir: &tempfile::TempDir, allow: Allow) -> ServerConfig {
    let mut cfg = ServerConfig::new(dir.path().join("g.sock"));
    cfg.allow = allow;
    cfg
}

fn assert_refused_as_caller(err: &ConnectError) {
    assert_eq!(err.code, ErrorCode::PermissionDenied, "{err:?}");
    assert_eq!(
        refusal::reason(err).as_deref(),
        Some(REASON_CALLER),
        "{err:?}"
    );
}

#[tokio::test]
async fn the_default_admits_the_uid_of_the_process_and_hands_it_to_the_handler() {
    let dir = tempfile::tempdir().unwrap();
    let r = start(ServerConfig::new(dir.path().join("g.sock"))).await;
    let answer = r.ask().await.expect("the same uid is admitted");
    assert_eq!(
        answer.reason,
        me().to_string(),
        "the handler saw another peer"
    );
    #[cfg(target_os = "linux")]
    assert_eq!(
        answer.field_path,
        std::process::id().to_string(),
        "the pid of the peer is the caller's"
    );
    assert_eq!(r.calls.load(Ordering::SeqCst), 1);
    r.stop().await;
}

#[tokio::test]
async fn a_listed_uid_is_admitted() {
    let dir = tempfile::tempdir().unwrap();
    let r = start(config(&dir, Allow::Uids(vec![me() + 1, me()]))).await;
    assert_eq!(r.ask().await.unwrap().reason, me().to_string());
    r.stop().await;
}

#[tokio::test]
async fn another_uid_is_refused_with_the_reason_caller_before_the_handler_runs() {
    // The caller is this process; the server is told that only another uid may call, which is what a caller of another uid meets.
    let dir = tempfile::tempdir().unwrap();
    let r = start(config(&dir, Allow::Uids(vec![me() + 1]))).await;
    let err = r.ask().await.expect_err("the uid is not in the set");
    assert_refused_as_caller(&err);
    assert_eq!(
        r.calls.load(Ordering::SeqCst),
        0,
        "a refused call reached the handler"
    );
    r.stop().await;
}

#[tokio::test]
async fn an_empty_set_admits_nobody() {
    let dir = tempfile::tempdir().unwrap();
    let r = start(config(&dir, Allow::Uids(vec![]))).await;
    assert_refused_as_caller(&r.ask().await.expect_err("nobody is admitted"));
    r.stop().await;
}

#[tokio::test]
async fn any_uid_is_the_plain_behaviour_and_still_hands_over_the_peer() {
    let dir = tempfile::tempdir().unwrap();
    let r = start(config(&dir, Allow::Any)).await;
    assert_eq!(r.ask().await.unwrap().reason, me().to_string());
    r.stop().await;
}

#[tokio::test]
async fn the_refusal_does_not_decode_the_message() {
    // A body that is not a message at all: a guard that decoded first would answer invalid_argument.
    let dir = tempfile::tempdir().unwrap();
    let r = start(config(&dir, Allow::Uids(vec![me() + 1]))).await;
    let (status, body) = raw_post(
        r.client.socket().unwrap(),
        "/atoom.test.v1.Who/Ask",
        b"\xff\xff not proto",
    )
    .await;
    assert_eq!(status, 403, "{body}");
    assert!(body.contains("permission_denied"), "{body}");
    assert!(body.contains("atoom.common.v1alpha1.Refusal"), "{body}");
    r.stop().await;
}

/// One Connect unary call with a raw body over HTTP/1.1 on the socket: the status and the body of the answer.
async fn raw_post(socket: &std::path::Path, path: &str, body: &[u8]) -> (u16, String) {
    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    let mut s = tokio::net::UnixStream::connect(socket).await.unwrap();
    let head = format!(
        "POST {path} HTTP/1.1\r\nHost: unix\r\nContent-Type: application/proto\r\nConnect-Protocol-Version: 1\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
        body.len()
    );
    s.write_all(head.as_bytes()).await.unwrap();
    s.write_all(body).await.unwrap();
    let mut out = Vec::new();
    s.read_to_end(&mut out).await.unwrap();
    let text = String::from_utf8_lossy(&out).into_owned();
    let status = text
        .split_whitespace()
        .nth(1)
        .and_then(|c| c.parse().ok())
        .unwrap_or(0);
    (status, text)
}

#[tokio::test]
async fn the_group_and_the_mode_are_applied_before_the_socket_has_its_name() {
    let dir = tempfile::tempdir().unwrap();
    let gid = rustix::process::getegid().as_raw();
    let mut cfg = config(&dir, Allow::SameUid);
    cfg.mode = 0o660;
    cfg.socket_gid = Some(gid);
    let socket = cfg.socket.clone();
    let r = start(cfg).await;
    let md = std::fs::metadata(&socket).unwrap();
    assert_eq!(md.permissions().mode() & 0o777, 0o660);
    assert_eq!(md.gid(), gid);
    r.stop().await;
}

#[tokio::test]
async fn a_group_the_process_is_not_in_is_refused_and_leaves_no_socket() {
    // chown(2) to a group that the caller is not a member of is refused for every uid but root: the check runs for the uid of the CI
    // runner and of a developer. As root nothing is refused, so the test does nothing there (a root run is not a pass of this case).
    if me() == 0 {
        eprintln!("as root a chown to any group succeeds: this case is not exercised");
        return;
    }
    let dir = tempfile::tempdir().unwrap();
    let mut cfg = config(&dir, Allow::SameUid);
    let socket = cfg.socket.clone();
    // A group number that no ordinary account is a member of.
    cfg.socket_gid = Some(rustix::process::getegid().as_raw().wrapping_add(54_321));
    let err = serve(cfg, Router::new(), async {}).await.unwrap_err();
    assert!(format!("{err:#}").contains("socket group"), "{err:#}");
    assert!(!socket.exists(), "no socket is left at the final name");
    let leftovers: Vec<_> = std::fs::read_dir(dir.path()).unwrap().collect();
    assert!(
        leftovers.is_empty(),
        "no temporary socket is left: {leftovers:?}"
    );
}

#[tokio::test]
async fn the_group_zero_is_refused() {
    let dir = tempfile::tempdir().unwrap();
    let mut cfg = config(&dir, Allow::SameUid);
    cfg.socket_gid = Some(0);
    let err = serve(cfg, Router::new(), async {}).await.unwrap_err();
    assert!(format!("{err:#}").contains("group 0"), "{err:#}");
}

#[test]
fn the_version_is_the_stamp_or_the_workspace_version() {
    assert!(!VERSION.is_empty());
    if option_env!("ATOOM_VERSION").is_none() {
        assert_eq!(VERSION, env!("CARGO_PKG_VERSION"));
    }
}

#[test]
fn allow_decides_by_uid() {
    assert!(Allow::SameUid.admits(5, 5));
    assert!(!Allow::SameUid.admits(0, 5), "root is not the same uid");
    assert!(Allow::Uids(vec![0, 9]).admits(9, 5));
    assert!(
        !Allow::Uids(vec![0, 9]).admits(5, 5),
        "the own uid is not implied by a list"
    );
    assert!(Allow::Any.admits(1, 5));
}

// The host's rule (Allow::check) and the observer of refusals (ServerConfig::on_refused).

#[tokio::test]
async fn a_host_rule_admits_from_the_whole_peer_and_the_procedure() {
    let dir = tempfile::tempdir().unwrap();
    let asked = Arc::new(std::sync::Mutex::new(Vec::new()));
    let seen = asked.clone();
    let own = me();
    let cfg = ServerConfig::new(dir.path().join("g.sock")).with_allow(Allow::check(
        move |p: &Peer, procedure: &str| {
            seen.lock()
                .unwrap()
                .push((p.uid, p.pid, procedure.to_owned()));
            if p.uid == own {
                Ok(())
            } else {
                Err("call_graph")
            }
        },
    ));
    let r = start(cfg).await;
    assert_eq!(r.ask().await.unwrap().reason, me().to_string());
    assert_eq!(r.calls.load(Ordering::SeqCst), 1);
    let asked = asked.lock().unwrap().clone();
    assert_eq!(asked.len(), 1, "the rule is asked once per call: {asked:?}");
    assert_eq!(asked[0].0, me());
    #[cfg(target_os = "linux")]
    assert_eq!(asked[0].1, i32::try_from(std::process::id()).ok());
    assert_eq!(asked[0].2, "/atoom.test.v1.Who/Ask");
    r.stop().await;
}

#[tokio::test]
async fn a_host_rule_refuses_with_its_own_reason_before_the_handler_runs_and_the_observer_hears_it()
{
    let dir = tempfile::tempdir().unwrap();
    let heard = Arc::new(std::sync::Mutex::new(Vec::<Refused>::new()));
    let log = heard.clone();
    let cfg = ServerConfig::new(dir.path().join("g.sock"))
        .with_allow(Allow::check(|_: &Peer, _: &str| Err("call_graph")))
        .with_on_refused(move |r: &Refused| log.lock().unwrap().push(r.clone()));
    let r = start(cfg).await;
    let err = r.ask().await.expect_err("the rule refuses every call");
    assert_eq!(err.code, ErrorCode::PermissionDenied, "{err:?}");
    assert_eq!(
        refusal::reason(&err).as_deref(),
        Some("call_graph"),
        "{err:?}"
    );
    assert_eq!(
        r.calls.load(Ordering::SeqCst),
        0,
        "a refused call reached the handler"
    );
    // The message is not decoded either: a body that is no message is still a refusal, not invalid_argument.
    let (status, body) = raw_post(
        r.client.socket().unwrap(),
        "/atoom.test.v1.Who/Ask",
        b"\xff\xff not proto",
    )
    .await;
    assert_eq!(status, 403, "{body}");
    let heard = heard.lock().unwrap().clone();
    assert_eq!(heard.len(), 2, "{heard:?}");
    assert_eq!(heard[0].reason, "call_graph");
    assert_eq!(heard[0].peer.map(|p| p.uid), Some(me()));
    assert_eq!(heard[0].procedure, "/atoom.test.v1.Who/Ask");
    r.stop().await;
}

#[tokio::test]
async fn an_empty_reason_of_a_host_rule_is_caller() {
    let dir = tempfile::tempdir().unwrap();
    let cfg = ServerConfig::new(dir.path().join("g.sock"))
        .with_allow(Allow::check(|_: &Peer, _: &str| Err("")));
    let r = start(cfg).await;
    assert_refused_as_caller(&r.ask().await.expect_err("refused"));
    r.stop().await;
}

#[tokio::test]
async fn the_observer_hears_a_uid_outside_the_set() {
    let dir = tempfile::tempdir().unwrap();
    let heard = Arc::new(AtomicUsize::new(0));
    let n = heard.clone();
    let cfg = config(&dir, Allow::Uids(vec![me() + 1])).with_on_refused(move |r: &Refused| {
        assert_eq!(r.reason, REASON_CALLER);
        n.fetch_add(1, Ordering::SeqCst);
    });
    let r = start(cfg).await;
    assert_refused_as_caller(&r.ask().await.expect_err("refused"));
    assert!(r.ask().await.is_err());
    assert_eq!(heard.load(Ordering::SeqCst), 2);
    // An admitted call is not a refusal.
    r.stop().await;
    let dir = tempfile::tempdir().unwrap();
    let quiet = Arc::new(AtomicUsize::new(0));
    let q = quiet.clone();
    let r = start(
        ServerConfig::new(dir.path().join("g.sock")).with_on_refused(move |_: &Refused| {
            q.fetch_add(1, Ordering::SeqCst);
        }),
    )
    .await;
    r.ask().await.unwrap();
    assert_eq!(quiet.load(Ordering::SeqCst), 0);
    r.stop().await;
}

#[test]
fn a_host_rule_is_asked_by_admits_with_the_uid() {
    let rule = Allow::check(|p: &Peer, _: &str| if p.uid == 7 { Ok(()) } else { Err("x") });
    assert!(rule.admits(7, 5));
    assert!(!rule.admits(5, 5), "the own uid is not implied by a rule");
    assert_eq!(rule.clone(), rule, "a rule is equal to itself");
    assert_ne!(
        Allow::check(|_: &Peer, _: &str| Ok(())),
        Allow::check(|_: &Peer, _: &str| Ok(())),
        "two rules are not equal because they decide alike"
    );
}

/// A name a host's interceptor puts in the request's extensions.
#[derive(Clone, Debug)]
struct Named(String);

/// The host's interceptor of the tests: names the peer, and counts the calls it saw.
struct Namer(Arc<AtomicUsize>);

#[connectrpc::interceptor::async_trait]
impl connectrpc::interceptor::Interceptor for Namer {
    async fn intercept_unary(
        &self,
        mut req: connectrpc::interceptor::UnaryRequest,
        next: connectrpc::interceptor::Next<'_>,
    ) -> Result<connectrpc::interceptor::UnaryResponse, ConnectError> {
        self.0.fetch_add(1, Ordering::SeqCst);
        let uid = peer(req.ctx.extensions()).map(|p| p.uid);
        if let Some(uid) = uid {
            req.ctx
                .extensions_mut()
                .insert(Named(format!("actor-{uid}")));
        }
        next.run(req).await
    }

    async fn intercept_streaming(
        &self,
        req: connectrpc::interceptor::StreamRequest,
        inbound: connectrpc::interceptor::PayloadStream,
        next: connectrpc::interceptor::NextStream<'_>,
    ) -> Result<connectrpc::interceptor::StreamResponse, ConnectError> {
        next.run(req, inbound).await
    }
}

async fn start_named(cfg: ServerConfig, seen: Arc<AtomicUsize>) -> Running {
    let socket = cfg.socket.clone();
    let calls = Arc::new(AtomicUsize::new(0));
    let counted = calls.clone();
    let router = Router::new().route(
        "atoom.test.v1.Who",
        "Ask",
        handler_fn(move |ctx: connectrpc::RequestContext, _req: Refusal| {
            counted.fetch_add(1, Ordering::SeqCst);
            let name = ctx.extensions().get::<Named>().map(|n| n.0.clone());
            async move {
                Response::ok(Refusal {
                    reason: name.unwrap_or_else(|| "unnamed".to_owned()),
                    ..Refusal::default()
                })
            }
        }),
    );
    let (stop, stopped) = tokio::sync::oneshot::channel::<()>();
    let task = tokio::spawn(serve(cfg.with_interceptor(Namer(seen)), router, async {
        let _ = stopped.await;
    }));
    for _ in 0..100 {
        if socket.exists() {
            break;
        }
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
    Running {
        client: dial(&socket),
        calls,
        stop,
        task,
    }
}

#[tokio::test]
async fn a_host_interceptor_runs_after_the_guard_and_hands_its_handlers_what_it_knows() {
    let dir = tempfile::tempdir().unwrap();
    let seen = Arc::new(AtomicUsize::new(0));
    let r = start_named(ServerConfig::new(dir.path().join("g.sock")), seen.clone()).await;
    assert_eq!(r.ask().await.unwrap().reason, format!("actor-{}", me()));
    assert_eq!(seen.load(Ordering::SeqCst), 1);
    r.stop().await;
    // A refused call never reaches the host's interceptor.
    let dir = tempfile::tempdir().unwrap();
    let seen = Arc::new(AtomicUsize::new(0));
    let r = start_named(config(&dir, Allow::Uids(vec![me() + 1])), seen.clone()).await;
    assert_refused_as_caller(&r.ask().await.expect_err("refused"));
    assert_eq!(
        seen.load(Ordering::SeqCst),
        0,
        "the interceptor saw a refused call"
    );
    assert_eq!(r.calls.load(Ordering::SeqCst), 0);
    r.stop().await;
}
