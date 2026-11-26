//go:build linux && !android && (amd64 || arm64)

package usbccid

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ioctl request numbers. They are built the way <asm-generic/ioctl.h> does, which is the layout of linux on amd64 and arm64, the only
// systems this file is built for (the layout differs on mips, powerpc and sparc); the size of the argument struct follows the architecture.
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

// procFdDir is the per-process directory of descriptors that openChecked reopens a node through. It is "/proc/self/fd" and is set only
// by this package's tests (nothing exported reaches it), which point it at a missing directory, to see ErrNoProcSelfFd, and at a
// directory that exists, to see ErrGone.
var procFdDir = "/proc/self/fd"

const (
	maxDescriptorBytes = 64 * 1024
	// maxInterface is the largest interface number the claim ioctl is asked for (USB interface numbers are one byte).
	maxInterface = 255
	// maxTransfer is the longest transfer handed to the kernel: 64 KiB. The kernel allocates the whole length for each transfer.
	maxTransfer = 64 * 1024
	// maxTimeout is the ceiling of the timeout of one transfer. A longer timeout is cut to it.
	maxTimeout = 10 * time.Minute
	// usbMajor is the major number of the character devices of the USB device nodes (/dev/bus/usb/BBB/DDD).
	usbMajor = 189
)

// Device is one open usbfs node. It implements Port.
//
// The node is opened once, the file descriptor is kept, and the node is never reopened by path. The descriptors are read at Open and
// held: a claim is accepted only for a CCID interface (class 0x0B) that they hold, and a bulk call only for a bulk endpoint of an
// interface this handle has claimed, in the direction of the call, so that the kernel is never asked to claim another interface
// (a passkey or one-time-code interface of the same card) on its own.
//
// The first fatal error (the device is gone or failed for good) is latched: every later call returns an error that wraps ErrGone
// without a system call, and the Device never recovers; the caller closes it and starts again. Calls are serialised by a mutex, so
// there is one exchange with the device at a time. Descriptors, Release and Close wait behind a transfer in progress, which ends
// within the timeout of that transfer (at most ten minutes).
//
// After Close, Release and Close return nil and every other call returns ErrClosed (ErrClosed wins over ErrGone). After the device is
// gone, Release and Close return nil and every other call returns ErrGone. A Device that is never closed keeps its file descriptor
// until the process ends: there is no finalizer. The zero Device (var d Device, new(Device)) was never opened: it holds no descriptor,
// every call returns ErrClosed except Release and Close, which return nil and touch nothing.
type Device struct {
	mu      sync.Mutex
	fd      int
	opened  bool // set only by the real open: the zero Device holds no descriptor and is closed
	closed  bool
	dead    error // the first fatal error; later calls return it wrapped in ErrGone
	stall   error // the first stall (EPIPE) of a bulk call; later bulk calls return it wrapped in ErrStalled
	claimed map[int]bool
	desc    []byte
	lay     layout // what desc holds, read at Open; the claim and the bulk calls are held to it

	// ioctl is the system call. It is the real one except in this package's tests, which script the answers of the kernel.
	ioctl func(fd int, req uintptr, arg unsafe.Pointer) (uintptr, unix.Errno)
}

// Open opens the usbfs node at path exactly once and reads the descriptors of the device. It first looks at the path without
// opening the device (O_PATH, never following a symbolic link in the last component) and refuses everything that is not a character
// device with the major number of the USB nodes, 189: a wrong path (a watchdog, a terminal, a regular file, a FIFO) is never opened
// for use. Only then does it open the same inode read-write without a controlling terminal and without blocking. The user needs the
// right to open it (a udev rule, or the owner); a refusal wraps the errno, and errors.Is(err, os.ErrPermission) tells it. Open
// claims nothing and sends nothing. A refusal of the kind of node is an error that wraps ErrNotUSBNode.
func Open(path string) (*Device, error) { return open(path, true) }

