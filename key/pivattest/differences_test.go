package pivattest_test

import (
	"crypto/x509"
	"errors"
	"testing"
	"time"

	"github.com/truvity/atoom/key/pivattest"
	"github.com/truvity/atoom/key/pivattest/attesttest"
)

// good is a verifier over a test PKI, and a slot certificate and f9 of it; the f9 states no Basic Constraints when old is true.
func good(t *testing.T, old bool) (*pivattest.Verifier, *x509.Certificate, *x509.Certificate) {
	t.Helper()
	p, err := attesttest.NewPKI(attesttest.PKIOptions{F9: attesttest.F9Options{NoBasicConstraints: old}})
	if err != nil {
		t.Fatal(err)
	}
	v, err := pivattest.NewVerifierFromPEM(p.RootPEM(), p.BundlePEM(), p.RootSHA256())
	if err != nil {
		t.Fatal(err)
	}
	slot, err := p.Slot(attesttest.SlotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return v, slot, p.F9
}

// Difference 1: the workaround for a signer without Basic Constraints is made on a copy. The signer of an older card verifies, and the
// caller's certificate is as it was.
func TestTheCallersCertificateIsNotChanged(t *testing.T) {
	v, slot, signer := good(t, true)
	if signer.BasicConstraintsValid || signer.IsCA {
		t.Fatal("the test signer states Basic Constraints")
	}
	rawSigner, rawSlot := append([]byte{}, signer.Raw...), append([]byte{}, slot.Raw...)
	for i := 0; i < 2; i++ {
		if _, err := v.Verify(slot, signer); err != nil {
			t.Fatalf("a signer without Basic Constraints is refused: %v", err)
		}
		if signer.BasicConstraintsValid || signer.IsCA {
			t.Fatalf("the signer was changed by call %d: BasicConstraintsValid %v, IsCA %v", i+1, signer.BasicConstraintsValid, signer.IsCA)
		}
	}
	if slot.BasicConstraintsValid || slot.IsCA || string(signer.Raw) != string(rawSigner) || string(slot.Raw) != string(rawSlot) {
		t.Fatal("a certificate was changed")
	}
	// A signer that states CA false is not made a CA: only an absent constraint is.
	p, _ := attesttest.NewPKI(attesttest.PKIOptions{F9: attesttest.F9Options{NotCA: true}})
	vv, _ := pivattest.NewVerifierFromPEM(p.RootPEM(), p.BundlePEM(), p.RootSHA256())
	s2, _ := p.Slot(attesttest.SlotOptions{})
	if _, err := vv.Verify(s2, p.F9); !errors.Is(err, pivattest.ErrChain) {
		t.Fatalf("a signer that says it is no CA: %v", err)
	}
}

// Difference 2: a nil argument is an error, not a panic. So is the zero Verifier, which would otherwise trust the system's roots.
func TestNilArgumentsAreAnError(t *testing.T) {
	v, slot, signer := good(t, false)
	for name, f := range map[string]func() error{
		"nil slot":   func() error { _, err := v.Verify(nil, signer); return err },
		"nil signer": func() error { _, err := v.Verify(slot, nil); return err },
		"both nil":   func() error { _, err := v.Verify(nil, nil); return err },
		"at, nil":    func() error { _, err := v.VerifyAt(time.Now(), nil, nil); return err },
	} {
		if err := f(); !errors.Is(err, pivattest.ErrNil) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := v.VerifyDER(nil, nil); !errors.Is(err, pivattest.ErrParse) {
		t.Errorf("no DER: %v", err)
	}
	if _, err := v.VerifyDER(slot.Raw, []byte{1, 2, 3}); !errors.Is(err, pivattest.ErrParse) {
		t.Errorf("a signer that is not DER: %v", err)
	}
	if _, err := v.VerifyDER([]byte{0x30, 0x00}, signer.Raw); !errors.Is(err, pivattest.ErrParse) {
		t.Errorf("a slot certificate that is not DER: %v", err)
	}
	for name, vz := range map[string]*pivattest.Verifier{"the zero verifier": {}, "a nil verifier": nil} {
		if _, err := vz.Verify(slot, signer); !errors.Is(err, pivattest.ErrNoAnchor) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Difference 3: VerifyAt checks the chain at the time it is given, so evidence kept for a while, or checked with no card, can be judged at
// the time it was made.
func TestVerifyAtJudgesTheChainAtThatTime(t *testing.T) {
	// The PKI is valid for a century; the slot certificate for an hour and a half around now.
	p, err := attesttest.NewPKI(attesttest.PKIOptions{NotBefore: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), NotAfter: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	v, err := pivattest.NewVerifierFromPEM(p.RootPEM(), p.BundlePEM(), p.RootSHA256())
	if err != nil {
		t.Fatal(err)
	}
	nb, na := time.Now().Add(-30*time.Minute), time.Now().Add(time.Hour)
	slot, err := p.Slot(attesttest.SlotOptions{NotBefore: nb, NotAfter: na})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		at   time.Time
		ok   bool
	}{
		{"now", time.Now(), true},
		{"the zero time is now", time.Time{}, true},
		{"before the slot certificate", nb.Add(-10 * time.Minute), false},
		{"in a year", time.Now().AddDate(1, 0, 0), false},
		{"in 50 years", time.Now().AddDate(50, 0, 0), false},
	} {
		_, err := v.VerifyAt(tt.at, slot, p.F9)
		if (err == nil) != tt.ok || (err != nil && !errors.Is(err, pivattest.ErrChain)) {
			t.Errorf("%s: %v, want ok %v", tt.name, err, tt.ok)
		}
	}
	if _, err := v.Verify(slot, p.F9); err != nil {
		t.Errorf("Verify at the current time: %v", err)
	}
}
