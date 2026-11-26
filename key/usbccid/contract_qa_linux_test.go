//go:build linux && (amd64 || arm64)

package usbccid

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// C. One table of cases for the contract of Port, run against the real Device over the scripted kernel and against the fakes of the
// tests, so that the next layer can rely on either. The Port comment is the contract:
//
//   - Claim takes only a CCID interface that the descriptors hold (else ErrNoSuchInterface); a bulk call goes only to a bulk endpoint
//     of a claimed interface, in the direction of the call (ErrNotClaimed, ErrBadEndpoint), with a length of at most 64 KiB and a read
//     buffer that is not empty (ErrBadLength).
//   - Claim of an interface another holder has is an error for which errors.Is(err, ErrBusy) holds; nothing is latched.
//   - Release of an interface that is not claimed is not an error.
//   - A timeout is an error for which errors.Is(err, ErrTimeout) holds, and the port stays usable.
//   - Once a call returned ErrGone, every later call of Descriptors, Claim, BulkOut and BulkIn returns an error that wraps ErrGone.
//     Release and Close of a gone device return nil (the kernel dropped the claims with the device).
//   - Close releases every claimed interface, is safe to call twice, and after it Descriptors, Claim, BulkOut and BulkIn return
//     ErrClosed. Release after Close returns nil (the Device comment says "every call returns ErrClosed", see the report).

// qaHarness is what the table needs of an implementation.
type qaHarness struct {
	port             Port
	holdElsewhere    func(n int)
	releaseElsewhere func(n int)
	unplug           func()
	armTimeout       func() // the next transfer times out; nil when the implementation has none
	holders          func() int
}

func qaDeviceHarness(t *testing.T) qaHarness {
	var mu sync.Mutex
	elsewhere := map[uint32]bool{}
	ours := map[uint32]bool{}
	unplugged, timeout := false, false
	k := &qaKernel{answer: func(c kcall) (uintptr, unix.Errno) {
		mu.Lock()
		defer mu.Unlock()
		if unplugged {
			return 0, unix.ENODEV
		}
		switch c.req {
		case reqClaim:
			if elsewhere[c.iface] {
				return 0, unix.EBUSY
			}
			ours[c.iface] = true
		case reqRelease:
			delete(ours, c.iface)
		case reqBulk:
			if timeout {
				timeout = false
				return 0, unix.ETIMEDOUT
			}
			return uintptr(c.bt.Len), 0
		}
		return 0, 0
	}}
	d, _ := qaDevice(t, nil, k)
	locked := func(f func()) { mu.Lock(); f(); mu.Unlock() }
	return qaHarness{
		port:             d,
		holdElsewhere:    func(n int) { locked(func() { elsewhere[uint32(n)] = true }) },  //nolint:gosec // a small test number
		releaseElsewhere: func(n int) { locked(func() { delete(elsewhere, uint32(n)) }) }, //nolint:gosec // a small test number
		unplug:           func() { locked(func() { unplugged = true }) },
		armTimeout:       func() { locked(func() { timeout = true }) },
		holders:          func() int { mu.Lock(); defer mu.Unlock(); return len(ours) },
	}
}

func qaFakeHarness(*testing.T) qaHarness {
	f := newFakePort()
	return qaHarness{
		port:             f,
		holdElsewhere:    func(n int) { f.mu.Lock(); f.elsewhr[n] = true; f.mu.Unlock() },
		releaseElsewhere: func(n int) { f.mu.Lock(); delete(f.elsewhr, n); f.mu.Unlock() },
		unplug:           func() { f.mu.Lock(); f.gone = true; f.mu.Unlock() },
		holders:          func() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.claimed) },
	}
}

// timeoutFake is the fake of the tests with a transfer that can be made to time out, as a test above this package would write it.
type timeoutFake struct {
	*fakePort
	tmu   sync.Mutex
	armed bool
}

func (f *timeoutFake) trip() error {
	f.tmu.Lock()
	defer f.tmu.Unlock()
	if f.armed {
		f.armed = false
		return fmt.Errorf("transfer: %w: %w", ErrTimeout, unix.ETIMEDOUT)
	}
	return nil
}

