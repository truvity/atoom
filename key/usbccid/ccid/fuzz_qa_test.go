package ccid

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/truvity/atoom/key/usbccid"
)

// K. Fuzzing. The raw target of fuzz_test.go (FuzzAnswers) feeds arbitrary bytes. These two add what it cannot say:
//
//   - FuzzQAAnswersModel builds the answers of a device from the fuzzer's bytes as a list of frames of known kinds, so that the property
//     can be exact: a response that the reader returns is the concatenation of the payloads of the first right-numbered answer of each
//     message it sent, and never a byte of a stale frame, a frame of another slot, or a time extension;
//   - FuzzQADescriptors holds the descriptor parser, Open and the layout together.

const staleMarker = 0xEE

// frame kinds of the model
const (
	fkAnswer = iota // an answer to this message: the right number, type and slot
	fkTimeExt
	fkStale
	fkFailed
	fkCardBits
	fkOtherSlot
	fkOtherType
	fkEmpty
	fkKinds
)

type modelFrame struct {
	kind    byte
	payload []byte
	chain   byte
	status  byte
	code    byte
}

// decodeScript turns the fuzzer's bytes into the replies to the first messages of a Transmit: each reply is up to three frames.
func decodeScript(data []byte) [][]modelFrame {
	var script [][]modelFrame
	next := func() byte {
		if len(data) == 0 {
			return 0
		}
		b := data[0]
		data = data[1:]
		return b
	}
	for len(data) > 0 && len(script) < 10 {
		var reply []modelFrame
		for range 1 + int(next())%3 {
			if len(data) == 0 {
				break
			}
			fr := modelFrame{kind: next() % fkKinds}
			n := int(next()) % 90
			if next()%16 == 0 {
				n = 200 + int(next())%70 // now and then a payload of the size of a whole message
			}
			fr.payload = make([]byte, n)
			for i := range fr.payload {
				fr.payload[i] = next()
				if fr.payload[i] == staleMarker {
					fr.payload[i]--
				}
			}
			fr.chain = []byte{chainWhole, chainBegin, chainEnd, chainMiddle, chainEmptyAck, 0x55}[next()%6]
			fr.status = next() % 4
			fr.code = next()
			reply = append(reply, fr)
		}
		script = append(script, reply)
	}
	return script
}

// firstAnswer returns the payload of the first frame of a reply that the reader must take for the answer: an answer frame with an
// accepted status, in front of nothing that ends the session. The model knows only the plain case; ok is false when the reply holds
// something else in front of it.
func firstAnswer(reply []modelFrame) (payload []byte, ok bool) {
	for _, fr := range reply {
		switch fr.kind {
		case fkTimeExt, fkStale, fkEmpty: // an empty packet in front of the first message is allowed after the drain, or the session is lost
			continue
		case fkAnswer:
			return fr.payload, true
		case fkCardBits:
			if fr.status == 0 { // a card that is active: an answer like any other
				return fr.payload, true
			}
			return nil, false
		default:
			return nil, false
		}
	}
	return nil, false
}

