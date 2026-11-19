# store/memory

The memory driver of the [item port](../docs/item-port.md): the item contract over a map under a mutex. For tests, and for a consumer that needs a store without a backend. It keeps nothing across a restart. It passes the same [conformance suite](../conformance/docs/suite.md) as every other item driver.

| | |
| --- | --- |
| Library | crate `atoom-store-memory`: `MemoryEngine::new()`; `.router()` is the ConnectRPC handler (register it on a server of your own); `.serve(socket, shutdown)` serves it on a UNIX socket (mode 0600, this process's uid; `.serve_with(config, shutdown)` takes a `ServerConfig` with its own group, mode and callers); `.sweep()` frees expired items as a real engine's sweeper does (a tail runs it too); `.reset()` starts again with an empty engine and a log with a new epoch |
| Thin main | `store-memory --socket PATH`: serves on the socket (mode 0600, callers of this process's uid) until SIGINT or SIGTERM; `--version` |
| Guard flags | `--allow-uid UID` (repeatable; without it only this process's uid may call), `--allow-any-uid`, `--socket-gid GID`, `--socket-mode MODE`: the [socket guard](../../docs/socket-guard.md) |

```rust
let engine = atoom_store_memory::MemoryEngine::new();
tokio::spawn(engine.clone().serve(&socket, std::future::pending()));
let cases = atoom_store_conformance::item_suite(atoom_socket::dial(&socket)).await;
```

The engine keeps items in a `BTreeMap` under a `Mutex`, a change log in a `Vec`, and an epoch minted when the log is made. Its tests run the suite over a socket, run it twice to prove that the suite removes what it wrote, and prove that a log made again by `reset` has another epoch and refuses a saved position.

Dependencies: the port, [`atoom-store-conformance`](../conformance/README.md) (for the shared `rules`; the suite is a test), [`atoom-common`](../../common/README.md), [`atoom-socket`](../../socket/README.md), `tokio`, `clap`. Where the code came from is in [../PROVENANCE.md](../PROVENANCE.md).
