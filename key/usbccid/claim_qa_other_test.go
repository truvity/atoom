//go:build !(linux && !android && (amd64 || arm64))

package usbccid

import (
	"errors"
	"testing"
)

// On a system that is not supported, Claim answers ErrNotSupported every time, and never says that the interface is held: a Device of
// this system holds nothing.
func TestQAStubClaimNeverSaysAlreadyClaimed(t *testing.T) {
	var p Port = &Device{}
	for range 3 {
		err := p.Claim(2)
		if !errors.Is(err, ErrNotSupported) || errors.Is(err, ErrAlreadyClaimed) {
			t.Fatalf("Claim: %v", err)
		}
	}
}
