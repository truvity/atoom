package pivattest_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/truvity/atoom/key/pivattest"
	"github.com/truvity/atoom/key/pivattest/attesttest"
)

// The oracle of this file is the test itself: it builds a chain with the test-support builder from values it chooses, and holds the
// Evidence to the values it put in.

const testAnchor = "Yubico PIV Attestation TEST ONLY 1"

func TestEvidenceHoldsWhatTheCertificateStates(t *testing.T) {
	p := fixedPKI(t, attesttest.PKIOptions{})
	v := verifierOf(t, p)
	full := func(fw [3]byte, serial int64, pin, touch, ff byte) []pkix.Extension {
		return []pkix.Extension{attesttest.Firmware(fw[0], fw[1], fw[2]), attesttest.Serial(serial), attesttest.Policy(pin, touch), attesttest.FormFactor(ff)}
	}
	tests := []struct {
		name string
		cn   string
		ext  []pkix.Extension
		want pivattest.Evidence // the key and the algorithm are checked by the tests below
	}{
		{"every field distinct", "YubiKey PIV Attestation 9c", full([3]byte{5, 7, 4}, 7654321, 3, 2, 0x83),
			pivattest.Evidence{Slot: 0x9c, HasSlot: true, Firmware: pivattest.Version{Major: 5, Minor: 7, Patch: 4}, HasFirmware: true, Serial: 7654321, HasSerial: true,
				FormFactor: 0x83, HasFormFactor: true, PIN: pivattest.PINAlways, Touch: pivattest.TouchAlways, HasPolicy: true}},
		{"the largest values", "YubiKey PIV Attestation 95", full([3]byte{255, 254, 253}, 1<<32-1, 5, 3, 0xff),
			pivattest.Evidence{Slot: 0x95, HasSlot: true, Firmware: pivattest.Version{Major: 255, Minor: 254, Patch: 253}, HasFirmware: true, Serial: 1<<32 - 1, HasSerial: true,
				FormFactor: 0xff, HasFormFactor: true, PIN: pivattest.PINMatchAlways, Touch: pivattest.TouchCached, HasPolicy: true}},
		{"the smallest values", "YubiKey PIV Attestation 82", full([3]byte{0, 0, 0}, 0, 1, 1, 0),
			pivattest.Evidence{Slot: 0x82, HasSlot: true, HasFirmware: true, HasSerial: true, HasFormFactor: true, PIN: pivattest.PINNever, Touch: pivattest.TouchNever, HasPolicy: true}},
		{"a serial over 32 bits is cut to its low 32 bits", "YubiKey PIV Attestation 9d", full([3]byte{5, 8, 0}, 1<<40+0xabcdef, 4, 1, 4),
			pivattest.Evidence{Slot: 0x9d, HasSlot: true, Firmware: pivattest.Version{Major: 5, Minor: 8}, HasFirmware: true, Serial: 0xabcdef, HasSerial: true,
				FormFactor: 4, HasFormFactor: true, PIN: pivattest.PINMatchOnce, Touch: pivattest.TouchNever, HasPolicy: true}},
		{"a name that gives no slot", "TEST ONLY something else", full([3]byte{1, 2, 3}, 9, 2, 2, 1),
			pivattest.Evidence{Firmware: pivattest.Version{Major: 1, Minor: 2, Patch: 3}, HasFirmware: true, Serial: 9, HasSerial: true,
				FormFactor: 1, HasFormFactor: true, PIN: pivattest.PINOnce, Touch: pivattest.TouchAlways, HasPolicy: true}},
		{"no extension at all", "YubiKey PIV Attestation 9e", []pkix.Extension{}, pivattest.Evidence{Slot: 0x9e, HasSlot: true}},
		{"only the firmware", "YubiKey PIV Attestation 9a", []pkix.Extension{attesttest.Firmware(5, 8, 0)},
			pivattest.Evidence{Slot: 0x9a, HasSlot: true, Firmware: pivattest.Version{Major: 5, Minor: 8}, HasFirmware: true}},
		{"only the serial", "YubiKey PIV Attestation 9a", []pkix.Extension{attesttest.Serial(42)}, pivattest.Evidence{Slot: 0x9a, HasSlot: true, Serial: 42, HasSerial: true}},
		{"only a serial of 0 (present, and zero)", "YubiKey PIV Attestation 9a", []pkix.Extension{attesttest.Serial(0)}, pivattest.Evidence{Slot: 0x9a, HasSlot: true, HasSerial: true}},
		{"only the policy", "YubiKey PIV Attestation 9a", []pkix.Extension{attesttest.Policy(3, 3)},
			pivattest.Evidence{Slot: 0x9a, HasSlot: true, PIN: pivattest.PINAlways, Touch: pivattest.TouchCached, HasPolicy: true}},
		{"only the form factor of 0 (present, and zero)", "YubiKey PIV Attestation 9a", []pkix.Extension{attesttest.FormFactor(0)},
			pivattest.Evidence{Slot: 0x9a, HasSlot: true, HasFormFactor: true}},
		{"only the form factor", "YubiKey PIV Attestation 9a", []pkix.Extension{attesttest.FormFactor(2)},
			pivattest.Evidence{Slot: 0x9a, HasSlot: true, FormFactor: 2, HasFormFactor: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			slot := fixedSlot(t, p, attesttest.SlotOptions{CommonName: tt.cn, Extensions: tt.ext})
			ev, err := v.VerifyAt(t2030, slot, p.F9)
			if err != nil {
				t.Fatal(err)
			}
			want := tt.want
			want.Algorithm, want.PublicKey, want.Anchor, want.Generated = pivattest.AlgEC256, slot.PublicKey, testAnchor, tt.want.HasSlot // a chain of three with a slot in the name
			if d := evidenceDiff(ev, want); d != "" {
				t.Fatal(d)
			}
		})
	}
}

