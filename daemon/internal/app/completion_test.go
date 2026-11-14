package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// runAgain is the completion block of a task that runs again, up to limit runs again in a row.
func runAgain(limit int) string {
	return fmt.Sprintf("  completion:\n    policy: run_again\n    runAgainLimit: %d\n", limit)
}

// settled waits until the task has n executions, every one ended, and the task is free, then returns them newest
// first. An execution more than n, which a run again past the limit would add, fails the test.
func (e *env) settled(task string, n int) []ExecutionSummary {
	e.t.Helper()
	var runs []ExecutionSummary
	e.poll(30*time.Second, func() bool {
		runs = e.executions(task)
		if len(runs) != n {
			return false
		}
		for _, r := range runs {
			if !r.Status.IsTerminal() {
				return false
			}
		}
		e.app.mu.Lock()
		defer e.app.mu.Unlock()
		_, busy := e.app.running[task]
		return !busy
	})
	if len(runs) != n {
		e.t.Fatalf("task %s has %d executions, want %d: %+v", task, len(runs), n, runs)
	}
	for _, r := range runs {
		if !r.Status.IsTerminal() {
			e.t.Fatalf("execution %s of task %s is %s, want it ended", r.ID, task, r.Status)
		}
	}
	return runs
}

// eventsOf lists the events of an execution's log with the type.
func (e *env) eventsOf(id, eventType string) []domain.Event {
	e.t.Helper()
	events, _, err := e.app.ExecutionEvents(id, 0, 0)
	if err != nil {
		e.t.Fatalf("ExecutionEvents(%s): %v", id, err)
	}
	var out []domain.Event
	for _, ev := range events {
		if ev.Type == eventType {
			out = append(out, ev)
		}
	}
	return out
}

// gated is a stand-in session that waits for the file go in dir, then ends completed; once the file exists, every
// session ends at once.
func gated(dir string) func(ports.RunRequest) string {
	return func(ports.RunRequest) string {
		return fmt.Sprintf("until [ -f %s ]; do sleep 0.05; done; echo 'END ok'", filepath.Join(dir, "go"))
	}
}

// With a limit of 2, one scheduled firing gives three executions: the scheduled one and two runs again, each recording
// the one it follows. The third records the limit and notifies the user, though the task notifies no event, and the
// next scheduled firing starts a new sequence, its count from zero again.
func TestRunAgainStopsAtItsLimit(t *testing.T) {
	e := newEnv(t)
	e.claude.setScript(func(ports.RunRequest) string { return "echo 'END ok'" })
	e.create("loop", "  schedule:\n    interval: 1h\n"+runAgain(2)+"  notifications:\n    native:\n      events: []\n")
	e.clock.Advance(time.Hour + 10*time.Second)
	e.app.Tick()
	runs := e.settled("loop", 3)
	slices.Reverse(runs) // oldest first
	wantTriggers := []domain.Trigger{domain.TriggerSchedule, domain.TriggerRunAgain, domain.TriggerRunAgain}
	for i, r := range runs {
		x, err := e.app.GetExecution(r.ID)
		if err != nil {
			t.Fatalf("GetExecution(%s): %v", r.ID, err)
		}
		if x.Status != domain.StatusCompleted || x.Trigger != wantTriggers[i] || x.RunAgainCount != i {
			t.Errorf("execution %d: status %s, trigger %s, runAgainCount %d; want completed, %s, %d", i, x.Status,
				x.Trigger, x.RunAgainCount, wantTriggers[i], i)
		}
		wantFollows := ""
		if i > 0 {
			wantFollows = runs[i-1].ID
		}
		if x.FollowsExecutionID != wantFollows {
			t.Errorf("execution %d follows %q, want %q", i, x.FollowsExecutionID, wantFollows)
		}
	}
	for i, r := range runs {
		reached := e.eventsOf(r.ID, domain.EventRunAgainLimitReached)
		if i < 2 && len(reached) != 0 {
			t.Errorf("execution %d records the run-again limit: %+v", i, reached)
		}
		if i == 2 && (len(reached) != 1 || reached[0].MessageKey != "run_again_limit_reached" ||
			reached[0].Params["count"] != "2") {
			t.Errorf("events %s of the execution at the limit = %+v, want one with count 2",
				domain.EventRunAgainLimitReached, reached)
		}
	}
	notices := e.app.Notifications(false)
	if len(notices) != 1 || notices[0].EventType != domain.EventRunAgainLimitReached ||
		notices[0].ExecutionID != runs[2].ID || notices[0].Params["count"] != "2" {
		t.Errorf("Notifications() = %+v, want the run-again limit's notice alone, for %s", notices, runs[2].ID)
	}

	view, err := e.app.GetTask("loop")
	if err != nil || view.NextFireAt == nil {
		t.Fatalf("GetTask(loop) = %+v, %v; want its next firing", view, err)
	}
	e.clock.Advance(view.NextFireAt.Sub(e.clock.Now()) + 10*time.Second)
	e.app.Tick()
	next := e.settled("loop", 6)[:3]
	slices.Reverse(next)
	for i, r := range next {
		x, _ := e.app.GetExecution(r.ID)
		if x.Trigger != wantTriggers[i] || x.RunAgainCount != i {
			t.Errorf("the next sequence's execution %d: trigger %s, runAgainCount %d; want %s, %d", i, x.Trigger,
				x.RunAgainCount, wantTriggers[i], i)
		}
	}
}

