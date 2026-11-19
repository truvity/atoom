# Provenance

This library is new code written for atoom (the extraction plan, step 3b, [docs/plan.md](../../docs/plan.md)); no code was copied from sluis. Its contract is the item port of this repository, and its rules are the ones the Rust drivers apply.

| Here | From | Changed |
| --- | --- | --- |
| `rules.go` | `store/conformance/src/rules.rs` (this repository) | ported to Go: the key, the page size, the page token and the checks of a `Transact`; the limits of this engine (writes in a transaction, value and index sizes) are new |
| `service.go`, `transact.go`, `encode.go`, `dynamodb.go` | new | the semantics follow `store/memory/src/lib.rs`, the reference engine; the DynamoDB mapping is [docs/layout.md](docs/layout.md) |
| `cmd/dynamodb/` | `key/software/cmd/software/main.go` | the socket code is the same (a driver requires no other driver, so each thin main holds its own copy); the flags, the AWS client and the table commands are new |
| `docs/layout.md` | new | |
| `docs/research/` | new, 2026-11-19; reads the DynamoDB developer guide (quotas, time to live, transactions) | cites, does not copy |

What stays in the host: the socket's peer check, the registry owner, the command line of a larger system, and the table and its IAM policy (deployment code).
