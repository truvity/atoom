package ccid

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/truvity/atoom/key/usbccid"
	"github.com/truvity/atoom/key/usbccid/usbccidtest"
)

// The four rules of the last round, at their edges: the order of a response chain, no chain at short APDU level, a budget that is spent
// before the first Port call, and the six packet sizes.

// ---- rule 1: after a begin block, only middle (0x03) continues and only end (0x02) ends ----

// chainByValues answers the command with blocks whose chain parameters are given (the first message gets blocks[0], each continuation the
// next), each with its own payload, and counts the messages.
type chainByValues struct {
	params []byte
	sent   int
	data   [][]byte
}

func (c *chainByValues) handler() func(f *fakeDevice, m []byte) {
	return func(f *fakeDevice, m []byte) {
		if m[0] != msgXfrBlock {
			cardHandler(echoSW)(f, m)
			return
		}
		i := c.sent
		c.sent++
		if i >= len(c.params) {
			return // nothing more: a read would time out
		}
		d := append(bytes.Repeat(qaResp, 2), byte(i&0xFF), 0x90)
		c.data = append(c.data, d)
		f.send(msg(rspDataBlock, m[6], stOK, 0, c.params[i], d))
	}
}

func TestQAChainSecondAndLaterBlockValues(t *testing.T) {
	for _, prefix := range [][]byte{{chainBegin}, {chainBegin, chainMiddle}, {chainBegin, chainMiddle, chainMiddle, chainMiddle}} {
		for v := range 256 {
			t.Run(fmt.Sprintf("after %v, %#02x", prefix, v), func(t *testing.T) {
				params := append(append([]byte(nil), prefix...), byte(v&0xFF))
				if v == chainMiddle {
					params = append(params, chainEnd) // only 0x03 continues: one more block
				}
				c := &chainByValues{params: params}
				r, f := openPowered(t, Options{}, c.handler())
				outs := len(f.outs)
				got, err := r.Transmit(apduSelect, testBudget)
				good := v == chainEnd || v == chainMiddle
				if good {
					var want []byte
					for _, d := range c.data {
						want = append(want, d...)
					}
					if err != nil || !bytes.Equal(got, want) {
						t.Fatalf("%d bytes, %v", len(got), err)
					}
					if sent := len(f.outs) - outs; sent != len(params) {
						t.Errorf("%d messages for %d blocks", sent, len(params))
					}
					return
				}
				lostWith(t, err, ErrProtocol)
				if got != nil {
					t.Error("a response with the error")
				}
				// No block was asked for after the refused one: the command and one continuation for each block so far.
				if sent := len(f.outs) - outs; sent != len(prefix)+1 {
					t.Errorf("%d messages sent, want %d", sent, len(prefix)+1)
				}
				assertWiped(t, r, "after the refused block")
				assertNoLeak(t, "chain", err, qaResp)
				untouched(t, r, f, err)
			})
		}
	}
}

// A chain of begin, many middle blocks and end, to the limit of the response, goes through.
func TestQAChainOfManyMiddleBlocksToTheLimit(t *testing.T) {
	want := pattern(MaxResponseAPDU, 4)
	blocks := cut(want, 261)
	cc := &chainCard{respParts: blocks}
	r, _ := openPowered(t, Options{}, cc.handler())
	got, err := r.Transmit(apduSelect, testBudget)
	if err != nil || !bytes.Equal(got, want) || len(blocks) < 200 {
		t.Fatalf("%d bytes in %d blocks, %v", len(got), len(blocks), err)
	}
}

// ---- rule 2: a reader at short APDU level has no chain ----

func shortLevelFake(h func(f *fakeDevice, m []byte)) *fakeDevice {
	return newFake(descSpec{features: featuresShort}, h)
}

