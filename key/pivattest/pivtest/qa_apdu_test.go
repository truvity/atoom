package pivtest

import (
	"bytes"
	"fmt"
	"testing"
)

// The edges of the APDU forms of ISO 7816-4: the seven cases, the two forms of Le, lengths that lie, chains of extended parts, chains and
// responses that something interrupts, and responses of the sizes at which a counter or a status word changes form.

// qaCut is what a response of data is cut to at a limit, and its status word.
func qaCut(data []byte, limit int) ([]byte, uint16) {
	if len(data) <= limit {
		return data, swOK
	}
	return data[:limit], swMore | qaLow(len(data)-limit)
}

func qaPattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = qaByte(i*13 + i>>8)
	}
	return b
}

// qaForm is one command in one ISO case.
type qaForm struct {
	name    string
	apdu    []byte
	ext     bool
	hasData bool
	hasLe   bool
	le      int // as ISO reads it: 00 is 256, and 00 00 is 65536
}

// qaForms writes a command in every ISO case that its data allows: 1, 2S, 3S, 4S, 2E, 3E and 4E.
func qaForms(ins, p1, p2 byte, data []byte, leS byte, leE uint16) []qaForm {
	h := []byte{0, ins, p1, p2}
	hi, lo := qaByte(int(leE>>8)), qaByte(int(leE&0xFF))
	out := []qaForm{
		{"1", h, false, false, false, 0},
		{"2S", join(h, []byte{leS}), false, false, true, qaLe(leS)},
		{"2E", join(h, []byte{0, hi, lo}), true, false, true, qaLe2(hi, lo)},
	}
	if len(data) == 0 {
		return out
	}
	n := len(data)
	if n <= 255 {
		out = append(out,
			qaForm{"3S", join(h, []byte{qaByte(n)}, data), false, true, false, 0},
			qaForm{"4S", join(h, []byte{qaByte(n)}, data, []byte{leS}), false, true, true, qaLe(leS)})
	}
	return append(out,
		qaForm{"3E", join(h, []byte{0, qaByte(n >> 8), qaByte(n)}, data), true, true, false, 0},
		qaForm{"4E", join(h, []byte{0, qaByte(n >> 8), qaByte(n)}, data, []byte{hi, lo}), true, true, true, qaLe2(hi, lo)})
}

// limit is the most data that the answer to a form may carry, from the Le and the mode, as the doc says it.
func (f qaForm) limit(mode ResponseMode) int {
	lim := 256
	if f.ext && mode == Extended {
		lim = 65536
	}
	if f.hasLe && f.le < lim {
		lim = f.le
	}
	return lim
}

