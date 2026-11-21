# The store port

`atoom.store.v1alpha1.ItemService`: a store of **items**. An item is a key, a version, bytes, an optional expiry and the index entries its writer computed. A driver sees items and nothing else; it never knows what the bytes mean. The contract is [item.proto](../proto/atoom/store/v1alpha1/item.proto); this page is its rules in prose, and the conformance kit ([README](../conformance/README.md), [the suite](../conformance/docs/suite.md)) holds every driver to them.

The port has five calls:

| Call | What it does |
| --- | --- |
| `Capabilities` | what the driver offers: the optional calls it declares |
| `Get` | an item by key; `not_found` (reason `absent`) if absent or expired |
| `Query` | items by key prefix, or by index entry, in key order, paged |
| `Transact` | conditions, puts and deletes, all or none |
| `TailLog` | the change log from a sequence, as a stream; optional (see Capabilities) |

## The contract

- **A key** is printable ASCII without a space, at most 512 bytes. Anything else is `invalid_argument` with the reason `address`.
- **A version** is set by the driver: 1 when an item is created, plus 1 at each change. An item that is removed and made again starts at 1 again; the driver keeps no counter for a key that is gone. A consumer that needs a version that only grows (a fencing token) keeps one item that it never deletes and gives it no expiry, and changes it by conditional update.
- **`Transact`** applies conditions, then deletes, then puts, and all or none. A condition expects `absent: true` or a version; a condition that expects neither is `invalid_argument` with the reason `batch`. A failed condition aborts the call with `aborted`, the reason `version` and the field path `conditions[i]` for the first that failed. A key may be written or deleted once per request. A put with no condition writes the next version.
- **One put with one condition is one conditional update**, not a transaction. A counter that many callers change depends on it, and a driver must not turn it into something slower.
- **An index** is a set of `(name, value)` entries that the writer computes and sends with the put. `Query` by `index` and `index_value` lists the items that have the entry, in key order, under the key prefix. The driver never computes an index.
- **`Query`** pages by key. A `page_size` of 0 is 100, above 500 is 500. The page token is the last key of the page, as bytes, and must be a key under the prefix; it is empty on the last page. `linearizable: true` asks for a read that sees every write acknowledged before it; a driver that cannot give one refuses the call (`unavailable`) and never answers a weaker read in its place. `Get` has the same flag.

## Capabilities

`Capabilities` is the first call a caller makes and the one the kit makes. It says:

- `store`: the name of the engine (`memory`, `rqlite`, `dynamodb`);
- `tail_log`: whether the driver keeps the change log;
- `durability` (planned, [record 0012](../../docs/decisions/0012-store-durability.md), step B3): `strict` (an acknowledged write survives a crash and a failover) or `lossy` (a failover can lose acknowledged writes). It is an additive field. A consumer refuses a binding that is weaker than its data needs. Until it is built, a consumer takes the durability from the driver's documentation: `store/dynamodb` and `store/rqlite` are strict, and `store/memory` is for tests.

A call the driver does not offer answers `unimplemented`, and the driver says so here. The kit skips the cases that need it and runs a case of its own that holds the driver to its word.

A driver with `tail_log: false`:

- answers `TailLog` with `unimplemented`;
- refuses a put or a delete that asks for a log row (`log: true`) with `unimplemented`, reason `log`, field `puts[i].log` or `deletes[i].log`, and writes nothing, so that no change is lost silently;
- names no log head and no epoch in `QueryResponse` (both 0).

A driver that does not serve `Capabilities` (it answers `unimplemented`) is taken to offer every call. Drivers may also be stricter than the contract in the size of a `Transact` (`store/dynamodb` takes 100 writes, not 256) and say so in their documentation.

## Time to live

