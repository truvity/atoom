package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truvity/atoom/key/conformance"
	"github.com/truvity/atoom/key/purpose"
)

// --print-declaration writes the declaration that a host starts the driver under test with, and it is the file that the thin mains read.
func TestThePrintedDeclarationIsTheKitsAndParses(t *testing.T) {
	out := filepath.Join(t.TempDir(), "purposes.json")
	stdout, err := os.Create(out) //nolint:gosec // a file of the test
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = stdout
	code := run([]string{"--print-declaration"})
	os.Stdout = old
	_ = stdout.Close()
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	f, err := os.Open(out) //nolint:gosec // a file of the test
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	set, err := purpose.Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	want := purpose.MustNew(conformance.Declaration())
	if strings.Join(set.Names(), ",") != strings.Join(want.Names(), ",") {
		t.Fatalf("names %v, want %v", set.Names(), want.Names())
	}
	for _, d := range want.List() {
		got, _ := set.Lookup(d.Name)
		if got.Algorithm != d.Algorithm || got.Slot != d.Slot || len(got.Operations) != len(d.Operations) {
			t.Errorf("%s: %+v, want %+v", d.Name, got, d)
		}
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"extra"}, {"--socket", "/x", "extra"}} {
		if code := run(args); code != 2 {
			t.Errorf("run(%v) = %d, want 2", args, code)
		}
	}
}
