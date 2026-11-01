package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/procutil"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// scriptYAML is a script task whose spec holds body, indented under spec.
func scriptYAML(id, body string) string {
	return fmt.Sprintf(`apiVersion: harmflow/v1alpha1
kind: Task
metadata:
  id: %s
  name: Script %s
spec:
  type: script
%s`, id, id, body)
}

func (e *env) createScript(id, body string) TaskView {
	e.t.Helper()
	v, err := e.app.CreateTask(e.t.Context(), TaskInput{YAML: scriptYAML(id, body)})
	if err != nil {
		e.t.Fatalf("create %s: %v", id, err)
	}
	return v
}

func (e *env) runScript(id string, want ...domain.ExecutionStatus) ExecutionView {
	e.t.Helper()
	res, err := e.app.RunTask(id)
	if err != nil || res.Outcome != "started" {
		e.t.Fatalf("run %s: %v %+v", id, err, res)
	}
	x := e.wait(res.ExecutionID, want...)
	e.idle(id)
	return x
}

// output waits for the execution's last event and returns its program's output lines, each with its stream.
func (e *env) output(id string) []string {
	e.t.Helper()
	var lines []string
	if !e.poll(5*time.Second, func() bool {
		events, _, _ := e.app.ExecutionEvents(id, 0, 0)
		lines = nil
		ended := false
		for _, ev := range events {
			if ev.Type == domain.EventScriptOutput {
				lines = append(lines, fmt.Sprint(ev.Payload["stream"])+": "+ev.Message)
			}
			ended = ended || domain.TerminalEventFor(domain.StatusCompleted) == ev.Type ||
				ev.Type == domain.EventExecutionFailed || ev.Type == domain.EventExecutionCancelled ||
				ev.Type == domain.EventExecutionInterrupted
		}
		return ended
	}) {
		e.t.Fatalf("execution %s never ended", id)
	}
	return lines
}

