// Package ccid is the CCID reader of the card transport (record 0026): it sends one command APDU to the card in a reader and returns the
// response APDU, over the bulk endpoints of a CCID interface that a usbccid.Port has claimed. It is pure Go (cgo off, the standard
// library and the package usbccid only), has no system call of its own and no build constraint: every byte goes through a Port, so it
// builds on every system and a test runs it against a fake Port. It knows no card application; those are built on it.
//
//	port, err := candidate.Open()          // usbccid: one usbfs node
//	r, err := ccid.Open(port, ccid.Options{Candidate: &candidate})
//	atr, err := r.PowerOn(5 * time.Second)
//	resp, err := r.Transmit(command, 30*time.Second) // the response APDU, with its two status bytes
//	err = r.PowerOff(time.Second)
//	err = r.Close()
//
// # What it accepts
//
// Open accepts only the simple case, and refuses the rest by name (the sentinel errors Err...), as the USB layer does with descriptors.
// The descriptors the Port gives must hold:
//
//   - one CCID interface (class 0x0B, alternate setting 0, subclass 0, protocol 0) with exactly one bulk OUT and one bulk IN endpoint,
//     named by Options.Candidate when the device has several (ErrNoInterface, ErrSeveralInterfaces, usbccid.ErrWrongDevice). The
//     reader takes the interface and the endpoint addresses from the descriptors and never guesses them;
//   - one class descriptor of the 54-byte form, of version 1, that agrees with the interface (ErrBadClass);
//   - exactly one slot (bMaxSlotIndex 0; ErrSeveralSlots);
//   - the exchange level short APDU or extended APDU. A reader at character or TPDU level, where the host builds the blocks of the
//     card's protocol, is refused (ErrExchangeLevel; the text says which level the reader declares);
//   - automatic parameter configuration from the answer to reset (dwFeatures bit 0x00000002; ErrParameters): the reader never sends
//     SetParameters or a PPS;
//   - a declared voltage (bVoltageSupport not 0; ErrVoltage): power-on always asks the reader to choose the voltage and the clock
//     (bPowerSelect 0), and never sets one. The automatic-voltage bit of dwFeatures (0x00000008) is not required: the reader this layer
//     comes from does not declare it and was driven with the same power-on message;
//   - T=0 or T=1 among the protocols, and a dwMaxCCIDMessageLength of at least MinMessageLength;
//   - a packet size of 8, 16, 32, 64, 512 or 1024 (the whole wMaxPacketSize field) on the bulk endpoints.
//
// A reader that needs anything more from the host (a PIN pad, a secure command, a clock change, a bulk message of another form, a
// second slot) is refused or never used: the reader sends only GetSlotStatus, IccPowerOn, IccPowerOff and XfrBlock.
//
// # Calls and the budget
//
// The calls are SlotStatus, PowerOn (the answer to reset), PowerOff and Transmit, and Close. Each takes the total budget of the
// call. The budget covers the whole exchange, every time-extension message that the card sends while it waits for a person
// included; it is at most MaxBudget (90 seconds, the bound that the code this layer was ported from put on one command). No context is taken:
// the port cannot cancel a blocking transfer, so a context could not be honoured. The caller derives the budget from its own deadline
// before the call, and checks its context between calls.
//
// Each bulk transfer has a timeout of its own (Options.TransferTimeout, DefaultTransferTimeout 10 seconds), cut to what is left of the
// budget. A card that is waiting for a touch keeps sending time-extension messages, so no transfer waits that long with no sign.
//
// # The lost session
//
// A transfer that timed out, a transfer that failed, a short write, a message that breaks the protocol, an exchange whose budget
// ran out after the call's first Port call, a panic of the Port and a card-level failure in the middle of a chain all leave the
// stream between the host and the reader out of step: the layer below says a late answer may arrive on the next read, and that what
// a timed-out read had moved is lost. This layer then marks the session LOST. The error of that call wraps ErrSessionLost and the
// cause (a usbccid error such as ErrTimeout, ErrStalled, ErrGone or ErrClosed stays reachable with errors.Is, in its own meaning,
// and ErrBudgetExhausted or ErrProtocol is the cause when the layer found it), and every later call returns the same error without
// touching the port. The caller closes the Reader and opens it again. The ErrStalled, ErrGone and ErrClosed of the Port end the
// session in the same way.
//
// A budget that runs out ends the session only once the call has made a call on the Port (the first read of the drain counts). A call
// whose budget is spent before that has sent and read nothing: it returns an error that wraps ErrBudgetExhausted and not
// ErrSessionLost, and the session, the power state and the drain stay as they were, so the next call works.
//
// # Sequence numbers and the first exchange
//
// A late answer of an earlier session can still sit in the device's IN queue when a new session begins: the earlier one lost its
// exchange on the budget while the card waited for a person, the person acted later, and the card's answer came after the caller
// had closed and opened again. Two rules keep such an answer from being taken for an answer of the new session.
//
//   - The sequence numbers come from ONE counter of the process, shared by every Reader and seeded at random (crypto/rand) at its first
//     use. A new Reader never starts where an old one was, and never at a place that can be guessed. Two Readers alive at once take
//     turns from the counter, so each sees its own numbers with gaps; every answer is matched against the number of the message it
//     answers, and the numbers are unique in any window of 256 messages of the process.
//   - The first exchange of a session, whichever call it belongs to, DRAINS the IN pipe before it sends anything: it reads (and drops
//     and clears) what is there, each read with a timeout of DrainReadTimeout (20 ms), until a read times out, which means that the pipe
//     is quiet; an empty packet that ended an earlier message is dropped with the rest, so the first read of a message after the drain
//     starts clean. It makes at most MaxDrainReads (8) reads and takes at most MaxDrainTime (250 ms); a device that is not quiet by then
//     keeps sending without being asked, and the session is lost with ErrProtocol. ErrStalled, ErrGone and ErrClosed in the drain end
//     the session as everywhere. On a quiet device it costs one timed-out read (DrainReadTimeout), and its time counts against the
//     budget of that call. The drain only reads, so Open itself still sends and reads nothing: it runs at the first exchange.
//
// A late answer of an earlier session cannot match until the counter has gone round, which is 256 messages of the process later.
// What remains is an answer that arrives after the drain with the number and the type of the pending command, which only a device
// that still holds an answer, or a hostile device, can produce; the layer above covers it by the rules of "What the layer above must
// not assume".
//
// # What it never does
//
// It never sends an abort (PC_to_RDR_Abort) and never tries to resynchronise by itself: this transport has no control transfer, which
// an abort needs. It never retries a transfer or a command, never reconnects, never resets the device, never selects a configuration
// and never takes a claim away from another holder. It sends no SetParameters, Escape, Secure, SetDataRateAndClockFrequency or any
// PIN message. It does not power the card off on Close: the caller decides. A card-level failure that the reader reports in its answer
// (a *CommandError) is returned as it is, with the status bytes, and the session goes on when the failure answers the only message of
// a command.
//
// # Chaining, empty packets and sizes
//
// A command that does not fit one CCID message (extended APDU level only) is sent in blocks (wLevelParameter begin, middle, end), each
// answered by an empty block that asks for the next, and a response that comes in blocks is collected by empty continuation blocks.
// A response is whole, or it is a begin block, then middle blocks, then an end block; a chain parameter in any other order ends the
// session. At short APDU level there is no chain: any chain parameter but 0 in any DataBlock answer, failed or not, ends the session.
// The answer to reset is one whole block at every level: a chain parameter but 0 in it ends the session. A reader that sends empty
// middle blocks without an end is bounded by the budget, not by MaxResponseAPDU (as with time extensions).
// The messages are as large as the reader's dwMaxCCIDMessageLength, held to MaxMessageLength (the 64 KiB of one transfer of the Port).
//
// Reads are sized from the same length: the buffer holds the largest message and one packet. The reader asks for the first packet of
// a message, takes dwLength from the header, and asks for exactly the rest, so a read never goes beyond its message. A message whose
// length is an exact multiple of the packet size of the bulk IN endpoint may be followed by an empty packet, which the next read
// returns as zero bytes: that one empty read, at the start of the next message, is consumed and is not an answer; an empty read
// anywhere else is a protocol error. A transfer that ends on a short packet before the length that the header gives is a message cut
// short, also a protocol error. An answer for another sequence number is a stale or foreign one, read whole and dropped, at most
// MaxStaleAnswers of them in one exchange; the next one ends the session.
//
// # The limits
//
// MaxShortCommandAPDU (261) and MaxCommandAPDU (65544) bound a command, MaxResponseAPDU (65538) a response over all its blocks,
// MaxStaleAnswers (3) the skipped answers of one exchange, MaxATR (33) the answer to reset, MaxDrainReads (8), MaxDrainTime (250 ms)
// and DrainReadTimeout (20 ms) the drain, MaxBudget (90 s) the budget of one
// exchange, MinMessageLength (271) the smallest message length a reader may declare, and MaxMessageLength (65536) the message size
// held. The time-extension messages of one exchange have no count of their own: the budget bounds them. Each refusal names its limit.
//
// # Errors and secrets
//
// The errors are values to test with errors.Is: the refusals of Open (ErrNoInterface, ErrBadClass, ErrExchangeLevel and the
// others), ErrSessionLost with its causes, the argument and state errors (ErrBadBudget, a budget spent before the call's first Port
// call, ErrAPDUTooShort, ErrAPDUTooLong, ErrNotPowered, ErrStillActive, ErrBadATR, ErrNotOpen for the zero Reader, a nil Reader and
// a nil Port) that leave the session as it was, and the card-level failures of the reader's answer (ErrNoCard, ErrCardMute,
// ErrCardParity, ErrCardProtocol, ErrSlotBusy, ErrReaderFault, ErrCommandNotSupported, ErrBadParameter, all of them
// ErrCommandFailed), where errors.As to *CommandError gives bError and bmICCStatus. The text of an error holds counts, header
// numbers and status bytes, and never a byte of a command or of a response: a command can carry a PIN.
//
// The reader copies a command into a buffer of its own and reads every message into another, and clears both before each call
// returns (and the reader's copy of a response that it dropped, and the array that a growing response left). It cannot clear the
// caller's own slices (the command it was given, the response it returned), the Port's copies or the kernel's: usbfs copies a transfer
// through the kernel's memory, and what a USB host controller held is not reachable from here.
//
// # What the layer above must not assume
//
//   - Transmit moves the bytes of ONE APDU. It builds no command, reads no status word, and does not follow a continuation such as 61xx
//     (GET RESPONSE) or 6Cxx: those, and every card application, belong to the layer above, which also splits an application command
//     that is longer than MaxCommandAPDU.
//   - A lost session cannot be repaired. After ErrSessionLost nothing is sent until the caller closes the Reader and opens it again
//     (a new Port from the usbccid layer, a new claim, a new PowerOn). The layer above treats whatever it held of the card (a
//     selected application, a verified PIN, a secure channel) as gone.
//   - A session begins with PowerOn, and the layer above never treats the first answers of a session as proof of anything that
//     happened before it. A late answer of an earlier session is dropped by the drain and by the sequence counter, but the rest risk
//     (above) is not nil: a verified PIN, a signature or a selected application is established only by an exchange of this session
//     that the layer above made after PowerOn, with data that it chose fresh for it.
//   - After ANY error, not only a lost session (also a *CommandError such as a mute card or an aborted command, a refused answer,
//     ErrNotPowered after a success that said the card was off), the card may have carried out the command. A PIN verification or a
//     signature is never to be assumed not to have happened. Success means only that a well-formed answer with the right sequence
//     number arrived; the reader knows nothing of status words, and a response of 6A82 is a success here. A command that must not run
//     twice is not repeated after an error without the layer above finding out by its own means.
//   - A device that sends time-extension messages without end costs a busy loop up to the budget: the budget is the only bound on
//     them (there is no count). The caller gives a budget that it can afford to spend.
//   - A card status in an answer that says "not active" (also on a success) makes the Reader refuse Transmit until PowerOn succeeds
//     again, and the answer that carried it is not returned.
//   - Time spent waiting for a person counts against the budget of the exchange. A command that needs a touch must be given a budget
//     that covers the touch and the transfers; when the budget runs out first, the session is lost and the card may still be waiting.
//   - Whether a power cycle (PowerOff then PowerOn, or a new session) ends a verified PIN state on a given card is not known from this
//     layer. The reader reports what the card and the reader answered, and nothing else: that must be measured on a real card.
//   - The reader does not decide which card is meant. The serial number of a card comes from the card application, and the layer above
//     compares it before it sends anything else.
//   - A Reader does not know the transfer size of the system below its Port; a Port that fails a transfer of the size the reader asked
//     for ends the session.
//   - The descriptors are a snapshot from Open. A reader that changes its mind afterwards is not noticed until a message fails.
//
// # Safety rule for tests
//
// A test of this package or of a program above it uses a fake Port and never opens a node under /dev/bus/usb or reads the real
// /sys/bus/usb: a machine that runs the tests may have real security keys plugged in.
package ccid
