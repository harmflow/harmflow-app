package domain

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"
)

// ExecutionStatus is a state of an execution's lifecycle.
type ExecutionStatus string

// The statuses of an execution, as ExecutionStatus in contracts/api/openapi.yaml names them.
const (
	StatusQueued           ExecutionStatus = "queued"
	StatusPreparing        ExecutionStatus = "preparing"
	StatusRunning          ExecutionStatus = "running"
	StatusSwitchingHarness ExecutionStatus = "switching_harness"
	StatusTesting          ExecutionStatus = "testing"
	StatusPublishing       ExecutionStatus = "publishing"
	StatusCompleted        ExecutionStatus = "completed"
	StatusNoWork           ExecutionStatus = "no_work"
	StatusFailed           ExecutionStatus = "failed"
	StatusCancelled        ExecutionStatus = "cancelled"
	StatusInterrupted      ExecutionStatus = "interrupted"
	StatusNeedsAttention   ExecutionStatus = "needs_attention"
)

// IsTerminal reports whether no further transition is possible.
func (s ExecutionStatus) IsTerminal() bool {
	switch s {
	case StatusCompleted, StatusNoWork, StatusFailed, StatusCancelled, StatusInterrupted, StatusNeedsAttention:
		return true
	}
	return false
}

// IsActive reports whether a harness may be working: started and not finished. A queued firing is not active.
func (s ExecutionStatus) IsActive() bool {
	return !s.IsTerminal() && s != StatusQueued
}

// CanTransition encodes the lifecycle: terminal states are final, and an active execution never goes back to
// queued. Cancellation, interruption, and failure may divert any non-terminal state.
func (s ExecutionStatus) CanTransition(to ExecutionStatus) error {
	if s.IsTerminal() {
		return fmt.Errorf("execution is %s, a final state", s)
	}
	if to == StatusQueued && s != StatusQueued {
		return fmt.Errorf("an execution in %s cannot return to queued", s)
	}
	if s == StatusQueued {
		switch to {
		case StatusPreparing, StatusCancelled, StatusInterrupted, StatusFailed:
			return nil
		}
		return fmt.Errorf("a queued execution starts by preparing, not %s", to)
	}
	return nil
}

// Trigger is what fired an execution: a run by hand, the schedule, or one catch-up for the firings missed while the
// daemon or the machine was unavailable.
type Trigger string

// The triggers of an execution, as Trigger in contracts/api/openapi.yaml names them.
const (
	TriggerManual   Trigger = "manual"
	TriggerSchedule Trigger = "schedule"
	TriggerCatchUp  Trigger = "catch_up"
)

// AttemptOutcome is how one harness process ended.
type AttemptOutcome string

// The outcomes of an attempt, as an Attempt's outcome in contracts/api/openapi.yaml names them; AttemptRunning is an
// attempt that has not ended.
const (
	AttemptRunning            AttemptOutcome = "running"
	AttemptSucceeded          AttemptOutcome = "succeeded"
	AttemptFailed             AttemptOutcome = "failed"
	AttemptQuotaExhausted     AttemptOutcome = "quota_exhausted"
	AttemptRateLimited        AttemptOutcome = "rate_limited"
	AttemptHarnessUnavailable AttemptOutcome = "harness_unavailable"
	AttemptNetworkFailed      AttemptOutcome = "network_failed"
	AttemptTimedOut           AttemptOutcome = "timed_out"
	AttemptCancelled          AttemptOutcome = "cancelled"
	AttemptInterrupted        AttemptOutcome = "interrupted"
)

// Attempt is one harness process. Model and Effort are what the harness reported using, empty when it reported
// nothing; the configured ones, default when the task set none, are kept apart and never stand in for them.
type Attempt struct {
	Number           int               `json:"number"`
	Harness          string            `json:"harness"`
	HarnessVersion   string            `json:"harnessVersion,omitempty"`
	Model            string            `json:"model,omitempty"`
	Effort           string            `json:"effort,omitempty"`
	ConfiguredModel  string            `json:"configuredModel"`
	ConfiguredEffort string            `json:"configuredEffort"`
	StartedAt        time.Time         `json:"startedAt"`
	EndedAt          *time.Time        `json:"endedAt,omitempty"`
	Outcome          AttemptOutcome    `json:"outcome,omitempty"`
	ExitCode         *int              `json:"exitCode,omitempty"`
	SessionID        string            `json:"sessionId,omitempty"`
	ErrorMessage     string            `json:"errorMessage,omitempty"`
	ErrorKey         string            `json:"errorKey,omitempty"`
	ErrorParams      map[string]string `json:"errorParams,omitempty"`
	Reason           string            `json:"reason,omitempty"`
	ReasonKey        string            `json:"reasonKey,omitempty"`
	ReasonParams     map[string]string `json:"reasonParams,omitempty"`
	// Usage is what the harness reported of its token use, as it runs and when it ends; absent when it reported none.
	Usage *Usage `json:"usage,omitempty"`
	// Prompt and SessionContext are what the attempt sent the harness, redacted like the raw output, since the task can
	// be edited afterwards and the context is composed per attempt; older records have neither.
	Prompt         string `json:"prompt,omitempty"`
	SessionContext string `json:"sessionContext,omitempty"`
	// Process is the harness process the attempt started, recorded as soon as it starts, so a daemon that restarts after
	// a crash ends it and what it started, and nothing else; absent when no harness process started, or when its start
	// time could not be read.
	Process *HarnessProcess `json:"process,omitempty"`
	// Checkpoint is what the attempt left in the workspace, read before the switch it ended in; absent on an attempt no
	// switch followed, and on older records.
	Checkpoint *Checkpoint `json:"checkpoint,omitempty"`
}

