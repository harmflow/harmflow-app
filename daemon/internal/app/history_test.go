package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/fsstore"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// scanStats counts the executions as Stats did before it read the kept orders: over every execution the daemon holds.
func scanStats(a *App) Stats {
	now := a.now().In(a.d.Location)
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, a.d.Location)
	const hours = 12
	start := now.Truncate(time.Hour).Add(-(hours - 1) * time.Hour)
	bucket := func(t *time.Time) int {
		if t == nil || t.Before(start) {
			return -1
		}
		return min(int(t.Sub(start)/time.Hour), hours-1)
	}
	s := Stats{Activity: Activity{Running: make([]int, hours), Queued: make([]int, hours), Completed: make([]int, hours),
		Failed: make([]int, hours)}}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.eachSummaryLocked(func(x *domain.ExecutionSummary) {
		switch {
		case x.Status.IsActive():
			s.Running++
		case x.Status == domain.StatusQueued:
			s.Queued++
		}
		done := x.Status == domain.StatusCompleted || x.Status == domain.StatusNoWork
		failed := x.Status == domain.StatusFailed || x.Status == domain.StatusInterrupted
		if x.EndedAt != nil && !x.EndedAt.Before(midnight) {
			if done {
				s.CompletedToday++
			} else if failed {
				s.FailedToday++
			}
		}
		if i := bucket(x.StartedAt); i >= 0 {
			s.Activity.Running[i]++
			if x.StartedAt.Sub(x.CreatedAt) > 5*time.Second {
				s.Activity.Queued[i]++
			}
		}
		if i := bucket(x.EndedAt); i >= 0 {
			if done {
				s.Activity.Completed[i]++
			} else if failed {
				s.Activity.Failed[i]++
			}
		}
	})
	return s
}

// checkAgainstAScan compares what the kept orders give — the list of executions, each task's latest, and the counters —
// with a scan of every execution the daemon holds.
func checkAgainstAScan(t *testing.T, e *env, tasks []string, want int) {
	t.Helper()
	a := e.app
	var all []*domain.ExecutionSummary
	a.mu.Lock()
	a.eachSummaryLocked(func(s *domain.ExecutionSummary) { all = append(all, s) })
	a.mu.Unlock()
	if len(all) != want {
		t.Fatalf("the daemon holds %d executions, want %d", len(all), want)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].CreatedAt.After(all[j].CreatedAt) })
	var scanned, listed []string
	for _, s := range all {
		scanned = append(scanned, s.ID+" "+string(s.Status))
	}
	for _, s := range a.ListExecutions(ExecutionFilter{Limit: 500}) {
		listed = append(listed, s.ID+" "+string(s.Status))
	}
	if !slices.Equal(listed, scanned) {
		t.Errorf("ListExecutions() = %v, want the scan's %v", listed, scanned)
	}
	if got := a.ListExecutions(ExecutionFilter{Limit: 2}); len(got) != 2 || got[0].ID != all[0].ID || got[1].ID != all[1].ID {
		t.Errorf("ListExecutions(limit 2) = %+v, want the two newest", got)
	}
	for _, task := range tasks {
		var newest *domain.ExecutionSummary
		for _, s := range all {
			if s.TaskID == task && (newest == nil || s.CreatedAt.After(newest.CreatedAt)) {
				newest = s
			}
		}
		v, err := a.GetTask(task)
		switch {
		case err != nil:
			t.Errorf("GetTask(%q) = %v", task, err)
		case newest == nil && v.LastExecution != nil:
			t.Errorf("GetTask(%q).LastExecution = %+v, want none", task, v.LastExecution)
		case newest != nil && (v.LastExecution == nil || v.LastExecution.ID != newest.ID ||
			v.LastExecution.Status != newest.Status):
			t.Errorf("GetTask(%q).LastExecution = %+v, want %s %s", task, v.LastExecution, newest.ID, newest.Status)
		}
	}
	if got, want := a.Stats(), scanStats(a); !reflect.DeepEqual(got, want) {
		t.Errorf("Stats() = %+v, want the scan's %+v", got, want)
	}
}

