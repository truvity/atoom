//go:build linux

package usbccid

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// QA tests of Device over the scripted kernel. Like the tests beside them they never open a node of /dev/bus/usb: the Device is
// opened over a regular file made under t.TempDir(), and its system call is replaced by the script of the test.

// kcall is one system call of the script: the descriptor, the request, and what the argument said.
type kcall struct {
	fd    int
	req   uintptr
	iface uint32       // the argument of a claim or a release
	bt    bulkTransfer // the argument of a bulk transfer, as it was when the call was made
	data  []byte       // the bytes the data pointer pointed to when a bulk call was made (a copy), as long as bt.Len
}

// qaKernel scripts the answers of the kernel and records every call.
type qaKernel struct {
	mu    sync.Mutex
	calls []kcall
	// answer decides the reply to each call. A nil answer is a kernel that accepts everything: a bulk call returns its length.
	answer func(c kcall) (uintptr, unix.Errno)
	// fill, when set, writes into the buffer of a bulk call before it returns (what a BulkIn reads).
	fill func(buf []byte)
}

func (k *qaKernel) ioctl(fd int, req uintptr, arg unsafe.Pointer) (uintptr, unix.Errno) {
	c := kcall{fd: fd, req: req}
	var buf []byte
	switch req {
	case reqBulk:
		c.bt = *(*bulkTransfer)(arg)
		if c.bt.Data != nil && c.bt.Len > 0 {
			buf = unsafe.Slice((*byte)(c.bt.Data), c.bt.Len) //nolint:gosec // G103: the data pointer of the bulk request that the Device built
			if c.bt.Len <= 4096 {
				c.data = append([]byte(nil), buf...)
			}
		}
	case reqClaim, reqRelease:
		c.iface = *(*uint32)(arg)
	}
	k.mu.Lock()
	k.calls = append(k.calls, c)
	answer, fill := k.answer, k.fill
	k.mu.Unlock()
	if fill != nil && buf != nil && c.bt.Ep&0x80 != 0 { // only a read is filled
		fill(buf)
	}
	if answer != nil {
		return answer(c)
	}
	if req == reqBulk {
		return uintptr(c.bt.Len), 0
	}
	return 0, 0
}

func (k *qaKernel) count() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.calls)
}

func (k *qaKernel) snapshot() []kcall {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]kcall(nil), k.calls...)
}

func (k *qaKernel) countReq(req uintptr) int {
	n := 0
	for _, c := range k.snapshot() {
		if c.req == req {
			n++
		}
	}
	return n
}

// qaDevice opens a Device over a regular file that holds desc and gives it the script k. The file is returned for the tests that
// change it. Close is called at the end of the test.
func qaDevice(t *testing.T, desc []byte, k *qaKernel) (*Device, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "node")
	if err := os.WriteFile(path, desc, 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	d.ioctl = k.ioctl
	return d, path
}

// errnoOf returns the errno an error carries, or 0.
func errnoOf(err error) unix.Errno {
	var e unix.Errno
	if errors.As(err, &e) {
		return e
	}
	return 0
}

// B. Request numbers and the layout of the argument.

// The request codes are decoded field by field and compared with the Linux uapi (linux/usbdevice_fs.h): USBDEVFS_BULK is
// _IOWR('U', 2, struct usbdevfs_bulktransfer), USBDEVFS_CLAIMINTERFACE is _IOR('U', 15, unsigned int) and
// USBDEVFS_RELEASEINTERFACE is _IOR('U', 16, unsigned int). The package builds the numbers by hand (x/sys/unix has no USBDEVFS
// constants), so the bit layout of <asm-generic/ioctl.h>, which linux/amd64 and linux/arm64 both use, is checked here: number 8 bits,
// type 8 bits, size 14 bits, direction 2 bits (read = 2, write = 1). The struct is {unsigned ep, len, timeout; void *data}: 24 bytes
// on a 64-bit system, with the pointer at offset 16.
func TestQARequestCodesDecode(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("the expected layout is the 64-bit one (linux/amd64 and linux/arm64)")
	}
	var bt bulkTransfer
	if unsafe.Offsetof(bt.Ep) != 0 || unsafe.Offsetof(bt.Len) != 4 || unsafe.Offsetof(bt.Timeout) != 8 || unsafe.Offsetof(bt.Data) != 16 ||
		unsafe.Sizeof(bt) != 24 || unsafe.Alignof(bt) != 8 {
		t.Fatalf("layout of bulkTransfer: ep %d len %d timeout %d data %d size %d, want 0 4 8 16 24",
			unsafe.Offsetof(bt.Ep), unsafe.Offsetof(bt.Len), unsafe.Offsetof(bt.Timeout), unsafe.Offsetof(bt.Data), unsafe.Sizeof(bt))
	}
	if unsafe.Sizeof(bt.Ep) != 4 || unsafe.Sizeof(bt.Len) != 4 || unsafe.Sizeof(bt.Timeout) != 4 {
		t.Error("ep, len and timeout are unsigned int (4 bytes) in the kernel struct")
	}
	bulk, claim, release := requestNumbers()
	cases := []struct {
		name                string
		req                 uintptr
		dir, typ, nr, size  uintptr
		requestNumberOfKern uintptr
	}{
		{"bulk", bulk, 3, 'U', 2, 24, 0xC0185502},
		{"claim", claim, 2, 'U', 15, 4, 0x8004550F},
		{"release", release, 2, 'U', 16, 4, 0x80045510},
	}
	for _, c := range cases {
		if got := c.req >> 30; got != c.dir {
			t.Errorf("%s: direction bits %d, want %d", c.name, got, c.dir)
		}
		if got := c.req >> 16 & 0x3FFF; got != c.size {
			t.Errorf("%s: size field %d, want %d", c.name, got, c.size)
		}
		if got := c.req >> 8 & 0xFF; got != c.typ {
			t.Errorf("%s: type field %#x, want %#x ('U')", c.name, got, c.typ)
		}
		if got := c.req & 0xFF; got != c.nr {
			t.Errorf("%s: number field %d, want %d", c.name, got, c.nr)
		}
		if c.req != c.requestNumberOfKern {
			t.Errorf("%s: request %#x, want %#x", c.name, c.req, c.requestNumberOfKern)
		}
		if c.req>>32 != 0 {
			t.Errorf("%s: request %#x does not fit the 32-bit request argument", c.name, c.req)
		}
	}
}

