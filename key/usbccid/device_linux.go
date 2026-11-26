//go:build linux

package usbccid

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ioctl request numbers. They are built the way <asm-generic/ioctl.h> does, so the size of the argument struct follows the
// architecture.
const (
	iocWrite = 1
	iocRead  = 2

	iocNRShift   = 0
	iocTypeShift = 8
	iocSizeShift = 16
	iocDirShift  = 30
)

func ioc(dir, typ, nr, size uintptr) uintptr {
	return dir<<iocDirShift | size<<iocSizeShift | typ<<iocTypeShift | nr<<iocNRShift
}

// bulkTransfer is struct usbdevfs_bulktransfer.
type bulkTransfer struct {
	Ep      uint32
	Len     uint32
	Timeout uint32 // milliseconds
	Data    unsafe.Pointer
}

// The three requests this package uses. There is deliberately no USBDEVFS_DISCONNECT_CLAIM, no USBDEVFS_IOCTL and no
// USBDEVFS_RESET here.
var (
	reqBulk    = ioc(iocRead|iocWrite, 'U', 2, unsafe.Sizeof(bulkTransfer{}))
	reqClaim   = ioc(iocRead, 'U', 15, 4)
	reqRelease = ioc(iocRead, 'U', 16, 4)
)

const (
	maxDescriptorBytes = 64 * 1024
	// maxInterface is the largest interface number the claim ioctl is asked for (USB interface numbers are one byte).
	maxInterface = 255
	// maxTransfer is the longest transfer handed to the kernel; the kernel refuses a length of INT_MAX/2 or more.
	maxTransfer = 1 << 24
)

// Device is one open usbfs node. It implements Port.
//
// The node is opened once, the file descriptor is kept, and the node is never reopened by path. The first fatal error (the device
// is gone or failed for good) is latched: every later call returns an error that wraps ErrGone without a system call, and the
// Device never recovers; the caller closes it and starts again. Calls are serialised by a mutex, so there is one exchange with the
// device at a time, and Close waits for a transfer in progress.
type Device struct {
	mu      sync.Mutex
	fd      int
	closed  bool
	dead    error // the first fatal errno; later calls return it wrapped in ErrGone
	claimed map[int]bool
	desc    []byte

	// ioctl is the system call. It is the real one except in this package's tests, which script the answers of the kernel.
	ioctl func(fd int, req uintptr, arg unsafe.Pointer) (uintptr, unix.Errno)
}

// Open opens the usbfs node at path exactly once and reads the descriptors of the device. The path must name a character device
// (a node under /dev/bus/usb, normally Candidate.NodePath). It opens the node read-write, so the user needs the right to do so
// (a udev rule, or the owner); a refusal wraps the errno, and errors.Is(err, os.ErrPermission) tells it. Open claims nothing and
// sends nothing.
func Open(path string) (*Device, error) { return open(path, true) }

// open is Open. needChar is false only in this package's tests, which pass a regular file under t.TempDir() to run the
// ioctl code against a file that refuses every request.
func open(path string, needChar bool) (*Device, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return nil, &os.PathError{Op: "fstat", Path: path, Err: err}
	}
	if needChar && st.Mode&unix.S_IFMT != unix.S_IFCHR {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("usbccid: %s is not a character device", path)
	}
	buf := make([]byte, maxDescriptorBytes)
	n, err := unix.Pread(fd, buf, 0)
	if err != nil {
		_ = unix.Close(fd)
		return nil, &os.PathError{Op: "read descriptors", Path: path, Err: err}
	}
	return &Device{fd: fd, claimed: map[int]bool{}, desc: buf[:n], ioctl: sysIoctl}, nil
}

func sysIoctl(fd int, req uintptr, arg unsafe.Pointer) (uintptr, unix.Errno) {
	r, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), req, uintptr(arg))
	return r, e
}

// Descriptors returns the raw descriptors read at Open.
func (d *Device) Descriptors() ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.state(); err != nil {
		return nil, err
	}
	return append([]byte(nil), d.desc...), nil
}

// state returns the error that makes every call fail, or nil. d.mu is held.
func (d *Device) state() error {
	if d.closed {
		return ErrClosed
	}
	if d.dead != nil {
		return fmt.Errorf("%w: %w", ErrGone, d.dead)
	}
	return nil
}

// fatal reports whether err means that the device is gone or broken for good.
func fatal(err error) bool {
	var e unix.Errno
	if !errors.As(err, &e) {
		return false
	}
	switch e {
	case unix.ENODEV, unix.EIO, unix.ESHUTDOWN, unix.EPROTO, unix.EILSEQ:
		return true
	}
	return false
}

// note latches err when it is fatal and none is latched yet. d.mu is held.
func (d *Device) note(err error) {
	if err != nil && d.dead == nil && fatal(err) {
		d.dead = err
	}
}

// fail makes the error of a failed request: the latch is set when the errno is fatal, and the error then wraps ErrGone; a busy
// claim wraps ErrBusy and a timeout ErrTimeout; every error wraps the errno. d.mu is held.
func (d *Device) fail(what string, e unix.Errno) error {
	d.note(e)
	switch {
	case fatal(e):
		return fmt.Errorf("usbccid: %s: %w: %w", what, ErrGone, e)
	case e == unix.ETIMEDOUT:
		return fmt.Errorf("usbccid: %s: %w: %w", what, ErrTimeout, e)
	}
	return fmt.Errorf("usbccid: %s: %w", what, e)
}