// open is Open. usb is false only in this package's tests, which pass a regular file under t.TempDir() to run the open logic and the
// ioctl code against a file that refuses every request: the node must then be a regular file.
func open(path string, usb bool) (*Device, error) {
	fd, err := openChecked(path, func(st *unix.Stat_t) error { return checkNode(path, st, usb) }, unix.O_RDWR)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, maxDescriptorBytes)
	n, err := unix.Pread(fd, buf, 0)
	if err != nil {
		_ = unix.Close(fd)
		if errors.Is(err, unix.ENODEV) || errors.Is(err, unix.ESHUTDOWN) {
			return nil, fmt.Errorf("usbccid: %s: read descriptors: %w: %w", path, ErrGone, err)
		}
		return nil, &os.PathError{Op: "read descriptors", Path: path, Err: err}
	}
	desc := buf[:n]
	lay, err := parseLayout(desc)
	if err != nil {
		_ = unix.Close(fd)
		if errors.Is(err, ErrSeveralConfigurations) {
			return nil, fmt.Errorf("%w: %s", ErrSeveralConfigurations, path)
		}
		return nil, fmt.Errorf("%w: %s: %v", ErrBadDescriptors, path, err) //nolint:errorlint // the rule that failed is text, not an error to match
	}
	return &Device{opened: true, fd: fd, claimed: map[int]bool{}, desc: desc, lay: lay, ioctl: sysIoctl}, nil
}

// checkNode decides from the stat result of a node whether it may be opened: with usb true a character device with the major number
// of the USB nodes, with usb false (tests only) a regular file.
func checkNode(path string, st *unix.Stat_t, usb bool) error {
	if !usb {
		if st.Mode&unix.S_IFMT != unix.S_IFREG {
			return fmt.Errorf("%w: %s is not a regular file (a test node)", ErrNotUSBNode, path)
		}
		return nil
	}
	if st.Mode&unix.S_IFMT != unix.S_IFCHR {
		return fmt.Errorf("%w: %s is not a character device", ErrNotUSBNode, path)
	}
	if m := unix.Major(st.Rdev); m != usbMajor {
		return fmt.Errorf("%w: %s is a character device of major %d, not %d", ErrNotUSBNode, path, m, usbMajor)
	}
	return nil
}

// openChecked opens path without ever opening what is not allowed. It first opens the path with O_PATH, which opens nothing and
// never blocks, and without following a symbolic link in the last component, and runs check on the stat result. Only then does it
// open the same inode through /proc/self/fd with flags, no controlling terminal and no blocking, and checks the result again, so
// that the inode is the one that was checked. The O_PATH descriptor is closed on every path.
func openChecked(path string, check func(*unix.Stat_t) error, flags int) (int, error) {
	pfd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, &os.PathError{Op: "open", Path: path, Err: err}
	}
	defer func() { _ = unix.Close(pfd) }()
	var st unix.Stat_t
	if err := unix.Fstat(pfd, &st); err != nil {
		return -1, &os.PathError{Op: "fstat", Path: path, Err: err}
	}
	if err := check(&st); err != nil {
		return -1, err
	}
	proc := procFdDir + "/" + strconv.Itoa(pfd)
	fd, err := unix.Open(proc, flags|unix.O_CLOEXEC|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.ENODEV) {
			// The node existed a moment ago, so either /proc/self/fd is not there, or the device went away between the two opens (the
			// kernel answers the same). An access check tells them apart. The errno is not wrapped, so that neither error reads as a
			// node that does not exist.
			if unix.Access(procFdDir, unix.F_OK) != nil {
				return -1, fmt.Errorf("%w: %s cannot be reopened through %s: %v", ErrNoProcSelfFd, path, proc, err) //nolint:errorlint // see above
			}
			return -1, fmt.Errorf("usbccid: %s: %w: unplugged between the two opens: %v", path, ErrGone, err) //nolint:errorlint // see above
		}
		return -1, &os.PathError{Op: "open", Path: path, Err: err}
	}
	var st2 unix.Stat_t
	if err := unix.Fstat(fd, &st2); err != nil {
		_ = unix.Close(fd)
		return -1, &os.PathError{Op: "fstat", Path: path, Err: err}
	}
	if st2.Dev != st.Dev || st2.Ino != st.Ino || st2.Rdev != st.Rdev || check(&st2) != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("%w: %s changed while it was opened", ErrNotUSBNode, path)
	}
	return fd, nil
}

func sysIoctl(fd int, req uintptr, arg unsafe.Pointer) (uintptr, unix.Errno) {
	r, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), req, uintptr(arg))
	return r, e
}

