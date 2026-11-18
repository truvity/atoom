//! The spool over real Unix sockets: the contract (`proto/`) is the only way in, both servers are served by `atoom_socket`, plain, and
//! the conformance kit runs through the sockets. What the sluis host adds (peer credentials, the call graph, the emitters of the audit
//! crate) is tested in sluis, with the wrapper that adds it.

#![allow(refining_impl_trait)]
#![allow(clippy::unwrap_used, clippy::expect_used)] // a failed unwrap in a test is the failure

use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::sync::atomic::Ordering;
use std::time::Duration;

use atoom_common::refusal::reason as refusal_reason;
use atoom_socket::{ServerConfig, dial};
use atoom_transport::atoom::transport::v1alpha1::{
    AckRequest, AuditMessage, EmitRequest, ReceiveRequest, TransportServiceClient,
};
use atoom_transport_spool::service::{Consume, Emit};
use atoom_transport_spool::{Config, Spool};
use connectrpc::Router;
use connectrpc::error::ErrorCode;

/// A spool served on both sockets.
struct Up {
    emit: PathBuf,
    receive: PathBuf,
    spool: Spool,
    stop: Vec<tokio::sync::oneshot::Sender<()>>,
}

fn root() -> tempfile::TempDir {
    let d = tempfile::tempdir().unwrap();
    std::fs::set_permissions(d.path(), std::fs::Permissions::from_mode(0o700)).unwrap();
    d
}

async fn up(root: &Path, socks: &Path, cfg: Config) -> Up {
    let spool = Spool::open(root, cfg).unwrap();
    up_with(
        spool.clone(),
        socks,
        Emit::fixed(spool.clone(), "entry@dev").router(),
    )
    .await
}

