package ccid

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvity/atoom/key/usbccid/usbccidtest"
)

// This file holds the tests of Options.Clock. Every clock in it is virtual (usbccidtest.Clock), the fake Port moves it from inside a
// call, and no test sleeps or reads the machine's clock to decide an outcome. The expected values are exact: a timeout that the Port
// was asked for is compared to the nanosecond.

// portCall is one call of the fake Port that has a timeout: a bulk write or a bulk read.
type portCall struct {
	call string // "out" or "in"
	to   time.Duration
}

// portCalls lists every bulk call the fake saw, in order, with the timeout it was given.
func portCalls(f *fakeDevice) []portCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []portCall
	i := 0
	for _, c := range f.calls {
		if c == "in" || c == "out" {
			out = append(out, portCall{c, f.timeouts[i]})
			i++
		}
	}
	return out
}

// callsSince returns the bulk calls made after the first n.
func callsSince(f *fakeDevice, n int) []portCall { return portCalls(f)[n:] }

func sameCalls(t *testing.T, got, want []portCall) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d Port calls %v, want %d: %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Port call %d is %v, want %v (all: %v)", i, got[i], want[i], got)
		}
	}
}

func reads(n int, to time.Duration) []portCall {
	out := make([]portCall, n)
	for i := range out {
		out[i] = portCall{"in", to}
	}
	return out
}

// scriptClock is a virtual clock that moves itself while the Reader reads it: it counts the readings since arm, and from the at-th one
// on (only that one unless each is set) it first advances by by. It stands for time that passes between two of the Reader's readings,
// which a fake Port cannot reach, because the Reader reads the clock where it makes no Port call.
type scriptClock struct {
	*usbccidtest.Clock
	mu   sync.Mutex
	n    int
	at   int
	by   time.Duration
	each bool
}

func (c *scriptClock) arm(at int, by time.Duration, each bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n, c.at, c.by, c.each = 0, at, by, each
}

func (c *scriptClock) disarm() { c.arm(0, 0, false) }

func (c *scriptClock) Now() time.Time {
	c.mu.Lock()
	c.n++
	if c.at > 0 && (c.n == c.at || c.each && c.n > c.at) {
		c.Advance(c.by)
	}
	c.mu.Unlock()
	return c.Clock.Now()
}

// ---- the default and the option ----

func TestClockNilMeansTheRealClock(t *testing.T) {
	r, _, err := openFake(Options{}, cardHandler(echoSW))
	if err != nil {
		t.Fatal(err)
	}
	if r.clock != nil {
		t.Errorf("a Reader opened with no Clock holds %T", r.clock)
	}
	// time.Now carries a monotonic reading ("m=" in its text); the default clock is time.Now itself, and keeps it.
	if s := r.now().String(); !strings.Contains(s, "m=") {
		t.Errorf("the default clock's reading has no monotonic reading: %s", s)
	}
	if s := (&Reader{}).now().String(); !strings.Contains(s, "m=") {
		t.Errorf("the zero Reader's reading has no monotonic reading: %s", s)
	}
	if _, err := r.PowerOn(testBudget); err != nil {
		t.Fatal(err)
	}
	got, err := r.Transmit(apduSelect, testBudget)
	if err != nil {
		t.Fatal(err)
	}
	if want := echoSW(apduSelect); string(got) != string(want) {
		t.Errorf("response % X, want % X", got, want)
	}
}