// Each extension, taken away alone: its flag is false, its value zero, and the other three are as they were.
func TestEachAbsentExtensionClearsItsFlagAndNoOther(t *testing.T) {
	p := fixedPKI(t, attesttest.PKIOptions{})
	v := verifierOf(t, p)
	base := pivattest.Evidence{Slot: 0x9a, HasSlot: true, Firmware: pivattest.Version{Major: 5, Minor: 8}, HasFirmware: true, Serial: 1234567, HasSerial: true,
		FormFactor: 4, HasFormFactor: true, PIN: pivattest.PINOnce, Touch: pivattest.TouchNever, HasPolicy: true, Algorithm: pivattest.AlgEC256, Anchor: testAnchor, Generated: true}
	drop := map[string]func(e *pivattest.Evidence){
		attesttest.OIDFirmware.String():   func(e *pivattest.Evidence) { e.Firmware, e.HasFirmware = pivattest.Version{}, false },
		attesttest.OIDSerial.String():     func(e *pivattest.Evidence) { e.Serial, e.HasSerial = 0, false },
		attesttest.OIDPolicy.String():     func(e *pivattest.Evidence) { e.PIN, e.Touch, e.HasPolicy = 0, 0, false },
		attesttest.OIDFormFactor.String(): func(e *pivattest.Evidence) { e.FormFactor, e.HasFormFactor = 0, false },
	}
	for oid, f := range drop {
		var ext []pkix.Extension
		for _, e := range attesttest.DefaultExtensions() {
			if e.Id.String() != oid {
				ext = append(ext, e)
			}
		}
		slot := fixedSlot(t, p, attesttest.SlotOptions{Extensions: ext})
		ev, err := v.VerifyAt(t2030, slot, p.F9)
		if err != nil {
			t.Fatal(err)
		}
		want := base
		f(&want)
		want.PublicKey = slot.PublicKey
		if d := evidenceDiff(ev, want); d != "" {
			t.Errorf("without %s: %s", oid, d)
		}
	}
}

