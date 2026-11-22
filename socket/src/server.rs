//! The server half: a UNIX socket listener that serves a Connect router.

use std::future::Future;
use std::os::unix::fs::{FileTypeExt, PermissionsExt};
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::Duration;

use anyhow::{Context, bail};
use connectrpc::interceptor::Interceptor;
use connectrpc::{ConnectRpcService, DeadlinePolicy, Router};
use hyper_util::rt::{TokioExecutor, TokioIo};
use hyper_util::server::conn::auto::Builder as AutoBuilder;
use hyper_util::server::graceful::GracefulShutdown;
use tokio::net::{UnixListener, UnixStream};
use tower::Service;

use crate::call::Inherit;
use crate::guard::{Allow, Guard, OnRefused, Peer};

/// How long a graceful shutdown waits for calls in flight.
const GRACE: Duration = Duration::from_secs(5);

/// The deadline of a call that carries none: a server's default, and the default of a router called in process.
pub(crate) const DEFAULT_DEADLINE: Duration = Duration::from_secs(30);

/// The configuration of one server on one socket.
#[derive(Clone)]
pub struct ServerConfig {
    /// The absolute path of the socket.
    pub socket: PathBuf,
    /// The mode of the socket file: 0o600 lets the owner connect and nobody else.
    pub mode: u32,
    /// The group of the socket file, or `None` to leave the one the kernel gave. A socket for a caller of another uid is given a group
    /// and the mode 0o660, so that the members of the group, and only they, can open it; the process must be a member of the group.
    /// It changes who may open the socket and nothing else: the guard still decides, from the peer's uid, who may call. A gid of 0 is
    /// refused.
    pub socket_gid: Option<u32>,
    /// Who may call: the second wall behind the mode. The default is the uid this process runs as.
    pub allow: Allow,
    /// The deadline of a call that carries none. A call that carries one keeps it (`Connect-Timeout-Ms`, honoured by the server), and the
    /// handler hands what is left to the calls it makes (`docs/call-context.md`).
    pub default_deadline: Duration,
    /// The host's observer of the calls the guard refuses, or `None`: the guard logs every refusal either way. A host that audits its
    /// refusals sets it ([`OnRefused`]).
    pub on_refused: Option<OnRefused>,
    /// The host's interceptors, run after the guard admitted a call ([`ServerConfig::with_interceptor`]).
    interceptors: Vec<Arc<dyn Interceptor>>,
}

impl std::fmt::Debug for ServerConfig {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("ServerConfig")
            .field("socket", &self.socket)
            .field("mode", &format_args!("{:#o}", self.mode))
            .field("socket_gid", &self.socket_gid)
            .field("allow", &self.allow)
            .field("default_deadline", &self.default_deadline)
            .field("on_refused", &self.on_refused)
            .field("interceptors", &self.interceptors.len())
            .finish()
    }
}

impl ServerConfig {
    /// A configuration for `socket`: mode 0o600, no group of its own, callers of this process's uid only, and a deadline of 30 seconds
    /// for a call that carries none.
    #[must_use]
    pub fn new(socket: impl Into<PathBuf>) -> Self {
        Self {
            socket: socket.into(),
            mode: 0o600,
            socket_gid: None,
            allow: Allow::SameUid,
            default_deadline: DEFAULT_DEADLINE,
            on_refused: None,
            interceptors: Vec::new(),
        }
    }

    /// The same configuration with an interceptor of the host's, run for every call the guard admitted, in the order added, after
    /// the guard and before the message is decoded (a Rust interceptor sees the request head, not the message). A host uses it to
    /// put what it knows of the caller (a name it maps the [`Peer`] to) in the request's extensions for its handlers, or to refuse
    /// a call by a rule that needs more than the peer and the procedure. It is the socket's counterpart of
    /// [`InProcessConfig::with_interceptor`](crate::InProcessConfig::with_interceptor).
    #[must_use]
    pub fn with_interceptor(mut self, i: impl Interceptor) -> Self {
        self.interceptors.push(Arc::new(i));
        self
    }

    /// The same configuration with the host's own rule of who may call ([`Allow::check`]).
    #[must_use]
    pub fn with_allow(mut self, allow: Allow) -> Self {
        self.allow = allow;
        self
    }

