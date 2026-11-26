package ccid

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/truvity/atoom/key/usbccid"
)

// The limits of this layer. Each is a constant of its own, and a refusal that passes one names it.
const (
	// MaxShortCommandAPDU is the largest command APDU at short APDU level: CLA INS P1 P2, Lc, 255 bytes of data, Le.
	MaxShortCommandAPDU = 4 + 1 + 255 + 1

	// MaxCommandAPDU is the largest command APDU at extended APDU level: the header, a 3-byte Lc, 65535 bytes of data and a 2-byte Le.
	// A command above it is refused with ErrAPDUTooLong before anything is sent.
	MaxCommandAPDU = 4 + 3 + 65535 + 2

	// MaxResponseAPDU is the largest response this layer collects: 65536 bytes of data and the two status bytes, over all the blocks
	// of a chain. A response above it ends the session with ErrResponseTooLong.
	MaxResponseAPDU = 65536 + 2

	// MaxStaleAnswers is the number of answers with another sequence number that an exchange skips. A fourth ends the session with
	// ErrProtocol. Such an answer is a late one, or one that is not for this host; it is read whole and dropped.
	MaxStaleAnswers = 3

	// MaxATR is the largest answer to reset that PowerOn returns (ISO/IEC 7816-3: 33 bytes). A longer one is ErrBadATR.
	MaxATR = 33

	// MaxBudget is the ceiling of the budget of one exchange: 90 seconds. It is the upper bound that the code this layer was ported
	// from put on one command, time-extension messages included. A larger budget is refused with ErrBadBudget.
	MaxBudget = 90 * time.Second

	// DefaultTransferTimeout is the timeout of one bulk transfer when Options.TransferTimeout is zero: 10 seconds. A card that
	// waits for a person keeps sending time-extension messages, so no single transfer waits that long without an answer. The
	// timeout of a transfer is also cut to what is left of the budget.
	DefaultTransferTimeout = 10 * time.Second

	// MaxDrainReads is the number of reads that the drain at the first exchange of a session makes before it gives up: eight. A device
	// that has not gone quiet by then keeps sending without being asked, and the session is lost with ErrProtocol.
	MaxDrainReads = 8

	// MaxDrainTime is the time the drain may take in all: 250 milliseconds.
	MaxDrainTime = 250 * time.Millisecond

	// DrainReadTimeout is the timeout of one read of the drain: 20 milliseconds. It is the cost of the drain on a quiet device (one
	// read that times out).
	DrainReadTimeout = 20 * time.Millisecond

	// MinMessageLength is the smallest dwMaxCCIDMessageLength this layer accepts: the 10-byte header and the largest short APDU
	// (CCID 1.1 requires at least this at short APDU level). A reader that declares less contradicts its exchange level.
	MinMessageLength = 10 + MaxShortCommandAPDU

	// MaxMessageLength is the largest CCID message this layer sends or reads: the limit of one transfer of the Port (64 KiB). A
	// reader that declares a larger dwMaxCCIDMessageLength is held to this one; a long command is then sent in more blocks.
	MaxMessageLength = 64 << 10
)

// CCID message types (CCID 1.1, chapter 6).
const (
	msgIccPowerOn   = 0x62
	msgIccPowerOff  = 0x63
	msgGetSlotState = 0x65
	msgXfrBlock     = 0x6F
	rspDataBlock    = 0x80
	rspSlotStatus   = 0x81

	headerLen = 10

	// wLevelParameter of XfrBlock at the APDU levels.
	lvlWhole    = 0x0000 // the whole command is in this message
	lvlBegin    = 0x0001 // first block, more follow
	lvlEnd      = 0x0002 // last block
	lvlMiddle   = 0x0003 // middle block
	lvlContinue = 0x0010 // empty block: send the next piece of the response

	// bChainParameter of a DataBlock response.
	chainWhole    = 0x00
	chainBegin    = 0x01
	chainEnd      = 0x02
	chainMiddle   = 0x03
	chainEmptyAck = 0x10
)

// Options tunes Open.
type Options struct {
	// Candidate, when set, is the device that Discover found. The descriptors of the Port must then hold that CCID interface with
	// those bulk endpoints (else an error that wraps usbccid.ErrWrongDevice), and the reader uses that interface when the device has
	// several; the vendor and product id of the device descriptor must be the candidate's too (a Port may have been opened without the
	// USB layer's own check). When nil, the descriptors must hold exactly one CCID interface (else ErrSeveralInterfaces). The reader
	// takes the interface and the endpoints from the descriptors, and never guesses them.
	Candidate *usbccid.Candidate

	// TransferTimeout bounds each bulk transfer, and is cut to what is left of the budget of the exchange. Zero means
	// DefaultTransferTimeout. It is at least 1 ms and at most MaxBudget (else Open returns ErrBadBudget).
	TransferTimeout time.Duration
}

