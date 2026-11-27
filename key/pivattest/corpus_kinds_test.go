package pivattest_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/truvity/atoom/key/pivattest/attesttest"
)

// The kinds of case a careful reader expects in the corpus, counted by name. A kind that shrinks, or whose golden answer is not the one
// that the old check's code gives, fails here: the replay proves the library equals the golden data, and this test proves the golden data
// is the data that is wanted.

type wanted struct {
	kind, name string
	ok         bool
	class      string
}

func wantedCases() []wanted {
	var w []wanted
	ok := func(kind string, names ...string) {
		for _, n := range names {
			w = append(w, wanted{kind: kind, name: n, ok: true})
		}
	}
	refused := func(kind, class string, names ...string) {
		for _, n := range names {
			w = append(w, wanted{kind: kind, name: n, class: class})
		}
	}
	ok("good chain, slot key EC P-256, P-384, RSA 2048/3072/4096, Ed25519, X25519", "key-ec-p256", "key-ec-p384", "key-rsa-2048", "key-rsa-3072", "key-rsa-4096", "key-ed25519", "key-x25519")
	ok("good chain, slot key of a kind a card does not hold", "key-ec-p224", "key-ec-p521", "key-rsa-2048-e3")
	for pin := 0; pin <= 6; pin++ {
		for touch := 0; touch <= 4; touch++ {
			name := fmt.Sprintf("policy-pin%d-touch%d", pin, touch)
			if pin >= 1 && pin <= 5 && touch >= 1 && touch <= 3 {
				ok("the 15 pairs of PIN and touch policy", name)
			} else {
				refused("a PIN or touch policy out of range", attesttest.ClassPolicy, name)
			}
		}
	}
	for _, s := range []string{"9a", "9c", "9d", "9e"} {
		ok("a slot that names its slot", "slot-name-"+fmt.Sprintf("%q", s))
	}
	for s := 0x82; s <= 0x95; s++ {
		ok("a retired key management slot, 82 to 95", "slot-name-"+fmt.Sprintf("%q", fmt.Sprintf("%x", s)))
	}
	ok("f9 and unknown slots (they name no slot)", `slot-name-"f9"`, `slot-name-"80"`, `slot-name-"81"`, `slot-name-"96"`, `slot-name-"97"`, `slot-name-"ff"`)
	refused("a wrong root", attesttest.ClassChain, "good-chain-against-the-other-verifier")
	refused("a rogue chain with the same names", attesttest.ClassChain, "rogue-pki-same-names", "rogue-pki-rsa")
	refused("a signer that does not chain", attesttest.ClassChain, "signer-is-another-card", "signer-is-the-intermediate")
	refused("expired and not yet valid, in the slot certificate and in f9", attesttest.ClassChain, "slot-expired", "slot-not-yet-valid", "f9-expired", "f9-not-yet-valid")
	refused("a SHA-1 signature", attesttest.ClassChain, "slot-signature-rsa-sha1", "f9-signature-sha1")
	refused("a changed body or a changed signature", attesttest.ClassChain, "tamper-slot-signature", "tamper-slot-common-name", "tamper-slot-9a-to-9c", "tamper-f9-signature", "tamper-f9-common-name")
	refused("an unknown critical extension", attesttest.ClassChain, "unknown-critical-extension", "f9-unknown-critical-extension")
	ok("the signer with no Basic Constraints (the workaround)", "f9-no-basic-constraints", "f9-no-basic-constraints-rsa")
	refused("the signer that says it is not a CA", attesttest.ClassChain, "f9-not-ca")
	refused("the firmware extension of 0, 2 and 4 bytes", attesttest.ClassFirmware, "firmware-length-0", "firmware-length-2", "firmware-length-4")
	ok("the firmware extension of 3 bytes", "firmware-5.8.0")
	ok("the serial 0, 2^32, 2^32+5 and trailing bytes", "serial-zero", "serial-2-pow-32", "serial-2-pow-32-plus-5", "serial-trailing-bytes")
	refused("a negative serial", attesttest.ClassSerial, "serial-minus-1", "serial-minus-2-pow-31")
	refused("a serial that is not minimal, or of a wrong tag", attesttest.ClassSerial, "serial-non-minimal", "serial-wrong-tag-octets", "serial-wrong-tag-utf8", "serial-wrong-tag-sequence")
	refused("a policy extension of the wrong length", attesttest.ClassPolicy, "policy-length-0", "policy-length-1", "policy-length-3", "policy-length-4")
	refused("a form factor extension of the wrong length", attesttest.ClassFormFactor, "formfactor-length-0", "formfactor-length-2")
	for _, v := range []string{"0x0", "0x1", "0x4", "0x85", "0xff"} {
		ok("a form factor value", "formfactor-"+v)
	}
	for _, oid := range []string{"1.3.6.1.4.1.41482.3.3", "1.3.6.1.4.1.41482.3.7", "1.3.6.1.4.1.41482.3.8", "1.3.6.1.4.1.41482.3.9"} {
		ok("each extension absent", "absent-"+oid)
		refused("the critical flag on a vendor extension", attesttest.ClassChain, "critical-"+oid)
	}
	ok("every extension absent", "absent-all")
	return w
}

