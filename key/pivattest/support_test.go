package pivattest_test

import (
	"crypto"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/truvity/atoom/key/pivattest"
	"github.com/truvity/atoom/key/pivattest/attesttest"
)

// Fixed times for the tests of the chain: a test never reads the clock of the machine to decide an outcome. The window of a test PKI is
// the year 2010 to 2099 unless a test says otherwise (so every verifier is built at any current time of this century).
var (
	t2010 = time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC)
	t2015 = time.Date(2015, 1, 1, 0, 0, 0, 0, time.UTC)
	t2020 = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	t2030 = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	t2040 = time.Date(2040, 1, 1, 0, 0, 0, 0, time.UTC)
	t2050 = time.Date(2050, 1, 1, 0, 0, 0, 0, time.UTC)
	t2099 = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
)

// fixedPKI is a test PKI valid from 2010 to 2099.
func fixedPKI(t *testing.T, o attesttest.PKIOptions) *attesttest.PKI {
	t.Helper()
	if o.NotBefore.IsZero() {
		o.NotBefore = t2010
	}
	if o.NotAfter.IsZero() {
		o.NotAfter = t2099
	}
	p, err := attesttest.NewPKI(o)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// verifierOf is the verifier over a test PKI.
func verifierOf(t *testing.T, p *attesttest.PKI) *pivattest.Verifier {
	t.Helper()
	v, err := pivattest.NewVerifierFromPEM(p.RootPEM(), p.BundlePEM(), p.RootSHA256())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// fixedSlot is a slot certificate of p valid from 2010 to 2099 unless the options say otherwise.
func fixedSlot(t *testing.T, p *attesttest.PKI, o attesttest.SlotOptions) *x509.Certificate {
	t.Helper()
	if o.NotBefore.IsZero() {
		o.NotBefore = t2010
	}
	if o.NotAfter.IsZero() {
		o.NotAfter = t2099
	}
	c, err := p.Slot(o)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

var (
	keysOnce sync.Once
	keysMap  map[string]crypto.PublicKey
	keysErr  error
)

// slotKeys are public keys of every kind a card holds (and the kinds a card does not), taken from the corpus so that the tests do not
// generate a 4096-bit key. The names are those of the key cases of the corpus without the prefix.
func slotKeys(t *testing.T) map[string]crypto.PublicKey {
	t.Helper()
	keysOnce.Do(func() {
		c, err := attesttest.Load()
		if err != nil {
			keysErr = err
			return
		}
		keysMap = map[string]crypto.PublicKey{}
		for _, cs := range c.Cases {
			const prefix = "key-"
			if len(cs.Name) <= len(prefix) || cs.Name[:len(prefix)] != prefix {
				continue
			}
			cert, err := x509.ParseCertificate(cs.Slot)
			if err != nil {
				keysErr = err
				return
			}
			pub := cert.PublicKey
			if pub == nil {
				if pub, err = x509.ParsePKIXPublicKey(cert.RawSubjectPublicKeyInfo); err != nil {
					keysErr = err
					return
				}
			}
			keysMap[cs.Name[len(prefix):]] = pub
		}
	})
	if keysErr != nil {
		t.Fatal(keysErr)
	}
	return keysMap
}

// sameKey says whether two public keys are the same key of the same type.
func sameKey(a, b crypto.PublicKey) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	k, ok := a.(interface{ Equal(crypto.PublicKey) bool })
	return ok && k.Equal(b)
}

// copyCorpus is a deep copy of a corpus, through its JSON form.
func copyCorpus(t *testing.T, c *attesttest.Corpus) *attesttest.Corpus {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	out, err := attesttest.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A small PKI of the test's own, with no use of the test-support package: a root, a PIV-named intermediate directly under it, and as many
// attestation certificates (f9) as the test asks for, each with a key of its own.
type ownPKI struct {
	root, piv *x509.Certificate
	rootKey   *ecdsa.PrivateKey
	pivKey    *ecdsa.PrivateKey
}

type ownCard struct {
	f9  *x509.Certificate
	key *ecdsa.PrivateKey
}

func ownSerial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 100))
	if err != nil {
		panic(err)
	}
	return n.Add(n, big.NewInt(1))
}

