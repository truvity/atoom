// Package attesttest is TEST-ONLY support for the tests of programs that read a YubiKey attestation with the package pivattest, and for the
// tests of pivattest itself. No program imports it; nothing in it is a vendor certificate, and every certificate it makes is signed by a key
// it generates, under names that say TEST ONLY.
//
// It has two parts. A PKI builder (NewPKI, PKI.Slot) makes a root, a PIV attestation intermediate, a card's attestation certificate (f9)
// and slot certificates with the vendor's extensions, to the shape the vendor's chain has, with options for each way the chain or an
// extension can be made wrong. The corpus (Corpus, Load, Build) is a set of such certificates as committed data, each case with the result
// that the check this library is equal to gave: a program that reads attestations replays it against its own code.
package attesttest

import (
	"bytes"
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
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"
)

// The vendor's extensions of a slot certificate.
var (
	OIDFirmware   = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 41482, 3, 3}
	OIDSerial     = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 41482, 3, 7}
	OIDPolicy     = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 41482, 3, 8}
	OIDFormFactor = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 41482, 3, 9}
)

// Ext makes an extension with a raw value.
func Ext(oid asn1.ObjectIdentifier, critical bool, value []byte) pkix.Extension {
	return pkix.Extension{Id: oid, Critical: critical, Value: value}
}

// Firmware is the firmware extension for a version.
func Firmware(major, minor, patch byte) pkix.Extension {
	return Ext(OIDFirmware, false, []byte{major, minor, patch})
}

// Serial is the serial number extension holding n as an ASN.1 integer.
func Serial(n int64) pkix.Extension {
	b, err := asn1.Marshal(n)
	if err != nil {
		panic(err) // an int64 always marshals
	}
	return Ext(OIDSerial, false, b)
}

// Policy is the policy extension with a PIN policy byte and a touch policy byte.
func Policy(pin, touch byte) pkix.Extension { return Ext(OIDPolicy, false, []byte{pin, touch}) }

// FormFactor is the form factor extension.
func FormFactor(b byte) pkix.Extension { return Ext(OIDFormFactor, false, []byte{b}) }

// DefaultExtensions are the four extensions of a slot certificate of a card with firmware 5.8.0: serial 1234567, PIN once, touch never,
// USB-C nano.
func DefaultExtensions() []pkix.Extension {
	return []pkix.Extension{Firmware(5, 8, 0), Serial(1234567), Policy(2, 1), FormFactor(4)}
}

// PKIOptions shape a test PKI. The zero value is a valid PKI of P-384 keys whose certificates are valid from an hour ago for a day.
type PKIOptions struct {
	NotBefore, NotAfter time.Time // of every certificate of the PKI; zero: an hour ago and a day ahead
	RSA                 bool      // RSA-2048 keys and RSA PKCS#1 v1.5 signatures instead of P-384 keys

	PIVName                   string    // the common name of the PIV intermediate; default "Yubico PIV Attestation TEST ONLY 1"
	PIVNotBefore, PIVNotAfter time.Time // of the PIV intermediate only; zero: those of the PKI
	PIVSignedByRoot           bool      // the PIV intermediate is signed by the root, not by the intermediate between
	F9                        F9Options
}

// F9Options shape the card's attestation certificate.
type F9Options struct {
	CommonName          string // default "TEST ONLY YubiKey PIV Attestation F9"
	SignedBy            string // "piv" (default), "root", "inter" (the non-PIV intermediate between) or "fido" (a FIDO attestation intermediate)
	NoBasicConstraints  bool   // the certificate states no Basic Constraints (a card of an older generation)
	NotCA               bool   // Basic Constraints with CA false
	MaxPathLenZero      bool   // Basic Constraints with a path length of 0
	NoCertSign          bool   // the key usage is digital signature only
	NotBefore, NotAfter time.Time
	ExtKeyUsage         []x509.ExtKeyUsage
	Extensions          []pkix.Extension // added to the certificate (for example an unknown critical extension)
	SignatureAlgorithm  x509.SignatureAlgorithm
}

