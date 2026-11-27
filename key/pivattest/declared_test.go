package pivattest_test

import (
	"bytes"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvity/atoom/key/pivattest"
	"github.com/truvity/atoom/key/pivattest/attesttest"
)

// The three declared differences from the check this library replaces, each pinned by tests of its own:
//   1. the caller's certificate is not changed;
//   2. a nil argument is an error, not a panic, and a verifier with no anchors never falls back to the system's roots;
//   3. VerifyAt takes the time.

// ---- 1. the caller's certificate is not changed ---------------------------------------------------------------------------------------

// twin is a fresh parse of the same DER: what the certificate was before any call.
func twin(t *testing.T, c *x509.Certificate) *x509.Certificate {
	t.Helper()
	n, err := x509.ParseCertificate(c.Raw)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestNoCallChangesTheCertificatesOfTheCaller(t *testing.T) {
	for _, tt := range []struct {
		name string
		f9   attesttest.F9Options
		slot attesttest.SlotOptions
	}{
		{"a signer with no Basic Constraints (the workaround applies)", attesttest.F9Options{NoBasicConstraints: true}, attesttest.SlotOptions{}},
		{"a signer with Basic Constraints", attesttest.F9Options{}, attesttest.SlotOptions{}},
		{"a signer that is no CA (refused)", attesttest.F9Options{NotCA: true}, attesttest.SlotOptions{}},
		{"a signer with no Basic Constraints and a refused slot certificate", attesttest.F9Options{NoBasicConstraints: true}, attesttest.SlotOptions{NotAfter: t2015}},
		{"a signer with no Basic Constraints and a malformed extension", attesttest.F9Options{NoBasicConstraints: true},
			attesttest.SlotOptions{Extensions: []pkix.Extension{attesttest.Ext(attesttest.OIDFirmware, false, []byte{1})}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := fixedPKI(t, attesttest.PKIOptions{F9: tt.f9})
			v := verifierOf(t, p)
			slot := fixedSlot(t, p, tt.slot)
			signer := p.F9
			slotBefore, signerBefore := twin(t, slot), twin(t, signer)
			rawSlot, rawSigner := bytes.Clone(slot.Raw), bytes.Clone(signer.Raw)
			noBC := !signer.BasicConstraintsValid
			calls := map[string]func() error{
				"Verify":                               func() error { _, err := v.Verify(slot, signer); return err },
				"VerifyAt":                             func() error { _, err := v.VerifyAt(t2030, slot, signer); return err },
				"VerifyAt, the arguments swapped":      func() error { _, err := v.VerifyAt(t2030, signer, slot); return err },
				"VerifyAt, the same certificate twice": func() error { _, err := v.VerifyAt(t2030, signer, signer); return err },
				"VerifyDER":                            func() error { _, err := v.VerifyDER(slot.Raw, signer.Raw); return err },
			}
			for name, call := range calls {
				_ = call()
				if !reflect.DeepEqual(slot, slotBefore) || !reflect.DeepEqual(signer, signerBefore) {
					t.Fatalf("%s changed a certificate", name)
				}
				if !bytes.Equal(slot.Raw, rawSlot) || !bytes.Equal(signer.Raw, rawSigner) {
					t.Fatalf("%s changed the DER of a certificate", name)
				}
				if noBC && (signer.BasicConstraintsValid || signer.IsCA) {
					t.Fatalf("%s: the workaround reached the signer of the caller (BasicConstraintsValid %v, IsCA %v)", name, signer.BasicConstraintsValid, signer.IsCA)
				}
			}
		})
	}
}

// The workaround applies to the signer only: a slot certificate that states no Basic Constraints is not made a CA, and so is not accepted
// as the signer of a chain (the same certificate, given as the signer, is no CA).
func TestTheWorkaroundIsForTheSignerOnly(t *testing.T) {
	p := fixedPKI(t, attesttest.PKIOptions{})
	v := verifierOf(t, p)
	slot := fixedSlot(t, p, attesttest.SlotOptions{})
	if slot.BasicConstraintsValid {
		t.Fatal("the test slot certificate states Basic Constraints")
	}
	// a leaf under a slot certificate that has no Basic Constraints: the workaround is for the signer, which here is the slot certificate
	// of another call, and it is not an anchor-signed certificate: the chain does not reach an anchor, so it is refused either way.
	other := fixedSlot(t, p, attesttest.SlotOptions{})
	if _, err := v.VerifyAt(t2030, other, slot); !errors.Is(err, pivattest.ErrChain) {
		t.Fatalf("a slot certificate given as the signer of another: %v", err)
	}
	if slot.BasicConstraintsValid || slot.IsCA {
		t.Fatal("a slot certificate was made a CA")
	}
}

// Calls that share one signer with no Basic Constraints, at the same time (the race detector holds the library to "no write").
func TestConcurrentCallsShareACertificateWithoutAWrite(t *testing.T) {
	p := fixedPKI(t, attesttest.PKIOptions{F9: attesttest.F9Options{NoBasicConstraints: true}})
	v := verifierOf(t, p)
	slot := fixedSlot(t, p, attesttest.SlotOptions{})
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ev, err := v.VerifyAt(t2030, slot, p.F9); err != nil || ev.Slot != 0x9a {
				errs <- fmt.Errorf("got %+v: %w", ev, err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// ---- 2. nil arguments are an error, and no verifier trusts the system's roots -----------------------------------------------------------

func noPanic(t *testing.T, name string, f func() error) error {
	t.Helper()
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("%s panicked: %v", name, r)
			}
		}()
		err = f()
	}()
	return err
}

func TestNilArgumentsAreANamedErrorAndNeverAPanic(t *testing.T) {
	p := fixedPKI(t, attesttest.PKIOptions{})
	v := verifierOf(t, p)
	slot := fixedSlot(t, p, attesttest.SlotOptions{})
	tests := []struct {
		name string
		f    func() (pivattest.Evidence, error)
		want error
	}{
		{"Verify(nil, signer)", func() (pivattest.Evidence, error) { return v.Verify(nil, p.F9) }, pivattest.ErrNil},
		{"Verify(slot, nil)", func() (pivattest.Evidence, error) { return v.Verify(slot, nil) }, pivattest.ErrNil},
		{"Verify(nil, nil)", func() (pivattest.Evidence, error) { return v.Verify(nil, nil) }, pivattest.ErrNil},
		{"VerifyAt(now, nil, signer)", func() (pivattest.Evidence, error) { return v.VerifyAt(t2030, nil, p.F9) }, pivattest.ErrNil},
		{"VerifyAt(now, slot, nil)", func() (pivattest.Evidence, error) { return v.VerifyAt(t2030, slot, nil) }, pivattest.ErrNil},
		{"VerifyAt(now, nil, nil)", func() (pivattest.Evidence, error) { return v.VerifyAt(t2030, nil, nil) }, pivattest.ErrNil},
		{"VerifyAt(zero, slot, signer)", func() (pivattest.Evidence, error) { return v.VerifyAt(time.Time{}, slot, p.F9) }, pivattest.ErrZeroTime},
		{"VerifyDER(nil, signer)", func() (pivattest.Evidence, error) { return v.VerifyDER(nil, p.F9.Raw) }, pivattest.ErrParse},
		{"VerifyDER(slot, nil)", func() (pivattest.Evidence, error) { return v.VerifyDER(slot.Raw, nil) }, pivattest.ErrParse},
		{"VerifyDER(empty, empty)", func() (pivattest.Evidence, error) { return v.VerifyDER([]byte{}, []byte{}) }, pivattest.ErrParse},
		{"a nil verifier", func() (pivattest.Evidence, error) { return (*pivattest.Verifier)(nil).Verify(slot, p.F9) }, pivattest.ErrNoAnchor},
		{"a nil verifier, VerifyAt", func() (pivattest.Evidence, error) { return (*pivattest.Verifier)(nil).VerifyAt(t2030, slot, p.F9) }, pivattest.ErrNoAnchor},
		{"a nil verifier, VerifyDER", func() (pivattest.Evidence, error) { return (*pivattest.Verifier)(nil).VerifyDER(slot.Raw, p.F9.Raw) }, pivattest.ErrNoAnchor},
		{"a zero verifier", func() (pivattest.Evidence, error) { return (&pivattest.Verifier{}).Verify(slot, p.F9) }, pivattest.ErrNoAnchor},
		{"a zero verifier, VerifyAt", func() (pivattest.Evidence, error) { return new(pivattest.Verifier).VerifyAt(t2030, slot, p.F9) }, pivattest.ErrNoAnchor},
		{"a zero verifier, VerifyDER", func() (pivattest.Evidence, error) { return (&pivattest.Verifier{}).VerifyDER(slot.Raw, p.F9.Raw) }, pivattest.ErrNoAnchor},
		// Nil certificates and a nil or zero verifier: the arguments are checked first (the order the doc comment gives), so ErrNil; a DER
		// entry has no certificate argument to check, and the verifier's own check comes first there.
		{"a zero verifier and nil certificates", func() (pivattest.Evidence, error) { return (&pivattest.Verifier{}).Verify(nil, nil) }, pivattest.ErrNil},
		{"a nil verifier and nil certificates", func() (pivattest.Evidence, error) { return (*pivattest.Verifier)(nil).Verify(nil, nil) }, pivattest.ErrNil},
		{"a nil verifier, VerifyAt, nil certificates", func() (pivattest.Evidence, error) { return (*pivattest.Verifier)(nil).VerifyAt(t2030, nil, nil) }, pivattest.ErrNil},
		{"a zero verifier, VerifyDER, nothing", func() (pivattest.Evidence, error) { return (&pivattest.Verifier{}).VerifyDER(nil, nil) }, pivattest.ErrNoAnchor},
		{"a zero verifier, a zero time", func() (pivattest.Evidence, error) { return (&pivattest.Verifier{}).VerifyAt(time.Time{}, slot, p.F9) }, pivattest.ErrZeroTime},
		// A certificate that is not a parsed one (no Raw): refused, with no panic, whatever its fields say.
		{"a certificate that was never parsed", func() (pivattest.Evidence, error) { return v.VerifyAt(t2030, &x509.Certificate{}, &x509.Certificate{}) }, pivattest.ErrNoRaw},
		{"a slot certificate that was never parsed", func() (pivattest.Evidence, error) { return v.VerifyAt(t2030, &x509.Certificate{}, p.F9) }, pivattest.ErrNoRaw},
		{"a signer that was never parsed", func() (pivattest.Evidence, error) { return v.VerifyAt(t2030, slot, &x509.Certificate{}) }, pivattest.ErrNoRaw},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ev pivattest.Evidence
			err := noPanic(t, tt.name, func() error {
				var err error
				ev, err = tt.f()
				return err
			})
			if !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
			if !reflect.DeepEqual(ev, pivattest.Evidence{}) {
				t.Fatalf("an error with an evidence: %+v", ev)
			}
		})
	}
}

