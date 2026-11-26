//go:build !(linux && !android && (amd64 || arm64))

package usbccid

import (
	"errors"
	"testing"
	"time"
)

// On a system that is not Linux on amd64 or arm64 the package builds and every entry point returns ErrNotSupported, except Release and
// Close of a Device, which return nil (there is nothing to release or to close). CI runs this on macOS.
func TestNotSupported(t *testing.T) {
	dir := t.TempDir()
	if _, err := Open(dir); !errors.Is(err, ErrNotSupported) {
		t.Errorf("Open: %v", err)
	}
	if _, err := Discover(dir, dir, Filter{}); !errors.Is(err, ErrNotSupported) {
		t.Errorf("Discover: %v", err)
	}
	if _, err := DiscoverHost(Filter{}); !errors.Is(err, ErrNotSupported) {
		t.Errorf("DiscoverHost: %v", err)
	}
	if _, err := (Candidate{NodePath: dir}).Open(); !errors.Is(err, ErrNotSupported) {
		t.Errorf("Candidate.Open: %v", err)
	}
	var p Port = &Device{}
	if _, err := p.Descriptors(); !errors.Is(err, ErrNotSupported) {
		t.Errorf("Descriptors: %v", err)
	}
	if err := p.Claim(0); !errors.Is(err, ErrNotSupported) {
		t.Errorf("Claim: %v", err)
	}
	if err := p.Release(0); err != nil {
		t.Errorf("Release: %v, want nil", err)
	}
	if _, err := p.BulkOut(2, nil, time.Second); !errors.Is(err, ErrNotSupported) {
		t.Errorf("BulkOut: %v", err)
	}
	if _, err := p.BulkIn(0x82, nil, time.Second); !errors.Is(err, ErrNotSupported) {
		t.Errorf("BulkIn: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Errorf("Close: %v, want nil", err)
	}
	if err := p.Close(); err != nil {
		t.Errorf("second Close: %v, want nil", err)
	}
}
