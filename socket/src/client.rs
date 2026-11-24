//! The client half: Connect over a UNIX socket (h2c), or in the same process with no socket.

use std::convert::Infallible;
use std::path::{Path, PathBuf};
use std::pin::Pin;
use std::sync::{Arc, OnceLock};
use std::task::{Context, Poll};
use std::time::Duration;

use bytes::Bytes;
use connectrpc::client::{
    ClientBody, ClientConfig, ClientTransport, Http2Connection, ServiceTransport,
    SharedHttp2Connection,
};
use connectrpc::interceptor::Interceptor;
use connectrpc::service::ConnectRpcBody;
use connectrpc::{ConnectRpcService, DeadlinePolicy, Router};
use futures::future::BoxFuture;
use http::{Request, Response};
use hyper::body::{Body, Frame, SizeHint};
use sync_wrapper::SyncWrapper;

use crate::call::Inherit;
use crate::server::DEFAULT_DEADLINE;

/// The base URI of every call: the authority is a name only, the socket is the address.
const BASE_URI: &str = "http://unix";
/// The base URI of a call made in process: there is no address at all.
const IN_PROCESS_URI: &str = "http://in-process";

/// A Connect client of one UNIX socket ([`dial`]) or of a router in the same process ([`in_process`]). Hand it to a generated client:
/// `ItemServiceClient::new(client.transport(), client.config())`. Both are the one type, so a consumer chooses between them by
/// configuration and the code that builds the generated clients does not change.
///
/// Every call made through it carries the deadline left and the correlation id of the call being served, when it is made inside a handler
/// of a server built by [`serve`](crate::serve) or inside [`call::scope`](crate::call::scope) (`docs/call-context.md`).
#[derive(Clone)]
pub struct Client {
    transport: Transport,
}

impl Client {
    /// The transport, to build a generated client.
    #[must_use]
    pub fn transport(&self) -> Transport {
        self.transport.clone()
    }

    /// The configuration for a generated client: Connect, Protobuf, no compression.
    #[must_use]
    pub fn config(&self) -> ClientConfig {
        let uri = match self.transport.inner {
            Inner::Socket { .. } => BASE_URI,
            Inner::InProcess(_) => IN_PROCESS_URI,
        };
        ClientConfig::new(http::Uri::from_static(uri))
    }

    /// The socket this client dials, or `None` for a client made by [`in_process`].
    #[must_use]
    pub fn socket(&self) -> Option<&Path> {
        match &self.transport.inner {
            Inner::Socket { socket, .. } => Some(socket.as_path()),
            Inner::InProcess(_) => None,
        }
    }
}

/// A Connect client of the UNIX socket. The connection is made at the first call, so `dial` does not need a running server, and is
/// made again when the server restarts.
#[must_use]
pub fn dial(socket: &Path) -> Client {
    Client {
        transport: Transport {
            inner: Inner::Socket {
                socket: Arc::new(socket.to_path_buf()),
                conn: Arc::new(OnceLock::new()),
            },
        },
    }
}

/// What a host sets around a driver's router that it calls in process ([`in_process_with`]).
#[derive(Clone)]
pub struct InProcessConfig {
    default_deadline: Duration,
    interceptors: Vec<Arc<dyn Interceptor>>,
}

impl Default for InProcessConfig {
    fn default() -> Self {
        Self::new()
    }
}

impl InProcessConfig {
    /// The configuration of [`in_process`]: a deadline of 30 seconds for a call that carries none, and no interceptor of the host.
    #[must_use]
    pub fn new() -> Self {
        Self {
            default_deadline: DEFAULT_DEADLINE,
            interceptors: Vec::new(),
        }
    }

    /// The deadline of a call that carries none (as `ServerConfig::default_deadline` is for a socket). A call that carries one keeps it.
    #[must_use]
    pub fn with_default_deadline(mut self, d: Duration) -> Self {
        self.default_deadline = d;
        self
    }

    /// The host's rule of who may call. There is no peer in process: the kernel says nothing about a caller in the same process, so the
    /// socket's guard has nothing to read. The host decides in an interceptor of its own, which runs before every call, in the order
    /// added, and may refuse it (`permission_denied`) or let it through.
    #[must_use]
    pub fn with_interceptor(mut self, i: impl Interceptor) -> Self {
        self.interceptors.push(Arc::new(i));
        self
    }
}

