package pivattest

import (
	"crypto"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/x509"
	"encoding/asn1"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// pivIntermediatePrefix is the common-name prefix of the vendor's intermediates that sign the attestation key of a PIV applet.
const pivIntermediatePrefix = "Yubico PIV Attestation "

// slotCNPrefix is the common-name prefix of a slot certificate: the slot is the hexadecimal number after it.
const slotCNPrefix = "YubiKey PIV Attestation "

// The vendor's extensions of a slot certificate, all under 1.3.6.1.4.1.41482.3.
var (
	oidFirmware   = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 41482, 3, 3}
	oidSerial     = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 41482, 3, 7}
	oidPolicy     = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 41482, 3, 8}
	oidFormFactor = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 41482, 3, 9}
)

// Version is the firmware version of a card.
type Version struct{ Major, Minor, Patch int }

// PINPolicy says when a key needs the PIN. The values are those of the vendor's extension; the zero value is "not stated".
type PINPolicy int

// The PIN policies of the extension.
const (
	PINNever PINPolicy = iota + 1
	PINOnce
	PINAlways
	PINMatchOnce // biometric match, once
	PINMatchAlways
)

// TouchPolicy says when a key needs a touch. The zero value is "not stated".
type TouchPolicy int

// The touch policies of the extension.
const (
	TouchNever TouchPolicy = iota + 1
	TouchAlways
	TouchCached
)

// Algorithm is the kind of the attested public key, as far as a card holds it. AlgUnknown covers every other key, an RSA key of another size
// or with another public exponent than 65537 included.
type Algorithm int

// The algorithms a PIV applet of a YubiKey 5 holds.
const (
	AlgUnknown Algorithm = iota
	AlgEC256
	AlgEC384
	AlgRSA2048
	AlgRSA3072
	AlgRSA4096
	AlgEd25519
	AlgX25519
)

// String names the algorithm.
func (a Algorithm) String() string {
	names := [...]string{"unknown", "EC256", "EC384", "RSA2048", "RSA3072", "RSA4096", "Ed25519", "X25519"}
	if a < 0 || int(a) >= len(names) {
		return "unknown"
	}
	return names[a]
}

// Evidence is what a verified slot certificate states. The vendor's extensions are optional for this package: an extension that is absent
// leaves its fields at the zero value and its Has flag false, and the caller decides whether that is acceptable. The package compares
// nothing between this evidence and a card or a plan; that is the caller's.
type Evidence struct {
	// Slot is the key reference read from the common name of the slot certificate (9a, 9c, 9d, 9e or 82 to 95), or 0 when the name does not
	// give one of those (the attestation slot f9 and the slots 80 and 96 included).
	Slot    byte
	HasSlot bool

	Firmware    Version // from the extension 41482.3.3
	HasFirmware bool

	// Serial is the serial number of the card, read as an ASN.1 integer (bytes after it are ignored) and cut to its low 32 bits.
	Serial    uint32
	HasSerial bool

	FormFactor    byte // from the extension 41482.3.9: one byte, 0 when absent
	HasFormFactor bool

	PIN       PINPolicy   // from the extension 41482.3.8
	Touch     TouchPolicy // from the same extension
	HasPolicy bool

	// Algorithm and PublicKey describe the key of the slot certificate (PublicKey is nil for a key of a kind that the standard library does
	// not read). The package does not check that the key is one the card can hold.
	Algorithm Algorithm
	PublicKey crypto.PublicKey

	// Anchor is the common name of the trust anchor the chain ended in.
	Anchor string
	// Generated is true when that anchor is a PIV attestation intermediate of the vendor: the card's attestation key, which only attests
	// keys the card made, was certified by it. It is false for a chain that ends in the root itself (no card does that).
	Generated bool
}

// Verifier checks slot certificates against trust anchors fixed at construction. It holds no state that a call changes, so it is safe for
// concurrent use. The zero Verifier has no anchors and refuses everything.
type Verifier struct {
	anchors *x509.CertPool
}

// Verify proves, against the anchors of v, that slot was certified by signer, the card's attestation certificate (f9), and reads what slot
// states. It does not change slot or signer. The chain is checked at the current time. The order of the checks is: the arguments, the chain,
// then the extensions in the order the certificate lists them (the first malformed one is the error), then the slot.
func (v *Verifier) Verify(slot, signer *x509.Certificate) (Evidence, error) {
	return v.VerifyAt(time.Time{}, slot, signer)
}

// VerifyAt is Verify with the chain checked at now (a zero now is the current time). It serves evidence that is checked with no card at
// hand, and tests.
func (v *Verifier) VerifyAt(now time.Time, slot, signer *x509.Certificate) (Evidence, error) {
	if v == nil || v.anchors == nil {
		return Evidence{}, ErrNoAnchor
	}
	if slot == nil || signer == nil {
		return Evidence{}, ErrNil
	}
	// A card of an older generation signs with an attestation certificate that states no Basic Constraints, which the standard
	// library refuses for a signer. The workaround is made on a copy, so the caller's certificate stays as it was.
	sc := *signer
	if !sc.BasicConstraintsValid {
		sc.BasicConstraintsValid = true
		sc.IsCA = true
	}
	inter := x509.NewCertPool()
	inter.AddCert(&sc)
	chains, err := slot.Verify(x509.VerifyOptions{
		Roots: v.anchors, Intermediates: inter, CurrentTime: now,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	})
	if err != nil {
		return Evidence{}, &classed{class: ErrChain, cause: err, text: "error verifying attestation certificate: " + err.Error()}
	}
	var e Evidence
	for _, ext := range slot.Extensions {
		if err := e.addExtension(ext.Id, ext.Value); err != nil {
			return Evidence{}, err
		}
	}
	e.Slot, e.HasSlot = slotFromName(slot.Subject.CommonName)
	e.PublicKey = slot.PublicKey
	if e.PublicKey == nil {
		// The standard library leaves the key of a certificate empty for an X25519 key, which a card holds; the key itself it reads.
		if k, err := x509.ParsePKIXPublicKey(slot.RawSubjectPublicKeyInfo); err == nil {
			e.PublicKey = k
		}
	}
	e.Algorithm = algorithmOf(e.PublicKey)
	if len(chains) > 0 && len(chains[0]) > 0 {
		e.Anchor = chains[0][len(chains[0])-1].Subject.CommonName
		e.Generated = strings.HasPrefix(e.Anchor, pivIntermediatePrefix)
	}
	return e, nil
}

