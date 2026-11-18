//! The server half: a UNIX socket listener that serves a Connect router.

use std::future::Future;
use std::os::unix::fs::{FileTypeExt, PermissionsExt};
use std::path::{Path, PathBuf};
use std::time::Duration;

use anyhow::{Context, bail};
use connectrpc::{ConnectRpcService, DeadlinePolicy, Router};
use hyper_util::rt::{TokioExecutor, TokioIo};
use hyper_util::server::conn::auto::Builder as AutoBuilder;
use hyper_util::server::graceful::GracefulShutdown;
use tokio::net::{UnixListener, UnixStream};
use tower::Service;

/// How long a graceful shutdown waits for calls in flight.
const GRACE: Duration = Duration::from_secs(5);

/// The configuration of one server on one socket.
#[derive(Clone, Debug)]
pub struct ServerConfig {
    /// The absolute path of the socket.
    pub socket: PathBuf,
    /// The mode of the socket file. 0o600 lets the owner connect and nobody else: who may connect is the file's permission and
    /// nothing more, since the server asks no one who they are.
    pub mode: u32,
    /// The deadline of a call that carries none.
    pub default_deadline: Duration,
}

impl ServerConfig {
    /// A configuration for `socket`: mode 0o600 and a deadline of 30 seconds for a call that carries none.
    #[must_use]
    pub fn new(socket: impl Into<PathBuf>) -> Self {
        Self {
            socket: socket.into(),
            mode: 0o600,
            default_deadline: Duration::from_secs(30),
        }
    }
}

/// Serves `router` on `cfg.socket` until `shutdown` resolves, then drains for at most 5 seconds.
///
/// The socket is made fresh: a stale socket file is replaced, a symlink or any other file is refused, and a socket that still has a
/// listener is never taken over.
///
/// # Errors
///
/// When the socket cannot be made, or the listener fails.
pub async fn serve(
    cfg: ServerConfig,
    router: Router,
    shutdown: impl Future<Output = ()> + Send,
) -> anyhow::Result<()> {
    let listener = listen(&cfg.socket, cfg.mode)?;
    let service = ConnectRpcService::new(router)
        .with_deadline_policy(DeadlinePolicy::new().with_default_timeout(cfg.default_deadline));
    tracing::info!(socket = %cfg.socket.display(), "listening");

    let graceful = GracefulShutdown::new();
    tokio::pin!(shutdown);
    let result = loop {
        tokio::select! {
            () = &mut shutdown => break Ok(()),
            accepted = listener.accept() => match accepted {
                Ok((stream, _)) => accept(stream, &service, &graceful),
                Err(e) => break Err(e).context("accept"),
            },
        }
    };
    drop(listener);
    // The file is ours: remove it so that the next start finds no stale socket.
    let _ = std::fs::remove_file(&cfg.socket);
    if tokio::time::timeout(GRACE, graceful.shutdown())
        .await
        .is_err()
    {
        tracing::warn!("calls still running after the grace period");
    }
    result
}

fn accept(stream: UnixStream, service: &ConnectRpcService, graceful: &GracefulShutdown) {
    let service = service.clone();
    let svc = hyper::service::service_fn(move |req: http::Request<hyper::body::Incoming>| {
        let mut service = service.clone();
        async move { service.call(req).await }
    });
    let conn = AutoBuilder::new(TokioExecutor::new())
        .serve_connection(TokioIo::new(stream), svc)
        .into_owned();
    let conn = graceful.watch(conn);
    tokio::spawn(async move {
        if let Err(e) = conn.await {
            tracing::debug!(error = %e, "connection ended with an error");
        }
    });
}

/// Makes the listening socket: a stale file is replaced, a symlink or a non-socket refused, and the mode is set before the socket
/// gets its final name.
fn listen(path: &Path, mode: u32) -> anyhow::Result<UnixListener> {
    match std::fs::symlink_metadata(path) {
        Ok(m) if m.file_type().is_symlink() => {
            bail!("{}: a symlink is in the way of the socket", path.display())
        }
        Ok(m) if m.file_type().is_socket() => {
            // A socket file that still has a listener is not stale: never take it over. Only a refused connection proves that
            // nobody listens.
            match std::os::unix::net::UnixStream::connect(path) {
                Ok(_) => bail!(
                    "{}: a live listener already serves this socket",
                    path.display()
                ),
                Err(e) if e.kind() == std::io::ErrorKind::ConnectionRefused => {
                    std::fs::remove_file(path).context("remove the stale socket")?;
                }
                Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
                Err(e) => {
                    return Err(e).context("cannot tell whether the socket is stale; left alone");
                }
            }
        }
        Ok(_) => bail!("{}: exists and is not a socket", path.display()),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
        Err(e) => return Err(e).context("stat the socket path"),
    }
    let parent = path.parent().context("the socket path has no directory")?;
    let name = path.file_name().context("the socket path has no name")?;
    // Bind under a temporary name, set the mode, then rename: the socket is never reachable at its final name with a wider mode than
    // asked for.
    let mut tmp = name.to_owned();
    tmp.push(format!(".{}.tmp", std::process::id()));
    let tmp = parent.join(tmp);
    let _ = std::fs::remove_file(&tmp);
    let listener = UnixListener::bind(&tmp).context("bind the socket")?;
    if let Err(e) = std::fs::set_permissions(&tmp, std::fs::Permissions::from_mode(mode)) {
        let _ = std::fs::remove_file(&tmp);
        return Err(e).context("set the socket mode");
    }
    std::fs::rename(&tmp, path).context("put the socket in place")?;
    Ok(listener)
}
