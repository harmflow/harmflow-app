// Package wake sees the machine wake from sleep or hibernation (FS-002 FR-004). While the machine is suspended its
// wall clock keeps time, and the monotonic clock Go reads stops: on macOS that clock is mach_absolute_time, which
// does not advance during sleep (Go 1.24's runtime, darwin/arm64 and darwin/amd64). A suspension shows as a gap
// opening between the two clocks; a slow check opens none, since both advance while the machine is awake.
package wake

import (
	"sync"
	"time"
)

// Threshold is the smallest gap that is a suspension: far above what clock slewing opens between two checks, and
// below any real sleep. A wall clock set forward by more also reads as one, as a jump the scheduler treats the same.
const Threshold = 5 * time.Second

// Monitor is the WakeMonitor port; it compares the two clocks between one call and the next.
type Monitor struct {
	read func() (wall time.Time, mono time.Duration)

	mu       sync.Mutex
	wall     time.Time
	mono     time.Duration
	lastWake time.Time
}

// NewMonitor watches this machine's clocks from now on.
func NewMonitor() *Monitor {
	start := time.Now()
	return newMonitor(func() (time.Time, time.Duration) {
		now := time.Now()
		return now.Round(0), now.Sub(start) // Round(0) drops the monotonic reading; Sub uses it
	})
}

func newMonitor(read func() (time.Time, time.Duration)) *Monitor {
	m := &Monitor{read: read}
	m.wall, m.mono = read()
	return m
}

// LastWake is the check at which the machine was last seen awake after a suspension. The wake itself happened at most
// one interval between checks before it, so a firing due in that interval counts as missed: the missed-run policy
// then never fails to cover a firing the suspension spanned.
func (m *Monitor) LastWake() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	wall, mono := m.read()
	if wall.Sub(m.wall)-(mono-m.mono) > Threshold {
		m.lastWake = wall
	}
	m.wall, m.mono = wall, mono
	return m.lastWake
}
