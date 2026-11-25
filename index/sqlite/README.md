# index/sqlite

The sqlite driver of the [index port](../docs/index-port.md): the write side and the read side over one SQLite file with FTS5. It passes the [conformance suite](../conformance/docs/suite.md).

| | |
| --- | --- |
| Library | crate `atoom-index-sqlite`: `Index::open(dir, &schema)` opens (and makes) the index file below a state directory, under the host's declaration (`Schema`, re-exported from the port); `service::Write(index).router()` and `service::Read(index).router()` are the two ConnectRPC handlers; `suite::run_temp()` is the backend suite (the file, its mode, its tokenizer and its declaration, a read side that cannot write, the rules of the port, over a declaration of its own, `suite::DECLARATION`) that a host may run from a command of its own |
| Thin main | `index-sqlite serve --socket PATH --read-socket PATH --state-dir DIR --declaration FILE` (the declaration is the JSON form of `atoom.index.v1alpha2.Declaration`; the write side and the read side, each on a socket of mode 0600 that admits this process's uid, until SIGINT or SIGTERM); `index-sqlite check --state-dir DIR --declaration FILE` (exit 3 when the index cannot be served); `index-sqlite version` (and `--version`) |
| Guard flags | `--allow-uid UID` (repeatable; without it only this process's uid may call), `--allow-any-uid`, `--socket-gid GID`, `--socket-mode MODE` (the group and the mode are for both sockets; `--allow-uid` is for the write socket), and `--read-allow-uid UID` for the callers of the read socket (the callers of `--allow-uid` when it is not given): the [socket guard](../../docs/socket-guard.md) |
| Feature | `cli` (default): the flags and the sockets (`clap`, `atoom-socket`, `tracing-subscriber`). A consumer that mounts the routers in a host of its own turns the default features off |

How it works is in [docs/sqlite.md](docs/sqlite.md). Where the code came from is in [provenance](../../docs/provenance.md#index).

A host with a guard of its own (peer credentials, one socket for each caller) does not use the thin main: it opens the `Index`, registers `Write(index.clone()).router()` on the socket of the indexer and `Read(index).router()` on the socket of the reader, and may call `suite::run_temp()` from a `conformance` command of its own. That is the seam: the driver knows nothing of who calls it.

Dependencies: the port, [`atoom-common`](../../common/README.md) (the refusal helper), `rusqlite` with the bundled SQLite and FTS5 (the database engine is built in), `rustix`, `tokio`, `tempfile` (the backend suite), and with the feature `cli` [`atoom-socket`](../../socket/README.md) and `clap`. [`atoom-index-conformance`](../conformance/README.md) is a test dependency.
