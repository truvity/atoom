package pivattest

import (
	"bytes"
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

// MaxCertificateSize is the longest certificate, in bytes of its DER form, that a check reads: the slot certificate and the signer given to
// Verify, VerifyAt and VerifyDER. A longer one is refused with ErrTooLarge before it is parsed. The certificates of a card are about 1 KiB; the limit
// is sixteen times that.
const MaxCertificateSize = 16 << 10

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

// Evidence is what a verified slot certificate states. Read it with the rules of the package comment: a fact is read only after its Has flag
// is true, a fact that is missing or unknown is a refusal, and the first thing a caller checks is the key it uses (AttestsKey). The package
// compares nothing between this evidence and a card or a plan; that is the caller's.
type Evidence struct {
	// Slot is the key reference read from the common name of the slot certificate, and HasSlot says that it was read. The names that give a
	// slot are exactly "YubiKey PIV Attestation " followed by a hexadecimal number of up to 32 bits (upper or lower case, leading zeros
	// allowed, no sign and no prefix) whose value is 9a, 9c, 9d, 9e or one of 82 to 95. Any other name, the attestation slot f9 and the
	// slots 80 and 96 included, leaves Slot 0 and HasSlot false.
	Slot    byte
	HasSlot bool

	Firmware    Version // from the extension 41482.3.3; zero when HasFirmware is false
	HasFirmware bool

	// Serial is the serial number of the card, read as an ASN.1 integer (bytes after it are ignored) and cut to its low 32 bits.
	Serial    uint32
	HasSerial bool

	FormFactor    byte // from the extension 41482.3.9: one byte, 0 when absent
	HasFormFactor bool

	PIN       PINPolicy   // from the extension 41482.3.8; 0 when HasPolicy is false, so a test "PIN is not never" would pass a missing policy
	Touch     TouchPolicy // from the same extension
	HasPolicy bool

	// Algorithm describes PublicKey. PublicKey is the key the slot certificate attests; it is nil for a key of a kind that the standard
	// library does not read, and that is a refusal. The package does not check that the key is one the card can hold. THE KEY A CALLER
	// USES MUST BE THE KEY OF THIS EVIDENCE: a true attestation of one key does not speak for another key later put in the same slot.
	// AttestsKey makes that comparison.
	Algorithm Algorithm
	PublicKey crypto.PublicKey

	// Generated is true only when the chain that was verified is exactly three certificates: the slot certificate, the signer that was
	// given (byte for byte) and one of the PIV attestation intermediates that this verifier admitted at construction (the root and the
	// other intermediates are not among them), and a slot was read from the name of the slot certificate (HasSlot). It says that a card
	// whose attestation key was certified by the vendor's PIV intermediate attested this key. It is false for a slot certificate that is
	// itself an anchor, for a chain that ends in the root or in another certificate, and for a signer that is not in the chain.
	Generated bool
	// Anchor is the common name of the anchor of the chain that makes Generated true; when Generated is false it is the common name of the
	// anchor whose certificate has the smallest SHA-256 among the anchors of all the chains found (so it does not depend on the order in
	// which the standard library lists them).
	Anchor string
}

// AttestsKey reports whether this evidence attests pub: true only when Generated is true, PublicKey is not nil, and pub equals PublicKey by
// the Equal method of the key type. It is false for a nil pub, for a pub of a type other than the forms that the standard library's
// certificate parser gives (*ecdsa.PublicKey, *rsa.PublicKey, ed25519.PublicKey and, for an X25519 key, *ecdh.PublicKey) and for two keys of
// different types. A caller that signs or decrypts with a key calls this with the public key it uses (for example the key it read back from
// the slot) before it believes that the key was made on a card.
func (e Evidence) AttestsKey(pub crypto.PublicKey) bool {
	if !e.Generated || !knownKey(e.PublicKey) || !knownKey(pub) {
		return false
	}
	eq, ok := e.PublicKey.(interface{ Equal(crypto.PublicKey) bool })
	return ok && eq.Equal(pub)
}

// knownKey is true for a complete key of one of the four forms that the parser of the standard library gives: an ECDSA key with its curve
// and both coordinates, an RSA key with its modulus, an Ed25519 key of the full length, an X25519 key with its curve and its bytes. An
// incomplete key (a zero struct, a typed nil, a half-filled one) is not known, so the Equal of the key type is never reached with one.
func knownKey(pub crypto.PublicKey) bool {
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		return k != nil && k.Curve != nil && k.X != nil && k.Y != nil //nolint:staticcheck // only to see that the coordinates are there: Bytes would panic on a half-filled key
	case *rsa.PublicKey:
		return k != nil && k.N != nil && k.N.Sign() > 0
	case ed25519.PublicKey:
		return len(k) == ed25519.PublicKeySize
	case *ecdh.PublicKey:
		return k != nil && k.Curve() != nil && len(k.Bytes()) > 0
	}
	return false
}

// Verifier checks slot certificates against trust anchors fixed at construction. It holds no state that a call changes, so it is safe for
// concurrent use. The zero Verifier has no anchors and refuses everything.
type Verifier struct {
	anchors *x509.CertPool
	// admitted holds the SHA-256 (DER, hexadecimal) of the PIV attestation intermediates that the construction admitted.
	admitted map[string]bool
}

