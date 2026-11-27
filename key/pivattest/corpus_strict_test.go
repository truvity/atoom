package pivattest_test

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/truvity/atoom/key/pivattest"
	"github.com/truvity/atoom/key/pivattest/attesttest"
)

// The replay of the corpus below is stricter than the one of corpus_test.go: it holds every fact of the Evidence to a value derived outside
// the library (the golden result for what the old check returned; the certificate itself, read by the test's own code, for what the old
// check had no word for), it holds the class of the error to exactly one named error, and it refuses a corpus that could be skipped
// silently: an empty one, one with a case that has no golden result, one with a repeated name, one recorded from this library itself.

var knownClasses = []string{
	attesttest.ClassChain, attesttest.ClassFirmware, attesttest.ClassSerial, attesttest.ClassPolicy, attesttest.ClassFormFactor,
	attesttest.ClassRootCount, attesttest.ClassRootPin, attesttest.ClassParse, attesttest.ClassNoAnchor,
}

// corpusShapeProblems lists what is wrong with a corpus as a corpus (not with the library).
func corpusShapeProblems(c *attesttest.Corpus) []string {
	var out []string
	bad := func(f string, a ...any) { out = append(out, fmt.Sprintf(f, a...)) }
	if len(c.Cases) == 0 {
		bad("the corpus holds no case")
	}
	if len(c.Constructions) == 0 {
		bad("the corpus holds no construction")
	}
	if len(c.PKIs) == 0 {
		bad("the corpus holds no PKI")
	}
	if c.At.IsZero() {
		bad("the corpus has no time for the replay")
	}
	src := strings.ToLower(c.GoldenSource)
	if src == "" || strings.HasPrefix(src, "none") || strings.Contains(src, "not yet confirmed") {
		bad("the results of the corpus were not given by the old library: %q", c.GoldenSource)
	}
	if !strings.Contains(src, "piv-go") {
		bad("the golden source does not name the old library (piv-go): %q", c.GoldenSource)
	}
	names := map[string]bool{}
	for _, cs := range c.Cases {
		switch {
		case cs.Name == "":
			bad("a case has no name")
		case names[cs.Name]:
			bad("the case name %q is twice in the corpus", cs.Name)
		}
		names[cs.Name] = true
		if cs.Want == (attesttest.Result{}) {
			bad("the case %q has no golden result", cs.Name)
		}
		if _, ok := c.PKIs[cs.PKI]; !ok {
			bad("the case %q names the PKI %q, which is not in the corpus", cs.Name, cs.PKI)
		}
		if len(cs.Slot) == 0 || len(cs.Signer) == 0 {
			bad("the case %q has an empty certificate", cs.Name)
		}
		switch {
		case cs.Want.OK && cs.Want.Class != "":
			bad("the case %q is both accepted and in the class %q", cs.Name, cs.Want.Class)
		case !cs.Want.OK && !slices.Contains(knownClasses, cs.Want.Class) && cs.Want != (attesttest.Result{}):
			bad("the case %q has the unknown class %q", cs.Name, cs.Want.Class)
		case !cs.Want.OK && (cs.Want.Slot != 0 || cs.Want.Serial != 0 || cs.Want.FormFactor != 0 || cs.Want.PIN != 0 || cs.Want.Touch != 0 || cs.Want.Firmware != [3]int{}):
			bad("the case %q is refused and still holds facts", cs.Name)
		}
	}
	cnames := map[string]bool{}
	for _, k := range c.Constructions {
		if k.Name == "" || cnames[k.Name] {
			bad("the construction name %q is empty or repeated", k.Name)
		}
		cnames[k.Name] = true
		switch {
		case k.WantOK && k.WantClass != "":
			bad("the construction %q is both built and in the class %q", k.Name, k.WantClass)
		case !k.WantOK && k.WantClass == "":
			bad("the construction %q has no golden result", k.Name)
		case !k.WantOK && !slices.Contains(knownClasses, k.WantClass):
			bad("the construction %q has the unknown class %q", k.Name, k.WantClass)
		}
	}
	return out
}

var sentinels = map[string]error{
	attesttest.ClassChain: pivattest.ErrChain, attesttest.ClassFirmware: pivattest.ErrFirmwareExt, attesttest.ClassSerial: pivattest.ErrSerialExt,
	attesttest.ClassPolicy: pivattest.ErrPolicyExt, attesttest.ClassFormFactor: pivattest.ErrFormFactorExt, attesttest.ClassParse: pivattest.ErrParse,
	attesttest.ClassNoAnchor: pivattest.ErrNoAnchor, attesttest.ClassRootCount: pivattest.ErrRootCount, attesttest.ClassRootPin: pivattest.ErrRootPin,
}

