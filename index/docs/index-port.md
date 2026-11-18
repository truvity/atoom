# The index port

`atoom.index.v1alpha1`: an **index** makes the records of an append-only audit log searchable. It is derived data: whatever it holds can be rebuilt from the log. The contract is [index.proto](../proto/atoom/index/v1alpha1/index.proto); this page is its rules in prose, and the conformance kit ([README](../conformance/README.md), [the suite](../conformance/docs/suite.md)) holds every driver to them.

Two services, which a host puts on two sockets, so that a driver serves the read side with no way to write:

| Service | Call | Who | What it does |
| --- | --- | --- | --- |
| `IndexService` | `GetCursor` | the indexer | where the indexer stands in one log |
| `IndexService` | `Apply` | the indexer | the rows of one batch of one log and, in the same step, the next cursor |
| `IndexReadService` | `Search` | the reader | rows of named partitions and tenants, newest first |

A **log** is named by a project, a log kind and the log's id (`LogRef`); a **partition** of the index is a project and a log kind. A **row** is one indexed record: the fixed columns (tenant, hash, sequence number, times, boundary, action, class, activity, status, actor, trace id, record id, product) and the record itself, once, as its canonical bytes.

## The contract

- **The cursor** of a log is the last checkpoint the indexer verified and indexed to: the tree size, the root hash (32 bytes) and a state that is opaque to the index. A log that was never indexed has size 0 and no root.
- **`Apply` moves the cursor forward only, and only from where the caller read it.** `expect_size` is the size of the cursor the caller read; the write is refused (`aborted`, reason `version`, field `expect_size`) when the cursor has moved, so that a second indexer during a deploy cannot move it back. The next cursor must be larger than `expect_size` and have a root of 32 bytes. The rows and the cursor are one transaction. A request with no cursor only adds rows.
- **A row is idempotent**: one whose tenant and hash (SHA-384 of the record's canonical bytes) are already in the log is a repeat, counted in `duplicates` and stored once.
- **Which log kinds have an index**: `audit` and `issuance`. The usage log has none: a write to it is `permission_denied` / `rule`, and a query that names it is `invalid_argument`.
- **A query names its partitions and its tenants.** Both are mandatory: they are the grant of the reader, a filter that no query can leave out. `*` as a tenant means every tenant and stands alone. A row of another partition or tenant is never returned.
- **A page** is newest first (`time_ms`, then the order of arrival), at most `page_size` rows (0 is 50 for the sqlite driver, at most 200), and the token of the next page is empty on the last page. The time range is `from_ms` at or after and `to_ms` before.
- **Exact filters** for the action, the status code, the status id, the actor, the trace id and the record id.
- **A word search** (`q`) reaches only the fields that the catalogue of the records says are searchable (today: the action, the status code and the product). Words are the alphanumeric runs of a field, folded for case and diacritics, with no stemming and no substring search; all the words of a query are required; a trailing `*` makes a prefix.

## Refusals

Every refusal is a Connect error with the `Refusal` detail of [common](../../common/docs/refusal.md): a reason from a closed list and the path of the field at fault, never a value.

| Code | Reason | When |
| --- | --- | --- |
| `invalid_argument` | `argument` | a field that is not acceptable; the field path says which (`log`, `next.size`, `next.root`, `rows.tenant`, `rows.hash`, `rows.record`, `partitions`, `tenants`, `q`, `page_token`, ...) |
| `aborted` | `version` | `Apply` at a cursor that is not where the caller read it (`expect_size`) |
| `permission_denied` | `rule` | `Apply` to a log kind that has no index (`log.kind`) |
| `unavailable` | `engine` | the backend failed; the message is the reason only |

## Drivers

| Driver | Backend | Where it fits |
| --- | --- | --- |
| [index/sqlite](../sqlite/README.md) | one SQLite file with FTS5 | a boundary whose index fits on one disk |

## A driver of your own

Serve `IndexService` and `IndexReadService` (Go: implement the handlers of `indexv1alpha1connect`; Rust: implement the traits of the crate `atoom-index`), serve them on a socket or mount them in a process, and run the kit against them: `index-conformance --socket PATH --read-socket PATH` (the same path twice for a driver that serves both on one socket). A driver passes the suite or it is not a driver. The kit speaks the contract only, so the driver may be written in any language.
