package card

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/truvity/atoom/key/pivattest"
	"github.com/truvity/atoom/key/pivattest/attesttest"
)

// The driver's check through a test PKI. The verifier is what NewAttestationVerifier returns (the library's verifier and fromEvidence); the
// certificates are made at test time, valid from 2010 to 2099, so that no test depends on the clock of the machine in any way that matters in
// this century. The check over the vendor's pinned certificates (verifyPinned) cannot chain a test certificate, so what is held of it here is what
// happens before the chain: the arguments, the limit, and the build of the verifier.

// qpEvery is every named error of the library: a refusal is of exactly one of them.
var qpEvery = []error{
	pivattest.ErrNil, pivattest.ErrNoRaw, pivattest.ErrZeroTime, pivattest.ErrParse, pivattest.ErrTooLarge, pivattest.ErrChain,
	pivattest.ErrFirmwareExt, pivattest.ErrSerialExt, pivattest.ErrPolicyExt, pivattest.ErrFormFactorExt,
	pivattest.ErrRootCount, pivattest.ErrRootPin, pivattest.ErrNoAnchor, pivattest.ErrPinTable, pivattest.ErrAdmission,
}

// qpOnly says that err is of the class want and of no other class of the library.
func qpOnly(err, want error) bool {
	if err == nil {
		return false
	}
	for _, e := range qpEvery {
		if errors.Is(err, e) != errors.Is(want, e) {
			return false
		}
	}
	return true
}

const qpChainPrefix = "error verifying attestation certificate: "

var qpFiller = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 1}

// qpSized makes a certificate of exactly n bytes: an extension of filler bytes, made again until the length is n (the length of an ECDSA
// signature changes by a byte or two from one signature to the next).
func qpSized(t *testing.T, n int, build func(extra pkix.Extension) ([]byte, error)) *x509.Certificate {
	t.Helper()
	fill := n - 1000
	for try := 0; try < 400; try++ {
		der, err := build(attesttest.Ext(qpFiller, false, make([]byte, fill)))
		if err != nil {
			t.Fatal(err)
		}
		if len(der) == n {
			c, err := x509.ParseCertificate(der)
			if err != nil {
				t.Fatal(err)
			}
			return c
		}
		fill += n - len(der)
	}
	t.Fatalf("no certificate of %d bytes in 400 tries", n)
	return nil
}

// qpBigSlot is a slot certificate of n bytes under the PKI's own attestation certificate.
func qpBigSlot(t *testing.T, p *attesttest.PKI, n int) *x509.Certificate {
	t.Helper()
	return qpSized(t, n, func(extra pkix.Extension) ([]byte, error) {
		return p.SlotDER(attesttest.SlotOptions{Extensions: append(attesttest.DefaultExtensions(), extra), NotBefore: windowFrom, NotAfter: windowTo})
	})
}

// qpBigF9 is a PKI that has the same keys as p and an attestation certificate of n bytes, and a slot certificate under it.
func qpBigF9(t *testing.T, p *attesttest.PKI, n int) (att, slot *x509.Certificate) {
	t.Helper()
	var q *attesttest.PKI
	att = qpSized(t, n, func(extra pkix.Extension) ([]byte, error) {
		var err error
		q, err = p.WithF9(attesttest.F9Options{Extensions: []pkix.Extension{extra}, NotBefore: windowFrom, NotAfter: windowTo})
		if err != nil {
			return nil, err
		}
		return q.F9.Raw, nil
	})
	slot, err := q.Slot(attesttest.SlotOptions{NotBefore: windowFrom, NotAfter: windowTo})
	if err != nil {
		t.Fatal(err)
	}
	return att, slot
}

