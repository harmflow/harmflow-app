package app

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/claudecode"
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

// A quota read that fails is kept like a good one: the attempts while it stands start with its warning and read
// nothing. It stands for the check interval, doubled on each failed read in a row, up to an hour; a good read ends the
// wait, and the next reading stands for the interval again.
func TestFailedQuotaReadIsKept(t *testing.T) {
	e := newEnv(t)
	e.create("t", "")
	failing := func() (domain.Quota, error) {
		return domain.Quota{}, domain.TextErr("quota_report_unrecognized", "Claude Code's usage report names no plan window")
	}
	e.claude.setQuota(failing)
	run := func(advance time.Duration, wantReads int, wantWarned bool) {
		t.Helper()
		e.clock.Advance(advance)
		r, _ := e.app.RunTask("t")
		x := e.wait(r.ExecutionID, domain.StatusCompleted)
		if n := e.claude.reads(); n != wantReads {
			t.Errorf("after %v, %d quota reads, want %d", advance, n, wantReads)
		}
		warned := len(x.Warnings) == 1 && x.Warnings[0].Key == "quota_unread"
		if warned != wantWarned || (!wantWarned && len(x.Warnings) != 0) {
			t.Errorf("after %v, warnings %+v; want the quota_unread warning: %v", advance, x.Warnings, wantWarned)
		}
	}
	run(0, 1, true)              // the first read fails and stands for 5 minutes
	run(4*time.Minute, 1, true)  // still standing: no read, the same warning
	run(time.Minute, 2, true)    // 5 minutes: read again, fails, stands for 10
	run(9*time.Minute, 2, true)  // still standing
	run(time.Minute, 3, true)    // 10 minutes: fails, stands for 20
	run(20*time.Minute, 4, true) // fails, stands for 40
	run(40*time.Minute, 5, true) // fails, stands for 60, the cap
	run(60*time.Minute, 6, true) // fails, still 60
	run(59*time.Minute, 6, true) // still standing at the cap
	e.claude.setQuota(func() (domain.Quota, error) { return domain.Quota{}, nil })
	run(time.Minute, 7, false)   // a good read ends the wait
	run(4*time.Minute, 7, false) // and stands for the interval
	e.claude.setQuota(failing)
	run(time.Minute, 8, true) // a failure after a good read stands for the interval again
	run(5*time.Minute, 9, true)
}

// The check interval the settings give a harness is how long its readings stand, a good one or a failed one.
func TestQuotaIntervalFromTheSettings(t *testing.T) {
	e := newEnv(t)
	if _, err := e.app.UpdateSettings(SettingsPatch{Quota: map[string]QuotaShare{
		domain.HarnessClaudeCode: {MaxPercent: 100, CheckIntervalMinutes: new(30.0)}}}); err != nil {
		t.Fatalf("UpdateSettings(claude-code every 30 minutes) = %v, want no error", err)
	}
	e.create("t", "")
	for i, c := range []struct {
		advance time.Duration
		reads   int
	}{{0, 1}, {29 * time.Minute, 1}, {time.Minute, 2}} {
		e.clock.Advance(c.advance)
		r, _ := e.app.RunTask("t")
		e.wait(r.ExecutionID, domain.StatusCompleted)
		if n := e.claude.reads(); n != c.reads {
			t.Errorf("run %d, %d quota reads, want %d", i, n, c.reads)
		}
	}
}