func FuzzQAAnswersModel(f *testing.F) {
	ok := []byte{1, fkAnswer, 2, 1, 0x90, 0x00, 0, 0, 0}
	f.Add(ok, uint8(0), false)
	f.Add(append(append([]byte(nil), ok...), 1, fkAnswer, 3, 1, 1, 2, 3, 0, 0, 0), uint8(0), true)
	f.Add([]byte{3, fkStale, 4, 1, 1, 2, 3, 4, 0, 0, 0, fkTimeExt, 0, 1, 0, 0, 0, fkAnswer, 2, 1, 0x90, 0, 0, 0, 0}, uint8(1), false)
	f.Add([]byte{1, fkAnswer, 200, 0, 1, 2, 3, 4, 0, 1, 0, 0}, uint8(2), true)
	f.Add([]byte{1, fkFailed, 0, 1, 0, 0, 0, 0xFE}, uint8(0), false)
	f.Add([]byte{1, fkEmpty, 0, 1, 0, 0, 0, 0, 1, fkAnswer, 2, 1, 0x90, 0, 0, 0, 0}, uint8(3), false)
	// Found by the fuzzer: an empty packet in front of a long answer, which the first read after the drain allows.
	f.Add([]byte("22\xf1100000000000000000000000000000000000000000000000000000000000000007c10000000000000"), uint8(0), true)
	f.Fuzz(func(t *testing.T, data []byte, mpsSel uint8, noZLP bool) {
		script := decodeScript(data)
		mps := []int{8, 16, 64, 512}[mpsSel%4]
		msgs := 0
		var firsts [][]byte
		h := func(f *fakeDevice, m []byte) {
			if m[0] != msgXfrBlock {
				cardHandler(echoSW)(f, m)
				return
			}
			f.noZLP = noZLP
			j := msgs
			msgs++
			if j >= len(script) {
				return // no more replies: the device is silent
			}
			seq := m[6]
			p, _ := firstAnswer(script[j])
			firsts = append(firsts, p)
			for _, fr := range script[j] {
				switch fr.kind {
				case fkAnswer:
					f.send(msg(rspDataBlock, seq, stOK, 0, fr.chain, fr.payload))
				case fkTimeExt:
					f.send(msg(rspDataBlock, seq, stTimeExt|fr.status, fr.code, 0, fr.payload))
				case fkStale:
					f.send(msg(rspDataBlock, seq-1-fr.code%4, stOK, 0, 0, bytes.Repeat([]byte{staleMarker}, len(fr.payload)+1)))
				case fkFailed:
					f.send(msg(rspDataBlock, seq, stFailed|fr.status&1, fr.code, 0, fr.payload))
				case fkCardBits:
					f.send(msg(rspDataBlock, seq, stOK|fr.status, 0, fr.chain, fr.payload))
				case fkOtherSlot:
					x := msg(rspDataBlock, seq, stOK, 0, fr.chain, fr.payload)
					x[5] = 1 + fr.code%3
					f.send(x)
				case fkOtherType:
					f.send(msg(rspSlotStatus, seq, stOK, 0, fr.chain, fr.payload))
				case fkEmpty:
					f.sendRaw([]byte{})
				}
			}
		}
		fk := newFake(descSpec{mpsIn: uint16(mps), mpsOut: uint16(mps), maxMsg: 300}, h) //nolint:gosec // small
		r, err := Open(fk, Options{TransferTimeout: 20 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.PowerOn(testBudget); err != nil {
			t.Fatal(err)
		}
		resp, err := r.Transmit(apduSelect, 2*time.Second) // the fake never waits: only a stall of seconds can spend this
		assertWiped(t, r, "after the call")
		if err == nil {
			if len(resp) < 2 || len(resp) > MaxResponseAPDU {
				t.Fatalf("a response of %d bytes", len(resp))
			}
			// The response is the payloads of the first answers of the first k messages, for some k.
			var acc []byte
			matched := false
			for _, p := range firsts {
				acc = append(acc, p...)
				if bytes.Equal(acc, resp) {
					matched = true
					break
				}
			}
			if !matched {
				t.Fatalf("response %x is not the payload of the answers to the messages sent (%d messages)", resp, msgs)
			}
			if bytes.IndexByte(resp, staleMarker) >= 0 {
				t.Fatal("a byte of a stale frame is in the response")
			}
			return
		}
		if resp != nil {
			t.Fatal("a response with an error")
		}
		var ce *CommandError
		lost := errors.Is(err, ErrSessionLost)
		if !lost && !errors.As(err, &ce) && !errors.Is(err, ErrNotPowered) {
			t.Fatalf("an error that is neither a lost session, a card-level failure nor a card that is off: %v", err)
		}
		assertNoLeak(t, "fuzz", err, qaResp)
		if lost {
			mark := fk.mark()
			for _, e := range []error{
				func() error { _, e := r.Transmit(apduSelect, testBudget); return e }(),
				func() error { _, e := r.SlotStatus(testBudget); return e }(),
				r.PowerOff(testBudget),
			} {
				if !errors.Is(e, err) {
					t.Fatalf("a later call: %v, want %v", e, err)
				}
			}
			if len(fk.callsAfter(mark)) != 0 {
				t.Fatal("the port was used after the loss")
			}
		}
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

// scanEndpoint reports whether the configuration of b holds an endpoint descriptor (of any length from 7) with this address and these
// attributes, found by walking the descriptors by their lengths as the USB layer does.
func scanEndpoint(b []byte, addr, attr byte) bool {
	if len(b) < 18+9 {
		return false
	}
	for p := b[18+b[18]:]; len(p) >= 2 && int(p[0]) >= 2 && int(p[0]) <= len(p); p = p[p[0]:] {
		if p[1] == descEndpoint && p[0] >= 7 && p[2] == addr && p[3]&3 == attr {
			return true
		}
	}
	return false
}

func FuzzQADescriptors(f *testing.F) {
	f.Add(descSpec{}.build())
	f.Add(descSpec{second: true}.build())
	f.Add(descSpec{features: featuresShort, mpsIn: 512, mpsOut: 512, maxMsg: 0xFFFFFFFF}.build())
	f.Add(descSpec{ccidFirst: true, noIntr: true}.build())
	f.Add(descSpec{twoClass: true}.build())
	f.Add(descSpec{altFirst: true}.build())
	f.Add(descSpec{classLen: 55}.build())
	// Found by the fuzzer: an endpoint descriptor longer than 7 bytes.
	f.Add([]byte("\x12\x01000000000000000\x01\t\x02\xc5\x00\x030000\t\x040\x0000000\t00000000\a000000\t\x041\x0000000\t00000000\a000000\t\x042\x00\x03\v\x00\x0006\x05\x022000000000000000000000000000000000000000000000000006!0\x01\x000100000000000000000000000000000000020200000000000\a\x05\x830000\a000000\a\x05\x822000"))
	// Found by the fuzzer: a decoy interface descriptor inside another descriptor.
	f.Add([]byte("\x12\x01000000000000000\x01\t\x02\xe3\x00\x040000\t\x040\x0000000\t00000000\a000000\t\x041\x0000000\t00000000\a000000\t\x042\x0000000600000000000000\t\x04700\v0006!0000000000000000000000000000\a000000\a000000\a000000\t\x047\x00\x03\v\x00\x0006!0\x01\x000100000000000000000000000000000000020200000000000\a\x05\x870000\a\x05\x062000\a\x05\x862000"))
	f.Fuzz(func(t *testing.T, b []byte) {
		l, perr := parseDescriptors(b, nil)
		l2, perr2 := parseDescriptors(b, nil)
		if (perr == nil) != (perr2 == nil) || perr == nil && *l != *l2 {
			t.Fatal("the parser is not deterministic")
		}
		fk := newFake(descSpec{}, nil)
		fk.desc = append([]byte(nil), b...)
		r, err := Open(fk, Options{})
		if (err == nil) != (perr == nil) {
			t.Fatalf("Open says %v, the parser says %v", err, perr)
		}
		if err != nil {
			if got := fmt.Sprint(fk.calls); got != "[descriptors close]" {
				t.Fatalf("a refused Open made the calls %s", got)
			}
			if err.Error() == "" || !isNamed(err) {
				t.Fatalf("a refusal with no name: %v", err)
			}
			return
		}
		if got := fmt.Sprint(fk.calls); got != "[descriptors claim]" {
			t.Fatalf("Open made the calls %s", got)
		}
		// What is accepted is what the rules say, and the layout and the Info agree.
		if l.iface < 0 || l.iface > 255 || l.bulkOut&0x80 != 0 || l.bulkIn&0x80 == 0 || l.bulkOut&0x0F == 0 || l.bulkIn&0x0F == 0 {
			t.Fatalf("layout %+v", l)
		}
		if !scanEndpoint(b, l.bulkOut, 2) || !scanEndpoint(b, l.bulkIn, 2) {
			t.Fatalf("the endpoints of the layout are not bulk endpoints of the descriptors: %+v", l)
		}
		if l.interruptIn != 0 && !scanEndpoint(b, l.interruptIn, 3) {
			t.Fatalf("an interrupt endpoint that is not there: %+v", l)
		}
		if l.maxPacketIn < 8 || l.maxPacketIn > 1024 || l.maxPacketOut < 8 || l.maxPacketOut > 1024 {
			t.Fatalf("packet sizes %+v", l)
		}
		c := l.class
		if c.bcdCCID>>8 != 1 || c.maxSlotIndex != 0 || c.voltageSupport == 0 || c.protocols&3 == 0 || c.features&featAutoParameters == 0 || c.maxMessageLength < MinMessageLength {
			t.Fatalf("class %+v", c)
		}
		info := r.Info()
		if info.MaxMessageLength < MinMessageLength || info.MaxMessageLength > MaxMessageLength || info.ExtendedAPDU != (c.features&featLevelMask == featExtAPDU) ||
			(info.ExtendedAPDU && info.MaxCommandAPDU != MaxCommandAPDU) || (!info.ExtendedAPDU && info.MaxCommandAPDU != MaxShortCommandAPDU) {
			t.Fatalf("info %+v for %+v", info, c)
		}
		if len(r.wbuf) != info.MaxMessageLength || len(r.rbuf) != info.MaxMessageLength+l.maxPacketIn {
			t.Fatalf("buffers of %d and %d bytes", len(r.wbuf), len(r.rbuf))
		}
		// The layout names itself: a candidate made from it picks the same layout.
		cand := &usbccid.Candidate{VendorID: le16(b[8:]), ProductID: le16(b[10:]), Interface: l.iface, BulkOut: l.bulkOut, BulkIn: l.bulkIn}
		if l3, err := parseDescriptors(b, cand); err != nil || *l3 != *l {
			t.Fatalf("a candidate made from the layout: %v", err)
		}
		// The bytes of the class descriptor that nobody reads change nothing.
		for _, i := range []int{10, 20, 39, 48, 53} {
			x := append([]byte(nil), b...)
			ifs, _, _, err := walk(x)
			if err != nil {
				t.Fatal(err)
			}
			for _, in := range ifs {
				if in.number == l.iface && len(in.classDescs) == 1 {
					in.classDescs[0][i] ^= 0xFF // the slice aliases x
				}
			}
			if l4, err := parseDescriptors(x, nil); err != nil || *l4 != *l {
				t.Fatalf("byte %d of the class descriptor is read after all: %v", i, err)
			}
		}
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

// isNamed says that a refusal wraps one of the sentinels of this layer or of the layer below.
func isNamed(err error) bool {
	for _, s := range []error{ErrNoInterface, ErrSeveralInterfaces, ErrBadClass, ErrSeveralSlots, ErrExchangeLevel, ErrParameters, ErrVoltage,
		usbccid.ErrBadDescriptors, usbccid.ErrSeveralConfigurations, usbccid.ErrWrongDevice} {
		if errors.Is(err, s) {
			return true
		}
	}
	return false
}
