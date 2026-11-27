package card

import (
	"crypto/x509"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/truvity/atoom/key/pivattest"
	"github.com/truvity/atoom/key/pivattest/attesttest"
)

// An oracle for the adapter fromEvidence, written from the code the driver had before it used the library, and a replay of the corpus through
// the adapter. The old code is toAttestation of piv.go at the commit before the move to key/pivattest: it took the attestation that piv-go's
// verifier had read (version, serial, form factor, PIN policy, touch policy, slot) and kept:
//
//	Version = the three numbers of piv-go's Version, as they were
//	Serial  = piv-go's Serial, as it was (an uint32)
//	Slot    = the key reference of piv-go's Slot, cut to one octet (a name that gave no slot left piv-go's Slot empty: key 0)
//	PIN     = PINNever for piv-go's "never" (1), PINOnce for "once" (2), and the zero value for every other value: "always" (3), the
//	          two biometric policies (4, 5) and no policy at all (0)
//	Touch   = TouchNever for "never" (1), TouchAlways for "always" (2), TouchCached for "cached" (3), and the zero value for no policy (0)
//	the form factor was dropped, and there was no word for "generated".
//
// piv-go numbered its policies as the vendor's extension does, and so does the library, so a value of the library's type is also the value
// that piv-go held for the same certificate. An absent extension left piv-go's field at its zero value, which is the zero value of the
// library's Evidence as well.

// qpOldRead is what piv-go's reading of a slot certificate held: the input of the old toAttestation.
type qpOldRead struct {
	Major, Minor, Patch int
	Serial              uint32
	SlotKey             uint32 // piv-go's Slot.Key: 0 when the name gave no slot
	Formfactor          int    // read by piv-go, never kept by the driver
	PINPolicy           int    // 0 none, 1 never, 2 once, 3 always, 4 match once, 5 match always
	TouchPolicy         int    // 0 none, 1 never, 2 always, 3 cached
}

// What the old code gave for a PIN policy and for a touch policy, as data. A value that the table does not hold (6, -1) gave the zero value:
// the old switch had no other case.
var (
	qpOldPIN = map[int]PINPolicy{
		0: 0,
		1: PINNever,
		2: PINOnce,
		3: 0,
		4: 0,
		5: 0,
	}
	qpOldTouch = map[int]TouchPolicy{
		0: 0,
		1: TouchNever,
		2: TouchAlways,
		3: TouchCached,
	}
)

// qpOldGave is the old toAttestation.
func qpOldGave(o qpOldRead) Attestation {
	return Attestation{
		Version: Version{o.Major, o.Minor, o.Patch},
		Serial:  o.Serial,
		Slot:    Slot(byte(o.SlotKey)), //nolint:gosec // a key reference is one octet, as in the old code
		PIN:     qpOldPIN[o.PINPolicy],
		Touch:   qpOldTouch[o.TouchPolicy],
	}
}