// A good chain gives the value that the extensions state, for a table of slots, serials, firmware versions and policies; the form factor is
// read and dropped. The expected values are written out, not computed.
func TestQPAGoodChainGivesTheValueTheCertificateStates(t *testing.T) {
	p := testPKI(t, "Yubico PIV Attestation B2 1")
	verify := verifierOf(t, p)
	named := func(cn string, ext ...pkix.Extension) *x509.Certificate {
		c, err := p.Slot(attesttest.SlotOptions{CommonName: cn, Extensions: append([]pkix.Extension{}, ext...), NotBefore: windowFrom, NotAfter: windowTo})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	all := func(fw [3]byte, serial int64, pin, touch, form byte) []pkix.Extension {
		return []pkix.Extension{attesttest.Firmware(fw[0], fw[1], fw[2]), attesttest.Serial(serial), attesttest.Policy(pin, touch), attesttest.FormFactor(form)}
	}
	tests := []struct {
		name string
		slot *x509.Certificate
		want Attestation
	}{
		{"slot 9a, firmware 5.8.0, serial 1234567, PIN once, touch never", named("YubiKey PIV Attestation 9a", all([3]byte{5, 8, 0}, 1234567, 2, 1, 4)...), Attestation{Version{5, 8, 0}, 1234567, 0x9a, PINOnce, TouchNever}},
		{"slot 9c, PIN never, touch always", named("YubiKey PIV Attestation 9c", all([3]byte{5, 7, 4}, 1, 1, 2, 4)...), Attestation{Version{5, 7, 4}, 1, 0x9c, PINNever, TouchAlways}},
		{"slot 9d, PIN always (read as none), touch cached", named("YubiKey PIV Attestation 9d", all([3]byte{5, 7, 0}, 7, 3, 3, 4)...), Attestation{Version{5, 7, 0}, 7, 0x9d, 0, TouchCached}},
		{"slot 9e, a biometric PIN policy (read as none)", named("YubiKey PIV Attestation 9e", all([3]byte{5, 8, 0}, 7, 4, 1, 4)...), Attestation{Version{5, 8, 0}, 7, 0x9e, 0, TouchNever}},
		{"slot 82, the other biometric PIN policy (read as none)", named("YubiKey PIV Attestation 82", all([3]byte{5, 8, 0}, 7, 5, 1, 4)...), Attestation{Version{5, 8, 0}, 7, 0x82, 0, TouchNever}},
		{"slot 95, the largest serial, the largest version", named("YubiKey PIV Attestation 95", all([3]byte{255, 254, 253}, 1<<32-1, 1, 1, 0xff)...), Attestation{Version{255, 254, 253}, 1<<32 - 1, 0x95, PINNever, TouchNever}},
		{"a serial of 2^32+5 is cut to 5", named("YubiKey PIV Attestation 9a", all([3]byte{5, 8, 0}, 1<<32+5, 2, 1, 4)...), Attestation{Version{5, 8, 0}, 5, 0x9a, PINOnce, TouchNever}},
		{"serial 0", named("YubiKey PIV Attestation 9a", all([3]byte{5, 8, 0}, 0, 2, 1, 4)...), Attestation{Version{5, 8, 0}, 0, 0x9a, PINOnce, TouchNever}},
		{"the form factor is not kept", named("YubiKey PIV Attestation 9a", all([3]byte{5, 8, 0}, 9, 2, 1, 0x85)...), Attestation{Version{5, 8, 0}, 9, 0x9a, PINOnce, TouchNever}},
		{"a name that gives no slot: slot 0", named("YubiKey PIV Attestation f9", all([3]byte{5, 8, 0}, 9, 2, 1, 4)...), Attestation{Version{5, 8, 0}, 9, 0, PINOnce, TouchNever}},
		{"no policy extension: the zero policies", named("YubiKey PIV Attestation 9a", attesttest.Firmware(5, 8, 0), attesttest.Serial(9)), Attestation{Version{5, 8, 0}, 9, 0x9a, 0, 0}},
		{"no firmware extension: 0.0.0", named("YubiKey PIV Attestation 9a", attesttest.Serial(9), attesttest.Policy(2, 1)), Attestation{Version{}, 9, 0x9a, PINOnce, TouchNever}},
		{"no serial extension: serial 0", named("YubiKey PIV Attestation 9a", attesttest.Firmware(5, 8, 0), attesttest.Policy(2, 1)), Attestation{Version{5, 8, 0}, 0, 0x9a, PINOnce, TouchNever}},
		{"no extension at all", named("YubiKey PIV Attestation 9c"), Attestation{Slot: 0x9c}},
	}
	for _, tt := range tests {
		got, err := verify(p.F9, tt.slot)
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		if got != tt.want {
			t.Errorf("%s: read %+v, want %+v", tt.name, got, tt.want)
		}
	}
}

// Each class of refusal of the library reaches the caller as an error of that class and of no other, with the zero value; the error of the
// chain keeps the text prefix that the driver's log line has always shown.
func TestQPEachRefusalClassReachesTheCaller(t *testing.T) {
	good, rogue := testPKI(t, "Yubico PIV Attestation B2 1"), testPKI(t, "Yubico PIV Attestation B2 1")
	verify := verifierOf(t, good)
	expired, err := good.Slot(attesttest.SlotOptions{NotBefore: windowFrom, NotAfter: windowFrom.AddDate(1, 0, 0)}) // over in 2011
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		att, slot *x509.Certificate
		want      error
	}{
		{"an attestation of another PKI", rogue.F9, slotOf(t, rogue, nil), pivattest.ErrChain},
		{"a slot certificate of another PKI under the right attestation certificate", good.F9, slotOf(t, rogue, nil), pivattest.ErrChain},
		{"a slot certificate that has expired", good.F9, expired, pivattest.ErrChain},
		{"a firmware extension of two bytes", good.F9, slotOf(t, good, []pkix.Extension{attesttest.Ext(attesttest.OIDFirmware, false, []byte{5, 8})}), pivattest.ErrFirmwareExt},
		{"a negative serial", good.F9, slotOf(t, good, []pkix.Extension{attesttest.Ext(attesttest.OIDSerial, false, []byte{0x02, 0x01, 0xff})}), pivattest.ErrSerialExt},
		{"a serial that is not an integer", good.F9, slotOf(t, good, []pkix.Extension{attesttest.Ext(attesttest.OIDSerial, false, []byte{0x04, 0x01, 0x01})}), pivattest.ErrSerialExt},
		{"a PIN policy out of range", good.F9, slotOf(t, good, []pkix.Extension{attesttest.Policy(6, 1)}), pivattest.ErrPolicyExt},
		{"a touch policy out of range", good.F9, slotOf(t, good, []pkix.Extension{attesttest.Policy(1, 4)}), pivattest.ErrPolicyExt},
		{"a form factor extension of two bytes", good.F9, slotOf(t, good, []pkix.Extension{attesttest.Ext(attesttest.OIDFormFactor, false, []byte{1, 2})}), pivattest.ErrFormFactorExt},
		{"a slot certificate that does not parse", good.F9, &x509.Certificate{Raw: []byte{1, 2, 3}}, pivattest.ErrParse},
		{"an attestation certificate that does not parse", &x509.Certificate{Raw: []byte{1, 2, 3}}, slotOf(t, good, nil), pivattest.ErrParse},
		{"a slot certificate with no raw bytes", good.F9, &x509.Certificate{}, pivattest.ErrNoRaw},
		{"an attestation certificate with no raw bytes", &x509.Certificate{}, slotOf(t, good, nil), pivattest.ErrNoRaw},
		{"no attestation certificate", nil, slotOf(t, good, nil), pivattest.ErrNil},
		{"no slot certificate", good.F9, nil, pivattest.ErrNil},
		{"no certificate", nil, nil, pivattest.ErrNil},
	}
	for _, tt := range tests {
		got, err := verify(tt.att, tt.slot)
		if !qpOnly(err, tt.want) {
			t.Errorf("%s: the error is %v, want the class %v and no other", tt.name, err, tt.want)
			continue
		}
		if got != (Attestation{}) {
			t.Errorf("%s: an error with the value %+v", tt.name, got)
		}
		if errors.Is(tt.want, pivattest.ErrChain) && !strings.HasPrefix(err.Error(), qpChainPrefix) {
			t.Errorf("%s: the text of a chain error is %q, want the prefix %q", tt.name, err, qpChainPrefix)
		}
	}
}

