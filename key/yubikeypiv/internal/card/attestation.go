package card

import (
	"crypto/x509"

	"github.com/truvity/atoom/key/pivattest"
)

// The reading of an attestation is the library key/pivattest: the vendor's certificates it pins (Yubico's attestation root and the PIV
// attestation intermediates that chain to it; docs/pins.md of that module says where they come from and how a person checks them), the chain of
// a slot certificate through the card's attestation certificate, and the extensions. This file is what the driver keeps of its answer. It has
// no cgo and no build tag, so that it is built and tested on every platform.

// verifyPinned is the code of pivCard.VerifyAttestation: the check over the vendor's pinned certificates. The verifier is the library's, built on
// the first call that succeeds; a call that fails (an embedded file that is not the pinned one, or a clock outside the validity of the pinned
// certificates) is built again on the next call, where the driver's own constructor kept a failed build until the program ended.
func verifyPinned(attCert, slotCert *x509.Certificate) (Attestation, error) {
	v, err := pivattest.Default()
	if err != nil {
		return Attestation{}, err
	}
	return verifyWith(v, attCert, slotCert)
}

// NewAttestationVerifier is the code of pivCard.VerifyAttestation over a root and a bundle of the caller's, for the tests of other packages: they
// bring a test PKI, and so run the real reading of an attestation (the library's verifier and fromEvidence) without a card.
func NewAttestationVerifier(rootPEM, bundlePEM []byte, wantRootSHA256 string) (func(attCert, slotCert *x509.Certificate) (Attestation, error), error) {
	v, err := pivattest.NewVerifierFromPEM(rootPEM, bundlePEM, wantRootSHA256)
	if err != nil {
		return nil, err
	}
	return func(attCert, slotCert *x509.Certificate) (Attestation, error) {
		return verifyWith(v, attCert, slotCert)
	}, nil
}

// verifyWith checks that slotCert chains through attCert (the card's attestation certificate) to an anchor of v, and reads what it says. The
// library takes the slot certificate first.
func verifyWith(v *pivattest.Verifier, attCert, slotCert *x509.Certificate) (Attestation, error) {
	e, err := v.Verify(slotCert, attCert)
	if err != nil {
		return Attestation{}, err
	}
	return fromEvidence(e), nil
}

// fromEvidence is what the driver keeps of the library's reading of a slot certificate. It keeps, as they were, the quirks of the reading the
// driver made with piv-go before it used the library:
//
//   - The reported values leave out the form factor, the algorithm, the key, the anchor and Generated. The whole of the evidence is kept in
//     Attestation.Evidence, and the driver holds what the library tells a caller to hold (Generated, the facts stated, the key of the signed
//     bytes) where it uses the answer: this function checks nothing.
//   - A fact whose extension is absent is the zero value in the reported values, with no error: no firmware is the version 0.0.0, no serial is 0,
//     and a name that gives no slot is slot 0. The evidence says which facts were stated (Attestation.MissingFact).
//   - Only the PIN policies "never" and "once" are reported. "Always", the two biometric ones and an absent policy are all the zero PINPolicy,
//     which no plan names, so a caller that compares it with the plan refuses them all (and pinPolicyName says "always" of it).
//   - The touch policies "never", "always" and "cached" are reported; an absent policy is the zero TouchPolicy.
func fromEvidence(e pivattest.Evidence) Attestation {
	out := Attestation{
		Version: Version{e.Firmware.Major, e.Firmware.Minor, e.Firmware.Patch},
		Serial:  e.Serial,
		Slot:    Slot(e.Slot),

		Evidence: e,
	}
	switch e.PIN {
	case pivattest.PINNever:
		out.PIN = PINNever
	case pivattest.PINOnce:
		out.PIN = PINOnce
	}
	switch e.Touch {
	case pivattest.TouchNever:
		out.Touch = TouchNever
	case pivattest.TouchAlways:
		out.Touch = TouchAlways
	case pivattest.TouchCached:
		out.Touch = TouchCached
	}
	return out
}
