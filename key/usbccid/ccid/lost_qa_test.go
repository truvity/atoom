package ccid

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/truvity/atoom/key/usbccid"
)

// A. The session-lost rule. One table of faults, run against every call of the reader that sends a message (SlotStatus, both messages
// of PowerOn, PowerOff, Transmit): from each, the reader is lost, every later call answers with the same error and does not call the
// port, the cause stays reachable, no run of the command or the response is in the text, the buffers are clear, and Close still
// releases and closes the port.

func inFrom(err error) func(f *fakeDevice, baseIn, baseOut, skip int) {
	return func(f *fakeDevice, baseIn, _, skip int) {
		f.inFail = func(n int) error {
			if n > baseIn+skip {
				return err
			}
			return nil
		}
	}
}

func outFrom(err error) func(f *fakeDevice, baseIn, baseOut, skip int) {
	return func(f *fakeDevice, _, baseOut, skip int) {
		f.outFail = func(n int) error {
			if n > baseOut+skip {
				return err
			}
			return nil
		}
	}
}

func lostFaults() []qaFault {
	stale := func(n int) func(f *fakeDevice, rsp, seq byte) {
		return func(f *fakeDevice, rsp, seq byte) {
			for i := range n {
				f.send(msg(rsp, seq-1-byte(i), stOK, 0, 0, qaResp))
			}
			f.send(msg(rsp, seq, stOK, 0, 0, qaResp))
		}
	}
	return []qaFault{
		{name: "BulkOut times out", cause: []error{usbccid.ErrTimeout}, not: []error{ErrBudgetExhausted}, tune: outFrom(usbccid.ErrTimeout)},
		{name: "BulkIn times out in the header", cause: []error{usbccid.ErrTimeout}, not: []error{ErrBudgetExhausted},
			reply: func(*fakeDevice, byte, byte) {}},
		{name: "BulkIn times out in the middle of a message", cause: []error{usbccid.ErrTimeout}, not: []error{ErrBudgetExhausted},
			reply: func(f *fakeDevice, rsp, seq byte) { f.sendRaw(bigFrame(rsp, seq, 150)[:64]) }},
		{name: "stall on BulkOut", cause: []error{usbccid.ErrStalled}, tune: outFrom(usbccid.ErrStalled)},
		{name: "stall on BulkIn", cause: []error{usbccid.ErrStalled}, tune: inFrom(usbccid.ErrStalled)},
		{name: "device gone on BulkOut", cause: []error{usbccid.ErrGone}, tune: outFrom(usbccid.ErrGone)},
		{name: "device gone on BulkIn", cause: []error{usbccid.ErrGone}, tune: inFrom(usbccid.ErrGone)},
		{name: "port closed on BulkOut", cause: []error{usbccid.ErrClosed}, tune: outFrom(usbccid.ErrClosed)},
		{name: "port closed on BulkIn", cause: []error{usbccid.ErrClosed}, tune: inFrom(usbccid.ErrClosed)},
		{name: "an error of no known kind on BulkIn", cause: []error{errOverflow}, tune: inFrom(errOverflow)},
		{name: "short write", cause: []error{ErrShortWrite}, tune: func(f *fakeDevice, _, _, _ int) { f.shortOut = true }},
		{name: "budget gone before the first transfer", cause: []error{ErrBudgetExhausted}, not: []error{usbccid.ErrTimeout},
			budget: time.Nanosecond, noPort: true},
		{name: "budget runs out during a transfer", cause: []error{ErrBudgetExhausted, usbccid.ErrTimeout}, budget: 30 * time.Millisecond,
			reply: func(*fakeDevice, byte, byte) {}},
		{name: "budget runs out between the transfers", cause: []error{ErrBudgetExhausted}, not: []error{usbccid.ErrTimeout}, budget: 30 * time.Millisecond,
			reply: func(f *fakeDevice, rsp, seq byte) {
				time.Sleep(80 * time.Millisecond)
				f.send(msg(rsp, seq, stOK, 0, 0, qaResp))
			}},
		{name: "a wait with no end", cause: []error{ErrBudgetExhausted}, budget: 40 * time.Millisecond,
			reply: func(f *fakeDevice, rsp, seq byte) { f.endless = msg(rsp, seq, stTimeExt, 1, 0, nil) }},
		{name: "three stale answers are skipped, the fourth ends it", cause: []error{ErrProtocol}, reply: stale(4)},
		{name: "many stale answers", cause: []error{ErrProtocol}, reply: stale(40)},
		{name: "an answer for another slot", cause: []error{ErrProtocol},
			reply: func(f *fakeDevice, rsp, seq byte) {
				m := msg(rsp, seq, stOK, 0, 0, qaResp)
				m[5] = 1
				f.sendRaw(m)
			}},
		{name: "an answer of the wrong type", cause: []error{ErrProtocol},
			reply: func(f *fakeDevice, rsp, seq byte) { f.sendRaw(msg(rsp^1, seq, stOK, 0, 0, qaResp)) }},
		{name: "an answer of a type nobody sends", cause: []error{ErrProtocol},
			reply: func(f *fakeDevice, _, seq byte) { f.sendRaw(msg(0x82, seq, stOK, 0, 0, qaResp)) }},
		{name: "a declared length of the maximum plus one", cause: []error{ErrProtocol},
			reply: func(f *fakeDevice, rsp, seq byte) { f.send(bigFrame(rsp, seq, 262)) }},
		{name: "a declared length of 4 GiB less one", cause: []error{ErrProtocol},
			reply: func(f *fakeDevice, rsp, seq byte) {
				m := msg(rsp, seq, stOK, 0, 0, qaResp)
				m[1], m[2], m[3], m[4] = 0xFF, 0xFF, 0xFF, 0xFF
				f.sendRaw(m)
			}},
		{name: "a short packet before the declared length", cause: []error{ErrProtocol}, not: []error{usbccid.ErrTimeout},
			reply: func(f *fakeDevice, rsp, seq byte) {
				m := msg(rsp, seq, stOK, 0, 0, bytes.Repeat(qaResp, 3))
				f.sendRaw(m[:25])
			}},
		{name: "a short packet inside the header", cause: []error{ErrProtocol}, not: []error{usbccid.ErrTimeout},
			reply: func(f *fakeDevice, rsp, seq byte) { f.sendRaw(msg(rsp, seq, stOK, 0, 0, qaResp)[:7]) }},
		{name: "more bytes than declared", cause: []error{ErrProtocol},
			reply: func(f *fakeDevice, rsp, seq byte) {
				m := msg(rsp, seq, stOK, 0, 0, qaResp)
				m[1] = 2
				f.sendRaw(m)
			}},
		{name: "an empty read where none is allowed", cause: []error{ErrProtocol},
			reply: func(f *fakeDevice, rsp, seq byte) { f.sendRaw([]byte{}, msg(rsp, seq, stOK, 0, 0, qaResp)) }},
		{name: "an empty read inside a message", cause: []error{ErrProtocol},
			reply: func(f *fakeDevice, rsp, seq byte) {
				m := msg(rsp, seq, stOK, 0, 0, bytes.Repeat(qaResp, 8))
				f.sendRaw(m[:64], []byte{}, m[64:])
			}},
		{name: "a reserved command status", cause: []error{ErrProtocol},
			reply: func(f *fakeDevice, rsp, seq byte) { f.sendRaw(msg(rsp, seq, 0xC0, 0, 0, qaResp)) }},
	}
}

