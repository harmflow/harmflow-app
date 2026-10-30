package integration

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// conflictLabel marks the fixture issue TestIssueAssignedMeanwhileIsLoggedAsAConflict opens in queueRepo, apart from
// the other fixture sets, which it never reads or changes. The file the fixture asks for goes in conflictDir, which the
// base branch never holds: a branch that holds it is one a session pushed for a fixture.
const (
	conflictLabel = "conflict-test"
	conflictDir   = "conflict-test"
)

// A task meets activity that conflicts with its work, goes on, and records the conflict. The real Claude Code runs the
// built-in agent's copy and the built-in skills of this checkout against queueRepo, on a fixture issue the test opens,
// from a clone that may push. Once the daemon tracks the issue as the execution's work item, and before the session
// opens its pull request, the test assigns the issue, as a person taking it would. The execution still ends
// completed, with a draft pull request for the issue, its log holds one work_item.conflicting_activity event for the
// issue's work item, and its record keeps a conflicting_activity warning for the repository. The test's account is the
// session's own, but the built-in skills never assign an issue, so the assignee is someone else's act.
func TestIssueAssignedMeanwhileIsLoggedAsAConflict(t *testing.T) {
	claude := realClaude(t)
	base := sandboxBase(t)
	for _, label := range []string{conflictLabel, "harmflow"} {
		sandboxLabel(t, label)
	}
	run := runSuffix()
	file := conflictDir + "/" + run + ".txt"
	issue := labelledFixture(t, conflictLabel, conflictDir, base, "Conflict test "+run+": add "+file,
		fmt.Sprintf("A fixture of HarmFlow's conflicting-activity test, which closes it when it ends.\n\n"+
			"Add the file `%s` with the single line `%s`, and change nothing else. The repository has no tests.\n", file, run))
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clone := shallowCloneIn(t, parent, queueRepo)
	// The clone shallowClone makes, with a push URL for this test's clone only: its session pushes the work branch.
	if out, err := exec.Command("git", "-C", clone, "remote", "set-url", "--push", "origin", "https://github.com/"+queueRepo).CombinedOutput(); err != nil {
		t.Fatalf("git remote set-url --push: %v\n%s", err, out)
	}
	home := t.TempDir()
	d := startDaemon(t, home)
	useClaude(d, claude, clone)
	d.call("POST", "/tasks", map[string]any{"yaml": labelledIssuesTask(d, "conflict", conflictLabel, clone, base)}, 201)

	id := d.call("POST", "/tasks/conflict/run", nil, 202)["executionId"].(string)
	dir := executionDir(d, id)
	if got := selectedIssue(t, d, id, dir); got != issue {
		t.Fatalf("execution %s selected #%d, want #%d, the fixture\n%s", id, got, issue, runFiles(dir))
	}
	waitTracked(t, d, home, id, dir, issue)
	if out, err := exec.Command("gh", "issue", "edit", strconv.Itoa(issue), "--repo", queueRepo, "--add-assignee", "@me").CombinedOutput(); err != nil {
		t.Fatalf("gh issue edit %d --add-assignee @me: %v\n%s", issue, err, out)
	}
	for _, e := range d.events(id) {
		if e["type"] == "pull_request.created" || e["type"] == "pull_request.updated" {
			t.Fatalf("execution %s logged %s before the test assigned #%d: the session published before the conflict, "+
				"so this run cannot test it\n%s", id, e["type"], issue, runFiles(dir))
		}
	}
	// Longer than the task's timeout, so that the execution's own end says why a session ran too long.
	x := d.waitEnd(id, withinDeadline(t, 13*time.Minute))
	if x["status"] != "completed" {
		t.Errorf("execution %s = %v (%v), want completed\n%s", id, x["status"], x["errorMessage"], runFiles(dir))
	}
	if pr := reportedPullRequest(t, dir, issue); pr == nil || pr.URL == "" || !pr.Draft || !pr.Created {
		t.Errorf("report.json gives #%d the pull request %+v, want a draft this execution created\n%s", issue, pr,
			runFiles(dir))
	}

	var conflicts []map[string]any
	for _, e := range d.events(id) {
		if e["type"] == "work_item.conflicting_activity" {
			conflicts = append(conflicts, e)
		}
	}
	if len(conflicts) != 1 {
		t.Errorf("execution %s logged %d work_item.conflicting_activity events, want 1: %v\n%s", id, len(conflicts),
			conflicts, runFiles(dir))
	} else if item, _ := conflicts[0]["workItemId"].(string); !strings.EqualFold(item, issueKey(issue)) {
		t.Errorf("work_item.conflicting_activity is for %q, want %s", item, issueKey(issue))
	}
	warnings, _ := x["warnings"].([]any)
	recorded := false
	for _, raw := range warnings {
		w, _ := raw.(map[string]any)
		if subject, _ := w["subject"].(string); w["code"] == "conflicting_activity" && strings.EqualFold(subject, queueRepo) {
			recorded = true
		}
	}
	if !recorded {
		t.Errorf("execution %s records the warnings %v, want a conflicting_activity warning for %s", id, warnings, queueRepo)
	}
}
