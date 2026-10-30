package integration

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// waitingLabel marks the two fixture issues TestWaitingFiringSelectsAnIssueOpenedMeanwhile opens in queueRepo. The test
// owns it alone, so that its cleanup never closes another test's fixtures.
const waitingLabel = "waiting-firing-test"

// waitingContext is what the tests read of the context.json the daemon gave a session: its history.
type waitingContext struct {
	Recent    []recentEntry    `json:"recentExecutions"`
	WorkItems []map[string]any `json:"workItems"`
}

// A firing that arrives while its GitHub task runs waits, and the daemon writes its context.json only when it starts,
// once the execution it waited for has ended: the context holds the history that execution left, not the one there was
// when the firing was queued. The first execution, through a stand-in Claude Code, selects o/r#3 and holds at its
// tests until the test opens a gate; it then reports the item completed with a draft pull request.
func TestWaitingFiringGetsAFreshContext(t *testing.T) {
	t.Parallel()
	const item = "o/r#3"
	home := t.TempDir()
	d := startDaemon(t, home)
	_, repo := pushRepository(t)
	gate := filepath.Join(t.TempDir(), "gate")
	stand := standIn(t, "fake-claude", "claude",
		"    sleep 1.5\n", fmt.Sprintf("    until [ -e '%s' ]; do sleep 0.05; done\n", gate))
	d.call("PUT", "/harnesses/claude-code", map[string]any{"path": stand.path}, 200)
	// The repository is outside the home folder, so an allowed folder.
	d.call("PATCH", "/settings", map[string]any{"allowedFolders": []any{repo}}, 200)
	d.call("POST", "/tasks", map[string]any{"yaml": waitingTask(repo, "ITEM="+item+" SCENARIO=report")}, 201)

	first := d.call("POST", "/tasks/waits/run", nil, 202)["executionId"].(string)
	waitWorkItem(d, first, item)
	second := d.call("POST", "/tasks/waits/run", nil, 202)
	if second["outcome"] != "queued" {
		t.Fatalf("POST /tasks/waits/run while %s runs = %v, want outcome queued", first, second)
	}
	waiting := second["executionId"].(string)
	waitingFile := filepath.Join(home, "runs", waiting, "context.json")
	if _, err := os.Stat(waitingFile); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the waiting execution %s has a context.json while it waits (%v), want none", waiting, err)
	}
	if x := d.call("GET", "/executions/"+waiting, nil, 200); x["status"] != "queued" {
		t.Fatalf("the waiting execution %s is %v, want queued while %s runs", waiting, x["status"], first)
	}

	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	x := d.waitStatus(first, "completed", "failed", "cancelled", "interrupted")
	if x["status"] != "completed" {
		t.Fatalf("the first execution %s = %v (%v), want completed", first, x["status"], x["errorMessage"])
	}
	started := d.waitStatus(waiting, "running", "testing", "publishing", "completed", "failed")
	data, err := os.ReadFile(filepath.Join(started["executionDir"].(string), "context.json"))
	var ctx waitingContext
	if err == nil {
		err = json.Unmarshal(data, &ctx)
	}
	if err != nil {
		t.Fatalf("context.json of the execution that waited, %s: %v", waiting, err)
	}

	var recent *recentEntry
	for i := range ctx.Recent {
		if ctx.Recent[i].ExecutionID == first {
			recent = &ctx.Recent[i]
		}
	}
	if recent == nil || recent.Status != x["status"] || recent.WorkItem != item {
		t.Errorf("the waiting execution's recentExecutions = %+v, want %s %v on %s", ctx.Recent, first, x["status"], item)
	}
	var got map[string]any
	for _, w := range ctx.WorkItems {
		if w["key"] == item {
			got = w
		}
	}
	// What the first execution's report left: the item completed, with its draft pull request and passing tests.
	want := map[string]any{"status": "completed", "lastExecutionId": first, "testsStatus": "passed",
		"pullRequestUrl": "https://github.com/o/r/pull/4"}
	if !priorityHolds(got, want) {
		t.Errorf("the waiting execution's workItems list %s as %v, want %v", item, got, want)
	}

	if end := d.waitStatus(waiting, "completed", "failed", "cancelled", "interrupted"); end["status"] != "completed" {
		t.Errorf("the execution that waited, %s = %v (%v), want completed", waiting, end["status"], end["errorMessage"])
	}
}

// waitingTask is a GitHub task, waits, on the repository o/r at localPath, whose prompt picks the stand-in's scenario.
func waitingTask(localPath, prompt string) string {
	return `apiVersion: harmflow/v1alpha1
kind: Task
metadata:
  id: waits
  name: Task waits
spec:
  type: github
  prompt: "` + prompt + `"
  harness:
    preferred:
      provider: claude-code
  github:
    repositories:
      - name: o/r
        localPath: "` + localPath + `"
`
}

// waitWorkItem waits until the execution names the work item its session selected.
func waitWorkItem(d *daemon, id, item string) {
	d.t.Helper()
	var x map[string]any
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if x = d.call("GET", "/executions/"+id, nil, 200); x["workItem"] == item {
			return
		}
		if x["endedAt"] != nil {
			break
		}
	}
	d.t.Fatalf("execution %s is %v on %v, want it running on %s", id, x["status"], x["workItem"], item)
}

