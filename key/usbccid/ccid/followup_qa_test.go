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

// Every read is a whole number of packets, the drain needs two quiet windows and is bounded in time, one Port holds one Reader, and
// two further cases: the budget that runs out between the header and the body, and a Port that counts too many bytes.

// ---- rule 1: whole packets ----

// packetRule is the fake Port with the rule of usbfs that matters here: a read request that is not a whole number of packets can end
// in an overflow error when a full packet arrives and does not fit the rest of the request. It refuses such a request, and records every
// length that was asked.
type packetRule struct {
	*fakeDevice
	mps  int
	lens []int
	bad  []int
}

func (p *packetRule) BulkIn(ep uint8, b []byte, to time.Duration) (int, error) {
	p.lens = append(p.lens, len(b))
	if len(b)%p.mps != 0 {
		p.bad = append(p.bad, len(b))
		return 0, fmt.Errorf("fake: bulk transfer on endpoint 0x%02X: %w", ep, errOverflow)
	}
	return p.fakeDevice.BulkIn(ep, b, to)
}

func TestQAEveryReadIsAWholeNumberOfPackets(t *testing.T) {
	const maxMsg = 5000
	for _, mps := range []uint16{8, 16, 32, 64, 512, 1024} {
		var totals []int
		for k := 1; k <= 3; k++ {
			for _, d := range []int{-1, 0, 1} {
				if tot := k*int(mps) + d; tot >= 12 && tot <= maxMsg {
					totals = append(totals, tot)
				}
			}
		}
		for _, noZLP := range []bool{false, true} {
			t.Run(fmt.Sprintf("packet %d, empty packet withheld %v", mps, noZLP), func(t *testing.T) {
				for _, t1 := range totals {
					for _, t2 := range totals {
						setSeq(0)
						var want []byte
						f := newFake(descSpec{mpsIn: mps, mpsOut: mps, maxMsg: maxMsg}, func(f *fakeDevice, m []byte) {
							f.noZLP = noZLP
							if m[0] != msgXfrBlock {
								cardHandler(echoSW)(f, m)
								return
							}
							// A time extension of t1 bytes and the answer of t2 bytes, back to back.
							f.send(msg(rspDataBlock, m[6], stTimeExt, 1, 0, pattern(t1-10, 1)))
							f.send(msg(rspDataBlock, m[6], stOK, 0, 0, want))
						})
						// Two messages of earlier sessions, back to back, in front of the first call.
						f.noZLP = noZLP
						f.send(msg(rspDataBlock, 200, stOK, 0, 0, pattern(t1-10, 2)))
						f.send(msg(rspDataBlock, 201, stOK, 0, 0, pattern(t2-10, 3)))
						p := &packetRule{fakeDevice: f, mps: int(mps)}
						r, err := Open(p, Options{})
						if err != nil {
							t.Fatal(err)
						}
						if _, err := r.PowerOn(testBudget); err != nil {
							t.Fatalf("drain and PowerOn with messages of %d and %d bytes: %v", t1, t2, err)
						}
						want = pattern(t2-10, 4)
						got, err := r.Transmit(apduSelect, testBudget)
						if err != nil || !bytes.Equal(got, want) {
							t.Fatalf("time extension of %d bytes, answer of %d: %d bytes, %v", t1, t2, len(got), err)
						}
						if len(p.bad) != 0 {
							t.Fatalf("requests of %v bytes at packet size %d", p.bad, mps)
						}
						for _, n := range p.lens {
							if n > len(r.rbuf) || n > MaxMessageLength {
								t.Fatalf("a request of %d bytes: the buffer holds %d", n, len(r.rbuf))
							}
						}
					}
				}
			})
		}
	}
}

// ---- rule 2: the drain needs two quiet windows ----

