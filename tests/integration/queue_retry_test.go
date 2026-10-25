package integration

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The queue's fixtures live in harmflow/harmflow-sandbox, a private repository kept for tests that need a real one, so
// they stay out of this repository's tracker: three open issues labelled queue-test, the oldest of them linked by an
// unmanaged draft pull request. A session is stopped as soon as the daemon has tracked the issue it
// selects, and its clone has no push URL, so no branch of its reaches GitHub and no pull request can come from one. A
// session that once outran the stop pushed a branch and opened a pull request for a fixture, which then counted as
// linked.
const (
	queueRepo  = "harmflow/harmflow-sandbox"
	queueLabel = "queue-test"
)

// An issue whose processing failed before any pull request linked it — here, the daemon killed mid-run — comes back
// as work to retry in the next firing, and no longer once its retry limit is spent. An issue a pull request links is
// never selected. The real Claude Code runs the built-in skills of this checkout.
func TestFailedIssueIsRetriedUntilItsLimit(t *testing.T) {
	claude := realClaude(t)
	linked, failing, untouched := queueFixtures(t)
	clone := shallowClone(t)
	home := t.TempDir()
	d := startDaemon(t, home)
	useClaude(d, claude, clone)
	d.call("POST", "/tasks", map[string]any{"yaml": queueTask(clone)}, 201)

	firings := []struct {
		want   int
		reason string
	}{
		{failing, "the first firing must skip the linked issue and take the oldest one left"},
		{failing, "the issue that failed, with no pull request linking it, must come back to the queue"},
		{untouched, "past its retry limit, the failed issue must stay out, and only the new one is left"},
	}
	for i, f := range firings {
		id := d.call("POST", "/tasks/queue/run", nil, 202)["executionId"].(string)
		dir := executionDir(d, id)
		got := selectedIssue(t, d, id, dir)
		waitTracked(t, d, home, id, dir, got)
		d = restartMidRun(t, d, home, claude, clone)
		if x := d.call("GET", "/executions/"+id, nil, 200); x["status"] != "interrupted" {
			t.Errorf("firing %d after the restart: status %v, want interrupted", i+1, x["status"])
		}
		if got == linked {
			t.Fatalf("firing %d selected #%d, which pull request links: it is already processed", i+1, linked)
		}
		if got != f.want {
			ledger, _ := os.ReadFile(filepath.Join(home, "state", "workitems.json"))
			t.Fatalf("firing %d selected #%d, want #%d: %s\ncontext.json lists #%d as %v\n%sledger: %s", i+1, got, f.want,
				f.reason, got, contextItem(t, dir, got), runFiles(dir), ledger)
		}
		if i == 1 {
			// What the daemon handed the session that retried: the item failed once, with no pull request.
			item := contextItem(t, dir, failing)
			if item["status"] != "failed" || item["failures"] != 1.0 || item["pullRequestUrl"] != nil {
				t.Errorf("context.json of the retry lists #%d as %v", failing, item)
			}
		}
	}

	ledger := readLedger(t, home)
	for _, c := range []struct {
		issue    int
		status   string
		failures int
	}{
		{failing, "needs_attention", 2},
		{untouched, "failed", 1},
	} {
		w, ok := ledger[issueKey(c.issue)]
		if !ok || w.Status != c.status || w.Failures != c.failures {
			t.Errorf("the ledger's #%d = %+v (present %v), want %s with %d failures", c.issue, w, ok, c.status, c.failures)
		}
	}
	if w, ok := ledger[issueKey(linked)]; ok {
		t.Errorf("the ledger tracks #%d, which a pull request links: %+v", linked, w)
	}
}

