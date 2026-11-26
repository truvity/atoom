//go:build linux && !android && (amd64 || arm64)

package usbccid

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Made-up USB descriptors for the tests of the device. Nothing here is the descriptor of a real device: the ids are 1234:5678 and the
// endpoint numbers are the ones listed below.

// The made-up composite device: interface 0 is a HID interface with two interrupt endpoints, interface 1 is a CCID interface (bulk
// OUT 0x02, bulk IN 0x82, interrupt IN 0x83, a class descriptor between the interface and the endpoints, as a real CCID interface has),
// interface 2 is a vendor interface with its own bulk endpoints, and interface 3 is a second CCID interface (bulk 0x06 and 0x86, interrupt 0x87), so that a test can hold two claims.
const (
	qaVID, qaPID = 0x1234, 0x5678

	qaCCID, qaCCID2, qaHID, qaVendor = 1, 3, 0, 2

	qaOut, qaIn, qaIntr     = 0x02, 0x82, 0x83 // the CCID interface 1
	qaOut2, qaIn2, qaIntr2  = 0x06, 0x86, 0x87 // the CCID interface 3
	qaVOut, qaVIn           = 0x04, 0x84       // the vendor interface 2: bulk endpoints of an interface that is not CCID
	qaHIn, qaHOut           = 0x81, 0x01       // the HID interface 0: interrupt endpoints
	qaAltOut, qaAltIn       = 0x08, 0x88       // an endpoint pair that no interface lists (the descriptors are simple: no alternate settings)
	qaClassCCID, qaClassHID = 0x0B, 0x03
)

// qaEP is an endpoint descriptor.
func qaEP(addr, attrs byte) []byte { return []byte{7, 5, addr, attrs, 64, 0, 0} }

// qaIface is an interface descriptor followed by the given descriptors (endpoints or others); bNumEndpoints is the number of endpoint
// descriptors among them.
func qaIface(num, alt, class byte, rest ...[]byte) []byte {
	declared := 0
	for _, r := range rest {
		if len(r) >= 2 && r[1] == descEndpoint {
			declared++
		}
	}
	return qaIfaceN(num, alt, class, byte(declared), rest...)
}

// qaIfaceN is qaIface with bNumEndpoints as given, which may disagree with the endpoint descriptors that follow.
func qaIfaceN(num, alt, class, declared byte, rest ...[]byte) []byte {
	b := []byte{9, 4, num, alt, declared, class, 0, 0, 0}
	for _, r := range rest {
		b = append(b, r...)
	}
	return b
}

// qaCfg is a configuration descriptor with the given descriptors after it, and its total length filled in. declared is bNumInterfaces.
func qaCfg(declared byte, parts ...[]byte) []byte {
	b := []byte{9, 2, 0, 0, declared, 1, 0, 0x80, 50}
	for _, p := range parts {
		b = append(b, p...)
	}
	b[2], b[3] = byte(len(b)), byte(len(b)>>8) //nolint:gosec // the low and the high byte of a length below 64 KiB
	return b
}

// qaDev is a device descriptor of a device with one configuration.
func qaDev(vid, pid uint16) []byte { return qaDevN(vid, pid, 1) }

// qaDevN is a device descriptor with bNumConfigurations as given.
func qaDevN(vid, pid uint16, configs byte) []byte {
	return []byte{18, 1, 0, 2, 0, 0, 0, 64, byte(vid), byte(vid >> 8), byte(pid), byte(pid >> 8), 0, 1, 0, 0, 0, configs} //nolint:gosec // the low and the high byte
}

// qaBlob is what a usbfs node returns: the device descriptor and the configurations.
func qaBlob(vid, pid uint16, cfgs ...[]byte) []byte { return qaBlobN(vid, pid, 1, cfgs...) }

// qaBlobN is qaBlob for a device that declares the given number of configurations, whatever follows.
func qaBlobN(vid, pid uint16, configs byte, cfgs ...[]byte) []byte {
	b := qaDevN(vid, pid, configs)
	for _, c := range cfgs {
		b = append(b, c...)
	}
	return b
}

