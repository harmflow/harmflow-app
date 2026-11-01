package domain

import "testing"

// harmflow-app#615: a report whose one work item failed with no pull request is a failed execution, whatever its own
// outcome says. A report with several work items keeps reading its outcome alone.
func TestStatusForReport(t *testing.T) {
	opened := &PullRequestRef{URL: "https://github.com/o/r/pull/9"}
	notOpened := WorkItemResult{Key: "o/r#7", Outcome: "failed"}
	for _, tc := range []struct {
		name string
		r    *ExecutionReport
		want ExecutionStatus
		item string // the key PullRequestNotOpened names, empty for none
	}{
		{"no report", nil, StatusCompleted, ""},
		{"completed", &ExecutionReport{Outcome: "completed"}, StatusCompleted, ""},
		{"no work", &ExecutionReport{Outcome: "no_work"}, StatusNoWork, ""},
		{"needs attention", &ExecutionReport{Outcome: "needs_attention"}, StatusNeedsAttention, ""},
		{"failed", &ExecutionReport{Outcome: "failed"}, StatusFailed, ""},
		{"not opened, completed", &ExecutionReport{Outcome: "completed", WorkItems: []WorkItemResult{notOpened}},
			StatusFailed, "o/r#7"},
		{"not opened, no outcome", &ExecutionReport{WorkItems: []WorkItemResult{notOpened}}, StatusFailed, "o/r#7"},
		{"not opened, needs attention", &ExecutionReport{Outcome: "needs_attention",
			WorkItems: []WorkItemResult{notOpened}}, StatusFailed, "o/r#7"},
		{"not opened, with a pull request of no URL", &ExecutionReport{Outcome: "completed",
			WorkItems: []WorkItemResult{{Key: "o/r#7", Outcome: "failed", PullRequest: &PullRequestRef{}}}},
			StatusFailed, "o/r#7"},
		{"failed with its pull request", &ExecutionReport{Outcome: "completed",
			WorkItems: []WorkItemResult{{Key: "o/r#7", Outcome: "failed", PullRequest: opened}}}, StatusCompleted, ""},
		{"a draft with failing tests", &ExecutionReport{Outcome: "completed", WorkItems: []WorkItemResult{{Key: "o/r#7",
			Outcome: "completed", PullRequest: opened, Tests: &TestsResult{Status: "failed"}}}}, StatusCompleted, ""},
		{"several items, one not opened", &ExecutionReport{Outcome: "completed", WorkItems: []WorkItemResult{notOpened,
			{Key: "o/r#8", Outcome: "completed", PullRequest: opened}}}, StatusCompleted, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := StatusForReport(tc.r); got != tc.want {
				t.Errorf("StatusForReport = %s, want %s", got, tc.want)
			}
			if key, ok := tc.r.PullRequestNotOpened(); key != tc.item || ok != (tc.item != "") {
				t.Errorf("PullRequestNotOpened = %q, %v; want %q", key, ok, tc.item)
			}
		})
	}
}
