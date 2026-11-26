package ccid

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// D. Chaining at extended APDU level, and the refusal of a long command at short APDU level.

// cut splits data into blocks of at most size bytes.
func cut(data []byte, size int) [][]byte {
	var out [][]byte
	for len(data) > size {
		out = append(out, data[:size])
		data = data[size:]
	}
	return append(out, data)
}

// A command of every size around the limits of a block goes out in the right blocks, and the device gets exactly the bytes.
func TestQAChainCommandSizes(t *testing.T) {
	for _, maxMsg := range []int{271, 512, 1000, 65535, 65536} {
		room := min(maxMsg, MaxMessageLength) - 10
		sizes := []int{4, 5, 7, room - 1, room, room + 1, 2*room - 1, 2 * room, 2*room + 1, 3*room + 5, MaxCommandAPDU - 1, MaxCommandAPDU}
		seen := map[int]bool{}
		for _, n := range sizes {
			if n < 4 || n > MaxCommandAPDU || seen[n] {
				continue
			}
			seen[n] = true
			t.Run(fmt.Sprintf("message %d, command %d", maxMsg, n), func(t *testing.T) {
				cc := &chainCard{respParts: [][]byte{{0x90, 0x00}}}
				f := newFake(descSpec{maxMsg: uint32(maxMsg), mpsIn: 512, mpsOut: 512}, cc.handler()) //nolint:gosec // at most 65536
				r, err := Open(f, Options{})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := r.PowerOn(testBudget); err != nil {
					t.Fatal(err)
				}
				apdu := pattern(n, n)
				outs := len(f.outs)
				if got, err := r.Transmit(apdu, testBudget); err != nil || !bytes.Equal(got, []byte{0x90, 0}) {
					t.Fatalf("%x %v", got, err)
				}
				if !bytes.Equal(cc.got, apdu) {
					t.Fatal("the device did not get the command")
				}
				blocks := (n + room - 1) / room
				if len(f.outs)-outs != blocks {
					t.Fatalf("%d messages for %d blocks", len(f.outs)-outs, blocks)
				}
				for i, m := range f.outs[outs:] {
					wantLen := room
					if i == blocks-1 {
						wantLen = n - room*(blocks-1)
					}
					lvl := uint16(lvlMiddle)
					switch {
					case blocks == 1:
						lvl = lvlWhole
					case i == 0:
						lvl = lvlBegin
					case i == blocks-1:
						lvl = lvlEnd
					}
					if len(m) != 10+wantLen || le32(m[1:5]) != uint32(wantLen) || uint16(m[8])|uint16(m[9])<<8 != lvl { //nolint:gosec // small
						t.Errorf("block %d: %d bytes, dwLength %d, level %#x; want %d bytes and level %#x", i, len(m), le32(m[1:5]), uint16(m[8])|uint16(m[9])<<8, wantLen, lvl)
					}
					if len(m) > maxMsg {
						t.Errorf("block %d is %d bytes, over the message length %d", i, len(m), maxMsg)
					}
				}
			})
		}
	}
}

// A response of every size around the limits: one block, the largest block, one byte more, many blocks, the largest response, and one
// byte over it (which ends the session).
func TestQAChainResponseSizes(t *testing.T) {
	for _, maxMsg := range []int{271, 65536} {
		room := maxMsg - 10
		for _, n := range []int{2, 3, room - 1, room, room + 1, 2 * room, 2*room + 1, 10 * room, MaxResponseAPDU - 1, MaxResponseAPDU, MaxResponseAPDU + 1} {
			t.Run(fmt.Sprintf("message %d, response %d", maxMsg, n), func(t *testing.T) {
				want := pattern(n, 3)
				blocks := cut(want, room)
				cc := &chainCard{respParts: blocks}
				f := newFake(descSpec{maxMsg: uint32(maxMsg), mpsIn: 512, mpsOut: 512}, cc.handler()) //nolint:gosec // at most 65536
				r, err := Open(f, Options{})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := r.PowerOn(testBudget); err != nil {
					t.Fatal(err)
				}
				outs := len(f.outs)
				got, err := r.Transmit(apduSelect, testBudget)
				if n > MaxResponseAPDU {
					lostWith(t, err, ErrResponseTooLong)
					if got != nil {
						t.Error("a response with the error")
					}
					untouched(t, r, f, err)
					assertWiped(t, r, "after the response that was too long")
					return
				}
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("got %d bytes, want %d, err %v", len(got), n, err)
				}
				if sent := len(f.outs) - outs; sent != len(blocks) {
					t.Errorf("%d messages for a response of %d blocks (the command and the continuations)", sent, len(blocks))
				}
				for i, m := range f.outs[outs+1:] {
					if len(m) != 10 || m[8] != 0x10 || m[9] != 0 {
						t.Errorf("continuation %d: % x", i, m[:min(len(m), 10)])
					}
				}
			})
		}
	}
}