// A firing that waited runs discovery again when it starts, and sees an issue opened while it waited. The real Claude
// Code runs the built-in agent's copy and the built-in skills of this checkout, one item a cycle, new work first, on
// the open issues of queueRepo labelled waitingLabel. The first execution selects the one fixture there is. While it
// runs, the task is run again, and that firing waits; the test then opens a second fixture, and stops the first
// execution as TestFailedIssueIsRetriedUntilItsLimit stops its sessions. The waiting execution starts on the restarted
// daemon, and its session must select the second fixture, which did not exist when the firing was queued: the first
// one is in the task's history, and new work comes before a retry. Each session is stopped once the daemon tracks the
// issue it selected, and its clone has no push URL, so no branch and no pull request of its reaches GitHub.
func TestWaitingFiringSelectsAnIssueOpenedMeanwhile(t *testing.T) {
	claude := realClaude(t)
	sandboxLabel(t, waitingLabel)
	closeLabelledFixtures(t, waitingLabel)
	var own []int
	t.Cleanup(func() { closeLabelledFixtures(t, waitingLabel, own...) })
	run := runSuffix()
	older := waitingIssue(t, run, "older")
	own = append(own, older)
	awaitWaitingListed(t, older)

	clone := shallowClone(t)
	home := t.TempDir()
	d := startDaemon(t, home)
	useClaude(d, claude, clone)
	d.call("POST", "/tasks", map[string]any{"yaml": waitingQueueTask(d, clone)}, 201)

	first := d.call("POST", "/tasks/queue/run", nil, 202)["executionId"].(string)
	dir := executionDir(d, first)
	if got := selectedIssue(t, d, first, dir); got != older {
		t.Fatalf("the first execution %s selected #%d, want #%d, the one fixture open\n%s", first, got, older, runFiles(dir))
	}
	waitTracked(t, d, home, first, dir, older)
	second := d.call("POST", "/tasks/queue/run", nil, 202)
	if second["outcome"] != "queued" {
		t.Fatalf("POST /tasks/queue/run while %s runs = %v, want outcome queued", first, second)
	}
	waiting := second["executionId"].(string)
	newer := waitingIssue(t, run, "newer")
	own = append(own, newer)
	awaitWaitingListed(t, older, newer)

	d = restartMidRun(t, d, home, claude, clone)
	if x := d.call("GET", "/executions/"+first, nil, 200); x["status"] != "interrupted" {
		t.Errorf("the first execution %s after the stop: status %v, want interrupted", first, x["status"])
	}
	wdir := executionDir(d, waiting)
	got := selectedIssue(t, d, waiting, wdir)
	waitTracked(t, d, home, waiting, wdir, got)
	d = restartMidRun(t, d, home, claude, clone)
	if got != newer {
		t.Errorf("the execution that waited, %s, selected #%d, want #%d, opened while it waited: it did not run "+
			"discovery again\ncontext.json lists #%d as %v\n%s", waiting, got, newer, older, contextItem(t, wdir, older),
			runFiles(wdir))
	}
	if x := d.call("GET", "/executions/"+waiting, nil, 200); x["status"] != "interrupted" {
		t.Errorf("the execution that waited, %s, after the stop at selection: status %v, want interrupted", waiting,
			x["status"])
	}
}

// waitingQueueTask is the task queueTask makes, with its eligible-issues line taking the open issues of queueRepo
// labelled waitingLabel, its queue in the oldest-first order, and new work before work to resume or retry.
func waitingQueueTask(d *daemon, clone string) string {
	d.t.Helper()
	task := issueQueueTask(d, clone,
		fmt.Sprintf("open issues of %s labelled %s; every other issue is excluded.", queueRepo, waitingLabel), "oldest_first")
	const from = "resumeStrategy: resume_first"
	if !strings.Contains(task, from) {
		d.t.Fatalf("issueQueueTask's task has no %q to replace:\n%s", from, task)
	}
	return strings.Replace(task, from, "resumeStrategy: new_first", 1)
}

// waitingIssue opens a fixture issue in queueRepo, labelled waitingLabel, and returns its number. Its text reads as a
// small real task, as the sandbox asks of every fixture.
func waitingIssue(t *testing.T, run, which string) int {
	t.Helper()
	file := fmt.Sprintf("%s/%s-%s.txt", waitingLabel, run, which)
	out := newWorkAPI(t, "repos/"+queueRepo+"/issues", "-f", "title=Waiting firing test "+run+": add "+file,
		"-f", fmt.Sprintf("body=A fixture of HarmFlow's waiting firing test, which closes it when it ends.\n\n"+
			"Add the file `%s` with the single line `%s`, and change nothing else. The repository has no tests.\n", file, run),
		"-f", "labels[]="+waitingLabel, "--jq", ".number")
	n, err := strconv.Atoi(out)
	if err != nil {
		t.Fatalf("open the %s fixture issue: %q, %v", which, out, err)
	}
	return n
}

// awaitWaitingListed waits until the open issues of queueRepo labelled waitingLabel are want, oldest first: GitHub
// lists a new issue under its label only some seconds after it opens it, and a session that read the list before would
// not find it.
func awaitWaitingListed(t *testing.T, want ...int) {
	t.Helper()
	var listed []int
	var err error
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(time.Second) {
		if listed, err = labelledIssues(waitingLabel); err == nil && slices.Equal(listed, want) {
			return
		}
	}
	t.Fatalf("after a minute, the open issues of %s labelled %s are %v (%v), want %v", queueRepo, waitingLabel, listed,
		err, want)
}
