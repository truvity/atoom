package ccid

import (
	"bytes"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/truvity/atoom/key/usbccid"
)

// G. The card-level errors, I. power, J. concurrency.

// wantSentinel is the sentinel that each bError maps to, written out from the doc of errors.go and CCID 1.1 table 6.2-2 (the names).
func wantSentinel(code uint8) error {
	switch {
	case code == 0x00:
		return ErrCommandNotSupported
	case code <= 0x7F:
		return ErrBadParameter
	case code == 0xFE:
		return ErrCardMute
	case code == 0xFD:
		return ErrCardParity
	case code >= 0xF3 && code <= 0xF8:
		return ErrCardProtocol
	case code == 0xE0 || code == 0xF2:
		return ErrSlotBusy
	case code == 0xFB || code == 0xFC:
		return ErrReaderFault
	}
	return nil
}

var allSentinels = []error{ErrCardMute, ErrCardParity, ErrCardProtocol, ErrSlotBusy, ErrReaderFault, ErrCommandNotSupported, ErrBadParameter}

// Every code of bError, with every card status, through every call that can fail: the sentinel is the one the doc names (and only
// that one), ErrCommandFailed always, ErrNoCard exactly when the card status says no card, the status bytes are in *CommandError, the
// text names the call and holds no APDU byte, and the session is not lost.
func TestQAEveryErrorCodeThroughEveryCall(t *testing.T) {
	for _, op := range qaOps {
		t.Run(op.name, func(t *testing.T) {
			armed := false
			code, icc := byte(0), byte(0)
			r, f := openPowered(t, Options{}, func(f *fakeDevice, m []byte) {
				if armed && m[0] == op.typ {
					f.send(msg(rspFor(m[0]), m[6], stFailed|icc, code, 0x10, qaResp))
					return
				}
				cardHandler(echoSW)(f, m)
			})
			for c := range 256 {
				code = byte(c)
				for icc = 0; icc <= 2; icc++ {
					armed = true
					err := op.call(r, testBudget)
					armed = false
					var ce *CommandError
					if !errors.As(err, &ce) || ce.Code != code || ce.ICCStatus != icc {
						t.Fatalf("code %#x icc %d: err = %v", code, icc, err)
					}
					wantOp := map[byte]string{msgGetSlotState: "GetSlotStatus", msgIccPowerOn: "IccPowerOn", msgIccPowerOff: "IccPowerOff", msgXfrBlock: "XfrBlock"}[op.typ]
					if ce.Op != wantOp {
						t.Errorf("Op = %q, want %q", ce.Op, wantOp)
					}
					if errors.Is(err, ErrSessionLost) || !errors.Is(err, ErrCommandFailed) || errors.Is(err, ErrNoCard) != (icc == 2) {
						t.Fatalf("code %#x icc %d: %v", code, icc, err)
					}
					want := wantSentinel(code)
					for _, s := range allSentinels {
						isWant := want != nil && errors.Is(s, want)
						if errors.Is(err, s) != isWant {
							t.Errorf("code %#x: errors.Is(%v) = %v", code, s, !isWant)
						}
					}
					text := err.Error()
					if !strings.Contains(text, wantOp) || !strings.Contains(text, fmt.Sprintf("0x%02X", code)) || !strings.Contains(text, fmt.Sprintf("ICC status %d", icc)) {
						t.Errorf("text %q", text)
					}
					assertNoLeak(t, "failed", err, qaCmd, qaResp)
					assertWiped(t, r, "after the failure")
					// The next call is in order. Bring the card back when the failure took it away.
					if _, err := r.PowerOn(testBudget); err != nil {
						t.Fatalf("PowerOn after code %#x icc %d: %v", code, icc, err)
					}
				}
			}
			_ = f
		})
	}
}

