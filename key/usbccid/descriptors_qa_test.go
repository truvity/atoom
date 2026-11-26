//go:build linux && (amd64 || arm64)

package usbccid

import (
	"bytes"
	"errors"
	"slices"
	"testing"
)

// Made-up USB descriptors for the tests of the device. Nothing here is the descriptor of a real device: the ids are 1234:5678 and the
// endpoint numbers are the ones listed below.

// The made-up composite device: interface 0 is a HID interface with two interrupt endpoints, interface 1 is a CCID interface (bulk
// OUT 0x02, bulk IN 0x82, interrupt IN 0x83, a class descriptor between the interface and the endpoints, as a real CCID interface has)
// that also has an alternate setting 1 with other endpoints, interface 2 is a vendor interface with its own bulk endpoints, and
// interface 3 is a second CCID interface (bulk 0x06 and 0x86, interrupt 0x87), so that a test can hold two claims.
const (
	qaVID, qaPID = 0x1234, 0x5678

	qaCCID, qaCCID2, qaHID, qaVendor = 1, 3, 0, 2

	qaOut, qaIn, qaIntr     = 0x02, 0x82, 0x83 // the CCID interface 1
	qaOut2, qaIn2, qaIntr2  = 0x06, 0x86, 0x87 // the CCID interface 3
	qaVOut, qaVIn           = 0x04, 0x84       // the vendor interface 2: bulk endpoints of an interface that is not CCID
	qaHIn, qaHOut           = 0x81, 0x01       // the HID interface 0: interrupt endpoints
	qaAltOut, qaAltIn       = 0x08, 0x88       // alternate setting 1 of the CCID interface 1
	qaClassCCID, qaClassHID = 0x0B, 0x03
)

// qaEP is an endpoint descriptor.
func qaEP(addr, attrs byte) []byte { return []byte{7, 5, addr, attrs, 64, 0, 0} }

// qaIface is an interface descriptor followed by the given descriptors (endpoints or others).
func qaIface(num, alt, class byte, rest ...[]byte) []byte {
	b := []byte{9, 4, num, alt, 0, class, 0, 0, 0}
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

// qaDev is a device descriptor.
func qaDev(vid, pid uint16) []byte {
	return []byte{18, 1, 0, 2, 0, 0, 0, 64, byte(vid), byte(vid >> 8), byte(pid), byte(pid >> 8), 0, 1, 0, 0, 0, 1} //nolint:gosec // the low and the high byte
}

// qaBlob is what a usbfs node returns: the device descriptor and the configurations.
func qaBlob(vid, pid uint16, cfgs ...[]byte) []byte {
	b := qaDev(vid, pid)
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
		qaIface(qaCCID, 1, qaClassCCID, qaEP(qaAltOut, 2), qaEP(qaAltIn, 2)),
		qaIface(qaVendor, 0, 0xFF, qaEP(qaVOut, 2), qaEP(qaVIn, 2)),
		qaIface(qaCCID2, 0, qaClassCCID, qaCCIDClassDesc, qaEP(qaOut2, 2), qaEP(qaIn2, 2), qaEP(qaIntr2, 3)),
	))
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
	}{{qaHID, qaClassHID, nil}, {qaCCID, qaClassCCID, []uint8{qaOut, qaIn}}, {qaVendor, 0xFF, []uint8{qaVOut, qaVIn}}, {qaCCID2, qaClassCCID, []uint8{qaOut2, qaIn2}}} {
		in := l.find(c.n)
		if in == nil || in.class != c.class || !slices.Equal(in.bulk, c.bulk) {
			t.Errorf("interface %d: %+v, want class %#x bulk %v", c.n, in, c.class, c.bulk)
		}
	}
	if l.find(qaCCID).has(qaAltOut) || l.find(qaCCID).has(qaIntr) {
		t.Error("the alternate setting 1 or the interrupt endpoint counts as a bulk endpoint")
	}
	if l.find(4) != nil || l.find(-1) != nil {
		t.Error("an interface that is not there was found")
	}
}

