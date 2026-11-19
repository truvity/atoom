//go:build !atoom_card

package main

import (
	"bytes"
	"strings"
	"testing"
)

// A build without the tag atoom_card has no C code and the same commands, and refuses to open a card.
func TestWithoutTheTagTheCardIsRefused(t *testing.T) {
	var errb bytes.Buffer
	code := run([]string{"check", "--environment", "development"}, &bytes.Buffer{}, &errb)
	if code != 1 || !strings.Contains(errb.String(), "atoom_card") {
		t.Fatalf("exit %d (%s), want 1 and the name of the tag", code, errb.String())
	}
}

// --reader is a flag of serve and of check, and reaches the card opener, which a build without the tag refuses.
func TestTheReaderFlagIsAccepted(t *testing.T) {
	var errb bytes.Buffer
	code := run([]string{"check", "--environment", "development", "--reader", "Some Reader 00 00"}, &bytes.Buffer{}, &errb)
	if code != 1 || !strings.Contains(errb.String(), "atoom_card") {
		t.Fatalf("exit %d (%s), want 1 and the name of the tag, not a usage error", code, errb.String())
	}
}
