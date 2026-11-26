package ccid

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvity/atoom/key/usbccid"
)

var apduSelect = []byte{0x00, 0xA4, 0x04, 0x00, 0x05, 0xA0, 0x00, 0x00, 0x03, 0x08}

// lostWith checks that err ended the session and still shows its cause.
func lostWith(t *testing.T, err, cause error) {
	t.Helper()
	if !errors.Is(err, ErrSessionLost) {
		t.Errorf("err = %v, want ErrSessionLost", err)
	}
	if cause != nil && !errors.Is(err, cause) {
		t.Errorf("err = %v, want it to wrap %v", err, cause)
	}
}

// untouched checks that every call of the Reader answers with the lost session and does not touch the Port.
func untouched(t *testing.T, r *Reader, f *fakeDevice, first error) {
	t.Helper()
	mark := f.mark()
	for i := range 2 {
		if _, err := r.Transmit(apduSelect, testBudget); !errors.Is(err, first) {
			t.Errorf("transmit %d after the loss: %v, want the first error", i, err)
		}
		if _, err := r.PowerOn(testBudget); !errors.Is(err, first) {
			t.Errorf("PowerOn after the loss: %v", err)
		}
		if err := r.PowerOff(testBudget); !errors.Is(err, first) {
			t.Errorf("PowerOff after the loss: %v", err)
		}
		if _, err := r.SlotStatus(testBudget); !errors.Is(err, first) {
			t.Errorf("SlotStatus after the loss: %v", err)
		}
	}
	if after := f.callsAfter(mark); len(after) != 0 {
		t.Errorf("the port was used after the loss: %v", after)
	}
}

// ---- open, power on, the shape of what is sent ----

func TestOpenSendsNothing(t *testing.T) {
	r, f, err := openFake(Options{}, cardHandler(echoSW))
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(f.calls) != "[descriptors claim]" {
		t.Errorf("calls = %v, want the descriptors and one plain claim", f.calls)
	}
	info := r.Info()
	if info.Interface != 2 || info.BulkOut != 0x02 || info.BulkIn != 0x82 || info.MaxPacketIn != 64 || info.MaxPacketOut != 64 ||
		!info.ExtendedAPDU || info.MaxMessageLength != 271 || info.MaxCommandAPDU != MaxCommandAPDU {
		t.Errorf("info = %+v", info)
	}
}

func TestPowerOnSendsOnlyGetSlotStatusAndPowerOn(t *testing.T) {
	setSeq(0) // the numbers of the process counter are read from 1 here
	r, f, err := openFake(Options{}, cardHandler(echoSW))
	if err != nil {
		t.Fatal(err)
	}
	atr, err := r.PowerOn(testBudget)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(atr, testATR) {
		t.Errorf("ATR = %x", atr)
	}
	if types := f.outTypes(); !bytes.Equal(types, []byte{msgGetSlotState, msgIccPowerOn}) {
		t.Fatalf("PowerOn sent message types %x, want 65 62", types)
	}
	for i, m := range f.outs {
		if len(m) != 10 {
			t.Errorf("message %d is %d bytes, want a bare 10-byte header", i, len(m))
		}
		if m[5] != 0 {
			t.Errorf("message %d: slot %d, want 0", i, m[5])
		}
		if m[6] != byte(i+1) {
			t.Errorf("message %d: sequence %d, want %d", i, m[6], i+1)
		}
		// bPowerSelect (PowerOn) and the reserved bytes are zero: the reader chooses the voltage.
		if m[7] != 0 || m[8] != 0 || m[9] != 0 {
			t.Errorf("message %d: parameter bytes %x", i, m[7:10])
		}
	}
}

func TestATRIsACopy(t *testing.T) {
	r, f, err := openFake(Options{}, cardHandler(echoSW))
	if err != nil {
		t.Fatal(err)
	}
	a, err := r.PowerOn(testBudget)
	if err != nil {
		t.Fatal(err)
	}
	a[0] = 0
	_ = f
	b, err := r.PowerOn(testBudget)
	if err != nil || b[0] != testATR[0] {
		t.Errorf("a second PowerOn returned %x, %v", b, err)
	}
}

func TestTransmitSendsOnlyXfrBlock(t *testing.T) {
	r, f := openPowered(t, Options{}, cardHandler(echoSW))
	for i := range 5 {
		got, err := r.Transmit(apduSelect, testBudget)
		if err != nil || !bytes.Equal(got, append(append([]byte(nil), apduSelect...), 0x90, 0)) {
			t.Fatalf("transmit %d: %x %v", i, got, err)
		}
	}
	for i, m := range f.outs[2:] {
		if m[0] != msgXfrBlock {
			t.Errorf("message %d is type %#x", i, m[0])
		}
		if int(m[1])|int(m[2])<<8 != len(apduSelect) || len(m) != 10+len(apduSelect) {
			t.Errorf("message %d: length field %x for %d bytes", i, m[1:5], len(m)-10)
		}
		if m[7] != 0 || m[8] != 0 || m[9] != 0 { // bBWI 0, wLevelParameter 0
			t.Errorf("message %d: bBWI/level %x", i, m[7:10])
		}
		if !bytes.Equal(m[10:], apduSelect) {
			t.Errorf("message %d: APDU altered: %x", i, m[10:])
		}
	}
	// Nothing other than 65, 62 and 6F was ever sent: no abort (0x72), power off (0x63), parameters (0x61), escape (0x6B), secure (0x69).
	for _, ty := range f.outTypes() {
		if ty != msgGetSlotState && ty != msgIccPowerOn && ty != msgXfrBlock {
			t.Errorf("unexpected message type %#x", ty)
		}
	}
}

func TestSequenceNumbersWrap(t *testing.T) {
	r, f := openPowered(t, Options{}, cardHandler(echoSW))
	for i := range 600 {
		if _, err := r.Transmit(apduSelect, testBudget); err != nil {
			t.Fatalf("transmit %d: %v", i, err)
		}
	}
	prev := f.outs[0][6]
	for i, m := range f.outs[1:] {
		if m[6] != prev+1 {
			t.Fatalf("message %d has sequence %d after %d", i+1, m[6], prev)
		}
		prev = m[6]
	}
	if f.outs[256][6] != f.outs[0][6] { // the 257th message has the number of the first
		t.Errorf("sequence did not wrap after 256 messages: %d and %d", f.outs[256][6], f.outs[0][6])
	}
}

// A message that fills whole packets is followed by an empty packet on some readers and not on others. Both must work for every
// length, and the empty packet must not disturb the next command.
func TestMessageLengthsAroundPacketBoundaries(t *testing.T) {
	for _, noZLP := range []bool{false, true} {
		t.Run(fmt.Sprintf("noZLP=%v", noZLP), func(t *testing.T) {
			var want []byte
			r, f := openPowered(t, Options{}, cardHandler(func([]byte) []byte { return want }))
			f.noZLP = noZLP
			for n := 2; n <= 261; n++ { // a response ends in two status bytes
				want = bytes.Repeat([]byte{byte(n)}, n)
				if n >= 2 {
					want[n-2], want[n-1] = 0x90, 0x00
				}
				got, err := r.Transmit(apduSelect, testBudget)
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("response of %d bytes: got %d bytes, err %v", n, len(got), err)
				}
			}
			if len(f.in) > 1 || len(f.in) == 1 && len(f.in[0]) != 0 {
				t.Errorf("unread packets left: %d", len(f.in))
			}
		})
	}
}

// A read never asks for more than the packets of its message: after the header the reader asks for the rest rounded up to whole
// packets, and every read is a whole number of packets (a bulk read of another size can end in EOVERFLOW).
func TestReadsNeverGoBeyondTheMessage(t *testing.T) {
	var want []byte
	r, f := openPowered(t, Options{}, cardHandler(func([]byte) []byte { return want }))
	prevBoundary := false // the answer to reset (24 bytes with its header) is no boundary
	for _, n := range []int{2, 5, 54, 55, 118, 119, 182, 183, 246, 247, 261, 54, 54} {
		want = bytes.Repeat([]byte{1}, n)
		f.mu.Lock()
		f.inLens = nil
		f.mu.Unlock()
		if _, err := r.Transmit(apduSelect, testBudget); err != nil {
			t.Fatalf("%d: %v", n, err)
		}
		total := 10 + n
		lens := f.inLens
		if prevBoundary { // the empty packet that ended the message before is read first, with a one-packet buffer
			if len(lens) == 0 || lens[0] != 64 {
				t.Fatalf("response of %d: reads of %v after a boundary message", n, f.inLens)
			}
			lens = lens[1:]
		}
		// The first read asks for one packet; the second, when there is one, for the rest in whole packets.
		for _, l := range f.inLens {
			if l%64 != 0 {
				t.Errorf("response of %d bytes: a read of %d bytes is not a whole number of packets", n, l)
			}
		}
		if total <= 64 && (len(lens) != 1 || lens[0] != 64) || total > 64 && (len(lens) != 2 || lens[0] != 64 || lens[1] != (total-64+63)/64*64) {
			t.Errorf("response of %d bytes: reads of %v", n, f.inLens)
		}
		prevBoundary = total%64 == 0
	}
}

// The one empty read after a message that filled whole packets is consumed; an empty read anywhere else is a protocol error.
func TestEmptyReadRules(t *testing.T) {
	t.Run("after a boundary message", func(t *testing.T) {
		var want []byte
		r, f := openPowered(t, Options{}, cardHandler(func([]byte) []byte { return want }))
		want = bytes.Repeat([]byte{7}, 64-10) // a 64-byte message: one full packet, then an empty one
		for i := range 3 {
			got, err := r.Transmit(apduSelect, testBudget)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("transmit %d: %v", i, err)
			}
		}
		if len(f.in) > 1 {
			t.Errorf("packets left: %d", len(f.in))
		}
	})
	t.Run("without a message before it", func(t *testing.T) {
		r, f := openPowered(t, Options{}, func(f *fakeDevice, m []byte) {
			if m[0] == msgXfrBlock {
				f.sendRaw([]byte{}, msg(rspDataBlock, m[6], stOK, 0, 0, []byte{0x90, 0}))
				return
			}
			cardHandler(echoSW)(f, m)
		})
		_, err := r.Transmit(apduSelect, testBudget)
		lostWith(t, err, ErrProtocol)
		untouched(t, r, f, err)
	})
	t.Run("two in a row after a boundary message", func(t *testing.T) {
		r, f := openPowered(t, Options{}, func(f *fakeDevice, m []byte) {
			if m[0] == msgXfrBlock {
				f.sendRaw(msg(rspDataBlock, m[6], stOK, 0, 0, bytes.Repeat([]byte{1}, 54)), []byte{}, []byte{})
				return
			}
			cardHandler(echoSW)(f, m)
		})
		if _, err := r.Transmit(apduSelect, testBudget); err != nil {
			t.Fatal(err)
		}
		_, err := r.Transmit(apduSelect, testBudget) // the second empty packet is read first, then the answer is not there
		lostWith(t, err, nil)
		untouched(t, r, f, err)
	})
	t.Run("inside a message", func(t *testing.T) {
		r, f := openPowered(t, Options{}, func(f *fakeDevice, m []byte) {
			if m[0] == msgXfrBlock {
				full := msg(rspDataBlock, m[6], stOK, 0, 0, bytes.Repeat([]byte{1}, 150))
				f.sendRaw(full[:64], []byte{}, full[64:])
				return
			}
			cardHandler(echoSW)(f, m)
		})
		_, err := r.Transmit(apduSelect, testBudget)
		lostWith(t, err, ErrProtocol)
		untouched(t, r, f, err)
	})
}

