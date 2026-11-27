package pivattest_test

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"testing"

	"github.com/truvity/atoom/key/pivattest"
	"github.com/truvity/atoom/key/pivattest/attesttest"
)

// Generated says that a card whose attestation key the vendor's PIV intermediate certified attested the key: the chain that was verified is
// exactly three certificates (the slot certificate, the signer that was given, one of the intermediates this verifier admitted), and a slot
// was read from the name. These tests hold it to that over every shape a caller can make.

// What was reported before the fix, and is answered: an anchor as the slot certificate is accepted, and is no attestation of a key.
func TestAnAnchorIsNotAnAttestationOfAKey(t *testing.T) {
	p := fixedPKI(t, attesttest.PKIOptions{})
	v := verifierOf(t, p)
	unrelated := fixedSlot(t, p, attesttest.SlotOptions{})
	for name, cert := range map[string]*x509.Certificate{"the PIV intermediate": p.PIV, "the root": p.Root} {
		for sname, signer := range map[string]*x509.Certificate{"itself": cert, "an unrelated certificate": unrelated} {
			ev, err := v.VerifyAt(t2030, cert, signer)
			if err != nil {
				t.Errorf("%s with %s as the signer: %v (what is accepted did not change)", name, sname, err)
				continue
			}
			if ev.Generated || ev.AttestsKey(cert.PublicKey) || ev.AttestsKey(ev.PublicKey) {
				t.Errorf("%s with %s as the signer: %+v", name, sname, ev)
			}
		}
	}
	// the vendor's own: the root, the three PIV intermediates and the two intermediates under the root, through Default
	def, err := pivattest.Default()
	if err != nil {
		t.Fatal(err)
	}
	f := embedded(t)
	accepted := 0
	for _, d := range append([][]byte{f.rootDER}, f.bundleDER...) {
		cert, err := x509.ParseCertificate(d)
		if err != nil {
			t.Fatal(err)
		}
		ev, err := def.VerifyAt(t2030, cert, unrelated)
		if err != nil {
			continue
		}
		accepted++
		if ev.Generated || ev.AttestsKey(cert.PublicKey) {
			t.Errorf("%s: Generated %v, AttestsKey %v", cert.Subject.CommonName, ev.Generated, ev.AttestsKey(cert.PublicKey))
		}
	}
	if accepted != 6 {
		t.Errorf("%d vendor certificates accepted as a slot certificate, want 6 (the root, three PIV intermediates, two intermediates)", accepted)
	}
}

type generatedCase struct {
	name      string
	slot, sig *x509.Certificate
	err       error // nil: accepted
	generated bool
}

