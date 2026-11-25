package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/truvity/atoom/blocks/token"
	"github.com/truvity/atoom/blocks/token/conformance"
	"github.com/truvity/atoom/key/purpose"
)

func TestUsage(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"--key-socket", "k"},
		{"--store-socket", "s"},
		{"--key-socket", "k", "--store-socket", "s", "--socket", "b"},
		{"--key-socket", "k", "--store-socket", "s", "--without-rules"},
		{"--print-declaration", "--print-purposes"},
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
	for _, name := range []string{"wrapping", conformance.PurposeES384, conformance.PurposeES256, conformance.PurposeRS256} {
		if _, ok := set.Lookup(name); !ok {
			t.Errorf("no %s in %s", name, out.String())
		}
	}
}

func TestPrintPurposesIsWhatTheThinMainTakes(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"--print-purposes"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	ps, err := token.ParseDeclaration(strings.NewReader(out.String()))
	if err != nil || len(ps) != len(conformance.Purposes()) {
		t.Fatalf("%v: %s", err, out.String())
	}
}

func TestAHostedBlockOverSocketsThatDoNotAnswerFails(t *testing.T) {
	var out, errb bytes.Buffer
	dir, err := os.MkdirTemp("/tmp", "atoom-tkc")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if code := run(context.Background(), []string{"--key-socket", dir + "/k", "--store-socket", dir + "/s"}, &out, &errb); code != 1 {
		t.Fatalf("exit %d, want 1: %s", code, errb.String())
	}
}
