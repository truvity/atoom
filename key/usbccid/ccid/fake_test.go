package ccid

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/truvity/atoom/key/usbccid"
)

// This file holds the in-memory CCID device the tests run against. It implements usbccid.Port, speaks the bulk-out and bulk-in messages
// of CCID 1.1 and behaves like the kernel's usbfs bulk ioctl where it matters:
//
//   - a bulk-in transfer ends when the buffer is full or a short packet (including a zero-length packet) arrived;
//   - a packet that does not fit in what is left of the buffer is an overflow error, so a reader that asks for too little fails here
//     as it would on a real node;
//   - the limits of the Port are held: a transfer of at most 64 KiB, a read with a buffer;
//   - with nothing queued, a transfer times out.
//
// Nothing here opens a device, and no value in it names a real device: the ids are made up.

// descSpec describes the descriptors of a fake reader.
type descSpec struct {
	features  uint32 // dwFeatures; 0 means short and extended APDU level
	maxMsg    uint32 // dwMaxCCIDMessageLength; 0 means 271
	voltage   uint8  // bVoltageSupport; 0 means 0x07
	protocols uint32 // dwProtocols; 0 means T=0 and T=1
	maxSlot   uint8  // bMaxSlotIndex
	bcdCCID   uint16 // 0 means 0x0110
	ccidFirst bool   // CCID interface before the two HID interfaces
	classLen  uint8  // length of the class descriptor; 0 means 54
	noClass   bool   // no class descriptor
	twoClass  bool   // two class descriptors
	noIntr    bool   // no interrupt endpoint
	noBulkIn  bool
	mpsIn     uint16 // 0 means 64
	mpsOut    uint16 // 0 means 64
	vid, pid  uint16
	bulkOut   uint8 // 0 means 0x02
	bulkIn    uint8 // 0 means 0x82
	intr      uint8 // 0 means 0x83
	ifnum     uint8 // interface number of the CCID interface; default 2
	subclass  uint8
	protocol  uint8
	altFirst  bool // add an alternate setting 1 of the same interface before setting 0
	intrOut   bool // an interrupt OUT endpoint before the interrupt IN endpoint
	extraCfg  bool // the device declares two configurations
	second    bool // a second CCID interface (number 3, endpoints 0x06 and 0x86)
	twoBulkIn bool // a second bulk IN endpoint on the CCID interface
}

const (
	// featuresExt is a dwFeatures with the automatic parameter, clock and baud bits and extended APDU level.
	featuresExt = 0x000404B6
	// featuresShort is the same at short APDU level.
	featuresShort = 0x000204B6
)

