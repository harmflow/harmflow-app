package app

import (
	"context"
	"slices"
	"sort"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/eventlog"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

type ledgerFile struct {
	SchemaVersion string                                 `json:"schemaVersion"`
	Tasks         map[string]map[string]*domain.WorkItem `json:"tasks"`
}

// ledgerVersion is the schemaVersion the ledger is saved at. A ledger saved at "1" comes from a daemon under which a
// skipped result, or an item a session selected and left without a result, left the item in progress for good;
// repairLedger mends such a ledger once.
const ledgerVersion = "2"

func (a *App) loadLedger() {
	var f ledgerFile
	if ok, err := a.d.State.Load("workitems", &f); err != nil {
		a.log.Printf("work items: %v", err)
	} else if ok && f.Tasks != nil {
		// A ledger saved before the agent's text was redacted on arrival may keep a credential in a title.
		redacted := eventlog.RedactFields(&f)
		a.mu.Lock()
		a.ledger = f.Tasks
		a.ledgerRepair = f.SchemaVersion == "1"
		a.mu.Unlock()
		if redacted {
			a.saveLedger()
		}
	}
}

func (a *App) saveLedger() {
	a.mu.Lock()
	f := ledgerFile{SchemaVersion: ledgerVersion, Tasks: map[string]map[string]*domain.WorkItem{}}
	for task, items := range a.ledger {
		m := map[string]*domain.WorkItem{}
		for k, w := range items {
			c := *w
			m[k] = &c
		}
		f.Tasks[task] = m
	}
	a.mu.Unlock()
	if err := a.d.State.Save("workitems", f); err != nil {
		a.log.Printf("saving work items: %v", err)
	}
}

func (a *App) itemLocked(taskID, key string) *domain.WorkItem {
	if a.ledger[taskID] == nil {
		a.ledger[taskID] = map[string]*domain.WorkItem{}
	}
	w := a.ledger[taskID][key]
	if w == nil {
		w = &domain.WorkItem{TaskID: taskID, Key: key, Status: domain.WorkItemInProgress}
		a.ledger[taskID][key] = w
	}
	return w
}

// selection is a work item a session selected, with what the ledger held for it just before: a skipped result returns
// the item to that status, and one the ledger did not hold leaves it again.
type selection struct {
	key    string
	held   bool
	status domain.WorkItemStatus
}

// trackWorkItem records the item a session selected, so an interruption can be counted against it, and returns what
// the ledger held for it before.
func (a *App) trackWorkItem(taskID, key, title, repo, executionID string) selection {
	s := selection{key: key}
	if key == "" {
		return s
	}
	a.mu.Lock()
	if w := a.ledger[taskID][key]; w != nil {
		s.held, s.status = true, w.Status
	}
	w := a.itemLocked(taskID, key)
	if title != "" {
		w.Title = title
	}
	if repo != "" {
		w.Repository = repo
	}
	if w.Status != domain.WorkItemNeedsAttention {
		w.Status = domain.WorkItemInProgress
	}
	w.LastExecutionID = executionID
	w.UpdatedAt = a.now()
	a.mu.Unlock()
	a.saveLedger()
	return s
}

// applyWorkItem folds a report's result for the item into the ledger. before is what the ledger held for the item when
// the session selected it, nil when the session did not select it. A skipped result changes no status: an item the
// session selected goes back to the status it had before, and an item the ledger did not hold gets no entry.
func (a *App) applyWorkItem(taskID string, r domain.WorkItemResult, executionID string, limits domain.WorkItemLimits,
	before *selection) {
	if r.Key == "" {
		return
	}
	a.mu.Lock()
	switch {
	case r.Skipped() && before != nil && !before.held:
		delete(a.ledger[taskID], r.Key)
	case r.Skipped() && before == nil && a.ledger[taskID][r.Key] == nil:
		a.mu.Unlock()
		return
	default:
		w := a.itemLocked(taskID, r.Key)
		w.ApplyResult(r, executionID, limits, a.now())
		if r.Skipped() && before != nil {
			w.Status = before.status
		}
	}
	a.mu.Unlock()
	a.saveLedger()
}

// failWorkItem returns an item that failed operationally to the queue, until its retry limit.
func (a *App) failWorkItem(taskID, key, executionID, lastOperation string, limits domain.WorkItemLimits) {
	a.mu.Lock()
	a.itemLocked(taskID, key).ApplyInterruption(executionID, lastOperation, limits, a.now())
	a.mu.Unlock()
	a.saveLedger()
}

// repairLedger mends, once, a ledger saved before ledgerVersion. An item left in progress whose last execution has
// ended, was not cancelled, and left no failing tests gets what the rules of ledgerVersion give it: one that execution
// reported skipped, with no resumption or failure counted, leaves the ledger, and one it gave no result counts an
// operational failure. An item whose execution's record is gone stays as it is.
func (a *App) repairLedger() {
	type failure struct{ task, key, execution, lastOperation string }
	var failures []failure
	removed := 0
	a.mu.Lock()
	if !a.ledgerRepair {
		a.mu.Unlock()
		return
	}
	a.ledgerRepair = false
	for task, items := range a.ledger {
		for key, w := range items {
			x, ok := a.summaryLocked(w.LastExecutionID)
			if w.Status != domain.WorkItemInProgress || w.TestsStatus == "failed" || !ok || !x.Status.IsTerminal() ||
				x.Status == domain.StatusCancelled {
				continue
			}
			i := slices.IndexFunc(x.WorkItems, func(r domain.WorkItemRef) bool { return r.Key == key })
			switch {
			case i < 0:
				failures = append(failures, failure{task, key, x.ID, x.LastOperation})
			case x.WorkItems[i].Skipped() && w.Resumes == 0 && w.Failures == 0:
				delete(items, key)
				removed++
			}
		}
	}
	a.mu.Unlock()
	for _, f := range failures {
		a.failWorkItem(f.task, f.key, f.execution, f.lastOperation, a.taskLimits(f.task))
	}
	a.log.Printf("work items: the ledger saved before version %s is repaired: %d skipped items removed, %d items "+
		"without a result counted as failed", ledgerVersion, removed, len(failures))
	a.saveLedger()
}

// recover marks every execution a crash or restart left active as interrupted: it is never continued, and each work
// item its session selected counts an operational failure, so a later cycle resumes or retries it — the item its record
// names, and every other one its log's selections name (otherSelections). First it ends the harness that each one's
// running attempt left, and what that harness started, which the daemon's end does not reach. Waiting firings stay
// queued; startQueued then drops each one whose task was disabled or deleted while the daemon was down.
//
// It reads the summary of each execution from the index of the records, and the whole record only of an execution
// that has not ended, or of one saved at an older schema: such a record was saved before the agent's text was redacted
// on arrival and may keep a credential, with no time limit, so it is redacted once and saved again at the current one.
func (a *App) recover(ctx context.Context) error {
	summaries, err := a.d.Executions.Summaries()
	if err != nil {
		return err
	}
	loaded := map[string]*domain.Execution{}
	for _, s := range summaries {
		if s.Status.IsTerminal() && s.SchemaVersion == domain.ExecutionSchemaVersion {
			continue
		}
		x, err := a.d.Executions.Load(s.ID)
		if err != nil {
			a.log.Printf("reading execution %s: %v", s.ID, err)
			continue
		}
		loaded[s.ID] = x
	}
	now := a.now()
	var interrupted, redacted []*domain.Execution
	left := map[string]leftHarness{}
	a.mu.Lock()
	a.ended = map[string]*domain.ExecutionSummary{}
	for _, s := range summaries {
		x := loaded[s.ID]
		if x == nil {
			if s.Status.IsTerminal() {
				delete(a.executions, s.ID)
				a.ended[s.ID] = s
			}
			continue
		}
		older := x.SchemaVersion != domain.ExecutionSchemaVersion
		normalize(x)
		if older {
			eventlog.RedactFields(x)
			x.Changed()
		}
		a.executions[x.ID] = x
		if x.Status.IsActive() {
			if at := x.CurrentAttempt(); at != nil && at.EndedAt == nil && at.Process != nil {
				left[x.ID] = leftHarness{process: *at.Process, attempt: at.Number, harness: at.Harness}
			}
			x.Status = domain.StatusInterrupted
			x.EndedAt = &now
			x.SetStep(domain.Text{})
			x.SetError(daemonStopped())
			if at := x.CurrentAttempt(); at != nil && at.EndedAt == nil {
				at.EndedAt = &now
				at.Outcome = domain.AttemptInterrupted
			}
			interrupted = append(interrupted, x)
		} else if older {
			redacted = append(redacted, x)
		}
	}
	a.relistLocked()
	a.mu.Unlock()
	// Each record saved below is of an execution that ended, whose save writes the whole index; on the first start at
	// this schema that is every record, so the index is held and written once, at the end.
	release := a.d.Executions.HoldIndex()
	defer release()
	for _, x := range redacted {
		a.persistExecution(x)
	}
	a.endLeftHarnesses(ctx, interrupted, left)
	for _, x := range interrupted {
		a.endLeftovers(ctx, x)
		a.persistExecution(x)
		limits := a.taskLimits(x.TaskID)
		if x.WorkItem != "" {
			a.failWorkItem(x.TaskID, x.WorkItem, x.ID, x.LastOperation, limits)
		}
		for _, key := range a.otherSelections(x) {
			a.failWorkItem(x.TaskID, key, x.ID, x.LastOperation, limits)
		}
		a.emit(domain.Event{Type: domain.EventExecutionInterrupted, TaskID: x.TaskID, ExecutionID: x.ID,
			WorkItemID: x.WorkItem, Severity: domain.SeverityError,
			Payload: map[string]any{"status": string(domain.StatusInterrupted), "lastOperation": x.LastOperation}}.Says(
			x.ErrorText()))
	}
	return nil
}

// otherSelections is every work item the session of an interrupted execution selected besides the one its record
// names, and left without a result, in the order its log first names each one. The record names only the last item a
// session selected, and the memory of the others was lost with the daemon, so they are read from the log's
// work_item.selected events. A log that cannot be read gives none.
func (a *App) otherSelections(x *domain.Execution) []string {
	events, _, err := a.d.Events.Replay(x.ID, 0, 0)
	if err != nil {
		a.log.Printf("reading the events of execution %s: %v", x.ID, err)
		return nil
	}
	var keys []string
	for _, e := range events {
		key := e.WorkItemID
		if e.Type != domain.EventWorkItemSelected || key == "" || key == x.WorkItem || slices.Contains(keys, key) ||
			slices.ContainsFunc(x.WorkItems, func(r domain.WorkItemResult) bool { return r.Key == key }) {
			continue
		}
		keys = append(keys, key)
	}
	return keys
}

// cancelGrace is how long the task gives its processes between SIGTERM and SIGKILL; the default once the task is gone.
func (a *App) cancelGrace(taskID string) time.Duration {
	seconds := domain.DefaultCancelGraceSeconds
	a.mu.Lock()
	if e := a.tasks[taskID]; e != nil {
		seconds = *e.doc.Effective().Spec.Execution.CancelGraceSeconds
	}
	a.mu.Unlock()
	return time.Duration(seconds) * time.Second
}

func (a *App) taskLimits(taskID string) domain.WorkItemLimits {
	l := domain.WorkItemLimits{MaxResumeAttempts: domain.DefaultMaxResumeAttempts, MaxFailedRetries: domain.DefaultMaxFailedRetries}
	a.mu.Lock()
	defer a.mu.Unlock()
	if e := a.tasks[taskID]; e != nil {
		if g := e.doc.Effective().Spec.GitHub; g != nil {
			l.MaxResumeAttempts, l.MaxFailedRetries = *g.Queue.MaxResumeAttempts, *g.Queue.MaxFailedRetries
		}
	}
	return l
}

func sortByCreated(xs []*domain.Execution) {
	sort.Slice(xs, func(i, j int) bool { return xs[i].CreatedAt.Before(xs[j].CreatedAt) })
}
