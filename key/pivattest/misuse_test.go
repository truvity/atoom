package pivattest_test

import (
	"crypto/ecdsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"testing"

	"github.com/truvity/atoom/key/pivattest"
	"github.com/truvity/atoom/key/pivattest/attesttest"
)

// Misuse: what Evidence says when a caller holds the call wrongly, and what a caller could mistake for the attestation of its key. The
// package compares nothing between the evidence and a card, a plan or a key (its documentation says so); these tests pin what it returns,
// so that a caller knows which checks are its own.

// The arguments swapped: the attestation certificate is read as the slot certificate. It chains to the PIV intermediate on its own, so the
// call is accepted (what is accepted is as before), and the evidence describes the attestation certificate: no slot, no extension, the key
// of the card's attestation key. Generated is false (a chain of two, with no slot in the name), so AttestsKey is false for every key.
func TestSwappedArgumentsAreAcceptedAndDescribeTheAttestationCertificate(t *testing.T) {
	p := fixedPKI(t, attesttest.PKIOptions{})
	v := verifierOf(t, p)
	slot := fixedSlot(t, p, attesttest.SlotOptions{})
	ev, err := v.VerifyAt(t2030, p.F9, slot)
	if err != nil {
		t.Fatalf("the swapped call is refused: %v", err)
	}
	if ev.HasSlot || ev.HasFirmware || ev.HasSerial || ev.HasPolicy || ev.HasFormFactor || ev.Generated || ev.Anchor != testAnchor {
		t.Errorf("evidence of the swapped call: %+v", ev)
	}
	if !sameKey(ev.PublicKey, p.F9.PublicKey) || sameKey(ev.PublicKey, slot.PublicKey) {
		t.Error("the evidence of the swapped call does not hold the key of the attestation certificate")
	}
	if ev.Algorithm != pivattest.AlgEC384 {
		t.Errorf("algorithm %v", ev.Algorithm)
	}
	if ev.AttestsKey(p.F9.PublicKey) || ev.AttestsKey(slot.PublicKey) {
		t.Error("the swapped call attests a key")
	}
}

// The same certificate twice. A slot certificate given as its own signer does not chain (its issuer is not there); the attestation
// certificate given twice does, and so does any certificate that is an anchor (the standard library takes a certificate that is itself in
// the roots as a chain of one): the evidence is that of that certificate. With Default, the vendor's own public certificates are accepted
// this way, whatever the second argument is; since the fix, Generated is false for every one of them (no chain of three, no slot in the name)
// and AttestsKey is false for the vendor's key.
func TestTheSameCertificateTwiceAndAnAnchorAsTheSlotCertificate(t *testing.T) {
	p := fixedPKI(t, attesttest.PKIOptions{})
	v := verifierOf(t, p)
	slot := fixedSlot(t, p, attesttest.SlotOptions{})
	if _, err := v.VerifyAt(t2030, slot, slot); !errors.Is(err, pivattest.ErrChain) {
		t.Errorf("a slot certificate twice: %v", err)
	}
	ev, err := v.VerifyAt(t2030, p.F9, p.F9)
	if err != nil || ev.HasSlot || ev.Generated || ev.AttestsKey(p.F9.PublicKey) || !sameKey(ev.PublicKey, p.F9.PublicKey) {
		t.Errorf("the attestation certificate twice: %+v, %v", ev, err)
	}
	// an anchor given as the slot certificate, with any signer (here the slot certificate, which has nothing to do with it)
	for name, c := range map[string]*x509.Certificate{"the PIV intermediate": p.PIV, "the root": p.Root} {
		for sname, signer := range map[string]*x509.Certificate{"itself": c, "an unrelated certificate": slot} {
			ev, err := v.VerifyAt(t2030, c, signer)
			if err != nil {
				t.Errorf("%s with %s as the signer: %v (the standard library takes a root as a chain of one)", name, sname, err)
				continue
			}
			if ev.Generated || ev.AttestsKey(c.PublicKey) || ev.Anchor != c.Subject.CommonName || !sameKey(ev.PublicKey, c.PublicKey) || ev.HasSlot {
				t.Errorf("%s with %s as the signer: %+v", name, sname, ev)
			}
		}
	}

	// the vendor's certificates through Default
	def, err := pivattest.Default()
	if err != nil {
		t.Fatal(err)
	}
	f := embedded(t)
	isAnchor := map[string]bool{"Yubico Attestation Root 1": true, "Yubico PIV Attestation A 1": true, "Yubico PIV Attestation B 1": true, "Yubico PIV Attestation B2 1": true}
	accepted := 0
	for _, d := range append([][]byte{f.rootDER}, f.bundleDER...) {
		cert, err := x509.ParseCertificate(d)
		if err != nil {
			t.Fatal(err)
		}
		ev, err := def.VerifyAt(t2030, cert, slot)
		cn, issuer := cert.Subject.CommonName, cert.Issuer.CommonName
		// accepted: an anchor, and a certificate that an anchor signed (the two intermediates under the root): a chain of one or of two
		if !isAnchor[cn] && !isAnchor[issuer] {
			if !errors.Is(err, pivattest.ErrChain) {
				t.Errorf("%s (issued by %s): %+v, %v", cn, issuer, ev, err)
			}
			continue
		}
		accepted++
		anchor := cn
		if !isAnchor[cn] {
			anchor = issuer
		}
		if err != nil || ev.Anchor != anchor || ev.Generated || ev.AttestsKey(cert.PublicKey) || !sameKey(ev.PublicKey, cert.PublicKey) || ev.HasSlot {
			t.Errorf("%s: %+v, %v", cn, ev, err)
		}
	}
	if accepted < 4 {
		t.Errorf("%d vendor certificates accepted as the slot certificate, want at least the four anchors", accepted)
	}
}

