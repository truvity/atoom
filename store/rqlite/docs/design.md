# How the rqlite driver works

## Tables

One set of tables per namespace, `durable` or `temp` (the two ports of a consumer that keeps long-lived and short-lived items apart; they share one rqlite and never see each other):

| Table | Holds |
| --- | --- |
| `<ns>_items` | the items: `key` (text), `version`, `value` (blob), `expires_at` (milliseconds since the epoch, or null), `logged` (written with a log row) |
| `<ns>_index` | one row per index entry: `(name, value, key)` |
| `<ns>_log` | the change log: `seq` (autoincrement), `key`, `version`, `deleted`, `logged_at` (the driver's clock, used only to trim old rows) |
| `<ns>_meta` | `schema_version` and `log_epoch` |
| `<ns>_guard` | always empty: a failed condition inserts a null into it, which fails the transaction and tells which statement failed |

The schema is made idempotently (`ensure_schema`, which `serve` runs), so three nodes may run it at once. The version of the schema this build speaks (2) is checked at start; another version is refused with the instruction to recreate the data of a development or board installation. The log's epoch is drawn once, when the log is created, and the statements that take it are conditional, so a second run keeps the epoch it finds.

## Statements

Every statement is in [`sql.rs`](../src/sql.rs), one function per shape, so that what runs is readable in one place and testable without a database. The rules that hold for all of them:

- **No `ATTACH`, no trigger, no default that is not deterministic.** rqlite replicates the statement text, and a replica that evaluated `now` itself would disagree with the leader. Every timestamp is a parameter, and the consumer chooses it.
- **Bytes travel as hex text** (`unhex(?)`, `hex(x)`), keys as text. A blob comes back from rqlite's query API as base64 text that cannot be told from a string; hex can.
- **A key range is `key >= prefix AND key < successor`**, never `LIKE` with user bytes.
- **A conditional update is a conditional statement** that checks the version and the number of rows changed. Writes go through the Raft log.

## Read levels

`Get` and `Query` read at rqlite's `weak` level when the request does not ask, which is what a read always was, and at `linearizable` when `linearizable: true`, where the leader confirms its leadership with a quorum before it answers. The driver never answers one level for the other: if rqlite cannot answer at the asked level, the call is `unavailable`. (Measured on 2026-10-30 on a three-node cluster: a local read on a follower missed a write acknowledged just before it, every time, and caught up within about 50 ms; linearizable and default reads were never stale.)

## The sweeper

`sweeper::run` ticks every `--sweep-interval` (30 s by default). On the leader only, it frees expired items in batches of 500 (an item written with a log row leaves its delete row), then trims the change log to `--changelog-retention` (one hour by default, at most seven days; a longer retention is refused at start with a message that begins `--changelog-retention`). It only frees space: a read checks the expiry itself. An item with no expiry is never swept.

## A cluster

rqlite is its own service, one cluster of 1, 3 or 5 nodes (an even number adds a node and no fault tolerance); the driver is its client and runs beside it. The client API should listen on the loopback address only, with the driver as its only client, and Raft is the port a node opens to the other nodes (with mutual TLS in production; this driver does not manage that). The client speaks plain `http://` and basic authentication (`--basic-auth-file`); it has no TLS client yet.

## Errors

A failure of rqlite is `unavailable` with the reason `engine`; the reason is logged, never sent, so the refusal shows nothing of rqlite's text. A condition that fails is `aborted/version`. See [the item port](../../docs/item-port.md#refusals).

## Testing

`tests/driver.rs` starts one `rqlited` per test on free loopback ports and a temporary data directory, and covers: the suite on both namespaces and a second run that finds the tables empty, the namespaces not seeing each other, the sweeper and the trim, a log made again (another epoch; a saved position refused; the floor of the old log not kept), the read level that a query asks for (through a recording TCP proxy), a dead engine, `check`, `--version` and the basic authentication. `just conformance` runs the kit's binary against the thin main on both namespaces.
