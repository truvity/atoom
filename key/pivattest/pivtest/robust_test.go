package pivtest

import "testing"

func TestMalformedAPDUsNeverPanic(t *testing.T) {
	for _, tc := range []struct {
		name string
		apdu []byte
		want uint16
	}{
		{"empty", nil, swWrongLength},
		{"one byte", []byte{0}, swWrongLength},
		{"three bytes", []byte{0, insSelect, 4}, swWrongLength},
		{"Lc longer than the data", []byte{0, insVerify, 0, 0x80, 8, 1, 2}, swWrongLength},
		{"Lc shorter than the data", []byte{0, insVerify, 0, 0x80, 1, 1, 2, 3}, swWrongLength},
		{"Lc and Le with a byte too many", []byte{0, insGetData, 0x3F, 0xFF, 1, 5, 0, 0}, swWrongLength},
		{"extended with four bytes of length", []byte{0, insVerify, 0, 0x80, 0, 0}, swWrongLength},
		{"extended Lc 0 with data", []byte{0, insVerify, 0, 0x80, 0, 0, 0, 1}, swWrongLength},
		{"extended Lc longer than the data", []byte{0, insVerify, 0, 0x80, 0, 0, 8, 1, 2}, swWrongLength},
		{"extended Lc and a byte of Le", []byte{0, insVerify, 0, 0x80, 0, 0, 1, 1, 0}, swWrongLength},
		{"extended Lc with three bytes after", []byte{0, insVerify, 0, 0x80, 0, 0, 1, 1, 0, 0, 0}, swWrongLength},
		{"too long for any form", append([]byte{0, insVerify, 0, 0x80, 0xFF}, make([]byte, 300)...), swWrongLength},
	} {
		c := newSelected(t, Options{})
		data, sw := send(t, c, tc.apdu)
		wantSW(t, tc.name, sw, tc.want)
		if len(data) != 0 {
			t.Errorf("%s: data %x", tc.name, data)
		}
		if _, sw := send(t, c, []byte{0, insGetVersion, 0, 0}); sw != swOK {
			t.Errorf("%s: the card does not answer after it", tc.name)
		}
		if log := c.Log(); len(log) != 3 || !log[1].Malformed || log[1].SW != swWrongLength || log[2].Malformed {
			t.Errorf("%s: log = %+v", tc.name, log)
		}
	}
	// Whatever the form, a malformed APDU spends no try.
	c := newSelected(t, Options{})
	send(t, c, []byte{0, insVerify, 0, 0x80, 8, 1, 2})
	if c.st().pin.left != 3 {
		t.Error("a malformed VERIFY spent a try")
	}
}

func FuzzExchange(f *testing.F) {
	pin := pad(DefaultPIN)
	for _, seed := range [][]byte{
		nil, {0}, {0, 0xA4, 4, 0}, {0, 0xA4, 4, 0, 5, 0xA0, 0, 0, 3, 8, 0},
		{0, 0xFD, 0, 0}, {0, 0xF8, 0, 0, 0}, emptyVerify(), verifyCmd(DefaultPIN),
		changeCmd(refPIN, DefaultPIN, "654321"), changeCmd(refPUK, DefaultPUK, "87654321"),
		metadataCmd(refPIN), metadataCmd(refPUK), getDataCmd(ObjectSlot9A, 0), getResponseCmd(0),
		extended(0, insGetData, 0x3F, 0xFF, []byte{0x5C, 3, 0x5F, 0xC1, 5}, 0),
		short(claChain, insVerify, 0, refPIN, pin[:4], -1), {0, 0xFB, 0, 0}, {0, 0xFE, 0, 0, 0},
		{0, 0x20, 0, 0x80, 0, 0, 8, 1, 2, 3, 4, 5, 6, 7, 8, 0, 0},
		{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF},
	} {
		f.Add(seed, false, false)
	}
	f.Fuzz(func(t *testing.T, apdu []byte, short61xx, noExtended bool) {
		o := Options{NoExtendedAPDU: noExtended}
		if short61xx {
			o.ResponseMode = Short61xx
		}
		c := New(o)
		c.PowerOn()
		selectPIV(t, c)
		c.PutObject(ObjectSlot9A, bigObject(600))
		for i := 0; i < 2; i++ {
			resp, wait := c.Exchange(apdu)
			if wait != 0 || len(resp) < 2 {
				t.Fatalf("Exchange(%x) = %x, %v", apdu, resp, wait)
			}
			if sw := uint16(resp[len(resp)-2])<<8 | uint16(resp[len(resp)-1]); sw>>8 == 0x61 && len(resp) == 2 {
				t.Fatalf("61xx with no data to go with it: %x", resp)
			}
		}
		if c.st().pin.left > c.st().pin.max || c.st().puk.left > c.st().puk.max {
			t.Fatalf("a counter is above its maximum")
		}
		for _, e := range c.Log()[1:] { // the first entry is the SELECT
			if e.DataLen > len(apdu) {
				t.Fatalf("log entry %+v for an APDU of %d bytes", e, len(apdu))
			}
		}
	})
}
