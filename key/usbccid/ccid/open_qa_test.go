package ccid

import (
	"encoding/binary"
	"errors"
	"fmt"
	"testing"

	"github.com/truvity/atoom/key/usbccid"
)

// F. The class descriptor and Open: a table of descriptor sets, each valid with exactly one thing wrong and refused by name; the shapes
// that are accepted; and for each, Open sends nothing and reads nothing on the port, and closes the port on every refusal.

// qaCand is the candidate that matches the default made-up device (ids 7777:0001, interface 2, endpoints 0x02 and 0x82).
var qaCand = usbccid.Candidate{VendorID: 0x7777, ProductID: 1, Interface: 2, BulkOut: 0x02, BulkIn: 0x82}

// patched builds the descriptors of s and lets edit change them in place.
func patched(s descSpec, edit func(b []byte)) []byte {
	b := s.build()
	edit(b)
	return b
}

// classAt returns the offset of the class descriptor of the (only or first) CCID interface of descriptors that descSpec built.
func classAt(b []byte) int {
	for i := 18; i+54 <= len(b); i++ {
		if b[i] == 54 && b[i+1] == 0x21 && i >= 9 && b[i-9] == 9 && b[i-8] == 4 {
			return i
		}
	}
	panic("no class descriptor")
}

// epAt returns the offset of the endpoint descriptor with the address addr.
func epAt(b []byte, addr byte) int {
	for i := 18; i+7 <= len(b); i++ {
		if b[i] == 7 && b[i+1] == 5 && b[i+2] == addr {
			return i
		}
	}
	panic(fmt.Sprintf("no endpoint %#x", addr))
}

// appendDesc adds a descriptor at the end of the configuration and fixes its total length.
func appendDesc(b []byte, d ...byte) []byte {
	b = append(b, d...)
	binary.LittleEndian.PutUint16(b[18+2:], uint16(len(b)-18)) //nolint:gosec // a few hundred bytes
	return b
}

func setClass32(b []byte, off int, v uint32) { binary.LittleEndian.PutUint32(b[classAt(b)+off:], v) }

