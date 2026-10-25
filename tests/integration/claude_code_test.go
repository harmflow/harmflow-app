package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// realClaudeEnv opts in to the tests that start the installed, signed-in Claude Code instead of the stand-in: each
// starts a real session and spends a little of the account's usage, so they run only on request, never in CI.
const realClaudeEnv = "HARMFLOW_TEST_CLAUDE_CODE"

// realClaude is the installed claude's path; it skips the test unless realClaudeEnv opts in and claude is on the PATH.
func realClaude(t *testing.T) string {
	t.Helper()
	if os.Getenv(realClaudeEnv) != "1" {
		t.Skipf("starts a real Claude Code session: set %s=1 on a machine where claude is signed in", realClaudeEnv)
	}
	path, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude is not on PATH")
	}
	return path
}

// waitEnd waits until an execution has ended, for as long as a real session may take.
func (d *daemon) waitEnd(id string, timeout time.Duration) map[string]any {
	d.t.Helper()
	var x map[string]any
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(time.Second) {
		if x = d.call("GET", "/executions/"+id, nil, 200); x["endedAt"] != nil {
			return x
		}
	}
	d.t.Fatalf("execution %s still %v after %s", id, x["status"], timeout)
	return nil
}

// A task's permissions become Claude Code's own. In a real headless session, a command the task allows runs, while make
// stays refused although a rule allows it: Claude Code asks for an approval no one can give. Tasks that build with make
// run its recipes command by command instead; this test fails the day Claude Code runs make, so they can call it again.
func TestClaudeCodeRefusesMakeInATask(t *testing.T) {
	claude := realClaude(t)
	d := startDaemon(t, t.TempDir())
	d.call("PUT", "/harnesses/claude-code", map[string]any{"path": claude}, 200)
	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d.call("PATCH", "/settings", map[string]any{"allowedFolders": []any{work}}, 200)
	if err := os.WriteFile(filepath.Join(work, "Makefile"), []byte("stamp:\n\techo made > stamp.txt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prompt := "Run these two commands with the Bash tool, each exactly as written, one per call, and nothing else: " +
		"`touch control.txt`, then `make stamp`. Then say what each one did."
	extra := fmt.Sprintf(`      model: haiku
      effort: low
  workspace:
    path: %s
  permissions:
    profile: custom
    custom:
      allowedTools: ["Bash(touch:*)", "Bash(make:*)"]
  execution:
    timeoutMinutes: 5
`, work)
	d.call("POST", "/tasks", map[string]any{"yaml": task("claude-make", prompt, extra)}, 201)
	id := d.call("POST", "/tasks/claude-make/run", nil, 202)["executionId"].(string)
	x := d.waitEnd(id, 4*time.Minute)

	if _, err := os.Stat(filepath.Join(work, "control.txt")); err != nil {
		t.Fatalf("the session did not run the command its permissions allow, so it says nothing about make: "+
			"execution %v, %v", x["status"], x["errorMessage"])
	}
	if _, err := os.Stat(filepath.Join(work, "stamp.txt")); err == nil {
		t.Errorf("Claude Code ran make in a headless session: the refusal this test pins down is gone, and tasks may " +
			"call make again instead of running its recipes")
	}
}