var qaCCIDClassDesc = func() []byte { b := make([]byte, 0x36); b[0], b[1] = 0x36, 0x21; return b }()

// qaDefaultDesc is the descriptors of the made-up composite device described above.
func qaDefaultDesc() []byte {
	return qaBlob(qaVID, qaPID, qaCfg(4,
		qaIface(qaHID, 0, qaClassHID, qaEP(qaHIn, 3), qaEP(qaHOut, 3)),
		qaIface(qaCCID, 0, qaClassCCID, qaCCIDClassDesc, qaEP(qaOut, 2), qaEP(qaIn, 2), qaEP(qaIntr, 3)),
		qaIface(qaVendor, 0, 0xFF, qaEP(qaVOut, 2), qaEP(qaVIn, 2)),
		qaIface(qaCCID2, 0, qaClassCCID, qaCCIDClassDesc, qaEP(qaOut2, 2), qaEP(qaIn2, 2), qaEP(qaIntr2, 3)),
	))
}

// bulkOf lists the addresses of the bulk endpoints of an interface, as the parser holds them.
func bulkOf(in *ifaceDesc) []uint8 {
	var out []uint8
	for _, e := range in.eps {
		if e.bulk() {
			out = append(out, e.addr)
		}
	}
	return out
}

func TestQADefaultDescriptorsParse(t *testing.T) {
	l, err := parseLayout(qaDefaultDesc())
	if err != nil || l.vendor != qaVID || l.product != qaPID || len(l.ifaces) != 4 {
		t.Fatalf("%+v %v", l, err)
	}
	for _, c := range []struct {
		n     int
		class uint8
		bulk  []uint8
		all   int
	}{{qaHID, qaClassHID, nil, 2}, {qaCCID, qaClassCCID, []uint8{qaOut, qaIn}, 3}, {qaVendor, 0xFF, []uint8{qaVOut, qaVIn}, 2}, {qaCCID2, qaClassCCID, []uint8{qaOut2, qaIn2}, 3}} {
		in := l.find(c.n)
		if in == nil || in.class != c.class || !equalBytes(bulkOf(in), c.bulk) || len(in.eps) != c.all {
			t.Errorf("interface %d: %+v, want class %#x bulk %v and %d endpoints of any type", c.n, in, c.class, c.bulk, c.all)
		}
	}
	if o, err := l.bulkOwner(qaOut); err != nil || o.number != qaCCID {
		t.Errorf("owner of 0x02: %v %v", o, err)
	}
	for _, a := range []uint8{qaAltOut, qaAltIn, qaIntr, qaHIn, 0x00, 0x80, 0x8F} {
		if _, err := l.bulkOwner(a); !errors.Is(err, ErrBadEndpoint) {
			t.Errorf("owner of %#02x: %v", a, err)
		}
	}
	if l.find(4) != nil || l.find(-1) != nil {
		t.Error("an interface that is not there was found")
	}
}

func equalBytes(a, b []uint8) bool { return bytes.Equal(a, b) }

// The valid composite set of the table below: a HID interface with one interrupt endpoint, a HID interface with two, and a CCID
// interface with bulk OUT, bulk IN and interrupt IN, with a class-specific descriptor between each interface and its endpoints.
var (
	qaHIDClass = []byte{9, 0x21, 0x11, 0x01, 0, 1, 0x22, 0x40, 0}
	qaHID1     = func() []byte { return qaIface(0, 0, 3, qaHIDClass, qaEP(0x81, 3)) }
	qaHID2     = func() []byte { return qaIface(1, 0, 3, qaHIDClass, qaEP(0x82, 3), qaEP(0x02, 3)) }
	qaCCIDIf   = func() []byte {
		return qaIface(2, 0, 0x0B, qaCCIDClassDesc, qaEP(0x04, 2), qaEP(0x84, 2), qaEP(0x85, 3))
	}
)

