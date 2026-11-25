# How a YubiKey holds keys

How a YubiKey 5 stores keys, what limits it, and what each part means for the `key/yubikeypiv` driver. The facts were read in Yubico's documentation, in the FIDO and WebAuthn standards and in tool source on 2026-11-10. Firmware numbers matter: older firmware does less.

## What is inside

A YubiKey 5 carries several applications: one-time codes, OATH and U2F among them. Three hold keys: PIV, OpenPGP and FIDO2. PIV (Personal Identity Verification) is the smart-card part. Yubico describes the applications as separate, with separate storage for keys (YubiKey 5 applications page). A PIV reset has no effect on other applications such as FIDO (Yubico PIV PIN page).

| Application | Holds | Its lock |
|---|---|---|
| PIV | 24 key slots, plus one factory key (slot f9) that vouches for the others. A certificate can sit beside each key. | A PIN (6 to 8 characters), a PUK that unblocks the PIN, and a management key for writing slots |
| OpenPGP | 3 keys: one to sign, one to decrypt, one to authenticate | A PIN and an Admin PIN |
| FIDO2 | Passkeys. Up to 100 kept on the card (firmware 5.7.4 and later; 25 before). | One PIN |

PIV and OpenPGP use the smart-card interface of the card, and only one program can hold it at a time. FIDO2 has its own interface, HID.

**The two applets are independent (measured 2026-10-31 on two cards, firmware 5.8.0).** Generating keys in six PIV slots, signing, and one wrong PIV PIN changed nothing in the OpenPGP applet: its data, PIN counters and signature counter were identical before and after. The driver uses PIV only; it never selects the OpenPGP applet ([card-slot-map.md](card-slot-map.md)). A `scdaemon` that holds the smart-card interface still locks the driver out, and the driver locks `scdaemon` out ([pcsc-and-cards-on-boards.md](pcsc-and-cards-on-boards.md)).

## One PIV slot, up close

A slot is a numbered place for one private key. Four things belong to it.

- **The private key** is generated on the card or imported, and can never be read out. A key born on a card has no copy. The driver only adopts keys that were generated on the card.
- **Certificates:** all of them share about 50,000 bytes.
- **The PIN rule and the touch rule** are chosen when the key is made or loaded and can never be changed after. "Once" means once per session: until the card is unplugged or another application is used. "Always" means the PIN is asked right before every use. "Cached" touch means a touch counts for 15 seconds. The slot plan makes every key PIN once and touch never, except `pin-release` (PIN never, touch never), so a host can sign without a person at the card.
- **The kind of key:** sign, or agree on a shared secret (for decryption). P-256, P-384 and RSA-2048 are on every YubiKey 5. RSA-3072, RSA-4096, Ed25519 and X25519 need firmware 5.7 or later (ykman checks 5.7.0). Ed25519 signs the whole message. The driver's plan uses P-384, P-256 and RSA-2048.

| Slot | Standard name | Asks for the PIN by default | Note |
|---|---|---|---|
| 9a | authentication | once per session | Meant for signing in. |
| 9c | digital signature | every time | Meant for signing as an authority. |
| 9d | key management | once per session | Meant for decryption. |
| 9e | card authentication | never | Signs with no PIN by default. The driver does not use it. |
| 82 to 95 | retired key management (20 slots) | once per session | Extra slots, with no special meaning of their own. The driver uses them for the rest of its plan. |
| f9 | attestation | not applicable | The factory key that vouches for the others. The driver never touches it. |

The default only applies when a key is made without stating a rule. The numbers are hexadecimal: 82 to 95 is twenty slots. Which purpose has which slot: [card-slot-map.md](card-slot-map.md).

## PIN, PUK and management key

- **PIN.** Three wrong PINs block it. The counters can be set from 1 to 255, for PIN and PUK together; setting them resets both to their defaults. The driver sends the PIN once, never retries, and sends none to a card with one attempt left; the PIN lives in locked memory ([slots-pin-touch.md](slots-pin-touch.md)).
- **PUK.** It unblocks the PIN; three wrong PUKs block it. The driver never uses the PUK. A card that is blocked is for a person to recover.
- **Management key.** It authorises writing slots. From firmware 5.7 it is AES-192 (3DES before), with the same octets. When it is stored PIN-protected (a convention of ykman and other tools, not a firmware feature), whoever knows the PIN can authenticate as manager and overwrite or delete any slot. The authority is all-or-nothing, not scoped per slot (Yubico PIV admin access page). The slots are one area of trust, not twenty-four. The driver uses the management key only to make a key, and only when its host allows factory defaults.
- **One PIN for all 24 slots.** That is why a host gives one driver the whole card.

## How a PIV signature happens

The card receives a digest (Ed25519: the whole message), checks the PIN and the touch if the slot's rule asks for them, and returns a signature. Three things follow:

- The card cannot show what it signs, so the program that builds the digest must be trusted. The driver is that program, and its host decides who may call it.
- A touch says that someone is at the card. It does not say which slot, or what for.
- Only one program can hold the smart-card side at a time. The driver connects exclusively; piv-go queues callers fairly and a second program makes the driver fail closed ([research/s4-piv-go-or-pkcs11.md](research/s4-piv-go-or-pkcs11.md), [research/yubikey-piv-speed-and-sharing-2026-11-13.md](research/yubikey-piv-speed-and-sharing-2026-11-13.md)).