// The time a failed read stands is the interval after the first, doubled on each failed read in a row after it, up to
// an hour, or the interval when it is longer.
func TestFailedQuotaReadStands(t *testing.T) {
	cases := []struct {
		name     string
		interval time.Duration
		failures int
		want     time.Duration
	}{
		{"5m after 1 failure", 5 * time.Minute, 1, 5 * time.Minute},
		{"5m after 2 failures", 5 * time.Minute, 2, 10 * time.Minute},
		{"5m after 4 failures", 5 * time.Minute, 4, 40 * time.Minute},
		{"5m after 5 failures, capped at an hour", 5 * time.Minute, 5, time.Hour},
		{"5m after 50 failures, capped at an hour", 5 * time.Minute, 50, time.Hour},
		{"7m after 5 failures, capped at an hour", 7 * time.Minute, 5, time.Hour},
		{"60m after 3 failures, the interval itself", 60 * time.Minute, 3, time.Hour},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := (quotaFailure{failures: c.failures}).stands(c.interval); got != c.want {
				t.Errorf("stands(%v) after %d failures = %v, want %v", c.interval, c.failures, got, c.want)
			}
		})
	}
}

// harmflow-app#975: a running attempt's report is no read of the quota endpoint, so it neither ends a failed read's
// wait nor resets its doubling. Between two failed reads, an attempt that reports a reading leaves the next attempts
// inside the window with the quota_unread warning and no read, and the next failed read stands twice as long.
func TestReportedQuotaLeavesAFailedReadStanding(t *testing.T) {
	e := newEnv(t)
	e.create("t", "")
	e.claude.setQuota(func() (domain.Quota, error) {
		return domain.Quota{}, domain.TextErr("quota_report_unrecognized", "Claude Code's usage report names no plan window")
	})
	e.claude.setScript(func(ports.RunRequest) string { return "echo 'WINDOW 10'; echo 'END ok'" })
	run := func(advance time.Duration, wantReads int) {
		t.Helper()
		e.clock.Advance(advance)
		r, _ := e.app.RunTask("t")
		x := e.wait(r.ExecutionID, domain.StatusCompleted)
		if n := e.claude.reads(); n != wantReads {
			t.Errorf("after %v, %d quota reads, want %d", advance, n, wantReads)
		}
		if len(x.Warnings) != 1 || x.Warnings[0].Key != "quota_unread" {
			t.Errorf("after %v, warnings %+v; want the quota_unread warning", advance, x.Warnings)
		}
	}
	run(0, 1)             // the first read fails and stands for 5 minutes; the attempt reports a reading
	run(time.Minute, 1)   // inside the window, beside that fresh report: no read, the same warning
	run(4*time.Minute, 2) // 5 minutes: read again, fails, stands for 10, not 5
	run(9*time.Minute, 2) // still standing, beside the report of the last attempt
	run(time.Minute, 3)   // 10 minutes: read again
}

