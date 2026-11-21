# Card libraries and tooling (2026-11-01)

Which libraries and tools can talk to a YubiKey 5, over PIV and over the OpenPGP applet, and how well they do on firmware 5.8.

## Question

Which code can drive a card for a key that is born on it, and for an OpenPGP key that has to be built around a card or a KMS key? Candidates: piv-go for PIV; go-openpgp-card, `gpg` with `gpg-agent`, and a Rust helper for the OpenPGP applet; ProtonMail/go-crypto and rPGP for the OpenPGP packets.

## When

2026-10-31 (two YubiKey 5 test cards, firmware 5.8.0, OpenPGP applet 3.4) and 2026-11-01 (a YubiKey 5, firmware 5.8.0, and two AWS KMS `ECC_NIST_P384` keys).

## Verdict

**piv-go drives PIV on firmware 5.8, given the maker's current attestation roots; go-openpgp-card needs three fixes on it.** A P-384 key in a PIV slot, and one in KMS, works as an OpenPGP version 4 primary and subkey for every verifier tested, when the OpenPGP packets are built around it. piv-go's built-in attestation roots fail on firmware 5.8, and the two applets of a card share one PC/SC lock.

## Method

- **Keys in PIV.** piv-go v2.6.0 generated P-384 keys in slot 9c and in retired slots. A prototype built the OpenPGP packets with ProtonMail/go-crypto v1.5.2 and had the card sign the digests: a direct-key signature (`0x1F`), a user ID certification (`0x13`), a subkey binding (`0x18`) with the subkey's back-signature (`0x19`), and revocations (`0x20`, `0x28`). The certificates were checked with several OpenPGP verifiers.
- **KMS keys.** The same packets with an `ECC_NIST_P384` key as primary and as subkey, through the AWS SDK.
- **The OpenPGP applet.** cunicu's go-openpgp-card (latest release v0.3.11, on Codeberg) generated a P-384 signing key on the applet and made its back-signature; a software primary made the binding.
- **The other ways to reach a card** (2026-10-31): a Go program driving `gpg` 2.2.27 and `gpg-agent`, and a Rust helper on openpgp-card 0.7.0, openpgp-card-rpgp 0.8.1 and rPGP 0.20.0.

## What the setup over- and under-counts

- Test cards on one host, with no other card software running except where the PC/SC lock was tested: real workstations run `scdaemon`, which makes the lock matter more, not less.
- Timings include one PIN verification per process; many signatures in one session are faster per signature.
- The touch policy was not exercised: every test ran without a person at the card, so the behaviour of touch always across a long session is not measured here. (PIN-always, touch-always was measured later: [yubikey-piv-pin-touch-and-reset-2026-11-13.md](yubikey-piv-pin-touch-and-reset-2026-11-13.md).)

## Numbers

| What | Result |
|---|---|
| One PIV card signature (P-384) | About 150 ms; a certificate with three signatures about 450 ms. About 7 signatures a second. |
| One KMS signature | About 15 ms; 200 further signatures verified with `gpgv` and `sqv`. |
| OpenPGP applet signing, including the PIN check | About 4 signatures a second. |
| Missed touch | Comes back after about 15 s (14.6 s measured) as a generic security status, not as a touch timeout. |
| Certificate size, bare primary, user ID and subkey | 766 bytes; 1,459 bytes when `gpg` generates the card key, because it adds encryption and authentication subkeys. |
| Spike code | Go with `gpg-agent`: 595 lines. Rust helper: 340 lines. |

Findings that are not numbers:

- **piv-go's built-in attestation roots fail on firmware 5.8.** The token's certificate is issued by an intermediate that piv-go v2.6.0 does not embed, and `piv.Verify` answers "certificate signed by unknown authority". Verification passes with the maker's current root and the full intermediate bundle given to `piv.Verifier`.
- **The two applets are independent.** Generating keys in six PIV slots, signing, and one wrong PIV PIN changed nothing in the OpenPGP applet: its data, PIN counters and signature counter were identical before and after, on two cards.
- **The PC/SC connection is one lock.** piv-go opens the card exclusively. While `scdaemon` holds it, piv-go cannot open it, and the reverse; both fail at once and nothing is corrupted. `gpgconf --kill scdaemon` frees it.
- **go-openpgp-card on firmware 5.8.** Reading the supported algorithms fails without a three-line patch. The fingerprint it stores for an ECDSA key is wrong, because it appends ECDH key-derivation parameters; writing the correct one with PUT DATA fixes it. It has no attestation: the YubiKey command `80 FB 01 00` works after the user PIN is verified, and the certificate chains to the maker's current root through the intermediate "Yubico OPGP Attestation B2 1", with extensions under `1.3.6.1.4.1.41482.5`. The user PIN does not survive reopening the card, and `Close()` resets it (`SCARD_RESET_CARD`). The last functional commit was in December 2024, by one maintainer.
- **go-crypto.** It writes every packet needed, and its ECDSA signing path takes any `crypto.Signer`, but `NewSignerPrivateKey` panics on a signer that is not a concrete key type; a bypass of about 40 lines works. It writes version 4 signatures with the Issuer Fingerprint subpacket only, which `gpg` 2.2.27 rejects on import.
- **KMS and OpenPGP.** A P-384 KMS key signs only 48-byte SHA-384 digests. Its creation date is not the OpenPGP creation time, which fixes the fingerprint and must be stored.
- **`gpg` as the card layer.** `gpg` 2.2.27 and 2.4.7 bind an existing P-384 key only as an encryption subkey, and `--quick-add-key` refuses a keygrip, so a program has to build the binding itself. `gpg` 2.2 sent two wrong PINs to a test card, because it does not say which PIN it asks for.

## Roads not taken

- **Own PC/SC code for the OpenPGP applet.** The most used Go PC/SC binding was last pushed on 2024-12-14, and the PIV route needs no applet code.
- **Go with `gpg-agent` as the card signer.** It worked, but depends on `gpg` at run time and on a PIN dialogue the program does not control.
- **A Rust helper.** It did every step, but adds a second language for a memory-safety argument that is gone when keys are born on the card.

## Changes made

Test cards only. On 2026-10-31 both cards were checked to be in factory state before the run and reset to it afterwards, and the software stand-in key was deleted. The two KMS keys made on 2026-11-01 were throw-away keys.

## What it means for key/yubikeypiv

The driver is Go on piv-go, over PIV only. [s4-piv-go-or-pkcs11.md](s4-piv-go-or-pkcs11.md) gives the reasons: piv-go connects exclusively and queues callers, where the PKCS#11 route resets the card when a second client appears. The findings above are the ones the driver depends on:

- **The attestation roots are the driver's own.** It embeds Yubico's root and the full intermediate bundle and passes them to piv-go's verifier, because piv-go's own set fails on cards made since August 2025 (`internal/card/yubico.go`, measured again on a board on 2026-11-04).
- **The PC/SC lock is the reason for exclusive access.** A `scdaemon` or a second driver on the card makes the driver fail closed ([restart-and-session-reset.md](../restart-and-session-reset.md)).
- **The OpenPGP applet is not used.** The plan has a release-signing key in a PIV slot ([card-slot-map.md](../card-slot-map.md)); the driver only signs digests and has no OpenPGP code, so whoever builds OpenPGP packets around that key does it outside the driver. go-openpgp-card, `gpg` and the Rust helper are not dependencies.
- **A card signature takes about 150 ms** ([yubikey-piv-speed-and-sharing-2026-11-13.md](yubikey-piv-speed-and-sharing-2026-11-13.md) has the driver's own figures).
