package app

import (
	"sort"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/eventlog"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

type ledgerFile struct {
	SchemaVersion string                                 `json:"schemaVersion"`
	Tasks         map[string]map[string]*domain.WorkItem `json:"tasks"`
}

func (a *App) loadLedger() {
	var f ledgerFile
	if ok, err := a.d.State.Load("workitems", &f); err != nil {
		a.log.Printf("work items: %v", err)
	} else if ok && f.Tasks != nil {
		// A ledger saved before the agent's text was redacted on arrival may keep a credential in a title.
		redacted := eventlog.RedactFields(&f)
		a.mu.Lock()
		a.ledger = f.Tasks
		a.mu.Unlock()
		if redacted {
			a.saveLedger()
		}
	}
}

func (a *App) saveLedger() {
	a.mu.Lock()
	f := ledgerFile{SchemaVersion: "1", Tasks: map[string]map[string]*domain.WorkItem{}}
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

// trackWorkItem records the item a session selected, so an interruption can be counted against it (D-089).
func (a *App) trackWorkItem(taskID, key, title, repo, executionID string) {
	if key == "" {
		return
	}
	a.mu.Lock()
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
}

func (a *App) applyWorkItem(taskID string, r domain.WorkItemResult, executionID string, limits domain.WorkItemLimits) {
	if r.Key == "" {
		return
	}
	a.mu.Lock()
	a.itemLocked(taskID, r.Key).ApplyResult(r, executionID, limits, a.now())
	a.mu.Unlock()
	a.saveLedger()
}

// failWorkItem returns an item that failed operationally to the queue, until its retry limit (FS-007 FR-009, FR-011).
func (a *App) failWorkItem(taskID, key, executionID, lastOperation string, limits domain.WorkItemLimits) {
	a.mu.Lock()
	a.itemLocked(taskID, key).ApplyInterruption(executionID, lastOperation, limits, a.now())
	a.mu.Unlock()
	a.saveLedger()
}

// recover marks every execution a crash or restart left active as interrupted: it is never continued, and its work
// item is resumed in a later cycle (FS-003 FR-010). Waiting firings stay queued; startQueued then drops each one whose
// task was disabled or deleted while the daemon was down.
func (a *App) recover() error {
	all, err := a.d.Executions.LoadAll()
	if err != nil {
		return err
	}
	now := a.now()
	var interrupted, redacted []*domain.Execution
	a.mu.Lock()
	for _, x := range all {
		x.Upgrade()
		// A record saved before the agent's text was redacted on arrival may keep a credential, with no time limit.
		changed := eventlog.RedactFields(x)
		if x.Attempts == nil {
			x.Attempts = []domain.Attempt{}
		}
		if x.WorkItems == nil {
			x.WorkItems = []domain.WorkItemResult{}
		}
		if x.Warnings == nil {
			x.Warnings = []domain.Warning{}
		}
		if x.ChangedFiles == nil {
			x.ChangedFiles = []string{}
		}
		if x.Commands == nil {
			x.Commands = []domain.CommandRecord{}
		}
		a.executions[x.ID] = x
		if x.Status.IsActive() {
			x.Status = domain.StatusInterrupted
			x.EndedAt = &now
			x.SetStep(domain.Text{})
			x.SetError(daemonStopped())
			if at := x.CurrentAttempt(); at != nil && at.EndedAt == nil {
				at.EndedAt = &now
				at.Outcome = domain.AttemptInterrupted
			}
			interrupted = append(interrupted, x)
		} else if changed {
			redacted = append(redacted, x)
		}
	}
	a.mu.Unlock()
	for _, x := range redacted {
		a.persistExecution(x)
	}
	for _, x := range interrupted {
		a.endLeftovers(x)
		a.persistExecution(x)
		if x.WorkItem != "" {
			limits := a.taskLimits(x.TaskID)
			a.failWorkItem(x.TaskID, x.WorkItem, x.ID, x.LastOperation, limits)
		}
		a.emit(domain.Event{Type: domain.EventExecutionInterrupted, TaskID: x.TaskID, ExecutionID: x.ID,
			WorkItemID: x.WorkItem, Severity: domain.SeverityError,
			Payload: map[string]any{"status": string(domain.StatusInterrupted), "lastOperation": x.LastOperation}}.Says(
			x.ErrorText()))
	}
	return nil
}

func (a *App) taskLimits(taskID string) domain.WorkItemLimits {
	l := domain.WorkItemLimits{MaxResumeAttempts: domain.DefaultMaxResumeAttempts, MaxFailedRetries: domain.DefaultMaxFailedRetries}
	a.mu.Lock()
	defer a.mu.Unlock()
	if e := a.tasks[taskID]; e != nil {
		if g := e.doc.Effective().Spec.GitHubIssueImplementation; g != nil {
			l.MaxResumeAttempts, l.MaxFailedRetries = *g.Queue.MaxResumeAttempts, *g.Queue.MaxFailedRetries
		}
	}
	return l
}

func sortByCreated(xs []*domain.Execution) {
	sort.Slice(xs, func(i, j int) bool { return xs[i].CreatedAt.Before(xs[j].CreatedAt) })
}

var _ = time.Second
