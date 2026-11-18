# Provenance

The code of this library was copied as it is from [truvity/sluis](https://github.com/truvity/sluis) `main` at commit `3f0afea8773b3d85205a5fb7d521282bbdcff598` (the extraction plan, step 2, [docs/plan.md](../../docs/plan.md)). The history stays in sluis. 

| Here | From sluis | Changed |
| --- | --- | --- |
| `pinrelease.go`, `pinrelease_test.go` | `kits/go/pinrelease/pinrelease.go`, `pinrelease_test.go` | `PurposePinRelease` and `SlotPinRelease` (they were in `slotmap.go`) are here; the package comment says "key service" where it said `key@`; the wire domains `sluis-pin-release-v1` and `sluis-pin-release-answer-v1` are unchanged (they are the wire of every board) |
| `docs/README.md` | | new |

What stayed in sluis: `kits/go/pinrelease/slotmap.go` (`SlotMapOf`, `DecodeSlotMap`, `SignerKey`: they read the slot map of a signed key plan with sluis's signed configuration, store and schema).