func TestSmallPacketReader(t *testing.T) {
	// A full-speed reader with 8-byte packets.
	f := newFake(descSpec{mpsIn: 8, mpsOut: 8}, cardHandler(echoSW))
	r, err := Open(f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.PowerOn(testBudget); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{0, 1, 5, 6, 14, 22, 100} {
		cmd := bytes.Repeat([]byte{7}, 4+n)
		got, err := r.Transmit(cmd, testBudget)
		if err != nil || !bytes.Equal(got, append(append([]byte(nil), cmd...), 0x90, 0)) {
			t.Fatalf("len %d: %v", len(cmd), err)
		}
	}
}

func TestHighSpeedPackets(t *testing.T) {
	// A reader with 512-byte packets and a message length of 1000.
	f := newFake(descSpec{mpsIn: 512, mpsOut: 512, maxMsg: 1000}, cardHandler(echoSW))
	r, err := Open(f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.PowerOn(testBudget); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{4, 100, 497, 502, 503, 600, 980, 981, 988} {
		cmd := bytes.Repeat([]byte{7}, n)
		got, err := r.Transmit(cmd, testBudget)
		if err != nil || !bytes.Equal(got, append(append([]byte(nil), cmd...), 0x90, 0)) {
			t.Fatalf("len %d: %v", n, err)
		}
	}
}

// ---- time extension ----

func TestTimeExtension(t *testing.T) {
	h := func(f *fakeDevice, m []byte) {
		seq := m[6]
		if m[0] != msgXfrBlock {
			cardHandler(echoSW)(f, m)
			return
		}
		// The card waits for a touch: time-extension messages (the error byte is the wait multiplier), one answer with a stale
		// sequence in between, then the answer.
		f.send(msg(rspDataBlock, seq, stTimeExt|iccPresent, 1, 0, nil))
		f.send(msg(rspDataBlock, seq-1, stOK, 0, 0, []byte{0xEE}))
		f.send(msg(rspDataBlock, seq, stTimeExt, 2, 0, nil))
		f.send(msg(rspDataBlock, seq, stTimeExt, 1, 0, nil))
		f.send(msg(rspDataBlock, seq, stOK, 0, 0, []byte{0x69, 0x82}))
	}
	r, f := openPowered(t, Options{}, h)
	n := len(f.outs)
	got, err := r.Transmit([]byte{0, 0x87, 0x14, 0x83, 0}, testBudget)
	if err != nil || !bytes.Equal(got, []byte{0x69, 0x82}) {
		t.Fatalf("got %x, %v", got, err)
	}
	// One command went out for the whole wait. No resend, no abort.
	if len(f.outs) != n+1 {
		t.Errorf("%d messages sent for one command", len(f.outs)-n)
	}
}

// Time-extension messages that carry data or the card-present bits are still time extensions.
func TestTimeExtensionWithDataAndIccBits(t *testing.T) {
	h := func(f *fakeDevice, m []byte) {
		if m[0] != msgXfrBlock {
			cardHandler(echoSW)(f, m)
			return
		}
		f.send(msg(rspDataBlock, m[6], stTimeExt|0x01, 1, 0, []byte{1, 2, 3}))
		f.send(msg(rspDataBlock, m[6], stOK, 0, 0, []byte{0x90, 0}))
	}
	r, _ := openPowered(t, Options{}, h)
	got, err := r.Transmit(apduSelect, testBudget)
	if err != nil || !bytes.Equal(got, []byte{0x90, 0}) {
		t.Fatalf("%x %v", got, err)
	}
}

// A wait with no end ends with the budget, and the session is lost.
func TestBudgetEndsAnEndlessWait(t *testing.T) {
	h := func(f *fakeDevice, m []byte) {
		if m[0] != msgXfrBlock {
			cardHandler(echoSW)(f, m)
			return
		}
		f.endless = msg(rspDataBlock, m[6], stTimeExt, 1, 0, nil)
	}
	r, f := openPowered(t, Options{}, h)
	start := time.Now()
	_, err := r.Transmit(apduSelect, 60*time.Millisecond)
	lostWith(t, err, ErrBudgetExhausted)
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %v", d)
	}
	if time.Since(start) < 50*time.Millisecond {
		t.Errorf("returned after %v, before the budget", time.Since(start))
	}
	untouched(t, r, f, err)
}

// The time spent waiting for a person is part of the budget: the same wait that fits a large budget ends a small one.
func TestTimeSpentWaitingCountsAgainstTheBudget(t *testing.T) {
	waits := func(f *fakeDevice, m []byte) {
		if m[0] != msgXfrBlock {
			cardHandler(echoSW)(f, m)
			return
		}
		f.onIn = func(*fakeDevice) { time.Sleep(15 * time.Millisecond) }
		for range 10 {
			f.send(msg(rspDataBlock, m[6], stTimeExt, 1, 0, nil))
		}
		f.send(msg(rspDataBlock, m[6], stOK, 0, 0, []byte{0x90, 0}))
	}
	r, _ := openPowered(t, Options{}, waits)
	if got, err := r.Transmit(apduSelect, testBudget); err != nil || !bytes.Equal(got, []byte{0x90, 0}) {
		t.Fatalf("large budget: %x %v", got, err)
	}
	r, f := openPowered(t, Options{}, waits)
	_, err := r.Transmit(apduSelect, 60*time.Millisecond)
	lostWith(t, err, ErrBudgetExhausted)
	untouched(t, r, f, err)
}

// The budget that runs out between two messages of a command ends the session before the next message goes out.
func TestBudgetEndsBeforeTheNextTransfer(t *testing.T) {
	r, f := openPowered(t, Options{}, func(f *fakeDevice, m []byte) {
		if m[0] == msgXfrBlock {
			time.Sleep(40 * time.Millisecond)
		}
		cardHandler(echoSW)(f, m)
	})
	n := f.nIn
	_, err := r.Transmit(apduSelect, 20*time.Millisecond)
	lostWith(t, err, ErrBudgetExhausted)
	if f.nIn != n {
		t.Errorf("a read was started with no budget left")
	}
	if errors.Is(err, usbccid.ErrTimeout) {
		t.Errorf("no transfer timed out: %v", err)
	}
}

// ---- timeouts and failure modes ----

func TestTransferTimeoutsFollowTheOptions(t *testing.T) {
	r, f := openPowered(t, Options{TransferTimeout: 7 * time.Millisecond}, cardHandler(echoSW))
	if _, err := r.Transmit(apduSelect, testBudget); err != nil {
		t.Fatal(err)
	}
	for _, d := range f.timeouts {
		if d <= 0 || d > 7*time.Millisecond {
			t.Fatalf("transfer timeout %v with TransferTimeout 7ms", d)
		}
	}

	// Default: ten seconds at most, and well above zero.
	_, f = openPowered(t, Options{}, cardHandler(echoSW))
	for _, d := range f.timeouts[2:] { // the first two are the reads of the drain
		if d <= 5*time.Second || d > DefaultTransferTimeout {
			t.Fatalf("default transfer timeout %v", d)
		}
	}

	// The budget cuts a transfer timeout that is longer.
	r, f = openPowered(t, Options{TransferTimeout: time.Minute}, cardHandler(echoSW))
	f.mu.Lock()
	f.timeouts = nil
	f.mu.Unlock()
	if _, err := r.Transmit(apduSelect, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	for _, d := range f.timeouts {
		if d > 50*time.Millisecond {
			t.Fatalf("transfer timeout %v with budget 50ms", d)
		}
	}
}

// A transfer timeout that the budget cut is the budget running out; one that the transfer timeout set is a plain timeout.
func TestTimeoutKindsAreTold(t *testing.T) {
	silent := func(f *fakeDevice, m []byte) {
		if m[0] != msgXfrBlock {
			cardHandler(echoSW)(f, m)
		}
	}
	r, _ := openPowered(t, Options{}, silent)
	_, err := r.Transmit(apduSelect, 30*time.Millisecond) // the transfer timeout (10 s) is cut to the budget
	lostWith(t, err, usbccid.ErrTimeout)
	if !errors.Is(err, ErrBudgetExhausted) {
		t.Errorf("a timeout cut by the budget: %v", err)
	}
	r, _ = openPowered(t, Options{TransferTimeout: time.Millisecond}, silent)
	_, err = r.Transmit(apduSelect, testBudget)
	lostWith(t, err, usbccid.ErrTimeout)
	if errors.Is(err, ErrBudgetExhausted) {
		t.Errorf("a timeout of the transfer is not the budget: %v", err)
	}
}

// A stall, a timeout, a gone device, a closed port or an unknown error leaves the stream in an unknown state: the session is lost, the
// error shows its cause, and the Reader does not touch the port again except to close it.
func TestFailureOfThePortLosesTheSession(t *testing.T) {
	type fail struct {
		name string
		err  error
		in   bool // inject on bulk-in (otherwise on bulk-out)
	}
	other := errors.New("fake: an error of no known kind")
	for _, c := range []fail{
		{"stall on read", usbccid.ErrStalled, true},
		{"stall on write", usbccid.ErrStalled, false},
		{"timeout on read", usbccid.ErrTimeout, true},
		{"timeout on write", usbccid.ErrTimeout, false},
		{"overflow on read", errOverflow, true},
		{"device gone on read", usbccid.ErrGone, true},
		{"device gone on write", usbccid.ErrGone, false},
		{"port closed on read", usbccid.ErrClosed, true},
		{"port closed on write", usbccid.ErrClosed, false},
		{"unknown on read", other, true},
		{"unknown on write", other, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, f := openPowered(t, Options{}, cardHandler(echoSW))
			baseIn, baseOut := f.nIn, f.nOut
			if c.in {
				f.inFail = func(n int) error {
					if n > baseIn {
						return c.err
					}
					return nil
				}
			} else {
				f.outFail = func(n int) error {
					if n > baseOut {
						return c.err
					}
					return nil
				}
			}
			_, err := r.Transmit(apduSelect, testBudget)
			lostWith(t, err, c.err)
			untouched(t, r, f, err)
			// Never reopened: descriptors read once, interface claimed once.
			if f.count("descriptors") != 1 || f.count("claim") != 1 {
				t.Errorf("descriptors read %d times, claimed %d times", f.count("descriptors"), f.count("claim"))
			}
			// Close still releases and closes the port, once.
			if err := r.Close(); err != nil {
				t.Errorf("close: %v", err)
			}
			if f.count("release") != 1 || f.count("close") != 1 || !f.closed {
				t.Errorf("release %d, close %d", f.count("release"), f.count("close"))
			}
		})
	}
}

// Every method ends the session on a failure of the port, not only Transmit.
func TestEveryCallLosesTheSessionOnAFailure(t *testing.T) {
	calls := map[string]func(r *Reader) error{
		"SlotStatus": func(r *Reader) error { _, err := r.SlotStatus(testBudget); return err },
		"PowerOn":    func(r *Reader) error { _, err := r.PowerOn(testBudget); return err },
		"PowerOff":   func(r *Reader) error { return r.PowerOff(testBudget) },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			r, f, err := openFake(Options{}, cardHandler(echoSW))
			if err != nil {
				t.Fatal(err)
			}
			f.inFail = func(int) error { return usbccid.ErrStalled }
			err = call(r)
			lostWith(t, err, usbccid.ErrStalled)
			untouched(t, r, f, err)
		})
	}
}