func TestTheCorpusHoldsTheKindsOfCaseAReaderExpects(t *testing.T) {
	c := loadCorpus(t)
	byName := map[string]attesttest.Case{}
	for _, cs := range c.Cases {
		byName[cs.Name] = cs
	}
	counts := map[string]int{}
	for _, w := range wantedCases() {
		cs, ok := byName[w.name]
		if !ok {
			t.Errorf("%s: the corpus has no case %q", w.kind, w.name)
			continue
		}
		counts[w.kind]++
		if cs.Want.OK != w.ok || cs.Want.Class != w.class {
			t.Errorf("%s: the case %q holds ok=%v class=%q, want ok=%v class=%q", w.kind, w.name, cs.Want.OK, cs.Want.Class, w.ok, w.class)
		}
	}
	for kind, n := range map[string]int{
		"the 15 pairs of PIN and touch policy":                                      15,
		"a PIN or touch policy out of range":                                        20,
		"a retired key management slot, 82 to 95":                                   20,
		"good chain, slot key EC P-256, P-384, RSA 2048/3072/4096, Ed25519, X25519": 7,
		"a serial that is not minimal, or of a wrong tag":                           4,
		"each extension absent":                                                     4,
		"the critical flag on a vendor extension":                                   4,
		"expired and not yet valid, in the slot certificate and in f9":              4,
	} {
		if counts[kind] != n {
			t.Errorf("%s: %d cases, want %d", kind, counts[kind], n)
		}
	}
	// the golden facts of the kinds whose facts are the point
	for pin := 1; pin <= 5; pin++ {
		for touch := 1; touch <= 3; touch++ {
			g := byName[fmt.Sprintf("policy-pin%d-touch%d", pin, touch)].Want
			if g.PIN != pin || g.Touch != touch {
				t.Errorf("policy %d %d: the golden holds %d %d", pin, touch, g.PIN, g.Touch)
			}
		}
	}
	for _, s := range []int{0x9a, 0x9c, 0x9d, 0x9e, 0x82, 0x83, 0x8f, 0x90, 0x95} {
		if g := byName["slot-name-"+fmt.Sprintf("%q", fmt.Sprintf("%x", s))].Want; int(g.Slot) != s {
			t.Errorf("slot %x: the golden holds %#x", s, g.Slot)
		}
	}
	for name, serial := range map[string]uint32{"serial-zero": 0, "serial-2-pow-32": 0, "serial-2-pow-32-plus-5": 5, "serial-trailing-bytes": 5, "serial-max-32": 1<<32 - 1, "serial-typical": 1234567} {
		if g := byName[name].Want; !g.OK || g.Serial != serial {
			t.Errorf("%s: the golden holds %+v, want serial %d", name, g, serial)
		}
	}
	// the variants of the common name of the slot certificate
	n := 0
	for name := range byName {
		if strings.HasPrefix(name, "slot-name-") || strings.HasPrefix(name, "slot-prefix-") {
			n++
		}
	}
	if n < 60 {
		t.Errorf("%d common-name variants, want at least 60", n)
	}
	if len(c.Cases) < 218 || len(c.Constructions) < 23 {
		t.Errorf("the corpus shrank to %d cases and %d constructions", len(c.Cases), len(c.Constructions))
	}
}

