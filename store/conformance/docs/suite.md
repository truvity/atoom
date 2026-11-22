# The item suite

The cases of `item_suite`, in the order they run. Each one speaks the contract and nothing else, over a client of the kit.

| Case | What it holds a driver to |
| --- | --- |
| `capabilities` | the driver names its engine; one that does not serve the call is taken to offer every call |
| `durability` | the driver declares a durability of [record 0012](../../../docs/decisions/0012-store-durability.md), `strict` or `lossy`, and the one the run expects (`--durability`); a driver that declares none (a store for tests) is skipped unless the run expects none, and a value the record does not name fails |
| `durability_survives_restart` | always skipped here, with the reason: the kit cannot crash an engine behind a socket. A driver's own test runs `durability_before_crash`, crashes and restarts its engine, and runs `durability_after_restart`, which finds every acknowledged write at its version (the rqlite driver's test does) |
| `item_round_trip` | a put is read back with its bytes, version 1 and its index entries |
| `create_if_absent_twice` | a conditional create succeeds once; the second is `aborted/version` |
| `update_at_version_and_stale` | an update at the current version succeeds and raises the version; a stale one is refused |
| `delete_at_version` | a delete at a version, and the refusal of a stale one |
| `transact_all_or_none` | a failed condition applies nothing; the field path names the first that failed |
| `one_put_one_condition` | the counter shape: one put with one condition is one conditional update |
| `expiry` | an item past its expiry is absent to a read, a listing and a condition, and a put over it continues its version; on DynamoDB Local the case is retried once, see below |
| `version_never_decreases` | a version never goes back while the engine still holds the item: not across an expiry and a create over it, however often (a delete, or a sweep that freed the row, starts the key again, and the suite cannot make an engine sweep); on DynamoDB Local, which sweeps in the background, the case is retried once, see below |
| `prefix_query_order_and_paging` | key order, the default and maximum page size, the page token |
| `index_query` | a query by index entry, in key order, under the prefix |
| `log_order_and_from_sequence` | the change log in order; a `TailLog` from a sequence and from 0 |
| `sequence_expired` | a sequence the log does not hold is `failed_precondition/sequence_expired` |
| `query_log_head` | every query names a log head, never 0, and a tail from it misses nothing the page shows |
| `log_epoch` | every query names an epoch, never 0, the same one while the log lasts; a tail is good only under it |
| `linearizable_query` | after each of fifty acknowledged writes, a listing at `linearizable: true` holds all of them |
| `expired_delete_is_logged` | an item written with a log row is logged when it is removed, whatever the path, a delete of an expired item that is not swept yet included; one written without a log row leaves none |
| `transact_logs_atomically` | the row of a change is written in the transaction of the change: a refused transaction leaves no row, one that holds leaves one row for each put and delete with `log` and nothing else |
| `concurrent_takers_one_winner` | takers racing for one key with the same condition: one wins and the others are refused, at the creation and at each takeover, and the winner's version is the next one |
| `no_log_is_refused` | for a driver that declares no change log: `TailLog` is unimplemented, a put or delete with a log row is `unimplemented/log` and writes nothing |
| `cleanup` | what the suite wrote is removed, so a driver that cannot delete does not pass |

A case that needs the change log (`log_order_and_from_sequence`, `sequence_expired`, `query_log_head`, `log_epoch`, `expired_delete_is_logged`, `transact_logs_atomically`) is **skipped** when `Capabilities` says `tail_log: false`, and the report says `SKIP` with the reason: the kit does not fail a driver for a call it did not declare and does not pass it for the call either. A run in which every case was skipped fails.

## What the suite fixes beyond the proto comments

These are the choices the contract left open. Every engine makes them, and a consumer may rely on them. They are in the crate documentation of `atoom_store_conformance` and, as prose, in the [item port](../../docs/store-port.md).

- A key is printable ASCII without a space, at most 512 bytes.
- A condition expects `absent: true` or a version; `absent: false` and none are `invalid_argument/batch`. A failed condition is `aborted/version` with the field path `conditions[i]`.
- `Transact` applies conditions, then deletes, then puts. A key may be written or deleted once per request.
- `TailLog` from 0 starts after the newest row at the time of the call; a sequence beyond the newest or below the oldest kept is `sequence_expired`.
- A log has an epoch that a log made again does not keep.

## What the suite cannot test

It cannot reset a driver's log, so that a log made again has another epoch is the test of each driver (the memory driver and the rqlite driver each have one). It cannot make an engine unavailable, so the `unavailable/engine` refusal is a driver test too. It cannot crash and restart an engine, so a declared `strict` is tested by the driver around a crash, with the two functions of the kit named above (the rqlite driver kills `rqlited` with SIGKILL and starts it on the same data directory; DynamoDB cannot be crashed from a test, and its `strict` is the service's).

## Running it

Against a socket: `store-conformance --socket PATH [--durability strict|lossy|none]`. From Rust: `atoom_store_conformance::item_suite(atoom_socket::dial(path)).await` (or `item_suite_expecting(client, Claims { durability })`), then `report`. `tools/scripts/conformance-store.sh` passes `none` for the memory driver and `strict` for the DynamoDB and rqlite drivers. A driver in Go can run the binary as a test step; the contract is the same in every language.

## The one retry

Two cases, `version_never_decreases` and `expiry`, are retried once, and only when the driver under test runs on DynamoDB Local: `tools/scripts/conformance-store.sh` sets `ATOOM_CONFORMANCE_DYNAMODB_LOCAL=1` for that one run, and the kit reads it. The two names are a short allow-list in the kit (`RETRIED_ON_DYNAMODB_LOCAL`); no other case is on it. DynamoDB Local deletes expired items in a background sweep, and a sweep that frees the row between two steps of a case breaks it: the version starts again (the versions `[1, 2, 3, 1]` on the arm64 runner for `version_never_decreases`), or an item the case just wrote is not where it expects it (`expiry`), issue [#14](https://github.com/truvity/atoom/issues/14). On a failure the kit prints `RETRIED <case>: <first failure>` and runs the case again under keys of its own; the case passes only if the second run passes. Every other case and every other driver run once. The cause stays open in #14: the retry makes the run tell a real regression (it fails twice) from the race, and the `RETRIED` line keeps the race visible in the log.