func (s descSpec) build() []byte {
	if s.features == 0 {
		s.features = featuresExt
	}
	if s.maxMsg == 0 {
		s.maxMsg = 271
	}
	if s.voltage == 0 {
		s.voltage = 0x07
	}
	if s.protocols == 0 {
		s.protocols = 3
	}
	if s.bcdCCID == 0 {
		s.bcdCCID = 0x0110
	}
	if s.classLen == 0 {
		s.classLen = 54
	}
	if s.mpsIn == 0 {
		s.mpsIn = 64
	}
	if s.mpsOut == 0 {
		s.mpsOut = 64
	}
	if s.vid == 0 {
		s.vid, s.pid = 0x7777, 0x0001
	}
	if s.bulkOut == 0 {
		s.bulkOut = 0x02
	}
	if s.bulkIn == 0 {
		s.bulkIn = 0x82
	}
	if s.intr == 0 {
		s.intr = 0x83
	}
	if s.ifnum == 0 {
		s.ifnum = 2
	}
	nCfg := byte(1)
	if s.extraCfg {
		nCfg = 2
	}
	dev := []byte{18, 1, 0x10, 0x02, 0, 0, 0, 8}
	dev = binary.LittleEndian.AppendUint16(dev, s.vid)
	dev = binary.LittleEndian.AppendUint16(dev, s.pid)
	dev = append(dev, 0x00, 0x01, 1, 2, 0, nCfg)

	hid := func(n, ep byte) []byte {
		return []byte{9, 4, n, 0, 1, 3, 1, 1, 0, 9, 0x21, 0x11, 1, 0, 1, 0x22, 0x40, 0, 7, 5, ep, 3, 8, 0, 8}
	}
	cls := make([]byte, s.classLen)
	cls[0], cls[1] = s.classLen, 0x21
	binary.LittleEndian.PutUint16(cls[2:], s.bcdCCID)
	put32 := func(o int, v uint32) {
		if o+4 <= len(cls) {
			binary.LittleEndian.PutUint32(cls[o:], v)
		}
	}
	if len(cls) > 4 {
		cls[4] = s.maxSlot
	}
	if len(cls) > 5 {
		cls[5] = s.voltage
	}
	put32(6, s.protocols)
	put32(40, s.features)
	put32(44, s.maxMsg)
	if len(cls) > 53 {
		cls[53] = 1 // bMaxCCIDBusySlots
	}
	iface := func(num, alt, bout, bin, in byte) []byte {
		var eps []byte
		n := 0
		add := func(e ...byte) { eps = append(eps, e...); n++ }
		if s.intrOut {
			add(7, 5, 0x04, 3, 8, 0, 16)
		}
		if !s.noIntr {
			add(7, 5, in, 3, 8, 0, 16)
		}
		add(7, 5, bout, 2)
		eps = binary.LittleEndian.AppendUint16(eps, s.mpsOut)
		eps = append(eps, 0)
		if !s.noBulkIn {
			add(7, 5, bin, 2)
			eps = binary.LittleEndian.AppendUint16(eps, s.mpsIn)
			eps = append(eps, 0)
		}
		if s.twoBulkIn {
			add(7, 5, 0x85, 2)
			eps = binary.LittleEndian.AppendUint16(eps, s.mpsIn)
			eps = append(eps, 0)
		}
		b := []byte{9, 4, num, alt, byte(n), 0x0B, s.subclass, s.protocol, 0}
		if !s.noClass {
			b = append(b, cls...)
			if s.twoClass {
				b = append(b, cls...)
			}
		}
		return append(b, eps...)
	}
	ccidIf := iface(s.ifnum, 0, s.bulkOut, s.bulkIn, s.intr)
	if s.second {
		ccidIf = append(ccidIf, iface(3, 0, 0x06, 0x86, 0x87)...)
	}
	if s.altFirst {
		// Setting 1 of the same interface, with endpoints of its own.
		alt1 := []byte{9, 4, s.ifnum, 1, 2, 0x0B, 0, 0, 0}
		alt1 = append(alt1, cls...)
		alt1 = append(alt1, 7, 5, 0x05, 2, 64, 0, 0, 7, 5, 0x85, 2, 64, 0, 0)
		ccidIf = append(ccidIf, alt1...)
	}
	var body []byte
	switch {
	case s.ccidFirst:
		body = append(append(body, ccidIf...), hid(0, 0x81)...)
		body = append(body, hid(1, 0x84)...)
	default:
		body = append(append(body, hid(0, 0x81)...), hid(1, 0x84)...)
		body = append(body, ccidIf...)
	}
	nIf := byte(3)
	if s.second {
		nIf++
	}
	c := []byte{9, 2, 0, 0, nIf, 1, 0, 0x80, 50}
	total := len(c) + len(body)
	binary.LittleEndian.PutUint16(c[2:], uint16(total)) //nolint:gosec // a few hundred bytes
	return append(dev, append(c, body...)...)
}

// msg builds a bulk-in message: type, dwLength, slot 0, sequence, status, error, chain parameter, data.
func msg(typ, seq, status, errb, chain byte, data []byte) []byte {
	m := make([]byte, 10+len(data))
	m[0] = typ
	binary.LittleEndian.PutUint32(m[1:], uint32(len(data))) //nolint:gosec // test data under 64 KiB
	m[6], m[7], m[8], m[9] = seq, status, errb, chain
	copy(m[10:], data)
	return m
}

