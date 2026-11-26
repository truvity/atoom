//go:build linux

package usbccid

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// These tests never open a USB node and never touch a card. The code that needs a real node (the ioctls on a usbfs file) is checked
// in three ways: the request numbers are compared with the kernel's values; the ioctl code runs against a regular file made under
// t.TempDir(), which answers every request with ENOTTY; and the answers of the kernel (busy, gone, timeout) are scripted through
// the ioctl hook of Device.

// scripted opens a Device over a file under t.TempDir() that holds desc, and replaces its system call with h. Close closes the file.
func scripted(t *testing.T, desc []byte, h func(req uintptr, arg unsafe.Pointer) (uintptr, unix.Errno)) *Device {
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
	d.ioctl = func(_ int, req uintptr, arg unsafe.Pointer) (uintptr, unix.Errno) { return h(req, arg) }
	return d
}

func TestRequestNumbers(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("the expected numbers are for 64-bit")
	}
	bulk, claim, release := requestNumbers()
	// _IOWR('U', 2, struct usbdevfs_bulktransfer) with a 24-byte struct, _IOR('U', 15, unsigned int), _IOR('U', 16, unsigned int).
	if got := unsafe.Sizeof(bulkTransfer{}); got != 24 {
		t.Fatalf("bulkTransfer is %d bytes, the kernel struct is 24", got)
	}
	if bulk != 0xC0185502 {
		t.Errorf("bulk request = %#x, want 0xC0185502", bulk)
	}
	if claim != 0x8004550F {
		t.Errorf("claim request = %#x, want 0x8004550F", claim)
	}
	if release != 0x80045510 {
		t.Errorf("release request = %#x, want 0x80045510", release)
	}
}

// The package never detaches a driver, never resets a device and never takes a card from its holder. It builds exactly three ioctl
// requests. A fourth one would have to be added on purpose, and this test would say so. The sources are read as text because the
// requests are package variables.
func TestOnlyThreeRequestsAreBuilt(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var built []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f) //nolint:gosec // a source file of this package
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(src), "\n") {
			l := strings.TrimSpace(line)
			if strings.HasPrefix(l, "//") {
				continue
			}
			if strings.Contains(l, "= ioc(") {
				built = append(built, l)
			}
			for _, name := range []string{"DISCONNECT_CLAIM", "USBDEVFS_RESET", "CLEAR_HALT", "SETCONFIGURATION", "USBDEVFS_IOCTL", "SETINTERFACE"} {
				if strings.Contains(l, name) {
					t.Errorf("%s mentions %s: %s", f, name, l)
				}
			}
		}
	}
	if len(built) != 3 {
		t.Fatalf("the package builds %d requests, want 3 (bulk, claim, release): %q", len(built), built)
	}
}

func TestFatalClassification(t *testing.T) {
	for _, e := range []unix.Errno{unix.ENODEV, unix.EIO, unix.ESHUTDOWN, unix.EPROTO, unix.EILSEQ} {
		if !fatal(e) {
			t.Errorf("%v must be fatal", e)
		}
		if !fatal(fmt.Errorf("wrapped: %w", e)) {
			t.Errorf("wrapped %v must be fatal", e)
		}
	}
	for _, e := range []error{unix.EBUSY, unix.ETIMEDOUT, unix.EPIPE, unix.EINVAL, unix.ENOTTY, unix.EOVERFLOW, unix.EINTR, ErrClosed, ErrBusy, errors.New("text"), nil} {
		if fatal(e) {
			t.Errorf("%v must not be fatal", e)
		}
	}
}

func TestErrorValuesAreDistinct(t *testing.T) {
	all := []error{ErrBusy, ErrGone, ErrTimeout, ErrClosed, ErrNotSupported}
	for i, a := range all {
		if a == nil || a.Error() == "" {
			t.Fatalf("error %d is empty", i)
		}
		for j, b := range all {
			if i != j && errors.Is(a, b) {
				t.Errorf("%v is %v", a, b)
			}
		}
	}
}