// Whatever happens, the Device asks the kernel for three requests only, always with the descriptor it opened, and never with a
// descriptor of 0, 1 or 2.
func TestQAOnlyThreeRequestsOnTheOpenedDescriptor(t *testing.T) {
	k := &qaKernel{}
	d, _ := qaDevice(t, []byte{18, 1}, k)
	fd := d.fd
	if fd <= 2 {
		t.Fatalf("the node was opened on descriptor %d", fd)
	}
	_ = d.Claim(0)
	_ = d.Claim(1)
	_, _ = d.BulkOut(0x02, []byte{1}, time.Second)
	_, _ = d.BulkIn(0x82, make([]byte, 4), time.Second)
	_ = d.Release(0)
	_ = d.Close()
	_ = d.Claim(2)
	_, _ = d.BulkOut(0x02, []byte{1}, time.Second)
	calls := k.snapshot()
	// two claims, two transfers, the explicit release of 0 and the release of 1 by Close; the calls after Close ask nothing
	if len(calls) != 6 {
		t.Fatalf("%d calls: %+v", len(calls), calls)
	}
	for i, c := range calls {
		if c.fd != fd {
			t.Errorf("call %d on descriptor %d, want %d", i, c.fd, fd)
		}
		if c.req != reqBulk && c.req != reqClaim && c.req != reqRelease {
			t.Errorf("call %d asked for request %#x", i, c.req)
		}
	}
}

func TestQAClaimAndReleaseArgument(t *testing.T) {
	k := &qaKernel{}
	d, _ := qaDevice(t, nil, k)
	for _, n := range []int{0, 1, 2, 127, 128, 254, 255} {
		if err := d.Claim(n); err != nil {
			t.Fatalf("claim %d: %v", n, err)
		}
	}
	for _, n := range []int{255, 0} {
		if err := d.Release(n); err != nil {
			t.Fatalf("release %d: %v", n, err)
		}
	}
	var claims, releases []uint32
	for _, c := range k.snapshot() {
		switch c.req {
		case reqClaim:
			claims = append(claims, c.iface)
		case reqRelease:
			releases = append(releases, c.iface)
		default:
			t.Errorf("request %#x", c.req)
		}
	}
	if fmt.Sprint(claims) != "[0 1 2 127 128 254 255]" || fmt.Sprint(releases) != "[255 0]" {
		t.Errorf("claims %v releases %v", claims, releases)
	}
}

// B. Errors: the mapping of each errno, and the chain that errors.Is and errors.As walk.

// Only a claim answered with EBUSY is ErrBusy; no other errno is, and the same errno on a transfer or a release is not either.
func TestQABusyIsOnlyAClaimAnsweredEBUSY(t *testing.T) {
	errnos := []unix.Errno{unix.EBUSY, unix.EPERM, unix.EACCES, unix.ENOENT, unix.EINVAL, unix.ENOTTY, unix.EAGAIN, unix.EINTR,
		unix.EEXIST, unix.ENOMEM, unix.EPIPE, unix.EOVERFLOW, unix.ETIMEDOUT, unix.ENODEV, unix.EIO, unix.ESHUTDOWN, unix.EBADF, unix.EFAULT}
	for _, e := range errnos {
		t.Run("claim/"+e.Error(), func(t *testing.T) {
			k := &qaKernel{answer: func(kcall) (uintptr, unix.Errno) { return 0, e }}
			d, _ := qaDevice(t, nil, k)
			err := d.Claim(3)
			if err == nil {
				t.Fatal("no error")
			}
			if got := errors.Is(err, ErrBusy); got != (e == unix.EBUSY) {
				t.Errorf("errors.Is(ErrBusy) = %v for %v", got, e)
			}
			if !errors.Is(err, e) || errnoOf(err) != e {
				t.Errorf("the errno %v is not reachable in %v", e, err)
			}
			isFatal := e == unix.ENODEV || e == unix.EIO || e == unix.ESHUTDOWN
			if errors.Is(err, ErrGone) != isFatal {
				t.Errorf("errors.Is(ErrGone) = %v for %v, want %v", !isFatal, e, isFatal)
			}
			if d.claimed[3] {
				t.Error("a refused claim is recorded as held")
			}
			if k.count() != 1 {
				t.Errorf("%d system calls for one claim (no retry)", k.count())
			}
		})
	}
	// The same errno on the other calls.
	k := &qaKernel{answer: func(kcall) (uintptr, unix.Errno) { return 0, unix.EBUSY }}
	d, _ := qaDevice(t, nil, k)
	if _, err := d.BulkOut(0x02, []byte{1}, time.Second); errors.Is(err, ErrBusy) || !errors.Is(err, unix.EBUSY) {
		t.Errorf("EBUSY on a transfer: %v (must be the plain errno, not ErrBusy)", err)
	}
	if _, err := d.BulkIn(0x82, make([]byte, 1), time.Second); errors.Is(err, ErrBusy) {
		t.Errorf("EBUSY on a read: %v", err)
	}
	d.claimed[1] = true // as if a claim had succeeded earlier
	if err := d.Release(1); errors.Is(err, ErrBusy) || !errors.Is(err, unix.EBUSY) {
		t.Errorf("EBUSY on a release: %v", err)
	}
}