func TestQaEveryCaseOfISO7816ForACommandThatTakesDataAndOneThatDoesNot(t *testing.T) {
	obj := []byte{0x53, 0x08, 1, 2, 3, 4, 5, 6, 7, 8}
	tagList := []byte{0x5C, 0x03, 0x5F, 0xC1, 0x05}
	les := []struct {
		s byte
		e uint16
	}{{0x00, 0x0000}, {0x01, 0x0001}, {0x04, 0x0004}, {0xFF, 0x0100}, {0x0B, 0xFFFF}}
	for _, mode := range []ResponseMode{Extended, Short61xx} {
		for _, le := range les {
			// GET VERSION takes no data: the forms with data are 6700.
			for _, f := range append(qaForms(insGetVersion, 0, 0, nil, le.s, le.e), qaForms(insGetVersion, 0, 0, []byte{9}, le.s, le.e)...) {
				c := newSelected(t, Options{ResponseMode: mode})
				data, sw := send(t, c, f.apdu)
				switch {
				case f.hasData:
					if sw != swWrongLength || len(data) != 0 {
						t.Errorf("GET VERSION %s with data (mode %d, Le %02X/%04X): %x %04X, want 6700", f.name, mode, le.s, le.e, data, sw)
					}
				default:
					wd, wsw := qaCut([]byte{5, 8, 0}, f.limit(mode))
					if sw != wsw || !bytes.Equal(data, wd) {
						t.Errorf("GET VERSION %s (mode %d, Le %02X/%04X): %x %04X, want %x %04X", f.name, mode, le.s, le.e, data, sw, wd, wsw)
					}
				}
			}
			// GET DATA takes the tag list: the forms with no data are a tag list that is not there, 6A80.
			for _, f := range qaForms(insGetData, 0x3F, 0xFF, tagList, le.s, le.e) {
				c := newSelected(t, Options{ResponseMode: mode})
				c.PutObject(ObjectSlot9A, obj)
				data, sw := send(t, c, f.apdu)
				if !f.hasData {
					if sw != swWrongData || len(data) != 0 {
						t.Errorf("GET DATA %s with no data (mode %d): %x %04X, want 6A80", f.name, mode, data, sw)
					}
					continue
				}
				wd, wsw := qaCut(obj, f.limit(mode))
				if sw != wsw || !bytes.Equal(data, wd) {
					t.Errorf("GET DATA %s (mode %d, Le %02X/%04X): %x %04X, want %x %04X", f.name, mode, le.s, le.e, data, sw, wd, wsw)
				}
				if f.limit(mode) < len(obj) { // the rest is there for GET RESPONSE
					rest, sw := send(t, c, getResponseCmd(0))
					if !bytes.Equal(rest, obj[len(wd):]) || sw != swOK {
						t.Errorf("GET DATA %s: the rest is %x %04X", f.name, rest, sw)
					}
				}
			}
			// VERIFY takes the PIN: with no data it reads the counter.
			for _, f := range qaForms(insVerify, 0, refPIN, pad(DefaultPIN), le.s, le.e) {
				c := newSelected(t, Options{ResponseMode: mode})
				data, sw := send(t, c, f.apdu)
				want := uint16(swTriesLeft | 3)
				if f.hasData {
					want = swOK
				}
				if sw != want || len(data) != 0 {
					t.Errorf("VERIFY %s (mode %d): %x %04X, want %04X", f.name, mode, data, sw, want)
				}
			}
		}
	}
}

func TestQaALcOfZeroAndTheTwoSpellingsOfTheLargestLe(t *testing.T) {
	c := newSelected(t, Options{})
	for _, tc := range []struct {
		name string
		apdu []byte
		want uint16
	}{
		{"case 2S, Le 00", []byte{0, insGetVersion, 0, 0, 0}, swOK},
		{"case 2E, Le 00 00", []byte{0, insGetVersion, 0, 0, 0, 0, 0}, swOK},
		{"six bytes: a zero byte, then one", []byte{0, insGetVersion, 0, 0, 0, 0}, swWrongLength},
		{"six bytes: a zero byte, then one that is not", []byte{0, insGetVersion, 0, 0, 0, 1}, swWrongLength},
		{"extended Lc of 0 and no data, no Le", []byte{0, insGetData, 0x3F, 0xFF, 0, 0, 0, 0}, swWrongLength},
		{"extended Lc of 0 and two bytes of Le", []byte{0, insGetData, 0x3F, 0xFF, 0, 0, 0, 0, 0}, swWrongLength},
		{"extended Lc of 0 and data", []byte{0, insGetData, 0x3F, 0xFF, 0, 0, 0, 5, 0x5C, 3, 0x5F, 0xC1, 5}, swWrongLength},
		{"a short Lc of 0 and data", []byte{0, insGetData, 0x3F, 0xFF, 0, 0x5C, 3, 0x5F, 0xC1, 5}, swWrongLength},
		{"a short Lc of 0, data and Le", []byte{0, insGetData, 0x3F, 0xFF, 0, 5, 0x5C, 3, 0x5F, 0xC1, 5, 0}, swWrongLength},
		{"extended Lc of one", []byte{0, insVerify, 0, refPIN, 0, 0, 1, 0xAA}, swWrongLength},
		{"extended Lc of eight, the PIN", join([]byte{0, insVerify, 0, refPIN, 0, 0, 8}, pad(DefaultPIN)), swOK},
	} {
		wantSW(t, tc.name, qaSWOf(t, c, tc.apdu), tc.want)
	}
	// An Le of 256 is 00 in the short form; in the extended one 0100 is 256 and 0000 is 65536.
	obj := qaPattern(70000)
	c.PutObject(ObjectSlot9A, obj)
	id := []byte{0x5C, 3, 0x5F, 0xC1, 5}
	for _, tc := range []struct {
		name string
		apdu []byte
		n    int
	}{
		{"short Le 00", short(0, insGetData, 0x3F, 0xFF, id, 0), 256},
		{"short Le FF", short(0, insGetData, 0x3F, 0xFF, id, 255), 255},
		{"short Le 01", short(0, insGetData, 0x3F, 0xFF, id, 1), 1},
		{"extended Le 0000", extended(0, insGetData, 0x3F, 0xFF, id, 0), 65536},
		{"extended Le 0100", extended(0, insGetData, 0x3F, 0xFF, id, 256), 256},
		{"extended Le 0101", extended(0, insGetData, 0x3F, 0xFF, id, 257), 257},
		{"extended Le FFFF", extended(0, insGetData, 0x3F, 0xFF, id, 65535), 65535},
		{"extended Le 0001", extended(0, insGetData, 0x3F, 0xFF, id, 1), 1},
		{"extended, Le 0000, with no data: a tag list that is not there", extended(0, insGetData, 0x3F, 0xFF, nil, 0), -1},
	} {
		data, sw := send(t, c, tc.apdu)
		if tc.n < 0 {
			wantSW(t, tc.name, sw, swWrongData)
			continue
		}
		if len(data) != tc.n || !bytes.Equal(data, obj[:tc.n]) || sw>>8 != 0x61 {
			t.Errorf("%s: %d bytes and %04X, want %d bytes and 61xx", tc.name, len(data), sw, tc.n)
		}
	}
}