func TestQADescriptorRefusals(t *testing.T) {
	def := descSpec{}
	for _, c := range []struct {
		name string
		b    []byte
		cand *usbccid.Candidate
		want error
	}{
		// The interface.
		{"no CCID interface", descSpecNoCCID(), nil, ErrNoInterface},
		{"CCID interface without a bulk IN endpoint", descSpec{noBulkIn: true}.build(), nil, ErrNoInterface},
		{"several CCID interfaces and no candidate", descSpec{second: true}.build(), nil, ErrSeveralInterfaces},
		{"a candidate whose interface is not there", def.build(), &usbccid.Candidate{VendorID: 0x7777, ProductID: 1, Interface: 9, BulkOut: 2, BulkIn: 0x82}, usbccid.ErrWrongDevice},
		{"a candidate that names a HID interface", def.build(), &usbccid.Candidate{VendorID: 0x7777, ProductID: 1, Interface: 0, BulkOut: 2, BulkIn: 0x82}, usbccid.ErrWrongDevice},
		{"a candidate with another bulk OUT", def.build(), &usbccid.Candidate{VendorID: 0x7777, ProductID: 1, Interface: 2, BulkOut: 4, BulkIn: 0x82}, usbccid.ErrWrongDevice},
		{"a candidate with another bulk IN", def.build(), &usbccid.Candidate{VendorID: 0x7777, ProductID: 1, Interface: 2, BulkOut: 2, BulkIn: 0x84}, usbccid.ErrWrongDevice},
		{"a candidate with another vendor", def.build(), &usbccid.Candidate{VendorID: 0x7778, ProductID: 1, Interface: 2, BulkOut: 2, BulkIn: 0x82}, usbccid.ErrWrongDevice},
		{"a candidate with another product", def.build(), &usbccid.Candidate{VendorID: 0x7777, ProductID: 2, Interface: 2, BulkOut: 2, BulkIn: 0x82}, usbccid.ErrWrongDevice},
		{"two bulk IN endpoints", descSpec{twoBulkIn: true}.build(), nil, usbccid.ErrBadDescriptors},
		{"two bulk OUT endpoints", patched(descSpec{intrOut: true}, func(b []byte) { b[epAt(b, 0x04)+3] = 2 }), nil, usbccid.ErrBadDescriptors},
		{"two interrupt IN endpoints", patched(descSpec{intrOut: true}, func(b []byte) { b[epAt(b, 0x04)+2] = 0x8A }), nil, usbccid.ErrBadDescriptors},
		{"bNumEndpoints too large", patched(def, func(b []byte) { b[classAt(b)-9+4]++ }), nil, usbccid.ErrBadDescriptors},
		{"bNumEndpoints too small", patched(def, func(b []byte) { b[classAt(b)-9+4]-- }), nil, usbccid.ErrBadDescriptors},
		{"subclass 1", descSpec{subclass: 1}.build(), nil, ErrBadClass},
		{"protocol 1", descSpec{protocol: 1}.build(), nil, ErrBadClass},
		{"an alternate setting 1 of the CCID interface", descSpec{altFirst: true}.build(), nil, usbccid.ErrBadDescriptors},
		{"an alternate setting 1 of a HID interface", patched(def, func(b []byte) { b[epAt(b, 0x81)-18+3] = 1 }), nil, usbccid.ErrBadDescriptors},
		{"two configurations", descSpec{extraCfg: true}.build(), nil, usbccid.ErrSeveralConfigurations},
		// Packet sizes.
		{"bulk IN packet size 7", descSpec{mpsIn: 7}.build(), nil, usbccid.ErrBadDescriptors},
		{"bulk IN packet size 1025", descSpec{mpsIn: 1025}.build(), nil, usbccid.ErrBadDescriptors},
		{"bulk OUT packet size 7", descSpec{mpsOut: 7}.build(), nil, usbccid.ErrBadDescriptors},
		{"bulk OUT packet size 1025", descSpec{mpsOut: 1025}.build(), nil, usbccid.ErrBadDescriptors},
		{"bulk OUT packet size 0", patched(def, func(b []byte) { binary.LittleEndian.PutUint16(b[epAt(b, 0x02)+4:], 0) }), nil, usbccid.ErrBadDescriptors},
		// The class descriptor: present, and of its one form.
		{"no class descriptor", descSpec{noClass: true}.build(), nil, ErrBadClass},
		{"class descriptor of 53 bytes", descSpec{classLen: 53}.build(), nil, ErrBadClass},
		{"class descriptor of 4 bytes", descSpec{classLen: 4}.build(), nil, ErrBadClass},
		{"class descriptor of 55 bytes", descSpec{classLen: 55}.build(), nil, ErrBadClass},
		{"class descriptor of 100 bytes", descSpec{classLen: 100}.build(), nil, ErrBadClass},
		{"two class descriptors", descSpec{twoClass: true}.build(), nil, ErrBadClass},
		// The version.
		{"version 2.0", descSpec{bcdCCID: 0x0200}.build(), nil, ErrBadClass},
		{"version 0.9", descSpec{bcdCCID: 0x0090}.build(), nil, ErrBadClass},
		{"version 0.0", patched(def, func(b []byte) { binary.LittleEndian.PutUint16(b[classAt(b)+2:], 0) }), nil, ErrBadClass},
		{"version 255.255", descSpec{bcdCCID: 0xFFFF}.build(), nil, ErrBadClass},
		// Slots.
		{"two slots", descSpec{maxSlot: 1}.build(), nil, ErrSeveralSlots},
		{"256 slots", descSpec{maxSlot: 255}.build(), nil, ErrSeveralSlots},
		// The exchange level: dwFeatures with the automatic-parameters bit and this level only.
		{"character level", descSpec{features: 0x2}.build(), nil, ErrExchangeLevel},
		{"TPDU level", descSpec{features: 0x00010002}.build(), nil, ErrExchangeLevel},
		{"TPDU level, with the other bits of a real reader", descSpec{features: 0x000104B6}.build(), nil, ErrExchangeLevel},
		{"short and extended level together", descSpec{features: 0x00060002}.build(), nil, ErrBadClass},
		{"TPDU and short level together", descSpec{features: 0x00030002}.build(), nil, ErrBadClass},
		{"TPDU and extended level together", descSpec{features: 0x00050002}.build(), nil, ErrBadClass},
		{"all three levels", descSpec{features: 0x00070002}.build(), nil, ErrBadClass},
		// Parameters, voltage, protocols.
		{"no automatic parameters at short level", descSpec{features: 0x00020000}.build(), nil, ErrParameters},
		{"no automatic parameters at extended level", descSpec{features: 0x000404B4}.build(), nil, ErrParameters},
		{"no voltage", patched(def, func(b []byte) { b[classAt(b)+5] = 0 }), nil, ErrVoltage},
		{"no protocol", patched(def, func(b []byte) { setClass32(b, 6, 0) }), nil, ErrBadClass},
		{"a protocol that is neither T=0 nor T=1", patched(def, func(b []byte) { setClass32(b, 6, 4) }), nil, ErrBadClass},
		{"protocols of the high bits only", patched(def, func(b []byte) { setClass32(b, 6, 0xFFFFFFFC) }), nil, ErrBadClass},
		// The message length.
		{"message length 0", patched(def, func(b []byte) { setClass32(b, 44, 0) }), nil, ErrBadClass},
		{"message length 10", patched(def, func(b []byte) { setClass32(b, 44, 10) }), nil, ErrBadClass},
		{"message length 270", descSpec{maxMsg: 270}.build(), nil, ErrBadClass},
		// The descriptors as a whole.
		{"nil", nil, nil, usbccid.ErrBadDescriptors},
		{"17 bytes", def.build()[:17], nil, usbccid.ErrBadDescriptors},
		{"configuration length one too large", patched(def, func(b []byte) { b[20]++ }), nil, usbccid.ErrBadDescriptors},
		{"configuration length one too small", patched(def, func(b []byte) { b[20]-- }), nil, usbccid.ErrBadDescriptors},
		{"bNumInterfaces too large", patched(def, func(b []byte) { b[18+4]++ }), nil, usbccid.ErrBadDescriptors},
		{"bNumInterfaces too small", patched(def, func(b []byte) { b[18+4]-- }), nil, usbccid.ErrBadDescriptors},
		{"an interface number twice", patched(def, func(b []byte) { b[epAt(b, 0x84)-18+2] = 0 }), nil, usbccid.ErrBadDescriptors},
		{"an endpoint address twice", patched(def, func(b []byte) { b[epAt(b, 0x83)+2] = 0x82 }), nil, usbccid.ErrBadDescriptors},
		{"an endpoint address with a reserved bit", descSpec{bulkIn: 0xA2}.build(), nil, usbccid.ErrBadDescriptors},
		{"an endpoint address 0", patched(def, func(b []byte) { b[epAt(b, 0x83)+2] = 0x80 }), nil, usbccid.ErrBadDescriptors},
		{"a descriptor of length 0", appendDesc(def.build(), 0, 0x24), nil, usbccid.ErrBadDescriptors},
		{"a descriptor of length 1", appendDesc(def.build(), 1, 0x24), nil, usbccid.ErrBadDescriptors},
		{"a descriptor that runs past the end", appendDesc(def.build(), 9, 0x24, 1), nil, usbccid.ErrBadDescriptors},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(descSpec{}, cardHandler(echoSW))
			f.desc = c.b
			var opts Options
			if c.cand != nil {
				opts.Candidate = c.cand
			}
			r, err := Open(f, opts)
			if r != nil || !errors.Is(err, c.want) {
				t.Fatalf("r = %v, err = %v, want %v", r, err, c.want)
			}
			if got := fmt.Sprint(f.calls); got != "[descriptors close]" {
				t.Errorf("calls %s: a refused Open reads the descriptors, closes the port, and does nothing else", got)
			}
			if len(err.Error()) > 400 {
				t.Errorf("the refusal text is %d bytes long", len(err.Error()))
			}
			// The same bytes through parseDescriptors alone.
			if _, err := parseDescriptors(c.b, c.cand); !errors.Is(err, c.want) {
				t.Errorf("parseDescriptors: %v", err)
			}
		})
	}
}