// qaSet builds a configuration of the given interface blocks (their bNumInterfaces is the number of blocks) behind a device descriptor.
func qaSet(blocks ...[]byte) []byte {
	return qaBlob(0x7EAF, 0xA7B3, qaCfg(byte(len(blocks)), blocks...)) //nolint:gosec // a few blocks
}

// Descriptor sets that Open refuses, each a small edit of the valid set that breaks exactly one rule, and sets that Open takes, each a
// harmless variant. The refusal is ErrBadDescriptors (ErrSeveralConfigurations for a device that declares other than one
// configuration), it names the rule, it holds no byte of the descriptors, and no descriptor is left open.
func TestQAOpenTakesOnlySimpleDescriptors(t *testing.T) {
	valid := qaSet(qaHID1(), qaHID2(), qaCCIDIf())
	edit := func(f func(b []byte) []byte) []byte { return f(slicesClone(valid)) }
	setCfg := func(off int, v byte) []byte { return edit(func(b []byte) []byte { b[18+off] = v; return b }) }
	setLen := func(total int) []byte {
		return edit(func(b []byte) []byte { b[18+2], b[18+3] = byte(total), byte(total>>8); return b }) //nolint:gosec // the low and the high byte
	}
	assoc := []byte{8, 0x0B, 0, 2, 0x0B, 0, 0, 0}
	type tc struct {
		name string
		blob []byte
		want error // nil: the device is taken
	}
	cases := []tc{
		// the valid set and its harmless variants
		{"the valid set", valid, nil},
		{"a class-specific descriptor after the endpoints of an interface", qaSet(qaHID1(), append(qaHID2(), qaCCIDClassDesc...), qaCCIDIf()), nil},
		{"a class-specific descriptor after the last interface", qaSet(qaHID1(), qaHID2(), append(qaCCIDIf(), qaHIDClass...)), nil},
		{"an interface association before an interface", qaSet(qaHID1(), append(slicesClone(assoc), qaHID2()...), qaCCIDIf()), nil},
		{"an interface association before the first interface", qaSet(append(slicesClone(assoc), qaHID1()...), qaHID2(), qaCCIDIf()), nil},
		{"interfaces in another order", qaSet(qaCCIDIf(), qaHID2(), qaHID1()), nil},
		{"one interface only", qaSet(qaCCIDIf()), nil},
		{"an interface without endpoints", qaSet(qaIface(0, 0, 0xFF), qaCCIDIf()), nil},
		{"the same endpoint number in the two directions", qaSet(qaIface(0, 0, 0xFF, qaEP(0x01, 2), qaEP(0x81, 2))), nil},
		{"every endpoint number 1 to 15 once", func() []byte {
			var eps [][]byte
			for n := 1; n <= 15; n++ {
				eps = append(eps, qaEP(byte(n), 2))
			}
			return qaSet(qaIface(0, 0, 0xFF, eps...))
		}(), nil},
		{"a descriptor of another type with a long length", qaSet(append(qaHID1(), append([]byte{250, 0x30}, make([]byte, 248)...)...), qaHID2(), qaCCIDIf()), nil},
		{"an endpoint descriptor longer than 7 bytes", qaSet(qaIface(0, 0, 1, []byte{9, 5, 0x81, 3, 8, 0, 10, 0, 0})), nil},
		// the device descriptor and the configuration count
		{"nothing", nil, ErrBadDescriptors},
		{"empty", []byte{}, ErrBadDescriptors},
		{"17 bytes", valid[:17], ErrBadDescriptors},
		{"a device descriptor only", valid[:18], ErrBadDescriptors},
		{"a first descriptor of another type", edit(func(b []byte) []byte { b[1] = 2; return b }), ErrBadDescriptors},
		{"a device descriptor of length 19", edit(func(b []byte) []byte { b[0] = 19; return b }), ErrBadDescriptors},
		{"0 configurations", edit(func(b []byte) []byte { b[17] = 0; return b }), ErrSeveralConfigurations},
		{"2 configurations", edit(func(b []byte) []byte { b[17] = 2; return b }), ErrSeveralConfigurations},
		{"255 configurations", edit(func(b []byte) []byte { b[17] = 255; return b }), ErrSeveralConfigurations},
		{"two configurations in the bytes, one declared", append(slicesClone(valid), qaCfg(1, qaCCIDIf())...), ErrBadDescriptors},
		{"two configurations declared and present", func() []byte { b := qaBlobN(0x7EAF, 0xA7B3, 2, qaCfg(1, qaCCIDIf()), qaCfg(1, qaHID1())); return b }(), ErrSeveralConfigurations},
		// the configuration descriptor and its length
		{"no configuration descriptor", qaDev(1, 2), ErrBadDescriptors},
		{"a configuration of another type", setCfg(1, 4), ErrBadDescriptors},
		{"configuration length byte 0", setCfg(0, 0), ErrBadDescriptors},
		{"configuration length byte 8", setCfg(0, 8), ErrBadDescriptors},
		{"configuration length byte past the end", setCfg(0, 255), ErrBadDescriptors},
		{"a total length one too long", setLen(len(valid) - 18 + 1), ErrBadDescriptors},
		{"a total length one too short", setLen(len(valid) - 18 - 1), ErrBadDescriptors},
		{"a total length of 0", setLen(0), ErrBadDescriptors},
		{"a total length of 65535", setLen(65535), ErrBadDescriptors},
		{"a byte after the configuration", append(slicesClone(valid), 0), ErrBadDescriptors},
		{"cut one byte short", valid[:len(valid)-1], ErrBadDescriptors},
		{"cut in the first interface", valid[:18+9+5], ErrBadDescriptors},
		// bNumInterfaces
		{"bNumInterfaces one more", setCfg(4, 4), ErrBadDescriptors},
		{"bNumInterfaces one less", setCfg(4, 2), ErrBadDescriptors},
		{"bNumInterfaces 0", setCfg(4, 0), ErrBadDescriptors},
		{"bNumInterfaces 255", setCfg(4, 255), ErrBadDescriptors},
		// alternate settings
		{"an alternate setting 1 after the interface", qaSet(qaHID1(), qaHID2(), qaCCIDIf(), qaIface(2, 1, 0x0B, qaEP(0x06, 2), qaEP(0x86, 2))), ErrBadDescriptors},
		{"an alternate setting 1 without endpoints", qaSet(qaHID1(), qaHID2(), qaCCIDIf(), qaIface(2, 1, 0x0B)), ErrBadDescriptors},
		{"an interface that has only alternate setting 1 (declared 3)", qaSet(qaHID1(), qaHID2(), qaIface(2, 1, 0x0B, qaEP(0x04, 2), qaEP(0x84, 2))), ErrBadDescriptors},
		{"alternate setting 2", qaSet(qaHID1(), qaHID2(), qaIface(2, 2, 0x0B, qaEP(0x04, 2), qaEP(0x84, 2))), ErrBadDescriptors},
		{"alternate setting 255", qaSet(qaHID1(), qaIface(1, 255, 3)), ErrBadDescriptors},
		// repeated numbers and addresses
		{"an interface number listed twice", qaSet(qaHID1(), qaHID2(), qaCCIDIf(), qaIface(2, 0, 0xFF)), ErrBadDescriptors},
		{"an interface number listed twice and declared once", qaSet(qaHID1(), qaIface(0, 0, 3)), ErrBadDescriptors},
		{"an endpoint address listed twice by one interface", qaSet(qaIface(0, 0, 0xFF, qaEP(0x04, 2), qaEP(0x04, 2))), ErrBadDescriptors},
		{"an endpoint address of the CCID interface listed by a HID interface", qaSet(qaHID1(), qaIface(1, 0, 3, qaEP(0x04, 3)), qaCCIDIf()), ErrBadDescriptors},
		{"an in address listed twice", qaSet(qaHID1(), qaIface(1, 0, 3, qaEP(0x81, 3)), qaCCIDIf()), ErrBadDescriptors},
		// the number of endpoint descriptors
		{"bNumEndpoints one more than listed (last interface)", edit(func(b []byte) []byte {
			return bytesReplaceIface(b, qaCCIDIf(), qaIfaceN(2, 0, 0x0B, 4, qaCCIDClassDesc, qaEP(0x04, 2), qaEP(0x84, 2), qaEP(0x85, 3)))
		}), ErrBadDescriptors},
		{"bNumEndpoints one more than listed (first interface)", qaSet(qaIfaceN(0, 0, 3, 2, qaHIDClass, qaEP(0x81, 3)), qaHID2(), qaCCIDIf()), ErrBadDescriptors},
		{"bNumEndpoints one less than listed", qaSet(qaHID1(), qaHID2(), qaIfaceN(2, 0, 0x0B, 2, qaCCIDClassDesc, qaEP(0x04, 2), qaEP(0x84, 2), qaEP(0x85, 3))), ErrBadDescriptors},
		{"bNumEndpoints 0 with endpoints listed", qaSet(qaHID1(), qaIfaceN(1, 0, 3, 0, qaEP(0x82, 3)), qaCCIDIf()), ErrBadDescriptors},
		{"bNumEndpoints 2 with 3 listed (the third is not ignored)", qaSet(qaHID1(), qaHID2(), qaIfaceN(2, 0, 0x0B, 2, qaCCIDClassDesc, qaEP(0x04, 2), qaEP(0x84, 2), qaEP(0x06, 2))), ErrBadDescriptors},
		{"bNumEndpoints 255", qaSet(qaHID1(), qaIfaceN(1, 0, 3, 255, qaEP(0x82, 3)), qaCCIDIf()), ErrBadDescriptors},
		{"an endpoint before any interface", qaSet(append(qaEP(0x81, 3), qaHID1()...), qaCCIDIf()), ErrBadDescriptors},
		// endpoint addresses
		{"endpoint number 0", qaSet(qaIface(0, 0, 0xFF, qaEP(0x00, 2))), ErrBadDescriptors},
		{"endpoint number 0 in", qaSet(qaIface(0, 0, 0xFF, qaEP(0x80, 2))), ErrBadDescriptors},
		{"a reserved bit 4", qaSet(qaIface(0, 0, 0xFF, qaEP(0x12, 2))), ErrBadDescriptors},
		{"a reserved bit 5", qaSet(qaIface(0, 0, 0xFF, qaEP(0x22, 2))), ErrBadDescriptors},
		{"a reserved bit 6", qaSet(qaIface(0, 0, 0xFF, qaEP(0xC2, 2))), ErrBadDescriptors},
		{"all reserved bits", qaSet(qaIface(0, 0, 0xFF, qaEP(0xFF, 2))), ErrBadDescriptors},
		// lengths
		{"a descriptor of length 0", qaSet(qaHID1(), qaHID2(), append(qaCCIDIf(), 0, 0x30)), ErrBadDescriptors},
		{"a descriptor of length 1", qaSet(qaHID1(), qaHID2(), append(qaCCIDIf(), 1)), ErrBadDescriptors},
		{"a class-specific descriptor that runs past the end", qaSet(qaHID1(), qaHID2(), append(qaCCIDIf(), 20, 0x21, 0)), ErrBadDescriptors},
		{"an interface descriptor of length 8", qaSet(qaHID1(), qaHID2(), qaCCIDIf(), []byte{8, 4, 3, 0, 0, 0xFF, 0, 0}), ErrBadDescriptors},
		{"an endpoint descriptor of length 6", qaSet(qaIface(0, 0, 0xFF, []byte{6, 5, 0x81, 2, 64, 0})), ErrBadDescriptors},
		{"an endpoint descriptor that runs past the end", qaSet(qaIface(0, 0, 0xFF), []byte{7, 5, 0x81, 2}), ErrBadDescriptors},
	}
	before := openFDs(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "node")
			if err := os.WriteFile(path, c.blob, 0o600); err != nil {
				t.Fatal(err)
			}
			d, err := open(path, false)
			if c.want == nil {
				if err != nil {
					t.Fatalf("a simple set is refused: %v", err)
				}
				_ = d.Close()
				return
			}
			if d != nil || !errors.Is(err, c.want) {
				t.Fatalf("got %v, %v, want %v", d, err, c.want)
			}
			if errors.Is(c.want, ErrSeveralConfigurations) && errors.Is(err, ErrBadDescriptors) {
				t.Errorf("several configurations is its own refusal: %v", err)
			}
			// The text names a rule and holds no byte of the descriptors: the ids 7EAF:A7B3 are in every set.
			text := strings.ToLower(strings.ReplaceAll(err.Error(), path, ""))
			if regexp.MustCompile(`7eaf|af7e|a7b3|b3a7|0x[0-9a-f]{2}|31407|42931`).MatchString(text) {
				t.Errorf("the refusal holds bytes of the descriptors: %q", err)
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("the refusal does not name the node: %q", err)
			}
		})
	}
	if after := openFDs(t); after != before {
		t.Errorf("descriptors left open by the refusals: %d -> %d", before, after)
	}
}