// The oracle holds itself first: its tables are the ones written above, and the numbers of the card's policies are the ones the old code
// named by piv-go's constants.
func TestQPTheOracleIsTheOldCode(t *testing.T) {
	if PINNever != 1 || PINOnce != 2 || TouchNever != 1 || TouchAlways != 2 || TouchCached != 3 {
		t.Fatalf("the values of the driver's policies changed: %d %d %d %d %d", PINNever, PINOnce, TouchNever, TouchAlways, TouchCached)
	}
	// the type of the answer has the five facts the old code filled, and the evidence that the strict checks of the driver hold (Generated, the
	// facts stated, the key of the signed bytes): another new field is a change of behaviour to be decided here. The five are what is reported.
	var want = []string{"Version", "Serial", "Slot", "PIN", "Touch", "Evidence"}
	rt := reflect.TypeOf(Attestation{})
	if rt.NumField() != len(want) {
		t.Fatalf("Attestation has %d fields, the oracle knows %d", rt.NumField(), len(want))
	}
	for i, n := range want {
		if rt.Field(i).Name != n {
			t.Errorf("field %d is %s, the oracle knows %s", i, rt.Field(i).Name, n)
		}
	}
	if got := qpOldGave(qpOldRead{Major: 5, Minor: 7, Patch: 4, Serial: 9, SlotKey: 0x9c, Formfactor: 4, PINPolicy: 2, TouchPolicy: 3}); got != (Attestation{Version{5, 7, 4}, 9, 0x9c, PINOnce, TouchCached, pivattest.Evidence{}}) {
		t.Errorf("the oracle gives %+v", got)
	}
	for pin, want := range map[int]PINPolicy{-1: 0, 0: 0, 1: PINNever, 2: PINOnce, 3: 0, 4: 0, 5: 0, 6: 0, 255: 0} {
		if got := qpOldGave(qpOldRead{PINPolicy: pin}).PIN; got != want {
			t.Errorf("the old code gave PIN %d for piv-go's %d, the oracle %d", want, pin, got)
		}
	}
	for touch, want := range map[int]TouchPolicy{-1: 0, 0: 0, 1: TouchNever, 2: TouchAlways, 3: TouchCached, 4: 0, 255: 0} {
		if got := qpOldGave(qpOldRead{TouchPolicy: touch}).Touch; got != want {
			t.Errorf("the old code gave touch %d for piv-go's %d, the oracle %d", want, touch, got)
		}
	}
	if got := qpOldGave(qpOldRead{SlotKey: 0x1009a}).Slot; got != 0x9a {
		t.Errorf("a key reference is cut to one octet: %d", got)
	}
}

// qpCombo is one combination of facts. The Evidence of the library and the old reading of piv-go are both made from it, each by its own
// method, and neither is made from the other or from the oracle.
type qpCombo struct {
	hasSlot             bool
	slot                byte
	hasFirmware         bool
	major, minor, patch int
	hasSerial           bool
	serial              uint32
	hasForm             bool
	form                byte
	hasPolicy           bool
	pin, touch          int
	generated           bool
}

func (c qpCombo) evidence() pivattest.Evidence {
	e := pivattest.Evidence{
		Slot: c.slot, HasSlot: c.hasSlot,
		Firmware: pivattest.Version{Major: c.major, Minor: c.minor, Patch: c.patch}, HasFirmware: c.hasFirmware,
		Serial: c.serial, HasSerial: c.hasSerial,
		FormFactor: c.form, HasFormFactor: c.hasForm,
		PIN: pivattest.PINPolicy(c.pin), Touch: pivattest.TouchPolicy(c.touch), HasPolicy: c.hasPolicy,
		Generated: c.generated,
	}
	if c.generated {
		e.Algorithm, e.Anchor = pivattest.AlgEC384, "Yubico PIV Attestation B2 1"
	}
	return e
}

func (c qpCombo) old() qpOldRead {
	return qpOldRead{
		Major: c.major, Minor: c.minor, Patch: c.patch, Serial: c.serial, SlotKey: uint32(c.slot), Formfactor: int(c.form),
		PINPolicy: c.pin, TouchPolicy: c.touch,
	}
}

