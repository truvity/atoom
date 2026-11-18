# Provenance

The code of this library was copied as it is from [truvity/sluis](https://github.com/truvity/sluis) `main` at commit `3f0afea8773b3d85205a5fb7d521282bbdcff598` (the extraction plan, step 2, [docs/plan.md](../../docs/plan.md)). The history stays in sluis. 

| Here | From sluis | Changed |
| --- | --- | --- |
| `slotplan.go`, `slotplan_test.go`, `testdata/vectors.json` | `kits/go/slotplan/` | the package comment says that `Default` is the reference map (plan, R3 A); the test that held `Default` to sluis's `docs/architecture/keys.yaml` stayed in sluis, and the other half of that test (the ten rows of version 1, no slot in the rotation spares, the card serves a subset) is here |
| `docs/README.md`, `docs/slot-plan.md` | `docs/decisions/0006-keys-and-sealing.md` (The slot plan; the amendment of 2026-11-18, default slot map version 2), `docs/reference/keys.md` (Board slot plan) | the sections named at the top of each page, copied |

sluis's Rust loader of its signed key plan reads the same `testdata/vectors.json`: sluis takes the file from this library (by the pinned commit) in its own PR.
