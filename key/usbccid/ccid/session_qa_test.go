package ccid

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/truvity/atoom/key/usbccid"
)

// The rules of one session against the late answers of an earlier one: the process-wide sequence counter and the drain of the IN pipe at
// the first exchange. session_test.go holds the basic cases; these are the edges.

// staleKinds are late answers of an earlier session, one for each of the first three messages of a new session (GetSlotStatus, then
// IccPowerOn, then the first XfrBlock), each with the number that message gets when the counter is pinned with setSeq(0). If one of
// them were taken, the call would return a PIN-looking answer instead of the real one.
func staleKinds() [][]byte {
	return [][]byte{
		msg(rspSlotStatus, 1, stOK|iccInact, 0, 0, nil),
		msg(rspDataBlock, 2, stOK, 0, 0, bytes.Repeat([]byte{0xEE}, 20)),
		msg(rspDataBlock, 3, stOK, 0, 0, []byte{0x6A, 0x82}),
	}
}

func TestQADrainDropsEveryNumberOfTheFirstThreeMessages(t *testing.T) {
	stale := staleKinds()
	for _, c := range []struct {
		name string
		pick []int
	}{{"the first", []int{0}}, {"the second", []int{1}}, {"the third", []int{2}}, {"all three", []int{0, 1, 2}}, {"all three, reversed", []int{2, 1, 0}}} {
		t.Run(c.name, func(t *testing.T) {
			setSeq(0)
			f := newFake(descSpec{}, cardHandler(func([]byte) []byte { return []byte{0x90, 0x00} }))
			for _, i := range c.pick {
				f.send(stale[i])
			}
			r, err := Open(f, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if st, err := r.SlotStatus(testBudget); err != nil || st != SlotActive {
				t.Fatalf("SlotStatus: %v %v", st, err)
			}
			if atr, err := r.PowerOn(testBudget); err != nil || !bytes.Equal(atr, testATR) {
				t.Fatalf("PowerOn: %x %v", atr, err)
			}
			if got, err := r.Transmit(apduSelect, testBudget); err != nil || !bytes.Equal(got, []byte{0x90, 0x00}) {
				t.Fatalf("Transmit: %x %v", got, err)
			}
			if len(f.in) != 0 {
				t.Errorf("%d packets left", len(f.in))
			}
			assertWiped(t, r, "after the drain and three calls")
		})
	}
}

// Messages and empty packets of an earlier session: the pipe is quiet after two reads in a row that time out. Six items and those two
// reads are the eight reads of the drain; seven items do not fit, and the session is lost.
func TestQADrainBoundsTheNumberOfItems(t *testing.T) {
	for n := 0; n <= MaxDrainReads+2; n++ {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			setSeq(0)
			f := newFake(descSpec{}, cardHandler(echoSW))
			for i := range n {
				if i%2 == 0 {
					f.sendRaw(msg(rspDataBlock, byte(i+1), stOK, 0, 0, qaResp))
				} else {
					f.sendRaw([]byte{}) // an empty packet
				}
			}
			r, err := Open(f, Options{})
			if err != nil {
				t.Fatal(err)
			}
			st, err := r.SlotStatus(testBudget)
			if n+2 <= MaxDrainReads {
				if err != nil || st != SlotActive || len(f.in) != 0 {
					t.Fatalf("%d items: %v %v, %d left", n, st, err, len(f.in))
				}
				return
			}
			lostWith(t, err, ErrProtocol)
			if f.nOut != 0 || f.nIn != MaxDrainReads {
				t.Errorf("%d items: %d writes and %d reads, want 0 and %d", n, f.nOut, f.nIn, MaxDrainReads)
			}
			assertNoLeak(t, "drain", err, qaResp)
			assertWiped(t, r, "after the drain that lost the session")
			untouched(t, r, f, err)
		})
	}
}

