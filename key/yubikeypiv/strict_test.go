package yubikeypiv_test

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"log/slog"
	"math/big"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/common/refusal"
	keyv1alpha1 "github.com/truvity/atoom/key/gen/atoom/key/v1alpha1"
	"github.com/truvity/atoom/key/pivattest"
	"github.com/truvity/atoom/key/yubikeypiv"
	"github.com/truvity/atoom/key/yubikeypiv/internal/card"
	"github.com/truvity/atoom/key/yubikeypiv/internal/card/cardtest"
)

// The driver believes an attestation only when it is one: the library's Generated on both paths, each fact stated before it is compared, and
// the key of the signed bytes. The tests run without the build tag, over the fake card and, where the fake cannot make the input, over a
// small card of the test's own.

// quirkCard is the fake with some answers replaced.
type quirkCard struct {
	*cardtest.Card
	verify   func(att, slot *x509.Certificate) (card.Attestation, error)
	attest   func(card.Slot) (*x509.Certificate, error) // when set, the certificate of a slot
	attCert  *x509.Certificate                          // when set, the card's attestation certificate
	generate func(mgmt []byte, s card.Slot, spec card.KeySpec) (crypto.PublicKey, error)
}

func (q *quirkCard) VerifyAttestation(att, slot *x509.Certificate) (card.Attestation, error) {
	if q.verify != nil {
		return q.verify(att, slot)
	}
	return q.Card.VerifyAttestation(att, slot)
}

func (q *quirkCard) Attest(s card.Slot) (*x509.Certificate, error) {
	if q.attest != nil {
		return q.attest(s)
	}
	return q.Card.Attest(s)
}

func (q *quirkCard) AttestationCertificate() (*x509.Certificate, error) {
	if q.attCert != nil {
		return q.attCert, nil
	}
	return q.Card.AttestationCertificate()
}

func (q *quirkCard) Generate(mgmt []byte, s card.Slot, spec card.KeySpec) (crypto.PublicKey, error) {
	if q.generate != nil {
		return q.generate(mgmt, s, spec)
	}
	return q.Card.Generate(mgmt, s, spec)
}

func quirkOpener(q *quirkCard) card.Opener {
	open := q.Opener()
	return func(sel card.Selector) (card.Card, error) {
		if _, err := open(sel); err != nil {
			return nil, err
		}
		return q, nil
	}
}

// startQuirk opens the driver over the card and returns what New returned.
func startQuirk(t *testing.T, q *quirkCard, factory bool) (*yubikeypiv.Service, *logRecorder, error) {
	t.Helper()
	rec := &logRecorder{}
	s, err := yubikeypiv.New(yubikeypiv.Options{
		Purposes: refPurposes(), Serial: q.SerialNo, AllowFactoryDefaults: factory, Open: quirkOpener(q), Logger: slog.New(rec),
	})
	if err == nil {
		t.Cleanup(func() { _ = s.Close() })
	}
	return s, rec, err
}

func fakeVerifier(t *testing.T, c *cardtest.Card) func(att, slot *x509.Certificate) (card.Attestation, error) {
	t.Helper()
	rootPEM, bundlePEM, hash, err := c.Anchors()
	if err != nil {
		t.Fatal(err)
	}
	v, err := card.NewAttestationVerifier(rootPEM, bundlePEM, hash)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func wantProblem(t *testing.T, err error, word string) {
	t.Helper()
	var p *yubikeypiv.Problems
	if !errors.As(err, &p) || len(p.List) != 1 || !strings.HasPrefix(p.List[0], word) {
		t.Fatalf("err = %v, want one problem that begins %q", err, word)
	}
}

var (
	oidFW  = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 41482, 3, 3}
	oidSer = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 41482, 3, 7}
	oidPol = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 41482, 3, 8}
)

// anchorAsSlot is a PKI in which the root is itself a certificate that states everything the plan of slot 9a asks (the name of the slot, the
// firmware 5.8.0, the serial, PIN once and touch never) and holds a P-384 key. Given as the slot certificate it is a chain of one: the chain
// check passes, with whatever signer, and every comparison of the old driver passes. Only Generated is false.
type anchorAsSlot struct {
	root, piv *x509.Certificate
	rootKey   *ecdsa.PrivateKey
	verify    func(att, slot *x509.Certificate) (card.Attestation, error)
}