func contains(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

func hasWarning(ws []domain.Warning, key string) bool {
	for _, w := range ws {
		if w.Key == key {
			return true
		}
	}
	return false
}

func issueAt(issues []schemaIssue, path string) *schemaIssue {
	for i := range issues {
		if issues[i].Path == path {
			return &issues[i]
		}
	}
	return nil
}

// A script task validates with no prompt and no harness, and one that sets a prompt is rejected with an error naming
// it; command and inline are one or the other.
func TestScriptTaskValidates(t *testing.T) {
	e := newEnv(t)
	r := e.app.Validate(t.Context(), TaskInput{YAML: scriptYAML("plain", "  script:\n    command: [/bin/echo, hi]\n")})
	if !r.Valid {
		t.Fatalf("a script task with no prompt or harness: %+v", r.Errors)
	}
	r = e.app.Validate(t.Context(), TaskInput{YAML: scriptYAML("prompted", "  prompt: do it\n  script:\n    command: [/bin/echo]\n")})
	if is := issueAt(r.Errors, "/spec/prompt"); r.Valid || is == nil || is.Key != "schema_false" {
		t.Errorf("a script task with a prompt: %+v", r.Errors)
	}
	both := "  script:\n    command: [/bin/echo]\n    inline:\n      interpreter: [/bin/sh]\n      source: echo hi\n"
	if r = e.app.Validate(t.Context(), TaskInput{YAML: scriptYAML("both", both)}); r.Valid {
		t.Error("a script task with both command and inline validated")
	}
	if r = e.app.Validate(t.Context(), TaskInput{YAML: scriptYAML("neither", "  script:\n    workingDirectory: /tmp\n")}); r.Valid {
		t.Error("a script task with neither command nor inline validated")
	}
	reserved := "  script:\n    command: [/bin/echo]\n    environment:\n      HARMFLOW_EXECUTION_ID: forged\n"
	if r = e.app.Validate(t.Context(), TaskInput{YAML: scriptYAML("forged", reserved)}); r.Valid {
		t.Error("a script task replaced one of HarmFlow's own variables")
	}
	// Every other type still needs its prompt and harness, and takes no script block.
	if r = e.app.Validate(t.Context(), TaskInput{YAML: taskYAML("general", "  script:\n    command: [/bin/echo]\n")}); r.Valid {
		t.Error("a general task with a script block validated")
	}
}

// A script task takes no harness, with or without a model, an effort, or a fallback chain, and no agents or skills:
// each is rejected with an error naming its field, and the same task without it validates. The form's document, sent as
// JSON, is held to the same rule.
func TestScriptTaskRejectsHarnessAgentsAndSkills(t *testing.T) {
	e := newEnv(t)
	const program = "  script:\n    command: [/bin/echo, hi]\n"
	if r := e.app.Validate(t.Context(), TaskInput{YAML: scriptYAML("plain", program)}); !r.Valid {
		t.Fatalf("Validate(script task with an argument list) errors = %+v, want valid", r.Errors)
	}
	for _, tc := range []struct {
		name, field, path string
	}{
		{"harness", "  harness:\n    preferred:\n      provider: claude-code\n", "/spec/harness"},
		{"harness with a model and an effort",
			"  harness:\n    preferred:\n      provider: claude-code\n      model: default\n      effort: high\n", "/spec/harness"},
		{"harness with a fallback chain",
			"  harness:\n    preferred:\n      provider: claude-code\n" + codexFallback, "/spec/harness"},
		{"agents", "  agents: [user-agent]\n", "/spec/agents"},
		{"skills", "  skills: [user-skill]\n", "/spec/skills"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := e.app.Validate(t.Context(), TaskInput{YAML: scriptYAML("extra", program+tc.field)})
			if is := issueAt(r.Errors, tc.path); r.Valid || is == nil || is.Key != "schema_false" {
				t.Errorf("Validate(script task with %s) = valid %v, errors %+v; want schema_false at %s", tc.name, r.Valid,
					r.Errors, tc.path)
			}
		})
	}

	// The form sends its document as JSON, through the same entry point the local API uses.
	const head = `{"apiVersion":"harmflow/v1alpha1","kind":"Task","metadata":{"id":"formed","name":"Formed"},` +
		`"spec":{"type":"script","script":{"command":["/bin/echo","hi"]}`
	if r := e.app.Validate(t.Context(), TaskInput{Document: []byte(head + `}}`)}); !r.Valid {
		t.Fatalf("Validate(the form's script document) errors = %+v, want valid", r.Errors)
	}
	doc := head + `,"harness":{"preferred":{"provider":"claude-code","model":"default","effort":"high"}}}}`
	r := e.app.Validate(t.Context(), TaskInput{Document: []byte(doc)})
	if is := issueAt(r.Errors, "/spec/harness"); r.Valid || is == nil || is.Key != "schema_false" {
		t.Errorf("Validate(the form's script document with a harness) = valid %v, errors %+v; want schema_false at "+
			"/spec/harness", r.Valid, r.Errors)
	}
}

// A script task's execution reads no harness's quota, though the harnesses report one, and carries no quota warning
// or event.
func TestScriptTaskReadsNoQuota(t *testing.T) {
	e := newEnv(t)
	e.claude.setQuota(sessionFull)
	e.createScript("unmetered", "  script:\n    command: [/bin/echo, hi]\n")
	claude, codex := e.claude.reads(), e.codex.reads()
	x := e.runScript("unmetered", domain.StatusCompleted)
	if n := e.claude.reads() - claude + e.codex.reads() - codex; n != 0 {
		t.Errorf("%d quota reads for a script execution, want 0", n)
	}
	for _, w := range x.Warnings {
		if strings.Contains(w.Key, "quota") {
			t.Errorf("execution warning %+v, want no quota warning", w)
		}
	}
	e.output(x.ID) // waits for the execution's last event
	events, _, _ := e.app.ExecutionEvents(x.ID, 0, 0)
	for _, ev := range events {
		if strings.Contains(ev.Type, "quota") || strings.Contains(ev.MessageKey, "quota") {
			t.Errorf("event %s (%s), want no quota event", ev.Type, ev.MessageKey)
		}
	}
}