func TestEverySlotNameGivesItsSlot(t *testing.T) {
	p := fixedPKI(t, attesttest.PKIOptions{})
	v := verifierOf(t, p)
	slots := []int{0x9a, 0x9c, 0x9d, 0x9e}
	for s := 0x82; s <= 0x95; s++ {
		slots = append(slots, s)
	}
	for _, s := range slots {
		for _, form := range []string{"%x", "%X", "%04x", "%08X"} {
			cn := "YubiKey PIV Attestation " + fmt.Sprintf(form, s)
			ev, err := v.VerifyAt(t2030, fixedSlot(t, p, attesttest.SlotOptions{CommonName: cn}), p.F9)
			if err != nil || !ev.HasSlot || int(ev.Slot) != s {
				t.Errorf("%q: slot %#x has %v, error %v", cn, s, ev.HasSlot, err)
			}
		}
	}
	for _, s := range []uint64{0x00, 0x01, 0x80, 0x81, 0x96, 0x97, 0x99, 0x9b, 0x9f, 0xa0, 0xf9, 0xff, 0x182, 0x1_0000_009a} {
		cn := "YubiKey PIV Attestation " + fmt.Sprintf("%x", s)
		ev, err := v.VerifyAt(t2030, fixedSlot(t, p, attesttest.SlotOptions{CommonName: cn}), p.F9)
		if err != nil || ev.HasSlot || ev.Slot != 0 {
			t.Errorf("%q names no slot, got %#x %v, error %v", cn, ev.Slot, ev.HasSlot, err)
		}
	}
}

// The key of the evidence is the key of the SLOT certificate (not the card's attestation key), of every kind, X25519 included, and the
// algorithm says what it is.
func TestEvidenceHoldsTheKeyOfTheSlotCertificateForEveryKind(t *testing.T) {
	p := fixedPKI(t, attesttest.PKIOptions{})
	v := verifierOf(t, p)
	keys := slotKeys(t)
	want := map[string]pivattest.Algorithm{
		"ec-p256": pivattest.AlgEC256, "ec-p384": pivattest.AlgEC384, "rsa-2048": pivattest.AlgRSA2048, "rsa-3072": pivattest.AlgRSA3072, "rsa-4096": pivattest.AlgRSA4096,
		"ed25519": pivattest.AlgEd25519, "x25519": pivattest.AlgX25519,
		"ec-p224": pivattest.AlgUnknown, "ec-p521": pivattest.AlgUnknown, "rsa-2048-e3": pivattest.AlgUnknown,
	}
	if len(keys) != len(want) {
		t.Fatalf("the corpus gives %d kinds of key, the test expects %d", len(keys), len(want))
	}
	for name, alg := range want {
		t.Run(name, func(t *testing.T) {
			pub, ok := keys[name]
			if !ok {
				t.Fatal("no key of this kind")
			}
			der, err := p.SlotDER(attesttest.SlotOptions{PublicKey: pub, NotBefore: t2010, NotAfter: t2099})
			if err != nil {
				t.Fatal(err)
			}
			ev, err := v.VerifyDER(der, p.F9.Raw)
			if err != nil {
				t.Fatal(err)
			}
			if ev.Algorithm != alg {
				t.Errorf("algorithm %v, want %v", ev.Algorithm, alg)
			}
			if ev.PublicKey == nil || !sameKey(ev.PublicKey, pub) {
				t.Errorf("the key of the evidence (%T) is not the key put in (%T)", ev.PublicKey, pub)
			}
			if sameKey(ev.PublicKey, p.F9.PublicKey) {
				t.Error("the evidence holds the key of the attestation certificate")
			}
			if got, want := ev.Algorithm.String(), alg.String(); got != want || got == "" {
				t.Errorf("the name of the algorithm is %q", got)
			}
		})
	}
}

func TestAlgorithmNames(t *testing.T) {
	for a, want := range map[pivattest.Algorithm]string{
		pivattest.AlgUnknown: "unknown", pivattest.AlgEC256: "EC256", pivattest.AlgEC384: "EC384", pivattest.AlgRSA2048: "RSA2048", pivattest.AlgRSA3072: "RSA3072",
		pivattest.AlgRSA4096: "RSA4096", pivattest.AlgEd25519: "Ed25519", pivattest.AlgX25519: "X25519", pivattest.Algorithm(99): "unknown", pivattest.Algorithm(-1): "unknown",
	} {
		if a.String() != want {
			t.Errorf("%d is %q, want %q", int(a), a.String(), want)
		}
	}
}

func TestPolicyConstantsAreTheValuesOfTheExtension(t *testing.T) {
	// the constants are the bytes of the vendor's extension: 1 to 5 and 1 to 3
	for i, pin := range []pivattest.PINPolicy{pivattest.PINNever, pivattest.PINOnce, pivattest.PINAlways, pivattest.PINMatchOnce, pivattest.PINMatchAlways} {
		if int(pin) != i+1 {
			t.Errorf("PIN policy %d is %d", i+1, pin)
		}
	}
	for i, touch := range []pivattest.TouchPolicy{pivattest.TouchNever, pivattest.TouchAlways, pivattest.TouchCached} {
		if int(touch) != i+1 {
			t.Errorf("touch policy %d is %d", i+1, touch)
		}
	}
}

