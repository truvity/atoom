// Package pivattest checks the attestation of a key made by a YubiKey in its PIV applet: that the slot certificate the card issued for the
// key chains, through the card's attestation certificate (the certificate of slot f9), to a vendor certificate this package pins, and what
// the slot certificate states in the vendor's extensions (firmware, serial number, PIN and touch policy, form factor) and in its common
// name (the slot). It uses the standard library only and has no cgo, so it builds and runs on every system, with no card.
//
// # What it checks, and what it leaves to the caller
//
// Verifier.Verify takes the slot certificate and the card's attestation certificate, checks the chain at the current time (VerifyAt: at a
// time of the caller's) and reads the extensions. Evidence says what was read. The package compares nothing between a certificate and a
// card or a plan: that the serial number is the card's, that the firmware is recent enough, that the slot and the policies are the ones
// a plan wants, that the key is of a kind the caller serves, and what a policy means for a caller are the caller's checks. An extension that
// is absent is not an error: its Has flag is false and its fields are zero, so a caller that needs it says so.
//
// The checks are, in order: the arguments (a nil certificate is ErrNil); the chain (ErrChain); the extensions in the order the certificate
// lists them, the first malformed one being the error (ErrFirmwareExt, ErrSerialExt, ErrPolicyExt, ErrFormFactorExt); the slot. The chain
// is checked with the standard library, with the trust anchors as roots, the signer as the only intermediate, any extended key usage, and,
// for a signer that states no Basic Constraints (a card of an older generation), the constraint "certificate authority" set on a copy: the
// caller's certificate is never changed.
//
// # Trust
//
// Default is the verifier over the vendor's pinned certificates: "Yubico Attestation Root 1", two vendor intermediates and three
// "Yubico PIV Attestation" intermediates, six in all, each named with the SHA-256 of its DER form in the table (Pins). The anchors of the
// chain are the root and the PIV attestation intermediates that chain to it; FIDO, OpenPGP, security domain and HSM intermediates of the
// vendor's bundle are not anchors. Default fails when an embedded file is not the pinned one, so a change to what is trusted is a change
// to the table, to docs/pins.md and to docs/provenance.md in one reviewed change. NewVerifierFromPEM builds the same kind of verifier over a
// root and a bundle of the caller's (a test PKI: see the package attesttest).
//
// The vendor's older PIV root ("Yubico PIV Root CA Serial 263751") is not pinned. By the vendor's attestation page as read on 2026-11-27,
// cards with firmware 5.7.0 to 5.7.3 chain to that older root, so this library refuses their attestations, as the card driver does today.
// That was not measured on a card, and it is on the list of things to measure.
//
// # Equal to the check it replaces
//
// The behaviour of the chain and of the extensions is the behaviour of the PIV attestation check of the library the card driver used
// before (the same options, the same strictness, the same quirks: a serial number longer than 32 bits is cut, bytes after the integer of the
// serial are ignored, a negative one is refused; the class of every error is the same, a chain error keeps the text of its prefix, and the texts of the extension errors are this package's own words); a corpus of certificates with the results of that check, in the package attesttest, is
// replayed by the tests of every platform. Three differences are deliberate, and each has a test: the caller's certificate is not changed, a
// nil argument is an error and not a panic, and VerifyAt takes the time.
package pivattest
