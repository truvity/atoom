# store/rqlite

The rqlite driver of the [item port](../docs/item-port.md): the item contract over [rqlite](https://rqlite.io)'s HTTP API (replicated SQLite on Raft). For a board or a local installation, with one node or a cluster. It passes the [conformance suite](../conformance/docs/suite.md) on both namespaces, `durable` and `temp`, against a real rqlite.

| | |
| --- | --- |
| Library | crate `atoom-store-rqlite`: `Driver::new(Rq::new(url, auth)?, Ns::new("durable")?)`; `Driver::router()` is the ConnectRPC handler; `ensure_schema`, `ensure_schema_within`, `verify_schema`, `log_epoch`; `sweeper::run` frees expired items and trims the log, on the leader; `config::Settings` builds all of it from values or from flags |
| Thin main | `store-rqlite serve --socket PATH --rqlite-url URL --namespace durable\|temp [--sweep-interval 30s] [--changelog-retention 1h] [--basic-auth-file F]`; `store-rqlite check ...` (exit 3 when rqlite or the schema is not right); `store-rqlite --version` |
| Feature | `cli` (default): the flags (`clap`). A consumer that links the library and builds its [`Settings`](src/config.rs) itself turns the default features off |

`serve` waits up to a minute for rqlite, makes the schema if it is not there, serves the contract on the socket (mode 0600) and sweeps until SIGINT or SIGTERM.

A host with a guard of its own (peer credentials, an owner check, its own command line) does not use the thin main: it registers `Driver::router()` on its own server, runs `sweeper::run` beside it, and may add the flags to its own command line with `config::flags` and read them with `Settings::from_matches`. That is the seam: the driver knows nothing of who calls it.

The tests start a real `rqlited` (a pinned release that `just rqlite-fetch` puts in `.cache/rqlite`; no container). A test without the binary prints `SKIP` and returns, which is "not run", never a pass; `just test` sets `ATOOM_REQUIRE_RQLITED=1` on Linux, where a missing binary is a failure. rqlite publishes no macOS build, so on macOS these tests skip and the driver's unit tests run.

How it works (schema, statements, read levels, the sweeper) is in [docs/design.md](docs/design.md). Where the code came from is in [../PROVENANCE.md](../PROVENANCE.md).

Dependencies: the port, [`atoom-store-conformance`](../conformance/README.md) (the shared `rules`), [`atoom-common`](../../common/README.md), [`atoom-socket`](../../socket/README.md) (the thin main and the tests), and an HTTP client (`hyper`, `hyper-util`), `serde_json`, `tokio`, `clap` (feature `cli`).