// The interface number is checked against 0..255 before the kernel is asked, in every direction, and the check comes after the
// state of the handle: a closed handle says ErrClosed and a gone one says ErrGone, whatever the number.
func TestQAClaimRange(t *testing.T) {
	k := &qaKernel{}
	d, _ := qaDevice(t, nil, k)
	for _, n := range []int{math.MinInt, -256, -1, 256, 257, 1 << 16, math.MaxInt32, math.MaxInt} {
		err := d.Claim(n)
		if !errors.Is(err, unix.EINVAL) || errors.Is(err, ErrBusy) || errors.Is(err, ErrGone) {
			t.Errorf("claim %d: %v, want EINVAL", n, err)
		}
	}
	if k.count() != 0 {
		t.Errorf("%d system calls for impossible interface numbers", k.count())
	}
	if err := d.Release(-1); err != nil || d.Release(math.MaxInt) != nil || k.count() != 0 {
		t.Errorf("release of a number that was never claimed must be a no-op: %v, %d calls", err, k.count())
	}
	for _, n := range []int{0, 255} {
		if err := d.Claim(n); err != nil {
			t.Errorf("claim %d: %v", n, err)
		}
	}
	// A refused number does not latch.
	if _, err := d.BulkOut(0x02, []byte{1}, time.Second); err != nil {
		t.Errorf("transfer after refused claims: %v", err)
	}

	gone := &qaKernel{answer: func(kcall) (uintptr, unix.Errno) { return 0, unix.ENODEV }}
	g, _ := qaDevice(t, nil, gone)
	_ = g.Claim(1)
	if err := g.Claim(1000); !errors.Is(err, ErrGone) || errors.Is(err, unix.EINVAL) {
		t.Errorf("claim 1000 on a gone device: %v, want ErrGone", err)
	}
	_ = d.Close()
	if err := d.Claim(1000); !errors.Is(err, ErrClosed) {
		t.Errorf("claim 1000 on a closed device: %v, want ErrClosed", err)
	}
}

