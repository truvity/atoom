# 0025. Generic blob, transport and index contracts: the host declares its pointers, its payload and its columns

Status: accepted, 2026-11-25 (owner decision on the pull request: D96 to D101, each choice A, and D112 A, which adds `blob/s3`; step B12 of the [plan](../plan.md); the owner's D89 A puts B12 before B6)

## Context

[0013](0013-what-moves-from-sluis.md) left audit's model in three ports of atoom: the blob, transport and index contracts still speak the vocabulary of the first log that used them. [0021](0021-drivers-know-no-deployment-shapes.md) says atoom knows no product. The plan's step B12 makes the three contracts generic; [0022](0022-the-blocks-order-after-0-7-0.md) found it is not small, and the owner's D89 A (2026-11-25) makes it the next step, before B6.

What names audit today, on master at 0.8.0:

| Port | In the contract | In the code |
| --- | --- | --- |
| blob | nothing in `blob.proto` but a comment; the rule that a pointer is a path whose last segment is `checkpoint` or `writer-state` | `blob/fsdir` (`POINTERS`), `blob/fs` and `blob/fsreadonly` (their mains and suites), the kit (its cases use the two names) |
| transport | `AuditMessage` with `uid`, `record` and `usage`; the comment `"<actor>@<boundary>"` on `Delivery.emitter`; the refusal fields `message.uid` and `message.record` | `transport/spool` (its frames hold an encoded `AuditMessage`), `transport/nats` and `transport/sqs` (they marshal and unmarshal `AuditMessage`), the kit (its redelivery case emits "usage records"), the Go and Rust stubs |
| index | `LogRef.project`, the log kinds `audit` and `issuance`, sixteen fixed columns of `Row` (boundary, action, class, activity, status, actor, trace, product, ...), the same as exact filters of `SearchRequest` | `index/sqlite` (`KINDS`, `SEARCHABLE`, the table `record` with those columns), its main and suite, the kit, the stubs |

Besides these, the `doc.go`, `lib.rs`, `Cargo.toml` descriptions and pages of the three ports call them "audit messages" or "audit records".

Three facts shape the design:

- **Nothing carries a usage part yet.** truvity/audit's emitters set `uid` and `record` only, and the writer that runs in sluis today refuses a message whose `usage` is not empty (the catalogue declares no meter). The `usage` field can go with no message in flight that uses it.
- **The transport's bytes are kept outside the process.** The spool writes the encoded message into its segment files; NATS JetStream and SQS hold it in their streams and queues. A message renamed with its field numbers kept is the same bytes, so what is in a spool or a queue at the upgrade reads the same before and after.
- **The index is derived data.** A file of another schema is refused and rebuilt from the log, never migrated; the sqlite driver already does so for its schema version and its tokenizer.

## Decision

### Blob: the host declares its pointers

- **The contract names no pointer.** `blob.proto` does not change on the wire: `Put` overwrites a pointer, and which paths are pointers is the host's declaration to the driver it starts.
- **A pointer is declared by its last segment**, as today, and a name follows the rules of a segment. The host gives the names: `Options.pointers` in `blob/fsdir` (the library under both `fs` drivers), `--pointer NAME` (repeatable) on `blob/fs`'s thin main. A driver started with no pointer keeps every object write-once and refuses every `Put` (`permission_denied` / `rule`), as it refuses a `Put` on any other path today.
- **`blob/fsreadonly`** refuses every write as before; it loses its mention of the two names and needs no declaration.
- **The kit declares its own pointer names** and runs the driver with them, as the key kit declares its own purposes (step B13): `blob-conformance --print-pointers` gives them to the harness that starts the driver. No case uses `checkpoint` or `writer-state`.
- **`blob/s3`, a Go driver of the blob port, joins B12** (the owner's D112 A, for audit's writer): a Go module a host mounts in process, as `transport/nats` and `transport/sqs` are, and a thin main. It is built on this contract, right after the blob pull request and before 0.9.0:
  - **the bucket and a key prefix are the host's options** (and its AWS configuration and credentials); the driver knows no region, account or installation ([0021](0021-drivers-know-no-deployment-shapes.md));
  - **`Create` is `PutObject` with `If-None-Match: *`**: S3's `412 Precondition Failed` is the port's `already_exists` / `taken`, so of any number of racing creators exactly one wins;
  - **`Put` on a declared pointer is a compare-and-swap.** The port's entity tag is the SHA-384 of the bytes, in hex, as for the `fs` drivers, computed by the driver from the bytes it writes or reads (S3's own ETag is not a function of the bytes alone under every kind of encryption; as built, nothing is kept in the object's metadata). An empty `if_match` writes with `If-None-Match: *`; any other value reads the object, compares its tag with `if_match`, and writes with `If-Match` on the S3 ETag it read, so that a write between the read and the replace fails as `412`, the port's `aborted` / `version`. A `Put` on another path is `permission_denied` / `rule` before any call to S3;
  - **`Get` and `List`** are `GetObject` and `ListObjectsV2` under the prefix, in byte order, with the port's page sizes; the page token is S3's continuation token;
  - **it passes the blob kit** against a local S3 double that implements both conditional writes (a pinned S3-compatible server in a container where it does, or an in-process fake through the real Go SDK client where it does not; which one is settled in its pull request after checking the double's support for `If-None-Match` and `If-Match`).

### Transport: a message is an id and an opaque payload

- **`AuditMessage` becomes `Message`, with `id` (field 1) and `payload` (field 2), and `usage` (field 3) is reserved with its name.** The field numbers are kept, so the encoded bytes of a message without usage, which is every message so far, are the same bytes: a spool's segments, a JetStream stream and an SQS queue need no drain at the upgrade, and an emitter and a writer of different releases understand each other while neither uses usage.
- **The rules stay, with the new names.** `id` is the message's unique id, the key of the at-least-once rule and of the brokers' deduplication (`Nats-Msg-Id`, the SQS deduplication id is the SHA-256 of `id`); `payload` is opaque bytes, at least one. The refusals name the fields `message.id` and `message.payload`.
- **What a payload holds is the consumer's.** audit packs its record and the usage part of the same event into it, in a form of its own (below, migration). The transport's rule "every delivery of an id carries the same bytes" covers the whole payload, so a consumer that keeps one entry per id counts a usage part once, as D50 A asks.
- **`Delivery.emitter` keeps its name and loses its example.** It is who the driver took the caller to be, from the socket's peer or the host's naming; `<actor>@<boundary>` is sluis's naming and moves to sluis's documentation.
- **The kit's redelivery case** becomes `a_redelivered_or_repeated_message_is_one_message`: a message emitted twice under one id and taken again after its consumer went away comes at least twice, always with the payload emitted. It no longer speaks of usage records.

### Index: fixed columns that any log has, and columns the host declares

The new contract, in short (field numbers are settled in the pull request):

```proto
// A log of the index: a scope (a host's partition key, such as a project), a declared kind, and the log's id.
message LogRef { string scope = 1; string kind = 2; string log = 3; }
message Partition { string scope = 1; string kind = 2; }

// One value of a declared column: text or a signed integer.
message Value { oneof v { string text = 1; int64 int = 2; } }

// One indexed entry: the fixed columns, the declared ones, and the entry's bytes once.
message Row {
  string tenant = 1;              // the owner of the row; the grant filters on it
  bytes hash = 2;                 // SHA-384 of the entry's bytes: the key of idempotent indexing
  int64 seq = 3;                  // the entry's position in the log
  int64 time_ms = 4;              // the entry's time; the order of a page
  string entry_id = 5;            // the entry's own id
  bytes entry = 6;                // the entry's bytes, opaque, returned as stored
  map<string, Value> columns = 7; // the declared columns
  LogRef log = 8;                 // set by the index on a read, ignored on a write
}

message Match { string column = 1; Value value = 2; }

message SearchRequest {
  repeated Partition partitions = 1; // mandatory, the grant
  repeated string tenants = 2;       // mandatory, the grant; "*" alone for every tenant
  string q = 3;                      // words over the declared word columns
  string entry_id = 4;               // only this entry, when set
  repeated Match matches = 5;        // exact filters over declared filter columns, all must hold
  int64 from_ms = 6; int64 to_ms = 7;
  int32 page_size = 8; bytes page_token = 9;
}
```

- **The fixed columns** are what any indexed log has: tenant, hash, sequence number, time, entry id, and the entry itself. The cursor (`size`, the 32-byte root, the opaque `state`) and `Apply`'s guard (`expect_size`) do not change: a verified position in an append-only log names no product. `recorded_ms` is no longer fixed: audit declares it as an integer column.
- **The host declares the rest when it starts the driver** (Q2): the log kinds that have an index, and each column with its name, its type (`text` or `int`), and whether it is a word column (reached by `q`), a filter column (reached by `matches`, with a database index), and required (a row without it is refused). A name is `[a-z][a-z0-9_]{0,31}`, is not a fixed column's, and a declaration holds at most 32 columns; a text value is at most 1024 bytes as today.
- **The declaration is a small document** (YAML or JSON) with one parser and one validator per language in the port library (`atoom-index` in Rust, the `index` module in Go), as key purposes are (`purpose.Declaration`), so that `index/rqlite` and `index/postgres` (B15) read the same document. The sqlite thin main takes `--declaration FILE`.
- **A declaration is part of the index's schema.** The sqlite driver makes the table `record` with the fixed columns and one column per declared column, the full-text table over the word columns, and an index per filter column, and keeps the declaration's digest in `meta`. A file made under another declaration is refused at the start, with the same message as another schema version: rebuild it.
- **The refusals keep their codes and reasons.** A write to a kind that is not declared is `permission_denied` / `rule` (`log.kind`); a query that names one is `invalid_argument`; a value of the wrong type, an undeclared column, a missing required column, or a match on a column that is not a filter column is `invalid_argument` with the field path (`rows.columns.<name>`, `matches.column`).
- **The kit declares its own kinds and columns** (`index-conformance --print-declaration`) and holds every driver to them: word search only over word columns, a match only over filter columns, the grant on every query, idempotent rows, the cursor guard.

### Versioning (Q1)

- **A clean break in 0.9.0**, by [0015](0015-versioning-and-consumption.md): `v1alpha1` may break, a breaking change raises the minor version, and every consumer pins an exact release. No release serves two versions of a port.
- **blob and transport change in place in `v1alpha1`**: the blob wire does not change; the transport's renamed message has the same bytes. A mixed installation keeps working where the bytes are the same, and fails loudly where they are not (a `Put` on a pointer the driver was not told of).
- **index moves to a new package, `atoom.index.v1alpha2`, and `v1alpha1` is removed in the same release.** Its messages change shape, and an old indexer or reader that talks to a new driver must fail at the route (`unimplemented`), not have its fields read as other fields or its filters ignored. There is no window in which a driver serves both.
- Each pull request carries the `breaking:<library>` label, and the changelog of 0.9.0 names each library and what its host now passes.

### What each driver and kit changes

| Library | Change |
| --- | --- |
| `blob` (proto, Go and Rust stubs, page) | comments; the page loses its "planned" note and documents the declaration |
| `blob/fsdir` | `POINTERS` gives way to `Options.pointers`; validation of the names |
| `blob/fs` | `--pointer NAME`; its suite runs with the kit's names |
| `blob/fsreadonly` | its mention of the names goes; behaviour unchanged |
| `blob/conformance` | its own pointer names, `--print-pointers` |
| `blob/s3` (new, Go) | the driver above, its thin main, its page, the kit in `just conformance` |
| `transport` (proto, stubs, page) | `Message`, `id`, `payload`, field 3 reserved; the refusal fields |
| `transport/spool` | the type and its comments; the frames' bytes unchanged; the limit is on the encoded `Message` |
| `transport/nats`, `transport/sqs` | the type, the deduplication from `id`, the comments and pages that say "audit records" |
| `transport/conformance` | the redelivery case renamed and without usage |
| `index` (proto `v1alpha2`, stubs, page) | the contract above; the declaration type and its parser in Go and Rust |
| `index/sqlite` | the table from the declaration, the digest in `meta`, `--declaration FILE`, `KINDS` and `SEARCHABLE` gone |
| `index/conformance` | its own declaration, `--print-declaration`, the cases over declared columns |
| docs, `doc.go`, `lib.rs`, `Cargo.toml` of the three ports, [architecture](../architecture.md), [packages](../packages.md) | no "audit records" or "audit messages"; audit is named only as a consumer, with a link |

`just check` gains a rule that the proto files and generated names of the blob, transport and index ports contain no `audit`, `usage`, `project` or the old column names, so that the principle of 0021 stays tested.

### The order of the pull requests (Q6)

1. **This record**, docs only, with the plan's order.
2. **transport**: the proto, the stubs, `spool`, `nats`, `sqs`, the kit and the page. First, because it is the smallest, it has the most drivers, and audit's emitters use only this port.
3. **blob**: `fsdir`, `fs`, `fsreadonly`, the kit and the page.
4. **`blob/s3`** (D112 A): the Go driver on the generic blob contract, with the kit against a local S3 double.
5. **index**: `v1alpha2`, the declaration in Go and Rust, `sqlite`, the kit and the page. The largest.
6. **The sweep and the check**: the remaining texts, the `just check` rule, the plan (B12 built), then **the release 0.9.0** with all three.

### The migration for audit and sluis

atoom is released first, and each consumer moves at its next pin, never the reverse ([0015](0015-versioning-and-consumption.md)).

- **truvity/audit** (0.20.0 on atoom 0.7.0): its emitters, Go and Rust, build `Message { id: uid, payload }` in place of `AuditMessage { uid, record }`. The record does not change. What the payload holds is audit's to decide in its own record: the record's bytes alone while there is no usage part, and, from its usage step, a form that carries the record and its usage part. Recommended to audit: a form that a writer tells apart from a bare record without guessing (a record is RFC 8785 JSON and begins with `{`; a versioned envelope that begins with another byte, or an envelope from the adoption on), so that messages emitted before and after the change are read the same. audit's log layout already names its pointers (`checkpoint`, `state/writer-state`); when the writer moves into audit (its step 2), audit's documentation gives the `--pointer` names, and when the indexer and reader move (its step 3), audit ships its index declaration (its kinds `audit` and `issuance`, and the columns that are fixed today) as a file of its release.
- **sluis** (`main`, on atoom 0.5.0): its move to 0.9.0 also carries 0.6.0 to 0.8.0, among them the PIN release domains of 0021 that it must pass. For B12: the writer reads `GetId` and `GetPayload` and reads the usage part out of the payload when there is one; `drivers/writer/fs` starts `blob/fs` with `--pointer checkpoint --pointer writer-state`; `drivers/indexer/sqlite` starts the index with the declaration of audit's columns; the indexer fills `Row.columns` from the record (where it fills the fixed fields today); the reader builds `matches` (where it sets the exact filters today); the vendored protos in `third_party/atoom` move with `just atoom-sync`. The index file is refused at the first start and is rebuilt from the log. The spool's segments are read as before: no drain.
- **session** takes the generic message through audit's emitter when it moves to the audit release that adopts 0.9.0; if it builds a transport message itself, it renames it the same way.

## Consequences

- One breaking release, 0.9.0, for three ports and thirteen libraries. Consumers that stay on their pin are not affected.
- The B12 notes of the plan and the three pages become the description of what is built, and "done when" holds: no field, message or constant of these ports names audit.
- `index/rqlite` and `index/postgres` (B15) are built on the generic contracts and read the same declarations; `blob/s3` moves from B9 into B12 (D112 A).
- A change of an index declaration is a rebuild of the index, as a change of the tokenizer is.
- The order of [0023](0023-session-to-done-first.md) changes only in that B12 comes before B6 (D89 A): B12, B6, B7, then B10, B11 and B15, with the rest of B9 (`source/ssm`, `blob/s3`, the extension zips) where 0023 puts it.

## The owner's choices

The owner took the pick of each choice on the pull request (2026-11-25): Q1 A (D96), Q2 A (D97), Q3 A (D98), Q4 A (D99), Q5 A (D100), Q6 A (D101). The options are kept as they were put.

**Q1. Versioning.**

- **A. A clean break in 0.9.0: blob and transport in place in `v1alpha1` (same wire bytes), index in a new package `v1alpha2` with `v1alpha1` removed in the same release.** Pros: one contract per port in every release; the transport's in-flight bytes need no drain; an old index client fails at the route instead of being misread; follows 0015, which rejected compatibility windows for alpha contracts. Cons: every consumer moves at once for the index; two package versions in the repository's names (index at `v1alpha2`, the others at `v1alpha1`).
- **B. `v1alpha2` beside `v1alpha1` for all three, both served through 0.9.x, `v1alpha1` removed in 0.10.0.** Pros: a rolling upgrade of mixed installations. Cons: six drivers and three kits serve and test two contracts for a release; the spool and the brokers hold bytes of one or the other; 0015 rejected the cost of such a window; the consumers pin exact versions and do not need one.
- **C. A clean break in place in `v1alpha1` for all three, with the retired index field numbers reserved.** Pros: the fewest names. Cons: an old reader's filters (action, actor, ...) become unknown fields and are ignored, so it gets more rows than it asked for (still within its grant), silently.
- **Pick: A. Taken.**

**Q2. When the host declares the index's kinds and columns.**

- **A. At the start of the driver** (`--declaration FILE`, an option in the library), kept with the index; another declaration is a rebuild. Pros: as the key port's purposes (B13); one schema per file, known before the first write; a reader cannot change it. Cons: the declaration lives in the installation's configuration next to the driver, so audit must ship it as a file and the installation must pass it.
- **B. With each write** (every `Apply` carries the columns it uses; the driver adds what it has not seen). Pros: no file to pass. Cons: the schema grows from whatever an indexer sends; a full-text table and database indexes rebuilt during writes; the reader does not know which columns are searchable until rows exist.
- **C. A `Declare` call on `IndexService`**, made by the indexer at its start, kept by the driver; another declaration is refused until a rebuild. Pros: the product that owns the columns declares them, with no file in the installation. Cons: a new call on the contract; the write side can reshape what the read side serves; two indexers during a deploy race to declare.
- **Pick: A. Taken.**

**Q3. The types of a declared column.**

- **A. `text` and `int` only.** Pros: every column of today is one of the two; every backend has both; small kit. Cons: a time, a boolean or bytes column is an `int` or `text` by convention.
- **B. Also `bool`, `bytes` and a time.** Pros: more exact types. Cons: more cases in every driver and in the kit, for no consumer yet.
- **Pick: A. Taken.** A new type is additive later.

**Q4. How a host declares its blob pointers.**

- **A. By the last segment of the path**, a list of names. Pros: the rule of today, with the names moved to the host; one string compare. Cons: a name is a pointer under every prefix.
- **B. By path patterns** (a prefix and a name, or a glob). Pros: a pointer only where the host means it. Cons: a pattern language in every driver and in the kit; no consumer needs it.
- **C. No declaration: an object is a pointer when its first write is a `Put`.** Pros: nothing to declare. Cons: whoever can `Put` can claim a path before its write-once `Create` and later overwrite it, which breaks the fence the port exists for.
- **Pick: A. Taken.**

**Q5. The names on the transport.**

- **A. `Message`, `id`, `payload`; `Delivery.emitter` stays.** Pros: neutral words; the field numbers are kept, so the bytes are the same. Cons: every consumer's code renames `uid` and `record`.
- **B. Keep `uid` and rename only `record` to `payload`.** Pros: less churn; `uid` is not audit's word. Cons: `uid` reads as audit's `metadata.uid`; two half-renamed names.
- **Pick: A. Taken.** (`emitter` names a role, not a product, and every driver and the kit already use it.)

**Q6. The releases.**

- **A. One release, 0.9.0, after the three pull requests.** Pros: one move for each consumer; the sweep and its check land with them. Cons: audit's emitters wait for the index pull request before they can adopt the transport rename.
- **B. A release per port** (0.9.0 transport, 0.10.0 blob, 0.11.0 index). Pros: audit's emitters move sooner. Cons: three breaking moves for sluis, which takes all three ports; three changelogs to read.
- **Pick: A. Taken.** The emitters gain nothing from the rename alone until the usage step, which needs the writer, which needs blob and index too.

## Roads not taken

- **Moving the three ports into truvity/audit.** Already rejected by 0013: audit would become a port owner and atoom would lose ports that other logs can use.
- **A transport message with headers or a content type.** Not taken: no consumer needs one; the payload's form is the consumer's, and a field is additive later.
- **Generic index rows as one JSON document with paths declared for search.** Not taken: every backend would parse JSON on every query, and filters over JSON paths are slower and differ between SQLite and PostgreSQL; declared columns map to real columns.
- **Keeping `recorded_ms` fixed.** Not taken: it is the writer's stamp, which another log may not have; audit declares it.
