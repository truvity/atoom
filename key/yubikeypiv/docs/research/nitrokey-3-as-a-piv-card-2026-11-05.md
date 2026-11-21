# A Nitrokey 3 (firmware v1.9.1) has no PIV attestation and no per-key policy (2026-11-05)

## Question

Can a Nitrokey 3 take the place of a YubiKey 5 as a PIV card, so that a program written for a YubiKey (here, the Go library piv-go) works with it? PIV is the smart-card standard NIST SP 800-73: a card with numbered key slots, a PIN, and certificates.

## When

2026-11-05. The device was only read from; nothing on it was changed.

## Verdict

**Not without changes to the program, and not as a card that can prove where its keys were born.** Three differences from a YubiKey 5 matter for a program that adopts only keys it can verify:

- **No attestation.** The card answers the attestation command with status `6A81` (function not supported). Attestation is a certificate that a card signs to say "this key was made inside me". Without it, a tool cannot check that a key never existed outside the card.
- **No per-key PIN and touch policy.** The vendor's source does not read the policy tags. A key that needs no PIN, or a key that needs a touch for every use, cannot be asked for.
- **A fixed serial number and firmware version.** The source returns the same made-up values on every card.

A fourth difference is smaller: piv-go lists the card but cannot open it, because it selects the PIV applet with a shorter name than the card accepts.

This agrees with a reading of the vendor's source on 2026-11-01 ([which-keys-to-buy.md](../which-keys-to-buy.md)), which found the same three facts. This note adds the answers of one real card.

## Method