// The anchor is named by the chain: the PIV intermediate that signed the card's attestation key, or the root when the chain ends there.
func TestAnchorAndGeneratedFollowTheChain(t *testing.T) {
	tests := []struct {
		name          string
		opts          attesttest.PKIOptions
		wantAnchor    string
		wantGenerated bool
	}{
		{"the usual chain", attesttest.PKIOptions{}, "Yubico PIV Attestation TEST ONLY 1", true},
		{"another PIV name", attesttest.PKIOptions{PIVName: "Yubico PIV Attestation B2 TEST ONLY 1"}, "Yubico PIV Attestation B2 TEST ONLY 1", true},
		{"the shortest PIV name", attesttest.PKIOptions{PIVName: "Yubico PIV Attestation X"}, "Yubico PIV Attestation X", true},
		{"the chain ends in the root", attesttest.PKIOptions{F9: attesttest.F9Options{SignedBy: "root"}}, "TEST ONLY Yubico Attestation Root 1", false},
		{"RSA keys", attesttest.PKIOptions{RSA: true}, "Yubico PIV Attestation TEST ONLY 1", true},
		{"RSA keys, ends in the root", attesttest.PKIOptions{RSA: true, F9: attesttest.F9Options{SignedBy: "root"}}, "TEST ONLY Yubico Attestation Root 1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := fixedPKI(t, tt.opts)
			ev, err := verifierOf(t, p).VerifyAt(t2030, fixedSlot(t, p, attesttest.SlotOptions{}), p.F9)
			if err != nil {
				t.Fatal(err)
			}
			if ev.Anchor != tt.wantAnchor || ev.Generated != tt.wantGenerated {
				t.Fatalf("anchor %q generated %v, want %q %v", ev.Anchor, ev.Generated, tt.wantAnchor, tt.wantGenerated)
			}
		})
	}
}

// The documentation of ErrChain says the error wraps the reason of the standard library and that its text begins with the old prefix.
func TestAChainErrorWrapsTheReasonOfTheStandardLibrary(t *testing.T) {
	p := fixedPKI(t, attesttest.PKIOptions{})
	v := verifierOf(t, p)
	rogue := fixedPKI(t, attesttest.PKIOptions{})
	expired := fixedSlot(t, p, attesttest.SlotOptions{NotBefore: t2010, NotAfter: t2015})
	for name, c := range map[string]struct {
		slot, signer *x509.Certificate
		as           func(error) bool
	}{
		"an unknown authority": {fixedSlot(t, rogue, attesttest.SlotOptions{}), rogue.F9, func(err error) bool { var e x509.UnknownAuthorityError; return errors.As(err, &e) }},
		"an expired certificate": {expired, p.F9, func(err error) bool {
			var e x509.CertificateInvalidError
			return errors.As(err, &e) && e.Reason == x509.Expired
		}},
	} {
		_, err := v.VerifyAt(t2030, c.slot, c.signer)
		if !errors.Is(err, pivattest.ErrChain) || !c.as(err) || !strings.HasPrefix(err.Error(), "error verifying attestation certificate: ") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// An RSA key that is not one of the three sizes a card holds, or has another exponent, is of an unknown algorithm; the key is still given.
func TestAnRSAKeyOfAnotherSizeIsUnknown(t *testing.T) {
	p := fixedPKI(t, attesttest.PKIOptions{})
	v := verifierOf(t, p)
	k, err := rsa.GenerateKey(rand.Reader, 1024) //nolint:gosec // a key below the sizes a card holds, on purpose
	if err != nil {
		t.Fatal(err)
	}
	ev, err := v.VerifyAt(t2030, fixedSlot(t, p, attesttest.SlotOptions{PublicKey: &k.PublicKey}), p.F9)
	if err != nil || ev.Algorithm != pivattest.AlgUnknown || !sameKey(ev.PublicKey, &k.PublicKey) {
		t.Errorf("an RSA 1024 key: %+v, %v", ev, err)
	}
}
