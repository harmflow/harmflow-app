package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/eventlog"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/fsstore"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/codex"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/schema"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/workspace"
	"github.com/harmflow/harmflow-app/daemon/internal/contracts"
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

// fakeWake is the machine's sleep as the test makes it: the test says when the machine woke.
type fakeWake struct {
	mu sync.Mutex
	at time.Time
}

func (w *fakeWake) LastWake() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.at
}

func (w *fakeWake) woke(at time.Time) {
	w.mu.Lock()
	w.at = at
	w.mu.Unlock()
}

// fakeHarness runs /bin/sh with a script the test chooses. The script prints the outcome on its last line:
// "END ok", "END quota", "END rate", "END network", "END unavailable" (signed out), or "END error"; "STARTED <model>
// <effort>" reports what the session runs with. Its user configuration holds the skill user-skill and the agent
// user-agent, each at a path of its own harness, and the skill review-notes for Claude Code only.
type fakeHarness struct {
	id          string
	mu          sync.Mutex
	unavailable bool
	script      func(req ports.RunRequest) string
	prompts     []string
	live        int           // attempts prepared to run whose parser has not finished
	gate        chan struct{} // Detect waits for it to close, when set
	held        int           // Detect calls waiting for the gate
	// cutLag is how long a held Detect takes to return once its context ends, as a CLI takes time to exit.
	cutLag time.Duration
	// quota answers ReadQuota when set; otherwise every read finds a quota with nothing used. quotaReads counts them.
	quota      func() (domain.Quota, error)
	quotaReads int
}

func (f *fakeHarness) ID() string   { return f.id }
func (f *fakeHarness) Name() string { return f.id }
func (f *fakeHarness) Detect(ctx context.Context, _ string) ports.HarnessInfo {
	f.mu.Lock()
	if gate := f.gate; gate != nil {
		f.held++
		f.mu.Unlock()
		select {
		case <-gate:
		case <-ctx.Done():
			f.mu.Lock()
			lag := f.cutLag
			f.mu.Unlock()
			time.Sleep(lag)
		}
		f.mu.Lock()
		f.held--
	}
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

// ReportsExtraUsage says what the real adapter of the same harness says: only Claude Code reports extra usage as
// it runs.
func (f *fakeHarness) ReportsExtraUsage() bool { return f.id == domain.HarnessClaudeCode }

func (f *fakeHarness) ReadQuota(context.Context, ports.HarnessInfo) (domain.Quota, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.quotaReads++
	if f.quota == nil {
		return domain.Quota{}, nil
	}
	return f.quota()
}

func (f *fakeHarness) setQuota(q func() (domain.Quota, error)) {
	f.mu.Lock()
	f.quota = q
	f.mu.Unlock()
}

func (f *fakeHarness) reads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.quotaReads
}
func (f *fakeHarness) UserItem(_ context.Context, _ ports.HarnessInfo, kind, name, _ string) (ports.ResolvedItem, bool, []ports.DiscoveryError, error) {
	found := kind == "skill" && name == "user-skill" || kind == "agent" && name == "user-agent" ||
		kind == "skill" && name == "review-notes" && f.id == domain.HarnessClaudeCode
	if !found {
		return ports.ResolvedItem{}, false, nil, nil
	}
	return ports.ResolvedItem{Name: name, Kind: kind, Path: filepath.Join("/", f.id, kind+"s", name)}, true, nil, nil
}
func (f *fakeHarness) UserItems(context.Context, ports.HarnessInfo, string, string) ([]ports.ResolvedItem, []ports.DiscoveryError, error) {
	return nil, nil, nil
}
func (f *fakeHarness) Prepare(_ ports.HarnessInfo, req ports.RunRequest) (ports.Invocation, ports.OutputParser, error) {
	f.mu.Lock()
	parser := &fakeParser{}
	if req.Prompt != "" { // validation's policy check prepares with no prompt
		f.prompts = append(f.prompts, req.Prompt)
		f.live++
		parser.finished = func() {
			f.mu.Lock()
			f.live--
			f.mu.Unlock()
		}
	}
	script := "echo 'END ok'"
	if f.script != nil {
		script = f.script(req)
	}
	f.mu.Unlock()
	return ports.Invocation{Path: "/bin/sh", Args: []string{"-c", script}, Env: req.Env, Dir: req.WorkDir}, parser, nil
}

func (f *fakeHarness) setScript(s func(ports.RunRequest) string) {
	f.mu.Lock()
	f.script = s
	f.mu.Unlock()
}

func (f *fakeHarness) running() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.live > 0
}

// hold makes Detect wait until the returned release is called, or its context ends; the test's end releases it too.
func (f *fakeHarness) hold(t *testing.T) (release func()) {
	gate := make(chan struct{})
	f.mu.Lock()
	f.gate = gate
	f.mu.Unlock()
	release = sync.OnceFunc(func() { close(gate) })
	t.Cleanup(release)
	return release
}

// holding reports whether a Detect call is waiting for the gate.
func (f *fakeHarness) holding() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.held > 0
}

// fakeParser also reads "USAGE <input tokens>", the usage so far, which is final once the attempt ends well, and
// "CMD <command>", a shell command the session ran to success. "WAIT network" is a wait for the network, as Codex
// prints one again and again without exiting: the first of a stretch is an event, and a timeout that ends a wait is a
// network failure. Any other line is the agent's message, and the last one is what a quota end reports as its final
// text, as the real adapters do.
type fakeParser struct {
	last     string
	text     string
	input    int64
	tools    int
	waiting  bool   // the last line was a wait for the network
	finished func() // the executor calls Finish once the process has exited and its output is drained
}

func (p *fakeParser) Line(b []byte) []ports.HarnessEvent {
	s := string(b)
	wasWaiting := p.waiting
	if p.waiting = s == "WAIT network"; p.waiting {
		if wasWaiting {
			return nil
		}
		return []ports.HarnessEvent{{Kind: ports.HarnessNetworkWait, Message: "Reconnecting... waiting for network"}}
	}
	if strings.HasPrefix(s, "END ") {
		p.last = strings.TrimPrefix(s, "END ")
		return nil
	}
	if rest, ok := strings.CutPrefix(s, "STARTED "); ok {
		model, effort, _ := strings.Cut(rest, " ")
		return []ports.HarnessEvent{{Kind: ports.HarnessStarted, Model: model, Effort: effort}}
	}
	if s == "EXTRA usage" {
		// A plan window used up and the session going on with extra usage, as Claude Code reports it.
		return []ports.HarnessEvent{{Kind: ports.HarnessQuota, Quota: &domain.Quota{UsingExtraUsage: true,
			Windows: []domain.QuotaWindow{{Name: "five_hour", UsedPercent: 100}}}}}
	}
	if n, ok := strings.CutPrefix(s, "USAGE "); ok {
		p.input, _ = strconv.ParseInt(n, 10, 64)
		return []ports.HarnessEvent{{Kind: ports.HarnessUsage, Usage: p.usage(domain.UsagePartial)}}
	}
	if cmd, ok := strings.CutPrefix(s, "CMD "); ok {
		p.tools++
		id := "tool-" + strconv.Itoa(p.tools)
		return []ports.HarnessEvent{{Kind: ports.HarnessToolStarted, ToolID: id, Tool: "Bash", Command: cmd},
			{Kind: ports.HarnessToolDone, ToolID: id, Tool: "Bash"}}
	}
	p.text = s
	return []ports.HarnessEvent{{Kind: ports.HarnessMessage, Message: s}}
}

func (p *fakeParser) usage(s domain.UsageStatus) *domain.Usage {
	if p.input == 0 {
		return domain.NotReported()
	}
	return &domain.Usage{Status: s, Models: []domain.ModelUsage{{Model: "fake", Tokens: domain.Tokens{Input: p.input}}}}
}

func (p *fakeParser) Finish(exit int, _ string, timedOut, cancelled bool) domain.AttemptEnd {
	if p.finished != nil {
		p.finished()
		p.finished = nil
	}
	switch {
	case cancelled:
		return domain.AttemptEnd{Class: domain.ClassCancelled, Usage: p.usage(domain.UsagePartial)}
	case timedOut && p.waiting:
		return domain.AttemptEnd{Class: domain.ClassNetwork, Error: domain.Text{Message: "Reconnecting... waiting for network"}}
	case timedOut:
		return domain.AttemptEnd{Class: domain.ClassTimeout}
	}
	switch p.last {
	case "ok":
		return domain.AttemptEnd{FinalText: "done", Usage: p.usage(domain.UsageReported)}
	case "quota":
		return domain.AttemptEnd{Class: domain.ClassQuotaExhausted, Error: domain.Text{Message: "usage limit reached"},
			FinalText: p.text, Usage: p.usage(domain.UsagePartial)}
	case "rate":
		return domain.AttemptEnd{Class: domain.ClassRateLimited, Error: domain.Text{Message: "429"}, RetryAfter: 10 * time.Millisecond}
	case "network":
		return domain.AttemptEnd{Class: domain.ClassNetwork, Error: domain.Text{Message: "connect ECONNREFUSED 127.0.0.1:443"}}
	case "unavailable":
		return domain.AttemptEnd{Class: domain.ClassHarnessUnavailable, Error: domain.Text{Message: "Not logged in"}}
	}
	return domain.AttemptEnd{Class: domain.ClassTaskError, Error: domain.Text{Message: fmt.Sprintf("exit %d", exit)}}
}

// hookedEvents runs the test's hook on each event before it is appended, on the goroutine that emits it. It then checks
// the event as the log wrote it against event.schema.json, so every test's events follow the event model; the problems
// are reported when the test ends, since an event may come from another goroutine.
type hookedEvents struct {
	*eventlog.Log
	schema  *schema.Validator
	mu      sync.Mutex
	hook    func(domain.Event)
	deleted func(executionID string)
	checked map[string]int // events checked, by type
	invalid []string
}

func newHookedEvents(t *testing.T, log *eventlog.Log, v *schema.Validator) *hookedEvents {
	h := &hookedEvents{Log: log, schema: v, checked: map[string]int{}}
	// Registered before the App's own cleanup, this runs after it, and so sees daemon.stopping too.
	t.Cleanup(func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, problem := range h.invalid {
			t.Errorf("an event does not fit event.schema.json: %s", problem)
		}
	})
	return h
}

func (h *hookedEvents) Append(ev domain.Event) domain.Event {
	h.mu.Lock()
	hook := h.hook
	h.mu.Unlock()
	if hook != nil {
		hook(ev)
	}
	stored := h.Log.Append(ev)
	h.check(stored)
	return stored
}

// check validates the JSON the log writes for the event, as a line of its execution's JSONL and as its SSE data.
func (h *hookedEvents) check(ev domain.Event) {
	data, err := json.Marshal(ev)
	var issues []schema.Issue
	if err == nil {
		var doc any
		if doc, err = schema.FromJSON(data); err == nil {
			issues = h.schema.ValidateEvent(doc)
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checked[ev.Type]++
	switch {
	case err != nil:
		h.invalid = append(h.invalid, fmt.Sprintf("%s: %v", ev.Type, err))
	case len(issues) > 0:
		problems := make([]string, len(issues))
		for i, is := range issues {
			problems[i] = is.Path + " " + trimTo(is.Message, 120)
		}
		h.invalid = append(h.invalid, fmt.Sprintf("%s in %s", strings.Join(problems, "; "), data))
	}
}

// checkedTypes is how many events of each type were checked so far.
func (h *hookedEvents) checkedTypes() map[string]int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return maps.Clone(h.checked)
}

func (h *hookedEvents) setHook(hook func(domain.Event)) {
	h.mu.Lock()
	h.hook = hook
	h.mu.Unlock()
}

// DeleteLog runs the test's delete hook, if any, once the log is deleted and before its caller goes on.
func (h *hookedEvents) DeleteLog(executionID string) error {
	err := h.Log.DeleteLog(executionID)
	h.mu.Lock()
	hook := h.deleted
	h.mu.Unlock()
	if hook != nil {
		hook(executionID)
	}
	return err
}

func (h *hookedEvents) setDeleteHook(hook func(executionID string)) {
	h.mu.Lock()
	h.deleted = hook
	h.mu.Unlock()
}

type env struct {
	t      *testing.T
	app    *App
	clock  *fakeClock
	claude *fakeHarness
	codex  *fakeHarness
	events *hookedEvents
	wake   *fakeWake
	home   string
	paths  Paths
	// service stands in for the launchd agent, when a test sets one; the App has none otherwise.
	service ports.HostServiceManager
	// startCtx is the context the App starts under, when a test sets one; the test's own otherwise.
	startCtx context.Context
	// timeoutUnit is what a task's timeoutMinutes counts, when a test sets it; a minute otherwise.
	timeoutUnit time.Duration
}

// newEnv starts an App on fake harnesses; configure sets up the env, its fakes included, before the App starts.
func newEnv(t *testing.T, configure ...func(*env)) *env {
	t.Helper()
	return openEnv(t, t.TempDir(), configure...)
}

// openEnv starts a daemon over home, which recovers what it finds there; configure is as for newEnv.
func openEnv(t *testing.T, home string, configure ...func(*env)) *env {
	t.Helper()
	builtins := t.TempDir()
	_ = os.MkdirAll(filepath.Join(builtins, "skills", "hf-github-draft-pr"), 0o700)
	_ = os.WriteFile(filepath.Join(builtins, "skills", "hf-github-draft-pr", "SKILL.md"), []byte("---\nname: hf-github-draft-pr\ndescription: d\n---\n"), 0o600)
	_ = os.MkdirAll(filepath.Join(builtins, "agents"), 0o700)
	e := &env{t: t, clock: &fakeClock{now: time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)}, wake: &fakeWake{},
		home: home, paths: NewPaths(home, t.TempDir(), builtins), claude: &fakeHarness{id: domain.HarnessClaudeCode},
		codex: &fakeHarness{id: domain.HarnessCodex}}
	e.start(t, configure...)
	return e
}

// start runs a new App on the env's data directory, with adapters of its own as a new daemon process has; configure
// runs once they exist, before the App starts.
func (e *env) start(t *testing.T, configure ...func(*env)) {
	t.Helper()
	tasks, _ := fsstore.NewTasks(e.paths.Tasks)
	state, _ := fsstore.NewState(e.paths.State)
	execs, _ := fsstore.NewExecutions(e.paths.Executions)
	events, _ := eventlog.New(e.paths.Logs, time.Now)
	v, err := schema.New()
	if err != nil {
		t.Fatal(err)
	}
	e.events = newHookedEvents(t, events, v)
	for _, c := range configure {
		c(e)
	}
	a, err := New(Deps{Paths: e.paths, Version: "test", Clock: e.clock, Wake: e.wake, Location: time.UTC, Tasks: tasks,
		State: state, Executions: execs, Events: e.events, Validator: v, Harnesses: []ports.HarnessAdapter{e.claude, e.codex},
		Service: e.service, Workspace: &workspace.Reader{}, TickInterval: time.Hour, TimeoutUnit: e.timeoutUnit,
		Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	e.app = a
	ctx := e.startCtx
	if ctx == nil {
		ctx = context.Background()
	}
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Stop(domain.Text{Message: "test done"}) })
}

// restart stops the daemon, if it still runs, and starts a new App on the same data directory, fake clock, and
// stand-in harnesses, as a daemon restart does. The scheduler loop of a test ticks hourly, so the clock may move
// before the restart as if the daemon were already down.
func (e *env) restart(t *testing.T) *env {
	t.Helper()
	e.app.Stop(domain.Text{Message: "restart"})
	n := &env{t: t, clock: e.clock, wake: e.wake, claude: e.claude, codex: e.codex, home: e.home, paths: e.paths}
	n.start(t)
	return n
}

// in is the env with its helpers reporting to t, for a subtest: a helper must not fail its parent test.
func (e *env) in(t *testing.T) *env {
	c := *e
	c.t = t
	return &c
}

// published lists the events of a type the daemon published since it started, from its replay buffer.
func (e *env) published(eventType string) []domain.Event {
	ch, _, cancel := e.app.Subscribe(1)
	defer cancel()
	var out []domain.Event
	for {
		select {
		case ev := <-ch:
			if ev.Type == eventType {
				out = append(out, ev)
			}
		default:
			return out
		}
	}
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
	if _, err := e.app.CreateTask(e.t.Context(), TaskInput{YAML: taskYAML(id, extra)}); err != nil {
		e.t.Fatalf("create %s: %v", id, err)
	}
}

// A loaded host can take seconds to run each process a fake harness starts, while the daemon's own work stays fast.
// poll spends its budget only while no fake attempt is running, from Prepare to its parser's Finish, so a daemon that
// hangs still fails within the budget; slow processes stretch it up to processCeiling in all.
const processCeiling = 3 * time.Minute

// poll checks cond every 20 ms until it holds, and reports whether it did.
func (e *env) poll(budget time.Duration, cond func() bool) bool {
	start, last := time.Now(), time.Now()
	for spent := time.Duration(0); ; time.Sleep(20 * time.Millisecond) {
		if cond() {
			return true
		}
		now := time.Now()
		if !e.claude.running() && !e.codex.running() {
			spent += now.Sub(last)
		}
		last = now
		if spent >= budget || now.Sub(start) >= processCeiling {
			return false
		}
	}
}

// wait returns the execution once it has one of the statuses; a final status it does not want fails at once.
func (e *env) wait(id string, want ...domain.ExecutionStatus) ExecutionView {
	e.t.Helper()
	return e.waitWithin(10*time.Second, id, want...)
}

// waitWithin is wait with a budget of its own, for an execution whose end comes later by design, such as after a grace
// period.
func (e *env) waitWithin(budget time.Duration, id string, want ...domain.ExecutionStatus) ExecutionView {
	e.t.Helper()
	var x ExecutionView
	e.poll(budget, func() bool {
		x, _ = e.app.GetExecution(id)
		return slices.Contains(want, x.Status) || x.Status.IsTerminal()
	})
	if !slices.Contains(want, x.Status) {
		e.t.Fatalf("execution %s is %s, want %v", id, x.Status, want)
	}
	return x
}

// prepared waits until the harness has prepared a run. An execution is running before its harness prepares the script,
// so a test that changes the script while a run goes on waits for this first, or the run may take the new script.
func (e *env) prepared(h *fakeHarness) {
	e.t.Helper()
	if !e.poll(10*time.Second, h.running) {
		e.t.Fatalf("%s never prepared a run", h.id)
	}
}

func (e *env) executions(task string) []ExecutionSummary {
	return e.app.ListExecutions(ExecutionFilter{TaskID: task})
}