// An argument list runs with no shell, the exit status decides the result, and an execution has one attempt and no
// harness.
func TestScriptRunsAndTheExitStatusDecides(t *testing.T) {
	e := newEnv(t)
	e.createScript("echo", "  script:\n    command: [/bin/echo, \"$HOME; echo injected\"]\n")
	x := e.runScript("echo", domain.StatusCompleted)
	if lines := e.output(x.ID); len(lines) != 1 || lines[0] != "stdout: $HOME; echo injected" {
		t.Errorf("output %q", lines)
	}
	if len(x.Attempts) != 1 || x.Attempts[0].Harness != "" || x.Harness != "" || *x.Attempts[0].ExitCode != 0 ||
		x.Attempts[0].Outcome != domain.AttemptSucceeded {
		t.Errorf("attempts %+v, harness %q", x.Attempts, x.Harness)
	}
	if s := x.Script; s == nil || s.Program != "/bin/echo" || len(s.Command) != 2 || *s.ExitCode != 0 || s.Process != nil {
		t.Errorf("script record %+v", x.Script)
	}

	e.createScript("three", "  script:\n    command: [/bin/sh, -c, \"exit 3\"]\n")
	x = e.runScript("three", domain.StatusFailed)
	if x.ErrorKey != "script_exit_code" || x.ErrorParams["code"] != "3" || *x.Script.ExitCode != 3 || x.Script.Signal != "" {
		t.Errorf("exit 3: %s %v, script %+v", x.ErrorKey, x.ErrorParams, x.Script)
	}
	if len(x.Attempts) != 1 {
		t.Errorf("a failed program was retried: %+v", x.Attempts)
	}

	e.createScript("killed", "  script:\n    command: [/bin/sh, -c, \"kill -9 $$\"]\n")
	x = e.runScript("killed", domain.StatusFailed)
	if x.ErrorKey != "script_signaled" || x.Script.Signal != "SIGKILL" || x.ErrorParams["signal"] != "SIGKILL" {
		t.Errorf("killed: %s %v, script %+v", x.ErrorKey, x.ErrorParams, x.Script)
	}
}

// An inline script runs through its interpreter from a file in the execution's directory, in the working directory,
// with the task's variables, as the user, with the default timeout.
func TestInlineScriptEnvironmentAndWorkingDirectory(t *testing.T) {
	e := newEnv(t)
	wd := t.TempDir()
	e.createScript("inline", fmt.Sprintf(`  script:
    inline:
      interpreter: [/bin/sh]
      source: |
        echo "script=$0"
        echo "wd=$(pwd -P)"
        echo "greeting=$GREETING"
        echo "uid=$(id -u)"
        echo "path=$PATH"
    workingDirectory: %s
    environment:
      GREETING: hello
`, wd))
	x := e.runScript("inline", domain.StatusCompleted)
	lines := e.output(x.ID)
	realWD, _ := filepath.EvalSymlinks(wd)
	script := filepath.Join(x.ExecutionDir, "script.sh")
	for _, want := range []string{"stdout: script=" + script, "stdout: wd=" + realWD, "stdout: greeting=hello",
		"stdout: uid=" + strconv.Itoa(os.Getuid()), "stdout: path=" + os.Getenv("PATH")} {
		if !contains(lines, want) {
			t.Errorf("missing %q in %q", want, lines)
		}
	}
	if data, err := os.ReadFile(script); err != nil || !strings.Contains(string(data), "greeting=$GREETING") {
		t.Errorf("the script file: %v %q", err, data)
	}
	if x.TimeoutAt == nil || x.StartedAt == nil || x.TimeoutAt.Sub(*x.StartedAt) != 60*time.Minute {
		t.Errorf("timeout %v from %v", x.TimeoutAt, x.StartedAt)
	}
	if x.Script.Environment["GREETING"] != "hello" || x.Script.WorkingDirectory != wd || x.Script.ScriptPath != script {
		t.Errorf("script record %+v", x.Script)
	}

	e.createScript("home", "  script:\n    command: [/bin/sh, -c, \"pwd -P\"]\n")
	x = e.runScript("home", domain.StatusCompleted)
	home, _ := filepath.EvalSymlinks(e.app.d.Paths.UserHome)
	if lines := e.output(x.ID); !contains(lines, "stdout: "+home) {
		t.Errorf("default working directory: %q, want %s", lines, home)
	}
}

