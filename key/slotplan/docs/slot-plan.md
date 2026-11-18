# The board slot plan, as a table

*Copied from [truvity/sluis](https://github.com/truvity/sluis) `main` at commit `3f0afea8773b3d85205a5fb7d521282bbdcff598`, file `docs/reference/keys.md` (the section Board slot plan. The table is sluis's default map, which `slotplan.Default` holds as the reference map). The text stays there as it is (decision records are append-only); sluis links here for the card behaviour. Names such as `key@` are sluis's key service, the first consumer of this driver; the links that point into sluis's own pages are not part of this repository.*

## Board slot plan

The default card slot map, version 2 ([keys per board](/decisions/0006-keys-and-sealing/#keys-per-board), [the slot plan](/decisions/0006-keys-and-sealing/#the-slot-plan)). A purpose that an installation does not enable gets no slot and no key.

| Slot | Purpose | Algorithm on the card | Runs on |
|---|---|---|---|
| `82` | [`ssh-user`](#ssh-user) | [`ES384`](#es384) | every board |
| `83` | [`ssh-host`](#ssh-host) | [`ES384`](#es384) | every board |
| `84` | [`audit-log`](#audit-log) | [`ES256`](#es256) | every board |
| `85` | [`release-signing`](#release-signing) | [`ES384`](#es384) | every board |
| `86` | [`workload-issuer`](#workload-issuer) | [`ES384`](#es384) | bare-metal |
| `87` | [`pin-release`](#pin-release) | [`ES384`](#es384) | bare-metal |
| `88` | [`token-signing-rs256`](#token-signing-rs256) | [`RS256`](#rs256) | every board |
| `89` | [`issuance-log`](#issuance-log) | [`ES384`](#es384) | every board |
| `8a` | [`enrolment`](#enrolment) | [`ECDH-P-384`](#ecdh-p-384) | every board |
| `8b` | [`token-signing-ps256`](#token-signing-ps256) | [`PS256`](#ps256) | every board |
| `8c` | [`token-signing-es256`](#token-signing-es256) | [`ES256`](#es256) | every board |
| `8d` | [`token-signing-eddsa`](#token-signing-eddsa) | [`EdDSA`](#eddsa) | every board |
| `8e` | [`witness`](#witness) | [`EdDSA`](#eddsa) | every board |
| `8f` | [`audit-log-second`](#audit-log-second) | [`ES256`](#es256) | bare-metal |
| `9a` | [`token-signing`](#token-signing) | [`ES384`](#es384) | every board |
| `9c` | [`x509-intermediate`](#x509-intermediate) | [`ES384`](#es384) | every board |
| `9d` | [`wrapping`](#wrapping) | [`ECDH-P-384`](#ecdh-p-384) | every board |

**Policies:** Every slot is generated with PIN once and touch never, except 87 (PIN never, touch never). A slot is generated only when its purpose is enabled, and purposes outside the baseline stay enabled on use. Slot f9, the Yubico attestation slot, is never touched.

**Spare slots:** `90` to `95` (6). Rotation spares: the new key of an event rotation while the old one finishes, and the older key-agreement key during a rewrap. A purpose that has a slot above takes that slot, never a spare one; a purpose the map does not hold (a further trust domain's intermediate, a third audit writer instance) is named in the KeyPlan with a slot of this range, and then the rotations have one slot fewer. The card driver serves the slots of the default map that it has a row for (version 1's ten: 9a, 9c, 9d and 82 to 88) and nothing else: it has no key for the purposes of 89 to 8f, so key@ refuses a KeyPlan that enables one of them on a card (serving them is separate Core: boards work).

**Unused:** slot `9e`. The card-authentication slot signs without a PIN by default.