// Verify checks, against the anchors of v and at the current time, that slot chains to an anchor with signer as the only intermediate that
// the chain may use, and reads what slot states. A nil error proves that, and no more: it does not prove that signer was used, and it does
// not prove that a card made the key. Evidence.Generated says that, and Evidence.AttestsKey ties it to the key a caller uses.
//
// Everything is taken from the signed bytes: both certificates are read again from their Raw field, and the chain, the extensions, the name
// and the key come from the values read again, so a change to a field of the caller's certificate has no effect and a certificate with no
// Raw is refused (ErrNoRaw). The caller's certificates are not changed. The order of the checks is: the arguments, the certificates, the
// chain, then the extensions in the order the certificate lists them (the first malformed one is the error), then the slot.
func (v *Verifier) Verify(slot, signer *x509.Certificate) (Evidence, error) {
	return v.verify(time.Now(), slot, signer)
}

// VerifyAt is Verify with the chain checked at now: the slot certificate, the signer and the anchor are all judged at that time. It serves
// evidence that is checked with no card at hand, and tests. A zero now is refused (ErrZeroTime): no entry point reads it as "the current
// time".
func (v *Verifier) VerifyAt(now time.Time, slot, signer *x509.Certificate) (Evidence, error) {
	if now.IsZero() {
		return Evidence{}, ErrZeroTime
	}
	return v.verify(now, slot, signer)
}

func (v *Verifier) verify(now time.Time, slot, signer *x509.Certificate) (Evidence, error) {
	if slot == nil || signer == nil {
		return Evidence{}, ErrNil
	}
	if len(slot.Raw) == 0 || len(signer.Raw) == 0 {
		return Evidence{}, ErrNoRaw
	}
	return v.verifyDER(now, slot.Raw, signer.Raw)
}

// VerifyDER parses the two certificates and verifies them as Verify does, at the current time.
func (v *Verifier) VerifyDER(slotDER, signerDER []byte) (Evidence, error) {
	return v.verifyDER(time.Now(), slotDER, signerDER)
}

// verifyDER is the one path that every entry takes.
func (v *Verifier) verifyDER(now time.Time, slotDER, signerDER []byte) (Evidence, error) {
	if v == nil || v.anchors == nil {
		return Evidence{}, ErrNoAnchor
	}
	// The one place that every entry passes before a byte is parsed: a certificate longer than the limit is refused.
	if len(slotDER) > MaxCertificateSize || len(signerDER) > MaxCertificateSize {
		return Evidence{}, &classed{class: ErrTooLarge, text: fmt.Sprintf("pivattest: a certificate is longer than %d bytes (the slot certificate %d, the signer %d)", MaxCertificateSize, len(slotDER), len(signerDER))}
	}
	// The parsed values point into the bytes they were read from: own copies, so that another goroutine changing the caller's bytes during
	// the call cannot split the check of the chain from the facts.
	slotDER, signerDER = bytes.Clone(slotDER), bytes.Clone(signerDER)
	slot, err := x509.ParseCertificate(slotDER)
	if err != nil {
		return Evidence{}, &classed{class: ErrParse, cause: err, text: "pivattest: the slot certificate does not parse: " + err.Error()}
	}
	signer, err := x509.ParseCertificate(signerDER)
	if err != nil {
		return Evidence{}, &classed{class: ErrParse, cause: err, text: "pivattest: the signer certificate does not parse: " + err.Error()}
	}
	// A card of an older generation signs with an attestation certificate that states no Basic Constraints, which the standard library
	// refuses for a signer. The workaround is made on the value read again, which no caller holds.
	if !signer.BasicConstraintsValid {
		signer.BasicConstraintsValid = true
		signer.IsCA = true
	}
	inter := x509.NewCertPool()
	inter.AddCert(signer)
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
		// The standard library (Go 1.27) leaves the key of a certificate empty for an X25519 key, which a card holds, because it does not
		// know the algorithm of the certificate; the key itself it reads.
		if k, err := x509.ParsePKIXPublicKey(slot.RawSubjectPublicKeyInfo); err == nil {
			e.PublicKey = k
		}
	}
	e.Algorithm = algorithmOf(e.PublicKey)
	e.Anchor, e.Generated = v.anchorOf(chains, slot, signer, e.HasSlot)
	return e, nil
}

// anchorOf chooses the anchor to report, and says whether a chain of exactly three certificates (the slot certificate, the signer and an
// admitted PIV attestation intermediate) was among the chains. Among equals the anchor with the smallest SHA-256 wins, so the answer does not
// depend on the order of the chains.
func (v *Verifier) anchorOf(chains [][]*x509.Certificate, slot, signer *x509.Certificate, hasSlot bool) (string, bool) {
	var best, bestSound struct{ digest, name string }
	for _, c := range chains {
		if len(c) == 0 {
			continue
		}
		top := c[len(c)-1]
		d := digest(top)
		if hasSlot && len(c) == 3 && bytes.Equal(c[0].Raw, slot.Raw) && bytes.Equal(c[1].Raw, signer.Raw) && v.admitted[d] &&
			(bestSound.digest == "" || d < bestSound.digest) {
			bestSound.digest, bestSound.name = d, top.Subject.CommonName
		}
		if best.digest == "" || d < best.digest {
			best.digest, best.name = d, top.Subject.CommonName
		}
	}
	if bestSound.digest != "" {
		return bestSound.name, true
	}
	return best.name, false
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
	if n > 0xff {
		return 0, false // No slot has a number above one byte. The cases below say the same; this line says it to a reader of the conversion.
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
