# key/pivattest

The check of a YubiKey PIV attestation: that the slot certificate a card issued for a key chains, through the card's attestation certificate, to a vendor certificate that is pinned here, and what the slot certificate states in the vendor's extensions (firmware, serial number, PIN and touch policy, form factor) and in its name (the slot). Standard library only, no cgo, no card: it builds and its tests run on every system. The card drivers use it ([record 0026](../../docs/decisions/0026-one-yubikey-driver-over-direct-usb.md)), and so may any program that reads an attestation it was sent.

| | |
| --- | --- |
| Go module | `github.com/truvity/atoom/key/pivattest` (the standard library only; no atoom module) |
| Packages | `pivattest` (this page) and `pivattest/attesttest` (test support, below) |
| API | `Default`, `NewVerifierFromPEM` give a `Verifier`; `Verify(slot, signer)`, `VerifyAt(now, slot, signer)`, `VerifyDER(slotDER, signerDER)` give an `Evidence` (`Slot`, `Firmware`, `Serial`, `FormFactor`, `PIN`, `Touch`, the flags `HasSlot`, `HasFirmware`, `HasSerial`, `HasFormFactor`, `HasPolicy`, `Algorithm`, `PublicKey`, `Anchor`, `Generated`); `Pins` is the pin table. The errors `ErrNil`, `ErrParse`, `ErrChain`, `ErrFirmwareExt`, `ErrSerialExt`, `ErrPolicyExt`, `ErrFormFactorExt`, `ErrRootCount`, `ErrRootPin`, `ErrNoAnchor`, `ErrPinTable`, `ErrAdmission` |
| Documentation | the package comment (`go doc`), [docs/pins.md](docs/pins.md) (the six pinned certificates and how to check them) |
| Where it came from | [provenance](../../docs/provenance.md#key-pivattest) |

## What it does

- **The chain.** `Verify` takes the slot certificate and the card's attestation certificate (slot f9) and checks, with the standard library, that the slot certificate chains through it to an anchor: the roots are the anchors, the only intermediate is the attestation certificate, any extended key usage is accepted, and a signer that states no Basic Constraints (a card of an older generation) is treated as a CA on a copy; the caller's certificate is not changed. The text of a chain error begins "error verifying attestation certificate: ".
- **The anchors.** `Default` trusts "Yubico Attestation Root 1" and the three "Yubico PIV Attestation" intermediates that chain to it, from the two vendor files in `certs/`, byte for byte. Six certificates are pinned by name and SHA-256; `Default` fails when a file is not the pinned one. A new root or intermediate is one reviewed change ([docs/pins.md](docs/pins.md)).
- **The extensions.** The firmware version is exactly three bytes. The serial number is an ASN.1 integer: bytes after it are ignored, a negative one is refused, and one longer than 32 bits is cut to its low 32 bits. The policy is exactly two bytes, PIN 1 to 5 and touch 1 to 3. The form factor is exactly one byte. An extension that is absent is not an error: its flag is false and its value zero. An extension the package does not know is ignored. The first malformed extension, in the order of the certificate, is the error. The slot comes from the common name "YubiKey PIV Attestation <hex>", and only 9a, 9c, 9d, 9e and 82 to 95 give one (no slot is 0 and `HasSlot` false).
- **What it leaves to the caller.** That the serial is the card's, that the firmware is recent enough, that the slot and the policies are the plan's, that the key is one the caller serves, and what a policy means for the caller. The package compares nothing between the evidence and a card.
- **The older vendor root.** By the vendor's attestation page as read on 2026-11-27, cards with firmware 5.7.0 to 5.7.3 chain to the vendor's older PIV root, which is not pinned here, so their attestations are refused by this library, as they are by the card driver today. This was not measured on a card, and it is on the list of things to measure.

## Equal to the check the card driver used

The library gives the answer of the check that the card driver made with piv-go before it (the same chain options, the same strictness and quirks), so that moving the driver onto it changes no answer. The proof is the corpus: `attesttest/corpus/corpus.json` holds more than 200 cases (good chains of every key kind, every policy pair, every slot name, wrong chains, extension edges, tampering) and the answer of the old check for each; the tests of this module replay it on every system. The differential test of the card driver (`key/yubikeypiv/internal/card/pivattest_diff_test.go`, build tag `atoom_card`) runs both checks live on every case and on fresh ones, and rewrites the answers from the old check when asked. The texts of the extension errors are worded here (their class is the same); a chain error keeps its text. Three differences are deliberate, each with a test: the caller's certificate is not changed, a nil argument is an error and not a panic, and `VerifyAt` takes the time.

## attesttest

Test support, never imported by a program. `NewPKI` makes a TEST ONLY root, a PIV-named intermediate, a card attestation certificate and slot certificates with the vendor's extensions, with options for each way a chain or an extension can be made wrong (`PKI.Slot`, `PKI.WithF9`, the extension builders `Firmware`, `Serial`, `Policy`, `FormFactor`, `Ext`). `Load` reads the corpus; `Build` makes a new one (its keys are new, so its results must be recorded again from the old check). No certificate in it is a vendor's.

## Tests

```text
go test ./...                       the unit tests, the pin tests, the corpus replay, the fuzz seeds
go test -run '^$' -fuzz FuzzExtensions ./
```

The differential test needs the card toolchain (`just go-card`); nothing here opens a card or a reader.