func TestMillisecondRounding(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want uint32
	}{
		{0, 1}, {-time.Second, 1}, {time.Nanosecond, 1}, {time.Millisecond, 1},
		{time.Millisecond + 1, 2}, {1500 * time.Microsecond, 2}, {10 * time.Second, 10000},
		{1000 * time.Hour, 1 << 30},
	}
	for _, c := range cases {
		if got := ms(c.in); got != c.want {
			t.Errorf("ms(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestOpenRefusesNonDevices(t *testing.T) {
	dir := t.TempDir()
	reg := filepath.Join(dir, "regular")
	if err := os.WriteFile(reg, make([]byte, 64), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(reg); err == nil || !strings.Contains(err.Error(), "not a character device") {
		t.Errorf("regular file: err = %v", err)
	}
	if _, err := Open(dir); err == nil {
		t.Error("a directory was opened")
	}
	if _, err := Open(filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file: err = %v", err)
	}
	c := Candidate{NodePath: filepath.Join(dir, "001", "002")}
	if _, err := c.Open(); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Candidate.Open on a missing node: err = %v", err)
	}
}

// A regular file refuses every ioctl with ENOTTY, which runs the real error path (wrapping, no panic, no latch) without a device.
func TestDeviceAgainstARegularFile(t *testing.T) {
	desc := []byte{1, 2, 3, 4}
	path := filepath.Join(t.TempDir(), "node")
	if err := os.WriteFile(path, desc, 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.Descriptors()
	if err != nil || string(got) != string(desc) {
		t.Fatalf("descriptors = %x, %v", got, err)
	}
	got[0] = 99
	if again, _ := d.Descriptors(); again[0] != 1 {
		t.Error("Descriptors hands out its own buffer")
	}

	err = d.Claim(2)
	if !errors.Is(err, unix.ENOTTY) || errors.Is(err, ErrBusy) || errors.Is(err, ErrGone) {
		t.Fatalf("claim on a file: %v, want ENOTTY only", err)
	}
	if !strings.Contains(err.Error(), "claim interface 2") {
		t.Errorf("claim error does not name the interface: %v", err)
	}
	if d.claimed[2] {
		t.Error("a failed claim was recorded as held")
	}
	if _, err := d.Descriptors(); err != nil {
		t.Errorf("descriptors after a failed claim (not sticky): %v", err)
	}
	n, err := d.BulkOut(0x02, []byte{1, 2, 3}, 10*time.Millisecond)
	if !errors.Is(err, unix.ENOTTY) || n != 0 || !strings.Contains(err.Error(), "0x02") {
		t.Errorf("bulk out: n=%d err=%v", n, err)
	}
	if _, err := d.BulkIn(0x82, make([]byte, 64), 10*time.Millisecond); !errors.Is(err, unix.ENOTTY) {
		t.Errorf("bulk in: %v", err)
	}
	if _, err := d.BulkIn(0x82, nil, 10*time.Millisecond); !errors.Is(err, unix.ENOTTY) { // the empty-buffer branch
		t.Errorf("bulk in, empty buffer: %v", err)
	}
	if err := d.Release(2); err != nil {
		t.Errorf("release of an unclaimed interface: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Errorf("second close: %v", err)
	}
	checkAllClosed(t, d)
}

func checkAllClosed(t *testing.T, p Port) {
	t.Helper()
	if _, err := p.Descriptors(); !errors.Is(err, ErrClosed) {
		t.Errorf("descriptors after close: %v", err)
	}
	if err := p.Claim(0); !errors.Is(err, ErrClosed) {
		t.Errorf("claim after close: %v", err)
	}
	if _, err := p.BulkOut(0x02, []byte{1}, time.Millisecond); !errors.Is(err, ErrClosed) {
		t.Errorf("bulk out after close: %v", err)
	}
	if _, err := p.BulkIn(0x82, make([]byte, 8), time.Millisecond); !errors.Is(err, ErrClosed) {
		t.Errorf("bulk in after close: %v", err)
	}
}

// BulkOut clears the direction bit and BulkIn sets it, whatever the caller passes; the length, the timeout and the data pointer
// reach the kernel structure.
func TestBulkArgumentsReachTheKernel(t *testing.T) {
	var seen []bulkTransfer
	d := scripted(t, nil, func(req uintptr, arg unsafe.Pointer) (uintptr, unix.Errno) {
		if req != reqBulk {
			t.Errorf("request %#x, want the bulk request", req)
		}
		bt := *(*bulkTransfer)(arg)
		seen = append(seen, bt)
		return uintptr(bt.Len), 0
	})
	n, err := d.BulkOut(0x82, []byte{1, 2, 3}, 1500*time.Microsecond)
	if err != nil || n != 3 {
		t.Fatalf("BulkOut = %d, %v", n, err)
	}
	n, err = d.BulkIn(0x02, make([]byte, 8), time.Second)
	if err != nil || n != 8 {
		t.Fatalf("BulkIn = %d, %v", n, err)
	}
	if _, err = d.BulkIn(0x82, nil, time.Second); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 3 {
		t.Fatalf("%d requests", len(seen))
	}
	if seen[0].Ep != 0x02 || seen[0].Len != 3 || seen[0].Timeout != 2 || seen[0].Data == nil {
		t.Errorf("BulkOut(0x82) sent %+v, want endpoint 0x02, 3 bytes, 2 ms", seen[0])
	}
	if seen[1].Ep != 0x82 || seen[1].Len != 8 || seen[1].Timeout != 1000 {
		t.Errorf("BulkIn(0x02) sent %+v, want endpoint 0x82, 8 bytes, 1000 ms", seen[1])
	}
	if seen[2].Len != 0 || seen[2].Data != nil {
		t.Errorf("an empty buffer sent %+v", seen[2])
	}
}

// A claim refused because another holder has the interface is ErrBusy (and EBUSY). It is not latched: the device stays usable, and
// nothing is retried.
func TestClaimBusyIsErrBusy(t *testing.T) {
	var claims atomic.Int32
	held := true
	d := scripted(t, nil, func(req uintptr, _ unsafe.Pointer) (uintptr, unix.Errno) {
		if req != reqClaim {
			return 0, 0 // the release of Close
		}
		claims.Add(1)
		if held {
			return 0, unix.EBUSY
		}
		return 0, 0
	})
	err := d.Claim(2)
	if !errors.Is(err, ErrBusy) || !errors.Is(err, unix.EBUSY) {
		t.Fatalf("claim of a held interface: %v, want ErrBusy and EBUSY", err)
	}
	if errors.Is(err, ErrGone) || !strings.Contains(err.Error(), "interface 2") {
		t.Errorf("busy error: %v", err)
	}
	if got := claims.Load(); got != 1 {
		t.Errorf("the claim was tried %d times, want once (no retry)", got)
	}
	if d.claimed[2] {
		t.Error("a busy claim was recorded as held")
	}
	if err := d.Release(2); err != nil {
		t.Errorf("release of what was never claimed: %v", err)
	}
	// The holder lets go; the same handle claims now.
	held = false
	if err := d.Claim(2); err != nil {
		t.Errorf("claim after the holder left: %v", err)
	}
}

// The first fatal error is latched: every later call returns an error that wraps ErrGone and the errno, and makes no system call.
func TestGoneIsLatched(t *testing.T) {
	for _, e := range []unix.Errno{unix.ENODEV, unix.ESHUTDOWN, unix.EIO, unix.EPROTO, unix.EILSEQ} {
		t.Run(e.Error(), func(t *testing.T) {
			var calls atomic.Int32
			gone := false
			d := scripted(t, []byte{1}, func(uintptr, unsafe.Pointer) (uintptr, unix.Errno) {
				calls.Add(1)
				if gone {
					return 0, e
				}
				return 0, 0
			})
			if err := d.Claim(1); err != nil {
				t.Fatal(err)
			}
			gone = true
			_, err := d.BulkIn(0x82, make([]byte, 64), time.Second)
			if !errors.Is(err, ErrGone) || !errors.Is(err, e) {
				t.Fatalf("the failing transfer: %v, want ErrGone and %v", err, e)
			}
			before := calls.Load()
			again := func(what string, err error) {
				t.Helper()
				if !errors.Is(err, ErrGone) || !errors.Is(err, e) {
					t.Errorf("%s after the device went: %v, want ErrGone and %v", what, err, e)
				}
			}
			_, err = d.Descriptors()
			again("descriptors", err)
			again("claim", d.Claim(3))
			_, err = d.BulkOut(0x02, []byte{1}, time.Second)
			again("bulk out", err)
			_, err = d.BulkIn(0x82, make([]byte, 8), time.Second)
			again("bulk in", err)
			if got := calls.Load(); got != before {
				t.Errorf("%d system calls after the latch", got-before)
			}
			// The kernel dropped the claim with the device: release asks the kernel for nothing.
			if err := d.Release(1); err != nil {
				t.Errorf("release after the device went: %v", err)
			}
			if err := d.Close(); err != nil {
				t.Errorf("close after the device went: %v", err)
			}
			if got := calls.Load(); got != before {
				t.Errorf("%d system calls in release and close of a gone device", got-before)
			}
			if err := d.Close(); err != nil {
				t.Errorf("second close: %v", err)
			}
			if _, err := d.BulkIn(0x82, make([]byte, 8), time.Second); !errors.Is(err, ErrClosed) {
				t.Errorf("after close: %v", err)
			}
		})
	}
}

// Only the first fatal error is kept: a later, different errno does not replace it.
func TestFirstFatalErrorStays(t *testing.T) {
	next := unix.EIO
	d := scripted(t, nil, func(uintptr, unsafe.Pointer) (uintptr, unix.Errno) { return 0, next })
	if err := d.Claim(0); !errors.Is(err, ErrGone) || !errors.Is(err, unix.EIO) {
		t.Fatalf("claim: %v", err)
	}
	next = unix.ENODEV
	if _, err := d.BulkOut(0x02, nil, time.Second); !errors.Is(err, unix.EIO) || errors.Is(err, unix.ENODEV) {
		t.Errorf("the first error was replaced: %v", err)
	}
}

// A timeout, a stall, an overflow and a busy interface leave the device usable; a timeout is ErrTimeout.
func TestOnlyFatalErrorsAreSticky(t *testing.T) {
	next := unix.Errno(0)
	d := scripted(t, nil, func(uintptr, unsafe.Pointer) (uintptr, unix.Errno) { return 0, next })
	for _, e := range []unix.Errno{unix.ETIMEDOUT, unix.EPIPE, unix.EBUSY, unix.EOVERFLOW, unix.EINTR} {
		next = e
		_, err := d.BulkIn(0x82, make([]byte, 8), time.Millisecond)
		if !errors.Is(err, e) || errors.Is(err, ErrGone) {
			t.Fatalf("%v: %v", e, err)
		}
		if got := errors.Is(err, ErrTimeout); got != (e == unix.ETIMEDOUT) {
			t.Errorf("%v: errors.Is(ErrTimeout) = %v", e, got)
		}
	}
	next = 0
	if _, err := d.BulkIn(0x82, make([]byte, 8), time.Millisecond); err != nil {
		t.Errorf("a transfer after the non-fatal errors: %v", err)
	}
}

// Close releases every claimed interface exactly once and closes the node; a second Close does nothing.
func TestCloseReleasesAndIsIdempotent(t *testing.T) {
	var released []uint32
	d := scripted(t, nil, func(req uintptr, arg unsafe.Pointer) (uintptr, unix.Errno) {
		if req == reqRelease {
			released = append(released, *(*uint32)(arg))
		}
		return 0, 0
	})
	for _, n := range []int{1, 4} {
		if err := d.Claim(n); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Release(1); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Errorf("second close: %v", err)
	}
	if len(released) != 2 || released[0] != 1 || released[1] != 4 {
		t.Errorf("released %v, want [1 4] (one explicit, one by Close, none by the second Close)", released)
	}
	checkAllClosed(t, d)
}

// A kernel that refuses the release reports it from Close, and the node is closed all the same.
func TestCloseReportsAReleaseFailureAndClosesTheNode(t *testing.T) {
	d := scripted(t, nil, func(req uintptr, _ unsafe.Pointer) (uintptr, unix.Errno) {
		if req == reqRelease {
			return 0, unix.EINVAL
		}
		return 0, 0
	})
	if err := d.Claim(0); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); !errors.Is(err, unix.EINVAL) {
		t.Errorf("close: %v, want EINVAL", err)
	}
	if err := unix.Fstat(d.fd, new(unix.Stat_t)); !errors.Is(err, unix.EBADF) {
		t.Errorf("the node is still open: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Errorf("second close: %v", err)
	}
}

// One exchange with the device at a time: two goroutines never overlap in the kernel.
func TestCallsAreSerialised(t *testing.T) {
	var inside, overlap atomic.Int32
	d := scripted(t, nil, func(uintptr, unsafe.Pointer) (uintptr, unix.Errno) {
		if inside.Add(1) > 1 {
			overlap.Add(1)
		}
		time.Sleep(time.Millisecond)
		inside.Add(-1)
		return 0, 0
	})
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 5 {
				_, _ = d.BulkOut(0x02, []byte{1}, time.Second)
				_, _ = d.BulkIn(0x82, make([]byte, 4), time.Second)
			}
		}()
	}
	wg.Wait()
	if overlap.Load() != 0 {
		t.Errorf("%d transfers overlapped", overlap.Load())
	}
}

// An interface number or a transfer that cannot be a real one is refused before the kernel is asked, and nothing is latched.
func TestImpossibleArgumentsAreRefusedBeforeTheKernel(t *testing.T) {
	var calls atomic.Int32
	d := scripted(t, nil, func(uintptr, unsafe.Pointer) (uintptr, unix.Errno) {
		calls.Add(1)
		return 0, 0
	})
	for _, n := range []int{-1, 256, 1 << 32} {
		if err := d.Claim(n); !errors.Is(err, unix.EINVAL) {
			t.Errorf("claim %d: %v", n, err)
		}
	}
	if _, err := d.BulkOut(0x02, make([]byte, maxTransfer+1), time.Second); !errors.Is(err, unix.EINVAL) {
		t.Errorf("a transfer of %d bytes: %v", maxTransfer+1, err)
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("%d system calls", got)
	}
	if err := d.Claim(255); err != nil {
		t.Errorf("claim 255: %v", err)
	}
}
