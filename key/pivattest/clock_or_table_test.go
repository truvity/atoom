package pivattest_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/truvity/atoom/key/pivattest"
	"github.com/truvity/atoom/key/pivattest/attesttest"
)

// The build decides between a clock error and a table error (pins.go): the clock is named only when a certificate that it puts outside its
// validity is itself a row of the table with the role of a PIV attestation intermediate. These tests hold the cases of that decision that the
// edges of the clock and the faults of the table (clock_edges_test.go) do not reach: what is collected as "the clock refuses it", and what is
// then done with it. The time is always given through the seam (BuildFromAt); the clock of the machine is never read.

// cotClassify reports the classes, of the four this decision chooses between, that the error belongs to.
func cotClassify(err error) []string {
	var got []string
	for name, class := range map[string]error{"ErrClock": pivattest.ErrClock, "ErrPinTable": pivattest.ErrPinTable, "ErrAdmission": pivattest.ErrAdmission, "ErrNoAnchor": pivattest.ErrNoAnchor} {
		if errors.Is(err, class) {
			got = append(got, name)
		}
	}
	slices.Sort(got)
	return got
}

// cotWant says that the build gave no verifier and an error of exactly the class given and of no other of the four.
func cotWant(t *testing.T, label string, v *pivattest.Verifier, err error, class string) {
	t.Helper()
	if v != nil || err == nil || !slices.Equal(cotClassify(err), []string{class}) {
		t.Errorf("%s: %v (classes %v, a verifier: %v), want %s and no other class", label, err, cotClassify(err), v != nil, class)
	}
}

func cotBuilds(t *testing.T, label string, v *pivattest.Verifier, err error) {
	t.Helper()
	if v == nil || err != nil {
		t.Errorf("%s: %v, want a verifier", label, err)
	}
}

// cotNamed says whether the text of the error names the certificate, in the form the clock error writes it.
func cotNamed(err error, cn string) bool { return strings.Contains(err.Error(), fmt.Sprintf("%q", cn)) }

// cotWithRole is a copy of the table in which the rows of the given names have the role given.
func cotWithRole(table []pivattest.Pin, role pivattest.Role, names ...string) []pivattest.Pin {
	out := slices.Clone(table)
	for i := range out {
		if slices.Contains(names, out[i].Name) {
			out[i].Role = role
		}
	}
	return out
}

// cotRefusedForTheClock checks the premise that a certificate is put outside its validity by the time given, in the words of the standard
// library: the error is a certificate error of the reason Expired (the library gives that reason for a certificate not yet valid as well),
// which is what the build collects.
func cotRefusedForTheClock(t *testing.T, c *x509.Certificate, at time.Time) {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(c)
	_, err := c.Verify(x509.VerifyOptions{Roots: pool, CurrentTime: at, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}})
	var invalid x509.CertificateInvalidError
	if !errors.As(err, &invalid) || invalid.Reason != x509.Expired {
		t.Fatalf("the premise: %q at %s is not refused as expired: %v", c.Subject.CommonName, at.Format(qpLayout), err)
	}
}

// cotPKIFiles is a test PKI made for a window, with its files and a table of its own (root, the intermediate between, the PIV intermediate).
type cotPKIFiles struct {
	p            *attesttest.PKI
	root, bundle []byte
	table        []pivattest.Pin
}

func cotPKI(t *testing.T, o attesttest.PKIOptions) cotPKIFiles {
	t.Helper()
	p, err := attesttest.NewPKI(o)
	if err != nil {
		t.Fatal(err)
	}
	table := []pivattest.Pin{
		{Name: p.Root.Subject.CommonName, Role: pivattest.RoleRoot, SHA256: attesttest.SHA256(p.Root)},
		{Name: p.Inter.Subject.CommonName, Role: pivattest.RoleIntermediate, SHA256: attesttest.SHA256(p.Inter)},
		{Name: p.PIV.Subject.CommonName, Role: pivattest.RolePIV, SHA256: attesttest.SHA256(p.PIV)},
	}
	return cotPKIFiles{p: p, root: p.RootPEM(), bundle: p.BundlePEM(), table: table}
}

