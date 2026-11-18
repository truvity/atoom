//! The destination port over a real Unix socket against the directory driver: the contract (`proto/`) is the only way in, the driver is
//! served by `atoom_socket`, plain, and the suite runs through the wire as well as in process.

#![allow(refining_impl_trait)]
#![allow(clippy::unwrap_used, clippy::expect_used)] // a failed unwrap in a test is the failure

use std::path::Path;
use std::sync::Arc;

use atoom_common::refusal::reason as refusal_reason;
use atoom_destination::atoom::destination::v1alpha1::{DestinationServiceClient, ReadBackRequest};
use atoom_destination_conformance::rpc::{Client, Service};
use atoom_destination_conformance::suite::{Caps, run};
use atoom_destination_conformance::{Destination, Document, Error};
use atoom_destination_dir::{Directory, Until, suite};
use atoom_socket::{ServerConfig, dial};
use connectrpc::error::ErrorCode;

fn serve(socket: &Path, d: Directory) -> tokio::sync::oneshot::Sender<()> {
    let router = Service(Arc::new(d)).router();
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

fn doc(address: &str, body: &[u8]) -> Document {
    Document {
        address: address.into(),
        content_type: "application/json".into(),
        cache_control: "public, max-age=300".into(),
        body: body.to_vec(),
    }
}

/// The port's suite and the directory's own cases pass over temporary directories (the backend suite).
#[tokio::test]
async fn the_destination_port_suite_passes_for_the_directory_driver() {
    suite::run_temp().await.unwrap();
}

/// The kit's suite through the Connect service (what the binary `destination-conformance` runs): every call crosses the wire.
#[tokio::test]
async fn the_suite_passes_through_the_socket() {
    let (root, state) = suite::temp().unwrap();
    let s = tempfile::tempdir().unwrap();
    let sock = s.path().join("directory.sock");
    let stop = serve(&sock, Directory::open(root.path(), state.path()).unwrap());
    up(&sock).await;
    let client = Client::new(&dial(&sock));
    run(&client, &Caps::default()).await.unwrap();
    // A directory has no public address: ReadBack is unsupported on the wire, as `unimplemented`.
    assert_eq!(
        client.read_back("https://docs.example.com/jwks").await,
        Err(Error::Unsupported)
    );
    let raw = dial(&sock);
    let c = DestinationServiceClient::new(raw.transport(), raw.config());
    let e = c
        .read_back(ReadBackRequest {
            address: "https://docs.example.com/jwks".into(),
            ..Default::default()
        })
        .await
        .unwrap_err();
    assert_eq!(
        (e.code, refusal_reason(&e).as_deref()),
        (ErrorCode::Unimplemented, Some("unsupported"))
    );
    let _ = stop.send(());
}

/// A crash in the middle of a write leaves the old document, never half of one; a restart sweeps the stray file and
/// the next write is whole.
#[tokio::test]
async fn a_crash_mid_write_leaves_the_old_document_and_a_restart_cleans_up() {
    let (root, state) = suite::temp().unwrap();
    let d = Directory::open(root.path(), state.path()).unwrap();
    let live = root.path().join("jwks.json");
    d.put(doc("jwks.json", br#"{"keys":[1]}"#)).await.unwrap();

    // Crash 1: the new body is in a temporary file; nothing is renamed.
    d.put_until(&doc("jwks.json", br#"{"keys":[1,2]}"#), Until::Temp)
        .unwrap();
    assert_eq!(std::fs::read(&live).unwrap(), br#"{"keys":[1]}"#);
    let strays = || {
        std::fs::read_dir(root.path())
            .unwrap()
            .filter_map(Result::ok)
            .filter(|e| e.file_name().to_string_lossy().starts_with(".tmp-"))
            .count()
    };
    assert_eq!(strays(), 1, "the crash left a temporary file");
    // A listing neither shows the stray file nor the half document.
    let l = d.list("", None, 0).await.unwrap().documents;
    assert_eq!(l.len(), 1);
    assert_eq!(
        l[0].sha256,
        atoom_destination_conformance::sha256_hex(br#"{"keys":[1]}"#)
    );

    // A restarted driver (a new Directory over the same directories) sweeps it.
    let d = Directory::open(root.path(), state.path()).unwrap();
    assert_eq!(d.sweep().unwrap(), 1);
    assert_eq!(strays(), 0);

    // Crash 2: the document is in place, its record is the old one. The listing reports the file's own digest.
    d.put_until(&doc("jwks.json", br#"{"keys":[3]}"#), Until::Document)
        .unwrap();
    assert_eq!(std::fs::read(&live).unwrap(), br#"{"keys":[3]}"#);
    let l = d.list("", None, 0).await.unwrap().documents;
    assert_eq!(
        l[0].sha256,
        atoom_destination_conformance::sha256_hex(br#"{"keys":[3]}"#)
    );

    // The next write is whole, and the record is current again.
    let w = d.put(doc("jwks.json", br#"{"keys":[3,4]}"#)).await.unwrap();
    assert_eq!(std::fs::read(&live).unwrap(), br#"{"keys":[3,4]}"#);
    assert_eq!(
        w.sha256,
        atoom_destination_conformance::sha256_hex(br#"{"keys":[3,4]}"#)
    );
    assert_eq!(strays(), 0);
}

/// The directories are checked at the start: a symlink, a wrong owner mode, a missing directory.
#[test]
fn the_directories_are_checked_at_the_start() {
    use std::os::unix::fs::PermissionsExt;
    let (root, state) = suite::temp().unwrap();
    assert!(Directory::open(root.path(), state.path()).is_ok());
    let link = root.path().join("link");
    std::os::unix::fs::symlink(root.path(), &link).unwrap();
    assert!(
        Directory::open(&link, state.path()).is_err(),
        "a symlink as the root"
    );
    assert!(Directory::open(&root.path().join("missing"), state.path()).is_err());
    std::fs::set_permissions(root.path(), std::fs::Permissions::from_mode(0o775)).unwrap();
    assert!(
        Directory::open(root.path(), state.path()).is_err(),
        "a root others' group can write"
    );
    std::fs::set_permissions(root.path(), std::fs::Permissions::from_mode(0o755)).unwrap();
    // The state directory may be open to traverse (its records are in closed subdirectories), never to a write.
    std::fs::set_permissions(state.path(), std::fs::Permissions::from_mode(0o755)).unwrap();
    let d = Directory::open(root.path(), state.path()).unwrap();
    assert_eq!(
        std::fs::metadata(state.path().join("meta"))
            .unwrap()
            .permissions()
            .mode()
            & 0o777,
        0o700
    );
    drop(d);
    std::fs::set_permissions(state.path(), std::fs::Permissions::from_mode(0o775)).unwrap();
    assert!(
        Directory::open(root.path(), state.path()).is_err(),
        "a state directory a group can write"
    );
}

/// A symlink planted inside the root is not followed by a write or a delete.
#[tokio::test]
async fn a_symlink_in_the_root_is_not_followed() {
    let (root, state) = suite::temp().unwrap();
    let outside = tempfile::tempdir().unwrap();
    std::os::unix::fs::symlink(outside.path(), root.path().join("out")).unwrap();
    let d = Directory::open(root.path(), state.path()).unwrap();
    assert!(d.put(doc("out/x.json", b"{}")).await.is_err());
    assert!(std::fs::read_dir(outside.path()).unwrap().next().is_none());
    std::os::unix::fs::symlink(outside.path().join("target"), root.path().join("jwks.json"))
        .unwrap();
    assert!(d.put(doc("jwks.json", b"{}")).await.is_err());
    assert!(!outside.path().join("target").exists());
}