// Standard output and error reach the events, each line with its stream, and are kept redacted.
func TestScriptOutputIsMarkedAndRedacted(t *testing.T) {
	e := newEnv(t)
	token := "ghp_" + strings.Repeat("a", 36)
	e.createScript("streams", fmt.Sprintf("  script:\n    command: [/bin/sh, -c, \"echo out; echo %s >&2\"]\n", token))
	x := e.runScript("streams", domain.StatusCompleted)
	lines := e.output(x.ID)
	if !contains(lines, "stdout: out") || !contains(lines, "stderr: [REDACTED]") {
		t.Errorf("output %q", lines)
	}
	out, err := e.app.ExecutionOutput(x.ID, 0)
	if err != nil || len(out.Attempts) != 1 || strings.Contains(out.Attempts[0].Text, token) ||
		!strings.Contains(out.Attempts[0].Text, "out\n") {
		t.Errorf("raw capture %+v %v", out, err)
	}
}

// script.started comes before every line of the program's output, and script.exited after it, also for a program that
// writes at once, whose first lines once raced the record of its start. The order was a race, so the test runs often.
func TestScriptStartedComesBeforeItsOutput(t *testing.T) {
	e := newEnv(t)
	e.createScript("early", "  script:\n    command: [/bin/sh, -c, \"echo one; echo two >&2\"]\n")
	for run := 1; run <= 30; run++ {
		x := e.runScript("early", domain.StatusCompleted)
		e.output(x.ID) // waits for the execution's last event
		events, _, _ := e.app.ExecutionEvents(x.ID, 0, 0)
		var started, exited int64
		var output []int64
		for _, ev := range events {
			switch ev.Type {
			case domain.EventScriptStarted:
				started = ev.ID
			case domain.EventScriptExited:
				exited = ev.ID
			case domain.EventScriptOutput:
				output = append(output, ev.ID)
			}
		}
		if started == 0 || exited == 0 || len(output) != 2 {
			t.Fatalf("run %d: script.started %d, script.exited %d, and %d output lines, want both events and 2 lines",
				run, started, exited, len(output))
		}
		for _, id := range output {
			if id < started || id > exited {
				t.Fatalf("run %d: script.output %d is not between script.started %d and script.exited %d",
					run, id, started, exited)
			}
		}
	}
}

// Cancelling, and the timeout, stop the program and the process it started in the background.
func TestScriptStopEndsItsProcesses(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	body := fmt.Sprintf(`  script:
    command: [/bin/sh, -c, "sleep 600 & echo $! > %s/child; sleep 600"]
  execution:
    cancelGraceSeconds: 1
`, dir)
	e.createScript("cancel", body)
	res, _ := e.app.RunTask("cancel")
	child := waitForPid(t, filepath.Join(dir, "child"))
	if err := e.app.CancelExecution(res.ExecutionID); err != nil {
		t.Fatal(err)
	}
	x := e.wait(res.ExecutionID, domain.StatusCancelled)
	e.idle("cancel")
	if alive(child) {
		t.Errorf("the background child %d survived the cancellation", child)
	}
	if x.Attempts[0].Outcome != domain.AttemptCancelled {
		t.Errorf("attempt %+v", x.Attempts[0])
	}

	// Set before the run starts, on this App alone: long enough on a loaded host for the shell to start its child first.
	e.app.d.TimeoutUnit = 3 * time.Second
	_ = os.Remove(filepath.Join(dir, "child"))
	e.createScript("slow", strings.Replace(body, "cancelGraceSeconds: 1", "cancelGraceSeconds: 1\n    timeoutMinutes: 1", 1))
	res, _ = e.app.RunTask("slow")
	child = waitForPid(t, filepath.Join(dir, "child"))
	x = e.wait(res.ExecutionID, domain.StatusFailed)
	e.idle("slow")
	if x.ErrorKey != "execution_timed_out" || x.Attempts[0].Outcome != domain.AttemptTimedOut || alive(child) {
		t.Errorf("timed out: %s, attempt %+v, child alive %v", x.ErrorKey, x.Attempts[0], alive(child))
	}
}

