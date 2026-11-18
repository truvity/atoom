# How the sqlite driver works

What it does and does not do, for a consumer that links it.

- **One SQLite file** (`index.sqlite`) below the state directory holds the whole index of a boundary. The directory must be a real directory (not a symlink), owned by this process's user and closed to others (mode no wider than 0750). It is checked at every start.
- **Tables.** `cursor`: one row per log, the last verified checkpoint, moving forward only by a conditional write. `record`: one row per record with the fixed columns and the record once, unique by project, log kind, log, tenant and hash, so that indexing a record twice is a no-op. `record_fts`: the full-text table, external content over `record`, tokenizer `unicode61` with `remove_diacritics 2` and no stemming, holding only the searchable columns (the action, the status code and the product).
- **The index is derived data.** A file of another schema version or another tokenizer is refused at the start: the way is a rebuild from the log, not a migration.
- **The read side runs on connections opened read-only** (`query_only`), a small pool of them, so that no query can write whatever it holds; the write side has one connection under a mutex and runs each `Apply` as one immediate transaction.
- **A query is a keyset query**: newest first by time and then by arrival, the page token carrying the last row's time and id, so that pages do not shift while the index grows. The default page is 50 rows and no page is larger than 200.
- **Words become quoted phrases** of the FTS5 expression, so that no FTS5 operator, column filter or special character of the person's text means anything; a trailing `*` makes a prefix; at most 16 words of at most 64 bytes.
- **Bounds.** At most 1024 rows in an `Apply`, a record of at most 1 MiB, at most 100 tenants and 32 partitions in a query.
- **The blocking calls run on the blocking threads** of the runtime (`spawn_blocking`), so a slow disk does not stall the server.
- **No guard on the sockets** beyond their mode, in the thin main. A host that guards its sockets registers the routers on servers of its own.