// The attestation certificate is the first argument and the slot certificate the second, as the driver's interface (VerifyAttestation) has
// it. The library takes them the other way round: an exchange would give another answer, which is made visible here for each way the driver
// can be written wrong.
func TestQPTheArgumentsAreNotSwapped(t *testing.T) {
	p := testPKI(t, "Yubico PIV Attestation B2 1")
	verify := verifierOf(t, p)
	slot := slotOf(t, p, nil)
	want := Attestation{Version{5, 8, 0}, 1234567, 0x9a, PINOnce, TouchNever}
	if got, err := verify(p.F9, slot); err != nil || got != want {
		t.Fatalf("the right order: %+v, %v, want %+v", got, err, want)
	}
	// the other order: the card's certificate read as the slot certificate states no extension and names no slot, so that it is not the value
	// of the slot certificate, whatever else is said of it
	if got, err := verify(slot, p.F9); err == nil && got == want {
		t.Errorf("the arguments were exchanged and the answer is the same: %+v", got)
	}
	// each argument is where its role is: a certificate that does not parse is named by the role it was given
	_, err := verify(p.F9, &x509.Certificate{Raw: []byte{1}})
	if !qpOnly(err, pivattest.ErrParse) || !strings.Contains(err.Error(), "slot certificate does not parse") {
		t.Errorf("a slot certificate that does not parse: %v", err)
	}
	_, err = verify(&x509.Certificate{Raw: []byte{1}}, slot)
	if !qpOnly(err, pivattest.ErrParse) || !strings.Contains(err.Error(), "signer certificate does not parse") {
		t.Errorf("an attestation certificate that does not parse: %v", err)
	}
	// and the same for the check over the pinned certificates, which cannot give a good answer to a test PKI: the error names the roles
	_, err = verifyPinned(p.F9, &x509.Certificate{Raw: []byte{1}})
	if !qpOnly(err, pivattest.ErrParse) || !strings.Contains(err.Error(), "slot certificate does not parse") {
		t.Errorf("the pinned check, a slot certificate that does not parse: %v", err)
	}
	_, err = verifyPinned(&x509.Certificate{Raw: []byte{1}}, slot)
	if !qpOnly(err, pivattest.ErrParse) || !strings.Contains(err.Error(), "signer certificate does not parse") {
		t.Errorf("the pinned check, an attestation certificate that does not parse: %v", err)
	}
}

