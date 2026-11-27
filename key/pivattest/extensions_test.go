package pivattest

import (
	"crypto/x509/pkix"
	"errors"
	"sync"
	"testing"

	"github.com/truvity/atoom/key/pivattest/attesttest"
)

func TestSlotFromName(t *testing.T) {
	for name, want := range map[string]struct {
		slot byte
		ok   bool
	}{
		"YubiKey PIV Attestation 9a": {0x9a, true}, "YubiKey PIV Attestation 9A": {0x9a, true}, "YubiKey PIV Attestation 009c": {0x9c, true},
		"YubiKey PIV Attestation 9d": {0x9d, true}, "YubiKey PIV Attestation 9e": {0x9e, true},
		"YubiKey PIV Attestation 82": {0x82, true}, "YubiKey PIV Attestation 95": {0x95, true},
		"YubiKey PIV Attestation 81": {}, "YubiKey PIV Attestation 96": {}, "YubiKey PIV Attestation f9": {}, "YubiKey PIV Attestation 80": {},
		"YubiKey PIV Attestation 10000009a": {}, "YubiKey PIV Attestation 0x9a": {}, "YubiKey PIV Attestation +9a": {}, "YubiKey PIV Attestation ": {},
		"YubiKey PIV Attestation 9a ": {}, "yubikey piv attestation 9a": {}, "9a": {}, "": {},
	} {
		if s, ok := slotFromName(name); s != want.slot || ok != want.ok {
			t.Errorf("%q: %#x %v, want %#x %v", name, s, ok, want.slot, want.ok)
		}
	}
}

func TestTheExtensionsAreReadWithTheirQuirks(t *testing.T) {
	read := func(oid []int, v []byte) (Evidence, error) {
		var e Evidence
		err := e.addExtension(oid, v)
		return e, err
	}
	// a serial longer than 32 bits is cut to the low 32 bits; bytes after the integer are ignored
	e, err := read(oidSerial, []byte{0x02, 0x05, 0x01, 0x00, 0x00, 0x00, 0x05, 0xaa, 0xbb})
	if err != nil || e.Serial != 5 || !e.HasSerial {
		t.Errorf("serial 2^32+5 with trailing bytes: %+v, %v", e, err)
	}
	if _, err := read(oidSerial, []byte{0x02, 0x01, 0xff}); !errors.Is(err, ErrSerialExt) {
		t.Errorf("a negative serial: %v", err)
	}
	if _, err := read(oidSerial, []byte{0x04, 0x01, 0x05}); !errors.Is(err, ErrSerialExt) {
		t.Errorf("a serial of the wrong tag: %v", err)
	}
	for _, n := range []int{0, 1, 2, 4} {
		if _, err := read(oidFirmware, make([]byte, n)); !errors.Is(err, ErrFirmwareExt) {
			t.Errorf("a firmware of %d bytes: %v", n, err)
		}
	}
	for _, n := range []int{0, 2} {
		if _, err := read(oidFormFactor, make([]byte, n)); !errors.Is(err, ErrFormFactorExt) {
			t.Errorf("a form factor of %d bytes: %v", n, err)
		}
	}
	for _, v := range [][]byte{{0, 1}, {6, 1}, {1, 0}, {1, 4}, {1}, {1, 1, 1}} {
		if _, err := read(oidPolicy, v); !errors.Is(err, ErrPolicyExt) {
			t.Errorf("the policy %v: %v", v, err)
		}
	}
	if e, err := read(oidPolicy, []byte{5, 3}); err != nil || e.PIN != PINMatchAlways || e.Touch != TouchCached || !e.HasPolicy {
		t.Errorf("the policy 5 3: %+v, %v", e, err)
	}
	// an extension of another identifier is not read
	if e, err := read([]int{1, 3, 6, 1, 4, 1, 41482, 3, 4}, []byte{1}); err != nil || e != (Evidence{}) {
		t.Errorf("an unknown extension: %+v, %v", e, err)
	}
	if _, err := read(oidFirmware, []byte{1, 2}); err == nil || err.Error() != "pivattest: the firmware version extension holds 2 bytes, want 3" {
		t.Errorf("the text of a firmware error: %v", err)
	}
}

