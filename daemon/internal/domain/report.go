package domain

// ExecutionReport is report.json, written by the session's agents and skills in the execution directory
// (contracts/schemas/execution-report.schema.json).
type ExecutionReport struct {
	Outcome       string           `json:"outcome,omitempty"`
	Summary       string           `json:"summary,omitempty"`
	LastOperation string           `json:"lastOperation,omitempty"`
	WorkItems     []WorkItemResult `json:"workItems,omitempty"`
	Warnings      []ReportWarning  `json:"warnings,omitempty"`
}

// ReportWarning is a warning in report.json: a code, the agent's message, the repository it is about, and the key of
// the work item it is about, when it names one. The executor records it as a Warning, keeping repository_unprotected
// and conflicting_activity and reading any other code as other.
type ReportWarning struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Repository string `json:"repository,omitempty"`
	WorkItem   string `json:"workItem,omitempty"`
}

// ProgressLine is one line of progress.jsonl, read while the session runs.
type ProgressLine struct {
	Type       string `json:"type"`
	Message    string `json:"message,omitempty"`
	WorkItem   string `json:"workItem,omitempty"`
	Phase      string `json:"phase,omitempty"`
	Status     string `json:"status,omitempty"`
	URL        string `json:"url,omitempty"`
	Number     int    `json:"number,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Repository string `json:"repository,omitempty"`
}

// StatusForReport maps a report's outcome to the execution's terminal status; no report means the harness
// finished without one, which is a completed execution.
func StatusForReport(r *ExecutionReport) ExecutionStatus {
	if r == nil {
		return StatusCompleted
	}
	switch r.Outcome {
	case "no_work":
		return StatusNoWork
	case "needs_attention":
		return StatusNeedsAttention
	case "failed":
		return StatusFailed
	default:
		return StatusCompleted
	}
}