// ENODEV in the middle of a message: the header came, the body did not.
func TestDeviceGoneInTheMiddleOfAMessage(t *testing.T) {
	h := func(f *fakeDevice, m []byte) {
		if m[0] != msgXfrBlock {
			cardHandler(echoSW)(f, m)
			return
		}
		// 200 bytes of data: the first packet queued, then the device vanishes.
		full := msg(rspDataBlock, m[6], stOK, 0, 0, bytes.Repeat([]byte{1}, 200))
		f.sendRaw(full[:64])
		nextRead := f.nIn + 2
		f.inFail = func(n int) error {
			if n >= nextRead {
				return usbccid.ErrGone
			}
			return nil
		}
	}
	r, f := openPowered(t, Options{}, h)
	_, err := r.Transmit(apduSelect, testBudget)
	lostWith(t, err, usbccid.ErrGone)
	untouched(t, r, f, err)
}

func TestNoAnswerIsATimeoutThenLost(t *testing.T) {
	r, f := openPowered(t, Options{}, func(f *fakeDevice, m []byte) {
		if m[0] != msgXfrBlock {
			cardHandler(echoSW)(f, m)
		}
	})
	_, err := r.Transmit(apduSelect, testBudget)
	lostWith(t, err, usbccid.ErrTimeout)
	outs := len(f.outs)
	untouched(t, r, f, err)
	if len(f.outs) != outs {
		t.Error("a command was sent after a timeout")
	}
}

// The reader never sends an abort, a power-off or anything else to repair a lost session.
func TestALostSessionSendsNothingToRepairIt(t *testing.T) {
	r, f := openPowered(t, Options{}, func(f *fakeDevice, m []byte) {
		if m[0] != msgXfrBlock {
			cardHandler(echoSW)(f, m)
		}
	})
	n := len(f.outs)
	_, err := r.Transmit(apduSelect, testBudget)
	lostWith(t, err, usbccid.ErrTimeout)
	if len(f.outs) != n+1 {
		t.Errorf("%d messages after the failed command, want only the command", len(f.outs)-n-1)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	for _, ty := range f.outTypes() {
		if ty == 0x72 || ty == msgIccPowerOff {
			t.Errorf("message type %#x sent", ty)
		}
	}
}

// ---- short reads, short writes and damaged messages ----

// A header that arrives cut by a short packet is a message that ended: this layer does not wait for the rest.
func TestHeaderCutByAShortPacketIsAProtocolError(t *testing.T) {
	h := func(f *fakeDevice, m []byte) {
		if m[0] != msgXfrBlock {
			cardHandler(echoSW)(f, m)
			return
		}
		full := msg(rspDataBlock, m[6], stOK, 0, 0, []byte{0xAA, 0xBB, 0x90, 0})
		f.sendRaw(full[:4], full[4:7], full[7:])
	}
	r, f := openPowered(t, Options{}, h)
	_, err := r.Transmit(apduSelect, testBudget)
	lostWith(t, err, ErrProtocol)
	if !strings.Contains(err.Error(), "inside its header") {
		t.Errorf("err = %v", err)
	}
	untouched(t, r, f, err)
}

// A message cut short (dwLength says 100, the device sent 40 and stopped) is a protocol error at once, not a timeout.
func TestTruncatedMessageIsAProtocolError(t *testing.T) {
	h := func(f *fakeDevice, m []byte) {
		if m[0] != msgXfrBlock {
			cardHandler(echoSW)(f, m)
			return
		}
		full := msg(rspDataBlock, m[6], stOK, 0, 0, bytes.Repeat([]byte{1}, 100))
		f.sendRaw(full[:40])
	}
	r, f := openPowered(t, Options{}, h)
	start := time.Now()
	_, err := r.Transmit(apduSelect, testBudget)
	lostWith(t, err, ErrProtocol)
	if errors.Is(err, usbccid.ErrTimeout) || time.Since(start) > time.Second {
		t.Errorf("a cut message should end at once: %v after %v", err, time.Since(start))
	}
	untouched(t, r, f, err)
}

// A message cut short in its second part (the first packet full, the rest short of dwLength).
func TestTruncatedSecondPartIsAProtocolError(t *testing.T) {
	h := func(f *fakeDevice, m []byte) {
		if m[0] != msgXfrBlock {
			cardHandler(echoSW)(f, m)
			return
		}
		full := msg(rspDataBlock, m[6], stOK, 0, 0, bytes.Repeat([]byte{1}, 150))
		f.sendRaw(full[:64], full[64:100])
	}
	r, f := openPowered(t, Options{}, h)
	_, err := r.Transmit(apduSelect, testBudget)
	lostWith(t, err, ErrProtocol)
	untouched(t, r, f, err)
}

func TestDamagedMessagesAreProtocolErrors(t *testing.T) {
	cases := []struct {
		name  string
		reply func(f *fakeDevice, seq byte)
	}{
		{"more bytes than dwLength", func(f *fakeDevice, seq byte) {
			m := msg(rspDataBlock, seq, stOK, 0, 0, []byte{1, 2, 3, 4, 5, 6})
			m[1] = 2 // dwLength says 2, six arrive
			f.sendRaw(m)
		}},
		{"dwLength beyond the reader's maximum", func(f *fakeDevice, seq byte) {
			m := msg(rspDataBlock, seq, stOK, 0, 0, nil)
			m[2] = 0x10 // 4096 bytes announced, the maximum is 271 with its header
			f.sendRaw(m)
		}},
		{"dwLength one over the maximum", func(f *fakeDevice, seq byte) {
			f.send(msg(rspDataBlock, seq, stOK, 0, 0, bytes.Repeat([]byte{1}, 262)))
		}},
		{"dwLength of 4 GiB", func(f *fakeDevice, seq byte) {
			m := msg(rspDataBlock, seq, stOK, 0, 0, nil)
			m[1], m[2], m[3], m[4] = 0xFF, 0xFF, 0xFF, 0xFF
			f.sendRaw(m)
		}},
		{"dwLength with the sign bit set", func(f *fakeDevice, seq byte) {
			m := msg(rspDataBlock, seq, stOK, 0, 0, nil)
			m[4] = 0x80
			f.sendRaw(m)
		}},
		{"wrong message type", func(f *fakeDevice, seq byte) {
			f.sendRaw(msg(rspSlotStatus, seq, stOK, 0, 0, nil))
		}},
		{"another slot", func(f *fakeDevice, seq byte) {
			m := msg(rspDataBlock, seq, stOK, 0, 0, []byte{0x90, 0})
			m[5] = 1
			f.sendRaw(m)
		}},
		{"reserved command status", func(f *fakeDevice, seq byte) {
			f.sendRaw(msg(rspDataBlock, seq, 0xC0, 0, 0, nil))
		}},
		{"only stale sequence numbers", func(f *fakeDevice, seq byte) {
			for i := range 6 {
				f.sendRaw(msg(rspDataBlock, seq-1-byte(i), stOK, 0, 0, []byte{0x90, 0}))
			}
		}},
		{"unknown chain parameter", func(f *fakeDevice, seq byte) {
			f.sendRaw(msg(rspDataBlock, seq, stOK, 0, 0x55, []byte{0x90, 0}))
		}},
		{"chain end marker without a chain", func(f *fakeDevice, seq byte) {
			f.sendRaw(msg(rspDataBlock, seq, stOK, 0, chainEmptyAck, nil))
		}},
		{"only empty reads", func(f *fakeDevice, _ byte) {
			f.sendRaw([]byte{}, []byte{}, []byte{}, []byte{}, []byte{}, []byte{})
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := func(f *fakeDevice, m []byte) {
				if m[0] == msgXfrBlock {
					c.reply(f, m[6])
					return
				}
				cardHandler(echoSW)(f, m)
			}
			r, f := openPowered(t, Options{}, h)
			_, err := r.Transmit(apduSelect, testBudget)
			lostWith(t, err, ErrProtocol)
			untouched(t, r, f, err)
		})
	}
}

// A message of exactly the declared maximum is accepted.
func TestMessageOfTheMaximumLengthIsAccepted(t *testing.T) {
	data := bytes.Repeat([]byte{1}, 261)
	r, _ := openPowered(t, Options{}, cardHandler(func([]byte) []byte { return data }))
	got, err := r.Transmit(apduSelect, testBudget)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("%d bytes, %v", len(got), err)
	}
}

