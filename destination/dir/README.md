# destination/dir

The dir driver of the [destination port](../docs/destination-port.md): the destination contract over a local directory that a forwarder (or, on a board, a file server) serves as static files. It receives public material only: what is written to it is served to anyone who asks. It passes the [conformance suite](../conformance/docs/suite.md).

| | |
| --- | --- |
| Library | crate `atoom-destination-dir`: `Directory::open(root, state)` opens the two directories; `Directory` is a `Destination` of the kit (serve it with `atoom_destination_conformance::rpc::Service(Arc::new(dir)).router()`); `Directory::sweep()` removes the temporary files a crash left; `suite::run_temp()` is the backend suite (the port's suite in process, plus modes, files the driver did not write, a document and a directory at one address, a write stopped before the rename) that a host may run from a command of its own |
| Thin main | `destination-dir serve --socket PATH --root DIR --state-dir DIR` (a socket of mode 0600 that admits this process's uid, until SIGINT or SIGTERM; removes the temporary files an earlier crash left); `destination-dir check --root DIR --state-dir DIR` (exit 3 when the directories cannot be served); `destination-dir version` (and `--version`) |
| Guard flags | `--allow-uid UID` (repeatable; without it only this process's uid may call), `--allow-any-uid`, `--socket-gid GID`, `--socket-mode MODE`: the [socket guard](../../docs/socket-guard.md) |
| Feature | `cli` (default): the flags and the socket (`clap`, `atoom-socket`, `tracing-subscriber`). A consumer that mounts the router in a host of its own turns the default features off |

How it works is in [docs/dir.md](docs/dir.md). Where the code came from is in [provenance](../../docs/provenance.md#destination).

A host with a guard of its own (peer credentials, an owner check, its own command line) does not use the thin main: it opens the `Directory`, registers the router on its own server, and may call `suite::run_temp()` from a `conformance` command of its own. That is the seam: the driver knows nothing of who calls it.

Dependencies: [`atoom-destination-conformance`](../conformance/README.md) (the `Destination` trait, the service and the suite; the driver is built on the kit's trait, as `store/rqlite` is on the kit's rules), `rustix`, `serde_json` (the records), `tokio`, `tempfile` (the backend suite), and with the feature `cli` [`atoom-socket`](../../socket/README.md) and `clap`.
