//go:build linux

package usbccid

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// fakePort is an in-memory Port that keeps the contract of Port: one holder per interface (a second claim is ErrBusy), a latched
// ErrGone after Unplug, Close safe to call twice. It is also the model of the fake that a test above this package writes.
type fakePort struct {
	mu      sync.Mutex
	elsewhr map[int]bool // interfaces held by another process
	claimed map[int]bool
	gone    bool
	closed  bool
}

func newFakePort() *fakePort { return &fakePort{elsewhr: map[int]bool{}, claimed: map[int]bool{}} }

func (f *fakePort) state() error {
	switch {
	case f.closed:
		return ErrClosed
	case f.gone:
		return fmt.Errorf("%w: %w", ErrGone, unix.ENODEV)
	}
	return nil
}

func (f *fakePort) Descriptors() ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return []byte{18, 1}, f.state()
}

func (f *fakePort) Claim(n int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.state(); err != nil {
		return err
	}
	if f.elsewhr[n] {
		return fmt.Errorf("claim interface %d: %w", n, ErrBusy)
	}
	f.claimed[n] = true
	return nil
}

func (f *fakePort) Release(n int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.claimed, n)
	return nil
}

func (f *fakePort) BulkOut(_ uint8, data []byte, _ time.Duration) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.state(); err != nil {
		return 0, err
	}
	return len(data), nil
}

func (f *fakePort) BulkIn(_ uint8, buf []byte, _ time.Duration) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.state(); err != nil {
		return 0, err
	}
	return len(buf), nil
}

func (f *fakePort) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	f.claimed = map[int]bool{}
	return nil
}

// harness is what the contract needs of a Port under test: to make another process hold an interface, and to unplug the device.
type harness struct {
	port        Port
	holdElsewhr func(n int)
	unplug      func()
}

func fakeHarness(*testing.T) harness {
	f := newFakePort()
	return harness{
		port:        f,
		holdElsewhr: func(n int) { f.mu.Lock(); f.elsewhr[n] = true; f.mu.Unlock() },
		unplug:      func() { f.mu.Lock(); f.gone = true; f.mu.Unlock() },
	}
}

// deviceHarness is a real Device whose kernel is scripted: a claim of an interface that is held elsewhere answers EBUSY, and every
// request after the unplug answers ENODEV.
func deviceHarness(t *testing.T) harness {
	var mu sync.Mutex
	held := map[uint32]bool{}
	unplugged := false
	d := scripted(t, []byte{18, 1}, func(req uintptr, arg unsafe.Pointer) (uintptr, unix.Errno) {
		mu.Lock()
		defer mu.Unlock()
		if unplugged {
			return 0, unix.ENODEV
		}
		if req == reqClaim && held[*(*uint32)(arg)] {
			return 0, unix.EBUSY
		}
		if req == reqBulk {
			return uintptr((*bulkTransfer)(arg).Len), 0
		}
		return 0, 0
	})
	return harness{
		port: d,
		holdElsewhr: func(n int) {
			mu.Lock()
			held[uint32(n)] = true //nolint:gosec // a small test number
			mu.Unlock()
		},
		unplug: func() { mu.Lock(); unplugged = true; mu.Unlock() },
	}
}

// TestPortContract holds the real Device and the fake to the same rules, so that the fake of a test above this package is a fair
// stand-in for the real thing.
func TestPortContract(t *testing.T) {
	for name, mk := range map[string]func(*testing.T) harness{"fake": fakeHarness, "device": deviceHarness} {
		t.Run(name+"/busy", func(t *testing.T) {
			h := mk(t)
			defer func() { _ = h.port.Close() }()
			h.holdElsewhr(2)
			if err := h.port.Claim(2); !errors.Is(err, ErrBusy) {
				t.Fatalf("claim of a held interface: %v, want ErrBusy", err)
			}
			if err := h.port.Claim(1); err != nil {
				t.Errorf("claim of another interface: %v", err)
			}
			if err := h.port.Release(2); err != nil {
				t.Errorf("release of the interface that was refused: %v", err)
			}
		})
		t.Run(name+"/gone", func(t *testing.T) {
			h := mk(t)
			if err := h.port.Claim(1); err != nil {
				t.Fatal(err)
			}
			if _, err := h.port.BulkOut(0x02, []byte{1}, time.Second); err != nil {
				t.Fatal(err)
			}
			h.unplug()
			_, err := h.port.BulkIn(0x82, make([]byte, 8), time.Second)
			if !errors.Is(err, ErrGone) {
				t.Fatalf("a transfer after the unplug: %v, want ErrGone", err)
			}
			for range 2 { // every later call, however often
				if _, err := h.port.BulkOut(0x02, []byte{1}, time.Second); !errors.Is(err, ErrGone) {
					t.Errorf("bulk out: %v", err)
				}
				if _, err := h.port.BulkIn(0x82, make([]byte, 8), time.Second); !errors.Is(err, ErrGone) {
					t.Errorf("bulk in: %v", err)
				}
				if err := h.port.Claim(1); !errors.Is(err, ErrGone) {
					t.Errorf("claim: %v", err)
				}
				if _, err := h.port.Descriptors(); !errors.Is(err, ErrGone) {
					t.Errorf("descriptors: %v", err)
				}
			}
			if err := h.port.Close(); err != nil {
				t.Errorf("close of a gone device: %v", err)
			}
		})
		t.Run(name+"/close twice", func(t *testing.T) {
			h := mk(t)
			if err := h.port.Claim(1); err != nil {
				t.Fatal(err)
			}
			if err := h.port.Close(); err != nil {
				t.Fatal(err)
			}
			if err := h.port.Close(); err != nil {
				t.Errorf("second close: %v", err)
			}
			checkAllClosed(t, h.port)
		})
	}
}
