package app

import (
	"sort"
	"strings"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

// ExecutionSummary is the row of an execution in lists; no progress percentage is invented.
type ExecutionSummary struct {
	ID                string                 `json:"id"`
	TaskID            string                 `json:"taskId"`
	TaskName          string                 `json:"taskName"`
	TaskType          string                 `json:"taskType"`
	Status            domain.ExecutionStatus `json:"status"`
	Trigger           domain.Trigger         `json:"trigger"`
	CreatedAt         time.Time              `json:"createdAt"`
	StartedAt         *time.Time             `json:"startedAt,omitempty"`
	EndedAt           *time.Time             `json:"endedAt,omitempty"`
	Harness           string                 `json:"harness"`
	Model             string                 `json:"model,omitempty"`
	AttemptCount      int                    `json:"attemptCount"`
	CurrentStep       string                 `json:"currentStep,omitempty"`
	CurrentStepKey    string                 `json:"currentStepKey,omitempty"`
	CurrentStepParams map[string]string      `json:"currentStepParams,omitempty"`
	Repository        string                 `json:"repository,omitempty"`
	WorkItem          string                 `json:"workItem,omitempty"`
	PullRequestURL    string                 `json:"pullRequestUrl,omitempty"`
	TestsStatus       string                 `json:"testsStatus,omitempty"`
	Summary           string                 `json:"summary,omitempty"`
	ErrorMessage      string                 `json:"errorMessage,omitempty"`
	ErrorKey          string                 `json:"errorKey,omitempty"`
	ErrorParams       map[string]string      `json:"errorParams,omitempty"`
	WarningCount      int                    `json:"warningCount"`
}

func summarize(x *domain.Execution) ExecutionSummary { return row(x.Summarize()) }

// row is an execution's row in lists, from its summary.
func row(s *domain.ExecutionSummary) ExecutionSummary {
	return ExecutionSummary{
		ID: s.ID, TaskID: s.TaskID, TaskName: s.TaskName, TaskType: s.TaskType, Status: s.Status, Trigger: s.Trigger,
		CreatedAt: s.CreatedAt, StartedAt: s.StartedAt, EndedAt: s.EndedAt, Harness: s.Harness, Model: s.Model,
		AttemptCount: s.AttemptCount, CurrentStep: s.CurrentStep, CurrentStepKey: s.CurrentStepKey,
		CurrentStepParams: s.CurrentStepParams, Repository: s.Repository, WorkItem: s.WorkItem,
		PullRequestURL: s.PullRequestURL, TestsStatus: s.TestsStatus, Summary: trimTo(s.Summary, 300),
		ErrorMessage: trimTo(s.ErrorMessage, 300), ErrorKey: s.ErrorKey, ErrorParams: trimParams(s.ErrorParams, 300),
		WarningCount: s.WarningCount,
	}
}

// trimParams shortens each param as trimTo does the en-US text, for the rows of a list.
func trimParams(p map[string]string, n int) map[string]string {
	if p == nil {
		return nil
	}
	out := make(map[string]string, len(p))
	for k, v := range p {
		out[k] = trimTo(v, n)
	}
	return out
}

// ExecutionFilter selects executions: of one task, in some statuses, active or not, and created before an instant; a
// zero field selects all. Limit caps the list, 100 when it is not between 1 and 500.
type ExecutionFilter struct {
	TaskID   string
	Statuses []string
	Active   *bool
	Limit    int
	Before   *time.Time
}

// ListExecutions lists the executions the filter selects, newest first: it walks the executions from the newest and
// stops at the limit.
func (a *App) ListExecutions(f ExecutionFilter) []ExecutionSummary {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []ExecutionSummary{}
	for i := len(a.byCreated) - 1; i >= 0 && len(out) < limit; i-- {
		s, ok := a.summaryLocked(a.byCreated[i].id)
		switch {
		case !ok:
		case f.TaskID != "" && s.TaskID != f.TaskID:
		case len(f.Statuses) > 0 && !containsString(f.Statuses, string(s.Status)):
		case f.Active != nil && s.Status.IsActive() != *f.Active:
		case f.Before != nil && !s.CreatedAt.Before(*f.Before):
		default:
			out = append(out, row(s))
		}
	}
	return out
}

// ExecutionView is the full record as the API returns it.
type ExecutionView struct {
	ExecutionSummary
	ScheduledFor       *time.Time              `json:"scheduledFor,omitempty"`
	CancelRequestedAt  *time.Time              `json:"cancelRequestedAt,omitempty"`
	TimeoutAt          *time.Time              `json:"timeoutAt,omitempty"`
	DefinitionRevision string                  `json:"definitionRevision,omitempty"`
	WorkspacePath      string                  `json:"workspacePath,omitempty"`
	ExecutionDir       string                  `json:"executionDir,omitempty"`
	Attempts           []domain.Attempt        `json:"attempts"`
	WorkItems          []domain.WorkItemResult `json:"workItems"`
	Warnings           []domain.Warning        `json:"warnings"`
	ChangedFiles       []string                `json:"changedFiles"`
	Commands           []domain.CommandRecord  `json:"commands"`
	LastOperation      string                  `json:"lastOperation,omitempty"`
	LogExpired         bool                    `json:"logExpired"`
	HarnessSwitches    int                     `json:"harnessSwitches"`
	Script             *domain.ScriptRun       `json:"script,omitempty"`
}

// GetExecution returns a copy of an execution's full record, read from its file once the execution ended; an unknown
// execution is CodeNotFound.
func (a *App) GetExecution(id string) (ExecutionView, error) {
	c, err := a.record(id)
	if err != nil {
		return ExecutionView{}, err
	}
	return ExecutionView{
		ExecutionSummary: summarize(c), ScheduledFor: c.ScheduledFor, CancelRequestedAt: c.CancelRequestedAt,
		TimeoutAt: c.TimeoutAt, DefinitionRevision: c.DefinitionRevision, WorkspacePath: c.WorkspacePath,
		ExecutionDir: c.ExecutionDir, Attempts: c.Attempts, WorkItems: c.WorkItems, Warnings: c.Warnings,
		ChangedFiles: c.ChangedFiles, Commands: c.Commands, LastOperation: c.LastOperation, LogExpired: c.LogExpired,
		HarnessSwitches: c.HarnessSwitches, Script: c.Script,
	}, nil
}

// ExecutionOutput is what the harness printed, attempt by attempt, as captured and redacted.
type ExecutionOutput struct {
	Attempts   []AttemptOutput `json:"attempts"`
	LogExpired bool            `json:"logExpired"`
}

// AttemptOutput is the end of one attempt's capture: Text starts at byte Offset of a capture of Size bytes.
type AttemptOutput struct {
	Attempt int    `json:"attempt"`
	Harness string `json:"harness"`
	Size    int64  `json:"size"`
	Offset  int64  `json:"offset"`
	Text    string `json:"text"`
}

// ExecutionOutput reads the end of each attempt's capture, at most limit bytes of each.
func (a *App) ExecutionOutput(id string, limit int64) (ExecutionOutput, error) {
	var attempts []domain.Attempt
	out := ExecutionOutput{Attempts: []AttemptOutput{}}
	a.mu.Lock()
	if x := a.executions[id]; x != nil {
		attempts = append(attempts, x.Attempts...)
		out.LogExpired = x.LogExpired
		a.mu.Unlock()
	} else {
		a.mu.Unlock()
		x, err := a.record(id)
		if err != nil {
			return ExecutionOutput{}, err
		}
		attempts, out.LogExpired = x.Attempts, x.LogExpired
	}
	for _, at := range attempts {
		c, err := a.d.Events.ReadRaw(id, at.Number, limit)
		if err != nil {
			return ExecutionOutput{}, err
		}
		out.Attempts = append(out.Attempts, AttemptOutput{Attempt: at.Number, Harness: at.Harness, Size: c.Size,
			Offset: c.Offset, Text: c.Text})
	}
	return out, nil
}

// Activity counts executions per hour, oldest hour first: those started in it, those started in it after waiting more
// than 5 seconds, and those that ended in it completed or failed.
type Activity struct {
	Running   []int `json:"running"`
	Queued    []int `json:"queued"`
	Completed []int `json:"completed"`
	Failed    []int `json:"failed"`
}

// Stats is the counters of the executions screen: executions running and queued now, those that completed or failed
// since midnight, the system gauges, and the activity of the last hours.
type Stats struct {
	Running        int            `json:"running"`
	Queued         int            `json:"queued"`
	CompletedToday int            `json:"completedToday"`
	FailedToday    int            `json:"failedToday"`
	System         map[string]any `json:"system,omitempty"`
	Activity       Activity       `json:"activity"`
}

// Stats counts executions for the counters of the executions screen, with per-hour activity for the last 12 hours.
func (a *App) Stats() Stats {
	now := a.now().In(a.d.Location)
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, a.d.Location)
	const hours = 12
	start := now.Truncate(time.Hour).Add(-(hours - 1) * time.Hour)
	bucket := func(t *time.Time) int {
		if t == nil || t.Before(start) {
			return -1
		}
		i := int(t.Sub(start) / time.Hour)
		if i >= hours {
			return hours - 1
		}
		return i
	}
	s := Stats{Activity: Activity{Running: make([]int, hours), Queued: make([]int, hours), Completed: make([]int, hours),
		Failed: make([]int, hours)}}
	count := func(status domain.ExecutionStatus, created time.Time, started, ended *time.Time) {
		if ended != nil && !ended.Before(midnight) {
			switch status {
			case domain.StatusCompleted, domain.StatusNoWork:
				s.CompletedToday++
			case domain.StatusFailed, domain.StatusInterrupted:
				s.FailedToday++
			}
		}
		if i := bucket(started); i >= 0 {
			s.Activity.Running[i]++
			if started.Sub(created) > 5*time.Second {
				s.Activity.Queued[i]++
			}
		}
		if i := bucket(ended); i >= 0 {
			switch status {
			case domain.StatusCompleted, domain.StatusNoWork:
				s.Activity.Completed[i]++
			case domain.StatusFailed, domain.StatusInterrupted:
				s.Activity.Failed[i]++
			}
		}
	}
	// What started or ended since the earlier of midnight and the first hour counted is at or after it in a.byEnd.
	since := midnight
	if start.Before(since) {
		since = start
	}
	a.mu.Lock()
	for _, x := range a.executions {
		switch {
		case x.Status.IsActive():
			s.Running++
		case x.Status == domain.StatusQueued:
			s.Queued++
		}
		count(x.Status, x.CreatedAt, x.StartedAt, x.EndedAt)
	}
	for i := len(a.byEnd) - 1; i >= 0 && !a.byEnd[i].at.Before(since); i-- {
		if x := a.ended[a.byEnd[i].id]; x != nil {
			count(x.Status, x.CreatedAt, x.StartedAt, x.EndedAt)
		}
	}
	a.mu.Unlock()
	if a.d.System != nil {
		s.System = a.d.System.Snapshot(a.d.Paths.Home)
	}
	return s
}