/// A Connect client of `router` in the same process: no socket, no second process. It is the transport of
/// [record 0009](../../docs/decisions/0009-connectrpc-in-process.md): the generated client talks to the driver's handler through
/// `connectrpc`'s own service, so the serialization, the interceptors, the error codes and the streaming are the ones of a socket.
///
/// The deadline and the correlation id of the caller (`call::scope`, or a handler that calls on) reach the handler as they do through a
/// socket. What does not exist in process is the peer: [`peer`](crate::peer) is `None`, and who may call is the host's rule
/// ([`InProcessConfig::with_interceptor`]).
///
/// It has to be called from inside a Tokio runtime, as a call through a socket does.
#[must_use]
pub fn in_process(router: Router) -> Client {
    in_process_with(router, InProcessConfig::new())
}

/// [`in_process`] with the host's own settings.
#[must_use]
pub fn in_process_with(router: Router, cfg: InProcessConfig) -> Client {
    let mut service = ConnectRpcService::new(router)
        .with_deadline_policy(DeadlinePolicy::new().with_default_timeout(cfg.default_deadline));
    for i in cfg.interceptors {
        service = service.with_interceptor_arc(i);
    }
    // Last, as in `serve`: the host's rules see a call before its headers are read.
    service = service.with_interceptor(Inherit);
    Client {
        transport: Transport {
            inner: Inner::InProcess(ServiceTransport::new(service)),
        },
    }
}

/// The client transport: one h2c connection to a UNIX socket, shared by clones, or the router of this process.
#[derive(Clone)]
pub struct Transport {
    inner: Inner,
}

#[derive(Clone)]
enum Inner {
    Socket {
        socket: Arc<PathBuf>,
        conn: Arc<OnceLock<SharedHttp2Connection>>,
    },
    InProcess(ServiceTransport<ConnectRpcService>),
}

/// The body of a response, from a socket or from the router of this process. It is `Send + Sync + 'static`, so a response stream
/// (`connectrpc`'s `ServerStream<ResponseBody, _>`) can sit in a struct that a spawned task borrows across an await.
pub struct ResponseBody(Source);

enum Source {
    /// From a socket: hyper's body, which is `Send + Sync` already.
    Socket(hyper::body::Incoming),
    /// From the router of this process. `connectrpc`'s body holds a boxed stream that is `Send` but not `Sync`; the wrapper makes it
    /// `Sync` by giving no access through a shared reference at all, only through `&mut` or `Pin<&mut>`, so two threads can never
    /// touch the inner body at once (`sync_wrapper`, no lock and no `unsafe` here).
    InProcess(SyncWrapper<ConnectRpcBody>),
}

impl Body for ResponseBody {
    type Data = Bytes;
    type Error = hyper::Error;

    fn poll_frame(
        self: Pin<&mut Self>,
        cx: &mut Context<'_>,
    ) -> Poll<Option<Result<Frame<Self::Data>, Self::Error>>> {
        match &mut self.get_mut().0 {
            Source::Socket(b) => Pin::new(b).poll_frame(cx),
            Source::InProcess(b) => Pin::new(b.get_mut())
                .poll_frame(cx)
                .map(|o| o.map(|r| r.map_err(|e: Infallible| match e {}))),
        }
    }

    // The two hints take `&self`, which the wrapper does not reach through. `ConnectRpcBody` does not override them either, so the
    // defaults below are the answers it gives itself: nothing is lost.
    fn is_end_stream(&self) -> bool {
        match &self.0 {
            Source::Socket(b) => b.is_end_stream(),
            Source::InProcess(_) => false,
        }
    }

    fn size_hint(&self) -> SizeHint {
        match &self.0 {
            Source::Socket(b) => b.size_hint(),
            Source::InProcess(_) => SizeHint::default(),
        }
    }
}

impl ClientTransport for Transport {
    type ResponseBody = ResponseBody;
    type Error = <SharedHttp2Connection as ClientTransport>::Error;

    fn send(
        &self,
        mut request: Request<ClientBody>,
    ) -> BoxFuture<'static, Result<Response<Self::ResponseBody>, Self::Error>> {
        crate::call::pass_on(&mut request);
        match &self.inner {
            Inner::Socket { socket, conn } => {
                // `shared` starts a worker task, so the connection is made here, inside the runtime.
                let conn = conn
                    .get_or_init(|| {
                        Http2Connection::lazy_unix(
                            socket.as_path(),
                            http::Uri::from_static(BASE_URI),
                        )
                        .shared(1024)
                    })
                    .clone();
                Box::pin(async move {
                    conn.send(request)
                        .await
                        .map(|r| r.map(|b| ResponseBody(Source::Socket(b))))
                })
            }
            Inner::InProcess(service) => {
                let sent = service.send(request);
                Box::pin(async move {
                    match sent.await {
                        Ok(r) => {
                            Ok(r.map(|b| ResponseBody(Source::InProcess(SyncWrapper::new(b)))))
                        }
                        Err(never) => match never {},
                    }
                })
            }
        }
    }
}
