//go:build linux && (amd64 || arm64)

package usbccid

import (
	"errors"
	"math"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The guards that rule the handle: the descriptors read at Open decide which interface is claimed and which endpoint is used, and a
// call that they refuse is refused before the kernel is asked. Every test here counts the calls of the scripted kernel.

// wantRefusal is the error a bulk call on endpoint ep gets in the made-up composite device when the interfaces in claimed are claimed:
// nil when the call is allowed, ErrNotClaimed for a bulk endpoint of an unclaimed CCID interface, and ErrBadEndpoint for anything else
// (a wrong direction, an interrupt endpoint, an endpoint of another class of interface, an alternate setting, an endpoint that is not
// in the descriptors).
func wantRefusal(ep uint8, in bool, claimed ...int) error {
	if (ep&0x80 != 0) != in {
		return ErrBadEndpoint
	}
	owner := map[uint8]int{qaOut: qaCCID, qaIn: qaCCID, qaOut2: qaCCID2, qaIn2: qaCCID2}
	n, ok := owner[ep]
	if !ok {
		return ErrBadEndpoint
	}
	for _, c := range claimed {
		if c == n {
			return nil
		}
	}
	return ErrNotClaimed
}

// Every one of the 256 endpoint bytes, in both directions, with nothing claimed, with the first CCID interface claimed and with both:
// the kernel is asked for exactly the allowed calls and for no other.
func TestQAEveryEndpointByteIsHeldToTheClaims(t *testing.T) {
	for _, claimed := range [][]int{nil, {qaCCID}, {qaCCID2}, {qaCCID, qaCCID2}} {
		k := &qaKernel{}
		d, _ := qaDevice(t, nil, k)
		for _, n := range claimed {
			if err := d.Claim(n); err != nil {
				t.Fatal(err)
			}
		}
		base, allowed := k.count(), 0
		for ep := range 256 {
			e := uint8(ep)
			_, errOut := d.BulkOut(e, []byte{1}, time.Second)
			_, errIn := d.BulkIn(e, make([]byte, 1), time.Second)
			for _, c := range []struct {
				name string
				got  error
				want error
			}{{"BulkOut", errOut, wantRefusal(e, false, claimed...)}, {"BulkIn", errIn, wantRefusal(e, true, claimed...)}} {
				if c.want == nil {
					allowed++
					if c.got != nil {
						t.Errorf("claimed %v: %s(%#02x): %v, want it allowed", claimed, c.name, e, c.got)
					}
					continue
				}
				if !errors.Is(c.got, c.want) || errors.Is(c.got, ErrGone) || errors.Is(c.got, ErrBusy) {
					t.Errorf("claimed %v: %s(%#02x): %v, want %v", claimed, c.name, e, c.got, c.want)
				}
			}
		}
		if got := k.count() - base; got != allowed || allowed != 2*len(claimed) {
			t.Errorf("claimed %v: the kernel was asked %d times for %d allowed calls (want %d)", claimed, got, allowed, 2*len(claimed))
		}
		if _, err := d.Descriptors(); err != nil {
			t.Errorf("the refusals latched: %v", err)
		}
	}
}

// A bulk call before any claim makes no kernel call; the endpoint of the other interface (a vendor interface with bulk endpoints of its
// own) makes none either, also after the CCID interface is claimed; the interrupt endpoint given to a bulk call and an endpoint the
// descriptors do not hold are refused; after Release the endpoints are refused again.
func TestQAGuardsByName(t *testing.T) {
	k := &qaKernel{}
	d, _ := qaDevice(t, nil, k)
	buf := make([]byte, 8)
	call := func(name string, err, want error) {
		t.Helper()
		if !errors.Is(err, want) || errors.Is(err, ErrBusy) || errors.Is(err, ErrGone) || errors.Is(err, ErrTimeout) {
			t.Errorf("%s: %v, want %v", name, err, want)
		}
	}
	out := func(ep uint8) error { _, e := d.BulkOut(ep, buf, time.Second); return e }
	in := func(ep uint8) error { _, e := d.BulkIn(ep, buf, time.Second); return e }

	call("write before any claim", out(qaOut), ErrNotClaimed)
	call("read before any claim", in(qaIn), ErrNotClaimed)
	call("the vendor interface before any claim", out(qaVOut), ErrBadEndpoint)
	if k.count() != 0 {
		t.Fatalf("%d kernel calls before any claim", k.count())
	}
	call("claim of the vendor interface", d.Claim(qaVendor), ErrNoSuchInterface)
	call("claim of the HID interface", d.Claim(qaHID), ErrNoSuchInterface)
	if k.count() != 0 {
		t.Fatalf("a refused claim reached the kernel: %d calls", k.count())
	}
	if err := d.Claim(qaCCID); err != nil {
		t.Fatal(err)
	}
	base := k.count()
	call("write to the endpoint of the vendor interface", out(qaVOut), ErrBadEndpoint)
	call("read from the endpoint of the vendor interface", in(qaVIn), ErrBadEndpoint)
	call("the claim of the vendor interface after the CCID claim", d.Claim(qaVendor), ErrNoSuchInterface)
	call("the endpoint of the other CCID interface", out(qaOut2), ErrNotClaimed)
	call("write to an IN address", out(qaIn), ErrBadEndpoint)
	call("read from an OUT address", in(qaOut), ErrBadEndpoint)
	call("an interrupt endpoint to BulkIn", in(qaIntr), ErrBadEndpoint)
	call("an interrupt endpoint of the HID interface", in(qaHIn), ErrBadEndpoint)
	call("an interrupt OUT endpoint of the HID interface", out(qaHOut), ErrBadEndpoint)
	call("an endpoint of the alternate setting", out(qaAltOut), ErrBadEndpoint)
	call("endpoint 0 in", in(0x80), ErrBadEndpoint)
	call("endpoint 0 out", out(0x00), ErrBadEndpoint)
	call("an endpoint the descriptors do not hold", in(0x8F), ErrBadEndpoint)
	if got := k.count() - base; got != 0 {
		t.Errorf("%d kernel calls for refused calls", got)
	}
	if err := out(qaOut); err != nil {
		t.Errorf("the claimed endpoint: %v", err)
	}
	if err := in(qaIn); err != nil {
		t.Errorf("the claimed endpoint: %v", err)
	}
	if err := d.Release(qaCCID); err != nil {
		t.Fatal(err)
	}
	base = k.count()
	call("write after the release", out(qaOut), ErrNotClaimed)
	call("read after the release", in(qaIn), ErrNotClaimed)
	if k.count() != base {
		t.Error("a call after the release reached the kernel")
	}
	if err := d.Claim(qaCCID); err != nil { // a new claim gives the endpoints back
		t.Fatal(err)
	}
	if err := out(qaOut); err != nil {
		t.Errorf("after a new claim: %v", err)
	}
}

// Claim of every number from -3 to 300 and of the extremes: exactly the CCID interfaces of the descriptors, with a kernel call each.
func TestQAClaimTableOverEveryNumber(t *testing.T) {
	k := &qaKernel{}
	d, _ := qaDevice(t, nil, k)
	ok := 0
	nums := []int{math.MinInt, math.MaxInt, math.MaxInt32, math.MinInt32}
	for n := -3; n <= 300; n++ {
		nums = append(nums, n)
	}
	for _, n := range nums {
		err := d.Claim(n)
		if n == qaCCID || n == qaCCID2 {
			ok++
			if err != nil {
				t.Errorf("claim %d: %v", n, err)
			}
			continue
		}
		if !errors.Is(err, ErrNoSuchInterface) {
			t.Errorf("claim %d: %v, want ErrNoSuchInterface", n, err)
		}
	}
	if k.count() != ok {
		t.Errorf("%d kernel calls for %d allowed claims", k.count(), ok)
	}
}

// A handle whose descriptors say nothing usable claims nothing and moves nothing, whatever it is asked.
func TestQAHandleWithoutUsableDescriptors(t *testing.T) {
	good := qaDefaultDesc()
	for name, desc := range map[string][]byte{
		"empty":                        {},
		"a device descriptor only":     good[:18],
		"cut inside the configuration": good[:len(good)-5],
		"no CCID interface":            qaBlob(1, 2, qaCfg(1, qaIface(0, 0, qaClassHID, qaEP(qaHIn, 3)), qaIface(2, 0, 0xFF, qaEP(qaVOut, 2), qaEP(qaVIn, 2)))),
		"garbage":                      {1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25},
	} {
		t.Run(name, func(t *testing.T) {
			k := &qaKernel{}
			d, _ := qaDevice(t, desc, k)
			for n := -1; n < 260; n++ {
				if err := d.Claim(n); !errors.Is(err, ErrNoSuchInterface) {
					t.Fatalf("claim %d: %v", n, err)
				}
			}
			for ep := range 256 {
				if _, err := d.BulkOut(uint8(ep), []byte{1}, time.Second); err == nil {
					t.Fatalf("write to %#02x got through", ep)
				}
				if _, err := d.BulkIn(uint8(ep), make([]byte, 1), time.Second); err == nil {
					t.Fatalf("read from %#02x got through", ep)
				}
			}
			if k.count() != 0 {
				t.Errorf("%d kernel calls", k.count())
			}
		})
	}
	// A CCID class interface that has no bulk endpoint is claimed (the class decides), and no endpoint is then usable.
	k := &qaKernel{}
	d, _ := qaDevice(t, qaBlob(1, 2, qaCfg(1, qaIface(0, 0, qaClassCCID, qaEP(0x81, 3)))), k)
	if err := d.Claim(0); err != nil {
		t.Fatal(err)
	}
	for ep := range 256 {
		if _, err := d.BulkIn(uint8(ep), make([]byte, 1), time.Second); err == nil {
			t.Fatalf("read from %#02x got through", ep)
		}
	}
}

// Candidate.Open holds what it opened to the candidate: the check is verify, run on the handle that Open returned. Every mismatch
// is ErrWrongDevice; nothing is asked of the kernel.
func TestQAVerify(t *testing.T) {
	k := &qaKernel{}
	d, _ := qaDevice(t, nil, k)
	ok := Candidate{VendorID: qaVID, ProductID: qaPID, Interface: qaCCID, BulkOut: qaOut, BulkIn: qaIn, InterruptIn: qaIntr, NodePath: "/made/up/001/002", PortPath: "1-2"}
	if err := d.verify(ok); err != nil {
		t.Fatalf("the matching candidate: %v", err)
	}
	second := ok
	second.Interface, second.BulkOut, second.BulkIn, second.InterruptIn = qaCCID2, qaOut2, qaIn2, qaIntr2
	if err := d.verify(second); err != nil {
		t.Fatalf("the second CCID interface: %v", err)
	}
	mut := func(f func(c *Candidate)) Candidate { c := ok; f(&c); return c }
	for name, c := range map[string]Candidate{
		"vendor":                             mut(func(c *Candidate) { c.VendorID++ }),
		"product":                            mut(func(c *Candidate) { c.ProductID++ }),
		"vendor and product swapped":         mut(func(c *Candidate) { c.VendorID, c.ProductID = c.ProductID, c.VendorID }),
		"an interface that is not there":     mut(func(c *Candidate) { c.Interface = 9 }),
		"a negative interface":               mut(func(c *Candidate) { c.Interface = -1 }),
		"an interface of another class":      mut(func(c *Candidate) { c.Interface, c.BulkOut, c.BulkIn = qaVendor, qaVOut, qaVIn }),
		"the HID interface":                  mut(func(c *Candidate) { c.Interface = qaHID }),
		"bulk out that is not there":         mut(func(c *Candidate) { c.BulkOut = 0x0E }),
		"bulk in that is not there":          mut(func(c *Candidate) { c.BulkIn = 0x8E }),
		"the endpoints swapped":              mut(func(c *Candidate) { c.BulkOut, c.BulkIn = c.BulkIn, c.BulkOut }),
		"the endpoints of another interface": mut(func(c *Candidate) { c.BulkOut, c.BulkIn = qaOut2, qaIn2 }),
		"a bulk out with a direction bit":    mut(func(c *Candidate) { c.BulkOut = qaOut | 0x80 }),
		"a bulk in without a direction bit":  mut(func(c *Candidate) { c.BulkIn = qaIn &^ 0x80 }),
		"the interrupt endpoint as bulk":     mut(func(c *Candidate) { c.BulkIn = qaIntr }),
		"the alternate setting":              mut(func(c *Candidate) { c.BulkOut, c.BulkIn = qaAltOut, qaAltIn }),
		"zero endpoints":                     mut(func(c *Candidate) { c.BulkOut, c.BulkIn = 0, 0 }),
	} {
		if err := d.verify(c); !errors.Is(err, ErrWrongDevice) || errors.Is(err, ErrGone) || errors.Is(err, ErrBusy) {
			t.Errorf("%s: %v, want ErrWrongDevice", name, err)
		}
	}
	if k.count() != 0 {
		t.Errorf("verify asked the kernel %d times", k.count())
	}
	if _, err := d.Descriptors(); err != nil {
		t.Errorf("verify latched: %v", err)
	}
}

// B4: counts, sizes and time.

// A count above the buffer is a layer below that cannot be trusted: ErrGone, latched, in both directions, from one byte above to the
// largest value; a count equal to the buffer or below it is the normal answer.
func TestQACountAboveTheBufferLatches(t *testing.T) {
	counts := []func(n int) uintptr{func(n int) uintptr { return uintptr(n) + 1 }, func(int) uintptr { return 1 << 20 }, func(int) uintptr { return 1 << 40 }, func(int) uintptr { return ^uintptr(0) }}
	for i, over := range counts {
		for _, dir := range []string{"BulkIn", "BulkOut"} {
			k := &qaKernel{answer: func(c kcall) (uintptr, unix.Errno) {
				if c.req == reqBulk {
					return over(int(c.bt.Len)), 0
				}
				return 0, 0
			}}
			d, _ := qaDevice(t, nil, k)
			qaClaim(t, d)
			var err error
			if dir == "BulkIn" {
				_, err = d.BulkIn(qaIn, make([]byte, 8), time.Second)
			} else {
				_, err = d.BulkOut(qaOut, make([]byte, 8), time.Second)
			}
			if !errors.Is(err, ErrGone) || errors.Is(err, ErrTimeout) || errors.Is(err, ErrBusy) {
				t.Fatalf("%s, count variant %d: %v, want ErrGone", dir, i, err)
			}
			before := k.count()
			if _, err := d.BulkIn(qaIn, make([]byte, 8), time.Second); !errors.Is(err, ErrGone) {
				t.Errorf("not latched: %v", err)
			}
			if _, err := d.Descriptors(); !errors.Is(err, ErrGone) {
				t.Errorf("descriptors after the latch: %v", err)
			}
			if err := d.Claim(qaCCID2); !errors.Is(err, ErrGone) {
				t.Errorf("claim after the latch: %v", err)
			}
			if k.count() != before {
				t.Error("the latch let a call through")
			}
			if err := d.Release(qaCCID); err != nil {
				t.Errorf("release: %v", err)
			}
			if err := d.Close(); err != nil {
				t.Errorf("close: %v", err)
			}
		}
	}
	// A zero-length write that the kernel counts as one byte.
	k := &qaKernel{answer: func(c kcall) (uintptr, unix.Errno) {
		if c.req == reqBulk {
			return 1, 0
		}
		return 0, 0
	}}
	d, _ := qaDevice(t, nil, k)
	qaClaim(t, d)
	if _, err := d.BulkOut(qaOut, nil, time.Second); !errors.Is(err, ErrGone) {
		t.Errorf("a count of 1 for an empty write: %v", err)
	}
	// Equal and below are fine.
	for _, r := range []uintptr{0, 1, 7, 8} {
		k := &qaKernel{answer: func(c kcall) (uintptr, unix.Errno) {
			if c.req == reqBulk {
				return r, 0
			}
			return 0, 0
		}}
		d, _ := qaDevice(t, nil, k)
		qaClaim(t, d)
		if n, err := d.BulkIn(qaIn, make([]byte, 8), time.Second); err != nil || n != int(r) {
			t.Errorf("count %d for a buffer of 8: %d %v", r, n, err)
		}
	}
}