One Nitrokey 3, LPC55 variant (USB ID `20a0:42b2`), firmware v1.9.1. The [release notes of v1.9.0](https://github.com/Nitrokey/nitrokey-3-firmware/releases/tag/v1.9.0) name the PIV applet `piv-authenticator` 0.7.0. The notes of [v1.9.1](https://github.com/Nitrokey/nitrokey-3-firmware/releases/tag/v1.9.1) fix one USB descriptor and do not list the applet.

A small probe program drove the card in two ways: through piv-go, and with raw smart-card commands (APDUs) over PC/SC. It only read. The probe asked for the applets present, the algorithm list, the PIN retry counter, and the attestation command and object.

The test did not record the version of piv-go, the version of OpenSC or the operating system. The vendor's source was read afterwards, for the points the card was not asked: `piv-authenticator` 0.7.0 from [docs.rs](https://docs.rs/crate/piv-authenticator/0.7.0/source/src/lib.rs) and piv-go v2.6.0 ([source](https://github.com/go-piv/piv-go/blob/v2.6.0/v2/piv/piv.go)).

## What the setup over- and under-counts

- One device and one firmware. Another unit, or a later firmware, may answer differently.
- Every probe command only read. Not one key was generated and no signature was made.
- Source reading shows what the code does. It does not show what a card does, and a card may run a different build.

## Numbers

Every row says whether it was **measured** (the card answered the probe) or **read** (taken from the vendor's documentation or source, not asked of the card). A row marked **listed by the probe** was reported by the probe program; the command behind it was not recorded. The YubiKey column is the vendor's documentation, not a measurement of this test.

| Need | YubiKey 5 (documented) | Nitrokey 3 | Measured or read |
|---|---|---|---|
| Applets present | not compared | The card lists five applications: PIV, OpenPGP card 3.4, OATH, FIDO2 and an administration application. It has no SmartCard-HSM or PKCS#15 application. | Listed by the probe |
| ECDSA and ECDH with P-384 | yes | **in the algorithm list** | Measured: list read from the card. Signing was not tested. |
| Slots 9a, 9c, 9d and retired slots 82 to 95 | yes | yes | Read: [vendor PIV page](https://docs.nitrokey.com/nitrokeys/features/piv/key_management.html), [`container.rs`](https://docs.rs/crate/piv-authenticator/0.7.0/source/src/container.rs) |
| **Attestation** command (instruction `F9`) | yes ([Yubico](https://developers.yubico.com/PIV/Introduction/PIV_attestation.html)) | **status `6A81`** | Measured. Also read: the source returns "function not supported" for it (`lib.rs`). |
| Attestation certificate object | yes | **not recognised** | Measured |
| **Per-key PIN and touch policy** | yes ([Yubico](https://developers.yubico.com/PIV/Introduction/Yubico_extensions.html)) | **no** | Read only: the key-generation code reads the algorithm tag; the policy tags `AA` and `AB` appear only in a comment, and the touch policy of the management key is a `TODO` (`lib.rs`) |
| Serial number and firmware version | real, per card | **fixed, the same on every card** | Read: `lib.rs` says "make up a 4-byte serial" and "make up a version", and returns `6.6.6`. The same on every card is not measured: only one card was probed. |
| Way to tell two cards apart | serial number | the GUID of the card holder object (CHUID) | Read: `state.rs` makes a random 16-byte GUID when the card is first set up |
| PIN retry counter, PUK | yes | yes, 3 tries | Measured: read from the card. A wrong PIN was not tried. |
| piv-go **lists** the card | yes | yes | Measured |
| piv-go **opens** the card | yes | **no** | Measured: it selects the applet with the 5-byte name `A000000308`, and the card answers `6A82` (file not found). The full name works. Read: the source declares the card's name as one that may be cut to 9 bytes (`constants.rs`); piv-go sends 5 (`piv.go`). The version of piv-go in the test is not recorded; the source read is v2.6.0. |
| piv-go management-key path | yes | probably broken | **Inferred**, not run: the card's "get metadata" command is a `TODO` in the source and answers with empty data for most references |
| OpenSC, PKCS#11 | not compared | the card is seen as a generic PIV card | Listed by the probe: OpenSC's own PIV emulation, with no card-specific driver |

**The OpenPGP applet does not change the result.** It is version 3.4. The OpenPGP card specification has [three key slots](https://gnupg.org/ftp/specs/OpenPGP-smart-card-application-3.4.pdf) (sign, decrypt, authenticate). A plan with more keys does not fit. The reading of 2026-11-01 found no attestation in that applet either; this test did not record whether the OpenPGP applet was asked about attestation.

The vendor's documents do not agree with each other on the algorithms. The [PIV page](https://docs.nitrokey.com/nitrokeys/features/piv/key_management.html) lists RSA 2048 and nistp256 (and RSA 3072 and 4096 with non-standard identifiers). The [release notes of v1.8.2](https://github.com/Nitrokey/nitrokey-3-firmware/releases/tag/v1.8.2) say that NIST P-384 was added. The card's own list, read in this test, has P-384.

## How to read it

The attestation gap decides the question for any key whose origin must be proven. The attestation result here is the PIV half of that statement for firmware v1.9.1. The PIN and touch policy gap decides it for any key that must be unlocked without a PIN or locked per use.

The short-name problem and the fixed GUID are workarounds, not obstacles: a patch to select the applet with the full name, and the GUID in place of the serial.

A fixed serial has one more effect. A tool that finds a card by its serial number finds none, or finds every Nitrokey 3 under the same number.

## What this does not show

- **No key was generated and no signature was made.** The P-384 row is a list, not a working signature. ECDH on the card (slot 9d) was not run.
- **Attestation was not asked again after a key was generated**, because no key was generated. The source says it is not supported at all, so a change is not expected, but it was not measured.
- **The PIN retry counter was read, not used.** The count after a wrong PIN was not watched.
- **A key that needs no PIN** was not tried. The result is read from the source.
- **The serial and firmware version were not compared across cards.** "The same on every card" comes from the source.
- **The OpenPGP applet was not tested** for signing, touch or attestation.
- **No test of PC/SC sharing** with `scdaemon`, of a factory reset, or of the touch button.
- **One device, one firmware.** Later firmware, or a different Nitrokey 3 variant, may differ. A tool that supports this card should name the firmware it was measured on, and repeat the attestation check on a later firmware.
- **The tool versions of the probe are not recorded** (piv-go, OpenSC, operating system), and it is not recorded how often each probe command was sent.

A write test on a card in factory state would turn the inferred and read rows into measured ones: generate and sign with P-384 in slot 9a, run ECDH in 9d, ask for attestation after a key exists, ask for a key with a "PIN never" policy, and spend one wrong PIN. It was not run.

## Roads not taken

- **The OpenPGP applet for the keys.** Rejected for a plan with many keys: three slots, and no attestation reported.
- **A write test.** Not run, because the verdict above does not depend on it.
- **A different class of device** (a hardware security module with PKCS#11) was not examined.

## What it means for key/yubikeypiv

The driver serves a YubiKey 5 and nothing else. The rows above are the reasons a Nitrokey 3 is not served, and each is a check in the driver:

- **It adopts a key only with its attestation.** The driver asks the card for the attestation of every key in the plan and verifies it against Yubico's roots; a card that answers `6A81` gives it nothing to verify, and the key is refused. This is the decision of [s4-piv-go-or-pkcs11.md](s4-piv-go-or-pkcs11.md) and the README: a key is trusted because the card proves it was born there.
- **It compares the PIN and touch policy of each key with the plan.** The policies come from the attestation. A card with no per-key policy cannot show them.
- **It binds to a card by its serial** (`--serial`, [the README](../../README.md#which-card-it-opens)). A serial that is the same on every card binds to none.
- **piv-go cannot open the card** with its short applet name, so even a card the driver was willing to serve would need a patch.

The write test described above was not run. PKCS#11 stays the route for a later driver of a non-YubiKey device ([s4-piv-go-or-pkcs11.md](s4-piv-go-or-pkcs11.md)).

Open: P-384 signatures, ECDH in slot 9d, the touch policy, PC/SC next to `scdaemon`, a factory reset and the behaviour of a later firmware are unmeasured on a Nitrokey 3.

## Changes made

None. The card was only read from.
