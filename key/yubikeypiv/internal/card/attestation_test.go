package card

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/truvity/atoom/key/pivattest"
	"github.com/truvity/atoom/key/pivattest/attesttest"
)

// These tests have no build tag and no cgo: the reading of an attestation is the library key/pivattest and the adapter of attestation.go, and
// both run on every platform. The differential test (pivattest_diff_test.go, tag atoom_card) holds the adapter to the reading of piv-go that
// the driver made before it used the library.

var (
	windowFrom = time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC)
	windowTo   = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
)

// testPKI is a test PKI whose PIV intermediate has the given name, valid from 2010 to 2099.
func testPKI(t *testing.T, pivName string) *attesttest.PKI {
	t.Helper()
	p, err := attesttest.NewPKI(attesttest.PKIOptions{NotBefore: windowFrom, NotAfter: windowTo, PIVName: pivName})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func slotOf(t *testing.T, p *attesttest.PKI, ext []pkix.Extension) *x509.Certificate {
	t.Helper()
	c, err := p.Slot(attesttest.SlotOptions{Extensions: ext, NotBefore: windowFrom, NotAfter: windowTo})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func verifierOf(t *testing.T, p *attesttest.PKI) func(att, slot *x509.Certificate) (Attestation, error) {
	t.Helper()
	v, err := NewAttestationVerifier(p.RootPEM(), p.BundlePEM(), p.RootSHA256())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// The quirks of the reading, as a table: what fromEvidence keeps of each value of the library's evidence.
func TestFromEvidenceKeepsTheQuirksOfTheReading(t *testing.T) {
	pins := map[pivattest.PINPolicy]PINPolicy{
		0: 0, pivattest.PINNever: PINNever, pivattest.PINOnce: PINOnce, pivattest.PINAlways: 0, pivattest.PINMatchOnce: 0, pivattest.PINMatchAlways: 0,
	}
	touches := map[pivattest.TouchPolicy]TouchPolicy{
		0: 0, pivattest.TouchNever: TouchNever, pivattest.TouchAlways: TouchAlways, pivattest.TouchCached: TouchCached,
	}
	for pin, wantPIN := range pins {
		for touch, wantTouch := range touches {
			got := fromEvidence(pivattest.Evidence{PIN: pin, Touch: touch, HasPolicy: pin != 0 || touch != 0})
			if got.PIN != wantPIN || got.Touch != wantTouch {
				t.Errorf("PIN %d touch %d: read PIN %d touch %d, want %d and %d", pin, touch, got.PIN, got.Touch, wantPIN, wantTouch)
			}
		}
	}
	tests := []struct {
		name string
		in   pivattest.Evidence
		want Attestation
	}{
		{"nothing stated: every fact is its zero value, and there is no error", pivattest.Evidence{}, Attestation{}},
		{
			"every fact stated",
			pivattest.Evidence{
				Slot: 0x9a, HasSlot: true, Firmware: pivattest.Version{Major: 5, Minor: 8, Patch: 0}, HasFirmware: true, Serial: 1234567, HasSerial: true,
				PIN: pivattest.PINOnce, Touch: pivattest.TouchNever, HasPolicy: true,
			},
			Attestation{Version: Version{5, 8, 0}, Serial: 1234567, Slot: 0x9a, PIN: PINOnce, Touch: TouchNever},
		},
		{
			"a retired slot and the largest values",
			pivattest.Evidence{Slot: 0x95, Firmware: pivattest.Version{Major: 255, Minor: 254, Patch: 253}, Serial: 1<<32 - 1, PIN: pivattest.PINNever, Touch: pivattest.TouchCached},
			Attestation{Version: Version{255, 254, 253}, Serial: 1<<32 - 1, Slot: 0x95, PIN: PINNever, Touch: TouchCached},
		},
		{
			"the form factor, the algorithm, the key, the anchor and Generated are dropped",
			pivattest.Evidence{
				Slot: 0x9c, FormFactor: 0x85, HasFormFactor: true, Algorithm: pivattest.AlgEC384, Anchor: "Yubico PIV Attestation B2 1", Generated: true,
				PIN: pivattest.PINOnce, Touch: pivattest.TouchNever, HasPolicy: true,
			},
			Attestation{Slot: 0x9c, PIN: PINOnce, Touch: TouchNever},
		},
		{
			"Generated is not required: an evidence that is not generated reads the same",
			pivattest.Evidence{Slot: 0x9c, PIN: pivattest.PINOnce, Touch: pivattest.TouchNever, HasPolicy: true, Generated: false},
			Attestation{Slot: 0x9c, PIN: PINOnce, Touch: TouchNever},
		},
	}
	for _, tt := range tests {
		if got := fromEvidence(tt.in); got != tt.want {
			t.Errorf("%s: read %+v, want %+v", tt.name, got, tt.want)
		}
	}
}

// The same quirks through the library, on certificates: a policy that is not "never" or "once" for the PIN is read as none, and a certificate
// with none of the extensions reads as zeros.
func TestTheQuirksHoldThroughTheLibrary(t *testing.T) {
	p := testPKI(t, "Yubico PIV Attestation B2 1")
	verify := verifierOf(t, p)
	policy := func(pin, touch byte) []pkix.Extension {
		return []pkix.Extension{attesttest.Firmware(5, 8, 0), attesttest.Serial(7), attesttest.Policy(pin, touch), attesttest.FormFactor(4)}
	}
	for pin := byte(1); pin <= 5; pin++ {
		for touch := byte(1); touch <= 3; touch++ {
			a, err := verify(p.F9, slotOf(t, p, policy(pin, touch)))
			if err != nil {
				t.Fatalf("PIN %d touch %d: %v", pin, touch, err)
			}
			wantPIN := PINPolicy(0)
			if pin <= 2 {
				wantPIN = PINPolicy(pin) // PINNever and PINOnce are 1 and 2
			}
			if a.PIN != wantPIN || a.Touch != TouchPolicy(touch) {
				t.Errorf("PIN %d touch %d: read %+v", pin, touch, a)
			}
		}
	}
	a, err := verify(p.F9, slotOf(t, p, []pkix.Extension{}))
	if err != nil || a != (Attestation{Slot: 0x9a}) {
		t.Errorf("no extension at all: %+v, %v (the slot is read from the name, 9a)", a, err)
	}
}

func TestAChainThroughAPIVAttestationIntermediateVerifiesAndIsRead(t *testing.T) {
	p := testPKI(t, "Yubico PIV Attestation B2 1")
	got, err := verifierOf(t, p)(p.F9, slotOf(t, p, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != (Version{5, 8, 0}) || got.Serial != 1234567 || got.Slot != 0x9a || got.PIN != PINOnce || got.Touch != TouchNever {
		t.Fatalf("read %+v, want 5.8.0, serial 1234567, slot 9a, PIN once, touch never", got)
	}
}

// The chain error keeps its text prefix, for the log line of the driver, and is the library's named error too. The error of a malformed
// extension is the library's own wording and its named error.
func TestTheErrorsAreTheLibrarysAndTheChainErrorKeepsItsPrefix(t *testing.T) {
	good, rogue := testPKI(t, "Yubico PIV Attestation B2 1"), testPKI(t, "Yubico PIV Attestation B2 1")
	verify := verifierOf(t, good)
	_, err := verify(rogue.F9, slotOf(t, rogue, nil))
	if !errors.Is(err, pivattest.ErrChain) || !strings.HasPrefix(err.Error(), "error verifying attestation certificate: ") {
		t.Errorf("an attestation of another PKI: %v", err)
	}
	_, err = verify(good.F9, slotOf(t, good, []pkix.Extension{attesttest.Policy(9, 9)}))
	if !errors.Is(err, pivattest.ErrPolicyExt) {
		t.Errorf("a malformed policy extension: %v", err)
	}
	// a nil certificate is an error, not a panic (the reading of piv-go panicked)
	if _, err := verify(nil, slotOf(t, good, nil)); !errors.Is(err, pivattest.ErrNil) {
		t.Errorf("a nil attestation certificate: %v", err)
	}
	// the certificate of the caller is not changed (piv-go set the Basic Constraints of the attestation certificate)
	noBC, err := good.WithF9(attesttest.F9Options{NoBasicConstraints: true, NotBefore: windowFrom, NotAfter: windowTo})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verify(noBC.F9, slotOf(t, noBC, nil)); err != nil {
		t.Fatal(err)
	}
	if noBC.F9.BasicConstraintsValid || noBC.F9.IsCA {
		t.Error("the attestation certificate of the caller was changed")
	}
}

// The constructor over a test PKI refuses what the library refuses: a root that is not the pinned one, and a bundle with no PIV
// attestation intermediate (a FIDO attestation CA is the vendor's too, and does not sign the attestation key of a PIV applet).
func TestTheConstructorRefusesARootAndABundleThatAreNotTheVendors(t *testing.T) {
	p := testPKI(t, "Yubico PIV Attestation B2 1")
	if _, err := NewAttestationVerifier(p.RootPEM(), p.BundlePEM(), strings.Repeat("00", 32)); !errors.Is(err, pivattest.ErrRootPin) {
		t.Errorf("a root with another hash: %v", err)
	}
	fido := testPKI(t, "Yubico FIDO Attestation B2 1")
	if _, err := NewAttestationVerifier(fido.RootPEM(), attesttest.PEM(fido.Inter, fido.PIV), fido.RootSHA256()); !errors.Is(err, pivattest.ErrNoAnchor) {
		t.Errorf("a bundle with no PIV attestation intermediate: %v", err)
	}
}

// pivCard.VerifyAttestation is verifyPinned: the vendor's pinned certificates, which a test PKI does not chain to.
func TestThePinnedVerifierRefusesATestPKI(t *testing.T) {
	p := testPKI(t, "Yubico PIV Attestation B2 1")
	_, err := verifyPinned(p.F9, slotOf(t, p, nil))
	if !errors.Is(err, pivattest.ErrChain) || !strings.HasPrefix(err.Error(), "error verifying attestation certificate: ") {
		t.Errorf("a test PKI against the vendor's certificates: %v", err)
	}
	if _, err := verifyPinned(nil, nil); !errors.Is(err, pivattest.ErrNil) {
		t.Errorf("no certificates: %v", err)
	}
}
