package yubikeypiv_test

import (
	"context"
	"crypto"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/common/refusal"
	keyv1alpha1 "github.com/truvity/atoom/key/gen/atoom/key/v1alpha1"
	"github.com/truvity/atoom/key/pivattest/attesttest"
	"github.com/truvity/atoom/key/yubikeypiv"
	"github.com/truvity/atoom/key/yubikeypiv/internal/card"
	"github.com/truvity/atoom/key/yubikeypiv/internal/card/cardtest"
)

// A test PKI of this file's own, made by code (TEST ONLY: nothing here is a vendor certificate), whose root, PIV intermediate, attestation
// certificate (f9) and slot certificates can be made in every shape the strict checks of the driver must tell apart, and a runner that puts
// one slot certificate through both paths of the driver: the adoption of a key (a card whose slot 9a answers with it) and the check of a
// peer's evidence (VerifyAttestation). Both read the certificates with the real library (card.NewAttestationVerifier).

var (
	qpFrom = time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC)
	qpTo   = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
)

type qpWorld struct {
	root, piv, f9, f9OfRoot *x509.Certificate
	rootKey, pivKey, f9Key  *ecdsa.PrivateKey
	verify                  func(att, slot *x509.Certificate) (card.Attestation, error)
	serial                  uint32
}

func qpKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func qpMint(t *testing.T, tmpl, issuer *x509.Certificate, pub crypto.PublicKey, key *ecdsa.PrivateKey) *x509.Certificate {
	t.Helper()
	if issuer == nil {
		issuer = tmpl
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, issuer, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func qpSerialExt(t *testing.T, n uint32) pkix.Extension {
	t.Helper()
	b, err := asn1.Marshal(int64(n))
	if err != nil {
		t.Fatal(err)
	}
	return pkix.Extension{Id: oidSer, Value: b}
}

// qpStates are the extensions of a slot certificate that states what the plan of slot 9a asks: firmware 5.8.0, the serial, PIN once, touch never.
func qpStates(t *testing.T, serial uint32) []pkix.Extension {
	t.Helper()
	return []pkix.Extension{{Id: oidFW, Value: []byte{5, 8, 0}}, qpSerialExt(t, serial), {Id: oidPol, Value: []byte{2, 1}}}
}

func qpPEM(cs ...*x509.Certificate) []byte {
	var b []byte
	for _, c := range cs {
		b = append(b, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})...)
	}
	return b
}

// newQPWorld makes the PKI. When rootStates, the root is a certificate that names slot 9a and states what the plan asks; when pivStates, the
// PIV intermediate states firmware, serial and policy (its name cannot be a slot's: an intermediate that is admitted is named "Yubico PIV
// Attestation ...").
func newQPWorld(t *testing.T, serial uint32, rootStates, pivStates bool) *qpWorld {
	t.Helper()
	w := &qpWorld{rootKey: qpKey(t), pivKey: qpKey(t), f9Key: qpKey(t), serial: serial}
	ca := func(n int64, cn string, ext []pkix.Extension) *x509.Certificate {
		return &x509.Certificate{
			SerialNumber: big.NewInt(n), Subject: pkix.Name{CommonName: cn}, NotBefore: qpFrom, NotAfter: qpTo,
			IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, MaxPathLen: -1, ExtraExtensions: ext,
		}
	}
	rootCN, rootExt, pivExt := "TEST ONLY Yubico Attestation Root 1", []pkix.Extension(nil), []pkix.Extension(nil)
	if rootStates {
		rootCN, rootExt = "YubiKey PIV Attestation 9a", qpStates(t, serial)
	}
	if pivStates {
		pivExt = qpStates(t, serial)
	}
	rt := ca(1, rootCN, rootExt)
	w.root = qpMint(t, rt, nil, &w.rootKey.PublicKey, w.rootKey)
	w.piv = qpMint(t, ca(2, "Yubico PIV Attestation TEST ONLY 1", pivExt), w.root, &w.pivKey.PublicKey, w.rootKey)
	w.f9 = qpMint(t, ca(3, "TEST ONLY YubiKey PIV Attestation F9", nil), w.piv, &w.f9Key.PublicKey, w.pivKey)
	w.f9OfRoot = qpMint(t, ca(4, "TEST ONLY YubiKey PIV Attestation F9 under the root", nil), w.root, &w.f9Key.PublicKey, w.rootKey)
	sum := sha256.Sum256(w.root.Raw)
	v, err := card.NewAttestationVerifier(qpPEM(w.root), qpPEM(w.piv), hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	w.verify = v
	return w
}

// qpSlot says how a slot certificate is made. The zero value is the good one: named slot 9a, stating what the plan asks, a fresh P-384 key,
// signed by the card's attestation certificate.
type qpSlot struct {
	cn     string
	ext    []pkix.Extension // nil: the stated ones
	none   bool             // no extension at all
	key    string           // "" (P-384), "p256", "p521", "rsa2048", "ed25519", "x25519", "unknown" (an algorithm the standard library does not read)
	by     string           // "" (f9), "piv", "root", "f9-of-root"
	filler int              // bytes of an extension that holds nothing, to make a certificate of a size
}

var qpOIDFiller = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 1}

