package integration

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

// A Claude Code task cites a user's agent and a user's skill, and its prompt, the copy of another user's agent, asks the
// session to delegate to the one and use the other, each by name, and says nothing else of them. The real Claude Code
// loads both by name from the configuration directory the daemon runs with, where HarmFlow found them, and the session
// follows each one's own instructions: each writes a marker in the workspace with a content only those instructions
// hold. The signed-in Claude Code reads the user's own configuration directory, so the items go there, under names
// unique to the run, and are removed when the test ends.
func TestCitedUserItemsReachARealClaudeCode(t *testing.T) {
	claude := realClaude(t)
	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run := runSuffix()
	reviewer, tester, notes := "reviewer-"+run, "tester-"+run, "review-notes-"+run
	agentMarker, skillMarker := newMarker(work, tester), newMarker(work, notes)
	config := claudeConfigDir(t)
	writeTemp(t, filepath.Join(config, "agents", reviewer+".md"), itemFile(reviewer, "Reviews a change.", citingPrompt(tester, notes)))
	writeTemp(t, filepath.Join(config, "agents", tester+".md"), itemFile(tester, "Tries a change.", agentMarker.instruction()))
	writeTemp(t, filepath.Join(config, "skills", notes, "SKILL.md"), itemFile(notes, "Writes review notes.", skillMarker.instruction()))

	d := startDaemon(t, t.TempDir())
	d.call("PUT", "/harnesses/claude-code", map[string]any{"path": claude}, 200)
	d.call("PATCH", "/settings", map[string]any{"allowedFolders": []any{work}}, 200)
	copied := d.userAgentCopy(reviewer)
	doc := map[string]any{"apiVersion": "harmflow/v1alpha1", "kind": "Task",
		"metadata": map[string]any{"id": "cited-claude-code", "name": "Cited items on Claude Code",
			"description": copied["description"]},
		"spec": map[string]any{
			"type":      "general",
			"prompt":    copied["prompt"],
			"origin":    map[string]any{"agent": reviewer, "hash": copied["hash"]},
			"agents":    []any{tester},
			"skills":    []any{notes},
			"harness":   map[string]any{"preferred": map[string]any{"provider": "claude-code", "model": "haiku", "effort": "low"}},
			"workspace": map[string]any{"path": work},
			"execution": map[string]any{"timeoutMinutes": 5},
		}}
	d.call("POST", "/tasks", map[string]any{"document": doc}, 201)
	id := d.call("POST", "/tasks/cited-claude-code/run", nil, 202)["executionId"].(string)
	// Longer than the task's timeout, so that the execution's own end says why a session ran too long.
	x := d.waitEnd(id, 6*time.Minute)

	if x["status"] != "completed" {
		t.Errorf("execution %s = %v (%v), want completed", id, x["status"], x["errorMessage"])
	}
	agentMarker.check(t)
	skillMarker.check(t)
}

// claudeConfigDir is the configuration directory of the signed-in Claude Code, which the daemon reads too:
// CLAUDE_CONFIG_DIR, or ~/.claude.
func claudeConfigDir(t *testing.T) string {
	t.Helper()
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(home, ".claude")
}

// userAgentCopy is what selecting the user's agent copies into a task, as GET /catalog gives it.
func (d *daemon) userAgentCopy(name string) map[string]any {
	d.t.Helper()
	for _, x := range d.call("GET", "/catalog", nil, 200)["agents"].([]any) {
		if a := x.(map[string]any); a["name"] == name && a["source"] == "user" {
			if c, ok := a["copy"].(map[string]any); ok {
				return c
			}
		}
	}
	d.t.Fatalf("GET /catalog lists no user's agent %s with a copy", name)
	return nil
}

// citingPrompt asks a session to delegate to the agent and to use the skill, each by name, and says nothing of what
// either does.
func citingPrompt(agent, skill string) string {
	return fmt.Sprintf("Delegate to the %s agent, and use the %s skill, each by name. Do what each of them says, and "+
		"nothing else.", agent, skill)
}

// runSuffix is unique to the run and keeps a name an identifier a task can cite. A test that writes items among the
// user's own names them with it, so that none takes the place of one of the user's.
func runSuffix() string { return strconv.FormatInt(time.Now().UnixNano(), 36) }

// itemFile is an agent's or a skill's Markdown file, with the front matter that names and describes it.
func itemFile(name, description, body string) string {
	return fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n%s\n", name, description, body)
}

// writeTemp writes a file, with the folders it lacks, and removes them when the test ends, so that what a test writes
// among the user's own items leaves them as they were.
func writeTemp(t *testing.T, path, content string) {
	t.Helper()
	var made []string // the innermost first
	for dir := filepath.Dir(path); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		if _, err := os.Stat(dir); err == nil {
			break
		}
		made = append(made, dir)
	}
	t.Cleanup(func() {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("removing %s: %v", path, err)
		}
		for _, dir := range made {
			_ = os.Remove(dir) // a folder something else wrote into meanwhile stays
		}
	})
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// marker is a file the instructions of a cited agent or skill tell the session to write in the workspace, with a
// content no other text of the task holds.
type marker struct{ item, path, content string }

func newMarker(work, item string) marker {
	return marker{item: item, path: filepath.Join(work, item+".txt"), content: item + " " + rand.Text()}
}

// instruction is what the item's instructions say: write the marker.
func (m marker) instruction() string {
	return fmt.Sprintf("Write a file at %s that holds this line, exactly, and nothing else:\n\n%s", m.path, m.content)
}

// check fails the test unless the session wrote the marker with its content, which only the item's instructions hold.
func (m marker) check(t *testing.T) {
	t.Helper()
	data, err := os.ReadFile(m.path)
	if err != nil {
		t.Errorf("the session did not follow %s, which the task cites: %v", m.item, err)
		return
	}
	if got := strings.TrimSpace(string(data)); got != m.content {
		t.Errorf("the marker of %s holds %q, want %q", m.item, got, m.content)
	}
}
