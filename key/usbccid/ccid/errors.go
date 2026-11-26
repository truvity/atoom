package ccid

import (
	"errors"
	"fmt"
)

// The refusals of Open: the device is a CCID device, but not the simple kind that this layer accepts. Each wraps the sentinel and
// names the rule in its text, with the facts of the descriptors (numbers) and no more. Open closes the Port after each of them.

// ErrNoInterface means that the descriptors hold no interface of class 0x0B at alternate setting 0 with a bulk OUT and a bulk IN
// endpoint.
var ErrNoInterface = errors.New("ccid: no CCID interface with bulk endpoints")

// ErrSeveralInterfaces means that the descriptors hold more than one such interface and Options.Candidate does not say which one. The
// reader never chooses between them.
var ErrSeveralInterfaces = errors.New("ccid: several CCID interfaces, and none was named")

// ErrBadClass means that the class descriptor of the CCID interface is missing, is not the 54-byte form, or contradicts the interface
// or itself: two class descriptors, an interface subclass or protocol other than 0 (a reader of another transport), a version other
// than 1, two exchange levels, no protocol (neither T=0 nor T=1), or a message length below MinMessageLength. The text names which.
var ErrBadClass = errors.New("ccid: the class descriptor is missing, of another form, or contradicts the interface")

// ErrSeveralSlots means that the reader declares more than one slot (bMaxSlotIndex above 0). The reader serves exactly one.
var ErrSeveralSlots = errors.New("ccid: the reader has several slots, and this layer serves exactly one")

// ErrExchangeLevel means that the reader works at character or TPDU level, where the host builds the card's protocol blocks. This
// layer needs short APDU or extended APDU level, where the reader does that. The text says which level the reader declares and the
// values of dwFeatures and dwMaxCCIDMessageLength.
var ErrExchangeLevel = errors.New("ccid: the reader's exchange level is not short APDU or extended APDU")

// ErrParameters means that the reader does not declare automatic parameter configuration from the ATR (dwFeatures bit 0x00000002).
// This layer sends no SetParameters and no PPS, so a reader that needs them from the host is refused.
var ErrParameters = errors.New("ccid: the reader does not declare automatic parameter configuration")

// ErrVoltage means that the reader declares no voltage (bVoltageSupport is 0). Power-on asks the reader to choose the voltage itself
// (bPowerSelect 0), and the layer never sets one.
var ErrVoltage = errors.New("ccid: the reader declares no voltage")

// The errors of a call.

// ErrSessionLost is wrapped by every error that ends the session of a Reader, and is what every later call of that Reader returns
// (together with the first cause) without touching the Port. A session is lost when a transfer timed out or failed, when a message
// of the reader broke the protocol, when the budget of an exchange ran out after the call's first Port call, when a write was
// short, and when a card-level failure came in the middle of a chain. It cannot be repaired: this layer never resynchronises and
// never sends an abort. The caller closes the Reader and opens again; a card whose PIN the caller verified may or may not still
// hold it after that, and this layer does not know.
var ErrSessionLost = errors.New("ccid: the session is lost: close the reader and open again")

// ErrBudgetExhausted is wrapped, together with ErrSessionLost, when the budget of an exchange ran out after the call's first Port
// call: before a transfer, or as a transfer's timeout that was cut to what was left of the budget. A call whose budget is spent
// before its first Port call wraps it alone: nothing was sent or read and the session goes on. The text names the call.
var ErrBudgetExhausted = errors.New("ccid: the budget of the exchange ran out")

// ErrProtocol is wrapped, together with ErrSessionLost, when the reader sent something this layer cannot follow: a message of
// another type, of a wrong length, cut short, for another slot, a chain parameter out of order (or any but 0 in a DataBlock answer
// at short APDU level, or in the answer to reset), too many stale answers (more than MaxStaleAnswers), an empty packet where none
// belongs, or a status that RDR_to_PC messages do not have. The text names which and holds the numbers of the header, never the
// data of a message.
var ErrProtocol = errors.New("ccid: protocol error")