// A full table, not a sample: every PIN policy and every touch policy that the library's types can hold (the five and six more, 0 and the
// out-of-range ones, with the policy stated), the policy absent, a slot named (the four named ones and the first and last retired ones) and
// not named, a serial absent, 0, 1 and the largest 32-bit value, a firmware absent and three present, a form factor absent and three
// present, and Generated both ways. The driver's value must be the oracle's for every combination.
func TestQPTheAdapterGivesWhatTheOldCodeGaveForEveryCombination(t *testing.T) {
	type pol struct {
		has        bool
		pin, touch int
	}
	pols := []pol{{false, 0, 0}} // the policy absent: both are zero
	for _, pin := range []int{0, 1, 2, 3, 4, 5, 6, -1} {
		for _, touch := range []int{0, 1, 2, 3, 4, -1} {
			pols = append(pols, pol{true, pin, touch})
		}
	}
	type slot struct {
		has bool
		key byte
	}
	slots := []slot{{false, 0}, {true, 0x9a}, {true, 0x9c}, {true, 0x9d}, {true, 0x9e}, {true, 0x82}, {true, 0x95}}
	type serial struct {
		has bool
		n   uint32
	}
	serials := []serial{{false, 0}, {true, 0}, {true, 1}, {true, 1<<32 - 1}}
	type fw struct {
		has                 bool
		major, minor, patch int
	}
	fws := []fw{{false, 0, 0, 0}, {true, 0, 0, 0}, {true, 5, 8, 0}, {true, 255, 254, 253}}
	type form struct {
		has bool
		b   byte
	}
	forms := []form{{false, 0}, {true, 0x04}, {true, 0x85}, {true, 0xff}}

	n := 0
	for _, p := range pols {
		for _, s := range slots {
			for _, sn := range serials {
				for _, f := range fws {
					for _, ff := range forms {
						for _, gen := range []bool{false, true} {
							c := qpCombo{
								hasSlot: s.has, slot: s.key, hasFirmware: f.has, major: f.major, minor: f.minor, patch: f.patch,
								hasSerial: sn.has, serial: sn.n, hasForm: ff.has, form: ff.b, hasPolicy: p.has, pin: p.pin, touch: p.touch, generated: gen,
							}
							n++
							if got, want := fromEvidence(c.evidence()).Reported(), qpOldGave(c.old()); got != want {
								t.Fatalf("%+v: the driver reads %+v, the old code gave %+v", c, got, want)
							}
						}
					}
				}
			}
		}
	}
	if want := len(pols) * len(slots) * len(serials) * len(fws) * len(forms) * 2; n != want || n < 30000 {
		t.Fatalf("%d combinations, want %d", n, want)
	}
}

// The mapping lines one by one, in words, so that a change to one of them is named by its own failure.
func TestQPTheMappingOfEachPolicyIsTheOldOne(t *testing.T) {
	pins := []struct {
		in   pivattest.PINPolicy
		want PINPolicy
	}{
		{pivattest.PINNever, PINNever}, {pivattest.PINOnce, PINOnce}, {pivattest.PINAlways, 0}, {pivattest.PINMatchOnce, 0}, {pivattest.PINMatchAlways, 0}, {0, 0},
	}
	for _, tt := range pins {
		if got := fromEvidence(pivattest.Evidence{PIN: tt.in, HasPolicy: tt.in != 0}).PIN; got != tt.want {
			t.Errorf("PIN %d: the driver reads %d, the old code gave %d", tt.in, got, tt.want)
		}
	}
	touches := []struct {
		in   pivattest.TouchPolicy
		want TouchPolicy
	}{
		{pivattest.TouchNever, TouchNever}, {pivattest.TouchAlways, TouchAlways}, {pivattest.TouchCached, TouchCached}, {0, 0},
	}
	for _, tt := range touches {
		if got := fromEvidence(pivattest.Evidence{Touch: tt.in, HasPolicy: tt.in != 0}).Touch; got != tt.want {
			t.Errorf("touch %d: the driver reads %d, the old code gave %d", tt.in, got, tt.want)
		}
	}
	// no fact leaks into another: each field is read from its own source
	got := fromEvidence(pivattest.Evidence{
		Slot: 0x82, HasSlot: true, Firmware: pivattest.Version{Major: 1, Minor: 2, Patch: 3}, HasFirmware: true, Serial: 4, HasSerial: true,
		FormFactor: 0x85, HasFormFactor: true, PIN: pivattest.PINOnce, Touch: pivattest.TouchCached, HasPolicy: true, Generated: true,
	})
	if want := (Attestation{Version{1, 2, 3}, 4, 0x82, PINOnce, TouchCached, pivattest.Evidence{}}); got.Reported() != want {
		t.Errorf("read %+v, want %+v", got, want)
	}
}

