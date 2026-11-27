//go:build atoom_card

package card

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"reflect"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-piv/piv-go/v2/piv"

	"github.com/truvity/atoom/key/pivattest"
	"github.com/truvity/atoom/key/pivattest/attesttest"
)

// The differential test of the attestation library (key/pivattest): it holds the library to the check this driver uses today, which is
// piv-go's piv.Verifier over the anchors of newVerifier (yubico.go). Both run live on every case of the corpus of key/pivattest/attesttest
// and on freshly made cases, on freshly parsed certificates (piv-go changes the certificate it is given), and must agree on acceptance, on
// every fact returned and on the class and the text of the error. The corpus also holds the old check's answers as committed data, which the
// library's own tests replay on every system with no card; this test is what keeps that data honest.
//
// To write the corpus's results from the old check (after the corpus was rebuilt, or when the library the driver uses changes), run
//
//	ATOOM_PIVATTEST_RECORD=1 go test -tags atoom_card -run TestPivattestDiffCorpus ./key/yubikeypiv/internal/card
//
// from the directory of this module; a value other than 1 is the path of the file to write. The test takes no reader and opens no card.

// outcome is an answer: the golden form, and the full text of an error.
type outcome struct {
	res  attesttest.Result
	text string
}

var timeInText = regexp.MustCompile(`current time \S+`)

// sameText compares the texts of two errors of the same class. The standard library's text of a chain error holds the time of the check.
func sameText(a, b string) bool {
	return timeInText.ReplaceAllString(a, "current time T") == timeInText.ReplaceAllString(b, "current time T")
}

// oldClass names the class of an error of piv-go's verifier by its text (piv-go's errors have no type to ask).
func oldClass(err error) string {
	s := err.Error()
	switch {
	case strings.HasPrefix(s, "error verifying attestation certificate: "):
		return attesttest.ClassChain
	case strings.HasPrefix(s, "parsing extension: expected 3 bytes for firmware version"):
		return attesttest.ClassFirmware
	case strings.HasPrefix(s, "parsing extension: parsing serial number:"), strings.HasPrefix(s, "parsing extension: serial number was negative"):
		return attesttest.ClassSerial
	case strings.HasPrefix(s, "parsing extension: expected 2 bytes from key policy"), strings.HasPrefix(s, "parsing extension: unrecognized pin policy"),
		strings.HasPrefix(s, "parsing extension: unrecognized touch policy"):
		return attesttest.ClassPolicy
	case strings.HasPrefix(s, "parsing extension: expected 1 byte from formfactor"):
		return attesttest.ClassFormFactor
	}
	return "unclassified: " + s
}

func newClass(err error) string {
	switch {
	case errors.Is(err, pivattest.ErrParse):
		return attesttest.ClassParse
	case errors.Is(err, pivattest.ErrChain):
		return attesttest.ClassChain
	case errors.Is(err, pivattest.ErrFirmwareExt):
		return attesttest.ClassFirmware
	case errors.Is(err, pivattest.ErrSerialExt):
		return attesttest.ClassSerial
	case errors.Is(err, pivattest.ErrPolicyExt):
		return attesttest.ClassPolicy
	case errors.Is(err, pivattest.ErrFormFactorExt):
		return attesttest.ClassFormFactor
	}
	return "unclassified: " + err.Error()
}

func errOutcome(class string, err error) outcome {
	return outcome{res: attesttest.Result{Class: class}, text: err.Error()}
}

// oldVerify is the check of today on two DER certificates, freshly parsed.
func oldVerify(v *piv.Verifier, slotDER, signerDER []byte) outcome {
	slot, err := x509.ParseCertificate(slotDER)
	if err != nil {
		return errOutcome(attesttest.ClassParse, err)
	}
	signer, err := x509.ParseCertificate(signerDER)
	if err != nil {
		return errOutcome(attesttest.ClassParse, err)
	}
	a, err := v.Verify(signer, slot) // piv-go takes the attestation certificate first
	if err != nil {
		return errOutcome(oldClass(err), err)
	}
	return outcome{res: attesttest.Result{
		OK: true, Slot: octet(a.Slot.Key), Firmware: [3]int{a.Version.Major, a.Version.Minor, a.Version.Patch}, Serial: a.Serial,
		FormFactor: octet(a.Formfactor), PIN: int(a.PINPolicy), Touch: int(a.TouchPolicy),
	}}
}

