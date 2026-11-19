# store/dynamodb

The DynamoDB item driver of atoom: the [item port](../README.md) over one DynamoDB table. Versioned items with an expiry and declared indexes, conditions and all-or-none writes through DynamoDB transactions, strongly consistent reads when the caller asks for a linearizable one, and DynamoDB's time to live for the space of expired items.

| | |
| --- | --- |
| Go module | `github.com/truvity/atoom/store/dynamodb` (requires the port, the common types and the AWS SDK for Go v2; no other driver) |
| Library | package `dynamodb`: `New(Options)` takes a DynamoDB client (`API`; `*dynamodb.Client` is one) and a table name and returns a `Service`; `Service.Handler()` is the Connect handler to mount in-process or on a server; `Run(ctx, opts, host)` serves it on a server the host provides; `EnsureTable` and `CheckTable` make and check the table |
| Thin main | `cmd/dynamodb`: `dynamodb serve --socket PATH --table NAME [--create-table]`, `dynamodb create-table`, `dynamodb check`, `dynamodb version` |
| Documentation | [docs/layout.md](docs/layout.md) (the table, how each call maps, the limits), [docs/research](docs/research) |
| Where it came from | [PROVENANCE.md](PROVENANCE.md) |

It serves `Get`, `Query` (by key prefix and by index entry) and `Transact` (conditions, puts, deletes). It does **not** keep the change log: `Capabilities` says `tail_log: false`, `TailLog` answers `unimplemented`, and a put or a delete that asks for a log row is refused (`unimplemented`, reason `log`) and writes nothing ([docs/layout.md](docs/layout.md), "The change log"). The conformance kit skips the cases that need the log and runs the rest.

## In process (a Lambda function)

There are no sidecars on Lambda, so the function links the library and mounts it on an in-memory transport.

```go
cfg, _ := config.LoadDefaultConfig(ctx)
svc, err := dynamodb.New(dynamodb.Options{
    DynamoDB: awsdynamodb.NewFromConfig(cfg),
    Table:    "sessions",
})
mux := http.NewServeMux()
mux.Handle(svc.Handler())
client := storev1alpha1connect.NewItemServiceClient(inMemoryHTTPClient(mux), "http://store")
```

The role needs `dynamodb:GetItem`, `BatchGetItem`, `Query`, `PutItem`, `DeleteItem` and `TransactWriteItems` (which needs the item-level actions it carries) on the table, and nothing on any index: there is none. The table is made by the deployment (Terraform, CloudFormation) in the shape of [docs/layout.md](docs/layout.md), "The table", or by `dynamodb create-table` / `EnsureTable` for a test or a first start; the driver never makes a table by itself.

## What it keeps

- **An item is one DynamoDB item; an index entry is another.** Entries are written and deleted in the transaction of their item, so an index is exactly as consistent as the table, and a strongly consistent query by index is possible (a global secondary index cannot do it).
- **A condition is judged on what the write is made on.** `Transact` reads what it touches, checks the conditions, and writes with a condition that the keys are still as read; a write that loses a race reads and decides again, so a counter or a lease taken by many callers has one winner each round.
- **One put with one condition is one conditional `PutItem`**, not a transaction (half the cost).
- **Expiry is checked on read**; DynamoDB's time to live frees the space, "typically within a few days" of the expiry, so an item past its expiry is absent whether or not it is still there.
- **A `Transact` is at most 100 writes** (one for each put, delete and index entry written or removed, one for each condition on a key that is not written). More is refused (`invalid_argument`, `batch`) and never split, because a split is not all or none. The contract's limit is 256 operations.
- **What DynamoDB said stays in the journal.** A refusal carries a code and the word `engine`, never DynamoDB's text.

## Tests

`just conformance` runs the item kit (`store-conformance`) against this driver's thin main, through its socket, with DynamoDB Local in a container (the multi-arch image, pinned by digest, one container at a time, removed at the end), and then the Go tests of this module against the same container. On a machine with no container runtime (the macOS CI runners) both are skipped and say so, and `go test` runs the tests that need no table: the rules, the encoding of an item. `ATOOM_REQUIRE_DYNAMODB_LOCAL=1` makes a missing runtime a failure; CI sets it on Linux. No AWS account and no network call to AWS are used. On DynamoDB Local the case `version_never_decreases` of the kit is retried once (its background sweep of expired items can free a row between two steps; issue [#14](https://github.com/truvity/atoom/issues/14)), and the run prints `RETRIED version_never_decreases: ...` when it does; see the [kit's suite page](../conformance/docs/suite.md).
