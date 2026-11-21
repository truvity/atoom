# The slot plan

What `key/slotplan` checks: a versioned map from key purpose to PIV slot, which the library holds to a few rules and the card driver holds to the rows it serves.

## The map

A map says which card slot serves which purpose: one slot per purpose and per algorithm, and a range of rotation spares. It covers the PIV slots `9a`, `9c`, `9d`, `9e` and the retired slots `82` to `95`. Every slot is generated with a PIN policy and a touch policy that the map states. Every card of one version of the map has the same layout, so enabling a purpose later never renumbers the keys of a live card.

`slotplan.Default` is the reference map. It holds the purposes of sluis, the first consumer, and the slot each takes. [The table](slot-plan.md) lists every slot with its algorithm and policy. The mechanism does not depend on those purposes: a consumer with other purposes brings its own `Purpose` list with the slots named, and the purposes of `Default` stay as the reference.

## The rules

`Resolve`, `Check` and `CheckCard` hold a plan, a list of `Purpose` (name, algorithm, and a slot or none), to these rules. A refusal names the entry and a word, never a value.

| Rule | Built today |
|---|---|
| A purpose names its slot, or takes the one of `Default`. A purpose that has neither is refused, and the message names the spare slots `90` to `95`. | yes |
| The slot is a board slot: `9a`, `9c`, `9d`, or a retired slot `82` to `95`, in lower-case hex. `9e` is refused. | yes |
| Two purposes in one slot are refused. | yes |
| A purpose listed twice is refused. | yes |
| A purpose that the plan does not list is not enabled: it needs no slot and gets no key. | yes |
| `CheckCard` adds the card driver's limits: no override of a slot, and no purpose outside the rows the card driver serves (`CardServed`). | yes |

- **Which purposes count.** The checks apply to a purpose whose algorithm is a card key: `ES384`, `ES256`, `RS256` and `ECDH-P384` (also spelled `ECDH-P-384`). A purpose with another algorithm is skipped by every check. `PS256` and `EdDSA` are not on the list yet, so a plan with two such purposes in one slot passes. They join the list with the card driver's rows for the slots `8b`, `8d` and `8e`.
- **The card serves ten rows today:** `9a`, `9c`, `9d` and `82` to `88`. A purpose of the slots `89` to `8f` has its slot in the map and no row in the driver, so `CheckCard` refuses it. The driver's own table is held to `CardServed` by its tests.
- **The RS256 row is optional on a card.** Slot `88` is a fixed row, not a name in the plan: the card driver verifies no override of a slot. A key in slot `88` that is not the plan's leaves the purpose absent and does not stop the driver.
- **Purposes are the host's.** The key port is meant to take any purpose that a host declares, with its slot named by the host. That change is planned. Today the card driver serves the closed list of rows above, and `Default` is the only table the library holds.

## Versions of the map

| Version | Slots pinned | Spare |
|---|---|---|
| 1 | `9a`, `9c`, `9d`, `82` to `88` (ten slots) | `89` to `95` |
| 2 (the one in `Default`) | version 1, and `89` to `8f` | `90` to `95` (six) |

Version 2 changed only slots that version 1 left spare, so every assignment of version 1 stays. The cost of pinning is that fewer slots are free: six rotation spares instead of thirteen. `CardServed` is still the ten rows of version 1.

## Where the map stops

- **Keys that are not card keys have no row.** A cloud-federation key and the keys of a messaging system live in a key service outside the card, so the map has no slot for them.
- **Slot `f9` is never touched.**
- **The map says nothing about who may use a key.** The host that runs the driver decides that.

The card driver is [key/yubikeypiv](../../yubikeypiv/README.md). The measured cost of an RSA signature on the card is in [algorithms](../../docs/algorithms.md).