// The misuse table, every row at once: what is accepted, and what Generated says.
func TestGeneratedOverTheWholeMisuseTable(t *testing.T) {
	p := fixedPKI(t, attesttest.PKIOptions{})
	v := verifierOf(t, p)
	good := fixedSlot(t, p, attesttest.SlotOptions{})
	noSlot := fixedSlot(t, p, attesttest.SlotOptions{CommonName: "YubiKey PIV Attestation f9"})
	notNamed := fixedSlot(t, p, attesttest.SlotOptions{CommonName: "TEST ONLY something"})
	byPIV := fixedSlot(t, p, attesttest.SlotOptions{SignedByPIV: true})
	// an attestation certificate whose own name reads as a slot: a chain of two when it is read as the slot certificate
	named := fixedPKI(t, attesttest.PKIOptions{F9: attesttest.F9Options{CommonName: "YubiKey PIV Attestation 9a"}})
	namedV := verifierOf(t, named)
	namedSlot := fixedSlot(t, named, attesttest.SlotOptions{})
	// the card's attestation certificate signed by the root
	rooted := fixedPKI(t, attesttest.PKIOptions{F9: attesttest.F9Options{SignedBy: "root"}})
	rootedV := verifierOf(t, rooted)
	rootedSlot := fixedSlot(t, rooted, attesttest.SlotOptions{})

	tests := []struct {
		v     *pivattest.Verifier
		cases []generatedCase
	}{
		{v, []generatedCase{
			{"the good chain", good, p.F9, nil, true},
			{"a name that gives no slot (f9)", noSlot, p.F9, nil, false},
			{"a name that is no slot name", notNamed, p.F9, nil, false},
			{"signed by the PIV intermediate, any signer", byPIV, p.F9, nil, false},
			{"signed by the PIV intermediate, the intermediate as signer", byPIV, p.PIV, nil, false},
			{"the arguments swapped", p.F9, good, nil, false},
			{"the attestation certificate twice", p.F9, p.F9, nil, false},
			{"the slot certificate twice", good, good, pivattest.ErrChain, false},
			{"the PIV intermediate as the slot certificate", p.PIV, p.F9, nil, false},
			{"the PIV intermediate twice", p.PIV, p.PIV, nil, false},
			{"the root as the slot certificate", p.Root, p.F9, nil, false},
			{"the signer is the PIV intermediate, the slot certificate is signed by f9", good, p.PIV, pivattest.ErrChain, false},
			{"the signer is the root", good, p.Root, pivattest.ErrChain, false},
		}},
		{namedV, []generatedCase{
			{"the good chain, the attestation certificate named as a slot", namedSlot, named.F9, nil, true},
			{"the attestation certificate read as the slot certificate (named as a slot, a chain of two)", named.F9, namedSlot, nil, false},
			{"the attestation certificate twice (named as a slot)", named.F9, named.F9, nil, false},
		}},
		{rootedV, []generatedCase{
			{"the chain ends in the root", rootedSlot, rooted.F9, nil, false},
		}},
	}
	for _, g := range tests {
		for _, c := range g.cases {
			t.Run(c.name, func(t *testing.T) {
				ev, err := g.v.VerifyAt(t2030, c.slot, c.sig)
				if c.err != nil {
					if !errors.Is(err, c.err) || ev != (pivattest.Evidence{}) {
						t.Fatalf("%+v, %v, want %v", ev, err, c.err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if ev.Generated != c.generated {
					t.Errorf("Generated %v, want %v (evidence %+v)", ev.Generated, c.generated, ev)
				}
				if ev.AttestsKey(c.slot.PublicKey) != c.generated {
					t.Errorf("AttestsKey of the key of the first argument is %v, want %v", ev.AttestsKey(c.slot.PublicKey), c.generated)
				}
				if c.generated && ev.Anchor != testAnchor {
					t.Errorf("the anchor of a generated chain is %q", ev.Anchor)
				}
			})
		}
	}
}

// How many accepted cases of the corpus are Generated: exactly the three-certificate shape with a slot name. The counts are written out so
// that a change in either direction is seen.
func TestGeneratedOverTheAcceptedCasesOfTheCorpus(t *testing.T) {
	c := loadCorpus(t)
	vs := verifiers(t, c)
	accepted, generated, noSlot := 0, 0, 0
	notGenerated := map[string]bool{}
	for _, cs := range c.Cases {
		if !cs.Want.OK {
			continue
		}
		accepted++
		slot, signer := parse(t, cs.Slot), parse(t, cs.Signer)
		ev, err := vs[cs.PKI].VerifyAt(c.At, slot, signer)
		if err != nil {
			t.Fatalf("%s: %v", cs.Name, err)
		}
		want := wantEvidence(slot, signer, cs.Want, anchorNames(c.PKIs[cs.PKI]), pivNames(c.PKIs[cs.PKI])).Generated
		if ev.Generated != want {
			t.Errorf("%s: Generated %v, want %v", cs.Name, ev.Generated, want)
		}
		if ev.AttestsKey(ev.PublicKey) != ev.Generated {
			t.Errorf("%s: AttestsKey of its own key is %v, Generated %v", cs.Name, ev.AttestsKey(ev.PublicKey), ev.Generated)
		}
		switch {
		case ev.Generated:
			generated++
		case !ev.HasSlot:
			noSlot++
			notGenerated[cs.Name] = true
		default:
			notGenerated[cs.Name] = true
		}
	}
	// the cases that are accepted with a slot name and are still not Generated: no chain of three
	for _, name := range []string{"f9-signed-by-root", "signer-is-the-piv-intermediate"} {
		if !notGenerated[name] {
			t.Errorf("%s is Generated", name)
		}
	}
	t.Logf("%d accepted cases: %d Generated, %d not (%d of them with no slot in the name)", accepted, generated, accepted-generated, noSlot)
	if accepted != 140 || generated != 107 || noSlot != 31 {
		t.Errorf("the counts changed: %d accepted, %d Generated, %d with no slot (want 140, 107, 31)", accepted, generated, noSlot)
	}
}

// ---- two chains at once, and the verifier of NewVerifierFromPEM ----

// An intermediate that the vendor cross-certified: two certificates with the same name and key, both admitted. A slot certificate then has two
// chains. Generated is true, the anchor is the same whichever order the bundle is in, and a verifier with only one of them gives the same.
func TestTwoChainsAtOnce(t *testing.T) {
	p := newOwnPKI(t)
	twin := ownCert(t, ownCA(p.piv.Subject.CommonName), p.root, &p.pivKey.PublicKey, p.rootKey)
	if attesttest.SHA256(twin) == attesttest.SHA256(p.piv) {
		t.Fatal("the twin is the same certificate")
	}
	root, sum := attesttest.PEM(p.root), attesttest.SHA256(p.root)
	card := p.card(t)
	slot := card.slot(t, "YubiKey PIV Attestation 9a")
	var first pivattest.Evidence
	for name, bundle := range map[string][]byte{"both, one order": attesttest.PEM(p.piv, twin), "both, the other order": attesttest.PEM(twin, p.piv), "only the twin": attesttest.PEM(twin), "only the first": attesttest.PEM(p.piv)} {
		v, err := pivattest.NewVerifierFromPEM(root, bundle, sum)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for i := 0; i < 20; i++ { // the standard library's order of chains must not matter
			ev, err := v.VerifyAt(t2030, slot, card.f9)
			if err != nil || !ev.Generated || ev.Anchor != p.piv.Subject.CommonName || !ev.AttestsKey(slot.PublicKey) {
				t.Fatalf("%s: %+v, %v", name, ev, err)
			}
			if first.Anchor == "" {
				first = ev
			}
			if d := evidenceDiff(ev, first); d != "" {
				t.Fatalf("%s: %s", name, d)
			}
		}
	}
}

// A mixed pair: the slot certificate is signed by the PIV intermediate itself and the signer given is a certificate of the same name and key
// that the root signed but that the bundle does not hold. The chains are [slot, PIV] and [slot, signer, root]: neither is the shape.
func TestAChainThroughAnUnadmittedTwinIsNotGenerated(t *testing.T) {
	p := newOwnPKI(t)
	v := p.verifier(t)
	twin := ownCert(t, ownCA(p.piv.Subject.CommonName), p.root, &p.pivKey.PublicKey, p.rootKey)
	slot := ownCert(t, &x509.Certificate{SerialNumber: ownSerial(), Subject: pkix.Name{CommonName: "YubiKey PIV Attestation 9a"}, NotBefore: t2010, NotAfter: t2099},
		p.piv, &p.pivKey.PublicKey, p.pivKey)
	ev, err := v.VerifyAt(t2030, slot, twin)
	if err != nil || ev.Generated || ev.AttestsKey(slot.PublicKey) {
		t.Fatalf("%+v, %v", ev, err)
	}
	// Anchor is the one of the two anchors found with the smaller SHA-256 (the PIV intermediate, or the root), whatever order the standard
	// library lists the chains in.
	want := p.piv.Subject.CommonName
	if attesttest.SHA256(p.root) < attesttest.SHA256(p.piv) {
		want = p.root.Subject.CommonName
	}
	if ev.Anchor != want {
		t.Errorf("the anchor is %q, the one with the smaller digest is %q", ev.Anchor, want)
	}
}

// The third certificate is an admitted intermediate, not a certificate with the name of one: a root that is named like a PIV intermediate
// is an anchor and is not admitted, so a chain that ends in it is not Generated.
func TestAnAnchorThatIsNamedLikeAPIVIntermediateIsNotAdmitted(t *testing.T) {
	rootKey, err := newKey()
	if err != nil {
		t.Fatal(err)
	}
	rt := ownCA("Yubico PIV Attestation TEST ONLY Root")
	root := ownCert(t, rt, rt, &rootKey.PublicKey, rootKey)
	pivKey, err := newKey()
	if err != nil {
		t.Fatal(err)
	}
	piv := ownCert(t, ownCA("Yubico PIV Attestation TEST ONLY Admitted"), root, &pivKey.PublicKey, rootKey)
	v, err := pivattest.NewVerifierFromPEM(attesttest.PEM(root), attesttest.PEM(piv), attesttest.SHA256(root))
	if err != nil {
		t.Fatal(err)
	}
	// a card whose attestation certificate is signed by the root (named like a PIV intermediate), and one signed by the admitted intermediate
	byRoot := &ownPKI{root: root, piv: root, rootKey: rootKey, pivKey: rootKey}
	cardRoot := byRoot.card(t)
	slotRoot := cardRoot.slot(t, "YubiKey PIV Attestation 9a")
	ev, err := v.VerifyAt(t2030, slotRoot, cardRoot.f9)
	if err != nil || ev.Generated || ev.Anchor != root.Subject.CommonName {
		t.Errorf("a chain that ends in a root named like a PIV intermediate: %+v, %v", ev, err)
	}
	byPIV := &ownPKI{root: root, piv: piv, rootKey: rootKey, pivKey: pivKey}
	cardPIV := byPIV.card(t)
	slotPIV := cardPIV.slot(t, "YubiKey PIV Attestation 9a")
	if ev, err := v.VerifyAt(t2030, slotPIV, cardPIV.f9); err != nil || !ev.Generated || ev.Anchor != piv.Subject.CommonName {
		t.Errorf("the admitted intermediate: %+v, %v", ev, err)
	}
}

// A verifier from NewVerifierFromPEM and the one of Default give the same sort of answer: the test PKI is Generated through the first and
// refused by the second.
func TestGeneratedThroughNewVerifierFromPEM(t *testing.T) {
	p := fixedPKI(t, attesttest.PKIOptions{})
	v := verifierOf(t, p)
	slot := fixedSlot(t, p, attesttest.SlotOptions{})
	ev, err := v.VerifyDER(slot.Raw, p.F9.Raw)
	if err != nil || !ev.Generated || !ev.AttestsKey(slot.PublicKey) || ev.Anchor != testAnchor {
		t.Fatalf("%+v, %v", ev, err)
	}
	def, err := pivattest.Default()
	if err != nil {
		t.Fatal(err)
	}
	if ev, err := def.VerifyAt(t2030, slot, p.F9); !errors.Is(err, pivattest.ErrChain) || ev.Generated {
		t.Errorf("Default: %+v, %v", ev, err)
	}
}
