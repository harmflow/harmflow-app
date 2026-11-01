package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Script tasks run a program of the user's instead of a harness session. These run the real harmflowd and real
// programs, across the daemon's process boundary.

func scriptTask(id, body string) string {
	return fmt.Sprintf(`apiVersion: harmflow/v1alpha1
kind: Task
metadata:
  id: %s
  name: Script %s
spec:
  type: script
%s`, id, id, body)
}

// outputLines waits for the execution's terminal event and returns its program's output, each line with its stream.
func (d *daemon) outputLines(id string) []string {
	d.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		events := d.call("GET", "/executions/"+id+"/events", nil, 200)["events"].([]any)
		var lines []string
		ended := false
		for _, raw := range events {
			e := raw.(map[string]any)
			switch e["type"] {
			case "script.output":
				payload, _ := e["payload"].(map[string]any)
				lines = append(lines, fmt.Sprint(payload["stream"])+": "+fmt.Sprint(e["message"]))
			case "execution.completed", "execution.failed", "execution.cancelled", "execution.interrupted":
				ended = true
			}
		}
		if ended {
			return lines
		}
		time.Sleep(100 * time.Millisecond)
	}
	d.t.Fatalf("execution %s never ended", id)
	return nil
}

func readPid(t *testing.T, path string) int {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if data, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
				return pid
			}
		}
	}
	t.Fatalf("no pid in %s", path)
	return 0
}

func gone(pid int) bool {
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return true
		}
	}
	return false
}

// A script task is created and imported over the API, runs its program with no shell and no harness, ends by its exit
// status, and keeps its output marked and redacted.
func TestScriptTaskOverTheAPI(t *testing.T) {
	t.Parallel()
	d := startDaemon(t, t.TempDir())
	token := "ghp_" + strings.Repeat("b", 36)
	inline := fmt.Sprintf(`  script:
    inline:
      interpreter: [/bin/sh]
      source: |
        echo one
        echo two
        echo "%s" >&2
        echo three >&2
        exit 3
`, token)
	created := d.call("POST", "/tasks", map[string]any{"yaml": scriptTask("audit", inline)}, 201)
	if created["harness"] != "" || created["type"] != "script" {
		t.Fatalf("created %v", created)
	}
	id := d.call("POST", "/tasks/audit/run", nil, 202)["executionId"].(string)
	x := d.waitStatus(id, "failed")
	script, _ := x["script"].(map[string]any)
	if x["errorKey"] != "script_exit_code" || script == nil || script["exitCode"] != float64(3) || x["harness"] != "" {
		t.Errorf("execution %v", x)
	}
	if attempts := x["attempts"].([]any); len(attempts) != 1 || attempts[0].(map[string]any)["harness"] != "" {
		t.Errorf("attempts %v", attempts)
	}
	// Each stream keeps its own order, and the events hold the lines in the raw capture's order. Across the two streams
	// the order is the one they arrived in, which nothing here fixes.
	lines := d.outputLines(id)
	streams := map[string][]string{}
	var order []string
	for _, line := range lines {
		stream, text, _ := strings.Cut(line, ": ")
		streams[stream] = append(streams[stream], text)
		order = append(order, text)
	}
	if strings.Join(streams["stdout"], "|") != "one|two" || strings.Join(streams["stderr"], "|") != "[REDACTED]|three" ||
		len(streams) != 2 {
		t.Errorf("output %q", lines)
	}
	output := d.call("GET", "/executions/"+id+"/output", nil, 200)
	text := output["attempts"].([]any)[0].(map[string]any)["text"].(string)
	if strings.Contains(text, token) || text != strings.Join(order, "\n")+"\n" {
		t.Errorf("raw capture %q, events %q", text, lines)
	}

	d.call("POST", "/tasks", map[string]any{"yaml": scriptTask("echo", "  script:\n    command: [/bin/echo, \"$HOME; echo injected\"]\n")}, 201)
	id = d.call("POST", "/tasks/echo/run", nil, 202)["executionId"].(string)
	d.waitStatus(id, "completed")
	if lines := d.outputLines(id); len(lines) != 1 || lines[0] != "stdout: $HOME; echo injected" {
		t.Errorf("argument list output %q", lines)
	}

	imported := d.call("POST", "/tasks/import", map[string]any{"yaml": scriptTask("imported",
		"  enabled: true\n  script:\n    command: [/bin/echo]\n")}, 201)
	if imported["enabled"] != false {
		t.Errorf("an imported script task is enabled: %v", imported)
	}
	if s, _ := d.request("POST", "/tasks/imported/run", nil, nil); s < 400 {
		t.Errorf("an imported script task ran before it was enabled: %d", s)
	}
}

