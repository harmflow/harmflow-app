package domain

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// ExecutionStatus is a state of architecture/execution-lifecycle.md.
type ExecutionStatus string

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

type Trigger string

const (
	TriggerManual   Trigger = "manual"
	TriggerSchedule Trigger = "schedule"
	TriggerCatchUp  Trigger = "catch_up"
)

// AttemptOutcome is how one harness process ended.
type AttemptOutcome string

const (
	AttemptRunning            AttemptOutcome = "running"
	AttemptSucceeded          AttemptOutcome = "succeeded"
	AttemptFailed             AttemptOutcome = "failed"
	AttemptQuotaExhausted     AttemptOutcome = "quota_exhausted"
	AttemptRateLimited        AttemptOutcome = "rate_limited"
	AttemptHarnessUnavailable AttemptOutcome = "harness_unavailable"
	AttemptTimedOut           AttemptOutcome = "timed_out"
	AttemptCancelled          AttemptOutcome = "cancelled"
	AttemptInterrupted        AttemptOutcome = "interrupted"
)

// Attempt is one harness process. Model and Effort are what the harness reported using, empty when it reported
// nothing; the configured ones, default when the task set none, are kept apart and never stand in for them (D-105).
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
	// be edited afterwards and the context is composed per attempt (FS-008 FR-010, D-104); older records have neither.
	Prompt         string `json:"prompt,omitempty"`
	SessionContext string `json:"sessionContext,omitempty"`
}

// SetError records why the attempt ended; ErrorMessage is the en-US text (FS-012 FR-005).
func (a *Attempt) SetError(t Text) {
	a.ErrorMessage, a.ErrorKey, a.ErrorParams = t.Message, t.Key, t.Params
}

// SetReason records why the attempt started.
func (a *Attempt) SetReason(t Text) {
	a.Reason, a.ReasonKey, a.ReasonParams = t.Message, t.Key, t.Params
}

type PullRequestRef struct {
	Number  int    `json:"number,omitempty"`
	URL     string `json:"url,omitempty"`
	Draft   *bool  `json:"draft,omitempty"`
	Created *bool  `json:"created,omitempty"`
}

type TestsResult struct {
	Status  string `json:"status,omitempty"`
	Summary string `json:"summary,omitempty"`
}

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

type Warning struct {
	Code string `json:"code"`
	Text
	Subject string `json:"subject,omitempty"`
}

const (
	WarningPolicyUnenforced      = "policy_unenforced"
	WarningRepositoryUnprotected = "repository_unprotected"
	WarningMissingAgent          = "missing_agent"
	WarningMissingSkill          = "missing_skill"
	WarningInvalidFallback       = "invalid_fallback"
	WarningHarnessNotReady       = "harness_not_ready"
	WarningVersionUntested       = "version_untested"
	WarningExternalEditInvalid   = "external_edit_invalid"
	WarningOther                 = "other"
)

type CommandRecord struct {
	ID        string     `json:"id"`
	Command   string     `json:"command"`
	Status    string     `json:"status"`
	ExitCode  *int       `json:"exitCode,omitempty"`
	StartedAt time.Time  `json:"startedAt"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
	Attempt   int        `json:"attempt"`
}

// Execution is one concrete attempt to carry out a task; the record is kept with no time limit (D-073).
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
}

const ExecutionSchemaVersion = "2"

// Upgrade reads a record of an older schema as the current one. In schema 1, an attempt's model and effort were the
// configured ones, so they move to ConfiguredModel and ConfiguredEffort, and the used ones read not reported (D-105).
func (x *Execution) Upgrade() {
	if x.SchemaVersion == "1" {
		for i := range x.Attempts {
			at := &x.Attempts[i]
			at.ConfiguredModel, at.ConfiguredEffort, at.Model, at.Effort = at.Model, at.Effort, "", ""
		}
		x.Model = ""
	}
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

// SetError records why the execution ended; ErrorMessage is the en-US text (FS-012 FR-005).
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