// openCandidate is Candidate.Open: it opens the node of the candidate and holds what it opened to what was found.
func openCandidate(c Candidate) (*Device, error) {
	d, err := Open(c.NodePath)
	if err != nil {
		return nil, err
	}
	if err := d.verify(c); err != nil {
		_ = d.Close()
		return nil, err
	}
	return d, nil
}

// verify checks that the device behind the node is the one the candidate described: the same vendor and product id, and the CCID
// interface of the candidate with its two bulk endpoints. The device number can be reused between Discover and Open (unplug, then
// another device plugged), and the node of the old number is then another device.
func (d *Device) verify(c Candidate) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	wrong := func(why string) error {
		return fmt.Errorf("%w: %s (node %s, port %s)", ErrWrongDevice, why, c.NodePath, c.PortPath)
	}
	if d.lay.vendor != c.VendorID || d.lay.product != c.ProductID {
		return wrong(fmt.Sprintf("the ids are %04x:%04x, not %04x:%04x", d.lay.vendor, d.lay.product, c.VendorID, c.ProductID))
	}
	in := d.lay.find(c.Interface)
	if in == nil || in.class != ClassCCID {
		return wrong(fmt.Sprintf("it has no CCID interface %d", c.Interface))
	}
	out, err1 := d.lay.bulkOwner(c.BulkOut)
	inn, err2 := d.lay.bulkOwner(c.BulkIn)
	if c.BulkOut&0x80 != 0 || c.BulkIn&0x80 == 0 || err1 != nil || err2 != nil || out != in || inn != in {
		return wrong(fmt.Sprintf("interface %d has not the bulk endpoints 0x%02x and 0x%02x", c.Interface, c.BulkOut, c.BulkIn))
	}
	return nil
}

