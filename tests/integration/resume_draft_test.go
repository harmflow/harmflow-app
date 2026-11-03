package integration

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The fixture set of TestDraftWhoseTestsFailedIsResumedOnItsPullRequest in queueRepo, apart from the other sets, which
// it never reads or changes: one issue labelled resumeDraftLabel and the draft pull request that links it, both opened
// by each run and closed when it ends. The issue's two files go in resumeDraftDir, which the base branch never holds: a
// branch that holds it is one pushed for a fixture.
const (
	resumeDraftLabel = "resume-draft-test"
	resumeDraftDir   = "messages"
	// resumeDraftTask is the id of the task failingTestsTask makes, which the draft's marking line names.
	resumeDraftTask = "failing-tests"
)

// standInReport is the report.json the report scenario of fake-claude writes, which the first execution's stand-in
// replaces with the fixture's.
const standInReport = `{"outcome":"completed","summary":"done","workItems":[{"key":"o/r#3","outcome":"completed",` +
	`"pullRequest":{"url":"https://github.com/o/r/pull/4","created":true,"draft":true},"tests":{"status":"passed"}}]}`

// A draft pull request whose tests failed, left by an execution that ended completed, is resumed by a later execution
// on that same pull request: the session works on its branch, updates it, and opens no other. The fixture issue asks
// for a change in two parts, and the test opens a managed draft pull request that links it, from a branch of its own
// whose one commit makes the first part. A first execution, through a stand-in Claude Code, reports the issue's item
// completed with its tests failed and that pull request, so the daemon's ledger keeps the item in progress, as it keeps
// every draft whose tests failed. Then the real Claude Code runs the built-in agent's copy and the built-in skills of
// this checkout against queueRepo once, from a clone that may push. It selects the fixture issue alone and reports it
// resumed, with the test's pull request, which it updated during the execution and left an open draft whose branch
// holds a commit beyond the test's. No other pull request links the issue.
func TestDraftWhoseTestsFailedIsResumedOnItsPullRequest(t *testing.T) {
	claude := realClaude(t)
	for _, label := range []string{resumeDraftLabel, "harmflow"} {
		sandboxLabel(t, label)
	}
	base := sandboxBase(t)
	sweepResumeDraftFixtures(t, base)
	issue := suiteFixture(t, resumeDraftLabel, base, "Add the welcome and farewell messages",
		"New users get no greeting, and leaving says nothing either. Add two short messages under `"+resumeDraftDir+
			"/`, one file each:\n\n"+
			"1. `"+resumeDraftDir+"/welcome.txt`, with the single line `Welcome aboard.`\n"+
			"2. `"+resumeDraftDir+"/farewell.txt`, with the single line `See you soon.`\n\n"+
			"Change nothing else. The repository has no tests.\n")
	t.Cleanup(func() { sweepResumeDraftFixtures(t, base, issue) })
	draft, pushed := resumeDraft(t, base, issue)
	key := issueKey(issue)

	clone := pushableClone(t)
	// The clone is shallow, which also makes it fetch its default branch alone. The draft's branch was pushed from
	// elsewhere, and the session works on it as origin/<branch>, which a fetch writes only under a refspec of every
	// branch.
	git(t, clone, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	home := t.TempDir()
	d := startDaemon(t, home)
	stand := standIn(t, "fake-claude", "claude",
		"\nSCENARIO=ok\n", "\nSCENARIO=report\n",
		`{"type":"test.completed","status":"passed","message":"12 passed"}`,
		`{"type":"test.completed","status":"failed","message":"1 failed"}`,
		standInReport, failedDraftReport(t, issue, draft))
	useClaude(d, stand.path, clone)
	d.call("POST", "/tasks", map[string]any{"yaml": failingTestsTask(d, clone, base, resumeDraftLabel, 0)}, 201)

	first := d.call("POST", "/tasks/"+resumeDraftTask+"/run", nil, 202)["executionId"].(string)
	if x := d.waitEnd(first, 2*time.Minute); x["status"] != "completed" {
		t.Fatalf("the stand-in's execution %s = %v (%v), want completed\n%s", first, x["status"], x["errorMessage"],
			runFiles(executionDir(d, first)))
	}
	// What a draft whose tests failed leaves in the ledger, and what the next execution must find there.
	want := map[string]any{"status": "in_progress", "testsStatus": "failed", "resumes": 0.0, "branch": draft.Head,
		"pullRequestUrl": draft.URL}
	if item := queuedItem(d, key); !holdsAll(item, want) {
		t.Fatalf("after the stand-in's execution, GET /queue gives %s as %v, want %v", key, item, want)
	}

	useClaude(d, claude, clone)
	next := d.call("POST", "/tasks/"+resumeDraftTask+"/run", nil, 202)["executionId"].(string)
	dir := executionDir(d, next)
	// Longer than the task's timeout, so that the execution's own end says why a session ran too long.
	x := d.waitEnd(next, withinDeadline(t, 13*time.Minute))
	if x["status"] != "completed" {
		t.Errorf("execution %s = %v (%v), want completed\n%s", next, x["status"], x["errorMessage"], runFiles(dir))
	}
	if item := contextItem(t, dir, issue); !holdsAll(item, want) {
		t.Errorf("the context.json of execution %s lists %s as %v, want %v", next, key, item, want)
	}
	if selected := selectedWorkItems(t, dir); len(selected) != 1 || !strings.EqualFold(selected[0], key) {
		t.Errorf("progress.jsonl selects %v, want %s alone\n%s", selected, key, runFiles(dir))
	}
	checkUpdatedPullRequest(t, d, next, dir, issue, draft)
	// The resume ends the item completed, which the ledger archives and GET /queue leaves out.
	if item := resumeLimitHeld(t, home, resumeDraftTask, key); item == nil || item["resumes"] != 1.0 {
		t.Errorf("after execution %s, the ledger holds %s as %v, want it resumed once", next, key, item)
	}

	prs, err := linkingPullRequests(issue)
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 1 || prs[0].Number != draft.Number {
		t.Fatalf("the pull requests of %s linking #%d are %+v, want %s alone, the one the test opened\n%s", queueRepo,
			issue, prs, draft.URL, runFiles(dir))
	}
	pr := prs[0]
	if pr.State != "OPEN" || !pr.Draft || pr.Merged {
		t.Errorf("%s is %s, draft %v, merged %v; want an open draft, not merged", pr.URL, pr.State, pr.Draft, pr.Merged)
	}
	if status, ahead := compareCommits(t, pushed, pr.HeadOID); status != "ahead" || ahead < 1 {
		t.Errorf("the head %s of %s is %s of the test's commit %s by %d commits, want ahead by one or more\n%s", pr.HeadOID,
			pr.URL, status, pushed, ahead, commandsOf(d, next))
	}
}

// resumeDraft opens a draft pull request of queueRepo into base from a branch of its own, and returns it with the one
// commit its branch holds beyond base, which adds the first of the fixture issue's two files. The pull request links
// the issue with Closes #<issue>, and carries the marking of the task failingTestsTask makes: the label harmflow, and
// the line naming the task resumeDraftTask. Its branch is named neither as the built-in agent names a work branch nor
// after the issue, so a session finds it only from the pull request. It returns once GitHub links the pull request to
// the issue, as the only one that links it: the session finds the draft to resume by that link.
func resumeDraft(t *testing.T, base string, issue int) (restartPR, string) {
	t.Helper()
	return openFixturePR(t, base, issue, fixturePR{
		Branch:  "add-messages-" + runSuffix(),
		File:    resumeDraftDir + "/welcome.txt",
		Content: "Welcome aboard.\n",
		Message: "Add the welcome message",
		Title:   "Add the welcome and farewell messages",
		Body: fmt.Sprintf("Adds `%s/welcome.txt`. The farewell message is still to do.\n\nCloses #%d\n\n"+
			"Managed by HarmFlow task %s.\n", resumeDraftDir, issue, resumeDraftTask),
		Draft:  true,
		Labels: []string{"harmflow"},
		Alone:  true,
	})
}

// fixturePR is a pull request of a fixture set that openFixturePR opens: from Branch, which it creates at the base
// branch, with one commit that adds File with Content and the commit message Message, under Title and Body. It is a
// draft when Draft is set, and carries Labels. Its zero values open a ready pull request with no label, and wait until
// GitHub lists it among the pull requests that link the issue; Alone waits until it is the only one.
type fixturePR struct {
	Branch, File, Content, Message, Title, Body string
	Draft                                       bool
	Labels                                      []string
	Alone                                       bool
}

// openFixturePR opens the pull request spec describes in queueRepo into base, for the fixture issue its body closes,
// and returns it with the one commit its branch holds beyond base. When that commit fails, it deletes the branch: with
// no fixture file on it, nothing would lead a cleanup to it. It returns once GitHub links the pull request to the
// issue, as a session reads it.
func openFixturePR(t *testing.T, base string, issue int, spec fixturePR) (restartPR, string) {
	t.Helper()
	sha := newWorkAPI(t, "repos/"+queueRepo+"/git/ref/heads/"+base, "--jq", ".object.sha")
	newWorkAPI(t, "repos/"+queueRepo+"/git/refs", "-f", "ref=refs/heads/"+spec.Branch, "-f", "sha="+sha)
	commit, err := newWorkGh("repos/"+queueRepo+"/contents/"+spec.File, "-X", "PUT", "-f", "branch="+spec.Branch,
		"-f", "message="+spec.Message, "-f", "content="+base64.StdEncoding.EncodeToString([]byte(spec.Content)),
		"--jq", ".commit.sha")
	if err != nil {
		_, _ = newWorkGh("repos/"+queueRepo+"/git/refs/heads/"+spec.Branch, "-X", "DELETE")
		t.Fatalf("commit %s on %s: %v", spec.File, spec.Branch, err)
	}
	out := newWorkAPI(t, "repos/"+queueRepo+"/pulls", "-f", "title="+spec.Title, "-f", "head="+spec.Branch,
		"-f", "base="+base, "-F", "draft="+strconv.FormatBool(spec.Draft), "-f", "body="+spec.Body,
		"--jq", "{number, url: .html_url}")
	pr := restartPR{Head: spec.Branch, State: "OPEN", Draft: spec.Draft}
	if err := json.Unmarshal([]byte(out), &pr); err != nil || pr.Number == 0 || pr.URL == "" {
		t.Fatalf("open the pull request from %s: %q, %v", spec.Branch, out, err)
	}
	if len(spec.Labels) > 0 {
		args := []string{fmt.Sprintf("repos/%s/issues/%d/labels", queueRepo, pr.Number)}
		for _, label := range spec.Labels {
			args = append(args, "-f", "labels[]="+label)
		}
		newWorkAPI(t, args...)
	}
	want := "among them"
	if spec.Alone {
		want = "alone"
	}
	// GitHub links a pull request to the issue it closes some seconds after it opens it.
	var linked []linkedPR
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(time.Second) {
		if linked, err = linkingPullRequests(issue); err == nil && links(linked, pr.Number, spec.Alone) {
			return pr, commit
		}
	}
	t.Fatalf("after a minute, the pull requests linking #%d are %+v (%v), want %s %s", issue, linked, err, pr.URL, want)
	return restartPR{}, ""
}

// links reports whether the pull request number is among the linked pull requests, or, when alone is set, the only one.
func links(linked []linkedPR, number int, alone bool) bool {
	if alone {
		return len(linked) == 1 && linked[0].Number == number
	}
	return slices.ContainsFunc(linked, func(l linkedPR) bool { return l.Number == number })
}

// failedDraftReport is the report.json of an execution that opened the draft pull request for the issue and whose
// tests failed: the issue's item completed, as new work, with its branch, the pull request, and its tests failed.
func failedDraftReport(t *testing.T, issue int, pr restartPR) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"outcome": "completed",
		"summary": "Opened a draft pull request whose tests failed.",
		"workItems": []any{map[string]any{
			"key": issueKey(issue), "kind": "new", "repository": queueRepo, "issueNumber": issue, "branch": pr.Head,
			"outcome":     "completed",
			"pullRequest": map[string]any{"number": pr.Number, "url": pr.URL, "draft": true, "created": true},
			"tests":       map[string]any{"status": "failed", "summary": "1 test failed"},
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

// sweepResumeDraftFixtures removes from queueRepo what the test's runs left: what sweepSuiteFixtures finds by the
// fixture issues and their label, and then every branch but base that still holds resumeDraftDir, such as one a session
// pushed for the issue with no pull request linking it, or the test's own when GitHub had not linked its pull request
// yet. Deleting a branch closes the pull request still open from it.
func sweepResumeDraftFixtures(t *testing.T, base string, own ...int) {
	t.Helper()
	sweepSuiteFixtures(t, resumeDraftLabel, base, own...)
	branches, err := branchesHolding(resumeDraftDir)
	if err != nil {
		t.Errorf("cleanup: %v", err)
	}
	for _, b := range branches {
		if b == base {
			t.Errorf("cleanup: %s holds %s, and stays: it is the base branch", base, resumeDraftDir)
			continue
		}
		if _, err := newWorkGh("repos/"+queueRepo+"/git/refs/heads/"+b, "-X", "DELETE"); err != nil {
			t.Errorf("cleanup: deleting the branch %s: %v", b, err)
		}
	}
}

// queuedItem is the daemon's work item under key, as GET /queue lists it: nil when it lists none.
func queuedItem(d *daemon, key string) map[string]any {
	d.t.Helper()
	items, _ := d.call("GET", "/queue", nil, 200)["workItems"].([]any)
	for _, x := range items {
		if item, _ := x.(map[string]any); item != nil && strings.EqualFold(fmt.Sprint(item["key"]), key) {
			return item
		}
	}
	return nil
}

// holdsAll reports whether item holds each field of want with its value.
func holdsAll(item, want map[string]any) bool {
	for k, v := range want {
		if item[k] != v {
			return false
		}
	}
	return true
}

// compareCommits is how GitHub compares the commit head of queueRepo with base: its status, ahead when head descends
// from base, and how many commits head holds beyond base.
func compareCommits(t *testing.T, base, head string) (string, int) {
	t.Helper()
	out := newWorkAPI(t, "repos/"+queueRepo+"/compare/"+base+"..."+head, "--jq", `"\(.status) \(.ahead_by)"`)
	status, ahead, _ := strings.Cut(out, " ")
	n, err := strconv.Atoi(ahead)
	if err != nil {
		t.Fatalf("compare %s with %s in %s: %q", head, base, queueRepo, out)
	}
	return status, n
}