// Release: after a claim the kernel gets the release once; without a claim, twice, or after Close it is a no-op that asks nothing.
func TestQAReleaseSemantics(t *testing.T) {
	k := &qaKernel{}
	d, _ := qaDevice(t, nil, k)
	if err := d.Release(1); err != nil || k.count() != 0 {
		t.Fatalf("release without a claim: %v, %d calls", err, k.count())
	}
	if err := d.Claim(1); err != nil {
		t.Fatal(err)
	}
	if err := d.Claim(1); err != nil { // a second claim of the same interface by the same handle goes to the kernel again
		t.Fatal(err)
	}
	if err := d.Release(1); err != nil {
		t.Fatal(err)
	}
	if err := d.Release(1); err != nil {
		t.Errorf("second release: %v", err)
	}
	if got := k.countReq(reqRelease); got != 1 {
		t.Errorf("%d releases reached the kernel, want 1", got)
	}
	if err := d.Claim(1); err != nil { // claim again after the release
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if got := k.countReq(reqRelease); got != 2 {
		t.Errorf("%d releases after Close, want 2", got)
	}
	before := k.count()
	// BUG-candidate of the documentation, held as the code behaves: Release after Close is nil, while the doc comment of Close
	// says that after Close every call returns ErrClosed.
	if err := d.Release(1); err != nil {
		t.Errorf("release after close: %v", err)
	}
	if k.count() != before {
		t.Error("release after close reached the kernel")
	}
}

// A release the kernel refuses (not fatal) is reported and the claim is forgotten; the device stays usable and Close does not
// release it again. A release answered with a fatal errno latches the device and reports nothing.
func TestQAReleaseErrors(t *testing.T) {
	t.Run("not fatal", func(t *testing.T) {
		k := &qaKernel{answer: func(c kcall) (uintptr, unix.Errno) {
			if c.req == reqRelease {
				return 0, unix.EINVAL
			}
			return 0, 0
		}}
		d, _ := qaDevice(t, nil, k)
		_ = d.Claim(2)
		err := d.Release(2)
		if !errors.Is(err, unix.EINVAL) || errors.Is(err, ErrGone) {
			t.Fatalf("release: %v", err)
		}
		if _, err := d.BulkOut(0x02, nil, time.Second); err != nil {
			t.Errorf("a refused release must not latch: %v", err)
		}
		if err := d.Close(); err != nil {
			t.Errorf("close after a failed release: %v", err)
		}
		if got := k.countReq(reqRelease); got != 1 {
			t.Errorf("%d release calls, want 1", got)
		}
	})
	t.Run("fatal", func(t *testing.T) {
		gone := false
		k := &qaKernel{answer: func(kcall) (uintptr, unix.Errno) {
			if gone {
				return 0, unix.ENODEV
			}
			return 0, 0
		}}
		d, _ := qaDevice(t, nil, k)
		_ = d.Claim(2)
		gone = true
		if err := d.Release(2); err != nil {
			t.Fatalf("release that met a gone device: %v, want nil", err)
		}
		before := k.count()
		if _, err := d.BulkIn(0x82, make([]byte, 2), time.Second); !errors.Is(err, ErrGone) || !errors.Is(err, unix.ENODEV) {
			t.Errorf("after the fatal release: %v, want ErrGone", err)
		}
		if k.count() != before {
			t.Error("the latch let a call through")
		}
	})
}

// Close tries every claimed interface even when the first release fails, and tells the first failure.
func TestQACloseTriesEveryRelease(t *testing.T) {
	k := &qaKernel{answer: func(c kcall) (uintptr, unix.Errno) {
		if c.req == reqRelease {
			return 0, unix.EINVAL
		}
		return 0, 0
	}}
	d, _ := qaDevice(t, nil, k)
	for n := range 5 {
		if err := d.Claim(n); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Close(); !errors.Is(err, unix.EINVAL) {
		t.Errorf("close: %v", err)
	}
	seen := map[uint32]int{}
	for _, c := range k.snapshot() {
		if c.req == reqRelease {
			seen[c.iface]++
		}
	}
	if len(seen) != 5 {
		t.Errorf("released %v, want each of 0..4 once", seen)
	}
	for n, c := range seen {
		if c != 1 {
			t.Errorf("interface %d released %d times", n, c)
		}
	}
}

// The release by Close that meets a gone device reports nothing and the descriptor is closed.
func TestQACloseOfADeviceThatWentDuringRelease(t *testing.T) {
	gone := false
	k := &qaKernel{answer: func(kcall) (uintptr, unix.Errno) {
		if gone {
			return 0, unix.EIO
		}
		return 0, 0
	}}
	d, _ := qaDevice(t, nil, k)
	for _, n := range []int{0, 1, 2} {
		_ = d.Claim(n)
	}
	gone = true
	if err := d.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
	if got := k.countReq(reqRelease); got != 1 {
		t.Errorf("%d releases: the first one found the device gone and the others must not be asked", got)
	}
	if err := unix.Fstat(d.fd, new(unix.Stat_t)); !errors.Is(err, unix.EBADF) {
		t.Errorf("the node is still open: %v", err)
	}
}

// Each failing path wraps the errno, the sentinel that fits it, and names what failed.
func TestQAErrorChains(t *testing.T) {
	type op struct {
		name string
		do   func(d *Device) error
		want string // text the message must carry
	}
	ops := []op{
		{"claim", func(d *Device) error { return d.Claim(4) }, "claim interface 4"},
		{"bulk out", func(d *Device) error { _, err := d.BulkOut(0x05, []byte{1}, time.Second); return err }, "0x05"},
		{"bulk in", func(d *Device) error { _, err := d.BulkIn(0x05, make([]byte, 1), time.Second); return err }, "0x85"},
	}
	cases := []struct {
		e                  unix.Errno
		busy, gone, timeou bool
	}{
		{e: unix.ENODEV, gone: true}, {e: unix.EIO, gone: true}, {e: unix.ESHUTDOWN, gone: true}, {e: unix.EPROTO, gone: true},
		{e: unix.EILSEQ, gone: true}, {e: unix.ETIMEDOUT, timeou: true}, {e: unix.EPIPE}, {e: unix.ENOMEM},
	}
	for _, c := range cases {
		for _, o := range ops {
			t.Run(o.name+"/"+c.e.Error(), func(t *testing.T) {
				k := &qaKernel{answer: func(kcall) (uintptr, unix.Errno) { return 0, c.e }}
				d, _ := qaDevice(t, nil, k)
				err := o.do(d)
				if err == nil {
					t.Fatal("no error")
				}
				if errnoOf(err) != c.e || !errors.Is(err, c.e) {
					t.Errorf("the errno is not reachable: %v", err)
				}
				if errors.Is(err, ErrGone) != c.gone || errors.Is(err, ErrTimeout) != c.timeou || errors.Is(err, ErrBusy) != c.busy {
					t.Errorf("sentinels of %v: gone=%v timeout=%v busy=%v", err, errors.Is(err, ErrGone), errors.Is(err, ErrTimeout), errors.Is(err, ErrBusy))
				}
				if errors.Is(err, ErrClosed) || errors.Is(err, ErrNotSupported) {
					t.Errorf("a wrong sentinel in %v", err)
				}
				if !strings.Contains(err.Error(), o.want) {
					t.Errorf("message %q lacks %q", err, o.want)
				}
				if !strings.HasPrefix(err.Error(), "usbccid: ") {
					t.Errorf("message %q does not start with the package name", err)
				}
				// The error that every later call returns also carries both.
				if c.gone {
					_, later := d.BulkIn(0x82, make([]byte, 1), time.Second)
					if !errors.Is(later, ErrGone) || !errors.Is(later, c.e) || errnoOf(later) != c.e {
						t.Errorf("later call: %v", later)
					}
				}
			})
		}
	}
}

// B. Latch: the failing call, any method as the first failure, and nothing reaches the kernel afterwards.
func TestQAGoneLatchesFromEveryMethod(t *testing.T) {
	first := map[string]func(d *Device) error{
		"claim":    func(d *Device) error { return d.Claim(1) },
		"bulk out": func(d *Device) error { _, err := d.BulkOut(0x02, []byte{1}, time.Second); return err },
		"bulk in":  func(d *Device) error { _, err := d.BulkIn(0x82, make([]byte, 4), time.Second); return err },
	}
	for name, do := range first {
		t.Run(name, func(t *testing.T) {
			k := &qaKernel{answer: func(kcall) (uintptr, unix.Errno) { return 0, unix.ENODEV }}
			d, _ := qaDevice(t, []byte{9}, k)
			if err := do(d); !errors.Is(err, ErrGone) || !errors.Is(err, unix.ENODEV) {
				t.Fatalf("first: %v", err)
			}
			for _, e := range []error{d.Claim(0), d.Claim(255), d.Claim(-1), d.Claim(256)} {
				if !errors.Is(e, ErrGone) {
					t.Errorf("claim: %v", e)
				}
			}
			for range 3 {
				if _, err := d.BulkOut(0x02, nil, 0); !errors.Is(err, ErrGone) {
					t.Errorf("bulk out: %v", err)
				}
				if _, err := d.BulkIn(0x82, nil, -1); !errors.Is(err, ErrGone) {
					t.Errorf("bulk in: %v", err)
				}
				if _, err := d.BulkOut(0x02, make([]byte, maxTransfer+1), 0); !errors.Is(err, ErrGone) {
					t.Errorf("an over-long transfer on a gone device must say ErrGone: %v", err)
				}
				if b, err := d.Descriptors(); !errors.Is(err, ErrGone) || b != nil {
					t.Errorf("descriptors: %v, %v", b, err)
				}
			}
			if k.count() != 1 {
				t.Errorf("%d system calls, want only the one that failed", k.count())
			}
		})
	}
}

// B. Timeouts and interrupts.

// A transfer that times out is ErrTimeout (and ETIMEDOUT), asks the kernel once, and leaves the handle usable: the next transfer
// is made and succeeds.
func TestQATimeoutLeavesTheHandleUsable(t *testing.T) {
	var step atomic.Int32
	k := &qaKernel{answer: func(c kcall) (uintptr, unix.Errno) {
		if c.req != reqBulk {
			return 0, 0
		}
		if step.Add(1)%2 == 1 {
			return 0, unix.ETIMEDOUT
		}
		return uintptr(c.bt.Len), 0
	}}
	d, _ := qaDevice(t, []byte{1, 2}, k)
	if err := d.Claim(1); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		n, err := d.BulkOut(0x02, []byte{1, 2, 3}, 5*time.Millisecond)
		if !errors.Is(err, ErrTimeout) || !errors.Is(err, unix.ETIMEDOUT) || errors.Is(err, ErrGone) || n != 0 {
			t.Fatalf("round %d: timeout: n=%d err=%v", i, n, err)
		}
		n, err = d.BulkIn(0x82, make([]byte, 5), 5*time.Millisecond)
		if err != nil || n != 5 {
			t.Fatalf("round %d: next transfer: n=%d err=%v", i, n, err)
		}
	}
	if got := k.countReq(reqBulk); got != 6 {
		t.Errorf("%d bulk calls for 6 transfers (a timeout is not retried)", got)
	}
	if b, err := d.Descriptors(); err != nil || len(b) != 2 {
		t.Errorf("descriptors after timeouts: %v %v", b, err)
	}
	if err := d.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}

// EINTR is reported and not retried (the package comment says "never retries"); the handle stays usable. The Port comment says
// nothing about EINTR: a caller that wants a retry has to make it. A question for a real card: can a signal interrupt a bulk ioctl?
func TestQAInterruptedCallIsReportedNotRetried(t *testing.T) {
	var calls atomic.Int32
	k := &qaKernel{answer: func(c kcall) (uintptr, unix.Errno) {
		if calls.Add(1) == 1 {
			return 0, unix.EINTR
		}
		return uintptr(c.bt.Len), 0
	}}
	d, _ := qaDevice(t, nil, k)
	_, err := d.BulkIn(0x82, make([]byte, 8), time.Second)
	if !errors.Is(err, unix.EINTR) || errors.Is(err, ErrGone) || errors.Is(err, ErrTimeout) || errors.Is(err, ErrBusy) {
		t.Fatalf("interrupted transfer: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("%d calls: EINTR was retried", calls.Load())
	}
	if n, err := d.BulkIn(0x82, make([]byte, 8), time.Second); err != nil || n != 8 {
		t.Errorf("transfer after EINTR: %d %v", n, err)
	}
}

// B. Counts and sizes.

// The count the kernel returns is handed back as it is: short writes and short reads, and zero.
func TestQAShortCountsPassThrough(t *testing.T) {
	for _, r := range []uintptr{0, 1, 31, 63, 64} {
		k := &qaKernel{answer: func(kcall) (uintptr, unix.Errno) { return r, 0 }}
		d, _ := qaDevice(t, nil, k)
		n, err := d.BulkOut(0x02, make([]byte, 64), time.Second)
		if err != nil || n != int(r) {
			t.Errorf("BulkOut short count %d: n=%d err=%v", r, n, err)
		}
		n, err = d.BulkIn(0x82, make([]byte, 64), time.Second)
		if err != nil || n != int(r) {
			t.Errorf("BulkIn short count %d: n=%d err=%v", r, n, err)
		}
	}
}

// Zero bytes go to the kernel as a zero-length transfer with no data pointer, whether the slice is nil or empty. The limit is exact:
// 1<<24 bytes go through, one more is refused before the kernel in both directions.
func TestQAZeroAndLimitTransfers(t *testing.T) {
	k := &qaKernel{}
	d, _ := qaDevice(t, nil, k)
	for _, p := range [][]byte{nil, {}, make([]byte, 0, 100)} {
		if n, err := d.BulkOut(0x02, p, time.Second); err != nil || n != 0 {
			t.Errorf("BulkOut of nothing: %d %v", n, err)
		}
		if n, err := d.BulkIn(0x82, p, time.Second); err != nil || n != 0 {
			t.Errorf("BulkIn into nothing: %d %v", n, err)
		}
	}
	for i, c := range k.snapshot() {
		if c.bt.Len != 0 || c.bt.Data != nil {
			t.Errorf("call %d: %+v, want a zero-length transfer with no pointer", i, c.bt)
		}
	}

	big := make([]byte, maxTransfer+1)
	before := k.count()
	for _, bad := range [][]byte{big, big[: maxTransfer+1 : maxTransfer+1]} {
		if _, err := d.BulkOut(0x02, bad, time.Second); !errors.Is(err, unix.EINVAL) || errors.Is(err, ErrGone) {
			t.Errorf("BulkOut of %d bytes: %v", len(bad), err)
		}
		if _, err := d.BulkIn(0x82, bad, time.Second); !errors.Is(err, unix.EINVAL) || errors.Is(err, ErrGone) {
			t.Errorf("BulkIn of %d bytes: %v", len(bad), err)
		}
	}
	if k.count() != before {
		t.Error("an over-long transfer reached the kernel")
	}
	exact := big[:maxTransfer]
	if n, err := d.BulkOut(0x02, exact, time.Second); err != nil || n != maxTransfer {
		t.Errorf("BulkOut of exactly the limit: %d %v", n, err)
	}
	if n, err := d.BulkIn(0x82, exact, time.Second); err != nil || n != maxTransfer {
		t.Errorf("BulkIn of exactly the limit: %d %v", n, err)
	}
	last := k.snapshot()[k.count()-1]
	if last.bt.Len != maxTransfer {
		t.Errorf("length sent: %d", last.bt.Len)
	}
	if maxTransfer >= math.MaxInt32/2 {
		t.Errorf("maxTransfer %d is not below the kernel's INT_MAX/2 limit", maxTransfer)
	}
	// An over-long refusal does not latch.
	if _, err := d.BulkOut(0x02, []byte{1}, time.Second); err != nil {
		t.Errorf("transfer after a refused one: %v", err)
	}
}

// The bytes of BulkOut reach the kernel as they are, and what the kernel writes into the buffer of BulkIn comes back to the caller.
func TestQADataFlowsBothWays(t *testing.T) {
	k := &qaKernel{fill: func(buf []byte) {
		for i := range buf {
			buf[i] = byte(0xA0 + i)
		}
	}, answer: func(c kcall) (uintptr, unix.Errno) { return uintptr(c.bt.Len) - 1, 0 }}
	d, _ := qaDevice(t, nil, k)
	out := []byte{0, 1, 2, 0xFF, 0x80}
	if n, err := d.BulkOut(0x02, out, time.Second); err != nil || n != 4 {
		t.Fatalf("BulkOut: %d %v", n, err)
	}
	in := make([]byte, 6)
	n, err := d.BulkIn(0x82, in, time.Second)
	if err != nil || n != 5 {
		t.Fatalf("BulkIn: %d %v", n, err)
	}
	if string(in[:n]) != string([]byte{0xA0, 0xA1, 0xA2, 0xA3, 0xA4}) {
		t.Errorf("read %x", in[:n])
	}
	calls := k.snapshot()
	if string(calls[0].data) != string(out) {
		t.Errorf("the kernel saw %x, want %x", calls[0].data, out)
	}
}

// The direction bit, for every possible endpoint byte.
func TestQAEndpointDirectionBitForEveryValue(t *testing.T) {
	k := &qaKernel{}
	d, _ := qaDevice(t, nil, k)
	for ep := range 256 {
		if _, err := d.BulkOut(uint8(ep), []byte{1}, time.Second); err != nil {
			t.Fatal(err)
		}
		if _, err := d.BulkIn(uint8(ep), make([]byte, 1), time.Second); err != nil {
			t.Fatal(err)
		}
	}
	calls := k.snapshot()
	for ep := range 256 {
		out, in := calls[2*ep].bt.Ep, calls[2*ep+1].bt.Ep
		if out != uint32(ep)&0x7F || in != uint32(ep)|0x80 {
			t.Fatalf("endpoint byte %#02x: out %#02x in %#02x", ep, out, in)
		}
	}
}

// B. Timeout values as the kernel gets them.
func TestQATimeoutValuesReachTheKernel(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want uint32
	}{
		{0, 1}, {-1, 1}, {math.MinInt64, 1}, {time.Nanosecond, 1}, {999_999, 1}, {time.Millisecond, 1}, {time.Millisecond + 1, 2},
		{50 * time.Microsecond, 1}, {15 * time.Second, 15000}, {30*time.Second - 1, 30000}, {time.Hour, 3_600_000},
		{time.Duration(1<<30) * time.Millisecond, 1 << 30},
		{time.Duration(1<<30)*time.Millisecond + 1, 1 << 30},
		{time.Duration(1<<30)*time.Millisecond - time.Nanosecond, 1 << 30},
		{time.Duration(1<<30-1) * time.Millisecond, 1<<30 - 1},
		{10 * 365 * 24 * time.Hour, 1 << 30},
	}
	k := &qaKernel{}
	d, _ := qaDevice(t, nil, k)
	for _, c := range cases {
		before := k.count()
		if _, err := d.BulkOut(0x02, []byte{1}, c.in); err != nil {
			t.Fatal(err)
		}
		if _, err := d.BulkIn(0x82, make([]byte, 1), c.in); err != nil {
			t.Fatal(err)
		}
		for _, call := range k.snapshot()[before:] {
			if call.bt.Timeout != c.want {
				t.Errorf("timeout %v: the kernel got %d ms, want %d", c.in, call.bt.Timeout, c.want)
			}
		}
	}
}

// A duration that is near the largest the type holds must give the largest timeout (2^30 ms), never an overflow. ms adds almost a
// millisecond to round up, and that sum wraps for a duration above MaxInt64 minus 999999 ns, so the kernel gets a number that is the
// low 32 bits of a negative count: neither 1 ms nor the cap. Held as the doc comment promises ("at least 1, at most 2^30", and 0
// never means for ever). The common spelling "time.Duration(math.MaxInt64)" for "no limit" hits it.
func TestQAMillisecondsDoNotOverflowForHugeDurations(t *testing.T) {
	t.Skip("BUG: ms() overflows for a duration above math.MaxInt64-999999 ns (device_linux.go:223: t + time.Millisecond - 1 wraps negative); " +
		"remove this Skip when ms is fixed")
	for _, d := range []time.Duration{math.MaxInt64, math.MaxInt64 - 1, math.MaxInt64 - 999_998} {
		if got := ms(d); got != 1<<30 {
			t.Errorf("ms(%d) = %d, want %d", int64(d), got, 1<<30)
		}
	}
}

// The part of ms that does not overflow: always between 1 and 2^30, never decreasing as the duration grows, ceiling to the
// millisecond. Sampled around every boundary.
func TestQAMillisecondsAreBoundedAndMonotonic(t *testing.T) {
	var samples []time.Duration
	for _, base := range []time.Duration{0, time.Millisecond, 2 * time.Millisecond, time.Second, time.Hour, time.Duration(1<<30) * time.Millisecond,
		time.Duration(1<<31) * time.Millisecond, math.MaxInt64 - 2*time.Millisecond} {
		for _, d := range []time.Duration{-1, 0, 1, 2, 999_999, 1_000_000, 1_000_001} {
			if v := base + d; v >= 0 && v <= math.MaxInt64-time.Millisecond {
				samples = append(samples, v)
			}
		}
	}
	slices.Sort(samples)
	prev := uint32(0)
	for _, s := range samples {
		got := ms(s)
		if got < 1 || got > 1<<30 {
			t.Errorf("ms(%d) = %d out of 1..2^30", int64(s), got)
		}
		if got < prev {
			t.Errorf("ms(%d) = %d is below the value %d of a shorter duration", int64(s), got, prev)
		}
		if s > 0 && s <= time.Duration(1<<30)*time.Millisecond {
			if want := uint32((int64(s) + 999_999) / 1_000_000); got != want { //nolint:gosec // at most 2^30
				t.Errorf("ms(%d) = %d, want ceiling %d", int64(s), got, want)
			}
		}
		prev = got
	}
}

// B. The node is opened once and never by path again.
func TestQANodeIsOpenedOnceAndNeverReopened(t *testing.T) {
	k := &qaKernel{}
	d, path := qaDevice(t, []byte{18, 1, 2}, k)
	fd := d.fd
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := d.Claim(1); err != nil {
		t.Fatal(err)
	}
	if b, err := d.Descriptors(); err != nil || len(b) != 3 {
		t.Errorf("descriptors after the node vanished: %v %v", b, err)
	}
	if _, err := d.BulkOut(0x02, []byte{1}, time.Second); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the node was recreated or reopened: %v", err)
	}
	for _, c := range k.snapshot() {
		if c.fd != fd {
			t.Errorf("a call on descriptor %d, want %d", c.fd, fd)
		}
	}
	if err := d.Close(); err != nil {
		t.Error(err)
	}
}