// A scheduled firing waiting under the overlap policy queue when an execution ends completed takes the run again:
// exactly one new execution starts, it keeps its own trigger, records the execution it follows, and goes on with the
// sequence's count instead of starting it again.
func TestRunAgainCoalescesWithAWaitingFiring(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	e.claude.setScript(gated(dir))
	e.create("busy", "  schedule:\n    interval: 1h\n"+runAgain(1))
	first, err := e.app.RunTask("busy")
	if err != nil || first.Outcome != "started" {
		t.Fatalf("RunTask(busy) = %+v, %v; want started", first, err)
	}
	e.wait(first.ExecutionID, domain.StatusRunning)
	e.clock.Advance(time.Hour + 10*time.Second)
	e.app.Tick()
	runs := e.executions("busy")
	if len(runs) != 2 || runs[0].Status != domain.StatusQueued || runs[0].Trigger != domain.TriggerSchedule {
		t.Fatalf("executions once the scheduled firing came due: %+v, want it queued", runs)
	}
	waiting := runs[0].ID
	if err := os.WriteFile(filepath.Join(dir, "go"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	runs = e.settled("busy", 2)
	x, _ := e.app.GetExecution(waiting)
	if x.Status != domain.StatusCompleted || x.Trigger != domain.TriggerSchedule ||
		x.FollowsExecutionID != first.ExecutionID || x.RunAgainCount != 1 {
		t.Errorf("the coalesced firing: status %s, trigger %s, follows %q, runAgainCount %d; want completed, schedule, %q, 1",
			x.Status, x.Trigger, x.FollowsExecutionID, x.RunAgainCount, first.ExecutionID)
	}
	coalesced := e.eventsOf(waiting, domain.EventFiringCoalesced)
	if len(coalesced) != 1 || coalesced[0].Payload["trigger"] != string(domain.TriggerRunAgain) ||
		coalesced[0].Payload["followsExecutionId"] != first.ExecutionID {
		t.Errorf("firing.coalesced of the waiting firing = %+v, want the run again", coalesced)
	}
	// Its count is the sequence's, so at a limit of 1 it fires none and records the limit.
	if reached := e.eventsOf(waiting, domain.EventRunAgainLimitReached); len(reached) != 1 {
		t.Errorf("events %s of the coalesced firing = %+v, want one", domain.EventRunAgainLimitReached, reached)
	}
}

// Only a completed execution runs again: one that ends no_work, failed, needs_attention, or cancelled leaves the task to
// its schedule. TestRunAgainNotAfterAnInterruption covers interrupted.
func TestRunAgainOnlyAfterCompleted(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script func(ports.RunRequest) string
		cancel bool
		want   domain.ExecutionStatus
	}{
		{name: "no work", want: domain.StatusNoWork, script: func(req ports.RunRequest) string {
			return fmt.Sprintf(`printf '{"outcome":"no_work"}' > %s/report.json; echo 'END ok'`, req.ExecutionDir)
		}},
		{name: "failed", want: domain.StatusFailed, script: func(req ports.RunRequest) string {
			return fmt.Sprintf(`printf '{"outcome":"failed","summary":"broke"}' > %s/report.json; echo 'END ok'`,
				req.ExecutionDir)
		}},
		{name: "needs attention", want: domain.StatusNeedsAttention, script: func(req ports.RunRequest) string {
			return fmt.Sprintf(`printf '{"outcome":"needs_attention","summary":"asks"}' > %s/report.json; echo 'END ok'`,
				req.ExecutionDir)
		}},
		{name: "cancelled", want: domain.StatusCancelled, cancel: true, script: func(ports.RunRequest) string { return slow }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.claude.setScript(tc.script)
			e.create("once", runAgain(5))
			r, err := e.app.RunTask("once")
			if err != nil {
				t.Fatalf("RunTask(once): %v", err)
			}
			if tc.cancel {
				e.wait(r.ExecutionID, domain.StatusRunning)
				if err := e.app.CancelExecution(r.ExecutionID); err != nil {
					t.Fatal(err)
				}
			}
			e.wait(r.ExecutionID, tc.want)
			runs := e.settled("once", 1)
			if runs[0].Trigger != domain.TriggerManual {
				t.Errorf("the one execution's trigger is %s, want manual", runs[0].Trigger)
			}
		})
	}
}