// Statuses for the bmCommandStatus byte, with bmICCStatus.
const (
	stOK       = 0x00
	stFailed   = 0x40
	stTimeExt  = 0x80
	iccPresent = 0x00
	iccInact   = 0x01
	iccNone    = 0x02
)

// testATR is a made-up answer to reset: TS, T0 with ten historical bytes, TD1 for T=0, and the text "TestCard01".
var testATR = []byte{0x3B, 0x8A, 0x80, 0x00, 'T', 'e', 's', 't', 'C', 'a', 'r', 'd', '0', '1'}

// errOverflow is what the fake answers when a packet does not fit the buffer, as the kernel's overflow error does.
var errOverflow = errors.New("fake: value too large for defined data type")

// fakeDevice is the usbccid.Port. All fields are guarded by mu, so a test that drives it from two goroutines is safe.
type fakeDevice struct {
	mu sync.Mutex

	desc     []byte
	mpsIn    int // packet size of the bulk-in endpoint; 64
	noZLP    bool
	descErr  error
	claimErr error
	relErr   error
	closeErr error

	// onOut is called with each bulk-out message after it was logged. It queues the answer with f.send.
	onOut func(f *fakeDevice, m []byte)
	// inFail and outFail inject a failure: they get the call number (from 1) and return the error for that call, or nil.
	inFail  func(n int) error
	outFail func(n int) error
	// shortOut makes BulkOut report fewer bytes than were given.
	shortOut bool
	// onIn is called before each bulk-in call (with the lock held), for a test that moves time.
	onIn func(f *fakeDevice)

	in       [][]byte // queued packets
	endless  []byte   // when the queue is empty, queue this message again
	outs     [][]byte // every bulk-out message
	calls    []string // "descriptors", "claim", "release", "out", "in", "close"
	timeouts []time.Duration
	inLens   []int // the buffer length of each bulk-in call
	nIn      int
	nOut     int
	closed   bool
	claimed  bool
}

func newFake(s descSpec, h func(f *fakeDevice, m []byte)) *fakeDevice {
	mps := s.mpsIn
	if mps == 0 {
		mps = 64
	}
	return &fakeDevice{desc: s.build(), mpsIn: int(mps), onOut: h}
}

func (f *fakeDevice) Descriptors() ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "descriptors")
	if f.descErr != nil {
		return nil, f.descErr
	}
	return append([]byte(nil), f.desc...), nil
}

func (f *fakeDevice) Claim(n int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "claim")
	if f.claimErr != nil {
		return fmt.Errorf("fake: claim interface %d: %w", n, f.claimErr)
	}
	f.claimed = true
	return nil
}

func (f *fakeDevice) Release(int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "release")
	return f.relErr
}

func (f *fakeDevice) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "close")
	f.closed = true
	return f.closeErr
}

func (f *fakeDevice) BulkOut(ep uint8, d []byte, to time.Duration) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "out")
	f.nOut++
	f.timeouts = append(f.timeouts, to)
	if ep != 0x02 {
		return 0, fmt.Errorf("fake: bulk out on endpoint 0x%02x", ep)
	}
	if len(d) > 64<<10 {
		return 0, usbccid.ErrBadLength
	}
	if f.outFail != nil {
		if err := f.outFail(f.nOut); err != nil {
			return 0, fmt.Errorf("fake: bulk transfer on endpoint 0x%02X: %w", ep, err)
		}
	}
	m := append([]byte(nil), d...)
	f.outs = append(f.outs, m)
	if f.onOut != nil {
		f.onOut(f, m)
	}
	if f.shortOut {
		return len(d) - 1, nil
	}
	return len(d), nil
}