func newAnchorAsSlot(t *testing.T, serial uint32) *anchorAsSlot {
	t.Helper()
	from, to := time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	rootKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ser, err := asn1.Marshal(int64(serial))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "YubiKey PIV Attestation 9a"}, NotBefore: from, NotAfter: to,
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
		ExtraExtensions: []pkix.Extension{{Id: oidFW, Value: []byte{5, 8, 0}}, {Id: oidSer, Value: ser}, {Id: oidPol, Value: []byte{2, 1}}},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	root, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pivKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pivTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "Yubico PIV Attestation TEST ONLY 1"}, NotBefore: from, NotAfter: to,
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	pder, err := x509.CreateCertificate(rand.Reader, pivTmpl, root, &pivKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	piv, err := x509.ParseCertificate(pder)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(root.Raw)
	pemOf := func(c *x509.Certificate) []byte {
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
	}
	verify, err := card.NewAttestationVerifier(pemOf(root), pemOf(piv), hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	return &anchorAsSlot{root: root, piv: piv, rootKey: rootKey, verify: verify}
}

// oldDriverAccepts is the comparison part of the driver's adopt before it held Generated and the facts stated: the firmware floor, the serial,
// the slot and both policies, on the reported values only.
func oldDriverAccepts(a card.Attestation, serial uint32, slot card.Slot, spec card.KeySpec) bool {
	return a.Version.AtLeast(yubikeypiv.FirmwareFloor.Major, yubikeypiv.FirmwareFloor.Minor, yubikeypiv.FirmwareFloor.Patch) &&
		a.Serial == serial && a.Slot == slot && a.PIN == spec.PIN && a.Touch == spec.Touch
}

// verifies: REQ-KEYS-022, REQ-KEYS-026
func TestATrustAnchorGivenAsTheSlotCertificateIsRefusedByAdopt(t *testing.T) {
	c := cardtest.New()
	p := newAnchorAsSlot(t, c.SerialNo)
	spec := card.KeySpec{Algorithm: card.EC384, PIN: card.PINOnce, Touch: card.TouchNever}
	if _, err := c.PutKey(0x9a, spec); err != nil {
		t.Fatal(err)
	}
	q := &quirkCard{Card: c, verify: p.verify, attCert: p.piv, attest: func(s card.Slot) (*x509.Certificate, error) {
		if s != 0x9a {
			return c.Attest(s)
		}
		return p.root, nil
	}}
	// The input, read by the driver's reading: the chain passes, every fact is stated and equal to the plan's, the key is a P-384 key.
	a, err := p.verify(p.piv, p.root)
	if err != nil {
		t.Fatal(err)
	}
	if !oldDriverAccepts(a, c.SerialNo, 0x9a, spec) || !a.Evidence.HasPolicy || a.MissingFact() != "" || a.Evidence.Generated {
		t.Fatalf("the input is not the one this test means: %+v", a)
	}
	if _, ok := a.Evidence.PublicKey.(*ecdsa.PublicKey); !ok {
		t.Fatal("the key is not an EC key")
	}
	// The old driver took exactly that: its comparisons pass and the key came from the certificate. The driver now refuses it for its origin.
	_, rec, err := startQuirk(t, q, factoryDefaults)
	wantProblem(t, err, "slots.9a.attestation: origin")
	l := rec.line(t, "attestation refused")
	if l.level != slog.LevelWarn || strings.Contains(strings.Join(l.keys(), " "), "certificate") {
		t.Errorf("the journal line: %v", l)
	}
	if !c.Closed() {
		t.Fatal("the refused card was left open")
	}
}

// The peer path: the same input is refused, with the refusal of every attestation that does not verify.
func TestATrustAnchorGivenAsAPeersAttestationIsRefused(t *testing.T) {
	c := cardtest.New()
	p := newAnchorAsSlot(t, c.SerialNo)
	q := &quirkCard{Card: c, verify: p.verify}
	s, rec, err := startQuirk(t, q, factoryDefaults)
	if err != nil {
		t.Fatal(err)
	}
	a, err := p.verify(p.piv, p.root)
	if err != nil || a.Version.AtLeast(5, 7, 0) == false || a.Evidence.Generated {
		t.Fatalf("the input is not the one this test means: %+v, %v", a, err)
	}
	// With the signer the card makes, and with the root itself as signer, and the PIV intermediate: whoever the signer is.
	for name, signer := range map[string]*x509.Certificate{"the PIV intermediate": p.piv, "the root itself": p.root} {
		_, err = s.VerifyAttestation(context.Background(), connect.NewRequest(&keyv1alpha1.VerifyAttestationRequest{Attestation: p.root.Raw, AttestationSigner: signer.Raw}))
		if connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Fatalf("%s: %v", name, err)
		}
		wantRefused(t, err, connect.CodeFailedPrecondition, "attestation")
	}
	line := rec.line(t, "a peer's attestation refused")
	if got := line.attrs[0].Value.String(); !strings.HasPrefix(got, "origin:") {
		t.Errorf("the journal says %q", got)
	}
}

