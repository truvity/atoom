# key/pivattest

The check of a YubiKey PIV attestation: that the slot certificate a card issued for a key chains, through the card's attestation certificate, to a vendor certificate that is pinned here, and what the slot certificate states in the vendor's extensions (firmware, serial number, PIN and touch policy, form factor) and in its name (the slot). Standard library only, no cgo, no card: it builds and its tests run on every system. It is meant for the card drivers ([record 0026](../../docs/decisions/0026-one-yubikey-driver-over-direct-usb.md)): `key/yubikeypiv` checks its attestations with it. Any program may use it to read an attestation it was sent.

| | |
| --- | --- |
| Go module | `github.com/truvity/atoom/key/pivattest` (the standard library only; no atoom module) |
| Packages | `pivattest` (this page) and `pivattest/attesttest` (test support, below) |
| API | `Default`, `NewVerifierFromPEM` give a `Verifier`; `Verify(slot, signer)`, `VerifyAt(now, slot, signer)`, `VerifyDER(slotDER, signerDER)` give an `Evidence` (`Slot`, `Firmware`, `Serial`, `FormFactor`, `PIN`, `Touch`, the flags `HasSlot`, `HasFirmware`, `HasSerial`, `HasFormFactor`, `HasPolicy`, `Algorithm`, `PublicKey`, `Generated`, `Anchor`, and the method `AttestsKey`); `Pins` is the pin table; `MaxCertificateSize` is the longest certificate a check reads. The errors `ErrNil`, `ErrNoRaw`, `ErrZeroTime`, `ErrParse`, `ErrTooLarge`, `ErrChain`, `ErrFirmwareExt`, `ErrSerialExt`, `ErrPolicyExt`, `ErrFormFactorExt`, `ErrRootCount`, `ErrRootPin`, `ErrNoAnchor`, `ErrPinTable`, `ErrAdmission` |
| Documentation | the package comment (`go doc`), [docs/pins.md](docs/pins.md) (the six pinned certificates and how to check them) |
| Where it came from | [provenance](../../docs/provenance.md#key-pivattest) |

## What it does

- **The chain.** `Verify` takes the slot certificate and the card's attestation certificate (slot f9) and checks, with the standard library, at the current time, that the slot certificate chains to an anchor with the signer as the only intermediate the chain may use: any extended key usage is accepted, and a signer that states no Basic Constraints (a card of an older generation) is treated as a CA. Both certificates are read again from their `Raw` bytes and everything (chain, extensions, name, key) comes from those values, so the caller's certificates are not changed, a changed field has no effect and a certificate with no `Raw` is refused (`ErrNoRaw`). A certificate longer than `MaxCertificateSize` (16 KiB; the vendor's are about 1 KiB) is refused before it is parsed (`ErrTooLarge`); the limit is on the one path every entry shares, and not on the files a verifier is built from. A nil error proves the chain and no more: not that the signer was used, not that a card made the key. The text of a chain error begins "error verifying attestation certificate: ". `VerifyAt` takes the time of the check and refuses the zero time (`ErrZeroTime`).
- **The anchors.** `Default` trusts "Yubico Attestation Root 1" and the three "Yubico PIV Attestation" intermediates that chain to it, from the two vendor files in `certs/`, byte for byte. Six certificates are pinned by name and SHA-256; `Default` fails when a file is not the pinned one, or when the machine's clock is outside the validity of the pinned certificates (a failed build is not kept; a success is). The pinned intermediates are checked against the root at the machine's clock when the verifier is built; `VerifyAt` then checks the slot certificate, the signer and the anchor at the time it is given. A new root or intermediate is one reviewed change ([docs/pins.md](docs/pins.md)). `NewVerifierFromPEM` is for a test PKI: its digest is the only pin and must come from outside the PEM.
- **The extensions.** The firmware version is exactly three bytes. The serial number is an ASN.1 integer: bytes after it are ignored, a negative one is refused, and one longer than 32 bits is cut to its low 32 bits. The policy is exactly two bytes, PIN 1 to 5 and touch 1 to 3. The form factor is exactly one byte. An extension that is absent is not an error: its flag is false and its value zero. An extension the package does not know is ignored. The first malformed extension, in the order of the certificate, is the error. The slot comes from the common name "YubiKey PIV Attestation <hex>" (any case, leading zeros, up to 32 bits, no sign, no prefix), and only 9a, 9c, 9d, 9e and 82 to 95 give one (otherwise `Slot` is 0 and `HasSlot` false).
- **`Generated`.** True only when the verified chain is exactly three certificates: the slot certificate, the signer given (byte for byte) and one of the PIV attestation intermediates this verifier admitted, and a slot was read from the name. It is false for a slot certificate that is itself an anchor, for the card's own f9 certificate given as the slot certificate, for a signer that is not in the chain and for a chain that ends in the root, though all of those verify.
- **`AttestsKey`.** `Evidence.AttestsKey(pub)` is true only when `Generated` is true and `pub` equals the attested key by the key type's `Equal` (the forms the standard library's parser gives: `*ecdsa.PublicKey`, `*rsa.PublicKey`, `ed25519.PublicKey`, `*ecdh.PublicKey` for X25519).
- **The older vendor root.** By the vendor's attestation page as read on 2026-11-27, every firmware before 5.7.4 is expected to chain to the vendor's older PIV root, which is not pinned here, so such a card is refused at the chain; in the card driver, whose floor is 5.7.0, that concerns 5.7.0 to 5.7.3, as it does with the driver's own check today. This was not measured on a card, and it is on the list of things to measure.

## What the caller must check

The package compares nothing between the evidence and a card or a plan. The caller checks, in this order:

1. **The key in use**: `AttestsKey` with the public key it actually uses. An attestation of one key says nothing of another key put into the slot later.
2. The slot: `HasSlot` and the slot expected.
3. The serial number against the card's own (32 bits).
4. The firmware against the caller's floor.
5. The PIN and touch policy.
6. `Generated` is true.
7. That the signer is this card's own f9 certificate, as read from the card.
8. That an attestation shows where a key was made, not that it is still in the slot or that no copy exists.

A fact is read only after its `Has` flag is true; a missing or unknown fact is a refusal, and so is a nil `PublicKey`. ("PIN is not never" passes a certificate with no policy.)

## Equal to the check the card driver made before

The library gives the answer of the check that the card driver made with piv-go before it moved onto this library (the same chain options, the same strictness and quirks), so that the move changed no answer. The proof is the corpus: `attesttest/corpus/corpus.json` holds more than 200 cases (good chains of every key kind, every policy pair, every slot name, wrong chains, extension edges, tampering, and a PIV intermediate or the root given as the slot certificate) and the answer of piv-go's check for each; the tests of this module replay it on every system. The differential test of the card driver (`key/yubikeypiv/internal/card/pivattest_diff_test.go`, build tag `atoom_card`) runs piv-go's check live beside the library on every case and on fresh ones, and rewrites the answers from piv-go's check when asked. The texts of the extension errors are worded here (their class is the same); a chain error keeps its text. Six differences are deliberate, each with a test: the caller's certificate is not changed; a nil argument is an error and not a panic; `VerifyAt` takes the time (and refuses the zero time); both certificates are read again from their raw bytes, so a changed field has no effect and a certificate with no raw bytes is refused (`ErrNoRaw`); `Default` builds again after a failed build, where the driver's own constructor kept a failed build for the life of the program; and a certificate longer than `MaxCertificateSize` is refused (`ErrTooLarge`), where piv-go has no limit. What is accepted is unchanged: a certificate that is itself a trust anchor still passes the chain check, with `Generated` false. `Generated` and `AttestsKey` are new.

## attesttest

Test support, never imported by a program. `NewPKI` makes a TEST ONLY root, a PIV-named intermediate, a card attestation certificate and slot certificates with the vendor's extensions, with options for each way a chain or an extension can be made wrong (`PKI.Slot`, `PKI.WithF9`, the extension builders `Firmware`, `Serial`, `Policy`, `FormFactor`, `Ext`). `Load` reads the corpus; `Build` makes a new one (its keys are new, so its results must be recorded again from piv-go's check). No certificate in it is a vendor's.

## Tests

```text
go test ./...                       the unit tests, the pin tests, the corpus replay, the fuzz seeds
go test -run '^$' -fuzz FuzzExtensions ./
```

The differential test needs the card toolchain (`just go-card`); nothing here opens a card or a reader.