func (f *fakeDevice) BulkIn(ep uint8, buf []byte, to time.Duration) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "in")
	f.nIn++
	f.timeouts = append(f.timeouts, to)
	f.inLens = append(f.inLens, len(buf))
	if f.onIn != nil {
		f.onIn(f)
	}
	if ep != 0x82 {
		return 0, fmt.Errorf("fake: bulk in on endpoint 0x%02x", ep)
	}
	if len(buf) == 0 || len(buf) > 64<<10 {
		return 0, usbccid.ErrBadLength
	}
	if f.inFail != nil {
		if err := f.inFail(f.nIn); err != nil {
			return 0, fmt.Errorf("fake: bulk transfer on endpoint 0x%02X: %w", ep, err)
		}
	}
	if len(f.in) == 0 && f.endless != nil {
		f.queueLocked(f.endless)
		time.Sleep(time.Millisecond)
	}
	if len(f.in) == 0 {
		return 0, fmt.Errorf("fake: bulk transfer on endpoint 0x%02X: %w", ep, usbccid.ErrTimeout)
	}
	n := 0
	for len(f.in) > 0 {
		p := f.in[0]
		if len(p) > len(buf)-n {
			return n, fmt.Errorf("fake: bulk transfer on endpoint 0x%02X: %w", ep, errOverflow)
		}
		f.in = f.in[1:]
		copy(buf[n:], p)
		n += len(p)
		if len(p) < f.mpsIn || n == len(buf) {
			break
		}
	}
	return n, nil
}

// send queues one message, cut into packets. A message that is a multiple of the packet size is followed by a zero-length packet
// unless noZLP is set.
func (f *fakeDevice) send(m []byte) { f.queueLocked(m) }

func (f *fakeDevice) queueLocked(m []byte) {
	m = append([]byte(nil), m...)
	for len(m) >= f.mpsIn {
		f.in = append(f.in, m[:f.mpsIn])
		m = m[f.mpsIn:]
		if len(m) == 0 && !f.noZLP {
			f.in = append(f.in, []byte{})
		}
	}
	if len(m) > 0 {
		f.in = append(f.in, m)
	}
}

// sendRaw queues packets exactly as given.
func (f *fakeDevice) sendRaw(packets ...[]byte) {
	for _, p := range packets {
		f.in = append(f.in, append([]byte(nil), p...))
	}
}

// outTypes lists the message type byte of every bulk-out message.
func (f *fakeDevice) outTypes() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	var t []byte
	for _, m := range f.outs {
		t = append(t, m[0])
	}
	return t
}

func (f *fakeDevice) count(call string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == call {
			n++
		}
	}
	return n
}

func (f *fakeDevice) callsAfter(i int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls[i:]...)
}

func (f *fakeDevice) mark() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// cardHandler is a reader that answers the four message types the reader may send. apdu is called for each XfrBlock with the data of
// the message; it returns the data of the DataBlock response.
func cardHandler(apdu func(cmd []byte) []byte) func(f *fakeDevice, m []byte) {
	return func(f *fakeDevice, m []byte) {
		seq := m[6]
		switch m[0] {
		case msgGetSlotState:
			f.send(msg(rspSlotStatus, seq, stOK|iccPresent, 0, 0, nil))
		case msgIccPowerOn:
			f.send(msg(rspDataBlock, seq, stOK, 0, 0, testATR))
		case msgIccPowerOff:
			f.send(msg(rspSlotStatus, seq, stOK|iccInact, 0, 0, nil))
		case msgXfrBlock:
			f.send(msg(rspDataBlock, seq, stOK, 0, 0, apdu(m[10:])))
		}
	}
}

// echoSW answers every command with the command bytes followed by 90 00.
func echoSW(cmd []byte) []byte { return append(append([]byte(nil), cmd...), 0x90, 0x00) }

// testBudget is the budget most tests give a call.
const testBudget = 10 * time.Second

// openFake opens a Reader on a fake with the default descriptors and the handler.
func openFake(opt Options, h func(f *fakeDevice, m []byte)) (*Reader, *fakeDevice, error) {
	f := newFake(descSpec{}, h)
	r, err := Open(f, opt)
	return r, f, err
}

// openPowered opens a Reader on a fake and powers the card on.
func openPowered(t interface {
	Helper()
	Fatal(...any)
}, opt Options, h func(f *fakeDevice, m []byte)) (*Reader, *fakeDevice) {
	t.Helper()
	r, f, err := openFake(opt, h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.PowerOn(testBudget); err != nil {
		t.Fatal(err)
	}
	return r, f
}
