# The slot plan

*Copied from [truvity/sluis](https://github.com/truvity/sluis) `main` at commit `3f0afea8773b3d85205a5fb7d521282bbdcff598`, file `docs/decisions/0006-keys-and-sealing.md` (the section The slot plan and the amendment "default slot map version 2"). The text stays there as it is (decision records are append-only); sluis links here for the card behaviour. Names such as `key@` are sluis's key service, the first consumer of this driver; the links that point into sluis's own pages are not part of this repository.*

## The slot plan

A documented, versioned map says which card slot serves which purpose: one slot per purpose and per algorithm, and a range of rotation spares. This is the default map, version 2 ([the amendment of 2026-11-18](/decisions/0006-keys-and-sealing/#amendment-of-2026-11-18-default-slot-map-version-2)); version 1, of 2026-11-03, pinned the first ten slots and left `89` to `95` spare. The map covers PIV slots `9a`, `9c`, `9d`, `9e` and the retired slots `82` to `95`. Every board slot is generated with a PIN policy and a touch policy that the map states, and the attestation of the slot shows them.

| Slot | Purpose |
|---|---|
| `9c` | X.509 intermediate |
| `9a` | Token signing (ES384) |
| `9d` | Key agreement (ECDH): unwraps data keys, the boundary's `grant` data key among them |
| `82` | `ssh-user` CA |
| `83` | `ssh-host` CA |
| `84` | Log key of an audit writer instance |
| `85` | OpenPGP release-signing subkey (release boundary only) |
| `86` | Workload issuer, for `sluis-node` (enabled on use) |
| `87` | PIN release key: signs the `PinReleaseRequest` of a locked board; PIN policy never, touch never |
| `88` | RS256 token key of the per-audience exception (RSA-2048; PIN once, touch never; enabled on use) |
| `89` | Issuance log key of `ca` (P-384) |
| `8a` | Enrolment key of `exchange` (P-384 ECDH) |
| `8b` | FAPI PS256 token key (RSA-2048) |
| `8c` | FAPI ES256 token key (P-256) |
| `8d` | FAPI EdDSA token key (Ed25519; firmware 5.7 or newer) |
| `8e` | Witness key (Ed25519) |
| `8f` | Log key of a second audit writer instance (P-256) |
| `90` to `95` | Rotation spares (six) |
| `9e` | Not used: the card-authentication slot signs without a PIN by default |

- **Slot `9d` is the key-agreement slot, and it is the only one.** Grants do not use it directly: they are sealed under a boundary data key, so a grant costs a memory operation and not an ECDH on the card ([Seal and open](/decisions/0006-keys-and-sealing/#seal-and-open)).
- **A purpose that the map holds has its slot, and a purpose enabled on use takes that slot and no other.** The map is built in; a `KeyPlan` that lists a purpose of the map without a slot gets the slot of the map, and `key@` checks a slot that the `KeyPlan` names against the other purposes. A purpose that the map does not hold, such as a further trust domain's X.509 intermediate or a third audit writer instance, is named in the `KeyPlan` with a slot of the rotation spares. **Today the card driver serves ten rows of the map (`9a`, `9c`, `9d` and `82` to `88`, slot `88` being the RS256 purpose's) and none of `89` to `8f`:** a purpose of those slots cannot be enabled on a card yet, and `key@` refuses a `KeyPlan` that lists one (or another slot for a purpose of the served rows) with the entry named. Serving them is work for the boards milestone, with its own tests. The RS256 key is RSA-2048. One RS256 signature takes about 0.17 seconds on a YubiKey 5 with firmware 5.8 (MEASURED 2026-11-08, median of 50: 173 ms; an ES384 signature on the same card 148 ms), and it holds the card for every other use while it runs. So key@ limits the purpose to 2 a second with a burst of 4, and the card driver admits four at once (that burst), signs them one at a time, and refuses the rest as busy; a call of another key waits for about one to two RSA signatures (up to 0.47 s in one sample; the bound is the four admitted, about 0.7 s). PS256 was not measured; it is the same card operation as RS256, so expect about the same; the issuance log ([REQ-KEYS-063](/requirements/keys/#REQ-KEYS-063)), in slot `89`, is the next purpose that needs a row of the card driver.
- **The six rotation spares, `90` to `95`, hold the new key of an event rotation** while the old one finishes, and the older key-agreement key until every wrap is rewrapped. A purpose that the map does not hold takes one of them too, and then a rotation has one slot fewer.
- **The map records each slot's attestation,** the policies it shows included: PIN once and touch never for every slot but `87`, PIN never and touch never for `87`. A slot is generated only when its purpose is enabled, so a card holds the keys of the enabled purposes and nothing in the other pinned slots. Slot `f9`, the Yubico attestation slot, is never touched. The slot map of root cards is ritueel's. The keys page shows the map.
- **The release subkey lives in a PIV slot reached through `key@`,** which also makes the subkey's back-signature on the card. The OpenPGP applet is not used on a board.
- **A second card per board serves a further L1 boundary** on the same engine, so that the boundaries are separated by wrapping keys of their own ([0008](/decisions/0008-boundaries-and-tenants/#the-ladder)).


## Amendment of 2026-11-18: default slot map version 2

2026-11-18 (maintainer decision S1 A, "Agree" to the proposed map; the plan is on the [roadmap](/roadmap/#the-maintainers-decisions-of-2026-11-18)). The default card slot map is now version 2. It changes only slots that version 1 left spare; every assignment of version 1 stays.

| Slot | Purpose | Version 1 | Version 2 |
|---|---|---|---|
| `89` | Issuance log key of `ca` (P-384) | spare | pinned |
| `8a` | Enrolment key of `exchange` (P-384 ECDH) | spare | pinned |
| `8b` | FAPI PS256 token key (RSA-2048) | spare | pinned |
| `8c` | FAPI ES256 token key (P-256) | spare | pinned |
| `8d` | FAPI EdDSA token key (Ed25519, firmware 5.7 or newer) | spare | pinned |
| `8e` | Witness key (Ed25519) | spare | pinned |
| `8f` | Log key of a second audit writer instance (P-256) | spare | pinned |
| `90` to `95` | Rotation spares (six) | spare | spare |

The other slots are as in [the slot plan](/decisions/0006-keys-and-sealing/#the-slot-plan): `9a`, `9c`, `9d`, `82` to `88` pinned and `9e` unused.

- **Why pin.** Every board of a version has the same layout, so enabling a purpose later never renumbers the keys of a live card. The cost is that fewer slots are free: six rotation spares instead of thirteen.
- **The policies stay.** Every slot is generated with PIN once and touch never, except `87` (PIN never, touch never). A slot is generated only when its purpose is enabled, and purposes outside the baseline stay enabled on use.
- **Slot `f9` is never touched.** It is the slot of the Yubico attestation key, and the map does not list it.
- **Cloud federation keys and NATS keys stay off the cards.** The GCP ES256 key, the Azure RS256 key and the NATS nkeys live in the key service, as the rule that cloud keys live outside the board installation requires. The FAPI keys of `8b` to `8d` and the RS256 key of `88` serve the audiences of the installation.
- **The registry names the second audit writer instance `audit-log-second`.** On a board the log key of a second writer instance is a purpose of its own, because a `KeyPlan` lists a purpose once; a third instance is named in the `KeyPlan` with a rotation spare. On AWS a further writer instance has a key under `audit-log`, as before ([Keys per boundary on AWS](/decisions/0006-keys-and-sealing/#keys-per-boundary-on-aws)).
- **Code and docs agree, serving comes later.** The default map of `key@`'s check, of its Rust twin and of the configuration check is version 2, and a `KeyPlan` that lists a purpose of `89` to `8f` without a slot gets the slot of the map. The card driver still serves its ten rows, and `key@` refuses a `KeyPlan` that enables a purpose of `89` to `8f` on a card, as it refuses today; slot `88` and the new purposes on the card are separate work of the boards milestone.
