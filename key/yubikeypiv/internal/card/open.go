package card

import (
	"fmt"
	"strings"
)

// Selector says which card to open. The driver connects to the card of one reader only, and chooses nothing.
//
//   - Reader is the exact PC/SC reader name (what SCardListReaders returns, such as "Yubico YubiKey OTP+FIDO+CCID 00 00").
//     When it is set, that reader is the only one the driver connects to.
//   - Serial, when it is not 0, is checked against the card of that reader after it is connected to; it never chooses a reader,
//     because a serial cannot be read without connecting to the card.
//
// With no Reader, the driver opens the only reader there is, and refuses when there are several (ErrAmbiguousReader).
type Selector struct {
	Reader string
	Serial uint32
}

// Backend is the PC/SC side of opening a card: a list of reader names, which connects to no card, and a connection to one
// reader. The real one is piv-go over pcsc-lite (piv.go); the tests have a bench of fake readers (cardtest).
type Backend interface {
	// Readers lists the reader names (SCardListReaders). It connects to no card and selects no applet.
	Readers() ([]string, error)
	// Connect connects to the card of the named reader exclusively and returns it. A reader that another process holds
	// answers with SharingError(reader), whatever the text of the layer below.
	Connect(reader string) (Card, error)
}

// releaser is a Card that can be let go without the reset that Close makes, which a card of the wrong serial needs: a
// card that is not ours is disconnected and left as it was.
type releaser interface{ Release() error }

// SharingError is the error of a reader that another process holds: it names the reader and says what to do. It wraps
// ErrSharing.
func SharingError(reader string) error {
	return fmt.Errorf("%w: reader %q is in use by another process (e.g. a scdaemon or other pcscd client); stop it and retry", ErrSharing, reader)
}

// OpenWith opens the card that sel names through b, by these rules (decision D12 C, 2026-11-19):
//
//  1. With sel.Reader set, only that reader is connected to. It must be in the list of readers (the list is not a
//     connection); if it is not, the error names the readers there are.
//  2. With no reader named, exactly one reader is opened; with none the error is ErrNoCard, and with more than one it is
//     ErrAmbiguousReader, which lists the names and says to pass the reader, and no reader is connected to. A Serial does
//     not change this: it can be read only from a connected card.
//  3. When sel.Serial is not 0, the serial of the connected card is read and compared. A different one releases the card
//     and is ErrSerialMismatch naming both serials; no other reader is tried.
//  4. A reader that another process holds is an error that names it (SharingError), never a skip.
func OpenWith(b Backend, sel Selector) (Card, error) {
	readers, err := b.Readers()
	if err != nil {
		return nil, fmt.Errorf("card: list readers: %w", err)
	}
	reader := sel.Reader
	switch {
	case reader != "":
		if !contains(readers, reader) {
			return nil, fmt.Errorf("%w: reader %q is not there; the readers are %s", ErrNoCard, reader, names(readers))
		}
	case len(readers) == 0:
		return nil, fmt.Errorf("%w: there is no reader", ErrNoCard)
	case len(readers) > 1:
		return nil, fmt.Errorf("%w: %d readers (%s); no card was opened. Name the reader (--reader, Options.Reader); a serial alone cannot choose, because it is read from the card after connecting to it",
			ErrAmbiguousReader, len(readers), names(readers))
	default:
		reader = readers[0]
	}
	c, err := b.Connect(reader)
	if err != nil {
		return nil, err
	}
	if sel.Serial == 0 {
		return c, nil
	}
	got, err := c.Serial()
	if err != nil {
		let(c)
		return nil, fmt.Errorf("card: read the serial of the card in reader %q: %w", reader, err)
	}
	if got != sel.Serial {
		let(c)
		return nil, fmt.Errorf("%w: reader %q holds the card with serial %d, the serial named is %d; no other reader was tried", ErrSerialMismatch, reader, got, sel.Serial)
	}
	return c, nil
}

// let releases a card that is not ours without resetting it where it can, and closes it otherwise.
func let(c Card) {
	if r, ok := c.(releaser); ok {
		_ = r.Release()
		return
	}
	_ = c.Close()
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func names(readers []string) string {
	q := make([]string, len(readers))
	for i, r := range readers {
		q[i] = fmt.Sprintf("%q", r)
	}
	return strings.Join(q, ", ")
}
