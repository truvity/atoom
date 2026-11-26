package ccid

import (
	"encoding/binary"
	"errors"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/truvity/atoom/key/usbccid"
)

func TestParseLayout(t *testing.T) {
	for _, first := range []bool{false, true} {
		l, err := parseDescriptors(descSpec{ccidFirst: first}.build(), nil)
		if err != nil {
			t.Fatalf("ccidFirst=%v: %v", first, err)
		}
		if l.iface != 2 {
			t.Errorf("interface %d", l.iface)
		}
		if l.bulkOut != 0x02 || l.bulkIn != 0x82 || l.interruptIn != 0x83 {
			t.Errorf("endpoints: out %#x in %#x intr %#x", l.bulkOut, l.bulkIn, l.interruptIn)
		}
		if l.maxPacketOut != 64 || l.maxPacketIn != 64 {
			t.Errorf("packet sizes %d %d", l.maxPacketOut, l.maxPacketIn)
		}
		c := l.class
		if c.bcdCCID != 0x0110 || c.features != featuresExt || c.maxMessageLength != 271 || c.maxSlotIndex != 0 || c.voltageSupport != 0x07 || c.protocols != 3 {
			t.Errorf("class: %+v", c)
		}
		if !c.extended() {
			t.Error("not extended")
		}
	}
}

func TestShortAPDULevelIsNotExtended(t *testing.T) {
	l, err := parseDescriptors(descSpec{features: featuresShort}.build(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if l.class.extended() {
		t.Error("short level reported as extended")
	}
}

// The exchange level is the part of dwFeatures under 0x00070000. Only short and extended APDU are accepted, and the refusal of the
// others names the level and holds the values the report needs.
func TestExchangeLevelRule(t *testing.T) {
	cases := []struct {
		features uint32
		ok       bool
		bad      error  // the sentinel of the refusal
		text     string // in the refusal's text
	}{
		{0x00000000 | 0x2, false, ErrExchangeLevel, "character level"},
		{0x000000B6, false, ErrExchangeLevel, "character level"},
		{0x00010000 | 0x2, false, ErrExchangeLevel, "TPDU level"},
		{0x000100B6, false, ErrExchangeLevel, "TPDU level"},
		{0x00020000 | 0x2, true, nil, ""},
		{0x000204B6, true, nil, ""},
		{0x00040000 | 0x2, true, nil, ""},
		{0x000404B6, true, nil, ""},
		{0x00060000 | 0x2, false, ErrBadClass, "more than one exchange level"},
		{0x00070000 | 0x2, false, ErrBadClass, "more than one exchange level"},
		{0x00030000 | 0x2, false, ErrBadClass, "more than one exchange level"},
		// No automatic parameters: refused by name, at either level.
		{0x00020000, false, ErrParameters, "automatic parameter"},
		{0x000404B6 &^ 0x2, false, ErrParameters, "0x00000002"},
	}
	for _, c := range cases {
		_, err := parseDescriptors(descSpec{features: c.features}.build(), nil)
		if c.ok {
			if err != nil {
				t.Errorf("features %#x refused: %v", c.features, err)
			}
			continue
		}
		if !errors.Is(err, c.bad) {
			t.Errorf("features %#x: err = %v, want %v", c.features, err, c.bad)
			continue
		}
		if !strings.Contains(err.Error(), c.text) {
			t.Errorf("features %#x: text %q lacks %q", c.features, err, c.text)
		}
		if errors.Is(c.bad, ErrExchangeLevel) {
			for _, want := range []string{"dwFeatures 0x", "dwMaxCCIDMessageLength 271"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("features %#x: text lacks %q: %v", c.features, want, err)
				}
			}
		}
	}
}

func TestClassRefusals(t *testing.T) {
	cases := []struct {
		name string
		spec descSpec
		bad  error
		text string
	}{
		{"several slots", descSpec{maxSlot: 1}, ErrSeveralSlots, "exactly one slot"},
		{"no class descriptor", descSpec{noClass: true}, ErrBadClass, "no class descriptor"},
		{"class descriptor too short", descSpec{classLen: 52}, ErrBadClass, "52 bytes"},
		{"class descriptor too long", descSpec{classLen: 56}, ErrBadClass, "56 bytes"},
		{"two class descriptors", descSpec{twoClass: true}, ErrBadClass, "2 class descriptors"},
		{"subclass", descSpec{subclass: 1}, ErrBadClass, "subclass 1"},
		{"protocol", descSpec{protocol: 2}, ErrBadClass, "protocol 2"},
		{"version 2", descSpec{bcdCCID: 0x0200}, ErrBadClass, "bcdCCID is 0x0200"},
		{"version 0", descSpec{bcdCCID: 0x0090}, ErrBadClass, "bcdCCID is 0x0090"},
		{"message length below 271", descSpec{maxMsg: 270}, ErrBadClass, "MinMessageLength"},
		{"message length of 20", descSpec{maxMsg: 20}, ErrBadClass, "20"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseDescriptors(c.spec.build(), nil)
			if !errors.Is(err, c.bad) {
				t.Fatalf("err = %v, want %v", err, c.bad)
			}
			if !strings.Contains(err.Error(), c.text) {
				t.Errorf("text %q lacks %q", err, c.text)
			}
		})
	}
}