// A length that claims more or fewer bytes than are there, and bytes after the end.
func TestQaLengthsThatLieAndBytesAfterTheEnd(t *testing.T) {
	list := []byte{0x5C, 0x03, 0x5F, 0xC1, 0x05}
	for _, tc := range []struct {
		name string
		apdu []byte
		want uint16
	}{
		{"short, Lc one more than the data", join([]byte{0, insGetData, 0x3F, 0xFF, 6}, list), swWrongLength},
		{"short, Lc one more than the data, and an Le", join([]byte{0, insGetData, 0x3F, 0xFF, 7}, list, []byte{0}), swWrongLength},
		{"short, Lc much more", join([]byte{0, insGetData, 0x3F, 0xFF, 200}, list), swWrongLength},
		{"short, Lc one less: the last byte is read as the Le, and the tag list is short", join([]byte{0, insGetData, 0x3F, 0xFF, 4}, list), swWrongData},
		{"short, Lc two less", join([]byte{0, insGetData, 0x3F, 0xFF, 3}, list), swWrongLength},
		{"short, a byte after the Le", join([]byte{0, insGetData, 0x3F, 0xFF, 5}, list, []byte{0, 0}), swWrongLength},
		{"short, three bytes after the data", join([]byte{0, insGetData, 0x3F, 0xFF, 5}, list, []byte{0, 0, 0}), swWrongLength},
		{"short, Lc of 255 and one byte", []byte{0, insGetData, 0x3F, 0xFF, 255, 1}, swWrongLength},
		{"extended, Lc one more than the data", join([]byte{0, insGetData, 0x3F, 0xFF, 0, 0, 6}, list), swWrongLength},
		{"extended, Lc one less", join([]byte{0, insGetData, 0x3F, 0xFF, 0, 0, 4}, list), swWrongLength},
		{"extended, Lc three less: the last two bytes are read as the Le", join([]byte{0, insGetData, 0x3F, 0xFF, 0, 0, 3}, list), swWrongData},
		{"extended, one byte after the Le", join([]byte{0, insGetData, 0x3F, 0xFF, 0, 0, 5}, list, []byte{0, 0, 0}), swWrongLength},
		{"extended, an Lc of 65535 and no data is a case 2E with an Le of 65535: a tag list that is not there", []byte{0, insGetData, 0x3F, 0xFF, 0, 0xFF, 0xFF}, swWrongData},
		{"case 1 and a byte is a case 2S with an Le of 1: one byte of three, and 61xx", []byte{0, insGetVersion, 0, 0, 1}, 0x6102},
		{"the data of a command with no Lc: five bytes after the header", []byte{0, insGetVersion, 0, 0, 5, 1, 2, 3, 4, 5}, swWrongLength},
		{"the data of a command with no Lc: five bytes of data", []byte{0, insGetVersion, 0, 0, 5, 1, 2, 3, 4, 5, 0}, swWrongLength},
	} {
		c := newSelected(t, Options{})
		c.PutObject(ObjectSlot9A, []byte{0x53, 0x01, 0xAB})
		data, sw := send(t, c, tc.apdu)
		wantSW(t, tc.name, sw, tc.want)
		if tc.want>>8 != 0x61 && tc.want != swOK && len(data) != 0 {
			t.Errorf("%s: data %x", tc.name, data)
		}
		last := c.Log()[1]
		if _, ok := qaParse(tc.apdu); last.Malformed == ok {
			t.Errorf("%s: entry %+v", tc.name, last)
		}
		if last.Malformed && last.DataLen != 0 {
			t.Errorf("%s: a malformed command has no data in the log, got %d", tc.name, last.DataLen)
		}
	}
	// A very long APDU of a size that fits no case, up to a megabyte.
	c := newSelected(t, Options{})
	for _, n := range []int{255, 256, 257, 65535, 65536, 65540, 70000, 1 << 20} {
		for _, form := range []string{"short Lc", "extended Lc", "zero"} {
			b := make([]byte, n)
			copy(b, []byte{0, insVerify, 0, refPIN})
			switch form {
			case "short Lc":
				b[4] = 0xFF
			case "extended Lc":
				b[4], b[5], b[6] = 0, 0xFF, 0xFF
			}
			_, sw := send(t, c, b)
			// Whether the size fits a case (a VERIFY of 255 or 65535 bytes of data) or fits none, the answer is 6700 and no try is spent.
			if sw != swWrongLength {
				t.Errorf("%d bytes, %s: SW %04X", n, form, sw)
			}
		}
	}
	if c.st().pin.left != 3 {
		t.Errorf("a malformed VERIFY spent a try: %d left", c.st().pin.left)
	}
}

