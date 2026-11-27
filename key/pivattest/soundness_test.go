package pivattest_test

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"math/big"
	"reflect"
	"testing"

	"github.com/truvity/atoom/key/pivattest"
	"github.com/truvity/atoom/key/pivattest/attesttest"
)

func newPKI(t *testing.T, o attesttest.PKIOptions) (*attesttest.PKI, *pivattest.Verifier) {
	t.Helper()
	p, err := attesttest.NewPKI(o)
	if err != nil {
		t.Fatal(err)
	}
	v, err := pivattest.NewVerifierFromPEM(p.RootPEM(), p.BundlePEM(), p.RootSHA256())
	if err != nil {
		t.Fatal(err)
	}
	return p, v
}

// R1: an attestation of one key does not speak for another. The slot certificate attests K1; a caller that uses K2 (imported over the slot)
// must find out, and AttestsKey is the comparison.
func TestAttestsKeyOnlyTheKeyAttested(t *testing.T) {
	p, v := newPKI(t, attesttest.PKIOptions{})
	k1, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	k2, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	edPub, _, _ := ed25519.GenerateKey(rand.Reader)
	slot, err := p.Slot(attesttest.SlotOptions{PublicKey: &k1.PublicKey})
	if err != nil {
		t.Fatal(err)
	}
	ev, err := v.Verify(slot, p.F9)
	if err != nil || !ev.Generated {
		t.Fatalf("%+v, %v", ev, err)
	}
	for name, tt := range map[string]struct {
		pub  any
		want bool
	}{
		"the key attested":                  {&k1.PublicKey, true},
		"another key imported to the slot":  {&k2.PublicKey, false},
		"nil":                               {nil, false},
		"a typed nil":                       {(*ecdsa.PublicKey)(nil), false},
		"another type":                      {edPub, false},
		"a private key":                     {k1, false},
		"a value of the key, not a pointer": {k1.PublicKey, false},
		"a string":                          {"key", false},
	} {
		if got := ev.AttestsKey(tt.pub); got != tt.want {
			t.Errorf("%s: %v, want %v", name, got, tt.want)
		}
	}
	// A nil key of the evidence, or an evidence that is not Generated, attests nothing.
	if (pivattest.Evidence{Generated: true}).AttestsKey(&k1.PublicKey) {
		t.Error("an evidence with no key attests a key")
	}
	weak := ev
	weak.Generated = false
	if weak.AttestsKey(&k1.PublicKey) {
		t.Error("an evidence that is not Generated attests a key")
	}
}

