//! The response body is `Send + Sync + 'static`, through a socket and in process, so a response stream can be held in a struct that a
//! spawned task borrows across an await: the shape of a watch that keeps its stream and asks its own client again while it follows it.

use std::time::Duration;

use atoom_common::atoom::common::v1alpha1::{Refusal, RefusalView};
use connectrpc::client::{
    CallOptions, ClientTransport, ServerStream, call_server_stream, call_unary,
};
use connectrpc::{ConnectError, RequestContext, Response, Router, ServiceStream, Spec, StreamType};
use connectrpc::{handler_fn, streaming_handler_fn};

use super::*;

const COUNT: Spec = Spec::client("/atoom.test.v1.Body/Count", StreamType::ServerStream);
const PING: Spec = Spec::client("/atoom.test.v1.Body/Ping", StreamType::Unary);

/// How many messages `Count` sends.
const MESSAGES: usize = 3;

fn assert_send<T: Send>() {}
fn assert_sync<T: Sync>() {}
fn assert_static<T: 'static>() {}

#[test]
fn every_public_body_and_transport_is_send_sync_and_static() {
    assert_send::<ResponseBody>();
    assert_sync::<ResponseBody>();
    assert_static::<ResponseBody>();
    assert_send::<<Transport as ClientTransport>::ResponseBody>();
    assert_sync::<<Transport as ClientTransport>::ResponseBody>();
    assert_send::<Transport>();
    assert_sync::<Transport>();
    assert_send::<Client>();
    assert_sync::<Client>();
    // What a consumer holds: the stream of a server-streaming call over this transport.
    assert_send::<ServerStream<ResponseBody, RefusalView<'static>>>();
    assert_sync::<ServerStream<ResponseBody, RefusalView<'static>>>();
}

/// `Count` sends `MESSAGES` messages, `n` in the reason, with a short pause between them so the stream is read while it is open.
/// `Ping` answers at once.
fn router() -> Router {
    Router::new()
        .route_server_stream(
            "atoom.test.v1.Body",
            "Count",
            streaming_handler_fn(|_ctx: RequestContext, _r: Refusal| async move {
                let s: ServiceStream<Refusal> =
                    Box::pin(futures::stream::unfold(0, |n| async move {
                        if n == MESSAGES {
                            return None;
                        }
                        tokio::time::sleep(Duration::from_millis(5)).await;
                        let m = Refusal {
                            reason: n.to_string(),
                            ..Refusal::default()
                        };
                        Some((Ok(m), n + 1))
                    }));
                Response::ok(s)
            }),
        )
        .route(
            "atoom.test.v1.Body",
            "Ping",
            handler_fn(|_ctx: RequestContext, _r: Refusal| async move {
                Response::ok(Refusal {
                    reason: "pong".to_owned(),
                    ..Refusal::default()
                })
            }),
        )
}

/// A watch as a consumer writes one: it keeps its client and its open stream.
struct Follow {
    client: Client,
    stream: ServerStream<ResponseBody, RefusalView<'static>>,
}

impl Follow {
    async fn open(client: Client) -> Result<Self, ConnectError> {
        let stream = call_server_stream::<_, Refusal, RefusalView>(
            &client.transport(),
            &client.config(),
            COUNT,
            Refusal::default(),
            CallOptions::default(),
        )
        .await?;
        Ok(Self { client, stream })
    }

    /// A call that borrows the whole watch, stream included, across an await. Its future is `Send` only if `Follow` is `Sync`, so
    /// only if the response body is.
    async fn ping(&self) -> Result<String, ConnectError> {
        let r = call_unary::<_, Refusal, RefusalView>(
            &self.client.transport(),
            &self.client.config(),
            PING,
            Refusal::default(),
            CallOptions::default(),
        )
        .await?;
        Ok(r.into_view().to_owned_message().reason)
    }

    /// Reads the stream to its end, pinging between messages.
    async fn run(mut self) -> Result<Vec<String>, ConnectError> {
        let mut seen = Vec::new();
        while let Some(m) = self.stream.message().await? {
            seen.push(m.to_owned_message().reason);
            assert_eq!(self.ping().await?, "pong");
        }
        Ok(seen)
    }
}

async fn follow_in_a_spawned_task(client: Client) -> Vec<String> {
    tokio::spawn(async move { Follow::open(client).await?.run().await })
        .await
        .expect("the task does not panic")
        .expect("the stream reads to its end")
}

fn counted() -> Vec<String> {
    (0..MESSAGES).map(|n| n.to_string()).collect()
}

#[tokio::test]
async fn a_stream_in_process_is_followed_from_a_spawned_task_across_an_await() {
    assert_eq!(
        follow_in_a_spawned_task(in_process(router())).await,
        counted()
    );
}

#[tokio::test]
async fn a_stream_through_a_socket_is_followed_from_a_spawned_task_across_an_await() {
    let dir = tempfile::tempdir().unwrap();
    let socket = dir.path().join("b.sock");
    let (stop, stopped) = tokio::sync::oneshot::channel::<()>();
    let server = tokio::spawn(serve(ServerConfig::new(&socket), router(), async {
        let _ = stopped.await;
    }));
    for _ in 0..100 {
        if socket.exists() {
            break;
        }
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
    assert_eq!(follow_in_a_spawned_task(dial(&socket)).await, counted());
    stop.send(()).unwrap();
    server.await.unwrap().unwrap();
}