// Descriptors returns the raw descriptors read at Open. It waits behind a transfer in progress.
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
	if d.closed || !d.opened {
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

// Claim takes interface n with USBDEVFS_CLAIMINTERFACE, for an interface that the descriptors hold, whose class is 0x0B (CCID) and that has both a bulk OUT and a bulk IN endpoint (this layer uses a CCID interface only through both, as Discover requires of a candidate);
// any other number (a number outside 0 to 255 included, which no longer reaches the kernel to be refused as an invalid argument) is
// refused before the kernel is asked, with an error that wraps ErrNoSuchInterface. A busy interface returns an
// error that wraps ErrBusy (and EBUSY); the holder may also be another handle of the same process. Nothing is detached, the device
// is not reset, and Claim never retries.
func (d *Device) Claim(n int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.state(); err != nil {
		return err
	}
	in := (*ifaceDesc)(nil)
	if n >= 0 && n <= maxInterface {
		in = d.lay.find(n)
	}
	if in == nil || in.class != ClassCCID || !in.bothBulk() {
		return fmt.Errorf("usbccid: claim interface %d: %w", n, ErrNoSuchInterface)
	}
	v := uint32(n)                                      //nolint:gosec // n is the number of an interface of the descriptors: 0 to 255
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
// device is gone (the kernel dropped the claim with the device) or after Close. It waits behind a transfer in progress.
func (d *Device) Release(n int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.release(n)
}

func (d *Device) release(n int) error {
	if d.closed || !d.opened || !d.claimed[n] {
		return nil
	}
	if d.dead != nil {
		delete(d.claimed, n)
		return nil // the kernel dropped the claim with the device
	}
	v := uint32(n)                                        //nolint:gosec // claimed only after the check of Claim: 0 to 255
	_, e := d.ioctl(d.fd, reqRelease, unsafe.Pointer(&v)) //nolint:gosec // G103: the kernel ioctl argument, which the type of the request fixes
	runtime.KeepAlive(&v)
	if e != 0 {
		err := d.fail(fmt.Sprintf("release interface %d", n), e)
		if fatal(e) {
			delete(d.claimed, n)
			return nil // the device went away under the release, and the claim with it
		}
		return err // the kernel may still hold the claim: the entry stays, a second Release tries again, and Close releases it
	}
	delete(d.claimed, n)
	return nil
}

// ms is a timeout in the whole milliseconds the kernel takes: at least 1 (zero would mean "wait for ever"), at most the ten
// minutes of maxTimeout. The clamp comes before the rounding up, so that no duration overflows.
func ms(t time.Duration) uint32 {
	if t <= 0 {
		return 1
	}
	if t > maxTimeout {
		t = maxTimeout
	}
	return uint32((t + time.Millisecond - 1) / time.Millisecond)
}

// owns reports whether ep may be used for a bulk transfer: it must be a bulk endpoint of exactly one interface of the descriptors
// (an address that two interfaces list is refused, because usbfs would pick an interface by itself and claim it), and that interface
// must be claimed by this handle. Otherwise it returns the error that says why. d.mu is held.
func (d *Device) owns(ep uint8) error {
	in, err := d.lay.bulkOwner(ep)
	if err != nil {
		return err
	}
	if d.claimed[in.number] {
		return nil
	}
	if in.class == ClassCCID {
		return ErrNotClaimed
	}
	return ErrBadEndpoint // an interface that is never claimed
}

func (d *Device) bulk(ep uint8, p []byte, timeout time.Duration, in bool) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.state(); err != nil {
		return 0, err
	}
	what := fmt.Sprintf("bulk transfer on endpoint 0x%02X", ep)
	if d.stall != nil {
		return 0, fmt.Errorf("usbccid: %s: %w: %w", what, ErrStalled, d.stall)
	}
	if (ep&0x80 != 0) != in {
		return 0, fmt.Errorf("usbccid: %s: the direction does not fit the call: %w", what, ErrBadEndpoint)
	}
	if err := d.owns(ep); err != nil {
		return 0, fmt.Errorf("usbccid: %s: %w", what, err)
	}
	if len(p) > maxTransfer || (in && len(p) == 0) {
		return 0, fmt.Errorf("usbccid: %s of %d bytes: %w", what, len(p), ErrBadLength)
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
		if e == unix.EPIPE {
			d.stall = e // the first stall ends the use of the handle: nothing here clears a halt
			return 0, fmt.Errorf("usbccid: %s: %w: %w", what, ErrStalled, e)
		}
		return 0, d.fail(what, e)
	}
	if r > uintptr(len(p)) {
		// A count larger than the buffer is a kernel or a layer below that cannot be trusted: the device is failed for good.
		cause := fmt.Errorf("the kernel counted %d bytes for a buffer of %d", r, len(p))
		if d.dead == nil {
			d.dead = cause
		}
		return 0, fmt.Errorf("usbccid: %s: %w: %w", what, ErrGone, cause)
	}
	return int(r), nil
}

// BulkOut writes data to the bulk OUT endpoint ep in one transfer. ep must be the address of a bulk OUT endpoint (direction bit
// clear) of an interface this handle has claimed, else the call is refused before the kernel is asked (ErrBadEndpoint, or
// ErrNotClaimed for an endpoint of another CCID interface). data may be empty, and is at most 64 KiB (ErrBadLength). The transfer
// blocks until it ends or the timeout runs out, and a context cannot cancel it. A timeout of zero or less is 1 ms, and one above ten
// minutes is ten minutes. A system call that the kernel interrupted is reported, not retried.
func (d *Device) BulkOut(ep uint8, data []byte, timeout time.Duration) (int, error) {
	return d.bulk(ep, data, timeout, false)
}

// BulkIn reads one transfer of at most len(buf) bytes from the bulk IN endpoint ep. ep must be the address of a bulk IN endpoint
// (direction bit set) of an interface this handle has claimed, as for BulkOut. buf must not be empty and is at most 64 KiB
// (ErrBadLength). The transfer blocks until it ends or the timeout runs out, and a context cannot cancel it. The timeout is bounded as
// for BulkOut. A count that the kernel returns above len(buf) is a fatal error and latches ErrGone.
func (d *Device) BulkIn(ep uint8, buf []byte, timeout time.Duration) (int, error) {
	return d.bulk(ep, buf, timeout, true)
}

// Close releases the claimed interfaces and closes the descriptor. It is safe to call twice (the second call does nothing and
// returns nil), also after the device is gone. It waits behind a transfer in progress. After Close, Release and Close return nil
// and every other call returns ErrClosed.
func (d *Device) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || !d.opened {
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