async fn up_with(spool: Spool, socks: &Path, emit_router: Router) -> Up {
    let mut stop = Vec::new();
    let mut serve = |name: &str, router: Router| {
        let socket = socks.join(name);
        let (tx, rx) = tokio::sync::oneshot::channel::<()>();
        stop.push(tx);
        tokio::spawn(atoom_socket::serve(
            ServerConfig::new(&socket),
            router,
            async move {
                let _ = rx.await;
            },
        ));
        socket
    };
    let emit = serve("emit.sock", emit_router);
    let receive = serve("receive.sock", Consume(spool.clone()).router());
    for s in [&emit, &receive] {
        for _ in 0..200 {
            if s.exists() {
                break;
            }
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
        assert!(s.exists(), "no socket");
    }
    Up {
        emit,
        receive,
        spool,
        stop,
    }
}

impl Up {
    fn down(self) {
        for s in self.stop {
            let _ = s.send(());
        }
    }
}

fn client(socket: &Path) -> TransportServiceClient<atoom_socket::Transport> {
    let c = dial(socket);
    TransportServiceClient::new(c.transport(), c.config())
}

fn msg(uid: &str, body: &[u8]) -> EmitRequest {
    EmitRequest {
        message: AuditMessage {
            uid: uid.into(),
            record: body.to_vec(),
            ..AuditMessage::default()
        }
        .into(),
        ..EmitRequest::default()
    }
}

/// The next batch of a receive stream as (delivery id, emitter, message uid).
macro_rules! next {
    ($st:expr) => {{
        let r = tokio::time::timeout(Duration::from_secs(5), $st.message())
            .await
            .expect("a delivery within 5 s")
            .unwrap()
            .expect("the stream is open");
        r.deliveries()
            .iter()
            .map(|d| {
                (
                    d.delivery_id,
                    d.emitter.to_string(),
                    d.message.uid.to_string(),
                )
            })
            .collect::<Vec<(u64, String, String)>>()
    }};
}

#[tokio::test]
async fn emits_are_durable_ordered_acked_and_replayed_after_a_restart() {
    let (r, s) = (root(), tempfile::tempdir().unwrap());
    let u = up(r.path(), s.path(), Config::default()).await;
    let emit = client(&u.emit);
    for (i, blocking) in [(1, false), (2, true), (3, true)] {
        let mut m = msg(&format!("u{i}"), b"{}");
        m.blocking = blocking;
        let before = u.spool.syncs().file.load(Ordering::SeqCst);
        emit.emit(m).await.unwrap();
        // The answer came after the fsync of the segment.
        assert_eq!(u.spool.syncs().file.load(Ordering::SeqCst), before + 1);
    }
    assert!(
        u.spool.syncs().dir.load(Ordering::SeqCst) >= 1,
        "the segment was not made durable in its directory"
    );
    assert_eq!(u.spool.depth(), 3);

    let recv = client(&u.receive);
    let mut st = recv.receive(ReceiveRequest::default()).await.unwrap();
    let got = next!(st);
    // The emitter is what the host named, not a field of the message.
    assert_eq!(
        got,
        vec![
            (1, "entry@dev".into(), "u1".into()),
            (2, "entry@dev".into(), "u2".into()),
            (3, "entry@dev".into(), "u3".into()),
        ]
    );
    // A second consumer is refused while the first is open.
    let second = recv.receive(ReceiveRequest::default()).await;
    let e = match second {
        Err(e) => e,
        Ok(mut st2) => st2.message().await.unwrap_err(),
    };
    assert_eq!(e.code, ErrorCode::AlreadyExists);

    // A message emitted later arrives on the open stream.
    emit.emit(msg("u4", b"{}")).await.unwrap();
    assert_eq!(next!(st)[0].0, 4);

    // Ack 1 and 2; the rest stays, and an ack of the unknown is harmless.
    recv.ack(AckRequest {
        delivery_ids: vec![2, 1, 77],
        ..AckRequest::default()
    })
    .await
    .unwrap();
    assert_eq!(u.spool.depth(), 2);
    drop(st);

    // The consumer restarts: the unacknowledged come again, under the same ids.
    tokio::time::sleep(Duration::from_millis(100)).await;
    let mut st = recv.receive(ReceiveRequest::default()).await.unwrap();
    let ids: Vec<u64> = next!(st).iter().map(|d| d.0).collect();
    assert_eq!(ids, [3, 4]);
    drop(st);

    // The spool restarts: the same.
    u.down();
    tokio::time::sleep(Duration::from_millis(100)).await;
    let s2 = tempfile::tempdir().unwrap();
    let u = up(r.path(), s2.path(), Config::default()).await;
    let recv = client(&u.receive);
    let mut st = recv.receive(ReceiveRequest::default()).await.unwrap();
    let ids: Vec<u64> = next!(st).iter().map(|d| d.0).collect();
    assert_eq!(ids, [3, 4]);
    u.down();
}

#[tokio::test]
async fn the_consumer_socket_does_not_take_emits_and_the_emit_socket_does_not_deliver() {
    let (r, s) = (root(), tempfile::tempdir().unwrap());
    let u = up(r.path(), s.path(), Config::default()).await;
    // Receive and Ack are not served on the emit socket.
    let e = client(&u.emit)
        .ack(AckRequest::default())
        .await
        .unwrap_err();
    assert_eq!(e.code, ErrorCode::Unimplemented);
    let e = match client(&u.emit).receive(ReceiveRequest::default()).await {
        Err(e) => e,
        Ok(mut st) => st.message().await.unwrap_err(),
    };
    assert_eq!(e.code, ErrorCode::Unimplemented);
    // Emit is not served on the receive socket.
    let e = client(&u.receive).emit(msg("u1", b"{}")).await.unwrap_err();
    assert_eq!(e.code, ErrorCode::Unimplemented);
    u.down();
}

/// The host names the caller: a call it cannot name is refused and nothing is kept, and the name it gives is the emitter.
#[tokio::test]
async fn a_caller_the_host_cannot_name_is_refused_and_nothing_is_kept() {
    let (r, s) = (root(), tempfile::tempdir().unwrap());
    let spool = Spool::open(r.path(), Config::default()).unwrap();
    // A host that knows the caller by a header of the call, as a stand-in for peer credentials.
    let router = Emit::new(spool.clone(), |ctx| {
        ctx.headers()
            .get("x-test-caller")
            .and_then(|v| v.to_str().ok())
            .map(str::to_owned)
    })
    .router();
    let u = up_with(spool, s.path(), router).await;
    let e = client(&u.emit).emit(msg("u1", b"{}")).await.unwrap_err();
    assert_eq!(e.code, ErrorCode::PermissionDenied);
    assert_eq!(refusal_reason(&e).as_deref(), Some("caller"));
    assert_eq!(u.spool.depth(), 0);
    u.down();
}

#[tokio::test]
async fn a_malformed_message_is_refused_with_its_field() {
    let (r, s) = (root(), tempfile::tempdir().unwrap());
    let u = up(r.path(), s.path(), Config::default()).await;
    let c = client(&u.emit);
    for (req, field) in [
        (EmitRequest::default(), "message"),
        (msg("", b"{}"), "message.uid"),
        (msg("u", b""), "message.record"),
    ] {
        let e = c.emit(req).await.unwrap_err();
        assert_eq!(e.code, ErrorCode::InvalidArgument, "{field}");
    }
    let e = c
        .emit(msg("u", &vec![0u8; atoom_transport_spool::MAX_MESSAGE + 1]))
        .await
        .unwrap_err();
    assert_eq!(e.code, ErrorCode::InvalidArgument);
    assert_eq!(u.spool.depth(), 0);
    u.down();
}

#[tokio::test]
async fn a_full_spool_refuses_with_unavailable_and_keeps_every_earlier_message() {
    let (r, s) = (root(), tempfile::tempdir().unwrap());
    let cfg = Config {
        limit: 1500,
        segment: 700,
    };
    let u = up(r.path(), s.path(), cfg).await;
    let c = client(&u.emit);
    let mut taken = 0;
    let refused = loop {
        match c.emit(msg(&format!("u{taken}"), &[b'x'; 200])).await {
            Ok(_) => taken += 1,
            Err(e) => break e,
        }
        assert!(taken < 50, "the bound never held");
    };
    assert_eq!(refused.code, ErrorCode::Unavailable);
    assert!(taken >= 2);
    assert_eq!(u.spool.depth(), taken, "an accepted message was dropped");
    assert!(u.spool.bytes() <= 1500);

    // The writer drains it all: the earlier messages are all there, in order.
    let mut st = client(&u.receive)
        .receive(ReceiveRequest::default())
        .await
        .unwrap();
    let mut seen = Vec::new();
    while seen.len() < taken {
        seen.extend(next!(st));
    }
    let uids: Vec<_> = seen.iter().map(|d| d.2.clone()).collect();
    assert_eq!(
        uids,
        (0..taken).map(|i| format!("u{i}")).collect::<Vec<_>>()
    );
    client(&u.receive)
        .ack(AckRequest {
            delivery_ids: seen.iter().map(|d| d.0).collect(),
            ..AckRequest::default()
        })
        .await
        .unwrap();
    // Room is back.
    c.emit(msg("again", &[b'x'; 200])).await.unwrap();
    u.down();
}

/// The conformance kit, through both sockets: what every transport driver must pass.
#[tokio::test]
async fn the_conformance_kit_passes_through_the_sockets() {
    let (r, s) = (root(), tempfile::tempdir().unwrap());
    let u = up(r.path(), s.path(), Config::default()).await;
    let cases = atoom_transport_conformance::transport_suite(dial(&u.emit), dial(&u.receive)).await;
    let mut out = Vec::new();
    let verdict = atoom_transport_conformance::report(&cases, &mut out);
    print!("{}", String::from_utf8_lossy(&out));
    u.down();
    verdict.unwrap();
}