## What the card can vouch for

- **Attestation.** For a key generated on it, the card issues a certificate that states: made on this card, with this serial, this firmware, this PIN rule and touch rule. Anyone can ask for it without the PIN. An imported key cannot get one. The driver verifies it against Yubico's roots for every key it adopts, and compares serial, slot, firmware and both policies with the plan. piv-go v2.6.0 does not embed the intermediate of cards made since August 2025, so the driver embeds the roots and intermediates itself.
- **Metadata.** From firmware 5.3 the card reports, per slot, the kind of key, the rules, whether it was generated or imported, and the public key.
- **Delete and move.** One key can be deleted or moved to another slot from firmware 5.7 (ykman checks 5.7.0). Before that, a slot could only be overwritten, or wiped by a PIV reset. The driver does neither.
- **Nothing is changed after birth:** not the PIN rule, not the touch rule, not the firmware. Cards older than firmware 5.7 have a known weakness in ECDSA signing (CVE-2024-45678, Yubico advisory YSA-2024-03), which is why 5.7.0 is the driver's floor.
- **No key leaves the card.** Two cards, each with a key born on it, hold two different keys.

## What a reset does

- **PIV reset.** Possible only when both the PIN and the PUK are blocked. It deletes the key in every slot. The driver never calls it. Measurements of what a reset puts back, and what "the card is clean" can prove, are in [research/yubikey-piv-pin-touch-and-reset-2026-11-13.md](research/yubikey-piv-pin-touch-and-reset-2026-11-13.md).
- **Session reset.** A warm reset at the PC/SC layer ends the PIN verification and touches nothing else. The driver does it at start and exit so that a card another process left PIN-verified is never trusted ([restart-and-session-reset.md](restart-and-session-reset.md)).
- **FIDO reset.** It needs no PIN: plug the card in, ask within about ten seconds, and touch. It removes the FIDO2 PIN and every passkey, and resets U2F too. The FIDO2 PIN is separate from the PIV PIN. FIDO2 locks after eight wrong PINs in a row (three need the card unplugged and plugged in again).

## The other two applications

**OpenPGP.** Three keys and no more: sign, decrypt, authenticate. `gpg`, `git` and SSH through `gpg-agent` use it directly, without a PKCS#11 module, which a key in a PIV slot needs. Each key has its own touch setting. A key made on the card can be vouched for, as in PIV. Kinds: RSA up to 4096, P-256, P-384, P-521, Ed25519 (to sign and authenticate) and X25519 (to decrypt). The driver does not serve this applet.

**FIDO2.** Passkeys for web sign-in. They are outside the key port.

## Sources

Read 2026-11-10.

- Yubico, [YubiKey 5 applications](https://docs.yubico.com/hardware/yubikey/yk-tech-manual/yk5-apps-index.html): applications are separate, with separate storage for keys.
- Yubico, [YubiKey 5 PIV](https://docs.yubico.com/hardware/yubikey/yk-tech-manual/yk5-apps-piv.html), [YubiKey 5 firmware 5.7](https://docs.yubico.com/hardware/yubikey/yk-tech-manual/yk5-firmware-5.7.html) (key kinds, move and delete: "5.7.x and later" in its summary, "As of 5.7.4" in its command tables; 100 passkeys from 5.7.4), [PIV certificate slots](https://developers.yubico.com/PIV/Introduction/Certificate_slots.html), [PIV admin access](https://developers.yubico.com/PIV/Introduction/Admin_access.html), [PIV PIN, PUK and management key](https://docs.yubico.com/yesdk/users-manual/application-piv/pin-puk-mgmt-key.html), [PIV PIN and touch policies](https://docs.yubico.com/yesdk/users-manual/application-piv/pin-touch-policies.html), [PIV attestation](https://docs.yubico.com/yesdk/users-manual/application-piv/attestation.html) and [keeping track of keys](https://docs.yubico.com/yesdk/users-manual/application-piv/keeping-track.html) (the 5.3 metadata).
- Yubico, [YSA-2024-03](https://www.yubico.com/support/security-advisories/ysa-2024-03/): the ECDSA weakness (CVE-2024-45678).
- Yubico, [YubiKey 5 OpenPGP](https://docs.yubico.com/hardware/yubikey/yk-tech-manual/yk5-apps-openpgp.html): the three keys and their kinds; [OpenPGP attestation](https://developers.yubico.com/PGP/Attestation.html).
- Yubico, [YubiKey 5 FIDO](https://docs.yubico.com/hardware/yubikey/yk-tech-manual/yk5-apps-fido.html): PIN, tries and reset. The page still says "up to 25 resident keys" (older text); the firmware 5.7 page gives 100 from firmware 5.7.4.
- FIDO Alliance, [CTAP 2.2](https://fidoalliance.org/specs/fido-v2.2-ps-20250714/fido-client-to-authenticator-protocol-v2.2-ps-20250714.html): section 6.6 (reset).
