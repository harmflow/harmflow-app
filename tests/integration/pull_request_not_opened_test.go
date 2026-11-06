package integration

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// prNotOpenedLabel marks the fixture issue TestPullRequestThatCannotBeOpenedFailsTheExecution opens in queueRepo,
// apart from the other fixture sets, which it never reads or changes. The file the fixture asks for goes in
// prNotOpenedDir, which the base branch never holds: a branch that holds it is one a session pushed for a fixture.
const (
	prNotOpenedLabel = "pr-not-opened-test"
	prNotOpenedDir   = "pr-not-opened-test"
)

// A session that pushes its work item's branch but cannot open its pull request ends the execution failed, an
// operational failure, never completed as a draft with failing tests does, and keeps the item's branch, its worktree,
// and the execution's log. The real Claude Code runs the built-in agent's copy and the built-in skills of this checkout
// against queueRepo, on a fixture issue the test opens, from a clone that may push, with gh pr create denied as
// restartTask denies it. The execution's error is pull_request_not_opened, naming the fixture's work item; the work
// item reads failed, with one failure and no pull request, and keeps its branch and its worktree, still on disk; the
// branch is on GitHub; the log holds the execution's events and no pull_request.created or pull_request.updated; and no
// pull request links the issue or comes from its branch.
func TestPullRequestThatCannotBeOpenedFailsTheExecution(t *testing.T) {
	claude := realClaude(t)
	base := sandboxBase(t)
	for _, label := range []string{prNotOpenedLabel, "harmflow"} {
		sandboxLabel(t, label)
	}
	run := runSuffix()
	file := prNotOpenedDir + "/" + run + ".txt"
	issue := labelledFixture(t, prNotOpenedLabel, prNotOpenedDir, base, "Add the note "+file,
		fmt.Sprintf("Add the file `%s` with the single line `%s`, and change nothing else. The repository has no tests.\n",
			file, run))
	before := branchHead(t, base)
	clone := pushableClone(t)
	home := t.TempDir()
	d := startDaemon(t, home)
	useClaude(d, claude, clone)
	d.call("POST", "/tasks", map[string]any{"yaml": restartTask(d, clone, issue, false)}, 201)

	id := d.call("POST", "/tasks/restart/run", nil, 202)["executionId"].(string)
	dir := executionDir(d, id)
	// Longer than the task's timeout, so that the execution's own end says why a session ran too long.
	x := d.waitEnd(id, withinDeadline(t, 13*time.Minute))
	key := issueKey(issue)
	if x["status"] != "failed" {
		t.Errorf("execution %s = %v (%v), want failed\n%s", id, x["status"], x["errorMessage"], runFiles(dir))
	}
	params, _ := x["errorParams"].(map[string]any)
	if item, _ := params["workItem"].(string); x["errorKey"] != "pull_request_not_opened" || !strings.EqualFold(item, key) {
		t.Errorf("execution %s ended with the error %v %v (%v), want pull_request_not_opened naming %s\n%s", id,
			x["errorKey"], params, x["errorMessage"], key, runFiles(dir))
	}

	var branch, worktreePath string
	results, _ := x["workItems"].([]any)
	for _, r := range results {
		w, _ := r.(map[string]any)
		if k, _ := w["key"].(string); strings.EqualFold(k, key) {
			branch, _ = w["branch"].(string)
			worktreePath, _ = w["worktreePath"].(string)
		}
	}
	if branch == "" || worktreePath == "" {
		t.Fatalf("execution %s gives %s the branch %q and the worktree %q, want both: %v\n%s", id, key, branch,
			worktreePath, results, runFiles(dir))
	}
	if info, err := os.Stat(worktreePath); err != nil || !info.IsDir() {
		t.Errorf("os.Stat(%q), the worktree of %s, = %v, want the folder still on disk", worktreePath, key, err)
	}
	var tracked map[string]any
	items, _ := d.call("GET", "/queue", nil, 200)["workItems"].([]any)
	for _, i := range items {
		w, _ := i.(map[string]any)
		if k, _ := w["key"].(string); strings.EqualFold(k, key) {
			tracked = w
		}
	}
	if tracked == nil || tracked["status"] != "failed" || tracked["failures"] != 1.0 || tracked["pullRequestUrl"] != nil ||
		tracked["branch"] != branch {
		t.Errorf("GET /queue tracks %s as %v, want failed, with one failure, no pull request, and the branch %s", key,
			tracked, branch)
	}

	pushed, err := branchesHolding(prNotOpenedDir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(pushed, []string{branch}) {
		t.Errorf("the branches of %s holding %s are %v, want %s alone, the work item's\n%s", queueRepo, prNotOpenedDir,
			pushed, branch, runFiles(dir))
	}

	var types []string
	selected := false
	for _, e := range d.events(id) {
		typ, _ := e["type"].(string)
		types = append(types, typ)
		if item, _ := e["workItemId"].(string); typ == "work_item.selected" && strings.EqualFold(item, key) {
			selected = true
		}
	}
	if !selected || !slices.Contains(types, "execution.failed") {
		t.Errorf("execution %s logged the events %q, want the selection of %s and execution.failed", id, types, key)
	}
	if slices.Contains(types, "pull_request.created") || slices.Contains(types, "pull_request.updated") {
		t.Errorf("execution %s logged the events %q, want no pull_request.created or pull_request.updated\n%s", id,
			types, runFiles(dir))
	}

	prs, err := issuePullRequests(issue, pushed)
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 0 {
		t.Errorf("the pull requests of %s that link #%d or come from %v are %+v, want none\nthe session ran:\n%s",
			queueRepo, issue, pushed, prs, commandsOf(d, id))
	}
	if after := branchHead(t, base); after != before {
		t.Errorf("the head of %s moved from %s to %s during the run, want it unchanged", base, before, after)
	}
}