// An execution the daemon's stop interrupts fires no run again: after the restart the task holds that execution alone.
func TestRunAgainNotAfterAnInterruption(t *testing.T) {
	e := newEnv(t)
	e.create("loop", runAgain(5))
	restarted, id := stopHarness(t, e, "loop", signalEnds, 128+int(syscall.SIGTERM))
	runs := restarted.settled("loop", 1)
	if runs[0].ID != id || runs[0].Status != domain.StatusInterrupted {
		t.Errorf("executions(loop) after the restart = %+v, want %s alone, interrupted", runs, id)
	}
}

// A completed execution runs again whether or not its tests failed: a draft whose tests failed still fires the task
// again.
func TestRunAgainAfterFailedTests(t *testing.T) {
	e := newEnv(t)
	e.claude.setScript(func(req ports.RunRequest) string {
		report := `{"outcome":"completed","summary":"opened a draft","workItems":[{"key":"o/r#7","outcome":"completed",` +
			`"pullRequest":{"url":"https://github.com/o/r/pull/9","created":true},"tests":{"status":"failed"}}]}`
		return fmt.Sprintf(`echo '{"type":"work_item.selected","workItem":"o/r#7","message":"Fix it"}' >> %[1]s/progress.jsonl
sleep 1.2; printf '%%s' '%[2]s' > %[1]s/report.json; echo 'END ok'`, req.ExecutionDir, report)
	})
	e.create("drafts", runAgain(1))
	r, err := e.app.RunTask("drafts")
	if err != nil {
		t.Fatalf("RunTask(drafts): %v", err)
	}
	runs := e.settled("drafts", 2)
	first, _ := e.app.GetExecution(r.ExecutionID)
	if first.Status != domain.StatusCompleted || first.TestsStatus != "failed" {
		t.Errorf("GetExecution(%s): status %s, tests %q; want completed, failed", r.ExecutionID, first.Status,
			first.TestsStatus)
	}
	again, _ := e.app.GetExecution(runs[0].ID)
	if again.Trigger != domain.TriggerRunAgain || again.FollowsExecutionID != r.ExecutionID {
		t.Errorf("GetExecution(%s): trigger %s, follows %q; want run_again, %q", runs[0].ID, again.Trigger,
			again.FollowsExecutionID, r.ExecutionID)
	}
}