// Claim takes interface n with USBDEVFS_CLAIMINTERFACE. A busy interface returns an error that wraps ErrBusy (and EBUSY). Nothing
// is detached, the device is not reset, and Claim never retries.
func (d *Device) Claim(n int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.state(); err != nil {
		return err
	}
	if n < 0 || n > maxInterface {
		return fmt.Errorf("usbccid: claim interface %d: %w", n, unix.EINVAL)
	}
	v := uint32(n)
	_, e := d.ioctl(d.fd, reqClaim, unsafe.Pointer(&v)) //nolint:gosec // G103: the kernel ioctl argument, which the type of the request fixes
	runtime.KeepAlive(&v)
	if e != 0 {
		if e == unix.EBUSY {
			return fmt.Errorf("usbccid: claim interface %d: %w: %w", n, ErrBusy, e)
		}
		return d.fail(fmt.Sprintf("claim interface %d", n), e)
	}
	d.claimed[n] = true
	return nil
}

// Release gives interface n back. Releasing an interface that is not claimed is not an error, and neither is releasing after the
// device is gone (the kernel dropped the claim with the device).
func (d *Device) Release(n int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.release(n)
}

func (d *Device) release(n int) error {
	if d.closed || !d.claimed[n] {
		return nil
	}
	delete(d.claimed, n)
	if d.dead != nil {
		return nil // the kernel dropped the claim with the device
	}
	v := uint32(n)                                        //nolint:gosec // claimed only after the check of Claim: 0 to 255
	_, e := d.ioctl(d.fd, reqRelease, unsafe.Pointer(&v)) //nolint:gosec // G103: the kernel ioctl argument, which the type of the request fixes
	runtime.KeepAlive(&v)
	if e != 0 {
		err := d.fail(fmt.Sprintf("release interface %d", n), e)
		if fatal(e) {
			return nil // the device went away under the release, and the claim with it
		}
		return err
	}
	return nil
}

// ms is a timeout in the whole milliseconds the kernel takes: at least 1 (zero would mean "wait for ever"), at most 2^30.
func ms(t time.Duration) uint32 {
	if t <= 0 {
		return 1
	}
	m := (t + time.Millisecond - 1) / time.Millisecond
	if m > 1<<30 {
		m = 1 << 30
	}
	return uint32(m)
}

func (d *Device) bulk(ep uint8, p []byte, timeout time.Duration) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.state(); err != nil {
		return 0, err
	}
	if len(p) > maxTransfer {
		return 0, fmt.Errorf("usbccid: bulk transfer on endpoint 0x%02X of %d bytes: %w", ep, len(p), unix.EINVAL)
	}
	bt := bulkTransfer{Ep: uint32(ep), Len: uint32(len(p)), Timeout: ms(timeout)} //nolint:gosec // len(p) is at most maxTransfer, checked above
	if len(p) > 0 {
		bt.Data = unsafe.Pointer(&p[0]) //nolint:gosec // G103: the buffer of the caller, which the kernel fills or reads during the call
	}
	// The ioctl returns the byte count.
	r, e := d.ioctl(d.fd, reqBulk, unsafe.Pointer(&bt)) //nolint:gosec // G103: the kernel ioctl argument, which the type of the request fixes
	runtime.KeepAlive(p)
	runtime.KeepAlive(&bt)
	if e != 0 {
		return 0, d.fail(fmt.Sprintf("bulk transfer on endpoint 0x%02X", ep), e)
	}
	return int(r), nil
}

// BulkOut writes data to the bulk OUT endpoint ep (the direction bit of ep is cleared) in one transfer. The transfer blocks until
// it ends or the timeout runs out, and a context cannot cancel it. A timeout of zero or less is 1 ms.
func (d *Device) BulkOut(ep uint8, data []byte, timeout time.Duration) (int, error) {
	return d.bulk(ep&^0x80, data, timeout)
}

// BulkIn reads one transfer of at most len(buf) bytes from the bulk IN endpoint ep (the direction bit of ep is set). The transfer
// blocks until it ends or the timeout runs out, and a context cannot cancel it. A timeout of zero or less is 1 ms.
func (d *Device) BulkIn(ep uint8, buf []byte, timeout time.Duration) (int, error) {
	return d.bulk(ep|0x80, buf, timeout)
}

// Close releases the claimed interfaces and closes the descriptor. It is safe to call twice (the second call does nothing and
// returns nil), also after the device is gone. It waits for a transfer in progress. After Close every call returns ErrClosed.
func (d *Device) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	var first error
	for n := range d.claimed {
		if err := d.release(n); err != nil && first == nil {
			first = err
		}
	}
	d.closed = true
	if err := unix.Close(d.fd); err != nil && first == nil {
		first = err
	}
	return first
}

// requestNumbers returns the three request numbers, for the test that checks their layout.
func requestNumbers() (bulk, claim, release uintptr) { return reqBulk, reqClaim, reqRelease }

var _ Port = (*Device)(nil)
