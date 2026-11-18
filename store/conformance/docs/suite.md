# The item suite

The cases of `item_suite`, in the order they run. Each one speaks the contract and nothing else, over a client of the kit.

| Case | What it holds a driver to |
| --- | --- |
| `item_round_trip` | a put is read back with its bytes, version 1 and its index entries |
| `create_if_absent_twice` | a conditional create succeeds once; the second is `aborted/version` |
| `update_at_version_and_stale` | an update at the current version succeeds and raises the version; a stale one is refused |
| `delete_at_version` | a delete at a version, and the refusal of a stale one |
| `transact_all_or_none` | a failed condition applies nothing; the field path names the first that failed |
| `one_put_one_condition` | the counter shape: one put with one condition is one conditional update |
| `expiry` | an item past its expiry is absent to a read, a listing and a condition, and a put over it continues its version |
| `version_never_decreases` | a version never goes back while the engine still holds the item: not across an expiry and a create over it, however often (a delete, or a sweep that freed the row, starts the key again, and the suite cannot make an engine sweep) |
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
| `cleanup` | what the suite wrote is removed, so a driver that cannot delete does not pass |

## What the suite fixes beyond the proto comments

These are the choices the contract left open. Every engine makes them, and a consumer may rely on them. They are in the crate documentation of `atoom_store_conformance` and, as prose, in the [item port](../../docs/item-port.md).

- A key is printable ASCII without a space, at most 512 bytes.
- A condition expects `absent: true` or a version; `absent: false` and none are `invalid_argument/batch`. A failed condition is `aborted/version` with the field path `conditions[i]`.
- `Transact` applies conditions, then deletes, then puts. A key may be written or deleted once per request.
- `TailLog` from 0 starts after the newest row at the time of the call; a sequence beyond the newest or below the oldest kept is `sequence_expired`.
- A log has an epoch that a log made again does not keep.

## What the suite cannot test

It cannot reset a driver's log, so that a log made again has another epoch is the test of each driver (the memory driver and the rqlite driver each have one). It cannot make an engine unavailable, so the `unavailable/engine` refusal is a driver test too.

## Running it

Against a socket: `store-conformance --socket PATH`. From Rust: `atoom_store_conformance::item_suite(atoom_socket::dial(path)).await`, then `report`. A driver in Go can run the binary as a test step; the contract is the same in every language.