// The policy applies whatever fired the ending execution: a catch-up firing that ends completed runs again too.
func TestRunAgainFollowsACatchUp(t *testing.T) {
	e := newEnv(t)
	e.claude.setScript(func(ports.RunRequest) string { return "echo 'END ok'" })
	e.create("daily", "  schedule:\n    cron: \"0 9 * * *\"\n    timezone: UTC\n"+runAgain(1))
	e.clock.Advance(3 * 24 * time.Hour) // the machine was off for three days
	e.app.Tick()
	runs := e.settled("daily", 2)
	slices.Reverse(runs) // oldest first
	if runs[0].Trigger != domain.TriggerCatchUp {
		t.Fatalf("executions(daily)[0]: trigger %s, want catch_up", runs[0].Trigger)
	}
	again, _ := e.app.GetExecution(runs[1].ID)
	if again.Trigger != domain.TriggerRunAgain || again.FollowsExecutionID != runs[0].ID || again.RunAgainCount != 1 {
		t.Errorf("GetExecution(%s): trigger %s, follows %q, runAgainCount %d; want run_again, %q, 1", runs[1].ID,
			again.Trigger, again.FollowsExecutionID, again.RunAgainCount, runs[0].ID)
	}
}

// contextExecution is the execution object of the context.json the daemon wrote for x's session.
func (e *env) contextExecution(x ExecutionView) map[string]any {
	e.t.Helper()
	data, err := os.ReadFile(filepath.Join(x.ExecutionDir, "context.json"))
	if err != nil {
		e.t.Fatalf("reading the context.json of %s: %v", x.ID, err)
	}
	var doc struct {
		Execution map[string]any `json:"execution"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		e.t.Fatalf("json.Unmarshal(the context.json of %s): %v", x.ID, err)
	}
	return doc.Execution
}

// A run again's session reads its trigger, run_again, and the execution it follows in context.json; the execution
// that started the sequence reads its own trigger and follows none.
func TestRunAgainReachesTheSessionContext(t *testing.T) {
	e := newEnv(t)
	e.claude.setScript(func(ports.RunRequest) string { return "echo 'END ok'" })
	e.create("loop", runAgain(1))
	r, err := e.app.RunTask("loop")
	if err != nil {
		t.Fatalf("RunTask(loop): %v", err)
	}
	runs := e.settled("loop", 2)
	first, _ := e.app.GetExecution(r.ExecutionID)
	if got := e.contextExecution(first); got["trigger"] != string(domain.TriggerManual) || got["followsExecutionId"] != nil {
		t.Errorf("the context.json of %s holds execution %v, want the trigger manual and no followsExecutionId",
			first.ID, got)
	}
	again, _ := e.app.GetExecution(runs[0].ID)
	if got := e.contextExecution(again); got["trigger"] != string(domain.TriggerRunAgain) ||
		got["followsExecutionId"] != r.ExecutionID {
		t.Errorf("the context.json of %s holds execution %v, want the trigger run_again and followsExecutionId %q",
			again.ID, got, r.ExecutionID)
	}
}

// A sequence that ends with a result other than completed ends there: the next firing starts a new sequence, its count
// from zero, with the whole limit of runs again before it.
func TestRunAgainCountStartsAgainAfterAFailure(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	// The first run again fails, and leaves the file failed behind it so that no later one does.
	e.claude.setScript(func(req ports.RunRequest) string {
		return fmt.Sprintf(`if grep -q '"trigger": "run_again"' %[1]s/context.json && [ ! -e %[2]s/failed ]; then
  touch %[2]s/failed; printf '{"outcome":"failed","summary":"broke"}' > %[1]s/report.json
fi
echo 'END ok'`, req.ExecutionDir, dir)
	})
	e.create("loop", runAgain(2))
	if _, err := e.app.RunTask("loop"); err != nil {
		t.Fatalf("RunTask(loop): %v", err)
	}
	ended := e.settled("loop", 2)
	if ended[0].Status != domain.StatusFailed || ended[0].Trigger != domain.TriggerRunAgain {
		t.Fatalf("the first sequence's last execution: status %s, trigger %s; want failed, run_again", ended[0].Status,
			ended[0].Trigger)
	}
	if _, err := e.app.RunTask("loop"); err != nil {
		t.Fatalf("RunTask(loop) after the failure: %v", err)
	}
	next := e.settled("loop", 5)[:3]
	slices.Reverse(next) // oldest first
	wantTriggers := []domain.Trigger{domain.TriggerManual, domain.TriggerRunAgain, domain.TriggerRunAgain}
	for i, r := range next {
		x, _ := e.app.GetExecution(r.ID)
		if x.Status != domain.StatusCompleted || x.Trigger != wantTriggers[i] || x.RunAgainCount != i {
			t.Errorf("GetExecution(%s), execution %d of the new sequence: status %s, trigger %s, runAgainCount %d; "+
				"want completed, %s, %d", r.ID, i, x.Status, x.Trigger, x.RunAgainCount, wantTriggers[i], i)
		}
	}
	if reached := e.eventsOf(next[2].ID, domain.EventRunAgainLimitReached); len(reached) != 1 {
		t.Errorf("events %s of %s = %+v, want one: the new sequence reached its limit", domain.EventRunAgainLimitReached,
			next[2].ID, reached)
	}
}

// The policy is read from the task's definition when the execution ends: a task disabled, or set back to
// wait_for_schedule, while its execution ran fires no run again, and neither does a daemon that drains.
func TestRunAgainReadsTheCurrentDefinition(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(e *env)
	}{
		{name: "disabled", change: func(e *env) {
			if _, err := e.app.SetEnabled(e.t.Context(), "loop", false); err != nil {
				e.t.Fatalf("SetEnabled(loop, false): %v", err)
			}
		}},
		{name: "set back to wait_for_schedule", change: func(e *env) {
			view, err := e.app.GetTask("loop")
			if err != nil {
				e.t.Fatal(err)
			}
			changed := strings.Replace(view.YAML, "policy: run_again", "policy: wait_for_schedule", 1)
			if changed == view.YAML {
				e.t.Fatalf("the task's YAML holds no run_again policy:\n%s", view.YAML)
			}
			if _, err := e.app.UpdateTask(e.t.Context(), "loop", TaskInput{YAML: changed, BaseRevision: view.Revision}); err != nil {
				e.t.Fatalf("UpdateTask(loop): %v", err)
			}
		}},
		{name: "draining", change: func(e *env) {
			if err := e.app.RequestDrain(time.Minute); err != nil {
				e.t.Fatalf("RequestDrain(1m): %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			dir := t.TempDir()
			e.claude.setScript(gated(dir))
			e.create("loop", runAgain(5))
			r, err := e.app.RunTask("loop")
			if err != nil {
				t.Fatalf("RunTask(loop): %v", err)
			}
			e.wait(r.ExecutionID, domain.StatusRunning)
			tc.change(e)
			if err := os.WriteFile(filepath.Join(dir, "go"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			e.wait(r.ExecutionID, domain.StatusCompleted)
			e.settled("loop", 1)
		})
	}
}

// A script task takes no completion policy: a file that sets one fails validation.
func TestScriptTaskTakesNoCompletionPolicy(t *testing.T) {
	e := newEnv(t)
	yaml := strings.Replace(taskYAML("prog", ""), "  type: general\n  prompt: Do the work of prog.\n  harness:\n"+
		"    preferred:\n      provider: claude-code\n", "  type: script\n  script:\n    command: [/bin/true]\n", 1)
	if _, err := e.app.CreateTask(t.Context(), TaskInput{YAML: yaml}); err != nil {
		t.Fatalf("CreateTask(a script task with no completion policy): %v", err)
	}
	withPolicy := strings.Replace(yaml, "id: prog", "id: prog-again", 1) + runAgain(3)
	if _, err := e.app.CreateTask(t.Context(), TaskInput{YAML: withPolicy}); err == nil {
		t.Error("CreateTask(a script task with a completion policy) = nil error, want a validation error")
	}
}

// A task that sets no completion policy waits for its schedule after a completed execution, as before the policy
// existed.
func TestNoCompletionPolicyWaitsForTheSchedule(t *testing.T) {
	e := newEnv(t)
	e.claude.setScript(func(ports.RunRequest) string { return "echo 'END ok'" })
	e.create("plain", "")
	r, err := e.app.RunTask("plain")
	if err != nil {
		t.Fatalf("RunTask(plain): %v", err)
	}
	e.wait(r.ExecutionID, domain.StatusCompleted)
	e.settled("plain", 1)
}