// Two cards under one PIV intermediate, with the same names. A slot certificate verifies with the attestation certificate of the card that
// signed it and with no other; what the signer states is not read; and the evidence carries nothing that names the card but the serial that
// the slot certificate states about itself.
func TestTwoCardsUnderOneIntermediate(t *testing.T) {
	p := newOwnPKI(t)
	v := p.verifier(t)
	cardA := p.card(t, attesttest.Serial(222222)) // the attestation certificate states a serial of its own: it is not read
	cardB := p.card(t)
	slotA := cardA.slot(t, "YubiKey PIV Attestation 9a", attesttest.Serial(111111), attesttest.Firmware(5, 8, 0))
	slotB := cardB.slot(t, "YubiKey PIV Attestation 9a", attesttest.Serial(333333), attesttest.Firmware(5, 8, 0))
	if cardA.f9.Subject.CommonName != cardB.f9.Subject.CommonName {
		t.Fatal("the two cards have different names")
	}
	evA, err := v.VerifyAt(t2030, slotA, cardA.f9)
	if err != nil {
		t.Fatal(err)
	}
	evB, err := v.VerifyAt(t2030, slotB, cardB.f9)
	if err != nil {
		t.Fatal(err)
	}
	if evA.Serial != 111111 || evB.Serial != 333333 {
		t.Errorf("serials %d and %d: the serial of the signer (222222) must not be read", evA.Serial, evB.Serial)
	}
	for name, c := range map[string]struct{ slot, signer *x509.Certificate }{
		"the slot certificate of A, the signer of B": {slotA, cardB.f9},
		"the slot certificate of B, the signer of A": {slotB, cardA.f9},
	} {
		if ev, err := v.VerifyAt(t2030, c.slot, c.signer); !errors.Is(err, pivattest.ErrChain) || ev != (pivattest.Evidence{}) {
			t.Errorf("%s: %+v, %v", name, ev, err)
		}
	}
	// the evidence of the two cards differs in what the slot certificates state and in the key, and in nothing the signer holds
	if evA.Anchor != evB.Anchor || evA.Algorithm != evB.Algorithm || sameKey(evA.PublicKey, evB.PublicKey) {
		t.Errorf("A: %+v, B: %+v", evA, evB)
	}
}

// The slot is the name the certificate states, and nothing in the certificate contradicts it: the same key with the same extensions under
// the names 9a and 9c is accepted as both, and Evidence.Slot is the name. Which slot a key was made in is the caller's to compare.
func TestTheSlotIsWhatTheNameStatesAndNothingContradictsIt(t *testing.T) {
	p := newOwnPKI(t)
	v := p.verifier(t)
	card := p.card(t)
	ext := []pkix.Extension{attesttest.Firmware(5, 8, 0), attesttest.Serial(5), attesttest.Policy(2, 1), attesttest.FormFactor(4)}
	for name, want := range map[string]byte{"YubiKey PIV Attestation 9a": 0x9a, "YubiKey PIV Attestation 9c": 0x9c, "YubiKey PIV Attestation 82": 0x82, "YubiKey PIV Attestation 9A": 0x9a, "YubiKey PIV Attestation 000009a": 0x9a} {
		ev, err := v.VerifyAt(t2030, card.slot(t, name, ext...), card.f9)
		if err != nil || !ev.HasSlot || ev.Slot != want || ev.PIN != pivattest.PINOnce {
			t.Errorf("%q: %+v, %v", name, ev, err)
		}
	}
	// a name that claims two slots, or a slot with text after it, claims none (and is no error)
	for _, name := range []string{"YubiKey PIV Attestation 9a 9c", "YubiKey PIV Attestation 9a,9c", "YubiKey PIV Attestation 9a-9c", "YubiKey PIV Attestation 9a\x00"} {
		ev, err := v.VerifyAt(t2030, card.slot(t, name, ext...), card.f9)
		if err != nil || ev.HasSlot || ev.Slot != 0 {
			t.Errorf("%q: %+v, %v", name, ev, err)
		}
	}
}

