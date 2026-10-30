package integration

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The fixture set of TestDraftAtItsResumeLimitIsNotResumed in queueRepo, apart from the other sets, which it never
// reads or changes: one issue labelled resumeLimitLabel and the managed draft pull request that links it, both opened
// by each run and closed when it ends. The draft's one file goes in resumeLimitDir, which the base branch never holds.
const (
	resumeLimitLabel = "resume-limit-test"
	resumeLimitDir   = "reminders"
)

// resumeLimitStandInReport is the report.json the report scenario of fake-claude writes, which each stand-in
// execution replaces with the fixture's.
const resumeLimitStandInReport = `{"outcome":"completed","summary":"done","workItems":[{"key":"o/r#3",` +
	`"outcome":"completed","pullRequest":{"url":"https://github.com/o/r/pull/4","created":true,"draft":true},` +
	`"tests":{"status":"passed"}}]}`

// A managed draft pull request whose work item has been resumed as many times as the task's limit allows is not
// resumed again, and its item needs attention. The test opens in queueRepo, under resumeLimitLabel, one fixture issue
// and, from a branch of its own whose one commit adds the file the issue asks for, a managed draft pull request that
// links it. Its task, the one queueTask makes with its eligible issues set to the fixture's label, allows one
// resumption. Two executions through a stand-in Claude Code bring the issue's item to that limit: the first reports it
// completed as new work, with the draft and its tests failed, and the second the same, resumed. Then the real Claude
// Code runs the built-in agent's copy and the built-in skills of this checkout once, from a clone with no push URL, and
// its execution ends. It selects nothing for the item, logs no pull request opened or updated, and reports the item,
// if at all, as needing attention, which the daemon's ledger then reads. On GitHub the draft is as the test left it:
// open, a draft, at the test's commit, its body never edited, and the only pull request that closes the issue. Whether
// the ledger already marks the item at its limit or only the skill's check of its resumptions keeps it out, the test
// reads only what the session and GitHub show.
func TestDraftAtItsResumeLimitIsNotResumed(t *testing.T) {
	claude := realClaude(t)
	for _, label := range []string{resumeLimitLabel, "harmflow"} {
		sandboxLabel(t, label)
	}
	base := sandboxBase(t)
	sweepSuiteFixtures(t, resumeLimitLabel, base)
	run := runSuffix()
	file := resumeLimitDir + "/" + run + ".txt"
	issue := suiteFixture(t, resumeLimitLabel, base, "Add the reminder "+file,
		fmt.Sprintf("Add the file `%s` with the single line `%s`, and change nothing else. The repository has no tests.\n",
			file, run))
	draft, pushed := resumeLimitDraft(t, base, run, file, issue)
	key := issueKey(issue)

	clone := shallowClone(t)
	home := t.TempDir()
	d := startDaemon(t, home)
	kinds := []string{"new", "resume"}
	var stands []cli
	for _, kind := range kinds {
		stands = append(stands, standIn(t, "fake-claude", "claude",
			"\nSCENARIO=ok\n", "\nSCENARIO=report\n",
			`{"type":"test.completed","status":"passed","message":"12 passed"}`,
			`{"type":"test.completed","status":"failed","message":"1 failed"}`,
			resumeLimitStandInReport, resumeLimitReport(t, issue, draft, kind)))
	}
	useClaude(d, stands[0].path, clone)
	d.call("POST", "/tasks", map[string]any{"yaml": resumeLimitTask(d, clone)}, 201)
	for resumes, kind := range kinds {
		useClaude(d, stands[resumes].path, clone)
		id := d.call("POST", "/tasks/queue/run", nil, 202)["executionId"].(string)
		if x := d.waitEnd(id, 2*time.Minute); x["status"] != "completed" {
			t.Fatalf("the stand-in's execution %s, reporting the item %s, = %v (%v), want completed\n%s", id, kind,
				x["status"], x["errorMessage"], runFiles(executionDir(d, id)))
		}
		// Its status is left out: the ledger may mark the item at its limit, or only once it is past it.
		want := map[string]any{"testsStatus": "failed", "resumes": float64(resumes), "branch": draft.Head,
			"pullRequestUrl": draft.URL}
		if item := resumeLimitHeld(t, home, key); !resumeLimitHolds(item, want) {
			t.Fatalf("after the stand-in's execution %s, reporting the item %s, the ledger holds %s as %v, want %v", id,
				kind, key, item, want)
		}
	}

	useClaude(d, claude, clone)
	id := d.call("POST", "/tasks/queue/run", nil, 202)["executionId"].(string)
	dir := executionDir(d, id)
	// Longer than the task's timeout, so that the execution's own end says why a session ran too long.
	x := d.waitEnd(id, withinDeadline(t, 17*time.Minute))
	// Every check below is of something the session did not do, which a session that never ran would not do either.
	switch x["status"] {
	case "completed", "no_work", "needs_attention":
	default:
		t.Errorf("execution %s = %v (%v), want it ended by its session's report\n%s", id, x["status"], x["errorMessage"],
			runFiles(dir))
	}
	if _, err := os.Stat(filepath.Join(dir, "report.json")); err != nil {
		t.Errorf("execution %s left no report.json: %v", id, err)
	}
	atLimit := map[string]any{"testsStatus": "failed", "resumes": 1.0, "branch": draft.Head, "pullRequestUrl": draft.URL}
	if item := contextItem(t, dir, issue); !resumeLimitHolds(item, atLimit) {
		t.Errorf("the context.json of execution %s lists %s as %v, want %v", id, key, item, atLimit)
	}
	for _, selected := range selectedWorkItems(t, dir) {
		if strings.EqualFold(selected, key) {
			t.Errorf("progress.jsonl selects %s, whose resumptions reached the task's limit: hf-github-issue-queue does "+
				"not skip it as it states\n%s", key, runFiles(dir))
		}
	}
	for _, e := range d.events(id) {
		if e["type"] == "pull_request.created" || e["type"] == "pull_request.updated" {
			t.Errorf("execution %s logged %v for %v, want no pull request opened or updated: %v\n%s", id, e["type"],
				e["workItemId"], e["payload"], commandsOf(d, id))
		}
	}
	if outcome, _ := reportedResult(t, dir, issue); outcome != "" && outcome != "needs_attention" {
		t.Errorf("report.json gives %s the outcome %q and the kind %q, want needs_attention, or no entry\n%s", key,
			outcome, reportedKind(t, dir, issue), runFiles(dir))
	}
	if item := resumeLimitHeld(t, home, key); item == nil || item["status"] != "needs_attention" {
		t.Errorf("after execution %s, the ledger holds %s as %v, want it needs_attention", id, key, item)
	}
	checkResumeLimitDraft(t, issue, draft, pushed)
}

