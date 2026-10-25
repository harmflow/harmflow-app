package wake

import (
	"testing"
	"time"
)

// clocks is a wall clock and a monotonic one the test moves apart, as a suspension does.
type clocks struct {
	wall time.Time
	mono time.Duration
}

func (c *clocks) read() (time.Time, time.Duration) { return c.wall, c.mono }

// awake moves both clocks, as time passes with the machine awake; asleep moves only the wall clock.
func (c *clocks) awake(d time.Duration)  { c.wall, c.mono = c.wall.Add(d), c.mono+d }
func (c *clocks) asleep(d time.Duration) { c.wall = c.wall.Add(d) }

// A gap between the wall clock and the monotonic one is a suspension; a slow check, slewing, and a
// clock set back are not.
func TestSuspensionIsTheGapBetweenClocks(t *testing.T) {
	c := &clocks{wall: time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)}
	m := newMonitor(c.read)
	c.awake(10 * time.Minute) // a check long delayed, with the machine awake
	if w := m.LastWake(); !w.IsZero() {
		t.Fatalf("a slow check read as a wake at %v", w)
	}
	c.awake(5 * time.Second)
	c.wall = c.wall.Add(Threshold / 2) // the wall clock stepped by NTP, within the threshold
	if w := m.LastWake(); !w.IsZero() {
		t.Fatalf("a clock adjustment read as a wake at %v", w)
	}
	c.awake(5 * time.Second)
	c.wall = c.wall.Add(-time.Hour) // the clock set back
	if w := m.LastWake(); !w.IsZero() {
		t.Fatalf("a clock set back read as a wake at %v", w)
	}

	c.awake(2 * time.Second)
	c.asleep(3 * time.Hour)
	c.awake(3 * time.Second)
	woke := c.wall
	if w := m.LastWake(); !w.Equal(woke) {
		t.Fatalf("after a sleep: %v, want %v", w, woke)
	}
	c.awake(5 * time.Second)
	if w := m.LastWake(); !w.Equal(woke) {
		t.Errorf("the next check moved the wake to %v", w)
	}

	c.asleep(Threshold + time.Second) // a nap just over the threshold
	c.awake(time.Second)
	if w := m.LastWake(); !w.Equal(c.wall) {
		t.Errorf("a short sleep: %v, want %v", w, c.wall)
	}
}

// The monitor of the running machine reads its two clocks from one instant, so no gap opens between them while the
// machine is awake.
func TestNoSuspensionWhileAwake(t *testing.T) {
	m := NewMonitor()
	for range 3 {
		time.Sleep(20 * time.Millisecond)
		if w := m.LastWake(); !w.IsZero() {
			t.Fatalf("a wake at %v with no sleep", w)
		}
	}
}