// Malformed descriptors: each is a refusal that says what is wrong, never a panic, and what is read before the fault is only what
// was certain.
func TestQAParseLayoutMalformed(t *testing.T) {
	good := qaDefaultDesc()
	cfgStart := 18
	ccidIface := qaIface(qaCCID, 0, qaClassCCID, qaEP(qaOut, 2), qaEP(qaIn, 2))
	type tc struct {
		name    string
		blob    []byte
		wantErr bool
		ifaces  int // the number of interfaces that survive, when wantErr or not
	}
	cases := []tc{
		{"nothing at all", nil, true, 0},
		{"empty", []byte{}, true, 0},
		{"17 bytes", good[:17], true, 0},
		{"a device descriptor only", good[:18], false, 0},
		{"a device descriptor of another length", append([]byte{19}, good[1:]...), true, 0},
		{"a first descriptor of another type", append([]byte{18, 2}, good[2:]...), true, 0},
		{"one to three bytes after the device descriptor", good[:20], false, 0},
		{"four bytes of a configuration", good[:22], true, 0},
		{"configuration length byte 0", append(slices.Clone(good[:cfgStart]), append([]byte{0}, good[cfgStart+1:]...)...), true, 0},
		{"configuration length byte 1", append(slices.Clone(good[:cfgStart]), append([]byte{1}, good[cfgStart+1:]...)...), true, 0},
		{"configuration length byte 3", append(slices.Clone(good[:cfgStart]), append([]byte{3}, good[cfgStart+1:]...)...), true, 0},
		{"configuration length byte 8", append(slices.Clone(good[:cfgStart]), append([]byte{8}, good[cfgStart+1:]...)...), true, 0},
		{"configuration length byte past the end", append(slices.Clone(good[:cfgStart]), append([]byte{255}, good[cfgStart+1:]...)...), true, 0},
		{"a configuration of another type", func() []byte { b := slices.Clone(good); b[cfgStart+1] = 4; return b }(), true, 0},
		{"total length below the length byte", func() []byte { b := slices.Clone(good); b[cfgStart+2], b[cfgStart+3] = 5, 0; return b }(), true, 0},
		{"total length past the end", func() []byte { b := slices.Clone(good); b[cfgStart+2], b[cfgStart+3] = 0xFF, 0xFF; return b }(), true, 0},
		{"cut in the middle of the configuration", good[:len(good)-3], true, 0},
		{"cut one byte short", good[:len(good)-1], true, 0},
		{"a descriptor of length 0 inside", qaBlob(1, 2, qaCfg(1, []byte{0, 4, 0, 0, 0, 0x0B, 0, 0, 0})), true, 0},
		{"a descriptor of length 1 inside", qaBlob(1, 2, qaCfg(1, ccidIface, []byte{1})), true, 1},
		{"a descriptor that runs past the end", qaBlob(1, 2, qaCfg(1, ccidIface, []byte{9, 5, 0x82, 2})), true, 1},
		{"an interface descriptor of length 8", qaBlob(1, 2, qaCfg(1, ccidIface, []byte{8, 4, 5, 0, 0, 0x0B, 0, 0})), true, 1},
		{"an endpoint before any interface", qaBlob(1, 2, qaCfg(1, qaEP(qaOut, 2), qaEP(qaIn, 2))), false, 0},
		{"an endpoint of length 6 is ignored", qaBlob(1, 2, qaCfg(1, qaIface(qaCCID, 0, qaClassCCID, []byte{6, 5, qaOut, 2, 64, 0}, qaEP(qaIn, 2)))), false, 1},
		{"more interfaces than declared", qaBlob(1, 2, qaCfg(1, ccidIface, qaIface(2, 0, qaClassCCID, qaEP(5, 2), qaEP(0x85, 2)), qaIface(3, 0, qaClassCCID, qaEP(6, 2), qaEP(0x86, 2)))), false, 3},
		{"fewer interfaces than declared", qaBlob(1, 2, qaCfg(9, ccidIface)), false, 1},
		{"only an alternate setting 1", qaBlob(1, 2, qaCfg(1, qaIface(qaCCID, 1, qaClassCCID, qaEP(qaOut, 2), qaEP(qaIn, 2)))), false, 0},
		{"alternate setting 2 and 255", qaBlob(1, 2, qaCfg(1, qaIface(qaCCID, 2, qaClassCCID, qaEP(qaOut, 2)), qaIface(qaCCID, 255, qaClassCCID, qaEP(qaIn, 2)))), false, 0},
		{"two configurations", qaBlob(1, 2, qaCfg(1, ccidIface), qaCfg(1, qaIface(2, 0, 0xFF, qaEP(5, 2)))), false, 2},
		{"two configurations with the same interface number: the first counts", qaBlob(1, 2, qaCfg(1, ccidIface), qaCfg(1, qaIface(qaCCID, 0, 0xFF, qaEP(5, 2)))), false, 1},
		{"a second configuration that is cut", qaBlob(1, 2, qaCfg(1, ccidIface), qaCfg(1, ccidIface)[:12]), true, 1},
		{"the same interface number twice in one configuration", qaBlob(1, 2, qaCfg(1, ccidIface, qaIface(qaCCID, 0, 0xFF, qaEP(5, 2)))), false, 1},
		{"garbage after the configuration", append(qaBlob(1, 2, qaCfg(1, ccidIface)), 0xFF, 0xFF, 0xFF, 0xFF, 0xFF), true, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l, err := parseLayout(c.blob)
			if (err != nil) != c.wantErr {
				t.Fatalf("error %v, want error: %v", err, c.wantErr)
			}
			if err != nil && err.Error() == "" {
				t.Error("an error without a text")
			}
			if len(l.ifaces) != c.ifaces {
				t.Errorf("%d interfaces survive, want %d: %+v", len(l.ifaces), c.ifaces, l.ifaces)
			}
		})
	}
}

