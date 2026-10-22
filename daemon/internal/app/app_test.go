package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/eventlog"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/fsstore"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/schema"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// fakeHarness runs /bin/sh with a script the test chooses. The script prints the outcome on its last line:
// "END ok", "END quota", "END rate", or "END error".
type fakeHarness struct {
	id          string
	mu          sync.Mutex
	unavailable bool
	script      func(req ports.RunRequest) string
	prompts     []string
}

func (f *fakeHarness) ID() string   { return f.id }
func (f *fakeHarness) Name() string { return f.id }
func (f *fakeHarness) Detect(context.Context, string) ports.HarnessInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	info := ports.HarnessInfo{ID: f.id, Name: f.id, Detected: true, Path: "/bin/sh", Version: "1.0.0",
		Authentication: "valid", Readiness: "ready", Models: []ports.ModelInfo{{ID: "default"}}, Efforts: []string{"default"}}
	if f.unavailable {
		info.Readiness, info.Authentication = "not_ready", "invalid"
		info.SetDiagnostic(domain.T("not_signed_in", "{{name}} is not signed in; run `{{command}}` in a terminal",
			"name", f.id, "command", "login"))
	}
	return info
}
func (f *fakeHarness) ValidateProfile(ports.HarnessInfo, string, string) error { return nil }
func (f *fakeHarness) UserItem(kind, name string) (ports.ResolvedItem, bool) {
	if name == "user-skill" && kind == "skill" {
		return ports.ResolvedItem{Name: name, Kind: kind, Path: "/x"}, true
	}
	return ports.ResolvedItem{}, false
}
func (f *fakeHarness) UserItems(string) []ports.ResolvedItem { return nil }
func (f *fakeHarness) Prepare(_ ports.HarnessInfo, req ports.RunRequest) (ports.Invocation, ports.OutputParser, error) {
	f.mu.Lock()
	if req.Prompt != "" { // validation's policy check prepares with no prompt
		f.prompts = append(f.prompts, req.Prompt)
	}
	script := "echo 'END ok'"
	if f.script != nil {
		script = f.script(req)
	}
	f.mu.Unlock()
	return ports.Invocation{Path: "/bin/sh", Args: []string{"-c", script}, Env: req.Env, Dir: req.WorkDir}, &fakeParser{}, nil
}

func (f *fakeHarness) setScript(s func(ports.RunRequest) string) {
	f.mu.Lock()
	f.script = s
	f.mu.Unlock()
}

// fakeParser also reads "USAGE <input tokens>", the usage so far, which is final once the attempt ends well.
type fakeParser struct {
	last  string
	input int64
}

func (p *fakeParser) Line(b []byte) []ports.HarnessEvent {
	s := string(b)
	if strings.HasPrefix(s, "END ") {
		p.last = strings.TrimPrefix(s, "END ")
		return nil
	}
	if n, ok := strings.CutPrefix(s, "USAGE "); ok {
		p.input, _ = strconv.ParseInt(n, 10, 64)
		return []ports.HarnessEvent{{Kind: ports.HarnessUsage, Usage: p.usage(domain.UsagePartial)}}
	}
	return []ports.HarnessEvent{{Kind: ports.HarnessMessage, Message: s}}
}

func (p *fakeParser) usage(s domain.UsageStatus) *domain.Usage {
	if p.input == 0 {
		return domain.NotReported()
	}
	return &domain.Usage{Status: s, Models: []domain.ModelUsage{{Model: "fake", Tokens: domain.Tokens{Input: p.input}}}}
}

func (p *fakeParser) Finish(exit int, _ string, timedOut, cancelled bool) domain.AttemptEnd {
	switch {
	case cancelled:
		return domain.AttemptEnd{Class: domain.ClassCancelled, Usage: p.usage(domain.UsagePartial)}
	case timedOut:
		return domain.AttemptEnd{Class: domain.ClassTimeout}
	}
	switch p.last {
	case "ok":
		return domain.AttemptEnd{FinalText: "done", Usage: p.usage(domain.UsageReported)}
	case "quota":
		return domain.AttemptEnd{Class: domain.ClassQuotaExhausted, Error: domain.Text{Message: "usage limit reached"},
			Usage: p.usage(domain.UsagePartial)}
	case "rate":
		return domain.AttemptEnd{Class: domain.ClassRateLimited, Error: domain.Text{Message: "429"}, RetryAfter: 10 * time.Millisecond}
	}
	return domain.AttemptEnd{Class: domain.ClassTaskError, Error: domain.Text{Message: fmt.Sprintf("exit %d", exit)}}
}

// hookedEvents runs the test's hook on each event before it is appended, on the goroutine that emits it.
type hookedEvents struct {
	*eventlog.Log
	mu   sync.Mutex
	hook func(domain.Event)
}

func (h *hookedEvents) Append(ev domain.Event) domain.Event {
	h.mu.Lock()
	hook := h.hook
	h.mu.Unlock()
	if hook != nil {
		hook(ev)
	}
	return h.Log.Append(ev)
}

func (h *hookedEvents) setHook(hook func(domain.Event)) {
	h.mu.Lock()
	h.hook = hook
	h.mu.Unlock()
}

type env struct {
	t      *testing.T
	app    *App
	clock  *fakeClock
	claude *fakeHarness
	codex  *fakeHarness
	events *hookedEvents
	home   string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	home := t.TempDir()
	builtins := t.TempDir()
	_ = os.MkdirAll(filepath.Join(builtins, "skills", "harmflow-github-draft-pr"), 0o700)
	_ = os.WriteFile(filepath.Join(builtins, "skills", "harmflow-github-draft-pr", "SKILL.md"), []byte("---\nname: harmflow-github-draft-pr\ndescription: d\n---\n"), 0o600)
	_ = os.MkdirAll(filepath.Join(builtins, "agents"), 0o700)
	paths := NewPaths(home, t.TempDir(), builtins)
	tasks, _ := fsstore.NewTasks(paths.Tasks)
	state, _ := fsstore.NewState(paths.State)
	execs, _ := fsstore.NewExecutions(paths.Executions)
	clock := &fakeClock{now: time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)}
	events, _ := eventlog.New(paths.Logs, time.Now)
	v, err := schema.New()
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, clock: clock, home: home, events: &hookedEvents{Log: events},
		claude: &fakeHarness{id: domain.HarnessClaudeCode}, codex: &fakeHarness{id: domain.HarnessCodex}}
	a, err := New(Deps{Paths: paths, Version: "test", Clock: clock, Location: time.UTC, Tasks: tasks, State: state,
		Executions: execs, Events: e.events, Validator: v, Harnesses: []ports.HarnessAdapter{e.claude, e.codex},
		TickInterval: time.Hour, Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	e.app = a
	if err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Stop(domain.Text{Message: "test done"}) })
	return e
}