// The bChainParameter of each answer, in each place of a response: the values that make a response (whole, or begin, middle, end) and
// the ones that do not.
func TestQAChainParameterValues(t *testing.T) {
	const (
		whole, begin, end, middle, ack = chainWhole, chainBegin, chainEnd, chainMiddle, chainEmptyAck
	)
	type chainCase struct {
		name   string
		params []byte
		ok     bool
	}
	cases := []chainCase{
		{"whole", []byte{whole}, true},
		{"begin, end", []byte{begin, end}, true},
		{"begin, middle, end", []byte{begin, middle, end}, true},
		{"begin, four middles, end", []byte{begin, middle, middle, middle, middle, end}, true},
		{"an acknowledgement as the whole response", []byte{ack}, false},
		{"0x04", []byte{0x04}, false},
		{"0x11", []byte{0x11}, false},
		{"0x80", []byte{0x80}, false},
		{"0xFF", []byte{0xFF}, false},
		{"begin, then an acknowledgement", []byte{begin, ack}, false},
		{"begin, then 0x04", []byte{begin, 0x04}, false},
		{"begin, middle, then 0xFF", []byte{begin, middle, 0xFF}, false},
		{"begin, then whole", []byte{begin, whole}, false},
		{"begin, then begin, end", []byte{begin, begin, end}, false},
		{"end as the first block", []byte{end}, false},
		{"middle as the first block, then end", []byte{middle, end}, false},
		{"middle, middle, whole", []byte{middle, middle, whole}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			i := 0
			var want []byte
			r, f := openPowered(t, Options{}, func(f *fakeDevice, m []byte) {
				if m[0] != msgXfrBlock {
					cardHandler(echoSW)(f, m)
					return
				}
				if i >= len(c.params) {
					t.Errorf("a block was asked for after the last one (level %#x)", m[8])
					return
				}
				data := []byte{byte(i & 0xFF), byte(i & 0xFF), 0x90}
				want = append(want, data...)
				f.send(msg(rspDataBlock, m[6], stOK, 0, c.params[i], data))
				i++
			})
			got, err := r.Transmit(apduSelect, testBudget)
			if c.ok {
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("got %x, want %x, err %v", got, want, err)
				}
				return
			}
			lostWith(t, err, ErrProtocol)
			if got != nil {
				t.Error("a response with the error")
			}
			untouched(t, r, f, err)
		})
	}
}

// The acknowledgement of each block of a command that is not the last asks for the next one with the parameter 0x10 and no data; any
// other answer ends the session.
func TestQAChainAcknowledgementValues(t *testing.T) {
	for _, c := range []struct {
		name  string
		chain byte
		data  []byte
		ok    bool
	}{
		{"0x10, empty", chainEmptyAck, nil, true},
		{"0x10 with data", chainEmptyAck, []byte{1}, false},
		{"0x00", chainWhole, nil, false},
		{"0x01", chainBegin, nil, false},
		{"0x02", chainEnd, nil, false},
		{"0x03", chainMiddle, nil, false},
		{"0x11", 0x11, nil, false},
		{"0xFF", 0xFF, nil, false},
		{"0x00 with data", chainWhole, []byte{0x90, 0}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dev := &chainDevice{respBlocks: [][]byte{{0x90, 0x00}}}
			dev.onBlock = func(f *fakeDevice, m []byte, lvl uint16, _ int) bool {
				if lvl == lvlBegin || lvl == lvlMiddle {
					f.send(msg(rspDataBlock, m[6], stOK, 0, c.chain, c.data))
					return true
				}
				return false
			}
			r, f := openPowered(t, Options{}, dev.handler())
			got, err := r.Transmit(qaLong, testBudget)
			if c.ok {
				if err != nil || !bytes.Equal(got, []byte{0x90, 0}) {
					t.Fatalf("%x %v", got, err)
				}
				return
			}
			lostWith(t, err, ErrProtocol)
			untouched(t, r, f, err)
		})
	}
}