// The shapes that are accepted, each with what Open reads from them. Open sends and reads nothing: the calls are the descriptors and
// one claim of the interface.
func TestQADescriptorShapesThatAreAccepted(t *testing.T) {
	type want struct {
		iface, out, in, mpsOut, mpsIn int
		ext                           bool
		msg                           int
	}
	def := want{2, 0x02, 0x82, 64, 64, true, 271}
	with := func(w want, edit func(*want)) want { edit(&w); return w }
	for _, c := range []struct {
		name string
		b    []byte
		cand *usbccid.Candidate
		want want
	}{
		{"the default", descSpec{}.build(), nil, def},
		{"the CCID interface first", descSpec{ccidFirst: true}.build(), nil, def},
		{"no interrupt endpoint", descSpec{noIntr: true}.build(), nil, def},
		{"an interrupt OUT endpoint too", descSpec{intrOut: true}.build(), nil, def},
		{"short APDU level", descSpec{features: featuresShort}.build(), nil, with(def, func(w *want) { w.ext = false })},
		{"short APDU level, bare", descSpec{features: 0x00020002}.build(), nil, with(def, func(w *want) { w.ext = false })},
		{"extended level, bare", descSpec{features: 0x00040002}.build(), nil, def},
		{"every other bit of dwFeatures set", descSpec{features: 0xFFFFFFFF &^ 0x00030000}.build(), nil, def},
		{"version 1.0", descSpec{bcdCCID: 0x0100}.build(), nil, def},
		{"version 1.ff", descSpec{bcdCCID: 0x01FF}.build(), nil, def},
		{"only T=0", patched(descSpec{}, func(b []byte) { setClass32(b, 6, 1) }), nil, def},
		{"only T=1", patched(descSpec{}, func(b []byte) { setClass32(b, 6, 2) }), nil, def},
		{"T=0, T=1 and a protocol more", patched(descSpec{}, func(b []byte) { setClass32(b, 6, 0xFFFFFFFF) }), nil, def},
		{"voltage 1", patched(descSpec{}, func(b []byte) { b[classAt(b)+5] = 1 }), nil, def},
		{"voltage 0xFF", patched(descSpec{}, func(b []byte) { b[classAt(b)+5] = 0xFF }), nil, def},
		{"message length 272", descSpec{maxMsg: 272}.build(), nil, with(def, func(w *want) { w.msg = 272 })},
		{"message length 65536", descSpec{maxMsg: 65536}.build(), nil, with(def, func(w *want) { w.msg = 65536 })},
		{"message length 4 GiB less one, held to 64 KiB", descSpec{maxMsg: 0xFFFFFFFF}.build(), nil, with(def, func(w *want) { w.msg = 65536 })},
		{"packet size 8", descSpec{mpsIn: 8, mpsOut: 8}.build(), nil, with(def, func(w *want) { w.mpsIn, w.mpsOut = 8, 8 })},
		{"packet size 512", descSpec{mpsIn: 512, mpsOut: 512}.build(), nil, with(def, func(w *want) { w.mpsIn, w.mpsOut = 512, 512 })},
		{"packet size 1024", descSpec{mpsIn: 1024, mpsOut: 1024}.build(), nil, with(def, func(w *want) { w.mpsIn, w.mpsOut = 1024, 1024 })},
		{"different packet sizes in and out", descSpec{mpsIn: 512, mpsOut: 64}.build(), nil, with(def, func(w *want) { w.mpsIn = 512 })},
		{"endpoints 0x01 and 0x85", descSpec{bulkOut: 0x01, bulkIn: 0x85, intr: 0x83}.build(), nil, with(def, func(w *want) { w.out, w.in = 1, 0x85 })},
		{"endpoints 0x0F and 0x8F", descSpec{bulkOut: 0x0F, bulkIn: 0x8F, intr: 0x8E}.build(), nil, with(def, func(w *want) { w.out, w.in = 0x0F, 0x8F })},
		{"another interface number", descSpec{ifnum: 7}.build(), nil, with(def, func(w *want) { w.iface = 7 })},
		{"a candidate that agrees", descSpec{}.build(), &qaCand, def},
		{"two CCID interfaces, the first named", descSpec{second: true}.build(), &qaCand, def},
		{"two CCID interfaces, the second named", descSpec{second: true}.build(),
			&usbccid.Candidate{VendorID: 0x7777, ProductID: 1, Interface: 3, BulkOut: 0x06, BulkIn: 0x86}, with(def, func(w *want) { w.iface, w.out, w.in = 3, 0x06, 0x86 })},
		{"a descriptor of another type after the endpoints", appendDesc(descSpec{}.build(), 4, 0x24, 1, 2), nil, def},
		{"an interface association descriptor", appendDesc(descSpec{}.build(), 8, 0x0B, 0, 2, 0x0B, 0, 0, 0), nil, def},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(descSpec{}, cardHandler(echoSW))
			f.desc = c.b
			var opts Options
			if c.cand != nil {
				opts.Candidate = c.cand
			}
			r, err := Open(f, opts)
			if err != nil {
				t.Fatal(err)
			}
			if got := fmt.Sprint(f.calls); got != "[descriptors claim]" {
				t.Errorf("Open made the calls %s", got)
			}
			info := r.Info()
			if info.Interface != c.want.iface || int(info.BulkOut) != c.want.out || int(info.BulkIn) != c.want.in ||
				info.MaxPacketOut != c.want.mpsOut || info.MaxPacketIn != c.want.mpsIn || info.ExtendedAPDU != c.want.ext || info.MaxMessageLength != c.want.msg {
				t.Errorf("info = %+v, want %+v", info, c.want)
			}
			if wantMax := map[bool]int{false: MaxShortCommandAPDU, true: MaxCommandAPDU}[c.want.ext]; info.MaxCommandAPDU != wantMax {
				t.Errorf("MaxCommandAPDU = %d, want %d", info.MaxCommandAPDU, wantMax)
			}
		})
	}
}