// Info describes the reader's interface, for a report. It is a copy.
type Info struct {
	// Interface is the number of the CCID interface the reader claimed.
	Interface int
	// BulkOut and BulkIn are the addresses of its bulk endpoints.
	BulkOut, BulkIn uint8
	// MaxPacketOut and MaxPacketIn are the packet sizes of those endpoints.
	MaxPacketOut, MaxPacketIn int
	// ExtendedAPDU is true for a reader at extended APDU level, false for one at short APDU level.
	ExtendedAPDU bool
	// MaxMessageLength is the size of the largest CCID message the reader sends or reads: dwMaxCCIDMessageLength, held to
	// MaxMessageLength.
	MaxMessageLength int
	// MaxCommandAPDU is the largest command APDU this reader takes: MaxShortCommandAPDU or MaxCommandAPDU.
	MaxCommandAPDU int
}

// SlotState is the state of the slot as GetSlotStatus reports it (bmICCStatus).
type SlotState uint8

const (
	// SlotActive says that a card is present and powered.
	SlotActive SlotState = 0
	// SlotInactive says that a card is present and not powered.
	SlotInactive SlotState = 1
	// SlotEmpty says that no card is present.
	SlotEmpty SlotState = 2
)

func (s SlotState) String() string {
	switch s {
	case SlotActive:
		return "card present and active"
	case SlotInactive:
		return "card present and inactive"
	case SlotEmpty:
		return "no card"
	}
	return fmt.Sprintf("slot state(%d)", uint8(s))
}

// Reader is a CCID reader over a claimed interface of a Port, in the simple case this layer accepts (see the package comment). It
// moves bytes of one APDU at a time. Its methods are safe to call from several goroutines: they take turns, and a call waits for the
// one in progress, which ends within its budget.
type Reader struct {
	mu      sync.Mutex
	port    usbccid.Port
	lay     layout
	xfer    time.Duration
	maxMsg  int
	limit   int // largest command APDU
	wbuf    []byte
	rbuf    []byte
	lost    error
	closed  bool
	powered bool
	// drained is true once the reader has emptied the IN pipe of what an earlier session left (see drain). It happens at the first
	// exchange, before the first message of the session is sent.
	drained bool
	// emptyOK is true when the last message read filled a whole number of packets: the reader may have sent the empty packet that
	// ends it, which the next read then returns as zero bytes. That one empty read is consumed, and is no answer.
	emptyOK bool
	// touched is true once the current call has made a call on the Port. A call that fails on its budget before that has sent and
	// read nothing, and leaves the session as it was. begin clears it.
	touched bool
}

// Open takes ownership of the Port p, reads its descriptors, checks that the device is of the simple kind this layer accepts and
// claims its CCID interface. It sends nothing to the reader and reads nothing from it: the caller asks for the slot status and
// powers the card on, and the first exchange of the session drains the IN pipe before it sends (see the package comment). A nil Port
// is ErrNotOpen. On every error it closes the Port. A claim refused because another holder has the interface returns an error that wraps usbccid.ErrBusy;
// the reader does not retry.
func Open(p usbccid.Port, o Options) (*Reader, error) {
	if d, typed := p.(*usbccid.Device); p == nil || typed && d == nil {
		return nil, fmt.Errorf("%w: the Port is nil", ErrNotOpen)
	}
	r, err := open(p, o)
	if err != nil {
		_ = p.Close()
		return nil, err
	}
	return r, nil
}