// qpDriverValue is the driver's value for a recorded answer of the old check: the library's evidence at the time of the corpus, through the
// adapter. (The driver's own entry takes the machine's clock; the replay is at the time of the corpus.)
func qpDriverValue(t *testing.T, c *attesttest.Corpus, v *pivattest.Verifier, cs attesttest.Case) (Attestation, error) {
	t.Helper()
	slot, err := x509.ParseCertificate(cs.Slot)
	if err != nil {
		t.Fatalf("%s: %v", cs.Name, err)
	}
	signer, err := x509.ParseCertificate(cs.Signer)
	if err != nil {
		t.Fatalf("%s: %v", cs.Name, err)
	}
	e, err := v.VerifyAt(c.At, slot, signer)
	if err != nil {
		return Attestation{}, err
	}
	return fromEvidence(e), nil
}

func qpCorpusVerifiers(t *testing.T, c *attesttest.Corpus) map[string]*pivattest.Verifier {
	t.Helper()
	out := map[string]*pivattest.Verifier{}
	for name, p := range c.PKIs {
		v, err := pivattest.NewVerifierFromPEM([]byte(p.RootPEM), []byte(p.BundlePEM), p.RootSHA256)
		if err != nil {
			t.Fatalf("the PKI %q: %v", name, err)
		}
		out[name] = v
	}
	return out
}

// qpClassOf names the one class of the library's errors that err is of, or says what it is.
func qpClassOf(err error) string {
	named := []struct {
		class string
		err   error
	}{
		{attesttest.ClassChain, pivattest.ErrChain}, {attesttest.ClassFirmware, pivattest.ErrFirmwareExt}, {attesttest.ClassSerial, pivattest.ErrSerialExt},
		{attesttest.ClassPolicy, pivattest.ErrPolicyExt}, {attesttest.ClassFormFactor, pivattest.ErrFormFactorExt}, {attesttest.ClassParse, pivattest.ErrParse},
		{attesttest.ClassNoAnchor, pivattest.ErrNoAnchor}, {attesttest.ClassRootCount, pivattest.ErrRootCount}, {attesttest.ClassRootPin, pivattest.ErrRootPin},
	}
	var got []string
	for _, n := range named {
		if errors.Is(err, n.err) {
			got = append(got, n.class)
		}
	}
	if len(got) != 1 {
		return fmt.Sprintf("%v: %v", got, err)
	}
	return got[0]
}

func qpTimeDependent(name string) bool {
	return strings.Contains(name, "expired") || strings.Contains(name, "not-yet")
}

// Every case of the corpus, through the driver's path: an accepted case gives the value that the oracle makes of the recorded answer (and the
// form factor is dropped), a refused case gives an error of the recorded class and the zero value, and a chain error keeps its prefix.
func TestQPEveryCaseOfTheCorpusGivesTheDriverTheValueOfTheOracle(t *testing.T) {
	c, err := attesttest.Load()
	if err != nil {
		t.Fatal(err)
	}
	vs := qpCorpusVerifiers(t, c)
	accepted, refused := 0, 0
	seenPIN, seenTouch := map[PINPolicy]bool{}, map[TouchPolicy]bool{}
	for _, cs := range c.Cases {
		t.Run(cs.Name, func(t *testing.T) {
			got, err := qpDriverValue(t, c, vs[cs.PKI], cs)
			if !cs.Want.OK {
				refused++
				if err == nil {
					t.Fatalf("accepted, the corpus holds the class %q", cs.Want.Class)
				}
				if class := qpClassOf(err); class != cs.Want.Class {
					t.Errorf("the class of %q is %s, the corpus holds %s", err, class, cs.Want.Class)
				}
				if got != (Attestation{}) {
					t.Errorf("an error with the value %+v", got)
				}
				return
			}
			accepted++
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			w := cs.Want
			want := qpOldGave(qpOldRead{
				Major: w.Firmware[0], Minor: w.Firmware[1], Patch: w.Firmware[2], Serial: w.Serial, SlotKey: uint32(w.Slot), Formfactor: int(w.FormFactor),
				PINPolicy: w.PIN, TouchPolicy: w.Touch,
			})
			if got.Reported() != want {
				t.Errorf("the driver reads %+v, the oracle makes %+v of the recorded answer %+v", got, want, w)
			}
			seenPIN[got.PIN], seenTouch[got.Touch] = true, true
		})
	}
	if accepted < 140 || refused < 70 || accepted+refused != len(c.Cases) {
		t.Errorf("%d accepted and %d refused of %d cases: a replay that skips is no replay", accepted, refused, len(c.Cases))
	}
	for _, p := range []PINPolicy{0, PINNever, PINOnce} {
		if !seenPIN[p] {
			t.Errorf("no accepted case of the corpus gives the PIN policy %d", p)
		}
	}
	for _, p := range []TouchPolicy{0, TouchNever, TouchAlways, TouchCached} {
		if !seenTouch[p] {
			t.Errorf("no accepted case of the corpus gives the touch policy %d", p)
		}
	}
}

