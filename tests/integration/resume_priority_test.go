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

// priorityLabel marks what TestResumePriorityDecidesBetweenADraftAndANewIssue opens in queueRepo: its two fixture
// issues and the managed draft pull request that links the first. The test owns the label alone, so that its cleanup
// never closes another test's fixtures. The issues' files go in priorityDir, which the base branch never holds.
const (
	priorityLabel = "resume-priority-test"
	priorityDir   = "notes"
)

// priorityStandInReport is the report.json the report scenario of fake-claude writes, which the first execution's
// stand-in replaces with the fixture's.
const priorityStandInReport = `{"outcome":"completed","summary":"done","workItems":[{"key":"o/r#3",` +
	`"outcome":"completed","pullRequest":{"url":"https://github.com/o/r/pull/4","created":true,"draft":true},` +
	`"tests":{"status":"passed"}}]}`

// A GitHub task's resume priority decides what a session takes first when it has both a draft pull request to resume
// and a new eligible issue: the draft's issue, as work to resume, under resume_first, and the new issue under
// new_first. Each subtest opens in queueRepo, under priorityLabel, two fixture issues, oldest first, and a managed draft
// pull request that links the first. In a daemon home of its own, a first execution through a stand-in Claude Code
// reports the first issue's item completed with that draft and its tests failed, so the ledger keeps the item in
// progress, as it keeps every draft whose tests failed, and holds nothing for the second issue. Then the real Claude
// Code runs the built-in agent's copy and the built-in skills of this checkout once, one item a cycle, under the
// subtest's setting. The session is stopped once the daemon tracks the issue it selects, and its clone has no push URL,
// so no branch and no pull request of its reaches GitHub. The third setting, alternate, is left out: nothing settles
// yet what it does.
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
			resumable, fresh, draft := priorityFixtures(t)
			clone := shallowClone(t)
			home := t.TempDir()
			d := startDaemon(t, home)
			stand := standIn(t, "fake-claude", "claude",
				"\nSCENARIO=ok\n", "\nSCENARIO=report\n",
				`{"type":"test.completed","status":"passed","message":"12 passed"}`,
				`{"type":"test.completed","status":"failed","message":"1 failed"}`,
				priorityStandInReport, priorityReport(t, resumable, draft))
			useClaude(d, stand.path, clone)
			d.call("POST", "/tasks", map[string]any{"yaml": priorityTask(d, clone, c.strategy)}, 201)

			first := d.call("POST", "/tasks/queue/run", nil, 202)["executionId"].(string)
			if x := d.waitEnd(first, 2*time.Minute); x["status"] != "completed" {
				t.Fatalf("the stand-in's execution %s = %v (%v), want completed\n%s", first, x["status"], x["errorMessage"],
					runFiles(executionDir(d, first)))
			}
			// What a draft whose tests failed leaves in the ledger, and what the next execution must find there.
			want := map[string]any{"status": "in_progress", "testsStatus": "failed", "resumes": 0.0, "branch": draft.Head,
				"pullRequestUrl": draft.URL}
			if item := priorityQueued(d, issueKey(resumable)); !priorityHolds(item, want) {
				t.Fatalf("after the stand-in's execution, GET /queue gives %s as %v, want %v", issueKey(resumable), item, want)
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

// priorityTask is the task queueTask makes, with its eligible-issues line taking the open issues of queueRepo labelled
// priorityLabel, its queue in the oldest-first order, and strategy as its resume priority.
func priorityTask(d *daemon, clone, strategy string) string {
	d.t.Helper()
	task := issueQueueTask(d, clone,
		fmt.Sprintf("open issues of %s labelled %s; every other issue is excluded.", queueRepo, priorityLabel), "oldest_first")
	const from = "resumeStrategy: resume_first"
	if !strings.Contains(task, from) {
		d.t.Fatalf("issueQueueTask's task has no %q to replace:\n%s", from, task)
	}
	return strings.Replace(task, from, "resumeStrategy: "+strategy, 1)
}

// priorityFixtures opens the two fixture issues in queueRepo, one after the other, and the managed draft pull request
// that links the first, and returns the issues' numbers, oldest first, with the pull request. It creates the labels it
// uses when queueRepo lacks them. What an earlier run left open under priorityLabel, when it was stopped before its
// cleanup, would come first in the queue's order, so it is closed first; when the subtest ends, failed or not, the same
// cleanup closes this run's. It returns once GitHub shows the fixtures as the session reads them.
func priorityFixtures(t *testing.T) (resumable, fresh int, draft restartPR) {
	t.Helper()
	for _, label := range []string{priorityLabel, "harmflow"} {
		sandboxLabel(t, label)
	}
	closeLabelledFixtures(t, priorityLabel)
	var own []int
	t.Cleanup(func() { closeLabelledFixtures(t, priorityLabel, own...) })
	run := runSuffix()
	for which := 1; which <= 2; which++ {
		if which > 1 {
			// GitHub stamps an issue's creation to the second, and the queue reads the issues in that order.
			time.Sleep(time.Second)
		}
		own = append(own, priorityIssue(t, run, which))
	}
	resumable, fresh = own[0], own[1]
	draft = priorityDraft(t, run, resumable)
	own = append(own, draft.Number)
	awaitPriorityListed(t, resumable, fresh, draft.Number)
	return resumable, fresh, draft
}

// priorityFile is the file the run's which-th fixture issue asks for.
func priorityFile(run string, which int) string {
	return fmt.Sprintf("%s/%s-%d.txt", priorityDir, run, which)
}

// priorityIssue opens the run's which-th fixture issue in queueRepo, labelled priorityLabel, and returns its number.
// Its text reads as a small real task, as the sandbox asks of every fixture.
func priorityIssue(t *testing.T, run string, which int) int {
	t.Helper()
	file := priorityFile(run, which)
	out := newWorkAPI(t, "repos/"+queueRepo+"/issues", "-f", "title=Add the note "+file,
		"-f", fmt.Sprintf("body=Add the file `%s` with the single line `%s`, and change nothing else. "+
			"The repository has no tests.\n", file, run),
		"-f", "labels[]="+priorityLabel, "--jq", ".number")
	n, err := strconv.Atoi(out)
	if err != nil {
		t.Fatalf("open the fixture issue %d: %q, %v", which, out, err)
	}
	return n
}

// priorityDraft opens, from a branch of its own whose one commit adds the file the issue asks for, a draft pull request
// of queueRepo into its default branch, which links the issue with Closes #<issue> and carries the task's marking: the
// label harmflow and the line naming the task queue, which queueTask's task is. It is also labelled priorityLabel, by
// which the cleanup finds it. The branch is named neither as the built-in agent names a work branch nor after the
// issue.
func priorityDraft(t *testing.T, run string, issue int) restartPR {
	t.Helper()
	base := sandboxBase(t)
	sha := newWorkAPI(t, "repos/"+queueRepo+"/git/ref/heads/"+base, "--jq", ".object.sha")
	branch := "add-note-" + run
	newWorkAPI(t, "repos/"+queueRepo+"/git/refs", "-f", "ref=refs/heads/"+branch, "-f", "sha="+sha)
	// Until the pull request carries priorityLabel, nothing leads the cleanup to the branch, and deleting the branch
	// closes a pull request still open from it.
	abandon := func(format string, args ...any) {
		t.Helper()
		_, _ = newWorkGh("repos/"+queueRepo+"/git/refs/heads/"+branch, "-X", "DELETE")
		t.Fatalf(format, args...)
	}
	file := priorityFile(run, 1)
	_, err := newWorkGh("repos/"+queueRepo+"/contents/"+file, "-X", "PUT", "-f", "branch="+branch,
		"-f", "message=Add "+file, "-f", "content="+base64.StdEncoding.EncodeToString([]byte(run+"\n")))
	var out string
	if err == nil {
		body := fmt.Sprintf("Adds `%s`.\n\nCloses #%d\n\nManaged by HarmFlow task queue.\n", file, issue)
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
	if _, err := newWorkGh(fmt.Sprintf("repos/%s/issues/%d/labels", queueRepo, pr.Number), "-f", "labels[]=harmflow",
		"-f", "labels[]="+priorityLabel); err != nil {
		abandon("label the draft pull request %s: %v", pr.URL, err)
	}
	return pr
}

// awaitPriorityListed waits until GitHub shows the fixtures as the session reads them: the open issues of queueRepo
// labelled priorityLabel are the two issues, oldest first, and the pull request is linked to the first. GitHub lists a
// new issue under its label, and links a pull request to the issue it closes, only some seconds after it makes them,
// and a session that ran before would find no draft to resume.
func awaitPriorityListed(t *testing.T, resumable, fresh, pr int) {
	t.Helper()
	want := []int{resumable, fresh}
	var listed, linking []int
	var err error
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(time.Second) {
		if listed, err = labelledIssues(priorityLabel); err != nil {
			continue
		}
		if linking, err = newWorkLinking(resumable); err != nil {
			continue
		}
		if slices.Equal(listed, want) && slices.Contains(linking, pr) {
			return
		}
	}
	t.Fatalf("after a minute, the open issues of %s labelled %s are %v and the pull requests linking #%d are %v (%v); "+
		"want %v, and #%d among them", queueRepo, priorityLabel, listed, resumable, linking, err, want, pr)
}

// priorityReport is the report.json of an execution that opened the draft pull request for the issue and whose tests
// failed: the issue's item completed, as new work, with the pull request's branch, the pull request, and its tests
// failed.
func priorityReport(t *testing.T, issue int, pr restartPR) string {
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