func TestClockOptionIsTheOneTheReaderReads(t *testing.T) {
	start := time.Date(2041, time.March, 5, 6, 7, 8, 9, time.UTC)
	clk := usbccidtest.NewClock(start)
	r, _, err := openFake(Options{Clock: clk}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.now(); !got.Equal(start) {
		t.Errorf("the Reader reads %v, want the clock's %v", got, start)
	}
	clk.Advance(time.Hour)
	if got := r.now(); !got.Equal(start.Add(time.Hour)) {
		t.Errorf("after Advance(1h) the Reader reads %v", got)
	}
}

// A clock that stands far from the machine's time, and never moves, changes no outcome of a plain session, and every timeout the Port
// is asked for is then exactly what the budget and the transfer timeout say. Had any path kept reading the machine's clock (time.Until,
// time.Since), a deadline that lies in the year 1 or the year 9000 would be hours or centuries off and the budget would fail.
func TestClockFrozenFarFromTheMachineChangesNoOutcome(t *testing.T) {
	starts := map[string]time.Time{
		"year 1":    time.Date(1, time.January, 2, 0, 0, 0, 0, time.UTC),
		"1970":      time.Unix(0, 0).UTC(),
		"2030":      time.Date(2030, time.January, 1, 0, 0, 0, 0, time.UTC),
		"year 9000": time.Date(9000, time.January, 1, 0, 0, 0, 0, time.UTC),
	}
	for name, start := range starts {
		t.Run(name, func(t *testing.T) {
			clk := usbccidtest.NewClock(start)
			r, f := openPowered(t, Options{Clock: clk}, cardHandler(echoSW))
			// PowerOn with testBudget (10 s, which equals DefaultTransferTimeout): the drain, then two exchanges of a write and a read.
			var want []portCall
			want = append(want, reads(2, DrainReadTimeout)...)
			want = append(want, portCall{"out", DefaultTransferTimeout}, portCall{"in", DefaultTransferTimeout})
			want = append(want, portCall{"out", DefaultTransferTimeout}, portCall{"in", DefaultTransferTimeout})
			sameCalls(t, portCalls(f), want)

			n := len(portCalls(f))
			got, err := r.Transmit(apduSelect, 5*time.Second) // under the transfer timeout: the budget cuts both transfers
			if err != nil {
				t.Fatal(err)
			}
			if w := echoSW(apduSelect); string(got) != string(w) {
				t.Errorf("response % X, want % X", got, w)
			}
			sameCalls(t, callsSince(f, n), []portCall{{"out", 5 * time.Second}, {"in", 5 * time.Second}})
			if !clk.Now().Equal(start) {
				t.Errorf("the Reader moved the clock to %v", clk.Now())
			}
		})
	}
}

// ---- the drain ----

// The timeout of a drain read is what is left of MaxDrainTime when that is under DrainReadTimeout. The clock is moved by the fake Port
// during the first read, so that the second read starts with exactly rem left.
func TestClockDrainReadTimeoutIsExactlyWhatIsLeftOfMaxDrainTime(t *testing.T) {
	const window = DrainReadTimeout
	cases := []struct {
		name string
		rem  time.Duration // what is left of MaxDrainTime at the second turn of the drain
	}{
		{"1 ns left", time.Nanosecond},
		{"5 ms left", 5 * time.Millisecond},
		{"one under the window", window - 1},
		{"exactly the window", window},
		{"one over the window", window + 1},
		{"all of it", MaxDrainTime - window}, // after a first read that took no time but the window
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clk := usbccidtest.NewDefaultClock()
			f := newFake(descSpec{}, cardHandler(echoSW))
			f.onIn = func(f *fakeDevice) {
				if f.nIn == 1 {
					clk.Advance(MaxDrainTime - c.rem)
				}
			}
			r, err := Open(f, Options{Clock: clk})
			if err != nil {
				t.Fatal(err)
			}
			_, err = r.SlotStatus(testBudget)
			if c.rem >= window {
				// A whole window: the second read is a whole window of quiet, and the drain ends. Then the exchange runs.
				if err != nil {
					t.Fatalf("SlotStatus: %v", err)
				}
				sameCalls(t, portCalls(f)[:2], reads(2, window))
				return
			}
			// Under a window: a read cut by MaxDrainTime proves no quiet and is not counted, and the clock stands still, so the same
			// remainder is asked again until MaxDrainReads.
			lostWith(t, err, ErrProtocol)
			want := append(reads(1, window), reads(MaxDrainReads-1, c.rem)...)
			sameCalls(t, portCalls(f), want)
			if f.nOut != 0 {
				t.Errorf("%d writes before the drain ended", f.nOut)
			}
			untouched(t, r, f, err)
		})
	}
}

// Nothing left of MaxDrainTime ends the session, and no read is started.
func TestClockDrainWithNothingLeftLosesTheSession(t *testing.T) {
	t.Run("after one read", func(t *testing.T) {
		clk := usbccidtest.NewDefaultClock()
		f := newFake(descSpec{}, cardHandler(echoSW))
		f.onIn = func(f *fakeDevice) {
			if f.nIn == 1 {
				clk.Advance(MaxDrainTime) // exactly all of it
			}
		}
		r, err := Open(f, Options{Clock: clk})
		if err != nil {
			t.Fatal(err)
		}
		_, err = r.SlotStatus(testBudget)
		lostWith(t, err, ErrProtocol)
		sameCalls(t, portCalls(f), reads(1, DrainReadTimeout))
		untouched(t, r, f, err)
	})
	// The clock moves between the Reader's readings, with no Port call between them: the first reading of the call is the budget's,
	// the next one starts the drain, and from it each reading is a MaxDrainTime later than the one before.
	t.Run("before the first read", func(t *testing.T) {
		clk := &scriptClock{Clock: usbccidtest.NewDefaultClock()}
		clk.arm(2, MaxDrainTime, true)
		r, f, err := openFake(Options{Clock: clk}, cardHandler(echoSW))
		if err != nil {
			t.Fatal(err)
		}
		_, err = r.SlotStatus(testBudget)
		lostWith(t, err, ErrProtocol)
		if got := portCalls(f); len(got) != 0 {
			t.Errorf("the Port was called: %v", got)
		}
		assertWiped(t, r, "after the loss")
		clk.disarm()
		untouched(t, r, f, err)
	})
	// One nanosecond short of nothing: a read is made, with that nanosecond as its timeout, and the next turn finds nothing left.
	t.Run("one nanosecond left before the first read", func(t *testing.T) {
		clk := &scriptClock{Clock: usbccidtest.NewDefaultClock()}
		clk.arm(2, MaxDrainTime-1, true)
		r, f, err := openFake(Options{Clock: clk}, cardHandler(echoSW))
		if err != nil {
			t.Fatal(err)
		}
		_, err = r.SlotStatus(testBudget)
		lostWith(t, err, ErrProtocol)
		sameCalls(t, portCalls(f), reads(1, time.Nanosecond))
		clk.disarm()
		untouched(t, r, f, err)
	})
}