// The entry of the driver itself (verify at the clock, what NewAttestationVerifier returns) on every case of the corpus whose answer does not
// depend on the time: the same acceptance, the same value and the same class as at the time of the corpus. The certificates of the corpus are
// valid for decades on both sides of the time of the corpus, as the replay of the library relies on.
func TestQPTheDriversEntryAnswersTheCorpusAsTheAdapterDoes(t *testing.T) {
	c, err := attesttest.Load()
	if err != nil {
		t.Fatal(err)
	}
	vs := qpCorpusVerifiers(t, c)
	entries := map[string]func(att, slot *x509.Certificate) (Attestation, error){}
	for name, p := range c.PKIs {
		entries[name], err = NewAttestationVerifier([]byte(p.RootPEM), []byte(p.BundlePEM), p.RootSHA256)
		if err != nil {
			t.Fatalf("the PKI %q: %v", name, err)
		}
	}
	n := 0
	for _, cs := range c.Cases {
		if qpTimeDependent(cs.Name) {
			continue
		}
		n++
		want, werr := qpDriverValue(t, c, vs[cs.PKI], cs)
		slot, _ := x509.ParseCertificate(cs.Slot)
		signer, _ := x509.ParseCertificate(cs.Signer)
		got, gerr := entries[cs.PKI](signer, slot) // the attestation certificate first, as the driver's interface has it
		if (gerr == nil) != (werr == nil) {
			t.Errorf("%s: the entry answers %v, the adapter at the time of the corpus %v", cs.Name, gerr, werr)
			continue
		}
		if gerr != nil {
			if qpClassOf(gerr) != qpClassOf(werr) {
				t.Errorf("%s: the class of the entry's error %q is not the class of %q", cs.Name, gerr, werr)
			}
			if got != (Attestation{}) {
				t.Errorf("%s: an error with the value %+v", cs.Name, got)
			}
			continue
		}
		if got.Reported() != want.Reported() {
			t.Errorf("%s: the entry reads %+v, the adapter %+v", cs.Name, got, want)
		}
	}
	if n < 200 {
		t.Errorf("only %d cases replayed", n)
	}
}

// The constructions of the corpus, through NewAttestationVerifier: a verifier is built, or is refused with the class that was recorded.
func TestQPEveryConstructionOfTheCorpusGivesTheDriverItsRecordedAnswer(t *testing.T) {
	c, err := attesttest.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Constructions) < 23 {
		t.Fatalf("%d constructions", len(c.Constructions))
	}
	for _, k := range c.Constructions {
		t.Run(k.Name, func(t *testing.T) {
			v, err := NewAttestationVerifier([]byte(k.RootPEM), []byte(k.BundlePEM), k.WantRootSHA256)
			if k.WantOK != (err == nil) {
				t.Fatalf("built: %v, the corpus holds ok=%v", err, k.WantOK)
			}
			if err != nil {
				if v != nil {
					t.Error("an error with a verifier")
				}
				if class := qpClassOf(err); class != k.WantClass {
					t.Errorf("the class of %q is %s, the corpus holds %s", err, class, k.WantClass)
				}
			} else if v == nil {
				t.Error("no error and no verifier")
			}
		})
	}
}