func TestQALostSessionMatrix(t *testing.T) {
	for _, op := range qaOps {
		for _, q := range lostFaults() {
			c := q.lostCase(op)
			t.Run(c.name, func(t *testing.T) { runLost(t, c) })
		}
	}
}

// A message of the right type, slot and sequence whose status is the reserved 3 of bmICCStatus is a protocol error of GetSlotStatus.
func TestQALostReservedSlotState(t *testing.T) {
	runLost(t, lostCase{
		name:  "bmICCStatus 3",
		reply: func(f *fakeDevice, m []byte) { f.send(msg(rspSlotStatus, m[6], stOK|3, 0, 0, nil)) },
		call:  func(r *Reader) error { _, err := r.SlotStatus(testBudget); return err },
		cause: []error{ErrProtocol},
	})
}

// A Port that reports a count it should not: more bytes written than given, a negative count, more bytes read than the buffer holds.
func TestQALostPortThatLies(t *testing.T) {
	cases := []struct {
		name  string
		out   func(sent, given int) int
		in    func(got, asked int) int
		cause error
	}{
		{"more written than given", func(_, g int) int { return g + 1 }, nil, ErrShortWrite},
		{"a negative count written", func(int, int) int { return -1 }, nil, ErrShortWrite},
		{"zero written", func(int, int) int { return 0 }, nil, ErrShortWrite},
		{"more read than the buffer", nil, func(_, a int) int { return a + 1 }, ErrProtocol},
		{"a negative count read", nil, func(int, int) int { return -1 }, ErrProtocol},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(descSpec{}, cardHandler(echoSW))
			p := &tweakPort{fakeDevice: f}
			r, err := Open(p, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.PowerOn(testBudget); err != nil {
				t.Fatal(err)
			}
			p.outN, p.inN = c.out, c.in
			_, err = r.Transmit(qaCmd, testBudget)
			if !errors.Is(err, ErrSessionLost) || !errors.Is(err, c.cause) {
				t.Fatalf("err = %v, want ErrSessionLost and %v", err, c.cause)
			}
			assertNoLeak(t, c.name, err, qaCmd, qaResp)
			mark := f.mark()
			if _, e := r.Transmit(qaCmd, testBudget); !errors.Is(e, err) {
				t.Errorf("second call: %v", e)
			}
			if len(f.callsAfter(mark)) != 0 {
				t.Error("the port was used after the loss")
			}
		})
	}
}

