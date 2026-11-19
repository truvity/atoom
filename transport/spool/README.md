# transport/spool

The spool driver of the [transport port](../docs/transport-port.md): the transport contract over a local queue of segment files that is fsync'd on every message and bounded in size. It passes the [conformance suite](../conformance/docs/suite.md).

| | |
| --- | --- |
| Library | crate `atoom-transport-spool`: `Spool::open(root, Config)` opens and recovers the directory; `service::Emit::new(spool, identify)` (or `Emit::fixed(spool, name)`) and `service::Consume(spool)`, each with `.router()`, are the two ConnectRPC handlers; `suite::run_temp()` is the backend suite (the directory checks, durable and ordered, acknowledgement and replay, the bound) that a host may run from a command of its own |
| Thin main | `transport-spool serve --socket-dir DIR --root DIR [--emitter NAME] [--emitter-name UID=NAME]... [--limit-bytes N] [--segment-bytes N]` (`DIR/emit.sock` and `DIR/receive.sock`, mode 0600, each admitting this process's uid, until SIGINT or SIGTERM); `transport-spool check --root DIR` (exit 3 when the directory cannot be served); `transport-spool version` (and `--version`) |
| Guard flags | `--allow-uid UID` (repeatable; without it only this process's uid may call), `--allow-any-uid`, `--socket-gid GID`, `--socket-mode MODE` (the group and the mode are for both sockets; `--allow-uid` is for the emit socket), and `--receive-allow-uid UID` for the callers of the receive socket (the callers of `--allow-uid` when it is not given): the [socket guard](../../docs/socket-guard.md) |
| Feature | `cli` (default): the flags and the sockets (`clap`, `atoom-socket`, `tracing-subscriber`). A consumer that mounts the routers in a host of its own turns the default features off |

How it works is in [docs/spool.md](docs/spool.md). Where the code came from is in [../PROVENANCE.md](../PROVENANCE.md).

**The seam: who the emitter is.** The library knows nothing of who calls it. `Emit` asks an `Identify` function (`Fn(&RequestContext) -> Option<String>`) for the name of the caller of each emit, and refuses the call (`permission_denied`/`caller`) when it answers `None`. The thin main gives every message the one `--emitter` name; with `--emitter-name UID=NAME` it names the caller from the peer credentials of the socket (the guard hands the peer to `Identify`), as `NAME@EMITTER`, and a caller it did not name as `uid<N>@EMITTER`. A host that tells its emitters apart in another way (sluis, until its hosts are deleted: as `<actor>@<boundary>`, from its registry) mounts `Emit::new(spool, identify).router()` behind its own guard, and `Consume(spool).router()` on a socket of its own that only the consumer may reach.

Dependencies: the port, [`atoom-common`](../../common/README.md) (the refusal helper), `buffa`, `aws-lc-rs` (the sum of a frame), `rustix`, `futures`, `tokio`, `tempfile` (the backend suite), and with the feature `cli` [`atoom-socket`](../../socket/README.md) and `clap`. [`atoom-transport-conformance`](../conformance/README.md) is a test dependency.