// queueFixtures reads the fixtures from GitHub and fails the test when they are not as it expects: the oldest open
// issue labelled queueLabel is linked by a pull request, and the two after it by none.
func queueFixtures(t *testing.T) (linked, failing, untouched int) {
	t.Helper()
	out, err := exec.Command("gh", "issue", "list", "--repo", queueRepo, "--state", "open", "--label", queueLabel,
		"--json", "number,closedByPullRequestsReferences").Output()
	if err != nil {
		t.Fatalf("gh issue list: %v", err)
	}
	var issues []struct {
		Number int   `json:"number"`
		Linked []any `json:"closedByPullRequestsReferences"`
	}
	if err := json.Unmarshal(out, &issues); err != nil {
		t.Fatal(err)
	}
	if len(issues) != 3 {
		t.Fatalf("%s has %d open issues labelled %s, want 3", queueRepo, len(issues), queueLabel)
	}
	// gh lists the newest first.
	o, f, u := issues[2], issues[1], issues[0]
	if len(o.Linked) == 0 || len(f.Linked) != 0 || len(u.Linked) != 0 {
		t.Fatalf("pull requests linking #%d, #%d, #%d: %d, %d, %d, want 1 or more, 0, 0", o.Number, f.Number, u.Number,
			len(o.Linked), len(f.Linked), len(u.Linked))
	}
	return o.Number, f.Number, u.Number
}

// shallowClone is the repository the task targets: a local path it must have, which the session never changes.
func shallowClone(t *testing.T) string {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, filepath.Base(queueRepo))
	if out, err := exec.Command("git", "clone", "--quiet", "--depth", "1", "https://github.com/"+queueRepo, dir).CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", dir, "remote", "set-url", "--push", "origin", "no-push://queue-test").CombinedOutput(); err != nil {
		t.Fatalf("git remote set-url --push: %v\n%s", err, out)
	}
	return dir
}

func useClaude(d *daemon, claude, clone string) {
	d.t.Helper()
	d.call("PUT", "/harnesses/claude-code", map[string]any{"path": claude}, 200)
	d.call("PATCH", "/settings", map[string]any{"allowedFolders": []any{filepath.Dir(clone)}}, 200)
}

func queueTask(clone string) string {
	return fmt.Sprintf(`apiVersion: harmflow/v1alpha1
kind: Task
metadata:
  id: queue
  name: Queue retry
spec:
  type: github-issue-implementation
  prompt: |
    Use the skill hf-github-issue-implementer.
    Work only on open issues of %[1]s labelled %[2]s; every other issue is excluded.
  skills: [hf-github-issue-implementer, hf-github-issue-queue, hf-github-worktree, hf-github-draft-pr]
  harness:
    preferred:
      provider: claude-code
      model: sonnet
      effort: low
  permissions:
    profile: custom
    network: allow
    custom:
      # Enough to build the queue and report. gh api can also write, which only the early stop guards against; a push
      # fails on the clone's missing push URL.
      allowedTools: [Read, Glob, Grep, Write, Edit, Skill, TodoWrite, "Bash(echo:*)", "Bash(cat:*)", "Bash(jq:*)",
        "Bash(date:*)", "Bash(git --version)", "Bash(git fetch:*)", "Bash(gh auth status:*)",
        "Bash(gh issue list:*)", "Bash(gh issue view:*)", "Bash(gh pr list:*)", "Bash(gh pr view:*)", "Bash(gh api:*)"]
  execution:
    timeoutMinutes: 15
  githubIssueImplementation:
    repositories:
      - name: %[1]s
        localPath: %[3]s
    queue:
      ordering: oldest_first
      maxItemsPerCycle: 1
      resumeStrategy: resume_first
      maxFailedRetries: 1
`, queueRepo, queueLabel, clone)
}

func executionDir(d *daemon, id string) string {
	d.t.Helper()
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if dir, _ := d.call("GET", "/executions/"+id, nil, 200)["executionDir"].(string); dir != "" {
			return dir
		}
	}
	d.t.Fatalf("execution %s has no execution directory after a minute", id)
	return ""
}

// selectedIssue waits until the session writes its work_item.selected line, and returns the issue's number.
func selectedIssue(t *testing.T, d *daemon, id, dir string) int {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Minute); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if n := selectedInProgress(t, filepath.Join(dir, "progress.jsonl")); n != 0 {
			return n
		}
		if x := d.call("GET", "/executions/"+id, nil, 200); x["endedAt"] != nil {
			t.Fatalf("execution %s ended %v before it selected an issue: %v\n%s", id, x["status"], x["errorMessage"],
				runFiles(dir))
		}
	}
	t.Fatalf("execution %s selected no issue in 10 minutes", id)
	return 0
}

