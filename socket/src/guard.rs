//! The guard of a socket: who may call, decided from the kernel's word about the peer, before the message is decoded.

use connectrpc::error::ErrorCode;
use connectrpc::interceptor::{
    Interceptor, Next, NextStream, PayloadStream, StreamRequest, StreamResponse, UnaryRequest,
    UnaryResponse, async_trait,
};
use connectrpc::{ConnectError, RequestContext};
use std::fmt;
use std::sync::Arc;
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
    /// The host's own rule ([`Allow::check`]): asked for every call with the peer the kernel reported and the procedure, before the
    /// message is decoded. A call whose peer could not be read is refused with the reason `caller` without asking it.
    Check(Admit),
}

impl Allow {
    /// The host's own rule of who may call: `rule(peer, procedure)` returns `Ok(())` to admit the call, or `Err(reason)` to refuse
    /// it with `permission_denied` and a `Refusal` of that reason (an empty reason is `caller`). The procedure is the path the
    /// caller sent, so it names what was asked, not who asked. The rule runs on the task of the request: it decides from what it
    /// is handed, and does not block.
    #[must_use]
    pub fn check(
        rule: impl Fn(&Peer, &str) -> Result<(), &'static str> + Send + Sync + 'static,
    ) -> Self {
        Self::Check(Admit(Arc::new(rule)))
    }

    /// Whether a caller with this uid is admitted, by a process that runs as `own`. A [`Allow::Check`] rule is asked with a peer
    /// that carries only the uid (gid 0, no pid) and an empty procedure; the server asks it with the whole peer.
    #[must_use]
    pub fn admits(&self, uid: u32, own: u32) -> bool {
        let peer = Peer {
            uid,
            gid: 0,
            pid: None,
        };
        self.decide(&peer, own, "").is_ok()
    }

    /// The decision for one call: `Ok(())`, or the reason of the refusal.
    fn decide(&self, peer: &Peer, own: u32, procedure: &str) -> Result<(), &'static str> {
        let admitted = match self {
            Self::SameUid => peer.uid == own,
            Self::Uids(set) => set.contains(&peer.uid),
            Self::Any => true,
            Self::Check(rule) => {
                return (rule.0)(peer, procedure)
                    .map_err(|r| if r.is_empty() { REASON_CALLER } else { r });
            }
        };
        if admitted { Ok(()) } else { Err(REASON_CALLER) }
    }
}

/// The rule of [`Allow::check`]. Two rules are equal when they are the same rule (the same allocation), not when they decide alike.
#[derive(Clone)]
pub struct Admit(Arc<Rule>);

/// A rule: the peer and the procedure in, `Ok(())` or the reason of the refusal out.
type Rule = dyn Fn(&Peer, &str) -> Result<(), &'static str> + Send + Sync;

impl fmt::Debug for Admit {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("Admit(..)")
    }
}

impl PartialEq for Admit {
    fn eq(&self, other: &Self) -> bool {
        Arc::ptr_eq(&self.0, &other.0)
    }
}

impl Eq for Admit {}

/// A call the guard refused, as the host's observer ([`OnRefused`]) is told of it: the reason, the peer the kernel reported (`None`
/// when its credentials could not be read) and the procedure the caller sent. Never a byte of the message.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Refused {
    /// The reason of the `Refusal`: `caller`, or the reason the host's rule gave.
    pub reason: &'static str,
    /// The caller, as the kernel reported it.
    pub peer: Option<Peer>,
    /// The path of the call, as the caller sent it: what was asked, not a name the host can trust.
    pub procedure: String,
}

/// The host's observer of the calls the guard refuses (`ServerConfig::on_refused`), for an audit of refusals. It runs on the task of
/// the refused request, after the decision and before the answer: it must not block, and a host that does slow work with a refusal
/// (writes it somewhere, counts it past a rate) hands it to a task of its own.
#[derive(Clone)]
pub struct OnRefused(Arc<dyn Fn(&Refused) + Send + Sync>);

impl OnRefused {
    /// An observer that calls `f` for every refused call.
    #[must_use]
    pub fn new(f: impl Fn(&Refused) + Send + Sync + 'static) -> Self {
        Self(Arc::new(f))
    }
}

impl fmt::Debug for OnRefused {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("OnRefused(..)")
    }
}

/// The interceptor in front of every procedure. It runs after the request head is read and before the message is decoded.
pub(crate) struct Guard {
    allow: Allow,
    own: u32,
    on_refused: Option<OnRefused>,
}

impl Guard {
    pub(crate) fn new(allow: Allow, on_refused: Option<OnRefused>) -> Self {
        Self {
            allow,
            own: rustix::process::geteuid().as_raw(),
            on_refused,
        }
    }

    fn check(&self, ctx: &RequestContext) -> Result<(), ConnectError> {
        let found = peer(ctx.extensions()).copied();
        let procedure = ctx.path().unwrap_or("");
        let decided = match &found {
            Some(p) => self.allow.decide(p, self.own, procedure),
            // Without credentials the caller cannot be named, and no rule is asked.
            None => Err(REASON_CALLER),
        };
        let Err(reason) = decided else {
            return Ok(());
        };
        tracing::info!(
            procedure,
            caller_uid = found.map(|p| p.uid),
            reason,
            "call refused"
        );
        if let Some(observe) = &self.on_refused {
            (observe.0)(&Refused {
                reason,
                peer: found,
                procedure: procedure.to_owned(),
            });
        }
        Err(refusal(ErrorCode::PermissionDenied, reason, ""))
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
