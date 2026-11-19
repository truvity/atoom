# Provenance

The code of the blob libraries was copied as it is from [truvity/sluis](https://github.com/truvity/sluis) `main` at commit `df108e5470802659a055373dc20f254066eb3a1f` (the extraction plan, step 5, [docs/plan.md](../docs/plan.md)). The history stays in sluis.

| Here | From sluis | Changed |
| --- | --- | --- |
| `proto/atoom/blob/v1alpha1/blob.proto` | `proto/sluis/port/blob/v2alpha1/blob.proto` | package `sluis.port.blob.v2alpha1` renamed to `atoom.blob.v1alpha1` (plan, R1 A); messages, fields and numbers unchanged; the comments no longer point at sluis's decision records |
| `gen/atoom/blob/v1alpha1/` | `gen/go/sluis/port/blob/v2alpha1/` | regenerated from the file above |
| `conformance/src/` | the `suite.rs` of `drivers/writer/fs` and of `drivers/indexer/fs` | **new, not merged**: those two suites call the directory (`Fs`) in a temporary directory and cannot judge another driver, so they stay with their drivers (below); the kit is a suite of the contract over a client, with the binary `blob-conformance` |
| `fsdir/src/lib.rs` | `drivers/writer/fs/src/lib.rs` | **split out**: `Fs` and its tests are the shared library, so that `blob/fs` and `blob/fsreadonly` do not require each other; the `suite` module is not in it; the documentation names no actor and no decision record |
| `fsdir/src/service.rs` | `drivers/writer/fs/src/service.rs` | the stubs and the refusal helper come from atoom; `Service::router()` is new |
| `fs/src/suite.rs` | `drivers/writer/fs/src/suite.rs` | the backend suite; `crate::{Error, Fs}` come from `blob/fsdir` |
| `fs/tests/port.rs` | `drivers/writer/fs/tests/port.rs` | the tests serve through `atoom_socket`, run the conformance kit through the socket, and test `check`; the test of the caller check (a guard of sluis's host) stays in sluis |
| `fs/src/main.rs` | `drivers/writer/fs/src/main.rs` | the thin main: `serve` and `check` through `atoom_socket`, no registry owner check, no `--socket-gid`, no `conformance` and no `version` subcommand (`--version`); `check` exits 3 when the root cannot be served |
| `fsreadonly/src/lib.rs`, `suite.rs` | `drivers/indexer/fs/src/{lib,suite}.rs` | `ReadOnly::router()` is new; the registry names of the driver and of the writer's driver are gone |
| `fsreadonly/tests/port.rs` | `drivers/indexer/fs/tests/port.rs` | as for `fs`; the test of the two sockets with a peer check each stays in sluis |
| `fsreadonly/src/main.rs` | `drivers/indexer/fs/src/main.rs` | the thin main: one socket, and the owner uid of the directory is the flag `--owner-uid` (this process's own uid by default) where sluis took it from its registry; the second socket, `--socket-gid`, `conformance` and `version` stay in sluis |

What stayed in sluis: the wrapper of each driver's `main` (the sockets with peer credentials, `registry.DriverOwner`, the uid of the writing driver from the registry, the command line with `--socket-gid` and the `conformance` and `version` subcommands, which call the `suite::run_temp` of each library), and the audit actors that call the port.

Sluis requirement tags in the tests (`verifies: REQ-...`) and references to sluis's decision records and pages were dropped or replaced by pointers into this repository's pages.

Added in step G1 (2026-11-19, [docs/plan.md](../docs/plan.md), decision D11 A): the thin mains `blob-fs` and `blob-fsreadonly` carry the guard flags (`--allow-uid`, `--allow-any-uid`, `--socket-gid`, `--socket-mode`) and the `version` command. The owner check of sluis's host (`registry.DriverOwner`) is `--allow-uid` of the owner's uid here. The guard comes from sluis's kits (`kits/rust/sluis-kit`, `kits/go/rpc`) with the names left out: it decides by uid and knows no registry. What stays in sluis: the registry names, the call graph, the second socket of the reader and the `conformance` subcommand, until the hosts are deleted (step G4).