// A nil certificate is an error and not a panic, for either argument, in the check over a test PKI and in the check over the pinned
// certificates.
func TestQPANilCertificateIsAnErrorAndNotAPanic(t *testing.T) {
	p := testPKI(t, "Yubico PIV Attestation B2 1")
	verify := verifierOf(t, p)
	slot := slotOf(t, p, nil)
	calls := map[string]func(att, slot *x509.Certificate) (Attestation, error){"test PKI": verify, "pinned": verifyPinned}
	for name, call := range calls {
		for _, in := range []struct {
			what      string
			att, slot *x509.Certificate
		}{{"attestation", nil, slot}, {"slot", p.F9, nil}, {"both", nil, nil}} {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("%s, a nil %s certificate: a panic: %v", name, in.what, r)
					}
				}()
				got, err := call(in.att, in.slot)
				if !qpOnly(err, pivattest.ErrNil) || got != (Attestation{}) {
					t.Errorf("%s, a nil %s certificate: %+v, %v", name, in.what, got, err)
				}
			}()
		}
	}
}

// A certificate of exactly the limit is taken and one byte more is refused, for either argument, before the certificate is parsed.
func TestQPACertificateOfExactlyTheLimitIsTakenAndOneByteMoreIsRefused(t *testing.T) {
	const limit = pivattest.MaxCertificateSize
	p := testPKI(t, "Yubico PIV Attestation B2 1")
	verify := verifierOf(t, p)
	want := Attestation{Version{5, 8, 0}, 1234567, 0x9a, PINOnce, TouchNever}

	slotAt, slotOver := qpBigSlot(t, p, limit), qpBigSlot(t, p, limit+1)
	attAt, slotUnderAtt := qpBigF9(t, p, limit)
	attOver, slotUnderOver := qpBigF9(t, p, limit+1)
	if len(slotAt.Raw) != limit || len(slotOver.Raw) != limit+1 || len(attAt.Raw) != limit || len(attOver.Raw) != limit+1 {
		t.Fatalf("the sizes are %d %d %d %d", len(slotAt.Raw), len(slotOver.Raw), len(attAt.Raw), len(attOver.Raw))
	}

	taken := []struct {
		name      string
		att, slot *x509.Certificate
	}{
		{"a slot certificate of the limit", p.F9, slotAt},
		{"an attestation certificate of the limit", attAt, slotUnderAtt},
	}
	for _, tt := range taken {
		if got, err := verify(tt.att, tt.slot); err != nil || got != want {
			t.Errorf("%s: %+v, %v, want %+v", tt.name, got, err, want)
		}
	}
	refused := []struct {
		name      string
		att, slot *x509.Certificate
	}{
		{"a slot certificate one byte over", p.F9, slotOver},
		{"an attestation certificate one byte over", attOver, slotUnderOver},
		{"both one byte over", attOver, slotOver},
	}
	for _, tt := range refused {
		got, err := verify(tt.att, tt.slot)
		if !qpOnly(err, pivattest.ErrTooLarge) || got != (Attestation{}) {
			t.Errorf("%s: %+v, %v, want ErrTooLarge", tt.name, got, err)
		}
		// the same through the check over the pinned certificates
		got, err = verifyPinned(tt.att, tt.slot)
		if !qpOnly(err, pivattest.ErrTooLarge) || got != (Attestation{}) {
			t.Errorf("%s, pinned: %+v, %v, want ErrTooLarge", tt.name, got, err)
		}
	}
	// the limit is checked before the chain and before the parse: a certificate over the limit that is not even a certificate is too large
	junk := &x509.Certificate{Raw: make([]byte, limit+1)}
	if _, err := verify(p.F9, junk); !qpOnly(err, pivattest.ErrTooLarge) {
		t.Errorf("a slot certificate of junk over the limit: %v", err)
	}
	if _, err := verify(junk, slotOf(t, p, nil)); !qpOnly(err, pivattest.ErrTooLarge) {
		t.Errorf("an attestation certificate of junk over the limit: %v", err)
	}
	// the roles in the text: the sizes are named in the order of the roles
	_, err := verifyPinned(attOver, slotAt)
	if !qpOnly(err, pivattest.ErrTooLarge) || !strings.Contains(err.Error(), fmt.Sprintf("the slot certificate %d, the signer %d", limit, limit+1)) {
		t.Errorf("the pinned check, only the attestation certificate over: %v", err)
	}
	// at the limit, the pinned check goes on to the chain, which a test certificate does not have
	if _, err := verifyPinned(p.F9, slotAt); !qpOnly(err, pivattest.ErrChain) {
		t.Errorf("the pinned check, a slot certificate of the limit: %v", err)
	}
	if _, err := verifyPinned(attAt, slotUnderAtt); !qpOnly(err, pivattest.ErrChain) {
		t.Errorf("the pinned check, an attestation certificate of the limit: %v", err)
	}
}

