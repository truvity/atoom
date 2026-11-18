# The card slot map, touch, PIN and exclusive access

*Copied from [truvity/sluis](https://github.com/truvity/sluis) `main` at commit `3f0afea8773b3d85205a5fb7d521282bbdcff598`, file `docs/concepts/keys.md` (the sections Keys per board (with The card slot map) and Exclusive card access, touch and PIN). The text stays there as it is (decision records are append-only); sluis links here for the card behaviour. Names such as `key@` are sluis's key service, the first consumer of this driver; the links that point into sluis's own pages are not part of this repository.*

## Keys per board

Each board has a card of its own, a YubiKey in PIV mode with 24 slots and no symmetric keys. A card belongs to one boundary, and every key of the board is born on its card. A second card per board serves a further L1 boundary on the same engine. Data keys are not on the card: key@ wraps them for each card's key-agreement key. A lost board is replaced, not restored: a new key on a new card, a new certificate, and ritueel revokes the old one; the other boards rewrap every data key for the new card. The root cards are ritueel's and never sit in a board.

### The card slot map

A documented, versioned map says which slot serves which purpose: one slot per purpose and per algorithm, and six rotation spares. This is the default map, version 2 (decided 2026-11-18; version 1 left `89` to `95` spare). Every board of a version has the same layout, so enabling a purpose later never renumbers the keys of a live card.

| Slot | Purpose |
|---|---|
| `9c` | X.509 intermediate (generated only when the PKI add-on turns the token certificate on; the core needs no slot `9c`) |
| `9a` | Token signing |
| `9d` | Key agreement (ECDH): unwraps data keys, the boundary's `grant` data key among them |
| `82` | `ssh-user` CA |
| `83` | `ssh-host` CA |
| `84` | Log key of an audit writer instance |
| `85` | OpenPGP release-signing subkey (release boundary only) |
| `86` | Workload issuer, for `sluis-node` (enabled on use) |
| `87` | PIN release key: signs the request of a locked board (PIN policy never, touch never) |
| `88` | RS256 token key of the per-audience exception (RSA-2048, enabled on use) |
| `89` | Issuance log key of `ca` (P-384) |
| `8a` | Enrolment key of `exchange` (P-384 ECDH) |
| `8b` | FAPI PS256 token key (RSA-2048) |
| `8c` | FAPI ES256 token key (P-256) |
| `8d` | FAPI EdDSA token key (Ed25519, firmware 5.7 or newer) |
| `8e` | Witness key (Ed25519) |
| `8f` | Log key of a second audit writer instance (P-256) |
| `90` to `95` | Rotation spares (six) |
| `9e` | Not used: the card-authentication slot signs without a PIN by default |

A slot is generated only when its purpose is enabled; the rotation spares hold the new key of an event rotation while the old one finishes, and a purpose that the map does not hold (a further trust domain's intermediate) is named in the installation's key plan with one of them. Cloud federation keys and NATS keys are not card keys. Slot `f9`, the Yubico attestation slot, is never touched. `sluis config check`, `sluis key request` and board initialisation refuse a collision and an enabled purpose with no slot. The release subkey lives in a PIV slot like every other board key; the OpenPGP applet is not used ([ADR 0006](/decisions/0006-keys-and-sealing/#the-slot-plan)).


## Exclusive card access, touch and PIN

key@ has the card to itself. Its card driver runs as a dedicated user, the card service accepts only that user, and no other software that talks to cards runs on the board. The reason was measured on 2026-10-31 on a YubiKey 5 Nano (firmware 5.8.0): the PIN state belongs to the powered card and not to a connection, so while it is unlocked any local process can sign; any client's default disconnect resets the card; and wrong PINs from any local user block it ([ADR 0006](/decisions/0006-keys-and-sealing/#exclusive-card-access)).

Board cards are touch never and PIN once, so a board signs unattended. A board starts locked, and key@ never stores the PIN in plaintext on disk:

- `sluis node unlock <node>` delivers the PIN over an authenticated channel;
- an unlocked peer releases the board's PIN through the store, sealed to a one-time key of the locked board's card driver, which covers a reboot of one board and a rolling update; the locked key@ signs its request with a card slot that needs no PIN, the peer verifies the signature and the slot's attestation first, and the locked board verifies the peer's signed answer before its card is given the PIN;
- after a full power loss one manual unlock is needed, and the first board then unlocks the others;
- a wrong PIN is never retried, because the card blocks after three attempts and then needs the PUK.