// R2: Generated is true for the good chain of three, and for no other.
func TestGeneratedIsSound(t *testing.T) {
	p, v := newPKI(t, attesttest.PKIOptions{F9: attesttest.F9Options{CommonName: "YubiKey PIV Attestation 9a"}})
	good, err := p.Slot(attesttest.SlotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ev, err := v.Verify(good, p.F9)
	if err != nil || !ev.Generated || ev.Anchor != "Yubico PIV Attestation TEST ONLY 1" || !ev.HasSlot {
		t.Fatalf("the good chain of three: %+v, %v", ev, err)
	}
	// A slot name that gives no slot: the chain is good, and Generated is false.
	noSlot, _ := p.Slot(attesttest.SlotOptions{CommonName: "YubiKey PIV Attestation f9"})
	if ev, err := v.Verify(noSlot, p.F9); err != nil || ev.Generated || ev.HasSlot {
		t.Errorf("no slot in the name: %+v, %v", ev, err)
	}
	// The slot certificate is an anchor (a public intermediate of the vendor), the signer is any certificate: accepted, as today, and not
	// Generated; the key is the CA's.
	ev, err = v.Verify(p.PIV, p.F9)
	if err != nil || ev.Generated || ev.AttestsKey(ev.PublicKey) {
		t.Errorf("an anchor as the slot certificate: %+v, %v", ev, err)
	}
	// The card's own attestation certificate as the slot certificate, whose name reads as a slot: a chain of two.
	ev, err = v.Verify(p.F9, p.F9)
	if err != nil || ev.Generated || !ev.HasSlot {
		t.Errorf("f9 as the slot certificate: %+v, %v", ev, err)
	}
	// The signer is not in the chain: the slot certificate is signed by the PIV intermediate itself, and any certificate is given as signer.
	direct, err := p.Slot(attesttest.SlotOptions{SignedByPIV: true})
	if err != nil {
		t.Fatal(err)
	}
	ev, err = v.Verify(direct, p.F9)
	if err != nil || ev.Generated {
		t.Errorf("the signer is not in the chain: %+v, %v", ev, err)
	}
	// A chain that ends in the root: three certificates, the third not admitted.
	byRoot, err := p.WithF9(attesttest.F9Options{CommonName: "YubiKey PIV Attestation 9a", SignedBy: "root"})
	if err != nil {
		t.Fatal(err)
	}
	slot, _ := byRoot.Slot(attesttest.SlotOptions{})
	if ev, err := v.Verify(slot, byRoot.F9); err != nil || ev.Generated || ev.Anchor != "TEST ONLY Yubico Attestation Root 1" {
		t.Errorf("a chain that ends in the root: %+v, %v", ev, err)
	}
}

// R3 is in differences_test.go (TestVerifyAtJudgesTheChainAtThatTime).

// R5, the fourth difference: everything is read from the signed bytes. A change to a field of the caller's certificate has no effect, and a
// certificate with no Raw is refused.
func TestFactsComeFromTheSignedBytes(t *testing.T) {
	p, v := newPKI(t, attesttest.PKIOptions{F9: attesttest.F9Options{NoBasicConstraints: true}})
	slot, err := p.Slot(attesttest.SlotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want, err := v.Verify(slot, p.F9)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	slot.Extensions = nil
	slot.Subject.CommonName = "YubiKey PIV Attestation 9c"
	slot.PublicKey = &other.PublicKey
	slot.RawSubjectPublicKeyInfo = nil
	slot.NotAfter = slot.NotBefore
	p.F9.IsCA, p.F9.BasicConstraintsValid = false, true // the signer's fields say it is not a CA
	got, err := v.Verify(slot, p.F9)
	if err != nil {
		t.Fatalf("a changed field changed the verdict: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("a changed field changed the evidence: %+v, want %+v", got, want)
	}
	// A field that says "CA" does not make a signer a CA: the signed bytes say CA false.
	q, vq := newPKI(t, attesttest.PKIOptions{F9: attesttest.F9Options{NotCA: true}})
	s2, _ := q.Slot(attesttest.SlotOptions{})
	q.F9.IsCA = true
	if _, err := vq.Verify(s2, q.F9); !errors.Is(err, pivattest.ErrChain) {
		t.Errorf("a signer whose field was set to CA: %v", err)
	}
	// A certificate with no Raw is refused, whatever its fields say.
	fresh, _ := q.Slot(attesttest.SlotOptions{})
	byHand := &x509.Certificate{Subject: fresh.Subject, Extensions: fresh.Extensions, PublicKey: fresh.PublicKey}
	if _, err := vq.Verify(byHand, q.F9); !errors.Is(err, pivattest.ErrNoRaw) {
		t.Errorf("a slot certificate with no Raw: %v", err)
	}
	if _, err := vq.Verify(fresh, &x509.Certificate{}); !errors.Is(err, pivattest.ErrNoRaw) {
		t.Errorf("a signer with no Raw: %v", err)
	}
	garbage := &x509.Certificate{Raw: []byte{1, 2, 3}}
	if _, err := vq.Verify(fresh, garbage); !errors.Is(err, pivattest.ErrParse) {
		t.Errorf("a signer whose Raw does not parse: %v", err)
	}
}

// An incomplete key, on either side of AttestsKey, is false and never a panic.
func TestAttestsKeyNeverPanicsOnAnIncompleteKey(t *testing.T) {
	good, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	rk, _ := rsa.GenerateKey(rand.Reader, 2048)
	edPub, _, _ := ed25519.GenerateKey(rand.Reader)
	xk, _ := ecdh.X25519().GenerateKey(rand.Reader)
	complete := []any{&good.PublicKey, &rk.PublicKey, edPub, xk.PublicKey()}
	incomplete := map[string]any{
		"nil interface":     nil,
		"ecdsa zero":        &ecdsa.PublicKey{},
		"ecdsa nil":         (*ecdsa.PublicKey)(nil),
		"ecdsa curve only":  &ecdsa.PublicKey{Curve: elliptic.P256()},
		"ecdsa no Y":        &ecdsa.PublicKey{Curve: elliptic.P256(), X: good.X}, //nolint:staticcheck // a half-filled key is the case
		"ecdsa no curve":    &ecdsa.PublicKey{X: good.X, Y: good.Y},              //nolint:staticcheck // a half-filled key is the case
		"rsa zero":          &rsa.PublicKey{},
		"rsa nil":           (*rsa.PublicKey)(nil),
		"rsa exponent only": &rsa.PublicKey{E: 65537},
		"rsa zero modulus":  &rsa.PublicKey{N: new(big.Int), E: 65537},
		"ed25519 nil":       ed25519.PublicKey(nil),
		"ed25519 short":     edPub[:16],
		"ed25519 long":      append(append(ed25519.PublicKey{}, edPub...), 0),
		"x25519 zero":       &ecdh.PublicKey{},
		"x25519 nil":        (*ecdh.PublicKey)(nil),
	}
	for name, bad := range incomplete {
		for _, key := range complete {
			// the incomplete key as the caller's key, against a good evidence
			if (pivattest.Evidence{Generated: true, PublicKey: key}).AttestsKey(bad) {
				t.Errorf("%s as the caller's key: true", name)
			}
			// the incomplete key inside a hand-filled evidence
			if (pivattest.Evidence{Generated: true, PublicKey: bad}).AttestsKey(key) {
				t.Errorf("%s inside the evidence: true", name)
			}
		}
		if (pivattest.Evidence{Generated: true, PublicKey: bad}).AttestsKey(bad) {
			t.Errorf("%s on both sides: true", name)
		}
	}
	// complete keys of every form still match themselves
	for _, key := range complete {
		if !(pivattest.Evidence{Generated: true, PublicKey: key}).AttestsKey(key) {
			t.Errorf("%T does not attest itself", key)
		}
	}
}

// The bytes a caller gives are copied: changing them during or after the call cannot change what was read.
func TestVerifyDERCopiesItsInputs(t *testing.T) {
	p, v := newPKI(t, attesttest.PKIOptions{})
	slot, err := p.SlotDER(attesttest.SlotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	signer := append([]byte{}, p.F9.Raw...)
	ev, err := v.VerifyDER(slot, signer)
	if err != nil {
		t.Fatal(err)
	}
	for i := range slot {
		slot[i] ^= 0xff
	}
	for i := range signer {
		signer[i] ^= 0xff
	}
	if ev.PublicKey == nil || !ev.Generated {
		t.Errorf("the evidence changed with the caller's bytes: %+v", ev)
	}
}