// VerifyDER parses the two certificates and verifies them as Verify does.
func (v *Verifier) VerifyDER(slotDER, signerDER []byte) (Evidence, error) {
	slot, err := x509.ParseCertificate(slotDER)
	if err != nil {
		return Evidence{}, &classed{class: ErrParse, cause: err, text: "pivattest: the slot certificate does not parse: " + err.Error()}
	}
	signer, err := x509.ParseCertificate(signerDER)
	if err != nil {
		return Evidence{}, &classed{class: ErrParse, cause: err, text: "pivattest: the signer certificate does not parse: " + err.Error()}
	}
	return v.Verify(slot, signer)
}

// addExtension reads one extension of the slot certificate. An extension with another identifier is not read.
func (e *Evidence) addExtension(id asn1.ObjectIdentifier, val []byte) error {
	switch {
	case id.Equal(oidFirmware):
		if len(val) != 3 {
			return &classed{class: ErrFirmwareExt, text: fmt.Sprintf("pivattest: the firmware version extension holds %d bytes, want 3", len(val))}
		}
		e.Firmware, e.HasFirmware = Version{int(val[0]), int(val[1]), int(val[2])}, true
	case id.Equal(oidSerial):
		var n int64
		// Bytes after the integer are ignored: the check this one is equal to ignores them.
		if _, err := asn1.Unmarshal(val, &n); err != nil {
			return &classed{class: ErrSerialExt, cause: err, text: "pivattest: the serial number extension is not an ASN.1 integer of up to 64 bits: " + err.Error()}
		}
		if n < 0 {
			return &classed{class: ErrSerialExt, text: fmt.Sprintf("pivattest: the serial number extension is negative (%d)", n)}
		}
		// n is not negative here; a serial number longer than 32 bits is cut to its low 32 bits on purpose: that is the behaviour of the check
		// this one is equal to, and a caller compares the result with the 32 bits it reads from the card.
		e.Serial, e.HasSerial = uint32(n), true
	case id.Equal(oidPolicy):
		if len(val) != 2 {
			return &classed{class: ErrPolicyExt, text: fmt.Sprintf("pivattest: the policy extension holds %d bytes, want 2", len(val))}
		}
		if val[0] < 1 || val[0] > 5 {
			return &classed{class: ErrPolicyExt, text: fmt.Sprintf("pivattest: the PIN policy byte is 0x%x, want 1 to 5", val[0])}
		}
		e.PIN = PINPolicy(val[0])
		if val[1] < 1 || val[1] > 3 {
			return &classed{class: ErrPolicyExt, text: fmt.Sprintf("pivattest: the touch policy byte is 0x%x, want 1 to 3", val[1])}
		}
		e.Touch, e.HasPolicy = TouchPolicy(val[1]), true
	case id.Equal(oidFormFactor):
		if len(val) != 1 {
			return &classed{class: ErrFormFactorExt, text: fmt.Sprintf("pivattest: the form factor extension holds %d bytes, want 1", len(val))}
		}
		e.FormFactor, e.HasFormFactor = val[0], true
	}
	return nil
}

// slotFromName reads the slot from the common name "YubiKey PIV Attestation <hex>": any hexadecimal number of up to 32 bits (case and zero
// padding are not distinguished), and only 9a, 9c, 9d, 9e and 82 to 95 name a slot.
func slotFromName(cn string) (byte, bool) {
	rest, ok := strings.CutPrefix(cn, slotCNPrefix)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseUint(rest, 16, 32)
	if err != nil {
		return 0, false
	}
	switch {
	case n == 0x9a, n == 0x9c, n == 0x9d, n == 0x9e, n >= 0x82 && n <= 0x95:
		return byte(n), true
	}
	return 0, false
}

// algorithmOf names the kind of a public key.
func algorithmOf(pub crypto.PublicKey) Algorithm {
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		switch k.Curve {
		case elliptic.P256():
			return AlgEC256
		case elliptic.P384():
			return AlgEC384
		}
	case *rsa.PublicKey:
		if k.E != 65537 {
			return AlgUnknown
		}
		switch k.N.BitLen() {
		case 2048:
			return AlgRSA2048
		case 3072:
			return AlgRSA3072
		case 4096:
			return AlgRSA4096
		}
	case ed25519.PublicKey:
		return AlgEd25519
	case *ecdh.PublicKey:
		if k.Curve() == ecdh.X25519() {
			return AlgX25519
		}
	}
	return AlgUnknown
}