// The list of executions, each task's latest, and the counters read the orders kept as executions are added and
// change status, and the orders a restart rebuilds from the index; each equals a scan of every execution.
func TestKeptOrdersMatchAScan(t *testing.T) {
	e := newEnv(t)
	tasks := []string{"alpha", "beta", "gamma"}
	for _, task := range tasks {
		e.create(task, "")
	}
	now := e.clock.Now()
	at := func(d time.Duration) *time.Time { t := now.Add(d); return &t }
	add := func(task string, created time.Duration, status domain.ExecutionStatus, started, ended *time.Time) *domain.Execution {
		x := domain.NewExecution(domain.TaskDocument{Metadata: domain.TaskMetadata{ID: task}}, domain.TriggerManual, nil,
			*at(created))
		x.Status, x.StartedAt, x.EndedAt = status, started, ended
		e.app.mu.Lock()
		e.app.addExecutionLocked(x)
		e.app.mu.Unlock()
		e.app.persistExecution(x)
		return x
	}
	// Added out of creation order: one created two days ago that ends only now, one created before midnight that ended
	// after it, executions still running or waiting, and ones that ended in and out of the counted hours.
	add("alpha", -3*time.Hour, domain.StatusCompleted, at(-3*time.Hour), at(-2*time.Hour))
	old := add("beta", -48*time.Hour, domain.StatusRunning, at(-48*time.Hour), nil)
	add("alpha", -10*time.Hour, domain.StatusFailed, at(-10*time.Hour), at(-7*time.Hour))
	running := add("gamma", -time.Hour, domain.StatusRunning, at(-50*time.Minute), nil)
	add("beta", -30*time.Hour, domain.StatusNoWork, at(-30*time.Hour), at(-29*time.Hour))
	waiting := add("gamma", -30*time.Minute, domain.StatusQueued, nil, nil)
	add("alpha", -5*time.Hour, domain.StatusCompleted, at(-5*time.Hour), at(-4*time.Hour))
	checkAgainstAScan(t, e, tasks, 7)

	// Executions change status and end, and a new one is the newest of its task.
	end := func(x *domain.Execution, status domain.ExecutionStatus) {
		e.app.mu.Lock()
		x.Status, x.EndedAt = status, at(0)
		if x.StartedAt == nil {
			x.StartedAt = at(0)
		}
		x.Changed()
		e.app.mu.Unlock()
		e.app.persistExecution(x)
	}
	end(running, domain.StatusCompleted)
	end(old, domain.StatusInterrupted)
	e.app.mu.Lock()
	waiting.Status, waiting.StartedAt = domain.StatusRunning, at(0)
	waiting.Changed()
	e.app.mu.Unlock()
	e.app.persistExecution(waiting)
	add("beta", -time.Minute, domain.StatusFailed, at(-time.Minute), at(0))
	checkAgainstAScan(t, e, tasks, 8)
	e.app.mu.Lock()
	held := len(e.app.executions)
	e.app.mu.Unlock()
	if held != 1 {
		t.Errorf("the daemon holds %d records whole, want 1: only the running execution's", held)
	}

	// A restart reads the summaries from the index, and the execution left running is interrupted.
	e.clock.Advance(time.Hour)
	e = e.restart(t)
	checkAgainstAScan(t, e, tasks, 8)
	if v, err := e.app.GetExecution(waiting.ID); err != nil || v.Status != domain.StatusInterrupted {
		t.Errorf("after the restart, GetExecution(%q) = %s, %v; want interrupted", waiting.ID, v.Status, err)
	}
}

// The full record of an execution that ended is read from its file when it is asked for: GetExecution, its output,
// its events, and a cancel that finds it ended all know it.
func TestEndedExecutionReadWhenAskedFor(t *testing.T) {
	e := newEnv(t)
	e.create("done", "")
	now := e.clock.Now()
	x := domain.NewExecution(domain.TaskDocument{Metadata: domain.TaskMetadata{ID: "done"}}, domain.TriggerManual, nil, now)
	x.Status, x.EndedAt, x.LastOperation = domain.StatusCompleted, &now, "opened draft PR o/r#3"
	x.Attempts = append(x.Attempts, domain.Attempt{Number: 1, Harness: domain.HarnessClaudeCode, StartedAt: now})
	x.Commands = nil // a record saved without commands reads with none, not null
	if err := e.app.d.Executions.Save(x); err != nil {
		t.Fatal(err)
	}
	e = e.restart(t)
	e.app.mu.Lock()
	_, whole := e.app.executions[x.ID]
	_, ended := e.app.ended[x.ID]
	e.app.mu.Unlock()
	if whole || !ended {
		t.Fatalf("after a restart, the ended execution is held whole %v, as a summary %v; want only its summary", whole, ended)
	}
	v, err := e.app.GetExecution(x.ID)
	if err != nil || v.LastOperation != x.LastOperation || len(v.Attempts) != 1 || v.Commands == nil {
		t.Errorf("GetExecution(%q) = %+v, %v; want the record on disk, with its commands empty", x.ID, v, err)
	}
	if out, err := e.app.ExecutionOutput(x.ID, 100); err != nil || len(out.Attempts) != 1 {
		t.Errorf("ExecutionOutput(%q) = %+v, %v; want its one attempt", x.ID, out, err)
	}
	if _, _, err := e.app.ExecutionEvents(x.ID, 0, 0); err != nil {
		t.Errorf("ExecutionEvents(%q) = %v", x.ID, err)
	}
	if err := e.app.CancelExecution(x.ID); err == nil || err.(*Error).Code != CodeConflict {
		t.Errorf("CancelExecution(%q) = %v, want a conflict: it ended", x.ID, err)
	}
	if _, err := e.app.GetExecution("exec-unknown"); err == nil || err.(*Error).Code != CodeNotFound {
		t.Errorf("GetExecution of an unknown execution = %v, want not found", err)
	}
}

