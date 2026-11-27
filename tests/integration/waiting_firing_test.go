package integration

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
	if !holdsFields(got, want) {
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

// holdsFields reports whether item holds each field of want with its value.
func holdsFields(item, want map[string]any) bool {
	for k, v := range want {
		if item[k] != v {
			return false
		}
	}
	return true
}