- **An item past its `expires_at` is absent** to a read, a listing and a condition, whatever the engine still holds. A put over it, not yet freed by a sweep, continues from its version, so a version never goes back.
- **A sweeper frees the space** of expired items (rqlite's leader runs one; DynamoDB does it with its time to live). The sweep only frees space; correctness comes from the read-time check. An item with no expiry is never swept.
- **The consumer sets every timestamp**, never the engine. A driver's clock only compares with `expires_at` and picks what to sweep. This matters for a replicated engine: a replica that computed its own `now` would disagree with the others.

## The change log

- **A put or a delete with `log: true` writes a change-log row in the same transaction.** No change that asked for a row is missed, and none is invented. A row holds the key, the version after the change and whether it was a delete; it holds no value, so a watcher reads the value when the row is delivered. A delete's row holds the version the item had when it was removed, and the delete of an absent key logs nothing.
- **Expiry is a delete** to a watcher: the sweeper that frees an item written with a log row writes its delete row.
- **`QueryResponse.log_head`** is the newest sequence, read before the page, so a `TailLog` from it misses nothing the page shows. It is never 0: a log starts with a marker row at sequence 1.
- **`TailLog` from 0** starts after the newest row at the time of the call. A sequence beyond the newest, or below the oldest the log keeps, is `failed_precondition` with the reason `sequence_expired`: a watcher must not resume where a change could be lost.
- **The log has an epoch**, `QueryResponse.log_epoch`: a nonzero value the driver draws at random when it creates the log and keeps with the log. A log that is made again (a restore into a new database, a reset) begins its sequence again, so a saved sequence would name another position; the epoch tells them apart. A `TailLog` from a nonzero sequence is good only under the epoch that came with it: any other, an absent one included, is `sequence_expired`, and a stream ends with the same refusal if the log's epoch changes under it. A driver that cannot read an epoch refuses the call (`unavailable`) and never names a position without one.
- **The log is kept for a bounded window** (the driver sets it; the rqlite driver: one hour by default, at most seven days) and the sweeper trims it. A trim raises a floor, and a tail behind the floor is `sequence_expired`.

## Refusals

Every refusal is a Connect error with the `Refusal` detail of [common](../../common/docs/refusal.md): a reason from a closed list and the path of the field at fault, never a value.

| Code | Reason | When |
| --- | --- | --- |
| `not_found` | `absent` | `Get` of a key that is absent or expired |
| `invalid_argument` | `address` | a bad key, or a page token that is not a key under the prefix (`page_token`) |
| `invalid_argument` | `batch` | a bad `Transact` (a condition that expects nothing, a key written or deleted twice, more than 256 operations), a bad `page_size` |
| `invalid_argument` | `index` | an `index_value` with no `index`, or an index entry of a put with no name |
| `aborted` | `version` | a failed condition, with `conditions[i]` |
| `failed_precondition` | `sequence_expired` | a sequence or an epoch the log does not hold (`from_sequence`) |
| `unimplemented` | `log` | a put or a delete asks for a log row of a driver that keeps no change log (`puts[i].log`, `deletes[i].log`) |
| `unavailable` | `engine` | the engine is down or cannot answer at the asked level; the message is the reason only, never the engine's text |

## Drivers

| Driver | Engine | Durability | Where it fits |
| --- | --- | --- | --- |
| [store/rqlite](../rqlite/README.md) | rqlite (replicated SQLite on Raft) | strict | a board or a local installation, one node or a cluster |
| [store/memory](../memory/README.md) | a map under a mutex | none: for tests | tests, and a consumer that needs no backend |
| [store/dynamodb](../dynamodb/README.md) | DynamoDB | strict | AWS, serverless; a Go library that links into a Lambda function; no change log |
| `store/postgres` | PostgreSQL | strict | Kubernetes (planned, step B6) |
| `store/valkey` | Valkey | strict on one node with the append-only file synced on every write; lossy with asynchronous replication | session records and other temporary data (planned, step B10) |

## The call context

Every call carries the deadline and the correlation id of the request it is part of, `TailLog` included. The rules are on [the call context](../../docs/call-context.md). A Rust client: `ItemServiceClient::new(c.transport(), c.config())` over `atoom_socket::dial`; a Go client: `storev1alpha1connect.NewItemServiceClient(hc, url, call.ClientOptions()...)`.

## A driver of your own

Serve `ItemService` (Go: implement `storev1alpha1connect.ItemServiceHandler`; Rust: implement the `ItemService` trait of the crate `atoom-store`), serve it on a socket or mount it in a process, and run the kit against it: `store-conformance --socket PATH`. A driver passes the suite or it is not a driver. The kit speaks the contract only, so the driver may be written in any language.
