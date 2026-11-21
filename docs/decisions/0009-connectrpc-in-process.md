# 0009. Always the ConnectRPC contract, also in one process

Status: accepted, 2026-11-20

## Decision

- A consumer calls a port or a block **through its generated client, always**, also when the driver runs in the same process and is written in the same language.
- **In-process, the driver's handler is joined to the client by an in-memory transport:**
  - In Go, the Connect handler is mounted on an in-memory HTTP transport.
  - In Rust, the generated client takes `connectrpc::client::ServiceTransport` over `connectrpc::ConnectRpcService` built from the driver's `router()`. Both types are in connectrpc 0.9.1.
- A probe measured this in Rust: the memory store driver, called through `ItemServiceClient`, took about 1.9 µs per call (release build, 2,000 calls), and the error codes were kept.
- **Moving a driver between in-process, a socket and a Lambda extension is configuration, not code.** The serialization, interceptors, error codes and streaming are the same on every path, and so is the kit.
- **In-process, two things come from the host, not the transport:**
  - **the deadline:** `call::scope` in Rust, the caller's context in Go;
  - **who is calling:** a socket's peer credentials do not exist in-process, so the host's rules hook decides who may call.
- **Every call carries a deadline and a correlation id** ([call context](../call-context.md)). The correlation id travels in the header `Atoom-Correlation-Id`. A server accepts the old name, `Sluis-Correlation-Id`, for one release after the rename. The rename is planned (step B14 of the [plan](../plan.md)); a neutral name belongs on a contract that several products share.
- **Calling a handler directly is not used,** except on a hot path where a measurement shows the cost. A direct call skips interceptors and checks, and the caller and the driver share message objects.
- **The one named exception is Cedar** ([0011](0011-cedar-as-a-library.md)).

## Consequences

- `socket` gains a small in-process helper in Rust, and the probe becomes its test (step B1 of the plan).
- In Go, `connect` and `protobuf` resolve to the highest version required in the build, so a consumer on a newer v1 version works. Rust's `connectrpc` is below 1.0, so its consumers use atoom's pinned minor version until connectrpc reaches 1.0. Each release states its runtime pins ([0015](0015-versioning-and-consumption.md)).

## Roads not taken

- **A direct call in-process.** Faster by about a microsecond; it skips interceptors, checks and error mapping, and it makes in-process a different code path from a socket.
- **A different contract in-process.** Two contracts to keep in agreement.
