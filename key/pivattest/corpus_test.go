package pivattest_test

import (
	"crypto/x509"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/truvity/atoom/key/pivattest"
	"github.com/truvity/atoom/key/pivattest/attesttest"
)

func loadCorpus(t *testing.T) *attesttest.Corpus {
	t.Helper()
	c, err := attesttest.Load()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// The corpus is the proof that this library answers as the check it replaces did: every case holds the answer of that check, and the replay
// holds the library to it on every system, with no card.
func TestCorpusIsReplayedAtItsTime(t *testing.T) {
	replay(t, func(v *pivattest.Verifier, slot, signer *x509.Certificate, at time.Time) (pivattest.Evidence, error) {
		return v.VerifyAt(at, slot, signer)
	})
}

// The same at the current time: the windows of the corpus are chosen so that the two agree.
func TestCorpusIsReplayedAtTheCurrentTime(t *testing.T) {
	replay(t, func(v *pivattest.Verifier, slot, signer *x509.Certificate, _ time.Time) (pivattest.Evidence, error) {
		return v.Verify(slot, signer)
	})
}

func replay(t *testing.T, do func(*pivattest.Verifier, *x509.Certificate, *x509.Certificate, time.Time) (pivattest.Evidence, error)) {
	t.Helper()
	c := loadCorpus(t)
	vs := verifiers(t, c)
	for _, cs := range c.Cases {
		t.Run(cs.Name, func(t *testing.T) {
			v, ok := vs[cs.PKI]
			if !ok {
				t.Fatalf("the PKI %q is not in the corpus", cs.PKI)
			}
			slot, signer := parse(t, cs.Slot), parse(t, cs.Signer)
			ev, err := do(v, slot, signer, c.At)
			got := resultOf(ev, err)
			if !reflect.DeepEqual(got, cs.Want) {
				t.Fatalf("got %+v, the corpus holds %+v (%s)", got, cs.Want, cs.Why)
			}
			if err != nil {
				if ev != (pivattest.Evidence{}) {
					t.Fatalf("an error with a non-zero evidence: %+v", ev)
				}
				return
			}
			// What the old check had no word for: the flags say exactly which extensions the certificate holds.
			has := map[string]bool{}
			for _, e := range slot.Extensions {
				has[e.Id.String()] = true
			}
			for _, f := range []struct {
				got  bool
				oid  string
				what string
			}{
				{ev.HasFirmware, attesttest.OIDFirmware.String(), "firmware"}, {ev.HasSerial, attesttest.OIDSerial.String(), "serial"},
				{ev.HasPolicy, attesttest.OIDPolicy.String(), "policy"}, {ev.HasFormFactor, attesttest.OIDFormFactor.String(), "form factor"},
			} {
				if f.got != has[f.oid] {
					t.Errorf("the flag of the %s is %v, the extension is present: %v", f.what, f.got, has[f.oid])
				}
			}
			if !ev.HasSlot != (ev.Slot == 0) {
				t.Errorf("HasSlot %v with slot %#x", ev.HasSlot, ev.Slot)
			}
			wantKey := slot.PublicKey
			if wantKey == nil {
				wantKey, _ = x509.ParsePKIXPublicKey(slot.RawSubjectPublicKeyInfo)
			}
			if !reflect.DeepEqual(wantKey, ev.PublicKey) {
				t.Error("the evidence holds another public key than the certificate")
			}
		})
	}
}

func TestCorpusConstructionsAreReplayed(t *testing.T) {
	c := loadCorpus(t)
	for _, k := range c.Constructions {
		t.Run(k.Name, func(t *testing.T) {
			_, err := pivattest.NewVerifierFromPEM([]byte(k.RootPEM), []byte(k.BundlePEM), k.WantRootSHA256)
			if (err == nil) != k.WantOK || classOf(err) != k.WantClass {
				t.Fatalf("got %v (%q), the corpus holds ok %v class %q (%s)", err, classOf(err), k.WantOK, k.WantClass, k.Why)
			}
		})
	}
}

func TestCorpusKeysAreNamedByKind(t *testing.T) {
	c := loadCorpus(t)
	vs := verifiers(t, c)
	want := map[string]pivattest.Algorithm{
		"key-ec-p224": pivattest.AlgUnknown, "key-ec-p256": pivattest.AlgEC256, "key-ec-p384": pivattest.AlgEC384, "key-ec-p521": pivattest.AlgUnknown,
		"key-rsa-2048": pivattest.AlgRSA2048, "key-rsa-2048-e3": pivattest.AlgUnknown, "key-rsa-3072": pivattest.AlgRSA3072, "key-rsa-4096": pivattest.AlgRSA4096,
		"key-ed25519": pivattest.AlgEd25519, "key-x25519": pivattest.AlgX25519,
	}
	seen := 0
	for _, cs := range c.Cases {
		alg, ok := want[cs.Name]
		if !ok {
			continue
		}
		seen++
		ev, err := vs[cs.PKI].VerifyDER(cs.Slot, cs.Signer)
		if err != nil {
			t.Fatalf("%s: %v", cs.Name, err)
		}
		if ev.Algorithm != alg {
			t.Errorf("%s: algorithm %v, want %v", cs.Name, ev.Algorithm, alg)
		}
	}
	if seen != len(want) {
		t.Fatalf("%d of %d key cases are in the corpus", seen, len(want))
	}
}

// Generated is true only for a chain of exactly three certificates (the slot certificate, the signer given, an admitted PIV intermediate) with
// a slot read from the name; the anchor is that of the chain.
func TestCorpusAnchorAndGenerated(t *testing.T) {
	c := loadCorpus(t)
	vs := verifiers(t, c)
	for _, cs := range c.Cases {
		if !cs.Want.OK {
			continue
		}
		ev, err := vs[cs.PKI].VerifyDER(cs.Slot, cs.Signer)
		if err != nil {
			t.Fatalf("%s: %v", cs.Name, err)
		}
		wantAnchor, wantGen := "Yubico PIV Attestation TEST ONLY 1", ev.HasSlot
		switch cs.Name {
		case "f9-signed-by-root":
			wantAnchor, wantGen = "TEST ONLY Yubico Attestation Root 1", false
		case "signer-is-the-piv-intermediate", "signer-and-slot-swapped":
			wantGen = false // a chain of two: the signer is not in it, or the card's certificate is read as the slot certificate
		}
		if ev.Anchor != wantAnchor || ev.Generated != wantGen {
			t.Errorf("%s: anchor %q generated %v, want %q %v", cs.Name, ev.Anchor, ev.Generated, wantAnchor, wantGen)
		}
	}
}

// The corpus is public: every certificate in it is a test certificate, none is a vendor's, and its results say where they came from.
func TestCorpusIsTestOnly(t *testing.T) {
	c := loadCorpus(t)
	if strings.HasPrefix(c.GoldenSource, "none") {
		t.Fatalf("the corpus holds no results: %s", c.GoldenSource)
	}
	pinned := map[string]bool{}
	for _, p := range pivattest.Pins() {
		pinned[p.SHA256] = true
	}
	check := func(what string, der []byte) {
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			return // a certificate that does not parse is a case of its own
		}
		if pinned[attesttest.SHA256(cert)] {
			t.Errorf("%s is a vendor certificate", what)
		}
	}
	for _, cs := range c.Cases {
		check(cs.Name, cs.Slot)
		check(cs.Name, cs.Signer)
	}
	for name, p := range c.PKIs {
		if !strings.Contains(p.RootPEM, "BEGIN CERTIFICATE") {
			t.Errorf("the PKI %q holds no root", name)
		}
		v, _ := pivattest.NewVerifierFromPEM([]byte(p.RootPEM), []byte(p.BundlePEM), p.RootSHA256)
		if v == nil {
			t.Errorf("the PKI %q is not a verifier", name)
		}
		if p.RootSHA256 == pivattest.Pins()[0].SHA256 {
			t.Errorf("the PKI %q has the vendor's root", name)
		}
	}
}

func TestCorpusShape(t *testing.T) {
	c := loadCorpus(t)
	names := map[string]bool{}
	classes := map[string]int{}
	for _, cs := range c.Cases {
		if names[cs.Name] {
			t.Errorf("the case %q is twice in the corpus", cs.Name)
		}
		names[cs.Name] = true
		classes[cs.Want.Class]++
		if cs.Why == "" {
			t.Errorf("the case %q says nothing of why", cs.Name)
		}
	}
	for _, class := range []string{"", attesttest.ClassChain, attesttest.ClassFirmware, attesttest.ClassSerial, attesttest.ClassPolicy, attesttest.ClassFormFactor} {
		if classes[class] == 0 {
			t.Errorf("no case of the class %q", class)
		}
	}
	for _, class := range []string{"", attesttest.ClassRootCount, attesttest.ClassRootPin, attesttest.ClassParse, attesttest.ClassNoAnchor} {
		found := false
		for _, k := range c.Constructions {
			found = found || k.WantClass == class
		}
		if !found {
			t.Errorf("no construction of the class %q", class)
		}
	}
	if len(c.Cases) < 200 {
		t.Errorf("only %d cases", len(c.Cases))
	}
}