// The redaction pass runs only on a record saved at an older schema, which it saves again at the current one, so
// each record is redacted once; a record at the current schema was redacted on arrival and is not read at start.
func TestRedactionPassOnlyForOlderRecords(t *testing.T) {
	e := newEnv(t)
	token := "ghp_" + strings.Repeat("f", 36)
	save := func(version string) *domain.Execution {
		now := e.clock.Now()
		x := domain.NewExecution(domain.TaskDocument{Metadata: domain.TaskMetadata{ID: "kept"}}, domain.TriggerManual,
			nil, now)
		x.SchemaVersion, x.Status, x.EndedAt, x.Summary = version, domain.StatusCompleted, &now, "Pushed with "+token
		if err := e.app.d.Executions.Save(x); err != nil {
			t.Fatal(err)
		}
		return x
	}
	older, current := save("2"), save(domain.ExecutionSchemaVersion)
	path := func(x *domain.Execution) string { return filepath.Join(e.paths.Executions, x.ID+".json") }
	read := func(x *domain.Execution) string {
		data, err := os.ReadFile(path(x))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	e = e.restart(t)
	if got := read(older); strings.Contains(got, token) || !strings.Contains(got, `"schemaVersion": "`+
		domain.ExecutionSchemaVersion+`"`) {
		t.Errorf("the older record was not saved again redacted at the current schema:\n%s", got)
	}
	// The current record was never read for redaction, so what it holds is as it was saved: the test wrote it
	// unredacted on purpose, which a daemon never does.
	if got := read(current); !strings.Contains(got, token) {
		t.Errorf("the record at the current schema was redacted again:\n%s", got)
	}
	info, err := os.Stat(path(older))
	if err != nil {
		t.Fatal(err)
	}
	e = e.restart(t)
	if again, err := os.Stat(path(older)); err != nil || !again.ModTime().Equal(info.ModTime()) {
		t.Errorf("a second start saved the redacted record again (%v): %v, then %v", err, info.ModTime(), again.ModTime())
	}
	if v, err := e.app.GetExecution(older.ID); err != nil || v.Summary != "Pushed with [REDACTED]" {
		t.Errorf("GetExecution(%q) = %q, %v; want its redacted summary", older.ID, v.Summary, err)
	}
}

// unheldSaves is an execution repository that notes each save of an execution that ended made with no HoldIndex
// under way: a save that writes the whole index.
type unheldSaves struct {
	ports.ExecutionRepository

	mu    sync.Mutex
	holds int
	saved []string
}

func (u *unheldSaves) HoldIndex() func() {
	release := u.ExecutionRepository.HoldIndex()
	u.mu.Lock()
	u.holds++
	u.mu.Unlock()
	return func() {
		u.mu.Lock()
		u.holds--
		u.mu.Unlock()
		release()
	}
}

func (u *unheldSaves) Save(x *domain.Execution) error {
	u.mu.Lock()
	if x.Status.IsTerminal() && u.holds == 0 {
		u.saved = append(u.saved, x.ID)
	}
	u.mu.Unlock()
	return u.ExecutionRepository.Save(x)
}

// The first start at the current schema saves every older record again, and each execution it interrupts: it makes
// those saves with the index held, so it writes the index once, not once per record, and the index holds each record
// at the current schema.
func TestRecoveryWritesTheIndexOnce(t *testing.T) {
	e := newEnv(t)
	want := map[string]bool{}
	for _, status := range []domain.ExecutionStatus{domain.StatusCompleted, domain.StatusFailed, domain.StatusCompleted,
		domain.StatusRunning} {
		now := e.clock.Now()
		x := domain.NewExecution(domain.TaskDocument{Metadata: domain.TaskMetadata{ID: "kept"}}, domain.TriggerManual,
			nil, now)
		x.SchemaVersion, x.Status = "2", status
		if status.IsTerminal() {
			x.EndedAt = &now
		}
		if err := e.app.d.Executions.Save(x); err != nil {
			t.Fatal(err)
		}
		want[x.ID] = true
		e.clock.Advance(time.Minute)
	}
	u := &unheldSaves{}
	e = e.restart(t, func(n *env) {
		u.ExecutionRepository = n.records
		n.records = u
	})
	u.mu.Lock()
	saved := u.saved
	u.mu.Unlock()
	if len(saved) != 0 {
		t.Errorf("the start saved %v with the index not held, writing the index once per record", saved)
	}
	data, err := os.ReadFile(filepath.Join(e.paths.Executions, fsstore.IndexFile))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Executions []struct {
			Summary domain.ExecutionSummary `json:"summary"`
		} `json:"executions"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, en := range doc.Executions {
		got[en.Summary.ID] = true
		if en.Summary.SchemaVersion != domain.ExecutionSchemaVersion || !en.Summary.Status.IsTerminal() {
			t.Errorf("the index holds %s at schema %q, %s; want it ended, at %q", en.Summary.ID,
				en.Summary.SchemaVersion, en.Summary.Status, domain.ExecutionSchemaVersion)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the index holds %v, want %v", got, want)
	}
}