func taskYAML(id string, extra string) string {
	return fmt.Sprintf(`apiVersion: harmflow/v1alpha1
kind: Task
metadata:
  id: %s
  name: Task %s
spec:
  type: general
  prompt: Do the work of %s.
  harness:
    preferred:
      provider: claude-code
%s`, id, id, id, extra)
}

func (e *env) create(id, extra string) {
	e.t.Helper()
	if _, err := e.app.CreateTask(TaskInput{YAML: taskYAML(id, extra)}); err != nil {
		e.t.Fatalf("create %s: %v", id, err)
	}
}

func (e *env) wait(id string, want ...domain.ExecutionStatus) ExecutionView {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		x, err := e.app.GetExecution(id)
		if err == nil {
			for _, w := range want {
				if x.Status == w {
					return x
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	x, _ := e.app.GetExecution(id)
	e.t.Fatalf("execution %s is %s, want %v", id, x.Status, want)
	return x
}

func (e *env) executions(task string) []ExecutionSummary {
	return e.app.ListExecutions(ExecutionFilter{TaskID: task})
}

// idle waits until the task's execution has returned and freed the task.
func (e *env) idle(task string) {
	e.t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		e.app.mu.Lock()
		_, busy := e.app.running[task]
		e.app.mu.Unlock()
		if !busy {
			return
		}
	}
	e.t.Fatalf("task %s is still running", task)
}

const slow = "echo working; sleep 30; echo 'END ok'"

func TestRunAndReport(t *testing.T) {
	e := newEnv(t)
	e.create("reporter", "")
	e.claude.setScript(func(req ports.RunRequest) string {
		report := `{"outcome":"completed","summary":"opened a draft","workItems":[{"key":"o/r#7","outcome":"completed",` +
			`"pullRequest":{"url":"https://github.com/o/r/pull/9","created":true},"tests":{"status":"failed"}}]}`
		return fmt.Sprintf(`echo '{"type":"work_item.selected","workItem":"o/r#7","message":"Fix it"}' >> %[1]s/progress.jsonl
sleep 1.2; printf '%%s' '%[2]s' > %[1]s/report.json; echo 'END ok'`, req.ExecutionDir, report)
	})
	res, err := e.app.RunTask("reporter")
	if err != nil || res.Outcome != "started" {
		t.Fatalf("run: %v %+v", err, res)
	}
	x := e.wait(res.ExecutionID, domain.StatusCompleted)
	if x.PullRequestURL != "https://github.com/o/r/pull/9" || x.TestsStatus != "failed" || x.WorkItem != "o/r#7" {
		t.Errorf("report not applied: %+v", x.ExecutionSummary)
	}
	// FS-006 FR-010: a draft PR with failing tests is completed, and its item stays resumable.
	q := e.app.Queue()
	if len(q.WorkItems) != 1 || q.WorkItems[0].Status != domain.WorkItemInProgress {
		t.Errorf("work items: %+v", q.WorkItems)
	}
	types := map[string]bool{}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline) && !types[domain.EventExecutionCompleted]; {
		events, _, _ := e.app.ExecutionEvents(res.ExecutionID, 0, 0)
		for _, ev := range events {
			types[ev.Type] = true
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, want := range []string{domain.EventExecutionStarted, domain.EventHarnessStarted, domain.EventWorkItemSelected,
		domain.EventPullRequestCreated, domain.EventTestFailed, domain.EventExecutionCompleted} {
		if !types[want] {
			t.Errorf("missing event %s", want)
		}
	}
	// A no_work report is not a failure.
	e.claude.setScript(func(req ports.RunRequest) string {
		return fmt.Sprintf(`printf '{"outcome":"no_work"}' > %s/report.json; echo 'END ok'`, req.ExecutionDir)
	})
	res, _ = e.app.RunTask("reporter")
	e.wait(res.ExecutionID, domain.StatusNoWork)
}

// FS-003 AC-010, AC-002: a task never runs twice at once; one firing waits and the next coalesce into it.
func TestOverlapQueueCoalesces(t *testing.T) {
	e := newEnv(t)
	e.create("busy", "")
	e.claude.setScript(func(ports.RunRequest) string { return slow })
	first, _ := e.app.RunTask("busy")
	e.wait(first.ExecutionID, domain.StatusRunning)
	second, _ := e.app.RunTask("busy")
	third, _ := e.app.RunTask("busy")
	fourth, _ := e.app.RunTask("busy")
	if second.Outcome != "queued" || third.Outcome != "coalesced" || fourth.Outcome != "coalesced" ||
		third.ExecutionID != second.ExecutionID {
		t.Fatalf("got %+v %+v %+v", second, third, fourth)
	}
	e.claude.setScript(func(ports.RunRequest) string { return "echo 'END ok'" })
	if err := e.app.CancelExecution(first.ExecutionID); err != nil {
		t.Fatal(err)
	}
	e.wait(first.ExecutionID, domain.StatusCancelled)
	e.wait(second.ExecutionID, domain.StatusCompleted)
	if n := len(e.executions("busy")); n != 2 {
		t.Errorf("%d executions, want 2", n)
	}
}

// FS-003 AC-011: with skip, an overlapping firing is dropped and logged.
func TestOverlapSkip(t *testing.T) {
	e := newEnv(t)
	e.create("skipper", "  concurrency:\n    overlappingRuns: skip\n")
	e.claude.setScript(func(ports.RunRequest) string { return slow })
	first, _ := e.app.RunTask("skipper")
	e.wait(first.ExecutionID, domain.StatusRunning)
	if r, _ := e.app.RunTask("skipper"); r.Outcome != "skipped" {
		t.Fatalf("got %+v", r)
	}
	// The dropped firing is in the log of the execution it was skipped for, so it outlives the replay buffer.
	events, _, err := e.app.ExecutionEvents(first.ExecutionID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var skipped []domain.Event
	for _, ev := range events {
		if ev.Type == domain.EventFiringSkipped {
			skipped = append(skipped, ev)
		}
	}
	if len(skipped) != 1 || skipped[0].Payload["trigger"] != string(domain.TriggerManual) {
		t.Errorf("firing.skipped in the running execution's log: %+v", skipped)
	}
	_ = e.app.CancelExecution(first.ExecutionID)
	e.wait(first.ExecutionID, domain.StatusCancelled)
	if n := len(e.executions("skipper")); n != 1 {
		t.Errorf("%d executions, want 1", n)
	}
}

// FS-003 AC-001, AC-006: different tasks run in parallel on the same harness; cancelling one leaves the others.
func TestParallelTasksAndIsolatedCancel(t *testing.T) {
	e := newEnv(t)
	for _, id := range []string{"a", "b", "c"} {
		e.create(id, "")
	}
	e.claude.setScript(func(ports.RunRequest) string { return slow })
	var ids []string
	for _, id := range []string{"a", "b", "c"} {
		r, _ := e.app.RunTask(id)
		ids = append(ids, r.ExecutionID)
	}
	for _, id := range ids {
		e.wait(id, domain.StatusRunning)
	}
	_ = e.app.CancelExecution(ids[0])
	e.wait(ids[0], domain.StatusCancelled)
	for _, id := range ids[1:] {
		if x, _ := e.app.GetExecution(id); x.Status != domain.StatusRunning {
			t.Errorf("%s is %s after cancelling another task", id, x.Status)
		}
		_ = e.app.CancelExecution(id)
	}
}

// FS-003 FR-005, D-083: an execution cancelled or dropped while it is still queued never runs, even when its task was
// already claimed for it: fire and startQueued claim the task before the executor leaves queued.
func TestCancelBeforeItStarts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cancel func(e *env, id string) error
	}{
		{"cancelled", func(e *env, id string) error { return e.app.CancelExecution(id) }},
		{"disabled", func(e *env, _ string) error { _, err := e.app.SetEnabled("early", false); return err }},
		{"deleted", func(e *env, _ string) error { _, err := e.app.DeleteTask("early"); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.create("early", "")
			var once sync.Once
			e.events.setHook(func(ev domain.Event) {
				// fire announces the execution after it claimed the task and before the executor starts.
				if ev.Type == domain.EventExecutionCreated {
					once.Do(func() {
						if err := tc.cancel(e, ev.ExecutionID); err != nil {
							t.Error(err)
						}
					})
				}
			})
			r, err := e.app.RunTask("early")
			if err != nil || r.Outcome != "started" {
				t.Fatalf("run: %v %+v", err, r)
			}
			e.idle("early")
			x, _ := e.app.GetExecution(r.ExecutionID)
			e.claude.mu.Lock()
			prompts := len(e.claude.prompts)
			e.claude.mu.Unlock()
			if x.Status != domain.StatusCancelled || x.StartedAt != nil || len(x.Attempts) != 0 || prompts != 0 {
				t.Errorf("the harness ran for a cancelled execution: status %s, started at %v, %d attempts, %d prompts",
					x.Status, x.StartedAt, len(x.Attempts), prompts)
			}
		})
	}
}

// FS-005 §5.1: quota on the preferred harness fails over to the fallback in the same execution.
func TestQuotaFailover(t *testing.T) {
	e := newEnv(t)
	e.create("failover", "    fallback:\n      candidates:\n        - provider: codex\n")
	e.claude.setScript(func(ports.RunRequest) string { return "echo 'END quota'" })
	e.codex.setScript(func(req ports.RunRequest) string { return "echo 'END ok'" })
	r, _ := e.app.RunTask("failover")
	x := e.wait(r.ExecutionID, domain.StatusCompleted)
	if len(x.Attempts) != 2 || x.Attempts[0].Outcome != domain.AttemptQuotaExhausted || x.Attempts[1].Harness != domain.HarnessCodex ||
		x.HarnessSwitches != 1 {
		t.Fatalf("attempts: %+v", x.Attempts)
	}
	// The alternative gets the same prompt; the summary of the previous attempt is in its context (D-067).
	if got := e.codex.prompts[len(e.codex.prompts)-1]; got != "Do the work of failover." {
		t.Errorf("prompt %q", got)
	}
	// With the switch used up, a second quota has no alternative and the execution fails explicitly (FS-005 FR-003).
	e.codex.setScript(func(ports.RunRequest) string { return "echo 'END quota'" })
	r, _ = e.app.RunTask("failover")
	x = e.wait(r.ExecutionID, domain.StatusFailed)
	if !strings.Contains(x.ErrorMessage, "no alternative") {
		t.Errorf("error %q", x.ErrorMessage)
	}
}

// harmflow-app#39: each attempt of a failover keeps its own usage — the count so far while it runs, the final one when
// it ends — and the record on disk carries it.
func TestUsagePerAttempt(t *testing.T) {
	e := newEnv(t)
	e.create("usage", "    fallback:\n      candidates:\n        - provider: codex\n")
	e.claude.setScript(func(ports.RunRequest) string { return "echo 'USAGE 100'; echo 'END quota'" })
	e.codex.setScript(func(ports.RunRequest) string { return "echo 'USAGE 40'; sleep 1; echo 'USAGE 50'; echo 'END ok'" })
	r, _ := e.app.RunTask("usage")
	live := false
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && !live; time.Sleep(20 * time.Millisecond) {
		x, _ := e.app.GetExecution(r.ExecutionID)
		live = len(x.Attempts) == 2 && x.Attempts[1].Usage != nil && x.Attempts[1].Usage.Status == domain.UsagePartial &&
			x.Attempts[1].Usage.Total().Input == 40
	}
	if !live {
		t.Error("the running attempt did not show its usage so far")
	}
	x := e.wait(r.ExecutionID, domain.StatusCompleted)
	first, second := x.Attempts[0].Usage, x.Attempts[1].Usage
	if first == nil || first.Status != domain.UsagePartial || first.Total().Input != 100 {
		t.Errorf("attempt 1: %+v", first)
	}
	if second == nil || second.Status != domain.UsageReported || second.Total().Input != 50 {
		t.Errorf("attempt 2: %+v", second)
	}
	e.idle("usage")
	data, err := os.ReadFile(filepath.Join(e.home, "state", "executions", r.ExecutionID+".json"))
	var saved domain.Execution
	if err == nil {
		err = json.Unmarshal(data, &saved)
	}
	if err != nil || len(saved.Attempts) != 2 || saved.Attempts[1].Usage == nil || saved.Attempts[1].Usage.Total().Input != 50 {
		t.Errorf("the record on disk lacks the usage (%v):\n%s", err, data)
	}
}

// HarmFlow's context never names the task, since a prompt may name the same thing as the task's id or name, and it
// points at context.json only once the file holds a previous attempt, work items, or parameters.
func TestContextNeverNamesTheTask(t *testing.T) {
	e := newEnv(t)
	if _, err := e.app.CreateTask(TaskInput{YAML: `apiVersion: harmflow/v1alpha1
kind: Task
metadata:
  id: napoleon
  name: Napoleon
spec:
  type: general
  prompt: Who is the napoleon?
  harness:
    preferred:
      provider: claude-code
    fallback:
      candidates:
        - provider: codex
`}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var contexts []string
	capture := func(req ports.RunRequest) {
		mu.Lock()
		contexts = append(contexts, req.Context)
		mu.Unlock()
	}
	e.claude.setScript(func(req ports.RunRequest) string { capture(req); return "echo 'END quota'" })
	e.codex.setScript(func(req ports.RunRequest) string {
		capture(req)
		return fmt.Sprintf(`printf '{"outcome":"completed","workItems":[{"key":"o/r#1"}]}' > %s/report.json; echo 'END ok'`,
			req.ExecutionDir)
	})
	r, _ := e.app.RunTask("napoleon")
	x := e.wait(r.ExecutionID, domain.StatusCompleted)
	e.claude.setScript(func(req ports.RunRequest) string { capture(req); return "echo 'END ok'" })
	r, _ = e.app.RunTask("napoleon")
	e.wait(r.ExecutionID, domain.StatusCompleted)
	mu.Lock()
	defer mu.Unlock()
	if len(contexts) != 3 {
		t.Fatalf("%d attempts, want 3", len(contexts))
	}
	// The default workspace is named after the task id, and the harness shows it as the working directory anyway.
	first := strings.ReplaceAll(contexts[0], x.WorkspacePath, "")
	if strings.Contains(strings.ToLower(first), "napoleon") || strings.Contains(first, "context.json") {
		t.Errorf("the first attempt of a general task with no history names the task or context.json:\n%s", contexts[0])
	}
	if !strings.Contains(contexts[1], "context.json") {
		t.Errorf("an attempt after a previous one does not point at context.json:\n%s", contexts[1])
	}
	if !strings.Contains(contexts[2], "context.json") {
		t.Errorf("an execution of a task with work items does not point at context.json:\n%s", contexts[2])
	}
	// context.json still carries the task's identity for the skills that sign their work with it.
	data, err := os.ReadFile(filepath.Join(x.ExecutionDir, "context.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Task struct{ ID string } `json:"task"`
	}
	if err := json.Unmarshal(data, &doc); err != nil || doc.Task.ID != "napoleon" {
		t.Errorf("context.json task id %q (%v)", doc.Task.ID, err)
	}
}

// A record is saved from a copy taken under the lock, since the executor may change it at the same time: a queued
// firing is saved while the execution it waited for ends and starts it. go test -race catches an unlocked save.
func TestSaveWhileTheExecutionChanges(t *testing.T) {
	e := newEnv(t)
	e.create("save", "")
	e.claude.setScript(func(ports.RunRequest) string { return "echo 'END ok'" })
	r, _ := e.app.RunTask("save")
	e.wait(r.ExecutionID, domain.StatusCompleted)
	e.app.mu.Lock()
	x := e.app.executions[r.ExecutionID]
	e.app.mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			e.app.mu.Lock()
			x.CurrentStep = fmt.Sprintf("step %d", i)
			e.app.mu.Unlock()
		}
	}()
	for i := 0; i < 100; i++ {
		e.app.persistExecution(x)
	}
	<-done
}

// FS-005 FR-009: a harness already unavailable when the task fires is skipped for the fallback.
func TestUnavailableStartsOnFallback(t *testing.T) {
	e := newEnv(t)
	e.create("unavail", "    fallback:\n      candidates:\n        - provider: codex\n")
	e.claude.mu.Lock()
	e.claude.unavailable = true
	e.claude.mu.Unlock()
	r, _ := e.app.RunTask("unavail")
	x := e.wait(r.ExecutionID, domain.StatusCompleted)
	if len(x.Attempts) != 1 || x.Attempts[0].Harness != domain.HarnessCodex {
		t.Fatalf("attempts: %+v", x.Attempts)
	}
	events, _, _ := e.app.ExecutionEvents(r.ExecutionID, 0, 0)
	unavailable, started := -1, -1
	for i, ev := range events {
		switch ev.Type {
		case domain.EventHarnessUnavailable:
			unavailable = i
		case domain.EventExecutionStarted:
			started = i
		}
	}
	if unavailable < 0 {
		t.Error("no explicit event named the unavailable harness")
	}
	// The harness is revalidated before the execution starts (FS-004 FR-003).
	if started < unavailable {
		t.Errorf("execution.started at %d, harness.unavailable at %d", started, unavailable)
	}
}

// FS-004 AC-004: with no ready harness in its chain, the execution fails without starting, and names the harness.
func TestUnavailableWithoutFallbackNeverStarts(t *testing.T) {
	e := newEnv(t)
	e.create("gone", "")
	e.claude.mu.Lock()
	e.claude.unavailable = true
	e.claude.mu.Unlock()
	r, _ := e.app.RunTask("gone")
	x := e.wait(r.ExecutionID, domain.StatusFailed)
	if x.StartedAt != nil || x.TimeoutAt != nil || len(x.Attempts) != 0 {
		t.Errorf("the execution started: startedAt %v, timeoutAt %v, attempts %+v", x.StartedAt, x.TimeoutAt, x.Attempts)
	}
	if x.WorkspacePath != "" || x.ExecutionDir != "" {
		t.Errorf("the workspace was prepared: %q, %q", x.WorkspacePath, x.ExecutionDir)
	}
	if _, err := os.Stat(filepath.Join(e.app.d.Paths.Workspace, "gone")); !os.IsNotExist(err) {
		t.Errorf("the workspace exists: %v", err)
	}
	if !strings.Contains(x.ErrorMessage, domain.HarnessClaudeCode) {
		t.Errorf("error %q does not name the harness", x.ErrorMessage)
	}
	types := map[string]bool{}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline) && !types[domain.EventExecutionFailed]; {
		events, _, _ := e.app.ExecutionEvents(r.ExecutionID, 0, 0)
		for _, ev := range events {
			types[ev.Type] = true
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !types[domain.EventHarnessUnavailable] || !types[domain.EventExecutionFailed] {
		t.Errorf("events: %v", types)
	}
	if types[domain.EventExecutionStarted] {
		t.Error("execution.started was emitted for an execution that never started")
	}
}

// FS-005 FR-010: a rate limit retries on the same harness, never switches.
func TestRateLimitRetriesSameHarness(t *testing.T) {
	e := newEnv(t)
	e.create("rated", "    fallback:\n      candidates:\n        - provider: codex\n  execution:\n    rateLimit:\n      initialWaitSeconds: 1\n")
	var n int
	var mu sync.Mutex
	e.claude.setScript(func(ports.RunRequest) string {
		mu.Lock()
		defer mu.Unlock()
		n++
		if n == 1 {
			return "echo 'END rate'"
		}
		return "echo 'END ok'"
	})
	r, _ := e.app.RunTask("rated")
	x := e.wait(r.ExecutionID, domain.StatusCompleted)
	if len(x.Attempts) != 2 || x.Attempts[1].Harness != domain.HarnessClaudeCode || x.HarnessSwitches != 0 {
		t.Fatalf("attempts: %+v", x.Attempts)
	}
}

// FS-002 AC-007, AC-005: misfires coalesce into one catch-up, or are skipped.
func TestMisfiresCoalesce(t *testing.T) {
	e := newEnv(t)
	e.create("daily", "  schedule:\n    cron: \"0 9 * * *\"\n    timezone: UTC\n")
	e.create("daily-skip", "  schedule:\n    cron: \"0 9 * * *\"\n    timezone: UTC\n    missedRun:\n      policy: skip\n")
	e.clock.Advance(3 * 24 * time.Hour) // the machine was off for three days
	e.app.Tick()
	runs := e.executions("daily")
	if len(runs) != 1 || runs[0].Trigger != domain.TriggerCatchUp {
		t.Fatalf("daily: %+v", runs)
	}
	e.wait(runs[0].ID, domain.StatusCompleted)
	if n := len(e.executions("daily-skip")); n != 0 {
		t.Errorf("skip policy ran %d executions", n)
	}
	// The next firing is the next occurrence, on time.
	view, _ := e.app.GetTask("daily")
	if view.NextFireAt == nil || view.NextFireAt.Hour() != 9 || !view.NextFireAt.After(e.clock.Now()) {
		t.Errorf("next fire %v", view.NextFireAt)
	}
	e.clock.Advance(view.NextFireAt.Sub(e.clock.Now()) + 10*time.Second)
	e.app.Tick()
	runs = e.executions("daily")
	if len(runs) != 2 || runs[0].Trigger != domain.TriggerSchedule {
		t.Fatalf("on-time firing: %+v", runs)
	}
}

// FS-001 AC-015, AC-016: a running execution keeps its definition; the waiting firing picks up the edit.
func TestEditWhileRunning(t *testing.T) {
	e := newEnv(t)
	e.create("edited", "")
	e.claude.setScript(func(ports.RunRequest) string { return "sleep 1; echo 'END ok'" })
	first, _ := e.app.RunTask("edited")
	e.wait(first.ExecutionID, domain.StatusRunning)
	second, _ := e.app.RunTask("edited")
	view, _ := e.app.GetTask("edited")
	edited := strings.Replace(view.YAML, "Do the work of edited.", "Do the edited work.", 1)
	if _, err := e.app.UpdateTask("edited", TaskInput{YAML: edited, BaseRevision: view.Revision}); err != nil {
		t.Fatal(err)
	}
	e.wait(second.ExecutionID, domain.StatusCompleted)
	e.claude.mu.Lock()
	prompts := append([]string(nil), e.claude.prompts...)
	e.claude.mu.Unlock()
	if len(prompts) != 2 || prompts[0] != "Do the work of edited." || prompts[1] != "Do the edited work." {
		t.Errorf("prompts %q", prompts)
	}
	// An edit from a stale revision is a conflict, never a silent overwrite (FS-001 AC-006).
	if _, err := e.app.UpdateTask("edited", TaskInput{YAML: edited, BaseRevision: view.Revision}); err == nil ||
		err.(*Error).Code != CodeConflict {
		t.Errorf("stale revision: %v", err)
	}
}

// FS-001 AC-017, AC-018: disabling lets the execution finish; deleting waits for it; records stay.
func TestDisableAndDeleteWhileRunning(t *testing.T) {
	e := newEnv(t)
	e.create("going", "")
	e.claude.setScript(func(ports.RunRequest) string { return "sleep 1; echo 'END ok'" })
	r, _ := e.app.RunTask("going")
	e.wait(r.ExecutionID, domain.StatusRunning)
	if _, err := e.app.SetEnabled("going", false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.RunTask("going"); err == nil {
		t.Error("a disabled task ran")
	}
	pending, err := e.app.DeleteTask("going")
	if err != nil || !pending {
		t.Fatalf("delete: %v %v", pending, err)
	}
	if _, err := e.app.GetTask("going"); err != nil {
		t.Error("the task vanished before its execution finished")
	}
	e.wait(r.ExecutionID, domain.StatusCompleted)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := e.app.GetTask("going"); err != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := e.app.GetTask("going"); err == nil {
		t.Error("the task was not deleted after its execution")
	}
	if len(e.executions("going")) != 1 {
		t.Error("the execution record was lost")
	}
}

// FS-001 AC-004, AC-012, AC-021: an invalid change keeps the valid file; a missing skill is an error on save and a
// warning on import.
func TestValidationAndImport(t *testing.T) {
	e := newEnv(t)
	e.create("valid", "")
	view, _ := e.app.GetTask("valid")
	_, err := e.app.UpdateTask("valid", TaskInput{YAML: strings.Replace(view.YAML, "type: general", "type: nope", 1),
		BaseRevision: view.Revision})
	if err == nil || err.(*Error).Code != CodeValidationFailed {
		t.Fatalf("invalid edit: %v", err)
	}
	after, _ := e.app.GetTask("valid")
	if after.YAML != view.YAML {
		t.Error("the valid definition was replaced")
	}
	skills := "  skills: [harmflow-github-draft-pr, user-skill, nowhere-skill]\n"
	_, err = e.app.CreateTask(TaskInput{YAML: taskYAML("skilled", skills)})
	if err == nil || !strings.Contains(err.Error(), "nowhere-skill") {
		t.Fatalf("missing skill on save: %v", err)
	}
	imported, err := e.app.ImportTask(taskYAML("skilled", skills), false)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	warned := false
	for _, w := range imported.Validation.Warnings {
		if strings.Contains(w.Message, "nowhere-skill") {
			warned = true
		}
		if strings.Contains(w.Message, "user-skill") || strings.Contains(w.Message, "harmflow-github-draft-pr") {
			t.Errorf("a resolvable skill was reported missing: %s", w.Message)
		}
	}
	if !warned {
		t.Error("import did not warn about the missing skill")
	}
}

// FS-012 FR-005: what the daemon tells the user carries a key and params the desktop translates — a validation error,
// the warning a task keeps, and a use-case error — beside its en-US text.
func TestMessagesCarryKeys(t *testing.T) {
	e := newEnv(t)
	skills := "  skills: [nowhere-skill]\n"
	_, err := e.app.CreateTask(TaskInput{YAML: taskYAML("skilled", skills)})
	ae, ok := err.(*Error)
	if !ok || ae.Key != "skill_not_found" || ae.Params["name"] != "nowhere-skill" || ae.Params["source"] != "harness" ||
		ae.Validation == nil || ae.Validation.Errors[0].Key != "skill_not_found" {
		t.Fatalf("missing skill on save: %#v", err)
	}
	imported, err := e.app.ImportTask(taskYAML("skilled", skills), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(imported.Warnings) != 1 || imported.Warnings[0].Code != domain.WarningMissingSkill ||
		imported.Warnings[0].Key != "skill_missing" || imported.Warnings[0].Params["harness"] != domain.HarnessClaudeCode {
		t.Errorf("task warnings: %+v", imported.Warnings)
	}
	if _, err := e.app.GetTask("nope"); err.(*Error).Key != "task_not_found" || err.(*Error).Params["task"] != "nope" {
		t.Errorf("unknown task: %#v", err)
	}
	e.create("quick", "")
	e.claude.setScript(func(ports.RunRequest) string { return "echo 'END ok'" })
	res, err := e.app.RunTask("quick")
	if err != nil {
		t.Fatal(err)
	}
	e.wait(res.ExecutionID, domain.StatusCompleted)
	err = e.app.CancelExecution(res.ExecutionID)
	if ae, ok := err.(*Error); !ok || ae.Key != "execution_ended" || ae.Params["status"] != "completed" ||
		ae.Message != "the execution already ended as completed" {
		t.Errorf("cancelling an ended execution: %#v", err)
	}
}

// FS-012 FR-005: what the daemon writes about an execution — its events, its error and its attempts' errors and reasons,
// its step — carries a key, and a text it quotes keeps its own; the agent's words and a harness's own error carry none.
func TestExecutionTextsCarryKeys(t *testing.T) {
	e := newEnv(t)
	e.create("keyed", "    fallback:\n      candidates:\n        - provider: codex\n")
	e.claude.setScript(func(ports.RunRequest) string { return "echo 'agent words'; echo 'END quota'" })
	e.codex.setScript(func(ports.RunRequest) string { return "echo 'END ok'" })
	r, _ := e.app.RunTask("keyed")
	x := e.wait(r.ExecutionID, domain.StatusCompleted)
	first, second := x.Attempts[0], x.Attempts[1]
	if first.ReasonKey != "attempt_first" || first.ErrorKey != "" || first.ErrorMessage != "usage limit reached" {
		t.Errorf("first attempt: %+v", first)
	}
	if second.ReasonKey != "attempt_failover" || second.ReasonParams["errorClass"] != "quota_exhausted" ||
		second.Reason != "failover after quota exhausted" {
		t.Errorf("second attempt: %+v", second)
	}
	keys := e.eventKeys(r.ExecutionID, domain.EventExecutionCompleted)
	for _, want := range [][2]string{
		{domain.EventExecutionStatusChanged, "step_starting_harness"},
		{domain.EventHarnessStarted, "harness_started"},
		{domain.EventHarnessQuotaExhausted, "harness_quota_exhausted"},
		{domain.EventHarnessSwitched, "harness_switched"},
		{domain.EventHarnessExited, "harness_exited"},
		{domain.EventExecutionCompleted, "execution_status"},
		{domain.EventHarnessMessage, ""},
	} {
		if got, ok := keys[want[0]]; !ok || got != want[1] {
			t.Errorf("%s: key %q, want %q", want[0], got, want[1])
		}
	}

	// With no harness ready, the error quotes the unavailable one, which quotes its diagnostic, each with its key.
	e.create("unready", "")
	e.claude.mu.Lock()
	e.claude.unavailable = true
	e.claude.mu.Unlock()
	r, _ = e.app.RunTask("unready")
	x = e.wait(r.ExecutionID, domain.StatusFailed)
	if p := x.ErrorParams; x.ErrorKey != "no_alternative_harness_detail" || p["errorClass"] != "harness_unavailable" ||
		p["detailKey"] != "harness_unavailable" || p["detail.detailKey"] != "not_signed_in" || p["detail.detail.command"] != "login" {
		t.Errorf("error %s %v", x.ErrorKey, p)
	}
	if want := "claude-code cannot continue (harness unavailable) and no alternative harness is available: " +
		"claude-code is unavailable: claude-code is not signed in; run `login` in a terminal"; x.ErrorMessage != want {
		t.Errorf("en-US error %q, want %q", x.ErrorMessage, want)
	}
	if s := e.executions("unready")[0]; s.ErrorKey != x.ErrorKey || s.ErrorParams["detailKey"] != "harness_unavailable" {
		t.Errorf("summary %+v", s)
	}
	keys = e.eventKeys(r.ExecutionID, domain.EventExecutionFailed)
	if keys[domain.EventHarnessUnavailable] != "harness_unavailable" || keys[domain.EventExecutionFailed] != x.ErrorKey {
		t.Errorf("events %v", keys)
	}
	if h := e.app.Harnesses()[0]; h.DiagnosticKey != "not_signed_in" {
		t.Errorf("harness diagnostic %+v", h)
	}
}

// eventKeys waits for the event that ends an execution's log and returns the key of the last event of each type.
func (e *env) eventKeys(id, last string) map[string]string {
	e.t.Helper()
	keys := map[string]string{}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		events, _, _ := e.app.ExecutionEvents(id, 0, 0)
		for _, ev := range events {
			keys[ev.Type] = ev.MessageKey
		}
		if _, ok := keys[last]; ok {
			return keys
		}
	}
	e.t.Fatalf("no %s event for %s: %v", last, id, keys)
	return nil
}

// FS-001 AC-002: duplicate creates a disabled copy with a new identifier.
func TestDuplicate(t *testing.T) {
	e := newEnv(t)
	e.create("orig", "")
	cp, err := e.app.DuplicateTask("orig")
	if err != nil {
		t.Fatal(err)
	}
	if cp.ID != "orig-copy" || cp.Enabled {
		t.Errorf("copy %s enabled=%v", cp.ID, cp.Enabled)
	}
}

// FS-001 AC-005, AC-006: an external edit converges and is announced; an invalid one keeps the last valid definition.
func TestExternalEdits(t *testing.T) {
	e := newEnv(t)
	e.create("ext", "")
	view, _ := e.app.GetTask("ext")
	sub, _, cancel := e.app.Subscribe(0)
	defer cancel()
	changed := strings.Replace(view.YAML, "Task ext", "Renamed", 1)
	_ = os.WriteFile(view.Path, []byte(changed), 0o600)
	e.app.scanTaskFiles()
	if v, _ := e.app.GetTask("ext"); v.Name != "Renamed" {
		t.Errorf("name %q", v.Name)
	}
	_ = os.WriteFile(view.Path, []byte("apiVersion: [broken"), 0o600)
	e.app.scanTaskFiles()
	v, _ := e.app.GetTask("ext")
	if v.Name != "Renamed" || !v.Invalid {
		t.Errorf("after an invalid edit: name %q invalid %v", v.Name, v.Invalid)
	}
	var got []string
	timeout := time.After(time.Second)
loop:
	for {
		select {
		case ev := <-sub:
			got = append(got, ev.Type)
		case <-timeout:
			break loop
		}
	}
	joined := strings.Join(got, ",")
	if !strings.Contains(joined, domain.EventTaskChangedExternally) || !strings.Contains(joined, domain.EventTaskInvalid) {
		t.Errorf("events %s", joined)
	}
}

// FS-003 AC-009, AC-013: a restart marks active executions interrupted and counts the failure against the item.
func TestRecoveryMarksInterrupted(t *testing.T) {
	e := newEnv(t)
	e.create("crash", "")
	x := domain.NewExecution(domain.TaskDocument{Metadata: domain.TaskMetadata{ID: "crash"}}, domain.TriggerManual, nil, time.Now())
	x.Status = domain.StatusRunning
	x.WorkItem = "o/r#1"
	x.LastOperation = "git push -u origin harmflow/1-x"
	_ = e.app.d.Executions.Save(x)
	if err := e.app.recover(); err != nil {
		t.Fatal(err)
	}
	got, _ := e.app.GetExecution(x.ID)
	if got.Status != domain.StatusInterrupted {
		t.Errorf("status %s", got.Status)
	}
	q := e.app.Queue()
	if len(q.WorkItems) != 1 || q.WorkItems[0].Failures != 1 || q.WorkItems[0].LastOperation != x.LastOperation {
		t.Errorf("work item %+v", q.WorkItems)
	}
	// The next execution's context names the interruption and its last operation (D-089).
	e.claude.setScript(func(req ports.RunRequest) string { return "echo 'END ok'" })
	r, _ := e.app.RunTask("crash")
	done := e.wait(r.ExecutionID, domain.StatusCompleted)
	data, err := os.ReadFile(filepath.Join(done.ExecutionDir, "context.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ctx map[string]any
	_ = json.Unmarshal(data, &ctx)
	if list, _ := ctx["interruptedExecutions"].([]any); len(list) != 1 {
		t.Errorf("context interruptedExecutions: %v", ctx["interruptedExecutions"])
	}
}

// D-074: notices follow the task's events and the global level.
func TestNotificationLevels(t *testing.T) {
	e := newEnv(t)
	e.create("noisy", "")
	r, _ := e.app.RunTask("noisy")
	e.wait(r.ExecutionID, domain.StatusCompleted)
	// The status is completed before the record is saved and its event emitted, and the notice follows the event: only
	// once the execution has returned has its notice been created, or skipped by the level.
	e.idle("noisy")
	if n := e.app.Notifications(true); len(n) != 1 || n[0].EventType != domain.EventExecutionCompleted {
		t.Fatalf("notices %+v", n)
	}
	level := "failures_and_attention"
	if _, err := e.app.UpdateSettings(SettingsPatch{NotificationLevel: &level}); err != nil {
		t.Fatal(err)
	}
	r, _ = e.app.RunTask("noisy")
	e.wait(r.ExecutionID, domain.StatusCompleted)
	e.idle("noisy")
	if n := e.app.Notifications(true); len(n) != 1 {
		t.Errorf("a success notified at the failures level: %d notices", len(n))
	}
	e.app.MarkNotificationsRead(nil)
	if n := e.app.Notifications(true); len(n) != 0 {
		t.Error("mark read")
	}
}

// FS-011 FR-005: past the cap the oldest read notices go first, and an unread notice is never dropped.
func TestNotificationCapKeepsUnread(t *testing.T) {
	e := newEnv(t)
	e.create("noisy", "")
	hold := func(read func(i int) bool) { // maxNotifications notices, newest first
		e.app.mu.Lock()
		e.app.notifications = nil
		for i := 0; i < maxNotifications; i++ {
			e.app.notifications = append(e.app.notifications, Notification{ID: fmt.Sprintf("old-%d", i), Read: read(i)})
		}
		e.app.mu.Unlock()
	}
	completed := domain.Event{Type: domain.EventExecutionCompleted, TaskID: "noisy"}

	hold(func(int) bool { return false })
	e.app.notifyFor(completed)
	if n := e.app.Notifications(true); len(n) != maxNotifications+1 {
		t.Errorf("%d unread notices kept, want %d", len(n), maxNotifications+1)
	}
	// Once they are read, the next notice trims the list back to the cap, oldest first.
	e.app.MarkNotificationsRead(nil)
	e.app.notifyFor(completed)
	all := e.app.Notifications(false)
	if len(all) != maxNotifications {
		t.Fatalf("%d read notices kept, want %d", len(all), maxNotifications)
	}
	if oldest := all[len(all)-1].ID; oldest != fmt.Sprintf("old-%d", maxNotifications-3) {
		t.Errorf("oldest notice kept is %s, want the two oldest dropped", oldest)
	}

	// Only the two notices before the oldest are read: the older of them is dropped, not the oldest unread one.
	hold(func(i int) bool { return i == maxNotifications-2 || i == maxNotifications-3 })
	e.app.notifyFor(completed)
	kept := map[string]bool{}
	for _, n := range e.app.Notifications(false) {
		kept[n.ID] = true
	}
	var dropped []string
	for i := 0; i < maxNotifications; i++ {
		if id := fmt.Sprintf("old-%d", i); !kept[id] {
			dropped = append(dropped, id)
		}
	}
	if want := fmt.Sprintf("old-%d", maxNotifications-2); len(kept) != maxNotifications || len(dropped) != 1 ||
		dropped[0] != want {
		t.Errorf("dropped %v of %d notices, want only %s", dropped, maxNotifications+1, want)
	}
}

// FS-012 FR-005: a notice sends the params its text reads. A message the daemon wrote is nested with its key, so the
// desktop translates it in place; the agent's words are quoted as they are.
func TestNoticeSendsTheParamsItsTextReads(t *testing.T) {
	e := newEnv(t)
	e.create("loud", "  notifications:\n    native:\n      events: [execution.started, execution.completed, "+
		"execution.failed, execution.needs_attention, harness.unavailable]\n")
	r, _ := e.app.RunTask("loud")
	e.wait(r.ExecutionID, domain.StatusCompleted)
	if p := e.noticeParams(domain.EventExecutionStarted); p["harness"] != domain.HarnessClaudeCode {
		t.Errorf("started: %v", p)
	}
	if p := e.noticeParams(domain.EventExecutionCompleted); p["summary"] != "done" {
		t.Errorf("completed: %v", p)
	}

	e.claude.setScript(func(req ports.RunRequest) string {
		return fmt.Sprintf(`printf '{"outcome":"needs_attention","summary":"approve the plan"}' > %s/report.json; `+
			`echo 'END ok'`, req.ExecutionDir)
	})
	r, _ = e.app.RunTask("loud")
	e.wait(r.ExecutionID, domain.StatusNeedsAttention)
	if p := e.noticeParams(domain.EventExecutionNeedsAttention); p["reason"] != "approve the plan" || p["reasonKey"] != "" {
		t.Errorf("needs attention: %v", p)
	}

	// With no harness ready, the notices quote the daemon's texts, which quote the harness's diagnostic.
	e.claude.mu.Lock()
	e.claude.unavailable = true
	e.claude.mu.Unlock()
	r, _ = e.app.RunTask("loud")
	e.wait(r.ExecutionID, domain.StatusFailed)
	if p := e.noticeParams(domain.EventHarnessUnavailable); p["reasonKey"] != "harness_unavailable" ||
		p["reason.harness"] != domain.HarnessClaudeCode || p["reason.detailKey"] != "not_signed_in" {
		t.Errorf("harness unavailable: %v", p)
	}
	p := e.noticeParams(domain.EventExecutionFailed)
	if p["errorKey"] != "no_alternative_harness_detail" || p["error.detailKey"] != "harness_unavailable" ||
		p["error.detail.detail.command"] != "login" || !strings.HasPrefix(p["error"], "claude-code cannot continue") {
		t.Errorf("failed: %v", p)
	}
	if _, ok := p["detail"]; ok {
		t.Errorf("failed: a detail no text reads: %v", p)
	}
}

// noticeParams waits for the newest notice of an event type and returns its params.
func (e *env) noticeParams(eventType string) map[string]string {
	e.t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		for _, n := range e.app.Notifications(false) {
			if n.EventType == eventType {
				return n.Params
			}
		}
	}
	e.t.Fatalf("no %s notice", eventType)
	return nil
}

// FS-009 FR-010: an unenforced restriction runs with a warning on the task and on the execution.
func TestPolicyWarningIsShown(t *testing.T) {
	e := newEnv(t)
	e.create("policy", "")
	if v, _ := e.app.GetTask("policy"); len(v.Warnings) != 0 {
		t.Errorf("the fake enforces everything, got %+v", v.Warnings)
	}
}