// A program that exits on its own leaves no background process running once the execution ends, even one that redirects
// its output, and the log names it; the execution keeps the result the program's exit status gives.
//
// $! is the shell the program forks, which runs sleep only once it execs, and the log names a leftover by what it is
// when it is ended. Under load the program could exit before that exec, so before it writes the child's pid and exits
// it polls, 50 times at most, until the kernel names the child sleep: ps's ucomm, padded to 16 columns, reads the name
// the log gives.
func TestScriptExitEndsWhatTheProgramLeft(t *testing.T) {
	needProcessTable(t)
	e := newEnv(t)
	dir := t.TempDir()
	e.createScript("leaves", fmt.Sprintf(`  script:
    command: [/bin/sh, -c, "sleep 600 >/dev/null 2>&1 & c=$!; i=0; while [ $i -lt 50 ]; do case $(ps -o ucomm= -p $c) in sleep*) break;; esac; sleep 0.05; i=$((i+1)); done; echo $c > %s/child"]
  execution:
    cancelGraceSeconds: 1
`, dir))
	x := e.runScript("leaves", domain.StatusCompleted)
	child := waitForPid(t, filepath.Join(dir, "child"))
	if alive(child) {
		t.Errorf("the background child %d survived the program's exit", child)
	}
	if x.Attempts[0].Outcome != domain.AttemptSucceeded || *x.Script.ExitCode != 0 {
		t.Errorf("attempt %+v, script %+v", x.Attempts[0], x.Script)
	}
	events, _, _ := e.app.ExecutionEvents(x.ID, 0, 0)
	named := false
	for _, ev := range events {
		named = named || ev.Type == domain.EventProcessEnded && ev.MessageKey == "process_left_ended" &&
			fmt.Sprint(ev.Payload["pid"]) == strconv.Itoa(child) && ev.Payload["executable"] == "sleep"
	}
	if !named {
		t.Errorf("the log does not name the background child %d: %+v", child, events)
	}
}

// A program that exits on its own keeps the result its exit status gives, though a cancel comes while the process it
// left running, which ignores SIGTERM, is ended over the grace period.
//
// $! is the subshell the program forks, which ignores SIGTERM only once it has run its trap, and the daemon sends
// SIGTERM to what the program left as soon as the program exits. Under load the program could exit before that trap
// ran: the subshell died of the SIGTERM, nothing was left for the grace period, and the execution ended before the
// cancel came. So the subshell marks its trap in place before it execs sleep, and the program polls for the mark, 100
// times at most, before it writes the child's pid and exits; a trap never set fails the wait for that pid rather than
// the cancel.
func TestScriptExitKeepsItsResultThroughACancel(t *testing.T) {
	// The execution ends only once the grace is over, since the leftover outlasts it, so the wait is the grace and a
	// margin for the kill and the execution's end. Unloaded, the end came within 0.1 s of the grace. At a load average of
	// 95 to 167 it came up to 7.8 s after it: the daemon's steps around the grace stretched with the load (identifying
	// the program and logging its start, when the cancel came first; starting the stop; waiting out the grace itself),
	// while the kill and the records of the end stayed under 0.1 s. The margin is about four times that.
	const grace = 2 * time.Second
	const killAndEnd = 30 * time.Second
	e := newEnv(t)
	dir := t.TempDir()
	e.createScript("late", fmt.Sprintf(`  script:
    command: [/bin/sh, -c, "(trap '' TERM; : > %[1]s/trapped; exec sleep 600) >/dev/null 2>&1 & c=$!; i=0; while [ ! -e %[1]s/trapped ] && [ $i -lt 100 ]; do sleep 0.05; i=$((i+1)); done; [ -e %[1]s/trapped ] && echo $c > %[1]s/child; echo $$ > %[1]s/program"]
  execution:
    cancelGraceSeconds: %[2]d
`, dir, int(grace/time.Second)))
	res, err := e.app.RunTask("late")
	if err != nil || res.Outcome != "started" {
		t.Fatalf("run late: %v %+v", err, res)
	}
	child := waitForPid(t, filepath.Join(dir, "child"))
	waitReaped(t, waitForPid(t, filepath.Join(dir, "program")))
	if err := e.app.CancelExecution(res.ExecutionID); err != nil {
		t.Fatal(err)
	}
	x := e.waitWithin(grace+killAndEnd, res.ExecutionID, domain.StatusCompleted)
	e.idle("late")
	if x.Attempts[0].Outcome != domain.AttemptSucceeded {
		t.Errorf("attempt %+v", x.Attempts[0])
	}
	if alive(child) {
		t.Errorf("the background child %d survived the program's exit", child)
	}
}

