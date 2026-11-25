# transport/sqs

The Amazon SQS driver of the [transport port](../docs/transport-port.md): the transport contract over one SQS FIFO queue. A Lambda function (or any Go host) links it and mounts its handler in its own process, so that audit on Lambda emits with no spool sidecar ([record 0022](../../docs/decisions/0022-the-blocks-order-after-0-7-0.md), D44 A; [record 0023](../../docs/decisions/0023-session-to-done-first.md), session's step 5); the thin main serves both sides on one UNIX socket. It passes the [conformance suite](../conformance/docs/suite.md), the redelivery and duplicate case included (D50 A).

| | |
| --- | --- |
| Go module | `github.com/truvity/atoom/transport/sqs` (requires the port, the common types, [`socket`](../../socket/README.md) and the AWS SDK for Go v2; no other driver) |
| Library | package `sqs`: `New(Options)` takes an SQS client (`API`; `*sqs.Client` is one), the URL of a FIFO queue and the emitter (`Emitter`, or `Identify`), and returns a `Service`; `Service.Handler()` is the Connect handler to mount in-process or on a server; `Run(ctx, opts, host)` serves it on a server the host provides |
| Thin main | `cmd/sqs`: `sqs serve --socket PATH --queue-url URL [--emitter NAME] [--message-group ID] [--wait-time D\|0s] [--visibility-timeout D]`, `sqs version` (and `--version`) |
| Guard flags | `--allow-uid UID` (repeatable; without it only this process's uid may call), `--allow-any-uid`, `--socket-gid GID`, `--socket-mode MODE`: the [socket guard](../../docs/socket-guard.md) |
| Documentation | [docs/sqs.md](docs/sqs.md): how the contract maps to SQS, the window of a FIFO queue, the redelivery and the writer's dedup, the IAM actions |
| Where it came from | [provenance](../../docs/provenance.md#transport-sqs) |

## In process (a Lambda function)

```go
cfg, _ := config.LoadDefaultConfig(ctx)
svc, err := sqs.New(sqs.Options{
    SQS:      awssqs.NewFromConfig(cfg),
    QueueURL: queueURL,            // a FIFO queue: its URL ends in .fifo
    Emitter:  "example@function",  // the host's name for itself
})
path, h := svc.Handler()
// Mount (path, h) on an in-memory transport (socket.InProcess) and give the client to the emitter. No extension, no layer.
```

An emitting function needs `sqs:SendMessage` on the queue (and `kms:GenerateDataKey` on its key when the queue is encrypted with a customer key); a consumer needs `sqs:ReceiveMessage` and `sqs:DeleteMessage` (and `kms:Decrypt`).

## What it keeps

- **The order of the emits**, within the one message group of the host (`MessageGroupID`, `transport` by default): the queue must be FIFO, and a standard queue is refused at `New`.
- **A retry is one message.** The deduplication id is the SHA-256 of the message's uid, and SQS drops a send of a seen id within five minutes.
- **What it delivered, until it is acknowledged**: sent first on the next stream under the same ids, with no new receive; a message SQS hands out again after its visibility timeout takes the newer receipt handle and is not delivered twice on a stream.
- **A window of one receive.** A FIFO queue hands out nothing of a group while part of it is in flight, so the driver delivers at most one receive (ten messages) ahead of the acknowledgements: a consumer acknowledges as it seals.
- **What SQS said stays in the journal.** A refusal carries a code and the reason `engine`, never SQS's text.

## Tests

`go test ./...` runs the driver over an in-process fake of a FIFO queue (`internal/sqsfake`: the order, the group's window, visibility, receipt handles, deduplication, attempt ids) through the Connect handler and through the thin main's socket, on every platform. With `ATOOM_SQS_ENDPOINT` set, the tests of `local_test.go` run through the real AWS SDK client against ElasticMQ; `just conformance` starts ElasticMQ in a container, runs the transport kit against the thin main over it and runs those tests (CI requires it on Linux). No AWS account is used.
