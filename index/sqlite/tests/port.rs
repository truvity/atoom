//! The index port over real Unix sockets: the contract (`proto/`) is the only way in, both sides are served by `atoom_socket`, plain, the
//! read side has no way to write, and the conformance kit runs through the sockets.

#![allow(refining_impl_trait)]
#![allow(clippy::unwrap_used, clippy::expect_used)] // a failed unwrap in a test is the failure

use std::os::unix::fs::PermissionsExt;
use std::path::Path;

use atoom_common::refusal::reason as refusal_reason;
use atoom_index::atoom::index::v1alpha1::{
    ApplyRequest, Cursor, GetCursorRequest, IndexReadServiceClient, IndexServiceClient, LogRef,
    Partition, Row, SearchRequest,
};
use atoom_index_sqlite::service::{Read, Write};
use atoom_index_sqlite::{Index, suite};
use atoom_socket::{ServerConfig, dial};
use connectrpc::Router;
use connectrpc::error::ErrorCode;

/// Serves one side on a socket.
fn serve(socket: &Path, router: Router) -> tokio::sync::oneshot::Sender<()> {
    let (tx, rx) = tokio::sync::oneshot::channel::<()>();
    tokio::spawn(atoom_socket::serve(
        ServerConfig::new(socket),
        router,
        async move {
            let _ = rx.await;
        },
    ));
    tx
}

async fn up(socket: &Path) {
    for _ in 0..200 {
        if socket.exists() {
            return;
        }
        tokio::time::sleep(std::time::Duration::from_millis(10)).await;
    }
    panic!("no socket");
}

fn dir() -> tempfile::TempDir {
    let d = tempfile::tempdir().unwrap();
    std::fs::set_permissions(d.path(), std::fs::Permissions::from_mode(0o750)).unwrap();
    d
}

fn row(n: u8) -> Row {
    Row {
        tenant: "acme".into(),
        hash: vec![n; 48],
        seq: i64::from(n),
        time_ms: i64::from(n) * 10,
        action: "token.exchange".into(),
        status_code: "ok".into(),
        record_id: format!("rec-{n}"),
        record: b"{}".to_vec(),
        ..Row::default()
    }
}

fn log() -> LogRef {
    LogRef {
        project: "sluis".into(),
        kind: "audit".into(),
        log: "main".into(),
        ..LogRef::default()
    }
}

fn search() -> SearchRequest {
    SearchRequest {
        partitions: vec![Partition {
            project: "sluis".into(),
            kind: "audit".into(),
            ..Partition::default()
        }],
        tenants: vec!["acme".into()],
        ..SearchRequest::default()
    }
}

/// The backend suite: word search matches words without stemming, folds diacritics, finds a prefix and no substring, and reaches only
/// the searchable columns.
#[test]
fn the_backend_suite_passes() {
    suite::run_temp().unwrap();
}

#[tokio::test]
async fn the_write_side_writes_and_the_read_side_reads_each_on_its_own_socket() {
    let (d, s) = (dir(), tempfile::tempdir().unwrap());
    let index = Index::open(d.path()).unwrap();
    let (wsock, rsock) = (s.path().join("index.sock"), s.path().join("index-ro.sock"));
    let stop_w = serve(&wsock, Write(index.clone()).router());
    let stop_r = serve(&rsock, Read(index).router());
    up(&wsock).await;
    up(&rsock).await;

    let w = dial(&wsock);
    let writer = IndexServiceClient::new(w.transport(), w.config());
    let applied = writer
        .apply(ApplyRequest {
            log: ::buffa::MessageField::some(log()),
            next: ::buffa::MessageField::some(Cursor {
                size: 2,
                root: vec![7; 32],
                ..Cursor::default()
            }),
            rows: vec![row(1), row(2)],
            ..ApplyRequest::default()
        })
        .await
        .unwrap()
        .into_owned();
    assert_eq!((applied.inserted, applied.duplicates), (2, 0));
    let stale = writer
        .apply(ApplyRequest {
            log: ::buffa::MessageField::some(log()),
            expect_size: 0,
            next: ::buffa::MessageField::some(Cursor {
                size: 3,
                root: vec![7; 32],
                ..Cursor::default()
            }),
            ..ApplyRequest::default()
        })
        .await
        .unwrap_err();
    assert_eq!(
        (stale.code, refusal_reason(&stale).as_deref()),
        (ErrorCode::Aborted, Some("version"))
    );
    let cur = writer
        .get_cursor(GetCursorRequest {
            log: ::buffa::MessageField::some(log()),
            ..GetCursorRequest::default()
        })
        .await
        .unwrap()
        .into_owned();
    assert_eq!(cur.cursor.as_option().map(|c| c.size), Some(2));

    let r = dial(&rsock);
    let reader = IndexReadServiceClient::new(r.transport(), r.config());
    let got = reader.search(search()).await.unwrap().into_owned();
    assert_eq!(
        got.rows
            .iter()
            .map(|r| r.record_id.as_str())
            .collect::<Vec<_>>(),
        ["rec-2", "rec-1"]
    );
    let none = reader
        .search(SearchRequest {
            tenants: vec![],
            ..search()
        })
        .await
        .unwrap_err();
    assert_eq!(
        (none.code, refusal_reason(&none).as_deref()),
        (ErrorCode::InvalidArgument, Some("argument"))
    );

    // The reader's socket serves no write method: the write service is not on it.
    let on_ro = IndexServiceClient::new(r.transport(), r.config());
    let e = on_ro
        .apply(ApplyRequest {
            log: ::buffa::MessageField::some(log()),
            rows: vec![row(9)],
            ..ApplyRequest::default()
        })
        .await
        .unwrap_err();
    assert_eq!(e.code, ErrorCode::Unimplemented);
    assert_eq!(
        reader
            .search(search())
            .await
            .unwrap()
            .into_owned()
            .rows
            .len(),
        2
    );
    let _ = (stop_w.send(()), stop_r.send(()));
}

/// The conformance kit, through both sockets: what every index driver must pass.
#[tokio::test]
async fn the_conformance_kit_passes_through_the_sockets() {
    let (d, s) = (dir(), tempfile::tempdir().unwrap());
    let index = Index::open(d.path()).unwrap();
    let (wsock, rsock) = (s.path().join("index.sock"), s.path().join("index-ro.sock"));
    let stop_w = serve(&wsock, Write(index.clone()).router());
    let stop_r = serve(&rsock, Read(index).router());
    up(&wsock).await;
    up(&rsock).await;
    let cases = atoom_index_conformance::index_suite(dial(&wsock), dial(&rsock)).await;
    let mut out = Vec::new();
    let verdict = atoom_index_conformance::report(&cases, &mut out);
    print!("{}", String::from_utf8_lossy(&out));
    let _ = (stop_w.send(()), stop_r.send(()));
    verdict.unwrap();
}