// waitReaped waits until the process has exited and been reaped, then a moment more, so that what waits for it has
// seen it exit.
func waitReaped(t *testing.T, pid int) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			time.Sleep(100 * time.Millisecond)
			return
		}
	}
	t.Fatalf("process %d never exited", pid)
}

// needProcessTable skips a test of what the daemon finds and ends outside a harness's or a program's group, which
// only a build that reads the process table does.
func needProcessTable(t *testing.T) {
	t.Helper()
	if !procutil.ReadsProcessTable() {
		t.Skip("needs the process table, which this build does not read")
	}
}

func waitForPid(t *testing.T, path string) int {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if data, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
				return pid
			}
		}
	}
	t.Fatalf("no pid in %s", path)
	return 0
}

func alive(pid int) bool {
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return false
		}
	}
	return true
}

// A daemon that starts after a crash ends what an interrupted script execution's program left running, and nothing in
// a group it cannot prove is that program's.
func TestRecoveryEndsWhatTheProgramLeft(t *testing.T) {
	e := newEnv(t)
	start := func() (*procutil.Process, procutil.Identity) {
		p, err := procutil.StartLines(ports.Invocation{Path: "/bin/sh", Args: []string{"-c", "sleep 600 & sleep 600"}},
			nil, func([]byte) {}, func([]byte) {})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { p.Stop(0) })
		id, err := procutil.Identify(t.Context(), p.Pid())
		if err != nil {
			t.Fatal(err)
		}
		return p, id
	}
	record := func(id procutil.Identity) *domain.Execution {
		x := domain.NewExecution(domain.TaskDocument{Metadata: domain.TaskMetadata{ID: "left"},
			Spec: domain.TaskSpec{Type: domain.TypeScriptTask}}, domain.TriggerManual, nil, time.Now())
		x.Status = domain.StatusRunning
		x.Script = &domain.ScriptRun{Program: "/bin/sh",
			Process: &domain.ProcessIdentity{Group: id.Group, LeaderStart: id.LeaderStart, Boot: id.Boot}}
		_ = e.app.d.Executions.Save(x)
		return x
	}
	ours, oursID := start()
	theirs, theirsID := start()
	theirsID.LeaderStart = "Thu Jan  1 00:00:00 1970" // the id now names a group another program leads
	x, other := record(oursID), record(theirsID)
	if err := e.app.recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ours.Done():
	case <-time.After(5 * time.Second):
		t.Error("the program the interrupted execution left is still running")
	}
	got, _ := e.app.GetExecution(x.ID)
	if got.Status != domain.StatusInterrupted || !hasWarning(got.Warnings, "script_leftovers_ended") || got.Script.Process != nil {
		t.Errorf("interrupted execution %s, warnings %+v, script %+v", got.Status, got.Warnings, got.Script)
	}
	select {
	case <-theirs.Done():
		t.Error("a group the record could not prove was the program's was ended")
	default:
	}
	if g, _ := e.app.GetExecution(other.ID); hasWarning(g.Warnings, "script_leftovers_ended") {
		t.Errorf("warnings %+v", g.Warnings)
	}
}

// The record keeps the program as it ran, whatever edit follows.
func TestScriptRecordKeepsWhatRan(t *testing.T) {
	e := newEnv(t)
	body := "  script:\n    inline:\n      interpreter: [/bin/sh]\n      source: echo first\n"
	e.createScript("edited", body)
	x := e.runScript("edited", domain.StatusCompleted)
	view, _ := e.app.GetTask("edited")
	if _, err := e.app.UpdateTask(t.Context(), "edited", TaskInput{YAML: strings.Replace(view.YAML, "echo first", "echo second", 1),
		BaseRevision: view.Revision}); err != nil {
		t.Fatal(err)
	}
	got, _ := e.app.GetExecution(x.ID)
	if got.Script.Source != "echo first" || got.Script.Interpreter[0] != "/bin/sh" {
		t.Errorf("record %+v", got.Script)
	}
}