// Upcoming is a task's next scheduled firing.
type Upcoming struct {
	TaskID     string    `json:"taskId"`
	TaskName   string    `json:"taskName"`
	Harness    string    `json:"harness,omitempty"`
	NextFireAt time.Time `json:"nextFireAt"`
}

// QueueView is the queue as the API returns it: the firings waiting for their task, oldest first, the next scheduled
// firings, soonest first, and the work items still in play, last updated first.
type QueueView struct {
	Waiting   []ExecutionSummary `json:"waiting"`
	Upcoming  []Upcoming         `json:"upcoming"`
	WorkItems []domain.WorkItem  `json:"workItems"`
}

// Queue is what waits: firings waiting for their task, the next scheduled firings, and the work items the task's
// sessions reported that are still in play. A closed item is left out as soon as it closes (harmflow-app#341): the
// ledger keeps it, and the execution that closed it is in the history.
func (a *App) Queue() QueueView {
	a.mu.Lock()
	defer a.mu.Unlock()
	q := QueueView{Waiting: []ExecutionSummary{}, Upcoming: []Upcoming{}, WorkItems: []domain.WorkItem{}}
	var waiting []*domain.Execution
	for _, x := range a.executions {
		if x.Status == domain.StatusQueued {
			waiting = append(waiting, x)
		}
	}
	sortByCreated(waiting)
	for _, x := range waiting {
		q.Waiting = append(q.Waiting, summarize(x))
	}
	for id, s := range a.sched {
		e := a.tasks[id]
		if e == nil || s.NextFireAt == nil || !e.doc.IsEnabled() || e.invalid != nil {
			continue
		}
		q.Upcoming = append(q.Upcoming, Upcoming{TaskID: id, TaskName: e.doc.Metadata.Name,
			Harness: e.doc.Effective().Spec.Harness.Preferred.Provider, NextFireAt: *s.NextFireAt})
	}
	sort.Slice(q.Upcoming, func(i, j int) bool { return q.Upcoming[i].NextFireAt.Before(q.Upcoming[j].NextFireAt) })
	for _, task := range sortedKeys(a.ledger) {
		for _, key := range sortedKeys(a.ledger[task]) {
			if w := a.ledger[task][key]; !w.Status.Closed() {
				q.WorkItems = append(q.WorkItems, *w)
			}
		}
	}
	sort.SliceStable(q.WorkItems, func(i, j int) bool { return q.WorkItems[i].UpdatedAt.After(q.WorkItems[j].UpdatedAt) })
	return q
}

// Diagnostics lists the directories and the local API's address.
type Diagnostics struct {
	Version      string `json:"version"`
	Address      string `json:"address"`
	HomeDir      string `json:"homeDir"`
	TasksDir     string `json:"tasksDir"`
	StateDir     string `json:"stateDir"`
	LogsDir      string `json:"logsDir"`
	WorkspaceDir string `json:"workspaceDir"`
	RunsDir      string `json:"runsDir"`
	RuntimeFile  string `json:"runtimeFile"`
	TokenStore   string `json:"tokenStore"`
	BuiltinsDir  string `json:"builtinsDir"`
}

// Diagnostics returns the version, the installation's directories, the API's address, and the kind of the token's
// store.
func (a *App) Diagnostics(address, tokenStore string) Diagnostics {
	p := a.d.Paths
	return Diagnostics{Version: a.d.Version, Address: address, HomeDir: p.Home, TasksDir: p.Tasks, StateDir: p.State,
		LogsDir: p.Logs, WorkspaceDir: p.Workspace, RunsDir: p.Runs, RuntimeFile: p.RuntimeFile, TokenStore: tokenStore,
		BuiltinsDir: p.Builtins}
}

// ParseStatuses splits a comma-separated status filter.
func ParseStatuses(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
