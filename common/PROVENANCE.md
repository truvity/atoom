# Provenance

The code of this library was copied as it is from [truvity/sluis](https://github.com/truvity/sluis) `main` at commit `e16a60a6e2001318452c9c52aef77cb9fbc0da4e` (the extraction plan, step 1, [docs/plan.md](../docs/plan.md)). The history stays in sluis.

| Here | From sluis | Changed |
| --- | --- | --- |
| `proto/atoom/common/v1alpha1/common.proto` (`Refusal`, `IndexEntry`) | `proto/sluis/type/v2alpha1/type.proto` | package `sluis.type.v2alpha1` renamed to `atoom.common.v1alpha1` (plan, R1 A); fields and numbers unchanged |
| `gen/atoom/common/v1alpha1/` | `gen/go/sluis/type/v2alpha1/` (the two messages) | regenerated from the file above |
| `refusal/refusal.go`, `refusal/refusal_test.go` | `kits/go/refusal/` | import of the stubs; the wire name in the comments; one test added for the wire name |
| `src/refusal.rs` | `kits/rust/sluis-kit/src/refusal_impl.rs` | the functions are `new`, `detail` and `reason` in a public module; `detail` is new (it returns the whole `Refusal`); tests added |
| `build.rs`, `Cargo.toml` | `kits/rust/sluis-proto/build.rs` and its `Cargo.toml` | reads this library's `proto/` only |

What stayed in sluis: `Address`, `Header`, `HttpReply` and `Grant` of `type.proto`, which are sluis's own domain.