// qpPub makes a public key of the kind, and for the kinds that the standard library cannot write into a certificate, the bytes that stand in
// for it and the bytes that replace them in the signed part.
func qpPub(t *testing.T, kind string) (pub crypto.PublicKey, old, repl []byte) {
	t.Helper()
	switch kind {
	case "", "p384":
		return &qpKey(t).PublicKey, nil, nil
	case "p256":
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return &k.PublicKey, nil, nil
	case "p521":
		k, err := ecdsa.GenerateKey(elliptic.P521(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return &k.PublicKey, nil, nil
	case "rsa2048":
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		return &k.PublicKey, nil, nil
	case "ed25519":
		p, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return p, nil, nil
	case "x25519", "unknown":
		p, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		old, err = x509.MarshalPKIXPublicKey(p)
		if err != nil {
			t.Fatal(err)
		}
		if kind == "unknown" {
			repl = append([]byte{}, old...)
			// the last byte of the algorithm identifier 1.3.101.112 (2b 65 70) becomes 7f: an algorithm that nothing knows
			i := strings.Index(string(repl), "\x2b\x65\x70")
			if i < 0 {
				t.Fatal("the identifier of Ed25519 is not in its key")
			}
			repl[i+2] = 0x7f
			return p, old, repl
		}
		xk, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		repl, err = x509.MarshalPKIXPublicKey(xk.PublicKey())
		if err != nil {
			t.Fatal(err)
		}
		return p, old, repl
	}
	t.Fatalf("no key of the kind %q", kind)
	return nil, nil, nil
}

// slot makes the slot certificate and the key it holds.
func (w *qpWorld) slot(t *testing.T, o qpSlot) (*x509.Certificate, crypto.PublicKey) {
	t.Helper()
	cn := o.cn
	if cn == "" {
		cn = "YubiKey PIV Attestation 9a"
	}
	ext := o.ext
	if ext == nil && !o.none {
		ext = qpStates(t, w.serial)
	}
	if o.filler > 0 {
		ext = append(append([]pkix.Extension{}, ext...), pkix.Extension{Id: qpOIDFiller, Value: make([]byte, o.filler)})
	}
	pub, old, repl := qpPub(t, o.key)
	issuer, key := w.f9, w.f9Key
	switch o.by {
	case "piv":
		issuer, key = w.piv, w.pivKey
	case "root":
		issuer, key = w.root, w.rootKey
	case "f9-of-root":
		issuer = w.f9OfRoot
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(100), Subject: pkix.Name{CommonName: cn}, NotBefore: qpFrom, NotAfter: qpTo, ExtraExtensions: ext}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, issuer, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	if old != nil {
		if der, err = attesttest.ReplaceInTBS(der, old, repl, key); err != nil {
			t.Fatal(err)
		}
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	if old == nil {
		return c, pub
	}
	return c, c.PublicKey // nil for an algorithm that the standard library does not read
}

// sizedSlot makes a slot certificate of exactly n bytes (an ECDSA signature changes by a byte or two from one signature to the next).
func (w *qpWorld) sizedSlot(t *testing.T, n int, o qpSlot) *x509.Certificate {
	t.Helper()
	o.filler = n - 1000
	for range 400 {
		c, _ := w.slot(t, o)
		if len(c.Raw) == n {
			return c
		}
		o.filler += n - len(c.Raw)
	}
	t.Fatalf("no slot certificate of %d bytes", n)
	return nil
}

// sizedF9 makes an attestation certificate of exactly n bytes, with the same key as the card's, and returns it as the PKI's own.
func (w *qpWorld) sizedF9(t *testing.T, n int) *qpWorld {
	t.Helper()
	fill := n - 1000
	for range 400 {
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: "TEST ONLY YubiKey PIV Attestation F9"}, NotBefore: qpFrom, NotAfter: qpTo,
			IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, MaxPathLen: -1,
			ExtraExtensions: []pkix.Extension{{Id: qpOIDFiller, Value: make([]byte, fill)}},
		}
		c := qpMint(t, tmpl, w.piv, &w.f9Key.PublicKey, w.pivKey)
		if len(c.Raw) == n {
			q := *w
			q.f9 = c
			return &q
		}
		fill += n - len(c.Raw)
	}
	t.Fatalf("no attestation certificate of %d bytes", n)
	return nil
}

// qpOut is what a path of the driver answered for a certificate.
type qpOut struct {
	err  error
	word string // adoption: the problem after "slots.9a.attestation: "; peer: the "error" of the journal line of a refused attestation
	rec  *logRecorder
	spki []byte // the key that the driver took: adoption, the key served for the purpose; peer, the key in the answer
}

func (o qpOut) accepted() bool { return o.err == nil }

// qpAdopt puts a slot certificate through the adoption of a key: a card whose slot 9a answers with it, and whose attestation certificate is att.
func qpAdopt(t *testing.T, w *qpWorld, att, slot *x509.Certificate) qpOut {
	t.Helper()
	return qpAdoptAt(t, w, att, 0x9a, "token-signing", slot)
}

// qpAdoptAt is qpAdopt for the slot and the purpose given.
func qpAdoptAt(t *testing.T, w *qpWorld, att *x509.Certificate, slotNo card.Slot, purpose string, slot *x509.Certificate) qpOut {
	t.Helper()
	c := cardtest.New()
	c.SerialNo = w.serial
	q := &quirkCard{Card: c, verify: w.verify, attCert: att}
	q.attest = func(s card.Slot) (*x509.Certificate, error) {
		if s == slotNo {
			return slot, nil
		}
		return c.Attest(s)
	}
	s, rec, err := startQuirk(t, q, factoryDefaults)
	out := qpOut{err: err, rec: rec}
	if err != nil {
		var p *yubikeypiv.Problems
		if !errors.As(err, &p) || len(p.List) != 1 || !strings.HasPrefix(p.List[0], "slots."+slotNo.String()+".attestation: ") {
			t.Fatalf("the refusal is not one problem of slot %s: %v", slotNo, err)
		}
		out.word = strings.TrimPrefix(p.List[0], "slots."+slotNo.String()+".attestation: ")
		return out
	}
	res, err := s.PublicKey(context.Background(), connect.NewRequest(&keyv1alpha1.PublicKeyRequest{Purpose: purpose}))
	if err != nil {
		out.err = err
		out.word = "no key served: " + err.Error()
		return out
	}
	out.spki = res.Msg.GetSpkiDer()
	return out
}

// qpPeerRaw puts the bytes of two certificates through the check of a peer's evidence.
func qpPeerRaw(t *testing.T, w *qpWorld, slotDER, signerDER []byte) qpOut {
	t.Helper()
	q := &quirkCard{Card: cardtest.New(), verify: w.verify}
	s, rec, err := startQuirk(t, q, factoryDefaults)
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.VerifyAttestation(context.Background(), connect.NewRequest(&keyv1alpha1.VerifyAttestationRequest{Attestation: slotDER, AttestationSigner: signerDER}))
	out := qpOut{err: err, rec: rec}
	for _, l := range rec.got() {
		if l.msg == "a peer's attestation refused" && len(l.attrs) > 0 {
			out.word = l.attrs[0].Value.String()
		}
	}
	if err == nil {
		out.spki = res.Msg.GetSpkiDer()
	}
	return out
}

func qpPeer(t *testing.T, w *qpWorld, signer, slot *x509.Certificate) qpOut {
	t.Helper()
	return qpPeerRaw(t, w, slot.Raw, signer.Raw)
}

// qpWantPeerRefusal says that the peer path refused for a certificate that is not an attestation: the code, the reason and the field are the
// refusal of every attestation that does not verify.
func qpWantPeerRefusal(t *testing.T, name string, o qpOut) {
	t.Helper()
	if o.err == nil {
		t.Fatalf("%s: the peer path accepted", name)
	}
	if connect.CodeOf(o.err) != connect.CodeFailedPrecondition {
		t.Fatalf("%s: code %s (%v)", name, connect.CodeOf(o.err), o.err)
	}
	if r, _ := refusal.Reason(o.err); r != "attestation" {
		t.Fatalf("%s: reason %q", name, r)
	}
	if f, _ := refusal.FieldPath(o.err); f != "attestation" {
		t.Fatalf("%s: field %q", name, f)
	}
}

// qpLeaks lists what a refusal's words hold of the certificate or of the card: the key, the serial, the certificate's bytes or names.
func qpLeaks(text string, c *x509.Certificate, serial uint32) []string {
	var found []string
	chk := func(what, s string) {
		if s != "" && strings.Contains(text, s) {
			found = append(found, what)
		}
	}
	chk("the key (hex)", hex.EncodeToString(c.RawSubjectPublicKeyInfo[len(c.RawSubjectPublicKeyInfo)-24:]))
	chk("the key (base64)", base64.StdEncoding.EncodeToString(c.RawSubjectPublicKeyInfo)[10:50])
	chk("the certificate (base64)", base64.StdEncoding.EncodeToString(c.Raw)[40:80])
	chk("the serial", fmt.Sprint(serial))
	chk("the name", c.Subject.CommonName)
	chk("the signature (hex)", hex.EncodeToString(c.Signature[:16]))
	return found
}
