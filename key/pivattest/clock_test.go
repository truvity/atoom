package pivattest_test

import (
	"crypto/x509"
	"errors"
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