func slicesClone(b []byte) []byte { return append([]byte(nil), b...) }

// bytesReplaceIface replaces the first occurrence of the interface block old by repl in a descriptor set and fixes the total length.
func bytesReplaceIface(b, old, repl []byte) []byte {
	i := bytes.Index(b, old)
	if i < 0 {
		panic("the interface block is not in the set")
	}
	b = append(append(slicesClone(b[:i]), repl...), b[i+len(old):]...)
	b[18+2], b[18+3] = byte(len(b)-18), byte((len(b)-18)>>8) //nolint:gosec // the low and the high byte
	return b
}

// For the valid set: Claim only for the CCID interface, a bulk call only on its two bulk endpoints in the right direction after the
// claim; the interrupt endpoint and the endpoints of the other interfaces are refused before the kernel is asked.
func TestQAValidSetHeldToTheCCIDInterface(t *testing.T) {
	k := &qaKernel{}
	d, _ := qaDevice(t, qaSet(qaHID1(), qaHID2(), qaCCIDIf()), k)
	for n := -1; n < 300; n++ {
		if n == 2 {
			continue
		}
		if err := d.Claim(n); !errors.Is(err, ErrNoSuchInterface) {
			t.Fatalf("claim %d: %v", n, err)
		}
	}
	buf := make([]byte, 8)
	if _, err := d.BulkOut(0x04, buf, time.Second); !errors.Is(err, ErrNotClaimed) {
		t.Errorf("before the claim: %v", err)
	}
	if k.count() != 0 {
		t.Fatalf("%d kernel calls", k.count())
	}
	if err := d.Claim(2); err != nil {
		t.Fatal(err)
	}
	base := k.count()
	for ep := range 256 {
		e := uint8(ep)
		_, eo := d.BulkOut(e, buf, time.Second)
		_, ei := d.BulkIn(e, buf, time.Second)
		if (eo == nil) != (e == 0x04) || (ei == nil) != (e == 0x84) {
			t.Errorf("endpoint %#02x: out %v, in %v", e, eo, ei)
		}
		if e == 0x85 || e == 0x81 || e == 0x82 || e == 0x02 {
			if !errors.Is(ei, ErrBadEndpoint) || !errors.Is(eo, ErrBadEndpoint) {
				t.Errorf("the interrupt endpoint or an endpoint of a HID interface %#02x: %v %v", e, eo, ei)
			}
		}
	}
	if got := k.count() - base; got != 2 {
		t.Errorf("%d kernel calls, want the two allowed ones", got)
	}
}