// Each fact the attestation does not state, in turn: the firmware, the serial, the slot and the policy. Refused for what it is, on adoption (a
// slot that the name does not give is refused as origin: the library says Generated only for a slot it read).
func TestAFactThatTheAttestationDoesNotStateIsRefusedByAdopt(t *testing.T) {
	spec := card.KeySpec{Algorithm: card.EC384, PIN: card.PINOnce, Touch: card.TouchNever}
	for _, word := range []string{"firmware", "serial", "slot", "policy"} {
		t.Run(word, func(t *testing.T) {
			c := cardtest.New()
			c.AttestOmits = []string{word}
			if _, err := c.PutKey(0x9a, spec); err != nil {
				t.Fatal(err)
			}
			_, _, err := startQuirk(t, &quirkCard{Card: c}, factoryDefaults)
			// A slot that the name does not give is also no generated key for the library (Generated needs the slot), so that is the refusal.
			want := word + ": the attestation does not state it"
			if word == "slot" {
				want = "origin: "
			}
			wantProblem(t, err, "slots.9a.attestation: "+want)
			if !c.Closed() {
				t.Fatal("the refused card was left open")
			}
		})
	}
	t.Run("nothing omitted", func(t *testing.T) {
		c := cardtest.New()
		if _, err := c.PutKey(0x9a, spec); err != nil {
			t.Fatal(err)
		}
		if _, _, err := startQuirk(t, &quirkCard{Card: c}, factoryDefaults); err != nil {
			t.Fatalf("a good attestation: %v", err)
		}
	})
}

// On the peer path a fact that is not stated would be reported as zero ("00" for the slot, serial 0, the policy "always"): it is refused.
func TestAFactThatAPeersAttestationDoesNotStateIsRefused(t *testing.T) {
	for _, word := range []string{"firmware", "serial", "slot", "policy"} {
		t.Run(word, func(t *testing.T) {
			b := newBoundary(t)
			b.locked.card.AttestOmits = []string{word}
			att, err := b.locked.key.Attest(context.Background(), connect.NewRequest(&keyv1alpha1.AttestRequest{Purpose: "pin-release"}))
			if err != nil {
				t.Fatalf("Attest: %v", err)
			}
			_, err = b.releaser.key.VerifyAttestation(context.Background(), connect.NewRequest(&keyv1alpha1.VerifyAttestationRequest{
				Attestation: att.Msg.GetAttestation(), AttestationSigner: att.Msg.GetAttestationSigner(),
			}))
			wantRefused(t, err, connect.CodeFailedPrecondition, "attestation")
		})
	}
}

