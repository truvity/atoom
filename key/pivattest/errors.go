package pivattest

import "errors"

// The named errors of the package. A caller tells them apart with errors.Is.
var (
	// ErrNil: a certificate argument is nil.
	ErrNil = errors.New("pivattest: a certificate is nil")
	// ErrNoRaw: a certificate has no Raw bytes (it was built by hand, not parsed). Everything is read from the signed bytes.
	ErrNoRaw = errors.New("pivattest: a certificate has no raw bytes")
	// ErrZeroTime: VerifyAt was given the zero time. No entry point reads it as the current time.
	ErrZeroTime = errors.New("pivattest: the time of the check is the zero time")
	// ErrParse: a certificate (a DER string, or a PEM block of a bundle) does not parse.
	ErrParse = errors.New("pivattest: a certificate does not parse")

	// ErrChain: the slot certificate does not chain through the signer to a trust anchor of the verifier (an unknown authority, a
	// time outside a validity period, a signature that does not match, an unhandled critical extension, a refused algorithm, an
	// extension of the Basic Constraints or of the key usage that forbids the signer to sign). The text of the error begins with
	// "error verifying attestation certificate: " and goes on with the reason of the standard library; the error also wraps that reason.
	ErrChain = errors.New("error verifying attestation certificate")

	// ErrFirmwareExt: the firmware version extension (1.3.6.1.4.1.41482.3.3) is present and is not exactly three bytes.
	ErrFirmwareExt = errors.New("pivattest: the firmware version extension is malformed")
	// ErrSerialExt: the serial number extension (1.3.6.1.4.1.41482.3.7) is present and is not an ASN.1 integer that fits 64 bits, or is
	// negative.
	ErrSerialExt = errors.New("pivattest: the serial number extension is malformed")
	// ErrPolicyExt: the policy extension (1.3.6.1.4.1.41482.3.8) is present and is not two bytes, or a byte is outside its range (PIN 1 to
	// 5, touch 1 to 3).
	ErrPolicyExt = errors.New("pivattest: the PIN and touch policy extension is malformed")
	// ErrFormFactorExt: the form factor extension (1.3.6.1.4.1.41482.3.9) is present and is not exactly one byte.
	ErrFormFactorExt = errors.New("pivattest: the form factor extension is malformed")

	// ErrRootCount: the root file does not hold exactly one certificate (or does not parse).
	ErrRootCount = errors.New("pivattest: the root file does not hold exactly one certificate")
	// ErrRootPin: the certificate of the root file is not the one whose SHA-256 the caller named.
	ErrRootPin = errors.New("pivattest: the root is not the pinned certificate")
	// ErrNoAnchor: no certificate of the bundle is a PIV attestation intermediate that chains to the root, or the Verifier has no anchors
	// (the zero Verifier).
	ErrNoAnchor = errors.New("pivattest: the bundle holds no PIV attestation intermediate that chains to the root")

	// ErrPinTable: an embedded vendor certificate is not the one the pin table names (Default only).
	ErrPinTable = errors.New("pivattest: an embedded vendor certificate does not match the pin table")
	// ErrAdmission: the PIV attestation intermediates that the bundle admits are not the ones of the pin table (Default only).
	ErrAdmission = errors.New("pivattest: the admitted intermediates differ from the pin table")
)

// classed is an error with a text of its own that still matches a named error (and, if it has one, the cause) with errors.Is.
type classed struct {
	class, cause error
	text         string
}

func (e *classed) Error() string { return e.text }

func (e *classed) Unwrap() []error {
	if e.cause == nil {
		return []error{e.class}
	}
	return []error{e.class, e.cause}
}