// A failed answer that leaves the card unpowered makes the next Transmit ErrNotPowered, without a port call; a failed answer with an
// active card does not.
func TestQAFailureThatLeavesTheCardUnpowered(t *testing.T) {
	for _, op := range []opCase{qaOps[2], qaOps[4]} { // IccPowerOn and XfrBlock
		for _, icc := range []byte{0, 1, 2} {
			t.Run(fmt.Sprintf("%s/icc%d", op.name, icc), func(t *testing.T) {
				armed := false
				r, f := openPowered(t, Options{}, func(f *fakeDevice, m []byte) {
					if armed && m[0] == op.typ {
						f.send(msg(rspFor(m[0]), m[6], stFailed|icc, 0xFE, 0, nil))
						return
					}
					cardHandler(echoSW)(f, m)
				})
				armed = true
				if err := op.call(r, testBudget); !errors.Is(err, ErrCardMute) {
					t.Fatal(err)
				}
				armed = false
				mark := f.mark()
				_, err := r.Transmit(qaCmd, testBudget)
				// A failed IccPowerOn leaves the card unpowered whatever its status; a failed XfrBlock only when the status says so.
				wantOff := icc != 0 || op.typ == msgIccPowerOn
				if wantOff != errors.Is(err, ErrNotPowered) {
					t.Errorf("Transmit after the failure: %v, want not powered = %v", err, wantOff)
				}
				if wantOff && len(f.callsAfter(mark)) != 0 {
					t.Error("a command went to a card that is not powered")
				}
			})
		}
	}
}

// ---- power ----

func TestQAPowerLifecycle(t *testing.T) {
	atrs := [][]byte{testATR, {0x3B, 0x00}, bytes.Repeat([]byte{0x3B}, MaxATR)}
	i := 0
	r, f, err := openFake(Options{}, func(f *fakeDevice, m []byte) {
		switch m[0] {
		case msgIccPowerOn:
			f.send(msg(rspDataBlock, m[6], stOK, 0, 0, atrs[i%len(atrs)]))
			i++
		default:
			cardHandler(echoSW)(f, m)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	// Before PowerOn: nothing is sent to the card.
	if _, err := r.Transmit(apduSelect, testBudget); !errors.Is(err, ErrNotPowered) {
		t.Fatalf("Transmit before PowerOn: %v", err)
	}
	if f.nOut != 0 {
		t.Fatal("a message was sent before PowerOn")
	}
	for round := range 6 {
		got, err := r.PowerOn(testBudget)
		if err != nil || !bytes.Equal(got, atrs[round%len(atrs)]) {
			t.Fatalf("round %d: PowerOn %x %v", round, got, err)
		}
		if _, err := r.Transmit(apduSelect, testBudget); err != nil {
			t.Fatalf("round %d: Transmit after PowerOn: %v", round, err)
		}
		if round%2 == 0 { // a second PowerOn of a powered card resets it and gives the answer again
			if got, err := r.PowerOn(testBudget); err != nil || len(got) < 2 {
				t.Fatalf("round %d: second PowerOn: %x %v", round, got, err)
			}
			i = round + 1
		}
		if err := r.PowerOff(testBudget); err != nil {
			t.Fatalf("round %d: PowerOff: %v", round, err)
		}
		mark := f.mark()
		if _, err := r.Transmit(apduSelect, testBudget); !errors.Is(err, ErrNotPowered) || len(f.callsAfter(mark)) != 0 {
			t.Fatalf("round %d: Transmit after PowerOff: %v", round, err)
		}
		if err := r.PowerOff(testBudget); err != nil { // off twice is not refused
			t.Fatalf("round %d: second PowerOff: %v", round, err)
		}
	}
}

// PowerOn asks the slot first. An empty slot is ErrNoCard and nothing more is sent; an inactive or an active card is sent IccPowerOn.
func TestQAPowerOnAsksTheSlotFirst(t *testing.T) {
	for _, c := range []struct {
		icc   byte
		sends []byte
		err   error
	}{{iccPresent, []byte{msgGetSlotState, msgIccPowerOn}, nil}, {iccInact, []byte{msgGetSlotState, msgIccPowerOn}, nil}, {iccNone, []byte{msgGetSlotState}, ErrNoCard}} {
		r, f, err := openFake(Options{}, func(f *fakeDevice, m []byte) {
			if m[0] == msgGetSlotState {
				f.send(msg(rspSlotStatus, m[6], stOK|c.icc, 0, 0, nil))
				return
			}
			cardHandler(echoSW)(f, m)
		})
		if err != nil {
			t.Fatal(err)
		}
		atr, err := r.PowerOn(testBudget)
		if !errors.Is(err, c.err) || (c.err == nil) != (atr != nil) || errors.Is(err, ErrSessionLost) {
			t.Errorf("icc %d: %x %v", c.icc, atr, err)
		}
		if !bytes.Equal(f.outTypes(), c.sends) {
			t.Errorf("icc %d: sent % x, want % x", c.icc, f.outTypes(), c.sends)
		}
	}
}

// The answer to reset is a copy, 2 to MaxATR bytes; outside that, ErrBadATR with the card powered on but not usable here, the session
// not lost, and PowerOff and a new PowerOn working.
func TestQAATRLengths(t *testing.T) {
	for _, n := range []int{0, 1, 2, 3, MaxATR - 1, MaxATR, MaxATR + 1, MaxATR + 2, 255} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			good := n >= 2 && n <= MaxATR
			size := n
			r, f, err := openFake(Options{}, func(f *fakeDevice, m []byte) {
				if m[0] == msgIccPowerOn {
					f.send(msg(rspDataBlock, m[6], stOK, 0, 0, pattern(size, 5)))
					return
				}
				cardHandler(echoSW)(f, m)
			})
			if err != nil {
				t.Fatal(err)
			}
			atr, err := r.PowerOn(testBudget)
			if good {
				if err != nil || !bytes.Equal(atr, pattern(n, 5)) {
					t.Fatalf("%x %v", atr, err)
				}
				assertWiped(t, r, "after PowerOn")
				if _, err := r.Transmit(apduSelect, testBudget); err != nil {
					t.Error(err)
				}
				return
			}
			if !errors.Is(err, ErrBadATR) || errors.Is(err, ErrSessionLost) || atr != nil || !strings.Contains(err.Error(), fmt.Sprint(n)) {
				t.Fatalf("%x %v", atr, err)
			}
			assertNoLeak(t, "ATR", err, pattern(n, 5))
			mark := f.mark()
			if _, err := r.Transmit(apduSelect, testBudget); !errors.Is(err, ErrNotPowered) || len(f.callsAfter(mark)) != 0 {
				t.Errorf("Transmit: %v", err)
			}
			if err := r.PowerOff(testBudget); err != nil {
				t.Errorf("PowerOff: %v", err)
			}
			size = 14
			if _, err := r.PowerOn(testBudget); err != nil {
				t.Errorf("PowerOn again: %v", err)
			}
		})
	}
}

