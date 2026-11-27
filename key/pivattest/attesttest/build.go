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
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"fmt"
	"maps"
	"math"
	"slices"
	"time"
)

// The validity window of every certificate of the corpus that is meant to be valid, and the windows of the ones that are not: long enough
// that a check at the current time and a check at Corpus.At agree for decades.
var (
	validFrom   = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	validTo     = time.Date(2099, 12, 31, 0, 0, 0, 0, time.UTC)
	expiredTo   = time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	notYetFrom  = time.Date(2098, 1, 1, 0, 0, 0, 0, time.UTC)
	corpusAt    = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	slotKeyBits = []int{2048, 3072, 4096}
)

type builder struct {
	c    *Corpus
	err  error
	good *PKI
	rsa  *PKI
}

func (b *builder) fail(err error) {
	if b.err == nil && err != nil {
		b.err = err
	}
}

func (b *builder) pki(o PKIOptions) *PKI {
	o.NotBefore, o.NotAfter = validFrom, validTo
	p, err := NewPKI(o)
	b.fail(err)
	return p
}

func (b *builder) withF9(p *PKI, o F9Options) *PKI {
	if o.NotBefore.IsZero() {
		o.NotBefore = validFrom
	}
	if o.NotAfter.IsZero() {
		o.NotAfter = validTo
	}
	q, err := p.WithF9(o)
	b.fail(err)
	return q
}

func (b *builder) slot(p *PKI, o SlotOptions) []byte {
	if o.NotBefore.IsZero() {
		o.NotBefore = validFrom
	}
	if o.NotAfter.IsZero() {
		o.NotAfter = validTo
	}
	der, err := p.SlotDER(o)
	b.fail(err)
	return der
}

// add appends a case; the verifier is the PKI named pki.
func (b *builder) add(name, why, pki string, slot, signer []byte) {
	for _, c := range b.c.Cases {
		if c.Name == name {
			b.fail(fmt.Errorf("attesttest: the case %q is made twice", name))
		}
	}
	b.c.Cases = append(b.c.Cases, Case{Name: name, Why: why, PKI: pki, Slot: slot, Signer: signer})
}

func (b *builder) construct(name, why string, rootPEM, bundlePEM []byte, wantRootSHA string) {
	b.c.Constructions = append(b.c.Constructions, Construction{Name: name, Why: why, RootPEM: string(rootPEM), BundlePEM: string(bundlePEM), WantRootSHA256: wantRootSHA})
}

// with returns the default extensions with the one of oid replaced by e, or dropped when e is nil.
func with(oid asn1.ObjectIdentifier, e *pkix.Extension) []pkix.Extension {
	var out []pkix.Extension
	for _, d := range DefaultExtensions() {
		if d.Id.Equal(oid) {
			if e == nil {
				continue
			}
			d = *e
		}
		out = append(out, d)
	}
	return out
}

func flip(der []byte, at int) []byte {
	out := bytes.Clone(der)
	out[at] ^= 0x01
	return out
}

// flipText changes one bit of the byte of the first occurrence of text in der at offset at (inside the signed part). The bit is the lowest,
// which keeps a letter or a digit a printable character.
func (b *builder) flipText(der []byte, text string, at int) []byte {
	i := bytes.Index(der, []byte(text))
	if i < 0 {
		b.fail(fmt.Errorf("attesttest: %q is not in the certificate", text))
		return der
	}
	return flip(der, i+at)
}

// Build makes the corpus: the certificates of every case, with no result filled in. The certificates are new on every call (the keys are
// generated), so a rebuilt corpus needs its results from the old check again. The committed corpus is the one the tests replay.
func Build() (*Corpus, error) {
	b := &builder{c: &Corpus{GoldenSource: "none: the results are not filled in yet", At: corpusAt, PKIs: map[string]PKIData{}}}
	b.good = b.pki(PKIOptions{})
	b.rsa = b.pki(PKIOptions{RSA: true})
	for name, p := range map[string]*PKI{"good": b.good, "rsa": b.rsa} { // a map of the output: its order does not matter
		b.c.PKIs[name] = PKIData{RootPEM: string(p.RootPEM()), BundlePEM: string(p.BundlePEM()), RootSHA256: p.RootSHA256()}
	}
	if b.err != nil {
		return nil, b.err
	}
	b.keyCases()
	b.policyCases()
	b.slotNameCases()
	b.extensionCases()
	b.chainCases()
	b.constructionCases()
	if b.err != nil {
		return nil, b.err
	}
	return b.c, nil
}

