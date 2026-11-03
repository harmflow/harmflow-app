package integration

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// otherBranchLabel marks the fixture issue TestIssueClosedFromAnotherBranchIsHandedToAPerson opens in queueRepo, apart
// from the other fixture sets, which it never reads or changes. The file the fixture asks for goes in otherBranchDir,
// which the base branch never holds: a branch that holds it is the test's own, or one a session pushed.
const (
	otherBranchLabel = "other-branch-test"
	otherBranchDir   = "other-branch-test"
)

// An item whose issue an open pull request from another branch closes is handed to a person, with nothing pushed. The
// real Claude Code runs the built-in agent's copy and the built-in skills of this checkout against queueRepo, on a
// fixture issue the test opens, from a clone that may push. Once the session has selected the issue, the test opens a
// pull request of its own that closes it, from a branch no session would name for it, and lets the session commit only
// once GitHub links that pull request to the issue: the session's lookups before it publishes come after its commit,
// so they find the link. The execution then logs no pull request created or updated, and its report gives the item
// needs_attention, no pull request, and a conflicting_activity warning that names the session's work branch, the test's
// branch, and the test's pull request. On GitHub, the test's branch alone holds the fixture's change, and the test's
// pull request alone links the issue, still open at the test's commit, its body never edited. The session's commits
// stay on its work branch, in its worktree under the execution's workspace.
func TestIssueClosedFromAnotherBranchIsHandedToAPerson(t *testing.T) {
	claude := realClaude(t)
	base := sandboxBase(t)
	for _, label := range []string{otherBranchLabel, "harmflow"} {
		sandboxLabel(t, label)
	}
	run := runSuffix()
	file := otherBranchDir + "/" + run + ".txt"
	issue := labelledFixture(t, otherBranchLabel, otherBranchDir, base, "Add the changelog entry "+file,
		fmt.Sprintf("Add the file `%s` with the single line `%s`, and change nothing else. The repository has no tests.\n",
			file, run))
	key := issueKey(issue)
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clone := shallowCloneIn(t, parent, queueRepo)
	// The clone shallowClone makes, with a push URL for this test's clone only, so that only the skills keep the session
	// from pushing its work branch.
	if out, err := exec.Command("git", "-C", clone, "remote", "set-url", "--push", "origin", "https://github.com/"+queueRepo).CombinedOutput(); err != nil {
		t.Fatalf("git remote set-url --push: %v\n%s", err, out)
	}
	release := holdCommits(t, clone)
	home := t.TempDir()
	d := startDaemon(t, home)
	useClaude(d, claude, clone)
	d.call("POST", "/tasks", map[string]any{"yaml": labelledIssuesTask(d, "other-branch", otherBranchLabel, clone, base)}, 201)

	id := d.call("POST", "/tasks/other-branch/run", nil, 202)["executionId"].(string)
	dir := executionDir(d, id)
	if got := selectedIssue(t, d, id, dir); got != issue {
		t.Fatalf("execution %s selected #%d, want #%d, the fixture\n%s", id, got, issue, runFiles(dir))
	}
	own, commit := otherBranchPullRequest(t, base, issue, file, run+"\n")
	checkNothingPublishedYet(t, d, id, dir, file, own)
	release()

	// Longer than the task's timeout, so that the execution's own end says why a session ran too long.
	x := d.waitEnd(id, withinDeadline(t, 13*time.Minute))
	for _, e := range d.events(id) {
		if e["type"] == "pull_request.created" || e["type"] == "pull_request.updated" {
			t.Errorf("execution %s (%v) logged %s %v, want no pull request created or updated\n%s", id, x["status"],
				e["type"], e["payload"], runFiles(dir))
		}
	}
	if outcome, _ := reportedResult(t, dir, issue); outcome != "needs_attention" {
		t.Errorf("report.json gives %s the outcome %q, want needs_attention\n%s", key, outcome, runFiles(dir))
	}
	if pr := reportedPullRequest(t, dir, issue); pr != nil {
		t.Errorf("report.json gives %s the pull request %+v, want none", key, pr)
	}
	if item := resumeLimitHeld(t, home, "other-branch", key); item == nil || item["status"] != "needs_attention" {
		t.Errorf("after execution %s, the ledger holds %s as %v, want it needs_attention", id, key, item)
	}

	workspace := contextWorkspace(t, dir)
	var work []worktree
	for _, w := range worktrees(t, clone)[1:] {
		if w.Branch != "" && w.Branch != base && within(t, workspace, w.Path) {
			work = append(work, w)
		}
	}
	if len(work) != 1 {
		t.Fatalf("git worktree list in the clone gives %d worktrees on a work branch under the workspace %s, want 1: %+v",
			len(work), workspace, worktrees(t, clone))
	}
	branch := work[0].Branch
	if ahead := git(t, clone, "rev-list", "--count", "origin/"+base+".."+branch); ahead == "0" {
		t.Errorf("git rev-list --count origin/%s..%s = %s, want the session's commits on its work branch", base, branch, ahead)
	}
	if !reportedConflict(t, dir, key, branch, own) {
		t.Errorf("report.json has no conflicting_activity warning for %s naming %s, %s, and %s\n%s", key, branch, own.Head,
			own.URL, runFiles(dir))
	}

	branches, prs := issueWork(t, issue, file)
	if !slices.Equal(branches, []string{own.Head}) {
		t.Errorf("the branches of %s holding %s are %v, want %s alone, the test's: the session pushed nothing", queueRepo,
			file, branches, own.Head)
	}
	if len(prs) != 1 || prs[0].Number != own.Number {
		t.Errorf("the pull requests of %s linking #%d or from a branch holding %s are %+v, want %s alone", queueRepo, issue,
			file, prs, own.URL)
	}
	linked, err := linkingPullRequests(issue)
	if err != nil {
		t.Fatal(err)
	}
	for _, pr := range linked {
		if pr.Number == own.Number && (pr.State != "OPEN" || pr.HeadOID != commit) {
			t.Errorf("%s is %s at %s, want it open at %s, the test's commit", pr.URL, pr.State, pr.HeadOID, commit)
		}
	}
	if edited := lastBodyEdit(t, own.Number); !edited.IsZero() {
		t.Errorf("GitHub records an edit of the body of %s at %v, want none", own.URL, edited)
	}
}