// MaxCheckpointFiles is how many changed files a checkpoint lists; it counts the rest.
const MaxCheckpointFiles = 500

// CheckpointScanLimit is how many entries of the workspace, files and folders, the scan of the part outside Git visits
// at most, so a large folder costs a switch a bounded time.
const CheckpointScanLimit = 20000

// The statuses of a CheckpointFile, as an Attempt's checkpoint in contracts/api/openapi.yaml names them.
const (
	FileAdded     = "added"
	FileModified  = "modified"
	FileDeleted   = "deleted"
	FileUntracked = "untracked"
)

// Checkpoint is the files an attempt changed in its workspace, read from the workspace itself whatever wrote them, as
// paths with their status, never their content. Inside a Git repository at the workspace's root or directly under it,
// they are what Git reports; outside one, the files modified since the attempt started, found by a scan.
type Checkpoint struct {
	Files []CheckpointFile `json:"files"`
	// More counts the changed files past the first MaxCheckpointFiles, which Files leaves out.
	More int `json:"more,omitempty"`
	// Scanned is set when part of the workspace is outside any Git repository, so its files were found by their
	// modification time: a deleted file, or one whose modification time was kept, is not seen there.
	Scanned bool `json:"scanned,omitempty"`
	// ScanLimited is set when the scan stopped at its limit, so files it did not reach may have changed too.
	ScanLimited bool `json:"scanLimited,omitempty"`
}

// CheckpointFile is one changed file: its path relative to the workspace, and whether it was added, modified,
// deleted, or left untracked.
type CheckpointFile struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

// NewCheckpoint lists files, sorted by path, up to MaxCheckpointFiles, and counts the rest.
func NewCheckpoint(files []CheckpointFile) *Checkpoint {
	sorted := slices.SortedFunc(slices.Values(files), func(a, b CheckpointFile) int { return strings.Compare(a.Path, b.Path) })
	if sorted == nil {
		sorted = []CheckpointFile{}
	}
	c := &Checkpoint{Files: sorted}
	if len(sorted) > MaxCheckpointFiles {
		c.Files, c.More = sorted[:MaxCheckpointFiles], len(sorted)-MaxCheckpointFiles
	}
	return c
}

// HarnessProcess is a harness process as the kernel recorded it when the attempt started it: its pid, the process group
// it leads, and its start time, so a reused pid is never mistaken for it.
type HarnessProcess struct {
	Pid       int       `json:"pid"`
	Group     int       `json:"group"`
	StartedAt time.Time `json:"startedAt"`
}

// SetError records why the attempt ended; ErrorMessage is the en-US text.
func (a *Attempt) SetError(t Text) {
	a.ErrorMessage, a.ErrorKey, a.ErrorParams = t.Message, t.Key, t.Params
}

// SetReason records why the attempt started.
func (a *Attempt) SetReason(t Text) {
	a.Reason, a.ReasonKey, a.ReasonParams = t.Message, t.Key, t.Params
}

// PullRequestRef is the pull request a work item delivered, as its report gave it: Draft and Created are nil when the
// report left them out.
type PullRequestRef struct {
	Number  int    `json:"number,omitempty"`
	URL     string `json:"url,omitempty"`
	Draft   *bool  `json:"draft,omitempty"`
	Created *bool  `json:"created,omitempty"`
}