// idle waits until the task's execution has returned and freed the task.
func (e *env) idle(task string) {
	e.t.Helper()
	if !e.poll(10*time.Second, func() bool {
		e.app.mu.Lock()
		defer e.app.mu.Unlock()
		_, busy := e.app.running[task]
		return !busy
	}) {
		e.t.Fatalf("task %s is still running", task)
	}
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
	// A draft PR with failing tests is completed, and its item stays resumable.
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

// A conflict the agent met keeps its code in the record, and its log holds it as a warning that a replay returns; the
// execution continues.
func TestConflictingActivityIsLogged(t *testing.T) {
	e := newEnv(t)
	e.create("conflict", "")
	const said = "someone pushed to the work branch; rebased and continued"
	e.claude.setScript(func(req ports.RunRequest) string {
		report := `{"outcome":"completed","summary":"opened a draft","workItems":[{"key":"o/r#7","outcome":"completed"}],` +
			`"warnings":[{"code":"conflicting_activity","message":"` + said + `","repository":"o/r"}]}`
		return fmt.Sprintf(`printf '%%s' '%s' > %s/report.json; echo 'END ok'`, report, req.ExecutionDir)
	})
	r, _ := e.app.RunTask("conflict")
	x := e.wait(r.ExecutionID, domain.StatusCompleted)
	if len(x.Warnings) != 1 || x.Warnings[0].Code != domain.WarningConflictingActivity ||
		x.Warnings[0].Subject != "o/r" || x.Warnings[0].Message != said {
		t.Errorf("warnings %+v", x.Warnings)
	}
	e.eventKeys(r.ExecutionID, domain.EventExecutionCompleted)
	conflict := func(ev domain.Event) bool {
		return ev.Type == domain.EventWorkItemConflicting && ev.Severity == domain.SeverityWarning && ev.Message == said &&
			ev.Payload["code"] == domain.WarningConflictingActivity && ev.Payload["subject"] == "o/r"
	}
	data, err := os.ReadFile(filepath.Join(e.home, "logs", r.ExecutionID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var logged []domain.Event
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var ev domain.Event
		if json.Unmarshal([]byte(line), &ev) == nil && conflict(ev) {
			logged = append(logged, ev)
		}
	}
	if len(logged) != 1 {
		t.Fatalf("%d conflict events in the log, want 1:\n%s", len(logged), data)
	}
	// A log opened afresh, as after the daemon restarts, replays it from the file.
	fresh, _ := eventlog.New(filepath.Join(e.home, "logs"), time.Now)
	replayed, _, err := fresh.Replay(r.ExecutionID, 0, 0)
	if err != nil || !slices.ContainsFunc(replayed, func(ev domain.Event) bool { return ev.ID == logged[0].ID && conflict(ev) }) {
		t.Errorf("the replay lacks the conflict (%v): %+v", err, replayed)
	}
	var contract struct {
		Defs struct {
			EventType struct{ Enum []string } `json:"eventType"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(contracts.EventSchema, &contract); err != nil ||
		!slices.Contains(contract.Defs.EventType.Enum, domain.EventWorkItemConflicting) {
		t.Errorf("%s is not an event type of the contract (%v)", domain.EventWorkItemConflicting, err)
	}
}

// Every event the use cases append follows event.schema.json. hookedEvents checks each one in every test; this one
// runs a success, a failover, a cancellation, and a no_work outcome, and makes sure each was checked.
func TestEventsFitTheirSchema(t *testing.T) {
	e := newEnv(t)
	e.create("succeeds", "")
	e.create("fails-over", "    fallback:\n      candidates:\n        - provider: codex\n")
	e.create("cancelled", "")
	e.create("finds-nothing", "")
	e.claude.setScript(func(req ports.RunRequest) string {
		switch {
		case strings.Contains(req.Prompt, "fails-over"):
			return "echo 'END quota'"
		case strings.Contains(req.Prompt, "cancelled"):
			return slow
		case strings.Contains(req.Prompt, "finds-nothing"):
			return fmt.Sprintf(`printf '{"outcome":"no_work"}' > %s/report.json; echo 'END ok'`, req.ExecutionDir)
		}
		return "echo 'Working on it'; echo 'END ok'"
	})
	e.codex.setScript(func(ports.RunRequest) string { return "echo 'END ok'" })
	ids := map[string]string{}
	for _, task := range []string{"succeeds", "fails-over", "cancelled", "finds-nothing"} {
		r, err := e.app.RunTask(task)
		if err != nil || r.Outcome != "started" {
			t.Fatalf("run %s: %v %+v", task, err, r)
		}
		ids[task] = r.ExecutionID
	}
	e.wait(ids["cancelled"], domain.StatusRunning)
	if err := e.app.CancelExecution(ids["cancelled"]); err != nil {
		t.Fatal(err)
	}
	e.wait(ids["succeeds"], domain.StatusCompleted)
	if x := e.wait(ids["fails-over"], domain.StatusCompleted); x.HarnessSwitches != 1 {
		t.Errorf("the failover switched %d times", x.HarnessSwitches)
	}
	e.wait(ids["cancelled"], domain.StatusCancelled)
	e.wait(ids["finds-nothing"], domain.StatusNoWork)
	// A record reads its final status before its terminal event is appended.
	want := map[string]int{domain.EventExecutionCreated: 4, domain.EventHarnessMessage: 1,
		domain.EventHarnessQuotaExhausted: 1, domain.EventHarnessSwitched: 1, domain.EventExecutionCompleted: 2,
		domain.EventExecutionCancelled: 1, domain.EventExecutionNoWork: 1}
	var checked map[string]int
	enough := e.poll(10*time.Second, func() bool {
		checked = e.events.checkedTypes()
		for typ, n := range want {
			if checked[typ] < n {
				return false
			}
		}
		return true
	})
	if !enough {
		t.Errorf("events checked against the schema, by type: %v; want at least %v", checked, want)
	}
}

// A task never runs twice at once; one firing waits and the next coalesce into it.
func TestOverlapQueueCoalesces(t *testing.T) {
	e := newEnv(t)
	e.create("busy", "")
	e.claude.setScript(func(ports.RunRequest) string { return slow })
	first, _ := e.app.RunTask("busy")
	e.wait(first.ExecutionID, domain.StatusRunning)
	e.prepared(e.claude)
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

// With skip, an overlapping firing is dropped and logged.
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

// Different tasks run in parallel on the same harness; cancelling one leaves the others.
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

// Three tasks run at once on the same harness; one fails when its harness exits with an error, and the other two keep
// running, then complete, their records and attempts untouched by the failure.
func TestFailureLeavesTheOthersRunning(t *testing.T) {
	e := newEnv(t)
	tasks := []string{"a", "b", "c"}
	for _, id := range tasks {
		e.create(id, "")
	}
	// Each attempt waits for its gate file, a minute at most; b's harness then exits with an error.
	gates := t.TempDir()
	e.claude.setScript(func(req ports.RunRequest) string {
		gate, end := "done", "echo 'END ok'"
		if req.Prompt == "Do the work of b." {
			gate, end = "fail", "echo 'the harness crashed' >&2; exit 3"
		}
		return fmt.Sprintf("echo working; i=0; while [ ! -e %s ] && [ $i -lt 1200 ]; do sleep 0.05; i=$((i+1)); done; %s",
			filepath.Join(gates, gate), end)
	})
	open := func(gate string) {
		if err := os.WriteFile(filepath.Join(gates, gate), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ids := map[string]string{}
	for _, id := range tasks {
		r, err := e.app.RunTask(id)
		if err != nil || r.Outcome != "started" {
			t.Fatalf("run %s: %v %+v", id, err, r)
		}
		ids[id] = r.ExecutionID
	}
	for _, id := range tasks {
		e.wait(ids[id], domain.StatusRunning)
	}

	open("fail")
	failed := e.wait(ids["b"], domain.StatusFailed)
	if len(failed.Attempts) != 1 || failed.Attempts[0].Outcome != domain.AttemptFailed {
		t.Errorf("the failed execution's attempts: %+v", failed.Attempts)
	}
	for _, id := range []string{"a", "c"} {
		if x, _ := e.app.GetExecution(ids[id]); x.Status != domain.StatusRunning {
			t.Errorf("%s is %s after another task failed", id, x.Status)
		}
	}

	open("done")
	for _, id := range []string{"a", "c"} {
		x := e.wait(ids[id], domain.StatusCompleted)
		if x.ErrorMessage != "" || x.CancelRequestedAt != nil || x.HarnessSwitches != 0 || len(x.Attempts) != 1 ||
			x.Attempts[0].Harness != domain.HarnessClaudeCode || x.Attempts[0].Outcome != domain.AttemptSucceeded ||
			x.Attempts[0].ErrorMessage != "" {
			t.Errorf("%s after another task failed: error %q, attempts %+v", id, x.ErrorMessage, x.Attempts)
		}
		events, _, err := e.app.ExecutionEvents(ids[id], 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range events {
			if ev.Type == domain.EventExecutionFailed || ev.Severity == domain.SeverityError {
				t.Errorf("%s's log holds %s: %s", id, ev.Type, ev.Message)
			}
		}
		if n := len(e.executions(id)); n != 1 {
			t.Errorf("%s has %d executions, want 1", id, n)
		}
	}
}

// A harness that exits on its own keeps the outcome its exit gives, though a cancel comes while the process it left
// running, which ignores SIGTERM, is ended over the grace period; that process is gone once the execution ends.
func TestHarnessExitKeepsItsOutcomeThroughACancel(t *testing.T) {
	e := newEnv(t)
	e.create("late", "  execution:\n    cancelGraceSeconds: 2\n")
	dir := t.TempDir()
	e.claude.setScript(func(ports.RunRequest) string {
		return fmt.Sprintf("(trap '' TERM; exec sleep 600) >/dev/null 2>&1 & echo $! > %[1]s/child; "+
			"echo $$ > %[1]s/harness; echo 'END ok'", dir)
	})
	r, err := e.app.RunTask("late")
	if err != nil || r.Outcome != "started" {
		t.Fatalf("run late: %v %+v", err, r)
	}
	child := waitForPid(t, filepath.Join(dir, "child"))
	waitReaped(t, waitForPid(t, filepath.Join(dir, "harness")))
	if err := e.app.CancelExecution(r.ExecutionID); err != nil {
		t.Fatal(err)
	}
	x := e.wait(r.ExecutionID, domain.StatusCompleted)
	e.idle("late")
	if len(x.Attempts) != 1 || x.Attempts[0].Outcome != domain.AttemptSucceeded {
		t.Errorf("attempts %+v", x.Attempts)
	}
	if alive(child) {
		t.Errorf("the background child %d survived the harness's exit", child)
	}
}

// An execution cancelled or dropped while it is still queued never runs, even when its task was already claimed for
// it: fire and startQueued claim the task before the executor leaves queued.
func TestCancelBeforeItStarts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cancel func(e *env, id string) error
	}{
		{"cancelled", func(e *env, id string) error { return e.app.CancelExecution(id) }},
		{"disabled", func(e *env, _ string) error { _, err := e.app.SetEnabled(t.Context(), "early", false); return err }},
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

// Quota on the preferred harness fails over to the fallback in the same execution.
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
	// The alternative gets the same prompt; the summary of the previous attempt is in its context.
	if got := e.codex.prompts[len(e.codex.prompts)-1]; got != "Do the work of failover." {
		t.Errorf("prompt %q", got)
	}
	// With the switch used up, a second quota has no alternative and the execution fails explicitly.
	e.codex.setScript(func(ports.RunRequest) string { return "echo 'END quota'" })
	r, _ = e.app.RunTask("failover")
	x = e.wait(r.ExecutionID, domain.StatusFailed)
	if !strings.Contains(x.ErrorMessage, "no alternative") {
		t.Errorf("error %q", x.ErrorMessage)
	}
}

// harmflow-app#212: a network failure is recorded as one, in the record on disk too, and does not fail over even with a
// fallback configured, as the specification asks.
func TestNetworkFailureRecordedWithoutFailover(t *testing.T) {
	e := newEnv(t)
	e.create("offline", "    fallback:\n      candidates:\n        - provider: codex\n")
	e.claude.setScript(func(ports.RunRequest) string { return "echo 'END network'" })
	e.codex.setScript(func(ports.RunRequest) string { return "echo 'END ok'" })
	r, _ := e.app.RunTask("offline")
	x := e.wait(r.ExecutionID, domain.StatusFailed)
	if len(x.Attempts) != 1 || x.Attempts[0].Outcome != domain.AttemptNetworkFailed || x.HarnessSwitches != 0 {
		t.Fatalf("attempts %+v, %d switches", x.Attempts, x.HarnessSwitches)
	}
	e.codex.mu.Lock()
	prompts := len(e.codex.prompts)
	e.codex.mu.Unlock()
	if prompts != 0 {
		t.Errorf("Codex got %d prompts", prompts)
	}
	events, _, _ := e.app.ExecutionEvents(r.ExecutionID, 0, 0)
	for _, ev := range events {
		if ev.Type == domain.EventHarnessSwitched {
			t.Errorf("a %s event followed the network failure", ev.Type)
		}
	}
	e.idle("offline")
	data, err := os.ReadFile(filepath.Join(e.home, "state", "executions", r.ExecutionID+".json"))
	var saved domain.Execution
	if err == nil {
		err = json.Unmarshal(data, &saved)
	}
	if err != nil || len(saved.Attempts) != 1 || saved.Attempts[0].Outcome != domain.AttemptNetworkFailed {
		t.Errorf("the record on disk lacks the network outcome (%v):\n%s", err, data)
	}
}

// harmflow-app#250: a Codex that cannot reach its provider waits for the network until the execution's timeout ends
// it. The attempt records a network failure, the timeline shows the wait once, the execution fails for its timeout, and
// no failover follows, even with a fallback configured.
func TestNetworkWaitUntilTheTimeout(t *testing.T) {
	e := newEnv(t, func(e *env) { e.timeoutUnit = time.Second })
	doc := strings.Replace(taskYAML("waiting", "    fallback:\n      candidates:\n        - provider: claude-code\n"+
		"  execution:\n    timeoutMinutes: 2\n    cancelGraceSeconds: 1\n"), "provider: claude-code", "provider: codex", 1)
	if _, err := e.app.CreateTask(t.Context(), TaskInput{YAML: doc}); err != nil {
		t.Fatal(err)
	}
	e.codex.setScript(func(ports.RunRequest) string {
		return "echo 'WAIT network'; echo 'WAIT network'; echo 'WAIT network'; sleep 30"
	})
	r, _ := e.app.RunTask("waiting")
	x := e.wait(r.ExecutionID, domain.StatusFailed)
	if x.ErrorKey != "execution_timed_out" {
		t.Errorf("the execution failed with %q (%s), want execution_timed_out", x.ErrorKey, x.ErrorMessage)
	}
	if len(x.Attempts) != 1 || x.Attempts[0].Harness != domain.HarnessCodex ||
		x.Attempts[0].Outcome != domain.AttemptNetworkFailed || x.HarnessSwitches != 0 {
		t.Fatalf("attempts %+v, %d switches", x.Attempts, x.HarnessSwitches)
	}
	e.claude.mu.Lock()
	prompts := len(e.claude.prompts)
	e.claude.mu.Unlock()
	if prompts != 0 {
		t.Errorf("Claude Code got %d prompts", prompts)
	}
	events, _, _ := e.app.ExecutionEvents(r.ExecutionID, 0, 0)
	waits := 0
	for _, ev := range events {
		switch ev.Type {
		case domain.EventHarnessSwitched:
			t.Errorf("a %s event followed the wait for the network", ev.Type)
		case domain.EventHarnessNetworkWaiting:
			waits++
			if ev.Severity != domain.SeverityWarning || ev.Message != "Reconnecting... waiting for network" {
				t.Errorf("the wait's event: %+v", ev)
			}
		}
	}
	if waits != 1 {
		t.Errorf("%d %s events, want 1", waits, domain.EventHarnessNetworkWaiting)
	}
}

// requests keeps what a fake harness received for each attempt, leaving out validation's policy check, which prepares
// with no prompt.
type requests struct {
	mu   sync.Mutex
	sent []ports.RunRequest
}

func (q *requests) add(req ports.RunRequest) {
	if req.Prompt == "" {
		return
	}
	q.mu.Lock()
	q.sent = append(q.sent, req)
	q.mu.Unlock()
}

func (q *requests) all() []ports.RunRequest {
	q.mu.Lock()
	defer q.mu.Unlock()
	return slices.Clone(q.sent)
}

// promptCount is how many attempts the harness was prepared for.
func (f *fakeHarness) promptCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.prompts)
}

// eventCounts counts an execution's logged events by type, and keeps the last one of each.
func (e *env) eventCounts(executionID string) (map[string]int, map[string]domain.Event) {
	e.t.Helper()
	events, _, err := e.app.ExecutionEvents(executionID, 0, 0)
	if err != nil {
		e.t.Fatalf("ExecutionEvents(%s): %v", executionID, err)
	}
	counts, last := map[string]int{}, map[string]domain.Event{}
	for _, ev := range events {
		counts[ev.Type]++
		last[ev.Type] = ev
	}
	return counts, last
}

const codexFallback = "    fallback:\n      candidates:\n        - provider: codex\n"

// harmflow-app#215: Claude Code exhausts its quota and Codex, its only fallback, is not ready. The execution fails,
// never completes, says that no alternative was available and why, and notifies the user natively. The executor checks
// a harness only as it is about to run it, so the quota is recorded as a switch first (HarnessSwitches 1 and a
// harness.switched event); Codex is then found not ready, and the reason the execution gives is Codex's, unavailable,
// while harness.quota_exhausted keeps Claude Code's.
func TestQuotaWithNoReadyAlternative(t *testing.T) {
	e := newEnv(t)
	e.create("stranded", codexFallback)
	e.codex.mu.Lock()
	e.codex.unavailable = true
	e.codex.mu.Unlock()
	e.claude.setScript(func(ports.RunRequest) string { return "echo 'END quota'" })
	r, _ := e.app.RunTask("stranded")
	x := e.wait(r.ExecutionID, domain.StatusFailed)
	e.idle("stranded") // the notice is decided once the execution has returned
	if len(x.Attempts) != 1 || x.Attempts[0].Harness != domain.HarnessClaudeCode ||
		x.Attempts[0].Outcome != domain.AttemptQuotaExhausted || x.HarnessSwitches != 1 {
		t.Fatalf("attempts %+v, %d switches; want one quota-exhausted Claude Code attempt and 1 switch", x.Attempts,
			x.HarnessSwitches)
	}
	if x.ErrorKey != "no_alternative_harness_detail" || !strings.Contains(x.ErrorMessage, "no alternative harness is available") ||
		!strings.Contains(x.ErrorMessage, "codex is unavailable: codex is not signed in") {
		t.Errorf("the execution's error is %q (%s), want no_alternative_harness_detail naming Codex's sign-in",
			x.ErrorMessage, x.ErrorKey)
	}
	if e.codex.promptCount() != 0 {
		t.Errorf("Codex got %d prompts, want 0", e.codex.promptCount())
	}
	counts, last := e.eventCounts(r.ExecutionID)
	for _, typ := range []string{domain.EventHarnessQuotaExhausted, domain.EventHarnessSwitched,
		domain.EventHarnessUnavailable, domain.EventHarnessNoAlternative, domain.EventExecutionFailed} {
		if counts[typ] != 1 {
			t.Errorf("%d %s events, want 1", counts[typ], typ)
		}
	}
	if counts[domain.EventExecutionCompleted] != 0 {
		t.Errorf("an execution.completed event was logged")
	}
	if ev := last[domain.EventHarnessQuotaExhausted]; ev.Harness != domain.HarnessClaudeCode {
		t.Errorf("%s names %q, want %s", ev.Type, ev.Harness, domain.HarnessClaudeCode)
	}
	if ev := last[domain.EventHarnessUnavailable]; ev.Harness != domain.HarnessCodex {
		t.Errorf("%s names %q, want %s", ev.Type, ev.Harness, domain.HarnessCodex)
	}
	none := last[domain.EventHarnessNoAlternative]
	if none.Severity != domain.SeverityError || none.Payload["harness"] != domain.HarnessCodex ||
		none.Payload["reason"] != string(domain.ClassHarnessUnavailable) {
		t.Errorf("%s = %+v, want an error naming Codex, unavailable", none.Type, none)
	}
	var notices []Notification
	for _, n := range e.app.Notifications(false) {
		if n.EventType == domain.EventHarnessNoAlternative && n.ExecutionID == r.ExecutionID {
			notices = append(notices, n)
		}
	}
	if len(notices) != 1 || notices[0].Severity != "error" || notices[0].Body == "" {
		t.Errorf("notices of %s = %+v, want one error notice", domain.EventHarnessNoAlternative, notices)
	}
}

// harmflow-app#215: a Claude Code attempt that ends signed out fails over to Codex, which completes the execution.
func TestSignedOutDuringAnAttemptFailsOver(t *testing.T) {
	e := newEnv(t)
	e.create("signed-out", codexFallback)
	e.claude.setScript(func(ports.RunRequest) string { return "echo working; echo 'END unavailable'" })
	e.codex.setScript(func(ports.RunRequest) string { return "echo 'END ok'" })
	r, _ := e.app.RunTask("signed-out")
	x := e.wait(r.ExecutionID, domain.StatusCompleted)
	if len(x.Attempts) != 2 || x.HarnessSwitches != 1 ||
		x.Attempts[0].Harness != domain.HarnessClaudeCode || x.Attempts[0].Outcome != domain.AttemptHarnessUnavailable ||
		x.Attempts[1].Harness != domain.HarnessCodex || x.Attempts[1].Outcome != domain.AttemptSucceeded {
		t.Fatalf("attempts %+v, %d switches; want Claude Code signed out, then Codex succeeding", x.Attempts,
			x.HarnessSwitches)
	}
	counts, last := e.eventCounts(r.ExecutionID)
	switched := last[domain.EventHarnessSwitched]
	if counts[domain.EventHarnessSwitched] != 1 || switched.Payload["from"] != domain.HarnessClaudeCode ||
		switched.Payload["to"] != domain.HarnessCodex || switched.Payload["reason"] != string(domain.ClassHarnessUnavailable) {
		t.Errorf("%d %s events, the last %+v; want one from Claude Code to Codex for harness_unavailable",
			counts[domain.EventHarnessSwitched], domain.EventHarnessSwitched, switched)
	}
}

// harmflow-app#215: with Codex configured as a fallback, neither an attempt that reaches the execution's timeout nor
// one that fails with a task error fails over: the execution fails after one attempt, with no harness.switched event.
func TestNoFailoverOnATimeoutOrATaskError(t *testing.T) {
	for _, c := range []struct {
		name, script string
		outcome      domain.AttemptOutcome
		key, message string // the execution's error
	}{
		{"timeout", "echo working; sleep 30", domain.AttemptTimedOut, "execution_timed_out", "the execution timeout elapsed"},
		{"task error", "echo working; echo 'END error'", domain.AttemptFailed, "", "exit 0"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, func(e *env) { e.timeoutUnit = time.Second })
			e.create("stay", codexFallback+"  execution:\n    timeoutMinutes: 2\n    cancelGraceSeconds: 1\n")
			e.claude.setScript(func(ports.RunRequest) string { return c.script })
			e.codex.setScript(func(ports.RunRequest) string { return "echo 'END ok'" })
			r, _ := e.app.RunTask("stay")
			x := e.wait(r.ExecutionID, domain.StatusFailed)
			if len(x.Attempts) != 1 || x.Attempts[0].Harness != domain.HarnessClaudeCode ||
				x.Attempts[0].Outcome != c.outcome || x.HarnessSwitches != 0 {
				t.Fatalf("attempts %+v, %d switches; want one Claude Code attempt %s and no switch", x.Attempts,
					x.HarnessSwitches, c.outcome)
			}
			if x.ErrorKey != c.key || x.ErrorMessage != c.message {
				t.Errorf("the execution's error is %q (%q), want %q (%q)", x.ErrorMessage, x.ErrorKey, c.message, c.key)
			}
			if n := e.codex.promptCount(); n != 0 {
				t.Errorf("Codex got %d prompts, want 0", n)
			}
			if counts, _ := e.eventCounts(r.ExecutionID); counts[domain.EventHarnessSwitched] != 0 {
				t.Errorf("%d %s events, want 0", counts[domain.EventHarnessSwitched], domain.EventHarnessSwitched)
			}
		})
	}
}

// harmflow-app#215: one timeout covers every attempt of an execution. The first attempt exhausts its quota and fails
// over, and the alternative, which ignores SIGTERM, is stopped when the same timeout elapses, and killed once its grace
// is over. A timeout started afresh for the alternative could not end it before the first attempt's quota plus the
// timeout plus the grace, so an execution that ends before that bound kept one timeout.
//
// The first attempt reports its quota once it reads the line the test writes 3 s in, so its shell's start counts
// within those 3 s, not after them, and no sleep has to start. The rest of the timeout's 8 s is for the alternative's
// process to start and ignore SIGTERM, or nothing tests its grace. The timeout is no longer so that 50 runs, which show
// the test holds under the load of a full test run, fit within go test's default 10 minutes, about 9 s each.
func TestOneTimeoutAcrossAFailover(t *testing.T) {
	const timeout, grace, quotaAfter = 8 * time.Second, time.Second, 3 * time.Second
	e := newEnv(t, func(e *env) { e.timeoutUnit = time.Second })
	e.create("shared", codexFallback+"  execution:\n    timeoutMinutes: 8\n    cancelGraceSeconds: 1\n")
	dir := t.TempDir()
	fifo := filepath.Join(dir, "quota")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("Mkfifo(%s): %v", fifo, err)
	}
	// Read-write, so the open does not wait for the shell, and the shell's own open does not wait for the test.
	feed, err := os.OpenFile(fifo, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open %s: %v", fifo, err)
	}
	t.Cleanup(func() { _ = feed.Close() }) // a reader still waiting then reads the end of the file
	e.claude.setScript(func(ports.RunRequest) string { return fmt.Sprintf("read quota < %s; echo 'END quota'", fifo) })
	e.codex.setScript(func(ports.RunRequest) string {
		return fmt.Sprintf("trap '' TERM; echo $$ > %s/codex; echo working; sleep 30; echo 'END ok'", dir)
	})
	began := time.Now()
	r, _ := e.app.RunTask("shared")
	time.Sleep(quotaAfter - time.Since(began))
	// Taken before the write: the first attempt cannot read the line, and so end, any earlier.
	quota := time.Since(began)
	if _, err := feed.WriteString("quota\n"); err != nil {
		t.Fatalf("write %s: %v", fifo, err)
	}
	x := e.wait(r.ExecutionID, domain.StatusFailed)
	elapsed := time.Since(began)
	if len(x.Attempts) != 2 {
		t.Fatalf("%d attempts %+v, the quota %s in; want 2: the quota did not fail over before the timeout",
			len(x.Attempts), x.Attempts, quota)
	}
	if x.ErrorKey != "execution_timed_out" || x.HarnessSwitches != 1 {
		t.Errorf("the execution failed with %q (%s) after %d switches, want execution_timed_out after 1", x.ErrorMessage,
			x.ErrorKey, x.HarnessSwitches)
	}
	if at := x.Attempts[1]; at.Harness != domain.HarnessCodex || at.Outcome != domain.AttemptTimedOut || at.EndedAt == nil {
		t.Errorf("Codex's attempt %+v, want it ended, timed out", at)
	}
	// Codex's shell writes its pid once it ignores SIGTERM, so the file is there when the execution ends unless the
	// timeout's SIGTERM ended the shell as it started.
	ignored, err := os.Stat(filepath.Join(dir, "codex"))
	if err != nil {
		t.Fatalf("Codex's pid file: %v, the quota %s in; want it written before the timeout, once Codex ignored SIGTERM",
			err, quota)
	}
	if fresh := quota + timeout + grace; elapsed < timeout+grace || elapsed >= fresh {
		t.Errorf("the execution ended after %s, the quota %s in and Codex ignoring SIGTERM %s in; want from %s, the "+
			"timeout and its grace, to before %s, the earliest end of a timeout started afresh", elapsed, quota,
			ignored.ModTime().Sub(began), timeout+grace, fresh)
	}
	if pid := waitForPid(t, filepath.Join(dir, "codex")); alive(pid) {
		t.Errorf("Codex's process %d outlived the execution", pid)
	}
}

// harmflow-app#216: after a quota failover from Claude Code, Codex runs with the permissions, work directory, and
// additional directories of the first attempt, its issue task's repository among them, and its prepared sandbox is
// workspace-write over the same directories.
func TestFailoverKeepsThePermissions(t *testing.T) {
	e := newEnv(t)
	work := filepath.Join(e.app.Paths().UserHome, "worktree")
	repo := mkdir(t, filepath.Join(e.app.Paths().UserHome, "repo"))
	doc := strings.Replace(taskYAML("confined", codexFallback+"  workspace:\n    path: "+work+"\n"+
		"  permissions:\n    profile: workspace_write\n    restrictToWorkspace: true\n"+
		fmt.Sprintf("  github:\n    repositories:\n      - name: o/r\n        localPath: %q\n", repo)),
		"type: general", "type: github", 1)
	if _, err := e.app.CreateTask(t.Context(), TaskInput{YAML: doc}); err != nil {
		t.Fatalf("CreateTask(confined): %v", err)
	}
	var q requests
	e.claude.setScript(func(req ports.RunRequest) string { q.add(req); return "echo 'END quota'" })
	e.codex.setScript(func(req ports.RunRequest) string { q.add(req); return "echo 'END ok'" })
	r, _ := e.app.RunTask("confined")
	x := e.wait(r.ExecutionID, domain.StatusCompleted)
	sent := q.all()
	if len(sent) != 2 || len(x.Attempts) != 2 || x.Attempts[1].Harness != domain.HarnessCodex {
		t.Fatalf("%d requests, attempts %+v; want 2, the second on Codex", len(sent), x.Attempts)
	}
	first, second := sent[0], sent[1]
	if p := first.Permissions; p.Profile != domain.ProfileWorkspaceWrite || p.RestrictToWorkspace == nil ||
		!*p.RestrictToWorkspace {
		t.Errorf("the first attempt's permissions %+v, want workspace_write restricted to the workspace", p)
	}
	if !reflect.DeepEqual(second.Permissions, first.Permissions) {
		t.Errorf("the second attempt's permissions %+v, want the first's %+v", second.Permissions, first.Permissions)
	}
	resolved, _ := domain.ResolvePath(work)
	if first.WorkDir != resolved || second.WorkDir != first.WorkDir {
		t.Errorf("work directories %q and %q, want %q for both", first.WorkDir, second.WorkDir, resolved)
	}
	resolvedRepo, _ := domain.ResolvePath(repo)
	if want := []string{resolvedRepo}; !slices.Equal(first.AdditionalDirs, want) ||
		!slices.Equal(second.AdditionalDirs, first.AdditionalDirs) {
		t.Errorf("additional directories %q and %q, want %q for both", first.AdditionalDirs, second.AdditionalDirs, want)
	}
	info := ports.HarnessInfo{Path: "codex", Features: map[string]bool{"--add-dir": true}}
	inv, _, err := (&codex.Adapter{Home: t.TempDir()}).Prepare(info, second)
	if err != nil {
		t.Fatalf("codex Prepare(second attempt): %v", err)
	}
	args := strings.Join(inv.Args, " ")
	for _, want := range []string{"--sandbox workspace-write", "--cd " + second.WorkDir, "--add-dir " + resolvedRepo} {
		if !strings.Contains(args, want) {
			t.Errorf("codex Prepare(second attempt) args %q, want %q", inv.Args, want)
		}
	}
}

// harmflow-app#216: the first attempt writes a file in the workspace, then exhausts its quota. The second runs in the
// same directory with the file present, and receives the task's prompt, its agents and skills resolved for Codex, and
// the summary of the first attempt.
func TestFailoverKeepsTheWorkspaceAndInput(t *testing.T) {
	e := newEnv(t)
	e.create("handover", codexFallback+"  agents: [user-agent]\n  skills: [user-skill, hf-github-draft-pr]\n")
	var q requests
	e.claude.setScript(func(req ports.RunRequest) string {
		q.add(req)
		return "echo draft > notes.txt; echo 'Wrote notes.txt'; echo 'END quota'"
	})
	// Codex succeeds only if it finds the first attempt's file where it runs.
	e.codex.setScript(func(req ports.RunRequest) string {
		q.add(req)
		return `if [ "$(cat notes.txt)" = draft ]; then echo 'END ok'; else echo 'END error'; fi`
	})
	r, _ := e.app.RunTask("handover")
	x := e.wait(r.ExecutionID, domain.StatusCompleted)
	sent := q.all()
	if len(sent) != 2 || len(x.Attempts) != 2 || x.Attempts[1].Harness != domain.HarnessCodex {
		t.Fatalf("%d requests, attempts %+v; want 2, the second on Codex", len(sent), x.Attempts)
	}
	first, second := sent[0], sent[1]
	if second.WorkDir != first.WorkDir || x.WorkspacePath != first.WorkDir {
		t.Errorf("work directories %q and %q, workspace %q; want one", first.WorkDir, second.WorkDir, x.WorkspacePath)
	}
	if data, err := os.ReadFile(filepath.Join(second.WorkDir, "notes.txt")); err != nil || string(data) != "draft\n" {
		t.Errorf("notes.txt in the second attempt's directory = %q (%v), want the first attempt's draft", data, err)
	}
	if second.Prompt != "Do the work of handover." {
		t.Errorf("the second attempt's prompt %q, want the task's", second.Prompt)
	}
	items := func(list []ports.ResolvedItem) []string {
		var out []string
		for _, it := range list {
			out = append(out, it.Kind+" "+it.Name+" "+it.Path)
		}
		return out
	}
	builtin := filepath.Join(e.app.d.Paths.Builtins, "skills", "hf-github-draft-pr")
	if got, want := items(second.Agents), []string{"agent user-agent /codex/agents/user-agent"}; !slices.Equal(got, want) {
		t.Errorf("the second attempt's agents %q, want %q", got, want)
	}
	if got, want := items(second.Skills), []string{"skill user-skill /codex/skills/user-skill",
		"skill hf-github-draft-pr " + builtin}; !slices.Equal(got, want) {
		t.Errorf("the second attempt's skills %q, want %q", got, want)
	}
	if want := "The previous attempt ran on claude-code and ended: quota exhausted. Its last message: Wrote notes.txt"; !strings.Contains(second.Context, want) {
		t.Errorf("the second attempt's context does not hold %q:\n%s", want, second.Context)
	}
}

// checkpointBeforeCodex runs task, whose Claude Code attempt runs script and exhausts its quota, and returns the first
// attempt's checkpoint as the record held it when Codex was being prepared, before its attempt started, and the
// context Codex got.
func (e *env) checkpointBeforeCodex(task, script string) (*domain.Checkpoint, string) {
	e.t.Helper()
	e.claude.setScript(func(ports.RunRequest) string { return script + "; echo 'END quota'" })
	var mu sync.Mutex
	var seen *domain.Checkpoint
	var sent string
	e.codex.setScript(func(req ports.RunRequest) string {
		x, err := e.app.GetExecution(req.ExecutionID)
		mu.Lock()
		defer mu.Unlock()
		if err == nil && len(x.Attempts) == 2 { // the second attempt is recorded before its harness is prepared
			seen = x.Attempts[0].Checkpoint
		}
		sent = req.Context
		return "echo 'END ok'"
	})
	r, _ := e.app.RunTask(task)
	x := e.wait(r.ExecutionID, domain.StatusCompleted)
	if len(x.Attempts) != 2 || x.Attempts[0].Outcome != domain.AttemptQuotaExhausted {
		e.t.Fatalf("attempts %+v, want a quota ending, then Codex", x.Attempts)
	}
	if x.Attempts[1].Checkpoint != nil {
		e.t.Errorf("the last attempt's checkpoint = %+v, want none: no switch followed it", x.Attempts[1].Checkpoint)
	}
	mu.Lock()
	defer mu.Unlock()
	return seen, sent
}

// writeOld writes a file whose modification time is an hour before now, as one left before the attempt.
func writeOld(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

// gitIn runs git in dir with no user or system configuration, failing the test when it fails.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=HarmFlow", "-c",
		"user.email=harmflow@example.invalid"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

// harmflow-app#214: the attempt creates a work item's worktree directly under its workspace, as the built-in skills do,
// commits a file there, and leaves another written by a shell command, which no adapter event reports, before its
// quota ends. Before Codex starts, the first attempt's checkpoint lists both with their status, measured from the
// commit the worktree was created from, and Codex's context names them. The file the worktree started with is not
// listed.
func TestCheckpointListsShellWrittenFilesInAWorktree(t *testing.T) {
	e := newEnv(t)
	origin := t.TempDir()
	gitIn(t, origin, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(origin, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, origin, "add", "README.md")
	gitIn(t, origin, "commit", "-q", "-m", "base")
	e.create("worktree", codexFallback)
	git := `GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 git -c user.name=HarmFlow -c user.email=harmflow@example.invalid`
	c, sent := e.checkpointBeforeCodex("worktree", git+` -C '`+origin+`' worktree add -q -b fix/1-shell "$PWD/harmflow-1" main && `+
		`cd harmflow-1 && echo fix > fix.txt && `+git+` add fix.txt && `+git+` commit -q -m fix && `+
		`sed -i.bak 's/base/changed/' README.md && rm README.md.bak && echo wip > wip.txt`)
	if c == nil {
		t.Fatalf("no checkpoint on the first attempt before Codex started; Codex's context:\n%s", sent)
	}
	want := []domain.CheckpointFile{{Path: "harmflow-1/README.md", Status: domain.FileModified},
		{Path: "harmflow-1/fix.txt", Status: domain.FileAdded}, {Path: "harmflow-1/wip.txt", Status: domain.FileUntracked}}
	if !slices.Equal(c.Files, want) || c.More != 0 || c.ScanLimited {
		t.Errorf("checkpoint %+v, want the files %+v", c, want)
	}
	// The workspace itself is a plain folder, so the part outside the worktree was scanned.
	if !c.Scanned {
		t.Errorf("checkpoint %+v: the workspace outside the worktree is not marked as scanned", c)
	}
	for _, f := range []string{"harmflow-1/README.md (modified)", "harmflow-1/fix.txt (added)", "harmflow-1/wip.txt (untracked)"} {
		if !strings.Contains(sent, f) {
			t.Errorf("Codex's context does not name %q:\n%s", f, sent)
		}
	}
}

// harmflow-app#214: in a workspace that is not a Git repository, the attempt writes a file from a shell command before
// its quota ends. Before Codex starts, the checkpoint lists it, found by its modification time, and not the file left
// there before the attempt; Codex's context names it and says what a scan cannot see.
func TestCheckpointListsAShellWrittenFileOutsideGit(t *testing.T) {
	e := newEnv(t)
	e.create("plain", codexFallback)
	work := filepath.Join(e.paths.Workspace, "plain")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	writeOld(t, filepath.Join(work, "old.txt"))
	c, sent := e.checkpointBeforeCodex("plain", "mkdir -p notes && echo half > notes/partial.txt")
	if c == nil {
		t.Fatalf("no checkpoint on the first attempt before Codex started; Codex's context:\n%s", sent)
	}
	want := []domain.CheckpointFile{{Path: "notes/partial.txt", Status: domain.FileModified}}
	if !slices.Equal(c.Files, want) || !c.Scanned || c.ScanLimited || c.More != 0 {
		t.Errorf("checkpoint %+v, want the scanned files %+v", c, want)
	}
	for _, s := range []string{"notes/partial.txt (modified)",
		"a file it deleted, or one whose modification time was kept, is not listed"} {
		if !strings.Contains(sent, s) {
			t.Errorf("Codex's context does not hold %q:\n%s", s, sent)
		}
	}
}

// harmflow-app#214: the session context names at most the checkpoint's files, counts the rest, and says when the
// scan stopped at its limit.
func TestCheckpointText(t *testing.T) {
	files := make([]domain.CheckpointFile, domain.MaxCheckpointFiles+2)
	for i := range files {
		files[i] = domain.CheckpointFile{Path: fmt.Sprintf("f%04d.txt", i), Status: domain.FileModified}
	}
	c := domain.NewCheckpoint(files)
	c.Scanned, c.ScanLimited = true, true
	text := checkpointText(c)
	for _, s := range []string{"f0000.txt (modified)", "f0499.txt (modified), and 2 more.",
		fmt.Sprintf("stopped at its limit of %d files and folders", domain.CheckpointScanLimit)} {
		if !strings.Contains(text, s) {
			t.Errorf("checkpointText does not hold %q:\n%s", s, text)
		}
	}
	if strings.Contains(text, "f0500.txt") {
		t.Errorf("checkpointText names a file past the first %d:\n%s", domain.MaxCheckpointFiles, text)
	}
	if got := checkpointText(domain.NewCheckpoint(nil)); got != "No file it changed was found in the workspace." {
		t.Errorf("checkpointText(no files) = %q", got)
	}
}

// harmflow-app#216: a fallback that cannot load the task's skill is not valid. The task saves with a warning that names
// Codex and the skill, and a run that exhausts Claude Code's quota does not switch to Codex.
func TestFallbackMissingASkillIsNotValid(t *testing.T) {
	e := newEnv(t)
	view, err := e.app.CreateTask(t.Context(), TaskInput{YAML: taskYAML("notes", codexFallback+"  skills: [review-notes]\n")})
	if err != nil {
		t.Fatalf("CreateTask(notes): %v", err)
	}
	var warned []schema.Issue
	for _, w := range view.Validation.Warnings {
		if w.Code == domain.WarningInvalidFallback {
			warned = append(warned, w)
		}
	}
	want := `codex is not a valid fallback: skill "review-notes" cannot be found for it (the codex configuration)`
	if len(warned) != 1 || warned[0].Key != "fallback_skill_missing" || warned[0].Message != want ||
		warned[0].Params["harness"] != domain.HarnessCodex || warned[0].Params["name"] != "review-notes" {
		t.Fatalf("CreateTask(notes) warnings %+v, want one %s: %s", view.Validation.Warnings,
			domain.WarningInvalidFallback, want)
	}

	e.claude.setScript(func(ports.RunRequest) string { return "echo 'END quota'" })
	r, _ := e.app.RunTask("notes")
	x := e.wait(r.ExecutionID, domain.StatusFailed)
	if len(x.Attempts) != 1 || x.HarnessSwitches != 0 || !strings.Contains(x.ErrorMessage, "no alternative") {
		t.Errorf("attempts %+v, %d switches, error %q; want one attempt failing with no alternative", x.Attempts,
			x.HarnessSwitches, x.ErrorMessage)
	}
	if n := e.codex.promptCount(); n != 0 {
		t.Errorf("Codex got %d prompts, want 0", n)
	}
	if counts, _ := e.eventCounts(r.ExecutionID); counts[domain.EventHarnessSwitched] != 0 {
		t.Errorf("%d %s events, want 0", counts[domain.EventHarnessSwitched], domain.EventHarnessSwitched)
	}
	if !slices.ContainsFunc(x.Warnings, func(w domain.Warning) bool {
		return w.Code == domain.WarningInvalidFallback && w.Text.Message == want
	}) {
		t.Errorf("the execution's warnings %+v, want %s: %s", x.Warnings, domain.WarningInvalidFallback, want)
	}
}

// An attempt records the model and effort its harness reported, and says nothing about the ones it did not report;
// the configured values are kept apart and never stand in for them.
func TestAttemptRecordsTheReportedModel(t *testing.T) {
	e := newEnv(t)
	e.create("attributed", "      effort: high\n    fallback:\n      candidates:\n        - provider: codex\n")
	e.claude.setScript(func(ports.RunRequest) string { return "echo 'STARTED claude-haiku-4-5'; echo 'END quota'" })
	e.codex.setScript(func(ports.RunRequest) string { return "echo 'END ok'" })
	r, _ := e.app.RunTask("attributed")
	x := e.wait(r.ExecutionID, domain.StatusCompleted)
	if len(x.Attempts) != 2 {
		t.Fatalf("attempts: %+v", x.Attempts)
	}
	claude, codex := x.Attempts[0], x.Attempts[1]
	if claude.Model != "claude-haiku-4-5" || claude.Effort != "" || claude.ConfiguredModel != "default" ||
		claude.ConfiguredEffort != "high" {
		t.Errorf("Claude Code's attempt: %+v", claude)
	}
	if codex.Model != "" || codex.Effort != "" || codex.ConfiguredModel != "default" ||
		codex.ConfiguredEffort != "default" {
		t.Errorf("Codex's attempt: %+v", codex)
	}
	// The execution's model follows its current attempt, which reported none.
	if x.Model != "" {
		t.Errorf("execution model %q", x.Model)
	}
	// A harness that reports its effort has it recorded too.
	e.claude.setScript(func(ports.RunRequest) string { return "echo 'STARTED claude-opus-4-1 max'; echo 'END ok'" })
	r, _ = e.app.RunTask("attributed")
	x = e.wait(r.ExecutionID, domain.StatusCompleted)
	if at := x.Attempts[0]; at.Model != "claude-opus-4-1" || at.Effort != "max" || x.Model != "claude-opus-4-1" {
		t.Errorf("attempt %+v, execution model %q", at, x.Model)
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
	live := e.poll(10*time.Second, func() bool {
		x, _ := e.app.GetExecution(r.ExecutionID)
		return len(x.Attempts) == 2 && x.Attempts[1].Usage != nil && x.Attempts[1].Usage.Status == domain.UsagePartial &&
			x.Attempts[1].Usage.Total().Input == 40
	})
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

// Each attempt keeps the prompt and the session context the harness received, redacted like the raw output, in the
// record on disk too; a later edit of the task's prompt does not change what the execution shows.
func TestAttemptKeepsItsInput(t *testing.T) {
	e := newEnv(t)
	token := "ghp_" + strings.Repeat("a", 36)
	prompt := "Push with " + token + ", then report."
	e.create("input", "    fallback:\n      candidates:\n        - provider: codex\n")
	view, _ := e.app.GetTask("input")
	if _, err := e.app.UpdateTask(t.Context(), "input", TaskInput{YAML: strings.Replace(view.YAML, "Do the work of input.", prompt, 1),
		BaseRevision: view.Revision}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var sent []ports.RunRequest
	capture := func(req ports.RunRequest) {
		if req.Prompt == "" { // validation's policy check prepares with no prompt
			return
		}
		mu.Lock()
		sent = append(sent, req)
		mu.Unlock()
	}
	e.claude.setScript(func(req ports.RunRequest) string { capture(req); return "echo 'END quota'" })
	e.codex.setScript(func(req ports.RunRequest) string { capture(req); return "echo 'END ok'" })
	r, _ := e.app.RunTask("input")
	x := e.wait(r.ExecutionID, domain.StatusCompleted)
	e.idle("input")
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 2 || len(x.Attempts) != 2 {
		t.Fatalf("%d requests and %d attempts, want 2 of each", len(sent), len(x.Attempts))
	}
	redacted := "Push with [REDACTED], then report."
	for i, at := range x.Attempts {
		if sent[i].Prompt != prompt {
			t.Errorf("attempt %d: the harness received %q, want the prompt unredacted", at.Number, sent[i].Prompt)
		}
		if at.Prompt != redacted {
			t.Errorf("attempt %d: the record keeps the prompt %q, want %q", at.Number, at.Prompt, redacted)
		}
		if at.SessionContext == "" || at.SessionContext != sent[i].Context {
			t.Errorf("attempt %d: the record keeps the context\n%s\nwant the one the harness received\n%s", at.Number,
				at.SessionContext, sent[i].Context)
		}
	}
	// The context is composed per attempt: the failover's tells the session it continues the previous one.
	if !strings.Contains(x.Attempts[1].SessionContext, "continuing the same execution") ||
		strings.Contains(x.Attempts[0].SessionContext, "continuing the same execution") {
		t.Errorf("the attempts do not keep their own contexts:\n%s\n---\n%s", x.Attempts[0].SessionContext,
			x.Attempts[1].SessionContext)
	}
	data, err := os.ReadFile(filepath.Join(e.home, "state", "executions", r.ExecutionID+".json"))
	var saved domain.Execution
	if err == nil {
		err = json.Unmarshal(data, &saved)
	}
	if err != nil || len(saved.Attempts) != 2 || saved.Attempts[0].Prompt != redacted ||
		saved.Attempts[1].SessionContext != x.Attempts[1].SessionContext {
		t.Errorf("the record on disk lacks the input (%v):\n%s", err, data)
	}
	if strings.Contains(string(data), "ghp_") {
		t.Errorf("the token survived in the record:\n%s", data)
	}

	view, _ = e.app.GetTask("input")
	if _, err := e.app.UpdateTask(t.Context(), "input", TaskInput{YAML: strings.Replace(view.YAML, "then report.", "then stop.", 1),
		BaseRevision: view.Revision}); err != nil {
		t.Fatal(err)
	}
	if view, _ = e.app.GetTask("input"); !strings.Contains(view.YAML, "then stop.") {
		t.Fatalf("the edit did not apply:\n%s", view.YAML)
	}
	if x, _ = e.app.GetExecution(r.ExecutionID); x.Attempts[0].Prompt != redacted || x.Attempts[1].Prompt != redacted {
		t.Errorf("after the edit the execution shows %q and %q, want the prompt it sent", x.Attempts[0].Prompt,
			x.Attempts[1].Prompt)
	}
}

// The input is recorded before the harness starts, so a running attempt shows it.
func TestRunningAttemptShowsItsInput(t *testing.T) {
	e := newEnv(t)
	e.create("live-input", "")
	e.claude.setScript(func(ports.RunRequest) string { return slow })
	r, _ := e.app.RunTask("live-input")
	var at domain.Attempt
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if x, _ := e.app.GetExecution(r.ExecutionID); len(x.Attempts) == 1 && x.Attempts[0].Prompt != "" {
			at = x.Attempts[0]
			break
		}
	}
	if at.Outcome != domain.AttemptRunning || at.Prompt != "Do the work of live-input." || at.SessionContext == "" {
		t.Errorf("the running attempt: outcome %q, prompt %q, context %q", at.Outcome, at.Prompt, at.SessionContext)
	}
	if err := e.app.CancelExecution(r.ExecutionID); err != nil {
		t.Fatal(err)
	}
	e.wait(r.ExecutionID, domain.StatusCancelled)
}

// A failover's context quotes the previous attempt's last message, so a secret the agent printed would reach the next
// harness, perhaps another provider's. The daemon redacts it as it reads it, so the next harness, the record, and the
// record on disk all show [REDACTED].
func TestFailoverContextRedacted(t *testing.T) {
	e := newEnv(t)
	token := "ghp_" + strings.Repeat("b", 36)
	e.create("leak", "    fallback:\n      candidates:\n        - provider: codex\n")
	var mu sync.Mutex
	var sent string
	e.claude.setScript(func(ports.RunRequest) string { return "echo 'Pushed with " + token + "'; echo 'END quota'" })
	e.codex.setScript(func(req ports.RunRequest) string {
		if req.Prompt != "" { // validation's policy check prepares with no prompt
			mu.Lock()
			sent = req.Context
			mu.Unlock()
		}
		return "echo 'END ok'"
	})
	r, _ := e.app.RunTask("leak")
	x := e.wait(r.ExecutionID, domain.StatusCompleted)
	e.idle("leak")
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(sent, "Its last message: Pushed with [REDACTED]") || strings.Contains(sent, token) {
		t.Fatalf("the failover's context does not quote the previous attempt's last message redacted:\n%s", sent)
	}
	if len(x.Attempts) != 2 {
		t.Fatalf("%d attempts, want 2", len(x.Attempts))
	}
	if got := x.Attempts[1].SessionContext; got != sent {
		t.Errorf("the record keeps the context\n%s\nwant the one the harness received\n%s", got, sent)
	}
	data, err := os.ReadFile(filepath.Join(e.home, "state", "executions", r.ExecutionID+".json"))
	var saved domain.Execution
	if err == nil {
		err = json.Unmarshal(data, &saved)
	}
	if err != nil || len(saved.Attempts) != 2 || saved.Attempts[1].SessionContext != sent {
		t.Errorf("the record on disk lacks the redacted context (%v):\n%s", err, data)
	}
	if strings.Contains(string(data), "ghp_") {
		t.Errorf("the token survived in the record:\n%s", data)
	}
}

// What the harness needs verbatim, such as its workspace path, reaches it as is, and the record keeps it redacted like
// the prompt. No agent text reaches the context unredacted, so this is the record's own redaction.
func TestContextRedactedInTheRecord(t *testing.T) {
	e := newEnv(t)
	token := "ghp_" + strings.Repeat("d", 36)
	work := filepath.Join(e.app.Paths().UserHome, token)
	e.create("odd-path", "  workspace:\n    path: "+work+"\n")
	var mu sync.Mutex
	var sent string
	e.claude.setScript(func(req ports.RunRequest) string {
		if req.Prompt != "" { // validation's policy check prepares with no prompt
			mu.Lock()
			sent = req.Context
			mu.Unlock()
		}
		return "echo 'END ok'"
	})
	r, _ := e.app.RunTask("odd-path")
	x := e.wait(r.ExecutionID, domain.StatusCompleted)
	mu.Lock()
	defer mu.Unlock()
	// As is, once resolved: the harness gets the path the rule checked.
	resolved, _ := domain.ResolvePath(work)
	if !strings.Contains(sent, "Workspace: "+resolved) {
		t.Fatalf("the harness did not get its workspace path as is:\n%s", sent)
	}
	if got, want := x.Attempts[0].SessionContext, strings.ReplaceAll(sent, token, "[REDACTED]"); got != want {
		t.Errorf("the record keeps the context\n%s\nwant\n%s", got, want)
	}
}

// What the agent writes — its progress, its last message, its report — is redacted before HarmFlow keeps or relays
// it: in the record and the API, the work-item ledger, context.json, the event log, and the next attempt's context. It
// is redacted before it is trimmed, so a token cut short by a trim leaves no prefix behind.
func TestAgentTextRedactedEverywhere(t *testing.T) {
	e := newEnv(t)
	token := "ghp_" + strings.Repeat("c", 36)
	e.create("agent-secret", "    fallback:\n      candidates:\n        - provider: codex\n")
	var progress []string
	for _, p := range []map[string]string{
		{"type": "step.started", "phase": "running", "message": "Signing in with " + token},
		{"type": "work_item.selected", "workItem": "o/r#7", "message": "Fix " + token},
		// Trimmed to 1000 characters first, the token would keep 10 of its characters, too few to match.
		{"type": "workspace.changed", "branch": "work", "message": strings.Repeat("x", 990) + token},
	} {
		line, _ := json.Marshal(p)
		progress = append(progress, "'"+string(line)+"'")
	}
	report, _ := json.Marshal(map[string]any{"outcome": "completed", "summary": "Opened it with " + token,
		"lastOperation": "git push " + token,
		"workItems": []any{map[string]any{"key": "o/r#7", "outcome": "completed", "title": "Fix " + token,
			"tests": map[string]any{"status": "failed", "summary": token + " in the output"}}},
		"warnings": []any{map[string]any{"code": "other", "message": "saw " + token}}})
	var mu sync.Mutex
	var second string
	e.claude.setScript(func(req ports.RunRequest) string {
		return fmt.Sprintf("printf '%%s\\n' %s >> %s/progress.jsonl\necho 'Pushed with %s'\necho 'END quota'",
			strings.Join(progress, " "), req.ExecutionDir, token)
	})
	e.codex.setScript(func(req ports.RunRequest) string {
		if req.Prompt != "" { // validation's policy check prepares with no prompt
			mu.Lock()
			second = req.Context
			mu.Unlock()
		}
		return fmt.Sprintf("printf '%%s' '%s' > %s/report.json\necho 'END ok'", report, req.ExecutionDir)
	})
	r, _ := e.app.RunTask("agent-secret")
	e.wait(r.ExecutionID, domain.StatusCompleted, domain.StatusNeedsAttention)
	e.idle("agent-secret")
	x, _ := e.app.GetExecution(r.ExecutionID)
	if len(x.Attempts) != 2 || len(x.WorkItems) != 1 {
		t.Fatalf("%d attempts and %d work items, want 2 and 1", len(x.Attempts), len(x.WorkItems))
	}
	view, _ := json.Marshal(x)
	kept := map[string]string{"the API's record": string(view)}
	for name, path := range map[string]string{
		"the record on disk":   filepath.Join(e.home, "state", "executions", r.ExecutionID+".json"),
		"the work-item ledger": filepath.Join(e.home, "state", "workitems.json"),
		"context.json":         filepath.Join(x.ExecutionDir, "context.json"),
		"the event log":        filepath.Join(e.home, "logs", r.ExecutionID+".jsonl"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		kept[name] = string(data)
	}
	mu.Lock()
	kept["attempt 2's context"] = second
	mu.Unlock()
	for name, text := range kept {
		if strings.Contains(text, "ghp_") {
			t.Errorf("%s keeps the token, or part of it:\n%s", name, text)
		}
		// A field that was dropped would also hold no token; [REDACTED] shows the text was kept, redacted.
		if !strings.Contains(text, "[REDACTED]") {
			t.Errorf("%s has no [REDACTED]:\n%s", name, text)
		}
	}
}

// HarmFlow's context never names the task, since a prompt may name the same thing as the task's id or name, and it
// points at context.json only once the file holds a previous attempt, work items, or parameters.
func TestContextNeverNamesTheTask(t *testing.T) {
	e := newEnv(t)
	if _, err := e.app.CreateTask(t.Context(), TaskInput{YAML: `apiVersion: harmflow/v1alpha1
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

// A harness already unavailable when the task fires is skipped for the fallback: the event names it, and the execution
// starts on the fallback.
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
			if ev.Harness != domain.HarnessClaudeCode || !strings.Contains(ev.Message, domain.HarnessClaudeCode) {
				t.Errorf("%s names %q (%q), want %s", ev.Type, ev.Harness, ev.Message, domain.HarnessClaudeCode)
			}
		case domain.EventExecutionStarted:
			started = i
			if ev.Harness != domain.HarnessCodex {
				t.Errorf("%s names %q, want %s", ev.Type, ev.Harness, domain.HarnessCodex)
			}
		}
	}
	if unavailable < 0 {
		t.Error("no explicit event named the unavailable harness")
	}
	// The harness is revalidated before the execution starts.
	if started < unavailable {
		t.Errorf("execution.started at %d, harness.unavailable at %d", started, unavailable)
	}
}

// With no ready harness in its chain, the execution fails without starting, and names the harness.
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

// A harness whose first check still runs is checking, never not installed; its result arrives with harness.checked,
// and a later check keeps the previous result until the new one lands.
func TestHarnessIsCheckingUntilItsCheckEnds(t *testing.T) {
	var release func()
	e := newEnv(t, func(e *env) { release = e.codex.hold(t) })
	sub, _, cancel := e.app.Subscribe(0)
	defer cancel()
	codex := func() ports.HarnessInfo {
		t.Helper()
		for _, h := range e.app.Harnesses() {
			if h.ID == domain.HarnessCodex {
				return h
			}
		}
		t.Fatal("codex is not listed")
		return ports.HarnessInfo{}
	}
	checked := func() domain.Event {
		t.Helper()
		timeout := time.After(10 * time.Second)
		for {
			select {
			case ev := <-sub:
				if ev.Type == domain.EventHarnessChecked && ev.Harness == domain.HarnessCodex {
					return ev
				}
			case <-timeout:
				t.Fatal("no harness.checked for codex")
			}
		}
	}

	if h := codex(); h.Readiness != "checking" || h.Detected || !h.LastCheckedAt.IsZero() || h.DiagnosticKey != "detection_running" {
		t.Errorf("while its first check runs: %+v", h)
	}
	release()
	ev := checked()
	if ev.Payload["readiness"] != "ready" || ev.Message != "" || ev.MessageKey != "" || ev.TaskID != "" {
		t.Errorf("harness.checked %+v", ev)
	}
	first := codex()
	if first.Readiness != "ready" || !first.Detected || first.LastCheckedAt.IsZero() {
		t.Errorf("once its check ended: %+v", first)
	}

	release = e.codex.hold(t)
	go e.app.RefreshHarnesses(context.Background())
	if !e.poll(10*time.Second, e.codex.holding) {
		t.Fatal("the second check did not start")
	}
	if h := codex(); h.Readiness != "ready" || !h.LastCheckedAt.Equal(first.LastCheckedAt) {
		t.Errorf("while a later check runs: %+v", h)
	}
	e.clock.Advance(time.Minute)
	release()
	checked()
	if h := codex(); h.Readiness != "ready" || !h.LastCheckedAt.After(first.LastCheckedAt) {
		t.Errorf("once the later check ended: %+v", h)
	}
}

// A rate limit retries on the same harness, never switches.
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

// Misfires coalesce into one catch-up, or are skipped.
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

// Firings missed while the daemon was down follow the task's missed-run policy once, after the restart: one catch-up
// for the first missed firing, or none. The next firing is the next occurrence, and another restart runs nothing.
func TestMisfiresAcrossRestart(t *testing.T) {
	e := newEnv(t)
	e.create("daily", "  schedule:\n    cron: \"0 9 * * *\"\n    timezone: UTC\n")
	e.create("daily-skip", "  schedule:\n    cron: \"0 9 * * *\"\n    timezone: UTC\n    missedRun:\n      policy: skip\n")
	e.app.Stop(domain.Text{Message: "test restart"}) // at 08:00, before the first firing
	e.clock.Advance(3 * 24 * time.Hour)              // the daemon was down for three days
	e = e.restart(t)
	e.app.Tick()
	// The start's own tick may have taken the firing and still be starting it.
	var runs []ExecutionSummary
	e.poll(10*time.Second, func() bool { runs = e.executions("daily"); return len(runs) > 0 })
	if len(runs) != 1 || runs[0].Trigger != domain.TriggerCatchUp {
		t.Fatalf("daily: %+v", runs)
	}
	x := e.wait(runs[0].ID, domain.StatusCompleted)
	if first := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC); x.ScheduledFor == nil || !x.ScheduledFor.Equal(first) {
		t.Errorf("catch-up scheduled for %v, want %v", x.ScheduledFor, first)
	}
	if n := len(e.executions("daily-skip")); n != 0 {
		t.Errorf("skip policy ran %d executions", n)
	}
	next := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	nextFires := func() {
		t.Helper()
		for _, id := range []string{"daily", "daily-skip"} {
			if view, _ := e.app.GetTask(id); view.NextFireAt == nil || !view.NextFireAt.Equal(next) {
				t.Errorf("%s's next firing is %v, want %v", id, view.NextFireAt, next)
			}
		}
	}
	nextFires()
	e = e.restart(t)
	e.app.Tick()
	if n := len(e.executions("daily")); n != 1 {
		t.Errorf("the second restart ran daily again: %d executions", n)
	}
	if n := len(e.executions("daily-skip")); n != 0 {
		t.Errorf("the second restart ran daily-skip: %d executions", n)
	}
	nextFires()
}

// A changed schedule takes effect at once, however it changes: an edit through the API, an edit of the task file, or a
// change of the time zone alone. The next firing follows the new schedule, and the old one never fires.
func TestChangedScheduleTakesEffect(t *testing.T) {
	bangkok, err := time.LoadLocation("Asia/Bangkok")
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		from, to string // the schedule block, before and after the change
		external bool   // the task file is edited, and the scan picks it up
		next     time.Time
	}{
		{"cron through the API", `cron: "0 18 * * *"`, `cron: "30 10 * * *"`, false, day.Add(10*time.Hour + 30*time.Minute)},
		{"cron in the task file", `cron: "0 18 * * *"`, `cron: "30 10 * * *"`, true, day.Add(10*time.Hour + 30*time.Minute)},
		// 18:00 in Bangkok, UTC+7 all year, is 11:00 UTC.
		{"time zone alone", "timezone: UTC", "timezone: Asia/Bangkok", false, time.Date(2026, 10, 20, 18, 0, 0, 0, bangkok)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.clock.Advance(2 * time.Hour) // 10:00 UTC
			e.create("moved", "  schedule:\n    cron: \"0 18 * * *\"\n    timezone: UTC\n")
			view, _ := e.app.GetTask("moved")
			if want := day.Add(18 * time.Hour); view.NextFireAt == nil || !view.NextFireAt.Equal(want) {
				t.Fatalf("next firing before the change %v, want %v", view.NextFireAt, want)
			}

			changed := strings.Replace(view.YAML, tc.from, tc.to, 1)
			if changed == view.YAML {
				t.Fatalf("the task file has no %q:\n%s", tc.from, view.YAML)
			}
			if tc.external {
				if err := os.WriteFile(view.Path, []byte(changed), 0o600); err != nil {
					t.Fatal(err)
				}
				e.app.scanTaskFiles(t.Context())
			} else if _, err := e.app.UpdateTask(t.Context(), "moved", TaskInput{YAML: changed, BaseRevision: view.Revision}); err != nil {
				t.Fatal(err)
			}
			view, _ = e.app.GetTask("moved")
			if view.NextFireAt == nil || !view.NextFireAt.Equal(tc.next) {
				t.Fatalf("next firing after the change %v, want %v", view.NextFireAt, tc.next)
			}

			// Within MisfireAfter of the new time, so the firing is on time, not a catch-up.
			e.clock.Advance(tc.next.Sub(e.clock.Now()) + 30*time.Second)
			e.app.Tick()
			runs := e.executions("moved")
			if len(runs) != 1 {
				t.Fatalf("%d executions after the new time, want 1: %+v", len(runs), runs)
			}
			x, err := e.app.GetExecution(runs[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			if x.Trigger != domain.TriggerSchedule || x.ScheduledFor == nil || !x.ScheduledFor.Equal(tc.next) {
				t.Errorf("execution trigger %s, scheduled for %v; want schedule at %v", x.Trigger, x.ScheduledFor, tc.next)
			}
			e.wait(x.ID, domain.StatusCompleted)
			e.idle("moved")

			// The old 18:00 UTC never comes again: the next firing is the new schedule's, a day later.
			view, _ = e.app.GetTask("moved")
			if want := tc.next.AddDate(0, 0, 1); view.NextFireAt == nil || !view.NextFireAt.Equal(want) {
				t.Errorf("next firing after it ran %v, want %v", view.NextFireAt, want)
			}
		})
	}
}

// A firing that came due while the daemon was down is a misfire however short the outage, here a restart 60 seconds
// after it, and follows the task's missed-run policy.
func TestMisfireAcrossShortRestart(t *testing.T) {
	e := newEnv(t)
	e.create("daily", "  schedule:\n    cron: \"0 9 * * *\"\n    timezone: UTC\n")
	e.create("daily-skip", "  schedule:\n    cron: \"0 9 * * *\"\n    timezone: UTC\n    missedRun:\n      policy: skip\n")
	e.clock.Advance(61 * time.Minute) // 09:01, a minute after the firing
	e = e.restart(t)
	e.app.Tick() // waits for the tick the start ran
	runs := e.executions("daily")
	if len(runs) != 1 || runs[0].Trigger != domain.TriggerCatchUp {
		t.Fatalf("daily: %+v", runs)
	}
	if x, _ := e.app.GetExecution(runs[0].ID); x.ScheduledFor == nil ||
		!x.ScheduledFor.Equal(time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("catch-up scheduled for %v", x.ScheduledFor)
	}
	if n := len(e.executions("daily-skip")); n != 0 {
		t.Errorf("skip policy ran %d executions", n)
	}
	policies := map[string]any{}
	for _, ev := range e.published(domain.EventFiringMissed) {
		policies[ev.TaskID] = ev.Payload["policy"]
	}
	if policies["daily"] != domain.MissedRunOnResume || policies["daily-skip"] != domain.MissedRunSkip {
		t.Errorf("firing.missed policies %v", policies)
	}
	e.wait(runs[0].ID, domain.StatusCompleted)
	// A firing that comes due while the daemon runs is on time.
	e.clock.Advance(24*time.Hour + 10*time.Second)
	e.app.Tick()
	if runs = e.executions("daily"); len(runs) != 2 || runs[0].Trigger != domain.TriggerSchedule {
		t.Fatalf("on-time firing: %+v", runs)
	}
	if runs = e.executions("daily-skip"); len(runs) != 1 || runs[0].Trigger != domain.TriggerSchedule {
		t.Fatalf("on-time firing of daily-skip: %+v", runs)
	}
}

// lastFiring is the latest record of the task's firing history.
func (e *env) lastFiring(id string) firingRecord {
	e.t.Helper()
	e.app.mu.Lock()
	defer e.app.mu.Unlock()
	h := e.app.firings[id]
	if len(h) == 0 {
		e.t.Fatalf("%s has no firing record", id)
	}
	return h[len(h)-1]
}

// The firings of a three-day outage are recorded, the catch-up with its execution and the skipped misfires with the
// policy, and the records survive a restart.
func TestFiringsRecordedAcrossRestart(t *testing.T) {
	e := newEnv(t)
	e.create("daily", "  schedule:\n    cron: \"0 9 * * *\"\n    timezone: UTC\n")
	e.create("daily-skip", "  schedule:\n    cron: \"0 9 * * *\"\n    timezone: UTC\n    missedRun:\n      policy: skip\n")
	e.clock.Advance(3 * 24 * time.Hour) // the daemon was down for three days
	e = e.restart(t)
	e.app.Tick()
	runs := e.executions("daily")
	if len(runs) != 1 {
		t.Fatalf("daily: %+v", runs)
	}
	e.wait(runs[0].ID, domain.StatusCompleted)
	e = e.restart(t)
	e.app.Tick()
	if n := len(e.executions("daily")); n != 1 {
		t.Errorf("the restart ran daily again: %d executions", n)
	}
	first := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	got := e.lastFiring("daily")
	want := firingRecord{ScheduledFor: first, ProcessedAt: e.clock.Now(), Trigger: domain.TriggerCatchUp, Missed: 3,
		Policy: domain.MissedRunOnResume, Outcome: "started", ExecutionID: runs[0].ID}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("daily's record:\n got %+v\nwant %+v", got, want)
	}
	got = e.lastFiring("daily-skip")
	if !got.ScheduledFor.Equal(first) || got.Trigger != domain.TriggerCatchUp || got.Missed != 3 ||
		got.Policy != domain.MissedRunSkip || got.Outcome != "skipped" || got.ExecutionID != "" ||
		got.ReasonKey != "firings_missed_skipped" {
		t.Errorf("daily-skip's record: %+v", got)
	}
}

