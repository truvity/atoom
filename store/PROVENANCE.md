# Provenance

The code of the store libraries was copied as it is from [truvity/sluis](https://github.com/truvity/sluis) `main` at commit `3f0afea8773b3d85205a5fb7d521282bbdcff598` (the extraction plan, step 3, [docs/plan.md](../docs/plan.md)). The history stays in sluis.

| Here | From sluis | Changed |
| --- | --- | --- |
| `proto/atoom/store/v1alpha1/item.proto` | `proto/sluis/port/item/v2alpha1/item.proto` | package `sluis.port.item.v2alpha1` renamed to `atoom.store.v1alpha1` (plan, R1 A); `IndexEntry` imported from `atoom.common.v1alpha1`; messages, fields and numbers unchanged; comments no longer name sluis's actors |
| `gen/atoom/store/v1alpha1/` | `gen/go/sluis/port/item/v2alpha1/` | regenerated from the file above |
| `conformance/src/suite.rs`, `rules.rs`, `lib.rs` | `kits/rust/sluis-conformance/src/{suite,rules,lib}.rs` | the stubs, `IndexEntry` and the refusal helpers come from atoom; the suite takes an `atoom_socket::Client`; the decoder of a refusal detail is `atoom_common::refusal::detail`; comments point at this repository's pages |
| `conformance/src/main.rs` | the `conformance` subcommand of `drivers/store/rqlite/src/main.rs` | a binary of its own, `store-conformance --socket PATH`, for any driver |
| `memory/src/lib.rs`, `memory/src/tests.rs` | `kits/rust/sluis-conformance/src/memory.rs` | a driver of its own (the feature `memory` of the kit is gone); it serves through `atoom_socket` with no uid table; `router()` is new; the tests moved with it |
| `memory/src/main.rs` | | new: the thin main |
| `rqlite/src/{client,sql,sweeper,service}.rs`, `rqlite/tests/driver.rs` | `drivers/store/rqlite/src/*.rs`, `tests/driver.rs` | the stubs and refusal helpers come from atoom; `Driver::ensure_schema_within` (the wait for rqlite that `serve` had) is new; the tests serve through `atoom_socket`, run a second suite instead of the `conformance` subcommand, and find `rqlited` under `.cache/rqlite` of this repository |
| `rqlite/src/config.rs` | `drivers/store/rqlite/src/config.rs` | `Settings::new` is new; the flags are behind the feature `cli` and read from clap's matches, not from sluis's `Flags`; `--socket-gid` is not here |
| `rqlite/src/main.rs` | `drivers/store/rqlite/src/main.rs` | the thin main: `serve` and `check` through `atoom_socket`, no registry owner check, no `conformance` and no `version` subcommand (`--version`) |

What stayed in sluis: the wrapper of each driver's `main` (the socket with peer credentials, `registry.DriverOwner`, the command line, `--socket-gid`, the version stamp), `actors/store` (store@), `kits/go/store`, `kits/rust/sluis-store`, the registry, the kinds and the signed configuration.

Sluis requirement tags in the tests and comments (`verifies: REQ-...`) and references to sluis's decision records were dropped or replaced by pointers into this repository's pages.

Added in step 3b (new, not from sluis): the call `Capabilities` of `item.proto` (an additive change, plan R4: `store` and `tail_log`), its answers in `memory` and `rqlite`, the skip of the log cases and the case `no_log_is_refused` in the kit, and the Go driver [`dynamodb`](dynamodb/PROVENANCE.md).
