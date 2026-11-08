package domain

import (
	"strings"
	"testing"
)

// harmflow-app#615: a report whose one work item failed with no pull request is a failed execution, whatever its own
// outcome says. harmflow-app#892: a report of several work items, some failed with no pull request, needs attention,
// or fails when its outcome is failed or when no item's pull request was opened.
func TestStatusForReport(t *testing.T) {
	opened := &PullRequestRef{URL: "https://github.com/o/r/pull/9"}
	notOpened := WorkItemResult{Key: "o/r#7", Outcome: "failed"}
	secondNotOpened := WorkItemResult{Key: "o/r#8", Outcome: "failed"}
	draft := WorkItemResult{Key: "o/r#8", Outcome: "completed", PullRequest: opened}
	draftTestsFailed := WorkItemResult{Key: "o/r#8", Outcome: "completed", PullRequest: opened,
		Tests: &TestsResult{Status: "failed"}}
	for _, tc := range []struct {
		name string
		r    *ExecutionReport
		want ExecutionStatus
		item string // the keys PullRequestNotOpened names, joined by commas, empty for none
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
		{"several items, one not opened, completed", &ExecutionReport{Outcome: "completed",
			WorkItems: []WorkItemResult{notOpened, draft}}, StatusNeedsAttention, "o/r#7"},
		{"several items, one not opened, no outcome", &ExecutionReport{WorkItems: []WorkItemResult{draft, notOpened}},
			StatusNeedsAttention, "o/r#7"},
		{"several items, one not opened, no work", &ExecutionReport{Outcome: "no_work",
			WorkItems: []WorkItemResult{notOpened, draft}}, StatusNeedsAttention, "o/r#7"},
		{"several items, one not opened, failed", &ExecutionReport{Outcome: "failed",
			WorkItems: []WorkItemResult{notOpened, draft}}, StatusFailed, "o/r#7"},
		{"several items, none opened, completed", &ExecutionReport{Outcome: "completed",
			WorkItems: []WorkItemResult{notOpened, secondNotOpened}}, StatusFailed, "o/r#7,o/r#8"},
		{"several items, none opened, needs attention", &ExecutionReport{Outcome: "needs_attention",
			WorkItems: []WorkItemResult{notOpened, secondNotOpened}}, StatusFailed, "o/r#7,o/r#8"},
		{"several items, all opened, one with failing tests", &ExecutionReport{Outcome: "completed",
			WorkItems: []WorkItemResult{{Key: "o/r#7", Outcome: "completed", PullRequest: opened}, draftTestsFailed}},
			StatusCompleted, ""},
		{"several items, one failed with its pull request", &ExecutionReport{Outcome: "completed",
			WorkItems: []WorkItemResult{{Key: "o/r#7", Outcome: "failed", PullRequest: opened}, draft}},
			StatusCompleted, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := StatusForReport(tc.r); got != tc.want {
				t.Errorf("StatusForReport = %s, want %s", got, tc.want)
			}
			if got := strings.Join(tc.r.PullRequestNotOpened(), ","); got != tc.item {
				t.Errorf("PullRequestNotOpened = %q, want %q", got, tc.item)
			}
		})
	}
}
