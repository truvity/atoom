package cardtest

import (
	"fmt"
	"sync"

	"github.com/truvity/atoom/key/yubikeypiv/internal/card"
)

// ReaderName is the name of the one reader of Card.Opener, in the form pcsc-lite gives (the name of the driver, the index and the
// slot).
const ReaderName = "Fake Reader 00 00"

// Reader is one reader of a Bench: its name, the card in it (none when nil) and whether another process holds it.
type Reader struct {
	Name string
	Card *Card
	// Busy makes a connection to the reader fail as a reader that another process holds does.
	Busy bool
}

// Bench is a set of fake PC/SC readers, a card.Backend that counts the connections made to each reader, so that a test can say
// which readers the driver touched. Listing the readers connects to no card and is counted apart.
type Bench struct {
	mu      sync.Mutex
	readers []Reader
	opens   map[string]int
	listed  int
}

// NewBench returns a bench of the readers, in the order given.
func NewBench(readers ...Reader) *Bench {
	return &Bench{readers: readers, opens: map[string]int{}}
}

// Opener returns the opener of the bench: card.OpenWith over it.
func (b *Bench) Opener() card.Opener {
	return func(sel card.Selector) (card.Card, error) { return card.OpenWith(b, sel) }
}

// Readers implements card.Backend: the names, with no connection.
func (b *Bench) Readers() ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.listed++
	names := make([]string, len(b.readers))
	for i, r := range b.readers {
		names[i] = r.Name
	}
	return names, nil
}

// Connect implements card.Backend: it counts the connection, then fails as a held reader or an empty one does, or gives the card.
func (b *Bench) Connect(reader string) (card.Card, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.opens[reader]++
	for _, r := range b.readers {
		if r.Name != reader {
			continue
		}
		if r.Busy {
			return nil, card.SharingError(reader)
		}
		if r.Card == nil {
			return nil, fmt.Errorf("card: open reader %q: no card in the reader", reader)
		}
		r.Card.mu.Lock()
		defer r.Card.mu.Unlock()
		if err := r.Card.init(); err != nil {
			return nil, err
		}
		r.Card.closed = false
		return r.Card, nil
	}
	return nil, fmt.Errorf("card: open reader %q: unknown reader", reader)
}

// Opens is how many connections were made to the reader.
func (b *Bench) Opens(reader string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.opens[reader]
}

// TotalOpens is how many connections were made to any reader.
func (b *Bench) TotalOpens() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, v := range b.opens {
		n += v
	}
	return n
}

// Listed is how many times the readers were listed.
func (b *Bench) Listed() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.listed
}

var _ card.Backend = (*Bench)(nil)