func TestAnAbsentExtensionLeavesAZeroValueAndNoFlag(t *testing.T) {
	p, err := attesttest.NewPKI(attesttest.PKIOptions{})
	if err != nil {
		t.Fatal(err)
	}
	v, err := NewVerifierFromPEM(p.RootPEM(), p.BundlePEM(), p.RootSHA256())
	if err != nil {
		t.Fatal(err)
	}
	full, err := p.Slot(attesttest.SlotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ev, err := v.Verify(full, p.F9)
	if err != nil {
		t.Fatal(err)
	}
	want := Evidence{Slot: 0x9a, HasSlot: true, Firmware: Version{5, 8, 0}, HasFirmware: true, Serial: 1234567, HasSerial: true, FormFactor: 4, HasFormFactor: true,
		PIN: PINOnce, Touch: TouchNever, HasPolicy: true, Algorithm: AlgEC256, Anchor: "Yubico PIV Attestation TEST ONLY 1", Generated: true}
	ev.PublicKey = nil
	if ev != want {
		t.Errorf("evidence %+v, want %+v", ev, want)
	}
	bare, err := p.Slot(attesttest.SlotOptions{Extensions: []pkix.Extension{}, CommonName: "TEST ONLY no name"})
	if err != nil {
		t.Fatal(err)
	}
	ev, err = v.Verify(bare, p.F9)
	if err != nil {
		t.Fatal(err)
	}
	if ev.HasSlot || ev.HasFirmware || ev.HasSerial || ev.HasPolicy || ev.HasFormFactor || ev.Slot != 0 || ev.Firmware != (Version{}) || ev.Serial != 0 || ev.PIN != 0 || ev.Touch != 0 {
		t.Errorf("a certificate with nothing states something: %+v", ev)
	}
}

func FuzzExtensions(f *testing.F) {
	f.Add([]byte{5, 8, 0}, []byte{0x02, 0x03, 0x12, 0xd6, 0x87}, []byte{2, 1}, []byte{4})
	f.Add([]byte{}, []byte{0x02, 0x01, 0xff}, []byte{9, 9}, []byte{1, 2})
	f.Add([]byte{1, 2, 3, 4}, []byte{0x30, 0x80}, []byte{0, 0, 0}, []byte{})
	f.Fuzz(func(t *testing.T, fw, ser, pol, ff []byte) {
		for _, c := range []struct {
			oid  []int
			v    []byte
			want error
		}{{oidFirmware, fw, ErrFirmwareExt}, {oidSerial, ser, ErrSerialExt}, {oidPolicy, pol, ErrPolicyExt}, {oidFormFactor, ff, ErrFormFactorExt}} {
			var e Evidence
			err := e.addExtension(c.oid, c.v)
			if err != nil {
				if !errors.Is(err, c.want) || e.HasFirmware || e.HasSerial || e.HasPolicy || e.HasFormFactor {
					t.Fatalf("the error %v of %x is not of its class, or left a flag", err, c.v)
				}
				continue
			}
			if e.HasPolicy && (e.PIN < PINNever || e.PIN > PINMatchAlways || e.Touch < TouchNever || e.Touch > TouchCached) {
				t.Fatalf("a policy out of range: %+v", e)
			}
			if e.HasFirmware && len(c.v) != 3 || e.HasFormFactor && len(c.v) != 1 {
				t.Fatalf("a flag with a value of %d bytes", len(c.v))
			}
		}
	})
}

var (
	fuzzOnce sync.Once
	fuzzV    *Verifier
	fuzzPKI  *attesttest.PKI
)

// FuzzVerifyDER feeds bytes as both certificates: the call returns, and an error comes with a zero evidence.
func FuzzVerifyDER(f *testing.F) {
	fuzzOnce.Do(func() {
		var err error
		if fuzzPKI, err = attesttest.NewPKI(attesttest.PKIOptions{}); err != nil {
			panic(err)
		}
		if fuzzV, err = NewVerifierFromPEM(fuzzPKI.RootPEM(), fuzzPKI.BundlePEM(), fuzzPKI.RootSHA256()); err != nil {
			panic(err)
		}
	})
	slot, err := fuzzPKI.SlotDER(attesttest.SlotOptions{})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(slot, fuzzPKI.F9.Raw)
	f.Add(fuzzPKI.F9.Raw, slot)
	f.Add([]byte{}, []byte{0x30, 0x00})
	f.Fuzz(func(t *testing.T, a, b []byte) {
		ev, err := fuzzV.VerifyDER(a, b)
		if err != nil && ev.PublicKey != nil || err == nil && ev.Anchor == "" {
			t.Fatalf("evidence %+v with error %v", ev, err)
		}
	})
}