// What the drain read is gone: no byte of it is in the reader after the call, whatever was in the pipe.
func TestQADrainClearsWhatItRead(t *testing.T) {
	setSeq(0)
	f := newFake(descSpec{}, cardHandler(echoSW))
	f.send(msg(rspDataBlock, 200, stOK, 0, 0, bytes.Repeat(qaResp, 10)))
	f.send(msg(rspDataBlock, 201, stOK, 0, 0, bytes.Repeat(qaResp, 3)))
	r, err := Open(f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	// Look into the read buffer between the drain's reads: the second read must not leave the first message behind.
	var seen [][]byte
	f.onIn = func(*fakeDevice) { seen = append(seen, append([]byte(nil), r.rbuf...)) }
	if _, err := r.SlotStatus(testBudget); err != nil {
		t.Fatal(err)
	}
	for i, b := range seen[1:] { // before the read i+1, everything the earlier reads put in the buffer is gone
		if !allZero(b) {
			t.Errorf("before read %d the read buffer still holds data", i+2)
		}
	}
	assertWiped(t, r, "after the drain")
}

// The drain reads only, once, before the first write of the session. Open and Close as the first call make no read.
func TestQADrainOrderAndOpenAndClose(t *testing.T) {
	setSeq(0)
	f := newFake(descSpec{}, cardHandler(echoSW))
	f.send(staleKinds()[0])
	f.send(staleKinds()[2])
	r, err := Open(f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(f.calls); got != "[descriptors claim]" {
		t.Fatalf("Open made the calls %s", got)
	}
	if _, err := r.PowerOn(testBudget); err != nil {
		t.Fatal(err)
	}
	// Four reads for the drain (two messages, then two quiet windows), then write and read for each of the two messages of PowerOn.
	if got := fmt.Sprint(f.calls[2:]); got != "[in in in in out in out in]" {
		t.Errorf("calls of the first call: %s", got)
	}
	n := f.mark()
	for range 3 {
		if _, err := r.Transmit(apduSelect, testBudget); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Join(f.callsAfter(n), " "); got != "out in out in out in" {
		t.Errorf("later calls: %s (no drain again, no read before a write)", got)
	}

	// Close as the first call: no read, no write.
	g := newFake(descSpec{}, cardHandler(echoSW))
	g.send(staleKinds()[1])
	r2, err := Open(g, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := r2.Close(); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(g.calls); got != "[descriptors claim release close]" {
		t.Errorf("Open then Close made the calls %s", got)
	}
	if len(g.in) != 1 {
		t.Errorf("Close read the pipe: %d packets left", len(g.in))
	}
}

// The first exchange is a drain whichever call it is, and every call that is first runs the drain: SlotStatus, PowerOn, PowerOff.
func TestQADrainRunsBeforeWhicheverCallIsFirst(t *testing.T) {
	for _, op := range qaOps {
		if op.name == "Transmit" {
			continue // refused before the port: the card is not powered
		}
		t.Run(op.name, func(t *testing.T) {
			setSeq(0)
			f := newFake(descSpec{}, cardHandler(echoSW))
			f.send(msg(rspSlotStatus, 1, stOK|iccNone, 0, 0, nil))
			f.send(msg(rspDataBlock, 1, stOK, 0, 0, qaResp))
			r, err := Open(f, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if err := op.call(r, testBudget); err != nil {
				t.Fatal(err)
			}
			// Two stale messages and two quiet windows, all reads, before the first write.
			if f.calls[2] != "in" || f.calls[3] != "in" || f.calls[4] != "in" || f.calls[5] != "in" || f.calls[6] != "out" {
				t.Errorf("calls: %v", f.calls[2:])
			}
		})
	}
}

// A Transmit that is refused before the port (the card is not powered, a bad command, a bad budget) does not drain.
func TestQARefusedCallsDoNotDrain(t *testing.T) {
	f := newFake(descSpec{}, cardHandler(echoSW))
	r, err := Open(f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = r.Transmit(apduSelect, testBudget)
	_, _ = r.Transmit(nil, testBudget)
	_, _ = r.Transmit(apduSelect, 0)
	_, _ = r.SlotStatus(-1)
	if got := fmt.Sprint(f.calls); got != "[descriptors claim]" {
		t.Errorf("calls %s", got)
	}
}

// A late answer that arrives AFTER the drain, while the first command waits, with another number: it is skipped like any stale answer
// (and counted).
func TestQALateAnswerAfterTheDrainWithAnotherNumberIsSkipped(t *testing.T) {
	setSeq(0)
	for _, delta := range []byte{1, 2, 255, 128} {
		f := newFake(descSpec{}, func(f *fakeDevice, m []byte) {
			if m[0] == msgXfrBlock {
				f.send(msg(rspDataBlock, m[6]+delta, stOK, 0, 0, []byte{0x6A, 0x82}))
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
		got, err := r.Transmit(apduSelect, testBudget)
		if err != nil || !bytes.Equal(got, append(append([]byte(nil), apduSelect...), 0x90, 0)) {
			t.Errorf("delta %d: %x %v", delta, got, err)
		}
	}
}

// REST RISK, documented and not a bug: the drain runs before the first write, so an answer that arrives after it with the very number
// and type of the command that waits for it cannot be told from the real answer. The reader takes the first answer that carries the right
// number, and the real one stays in the pipe, where the next exchange skips it as stale. The sequence counter makes the number hard to
// hit (random seed, one counter for the process), and nothing in this layer can close the gap that remains.
func TestQARestRiskLateAnswerWithTheSameNumberAfterTheDrain(t *testing.T) {
	setSeq(0)
	late := []byte{0x6A, 0x82}
	genuine := []byte{0x90, 0x00}
	first := true
	f := newFake(descSpec{}, func(f *fakeDevice, m []byte) {
		if m[0] == msgXfrBlock && first {
			first = false
			f.send(msg(rspDataBlock, m[6], stOK, 0, 0, late))    // the late answer: the same number and type
			f.send(msg(rspDataBlock, m[6], stOK, 0, 0, genuine)) // the real one
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
	got, err := r.Transmit(apduSelect, testBudget)
	if err != nil || !bytes.Equal(got, late) {
		t.Fatalf("got %x, %v: the reader is expected to return the first answer that has the number (the late one)", got, err)
	}
	if len(f.in) != 1 {
		t.Fatalf("%d packets left, want the real answer", len(f.in))
	}
	// The real answer is stale for the next exchange (another number) and is skipped there; the session goes on.
	got, err = r.Transmit(apduSelect, testBudget)
	if err != nil || !bytes.Equal(got, append(append([]byte(nil), apduSelect...), 0x90, 0)) {
		t.Fatalf("next exchange: %x %v", got, err)
	}
}

// The whole story: a session is lost while the card waits, the caller closes and opens again, and the late answer of the old session
// reaches the pipe of the new one. The new numbers continue the counter, and the drain drops the late answer.
func TestQAEndToEndLostSessionThenLateAnswer(t *testing.T) {
	setSeq(40)
	var oldSeq byte
	silent := true
	f := newFake(descSpec{}, func(f *fakeDevice, m []byte) {
		if m[0] == msgXfrBlock && silent {
			oldSeq = m[6]
			return // the card waits for a person
		}
		cardHandler(echoSW)(f, m)
	})
	r1, err := Open(f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r1.PowerOn(testBudget); err != nil {
		t.Fatal(err)
	}
	_, err = r1.Transmit(qaCmd, 30*time.Millisecond)
	lostWith(t, err, ErrBudgetExhausted)
	if err := r1.Close(); err != nil {
		t.Fatal(err)
	}
	f.claimed = false // the second Reader stands for a new Device of the same reader: the first one let go of its claim
	// The person touches the key late: the old answer, with the number of the old command, arrives.
	silent = false
	f.send(msg(rspDataBlock, oldSeq, stOK, 0, 0, []byte{0x90, 0x00}))
	f.send(msg(rspDataBlock, oldSeq, stOK, 0, 0, qaResp))
	r2, err := Open(f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r2.PowerOn(testBudget); err != nil {
		t.Fatal(err)
	}
	got, err := r2.Transmit(qaCmd, testBudget)
	if err != nil || !bytes.Equal(got, append(append([]byte(nil), qaCmd...), 0x90, 0)) {
		t.Fatalf("%x %v", got, err)
	}
	var newNumbers []byte
	for _, m := range f.outs[3:] {
		newNumbers = append(newNumbers, m[6])
	}
	for _, n := range newNumbers {
		if n == oldSeq {
			t.Errorf("the new session used the number %d of the old command", oldSeq)
		}
	}
}

// Two Readers alive at once, each on its own device, take turns from the one counter. Each gets only its own answers, across the wrap at
// 255, even when an answer of the one reaches the pipe of the other.
func TestQATwoReadersTakeTurnsAcrossTheWrap(t *testing.T) {
	setSeq(238)
	var fa, fb *fakeDevice
	echoTo := func(other **fakeDevice) func(f *fakeDevice, m []byte) {
		return func(f *fakeDevice, m []byte) {
			cardHandler(echoSW)(f, m)
			if m[0] == msgXfrBlock && *other != nil {
				(*other).queueLocked(msg(rspDataBlock, m[6], stOK, 0, 0, qaResp)) // a stray answer in the other pipe
			}
		}
	}
	fa = newFake(descSpec{}, echoTo(&fb))
	fb = newFake(descSpec{}, echoTo(&fa))
	a, err := Open(fa, Options{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open(fb, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.PowerOn(testBudget); err != nil {
		t.Fatal(err)
	}
	if _, err := b.PowerOn(testBudget); err != nil {
		t.Fatal(err)
	}
	for i := range 40 {
		ca, cb := pattern(20, 2*i), pattern(30, 2*i+1)
		if got, err := a.Transmit(ca, testBudget); err != nil || !bytes.Equal(got, append(append([]byte(nil), ca...), 0x90, 0)) {
			t.Fatalf("round %d, a: %x %v", i, got, err)
		}
		if got, err := b.Transmit(cb, testBudget); err != nil || !bytes.Equal(got, append(append([]byte(nil), cb...), 0x90, 0)) {
			t.Fatalf("round %d, b: %x %v", i, got, err)
		}
	}
	// 4 + 80 messages from 239: the counter went through 255 and 0.
	wrapped := false
	prev := fa.outs[0][6]
	for _, m := range fa.outs[1:] {
		if m[6] < prev {
			wrapped = true
		}
		prev = m[6]
	}
	if !wrapped {
		t.Error("the counter did not wrap during the test")
	}
	seen := map[byte]int{}
	for _, m := range append(append([][]byte(nil), fa.outs...), fb.outs...) {
		seen[m[6]]++
	}
	for n, c := range seen {
		if c > 1 && len(fa.outs)+len(fb.outs) <= 256 {
			t.Errorf("number %d used %d times within 256 messages", n, c)
		}
	}
}

// The sequence numbers of a process: each message takes the next, whichever Reader, and after a Reader is lost the next Reader goes on.
func TestQANumbersContinueAcrossReaders(t *testing.T) {
	setSeq(5)
	_, f1 := openPowered(t, Options{}, cardHandler(echoSW))
	if f1.outs[0][6] != 6 || f1.outs[1][6] != 7 {
		t.Errorf("first session: %d %d", f1.outs[0][6], f1.outs[1][6])
	}
	// A lost session in the middle.
	r2, f2, err := openFake(Options{}, func(*fakeDevice, []byte) {})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r2.SlotStatus(testBudget); !errors.Is(err, ErrSessionLost) {
		t.Fatal(err)
	}
	_, f3 := openPowered(t, Options{}, cardHandler(echoSW))
	if f2.outs[0][6] != 8 || f3.outs[0][6] != 9 || f3.outs[1][6] != 10 {
		t.Errorf("numbers: %d, %d %d", f2.outs[0][6], f3.outs[0][6], f3.outs[1][6])
	}
}

// ---- the declared message length, on every build ----

func TestQADeclaredMessageLengthIsHeldOnEveryBuild(t *testing.T) {
	for _, declared := range []uint32{271, 272, 65535, 65536, 65537, 1 << 20, 1<<31 - 1, 1 << 31, 1<<31 + 1, 0xFFFFFFFE, 0xFFFFFFFF} {
		t.Run(fmt.Sprintf("%#x", declared), func(t *testing.T) {
			cc := &chainCard{respParts: [][]byte{{0x90, 0x00}}}
			f := newFake(descSpec{maxMsg: declared, mpsIn: 512, mpsOut: 512}, cc.handler())
			r, err := Open(f, Options{})
			if err != nil {
				t.Fatal(err)
			}
			want := int(min(declared, uint32(MaxMessageLength)))
			if info := r.Info(); info.MaxMessageLength != want {
				t.Errorf("message length %d, want %d", info.MaxMessageLength, want)
			}
			if len(r.wbuf) != want || len(r.rbuf) != want+512 {
				t.Errorf("buffers of %d and %d bytes", len(r.wbuf), len(r.rbuf))
			}
			if _, err := r.PowerOn(testBudget); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Transmit(bytes.Repeat([]byte{3}, MaxCommandAPDU), testBudget); err != nil {
				t.Fatal(err)
			}
			for _, m := range f.outs {
				if len(m) > want {
					t.Fatalf("a message of %d bytes, over %d", len(m), want)
				}
			}
		})
	}
}

// ---- the status of an answer, for every call ----

// A card status of 3 is reserved in an accepted answer and in a failed one, for every call; in a time extension the card bits are not
// looked at.
func TestQAReservedCardStatusForEveryCall(t *testing.T) {
	for _, op := range qaOps {
		for _, c := range []struct {
			name   string
			status byte
			ok     bool
		}{{"accepted", stOK | 3, false}, {"failed", stFailed | 3, false}, {"time extension", stTimeExt | 3, true}} {
			t.Run(op.name+"/"+c.name, func(t *testing.T) {
				armed := false
				h := func(ff *fakeDevice, m []byte) {
					if armed && m[0] == op.typ {
						if c.status&0xC0 == stTimeExt {
							ff.send(msg(rspFor(m[0]), m[6], c.status, 1, 0, nil))
							cardHandler(echoSW)(ff, m)
							return
						}
						ff.send(msg(rspFor(m[0]), m[6], c.status, 0xFE, 0, qaResp))
						return
					}
					cardHandler(echoSW)(ff, m)
				}
				r, ff := openPowered(t, Options{}, h)
				armed = true
				err := op.call(r, testBudget)
				if c.ok {
					if err != nil {
						t.Fatal(err)
					}
					return
				}
				lostWith(t, err, ErrProtocol)
				assertNoLeak(t, "reserved status", err, qaResp, qaCmd)
				untouched(t, r, ff, err)
			})
		}
	}
}

// A successful answer of a card that is not active, for each call and each state: what the call returns, and whether the next
// Transmit goes to the port.
func TestQASuccessWithACardThatIsNotActive(t *testing.T) {
	for _, icc := range []byte{iccInact, iccNone} {
		armed := false
		answer := func(f *fakeDevice, m []byte, typ byte) bool {
			if !armed || m[0] != typ {
				return false
			}
			switch typ {
			case msgXfrBlock:
				f.send(msg(rspDataBlock, m[6], stOK|icc, 0, 0, []byte{0x90, 0}))
			case msgIccPowerOn:
				f.send(msg(rspDataBlock, m[6], stOK|icc, 0, 0, testATR))
			case msgGetSlotState, msgIccPowerOff:
				f.send(msg(rspSlotStatus, m[6], stOK|icc, 0, 0, nil))
			}
			return true
		}
		t.Run(fmt.Sprintf("icc%d/Transmit", icc), func(t *testing.T) {
			armed = false
			r, f := openPowered(t, Options{}, func(f *fakeDevice, m []byte) {
				if !answer(f, m, msgXfrBlock) {
					cardHandler(echoSW)(f, m)
				}
			})
			armed = true
			got, err := r.Transmit(qaCmd, testBudget)
			if got != nil || !errors.Is(err, ErrNotPowered) || errors.Is(err, ErrSessionLost) {
				t.Fatalf("%x %v", got, err)
			}
			assertNoLeak(t, "not active", err, qaCmd)
			mark := f.mark()
			if _, err := r.Transmit(qaCmd, testBudget); !errors.Is(err, ErrNotPowered) || len(f.callsAfter(mark)) != 0 {
				t.Errorf("a command to a card that is off: %v", err)
			}
			if _, err := r.PowerOn(testBudget); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Transmit(qaCmd, testBudget); err == nil {
				t.Error("the card answers inactive again and the answer was accepted")
			}
		})
		t.Run(fmt.Sprintf("icc%d/PowerOn", icc), func(t *testing.T) {
			armed = false
			r, _, err := openFake(Options{}, func(f *fakeDevice, m []byte) {
				if !answer(f, m, msgIccPowerOn) {
					cardHandler(echoSW)(f, m)
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			armed = true
			atr, err := r.PowerOn(testBudget)
			if atr != nil || !errors.Is(err, ErrNotPowered) || errors.Is(err, ErrSessionLost) {
				t.Fatalf("%x %v", atr, err)
			}
			if _, err := r.Transmit(qaCmd, testBudget); !errors.Is(err, ErrNotPowered) {
				t.Errorf("Transmit: %v", err)
			}
		})
		t.Run(fmt.Sprintf("icc%d/PowerOff", icc), func(t *testing.T) {
			armed = false
			r, _ := openPowered(t, Options{}, func(f *fakeDevice, m []byte) {
				if !answer(f, m, msgIccPowerOff) {
					cardHandler(echoSW)(f, m)
				}
			})
			armed = true
			if err := r.PowerOff(testBudget); err != nil { // the reader says the card is off or gone: that is what was asked
				t.Fatalf("%v", err)
			}
			if _, err := r.Transmit(qaCmd, testBudget); !errors.Is(err, ErrNotPowered) {
				t.Errorf("Transmit after PowerOff: %v", err)
			}
		})
		t.Run(fmt.Sprintf("icc%d/SlotStatus", icc), func(t *testing.T) {
			armed = false
			r, _ := openPowered(t, Options{}, func(f *fakeDevice, m []byte) {
				if !answer(f, m, msgGetSlotState) {
					cardHandler(echoSW)(f, m)
				}
			})
			armed = true
			if st, err := r.SlotStatus(testBudget); err != nil || st != SlotState(icc) {
				t.Fatalf("%v %v", st, err)
			}
			if _, err := r.Transmit(qaCmd, testBudget); !errors.Is(err, ErrNotPowered) {
				t.Errorf("Transmit after SlotStatus: %v", err)
			}
		})
	}
}

// A failed answer to GetSlotStatus with a card that is not active takes the power state away; with an active card it does not.
func TestQAFailedSlotStatusAndThePowerState(t *testing.T) {
	for _, icc := range []byte{iccPresent, iccInact, iccNone} {
		t.Run(fmt.Sprint(icc), func(t *testing.T) {
			failing := false
			r, _ := openPowered(t, Options{}, func(f *fakeDevice, m []byte) {
				if failing && m[0] == msgGetSlotState {
					f.send(msg(rspSlotStatus, m[6], stFailed|icc, 0xFB, 0, nil))
					return
				}
				cardHandler(echoSW)(f, m)
			})
			failing = true
			if _, err := r.SlotStatus(testBudget); !errors.Is(err, ErrReaderFault) || errors.Is(err, ErrSessionLost) {
				t.Fatal(err)
			}
			failing = false
			_, err := r.Transmit(qaCmd, testBudget)
			if (icc == iccPresent) != (err == nil) || icc != iccPresent && !errors.Is(err, ErrNotPowered) {
				t.Errorf("icc %d: Transmit after the failure: %v", icc, err)
			}
		})
	}
}

// ---- a panic of the port ----

type qaPanic struct{ secret []byte }

func TestQAPanicInThePortLosesTheSession(t *testing.T) {
	boom := errors.New("fake: the port broke")
	for _, c := range []struct {
		name  string
		value any
		arm   func(f *fakeDevice, baseIn, baseOut int)
		call  func(r *Reader)
	}{
		{"BulkIn of a Transmit, a string", "the port broke", func(f *fakeDevice, in, _ int) {
			f.inFail = func(n int) error {
				if n > in {
					panic("the port broke")
				}
				return nil
			}
		}, func(r *Reader) { _, _ = r.Transmit(qaCmd, testBudget) }},
		{"BulkOut of a Transmit, an error", boom, func(f *fakeDevice, _, out int) {
			f.outFail = func(n int) error {
				if n > out {
					panic(boom)
				}
				return nil
			}
		}, func(r *Reader) { _, _ = r.Transmit(qaCmd, testBudget) }},
		{"the second message of PowerOn, a struct", qaPanic{qaResp}, func(f *fakeDevice, _, out int) {
			f.outFail = func(n int) error {
				if n > out+1 {
					panic(qaPanic{qaResp})
				}
				return nil
			}
		}, func(r *Reader) { _, _ = r.PowerOn(testBudget) }},
		{"BulkIn of SlotStatus, an integer", 42, func(f *fakeDevice, in, _ int) {
			f.inFail = func(n int) error {
				if n > in {
					panic(42)
				}
				return nil
			}
		}, func(r *Reader) { _, _ = r.SlotStatus(testBudget) }},
		{"PowerOff, a nil func call", "runtime", func(f *fakeDevice, _, _ int) {
			f.onOut = func(*fakeDevice, []byte) { panic("runtime") }
		}, func(r *Reader) { _ = r.PowerOff(testBudget) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, f := openPowered(t, Options{}, cardHandler(echoSW))
			c.arm(f, f.nIn, f.nOut)
			var got any
			func() {
				defer func() { got = recover() }()
				c.call(r)
			}()
			if fmt.Sprint(got) != fmt.Sprint(c.value) {
				t.Fatalf("recovered %v (%T), want the port's own value %v", got, got, c.value)
			}
			if _, ok := c.value.(error); ok && got != c.value {
				t.Errorf("the error value was changed on its way: %v", got)
			}
			f.inFail, f.outFail, f.onOut = nil, nil, nil
			assertWiped(t, r, "after the panic")
			// The reader is usable as a lost session: the lock is free, and the answer is the same without the port.
			mark := f.mark()
			_, err := r.Transmit(qaCmd, testBudget)
			if !errors.Is(err, ErrSessionLost) || strings.Contains(err.Error(), fmt.Sprint(c.value)) && fmt.Sprint(c.value) != "42" {
				t.Errorf("after the panic: %v", err)
			}
			if after := f.callsAfter(mark); len(after) != 0 {
				t.Errorf("the port was used after the panic: %v", after)
			}
			if err := r.Close(); err != nil || f.count("release") != 1 || !f.closed {
				t.Errorf("Close after the panic: %v, release %d, closed %v", err, f.count("release"), f.closed)
			}
		})
	}
}

// A panic in the very first read of a session (the drain) is the same.
func TestQAPanicInTheDrain(t *testing.T) {
	f := newFake(descSpec{}, cardHandler(echoSW))
	f.onIn = func(*fakeDevice) { panic("the drain read broke") }
	r, err := Open(f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var got any
	func() {
		defer func() { got = recover() }()
		_, _ = r.SlotStatus(testBudget)
	}()
	if got != "the drain read broke" {
		t.Fatalf("recovered %v", got)
	}
	f.onIn = nil
	mark := f.mark()
	if _, err := r.SlotStatus(testBudget); !errors.Is(err, ErrSessionLost) || len(f.callsAfter(mark)) != 0 {
		t.Errorf("after the panic: %v", err)
	}
	if f.nOut != 0 {
		t.Error("something was written")
	}
}

// ---- the drain meets the faults of the port ----

func TestQADrainFaults(t *testing.T) {
	t.Run("a budget that is gone makes no read and keeps the session", func(t *testing.T) {
		f := newFake(descSpec{}, cardHandler(echoSW))
		r, _ := Open(f, Options{})
		mark := f.mark()
		_, err := r.SlotStatus(time.Nanosecond)
		if !errors.Is(err, ErrBudgetExhausted) || errors.Is(err, ErrSessionLost) {
			t.Fatalf("err = %v, want ErrBudgetExhausted alone", err)
		}
		if len(f.callsAfter(mark)) != 0 {
			t.Error("the port was used")
		}
		if _, err := r.SlotStatus(testBudget); err != nil {
			t.Errorf("the next call: %v", err)
		}
	})
	for _, c := range []struct {
		name string
		in   func(got, asked int) int
	}{
		{"more bytes than the buffer", func(_, a int) int { return a + 1 }},
		{"a negative count", func(int, int) int { return -1 }},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(descSpec{}, cardHandler(echoSW))
			f.send(msg(rspDataBlock, 9, stOK, 0, 0, qaResp))
			p := &tweakPort{fakeDevice: f, inN: c.in}
			r, err := Open(p, Options{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = r.SlotStatus(testBudget)
			lostWith(t, err, ErrProtocol)
			if f.nOut != 0 {
				t.Error("something was written")
			}
			untouched(t, r, f, err)
		})
	}
	t.Run("a timeout inside the drain is a quiet pipe, whatever wraps it", func(t *testing.T) {
		f := newFake(descSpec{}, cardHandler(echoSW))
		r, _ := Open(f, Options{})
		if _, err := r.SlotStatus(testBudget); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("the drain of a lost-then-new session after a stall", func(t *testing.T) {
		f := newFake(descSpec{}, cardHandler(echoSW))
		f.inFail = func(int) error { return usbccid.ErrStalled }
		r, _ := Open(f, Options{})
		_, err := r.PowerOn(testBudget)
		lostWith(t, err, usbccid.ErrStalled)
		if f.nOut != 0 {
			t.Error("a message was written after a failed drain")
		}
	})
}

// ---- after the drain ----

// One empty packet that arrives just after the drain (the end of a whole-packet message of an earlier session) is consumed by the first
// read of the session; a second one is a protocol error. Later exchanges get no such allowance.
func TestQAEmptyReadAfterTheDrain(t *testing.T) {
	for _, c := range []struct {
		name   string
		empty  int
		lostOK bool
	}{{"no empty packet", 0, false}, {"one empty packet", 1, false}, {"two empty packets", 2, true}} {
		t.Run(c.name, func(t *testing.T) {
			setSeq(0)
			first := true
			f := newFake(descSpec{}, func(f *fakeDevice, m []byte) {
				if first {
					first = false
					for range c.empty {
						f.sendRaw([]byte{})
					}
				}
				cardHandler(echoSW)(f, m)
			})
			r, err := Open(f, Options{})
			if err != nil {
				t.Fatal(err)
			}
			st, err := r.SlotStatus(testBudget)
			if c.lostOK {
				lostWith(t, err, ErrProtocol)
				assertWiped(t, r, "after the loss")
				untouched(t, r, f, err)
				return
			}
			if err != nil || st != SlotActive {
				t.Fatalf("%v %v", st, err)
			}
		})
	}
	t.Run("not for the second exchange", func(t *testing.T) {
		n := 0
		r, f, err := openFake(Options{}, func(f *fakeDevice, m []byte) {
			n++
			if n == 2 {
				f.sendRaw([]byte{})
			}
			cardHandler(echoSW)(f, m)
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.SlotStatus(testBudget); err != nil {
			t.Fatal(err)
		}
		_, err = r.SlotStatus(testBudget)
		lostWith(t, err, ErrProtocol)
		untouched(t, r, f, err)
	})
}

// A drain read needs a whole DrainReadTimeout (or the transfer timeout, when that is shorter) left in the budget. With less, the call is
// refused with ErrBudgetExhausted before any read, the session is NOT lost (nothing was read or sent), the drain stays pending and
// happens, once, in the next call that has the budget. At the amount itself the call already has microseconds spent, so it is refused
// as well; a few milliseconds above it, the call goes through.
func TestQADrainNeedsAWholeReadInTheBudget(t *testing.T) {
	for _, c := range []struct {
		name string
		xfer time.Duration
		to   time.Duration // the timeout of a drain read
	}{{"default transfer timeout", 0, DrainReadTimeout}, {"short transfer timeout", 5 * time.Millisecond, 5 * time.Millisecond}} {
		for _, b := range []struct {
			name   string
			budget time.Duration
			ok     bool
		}{
			{"1 ns below", c.to - 1, false},
			{"1 ms below", c.to - time.Millisecond, false},
			{"at it", c.to, false},
			{"5 ms above", c.to + 5*time.Millisecond, true},
		} {
			t.Run(c.name+"/"+b.name, func(t *testing.T) {
				f := newFake(descSpec{}, cardHandler(echoSW))
				r, err := Open(f, Options{TransferTimeout: c.xfer})
				if err != nil {
					t.Fatal(err)
				}
				mark := f.mark()
				_, err = r.SlotStatus(b.budget)
				if b.ok {
					if err != nil {
						t.Fatal(err)
					}
					if f.timeouts[0] != c.to {
						t.Errorf("the drain read had a timeout of %v, want %v", f.timeouts[0], c.to)
					}
					return
				}
				if !errors.Is(err, ErrBudgetExhausted) || errors.Is(err, ErrSessionLost) {
					t.Fatalf("err = %v, want ErrBudgetExhausted without ErrSessionLost", err)
				}
				if len(f.callsAfter(mark)) != 0 {
					t.Errorf("the port was used: %v", f.callsAfter(mark))
				}
				if r.drained {
					t.Error("the drain was marked done after a read that was not made")
				}
				// The drain is still pending: it happens before the first write of the next call, once (two quiet windows).
				if _, err := r.SlotStatus(testBudget); err != nil {
					t.Fatalf("the next call: %v", err)
				}
				if got := fmt.Sprint(f.callsAfter(mark)); got != "[in in out in]" {
					t.Errorf("calls of the next call: %s, want two drain reads, then write and read", got)
				}
			})
		}
	}
}

// A shortened drain read proves nothing: with a budget that leaves one whole read for the first, the second read of a drain that
// has more to read is refused when the budget runs short in between, and the session is lost, not called quiet.
func TestQADrainDoesNotCallAShortenedReadQuiet(t *testing.T) {
	f := newFake(descSpec{}, cardHandler(echoSW))
	for i := range 3 {
		f.send(msg(rspDataBlock, byte(i+1), stOK, 0, 0, qaResp))
	}
	f.onIn = func(*fakeDevice) { time.Sleep(15 * time.Millisecond) } // each drain read costs 15 ms of the budget
	r, _ := Open(f, Options{})
	_, err := r.SlotStatus(DrainReadTimeout + 20*time.Millisecond)
	lostWith(t, err, ErrBudgetExhausted)
	if r.drained || f.nOut != 0 {
		t.Errorf("drained %v, writes %d", r.drained, f.nOut)
	}
	assertWiped(t, r, "after the drain that ran out of budget")
}

// A typed nil *usbccid.Device is a nil Port, not a Port that panics at its first call.
func TestQAOpenWithATypedNilDevice(t *testing.T) {
	var d *usbccid.Device
	r, err := Open(d, Options{})
	if r != nil || !errors.Is(err, ErrNotOpen) {
		t.Fatalf("%v %v", r, err)
	}
}