func TestQaTheLargestCommandsThatFitACase(t *testing.T) {
	c := newSelected(t, Options{})
	for _, n := range []int{1, 255, 256, 65535} {
		data := make([]byte, n)
		var apdu []byte
		if n <= 255 {
			apdu = short(0, insGetVersion, 0, 0, data, -1)
		} else {
			apdu = extended(0, insGetVersion, 0, 0, data, -1)
		}
		wantSW(t, fmt.Sprintf("GET VERSION with %d bytes of data", n), qaSWOf(t, c, apdu), swWrongLength)
		log := c.Log()
		if e := log[len(log)-1]; e.Malformed || e.DataLen != n || e.SW != swWrongLength {
			t.Errorf("%d bytes: entry %+v", n, e)
		}
	}
	wantSW(t, "a short command of 255 bytes with Le", qaSWOf(t, c, short(0, insGetVersion, 0, 0, make([]byte, 255), 0)), swWrongLength)
	if e := c.Log()[len(c.Log())-1]; e.Malformed || e.DataLen != 255 {
		t.Errorf("entry %+v", e)
	}
}

// A chained command whose parts are extended, mixed with short ones, and with Le.
func TestQaAChainOfExtendedPartsRunsWhole(t *testing.T) {
	obj := qaPattern(1200)
	for _, mode := range []ResponseMode{Extended, Short61xx} {
		c := newSelected(t, Options{ResponseMode: mode})
		c.PutObject(ObjectSlot9A, obj)
		parts := [][]byte{
			extended(claChain, insGetData, 0x3F, 0xFF, []byte{0x5C}, -1),
			extended(claChain, insGetData, 0x3F, 0xFF, []byte{0x03}, 0),
			short(claChain, insGetData, 0x3F, 0xFF, []byte{0x5F, 0xC1}, 0),
			short(claChain, insGetData, 0x3F, 0xFF, nil, -1),
			extended(claChain, insGetData, 0x3F, 0xFF, nil, -1),
		}
		for i, p := range parts {
			data, sw := send(t, c, p)
			if sw != swOK || len(data) != 0 {
				t.Fatalf("mode %d part %d %x: %x %04X", mode, i, p, data, sw)
			}
		}
		// The last part is extended, with an Le of 0000: the whole object in the mode Extended, 256 bytes in the other.
		data, sw := send(t, c, extended(claPlain, insGetData, 0x3F, 0xFF, []byte{0x05}, 0))
		wd, wsw := qaCut(obj, map[ResponseMode]int{Extended: 65536, Short61xx: 256}[mode])
		if sw != wsw || !bytes.Equal(data, wd) {
			t.Errorf("mode %d: %d bytes %04X, want %d bytes %04X", mode, len(data), sw, len(wd), wsw)
		}
	}
	t.Run("a PIN and a new PIN in extended parts", func(t *testing.T) {
		c := newSelected(t, Options{})
		whole := join(pad(DefaultPIN), pad("654321"))
		wantSW(t, "part", qaSWOf(t, c, extended(claChain, insChange, 0, refPIN, whole[:5], -1)), swOK)
		wantSW(t, "part", qaSWOf(t, c, extended(claChain, insChange, 0, refPIN, whole[5:11], -1)), swOK)
		wantSW(t, "last", qaSWOf(t, c, extended(claPlain, insChange, 0, refPIN, whole[11:], -1)), swOK)
		wantSW(t, "the new PIN", qaSWOf(t, c, verifyCmd("654321")), swOK)
	})
	t.Run("the log has the length of each part, not of the whole", func(t *testing.T) {
		c := newSelected(t, Options{})
		whole := join(pad(DefaultPIN), pad("654321"))
		send(t, c, extended(claChain, insChange, 0, refPIN, whole[:5], -1))
		send(t, c, short(claChain, insChange, 0, refPIN, whole[5:11], -1))
		send(t, c, short(claPlain, insChange, 0, refPIN, whole[11:], -1))
		log := c.Log()
		for i, want := range []int{5, 6, 5} {
			if log[i+1].DataLen != want {
				t.Errorf("entry %d: DataLen %d, want %d", i+1, log[i+1].DataLen, want)
			}
		}
	})
}