func (f *timeoutFake) BulkOut(ep uint8, data []byte, timeout time.Duration) (int, error) {
	if _, err := f.fakePort.BulkOut(ep, data, timeout); err != nil {
		return 0, err
	}
	if err := f.trip(); err != nil {
		return 0, err
	}
	return len(data), nil
}

func (f *timeoutFake) BulkIn(ep uint8, buf []byte, timeout time.Duration) (int, error) {
	if _, err := f.fakePort.BulkIn(ep, buf, timeout); err != nil {
		return 0, err
	}
	if err := f.trip(); err != nil {
		return 0, err
	}
	return len(buf), nil
}

func qaTimeoutFakeHarness(t *testing.T) qaHarness {
	h := qaFakeHarness(t)
	tf := &timeoutFake{fakePort: h.port.(*fakePort)}
	h.port = tf
	h.armTimeout = func() { tf.tmu.Lock(); tf.armed = true; tf.tmu.Unlock() }
	return h
}

func TestQAPortContractTable(t *testing.T) {
	impls := []struct {
		name string
		mk   func(*testing.T) qaHarness
	}{{"device", qaDeviceHarness}, {"fake", qaFakeHarness}, {"timeout fake", qaTimeoutFakeHarness}}

	cases := []struct {
		name string
		run  func(t *testing.T, h qaHarness)
	}{
		{"transfers", func(t *testing.T, h qaHarness) {
			if err := h.port.Claim(qaCCID); err != nil {
				t.Fatal(err)
			}
			if n, err := h.port.BulkOut(qaOut, []byte{1, 2, 3}, time.Second); err != nil || n != 3 {
				t.Errorf("BulkOut: %d %v", n, err)
			}
			buf := make([]byte, 64)
			if n, err := h.port.BulkIn(qaIn, buf, time.Second); err != nil || n < 0 || n > len(buf) {
				t.Errorf("BulkIn: %d %v", n, err)
			}
		}},
		{"an empty write is allowed and an empty read is refused", func(t *testing.T, h qaHarness) {
			_ = h.port.Claim(qaCCID)
			if n, err := h.port.BulkOut(qaOut, nil, time.Second); err != nil || n != 0 {
				t.Errorf("BulkOut of nothing: %d %v", n, err)
			}
			if n, err := h.port.BulkIn(qaIn, []byte{}, time.Second); !errors.Is(err, ErrBadLength) || n != 0 {
				t.Errorf("BulkIn into nothing: %d %v, want ErrBadLength (a read needs a buffer)", n, err)
			}
		}},
		{"timeouts of every size are accepted", func(t *testing.T, h qaHarness) {
			_ = h.port.Claim(qaCCID)
			for _, d := range []time.Duration{-time.Hour, 0, 1, time.Millisecond, time.Minute} {
				if _, err := h.port.BulkOut(qaOut, []byte{1}, d); err != nil {
					t.Errorf("BulkOut with timeout %v: %v", d, err)
				}
			}
		}},
		{"busy", func(t *testing.T, h qaHarness) {
			h.holdElsewhere(qaCCID2)
			err := h.port.Claim(qaCCID2)
			if !errors.Is(err, ErrBusy) || errors.Is(err, ErrGone) || errors.Is(err, ErrClosed) || errors.Is(err, ErrTimeout) {
				t.Fatalf("claim of a held interface: %v, want ErrBusy only", err)
			}
			if h.holders() != 0 {
				t.Error("a refused claim holds the interface")
			}
			if err := h.port.Claim(qaCCID); err != nil {
				t.Errorf("claim of another interface after a busy one: %v", err)
			}
			if _, err := h.port.BulkOut(qaOut, []byte{1}, time.Second); err != nil {
				t.Errorf("a busy claim must not latch: %v", err)
			}
			if err := h.port.Claim(qaCCID2); !errors.Is(err, ErrBusy) {
				t.Errorf("the same claim again: %v", err)
			}
			h.releaseElsewhere(qaCCID2)
			if err := h.port.Claim(qaCCID2); err != nil {
				t.Errorf("claim after the holder let go: %v", err)
			}
			if h.holders() != 2 {
				t.Errorf("%d interfaces held, want 2", h.holders())
			}
		}},
		{"claim takes only a CCID interface of the descriptors", func(t *testing.T, h qaHarness) {
			for _, n := range []int{qaHID, qaVendor, 4, 5, 255, 256, -1} {
				if err := h.port.Claim(n); !errors.Is(err, ErrNoSuchInterface) || errors.Is(err, ErrBusy) || errors.Is(err, ErrGone) {
					t.Errorf("claim %d: %v, want ErrNoSuchInterface", n, err)
				}
			}
			if h.holders() != 0 {
				t.Error("a refused claim holds an interface")
			}
		}},
		{"bulk calls are held to the claimed interface", func(t *testing.T, h qaHarness) {
			buf := make([]byte, 8)
			if _, err := h.port.BulkOut(qaOut, buf, time.Second); !errors.Is(err, ErrNotClaimed) {
				t.Errorf("before the claim: %v", err)
			}
			if _, err := h.port.BulkIn(qaIn, buf, time.Second); !errors.Is(err, ErrNotClaimed) {
				t.Errorf("before the claim: %v", err)
			}
			if err := h.port.Claim(qaCCID); err != nil {
				t.Fatal(err)
			}
			for name, c := range map[string]struct {
				err  error
				want error
			}{
				"the endpoint of the other CCID interface": {func() error { _, e := h.port.BulkOut(qaOut2, buf, time.Second); return e }(), ErrNotClaimed},
				"a bulk endpoint of the vendor interface":  {func() error { _, e := h.port.BulkOut(qaVOut, buf, time.Second); return e }(), ErrBadEndpoint},
				"an IN address for a write":                {func() error { _, e := h.port.BulkOut(qaIn, buf, time.Second); return e }(), ErrBadEndpoint},
				"an OUT address for a read":                {func() error { _, e := h.port.BulkIn(qaOut, buf, time.Second); return e }(), ErrBadEndpoint},
				"the interrupt endpoint":                   {func() error { _, e := h.port.BulkIn(qaIntr, buf, time.Second); return e }(), ErrBadEndpoint},
				"an endpoint the descriptors do not hold":  {func() error { _, e := h.port.BulkIn(0x8F, buf, time.Second); return e }(), ErrBadEndpoint},
				"an empty read":                            {func() error { _, e := h.port.BulkIn(qaIn, nil, time.Second); return e }(), ErrBadLength},
				"a write of more than 64 KiB":              {func() error { _, e := h.port.BulkOut(qaOut, make([]byte, 64*1024+1), time.Second); return e }(), ErrBadLength},
				"a read of more than 64 KiB":               {func() error { _, e := h.port.BulkIn(qaIn, make([]byte, 64*1024+1), time.Second); return e }(), ErrBadLength},
			} {
				if !errors.Is(c.err, c.want) || errors.Is(c.err, ErrGone) || errors.Is(c.err, ErrBusy) {
					t.Errorf("%s: %v, want %v", name, c.err, c.want)
				}
			}
			if _, err := h.port.BulkOut(qaOut, nil, time.Second); err != nil {
				t.Errorf("an empty write: %v", err)
			}
			if _, err := h.port.BulkOut(qaOut, make([]byte, 64*1024), time.Second); err != nil {
				t.Errorf("a write of exactly 64 KiB: %v", err)
			}
			if err := h.port.Release(qaCCID); err != nil {
				t.Fatal(err)
			}
			if _, err := h.port.BulkOut(qaOut, buf, time.Second); !errors.Is(err, ErrNotClaimed) {
				t.Errorf("after the release: %v", err)
			}
		}},
		{"release", func(t *testing.T, h qaHarness) {
			if err := h.port.Release(qaCCID2); err != nil {
				t.Errorf("release of an interface that was never claimed: %v", err)
			}
			if err := h.port.Claim(qaCCID2); err != nil {
				t.Fatal(err)
			}
			if err := h.port.Release(qaCCID2); err != nil {
				t.Errorf("release: %v", err)
			}
			if h.holders() != 0 {
				t.Error("the interface is still held after the release")
			}
			if err := h.port.Release(qaCCID2); err != nil {
				t.Errorf("second release: %v", err)
			}
		}},
		{"timeout", func(t *testing.T, h qaHarness) {
			if h.armTimeout == nil {
				t.Skip("this fake has no timeouts")
			}
			_ = h.port.Claim(qaCCID)
			h.armTimeout()
			n, err := h.port.BulkIn(qaIn, make([]byte, 8), time.Millisecond)
			if !errors.Is(err, ErrTimeout) || errors.Is(err, ErrGone) || errors.Is(err, ErrBusy) || n != 0 {
				t.Fatalf("a transfer that times out: %d %v", n, err)
			}
			h.armTimeout()
			if _, err := h.port.BulkOut(qaOut, []byte{1}, time.Millisecond); !errors.Is(err, ErrTimeout) {
				t.Fatalf("BulkOut that times out: %v", err)
			}
			if _, err := h.port.BulkOut(qaOut, []byte{1}, time.Millisecond); err != nil {
				t.Errorf("a transfer after a timeout: %v", err)
			}
			if _, err := h.port.Descriptors(); err != nil {
				t.Errorf("descriptors after a timeout: %v", err)
			}
		}},
		{"descriptors are a private copy", func(t *testing.T, h qaHarness) {
			a, err := h.port.Descriptors()
			if err != nil || len(a) < 2 {
				t.Fatalf("descriptors: %v %v", a, err)
			}
			want := a[0]
			a[0] ^= 0xFF
			b, _ := h.port.Descriptors()
			if b[0] != want {
				t.Error("a change of the returned slice reached the port")
			}
		}},
		{"gone is final", func(t *testing.T, h qaHarness) {
			_ = h.port.Claim(qaCCID)
			h.unplug()
			if _, err := h.port.BulkIn(qaIn, make([]byte, 8), time.Second); !errors.Is(err, ErrGone) {
				t.Fatalf("the transfer that met the unplug: %v", err)
			}
			for range 3 {
				for _, err := range []error{
					h.port.Claim(qaCCID), h.port.Claim(qaCCID2),
					func() error { _, err := h.port.BulkOut(qaOut, []byte{1}, time.Second); return err }(),
					func() error { _, err := h.port.BulkIn(qaIn, make([]byte, 8), time.Second); return err }(),
					func() error { _, err := h.port.Descriptors(); return err }(),
				} {
					if !errors.Is(err, ErrGone) || errors.Is(err, ErrClosed) || errors.Is(err, ErrBusy) {
						t.Errorf("a call after the unplug: %v, want ErrGone", err)
					}
				}
			}
			if err := h.port.Release(qaCCID); err != nil {
				t.Errorf("release on a gone device: %v", err)
			}
			if err := h.port.Close(); err != nil {
				t.Errorf("close of a gone device: %v", err)
			}
			if _, err := h.port.BulkIn(qaIn, make([]byte, 8), time.Second); !errors.Is(err, ErrClosed) {
				t.Errorf("after closing a gone device: %v, want ErrClosed", err)
			}
		}},
		{"close", func(t *testing.T, h qaHarness) {
			_ = h.port.Claim(qaCCID)
			_ = h.port.Claim(qaCCID2)
			if err := h.port.Close(); err != nil {
				t.Fatal(err)
			}
			if h.holders() != 0 {
				t.Errorf("%d interfaces held after Close", h.holders())
			}
			for range 2 {
				if err := h.port.Close(); err != nil {
					t.Errorf("close again: %v", err)
				}
				if err := h.port.Release(qaCCID); err != nil {
					t.Errorf("release after close: %v", err)
				}
				if _, err := h.port.Descriptors(); !errors.Is(err, ErrClosed) {
					t.Errorf("descriptors after close: %v", err)
				}
				if err := h.port.Claim(qaCCID); !errors.Is(err, ErrClosed) {
					t.Errorf("claim after close: %v", err)
				}
				if _, err := h.port.BulkOut(qaOut, []byte{1}, time.Second); !errors.Is(err, ErrClosed) {
					t.Errorf("bulk out after close: %v", err)
				}
				if _, err := h.port.BulkIn(qaIn, make([]byte, 1), time.Second); !errors.Is(err, ErrClosed) {
					t.Errorf("bulk in after close: %v", err)
				}
			}
		}},
	}
	for _, impl := range impls {
		for _, c := range cases {
			t.Run(impl.name+"/"+c.name, func(t *testing.T) {
				h := impl.mk(t)
				defer func() { _ = h.port.Close() }()
				c.run(t, h)
			})
		}
	}
}