// chainDevice answers a command that comes in blocks and sends a response in blocks, and lets a test break one place. Every block of a
// command carries the level in m[8:10]. The response is respBlocks (the last holds the status word).
type chainDevice struct {
	respBlocks [][]byte
	// onBlock is called for each XfrBlock before the normal answer, with its level and its place (counting from 0 for the blocks of
	// the command and then the continuations); it returns true when it answered itself.
	onBlock func(f *fakeDevice, m []byte, lvl uint16, n int) bool
	n       int
	next    int
}

func (c *chainDevice) handler() func(f *fakeDevice, m []byte) {
	return func(f *fakeDevice, m []byte) {
		if m[0] != msgXfrBlock {
			cardHandler(echoSW)(f, m)
			return
		}
		seq := m[6]
		lvl := uint16(m[8]) | uint16(m[9])<<8
		n := c.n
		c.n++
		if c.onBlock != nil && c.onBlock(f, m, lvl, n) {
			return
		}
		switch lvl {
		case lvlBegin, lvlMiddle:
			f.send(msg(rspDataBlock, seq, stOK, 0, chainEmptyAck, nil))
		case lvlWhole, lvlEnd:
			c.next = 0
			c.sendBlock(f, seq)
		case lvlContinue:
			c.sendBlock(f, seq)
		}
	}
}

func (c *chainDevice) sendBlock(f *fakeDevice, seq byte) {
	chain := byte(chainMiddle)
	switch {
	case len(c.respBlocks) == 1:
		chain = chainWhole
	case c.next == 0:
		chain = chainBegin
	case c.next == len(c.respBlocks)-1:
		chain = chainEnd
	}
	f.send(msg(rspDataBlock, seq, stOK, 0, chain, c.respBlocks[c.next]))
	c.next++
}