    /// The same configuration with an observer of the calls the guard refuses.
    #[must_use]
    pub fn with_on_refused(mut self, f: impl Fn(&crate::Refused) + Send + Sync + 'static) -> Self {
        self.on_refused = Some(OnRefused::new(f));
        self
    }
}

/// Serves `router` on `cfg.socket` until `shutdown` resolves, then drains for at most 5 seconds.
///
/// The socket is made fresh: a stale socket file is replaced, a symlink or any other file is refused, and a socket that still has a
/// listener is never taken over. Each connection's peer credentials are read once; every call passes the guard (`cfg.allow`) before
/// its message is decoded, and a caller outside the set is `permission_denied` with the reason `caller` (or the reason of the host's
/// rule, [`Allow::check`]), and `cfg.on_refused` is told of it. A handler reads the caller with [`peer`](crate::peer).
///
/// # Errors
///
/// When the socket cannot be made, or the listener fails.
pub async fn serve(
    cfg: ServerConfig,
    router: Router,
    shutdown: impl Future<Output = ()> + Send,
) -> anyhow::Result<()> {
    let listener = listen(&cfg.socket, cfg.mode, cfg.socket_gid)?;
    let mut service = ConnectRpcService::new(router)
        .with_deadline_policy(DeadlinePolicy::new().with_default_timeout(cfg.default_deadline));
    // A server that admits every uid that can open the socket has nothing to check.
    let any = matches!(cfg.allow, Allow::Any);
    if !any {
        service = service.with_interceptor(Guard::new(cfg.allow.clone(), cfg.on_refused.clone()));
    }
    // The host's own, after the guard: they see only the calls it admitted.
    for i in cfg.interceptors {
        service = service.with_interceptor_arc(i);
    }
    // After the guard, so that a refused call is refused before its headers are read. Every server hands the call down.
    service = service.with_interceptor(Inherit);
    tracing::info!(socket = %cfg.socket.display(), "listening");

    let graceful = GracefulShutdown::new();
    tokio::pin!(shutdown);
    let result = loop {
        tokio::select! {
            () = &mut shutdown => break Ok(()),
            accepted = listener.accept() => match accepted {
                Ok((stream, _)) => accept(stream, &service, &graceful, any),
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

fn accept(stream: UnixStream, service: &ConnectRpcService, graceful: &GracefulShutdown, any: bool) {
    let peer = match Peer::read(&stream) {
        Ok(p) => Some(p),
        Err(e) if any => {
            tracing::debug!(error = %e, "peer credentials unreadable; the connection is served without a caller");
            None
        }
        Err(e) => {
            // Without credentials the caller cannot be named: drop the connection.
            tracing::warn!(error = %e, "peer credentials unreadable; connection dropped");
            return;
        }
    };
    let service = service.clone();
    let svc = hyper::service::service_fn(move |mut req: http::Request<hyper::body::Incoming>| {
        if let Some(p) = peer {
            req.extensions_mut().insert(p);
        }
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

/// Makes the listening socket: a stale file is replaced, a symlink or a non-socket refused, and the group and the mode are set before
/// the socket gets its final name.
fn listen(path: &Path, mode: u32, gid: Option<u32>) -> anyhow::Result<UnixListener> {
    if gid == Some(0) {
        bail!("the socket group 0 is refused: use None for no group");
    }
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
    // Bind under a temporary name, set the group and the mode, then rename: the socket is never reachable at its final name with a wider
    // mode than asked for.
    let mut tmp = name.to_owned();
    tmp.push(format!(".{}.tmp", std::process::id()));
    let tmp = parent.join(tmp);
    let _ = std::fs::remove_file(&tmp);
    let listener = UnixListener::bind(&tmp).context("bind the socket")?;
    // The group first, then the mode, both before the final name. A failure leaves no socket behind.
    let placed = (|| -> anyhow::Result<()> {
        if let Some(g) = gid {
            rustix::fs::chown(&tmp, None, Some(rustix::fs::Gid::from_raw(g)))
                .context("set the socket group")?;
        }
        std::fs::set_permissions(&tmp, std::fs::Permissions::from_mode(mode))
            .context("set the socket mode")
    })();
    if let Err(e) = placed {
        let _ = std::fs::remove_file(&tmp);
        return Err(e);
    }
    std::fs::rename(&tmp, path).context("put the socket in place")?;
    Ok(listener)
}
