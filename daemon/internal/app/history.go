package app

import (
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// The executions the daemon holds, guarded by a.mu: a.executions keeps the whole record of each execution that has not
// ended, and of one that ended until its record is saved; a.ended keeps the summary of every other, whose full record
// is read when it is asked for. a.byCreated lists every execution by creation, oldest first; a.byEnd lists the ended
// ones by endKey, oldest first; a.latest names each task's newest execution. They change as executions are added and
// end, so no list or counter scans the whole history.

// placed is an execution in one of the orders: its id, and the instant it is ordered by.
type placed struct {
	id string
	at time.Time
}

// insertPlaced inserts l into list, sorted by instant, after the entries at the same instant.
func insertPlaced(list []placed, l placed) []placed {
	i := sort.Search(len(list), func(i int) bool { return list[i].at.After(l.at) })
	return slices.Insert(list, i, l)
}

// endKey is the last instant an execution moved — when it ended, else when it started, else when it was created — so
// that every execution that started or ended since an instant is at or after it in a.byEnd.
func endKey(s *domain.ExecutionSummary) time.Time {
	t := s.CreatedAt
	for _, p := range []*time.Time{s.StartedAt, s.EndedAt} {
		if p != nil && p.After(t) {
			t = *p
		}
	}
	return t
}

// addExecutionLocked holds a new execution whole, and lists it.
func (a *App) addExecutionLocked(x *domain.Execution) {
	a.executions[x.ID] = x
	a.listLocked(x.ID, x.TaskID, x.CreatedAt)
}

func (a *App) listLocked(id, taskID string, created time.Time) {
	a.byCreated = insertPlaced(a.byCreated, placed{id, created})
	if l, ok := a.latest[taskID]; !ok || !created.Before(l.at) {
		a.latest[taskID] = placed{id, created}
	}
}

// keepEndedLocked holds an execution that ended as s, the summary of the record just saved from x: x leaves memory when
// it is the record held. Another record held whole for the same id stays, and s is dropped.
func (a *App) keepEndedLocked(x *domain.Execution, s *domain.ExecutionSummary) {
	held := a.executions[s.ID]
	if held != nil && held != x {
		return
	}
	_, ended := a.ended[s.ID]
	if held == nil && !ended {
		a.listLocked(s.ID, s.TaskID, s.CreatedAt)
	}
	delete(a.executions, s.ID)
	if !ended {
		a.byEnd = insertPlaced(a.byEnd, placed{s.ID, endKey(s)})
	}
	a.ended[s.ID] = s
}

// relistLocked builds the orders again from the executions held.
func (a *App) relistLocked() {
	a.byCreated, a.byEnd, a.latest = nil, nil, map[string]placed{}
	a.eachSummaryLocked(func(s *domain.ExecutionSummary) {
		a.byCreated = append(a.byCreated, placed{s.ID, s.CreatedAt})
		if _, ok := a.ended[s.ID]; ok {
			a.byEnd = append(a.byEnd, placed{s.ID, endKey(s)})
		}
	})
	byAt := func(x, y placed) int { return x.at.Compare(y.at) }
	slices.SortStableFunc(a.byCreated, byAt)
	slices.SortStableFunc(a.byEnd, byAt)
	for _, l := range a.byCreated {
		if s, ok := a.summaryLocked(l.id); ok {
			a.latest[s.TaskID] = l
		}
	}
}

// knownLocked reports whether the daemon holds execution id, whole or as its summary.
func (a *App) knownLocked(id string) bool {
	if _, ok := a.executions[id]; ok {
		return true
	}
	_, ok := a.ended[id]
	return ok
}

// summaryLocked returns the summary of execution id, and false when the daemon has no such execution.
func (a *App) summaryLocked(id string) (*domain.ExecutionSummary, bool) {
	if x := a.executions[id]; x != nil {
		return x.Summarize(), true
	}
	s, ok := a.ended[id]
	return s, ok
}

// eachSummaryLocked calls f with the summary of every execution the daemon holds.
func (a *App) eachSummaryLocked(f func(*domain.ExecutionSummary)) {
	for _, x := range a.executions {
		f(x.Summarize())
	}
	for _, s := range a.ended {
		f(s)
	}
}

// record returns a copy of execution id's full record: of the record held, or the record saved for an execution that
// ended; an unknown execution is CodeNotFound. The caller must not hold a.mu.
func (a *App) record(id string) (*domain.Execution, error) {
	a.mu.Lock()
	if x := a.executions[id]; x != nil {
		// A copy through JSON, as persistExecution takes: a record that marshals for the file marshals here, and data is
		// what json.Marshal just wrote.
		data, _ := json.Marshal(x)
		a.mu.Unlock()
		var c domain.Execution
		_ = json.Unmarshal(data, &c)
		return &c, nil
	}
	_, ok := a.ended[id]
	a.mu.Unlock()
	if !ok {
		return nil, executionNotFound(id)
	}
	x, err := a.d.Executions.Load(id)
	if errors.Is(err, ports.ErrNotFound) {
		return nil, executionNotFound(id)
	}
	if err != nil {
		return nil, err
	}
	normalize(x)
	return x, nil
}

func executionNotFound(id string) *Error {
	return newError(CodeNotFound, domain.T("execution_not_found", "no execution {{execution}}", "execution", id))
}

// normalize reads a record from disk as the daemon holds one: at the current schema, with its lists empty, not null.
func normalize(x *domain.Execution) {
	x.Upgrade()
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
}
