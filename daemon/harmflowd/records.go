package harmflowd

import (
	"context"

	"github.com/harmflow/harmflow-app/daemon/internal/app"
)

// TaskInput is a task document to check: its YAML text, or the document as JSON, as the local API's task validation
// takes it.
type TaskInput = app.TaskInput

// TaskValidation is the outcome of checking a task document: Valid when it has no error, each error and warning located
// in the YAML when it can be, and the document as read, with its defaults applied.
type TaskValidation = app.ValidationResult

// ExecutionFilter selects execution records, as the local API's list of executions does; its zero value selects the
// newest 100.
type ExecutionFilter = app.ExecutionFilter

// ExecutionPage is one page of the execution records a filter selects, newest first, and how many it selects in all.
type ExecutionPage = app.ExecutionPage

// ExecutionView is an execution's full record, as the Executions screen reads it.
type ExecutionView = app.ExecutionView

// ExecutionOutput is what the harness printed, attempt by attempt, as the Executions screen shows it: redacted.
type ExecutionOutput = app.ExecutionOutput

// ValidateTask checks a task document with the validation the local API's task validation applies, and writes nothing.
// Each error and warning is in the result; ctx bounds what the validation asks the harnesses.
func (d *Daemon) ValidateTask(ctx context.Context, in TaskInput) *TaskValidation {
	return d.app.Validate(ctx, in)
}

// Executions lists the execution records the filter selects, newest first, as the local API's list of executions does.
func (d *Daemon) Executions(f ExecutionFilter) ExecutionPage {
	return d.app.ListExecutions(f)
}

// Execution reads one execution's full record; an unknown execution is an error.
func (d *Daemon) Execution(id string) (ExecutionView, error) {
	return d.app.GetExecution(id)
}

// ExecutionEvents reads the stored events of an execution after the id after, at most limit of them when limit is
// positive, redacted as they were logged, and reports whether retention has deleted its log; an unknown execution is
// an error.
func (d *Daemon) ExecutionEvents(id string, after int64, limit int) (events []Event, logExpired bool, err error) {
	return d.app.ExecutionEvents(id, after, limit)
}

// ExecutionOutput reads the end of what the harness printed in each attempt of an execution, at most limit bytes of
// each, or 1 MiB when limit is not positive, as the local API's default, redacted as the Executions screen shows it; an
// unknown execution is an error.
func (d *Daemon) ExecutionOutput(id string, limit int64) (ExecutionOutput, error) {
	if limit <= 0 {
		limit = 1 << 20
	}
	return d.app.ExecutionOutput(id, limit)
}