func ownCert(t *testing.T, tmpl, parent *x509.Certificate, pub crypto.PublicKey, key crypto.Signer) *x509.Certificate {
	t.Helper()
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func ownCA(cn string) *x509.Certificate {
	return &x509.Certificate{
		SerialNumber: ownSerial(), Subject: pkix.Name{CommonName: cn}, NotBefore: t2010, NotAfter: t2099,
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
}

func newOwnPKI(t *testing.T) *ownPKI {
	t.Helper()
	rootKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pivKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rt := ownCA("TEST ONLY Own Root")
	root := ownCert(t, rt, rt, &rootKey.PublicKey, rootKey)
	piv := ownCert(t, ownCA("Yubico PIV Attestation TEST ONLY Own"), root, &pivKey.PublicKey, rootKey)
	return &ownPKI{root: root, piv: piv, rootKey: rootKey, pivKey: pivKey}
}

func (p *ownPKI) verifier(t *testing.T) *pivattest.Verifier {
	t.Helper()
	v, err := pivattest.NewVerifierFromPEM(attesttest.PEM(p.root), attesttest.PEM(p.piv), attesttest.SHA256(p.root))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// extraPIV is one more certificate under the root, with a key of its own and the name given.
func (p *ownPKI) extraPIV(t *testing.T, name string) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return ownCert(t, ownCA(name), p.root, &k.PublicKey, p.rootKey), k
}

// card makes the attestation certificate of one card: its own key, the same name as every other card's.
func (p *ownPKI) card(t *testing.T, ext ...pkix.Extension) *ownCard {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := ownCA("TEST ONLY YubiKey PIV Attestation F9")
	tmpl.ExtraExtensions = ext
	f9 := ownCert(t, tmpl, p.piv, &k.PublicKey, p.pivKey)
	return &ownCard{f9: f9, key: k}
}

// slot makes a slot certificate signed by this card, for a key of its own (an ECDSA P-256 key).
func (c *ownCard) slot(t *testing.T, cn string, ext ...pkix.Extension) *x509.Certificate {
	t.Helper()
	cert, _ := c.slotKey(t, cn, ext...)
	return cert
}

// slotKey is slot with the private key of the attested key.
func (c *ownCard) slotKey(t *testing.T, cn string, ext ...pkix.Extension) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: ownSerial(), Subject: pkix.Name{CommonName: cn}, NotBefore: t2010, NotAfter: t2099, ExtraExtensions: ext}
	return ownCert(t, tmpl, c.f9, &k.PublicKey, c.key), k
}

// independentAlgorithm names the kind of a key in the words of the package, by a switch of the test's own.
func independentAlgorithm(pub crypto.PublicKey) pivattest.Algorithm {
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		if k.Curve == elliptic.P256() {
			return pivattest.AlgEC256
		}
		if k.Curve == elliptic.P384() {
			return pivattest.AlgEC384
		}
	case *rsa.PublicKey:
		if k.E != 65537 {
			return pivattest.AlgUnknown
		}
		switch k.N.BitLen() {
		case 2048:
			return pivattest.AlgRSA2048
		case 3072:
			return pivattest.AlgRSA3072
		case 4096:
			return pivattest.AlgRSA4096
		}
	case ed25519.PublicKey:
		return pivattest.AlgEd25519
	case *ecdh.PublicKey:
		if k.Curve() == ecdh.X25519() {
			return pivattest.AlgX25519
		}
	}
	return pivattest.AlgUnknown
}

func newKey() (*ecdsa.PrivateKey, error) { return ecdsa.GenerateKey(elliptic.P256(), rand.Reader) }
