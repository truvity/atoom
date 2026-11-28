package pivtest

import (
	"encoding/binary"
	"fmt"
	"sync"
	"time"
)

// The instructions this package answers, and the three that it records as forbidden. They are this package's own.
const (
	insSelect      = 0xA4
	insVerify      = 0x20
	insChange      = 0x24
	insGetData     = 0xCB
	insGetResponse = 0xC0
	insGetVersion  = 0xFD
	insGetSerial   = 0xF8
	insGetMetadata = 0xF7

	insReset      = 0xFB // reset of the application
	insSetRetries = 0xFA // set the retry counters of the PIN and the PUK
	insImportKey  = 0xFE // import a key
)

// The status words.
const (
	swOK            = 0x9000
	swMore          = 0x6100 // 61xx: xx bytes more, to be read with GET RESPONSE
	swTriesLeft     = 0x63C0 // 63Cx: wrong value, x tries left
	swWrongLength   = 0x6700
	swBlocked       = 0x6983
	swNotAllowed    = 0x6985 // conditions of use not satisfied
	swWrongData     = 0x6A80
	swNotFound      = 0x6A82
	swWrongP1P2     = 0x6A86
	swRefNotFound   = 0x6A88
	swInsNotAllowed = 0x6D00
	swClaNotAllowed = 0x6E00
)

// The references of VERIFY, CHANGE REFERENCE DATA and GET METADATA.
const (
	refPIN = 0x80
	refPUK = 0x81
)

// The factory values of the PIN and the PUK.
const (
	DefaultPIN = "123456"
	DefaultPUK = "12345678"
)

// DefaultSerial is the serial number of a card made with no Options.Serial. It is made up.
const DefaultSerial = 7654321

// ResponseMode says how a response that is longer than the command allows reaches the reader.
type ResponseMode int

const (
	// Extended is the default: an answer to a command that came as an extended APDU is returned at once, as long as its Le allows (up to
	// 65536 bytes). An answer to a short command that does not fit is cut at its Le and followed by 61xx.
	Extended ResponseMode = iota
	// Short61xx cuts every answer at 256 bytes or at the Le of the command, whichever is less, and follows it with 61xx while there is more;
	// GET RESPONSE reads the rest, whatever form the command had.
	Short61xx
)

// Options shape a card. The zero value is a valid card: firmware 5.8.0, the serial DefaultSerial, the factory PIN and PUK with three tries
// each, extended APDUs accepted and the mode Extended.
//
// Options is a plain struct, the caller's own input, and it carries the PIN and the PUK in the clear: a test that prints its Options, or a
// struct that holds them, prints them, whatever verb it uses. Its Format method keeps them out of the verbs that reach it, and no method can
// do more. A Card never gives them out by any print, and New keeps no copy of the Options value.
type Options struct {
	Firmware [3]byte // major, minor, patch; zero: 5.8.0
	Serial   uint32  // zero: DefaultSerial

	// PIN and PUK are 6 to 8 bytes and must not end in the byte FF, which is the padding on the wire: a value that ends in it could never be
	// sent, and New panics. An FF inside a value is allowed. Empty: DefaultPIN and DefaultPUK, and the card says it is the default; a value
	// given is not the default.
	PIN string
	PUK string

	MaxPINTries byte // 1 to 15; zero: 3
	MaxPUKTries byte // 1 to 15; zero: 3

	ResponseMode   ResponseMode
	NoExtendedAPDU bool // extended APDUs are answered 6700; by default they are accepted
}

// Card is a simulated PIV application at the level of command and response APDUs. It is a state machine with no goroutine, timer, clock,
// I/O or randomness: the same commands in the same order always give the same answers. A command that makes a real card wait would return
// the wait as a duration; no command of this part does.
//
// The card state (firmware, serial, PIN, PUK, their counters and the data objects) survives PowerOff. The session state (the selected
// application, the verified PIN, the buffer of a chained command and the rest of a response) does not. Log and Forbidden are
// instrumentation and not card state: they record what was sent, and are never cleared.
//
// A Card is safe for use by several goroutines, one command at a time.
//
// A Card is a handle: it holds one unexported func value that returns the state, and everything (the mutex, the PIN, the PUK, the counters,
// the objects, the session and the log) lives behind it. A copy of a Card is a second handle to the SAME card, not a second card; make
// another card with New. A func is a kind that fmt never follows, so no print of a Card, by value or by pointer, in a field that fmt reads
// by reflection or not, with any verb and with the methods of fmt on or off, can reach more than an address; and Format prints a fixed text with the firmware, the serial number and the counters, and nothing of
// the PIN, the PUK or the objects. The zero Card is no card: its methods panic.
type Card struct {
	get func() *state
}

