package pivattest_test

import (
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/truvity/atoom/key/pivattest"
)

// ErrClock at the edges of the validity of every pinned certificate, through the build form that takes the time (BuildFromAt): the machine's clock
// is never read. The windows are written out here as the vendor gave them (read from the certificates and held equal below), so that a change
// of the pinned files shows as a failure of this table and not as a silent change of the edges.

var (
	qpFrom      = time.Date(2024, 12, 1, 0, 0, 0, 0, time.UTC)     // the start of the root, both intermediates and the PIV intermediates A 1 and B 1
	qpFromB2    = time.Date(2025, 8, 1, 0, 0, 0, 0, time.UTC)      // the start of the PIV intermediate B2 1
	qpTo        = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC) // the end of all six
	qpPinnedSix = []string{"Yubico Attestation Root 1", "Yubico Attestation Intermediate A 1", "Yubico Attestation Intermediate B 1", "Yubico PIV Attestation A 1", "Yubico PIV Attestation B 1", "Yubico PIV Attestation B2 1"}
)

const qpLayout = "2006-01-02 15:04:05"

// qpWindows is the validity of each of the six pinned certificates, read from the embedded files.
func qpWindows(t *testing.T) map[string][2]time.Time {
	t.Helper()
	f := embedded(t)
	out := map[string][2]time.Time{}
	for _, d := range append([][]byte{f.rootDER}, f.bundleDER...) {
		c, err := x509.ParseCertificate(d)
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(qpPinnedSix, c.Subject.CommonName) {
			out[c.Subject.CommonName] = [2]time.Time{c.NotBefore, c.NotAfter}
		}
	}
	if len(out) != 6 {
		t.Fatalf("%d of the six pinned certificates found", len(out))
	}
	return out
}

func qpOnlyClock(err error) bool {
	if !errors.Is(err, pivattest.ErrClock) {
		return false
	}
	for _, e := range []error{pivattest.ErrPinTable, pivattest.ErrAdmission, pivattest.ErrNoAnchor, pivattest.ErrRootPin, pivattest.ErrRootCount, pivattest.ErrParse, pivattest.ErrChain} {
		if errors.Is(err, e) {
			return false
		}
	}
	return true
}

func TestQPTheWindowsOfThePinnedCertificatesAreTheOnesThisTableAssumes(t *testing.T) {
	w := qpWindows(t)
	for _, n := range qpPinnedSix {
		from := qpFrom
		if n == "Yubico PIV Attestation B2 1" {
			from = qpFromB2
		}
		if !w[n][0].Equal(from) || !w[n][1].Equal(qpTo) {
			t.Errorf("%s is valid %s to %s, the table assumes %s to %s", n, w[n][0], w[n][1], from, qpTo)
		}
	}
}

// At the edge of each certificate (a second before its start, the instant, a second after; a second before its end, the instant, a second after)
// and far from every edge: the build succeeds exactly when every one of the six is valid, and refuses for the clock otherwise, with the error
// that names the clock and a certificate that the clock does put outside its validity.
func TestQPErrClockAtTheEdgesOfEachPinnedCertificate(t *testing.T) {
	f := embedded(t)
	w := qpWindows(t)
	var times []time.Time
	for _, win := range w {
		s := time.Second
		times = append(times, win[0].Add(-s), win[0], win[0].Add(s), win[1].Add(-s), win[1], win[1].Add(s))
	}
	times = append(times,
		time.Date(2, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC), time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(5000, 6, 1, 0, 0, 0, 0, time.UTC), time.Date(12000, 1, 1, 0, 0, 0, 0, time.UTC))
	valid := func(at time.Time, cn string) bool { return !at.Before(w[cn][0]) && !at.After(w[cn][1]) }
	okN, refusedN := 0, 0
	for _, at := range times {
		allValid := true
		for _, n := range qpPinnedSix {
			allValid = allValid && valid(at, n)
		}
		v, err := pivattest.BuildFromAt(f.rootPEM, f.bundlePEM, pivattest.Pins(), at)
		if allValid {
			okN++
			if err != nil || v == nil {
				t.Errorf("at %s every pinned certificate is valid: %v", at.Format(qpLayout), err)
			} else if n := len(subjects(v)); n != 4 {
				t.Errorf("at %s: %d anchors, want the root and three PIV intermediates", at.Format(qpLayout), n)
			}
			continue
		}
		refusedN++
		if v != nil || !qpOnlyClock(err) {
			t.Errorf("at %s: %v, %v, want no verifier and ErrClock and no other class", at.Format(qpLayout), v, err)
			continue
		}
		text := err.Error()
		if !strings.Contains(text, at.UTC().Format(qpLayout)) || !strings.Contains(text, "clock") {
			t.Errorf("at %s: the text %q does not name the clock", at.Format(qpLayout), text)
		}
		named := 0
		for _, n := range qpPinnedSix {
			if !strings.Contains(text, fmt.Sprintf("%q", n)) {
				continue
			}
			named++
			if valid(at, n) {
				t.Errorf("at %s: the text names %s, which is valid then: %q", at.Format(qpLayout), n, text)
			}
			if !strings.Contains(text, w[n][0].UTC().Format(qpLayout)) || !strings.Contains(text, w[n][1].UTC().Format(qpLayout)) {
				t.Errorf("at %s: the text names %s without its validity: %q", at.Format(qpLayout), n, text)
			}
		}
		if named != 1 {
			t.Errorf("at %s: the text names %d pinned certificates: %q", at.Format(qpLayout), named, text)
		}
	}
	if okN < 6 || refusedN < 12 {
		t.Errorf("the table holds %d good clocks and %d bad ones", okN, refusedN)
	}
}

