package pivattest_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/truvity/atoom/key/pivattest"
)

// Difference 7 of the package comment: Default needs all three PIV attestation intermediates of the pin table admitted at the clock of the
// build, where the check it replaces built a verifier from any one of them (from 2024-12-01, the start of the validity of the first two;
// before that date the old check also built nothing). The clock is the one given through the seam, never the machine's.

func validityOf(t *testing.T, name string) (from, to time.Time) {
	t.Helper()
	f := embedded(t)
	for _, d := range append([][]byte{f.rootDER}, f.bundleDER...) {
		if cnOf(t, d) == name {
			c, err := x509.ParseCertificate(d)
			if err != nil {
				t.Fatal(err)
			}
			return c.NotBefore, c.NotAfter
		}
	}
	t.Fatalf("%s is not in the embedded files", name)
	return
}

func TestDefaultNeedsEveryPinnedIntermediateAtTheClockOfTheBuild(t *testing.T) {
	f := embedded(t)
	newest, _ := validityOf(t, "Yubico PIV Attestation B2 1")
	older, _ := validityOf(t, "Yubico PIV Attestation B 1")
	if !older.Before(newest) {
		t.Fatalf("the newest PIV intermediate (from %s) is not newer than B 1 (from %s): the test needs a clock between them", newest, older)
	}
	build := func(at time.Time) (*pivattest.Verifier, error) {
		return pivattest.BuildFromAt(f.rootPEM, f.bundlePEM, pivattest.Pins(), at)
	}
	mustRefuseForTheClock := func(name string, at time.Time, cert string) {
		t.Helper()
		from, to := validityOf(t, cert)
		v, err := build(at)
		if v != nil || !errors.Is(err, pivattest.ErrClock) {
			t.Fatalf("%s: %v, %v, want no verifier and ErrClock", name, v, err)
		}
		if errors.Is(err, pivattest.ErrAdmission) || errors.Is(err, pivattest.ErrNoAnchor) || errors.Is(err, pivattest.ErrPinTable) {
			t.Errorf("%s: the error is also of the table: %v", name, err)
		}
		for _, want := range []string{at.UTC().Format("2006-01-02 15:04:05"), cert, from.UTC().Format("2006-01-02 15:04:05"), to.UTC().Format("2006-01-02 15:04:05"), "clock"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: the text %q does not hold %q", name, err, want)
			}
		}
	}
	// two of the three are valid at this clock: one or two admitted is not enough
	mustRefuseForTheClock("2025-03-01", time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC), "Yubico PIV Attestation B2 1")
	mustRefuseForTheClock("a second before the start of the newest", newest.Add(-time.Second), "Yubico PIV Attestation B2 1")
	// before the first two start none is admitted: the text names the first certificate that the clock refuses
	mustRefuseForTheClock("before every PIV intermediate", older.Add(-24*time.Hour), "Yubico PIV Attestation A 1")
	v, err := build(newest.Add(time.Second))
	if err != nil || v == nil {
		t.Fatalf("a second after the start of the newest: %v", err)
	}
	if n := len(subjects(v)); n != 4 {
		t.Errorf("%d anchors, want the root and three PIV intermediates", n)
	}
	// a disagreement of the table, at a good clock, is not the clock's
	table := slices.DeleteFunc(pivattest.Pins(), func(p pivattest.Pin) bool { return p.Name == "Yubico PIV Attestation A 1" })
	if v, err := pivattest.BuildFromAt(f.rootPEM, f.bundlePEM, table, t2030); v != nil || !errors.Is(err, pivattest.ErrAdmission) || errors.Is(err, pivattest.ErrClock) {
		t.Errorf("a table without a row: %v, %v", v, err)
	}
	other := slices.Clone(pivattest.Pins())
	for i := range other {
		if other[i].Name == "Yubico PIV Attestation A 1" {
			other[i].Role = pivattest.RoleIntermediate
		}
	}
	if v, err := pivattest.BuildFromAt(f.rootPEM, f.bundlePEM, other, t2030); v != nil || !errors.Is(err, pivattest.ErrAdmission) || errors.Is(err, pivattest.ErrClock) {
		t.Errorf("a table that names an admitted certificate as no anchor: %v, %v", v, err)
	}
}

