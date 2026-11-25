//! The read-only blob port over a real Unix socket: the contract (`proto/`) is the only way in, the driver is served by `atoom_socket`,
//! plain, nothing the driver serves changes the directory, and the conformance kit runs through the socket.

#![allow(refining_impl_trait)]
#![allow(clippy::unwrap_used, clippy::expect_used)] // a failed unwrap in a test is the failure

use std::os::unix::fs::PermissionsExt;
use std::path::Path;

use atoom_blob::atoom::blob::v1alpha1::{
    BlobGetRequest, BlobListRequest, BlobServiceClient, CreateRequest, PutRequest,
};
use atoom_blob_fsdir::Fs;
use atoom_blob_fsreadonly::{ReadOnly, suite};
use atoom_common::refusal::reason as refusal_reason;
use atoom_socket::{ServerConfig, dial};
use connectrpc::error::ErrorCode;

fn serve(socket: &Path, fs: Fs) -> tokio::sync::oneshot::Sender<()> {
    let router = ReadOnly(fs).router();
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

fn client(sock: &Path) -> BlobServiceClient<atoom_socket::Transport> {
    let c = dial(sock);
    BlobServiceClient::new(c.transport(), c.config())
}

fn get(path: &str) -> BlobGetRequest {
    BlobGetRequest {
        path: path.into(),
        ..Default::default()
    }
}

#[test]
fn the_backend_suite_passes() {
    suite::run_temp().unwrap();
}

#[tokio::test]
async fn a_client_reads_what_the_writer_made_and_can_change_nothing() {
    let (r, s) = (root(), tempfile::tempdir().unwrap());
    let me = rustix::process::geteuid().as_raw();
    let writer = Fs::open(r.path())
        .unwrap()
        .with_pointers(["checkpoint"])
        .unwrap();
    writer.create("audit/main/tile/entries/000", b"x").unwrap();
    writer.put("audit/main/checkpoint", b"c", "").unwrap();

    let sock = s.path().join("blob.sock");
    let stop = serve(&sock, Fs::open_read_only(r.path(), me).unwrap());
    up(&sock).await;
    let c = client(&sock);

    let got = c
        .get(get("audit/main/tile/entries/000"))
        .await
        .unwrap()
        .into_owned();
    assert_eq!(got.data, b"x");
    let list = c
        .list(BlobListRequest {
            prefix: "audit/main/".into(),
            ..Default::default()
        })
        .await
        .unwrap()
        .into_owned();
    assert_eq!(
        list.paths,
        ["audit/main/checkpoint", "audit/main/tile/entries/000"]
    );
    let missing = c.get(get("audit/main/none")).await.unwrap_err();
    assert_eq!(
        (missing.code, refusal_reason(&missing).as_deref()),
        (ErrorCode::NotFound, Some("absent"))
    );
    let bad = c.get(get("../a")).await.unwrap_err();
    assert_eq!(
        (bad.code, refusal_reason(&bad).as_deref()),
        (ErrorCode::InvalidArgument, Some("path"))
    );

    // Create and Put are refused by the service, and the directory is as it was.
    let create = c
        .create(CreateRequest {
            path: "audit/main/tile/entries/001".into(),
            data: vec![1],
            ..Default::default()
        })
        .await
        .unwrap_err();
    assert_eq!(
        (create.code, refusal_reason(&create).as_deref()),
        (ErrorCode::PermissionDenied, Some("rule"))
    );
    let put = c
        .put(PutRequest {
            path: "audit/main/checkpoint".into(),
            data: b"evil".to_vec(),
            if_match: String::new(),
            ..Default::default()
        })
        .await
        .unwrap_err();
    assert_eq!(
        (put.code, refusal_reason(&put).as_deref()),
        (ErrorCode::PermissionDenied, Some("rule"))
    );
    assert!(!r.path().join("audit/main/tile/entries/001").exists());
    assert_eq!(
        std::fs::read(r.path().join("audit/main/checkpoint")).unwrap(),
        b"c"
    );
    let _ = stop.send(());
}

/// The conformance kit, through the socket, over objects that the writing driver made.
#[tokio::test]
async fn the_conformance_kit_passes_through_the_socket() {
    let (r, s) = (root(), tempfile::tempdir().unwrap());
    let me = rustix::process::geteuid().as_raw();
    Fs::open(r.path())
        .unwrap()
        .create("audit/main/tile/entries/000", b"fixture bytes")
        .unwrap();
    let sock = s.path().join("blob.sock");
    let stop = serve(&sock, Fs::open_read_only(r.path(), me).unwrap());
    up(&sock).await;
    let fixture = atoom_blob_conformance::Fixture {
        path: "audit/main/tile/entries/000".into(),
        data: b"fixture bytes".to_vec(),
    };
    let cases = atoom_blob_conformance::blob_read_suite(dial(&sock), &fixture).await;
    let mut out = Vec::new();
    let verdict = atoom_blob_conformance::report(&cases, &mut out);
    print!("{}", String::from_utf8_lossy(&out));
    let _ = stop.send(());
    verdict.unwrap();
}

/// A failed check is a refusal (exit 3): a directory of another owner is not served, and the owner is named by `--owner-uid`.
#[test]
fn check_exits_3_when_the_directory_is_not_the_owners() {
    let dir = root();
    let me = rustix::process::geteuid().as_raw();
    let run = |owner: u32| {
        std::process::Command::new(env!("CARGO_BIN_EXE_blob-fsreadonly"))
            .args(["check", "--root"])
            .arg(dir.path())
            .arg("--owner-uid")
            .arg(owner.to_string())
            .output()
            .unwrap()
    };
    assert_eq!(run(me).status.code(), Some(0));
    assert_eq!(run(me.wrapping_add(1)).status.code(), Some(3));
}