// A chain that a power cycle, a SELECT or a GET RESPONSE interrupts, and a response that a chain interrupts.
func TestQaWhatInterruptsAChainAndWhatAChainInterrupts(t *testing.T) {
	pin := pad(DefaultPIN)
	first := short(claChain, insVerify, 0, refPIN, pin[:3], -1)
	last := short(claPlain, insVerify, 0, refPIN, pin[3:], -1)
	wholeChain := func(t *testing.T, c *Card) {
		t.Helper()
		wantSW(t, "the part", qaSWOf(t, c, first), swOK)
		wantSW(t, "the last part", qaSWOf(t, c, last), swOK)
	}
	t.Run("the chain on its own works", func(t *testing.T) {
		wholeChain(t, newSelected(t, Options{}))
	})
	t.Run("a power cycle", func(t *testing.T) {
		c := newSelected(t, Options{})
		wantSW(t, "the part", qaSWOf(t, c, first), swOK)
		c.PowerOff()
		c.PowerOn()
		selectPIV(t, c)
		wantSW(t, "the last part alone", qaSWOf(t, c, last), swWrongLength)
		if c.st().pin.left != 3 {
			t.Error("a try was spent")
		}
		wholeChain(t, c)
	})
	t.Run("a chain of SELECT, which a card that is not selected still takes, across a power cycle", func(t *testing.T) {
		for _, cycle := range []func(*Card){func(c *Card) { c.PowerOff(); c.PowerOn() }, func(c *Card) { c.PowerOn() }} {
			c := New(Options{})
			c.PowerOn()
			wantSW(t, "the part", qaSWOf(t, c, short(claChain, insSelect, 4, 0, aidFull[:3], -1)), swOK)
			cycle(c)
			// the rest of the AID alone is not an AID; joined with the part before the cycle it would be
			wantSW(t, "the last part alone", qaSWOf(t, c, short(claPlain, insSelect, 4, 0, aidFull[3:], 0)), swNotFound)
			wantSW(t, "not selected", qaSWOf(t, c, []byte{0, insGetVersion, 0, 0}), swNotAllowed)
		}
	})
	t.Run("a command before SELECT drops a chain of SELECT", func(t *testing.T) {
		c := New(Options{})
		c.PowerOn()
		wantSW(t, "the part", qaSWOf(t, c, short(claChain, insSelect, 4, 0, aidFull[:3], -1)), swOK)
		wantSW(t, "GET VERSION before SELECT", qaSWOf(t, c, []byte{0, insGetVersion, 0, 0}), swNotAllowed)
		wantSW(t, "the last part alone", qaSWOf(t, c, short(claPlain, insSelect, 4, 0, aidFull[3:], 0)), swNotFound)
		wantSW(t, "a part of another command before SELECT", qaSWOf(t, c, short(claChain, insGetVersion, 0, 0, nil, -1)), swNotAllowed)
	})
	t.Run("PowerOn on a card that is on", func(t *testing.T) {
		c := newSelected(t, Options{})
		qaSWOf(t, c, first)
		c.PowerOn()
		selectPIV(t, c)
		wantSW(t, "the last part alone", qaSWOf(t, c, last), swWrongLength)
	})
	t.Run("a SELECT", func(t *testing.T) {
		c := newSelected(t, Options{})
		wantSW(t, "the part", qaSWOf(t, c, first), swOK)
		selectPIV(t, c)
		wantSW(t, "the last part alone", qaSWOf(t, c, last), swWrongLength)
		if c.st().pin.left != 3 {
			t.Error("a try was spent")
		}
	})
	t.Run("a SELECT that fails", func(t *testing.T) {
		c := newSelected(t, Options{})
		qaSWOf(t, c, first)
		wantSW(t, "SELECT of another AID", qaSWOf(t, c, short(0, insSelect, 4, 0, []byte{1, 2, 3, 4, 5}, 0)), swNotFound)
		wantSW(t, "the last part alone", qaSWOf(t, c, last), swWrongLength)
	})
	t.Run("a GET RESPONSE", func(t *testing.T) {
		c := newSelected(t, Options{})
		qaSWOf(t, c, first)
		wantSW(t, "GET RESPONSE", qaSWOf(t, c, getResponseCmd(0)), swWrongP1P2)
		wantSW(t, "the last part alone", qaSWOf(t, c, last), swWrongLength)
	})
	t.Run("another command", func(t *testing.T) {
		c := newSelected(t, Options{})
		qaSWOf(t, c, first)
		wantSW(t, "GET VERSION", qaSWOf(t, c, []byte{0, insGetVersion, 0, 0}), swOK)
		wantSW(t, "the last part alone", qaSWOf(t, c, last), swWrongLength)
	})
	t.Run("another chain", func(t *testing.T) {
		c := newSelected(t, Options{})
		c.PutObject(ObjectSlot9A, []byte{0x53, 0x01, 0xAB})
		qaSWOf(t, c, first)
		other := short(claChain, insGetData, 0x3F, 0xFF, []byte{0x5C, 0x03}, -1)
		wantSW(t, "a part of another command", qaSWOf(t, c, other), swWrongLength) // the first chain is dropped
		wantSW(t, "the same part again opens a new chain", qaSWOf(t, c, other), swOK)
		data, sw := send(t, c, short(claPlain, insGetData, 0x3F, 0xFF, []byte{0x5F, 0xC1, 0x05}, 0))
		if sw != swOK || !bytes.Equal(data, []byte{0x53, 0x01, 0xAB}) {
			t.Errorf("the second chain: %x %04X", data, sw)
		}
	})
	t.Run("a response that waits is dropped by a chain, and the chain goes on", func(t *testing.T) {
		c := newSelected(t, Options{})
		c.PutObject(ObjectSlot9A, bigObject(600))
		wantSW(t, "GET DATA", qaSWOf(t, c, getDataCmd(ObjectSlot9A, 0)), swMore)
		wantSW(t, "the part", qaSWOf(t, c, first), swOK)
		wantSW(t, "GET RESPONSE is a command other than the chain's: it drops the chain, and finds nothing waiting", qaSWOf(t, c, getResponseCmd(0)), swWrongP1P2)
		wantSW(t, "the last part alone", qaSWOf(t, c, last), swWrongLength)
		wantSW(t, "GET DATA again", qaSWOf(t, c, getDataCmd(ObjectSlot9A, 0)), swMore)
		wantSW(t, "the part", qaSWOf(t, c, first), swOK)
		wantSW(t, "the last part", qaSWOf(t, c, last), swOK)
		wantSW(t, "GET RESPONSE after the chain", qaSWOf(t, c, getResponseCmd(0)), swWrongP1P2)
	})
}

