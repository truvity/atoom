package card_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/truvity/atoom/key/yubikeypiv/internal/card"
	"github.com/truvity/atoom/key/yubikeypiv/internal/card/cardtest"
)

// The reader names are in the form pcsc-lite gives: the driver's name, the index, the slot.
const (
	readerA = "Fake Reader A 00 00"
	readerB = "Fake Reader B 01 00"
	readerC = "Fake Reader C 02 00"
)

// fakeCard is a card with its own serial.
func fakeCard(serial uint32) *cardtest.Card {
	c := cardtest.New()
	c.SerialNo = serial
	return c
}

// bench of three readers: the card the operator names (serial 1001), the operator's daily card and one more.
func threeReaders() (*cardtest.Bench, *cardtest.Card, *cardtest.Card) {
	mine, daily := fakeCard(1001), fakeCard(2002)
	return cardtest.NewBench(
		cardtest.Reader{Name: readerA, Card: mine},
		cardtest.Reader{Name: readerB, Card: daily},
		cardtest.Reader{Name: readerC, Card: fakeCard(3003)},
	), mine, daily
}

// verifies: D12 C (2026-11-19), "with more than one reader and none named, refuse instead of trying each".
func TestSeveralReadersAndNoneNamedOpenNone(t *testing.T) {
	for name, sel := range map[string]card.Selector{
		"nothing named":   {},
		"a serial alone":  {Serial: 1001},
		"another serial":  {Serial: 9999},
		"an empty reader": {Reader: "", Serial: 2002},
	} {
		t.Run(name, func(t *testing.T) {
			b, _, _ := threeReaders()
			c, err := b.Opener()(sel)
			if c != nil || !errors.Is(err, card.ErrAmbiguousReader) {
				t.Fatalf("got %v, %v: want the refusal ErrAmbiguousReader and no card", c, err)
			}
			if n := b.TotalOpens(); n != 0 {
				t.Fatalf("%d connections were made: the refusal must connect to no reader", n)
			}
			for _, want := range []string{readerA, readerB, readerC, "--reader", "3 readers"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal %q does not say %q", err, want)
				}
			}
			if b.Listed() != 1 {
				t.Errorf("the readers were listed %d times, once is the rule", b.Listed())
			}
		})
	}
}

func TestANamedReaderOpensOnlyThatOne(t *testing.T) {
	b, mine, _ := threeReaders()
	c, err := b.Opener()(card.Selector{Reader: readerA})
	if err != nil {
		t.Fatal(err)
	}
	if c != card.Card(mine) {
		t.Fatal("not the card of the named reader")
	}
	if b.Opens(readerA) != 1 || b.Opens(readerB) != 0 || b.Opens(readerC) != 0 || b.TotalOpens() != 1 {
		t.Fatalf("connections: A %d, B %d, C %d: only the named reader may be connected to, once", b.Opens(readerA), b.Opens(readerB), b.Opens(readerC))
	}
}

func TestANamedReaderWithItsSerialOpens(t *testing.T) {
	b, mine, _ := threeReaders()
	c, err := b.Opener()(card.Selector{Reader: readerA, Serial: 1001})
	if err != nil || c != card.Card(mine) {
		t.Fatalf("got %v, %v", c, err)
	}
	if b.TotalOpens() != 1 {
		t.Fatalf("%d connections, want 1", b.TotalOpens())
	}
}

// The serial is checked after the connection to the named reader and never sends the driver to another reader, even to the one
// whose card has that serial.
func TestASerialThatDiffersIsRefusedAndNoOtherReaderIsTried(t *testing.T) {
	b, mine, daily := threeReaders()
	// The operator named reader A and the serial of the card in reader B.
	c, err := b.Opener()(card.Selector{Reader: readerA, Serial: 2002})
	if c != nil || !errors.Is(err, card.ErrSerialMismatch) {
		t.Fatalf("got %v, %v: want ErrSerialMismatch", c, err)
	}
	for _, want := range []string{"1001", "2002", readerA} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not say %q (both serials and the reader)", err, want)
		}
	}
	if b.Opens(readerA) != 1 || b.Opens(readerB) != 0 || b.Opens(readerC) != 0 {
		t.Fatalf("connections: A %d, B %d, C %d: the other readers must never be connected to", b.Opens(readerA), b.Opens(readerB), b.Opens(readerC))
	}
	if !mine.Closed() {
		t.Error("the card of the wrong serial was not released")
	}
	if daily.Closed() {
		t.Error("another card was touched")
	}
}

