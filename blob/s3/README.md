# blob/s3

The Amazon S3 driver of the [blob port](../docs/blob-port.md): the blob contract over one S3 bucket, under a key prefix. A Go host (a Lambda function, a service on Kubernetes) links it and mounts its handler in its own process, so that it keeps a log's objects in S3 with no sidecar (step B12, the owner's D112 A, [record 0025](../../docs/decisions/0025-generic-blob-transport-and-index.md)); the thin main serves it on one UNIX socket. It passes the [conformance suite](../conformance/docs/suite.md).

| | |
| --- | --- |
| Go module | `github.com/truvity/atoom/blob/s3` (requires the port, the common types, [`socket`](../../socket/README.md) and the AWS SDK for Go v2; no other driver) |
| Library | package `s3`: `New(Options)` takes an S3 client (`API`; `*s3.Client` is one), the bucket, a key prefix and the pointer names, and returns a `Service`; `Service.Handler()` is the Connect handler to mount in-process or on a server; `Run(ctx, opts, host)` serves it on a server the host provides |
| Thin main | `cmd/s3`: `s3 serve --socket PATH --bucket NAME [--prefix P/] [--pointer NAME]... [--path-style]`, `s3 version` (and `--version`) |
| Guard flags | `--allow-uid UID` (repeatable; without it only this process's uid may call), `--allow-any-uid`, `--socket-gid GID`, `--socket-mode MODE`: the [socket guard](../../docs/socket-guard.md) |
| Documentation | [docs/s3.md](docs/s3.md): how the contract maps to S3, the conditional writes, the IAM actions |
| Where it came from | [provenance](../../docs/provenance.md#blob-s3) |

## In process

```go
cfg, _ := config.LoadDefaultConfig(ctx)
svc, err := s3.New(s3.Options{
    S3:       awss3.NewFromConfig(cfg),
    Bucket:   bucket,
    Prefix:   "example/",                        // empty, or segments ending in /
    Pointers: []string{"checkpoint", "state"},   // the host's pointer names; none: every object is written once
})
path, h := svc.Handler()
// Mount (path, h) on an in-memory transport (socket.InProcess) and give the client to the writer. No extension, no layer.
```

A writer needs `s3:PutObject` and `s3:GetObject` on the prefix's keys and `s3:ListBucket` on the bucket (with the prefix as its condition); a reader needs only the last two. The driver never deletes.

## What it keeps

- **The write-once fence.** `Create` is a `PutObject` with `If-None-Match: *`: of any number of racing creators exactly one wins.
- **Only the declared pointers move,** and by a compare-and-swap: the driver reads the pointer, compares the port's tag with `if_match`, and writes with `If-Match` on the S3 ETag it read, so that a write in between makes S3 refuse the replace.
- **The tag is the SHA-384 of the bytes**, computed by the driver, the same for the same bytes at any path, as for the `fs` drivers.
- **What S3 said stays in the journal.** A refusal carries a code and the reason `engine`, never S3's text.

## Tests

`go test ./...` runs the driver over an in-process fake of S3 (`internal/s3fake`: the two conditional writes, `GetObject`, `ListObjectsV2` with its pages) through the real AWS SDK client, through the Connect handler and through the thin main's socket, on every platform. `just conformance` runs the blob kit against the thin main over the same fake, run as a process. With `ATOOM_S3_ENDPOINT` and `ATOOM_S3_BUCKET` set (and the `AWS_*` credentials), the conformance run and `TestAgainstARealServer` also run against that S3-compatible server. No AWS account is used.
