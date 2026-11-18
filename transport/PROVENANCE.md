# Provenance

The code of the transport libraries was copied as it is from [truvity/sluis](https://github.com/truvity/sluis) `main` at commit `df108e5470802659a055373dc20f254066eb3a1f` (the extraction plan, step 5, [docs/plan.md](../docs/plan.md)). The history stays in sluis.

| Here | From sluis | Changed |
| --- | --- | --- |
| `proto/atoom/transport/v1alpha1/transport.proto` | `proto/sluis/port/transport/v2alpha1/transport.proto` | package `sluis.port.transport.v2alpha1` renamed to `atoom.transport.v1alpha1` (plan, R1 A); messages, fields and numbers unchanged; the comments no longer point at sluis's decision records |
| `gen/atoom/transport/v1alpha1/` | `gen/go/sluis/port/transport/v2alpha1/` | regenerated from the file above |
| `conformance/src/` | the `suite.rs` of `drivers/writer/spool` | **new, not merged**: that suite calls the spool (`Spool`) in a temporary directory and cannot judge another driver, so it stays with its driver (below); the kit is a suite of the contract over a client, with the binary `transport-conformance` |
| `spool/src/lib.rs`, `suite.rs` | `drivers/writer/spool/src/{lib,suite}.rs` | the documentation no longer points at sluis's decision records; the suite is called the backend suite and keeps its cases |
| `spool/src/service.rs` | `drivers/writer/spool/src/service.rs` | the stubs and the refusal helper come from atoom; **`Emit` no longer reads the peer credentials**: it asks an `Identify` function for the emitter name (`Emit::new(spool, identify)`, or `Emit::fixed(spool, name)`), and `Emit` and `Consume` have `router()`; the boundary field is gone with it |
| `spool/tests/spool.rs` | `drivers/writer/spool/tests/spool.rs` | the tests serve through `atoom_socket`, name the emitter through `Emit::fixed`, run the conformance kit through the sockets, and test an `Identify` that cannot name the caller; the tests of the call graph, of the uid of the registry and of the emitters of the audit crate stay in sluis with the wrapper that adds them |
| `spool/tests/storage.rs` | `drivers/writer/spool/tests/storage.rs` | the names only |
| `spool/src/main.rs` | `drivers/writer/spool/src/main.rs` | the thin main: `serve` (two sockets, mode 0600, one `--emitter` name for every message) and `check` through `atoom_socket`, no registry, no call graph, no `--socket-gid`, no `conformance` and no `version` subcommand (`--version`); `check` exits 3 when the directory cannot be served |
| (none) | `drivers/writer/spool/tests/cli.rs` | stays in sluis: it tests `--socket-gid`, a flag of sluis's host |

What stayed in sluis: the wrapper of the driver's `main` (the two sockets with peer credentials and the call graph, `--socket-gid`, the `conformance` and `version` subcommands, which call the `suite::run_temp` of the library), the `Identify` that turns the peer into `<actor>@<boundary>`, and the audit actors and emitters that call the port.

Sluis requirement tags in the tests (`verifies: REQ-...`) and references to sluis's decision records and pages were dropped or replaced by pointers into this repository's pages.