// resumeLimitTask is the task queueTask makes, with its eligible-issues line taking the open issues of queueRepo
// labelled resumeLimitLabel, and its queue allowing one resumption of a work item.
func resumeLimitTask(d *daemon, clone string) string {
	d.t.Helper()
	task := issueQueueTask(d, clone,
		fmt.Sprintf("open issues of %s labelled %s; every other issue is excluded.", queueRepo, resumeLimitLabel),
		"oldest_first")
	const after = "      resumeStrategy: resume_first\n"
	if !strings.Contains(task, after) {
		d.t.Fatalf("issueQueueTask's task has no %q to add the resume limit after:\n%s", after, task)
	}
	return strings.Replace(task, after, after+"      maxResumeAttempts: 1\n", 1)
}

// resumeLimitDraft opens a draft pull request of queueRepo into base from a branch of its own, and returns it with the
// one commit its branch holds beyond base, which adds the file the fixture issue asks for. The pull request links the
// issue with Closes #<issue>, and carries the marking of the task queueTask makes: the label harmflow, and the line
// naming the task queue. Its branch is named neither as the built-in agent names a work branch nor after the issue. It
// returns once GitHub links the pull request to the issue, as the session reads it.
func resumeLimitDraft(t *testing.T, base, run, file string, issue int) (restartPR, string) {
	t.Helper()
	branch := "add-reminder-" + run
	sha := newWorkAPI(t, "repos/"+queueRepo+"/git/ref/heads/"+base, "--jq", ".object.sha")
	newWorkAPI(t, "repos/"+queueRepo+"/git/refs", "-f", "ref=refs/heads/"+branch, "-f", "sha="+sha)
	// Until GitHub links the pull request to the issue, nothing leads the cleanup to the branch, and deleting the branch
	// closes a pull request still open from it.
	abandon := func(format string, args ...any) {
		t.Helper()
		_, _ = newWorkGh("repos/"+queueRepo+"/git/refs/heads/"+branch, "-X", "DELETE")
		t.Fatalf(format, args...)
	}
	commit, err := newWorkGh("repos/"+queueRepo+"/contents/"+file, "-X", "PUT", "-f", "branch="+branch,
		"-f", "message=Add "+file, "-f", "content="+base64.StdEncoding.EncodeToString([]byte(run+"\n")),
		"--jq", ".commit.sha")
	if err != nil {
		abandon("commit %s on %s: %v", file, branch, err)
	}
	body := fmt.Sprintf("Adds `%s`.\n\nCloses #%d\n\nManaged by HarmFlow task queue.\n", file, issue)
	out, err := newWorkGh("repos/"+queueRepo+"/pulls", "-f", "title=Add the reminder "+file, "-f", "head="+branch,
		"-f", "base="+base, "-F", "draft=true", "-f", "body="+body, "--jq", "{number, url: .html_url}")
	if err != nil {
		abandon("open the draft pull request from %s: %v", branch, err)
	}
	pr := restartPR{Head: branch, State: "OPEN", Draft: true}
	if err := json.Unmarshal([]byte(out), &pr); err != nil || pr.Number == 0 || pr.URL == "" {
		abandon("open the draft pull request from %s: %q, %v", branch, out, err)
	}
	if _, err := newWorkGh(fmt.Sprintf("repos/%s/issues/%d/labels", queueRepo, pr.Number), "-f",
		"labels[]=harmflow"); err != nil {
		abandon("label the draft pull request %s: %v", pr.URL, err)
	}
	var linked []linkedPR
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(time.Second) {
		if linked, err = linkingPullRequests(issue); err == nil && len(linked) == 1 && linked[0].Number == pr.Number {
			return pr, commit
		}
	}
	abandon("after a minute, the pull requests linking #%d are %+v (%v), want %s alone", issue, linked, err, pr.URL)
	return restartPR{}, ""
}