// bVoltageSupport 0 and dwProtocols without T=0 and T=1 cannot be built by descSpec (zero means the default), so they are patched in.
func TestVoltageAndProtocolRules(t *testing.T) {
	b := descSpec{}.build()
	i := classOffset(t, b)
	v := append([]byte(nil), b...)
	v[i+5] = 0
	if _, err := parseDescriptors(v, nil); !errors.Is(err, ErrVoltage) || !strings.Contains(err.Error(), "bVoltageSupport is 0") {
		t.Errorf("no voltage: %v", err)
	}
	p := append([]byte(nil), b...)
	p[i+6], p[i+7], p[i+8], p[i+9] = 0x04, 0, 0, 0 // a protocol other than T=0 and T=1
	if _, err := parseDescriptors(p, nil); !errors.Is(err, ErrBadClass) || !strings.Contains(err.Error(), "neither T=0 nor T=1") {
		t.Errorf("no protocol: %v", err)
	}
	for _, only := range []byte{1, 2} {
		q := append([]byte(nil), b...)
		q[i+6] = only
		if _, err := parseDescriptors(q, nil); err != nil {
			t.Errorf("protocols %d: %v", only, err)
		}
	}
	// The order of the rules: a TPDU reader with no voltage is refused for its level first.
	w := append([]byte(nil), b...)
	w[i+5] = 0
	w[i+40], w[i+41], w[i+42], w[i+43] = 0xB6, 0x04, 0x01, 0x00 // dwFeatures 0x000104B6: TPDU level
	if _, err := parseDescriptors(w, nil); !errors.Is(err, ErrExchangeLevel) {
		t.Errorf("order of the rules: %v", err)
	}
}

// classOffset finds the class descriptor in descriptors made by descSpec.
func classOffset(t *testing.T, b []byte) int {
	t.Helper()
	for i := 18; i+54 <= len(b); i++ {
		if b[i] == 54 && b[i+1] == 0x21 {
			return i
		}
	}
	t.Fatal("no class descriptor in the test descriptors")
	return 0
}

// An alternate setting other than 0 is not known to this layer and the layer below refuses it too.
func TestAlternateSettingIsRefused(t *testing.T) {
	_, err := parseDescriptors(descSpec{altFirst: true}.build(), nil)
	if !errors.Is(err, usbccid.ErrBadDescriptors) || !strings.Contains(err.Error(), "alternate setting") {
		t.Errorf("err = %v", err)
	}
}

func TestSeveralConfigurationsAreRefused(t *testing.T) {
	_, err := parseDescriptors(descSpec{extraCfg: true}.build(), nil)
	if !errors.Is(err, usbccid.ErrSeveralConfigurations) {
		t.Errorf("err = %v", err)
	}
}

