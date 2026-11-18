use std::os::unix::fs::PermissionsExt;
use std::time::Duration;

use connectrpc::Router;

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
    assert_eq!(c.socket(), std::path::Path::new("/nonexistent/atoom.sock"));
}
