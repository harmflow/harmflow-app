package integration

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var (
	// gitHubCredential matches what would give a task a GitHub credential of its own: a variable gh takes a token from,
	// or a token itself.
	gitHubCredential = regexp.MustCompile(`\b(GH_TOKEN|GITHUB_TOKEN|GH_ENTERPRISE_TOKEN|GITHUB_ENTERPRISE_TOKEN)\b|\b(gh[pousr]_|github_pat_)\w+`)
	// namesGh and signInFailure read a summary the session writes in its own words: it names gh, together with a
	// sign-in that failed.
	namesGh       = regexp.MustCompile(`(?i)\bgh\b|GitHub CLI`)
	signInFailure = regexp.MustCompile(`(?i)not (signed|logged) in|signed out|logged out|not authenticated|unauthenticated|no (GitHub )?(credentials?|authentication|sign-in)|auth(entication)? (status )?(check )?(failed|fails)`)
)

// harmflow-app#364: a GitHub task holds no GitHub credential of its own. Its session uses the user's gh, and checks
// that gh is signed in before it looks for work. With gh signed out for this test's daemon alone, the real Claude Code
// running the built-in issue implementer must stop at that check: the execution ends failed and says why, and it
// selects, prepares, and publishes nothing. TestFailedIssueIsRetriedUntilItsLimit passes the same check signed in.
func TestGitHubTaskStopsAtItsCheckWhenGhIsSignedOut(t *testing.T) {
	claude := realClaude(t)
	env := signedOutGhEnv(t.TempDir())
	if out, err := ghAuthStatus(env); err == nil || !strings.Contains(string(out), "not logged in") {
		t.Fatalf("gh auth status with the daemon's environment = %v, %q; want it to fail, not logged in", err, out)
	}
	clone := shallowClone(t)
	home := t.TempDir()
	d := startDaemon(t, home, env...)
	useClaude(d, claude, clone)
	yaml := queueTask(d, clone)
	if c := gitHubCredential.FindString(yaml); c != "" {
		t.Fatalf("queueTask's YAML holds %q, want no GitHub credential", c)
	}
	d.call("POST", "/tasks", map[string]any{"yaml": yaml}, 201)

	id := d.call("POST", "/tasks/queue/run", nil, 202)["executionId"].(string)
	dir := executionDir(d, id)
	// A minute past the task's own timeout, so that the daemon ends a session that never stops.
	if x := d.waitEnd(id, 16*time.Minute); x["status"] != "failed" {
		t.Errorf("execution %s ended %v (%v), want failed\n%s", id, x["status"], x["errorMessage"], runFiles(dir))
	}

	var report struct {
		Outcome string `json:"outcome"`
		Summary string `json:"summary"`
	}
	data, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		t.Fatalf("the session wrote no report: %v\n%s", err, runFiles(dir))
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("report.json: %v\n%s", err, data)
	}
	if report.Outcome != "failed" {
		t.Errorf("report.json's outcome = %q, want failed", report.Outcome)
	}
	if !namesGh.MatchString(report.Summary) || !signInFailure.MatchString(report.Summary) {
		t.Errorf("report.json's summary = %q, want it to say that gh is not signed in", report.Summary)
	}

	if progressHas(t, filepath.Join(dir, "progress.jsonl"), "work_item.selected") {
		t.Errorf("progress.jsonl has a work_item.selected line, want the session to select no work\n%s", runFiles(dir))
	}
	for _, e := range d.call("GET", "/executions/"+id+"/events", nil, 200)["events"].([]any) {
		if e := e.(map[string]any); e["type"] == "workspace.changed" || e["type"] == "pull_request.created" {
			t.Errorf("the execution logged %v, want no worktree and no pull request", e)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "state", "workitems.json")); err == nil {
		if items := readLedger(t, home); len(items) != 0 {
			t.Errorf("the ledger tracks %v for the task, want no work item", items)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
}

// signedOutGhEnv is what this test's daemon applies to the test's environment so that gh reads signed out: config, an
// empty directory, as gh's configuration directory, and none of the variables gh takes a token from. The test's own
// gh auth status applies the same entries, so the check and the run cannot drift apart. Neither signs the user's gh
// out.
func signedOutGhEnv(config string) []string {
	return []string{"GH_CONFIG_DIR=" + config, "GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"}
}

// ghAuthStatus runs gh auth status with env applied to the test's environment, as startDaemon applies it.
func ghAuthStatus(env []string) ([]byte, error) {
	cmd := exec.Command("gh", "auth", "status")
	cmd.Env = daemonEnv(env)
	return cmd.CombinedOutput()
}

// progressHas reports whether the progress file at path has a line of type typ; a file never written has none.
func progressHas(t *testing.T, path, typ string) bool {
	t.Helper()
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var line struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(sc.Bytes(), &line) == nil && line.Type == typ {
			return true
		}
	}
	return false
}
