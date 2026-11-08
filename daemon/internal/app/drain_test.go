package app

import (
	"errors"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// ownOutcomes are the final statuses an execution reaches on its own, interrupted left out.
var ownOutcomes = []domain.ExecutionStatus{domain.StatusCompleted, domain.StatusNoWork, domain.StatusFailed,
	domain.StatusCancelled, domain.StatusNeedsAttention}

// waitShutdownRequested waits until ShutdownRequested is closed, and fails the test when it stays open.
func (e *env) waitShutdownRequested() {
	e.t.Helper()
	if !e.poll(10*time.Second, e.shutdownRequested) {
		e.t.Fatal("ShutdownRequested is still open, want it closed")
	}
}

// conflictKey is the key of err when it is a CodeConflict *Error, and "" otherwise.
func conflictKey(err error) string {
	var ae *Error
	if !errors.As(err, &ae) || ae.Code != CodeConflict {
		return ""
	}
	return ae.Key
}

// A drain lets the running execution end on its own outcome, refuses every new firing meanwhile, manual or scheduled,
// shows how many executions it waits for and when its deadline passes, and asks main to stop the daemon once none is
// left.
func TestDrainEndsOnItsOwn(t *testing.T) {
	e := newEnv(t)
	e.create("busy", "")
	e.create("other", "")
	e.create("timed", "  schedule:\n    interval: 1h\n")
	e.claude.setScript(func(ports.RunRequest) string { return "sleep 1; echo 'END ok'" })
	r, err := e.app.RunTask("busy")
	if err != nil {
		t.Fatalf("RunTask(%q): %v", "busy", err)
	}
	e.wait(r.ExecutionID, domain.StatusRunning)

	if err := e.app.RequestDrain(time.Minute); err != nil {
		t.Fatalf("RequestDrain(1m) = %v, want nil", err)
	}
	want := DrainView{Executions: 1, DeadlineAt: e.clock.Now().Add(time.Minute)}
	if got, ok := e.app.Draining(); !ok || got.Executions != want.Executions || !got.DeadlineAt.Equal(want.DeadlineAt) {
		t.Errorf("Draining() = %+v, %v; want %+v, true", got, ok, want)
	}
	if e.app.AcceptingFirings() {
		t.Error("AcceptingFirings() = true during a drain, want false")
	}
	if _, err := e.app.RunTask("other"); conflictKey(err) != "daemon_stopping" {
		t.Errorf("RunTask(%q) during a drain = %v, want the daemon_stopping conflict", "other", err)
	}
	e.clock.Advance(61 * time.Minute) // the scheduled task's firing comes due
	e.app.Tick()
	if got := e.executions("timed"); len(got) != 0 {
		t.Errorf("executions of the scheduled task due during the drain: %d, want none", len(got))
	}
	if e.shutdownRequested() {
		t.Error("ShutdownRequested is closed while the execution still runs, want it open")
	}

	x := e.waitWithin(30*time.Second, r.ExecutionID, ownOutcomes...)
	if x.Status == domain.StatusInterrupted {
		t.Errorf("execution %s is %s after a drain, want its own outcome", x.ID, x.Status)
	}
	e.waitShutdownRequested()
	if got := e.app.ShutdownReason(); got.Key != "daemon_drained" {
		t.Errorf("ShutdownReason() = %q (%s), want daemon_drained", got.Key, got.Message)
	}
	if got := e.executions("other"); len(got) != 0 {
		t.Errorf("executions of the task refused during the drain: %d, want none", len(got))
	}
}

// A drain whose deadline passes with an execution still running asks main to stop the daemon; the stop cancels the
// execution, which ends interrupted, saying that the drain's deadline passed.
func TestDrainReachesItsDeadline(t *testing.T) {
	e := newEnv(t)
	id := e.runSlow("busy")

	if err := e.app.RequestDrain(200 * time.Millisecond); err != nil {
		t.Fatalf("RequestDrain(200ms) = %v, want nil", err)
	}
	e.waitShutdownRequested()
	reason := e.app.ShutdownReason()
	if reason.Key != "drain_deadline_passed" {
		t.Errorf("ShutdownReason() = %q (%s), want drain_deadline_passed", reason.Key, reason.Message)
	}
	if e.cancelMarked("busy") {
		t.Error("the running execution is marked for cancellation by the drain's deadline, want it interrupted")
	}
	e.app.Stop(reason) // as main does once ShutdownRequested is closed

	x, err := e.app.GetExecution(id)
	if err != nil {
		t.Fatal(err)
	}
	if x.Status != domain.StatusInterrupted {
		t.Errorf("execution %s is %s, want %s", id, x.Status, domain.StatusInterrupted)
	}
	var said []string
	for _, ev := range e.published(domain.TerminalEventFor(domain.StatusInterrupted)) {
		if ev.ExecutionID == id {
			said = append(said, ev.MessageKey)
		}
	}
	if len(said) != 1 || said[0] != "drain_deadline_passed" {
		t.Errorf("the execution's terminal events say %v, want one saying drain_deadline_passed", said)
	}
	if stopping := e.published(domain.EventDaemonStopping); len(stopping) != 1 || stopping[0].MessageKey != "drain_deadline_passed" {
		t.Errorf("daemon.stopping events: %+v, want one saying drain_deadline_passed", stopping)
	}
}

// During a drain, a stop without force is still refused while the execution runs, and a forced stop is accepted at
// once: it marks the execution for cancellation and asks main to stop the daemon at the user's request.
func TestForcedStopDuringADrain(t *testing.T) {
	e := newEnv(t)
	e.runSlow("busy")
	if err := e.app.RequestDrain(time.Hour); err != nil {
		t.Fatalf("RequestDrain(1h) = %v, want nil", err)
	}

	var ae *Error
	if err := e.app.RequestShutdown(false); !errors.As(err, &ae) || ae.Code != CodeExecutionsRunning {
		t.Errorf("RequestShutdown(false) during a drain = %v, want %s", err, CodeExecutionsRunning)
	}
	if e.shutdownRequested() {
		t.Error("ShutdownRequested is closed after a refused stop during a drain, want it open")
	}
	if err := e.app.RequestShutdown(true); err != nil {
		t.Fatalf("RequestShutdown(true) during a drain = %v, want nil", err)
	}
	if !e.shutdownRequested() {
		t.Error("ShutdownRequested is open after a forced stop during a drain, want it closed")
	}
	if got := e.app.ShutdownReason(); got.Key != "daemon_stop_requested" {
		t.Errorf("ShutdownReason() = %q, want daemon_stop_requested", got.Key)
	}
	if !e.cancelMarked("busy") {
		t.Error("the running execution is not marked for cancellation after a forced stop during a drain")
	}
}

// A drain discards no queued firing: it stays queued while the daemon drains, and starts at the next daemon start.
func TestQueuedFiringOutlivesADrain(t *testing.T) {
	e := newEnv(t)
	e.create("busy", "")
	e.claude.setScript(func(ports.RunRequest) string { return "sleep 1; echo 'END ok'" })
	first, err := e.app.RunTask("busy")
	if err != nil {
		t.Fatalf("RunTask(%q): %v", "busy", err)
	}
	e.wait(first.ExecutionID, domain.StatusRunning)
	second, err := e.app.RunTask("busy")
	if err != nil || second.Outcome != "queued" {
		t.Fatalf("a second RunTask(%q) = %+v, %v; want it queued", "busy", second, err)
	}

	if err := e.app.RequestDrain(time.Minute); err != nil {
		t.Fatalf("RequestDrain(1m) = %v, want nil", err)
	}
	e.waitWithin(30*time.Second, first.ExecutionID, ownOutcomes...)
	e.waitShutdownRequested()
	if x, _ := e.app.GetExecution(second.ExecutionID); x.Status != domain.StatusQueued {
		t.Fatalf("the queued execution is %s once the drain ended, want %s", x.Status, domain.StatusQueued)
	}

	n := e.restart(t)
	n.waitWithin(30*time.Second, second.ExecutionID, ownOutcomes...)
}

// A drain with nothing running asks main to stop the daemon at once, with the default deadline; a second drain changes
// nothing, and a drain asked of a daemon already asked to stop is refused.
func TestDrainWithNothingRunning(t *testing.T) {
	e := newEnv(t)
	if err := e.app.RequestDrain(0); err != nil {
		t.Fatalf("RequestDrain(0) = %v, want nil", err)
	}
	e.waitShutdownRequested()
	want := e.clock.Now().Add(DefaultDrainDeadline)
	if got, ok := e.app.Draining(); !ok || got.Executions != 0 || !got.DeadlineAt.Equal(want) {
		t.Errorf("Draining() = %+v, %v; want no execution and the deadline at %s", got, ok, want)
	}
	if got := e.app.ShutdownReason(); got.Key != "daemon_drained" {
		t.Errorf("ShutdownReason() = %q, want daemon_drained", got.Key)
	}
	if err := e.app.RequestDrain(time.Second); err != nil {
		t.Errorf("a second RequestDrain(1s) = %v, want nil", err)
	}
	if got, _ := e.app.Draining(); !got.DeadlineAt.Equal(want) {
		t.Errorf("Draining() after a second drain = %+v, want the first one's deadline %s", got, want)
	}

	stopped := newEnv(t)
	if err := stopped.app.RequestShutdown(false); err != nil {
		t.Fatalf("RequestShutdown(false) = %v, want nil", err)
	}
	if err := stopped.app.RequestDrain(0); conflictKey(err) != "daemon_stopping" {
		t.Errorf("RequestDrain(0) after a stop = %v, want the daemon_stopping conflict", err)
	}
	if _, ok := stopped.app.Draining(); ok {
		t.Error("Draining() after a refused drain = true, want false")
	}
}
