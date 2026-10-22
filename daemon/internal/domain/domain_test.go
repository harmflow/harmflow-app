package domain

import (
	"testing"
	"time"
)

func minimalTask() TaskDocument {
	return TaskDocument{APIVersion: APIVersion, Kind: KindTask, Metadata: TaskMetadata{ID: "t", Name: "T"},
		Spec: TaskSpec{Type: TypeGeneral, Prompt: "p", Harness: HarnessSelection{Preferred: HarnessProfile{Provider: HarnessClaudeCode}}}}
}

// FS-001 AC-014: a task that sets nothing uses every default of D-082; one that sets a timeout keeps it.
func TestEffectiveDefaults(t *testing.T) {
	e := minimalTask().Effective()
	s := e.Spec
	checks := map[string][2]int{
		"timeoutMinutes":     {*s.Execution.TimeoutMinutes, 60},
		"cancelGraceSeconds": {*s.Execution.CancelGraceSeconds, 30},
		"rateMaxRetries":     {*s.Execution.RateLimit.MaxRetries, 5},
		"rateInitialWait":    {*s.Execution.RateLimit.InitialWaitSeconds, 60},
		"rateMaxWait":        {*s.Execution.RateLimit.MaxWaitSeconds, 900},
		"maxSwitches":        {*s.Harness.Fallback.MaxSwitches, 1},
	}
	for name, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s = %d, want %d", name, c[0], c[1])
		}
	}
	if s.Concurrency.OverlappingRuns != OverlapQueue || s.Permissions.Profile != ProfileWorkspaceWrite ||
		!*s.Permissions.RestrictToWorkspace || !*s.Enabled {
		t.Errorf("policy defaults wrong: %+v %+v", s.Concurrency, s.Permissions)
	}

	g := minimalTask()
	g.Spec.Type = TypeGitHubIssueImplementation
	g.Spec.GitHubIssueImplementation = &GitHubIssueImplementation{Repositories: []RepositoryTarget{{Name: "o/r", LocalPath: "/x"}}}
	g.Spec.Execution = &ExecutionLimits{TimeoutMinutes: ptr(20)}
	ge := g.Effective().Spec
	q := ge.GitHubIssueImplementation.Queue
	if *ge.Execution.TimeoutMinutes != 20 {
		t.Errorf("timeout override lost")
	}
	if q.Ordering != OrderingOldestFirst || q.ResumeStrategy != ResumeFirst || *q.MaxDiscoveredPerCycle != 10 ||
		*q.MaxItemsPerCycle != 1 || *q.MaxResumeAttempts != 3 || *q.MaxFailedRetries != 3 ||
		*ge.GitHubIssueImplementation.Delivery.TestRepairAttempts != 2 ||
		ge.GitHubIssueImplementation.Repositories[0].BaseBranch != "main" {
		t.Errorf("queue defaults wrong: %+v", q)
	}
	// Effective never mutates the document it came from.
	if g.Spec.GitHubIssueImplementation.Queue != nil || g.Spec.Permissions != nil {
		t.Error("Effective mutated the original")
	}
}

