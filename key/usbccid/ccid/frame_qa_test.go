package ccid

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// B. Framing: the ten bytes of every message the reader sends, the fields of every answer it reads, the sequence numbers, stale
// answers and time extensions. The expected bytes are written out here from CCID 1.1 (chapter 6), not taken from the source.

func TestQAOutgoingHeadersByteByByte(t *testing.T) {
	f := newFake(descSpec{}, cardHandler(echoSW))
	r, err := Open(f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.SlotStatus(testBudget); err != nil { // seq 1
		t.Fatal(err)
	}
	if _, err := r.PowerOn(testBudget); err != nil { // seq 2 GetSlotStatus, seq 3 IccPowerOn
		t.Fatal(err)
	}
	if _, err := r.Transmit(apduSelect, testBudget); err != nil { // seq 4
		t.Fatal(err)
	}
	if err := r.PowerOff(testBudget); err != nil { // seq 5
		t.Fatal(err)
	}
	// type, dwLength (little-endian), bSlot, bSeq, three bytes that belong to the message: bBWI/bPowerSelect/RFU, wLevelParameter/RFU.
	want := [][]byte{
		{0x65, 0, 0, 0, 0, 0x00, 0x01, 0, 0, 0},
		{0x65, 0, 0, 0, 0, 0x00, 0x02, 0, 0, 0},
		{0x62, 0, 0, 0, 0, 0x00, 0x03, 0, 0, 0},
		append([]byte{0x6F, 0x0A, 0, 0, 0, 0x00, 0x04, 0, 0, 0}, apduSelect...),
		{0x63, 0, 0, 0, 0, 0x00, 0x05, 0, 0, 0},
	}
	if len(f.outs) != len(want) {
		t.Fatalf("%d messages sent, want %d", len(f.outs), len(want))
	}
	for i := range want {
		if !bytes.Equal(f.outs[i], want[i]) {
			t.Errorf("message %d = % x, want % x", i, f.outs[i], want[i])
		}
	}
}

// The blocks of a command and the continuations: the length field is little-endian over four bytes, and wLevelParameter over two.
func TestQAOutgoingHeadersOfChainedMessages(t *testing.T) {
	for _, c := range []struct {
		name  string
		spec  descSpec
		apdu  int
		heads [][]byte
	}{
		{"271 byte messages", descSpec{}, 700, [][]byte{
			{0x6F, 0x05, 0x01, 0, 0, 0, 0x03, 0, 0x01, 0x00},
			{0x6F, 0x05, 0x01, 0, 0, 0, 0x04, 0, 0x03, 0x00},
			{0x6F, 0xB2, 0x00, 0, 0, 0, 0x05, 0, 0x02, 0x00}, // 700 - 2*261 = 178
			{0x6F, 0x00, 0x00, 0, 0, 0, 0x06, 0, 0x10, 0x00},
		}},
		{"messages of 64 KiB", descSpec{maxMsg: 1 << 16, mpsIn: 512, mpsOut: 512}, MaxCommandAPDU, [][]byte{
			{0x6F, 0xF6, 0xFF, 0, 0, 0, 0x03, 0, 0x01, 0x00}, // 65526 = 0xFFF6
			{0x6F, 0x12, 0x00, 0, 0, 0, 0x04, 0, 0x02, 0x00}, // 65544 - 65526 = 18
			{0x6F, 0x00, 0x00, 0, 0, 0, 0x05, 0, 0x10, 0x00},
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			cc := &chainCard{respParts: [][]byte{{0x01}, {0x90, 0x00}}} // two blocks, so one continuation
			f := newFake(c.spec, cc.handler())
			r, err := Open(f, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.PowerOn(testBudget); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Transmit(bytes.Repeat([]byte{0x11}, c.apdu), testBudget); err != nil {
				t.Fatal(err)
			}
			outs := f.outs[2:]
			if len(outs) != len(c.heads) {
				t.Fatalf("%d messages, want %d", len(outs), len(c.heads))
			}
			for i, h := range c.heads {
				if !bytes.Equal(outs[i][:10], h) {
					t.Errorf("block %d: header % x, want % x", i, outs[i][:10], h)
				}
			}
		})
	}
}

// The fields of an answer are read from their places in the ten bytes: a failed answer has the status in byte 7 (bits 0-1 the card,
// bits 6-7 the command) and the error in byte 8; an accepted answer's data starts at byte 10 and has dwLength bytes.
func TestQAIncomingHeaderFields(t *testing.T) {
	reply := func(build func(seq byte) []byte) func(f *fakeDevice, m []byte) {
		return func(f *fakeDevice, m []byte) {
			if m[0] == msgXfrBlock {
				f.send(build(m[6]))
				return
			}
			cardHandler(echoSW)(f, m)
		}
	}
	t.Run("failed answer", func(t *testing.T) {
		for _, c := range []struct{ status, code, icc byte }{{0x40, 0xE0, 0}, {0x41, 0xFE, 1}, {0x42, 0xF2, 2}, {0x7D, 0x05, 1}} {
			r, _ := openPowered(t, Options{}, reply(func(seq byte) []byte { return msg(rspDataBlock, seq, c.status, c.code, 0x55, qaResp) }))
			_, err := r.Transmit(apduSelect, testBudget)
			var ce *CommandError
			if !errors.As(err, &ce) || *ce != (CommandError{Op: "XfrBlock", Code: c.code, ICCStatus: c.icc}) {
				t.Errorf("status %#x: err = %v, ce = %+v", c.status, err, ce)
			}
		}
	})
	t.Run("data of the length that the header says", func(t *testing.T) {
		for _, n := range []int{0, 1, 2, 255, 256, 257, 261} {
			data := bytes.Repeat([]byte{0x77}, n)
			r, _ := openPowered(t, Options{}, reply(func(seq byte) []byte {
				m := msg(rspDataBlock, seq, stOK, 0xAB, 0, data) // bError of an accepted answer is not looked at
				return m
			}))
			got, err := r.Transmit(apduSelect, testBudget)
			if err != nil || !bytes.Equal(got, data) {
				t.Errorf("%d bytes: got %d bytes, %v", n, len(got), err)
			}
		}
	})
	t.Run("the four length bytes", func(t *testing.T) {
		for _, c := range []struct {
			len4 [4]byte
			data int
			ok   bool
		}{
			{[4]byte{0x00, 0x01, 0x00, 0x00}, 256, true},
			{[4]byte{0x01, 0x01, 0x00, 0x00}, 257, true},
			{[4]byte{0x05, 0x01, 0x00, 0x00}, 261, true}, // the largest at a message length of 271
			{[4]byte{0x06, 0x01, 0x00, 0x00}, 262, false},
			{[4]byte{0x00, 0x00, 0x01, 0x00}, 0, false}, // 65536
			{[4]byte{0x00, 0x00, 0x00, 0x01}, 0, false}, // 16 MiB
		} {
			r, _ := openPowered(t, Options{}, reply(func(seq byte) []byte {
				m := msg(rspDataBlock, seq, stOK, 0, 0, bytes.Repeat([]byte{0x31}, c.data))
				copy(m[1:5], c.len4[:])
				return m
			}))
			got, err := r.Transmit(apduSelect, testBudget)
			if c.ok && (err != nil || len(got) != c.data) || !c.ok && !errors.Is(err, ErrProtocol) {
				t.Errorf("% x: %d bytes, err = %v, want ok=%v", c.len4, len(got), err, c.ok)
			}
		}
	})
	t.Run("bmICCStatus of the slot status", func(t *testing.T) {
		for status, want := range map[byte]SlotState{0x00: SlotActive, 0x01: SlotInactive, 0x02: SlotEmpty} {
			r, _, err := openFake(Options{}, func(f *fakeDevice, m []byte) { f.send(msg(rspSlotStatus, m[6], status, 0, 0, nil)) })
			if err != nil {
				t.Fatal(err)
			}
			if got, err := r.SlotStatus(testBudget); err != nil || got != want {
				t.Errorf("status %#x: %v %v", status, got, err)
			}
		}
	})
}

// Sequence numbers count every message, all kinds and every block of a chain, and wrap from 255 to 0 and go on.
func TestQASequenceNumbersCountEveryMessage(t *testing.T) {
	cc := &chainCard{respParts: [][]byte{{0x01}, {0x02}, {0x90, 0}}}
	r, f, err := openFake(Options{}, cc.handler())
	if err != nil {
		t.Fatal(err)
	}
	long := bytes.Repeat([]byte{4}, 600)
	for i := 0; len(f.outs) < 700; i++ {
		switch i % 5 {
		case 0:
			_, err = r.SlotStatus(testBudget)
		case 1:
			_, err = r.PowerOn(testBudget)
		case 2:
			_, err = r.Transmit(apduSelect, testBudget)
		case 3:
			_, err = r.Transmit(long, testBudget)
		case 4:
			err = r.PowerOff(testBudget)
			if err == nil {
				_, err = r.PowerOn(testBudget)
			}
		}
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	for i, m := range f.outs {
		if want := byte(i + 1); m[6] != want {
			t.Fatalf("message %d has sequence %d, want %d (every message counts, and 255 is followed by 0)", i, m[6], want)
		}
	}
	if f.outs[254][6] != 255 || f.outs[255][6] != 0 || f.outs[256][6] != 1 {
		t.Errorf("around the wrap: %d %d %d", f.outs[254][6], f.outs[255][6], f.outs[256][6])
	}
}

// An answer that carries one of the previous sequence numbers is skipped, up to MaxStaleAnswers, and then the session is lost. The
// count is per exchange; the check runs across the wrap of the sequence number.
func TestQAStaleAnswersAcrossTheWrap(t *testing.T) {
	for k := 0; k <= MaxStaleAnswers+1; k++ {
		t.Run(fmt.Sprintf("%d stale", k), func(t *testing.T) {
			armed := false
			h := func(f *fakeDevice, m []byte) {
				if m[0] != msgXfrBlock || !armed {
					cardHandler(echoSW)(f, m)
					return
				}
				for i := range k {
					f.send(msg(rspDataBlock, m[6]-1-byte(i), stOK, 0, 0, qaResp))
				}
				f.send(msg(rspDataBlock, m[6], stOK, 0, 0, []byte{0x90, 0x00}))
			}
			r, f := openPowered(t, Options{}, h)
			for i := 0; len(f.outs) < 250; i++ { // the next sequence number is 251
				if _, err := r.Transmit(apduSelect, testBudget); err != nil {
					t.Fatal(err)
				}
			}
			armed = true
			for i := 0; i < 20; i++ { // goes through 255, 0, 1
				got, err := r.Transmit(apduSelect, testBudget)
				if k <= MaxStaleAnswers {
					if err != nil || !bytes.Equal(got, []byte{0x90, 0}) {
						t.Fatalf("exchange %d (sequence %d): %x %v", i, f.outs[len(f.outs)-1][6], got, err)
					}
					continue
				}
				lostWith(t, err, ErrProtocol)
				assertNoLeak(t, "stale", err, qaResp)
				untouched(t, r, f, err)
				return
			}
			if k > MaxStaleAnswers {
				t.Fatal("never lost")
			}
		})
	}
}

// An answer for a sequence number that has not been used yet is not for this call either.
func TestQAAnswerFromTheFutureIsStale(t *testing.T) {
	r, f := openPowered(t, Options{}, func(f *fakeDevice, m []byte) {
		if m[0] == msgXfrBlock {
			f.send(msg(rspDataBlock, m[6]+1, stOK, 0, 0, qaResp))
			f.send(msg(rspDataBlock, m[6], stOK, 0, 0, []byte{0x90, 0}))
			return
		}
		cardHandler(echoSW)(f, m)
	})
	if got, err := r.Transmit(apduSelect, testBudget); err != nil || !bytes.Equal(got, []byte{0x90, 0}) {
		t.Fatalf("%x %v", got, err)
	}
	_ = f
}

// Time extensions are consumed, never the answer, whatever their ICC bits, their bError (the wait multiplier) and their data, in any
// number within the budget, for every call that waits. They do not count as stale answers.
func TestQATimeExtensions(t *testing.T) {
	for _, op := range qaOps {
		for _, n := range []int{1, 2, 10, 500, 4000} {
			t.Run(fmt.Sprintf("%s/%d", op.name, n), func(t *testing.T) {
				extra := false
				h := func(f *fakeDevice, m []byte) {
					if extra && m[0] == op.typ {
						for i := range n {
							st := byte(stTimeExt | byte(i%3)) // the card bits are anything
							var data []byte
							if i%4 == 0 {
								data = qaResp[:5]
							}
							f.send(msg(rspFor(m[0]), m[6], st, byte(i), 0, data))
						}
					}
					cardHandler(echoSW)(f, m)
				}
				r, f := openPowered(t, Options{}, h)
				extra = true
				outs := len(f.outs)
				var err error
				switch op.name {
				case "Transmit":
					var got []byte
					got, err = r.Transmit(apduSelect, testBudget)
					if err == nil && !bytes.Equal(got, append(append([]byte(nil), apduSelect...), 0x90, 0)) {
						t.Errorf("a time extension was taken for the answer: %x", got)
					}
				default:
					err = op.call(r, testBudget)
				}
				if err != nil {
					t.Fatal(err)
				}
				want := 1
				if strings.HasPrefix(op.name, "PowerOn") {
					want = 2 // GetSlotStatus, then IccPowerOn
				}
				if sent := len(f.outs) - outs; sent != want {
					t.Errorf("%d messages sent, want %d (a time extension is no reason to send again)", sent, want)
				}
				if len(f.in) != 0 {
					t.Errorf("%d packets left", len(f.in))
				}
			})
		}
	}
}

// The count of stale answers is not reset by the time extensions between them.
func TestQATimeExtensionsDoNotResetTheStaleCount(t *testing.T) {
	r, f := openPowered(t, Options{}, func(f *fakeDevice, m []byte) {
		if m[0] == msgXfrBlock {
			for i := range MaxStaleAnswers + 1 {
				f.send(msg(rspDataBlock, m[6], stTimeExt, 1, 0, nil))
				f.send(msg(rspDataBlock, m[6]-1-byte(i), stOK, 0, 0, qaResp))
			}
			f.send(msg(rspDataBlock, m[6], stOK, 0, 0, []byte{0x90, 0}))
			return
		}
		cardHandler(echoSW)(f, m)
	})
	_, err := r.Transmit(apduSelect, testBudget)
	lostWith(t, err, ErrProtocol)
	untouched(t, r, f, err)
}

// A stale time extension (another sequence number) is a stale answer, not a wait of this call.
func TestQAStaleTimeExtensionCounts(t *testing.T) {
	r, f := openPowered(t, Options{}, func(f *fakeDevice, m []byte) {
		if m[0] == msgXfrBlock {
			for i := range MaxStaleAnswers + 1 {
				f.send(msg(rspDataBlock, m[6]-1-byte(i), stTimeExt, 1, 0, nil))
			}
			f.send(msg(rspDataBlock, m[6], stOK, 0, 0, []byte{0x90, 0}))
			return
		}
		cardHandler(echoSW)(f, m)
	})
	_, err := r.Transmit(apduSelect, testBudget)
	lostWith(t, err, ErrProtocol)
	untouched(t, r, f, err)
}

// After the budget no transfer is started: with time extensions queued without end, the last BulkIn begins before the deadline.
func TestQANoTransferAfterTheBudget(t *testing.T) {
	const budget = 80 * time.Millisecond
	var times []time.Time
	r, f := openPowered(t, Options{TransferTimeout: time.Second}, func(f *fakeDevice, m []byte) {
		if m[0] != msgXfrBlock {
			cardHandler(echoSW)(f, m)
			return
		}
		f.endless = msg(rspDataBlock, m[6], stTimeExt, 1, 0, nil)
		f.onIn = func(*fakeDevice) { times = append(times, time.Now()) }
	})
	start := time.Now()
	_, err := r.Transmit(apduSelect, budget)
	end := time.Now()
	lostWith(t, err, ErrBudgetExhausted)
	if len(times) == 0 {
		t.Fatal("no read")
	}
	if last := times[len(times)-1]; last.After(start.Add(budget + 5*time.Millisecond)) {
		t.Errorf("a transfer started %v after the budget", last.Sub(start.Add(budget)))
	}
	if end.Sub(start) < budget {
		t.Errorf("returned after %v, before the budget %v", end.Sub(start), budget)
	}
	untouched(t, r, f, err)
}

// A time extension in the middle of a chain, before an acknowledgement and before a block of the response, is a wait and no more.
func TestQATimeExtensionsInsideChains(t *testing.T) {
	respBlocks := [][]byte{bytes.Repeat([]byte{1}, 100), bytes.Repeat([]byte{2}, 100), {0x90, 0x00}}
	dev := &chainDevice{respBlocks: respBlocks}
	dev.onBlock = func(f *fakeDevice, m []byte, _ uint16, _ int) bool {
		for range 5 {
			f.send(msg(rspDataBlock, m[6], stTimeExt, 2, 0, nil))
		}
		return false
	}
	r, f := openPowered(t, Options{}, dev.handler())
	outs := len(f.outs)
	got, err := r.Transmit(qaLong, testBudget)
	want := append(append(bytes.Repeat([]byte{1}, 100), bytes.Repeat([]byte{2}, 100)...), 0x90, 0x00)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("%d bytes, %v", len(got), err)
	}
	if sent := len(f.outs) - outs; sent != 3+2 { // three blocks of the command, two continuations
		t.Errorf("%d messages sent", sent)
	}
}