// A drain of 0 to 7 items (messages) with an empty packet at each position, or none: n items and the two quiet windows are n+2 reads,
// and eight reads are all the drain makes.
func TestQADrainReadPatternForEveryNumberOfItems(t *testing.T) {
	for n := 0; n <= 7; n++ {
		for pos := -1; pos < n; pos++ {
			t.Run(fmt.Sprintf("%d items, empty packet at %d", n, pos), func(t *testing.T) {
				setSeq(0)
				f := newFake(descSpec{}, cardHandler(echoSW))
				for i := range n {
					if i == pos {
						f.sendRaw([]byte{})
					} else {
						f.sendRaw(msg(rspDataBlock, byte(i+1), stOK, 0, 0, qaResp))
					}
				}
				r, err := Open(f, Options{})
				if err != nil {
					t.Fatal(err)
				}
				st, err := r.SlotStatus(testBudget)
				if n+2 <= MaxDrainReads {
					if err != nil || st != SlotActive {
						t.Fatalf("%v %v", st, err)
					}
					want := strings.Repeat("in ", n+2) + "out in"
					if got := strings.Join(f.calls[2:], " "); got != want {
						t.Errorf("calls %q, want %q", got, want)
					}
					// The drain ran once: the next call reads once.
					before := f.nIn
					if _, err := r.SlotStatus(testBudget); err != nil || f.nIn != before+1 {
						t.Errorf("the second call: %v, %d reads", err, f.nIn-before)
					}
					return
				}
				lostWith(t, err, ErrProtocol)
				if f.nIn != MaxDrainReads || f.nOut != 0 {
					t.Errorf("%d reads, %d writes", f.nIn, f.nOut)
				}
			})
		}
	}
}

// One timeout is not a quiet pipe: timeout, item, timeout, timeout is done after four reads, and the item is dropped. A late answer
// that arrives in the second window is discarded and never returned, and the drain runs before the first write, once.
func TestQADrainNeedsTwoTimeoutsInARow(t *testing.T) {
	t.Run("a timeout between two items", func(t *testing.T) {
		setSeq(0)
		f := newFake(descSpec{}, cardHandler(echoSW))
		f.sendRaw(msg(rspSlotStatus, 1, stOK|iccInact, 0, 0, nil)) // the number of the first message, and a state that is not the real one
		f.inFail = func(n int) error {
			if n == 1 {
				return usbccid.ErrTimeout
			}
			return nil
		}
		r, _ := Open(f, Options{})
		st, err := r.SlotStatus(testBudget)
		if err != nil || st != SlotActive {
			t.Fatalf("%v %v: the single timeout ended the drain, and the late answer was taken", st, err)
		}
		if got := strings.Join(f.calls[2:], " "); got != "in in in in out in" {
			t.Errorf("calls %q", got)
		}
	})
	t.Run("a late answer in the second window", func(t *testing.T) {
		setSeq(0)
		f := newFake(descSpec{}, cardHandler(echoSW))
		f.inFail = func(n int) error {
			if n == 1 {
				return usbccid.ErrTimeout
			}
			return nil
		}
		f.onIn = func(f *fakeDevice) {
			if f.nIn == 2 {
				f.sendRaw(msg(rspSlotStatus, 1, stOK|iccInact, 0, 0, nil)) // arrives in the second window
			}
		}
		r, _ := Open(f, Options{})
		st, err := r.SlotStatus(testBudget)
		if err != nil || st != SlotActive {
			t.Fatalf("%v %v", st, err)
		}
		if f.calls[len(f.calls)-2] != "out" || strings.Count(strings.Join(f.calls[2:], " "), "in") != 5 {
			t.Errorf("calls %v", f.calls[2:])
		}
		if len(f.in) != 0 {
			t.Errorf("%d packets left", len(f.in))
		}
	})
}

// ---- rule 3: the drain is bounded in time ----

// readLog records, for each drain read, the timeout it was given and a LOWER bound of the time the drain had spent when the read was
// begun: the time from the start of the first read to the end of the previous read's hook, both measured by the fake itself. The drain
// reads its own clock before it hands the read to the Port, so what it had spent is never less than that bound, however slow the
// machine is: a slow machine only makes the true figure larger, so every check below that holds at the bound holds at any speed.
//
// What the bound cannot give is the exact figure (how much was left when a read began): that needs a clock that the test controls,
// which the Reader does not have (see TestQADrainTimeIsBounded).
type readLog struct {
	to    []time.Duration // the timeout of each read
	floor []time.Duration // at least this much of MaxDrainTime was spent when the read began
	first time.Time       // the start of the first read: after the drain began
	last  time.Time       // the end of the hook of the previous read: before the drain looked at its clock again
}

