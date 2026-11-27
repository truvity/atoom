package pivattest

import (
	"bytes"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/truvity/atoom/key/pivattest/attesttest"
)

// The six certificates that the package trusts, by name and digest. This table is written out a second time here on purpose: the test
// fails on any change to the table of pins.go that this list does not repeat, so a change to what is trusted is two edits, in one review.
var wantPins = []Pin{
	{"Yubico Attestation Root 1", RoleRoot, "62760c6a6ef91679f454c8902b80fd009825b3f25da90f1fbace2ec6586cd5a8"},
	{"Yubico Attestation Intermediate A 1", RoleIntermediate, "4698a1d3389c3ec60016c216250f1d0439922832d65142327436376dc2942b55"},
	{"Yubico Attestation Intermediate B 1", RoleIntermediate, "d4cc3f456fdaf4e7812a21aab1dfe9d8e27d24e2fd2d6f21c9940109f0daa754"},
	{"Yubico PIV Attestation A 1", RolePIV, "6de693f05376f5d8ca29069261e1c8626c75d503bd2edbfd75354cad1f722870"},
	{"Yubico PIV Attestation B 1", RolePIV, "2d55b7998f4e42569d6d8fa382b6dc77d1dacf07358b19701163892922b17052"},
	{"Yubico PIV Attestation B2 1", RolePIV, "0c90b7d184a36edf50a35f9be935f0c5689bfdcfe5bdd073366cafb49061a440"},
}

func TestThePinTableIsExactlyTheSixCertificates(t *testing.T) {
	if !slices.Equal(Pins(), wantPins) {
		t.Fatalf("the pin table is %+v", Pins())
	}
	// Pins gives a copy: a caller cannot change what is trusted.
	p := Pins()
	p[0].SHA256 = "00"
	if Pins()[0].SHA256 == "00" {
		t.Fatal("Pins returns the table itself")
	}
}

func TestDefaultIsBuiltFromThePinnedFiles(t *testing.T) {
	v, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if v2, _ := Default(); v2 != v {
		t.Fatal("Default builds a verifier on every call")
	}
	// The anchors are the root and the three PIV intermediates, and no other certificate of the bundle.
	got := map[string]bool{}
	for _, s := range v.anchors.Subjects() { //nolint:staticcheck // a pool built here, not the system's
		got[string(s)] = true
	}
	if len(got) != 4 {
		t.Fatalf("%d anchors, want the root and three PIV attestation intermediates", len(got))
	}
}

func TestEveryRowOfTheTableIsInTheFilesAndTheOthersAreNotAnchors(t *testing.T) {
	root, err := parseCerts(rootPEM)
	if err != nil || len(root) != 1 {
		t.Fatalf("root file: %v, %d certificates", err, len(root))
	}
	bundle, err := parseCerts(bundlePEM)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle) != 15 {
		t.Errorf("the bundle holds %d certificates, the vendor's file has 15", len(bundle))
	}
	digests := map[string]string{}
	for _, c := range append(root, bundle...) {
		digests[digest(c)] = c.Subject.CommonName
	}
	for _, p := range pins {
		if digests[p.SHA256] != p.Name {
			t.Errorf("%s (%s) is not in the embedded files", p.Name, p.SHA256)
		}
	}
}

// The pinned files are the vendor's files as the driver's directory holds them today. Pull request 2 of the series removes the older copy
// (key/yubikeypiv/internal/card/certs) and this comparison with it; until then the two must not drift.
func TestTheEmbeddedFilesAreTheDriversCopiesWhileBothExist(t *testing.T) {
	for _, f := range []string{"yubico-attestation-root-1.pem", "yubico-intermediate.pem"} {
		old, err := os.ReadFile("../yubikeypiv/internal/card/certs/" + f) //nolint:gosec // one of two fixed names of this test
		if errors.Is(err, os.ErrNotExist) {
			t.Skipf("the older copy of %s is gone", f)
		}
		if err != nil {
			t.Fatal(err)
		}
		mine, err := os.ReadFile("certs/" + f) //nolint:gosec // one of two fixed names of this test
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(old, mine) {
			t.Errorf("%s differs from the driver's copy", f)
		}
	}
}