// qaDrain reads a response to its end with GET RESPONSE and checks every part against the rule of the doc.
func qaDrain(t *testing.T, name string, c *Card, first []byte, obj []byte, firstLimit int, next func() ([]byte, int)) {
	t.Helper()
	data, sw := send(t, c, first)
	wd, wsw := qaCut(obj, firstLimit)
	if sw != wsw || !bytes.Equal(data, wd) {
		t.Fatalf("%s: first part %d bytes %04X, want %d bytes %04X", name, len(data), sw, len(wd), wsw)
	}
	got := append([]byte(nil), data...)
	for steps := 0; sw != swOK; steps++ {
		if steps > 300 {
			t.Fatalf("%s: the response does not end", name)
		}
		apdu, limit := next()
		data, sw = send(t, c, apdu)
		wd, wsw = qaCut(obj[len(got):], limit)
		if sw != wsw || !bytes.Equal(data, wd) {
			t.Fatalf("%s: part at %d: %d bytes %04X, want %d bytes %04X", name, len(got), len(data), sw, len(wd), wsw)
		}
		got = append(got, data...)
	}
	if !bytes.Equal(got, obj) {
		t.Errorf("%s: the parts do not join to the object", name)
	}
	apdu, _ := next()
	wantSW(t, name+": GET RESPONSE after the last part", qaSWOf(t, c, apdu), swWrongP1P2)
}