// exactlyClass says whether err is of the named class and of no other class of the package.
func exactlyClass(err error, class string) bool {
	for name, s := range sentinels {
		if errors.Is(err, s) != (name == class) {
			return false
		}
	}
	return !errors.Is(err, pivattest.ErrNil) && !errors.Is(err, pivattest.ErrPinTable) && !errors.Is(err, pivattest.ErrAdmission)
}

// wantEvidence is the evidence a verified slot certificate must give: the facts the old check returned come from the golden result; the
// flags, the key, the algorithm and the anchor come from the certificates, read by this test's own code.
func wantEvidence(slot, signer *x509.Certificate, g attesttest.Result, anchors, pivs map[string]bool) pivattest.Evidence {
	has := map[string]bool{}
	for _, e := range slot.Extensions {
		has[e.Id.String()] = true
	}
	pub := slot.PublicKey
	if pub == nil {
		pub, _ = x509.ParsePKIXPublicKey(slot.RawSubjectPublicKeyInfo)
	}
	// The chain is the slot certificate, the signer when the signer issued it and is not an anchor itself, and the anchor above: the anchor is
	// named by the issuer of the last certificate before it.
	anchor := slot.Issuer.CommonName
	if !anchors[anchor] && bytes.Equal(slot.RawIssuer, signer.RawSubject) {
		anchor = signer.Issuer.CommonName
	}
	// Generated: a slot was read from the name, the signer given issued the slot certificate (the same bytes), and a PIV intermediate that the
	// verifier admitted issued the signer. That is a chain of exactly three; the slot certificate is not an anchor (it is named by a slot).
	generated := g.Slot != 0 && bytes.Equal(slot.RawIssuer, signer.RawSubject) && pivs[signer.Issuer.CommonName] && !anchors[slot.Subject.CommonName]
	if generated {
		anchor = signer.Issuer.CommonName
	}
	return pivattest.Evidence{
		Slot: g.Slot, HasSlot: g.Slot != 0,
		Firmware: pivattest.Version{Major: g.Firmware[0], Minor: g.Firmware[1], Patch: g.Firmware[2]}, HasFirmware: has[attesttest.OIDFirmware.String()],
		Serial: g.Serial, HasSerial: has[attesttest.OIDSerial.String()],
		FormFactor: g.FormFactor, HasFormFactor: has[attesttest.OIDFormFactor.String()],
		PIN: pivattest.PINPolicy(g.PIN), Touch: pivattest.TouchPolicy(g.Touch), HasPolicy: has[attesttest.OIDPolicy.String()],
		Algorithm: independentAlgorithm(pub), PublicKey: pub,
		Anchor: anchor, Generated: generated,
	}
}

func evidenceDiff(got, want pivattest.Evidence) string {
	g, w := got, want
	var out []string
	if !sameKey(g.PublicKey, w.PublicKey) {
		out = append(out, fmt.Sprintf("PublicKey %T differs", g.PublicKey))
	}
	g.PublicKey, w.PublicKey = nil, nil
	if g != w {
		out = append(out, fmt.Sprintf("got %+v, want %+v", g, w))
	}
	return strings.Join(out, "; ")
}

// anchorNames are the names of the trust anchors of a PKI of the corpus: its root, and the PIV-named certificates of its bundle.
func anchorNames(p attesttest.PKIData) map[string]bool {
	out := map[string]bool{}
	for _, f := range []string{p.RootPEM, p.BundlePEM} {
		rest := []byte(f)
		for {
			var blk *pem.Block
			blk, rest = pem.Decode(rest)
			if blk == nil {
				break
			}
			if cert, err := x509.ParseCertificate(blk.Bytes); err == nil && (f == p.RootPEM || strings.HasPrefix(cert.Subject.CommonName, "Yubico PIV Attestation ")) {
				out[cert.Subject.CommonName] = true
			}
		}
	}
	return out
}

// pivNames are the names of the PIV intermediates of a PKI of the corpus that its verifier admits.
func pivNames(p attesttest.PKIData) map[string]bool {
	out := map[string]bool{}
	for n := range anchorNames(p) {
		if strings.HasPrefix(n, "Yubico PIV Attestation ") {
			out[n] = true
		}
	}
	return out
}

// timeDependent says that the answer of a case is about the clock: a replay at the current time is not made for it.
func timeDependent(name string) bool {
	return strings.Contains(name, "expired") || strings.Contains(name, "not-yet")
}

