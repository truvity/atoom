//! The config source over a real Unix socket: the contract (`proto/`) is the only way in, and the driver is served by
//! `atoom_socket`, plain.

#![allow(refining_impl_trait)]
#![allow(clippy::unwrap_used, clippy::expect_used)] // a failed unwrap in a test is the failure

use std::path::Path;
use std::time::Duration;

use atoom_common::refusal::reason as refusal_reason;
use atoom_socket::{ServerConfig, dial};
use atoom_source::atoom::source::v1alpha1::{
    CapabilitiesRequest, ReadRequest, SourceServiceClient, SourceWatchRequest, Watch,
};
use atoom_source_file::service::Service;
use atoom_source_file::{Dir, suite};
use connectrpc::error::ErrorCode;

fn serve(socket: &Path, dir: &Path) -> tokio::sync::oneshot::Sender<()> {
    let router = Service(Dir::open(dir).unwrap()).router();
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
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    panic!("no socket");
}

#[test]
fn the_source_suite_passes() {
    suite::run_temp().unwrap();
}

#[tokio::test]
async fn a_document_is_read_and_every_refusal_names_a_reason() {
    let (docs, s) = (tempfile::tempdir().unwrap(), tempfile::tempdir().unwrap());
    let sock = s.path().join("config-source.sock");
    let stop = serve(&sock, docs.path());
    up(&sock).await;
    let client = dial(&sock);
    let c = SourceServiceClient::new(client.transport(), client.config());

    let caps = c
        .capabilities(CapabilitiesRequest::default())
        .await
        .unwrap()
        .into_owned();
    assert_eq!(caps.watch.as_known(), Some(Watch::NOTIFY));

    std::fs::write(docs.path().join("boundary.jws"), b"doc one").unwrap();
    let read = |name: &str, held: &str| {
        c.read(ReadRequest {
            name: name.into(),
            if_none_match: held.into(),
            ..Default::default()
        })
    };
    let first = read("boundary", "").await.unwrap().into_owned();
    assert_eq!(first.data, b"doc one");
    assert!(!first.unchanged);
    let again = read("boundary", &first.version).await.unwrap().into_owned();
    assert!(again.unchanged && again.data.is_empty() && again.version == first.version);
    let other = read("boundary", "not-the-label")
        .await
        .unwrap()
        .into_owned();
    assert!(!other.unchanged && other.data == b"doc one");

    let absent = read("upstream", "").await.unwrap_err();
    assert_eq!(
        (absent.code, refusal_reason(&absent).as_deref()),
        (ErrorCode::NotFound, Some("absent"))
    );
    let bad = read("../boundary", "").await.unwrap_err();
    assert_eq!(
        (bad.code, refusal_reason(&bad).as_deref()),
        (ErrorCode::InvalidArgument, Some("schema"))
    );
    let _ = stop.send(());
}

#[tokio::test]
async fn a_document_that_is_not_valid_json_or_utf8_comes_back_byte_for_byte() {
    let (docs, s) = (tempfile::tempdir().unwrap(), tempfile::tempdir().unwrap());
    let sock = s.path().join("config-source.sock");
    let stop = serve(&sock, docs.path());
    up(&sock).await;
    let client = dial(&sock);
    let c = SourceServiceClient::new(client.transport(), client.config());
    for (name, bytes) in [
        ("boundary", &b"{\"payload\": [1, 2,\n"[..]),
        (
            "key-plan",
            &b"  \xff\xfe\x00 not utf-8, leading and trailing space \r\n\n"[..],
        ),
        ("token-lifetime", &b""[..]),
    ] {
        std::fs::write(docs.path().join(format!("{name}.jws")), bytes).unwrap();
        let got = c
            .read(ReadRequest {
                name: name.into(),
                ..Default::default()
            })
            .await
            .unwrap()
            .into_owned();
        assert_eq!(got.data, bytes, "{name}");
        assert!(!got.version.is_empty(), "{name}: a label");
    }
    let _ = stop.send(());
}

#[tokio::test]
async fn watch_tells_of_a_replaced_and_a_removed_document_and_of_nothing_else() {
    let (docs, s) = (tempfile::tempdir().unwrap(), tempfile::tempdir().unwrap());
    let sock = s.path().join("config-source.sock");
    std::fs::write(docs.path().join("key-plan.jws"), b"one").unwrap();
    let stop = serve(&sock, docs.path());
    up(&sock).await;
    let client = dial(&sock);
    let c = SourceServiceClient::new(client.transport(), client.config());
    let held = c
        .read(ReadRequest {
            name: "key-plan".into(),
            ..Default::default()
        })
        .await
        .unwrap()
        .into_owned()
        .version;
    let mut w = c
        .watch(SourceWatchRequest {
            name: "key-plan".into(),
            ..Default::default()
        })
        .await
        .unwrap();

    // Nothing changes: nothing is said.
    assert!(
        tokio::time::timeout(Duration::from_millis(700), w.message())
            .await
            .is_err(),
        "told of a change that did not happen"
    );

    // The swap of a document, as a pipeline or a mounted ConfigMap does it: write beside, rename.
    std::fs::write(docs.path().join(".next"), b"two").unwrap();
    std::fs::rename(docs.path().join(".next"), docs.path().join("key-plan.jws")).unwrap();
    let said = tokio::time::timeout(Duration::from_secs(5), w.message())
        .await
        .expect("told within five seconds")
        .unwrap()
        .expect("the stream is open")
        .to_owned_message();
    assert_eq!(said.name, "key-plan");
    assert!(!said.version.is_empty() && said.version != held);

    std::fs::remove_file(docs.path().join("key-plan.jws")).unwrap();
    let gone = tokio::time::timeout(Duration::from_secs(5), w.message())
        .await
        .expect("told within five seconds")
        .unwrap()
        .expect("the stream is open")
        .to_owned_message();
    assert_eq!(
        (gone.name.as_str(), gone.version.as_str()),
        ("key-plan", "")
    );
    let _ = stop.send(());
}

/// The conformance kit, through the socket: what every source driver must pass.
#[tokio::test]
async fn the_conformance_kit_passes_through_the_socket() {
    let (docs, s) = (tempfile::tempdir().unwrap(), tempfile::tempdir().unwrap());
    let sock = s.path().join("config-source.sock");
    std::fs::write(docs.path().join("boundary.jws"), b"{\"payload\":\"x\"}\n").unwrap();
    let stop = serve(&sock, docs.path());
    up(&sock).await;
    let fixture = atoom_source_conformance::Fixture {
        name: "boundary".into(),
        data: b"{\"payload\":\"x\"}\n".to_vec(),
    };
    let cases = atoom_source_conformance::source_suite(dial(&sock), &fixture).await;
    let mut out = Vec::new();
    let verdict = atoom_source_conformance::report(&cases, &mut out);
    print!("{}", String::from_utf8_lossy(&out));
    let _ = stop.send(());
    verdict.unwrap();
}

/// `check` of a directory that is not there is a refusal: exit 3, so that a script tells it from a bad flag.
#[test]
fn check_of_a_missing_directory_exits_three() {
    let t = tempfile::tempdir().unwrap();
    let out = std::process::Command::new(env!("CARGO_BIN_EXE_source-file"))
        .args(["check", "--dir"])
        .arg(t.path().join("nothing"))
        .output()
        .unwrap();
    assert_eq!(out.status.code(), Some(3));
}
