//go:build !atoom_card

package card

// HasCardSupport is false in a build without the tag atoom_card: the driver's logic runs, over a fake card in the
// tests, and Open says why it cannot open a real one.
const HasCardSupport = false

// Open always fails in a build without card support.
func Open(Selector) (Card, error) { return nil, ErrNoCardSupport }

// ListReaders always fails in a build without card support.
func ListReaders() ([]string, error) { return nil, ErrNoCardSupport }
