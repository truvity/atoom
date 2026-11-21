# 0009. Always the ConnectRPC contract, also in one process

Status: accepted, 2026-11-20

## Decision

- A consumer calls a port or a block **through its generated client, always**, also when the driver runs in the same process and is written in the same language.
- **In-process, the driver's handler is joined to the client by an in-memory transport:**
  - In Go, the Connect handler is mounted on an in-memory HTTP transport.
  - In Rust, the generated client takes `connectrpc::client::ServiceTransport` over `connectrpc::ConnectRpcService` built from the driver's `router()`. Both types are in connectrpc 0.9.1.
- A probe on 2026-11-20 measured this in Rust. The memory store driver, called through `ItemServiceClient`, took about 1.9 µs per call (release build, 2,000 calls). The error codes were kept.
- **Moving a driver between in-process, a socket and a Lambda extension is configuration, not code.** The serialization, interceptors, error codes and streaming are the same on every path, and so is the kit.
- **In-process, two things come from the host, not the transport:**
  - **the deadline:** `call::scope` in Rust, the caller's context in Go;
  - **who is calling:** a socket's peer credentials don't exist in-process, so the host's rules hook decides who may call.
- **Calling a handler directly is not used,** except on a hot path where a measurement shows the cost. A direct call skips interceptors and checks, and the caller and the driver share message objects.
- **The one named exception is Cedar** ([0011](0011-cedar-as-a-library.md)).

## Consequences

- `socket` gains a small in-process helper in Rust, and the probe becomes its test.
- In Go, `connect` and `protobuf` resolve to the highest version required in the build, so a consumer on a newer v1 version works. Rust's `connectrpc` is below 1.0, so its consumers use atoom's pinned minor version until connectrpc reaches 1.0. Each release states its runtime pins.
