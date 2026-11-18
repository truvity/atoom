# Provenance

The code of the source libraries was copied as it is from [truvity/sluis](https://github.com/truvity/sluis) `main` at commit `df108e5470802659a055373dc20f254066eb3a1f` (the extraction plan, step 4, [docs/plan.md](../docs/plan.md)). The history stays in sluis.

| Here | From sluis | Changed |
| --- | --- | --- |
| `proto/atoom/source/v1alpha1/source.proto` | `proto/sluis/port/source/v2alpha1/source.proto` | package `sluis.port.source.v2alpha1` renamed to `atoom.source.v1alpha1` (plan, R1 A); messages, fields and numbers unchanged; the comments no longer point at sluis's pages and name this repository's drivers |
| `gen/atoom/source/v1alpha1/` | `gen/go/sluis/port/source/v2alpha1/` | regenerated from the file above |
| `conformance/src/suite.rs`, `lib.rs`, `main.rs` | the `suite.rs` of `drivers/store/file` and of `drivers/store/credential` | **new, not merged**: those two suites call each driver's backend (`Dir`) in a temporary directory and cannot judge another driver, so they stay with their drivers (below); the kit is a suite of the contract over a client, the cases both drivers share (capabilities, the bytes and the label, `if_none_match`, the absent name, the malformed names, watch by capability), with the binary `source-conformance --socket PATH --present NAME --bytes FILE` |
| `file/src/lib.rs`, `service.rs`, `suite.rs` | `drivers/store/file/src/{lib,service,suite}.rs` | the stubs and the refusal helper come from atoom; `Service::router()` is new; the documentation no longer points at sluis's pages and names no actor; the suite is called the backend suite and keeps its cases |
| `file/tests/port.rs` | `drivers/store/file/tests/port.rs` | the tests serve through `atoom_socket`, run the conformance kit through the socket, and test `check`; the tests of the caller check (store@ only) and of `--socket-gid` stay in sluis with the wrapper they test |
| `file/src/main.rs` | `drivers/store/file/src/main.rs` | the thin main: `serve` and `check` through `atoom_socket`, no registry owner check, no `--socket-gid`, no `conformance` and no `version` subcommand (`--version`); `check` exits 3 when the directory cannot be served |
| `credential/src/lib.rs`, `service.rs`, `suite.rs` | `drivers/store/credential/src/{lib,service,suite}.rs` | as for `file`; the `verifies: REQ-...` tags of the tests were dropped |
| `credential/tests/port.rs` | `drivers/store/credential/tests/port.rs` | as for `file` |
| `credential/src/main.rs` | `drivers/store/credential/src/main.rs` | as for `file`; `--dir` falls back to `$CREDENTIALS_DIRECTORY` as before |

What stayed in sluis: the wrapper of each driver's `main` (the socket with peer credentials, `registry.DriverOwner`, the command line with `--socket-gid` and the `conformance` and `version` subcommands, which call the `suite::run_temp` of the library), `actors/store` (store@, which links the file driver for `store check --validate`), `actors/exchange` (a client of the source port) and the signed configuration.

Sluis requirement tags in the tests (`verifies: REQ-...`) and references to sluis's decision records and pages were dropped or replaced by pointers into this repository's pages.
