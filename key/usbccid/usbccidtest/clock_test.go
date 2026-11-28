package usbccidtest

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestClockStartsWhereItIsToldAndMovesOnlyWhenAdvanced(t *testing.T) {
	start := time.Date(1999, time.December, 31, 23, 59, 59, 7, time.UTC)
	c := NewClock(start)
	for range 1000 { // reading never moves it
		if got := c.Now(); !got.Equal(start) {
			t.Fatalf("Now = %v, want %v", got, start)
		}
	}
	c.Advance(3 * time.Millisecond)
	if got, want := c.Now(), start.Add(3*time.Millisecond); !got.Equal(want) {
		t.Errorf("after Advance(3ms): %v, want %v", got, want)
	}
	c.Advance(-3 * time.Millisecond)
	if got := c.Now(); !got.Equal(start) {
		t.Errorf("after Advance(-3ms): %v, want %v", got, start)
	}
	c.Advance(time.Nanosecond)
	if got, want := c.Now(), start.Add(time.Nanosecond); !got.Equal(want) {
		t.Errorf("after Advance(1ns): %v, want %v", got, want)
	}
}

var defaultStart = time.Date(2030, time.January, 1, 0, 0, 0, 0, time.UTC)

func TestDefaultClockStartsAt2030(t *testing.T) {
	want := time.Date(2030, time.January, 1, 0, 0, 0, 0, time.UTC)
	if got := NewDefaultClock().Now(); !got.Equal(want) {
		t.Errorf("NewDefaultClock().Now() = %v, want %v", got, want)
	}
}

func TestClockSetMovesBothWays(t *testing.T) {
	c := NewDefaultClock()
	later := defaultStart.Add(48 * time.Hour)
	earlier := defaultStart.Add(-48 * time.Hour)
	for _, to := range []time.Time{later, earlier, defaultStart, {}} {
		c.Set(to)
		if got := c.Now(); !got.Equal(to) {
			t.Errorf("after Set(%v): %v", to, got)
		}
	}
}

// The clock never carries the monotonic reading of a time that came from the machine's clock, however it was given.
func TestClockDropsTheMonotonicReading(t *testing.T) {
	machine := time.Now() // only to have a time that carries a monotonic reading; its value decides nothing
	if !strings.Contains(machine.String(), "m=") {
		t.Skip("this runtime's time.Now carries no monotonic reading")
	}
	c := NewClock(machine)
	if s := c.Now().String(); strings.Contains(s, "m=") {
		t.Errorf("NewClock kept the monotonic reading: %s", s)
	}
	c.Set(machine)
	if s := c.Now().String(); strings.Contains(s, "m=") {
		t.Errorf("Set kept the monotonic reading: %s", s)
	}
	c.Advance(time.Second)
	if s := c.Now().String(); strings.Contains(s, "m=") {
		t.Errorf("Advance gave a monotonic reading: %s", s)
	}
}

// Advance from several goroutines loses no step; run under -race.
func TestClockAdvanceFromSeveralGoroutines(t *testing.T) {
	const workers, steps = 16, 500
	c := NewDefaultClock()
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range steps {
				c.Advance(time.Nanosecond)
				_ = c.Now()
			}
		}()
	}
	wg.Wait()
	if got, want := c.Now(), defaultStart.Add(workers*steps*time.Nanosecond); !got.Equal(want) {
		t.Errorf("after %d steps: %v, want %v", workers*steps, got, want)
	}
}

// Set, Advance and Now from several goroutines at once; run under -race. Every reading is a time that some call put there.
func TestClockSetAndAdvanceAndNowFromSeveralGoroutines(t *testing.T) {
	const workers, rounds = 8, 300
	c := NewDefaultClock()
	lo, hi := defaultStart, defaultStart.Add(24*time.Hour)
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range rounds {
				switch w % 3 {
				case 0:
					c.Set(lo)
				case 1:
					c.Set(hi)
				default:
					c.Advance(0)
				}
				if got := c.Now(); got.Before(lo) || got.After(hi) {
					t.Errorf("a reading of %v is outside [%v, %v]", got, lo, hi)
					return
				}
			}
		}()
	}
	wg.Wait()
	if got := c.Now(); !got.Equal(lo) && !got.Equal(hi) {
		t.Errorf("the clock ends at %v, which no Set put there", got)
	}
}
