package pivattest

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"slices"
	"strings"
	"sync"

	_ "embed" // the vendor's certificates below
)

// The two files below are the vendor's public files, byte for byte (docs/pins.md says where they come from and how a person checks them):
// the PEM of "Yubico Attestation Root 1" and the vendor's bundle of intermediates (15 certificates, of which the six of the pin table are
// the ones this package uses). A change to either file, or to the table, is a change to what a program that links this package trusts.
//
//go:embed certs/yubico-attestation-root-1.pem
var rootPEM []byte

//go:embed certs/yubico-intermediate.pem
var bundlePEM []byte

// Role is what a pinned certificate is for.
type Role string

// The roles of the pin table.
const (
	RoleRoot         Role = "root"            // the trust root
	RoleIntermediate Role = "intermediate"    // links a PIV attestation intermediate to the root; not an anchor
	RolePIV          Role = "piv-attestation" // signs the attestation key of a PIV applet; an anchor
)

// Pin is one vendor certificate this package knows by name and by the SHA-256 of its DER form.
type Pin struct {
	Name   string
	Role   Role
	SHA256 string // lower-case hexadecimal
}

// The pin table: exactly six certificates. An added or changed row needs the file, a row of docs/pins.md and a row of docs/provenance.md in
// the same change.
var pins = []Pin{
	{"Yubico Attestation Root 1", RoleRoot, "62760c6a6ef91679f454c8902b80fd009825b3f25da90f1fbace2ec6586cd5a8"},
	{"Yubico Attestation Intermediate A 1", RoleIntermediate, "4698a1d3389c3ec60016c216250f1d0439922832d65142327436376dc2942b55"},
	{"Yubico Attestation Intermediate B 1", RoleIntermediate, "d4cc3f456fdaf4e7812a21aab1dfe9d8e27d24e2fd2d6f21c9940109f0daa754"},
	{"Yubico PIV Attestation A 1", RolePIV, "6de693f05376f5d8ca29069261e1c8626c75d503bd2edbfd75354cad1f722870"},
	{"Yubico PIV Attestation B 1", RolePIV, "2d55b7998f4e42569d6d8fa382b6dc77d1dacf07358b19701163892922b17052"},
	{"Yubico PIV Attestation B2 1", RolePIV, "0c90b7d184a36edf50a35f9be935f0c5689bfdcfe5bdd073366cafb49061a440"},
}

// Pins returns a copy of the pin table.
func Pins() []Pin { return slices.Clone(pins) }

var (
	defaultOnce sync.Once
	defaultV    *Verifier
	defaultErr  error
)

// Default is the verifier over the vendor's pinned certificates, built once. It fails, and keeps failing, when an embedded certificate is
// not the one the pin table names by its SHA-256, when the root file holds more or fewer than one certificate, or when the PIV attestation
// intermediates that the bundle admits (below) are not exactly those of the table. So a changed file is refused at the first call, and
// nothing is admitted silently.
//
// The older vendor root is not pinned: see the package comment.
func Default() (*Verifier, error) {
	defaultOnce.Do(func() { defaultV, defaultErr = buildDefault() })
	return defaultV, defaultErr
}

func buildDefault() (*Verifier, error) { return buildFrom(rootPEM, bundlePEM, pins) }

// buildFrom is Default over the given files and table (the tests give others).
func buildFrom(rootPEM, bundlePEM []byte, pins []Pin) (*Verifier, error) {
	rootSHA := ""
	for _, p := range pins {
		if p.Role == RoleRoot {
			rootSHA = p.SHA256
		}
	}
	v, admitted, err := newVerifier(rootPEM, bundlePEM, rootSHA)
	if err != nil {
		return nil, err
	}
	// Every table row must be the one certificate of its name in the files, with the pinned digest.
	root, _ := parseCerts(rootPEM)
	bundle, _ := parseCerts(bundlePEM)
	byName := map[string][]string{}
	for _, c := range append(root, bundle...) {
		byName[c.Subject.CommonName] = append(byName[c.Subject.CommonName], digest(c))
	}
	var wantAdmitted []string
	for _, p := range pins {
		if got := byName[p.Name]; len(got) != 1 || got[0] != p.SHA256 {
			return nil, fmt.Errorf("%w: %q", ErrPinTable, p.Name)
		}
		if p.Role == RolePIV {
			wantAdmitted = append(wantAdmitted, p.SHA256)
		}
	}
	slices.Sort(wantAdmitted)
	slices.Sort(admitted)
	if !slices.Equal(wantAdmitted, admitted) {
		return nil, ErrAdmission
	}
	return v, nil
}

// NewVerifierFromPEM builds a verifier whose trust anchors are the one certificate of rootPEM, which must have the SHA-256 wantRootSHA256
// (lower-case hexadecimal), and every certificate of bundlePEM whose common name begins "Yubico PIV Attestation " and that chains to the
// root through the bundle at the current time (the admission rule). A bundle with no such certificate is refused. It is for a test PKI;
// Default is for programs.
//
// A PEM block that is not a CERTIFICATE is skipped, and bytes after the last block are ignored; a certificate that does not parse is an
// error (ErrRootCount for the root file, ErrParse for the bundle).
func NewVerifierFromPEM(rootPEM, bundlePEM []byte, wantRootSHA256 string) (*Verifier, error) {
	v, _, err := newVerifier(rootPEM, bundlePEM, wantRootSHA256)
	return v, err
}

// newVerifier also returns the SHA-256 of every admitted intermediate.
func newVerifier(rootPEM, bundlePEM []byte, wantRootSHA256 string) (*Verifier, []string, error) {
	roots, err := parseCerts(rootPEM)
	if err != nil || len(roots) != 1 {
		return nil, nil, ErrRootCount
	}
	if digest(roots[0]) != wantRootSHA256 {
		return nil, nil, ErrRootPin
	}
	bundle, err := parseCerts(bundlePEM)
	if err != nil {
		return nil, nil, &classed{class: ErrParse, cause: err, text: "pivattest: a certificate of the bundle does not parse: " + err.Error()}
	}
	rootPool, interPool := x509.NewCertPool(), x509.NewCertPool()
	rootPool.AddCert(roots[0])
	for _, c := range bundle {
		interPool.AddCert(c)
	}
	anchors := x509.NewCertPool()
	anchors.AddCert(roots[0])
	var admitted []string
	for _, c := range bundle {
		if !strings.HasPrefix(c.Subject.CommonName, pivIntermediatePrefix) {
			continue
		}
		if _, err := c.Verify(x509.VerifyOptions{Roots: rootPool, Intermediates: interPool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
			continue // not signed through the pinned root: not trusted
		}
		anchors.AddCert(c)
		admitted = append(admitted, digest(c))
	}
	if len(admitted) == 0 {
		return nil, nil, ErrNoAnchor
	}
	return &Verifier{anchors: anchors}, admitted, nil
}

func parseCerts(b []byte) ([]*x509.Certificate, error) {
	var out []*x509.Certificate
	for {
		var blk *pem.Block
		blk, b = pem.Decode(b)
		if blk == nil {
			return out, nil
		}
		if blk.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
}

func digest(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	return hex.EncodeToString(sum[:])
}
