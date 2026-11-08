package integration

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// priorityLabel marks what TestResumePriorityDecidesBetweenADraftAndANewIssue opens in queueRepo: its two fixture
// issues and the managed draft pull request that links the first. The test owns the label alone, so that its cleanup
// never closes another test's fixtures. The issues' files go in priorityDir, which the base branch never holds.
const (
	priorityLabel = "resume-priority-test"
	priorityDir   = "notes"
)

// retryPriorityLabel marks the two fixture issues TestResumePriorityDecidesBetweenWorkToRetryAndANewIssue opens in
// queueRepo, which the test owns alone, as priorityLabel is TestResumePriorityDecidesBetweenADraftAndANewIssue's.
const retryPriorityLabel = "retry-priority-test"

// alternateLabel marks what TestResumePriorityAlternatesBetweenDraftsAndNewIssues opens in queueRepo: its four fixture
// issues and the two managed draft pull requests that link the two oldest. The test owns it alone, as priorityLabel is
// TestResumePriorityDecidesBetweenADraftAndANewIssue's.
const alternateLabel = "resume-priority-alternate-test"

// priorityStandInReport is the report.json the report scenario of fake-claude writes, which the first execution's
// stand-in replaces with the fixture's.
const priorityStandInReport = `{"outcome":"completed","summary":"done","workItems":[{"key":"o/r#3",` +
	`"outcome":"completed","pullRequest":{"url":"https://github.com/o/r/pull/4","created":true,"draft":true},` +
	`"tests":{"status":"passed"}}]}`

// A GitHub task's resume priority decides what a session takes first when it has both a draft pull request to resume
// and a new eligible issue: the draft's issue, as work to resume, under resume_first, and the new issue under
// new_first. Each subtest opens in queueRepo, under priorityLabel, two fixture issues, oldest first, and a managed
// draft pull request that links the first. In a daemon home of its own, a first execution through a stand-in Claude
// Code reports the first issue's item completed with that draft and its tests failed, so the ledger keeps the item in
// progress, as it keeps every draft whose tests failed, and holds nothing for the second issue. Then the real Claude
// Code runs the built-in agent's copy and the built-in skills of this checkout once, one item a cycle, under the
// subtest's setting. The session is stopped once the daemon tracks the issue it selects, and its clone has no push URL,
// so no branch and no pull request of its reaches GitHub. The third setting, alternate, takes the draft here too, as it
// starts with work to resume; what sets it apart, the turns it takes, needs a cycle of several items, which
// TestResumePriorityAlternatesBetweenDraftsAndNewIssues runs.
func TestResumePriorityDecidesBetweenADraftAndANewIssue(t *testing.T) {
	claude := realClaude(t)
	for _, c := range []struct {
		strategy string
		// takesDraft is whether the session must select the draft's issue rather than the new one.
		takesDraft bool
		reason     string
	}{
		{"resume_first", true, "resume_first puts the draft to resume before the new issue"},
		{"new_first", false, "new_first puts the new issue before the draft to resume"},
	} {
		t.Run(c.strategy, func(t *testing.T) {
			resumable, fresh, draft := priorityFixtures(t, priorityLabel, true)
			clone := shallowClone(t)
			home := t.TempDir()
			d := startDaemon(t, home)
			want := priorityFailedDraft(t, d, clone, priorityTask(d, clone, c.strategy), resumable, fresh, draft)

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
			wantKey := issueKey(fresh)
			if c.takesDraft {
				wantKey = issueKey(resumable)
			}
			if selected := selectedWorkItems(t, dir); len(selected) != 1 || !strings.EqualFold(selected[0], wantKey) {
				t.Errorf("progress.jsonl selects %v, want %s alone, since %s: hf-github-issue-queue does not order as it "+
					"states\n%s", selected, wantKey, c.reason, runFiles(dir))
			}
			if c.takesDraft {
				if item := priorityQueued(d, issueKey(fresh)); item != nil {
					t.Errorf("GET /queue tracks %s, though the session took the draft to resume: %v", issueKey(fresh), item)
				}
			}
		})
	}
}

