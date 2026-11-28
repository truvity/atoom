// Package usbccidtest is TEST-ONLY support for the tests of programs that read a card over the CCID layer of this module (the package
// ccid), and for the tests of the module itself. No program imports it, and it holds nothing that opens a device, reads the sysfs tree of
// the machine or talks to a card: a test of a program that uses it still gives the reader a fake Port.
//
// It holds one thing for now: a virtual clock, Clock, to give to ccid.Options.Clock. The reader never sleeps; it reads the clock to
// measure the budget of an exchange and the drain before the first exchange of a session, and every wait happens inside the Port. With a
// virtual clock that a fake Port moves, a test reaches an exact timeout or the exact edge of a budget at once, with no sleep and no
// dependence on how fast the machine runs.
//
// It needs only the standard library, has no build constraint and no cgo, and builds and runs on every system.
package usbccidtest

import (
	"sync"
	"time"
)

// Clock is a virtual clock. It starts where it is told and moves only when Advance or Set is called: reading it does not move it, and it
// never reads the machine's clock. It satisfies ccid.Clock. It is safe for use from several goroutines.
//
// The times it returns carry no monotonic reading (the start is stripped of it), so two readings compare by their wall time alone, as
// the times of a clock that was set by hand do.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// NewClock returns a Clock that reads start until it is moved.
func NewClock(start time.Time) *Clock {
	return &Clock{now: start.Round(0)}
}

// NewDefaultClock returns a Clock that starts at 2030-01-01T00:00:00Z: a fixed date that is no zero time, so that a test that forgets to
// give the clock to what it tests meets a clock that disagrees with the machine's.
func NewDefaultClock() *Clock {
	return NewClock(time.Date(2030, time.January, 1, 0, 0, 0, 0, time.UTC))
}

// Now returns the time of the clock. Two calls with no Advance or Set between them return the same time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward by d. A negative d moves it back; the ccid reader expects a clock that does not go back, so a test
// that does it means to.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// Set puts the clock at t, whether that is later or earlier than it was.
func (c *Clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t.Round(0)
}