// A task that declares only other systems shows why it does not run here, and no firing starts it.
func TestScriptDoesNotRunOnAnUndeclaredSystem(t *testing.T) {
	e := newEnv(t)
	v := e.createScript("windows", "  script:\n    command: [/bin/echo]\n    systems: [windows]\n"+
		"  schedule:\n    interval: 1h\n")
	if !hasWarning(v.Warnings, "script_system_not_declared") {
		t.Errorf("task warnings %+v", v.Warnings)
	}
	_, err := e.app.RunTask("windows")
	if ae, ok := err.(*Error); !ok || ae.Code != CodeConflict || ae.Key != "script_system_not_declared" ||
		ae.Params["systems"] != "windows" || ae.Params["system"] != e.app.d.OS {
		t.Errorf("run by hand: %#v", err)
	}
	e.clock.Advance(61 * time.Minute)
	e.app.Tick()
	if xs := e.executions("windows"); len(xs) != 0 {
		t.Errorf("a firing started it: %+v", xs)
	}
	e.createScript("here", fmt.Sprintf("  script:\n    command: [/bin/echo]\n    systems: [%s]\n", e.app.d.OS))
	e.runScript("here", domain.StatusCompleted)
}

// A program not found on this machine is a warning on save, and its execution fails naming it.
func TestMissingInterpreterWarnsAndFails(t *testing.T) {
	e := newEnv(t)
	v := e.createScript("pwsh", "  script:\n    inline:\n      interpreter: [no-such-interpreter-xyz]\n      source: x\n")
	if !hasWarning(v.Warnings, "script_program_not_found") {
		t.Errorf("task warnings %+v", v.Warnings)
	}
	x := e.runScript("pwsh", domain.StatusFailed)
	if x.ErrorKey != "script_program_not_found" || x.ErrorParams["program"] != "no-such-interpreter-xyz" {
		t.Errorf("error %s %v", x.ErrorKey, x.ErrorParams)
	}
	e.createScript("nowhere", "  script:\n    command: [/bin/echo]\n    workingDirectory: /no/such/dir\n")
	if x = e.runScript("nowhere", domain.StatusFailed); x.ErrorKey != "script_working_directory_missing" {
		t.Errorf("missing working directory: %s", x.ErrorKey)
	}
}

// An imported script task is saved disabled, whatever its file says, also over an existing task.
func TestImportedScriptTaskIsDisabled(t *testing.T) {
	e := newEnv(t)
	yaml := scriptYAML("imported", "  enabled: true\n  script:\n    command: [/bin/echo, hi]\n")
	for _, overwrite := range []bool{false, true} {
		v, err := e.app.ImportTask(t.Context(), yaml, overwrite)
		if err != nil {
			t.Fatal(err)
		}
		if v.Enabled || !strings.Contains(v.YAML, "enabled: false") || !hasWarning(issuesToWarnings(v.Validation.Warnings),
			"script_imported_disabled") {
			t.Errorf("overwrite %v: enabled %v, yaml %q", overwrite, v.Enabled, v.YAML)
		}
		if _, err := e.app.RunTask("imported"); err == nil {
			t.Error("an imported script task ran before it was enabled")
		}
	}
	// Any other type keeps its enabled state on import.
	if v, _ := e.app.ImportTask(t.Context(), taskYAML("general", ""), false); !v.Enabled {
		t.Error("a general task was imported disabled")
	}
}

// With the default profile, the restriction nothing enforces is named on the task and the execution.
func TestScriptRestrictionsAreUnenforced(t *testing.T) {
	e := newEnv(t)
	v := e.createScript("confined", "  script:\n    command: [/bin/echo]\n")
	if !hasWarning(v.Warnings, "restriction_script_unenforced") {
		t.Errorf("task warnings %+v", v.Warnings)
	}
	x := e.runScript("confined", domain.StatusCompleted)
	if len(x.Warnings) != 1 || x.Warnings[0].Code != domain.WarningPolicyUnenforced || x.Warnings[0].Subject != "restrictToWorkspace" {
		t.Errorf("execution warnings %+v", x.Warnings)
	}
	if keys := e.eventKeys(x.ID, domain.EventExecutionCompleted); keys[domain.EventPolicyUnenforced] != "restriction_script_unenforced" {
		t.Errorf("events %v", keys)
	}
	if got := scriptRestrictions(domain.Permissions{Profile: domain.ProfileReadOnly, Network: domain.NetworkDeny}); len(got) != 2 {
		t.Errorf("read-only with no network: %+v", got)
	}
}

