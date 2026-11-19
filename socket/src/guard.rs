//! The guard of a socket: who may call, decided from the kernel's word about the peer, before the message is decoded.

use connectrpc::error::ErrorCode;
use connectrpc::interceptor::{
    Interceptor, Next, NextStream, PayloadStream, StreamRequest, StreamResponse, UnaryRequest,
    UnaryResponse, async_trait,
};
use connectrpc::{ConnectError, RequestContext};
use tokio::net::UnixStream;

use atoom_common::refusal::new as refusal;

/// The reason of the refusal of a caller the guard does not admit (`atoom.common.v1alpha1.Refusal`).
pub const REASON_CALLER: &str = "caller";

/// The caller of a call: what the kernel said about the process at the other end of the connection (`SO_PEERCRED` on Linux,
/// `getpeereid` on macOS), read once when the connection was accepted. A handler reads it with [`peer`].
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Peer {
    /// The uid of the peer process.
    pub uid: u32,
    /// The gid of the peer process.
    pub gid: u32,
    /// The pid of the peer process (`None` where the platform does not say, as macOS does not).
    pub pid: Option<i32>,
}

impl Peer {
    /// Reads the credentials of the connection's peer.
    pub(crate) fn read(stream: &UnixStream) -> std::io::Result<Self> {
        let c = stream.peer_cred()?;
        Ok(Self {
            uid: c.uid(),
            gid: c.gid(),
            pid: c.pid(),
        })
    }
}

/// The caller of the call being served, set by the server from the connection's peer credentials. `None` outside a handler of a
/// server built by [`serve`](crate::serve), or when the platform could not read them (only a server that admits any uid serves such a
/// connection).
#[must_use]
pub fn peer(ext: &http::Extensions) -> Option<&Peer> {
    ext.get::<Peer>()
}

/// Who may call a socket. The file mode of the socket is the first wall and this is the second: a process that can open the socket
/// is still refused (`permission_denied`, reason `caller`) unless its uid is admitted.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub enum Allow {
    /// Only the uid this process runs as. The default: with the default mode 0600 it is what the file system allowed already, with
    /// the one difference that root is refused too.
    #[default]
    SameUid,
    /// Exactly these uids, and no other: the uid of this process is not in the set unless it is listed. An empty list admits nobody.
    Uids(Vec<u32>),
    /// Every uid that can open the socket: the file permission is the only guard. A host that wants the plain behaviour says so.
    Any,
}

impl Allow {
    /// Whether a caller with this uid is admitted, by a process that runs as `own`.
    #[must_use]
    pub fn admits(&self, uid: u32, own: u32) -> bool {
        match self {
            Self::SameUid => uid == own,
            Self::Uids(set) => set.contains(&uid),
            Self::Any => true,
        }
    }
}

/// The interceptor in front of every procedure. It runs after the request head is read and before the message is decoded.
pub(crate) struct Guard {
    allow: Allow,
    own: u32,
}

impl Guard {
    pub(crate) fn new(allow: Allow) -> Self {
        Self {
            allow,
            own: rustix::process::geteuid().as_raw(),
        }
    }

    fn check(&self, ctx: &RequestContext) -> Result<(), ConnectError> {
        let found = peer(ctx.extensions());
        if found.is_some_and(|p| self.allow.admits(p.uid, self.own)) {
            return Ok(());
        }
        tracing::info!(
            procedure = ctx.path().unwrap_or(""),
            caller_uid = found.map(|p| p.uid),
            reason = REASON_CALLER,
            "call refused"
        );
        Err(refusal(ErrorCode::PermissionDenied, REASON_CALLER, ""))
    }
}

#[async_trait]
impl Interceptor for Guard {
    async fn intercept_unary(
        &self,
        req: UnaryRequest,
        next: Next<'_>,
    ) -> Result<UnaryResponse, ConnectError> {
        self.check(&req.ctx)?;
        next.run(req).await
    }

    async fn intercept_streaming(
        &self,
        req: StreamRequest,
        inbound: PayloadStream,
        next: NextStream<'_>,
    ) -> Result<StreamResponse, ConnectError> {
        self.check(&req.ctx)?;
        next.run(req, inbound).await
    }
}
