package pivtest

import (
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
)

// TestQaOneCardFromSeveralGoroutinesKeepsItsInvariants: "A Card is safe for use by several goroutines, one command at a time." Eight
// goroutines run their own seeded sequences on one card, with power cycles, seeded objects and reads of the log in between. The answers may
// interleave; what must hold is what holds for any interleaving. Under -race this is also the test that nothing is read or written
// outside the lock.
func TestQaOneCardFromSeveralGoroutinesKeepsItsInvariants(t *testing.T) {
	for _, o := range []Options{{}, {ResponseMode: Short61xx, MaxPINTries: 15, MaxPUKTries: 15}, {NoExtendedAPDU: true, MaxPINTries: 2}} {
		c := New(o)
		c.PowerOn()
		var wg sync.WaitGroup
		var mu sync.Mutex
		var problems []string
		report := func(format string, args ...any) {
			mu.Lock()
			defer mu.Unlock()
			problems = append(problems, fmt.Sprintf(format, args...))
		}
		var answered, trips atomic.Int64
		const workers, steps = 8, 150
		for w := uint64(0); w < workers; w++ {
			wg.Add(1)
			go func(w uint64) {
				defer wg.Done()
				g := newQaGen(1000+w, nil)
				var snap []Entry
				for n := 0; n < steps; n++ {
					for _, op := range g.next() {
						switch op.kind {
						case qaOn:
							c.PowerOn()
						case qaOff:
							c.PowerOff()
						case qaPut:
							c.PutObject(op.id, op.body)
						default:
							resp, wait := c.Exchange(op.apdu)
							if wait != 0 {
								report("wait %v", wait)
							}
							if resp == nil {
								continue // the card was off at that moment
							}
							answered.Add(1)
							if len(resp) < 2 {
								report("answer %x to %x", resp, op.apdu)
							}
							if len(op.apdu) >= 2 && qaIsTripwire(op.apdu[1]) {
								trips.Add(1)
							}
						}
					}
					if n%10 == 0 {
						now := c.Log()
						if len(now) < len(snap) || (len(snap) > 0 && !reflect.DeepEqual(snap, now[:len(snap)])) {
							report("a log snapshot is not a prefix of the next")
						}
						snap = now
						_ = c.Forbidden()
					}
				}
			}(w)
		}
		wg.Wait()
		for _, p := range problems {
			t.Error(p)
		}
		if got := int64(len(c.Log())); got != answered.Load() {
			t.Errorf("%+v: %d log entries for %d answers", o, got, answered.Load())
		}
		if got := int64(len(c.Forbidden())); got != trips.Load() {
			t.Errorf("%+v: %d forbidden entries for %d forbidden commands", o, got, trips.Load())
		}
		if c.st().pin.left > c.st().pin.max || c.st().puk.left > c.st().puk.max {
			t.Errorf("%+v: a counter above its maximum: PIN %d/%d PUK %d/%d", o, c.st().pin.left, c.st().pin.max, c.st().puk.left, c.st().puk.max)
		}
		for _, e := range c.Log() {
			if e.Why != "" {
				t.Errorf("a log entry holds a reason: %+v", e)
			}
		}
		for _, e := range c.Forbidden() {
			if !qaIsTripwire(e.INS) || e.Why == "" {
				t.Errorf("forbidden entry %+v", e)
			}
		}
	}
}