// holdCommits holds every commit made in the clone, its worktrees' included, until the returned release is called, and
// returns it. The hold rests on no timing: a pre-commit hook in the clone, which every worktree of the clone runs,
// waits for a file that release writes. The wait is bounded at 100 seconds, below the two minutes a Bash call of
// Claude Code gets by default, after which the hook fails the commit, so a commit tried again is held again. When the
// test ends, failed or not, the commits are released and the hook removed.
func holdCommits(t *testing.T, clone string) (release func()) {
	t.Helper()
	released := filepath.Join(t.TempDir(), "released")
	hook := filepath.Join(clone, ".git", "hooks", "pre-commit")
	script := fmt.Sprintf(`#!/bin/sh
i=0
while [ ! -e '%s' ]; do
	i=$((i + 1))
	if [ "$i" -gt 100 ]; then
		echo "pre-commit: the repository is busy; commit again" >&2
		exit 1
	fi
	sleep 1
done
`, released)
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	release = func() {
		if err := os.WriteFile(released, nil, 0o644); err != nil {
			t.Errorf("release the commits held in %s: %v", clone, err)
		}
	}
	t.Cleanup(func() {
		release()
		if err := os.Remove(hook); err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("cleanup: removing %s: %v", hook, err)
		}
	})
	return release
}

// otherBranchPullRequest opens a pull request of queueRepo into base that closes the issue, from a branch of the
// test's own, and returns it with the one commit its branch holds beyond base, which makes the fixture's change: the
// file with its content. The branch is named under otherBranchDir with no issue number in its last segment, as no
// session names a work branch, and the pull request is ready and carries no harmflow label, so no session takes it for
// its own. It returns once GitHub lists the pull request among those that link the issue, as a session's lookup reads
// it.
func otherBranchPullRequest(t *testing.T, base string, issue int, file, content string) (restartPR, string) {
	t.Helper()
	return openFixturePR(t, base, issue, fixturePR{
		Branch:  otherBranchDir + "/fixture-" + runSuffix(),
		File:    file,
		Content: content,
		Message: "Add the changelog entry",
		Title:   "Add the changelog entry",
		Body:    fmt.Sprintf("Adds `%s`.\n\nCloses #%d\n", file, issue),
	})
}

// checkNothingPublishedYet fails the test when the session published before GitHub linked the test's pull request to
// the issue: a pull_request.created or pull_request.updated event in the execution's log, or a branch other than the
// test's holding the fixture's file. Such a run lost the race the test sets up, and proves nothing about the handoff.
func checkNothingPublishedYet(t *testing.T, d *daemon, id, dir, file string, own restartPR) {
	t.Helper()
	for _, e := range d.events(id) {
		if e["type"] == "pull_request.created" || e["type"] == "pull_request.updated" {
			t.Fatalf("execution %s logged %s %v before GitHub linked %s to the issue: the session published first, so "+
				"this run cannot test the handoff\n%s", id, e["type"], e["payload"], own.URL, runFiles(dir))
		}
	}
	branches, err := issueBranches(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range branches {
		if b != own.Head {
			t.Fatalf("the branch %s held %s before GitHub linked %s to the issue: the session pushed first, so this run "+
				"cannot test the handoff\n%s", b, file, own.URL, runFiles(dir))
		}
	}
}

// reportedConflict reports whether the execution's report.json warns of conflicting activity for the work item key,
// in a message that names the session's work branch, the other pull request's branch, and its URL.
func reportedConflict(t *testing.T, dir, key, branch string, other restartPR) bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		t.Errorf("report.json: %v", err)
		return false
	}
	var report struct {
		Warnings []struct {
			Code     string `json:"code"`
			Message  string `json:"message"`
			WorkItem string `json:"workItem"`
		} `json:"warnings"`
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Errorf("report.json: %v", err)
		return false
	}
	for _, w := range report.Warnings {
		if w.Code == "conflicting_activity" && strings.EqualFold(w.WorkItem, key) && strings.Contains(w.Message, branch) &&
			strings.Contains(w.Message, other.Head) && strings.Contains(w.Message, other.URL) {
			return true
		}
	}
	return false
}