// ---- the budget ----

// advancing answers like a card, and moves the clock by *by when an XfrBlock arrives: time that passes while the card works.
func advancing(clk *usbccidtest.Clock, by *time.Duration) func(*fakeDevice, []byte) {
	return func(f *fakeDevice, m []byte) {
		if m[0] == msgXfrBlock {
			clk.Advance(*by)
		}
		cardHandler(echoSW)(f, m)
	}
}

// The timeout of a transfer is the transfer timeout, cut to exactly what is left of the budget; with nothing left the exchange fails with
// the budget error, and as the write was made the session is lost.
func TestClockTransferTimeoutIsCutToWhatIsLeftOfTheBudget(t *testing.T) {
	const budget = 30 * time.Second // three transfer timeouts
	const xfer = DefaultTransferTimeout
	cases := []struct {
		name  string
		spent time.Duration // time that passes between the write and the read
		want  time.Duration // the timeout the read is asked for; zero when the budget is gone
	}{
		{"nothing spent", 0, xfer},
		{"left is exactly the transfer timeout", budget - xfer, xfer},
		{"left is the transfer timeout less 1 ns", budget - xfer + 1, xfer - 1},
		{"5 ms left", budget - 5*time.Millisecond, 5 * time.Millisecond},
		{"1 ns left", budget - 1, time.Nanosecond},
		{"nothing left", budget, 0},
		{"1 ns over", budget + 1, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clk := usbccidtest.NewDefaultClock()
			var spent time.Duration
			r, f := openPowered(t, Options{Clock: clk}, advancing(clk, &spent))
			n := len(portCalls(f))
			spent = c.spent
			got, err := r.Transmit(apduSelect, budget)
			if c.want == 0 {
				// The first Port call was made: the session is lost, with the budget as its cause, and the read never starts.
				lostWith(t, err, ErrBudgetExhausted)
				sameCalls(t, callsSince(f, n), []portCall{{"out", xfer}})
				assertWiped(t, r, "after the loss")
				untouched(t, r, f, err)
				return
			}
			if err != nil {
				t.Fatalf("Transmit: %v", err)
			}
			if w := echoSW(apduSelect); string(got) != string(w) {
				t.Errorf("response % X, want % X", got, w)
			}
			sameCalls(t, callsSince(f, n), []portCall{{"out", xfer}, {"in", c.want}})
		})
	}
}

