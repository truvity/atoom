# key/slotplan

The check of a card slot map: which PIV slot each enabled key purpose of a plan lives in. Every program that takes a slot map asks the same question through this library, so that they refuse the same maps.

| | |
| --- | --- |
| Go module | `github.com/truvity/atoom/key/slotplan` (no requirement at all) |
| API | `Resolve`, `Check`, `CheckCard` over a list of `Purpose`; `Reference` and `CardServed` (the reference map) and `WithReference`; `CardStore` |
| Documentation | [docs/README.md](docs/README.md) (the rules and the versions of the map), [docs/slot-plan.md](docs/slot-plan.md) (the table of slots) |
| Test vectors | [testdata/vectors.json](testdata/vectors.json), read by the tests and by any other implementation of the same check |
| Where it came from | [provenance](../../docs/provenance.md#key-slotplan) |

## The reference map

`Reference` (before 0.7.0 `Default`) holds the purposes of sluis, the first consumer, with the slot each takes (version 2 of the map). It is the **reference map**, data a host may take and not a default of the mechanism ([record 0021](../../docs/decisions/0021-drivers-know-no-deployment-shapes.md)). The mechanism (`Resolve`, `Check`, `CheckCard`: two purposes in one slot, a purpose with no slot, a slot that is no board slot, a purpose listed twice) knows no purpose name: **every card-key purpose names its slot**, and `Resolve` takes none from the map. A host whose plans let a purpose take its reference slot (sluis's KeyPlan) fills the slots in first, explicitly, with `WithReference`; `purpose.Reference()` carries the reference slots already.

`CardServed` is the part of the map that a card can hold: every row whose algorithm is `ES384`, `ES256`, `RS256` or `ECDH-P384`. `CheckCard` adds the card driver's one limit to `Check`: a purpose whose algorithm a card does not hold (`PS256`, `EdDSA`) is refused. The card driver serves any purpose that a host declares, with a slot from the host or from `Default`, so a host with purposes of its own names a rotation spare for each.
