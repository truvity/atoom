//! The blob port over a real Unix socket: the contract (`proto/`) is the only way in, the driver is served by `atoom_socket`, plain, and
//! the conformance kit runs through the socket.

#![allow(refining_impl_trait)]
#![allow(clippy::unwrap_used, clippy::expect_used)] // a failed unwrap in a test is the failure

use std::os::unix::fs::PermissionsExt;
use std::path::Path;

use atoom_blob::atoom::blob::v1alpha1::{
    BlobGetRequest, BlobListRequest, BlobServiceClient, CreateRequest, PutRequest,
};
use atoom_blob_fs::service::Service;
use atoom_blob_fs::{Fs, suite};
use atoom_common::refusal::reason as refusal_reason;
use atoom_socket::{ServerConfig, dial};
use connectrpc::error::ErrorCode;

fn serve(socket: &Path, root: &Path) -> tokio::sync::oneshot::Sender<()> {
    let router = Service(
        Fs::open(root)
            .unwrap()
            .with_pointers(atoom_blob_conformance::POINTERS)
            .unwrap(),
    )
    .router();
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

fn root() -> tempfile::TempDir {
    let d = tempfile::tempdir().unwrap();
    std::fs::set_permissions(d.path(), std::fs::Permissions::from_mode(0o750)).unwrap();
    d
}

#[test]
fn the_backend_suite_passes() {
    suite::run_temp().unwrap();
}

/// A name that exists is refused to a second create whatever the bytes, and a put changes nothing but a pointer: the
/// port has no way to rewrite a sealed object, and no delete.
///
#[tokio::test]
async fn a_client_uses_the_port_and_every_refusal_names_a_reason() {
    let (r, s) = (root(), tempfile::tempdir().unwrap());
    let sock = s.path().join("blob.sock");
    let stop = serve(&sock, r.path());
    up(&sock).await;
    let client = dial(&sock);
    let c = BlobServiceClient::new(client.transport(), client.config());

    let made = c
        .create(CreateRequest {
            path: "a/b".into(),
            data: b"x".to_vec(),
            ..Default::default()
        })
        .await
        .unwrap()
        .into_owned();
    let again = c
        .create(CreateRequest {
            path: "a/b".into(),
            data: b"y".to_vec(),
            ..Default::default()
        })
        .await
        .unwrap_err();
    assert_eq!(
        (again.code, refusal_reason(&again).as_deref()),
        (ErrorCode::AlreadyExists, Some("taken"))
    );
    let got = c
        .get(BlobGetRequest {
            path: "a/b".into(),
            ..Default::default()
        })
        .await
        .unwrap()
        .into_owned();
    assert_eq!((got.data, got.etag), (b"x".to_vec(), made.etag));
    let bad = c
        .get(BlobGetRequest {
            path: "../a".into(),
            ..Default::default()
        })
        .await
        .unwrap_err();
    assert_eq!(
        (bad.code, refusal_reason(&bad).as_deref()),
        (ErrorCode::InvalidArgument, Some("path"))
    );
    let put = c
        .put(PutRequest {
            path: "a/b".into(),
            data: b"z".to_vec(),
            ..Default::default()
        })
        .await
        .unwrap_err();
    assert_eq!(
        (put.code, refusal_reason(&put).as_deref()),
        (ErrorCode::PermissionDenied, Some("rule"))
    );
    let ptr = c
        .put(PutRequest {
            path: "a/kit-head".into(),
            data: b"z".to_vec(),
            if_match: "old".into(),
            ..Default::default()
        })
        .await
        .unwrap_err();
    assert_eq!(
        (ptr.code, refusal_reason(&ptr).as_deref()),
        (ErrorCode::Aborted, Some("version"))
    );
    let list = c
        .list(BlobListRequest {
            prefix: "a/".into(),
            ..Default::default()
        })
        .await
        .unwrap()
        .into_owned();
    assert_eq!(list.paths, ["a/b"]);
    let _ = stop.send(());
}

/// The conformance kit, through the socket: what every blob driver must pass.
#[tokio::test]
async fn the_conformance_kit_passes_through_the_socket() {
    let (r, s) = (root(), tempfile::tempdir().unwrap());
    let sock = s.path().join("blob.sock");
    let stop = serve(&sock, r.path());
    up(&sock).await;
    let cases = atoom_blob_conformance::blob_suite(dial(&sock)).await;
    let mut out = Vec::new();
    let verdict = atoom_blob_conformance::report(&cases, &mut out);
    print!("{}", String::from_utf8_lossy(&out));
    let _ = stop.send(());
    verdict.unwrap();
}

/// A failed check is a refusal (exit 3), not a crash or a bad flag.
#[test]
fn check_exits_3_when_the_directory_cannot_be_served() {
    let good = root();
    let run = |dir: &Path| {
        std::process::Command::new(env!("CARGO_BIN_EXE_blob-fs"))
            .args(["check", "--root"])
            .arg(dir)
            .output()
            .unwrap()
    };
    assert_eq!(run(good.path()).status.code(), Some(0));
    let wide = tempfile::tempdir().unwrap();
    std::fs::set_permissions(wide.path(), std::fs::Permissions::from_mode(0o777)).unwrap();
    let out = run(wide.path());
    assert_eq!(out.status.code(), Some(3), "{out:?}");
    assert_eq!(run(&good.path().join("missing")).status.code(), Some(3));
}
