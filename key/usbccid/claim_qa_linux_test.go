//go:build linux && !android && (amd64 || arm64)

package usbccid

import (
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// A claim of an interface that the handle holds already, over the scripted kernel: ErrAlreadyClaimed before any ioctl, and the claim
// stays.
func TestQAClaimTwiceIsRefusedBeforeTheKernel(t *testing.T) {
	k := &qaKernel{}
	d, _ := qaDevice(t, nil, k)
	qaClaim(t, d)
	if got := k.countReq(reqClaim); got != 1 {
		t.Fatalf("%d claim ioctls after the first claim", got)
	}
	total := len(k.snapshot())
	for range 3 {
		err := d.Claim(qaCCID)
		if !errors.Is(err, ErrAlreadyClaimed) || errors.Is(err, ErrBusy) || errors.Is(err, ErrGone) || errors.Is(err, ErrNoSuchInterface) || errors.Is(err, ErrClosed) {
			t.Fatalf("second claim: %v", err)
		}
		if !strings.Contains(err.Error(), "claim interface 1") {
			t.Errorf("text %q does not name the interface", err)
		}
	}
	if len(k.snapshot()) != total {
		t.Errorf("a refused claim reached the kernel: %d calls, was %d", len(k.snapshot()), total)
	}
	// The claim stays: a transfer on the interface still goes through, and nothing was released.
	if _, err := d.BulkOut(qaOut, []byte{1}, time.Second); err != nil {
		t.Errorf("BulkOut after the refused claim: %v", err)
	}
	if got := k.countReq(reqRelease); got != 0 {
		t.Errorf("%d releases", got)
	}
	// Release, then Claim again works, and asks the kernel again.
	if err := d.Release(qaCCID); err != nil {
		t.Fatal(err)
	}
	qaClaim(t, d)
	if got := k.countReq(reqClaim); got != 2 {
		t.Errorf("%d claim ioctls in all, want 2 (one before the release, one after)", got)
	}
	// Another interface of the same handle is a claim of its own.
	if err := d.Claim(qaCCID + 2); err != nil {
		t.Errorf("claim of the second CCID interface: %v", err)
	}
	if err := d.Claim(qaCCID + 2); !errors.Is(err, ErrAlreadyClaimed) {
		t.Errorf("second claim of the second CCID interface: %v", err)
	}
}

// The refusals that come before: a closed handle says ErrClosed and a gone device ErrGone, not ErrAlreadyClaimed; an interface that is
// no CCID interface is ErrNoSuchInterface, held or not.
func TestQAAlreadyClaimedComesAfterTheOtherRefusals(t *testing.T) {
	t.Run("closed", func(t *testing.T) {
		d, _ := qaDevice(t, nil, &qaKernel{})
		qaClaim(t, d)
		_ = d.Close()
		if err := d.Claim(qaCCID); !errors.Is(err, ErrClosed) || errors.Is(err, ErrAlreadyClaimed) {
			t.Errorf("claim after Close: %v", err)
		}
	})
	t.Run("gone", func(t *testing.T) {
		k := &qaKernel{}
		d, _ := qaDevice(t, nil, k)
		qaClaim(t, d)
		k.answer = func(kcall) (uintptr, unix.Errno) { return 0, unix.ENODEV }
		_, _ = d.BulkOut(qaOut, []byte{1}, time.Second)
		if err := d.Claim(qaCCID); !errors.Is(err, ErrGone) || errors.Is(err, ErrAlreadyClaimed) {
			t.Errorf("claim on a gone device: %v", err)
		}
	})
	t.Run("not a CCID interface", func(t *testing.T) {
		d, _ := qaDevice(t, nil, &qaKernel{})
		qaClaim(t, d)
		for _, n := range []int{0, 99, -1} {
			if err := d.Claim(n); !errors.Is(err, ErrNoSuchInterface) || errors.Is(err, ErrAlreadyClaimed) {
				t.Errorf("claim %d: %v", n, err)
			}
		}
	})
}