// newVerify is the library's check on the same two DER certificates, and the check of its flags.
func newVerify(v *pivattest.Verifier, slotDER, signerDER []byte) (outcome, error) {
	ev, err := v.VerifyDER(slotDER, signerDER)
	if err != nil {
		return errOutcome(newClass(err), err), nil
	}
	slot, perr := x509.ParseCertificate(slotDER)
	if perr != nil {
		return outcome{}, perr
	}
	has := map[string]bool{}
	for _, e := range slot.Extensions {
		has[e.Id.String()] = true
	}
	if ev.HasFirmware != has[attesttest.OIDFirmware.String()] || ev.HasSerial != has[attesttest.OIDSerial.String()] ||
		ev.HasPolicy != has[attesttest.OIDPolicy.String()] || ev.HasFormFactor != has[attesttest.OIDFormFactor.String()] {
		return outcome{}, fmt.Errorf("the flags %v %v %v %v do not say which extensions the certificate holds", ev.HasFirmware, ev.HasSerial, ev.HasPolicy, ev.HasFormFactor)
	}
	return outcome{res: attesttest.Result{
		OK: true, Slot: ev.Slot, Firmware: [3]int{ev.Firmware.Major, ev.Firmware.Minor, ev.Firmware.Patch}, Serial: ev.Serial,
		FormFactor: ev.FormFactor, PIN: int(ev.PIN), Touch: int(ev.Touch),
	}}, nil
}

// same says what differs between the two checks, or "".
func same(old, nw outcome) string {
	if old.res.OK != nw.res.OK {
		return fmt.Sprintf("acceptance differs: old %+v, library %+v", old, nw)
	}
	if old.res.OK {
		if !reflect.DeepEqual(old.res, nw.res) {
			return fmt.Sprintf("the facts differ: old %+v, library %+v", old.res, nw.res)
		}
		return ""
	}
	if old.res.Class != nw.res.Class {
		return fmt.Sprintf("the error class differs: old %q (%s), library %q (%s)", old.res.Class, old.text, nw.res.Class, nw.text)
	}
	// The text of a chain error is the standard library's behind the same prefix, and stays (a log line of the driver holds it). The text of
	// an extension error is the library's own wording; its class is what must agree.
	if old.res.Class == attesttest.ClassChain && !sameText(old.text, nw.text) {
		return fmt.Sprintf("the error text differs: old %q, library %q", old.text, nw.text)
	}
	return ""
}

// oldConstruction is newVerifier of yubico.go, with the class of its error.
func oldConstruction(rootPEM, bundlePEM []byte, sum string) (*piv.Verifier, string) {
	v, err := newVerifier(rootPEM, bundlePEM, sum)
	if err == nil {
		return v, ""
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "does not hold exactly one certificate"):
		return nil, attesttest.ClassRootCount
	case strings.Contains(s, "is not the pinned certificate"):
		return nil, attesttest.ClassRootPin
	case strings.Contains(s, "does not parse"):
		return nil, attesttest.ClassParse
	case strings.Contains(s, "holds no PIV attestation intermediate"):
		return nil, attesttest.ClassNoAnchor
	}
	return nil, "unclassified: " + s
}

func newConstructionClass(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, pivattest.ErrRootCount):
		return attesttest.ClassRootCount
	case errors.Is(err, pivattest.ErrRootPin):
		return attesttest.ClassRootPin
	case errors.Is(err, pivattest.ErrParse):
		return attesttest.ClassParse
	case errors.Is(err, pivattest.ErrNoAnchor):
		return attesttest.ClassNoAnchor
	}
	return "unclassified: " + err.Error()
}

// recordPath is where the results of the old check are written, or "" when the test only compares.
func recordPath() string {
	switch v := os.Getenv("ATOOM_PIVATTEST_RECORD"); v {
	case "":
		return ""
	case "1":
		return "../../../pivattest/attesttest/corpus/corpus.json"
	default:
		return v
	}
}

func pivGoVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, d := range bi.Deps {
			if d.Path == "github.com/go-piv/piv-go/v2" {
				if d.Replace != nil {
					return d.Version + " (replaced by " + d.Replace.Path + ")"
				}
				return d.Version
			}
		}
	}
	return "unknown version"
}

type pair struct {
	old *piv.Verifier
	new *pivattest.Verifier
}

func pairOf(t *testing.T, rootPEM, bundlePEM []byte, sum string) pair {
	t.Helper()
	old, class := oldConstruction(rootPEM, bundlePEM, sum)
	if class != "" {
		t.Fatalf("the old check was not built: %s", class)
	}
	nw, err := pivattest.NewVerifierFromPEM(rootPEM, bundlePEM, sum)
	if err != nil {
		t.Fatalf("the library's verifier was not built: %v", err)
	}
	return pair{old, nw}
}

// The corpus: both checks on every case, and the committed results against the old check (or, with ATOOM_PIVATTEST_RECORD, the new
// results written from it).
func TestPivattestDiffCorpus(t *testing.T) {
	c, err := attesttest.Load()
	if err != nil {
		t.Fatal(err)
	}
	record := recordPath()
	pairs := map[string]pair{}
	for name, p := range c.PKIs {
		pairs[name] = pairOf(t, []byte(p.RootPEM), []byte(p.BundlePEM), p.RootSHA256)
	}
	for i, cs := range c.Cases {
		pr := pairs[cs.PKI]
		old := oldVerify(pr.old, cs.Slot, cs.Signer)
		nw, err := newVerify(pr.new, cs.Slot, cs.Signer)
		if err != nil {
			t.Errorf("%s: %v", cs.Name, err)
			continue
		}
		if d := same(old, nw); d != "" {
			t.Errorf("%s: %s", cs.Name, d)
		}
		if record != "" {
			c.Cases[i].Want = old.res
		} else if !reflect.DeepEqual(old.res, cs.Want) {
			t.Errorf("%s: the old check says %+v, the corpus holds %+v: record it again", cs.Name, old.res, cs.Want)
		}
	}
	for i, k := range c.Constructions {
		_, oldClass := oldConstruction([]byte(k.RootPEM), []byte(k.BundlePEM), k.WantRootSHA256)
		_, newErr := pivattest.NewVerifierFromPEM([]byte(k.RootPEM), []byte(k.BundlePEM), k.WantRootSHA256)
		if newClass := newConstructionClass(newErr); newClass != oldClass {
			t.Errorf("%s: the old check built a verifier with class %q, the library with %q", k.Name, oldClass, newClass)
		}
		if record != "" {
			c.Constructions[i].WantOK, c.Constructions[i].WantClass = oldClass == "", oldClass
		} else if k.WantOK != (oldClass == "") || k.WantClass != oldClass {
			t.Errorf("%s: the old check says %q, the corpus holds ok %v class %q: record it again", k.Name, oldClass, k.WantOK, k.WantClass)
		}
	}
	if record != "" && !t.Failed() {
		c.GoldenSource = "recorded from piv-go " + pivGoVersion() + " (piv.Verifier over the anchors of card.newVerifier) by TestPivattestDiffCorpus of key/yubikeypiv/internal/card, run with ATOOM_PIVATTEST_RECORD"
		if err := c.Write(record); err != nil {
			t.Fatal(err)
		}
		t.Logf("the results of the old check are written to %s", record)
	}
}

// fresh is a PKI made now, and a rogue one with the same names, for the cases made on the spot.
type fresh struct {
	good, rogue *attesttest.PKI
	pair        pair
}

var (
	freshOnce sync.Once
	freshPKI  fresh
	freshErr  error
)

func freshPair(t testing.TB) fresh {
	freshOnce.Do(func() {
		var f fresh
		if f.good, freshErr = attesttest.NewPKI(attesttest.PKIOptions{}); freshErr != nil {
			return
		}
		if f.rogue, freshErr = attesttest.NewPKI(attesttest.PKIOptions{}); freshErr != nil {
			return
		}
		old, class := oldConstruction(f.good.RootPEM(), f.good.BundlePEM(), f.good.RootSHA256())
		if class != "" {
			freshErr = fmt.Errorf("the old check was not built: %s", class)
			return
		}
		nw, err := pivattest.NewVerifierFromPEM(f.good.RootPEM(), f.good.BundlePEM(), f.good.RootSHA256())
		if err != nil {
			freshErr = err
			return
		}
		f.pair = pair{old, nw}
		freshPKI = f
	})
	if freshErr != nil {
		t.Fatal(freshErr)
	}
	return freshPKI
}