// (a) A certificate that the clock refuses but that is no row of the table does not make the clock the cause of a disagreement: the
// disagreement is a table error. The clock error needs a refused certificate that is a PIV row (the neighbour below, with the same extra,
// the same table and a clock that refuses the newest pinned intermediate).
func TestCOTAnExpiredCertificateThatIsNoRowDoesNotMakeATableDisagreementTheClocks(t *testing.T) {
	f := embedded(t)
	extra := expiredCertNamed(t, "Yubico PIV Attestation TEST EXTRA")
	c, err := x509.ParseCertificate(derOf(t, extra)[0])
	if err != nil {
		t.Fatal(err)
	}
	cotRefusedForTheClock(t, c, t2030)
	extraLast := append(slices.Clone(f.bundlePEM), extra...)
	extraFirst := append(slices.Clone(extra), f.bundlePEM...)
	newest, _ := validityOf(t, "Yubico PIV Attestation B2 1")
	beforeNewest := newest.Add(-time.Second)

	// the passing neighbour: the files with the extra and the intact table, at a good clock
	for name, bundle := range map[string][]byte{"extra last": extraLast, "extra first": extraFirst} {
		v, err := pivattest.BuildFromAt(f.rootPEM, bundle, pivattest.Pins(), t2030)
		cotBuilds(t, "the intact table, "+name, v, err)
	}
	for tname, table := range map[string][]pivattest.Pin{
		"a row left out":                       slices.DeleteFunc(pivattest.Pins(), func(p pivattest.Pin) bool { return p.Name == "Yubico PIV Attestation A 1" }),
		"a row that makes an anchor no anchor": cotWithRole(pivattest.Pins(), pivattest.RoleIntermediate, "Yubico PIV Attestation A 1"),
	} {
		for bname, bundle := range map[string][]byte{"extra last": extraLast, "extra first": extraFirst} {
			// the extra is refused for the clock and is no row: the admitted set is not the table's, and that is a table error
			v, err := pivattest.BuildFromAt(f.rootPEM, bundle, table, t2030)
			cotWant(t, tname+", "+bname+", a good clock", v, err, "ErrAdmission")
			// the same, at a clock that refuses the newest pinned intermediate too: that one is a row, so the clock is named, and it is that one
			// that is named and not the extra, wherever the extra stands in the file
			v, err = pivattest.BuildFromAt(f.rootPEM, bundle, table, beforeNewest)
			cotWant(t, tname+", "+bname+", a clock before the newest", v, err, "ErrClock")
			if err != nil && (!cotNamed(err, "Yubico PIV Attestation B2 1") || strings.Contains(err.Error(), "TEST EXTRA")) {
				t.Errorf("%s, %s: the clock error names the wrong certificate: %v", tname, bname, err)
			}
		}
	}
}