// qaLong is a command of 700 bytes: three blocks at the default message length.
var qaLong = append(append([]byte(nil), qaCmd...), bytes.Repeat([]byte{0x5A}, 700-len(qaCmd))...)

// The faults inside a chain: of the command, between its blocks, and of the response. From each, the session is lost.
func TestQALostInsideChains(t *testing.T) {
	respBlocks := [][]byte{bytes.Repeat(qaResp, 10), bytes.Repeat(qaResp, 10), append(bytes.Repeat(qaResp, 3), 0x90, 0x00)}
	type chainCase struct {
		name    string
		onBlock func(f *fakeDevice, m []byte, lvl uint16, n int) bool
		tune    func(f *fakeDevice, baseIn, baseOut int)
		budget  time.Duration
		cause   []error
		not     []error
	}
	at := func(want int, do func(f *fakeDevice, m []byte)) func(f *fakeDevice, m []byte, lvl uint16, n int) bool {
		return func(f *fakeDevice, m []byte, _ uint16, n int) bool {
			if n != want {
				return false
			}
			do(f, m)
			return true
		}
	}
	// n counts the XfrBlock messages of the call: 0, 1 and 2 are the three blocks of the command, 3 and 4 the continuations.
	cases := []chainCase{
		{name: "no answer to the first block", onBlock: at(0, func(*fakeDevice, []byte) {}), cause: []error{usbccid.ErrTimeout}},
		{name: "no answer to the middle block", onBlock: at(1, func(*fakeDevice, []byte) {}), cause: []error{usbccid.ErrTimeout}},
		{name: "no answer to the last block", onBlock: at(2, func(*fakeDevice, []byte) {}), cause: []error{usbccid.ErrTimeout}},
		{name: "BulkOut times out on the second block", tune: func(f *fakeDevice, _, out int) {
			f.outFail = func(n int) error {
				if n > out+1 {
					return usbccid.ErrTimeout
				}
				return nil
			}
		}, cause: []error{usbccid.ErrTimeout}},
		{name: "stall on the third block", tune: func(f *fakeDevice, _, out int) {
			f.outFail = func(n int) error {
				if n > out+2 {
					return usbccid.ErrStalled
				}
				return nil
			}
		}, cause: []error{usbccid.ErrStalled}},
		{name: "the device is gone after the first block", tune: func(f *fakeDevice, in, _ int) {
			f.inFail = func(n int) error {
				if n > in+1 {
					return usbccid.ErrGone
				}
				return nil
			}
		}, cause: []error{usbccid.ErrGone}},
		{name: "ack of the first block asks for nothing", onBlock: at(0, func(f *fakeDevice, m []byte) {
			f.send(msg(rspDataBlock, m[6], stOK, 0, chainWhole, qaResp))
		}), cause: []error{ErrProtocol}},
		{name: "ack of a middle block carries data", onBlock: at(1, func(f *fakeDevice, m []byte) {
			f.send(msg(rspDataBlock, m[6], stOK, 0, chainEmptyAck, qaResp))
		}), cause: []error{ErrProtocol}},
		{name: "ack of a middle block is labelled begin", onBlock: at(1, func(f *fakeDevice, m []byte) {
			f.send(msg(rspDataBlock, m[6], stOK, 0, chainBegin, nil))
		}), cause: []error{ErrProtocol}},
		{name: "the first block is answered as failed", onBlock: at(0, func(f *fakeDevice, m []byte) {
			f.send(msg(rspDataBlock, m[6], stFailed, 0xFE, 0, nil))
		}), cause: []error{ErrCardMute, ErrCommandFailed}},
		{name: "the last block is answered as failed", onBlock: at(2, func(f *fakeDevice, m []byte) {
			f.send(msg(rspDataBlock, m[6], stFailed|iccNone, 0xFB, 0, nil))
		}), cause: []error{ErrReaderFault, ErrNoCard}},
		{name: "a wait with no end between the blocks", onBlock: at(1, func(f *fakeDevice, m []byte) {
			f.endless = msg(rspDataBlock, m[6], stTimeExt, 1, 0, nil)
		}), budget: 40 * time.Millisecond, cause: []error{ErrBudgetExhausted}},
		{name: "the budget runs out between the blocks", onBlock: at(1, func(f *fakeDevice, m []byte) {
			time.Sleep(80 * time.Millisecond)
			f.send(msg(rspDataBlock, m[6], stOK, 0, chainEmptyAck, nil))
		}), budget: 30 * time.Millisecond, cause: []error{ErrBudgetExhausted}, not: []error{usbccid.ErrTimeout}},
		{name: "short write on the second block", tune: func(f *fakeDevice, _, out int) {
			f.outFail = func(n int) error {
				if n == out+2 {
					f.shortOut = true
				}
				return nil
			}
		}, cause: []error{ErrShortWrite}},
		// The response.
		{name: "no answer to the first continuation", onBlock: at(3, func(*fakeDevice, []byte) {}), cause: []error{usbccid.ErrTimeout}},
		{name: "the continuation is cut in the middle", onBlock: at(3, func(f *fakeDevice, m []byte) {
			f.sendRaw(bigFrame(rspDataBlock, m[6], 150)[:64])
		}), cause: []error{usbccid.ErrTimeout}},
		{name: "the continuation is cut by a short packet", onBlock: at(3, func(f *fakeDevice, m []byte) {
			f.sendRaw(bigFrame(rspDataBlock, m[6], 150)[:64], bigFrame(rspDataBlock, m[6], 150)[64:90])
		}), cause: []error{ErrProtocol}, not: []error{usbccid.ErrTimeout}},
		{name: "BulkOut fails on the continuation", tune: func(f *fakeDevice, _, out int) {
			f.outFail = func(n int) error {
				if n > out+3 {
					return usbccid.ErrStalled
				}
				return nil
			}
		}, cause: []error{usbccid.ErrStalled}},
		{name: "four stale answers before the continuation", onBlock: at(3, func(f *fakeDevice, m []byte) {
			for i := range 4 {
				f.send(msg(rspDataBlock, m[6]-1-byte(i), stOK, 0, chainEnd, qaResp))
			}
			f.send(msg(rspDataBlock, m[6], stOK, 0, chainEnd, qaResp))
		}), cause: []error{ErrProtocol}},
		{name: "the continuation is for another slot", onBlock: at(3, func(f *fakeDevice, m []byte) {
			x := msg(rspDataBlock, m[6], stOK, 0, chainEnd, qaResp)
			x[5] = 2
			f.sendRaw(x)
		}), cause: []error{ErrProtocol}},
		{name: "the continuation has the wrong type", onBlock: at(3, func(f *fakeDevice, m []byte) {
			f.sendRaw(msg(rspSlotStatus, m[6], stOK, 0, chainEnd, qaResp))
		}), cause: []error{ErrProtocol}},
		{name: "the continuation declares more than the maximum", onBlock: at(3, func(f *fakeDevice, m []byte) {
			f.send(bigFrame(rspDataBlock, m[6], 262))
		}), cause: []error{ErrProtocol}},
		{name: "an empty read where none is allowed in the response chain", onBlock: at(3, func(f *fakeDevice, m []byte) {
			f.sendRaw([]byte{}, msg(rspDataBlock, m[6], stOK, 0, chainEnd, qaResp))
		}), cause: []error{ErrProtocol}},
		{name: "a continuation with an unknown chain parameter", onBlock: at(3, func(f *fakeDevice, m []byte) {
			f.send(msg(rspDataBlock, m[6], stOK, 0, 0x55, qaResp))
		}), cause: []error{ErrProtocol}},
		{name: "the continuation is answered as failed", onBlock: at(3, func(f *fakeDevice, m []byte) {
			f.send(msg(rspDataBlock, m[6], stFailed|iccInact, 0xFD, 0, nil))
		}), cause: []error{ErrCardParity, ErrCommandFailed}},
		{name: "a wait with no end between the response blocks", onBlock: at(4, func(f *fakeDevice, m []byte) {
			f.endless = msg(rspDataBlock, m[6], stTimeExt, 1, 0, nil)
		}), budget: 40 * time.Millisecond, cause: []error{ErrBudgetExhausted}},
		{name: "the budget runs out between the response blocks", onBlock: at(4, func(f *fakeDevice, m []byte) {
			time.Sleep(80 * time.Millisecond)
			f.send(msg(rspDataBlock, m[6], stOK, 0, chainEnd, qaResp))
		}), budget: 30 * time.Millisecond, cause: []error{ErrBudgetExhausted}, not: []error{usbccid.ErrTimeout}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dev := &chainDevice{respBlocks: respBlocks, onBlock: c.onBlock}
			b := c.budget
			if b == 0 {
				b = testBudget
			}
			runLost(t, lostCase{
				name:  c.name,
				opts:  Options{TransferTimeout: time.Second},
				reply: dev.handler(),
				tune:  c.tune,
				call:  func(r *Reader) error { _, err := r.Transmit(qaLong, b); return err },
				cause: c.cause,
				not:   c.not,
			})
		})
	}
}