func open(p usbccid.Port, o Options) (*Reader, error) {
	xfer := o.TransferTimeout
	switch {
	case xfer == 0:
		xfer = DefaultTransferTimeout
	case xfer < time.Millisecond || xfer > MaxBudget:
		return nil, fmt.Errorf("%w: TransferTimeout is %v", ErrBadBudget, xfer)
	}
	raw, err := p.Descriptors()
	if err != nil {
		return nil, fmt.Errorf("ccid: read the descriptors: %w", err)
	}
	l, err := parseDescriptors(raw, o.Candidate)
	if err != nil {
		return nil, err
	}
	// The length is declared by the device: take the minimum in uint32, and only then convert (a 32-bit int would turn 0x80000000 negative).
	maxMsg := int(min(l.class.maxMessageLength, uint32(MaxMessageLength)))
	r := &Reader{port: p, lay: *l, xfer: xfer, maxMsg: maxMsg, limit: MaxShortCommandAPDU}
	if l.class.extended() {
		r.limit = MaxCommandAPDU
	}
	// The read buffer holds the largest message and one packet more, so that the first read of a message, which asks for one packet,
	// always fits. A read never asks for more than the largest message.
	r.wbuf = make([]byte, maxMsg)
	r.rbuf = make([]byte, maxMsg+l.maxPacketIn)
	if err := p.Claim(l.iface); err != nil {
		return nil, fmt.Errorf("ccid: claim interface %d: %w", l.iface, err)
	}
	return r, nil
}

// Info returns what the descriptors say about the interface the reader uses.
func (r *Reader) Info() Info {
	if r == nil {
		return Info{}
	}
	return Info{
		Interface:        r.lay.iface,
		BulkOut:          r.lay.bulkOut,
		BulkIn:           r.lay.bulkIn,
		MaxPacketOut:     r.lay.maxPacketOut,
		MaxPacketIn:      r.lay.maxPacketIn,
		ExtendedAPDU:     r.lay.class.extended(),
		MaxMessageLength: r.maxMsg,
		MaxCommandAPDU:   r.limit,
	}
}

// Close releases the interface and closes the Port. It sends nothing (no power-off, no abort), is safe to call twice and on a nil
// Reader, and works on a lost session. After it every other call returns usbccid.ErrClosed. It waits for a call in progress.
func (r *Reader) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	r.wipe()
	if r.port == nil { // the zero Reader holds nothing
		return nil
	}
	e1 := r.port.Release(r.lay.iface)
	e2 := r.port.Close()
	return errors.Join(e1, e2)
}