// (b) No PIV intermediate is admitted and some are refused for the clock, but none of those is a PIV row of the table: the error is the
// one of the construction, no anchor, and not the clock. Reached with the vendor's files and a table that gives its three PIV rows the plain
// intermediate role, at a clock before all three start.
func TestCOTNoAnchorWhileTheClockRefusesOnlyCertificatesThatAreNoPIVRows(t *testing.T) {
	f := embedded(t)
	w := qpWindows(t)
	pivNames := []string{"Yubico PIV Attestation A 1", "Yubico PIV Attestation B 1", "Yubico PIV Attestation B2 1"}
	before := qpFrom.Add(-24 * time.Hour)
	for _, n := range pivNames {
		if !before.Before(w[n][0]) {
			t.Fatalf("the clock %s is not before the start of %s", before, n)
		}
	}
	plain := cotWithRole(pivattest.Pins(), pivattest.RoleIntermediate, pivNames...)

	// the passing neighbours: the same files and clock with the table as it is (the clock is named); the same table at a good clock (no
	// intermediate is refused for the clock, and the table names three that are not anchors, so it is the admission that fails)
	v, err := pivattest.BuildFromAt(f.rootPEM, f.bundlePEM, pivattest.Pins(), before)
	cotWant(t, "the table as it is, before every start", v, err, "ErrClock")
	v, err = pivattest.BuildFromAt(f.rootPEM, f.bundlePEM, plain, t2030)
	cotWant(t, "the plain table, a good clock", v, err, "ErrAdmission")

	v, err = pivattest.BuildFromAt(f.rootPEM, f.bundlePEM, plain, before)
	cotWant(t, "the plain table, before every start", v, err, "ErrNoAnchor")
	if err != nil && strings.Contains(err.Error(), "clock") {
		t.Errorf("the no-anchor error speaks of the clock: %v", err)
	}
	// one PIV row left as it is (the newest) is enough to name the clock: the refused certificates are looked at together
	v, err = pivattest.BuildFromAt(f.rootPEM, f.bundlePEM, cotWithRole(pivattest.Pins(), pivattest.RoleIntermediate, pivNames[:2]...), before)
	cotWant(t, "two plain rows, the newest a PIV row", v, err, "ErrClock")
	if err != nil && !cotNamed(err, "Yubico PIV Attestation B2 1") {
		t.Errorf("two plain rows: the clock error does not name the certificate that is a PIV row: %v", err)
	}
}

// (c) The certificate that the clock refuses is the root and not a PIV certificate. This is written down as it is: the standard library
// answers for a PIV certificate whose chain ends in an expired root that the root is outside its validity, so the build collects the root;
// the root is no PIV row of the table, so it is not named as the clock, and the build reports what the construction found: that no anchor was
// admitted. (The vendor's root is valid to the year 9999, so Default does not meet this; a caller's PEM built here does.)
func TestCOTARootThatTheClockRefusesIsNotNamedAsTheClock(t *testing.T) {
	// the window of the root, and of the intermediate between, ends in 2020; the PIV intermediate is valid to 2099
	for name, o := range map[string]attesttest.PKIOptions{
		"a PIV certificate signed by the root":                          {NotBefore: t2010, NotAfter: t2020, PIVNotAfter: t2099, PIVSignedByRoot: true},
		"a PIV certificate signed by the intermediate between, as well": {NotBefore: t2010, NotAfter: t2020, PIVNotAfter: t2099},
	} {
		k := cotPKI(t, o)
		if !k.p.PIV.NotAfter.After(t2030) || !k.p.Root.NotAfter.Before(t2030) {
			t.Fatalf("%s: the premise: the PIV certificate must be valid and the root not, at the clock", name)
		}
		// the premise: it is the root that the standard library names as outside its validity
		pool, inter := x509.NewCertPool(), x509.NewCertPool()
		pool.AddCert(k.p.Root)
		inter.AddCert(k.p.Inter)
		_, verr := k.p.PIV.Verify(x509.VerifyOptions{Roots: pool, Intermediates: inter, CurrentTime: t2030, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}})
		var invalid x509.CertificateInvalidError
		if !errors.As(verr, &invalid) || invalid.Reason != x509.Expired || invalid.Cert.Subject.CommonName == k.p.PIV.Subject.CommonName {
			t.Fatalf("%s: the premise: the library does not name an expired certificate of the chain above the PIV certificate: %v", name, verr)
		}
		// the passing neighbour: the same files at a clock inside the window of the root
		v, err := pivattest.BuildFromAt(k.root, k.bundle, k.table, t2015)
		cotBuilds(t, name+", inside the window of the root", v, err)
		// the root is refused for the clock: no anchor, and not the clock
		v, err = pivattest.BuildFromAt(k.root, k.bundle, k.table, t2030)
		cotWant(t, name+", after the end of the root", v, err, "ErrNoAnchor")
		if err != nil && strings.Contains(err.Error(), "clock") {
			t.Errorf("%s: the error speaks of the clock, which is not what is written down: %v", name, err)
		}
	}
}