// The response chain that never ends is cut at MaxResponseAPDU, and the session is lost with its own name.
func TestQALostResponseTooLong(t *testing.T) {
	runLost(t, lostCase{
		name: "response too long",
		reply: func(f *fakeDevice, m []byte) {
			f.send(msg(rspDataBlock, m[6], stOK, 0, chainBegin, bytes.Repeat(qaResp, 16)[:250]))
			f.endless = msg(rspDataBlock, m[6], stOK, 0, chainMiddle, bytes.Repeat(qaResp, 16)[:250])
		},
		call:  func(r *Reader) error { _, err := r.Transmit(qaCmd, testBudget); return err },
		cause: []error{ErrResponseTooLong},
	})
}

// A failure that answers the only message of a command does NOT end the session, for every call that sends a message and every ICC
// status. The next call works, and the port is used for it.
func TestQACardLevelFailureDoesNotEndTheSession(t *testing.T) {
	for _, op := range qaOps {
		for icc := byte(0); icc <= 2; icc++ {
			t.Run(fmt.Sprintf("%s/icc%d", op.name, icc), func(t *testing.T) {
				var armed bool
				h := func(f *fakeDevice, m []byte) {
					if armed && m[0] == op.typ {
						armed = false
						f.send(msg(rspFor(m[0]), m[6], stFailed|icc, 0xFE, 0, qaResp))
						return
					}
					cardHandler(echoSW)(f, m)
				}
				f := newFake(descSpec{}, h)
				r, err := Open(f, Options{})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := r.PowerOn(testBudget); err != nil {
					t.Fatal(err)
				}
				armed = true
				err = op.call(r, testBudget)
				var ce *CommandError
				if !errors.As(err, &ce) || ce.Code != 0xFE || ce.ICCStatus != icc || errors.Is(err, ErrSessionLost) {
					t.Fatalf("err = %v, want a *CommandError and no lost session", err)
				}
				if !errors.Is(err, ErrCardMute) || !errors.Is(err, ErrCommandFailed) || errors.Is(err, ErrNoCard) != (icc == 2) {
					t.Errorf("sentinels of %v", err)
				}
				assertNoLeak(t, "failed", err, qaCmd, qaResp)
				assertWiped(t, r, "after the failure")
				// The stream is in order: the next calls work. PowerOn first when the failure took the power state away.
				if _, err := r.PowerOn(testBudget); err != nil {
					t.Fatalf("PowerOn after the failure: %v", err)
				}
				if got, err := r.Transmit(qaCmd, testBudget); err != nil || !bytes.Equal(got, append(append([]byte(nil), qaCmd...), 0x90, 0)) {
					t.Fatalf("Transmit after the failure: %x %v", got, err)
				}
			})
		}
	}
}