// hook returns the onIn hook. plan says how long the fake keeps read k (counted from 0), given the time since the first read began. A
// read that has nothing to return takes no time: the fake does not wait out the timeout it is given.
func (l *readLog) hook(f *fakeDevice, plan func(k int, since time.Duration) time.Duration) func(*fakeDevice) {
	return func(*fakeDevice) {
		now := time.Now()
		if len(l.to) == 0 {
			l.first, l.last = now, now
		}
		k := len(l.to)
		l.to = append(l.to, f.timeouts[len(f.timeouts)-1])
		l.floor = append(l.floor, l.last.Sub(l.first))
		if len(f.in) > 0 || f.endless != nil { // a read that has something to return takes its time
			if d := plan(k, now.Sub(l.first)); d > 0 {
				time.Sleep(d)
			}
		}
		l.last = time.Now()
	}
}

// each is a plan: every read that returns something takes d.
func each(d time.Duration) func(int, time.Duration) time.Duration {
	return func(int, time.Duration) time.Duration { return d }
}

// until is a plan: read k lasts until marks[k] after the first read began (and not at all when that is past): the fake absorbs the
// overshoot of its own sleeps, so what is left of MaxDrainTime at a later read does not depend on how long each sleep really lasted.
func until(marks ...time.Duration) func(int, time.Duration) time.Duration {
	return func(k int, since time.Duration) time.Duration {
		if k < len(marks) {
			return marks[k] - since
		}
		return 0
	}
}

// within checks the rule at any speed: no read is asked to wait longer than what was left of MaxDrainTime when it began, which is at
// most MaxDrainTime minus the time the fake had kept the drain, and the timeout is neither zero nor more than one window. Exact: no
// tolerance, because the bound is on the side that a slow machine moves away from.
func (l *readLog) within(t *testing.T) {
	t.Helper()
	for i := range l.to {
		if l.to[i] <= 0 || l.to[i] > DrainReadTimeout {
			t.Errorf("read %d has a timeout of %v", i, l.to[i])
		}
		if l.floor[i]+l.to[i] > MaxDrainTime {
			t.Errorf("read %d began after at least %v and waits %v: past MaxDrainTime (%v)", i, l.floor[i], l.to[i], MaxDrainTime)
		}
	}
}

func TestQADrainTimeIsBounded(t *testing.T) {
	t.Run("a device that never stops, slow reads", func(t *testing.T) {
		f := newFake(descSpec{}, cardHandler(echoSW))
		f.endless = msg(rspDataBlock, 9, stOK, 0, 0, qaResp)
		var l readLog
		f.onIn = l.hook(f, each(45*time.Millisecond))
		r, _ := Open(f, Options{})
		_, err := r.SlotStatus(testBudget)
		lostWith(t, err, ErrProtocol)
		l.within(t)
		if f.nOut != 0 || r.drained {
			t.Errorf("%d writes, drained %v", f.nOut, r.drained)
		}
	})
	// The rule: the read that begins when less than DrainReadTimeout of MaxDrainTime is left is given that remainder, not a whole
	// window. The reads below return something, so the drain goes on; the fake keeps the fourth until 240 ms after the first began,
	// so any later read begins with at most 10 ms left, whatever the speed of the machine. The two outcomes that can follow are both
	// right: a read, which must have a timeout of at most 10 ms (cut to the remainder), or the loss of the session because the
	// machine was so slow that nothing was left (which needs MaxDrainTime to have passed in the call).
	//
	// What a clock seam in the Reader (a func() time.Time in Options, or a field of Reader) would add: a test could fix the remainder
	// (say 7 ms) and assert the timeout is exactly 7 ms, and that a remainder of zero loses the session, on every machine, with no
	// sleep. Without it the test can only prove the upper bound (at most the remainder that the fake's own sleeps leave), which
	// catches a read that is NOT cut, and not a read that is cut too much.
	t.Run("the last read waits for the remainder only", func(t *testing.T) {
		f := newFake(descSpec{}, cardHandler(echoSW))
		const reads = 4
		for i := range reads {
			f.sendRaw(msg(rspDataBlock, byte(i+1), stOK, 0, 0, qaResp))
		}
		var l readLog
		marks := []time.Duration{60 * time.Millisecond, 120 * time.Millisecond, 180 * time.Millisecond, MaxDrainTime - 10*time.Millisecond}
		f.onIn = l.hook(f, until(marks...))
		r, _ := Open(f, Options{})
		began := time.Now()
		_, err := r.SlotStatus(testBudget)
		spent := time.Since(began)
		lostWith(t, err, ErrProtocol) // reads that are cut do not make the pipe quiet
		l.within(t)
		if len(l.to) > reads {
			for i := reads; i < len(l.to); i++ {
				if l.floor[i] < MaxDrainTime-10*time.Millisecond {
					t.Errorf("read %d began after only %v: the fake kept the fourth read until %v", i, l.floor[i], marks[reads-1])
				}
				if l.to[i] >= DrainReadTimeout {
					t.Errorf("read %d, begun with at most %v left, has the timeout %v: not cut to the remainder", i, MaxDrainTime-l.floor[i], l.to[i])
				}
			}
		} else if spent < MaxDrainTime { // a slow machine: no read followed, which is right only when the time was gone
			t.Errorf("the session was lost after %v with %d reads: MaxDrainTime had not passed", spent, len(l.to))
		}
		if f.nOut != 0 || r.drained {
			t.Errorf("%d writes, drained %v: a cut read was taken for a quiet window", f.nOut, r.drained)
		}
		assertWiped(t, r, "after the drain")
	})
	t.Run("a remainder of zero loses the session at once", func(t *testing.T) {
		f := newFake(descSpec{}, cardHandler(echoSW))
		f.sendRaw(msg(rspDataBlock, 1, stOK, 0, 0, qaResp))
		var l readLog
		f.onIn = l.hook(f, each(MaxDrainTime+30*time.Millisecond))
		r, _ := Open(f, Options{})
		_, err := r.SlotStatus(testBudget)
		lostWith(t, err, ErrProtocol)
		if f.nIn != 1 || f.nOut != 0 {
			t.Errorf("%d reads, %d writes after the time was spent", f.nIn, f.nOut)
		}
		untouched(t, r, f, err)
	})
}

