package app

import (
	"strings"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// sessionFull is a Claude Code quota whose session window is used up until a known time.
func sessionFull() (domain.Quota, error) {
	return domain.Quota{Windows: []domain.QuotaWindow{{Name: "Current session", UsedPercent: 100,
		ResetsAt: time.Date(2026, 10, 20, 13, 0, 0, 0, time.UTC)}}}, nil
}

// A task that allows no extra usage does not start an attempt on a harness whose quota is exhausted: the execution
// moves to its fallback, with an event that names the harness and when its quota resets, and the move counts as a
// switch. With no fallback, it fails with that reason.
func TestExhaustedQuotaHoldsTheAttemptBack(t *testing.T) {
	e := newEnv(t)
	e.create("held", codexFallback)
	e.create("alone", "")
	e.claude.setQuota(sessionFull)
	r, _ := e.app.RunTask("held")
	x := e.wait(r.ExecutionID, domain.StatusCompleted)
	if len(x.Attempts) != 1 || x.Attempts[0].Harness != domain.HarnessCodex || x.HarnessSwitches != 1 {
		t.Fatalf("attempts %+v, %d switches; want one Codex attempt and 1 switch", x.Attempts, x.HarnessSwitches)
	}
	if n := e.claude.promptCount(); n != 0 {
		t.Errorf("Claude Code got %d prompts, want 0", n)
	}
	counts, last := e.eventCounts(r.ExecutionID)
	exhausted := last[domain.EventHarnessQuotaExhausted]
	if counts[domain.EventHarnessQuotaExhausted] != 1 || exhausted.Harness != domain.HarnessClaudeCode ||
		exhausted.Payload["resetsAt"] == nil || !strings.Contains(exhausted.Payload["error"].(string), "2026-10-20 13:00 UTC") {
		t.Errorf("%d %s events, the last %+v; want one naming Claude Code and its reset", counts[domain.EventHarnessQuotaExhausted],
			domain.EventHarnessQuotaExhausted, exhausted)
	}
	if switched := last[domain.EventHarnessSwitched]; switched.Payload["reason"] != string(domain.ClassQuotaExhausted) {
		t.Errorf("%s = %+v, want the reason quota_exhausted", domain.EventHarnessSwitched, switched)
	}

	r, _ = e.app.RunTask("alone")
	x = e.wait(r.ExecutionID, domain.StatusFailed)
	if len(x.Attempts) != 0 || x.ErrorKey != "no_alternative_harness_detail" ||
		x.ErrorParams["detailKey"] != "quota_exhausted_until" || !strings.Contains(x.ErrorMessage, "Current session") {
		t.Errorf("attempts %+v, error %q (%s %v); want no attempt and the exhausted session window as the reason",
			x.Attempts, x.ErrorMessage, x.ErrorKey, x.ErrorParams)
	}
}

// The quota is read with no model call before an attempt, and that reading stands for five minutes: the attempt after
// it reads nothing, and the first attempt after five minutes reads again. A quota that cannot be read lets the attempt
// start, with a warning on its execution.
func TestQuotaReadAtMostOnceInFiveMinutes(t *testing.T) {
	e := newEnv(t)
	e.create("first", "")
	e.create("second", "")
	r, _ := e.app.RunTask("first")
	e.wait(r.ExecutionID, domain.StatusCompleted)
	e.clock.Advance(4 * time.Minute)
	r, _ = e.app.RunTask("second")
	x := e.wait(r.ExecutionID, domain.StatusCompleted)
	if n := e.claude.reads(); n != 1 {
		t.Errorf("%d quota reads for two attempts within five minutes, want 1", n)
	}
	if len(x.Warnings) != 0 {
		t.Errorf("warnings %+v, want none", x.Warnings)
	}

	e.clock.Advance(time.Minute)
	e.claude.setQuota(func() (domain.Quota, error) {
		return domain.Quota{}, domain.TextErr("command_failed", "{{command}} failed: {{detail}}", "command",
			"claude -p /usage", "detail", "exit status 1")
	})
	r, _ = e.app.RunTask("first")
	x = e.wait(r.ExecutionID, domain.StatusCompleted)
	if n := e.claude.reads(); n != 2 {
		t.Errorf("%d quota reads once five minutes passed, want 2", n)
	}
	if len(x.Attempts) != 1 || len(x.Warnings) != 1 || x.Warnings[0].Key != "quota_unread" ||
		!strings.Contains(x.Warnings[0].Message, "exit status 1") {
		t.Errorf("attempts %+v, warnings %+v; want one attempt with a warning that its quota was not read", x.Attempts,
			x.Warnings)
	}
}

// A Claude Code attempt that turns to extra usage is ended as an exhausted quota when its task does not allow it, and
// the execution goes on with its fallback. What the attempt reported is the harness's reading: the next attempt on it
// within five minutes is held back with no read.
func TestExtraUsageEndsTheAttempt(t *testing.T) {
	e := newEnv(t)
	e.create("frugal", codexFallback+"  execution:\n    cancelGraceSeconds: 1\n")
	e.claude.setScript(func(ports.RunRequest) string { return "echo 'EXTRA usage'; sleep 30; echo 'END ok'" })
	began := time.Now()
	r, _ := e.app.RunTask("frugal")
	x := e.wait(r.ExecutionID, domain.StatusCompleted)
	if took := time.Since(began); took > 20*time.Second {
		t.Errorf("the execution took %v; the attempt was not ended when it turned to extra usage", took)
	}
	if len(x.Attempts) != 2 || x.Attempts[0].Outcome != domain.AttemptQuotaExhausted ||
		x.Attempts[0].ErrorKey != "extra_usage_stopped" || x.Attempts[1].Harness != domain.HarnessCodex ||
		x.Attempts[1].Outcome != domain.AttemptSucceeded {
		t.Fatalf("attempts %+v; want Claude Code ended for its extra usage, then Codex succeeding", x.Attempts)
	}
	if x.CancelRequestedAt != nil {
		t.Errorf("the execution reads cancelled at %v", x.CancelRequestedAt)
	}

	reads := e.claude.reads()
	r, _ = e.app.RunTask("frugal")
	x = e.wait(r.ExecutionID, domain.StatusCompleted)
	if len(x.Attempts) != 1 || x.Attempts[0].Harness != domain.HarnessCodex || e.claude.reads() != reads {
		t.Errorf("attempts %+v, %d new reads; want Claude Code held back on the reading its attempt reported", x.Attempts,
			e.claude.reads()-reads)
	}
}

// A task that allows extra usage has its quota checked too, yet starts on an exhausted one and keeps an attempt that
// turns to extra usage.
func TestExtraUsageAllowed(t *testing.T) {
	e := newEnv(t)
	e.create("generous", codexFallback+"  execution:\n    allowExtraUsage: true\n")
	e.claude.setQuota(sessionFull)
	e.claude.setScript(func(ports.RunRequest) string { return "echo 'EXTRA usage'; echo 'END ok'" })
	r, _ := e.app.RunTask("generous")
	x := e.wait(r.ExecutionID, domain.StatusCompleted)
	if len(x.Attempts) != 1 || x.Attempts[0].Harness != domain.HarnessClaudeCode ||
		x.Attempts[0].Outcome != domain.AttemptSucceeded || x.HarnessSwitches != 0 {
		t.Errorf("attempts %+v, %d switches; want one Claude Code attempt that succeeded", x.Attempts, x.HarnessSwitches)
	}
	if n := e.claude.reads(); n != 1 {
		t.Errorf("%d quota reads, want 1: the quota is checked before each attempt", n)
	}
	for _, w := range x.Warnings {
		if w.Code == domain.WarningPolicyUnenforced {
			t.Errorf("warning %+v on a task that allows extra usage", w)
		}
	}
}

// A task that allows no extra usage is warned at validation for each harness of its chain that cannot be stopped once
// it runs, Codex here; one that allows it is not. A script task, which runs no harness, does not take the key.
func TestExtraUsageValidation(t *testing.T) {
	e := newEnv(t)
	res := e.app.Validate(t.Context(), TaskInput{YAML: taskYAML("check", codexFallback)})
	var unenforced []string
	for _, w := range res.Warnings {
		if w.Code == domain.WarningPolicyUnenforced && w.Key == "extra_usage_unenforced" {
			unenforced = append(unenforced, w.Path)
		}
	}
	if !res.Valid || len(unenforced) != 1 || unenforced[0] != "/spec/harness/fallback/candidates/0/provider" {
		t.Errorf("valid %v, extra-usage warnings at %v; want one, on the Codex fallback", res.Valid, unenforced)
	}
	res = e.app.Validate(t.Context(), TaskInput{YAML: taskYAML("check", codexFallback+"  execution:\n    allowExtraUsage: true\n")})
	for _, w := range res.Warnings {
		if w.Key == "extra_usage_unenforced" {
			t.Errorf("warning %+v on a task that allows extra usage", w)
		}
	}
	script := `apiVersion: harmflow/v1alpha1
kind: Task
metadata:
  id: program
  name: Program
spec:
  type: script
  script:
    command: [/bin/echo, hi]
  execution:
    allowExtraUsage: false
`
	if res := e.app.Validate(t.Context(), TaskInput{YAML: script}); res.Valid || len(res.Errors) == 0 ||
		!strings.Contains(res.Errors[0].Path, "/spec/execution") {
		t.Errorf("a script task that sets allowExtraUsage: valid %v, errors %+v; want it rejected", res.Valid, res.Errors)
	}
}
