# A driver in the same process

[Record 0009](../../docs/decisions/0009-connectrpc-in-process.md): a consumer calls a port **through its generated client, always**, also when the driver runs in the same process and in the same language. The client and the driver's handler are joined by an in-memory transport instead of a socket, so the serialization, the interceptors, the error codes and the streaming are the ones of a socket, and moving a driver between a process, a socket and a Lambda extension is configuration, not code.

This page has the two ways: Rust, with the helper of this crate, and Go, with the Connect handler on an in-memory transport. Both have the same two limits ([what does not cross](#what-does-not-exist-in-process)).

## Rust: `atoom_socket::in_process`

`in_process(router)` takes the `Router` of a driver (`MemoryEngine::new().router()`, `Service(dir).router()`) and returns a `Client`: the **same type** that `dial(path)` returns. It has the same two methods, so the generated client is built the same way:

```rust
use atoom_socket::{dial, in_process};

// One value of the consumer's configuration says which; nothing else in the code depends on it.
let client = match &config.source_socket {
    Some(path) => dial(path),                                  // a driver in a process of its own
    None => in_process(Service(Dir::open(&config.dir)?).router()), // the driver, linked in
};
let rpc = SourceServiceClient::new(client.transport(), client.config());
let doc = rpc.read(ReadRequest { name: "boundary".into(), ..Default::default() }).await?;
```

- **How it works.** The transport is `connectrpc::client::ServiceTransport` over `connectrpc::ConnectRpcService::new(router)`, both in connectrpc 0.9.1. A call is encoded, goes through the service's interceptors and the handler, and the answer is decoded, with no socket and no connection. `Client::socket()` is `None` for such a client.
- **It must run inside a Tokio runtime,** as a call through a socket does.
- **The response body is `Send + Sync + 'static`** (`atoom_socket::ResponseBody`, the body of both clients), so a response stream (`ServerStream<ResponseBody, _>`) can be kept in a struct that a spawned task borrows across an await, a watch that asks its own client again while it follows its stream, say. `connectrpc`'s in-process body is `Send` only; the crate wraps it in `sync_wrapper::SyncWrapper`, which gives no access through `&` at all, so it is `Sync` with no lock. `socket/src/body_tests.rs` proves it through a socket and in process.
- **`in_process_with(router, InProcessConfig)`** sets what a host sets around its handlers: `with_default_deadline(d)` (the deadline of a call that carries none, 30 seconds as for `ServerConfig::default_deadline`) and `with_interceptor(i)` (the host's rules, below).
- **Cost.** One call through `ItemServiceClient` over the memory driver took 2.3 µs in a release build (the mean of 2,000 calls after 200 of warm-up; the record's probe measured about 1.9 µs; a debug build takes about 13 µs). The tests print it: `cargo test --release -p atoom-store-memory --test in_process -- --nocapture` and `cargo test --release -p atoom-socket the_cost -- --nocapture`. A direct call of the handler would save about as much again and skips the interceptors, so it is not used ([0009](../../docs/decisions/0009-connectrpc-in-process.md), roads not taken).

### The deadline and the correlation id

They come from the caller, as on a socket ([the call context](../../docs/call-context.md)):

- A call made inside a handler of an `atoom_socket::serve` server, or inside `call::scope(Call::new().with_correlation(id).with_timeout(d), fut)`, carries what is left of the deadline and the id. The service reads them as a server does: the handler sees the id (`call::correlation_of(ctx.extensions())`, or `call::correlation()` from any code inside it) and holds the deadline (`ctx.time_remaining()`), and a handler that calls on through another `in_process` or `dial` client hands them down.
- A callee never extends its caller's deadline, a deadline that passes cancels the handler (`deadline_exceeded`), an invalid id is dropped, and a call made outside any scope carries neither and gets the default deadline.

`socket/src/in_process_tests.rs` proves each of these through the helper, and `store/memory/tests/in_process.rs` proves the generated `ItemServiceClient` over it, with the error code and the refusal reason of an absent key kept.

### What does not exist in process

- **A peer.** A socket's peer credentials come from the kernel; a caller in the same process has none. `atoom_socket::peer(ctx.extensions())` is `None`, and the socket guard (`Allow`) is not there. **Who may call is the host's rule:** the host decides in an interceptor and adds it with `InProcessConfig::with_interceptor`. The interceptor runs before every call, in the order added, and may refuse it (`permission_denied`, reason `caller` by convention). A host that gives each in-process client to one consumer has already decided, by giving it. The test `there_is_no_peer_and_the_hosts_interceptor_decides_who_may_call` shows the shape.
- **A second wall.** There is no socket file and no mode. Anything in the process that holds the `Client` can call the driver.

## Go: `socket.InProcess`

The generated Connect client takes an `http.Client`, so the handler is served on a listener whose connections are in memory and the client dials it. `socket.InProcess(path, handler)` does that: it serves the handler (HTTP/1.1 and HTTP/2 without TLS) on a listener of `net.Pipe` pairs, wrapped in `call.Inherit`, and returns the `http.Client` that reaches it (HTTP/2, so a bidirectional stream works) and the function that stops it. A driver or a block gives its handler (`Service.Handler()` in `key/awskms`, `store/dynamodb` and `source/file`, `Block.Handler()` in `blocks/seal`):

```go
b, err := seal.New(ctx, opts)                  // or a driver: file.New, awskms.New, ...
hc, stop := socket.InProcess(b.Handler())
defer stop()
client := sealv1alpha1connect.NewSealServiceClient(hc, "http://seal", call.ClientOptions()...)
```

`socket.Client(path)` is the `http.Client` of the same handler behind a socket (HTTP/2 without TLS, as `socket.Serve` and the Rust thin mains serve), so a host switches between the two by configuration. `blocks/seal` uses both: its kit command hosts the block in its process over a key driver and a store driver on sockets, and calls the block in-process. `socket/inprocess_test.go` proves the correlation id, HTTP/2, many calls at once and the stop; `source/file/service_test.go` proves the deadline and the id through the same transport, written out by hand in `source/file/memnet_test.go`.

- **One value switches it.** The client of a socket differs only in its `http.Client` (a transport that dials the socket, with unencrypted HTTP/2) and the URL; the generated client and every call are the same. A consumer builds the `http.Client` from its configuration.
- **Streaming works**: a server stream such as `Watch`, and a bidirectional stream, which needs HTTP/2; `InProcess` and `Client` speak HTTP/2 without TLS (`http.Protocols`, `SetUnencryptedHTTP2`), as the socket does.
- **The deadline and the correlation id come from the context.** The Connect client sends the deadline of the context of the call as `Connect-Timeout-Ms` and the Connect handler turns it back into the deadline of the handler's context, with no option. The id travels with `call.ClientOptions()` on the client and `call.Inherit` around the handler ([the call context](../../docs/call-context.md)). `TestTheDeadlineAndTheCorrelationIDOfTheCallersContextReachTheHandlerInProcess` in `source/file` proves both. Go sets no default deadline: a host that wants one wraps the context.
- **There is no peer.** `socket.PeerFromContext` returns nothing, and `socket.Serve` and its guard are not involved. The host's rule is a Connect interceptor passed as a handler option (`Service.Handler(connect.WithInterceptors(...))`, or `Host.HandlerOptions`).
- **The helper is `socket.InProcess`**, in the module `socket` next to `Serve`.
