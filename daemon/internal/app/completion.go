package app

import (
	"strconv"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

// runAgainAfter applies the completion policy of x's task once x has ended, while its task still counts as running, so
// startQueued starts what it fired once the task is free. Under run_again an execution that ended completed fires its
// task again at once: a firing already waiting takes the run again, coalesced, and otherwise a new execution waits for
// startQueued, which starts it through queued and preparing as any firing. The policy is read from the task's
// definition current now, so a task that was disabled, deleted, made invalid, or set back to wait_for_schedule fires
// none, and neither does a daemon that accepts no new firing, as while it drains. The execution that ends completed at
// the run-again limit fires none, records that it reached it, and tells the user.
func (a *App) runAgainAfter(x *domain.Execution) {
	a.mu.Lock()
	e := a.tasks[x.TaskID]
	if x.Status != domain.StatusCompleted || e == nil || e.deletePending || e.invalid != nil || !e.doc.IsEnabled() ||
		e.doc.IsScript() || !a.acceptingLocked() {
		a.mu.Unlock()
		return
	}
	doc := e.doc
	completion := doc.Effective().Spec.Completion
	if completion.Policy != domain.CompletionRunAgain {
		a.mu.Unlock()
		return
	}
	limit := *completion.RunAgainLimit
	if x.RunAgainCount >= limit {
		a.mu.Unlock()
		a.emit(domain.Event{Type: domain.EventRunAgainLimitReached, TaskID: x.TaskID, ExecutionID: x.ID,
			Severity: domain.SeverityWarning, Payload: map[string]any{"runAgainLimit": limit}}.Says(runAgainLimitText(limit)))
		return
	}
	count := x.RunAgainCount + 1
	now := a.now()
	r := firingRecord{ScheduledFor: now, ProcessedAt: now, Trigger: domain.TriggerRunAgain}
	var waiting *domain.Execution
	for _, w := range a.executions {
		if w.TaskID == x.TaskID && w.Status == domain.StatusQueued {
			waiting = w
		}
	}
	if waiting != nil {
		// The waiting firing keeps its own trigger and takes the run again's place in the sequence, so its count goes on
		// from x's instead of starting again.
		waiting.FollowsExecutionID, waiting.RunAgainCount = x.ID, count
		waiting.Changed()
		r.Outcome, r.ExecutionID = "coalesced", waiting.ID
		a.recordFiringLocked(x.TaskID, r)
		a.mu.Unlock()
		a.persistExecution(waiting)
		a.emit(domain.Event{Type: domain.EventFiringCoalesced, TaskID: x.TaskID, ExecutionID: waiting.ID,
			Payload: map[string]any{"trigger": string(domain.TriggerRunAgain), "followsExecutionId": x.ID,
				"runAgainCount": count}}.Says(
			domain.T("firing_coalesced", "a firing already waits; this one coalesced into it")))
		a.saveSchedule()
		return
	}
	next := domain.NewExecution(doc, domain.TriggerRunAgain, nil, now)
	next.FollowsExecutionID, next.RunAgainCount = x.ID, count
	a.addExecutionLocked(next)
	r.Outcome, r.ExecutionID = "started", next.ID
	a.recordFiringLocked(x.TaskID, r)
	status := next.Status
	a.mu.Unlock()
	a.persistExecution(next)
	a.emit(domain.Event{Type: domain.EventExecutionCreated, TaskID: x.TaskID, ExecutionID: next.ID,
		Payload: map[string]any{"trigger": string(domain.TriggerRunAgain), "status": string(status),
			"followsExecutionId": x.ID, "runAgainCount": count}})
	a.saveSchedule()
}

// runAgainLimitText says that a task reached its run-again limit; the desktop picks the plural form from the count, and
// so does the en-US text.
func runAgainLimitText(limit int) domain.Text {
	count := strconv.Itoa(limit)
	if limit == 1 {
		return domain.T("run_again_limit_reached",
			"the task ran again {{count}} time in a row, its run-again limit; it now waits for its schedule", "count", count)
	}
	return domain.T("run_again_limit_reached",
		"the task ran again {{count}} times in a row, its run-again limit; it now waits for its schedule", "count", count)
}
