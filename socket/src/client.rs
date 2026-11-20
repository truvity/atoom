//! The client half: Connect over a UNIX socket, h2c.

use std::path::{Path, PathBuf};
use std::sync::{Arc, OnceLock};

use connectrpc::client::{
    ClientBody, ClientConfig, ClientTransport, Http2Connection, SharedHttp2Connection,
};
use futures::future::BoxFuture;
use http::{Request, Response};

/// The base URI of every call: the authority is a name only, the socket is the address.
const BASE_URI: &str = "http://unix";

/// A Connect client of one UNIX socket. Hand it to a generated client:
/// `ItemServiceClient::new(client.transport(), client.config())`.
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
        ClientConfig::new(http::Uri::from_static(BASE_URI))
    }

    /// The socket this client dials.
    #[must_use]
    pub fn socket(&self) -> &Path {
        &self.transport.socket
    }
}

/// A Connect client of the UNIX socket. The connection is made at the first call, so `dial` does not need a running server, and is
/// made again when the server restarts.
#[must_use]
pub fn dial(socket: &Path) -> Client {
    Client {
        transport: Transport {
            socket: Arc::new(socket.to_path_buf()),
            conn: Arc::new(OnceLock::new()),
        },
    }
}

/// The client transport: one h2c connection to a UNIX socket, shared by clones.
#[derive(Clone)]
pub struct Transport {
    socket: Arc<PathBuf>,
    conn: Arc<OnceLock<SharedHttp2Connection>>,
}

impl ClientTransport for Transport {
    type ResponseBody = <SharedHttp2Connection as ClientTransport>::ResponseBody;
    type Error = <SharedHttp2Connection as ClientTransport>::Error;

    fn send(
        &self,
        mut request: Request<ClientBody>,
    ) -> BoxFuture<'static, Result<Response<Self::ResponseBody>, Self::Error>> {
        crate::call::pass_on(&mut request);
        // `shared` starts a worker task, so the connection is made here, inside the runtime.
        let conn = self
            .conn
            .get_or_init(|| {
                Http2Connection::lazy_unix(self.socket.as_path(), http::Uri::from_static(BASE_URI))
                    .shared(1024)
            })
            .clone();
        Box::pin(async move { conn.send(request).await })
    }
}
