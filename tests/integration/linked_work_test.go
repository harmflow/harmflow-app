package integration

import (
	"encoding/base64"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// linkedWorkLabel marks what TestIssuesLinkedInAnyStateAreNotNewWork opens in queueRepo on each run: its three fixture
// issues and the pull request ready for review that links the first. mergedLinkLabel marks its lasting fixture, made
// once in queueRepo as tests/integration/README.md says, which the test reads and never opens, closes, or changes: an
// open issue that a merged pull request links. The test owns both labels alone, and its cleanup reads only the first.
// Its pull requests' files go in linkedWorkDir, which the base branch never holds.
const (
	linkedWorkLabel = "linked-work-test"
	mergedLinkLabel = "merged-link-test"
	linkedWorkDir   = "linked-work-test"
)

// An issue a pull request links is already processed, whatever the pull request's state and whoever opened it: an open
// one ready for review without the task's marking, one closed without merging, and a merged one. The real Claude Code
// runs the built-in agent's copy and the built-in skills of this checkout against queueRepo. The lasting fixture comes
// first in the queue's order, then the issues the test opens under linkedWorkLabel, oldest first: one linked by the
// ready pull request, one by the closed pull request, and one by none. The task's queue takes new work first, one item
// a cycle, and its daemon's home is fresh, so its history holds none of the fixtures: the one issue it selects is its
// first new work, and must be the last. The session is stopped once the daemon tracks that issue, and its clone has no
// push URL, so no branch and no pull request of its reaches GitHub.
func TestIssuesLinkedInAnyStateAreNotNewWork(t *testing.T) {
	claude := realClaude(t)
	merged := mergedLinkFixture(t)
	ready, closed, eligible := linkedWorkFixtures(t)
	clone := shallowClone(t)
	home := t.TempDir()
	d := startDaemon(t, home)
	useClaude(d, claude, clone)
	d.call("POST", "/tasks", map[string]any{"yaml": linkedWorkTask(d, clone)}, 201)

	id := d.call("POST", "/tasks/queue/run", nil, 202)["executionId"].(string)
	dir := executionDir(d, id)
	got := selectedIssue(t, d, id, dir)
	waitTracked(t, d, home, id, dir, got)
	d = restartMidRun(t, d, home, claude, clone)
	if x := d.call("GET", "/executions/"+id, nil, 200); x["status"] != "interrupted" {
		t.Errorf("execution %s after the stop at selection: status %v, want interrupted", id, x["status"])
	}

	notNew := map[int]string{
		ready:  "an open pull request ready for review, without the task's marking, links it",
		closed: "a pull request closed without merging links it",
		merged: "a merged pull request links it",
	}
	if selected := selectedWorkItems(t, dir); len(selected) != 1 || !strings.EqualFold(selected[0], issueKey(eligible)) {
		t.Errorf("progress.jsonl selects %v, want %s alone, the one fixture no pull request links\n%s", selected,
			issueKey(eligible), runFiles(dir))
	}
	if reason, ok := notNew[got]; ok {
		t.Errorf("the session took #%d as new work, though %s, so it is already processed: hf-github-issue-queue does "+
			"not select as it states", got, reason)
	}
	items, _ := d.call("GET", "/queue", nil, 200)["workItems"].([]any)
	for _, x := range items {
		key, _ := x.(map[string]any)["key"].(string)
		for n, reason := range notNew {
			if strings.EqualFold(key, issueKey(n)) {
				t.Errorf("GET /queue tracks %s as a work item, though %s: %v", key, reason, x)
			}
		}
	}
}

// linkedWorkTask is the task queueTask makes, with its eligible-issues line taking the open issues of queueRepo
// labelled linkedWorkLabel or mergedLinkLabel, and its queue taking new work before work to resume.
func linkedWorkTask(d *daemon, clone string) string {
	d.t.Helper()
	task := queueTask(d, clone)
	for _, r := range []struct{ from, to string }{
		{
			fmt.Sprintf("- **Eligible issues:** open issues of %s labelled %s; every other issue is excluded.", queueRepo,
				queueLabel),
			fmt.Sprintf("- **Eligible issues:** open issues of %s labelled %s, and open issues of %s labelled %s; every "+
				"other issue is excluded.", queueRepo, mergedLinkLabel, queueRepo, linkedWorkLabel),
		},
		{"resumeStrategy: resume_first", "resumeStrategy: new_first"},
	} {
		if !strings.Contains(task, r.from) {
			d.t.Fatalf("queueTask's task has no %q to replace:\n%s", r.from, task)
		}
		task = strings.Replace(task, r.from, r.to, 1)
	}
	return task
}

// mergedLinkFixture reads the lasting fixture from GitHub, before the test opens anything, and fails the test as a
// fixture error when it is not as tests/integration/README.md says to make it: the one open issue of queueRepo labelled
// mergedLinkLabel, which a merged pull request links. It returns the issue's number.
func mergedLinkFixture(t *testing.T) int {
	t.Helper()
	var open []int
	if err := newWorkJSON(&open, "repos/"+queueRepo+"/issues?state=open&per_page=100&labels="+mergedLinkLabel,
		"--jq", "[.[] | select(.pull_request | not) | .number]"); err != nil {
		t.Fatalf("read the lasting fixture: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("fixture error: %s has the open issues %v labelled %s, want one, the lasting fixture "+
			"tests/integration/README.md says how to make", queueRepo, open, mergedLinkLabel)
	}
	linking, err := newWorkLinking(open[0])
	if err != nil {
		t.Fatalf("read the pull requests linking the lasting fixture #%d: %v", open[0], err)
	}
	for _, pr := range linking {
		merged, err := newWorkGh(fmt.Sprintf("repos/%s/pulls/%d", queueRepo, pr), "--jq", ".merged_at != null")
		if err != nil {
			t.Fatalf("read the pull request #%d linking the lasting fixture: %v", pr, err)
		}
		if merged == "true" {
			return open[0]
		}
	}
	t.Fatalf("fixture error: the pull requests linking the lasting fixture #%d are %v, want a merged one among them, as "+
		"tests/integration/README.md says", open[0], linking)
	return 0
}

// linkedWorkFixtures opens the three fixture issues in queueRepo under linkedWorkLabel, one after the other, the pull
// request ready for review that links the first, and the pull request that links the second, which it closes without
// merging, and returns the issues' numbers, oldest first. It creates the label when queueRepo lacks it. What an earlier
// run left open under the label, when it was stopped before its cleanup, would come first in the queue's order, so it
// is closed first; when the test ends, failed or not, the same cleanup closes this run's. It returns once GitHub shows
// the fixtures as the session reads them.
func linkedWorkFixtures(t *testing.T) (ready, closed, eligible int) {
	t.Helper()
	if _, err := newWorkGh("repos/" + queueRepo + "/labels/" + linkedWorkLabel); err != nil {
		newWorkAPI(t, "repos/"+queueRepo+"/labels", "-f", "name="+linkedWorkLabel)
	}
	closeLabelledFixtures(t, linkedWorkLabel)
	var own []int
	t.Cleanup(func() { closeLabelledFixtures(t, linkedWorkLabel, own...) })
	run := runSuffix()
	for i := range 3 {
		if i > 0 {
			// GitHub stamps an issue's creation to the second, and the queue reads the issues in that order.
			time.Sleep(time.Second)
		}
		own = append(own, newWorkIssue(t, run, i+1, []string{linkedWorkLabel}))
	}
	issues := slices.Clone(own)
	readyPR, _ := linkedWorkPull(t, run, 1, issues[0])
	own = append(own, readyPR)
	closedPR, branch := linkedWorkPull(t, run, 2, issues[1])
	own = append(own, closedPR)
	newWorkAPI(t, fmt.Sprintf("repos/%s/pulls/%d", queueRepo, closedPR), "-X", "PATCH", "-f", "state=closed")
	// The cleanup reads only what is open, so the closed pull request's branch goes now; the link stays with the
	// pull request's body.
	newWorkAPI(t, "repos/"+queueRepo+"/git/refs/heads/"+branch, "-X", "DELETE")
	awaitLinkedWorkListed(t, issues, readyPR, closedPR)
	return issues[0], issues[1], issues[2]
}

// linkedWorkPull opens, from a branch of its own that adds one file in linkedWorkDir, a pull request of queueRepo into
// its default branch, which links the issue with Closes #<issue>. It is ready for review, not a draft, and carries none
// of the task's marking, neither the label harmflow nor the line naming the task: what makes it someone else's, though
// the account that opens it is also the session's. It is labelled linkedWorkLabel, by which the cleanup finds it. It
// returns the pull request's number and its branch.
func linkedWorkPull(t *testing.T, run string, which, issue int) (int, string) {
	t.Helper()
	base := newWorkAPI(t, "repos/"+queueRepo, "--jq", ".default_branch")
	sha := newWorkAPI(t, "repos/"+queueRepo+"/git/ref/heads/"+base, "--jq", ".object.sha")
	branch := fmt.Sprintf("%s/%s-%d", linkedWorkDir, run, which)
	file := branch + ".txt"
	newWorkAPI(t, "repos/"+queueRepo+"/git/refs", "-f", "ref=refs/heads/"+branch, "-f", "sha="+sha)
	_, err := newWorkGh("repos/"+queueRepo+"/contents/"+file, "-X", "PUT", "-f", "branch="+branch, "-f", "message=Add "+file,
		"-f", "content="+base64.StdEncoding.EncodeToString([]byte(run+"\n")))
	var number string
	if err == nil {
		body := fmt.Sprintf("Adds `%s`.\n\nCloses #%d\n", file, issue)
		number, err = newWorkGh("repos/"+queueRepo+"/pulls", "-f", "title=Add the note "+file,
			"-f", "head="+branch, "-f", "base="+base, "-F", "draft=false", "-f", "body="+body, "--jq", ".number")
	}
	if err != nil {
		// With no pull request, nothing labelled leads the cleanup to the branch.
		_, _ = newWorkGh("repos/"+queueRepo+"/git/refs/heads/"+branch, "-X", "DELETE")
		t.Fatalf("open the pull request from %s: %v", branch, err)
	}
	pr, err := strconv.Atoi(number)
	if err != nil {
		t.Fatalf("open the pull request from %s: %v", branch, err)
	}
	newWorkAPI(t, "repos/"+queueRepo+"/issues/"+number+"/labels", "-f", "labels[]="+linkedWorkLabel)
	return pr, branch
}

// awaitLinkedWorkListed waits until GitHub shows the fixtures as the session reads them: the open issues of queueRepo
// labelled linkedWorkLabel are the issues, in the order they were opened, the ready pull request is linked to the
// first, the closed one to the second, and none to the last. GitHub lists a new issue under its label, and links a
// pull request to the issue it closes, only some seconds after it makes them, and a session that ran before would
// rightly take the first or the second as new work. It fails as a fixture error, not as the session's, after a minute.
func awaitLinkedWorkListed(t *testing.T, issues []int, ready, closed int) {
	t.Helper()
	var listed []int
	linking := make([][]int, len(issues))
	var err error
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(time.Second) {
		if err = newWorkJSON(&listed, "repos/"+queueRepo+"/issues?state=open&sort=created&direction=asc&per_page=100&labels="+
			linkedWorkLabel, "--jq", "[.[] | select(.pull_request | not) | .number]"); err != nil {
			continue
		}
		for i, n := range issues {
			if linking[i], err = newWorkLinking(n); err != nil {
				break
			}
		}
		if err == nil && slices.Equal(listed, issues) && slices.Contains(linking[0], ready) &&
			slices.Contains(linking[1], closed) && len(linking[2]) == 0 {
			return
		}
	}
	t.Fatalf("fixture error: after a minute, the open issues of %s labelled %s are %v and the pull requests linking "+
		"each are %v (%v); want %v, with #%d linking the first, #%d the second, and none the last", queueRepo,
		linkedWorkLabel, listed, linking, err, issues, ready, closed)
}