// Responses of 255, 256, 257, 512, 65535, 65536 and 65537 bytes, in both modes, asked for in the short form and in the extended one, and read
// to the end with GET RESPONSE in the short form and in the extended one.
func TestQaResponsesAtTheSizesWhereAStatusWordChangesForm(t *testing.T) {
	id := []byte{0x5C, 0x03, 0x5F, 0xC1, 0x05}
	sizes := []int{1, 255, 256, 257, 511, 512, 513, 767, 768, 65535, 65536, 65537}
	if testing.Short() {
		sizes = []int{255, 256, 257, 512, 65537}
	}
	for _, mode := range []ResponseMode{Extended, Short61xx} {
		for _, n := range sizes {
			obj := qaPattern(n)
			for _, askExt := range []bool{false, true} {
				for _, readExt := range []bool{false, true} {
					c := newSelected(t, Options{ResponseMode: mode})
					c.PutObject(ObjectSlot9A, obj)
					first, firstLimit := short(0, insGetData, 0x3F, 0xFF, id, 0), 256
					if askExt {
						first = extended(0, insGetData, 0x3F, 0xFF, id, 0)
						if mode == Extended {
							firstLimit = 65536
						}
					}
					next := func() ([]byte, int) {
						if readExt {
							if mode == Extended {
								return extended(0, insGetResponse, 0, 0, nil, 0), 65536
							}
							return extended(0, insGetResponse, 0, 0, nil, 0), 256
						}
						return getResponseCmd(0), 256
					}
					qaDrain(t, fmt.Sprintf("mode %d, %d bytes, ask extended %v, read extended %v", mode, n, askExt, readExt), c, first, obj, firstLimit, next)
				}
			}
		}
	}
}

// 61xx: "xx bytes more", and 00 for 256 or more.
func TestQaTheStatusWord61xxCountsWhatIsLeft(t *testing.T) {
	id := []byte{0x5C, 0x03, 0x5F, 0xC1, 0x05}
	for _, tc := range []struct {
		n    int
		want uint16
	}{{256, swOK}, {257, 0x6101}, {300, 0x612C}, {511, 0x61FF}, {512, 0x6100}, {513, 0x6100}} {
		c := newSelected(t, Options{})
		c.PutObject(ObjectSlot9A, qaPattern(tc.n))
		data, sw := send(t, c, short(0, insGetData, 0x3F, 0xFF, id, 0))
		if len(data) != 256 || sw != tc.want {
			t.Errorf("%d bytes: %d bytes and %04X, want 256 and %04X", tc.n, len(data), sw, tc.want)
		}
	}
	// and a larger rest in the extended answer
	c := newSelected(t, Options{})
	c.PutObject(ObjectSlot9A, qaPattern(70000))
	data, sw := send(t, c, extended(0, insGetData, 0x3F, 0xFF, id, 0))
	if len(data) != 65536 || sw != 0x6100 {
		t.Errorf("70000 bytes, extended: %d bytes and %04X, want 65536 and 6100", len(data), sw)
	}
	rest, sw := send(t, c, extended(0, insGetResponse, 0, 0, nil, 0))
	if len(rest) != 70000-65536 || sw != swOK {
		t.Errorf("the rest: %d bytes and %04X", len(rest), sw)
	}
}