func TestStaleAnswersBelowTheLimitAreSkipped(t *testing.T) {
	h := func(f *fakeDevice, m []byte) {
		if m[0] != msgXfrBlock {
			cardHandler(echoSW)(f, m)
			return
		}
		seq := m[6]
		for i := range MaxStaleAnswers {
			f.send(msg(rspDataBlock, seq-1-byte(i), stOK, 0, 0, []byte{0xDE, 0xAD}))
		}
		f.send(msg(rspDataBlock, seq, stOK, 0, 0, []byte{0x90, 0}))
	}
	r, _ := openPowered(t, Options{}, h)
	for range 3 { // the count is per exchange
		got, err := r.Transmit(apduSelect, testBudget)
		if err != nil || !bytes.Equal(got, []byte{0x90, 0}) {
			t.Fatalf("%x %v", got, err)
		}
	}
}

func TestOneStaleAnswerOverTheLimitLosesTheSession(t *testing.T) {
	h := func(f *fakeDevice, m []byte) {
		if m[0] != msgXfrBlock {
			cardHandler(echoSW)(f, m)
			return
		}
		seq := m[6]
		for i := range MaxStaleAnswers + 1 {
			f.send(msg(rspDataBlock, seq+1+byte(i), stOK, 0, 0, []byte{0xDE, 0xAD}))
		}
		f.send(msg(rspDataBlock, seq, stOK, 0, 0, []byte{0x90, 0}))
	}
	r, f := openPowered(t, Options{}, h)
	_, err := r.Transmit(apduSelect, testBudget)
	lostWith(t, err, ErrProtocol)
	if !strings.Contains(err.Error(), "MaxStaleAnswers") {
		t.Errorf("the refusal does not name its limit: %v", err)
	}
	untouched(t, r, f, err)
}

// A short write ends the session, whatever the call.
func TestShortWriteLosesTheSession(t *testing.T) {
	r, f, err := openFake(Options{}, cardHandler(echoSW))
	if err != nil {
		t.Fatal(err)
	}
	f.shortOut = true
	_, err = r.PowerOn(testBudget)
	lostWith(t, err, ErrShortWrite)
	untouched(t, r, f, err)
	if err := r.Close(); err != nil {
		t.Error(err)
	}
	if f.count("release") != 1 || !f.closed {
		t.Error("port not released and closed")
	}

	r, f = openPowered(t, Options{}, cardHandler(echoSW))
	f.shortOut = true
	_, err = r.Transmit(apduSelect, testBudget)
	lostWith(t, err, ErrShortWrite)
	untouched(t, r, f, err)
}

// ---- card-level failure ----

func TestCommandFailedKeepsTheSessionUsable(t *testing.T) {
	n := 0
	h := func(f *fakeDevice, m []byte) {
		if m[0] != msgXfrBlock {
			cardHandler(echoSW)(f, m)
			return
		}
		n++
		if n == 1 {
			f.send(msg(rspDataBlock, m[6], stFailed|iccPresent, 0xFE, 0, nil))
			return
		}
		f.send(msg(rspDataBlock, m[6], stOK, 0, 0, []byte{0x90, 0}))
	}
	r, _ := openPowered(t, Options{}, h)
	_, err := r.Transmit(apduSelect, testBudget)
	var ce *CommandError
	if !errors.As(err, &ce) || ce.Code != 0xFE || ce.ICCStatus != 0 || ce.Op != "XfrBlock" {
		t.Fatalf("err = %v", err)
	}
	if errors.Is(err, ErrSessionLost) {
		t.Error("a failure that answers the only message does not end the session")
	}
	if !errors.Is(err, ErrCommandFailed) || !errors.Is(err, ErrCardMute) {
		t.Errorf("sentinels: %v", err)
	}
	if !strings.Contains(err.Error(), "ICC_MUTE") {
		t.Errorf("error text lacks the name: %v", err)
	}
	got, err := r.Transmit(apduSelect, testBudget)
	if err != nil || !bytes.Equal(got, []byte{0x90, 0}) {
		t.Fatalf("after a card-level failure: %x %v", got, err)
	}
}

// A failure whose ICC status says that the card is inactive or gone leaves the stream in order and the card unpowered.
func TestCommandFailureOfAnInactiveCardNeedsPowerOn(t *testing.T) {
	for _, tc := range []struct {
		icc  byte
		want error
	}{{iccInact, nil}, {iccNone, ErrNoCard}} {
		failing := true
		h := func(f *fakeDevice, m []byte) {
			if m[0] == msgXfrBlock && failing {
				failing = false
				f.send(msg(rspDataBlock, m[6], stFailed|tc.icc, 0xFE, 0, nil))
				return
			}
			cardHandler(echoSW)(f, m)
		}
		r, f := openPowered(t, Options{}, h)
		_, err := r.Transmit(apduSelect, testBudget)
		if !errors.Is(err, ErrCardMute) || errors.Is(err, ErrSessionLost) {
			t.Fatalf("err = %v", err)
		}
		if tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("icc %d: err = %v, want %v", tc.icc, err, tc.want)
		}
		mark := f.mark()
		if _, err := r.Transmit(apduSelect, testBudget); !errors.Is(err, ErrNotPowered) {
			t.Errorf("a command to a card that is off: %v", err)
		}
		if len(f.callsAfter(mark)) != 0 {
			t.Error("a command went to a card that is off")
		}
		if _, err := r.PowerOn(testBudget); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Transmit(apduSelect, testBudget); err != nil {
			t.Errorf("after power on: %v", err)
		}
	}
}

// Each status the reader reports is a named error with the status bytes, and no APDU byte is in the text.
func TestCommandErrorNamesAndSentinels(t *testing.T) {
	cases := []struct {
		code uint8
		want []error
		name string
	}{
		{0xFE, []error{ErrCardMute}, "ICC_MUTE"},
		{0xFD, []error{ErrCardParity}, "XFR_PARITY_ERROR"},
		{0xF8, []error{ErrCardProtocol}, "BAD_ATR_TS"},
		{0xF7, []error{ErrCardProtocol}, "BAD_ATR_TCK"},
		{0xF6, []error{ErrCardProtocol}, "ICC_PROTOCOL_NOT_SUPPORTED"},
		{0xF5, []error{ErrCardProtocol}, "ICC_CLASS_NOT_SUPPORTED"},
		{0xF4, []error{ErrCardProtocol}, "PROCEDURE_BYTE_CONFLICT"},
		{0xF3, []error{ErrCardProtocol}, "DEACTIVATED_PROTOCOL"},
		{0xF2, []error{ErrSlotBusy}, "BUSY_WITH_AUTO_SEQUENCE"},
		{0xE0, []error{ErrSlotBusy}, "SLOT_BUSY"},
		{0xFB, []error{ErrReaderFault}, "HW_ERROR"},
		{0xFC, []error{ErrReaderFault}, "XFR_OVERRUN"},
		{0x00, []error{ErrCommandNotSupported}, "command not supported"},
		{0x07, []error{ErrBadParameter}, "bad parameter"},
		{0x7F, []error{ErrBadParameter}, "bad parameter"},
		{0xFF, nil, "CMD_ABORTED"},
		{0x80, nil, "unknown"},
		{0x42 + 0x40, nil, "unknown"},
	}
	for _, c := range cases {
		e := &CommandError{Op: "XfrBlock", Code: c.code, ICCStatus: 1}
		if !errors.Is(e, ErrCommandFailed) || errors.Is(e, ErrNoCard) {
			t.Errorf("code %#x: base sentinels wrong", c.code)
		}
		for _, w := range c.want {
			if !errors.Is(e, w) {
				t.Errorf("code %#x: %v is not %v", c.code, e, w)
			}
		}
		if len(c.want) == 0 {
			for _, s := range []error{ErrCardMute, ErrCardParity, ErrCardProtocol, ErrSlotBusy, ErrReaderFault, ErrCommandNotSupported, ErrBadParameter} {
				if errors.Is(e, s) {
					t.Errorf("code %#x: matches %v", c.code, s)
				}
			}
		}
		if !strings.Contains(e.Error(), c.name) || !strings.Contains(e.Error(), fmt.Sprintf("0x%02X", c.code)) {
			t.Errorf("code %#x: text %q", c.code, e.Error())
		}
	}
	if !errors.Is(&CommandError{ICCStatus: 2}, ErrNoCard) {
		t.Error("ICC status 2 is not ErrNoCard")
	}
}

// A command APDU can carry a PIN: no error text holds a byte of the command or of the response.
func TestNoErrorTextHoldsAnAPDU(t *testing.T) {
	pin := []byte{0x00, 0x20, 0x00, 0x80, 0x08, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0xFF, 0xFF}
	secret := []byte{0xC0, 0xFF, 0xEE, 0x5E, 0xCA, 0xFE}
	hexes := func(b []byte) []string {
		return []string{fmt.Sprintf("%x", b), fmt.Sprintf("%X", b), fmt.Sprintf("%v", b)}
	}
	check := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s: no error", name)
			return
		}
		for _, s := range append(append(hexes(pin), hexes(secret)...), "313233343536", "123456", "C0FFEE", "c0ffee") {
			if strings.Contains(err.Error(), s) {
				t.Errorf("%s: the error text holds %q: %v", name, s, err)
			}
		}
	}
	type scenario struct {
		name string
		h    func(f *fakeDevice, m []byte)
		tune func(f *fakeDevice)
	}
	reply := func(build func(seq byte) [][]byte) func(f *fakeDevice, m []byte) {
		return func(f *fakeDevice, m []byte) {
			if m[0] != msgXfrBlock {
				cardHandler(echoSW)(f, m)
				return
			}
			f.sendRaw(build(m[6])...)
		}
	}
	for _, s := range []scenario{
		{"failed", reply(func(seq byte) [][]byte { return [][]byte{msg(rspDataBlock, seq, stFailed, 0xFE, 0, secret)} }), nil},
		{"wrong type", reply(func(seq byte) [][]byte { return [][]byte{msg(rspSlotStatus, seq, stOK, 0, 0, secret)} }), nil},
		{"bad chain", reply(func(seq byte) [][]byte { return [][]byte{msg(rspDataBlock, seq, stOK, 0, 0x55, secret)} }), nil},
		{"over the maximum", reply(func(seq byte) [][]byte {
			return [][]byte{msg(rspDataBlock, seq, stOK, 0, 0, append(bytes.Repeat([]byte{1}, 300), secret...))}
		}), nil},
		{"short write", cardHandler(echoSW), func(f *fakeDevice) { f.shortOut = true }},
		{"timeout", func(f *fakeDevice, m []byte) {
			if m[0] != msgXfrBlock {
				cardHandler(echoSW)(f, m)
			}
		}, nil},
		{"port error", cardHandler(echoSW), func(f *fakeDevice) {
			f.outFail = func(int) error { return fmt.Errorf("fake: %w", usbccid.ErrStalled) }
		}},
	} {
		r, f := openPowered(t, Options{}, s.h)
		if s.tune != nil {
			s.tune(f)
		}
		resp, err := r.Transmit(pin, 20*time.Millisecond)
		check(s.name, err)
		_ = resp
	}
	// The refusals of arguments.
	r, _ := openPowered(t, Options{}, cardHandler(echoSW))
	_, err := r.Transmit(append(pin, make([]byte, MaxCommandAPDU)...), testBudget)
	check("too long", err)
	_, err = r.Transmit(pin[:3], testBudget)
	check("too short", err)
}