// PKI is a test PKI: a root, an intermediate under it, a FIDO-named intermediate and a PIV-named intermediate, the card's attestation
// certificate (f9), and the keys to sign slot certificates with.
type PKI struct {
	Root, Inter, FIDO, PIV, F9 *x509.Certificate

	rsa                                bool
	rootKey, interKey, fidoKey, pivKey crypto.Signer
	f9Key                              crypto.Signer
	slotKey                            crypto.PublicKey
}

func (o PKIOptions) window() (time.Time, time.Time) {
	nb, na := o.NotBefore, o.NotAfter
	if nb.IsZero() {
		nb = time.Now().Add(-time.Hour)
	}
	if na.IsZero() {
		na = time.Now().Add(24 * time.Hour)
	}
	return nb, na
}

func genKey(useRSA bool) (crypto.Signer, error) {
	if useRSA {
		return rsa.GenerateKey(rand.Reader, 2048)
	}
	return ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
}

func serialNumber() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 100))
	if err != nil {
		panic(err)
	}
	return n.Add(n, big.NewInt(1))
}

func sign(tmpl, parent *x509.Certificate, pub crypto.PublicKey, key crypto.Signer) (*x509.Certificate, error) {
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, pub, key)
	if err != nil {
		return nil, err
	}
	return x509.ParseCertificate(der)
}