// D-067: quota and an unavailable harness fail over; rate limits retry; network, timeouts, and task errors never do.
func TestDecideAfterAttempt(t *testing.T) {
	base := FailoverInput{FallbackOn: []string{FallbackOnQuota, FallbackOnUnavailable}, HasNextCandidate: true,
		MaxSwitches: 1, MaxRateRetries: 5}
	cases := []struct {
		name  string
		class ErrorClass
		mod   func(*FailoverInput)
		want  Action
	}{
		{"success", ClassNone, nil, ActionFinish},
		{"quota", ClassQuotaExhausted, nil, ActionSwitch},
		{"unavailable", ClassHarnessUnavailable, nil, ActionSwitch},
		{"rate limit", ClassRateLimited, nil, ActionRetrySame},
		{"rate limit exhausted", ClassRateLimited, func(i *FailoverInput) { i.RateLimitRetries = 5 }, ActionFail},
		{"network", ClassNetwork, nil, ActionFail},
		{"timeout", ClassTimeout, nil, ActionFail},
		{"task error", ClassTaskError, nil, ActionFail},
		{"switch cap", ClassQuotaExhausted, func(i *FailoverInput) { i.Switches = 1 }, ActionNoAlternative},
		{"no candidate", ClassQuotaExhausted, func(i *FailoverInput) { i.HasNextCandidate = false }, ActionNoAlternative},
		{"trigger not chosen", ClassHarnessUnavailable, func(i *FailoverInput) { i.FallbackOn = []string{FallbackOnQuota} }, ActionFail},
	}
	for _, c := range cases {
		in := base
		in.End = AttemptEnd{Class: c.class}
		if c.mod != nil {
			c.mod(&in)
		}
		if got := DecideAfterAttempt(in); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

// D-082: the harness's indicated wait, otherwise 1 minute doubling up to 15.
func TestRateLimitWait(t *testing.T) {
	m := time.Minute
	want := []time.Duration{m, 2 * m, 4 * m, 8 * m, 15 * m, 15 * m}
	for i, w := range want {
		if got := RateLimitWait(i, 0, m, 15*m); got != w {
			t.Errorf("retry %d: got %v, want %v", i, got, w)
		}
	}
	if got := RateLimitWait(0, 90*time.Second, m, 15*m); got != 90*time.Second {
		t.Errorf("indicated wait ignored: %v", got)
	}
}

func TestLifecycleTransitions(t *testing.T) {
	if err := StatusCompleted.CanTransition(StatusRunning); err == nil {
		t.Error("a terminal state moved")
	}
	if err := StatusRunning.CanTransition(StatusQueued); err == nil {
		t.Error("an active execution went back to queued")
	}
	if err := StatusQueued.CanTransition(StatusRunning); err == nil {
		t.Error("queued skipped preparing")
	}
	for _, s := range []ExecutionStatus{StatusPreparing, StatusRunning, StatusTesting, StatusPublishing} {
		for _, to := range []ExecutionStatus{StatusCancelled, StatusInterrupted, StatusFailed} {
			if err := s.CanTransition(to); err != nil {
				t.Errorf("%s → %s refused: %v", s, to, err)
			}
		}
	}
	if !StatusNoWork.IsTerminal() || StatusQueued.IsActive() || !StatusSwitchingHarness.IsActive() {
		t.Error("state predicates wrong")
	}
}

// D-105: a record of schema 1 kept the configured model and effort as the used ones, so they read as configured and
// the used ones as not reported; a current record is left as it is.
func TestUpgradeMovesConfiguredValues(t *testing.T) {
	x := Execution{SchemaVersion: "1", Model: "default",
		Attempts: []Attempt{{Number: 1, Harness: HarnessClaudeCode, Model: "sonnet", Effort: "high"}}}
	x.Upgrade()
	at := x.Attempts[0]
	if x.SchemaVersion != ExecutionSchemaVersion || x.Model != "" || at.Model != "" || at.Effort != "" ||
		at.ConfiguredModel != "sonnet" || at.ConfiguredEffort != "high" {
		t.Errorf("upgraded: %+v", x)
	}
	x.Attempts[0].Model = "claude-sonnet-4-5"
	x.Upgrade()
	if x.Attempts[0].Model != "claude-sonnet-4-5" || x.Attempts[0].ConfiguredModel != "sonnet" {
		t.Errorf("a current record changed: %+v", x.Attempts[0])
	}
}

// FS-007 FR-006, FR-011, FS-003 AC-013: past the limits an item needs a human.
func TestWorkItemLimits(t *testing.T) {
	limits := WorkItemLimits{MaxResumeAttempts: 3, MaxFailedRetries: 3}
	w := &WorkItem{Key: "o/r#1"}
	now := time.Now()
	for i := 0; i < 3; i++ {
		w.ApplyInterruption("x", "pushed", limits, now)
		if w.Status != WorkItemFailed {
			t.Fatalf("after %d interruptions: %s", i+1, w.Status)
		}
	}
	w.ApplyInterruption("x", "pushed", limits, now)
	if w.Status != WorkItemNeedsAttention {
		t.Errorf("after 4 interruptions: %s, want needs_attention", w.Status)
	}
	r := &WorkItem{Key: "o/r#2"}
	failing := WorkItemResult{Key: "o/r#2", Kind: "resume", Outcome: "completed", Tests: &TestsResult{Status: "failed"}}
	for i := 0; i < 3; i++ {
		r.ApplyResult(failing, "x", limits, now)
	}
	if r.Status != WorkItemInProgress {
		t.Errorf("a draft PR with failing tests should stay resumable, got %s", r.Status)
	}
	r.ApplyResult(failing, "x", limits, now)
	if r.Status != WorkItemNeedsAttention {
		t.Errorf("past the resume limit: %s", r.Status)
	}
}

func TestExpandPath(t *testing.T) {
	got, err := ExpandPath("${PROJECTS_ROOT}/core", "/Users/u", map[string]string{"PROJECTS_ROOT": "/p"}, nil)
	if err != nil || got != "/p/core" {
		t.Errorf("got %q, %v", got, err)
	}
	if got, _ := ExpandPath("~/x", "/Users/u", nil, nil); got != "/Users/u/x" {
		t.Errorf("tilde: %q", got)
	}
	if _, err := ExpandPath("${MISSING}/x", "/Users/u", nil, func(string) string { return "" }); err == nil {
		t.Error("undefined variable accepted")
	}
}