// An error of the build of the pinned verifier is the answer of the check, before the certificates are looked at, and a failed build is made
// again at the next call (the comment of verifyPinned). The build fails when the clock is before the validity of the pinned certificates, and
// the clock of the test is the fake clock of testing/synctest, which starts in the year 2000; so that no test depends on the machine's
// clock. The verifier of the library is kept for the whole program once it is built, so the test runs alone, in a copy of the test binary:
// an earlier test of the package has already built it.
func TestQPTheErrorOfTheBuildOfThePinnedVerifierIsTheAnswer(t *testing.T) {
	const child = "QP_PINNED_BUILD_CHILD"
	if os.Getenv(child) != "1" {
		exe, err := os.Executable()
		if err != nil {
			t.Skip("the test binary is not known:", err)
		}
		cmd := exec.Command(exe, //nolint:gosec // the test binary itself, run again for one test
			"-test.run=^TestQPTheErrorOfTheBuildOfThePinnedVerifierIsTheAnswer$", "-test.count=1", "-test.v")
		cmd.Env = append(os.Environ(), child+"=1")
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "--- PASS: TestQPTheErrorOfTheBuildOfThePinnedVerifierIsTheAnswer") {
			t.Fatalf("the test alone: %v\n%s", err, out)
		}
		return
	}
	synctest.Test(t, func(t *testing.T) {
		if y := time.Now().UTC().Year(); y != 2000 {
			t.Fatalf("the fake clock is in the year %d", y)
		}
		// no certificate at all: the answer would be ErrNil if the build were not asked first, or if its error were dropped
		if _, err := verifyPinned(nil, nil); !qpOnly(err, pivattest.ErrNoAnchor) {
			t.Fatalf("in the year 2000: %v, want the error of the build (no PIV attestation intermediate chains to the root yet)", err)
		}
		// another call builds again
		if _, err := verifyPinned(nil, nil); !qpOnly(err, pivattest.ErrNoAnchor) {
			t.Fatalf("in the year 2000, a second call: %v", err)
		}
		time.Sleep(30 * 365 * 24 * time.Hour) // the fake clock: the year 2029
		if _, err := verifyPinned(nil, nil); !qpOnly(err, pivattest.ErrNil) {
			t.Fatalf("after the failed build, in the year %d: %v, want the build to succeed and ErrNil", time.Now().UTC().Year(), err)
		}
	})
}
