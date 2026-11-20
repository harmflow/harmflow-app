package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// geminiTask is a task whose preferred harness is the Gemini CLI.
func geminiTask(id, prompt, extra string) string {
	return strings.Replace(task(id, prompt, extra), "provider: claude-code", "provider: gemini-cli", 1)
}

// startWithGeminiSignedIn starts a daemon whose stand-in Gemini CLI is signed in through its own store.
func startWithGeminiSignedIn(t *testing.T) *daemon {
	t.Helper()
	home := t.TempDir()
	store := filepath.Join(home, "gemini", ".gemini")
	if err := os.MkdirAll(store, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, "oauth_creds.json"), []byte(`{"stand-in":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return startDaemon(t, home)
}

// FS-004 AC-026 and AC-027, FS-008 AC-057: a task on gemini-cli runs to completion on the stand-in, unattended in its
// workspace, and its log holds the stream's events as the event types the other harnesses produce.
func TestGeminiCLITaskRunsToCompletion(t *testing.T) {
	t.Parallel()
	d := startWithGeminiSignedIn(t)
	for _, h := range d.list("/harnesses") {
		if h["id"] == "gemini-cli" && h["readiness"] != "ready" {
			t.Fatalf("the signed-in stand-in reads %v: %v", h["readiness"], h["diagnostic"])
		}
	}
	d.call("POST", "/tasks", map[string]any{"yaml": geminiTask("gemini", "GEMINI=ok", "")}, 201)
	run := d.call("POST", "/tasks/gemini/run", nil, 202)
	id := run["executionId"].(string)
	x := d.waitStatus(id, "completed", "failed")
	attempts := x["attempts"].([]any)
	if x["status"] != "completed" || len(attempts) != 1 {
		t.Fatalf("status %v attempts %v error %v", x["status"], attempts, x["errorMessage"])
	}
	if a := attempts[0].(map[string]any); a["harness"] != "gemini-cli" || a["model"] != "gemini-2.5-pro" {
		t.Errorf("attempt %v", a)
	}
	args, err := os.ReadFile(filepath.Join(d.home, "workspace", "gemini", "gemini-args"))
	if err != nil || !strings.Contains(string(args), "stream-json") || !strings.Contains(string(args), "--approval-mode") {
		t.Errorf("the stand-in was not started in the workspace, unattended: %q %v", args, err)
	}
	events, _ := d.storedLog(t, id)
	seen := map[string]bool{}
	for _, e := range events {
		seen[e.Type] = true
	}
	for _, want := range []string{"harness.started", "harness.message", "tool.started", "tool.completed"} {
		if !seen[want] {
			t.Errorf("the log lacks %s: %v", want, seen)
		}
	}
}

// FS-004 AC-026: with no sign-in in its own store, the Gemini CLI reads not ready, an API key in the environment
// notwithstanding.
func TestGeminiCLISignedOutDespiteAnAPIKey(t *testing.T) {
	t.Parallel()
	d := startDaemon(t, t.TempDir(), "GEMINI_API_KEY=stand-in-key")
	for _, h := range d.list("/harnesses") {
		if h["id"] != "gemini-cli" {
			continue
		}
		if h["readiness"] != "not_ready" || h["authentication"] != "invalid" {
			t.Fatalf("got readiness %v authentication %v", h["readiness"], h["authentication"])
		}
		return
	}
	t.Fatal("the harness list has no gemini-cli")
}

// FS-005 AC-018: the Gemini CLI's failures are classified as the others' are; quota and a sign-in fail over to Claude
// Code, a network failure does not, and a task whose Claude Code runs out of quota fails over to the Gemini CLI.
func TestGeminiCLIFailover(t *testing.T) {
	t.Parallel()
	d := startWithGeminiSignedIn(t)
	fallback := "    fallback:\n      candidates:\n        - provider: claude-code\n"
	cases := []struct {
		scenario, status, outcome string
		attempts                  int
	}{
		{"quota", "completed", "quota_exhausted", 2},
		{"signedout", "completed", "harness_unavailable", 2},
		{"network", "failed", "network_failed", 1},
	}
	for _, c := range cases {
		id := "gemini-" + c.scenario
		d.call("POST", "/tasks", map[string]any{"yaml": geminiTask(id, "GEMINI="+c.scenario, fallback)}, 201)
		run := d.call("POST", "/tasks/"+id+"/run", nil, 202)
		x := d.waitStatus(run["executionId"].(string), "completed", "failed")
		attempts := x["attempts"].([]any)
		first := attempts[0].(map[string]any)
		if x["status"] != c.status || len(attempts) != c.attempts || first["harness"] != "gemini-cli" || first["outcome"] != c.outcome {
			t.Errorf("%s: status %v, attempts %v", c.scenario, x["status"], attempts)
			continue
		}
		if c.attempts == 2 {
			if a2 := attempts[1].(map[string]any); a2["harness"] != "claude-code" || a2["outcome"] != "succeeded" {
				t.Errorf("%s: the fallback attempt %v", c.scenario, a2)
			}
		}
	}

	d.call("POST", "/tasks", map[string]any{"yaml": task("to-gemini", "SCENARIO=quota",
		"    fallback:\n      candidates:\n        - provider: gemini-cli\n")}, 201)
	run := d.call("POST", "/tasks/to-gemini/run", nil, 202)
	x := d.waitStatus(run["executionId"].(string), "completed", "failed")
	attempts := x["attempts"].([]any)
	if x["status"] != "completed" || len(attempts) != 2 {
		t.Fatalf("to the Gemini CLI: status %v attempts %v error %v", x["status"], attempts, x["errorMessage"])
	}
	if a2 := attempts[1].(map[string]any); a2["harness"] != "gemini-cli" || a2["outcome"] != "succeeded" {
		t.Errorf("to the Gemini CLI: %v", a2)
	}
}