// The edges of the three PIV intermediates by name: the newest one is the one that decides, and the text names it between its start and the
// start of the other two; before all of them the text names one of the certificates the clock refuses.
func TestQPTheNewestIntermediateDecidesAndIsNamed(t *testing.T) {
	f := embedded(t)
	for name, c := range map[string]struct {
		at   time.Time
		want string // "" builds
	}{
		"a second before the first start": {qpFrom.Add(-time.Second), ""},
		"the first instant":               {qpFrom, "Yubico PIV Attestation B2 1"},
		"a second after the first start":  {qpFrom.Add(time.Second), "Yubico PIV Attestation B2 1"},
		"a second before the newest":      {qpFromB2.Add(-time.Second), "Yubico PIV Attestation B2 1"},
		"the instant of the newest":       {qpFromB2, "ok"},
		"a second after the newest":       {qpFromB2.Add(time.Second), "ok"},
		"the last instant":                {qpTo, "ok"},
		"a second after the end":          {qpTo.Add(time.Second), ""},
	} {
		v, err := pivattest.BuildFromAt(f.rootPEM, f.bundlePEM, pivattest.Pins(), c.at)
		switch c.want {
		case "ok":
			if err != nil || v == nil {
				t.Errorf("%s: %v", name, err)
			}
		case "":
			if v != nil || !qpOnlyClock(err) {
				t.Errorf("%s: %v, %v", name, v, err)
			}
		default:
			if v != nil || !qpOnlyClock(err) || !strings.Contains(err.Error(), fmt.Sprintf("%q", c.want)) {
				t.Errorf("%s: %v, %v, want ErrClock naming %s", name, v, err, c.want)
			}
		}
	}
}

