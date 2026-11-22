# store/conformance

The conformance kit of the [item port](../docs/store-port.md): the cases every item driver must pass, run through the driver's ConnectRPC endpoint. A driver passes the suite or it is not a driver.

| | |
| --- | --- |
| Library | crate `atoom-store-conformance`: `item_suite(client)` runs the cases and returns their results (`item_suite_expecting(client, Claims)` also holds the driver to the durability it must declare), `report(&cases, out)` prints them and fails on a failed case and on none run; `durability_before_crash` and `durability_after_restart` are the two halves of a crash test that a driver which can restart its engine runs around the restart; `rules` holds what every Rust engine decides in the same way (keys, page sizes, timestamps, the checks of a `Transact`, the epoch of a log) |
| Binary | `store-conformance --socket PATH [--durability strict\|lossy\|none]` runs the suite against the driver that serves on the UNIX socket PATH, whatever its language; the exit status is 0 only when no case failed and at least one ran (a case the driver does not declare is skipped) |

```sh
cargo build -p atoom-store-memory -p atoom-store-conformance
target/debug/store-memory --socket /tmp/item.sock &
target/debug/store-conformance --socket /tmp/item.sock
```

`just conformance` (CI runs it on every architecture) builds the kit once and runs it against the thin main of every item driver in this repository: the memory driver, and the rqlite driver on both namespaces where an `rqlited` is available.

The suite writes under a prefix of its own, `conformance-<run>/`, and removes what it wrote at the end (the case `cleanup`), so it can run against a driver that serves a real namespace, and a second run finds the driver clean. The cases and what they fix beyond the contract's comments are in [docs/suite.md](docs/suite.md). Where the code came from is in [provenance](../../docs/provenance.md#store).

Dependencies: the port ([`atoom-store`](../README.md)), [`atoom-common`](../../common/README.md) for the refusal helpers, [`atoom-socket`](../../socket/README.md) for the client, and `clap`, `tokio`, `anyhow` and `futures`. An engine that shares `rules` depends on this crate; the suite itself needs no engine.
