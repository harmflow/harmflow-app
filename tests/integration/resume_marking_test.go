package integration

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// markingLabel marks what TestUnmarkedDraftIsNotResumed opens in queueRepo: its two fixture issues and the draft pull
// request without the task's marking that links the first. The test owns the label alone, so that its cleanup never
// closes another test's fixtures.
const markingLabel = "resume-marking-test"

// A draft pull request without the task's marking is not resumed, though its issue's work item reads in progress with
// failing tests: it is someone else's work. The test opens in queueRepo, under markingLabel, two fixture issues, oldest
// first, and a draft pull request that links the first and carries neither the label harmflow nor the line naming the
// task. In a daemon home of its own, a first execution through a stand-in Claude Code reports the first issue's item
// completed with that draft and its tests failed, so the ledger keeps the item in progress and holds nothing for the
// second issue. Then the real Claude Code runs the built-in agent's copy and the built-in skills of this checkout once,
// one item a cycle, under resume_first, which would put the draft's issue first if the draft carried the marking. The
// session is stopped once the daemon tracks the issue it selects, and its clone has no push URL, so no branch and no
// pull request of its reaches GitHub. It must take the second issue as new work, and leave the draft and the first
// issue's work item as they were.
func TestUnmarkedDraftIsNotResumed(t *testing.T) {
	claude := realClaude(t)
	resumable, fresh, draft := priorityFixtures(t, markingLabel, false)
	clone := shallowClone(t)
	home := t.TempDir()
	d := startDaemon(t, home)
	want := priorityFailedDraft(t, d, clone, labelledPriorityTask(d, clone, markingLabel, "resume_first"), resumable, fresh,
		draft)
	before := markingDraftState(t, draft.Number)
	if before.State != "open" || !before.Draft || before.Merged || !slices.Equal(before.Labels, []string{markingLabel}) {
		t.Fatalf("before the run, the draft pull request %s reads %+v, want an open draft, not merged, labelled %s alone",
			draft.URL, before, markingLabel)
	}

	useClaude(d, claude, clone)
	id := d.call("POST", "/tasks/queue/run", nil, 202)["executionId"].(string)
	dir := executionDir(d, id)
	got := selectedIssue(t, d, id, dir)
	waitTracked(t, d, home, id, dir, got)
	d = restartMidRun(t, d, home, claude, clone)
	if x := d.call("GET", "/executions/"+id, nil, 200); x["status"] != "interrupted" {
		t.Errorf("execution %s after the stop at selection: status %v, want interrupted", id, x["status"])
	}

	if item := contextItem(t, dir, resumable); !priorityHolds(item, want) {
		t.Errorf("the context.json of execution %s lists %s as %v, want %v", id, issueKey(resumable), item, want)
	}
	if item := contextItem(t, dir, fresh); item != nil {
		t.Errorf("the context.json of execution %s lists %s, which no execution took: %v", id, issueKey(fresh), item)
	}
	if selected := selectedWorkItems(t, dir); len(selected) != 1 || !strings.EqualFold(selected[0], issueKey(fresh)) {
		t.Errorf("progress.jsonl selects %v, want %s alone, since the draft linking %s lacks the task's marking: "+
			"hf-github-issue-queue resumed someone else's work\n%s", selected, issueKey(fresh), issueKey(resumable),
			runFiles(dir))
	}
	if msg := selectedMessage(t, dir); !strings.HasSuffix(msg, " — from new issues") {
		t.Errorf("the work_item.selected message is %q, want the issue's title and \" — from new issues\"", msg)
	}
	if lines := markingProgressNaming(t, dir, resumable); len(lines) != 0 {
		t.Errorf("progress.jsonl of execution %s names %s, the unmarked draft's issue: %q", id, issueKey(resumable), lines)
	}
	for _, e := range d.events(id) {
		item, _ := e["workItemId"].(string)
		if e["type"] == "pull_request.created" || e["type"] == "pull_request.updated" || strings.EqualFold(item, issueKey(resumable)) {
			t.Errorf("execution %s logged %v for %q, want no pull request event and none for %s: %v", id, e["type"], item,
				issueKey(resumable), e)
		}
	}
	if item := priorityQueued(d, issueKey(resumable)); !priorityHolds(item, map[string]any{"status": "in_progress", "resumes": 0.0}) {
		t.Errorf("after the run, GET /queue gives %s as %v, want it in_progress with resumes 0", issueKey(resumable), item)
	}

	if after := markingDraftState(t, draft.Number); !reflect.DeepEqual(after, before) {
		t.Errorf("after the run, the draft pull request %s reads %+v, want %+v, as it was before the run", draft.URL, after,
			before)
	}
}

// markingDraft is what TestUnmarkedDraftIsNotResumed reads of its draft pull request on GitHub, before and after the
// run: a session that resumed it would change one of these.
type markingDraft struct {
	State  string    `json:"state"`
	Draft  bool      `json:"draft"`
	Merged bool      `json:"merged"`
	Head   string    `json:"head"`
	Labels []string  `json:"labels"`
	Edited time.Time `json:"-"`
}

// markingDraftState reads the pull request of queueRepo with gh: its state, whether it is a draft and merged, its head
// commit, its labels in order of name, and the last edit of its body, by lastBodyEdit.
func markingDraftState(t *testing.T, number int) markingDraft {
	t.Helper()
	var pr markingDraft
	if err := newWorkJSON(&pr, fmt.Sprintf("repos/%s/pulls/%d", queueRepo, number), "--jq",
		"{state, draft, merged, head: .head.sha, labels: [.labels[].name]}"); err != nil {
		t.Fatal(err)
	}
	slices.Sort(pr.Labels)
	pr.Edited = lastBodyEdit(t, number)
	return pr
}

// markingProgressNaming lists the lines of the session's progress.jsonl whose workItem is the issue's.
func markingProgressNaming(t *testing.T, dir string, issue int) []string {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "progress.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var line struct {
			WorkItem string `json:"workItem"`
		}
		if json.Unmarshal(sc.Bytes(), &line) == nil && strings.EqualFold(line.WorkItem, issueKey(issue)) {
			lines = append(lines, sc.Text())
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}
