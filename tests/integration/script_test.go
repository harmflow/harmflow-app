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

// A script program's output reaches the event stream while the program runs: each line arrives as a script.output
// event with its stream, redacted, while the execution still reads running.
func TestScriptOutputStreamsWhileItRuns(t *testing.T) {
	t.Parallel()
	d := startDaemon(t, t.TempDir())
	token := "ghp_" + strings.Repeat("c", 36)
	d.call("POST", "/tasks", map[string]any{"yaml": scriptTask("talking", fmt.Sprintf(`  script:
    inline:
      interpreter: [/bin/sh]
      source: |
        echo one
        echo two
        echo "%s" >&2
        echo three >&2
        sleep 600
  execution:
    cancelGraceSeconds: 1
`, token))}, 201)
	events, closeStream := d.stream(t, 0)
	defer closeStream()
	id := d.call("POST", "/tasks/talking/run", nil, 202)["executionId"].(string)
	streams := map[string][]string{}
	// The wait follows the run's pace: it fails once silence passes with no event of the execution, not at a fixed time
	// from the request.
	const silence = 20 * time.Second
	quiet := time.NewTimer(silence)
	defer quiet.Stop()
	last := "the run request"
	for received := 0; received < 4; {
		select {
		case e, ok := <-events:
			if !ok {
				t.Fatalf("the stream closed; received %d lines: %v", received, streams)
			}
			if e.Data["executionId"] != id {
				continue
			}
			checkFrame(t, e)
			last = e.Type
			quiet.Reset(silence)
			switch e.Type {
			case "script.output":
				payload, _ := e.Data["payload"].(map[string]any)
				stream := fmt.Sprint(payload["stream"])
				streams[stream] = append(streams[stream], fmt.Sprint(e.Data["message"]))
				if strings.Contains(string(e.Raw), "ghp_") {
					t.Errorf("a streamed line carries the token: %s", e.Raw)
				}
				received++
			case "script.exited", "execution.completed", "execution.failed", "execution.cancelled", "execution.interrupted":
				t.Fatalf("%s arrived before the program's four lines; received %v", e.Type, streams)
			}
		case <-quiet.C:
			t.Fatalf("no event of the execution in %v since %s; received %v", silence, last, streams)
		}
	}
	if x := d.call("GET", "/executions/"+id, nil, 200); x["status"] != "running" {
		t.Errorf("GET /executions/%s after the four lines: status %v, want running", id, x["status"])
	}
	// Each stream keeps its own order; across the two streams the order is the one the lines arrived in.
	if got := strings.Join(streams["stdout"], "|"); got != "one|two" {
		t.Errorf("stdout lines %q, want %q", got, "one|two")
	}
	if got := strings.Join(streams["stderr"], "|"); got != "[REDACTED]|three" {
		t.Errorf("stderr lines %q, want %q", got, "[REDACTED]|three")
	}
	if len(streams) != 2 {
		t.Errorf("streams %v, want stdout and stderr", streams)
	}
	d.call("POST", "/executions/"+id+"/cancel", nil, 202)
	d.waitStatus(id, "cancelled")
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