// A refusal at the claim: the descriptors were read, the claim was asked once, nothing was released (nothing was held), and the port is
// closed.
func TestQAOpenFailsAtTheClaim(t *testing.T) {
	for _, cause := range []error{usbccid.ErrBusy, usbccid.ErrNoSuchInterface, usbccid.ErrGone, usbccid.ErrClosed, errors.New("fake: another error")} {
		f := newFake(descSpec{}, cardHandler(echoSW))
		f.claimErr = cause
		r, err := Open(f, Options{})
		if r != nil || !errors.Is(err, cause) {
			t.Errorf("%v: r=%v err=%v", cause, r, err)
		}
		if got := fmt.Sprint(f.calls); got != "[descriptors claim close]" {
			t.Errorf("%v: calls %s", cause, got)
		}
	}
	f := newFake(descSpec{}, nil)
	f.descErr = usbccid.ErrClosed
	if r, err := Open(f, Options{}); r != nil || !errors.Is(err, usbccid.ErrClosed) || fmt.Sprint(f.calls) != "[descriptors close]" {
		t.Errorf("r=%v err=%v calls=%v", r, err, f.calls)
	}
}

// Open takes ownership: when the port's own Close fails on a refusal, the refusal is still what is returned.
func TestQAOpenRefusalWhenCloseFails(t *testing.T) {
	f := newFake(descSpec{maxSlot: 1}, nil)
	f.closeErr = errors.New("fake: close failed")
	if _, err := Open(f, Options{}); !errors.Is(err, ErrSeveralSlots) {
		t.Errorf("err = %v", err)
	}
}

// Bytes of the class descriptor that this layer does not read do not change what it accepts.
func TestQAUnreadBytesOfTheClassDescriptorDoNotMatter(t *testing.T) {
	good := descSpec{}.build()
	base, err := parseDescriptors(good, nil)
	if err != nil {
		t.Fatal(err)
	}
	read := map[int]bool{2: true, 3: true, 4: true, 5: true}
	for i := 6; i < 10; i++ {
		read[i] = true
	}
	for i := 40; i < 48; i++ {
		read[i] = true
	}
	off := classAt(good)
	for i := 2; i < 54; i++ {
		if read[i] {
			continue
		}
		for _, v := range []byte{0x00, 0x5A, 0xFF} {
			b := append([]byte(nil), good...)
			b[off+i] = v
			l, err := parseDescriptors(b, nil)
			if err != nil || *l != *base {
				t.Errorf("byte %d set to %#x: %v", i, v, err)
			}
		}
	}
}
