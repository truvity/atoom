# transport/nats

The NATS JetStream driver of the [transport port](../docs/transport-port.md): the transport contract over one JetStream stream (the log) and one durable consumer (its writer). A Go host links it and mounts its handler in its own process, so that its audit records reach the writer with no sidecar ([record 0023](../../docs/decisions/0023-session-to-done-first.md), the owner's D65 A); the thin main serves both sides on one UNIX socket. It passes the [conformance suite](../conformance/docs/suite.md), the redelivery and duplicate case included.

| | |
| --- | --- |
| Go module | `github.com/truvity/atoom/transport/nats` (requires the port, the common types, [`socket`](../../socket/README.md) and the NATS client `nats.go`; its tests run the NATS server embedded; no other driver) |
| Library | package `nats`: `New(Options)` takes a JetStream context over the host's connection, the stream, the subject, the consumer and the emitter (`Emitter`, or `Identify` for a host that tells its callers apart), and returns a `Service`; `Service.Handler()` is the Connect handler to mount in-process or on a server; `Run(ctx, opts, host)` serves it on a server the host provides |
| Thin main | `cmd/nats`: `nats serve --socket PATH --url URL --stream NAME --subject SUBJECT --consumer NAME [--create] [--ack-wait D] [--creds FILE \| --token-file FILE] [--tls-ca FILE] [--emitter NAME] [--wait-time D]`, `nats version` (and `--version`) |
| Guard flags | `--allow-uid UID` (repeatable; without it only this process's uid may call), `--allow-any-uid`, `--socket-gid GID`, `--socket-mode MODE`: the [socket guard](../../docs/socket-guard.md) |
| Documentation | [docs/nats.md](docs/nats.md): how the contract maps to JetStream, the redelivery and the writer's dedup, the permissions |
| Where it came from | [provenance](../../docs/provenance.md#transport-nats) |

## In process

```go
nc, _ := natsgo.Connect(url, natsgo.Token(token)) // the host's connection, with the credentials its deployment gives it
js, _ := jetstream.New(nc)
svc, err := nats.New(nats.Options{
    JetStream: js,
    Stream:    "EXAMPLE",           // the stream of the log
    Subject:   "example.records",   // the subject the stream captures
    Consumer:  "writer",            // the writer's durable consumer
    Emitter:   "example@host",      // the host's name for itself
})
path, h := svc.Handler()
// Mount (path, h) on an in-memory transport (socket.InProcess) and give the client to the emitter.
```

**The connection is the host's.** A broker that maps a workload's identity token to a NATS user takes the token as the connection's token (`natsgo.Token`, or `natsgo.TokenHandler` to read a renewed one at each connect; the thin main's `--token-file` does that); a credentials file is `natsgo.UserCredentials` (`--creds`). The driver takes the JetStream context and knows nothing of how the connection was authorised ([record 0021](../../docs/decisions/0021-drivers-know-no-deployment-shapes.md)).

## What it keeps

- **The order of the publishes, and the stream sequence as the delivery id.** The id is the same for a message that comes again, after a restart of the driver too, and grows with the order.
- **A retry is one message.** An emit publishes with `Nats-Msg-Id` set to the message's uid, and JetStream drops a publish of a uid it has seen within the stream's duplicate window.
- **What it delivered, until it is acknowledged.** A message whose stream ended comes first on the next stream under its id, with no new pull; one that JetStream delivers again after the ack wait takes the place of the held one and is not delivered twice on a stream. An ack waits for JetStream's answer.
- **Nothing on disk of its own.** The stream's storage is the server's; the held deliveries are in memory, and a restart gets them again from JetStream after the ack wait.
- **What NATS said stays in the journal.** A refusal carries a code and the reason `engine`, never NATS's text.

## Tests

`go test ./...` runs the driver against a real NATS server with JetStream embedded in the test process (`internal/natstest`), through the Connect handler on an in-memory transport and through the UNIX socket of the thin main (with a token read from a file, and the guard). `just conformance` runs the transport kit against the thin main over the same server run as a process (`internal/natstest/cmd/natsserver`). No container and no network beyond the loopback.