// Every cut of the default descriptors, at every length: no panic, an error whenever the cut is inside the configuration, and the
// interfaces that survive are the ones that were there. A cut handle claims nothing it was not sure of.
func TestQAParseLayoutEveryCut(t *testing.T) {
	good := qaDefaultDesc()
	full, err := parseLayout(good)
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n <= len(good); n++ {
		l, err := parseLayout(good[:n])
		switch {
		case n == len(good):
			if err != nil {
				t.Errorf("the whole blob: %v", err)
			}
		case n >= 18+4:
			if err == nil {
				t.Errorf("cut at %d parsed without an error", n)
			}
		}
		for _, in := range l.ifaces {
			f := full.find(in.number)
			if f == nil || f.class != in.class || !slices.Equal(f.bulk, in.bulk) {
				t.Fatalf("cut at %d: interface %+v is not one of the whole blob", n, in)
			}
		}
	}
}

// Descriptors of 64 KiB, and of more: the read at Open stops at 64 KiB, and a configuration that does not fit is refused, so the
// handle claims nothing.
func TestQALargeDescriptors(t *testing.T) {
	// A valid configuration of nearly 64 KiB: many interfaces, each with a class descriptor and endpoints.
	var parts [][]byte
	for i := range 250 {
		parts = append(parts, qaIface(byte(i), 0, 0xFF, qaEP(0x01, 2), qaEP(0x81, 2)))
	}
	pad := append([]byte{200, 0x21}, make([]byte, 198)...)
	cfg := qaCfg(250, parts...)
	for len(cfg) < 65000 {
		parts = append(parts, pad)
		cfg = qaCfg(250, parts...)
	}
	blob := qaBlob(1, 2, cfg)
	if len(blob) >= 65536 {
		t.Fatalf("the test blob is %d bytes", len(blob))
	}
	l, err := parseLayout(blob)
	if err != nil || len(l.ifaces) != 250 {
		t.Errorf("a configuration of %d bytes: %d interfaces, %v", len(blob), len(l.ifaces), err)
	}
	// Through Open: a blob of 70 000 bytes is read to 64 KiB, cut inside the configuration, and the handle holds nothing.
	big := qaBlob(1, 2, qaCfg(1, qaIface(qaCCID, 0, qaClassCCID, qaEP(qaOut, 2), qaEP(qaIn, 2), make([]byte, 70000))))
	big[18+2], big[18+3] = 0xFF, 0xFF // the total length of the configuration is the largest a 16-bit field holds, more than is read
	k := &qaKernel{}
	d, _ := qaDevice(t, big, k)
	if b, _ := d.Descriptors(); len(b) != maxDescriptorBytes {
		t.Fatalf("descriptors of %d bytes", len(b))
	}
	if err := d.Claim(qaCCID); !errors.Is(err, ErrNoSuchInterface) || k.count() != 0 {
		t.Errorf("claim on a handle whose descriptors were cut: %v, %d calls", err, k.count())
	}
}

