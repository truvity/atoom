package pivattest

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

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

// defaultCache keeps the first verifier that was built successfully. A failed build is not kept: a board with no battery for its clock
// starts in the past, where the pinned certificates are not valid yet, and has the right time later.
type defaultCache struct {
	mu sync.Mutex
	v  *Verifier
}

var theDefault defaultCache

// get returns the kept verifier, or builds one at the time that now gives, and keeps it only when the build succeeded.
func (c *defaultCache) get(now func() time.Time) (*Verifier, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.v != nil {
		return c.v, nil
	}
	v, err := buildFrom(rootPEM, bundlePEM, pins, now())
	if err != nil {
		return nil, err
	}
	c.v = v
	return v, nil
}

// Default is the verifier over the vendor's pinned certificates. It is built on the first call that succeeds and kept; a call that fails
// is not kept, and the next call builds again. It fails when an embedded certificate is not the one the pin table names by its SHA-256,
// when the root file holds more or fewer than one certificate, when the PIV attestation intermediates that the bundle admits (below) are not
// exactly those of the table, and when the machine's clock is outside the validity of the pinned certificates (ErrClock, which names the clock and the certificate's
// validity and is told apart from the two errors of the table, ErrPinTable and ErrAdmission): the pinned intermediates are checked against
// the root at the clock of the machine when the verifier is built. A changed file is refused at the first call, and nothing
// is admitted silently. The verifier then checks a slot certificate, a signer and an anchor at the time of each call (VerifyAt: the time
// given).
//
// The older vendor root is not pinned: see the package comment.
func Default() (*Verifier, error) { return theDefault.get(time.Now) }

// buildFrom is Default over the given files and table (the tests give others).
func buildFrom(rootPEM, bundlePEM []byte, pins []Pin, now time.Time) (*Verifier, error) {
	rootSHA := ""
	for _, p := range pins {
		if p.Role == RoleRoot {
			rootSHA = p.SHA256
		}
	}
	v, admitted, outside, err := newVerifier(rootPEM, bundlePEM, rootSHA, now)
	if err != nil {
		if errors.Is(err, ErrNoAnchor) && outside != nil {
			return nil, clockError(now, outside)
		}
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
		if outside != nil {
			// A pinned certificate that the clock puts outside its validity is the reason that the admitted set is not the table's.
			return nil, clockError(now, outside)
		}
		return nil, ErrAdmission
	}
	return v, nil
}

// NewVerifierFromPEM builds a verifier whose trust anchors are the one certificate of rootPEM, which must have the SHA-256 wantRootSHA256
// (lower-case hexadecimal), and every certificate of bundlePEM whose common name begins "Yubico PIV Attestation " and that chains to the
// root through the bundle at the current time (the admission rule). A bundle with no such certificate is refused.
//
// It is for a test PKI; Default is for programs. The digest is the only pin: it must be a constant that comes from outside the PEM, from the
// source of the program or from a record a person checked. A root file and a digest that were both taken from the party being verified (a
// card, a peer, a file it sent) make the check worthless: whoever made them is trusted by construction.
//
// A PEM block that is not a CERTIFICATE is skipped; a CERTIFICATE block whose bytes do not parse is an error (ErrRootCount for the root
// file, ErrParse for the bundle). A block that is cut short or whose armor or base64 body is damaged is skipped without an error, as are
// bytes that are no block, before, between or after the whole blocks. The whole blocks are still read, with one exception: a block that is
// cut short with no line end before the next block takes that block with it. A verifier built from a damaged or cut bundle has fewer
// anchors, and so refuses more. Only Default checks the bundle against the pins and fails closed.
func NewVerifierFromPEM(rootPEM, bundlePEM []byte, wantRootSHA256 string) (*Verifier, error) {
	v, _, _, err := newVerifier(rootPEM, bundlePEM, wantRootSHA256, time.Now())
	return v, err
}

// clockError is ErrClock with the time of the clock and the validity of the certificate that the clock puts outside it.
func clockError(now time.Time, c *x509.Certificate) error {
	const layout = "2006-01-02 15:04:05"
	return &classed{class: ErrClock, text: fmt.Sprintf("pivattest: the clock of this machine reads %s UTC, which is outside the validity of the pinned certificate %q (valid %s to %s UTC): set the clock; the embedded files are not at fault",
		now.UTC().Format(layout), c.Subject.CommonName, c.NotBefore.UTC().Format(layout), c.NotAfter.UTC().Format(layout))}
}

// newVerifier admits at the time now, and also returns the SHA-256 of every admitted intermediate and, when a certificate that the
// admission needed was refused because now is outside its validity, the first such certificate (nil otherwise).
func newVerifier(rootPEM, bundlePEM []byte, wantRootSHA256 string, now time.Time) (*Verifier, []string, *x509.Certificate, error) {
	roots, err := parseCerts(rootPEM)
	if err != nil || len(roots) != 1 {
		return nil, nil, nil, ErrRootCount
	}
	if digest(roots[0]) != wantRootSHA256 {
		return nil, nil, nil, ErrRootPin
	}
	bundle, err := parseCerts(bundlePEM)
	if err != nil {
		return nil, nil, nil, &classed{class: ErrParse, cause: err, text: "pivattest: a certificate of the bundle does not parse: " + err.Error()}
	}
	rootPool, interPool := x509.NewCertPool(), x509.NewCertPool()
	rootPool.AddCert(roots[0])
	for _, c := range bundle {
		interPool.AddCert(c)
	}
	anchors := x509.NewCertPool()
	anchors.AddCert(roots[0])
	var admitted []string
	var outside *x509.Certificate
	for _, c := range bundle {
		if !strings.HasPrefix(c.Subject.CommonName, pivIntermediatePrefix) {
			continue
		}
		if _, err := c.Verify(x509.VerifyOptions{Roots: rootPool, Intermediates: interPool, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
			var invalid x509.CertificateInvalidError
			if errors.As(err, &invalid) && invalid.Reason == x509.Expired && outside == nil {
				outside = invalid.Cert // the clock, not the chain, refuses it
			}
			continue // not signed through the pinned root, or not valid now: not trusted
		}
		anchors.AddCert(c)
		admitted = append(admitted, digest(c))
	}
	if len(admitted) == 0 {
		return nil, nil, outside, ErrNoAnchor
	}
	set := make(map[string]bool, len(admitted))
	for _, d := range admitted {
		set[d] = true
	}
	return &Verifier{anchors: anchors, admitted: set}, admitted, outside, nil
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
