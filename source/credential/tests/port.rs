//! The secret source over a real Unix socket: the contract (`proto/`) is the only way in, and the driver is served by
//! `atoom_socket`, plain.

#![allow(refining_impl_trait)]
#![allow(clippy::unwrap_used, clippy::expect_used)] // a failed unwrap in a test is the failure

use std::os::unix::fs::PermissionsExt as _;
use std::path::Path;
use std::time::Duration;

use atoom_common::refusal::reason as refusal_reason;
use atoom_socket::{ServerConfig, dial};
use atoom_source::atoom::source::v1alpha1::{
    CapabilitiesRequest, ReadRequest, SourceServiceClient, SourceWatchRequest, Watch,
};
use atoom_source_credential::service::Service;
use atoom_source_credential::{Dir, suite};
use connectrpc::error::ErrorCode;

/// A value that no log, error text or label may repeat.
const SECRET: &[u8] = b"tr0ub4dor-and-three-more-words";

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

/// A credentials directory; the driver refuses one that group or others can write, and a
/// temporary directory takes the umask.
fn credentials() -> tempfile::TempDir {
    let d = tempfile::tempdir().unwrap();
    std::fs::set_permissions(d.path(), std::fs::Permissions::from_mode(0o700)).unwrap();
    d
}

fn credential(dir: &Path, name: &str, mode: u32) {
    let p = dir.join(name);
    std::fs::write(&p, SECRET).unwrap();
    std::fs::set_permissions(&p, std::fs::Permissions::from_mode(mode)).unwrap();
}

#[test]
fn the_source_suite_passes() {
    suite::run_temp().unwrap();
}

#[tokio::test]
async fn a_secret_is_read_and_no_refusal_carries_it() {
    let (creds, s) = (credentials(), tempfile::tempdir().unwrap());
    let sock = s.path().join("secret-source.sock");
    credential(creds.path(), "upstream-workforce", 0o400);
    credential(creds.path(), "too-open", 0o644);
    let stop = serve(&sock, creds.path());
    up(&sock).await;
    let client = dial(&sock);
    let c = SourceServiceClient::new(client.transport(), client.config());

    let caps = c
        .capabilities(CapabilitiesRequest::default())
        .await
        .unwrap()
        .into_owned();
    assert_eq!(caps.watch.as_known(), Some(Watch::NONE));

    let read = |name: &str, held: &str| {
        c.read(ReadRequest {
            name: name.into(),
            if_none_match: held.into(),
            ..Default::default()
        })
    };
    let got = read("upstream-workforce", "").await.unwrap().into_owned();
    assert_eq!(got.data, SECRET);
    assert!(
        !String::from_utf8_lossy(got.version.as_bytes()).contains("tr0ub4dor"),
        "the label shows the value"
    );
    let again = read("upstream-workforce", &got.version)
        .await
        .unwrap()
        .into_owned();
    assert!(again.unchanged && again.data.is_empty());

    for (name, code, reason) in [
        ("nothing", ErrorCode::NotFound, "absent"),
        (
            "../upstream-workforce",
            ErrorCode::InvalidArgument,
            "schema",
        ),
        ("too-open", ErrorCode::PermissionDenied, "rule"),
    ] {
        let e = read(name, "").await.unwrap_err();
        assert_eq!(
            (e.code, refusal_reason(&e).as_deref()),
            (code, Some(reason))
        );
        assert!(
            !format!("{e:?}").contains("tr0ub4dor"),
            "an error shows the value"
        );
    }

    let w = c
        .watch(SourceWatchRequest {
            name: "upstream-workforce".into(),
            ..Default::default()
        })
        .await;
    let mut w = w.unwrap();
    let e = w.message().await.unwrap_err();
    assert_eq!(e.code, ErrorCode::Unimplemented);
    let _ = stop.send(());
}

#[tokio::test]
async fn a_fifo_or_a_foreign_kind_of_file_is_refused_as_bound_and_never_hangs() {
    let (creds, s) = (credentials(), tempfile::tempdir().unwrap());
    let sock = s.path().join("secret-source.sock");
    rustix::fs::mknodat(
        rustix::fs::CWD,
        creds.path().join("pipe"),
        rustix::fs::FileType::Fifo,
        rustix::fs::Mode::from_raw_mode(0o600),
        0,
    )
    .unwrap();
    std::fs::create_dir(creds.path().join("subdir")).unwrap();
    let stop = serve(&sock, creds.path());
    up(&sock).await;
    let client = dial(&sock);
    let c = SourceServiceClient::new(client.transport(), client.config());
    for name in ["pipe", "subdir"] {
        let call = c.read(ReadRequest {
            name: name.into(),
            ..Default::default()
        });
        let e = tokio::time::timeout(Duration::from_secs(10), call)
            .await
            .unwrap_or_else(|_| panic!("{name}: the read hung"))
            .unwrap_err();
        assert_eq!(
            (e.code, refusal_reason(&e).as_deref()),
            (ErrorCode::FailedPrecondition, Some("bound")),
            "{name}"
        );
    }
    let _ = stop.send(());
}

#[test]
fn a_credentials_directory_that_others_can_write_is_not_served() {
    let creds = credentials();
    for mode in [0o770, 0o707, 0o777] {
        std::fs::set_permissions(creds.path(), std::fs::Permissions::from_mode(mode)).unwrap();
        let out = std::process::Command::new(env!("CARGO_BIN_EXE_source-credential"))
            .args(["check", "--dir"])
            .arg(creds.path())
            .output()
            .unwrap();
        assert!(!out.status.success(), "{mode:o}");
    }
    std::fs::set_permissions(creds.path(), std::fs::Permissions::from_mode(0o700)).unwrap();
    assert!(Dir::open(creds.path()).is_ok());
}

/// The conformance kit, through the socket: what every source driver must pass.
#[tokio::test]
async fn the_conformance_kit_passes_through_the_socket() {
    let (creds, s) = (credentials(), tempfile::tempdir().unwrap());
    let sock = s.path().join("secret-source.sock");
    credential(creds.path(), "upstream-workforce", 0o400);
    let stop = serve(&sock, creds.path());
    up(&sock).await;
    let fixture = atoom_source_conformance::Fixture {
        name: "upstream-workforce".into(),
        data: SECRET.to_vec(),
    };
    let cases = atoom_source_conformance::source_suite(dial(&sock), &fixture).await;
    let mut out = Vec::new();
    let verdict = atoom_source_conformance::report(&cases, &mut out);
    print!("{}", String::from_utf8_lossy(&out));
    let _ = stop.send(());
    verdict.unwrap();
}
