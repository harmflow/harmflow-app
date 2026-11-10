package app

import (
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

// DefaultDrainDeadline is how long a draining stop waits for the running executions when its request sets no deadline.
const DefaultDrainDeadline = 30 * time.Minute

// drainState is a draining stop under way: when its deadline passes, and whether it has passed.
type drainState struct {
	until   time.Time
	expired bool
}

// DrainView is a draining stop under way, as the local API shows it: how many executions it still waits for, and when
// its deadline passes.
type DrainView struct {
	Executions int       `json:"executions"`
	DeadlineAt time.Time `json:"deadlineAt"`
}

// RequestDrain asks the daemon to stop once the executions it runs have ended, or once deadline has passed, whichever
// comes first; a deadline of zero or less is DefaultDrainDeadline. From then on no firing starts, scheduled or
// manual, and the queued ones stay queued for the next start. The executions that run go on to their own outcome; those
// still running at the deadline are cancelled as a stop cancels them, and end interrupted. A second drain keeps the
// first one's deadline; a drain asked of a daemon already stopping is CodeConflict.
func (a *App) RequestDrain(deadline time.Duration) error {
	if deadline <= 0 {
		deadline = DefaultDrainDeadline
	}
	a.mu.Lock()
	if a.drain != nil {
		a.mu.Unlock()
		return nil
	}
	if !a.acceptingLocked() || a.stopping {
		a.mu.Unlock()
		return newError(CodeConflict, domain.T("daemon_stopping", "the daemon is stopping"))
	}
	a.accepting = false
	a.drain = &drainState{until: a.now().Add(deadline)}
	n := len(a.running)
	a.mu.Unlock()
	a.log.Printf("draining: waiting for %d running executions, at most %s", n, deadline)
	// The deadline runs on real time, as an execution's timeout does; the API shows it on the daemon's clock.
	a.wg.Go(func() { a.watchDrain(deadline) })
	return nil
}

// watchDrain waits for the running executions to end, then asks main to stop the daemon. Once deadline has passed it
// marks the drain expired and asks main to stop it all the same, which cancels the executions left. It returns when
// Stop begins, whatever it waited for.
func (a *App) watchDrain(deadline time.Duration) {
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	for {
		a.mu.Lock()
		var done chan struct{}
		for _, h := range a.running {
			done = h.done
			break
		}
		a.mu.Unlock()
		if done == nil {
			a.requestStop(domain.T("daemon_drained", "the running executions ended, so the daemon stopped"))
			return
		}
		select {
		case <-done:
		case <-timer.C:
			a.mu.Lock()
			a.drain.expired = true
			a.mu.Unlock()
			a.requestStop(drainDeadlinePassed())
			return
		case <-a.stopBegun:
			return
		}
	}
}

// Draining reports the draining stop under way, and whether one is.
func (a *App) Draining() (DrainView, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.drain == nil || a.stopping {
		return DrainView{}, false
	}
	return DrainView{Executions: len(a.running), DeadlineAt: a.drain.until}, true
}

// requestStop asks main to stop the daemon, for reason; only the first request counts. It takes no lock, so a forced
// stop asks at once.
func (a *App) requestStop(reason domain.Text) {
	a.shutdownOnce.Do(func() {
		a.shutdownReason.Store(&reason)
		close(a.ShutdownRequested)
	})
}

// ShutdownReason is why ShutdownRequested was closed: the user's request, or the end of a drain.
func (a *App) ShutdownReason() domain.Text {
	if reason := a.shutdownReason.Load(); reason != nil && reason.Key != "" {
		return *reason
	}
	return stopRequested()
}

// stopRequested is why the daemon stops when the user asked it to.
func stopRequested() domain.Text {
	return domain.T("daemon_stop_requested", "stopped at the user's request")
}

// drainDeadlinePassed is why a drain stopped the daemon with executions still running.
func drainDeadlinePassed() domain.Text {
	return domain.T("drain_deadline_passed", "the drain's deadline passed before the running executions ended")
}

// stoppedReason is why an execution the stopping daemon ended was interrupted: the drain's deadline when one passed,
// else the stop itself.
func (a *App) stoppedReason() domain.Text {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.drain != nil && a.drain.expired {
		return drainDeadlinePassed()
	}
	return daemonStopped()
}