// ErrShortWrite is wrapped, together with ErrSessionLost, when BulkOut reported fewer bytes than it was given: the reader holds part
// of a message.
var ErrShortWrite = errors.New("ccid: short write")

// ErrResponseTooLong is wrapped, together with ErrSessionLost, when the response of the card, with its chained blocks, passes
// MaxResponseAPDU bytes. The rest of the chain is still on the way, and so the session is lost.
var ErrResponseTooLong = errors.New("ccid: the response is longer than MaxResponseAPDU")

// The refusals below are errors of the arguments or of the state: nothing was sent, and the session is not lost.

// ErrBadBudget is returned for a budget that is zero or negative, or above MaxBudget, and by Open for a TransferTimeout below 1 ms or
// above MaxBudget.
var ErrBadBudget = errors.New("ccid: the budget of the exchange must be above zero and at most MaxBudget")

// ErrAPDUTooShort is returned for a command of fewer than 4 bytes (the header of an APDU).
var ErrAPDUTooShort = errors.New("ccid: the command APDU is shorter than its 4-byte header")

// ErrAPDUTooLong is returned for a command above the largest this reader takes: MaxShortCommandAPDU bytes at short APDU level and
// MaxCommandAPDU at extended APDU level.
var ErrAPDUTooLong = errors.New("ccid: the command APDU is longer than this reader takes (MaxShortCommandAPDU, MaxCommandAPDU)")

// ErrNotPowered is returned by Transmit when the card has not been powered on by this Reader (or was powered off, or the reader
// reported it removed or inactive), and by Transmit and PowerOn when an answer that succeeded says in its card status that the card is
// not active: the answer (the response, the answer to reset) is then not returned.
var ErrNotPowered = errors.New("ccid: the card is not powered on")

// ErrNotOpen is returned by every method of the zero Reader and of a nil *Reader (Close and Info excepted: Close returns nil, Info the
// zero Info), and by Open for a nil Port. Use Open.
var ErrNotOpen = errors.New("ccid: the reader is not open: use Open with a Port")

// ErrStillActive is returned by PowerOff when the reader's answer says that the card is still active. The Reader treats the card as
// not powered and sends it no command; the session is not lost.
var ErrStillActive = errors.New("ccid: the reader says the card is still active after power off")

// ErrBadATR is returned by PowerOn when the answer to reset is shorter than 2 bytes or longer than MaxATR. The reader has reset the
// card, but the Reader treats it as not powered (Transmit returns ErrNotPowered until a PowerOn succeeds), and the session is not
// lost; the caller decides whether to power the card off. The text holds the length and no byte.
var ErrBadATR = errors.New("ccid: the answer to reset has a length that is refused (2 to MaxATR bytes)")

// The card-level failures. A reader reports them in its answer, with the status bytes, and the session goes on unless the failure
// came in the middle of a chain. Each is a value to test with errors.Is on the error of the call; errors.As to *CommandError gives the
// status bytes.

// ErrCommandFailed is wrapped by every *CommandError.
var ErrCommandFailed = errors.New("ccid: the reader answered that the command failed")

// ErrNoCard is returned when the slot holds no card: by PowerOn after GetSlotStatus said so, and wrapped by a *CommandError whose
// bmICCStatus says no card.
var ErrNoCard = errors.New("ccid: no card in the slot")

// ErrCardMute is wrapped by a *CommandError with bError 0xFE (ICC_MUTE): the card did not answer.
var ErrCardMute = errors.New("ccid: the card is mute")

// ErrCardParity is wrapped by a *CommandError with bError 0xFD (XFR_PARITY_ERROR): a parity error in the exchange with the card.
var ErrCardParity = errors.New("ccid: parity error in the exchange with the card")

// ErrCardProtocol is wrapped by a *CommandError with bError 0xF3 to 0xF8: the bad answer to reset (TS or TCK), the protocol or class
// that the reader does not support, a procedure byte conflict, a deactivated protocol.
var ErrCardProtocol = errors.New("ccid: protocol error with the card")