// A script task is caught up, queued, and notified like any task.
func TestScriptTaskSharesTheScheduler(t *testing.T) {
	e := newEnv(t)
	e.createScript("weekly", "  script:\n    command: [/bin/sh, -c, \"exit 1\"]\n  schedule:\n    interval: 1h\n")
	e.clock.Advance(3 * time.Hour) // the computer slept through two firings
	e.app.Tick()
	var xs []ExecutionSummary
	e.poll(5*time.Second, func() bool { xs = e.executions("weekly"); return len(xs) == 1 && xs[0].Status.IsTerminal() })
	if len(xs) != 1 || xs[0].Trigger != domain.TriggerCatchUp || xs[0].Status != domain.StatusFailed {
		t.Fatalf("catch-up %+v", xs)
	}
	e.idle("weekly")
	found := false
	for _, n := range e.app.Notifications(false) {
		found = found || (n.ExecutionID == xs[0].ID && n.EventType == domain.EventExecutionFailed)
	}
	if !found {
		t.Error("no notice of the failed program")
	}

	dir := t.TempDir()
	e.createScript("busy", fmt.Sprintf("  script:\n    command: [/bin/sh, -c, \"echo $$ > %s/pid; sleep 600\"]\n", dir))
	first, _ := e.app.RunTask("busy")
	waitForPid(t, filepath.Join(dir, "pid"))
	second, _ := e.app.RunTask("busy")
	third, _ := e.app.RunTask("busy")
	if second.Outcome != "queued" || third.Outcome != "coalesced" || third.ExecutionID != second.ExecutionID {
		t.Errorf("overlapping firings %+v %+v", second, third)
	}
	_ = e.app.CancelExecution(second.ExecutionID)
	_ = e.app.CancelExecution(first.ExecutionID)
	e.wait(first.ExecutionID, domain.StatusCancelled)
	e.idle("busy")
}

// A script task goes through every use case a task has, none of which may assume a harness.
func TestScriptTaskThroughEveryUseCase(t *testing.T) {
	e := newEnv(t)
	e.createScript("full", "  script:\n    command: [/bin/echo, hi]\n")
	if list := e.app.ListTasks(); len(list) != 1 || list[0].Harness != "" || list[0].Type != domain.TypeScriptTask {
		t.Errorf("list %+v", list)
	}
	v, err := e.app.GetTask("full")
	if err != nil || strings.Contains(v.YAML, "harness") || strings.Contains(v.YAML, "prompt") {
		t.Fatalf("get: %v %q", err, v.YAML)
	}
	if _, err := e.app.UpdateTask(t.Context(), "full", TaskInput{YAML: strings.Replace(v.YAML, "hi", "there", 1),
		BaseRevision: v.Revision}); err != nil {
		t.Errorf("update: %v", err)
	}
	if _, err := e.app.SetEnabled(t.Context(), "full", false); err != nil {
		t.Errorf("disable: %v", err)
	}
	if _, err := e.app.SetEnabled(t.Context(), "full", true); err != nil {
		t.Errorf("enable: %v", err)
	}
	if cp, err := e.app.DuplicateTask(t.Context(), "full"); err != nil || cp.Enabled {
		t.Errorf("duplicate: %v %+v", err, cp)
	}
	exported, err := e.app.ExportTask("full")
	if err != nil {
		t.Fatal(err)
	}
	if r := e.app.Validate(t.Context(), TaskInput{YAML: string(exported)}); !r.Valid {
		t.Errorf("the exported file does not validate: %+v", r.Errors)
	}
	// The form sends a document as JSON, and the daemon writes YAML with no harness or prompt for it.
	doc := `{"apiVersion":"harmflow/v1alpha1","kind":"Task","metadata":{"id":"formed","name":"Formed"},` +
		`"spec":{"type":"script","script":{"command":["/bin/echo"]}}}`
	created, err := e.app.CreateTask(t.Context(), TaskInput{Document: []byte(doc)})
	if err != nil || strings.Contains(created.YAML, "harness") || strings.Contains(created.YAML, "prompt") {
		t.Errorf("created from the form: %v %q", err, created.YAML)
	}
	e.runScript("full", domain.StatusCompleted)
	_ = e.app.Queue()
	_ = e.app.Stats()
}