// A person who changes the table changes docs/pins.md: every name and digest of the table is a row there.
func TestThePinsPageNamesEveryPin(t *testing.T) {
	b, err := os.ReadFile("docs/pins.md")
	if err != nil {
		t.Fatal(err)
	}
	page := string(b)
	for _, p := range pins {
		if !strings.Contains(page, p.SHA256) || !strings.Contains(page, p.Name) {
			t.Errorf("docs/pins.md has no row for %s (%s)", p.Name, p.SHA256)
		}
	}
	rows := 0
	for _, line := range strings.Split(page, "\n") {
		if strings.HasPrefix(line, "| Yubico ") {
			rows++
		}
	}
	if rows != len(pins) {
		t.Errorf("docs/pins.md has %d rows of certificates, the table %d", rows, len(pins))
	}
}

func TestDefaultFailsClosed(t *testing.T) {
	mod := func(f func(p []Pin)) []Pin {
		p := Pins()
		f(p)
		return p
	}
	other, err := attesttest.NewPKI(attesttest.PKIOptions{})
	if err != nil {
		t.Fatal(err)
	}
	twin, err := attesttest.NewPKI(attesttest.PKIOptions{PIVName: "Yubico PIV Attestation B 1"})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name         string
		root, bundle []byte
		table        []Pin
		want         error
	}{
		{"the files and the table agree", rootPEM, bundlePEM, pins, nil},
		{"a digest of the table is not the certificate's", rootPEM, bundlePEM, mod(func(p []Pin) { p[3].SHA256 = strings.Repeat("0", 64) }), ErrPinTable},
		{"the root's digest in the table is another", rootPEM, bundlePEM, mod(func(p []Pin) { p[0].SHA256 = p[1].SHA256 }), ErrRootPin},
		{"a row names a certificate that is not in the files", rootPEM, bundlePEM, mod(func(p []Pin) { p[4].Name = "Yubico PIV Attestation C 1" }), ErrPinTable},
		{"the bundle admits an intermediate that the table lacks", rootPEM, bundlePEM, mod(func(p []Pin) { p[5].Role = RoleIntermediate }), ErrAdmission},
		{"the table names an intermediate that is not admitted", rootPEM, bundlePEM, mod(func(p []Pin) { p[1].Role = RolePIV }), ErrAdmission},
		{"the root file holds two certificates", append(append([]byte{}, rootPEM...), attesttest.PEM(other.Root)...), bundlePEM, pins, ErrRootCount},
		{"the root file holds another certificate", attesttest.PEM(other.Root), bundlePEM, pins, ErrRootPin},
		{"the root file is empty", nil, bundlePEM, pins, ErrRootCount},
		{"the bundle lacks an intermediate of the table", rootPEM, without(t, "Yubico Attestation Intermediate B 1"), pins, ErrPinTable},
		{"a second certificate has the name of a pinned one", rootPEM, append(append([]byte{}, bundlePEM...), attesttest.PEM(twin.PIV)...), pins, ErrPinTable},
		{"the bundle is empty", rootPEM, nil, pins, ErrNoAnchor},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := buildFrom(tt.root, tt.bundle, tt.table)
			if tt.want == nil {
				if err != nil || v == nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if !errors.Is(err, tt.want) || v != nil {
				t.Fatalf("got %v (a verifier: %v), want %v", err, v != nil, tt.want)
			}
		})
	}
}

// without is the vendor's bundle with one certificate left out, as PEM.
func without(t *testing.T, name string) []byte {
	t.Helper()
	cs, err := parseCerts(bundlePEM)
	if err != nil {
		t.Fatal(err)
	}
	var out []byte
	for _, c := range cs {
		if c.Subject.CommonName != name {
			out = append(out, attesttest.PEM(c)...)
		}
	}
	return out
}
