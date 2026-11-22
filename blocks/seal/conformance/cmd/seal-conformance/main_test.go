package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/truvity/atoom/key/purpose"
)

func TestUsage(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"--key-socket", "k"},
		{"--store-socket", "s"},
		{"--key-socket", "k", "--store-socket", "s", "--socket", "b"},
		{"extra"},
		{"--no-such-flag"},
	} {
		var out, errb bytes.Buffer
		if code := run(context.Background(), args, &out, &errb); code != 2 {
			t.Errorf("%q: exit %d, want 2 (%s)", args, code, errb.String())
		}
	}
}

func TestPrintDeclarationIsWhatTheKeyDriverTakes(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"--print-declaration"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	set, err := purpose.Parse(strings.NewReader(out.String()))
	if err != nil {
		t.Fatalf("the declaration does not parse: %v\n%s", err, out.String())
	}
	for _, name := range []string{"wrapping", "kit-es384", "kit-es256", "kit-rs256"} {
		if _, ok := set.Lookup(name); !ok {
			t.Errorf("no %s in %s", name, out.String())
		}
	}
}

func TestAHostedBlockOverSocketsThatDoNotAnswerFails(t *testing.T) {
	var out, errb bytes.Buffer
	dir := t.TempDir()
	if code := run(context.Background(), []string{"--key-socket", dir + "/k", "--store-socket", dir + "/s"}, &out, &errb); code != 1 {
		t.Fatalf("exit %d, want 1: %s", code, errb.String())
	}
}