// A quota read that timed out, with the error each adapter returns at its bound, is a quota that cannot be read: the
// attempt starts, and the execution carries one warning that names the harness and why.
func TestTimedOutQuotaReadLetsTheAttemptStart(t *testing.T) {
	cases := []struct {
		name, harness string
		timedOut      error
		detail        string
	}{
		{"Claude Code", domain.HarnessClaudeCode, domain.TextErr("command_failed", "{{command}} failed: {{detail}}",
			"command", `claude -p "/usage"`, "detail", "signal: killed"), "signal: killed"},
		{"Codex", domain.HarnessCodex, domain.TextErr("codex_app_server_timed_out",
			"Codex's app server did not answer {{method}} within {{seconds}} seconds", "method", "account/rateLimits/read",
			"seconds", "15"), "did not answer account/rateLimits/read"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			doc := strings.Replace(taskYAML("unread", ""), "provider: claude-code", "provider: "+c.harness, 1)
			if _, err := e.app.CreateTask(t.Context(), TaskInput{YAML: doc}); err != nil {
				t.Fatalf("CreateTask(unread on %s) = %v, want no error", c.harness, err)
			}
			h := map[string]*fakeHarness{domain.HarnessClaudeCode: e.claude, domain.HarnessCodex: e.codex}[c.harness]
			h.setQuota(func() (domain.Quota, error) { return domain.Quota{}, c.timedOut })
			r, _ := e.app.RunTask("unread")
			x := e.wait(r.ExecutionID, domain.StatusCompleted)
			if n := h.reads(); n != 1 {
				t.Errorf("%d quota reads, want 1", n)
			}
			if len(x.Attempts) != 1 || x.Attempts[0].Harness != c.harness || x.Attempts[0].Outcome != domain.AttemptSucceeded {
				t.Errorf("attempts %+v, want one on %s that succeeded", x.Attempts, c.harness)
			}
			// A Codex task that allows no extra usage is also warned that nothing stops Codex once it runs.
			var unread []domain.Warning
			for _, w := range x.Warnings {
				if w.Key == "quota_unread" {
					unread = append(unread, w)
				}
			}
			if len(unread) != 1 || unread[0].Subject != c.harness || unread[0].Params["harness"] != h.Name() ||
				!strings.Contains(unread[0].Message, c.detail) {
				t.Errorf("warnings %+v, want one quota_unread naming %s and %q", x.Warnings, c.harness, c.detail)
			}
		})
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

// A task that allows extra usage, whose Claude Code session turns to extra usage and then fails for a reason of its
// own, ends as that failure: the attempt is not ended for its extra usage, its error is not read as an exhausted quota,
// and the execution does not move to its fallback. The pause after the report gives a stop the time to end the attempt
// first, were it applied whatever the task allows.
func TestExtraUsageAllowedEndsOnTheSessionsOwnFailure(t *testing.T) {
	e := newEnv(t)
	e.create("generous", codexFallback+"  execution:\n    allowExtraUsage: true\n")
	e.claude.setScript(func(ports.RunRequest) string { return "echo 'EXTRA usage'; sleep 1; echo 'END error'" })
	r, _ := e.app.RunTask("generous")
	x := e.wait(r.ExecutionID, domain.StatusFailed)
	if len(x.Attempts) != 1 || x.Attempts[0].Harness != domain.HarnessClaudeCode ||
		x.Attempts[0].Outcome != domain.AttemptFailed || x.Attempts[0].ErrorKey == "extra_usage_stopped" {
		t.Fatalf("attempts %+v; want one Claude Code attempt that failed on its own error", x.Attempts)
	}
	if x.HarnessSwitches != 0 {
		t.Errorf("%d harness switches, want 0", x.HarnessSwitches)
	}
	if n := e.codex.promptCount(); n != 0 {
		t.Errorf("Codex got %d prompts, want 0", n)
	}
	counts, _ := e.eventCounts(r.ExecutionID)
	for _, ev := range []string{domain.EventHarnessSwitched, domain.EventHarnessQuotaExhausted} {
		if counts[ev] != 0 {
			t.Errorf("%d %s events, want 0", counts[ev], ev)
		}
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

// sessionAt is a Claude Code quota whose session window is used at percent until a known time.
func sessionAt(percent float64) func() (domain.Quota, error) {
	return func() (domain.Quota, error) {
		return domain.Quota{Windows: []domain.QuotaWindow{{Name: "Current session", UsedPercent: percent,
			ResetsAt: time.Date(2026, 10, 20, 13, 0, 0, 0, time.UTC)}}}, nil
	}
}

// setShare sets the share of Claude Code's quota HarmFlow may spend, through the settings.
func setShare(t *testing.T, e *env, share float64) {
	t.Helper()
	if _, err := e.app.UpdateSettings(SettingsPatch{Quota: map[string]QuotaShare{
		domain.HarnessClaudeCode: {MaxPercent: share}}}); err != nil {
		t.Fatalf("UpdateSettings(quota.claude-code.maxPercent = %v) = %v, want no error", share, err)
	}
}

// With a share of 95, a reading below it starts the attempt on Claude Code, and a reading at it moves the execution to
// its fallback before the session opens, with an event that names Claude Code, the share, and when the window resets.
// With no fallback, the execution fails with that reason.
func TestQuotaShareHoldsTheAttemptBack(t *testing.T) {
	cases := []struct {
		name      string
		used      float64
		wantCodex bool
	}{
		{"a reading below the share starts", 94, false},
		{"a reading at the share does not start", 95, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			setShare(t, e, 95)
			e.create("shared", codexFallback)
			e.claude.setQuota(sessionAt(c.used))
			r, _ := e.app.RunTask("shared")
			x := e.wait(r.ExecutionID, domain.StatusCompleted)
			want := domain.HarnessClaudeCode
			if c.wantCodex {
				want = domain.HarnessCodex
			}
			if len(x.Attempts) != 1 || x.Attempts[0].Harness != want {
				t.Fatalf("attempts %+v, want one on %s", x.Attempts, want)
			}
			if !c.wantCodex {
				return
			}
			if n := e.claude.promptCount(); n != 0 {
				t.Errorf("Claude Code got %d prompts, want 0", n)
			}
			_, last := e.eventCounts(r.ExecutionID)
			exhausted := last[domain.EventHarnessQuotaExhausted]
			text, _ := exhausted.Payload["error"].(string)
			if exhausted.Harness != domain.HarnessClaudeCode || !strings.Contains(text, "95%") ||
				!strings.Contains(text, "2026-10-20 13:00 UTC") {
				t.Errorf("%s = %+v, want it to name Claude Code, the share of 95%%, and the reset",
					domain.EventHarnessQuotaExhausted, exhausted)
			}
		})
	}

	e := newEnv(t)
	setShare(t, e, 95)
	e.create("alone", "")
	e.claude.setQuota(sessionAt(95))
	r, _ := e.app.RunTask("alone")
	x := e.wait(r.ExecutionID, domain.StatusFailed)
	if len(x.Attempts) != 0 || x.ErrorParams["detailKey"] != "quota_share_reached_until" {
		t.Errorf("attempts %+v, error %q (%s %v); want no attempt and the share as the reason", x.Attempts, x.ErrorMessage,
			x.ErrorKey, x.ErrorParams)
	}
}

// With no share set, a harness may spend the whole of each window: a reading at 99.9% starts, as before the share.
func TestQuotaShareDefaultKeepsTheWholeWindow(t *testing.T) {
	e := newEnv(t)
	e.create("default", codexFallback)
	e.claude.setQuota(sessionAt(99.9))
	r, _ := e.app.RunTask("default")
	x := e.wait(r.ExecutionID, domain.StatusCompleted)
	if len(x.Attempts) != 1 || x.Attempts[0].Harness != domain.HarnessClaudeCode {
		t.Errorf("attempts %+v, want one on Claude Code", x.Attempts)
	}
	if got := e.app.GetSettings().Quota; len(got) != 0 {
		t.Errorf("GetSettings().Quota = %v, want no share set", got)
	}
}

// A window that reaches the share while an attempt runs does not end it: the attempt runs to its end, and the reading
// it reported holds the next attempt back, within five minutes, with no read.
func TestQuotaShareNeverEndsARunningAttempt(t *testing.T) {
	e := newEnv(t)
	setShare(t, e, 95)
	e.create("running", codexFallback)
	e.claude.setQuota(sessionAt(10))
	e.claude.setScript(func(ports.RunRequest) string { return "echo 'WINDOW 96'; sleep 1; echo 'END ok'" })
	r, _ := e.app.RunTask("running")
	x := e.wait(r.ExecutionID, domain.StatusCompleted)
	if len(x.Attempts) != 1 || x.Attempts[0].Harness != domain.HarnessClaudeCode ||
		x.Attempts[0].Outcome != domain.AttemptSucceeded || x.HarnessSwitches != 0 {
		t.Fatalf("attempts %+v, %d switches; want the Claude Code attempt to run to its end", x.Attempts, x.HarnessSwitches)
	}
	reads := e.claude.reads()
	r, _ = e.app.RunTask("running")
	x = e.wait(r.ExecutionID, domain.StatusCompleted)
	if len(x.Attempts) != 1 || x.Attempts[0].Harness != domain.HarnessCodex || e.claude.reads() != reads {
		t.Errorf("attempts %+v, %d new reads; want Codex, held back on the reading the attempt reported", x.Attempts,
			e.claude.reads()-reads)
	}
}

// harmflow-app#977: a Claude Code session whose usage an admin disabled, replayed from the stream Claude Code 2.1.309
// printed and read by the Claude Code adapter, is not retried and does not wait for its reset. With a fallback that
// does not fail over on quota, the execution fails after that one attempt with the CLI's message; with no fallback, it
// fails as having no alternative, the CLI's message as the detail. The rate limit's short wait and single retry make a
// retry show as a second attempt and a harness.rate_limited event, not as a test waiting for the reset.
func TestAdminDisabledUsageFailsAtOnce(t *testing.T) {
	const message = "Your usage allocation has been disabled by your admin · run /usage-credits to ask your admin for a higher limit"
	const rateLimit = "  execution:\n    rateLimit:\n      maxRetries: 1\n      initialWaitSeconds: 1\n      maxWaitSeconds: 1\n"
	fixture, err := filepath.Abs(filepath.Join("..", "adapters", "harness", "claudecode", "testdata", "admin-disabled.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	script := "cat '" + strings.ReplaceAll(fixture, "'", `'\''`) + "'; exit 1"
	cases := []struct {
		name, extra string
		key         string // the execution's error key; none for the CLI's own message
	}{
		{"failover off", "    fallback:\n      on: [harness_unavailable]\n      candidates:\n        - provider: codex\n" + rateLimit, ""},
		{"no next harness", rateLimit, "no_alternative_harness_detail"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.claude.mu.Lock()
			e.claude.parsedBy = &claudecode.Adapter{Home: t.TempDir(), PathEnv: t.TempDir(), KnownLocations: []string{}}
			e.claude.mu.Unlock()
			e.claude.setScript(func(ports.RunRequest) string { return script })
			e.create("disabled", c.extra)
			r, _ := e.app.RunTask("disabled")
			x := e.wait(r.ExecutionID, domain.StatusFailed)
			if len(x.Attempts) != 1 || x.Attempts[0].Harness != domain.HarnessClaudeCode ||
				x.Attempts[0].Outcome != domain.AttemptQuotaExhausted || x.HarnessSwitches != 0 {
				t.Fatalf("attempts %+v, %d switches; want one quota-exhausted Claude Code attempt and no switch", x.Attempts,
					x.HarnessSwitches)
			}
			if x.ErrorKey != c.key || !strings.Contains(x.ErrorMessage, message) {
				t.Errorf("the execution's error is %q (%q), want %q with the CLI's message", x.ErrorMessage, x.ErrorKey, c.key)
			}
			if n := e.codex.promptCount(); n != 0 {
				t.Errorf("Codex got %d prompts, want 0", n)
			}
			counts, _ := e.eventCounts(r.ExecutionID)
			if counts[domain.EventHarnessRateLimited] != 0 || counts[domain.EventHarnessSwitched] != 0 {
				t.Errorf("%d %s and %d %s events, want none", counts[domain.EventHarnessRateLimited],
					domain.EventHarnessRateLimited, counts[domain.EventHarnessSwitched], domain.EventHarnessSwitched)
			}
		})
	}
}

// A task that allows extra usage is not held back by the share.
func TestQuotaShareIgnoredWithExtraUsage(t *testing.T) {
	e := newEnv(t)
	setShare(t, e, 95)
	e.create("generous", codexFallback+"  execution:\n    allowExtraUsage: true\n")
	e.claude.setQuota(sessionAt(95))
	r, _ := e.app.RunTask("generous")
	x := e.wait(r.ExecutionID, domain.StatusCompleted)
	if len(x.Attempts) != 1 || x.Attempts[0].Harness != domain.HarnessClaudeCode || x.HarnessSwitches != 0 {
		t.Errorf("attempts %+v, %d switches; want one Claude Code attempt", x.Attempts, x.HarnessSwitches)
	}
}
