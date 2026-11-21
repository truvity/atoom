# The call context: the deadline and the correlation id

A call that crosses a socket carries two things that belong to the request and not to the call: **how long it may still take** and **which request it is part of**. Every client of an atoom port sends both, every thin main of atoom reads both, and a handler that calls on hands both down. The same rules hold in Rust (the crate [`atoom-socket`](../socket/README.md), module `call`) and in Go (the module `github.com/truvity/atoom/common`, package `call`; `socket.Serve` applies it). They are the rules sluis first wrote for its own kits, moved here so that a consumer's client is not a copy of atoom's. This page is their one home: each [port page](architecture.md#ports) links here and does not repeat them.

## The deadline

- **On the wire** it is the Connect header **`Connect-Timeout-Ms`**: what is left of the caller's deadline, in milliseconds, at most ten digits (Connect protocol, "Timeouts"). It is not a header of atoom's; atoom only makes sure it is sent and honoured.
- **A server honours it.** The Rust server (`atoom_socket::serve`) and every Connect handler in Go turn the header into the deadline of the call: the handler is cancelled when it passes, and the caller gets `deadline_exceeded`. A call that carries none gets the server's default: 30 seconds in Rust (`ServerConfig::default_deadline`); Go sets none, so a Go host that wants one wraps its context.
- **A client sends what is left.** In Go the Connect client sends the deadline of the context of the call, with no option. In Rust a client of `atoom_socket::dial` sends the deadline of the call being served, or of the [`call::scope`](#the-calling-code-that-is-not-a-handler) it is in, as what is left of it at the moment of sending.
- **A callee never extends its caller's deadline.** A timeout that the call itself names stays when it is shorter; a longer one is cut to what is left. A Go context can only be narrowed, so the same holds there without code.
- **A handler hands down what is left.** In Rust the server keeps the deadline of the call in a task-local around the handler, so every call the handler makes through a client of `dial` carries what is left, with no plumbing. In Go the handler calls on with the context it was given.
- A deadline that has passed is sent as `0`, and the callee answers `deadline_exceeded` at once.

## The correlation id

- **On the wire** it is the header **`Atoom-Correlation-Id`** (HTTP header names are not case sensitive; Rust spells it in lower case). It is a constant of one line in each language (`call::HEADER_CORRELATION`, `call.HeaderCorrelation`).
  - **Built today:** the header is still named `Sluis-Correlation-Id`, the name sluis chose first.
  - **Planned (step B14 of the [plan](plan.md), [record 0009](decisions/0009-connectrpc-in-process.md)):** the name becomes `Atoom-Correlation-Id`, a neutral name on a contract that sluis, session and audit share. A server accepts the old name for one release and reads the new name first; a client sends the new name only. A product name does not belong on a shared contract.
- **The format** is 1 to 64 characters of `A-Z`, `a-z`, `0-9`, `-` and `_`: a UUID fits, so does a token. An id in any other shape is **dropped** by the server, never made the id of the handler, never logged, never echoed and never passed on, so a hostile header cannot reach a log line or a record. A client does the same: an id that is not valid is not set, and is not sent.
- **Atoom makes no ids.** The first hop of a request makes it (for example, sluis's `entry` makes a UUID version 4 for every request) and every later hop passes it on. A caller outside any handler gives its own: in Rust `call::scope(Call::new().with_correlation(id)…)`, in Go `call.WithCorrelationID(ctx, id)`.
- **A server reads it** into the handler: Rust `call::correlation_of(ctx.extensions())` (the request) or `call::correlation()` (from any code inside the handler); Go `call.CorrelationID(ctx)`. `None` or `""` when the caller sent none or an invalid one.
- **A client sends it.** Rust: every call made through a client of `dial` inside a handler or a `call::scope`. Go: the options `call.ClientOptions()` of a generated client (`New<Service>Client(hc, url, call.ClientOptions()...)`) put the id of the context on every call, unary and streaming. The Go clients of atoom's own kits (`key/conformance.Dial`) already carry them.
- The server does not echo the id in a response header.

## The calling code that is not a handler

A background loop, a command and a test are not inside a handler, so nothing is inherited. They state the call themselves:

```rust
use atoom_socket::call::{self, Call};

let call = Call::new().with_correlation(id).with_timeout(Duration::from_secs(30));
let answer = call::scope(call, client.put(doc)).await;   // carries the id and what is left of 30 s
```

```go
ctx, cancel := context.WithTimeout(call.WithCorrelationID(ctx, id), 30*time.Second)
defer cancel()
res, err := client.Get(ctx, connect.NewRequest(&req))    // a client made with call.ClientOptions()
```

## In process

A driver mounted in a process of its own (a Lambda function, a test) is called with no socket in between: the handler gets the context of its caller as it is. A host that serves the handler over HTTP itself wraps it with `call.Inherit` (Go) so that the correlation id of the request reaches the handler; the Connect handler reads the deadline on its own.

## What is tested

- `atoom-socket` and `socket` hold the rules: the header is sent and read, a handler hands both down to a second server, a shorter timeout stays and a longer one is cut, an invalid id is dropped by both sides, a passed deadline answers `deadline_exceeded`, and a server that admits any uid hands the call down too.
- Each port proves its own client: a test per port (Rust `<port>/conformance/tests/call.rs`, Go `<port>/call_test.go`, `key/conformance/call_test.go`) calls every method of its generated clients, unary and streaming, against a server that records what arrived, and checks the id and the timeout; and that a call made outside any scope carries neither. The Rust recorder is `atoom_socket::testing::Recorder` (feature `testing`) and the Go one is `common/call/calltest`.
