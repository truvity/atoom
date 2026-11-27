package ccid

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truvity/atoom/key/usbccid"
)

// This file holds what the QA tests share: made-up secrets, the check that no run of them is in an error text, the check that the
// reader's buffers are clear, a Port that reports odd counts, and the table runner of the lost-session rule. Every Port in these
// tests is the in-memory fake of fake_test.go: nothing here opens a device node or reads the sysfs tree of the machine.

// qaCmd is a made-up command APDU that carries a PIN-like run; qaResp is a made-up response with a token. No run of four or more bytes
// of either may be in the text of an error.
var (
	qaCmd  = []byte{0x00, 0xDB, 0x3F, 0xFF, 0x0C, 0xB3, 0x5C, 0xE7, 0x2A, 'p', 'i', 'n', '=', '7', '3', '9', '1', 0x1D}
	qaResp = []byte{0xA7, 0x3C, 0x91, 0xD2, 't', 'o', 'k', '=', '5', '5', '8', '8', 0x6E, 0x1B, 0x90, 0x00}
)

// leakForms returns every way a window of four bytes of s could be written in an error text.
func leakForms(s []byte) []string {
	var out []string
	for i := 0; i+4 <= len(s); i++ {
		w := s[i : i+4]
		inner := func(v string) string { return strings.Trim(v, "[]{}\"") }
		out = append(out,
			fmt.Sprintf("%x", w), fmt.Sprintf("%X", w), fmt.Sprintf("% x", w), fmt.Sprintf("% X", w),
			inner(fmt.Sprintf("%v", w)), inner(fmt.Sprintf("%q", w)), inner(fmt.Sprintf("%#v", w)), string(w),
			fmt.Sprintf("0x%02x 0x%02x 0x%02x 0x%02x", w[0], w[1], w[2], w[3]),
			fmt.Sprintf("0x%02X, 0x%02X, 0x%02X, 0x%02X", w[0], w[1], w[2], w[3]),
		)
	}
	return out
}

