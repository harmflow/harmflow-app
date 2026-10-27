package domain

import (
	"slices"
	"testing"
	"time"
)

func minimalTask() TaskDocument {
	return TaskDocument{APIVersion: APIVersion, Kind: KindTask, Metadata: TaskMetadata{ID: "t", Name: "T"},
		Spec: TaskSpec{Type: TypeGeneral, Prompt: "p", Harness: HarnessSelection{Preferred: HarnessProfile{Provider: HarnessClaudeCode}}}}
}

// A task that sets nothing uses every default; one that sets a timeout keeps it.
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
	g.Spec.Type = TypeGitHub
	g.Spec.GitHub = &GitHubSpec{Repositories: []RepositoryTarget{{Name: "o/r", LocalPath: "/x"}}}
	g.Spec.Execution = &ExecutionLimits{TimeoutMinutes: ptr(20)}
	ge := g.Effective().Spec
	q := ge.GitHub.Queue
	if *ge.Execution.TimeoutMinutes != 20 {
		t.Errorf("timeout override lost")
	}
	if q.Ordering != OrderingOldestFirst || q.ResumeStrategy != ResumeFirst || *q.MaxDiscoveredPerCycle != 10 ||
		*q.MaxItemsPerCycle != 1 || *q.MaxResumeAttempts != 3 || *q.MaxFailedRetries != 3 ||
		*ge.GitHub.Delivery.TestRepairAttempts != 2 ||
		ge.GitHub.Repositories[0].BaseBranch != "main" {
		t.Errorf("queue defaults wrong: %+v", q)
	}
	// Effective never mutates the document it came from.
	if g.Spec.GitHub.Queue != nil || g.Spec.Permissions != nil {
		t.Error("Effective mutated the original")
	}
}

// A script task's program starts in its own working directory, never in a workspace, so its effective document has
// none, as it has no harness; a task of any other type gets an empty one.
func TestEffectiveWorkspaceFollowsTheType(t *testing.T) {
	script := TaskDocument{APIVersion: APIVersion, Kind: KindTask, Metadata: TaskMetadata{ID: "s", Name: "S"},
		Spec: TaskSpec{Type: TypeScriptTask, Script: &ScriptSpec{Command: []string{"echo", "hi"}}}}
	if w := script.Effective().Spec.Workspace; w != nil {
		t.Errorf("script task: Effective().Spec.Workspace = %+v, want nil", *w)
	}
	for _, typ := range []string{TypeGeneral, TypeGitHub} {
		d := minimalTask()
		d.Spec.Type = typ
		if w := d.Effective().Spec.Workspace; w == nil || *w != (Workspace{}) {
			t.Errorf("%s task: Effective().Spec.Workspace = %v, want an empty workspace", typ, w)
		}
	}
}

// A preferred or fallback profile that sets no model or effort reads default for both, which leaves each harness its
// own; a value a profile sets is kept.
func TestProfilesReadDefaults(t *testing.T) {
	d := minimalTask()
	d.Spec.Harness.Fallback = &FallbackPolicy{Candidates: []HarnessProfile{{Provider: HarnessCodex},
		{Provider: HarnessCodex, Model: "gpt-x"}}}
	want := []HarnessProfile{
		{Provider: HarnessClaudeCode, Model: DefaultModel, Effort: DefaultEffort},
		{Provider: HarnessCodex, Model: DefaultModel, Effort: DefaultEffort},
		{Provider: HarnessCodex, Model: "gpt-x", Effort: DefaultEffort},
	}
	if got := d.Profiles(); !slices.Equal(got, want) {
		t.Errorf("profiles %+v, want %+v", got, want)
	}
	if d.Spec.Harness.Preferred.Model != "" || d.Spec.Harness.Fallback.Candidates[0].Effort != "" {
		t.Error("Profiles mutated the document")
	}
}

// An empty list of notified events or failover triggers is a choice and stays empty; only an absent one takes the
// default.
func TestEffectiveKeepsEmptyLists(t *testing.T) {
	d := minimalTask()
	d.Spec.Harness.Fallback = &FallbackPolicy{On: []string{}}
	d.Spec.Notifications = &Notifications{Native: &NativeNotifications{Events: []string{}}}
	s := d.Effective().Spec
	if s.Harness.Fallback.On == nil || len(s.Harness.Fallback.On) != 0 {
		t.Errorf("fallback.on = %#v, want an empty list", s.Harness.Fallback.On)
	}
	if s.Notifications.Native.Events == nil || len(s.Notifications.Native.Events) != 0 {
		t.Errorf("notifications.native.events = %#v, want an empty list", s.Notifications.Native.Events)
	}
	s = minimalTask().Effective().Spec
	if !slices.Equal(s.Harness.Fallback.On, []string{FallbackOnQuota, FallbackOnUnavailable}) {
		t.Errorf("absent fallback.on = %v, want both triggers", s.Harness.Fallback.On)
	}
	if !slices.Equal(s.Notifications.Native.Events, DefaultNotifiedEvents) {
		t.Errorf("absent notifications.native.events = %v, want %v", s.Notifications.Native.Events, DefaultNotifiedEvents)
	}
}

// Quota and an unavailable harness fail over; rate limits retry; network, timeouts, and task errors never do.
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
		t.Run(c.name, func(t *testing.T) {

			in := base
			in.End = AttemptEnd{Class: c.class}
			if c.mod != nil {
				c.mod(&in)
			}
			if got := DecideAfterAttempt(in); got != c.want {
				t.Errorf("DecideAfterAttempt(%+v) = %s, want %s", in, got, c.want)
			}
		})
	}
}

// The harness's indicated wait, otherwise 1 minute doubling up to 15.
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

// A record of schema 1 kept the configured model and effort as the used ones, so they read as configured and the used
// ones as not reported; a current record is left as it is.
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

// A record of any schema that names its task's type by the GitHub type's legacy name reads as a GitHub task's, and
// every other type stays as it is.
func TestUpgradeReadsTheLegacyGitHubType(t *testing.T) {
	for _, c := range []struct{ schema, recorded, want string }{
		{"1", LegacyTypeGitHub, TypeGitHub},
		{ExecutionSchemaVersion, LegacyTypeGitHub, TypeGitHub},
		{ExecutionSchemaVersion, TypeGitHub, TypeGitHub},
		{ExecutionSchemaVersion, TypeGeneral, TypeGeneral},
		{ExecutionSchemaVersion, TypeScriptTask, TypeScriptTask},
	} {
		x := Execution{SchemaVersion: c.schema, TaskType: c.recorded}
		x.Upgrade()
		if x.TaskType != c.want {
			t.Errorf("Upgrade() of a schema %s record of a %s task: TaskType = %q, want %q", c.schema, c.recorded,
				x.TaskType, c.want)
		}
	}
}

// Past the limits an item needs a human.
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