// Descriptors of 64 KiB: a valid configuration of exactly the 64 KiB that are read, with more bytes behind them in the node, is
// taken; one that is longer than the 64 KiB is refused.
func TestQALargeDescriptors(t *testing.T) {
	// 250 interfaces without endpoints, padded with class-specific descriptors to the exact length.
	pad := func(n int) []byte { return append([]byte{byte(n), 0x21}, make([]byte, n-2)...) } //nolint:gosec // at most 255
	build := func(total int) []byte {
		var parts [][]byte
		for i := range 250 {
			parts = append(parts, qaIface(byte(i), 0, 0xFF))
		}
		cfg := qaCfg(250, parts...)
		for rem := total - len(cfg); rem > 0; {
			n := min(rem, 200)
			if rem-n == 1 {
				n--
			}
			cfg = append(cfg, pad(n)...)
			rem -= n
		}
		cfg[2], cfg[3] = byte(len(cfg)), byte(len(cfg)>>8) //nolint:gosec // the low and the high byte
		return qaBlob(1, 2, cfg)
	}
	blob := build(65536 - 18)
	if len(blob) != 65536 {
		t.Fatalf("the test blob is %d bytes", len(blob))
	}
	l, err := parseLayout(blob)
	if err != nil || len(l.ifaces) != 250 {
		t.Errorf("a configuration that fills 64 KiB: %d interfaces, %v", len(l.ifaces), err)
	}
	write := func(b []byte) string {
		path := filepath.Join(t.TempDir(), "node")
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	// The node holds more than is read: the read stops at 64 KiB, and what is left behind is never seen.
	d, err := open(write(append(slicesClone(blob), make([]byte, 5000)...)), false)
	if err != nil {
		t.Fatalf("descriptors of 64 KiB with more in the node: %v", err)
	}
	if b, _ := d.Descriptors(); len(b) != maxDescriptorBytes {
		t.Errorf("descriptors of %d bytes", len(b))
	}
	_ = d.Close()
	// A configuration that says it is longer than what is read.
	before := openFDs(t)
	over := build(65536 - 18 + 100)
	if d, err := open(write(over), false); !errors.Is(err, ErrBadDescriptors) || d != nil {
		t.Errorf("a configuration longer than 64 KiB: %v %v", d, err)
	}
	if after := openFDs(t); after != before {
		t.Errorf("a descriptor leaked: %d -> %d", before, after)
	}
}

// Every cut of the default descriptors, at every length: no panic, and an error at every length but the whole.
func TestQAParseLayoutEveryCut(t *testing.T) {
	good := qaDefaultDesc()
	for n := 0; n <= len(good); n++ {
		_, err := parseLayout(good[:n])
		if (err == nil) != (n == len(good)) {
			t.Errorf("cut at %d of %d: error %v", n, len(good), err)
		}
	}
}

// qaRead is a tiny independent reader of a descriptor set that parseLayout accepted: it walks the bytes by their lengths and returns
// what it finds, with no rule of the parser in it. ok is false when the bytes do not walk (which an accepted set must).
type qaSeen struct {
	ok     bool
	ifaces []qaSeenIface
}
type qaSeenIface struct {
	num, alt int
	eps      []byte
}

func qaRead(b []byte) qaSeen {
	var s qaSeen
	if len(b) < 27 {
		return s
	}
	p := b[18+int(b[18]):]
	for len(p) > 0 {
		n := int(p[0])
		if n < 2 || n > len(p) {
			return s
		}
		switch p[1] {
		case 4:
			if n < 9 {
				return s
			}
			s.ifaces = append(s.ifaces, qaSeenIface{num: int(p[2]), alt: int(p[3])})
		case 5:
			if n < 7 || len(s.ifaces) == 0 {
				return s
			}
			last := &s.ifaces[len(s.ifaces)-1]
			last.eps = append(last.eps, p[2])
		}
		p = p[n:]
	}
	s.ok = true
	return s
}

// A fuzz target for the parser, with seeds built in code. The property: a descriptor set is either refused, or every endpoint
// address of the layout belongs to exactly one interface and every interface has alternate setting 0 only, checked against an
// independent reader of the same bytes.
func FuzzParseLayout(f *testing.F) {
	for _, b := range [][]byte{qaDefaultDesc(), qaSet(qaHID1(), qaHID2(), qaCCIDIf()), qaDefaultDesc()[:18], qaDefaultDesc()[:30], qaDefaultDesc()[:len(qaDefaultDesc())-1],
		{}, {18, 1}, qaDev(0, 0), qaBlob(1, 2, qaCfg(1, qaEP(qaOut, 2))), qaBlobN(1, 2, 2, qaCfg(1, qaCCIDIf())),
		qaSet(qaIface(0, 0, 0xFF, qaEP(4, 2), qaEP(4, 2))), qaSet(qaIface(0, 0, 0xFF), qaIface(0, 0, 0xFF)), qaSet(qaIface(0, 1, 0xFF)),
		qaSet(qaIfaceN(0, 0, 0xFF, 3, qaEP(4, 2))), qaSet(qaIface(0, 0, 0xFF, qaEP(0x12, 2))), qaSet(qaIface(0, 0, 0xFF, qaEP(0x01, 2), qaEP(0x81, 2))),
		bytes.Repeat([]byte{9, 2, 9, 0, 1, 1, 0, 0x80, 50}, 20), append(qaDev(1, 2), bytes.Repeat([]byte{0xFF}, 300)...)} {
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		l, err := parseLayout(b)
		l2, err2 := parseLayout(b)
		if (err == nil) != (err2 == nil) || len(l.ifaces) != len(l2.ifaces) {
			t.Fatal("two parses of the same bytes differ")
		}
		if err != nil {
			if len(l.ifaces) != 0 {
				t.Fatalf("a refused set leaves %d interfaces", len(l.ifaces))
			}
			// A refused set opens nothing: a device built on what is left claims and moves nothing.
			d := &Device{opened: true, fd: -1, lay: l, claimed: map[int]bool{}}
			for n := -1; n < 258; n++ {
				if err := d.Claim(n); !errors.Is(err, ErrNoSuchInterface) {
					t.Fatalf("claim %d on a refused set: %v", n, err)
				}
			}
			return
		}
		// Accepted: the bytes are what the rules say.
		if len(b) < 18 || b[17] != 1 {
			t.Fatalf("a device that declares %v configurations is accepted", b[min(17, len(b)-1):])
		}
		if l.vendor != uint16(b[8])|uint16(b[9])<<8 || l.product != uint16(b[10])|uint16(b[11])<<8 {
			t.Fatalf("ids %04x:%04x from %x", l.vendor, l.product, b[8:12])
		}
		if int(b[18+2])|int(b[18+3])<<8 != len(b)-18 || int(b[18+4]) != len(l.ifaces) {
			t.Fatal("the length or bNumInterfaces of an accepted set is not as declared")
		}
		seen := qaRead(b)
		if !seen.ok || len(seen.ifaces) != len(l.ifaces) {
			t.Fatalf("the independent reader finds %+v, the layout %d interfaces", seen, len(l.ifaces))
		}
		owner := map[byte]int{}
		for i, si := range seen.ifaces {
			if si.alt != 0 {
				t.Fatalf("interface %d has alternate setting %d", si.num, si.alt)
			}
			in := l.find(si.num)
			if in == nil || l.ifaces[i].number != si.num || len(in.eps) != len(si.eps) {
				t.Fatalf("the layout and the reader disagree on interface %d", si.num)
			}
			for j, a := range si.eps {
				if in.eps[j].addr != a || !validEndpoint(a) {
					t.Fatalf("endpoint %#x of interface %d", a, si.num)
				}
				if prev, dup := owner[a]; dup {
					t.Fatalf("the address %#x belongs to interfaces %d and %d", a, prev, si.num)
				}
				owner[a] = si.num
			}
		}
		// Every address of the layout has exactly one owner in the layout itself.
		for _, in := range l.ifaces {
			for _, e := range in.eps {
				o, err := l.bulkOwner(e.addr)
				if e.bulk() && (err != nil || o.number != in.number) {
					t.Fatalf("bulk endpoint %#x of interface %d: owner %v, %v", e.addr, in.number, o, err)
				}
				if !e.bulk() && err == nil {
					t.Fatalf("endpoint %#x is not bulk and has an owner", e.addr)
				}
			}
		}
	})
}