// A verifier built from nothing (a copy of the zero value, a value in a struct) refuses everything: the zero Verifier has no anchors.
func TestAZeroVerifierRefusesEverythingEvenWithGoodCertificates(t *testing.T) {
	p := fixedPKI(t, attesttest.PKIOptions{})
	slot := fixedSlot(t, p, attesttest.SlotOptions{})
	type holder struct{ V pivattest.Verifier }
	var h holder
	good := verifierOf(t, p)
	copyOfGood := *good // a copy of a verifier works (it holds a pool by reference) and the zero value of the struct does not
	for name, v := range map[string]*pivattest.Verifier{"the zero value": &h.V, "the zero value of a new": new(pivattest.Verifier)} {
		if _, err := v.VerifyAt(t2030, slot, p.F9); !errors.Is(err, pivattest.ErrNoAnchor) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := copyOfGood.VerifyAt(t2030, slot, p.F9); err != nil {
		t.Errorf("a copy of a verifier: %v", err)
	}
}

// A chain that a system root would accept is not accepted by any verifier of this package: the test makes a test root the system's (the
// variable SSL_CERT_FILE, which the standard library reads on Linux and other Unix systems) in a process of its own, shows that the standard
// library accepts the chain there, and shows that the zero verifier, a nil one, Default and a verifier over another PKI refuse it.
func TestNoVerifierFallsBackToTheSystemRoots(t *testing.T) {
	if os.Getenv("ATOOM_PIVATTEST_CHILD") != "" {
		t.Skip("the parent runs this in a child process")
	}
	// the attestation certificate is signed by the root itself, so that "the root is a system root" is enough for the chain
	p := fixedPKI(t, attesttest.PKIOptions{F9: attesttest.F9Options{SignedBy: "root"}})
	slot := fixedSlot(t, p, attesttest.SlotOptions{})
	dir := t.TempDir()
	files := map[string][]byte{"root.pem": p.RootPEM(), "slot.der": slot.Raw, "f9.der": p.F9.Raw}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	empty := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSystemRootsChild$", "-test.v", "-test.count=1") //nolint:gosec // the test binary itself
	cmd.Env = append(os.Environ(), "ATOOM_PIVATTEST_CHILD="+dir, "SSL_CERT_FILE="+filepath.Join(dir, "root.pem"), "SSL_CERT_DIR="+empty)
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err != nil {
		t.Fatalf("the child failed: %v\n%s", err, text)
	}
	if strings.Contains(text, "--- SKIP") {
		if runtime.GOOS == "linux" {
			t.Fatalf("the child skipped on Linux:\n%s", text)
		}
		t.Skipf("this system does not read SSL_CERT_FILE:\n%s", text)
	}
	if !strings.Contains(text, "--- PASS: TestSystemRootsChild") {
		t.Fatalf("the child did not pass:\n%s", text)
	}
}

// TestSystemRootsChild is the child of the test above; it does nothing when run by itself.
func TestSystemRootsChild(t *testing.T) {
	dir := os.Getenv("ATOOM_PIVATTEST_CHILD")
	if dir == "" {
		t.Skip("a child of TestNoVerifierFallsBackToTheSystemRoots")
	}
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // a file the parent wrote
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	slot, err := x509.ParseCertificate(read("slot.der"))
	if err != nil {
		t.Fatal(err)
	}
	f9, err := x509.ParseCertificate(read("f9.der"))
	if err != nil {
		t.Fatal(err)
	}
	inter := x509.NewCertPool()
	inter.AddCert(f9)
	// the control: with no roots given, the standard library takes the system's, and the test root is the system's here
	if _, err := slot.Verify(x509.VerifyOptions{Intermediates: inter, CurrentTime: t2030, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		t.Skipf("the standard library does not take SSL_CERT_FILE on this system: %v", err)
	}
	def, err := pivattest.Default()
	if err != nil {
		t.Fatal(err)
	}
	other := fixedPKI(t, attesttest.PKIOptions{})
	for _, tt := range []struct {
		name     string
		v        *pivattest.Verifier
		noAnchor bool
	}{
		{"a zero verifier", &pivattest.Verifier{}, true}, {"a nil verifier", nil, true}, {"new", new(pivattest.Verifier), true},
		{"Default", def, false}, {"a verifier of another PKI", verifierOf(t, other), false},
	} {
		ev, err := tt.v.VerifyAt(t2030, slot, f9)
		if err == nil {
			t.Errorf("%s accepted a chain that the system's roots accept: %+v", tt.name, ev)
		}
		if tt.noAnchor && !errors.Is(err, pivattest.ErrNoAnchor) {
			t.Errorf("%s: %v, want ErrNoAnchor", tt.name, err)
		}
		if !tt.noAnchor && !errors.Is(err, pivattest.ErrChain) {
			t.Errorf("%s: %v, want a chain error", tt.name, err)
		}
	}
}

// ---- 3. VerifyAt takes the time ---------------------------------------------------------------------------------------------------------

// Each certificate of the chain (the slot certificate, the attestation certificate, the PIV intermediate that is the anchor and, when the
// attestation certificate is signed by the root, the root) is made valid from 2020 to 2040 while the others are valid from 2010 to 2099:
// before, inside and after that window the chain is judged by the time given, and at the edges of the window too (both edges are inside).
func TestVerifyAtJudgesEveryCertificateOfTheChain(t *testing.T) {
	type position struct {
		name string
		make func(t *testing.T, from, to time.Time) (*pivattest.Verifier, *x509.Certificate, *x509.Certificate)
	}
	positions := []position{
		{"the slot certificate", func(t *testing.T, from, to time.Time) (*pivattest.Verifier, *x509.Certificate, *x509.Certificate) {
			p := fixedPKI(t, attesttest.PKIOptions{})
			return verifierOf(t, p), fixedSlot(t, p, attesttest.SlotOptions{NotBefore: from, NotAfter: to}), p.F9
		}},
		{"the attestation certificate", func(t *testing.T, from, to time.Time) (*pivattest.Verifier, *x509.Certificate, *x509.Certificate) {
			p := fixedPKI(t, attesttest.PKIOptions{F9: attesttest.F9Options{NotBefore: from, NotAfter: to}})
			return verifierOf(t, p), fixedSlot(t, p, attesttest.SlotOptions{}), p.F9
		}},
		{"the PIV intermediate, which is the anchor", func(t *testing.T, from, to time.Time) (*pivattest.Verifier, *x509.Certificate, *x509.Certificate) {
			p := fixedPKI(t, attesttest.PKIOptions{PIVNotBefore: from, PIVNotAfter: to})
			return verifierOf(t, p), fixedSlot(t, p, attesttest.SlotOptions{}), p.F9
		}},
		{"the root, which is the anchor when the attestation certificate is signed by it", func(t *testing.T, from, to time.Time) (*pivattest.Verifier, *x509.Certificate, *x509.Certificate) {
			p := fixedPKI(t, attesttest.PKIOptions{NotBefore: from, NotAfter: to, PIVNotBefore: t2010, PIVNotAfter: t2099,
				F9: attesttest.F9Options{SignedBy: "root", NotBefore: t2010, NotAfter: t2099}})
			return verifierOf(t, p), fixedSlot(t, p, attesttest.SlotOptions{}), p.F9
		}},
	}
	times := []struct {
		name string
		at   time.Time
		ok   bool
	}{
		{"long before", t2015, false},
		{"one second before the start", t2020.Add(-time.Second), false},
		{"at the start", t2020, true},
		{"inside", t2030, true},
		{"at the end", t2040, true},
		{"one second after the end", t2040.Add(time.Second), false},
		{"long after", t2050, false},
		{"inside, in another zone", t2030.In(time.FixedZone("UTC+14", 14*3600)), true},
		{"one second before the start, in another zone", t2020.Add(-time.Second).In(time.FixedZone("UTC-12", -12*3600)), false},
	}
	for _, pos := range positions {
		t.Run(pos.name, func(t *testing.T) {
			v, slot, signer := pos.make(t, t2020, t2040)
			for _, tt := range times {
				ev, err := v.VerifyAt(tt.at, slot, signer)
				if tt.ok != (err == nil) {
					t.Errorf("%s: %v, want ok %v", tt.name, err, tt.ok)
				}
				if err != nil && !exactlyClass(err, attesttest.ClassChain) {
					t.Errorf("%s: the error is not a chain error: %v", tt.name, err)
				}
				if err == nil && (ev.Slot != 0x9a || ev.Anchor == "") {
					t.Errorf("%s: the evidence is %+v", tt.name, ev)
				}
			}
		})
	}
}

// A zero time is refused by VerifyAt (ErrZeroTime), whatever the certificates; Verify and VerifyDER use the clock. Pinned with windows that hold
// any clock of this century, so that no result depends on the day: a window from 2010 to the end of time is accepted by the entries that use
// the clock, and a slot certificate that ended in 2011 is refused by them.
func TestAZeroTimeIsRefusedAndTheClockEntriesUseTheClock(t *testing.T) {
	end := time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	p := fixedPKI(t, attesttest.PKIOptions{NotBefore: t2010, NotAfter: end})
	v := verifierOf(t, p)
	forever := fixedSlot(t, p, attesttest.SlotOptions{NotBefore: t2010, NotAfter: end})
	ended := fixedSlot(t, p, attesttest.SlotOptions{NotBefore: t2010, NotAfter: time.Date(2011, 1, 1, 0, 0, 0, 0, time.UTC)})
	for name, c := range map[string]*x509.Certificate{"a window to the end of time": forever, "a certificate that ended in 2011": ended} {
		ev, err := v.VerifyAt(time.Time{}, c, p.F9)
		if !errors.Is(err, pivattest.ErrZeroTime) || ev != (pivattest.Evidence{}) {
			t.Errorf("%s: the zero time gave %+v, %v", name, ev, err)
		}
	}
	if _, err := v.Verify(forever, p.F9); err != nil {
		t.Errorf("Verify, valid until the end of time: %v", err)
	}
	if _, err := v.VerifyDER(forever.Raw, p.F9.Raw); err != nil {
		t.Errorf("VerifyDER, valid until the end of time: %v", err)
	}
	if _, err := v.Verify(ended, p.F9); !errors.Is(err, pivattest.ErrChain) {
		t.Errorf("Verify, a certificate that ended in 2011: %v", err)
	}
	if _, err := v.VerifyDER(ended.Raw, p.F9.Raw); !errors.Is(err, pivattest.ErrChain) {
		t.Errorf("VerifyDER, a certificate that ended in 2011: %v", err)
	}
	// the epoch is a time like any other (here before the window)
	if _, err := v.VerifyAt(time.Unix(0, 0), forever, p.F9); !errors.Is(err, pivattest.ErrChain) {
		t.Errorf("the epoch: %v", err)
	}
	// the zero value in another zone is still the zero value
	if _, err := v.VerifyAt(time.Time{}.In(time.FixedZone("UTC+3", 3*3600)), forever, p.F9); !errors.Is(err, pivattest.ErrZeroTime) {
		t.Errorf("the zero instant in another zone: %v", err)
	}
}
