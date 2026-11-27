package pivattest_test

import (
	"crypto"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"math/big"
	"testing"

	"github.com/truvity/atoom/key/pivattest"
	"github.com/truvity/atoom/key/pivattest/attesttest"
)

// otherKey makes another key of the same kind as pub (the RSA ones by moving the modulus by 2, which keeps the size and the exponent: a key is
// not made, only compared).
func otherKey(t *testing.T, pub crypto.PublicKey) crypto.PublicKey {
	t.Helper()
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		n, err := ecdsa.GenerateKey(k.Curve, rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return &n.PublicKey
	case *rsa.PublicKey:
		return &rsa.PublicKey{N: new(big.Int).Add(k.N, big.NewInt(2)), E: k.E}
	case ed25519.PublicKey:
		n, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return n
	case *ecdh.PublicKey:
		n, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return n.PublicKey()
	}
	t.Fatalf("no key of this kind: %T", pub)
	return nil
}

func attests(t *testing.T, ev pivattest.Evidence, pub crypto.PublicKey) (got bool) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("AttestsKey panicked on %T: %v", pub, r)
		}
	}()
	return ev.AttestsKey(pub)
}

// AttestsKey for every kind of key of the corpus (the kinds a card holds and the kinds it does not), against the same key, another key of the
// same kind, a key of every other kind, and every incomplete form.
func TestAttestsKeyForEveryKindOfKey(t *testing.T) {
	p := fixedPKI(t, attesttest.PKIOptions{})
	v := verifierOf(t, p)
	keys := slotKeys(t)
	evidence := map[string]pivattest.Evidence{}
	for name, pub := range keys {
		der, err := p.SlotDER(attesttest.SlotOptions{PublicKey: pub, NotBefore: t2010, NotAfter: t2099})
		if err != nil {
			t.Fatal(err)
		}
		ev, err := v.VerifyDER(der, p.F9.Raw)
		if err != nil || !ev.Generated {
			t.Fatalf("%s: %+v, %v", name, ev, err)
		}
		evidence[name] = ev
	}
	if len(evidence) != 10 {
		t.Fatalf("%d kinds of key", len(evidence))
	}
	g, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	edPub, _, _ := ed25519.GenerateKey(rand.Reader)
	incomplete := map[string]crypto.PublicKey{
		"nil": nil, "ecdsa nil": (*ecdsa.PublicKey)(nil), "ecdsa zero": &ecdsa.PublicKey{}, "ecdsa curve only": &ecdsa.PublicKey{Curve: elliptic.P256()},
		"ecdsa no Y":     &ecdsa.PublicKey{Curve: elliptic.P256(), X: g.X}, //nolint:staticcheck // a half-filled key is the case
		"ecdsa no curve": &ecdsa.PublicKey{X: g.X, Y: g.Y},                 //nolint:staticcheck // a half-filled key is the case
		"rsa nil":        (*rsa.PublicKey)(nil), "rsa zero": &rsa.PublicKey{}, "rsa no modulus": &rsa.PublicKey{E: 65537}, "rsa empty modulus": &rsa.PublicKey{N: new(big.Int), E: 65537},
		"ed25519 nil": ed25519.PublicKey(nil), "ed25519 short": edPub[:31], "ed25519 long": append(append(ed25519.PublicKey{}, edPub...), 0),
		"ecdh nil": (*ecdh.PublicKey)(nil), "ecdh zero": &ecdh.PublicKey{},
		"a value, not a pointer": g.PublicKey, "a private key": g, "a string": "key", "a byte slice": []byte{1, 2, 3},
	}
	for name, ev := range evidence {
		t.Run(name, func(t *testing.T) {
			if !attests(t, ev, keys[name]) {
				t.Error("the evidence does not attest the key it holds")
			}
			if !attests(t, ev, ev.PublicKey) {
				t.Error("the evidence does not attest its own PublicKey")
			}
			if attests(t, ev, otherKey(t, keys[name])) {
				t.Error("the evidence attests another key of the same kind")
			}
			for other, pub := range keys {
				if other != name && attests(t, ev, pub) {
					t.Errorf("the evidence attests the key of kind %s", other)
				}
			}
			for what, bad := range incomplete {
				if attests(t, ev, bad) {
					t.Errorf("the evidence attests %s", what)
				}
				// the incomplete form inside a hand-filled evidence, against the key and against itself
				hand := pivattest.Evidence{Generated: true, PublicKey: bad}
				if attests(t, hand, keys[name]) || attests(t, hand, bad) {
					t.Errorf("a hand-filled evidence with %s attests a key", what)
				}
			}
			// not Generated: nothing is attested, the key held included
			weak := ev
			weak.Generated = false
			if attests(t, weak, keys[name]) {
				t.Error("an evidence that is not Generated attests a key")
			}
		})
	}
	// the Algorithm of the evidence and the kind of the key agree on what a card holds; the kinds it does not hold are still compared
	for _, name := range []string{"ec-p224", "ec-p521", "rsa-2048-e3"} {
		if evidence[name].Algorithm != pivattest.AlgUnknown {
			t.Errorf("%s: algorithm %v", name, evidence[name].Algorithm)
		}
	}
}