// assertNoLeak fails when the text of err holds a run of four or more bytes of one of the secrets.
func assertNoLeak(t *testing.T, what string, err error, secrets ...[]byte) {
	t.Helper()
	if err == nil {
		return
	}
	text := err.Error()
	for _, s := range secrets {
		for _, form := range leakForms(s) {
			if form != "" && strings.Contains(text, form) {
				t.Errorf("%s: the error text holds %q (a run of the command or the response): %v", what, form, err)
				return
			}
		}
	}
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// assertWiped fails when a buffer of the reader still holds a byte. The tests call it between calls, so no lock is needed.
func assertWiped(t *testing.T, r *Reader, when string) {
	t.Helper()
	if !allZero(r.wbuf) {
		t.Errorf("%s: the write buffer of the reader holds data", when)
	}
	if !allZero(r.rbuf) {
		t.Errorf("%s: the read buffer of the reader holds data", when)
	}
}

// tweakPort is the fake Port with the counts that BulkOut and BulkIn report changed, to show how the reader meets a Port that lies.
type tweakPort struct {
	*fakeDevice
	outN func(sent, given int) int
	inN  func(got, asked int) int
}

func (p *tweakPort) BulkOut(ep uint8, d []byte, to time.Duration) (int, error) {
	n, err := p.fakeDevice.BulkOut(ep, d, to)
	if err == nil && p.outN != nil {
		n = p.outN(n, len(d))
	}
	return n, err
}

func (p *tweakPort) BulkIn(ep uint8, b []byte, to time.Duration) (int, error) {
	n, err := p.fakeDevice.BulkIn(ep, b, to)
	if err == nil && p.inN != nil {
		n = p.inN(n, len(b))
	}
	return n, err
}

// rspFor is the type of the answer to a message type.
func rspFor(typ byte) byte {
	if typ == msgGetSlotState || typ == msgIccPowerOff {
		return rspSlotStatus
	}
	return rspDataBlock
}

// lostCase is one way to end a session, and the call that meets it.
type lostCase struct {
	name string
	spec descSpec
	opts Options
	// reply, when set, is the device's answer to every message that the call sends once the reader is armed. It runs inside BulkOut
	// with the lock of the fake held, so it uses f.send, f.sendRaw and the fields, never the locking methods.
	reply func(f *fakeDevice, m []byte)
	// tune runs after the reader is powered on and before the call; base is the number of bulk-in and bulk-out calls so far.
	tune func(f *fakeDevice, baseIn, baseOut int)
	call func(r *Reader) error
	// cause must be reachable with errors.Is (nil: only ErrSessionLost is required); not must not be.
	cause []error
	not   []error
}

// A test that spends a budget on purpose uses smallBudget, and a fake that keeps a call for pastBudget to run it out. smallBudget is
// large enough that a stall of the test process before the first call on the Port (a slow or busy machine) cannot use it up and turn
// the expected loss of the session into a refusal that touched nothing; pastBudget is longer, and a sleep only ever lasts longer than
// asked, so the budget is gone however slow the machine is.
const (
	smallBudget = 100 * time.Millisecond
	pastBudget  = 150 * time.Millisecond
)

// runLost runs the case and holds the lost-session rule: ErrSessionLost with the cause reachable, no secret in the text, the buffers
// clear, every later call answers with the same error without one call on the port, and Close still releases and closes the port.
func runLost(t *testing.T, c lostCase) {
	t.Helper()
	var armed atomic.Bool
	h := func(f *fakeDevice, m []byte) {
		if armed.Load() && c.reply != nil {
			c.reply(f, m)
			return
		}
		cardHandler(echoSW)(f, m)
	}
	f := newFake(c.spec, h)
	r, err := Open(f, c.opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.PowerOn(testBudget); err != nil {
		t.Fatal(err)
	}
	baseIn, baseOut := f.nIn, f.nOut
	if c.tune != nil {
		c.tune(f, baseIn, baseOut)
	}
	armed.Store(true)
	err = c.call(r)
	if err == nil {
		t.Fatal("the call succeeded")
	}
	if !errors.Is(err, ErrSessionLost) {
		t.Fatalf("err = %v, want ErrSessionLost", err)
	}
	for _, want := range c.cause {
		if !errors.Is(err, want) {
			t.Errorf("err = %v, want the cause %v to be reachable", err, want)
		}
	}
	for _, no := range c.not {
		if errors.Is(err, no) {
			t.Errorf("err = %v, must not be %v", err, no)
		}
	}
	assertNoLeak(t, c.name, err, qaCmd, qaResp)
	assertWiped(t, r, "after the loss")

	// Every later call: the same error, and not one call on the port. The checks of the arguments come after the latch.
	mark := f.mark()
	for i := range 2 {
		check := func(op string, e error) {
			t.Helper()
			if !errors.Is(e, ErrSessionLost) || !errors.Is(e, err) {
				t.Errorf("%s %d after the loss: %v, want the first error", op, i, e)
			}
			for _, want := range c.cause {
				if !errors.Is(e, want) {
					t.Errorf("%s %d after the loss: %v lost the cause %v", op, i, e, want)
				}
			}
		}
		_, e := r.Transmit(qaCmd, testBudget)
		check("Transmit", e)
		_, e = r.Transmit(nil, 0) // a bad command and a bad budget: the latch answers first
		check("Transmit(nil, 0)", e)
		_, e = r.PowerOn(testBudget)
		check("PowerOn", e)
		check("PowerOff", r.PowerOff(testBudget))
		_, e = r.SlotStatus(testBudget)
		check("SlotStatus", e)
	}
	if after := f.callsAfter(mark); len(after) != 0 {
		t.Errorf("the port was used after the loss: %v", after)
	}
	assertWiped(t, r, "after the calls of a lost session")

	// Close still releases the interface and closes the port, exactly once, and sends nothing.
	outs := f.nOut
	if e := r.Close(); e != nil {
		t.Errorf("Close: %v", e)
	}
	if got := fmt.Sprint(f.callsAfter(mark)); got != "[release close]" {
		t.Errorf("Close of a lost session made the calls %s, want [release close]", got)
	}
	if f.nOut != outs || !f.closed {
		t.Errorf("Close sent %d messages, port closed %v", f.nOut-outs, f.closed)
	}
	if e := r.Close(); e != nil {
		t.Errorf("second Close: %v", e)
	}
	if _, e := r.Transmit(qaCmd, testBudget); !errors.Is(e, usbccid.ErrClosed) {
		t.Errorf("Transmit after Close of a lost session: %v", e)
	}
}

// opCase is one call of the reader, with the type of the message that carries its failure.
type opCase struct {
	name string
	skip int  // normal exchanges before the message that fails
	typ  byte // type of that message
	call func(r *Reader, budget time.Duration) error
}

var qaOps = []opCase{
	{"SlotStatus", 0, msgGetSlotState, func(r *Reader, b time.Duration) error { _, err := r.SlotStatus(b); return err }},
	{"PowerOn/GetSlotStatus", 0, msgGetSlotState, func(r *Reader, b time.Duration) error { _, err := r.PowerOn(b); return err }},
	{"PowerOn/IccPowerOn", 1, msgIccPowerOn, func(r *Reader, b time.Duration) error { _, err := r.PowerOn(b); return err }},
	{"PowerOff", 0, msgIccPowerOff, func(r *Reader, b time.Duration) error { return r.PowerOff(b) }},
	{"Transmit", 0, msgXfrBlock, func(r *Reader, b time.Duration) error { _, err := r.Transmit(qaCmd, b); return err }},
}

// qaFault is a way for one message to go wrong, whatever the message is.
type qaFault struct {
	name   string
	cause  []error
	not    []error
	budget time.Duration // 0 means testBudget
	// reply answers the message of the failing type: rsp is the type of the answer to it.
	reply func(f *fakeDevice, rsp, seq byte)
	// tune injects a failure on the port; skip is the number of normal exchanges before the failing one.
	tune func(f *fakeDevice, baseIn, baseOut, skip int)
}

func (q qaFault) lostCase(op opCase) lostCase {
	// A transfer timeout shorter than the budget, so that a timeout of the transfer is not taken for the budget running out.
	c := lostCase{name: op.name + "/" + q.name, cause: q.cause, not: q.not, opts: Options{TransferTimeout: time.Second}}
	b := q.budget
	if b == 0 {
		b = testBudget
	}
	c.call = func(r *Reader) error { return op.call(r, b) }
	if q.reply != nil {
		c.reply = func(f *fakeDevice, m []byte) {
			if m[0] == op.typ {
				q.reply(f, rspFor(m[0]), m[6])
				return
			}
			cardHandler(echoSW)(f, m)
		}
	}
	if q.tune != nil {
		c.tune = func(f *fakeDevice, in, out int) { q.tune(f, in, out, op.skip) }
	}
	return c
}

// bigFrame is an answer of dlen data bytes (qaResp repeated) that the header of the answer announces in full.
func bigFrame(rsp, seq byte, dlen int) []byte {
	return msg(rsp, seq, stOK, 0, 0, bytes.Repeat(qaResp, dlen/len(qaResp)+1)[:dlen])
}