// A firing due while its task is disabled, pending deletion, or invalid on disk is recorded as skipped, with the
// reason.
func TestHeldBackFiringsRecorded(t *testing.T) {
	e := newEnv(t)
	schedule := "  schedule:\n    cron: \"0 9 * * *\"\n    timezone: UTC\n"
	for _, id := range []string{"off", "going", "broken", "on"} {
		e.create(id, schedule)
	}
	if _, err := e.app.SetEnabled(t.Context(), "off", false); err != nil {
		t.Fatal(err)
	}
	e.claude.setScript(func(ports.RunRequest) string { return slow })
	r, _ := e.app.RunTask("going")
	e.wait(r.ExecutionID, domain.StatusRunning)
	e.prepared(e.claude)
	if pending, err := e.app.DeleteTask("going"); err != nil || !pending {
		t.Fatalf("delete: %v %v", pending, err)
	}
	view, _ := e.app.GetTask("broken")
	invalid := strings.Replace(view.YAML, "type: general", "type: nope", 1)
	if err := os.WriteFile(filepath.Join(e.paths.Tasks, "broken.yaml"), []byte(invalid), 0o600); err != nil {
		t.Fatal(err)
	}
	e.app.scanTaskFiles(t.Context())
	e.claude.setScript(nil)
	e.clock.Advance(61 * time.Minute)
	e.app.Tick()
	for id, key := range map[string]string{"off": "task_was_disabled", "going": "task_being_deleted",
		"broken": "task_file_invalid"} {
		got := e.lastFiring(id)
		if got.Outcome != "skipped" || got.ReasonKey != key || got.Trigger != domain.TriggerSchedule ||
			got.ExecutionID != "" || got.Reason == "" {
			t.Errorf("%s's record: %+v", id, got)
		}
	}
	if got := e.lastFiring("broken"); got.ReasonParams["file"] != "broken.yaml" {
		t.Errorf("the invalid file's record names %q", got.ReasonParams["file"])
	}
	// The task that could run fired on time, and its record names the execution.
	runs := e.executions("on")
	if got := e.lastFiring("on"); len(runs) != 1 || got.Outcome != "started" || got.ExecutionID != runs[0].ID ||
		got.Trigger != domain.TriggerSchedule || got.Policy != "" {
		t.Errorf("on's record: %+v, executions %+v", got, runs)
	}
	if err := e.app.CancelExecution(r.ExecutionID); err != nil {
		t.Fatal(err)
	}
}