// The budget is checked before a transfer, never after one. A card that sends a time extension makes a second read, and the second read
// is where the edge is: with 1 ns left it is made, with nothing left the session is lost. A last read that ends exactly on the budget, or
// later, is a success: the clock is not read again after the answer arrived.
func TestClockBudgetEdgeOfAnExchange(t *testing.T) {
	const budget = 2 * time.Second // under the transfer timeout: every timeout is cut by the budget
	cases := []struct {
		name       string
		first      time.Duration // time that passes during the first read (it answers with a time extension)
		last       time.Duration // time that passes during the second read (the answer)
		wantOK     bool
		wantSecond time.Duration // the timeout of the second read, when it is made
	}{
		{"1 ns left at the second read", budget - 1, 0, true, 1},
		{"the answer arrives exactly on the budget", budget - 1, 1, true, 1},
		{"the answer arrives one tick after the budget", budget - 1, 2, true, 1},
		{"nothing left at the second read", budget, 0, false, 0},
		{"over by 1 ns at the second read", budget + 1, 0, false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clk := usbccidtest.NewDefaultClock()
			h := func(f *fakeDevice, m []byte) {
				if m[0] != msgXfrBlock {
					cardHandler(echoSW)(f, m)
					return
				}
				f.send(msg(rspDataBlock, m[6], stTimeExt, 0, 0, nil)) // the card is busy: a time extension
				cardHandler(echoSW)(f, m)                             // and then its answer
			}
			r, f := openPowered(t, Options{Clock: clk}, h)
			n, base := len(portCalls(f)), f.nIn
			f.onIn = func(f *fakeDevice) {
				switch f.nIn - base {
				case 1:
					clk.Advance(c.first)
				case 2:
					clk.Advance(c.last)
				}
			}
			got, err := r.Transmit(apduSelect, budget)
			if !c.wantOK {
				lostWith(t, err, ErrBudgetExhausted)
				sameCalls(t, callsSince(f, n), []portCall{{"out", budget}, {"in", budget}})
				untouched(t, r, f, err)
				return
			}
			if err != nil {
				t.Fatalf("Transmit: %v", err)
			}
			if w := echoSW(apduSelect); string(got) != string(w) {
				t.Errorf("response % X, want % X", got, w)
			}
			sameCalls(t, callsSince(f, n), []portCall{{"out", budget}, {"in", budget}, {"in", c.wantSecond}})
		})
	}
}

// Where the budget runs out decides whether the session is kept. Before the first Port call of the call it is kept and only the call is
// refused; after it, it is lost. This pins what the code does today.
func TestClockBudgetEdgeAndTheSession(t *testing.T) {
	t.Run("gone before the first Port call of an exchange: refused, session kept", func(t *testing.T) {
		clk := &scriptClock{Clock: usbccidtest.NewDefaultClock()}
		r, f := openPowered(t, Options{Clock: clk}, cardHandler(echoSW))
		const budget = time.Second
		clk.arm(2, budget, false) // the budget's reading is the first of the call; the next one finds it spent
		mark := f.mark()
		_, err := r.Transmit(apduSelect, budget)
		refusedOnBudget(t, r, f, err, mark)
		clk.disarm()
		if _, err := r.Transmit(apduSelect, testBudget); err != nil {
			t.Errorf("Transmit after the refusal: %v", err)
		}
	})
	t.Run("gone before the first read of the drain: refused, session kept", func(t *testing.T) {
		clk := &scriptClock{Clock: usbccidtest.NewDefaultClock()}
		r, f, err := openFake(Options{Clock: clk}, cardHandler(echoSW))
		if err != nil {
			t.Fatal(err)
		}
		const budget = time.Second
		clk.arm(2, budget, false) // the reading that starts the drain is the one that finds the budget spent
		mark := f.mark()
		_, err = r.SlotStatus(budget)
		refusedOnBudget(t, r, f, err, mark)
		clk.disarm()
		if _, err := r.SlotStatus(testBudget); err != nil {
			t.Errorf("SlotStatus after the refusal: %v", err)
		}
	})
	t.Run("gone after the first read of the drain: session lost", func(t *testing.T) {
		clk := usbccidtest.NewDefaultClock()
		const budget = 100 * time.Millisecond // under MaxDrainTime, over the two windows
		f := newFake(descSpec{}, cardHandler(echoSW))
		f.onIn = func(f *fakeDevice) {
			if f.nIn == 1 {
				clk.Advance(budget)
			}
		}
		r, err := Open(f, Options{Clock: clk})
		if err != nil {
			t.Fatal(err)
		}
		_, err = r.SlotStatus(budget)
		lostWith(t, err, ErrBudgetExhausted)
		sameCalls(t, portCalls(f), reads(1, DrainReadTimeout))
		untouched(t, r, f, err)
	})
	// The first exchange of a session needs two whole drain windows. The edge is exact: 2 x DrainReadTimeout is enough, and one
	// nanosecond less is refused before any Port call, with the session kept.
	t.Run("the budget of a first exchange: exactly two windows is enough, one nanosecond less is refused", func(t *testing.T) {
		const need = 2 * DrainReadTimeout
		clk := usbccidtest.NewDefaultClock()
		r, f, err := openFake(Options{Clock: clk}, cardHandler(echoSW))
		if err != nil {
			t.Fatal(err)
		}
		mark := f.mark()
		_, err = r.SlotStatus(need - 1)
		refusedOnBudget(t, r, f, err, mark)
		if _, err = r.SlotStatus(need); err != nil {
			t.Fatalf("SlotStatus with exactly two windows: %v", err)
		}
		// The clock stands still, so after the two windows of the drain the whole budget is left for the write and the read.
		sameCalls(t, portCalls(f), append(reads(2, DrainReadTimeout), portCall{"out", need}, portCall{"in", need}))
	})
}