func (b *builder) keyCases() {
	g := b.good
	f9 := g.F9.Raw
	pubs := map[string]crypto.PublicKey{}
	curves := map[string]elliptic.Curve{"ec-p224": elliptic.P224(), "ec-p256": elliptic.P256(), "ec-p384": elliptic.P384(), "ec-p521": elliptic.P521()}
	for _, name := range slices.Sorted(maps.Keys(curves)) {
		curve := curves[name]
		k, err := ecdsa.GenerateKey(curve, rand.Reader)
		b.fail(err)
		pubs[name] = &k.PublicKey
	}
	for _, bits := range slotKeyBits {
		k, err := rsa.GenerateKey(rand.Reader, bits)
		b.fail(err)
		pubs[fmt.Sprintf("rsa-%d", bits)] = &k.PublicKey
		if bits == 2048 {
			pubs["rsa-2048-e3"] = &rsa.PublicKey{N: k.N, E: 3}
		}
	}
	ed, _, err := ed25519.GenerateKey(rand.Reader)
	b.fail(err)
	pubs["ed25519"] = ed
	x, err := ecdh.X25519().GenerateKey(rand.Reader)
	b.fail(err)
	pubs["x25519"] = x.PublicKey()
	for _, name := range []string{"ec-p224", "ec-p256", "ec-p384", "ec-p521", "rsa-2048", "rsa-2048-e3", "rsa-3072", "rsa-4096", "ed25519", "x25519"} {
		b.add("key-"+name, "a slot certificate for a key of this kind verifies, whatever the key", "good", b.slot(g, SlotOptions{PublicKey: pubs[name]}), f9)
	}
}

func (b *builder) policyCases() {
	g := b.good
	f9 := g.F9.Raw
	for pin := byte(0); pin <= 6; pin++ {
		for touch := byte(0); touch <= 4; touch++ {
			e := Policy(pin, touch)
			b.add(fmt.Sprintf("policy-pin%d-touch%d", pin, touch), "the PIN policy 1 to 5 and the touch policy 1 to 3 are read; any other value is refused", "good",
				b.slot(g, SlotOptions{Extensions: with(OIDPolicy, &e)}), f9)
		}
	}
	for _, n := range []int{0, 1, 3, 4} {
		e := Ext(OIDPolicy, false, bytes.Repeat([]byte{2}, n))
		b.add(fmt.Sprintf("policy-length-%d", n), "the policy extension is exactly two bytes", "good", b.slot(g, SlotOptions{Extensions: with(OIDPolicy, &e)}), f9)
	}
}

func (b *builder) slotNameCases() {
	g := b.good
	f9 := g.F9.Raw
	names := []string{"9a", "9c", "9d", "9e", "f9", "80", "81", "96", "97", "00", "0", "ff", "9A", "9C", "09a", "009a", "0000009a", "00000000009a", "100000009a", "ffffffff",
		"100000000", "0x9a", " 9a", "9a ", "9a\n", "9a9c", "+9a", "-9a", "9_a", "", "gg", "9g"}
	for s := 0x82; s <= 0x95; s++ {
		names = append(names, fmt.Sprintf("%x", s))
	}
	for _, n := range names {
		b.add("slot-name-"+fmt.Sprintf("%q", n), "the slot is the hexadecimal number after the prefix of the common name; only 9a, 9c, 9d, 9e and 82 to 95 name a slot", "good",
			b.slot(g, SlotOptions{CommonName: "YubiKey PIV Attestation " + n}), f9)
	}
	for _, n := range []string{"yubikey piv attestation 9a", "YubiKey PIV Attestation", "YubiKey PIV Attestation9a", "YubiKey  PIV Attestation 9a", "TEST ONLY 9a",
		"Yubico PIV Attestation 9a", "YubiKey PIV Attestation 9a 9c", "9a"} {
		b.add("slot-prefix-"+fmt.Sprintf("%q", n), "a common name without the exact prefix names no slot, and is no error", "good", b.slot(g, SlotOptions{CommonName: n}), f9)
	}
}