// The key of the driver is the key of the signed bytes. A card that hands over a parsed certificate whose field PublicKey was changed (the
// bytes are the original's) gets the key of the bytes; the driver before took the field.
func TestTheKeyOfAKeyIsTheOneOfTheSignedBytes(t *testing.T) {
	spec := card.KeySpec{Algorithm: card.EC384, PIN: card.PINOnce, Touch: card.TouchNever}
	c := cardtest.New()
	signed, err := c.PutKey(0x9a, spec)
	if err != nil {
		t.Fatal(err)
	}
	other, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	q := &quirkCard{Card: c, verify: fakeVerifier(t, c)}
	q.attest = func(s card.Slot) (*x509.Certificate, error) {
		cert, err := c.Attest(s)
		if err != nil {
			return nil, err
		}
		swapped := *cert
		swapped.PublicKey = &other.PublicKey // the field of the parsed value, not the signed bytes
		return &swapped, nil
	}
	s, _, err := startQuirk(t, q, factoryDefaults)
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.PublicKey(context.Background(), connect.NewRequest(&keyv1alpha1.PublicKeyRequest{Purpose: "token-signing"}))
	if err != nil {
		t.Fatal(err)
	}
	want, err := x509.MarshalPKIXPublicKey(signed)
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Msg.GetSpkiDer()) != string(want) {
		t.Fatal("the driver took the key of the field of the parsed certificate, not the key of its signed bytes")
	}
}

// A key that the card hands back from Generate and that the attestation does not certify is refused (Evidence.AttestsKey).
func TestAKeyThatIsNotTheKeyTheAttestationCertifiesIsRefused(t *testing.T) {
	c := cardtest.New()
	other, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	q := &quirkCard{Card: c, verify: fakeVerifier(t, c)}
	q.generate = func(mgmt []byte, s card.Slot, spec card.KeySpec) (crypto.PublicKey, error) {
		if _, err := c.Generate(mgmt, s, spec); err != nil {
			return nil, err
		}
		return &other.PublicKey, nil // not the key of the slot
	}
	s, _, err := startQuirk(t, q, factoryDefaults)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Ensure(context.Background(), connect.NewRequest(&keyv1alpha1.EnsureRequest{Purpose: "token-signing", Algorithm: "ES384"}))
	wantRefused(t, err, connect.CodeFailedPrecondition, "card")
	if _, err := s.PublicKey(context.Background(), connect.NewRequest(&keyv1alpha1.PublicKeyRequest{Purpose: "token-signing"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("a key that was refused is served: %v", err)
	}
}

// A peer's bytes over the limit of the library are refused before they are parsed. The certificate here is a valid one that x509 parses (the
// driver before parsed it, and the library then refused it, with a line in the journal for a refused attestation): now the refusal is the one
// of bytes that are no certificate, from the field that holds them, and the check of the library is never reached.
func TestAPeersCertificateOverTheLimitIsRefusedBeforeItIsParsed(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "an oversize certificate"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		ExtraExtensions: []pkix.Extension{{Id: asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 1}, Value: make([]byte, pivattest.MaxCertificateSize)}},
	}
	big1, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(big1) <= pivattest.MaxCertificateSize {
		t.Fatalf("the certificate has %d bytes", len(big1))
	}
	if _, err := x509.ParseCertificate(big1); err != nil {
		t.Fatalf("x509 does not parse it, so the test shows nothing: %v", err)
	}
	c := cardtest.New()
	s, rec, err := startQuirk(t, &quirkCard{Card: c}, factoryDefaults)
	if err != nil {
		t.Fatal(err)
	}
	small := attRaw(t, c)
	for field, req := range map[string]*keyv1alpha1.VerifyAttestationRequest{
		"attestation":        {Attestation: big1, AttestationSigner: small},
		"attestation_signer": {Attestation: small, AttestationSigner: big1},
	} {
		_, err := s.VerifyAttestation(context.Background(), connect.NewRequest(req))
		wantRefused(t, err, connect.CodeInvalidArgument, "attestation")
		if path, _ := refusal.FieldPath(err); path != field {
			t.Errorf("the field is %q, want %q", path, field)
		}
	}
	for _, l := range rec.got() {
		if l.msg == "a peer's attestation refused" {
			t.Error("the check of the library was reached")
		}
	}
}

// attRaw is the card's own attestation certificate, DER: a small certificate that parses.
func attRaw(t *testing.T, c *cardtest.Card) []byte {
	t.Helper()
	open, err := c.Opener()(card.Selector{})
	if err != nil {
		t.Fatal(err)
	}
	cert, err := open.AttestationCertificate()
	if err != nil {
		t.Fatal(err)
	}
	return cert.Raw
}