// caseProblems replays one case, three ways (the pointers at the time of the corpus, the same at the same time through a fresh parse, and
// the DER entry), and lists every difference from the golden result.
func caseProblems(c *attesttest.Corpus, vs map[string]*pivattest.Verifier, cs attesttest.Case) []string {
	var out []string
	bad := func(f string, a ...any) { out = append(out, fmt.Sprintf(f, a...)) }
	v, ok := vs[cs.PKI]
	if !ok {
		return []string{fmt.Sprintf("no verifier for the PKI %q", cs.PKI)}
	}
	slot, err := x509.ParseCertificate(cs.Slot)
	if err != nil {
		return []string{"the slot certificate does not parse: " + err.Error()}
	}
	signer, err := x509.ParseCertificate(cs.Signer)
	if err != nil {
		return []string{"the signer does not parse: " + err.Error()}
	}
	ev, err := v.VerifyAt(c.At, slot, signer)
	if cs.Want.OK != (err == nil) {
		bad("acceptance: got error %v, the corpus holds ok=%v", err, cs.Want.OK)
	}
	switch {
	case err != nil:
		if !exactlyClass(err, cs.Want.Class) {
			bad("the class of %q is not exactly %q", err, cs.Want.Class)
		}
		if cs.Want.Class == attesttest.ClassChain && !strings.HasPrefix(err.Error(), "error verifying attestation certificate: ") {
			bad("the text of a chain error does not begin with the old prefix: %q", err)
		}
		if !reflect.DeepEqual(ev, pivattest.Evidence{}) {
			bad("an error with a non-zero evidence: %+v", ev)
		}
	case cs.Want.OK:
		if d := evidenceDiff(ev, wantEvidence(slot, signer, cs.Want, anchorNames(c.PKIs[cs.PKI]), pivNames(c.PKIs[cs.PKI]))); d != "" {
			bad("evidence: %s", d)
		}
	}
	if !timeDependent(cs.Name) {
		ev2, err2 := v.VerifyDER(cs.Slot, cs.Signer)
		if (err == nil) != (err2 == nil) || (err != nil && err.Error() != err2.Error()) {
			// the text of a chain error holds the time of the check: only the class is compared for it
			if err == nil || err2 == nil || cs.Want.Class != attesttest.ClassChain || !exactlyClass(err2, cs.Want.Class) {
				bad("VerifyDER answers %v where VerifyAt answers %v", err2, err)
			}
		}
		if err == nil && err2 == nil && evidenceDiff(ev2, ev) != "" {
			bad("VerifyDER gives other evidence than VerifyAt: %s", evidenceDiff(ev2, ev))
		}
	}
	return out
}

func constructionProblems(k attesttest.Construction) []string {
	v, err := pivattest.NewVerifierFromPEM([]byte(k.RootPEM), []byte(k.BundlePEM), k.WantRootSHA256)
	var out []string
	if k.WantOK != (err == nil) {
		out = append(out, fmt.Sprintf("built: %v, the corpus holds ok=%v", err, k.WantOK))
	}
	if err != nil {
		if !exactlyClass(err, k.WantClass) {
			out = append(out, fmt.Sprintf("the class of %q is not exactly %q", err, k.WantClass))
		}
		if v != nil {
			out = append(out, "an error with a verifier")
		}
	} else if v == nil {
		out = append(out, "no error and no verifier")
	}
	return out
}

func TestTheCorpusCannotBeSkippedSilently(t *testing.T) {
	for _, p := range corpusShapeProblems(loadCorpus(t)) {
		t.Error(p)
	}
}

func TestEveryCaseOfTheCorpusGivesItsGoldenAnswerInEveryFact(t *testing.T) {
	c := loadCorpus(t)
	vs := verifiers(t, c)
	if len(c.Cases) == 0 {
		t.Fatal("the corpus holds no case")
	}
	for _, cs := range c.Cases {
		t.Run(cs.Name, func(t *testing.T) {
			for _, p := range caseProblems(c, vs, cs) {
				t.Error(p + " (" + cs.Why + ")")
			}
		})
	}
}

func TestEveryConstructionOfTheCorpusGivesItsGoldenAnswer(t *testing.T) {
	c := loadCorpus(t)
	if len(c.Constructions) == 0 {
		t.Fatal("the corpus holds no construction")
	}
	for _, k := range c.Constructions {
		t.Run(k.Name, func(t *testing.T) {
			for _, p := range constructionProblems(k) {
				t.Error(p + " (" + k.Why + ")")
			}
		})
	}
}

