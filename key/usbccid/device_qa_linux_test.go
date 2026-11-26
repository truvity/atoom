//go:build linux && !android && (amd64 || arm64)

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

// qaDevice opens a Device over a regular file that holds desc (the made-up composite device when desc is nil) and gives it the script k. The file is returned for the tests that
// change it. Close is called at the end of the test.
func qaDevice(t *testing.T, desc []byte, k *qaKernel) (*Device, string) {
	t.Helper()
	if desc == nil {
		desc = qaDefaultDesc()
	}
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

// qaClaim claims the CCID interface of the made-up device.
func qaClaim(t *testing.T, d *Device) {
	t.Helper()
	if err := d.Claim(qaCCID); err != nil {
		t.Fatal(err)
	}
}

// qaLast is the last call of the script, or a failure of the test (never a panic).
func (k *qaKernel) last(t *testing.T) kcall {
	t.Helper()
	c := k.snapshot()
	if len(c) == 0 {
		t.Fatal("the kernel was never asked")
	}
	return c[len(c)-1]
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
	d, _ := qaDevice(t, nil, k)
	fd := d.fd
	if fd <= 2 {
		t.Fatalf("the node was opened on descriptor %d", fd)
	}
	_ = d.Claim(qaCCID)
	_ = d.Claim(qaCCID2)
	_, _ = d.BulkOut(qaOut, []byte{1}, time.Second)
	_, _ = d.BulkIn(qaIn, make([]byte, 4), time.Second)
	_ = d.Release(qaCCID)
	_ = d.Close()
	_ = d.Claim(qaCCID)
	_, _ = d.BulkOut(qaOut, []byte{1}, time.Second)
	calls := k.snapshot()
	// two claims, two transfers, the explicit release of 1 and the release of 3 by Close; the calls after Close ask nothing
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
	for _, n := range []int{qaCCID, qaCCID2} {
		if err := d.Claim(n); err != nil {
			t.Fatalf("claim %d: %v", n, err)
		}
	}
	for _, n := range []int{qaCCID2, qaCCID} {
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
	if fmt.Sprint(claims) != "[1 3]" || fmt.Sprint(releases) != "[3 1]" {
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
			err := d.Claim(qaCCID2)
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
			if d.claimed[qaCCID2] {
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
	d.claimed[qaCCID] = true // as if a claim had succeeded earlier
	if _, err := d.BulkOut(qaOut, []byte{1}, time.Second); errors.Is(err, ErrBusy) || !errors.Is(err, unix.EBUSY) {
		t.Errorf("EBUSY on a transfer: %v (must be the plain errno, not ErrBusy)", err)
	}
	if _, err := d.BulkIn(qaIn, make([]byte, 1), time.Second); errors.Is(err, ErrBusy) {
		t.Errorf("EBUSY on a read: %v", err)
	}
	if err := d.Release(qaCCID); errors.Is(err, ErrBusy) || !errors.Is(err, unix.EBUSY) {
		t.Errorf("EBUSY on a release: %v", err)
	}
}

// An interface number that is not a CCID interface of the descriptors is refused before the kernel is asked, in every direction, and
// the check comes after the state of the handle: a closed handle says ErrClosed and a gone one says ErrGone, whatever the number.
func TestQAClaimRange(t *testing.T) {
	k := &qaKernel{}
	d, _ := qaDevice(t, nil, k)
	for _, n := range []int{math.MinInt, -256, -1, 0, 2, 4, 255, 256, 257, 1 << 16, math.MaxInt32, math.MaxInt} {
		err := d.Claim(n)
		if !errors.Is(err, ErrNoSuchInterface) || errors.Is(err, ErrBusy) || errors.Is(err, ErrGone) || errors.Is(err, unix.EINVAL) {
			t.Errorf("claim %d: %v, want ErrNoSuchInterface", n, err)
		}
	}
	if k.count() != 0 {
		t.Errorf("%d system calls for impossible interface numbers", k.count())
	}
	if err := d.Release(-1); err != nil || d.Release(math.MaxInt) != nil || k.count() != 0 {
		t.Errorf("release of a number that was never claimed must be a no-op: %v, %d calls", err, k.count())
	}
	for _, n := range []int{qaCCID, qaCCID2} {
		if err := d.Claim(n); err != nil {
			t.Errorf("claim %d: %v", n, err)
		}
	}
	// A refused number does not latch.
	if _, err := d.BulkOut(qaOut, []byte{1}, time.Second); err != nil {
		t.Errorf("transfer after refused claims: %v", err)
	}

	gone := &qaKernel{answer: func(kcall) (uintptr, unix.Errno) { return 0, unix.ENODEV }}
	g, _ := qaDevice(t, nil, gone)
	_ = g.Claim(qaCCID)
	if err := g.Claim(1000); !errors.Is(err, ErrGone) || errors.Is(err, ErrNoSuchInterface) {
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
	// After Close, Release returns nil (and asks nothing), as the doc comments say.
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
	// A release that fails and is not fatal keeps the claim: the kernel may still hold it. A second Release asks the kernel again, Close
	// releases it, a bulk call is still allowed while the claim is kept, and nothing is latched.
	t.Run("not fatal keeps the claim", func(t *testing.T) {
		var fail atomic.Bool
		fail.Store(true)
		k := &qaKernel{answer: func(c kcall) (uintptr, unix.Errno) {
			if c.req == reqRelease && fail.Load() {
				return 0, unix.EINVAL
			}
			return uintptr(c.bt.Len), 0
		}}
		d, _ := qaDevice(t, nil, k)
		qaClaim(t, d)
		for i := 1; i <= 2; i++ {
			err := d.Release(qaCCID)
			if !errors.Is(err, unix.EINVAL) || errors.Is(err, ErrGone) {
				t.Fatalf("release %d: %v", i, err)
			}
			if got := k.countReq(reqRelease); got != i {
				t.Fatalf("%d release requests after %d calls: the second call must ask the kernel again", got, i)
			}
		}
		if _, err := d.Descriptors(); err != nil {
			t.Errorf("a refused release must not latch: %v", err)
		}
		if _, err := d.BulkOut(qaOut, nil, time.Second); err != nil {
			t.Errorf("a bulk call while the claim is kept: %v", err)
		}
		fail.Store(false) // the kernel lets it go
		if err := d.Release(qaCCID); err != nil {
			t.Fatalf("the release that succeeds: %v", err)
		}
		if _, err := d.BulkOut(qaOut, nil, time.Second); !errors.Is(err, ErrNotClaimed) {
			t.Errorf("after the release that succeeded: %v", err)
		}
		if err := d.Release(qaCCID); err != nil || k.countReq(reqRelease) != 3 {
			t.Errorf("a release of what is gone: %v, %d requests", err, k.countReq(reqRelease))
		}
		if err := d.Close(); err != nil || k.countReq(reqRelease) != 3 {
			t.Errorf("close: %v, %d release requests", err, k.countReq(reqRelease))
		}
	})
	t.Run("Close releases a claim that a failed release kept", func(t *testing.T) {
		var fail atomic.Bool
		fail.Store(true)
		k := &qaKernel{answer: func(c kcall) (uintptr, unix.Errno) {
			if c.req == reqRelease && fail.Load() {
				return 0, unix.EINVAL
			}
			return 0, 0
		}}
		d, _ := qaDevice(t, nil, k)
		qaClaim(t, d)
		if err := d.Release(qaCCID); !errors.Is(err, unix.EINVAL) {
			t.Fatal(err)
		}
		fail.Store(false)
		if err := d.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
		if got := k.countReq(reqRelease); got != 2 {
			t.Errorf("%d release requests, want 2 (the failed one, and Close)", got)
		}
	})
	t.Run("fatal forgets the claim", func(t *testing.T) {
		gone := false
		k := &qaKernel{answer: func(kcall) (uintptr, unix.Errno) {
			if gone {
				return 0, unix.ENODEV
			}
			return 0, 0
		}}
		d, _ := qaDevice(t, nil, k)
		qaClaim(t, d)
		gone = true
		if err := d.Release(qaCCID); err != nil {
			t.Fatalf("release that met a gone device: %v, want nil", err)
		}
		if d.claimed[qaCCID] {
			t.Error("the claim is kept after a fatal release")
		}
		before := k.count()
		if _, err := d.BulkIn(qaIn, make([]byte, 2), time.Second); !errors.Is(err, ErrGone) || !errors.Is(err, unix.ENODEV) {
			t.Errorf("after the fatal release: %v, want ErrGone", err)
		}
		if err := d.Release(qaCCID); err != nil || d.Close() != nil || k.count() != before {
			t.Errorf("release and close after the latch asked the kernel (%d calls) or failed", k.count()-before)
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
	for _, n := range []int{qaCCID, qaCCID2} {
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
	if len(seen) != 2 {
		t.Errorf("released %v, want each of 1 and 3 once", seen)
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
	for _, n := range []int{qaCCID, qaCCID2} {
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
		{"claim", func(d *Device) error { return d.Claim(qaCCID) }, "claim interface 1"},
		{"bulk out", func(d *Device) error { _, err := d.BulkOut(qaOut, []byte{1}, time.Second); return err }, "0x02"},
		{"bulk in", func(d *Device) error { _, err := d.BulkIn(qaIn, make([]byte, 1), time.Second); return err }, "0x82"},
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
				d.claimed[qaCCID] = true // as if a claim had succeeded earlier: the transfers are then asked of the kernel
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
					_, later := d.BulkIn(qaIn, make([]byte, 1), time.Second)
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
		"claim":    func(d *Device) error { return d.Claim(qaCCID) },
		"bulk out": func(d *Device) error { _, err := d.BulkOut(qaOut, []byte{1}, time.Second); return err },
		"bulk in":  func(d *Device) error { _, err := d.BulkIn(qaIn, make([]byte, 4), time.Second); return err },
	}
	for name, do := range first {
		t.Run(name, func(t *testing.T) {
			k := &qaKernel{answer: func(kcall) (uintptr, unix.Errno) { return 0, unix.ENODEV }}
			d, _ := qaDevice(t, nil, k)
			d.claimed[qaCCID] = true // as if a claim had succeeded earlier
			if err := do(d); !errors.Is(err, ErrGone) || !errors.Is(err, unix.ENODEV) {
				t.Fatalf("first: %v", err)
			}
			for _, e := range []error{d.Claim(0), d.Claim(255), d.Claim(-1), d.Claim(256)} {
				if !errors.Is(e, ErrGone) {
					t.Errorf("claim: %v", e)
				}
			}
			for range 3 {
				if _, err := d.BulkOut(qaOut, nil, 0); !errors.Is(err, ErrGone) {
					t.Errorf("bulk out: %v", err)
				}
				if _, err := d.BulkIn(qaIn, nil, -1); !errors.Is(err, ErrGone) {
					t.Errorf("bulk in: %v", err)
				}
				if _, err := d.BulkOut(qaOut, make([]byte, maxTransfer+1), 0); !errors.Is(err, ErrGone) {
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

// A transfer that times out is ErrTimeout (and ETIMEDOUT) and asks the kernel once. The handle can still be called: the next transfer
// is made and, here, succeeds. That does not mean the session is sound: the stream of the endpoint is out of step, a late answer may
// arrive on the next read, and the layer above must treat its session as lost and close and open again (this layer never aborts or
// resynchronises for it; see TestQAAfterATimeoutTheNextReadMayReturnALateAnswer).
func TestQAAfterATimeoutTheHandleCanBeCalledButTheSessionIsLost(t *testing.T) {
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
	d, _ := qaDevice(t, nil, k)
	qaClaim(t, d)
	for i := range 3 {
		n, err := d.BulkOut(qaOut, []byte{1, 2, 3}, 5*time.Millisecond)
		if !errors.Is(err, ErrTimeout) || !errors.Is(err, unix.ETIMEDOUT) || errors.Is(err, ErrGone) || n != 0 {
			t.Fatalf("round %d: timeout: n=%d err=%v", i, n, err)
		}
		n, err = d.BulkIn(qaIn, make([]byte, 5), 5*time.Millisecond)
		if err != nil || n != 5 {
			t.Fatalf("round %d: next transfer: n=%d err=%v", i, n, err)
		}
	}
	if got := k.countReq(reqBulk); got != 6 {
		t.Errorf("%d bulk calls for 6 transfers (a timeout is not retried)", got)
	}
	if b, err := d.Descriptors(); err != nil || len(b) != len(qaDefaultDesc()) {
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
	d.claimed[qaCCID] = true
	_, err := d.BulkIn(qaIn, make([]byte, 8), time.Second)
	if !errors.Is(err, unix.EINTR) || errors.Is(err, ErrGone) || errors.Is(err, ErrTimeout) || errors.Is(err, ErrBusy) {
		t.Fatalf("interrupted transfer: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("%d calls: EINTR was retried", calls.Load())
	}
	if n, err := d.BulkIn(qaIn, make([]byte, 8), time.Second); err != nil || n != 8 {
		t.Errorf("transfer after EINTR: %d %v", n, err)
	}
}

// B. Counts and sizes.

// The count the kernel returns is handed back as it is: short writes and short reads, and zero.
func TestQAShortCountsPassThrough(t *testing.T) {
	for _, r := range []uintptr{0, 1, 31, 63, 64} {
		k := &qaKernel{answer: func(kcall) (uintptr, unix.Errno) { return r, 0 }}
		d, _ := qaDevice(t, nil, k)
		qaClaim(t, d)
		n, err := d.BulkOut(qaOut, make([]byte, 64), time.Second)
		if err != nil || n != int(r) {
			t.Errorf("BulkOut short count %d: n=%d err=%v", r, n, err)
		}
		n, err = d.BulkIn(qaIn, make([]byte, 64), time.Second)
		if err != nil || n != int(r) {
			t.Errorf("BulkIn short count %d: n=%d err=%v", r, n, err)
		}
	}
}

// An empty write goes to the kernel as a zero-length transfer with no data pointer, whether the slice is nil or empty (a zero-length
// packet is a thing a bulk OUT endpoint takes). An empty read is refused before the kernel is asked. The limit is exact: 64 KiB go
// through in both directions, one more is refused before the kernel.
func TestQAZeroAndLimitTransfers(t *testing.T) {
	k := &qaKernel{}
	d, _ := qaDevice(t, nil, k)
	qaClaim(t, d)
	for _, p := range [][]byte{nil, {}, make([]byte, 0, 100)} {
		if n, err := d.BulkOut(qaOut, p, time.Second); err != nil || n != 0 {
			t.Errorf("BulkOut of nothing: %d %v", n, err)
		}
	}
	for _, c := range k.snapshot()[1:] { // the first call is the claim
		if c.bt.Len != 0 || c.bt.Data != nil {
			t.Errorf("%+v, want a zero-length transfer with no pointer", c.bt)
		}
	}
	before := k.count()
	for _, p := range [][]byte{nil, {}, make([]byte, 0, 100)} {
		if n, err := d.BulkIn(qaIn, p, time.Second); !errors.Is(err, ErrBadLength) || n != 0 || errors.Is(err, ErrGone) {
			t.Errorf("BulkIn into nothing: %d %v", n, err)
		}
	}
	big := make([]byte, maxTransfer+1)
	for _, bad := range [][]byte{big, big[: maxTransfer+1 : maxTransfer+1]} {
		if _, err := d.BulkOut(qaOut, bad, time.Second); !errors.Is(err, ErrBadLength) || errors.Is(err, ErrGone) {
			t.Errorf("BulkOut of %d bytes: %v", len(bad), err)
		}
		if _, err := d.BulkIn(qaIn, bad, time.Second); !errors.Is(err, ErrBadLength) || errors.Is(err, ErrGone) {
			t.Errorf("BulkIn of %d bytes: %v", len(bad), err)
		}
	}
	if k.count() != before {
		t.Error("a refused transfer reached the kernel")
	}
	exact := big[:maxTransfer]
	if maxTransfer != 64*1024 {
		t.Errorf("the limit is %d, the documentation says 64 KiB", maxTransfer)
	}
	if n, err := d.BulkOut(qaOut, exact, time.Second); err != nil || n != maxTransfer {
		t.Errorf("BulkOut of exactly the limit: %d %v", n, err)
	}
	if got := k.last(t).bt.Len; got != maxTransfer {
		t.Errorf("length sent: %d", got)
	}
	if n, err := d.BulkIn(qaIn, exact, time.Second); err != nil || n != maxTransfer {
		t.Errorf("BulkIn of exactly the limit: %d %v", n, err)
	}
	if got := k.last(t).bt.Len; got != maxTransfer {
		t.Errorf("length read: %d", got)
	}
	// A refusal does not latch.
	if _, err := d.BulkOut(qaOut, []byte{1}, time.Second); err != nil {
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
	qaClaim(t, d)
	out := []byte{0, 1, 2, 0xFF, 0x80}
	if n, err := d.BulkOut(qaOut, out, time.Second); err != nil || n != 4 {
		t.Fatalf("BulkOut: %d %v", n, err)
	}
	in := make([]byte, 6)
	n, err := d.BulkIn(qaIn, in, time.Second)
	if err != nil || n != 5 {
		t.Fatalf("BulkIn: %d %v", n, err)
	}
	if string(in[:n]) != string([]byte{0xA0, 0xA1, 0xA2, 0xA3, 0xA4}) {
		t.Errorf("read %x", in[:n])
	}
	calls := k.snapshot()
	if string(calls[1].data) != string(out) {
		t.Errorf("the kernel saw %x, want %x", calls[1].data, out)
	}
}

// B. Timeout values as the kernel gets them.
func TestQATimeoutValuesReachTheKernel(t *testing.T) {
	const capMs = 600000 // ten minutes in ms
	cases := []struct {
		in   time.Duration
		want uint32
	}{
		{0, 1}, {-1, 1}, {math.MinInt64, 1}, {time.Nanosecond, 1}, {999_999, 1}, {time.Millisecond, 1}, {time.Millisecond + 1, 2},
		{50 * time.Microsecond, 1}, {15 * time.Second, 15000}, {30*time.Second - 1, 30000}, {time.Hour, capMs},
		{maxTimeout, capMs}, {maxTimeout + 1, capMs}, {maxTimeout - 1, capMs}, {maxTimeout - time.Millisecond, capMs - 1},
		{maxTimeout - time.Millisecond + 1, capMs}, {time.Duration(1<<30) * time.Millisecond, capMs}, {10 * 365 * 24 * time.Hour, capMs},
		{math.MaxInt64, capMs}, {math.MaxInt64 - 1, capMs}, {math.MaxInt64 - 999_998, capMs}, {math.MaxInt64 - 999_999, capMs},
	}
	k := &qaKernel{}
	d, _ := qaDevice(t, nil, k)
	qaClaim(t, d)
	for _, c := range cases {
		before := k.count()
		if _, err := d.BulkOut(qaOut, []byte{1}, c.in); err != nil {
			t.Fatal(err)
		}
		if _, err := d.BulkIn(qaIn, make([]byte, 1), c.in); err != nil {
			t.Fatal(err)
		}
		for _, call := range k.snapshot()[before:] {
			if call.bt.Timeout != c.want {
				t.Errorf("timeout %v: the kernel got %d ms, want %d", c.in, call.bt.Timeout, c.want)
			}
		}
	}
}

// A duration that is near the largest the type holds must give the ceiling of ten minutes, never an overflow. This was a bug of the
// first version (ms added almost a millisecond to round up before it clamped, and the sum wrapped for a duration above
// MaxInt64 minus 999999 ns, so ms(MaxInt64) was 2217714955); it is fixed by clamping first, and this test keeps it fixed.
func TestQAMillisecondsDoNotOverflowForHugeDurations(t *testing.T) {
	for _, d := range []time.Duration{math.MaxInt64, math.MaxInt64 - 1, math.MaxInt64 - 999_998, math.MaxInt64 - 999_999, math.MaxInt64 - 1_000_000,
		math.MaxInt64 / 2, 1 << 62, 1 << 40} {
		if got := ms(d); got != 600000 {
			t.Errorf("ms(%d) = %d, want 600000", int64(d), got)
		}
	}
}

// The part of ms that does not overflow: always between 1 and 2^30, never decreasing as the duration grows, ceiling to the
// millisecond. Sampled around every boundary.
func TestQAMillisecondsAreBoundedAndMonotonic(t *testing.T) {
	var samples []time.Duration
	for _, base := range []time.Duration{0, time.Millisecond, 2 * time.Millisecond, time.Second, time.Hour, time.Duration(1<<30) * time.Millisecond,
		time.Duration(1<<31) * time.Millisecond, maxTimeout, math.MaxInt64 - 2*time.Millisecond} {
		for _, d := range []time.Duration{-1, 0, 1, 2, 999_999, 1_000_000, 1_000_001} {
			if v := base + d; v >= 0 {
				samples = append(samples, v)
			}
		}
	}
	slices.Sort(samples)
	prev := uint32(0)
	for _, s := range samples {
		got := ms(s)
		if got < 1 || got > 600000 {
			t.Errorf("ms(%d) = %d out of 1..600000", int64(s), got)
		}
		if got < prev {
			t.Errorf("ms(%d) = %d is below the value %d of a shorter duration", int64(s), got, prev)
		}
		if s > 0 && s <= maxTimeout {
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
	d, path := qaDevice(t, nil, k)
	fd := d.fd
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	qaClaim(t, d)
	if b, err := d.Descriptors(); err != nil || len(b) != len(qaDefaultDesc()) {
		t.Errorf("descriptors after the node vanished: %v %v", b, err)
	}
	if _, err := d.BulkOut(qaOut, []byte{1}, time.Second); err != nil {
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

// Descriptors are read once at Open, whatever the node holds afterwards (the read at most 64 KiB is held in TestQALargeDescriptors).
func TestQADescriptorsReadOnce(t *testing.T) {
	k := &qaKernel{}
	d, path := qaDevice(t, nil, k)
	b, err := d.Descriptors()
	if err != nil || len(b) != len(qaDefaultDesc()) {
		t.Fatalf("descriptors: %d bytes, %v", len(b), err)
	}
	if err := os.WriteFile(path, []byte{1, 2, 3}, 0o600); err != nil {
		t.Fatal(err)
	}
	if again, _ := d.Descriptors(); len(again) != len(b) {
		t.Errorf("descriptors read again: %d bytes", len(again))
	}
	// A node that gives nothing is no device.
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if d, err := open(empty, false); !errors.Is(err, ErrBadDescriptors) || d != nil {
		t.Errorf("empty node: %v %v", d, err)
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
	qaClaim(t, d)
	type result struct {
		n   int
		err error
	}
	transfer := make(chan result, 1)
	go func() {
		n, err := d.BulkIn(qaIn, make([]byte, 16), time.Hour)
		transfer <- result{n, err}
	}()
	<-inside
	closed := make(chan error, 1)
	go func() { closed <- d.Close() }()
	late := make(chan error, 1)
	go func() { _, err := d.BulkOut(qaOut, []byte{1}, time.Second); late <- err }()
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
	if _, err := d.BulkIn(qaIn, make([]byte, 1), time.Second); !errors.Is(err, ErrClosed) {
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
		d, _ := qaDevice(t, nil, k)
		var wg sync.WaitGroup
		var bad atomic.Int32
		check := func(err error) {
			// Another goroutine may have released the interface: ErrNotClaimed is then the answer, and the refusals of the guards.
			if err != nil && !errors.Is(err, ErrClosed) && !errors.Is(err, ErrNotClaimed) {
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
						_, err := d.BulkOut(qaOut, []byte{1, 2}, time.Second)
						check(err)
					case 1:
						_, err := d.BulkIn(qaIn, make([]byte, 8), time.Second)
						check(err)
					case 2:
						check(d.Claim(qaCCID + 2*(g%2)))
					case 3:
						_ = d.Release(qaCCID + 2*(g%2))
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
		_, _ = d.BulkOut(qaOut, []byte{1}, time.Second)
		_ = d.Claim(qaCCID)
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
	d.claimed[qaCCID] = true // as if a claim had succeeded earlier
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	start := make(chan struct{})
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := d.BulkIn(qaIn, make([]byte, 4), time.Second)
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
	for _, n := range []int{qaCCID, qaCCID2} {
		_ = d.Claim(n)
	}
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() { defer wg.Done(); _ = d.Close() }()
	}
	wg.Wait()
	if got := k.countReq(reqRelease); got != 2 {
		t.Errorf("%d releases for 2 interfaces", got)
	}
}
