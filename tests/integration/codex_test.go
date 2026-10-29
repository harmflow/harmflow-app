package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// realCodexEnv opts in to the tests that start the installed, signed-in Codex instead of the stand-in: each starts a
// real session and spends a little of the account's usage, so they run only on request, never in CI.
const realCodexEnv = "HARMFLOW_TEST_CODEX"

// realCodex is the installed codex's path; it skips the test unless realCodexEnv opts in and codex is on the PATH.
func realCodex(t *testing.T) string {
	t.Helper()
	if os.Getenv(realCodexEnv) != "1" {
		t.Skipf("starts a real Codex session: set %s=1 on a machine where codex is signed in", realCodexEnv)
	}
	path, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("codex is not on PATH")
	}
	return path
}

// codexHome is the Codex home of the signed-in codex: CODEX_HOME, or ~/.codex.
func codexHome(t *testing.T) string {
	t.Helper()
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(home, ".codex")
}

// A Codex task cites a user's skill that Codex's skill list returns for the task's workspace, and a user's agent of the
// Codex home, and its prompt asks the session to delegate to the one and use the other, each by name, and says nothing
// else of them. The session's instructions point at each item's file, and the session follows each one's own
// instructions: each writes a marker in the workspace with a content only those instructions hold. The skill is in the
// workspace's .agents/skills. The signed-in Codex reads the user's own Codex home, so the daemon runs with it, and the
// agent goes there, under a name unique to the run, and is removed when the test ends.
func TestCitedUserItemsReachARealCodex(t *testing.T) {
	codex := realCodex(t)
	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run := runSuffix()
	tester, notes := "tester-"+run, "review-notes-"+run
	agentMarker, skillMarker := newMarker(work, tester), newMarker(work, notes)
	home := codexHome(t)
	writeTemp(t, filepath.Join(home, "agents", tester+".md"), itemFile(tester, "Tries a change.", agentMarker.instruction()))
	// The skill list also returns the user's own skills, and a task cites the first of a name, so the workspace's skill
	// is named for the run too.
	writeTemp(t, filepath.Join(work, ".agents", "skills", notes, "SKILL.md"), itemFile(notes, "Writes review notes.", skillMarker.instruction()))

	d := startDaemon(t, t.TempDir(), "CODEX_HOME="+home)
	d.call("PUT", "/harnesses/codex", map[string]any{"path": codex}, 200)
	d.call("PATCH", "/settings", map[string]any{"allowedFolders": []any{work}}, 200)
	doc := map[string]any{"apiVersion": "harmflow/v1alpha1", "kind": "Task",
		"metadata": map[string]any{"id": "cited-codex", "name": "Cited items on Codex"},
		"spec": map[string]any{
			"type":   "general",
			"prompt": citingPrompt(tester, notes),
			"agents": []any{tester},
			"skills": []any{notes},
			// Codex's list of models gives no order to find the smallest one by, and a model named here would be missing
			// from another account's list, so the task runs the model Codex's configuration sets, at the effort low.
			"harness":   map[string]any{"preferred": map[string]any{"provider": "codex", "effort": "low"}},
			"workspace": map[string]any{"path": work},
			"execution": map[string]any{"timeoutMinutes": 5},
		}}
	d.call("POST", "/tasks", map[string]any{"document": doc}, 201)
	id := d.call("POST", "/tasks/cited-codex/run", nil, 202)["executionId"].(string)
	// Longer than the task's timeout, so that the execution's own end says why a session ran too long.
	x := d.waitEnd(id, 6*time.Minute)

	if x["status"] != "completed" {
		t.Errorf("execution %s = %v (%v), want completed", id, x["status"], x["errorMessage"])
	}
	agentMarker.check(t)
	skillMarker.check(t)
}

// A Codex task cites a built-in skill, and its prompt asks the session to use it by name and says nothing else of it.
// The real Codex reads the skill from the copy HarmFlow hands it in the execution's directory and follows it, and the
// user's Codex home keeps its configuration files: the configuration, the agents, and the skills. The signed-in Codex
// reads the user's own Codex home, so the daemon runs with it, and the test compares that one, and writes nothing into
// it.
func TestBuiltinSkillLoadsInARealCodex(t *testing.T) {
	codex := realCodex(t)
	home := codexHome(t)
	builtinSkillLoads(t, realHarness{
		id:   "codex",
		path: codex,
		// The model Codex's configuration sets, at the effort low, as in TestCitedUserItemsReachARealCodex.
		preferred: map[string]any{"provider": "codex", "effort": "low"},
		config:    home,
		files:     []string{"config.toml", "agents", "skills"},
		env:       []string{"CODEX_HOME=" + home},
	})
}
