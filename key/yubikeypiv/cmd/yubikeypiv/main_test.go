package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestUsage(t *testing.T) {
	for _, args := range [][]string{
		nil, {"nope"}, {"serve"}, {"serve", "--environment", "development"}, {"check", "extra"},
		{"serve", "--environment", "development", "--socket", "/x", "--admin-socket", "/y", "--node", "n", "--socket-mode", "zz"},
		{"serve", "--environment", "development", "--socket", "/x", "--admin-socket", "/y", "--node", "n", "--socket-gid", "0"},
		{"serve", "--environment", "development", "--socket", "/x", "--admin-socket", "/y", "--node", "n", "--allow-any-uid", "--allow-uid", "1"},
	} {
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

// The environment is checked before anything is opened: a value that is not one is a usage error, and no socket or card is touched.
func TestAnUnknownEnvironmentIsAUsageError(t *testing.T) {
	var errb bytes.Buffer
	code := run([]string{"serve", "--environment", "prod", "--socket", "/nonexistent/s", "--admin-socket", "/nonexistent/a", "--node", "n"}, &bytes.Buffer{}, &errb)
	if code != 2 || !strings.Contains(errb.String(), "environment") {
		t.Fatalf("exit %d (%s)", code, errb.String())
	}
}

// Outside development the card is named by its serial: without one the start is a usage error.
func TestOutsideDevelopmentASerialIsRequired(t *testing.T) {
	var errb bytes.Buffer
	if code := run([]string{"check", "--environment", "production"}, &bytes.Buffer{}, &errb); code != 2 {
		t.Fatalf("exit %d (%s)", code, errb.String())
	}
}