// A firing due while the machine slept is a misfire however soon the machine wakes, here 60 seconds after it; a sleep
// that ended before a firing's time changes nothing.
func TestMisfireDuringSleep(t *testing.T) {
	e := newEnv(t)
	e.create("daily", "  schedule:\n    cron: \"0 9 * * *\"\n    timezone: UTC\n")
	e.create("daily-skip", "  schedule:\n    cron: \"0 9 * * *\"\n    timezone: UTC\n    missedRun:\n      policy: skip\n")
	e.clock.Advance(61 * time.Minute) // asleep across 09:00, awake at 09:01
	e.wake.woke(e.clock.Now())
	e.app.Tick()
	runs := e.executions("daily")
	if len(runs) != 1 || runs[0].Trigger != domain.TriggerCatchUp {
		t.Fatalf("daily: %+v", runs)
	}
	if n := len(e.executions("daily-skip")); n != 0 {
		t.Errorf("skip policy ran %d executions", n)
	}
	policies := map[string]any{}
	for _, ev := range e.published(domain.EventFiringMissed) {
		policies[ev.TaskID] = ev.Payload["policy"]
	}
	if policies["daily"] != domain.MissedRunOnResume || policies["daily-skip"] != domain.MissedRunSkip {
		t.Errorf("firing.missed policies %v", policies)
	}
	e.wait(runs[0].ID, domain.StatusCompleted)
	// Asleep again, awake at 08:59 the next day: the 09:00 firing is on time.
	e.clock.Advance(24*time.Hour - 2*time.Minute)
	e.wake.woke(e.clock.Now())
	e.app.Tick()
	e.clock.Advance(65 * time.Second)
	e.app.Tick()
	if runs = e.executions("daily"); len(runs) != 2 || runs[0].Trigger != domain.TriggerSchedule {
		t.Fatalf("on-time firing: %+v", runs)
	}
	if runs = e.executions("daily-skip"); len(runs) != 1 || runs[0].Trigger != domain.TriggerSchedule {
		t.Fatalf("on-time firing of daily-skip: %+v", runs)
	}
}

// A running execution keeps its definition; the waiting firing picks up the edit.
func TestEditWhileRunning(t *testing.T) {
	e := newEnv(t)
	e.create("edited", "")
	e.claude.setScript(func(ports.RunRequest) string { return "sleep 1; echo 'END ok'" })
	first, _ := e.app.RunTask("edited")
	e.wait(first.ExecutionID, domain.StatusRunning)
	second, _ := e.app.RunTask("edited")
	view, _ := e.app.GetTask("edited")
	edited := strings.Replace(view.YAML, "Do the work of edited.", "Do the edited work.", 1)
	if _, err := e.app.UpdateTask(t.Context(), "edited", TaskInput{YAML: edited, BaseRevision: view.Revision}); err != nil {
		t.Fatal(err)
	}
	e.wait(second.ExecutionID, domain.StatusCompleted)
	e.claude.mu.Lock()
	prompts := append([]string(nil), e.claude.prompts...)
	e.claude.mu.Unlock()
	if len(prompts) != 2 || prompts[0] != "Do the work of edited." || prompts[1] != "Do the edited work." {
		t.Errorf("prompts %q", prompts)
	}
	// An edit from a stale revision is a conflict, never a silent overwrite.
	if _, err := e.app.UpdateTask(t.Context(), "edited", TaskInput{YAML: edited, BaseRevision: view.Revision}); err == nil ||
		err.(*Error).Code != CodeConflict {
		t.Errorf("stale revision: %v", err)
	}
}