// A fuzz target for the parser, with seeds built in code: no panic, nothing outside the bounds of the types, and what comes out is
// consistent with the bytes that went in.
func FuzzParseLayout(f *testing.F) {
	good := qaDefaultDesc()
	f.Add(good)
	f.Add(good[:18])
	f.Add(good[:30])
	f.Add(good[:len(good)-1])
	f.Add([]byte{})
	f.Add([]byte{18, 1})
	f.Add(qaBlob(0, 0))
	f.Add(qaBlob(1, 2, qaCfg(1, qaEP(qaOut, 2))))
	f.Add(qaBlob(1, 2, qaCfg(1, []byte{0}), qaCfg(2, []byte{1, 4}), qaCfg(3, []byte{9, 4, 1, 0})))
	f.Add(qaBlob(1, 2, qaCfg(1, qaIface(1, 1, 0x0B, qaEP(2, 2)), qaIface(1, 0, 0x0B, qaEP(0x12, 2), qaEP(0, 2), qaEP(0x82, 2)))))
	f.Add(bytes.Repeat([]byte{9, 2, 9, 0, 1, 1, 0, 0x80, 50}, 20))
	f.Add(append(qaDev(1, 2), bytes.Repeat([]byte{0xFF}, 300)...))
	f.Fuzz(func(t *testing.T, b []byte) {
		l, err := parseLayout(b)
		l2, err2 := parseLayout(b)
		if (err == nil) != (err2 == nil) || len(l.ifaces) != len(l2.ifaces) {
			t.Fatal("two parses of the same bytes differ")
		}
		if len(b) < 18 && (err == nil || len(l.ifaces) != 0 || l.vendor != 0 || l.product != 0) {
			t.Fatalf("%d bytes: %+v %v", len(b), l, err)
		}
		if len(b) >= 18 && err == nil && b[0] == 18 && b[1] == 1 && (l.vendor != uint16(b[8])|uint16(b[9])<<8 || l.product != uint16(b[10])|uint16(b[11])<<8) {
			t.Fatalf("ids %04x:%04x from %x", l.vendor, l.product, b[8:12])
		}
		seen := map[int]bool{}
		for _, in := range l.ifaces {
			if in.number < 0 || in.number > 255 || seen[in.number] {
				t.Fatalf("interface number %d (twice: %v)", in.number, seen[in.number])
			}
			seen[in.number] = true
			if l.find(in.number) == nil {
				t.Fatal("find does not find an interface of the layout")
			}
			for _, a := range in.bulk {
				if !validEndpoint(a) || !in.has(a) || !bytes.Contains(b, []byte{a}) {
					t.Fatalf("bulk endpoint %#x of interface %d", a, in.number)
				}
			}
			if !bytes.Contains(b, []byte{in.class}) {
				t.Fatalf("class %#x is not in the input", in.class)
			}
		}
		// A claim and a bulk call on this layout are held to it, whatever it is.
		d := &Device{lay: l, claimed: map[int]bool{}}
		d.ioctl = nil // a call that gets through the guards would panic: none may
		for n := -1; n < 258; n++ {
			in := l.find(n)
			if n >= 0 && n <= 255 && in != nil && in.class == ClassCCID {
				continue
			}
			if err := d.Claim(n); !errors.Is(err, ErrNoSuchInterface) {
				t.Fatalf("claim %d: %v", n, err)
			}
		}
		for ep := range 256 {
			if _, err := d.BulkOut(uint8(ep), []byte{1}, 1); err == nil {
				t.Fatalf("a bulk call on endpoint %#x got through with nothing claimed", ep)
			}
			if _, err := d.BulkIn(uint8(ep), []byte{1}, 1); err == nil {
				t.Fatalf("a bulk call on endpoint %#x got through with nothing claimed", ep)
			}
		}
	})
}
