# How the store port maps to DynamoDB

The driver ([README](../README.md)) keeps the items of the [store port](../../docs/store-port.md) in one DynamoDB table. This page says the table, how each call maps, what the driver does not offer, and the limits it adds to the contract. The research behind the choices is in [research/dynamodb-as-an-item-store-2026-11-19.md](research/dynamodb-as-an-item-store-2026-11-19.md).

## The table

One table, on-demand billing, no secondary index, two binary key attributes.

| Attribute | Type | Holds |
| --- | --- | --- |
| `pk` | B, hash key | an item: the byte `0x01`. An index entry: `0x02`, the index name, a zero byte, the index value |
| `sk` | B, range key | the item's key (for an entry: the key of the item it names) |
| `v` | N | the version (items only) |
| `val` | B | the bytes the consumer gave (items only) |
| `exp` | N | the expiry in milliseconds since the epoch, when the consumer set one |
| `ttl` | N | the expiry in whole seconds, rounded up, which DynamoDB's time to live reads; the entries of an item carry the item's |
| `ix` | L of M{n: S, v: B} | the item's declared index entries, so that a rewrite and a delete know which entries to remove |

`EnsureTable` makes this table and enables the time to live on `ttl`; `CheckTable` holds an existing table to the two keys. DynamoDB orders binary keys by unsigned byte, which is the order of the contract.

## The mapping

| Contract | DynamoDB | Notes |
| --- | --- | --- |
| `Get` | `GetItem`, `ConsistentRead` = `linearizable` | an item past its `exp` is `not_found` (`absent`) |
| `Query` by prefix | `Query` on `pk = 0x01` and `begins_with(sk, prefix)`, from the page token | reads one item more than the page to know whether there is a next page; skips what has expired; a page token is the last key of the page |
| `Query` by index | `Query` on the entry partition of (name, value) and `begins_with(sk, prefix)`, then `BatchGetItem` of the items they name | an entry whose item is gone, expired, or no longer declares it is skipped |
| `Transact` | `BatchGetItem` of every key it touches, the conditions judged, then `TransactWriteItems` (or one `PutItem` or `DeleteItem`) | see below |
| `TailLog` | none | `unimplemented`; see "The change log" |
| `Capabilities` | none | `store: dynamodb`, `tail_log: false` |

`Transact` in detail:

1. The request is checked as the other drivers check it (keys, conditions, keys written once, index names), plus this engine's limits.
2. Every key the request names is read with a strongly consistent `BatchGetItem`.
3. Each condition is judged on what was read, in order; the first that fails is `aborted` (`version`) with `conditions[i]`. An expired item is absent to a condition.
4. The writes are planned. A put writes version `old + 1` (1 when nothing is there; over an item past its expiry that nothing has freed, the version continues: it never goes back), with the guard "the item is still at the version that was read, or still absent". A delete of an item that is there removes it and its index entries, with the same guard. A condition on a key that no write touches is a `ConditionCheck` with the guard. The entries of a put that are new, or that change their time to live, are written; the entries of the item it replaces that the put no longer declares are deleted.
5. One write that is a put or a delete is one conditional call; more is one `TransactWriteItems`.
6. If a guard fails (another writer changed a key since step 2) or the transaction conflicts with another, nothing was written and the driver goes back to step 2, up to eight times with a short growing wait. A request that kept losing is `unavailable` (`engine`); one whose condition no longer holds on the new read is `aborted` (`version`). So of takers racing for one key with one condition, one wins each round and the others are refused on the state the winner left.

## Reads and linearizability

`Get` and `Query` pass `ConsistentRead` when the caller asks for a linearizable read, and a strongly consistent read sees every write acknowledged before it. A read that does not ask is eventually consistent, which the contract allows. Because index entries are items of the base table, **a query by index can be linearizable too**; a global secondary index could not, and the driver would have had to refuse the flag (`unavailable`) for every index query.

## The change log

The driver keeps none. `Capabilities` says `tail_log: false`, `TailLog` answers `unimplemented`, `QueryResponse.log_head` and `log_epoch` are 0, and a put or a delete with `log: true` is refused with `unimplemented` (reason `log`, field `puts[i].log` or `deletes[i].log`) and writes nothing: a driver that accepted the flag and wrote no row would lose the change without a word. The kit skips the cases that need the log and runs `no_log_is_refused` instead.

Why: the contract's log is ordered by a sequence, has an epoch, is trimmed by a floor, and writes the row in the transaction of the change; DynamoDB Streams has no global order (it is ordered per shard), keeps records for a bounded time, delivers the records of one transaction at different times, and records a time-to-live deletion as a service delete that a log would have to turn into a row. An in-table log (a counter item that every logged write raises, rows with a time to live) is possible and is the road to take if a consumer needs `TailLog` here; it would make every logged write contend on the counter. The road is taken when a consumer needs `TailLog` here ([research](research/dynamodb-as-an-item-store-2026-11-19.md)).

## Limits the driver adds to the contract

| Limit | Value | When it is exceeded |
| --- | --- | --- |
| Writes in one `Transact` | 100 | `invalid_argument` (`batch`), field `puts` (or `deletes`); nothing is written. The contract's 256 operations is a limit a driver may be stricter than: one DynamoDB transaction holds 100 writes, and each put, delete and index entry written or removed is one |
| Value of a put | 380 KiB | `invalid_argument` (`batch`), field `puts[i].value`; DynamoDB's item limit is 400 KB for the whole item |
| Index name, index value | 255 bytes, 1,024 bytes | `invalid_argument` (`index`); a name holds no zero byte. They are part of a partition key, which holds at most 2,048 bytes |
| Index entries of an item | the writes above | each one is a write of the transaction |

The conformance kit deletes what it wrote in chunks of 25 items for this reason.

## Partitions and throughput

Every item is in one partition key (`pk = 0x01`) with its key as the sort key, so that a listing by key prefix is one `Query`. DynamoDB can split a hot item collection by sort-key ranges when the table has no local secondary index; it does not promise to, and one partition key value has its own throughput limit that the documentation states per partition. The consumers in mind (sessions with random identifiers; a store of leases and counters) are within it; a consumer that writes thousands of items a second to one key range should say so (options in the research page: partition by the first path segment of the key, which refuses a prefix without one, or shard the partition and merge on read).

An index entry is in the partition of its (name, value): an index value that many items share is one partition key, the same remark.

## Time to live

The consumer sets every timestamp. `exp` is compared with the driver's clock at read time, so an item past its expiry is absent whether or not DynamoDB has freed it. `ttl` (seconds, rounded up, so never before the expiry) lets DynamoDB free the space, for the item and its entries, "typically within a few days". An entry may be freed before or after its item: an entry with no item names nothing and is skipped. A put over an item that is past its expiry and not yet freed continues its version; a key that DynamoDB has freed starts again at 1, as after a delete.

## Refusals

| Code | Reason | When |
| --- | --- | --- |
| as the store port | | the refusals of [the store port](../../docs/store-port.md) |
| `unimplemented` | `log` | a put or a delete asked for a log row |
| `invalid_argument` | `batch` | more writes, or a longer value, than this engine takes; a request DynamoDB refuses as malformed (the journal says which) |
| `unavailable` | `engine` | DynamoDB throttles, times out, is not reachable, or the driver may not use the table; a `Transact` that lost its race eight times |
