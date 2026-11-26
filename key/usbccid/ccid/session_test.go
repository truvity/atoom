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

// Tests of the session rules: the shared sequence counter, the drain of the IN pipe, the checks of the
// answer's status, the zero values and a panic of the port.

// ---- the sequence counter ----

// Two Readers alive at once take turns from the one counter: no number is used twice in a window, and each Reader still matches its own.
func TestSequenceCounterIsSharedByReaders(t *testing.T) {
	setSeq(10)
	a, fa := openPowered(t, Options{}, cardHandler(echoSW))
	b, fb := openPowered(t, Options{}, cardHandler(echoSW))
	if fa.outs[0][6] != 11 || fa.outs[1][6] != 12 || fb.outs[0][6] != 13 || fb.outs[1][6] != 14 {
		t.Errorf("sequence numbers: a %d %d, b %d %d", fa.outs[0][6], fa.outs[1][6], fb.outs[0][6], fb.outs[1][6])
	}
	for range 3 {
		if _, err := a.Transmit(apduSelect, testBudget); err != nil {
			t.Fatal(err)
		}
		if _, err := b.Transmit(apduSelect, testBudget); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[byte]bool{}
	for _, m := range append(append([][]byte(nil), fa.outs...), fb.outs...) {
		if seen[m[6]] {
			t.Errorf("sequence number %d used twice", m[6])
		}
		seen[m[6]] = true
	}
}

// A Reader that opens after another began where that one was not: the counter continues.
func TestANewReaderNeverStartsWhereTheOldOneWas(t *testing.T) {
	setSeq(0)
	_, f1 := openPowered(t, Options{}, cardHandler(echoSW))
	_, f2 := openPowered(t, Options{}, cardHandler(echoSW))
	if f1.outs[0][6] != 1 || f2.outs[0][6] != 3 {
		t.Errorf("first numbers of two sessions: %d and %d", f1.outs[0][6], f2.outs[0][6])
	}
}

// The first use seeds the counter at random: the numbers of a fresh process do not all start at 1.
func TestTheCounterIsSeededAtFirstUse(t *testing.T) {
	// Not provable for one run; check the mechanism: the counter moves by one for each call, and wraps.
	setSeq(254)
	if a, b, c := nextSeq(), nextSeq(), nextSeq(); a != 255 || b != 0 || c != 1 {
		t.Errorf("%d %d %d", a, b, c)
	}
}

// ---- the drain ----

// The late answer of an earlier session, with the very number that this session's first Transmit gets, is dropped before anything is
// sent, and the answer of this session is the one returned.
func TestALateAnswerOfAnEarlierSessionIsDrained(t *testing.T) {
	for _, tail := range []string{"short message", "whole packets and an empty packet", "whole packets and no empty packet"} {
		t.Run(tail, func(t *testing.T) {
			setSeq(0) // this session: slot status 1, power on 2, Transmit 3
			f := newFake(descSpec{}, cardHandler(func([]byte) []byte { return []byte{0x90, 0x00} }))
			old := []byte{0x6A, 0x82}
			switch tail {
			case "whole packets and an empty packet":
				old = bytes.Repeat([]byte{0xAB}, 64-10-2)
				f.send(msg(rspDataBlock, 3, stOK, 0, 0, append(old, 0x90, 0)))
			case "whole packets and no empty packet":
				old = bytes.Repeat([]byte{0xAB}, 64-10-2)
				f.noZLP = true
				f.send(msg(rspDataBlock, 3, stOK, 0, 0, append(old, 0x90, 0)))
			default:
				f.send(msg(rspDataBlock, 3, stOK, 0, 0, old))
			}
			f.send(msg(rspDataBlock, 1, stOK, 0, 0, nil)) // and one for the slot status, which would be taken as its answer
			f.send(msg(rspDataBlock, 2, stOK, 0, 0, testATR))
			f.noZLP = false
			r, err := Open(f, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.PowerOn(testBudget); err != nil {
				t.Fatal(err)
			}
			got, err := r.Transmit(apduSelect, testBudget)
			if err != nil || !bytes.Equal(got, []byte{0x90, 0x00}) {
				t.Fatalf("%x %v", got, err)
			}
			if len(f.in) != 0 {
				t.Errorf("%d packets left", len(f.in))
			}
		})
	}
}

// The drain only reads: nothing is written before it, and it runs once, at the first exchange, with a read that is as short as
// DrainReadTimeout.
func TestDrainReadsOnlyOnceAtTheFirstExchange(t *testing.T) {
	f := newFake(descSpec{}, cardHandler(echoSW))
	r, err := Open(f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if f.nIn != 0 || f.nOut != 0 {
		t.Fatalf("Open did I/O: %v", f.calls)
	}
	if _, err := r.SlotStatus(testBudget); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(f.calls[2:6]) != "[in in out in]" {
		t.Errorf("calls after Open: %v, want the two drain reads before the first write", f.calls[2:])
	}
	if f.timeouts[0] != DrainReadTimeout {
		t.Errorf("the drain read has a timeout of %v", f.timeouts[0])
	}
	n := f.nIn
	if _, err := r.SlotStatus(testBudget); err != nil {
		t.Fatal(err)
	}
	if f.nIn != n+1 {
		t.Errorf("the second call read %d times, want one", f.nIn-n)
	}
}

func TestDrainTimeoutIsCutToTheTransferTimeoutAndASmallBudgetIsRefused(t *testing.T) {
	f := newFake(descSpec{}, cardHandler(echoSW))
	r, _ := Open(f, Options{TransferTimeout: 5 * time.Millisecond})
	if _, err := r.SlotStatus(testBudget); err != nil {
		t.Fatal(err)
	}
	if f.timeouts[0] != 5*time.Millisecond {
		t.Errorf("drain read timeout %v", f.timeouts[0])
	}
	f = newFake(descSpec{}, cardHandler(echoSW))
	r, _ = Open(f, Options{})
	// A budget that leaves no room for one whole drain read is no proof of a quiet pipe: the call is refused before its first read,
	// nothing is read or written, the pipe is not marked quiet, and the session is kept.
	_, err := r.SlotStatus(3 * time.Millisecond)
	if !errors.Is(err, ErrBudgetExhausted) || errors.Is(err, ErrSessionLost) {
		t.Errorf("err = %v, want ErrBudgetExhausted and not ErrSessionLost", err)
	}
	if f.nIn != 0 || f.nOut != 0 || r.drained {
		t.Errorf("reads %d, writes %d, drained %v", f.nIn, f.nOut, r.drained)
	}
	if _, err := r.SlotStatus(testBudget); err != nil || !r.drained {
		t.Errorf("the next call: %v, drained %v", err, r.drained)
	}
}

func TestDrainThatNeverGetsQuietLosesTheSession(t *testing.T) {
	f := newFake(descSpec{}, cardHandler(echoSW))
	f.endless = msg(rspDataBlock, 9, stOK, 0, 0, []byte{1, 2})
	r, err := Open(f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.PowerOn(testBudget)
	lostWith(t, err, ErrProtocol)
	if !strings.Contains(err.Error(), "MaxDrainReads") || f.nIn != MaxDrainReads || f.nOut != 0 {
		t.Errorf("err = %v, reads %d, writes %d", err, f.nIn, f.nOut)
	}
	untouched(t, r, f, err)
}

func TestDrainThatTakesTooLongLosesTheSession(t *testing.T) {
	f := newFake(descSpec{}, cardHandler(echoSW))
	f.endless = msg(rspDataBlock, 9, stOK, 0, 0, []byte{1, 2})
	f.onIn = func(*fakeDevice) { time.Sleep(MaxDrainTime/4 + 5*time.Millisecond) }
	r, err := Open(f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.SlotStatus(testBudget)
	lostWith(t, err, ErrProtocol)
	if f.nIn >= MaxDrainReads || f.nOut != 0 {
		t.Errorf("reads %d, writes %d", f.nIn, f.nOut)
	}
}

func TestDrainEndsWithTheBudget(t *testing.T) {
	f := newFake(descSpec{}, cardHandler(echoSW))
	f.endless = msg(rspDataBlock, 9, stOK, 0, 0, []byte{1, 2})
	f.onIn = func(*fakeDevice) { time.Sleep(15 * time.Millisecond) }
	r, _ := Open(f, Options{})
	_, err := r.SlotStatus(40 * time.Millisecond)
	lostWith(t, err, ErrBudgetExhausted)
	if f.nOut != 0 {
		t.Error("something was written")
	}
}

func TestDrainFailuresEndTheSession(t *testing.T) {
	for _, cause := range []error{usbccid.ErrStalled, usbccid.ErrGone, usbccid.ErrClosed, errOverflow} {
		f := newFake(descSpec{}, cardHandler(echoSW))
		f.inFail = func(int) error { return cause }
		r, _ := Open(f, Options{})
		_, err := r.PowerOn(testBudget)
		lostWith(t, err, cause)
		if f.nOut != 0 {
			t.Errorf("%v: something was written", cause)
		}
		untouched(t, r, f, err)
	}
}

// A reader that sent an empty packet after its last message of the earlier session and one that did not: the first message of this session is
// read right in both cases, also when the drain ran in the middle of a stream.
func TestFirstReadAfterTheDrain(t *testing.T) {
	for _, zlp := range []bool{true, false} {
		f := newFake(descSpec{}, cardHandler(echoSW))
		f.noZLP = !zlp
		f.send(msg(rspDataBlock, 77, stOK, 0, 0, bytes.Repeat([]byte{1}, 54))) // a 64-byte message, with its empty packet or not
		r, _ := Open(f, Options{})
		if _, err := r.PowerOn(testBudget); err != nil {
			t.Fatalf("zlp=%v: %v", zlp, err)
		}
		if got, err := r.Transmit(apduSelect, testBudget); err != nil || len(got) != len(apduSelect)+2 {
			t.Fatalf("zlp=%v: %v", zlp, err)
		}
	}
}

// ---- the status of a successful answer ----

func TestSuccessfulAnswerOfAnInactiveCardIsNotAnAnswer(t *testing.T) {
	for _, icc := range []byte{iccInact, iccNone} {
		r, f := openPowered(t, Options{}, func(f *fakeDevice, m []byte) {
			if m[0] == msgXfrBlock {
				f.send(msg(rspDataBlock, m[6], stOK|icc, 0, 0, []byte{0x90, 0}))
				return
			}
			cardHandler(echoSW)(f, m)
		})
		resp, err := r.Transmit(apduSelect, testBudget)
		if !errors.Is(err, ErrNotPowered) || errors.Is(err, ErrSessionLost) || resp != nil {
			t.Fatalf("icc %d: %x %v", icc, resp, err)
		}
		mark := f.mark()
		if _, err := r.Transmit(apduSelect, testBudget); !errors.Is(err, ErrNotPowered) || len(f.callsAfter(mark)) != 0 {
			t.Errorf("a command after it: %v", err)
		}
	}
}

func TestInactiveCardInTheMiddleOfAChainLosesTheSession(t *testing.T) {
	// In the response chain.
	calls := 0
	r, f := openPowered(t, Options{}, func(f *fakeDevice, m []byte) {
		if m[0] != msgXfrBlock {
			cardHandler(echoSW)(f, m)
			return
		}
		calls++
		if calls == 1 {
			f.send(msg(rspDataBlock, m[6], stOK, 0, chainBegin, []byte{1, 2}))
			return
		}
		f.send(msg(rspDataBlock, m[6], stOK|iccInact, 0, chainEnd, []byte{3, 0x90, 0}))
	})
	_, err := r.Transmit(apduSelect, testBudget)
	lostWith(t, err, ErrNotPowered)
	untouched(t, r, f, err)
	// In the command chain.
	r, f = openPowered(t, Options{}, func(f *fakeDevice, m []byte) {
		if m[0] != msgXfrBlock {
			cardHandler(echoSW)(f, m)
			return
		}
		f.send(msg(rspDataBlock, m[6], stOK|iccNone, 0, chainEmptyAck, nil))
	})
	_, err = r.Transmit(bytes.Repeat([]byte{9}, 700), testBudget)
	lostWith(t, err, ErrNotPowered)
	untouched(t, r, f, err)
}

func TestPowerOnWithAnInactiveCardAnswersNoATR(t *testing.T) {
	r, _, err := openFake(Options{}, func(f *fakeDevice, m []byte) {
		switch m[0] {
		case msgGetSlotState:
			f.send(msg(rspSlotStatus, m[6], stOK, 0, 0, nil))
		case msgIccPowerOn:
			f.send(msg(rspDataBlock, m[6], stOK|iccInact, 0, 0, testATR))
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	atr, err := r.PowerOn(testBudget)
	if atr != nil || !errors.Is(err, ErrNotPowered) || errors.Is(err, ErrSessionLost) {
		t.Fatalf("%x %v", atr, err)
	}
	if _, err := r.Transmit(apduSelect, testBudget); !errors.Is(err, ErrNotPowered) {
		t.Error(err)
	}
}

func TestPowerOffThatLeavesTheCardActive(t *testing.T) {
	r, f := openPowered(t, Options{}, func(f *fakeDevice, m []byte) {
		if m[0] == msgIccPowerOff {
			f.send(msg(rspSlotStatus, m[6], stOK|iccPresent, 0, 0, nil))
			return
		}
		cardHandler(echoSW)(f, m)
	})
	err := r.PowerOff(testBudget)
	if !errors.Is(err, ErrStillActive) || errors.Is(err, ErrSessionLost) {
		t.Fatal(err)
	}
	mark := f.mark()
	if _, err := r.Transmit(apduSelect, testBudget); !errors.Is(err, ErrNotPowered) || len(f.callsAfter(mark)) != 0 {
		t.Errorf("a command after it: %v", err)
	}
}

func TestReservedCardStatusIsAProtocolError(t *testing.T) {
	r, f := openPowered(t, Options{}, func(f *fakeDevice, m []byte) {
		if m[0] == msgXfrBlock {
			f.send(msg(rspDataBlock, m[6], stOK|3, 0, 0, []byte{0x90, 0}))
			return
		}
		cardHandler(echoSW)(f, m)
	})
	_, err := r.Transmit(apduSelect, testBudget)
	lostWith(t, err, ErrProtocol)
	untouched(t, r, f, err)
}

func TestFailedSlotStatusOfAnInactiveCardClearsThePowerState(t *testing.T) {
	failing := false
	r, _ := openPowered(t, Options{}, func(f *fakeDevice, m []byte) {
		if m[0] == msgGetSlotState && failing {
			f.send(msg(rspSlotStatus, m[6], stFailed|iccNone, 0xFE, 0, nil))
			return
		}
		cardHandler(echoSW)(f, m)
	})
	failing = true
	if _, err := r.SlotStatus(testBudget); !errors.Is(err, ErrNoCard) || errors.Is(err, ErrSessionLost) {
		t.Fatal(err)
	}
	failing = false
	if _, err := r.Transmit(apduSelect, testBudget); !errors.Is(err, ErrNotPowered) {
		t.Errorf("err = %v", err)
	}
}

// ---- the response ----

func TestResponseShorterThanTwoBytesIsAProtocolError(t *testing.T) {
	for _, n := range []int{0, 1} {
		r, f := openPowered(t, Options{}, cardHandler(func([]byte) []byte { return make([]byte, n) }))
		resp, err := r.Transmit(apduSelect, testBudget)
		lostWith(t, err, ErrProtocol)
		if resp != nil || !strings.Contains(err.Error(), fmt.Sprintf("%d bytes", n)) {
			t.Errorf("%d bytes: %x %v", n, resp, err)
		}
		untouched(t, r, f, err)
	}
	// Two bytes is a response.
	r, _ := openPowered(t, Options{}, cardHandler(func([]byte) []byte { return []byte{0x6A, 0x82} }))
	if got, err := r.Transmit(apduSelect, testBudget); err != nil || !bytes.Equal(got, []byte{0x6A, 0x82}) {
		t.Errorf("%x %v", got, err)
	}
	// Also over a chain: the whole is what counts.
	cc := &chainCard{respParts: [][]byte{{0x01}, {0x90}}}
	r, _ = openPowered(t, Options{}, cc.handler())
	if got, err := r.Transmit(apduSelect, testBudget); err != nil || !bytes.Equal(got, []byte{0x01, 0x90}) {
		t.Errorf("%x %v", got, err)
	}
}

func TestFirstBlockThatEndsAChainIsAProtocolError(t *testing.T) {
	for _, chain := range []byte{chainEnd, chainMiddle, chainEmptyAck} {
		r, f := openPowered(t, Options{}, func(f *fakeDevice, m []byte) {
			if m[0] == msgXfrBlock {
				f.send(msg(rspDataBlock, m[6], stOK, 0, chain, []byte{0x90, 0}))
				return
			}
			cardHandler(echoSW)(f, m)
		})
		resp, err := r.Transmit(apduSelect, testBudget)
		lostWith(t, err, ErrProtocol)
		if resp != nil || !strings.Contains(err.Error(), "first response block") {
			t.Errorf("chain %#x: %x %v", chain, resp, err)
		}
		untouched(t, r, f, err)
	}
}

// ---- numbers from the device ----

func TestMessageLengthFromTheDeviceIsHeldBeforeItIsConverted(t *testing.T) {
	for _, declared := range []uint32{0x7FFFFFFF, 0x80000000, 0xFFFFFFFF, 65536, 65537} {
		f := newFake(descSpec{maxMsg: declared}, cardHandler(echoSW))
		r, err := Open(f, Options{})
		if err != nil {
			t.Fatalf("%#x: %v", declared, err)
		}
		if got := r.Info().MaxMessageLength; got != MaxMessageLength {
			t.Errorf("%#x: message length %d", declared, got)
		}
	}
}

// ---- a panic of the port, and the zero values ----

func TestPanicOfThePortLosesTheSessionAndGoesOn(t *testing.T) {
	r, f := openPowered(t, Options{}, cardHandler(echoSW))
	f.onIn = func(*fakeDevice) { panic("the port broke") }
	func() {
		defer func() {
			if p := recover(); p != "the port broke" {
				t.Errorf("recovered %v, want the port's own value", p)
			}
		}()
		_, _ = r.Transmit(apduSelect, testBudget)
	}()
	f.onIn = nil
	r.mu.Lock() // the lock was released by the unwinding: this would block for ever otherwise
	lost := r.lost
	r.mu.Unlock()
	if !errors.Is(lost, ErrSessionLost) {
		t.Fatalf("lost = %v", lost)
	}
	if strings.Contains(lost.Error(), "the port broke") {
		t.Error("the panic value is in the error text")
	}
	untouched(t, r, f, lost)
}

func TestZeroValuesAndNilsAreNamedErrors(t *testing.T) {
	var nilReader *Reader
	for name, r := range map[string]*Reader{"nil": nilReader, "zero": {}} {
		if _, err := r.SlotStatus(testBudget); !errors.Is(err, ErrNotOpen) {
			t.Errorf("%s SlotStatus: %v", name, err)
		}
		if _, err := r.PowerOn(testBudget); !errors.Is(err, ErrNotOpen) {
			t.Errorf("%s PowerOn: %v", name, err)
		}
		if err := r.PowerOff(testBudget); !errors.Is(err, ErrNotOpen) {
			t.Errorf("%s PowerOff: %v", name, err)
		}
		if _, err := r.Transmit(apduSelect, testBudget); !errors.Is(err, ErrNotOpen) {
			t.Errorf("%s Transmit: %v", name, err)
		}
		if got := r.Info(); got != (Info{}) {
			t.Errorf("%s Info: %+v", name, got)
		}
		if err := r.Close(); err != nil {
			t.Errorf("%s Close: %v", name, err)
		}
	}
	// A zero Reader that was closed says closed, as every other.
	z := &Reader{}
	_ = z.Close()
	if _, err := z.Transmit(apduSelect, testBudget); !errors.Is(err, usbccid.ErrClosed) {
		t.Errorf("closed zero Reader: %v", err)
	}
	if r, err := Open(nil, Options{}); r != nil || !errors.Is(err, ErrNotOpen) {
		t.Errorf("Open(nil): %v %v", r, err)
	}
}

func TestOpenWithACandidateOfAnotherDevice(t *testing.T) {
	for _, c := range []usbccid.Candidate{
		{VendorID: 0x7778, ProductID: 1, Interface: 2, BulkOut: 0x02, BulkIn: 0x82},
		{VendorID: 0x7777, ProductID: 9, Interface: 2, BulkOut: 0x02, BulkIn: 0x82},
	} {
		f := newFake(descSpec{}, nil)
		r, err := Open(f, Options{Candidate: &c})
		if r != nil || !errors.Is(err, usbccid.ErrWrongDevice) || !f.closed || f.count("claim") != 0 {
			t.Errorf("err=%v calls=%v", err, f.calls)
		}
	}
}

// An empty packet that arrives just after the drain (the end of a whole-packet message of an earlier session) is consumed by the first
// read, and does not break the session.
func TestEmptyPacketRightAfterTheDrainIsConsumed(t *testing.T) {
	first := true
	r, _, err := openFake(Options{}, func(f *fakeDevice, m []byte) {
		if m[0] == msgGetSlotState && first {
			first = false
			f.sendRaw([]byte{}, msg(rspSlotStatus, m[6], stOK, 0, 0, nil))
			return
		}
		cardHandler(echoSW)(f, m)
	})
	if err != nil {
		t.Fatal(err)
	}
	if st, err := r.SlotStatus(testBudget); err != nil || st != SlotActive {
		t.Fatal(st, err)
	}
	// Only one is consumed: the later exchanges find none (the fake sends none).
	if _, err := r.PowerOn(testBudget); err != nil {
		t.Fatal(err)
	}
}

// A typed nil *usbccid.Device is a nil Port.
func TestOpenWithATypedNilDevice(t *testing.T) {
	var d *usbccid.Device
	if r, err := Open(d, Options{}); r != nil || !errors.Is(err, ErrNotOpen) {
		t.Errorf("%v %v", r, err)
	}
}

// Every read of the drain asks for a whole number of packets, and the bytes of the answer to reset ask in whole packets too.
func TestDrainReadsAskForWholePackets(t *testing.T) {
	f := newFake(descSpec{maxMsg: 300}, cardHandler(echoSW))
	r, err := Open(f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.SlotStatus(testBudget); err != nil {
		t.Fatal(err)
	}
	if len(f.inLens) < 2 || f.inLens[0] != 320 || f.inLens[1] != 320 { // 300+64 bytes held, 320 of them a whole number of 64
		t.Errorf("reads of the drain: %v, want 320 bytes each", f.inLens)
	}
}

// One read that times out is not proof of quiet: a message that was half on its way arrives after it, and the count of reads in a
// row that time out starts again at any read that returns bytes.
func TestDrainNeedsTwoTimedOutReadsInARow(t *testing.T) {
	f := newFake(descSpec{}, cardHandler(echoSW))
	f.onIn = func(f *fakeDevice) {
		if f.nIn == 2 { // the tail that follows the first timed-out read
			f.queueLocked(msg(rspSlotStatus, 9, stOK, 0, 0, nil))
		}
	}
	r, err := Open(f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.SlotStatus(testBudget); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(f.calls[2:7]); got != "[in in in in out]" {
		t.Errorf("calls after Open: %v, want timeout, tail, two timeouts, then the write", f.calls[2:])
	}
}

// A read of the drain waits no longer than what is left of MaxDrainTime.
func TestDrainReadIsCutToWhatIsLeftOfMaxDrainTime(t *testing.T) {
	f := newFake(descSpec{}, cardHandler(echoSW))
	f.endless = msg(rspDataBlock, 9, stOK, 0, 0, []byte{1, 2})
	f.onIn = func(*fakeDevice) { time.Sleep(MaxDrainTime/2 - 5*time.Millisecond) }
	r, err := Open(f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.SlotStatus(testBudget)
	lostWith(t, err, ErrProtocol)
	if f.nOut != 0 {
		t.Error("something was written")
	}
	// A third read can only start after 2 x 120 ms, when less than DrainReadTimeout of MaxDrainTime is left.
	for i, to := range f.timeouts {
		if to <= 0 || to > DrainReadTimeout {
			t.Errorf("read %d has a timeout of %v", i, to)
		}
	}
	if n := len(f.timeouts); n >= 3 && f.timeouts[n-1] >= DrainReadTimeout {
		t.Errorf("the last read, which started after %v, has a timeout of %v", 2*(MaxDrainTime/2-5*time.Millisecond), f.timeouts[n-1])
	}
}

// A second Open on a Port that a Reader holds is refused with ErrAlreadyClaimed, and leaves the Port and its Reader as they were.
func TestSecondOpenOnTheSamePortLeavesTheFirstReaderAlone(t *testing.T) {
	f := newFake(descSpec{}, cardHandler(echoSW))
	f.refuseSecondClaim = true
	r1, err := Open(f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := Open(f, Options{})
	if r2 != nil || !errors.Is(err, usbccid.ErrAlreadyClaimed) {
		t.Fatalf("second Open: %v, %v", r2, err)
	}
	if f.closed {
		t.Fatal("the failed Open closed the Port of the first Reader")
	}
	if _, err := r1.PowerOn(testBudget); err != nil {
		t.Errorf("the first Reader after the second Open: %v", err)
	}
}