func TestQAShortLevelChainValueInTheAnswerOfTransmit(t *testing.T) {
	for v := range 256 {
		t.Run(fmt.Sprintf("%#02x", v), func(t *testing.T) {
			f := shortLevelFake(func(f *fakeDevice, m []byte) {
				if m[0] == msgXfrBlock {
					f.send(msg(rspDataBlock, m[6], stOK, 0, byte(v&0xFF), append(bytes.Repeat(qaResp, 2), 0x90, 0)))
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
			outs := len(f.outs)
			got, err := r.Transmit(apduSelect, testBudget)
			if v == 0 {
				if err != nil || len(got) != 2*len(qaResp)+2 {
					t.Fatalf("%d bytes, %v", len(got), err)
				}
				return
			}
			lostWith(t, err, ErrProtocol)
			if got != nil || len(f.outs)-outs != 1 {
				t.Errorf("response %v, %d messages sent: no further message is allowed", got != nil, len(f.outs)-outs)
			}
			assertNoLeak(t, "short level", err, qaResp)
			assertWiped(t, r, "after the refusal")
			untouched(t, r, f, err)
		})
	}
}

func TestQAShortLevelChainValueInTheAnswerToReset(t *testing.T) {
	for v := range 256 {
		t.Run(fmt.Sprintf("%#02x", v), func(t *testing.T) {
			f := shortLevelFake(func(f *fakeDevice, m []byte) {
				if m[0] == msgIccPowerOn {
					f.send(msg(rspDataBlock, m[6], stOK, 0, byte(v&0xFF), testATR))
					return
				}
				cardHandler(echoSW)(f, m)
			})
			r, err := Open(f, Options{})
			if err != nil {
				t.Fatal(err)
			}
			atr, err := r.PowerOn(testBudget)
			if v == 0 {
				if err != nil || !bytes.Equal(atr, testATR) {
					t.Fatalf("%x %v", atr, err)
				}
				return
			}
			lostWith(t, err, ErrProtocol)
			if atr != nil || len(f.outs) != 2 { // GetSlotStatus and IccPowerOn, and nothing after
				t.Errorf("answer to reset %v, %d messages", atr != nil, len(f.outs))
			}
			untouched(t, r, f, err)
		})
	}
}

// The check is for the answers that carry a response (DataBlock): the byte of a slot status answer is bClockStatus there, and at
// extended level every value is judged by the chain rules (PowerOn takes the answer to reset whatever its chain byte).
func TestQAChainByteWhereItIsNotAChainByte(t *testing.T) {
	for _, features := range []uint32{featuresShort, featuresExt} {
		t.Run(fmt.Sprintf("features %#x", features), func(t *testing.T) {
			f := newFake(descSpec{features: features}, func(f *fakeDevice, m []byte) {
				switch m[0] {
				case msgGetSlotState, msgIccPowerOff:
					st := byte(iccPresent)
					if m[0] == msgIccPowerOff {
						st = iccInact
					}
					f.send(msg(rspSlotStatus, m[6], stOK|st, 0, 0x55, nil)) // bClockStatus
				case msgIccPowerOn:
					f.send(msg(rspDataBlock, m[6], stOK, 0, 0, testATR))
				default:
					cardHandler(echoSW)(f, m)
				}
			})
			r, err := Open(f, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if st, err := r.SlotStatus(testBudget); err != nil || st != SlotActive {
				t.Fatalf("SlotStatus: %v %v", st, err)
			}
			if err := r.PowerOff(testBudget); err != nil {
				t.Fatalf("PowerOff: %v", err)
			}
		})
	}
	t.Run("an answer to reset is one whole block at the extended level too", func(t *testing.T) {
		for v := range 256 {
			f := newFake(descSpec{}, func(f *fakeDevice, m []byte) {
				if m[0] == msgIccPowerOn {
					f.send(msg(rspDataBlock, m[6], stOK, 0, byte(v&0xFF), qaResp))
					return
				}
				cardHandler(echoSW)(f, m)
			})
			r, err := Open(f, Options{})
			if err != nil {
				t.Fatal(err)
			}
			atr, err := r.PowerOn(testBudget)
			if v == 0 {
				if err != nil || !bytes.Equal(atr, qaResp) {
					t.Fatalf("%x %v", atr, err)
				}
				continue
			}
			lostWith(t, err, ErrProtocol)
			if atr != nil || len(f.outs) != 2 {
				t.Fatalf("chain byte %#x: answer %v, %d messages (none after IccPowerOn)", v, atr != nil, len(f.outs))
			}
			assertNoLeak(t, "answer to reset", err, qaResp)
			assertWiped(t, r, "after the refusal")
			untouched(t, r, f, err)
		}
	})
	t.Run("extended level still chains", func(t *testing.T) {
		cc := &chainCard{respParts: [][]byte{{1, 2}, {3, 0x90, 0}}}
		r, _ := openPowered(t, Options{}, cc.handler())
		if got, err := r.Transmit(apduSelect, testBudget); err != nil || !bytes.Equal(got, []byte{1, 2, 3, 0x90, 0}) {
			t.Fatalf("%x %v", got, err)
		}
	})
}

// At short level ANY DataBlock answer with a chain byte other than 0 ends the session: an accepted one, a FAILED one (command status 1)
// and a time extension, for Transmit and for PowerOn. With the value 0 a failed answer is a *CommandError and the session goes on.
func TestQAShortLevelChainByteInFailedAnswersAndTimeExtensions(t *testing.T) {
	type kind struct {
		name  string
		frame func(seq, chain byte) []byte
	}
	kinds := []kind{
		{"failed", func(seq, chain byte) []byte { return msg(rspDataBlock, seq, stFailed, 0xFE, chain, nil) }},
		{"failed, card gone", func(seq, chain byte) []byte { return msg(rspDataBlock, seq, stFailed|iccNone, 0xFB, chain, nil) }},
		{"time extension", func(seq, chain byte) []byte { return msg(rspDataBlock, seq, stTimeExt, 1, chain, nil) }},
	}
	for _, k := range kinds {
		for _, op := range []opCase{qaOps[2], qaOps[4]} { // IccPowerOn, XfrBlock
			for _, v := range []byte{0, 1, 2, 3, 0x10, 0x55, 0xFF} {
				t.Run(fmt.Sprintf("%s/%s/%#02x", k.name, op.name, v), func(t *testing.T) {
					armed := false
					f := shortLevelFake(func(f *fakeDevice, m []byte) {
						if armed && m[0] == op.typ {
							f.send(k.frame(m[6], v))
							if k.name == "time extension" {
								cardHandler(echoSW)(f, m) // then the real answer
							}
							return
						}
						cardHandler(echoSW)(f, m)
					})
					r, err := Open(f, Options{})
					if err != nil {
						t.Fatal(err)
					}
					if op.typ == msgXfrBlock {
						if _, err := r.PowerOn(testBudget); err != nil {
							t.Fatal(err)
						}
					}
					armed = true
					outs := len(f.outs)
					err = op.call(r, testBudget)
					if v == 0 {
						var ce *CommandError
						if k.name == "time extension" {
							if err != nil {
								t.Fatalf("a time extension with chain byte 0: %v", err)
							}
						} else if !errors.As(err, &ce) || errors.Is(err, ErrSessionLost) {
							t.Fatalf("a failed answer with chain byte 0: %v", err)
						}
						return
					}
					lostWith(t, err, ErrProtocol)
					want := 1
					if op.typ == msgIccPowerOn {
						want = 2
					}
					if sent := len(f.outs) - outs; sent != want {
						t.Errorf("%d messages sent, want %d", sent, want)
					}
					untouched(t, r, f, err)
				})
			}
		}
	}
}

// ---- rule 3: a budget that is spent before the first Port call keeps the session; from the first Port call on it loses it ----

// refusedOnBudget checks the shape of a refusal on the budget before any Port call.
func refusedOnBudget(t *testing.T, r *Reader, f *fakeDevice, err error, mark int) {
	t.Helper()
	if !errors.Is(err, ErrBudgetExhausted) || errors.Is(err, ErrSessionLost) {
		t.Fatalf("err = %v, want ErrBudgetExhausted without ErrSessionLost", err)
	}
	if after := f.callsAfter(mark); len(after) != 0 {
		t.Fatalf("the port was used: %v", after)
	}
	assertWiped(t, r, "after the refusal")
}

// The budget is spent in the time between the first reading of the call and the Port call: the clock (a scriptClock) moves by the whole
// budget at its second reading, which is the one before the first Port call of a later call and the one that starts the drain of a first
// call. No machine has to be slow for it, and none can be fast enough to miss it.
func TestQABudgetSpentBeforeTheFirstPortCall(t *testing.T) {
	budgets := []time.Duration{time.Nanosecond, 5 * time.Millisecond} // the last is under one drain read for a first call
	for _, op := range []opCase{qaOps[0], qaOps[1], qaOps[3], qaOps[4]} {
		for _, first := range []bool{true, false} {
			if op.typ == msgXfrBlock && first {
				continue // a Transmit needs a powered card, which a first call cannot have
			}
			for _, b := range budgets {
				if !first && b > time.Millisecond {
					continue // a later call with 5 ms does reach the port: that is not a refusal
				}
				name := fmt.Sprintf("%s/first=%v/%v", op.name, first, b)
				t.Run(name, func(t *testing.T) {
					setSeq(100)
					clk := &scriptClock{Clock: usbccidtest.NewDefaultClock()}
					stale := msg(rspDataBlock, 101, stOK, 0, 0, qaResp) // a late answer with the number of the next message
					var f *fakeDevice
					var r *Reader
					if first {
						var err error
						f = newFake(descSpec{}, cardHandler(echoSW))
						f.send(stale)
						if r, err = Open(f, Options{Clock: clk}); err != nil {
							t.Fatal(err)
						}
					} else {
						r, f = openPowered(t, Options{Clock: clk}, cardHandler(echoSW))
						setSeq(100) // no drain in a later call: a stale answer with the very number would be taken (the rest risk)
					}
					mark := f.mark()
					clk.arm(2, b, false)
					err := op.call(r, b)
					clk.disarm()
					refusedOnBudget(t, r, f, err, mark)
					// The same Reader works with a normal budget. The late answer is dropped by the drain, once, before the first
					// send; no sequence number was spent by the refusal.
					if err := op.call(r, testBudget); err != nil {
						t.Fatalf("the next call: %v", err)
					}
					after := f.callsAfter(mark)
					reads := 0
					for _, c := range after {
						if c == "out" {
							break
						}
						if c == "in" {
							reads++
						}
					}
					if first && reads < 2 { // the stale message and the quiet read
						t.Errorf("%d reads before the first write, want the drain (calls %v)", reads, after)
					}
					if !first && reads != 0 {
						t.Errorf("a drain again: %v", after)
					}
					firstOut := f.outs[len(f.outs)-countOuts(op)]
					if firstOut[6] != 101 {
						t.Errorf("the first message after the refusal has the number %d, want 101 (no number was spent)", firstOut[6])
					}
					if len(f.in) != 0 {
						t.Errorf("%d packets left", len(f.in))
					}
				})
			}
		}
	}
}

// countOuts is the number of messages a successful call of op sends.
func countOuts(op opCase) int {
	if strings.HasPrefix(op.name, "PowerOn") {
		return 2
	}
	return 1
}

// The state of the card after a refusal on the budget is what it was.
func TestQABudgetRefusalKeepsThePowerState(t *testing.T) {
	// Each refusal is a call whose whole budget is spent at the second reading of the clock, before the first Port call (see
	// TestQABudgetSpentBeforeTheFirstPortCall).
	spent := func() *scriptClock { return &scriptClock{Clock: usbccidtest.NewDefaultClock()} }
	t.Run("PowerOn refused on a powered card", func(t *testing.T) {
		clk := spent()
		r, f := openPowered(t, Options{Clock: clk}, cardHandler(echoSW))
		mark := f.mark()
		clk.arm(2, time.Nanosecond, false)
		_, err := r.PowerOn(time.Nanosecond)
		clk.disarm()
		refusedOnBudget(t, r, f, err, mark)
		if _, err := r.Transmit(apduSelect, testBudget); err != nil {
			t.Errorf("Transmit after a refused PowerOn on a powered card: %v", err)
		}
	})
	t.Run("PowerOn refused on a card that is off", func(t *testing.T) {
		clk := spent()
		r, f, err := openFake(Options{Clock: clk}, cardHandler(echoSW))
		if err != nil {
			t.Fatal(err)
		}
		mark := f.mark()
		clk.arm(2, time.Nanosecond, false)
		_, err = r.PowerOn(time.Nanosecond)
		clk.disarm()
		refusedOnBudget(t, r, f, err, mark)
		if _, err := r.Transmit(apduSelect, testBudget); !errors.Is(err, ErrNotPowered) {
			t.Errorf("Transmit after a refused PowerOn: %v", err)
		}
		if _, err := r.PowerOn(testBudget); err != nil {
			t.Errorf("PowerOn after the refusal: %v", err)
		}
	})
	t.Run("PowerOff refused on a powered card", func(t *testing.T) {
		clk := spent()
		r, f := openPowered(t, Options{Clock: clk}, cardHandler(echoSW))
		mark := f.mark()
		clk.arm(2, time.Nanosecond, false)
		err := r.PowerOff(time.Nanosecond)
		clk.disarm()
		refusedOnBudget(t, r, f, err, mark)
		if _, err := r.Transmit(apduSelect, testBudget); err != nil {
			t.Errorf("Transmit after a refused PowerOff on a powered card: %v", err)
		}
	})
	t.Run("PowerOff refused on a card that is off", func(t *testing.T) {
		clk := spent()
		r, f, err := openFake(Options{Clock: clk}, cardHandler(echoSW))
		if err != nil {
			t.Fatal(err)
		}
		mark := f.mark()
		clk.arm(2, time.Nanosecond, false)
		err = r.PowerOff(time.Nanosecond)
		clk.disarm()
		refusedOnBudget(t, r, f, err, mark)
		if _, err := r.Transmit(apduSelect, testBudget); !errors.Is(err, ErrNotPowered) {
			t.Errorf("Transmit: %v", err)
		}
	})
	t.Run("SlotStatus and Transmit refused on a powered card", func(t *testing.T) {
		clk := spent()
		r, f := openPowered(t, Options{Clock: clk}, cardHandler(echoSW))
		mark := f.mark()
		clk.arm(2, time.Nanosecond, false)
		_, err := r.SlotStatus(time.Nanosecond)
		refusedOnBudget(t, r, f, err, mark)
		clk.arm(2, time.Nanosecond, false)
		_, err = r.Transmit(qaCmd, time.Nanosecond)
		refusedOnBudget(t, r, f, err, mark)
		clk.disarm()
		if _, err := r.Transmit(apduSelect, testBudget); err != nil {
			t.Errorf("Transmit after the refusals: %v", err)
		}
	})
}

// From the first Port call on, a budget failure loses the session: after one drain read; after a write whose answer is not read in time;
// after the first message of a PowerOn; after the first block of a chain.
func TestQABudgetFailureAfterAPortCallLosesTheSession(t *testing.T) {
	lost := func(t *testing.T, r *Reader, f *fakeDevice, err error) {
		t.Helper()
		lostWith(t, err, ErrBudgetExhausted)
		assertWiped(t, r, "after the loss")
		untouched(t, r, f, err)
	}
	for _, op := range []opCase{qaOps[0], qaOps[1], qaOps[3]} {
		t.Run("one drain read, then the budget is gone: "+op.name, func(t *testing.T) {
			clk := usbccidtest.NewDefaultClock()
			f := newFake(descSpec{}, cardHandler(echoSW))
			f.clk = clk
			f.send(msg(rspDataBlock, 77, stOK, 0, 0, qaResp)) // the first read returns bytes, and takes longer than the budget
			f.onIn = func(f *fakeDevice) { f.pass(pastBudget) }
			r, _ := Open(f, Options{Clock: clk})
			err := op.call(r, smallBudget) // room for the two windows with 60 ms to spare, and less than the time the read takes
			lost(t, r, f, err)
			if f.nOut != 0 || f.nIn != 1 {
				t.Errorf("%d writes, %d reads", f.nOut, f.nIn)
			}
		})
	}
	t.Run("PowerOn: the first message answered, the budget gone before the second", func(t *testing.T) {
		r, f, err := openFake(Options{Clock: usbccidtest.NewDefaultClock()}, cardHandler(echoSW))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.SlotStatus(testBudget); err != nil { // the drain is done
			t.Fatal(err)
		}
		f.onIn = func(f *fakeDevice) { f.pass(pastBudget) }
		outs := f.nOut
		_, err = r.PowerOn(smallBudget)
		lost(t, r, f, err)
		if f.nOut-outs != 1 {
			t.Errorf("%d messages written, want only GetSlotStatus", f.nOut-outs)
		}
	})
	t.Run("a chain: the first block answered, the budget gone before the second", func(t *testing.T) {
		cc := &chainCard{respParts: [][]byte{{0x90, 0}}}
		r, f := openPowered(t, Options{Clock: usbccidtest.NewDefaultClock()}, cc.handler())
		f.onIn = func(f *fakeDevice) { f.pass(pastBudget) }
		outs := f.nOut
		_, err := r.Transmit(qaLong, smallBudget)
		lost(t, r, f, err)
		if f.nOut-outs != 1 {
			t.Errorf("%d blocks written, want 1", f.nOut-outs)
		}
	})
	t.Run("a write that succeeded and a budget gone before the read", func(t *testing.T) {
		r, f := openPowered(t, Options{Clock: usbccidtest.NewDefaultClock()}, func(f *fakeDevice, m []byte) {
			f.pass(pastBudget)
			cardHandler(echoSW)(f, m)
		})
		reads := f.nIn
		_, err := r.Transmit(qaCmd, smallBudget)
		lost(t, r, f, err)
		if f.nIn != reads {
			t.Error("a read was started with no budget")
		}
	})
	t.Run("a write that failed", func(t *testing.T) {
		r, f := openPowered(t, Options{Clock: usbccidtest.NewDefaultClock()}, cardHandler(echoSW))
		out := f.nOut
		f.outFail = func(n int) error {
			if n > out {
				f.pass(pastBudget)
				return usbccid.ErrTimeout
			}
			return nil
		}
		_, err := r.Transmit(qaCmd, smallBudget)
		lost(t, r, f, err)
	})
}

// The invariant, searched at random: whenever a call returns ErrBudgetExhausted and the session is not lost, the call made not one call
// on the Port; and when it made a call on the Port, the session is lost or the call succeeded or failed for another reason. The time
// that every Port call takes (virtual, moved by the fake) and the budgets are random, so the call is cut at every place. A small
// budget in nanoseconds is also spent between two readings of the clock, as a real clock does when a call is slow to start.
func TestQABudgetRefusalMeansNoPortCall(t *testing.T) {
	rng := rand.New(rand.NewPCG(11, 12)) //nolint:gosec // a fixed seed
	refused, lostN := 0, 0
	for i := range 900 {
		op := qaOps[rng.IntN(len(qaOps))]
		first := rng.IntN(2) == 0
		var r *Reader
		var f *fakeDevice
		clk := &scriptClock{Clock: usbccidtest.NewDefaultClock()}
		h := func(f *fakeDevice, m []byte) {
			f.pass(time.Duration(rng.IntN(4)) * time.Millisecond)
			cardHandler(echoSW)(f, m)
		}
		if first && op.typ != msgXfrBlock {
			f = newFake(descSpec{}, h)
			f.clk = clk
			var err error
			if r, err = Open(f, Options{Clock: clk, TransferTimeout: time.Duration(1+rng.IntN(25)) * time.Millisecond}); err != nil {
				t.Fatal(err)
			}
		} else {
			r, f = openPowered(t, Options{Clock: clk, TransferTimeout: time.Duration(1+rng.IntN(25)) * time.Millisecond}, h)
		}
		f.onIn = func(f *fakeDevice) { f.pass(time.Duration(rng.IntN(4)) * time.Millisecond) }
		budget := time.Duration(1+rng.IntN(14)) * time.Millisecond
		tiny := rng.IntN(5) == 0
		if tiny {
			budget = time.Duration(1 + rng.IntN(2000))
			clk.arm(2, budget+time.Duration(rng.IntN(2)), false) // spent at the second reading, or one nanosecond more
		}
		mark := f.mark()
		err := op.call(r, budget)
		clk.disarm()
		calls := len(f.callsAfter(mark))
		switch {
		case err == nil:
		case errors.Is(err, ErrBudgetExhausted) && !errors.Is(err, ErrSessionLost):
			refused++
			if calls != 0 {
				t.Fatalf("case %d (%s, first=%v, budget %v): ErrBudgetExhausted without ErrSessionLost after %d Port calls: %v", i, op.name, first, budget, calls, err)
			}
		case errors.Is(err, ErrSessionLost):
			lostN++
		}
		if calls != 0 && errors.Is(err, ErrBudgetExhausted) && !errors.Is(err, ErrSessionLost) {
			t.Fatalf("case %d: a budget failure after a Port call kept the session: %v", i, err)
		}
		// A refusal leaves a usable Reader; a lost session is the same error for every call.
		if errors.Is(err, ErrSessionLost) {
			mark = f.mark()
			if _, e := r.SlotStatus(testBudget); !errors.Is(e, err) || len(f.callsAfter(mark)) != 0 {
				t.Fatalf("case %d: a lost session is not latched: %v", i, e)
			}
		} else if errors.Is(err, ErrBudgetExhausted) {
			if _, e := r.SlotStatus(testBudget); e != nil {
				t.Fatalf("case %d: the Reader does not work after a refusal: %v", i, e)
			}
		}
	}
	if refused < 20 || lostN < 20 {
		t.Errorf("the search met %d refusals and %d lost sessions: it does not cover both", refused, lostN)
	}
}

// ---- rule 4: the packet sizes ----

func setBulkPacketSizes(b []byte, out, in uint16) {
	binary.LittleEndian.PutUint16(b[epAt(b, 0x02)+4:], out)
	binary.LittleEndian.PutUint16(b[epAt(b, 0x82)+4:], in)
}

func TestQAEveryPacketSizeOfTheBulkEndpoints(t *testing.T) {
	legal := map[int]bool{8: true, 16: true, 32: true, 64: true, 512: true, 1024: true}
	for _, which := range []string{"bulk IN", "bulk OUT"} {
		accepted := 0
		for v := range 1 << 16 {
			b := descSpec{}.build()
			if which == "bulk IN" {
				setBulkPacketSizes(b, 64, uint16(v))
			} else {
				setBulkPacketSizes(b, uint16(v), 64)
			}
			f := newFake(descSpec{}, nil)
			f.desc = b
			r, err := Open(f, Options{})
			if legal[v] {
				accepted++
				if err != nil || fmt.Sprint(f.calls) != "[descriptors claim]" {
					t.Fatalf("%s size %d: err = %v, calls %v", which, v, err, f.calls)
				}
				info := r.Info()
				if got := map[string]int{"bulk IN": info.MaxPacketIn, "bulk OUT": info.MaxPacketOut}[which]; got != v {
					t.Fatalf("%s size %d: Info says %d", which, v, got)
				}
				continue
			}
			if r != nil || !errors.Is(err, usbccid.ErrBadDescriptors) || fmt.Sprint(f.calls) != "[descriptors close]" {
				t.Fatalf("%s size %d: r=%v err=%v calls %v", which, v, r, err, f.calls)
			}
		}
		if accepted != 6 {
			t.Errorf("%s: %d sizes accepted, want 6", which, accepted)
		}
	}
}