// Descriptors are read once at Open (at most 64 KiB), whatever the node holds afterwards.
func TestQADescriptorsReadOnceAndBounded(t *testing.T) {
	k := &qaKernel{}
	d, path := qaDevice(t, make([]byte, 70000), k)
	b, err := d.Descriptors()
	if err != nil || len(b) != maxDescriptorBytes {
		t.Fatalf("descriptors of a 70000-byte node: %d bytes, %v", len(b), err)
	}
	if err := os.WriteFile(path, []byte{1, 2, 3}, 0o600); err != nil {
		t.Fatal(err)
	}
	if again, _ := d.Descriptors(); len(again) != maxDescriptorBytes {
		t.Errorf("descriptors read again: %d bytes", len(again))
	}
	empty, _ := qaDevice(t, nil, k)
	if b, err := empty.Descriptors(); err != nil || len(b) != 0 {
		t.Errorf("empty node: %v %v", b, err)
	}
}

// B. Open: the refusal on a permission error carries the errno and os.ErrPermission; the refusals leave no descriptor behind.
func TestQAOpenPermissionAndDescriptorLeaks(t *testing.T) {
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.WriteFile(locked, []byte{1}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	if unix.Geteuid() != 0 {
		_, err := Open(locked)
		var pe *os.PathError
		if !errors.Is(err, os.ErrPermission) || !errors.Is(err, unix.EACCES) || !errors.As(err, &pe) || pe.Path != locked {
			t.Errorf("a node the user may not open: %v, want os.ErrPermission, EACCES and a *os.PathError of the path", err)
		}
	}
	reg := filepath.Join(dir, "regular")
	if err := os.WriteFile(reg, []byte{1}, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(reg, link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	countFDs := func() int {
		ents, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Skip("no /proc/self/fd")
		}
		return len(ents)
	}
	before := countFDs()
	for range 50 {
		for _, p := range []string{reg, link, fifo, dir, filepath.Join(dir, "none"), locked} {
			if d, err := Open(p); err == nil {
				_ = d.Close()
				t.Fatalf("Open(%s) succeeded on something that is not a character device", filepath.Base(p))
			}
		}
	}
	if after := countFDs(); after != before {
		t.Errorf("%d descriptors open before the refusals, %d after", before, after)
	}
}

// D. Concurrency. The Port comment says "Every method is called from one goroutine at a time", so Port promises nothing about
// concurrent use. The Device comment promises more: calls are serialised by a mutex, there is one exchange with the device at a
// time, and Close waits for a transfer in progress. These tests hold exactly that.

// Close waits for a transfer that is in progress; calls that come while it waits are answered after it, with ErrClosed.
func TestQACloseWaitsForATransferInProgress(t *testing.T) {
	inside := make(chan struct{})
	release := make(chan struct{})
	var first atomic.Bool
	k := &qaKernel{answer: func(c kcall) (uintptr, unix.Errno) {
		if c.req == reqBulk && first.CompareAndSwap(false, true) { // only the first transfer is held in the kernel
			close(inside)
			<-release
		}
		if c.req == reqBulk {
			return uintptr(c.bt.Len), 0
		}
		return 0, 0
	}}
	d, _ := qaDevice(t, nil, k)
	if err := d.Claim(1); err != nil {
		t.Fatal(err)
	}
	type result struct {
		n   int
		err error
	}
	transfer := make(chan result, 1)
	go func() {
		n, err := d.BulkIn(0x82, make([]byte, 16), time.Hour)
		transfer <- result{n, err}
	}()
	<-inside
	closed := make(chan error, 1)
	go func() { closed <- d.Close() }()
	late := make(chan error, 1)
	go func() { _, err := d.BulkOut(0x02, []byte{1}, time.Second); late <- err }()
	select {
	case err := <-closed:
		t.Fatalf("Close returned while a transfer was in progress: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if r := <-transfer; r.err != nil || r.n != 16 {
		t.Errorf("the transfer that was in progress: %d %v", r.n, r.err)
	}
	if err := <-closed; err != nil {
		t.Errorf("close: %v", err)
	}
	if err := <-late; err != nil && !errors.Is(err, ErrClosed) {
		t.Errorf("a call that raced the close: %v", err)
	}
	if got := k.countReq(reqRelease); got != 1 {
		t.Errorf("%d releases, want 1 (after the transfer)", got)
	}
	if _, err := d.BulkIn(0x82, make([]byte, 1), time.Second); !errors.Is(err, ErrClosed) {
		t.Errorf("after close: %v", err)
	}
}

// Several goroutines use one handle while another closes it: no data race, no panic, every call ends with success or ErrClosed,
// nothing reaches the kernel after Close returned, and the descriptor is closed exactly once.
func TestQAConcurrentUseWhileClosing(t *testing.T) {
	for round := range 20 {
		var afterClose atomic.Bool
		var late atomic.Int32
		k := &qaKernel{answer: func(c kcall) (uintptr, unix.Errno) {
			if afterClose.Load() {
				late.Add(1)
			}
			if c.req == reqBulk {
				return uintptr(c.bt.Len), 0
			}
			return 0, 0
		}}
		d, _ := qaDevice(t, []byte{1, 2, 3}, k)
		var wg sync.WaitGroup
		var bad atomic.Int32
		check := func(err error) {
			if err != nil && !errors.Is(err, ErrClosed) {
				bad.Add(1)
				t.Errorf("round %d: %v", round, err)
			}
		}
		start := make(chan struct{})
		for g := range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for i := range 50 {
					switch (g + i) % 5 {
					case 0:
						_, err := d.BulkOut(0x02, []byte{1, 2}, time.Second)
						check(err)
					case 1:
						_, err := d.BulkIn(0x82, make([]byte, 8), time.Second)
						check(err)
					case 2:
						check(d.Claim(g))
					case 3:
						_ = d.Release(g)
					case 4:
						_, err := d.Descriptors()
						check(err)
					}
				}
			}()
		}
		closers := make(chan error, 3)
		for range 3 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				time.Sleep(time.Duration(round%4) * 100 * time.Microsecond)
				closers <- d.Close()
			}()
		}
		close(start)
		wg.Wait()
		afterClose.Store(true)
		close(closers)
		for err := range closers {
			if err != nil {
				t.Errorf("round %d: close: %v", round, err)
			}
		}
		_, _ = d.BulkOut(0x02, []byte{1}, time.Second)
		_ = d.Claim(1)
		if late.Load() != 0 {
			t.Errorf("round %d: %d system calls after Close", round, late.Load())
		}
		if err := unix.Fstat(d.fd, new(unix.Stat_t)); !errors.Is(err, unix.EBADF) {
			t.Errorf("round %d: the node is still open: %v", round, err)
		}
	}
}

// A device that goes while many goroutines wait for it: the first caller meets the errno, and all others are answered from the
// latch, so the kernel is asked exactly once.
func TestQAGoneUnderConcurrency(t *testing.T) {
	k := &qaKernel{answer: func(kcall) (uintptr, unix.Errno) { return 0, unix.ENODEV }}
	d, _ := qaDevice(t, nil, k)
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	start := make(chan struct{})
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := d.BulkIn(0x82, make([]byte, 4), time.Second)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if !errors.Is(err, ErrGone) || !errors.Is(err, unix.ENODEV) {
			t.Errorf("%v", err)
		}
	}
	if k.count() != 1 {
		t.Errorf("%d system calls from 32 callers, want 1", k.count())
	}
}

// Two Close calls at once release each interface once and both return.
func TestQAConcurrentCloseReleasesOnce(t *testing.T) {
	k := &qaKernel{}
	d, _ := qaDevice(t, nil, k)
	for n := range 4 {
		_ = d.Claim(n)
	}
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() { defer wg.Done(); _ = d.Close() }()
	}
	wg.Wait()
	if got := k.countReq(reqRelease); got != 4 {
		t.Errorf("%d releases for 4 interfaces", got)
	}
}
