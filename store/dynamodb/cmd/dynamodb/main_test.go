package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"nope"}, {"serve"}, {"serve", "--socket", "/x", "--socket-mode", "zz"}, {"serve", "--socket", "/x", "extra"}, {"create-table", "extra"}, {"check", "--nope"}} {
		var out, errb bytes.Buffer
		if code := run(args, &out, &errb); code != 2 {
			t.Errorf("run(%v) = %d, want 2 (%s)", args, code, errb.String())
		}
	}
}

func TestVersion(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"version"}, &out, &bytes.Buffer{}); code != 0 || strings.TrimSpace(out.String()) == "" {
		t.Fatalf("version: %d %q", code, out.String())
	}
}