// SlotStatus asks the reader for the state of the slot (GetSlotStatus). It does not touch the card.
func (r *Reader) SlotStatus(budget time.Duration) (SlotState, error) {
	if r == nil {
		return 0, ErrNotOpen
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	defer r.wipe()
	dl, err := r.begin(budget)
	if err != nil {
		return 0, err
	}
	st, err := r.slotStatus(dl)
	if err != nil {
		return 0, err
	}
	return st, nil
}

// PowerOn powers the card on, with the reader choosing the voltage and the parameters, and returns the answer to reset (a copy, 2
// to MaxATR bytes). It first asks for the slot status and sends nothing more when the slot is empty (ErrNoCard). A card that is
// already powered is reset.
//
// Whether a power cycle ends a verified PIN state on a given card is not known to this layer. It is a property of the card and its
// application, and it must be measured on a real card; nothing here claims it. A caller that needs the PIN state gone calls PowerOff
// and PowerOn and then checks the card's own answer, or closes the Reader and opens it again, and measures which of them works.
func (r *Reader) PowerOn(budget time.Duration) ([]byte, error) {
	if r == nil {
		return nil, ErrNotOpen
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	defer r.wipe()
	dl, err := r.begin(budget)
	if err != nil {
		return nil, err
	}
	was := r.powered
	r.powered = false
	st, err := r.slotStatus(dl)
	if err != nil {
		if !r.touched { // refused on the budget before any Port call: nothing changed
			r.powered = was
		}
		return nil, err
	}
	if st == SlotEmpty {
		return nil, fmt.Errorf("ccid: IccPowerOn: %w", ErrNoCard)
	}
	a, ce, err := r.exchange("IccPowerOn", msgIccPowerOn, 0, nil, rspDataBlock, dl)
	if err != nil {
		return nil, err
	}
	if ce != nil {
		// The failure answers the only message sent, so the stream is in order.
		return nil, ce
	}
	if a.chain != chainWhole {
		// The answer to reset is one whole block at every level.
		return nil, r.lose(fmt.Errorf("%w: IccPowerOn: bChainParameter 0x%02X in the answer to reset, which is never a chain", ErrProtocol, a.chain))
	}
	if a.state != 0 {
		// A success that says the card is not active is no power-on: the answer to reset is not returned.
		return nil, fmt.Errorf("ccid: IccPowerOn: the answer says %s: %w", SlotState(a.state), ErrNotPowered)
	}
	if n := len(a.data); n < 2 || n > MaxATR {
		return nil, fmt.Errorf("%w: %d bytes", ErrBadATR, n)
	}
	r.powered = true
	return append([]byte(nil), a.data...), nil
}

// PowerOff powers the card off (IccPowerOff). The reader answers with the slot status; a slot with no card answers that no card is
// present, which is ErrNoCard (the card is off, whatever the reason). A caller can send it to a card that is not powered.
func (r *Reader) PowerOff(budget time.Duration) error {
	if r == nil {
		return ErrNotOpen
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	defer r.wipe()
	dl, err := r.begin(budget)
	if err != nil {
		return err
	}
	was := r.powered
	r.powered = false
	a, ce, err := r.exchange("IccPowerOff", msgIccPowerOff, 0, nil, rspSlotStatus, dl)
	if err != nil {
		if !r.touched { // refused on the budget before any Port call: nothing changed
			r.powered = was
		}
		return err
	}
	if ce != nil {
		return ce
	}
	if a.state == uint8(SlotActive) {
		return fmt.Errorf("ccid: IccPowerOff: %w", ErrStillActive)
	}
	return nil
}

// Transmit sends one command APDU to the powered card and returns its response APDU (the data and the two status bytes). It moves the
// bytes of one APDU and nothing else: it builds no command, reads no status word and does not follow a continuation (61xx or any
// other); that belongs to the layer above. A command that does not fit one CCID message is sent in blocks (extended APDU level), and
// a response that comes in blocks is collected.
//
// budget is the total time of the exchange, including every time-extension message the card sends while it waits for a person; it is
// at most MaxBudget. When it runs out the session is lost (ErrSessionLost, ErrBudgetExhausted), unless that was before the call's
// first Port call: then the error wraps ErrBudgetExhausted alone and the session goes on. A transfer cannot be cancelled once
// started, so a call returns at most about one scheduling delay after the budget, and a context is not taken.
//
// A card-level failure that the reader reports (a *CommandError: a mute card, a parity error, no card, and the rest) answers the
// only message of a command and leaves the session in order; the same failure inside a chain ends the session. The session is also
// left as it was by an ErrNotPowered from an answer that ends the command, by an argument or state error (ErrBadBudget,
// ErrAPDUTooShort, ErrAPDUTooLong, ErrNotPowered, ErrNotOpen, usbccid.ErrClosed) and by a budget refusal before the first Port call;
// every other error ends it. The text of an error never holds a byte of a command or of a response.
//
// After ANY error, not only a lost session (also a *CommandError such as a mute card or an aborted command), the card may have
// carried out the command: a PIN verification or a signature is never to be assumed not to have happened. Success means only that a
// well-formed answer with the right sequence number arrived, of two bytes or more; the reader knows nothing of status words. A
// successful answer whose card status says that the card is not active is not returned: the error is ErrNotPowered. A device that
// sends time extensions without end costs a busy loop up to the budget, which is the only bound on them.
//
// The returned response is the caller's: this layer keeps no copy of it. The command is copied into a buffer of the reader, which is
// cleared before the call returns; the bytes of the caller's slice, of the Port's and of the kernel's copies are not this layer's to
// clear.
func (r *Reader) Transmit(apdu []byte, budget time.Duration) ([]byte, error) {
	if r == nil {
		return nil, ErrNotOpen
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	defer r.wipe()
	dl, err := r.begin(budget)
	if err != nil {
		return nil, err
	}
	switch {
	case len(apdu) < 4:
		return nil, fmt.Errorf("%w: %d bytes", ErrAPDUTooShort, len(apdu))
	case len(apdu) > r.limit:
		return nil, fmt.Errorf("%w: %d bytes, and this reader takes %d", ErrAPDUTooLong, len(apdu), r.limit)
	case !r.powered:
		return nil, ErrNotPowered
	}
	out, err := r.xfr(apdu, dl)
	if err != nil {
		wipeBytes(out)
		return nil, err
	}
	return out, nil
}

// begin checks the state and the budget of a call, starts it (no Port call made yet) and returns its deadline. The caller holds the
// lock.
func (r *Reader) begin(budget time.Duration) (time.Time, error) {
	if r.closed {
		return time.Time{}, usbccid.ErrClosed
	}
	if r.port == nil { // the zero Reader
		return time.Time{}, ErrNotOpen
	}
	if r.lost != nil {
		return time.Time{}, r.lost
	}
	if budget <= 0 || budget > MaxBudget {
		return time.Time{}, fmt.Errorf("%w: %v", ErrBadBudget, budget)
	}
	r.touched = false
	return time.Now().Add(budget), nil
}

// wipe clears the buffers that held a command or a response. The caller holds the lock.
func (r *Reader) wipe() {
	clear(r.wbuf)
	clear(r.rbuf)
}

func wipeBytes(b []byte) { clear(b) }

// lose ends the session with a first cause, and returns the error every call from now on returns.
func (r *Reader) lose(cause error) error {
	if r.lost == nil {
		r.lost = &lostError{cause: cause}
	}
	return r.lost
}

// slotStatus runs GetSlotStatus and keeps the power state in step with what the reader says: a card that is not active is not
// powered, whether the answer is a success or a failure. A failure of the reader is a *CommandError that ends nothing (it answers the
// only message).
func (r *Reader) slotStatus(dl time.Time) (SlotState, error) {
	a, ce, err := r.exchange("GetSlotStatus", msgGetSlotState, 0, nil, rspSlotStatus, dl)
	if err != nil {
		return 0, err
	}
	if ce != nil {
		if ce.ICCStatus != 0 {
			r.powered = false
		}
		return 0, ce
	}
	st := SlotState(a.state)
	r.powered = r.powered && st == SlotActive
	return st, nil
}

// xfr sends the command in one or more XfrBlock messages and collects the response. A failure answered in the middle of a chain, or
// to anything but the only message of a command, ends the session.
func (r *Reader) xfr(apdu []byte, dl time.Time) ([]byte, error) {
	room := r.maxMsg - headerLen
	if len(apdu) > room && !r.lay.class.extended() {
		return nil, fmt.Errorf("%w: %d bytes do not fit one message of %d, and chaining needs extended APDU level", ErrAPDUTooLong, len(apdu), r.maxMsg)
	}
	nchunks := (len(apdu) + room - 1) / room
	var (
		a   answer
		ce  *CommandError
		err error
	)
	for i, rest := 0, apdu; i < nchunks; i++ {
		chunk := rest[:min(len(rest), room)]
		rest = rest[len(chunk):]
		lvl := uint16(lvlWhole)
		switch {
		case nchunks == 1:
		case i == 0:
			lvl = lvlBegin
		case i == nchunks-1:
			lvl = lvlEnd
		default:
			lvl = lvlMiddle
		}
		a, ce, err = r.exchange("XfrBlock", msgXfrBlock, lvl, chunk, rspDataBlock, dl)
		if err != nil {
			return nil, err
		}
		if ce != nil {
			return nil, r.failXfr(ce, nchunks == 1)
		}
		if i < nchunks-1 {
			if a.chain != chainEmptyAck || len(a.data) != 0 {
				return nil, r.lose(fmt.Errorf("%w: the reader did not ask for the next command block (bChainParameter 0x%02X)", ErrProtocol, a.chain))
			}
			if a.state != 0 {
				return nil, r.cardOff(a, false)
			}
		}
	}
	// The first block of a response is the whole response or the beginning of a chain; a middle block, an end block or an
	// acknowledgement of a command block cannot be the first.
	if a.chain != chainWhole && a.chain != chainBegin {
		return nil, r.lose(fmt.Errorf("%w: bChainParameter 0x%02X in the first response block", ErrProtocol, a.chain))
	}
	if a.state != 0 {
		return nil, r.cardOff(a, nchunks == 1 && a.chain == chainWhole)
	}
	out, err := r.collect(nil, a.data)
	if err != nil {
		return out, err
	}
	for chain := a.chain; chain == chainBegin || chain == chainMiddle; chain = a.chain {
		a, ce, err = r.exchange("XfrBlock", msgXfrBlock, lvlContinue, nil, rspDataBlock, dl)
		if err != nil {
			return out, err
		}
		if ce != nil {
			return out, r.failXfr(ce, false)
		}
		// After a begin block each block is a middle one (more follows) or the end; anything else is out of order.
		if a.chain != chainMiddle && a.chain != chainEnd {
			return out, r.lose(fmt.Errorf("%w: bChainParameter 0x%02X in a response block after a begin block", ErrProtocol, a.chain))
		}
		if a.state != 0 {
			return out, r.cardOff(a, false)
		}
		if out, err = r.collect(out, a.data); err != nil {
			return out, err
		}
	}
	if len(out) < 2 {
		return out, r.lose(fmt.Errorf("%w: a response of %d bytes: a response APDU ends in two status bytes", ErrProtocol, len(out)))
	}
	return out, nil
}

// cardOff handles a successful answer whose card status says that the card is not active: the card is not powered any more, and the
// answer is not returned. The session stays in order only when the answer ended the command.
func (r *Reader) cardOff(a answer, inOrder bool) error {
	r.powered = false
	err := fmt.Errorf("ccid: XfrBlock: the answer says %s: %w", SlotState(a.state), ErrNotPowered)
	if inOrder {
		return err
	}
	return r.lose(err)
}

// failXfr returns the failure of a command. It leaves the session in order only when the reader answered the only message of the
// command and no chain was begun.
func (r *Reader) failXfr(ce *CommandError, onlyMessage bool) error {
	if ce.ICCStatus != 0 {
		r.powered = false
	}
	if onlyMessage {
		return ce
	}
	return r.lose(ce)
}

// collect appends the data of a block to the response, within MaxResponseAPDU. When the response grows it moves to a new array and
// clears the old one, so that no copy of a response is left behind.
func (r *Reader) collect(out, data []byte) ([]byte, error) {
	if len(out)+len(data) > MaxResponseAPDU {
		return out, r.lose(fmt.Errorf("%w: %d bytes after this block", ErrResponseTooLong, len(out)+len(data)))
	}
	if len(out)+len(data) > cap(out) {
		grown := make([]byte, len(out), max(2*cap(out), len(out)+len(data)))
		copy(grown, out)
		clear(out[:cap(out)])
		out = grown
	}
	return append(out, data...), nil
}

// answer is a complete message from the reader whose sequence number is the one asked for: its status byte and, for a DataBlock, its
// chain parameter. data is a slice of the reader's read buffer, valid until the next read.
type answer struct {
	state uint8 // bmICCStatus
	chain uint8
	data  []byte
}

// exchange sends one CCID message and reads its answer. It skips stale answers (at most MaxStaleAnswers) and time-extension
// messages. A failed command status comes back as a *CommandError, with a nil error, and leaves the decision to the caller. Every
// other failure has ended the session already.
func (r *Reader) exchange(op string, typ byte, level uint16, payload []byte, want byte, dl time.Time) (answer, *CommandError, error) {
	// A panic inside the Port leaves the stream in an unknown state: mark the session lost, and let the panic go on with its value.
	defer func() {
		if p := recover(); p != nil {
			_ = r.lose(fmt.Errorf("ccid: %s: the port panicked", op))
			panic(p)
		}
	}()
	if !r.drained {
		if err := r.drain(op, dl); err != nil {
			return answer{}, nil, err
		}
	}
	// A budget that is spent before the first Port call of the call has sent and read nothing: it refuses the call and keeps the
	// session. It is checked before the sequence number is taken, so that a refusal spends none.
	to, clamped, err := r.timeout(dl)
	if err != nil {
		err = fmt.Errorf("ccid: %s: %w", op, err)
		if r.touched {
			err = r.lose(err)
		}
		return answer{}, nil, err
	}
	r.touched = true
	seq := nextSeq()
	n := headerLen + len(payload)
	msg := r.wbuf[:n]
	msg[0] = typ
	binary.LittleEndian.PutUint32(msg[1:5], uint32(len(payload))) //nolint:gosec // at most MaxMessageLength
	msg[5] = 0                                                    // slot 0: this layer serves one slot
	msg[6] = seq
	msg[7] = 0 // bBWI, or bPowerSelect: 0, the reader chooses
	binary.LittleEndian.PutUint16(msg[8:10], level)
	copy(msg[headerLen:], payload)
	sent, err := r.port.BulkOut(r.lay.bulkOut, msg, to)
	clear(msg)
	if err != nil {
		return answer{}, nil, r.lose(portError(op, "write", err, clamped))
	}
	if sent != n {
		return answer{}, nil, r.lose(fmt.Errorf("%w: %s: %d of %d bytes sent", ErrShortWrite, op, sent, n))
	}
	stale := 0
	for {
		total, err := r.readMessage(op, dl)
		if err != nil {
			return answer{}, nil, err
		}
		h := r.rbuf[:headerLen]
		if h[6] != seq {
			clear(r.rbuf[:total])
			if stale++; stale > MaxStaleAnswers {
				return answer{}, nil, r.lose(fmt.Errorf("%w: %s: more than %d answers with another sequence number (MaxStaleAnswers), the last for %d and the call is %d", ErrProtocol, op, MaxStaleAnswers, h[6], seq))
			}
			continue
		}
		if h[0] != want {
			return answer{}, nil, r.lose(fmt.Errorf("%w: %s: message type 0x%02X, expected 0x%02X", ErrProtocol, op, h[0], want))
		}
		if h[5] != 0 {
			return answer{}, nil, r.lose(fmt.Errorf("%w: %s: the answer is for slot %d, and this layer serves slot 0", ErrProtocol, op, h[5]))
		}
		if h[7]&3 == 3 && h[7]>>6 < 2 {
			return answer{}, nil, r.lose(fmt.Errorf("%w: %s: bmICCStatus 3 is reserved", ErrProtocol, op))
		}
		if want == rspDataBlock && h[9] != chainWhole && !r.lay.class.extended() {
			return answer{}, nil, r.lose(fmt.Errorf("%w: %s: bChainParameter 0x%02X at short APDU level, where an answer has no chain", ErrProtocol, op, h[9]))
		}
		switch h[7] >> 6 { // bmCommandStatus
		case 0:
			return answer{state: h[7] & 3, chain: h[9], data: r.rbuf[headerLen:total]}, nil, nil
		case 1:
			return answer{}, &CommandError{Op: op, Code: h[8], ICCStatus: h[7] & 3}, nil
		case 2:
			clear(r.rbuf[:total]) // time extension: the card is busy (a person's touch); read again
		default:
			return answer{}, nil, r.lose(fmt.Errorf("%w: %s: bmCommandStatus 3 is reserved", ErrProtocol, op))
		}
	}
}

// drain empties the IN pipe before the first message of the session is sent. An answer that an earlier session left there (it was
// lost while the card waited for a person, and the person acted later) must not be taken for an answer of this one. It only reads,
// so Open still sends nothing; it runs at the first exchange of the session, whichever call that is, and its time counts against
// that call's budget. It reads whole messages into the read buffer, each read with a timeout of DrainReadTimeout (and no more than
// the transfer timeout or what is left of the budget), discards and clears what arrives, and stops at the first read that times
// out: the pipe is quiet. An empty packet that ended an earlier message is read and dropped here; one that arrives just after the
// drain is consumed by the first read of the session (as after a whole-packet message). A read cut short by the budget is no proof
// of quiet: the budget error ends the session, except before the first read, where it only refuses the call. At most MaxDrainReads
// reads and MaxDrainTime in all: a device that is not quiet by then keeps sending without being asked, and the session is lost with
// ErrProtocol. On a quiet device the cost is one timed-out read, DrainReadTimeout.
func (r *Reader) drain(op string, dl time.Time) error {
	buf := r.rbuf[:min(len(r.rbuf), MaxMessageLength)]
	start := time.Now()
	for reads := 0; ; reads++ {
		if reads >= MaxDrainReads || time.Since(start) > MaxDrainTime {
			return r.lose(fmt.Errorf("%w: %s: the IN pipe is not quiet after %d reads (MaxDrainReads, MaxDrainTime): the device sends without being asked", ErrProtocol, op, reads))
		}
		// A read that the budget cut short proves nothing about the pipe: unless the budget leaves room for one whole drain read
		// (DrainReadTimeout, or the transfer timeout when that is shorter), the drain is not marked done. Before its first read
		// that only refuses the call (nothing was read, the session is kept); later it ends the session.
		to := min(DrainReadTimeout, r.xfer)
		if time.Until(dl) < to {
			err := fmt.Errorf("ccid: %s: drain: %w: not enough left for one whole read of %v", op, ErrBudgetExhausted, to)
			if r.touched {
				err = r.lose(err)
			}
			return err
		}
		r.touched = true
		n, err := r.port.BulkIn(r.lay.bulkIn, buf, to)
		clear(buf)
		switch {
		case err == nil:
			if n < 0 || n > len(buf) {
				return r.lose(fmt.Errorf("%w: %s: the port returned %d bytes for a buffer of %d", ErrProtocol, op, n, len(buf)))
			}
		case errors.Is(err, usbccid.ErrTimeout):
			// One empty packet may still arrive, the end of a whole-packet message of an earlier session: the first read consumes it.
			r.drained, r.emptyOK = true, true
			return nil
		default:
			return r.lose(portError(op, "drain", err, false))
		}
	}
}

// seqs is the one counter of sequence numbers of the process, shared by every Reader and seeded at random at first use. A Reader
// that opens after a lost session therefore never starts where the old one was, and never at a place that can be guessed: a late
// answer of the old session carries a number that the new one will not use for another 255 messages of this process.
var seqs struct {
	once sync.Once
	n    atomic.Uint32
}

// nextSeq returns the next sequence number of the process. Two Readers alive at once take turns from the same counter, so each sees
// its own numbers with gaps (the numbers are unique in any window of 256 messages of the process, and every answer is matched against
// the number of the message it answers).
func nextSeq() uint8 {
	seqs.once.Do(func() {
		var b [1]byte
		if _, err := rand.Read(b[:]); err != nil { // never fails since Go 1.24; the fallback is the clock
			b[0] = byte(time.Now().UnixNano()) //nolint:gosec // only a seed
		}
		seqs.n.Store(uint32(b[0]))
	})
	return uint8(seqs.n.Add(1)) //nolint:gosec // the counter wraps at 256 on purpose
}

// timeout returns the timeout of the next transfer: the transfer timeout, cut to what is left of the budget. clamped says that the
// budget cut it.
func (r *Reader) timeout(dl time.Time) (to time.Duration, clamped bool, err error) {
	left := time.Until(dl)
	if left <= 0 {
		return 0, false, ErrBudgetExhausted
	}
	if left < r.xfer {
		return left, true, nil
	}
	return r.xfer, false, nil
}

// portError wraps an error of the Port. A timeout that the budget cut is also ErrBudgetExhausted.
func portError(op, dir string, err error, clamped bool) error {
	if clamped && errors.Is(err, usbccid.ErrTimeout) {
		return fmt.Errorf("ccid: %s: %s: %w: %w", op, dir, ErrBudgetExhausted, err)
	}
	return fmt.Errorf("ccid: %s: %s: %w", op, dir, err)
}

// readMessage reads one complete CCID message into the read buffer and returns its length. It reads the first packet, takes
// dwLength from the header, and reads the rest. A read that ends before the bytes asked for ended on a short packet, which ends the
// message: a message cut short is a protocol error. After a message that filled whole packets the reader may have sent the empty
// packet that ends it: one empty read at the start of the next message is consumed and is no answer; an empty read anywhere else
// is a protocol error.
func (r *Reader) readMessage(op string, dl time.Time) (int, error) {
	buf := r.rbuf
	mps := r.lay.maxPacketIn
	allowEmpty := r.emptyOK
	r.emptyOK = false
	got := 0
	short := false // the last read ended on a short packet
	for got < headerLen {
		to, clamped, err := r.timeout(dl)
		if err != nil {
			return 0, r.lose(fmt.Errorf("ccid: %s: read: %w", op, err))
		}
		want := mps
		n, err := r.port.BulkIn(r.lay.bulkIn, buf[got:got+want], to)
		if err != nil {
			return 0, r.lose(portError(op, "read", err, clamped))
		}
		if n < 0 || n > want {
			return 0, r.lose(fmt.Errorf("%w: %s: the port returned %d bytes for a buffer of %d", ErrProtocol, op, n, want))
		}
		if n == 0 {
			if got == 0 && allowEmpty {
				allowEmpty = false
				continue
			}
			return 0, r.lose(fmt.Errorf("%w: %s: an empty read where no message ends", ErrProtocol, op))
		}
		got += n
		short = n < want
		if short && got < headerLen {
			return 0, r.lose(fmt.Errorf("%w: %s: the message ended after %d bytes, inside its header", ErrProtocol, op, got))
		}
	}
	dlen := le32(buf[1:5])
	if int64(dlen) > int64(r.maxMsg-headerLen) {
		return 0, r.lose(fmt.Errorf("%w: %s: a message of %d data bytes exceeds the %d of dwMaxCCIDMessageLength", ErrProtocol, op, dlen, r.maxMsg-headerLen))
	}
	total := headerLen + int(dlen)
	if got > total {
		return 0, r.lose(fmt.Errorf("%w: %s: %d bytes arrived for a message of %d", ErrProtocol, op, got, total))
	}
	if short && got < total {
		return 0, r.lose(fmt.Errorf("%w: %s: the message ended after %d of %d bytes", ErrProtocol, op, got, total))
	}
	for got < total {
		to, clamped, err := r.timeout(dl)
		if err != nil {
			return 0, r.lose(fmt.Errorf("ccid: %s: read: %w", op, err))
		}
		want := total - got
		n, err := r.port.BulkIn(r.lay.bulkIn, buf[got:total], to)
		if err != nil {
			return 0, r.lose(portError(op, "read", err, clamped))
		}
		if n < 0 || n > want {
			return 0, r.lose(fmt.Errorf("%w: %s: the port returned %d bytes for a buffer of %d", ErrProtocol, op, n, want))
		}
		if n == 0 || n < want {
			return 0, r.lose(fmt.Errorf("%w: %s: the message ended after %d of %d bytes", ErrProtocol, op, got+n, total))
		}
		got += n
	}
	r.emptyOK = total%mps == 0
	return total, nil
}