// st is the state behind a handle. The zero Card is no card, and panics here with a clear text.
func (card Card) st() *state {
	if card.get == nil {
		panic("pivtest: the zero Card is no card: make one with New")
	}
	return card.get()
}

// state is the card behind the handle.
type state struct {
	mu sync.Mutex

	// card state
	fw      [3]byte
	serial  uint32
	pin     reference
	puk     reference
	objects map[uint32][]byte

	// options
	mode       ResponseMode
	noExtended bool

	// session state
	on       bool
	selected bool
	verified bool
	chain    *chain
	pending  []byte

	// instrumentation
	log       []Entry
	forbidden []Entry
}

// New returns a card in the state Options describe, powered off. It panics on an option that is out of range: it is test support.
func New(o Options) *Card {
	c := &state{
		fw:         o.Firmware,
		serial:     o.Serial,
		objects:    map[uint32][]byte{},
		mode:       o.ResponseMode,
		noExtended: o.NoExtendedAPDU,
	}
	if c.fw == [3]byte{} {
		c.fw = [3]byte{5, 8, 0}
	}
	if c.serial == 0 {
		c.serial = DefaultSerial
	}
	c.pin = newReference("PIN", o.PIN, DefaultPIN, o.MaxPINTries)
	c.puk = newReference("PUK", o.PUK, DefaultPUK, o.MaxPUKTries)
	if o.ResponseMode != Extended && o.ResponseMode != Short61xx {
		panic(fmt.Sprintf("pivtest: unknown response mode %d", o.ResponseMode))
	}
	return &Card{get: func() *state { return c }}
}

// Format prints the options with the PIN and the PUK left out, for every verb: a test that prints its options leaks neither.
func (o Options) Format(f fmt.State, _ rune) {
	_, _ = f.Write(fmt.Appendf(nil, "pivtest.Options{firmware %d.%d.%d, serial %d, PIN %s, PUK %s, max tries %d and %d, mode %d, no extended APDU %t}",
		o.Firmware[0], o.Firmware[1], o.Firmware[2], o.Serial, given(o.PIN), given(o.PUK), o.MaxPINTries, o.MaxPUKTries, o.ResponseMode, o.NoExtendedAPDU))
}

func given(s string) string {
	if s == "" {
		return "default"
	}
	return "given, not shown"
}

// Format prints a fixed text for every verb (so also %v, %+v, %#v, %s, %d, %x and %q): the firmware, the serial number and the counters,
// and nothing of the PIN, the PUK or the objects.
func (card Card) Format(f fmt.State, _ rune) {
	if card.get == nil {
		_, _ = f.Write([]byte("pivtest.Card{not made by New}"))
		return
	}
	c := card.st()
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _ = f.Write(fmt.Appendf(nil, "pivtest.Card{firmware %d.%d.%d, serial %d, PIN tries %d of %d, PUK tries %d of %d, %d objects, PIN, PUK and objects not shown}",
		c.fw[0], c.fw[1], c.fw[2], c.serial, c.pin.left, c.pin.max, c.puk.left, c.puk.max, len(c.objects)))
}

// atr is the answer to reset: made up, and not the answer of any real card. TS 3B, T0 8B (an interface byte TD1 follows, 11 historical
// bytes), TD1 01 (protocol T=1), the historical bytes "pivtest-sim", and the check byte.
var atr = func() []byte {
	b := []byte{0x3B, 0x8B, 0x01}
	b = append(b, "pivtest-sim"...)
	var tck byte
	for _, x := range b[1:] {
		tck ^= x
	}
	return append(b, tck)
}()

// PowerOn starts a fresh session and returns the answer to reset. The session state is empty: nothing is selected and no PIN is verified.
// The card state is as it was.
func (card *Card) PowerOn() []byte {
	c := card.st()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.endSession()
	c.on = true
	return append([]byte(nil), atr...)
}

// PowerOff ends the session. The card state is kept.
func (card *Card) PowerOff() {
	c := card.st()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.endSession()
	c.on = false
}

func (c *state) endSession() {
	c.selected, c.verified, c.chain, c.pending = false, false, nil, nil
}