// TestsResult is the outcome of a work item's tests, as its report gave it.
type TestsResult struct {
	Status  string `json:"status,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// WorkItemResult is one work item an execution handled, as the session's report gave it
// (contracts/schemas/execution-report.schema.json); the execution keeps its report's list as it is.
type WorkItemResult struct {
	Key          string          `json:"key"`
	Kind         string          `json:"kind,omitempty"`
	Repository   string          `json:"repository,omitempty"`
	IssueNumber  int             `json:"issueNumber,omitempty"`
	Title        string          `json:"title,omitempty"`
	Branch       string          `json:"branch,omitempty"`
	WorktreePath string          `json:"worktreePath,omitempty"`
	Outcome      string          `json:"outcome,omitempty"`
	PullRequest  *PullRequestRef `json:"pullRequest,omitempty"`
	Tests        *TestsResult    `json:"tests,omitempty"`
	Commits      []string        `json:"commits,omitempty"`
	ChangedFiles []string        `json:"changedFiles,omitempty"`
}

// Warning is a warning on an execution or a task: a code, its text, and its subject, such as a repository or the path
// of the task field it is about.
type Warning struct {
	Code string `json:"code"`
	Text
	Subject string `json:"subject,omitempty"`
}

// The codes of a Warning, as Warning in contracts/api/openapi.yaml names them; WarningOther is any warning no other
// code covers.
const (
	WarningPolicyUnenforced      = "policy_unenforced"
	WarningRepositoryUnprotected = "repository_unprotected"
	WarningConflictingActivity   = "conflicting_activity"
	WarningMissingAgent          = "missing_agent"
	WarningMissingSkill          = "missing_skill"
	WarningInvalidFallback       = "invalid_fallback"
	WarningHarnessNotReady       = "harness_not_ready"
	WarningVersionUntested       = "version_untested"
	WarningExternalEditInvalid   = "external_edit_invalid"
	WarningOther                 = "other"
)

// CommandRecord is one command the harness ran, recorded when its tool call starts: its status is running until the
// call ends, then succeeded or failed, or declined when the harness refused it before it ran, and failed if its attempt
// ends first. Attempt is the number of that attempt.
type CommandRecord struct {
	ID        string     `json:"id"`
	Command   string     `json:"command"`
	Status    string     `json:"status"`
	ExitCode  *int       `json:"exitCode,omitempty"`
	StartedAt time.Time  `json:"startedAt"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
	Attempt   int        `json:"attempt"`
}

// Execution is one concrete attempt to carry out a task; the record is kept with no time limit.
type Execution struct {
	SchemaVersion      string            `json:"schemaVersion"`
	ID                 string            `json:"id"`
	TaskID             string            `json:"taskId"`
	TaskName           string            `json:"taskName"`
	TaskType           string            `json:"taskType"`
	Status             ExecutionStatus   `json:"status"`
	Trigger            Trigger           `json:"trigger"`
	ScheduledFor       *time.Time        `json:"scheduledFor,omitempty"`
	CreatedAt          time.Time         `json:"createdAt"`
	StartedAt          *time.Time        `json:"startedAt,omitempty"`
	EndedAt            *time.Time        `json:"endedAt,omitempty"`
	CancelRequestedAt  *time.Time        `json:"cancelRequestedAt,omitempty"`
	TimeoutAt          *time.Time        `json:"timeoutAt,omitempty"`
	DefinitionRevision string            `json:"definitionRevision,omitempty"`
	WorkspacePath      string            `json:"workspacePath,omitempty"`
	ExecutionDir       string            `json:"executionDir,omitempty"`
	Harness            string            `json:"harness"`
	Model              string            `json:"model,omitempty"` // the current attempt's, as its harness reported it
	CurrentStep        string            `json:"currentStep,omitempty"`
	CurrentStepKey     string            `json:"currentStepKey,omitempty"`
	CurrentStepParams  map[string]string `json:"currentStepParams,omitempty"`
	Repository         string            `json:"repository,omitempty"`
	WorkItem           string            `json:"workItem,omitempty"`
	PullRequestURL     string            `json:"pullRequestUrl,omitempty"`
	TestsStatus        string            `json:"testsStatus,omitempty"`
	Summary            string            `json:"summary,omitempty"`
	ErrorMessage       string            `json:"errorMessage,omitempty"`
	ErrorKey           string            `json:"errorKey,omitempty"`
	ErrorParams        map[string]string `json:"errorParams,omitempty"`
	LastOperation      string            `json:"lastOperation,omitempty"`
	HarnessSwitches    int               `json:"harnessSwitches"`
	Attempts           []Attempt         `json:"attempts"`
	WorkItems          []WorkItemResult  `json:"workItems"`
	Warnings           []Warning         `json:"warnings"`
	ChangedFiles       []string          `json:"changedFiles"`
	Commands           []CommandRecord   `json:"commands"`
	LogExpired         bool              `json:"logExpired,omitempty"`
	// Script is the program a script task's execution ran, as it ran; absent for every other type.
	Script *ScriptRun `json:"script,omitempty"`
}