var vendorOIDs = []asn1.ObjectIdentifier{attesttest.OIDFirmware, attesttest.OIDSerial, attesttest.OIDPolicy, attesttest.OIDFormFactor}

// randomExt gives the extension of one kind for a random draw: absent, valid, or damaged in one of several ways.
func randomExt(r *rand.Rand, oid asn1.ObjectIdentifier) (pkix.Extension, bool) {
	valid := map[string]pkix.Extension{
		attesttest.OIDFirmware.String():   attesttest.Firmware(octet(r.IntN(256)), octet(r.IntN(256)), octet(r.IntN(256))),
		attesttest.OIDSerial.String():     attesttest.Serial(r.Int64N(1<<40) - 1<<20),
		attesttest.OIDPolicy.String():     attesttest.Policy(octet(r.IntN(7)), octet(r.IntN(5))),
		attesttest.OIDFormFactor.String(): attesttest.FormFactor(octet(r.IntN(256))),
	}[oid.String()]
	switch r.IntN(8) {
	case 0:
		return pkix.Extension{}, false
	case 1: // a random length of random bytes
		b := make([]byte, r.IntN(12))
		for i := range b {
			b[i] = octet(r.IntN(256))
		}
		return attesttest.Ext(oid, false, b), true
	case 2: // the valid value with a byte appended
		return attesttest.Ext(oid, false, append(append([]byte{}, valid.Value...), octet(r.IntN(256)))), true
	case 3: // the valid value cut short
		if len(valid.Value) > 0 {
			return attesttest.Ext(oid, false, valid.Value[:len(valid.Value)-1]), true
		}
		return valid, true
	case 4: // marked critical
		valid.Critical = true
		return valid, true
	}
	return valid, true
}

var slotNames = []string{"9a", "9c", "9d", "9e", "82", "95", "96", "81", "f9", "9A", "09a", "0009c", "100000009a", "x", "", "9a ", "0x9a"}

// freshCase makes one slot certificate from a random draw, with the chain good, or wrong in one way.
func freshCase(t testing.TB, r *rand.Rand, f fresh) (slotDER, signerDER []byte) {
	var ext []pkix.Extension
	for _, oid := range vendorOIDs {
		if e, ok := randomExt(r, oid); ok {
			ext = append(ext, e)
		}
	}
	r.Shuffle(len(ext), func(i, j int) { ext[i], ext[j] = ext[j], ext[i] })
	if ext == nil {
		ext = []pkix.Extension{}
	}
	o := attesttest.SlotOptions{CommonName: "YubiKey PIV Attestation " + slotNames[r.IntN(len(slotNames))], Extensions: ext}
	p, signer := f.good, f.good.F9.Raw
	switch r.IntN(12) {
	case 0:
		p, signer = f.rogue, f.rogue.F9.Raw
	case 1:
		o.NotAfter = time.Now().Add(-time.Minute)
	case 2:
		o.NotBefore = time.Now().Add(time.Hour)
	case 3:
		signer = f.good.PIV.Raw
	}
	der, err := p.SlotDER(o)
	if err != nil {
		t.Fatal(err)
	}
	if r.IntN(20) == 0 {
		der[len(der)-1-r.IntN(16)] ^= 1 << r.IntN(8) // a bit of the signature
	}
	return der, signer
}

func TestPivattestDiffFreshCases(t *testing.T) {
	seed := uint64(time.Now().UnixNano())
	if s := os.Getenv("ATOOM_PIVATTEST_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seed = n
	}
	t.Logf("seed %d (ATOOM_PIVATTEST_SEED repeats the run)", seed)
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)) //nolint:gosec // a draw of test cases, not a secret; the seed is logged so that a run repeats
	f := freshPair(t)
	for i := 0; i < 600; i++ {
		slot, signer := freshCase(t, r, f)
		old := oldVerify(f.pair.old, slot, signer)
		nw, err := newVerify(f.pair.new, slot, signer)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if d := same(old, nw); d != "" {
			t.Fatalf("case %d (seed %d): %s", i, seed, d)
		}
	}
}