// Through the cache behind Default: a refusal for the clock is not kept, and the next call at a good clock builds.
func TestDefaultRefusedForTheClockIsNotKept(t *testing.T) {
	var c pivattest.DefaultCache
	newest, _ := validityOf(t, "Yubico PIV Attestation B2 1")
	for range 2 {
		if v, err := c.Get(func() time.Time { return newest.Add(-time.Hour) }); v != nil || !errors.Is(err, pivattest.ErrClock) || c.Kept() != nil {
			t.Fatalf("%v, %v, kept %v", v, err, c.Kept())
		}
	}
	if v, err := c.Get(func() time.Time { return newest.Add(time.Hour) }); err != nil || v == nil || c.Kept() != v {
		t.Fatalf("at a good clock: %v, %v", v, err)
	}
}

// expiredCertNamed is a self-signed certificate with the given name whose validity ended in 2020: the stand-in for a changed file.
func expiredCertNamed(t *testing.T, name string) []byte {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, NotBefore: t2010, NotAfter: t2020,
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// The clock is named only when the embedded files are the pinned ones. A changed file with an expired PIV-named certificate is a table error,
// never a stale clock ("the embedded files are not at fault" would be false).
func TestAChangedFileWithAnExpiredPIVNamedCertificateIsATableErrorNotTheClock(t *testing.T) {
	f := embedded(t)
	blocks := strings.SplitAfter(string(f.bundlePEM), "-----END CERTIFICATE-----\n")
	blocks = blocks[:len(blocks)-1]
	index := func(name string) int {
		for k, d := range f.bundleDER {
			if cnOf(t, d) == name {
				return k
			}
		}
		t.Fatalf("%s is not in the bundle", name)
		return -1
	}
	const target = "Yubico PIV Attestation B2 1"
	k := index(target)
	replaced := slices.Clone(blocks)
	replaced[k] = string(expiredCertNamed(t, target))
	removed := slices.Delete(slices.Clone(blocks), k, k+1)
	extra := string(expiredCertNamed(t, "Yubico PIV Attestation TEST EXTRA"))
	for name, bundle := range map[string]string{
		"a pinned PIV intermediate replaced by one of the same name whose validity has ended": strings.Join(replaced, ""),
		"a pinned one removed and an expired PIV-named extra added":                           strings.Join(removed, "") + extra,
	} {
		v, err := pivattest.BuildFromAt(f.rootPEM, []byte(bundle), pivattest.Pins(), t2030)
		if v != nil || !errors.Is(err, pivattest.ErrPinTable) || errors.Is(err, pivattest.ErrClock) {
			t.Errorf("%s: %v, %v, want ErrPinTable and never ErrClock", name, v, err)
		}
	}
	// the files are the pinned ones and a PIV-named extra has expired: the extra is no row of the table, so at a good clock nothing is wrong,
	// and at a stale clock the pinned certificate that the clock refuses is named, not the extra
	withExtra := []byte(string(f.bundlePEM) + extra)
	if v, err := pivattest.BuildFromAt(f.rootPEM, withExtra, pivattest.Pins(), t2030); err != nil || v == nil {
		t.Errorf("an expired PIV-named extra at a good clock: %v", err)
	}
	newest, _ := validityOf(t, target)
	_, err := pivattest.BuildFromAt(f.rootPEM, withExtra, pivattest.Pins(), newest.Add(-time.Hour))
	if !errors.Is(err, pivattest.ErrClock) || !strings.Contains(err.Error(), target) || strings.Contains(err.Error(), "TEST EXTRA") {
		t.Errorf("a stale clock with an extra: %v", err)
	}
}
