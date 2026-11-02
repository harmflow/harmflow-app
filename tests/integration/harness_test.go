package integration

import (
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// standInClaude copies the fake-claude stand-in into a temporary directory with one line replaced, so a test changes
// how one probe answers and keeps the rest.
func standInClaude(t *testing.T, line, replacement string) string {
	t.Helper()
	return standIn(t, "fake-claude", "claude", line, replacement).path
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

// redetect asks the daemon to detect every harness again, as Detect again does, and returns once each harness has
// announced its new check: POST /harnesses/refresh answers at once, before the detection ends.
func (d *daemon) redetect() {
	d.t.Helper()
	events, closeStream := d.stream(d.t, 0)
	defer closeStream()
	// The stream replays what the daemon announced before; a check counts only once the request was made.
	var before int64
	for drained := false; !drained; {
		select {
		case e := <-events:
			before = max(before, e.ID)
		case <-time.After(300 * time.Millisecond):
			drained = true
		}
	}
	pending := map[string]bool{}
	for _, h := range d.list("/harnesses") {
		pending[fmt.Sprint(h["id"])] = true
	}
	if status, body := d.request("POST", "/harnesses/refresh", nil, nil); status != 200 {
		d.t.Fatalf("POST /harnesses/refresh: %d %s", status, body)
	}
	timeout := time.After(2 * time.Minute)
	for len(pending) > 0 {
		select {
		case e, ok := <-events:
			if !ok {
				d.t.Fatalf("the event stream ended with no harness.checked for %v", slices.Sorted(maps.Keys(pending)))
			}
			if e.Type == "harness.checked" && e.ID > before {
				delete(pending, fmt.Sprint(e.Data["harness"]))
			}
		case <-timeout:
			d.t.Fatalf("2 minutes after POST /harnesses/refresh, no harness.checked for %v", slices.Sorted(maps.Keys(pending)))
		}
	}
}

// harmflow-app#669: Detect again answers at once, and its detection lands after the request ended. Codex's sign-in
// check takes 4 s, longer than the client waits, as a loaded host makes the desktop's 30-second limit too short; the
// request still succeeds, and Codex's new check is stored and announced once it ends.
func TestDetectAgainLandsAfterItsRequestEnded(t *testing.T) {
	t.Parallel()
	d := startDaemon(t, t.TempDir())
	slow := filepath.Join(t.TempDir(), "slow")
	codex := standIn(t, "fake-codex", "codex", `  login) echo "Logged in using ChatGPT"; exit 0 ;;`,
		`  login) [ -e '`+slow+`' ] && sleep 4; echo "Logged in using ChatGPT"; exit 0 ;;`)
	d.call("PUT", "/harnesses/codex", map[string]any{"path": codex.path}, 200)
	before := fmt.Sprint(d.harness("codex")["lastCheckedAt"])
	if err := os.WriteFile(slow, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	events, closeStream := d.stream(t, 0)
	defer closeStream()
	var seen int64
	for drained := false; !drained; {
		select {
		case e := <-events:
			seen = max(seen, e.ID)
		case <-time.After(300 * time.Millisecond):
			drained = true
		}
	}

	req, _ := http.NewRequest("POST", d.base+"/harnesses/refresh", nil)
	req.Header.Set("Authorization", "Bearer "+d.token)
	start := time.Now()
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("POST /harnesses/refresh with a 2-second client limit: %v, after %v; want an answer at once", err, time.Since(start))
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("POST /harnesses/refresh: %d, want 200", resp.StatusCode)
	}

	timeout := time.After(2 * time.Minute)
	for checked := false; !checked; {
		select {
		case e, ok := <-events:
			if !ok {
				t.Fatal("the event stream ended with no harness.checked for codex")
			}
			checked = e.Type == "harness.checked" && e.ID > seen && e.Data["harness"] == "codex"
		case <-timeout:
			t.Fatal("2 minutes after POST /harnesses/refresh, no harness.checked for codex")
		}
	}
	if h := d.harness("codex"); h["readiness"] != "ready" || fmt.Sprint(h["lastCheckedAt"]) == before {
		t.Errorf("Codex after the detection: readiness %v, last checked %v (%v before); want ready, checked again",
			h["readiness"], h["lastCheckedAt"], before)
	}
}

// harmflow-app#71: a sign-in the probe could not establish reads unknown, not signed out. The harness is ready with a
// diagnostic, and a task on it starts, so the first execution proves the sign-in.
func TestUnknownSignInIsReady(t *testing.T) {
	t.Parallel()
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

// harmflow-app#71: a version the probe could not read is unknown, never supported. The harness runs, and each execution
// records that its version is unknown, as one on an untested newer version does.
func TestUnknownVersionRunsWithAWarning(t *testing.T) {
	t.Parallel()
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
