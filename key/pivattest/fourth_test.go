package pivattest_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/truvity/atoom/key/pivattest"
	"github.com/truvity/atoom/key/pivattest/attesttest"
)

// The fourth declared difference: both certificates are read again from their Raw bytes. A changed field of either struct has no effect, a
// certificate with no Raw is refused, and the answer describes the bytes.

// damage returns a copy of c with the exported field i changed: a flag turned, a number moved, anything else cleared.
func damage(c *x509.Certificate, i int) *x509.Certificate {
	n := *c
	f := reflect.ValueOf(&n).Elem().Field(i)
	switch f.Kind() {
	case reflect.Bool:
		f.SetBool(!f.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		f.SetInt(f.Int() + 1)
	default:
		f.Set(reflect.Zero(f.Type()))
	}
	return &n
}

func TestEveryFieldOfBothCertificatesIsIgnored(t *testing.T) {
	for _, tt := range []struct {
		name string
		f9   attesttest.F9Options
	}{{"a signer that states Basic Constraints", attesttest.F9Options{}}, {"a signer that states none", attesttest.F9Options{NoBasicConstraints: true}}} {
		t.Run(tt.name, func(t *testing.T) {
			p := fixedPKI(t, attesttest.PKIOptions{F9: tt.f9})
			v := verifierOf(t, p)
			slot := fixedSlot(t, p, attesttest.SlotOptions{})
			want, err := v.VerifyAt(t2030, slot, p.F9)
			if err != nil || !want.Generated {
				t.Fatalf("%+v, %v", want, err)
			}
			typ := reflect.TypeOf(x509.Certificate{})
			changed := 0
			for i := 0; i < typ.NumField(); i++ {
				name := typ.Field(i).Name
				if name == "Raw" || !typ.Field(i).IsExported() {
					continue
				}
				changed += 2
				ev, err := v.VerifyAt(t2030, damage(slot, i), p.F9)
				if err != nil || evidenceDiff(ev, want) != "" {
					t.Errorf("the slot certificate with %s changed: %+v, %v", name, ev, err)
				}
				ev, err = v.VerifyAt(t2030, slot, damage(p.F9, i))
				if err != nil || evidenceDiff(ev, want) != "" {
					t.Errorf("the signer with %s changed: %+v, %v", name, ev, err)
				}
				if !ev.AttestsKey(slot.PublicKey) {
					t.Errorf("%s changed: the key is not attested", name)
				}
			}
			if changed < 60 {
				t.Errorf("only %d fields were changed: the struct is not what the test expects", changed)
			}
			// both changed at once
			a, b := *slot, *p.F9
			for i := 0; i < typ.NumField(); i++ {
				if typ.Field(i).Name != "Raw" && typ.Field(i).IsExported() {
					a, b = *damage(&a, i), *damage(&b, i)
				}
			}
			if ev, err := v.VerifyAt(t2030, &a, &b); err != nil || evidenceDiff(ev, want) != "" {
				t.Errorf("every field of both changed: %+v, %v", ev, err)
			}
		})
	}
}

func TestACertificateWithNoRawIsRefusedWhateverItsFieldsSay(t *testing.T) {
	p := fixedPKI(t, attesttest.PKIOptions{})
	v := verifierOf(t, p)
	slot := fixedSlot(t, p, attesttest.SlotOptions{})
	for name, raw := range map[string][]byte{"nil": nil, "empty": {}} {
		s, f := *slot, *p.F9
		s.Raw, f.Raw = raw, raw
		if _, err := v.VerifyAt(t2030, &s, p.F9); !errors.Is(err, pivattest.ErrNoRaw) {
			t.Errorf("a slot certificate with %s Raw: %v", name, err)
		}
		if _, err := v.VerifyAt(t2030, slot, &f); !errors.Is(err, pivattest.ErrNoRaw) {
			t.Errorf("a signer with %s Raw: %v", name, err)
		}
		if _, err := v.Verify(&s, &f); !errors.Is(err, pivattest.ErrNoRaw) {
			t.Errorf("Verify with %s Raw: %v", name, err)
		}
	}
	// the other Raw fields do not stand for Raw
	s := *slot
	s.Raw = nil
	if _, err := v.VerifyAt(t2030, &s, p.F9); !errors.Is(err, pivattest.ErrNoRaw) || s.RawTBSCertificate == nil {
		t.Errorf("a slot certificate with its other Raw fields: %v", err)
	}
}

// The Raw of one certificate with the fields of another: the answer describes the bytes.
func TestTheRawBytesAreWhatIsAnswered(t *testing.T) {
	p := fixedPKI(t, attesttest.PKIOptions{})
	v := verifierOf(t, p)
	keys := slotKeys(t)
	fields := fixedSlot(t, p, attesttest.SlotOptions{}) // the fields of a 9a certificate with the default extensions
	bytesOf := fixedSlot(t, p, attesttest.SlotOptions{CommonName: "YubiKey PIV Attestation 9c", PublicKey: keys["ec-p384"],
		Extensions: attesttest.DefaultExtensions()[:0:0]})
	mixed := *fields
	mixed.Raw = bytesOf.Raw
	ev, err := v.VerifyAt(t2030, &mixed, p.F9)
	if err != nil || ev.Slot != 0x9c || ev.HasSerial || ev.Algorithm != pivattest.AlgEC384 || !sameKey(ev.PublicKey, keys["ec-p384"]) {
		t.Errorf("the fields of one and the bytes of another: %+v, %v", ev, err)
	}
	// a rogue chain's bytes under the fields of the good chain: refused; the good bytes under the rogue's fields: accepted
	rogue := fixedPKI(t, attesttest.PKIOptions{})
	rs := fixedSlot(t, rogue, attesttest.SlotOptions{})
	m := *fields
	m.Raw = rs.Raw
	if _, err := v.VerifyAt(t2030, &m, p.F9); !errors.Is(err, pivattest.ErrChain) {
		t.Errorf("the bytes of a rogue slot certificate under good fields: %v", err)
	}
	m = *rs
	m.Raw = fields.Raw
	if ev, err := v.VerifyAt(t2030, &m, p.F9); err != nil || !ev.Generated {
		t.Errorf("good bytes under a rogue's fields: %+v, %v", ev, err)
	}
	// the signer: the bytes of a rogue attestation certificate under the good one's fields
	f := *p.F9
	f.Raw = rogue.F9.Raw
	if _, err := v.VerifyAt(t2030, fields, &f); !errors.Is(err, pivattest.ErrChain) {
		t.Errorf("the bytes of a rogue signer under good fields: %v", err)
	}
	// a Raw that does not parse is a parse error, whatever the fields are
	m = *fields
	m.Raw = fields.Raw[:len(fields.Raw)-1]
	if _, err := v.VerifyAt(t2030, &m, p.F9); !errors.Is(err, pivattest.ErrParse) {
		t.Errorf("a cut Raw: %v", err)
	}
}

// ---- VerifyDER copies what it is given ----

// After the call returns, the caller may overwrite the bytes: the evidence (the Ed25519 key of it points into the parsed bytes) does not move.
func TestVerifyDEROwnsWhatItReturns(t *testing.T) {
	p := fixedPKI(t, attesttest.PKIOptions{})
	v := verifierOf(t, p)
	for _, kind := range []string{"ed25519", "x25519", "ec-p256", "rsa-2048"} {
		pub := slotKeys(t)[kind]
		slot, err := p.SlotDER(attesttest.SlotOptions{PublicKey: pub, NotBefore: t2010, NotAfter: t2099})
		if err != nil {
			t.Fatal(err)
		}
		signer := bytes.Clone(p.F9.Raw)
		ev, err := v.VerifyDER(slot, signer)
		if err != nil || !ev.Generated {
			t.Fatalf("%s: %+v, %v", kind, ev, err)
		}
		anchor := string(bytes.Clone([]byte(ev.Anchor)))
		for i := range slot {
			slot[i] = 0xAA
		}
		for i := range signer {
			signer[i] = 0x55
		}
		if !sameKey(ev.PublicKey, pub) || !ev.AttestsKey(pub) || ev.Anchor != anchor {
			t.Errorf("%s: the evidence changed with the caller's bytes: %+v", kind, ev)
		}
		if ed, ok := ev.PublicKey.(ed25519.PublicKey); ok && !bytes.Equal(ed, pub.(ed25519.PublicKey)) {
			t.Errorf("the Ed25519 key of the evidence changed with the caller's bytes")
		}
	}
}

// One small run under the race detector: many goroutines read the same inputs through VerifyDER at once (the call only reads what it is
// given, and shares nothing but the verifier), and the caller changes the bytes once they are all done. (A caller that writes the bytes
// while the call runs is a data race of the caller's own; TestVerifyDERWhileTheCallerWritesTheBytes covers the logic of that where the race
// detector is not on.)
func TestConcurrentVerifyDERShareTheInputsAndTheVerifier(t *testing.T) {
	p := fixedPKI(t, attesttest.PKIOptions{})
	v := verifierOf(t, p)
	slot, err := p.SlotDER(attesttest.SlotOptions{NotBefore: t2010, NotAfter: t2099})
	if err != nil {
		t.Fatal(err)
	}
	signer := p.F9.Raw
	want, err := v.VerifyDER(slot, signer)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan string, 8)
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				ev, err := v.VerifyDER(slot, signer)
				if err != nil || evidenceDiff(ev, want) != "" {
					errs <- "a concurrent call gave another answer"
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	for i := range slot {
		slot[i] = 0
	}
	if !want.AttestsKey(want.PublicKey) {
		t.Error("the evidence changed with the bytes")
	}
}
