//go:build !linux

package usbccid

import "time"

// Device is one open usbfs node. On this system there is none: Open returns ErrNotSupported and the methods of a Device that was
// never opened do the same. The type is here so that a program that links this package compiles.
type Device struct{}

// Open returns ErrNotSupported: direct USB access needs Linux (usbfs).
func Open(_ string) (*Device, error) { return nil, ErrNotSupported }

// Descriptors returns ErrNotSupported.
func (*Device) Descriptors() ([]byte, error) { return nil, ErrNotSupported }

// Claim returns ErrNotSupported.
func (*Device) Claim(_ int) error { return ErrNotSupported }

// Release returns ErrNotSupported.
func (*Device) Release(_ int) error { return ErrNotSupported }

// BulkOut returns ErrNotSupported.
func (*Device) BulkOut(_ uint8, _ []byte, _ time.Duration) (int, error) {
	return 0, ErrNotSupported
}

// BulkIn returns ErrNotSupported.
func (*Device) BulkIn(_ uint8, _ []byte, _ time.Duration) (int, error) {
	return 0, ErrNotSupported
}

// Close returns ErrNotSupported.
func (*Device) Close() error { return ErrNotSupported }

var _ Port = (*Device)(nil)

// Discover returns ErrNotSupported: there is no sysfs on this system.
func Discover(_, _ string, _ Filter) ([]Candidate, error) { return nil, ErrNotSupported }

// DiscoverHost returns ErrNotSupported.
func DiscoverHost(_ Filter) ([]Candidate, error) { return nil, ErrNotSupported }
