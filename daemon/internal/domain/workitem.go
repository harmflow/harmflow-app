package domain

import "time"

// WorkItemStatus is the daemon's view of a task's work item, built from the reports its sessions leave. The daemon
// never discovers or selects work itself (D-064); it keeps the history the skills read to skip processed items and to
// apply the retry and resume limits.
type WorkItemStatus string

// The statuses of a work item, as WorkItem in contracts/api/openapi.yaml names them.
const (
	WorkItemInProgress     WorkItemStatus = "in_progress"
	WorkItemCompleted      WorkItemStatus = "completed"
	WorkItemFailed         WorkItemStatus = "failed"
	WorkItemNeedsAttention WorkItemStatus = "needs_attention"
	WorkItemClosed         WorkItemStatus = "closed"
)

// WorkItem is one entry of the daemon's ledger of a task's work items: the item's latest state, as its last execution
// reported it, and the resumptions and failures counted toward the limits of WorkItemLimits.
type WorkItem struct {
	TaskID          string         `json:"taskId"`
	Key             string         `json:"key"`
	Title           string         `json:"title,omitempty"`
	Repository      string         `json:"repository,omitempty"`
	Status          WorkItemStatus `json:"status"`
	Resumes         int            `json:"resumes"`
	Failures        int            `json:"failures"`
	Branch          string         `json:"branch,omitempty"`
	PullRequestURL  string         `json:"pullRequestUrl,omitempty"`
	TestsStatus     string         `json:"testsStatus,omitempty"`
	LastExecutionID string         `json:"lastExecutionId,omitempty"`
	LastOperation   string         `json:"lastOperation,omitempty"`
	UpdatedAt       time.Time      `json:"updatedAt"`
}

// Limits of a task's work items (D-082).
type WorkItemLimits struct {
	MaxResumeAttempts int
	MaxFailedRetries  int
}

// ApplyResult folds one execution's result for the item into the ledger entry. A failure counts toward the retry
// limit, a resumption toward the resume limit; past either, the item needs a human (FS-007 FR-006, FR-011).
func (w *WorkItem) ApplyResult(r WorkItemResult, executionID string, limits WorkItemLimits, now time.Time) {
	if r.Title != "" {
		w.Title = r.Title
	}
	if r.Repository != "" {
		w.Repository = r.Repository
	}
	if r.Branch != "" {
		w.Branch = r.Branch
	}
	if r.PullRequest != nil && r.PullRequest.URL != "" {
		w.PullRequestURL = r.PullRequest.URL
	}
	if r.Tests != nil && r.Tests.Status != "" {
		w.TestsStatus = r.Tests.Status
	}
	w.LastExecutionID = executionID
	w.UpdatedAt = now
	if r.Kind == "resume" {
		w.Resumes++
	}
	switch r.Outcome {
	case "failed":
		w.Failures++
		w.Status = WorkItemFailed
	case "needs_attention":
		w.Status = WorkItemNeedsAttention
	case "closed":
		w.Status = WorkItemClosed
	case "skipped":
		// nothing changed for this item
	default:
		w.Status = WorkItemCompleted
		if w.TestsStatus == "failed" {
			// a draft PR with failing tests stays eligible for resumption (D-032, FS-007 FR-003)
			w.Status = WorkItemInProgress
		}
	}
	w.enforceLimits(limits)
}

// ApplyInterruption counts an interruption as an operational failure (D-089).
func (w *WorkItem) ApplyInterruption(executionID, lastOperation string, limits WorkItemLimits, now time.Time) {
	w.Failures++
	w.Status = WorkItemFailed
	w.LastExecutionID = executionID
	if lastOperation != "" {
		w.LastOperation = lastOperation
	}
	w.UpdatedAt = now
	w.enforceLimits(limits)
}

func (w *WorkItem) enforceLimits(l WorkItemLimits) {
	if w.Status == WorkItemClosed || w.Status == WorkItemCompleted {
		return
	}
	if w.Failures > l.MaxFailedRetries || w.Resumes > l.MaxResumeAttempts {
		w.Status = WorkItemNeedsAttention
	}
}
