package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUsage(t *testing.T) {
	for _, args := range [][]string{
		nil, {"nope"}, {"serve"}, {"serve", "--socket", "/x"}, {"serve", "--dir", "/x"},
		{"serve", "--socket", "/x", "--dir", "/x", "--socket-mode", "zz"}, {"serve", "--socket", "/x", "--dir", "/x", "extra"},
		{"check"}, {"check", "--dir", "/x", "extra"}, {"check", "--nope"},
	} {
		var out, errb bytes.Buffer
		if code := run(args, &out, &errb); code != 2 {
			t.Errorf("run(%v) = %d, want 2 (%s)", args, code, errb.String())
		}
	}
}

func TestVersion(t *testing.T) {
	// `version` and `--version` say the same, and the release smoke runs the second (tools/release/smoke.sh).
	for _, arg := range []string{"version", "--version"} {
		var out bytes.Buffer
		if code := run([]string{arg}, &out, &bytes.Buffer{}); code != 0 || strings.TrimSpace(out.String()) == "" {
			t.Fatalf("%s: %d %q", arg, code, out.String())
		}
	}
}

// `check` of a directory that is not there is a refusal: exit 3, so that a script tells it from a bad flag.
func TestCheckOfAMissingDirectoryExitsThree(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"check", "--dir", filepath.Join(t.TempDir(), "nothing")}, &out, &errb); code != 3 {
		t.Fatalf("check of a missing directory = %d, want 3 (%s)", code, errb.String())
	}
	f := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"check", "--dir", f}, &out, &errb); code != 3 {
		t.Fatalf("check of a file = %d, want 3", code)
	}
}

func TestCheckOfADirectoryIsOK(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"check", "--dir", t.TempDir()}, &out, &bytes.Buffer{}); code != 0 || !strings.Contains(out.String(), "ok") {
		t.Fatalf("check = %d %q", code, out.String())
	}
}

// No directory, no service: `serve` of a directory that is not there fails before it makes a socket.
func TestServeOfAMissingDirectoryFailsBeforeTheSocketExists(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "s")
	var errb bytes.Buffer
	if code := run([]string{"serve", "--socket", socket, "--dir", filepath.Join(dir, "nothing")}, &bytes.Buffer{}, &errb); code != 1 {
		t.Fatalf("serve = %d, want 1 (%s)", code, errb.String())
	}
	if _, err := os.Lstat(socket); err == nil {
		t.Fatal("a socket was made for a directory that is not there")
	}
}
