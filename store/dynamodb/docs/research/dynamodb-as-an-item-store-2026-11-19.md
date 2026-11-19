# DynamoDB as an item store: what the contract needs, what DynamoDB gives, and the choices (2026-11-19)

*Research for the driver `store/dynamodb` (step 3b of [docs/plan.md](../../../../docs/plan.md)). Read on 2026-11-19 in the DynamoDB developer guide pages "Quotas in Amazon DynamoDB", "Using time to live (TTL)" and "Amazon DynamoDB Transactions: How it works". No AWS account was used and no call was made to AWS; the driver is tested against DynamoDB Local 3.3.2 (a container AWS publishes), which is not the service: where its behaviour matters the page says so. UNVERIFIED marks what no page I read this session says (the figures are from my knowledge of the documentation and should be checked before they are relied on).*

## Question

Can the item port ([contract](../../docs/item-port.md)) be served by DynamoDB: Get, Query by key prefix and by index entry, Transact with conditions, an expiry, and, if possible, the change log? What must the driver do that DynamoDB does not?

## Answer in short

Yes for `Get`, `Query` (both ways), `Transact` and the expiry, with one limit added (100 writes in a transaction, against the contract's 256). Not for the change log: DynamoDB offers no log the contract could use, so the driver declares it absent (plan R4). The mapping is in [layout.md](../layout.md). The choices that needed research follow.

## What DynamoDB gives

- **Conditions and all-or-none writes.** `TransactWriteItems` groups up to 100 write actions, on up to 100 distinct items, into one all-or-nothing operation of at most 4 MB; its actions are `Put`, `Update`, `Delete` and `ConditionCheck`, each with a condition expression; one item may be targeted once; a failed condition cancels the whole transaction with `TransactionCanceledException` and a reason for each action (`ConditionalCheckFailed`, `TransactionConflict`, ...). Isolation between a transaction and a single `GetItem` or write is serializable; between a transaction and a `Query` or `BatchGetItem` as a unit it is read-committed. ("Transactions: How it works".)
- **Conditional single writes.** `PutItem` and `DeleteItem` take a condition and fail with `ConditionalCheckFailedException`. The contract names "one put with one condition" (a counter that many callers change) and says the driver must not make it slower; a conditional `PutItem` is half the capacity of a transaction (each item in a transaction costs a prepare and a commit: "Capacity management for transactions").
- **Strongly consistent reads** (`ConsistentRead`) on `GetItem`, `BatchGetItem` and `Query` of the base table. The transactions page says that after a transaction completes an eventually consistent read "may still return the old state for a short period" and that a strongly consistent read guarantees the latest data. That is the contract's `linearizable`. That a global secondary index cannot serve a strongly consistent read is from my knowledge of the documentation (UNVERIFIED this session).
- **An expiry attribute.** Time to live: a Number attribute of Unix epoch seconds, enabled per table on one attribute name. DynamoDB deletes expired items "typically within a few days after their expiration", without write capacity; items with a TTL that is not a Number are ignored; an expired item pending deletion can still be read and updated (the page recommends a filter on reads and a condition on updates). The deletions appear in Streams as service deletes. ("Using time to live".)
- **Quotas.** Throughput per table on demand: 40,000 read and 40,000 write request units by default, adjustable; no practical limit on a table's size; 2,500 tables per account and Region by default. Item size 400 KB, partition key up to 2,048 bytes and sort key up to 1,024 bytes (UNVERIFIED this session; the quotas page I read does not list them).

## What the contract needs that DynamoDB does not give

| Need | Why DynamoDB does not give it | What the driver does |
| --- | --- | --- |
| **A prefix listing in key order**, with a page token | A `Query` needs the whole partition key; a range is only on the sort key | One partition key for every item, the item's key as the sort key: a listing is one `Query` with `begins_with`. Throughput per partition key is the cost (below) |
| **An index by (name, value) the writer computes**, with names the consumer chooses | A secondary index has fixed attribute names declared with the table, one key per item | An index entry is an item of its own, partition = name and value, sort = the item's key, written and deleted in the item's transaction |
| **A version the driver raises, a version condition, "absent" for an expired item** | No version attribute of its own; an expired item is still there | The driver reads, judges the conditions itself (so it can name the first that failed and treat an expired item as absent), and writes with a guard on the version it read |
| **Up to 256 operations in a `Transact`** | 100 writes in a transaction | Refuse more (`batch`); never split, because a split is not all or none |
| **The change log**: ordered by a sequence, with an epoch and a floor, a row written in the transaction of the change, expiry as a delete | see "The change log" | declared absent |

## Decisions

1. **Index entries are items, not global secondary indexes.** A GSI has fixed key attributes, so the consumer's index names cannot be GSIs without declaring each in the table (the contract says "the driver never computes an index" and does not know the names), and a GSI cannot be read strongly consistently, so every `linearizable` index query would have had to be refused. The price: each index entry is one more write in the transaction (and one more of the 100), one `BatchGetItem` per 100 results on an index query, and entries that outlive their item when the time to live frees them at different moments (they name nothing and are skipped).
2. **One partition key for every item.** Options: A, one partition key, the key as sort key (chosen: any prefix works, the simplest, no refused prefix); B, partition by the first path segment of the key (spreads kinds over partitions; a prefix with no `/` would be refused, which the contract does not allow); C, a hash shard in the partition key and a merge on read (spreads everything; a listing is a scatter-gather and the page token a vector). DynamoDB can split a hot item collection by sort-key ranges when the table has no local secondary index, but it does not promise to; the consumers in mind are within one partition's limits (the documentation states a per-partition throughput; I did not read it this session, UNVERIFIED). Revisit with a measured consumer.
3. **Read, judge, write with a guard, and read again on a lost race.** The alternative, a condition expression that is the caller's condition (`v = :expected`), cannot treat an expired item as absent and cannot name which condition failed; reading first costs one `BatchGetItem` and gives the driver the old index entries it must remove. The retry is bounded (eight) with a short growing wait.
4. **Entries are written in the transaction of the item** (atomic) rather than before and after it (cheaper per transaction, no 100-write ceiling on entries, but an entry removed after the commit can race a later re-add and needs a version stamp on each entry to stay correct). The atomic form is simple to reason about and passes the kit; the cost is the lower operation limit. If a consumer needs 256-operation batches with many entries, the non-atomic form with version-stamped entries is the road to take.
5. **TTL is the space, not the semantics.** The expiry is checked at read time, so the driver is correct whatever DynamoDB's schedule, and DynamoDB Local's handling of time to live (not measured here) does not matter to the kit.
6. **The change log is declared absent.** See the next section.
7. **Go** (coordinator's call R2 A), as for `key/awskms`: the first consumer, session, is a Go Lambda with no sidecars.

## The change log: why not Streams, and the road not taken

The contract's log ([item port](../../docs/item-port.md), "The change log"): rows with a sequence that grows, a row written in the transaction of the change, a newest-row marker so the head is never 0, an epoch that tells a log made again from the same numbers, a floor that trimming raises, a delete row when an expired item written with a log row is freed.

- **DynamoDB Streams** record the changes of a table, but the transactions page says that stream records of one transaction "might appear at different times and could be interleaved with records from other transactions" and that consumers "shouldn't assume transaction atomicity or ordering guarantees". A sequence that grows, and a head that a snapshot can name so that "a watch from it misses nothing the page shows", cannot be built on that. A time-to-live deletion is a service delete in the stream: it would be the "expiry is a delete" row, but only for the items a reader can still tell were written with a log row, and a stream is read by shard iterators, not by a sequence. The retention of a stream (24 hours, UNVERIFIED) bounds the floor but is not the driver's to set.
- **An in-table log** is possible: a counter item raised by every logged write, in the same transaction, with the row as an item that carries a time to live, and an epoch item made when the log is made. It gives the contract's semantics except that a time-to-live deletion writes no row. It also makes every logged write contend on one item (about 1,000 write units a second for one item, UNVERIFIED) and adds two writes to each logged transaction.
- **Decision:** declare the log absent (the brief and plan R4 allow it) and build the in-table log only for a consumer that needs `TailLog` on DynamoDB. Session does not (a session is read by key and by a declared index).

## Roads not taken

- **Partition by the first path segment** (option B above): refused for the prefix it would have to refuse.
- **A `Scan` for a listing:** reads the table, indexes included; no.
- **A global secondary index per declared index:** see decision 1.
- **Splitting a large `Transact`** into several transactions: not all or none.
- **A local DynamoDB emulator written in the test process** (as the fake of `key/awskms`): the DynamoDB API is large (expressions, transactions, pagination) and a wrong fake would prove nothing; the container AWS publishes is the better test. It is why the macOS CI jobs, which have no container runtime, skip the table tests and run only the rules and encoding tests.

## What is not measured

Latency of a transaction against a real table, the throughput one partition key gives, how long the real service takes to free an expired item, and the behaviour of DynamoDB Local with time to live. The first real use should measure them and amend this page.
