package yubikeypiv_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"testing"

	"connectrpc.com/connect"

	keyv1alpha1 "github.com/truvity/atoom/key/gen/atoom/key/v1alpha1"
	"github.com/truvity/atoom/key/yubikeypiv/internal/card"
	"github.com/truvity/atoom/key/yubikeypiv/internal/card/cardtest"
)

// B. The key. The driver uses the key of the signed bytes of the certificate, the key a card hands back after it generated one has to be that
// key, and a key of a kind that the plan does not want is refused where the plan is compared.

func qpSPKI(t *testing.T, pub crypto.PublicKey) []byte {
	t.Helper()
	b, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A parsed certificate whose field PublicKey is another key than the one in its signed bytes: the key that comes back is the one of the bytes, on
// adoption (an EC key and an RSA key) and on the peer path (where the field is changed in place, on the value that the driver parsed).
func TestQPTheKeyThatComesBackIsTheKeyOfTheSignedBytes(t *testing.T) {
	other := qpKey(t)

	t.Run("adoption, the RSA key", func(t *testing.T) {
		c := cardtest.New()
		signed, err := c.PutRSAKey(0x88, card.KeySpec{Algorithm: card.RSA2048, PIN: card.PINOnce, Touch: card.TouchNever})
		if err != nil {
			t.Fatal(err)
		}
		otherRSA, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		q := &quirkCard{Card: c, verify: fakeVerifier(t, c)}
		q.attest = func(s card.Slot) (*x509.Certificate, error) {
			cert, err := c.Attest(s)
			if err != nil || s != 0x88 {
				return cert, err
			}
			swapped := *cert
			swapped.PublicKey = &otherRSA.PublicKey
			return &swapped, nil
		}
		s, _, err := startQuirk(t, q, factoryDefaults)
		if err != nil {
			t.Fatal(err)
		}
		res, err := s.PublicKey(context.Background(), connect.NewRequest(&keyv1alpha1.PublicKeyRequest{Purpose: "token-signing-rs256"}))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(res.Msg.GetSpkiDer(), qpSPKI(t, signed)) || bytes.Equal(res.Msg.GetSpkiDer(), qpSPKI(t, &otherRSA.PublicKey)) {
			t.Fatal("the driver took the key of the field of the parsed certificate, not the key of its signed bytes")
		}
	})

	t.Run("the peer path", func(t *testing.T) {
		w := newQPWorld(t, 12345678, false, false)
		slot, signed := w.slot(t, qpSlot{})
		slotDER := slot.Raw
		// the card layer is given the value that the driver parsed: it changes the field of that value, and then reads as it always does
		q := &quirkCard{Card: cardtest.New()}
		q.verify = func(att, slot *x509.Certificate) (card.Attestation, error) {
			slot.PublicKey = &other.PublicKey
			return w.verify(att, slot)
		}
		s, _, err := startQuirk(t, q, factoryDefaults)
		if err != nil {
			t.Fatal(err)
		}
		res, err := s.VerifyAttestation(context.Background(), connect.NewRequest(&keyv1alpha1.VerifyAttestationRequest{Attestation: slotDER, AttestationSigner: w.f9.Raw}))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(res.Msg.GetSpkiDer(), qpSPKI(t, signed)) || bytes.Equal(res.Msg.GetSpkiDer(), qpSPKI(t, &other.PublicKey)) {
			t.Fatal("the peer path answered with the key of the field of the parsed certificate, not the key of its signed bytes")
		}
	})
}

// A key of a kind that the plan does not want, and a key that the standard library does not read, through both paths. On adoption the plan of
// slot 9a is a P-384 key and it is refused for its algorithm; the peer path answers with the key of any EC curve (key@ holds it to its plan
// afterwards) and refuses every key that is not an EC key. The neighbour is the key the plan asks.
func TestQPAKeyOfAKindThePlanDoesNotWantIsRefused(t *testing.T) {
	const serial = 12345678
	w := newQPWorld(t, serial, false, false)
	const algo = "algorithm: not the curve of the plan"
	for _, c := range []struct {
		kind      string
		adoptWord string // "" accepted
		peerOK    bool
	}{
		{"", "", true},
		{"p256", algo, true},
		{"p521", algo, true},
		{"rsa2048", algo, false},
		{"ed25519", algo, false},
		{"x25519", algo, false},
		{"unknown", algo, false},
	} {
		t.Run("a key of the kind "+c.kind, func(t *testing.T) {
			slot, key := w.slot(t, qpSlot{key: c.kind})
			if c.kind == "unknown" && slot.PublicKey != nil {
				t.Fatalf("the standard library reads the key %T: the test does not make a key it cannot read", slot.PublicKey)
			}
			a := qpAdopt(t, w, w.f9, slot)
			if c.adoptWord == "" {
				if !a.accepted() || !bytes.Equal(a.spki, qpSPKI(t, key)) {
					t.Errorf("adoption: %v", a.err)
				}
			} else if a.accepted() || a.word != c.adoptWord {
				t.Errorf("adoption: %v, want %q", a.err, c.adoptWord)
			}
			p := qpPeer(t, w, w.f9, slot)
			if c.peerOK {
				if !p.accepted() || !bytes.Equal(p.spki, qpSPKI(t, key)) {
					t.Errorf("peer: %v", p.err)
				}
			} else {
				qpWantPeerRefusal(t, "peer", p)
			}
		})
	}
}

// The plan of the optional RSA purpose: an RSA-2048 key is adopted, and a key that is not one leaves the purpose absent, with the reason in the
// journal and no refusal of the start.
func TestQPAKeyOfAKindTheRSAPlanDoesNotWantLeavesThePurposeAbsent(t *testing.T) {
	const serial = 12345678
	w := newQPWorld(t, serial, false, false)
	rsaSlot := func(kind string) qpOut {
		cert, _ := w.slot(t, qpSlot{cn: "YubiKey PIV Attestation 88", key: kind})
		return qpAdoptAt(t, w, w.f9, 0x88, "token-signing-rs256", cert)
	}
	if o := rsaSlot("rsa2048"); !o.accepted() {
		t.Fatalf("the RSA-2048 key of the plan: %v (%s)", o.err, o.word)
	}
	for _, kind := range []string{"", "p256", "ed25519", "x25519", "unknown"} {
		o := rsaSlot(kind)
		if o.accepted() {
			t.Errorf("the key of the kind %q was adopted for the RSA purpose", kind)
			continue
		}
		if connect.CodeOf(o.err) != connect.CodeNotFound {
			t.Errorf("the kind %q: the purpose is not absent: %v", kind, o.err)
		}
		var line logLine
		for _, l := range o.rec.got() {
			if l.msg == "the key in the slot of an optional purpose is not the plan's: the purpose is left absent" {
				line = l
			}
		}
		if line.msg == "" {
			t.Errorf("the kind %q: no journal line: %v", kind, o.rec.messages())
			continue
		}
		got := map[string]string{}
		for _, at := range line.attrs {
			got[at.Key] = at.Value.String()
		}
		if got["reason"] != "algorithm: not the RSA-2048 key of the plan" || got["slot"] != "88" || got["purpose"] != "token-signing-rs256" {
			t.Errorf("the kind %q: the line says %v", kind, got)
		}
	}
}

// qpPurposes is the reference plan: the purposes a card holds, their algorithm word and whether the key is an RSA key.
var qpPurposes = []struct {
	name, algorithm string
	rsa             bool
}{
	{"wrapping", "ECDH-P384", false}, {"token-signing", "ES384", false}, {"audit-log", "ES256", false}, {"pin-release", "ES384", false},
	{"workload-issuer", "ES384", false}, {"x509-intermediate", "ES384", false}, {"ssh-user", "ES384", false}, {"ssh-host", "ES384", false},
	{"issuance-log", "ES384", false}, {"release-signing", "ES384", false}, {"token-signing-rs256", "RS256", true},
}

// After a key is generated, the card answers with the public key it returns. With the key that the attestation certifies (the same value, or a
// copy of it made from its encoding) the key is accepted, for every purpose of the reference plan: the comparison is never the reason a right key
// is refused. With another key of the same type, with a key of the other type or with nothing, it is refused, and the purpose has no key.
func TestQPTheKeyThatTheCardReturnsAfterGenerateMustBeTheAttestedOne(t *testing.T) {
	otherEC, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherEC256, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherRSA, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	copyOf := func(pub crypto.PublicKey) crypto.PublicKey {
		k, err := x509.ParsePKIXPublicKey(qpSPKI(t, pub))
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	for _, p := range qpPurposes {
		for _, c := range []struct {
			name   string
			answer func(got crypto.PublicKey) crypto.PublicKey
			ok     bool
		}{
			{"the key itself", func(got crypto.PublicKey) crypto.PublicKey { return got }, true},
			{"a copy made from its encoding", copyOf, true},
			{"another key of the same type", func(got crypto.PublicKey) crypto.PublicKey {
				switch got.(type) {
				case *rsa.PublicKey:
					return &otherRSA.PublicKey
				}
				if k := got.(*ecdsa.PublicKey); k.Curve == elliptic.P256() {
					return &otherEC256.PublicKey
				}
				return &otherEC.PublicKey
			}, false},
			{"a key of the other type", func(got crypto.PublicKey) crypto.PublicKey {
				if _, ok := got.(*rsa.PublicKey); ok {
					return &otherEC.PublicKey
				}
				return &otherRSA.PublicKey
			}, false},
			{"nothing", func(crypto.PublicKey) crypto.PublicKey { return nil }, false},
		} {
			t.Run(p.name+"/"+c.name, func(t *testing.T) {
				cd := cardtest.New()
				q := &quirkCard{Card: cd}
				q.generate = func(mgmt []byte, s card.Slot, spec card.KeySpec) (crypto.PublicKey, error) {
					got, err := cd.Generate(mgmt, s, spec)
					if err != nil {
						return nil, err
					}
					return c.answer(got), nil
				}
				s, _, err := startQuirk(t, q, factoryDefaults)
				if err != nil {
					t.Fatal(err)
				}
				res, err := s.Ensure(context.Background(), connect.NewRequest(&keyv1alpha1.EnsureRequest{Purpose: p.name, Algorithm: p.algorithm}))
				pk, perr := s.PublicKey(context.Background(), connect.NewRequest(&keyv1alpha1.PublicKeyRequest{Purpose: p.name}))
				if c.ok {
					if err != nil || !res.Msg.GetCreated() || perr != nil || !bytes.Equal(res.Msg.GetSpkiDer(), pk.Msg.GetSpkiDer()) {
						t.Fatalf("a right key was refused: %v, %v", err, perr)
					}
					return
				}
				wantRefused(t, err, connect.CodeFailedPrecondition, "card")
				if connect.CodeOf(perr) != connect.CodeNotFound {
					t.Errorf("a key that was refused is served: %v", perr)
				}
			})
		}
	}
}
