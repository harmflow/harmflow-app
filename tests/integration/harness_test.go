package integration

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// standInClaude copies the fake-claude stand-in into a temporary directory with one line replaced, so a test changes
// how one probe answers and keeps the rest.
func standInClaude(t *testing.T, line, replacement string) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(repoRoot(), "tests", "integration", "testdata", "fake-claude"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(src, []byte(line)) {
		t.Fatalf("fake-claude has no line %q", line)
	}
	path := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(path, bytes.Replace(src, []byte(line), []byte(replacement), 1), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// harness is the harness's entry in GET /harnesses.
func (d *daemon) harness(id string) map[string]any {
	d.t.Helper()
	for _, h := range d.list("/harnesses") {
		if h["id"] == id {
			return h
		}
	}
	d.t.Fatalf("GET /harnesses has no %s", id)
	return nil
}

// FS-004 FR-006 (harmflow-app#71): a sign-in the probe could not establish reads unknown, not signed out. The harness is
// ready with a diagnostic, and a task on it starts, so the first execution proves the sign-in.
func TestUnknownSignInIsReady(t *testing.T) {
	d := startDaemon(t, t.TempDir())
	path := standInClaude(t, `auth) echo '{"loggedIn": true}'; exit 0 ;;`,
		`auth) echo "Error: could not reach the credential store" >&2; exit 1 ;;`)
	d.call("PUT", "/harnesses/claude-code", map[string]any{"path": path}, 200)
	h := d.harness("claude-code")
	if h["readiness"] != "ready" || h["authentication"] != "unknown" || h["diagnosticKey"] != "authentication_unverified" ||
		h["diagnostic"] == nil {
		t.Fatalf("GET /harnesses: readiness %v, authentication %v, diagnostic %v (%v)", h["readiness"],
			h["authentication"], h["diagnosticKey"], h["diagnostic"])
	}
	d.call("POST", "/tasks", map[string]any{"yaml": task("signin", "Say hi.", "")}, 201)
	run := d.call("POST", "/tasks/signin/run", nil, 202)
	x := d.waitStatus(run["executionId"].(string), "completed", "failed")
	attempts, _ := x["attempts"].([]any)
	if x["status"] != "completed" || len(attempts) != 1 || attempts[0].(map[string]any)["harness"] != "claude-code" {
		t.Errorf("status %v, attempts %v, error %v", x["status"], attempts, x["errorMessage"])
	}
}

// FS-004 FR-008 (harmflow-app#71): a version the probe could not read is unknown, never supported. The harness runs,
// and each execution records that its version is unknown, as one on an untested newer version does.
func TestUnknownVersionRunsWithAWarning(t *testing.T) {
	d := startDaemon(t, t.TempDir())
	path := standInClaude(t, `--version) echo "2.1.239 (Claude Code)"; exit 0 ;;`,
		`--version) echo "Claude Code (dev build)"; exit 0 ;;`)
	d.call("PUT", "/harnesses/claude-code", map[string]any{"path": path}, 200)
	h := d.harness("claude-code")
	if h["readiness"] != "ready" || h["versionSupport"] != "unknown" || h["version"] != nil {
		t.Fatalf("GET /harnesses: readiness %v, version %v, versionSupport %v", h["readiness"], h["version"],
			h["versionSupport"])
	}
	d.call("POST", "/tasks", map[string]any{"yaml": task("noversion", "Say hi.", "")}, 201)
	for i := 0; i < 2; i++ {
		run := d.call("POST", "/tasks/noversion/run", nil, 202)
		id := run["executionId"].(string)
		x := d.waitStatus(id, "completed", "failed")
		if x["status"] != "completed" {
			t.Fatalf("execution %d: status %v, error %v", i+1, x["status"], x["errorMessage"])
		}
		warned := false
		for _, w := range x["warnings"].([]any) {
			w := w.(map[string]any)
			warned = warned || (w["code"] == "version_untested" && w["messageKey"] == "harness_version_unknown" &&
				w["subject"] == "claude-code")
		}
		if !warned {
			t.Errorf("execution %d records no unknown-version warning: %v", i+1, x["warnings"])
		}
		logged := false
		for _, e := range d.call("GET", "/executions/"+id+"/events", nil, 200)["events"].([]any) {
			e := e.(map[string]any)
			logged = logged || (e["type"] == "harness.version_untested" && e["messageKey"] == "harness_version_unknown")
		}
		if !logged {
			t.Errorf("execution %d logs no unknown-version event", i+1)
		}
	}
}