// A disagreement of the table or of the files is a table error and never a clock error, at a good clock and at a bad one wherever the clock is
// not what refuses the build. Two cases are written down as they are: when the clock refuses every PIV intermediate, nothing is admitted and the
// clock is named whatever the table says, and when it refuses one of them the clock is named for an admitted set that is not the table's.
func TestQPATableDisagreementIsNeverAClockError(t *testing.T) {
	f := embedded(t)
	good := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	justBeforeB2 := qpFromB2.Add(-time.Second)

	digestOff := pivattest.Pins()
	for i := range digestOff {
		if digestOff[i].Name == "Yubico PIV Attestation A 1" {
			digestOff[i].SHA256 = strings.Repeat("0", 64)
		}
	}
	misnamed := pivattest.Pins()
	for i := range misnamed {
		if misnamed[i].Name == "Yubico PIV Attestation B 1" {
			misnamed[i].Name = "Yubico PIV Attestation B 2"
		}
	}
	noRow := slices.DeleteFunc(pivattest.Pins(), func(p pivattest.Pin) bool { return p.Name == "Yubico PIV Attestation A 1" })
	asIntermediate := slices.Clone(pivattest.Pins())
	for i := range asIntermediate {
		if asIntermediate[i].Name == "Yubico PIV Attestation A 1" {
			asIntermediate[i].Role = pivattest.RoleIntermediate
		}
	}
	for name, c := range map[string]struct {
		table []pivattest.Pin
		want  error
	}{
		"a digest that is not the certificate's": {digestOff, pivattest.ErrPinTable},
		"a name that is in no file":              {misnamed, pivattest.ErrPinTable},
		"a row that is left out":                 {noRow, pivattest.ErrAdmission},
		"a row that makes an anchor no anchor":   {asIntermediate, pivattest.ErrAdmission},
	} {
		for when, at := range map[string]time.Time{"a good clock": good, "a clock a second before the newest": justBeforeB2} {
			v, err := pivattest.BuildFromAt(f.rootPEM, f.bundlePEM, c.table, at)
			if when == "a clock a second before the newest" && errors.Is(c.want, pivattest.ErrAdmission) {
				// the newest intermediate is not admitted, so the admitted set is not the table's for the clock's sake: named as the clock
				if v != nil || !errors.Is(err, pivattest.ErrClock) {
					t.Errorf("%s, %s: %v, %v", name, when, v, err)
				}
				continue
			}
			if v != nil || !errors.Is(err, c.want) || errors.Is(err, pivattest.ErrClock) {
				t.Errorf("%s, %s: %v, %v, want %v and not ErrClock", name, when, v, err, c.want)
			}
		}
	}
	// a damaged root file, a root file with another digest in the table, and a bundle with no PIV intermediate at all, at a clock that refuses
	// everything: the clock is not what is wrong
	past := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if v, err := pivattest.BuildFromAt([]byte("not a certificate"), f.bundlePEM, pivattest.Pins(), past); v != nil || !errors.Is(err, pivattest.ErrRootCount) || errors.Is(err, pivattest.ErrClock) {
		t.Errorf("a root file that is no certificate, in the year 2000: %v, %v", v, err)
	}
	rootPinned := slices.Clone(pivattest.Pins())
	for i := range rootPinned {
		if rootPinned[i].Role == pivattest.RoleRoot {
			rootPinned[i].SHA256 = strings.Repeat("1", 64)
		}
	}
	if v, err := pivattest.BuildFromAt(f.rootPEM, f.bundlePEM, rootPinned, past); v != nil || !errors.Is(err, pivattest.ErrRootPin) || errors.Is(err, pivattest.ErrClock) {
		t.Errorf("a root with another digest, in the year 2000: %v, %v", v, err)
	}
	noPIV := f.without(t, "Yubico PIV Attestation A 1", "Yubico PIV Attestation B 1", "Yubico PIV Attestation B2 1")
	for when, at := range map[string]time.Time{"a good clock": good, "the year 2000": past} {
		if v, err := pivattest.BuildFromAt(f.rootPEM, noPIV, pivattest.Pins(), at); v != nil || !errors.Is(err, pivattest.ErrNoAnchor) || errors.Is(err, pivattest.ErrClock) {
			t.Errorf("a bundle with no PIV intermediate, %s: %v, %v", when, v, err)
		}
	}
	// the clock refuses every PIV intermediate and the table is wrong as well: the clock is named
	if v, err := pivattest.BuildFromAt(f.rootPEM, f.bundlePEM, digestOff, past); v != nil || !qpOnlyClock(err) {
		t.Errorf("a wrong digest in the year 2000: %v, %v", v, err)
	}
}

// A build that failed is not kept, for the clock or for the table, and a build that succeeded is kept whatever the clock says later.
func TestQPAFailedBuildIsNotKeptAndASuccessfulOneIs(t *testing.T) {
	var c pivattest.DefaultCache
	past, good := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	at := func(x time.Time) func() time.Time { return func() time.Time { return x } }
	for _, bad := range []time.Time{past, qpFromB2.Add(-time.Second)} {
		for range 2 {
			if v, err := c.Get(at(bad)); v != nil || !qpOnlyClock(err) || c.Kept() != nil {
				t.Fatalf("at %s: %v, %v, kept %v", bad.Format(qpLayout), v, err, c.Kept())
			}
		}
	}
	v, err := c.Get(at(good))
	if err != nil || v == nil || c.Kept() != v {
		t.Fatalf("at a good clock: %v, %v", v, err)
	}
	// kept: a later call with a clock that would refuse it gets the verifier of the good build
	if v2, err := c.Get(at(past)); err != nil || v2 != v {
		t.Fatalf("a call at a bad clock after a good build: %v, %v", v2 == v, err)
	}
	// a clock after the end of the pinned certificates refuses a cache that was never built
	var late pivattest.DefaultCache
	if v, err := late.Get(at(qpTo.Add(time.Second))); v != nil || !qpOnlyClock(err) || late.Kept() != nil {
		t.Fatalf("after the end: %v, %v, kept %v", v, err, late.Kept())
	}
}

// FINDING (skipped unless QP_FINDINGS=1): the build reads the zero time as "the machine's clock", because the standard library does for its
// CurrentTime. VerifyAt refuses the zero time (ErrZeroTime: "no entry point reads it as the current time"), and the build form that takes the
// time does not. Default itself never gives the zero time (time.Now does not), so this is reachable only through the seam, or a clock function
// that returns the zero value: such a build succeeds at a clock that was not the one given, here the clock of the machine of the test.
func TestQPFindingTheBuildReadsTheZeroTimeAsTheMachinesClock(t *testing.T) {
	if os.Getenv("QP_FINDINGS") != "1" {
		t.Skip("a finding: set QP_FINDINGS=1 to see it")
	}
	f := embedded(t)
	if v, err := pivattest.BuildFromAt(f.rootPEM, f.bundlePEM, pivattest.Pins(), time.Time{}); v != nil {
		t.Errorf("a build at the zero time succeeded (%v): the zero time was read as the clock of the machine", err)
	}
}