// resumeLimitReport is the report.json of an execution that worked the issue's draft pull request, as work of the
// kind, and whose tests failed: the issue's item completed, with the pull request's branch, the pull request, which
// only new work opened, and its tests failed. The stand-in writes the report between single quotes.
func resumeLimitReport(t *testing.T, issue int, pr restartPR, kind string) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"outcome": "completed",
		"summary": "Worked a draft pull request whose tests failed.",
		"workItems": []any{map[string]any{
			"key": issueKey(issue), "kind": kind, "repository": queueRepo, "issueNumber": issue, "branch": pr.Head,
			"outcome":     "completed",
			"pullRequest": map[string]any{"number": pr.Number, "url": pr.URL, "draft": true, "created": kind == "new"},
			"tests":       map[string]any{"status": "failed", "summary": "1 test failed"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "'") {
		t.Fatalf("the stand-in's report %s holds a single quote", data)
	}
	return string(data)
}

// checkResumeLimitDraft checks on GitHub that the draft pull request is as the test left it: open, a draft, and not
// merged; its head the commit the test pushed; its body never edited, by GitHub's record of the body's edits; and no
// other pull request with the issue among its closingIssuesReferences.
func checkResumeLimitDraft(t *testing.T, issue int, draft restartPR, pushed string) {
	t.Helper()
	linked, err := linkingPullRequests(issue)
	if err != nil {
		t.Fatal(err)
	}
	if len(linked) != 1 || linked[0].Number != draft.Number {
		t.Fatalf("the pull requests of %s linking #%d are %+v, want %s alone, the one the test opened", queueRepo, issue,
			linked, draft.URL)
	}
	pr := linked[0]
	if pr.State != "OPEN" || !pr.Draft || pr.Merged {
		t.Errorf("%s is %s, draft %v, merged %v; want an open draft, not merged", pr.URL, pr.State, pr.Draft, pr.Merged)
	}
	if pr.HeadOID != pushed {
		t.Errorf("the head of %s is %s, want %s, the commit the test pushed", pr.URL, pr.HeadOID, pushed)
	}
	if edited := lastBodyEdit(t, pr.Number); !edited.IsZero() {
		t.Errorf("GitHub records an edit of the body of %s at %v, want none since the test opened it", pr.URL, edited)
	}
	closing, err := issuePullRequests(issue, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range closing {
		if c.Number != draft.Number {
			t.Errorf("%s, from %s, has #%d among its closingIssuesReferences, want %s alone", c.URL, c.Head, issue,
				draft.URL)
		}
	}
}

// resumeLimitHeld is the queue task's work item under key as the daemon's ledger in home holds it, nil when it holds
// none. The ledger is read from its files, the archive of closed items first, then the items still in play, which hold
// an item's later copy: GET /queue lists only the items in play (harmflow-app#341), and an item at its resume limit
// needs attention.
func resumeLimitHeld(t *testing.T, home, key string) map[string]any {
	t.Helper()
	var held map[string]any
	for _, name := range []string{"workitems-archive.json", "workitems.json"} {
		data, err := os.ReadFile(filepath.Join(home, "state", name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		var f struct {
			Tasks map[string]map[string]map[string]any `json:"tasks"`
		}
		if err := json.Unmarshal(data, &f); err != nil {
			t.Fatal(err)
		}
		for k, item := range f.Tasks["queue"] {
			if strings.EqualFold(k, key) {
				held = item
			}
		}
	}
	return held
}

// resumeLimitHolds reports whether item holds each field of want with its value.
func resumeLimitHolds(item, want map[string]any) bool {
	for k, v := range want {
		if item[k] != v {
			return false
		}
	}
	return true
}
