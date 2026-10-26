package app

import (
	"context"
	"strconv"
	"sync"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/procutil"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

// logEnded names on the execution's log, by pid and executable, each process that ending the attempt ended and that the
// harness or program left running when it exited on its own, and each one it ended outside the process group of its
// harness or program, which a signal to the group would have missed.
func (r *run) logEnded(ended []procutil.Ended) {
	for _, e := range ended {
		pid := strconv.Itoa(e.Pid)
		var text domain.Text
		switch {
		case e.LeftRunning:
			text = domain.T("process_left_ended", "ended {{executable}} (pid {{pid}}), which the harness or program left running",
				"executable", e.Executable, "pid", pid)
		case e.OutsideGroup:
			text = domain.T("process_ended", "ended {{executable}} (pid {{pid}}), which ran outside the process group",
				"executable", e.Executable, "pid", pid)
		default:
			continue
		}
		r.event(domain.Event{Type: domain.EventProcessEnded,
			Payload: map[string]any{"pid": e.Pid, "executable": e.Executable}}.Says(text))
	}
}

// leftHarness is the harness an interrupted execution's running attempt started, as its record names it, with the
// attempt it belongs to.
type leftHarness struct {
	process domain.HarnessProcess
	attempt int
	harness string
}

// endLeftHarnesses ends the harness each interrupted execution left, and every process it started, as cancelling the
// execution does: SIGTERM, then SIGKILL after the task's grace period. The executions' harnesses are ended at once, so
// the longest grace period bounds the wait, and the scheduler starts nothing before they are gone. Each execution's log
// names every process ended.
func (a *App) endLeftHarnesses(ctx context.Context, xs []*domain.Execution, left map[string]leftHarness) {
	ended := make([][]procutil.Ended, len(xs))
	var wg sync.WaitGroup
	for i, x := range xs {
		l, ok := left[x.ID]
		if !ok {
			continue
		}
		tree := procutil.Tree{Pid: l.process.Pid, Group: l.process.Group, Start: l.process.StartedAt, ExecutionID: x.ID}
		grace := a.cancelGrace(x.TaskID)
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			if ended[i], err = procutil.EndStoredTree(ctx, tree, grace); err != nil {
				a.log.Printf("ending the harness execution %s left running: %v", x.ID, err)
			}
		}()
	}
	wg.Wait()
	for i, x := range xs {
		l := left[x.ID]
		for _, e := range ended[i] {
			a.emit(domain.Event{Type: domain.EventProcessEnded, TaskID: x.TaskID, ExecutionID: x.ID, Attempt: l.attempt,
				Harness: l.harness, WorkItemID: x.WorkItem, Payload: map[string]any{"pid": e.Pid, "executable": e.Executable}}.Says(
				domain.T("process_interrupted_ended", "ended {{executable}} (pid {{pid}}), which the interrupted execution left running",
					"executable", e.Executable, "pid", strconv.Itoa(e.Pid))))
		}
	}
}