// FuzzPivattestDiff mutates the bytes of the four extensions and the common name, signs again, and holds the two checks equal.
func FuzzPivattestDiff(f *testing.F) {
	f.Add([]byte{5, 8, 0}, []byte{0x02, 0x03, 0x12, 0xd6, 0x87}, []byte{2, 1}, []byte{4}, "YubiKey PIV Attestation 9a", byte(0))
	f.Add([]byte{5, 8}, []byte{0x02, 0x01, 0x85, 0x01}, []byte{9, 9}, []byte{}, "YubiKey PIV Attestation 0009C", byte(0))
	f.Add([]byte{}, []byte{}, []byte{}, []byte{}, "YubiKey PIV Attestation ", byte(0x0f))
	f.Fuzz(func(t *testing.T, fw, ser, pol, ff []byte, cn string, absent byte) {
		if strings.ContainsRune(cn, 0) || len(cn) > 64 {
			t.Skip("a name the encoder refuses or an oversize one")
		}
		pr := freshPair(t)
		var ext []pkix.Extension
		for i, v := range [][]byte{fw, ser, pol, ff} {
			if absent&(1<<i) == 0 {
				ext = append(ext, attesttest.Ext(vendorOIDs[i], absent&(0x10<<i) != 0, v))
			}
		}
		if ext == nil {
			ext = []pkix.Extension{}
		}
		slot, err := pr.good.SlotDER(attesttest.SlotOptions{CommonName: cn, Extensions: ext})
		if err != nil {
			t.Skip("the encoder refuses this name")
		}
		signer := pr.good.F9.Raw
		old := oldVerify(pr.pair.old, slot, signer)
		nw, err := newVerify(pr.pair.new, slot, signer)
		if err != nil {
			t.Fatal(err)
		}
		if d := same(old, nw); d != "" {
			t.Fatal(d)
		}
	})
}

// The three deliberate differences of the library, seen against the old check. Each has a test of its own in the library; here the old
// side is shown, so that a change in the old library (a fix of either) is noticed.
func TestPivattestDiffDeliberateDifferences(t *testing.T) {
	f := freshPair(t)
	noBC, err := f.good.WithF9(attesttest.F9Options{NoBasicConstraints: true})
	if err != nil {
		t.Fatal(err)
	}
	slotDER, err := noBC.SlotDER(attesttest.SlotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("the caller's certificate", func(t *testing.T) {
		for _, which := range []string{"old", "library"} {
			slot, signer := mustParse(t, slotDER), mustParse(t, noBC.F9.Raw)
			var verr error
			if which == "old" {
				_, verr = f.pair.old.Verify(signer, slot)
			} else {
				_, verr = f.pair.new.Verify(slot, signer)
			}
			if verr != nil {
				t.Fatalf("%s: %v", which, verr)
			}
			changed := signer.BasicConstraintsValid || signer.IsCA
			if changed != (which == "old") {
				t.Errorf("%s: the signer was changed: %v", which, changed)
			}
		}
	})
	t.Run("a nil certificate", func(t *testing.T) {
		panics := func(do func()) (p bool) {
			defer func() { p = recover() != nil }()
			do()
			return false
		}
		slot := mustParse(t, slotDER)
		if !panics(func() { _, _ = f.pair.old.Verify(nil, slot) }) {
			t.Error("the old check did not panic on a nil attestation certificate")
		}
		if _, err := f.pair.new.Verify(slot, nil); !errors.Is(err, pivattest.ErrNil) {
			t.Errorf("the library gave %v for a nil signer", err)
		}
		if _, err := f.pair.new.Verify(nil, slot); !errors.Is(err, pivattest.ErrNil) {
			t.Errorf("the library gave %v for a nil slot certificate", err)
		}
	})
}

// octet is the low octet of n: a slot key reference, a form factor and the draws below are all one octet.
func octet[T ~int | ~uint32](n T) byte { return byte(n & 0xff) }

func mustParse(t *testing.T, der []byte) *x509.Certificate {
	t.Helper()
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
