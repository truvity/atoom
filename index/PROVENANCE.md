# Provenance

The code of the index libraries was copied as it is from [truvity/sluis](https://github.com/truvity/sluis) `main` at commit `df108e5470802659a055373dc20f254066eb3a1f` (the extraction plan, step 5, [docs/plan.md](../docs/plan.md)). The history stays in sluis.

| Here | From sluis | Changed |
| --- | --- | --- |
| `proto/atoom/index/v1alpha1/index.proto` | `proto/sluis/port/index/v2alpha1/index.proto` | package `sluis.port.index.v2alpha1` renamed to `atoom.index.v1alpha1` (plan, R1 A); messages, fields and numbers unchanged; the comments no longer point at sluis's decision records or socket names |
| `gen/atoom/index/v1alpha1/` | `gen/go/sluis/port/index/v2alpha1/` | regenerated from the file above |
| `conformance/src/` | the `suite.rs` of `drivers/indexer/sqlite` | **new, not merged**: that suite calls the index (`Index`) in a temporary directory and looks into the SQLite file, so it cannot judge another driver; it stays with its driver (below); the kit is a suite of the contract over a client, with the binary `index-conformance` |
| `sqlite/src/lib.rs`, `suite.rs` | `drivers/indexer/sqlite/src/{lib,suite}.rs` | the stubs come from atoom; the documentation no longer points at sluis's decision records; the suite is called the backend suite and keeps its cases; `Index::open` resolves the state directory (`canonicalize`, after the check that it is no symlink) before it gives SQLite the path, because SQLite with `NOFOLLOW` refuses a symlink in any component and a system directory can be one (macOS `/var`); the file itself is still opened with `NOFOLLOW` |
| `sqlite/src/service.rs` | `drivers/indexer/sqlite/src/service.rs` | the refusal helper comes from atoom; `Write::router()` and `Read::router()` are new |
| `sqlite/tests/port.rs` | `drivers/indexer/sqlite/tests/port.rs` | the tests serve through `atoom_socket`, run the conformance kit through the sockets; the test of the two sockets with a peer check each stays in sluis with the wrapper that adds the checks |
| `sqlite/src/main.rs` | `drivers/indexer/sqlite/src/main.rs` | the thin main: `serve` (the write socket and `--read-socket`, mode 0600) and `check` through `atoom_socket`, no registry, no peer check, no `--socket-gid`, no `conformance` and no `version` subcommand (`--version`); `check` exits 3 when the index cannot be served |

What stayed in sluis: the wrapper of the driver's `main` (the two sockets with peer credentials and the call graph, `--socket-gid`, the `conformance` and `version` subcommands, which call the `suite::run_temp` of the library), and the indexer and the reader, the actors that call the port.

Sluis requirement tags in the tests (`verifies: REQ-...`) and references to sluis's decision records and pages were dropped or replaced by pointers into this repository's pages.

Added in step G1 (2026-11-19, [docs/plan.md](../docs/plan.md), decision D11 A): the thin main `index-sqlite` carries the guard flags, `--read-allow-uid` for the callers of the read socket, and the `version` command. The guard comes from sluis's kits (`kits/rust/sluis-kit`, `kits/go/rpc`) with the names left out: it decides by uid and knows no registry. What stays in sluis: the registry names, the call graph and the `conformance` subcommand, until the host is deleted (step G4).
