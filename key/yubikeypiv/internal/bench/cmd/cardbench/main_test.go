package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/truvity/atoom/key/yubikeypiv/internal/card"
	"github.com/truvity/atoom/key/yubikeypiv/internal/card/cardtest"
)

func fakeDeps(names ...string) deps {
	rs := make([]cardtest.Reader, len(names))
	for i, n := range names {
		rs[i] = cardtest.Reader{Name: n, Card: cardtest.New()}
	}
	b := cardtest.NewBench(rs...)
	return deps{readers: b.Readers, open: b.Opener()}
}

func do(args []string, d deps) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(context.Background(), args, &out, &errb, d)
	return code, out.String(), errb.String()
}

func TestUsageErrorsExitWithTwo(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"nonsense"},
		{"readers", "extra"},
		{"status"},
		{"unlock"},
		{"status", "--admin-socket", "/tmp/x", "extra"},
		{"hold"},
		{"hold", "--reader", "R"},
		{"hold", "--seconds", "5"},
		{"hold", "--reader", "R", "--seconds", "0"},
		{"hold", "--reader", "R", "--seconds", "301"},
	} {
		if code, _, _ := do(args, fakeDeps("R")); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}

func TestReadersListsOneNameOnEachLine(t *testing.T) {
	code, out, _ := do([]string{"readers"}, fakeDeps("Reader A 00 00", "Reader B 00 01"))
	if code != 0 || out != "Reader A 00 00\nReader B 00 01\n" {
		t.Fatalf("exit %d, output %q", code, out)
	}
}

func TestReadersFailsWithTheReasonOfTheBuild(t *testing.T) {
	d := deps{readers: func() ([]string, error) { return nil, card.ErrNoCardSupport }}
	code, _, errb := do([]string{"readers"}, d)
	if code != 1 || !strings.Contains(errb, "atoom_card") {
		t.Fatalf("exit %d (%s), want 1 and the name of the tag", code, errb)
	}
	d = deps{readers: func() ([]string, error) { return nil, errors.New("boom") }}
	if code, _, _ := do([]string{"readers"}, d); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
}

func TestHoldHoldsTheNamedReader(t *testing.T) {
	code, out, errb := do([]string{"hold", "--reader", "Reader A 00 00", "--seconds", "1"}, fakeDeps("Reader A 00 00"))
	if code != 0 || !strings.Contains(out, "held:") || !strings.Contains(out, "released:") {
		t.Fatalf("exit %d, output %q, error %q", code, out, errb)
	}
}

func TestHoldOfAReaderThatIsNotThereFails(t *testing.T) {
	code, _, errb := do([]string{"hold", "--reader", "Nope", "--seconds", "1"}, fakeDeps("Reader A 00 00"))
	if code != 1 || !strings.Contains(errb, "not there") {
		t.Fatalf("exit %d (%s), want 1 and the refusal for a reader that is not there", code, errb)
	}
}

func TestStatusOfASocketNobodyServesFails(t *testing.T) {
	code, _, errb := do([]string{"status", "--admin-socket", "/nonexistent/atoom-bench.sock"}, fakeDeps())
	if code != 1 || errb == "" {
		t.Fatalf("exit %d (%s), want 1", code, errb)
	}
}
