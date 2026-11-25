`atoom.index.v1alpha2`: an **index** makes the entries of append-only logs searchable. It is derived data: whatever it holds can be rebuilt from the logs. The index does not know what an entry means: which columns an entry has and which logs have an index are the host's, declared when it starts the driver ([record 0025](../../docs/decisions/0025-generic-blob-transport-and-index.md)). The audit model that first used this port lives in [truvity/audit](https://github.com/truvity/audit), not here.

The contract is [index.proto](../proto/atoom/index/v1alpha2/index.proto); this page is its rules in prose, and the conformance kit ([README](../conformance/README.md), [the suite](../conformance/docs/suite.md)) holds every driver to them.

Two services, which a host puts on two sockets, so that a driver serves the read side with no way to write:

| Service | Call | Who | What it does |
| --- | --- | --- | --- |
| `IndexService` | `GetCursor` | the indexer | where the indexer stands in one log |
| `IndexService` | `Apply` | the indexer | the rows of one batch of one log and, in the same step, the next cursor |
| `IndexReadService` | `Search` | the reader | rows of named partitions and tenants, newest first |

A **log** is named by a scope (the host's partition key, such as a project), a log kind and the log's id (`LogRef`); a **partition** of the index is a scope and a log kind; a **tenant** is the owner of a row. A **row** is one indexed entry: the **fixed columns** that any log has (tenant, the entry's hash, its sequence number, its time, its id) and the entry itself, once, as its bytes; and the **declared columns** of the host, `columns`, a map from a declared name to a value (`text` or `int`).

## The declaration

The host declares, when it starts the driver, the log kinds that have an index and its columns (`atoom.index.v1alpha2.Declaration`; its JSON form is the file of a thin main's `--declaration`):

```json
{
  "kinds": ["events", "certs"],
  "columns": [
    {"name": "action", "type": "COLUMN_TYPE_TEXT", "words": true, "filter": true, "required": true},
    {"name": "status", "type": "COLUMN_TYPE_INT", "filter": true},
    {"name": "note", "type": "COLUMN_TYPE_TEXT"}
  ]
}
```

- **Kinds:** from 1 to 32, each 1 to 128 bytes with no control character, none twice. A kind that is not declared has no index.
- **Columns:** at most 32; a name of `[a-z][a-z0-9_]{0,31}` that is not a fixed one (`scope`, `kind`, `log`, `tenant`, `hash`, `seq`, `time_ms`, `entry_id`, `entry`, `id`), none twice; a type, `COLUMN_TYPE_TEXT` (at most 1024 bytes, no NUL) or `COLUMN_TYPE_INT` (a signed 64-bit integer); `words`, reached by a word search (text only); `filter`, reached by an exact match; `required`, a row without it is refused. A column that is none of the three is stored and returned.
- **A declaration is part of the index's schema.** A driver keeps it with its data and refuses, at the start, data made under another: a new declaration is a rebuild from the logs, as a new tokenizer is.
- The checks are the same in both languages: `atoom_index::declaration::Schema` in Rust and `index.ParseDeclaration` / `index.CheckDeclaration` in Go, held to the same vectors (`index/testdata/declarations`).

## The contract

- **The cursor** of a log is the last checkpoint the indexer verified and indexed to: the tree size, the root hash (32 bytes) and a state that is opaque to the index. A log that was never indexed has size 0 and no root.
- **`Apply` moves the cursor forward only, and only from where the caller read it.** `expect_size` is the size of the cursor the caller read; the write is refused (`aborted`, reason `version`, field `expect_size`) when the cursor has moved, so that a second indexer during a deploy cannot move it back. The next cursor must be larger than `expect_size` and have a root of 32 bytes. The rows and the cursor are one transaction. A request with no cursor only adds rows.
- **A row is idempotent**: one whose tenant and hash (SHA-384 of the entry's bytes) are already in the log is a repeat, counted in `duplicates` and stored once.
- **A row is held to the declaration**: a column that is not declared, a value of another type, or a required column left out is `invalid_argument`, field `rows.columns`. A column that is not required may be left out, and a read returns the row without it.
- **A write to a kind that is not declared** is `permission_denied` / `rule`; a query that names one is `invalid_argument` (`partitions`).
- **A query names its partitions and its tenants.** Both are mandatory: they are the grant of the reader, a filter that no query can leave out. `*` as a tenant means every tenant and stands alone. A row of another partition or tenant is never returned.
- **A page** is newest first (`time_ms`, then the order of arrival), at most `page_size` rows (0 is 50 for the sqlite driver, at most 200), and the token of the next page is empty on the last page. The time range is `from_ms` at or after and `to_ms` before.
- **Exact matches** (`matches`) reach only the declared filter columns, with a value of the column's type (`matches.column`, `matches.value` otherwise); all of them must hold. `entry_id` is an exact filter of the fixed column.
- **A word search** (`q`) reaches only the declared word columns. Words are the alphanumeric runs of a value, folded for case and diacritics, with no stemming and no substring search; all the words of a query are required; a trailing `*` makes a prefix. A query with words where no column is a word column is `invalid_argument` (`q`).

## Capabilities

The port has no `Capabilities` call. Every driver serves both services in full; the two services let a host expose the read side alone. Open: a driver with a smaller feature set (no word search, no prefix match) would need to declare it, and whether that is a `Capabilities` call is decided with the first such driver.

## Refusals

Every refusal is a Connect error with the `Refusal` detail of [common](../../common/docs/refusal.md): a reason from a closed list and the path of the field at fault, never a value.

| Code | Reason | When |
| --- | --- | --- |
| `invalid_argument` | `argument` | a field that is not acceptable; the field path says which (`log`, `next.size`, `next.root`, `rows.tenant`, `rows.hash`, `rows.entry`, `rows.columns`, `partitions`, `tenants`, `matches.column`, `matches.value`, `q`, `page_token`, ...) |
| `aborted` | `version` | `Apply` at a cursor that is not where the caller read it (`expect_size`) |
| `permission_denied` | `rule` | `Apply` to a log kind that is not declared (`log.kind`) |
| `unavailable` | `engine` | the backend failed; the message is the reason only |

## Drivers

| Driver | Backend | Where it fits |
| --- | --- | --- |
| [index/sqlite](../sqlite/README.md) | one SQLite file with FTS5 | an index that fits on one disk |
| `index/rqlite` | rqlite, replicated SQLite | an index that several nodes serve (planned, step B15) |
| `index/postgres` | PostgreSQL, with row-level security per tenant | a Kubernetes installation (planned, step B15) |

## The call context

Every call carries the deadline and the correlation id of the request it is part of. The rules are on [the call context](../../docs/call-context.md). A Rust client: `IndexServiceClient` and `IndexReadServiceClient` over `atoom_socket::dial`; a Go client: `indexv1alpha2connect.NewIndexServiceClient(hc, url, call.ClientOptions()...)` (and `NewIndexReadServiceClient`).

## A driver of your own

Serve `IndexService` and `IndexReadService` (Go: implement the handlers of `indexv1alpha2connect`; Rust: implement the traits of the crate `atoom-index`), serve them on a socket or mount them in a process, and run the kit against them: `index-conformance --socket PATH --read-socket PATH` (the same path twice for a driver that serves both on one socket), with the driver started under the kit's declaration (`index-conformance --print-declaration`). A driver passes the suite or it is not a driver. The kit speaks the contract only, so the driver may be written in any language.