// ---- concurrency ----

// The doc promises that the methods are safe to call from several goroutines and take turns. Under -race, goroutines mix every call,
// and the device checks that it never meets a new message while an answer of the last one is unread.
func TestQAConcurrentCallsTakeTurns(t *testing.T) {
	var overlaps atomic.Int32
	echo100 := func(cmd []byte) []byte { return append(append([]byte(nil), cmd[:min(len(cmd), 100)]...), 0x90, 0x00) }
	r, f := openPowered(t, Options{}, func(f *fakeDevice, m []byte) {
		for _, p := range f.in {
			if len(p) != 0 {
				overlaps.Add(1) // a message arrived while an answer of the last one is still unread
				break
			}
		}
		if m[0] == msgXfrBlock {
			switch lvl := uint16(m[8]) | uint16(m[9])<<8; lvl {
			case lvlBegin, lvlMiddle:
				f.send(msg(rspDataBlock, m[6], stOK, 0, chainEmptyAck, nil))
			case lvlEnd:
				f.send(msg(rspDataBlock, m[6], stOK, 0, chainWhole, []byte{0x90, 0x00}))
			default:
				f.send(msg(rspDataBlock, m[6], stOK, 0, chainWhole, echo100(m[10:])))
			}
			return
		}
		cardHandler(echoSW)(f, m)
	})
	var wg sync.WaitGroup
	var bad atomic.Value
	for g := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 60 {
				var err error
				switch (g + i) % 5 {
				case 0:
					_, err = r.SlotStatus(testBudget)
				case 1:
					cmd := bytes.Repeat([]byte{byte(g & 0xFF)}, 8+i)
					var got []byte
					got, err = r.Transmit(cmd, testBudget)
					if err == nil && !bytes.Equal(got[:len(cmd)], cmd) {
						err = fmt.Errorf("goroutine %d got another goroutine's answer: %x", g, got)
					}
				case 2:
					long := bytes.Repeat([]byte{byte(g & 0xFF)}, 300)
					var got []byte
					got, err = r.Transmit(long, testBudget) // chained: three messages that nobody may break into
					_ = got
				case 3:
					if i%10 == 3 {
						err = r.PowerOff(testBudget)
						if err == nil {
							_, err = r.PowerOn(testBudget)
						}
					}
				case 4:
					_ = r.Info()
				}
				if err != nil && !errors.Is(err, ErrNotPowered) && !errors.Is(err, ErrAPDUTooLong) {
					bad.Store(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if e := bad.Load(); e != nil {
		t.Fatal(e)
	}
	if overlaps.Load() != 0 {
		t.Errorf("%d messages went out while an answer was unread", overlaps.Load())
	}
	_ = f
}

// parkedOnTheReaderLock counts the goroutines that are parked on a mutex inside the method of a Reader called method (and not inside
// the Port): the stack of such a goroutine names the mutex wait in its first line and the method in its frames.
func parkedOnTheReaderLock(method string) int {
	buf := make([]byte, 1<<16)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	count := 0
	for _, g := range strings.Split(string(buf), "\n\n") {
		head, _, _ := strings.Cut(g, "\n")
		if strings.Contains(head, "Mutex") && strings.Contains(g, "(*Reader)."+method+"(") && !strings.Contains(g, "fakeDevice") {
			count++
		}
	}
	return count
}

// Close waits for the call in progress, and the call that follows it is refused as closed; no call reaches the port after Close.
func TestQACloseWaitsForTheCallInProgress(t *testing.T) {
	r, f := openPowered(t, Options{}, cardHandler(echoSW))
	entered, gate := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.onIn = func(*fakeDevice) {
		once.Do(func() { close(entered) })
		<-gate
	}
	transmit := make(chan error, 1)
	go func() { _, err := r.Transmit(apduSelect, testBudget); transmit <- err }()
	<-entered
	closed, second := make(chan error, 1), make(chan error, 1)
	go func() { closed <- r.Close() }()
	go func() { _, err := r.Transmit(apduSelect, testBudget); second <- err }()
	// Neither Close nor the second call may return while the call in progress holds the Reader: both wait for its lock. The test does not
	// wait for a length of time: it yields until a goroutine is parked on that lock inside Close and another inside Transmit (as the
	// stacks of the process show), and fails at once if either call has returned instead. A call that did not wait is never parked, so
	// it has returned, or the bounded loop runs out.
	for round := 0; ; round++ {
		select {
		case e := <-closed:
			t.Fatalf("Close returned while a call was in progress: %v", e)
		case e := <-second:
			t.Fatalf("a second call ran while a call was in progress: %v", e)
		default:
		}
		if parkedOnTheReaderLock("Close") > 0 && parkedOnTheReaderLock("Transmit") > 0 {
			break
		}
		if round == 10000 {
			t.Fatal("Close and the second call neither returned nor waited for the lock of the Reader: no goroutine whose stack header names a Mutex wait inside (*Reader).Close and (*Reader).Transmit; if the Go release changed the stack format, update parkedOnTheReaderLock")
		}
		for range 100 {
			runtime.Gosched()
		}
	}
	close(gate)
	if err := <-transmit; err != nil {
		t.Errorf("the call in progress: %v", err)
	}
	if err := <-closed; err != nil {
		t.Errorf("Close: %v", err)
	}
	// The second call either ran before Close (and succeeded) or after it (and was refused as closed).
	if err := <-second; err != nil && !errors.Is(err, usbccid.ErrClosed) {
		t.Errorf("second call: %v", err)
	}
	f.mu.Lock()
	callsAtClose := len(f.calls)
	f.mu.Unlock()
	_, _ = r.Transmit(apduSelect, testBudget)
	_, _ = r.PowerOn(testBudget)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != callsAtClose {
		t.Error("the port was used after Close")
	}
	if f.calls[len(f.calls)-1] != "close" {
		t.Errorf("the last call on the port is %s", f.calls[len(f.calls)-1])
	}
}

// A session that is lost under load: every goroutine sees the lost session, and the port is not touched after the loss (one message for
// each exchange that succeeded, and one for the one that failed).
func TestQAConcurrentLossIsSeenByAll(t *testing.T) {
	r, f := openPowered(t, Options{}, cardHandler(echoSW))
	baseOut := f.nOut
	var okCount atomic.Int32
	f.inFail = func(n int) error {
		if n >= 25 {
			return usbccid.ErrStalled
		}
		return nil
	}
	var wg sync.WaitGroup
	var lostCount atomic.Int32
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 30 {
				_, err := r.Transmit(apduSelect, testBudget)
				switch {
				case err == nil:
					okCount.Add(1)
				case errors.Is(err, ErrSessionLost) && errors.Is(err, usbccid.ErrStalled):
					lostCount.Add(1)
				default:
					t.Errorf("err = %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if lostCount.Load() == 0 {
		t.Fatal("never lost")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if got, want := f.nOut-baseOut, int(okCount.Load())+1; got != want {
		t.Errorf("%d messages went out, want %d: one for each success and the one that was lost; a call reached the port after the loss", got, want)
	}
}