// The last block of a command is answered by the response (whole or begin), and by nothing that asks for more of the command.
func TestQAChainLastBlockAnsweredWithAnAcknowledgement(t *testing.T) {
	dev := &chainDevice{respBlocks: [][]byte{{0x90, 0x00}}}
	dev.onBlock = func(f *fakeDevice, m []byte, lvl uint16, _ int) bool {
		if lvl == lvlEnd {
			f.send(msg(rspDataBlock, m[6], stOK, 0, chainEmptyAck, nil))
			return true
		}
		return false
	}
	r, f := openPowered(t, Options{}, dev.handler())
	_, err := r.Transmit(qaLong, testBudget)
	lostWith(t, err, ErrProtocol)
	untouched(t, r, f, err)
}

// At short APDU level a command over the short limit is refused before anything is sent, whatever message length the reader declares,
// and a command of the limit goes out as one message with level 0.
func TestQAShortLevelRefusesALongCommand(t *testing.T) {
	for _, maxMsg := range []uint32{271, 1000, 65536, 1 << 20} {
		t.Run(fmt.Sprint(maxMsg), func(t *testing.T) {
			f := newFake(descSpec{features: featuresShort, maxMsg: maxMsg, mpsIn: 512, mpsOut: 512}, cardHandler(func([]byte) []byte { return []byte{0x90, 0} }))
			r, err := Open(f, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.PowerOn(testBudget); err != nil {
				t.Fatal(err)
			}
			if info := r.Info(); info.ExtendedAPDU || info.MaxCommandAPDU != MaxShortCommandAPDU {
				t.Errorf("info = %+v", info)
			}
			outs, mark := len(f.outs), f.mark()
			for _, n := range []int{MaxShortCommandAPDU + 1, MaxShortCommandAPDU + 2, 1000, 65535, MaxCommandAPDU, MaxCommandAPDU + 1} {
				_, err := r.Transmit(make([]byte, n), testBudget)
				if !errors.Is(err, ErrAPDUTooLong) || errors.Is(err, ErrSessionLost) || !strings.Contains(err.Error(), fmt.Sprint(MaxShortCommandAPDU)) {
					t.Errorf("%d bytes: %v", n, err)
				}
			}
			if len(f.outs) != outs || len(f.callsAfter(mark)) != 0 {
				t.Errorf("a refused command reached the port: %v", f.callsAfter(mark))
			}
			big := pattern(MaxShortCommandAPDU, 1)
			if got, err := r.Transmit(big, testBudget); err != nil || !bytes.Equal(got, []byte{0x90, 0}) {
				t.Fatalf("the largest short command: %v", err)
			}
			m := f.outs[len(f.outs)-1]
			if len(m) != 10+MaxShortCommandAPDU || m[8] != 0 || m[9] != 0 {
				t.Errorf("the largest short command went out as % x ...", m[:10])
			}
		})
	}
}

// A reader at short APDU level has no chains, in either direction: a block that says "begin" is not for it.
func TestQAShortLevelHasNoResponseChain(t *testing.T) {
	continued := false
	f := newFake(descSpec{features: featuresShort}, func(f *fakeDevice, m []byte) {
		if m[0] == msgXfrBlock {
			if m[8] == 0x10 {
				continued = true
			}
			f.send(msg(rspDataBlock, m[6], stOK, 0, chainBegin, []byte{1, 2}))
			return
		}
		cardHandler(echoSW)(f, m)
	})
	r, err := Open(f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.PowerOn(testBudget); err != nil {
		t.Fatal(err)
	}
	_, err = r.Transmit(apduSelect, testBudget)
	if continued {
		t.Error("a continuation block was sent to a reader at short APDU level")
	}
	lostWith(t, err, ErrProtocol)
}

// Chaining with the sizes of the buffers: a response that grows block by block is exact, and the arrays it leaves behind are clear (the
// reader's own buffers hold nothing after the call).
func TestQAChainLeavesNothingInTheBuffers(t *testing.T) {
	blocks := cut(pattern(5000, 9), 261)
	cc := &chainCard{respParts: blocks}
	r, _ := openPowered(t, Options{}, cc.handler())
	got, err := r.Transmit(append(append([]byte(nil), qaCmd...), pattern(2000, 1)...), testBudget)
	if err != nil || !bytes.Equal(got, pattern(5000, 9)) {
		t.Fatalf("%d bytes, %v", len(got), err)
	}
	assertWiped(t, r, "after a chained exchange")
}
