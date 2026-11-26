//go:build linux && !android && (amd64 || arm64)

package usbccid

import (
	"errors"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// 6. The semantics that doc.go, errors.go and port.go write down, one test for each sentence. 8. The sentences of "What the layer
// above must not assume" that the behaviour of this layer can show, each with the scripted kernel, so that the author of the CCID
// layer can read the behaviour here.

// "After Close, Release and Close return nil and every other call returns ErrClosed (also when the device is gone as well)."
func TestQAAfterCloseReleaseAndCloseAreNilAndEverythingElseIsErrClosed(t *testing.T) {
	for _, gone := range []bool{false, true} {
		k := &qaKernel{answer: func(c kcall) (uintptr, unix.Errno) {
			if gone && c.req == reqBulk {
				return 0, unix.ENODEV
			}
			return 0, 0
		}}
		d, _ := qaDevice(t, nil, k)
		qaClaim(t, d)
		if gone {
			if _, err := d.BulkIn(qaIn, make([]byte, 1), time.Second); !errors.Is(err, ErrGone) {
				t.Fatal(err)
			}
		}
		if err := d.Close(); err != nil {
			t.Fatal(err)
		}
		before := k.count()
		for range 2 {
			if err := d.Release(qaCCID); err != nil {
				t.Errorf("gone=%v: Release after Close: %v", gone, err)
			}
			if err := d.Close(); err != nil {
				t.Errorf("gone=%v: Close after Close: %v", gone, err)
			}
			if _, err := d.Descriptors(); !errors.Is(err, ErrClosed) || errors.Is(err, ErrGone) {
				t.Errorf("gone=%v: Descriptors: %v", gone, err)
			}
			if err := d.Claim(qaCCID); !errors.Is(err, ErrClosed) || errors.Is(err, ErrGone) {
				t.Errorf("gone=%v: Claim: %v", gone, err)
			}
			if err := d.Claim(77); !errors.Is(err, ErrClosed) || errors.Is(err, ErrNoSuchInterface) {
				t.Errorf("gone=%v: Claim of a number that is not there: %v", gone, err)
			}
			if _, err := d.BulkOut(qaOut, []byte{1}, time.Second); !errors.Is(err, ErrClosed) || errors.Is(err, ErrGone) {
				t.Errorf("gone=%v: BulkOut: %v", gone, err)
			}
			if _, err := d.BulkIn(0x55, nil, time.Second); !errors.Is(err, ErrClosed) || errors.Is(err, ErrBadEndpoint) || errors.Is(err, ErrBadLength) {
				t.Errorf("gone=%v: BulkIn with arguments that are wrong: %v", gone, err)
			}
		}
		if k.count() != before {
			t.Errorf("gone=%v: a call after Close reached the kernel", gone)
		}
	}
}

// "After the device is gone, Release and Close return nil and every other call returns ErrGone", without a kernel call, whatever
// the arguments (an argument that is wrong is not named: the latch comes first).
func TestQAAfterGoneReleaseAndCloseAreNilAndEverythingElseIsErrGone(t *testing.T) {
	k := &qaKernel{answer: func(c kcall) (uintptr, unix.Errno) {
		if c.req == reqBulk {
			return 0, unix.ESHUTDOWN
		}
		return 0, 0
	}}
	d, _ := qaDevice(t, nil, k)
	qaClaim(t, d)
	if err := d.Claim(qaCCID2); err != nil {
		t.Fatal(err)
	}
	if _, err := d.BulkOut(qaOut, []byte{1}, time.Second); !errors.Is(err, ErrGone) || !errors.Is(err, unix.ESHUTDOWN) {
		t.Fatal(err)
	}
	before := k.count()
	for range 2 {
		if _, err := d.Descriptors(); !errors.Is(err, ErrGone) || errors.Is(err, ErrClosed) {
			t.Errorf("Descriptors: %v", err)
		}
		for _, n := range []int{qaCCID, 77, -1} {
			if err := d.Claim(n); !errors.Is(err, ErrGone) || errors.Is(err, ErrNoSuchInterface) {
				t.Errorf("Claim(%d): %v", n, err)
			}
		}
		if _, err := d.BulkOut(0x55, make([]byte, maxTransfer+1), time.Second); !errors.Is(err, ErrGone) || errors.Is(err, ErrBadEndpoint) {
			t.Errorf("BulkOut: %v", err)
		}
		if _, err := d.BulkIn(0x55, nil, time.Second); !errors.Is(err, ErrGone) || errors.Is(err, ErrBadLength) {
			t.Errorf("BulkIn: %v", err)
		}
		if err := d.Release(qaCCID); err != nil {
			t.Errorf("Release: %v", err)
		}
	}
	if err := d.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if k.count() != before {
		t.Errorf("%d kernel calls after the latch, in Release and Close included", k.count()-before)
	}
	// ErrClosed wins after Close.
	if _, err := d.BulkIn(qaIn, make([]byte, 1), time.Second); !errors.Is(err, ErrClosed) || errors.Is(err, ErrGone) {
		t.Errorf("after Close of a gone device: %v", err)
	}
}

// "Descriptors, Release and Close wait behind a transfer in progress", and so does every other call (port.go: a call waits behind a
// transfer in progress, which ends within its timeout).
func TestQACallsWaitBehindATransferInProgress(t *testing.T) {
	for _, name := range []string{"Descriptors", "Release", "Close", "Claim", "BulkOut"} {
		inside, release := make(chan struct{}), make(chan struct{})
		var first atomic.Bool
		k := &qaKernel{answer: func(c kcall) (uintptr, unix.Errno) {
			if c.req == reqBulk && first.CompareAndSwap(false, true) {
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
		go func() { _, _ = d.BulkIn(qaIn, make([]byte, 4), time.Hour) }()
		<-inside
		returned := make(chan struct{})
		go func() {
			defer close(returned)
			switch name {
			case "Descriptors":
				_, _ = d.Descriptors()
			case "Release":
				_ = d.Release(qaCCID)
			case "Close":
				_ = d.Close()
			case "Claim":
				_ = d.Claim(qaCCID2)
			case "BulkOut":
				_, _ = d.BulkOut(qaOut, []byte{1}, time.Second)
			}
		}()
		select {
		case <-returned:
			t.Errorf("%s returned while a transfer was in progress", name)
		case <-time.After(100 * time.Millisecond):
		}
		close(release)
		select {
		case <-returned:
		case <-time.After(10 * time.Second):
			t.Errorf("%s did not return after the transfer ended", name)
		}
	}
}

// "A Device that is never closed keeps its file descriptor until the process ends: there is no finalizer", and the package never
// retries, never detaches, never resets: read from the source.
func TestQANoFinalizerNoRetryLoop(t *testing.T) {
	for _, f := range []string{"device_linux.go", "discover_linux.go", "layout_linux.go"} {
		src, err := os.ReadFile(f) //nolint:gosec // a source file of this package
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parser.ParseFile(token.NewFileSet(), f, src, 0); err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"SetFinalizer", "AddCleanup", "EINTR", "time.Sleep", "time.After"} {
			for _, line := range strings.Split(string(src), "\n") {
				l := strings.TrimSpace(line)
				if strings.HasPrefix(l, "//") {
					continue
				}
				if strings.Contains(l, bad) {
					t.Errorf("%s mentions %s: %s", f, bad, l)
				}
			}
		}
	}
}

// 8. What the layer above must not assume.

// "A transfer that timed out is abandoned, and what it had already moved is lost. A late answer may arrive on the next read": the
// layer asks the kernel for nothing between the two calls (no flush, no drain, no reset) and hands the late bytes to the next read.
func TestQAAfterATimeoutTheNextReadMayReturnALateAnswer(t *testing.T) {
	var reads atomic.Int32
	k := &qaKernel{
		answer: func(c kcall) (uintptr, unix.Errno) {
			if c.req != reqBulk || c.bt.Ep&0x80 == 0 {
				return uintptr(c.bt.Len), 0
			}
			if reads.Add(1) == 1 {
				return 0, unix.ETIMEDOUT // the card was slow
			}
			return 4, 0 // the late answer to the first command, now delivered
		},
		fill: func(buf []byte) { copy(buf, "LATE") },
	}
	d, _ := qaDevice(t, nil, k)
	qaClaim(t, d)
	if _, err := d.BulkOut(qaOut, []byte("command-1"), time.Second); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	if _, err := d.BulkIn(qaIn, buf, 10*time.Millisecond); !errors.Is(err, ErrTimeout) {
		t.Fatalf("the slow read: %v", err)
	}
	callsBefore := k.count()
	n, err := d.BulkIn(qaIn, buf, 10*time.Millisecond)
	if err != nil || string(buf[:n]) != "LATE" {
		t.Fatalf("the next read: %q %v: it returns whatever the kernel hands over", buf[:n], err)
	}
	if k.count()-callsBefore != 1 {
		t.Errorf("%d kernel calls for one read", k.count()-callsBefore)
	}
	for _, c := range k.snapshot() {
		if c.req != reqBulk && c.req != reqClaim {
			t.Errorf("a request that is not a transfer or a claim: %#x (no drain, flush or reset)", c.req)
		}
	}
}

// "A stall is final for this handle": the layer never clears a halt. A stalled endpoint's error is passed through (EPIPE) on every
// call, the handle is not latched, and the only requests the layer ever makes are the three it has.
func TestQAAStallIsNeverCleared(t *testing.T) {
	stalled := true
	k := &qaKernel{answer: func(c kcall) (uintptr, unix.Errno) {
		if c.req == reqBulk && stalled {
			return 0, unix.EPIPE
		}
		return uintptr(c.bt.Len), 0
	}}
	d, _ := qaDevice(t, nil, k)
	qaClaim(t, d)
	for range 3 {
		_, err := d.BulkIn(qaIn, make([]byte, 8), time.Second)
		if !errors.Is(err, unix.EPIPE) || errors.Is(err, ErrGone) || errors.Is(err, ErrTimeout) {
			t.Fatalf("a stalled endpoint: %v", err)
		}
		_, err = d.BulkOut(qaOut, []byte{1}, time.Second)
		if !errors.Is(err, unix.EPIPE) {
			t.Fatalf("a stalled endpoint: %v", err)
		}
	}
	if got := k.countReq(reqBulk); got != 6 {
		t.Errorf("%d bulk requests for 6 calls (no retry)", got)
	}
	if k.countReq(reqBulk)+k.countReq(reqClaim) != k.count() {
		t.Error("the layer made a request that clears the halt")
	}
	// The handle is not marked dead: the layer does nothing about the stall, and the endpoint works again only if the device lets it.
	stalled = false
	if _, err := d.BulkIn(qaIn, make([]byte, 8), time.Second); err != nil {
		t.Errorf("after the device let go: %v", err)
	}
}

// "A read buffer must hold the largest message expected: a larger one is lost with an overflow error": the error is the kernel's
// EOVERFLOW, it does not latch, the layer does not retry with a bigger buffer, and the next read returns the next message.
func TestQAABufferThatIsTooSmallIsAnOverflowAndTheMessageIsLost(t *testing.T) {
	messages := [][]byte{[]byte("a message of 20 bytes"), []byte("next")}
	var served atomic.Int32
	k := &qaKernel{
		answer: func(c kcall) (uintptr, unix.Errno) {
			if c.req != reqBulk {
				return 0, 0
			}
			m := messages[served.Add(1)-1]
			if int(c.bt.Len) < len(m) {
				return 0, unix.EOVERFLOW // the kernel consumed the packet
			}
			return uintptr(len(m)), 0
		},
		fill: func(buf []byte) { copy(buf, messages[1]) },
	}
	d, _ := qaDevice(t, nil, k)
	qaClaim(t, d)
	small := make([]byte, 8)
	n, err := d.BulkIn(qaIn, small, time.Second)
	if !errors.Is(err, unix.EOVERFLOW) || errors.Is(err, ErrGone) || n != 0 {
		t.Fatalf("a buffer that is too small: %d %v", n, err)
	}
	if got := k.countReq(reqBulk); got != 1 {
		t.Errorf("%d bulk calls: the layer retried", got)
	}
	n, err = d.BulkIn(qaIn, make([]byte, 64), time.Second)
	if err != nil || string(messages[1]) != "next" || n != 4 {
		t.Errorf("the read after the overflow: %d %v: the first message is lost, the next one is read", n, err)
	}
}

// "A read can return zero bytes", and "a message whose length is an exact multiple of the packet size is followed by an empty packet,
// which the next read returns as zero bytes": a count of zero is a success, not an error and not a timeout.
func TestQAAReadCanReturnZeroBytes(t *testing.T) {
	script := []uintptr{64, 0, 5, 0}
	var i atomic.Int32
	k := &qaKernel{answer: func(c kcall) (uintptr, unix.Errno) {
		if c.req != reqBulk {
			return 0, 0
		}
		return script[i.Add(1)-1], 0
	}}
	d, _ := qaDevice(t, nil, k)
	qaClaim(t, d)
	buf := make([]byte, 512)
	for j, want := range script {
		n, err := d.BulkIn(qaIn, buf, time.Second)
		if err != nil || n != int(want) {
			t.Errorf("read %d: %d %v, want %d bytes and no error", j, n, err, want)
		}
	}
}

// "A timed-out BulkOut may have sent part of a message", "BulkOut returns a count to compare": a short count is a success with the
// count the kernel gave, a timeout reports no count (0), and the layer does not send the rest.
func TestQAABulkOutCountIsForTheCallerToCompare(t *testing.T) {
	var step atomic.Int32
	k := &qaKernel{answer: func(c kcall) (uintptr, unix.Errno) {
		if c.req != reqBulk {
			return 0, 0
		}
		switch step.Add(1) {
		case 1:
			return 10, 0 // fewer bytes than asked
		default:
			return 0, unix.ETIMEDOUT // some of it may have gone
		}
	}}
	d, _ := qaDevice(t, nil, k)
	qaClaim(t, d)
	msg := make([]byte, 64)
	n, err := d.BulkOut(qaOut, msg, time.Second)
	if err != nil || n != 10 {
		t.Fatalf("a short write: %d %v: the count is the caller's to compare", n, err)
	}
	if got := k.countReq(reqBulk); got != 1 {
		t.Errorf("%d bulk requests: the layer sent the rest", got)
	}
	n, err = d.BulkOut(qaOut, msg, time.Millisecond)
	if !errors.Is(err, ErrTimeout) || n != 0 {
		t.Errorf("a timed-out write: %d %v: no count is reported, though part may have gone", n, err)
	}
	if got := k.countReq(reqBulk); got != 2 {
		t.Errorf("%d bulk requests: the layer retried", got)
	}
}

// "The overflow and stall errors do not latch": covered above for each; here every errno that is not fatal, once, then a good call.
func TestQANonFatalErrorsDoNotLatch(t *testing.T) {
	next := unix.Errno(0)
	k := &qaKernel{answer: func(c kcall) (uintptr, unix.Errno) {
		if c.req == reqBulk {
			return uintptr(c.bt.Len), next
		}
		return 0, 0
	}}
	d, _ := qaDevice(t, nil, k)
	qaClaim(t, d)
	for _, e := range []unix.Errno{unix.EOVERFLOW, unix.EPIPE, unix.ETIMEDOUT, unix.EINTR, unix.EAGAIN, unix.ENOMEM, unix.EINVAL, unix.ENOENT, unix.EBUSY} {
		next = e
		if _, err := d.BulkIn(qaIn, make([]byte, 4), time.Second); !errors.Is(err, e) || errors.Is(err, ErrGone) {
			t.Fatalf("%v: %v", e, err)
		}
		next = 0
		if n, err := d.BulkIn(qaIn, make([]byte, 4), time.Second); err != nil || n != 4 {
			t.Fatalf("after %v: %d %v", e, n, err)
		}
	}
}

// "Only alternate setting 0", "the interrupt endpoint is never used", "the descriptors are a snapshot from Open": the layer knows
// nothing of the alternate setting and nothing of the interrupt endpoint, and does not read the descriptors again.
func TestQAOnlyAlternateSettingZeroNoInterruptSnapshotOfDescriptors(t *testing.T) {
	k := &qaKernel{}
	d, path := qaDevice(t, nil, k)
	qaClaim(t, d)
	for _, ep := range []uint8{qaAltOut, qaAltIn, qaIntr, qaIntr2, qaHIn} {
		_, err1 := d.BulkIn(ep|0x80, make([]byte, 4), time.Second)
		_, err2 := d.BulkOut(ep&^0x80, []byte{1}, time.Second)
		if !errors.Is(err1, ErrBadEndpoint) && !errors.Is(err1, ErrNotClaimed) || !errors.Is(err2, ErrBadEndpoint) && !errors.Is(err2, ErrNotClaimed) {
			t.Errorf("endpoint %#02x: %v %v", ep, err1, err2)
		}
	}
	// The descriptors are the snapshot of Open: a node that changes afterwards changes nothing, and nothing is read again.
	before, _ := d.Descriptors()
	if err := os.WriteFile(path, qaBlob(9, 9), 0o600); err != nil {
		t.Fatal(err)
	}
	after, _ := d.Descriptors()
	if string(before) != string(after) || len(after) != len(qaDefaultDesc()) {
		t.Error("the descriptors changed after Open")
	}
	if err := d.Claim(qaCCID2); err != nil {
		t.Errorf("the layout of Open is still in force: %v", err)
	}
	// "A reset of the device by another process is not noticed until a call fails."
	k.mu.Lock()
	k.answer = func(c kcall) (uintptr, unix.Errno) {
		if c.req == reqBulk {
			return 0, unix.ENODEV
		}
		return 0, 0
	}
	k.mu.Unlock()
	if _, err := d.Descriptors(); err != nil {
		t.Errorf("Descriptors after an outside reset: %v", err)
	}
	if err := d.Release(qaCCID2); err != nil {
		t.Errorf("Release after an outside reset: %v", err)
	}
	if _, err := d.BulkIn(qaIn, make([]byte, 4), time.Second); !errors.Is(err, ErrGone) {
		t.Errorf("the first call that fails says ErrGone: %v", err)
	}
}

// "The maximum packet size of an endpoint is not read here": a read buffer of any size from 1 to 64 KiB goes to the kernel as it is.
func TestQAReadBufferSizeIsNotMatchedToAPacketSize(t *testing.T) {
	k := &qaKernel{}
	d, _ := qaDevice(t, nil, k)
	qaClaim(t, d)
	for _, n := range []int{1, 7, 63, 64, 65, 511, 512, 513, 4096, maxTransfer} {
		if got, err := d.BulkIn(qaIn, make([]byte, n), time.Second); err != nil || got != n {
			t.Errorf("a buffer of %d: %d %v", n, got, err)
		}
		if k.last(t).bt.Len != uint32(n) { //nolint:gosec // at most 64 KiB
			t.Errorf("a buffer of %d went to the kernel as %d", n, k.last(t).bt.Len)
		}
	}
}

// "A read whose buffer is exactly as long as the message ends before the empty packet, and the next read returns it as zero bytes; a
// read with a longer buffer ends on the empty packet in the same call": the layer passes the counts the kernel gives and makes one
// kernel call for each read, whatever the buffer; it neither waits for the empty packet nor reads it on its own.
func TestQAMessageOfAWholeNumberOfPacketsAndTheEmptyPacket(t *testing.T) {
	// The kernel's answers for a 64-byte message on an endpoint of 64-byte packets: with a buffer of 64 the read ends when the buffer is
	// full, and the empty packet is left for the next read; with a larger buffer the read ends on the empty packet, in the same call.
	k := &qaKernel{answer: func(c kcall) (uintptr, unix.Errno) {
		if c.req != reqBulk {
			return 0, 0
		}
		return 64, 0
	}}
	d, _ := qaDevice(t, nil, k)
	qaClaim(t, d)
	base := k.count()
	if n, err := d.BulkIn(qaIn, make([]byte, 512), time.Second); err != nil || n != 64 {
		t.Fatalf("a read with a longer buffer: %d %v", n, err)
	}
	if k.count()-base != 1 {
		t.Errorf("%d kernel calls for one read: the layer waits for or reads the empty packet by itself", k.count()-base)
	}
	empty := false
	k.mu.Lock()
	k.answer = func(c kcall) (uintptr, unix.Errno) {
		if c.req != reqBulk {
			return 0, 0
		}
		if !empty {
			empty = true
			return 64, 0
		}
		return 0, 0
	}
	k.mu.Unlock()
	if n, err := d.BulkIn(qaIn, make([]byte, 64), time.Second); err != nil || n != 64 {
		t.Fatalf("a read with a buffer as long as the message: %d %v", n, err)
	}
	if n, err := d.BulkIn(qaIn, make([]byte, 64), time.Second); err != nil || n != 0 {
		t.Fatalf("the next read: %d %v, want the empty packet as zero bytes", n, err)
	}
}