func selectedInProgress(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var line struct {
			Type     string `json:"type"`
			WorkItem string `json:"workItem"`
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil || line.Type != "work_item.selected" {
			continue
		}
		repo, num, ok := strings.Cut(line.WorkItem, "#")
		n, err := strconv.Atoi(num)
		if !ok || err != nil || !strings.EqualFold(repo, queueRepo) {
			t.Fatalf("progress.jsonl selected %q, want %s#<issue>", line.WorkItem, queueRepo)
		}
		return n
	}
	return 0
}

// waitTracked waits until the record of the execution on disk names the issue the session selected: the daemon reads
// the progress line a moment after it is written, and a restart before that leaves nothing to count against the item.
func waitTracked(t *testing.T, d *daemon, home, id, dir string, issue int) {
	t.Helper()
	var rec struct {
		Status   string `json:"status"`
		WorkItem string `json:"workItem"`
	}
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		data, err := os.ReadFile(filepath.Join(home, "state", "executions", id+".json"))
		// Any state a recovery interrupts: the session may have moved on to testing or publishing.
		if err == nil && json.Unmarshal(data, &rec) == nil && activeStatus(rec.Status) && strings.EqualFold(rec.WorkItem, issueKey(issue)) {
			return
		}
		if x := d.call("GET", "/executions/"+id, nil, 200); x["endedAt"] != nil {
			t.Fatalf("execution %s ended %v right after it selected #%d, before the restart\n%s", id, x["status"], issue,
				runFiles(dir))
		}
	}
	t.Fatalf("the record of %s on disk is %q for %q, want an active state for %s", id, rec.Status, rec.WorkItem,
		issueKey(issue))
}

func activeStatus(s string) bool {
	switch s {
	case "preparing", "running", "switching_harness", "testing", "publishing":
		return true
	}
	return false
}

// restartMidRun kills the daemon while a session runs, as a crash would, then ends the session's process group, which
// a daemon killed this way leaves running, and starts the daemon again on the same home.
func restartMidRun(t *testing.T, d *daemon, home, claude, clone string) *daemon {
	t.Helper()
	out, _ := exec.Command("pgrep", "-P", strconv.Itoa(d.cmd.Process.Pid)).Output()
	var groups []int
	for _, f := range strings.Fields(string(out)) {
		pid, _ := strconv.Atoi(f)
		if pgid, err := syscall.Getpgid(pid); err == nil && pgid > 1 && pgid != syscall.Getpgrp() {
			groups = append(groups, pgid)
		}
	}
	if len(groups) == 0 {
		t.Fatal("found no session process group under the daemon")
	}
	d.kill()
	for _, g := range groups {
		_ = syscall.Kill(-g, syscall.SIGKILL)
	}
	d2 := startDaemon(t, home)
	useClaude(d2, claude, clone)
	return d2
}

// runFiles is what a session left in its execution directory, for a failure message.
func runFiles(dir string) string {
	var b strings.Builder
	for _, name := range []string{"progress.jsonl", "report.json"} {
		data, _ := os.ReadFile(filepath.Join(dir, name))
		fmt.Fprintf(&b, "%s:\n%s\n", name, data)
	}
	return b.String()
}

func issueKey(n int) string { return fmt.Sprintf("%s#%d", queueRepo, n) }

func contextItem(t *testing.T, dir string, issue int) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "context.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ctx struct {
		WorkItems []map[string]any `json:"workItems"`
	}
	if err := json.Unmarshal(data, &ctx); err != nil {
		t.Fatal(err)
	}
	for _, w := range ctx.WorkItems {
		if w["key"] == issueKey(issue) {
			return w
		}
	}
	return nil
}

type ledgerItem struct {
	Status   string `json:"status"`
	Failures int    `json:"failures"`
}

func readLedger(t *testing.T, home string) map[string]ledgerItem {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "state", "workitems.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Tasks map[string]map[string]ledgerItem `json:"tasks"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	return f.Tasks["queue"]
}