// ---- open: refusals ----

func TestOpenBusyClaimSendsNothing(t *testing.T) {
	f := newFake(descSpec{}, cardHandler(echoSW))
	f.claimErr = usbccid.ErrBusy
	r, err := Open(f, Options{})
	if r != nil || !errors.Is(err, usbccid.ErrBusy) {
		t.Fatalf("r=%v err=%v", r, err)
	}
	if f.count("out") != 0 || f.count("in") != 0 {
		t.Errorf("busy claim still did I/O: %v", f.calls)
	}
	if !f.closed {
		t.Error("port not closed")
	}
	if f.count("claim") != 1 {
		t.Errorf("claim tried %d times", f.count("claim"))
	}
}

func TestOpenRefusalsClosePortBeforeTheClaim(t *testing.T) {
	cases := []struct {
		name string
		spec descSpec
		bad  error
	}{
		{"TPDU level", descSpec{features: 0x00010000 | 0x2}, ErrExchangeLevel},
		{"character level", descSpec{features: 0x000000B6}, ErrExchangeLevel},
		{"several slots", descSpec{maxSlot: 3}, ErrSeveralSlots},
		{"no automatic parameters", descSpec{features: 0x00040000}, ErrParameters},
		{"tiny message length", descSpec{maxMsg: 20}, ErrBadClass},
		{"no class descriptor", descSpec{noClass: true}, ErrBadClass},
		{"several interfaces", descSpec{second: true}, ErrSeveralInterfaces},
		{"no interface", descSpec{noBulkIn: true}, ErrNoInterface},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(c.spec, cardHandler(echoSW))
			r, err := Open(f, Options{})
			if r != nil || !errors.Is(err, c.bad) {
				t.Fatalf("r=%v err=%v", r, err)
			}
			if f.count("claim") != 0 || f.count("out") != 0 || f.count("release") != 0 || !f.closed {
				t.Errorf("calls %v", f.calls)
			}
		})
	}
}

func TestExchangeLevelRefusalSaysTheLevelAndTheNumbers(t *testing.T) {
	for _, c := range []struct {
		features uint32
		level    string
	}{{0x00010000 | 2, "TPDU"}, {0x000100B6, "TPDU"}, {0x000000B6, "character"}} {
		f := newFake(descSpec{features: c.features}, nil)
		_, err := Open(f, Options{})
		if !errors.Is(err, ErrExchangeLevel) {
			t.Fatalf("features %#x: err = %v", c.features, err)
		}
		for _, want := range []string{c.level, fmt.Sprintf("0x%08x", c.features), "271"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error text lacks %q: %v", want, err)
			}
		}
	}
}

