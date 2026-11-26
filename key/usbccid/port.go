package usbccid

import "time"

// Port is what a CCID reader needs from one USB device. Every method is called from one goroutine at a time. *Device implements it
// over a real usbfs node; a test above this package implements it with an in-memory fake, which is the test seam.
//
// A Port is claimed per interface and carries the latched failure of a gone device: once a call returned an error that wraps
// ErrGone, every later call returns one too (see Device).
type Port interface {
	// Descriptors returns the raw descriptors of the device as the kernel holds them: the 18-byte device descriptor, then each
	// configuration descriptor with all its interface, class and endpoint descriptors. They are read once, at Open.
	Descriptors() ([]byte, error)

	// Claim takes interface n with a plain claim. A claim refused because another holder has the interface returns an error for
	// which errors.Is(err, ErrBusy) is true. Nothing is detached, and the caller must not retry on its own.
	Claim(n int) error

	// Release gives interface n back. Releasing an interface that is not claimed is not an error.
	Release(n int) error

	// BulkOut writes data to the bulk OUT endpoint ep (address; the direction bit is cleared) in one transfer and returns the
	// number of bytes sent. The transfer is bounded by timeout and cannot be cancelled once started; a timeout of zero or less is
	// the shortest one the kernel takes (1 ms), never "no limit". A timeout is an error for which errors.Is(err, ErrTimeout) holds.
	BulkOut(ep uint8, data []byte, timeout time.Duration) (int, error)

	// BulkIn reads one transfer of at most len(buf) bytes from the bulk IN endpoint ep (address; the direction bit is set). The
	// transfer ends when len(buf) bytes arrived or a short packet came. It is bounded by timeout in the same way as BulkOut.
	BulkIn(ep uint8, buf []byte, timeout time.Duration) (int, error)

	// Close releases every claimed interface and closes the node. It is safe to call twice. It waits for a transfer in progress.
	Close() error
}