// The kinds the corpus lacks, as live cases. THESE ARE NOT CONFIRMED BY THE OLD LIBRARY: the answers are those that its code gives, read
// from piv-go's Verifier.Verify (the standard library's x509 Verify with the same options, which checks the validity period of every
// certificate of the chain, the anchor included), and no golden data holds them. The differential test of the card driver is the place to
// record them.
//
// Missing from the corpus: a verification (not the construction of a verifier) at a time when the PIV attestation intermediate, or the root,
// is outside its validity; the PIV intermediate and the root are anchors, and their validity is checked as any other certificate's.
func TestKindsTheCorpusLacksExpiryOfTheAnchors(t *testing.T) {
	tests := []struct {
		name         string
		pki          attesttest.PKIOptions
		at           time.Time
		wantRootPath bool // the attestation certificate is signed by the root itself, so the chain ends in the root
		ok           bool
	}{
		{"the PIV intermediate is valid", attesttest.PKIOptions{}, t2030, false, true},
		{"the PIV intermediate has expired", attesttest.PKIOptions{PIVNotBefore: t2010, PIVNotAfter: t2040}, t2050, false, false},
		{"the PIV intermediate is not valid yet", attesttest.PKIOptions{PIVNotBefore: t2020, PIVNotAfter: t2099}, t2015, false, false},
		{"the PIV intermediate is valid at the time", attesttest.PKIOptions{PIVNotBefore: t2020, PIVNotAfter: t2040}, t2030, false, true},
		{"the root is valid", attesttest.PKIOptions{}, t2030, true, true},
		{"the root has expired", attesttest.PKIOptions{NotBefore: t2010, NotAfter: t2040, PIVNotBefore: t2010, PIVNotAfter: t2099}, t2050, true, false},
		{"the root is not valid yet", attesttest.PKIOptions{NotBefore: t2020, NotAfter: t2099, PIVNotBefore: t2010, PIVNotAfter: t2099}, t2015, true, false},
		// the root and the intermediate above the PIV intermediate are not in the chain of a card of the usual shape: their expiry is not a
		// chain error (it is for the construction of a verifier, which is made at the current time)
		{"the root has expired but the chain ends below it", attesttest.PKIOptions{NotBefore: t2010, NotAfter: t2040, PIVNotBefore: t2010, PIVNotAfter: t2099}, t2050, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := tt.pki
			if o.NotBefore.IsZero() {
				o.NotBefore, o.NotAfter = t2010, t2099
			}
			if tt.wantRootPath {
				o.F9 = attesttest.F9Options{SignedBy: "root", NotBefore: t2010, NotAfter: t2099}
			} else {
				o.F9 = attesttest.F9Options{NotBefore: t2010, NotAfter: t2099}
			}
			p, err := attesttest.NewPKI(o)
			if err != nil {
				t.Fatal(err)
			}
			v := verifierOf(t, p)
			slot := fixedSlot(t, p, attesttest.SlotOptions{})
			ev, err := v.VerifyAt(tt.at, slot, p.F9)
			if tt.ok != (err == nil) || (err != nil && !exactlyClass(err, attesttest.ClassChain)) {
				t.Fatalf("at %s: %v, want ok %v (a chain error otherwise)", tt.at.Format(time.DateOnly), err, tt.ok)
			}
			if err == nil && ev.Anchor == "" {
				t.Fatal("no anchor")
			}
		})
	}
}
