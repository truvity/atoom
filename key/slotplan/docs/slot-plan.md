# The reference slot map

The table of `slotplan.Default`: the slot, the algorithm and the policy of each purpose. The purposes are sluis's, the first host of the card driver, so this is the reference map and not a list that the library or the driver prescribes: a host declares its own purposes to the driver and names the slot of each that the map does not hold.

## The table

`Default` is version 2 of the map. A purpose that a host does not enable gets no slot and no key.

| Slot | Purpose | Algorithm on the card | PIN policy | Touch policy | A card holds it |
|---|---|---|---|---|---|
| `9a` | `token-signing` | `ES384` | once | never | yes |
| `9c` | `x509-intermediate` | `ES384` | once | never | yes |
| `9d` | `wrapping` | `ECDH-P384` | once | never | yes |
| `82` | `ssh-user` | `ES384` | once | never | yes |
| `83` | `ssh-host` | `ES384` | once | never | yes |
| `84` | `audit-log` | `ES256` | once | never | yes |
| `85` | `release-signing` | `ES384` | once | never | yes |
| `86` | `workload-issuer` | `ES384` | once | never | yes |
| `87` | `pin-release` | `ES384` | never | never | yes |
| `88` | `token-signing-rs256` | `RS256` (RSA-2048) | once | never | yes, optional |
| `89` | `issuance-log` | `ES384` | once | never | yes, when a host declares it |
| `8a` | `enrolment` | `ECDH-P384` | once | never | yes, when a host declares it |
| `8b` | `token-signing-ps256` | `PS256` (RSA-2048) | once | never | no (the port has no `PS256`) |
| `8c` | `token-signing-es256` | `ES256` | once | never | yes, when a host declares it |
| `8d` | `token-signing-eddsa` | `EdDSA` (Ed25519; firmware 5.7 or newer) | once | never | no (the port has no `EdDSA`) |
| `8e` | `witness` | `EdDSA` (Ed25519) | once | never | no (the port has no `EdDSA`) |
| `8f` | `audit-log-second` | `ES256` | once | never | yes, when a host declares it |
| `90` to `95` | none | none | | | rotation spares (six) |
| `9e` | none | none | | | not used |

- **Policies.** A slot is generated with the PIN policy and touch policy above, and the attestation of the slot shows them. `87` is the one slot that signs while the card is locked, so it has PIN policy never.
- **Slot `9e`** is the card-authentication slot, which signs without a PIN by default. The map does not use it.
- **Slot `f9`** is the Yubico attestation slot. The map does not list it and the driver never touches it.
- **A slot is generated only when its purpose is enabled.** A card holds the keys of the enabled purposes and nothing in the other slots.
- **The rows a card does not hold** are the three of `PS256` and `EdDSA`. `CheckCard` refuses a plan that enables one on a card. They are not algorithms that `Resolve` counts as card keys (see [the slot plan](README.md)).

## The rotation spares

The six slots `90` to `95` hold the new key of a rotation while the old one finishes, and the older key-agreement key until every wrap is rewrapped. A purpose that the map does not hold (a further trust domain's intermediate, a third audit writer instance) is named by the host's plan with one of them, and then a rotation has one slot fewer. A purpose that has a slot above takes that slot and never a spare one.

## Timing of the RSA row

One `RS256` signature takes about 0.17 s on a YubiKey 5 with firmware 5.8 (median 173 ms of 50, against 148 ms for `ES384` on the same card, measured 2026-11-08). The card driver admits four at once and refuses the rest as `busy`. See [algorithms](../../docs/algorithms.md).