// (d) A PIV-named certificate that is refused for a reason other than the clock is not collected for the clock and is not admitted: when the
// table names it as a PIV row, the admitted set is not the table's, and the error is the admission, never the clock. Were any refusal
// collected, this row (a PIV row that is not admitted) would be named as the clock.
func TestCOTAPIVNamedCertificateRefusedForAnotherReasonIsNotTheClocks(t *testing.T) {
	p := newOwnPKI(t)
	rootPEM := attesttest.PEM(p.root)
	rootRow := pivattest.Pin{Name: p.root.Subject.CommonName, Role: pivattest.RoleRoot, SHA256: attesttest.SHA256(p.root)}
	pivRow := pivattest.Pin{Name: p.piv.Subject.CommonName, Role: pivattest.RolePIV, SHA256: attesttest.SHA256(p.piv)}
	rowOf := func(c *x509.Certificate) pivattest.Pin {
		return pivattest.Pin{Name: c.Subject.CommonName, Role: pivattest.RolePIV, SHA256: attesttest.SHA256(c)}
	}
	const extraName = "Yubico PIV Attestation TEST ONLY Extra"

	// a member of the bundle that is not a CA, signed by the root, and a PIV-named certificate that it signs
	notCAKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	notCA := ownCert(t, &x509.Certificate{
		SerialNumber: ownSerial(), Subject: pkix.Name{CommonName: "TEST ONLY Not A CA"}, NotBefore: t2010, NotAfter: t2099,
		BasicConstraintsValid: true, IsCA: false, KeyUsage: x509.KeyUsageDigitalSignature,
	}, p.root, &notCAKey.PublicKey, p.rootKey)
	extraKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	underNotCA := ownCert(t, ownCA(extraName), notCA, &extraKey.PublicKey, notCAKey)
	// a PIV-named certificate of another root that is not in the files
	stranger := newOwnPKI(t)
	ofStranger, _ := stranger.extraPIV(t, extraName)
	// the passing neighbour of both: the same name signed by the root, a CA that is in the files
	underRoot, _ := p.extraPIV(t, extraName)

	table := func(extra *x509.Certificate) []pivattest.Pin { return []pivattest.Pin{rootRow, pivRow, rowOf(extra)} }
	v, err := pivattest.BuildFromAt(rootPEM, attesttest.PEM(p.piv, notCA, underRoot), table(underRoot), t2030)
	cotBuilds(t, "the extra signed by the root, a row of the table", v, err)
	for name, c := range map[string]struct {
		bundle []byte
		extra  *x509.Certificate
	}{
		"signed by a member of the bundle that is not a CA": {attesttest.PEM(p.piv, notCA, underNotCA), underNotCA},
		"signed by a root that is not in the files":         {attesttest.PEM(p.piv, ofStranger), ofStranger},
	} {
		v, err := pivattest.BuildFromAt(rootPEM, c.bundle, table(c.extra), t2030)
		cotWant(t, name, v, err, "ErrAdmission")
		// the passing neighbour: the table without the row of the extra agrees with what the files admit (the extra is refused, and no row says so)
		v, err = pivattest.BuildFromAt(rootPEM, c.bundle, []pivattest.Pin{rootRow, pivRow}, t2030)
		cotBuilds(t, name+", the table without its row", v, err)
	}
}

