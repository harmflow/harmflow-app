package domain

import "time"

// WorkItemStatus is the daemon's view of a task's work item, built from the reports its sessions leave. The daemon
// never discovers or selects work itself; it keeps the history the skills read to skip processed items and to apply
// the retry and resume limits.
type WorkItemStatus string

// The statuses of a work item, as WorkItem in contracts/api/openapi.yaml names them.
const (
	WorkItemInProgress     WorkItemStatus = "in_progress"
	WorkItemCompleted      WorkItemStatus = "completed"
	WorkItemFailed         WorkItemStatus = "failed"
	WorkItemNeedsAttention WorkItemStatus = "needs_attention"
	WorkItemClosed         WorkItemStatus = "closed"
)

// Closed reports whether an item with the status is out of play: completed, needing attention, or closed. The queue
// leaves such an item out, and the ledger keeps it in its archive.
func (s WorkItemStatus) Closed() bool {
	return s == WorkItemCompleted || s == WorkItemNeedsAttention || s == WorkItemClosed
}

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

// WorkItemLimits are the limits of a task's work items. An item resumed MaxResumeAttempts times is not resumed again,
// and an item whose failures exceed MaxFailedRetries is not retried again; either one needs a human.
type WorkItemLimits struct {
	MaxResumeAttempts int
	MaxFailedRetries  int
}

// Skipped reports whether the session left the item alone: its result changes none of the item's status.
func (r WorkItemResult) Skipped() bool {
	return r.Outcome == "skipped"
}

// ApplyResult folds one execution's result for the item into the ledger entry. A failure counts toward the retry
// limit, a resumption toward the resume limit. The item needs a human past the retry limit, or at the resume limit
// while a later cycle would still resume it.
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
			// a draft PR with failing tests stays eligible for resumption
			w.Status = WorkItemInProgress
		}
	}
	w.enforceLimits(limits)
}

// ApplyInterruption counts an interruption as an operational failure.
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
	if w.Failures > l.MaxFailedRetries || (w.Resumes >= l.MaxResumeAttempts && w.resumable()) {
		w.Status = WorkItemNeedsAttention
	}
}

// resumable reports whether a later cycle would resume the item: its draft pull request's tests failed, or it failed
// with a pull request. An item that failed with none is retried instead, under the retry limit alone.
func (w *WorkItem) resumable() bool {
	return w.Status == WorkItemInProgress || (w.Status == WorkItemFailed && w.PullRequestURL != "")
}
