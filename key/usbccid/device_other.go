//go:build !(linux && !android && (amd64 || arm64))

package usbccid

import "time"

// Device is one open usbfs node. On this system (anything but Linux, not Android, on amd64 or arm64) there is none: Open returns ErrNotSupported and the methods of a Device that was
// never opened do the same. The type is here so that a program that links this package compiles.
type Device struct{}

// Open returns ErrNotSupported: direct USB access needs Linux (usbfs) on amd64 or arm64.
func Open(_ string) (*Device, error) { return nil, ErrNotSupported }

// Descriptors returns ErrNotSupported.
func (*Device) Descriptors() ([]byte, error) { return nil, ErrNotSupported }

// Claim returns ErrNotSupported (on a system that supports it, an interface held already is ErrAlreadyClaimed).
func (*Device) Claim(_ int) error { return ErrNotSupported }

// Release returns nil: a Device of this system holds nothing to release.
func (*Device) Release(_ int) error { return nil }

// BulkOut returns ErrNotSupported.
func (*Device) BulkOut(_ uint8, _ []byte, _ time.Duration) (int, error) {
	return 0, ErrNotSupported
}

// BulkIn returns ErrNotSupported.
func (*Device) BulkIn(_ uint8, _ []byte, _ time.Duration) (int, error) {
	return 0, ErrNotSupported
}

// Close returns nil: a Device of this system holds nothing to close.
func (*Device) Close() error { return nil }

var _ Port = (*Device)(nil)

// Discover returns ErrNotSupported: there is no sysfs on this system.
func Discover(_, _ string, _ Filter) ([]Candidate, error) { return nil, ErrNotSupported }

// DiscoverHost returns ErrNotSupported.
func DiscoverHost(_ Filter) ([]Candidate, error) { return nil, ErrNotSupported }

// openCandidate returns ErrNotSupported.
func openCandidate(Candidate) (*Device, error) { return nil, ErrNotSupported }