// GET RESPONSE with an Le smaller than what is left, larger, and 00.
func TestQaGetResponseWithAnLeSmallerLargerAndZero(t *testing.T) {
	obj := qaPattern(1000)
	id := []byte{0x5C, 0x03, 0x5F, 0xC1, 0x05}
	start := func(mode ResponseMode) *Card {
		c := newSelected(t, Options{ResponseMode: mode})
		c.PutObject(ObjectSlot9A, obj)
		if _, sw := send(t, c, short(0, insGetData, 0x3F, 0xFF, id, 0)); sw != swMore {
			t.Fatalf("GET DATA: %04X", sw)
		}
		return c
	}
	c := start(Extended)
	got := obj[:256] // 744 left
	step := func(apdu []byte, wantLen int, wantSW uint16) {
		t.Helper()
		data, sw := send(t, c, apdu)
		if len(data) != wantLen || sw != wantSW || !bytes.Equal(data, obj[len(got):len(got)+len(data)]) {
			t.Fatalf("GET RESPONSE %x: %d bytes and %04X, want %d and %04X", apdu, len(data), sw, wantLen, wantSW)
		}
		got = obj[:len(got)+len(data)]
	}
	step(getResponseCmd(0x10), 16, 0x6100)  // smaller: 728 left
	step(getResponseCmd(1), 1, 0x6100)      // 727
	step(getResponseCmd(0), 256, 0x6100)    // 00 is 256: 471
	step(getResponseCmd(0xFF), 255, 0x61D8) // 216 left
	step(extended(0, insGetResponse, 0, 0, nil, 0x20), 32, 0x61B8)
	step(getResponseCmd(200), 184, 0x9000) // an Le larger than what is left gives what is left
	if len(got) != len(obj) {
		t.Errorf("got %d of %d bytes", len(got), len(obj))
	}
	wantSW(t, "after the end", qaSWOf(t, c, getResponseCmd(0)), swWrongP1P2)
	// An Le equal to what is left, and the 00 of the end.
	c = start(Extended)
	got = obj[:256]
	step(getResponseCmd(0), 256, 0x6100)   // 488 left
	step(getResponseCmd(0), 256, 0x61E8)   // 232
	step(getResponseCmd(232), 232, 0x9000) // exactly what is left
	c = start(Short61xx)
	got = obj[:256]
	step(extended(0, insGetResponse, 0, 0, nil, 0), 256, 0x6100) // the extended form in the mode Short61xx is cut at 256
	step(extended(0, insGetResponse, 0, 0, nil, 400), 256, 0x61E8)
	step(extended(0, insGetResponse, 0, 0, nil, 0), 232, 0x9000)
}

// "The rest of the response": an Le of the command that cut it does not matter for GET RESPONSE; the Le of GET RESPONSE rules.
func TestQaAnAnswerCutByAnSmallLeCanBeReadInTinyParts(t *testing.T) {
	obj := qaPattern(40)
	id := []byte{0x5C, 0x03, 0x5F, 0xC1, 0x05}
	c := newSelected(t, Options{})
	c.PutObject(ObjectSlot9A, obj)
	data, sw := send(t, c, short(0, insGetData, 0x3F, 0xFF, id, 7))
	wantSW(t, "Le 7", sw, 0x6100|33)
	got := data
	for i := 0; sw != swOK; i++ {
		if i > 10 {
			t.Fatalf("the response does not end: %x", got)
		}
		data, sw = send(t, c, getResponseCmd(7))
		got = append(got, data...)
	}
	if !bytes.Equal(got, obj) {
		t.Errorf("parts of 7 do not join: %x", got)
	}
}
