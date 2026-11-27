// Package pivattest checks the attestation of a key made by a YubiKey in its PIV applet: that the slot certificate the card issued for the
// key chains, through the card's attestation certificate (the certificate of slot f9), to a vendor certificate this package pins, and what
// the slot certificate states in the vendor's extensions (firmware, serial number, PIN and touch policy, form factor) and in its common
// name (the slot). It uses the standard library only and has no cgo, so it builds and runs on every system, with no card.
//
// # What a caller must check
//
// A nil error from Verify proves that the slot certificate chains to an anchor of the verifier, with the signer given as the only
// intermediate the chain may use. It does not prove that the signer was used, that a card made the key, or that the key is the one the
// caller holds. The package compares nothing between a certificate and a card or a plan. The caller checks, in this order:
//
//  1. THE KEY IN USE. Evidence.AttestsKey(pub) with the public key the caller actually uses (read back from the slot, or the one it signs
//     with). A true attestation of one key says nothing of another key later put into the same slot. Without this check, nothing else here
//     means anything.
//  2. The slot: Evidence.HasSlot is true and Evidence.Slot is the slot the caller expects. Only a name of the form "YubiKey PIV Attestation "
//     followed by a hexadecimal number of up to 32 bits (any case, leading zeros allowed, no sign and no prefix) whose value is 9a, 9c, 9d,
//     9e or 82 to 95 gives a slot.
//  3. The serial number: HasSerial is true and Serial is the card's own (32 bits: a longer serial in the certificate is cut).
//  4. The firmware: HasFirmware is true and the version is at or above the caller's floor.
//  5. The PIN and touch policy: HasPolicy is true and PIN and Touch are what the caller needs.
//  6. Evidence.Generated is true: the chain is the slot certificate, the signer given and an admitted PIV attestation intermediate of the
//     vendor, and nothing else (a certificate that is itself an anchor, the card's own f9 certificate given as the slot certificate, a
//     signer that is not in the chain and a chain that ends in the root all leave it false, though their chain verifies).
//  7. That the signer is this card's own f9 certificate, as the caller read it from the card. The package does not know which card the
//     caller is talking to.
//  8. That an attestation shows where a key was made, not that it is still in the slot, or that nobody else holds a copy of a key that was
//     later exported or replaced.
//
// A fact is read only after its Has flag is true. An absent extension is not an error: its value is zero and its flag false, so a test such
// as "PIN is not never" would pass a certificate with no policy. A missing or unknown fact is a refusal, and so is a nil PublicKey (a kind of
// key the standard library does not read).
//
// # The checks of Verify
//
// In order: the arguments (a nil certificate is ErrNil); both certificates read again from their Raw bytes (a certificate with no Raw is
// ErrNoRaw, bytes that do not parse ErrParse), and everything below is taken from the values read again, never from a field of the
// caller's certificate; the chain (ErrChain); the extensions in the order the certificate lists them, the first malformed one being the
// error (ErrFirmwareExt, ErrSerialExt, ErrPolicyExt, ErrFormFactorExt); the slot. The chain is checked with the standard library, with the
// trust anchors as roots, the signer as the only intermediate, any extended key usage, and, for a signer that states no Basic Constraints (a
// card of an older generation), the constraint "certificate authority" set on the value read again. Verify checks at the current time;
// VerifyAt at the time of the caller, which must not be the zero time (ErrZeroTime): no entry point reads zero as "now".
//
// # Trust
//
// Default is the verifier over the vendor's pinned certificates: "Yubico Attestation Root 1", two vendor intermediates and three
// "Yubico PIV Attestation" intermediates, six in all, each named with the SHA-256 of its DER form in the table (Pins). The anchors of the
// chain are the root and the PIV attestation intermediates that chain to it; FIDO, OpenPGP, security domain and HSM intermediates of the
// vendor's bundle are not anchors. Default fails when an embedded file is not the pinned one, so a change to what is trusted is a change
// to the table, to docs/pins.md and to docs/provenance.md in one reviewed change. The pinned intermediates are checked against the root at
// the machine's clock when the verifier is built (a failed build is not kept, so a machine whose clock is set later gets a verifier then);
// VerifyAt then checks the slot certificate, the signer and the anchor at the time it is given. NewVerifierFromPEM builds the same kind of
// verifier over a root and a bundle of the caller's, for a test PKI (see the package attesttest); its digest is the only pin and must come
// from outside the PEM.
//
// The vendor's older PIV root ("Yubico PIV Root CA Serial 263751", SHA-256
// 63ece914e54dd87915f34033c85af4c0696ba1512f8add66ced738331207b546) is not pinned. By the vendor's attestation page as read on 2026-11-27,
// every firmware before 5.7.4 is expected to chain to that older root, so a card with such firmware is refused at the chain by this
// library; in the card driver, whose firmware floor is 5.7.0, that concerns 5.7.0 to 5.7.3, as it does with the driver's own check today.
// That was not measured on a card, and it is on the list of things to measure.
//
// # Equal to the check it replaces
//
// What the library accepts is what the PIV attestation check of the library the card driver used before accepts (the same options, the same
// strictness, the same quirks: a serial number longer than 32 bits is cut, bytes after the integer of the serial are ignored, a negative one
// is refused; the class of every error is the same, a chain error keeps the text of its prefix, and the texts of the extension errors are
// this package's own words); a corpus of certificates with the results of that check, in the package attesttest, is replayed by the tests
// of every platform. Four differences are deliberate, and each has a test: the caller's certificate is not changed; a nil argument is an
// error and not a panic; VerifyAt takes the time; and the facts are read from the signed bytes, so a change to a field of a parsed
// certificate has no effect and a certificate built by hand with no Raw is refused. Evidence.Generated and AttestsKey are new and have no
// counterpart in the old check.
package pivattest
