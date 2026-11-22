# The card slot map

Which PIV slot of a YubiKey holds which key, with the algorithm and the PIN and touch policy of each, as the `key/yubikeypiv` driver makes and adopts them.

## The card

A YubiKey in PIV mode has 24 key slots and no symmetric keys. The driver holds one card for one host, and every key it serves is born on that card ([slots-pin-touch.md](slots-pin-touch.md)). A card has no backup: a host whose card is lost makes new keys on a new card.

## The rows the driver serves

The driver holds no table of purposes. The host declares each purpose it uses (a name, an algorithm, the operations allowed and, for a purpose that is not in the reference map, a slot; [the declaration](../../docs/key-port.md#the-declaration-of-purposes)), and `plan.go` makes a row of each: its slot (the one the host names, or the one of the reference map), its algorithm and its policy. Two purposes in one slot, a slot that is no slot of a board, a purpose with no slot and an algorithm that a card does not hold are refused before the card is opened, and a card whose slot holds another key than the declared one refuses the start. A host with other purposes (session declares `ES256` and `ES384` purposes for its client assertions and its identity header) names a rotation spare (`90` to `95`) for each.

Every key is generated with PIN once and touch never, except the PIN release key, which is the purpose named `pin-release` (`ES384`, slot `87`, PIN never, and it signs nothing for a caller). The table below is the reference map, which is sluis's: the slots that its purposes take when the host names none.

| Slot | Purpose in the reference map | Algorithm | PIN policy | Touch policy |
| --- | --- | --- | --- | --- |
| `9c` | `x509-intermediate` | ES384 (ECDSA P-384, SHA-384) | once | never |
| `9a` | `token-signing` | ES384 | once | never |
| `9d` | `wrapping` | ECDH-P384 (key agreement, the only such slot) | once | never |
| `82` | `ssh-user` | ES384 | once | never |
| `83` | `ssh-host` | ES384 | once | never |
| `84` | `audit-log` | ES256 (ECDSA P-256, SHA-256) | once | never |
| `85` | `release-signing` | ES384 | once | never |
| `86` | `workload-issuer` | ES384 | once | never |
| `87` | `pin-release` | ES384 | never | never |
| `88` | `token-signing-rs256` | RS256 (RSA-2048, PKCS#1 v1.5, SHA-256) | once | never |

- **The names are the reference map, which is sluis's.** They are the purposes of sluis, the first host of the driver. The same table is `slotplan.Default` in [key/slotplan](../../slotplan/README.md), with the other slots of the map: the table with its reasons is in [the slot plan](../../slotplan/docs/README.md) and [slot-plan.md](../../slotplan/docs/slot-plan.md). What the driver needs from a row is the slot, the algorithm and the two policies. `purpose.Reference()` declares these ten and `issuance-log` as sluis does; `yubikeypiv.SlotPlan(purposes)` returns the rows that the driver makes of a declaration, for a host that checks them against its own registry.
- **One slot for each purpose and algorithm.** Every card of one version of the map has the same layout, so making a key for a purpose later never renumbers the keys of a card in use.
- **An `RS256` purpose is optional (`88` in the reference map).** A card whose slot of an `RS256` purpose holds a key that is not the plan's (another algorithm, an imported key) still starts, and the driver serves the other purposes and leaves that one absent. Any other slot with a wrong key refuses the start.
- **The RSA key is slow.** One private-key operation of the card takes about 0.17 s ([pcsc-and-cards-on-boards.md](pcsc-and-cards-on-boards.md#the-rsa-key-and-the-queue)).
- **Slots `89` to `8f`** have a place in version 2 of the map (an issuance log key, an enrolment key, three FAPI token keys, a witness key and a second audit log key). A purpose of those slots that is `ES384`, `ES256` or `ECDH-P384` (`89`, `8a`, `8c`, `8f`) is served like any other once a host declares it. The `PS256` (`8b`) and `EdDSA` (`8d`, `8e`) ones are not: the port has no such algorithm, and `slotplan.CheckCard` refuses a plan that enables one.
- **Slots `90` to `95`** are rotation spares: the new key of a rotation holds one while the old one finishes. A purpose that the map does not hold takes one of them, which the host names in its declaration (the kit of the port uses `90` to `94`).
- **Slot `9e` is not used,** because the card-authentication slot signs without a PIN by default. **Slot `f9`, the Yubico attestation slot, is never touched.**
- **The OpenPGP applet is not used.** A key that a host needs for an OpenPGP signature (`release-signing`, `85`) lives in a PIV slot, and the driver only signs digests with it.
- **Keys that are not card keys stay off the card.** The driver serves what the host declares with an algorithm a card holds. Cloud federation keys and message-bus keys of a host are the host's.

## Keys are generated on the card

A key is made on the card, with the policies of its row, in development only and with the factory management key. In any other environment the driver adopts a key it finds in the slot after it verifies the attestation (Yubico's roots, the firmware floor, the slot, the algorithm and both policies), and refuses a key that fails ([cards.md](cards.md)).