// ErrSlotBusy is wrapped by a *CommandError with bError 0xE0 (SLOT_BUSY) or 0xF2 (BUSY_WITH_AUTO_SEQUENCE).
var ErrSlotBusy = errors.New("ccid: the slot is busy")

// ErrReaderFault is wrapped by a *CommandError with bError 0xFB (HW_ERROR) or 0xFC (XFR_OVERRUN).
var ErrReaderFault = errors.New("ccid: the reader reports a hardware fault or an overrun")

// ErrCommandNotSupported is wrapped by a *CommandError with bError 0x00: the reader does not support the command.
var ErrCommandNotSupported = errors.New("ccid: the reader does not support the command")

// ErrBadParameter is wrapped by a *CommandError with bError 0x01 to 0x7F: the reader refused the parameter at that offset of the
// message it was sent.
var ErrBadParameter = errors.New("ccid: the reader refused a parameter of the message")

// CommandError is a CCID message answered with the command status "failed". It holds the status bytes of the answer and never a
// byte of an APDU. errors.Is(err, ErrCommandFailed) holds for every one, and for the sentinel that the code maps to (the doc of each
// sentinel says which), and errors.Is(err, ErrNoCard) also when the ICC status says that no card is present.
type CommandError struct {
	// Op names the message: "GetSlotStatus", "IccPowerOn", "IccPowerOff" or "XfrBlock".
	Op string
	// Code is bError of the answer.
	Code uint8
	// ICCStatus is bmICCStatus of the answer: 0 a card present and active, 1 present and inactive, 2 no card.
	ICCStatus uint8
}

func (e *CommandError) Error() string {
	return fmt.Sprintf("ccid: %s failed: bError 0x%02X (%s), ICC status %d", e.Op, e.Code, errName(e.Code), e.ICCStatus)
}

// Unwrap returns ErrCommandFailed and the sentinel of the code, if there is one, and ErrNoCard when the ICC status says no card.
func (e *CommandError) Unwrap() []error {
	out := []error{ErrCommandFailed}
	if s := sentinel(e.Code); s != nil {
		out = append(out, s)
	}
	if e.ICCStatus == 2 {
		out = append(out, ErrNoCard)
	}
	return out
}

func sentinel(code uint8) error {
	switch {
	case code == 0x00:
		return ErrCommandNotSupported
	case code <= 0x7F:
		return ErrBadParameter
	}
	switch code {
	case 0xFE:
		return ErrCardMute
	case 0xFD:
		return ErrCardParity
	case 0xF8, 0xF7, 0xF6, 0xF5, 0xF4, 0xF3:
		return ErrCardProtocol
	case 0xE0, 0xF2:
		return ErrSlotBusy
	case 0xFB, 0xFC:
		return ErrReaderFault
	}
	return nil
}

func errName(c uint8) string {
	switch c {
	case 0xFF:
		return "CMD_ABORTED"
	case 0xFE:
		return "ICC_MUTE"
	case 0xFD:
		return "XFR_PARITY_ERROR"
	case 0xFC:
		return "XFR_OVERRUN"
	case 0xFB:
		return "HW_ERROR"
	case 0xF8:
		return "BAD_ATR_TS"
	case 0xF7:
		return "BAD_ATR_TCK"
	case 0xF6:
		return "ICC_PROTOCOL_NOT_SUPPORTED"
	case 0xF5:
		return "ICC_CLASS_NOT_SUPPORTED"
	case 0xF4:
		return "PROCEDURE_BYTE_CONFLICT"
	case 0xF3:
		return "DEACTIVATED_PROTOCOL"
	case 0xF2:
		return "BUSY_WITH_AUTO_SEQUENCE"
	case 0xE0:
		return "SLOT_BUSY"
	case 0x00:
		return "command not supported"
	}
	if c <= 0x7F {
		return "bad parameter at this offset"
	}
	return "unknown"
}

// lostError is the error of a session that was lost: the first cause, and ErrSessionLost. Every later call returns the same value.
type lostError struct{ cause error }

func (e *lostError) Error() string   { return "ccid: session lost: " + e.cause.Error() }
func (e *lostError) Unwrap() []error { return []error{ErrSessionLost, e.cause} }
