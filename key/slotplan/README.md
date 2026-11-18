# key/slotplan

The check of a card slot map: which PIV slot each enabled key purpose of a plan lives in. Every program that takes a slot map asks the same question through this library, so that they refuse the same maps.

| | |
| --- | --- |
| Go module | `github.com/truvity/atoom/key/slotplan` (no requirement at all) |
| API | `Resolve`, `Check`, `CheckCard` over a list of `Purpose`; `Default` and `CardServed` (the reference map); `CardStore` |
| Documentation | [docs/README.md](docs/README.md) (the slot plan and its version 2), [docs/slot-plan.md](docs/slot-plan.md) (the table) |
| Test vectors | [testdata/vectors.json](testdata/vectors.json), read by the tests and by any other implementation of the same check |
| Where it came from | [PROVENANCE.md](PROVENANCE.md) |

## The reference map

`Default` holds the purposes of sluis, the first consumer, with the slot each takes (version 2 of its map): it is the **reference map**. The mechanism (`Resolve`, `Check`, `CheckCard`: two purposes in one slot, a purpose with no slot, a slot that is no board slot, a purpose listed twice) is the part that is not sluis's. When another consumer with other purposes appears, the table is split from the mechanism (docs/plan.md, R3); until then a consumer takes the table as it is or brings its own `Purpose` list with the slots named.

`CardServed` is the part of the map that the card driver serves today (version 1's ten rows); `CheckCard` adds the card driver's limit to `Check`: no override of a slot, no purpose outside the rows it serves.