func TestShortAPDUReaderIsAccepted(t *testing.T) {
	f := newFake(descSpec{features: featuresShort}, cardHandler(func(cmd []byte) []byte {
		if len(cmd) > 20 {
			return []byte{0x90, 0}
		}
		return echoSW(cmd)
	}))
	r, err := Open(f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if info := r.Info(); info.ExtendedAPDU || info.MaxCommandAPDU != MaxShortCommandAPDU {
		t.Errorf("info = %+v", info)
	}
	if _, err := r.PowerOn(testBudget); err != nil {
		t.Fatal(err)
	}
	// Largest short APDU: CLA INS P1 P2 Lc(255) data(255) Le = 5+255+1 bytes.
	big := make([]byte, 261)
	big[4] = 255
	if _, err := r.Transmit(big, testBudget); err != nil {
		t.Fatalf("261-byte command: %v", err)
	}
	n := f.nOut
	_, err = r.Transmit(make([]byte, 262), testBudget)
	if !errors.Is(err, ErrAPDUTooLong) || !strings.Contains(err.Error(), "261") {
		t.Errorf("262-byte command: %v", err)
	}
	if f.nOut != n {
		t.Error("an oversize command was sent")
	}
	// The refusal is not a transport failure: the reader still works.
	if _, err := r.Transmit(apduSelect, testBudget); err != nil {
		t.Errorf("after the refusal: %v", err)
	}
	// A short APDU reader never gets the blocks of a chain, whatever its message length.
	for _, out := range f.outs {
		if out[0] == msgXfrBlock && (out[8] != 0 || out[9] != 0) {
			t.Errorf("a chained block at short APDU level: %x", out[:10])
		}
	}
}

func TestOpenDescriptorErrorClosesThePort(t *testing.T) {
	f := newFake(descSpec{}, nil)
	f.descErr = usbccid.ErrGone
	r, err := Open(f, Options{})
	if r != nil || !errors.Is(err, usbccid.ErrGone) || !f.closed {
		t.Errorf("err=%v closed=%v", err, f.closed)
	}
}

func TestOpenRefusesABadTransferTimeout(t *testing.T) {
	for _, d := range []time.Duration{-1, time.Microsecond, MaxBudget + time.Second} {
		f := newFake(descSpec{}, nil)
		r, err := Open(f, Options{TransferTimeout: d})
		if r != nil || !errors.Is(err, ErrBadBudget) || !f.closed || f.count("descriptors") != 0 {
			t.Errorf("%v: err=%v calls=%v", d, err, f.calls)
		}
	}
}

func TestOpenWithACandidate(t *testing.T) {
	f := newFake(descSpec{second: true}, cardHandler(echoSW))
	c := usbccid.Candidate{VendorID: 0x7777, ProductID: 1, Interface: 3, BulkOut: 0x06, BulkIn: 0x86}
	// The fake only serves its own endpoints (2, 0x82), so only the choice of interface and the claim are looked at.
	r, err := Open(f, Options{Candidate: &c})
	if err != nil {
		t.Fatal(err)
	}
	if info := r.Info(); info.Interface != 3 || info.BulkOut != 0x06 || info.BulkIn != 0x86 {
		t.Errorf("info = %+v", info)
	}
	f = newFake(descSpec{}, nil)
	bad := usbccid.Candidate{VendorID: 0x7777, ProductID: 1, Interface: 2, BulkOut: 0x02, BulkIn: 0x84}
	if _, err := Open(f, Options{Candidate: &bad}); !errors.Is(err, usbccid.ErrWrongDevice) || !f.closed || f.count("claim") != 0 {
		t.Errorf("err=%v calls=%v", err, f.calls)
	}
}

// ---- slot status, power on, power off ----

func TestSlotStatus(t *testing.T) {
	for _, tc := range []struct {
		icc  byte
		want SlotState
	}{{iccPresent, SlotActive}, {iccInact, SlotInactive}, {iccNone, SlotEmpty}} {
		r, f, err := openFake(Options{}, func(f *fakeDevice, m []byte) { f.send(msg(rspSlotStatus, m[6], stOK|tc.icc, 0, 0, nil)) })
		if err != nil {
			t.Fatal(err)
		}
		st, err := r.SlotStatus(testBudget)
		if err != nil || st != tc.want {
			t.Errorf("icc %d: %v, %v", tc.icc, st, err)
		}
		if !bytes.Equal(f.outTypes(), []byte{msgGetSlotState}) {
			t.Errorf("sent %x", f.outTypes())
		}
		if tc.want.String() == "" {
			t.Error("no text")
		}
	}
	if !strings.Contains(SlotState(9).String(), "9") {
		t.Error("an unknown state has no number in its text")
	}
	// The reserved value 3 is a protocol error.
	r, f, err := openFake(Options{}, func(f *fakeDevice, m []byte) { f.send(msg(rspSlotStatus, m[6], stOK|3, 0, 0, nil)) })
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.SlotStatus(testBudget)
	lostWith(t, err, ErrProtocol)
	untouched(t, r, f, err)
}

func TestNoCardInTheSlot(t *testing.T) {
	f := newFake(descSpec{}, func(f *fakeDevice, m []byte) {
		f.send(msg(rspSlotStatus, m[6], iccNone, 0, 0, nil))
	})
	r, err := Open(f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.PowerOn(testBudget)
	if !errors.Is(err, ErrNoCard) || errors.Is(err, ErrSessionLost) {
		t.Fatalf("err = %v", err)
	}
	if !bytes.Equal(f.outTypes(), []byte{msgGetSlotState}) {
		t.Errorf("sent %x; PowerOn must not follow an empty slot", f.outTypes())
	}
	// Not lost: the card may be put in and the call repeated.
	if _, err := r.SlotStatus(testBudget); err != nil {
		t.Error(err)
	}
}

func TestPowerOnFailsAtTheCard(t *testing.T) {
	r, f, err := openFake(Options{}, func(f *fakeDevice, m []byte) {
		switch m[0] {
		case msgGetSlotState:
			f.send(msg(rspSlotStatus, m[6], stOK, 0, 0, nil))
		case msgIccPowerOn:
			f.send(msg(rspDataBlock, m[6], stFailed|iccInact, 0xFE, 0, nil))
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.PowerOn(testBudget)
	var ce *CommandError
	if !errors.As(err, &ce) || ce.Op != "IccPowerOn" || !errors.Is(err, ErrCardMute) || errors.Is(err, ErrSessionLost) {
		t.Fatalf("err = %v", err)
	}
	if _, err := r.Transmit(apduSelect, testBudget); !errors.Is(err, ErrNotPowered) {
		t.Errorf("a command to a card that did not power on: %v", err)
	}
	_ = f
}

func TestPowerOnBadATRLengths(t *testing.T) {
	for _, n := range []int{0, 1, MaxATR + 1, 200} {
		r, f, err := openFake(Options{}, func(f *fakeDevice, m []byte) {
			switch m[0] {
			case msgGetSlotState:
				f.send(msg(rspSlotStatus, m[6], stOK, 0, 0, nil))
			case msgIccPowerOn:
				f.send(msg(rspDataBlock, m[6], stOK, 0, 0, bytes.Repeat([]byte{0x3B}, n)))
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = r.PowerOn(testBudget)
		if !errors.Is(err, ErrBadATR) || errors.Is(err, ErrSessionLost) || !strings.Contains(err.Error(), fmt.Sprint(n)) {
			t.Errorf("%d bytes: %v", n, err)
		}
		if _, err := r.Transmit(apduSelect, testBudget); !errors.Is(err, ErrNotPowered) {
			t.Errorf("a command after a refused ATR: %v", err)
		}
		_ = f
	}
	for _, n := range []int{2, MaxATR} {
		r, _, err := openFake(Options{}, func(f *fakeDevice, m []byte) {
			switch m[0] {
			case msgGetSlotState:
				f.send(msg(rspSlotStatus, m[6], stOK, 0, 0, nil))
			case msgIccPowerOn:
				f.send(msg(rspDataBlock, m[6], stOK, 0, 0, bytes.Repeat([]byte{0x3B}, n)))
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		if a, err := r.PowerOn(testBudget); err != nil || len(a) != n {
			t.Errorf("%d bytes: %v", n, err)
		}
	}
}

func TestPowerOff(t *testing.T) {
	r, f := openPowered(t, Options{}, cardHandler(echoSW))
	if err := r.PowerOff(testBudget); err != nil {
		t.Fatal(err)
	}
	last := f.outs[len(f.outs)-1]
	if last[0] != msgIccPowerOff || len(last) != 10 || last[5] != 0 || last[7] != 0 || last[8] != 0 || last[9] != 0 {
		t.Errorf("sent %x", last)
	}
	if _, err := r.Transmit(apduSelect, testBudget); !errors.Is(err, ErrNotPowered) {
		t.Errorf("a command after power off: %v", err)
	}
	// A power cycle: the card answers again.
	if _, err := r.PowerOn(testBudget); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Transmit(apduSelect, testBudget); err != nil {
		t.Fatal(err)
	}
	// Power off of a card that is already off is sent and not refused.
	if err := r.PowerOff(testBudget); err != nil {
		t.Fatal(err)
	}
	if err := r.PowerOff(testBudget); err != nil {
		t.Fatal(err)
	}
}

func TestPowerOffFailureAndEmptySlot(t *testing.T) {
	r, _ := openPowered(t, Options{}, func(f *fakeDevice, m []byte) {
		if m[0] == msgIccPowerOff {
			f.send(msg(rspSlotStatus, m[6], stFailed|iccNone, 0x05, 0, nil))
			return
		}
		cardHandler(echoSW)(f, m)
	})
	err := r.PowerOff(testBudget)
	if !errors.Is(err, ErrNoCard) || !errors.Is(err, ErrBadParameter) || errors.Is(err, ErrSessionLost) {
		t.Errorf("err = %v", err)
	}
}

// ---- close ----

func TestCloseReleasesThenClosesOnce(t *testing.T) {
	r, f := openPowered(t, Options{}, cardHandler(echoSW))
	outs := len(f.outs)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	tail := f.calls[len(f.calls)-2:]
	if tail[0] != "release" || tail[1] != "close" || f.count("release") != 1 || f.count("close") != 1 {
		t.Errorf("calls: %v", f.calls)
	}
	// Close powers nothing off and sends nothing.
	if len(f.outs) != outs {
		t.Errorf("Close sent %x", f.outTypes()[outs:])
	}
	mark := f.mark()
	if _, err := r.Transmit(apduSelect, testBudget); !errors.Is(err, usbccid.ErrClosed) {
		t.Errorf("transmit after close: %v", err)
	}
	if _, err := r.PowerOn(testBudget); !errors.Is(err, usbccid.ErrClosed) {
		t.Errorf("PowerOn after close: %v", err)
	}
	if err := r.PowerOff(testBudget); !errors.Is(err, usbccid.ErrClosed) {
		t.Errorf("PowerOff after close: %v", err)
	}
	if _, err := r.SlotStatus(testBudget); !errors.Is(err, usbccid.ErrClosed) {
		t.Errorf("SlotStatus after close: %v", err)
	}
	if len(f.callsAfter(mark)) != 0 {
		t.Error("the port was used after Close")
	}
	var nilReader *Reader
	if err := nilReader.Close(); err != nil {
		t.Errorf("nil reader: %v", err)
	}
}

func TestCloseReportsReleaseErrorButStillCloses(t *testing.T) {
	r, f, err := openFake(Options{}, cardHandler(echoSW))
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("fake: release failed")
	f.relErr = cause
	if err := r.Close(); !errors.Is(err, cause) {
		t.Errorf("close error = %v", err)
	}
	if !f.closed {
		t.Error("port not closed after a release error")
	}
}

// ---- Transmit: arguments ----

func TestTransmitRefusesWhatItCannotSend(t *testing.T) {
	r, f := openPowered(t, Options{}, cardHandler(echoSW))
	n := len(f.calls)
	for _, c := range [][]byte{nil, {}, {0}, {0, 1, 2}} {
		if _, err := r.Transmit(c, testBudget); !errors.Is(err, ErrAPDUTooShort) || errors.Is(err, ErrSessionLost) {
			t.Errorf("%x: %v", c, err)
		}
	}
	for _, d := range []time.Duration{0, -time.Second, MaxBudget + 1, 24 * time.Hour} {
		if _, err := r.Transmit(apduSelect, d); !errors.Is(err, ErrBadBudget) || errors.Is(err, ErrSessionLost) || !strings.Contains(err.Error(), "MaxBudget") {
			t.Errorf("budget %v: %v", d, err)
		}
		if _, err := r.PowerOn(d); !errors.Is(err, ErrBadBudget) {
			t.Errorf("PowerOn budget %v: %v", d, err)
		}
		if err := r.PowerOff(d); !errors.Is(err, ErrBadBudget) {
			t.Errorf("PowerOff budget %v: %v", d, err)
		}
		if _, err := r.SlotStatus(d); !errors.Is(err, ErrBadBudget) {
			t.Errorf("SlotStatus budget %v: %v", d, err)
		}
	}
	if _, err := r.Transmit(apduSelect, MaxBudget); err != nil {
		t.Errorf("the ceiling itself: %v", err)
	}
	if len(f.calls) != n+2 { // the one good call: out, in
		t.Errorf("I/O for a refused call: %v", f.calls[n:])
	}
	if _, err := r.Transmit(apduSelect, testBudget); err != nil {
		t.Errorf("reader unusable after the refusals: %v", err)
	}
}

func TestTransmitBeforePowerOnIsRefused(t *testing.T) {
	r, f, err := openFake(Options{}, cardHandler(echoSW))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Transmit(apduSelect, testBudget); !errors.Is(err, ErrNotPowered) {
		t.Errorf("err = %v", err)
	}
	if f.count("out") != 0 {
		t.Error("a command was sent to a card that was never powered on")
	}
}

// The slot state that the reader reports, when it says the card is not active, takes the power state away.
func TestSlotStatusClearsThePowerState(t *testing.T) {
	inactive := false
	r, _ := openPowered(t, Options{}, func(f *fakeDevice, m []byte) {
		if m[0] == msgGetSlotState && inactive {
			f.send(msg(rspSlotStatus, m[6], stOK|iccInact, 0, 0, nil))
			return
		}
		cardHandler(echoSW)(f, m)
	})
	inactive = true
	if st, err := r.SlotStatus(testBudget); err != nil || st != SlotInactive {
		t.Fatal(st, err)
	}
	if _, err := r.Transmit(apduSelect, testBudget); !errors.Is(err, ErrNotPowered) {
		t.Errorf("err = %v", err)
	}
}

// Calls take turns; two goroutines never interleave messages.
func TestConcurrentTransmitsAreSerialised(t *testing.T) {
	r, _ := openPowered(t, Options{}, cardHandler(echoSW))
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for g := range byte(8) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := bytes.Repeat([]byte{g}, 20)
			for range 50 {
				got, err := r.Transmit(cmd, testBudget)
				if err != nil || !bytes.Equal(got[:20], cmd) {
					errs <- fmt.Errorf("goroutine %d: %x %w", g, got, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

// ---- secrets ----

// After each call the buffers of the reader that held a command or a response are zero, and the returned response is the caller's.
func TestBuffersAreClearedAfterEachCall(t *testing.T) {
	pin := append([]byte{0x00, 0x20, 0x00, 0x80, 0x08}, bytes.Repeat([]byte{0x5A}, 8)...)
	resp := append(bytes.Repeat([]byte{0xA5}, 100), 0x90, 0x00)
	r, f := openPowered(t, Options{}, cardHandler(func([]byte) []byte { return resp }))
	zero := func(when string) {
		t.Helper()
		for _, b := range [][]byte{r.wbuf, r.rbuf} {
			if !bytes.Equal(b, make([]byte, len(b))) {
				t.Errorf("%s: a buffer of the reader holds data", when)
			}
		}
	}
	zero("after PowerOn")
	got, err := r.Transmit(pin, testBudget)
	if err != nil || !bytes.Equal(got, resp) {
		t.Fatal(err)
	}
	zero("after Transmit")
	// The response is the caller's: writing in it does not touch the reader.
	got[0] = 0
	if _, err := r.SlotStatus(testBudget); err != nil {
		t.Fatal(err)
	}
	zero("after SlotStatus")
	// After a failure, also.
	f.inFail = func(int) error { return usbccid.ErrStalled }
	if _, err := r.Transmit(pin, testBudget); err == nil {
		t.Fatal("no error")
	}
	zero("after a failed Transmit")
	_ = r.Close()
	zero("after Close")
}

// A command that is chained leaves nothing in the buffers either, and a response that grew leaves no copy in the arrays it left.
func TestChainedCommandAndResponseAreCleared(t *testing.T) {
	resp := [][]byte{bytes.Repeat([]byte{0xA5}, 200), bytes.Repeat([]byte{0xA6}, 200), append(bytes.Repeat([]byte{0xA7}, 50), 0x90, 0)}
	cc := &chainCard{respParts: resp}
	r, _ := openPowered(t, Options{}, cc.handler())
	apdu := append([]byte{0, 0xDB, 0x3F, 0xFF, 0, 0x02, 0xBC}, bytes.Repeat([]byte{0x5A}, 700)...)
	if _, err := r.Transmit(apdu, testBudget); err != nil {
		t.Fatal(err)
	}
	for _, b := range [][]byte{r.wbuf, r.rbuf} {
		if !bytes.Equal(b, make([]byte, len(b))) {
			t.Error("a buffer holds data after a chained call")
		}
	}
}

func TestCollectClearsTheArrayItLeaves(t *testing.T) {
	r := &Reader{}
	first := bytes.Repeat([]byte{0xA5}, 10)
	out, err := r.collect(nil, first)
	if err != nil {
		t.Fatal(err)
	}
	old := out
	out, err = r.collect(out, bytes.Repeat([]byte{0xA6}, 10))
	if err != nil || len(out) != 20 || !bytes.Equal(out[:10], first) {
		t.Fatal(err)
	}
	if !bytes.Equal(old[:cap(old)], make([]byte, cap(old))) {
		t.Error("the array that the response left still holds it")
	}
	// The limit.
	big := make([]byte, MaxResponseAPDU)
	if _, err := r.collect(nil, big); err != nil {
		t.Errorf("the limit itself: %v", err)
	}
	r2 := &Reader{}
	if _, err := r2.collect(big, []byte{1}); !errors.Is(err, ErrResponseTooLong) || !errors.Is(err, ErrSessionLost) || !strings.Contains(err.Error(), "bytes after this block") {
		t.Errorf("over the limit: %v", err)
	}
}

// ---- chaining ----

// chainCard answers a command that arrives in blocks and a response that leaves in blocks. cmdLevels records the wLevelParameter of
// each block.
type chainCard struct {
	got       []byte
	cmdLevels []uint16
	respParts [][]byte // the response, cut into blocks; the last holds SW
	next      int
	badAck    bool
}

func (c *chainCard) handler() func(f *fakeDevice, m []byte) {
	return func(f *fakeDevice, m []byte) {
		seq := m[6]
		switch m[0] {
		case msgXfrBlock:
			lvl := uint16(m[8]) | uint16(m[9])<<8
			c.cmdLevels = append(c.cmdLevels, lvl)
			switch lvl {
			case lvlBegin, lvlMiddle:
				c.got = append(c.got, m[10:]...)
				if c.badAck {
					f.send(msg(rspDataBlock, seq, stOK, 0, chainWhole, []byte{0x90, 0}))
				} else {
					f.send(msg(rspDataBlock, seq, stOK, 0, chainEmptyAck, nil))
				}
			case lvlEnd, lvlWhole:
				c.got = append(c.got, m[10:]...)
				c.next = 0
				c.sendPart(f, seq)
			case lvlContinue:
				c.sendPart(f, seq)
			}
		default:
			cardHandler(echoSW)(f, m)
		}
	}
}

func (c *chainCard) sendPart(f *fakeDevice, seq byte) {
	p := c.respParts[c.next]
	chain := byte(chainMiddle)
	switch {
	case len(c.respParts) == 1:
		chain = chainWhole
	case c.next == 0:
		chain = chainBegin
	case c.next == len(c.respParts)-1:
		chain = chainEnd
	}
	c.next++
	f.send(msg(rspDataBlock, seq, stOK, 0, chain, p))
}

func TestExtendedAPDUSplitOverSeveralTransfers(t *testing.T) {
	resp := [][]byte{bytes.Repeat([]byte{1}, 200), bytes.Repeat([]byte{2}, 200), append(bytes.Repeat([]byte{3}, 50), 0x90, 0)}
	cc := &chainCard{respParts: resp}
	r, f := openPowered(t, Options{}, cc.handler())
	// 7-byte extended header (Lc = 700) and 700 data bytes.
	apdu := append([]byte{0, 0xDB, 0x3F, 0xFF, 0, 0x02, 0xBC}, bytes.Repeat([]byte{7}, 700)...)
	out, err := r.Transmit(apdu, testBudget)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cc.got, apdu) {
		t.Fatal("command bytes differ after reassembly")
	}
	// 707 bytes in blocks of 261: begin, middle, end; then two empty blocks ask for the rest of the response.
	if fmt.Sprint(cc.cmdLevels) != fmt.Sprint([]uint16{lvlBegin, lvlMiddle, lvlEnd, lvlContinue, lvlContinue}) {
		t.Errorf("block levels = %v", cc.cmdLevels)
	}
	if len(out) != 452 || !bytes.Equal(out[:200], resp[0]) || !bytes.Equal(out[200:400], resp[1]) || !bytes.Equal(out[400:], resp[2]) {
		t.Errorf("response of %d bytes is wrong", len(out))
	}
	for i, m := range f.outs[2:] {
		if len(m) > 271 {
			t.Errorf("message %d is %d bytes, over dwMaxCCIDMessageLength", i, len(m))
		}
		if m[0] != msgXfrBlock {
			t.Errorf("message %d has type %#x", i, m[0])
		}
	}
	// The continuation blocks are empty.
	for _, m := range f.outs[len(f.outs)-2:] {
		if len(m) != 10 {
			t.Errorf("continuation block has %d bytes", len(m))
		}
	}
	// The reader is fine afterwards.
	cc.respParts = [][]byte{{0x90, 0}}
	if got, err := r.Transmit(apduSelect, testBudget); err != nil || !bytes.Equal(got, []byte{0x90, 0}) {
		t.Errorf("next command: %x %v", got, err)
	}
}

func TestChainingBoundaries(t *testing.T) {
	const room = 271 - 10
	for _, n := range []int{room - 1, room, room + 1, 2 * room, 2*room + 1, 5000} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			cc := &chainCard{respParts: [][]byte{{0x90, 0}}}
			r, _ := openPowered(t, Options{}, cc.handler())
			apdu := bytes.Repeat([]byte{9}, n)
			if _, err := r.Transmit(apdu, testBudget); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(cc.got, apdu) {
				t.Fatal("reassembled command differs")
			}
			blocks := (n + room - 1) / room
			wantLevels := []uint16{lvlWhole}
			if blocks > 1 {
				wantLevels = []uint16{lvlBegin}
				for i := 1; i < blocks-1; i++ {
					wantLevels = append(wantLevels, lvlMiddle)
				}
				wantLevels = append(wantLevels, lvlEnd)
			}
			if fmt.Sprint(cc.cmdLevels) != fmt.Sprint(wantLevels) {
				t.Errorf("levels %v, want %v", cc.cmdLevels, wantLevels)
			}
		})
	}
}

// The largest command goes out in blocks, and a command one byte over is refused before anything is sent.
func TestTheLargestCommand(t *testing.T) {
	cc := &chainCard{respParts: [][]byte{{0x90, 0}}}
	r, f := openPowered(t, Options{}, cc.handler())
	apdu := bytes.Repeat([]byte{9}, MaxCommandAPDU)
	if _, err := r.Transmit(apdu, testBudget); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cc.got, apdu) {
		t.Fatal("reassembled command differs")
	}
	n := f.nOut
	_, err := r.Transmit(append(apdu, 1), testBudget)
	if !errors.Is(err, ErrAPDUTooLong) || errors.Is(err, ErrSessionLost) || !strings.Contains(err.Error(), "65545") {
		t.Errorf("err = %v", err)
	}
	if f.nOut != n {
		t.Error("an oversize command was sent")
	}
}

// A reader that declares a message length above the limit of one transfer is held to MaxMessageLength, and the reader never asks the
// Port for more than 64 KiB (the fake refuses a longer transfer).
func TestMessageLengthIsHeldToTheTransferLimit(t *testing.T) {
	cc := &chainCard{respParts: [][]byte{{0x90, 0}}}
	f := newFake(descSpec{maxMsg: 1 << 20, mpsIn: 512, mpsOut: 512}, cc.handler())
	r, err := Open(f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Info().MaxMessageLength; got != MaxMessageLength {
		t.Errorf("message length %d", got)
	}
	if _, err := r.PowerOn(testBudget); err != nil {
		t.Fatal(err)
	}
	apdu := bytes.Repeat([]byte{9}, MaxCommandAPDU)
	if _, err := r.Transmit(apdu, testBudget); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cc.got, apdu) {
		t.Fatal("reassembled command differs")
	}
	for _, m := range f.outs {
		if len(m) > MaxMessageLength {
			t.Fatalf("a message of %d bytes", len(m))
		}
	}
	// An answer of the largest size.
	big := bytes.Repeat([]byte{3}, MaxMessageLength-10)
	cc.respParts = [][]byte{big, {0x90, 0}}
	got, err := r.Transmit(apduSelect, testBudget)
	if err != nil || len(got) != len(big)+2 {
		t.Fatalf("%d bytes, %v", len(got), err)
	}
	for _, l := range f.inLens {
		if l <= 0 || l > MaxMessageLength {
			t.Fatalf("a read of %d bytes", l)
		}
	}
}

func TestResponseChainOfManyBlocks(t *testing.T) {
	var parts [][]byte
	var want []byte
	for i := range 20 {
		p := bytes.Repeat([]byte{byte(i)}, 100+i)
		parts = append(parts, p)
		want = append(want, p...)
	}
	cc := &chainCard{respParts: parts}
	r, f := openPowered(t, Options{}, cc.handler())
	got, err := r.Transmit(apduSelect, testBudget)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("got %d bytes, want %d, err %v", len(got), len(want), err)
	}
	if n := len(f.outs) - 2; n != 20 { // one command plus 19 continuations
		t.Errorf("%d messages for a 20-block response", n)
	}
}

func TestChainRefusals(t *testing.T) {
	apdu := bytes.Repeat([]byte{9}, 700)
	t.Run("reader does not ask for the next block", func(t *testing.T) {
		cc := &chainCard{respParts: [][]byte{{0x90, 0}}, badAck: true}
		r, f := openPowered(t, Options{}, cc.handler())
		_, err := r.Transmit(apdu, testBudget)
		lostWith(t, err, ErrProtocol)
		untouched(t, r, f, err)
	})
	t.Run("card-level failure in the middle of the command", func(t *testing.T) {
		cc := &chainCard{respParts: [][]byte{{0x90, 0}}}
		inner := cc.handler()
		blocks := 0
		r, f := openPowered(t, Options{}, func(f *fakeDevice, m []byte) {
			if m[0] == msgXfrBlock {
				blocks++
				if blocks == 2 {
					f.send(msg(rspDataBlock, m[6], stFailed|0x01, 0xFE, 0, nil))
					return
				}
			}
			inner(f, m)
		})
		_, err := r.Transmit(apdu, testBudget)
		var ce *CommandError
		if !errors.As(err, &ce) {
			t.Fatalf("err = %v", err)
		}
		lostWith(t, err, ErrCardMute)
		untouched(t, r, f, err)
	})
	t.Run("response chain that never ends", func(t *testing.T) {
		// A begin block, then middle blocks, each saying "more follows"; the reader stops at MaxResponseAPDU.
		blocks := 0
		h := func(f *fakeDevice, m []byte) {
			if m[0] != msgXfrBlock {
				cardHandler(echoSW)(f, m)
				return
			}
			chain := byte(chainMiddle)
			if blocks == 0 {
				chain = chainBegin
			}
			blocks++
			f.send(msg(rspDataBlock, m[6], stOK, 0, chain, bytes.Repeat([]byte{1}, 250)))
			f.endless = msg(rspDataBlock, m[6], stOK, 0, chainMiddle, bytes.Repeat([]byte{1}, 250))
		}
		r, f := openPowered(t, Options{}, h)
		_, err := r.Transmit(apduSelect, testBudget)
		lostWith(t, err, ErrResponseTooLong)
		if !strings.Contains(err.Error(), "bytes after this block") {
			t.Errorf("err = %v", err)
		}
		untouched(t, r, f, err)
	})
	t.Run("last block of a response with a begin marker", func(t *testing.T) {
		calls := 0
		h := func(f *fakeDevice, m []byte) {
			if m[0] != msgXfrBlock {
				cardHandler(echoSW)(f, m)
				return
			}
			calls++
			if calls == 1 {
				f.send(msg(rspDataBlock, m[6], stOK, 0, chainBegin, []byte{1}))
				return
			}
			f.send(msg(rspDataBlock, m[6], stOK, 0, 0x77, []byte{2}))
		}
		r, f := openPowered(t, Options{}, h)
		_, err := r.Transmit(apduSelect, testBudget)
		lostWith(t, err, ErrProtocol)
		untouched(t, r, f, err)
	})
}

// A one-message command whose response chain is cut by a card-level failure leaves the card in the middle of a response: the session
// is lost.
func TestFailureInsideAResponseChainLosesTheSession(t *testing.T) {
	calls := 0
	h := func(f *fakeDevice, m []byte) {
		if m[0] != msgXfrBlock {
			cardHandler(echoSW)(f, m)
			return
		}
		calls++
		switch calls {
		case 1:
			f.send(msg(rspDataBlock, m[6], stOK, 0, chainBegin, []byte{1, 2, 3}))
		case 2:
			f.send(msg(rspDataBlock, m[6], stFailed|0x01, 0xFE, 0, nil))
		default:
			f.send(msg(rspDataBlock, m[6], stOK, 0, 0, []byte{0x90, 0}))
		}
	}
	r, f := openPowered(t, Options{}, h)
	_, err := r.Transmit(apduSelect, testBudget)
	var ce *CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v", err)
	}
	lostWith(t, err, ErrCardMute)
	untouched(t, r, f, err)
}

// The message type bytes are written out here, from CCID 1.1 chapter 6, so a wrong constant in the source is caught.
func TestMessageTypeValues(t *testing.T) {
	for name, c := range map[string][2]byte{
		"PC_to_RDR_GetSlotStatus": {msgGetSlotState, 0x65},
		"PC_to_RDR_IccPowerOn":    {msgIccPowerOn, 0x62},
		"PC_to_RDR_IccPowerOff":   {msgIccPowerOff, 0x63},
		"PC_to_RDR_XfrBlock":      {msgXfrBlock, 0x6F},
		"RDR_to_PC_DataBlock":     {rspDataBlock, 0x80},
		"RDR_to_PC_SlotStatus":    {rspSlotStatus, 0x81},
	} {
		if c[0] != c[1] {
			t.Errorf("%s = %#x, want %#x", name, c[0], c[1])
		}
	}
	// wLevelParameter of XfrBlock (table 6.1-3) and bChainParameter of DataBlock.
	if lvlWhole != 0 || lvlBegin != 1 || lvlEnd != 2 || lvlMiddle != 3 || lvlContinue != 0x10 {
		t.Error("wLevelParameter values")
	}
	if chainWhole != 0 || chainBegin != 1 || chainEnd != 2 || chainMiddle != 3 || chainEmptyAck != 0x10 {
		t.Error("bChainParameter values")
	}
	if headerLen != 10 {
		t.Errorf("header length %d", headerLen)
	}
}

// The limits are what the documentation says.
func TestTheLimits(t *testing.T) {
	if MaxShortCommandAPDU != 261 || MaxCommandAPDU != 65544 || MaxResponseAPDU != 65538 || MaxStaleAnswers != 3 || MaxATR != 33 ||
		MaxBudget != 90*time.Second || DefaultTransferTimeout != 10*time.Second || MinMessageLength != 271 || MaxMessageLength != 65536 {
		t.Error("a limit changed")
	}
}

// After a begin block each response block is a middle one or the end: a whole, a second begin or any other value is out of order, and
// the reader asks for nothing more.
func TestResponseChainKeepsItsOrder(t *testing.T) {
	for _, next := range []byte{chainWhole, chainBegin, chainEmptyAck, 0x77} {
		t.Run(fmt.Sprintf("0x%02X after begin", next), func(t *testing.T) {
			calls := 0
			h := func(f *fakeDevice, m []byte) {
				if m[0] != msgXfrBlock {
					cardHandler(echoSW)(f, m)
					return
				}
				calls++
				chain := next
				if calls == 1 {
					chain = chainBegin
				}
				f.send(msg(rspDataBlock, m[6], stOK, 0, chain, []byte{0x90, 0}))
			}
			r, f := openPowered(t, Options{}, h)
			_, err := r.Transmit(apduSelect, testBudget)
			lostWith(t, err, ErrProtocol)
			if calls != 2 {
				t.Errorf("%d XfrBlock messages, want the command and one continuation", calls)
			}
			untouched(t, r, f, err)
		})
	}
}

// A reader at short APDU level has no response chain: any chain parameter but 0 ends the session, and nothing more is sent.
func TestShortLevelAnswerWithAChainEndsTheSession(t *testing.T) {
	for _, chain := range []byte{chainBegin, chainEnd, chainMiddle, chainEmptyAck} {
		t.Run(fmt.Sprintf("0x%02X", chain), func(t *testing.T) {
			f := newFake(descSpec{features: featuresShort}, func(f *fakeDevice, m []byte) {
				if m[0] == msgXfrBlock {
					f.send(msg(rspDataBlock, m[6], stOK, 0, chain, []byte{0x90, 0}))
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
			n := f.nOut
			_, err = r.Transmit(apduSelect, testBudget)
			lostWith(t, err, ErrProtocol)
			if f.nOut != n+1 {
				t.Errorf("%d messages written, want one", f.nOut-n)
			}
			untouched(t, r, f, err)
		})
	}
}

// A call whose budget is spent before its first Port call is refused with ErrBudgetExhausted and keeps the session, the power state
// and the sequence counter; the next call works.
func TestSpentBudgetBeforeTheFirstPortCallKeepsTheSession(t *testing.T) {
	calls := map[string]func(r *Reader) error{
		"SlotStatus": func(r *Reader) error { _, err := r.SlotStatus(time.Nanosecond); return err },
		"PowerOn":    func(r *Reader) error { _, err := r.PowerOn(time.Nanosecond); return err },
		"PowerOff":   func(r *Reader) error { return r.PowerOff(time.Nanosecond) },
		"Transmit":   func(r *Reader) error { _, err := r.Transmit(apduSelect, time.Nanosecond); return err },
	}
	for name, call := range calls {
		for _, first := range []bool{true, false} {
			if name == "Transmit" && first {
				continue // a Transmit needs a powered card, so it is never the first exchange
			}
			t.Run(fmt.Sprintf("%s first=%v", name, first), func(t *testing.T) {
				r, f, err := openFake(Options{}, cardHandler(echoSW))
				if err != nil {
					t.Fatal(err)
				}
				if !first {
					if _, err := r.PowerOn(testBudget); err != nil {
						t.Fatal(err)
					}
				}
				powered, drained, mark := r.powered, r.drained, f.mark()
				setSeq(41)
				err = call(r)
				if !errors.Is(err, ErrBudgetExhausted) || errors.Is(err, ErrSessionLost) {
					t.Fatalf("err = %v, want ErrBudgetExhausted and not ErrSessionLost", err)
				}
				if r.lost != nil || r.powered != powered || r.drained != drained || len(f.callsAfter(mark)) != 0 || seqs.n.Load() != 41 {
					t.Errorf("lost %v, powered %v, drained %v, port calls %v, counter %d", r.lost, r.powered, r.drained, f.callsAfter(mark), seqs.n.Load())
				}
				if _, err := r.PowerOn(testBudget); err != nil {
					t.Errorf("the next call: %v", err)
				}
			})
		}
	}
}

// At short APDU level a failed DataBlock answer with a chain parameter ends the session too, and at either level the answer to reset
// is never a chain.
func TestChainParameterInAFailedShortAnswerAndInTheATR(t *testing.T) {
	t.Run("failed answer at short level", func(t *testing.T) {
		f := newFake(descSpec{features: featuresShort}, func(f *fakeDevice, m []byte) {
			if m[0] == msgXfrBlock {
				f.send(msg(rspDataBlock, m[6], stFailed|0x01, 0xFE, chainBegin, nil))
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
		lostWith(t, err, ErrProtocol)
		untouched(t, r, f, err)
	})
	for name, feat := range map[string]uint32{"short": featuresShort, "extended": featuresExt} {
		t.Run("ATR with a chain, "+name, func(t *testing.T) {
			f := newFake(descSpec{features: feat}, func(f *fakeDevice, m []byte) {
				if m[0] == msgIccPowerOn {
					f.send(msg(rspDataBlock, m[6], stOK, 0, chainBegin, testATR))
					return
				}
				cardHandler(echoSW)(f, m)
			})
			r, err := Open(f, Options{})
			if err != nil {
				t.Fatal(err)
			}
			atr, err := r.PowerOn(testBudget)
			lostWith(t, err, ErrProtocol)
			if atr != nil || strings.Contains(err.Error(), fmt.Sprintf("%x", testATR)) {
				t.Errorf("atr %x, err %v", atr, err)
			}
			untouched(t, r, f, err)
		})
	}
}