// Cancelling a script execution ends its program and the child it started in the background.
func TestScriptCancelEndsItsProcesses(t *testing.T) {
	t.Parallel()
	d := startDaemon(t, t.TempDir())
	dir := t.TempDir()
	d.call("POST", "/tasks", map[string]any{"yaml": scriptTask("slow", fmt.Sprintf(
		"  script:\n    command: [/bin/sh, -c, \"sleep 600 & echo $! > %s/child; sleep 600\"]\n  execution:\n    cancelGraceSeconds: 1\n",
		dir))}, 201)
	id := d.call("POST", "/tasks/slow/run", nil, 202)["executionId"].(string)
	d.waitStatus(id, "running")
	child := readPid(t, filepath.Join(dir, "child"))
	d.call("POST", "/executions/"+id+"/cancel", nil, 202)
	d.waitStatus(id, "cancelled")
	if !gone(child) {
		t.Errorf("the program's child %d survived the cancellation", child)
	}
}

// A program that exits on its own with code 0 leaves no process behind: the daemon ends the child it started in the
// background before the execution ends, and the execution keeps the result the exit status gives. Where the daemon
// reads the process table, the log names the child it ended. The program writes the child's pid only once the child
// runs sleep, so the name the log gives it is not that of the shell the child is until it execs.
func TestScriptExitEndsWhatTheProgramLeft(t *testing.T) {
	t.Parallel()
	d := startDaemon(t, t.TempDir())
	dir := t.TempDir()
	d.call("POST", "/tasks", map[string]any{"yaml": scriptTask("leaves", fmt.Sprintf(`  script:
    command: [/bin/sh, -c, "sleep 600 >/dev/null 2>&1 & c=$!; i=0; while [ $i -lt 50 ]; do case $(ps -o ucomm= -p $c) in sleep*) break;; esac; sleep 0.05; i=$((i+1)); done; echo $c > %s/child"]
  execution:
    cancelGraceSeconds: 1
`, dir))}, 201)
	id := d.call("POST", "/tasks/leaves/run", nil, 202)["executionId"].(string)
	x := d.waitStatus(id, "completed", "failed", "cancelled", "interrupted")
	child := readPid(t, filepath.Join(dir, "child"))
	t.Cleanup(func() {
		if !gone(child) {
			_ = syscall.Kill(child, syscall.SIGKILL)
		}
	})
	if x["status"] != "completed" {
		t.Fatalf("status %v, want completed; error %v", x["status"], x["errorMessage"])
	}
	if !gone(child) {
		t.Errorf("the background child %d survived the program's exit", child)
	}
	attempts := x["attempts"].([]any)
	if len(attempts) != 1 {
		t.Fatalf("attempts %v, want one", attempts)
	}
	if a := attempts[0].(map[string]any); a["outcome"] != "succeeded" || a["exitCode"] != float64(0) {
		t.Errorf("attempt %v, want succeeded with exit code 0", a)
	}
	events := d.waitEvent(id, "execution.completed")["events"].([]any)
	// Without the process table the daemon ends the program's group alone, which names no process.
	t.Run("the log names the child", func(t *testing.T) {
		needProcessTable(t)
		named := false
		for _, raw := range events {
			ev := raw.(map[string]any)
			p, _ := ev["payload"].(map[string]any)
			named = named || ev["type"] == "process.ended" && ev["messageKey"] == "process_left_ended" &&
				p["pid"] == float64(child) && p["executable"] == "sleep"
		}
		if !named {
			t.Errorf("the log does not name the background child %d as process_left_ended", child)
		}
	})
}