// A GitHub task's resume priority also decides between work to retry — an issue whose processing failed before any
// pull request linked it — and a new eligible issue: the issue to retry under resume_first, and the new issue under
// new_first, though the issue to retry is the older. Each subtest opens in queueRepo, under retryPriorityLabel, two
// fixture issues, oldest first, and no pull request. In a daemon home of its own, a first execution through a stand-in
// Claude Code reports the first issue's item failed with no pull request, so the ledger holds it failed once, under a
// retry limit of one, and holds nothing for the second issue. Then the real Claude Code runs the built-in agent's copy
// and the built-in skills of this checkout once, one item a cycle, under the subtest's setting. The session is stopped
// once the daemon tracks the issue it selects, and its clone has no push URL, so no branch and no pull request of its
// reaches GitHub. Its work_item.selected line must also name the list the issue came from.
func TestResumePriorityDecidesBetweenWorkToRetryAndANewIssue(t *testing.T) {
	claude := realClaude(t)
	for _, c := range []struct {
		strategy string
		// takesRetry is whether the session must select the issue to retry rather than the new one.
		takesRetry bool
		reason     string
	}{
		{"resume_first", true, "resume_first puts the work to retry before the new issue"},
		{"new_first", false, "new_first puts the new issue before the work to retry"},
	} {
		t.Run(c.strategy, func(t *testing.T) {
			retry, fresh := retryPriorityFixtures(t)
			clone := shallowClone(t)
			home := t.TempDir()
			d := startDaemon(t, home)
			stand := standIn(t, "fake-claude", "claude",
				"\nSCENARIO=ok\n", "\nSCENARIO=report\n",
				priorityStandInReport, retryPriorityReport(t, retry))
			useClaude(d, stand.path, clone)
			d.call("POST", "/tasks", map[string]any{"yaml": retryPriorityTask(d, clone, c.strategy)}, 201)

			first := d.call("POST", "/tasks/queue/run", nil, 202)["executionId"].(string)
			// The execution's own status is left unread: what the next execution reads is the item's.
			d.waitEnd(first, 2*time.Minute)
			// What an issue that failed with no pull request leaves in the ledger, and what makes it work to retry.
			want := map[string]any{"status": "failed", "failures": 1.0, "resumes": 0.0}
			item := priorityQueued(d, issueKey(retry))
			if !priorityHolds(item, want) || item["pullRequestUrl"] != nil {
				t.Fatalf("after the stand-in's execution, GET /queue gives %s as %v, want %v and no pull request",
					issueKey(retry), item, want)
			}
			if item := priorityQueued(d, issueKey(fresh)); item != nil {
				t.Fatalf("after the stand-in's execution, GET /queue tracks %s, which no execution took: %v", issueKey(fresh),
					item)
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

			if item := contextItem(t, dir, retry); !priorityHolds(item, want) || item["pullRequestUrl"] != nil {
				t.Errorf("the context.json of execution %s lists %s as %v, want %v and no pull request", id, issueKey(retry),
					item, want)
			}
			if item := contextItem(t, dir, fresh); item != nil {
				t.Errorf("the context.json of execution %s lists %s, which no execution took: %v", id, issueKey(fresh), item)
			}
			wantKey, wantList := issueKey(fresh), "new issues"
			if c.takesRetry {
				wantKey, wantList = issueKey(retry), "work to retry"
			}
			if selected := selectedWorkItems(t, dir); len(selected) != 1 || !strings.EqualFold(selected[0], wantKey) {
				t.Errorf("progress.jsonl selects %v, want %s alone, since %s: hf-github-issue-queue does not order as it "+
					"states\n%s", selected, wantKey, c.reason, runFiles(dir))
			}
			if msg := selectedMessage(t, dir); !strings.HasSuffix(msg, " — from "+wantList) {
				t.Errorf("the work_item.selected message is %q, want the issue's title and \" — from %s\"", msg, wantList)
			}
		})
	}
}

// Under alternate, a session that has both drafts to resume and new eligible issues takes one of each in turn, starting
// with a draft to resume. The test opens in queueRepo, under alternateLabel, four fixture issues, oldest first, and two
// managed draft pull requests, each linking one of the two oldest. In a daemon home of its own, a first execution
// through a stand-in Claude Code reports both drafts' items completed with their tests failed, so the ledger keeps both
// in progress and holds nothing for the two newest issues. Then the real Claude Code runs the built-in agent's copy and
// the built-in skills of this checkout once, four items a cycle, oldest first, under alternate. The drafts' issues are
// the two oldest, so a session that took the issues by age, or every draft first, would select two drafts in a row. A
// session selects each item as it takes it up, so it runs until its selection is whole, as awaitSelection waits for
// TestDiscoveryLimitCutsTheEligibleIssuesInOrder's, and is then stopped with its daemon unless it ended first. Its
// clone has no push URL, so the work of each item ends at its push at the latest, and no branch and no pull request of
// its reaches GitHub. Each work_item.selected line must also name the list its item came from.
func TestResumePriorityAlternatesBetweenDraftsAndNewIssues(t *testing.T) {
	claude := realClaude(t)
	resumable, fresh, drafts := alternateFixtures(t)
	clone := shallowClone(t)
	home := t.TempDir()
	d := startDaemon(t, home)
	task := labelledPriorityTask(d, clone, alternateLabel, "alternate")
	const from = "maxItemsPerCycle: 1"
	if !strings.Contains(task, from) {
		t.Fatalf("labelledPriorityTask's task has no %q to replace:\n%s", from, task)
	}
	task = strings.Replace(task, from, "maxItemsPerCycle: 4", 1)
	priorityFailedDrafts(t, d, clone, task, resumable, drafts, fresh)

	useClaude(d, claude, clone)
	id := d.call("POST", "/tasks/queue/run", nil, 202)["executionId"].(string)
	dir := executionDir(d, id)
	selected := awaitSelection(t, d, id, dir, len(resumable)+len(fresh))
	if x := d.call("GET", "/executions/"+id, nil, 200); x["endedAt"] == nil {
		// Stopped as TestDiscoveryLimitCutsTheEligibleIssuesInOrder stops its session: the selection was read from the
		// execution's files, so the daemon is not started again.
		crashMidRun(t, d)
	}

	// isDraft reports whether key names the issue of a draft to resume.
	isDraft := func(key string) bool {
		return slices.ContainsFunc(resumable, func(n int) bool { return strings.EqualFold(key, issueKey(n)) })
	}
	keys, garbled := selectedKeys(selected)
	if len(garbled) > 0 {
		t.Logf("progress.jsonl names work items with text after the issue number, read as the issues they name: %q",
			garbled)
	}
	var want []string
	for _, n := range append(slices.Clone(resumable), fresh...) {
		want = append(want, issueKey(n))
	}
	if !sameWorkItems(keys, want) {
		t.Errorf("progress.jsonl selects %q, read as %v, want %v, in turn: hf-github-issue-queue does not order as it "+
			"states\n%s", selected, keys, want, runFiles(dir))
	}
	for i, k := range keys {
		// alternate starts with a draft to resume, so the drafts' issues come first, third, and so on.
		if isDraft(k) != (i%2 == 0) {
			t.Errorf("progress.jsonl selects %v in this order, want a draft to resume and a new issue in turn, a draft "+
				"first, so that no two of one kind come in a row while both lists have items: hf-github-issue-queue does "+
				"not alternate as it states\n%s", keys, runFiles(dir))
			break
		}
	}
	for _, line := range selectedLines(t, dir) {
		key, _ := selectedKeys([]string{line.WorkItem})
		wantList := "new issues"
		if isDraft(key[0]) {
			wantList = "drafts to resume"
		}
		if !strings.HasSuffix(line.Message, " — from "+wantList) {
			t.Errorf("the work_item.selected message of %s is %q, want the issue's title and \" — from %s\"", line.WorkItem,
				line.Message, wantList)
		}
	}
}

// alternateFixtures opens the four fixture issues in queueRepo, labelled alternateLabel, one after the other, and a
// managed draft pull request linking each of the two oldest, as priorityFixtures opens its own. It returns the drafts'
// issues and the two newest issues, each oldest first, with the drafts in the order of their issues. It creates the
// labels it uses when queueRepo lacks them. What an earlier run left open under the label is closed first, and so is
// this run's when the test ends, failed or not. It returns once GitHub shows the fixtures as the session reads them.
func alternateFixtures(t *testing.T) (resumable, fresh []int, drafts []restartPR) {
	t.Helper()
	for _, l := range []string{alternateLabel, "harmflow"} {
		sandboxLabel(t, l)
	}
	closeLabelledFixtures(t, alternateLabel)
	var own []int
	t.Cleanup(func() { closeLabelledFixtures(t, alternateLabel, own...) })
	run := runSuffix()
	for which := 1; which <= 4; which++ {
		if which > 1 {
			// GitHub stamps an issue's creation to the second, and the queue reads the issues in that order.
			time.Sleep(time.Second)
		}
		own = append(own, priorityIssue(t, run, which, alternateLabel))
	}
	issues := slices.Clone(own)
	resumable, fresh = issues[:2], issues[2:]
	links := map[int]int{}
	for i, n := range resumable {
		draft := priorityDraft(t, run, i+1, n, alternateLabel, true)
		own = append(own, draft.Number)
		drafts = append(drafts, draft)
		links[n] = draft.Number
	}
	awaitPriorityListed(t, alternateLabel, issues, links)
	return resumable, fresh, drafts
}

// retryPriorityTask is the task priorityTask makes, with its eligible-issues line taking the open issues of queueRepo
// labelled retryPriorityLabel instead.
func retryPriorityTask(d *daemon, clone, strategy string) string {
	d.t.Helper()
	return labelledPriorityTask(d, clone, retryPriorityLabel, strategy)
}

// retryPriorityFixtures opens the two fixture issues in queueRepo, labelled retryPriorityLabel, one after the other,
// and returns their numbers, oldest first. It creates the label when queueRepo lacks it. What an earlier run left open
// under the label is closed first, as priorityFixtures does, and so is this run's when the subtest ends, failed or not.
// It returns once GitHub lists the two issues under the label, oldest first, as the session reads them.
func retryPriorityFixtures(t *testing.T) (retry, fresh int) {
	t.Helper()
	sandboxLabel(t, retryPriorityLabel)
	closeLabelledFixtures(t, retryPriorityLabel)
	var own []int
	t.Cleanup(func() { closeLabelledFixtures(t, retryPriorityLabel, own...) })
	run := runSuffix()
	for which := 1; which <= 2; which++ {
		if which > 1 {
			// GitHub stamps an issue's creation to the second, and the queue reads the issues in that order.
			time.Sleep(time.Second)
		}
		own = append(own, priorityIssue(t, run, which, retryPriorityLabel))
	}
	want := slices.Clone(own)
	var listed []int
	var err error
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(time.Second) {
		if listed, err = labelledIssues(retryPriorityLabel); err == nil && slices.Equal(listed, want) {
			return want[0], want[1]
		}
	}
	t.Fatalf("after a minute, the open issues of %s labelled %s are %v (%v), want %v", queueRepo, retryPriorityLabel,
		listed, err, want)
	return 0, 0
}

// retryPriorityReport is the report.json of an execution whose processing of the issue failed before it opened any
// pull request: the issue's item failed, as new work, with no branch, no pull request, and no tests run.
func retryPriorityReport(t *testing.T, issue int) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"outcome": "failed",
		"summary": "The push failed, so no pull request was opened.",
		"workItems": []any{map[string]any{
			"key": issueKey(issue), "kind": "new", "repository": queueRepo, "issueNumber": issue, "outcome": "failed",
			"tests": map[string]any{"status": "not_run", "summary": "no test ran"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The stand-in writes the report between single quotes.
	if strings.Contains(string(data), "'") {
		t.Fatalf("the stand-in's report %s holds a single quote", data)
	}
	return string(data)
}

// selectedMessage is the message of the first work_item.selected line of the session's progress.jsonl, empty when it
// has none.
func selectedMessage(t *testing.T, dir string) string {
	t.Helper()
	if lines := selectedLines(t, dir); len(lines) > 0 {
		return lines[0].Message
	}
	return ""
}

// selectedLine is a work_item.selected line of a session's progress.jsonl: the work item it names, and its message.
type selectedLine struct {
	WorkItem string `json:"workItem"`
	Message  string `json:"message"`
}

// selectedLines lists the work_item.selected lines of the session's progress.jsonl, in order.
func selectedLines(t *testing.T, dir string) []selectedLine {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "progress.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var lines []selectedLine
	for _, l := range strings.Split(string(data), "\n") {
		var line struct {
			Type string `json:"type"`
			selectedLine
		}
		if json.Unmarshal([]byte(l), &line) == nil && line.Type == "work_item.selected" {
			lines = append(lines, line.selectedLine)
		}
	}
	return lines
}

// priorityTask is the task queueTask makes, with its eligible-issues line taking the open issues of queueRepo labelled
// priorityLabel, its queue in the oldest-first order, and strategy as its resume priority.
func priorityTask(d *daemon, clone, strategy string) string {
	d.t.Helper()
	return labelledPriorityTask(d, clone, priorityLabel, strategy)
}

// labelledPriorityTask is the task queueTask makes, with its eligible-issues line taking the open issues of queueRepo
// labelled label, its queue in the oldest-first order, and strategy as its resume priority.
func labelledPriorityTask(d *daemon, clone, label, strategy string) string {
	d.t.Helper()
	task := issueQueueTask(d, clone,
		fmt.Sprintf("open issues of %s labelled %s; every other issue is excluded.", queueRepo, label), "oldest_first")
	const from = "resumeStrategy: resume_first"
	if !strings.Contains(task, from) {
		d.t.Fatalf("issueQueueTask's task has no %q to replace:\n%s", from, task)
	}
	return strings.Replace(task, from, "resumeStrategy: "+strategy, 1)
}

// priorityFixtures opens the two fixture issues in queueRepo, labelled label, one after the other, and the draft pull
// request that links the first, which carries the task's marking when marked, and returns the issues' numbers, oldest
// first, with the pull request. It creates the labels it uses when queueRepo lacks them. What an earlier run left open
// under label, when it was stopped before its cleanup, would come first in the queue's order, so it is closed first;
// when the test ends, failed or not, the same cleanup closes this run's. It returns once GitHub shows the fixtures as
// the session reads them.
func priorityFixtures(t *testing.T, label string, marked bool) (resumable, fresh int, draft restartPR) {
	t.Helper()
	labels := []string{label}
	if marked {
		labels = append(labels, "harmflow")
	}
	for _, l := range labels {
		sandboxLabel(t, l)
	}
	closeLabelledFixtures(t, label)
	var own []int
	t.Cleanup(func() { closeLabelledFixtures(t, label, own...) })
	run := runSuffix()
	for which := 1; which <= 2; which++ {
		if which > 1 {
			// GitHub stamps an issue's creation to the second, and the queue reads the issues in that order.
			time.Sleep(time.Second)
		}
		own = append(own, priorityIssue(t, run, which, label))
	}
	resumable, fresh = own[0], own[1]
	draft = priorityDraft(t, run, 1, resumable, label, marked)
	own = append(own, draft.Number)
	awaitPriorityListed(t, label, []int{resumable, fresh}, map[int]int{resumable: draft.Number})
	return resumable, fresh, draft
}

// priorityFile is the file the run's which-th fixture issue asks for.
func priorityFile(run string, which int) string {
	return fmt.Sprintf("%s/%s-%d.txt", priorityDir, run, which)
}

// priorityIssue opens the run's which-th fixture issue in queueRepo, labelled label, and returns its number. Its text
// reads as a small real task, as the sandbox asks of every fixture.
func priorityIssue(t *testing.T, run string, which int, label string) int {
	t.Helper()
	file := priorityFile(run, which)
	out := newWorkAPI(t, "repos/"+queueRepo+"/issues", "-f", "title=Add the note "+file,
		"-f", fmt.Sprintf("body=Add the file `%s` with the single line `%s`, and change nothing else. "+
			"The repository has no tests.\n", file, run),
		"-f", "labels[]="+label, "--jq", ".number")
	n, err := strconv.Atoi(out)
	if err != nil {
		t.Fatalf("open the fixture issue %d: %q, %v", which, out, err)
	}
	return n
}

// priorityDraft opens, from a branch of its own whose one commit adds the file the issue asks for, a draft pull request
// of queueRepo into its default branch, which links the issue, the run's which-th fixture, with Closes #<issue>. When
// marked, it carries the task's marking: the label harmflow and the line naming the task queue, which queueTask's task
// is; otherwise it carries neither. It is also labelled label, by which the cleanup finds it. The branch is named
// neither as the built-in agent names a work branch nor after the issue.
func priorityDraft(t *testing.T, run string, which, issue int, label string, marked bool) restartPR {
	t.Helper()
	base := sandboxBase(t)
	sha := newWorkAPI(t, "repos/"+queueRepo+"/git/ref/heads/"+base, "--jq", ".object.sha")
	branch := fmt.Sprintf("add-note-%s-%d", run, which)
	newWorkAPI(t, "repos/"+queueRepo+"/git/refs", "-f", "ref=refs/heads/"+branch, "-f", "sha="+sha)
	// Until the pull request carries label, nothing leads the cleanup to the branch, and deleting the branch
	// closes a pull request still open from it.
	abandon := func(format string, args ...any) {
		t.Helper()
		_, _ = newWorkGh("repos/"+queueRepo+"/git/refs/heads/"+branch, "-X", "DELETE")
		t.Fatalf(format, args...)
	}
	file := priorityFile(run, which)
	_, err := newWorkGh("repos/"+queueRepo+"/contents/"+file, "-X", "PUT", "-f", "branch="+branch,
		"-f", "message=Add "+file, "-f", "content="+base64.StdEncoding.EncodeToString([]byte(run+"\n")))
	var out string
	if err == nil {
		body := fmt.Sprintf("Adds `%s`.\n\nCloses #%d\n", file, issue)
		if marked {
			body += "\nManaged by HarmFlow task queue.\n"
		}
		out, err = newWorkGh("repos/"+queueRepo+"/pulls", "-f", "title=Add the note "+file, "-f", "head="+branch,
			"-f", "base="+base, "-F", "draft=true", "-f", "body="+body, "--jq", "{number, url: .html_url}")
	}
	if err != nil {
		abandon("open the draft pull request from %s: %v", branch, err)
	}
	pr := restartPR{Head: branch, State: "OPEN", Draft: true}
	if err := json.Unmarshal([]byte(out), &pr); err != nil || pr.Number == 0 || pr.URL == "" {
		abandon("open the draft pull request from %s: %q, %v", branch, out, err)
	}
	labels := []string{"-f", "labels[]=" + label}
	if marked {
		labels = append(labels, "-f", "labels[]=harmflow")
	}
	if _, err := newWorkGh(append([]string{fmt.Sprintf("repos/%s/issues/%d/labels", queueRepo, pr.Number)}, labels...)...); err != nil {
		abandon("label the draft pull request %s: %v", pr.URL, err)
	}
	return pr
}

// awaitPriorityListed waits until GitHub shows the fixtures as the session reads them: the open issues of queueRepo
// labelled label are issues, oldest first, and each pull request of links, by the issue it links, is linked to that
// issue. GitHub lists a new issue under its label, and links a pull request to the issue it closes, only some seconds
// after it makes them, and a session that ran before would find no draft to resume.
func awaitPriorityListed(t *testing.T, label string, issues []int, links map[int]int) {
	t.Helper()
	var listed []int
	linking := map[int][]int{}
	var err error
	linked := func() bool {
		for issue, pr := range links {
			if linking[issue], err = newWorkLinking(issue); err != nil || !slices.Contains(linking[issue], pr) {
				return false
			}
		}
		return true
	}
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(time.Second) {
		if listed, err = labelledIssues(label); err != nil {
			continue
		}
		if slices.Equal(listed, issues) && linked() {
			return
		}
	}
	t.Fatalf("after a minute, the open issues of %s labelled %s are %v and the pull requests linking them are %v (%v); "+
		"want %v, and the pull requests of %v, by issue, among them", queueRepo, label, listed, linking, err, issues, links)
}

// priorityReport is the report.json of an execution that opened a draft pull request for each issue, the one of drafts
// at the same index, and whose tests failed: each issue's item completed, as new work, with its pull request's branch,
// the pull request, and its tests failed.
func priorityReport(t *testing.T, issues []int, drafts []restartPR) string {
	t.Helper()
	var items []any
	for i, issue := range issues {
		pr := drafts[i]
		items = append(items, map[string]any{
			"key": issueKey(issue), "kind": "new", "repository": queueRepo, "issueNumber": issue, "branch": pr.Head,
			"outcome":     "completed",
			"pullRequest": map[string]any{"number": pr.Number, "url": pr.URL, "draft": true, "created": true},
			"tests":       map[string]any{"status": "failed", "summary": "1 test failed"},
		})
	}
	data, err := json.Marshal(map[string]any{
		"outcome":   "completed",
		"summary":   "Opened draft pull requests whose tests failed.",
		"workItems": items,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The stand-in writes the report between single quotes.
	if strings.Contains(string(data), "'") {
		t.Fatalf("the stand-in's report %s holds a single quote", data)
	}
	return string(data)
}

// priorityFailedDraft creates the task, given as YAML, and runs it once through a stand-in Claude Code that reports the
// resumable issue's item completed with the draft pull request and its tests failed. It checks that the ledger then
// keeps the item in progress, as it keeps every draft whose tests failed, and holds nothing for the fresh issue, and
// returns the fields the item holds there, which the next execution must find.
func priorityFailedDraft(t *testing.T, d *daemon, clone, task string, resumable, fresh int, draft restartPR) map[string]any {
	t.Helper()
	return priorityFailedDrafts(t, d, clone, task, []int{resumable}, []restartPR{draft}, []int{fresh})[0]
}

// priorityFailedDrafts is priorityFailedDraft for several drafts: the stand-in reports the item of each resumable issue
// completed with the draft of drafts at the same index and its tests failed, and the ledger must then keep each item in
// progress and hold nothing for any fresh issue. It returns the fields each item holds there, in the issues' order.
func priorityFailedDrafts(t *testing.T, d *daemon, clone, task string, resumable []int, drafts []restartPR, fresh []int) []map[string]any {
	t.Helper()
	stand := standIn(t, "fake-claude", "claude",
		"\nSCENARIO=ok\n", "\nSCENARIO=report\n",
		`{"type":"test.completed","status":"passed","message":"12 passed"}`,
		`{"type":"test.completed","status":"failed","message":"1 failed"}`,
		priorityStandInReport, priorityReport(t, resumable, drafts))
	useClaude(d, stand.path, clone)
	d.call("POST", "/tasks", map[string]any{"yaml": task}, 201)

	first := d.call("POST", "/tasks/queue/run", nil, 202)["executionId"].(string)
	if x := d.waitEnd(first, 2*time.Minute); x["status"] != "completed" {
		t.Fatalf("the stand-in's execution %s = %v (%v), want completed\n%s", first, x["status"], x["errorMessage"],
			runFiles(executionDir(d, first)))
	}
	var wants []map[string]any
	for i, n := range resumable {
		want := map[string]any{"status": "in_progress", "testsStatus": "failed", "resumes": 0.0, "branch": drafts[i].Head,
			"pullRequestUrl": drafts[i].URL}
		if item := priorityQueued(d, issueKey(n)); !priorityHolds(item, want) {
			t.Fatalf("after the stand-in's execution, GET /queue gives %s as %v, want %v", issueKey(n), item, want)
		}
		wants = append(wants, want)
	}
	for _, n := range fresh {
		if item := priorityQueued(d, issueKey(n)); item != nil {
			t.Fatalf("after the stand-in's execution, GET /queue tracks %s, which no execution took: %v", issueKey(n), item)
		}
	}
	return wants
}

// priorityQueued is the daemon's work item under key, as GET /queue lists it: nil when it lists none.
func priorityQueued(d *daemon, key string) map[string]any {
	d.t.Helper()
	items, _ := d.call("GET", "/queue", nil, 200)["workItems"].([]any)
	for _, x := range items {
		if item, _ := x.(map[string]any); item != nil && strings.EqualFold(fmt.Sprint(item["key"]), key) {
			return item
		}
	}
	return nil
}

// priorityHolds reports whether item holds each field of want with its value.
func priorityHolds(item, want map[string]any) bool {
	for k, v := range want {
		if item[k] != v {
			return false
		}
	}
	return true
}