// The checks above are only worth what they catch: each is run against a corpus that was damaged on purpose, and must report it.
func TestTheCheckOfTheCorpusCatchesADamagedCorpus(t *testing.T) {
	good := loadCorpus(t)
	byName := func(c *attesttest.Corpus, name string) *attesttest.Case {
		for i := range c.Cases {
			if c.Cases[i].Name == name {
				return &c.Cases[i]
			}
		}
		t.Fatalf("no case %q", name)
		return nil
	}
	shape := []struct {
		name   string
		damage func(c *attesttest.Corpus)
	}{
		{"an empty corpus", func(c *attesttest.Corpus) { c.Cases, c.Constructions = nil, nil }},
		{"no cases", func(c *attesttest.Corpus) { c.Cases = nil }},
		{"no constructions", func(c *attesttest.Corpus) { c.Constructions = nil }},
		{"a case without a golden result", func(c *attesttest.Corpus) { byName(c, "chain-good").Want = attesttest.Result{} }},
		{"a repeated case name", func(c *attesttest.Corpus) { c.Cases[1].Name = c.Cases[0].Name }},
		{"a construction without a golden result", func(c *attesttest.Corpus) { c.Constructions[1].WantOK, c.Constructions[1].WantClass = false, "" }},
		{"a corpus recorded from the library itself", func(c *attesttest.Corpus) {
			c.GoldenSource = "not yet confirmed by the old library: given by key/pivattest"
		}},
		{"no source at all", func(c *attesttest.Corpus) { c.GoldenSource = "" }},
		{"an accepted case with a class", func(c *attesttest.Corpus) { byName(c, "chain-good").Want.Class = attesttest.ClassChain }},
	}
	for _, tt := range shape {
		t.Run("shape/"+tt.name, func(t *testing.T) {
			c := copyCorpus(t, good)
			tt.damage(c)
			if len(corpusShapeProblems(c)) == 0 {
				t.Fatal("the damage was not reported")
			}
		})
	}
	if p := corpusShapeProblems(copyCorpus(t, good)); len(p) != 0 {
		t.Fatalf("an undamaged copy has problems: %v", p)
	}
	vs := verifiers(t, good)
	replay := []struct {
		name     string
		caseName string
		damage   func(w *attesttest.Result)
	}{
		{"acceptance flipped to refused", "chain-good", func(w *attesttest.Result) { *w = attesttest.Result{Class: attesttest.ClassChain} }},
		{"a refusal flipped to accepted", "chain-good-rsa", func(w *attesttest.Result) { w.OK = false; w.Class = attesttest.ClassFirmware }},
		{"the class of a refusal", "policy-length-0", func(w *attesttest.Result) { w.Class = attesttest.ClassSerial }},
		{"a slot", "chain-good", func(w *attesttest.Result) { w.Slot = 0x9c }},
		{"a firmware", "chain-good", func(w *attesttest.Result) { w.Firmware[2]++ }},
		{"a serial", "chain-good", func(w *attesttest.Result) { w.Serial++ }},
		{"a form factor", "chain-good", func(w *attesttest.Result) { w.FormFactor++ }},
		{"a PIN policy", "chain-good", func(w *attesttest.Result) { w.PIN++ }},
		{"a touch policy", "chain-good", func(w *attesttest.Result) { w.Touch++ }},
	}
	for _, tt := range replay {
		t.Run("replay/"+tt.name, func(t *testing.T) {
			c := copyCorpus(t, good)
			cs := byName(c, tt.caseName)
			tt.damage(&cs.Want)
			if len(caseProblems(c, vs, *cs)) == 0 {
				t.Fatal("the damage was not reported")
			}
		})
	}
	// the expected evidence itself is held to the certificate: a wrong flag in the expectation is a failure of the check, so the check of the
	// flags is exercised by taking the golden of a case that states every extension and giving it to a certificate that states none.
	t.Run("replay/the golden of another certificate", func(t *testing.T) {
		c := copyCorpus(t, good)
		cs := byName(c, "absent-all")
		cs.Want = byName(c, "chain-good").Want
		if len(caseProblems(c, vs, *cs)) == 0 {
			t.Fatal("the golden of a full certificate was accepted for a bare one")
		}
	})
	t.Run("construction/damaged", func(t *testing.T) {
		k := good.Constructions[0]
		if len(constructionProblems(k)) != 0 {
			t.Fatalf("the baseline construction has problems: %v", constructionProblems(k))
		}
		k.WantOK, k.WantClass = false, attesttest.ClassRootPin
		if len(constructionProblems(k)) == 0 {
			t.Fatal("the damage was not reported")
		}
	})
}
