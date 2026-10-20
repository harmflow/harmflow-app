package domain

import "time"

// Event types of contracts/schemas/event.schema.json (D-094).
const (
	EventDaemonStarted           = "daemon.started"
	EventDaemonStopping          = "daemon.stopping"
	EventTaskCreated             = "task.created"
	EventTaskUpdated             = "task.updated"
	EventTaskDeleted             = "task.deleted"
	EventTaskChangedExternally   = "task.changed_externally"
	EventTaskInvalid             = "task.invalid"
	EventFiringQueued            = "firing.queued"
	EventFiringCoalesced         = "firing.coalesced"
	EventFiringSkipped           = "firing.skipped"
	EventFiringMissed            = "firing.missed"
	EventExecutionCreated        = "execution.created"
	EventExecutionStarted        = "execution.started"
	EventExecutionStatusChanged  = "execution.status_changed"
	EventExecutionCancelRequest  = "execution.cancel_requested"
	EventExecutionCompleted      = "execution.completed"
	EventExecutionNoWork         = "execution.no_work"
	EventExecutionFailed         = "execution.failed"
	EventExecutionCancelled      = "execution.cancelled"
	EventExecutionInterrupted    = "execution.interrupted"
	EventExecutionNeedsAttention = "execution.needs_attention"
	EventHarnessStarted          = "harness.started"
	EventHarnessMessage          = "harness.message"
	EventHarnessOutput           = "harness.output"
	EventHarnessExited           = "harness.exited"
	EventHarnessQuotaExhausted   = "harness.quota_exhausted"
	EventHarnessRateLimited      = "harness.rate_limited"
	EventHarnessUnavailable      = "harness.unavailable"
	EventHarnessSwitched         = "harness.switched"
	EventHarnessNoAlternative    = "harness.no_alternative"
	EventHarnessVersionUntested  = "harness.version_untested"
	EventToolStarted             = "tool.started"
	EventToolCompleted           = "tool.completed"
	EventStepStarted             = "step.started"
	EventWorkItemSelected        = "work_item.selected"
	EventWorkspaceChanged        = "workspace.changed"
	EventTestStarted             = "test.started"
	EventTestCompleted           = "test.completed"
	EventTestFailed              = "test.failed"
	EventPullRequestCreated      = "pull_request.created"
	EventPullRequestUpdated      = "pull_request.updated"
	EventPolicyUnenforced        = "policy.unenforced"
	EventRepositoryUnprotected   = "repository.unprotected"
	EventNotificationCreated     = "notification.created"
)

type Severity string

const (
	SeverityDebug   Severity = "debug"
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

const EventSchemaVersion = "1"

// Event is a normalized event (architecture/event-model.md). Agent text and tool output keep their original
// language; only the label of Type is translated by the dashboard.
type Event struct {
	ID            int64          `json:"id"`
	SchemaVersion string         `json:"schemaVersion"`
	Timestamp     time.Time      `json:"timestamp"`
	TaskID        string         `json:"taskId,omitempty"`
	ExecutionID   string         `json:"executionId,omitempty"`
	WorkItemID    string         `json:"workItemId,omitempty"`
	Attempt       int            `json:"attempt,omitempty"`
	Harness       string         `json:"harness,omitempty"`
	Type          string         `json:"type"`
	Severity      Severity       `json:"severity"`
	Message       string         `json:"message,omitempty"`
	Payload       map[string]any `json:"payload,omitempty"`
}

// TerminalEventFor maps a terminal status to the event that announces it.
func TerminalEventFor(s ExecutionStatus) string {
	switch s {
	case StatusCompleted:
		return EventExecutionCompleted
	case StatusNoWork:
		return EventExecutionNoWork
	case StatusCancelled:
		return EventExecutionCancelled
	case StatusInterrupted:
		return EventExecutionInterrupted
	case StatusNeedsAttention:
		return EventExecutionNeedsAttention
	default:
		return EventExecutionFailed
	}
}