// Exchange takes one command APDU and returns one response APDU (the data, then SW1 SW2) and the time the card makes the reader wait
// before it answers, which is always zero in this part. A card that is not powered on answers nothing: the response is nil. A command
// that is shorter than four bytes or whose lengths do not match its size is answered 6700.
func (card *Card) Exchange(apdu []byte) (resp []byte, wait time.Duration) {
	c := card.st()
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.on {
		return nil, 0
	}
	cmd, ok := parseCommand(apdu)
	if len(apdu) >= 2 { // the INS is there
		cmd.why = trip(&cmd)
	}
	sent := len(cmd.data)
	var r reply
	if ok {
		r = c.run(&cmd)
	} else {
		c.pending, c.chain = nil, nil
		r = status(swWrongLength)
	}
	c.record(&cmd, sent, !ok, r.sw)
	return r.bytes(), 0
}

// run answers one well-formed command.
func (c *state) run(cmd *command) reply {
	if cmd.extended && c.noExtended {
		c.pending, c.chain = nil, nil
		return status(swWrongLength)
	}
	if cmd.cla != claPlain && cmd.cla != claChain {
		c.pending, c.chain = nil, nil
		return status(swClaNotAllowed)
	}
	if cmd.cla != claPlain || cmd.ins != insGetResponse {
		c.pending = nil // a new command drops the rest of a response (assumed: R10)
	}
	if !c.selected && cmd.ins != insSelect {
		c.chain = nil
		return status(swNotAllowed) // assumed: R7
	}
	if cmd.cla == claChain {
		return c.chained(cmd)
	}
	if !c.complete(cmd) {
		return status(swWrongLength) // assumed: R21
	}
	return c.shape(cmd, c.dispatch(cmd))
}

func (c *state) dispatch(cmd *command) reply {
	switch cmd.ins {
	case insSelect:
		return c.selectApplication(cmd)
	case insGetVersion:
		return c.getVersion(cmd)
	case insGetSerial:
		return c.getSerial(cmd)
	case insVerify:
		return c.verify(cmd)
	case insChange:
		return c.changeReference(cmd)
	case insGetMetadata:
		return c.getMetadata(cmd)
	case insGetData:
		return c.getData(cmd)
	case insGetResponse:
		return c.getResponse(cmd)
	}
	// Includes the three instructions of the tripwire: they are recorded in Forbidden and answered as an instruction the card does not know.
	return status(swInsNotAllowed)
}

// The AID of the PIV application, as the standard gives it (the first five bytes: the national prefix) and in full.
var (
	aidShort = []byte{0xA0, 0x00, 0x00, 0x03, 0x08}
	aidFull  = []byte{0xA0, 0x00, 0x00, 0x03, 0x08, 0x00, 0x00, 0x10, 0x00, 0x01, 0x00}
)

// selectResponse is the application property template: 61 { 4F 06 <the AID without its prefix>, 79 { 4F 05 <the prefix> } }.
var selectResponse = tlv(0x61,
	tlv(0x4F, aidFull[5:]),
	tlv(0x79, tlv(0x4F, aidShort)),
)

// selectApplication is SELECT by name. A SELECT that is answered 9000 clears the verified PIN (assumed: R2); one that is not changes
// nothing.
func (c *state) selectApplication(cmd *command) reply {
	if cmd.p1 != 0x04 || cmd.p2 != 0x00 {
		return status(swWrongP1P2) // assumed: R8
	}
	if string(cmd.data) != string(aidShort) && string(cmd.data) != string(aidFull) {
		return status(swNotFound)
	}
	c.selected, c.verified = true, false // assumed: R2
	return reply{data: selectResponse, sw: swOK}
}

func (c *state) getVersion(cmd *command) reply {
	if r, bad := c.plain(cmd); bad {
		return r
	}
	return reply{data: c.fw[:], sw: swOK}
}

func (c *state) getSerial(cmd *command) reply {
	if r, bad := c.plain(cmd); bad {
		return r
	}
	return reply{data: binary.BigEndian.AppendUint32(nil, c.serial), sw: swOK}
}

// plain checks a command that takes P1 = P2 = 00 and no data (assumed: R8).
func (c *state) plain(cmd *command) (reply, bool) {
	switch {
	case cmd.p1 != 0 || cmd.p2 != 0:
		return status(swWrongP1P2), true
	case len(cmd.data) != 0:
		return status(swWrongLength), true
	}
	return reply{}, false
}