// A script task on a one-minute interval with the queue policy keeps one waiting firing while its program runs, on the
// real daemon's own firings, and that firing starts once the running execution has ended on its own. The program is
// held by a file the test removes once the second firing has coalesced, so the case takes a little over two minutes
// from its first firing.
func TestScriptScheduledFiringsQueueOverTheAPI(t *testing.T) {
	t.Parallel()
	d := startDaemon(t, t.TempDir())
	hold := filepath.Join(t.TempDir(), "hold")
	if err := os.WriteFile(hold, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	d.call("POST", "/tasks", map[string]any{"yaml": scriptTask("every-minute", fmt.Sprintf(
		"  script:\n    command: [/bin/sh, -c, \"while [ -e %s ]; do sleep 0.2; done\"]\n"+
			"  schedule:\n    interval: 1m\n  concurrency:\n    overlappingRuns: queue\n", hold))}, 201)
	// until polls the task's executions over the API until cond holds of them, for up to a firing and a half beyond
	// budget: the scheduler ticks every few seconds, and a loaded host stretches each step.
	until := func(budget time.Duration, what string, cond func([]map[string]any) bool) []map[string]any {
		t.Helper()
		deadline := time.Now().Add(budget + 90*time.Second)
		for {
			runs := d.list("/executions?taskId=every-minute")
			if cond(runs) {
				return runs
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: executions %v", what, runs)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	// The first firing comes at most a minute after the save, and its program runs until the file goes.
	runs := until(time.Minute, "the first firing never started", func(runs []map[string]any) bool {
		return len(runs) == 1 && runs[0]["status"] == "running"
	})
	if runs[0]["trigger"] != "schedule" {
		t.Fatalf("the first execution: %v", runs[0])
	}
	first := runs[0]["id"].(string)
	// The next firing waits; the one after coalesces into it, which the waiting execution's log records.
	runs = until(time.Minute, "the second firing never waited", func(runs []map[string]any) bool { return len(runs) == 2 })
	var waiting string
	for _, r := range runs {
		if r["id"] != first {
			waiting = r["id"].(string)
			if r["status"] != "queued" || r["trigger"] != "schedule" {
				t.Fatalf("the second execution: %v, want queued on the schedule", r)
			}
		}
	}
	coalesced := func() bool {
		for _, raw := range d.call("GET", "/executions/"+waiting+"/events", nil, 200)["events"].([]any) {
			e := raw.(map[string]any)
			if payload, _ := e["payload"].(map[string]any); e["type"] == "firing.coalesced" && payload["trigger"] == "schedule" {
				return true
			}
		}
		return false
	}
	runs = until(time.Minute, "the third firing never coalesced", func(runs []map[string]any) bool {
		return len(runs) != 2 || coalesced()
	})
	if len(runs) != 2 {
		t.Fatalf("executions after the third firing: %v, want the running one and one waiting", runs)
	}
	for _, r := range runs {
		want := "queued"
		if r["id"] == first {
			want = "running"
		}
		if r["status"] != want {
			t.Errorf("execution %v is %v after the third firing, want %s", r["id"], r["status"], want)
		}
	}

	// The program ends on its own, with exit code 0, and the waiting execution runs it after.
	if err := os.Remove(hold); err != nil {
		t.Fatal(err)
	}
	ended, started := d.waitStatus(first, "completed"), d.waitStatus(waiting, "completed")
	for _, x := range []map[string]any{ended, started} {
		if script, _ := x["script"].(map[string]any); script == nil || script["exitCode"] != float64(0) {
			t.Errorf("execution %v: script %v, want exit code 0", x["id"], x["script"])
		}
	}
	// Event IDs increase across every execution's log, so the waiting execution's start follows the first one's end.
	eventID := func(id, eventType string) float64 {
		for _, raw := range d.call("GET", "/executions/"+id+"/events", nil, 200)["events"].([]any) {
			if e := raw.(map[string]any); e["type"] == eventType {
				return e["id"].(float64)
			}
		}
		return 0
	}
	if end, start := eventID(first, "execution.completed"), eventID(waiting, "execution.started"); end == 0 || start <= end {
		t.Errorf("the first execution ended at event %.0f and the waiting one started at %.0f, want it to start after",
			end, start)
	}
	if runs := d.list("/executions?taskId=every-minute"); len(runs) != 2 {
		t.Errorf("executions at the end: %v, want 2", runs)
	}
}

// A daemon killed while a script runs leaves the program running in its own group; started again, it marks the
// execution interrupted and ends what the program left.
func TestRestartEndsWhatTheScriptLeft(t *testing.T) {
	t.Parallel()
	home, dir := t.TempDir(), t.TempDir()
	d := startDaemon(t, home)
	d.call("POST", "/tasks", map[string]any{"yaml": scriptTask("left", fmt.Sprintf(
		"  script:\n    command: [/bin/sh, -c, \"echo $$ > %[1]s/leader; sleep 600 & echo $! > %[1]s/child; sleep 600\"]\n", dir))}, 201)
	id := d.call("POST", "/tasks/left/run", nil, 202)["executionId"].(string)
	d.waitStatus(id, "running")
	leader, child := readPid(t, filepath.Join(dir, "leader")), readPid(t, filepath.Join(dir, "child"))
	time.Sleep(1500 * time.Millisecond) // let the record, with the program's process group, reach the disk
	d.kill()
	if gone(leader) {
		t.Fatal("the program ended with the daemon; nothing is left to end")
	}
	d2 := startDaemon(t, home)
	x := d2.call("GET", "/executions/"+id, nil, 200)
	if x["status"] != "interrupted" {
		t.Fatalf("status %v", x["status"])
	}
	if !gone(leader) || !gone(child) {
		t.Errorf("after the restart, the program (%d) or its child (%d) still runs", leader, child)
	}
	found := false
	for _, raw := range x["warnings"].([]any) {
		found = found || raw.(map[string]any)["messageKey"] == "script_leftovers_ended"
	}
	if !found {
		t.Errorf("warnings %v", x["warnings"])
	}
}