// ScriptRun is the program of a script execution as it ran, redacted like the raw output, so a later edit of the task
// never changes what an older execution shows.
type ScriptRun struct {
	Command          []string          `json:"command,omitempty"`
	Interpreter      []string          `json:"interpreter,omitempty"`
	Source           string            `json:"source,omitempty"`
	ScriptPath       string            `json:"scriptPath,omitempty"`
	Program          string            `json:"program"`
	WorkingDirectory string            `json:"workingDirectory"`
	Environment      map[string]string `json:"environment,omitempty"`
	ExitCode         *int              `json:"exitCode,omitempty"`
	Signal           string            `json:"signal,omitempty"`
	// Process identifies the program's process group, so a daemon that restarts after a crash can end what it left
	// running, and nothing else.
	Process *ProcessIdentity `json:"process,omitempty"`
}

// ProcessIdentity is a process group as the operating system reported it when the program started: its id, its
// leader's start time, and the system's boot time, so a reused id is never mistaken for it.
type ProcessIdentity struct {
	Group       int    `json:"group"`
	LeaderStart string `json:"leaderStart"`
	Boot        string `json:"boot"`
}

// ExecutionSchemaVersion is the schema version of the execution records this daemon writes; Upgrade reads older ones.
const ExecutionSchemaVersion = "2"

// Upgrade reads a record of an older schema as the current one. In schema 1, an attempt's model and effort were the
// configured ones, so they move to ConfiguredModel and ConfiguredEffort, and the used ones read not reported. A record
// of any schema may name its task's type by a legacy name, which reads as the current one.
func (x *Execution) Upgrade() {
	if x.SchemaVersion == "1" {
		for i := range x.Attempts {
			at := &x.Attempts[i]
			at.ConfiguredModel, at.ConfiguredEffort, at.Model, at.Effort = at.Model, at.Effort, "", ""
		}
		x.Model = ""
	}
	x.TaskType = CanonicalType(x.TaskType)
	x.SchemaVersion = ExecutionSchemaVersion
}

// NewExecution creates a record for a firing of a task.
func NewExecution(task TaskDocument, trigger Trigger, scheduledFor *time.Time, now time.Time) *Execution {
	e := task.Effective()
	return &Execution{
		SchemaVersion: ExecutionSchemaVersion,
		ID:            NewExecutionID(now),
		TaskID:        task.Metadata.ID,
		TaskName:      task.Metadata.Name,
		TaskType:      task.Spec.Type,
		Status:        StatusQueued,
		Trigger:       trigger,
		ScheduledFor:  scheduledFor,
		CreatedAt:     now,
		Harness:       e.Spec.Harness.Preferred.Provider,
		Attempts:      []Attempt{},
		WorkItems:     []WorkItemResult{},
		Warnings:      []Warning{},
		ChangedFiles:  []string{},
		Commands:      []CommandRecord{},
	}
}

// NewExecutionID returns a sortable, unique execution identifier.
func NewExecutionID(now time.Time) string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("exec-%s-%s", now.UTC().Format("20060102-150405"), hex.EncodeToString(b[:]))
}

// SetStep records the current step: the daemon's own, with a key, or the agent's or a tool's words, with none.
func (x *Execution) SetStep(t Text) {
	x.CurrentStep, x.CurrentStepKey, x.CurrentStepParams = t.Message, t.Key, t.Params
}

// SetError records why the execution ended; ErrorMessage is the en-US text.
func (x *Execution) SetError(t Text) {
	x.ErrorMessage, x.ErrorKey, x.ErrorParams = t.Message, t.Key, t.Params
}

// ErrorText is the text SetError recorded.
func (x *Execution) ErrorText() Text {
	return Text{Key: x.ErrorKey, Params: x.ErrorParams, Message: x.ErrorMessage}
}

// AddWarning records a warning once per code and subject.
func (x *Execution) AddWarning(w Warning) bool {
	for _, have := range x.Warnings {
		if have.Code == w.Code && have.Subject == w.Subject && have.Message == w.Message {
			return false
		}
	}
	x.Warnings = append(x.Warnings, w)
	return true
}

// AddChangedFile records a changed path once.
func (x *Execution) AddChangedFile(path string) bool {
	for _, p := range x.ChangedFiles {
		if p == path {
			return false
		}
	}
	x.ChangedFiles = append(x.ChangedFiles, path)
	return true
}

// CurrentAttempt returns the last attempt, or nil before the first.
func (x *Execution) CurrentAttempt() *Attempt {
	if len(x.Attempts) == 0 {
		return nil
	}
	return &x.Attempts[len(x.Attempts)-1]
}