// What a caller could mistake for the attestation of its key. The package does not compare the key of the evidence with any key: a slot
// certificate of another key verifies the same, and the call gives no sign that the key is not the one the caller holds.
func TestTheEvidenceIsNotTiedToTheKeyTheCallerHolds(t *testing.T) {
	p := fixedPKI(t, attesttest.PKIOptions{})
	v := verifierOf(t, p)
	mine := fixedSlot(t, p, attesttest.SlotOptions{})
	theirs := fixedSlot(t, p, attesttest.SlotOptions{PublicKey: slotKeys(t)["ec-p384"]})
	if sameKey(mine.PublicKey, theirs.PublicKey) {
		t.Fatal("two slot certificates of one key")
	}
	evMine, err := v.VerifyAt(t2030, mine, p.F9)
	if err != nil {
		t.Fatal(err)
	}
	evTheirs, err := v.VerifyAt(t2030, theirs, p.F9)
	if err != nil {
		t.Fatal(err)
	}
	if evMine.Slot != evTheirs.Slot || evMine.Serial != evTheirs.Serial || evMine.Generated != evTheirs.Generated {
		t.Error("the evidence of two keys of one card differs in what a caller reads without the key")
	}
	if sameKey(evMine.PublicKey, evTheirs.PublicKey) {
		t.Error("the evidence of two keys holds one key")
	}
}

// A key that was attested cannot be used to mint a certificate that verifies: the signer is the only intermediate, and the issuer of the
// slot certificate (the card's attestation certificate) is not an anchor, so a certificate signed with the attested key does not chain, even
// though the workaround makes a signer with no Basic Constraints a CA.
func TestACertificateSignedWithAnAttestedKeyDoesNotVerify(t *testing.T) {
	p := newOwnPKI(t)
	v := p.verifier(t)
	card := p.card(t)
	slot, slotKey := card.slotKey(t, "YubiKey PIV Attestation 9c")
	if slot.BasicConstraintsValid {
		t.Fatal("the slot certificate states Basic Constraints")
	}
	leafKey, err := newKey()
	if err != nil {
		t.Fatal(err)
	}
	leaf := ownCert(t, &x509.Certificate{SerialNumber: ownSerial(), Subject: pkix.Name{CommonName: "YubiKey PIV Attestation 9a"}, NotBefore: t2010, NotAfter: t2099,
		ExtraExtensions: attesttest.DefaultExtensions()}, slot, &leafKey.PublicKey, slotKey)
	for name, signer := range map[string]*x509.Certificate{"the slot certificate as the signer": slot, "the attestation certificate as the signer": card.f9} {
		if ev, err := v.VerifyAt(t2030, leaf, signer); !errors.Is(err, pivattest.ErrChain) || ev != (pivattest.Evidence{}) {
			t.Errorf("%s: %+v, %v", name, ev, err)
		}
	}
}

// A slot certificate whose key the standard library cannot read (here the key of a kind it has no name for) verifies: Evidence.PublicKey is
// nil and Algorithm is unknown. A caller that uses the key without a nil check would panic on it; the library does not say the key is
// one a card can hold.
func TestAKeyOfAnUnreadableKindGivesANilPublicKey(t *testing.T) {
	p := fixedPKI(t, attesttest.PKIOptions{})
	v := verifierOf(t, p)
	keys := slotKeys(t)
	der, err := p.SlotDER(attesttest.SlotOptions{PublicKey: keys["ed25519"], NotBefore: t2010, NotAfter: t2099})
	if err != nil {
		t.Fatal(err)
	}
	// the object identifier 1.3.101.112 (Ed25519) becomes 1.3.101.111, which the standard library has no name for
	key, ok := p.Signer().(*ecdsa.PrivateKey)
	if !ok {
		t.Fatal("the attestation key is not ECDSA")
	}
	der, err = attesttest.ReplaceInTBS(der, []byte{0x06, 0x03, 0x2b, 0x65, 0x70}, []byte{0x06, 0x03, 0x2b, 0x65, 0x6f}, key)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := v.VerifyDER(der, p.F9.Raw)
	if err != nil {
		t.Fatalf("a slot certificate with a key of an unknown kind is refused: %v", err)
	}
	if ev.PublicKey != nil || ev.Algorithm != pivattest.AlgUnknown || !ev.Generated {
		t.Errorf("evidence %+v", ev)
	}
}