func TestANamedReaderThatIsNotThereIsRefusedWithoutAConnection(t *testing.T) {
	b, _, _ := threeReaders()
	_, err := b.Opener()(card.Selector{Reader: "Fake Reader Z 09 00"})
	if !errors.Is(err, card.ErrNoCard) || !strings.Contains(err.Error(), readerB) {
		t.Fatalf("got %v: want ErrNoCard naming the readers there are", err)
	}
	if b.TotalOpens() != 0 {
		t.Fatalf("%d connections, want none", b.TotalOpens())
	}
}

func TestOneReaderOpensWithoutAName(t *testing.T) {
	only := fakeCard(1001)
	for name, sel := range map[string]card.Selector{
		"nothing named":   {},
		"its serial":      {Serial: 1001},
		"its name":        {Reader: readerA},
		"name and serial": {Reader: readerA, Serial: 1001},
	} {
		t.Run(name, func(t *testing.T) {
			b := cardtest.NewBench(cardtest.Reader{Name: readerA, Card: only})
			c, err := b.Opener()(sel)
			if err != nil || c != card.Card(only) {
				t.Fatalf("got %v, %v", c, err)
			}
			if b.TotalOpens() != 1 {
				t.Fatalf("%d connections, want 1", b.TotalOpens())
			}
		})
	}
}

func TestOneReaderWithAnotherSerialIsRefused(t *testing.T) {
	b := cardtest.NewBench(cardtest.Reader{Name: readerA, Card: fakeCard(1001)})
	if _, err := b.Opener()(card.Selector{Serial: 2002}); !errors.Is(err, card.ErrSerialMismatch) {
		t.Fatalf("got %v, want ErrSerialMismatch", err)
	}
}

func TestNoReaderIsNoCard(t *testing.T) {
	b := cardtest.NewBench()
	if _, err := b.Opener()(card.Selector{}); !errors.Is(err, card.ErrNoCard) {
		t.Fatalf("got %v, want ErrNoCard", err)
	}
}

// A reader that another process holds is an error that names it, never a skip: not when it is the only reader, and not when it is
// the named one while the card the serial names sits in a free reader.
func TestABusyReaderIsAnErrorAndNotASkip(t *testing.T) {
	const text = "in use by another process (e.g. a scdaemon or other pcscd client); stop it and retry"
	check := func(t *testing.T, err error, reader string) {
		t.Helper()
		if !errors.Is(err, card.ErrSharing) {
			t.Fatalf("got %v, want ErrSharing", err)
		}
		for _, want := range []string{"reader " + `"` + reader + `"`, text} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the error %q does not say %q", err, want)
			}
		}
	}
	t.Run("the only reader", func(t *testing.T) {
		b := cardtest.NewBench(cardtest.Reader{Name: readerA, Card: fakeCard(1001), Busy: true})
		_, err := b.Opener()(card.Selector{Serial: 1001})
		check(t, err, readerA)
	})
	t.Run("the named reader, the serial is in a free one", func(t *testing.T) {
		b := cardtest.NewBench(
			cardtest.Reader{Name: readerA, Card: fakeCard(1001), Busy: true},
			cardtest.Reader{Name: readerB, Card: fakeCard(2002)},
		)
		_, err := b.Opener()(card.Selector{Reader: readerA, Serial: 2002})
		check(t, err, readerA)
		if b.Opens(readerB) != 0 {
			t.Fatal("the free reader was tried after the busy one")
		}
	})
	t.Run("a busy reader that is not named is left alone", func(t *testing.T) {
		b := cardtest.NewBench(
			cardtest.Reader{Name: readerA, Card: fakeCard(1001), Busy: true},
			cardtest.Reader{Name: readerB, Card: fakeCard(2002)},
		)
		c, err := b.Opener()(card.Selector{Reader: readerB, Serial: 2002})
		if err != nil || c == nil {
			t.Fatalf("got %v, %v", c, err)
		}
		if b.Opens(readerA) != 0 {
			t.Fatal("the busy reader was connected to though it was not named")
		}
	})
}