func (b *builder) extensionCases() {
	g := b.good
	f9 := g.F9.Raw
	add := func(name, why string, ext []pkix.Extension) {
		b.add(name, why, "good", b.slot(g, SlotOptions{Extensions: ext}), f9)
	}
	for _, n := range []int{0, 1, 2, 4, 5} {
		e := Ext(OIDFirmware, false, bytes.Repeat([]byte{5}, n))
		add(fmt.Sprintf("firmware-length-%d", n), "the firmware extension is exactly three bytes", with(OIDFirmware, &e))
	}
	for _, v := range [][3]byte{{5, 8, 0}, {5, 7, 0}, {5, 7, 3}, {5, 4, 3}, {0, 0, 0}, {255, 255, 255}} {
		e := Firmware(v[0], v[1], v[2])
		add(fmt.Sprintf("firmware-%d.%d.%d", v[0], v[1], v[2]), "any three bytes are a version; the floor is the caller's", with(OIDFirmware, &e))
	}
	serials := map[string][]byte{
		"zero": nil, "one": nil, "typical": nil, "max-32": nil, "2-pow-32": nil, "2-pow-32-plus-5": nil, "max-64": nil, "minus-1": nil, "minus-2-pow-31": nil,
		"non-minimal":              {0x02, 0x02, 0x00, 0x05},
		"trailing-bytes":           {0x02, 0x01, 0x05, 0xaa, 0xbb},
		"trailing-integer":         {0x02, 0x01, 0x05, 0x02, 0x01, 0x07},
		"wrong-tag-octets":         {0x04, 0x01, 0x05},
		"wrong-tag-utf8":           {0x0c, 0x01, 0x35},
		"wrong-tag-sequence":       {0x30, 0x03, 0x02, 0x01, 0x05},
		"empty":                    {},
		"zero-length-integer":      {0x02, 0x00},
		"truncated":                {0x02, 0x01},
		"long-form-length":         {0x02, 0x81, 0x01, 0x05},
		"too-large-for-64":         {0x02, 0x09, 0x01, 0, 0, 0, 0, 0, 0, 0},
		"2-pow-63":                 {0x02, 0x09, 0x00, 0x80, 0, 0, 0, 0, 0, 0, 0},
		"raw-bytes-no-tag":         {0x00, 0x12, 0xd6, 0x87},
		"five-byte-positive":       {0x02, 0x05, 0x01, 0x00, 0x00, 0x00, 0x05},
		"high-bit-needs-padding":   {0x02, 0x01, 0x85},
		"indefinite-length":        {0x02, 0x80, 0x05, 0x00, 0x00},
		"leading-0xff-non-minimal": {0x02, 0x02, 0xff, 0x85},
	}
	nums := map[string]int64{"zero": 0, "one": 1, "typical": 1234567, "max-32": math.MaxUint32, "2-pow-32": 1 << 32, "2-pow-32-plus-5": 1<<32 + 5,
		"max-64": math.MaxInt64, "minus-1": -1, "minus-2-pow-31": -1 << 31}
	for _, name := range slices.Sorted(maps.Keys(serials)) {
		raw := serials[name]
		e := Ext(OIDSerial, false, raw)
		if n, ok := nums[name]; ok {
			e = Serial(n)
		}
		add("serial-"+name, "the serial is an ASN.1 integer that fits 64 bits and is not negative; bytes after it are ignored; it is cut to 32 bits", with(OIDSerial, &e))
	}
	for _, n := range []int{0, 2} {
		e := Ext(OIDFormFactor, false, bytes.Repeat([]byte{4}, n))
		add(fmt.Sprintf("formfactor-length-%d", n), "the form factor extension is exactly one byte", with(OIDFormFactor, &e))
	}
	for _, v := range []byte{0, 1, 2, 3, 4, 5, 6, 0x80, 0x81, 0x85, 0xff} {
		e := FormFactor(v)
		add(fmt.Sprintf("formfactor-%#x", v), "any one byte is a form factor", with(OIDFormFactor, &e))
	}
	for _, oid := range []asn1.ObjectIdentifier{OIDFirmware, OIDSerial, OIDPolicy, OIDFormFactor} {
		var e pkix.Extension
		for _, d := range DefaultExtensions() {
			if d.Id.Equal(oid) {
				e = d
				e.Critical = true
			}
		}
		add("critical-"+oid.String(), "a vendor extension marked critical is not one the standard library handles: the chain is refused", with(oid, &e))
		add("absent-"+oid.String(), "an extension that is absent is no error: its value is zero", with(oid, nil))
	}
	add("absent-all", "a slot certificate with none of the four extensions verifies, and states nothing", []pkix.Extension{})
	unknown := Ext(asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 41482, 3, 99}, false, []byte{1, 2, 3})
	add("unknown-vendor-extension", "an extension of the vendor's arc that is not read is ignored", append(DefaultExtensions(), unknown))
	other := Ext(asn1.ObjectIdentifier{1, 2, 3, 4}, false, []byte{9})
	add("unknown-other-extension", "an extension of another arc that is not critical is ignored", append(DefaultExtensions(), other))
	other.Critical = true
	add("unknown-critical-extension", "an unknown critical extension is refused by the chain check", append(DefaultExtensions(), other))
	badFW, badSer := Ext(OIDFirmware, false, []byte{1}), Ext(OIDSerial, false, []byte{4})
	badPol, badFF := Ext(OIDPolicy, false, []byte{9, 9}), Ext(OIDFormFactor, false, nil)
	add("order-firmware-then-serial", "the first malformed extension in the order of the certificate is the error", []pkix.Extension{badFW, badSer})
	add("order-serial-then-firmware", "the first malformed extension in the order of the certificate is the error", []pkix.Extension{badSer, badFW})
	add("order-policy-then-formfactor", "the first malformed extension in the order of the certificate is the error", []pkix.Extension{badPol, badFF})
	add("order-formfactor-then-policy", "the first malformed extension in the order of the certificate is the error", []pkix.Extension{badFF, badPol})
	add("order-good-then-bad", "an extension before the bad one is read but the call returns the error only", []pkix.Extension{Firmware(5, 8, 0), badPol})
}