// ---- rule 5: one Port, one Reader ----

// closeAware is a Port like a Device: after Close every transfer answers ErrClosed.
type closeAware struct{ *fakeDevice }

func (p *closeAware) BulkOut(ep uint8, d []byte, to time.Duration) (int, error) {
	if p.closed {
		return 0, usbccid.ErrClosed
	}
	return p.fakeDevice.BulkOut(ep, d, to)
}

func (p *closeAware) BulkIn(ep uint8, b []byte, to time.Duration) (int, error) {
	if p.closed {
		return 0, usbccid.ErrClosed
	}
	return p.fakeDevice.BulkIn(ep, b, to)
}

func TestQASecondOpenOnAHeldPort(t *testing.T) {
	f := newFake(descSpec{}, cardHandler(echoSW))
	r1, err := Open(f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r1.PowerOn(testBudget); err != nil {
		t.Fatal(err)
	}
	mark := f.mark()
	r2, err := Open(f, Options{})
	if r2 != nil || !errors.Is(err, usbccid.ErrAlreadyClaimed) {
		t.Fatalf("second Open: %v %v", r2, err)
	}
	if got := fmt.Sprint(f.callsAfter(mark)); got != "[descriptors claim]" {
		t.Errorf("the second Open made the calls %s: no Close, no Release, nothing else", got)
	}
	if f.closed {
		t.Error("the Port was closed")
	}
	// The first Reader still works: a full exchange.
	if got, err := r1.Transmit(apduSelect, testBudget); err != nil || !bytes.Equal(got, append(append([]byte(nil), apduSelect...), 0x90, 0)) {
		t.Fatalf("the first Reader after the refusal: %x %v", got, err)
	}
	if err := r1.Close(); err != nil {
		t.Fatal(err)
	}
	f.claimed = false // the Port is the same Device only until Close; a new one is a new claim
	f.closed = false
	r3, err := Open(f, Options{})
	if err != nil {
		t.Fatalf("Open after the first Reader's Close: %v", err)
	}
	if _, err := r3.PowerOn(testBudget); err != nil {
		t.Errorf("the new Reader: %v", err)
	}
}

// What is left as it was by the refusal: a second Open that fails BEFORE the claim (a bad TransferTimeout) still closes the Port, and
// the first Reader, which shares it, then finds it closed: its next call is a lost session with usbccid.ErrClosed as the cause. Pinned as
// it is; the doc says Open closes the Port on every error but ErrAlreadyClaimed, and a caller that opens a held Port twice has handed
// it over twice.
func TestQASecondOpenThatFailsBeforeTheClaimClosesTheSharedPort(t *testing.T) {
	f := newFake(descSpec{}, cardHandler(echoSW))
	p := &closeAware{f}
	r1, err := Open(p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r1.PowerOn(testBudget); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(p, Options{TransferTimeout: -time.Second}); !errors.Is(err, ErrBadBudget) {
		t.Fatalf("err = %v", err)
	}
	if !f.closed {
		t.Fatal("the shared Port was not closed")
	}
	_, err = r1.Transmit(apduSelect, testBudget)
	lostWith(t, err, usbccid.ErrClosed)
}

// ---- the budget between the header and the body, and a Port that counts too many bytes ----

// The budget runs out between the header of an answer and its body: the session is lost with the budget as the cause, and the body is
// not read.
func TestQABudgetGoneBetweenTheHeaderAndTheBody(t *testing.T) {
	data := bytes.Repeat(qaResp, 8) // 128 bytes: header and body come in two reads at a packet size of 64
	for _, c := range []struct {
		name   string
		onRead func(f *fakeDevice, n int)
		cause  []error
	}{
		{"the budget is spent while the header is read", func(_ *fakeDevice, n int) {
			if n == 1 {
				time.Sleep(pastBudget)
			}
		}, []error{ErrBudgetExhausted}},
		{"the body never comes, and the read is cut to the budget", func(f *fakeDevice, n int) {
			if n == 1 {
				f.in = f.in[:1] // only the first packet
			}
		}, []error{ErrBudgetExhausted, usbccid.ErrTimeout}},
	} {
		t.Run(c.name, func(t *testing.T) {
			armed := false
			r, f := openPowered(t, Options{}, func(f *fakeDevice, m []byte) {
				if armed && m[0] == msgXfrBlock {
					f.send(msg(rspDataBlock, m[6], stOK, 0, 0, data))
					return
				}
				cardHandler(echoSW)(f, m)
			})
			base := f.nIn
			f.onIn = func(f *fakeDevice) { c.onRead(f, f.nIn-base) }
			armed = true
			_, err := r.Transmit(qaCmd, smallBudget)
			lostWith(t, err, nil)
			for _, want := range c.cause {
				if !errors.Is(err, want) {
					t.Errorf("err = %v, want the cause %v", err, want)
				}
			}
			if len(c.cause) == 1 && f.nIn-base != 1 {
				t.Errorf("%d reads: the body was read after the budget was gone", f.nIn-base)
			}
			assertNoLeak(t, "budget", err, qaResp, qaCmd)
			assertWiped(t, r, "after the loss")
			untouched(t, r, f, err)
		})
	}
}

// A Port that reports more bytes than were asked, in the first read of an answer and in the read of its body (both more than the
// request, and more than the message needs but within the request): the session is lost with ErrProtocol, nothing more is read, and the
// text holds no byte.
func TestQAPortCountsMoreThanWasAsked(t *testing.T) {
	data := bytes.Repeat(qaResp, 8) // 128 bytes of data: 138 in all, 64 + 74
	for _, c := range []struct {
		name string
		call int // which read of the answer lies (1 = the header, 2 = the body)
		more func(got, asked int) int
	}{
		{"header read, one more than the request", 1, func(_, a int) int { return a + 1 }},
		{"header read, far more", 1, func(_, a int) int { return a + 4096 }},
		{"body read, one more than the request", 2, func(_, a int) int { return a + 1 }},
		{"body read, more than the message but within the request", 2, func(g, _ int) int { return g + 1 }},
		{"body read, a negative count", 2, func(int, int) int { return -5 }},
	} {
		t.Run(c.name, func(t *testing.T) {
			armed := false
			f := newFake(descSpec{}, func(f *fakeDevice, m []byte) {
				if armed && m[0] == msgXfrBlock {
					f.send(msg(rspDataBlock, m[6], stOK, 0, 0, data))
					return
				}
				cardHandler(echoSW)(f, m)
			})
			count := 0
			p := &tweakPort{fakeDevice: f, inN: func(got, asked int) int {
				if !armed {
					return got
				}
				count++
				if count == c.call {
					return c.more(got, asked)
				}
				return got
			}}
			r, err := Open(p, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.PowerOn(testBudget); err != nil {
				t.Fatal(err)
			}
			armed = true
			_, err = r.Transmit(qaCmd, testBudget)
			lostWith(t, err, ErrProtocol)
			assertNoLeak(t, "count", err, qaResp, qaCmd)
			assertWiped(t, r, "after the loss")
			untouched(t, r, f, err)
			if count > c.call {
				t.Errorf("%d reads after the one that lied", count-c.call)
			}
		})
	}
}