// An interrupt OUT endpoint is not the interrupt IN endpoint.
func TestParseIgnoresInterruptOut(t *testing.T) {
	l, err := parseDescriptors(descSpec{intrOut: true}.build(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if l.interruptIn != 0x83 {
		t.Errorf("interrupt in = %#x, want 0x83", l.interruptIn)
	}
}

func TestParseWithoutInterruptEndpoint(t *testing.T) {
	l, err := parseDescriptors(descSpec{noIntr: true}.build(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if l.interruptIn != 0 {
		t.Errorf("interrupt endpoint %#x, want none", l.interruptIn)
	}
}

func TestParseOtherEndpointAddressesAndSizes(t *testing.T) {
	l, err := parseDescriptors(descSpec{bulkOut: 0x05, bulkIn: 0x86, intr: 0x87, mpsIn: 512, mpsOut: 512}.build(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if l.bulkOut != 0x05 || l.bulkIn != 0x86 || l.interruptIn != 0x87 || l.maxPacketIn != 512 || l.maxPacketOut != 512 {
		t.Errorf("%+v", l)
	}
}

func TestParseRefusals(t *testing.T) {
	good := descSpec{}.build()
	cases := []struct {
		name string
		in   []byte
		bad  error
		text string
	}{
		{"nil", nil, usbccid.ErrBadDescriptors, "device descriptor"},
		{"short", good[:17], usbccid.ErrBadDescriptors, "device descriptor"},
		{"wrong type", append([]byte{18, 2}, good[2:]...), usbccid.ErrBadDescriptors, "device descriptor"},
		{"wrong length", append([]byte{19}, good[1:]...), usbccid.ErrBadDescriptors, "device descriptor"},
		{"no configuration", good[:18], usbccid.ErrBadDescriptors, "configuration descriptor"},
		{"configuration cut", good[:len(good)-1], usbccid.ErrBadDescriptors, "number of bytes present"},
		{"no ccid interface", descSpecNoCCID(), ErrNoInterface, ""},
		{"no bulk in", descSpec{noBulkIn: true}.build(), ErrNoInterface, ""},
		{"two bulk in", descSpec{twoBulkIn: true}.build(), usbccid.ErrBadDescriptors, "2 bulk IN"},
		{"packet size of 0", patchBulkInPacket(descSpec{}.build(), 0), usbccid.ErrBadDescriptors, "packet size of 0"},
		{"packet size of 4", patchBulkInPacket(descSpec{}.build(), 4), usbccid.ErrBadDescriptors, "packet size of 4"},
		{"packet size of 2048", patchBulkInPacket(descSpec{}.build(), 2048), usbccid.ErrBadDescriptors, "packet size of 0"}, // bits 11 and up are not the size
		{"a reserved bit in an endpoint address", descSpec{bulkIn: 0x92}.build(), usbccid.ErrBadDescriptors, "reserved bits"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l, err := parseDescriptors(c.in, nil)
			if err == nil {
				t.Fatalf("accepted: %+v", l)
			}
			if !errors.Is(err, c.bad) {
				t.Errorf("err = %v, want %v", err, c.bad)
			}
			if c.text != "" && !strings.Contains(err.Error(), c.text) {
				t.Errorf("err = %v, want text %q", err, c.text)
			}
		})
	}
}

// descSpecNoCCID is a device with two HID interfaces and a vendor-class interface.
func descSpecNoCCID() []byte {
	b := descSpec{}.build()
	for i := 18; i+9 <= len(b); i++ {
		if b[i] == 9 && b[i+1] == 4 && b[i+5] == 0x0B {
			b[i+5] = 0xFF
		}
	}
	return b
}

// patchBulkInPacket sets wMaxPacketSize of the bulk-in endpoint 0x82.
func patchBulkInPacket(b []byte, size uint16) []byte {
	b = append([]byte(nil), b...)
	for i := 18; i+7 <= len(b); i++ {
		if b[i] == 7 && b[i+1] == 5 && b[i+2] == 0x82 && b[i+3] == 2 {
			binary.LittleEndian.PutUint16(b[i+4:], size)
		}
	}
	return b
}

func TestCandidateChoosesAmongSeveralInterfaces(t *testing.T) {
	b := descSpec{second: true}.build()
	if _, err := parseDescriptors(b, nil); !errors.Is(err, ErrSeveralInterfaces) {
		t.Fatalf("no candidate: %v", err)
	}
	c2 := usbccid.Candidate{VendorID: 0x7777, ProductID: 1, Interface: 2, BulkOut: 0x02, BulkIn: 0x82}
	c3 := usbccid.Candidate{VendorID: 0x7777, ProductID: 1, Interface: 3, BulkOut: 0x06, BulkIn: 0x86}
	for _, c := range []usbccid.Candidate{c2, c3} {
		l, err := parseDescriptors(b, &c)
		if err != nil || l.iface != c.Interface || l.bulkOut != c.BulkOut || l.bulkIn != c.BulkIn {
			t.Errorf("candidate %d: %+v %v", c.Interface, l, err)
		}
	}
}

func TestCandidateMustAgreeWithTheDescriptors(t *testing.T) {
	b := descSpec{}.build()
	for name, c := range map[string]usbccid.Candidate{
		"interface not there":     {VendorID: 0x7777, ProductID: 1, Interface: 5, BulkOut: 0x02, BulkIn: 0x82},
		"interface of a HID":      {VendorID: 0x7777, ProductID: 1, Interface: 0, BulkOut: 0x02, BulkIn: 0x82},
		"other bulk out endpoint": {VendorID: 0x7777, ProductID: 1, Interface: 2, BulkOut: 0x04, BulkIn: 0x82},
		"other bulk in endpoint":  {VendorID: 0x7777, ProductID: 1, Interface: 2, BulkOut: 0x02, BulkIn: 0x84},
		"other vendor id":         {VendorID: 0x7778, ProductID: 1, Interface: 2, BulkOut: 0x02, BulkIn: 0x82},
		"other product id":        {VendorID: 0x7777, ProductID: 2, Interface: 2, BulkOut: 0x02, BulkIn: 0x82},
		"no ids":                  {Interface: 2, BulkOut: 0x02, BulkIn: 0x82},
	} {
		if _, err := parseDescriptors(b, &c); !errors.Is(err, usbccid.ErrWrongDevice) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// A matching candidate does not excuse a bad class descriptor.
	c := usbccid.Candidate{VendorID: 0x7777, ProductID: 1, Interface: 2, BulkOut: 0x02, BulkIn: 0x82}
	if _, err := parseDescriptors(descSpec{maxSlot: 2}.build(), &c); !errors.Is(err, ErrSeveralSlots) {
		t.Errorf("err = %v", err)
	}
}

// Truncated or damaged descriptors must give an error or a layout, and must never panic or loop.
func TestParseNeverPanics(t *testing.T) {
	good := descSpec{}.build()
	for n := 0; n <= len(good); n++ {
		_, _ = parseDescriptors(good[:n], nil)
	}
	rng := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // a fixed seed, for a repeatable test
	for range 5000 {
		b := append([]byte(nil), good...)
		for range 1 + rng.IntN(4) {
			b[rng.IntN(len(b))] = byte(rng.IntN(256)) //nolint:gosec // below 256
		}
		if rng.IntN(3) == 0 {
			b = b[:rng.IntN(len(b)+1)]
		}
		_, _ = parseDescriptors(b, nil)
		_, _ = parseDescriptors(b, &usbccid.Candidate{VendorID: 0x7777, ProductID: 1, Interface: 2, BulkOut: 2, BulkIn: 0x82})
	}
	// Descriptor lengths of 0 and 1 inside a configuration must be refused.
	for _, bad := range []byte{0, 1} {
		b := append([]byte(nil), good...)
		b[18+9] = bad // first descriptor after the configuration header
		if _, err := parseDescriptors(b, nil); err == nil {
			t.Errorf("descriptor length %d accepted", bad)
		}
	}
}

func FuzzParseDescriptors(f *testing.F) {
	f.Add(descSpec{}.build())
	f.Add(descSpec{second: true}.build())
	f.Add(descSpec{features: featuresShort, mpsIn: 512}.build())
	f.Fuzz(func(t *testing.T, b []byte) {
		l, err := parseDescriptors(b, nil)
		if err != nil {
			return
		}
		// What it accepts is within the rules it states.
		if l.maxPacketIn < 8 || l.maxPacketIn > 1024 || l.maxPacketOut < 8 || l.maxPacketOut > 1024 {
			t.Fatalf("packet sizes %d %d", l.maxPacketIn, l.maxPacketOut)
		}
		if l.class.maxSlotIndex != 0 || l.class.maxMessageLength < MinMessageLength || l.class.voltageSupport == 0 || l.class.features&featAutoParameters == 0 {
			t.Fatalf("class %+v", l.class)
		}
		if lv := l.class.features & featLevelMask; lv != featShortAPDU && lv != featExtAPDU {
			t.Fatalf("level %#x", lv)
		}
		if l.bulkOut&0x80 != 0 || l.bulkIn&0x80 == 0 {
			t.Fatalf("endpoint directions %#x %#x", l.bulkOut, l.bulkIn)
		}
	})
}

func TestClassDescriptorFieldOffsets(t *testing.T) {
	cls := make([]byte, 54)
	cls[0], cls[1] = 54, 0x21
	for i := 2; i < 54; i++ {
		cls[i] = byte(i)
	}
	c, err := parseClass(cls, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2]uint64{
		"bcdCCID":    {uint64(c.bcdCCID), 0x0302},
		"slotIndex":  {uint64(c.maxSlotIndex), 4},
		"voltage":    {uint64(c.voltageSupport), 5},
		"protocols":  {uint64(c.protocols), 0x09080706},
		"features":   {uint64(c.features), 0x2B2A2928},
		"maxMessage": {uint64(c.maxMessageLength), 0x2F2E2D2C},
	}
	for name, v := range want {
		if v[0] != v[1] {
			t.Errorf("%s = %#x, want %#x", name, v[0], v[1])
		}
	}
}

// The error texts of the refusals hold the numbers of the descriptors and no byte of them beyond those.
func TestRefusalTextsAreShortAndNameTheRule(t *testing.T) {
	for _, s := range []descSpec{{maxSlot: 1}, {features: 0x00010000 | 2}, {noClass: true}, {classLen: 50}, {maxMsg: 100}} {
		_, err := parseDescriptors(s.build(), nil)
		if err == nil || len(err.Error()) > 300 {
			t.Errorf("%+v: %v", s, err)
		}
	}
}