func (b *builder) chainCases() {
	g, r := b.good, b.rsa
	f9 := g.F9.Raw
	def := SlotOptions{}
	b.add("chain-good", "the vendor's shape: slot, f9, PIV intermediate", "good", b.slot(g, def), f9)
	b.add("chain-good-rsa", "the same with RSA keys and PKCS#1 v1.5 signatures", "rsa", b.slot(r, def), r.F9.Raw)

	// the Basic Constraints of f9
	noBC := b.withF9(g, F9Options{NoBasicConstraints: true})
	b.add("f9-no-basic-constraints", "a signer without Basic Constraints is made a CA on a copy (the workaround of the older generation)", "good", b.slot(noBC, def), noBC.F9.Raw)
	notCA := b.withF9(g, F9Options{NotCA: true})
	b.add("f9-not-ca", "a signer whose Basic Constraints say it is not a CA is refused", "good", b.slot(notCA, def), notCA.F9.Raw)
	zero := b.withF9(g, F9Options{MaxPathLenZero: true})
	b.add("f9-pathlen-zero", "a path length of 0 on the signer allows a slot certificate below it", "good", b.slot(zero, def), zero.F9.Raw)
	noSign := b.withF9(g, F9Options{NoCertSign: true})
	b.add("f9-no-cert-sign", "a signer whose key usage lacks certificate signing is refused", "good", b.slot(noSign, def), noSign.F9.Raw)
	eku := b.withF9(g, F9Options{ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	b.add("f9-eku-server-auth", "any extended key usage is accepted on the signer", "good", b.slot(eku, def), eku.F9.Raw)
	ekuCode := b.withF9(g, F9Options{ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning, x509.ExtKeyUsageClientAuth}})
	b.add("f9-eku-code-client", "any extended key usage is accepted on the signer", "good", b.slot(ekuCode, def), ekuCode.F9.Raw)
	critF9 := b.withF9(g, F9Options{Extensions: []pkix.Extension{Ext(asn1.ObjectIdentifier{1, 2, 3, 4}, true, []byte{1})}})
	b.add("f9-unknown-critical-extension", "an unknown critical extension on the signer is refused", "good", b.slot(critF9, def), critF9.F9.Raw)
	noBCrsa := b.withF9(r, F9Options{NoBasicConstraints: true})
	b.add("f9-no-basic-constraints-rsa", "the workaround with an RSA signer", "rsa", b.slot(noBCrsa, def), noBCrsa.F9.Raw)

	// the extended key usage of the slot certificate: the check asks for any
	b.add("slot-eku-server-auth", "any extended key usage is accepted on the slot certificate", "good", b.slot(g, SlotOptions{ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}), f9)
	b.add("slot-eku-client-and-code", "any extended key usage is accepted on the slot certificate", "good", b.slot(g, SlotOptions{ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageCodeSigning}}), f9)
	b.add("slot-eku-unknown", "an extended key usage the standard library does not know is accepted", "good", b.slot(g, SlotOptions{UnknownExtKeyUsage: []asn1.ObjectIdentifier{{1, 2, 3, 4, 5}}}), f9)
	b.add("slot-eku-any", "the any extended key usage", "good", b.slot(g, SlotOptions{ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}), f9)

	// who signed f9
	byRoot := b.withF9(g, F9Options{SignedBy: "root"})
	b.add("f9-signed-by-root", "a chain that ends in the root itself is accepted, because the root is an anchor", "good", b.slot(byRoot, def), byRoot.F9.Raw)
	byInter := b.withF9(g, F9Options{SignedBy: "inter"})
	b.add("f9-signed-by-the-intermediate-above", "an intermediate that is not a PIV attestation intermediate is not an anchor, and the chain does not reach the root through it", "good", b.slot(byInter, def), byInter.F9.Raw)
	byFIDO := b.withF9(g, F9Options{SignedBy: "fido"})
	b.add("f9-signed-by-a-fido-intermediate", "a FIDO intermediate under the same root is not an anchor", "good", b.slot(byFIDO, def), byFIDO.F9.Raw)
	b.add("signer-is-the-piv-intermediate", "a slot certificate signed by the PIV intermediate itself, with that as the signer", "good", b.slot(g, SlotOptions{SignedWith: g.pivKey, Issuer: g.PIV}), g.PIV.Raw)
	other := b.pki(PKIOptions{})
	b.add("signer-is-another-card", "the signer is another card's f9 under the same PIV intermediate name but another PKI", "good", b.slot(g, def), other.F9.Raw)
	b.add("signer-and-slot-swapped", "the arguments in the wrong order", "good", f9, b.slot(g, def))
	b.add("signer-is-the-intermediate", "the signer is the PIV intermediate, but the slot certificate is signed by f9", "good", b.slot(g, def), g.PIV.Raw)

	// another PKI
	b.add("rogue-pki-same-names", "a PKI with the same names and its own keys", "good", b.slot(other, def), other.F9.Raw)
	rogueRSA := b.pki(PKIOptions{RSA: true})
	b.add("rogue-pki-rsa", "a rogue RSA PKI against the EC verifier", "good", b.slot(rogueRSA, def), rogueRSA.F9.Raw)
	b.add("good-chain-against-the-other-verifier", "a chain of the EC PKI against the verifier of the RSA PKI", "rsa", b.slot(g, def), f9)

	// time
	b.add("slot-expired", "a slot certificate past its validity", "good", b.slot(g, SlotOptions{NotBefore: validFrom, NotAfter: expiredTo}), f9)
	b.add("slot-not-yet-valid", "a slot certificate before its validity", "good", b.slot(g, SlotOptions{NotBefore: notYetFrom, NotAfter: validTo}), f9)
	expF9 := b.withF9(g, F9Options{NotBefore: validFrom, NotAfter: expiredTo})
	b.add("f9-expired", "an attestation certificate past its validity", "good", b.slot(expF9, def), expF9.F9.Raw)
	newF9 := b.withF9(g, F9Options{NotBefore: notYetFrom, NotAfter: validTo})
	b.add("f9-not-yet-valid", "an attestation certificate before its validity", "good", b.slot(newF9, def), newF9.F9.Raw)

	// signature algorithms
	ecAlgs := map[string]x509.SignatureAlgorithm{"sha256": x509.ECDSAWithSHA256, "sha384": x509.ECDSAWithSHA384, "sha512": x509.ECDSAWithSHA512}
	for _, name := range slices.Sorted(maps.Keys(ecAlgs)) {
		alg := ecAlgs[name]
		b.add("slot-signature-ecdsa-"+name, "the hash of an ECDSA signature may be SHA-256, SHA-384 or SHA-512", "good", b.slot(g, SlotOptions{SignatureAlgorithm: alg}), f9)
	}
	rsaAlgs := map[string]x509.SignatureAlgorithm{"rsa-sha256": x509.SHA256WithRSA, "rsa-sha384": x509.SHA384WithRSA, "rsa-pss-sha256": x509.SHA256WithRSAPSS, "rsa-sha1": x509.SHA1WithRSA}
	for _, name := range slices.Sorted(maps.Keys(rsaAlgs)) {
		alg := rsaAlgs[name]
		b.add("slot-signature-"+name, "RSA signatures: PKCS#1 v1.5 and PSS verify; SHA-1 is refused", "rsa", b.slot(r, SlotOptions{SignatureAlgorithm: alg}), r.F9.Raw)
	}
	sha1F9 := b.withF9(r, F9Options{SignatureAlgorithm: x509.SHA1WithRSA})
	b.add("f9-signature-sha1", "an attestation certificate signed with SHA-1 is refused", "rsa", b.slot(sha1F9, def), sha1F9.F9.Raw)

	// tampering
	good := b.slot(g, def)
	b.add("tamper-slot-signature", "a flipped bit in the signature of the slot certificate", "good", flip(good, len(good)-1), f9)
	b.add("tamper-slot-common-name", "a flipped bit in the signed part of the slot certificate", "good", b.flipText(good, "YubiKey PIV Attestation 9a", 0), f9)
	b.add("tamper-slot-9a-to-9c", "the slot named in the certificate edited without a new signature", "good", b.flipTextTo(good, "9a", "9c"), f9)
	b.add("tamper-f9-signature", "a flipped bit in the signature of the attestation certificate", "good", good, flip(f9, len(f9)-1))
	b.add("tamper-f9-common-name", "a flipped bit in the signed part of the attestation certificate", "good", good, b.flipText(f9, "Attestation F9", len("Attestation F9")-1))
	// edited and signed again by the card's own key: the signature is right, so the extensions are what the certificate states.
	ek, ok := g.f9Key.(*ecdsa.PrivateKey)
	if !ok {
		b.fail(fmt.Errorf("attesttest: the f9 key is not ECDSA"))
		return
	}
	nine := b.slot(g, SlotOptions{CommonName: "YubiKey PIV Attestation 9a"})
	edited, err := ReplaceInTBS(nine, []byte("Attestation 9a"), []byte("Attestation 9c"), ek)
	b.fail(err)
	b.add("edited-and-signed-again", "a slot certificate edited and signed again by the card's own key verifies: the check proves who signed, not what the card did", "good", edited, f9)
}

