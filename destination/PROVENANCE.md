# Provenance

The code of the destination libraries was copied as it is from [truvity/sluis](https://github.com/truvity/sluis) `main` at commit `df108e5470802659a055373dc20f254066eb3a1f` (the extraction plan, step 5, [docs/plan.md](../docs/plan.md)). The history stays in sluis.

| Here | From sluis | Changed |
| --- | --- | --- |
| `proto/atoom/destination/v1alpha1/destination.proto` | `proto/sluis/port/destination/v2alpha1/destination.proto` | package `sluis.port.destination.v2alpha1` renamed to `atoom.destination.v1alpha1` (plan, R1 A); messages, fields and numbers unchanged; the comments no longer name sluis's actor or decision record |
| `gen/atoom/destination/v1alpha1/` | `gen/go/sluis/port/destination/v2alpha1/` | regenerated from the file above |
| `src/port.rs` (the trait, the values and the address, prefix and document rules of `lib.rs`; the digest stays in the kit), `conformance/src/lib.rs`, `memory.rs`, `suite.rs` | `kits/rust/sluis-destination/src/{lib,memory,suite}.rs` | the crate `sluis-destination` is the kit of this port (trait, service, in-memory driver, suite), so it is `destination/conformance` here; the documentation no longer names an actor or a decision record; the reserved first address segment is `.atoom`, renamed from `.sluis` before v0.1.0 (owner decision D6 B, 2026-11-19: "Rename the reserved name segment `.sluis` to `.atoom` before v0.1.0."; no shim, since no release exists; sluis follows in its pin-bump PR and migrates its own data written under `.sluis`) |
| `conformance/src/rpc.rs` (`Service`), `src/client.rs` (`Client`, `error_of`) | `kits/rust/sluis-destination/src/rpc.rs` | the stubs and the refusal helper come from atoom; `Service::router()` is new; the client moved to the port crate in step D5 (below), where it is generic over the transport instead of holding `atoom-socket`'s |
| `conformance/src/main.rs`, `src/bin/memory.rs` | (none) | **new**: the binary `destination-conformance` (the suite through a socket, with the flags that say what the destination is) and `destination-memory` (the in-memory driver on a socket) |
| `dir/src/lib.rs`, `suite.rs` | `drivers/exporter/dir/src/{lib,suite}.rs` | the stubs come from atoom; the registry names of the driver and of its caller are gone; the suite is called the backend suite and keeps its cases |
| `dir/tests/port.rs` | `drivers/exporter/dir/tests/port.rs` | the tests serve through `atoom_socket`; the test of a caller that is not the exporter (a guard of sluis's host) stays in sluis |
| `dir/src/main.rs` | `drivers/exporter/dir/src/main.rs` | the thin main: `serve` and `check` through `atoom_socket`, no registry owner check, no `--socket-gid`, no `conformance` and no `version` subcommand (`--version`); `check` exits 3 when the directories cannot be served |

What stayed in sluis: the wrapper of the driver's `main` (the socket with peer credentials and the owner check, `--socket-gid`, the `conformance` and `version` subcommands, which call the `suite::run_temp` of the library), and exporter@, the actor that calls the port.

Sluis requirement tags in the tests (`verifies: REQ-...`) and references to sluis's decision records and pages were dropped or replaced by pointers into this repository's pages.

Added in step G1 (2026-11-19, [docs/plan.md](../docs/plan.md), decision D11 A): the thin mains `destination-dir` and `destination-memory` carry the guard flags (and `destination-dir` the `version` command). The owner check of sluis's host is `--allow-uid` of the owner's uid here. The guard comes from sluis's kits (`kits/rust/sluis-kit`, `kits/go/rpc`) with the names left out: it decides by uid and knows no registry. What stays in sluis: the registry names and the `conformance` subcommand, until the host is deleted (step G4).

Added in step D5 (2026-11-20, owner decision C2 A of 2026-11-20): `src/client.rs` is `Client`, `error_of` and the `Destination` impl that were in `conformance/src/rpc.rs`, moved to the port crate so that a consumer (exporter@) takes the client without the kit's `clap`, `aws-lc-rs` and `atoom-socket[cli]`. The code is the same; `Client` is generic over the transport of the generated client (`Client::new(transport, config)`) where it held `atoom_socket::Transport`, so the port crate takes no socket host. The kit re-exports it as `rpc::Client`. The port crate now requires `atoom-common` (for the reason of a refusal).