// (e) The root and a pinned PIV intermediate are both outside the validity at the clock: the clock error names the PIV intermediate, and
// never the root (nor an intermediate between), whichever is the first that the clock refuses.
func TestCOTTheClockErrorNamesThePIVIntermediateWhenTheRootIsOutsideToo(t *testing.T) {
	// the vendor's files, before the start of everything: the root, the intermediates between and the three PIV intermediates are all outside
	f := embedded(t)
	w := qpWindows(t)
	at := qpFrom.Add(-24 * time.Hour)
	for _, n := range qpPinnedSix {
		if !at.Before(w[n][0]) {
			t.Fatalf("the premise: %s is valid at %s", n, at)
		}
	}
	v, err := pivattest.BuildFromAt(f.rootPEM, f.bundlePEM, pivattest.Pins(), at)
	cotWant(t, "the vendor's files before every start", v, err, "ErrClock")
	if err != nil {
		var piv int
		for _, n := range qpPinnedSix {
			if cotNamed(err, n) {
				if !strings.HasPrefix(n, "Yubico PIV Attestation ") {
					t.Errorf("the clock error names %q, which is not a PIV intermediate: %v", n, err)
				}
				piv++
			}
		}
		if piv != 1 {
			t.Errorf("the clock error names %d pinned certificates: %v", piv, err)
		}
	}

	// a test PKI whose whole window has ended at the clock, with the table of its own
	k := cotPKI(t, attesttest.PKIOptions{NotBefore: t2010, NotAfter: t2020})
	if !k.p.Root.NotAfter.Before(t2030) || !k.p.PIV.NotAfter.Before(t2030) {
		t.Fatal("the premise: the root and the PIV intermediate are outside at the clock")
	}
	v, err = pivattest.BuildFromAt(k.root, k.bundle, k.table, t2015)
	cotBuilds(t, "the test PKI, inside its window", v, err)
	v, err = pivattest.BuildFromAt(k.root, k.bundle, k.table, t2030)
	cotWant(t, "the test PKI, after its window", v, err, "ErrClock")
	if err != nil {
		if !cotNamed(err, k.p.PIV.Subject.CommonName) || cotNamed(err, k.p.Root.Subject.CommonName) || cotNamed(err, k.p.Inter.Subject.CommonName) {
			t.Errorf("the clock error does not name the PIV intermediate alone: %v", err)
		}
	}
}

// (e), with the root among the refused certificates: a bundle of two PIV intermediates, one valid whose root has ended (the standard library
// then names the root as outside its validity) and one that has ended itself. Both the root and the second one are collected; only the second
// is a PIV row, and it is the one that the clock error names, whichever of the two PIV certificates stands first in the file.
func TestCOTTheClockErrorSkipsTheRootAndNamesTheRefusedPIVRow(t *testing.T) {
	rootKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rt := ownCA("TEST ONLY Own Root, ended in 2020")
	rt.NotAfter = t2020
	root := ownCert(t, rt, rt, &rootKey.PublicKey, rootKey)
	pivKey := func() *ecdsa.PublicKey {
		k, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return &k.PublicKey
	}
	const stillValid, ended = "Yubico PIV Attestation TEST ONLY Valid", "Yubico PIV Attestation TEST ONLY Ended"
	valid := ownCert(t, ownCA(stillValid), root, pivKey(), rootKey) // valid to 2099, under a root that has ended
	et := ownCA(ended)
	et.NotAfter = t2020
	over := ownCert(t, et, root, pivKey(), rootKey) // has ended itself
	table := []pivattest.Pin{
		{Name: root.Subject.CommonName, Role: pivattest.RoleRoot, SHA256: attesttest.SHA256(root)},
		{Name: valid.Subject.CommonName, Role: pivattest.RolePIV, SHA256: attesttest.SHA256(valid)},
		{Name: over.Subject.CommonName, Role: pivattest.RolePIV, SHA256: attesttest.SHA256(over)},
	}
	for name, bundle := range map[string][]byte{"the valid one first": attesttest.PEM(valid, over), "the ended one first": attesttest.PEM(over, valid)} {
		v, err := pivattest.BuildFromAt(attesttest.PEM(root), bundle, table, t2015)
		cotBuilds(t, name+", inside every window", v, err)
		v, err = pivattest.BuildFromAt(attesttest.PEM(root), bundle, table, t2030)
		cotWant(t, name+", after the end of the root", v, err, "ErrClock")
		if err != nil && (!cotNamed(err, ended) || cotNamed(err, stillValid) || cotNamed(err, root.Subject.CommonName)) {
			t.Errorf("%s: the clock error names more or other than the PIV row that has ended: %v", name, err)
		}
	}
}