// What a running execution reads after an edit still comes from the definition it started with. The edit changes the
// prompt and removes the fallback while the first attempt runs; the quota that ends that attempt still switches to
// codex, and codex reads the prompt as it was. The next execution runs the edit.
func TestEditWhileRunningKeepsTheStartingDefinition(t *testing.T) {
	e := newEnv(t)
	gates := t.TempDir()
	e.claude.setScript(func(req ports.RunRequest) string {
		return fmt.Sprintf("while [ ! -e %s ]; do sleep 0.05; done; echo 'END quota'", filepath.Join(gates, req.TaskID))
	})
	e.codex.setScript(func(ports.RunRequest) string { return "echo 'END ok'" })
	e.create("late", "    fallback:\n      candidates:\n        - provider: codex\n")
	first, _ := e.app.RunTask("late")
	e.wait(first.ExecutionID, domain.StatusRunning)
	view, _ := e.app.GetTask("late")
	edited := strings.Replace(taskYAML("late", ""), "Do the work of late.", "Do the edited work of late.", 1)
	after, err := e.app.UpdateTask(t.Context(), "late", TaskInput{YAML: edited, BaseRevision: view.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision == view.Revision || strings.Contains(after.YAML, "fallback") ||
		!strings.Contains(after.YAML, "Do the edited work of late.") {
		t.Fatalf("the edit was not saved: revision %s, YAML %q", after.Revision, after.YAML)
	}
	if err := os.WriteFile(filepath.Join(gates, "late"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	x := e.wait(first.ExecutionID, domain.StatusCompleted)
	if len(x.Attempts) != 2 || x.Attempts[0].Outcome != domain.AttemptQuotaExhausted ||
		x.Attempts[1].Harness != domain.HarnessCodex || x.HarnessSwitches != 1 {
		t.Fatalf("the running execution did not fail over as it started: %+v", x.Attempts)
	}
	if x.Attempts[1].Prompt != "Do the work of late." {
		t.Errorf("the second attempt read the edited prompt: %q", x.Attempts[1].Prompt)
	}
	if x.DefinitionRevision != view.Revision {
		t.Errorf("the execution's revision is %s, want %s, the one it started with", x.DefinitionRevision, view.Revision)
	}
	e.codex.mu.Lock()
	codex := append([]string(nil), e.codex.prompts...)
	e.codex.mu.Unlock()
	if len(codex) != 1 || codex[0] != "Do the work of late." {
		t.Errorf("codex received %q, want the prompt the execution started with", codex)
	}

	e.idle("late")
	next, _ := e.app.RunTask("late")
	y := e.wait(next.ExecutionID, domain.StatusFailed)
	if y.ErrorKey != "no_alternative_harness_detail" || len(y.Attempts) != 1 ||
		y.Attempts[0].Prompt != "Do the edited work of late." || y.DefinitionRevision != after.Revision {
		t.Errorf("the next execution did not run the edit: %s (%s), revision %s, attempts %+v", y.Status, y.ErrorKey,
			y.DefinitionRevision, y.Attempts)
	}
	e.codex.mu.Lock()
	switched := len(e.codex.prompts)
	e.codex.mu.Unlock()
	if switched != 1 {
		t.Errorf("codex was prepared %d times, want 1: the next execution kept the removed fallback", switched)
	}
}

// An edit that fails validation while an execution runs changes nothing. The task keeps its file and revision, the
// execution ends with the definition it started with, and so does the next one.
func TestInvalidEditWhileRunning(t *testing.T) {
	e := newEnv(t)
	gates := t.TempDir()
	e.claude.setScript(func(req ports.RunRequest) string {
		return fmt.Sprintf("while [ ! -e %s ]; do sleep 0.05; done; echo 'END ok'", filepath.Join(gates, req.TaskID))
	})
	e.create("kept", "")
	first, _ := e.app.RunTask("kept")
	e.wait(first.ExecutionID, domain.StatusRunning)
	view, _ := e.app.GetTask("kept")
	// The edit would change the prompt too, so a partial save would show in what the executions read.
	invalid := strings.NewReplacer("Do the work of kept.", "Do the edited work of kept.", "type: general", "type: nope").
		Replace(view.YAML)
	_, err := e.app.UpdateTask(t.Context(), "kept", TaskInput{YAML: invalid, BaseRevision: view.Revision})
	if appErr, ok := err.(*Error); !ok || appErr.Code != CodeValidationFailed {
		t.Fatalf("invalid edit: %v, want %s", err, CodeValidationFailed)
	}
	after, _ := e.app.GetTask("kept")
	onDisk, err := os.ReadFile(view.Path)
	if err != nil {
		t.Fatal(err)
	}
	if after.YAML != view.YAML || after.Revision != view.Revision || string(onDisk) != view.YAML {
		t.Errorf("the invalid edit changed the task: revision %s, want %s; YAML %q; on disk %q", after.Revision,
			view.Revision, after.YAML, onDisk)
	}
	if err := os.WriteFile(filepath.Join(gates, "kept"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	x := e.wait(first.ExecutionID, domain.StatusCompleted)
	e.idle("kept")
	next, _ := e.app.RunTask("kept")
	y := e.wait(next.ExecutionID, domain.StatusCompleted)
	for _, run := range []ExecutionView{x, y} {
		if len(run.Attempts) != 1 || run.Attempts[0].Prompt != "Do the work of kept." ||
			run.DefinitionRevision != view.Revision {
			t.Errorf("execution %s did not run the valid definition: revision %s, want %s; attempts %+v", run.ID,
				run.DefinitionRevision, view.Revision, run.Attempts)
		}
	}
}

// Disabling lets the execution finish; deleting waits for it; records stay.
func TestDisableAndDeleteWhileRunning(t *testing.T) {
	e := newEnv(t)
	e.create("going", "")
	e.claude.setScript(func(ports.RunRequest) string { return "sleep 1; echo 'END ok'" })
	r, _ := e.app.RunTask("going")
	e.wait(r.ExecutionID, domain.StatusRunning)
	if _, err := e.app.SetEnabled(t.Context(), "going", false); err != nil {
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

// Disabling a running task lets its execution finish and stops every new firing until the task is enabled: the firing
// waiting behind it is cancelled and never starts, and a firing that comes due while it is disabled creates no
// execution. Once enabled, the next firing is on time, not a catch-up.
func TestDisableStopsNewFiringsUntilEnabled(t *testing.T) {
	e := newEnv(t)
	gates := t.TempDir()
	e.claude.setScript(func(req ports.RunRequest) string {
		return fmt.Sprintf("while [ ! -e %s ]; do sleep 0.05; done; echo 'END ok'", filepath.Join(gates, req.TaskID))
	})
	e.create("paused", "  schedule:\n    interval: 30m\n")
	// tickAtNext moves the clock just past the task's next firing, within the misfire window, and ticks.
	tickAtNext := func(next time.Time) {
		e.clock.Advance(next.Sub(e.clock.Now()) + 10*time.Second)
		e.app.Tick()
	}
	view, _ := e.app.GetTask("paused")
	tickAtNext(*view.NextFireAt)
	runs := e.executions("paused")
	if len(runs) != 1 || runs[0].Trigger != domain.TriggerSchedule {
		t.Fatalf("first firing: %+v", runs)
	}
	running := runs[0].ID
	e.wait(running, domain.StatusRunning)
	view, _ = e.app.GetTask("paused")
	tickAtNext(*view.NextFireAt)
	runs = e.executions("paused")
	if len(runs) != 2 || runs[0].Status != domain.StatusQueued {
		t.Fatalf("the second firing does not wait: %+v", runs)
	}
	waiting := runs[0].ID
	view, _ = e.app.GetTask("paused")
	due := *view.NextFireAt // the view shows no next firing once the task is disabled

	if _, err := e.app.SetEnabled(t.Context(), "paused", false); err != nil {
		t.Fatal(err)
	}
	if x, _ := e.app.GetExecution(waiting); x.Status != domain.StatusCancelled || x.ErrorKey != "task_was_disabled" {
		t.Errorf("the waiting execution is %s (%q), want cancelled (task_was_disabled)", x.Status, x.ErrorKey)
	}
	if err := os.WriteFile(filepath.Join(gates, "paused"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	e.wait(running, domain.StatusCompleted)
	e.idle("paused")
	if x, _ := e.app.GetExecution(waiting); x.Status != domain.StatusCancelled || x.StartedAt != nil ||
		len(x.Attempts) != 0 {
		t.Errorf("the dropped firing started: %s, started at %v, %d attempts", x.Status, x.StartedAt, len(x.Attempts))
	}
	e.claude.mu.Lock()
	prompts := len(e.claude.prompts)
	e.claude.mu.Unlock()
	if prompts != 1 {
		t.Errorf("the harness was prepared %d times, want 1: the dropped firing started", prompts)
	}

	tickAtNext(due)
	if runs := e.executions("paused"); len(runs) != 2 {
		t.Fatalf("a disabled task fired: %+v", runs)
	}

	if _, err := e.app.SetEnabled(t.Context(), "paused", true); err != nil {
		t.Fatal(err)
	}
	view, _ = e.app.GetTask("paused")
	if view.NextFireAt == nil || !view.NextFireAt.After(e.clock.Now()) {
		t.Fatalf("next firing once enabled: %v, now %v", view.NextFireAt, e.clock.Now())
	}
	next := *view.NextFireAt
	tickAtNext(next)
	runs = e.executions("paused")
	if len(runs) != 3 || runs[0].ID == running || runs[0].ID == waiting {
		t.Fatalf("the enabled task did not fire: %+v", runs)
	}
	x := e.wait(runs[0].ID, domain.StatusCompleted)
	if x.Trigger != domain.TriggerSchedule || x.ScheduledFor == nil || !x.ScheduledFor.Equal(next) {
		t.Errorf("the firing once enabled is %s for %v, want %s for %v", x.Trigger, x.ScheduledFor,
			domain.TriggerSchedule, next)
	}
}

// endedRun runs the task to the status it wants and returns the execution once the task is free again: from then on
// nothing writes to the execution's log, so its age stays what the test sets.
func (e *env) endedRun(task string, want domain.ExecutionStatus) ExecutionView {
	e.t.Helper()
	r, err := e.app.RunTask(task)
	if err != nil || r.Outcome != "started" {
		e.t.Fatalf("run %s: %v %+v", task, err, r)
	}
	e.wait(r.ExecutionID, want)
	e.idle(task)
	x, _ := e.app.GetExecution(r.ExecutionID)
	return x
}

// backdateLog sets the modification time of the execution's log to age before the app's clock. Retention reads a
// log's age from that time, and its cutoff from the clock, which these tests never advance.
func (e *env) backdateLog(id string, age time.Duration) time.Time {
	e.t.Helper()
	at := e.clock.Now().Add(-age)
	if err := os.Chtimes(filepath.Join(e.app.Paths().Logs, id+".jsonl"), at, at); err != nil {
		e.t.Fatal(err)
	}
	return at
}

// retentionFiles are the files retention deletes with an execution's log: the log, its raw captures under
// logs/<execution>/, and runs/<execution>/, directories included.
func (e *env) retentionFiles(id string) []string {
	p := e.app.Paths()
	files := []string{filepath.Join(p.Logs, id+".jsonl")}
	for _, dir := range []string{filepath.Join(p.Logs, id), filepath.Join(p.Runs, id)} {
		_ = filepath.WalkDir(dir, func(path string, _ os.DirEntry, err error) error {
			if err == nil {
				files = append(files, path)
			}
			return nil
		})
	}
	return files
}

// presentFiles are the files that still exist.
func presentFiles(files []string) []string {
	var out []string
	for _, f := range files {
		if _, err := os.Stat(f); err == nil {
			out = append(out, f)
		}
	}
	return out
}

// With logRetentionDays at 7, retention deletes the log, the raw captures, and the execution directory of an ended
// execution whose log is 8 days old, and keeps every file of one whose log is 6 days old. The expired execution's
// record is still listed and read whole; only its log reads as expired.
func TestRetentionDeletesOnlyExpiredLogs(t *testing.T) {
	const day = 24 * time.Hour
	e := newEnv(t)
	week := 7
	if _, err := e.app.UpdateSettings(SettingsPatch{LogRetentionDays: &week}); err != nil {
		t.Fatal(err)
	}
	e.create("aged", "    fallback:\n      candidates:\n        - provider: codex\n")
	// The execution that expires fails over, so its record keeps two attempts; the second reports a pull request.
	e.claude.setScript(func(ports.RunRequest) string { return "echo 'END quota'" })
	e.codex.setScript(func(req ports.RunRequest) string {
		report := `{"outcome":"completed","summary":"opened a draft","workItems":[{"key":"o/r#7","outcome":"completed",` +
			`"pullRequest":{"url":"https://github.com/o/r/pull/9","created":true}}]}`
		return fmt.Sprintf(`printf '%%s' '%s' > %s/report.json; echo 'END ok'`, report, req.ExecutionDir)
	})
	old := e.endedRun("aged", domain.StatusCompleted)
	e.claude.setScript(func(ports.RunRequest) string { return "echo 'END ok'" })
	recent := e.endedRun("aged", domain.StatusCompleted)

	p := e.app.Paths()
	for _, f := range []string{filepath.Join(p.Logs, old.ID, "attempt-1.raw"), filepath.Join(p.Logs, old.ID, "attempt-2.raw"),
		filepath.Join(p.Runs, old.ID), filepath.Join(p.Logs, recent.ID, "attempt-1.raw"), filepath.Join(p.Runs, recent.ID)} {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("before retention: %v", err)
		}
	}
	oldFiles, recentFiles := e.retentionFiles(old.ID), e.retentionFiles(recent.ID)
	e.backdateLog(old.ID, 8*day)
	e.backdateLog(recent.ID, 6*day)
	e.app.ApplyRetention()
	if left := presentFiles(oldFiles); len(left) != 0 {
		t.Errorf("retention left files of the execution whose log is 8 days old: %v", left)
	}
	if left := presentFiles(recentFiles); len(left) != len(recentFiles) {
		t.Errorf("retention deleted files of the execution whose log is 6 days old: %d of %d left, %v", len(left),
			len(recentFiles), left)
	}

	listed := false
	for _, s := range e.executions("aged") {
		if s.ID == old.ID {
			listed = s.Status == domain.StatusCompleted
		}
	}
	if !listed {
		t.Errorf("the execution whose log expired is not listed for its task as completed: %+v", e.executions("aged"))
	}
	x, err := e.app.GetExecution(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !x.LogExpired || x.Status != domain.StatusCompleted || x.WorkItem != "o/r#7" || len(x.WorkItems) != 1 ||
		x.WorkItems[0].Key != "o/r#7" || x.PullRequestURL != "https://github.com/o/r/pull/9" {
		t.Errorf("the record lost what outlives an expired log: log expired %v, %+v, work items %+v", x.LogExpired,
			x.ExecutionSummary, x.WorkItems)
	}
	if len(x.Attempts) != 2 || x.Attempts[0].Harness != domain.HarnessClaudeCode ||
		x.Attempts[0].Outcome != domain.AttemptQuotaExhausted || x.Attempts[1].Harness != domain.HarnessCodex ||
		x.Attempts[1].Outcome != domain.AttemptSucceeded {
		t.Errorf("the record lost its attempts: %+v", x.Attempts)
	}
	// The events read as expired because the log is gone, the output because the record says so.
	if events, expired, err := e.app.ExecutionEvents(old.ID, 0, 0); err != nil || !expired || len(events) != 0 {
		t.Errorf("the expired execution's events: %d, expired %v, %v", len(events), expired, err)
	}
	out, err := e.app.ExecutionOutput(old.ID, 0)
	if err != nil || !out.LogExpired {
		t.Errorf("the expired execution's output: logExpired %v, %v", out.LogExpired, err)
	}
	for _, at := range out.Attempts {
		if at.Text != "" {
			t.Errorf("attempt %d still serves its raw capture: %q", at.Attempt, at.Text)
		}
	}
	if x, _ := e.app.GetExecution(recent.ID); x.LogExpired {
		t.Error("the execution whose log is 6 days old reads as expired")
	}
	if events, expired, _ := e.app.ExecutionEvents(recent.ID, 0, 0); expired || len(events) == 0 {
		t.Errorf("the execution whose log is 6 days old: %d events, expired %v", len(events), expired)
	}
	// The record was saved as expired, so the next start reads it the same way.
	e = e.restart(t)
	if x, _ := e.app.GetExecution(old.ID); !x.LogExpired || x.PullRequestURL == "" || len(x.Attempts) != 2 {
		t.Errorf("after a restart, the expired record: log expired %v, %+v", x.LogExpired, x.ExecutionSummary)
	}
}

// logRetentionDays is 30 on a fresh install, so a 29-day-old log is kept and a 31-day-old one deleted; it accepts 1 to
// 3650 and rejects 0 and 3651; and a saved value is still in force after a restart.
func TestLogRetentionSetting(t *testing.T) {
	const day = 24 * time.Hour
	e := newEnv(t)
	if got := e.app.GetSettings().LogRetentionDays; got != 30 {
		t.Fatalf("a fresh app reads logRetentionDays %d, want 30", got)
	}
	e.create("aged", "")
	ids := map[int]string{}
	for _, age := range []int{27, 29, 31} {
		ids[age] = e.endedRun("aged", domain.StatusCompleted).ID
	}
	for age, id := range ids {
		e.backdateLog(id, time.Duration(age)*day)
	}
	expiredOver := func(e *env, retention int) {
		t.Helper()
		for age, id := range ids {
			x, _ := e.app.GetExecution(id)
			_, err := os.Stat(filepath.Join(e.app.Paths().Logs, id+".jsonl"))
			if want := age > retention; x.LogExpired != want || os.IsNotExist(err) != want {
				t.Errorf("at %d days, the %d-day-old log: expired %v, file %v; want expired %v", retention, age,
					x.LogExpired, err, want)
			}
		}
	}
	e.app.ApplyRetention()
	expiredOver(e, 30)

	for _, bad := range []int{0, 3651} {
		if _, err := e.app.UpdateSettings(SettingsPatch{LogRetentionDays: &bad}); err == nil {
			t.Errorf("logRetentionDays %d was accepted", bad)
		} else if ae, ok := err.(*Error); !ok || ae.Code != CodeValidationFailed {
			t.Errorf("logRetentionDays %d: %v, want %s", bad, err, CodeValidationFailed)
		}
	}
	if got := e.app.GetSettings().LogRetentionDays; got != 30 {
		t.Errorf("a rejected value changed logRetentionDays to %d", got)
	}
	for _, good := range []int{1, 3650, 28} {
		if v, err := e.app.UpdateSettings(SettingsPatch{LogRetentionDays: &good}); err != nil || v.LogRetentionDays != good {
			t.Errorf("logRetentionDays %d: %v, reads %d", good, err, v.LogRetentionDays)
		}
	}
	// 28 days, saved, is what the next start applies: the 29-day-old log, which 30 days kept, expires.
	e = e.restart(t)
	if got := e.app.GetSettings().LogRetentionDays; got != 28 {
		t.Fatalf("after a restart, logRetentionDays is %d, want 28", got)
	}
	e.app.ApplyRetention()
	expiredOver(e, 28)
}

// A retention pass that finds another one deleting waits for it, so once ApplyRetention returns, every log past the
// cutoff is deleted and its record says so. A pass that went on beside the other would skip the log the other had just
// deleted, since a missing log is aged by its execution's end, and return before that log's record read expired. That
// is what the start's pass and TestLogRetentionSetting's own call after its restart could do.
func TestRetentionPassesRunOneAtATime(t *testing.T) {
	e := newEnv(t)
	e.create("aged", "")
	id := e.endedRun("aged", domain.StatusCompleted).ID
	e.backdateLog(id, 31*24*time.Hour)
	deleted, release := make(chan struct{}), make(chan struct{})
	var hold sync.Once
	e.events.setDeleteHook(func(string) {
		hold.Do(func() {
			close(deleted)
			<-release
		})
	})
	first := make(chan struct{})
	go func() {
		defer close(first)
		e.app.ApplyRetention()
	}()
	select {
	case <-deleted:
	case <-time.After(10 * time.Second):
		t.Fatal("the first retention pass deleted no log")
	}
	// The first pass is held right after it deleted the log, before its record says so.
	second := make(chan bool)
	go func() {
		e.app.ApplyRetention()
		x, _ := e.app.GetExecution(id)
		second <- x.LogExpired
	}()
	// A second pass that does not wait returns at once. One that waits is still waiting when the first is let go; the
	// wait only bounds how long a pass that did not wait has to show it.
	select {
	case expired := <-second:
		t.Errorf("a second retention pass returned while the first was still deleting; the log's record read expired %v",
			expired)
		close(release)
	case <-time.After(200 * time.Millisecond):
		close(release)
		if expired := <-second; !expired {
			t.Error("once a second retention pass returned, the deleted log's record did not read expired")
		}
	}
	<-first
}

// Retention never deletes the log of an execution still running or queued, however old its file is.
func TestRetentionKeepsTheLogsOfActiveExecutions(t *testing.T) {
	const day = 24 * time.Hour
	e := newEnv(t)
	one := 1
	if _, err := e.app.UpdateSettings(SettingsPatch{LogRetentionDays: &one}); err != nil {
		t.Fatal(err)
	}
	e.create("busy", "")
	e.claude.setScript(func(ports.RunRequest) string { return slow })
	running, _ := e.app.RunTask("busy")
	e.wait(running.ExecutionID, domain.StatusRunning)
	queued, _ := e.app.RunTask("busy")
	if queued.Outcome != "queued" {
		t.Fatalf("second run: %+v", queued)
	}
	// The harness prints one line and sleeps: from then on nothing writes to either log.
	if !e.poll(10*time.Second, func() bool {
		events, _, _ := e.app.ExecutionEvents(running.ExecutionID, 0, 0)
		return slices.ContainsFunc(events, func(ev domain.Event) bool {
			return ev.Type == domain.EventHarnessMessage && ev.Message == "working"
		})
	}) {
		t.Fatal("the harness printed nothing")
	}
	aged := map[string]time.Time{}
	for _, id := range []string{running.ExecutionID, queued.ExecutionID} {
		aged[id] = e.backdateLog(id, 3650*day)
	}
	e.app.ApplyRetention()
	for id, at := range aged {
		x, _ := e.app.GetExecution(id)
		switch mod, ok := e.events.LogModTime(id); {
		case !ok || x.LogExpired:
			t.Errorf("retention deleted the log of the %s execution: file %v, expired %v", x.Status, ok, x.LogExpired)
		case !mod.Equal(at):
			t.Errorf("the %s execution's log was written during retention, so its age was not tested", x.Status)
		}
	}
	e.claude.setScript(func(ports.RunRequest) string { return "echo 'END ok'" })
	_ = e.app.CancelExecution(queued.ExecutionID)
	_ = e.app.CancelExecution(running.ExecutionID)
	e.wait(running.ExecutionID, domain.StatusCancelled)
	e.idle("busy")
}

// A running task disabled or deleted by a change of its file drops its waiting firing, as one disabled or deleted
// through the API does, and the firing never starts.
func TestExternalChangeDropsTheWaitingFiring(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, path string)
		reason string
	}{
		{"disabled", disableTaskFile, "task_was_disabled"},
		{"deleted", removeTaskFile, "task_file_removed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.create("changed", "")
			e.claude.setScript(func(ports.RunRequest) string { return slow })
			first, _ := e.app.RunTask("changed")
			e.wait(first.ExecutionID, domain.StatusRunning)
			e.prepared(e.claude)
			second, _ := e.app.RunTask("changed")
			if second.Outcome != "queued" {
				t.Fatalf("second run: %+v", second)
			}
			view, _ := e.app.GetTask("changed")
			tc.change(t, view.Path)
			e.app.scanTaskFiles(t.Context())
			if x, _ := e.app.GetExecution(second.ExecutionID); x.Status != domain.StatusCancelled || x.ErrorKey != tc.reason {
				t.Errorf("the waiting execution is %s (%q), want cancelled (%s)", x.Status, x.ErrorKey, tc.reason)
			}
			e.claude.setScript(func(ports.RunRequest) string { return "echo 'END ok'" })
			_ = e.app.CancelExecution(first.ExecutionID)
			e.wait(first.ExecutionID, domain.StatusCancelled)
			e.idle("changed")
			e.claude.mu.Lock()
			prompts := len(e.claude.prompts)
			e.claude.mu.Unlock()
			if prompts != 1 {
				t.Errorf("the harness was prepared %d times, want 1: the dropped firing started", prompts)
			}
		})
	}
}

func disableTaskFile(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err == nil {
		data, err = setYAMLField(data, "spec", "enabled", false)
	}
	if err == nil {
		err = os.WriteFile(path, data, 0o600)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func removeTaskFile(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

// A firing waiting when the daemon stopped still waits after the restart, then starts; when its task file was disabled
// or deleted while the daemon was down, it is cancelled instead, never starts, and leaves the queue.
func TestWaitingFiringAcrossRestart(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, path string)
		want   domain.ExecutionStatus
		reason string
	}{
		{"kept", func(*testing.T, string) {}, domain.StatusCompleted, ""},
		{"disabled", disableTaskFile, domain.StatusCancelled, "task_was_disabled"},
		{"deleted", removeTaskFile, domain.StatusCancelled, "task_file_removed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.create("waits", "")
			e.claude.setScript(func(ports.RunRequest) string { return slow })
			first, _ := e.app.RunTask("waits")
			e.wait(first.ExecutionID, domain.StatusRunning)
			e.prepared(e.claude)
			second, _ := e.app.RunTask("waits")
			if second.Outcome != "queued" {
				t.Fatalf("second run: %+v", second)
			}
			e.claude.setScript(func(ports.RunRequest) string { return "echo 'END ok'" })
			view, _ := e.app.GetTask("waits")
			// The change is made while no daemon runs, so only the start can see it.
			e.app.Stop(domain.Text{Message: "test restart"})
			tc.change(t, view.Path)
			e = e.restart(t)
			if x, _ := e.app.GetExecution(first.ExecutionID); x.Status != domain.StatusInterrupted {
				t.Errorf("the running execution is %s, want interrupted", x.Status)
			}
			x := e.wait(second.ExecutionID, tc.want)
			if x.ErrorKey != tc.reason {
				t.Errorf("the waiting execution's reason is %q, want %q", x.ErrorKey, tc.reason)
			}
			if tc.want == domain.StatusCancelled && (x.StartedAt != nil || len(x.Attempts) != 0) {
				t.Errorf("the dropped firing started: started at %v, %d attempts", x.StartedAt, len(x.Attempts))
			}
			if q := e.app.Queue(); len(q.Waiting) != 0 {
				t.Errorf("the queue still lists %d waiting firings", len(q.Waiting))
			}
		})
	}
}

// A harness the daemon's stop ends is interrupted, even when it exits as one that finished. The stand-in answers the
// stop's SIGTERM with its success line and code 0, as a shell whose child the signal ended first may go on to do.
func TestStopInterruptsAHarnessThatExitsAsFinished(t *testing.T) {
	stopHarness(t, `exec perl -e '$| = 1; $SIG{TERM} = sub { print "END ok\n"; exit 0 }; print "working\n"; sleep 30'`, 0)
}

// A harness the daemon's stop ends with its signal is interrupted by the stop, not failed by its exit code.
func TestStopInterruptsAHarnessTheSignalEnds(t *testing.T) {
	stopHarness(t, `exec perl -e '$| = 1; print "working\n"; sleep 30'`, 128+int(syscall.SIGTERM))
}

// stopHarness runs a task whose harness runs script, and stops the daemon once the harness has printed "working", by
// then ready for the stop. Before and after a restart, the execution and its one attempt read interrupted, the attempt
// with the daemon's stop as its error and the exit code it ended with, and so does the attempt's harness.exited event.
func stopHarness(t *testing.T, script string, exitCode int) {
	t.Helper()
	e := newEnv(t)
	e.create("stopped", "")
	e.claude.setScript(func(ports.RunRequest) string { return script })
	r, _ := e.app.RunTask("stopped")
	if !e.poll(10*time.Second, func() bool {
		events, _, _ := e.app.ExecutionEvents(r.ExecutionID, 0, 0)
		return slices.ContainsFunc(events, func(ev domain.Event) bool {
			return ev.Type == domain.EventHarnessMessage && ev.Message == "working"
		})
	}) {
		t.Fatal("the harness printed nothing")
	}
	e.app.Stop(domain.Text{Message: "test stop"})
	check := func(when string, e *env) {
		t.Helper()
		x, _ := e.app.GetExecution(r.ExecutionID)
		if x.Status != domain.StatusInterrupted {
			t.Errorf("%s, the stopped execution is %s, want interrupted", when, x.Status)
		}
		if len(x.Attempts) != 1 {
			t.Fatalf("%s, attempts %+v, want one", when, x.Attempts)
		}
		at := x.Attempts[0]
		if at.Outcome != domain.AttemptInterrupted || at.ErrorKey != "daemon_stopped_execution" {
			t.Errorf("%s, the attempt reads %s (%s: %q), want interrupted (daemon_stopped_execution)", when, at.Outcome,
				at.ErrorKey, at.ErrorMessage)
		}
		if at.ExitCode == nil {
			t.Errorf("%s, the attempt recorded no exit code, want %d", when, exitCode)
		} else if *at.ExitCode != exitCode {
			t.Errorf("%s, the attempt's exit code is %d, want %d", when, *at.ExitCode, exitCode)
		}
		events, _, err := e.app.ExecutionEvents(r.ExecutionID, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		var exited []map[string]any
		for _, ev := range events {
			if ev.Type == domain.EventHarnessExited {
				exited = append(exited, ev.Payload)
			}
		}
		if len(exited) != 1 || exited[0]["outcome"] != string(domain.AttemptInterrupted) ||
			exited[0]["errorKey"] != "daemon_stopped_execution" {
			t.Errorf("%s, the harness.exited payloads are %v, want one with outcome interrupted and errorKey "+
				"daemon_stopped_execution", when, exited)
		}
	}
	check("after the stop", e)
	check("after the restart", e.restart(t))
}

// A harness that exits on its own keeps the outcome its exit gives, though the daemon begins stopping while the process
// it left running, which ignores SIGTERM, is ended over the grace period.
func TestHarnessExitKeepsItsOutcomeThroughAStop(t *testing.T) {
	e := newEnv(t)
	e.create("own-exit", "  execution:\n    cancelGraceSeconds: 2\n")
	dir := t.TempDir()
	e.claude.setScript(func(ports.RunRequest) string {
		return fmt.Sprintf("(trap '' TERM; exec sleep 600) >/dev/null 2>&1 & echo $! > %[1]s/child; "+
			"echo $$ > %[1]s/harness; echo 'END ok'", dir)
	})
	r, err := e.app.RunTask("own-exit")
	if err != nil || r.Outcome != "started" {
		t.Fatalf("run own-exit: %v %+v", err, r)
	}
	waitForPid(t, filepath.Join(dir, "child"))
	waitReaped(t, waitForPid(t, filepath.Join(dir, "harness")))
	e.app.Stop(domain.Text{Message: "test stop"})
	x, _ := e.app.GetExecution(r.ExecutionID)
	if len(x.Attempts) != 1 || x.Attempts[0].Outcome != domain.AttemptSucceeded || x.Attempts[0].ErrorKey != "" {
		t.Errorf("attempts %+v, want one that succeeded", x.Attempts)
	}
	if x.Status != domain.StatusCompleted {
		t.Errorf("the execution is %s, want completed", x.Status)
	}
}

// Autostart is opt-in. Starting the app, a patch that leaves autostart out, and completing onboarding neither turn it
// on nor off; only a patch that names it does, and turning it on starts this daemon with its data directory.
func TestAutostartChangesOnlyOnRequest(t *testing.T) {
	service := &fakeAutostart{}
	exe := "/Applications/HarmFlow.app/Contents/MacOS/harmflowd"
	args := []string{"--home", "/data", "--builtins", "/builtins"}
	daemonEnv := map[string]string{"PATH": "/usr/bin:/bin"}
	e := newEnv(t, func(e *env) { e.service = service })
	// What autostart launches is read only when it is turned on.
	e.app.d.DaemonExecutable, e.app.d.DaemonArgs, e.app.d.DaemonEnv = exe, args, daemonEnv
	calls := func(step string, enables, disables int) {
		t.Helper()
		if len(service.enables) != enables || service.disables != disables {
			t.Errorf("%s: Enable called %d times and Disable %d, want %d and %d", step, len(service.enables),
				service.disables, enables, disables)
		}
	}
	patch := func(step string, p SettingsPatch, enabled bool) {
		t.Helper()
		v, err := e.app.UpdateSettings(p)
		if err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		if v.Autostart.Enabled != enabled || v.Autostart.Mechanism != "launchd" {
			t.Errorf("%s: autostart reads %+v, want enabled %v", step, v.Autostart, enabled)
		}
	}

	calls("starting the app", 0, 0)
	if v := e.app.GetSettings(); v.Autostart.Enabled || v.Autostart.Mechanism != "launchd" {
		t.Errorf("a fresh install reads autostart %+v", v.Autostart)
	}
	language, level := "en-US", "none"
	patch("a patch without autostart", SettingsPatch{Language: &language, NotificationLevel: &level}, false)
	calls("a patch without autostart", 0, 0)
	completed := true
	patch("completing onboarding", SettingsPatch{OnboardingCompleted: &completed}, false)
	calls("completing onboarding", 0, 0)

	on, off := true, false
	patch("autostart: true", SettingsPatch{Autostart: &on}, true)
	calls("autostart: true", 1, 0)
	for _, got := range service.enables {
		if got.executable != exe || !slices.Equal(got.args, args) || fmt.Sprint(got.env) != fmt.Sprint(daemonEnv) {
			t.Errorf("Enable starts %s %q with %v, want %s %q with %v", got.executable, got.args, got.env, exe, args,
				daemonEnv)
		}
	}
	patch("autostart: false", SettingsPatch{Autostart: &off}, false)
	calls("autostart: false", 1, 1)
}

// An invalid change keeps the valid file; a missing skill is an error on save and a warning on import.
func TestValidationAndImport(t *testing.T) {
	e := newEnv(t)
	e.create("valid", "")
	view, _ := e.app.GetTask("valid")
	_, err := e.app.UpdateTask(t.Context(), "valid", TaskInput{YAML: strings.Replace(view.YAML, "type: general", "type: nope", 1),
		BaseRevision: view.Revision})
	if err == nil || err.(*Error).Code != CodeValidationFailed {
		t.Fatalf("invalid edit: %v", err)
	}
	after, _ := e.app.GetTask("valid")
	if after.YAML != view.YAML {
		t.Error("the valid definition was replaced")
	}
	skills := "  skills: [hf-github-draft-pr, user-skill, nowhere-skill]\n"
	_, err = e.app.CreateTask(t.Context(), TaskInput{YAML: taskYAML("skilled", skills)})
	if err == nil || !strings.Contains(err.Error(), "nowhere-skill") {
		t.Fatalf("missing skill on save: %v", err)
	}
	imported, err := e.app.ImportTask(t.Context(), taskYAML("skilled", skills), false)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	warned := false
	for _, w := range imported.Validation.Warnings {
		if strings.Contains(w.Message, "nowhere-skill") {
			warned = true
		}
		if strings.Contains(w.Message, "user-skill") || strings.Contains(w.Message, "hf-github-draft-pr") {
			t.Errorf("a resolvable skill was reported missing: %s", w.Message)
		}
	}
	if !warned {
		t.Error("import did not warn about the missing skill")
	}
}

// A saved task keeps its type. A duplicate inherits it, and an update that changes it is refused at the type and writes
// nothing, from the form's document and from the YAML editor alike. The file is the source of truth: a direct edit
// that changes the type loads, and the task keeps that type from then on.
func TestUpdateKeepsTheType(t *testing.T) {
	e := newEnv(t)
	repo := mkdir(t, filepath.Join(e.app.Paths().UserHome, "repo"))
	if _, err := e.app.CreateTask(t.Context(), TaskInput{YAML: issueTask("hub", repo, "")}); err != nil {
		t.Fatal(err)
	}
	if cp, err := e.app.DuplicateTask(t.Context(), "hub"); err != nil || cp.Type != domain.TypeGitHub {
		t.Errorf("DuplicateTask(hub) = %q, %v; want a %s task", cp.Type, err, domain.TypeGitHub)
	}
	view, _ := e.app.GetTask("hub")
	general := taskYAML("hub", "")
	document, err := json.Marshal(map[string]any{"apiVersion": "harmflow/v1alpha1", "kind": "Task",
		"metadata": map[string]any{"id": "hub", "name": "Task hub"},
		"spec": map[string]any{"type": domain.TypeGeneral, "prompt": "Do the work of hub.",
			"harness": map[string]any{"preferred": map[string]any{"provider": domain.HarnessClaudeCode}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []TaskInput{{Document: document}, {YAML: general}} {
		if r := e.app.Validate(t.Context(), in); !r.Valid {
			t.Fatalf("Validate(the general task) errors = %+v, want none: only its type may fail the update", r.Errors)
		}
	}
	for _, c := range []struct {
		name string
		in   TaskInput
	}{
		{"the form's document", TaskInput{Document: document, BaseRevision: view.Revision}},
		{"the YAML editor", TaskInput{YAML: general, BaseRevision: view.Revision}},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := e.app.UpdateTask(t.Context(), "hub", c.in)
			refused, ok := err.(*Error)
			if !ok || refused.Code != CodeValidationFailed || refused.Key != "type_immutable" {
				t.Fatalf("UpdateTask(hub as general) error = %v, want %s with type_immutable", err, CodeValidationFailed)
			}
			if r := refused.Validation; len(r.Errors) != 1 || r.Errors[0].Path != "/spec/type" {
				t.Errorf("UpdateTask(hub as general) errors = %+v, want one at /spec/type", r.Errors)
			}
			if onDisk, err := os.ReadFile(view.Path); err != nil || string(onDisk) != view.YAML {
				t.Errorf("the task file after the refused update = %q (%v), want it unchanged", onDisk, err)
			}
		})
	}

	if err := os.WriteFile(view.Path, []byte(general), 0o600); err != nil {
		t.Fatal(err)
	}
	e.app.scanTaskFiles(t.Context())
	edited, _ := e.app.GetTask("hub")
	if edited.Type != domain.TypeGeneral || edited.Invalid {
		t.Fatalf("GetTask(hub) after the file was rewritten = %s task, invalid %v; want a valid general task", edited.Type,
			edited.Invalid)
	}
	// An editor opened before the rewrite gets the conflict that says to reload, not a refusal of the type it kept.
	_, err = e.app.UpdateTask(t.Context(), "hub", TaskInput{YAML: view.YAML, BaseRevision: view.Revision})
	if stale, ok := err.(*Error); !ok || stale.Code != CodeConflict {
		t.Errorf("UpdateTask(hub from the revision before the rewrite) error = %v, want %s", err, CodeConflict)
	}
	kept := strings.Replace(general, "Do the work of hub.", "Do the edited work of hub.", 1)
	after, err := e.app.UpdateTask(t.Context(), "hub", TaskInput{YAML: kept, BaseRevision: edited.Revision})
	if err != nil || after.Type != domain.TypeGeneral || after.YAML != kept {
		t.Errorf("UpdateTask(hub keeping its type) = %s task, YAML %q, %v; want the edit saved", after.Type, after.YAML, err)
	}
}

// legacyIssueTask is issueTask under the GitHub type's legacy names, with a comment a save keeps.
func legacyIssueTask(id, localPath string) string {
	return strings.NewReplacer(
		"  type: github\n", "  # Written before the type was renamed.\n  type: "+domain.LegacyTypeGitHub+"\n",
		"  github:\n", "  "+domain.LegacyGitHubKey+":\n",
	).Replace(issueTask(id, localPath, ""))
}

// hasLegacyNames reports whether a task's text names the GitHub type or its block by a legacy name.
func hasLegacyNames(text string) bool {
	return strings.Contains(text, domain.LegacyTypeGitHub) || strings.Contains(text, domain.LegacyGitHubKey)
}

// firstRepositoryName is the name of the first repository of a decoded github block, nil when it has none.
func firstRepositoryName(block any) any {
	repos, _ := valueAt(block, "repositories").([]any)
	if len(repos) == 0 {
		return nil
	}
	return valueAt(repos[0], "name")
}

// A task file that still names the GitHub type and its block by their legacy names loads as a github task, and nothing
// rewrites it until it is saved. Then the form and the YAML editor alike write the new names, the YAML editor keeping
// the file's comments, and the save is no change of the type. A form that still sends the legacy names is read the
// same way.
func TestLegacyGitHubNamesLoadAndSaveAsNew(t *testing.T) {
	e := newEnv(t)
	repo := mkdir(t, filepath.Join(e.app.Paths().UserHome, "repo"))
	written := map[string]string{}
	for _, id := range []string{"from-form", "from-editor"} {
		written[id] = legacyIssueTask(id, repo)
		path := filepath.Join(e.app.Paths().Tasks, id+".yaml")
		if err := os.WriteFile(path, []byte(written[id]), 0o600); err != nil {
			t.Fatalf("os.WriteFile(%s) = %v, want no error", path, err)
		}
	}
	e.app.scanTaskFiles(t.Context())
	for id, text := range written {
		v, err := e.app.GetTask(id)
		if err != nil || v.Invalid || v.Type != domain.TypeGitHub || !slices.Equal(v.Repositories, []string{"octo/repo"}) {
			t.Fatalf("GetTask(%s) = %s task, invalid %v, repositories %v, %v; want a valid %s task of octo/repo", id,
				v.Type, v.Invalid, v.Repositories, err, domain.TypeGitHub)
		}
		if got := firstRepositoryName(valueAt(v.Document, "spec", "github")); got != "octo/repo" {
			t.Errorf("GetTask(%s).Document spec.github.repositories[0].name = %v, want octo/repo", id, got)
		}
		if onDisk, err := os.ReadFile(v.Path); err != nil || string(onDisk) != text || v.YAML != text {
			t.Errorf("os.ReadFile(%s) = %q, %v and GetTask(%s).YAML = %q; want both as written",
				v.Path, onDisk, err, id, v.YAML)
		}
	}

	t.Run("the YAML editor", func(t *testing.T) {
		view, _ := e.app.GetTask("from-editor")
		edited := strings.Replace(view.YAML, "Implement the oldest", "Implement the newest", 1)
		if _, err := e.app.UpdateTask(t.Context(), "from-editor", TaskInput{YAML: edited, BaseRevision: view.Revision}); err != nil {
			t.Fatalf("UpdateTask(from-editor) = %v, want the edit saved", err)
		}
		onDisk, _ := os.ReadFile(view.Path)
		if text := string(onDisk); hasLegacyNames(text) || !strings.Contains(text, "type: github\n") ||
			!strings.Contains(text, "  github:\n") || !strings.Contains(text, "Implement the newest") ||
			!strings.Contains(text, "# Written before the type was renamed.") {
			t.Errorf("os.ReadFile(%s) after UpdateTask(from-editor) = %q, "+
				"want the edit and the comment under the new names", view.Path, text)
		}
	})

	t.Run("the form", func(t *testing.T) {
		view, _ := e.app.GetTask("from-form")
		document, err := json.Marshal(view.Document)
		if err != nil {
			t.Fatalf("json.Marshal(GetTask(from-form).Document) = %v, want no error", err)
		}
		legacy := strings.NewReplacer(`"type":"github"`, `"type":"`+domain.LegacyTypeGitHub+`"`,
			`"github":{`, `"`+domain.LegacyGitHubKey+`":{`).Replace(string(document))
		if !hasLegacyNames(legacy) {
			t.Fatalf("hasLegacyNames(%s) = false, want true", legacy)
		}
		if r := e.app.Validate(t.Context(), TaskInput{Document: json.RawMessage(legacy)}); !r.Valid || hasLegacyNames(r.YAML) {
			t.Errorf("Validate(the form's document under the legacy names) valid %v, YAML %q, errors %+v; want it valid "+
				"under the new names", r.Valid, r.YAML, r.Errors)
		}
		if _, err := e.app.UpdateTask(t.Context(), "from-form", TaskInput{Document: document, BaseRevision: view.Revision}); err != nil {
			t.Fatalf("UpdateTask(from-form) = %v, want it saved", err)
		}
		if onDisk, _ := os.ReadFile(view.Path); hasLegacyNames(string(onDisk)) {
			t.Errorf("os.ReadFile(%s) after UpdateTask(from-form) = %q, want the new names", view.Path, onDisk)
		}
	})
}

// A GitHub task need not select the built-in issue implementer, and the rule against merging is the implementer's, not
// the type's: a GitHub task with no agent or skill whose own prompt merges validates and runs, its repositories in its
// session's context.
func TestGitHubTaskWithoutTheImplementerRuns(t *testing.T) {
	e := newEnv(t)
	repo := mkdir(t, filepath.Join(e.app.Paths().UserHome, "repo"))
	const prompt = "Merge each approved pull request of octo/repo whose checks passed."
	doc := strings.Replace(issueTask("merger", repo, ""), "Implement the oldest eligible issue.", prompt, 1)
	if r := e.app.Validate(t.Context(), TaskInput{YAML: doc}); !r.Valid {
		t.Fatalf("Validate(merger) errors = %+v, want none", r.Errors)
	}
	if _, err := e.app.CreateTask(t.Context(), TaskInput{YAML: doc}); err != nil {
		t.Fatalf("CreateTask(merger) = %v, want it saved", err)
	}
	var q requests
	e.claude.setScript(func(req ports.RunRequest) string { q.add(req); return "echo 'END ok'" })
	r, err := e.app.RunTask("merger")
	if err != nil {
		t.Fatalf("RunTask(merger) = %v, want it started", err)
	}
	e.wait(r.ExecutionID, domain.StatusCompleted)
	sent := q.all()
	if len(sent) != 1 || sent[0].Prompt != prompt || len(sent[0].Agents) != 0 || len(sent[0].Skills) != 0 {
		t.Fatalf("RunTask(merger) sent the harness %+v, "+
			"want one request with the task's own prompt and no agent or skill", sent)
	}
	path := filepath.Join(sent[0].ExecutionDir, "context.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%s) = %v, want no error", path, err)
	}
	var ctx map[string]any
	if err := json.Unmarshal(data, &ctx); err != nil {
		t.Fatalf("json.Unmarshal(%s) = %v, want no error", path, err)
	}
	if typ, name := valueAt(ctx, "task", "type"), firstRepositoryName(valueAt(ctx, "task", "parameters")); typ != domain.TypeGitHub || name != "octo/repo" {
		t.Errorf("valueAt(context.json, task, type) = %v and its first repository = %v; want %s and octo/repo",
			typ, name, domain.TypeGitHub)
	}
}

// The type an update keeps is the task's last valid definition's, which a task whose file broke on disk still has. A
// file never valid has no type to keep.
func TestUpdateOfABrokenFileKeepsTheLastValidType(t *testing.T) {
	e := newEnv(t)
	script := func(id string) string {
		return scriptYAML(id, "  script:\n    inline:\n      interpreter: [/bin/sh]\n      source: echo hi\n")
	}
	e.create("kept", "")
	view, _ := e.app.GetTask("kept")
	if err := os.WriteFile(view.Path, []byte("apiVersion: [broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(filepath.Dir(view.Path), "fresh.yaml")
	if err := os.WriteFile(fresh, []byte("apiVersion: [broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.app.scanTaskFiles(t.Context())

	broken, _ := e.app.GetTask("kept")
	if !broken.Invalid {
		t.Fatalf("GetTask(kept) = %+v, want it invalid", broken)
	}
	_, err := e.app.UpdateTask(t.Context(), "kept", TaskInput{YAML: script("kept"), BaseRevision: broken.Revision})
	if refused, ok := err.(*Error); !ok || refused.Key != "type_immutable" {
		t.Errorf("UpdateTask(kept as a script) error = %v, want type_immutable", err)
	}
	if fixed, err := e.app.UpdateTask(t.Context(), "kept", TaskInput{YAML: taskYAML("kept", ""), BaseRevision: broken.Revision}); err != nil ||
		fixed.Type != domain.TypeGeneral || fixed.Invalid {
		t.Errorf("UpdateTask(kept as general) = %s task, invalid %v, %v; want the file fixed", fixed.Type, fixed.Invalid, err)
	}

	never, _ := e.app.GetTask("fresh")
	if !never.Invalid {
		t.Fatalf("GetTask(fresh) = %+v, want it invalid", never)
	}
	if first, err := e.app.UpdateTask(t.Context(), "fresh", TaskInput{YAML: script("fresh"), BaseRevision: never.Revision}); err != nil ||
		first.Type != domain.TypeScriptTask || first.Invalid {
		t.Errorf("UpdateTask(fresh as a script) = %s task, invalid %v, %v; want its first valid definition saved", first.Type,
			first.Invalid, err)
	}
}

// What the daemon tells the user carries a key and params the desktop translates — a validation error, the warning a
// task keeps, and a use-case error — beside its en-US text.
func TestMessagesCarryKeys(t *testing.T) {
	e := newEnv(t)
	skills := "  skills: [nowhere-skill]\n"
	_, err := e.app.CreateTask(t.Context(), TaskInput{YAML: taskYAML("skilled", skills)})
	ae, ok := err.(*Error)
	if !ok || ae.Key != "skill_not_found" || ae.Params["name"] != "nowhere-skill" || ae.Params["source"] != "harness" ||
		ae.Validation == nil || ae.Validation.Errors[0].Key != "skill_not_found" {
		t.Fatalf("missing skill on save: %#v", err)
	}
	imported, err := e.app.ImportTask(t.Context(), taskYAML("skilled", skills), false)
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

// What the daemon writes about an execution — its events, its error and its attempts' errors and reasons, its step —
// carries a key, and a text it quotes keeps its own; the agent's words and a harness's own error carry none.
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
	if !e.poll(5*time.Second, func() bool {
		events, _, _ := e.app.ExecutionEvents(id, 0, 0)
		for _, ev := range events {
			keys[ev.Type] = ev.MessageKey
		}
		_, ok := keys[last]
		return ok
	}) {
		e.t.Fatalf("no %s event for %s: %v", last, id, keys)
	}
	return keys
}

// Duplicate creates a disabled copy with a new identifier.
func TestDuplicate(t *testing.T) {
	e := newEnv(t)
	e.create("orig", "")
	cp, err := e.app.DuplicateTask(t.Context(), "orig")
	if err != nil {
		t.Fatal(err)
	}
	if cp.ID != "orig-copy" || cp.Enabled {
		t.Errorf("copy %s enabled=%v", cp.ID, cp.Enabled)
	}
}

// The catalog carries what selecting an agent copies into a task, the harnesses that accept its model, and the fields
// it leaves out.
func TestCatalogCarriesTheAgentCopy(t *testing.T) {
	e := newEnv(t)
	agent := "---\nname: hf-reviewer\ndescription: Reviews a change\nmodel: sonnet\ntools: Read\nskills: [hf-github-draft-pr]\n---\nYou review."
	if err := os.WriteFile(filepath.Join(e.app.d.Paths.Builtins, "agents", "hf-reviewer.md"), []byte(agent), 0o600); err != nil {
		t.Fatalf("os.WriteFile(hf-reviewer.md) = %v, want no error", err)
	}
	var entry *CatalogEntry
	for _, a := range e.app.Catalog(t.Context(), "").Agents {
		if a.Name == "hf-reviewer" {
			entry = &a
		}
	}
	if entry == nil || entry.Copy == nil {
		t.Fatalf("Catalog() agent hf-reviewer = %+v, want an entry with a copy", entry)
	}
	c := entry.Copy
	if c.Prompt != "You review." || c.Description != "Reviews a change" || c.Model != "sonnet" ||
		!slices.Equal(c.Skills, []string{"hf-github-draft-pr"}) || !slices.Equal(c.OmittedFields, []string{"tools"}) ||
		!strings.HasPrefix(c.Hash, "sha256:") {
		t.Errorf("Catalog() copy of hf-reviewer = %+v, want prompt %q, description %q, model %q, skills "+
			"[hf-github-draft-pr], omitted fields [tools], and a sha256 hash", c, "You review.", "Reviews a change", "sonnet")
	}
	if want := []string{domain.HarnessClaudeCode, domain.HarnessCodex}; !slices.Equal(c.ModelHarnesses, want) {
		t.Errorf("Catalog() copy of hf-reviewer: model harnesses = %v, want %v", c.ModelHarnesses, want)
	}
	for _, s := range e.app.Catalog(t.Context(), "").Skills {
		if s.Copy != nil {
			t.Errorf("Catalog() skill %s: copy = %+v, want none", s.Name, s.Copy)
		}
	}
}

// A new GitHub task selects the built-in implementer, whose copy becomes its prompt; the
// implementer is neither one of the type's skills nor a subagent, and no other type selects an agent.
func TestGitHubTypeSelectsTheImplementer(t *testing.T) {
	e := newEnv(t)
	for _, tt := range e.app.Catalog(t.Context(), "").TaskTypes {
		want := ""
		if tt.ID == domain.TypeGitHub {
			want = BuiltinIssueAgent
			if skills := []string{"hf-github-issue-queue", "hf-github-worktree", "hf-github-draft-pr"}; !slices.Equal(tt.DefaultSkills, skills) {
				t.Errorf("Catalog() type %s: default skills = %v, want %v", tt.ID, tt.DefaultSkills, skills)
			}
		}
		if tt.SelectedAgent != want || len(tt.DefaultAgents) != 0 {
			t.Errorf("Catalog() type %s: selected agent %q and default agents %v, want %q and none", tt.ID, tt.SelectedAgent,
				tt.DefaultAgents, want)
		}
	}
}

// A task's origin names the agent its prompt was copied from; it is not a reference, so an agent that no longer exists
// rejects nothing. Export and import keep the origin as it was.
func TestOriginSurvivesExportAndImport(t *testing.T) {
	e := newEnv(t)
	hash := "sha256:" + strings.Repeat("5e", 32)
	task := `apiVersion: harmflow/v1alpha1
kind: Task
metadata:
  id: review
  name: Review
spec:
  type: general
  prompt: You review the change.
  origin:
    agent: deleted-reviewer
    hash: ` + hash + `
  harness:
    preferred:
      provider: claude-code
`
	if _, err := e.app.CreateTask(t.Context(), TaskInput{YAML: task}); err != nil {
		t.Fatalf("CreateTask(review with the origin of a missing agent) = %v, want no error", err)
	}
	exported, err := e.app.ExportTask("review")
	if err != nil {
		t.Fatalf("ExportTask(review) = %v, want no error", err)
	}
	if !strings.Contains(string(exported), "agent: deleted-reviewer") || !strings.Contains(string(exported), "hash: "+hash) {
		t.Errorf("ExportTask(review) =\n%s\nwant the origin agent: deleted-reviewer and hash: %s", exported, hash)
	}
	if _, err := e.app.ImportTask(t.Context(), string(exported), true); err != nil {
		t.Fatalf("ImportTask(export of review) = %v, want no error", err)
	}
	v, err := e.app.GetTask("review")
	if err != nil {
		t.Fatalf("GetTask(review) = %v, want no error", err)
	}
	js, _ := json.Marshal(v.Document)
	var doc struct {
		Spec struct {
			Origin *domain.Origin `json:"origin"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(js, &doc); err != nil {
		t.Fatalf("json.Unmarshal(GetTask(review) document) = %v, want no error", err)
	}
	if want := (domain.Origin{Agent: "deleted-reviewer", Hash: hash}); doc.Spec.Origin == nil || *doc.Spec.Origin != want {
		t.Errorf("GetTask(review) after ImportTask: origin = %+v, want %+v", doc.Spec.Origin, want)
	}
}

// An export masks a password, a cookie, and a session value wherever the file holds one — the prompt, the description,
// or a comment — leaves the words around them, and still validates.
func TestExportMasksCredentials(t *testing.T) {
	e := newEnv(t)
	task := `apiVersion: harmflow/v1alpha1
kind: Task
metadata:
  id: sign-in
  name: Sign in
  description: "Uses pwd=desc-s3cret for the staging account"
# the old one was password=comment-s3cret
spec:
  type: general
  prompt: |
    Sign in with password: hunter2 and keep the session.
    Send Cookie: sessionid=abc123def456; theme=dark
    Then open https://example.test/home?sid=xyz789&tab=1 and check the password field.
  harness:
    preferred:
      provider: claude-code
`
	if _, err := e.app.CreateTask(t.Context(), TaskInput{YAML: task}); err != nil {
		t.Fatal(err)
	}
	data, err := e.app.ExportTask("sign-in")
	if err != nil {
		t.Fatal(err)
	}
	exported := string(data)
	for _, secret := range []string{"hunter2", "abc123def456", "theme=dark", "xyz789", "desc-s3cret", "comment-s3cret"} {
		if strings.Contains(exported, secret) {
			t.Errorf("the export holds %q", secret)
		}
	}
	for _, kept := range []string{"Sign in with password: [REDACTED] and keep the session.", "Send Cookie: [REDACTED]\n",
		"home?sid=[REDACTED]&tab=1 and check the password field.", "Uses pwd=[REDACTED] for the staging account",
		"# the old one was password=[REDACTED]"} {
		if !strings.Contains(exported, kept) {
			t.Errorf("the export lacks %q", kept)
		}
	}
	if r := e.app.Validate(t.Context(), TaskInput{YAML: exported}); !r.Valid {
		t.Errorf("the exported file does not validate: %+v", r.Errors)
	}
	if t.Failed() {
		t.Logf("the export:\n%s", exported)
	}
}

// An empty list of notified events or failover triggers is a choice. Saved from the form or as YAML, and duplicated,
// the file keeps the key and the API's document and effective carry the empty list; an execution then neither notifies
// nor fails over.
func TestEmptyListsAreKept(t *testing.T) {
	e := newEnv(t)
	doc, _ := json.Marshal(map[string]any{"apiVersion": "harmflow/v1alpha1", "kind": "Task",
		"metadata": map[string]any{"id": "form-empty", "name": "Form empty"},
		"spec": map[string]any{"type": "general", "prompt": "p",
			"harness": map[string]any{"preferred": map[string]any{"provider": "claude-code"},
				"fallback": map[string]any{"on": []any{}, "candidates": []any{map[string]any{"provider": "codex"}}}},
			"notifications": map[string]any{"native": map[string]any{"events": []any{}}}}})
	if _, err := e.app.CreateTask(t.Context(), TaskInput{Document: doc}); err != nil {
		t.Fatalf("create from a document: %v", err)
	}
	e.create("yaml-empty", "    fallback:\n      on: []\n      candidates:\n        - provider: codex\n"+
		"  notifications:\n    native:\n      events: []\n")
	ids := []string{"form-empty", "yaml-empty"}
	for _, id := range []string{"form-empty", "yaml-empty"} {
		cp, err := e.app.DuplicateTask(t.Context(), id)
		if err != nil {
			t.Fatalf("duplicate %s: %v", id, err)
		}
		ids = append(ids, cp.ID)
	}
	for _, id := range ids {
		v, _ := e.app.GetTask(id)
		data, err := os.ReadFile(v.Path)
		if err != nil {
			t.Fatal(err)
		}
		var file map[string]any
		if err := yaml.Unmarshal(data, &file); err != nil {
			t.Fatal(err)
		}
		for name, m := range map[string]any{"the file": file, "document": v.Document, "effective": v.Effective} {
			for _, path := range [][]string{{"spec", "harness", "fallback", "on"}, {"spec", "notifications", "native", "events"}} {
				if l, ok := valueAt(m, path...).([]any); !ok || len(l) != 0 {
					t.Errorf("%s: %s in %s is %#v, want []", id, strings.Join(path, "."), name, valueAt(m, path...))
				}
			}
		}
	}
	r, _ := e.app.RunTask("form-empty")
	e.wait(r.ExecutionID, domain.StatusCompleted)
	e.idle("form-empty")
	e.claude.setScript(func(ports.RunRequest) string { return "echo 'END quota'" })
	r, _ = e.app.RunTask("yaml-empty")
	x := e.wait(r.ExecutionID, domain.StatusFailed)
	e.idle("yaml-empty")
	if len(x.Attempts) != 1 || x.HarnessSwitches != 0 {
		t.Errorf("quota failed over with no trigger chosen: %d attempts, %d switches", len(x.Attempts), x.HarnessSwitches)
	}
	if n := e.app.Notifications(false); len(n) != 0 {
		t.Errorf("a task that chose no events notified: %+v", n)
	}
}

// valueAt reads a nested key of a decoded YAML or JSON document, nil when one is missing.
func valueAt(v any, keys ...string) any {
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

// Every default stays adjustable per task, through the YAML key the schema names. A task that sets each one, 0
// included wherever the schema allows it, keeps it; a task that sets none gets every default; one that sets only its
// timeout keeps it and gets the others. Each value is read from the effective definition the API returns, by its key,
// so a key that drifted between the schema, the file, and the API fails here.
func TestDefaultsAndOverridesFromYAML(t *testing.T) {
	e := newEnv(t)
	params := []struct {
		key      string // under spec
		def, set any
	}{
		{"execution.timeoutMinutes", 60, 90},
		{"github.delivery.testRepairAttempts", 2, 0},
		{"github.queue.maxResumeAttempts", 3, 0},
		{"github.queue.resumeStrategy", "resume_first", "alternate"},
		{"github.queue.maxDiscoveredPerCycle", 10, 25},
		{"github.queue.maxItemsPerCycle", 1, 2},
		{"github.queue.ordering", "oldest_first", "newest_first"},
		{"github.queue.maxFailedRetries", 3, 0},
		{"execution.rateLimit.initialWaitSeconds", 60, 15},
		{"execution.rateLimit.maxWaitSeconds", 900, 120},
		{"execution.rateLimit.maxRetries", 5, 0},
		{"harness.fallback.maxSwitches", 1, 0},
		{"execution.cancelGraceSeconds", 30, 5},
	}
	repo := filepath.Join(e.app.Paths().UserHome, "repo")
	_ = os.MkdirAll(repo, 0o700)
	task := func(id, fallback, execution, queue string) string {
		return fmt.Sprintf(`apiVersion: harmflow/v1alpha1
kind: Task
metadata:
  id: %s
  name: Task %s
spec:
  type: github
  prompt: Implement the oldest eligible issue.
  harness:
    preferred:
      provider: claude-code
%s%s  github:
    repositories:
      - name: octo/repo
        localPath: %s
%s`, id, id, fallback, execution, repo, queue)
	}
	effective := func(id, yamlText string) any {
		t.Helper()
		v, err := e.app.CreateTask(t.Context(), TaskInput{YAML: yamlText})
		if err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		return v.Effective
	}
	check := func(id string, eff any, want func(i int) any) {
		t.Helper()
		for i, p := range params {
			t.Run(id+" "+p.key, func(t *testing.T) {

				got := valueAt(eff, append([]string{"spec"}, strings.Split(p.key, ".")...)...)
				if fmt.Sprint(got) != fmt.Sprint(want(i)) {
					t.Errorf("CreateTask(%s).Effective spec.%s = %v, want %v", id, p.key, got, want(i))
				}
			})
		}
	}
	for _, p := range params {
		if fmt.Sprint(p.set) == fmt.Sprint(p.def) {
			t.Fatalf("the override of %s is its default", p.key)
		}
	}

	overridden := task("overridden", "    fallback:\n      maxSwitches: 0\n",
		"  execution:\n    timeoutMinutes: 90\n    cancelGraceSeconds: 5\n"+
			"    rateLimit:\n      maxRetries: 0\n      initialWaitSeconds: 15\n      maxWaitSeconds: 120\n",
		"    queue:\n      ordering: newest_first\n      maxDiscoveredPerCycle: 25\n      maxItemsPerCycle: 2\n"+
			"      resumeStrategy: alternate\n      maxResumeAttempts: 0\n      maxFailedRetries: 0\n"+
			"    delivery:\n      testRepairAttempts: 0\n")
	check("overridden", effective("overridden", overridden), func(i int) any { return params[i].set })
	check("bare", effective("bare", task("bare", "", "", "")), func(i int) any { return params[i].def })
	timed := task("timed", "", "  execution:\n    timeoutMinutes: 20\n", "")
	check("timed", effective("timed", timed), func(i int) any {
		if params[i].key == "execution.timeoutMinutes" {
			return 20
		}
		return params[i].def
	})
}

// An external edit converges and is announced; an invalid one keeps the last valid definition.
func TestExternalEdits(t *testing.T) {
	e := newEnv(t)
	e.create("ext", "")
	view, _ := e.app.GetTask("ext")
	sub, _, cancel := e.app.Subscribe(0)
	defer cancel()
	changed := strings.Replace(view.YAML, "Task ext", "Renamed", 1)
	_ = os.WriteFile(view.Path, []byte(changed), 0o600)
	e.app.scanTaskFiles(t.Context())
	if v, _ := e.app.GetTask("ext"); v.Name != "Renamed" {
		t.Errorf("name %q", v.Name)
	}
	_ = os.WriteFile(view.Path, []byte("apiVersion: [broken"), 0o600)
	e.app.scanTaskFiles(t.Context())
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

// invalidEvents collects the task.invalid events the app emits from now on.
func (e *env) invalidEvents() func() []domain.Event {
	var mu sync.Mutex
	var got []domain.Event
	e.events.setHook(func(ev domain.Event) {
		if ev.Type == domain.EventTaskInvalid {
			mu.Lock()
			got = append(got, ev)
			mu.Unlock()
		}
	})
	return func() []domain.Event {
		mu.Lock()
		defer mu.Unlock()
		return append([]domain.Event(nil), got...)
	}
}

// namesFile fails unless the text names the file before the text it quotes, which keeps its own key.
func namesFile(t *testing.T, what string, text domain.Text, file, innerKey string) {
	t.Helper()
	if text.Key != "task_file_error" || text.Params["file"] != file || text.Params["detailKey"] != innerKey ||
		!strings.HasPrefix(text.Message, file+": ") {
		t.Errorf("%s does not name %s: %+v", what, file, text)
	}
}

// A task file found broken at start is named, beside its line, in the task's warning and its validation errors, and
// announced by task.invalid as one the watcher finds. The file is broken.yml, not the broken.yaml its id would give,
// so the name is the file's own.
func TestBrokenFileAtStartIsNamed(t *testing.T) {
	var invalid func() []domain.Event
	e := newEnv(t, func(e *env) {
		broken := "apiVersion: harmflow/v1alpha1\nkind: Task\nmetadata:\n  id: [unclosed\n"
		if err := os.WriteFile(filepath.Join(e.home, "tasks", "broken.yml"), []byte(broken), 0o600); err != nil {
			t.Fatal(err)
		}
		invalid = e.invalidEvents()
	})
	tasks := e.app.ListTasks()
	if len(tasks) != 1 || tasks[0].ID != "broken" || !tasks[0].Invalid || len(tasks[0].Warnings) != 1 ||
		tasks[0].Warnings[0].Code != domain.WarningExternalEditInvalid {
		t.Fatalf("listed %+v", tasks)
	}
	warning := tasks[0].Warnings[0]
	namesFile(t, "the warning", warning.Text, "broken.yml", "yaml_syntax")
	view, _ := e.app.GetTask("broken")
	if view.Validation == nil || len(view.Validation.Errors) != 1 || view.Validation.Errors[0].Line == 0 {
		t.Fatalf("validation %+v", view.Validation)
	}
	first := view.Validation.Errors[0]
	namesFile(t, "the validation error", first.Text, "broken.yml", "yaml_syntax")
	if line := fmt.Sprintf("line %d", first.Line); !strings.Contains(warning.Message, line) {
		t.Errorf("the warning %q does not give the %s", warning.Message, line)
	}
	evs := invalid()
	if len(evs) != 1 || evs[0].TaskID != "broken" || evs[0].MessageKey != "task_file_invalid" ||
		evs[0].Params["file"] != "broken.yml" || evs[0].Payload["file"] != "broken.yml" {
		t.Fatalf("task.invalid at start: %+v", evs)
	}
	if errs, _ := evs[0].Payload["errors"].([]schemaIssue); len(errs) != 1 || errs[0].Params["file"] != "broken.yml" {
		t.Errorf("the event's errors %+v", evs[0].Payload["errors"])
	}
}

// The watcher names a loaded task's file once it breaks, for a syntax error and for a schema rule, which keeps its
// JSON pointer and line.
func TestBrokenFileFromTheWatcherIsNamed(t *testing.T) {
	e := newEnv(t)
	e.create("ext", "")
	view, _ := e.app.GetTask("ext")
	invalid := e.invalidEvents()
	for i, c := range []struct {
		name, yaml, key, path string
		line                  int
	}{
		{"a syntax error", "apiVersion: [broken", "yaml_syntax", "", 1},
		{"a schema rule", strings.Replace(view.YAML, "kind: Task", "kind: Job", 1), "schema_const", "/kind", 2},
	} {
		t.Run(c.name, func(t *testing.T) {

			_ = os.WriteFile(view.Path, []byte(c.yaml), 0o600)
			e.app.scanTaskFiles(t.Context())
			got, _ := e.app.GetTask("ext")
			if !got.Invalid || len(got.Warnings) != 1 || got.Validation == nil || len(got.Validation.Errors) == 0 {
				t.Fatalf("GetTask(ext) = %+v, want it invalid, with one warning and a validation error", got)
			}
			namesFile(t, c.name+": the warning", got.Warnings[0].Text, "ext.yaml", c.key)
			if got.Warnings[0].Subject != c.path {
				t.Errorf("GetTask(ext).Warnings[0].Subject = %q, want %q", got.Warnings[0].Subject, c.path)
			}
			first := got.Validation.Errors[0]
			namesFile(t, c.name+": the validation error", first.Text, "ext.yaml", c.key)
			if first.Path != c.path || first.Line != c.line {
				t.Errorf("GetTask(ext).Validation.Errors[0] at %q line %d, want %q line %d", first.Path, first.Line, c.path, c.line)
			}
			evs := invalid()
			if len(evs) != i+1 {
				t.Fatalf("task.invalid events = %d, want %d", len(evs), i+1)
			}
			if ev := evs[i]; ev.MessageKey != "task_changed_externally_invalid" || ev.Params["file"] != "ext.yaml" ||
				ev.Payload["file"] != "ext.yaml" {
				t.Errorf("task.invalid = %+v, want task_changed_externally_invalid naming ext.yaml", ev)
			}
		})
	}
}

// The child of TestStartAfterAKillMidSave is a re-run of the same test binary, told by these variables to save a task
// file and hold before its rename until it is killed.
const (
	taskFileCrashDirEnv     = "HARMFLOW_TEST_TASK_FILE_CRASH_DIR"
	taskFileCrashContentEnv = "HARMFLOW_TEST_TASK_FILE_CRASH_CONTENT"
	taskFileCrashHeld       = "held before the rename"
)

// A daemon killed while it saves a task file starts again with the task's old definition. A child process saves a new
// version through the real fsstore and holds once its temporary file is written and synced, before the rename; it is
// killed there with SIGKILL, and a new App starts over the directory the kill left.
func TestStartAfterAKillMidSave(t *testing.T) {
	if taskFileCrashChild() {
		return
	}
	e := newEnv(t)
	e.create("crash-save", "")
	view, _ := e.app.GetTask("crash-save")
	old, err := os.ReadFile(view.Path)
	if err != nil {
		t.Fatal(err)
	}
	next := strings.Replace(string(old), "name: Task crash-save", "name: Renamed before the kill", 1)
	if next == string(old) {
		t.Fatalf("the new version changes nothing in:\n%s", old)
	}
	e.app.Stop(domain.Text{Message: "test kill"})
	dir := e.app.Paths().Tasks
	killTaskFileSave(t, dir, next)
	if temps, _ := filepath.Glob(filepath.Join(dir, ".crash-save.yaml.tmp-*")); len(temps) != 1 {
		t.Fatalf("temporary files %v, want the one the kill left", temps)
	}
	e = e.restart(t)
	list := e.app.ListTasks()
	if len(list) != 1 || list[0].ID != "crash-save" || list[0].Invalid || list[0].Name != "Task crash-save" {
		t.Fatalf("listed %+v, want crash-save alone, valid, with its old name", list)
	}
	got, err := e.app.GetTask("crash-save")
	if err != nil {
		t.Fatal(err)
	}
	if got.YAML != string(old) || got.Validation != nil || len(got.Warnings) > 0 {
		t.Errorf("after the kill: validation %+v, warnings %+v, yaml:\n%s\nwant the old one:\n%s", got.Validation,
			got.Warnings, got.YAML, old)
	}
}

// taskFileCrashChild is the child's part, run when the variables name a directory: it saves the new version of
// crash-save there and holds before the rename, printing taskFileCrashHeld, until it is killed. It returns false in any
// other process.
func taskFileCrashChild() bool {
	dir := os.Getenv(taskFileCrashDirEnv)
	if dir == "" {
		return false
	}
	tasks, err := fsstore.NewTasks(dir)
	if err == nil {
		tasks.HoldAt = func(p fsstore.WritePoint) {
			if p != fsstore.BeforeRename {
				return
			}
			fmt.Println(taskFileCrashHeld)
			// Held until killed. Should the parent die first, its end of stdin closes and the child exits.
			_, _ = io.Copy(io.Discard, os.Stdin)
			os.Exit(2)
		}
		_, err = tasks.Save("crash-save", []byte(os.Getenv(taskFileCrashContentEnv)), "")
	}
	fmt.Fprintf(os.Stderr, "the save returned without holding: %v\n", err)
	os.Exit(3)
	return true
}

// killTaskFileSave runs the child that saves content as crash-save in dir, waits until it holds before the rename, and
// kills it there with SIGKILL.
func killTaskFileSave(t *testing.T, dir, content string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^"+t.Name()+"$")
	cmd.Env = append(os.Environ(), taskFileCrashDirEnv+"="+dir, taskFileCrashContentEnv+"="+content)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	held, drained := make(chan bool, 1), make(chan struct{})
	go func() {
		defer close(drained)
		found := false
		for scan := bufio.NewScanner(stdout); scan.Scan(); {
			if !found && scan.Text() == taskFileCrashHeld {
				found = true
				held <- true
			}
		}
		if !found {
			held <- false
		}
	}()
	select {
	case ok := <-held:
		if !ok {
			<-drained
			err := cmd.Wait()
			t.Fatalf("the child ended (%v) without holding:\n%s", err, stderr.String())
		}
	case <-time.After(2 * time.Minute): // only a broken child gets here: the kill itself waits for the hold
		_ = cmd.Process.Kill()
		<-drained
		_ = cmd.Wait()
		t.Fatalf("the child never held:\n%s", stderr.String())
	}
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	<-drained
	err = cmd.Wait()
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Fatalf("the child ended with %v, want it killed by SIGKILL:\n%s", err, stderr.String())
	}
}

// A record or a ledger saved before the agent's text was redacted on arrival would keep a credential with no time
// limit, so the daemon redacts both as it loads them, and saves them back.
func TestRedactsWhatWasKeptBefore(t *testing.T) {
	e := newEnv(t)
	token := "ghp_" + strings.Repeat("e", 36)
	x := domain.NewExecution(domain.TaskDocument{Metadata: domain.TaskMetadata{ID: "old"}}, domain.TriggerManual, nil,
		time.Now())
	x.Status = domain.StatusCompleted
	x.Summary = "Pushed with " + token
	x.LastOperation = "git push https://x-access-token:" + token + "@github.com/o/r"
	if err := e.app.d.Executions.Save(x); err != nil {
		t.Fatal(err)
	}
	if err := e.app.d.State.Save("workitems", ledgerFile{SchemaVersion: "1", Tasks: map[string]map[string]*domain.WorkItem{
		"old": {"o/r#1": {TaskID: "old", Key: "o/r#1", Title: "Fix " + token, Status: domain.WorkItemInProgress}}}}); err != nil {
		t.Fatal(err)
	}
	if err := e.app.recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	e.app.loadLedger()
	got, _ := e.app.GetExecution(x.ID)
	if got.Summary != "Pushed with [REDACTED]" || got.LastOperation != "git push https://x-access-token:[REDACTED]@github.com/o/r" {
		t.Errorf("the loaded record: summary %q, last operation %q", got.Summary, got.LastOperation)
	}
	e.app.mu.Lock()
	title := e.app.ledger["old"]["o/r#1"].Title
	e.app.mu.Unlock()
	if title != "Fix [REDACTED]" {
		t.Errorf("the loaded ledger's title %q", title)
	}
	for name, path := range map[string]string{
		"the record": filepath.Join(e.home, "state", "executions", x.ID+".json"),
		"the ledger": filepath.Join(e.home, "state", "workitems.json"),
	} {
		data, err := os.ReadFile(path)
		if err != nil || strings.Contains(string(data), "ghp_") || !strings.Contains(string(data), "[REDACTED]") {
			t.Errorf("%s on disk was not saved back redacted (%v):\n%s", name, err, data)
		}
	}
}

// A restart marks active executions interrupted and counts the failure against the item.
func TestRecoveryMarksInterrupted(t *testing.T) {
	e := newEnv(t)
	e.create("crash", "")
	x := domain.NewExecution(domain.TaskDocument{Metadata: domain.TaskMetadata{ID: "crash"}}, domain.TriggerManual, nil, time.Now())
	x.Status = domain.StatusRunning
	x.WorkItem = "o/r#1"
	x.LastOperation = "git push -u origin harmflow/1-x"
	_ = e.app.d.Executions.Save(x)
	if err := e.app.recover(t.Context()); err != nil {
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
	// The next execution's context names the interruption and its last operation.
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

	// Each recovery counts against the retry limit of the item's own task, here one retry, where the default of three
	// would leave the item failed after its second interruption.
	e.idle("crash")
	issues := strings.Replace(taskYAML("issues", fmt.Sprintf("  github:\n    repositories:\n"+
		"      - name: o/r\n        localPath: %q\n    queue:\n      maxFailedRetries: 1\n",
		mkdir(t, filepath.Join(e.app.Paths().UserHome, "repo")))),
		"type: general", "type: github", 1)
	if _, err := e.app.CreateTask(t.Context(), TaskInput{YAML: issues}); err != nil {
		t.Fatal(err)
	}
	for i, want := range []domain.WorkItemStatus{domain.WorkItemFailed, domain.WorkItemNeedsAttention} {
		x := domain.NewExecution(domain.TaskDocument{Metadata: domain.TaskMetadata{ID: "issues"}}, domain.TriggerManual,
			nil, time.Now())
		x.Status = domain.StatusRunning
		x.WorkItem = "o/r#2"
		if err := e.app.d.Executions.Save(x); err != nil {
			t.Fatal(err)
		}
		if err := e.app.recover(t.Context()); err != nil {
			t.Fatal(err)
		}
		var item *domain.WorkItem
		for _, w := range e.app.Queue().WorkItems {
			if w.TaskID == "issues" && w.Key == "o/r#2" {
				item = &w
			}
		}
		if item == nil || item.Status != want || item.Failures != i+1 || item.LastExecutionID != x.ID {
			t.Errorf("after recovery %d, work item %+v; want %s with %d failures, the last by %s", i+1, item, want, i+1,
				x.ID)
		}
	}
}

// harmflow-app#63: a crash at any moment leaves a record that is not behind the execution's log. The home is copied
// just before an event is appended, which is what a crash then leaves on disk, and a new daemon recovers the copy: it
// names the last operation the log recorded, counts the interruption against the item the session selected, and the
// next session's context carries both.
func TestRecoveryAfterACrashMatchesTheLog(t *testing.T) {
	const item = "o/r#7"
	selected := `{"type":"work_item.selected","workItem":"o/r#7","message":"Fix the crash"}`
	step := func(message string) string { return `{"type":"step.started","message":"` + message + `"}` }
	pullRequest := func(n int, message string) string {
		return fmt.Sprintf(`{"type":"pull_request.created","url":"https://github.com/o/r/pull/%d","number":%d,`+
			`"workItem":"o/r#7","message":"%s"}`, n, n, message)
	}
	// The session's steps: lines appended to progress.jsonl in one write, so one pass reads them all, and a wait until
	// the execution's log has a text, so the next steps follow what the daemon already took in.
	progress := func(lines ...string) string { return `printf '%s\n' '` + strings.Join(lines, `' '`) + `' >> "$P"` }
	logged := func(text string) string { return `until grep -qF '` + text + `' "$L"; do sleep 0.05; done` }
	for _, tc := range []struct {
		name  string
		steps []string
		crash string // the message of the event the daemon dies before appending
		want  string // the last operation the log recorded
	}{
		{"work item selected", []string{progress(selected, step("Reading the issue"))}, "Reading the issue", ""},
		{"completed command", []string{progress(selected), logged("Fix the crash"), "echo 'CMD git commit -m x'",
			"echo 'CMD git push -u origin harmflow/7-x'", "echo 'Pushed the branch'"},
			"Pushed the branch", "git push -u origin harmflow/7-x"},
		{"branch", []string{progress(selected), logged("Fix the crash"), progress(`{"type":"workspace.changed",`+
			`"message":"Created the branch harmflow/7-x","branch":"harmflow/7-x"}`, step("Writing the fix"))},
			"Writing the fix", "Created the branch harmflow/7-x"},
		{"later pull request", []string{progress(selected, pullRequest(9, "Opened a draft")), logged("Opened a draft"),
			progress(pullRequest(10, "Opened another draft"), step("Waiting for review"))},
			"Waiting for review", "pull_request.created https://github.com/o/r/pull/10"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.create("crashed", "")
			logs := e.app.d.Paths.Logs
			e.claude.setScript(func(ports.RunRequest) string {
				return `P="$HARMFLOW_EXECUTION_DIR/progress.jsonl"; L="` + logs + `/$HARMFLOW_EXECUTION_ID.jsonl"
` + strings.Join(tc.steps, "\n") + "\nexec sleep 30"
			})
			rec, id := e.crashAt("crashed", func(ev domain.Event) bool { return ev.Message == tc.crash })

			x, err := rec.app.GetExecution(id)
			if err != nil {
				t.Fatal(err)
			}
			if x.Status != domain.StatusInterrupted || x.WorkItem != item || x.LastOperation != tc.want {
				t.Errorf("recovered execution: %s, work item %q, last operation %q; want interrupted, %q, %q", x.Status,
					x.WorkItem, x.LastOperation, item, tc.want)
			}
			var got *domain.WorkItem
			for _, w := range rec.app.Queue().WorkItems {
				if w.Key == item {
					got = &w
				}
			}
			if got == nil || got.Status != domain.WorkItemFailed || got.Failures != 1 || got.LastExecutionID != id ||
				got.LastOperation != tc.want {
				t.Errorf("recovered work item %+v; want failed with one failure by %s, last operation %q", got, id, tc.want)
			}

			r, _ := rec.app.RunTask("crashed")
			next := rec.wait(r.ExecutionID, domain.StatusCompleted)
			data, err := os.ReadFile(filepath.Join(next.ExecutionDir, "context.json"))
			if err != nil {
				t.Fatal(err)
			}
			type interrupted struct {
				ExecutionID   string `json:"executionId"`
				WorkItem      string `json:"workItem"`
				LastOperation string `json:"lastOperation"`
			}
			var doc struct {
				Interrupted []interrupted `json:"interruptedExecutions"`
			}
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatal(err)
			}
			if want := (interrupted{id, item, tc.want}); len(doc.Interrupted) != 1 || doc.Interrupted[0] != want {
				t.Errorf("the next context's interruptedExecutions %+v, want [%+v]", doc.Interrupted, want)
			}
		})
	}
}

// A harness still running when the daemon crashed is ended once a daemon starts again, before the execution reads
// interrupted: the harness the attempt's record names, its child in its group, and a command in a session of its own,
// as Claude Code runs a tool command. The record names the harness from the moment it started, and the log names each
// process ended, ahead of the interruption.
func TestRecoveryEndsTheHarnessACrashLeft(t *testing.T) {
	e := newEnv(t)
	e.create("crashed", "")
	// macOS has no setsid command; Perl's POSIX::setsid stands in for it.
	e.claude.setScript(func(ports.RunRequest) string {
		return `D="$HARMFLOW_EXECUTION_DIR"
sleep 300 & echo $! > "$D/child.pid"
perl -e 'use POSIX; POSIX::setsid() or die; open STDOUT, ">/dev/null"; open STDERR, ">/dev/null"; ` +
			`open F, ">", "$ARGV[0].tmp"; print F $$; close F; rename "$ARGV[0].tmp", $ARGV[0]; exec "sleep", "300"' "$D/session.pid" &
until [ -s "$D/session.pid" ]; do sleep 0.05; done
printf '%s\n' '{"type":"step.started","message":"Holding"}' >> "$D/progress.jsonl"
wait`
	})
	rec, id := e.crashAt("crashed", func(ev domain.Event) bool { return ev.Message == "Holding" })

	x, err := rec.app.GetExecution(id)
	if err != nil {
		t.Fatal(err)
	}
	if x.Status != domain.StatusInterrupted || len(x.Attempts) != 1 || x.Attempts[0].Process == nil {
		t.Fatalf("recovered execution: %s, attempts %+v; want interrupted, one attempt naming its process", x.Status,
			x.Attempts)
	}
	harness := x.Attempts[0].Process.Pid
	child := waitForPid(t, filepath.Join(x.ExecutionDir, "child.pid"))
	session := waitForPid(t, filepath.Join(x.ExecutionDir, "session.pid"))
	events, _, _ := rec.app.ExecutionEvents(id, 0, 0)
	ended := map[int]int{} // the index in the log of each process ended
	started, interrupted := 0, -1
	for i, ev := range events {
		pid, _ := strconv.Atoi(fmt.Sprint(ev.Payload["pid"]))
		switch {
		case ev.Type == domain.EventHarnessStarted:
			started = pid
		case ev.Type == domain.EventProcessEnded && ev.MessageKey == "process_interrupted_ended":
			ended[pid] = i
		case ev.Type == domain.EventExecutionInterrupted:
			interrupted = i
		}
	}
	if started != harness {
		t.Errorf("the record names the harness %d, harness.started %d", harness, started)
	}
	for name, pid := range map[string]int{"harness": harness, "child in its group": child, "command in a session": session} {
		if alive(pid) {
			t.Errorf("the %s, %d, survived the restart", name, pid)
		}
		if i, ok := ended[pid]; !ok || i > interrupted {
			t.Errorf("the log names the %s, %d, as ended at %d (%v), the interruption at %d", name, pid, i, ok, interrupted)
		}
	}
}

// crashAt runs the task and copies the daemon's home just before the first event crash matches is appended, which is
// what a crash at that moment leaves on disk. It returns a daemon started over the copy, which recovered it, and the
// execution's id. The first daemon goes on; what follows never reaches the copy.
func (e *env) crashAt(task string, crash func(domain.Event) bool) (*env, string) {
	e.t.Helper()
	home := filepath.Join(e.t.TempDir(), "home")
	copied := make(chan error, 1)
	var once sync.Once
	e.events.setHook(func(ev domain.Event) {
		if !crash(ev) {
			return
		}
		once.Do(func() {
			// A record is saved whole or not at all: a save in progress finishes first, and the next waits for the copy.
			e.app.saveMu.Lock()
			defer e.app.saveMu.Unlock()
			copied <- os.CopyFS(home, os.DirFS(e.home))
		})
	})
	r, err := e.app.RunTask(task)
	if err != nil || r.Outcome != "started" {
		e.t.Fatalf("run: %v %+v", err, r)
	}
	var copyErr error
	if !e.poll(10*time.Second, func() bool {
		select {
		case copyErr = <-copied:
			return true
		default:
			return false
		}
	}) {
		e.t.Fatalf("execution %s never reached the event to crash at", r.ExecutionID)
	}
	if copyErr != nil {
		e.t.Fatalf("copying the home: %v", copyErr)
	}
	return openEnv(e.t, home), r.ExecutionID
}

// Notices follow the task's events and the global level.
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

// Whether a running execution notifies is decided by the definition it started with, its task's name included; an
// edit to the task's notifications applies from the next execution.
func TestNoticesFollowTheStartingDefinition(t *testing.T) {
	e := newEnv(t)
	gates := t.TempDir()
	e.claude.setScript(func(req ports.RunRequest) string {
		return fmt.Sprintf("while [ ! -e %s ]; do sleep 0.05; done; echo 'END ok'", filepath.Join(gates, req.TaskID))
	})
	events := func(list string) string { return "  notifications:\n    native:\n      events: [" + list + "]\n" }
	for _, c := range []struct {
		id, before, after string
		first, next       int // the execution.completed notices of the running execution and of the next one
	}{
		{"removed", "execution.completed", "execution.failed", 1, 0},
		{"added", "execution.failed", "execution.completed", 0, 1},
	} {
		t.Run(c.id, func(t *testing.T) {
			e := e.in(t)

			e.create(c.id, events(c.before))
			first, _ := e.app.RunTask(c.id)
			e.wait(first.ExecutionID, domain.StatusRunning)
			view, _ := e.app.GetTask(c.id)
			edited := strings.Replace(taskYAML(c.id, events(c.after)), "name: Task "+c.id, "name: Renamed "+c.id, 1)
			if _, err := e.app.UpdateTask(t.Context(), c.id, TaskInput{YAML: edited, BaseRevision: view.Revision}); err != nil {
				t.Fatal(err)
			}
			if view, _ := e.app.GetTask(c.id); view.Name != "Renamed "+c.id {
				t.Fatalf("GetTask(%s) = %+v, want the edit saved", c.id, view)
			}
			if err := os.WriteFile(filepath.Join(gates, c.id), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			e.wait(first.ExecutionID, domain.StatusCompleted)
			e.idle(c.id) // the notice is decided once the execution has returned
			got := e.completedNotices(first.ExecutionID)
			if len(got) != c.first {
				t.Errorf("completedNotices(running execution) = %d, want %d", len(got), c.first)
			}
			for _, n := range got {
				if n.Params["taskName"] != "Task "+c.id || n.Body != "Task "+c.id+" completed." {
					t.Errorf("notice = %+v, want it to name the task as it started, Task %s", n, c.id)
				}
			}

			next, _ := e.app.RunTask(c.id)
			e.wait(next.ExecutionID, domain.StatusCompleted)
			e.idle(c.id)
			got = e.completedNotices(next.ExecutionID)
			if len(got) != c.next {
				t.Errorf("completedNotices(next execution) = %d, want %d", len(got), c.next)
			}
			for _, n := range got {
				if n.Params["taskName"] != "Renamed "+c.id {
					t.Errorf("notice = %+v, want it to name the task as renamed, Renamed %s", n, c.id)
				}
			}
		})
	}
}

// completedNotices returns the execution.completed notices of one execution.
func (e *env) completedNotices(executionID string) []Notification {
	var out []Notification
	for _, n := range e.app.Notifications(false) {
		if n.EventType == domain.EventExecutionCompleted && n.ExecutionID == executionID {
			out = append(out, n)
		}
	}
	return out
}

// Past the cap the oldest read notices go first, and an unread notice is never dropped.
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
	e.app.notifyFor(completed, nil)
	if n := e.app.Notifications(true); len(n) != maxNotifications+1 {
		t.Errorf("%d unread notices kept, want %d", len(n), maxNotifications+1)
	}
	// Once they are read, the next notice trims the list back to the cap, oldest first.
	e.app.MarkNotificationsRead(nil)
	e.app.notifyFor(completed, nil)
	all := e.app.Notifications(false)
	if len(all) != maxNotifications {
		t.Fatalf("%d read notices kept, want %d", len(all), maxNotifications)
	}
	if oldest := all[len(all)-1].ID; oldest != fmt.Sprintf("old-%d", maxNotifications-3) {
		t.Errorf("oldest notice kept is %s, want the two oldest dropped", oldest)
	}

	// Only the two notices before the oldest are read: the older of them is dropped, not the oldest unread one.
	hold(func(i int) bool { return i == maxNotifications-2 || i == maxNotifications-3 })
	e.app.notifyFor(completed, nil)
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

// A notice sends the params its text reads. A message the daemon wrote is nested with its key, so the desktop
// translates it in place; the agent's words are quoted as they are.
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
	var params map[string]string
	if !e.poll(5*time.Second, func() bool {
		for _, n := range e.app.Notifications(false) {
			if n.EventType == eventType {
				params = n.Params
				return true
			}
		}
		return false
	}) {
		e.t.Fatalf("no %s notice", eventType)
	}
	return params
}

// An unenforced restriction runs with a warning on the task and on the execution.
func TestPolicyWarningIsShown(t *testing.T) {
	e := newEnv(t)
	e.create("policy", "")
	if v, _ := e.app.GetTask("policy"); len(v.Warnings) != 0 {
		t.Errorf("the fake enforces everything, got %+v", v.Warnings)
	}
}

// Cancelling the context Start received ends the scheduler, watcher, and maintenance loops, as Stop does.
func TestCancellingStartContextEndsTheLoops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	e := newEnv(t, func(e *env) { e.startCtx = ctx })
	cancel()
	ended := make(chan struct{})
	go func() {
		e.app.wg.Wait()
		close(ended)
	}()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the loops still run 5s after Start's context was cancelled")
	}
}

// Stop ends the harness detection Start launched and returns only once it has ended, so the event log holds no harness
// check after daemon.stopping.
func TestStopEndsAndWaitsForHarnessDetection(t *testing.T) {
	var mu sync.Mutex
	var types []string
	e := newEnv(t, func(e *env) {
		e.codex.hold(t)
		e.codex.cutLag = 300 * time.Millisecond
		e.events.setHook(func(ev domain.Event) {
			mu.Lock()
			types = append(types, ev.Type)
			mu.Unlock()
		})
	})
	if !e.poll(5*time.Second, e.codex.holding) {
		t.Fatal("the detection Start launched never reached Detect")
	}
	e.app.Stop(domain.Text{Message: "test stop"})
	if e.codex.holding() {
		t.Fatal("Stop returned while a detection it launched was still running")
	}
	mu.Lock()
	defer mu.Unlock()
	stopping := slices.Index(types, domain.EventDaemonStopping)
	if stopping < 0 {
		t.Fatalf("events = %v, want %s among them", types, domain.EventDaemonStopping)
	}
	if after := slices.Index(types[stopping:], domain.EventHarnessChecked); after >= 0 {
		t.Errorf("events = %v, want no %s after %s", types, domain.EventHarnessChecked, domain.EventDaemonStopping)
	}
}