func caTemplate(cn string, nb, na time.Time) *x509.Certificate {
	return &x509.Certificate{
		SerialNumber: serialNumber(), Subject: pkix.Name{CommonName: cn}, NotBefore: nb, NotAfter: na,
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
}

// NewPKI makes a test PKI.
func NewPKI(o PKIOptions) (*PKI, error) {
	nb, na := o.window()
	p := &PKI{rsa: o.RSA}
	var err error
	keys := []*crypto.Signer{&p.rootKey, &p.interKey, &p.fidoKey, &p.pivKey, &p.f9Key}
	for _, k := range keys {
		if *k, err = genKey(o.RSA); err != nil {
			return nil, err
		}
	}
	rt := caTemplate("TEST ONLY Yubico Attestation Root 1", nb, na)
	if p.Root, err = sign(rt, rt, p.rootKey.Public(), p.rootKey); err != nil {
		return nil, err
	}
	if p.Inter, err = sign(caTemplate("TEST ONLY Yubico Attestation Intermediate B 1", nb, na), p.Root, p.interKey.Public(), p.rootKey); err != nil {
		return nil, err
	}
	if p.FIDO, err = sign(caTemplate("TEST ONLY Yubico FIDO Attestation B 1", nb, na), p.Inter, p.fidoKey.Public(), p.interKey); err != nil {
		return nil, err
	}
	pivName := o.PIVName
	if pivName == "" {
		pivName = "Yubico PIV Attestation TEST ONLY 1"
	}
	pnb, pna := o.PIVNotBefore, o.PIVNotAfter
	if pnb.IsZero() {
		pnb = nb
	}
	if pna.IsZero() {
		pna = na
	}
	pivParent, pivParentKey := p.Inter, p.interKey
	if o.PIVSignedByRoot {
		pivParent, pivParentKey = p.Root, p.rootKey
	}
	if p.PIV, err = sign(caTemplate(pivName, pnb, pna), pivParent, p.pivKey.Public(), pivParentKey); err != nil {
		return nil, err
	}
	if p.F9, err = p.makeF9(o.F9, nb, na); err != nil {
		return nil, err
	}
	if p.slotKey, err = slotPublicKey(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *PKI) makeF9(o F9Options, nb, na time.Time) (*x509.Certificate, error) {
	cn := o.CommonName
	if cn == "" {
		cn = "TEST ONLY YubiKey PIV Attestation F9"
	}
	if !o.NotBefore.IsZero() {
		nb = o.NotBefore
	}
	if !o.NotAfter.IsZero() {
		na = o.NotAfter
	}
	t := &x509.Certificate{
		SerialNumber: serialNumber(), Subject: pkix.Name{CommonName: cn}, NotBefore: nb, NotAfter: na,
		IsCA: !o.NotCA, BasicConstraintsValid: !o.NoBasicConstraints, KeyUsage: x509.KeyUsageCertSign,
		MaxPathLenZero: o.MaxPathLenZero, ExtKeyUsage: o.ExtKeyUsage, ExtraExtensions: o.Extensions, SignatureAlgorithm: o.SignatureAlgorithm,
	}
	if o.MaxPathLenZero {
		t.MaxPathLen = 0
	} else {
		t.MaxPathLen = -1
	}
	if o.NoCertSign {
		t.KeyUsage = x509.KeyUsageDigitalSignature
	}
	parent, key := p.PIV, p.pivKey
	switch o.SignedBy {
	case "", "piv":
	case "root":
		parent, key = p.Root, p.rootKey
	case "inter":
		parent, key = p.Inter, p.interKey
	case "fido":
		parent, key = p.FIDO, p.fidoKey
	default:
		return nil, fmt.Errorf("attesttest: unknown signer %q", o.SignedBy)
	}
	return sign(t, parent, p.f9Key.Public(), key)
}

// WithF9 returns a copy of the PKI whose card attestation certificate is made with other options: the same key, so that slot certificates
// are signed as before, under another certificate. Its validity is an hour ago to a day ahead unless the options say otherwise.
func (p *PKI) WithF9(o F9Options) (*PKI, error) {
	f9, err := p.makeF9(o, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
	if err != nil {
		return nil, err
	}
	q := *p
	q.F9 = f9
	return &q, nil
}

// SlotOptions shape a slot certificate.
type SlotOptions struct {
	CommonName          string           // default "YubiKey PIV Attestation 9a"
	PublicKey           crypto.PublicKey // the attested key; default a P-256 key
	Extensions          []pkix.Extension // nil: DefaultExtensions; a non-nil empty slice: none
	NotBefore, NotAfter time.Time        // zero: an hour ago and a day ahead
	ExtKeyUsage         []x509.ExtKeyUsage
	UnknownExtKeyUsage  []asn1.ObjectIdentifier
	SignatureAlgorithm  x509.SignatureAlgorithm
	SignedWith          crypto.Signer     // default: the key of this PKI's f9
	Issuer              *x509.Certificate // the certificate of that key; default this PKI's f9
	SignedByPIV         bool              // signed by the PIV intermediate itself, the card's attestation certificate left out of the chain
}

// Slot makes a slot certificate signed by the card's attestation key of this PKI.
func (p *PKI) Slot(o SlotOptions) (*x509.Certificate, error) {
	der, err := p.SlotDER(o)
	if err != nil {
		return nil, err
	}
	return x509.ParseCertificate(der)
}

// SlotDER is Slot as a DER string. A public key that the standard library cannot put in a certificate (X25519) is put in by a patch of the
// DER and a new signature (the PKI's attestation key must be an ECDSA key then).
func (p *PKI) SlotDER(o SlotOptions) ([]byte, error) {
	var err error
	cn := o.CommonName
	if cn == "" {
		cn = "YubiKey PIV Attestation 9a"
	}
	nb, na := o.NotBefore, o.NotAfter
	if nb.IsZero() {
		nb = time.Now().Add(-time.Hour)
	}
	if na.IsZero() {
		na = time.Now().Add(24 * time.Hour)
	}
	ext := o.Extensions
	if ext == nil {
		ext = DefaultExtensions()
	}
	pub := o.PublicKey
	if pub == nil {
		pub = p.slotKey
	}
	x25519, isX := pub.(*ecdh.PublicKey)
	templatePub := pub
	var placeholder ed25519.PublicKey
	if isX {
		// The standard library cannot write an X25519 key into a certificate: write an Ed25519 key, whose SPKI differs in two bytes of the
		// algorithm identifier and in the key, and patch it below.
		if placeholder, _, err = ed25519.GenerateKey(rand.Reader); err != nil {
			return nil, err
		}
		templatePub = placeholder
	}
	key := o.SignedWith
	if key == nil {
		key = p.f9Key
	}
	t := &x509.Certificate{
		SerialNumber: serialNumber(), Subject: pkix.Name{CommonName: cn}, NotBefore: nb, NotAfter: na,
		ExtraExtensions: ext, ExtKeyUsage: o.ExtKeyUsage, UnknownExtKeyUsage: o.UnknownExtKeyUsage, SignatureAlgorithm: o.SignatureAlgorithm,
	}
	issuer := o.Issuer
	if o.SignedByPIV {
		issuer, key = p.PIV, p.pivKey
	}
	if issuer == nil {
		issuer = p.F9
	}
	der, err := x509.CreateCertificate(rand.Reader, t, issuer, templatePub, key)
	if err != nil {
		return nil, err
	}
	if !isX {
		return der, nil
	}
	ecKey, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("attesttest: an X25519 slot key needs an ECDSA attestation key")
	}
	old, err := x509.MarshalPKIXPublicKey(placeholder)
	if err != nil {
		return nil, err
	}
	spki, err := x509.MarshalPKIXPublicKey(x25519)
	if err != nil {
		return nil, err
	}
	return ReplaceInTBS(der, old, spki, ecKey)
}

// ReplaceInTBS returns the certificate der with the first occurrence of old in its to-be-signed part replaced by repl, which has the same
// length, signed again with key, an ECDSA key, with the signature algorithm the certificate names. It is how the corpus makes a
// certificate that the standard library cannot make, and a certificate with a field that was edited and signed again.
func ReplaceInTBS(der, old, repl []byte, key *ecdsa.PrivateKey) ([]byte, error) {
	if len(old) != len(repl) {
		return nil, fmt.Errorf("attesttest: the replacement must have the length of what it replaces")
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	tbs := bytes.Clone(c.RawTBSCertificate)
	i := bytes.Index(tbs, old)
	if i < 0 {
		return nil, fmt.Errorf("attesttest: the bytes to replace are not in the certificate")
	}
	copy(tbs[i:], repl)
	return resign(tbs, c.SignatureAlgorithm, key)
}

func resign(tbs []byte, alg x509.SignatureAlgorithm, key *ecdsa.PrivateKey) ([]byte, error) {
	var h crypto.Hash
	var oid asn1.ObjectIdentifier
	switch alg {
	case x509.ECDSAWithSHA256:
		h, oid = crypto.SHA256, asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 2}
	case x509.ECDSAWithSHA384:
		h, oid = crypto.SHA384, asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 3}
	case x509.ECDSAWithSHA512:
		h, oid = crypto.SHA512, asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 4}
	default:
		return nil, fmt.Errorf("attesttest: cannot sign again with %v", alg)
	}
	hh := h.New()
	hh.Write(tbs)
	sig, err := ecdsa.SignASN1(rand.Reader, key, hh.Sum(nil))
	if err != nil {
		return nil, err
	}
	return asn1.Marshal(struct {
		TBS asn1.RawValue
		Alg pkix.AlgorithmIdentifier
		Sig asn1.BitString
	}{asn1.RawValue{FullBytes: tbs}, pkix.AlgorithmIdentifier{Algorithm: oid}, asn1.BitString{Bytes: sig, BitLength: 8 * len(sig)}})
}

func slotPublicKey() (crypto.PublicKey, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return &k.PublicKey, nil
}

// RootPEM is the root as a PEM file, the form of the vendor's root file.
func (p *PKI) RootPEM() []byte { return PEM(p.Root) }

// BundlePEM is the bundle of intermediates as a PEM file: the intermediate under the root, a FIDO-named one and the PIV-named one.
func (p *PKI) BundlePEM() []byte { return PEM(p.Inter, p.FIDO, p.PIV) }

// RootSHA256 is the SHA-256 of the DER form of the root, in lower-case hexadecimal.
func (p *PKI) RootSHA256() string { return SHA256(p.Root) }

// Signer is the card's attestation key of this PKI.
func (p *PKI) Signer() crypto.Signer { return p.f9Key }

// PEM encodes certificates as PEM blocks.
func PEM(cs ...*x509.Certificate) []byte {
	var b []byte
	for _, c := range cs {
		b = append(b, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})...)
	}
	return b
}

// SHA256 is the SHA-256 of the DER form of a certificate, in lower-case hexadecimal.
func SHA256(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	return hex.EncodeToString(sum[:])
}
