package yubikeypiv_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/truvity/atoom/key/yubikeypiv"
	"github.com/truvity/atoom/key/yubikeypiv/internal/card"
	"github.com/truvity/atoom/key/yubikeypiv/internal/card/cardtest"
)

func twoCards() (*cardtest.Bench, *cardtest.Card, *cardtest.Card) {
	mine, daily := cardtest.New(), cardtest.New()
	mine.SerialNo, daily.SerialNo = 1001, 2002
	return cardtest.NewBench(
		cardtest.Reader{Name: "Fake Reader A 00 00", Card: mine},
		cardtest.Reader{Name: "Fake Reader B 01 00", Card: daily},
	), mine, daily
}

// Options.Reader reaches the opener: the driver starts on the named reader and never connects to the other one, which holds the
// operator's daily card.
func TestTheDriverOpensOnlyTheNamedReader(t *testing.T) {
	b, mine, daily := twoCards()
	s, err := yubikeypiv.New(yubikeypiv.Options{Environment: yubikeypiv.Production, Reader: "Fake Reader A 00 00", Serial: mine.SerialNo, Open: b.Opener()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if b.Opens("Fake Reader B 01 00") != 0 || daily.Closed() {
		t.Fatal("the other reader was touched")
	}
}

// Two readers and no reader named: the driver does not start, whatever the serial, and no reader is connected to.
func TestTheDriverRefusesToChooseAmongReaders(t *testing.T) {
	for _, env := range []string{yubikeypiv.Development, yubikeypiv.Production} {
		b, mine, _ := twoCards()
		_, err := yubikeypiv.New(yubikeypiv.Options{Environment: env, Serial: mine.SerialNo, Open: b.Opener()})
		if !errors.Is(err, card.ErrAmbiguousReader) || !strings.Contains(err.Error(), "--reader") {
			t.Fatalf("%s: got %v, want the refusal that names --reader", env, err)
		}
		if b.TotalOpens() != 0 {
			t.Fatalf("%s: %d connections, want none", env, b.TotalOpens())
		}
	}
}

func TestTheDriverRefusesTheSerialOfAnotherReader(t *testing.T) {
	b, _, daily := twoCards()
	_, err := yubikeypiv.New(yubikeypiv.Options{Environment: yubikeypiv.Production, Reader: "Fake Reader A 00 00", Serial: daily.SerialNo, Open: b.Opener()})
	if !errors.Is(err, card.ErrSerialMismatch) {
		t.Fatalf("got %v, want ErrSerialMismatch", err)
	}
	if b.Opens("Fake Reader B 01 00") != 0 {
		t.Fatal("the reader of that serial was connected to")
	}
}
