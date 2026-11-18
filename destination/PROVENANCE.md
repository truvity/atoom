# Provenance

The code of the destination libraries was copied as it is from [truvity/sluis](https://github.com/truvity/sluis) `main` at commit `df108e5470802659a055373dc20f254066eb3a1f` (the extraction plan, step 5, [docs/plan.md](../docs/plan.md)). The history stays in sluis.

| Here | From sluis | Changed |
| --- | --- | --- |
| `proto/atoom/destination/v1alpha1/destination.proto` | `proto/sluis/port/destination/v2alpha1/destination.proto` | package `sluis.port.destination.v2alpha1` renamed to `atoom.destination.v1alpha1` (plan, R1 A); messages, fields and numbers unchanged; the comments no longer name sluis's actor or decision record |
| `gen/atoom/destination/v1alpha1/` | `gen/go/sluis/port/destination/v2alpha1/` | regenerated from the file above |
| `conformance/src/lib.rs`, `memory.rs`, `suite.rs` | `kits/rust/sluis-destination/src/{lib,memory,suite}.rs` | the crate `sluis-destination` is the kit of this port (trait, service, in-memory driver, suite), so it is `destination/conformance` here; the documentation no longer names an actor or a decision record; the reserved address name `.sluis` is unchanged |
| `conformance/src/rpc.rs` | `kits/rust/sluis-destination/src/rpc.rs` | the stubs, the refusal helper and the client dial come from atoom (`atoom-socket`); `Service::router()` is new |
| `conformance/src/main.rs`, `src/bin/memory.rs` | (none) | **new**: the binary `destination-conformance` (the suite through a socket, with the flags that say what the destination is) and `destination-memory` (the in-memory driver on a socket) |
| `dir/src/lib.rs`, `suite.rs` | `drivers/exporter/dir/src/{lib,suite}.rs` | the stubs come from atoom; the registry names of the driver and of its caller are gone; the suite is called the backend suite and keeps its cases |
| `dir/tests/port.rs` | `drivers/exporter/dir/tests/port.rs` | the tests serve through `atoom_socket`; the test of a caller that is not the exporter (a guard of sluis's host) stays in sluis |
| `dir/src/main.rs` | `drivers/exporter/dir/src/main.rs` | the thin main: `serve` and `check` through `atoom_socket`, no registry owner check, no `--socket-gid`, no `conformance` and no `version` subcommand (`--version`); `check` exits 3 when the directories cannot be served |

What stayed in sluis: the wrapper of the driver's `main` (the socket with peer credentials and the owner check, `--socket-gid`, the `conformance` and `version` subcommands, which call the `suite::run_temp` of the library), and exporter@, the actor that calls the port.

Sluis requirement tags in the tests (`verifies: REQ-...`) and references to sluis's decision records and pages were dropped or replaced by pointers into this repository's pages.