// flipTextTo replaces the last occurrence of old with repl, without a new signature.
func (b *builder) flipTextTo(der []byte, old, repl string) []byte {
	i := bytes.LastIndex(der, []byte(old))
	if i < 0 || len(old) != len(repl) {
		b.fail(fmt.Errorf("attesttest: cannot replace %q", old))
		return der
	}
	out := bytes.Clone(der)
	copy(out[i:], repl)
	return out
}

func (b *builder) constructionCases() {
	g := b.good
	root, bundle, sum := g.RootPEM(), g.BundlePEM(), g.RootSHA256()
	other := b.pki(PKIOptions{})
	b.construct("baseline", "a root, a bundle with a PIV intermediate, the right digest", root, bundle, sum)
	signedByRoot := b.pki(PKIOptions{PIVSignedByRoot: true})
	b.construct("piv-intermediate-directly-under-the-root", "no intermediate in between", signedByRoot.RootPEM(), PEM(signedByRoot.PIV), signedByRoot.RootSHA256())
	b.construct("bundle-without-a-piv-intermediate", "only an intermediate and a FIDO intermediate", root, PEM(g.Inter, g.FIDO), sum)
	b.construct("bundle-empty", "no certificate at all", root, nil, sum)
	b.construct("bundle-without-the-link", "the PIV intermediate cannot be chained to the root without the intermediate in between", root, PEM(g.FIDO, g.PIV), sum)
	fido := b.pki(PKIOptions{PIVName: "Yubico FIDO Attestation TEST ONLY 1"})
	b.construct("piv-named-like-fido", "an intermediate whose name is not the PIV one is not admitted", fido.RootPEM(), PEM(fido.Inter, fido.PIV), fido.RootSHA256())
	b.construct("bundle-of-another-root", "a PIV intermediate that does not chain to the pinned root", root, other.BundlePEM(), sum)
	b.construct("bundle-with-another-roots-intermediate-too", "the good one is admitted, the other is not", root, append(append([]byte{}, bundle...), other.BundlePEM()...), sum)
	b.construct("trailing-garbage", "bytes after the last PEM block are ignored", root, append(append([]byte{}, bundle...), []byte("trailing text that is not PEM\n")...), sum)
	b.construct("garbage-before", "text before the first PEM block is ignored", root, append([]byte("a note before the blocks\n"), bundle...), sum)
	junk := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte{1, 2, 3}})
	b.construct("a-block-that-is-not-a-certificate", "a PEM block of another type is skipped", root, append(append([]byte{}, junk...), bundle...), sum)
	badCert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte{1, 2, 3}})
	b.construct("a-certificate-that-does-not-parse", "a CERTIFICATE block that is not a certificate is an error", root, append(append([]byte{}, bundle...), badCert...), sum)
	b.construct("root-file-with-two-certificates", "the root file must hold exactly one certificate", PEM(g.Root, g.Inter), bundle, sum)
	b.construct("root-file-empty", "no certificate in the root file", nil, bundle, sum)
	b.construct("root-file-garbage", "no PEM in the root file", []byte("not a pem file"), bundle, sum)
	b.construct("root-file-bad-certificate", "the root file holds a certificate that does not parse", badCert, bundle, sum)
	b.construct("root-file-with-another-block", "a block of another type beside the one certificate is skipped", append(append([]byte{}, junk...), root...), bundle, sum)
	b.construct("root-digest-of-another-certificate", "the digest does not match the root", root, bundle, SHA256(g.Inter))
	b.construct("root-digest-upper-case", "the digest is compared as lower-case text", root, bundle, upper(sum))
	b.construct("root-digest-empty", "no digest", root, bundle, "")
	b.construct("root-is-the-intermediate", "the root file holds the intermediate: it is the one certificate, and the PIV intermediate chains to it", PEM(g.Inter), PEM(g.PIV), SHA256(g.Inter))
	exp := b.pki(PKIOptions{PIVNotBefore: validFrom, PIVNotAfter: expiredTo})
	b.construct("piv-intermediate-expired", "an expired intermediate is not admitted", exp.RootPEM(), exp.BundlePEM(), exp.RootSHA256())
	early := b.pki(PKIOptions{PIVNotBefore: notYetFrom, PIVNotAfter: validTo})
	b.construct("piv-intermediate-not-yet-valid", "an intermediate that is not valid yet is not admitted", early.RootPEM(), early.BundlePEM(), early.RootSHA256())
}

func upper(s string) string {
	return string(bytes.ToUpper([]byte(s)))
}
