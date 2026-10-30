package integration

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// newWorkLabel marks what TestExcludedAndManagedIssuesAreNotNewWork opens in queueRepo: its three fixture issues and
// the managed draft pull request that links the second. newWorkExcluded is the second label the first fixture carries,
// which the task's prompt excludes. The test owns both alone, so that its cleanup never closes another test's fixtures.
// The draft's one file goes in newWorkDir, which the base branch never holds.
const (
	newWorkLabel    = "new-work-test"
	newWorkExcluded = "new-work-test-excluded"
	newWorkDir      = "new-work-test"
)

// Neither an issue the task's prompt excludes by a label nor one an open managed draft pull request links is new work,
// though both come first in the queue's order. The real Claude Code runs the built-in agent's copy and the built-in
// skills of this checkout against queueRepo, where the test opens three fixture issues under newWorkLabel, oldest
// first: one also labelled newWorkExcluded, one linked by a draft pull request with the task's marking, and one with
// neither. The task's queue takes new work first, one item a cycle, and its daemon's home is fresh, so its history
// holds none of the fixtures: the one issue it selects is its first new work, and must be the third. The session is
// stopped once the daemon tracks that issue, and its clone has no push URL, so no branch and no pull request of its
// reaches GitHub.
func TestExcludedAndManagedIssuesAreNotNewWork(t *testing.T) {
	claude := realClaude(t)
	excluded, managed, eligible := newWorkFixtures(t)
	clone := shallowClone(t)
	home := t.TempDir()
	d := startDaemon(t, home)
	useClaude(d, claude, clone)
	d.call("POST", "/tasks", map[string]any{"yaml": newWorkTask(d, clone)}, 201)

	id := d.call("POST", "/tasks/queue/run", nil, 202)["executionId"].(string)
	dir := executionDir(d, id)
	got := selectedIssue(t, d, id, dir)
	waitTracked(t, d, home, id, dir, got)
	d = restartMidRun(t, d, home, claude, clone)
	if x := d.call("GET", "/executions/"+id, nil, 200); x["status"] != "interrupted" {
		t.Errorf("execution %s after the stop at selection: status %v, want interrupted", id, x["status"])
	}

	notNew := map[int]string{
		excluded: fmt.Sprintf("it is labelled %s, which the prompt excludes", newWorkExcluded),
		managed:  "an open managed draft pull request links it, so it is already processed",
	}
	if selected := selectedWorkItems(t, dir); len(selected) != 1 || !strings.EqualFold(selected[0], issueKey(eligible)) {
		t.Errorf("progress.jsonl selects %v, want %s alone, the one fixture neither excluded nor linked\n%s", selected,
			issueKey(eligible), runFiles(dir))
	}
	if reason, ok := notNew[got]; ok {
		t.Errorf("the session took #%d as new work, though %s: hf-github-issue-queue does not select as it states", got,
			reason)
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

// newWorkTask is the task queueTask makes, with its eligible-issues line taking the open issues of queueRepo labelled
// newWorkLabel but those labelled newWorkExcluded, and its queue taking new work before work to resume.
func newWorkTask(d *daemon, clone string) string {
	d.t.Helper()
	task := queueTask(d, clone)
	for _, r := range []struct{ from, to string }{
		{
			fmt.Sprintf("- **Eligible issues:** open issues of %s labelled %s; every other issue is excluded.", queueRepo,
				queueLabel),
			fmt.Sprintf("- **Eligible issues:** open issues of %s labelled %s that are not labelled %s; every other issue "+
				"is excluded.", queueRepo, newWorkLabel, newWorkExcluded),
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

// newWorkFixtures opens the three fixture issues in queueRepo, one after the other, and the managed draft pull request
// that links the second, and returns the issues' numbers, oldest first. It creates the labels it uses when queueRepo
// lacks them. What an earlier run left open under newWorkLabel, when it was stopped before its cleanup, would come
// first in the queue's order, so it is closed first; when the test ends, failed or not, the same cleanup closes this
// run's. It returns once GitHub shows the fixtures as the session reads them.
func newWorkFixtures(t *testing.T) (excluded, managed, eligible int) {
	t.Helper()
	for _, label := range []string{newWorkLabel, newWorkExcluded, "harmflow"} {
		if _, err := newWorkGh("repos/" + queueRepo + "/labels/" + label); err != nil {
			newWorkAPI(t, "repos/"+queueRepo+"/labels", "-f", "name="+label)
		}
	}
	closeLabelledFixtures(t, newWorkLabel)
	var own []int
	t.Cleanup(func() { closeLabelledFixtures(t, newWorkLabel, own...) })
	run := runSuffix()
	for i, labels := range [][]string{{newWorkLabel, newWorkExcluded}, {newWorkLabel}, {newWorkLabel}} {
		if i > 0 {
			// GitHub stamps an issue's creation to the second, and the queue reads the issues in that order.
			time.Sleep(time.Second)
		}
		own = append(own, newWorkIssue(t, run, i+1, labels))
	}
	issues := slices.Clone(own)
	pr := newWorkDraft(t, run, issues[1])
	own = append(own, pr)
	awaitNewWorkListed(t, issues, pr)
	return issues[0], issues[1], issues[2]
}

// newWorkIssue opens the run's which-th fixture issue in queueRepo, with the labels, and returns its number.
func newWorkIssue(t *testing.T, run string, which int, labels []string) int {
	t.Helper()
	file := fmt.Sprintf("%s/%s-%d.txt", newWorkDir, run, which)
	args := []string{"repos/" + queueRepo + "/issues", "-f", fmt.Sprintf("title=New work test %s: add %s", run, file),
		"-f", fmt.Sprintf("body=A fixture of HarmFlow's new work test, which closes it when it ends.\n\n"+
			"Add the file `%s` with the single line `%s`, and change nothing else. The repository has no tests.\n", file, run),
		"--jq", ".number"}
	for _, l := range labels {
		args = append(args, "-f", "labels[]="+l)
	}
	n, err := strconv.Atoi(newWorkAPI(t, args...))
	if err != nil {
		t.Fatalf("open the fixture issue %d: %v", which, err)
	}
	return n
}

// newWorkDraft opens, from a branch of its own that adds one file in newWorkDir, a draft pull request of queueRepo into
// its default branch, which links the issue with Closes #<issue> and carries the task's marking: the label harmflow and
// the line naming the task queue, which queueTask's task is. It is also labelled newWorkLabel, by which the cleanup
// finds it. It returns the pull request's number.
func newWorkDraft(t *testing.T, run string, issue int) int {
	t.Helper()
	base := newWorkAPI(t, "repos/"+queueRepo, "--jq", ".default_branch")
	sha := newWorkAPI(t, "repos/"+queueRepo+"/git/ref/heads/"+base, "--jq", ".object.sha")
	branch := newWorkDir + "/" + run
	file := branch + ".txt"
	newWorkAPI(t, "repos/"+queueRepo+"/git/refs", "-f", "ref=refs/heads/"+branch, "-f", "sha="+sha)
	_, err := newWorkGh("repos/"+queueRepo+"/contents/"+file, "-X", "PUT", "-f", "branch="+branch, "-f", "message=Add "+file,
		"-f", "content="+base64.StdEncoding.EncodeToString([]byte(run+"\n")))
	var number string
	if err == nil {
		body := fmt.Sprintf("A fixture of HarmFlow's new work test, which closes it when it ends.\n\nCloses #%d\n\n"+
			"Managed by HarmFlow task queue.\n", issue)
		number, err = newWorkGh("repos/"+queueRepo+"/pulls", "-f", "title=New work test "+run+": add "+file,
			"-f", "head="+branch, "-f", "base="+base, "-F", "draft=true", "-f", "body="+body, "--jq", ".number")
	}
	if err != nil {
		// With no pull request, nothing labelled leads the cleanup to the branch.
		_, _ = newWorkGh("repos/"+queueRepo+"/git/refs/heads/"+branch, "-X", "DELETE")
		t.Fatalf("open the draft pull request from %s: %v", branch, err)
	}
	pr, err := strconv.Atoi(number)
	if err != nil {
		t.Fatalf("open the draft pull request from %s: %v", branch, err)
	}
	newWorkAPI(t, "repos/"+queueRepo+"/issues/"+number+"/labels", "-f", "labels[]=harmflow", "-f", "labels[]="+newWorkLabel)
	return pr
}

// awaitNewWorkListed waits until GitHub shows the fixtures as the session reads them: the open issues of queueRepo
// labelled newWorkLabel are the issues, in the order they were opened, and the pull request is linked to the second.
// GitHub lists a new issue under its label, and links a pull request to the issue it closes, only some seconds after
// it makes them, and a session that ran before would rightly take the second as new work.
func awaitNewWorkListed(t *testing.T, issues []int, pr int) {
	t.Helper()
	var listed, linking []int
	var err error
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(time.Second) {
		if err = newWorkJSON(&listed, "repos/"+queueRepo+"/issues?state=open&sort=created&direction=asc&per_page=100&labels="+
			newWorkLabel, "--jq", "[.[] | select(.pull_request | not) | .number]"); err != nil {
			continue
		}
		if linking, err = newWorkLinking(issues[1]); err != nil {
			continue
		}
		if slices.Equal(listed, issues) && slices.Contains(linking, pr) {
			return
		}
	}
	t.Fatalf("after a minute, the open issues of %s labelled %s are %v and the pull requests linking #%d are %v (%v); "+
		"want %v, and #%d among them", queueRepo, newWorkLabel, listed, issues[1], linking, err, issues, pr)
}

// newWorkLinking lists the pull requests of queueRepo, in any state, that GitHub links to the issue as closing it.
func newWorkLinking(issue int) ([]int, error) {
	const query = `query($owner: String!, $repo: String!, $n: Int!) { repository(owner: $owner, name: $repo) {
  issue(number: $n) { closedByPullRequestsReferences(first: 10, includeClosedPrs: true) { nodes { number } } } } }`
	owner, name, _ := strings.Cut(queueRepo, "/")
	var prs []int
	err := newWorkJSON(&prs, "graphql", "-F", "owner="+owner, "-F", "repo="+name, "-F", "n="+strconv.Itoa(issue),
		"-f", "query="+query, "--jq", "[.data.repository.issue.closedByPullRequestsReferences.nodes[].number]")
	return prs, err
}

// closeLabelledFixtures closes what a test's runs opened in queueRepo under the label the test owns alone: the open
// issues and pull requests labelled label, and the own ones, which GitHub may list under the label only some seconds
// after it opens them. A pull request's branch is deleted with it, read from the pull request: nothing is found by a
// branch's name.
func closeLabelledFixtures(t *testing.T, label string, own ...int) {
	t.Helper()
	var open []int
	if err := newWorkJSON(&open, "repos/"+queueRepo+"/issues?state=open&per_page=100&labels="+label,
		"--jq", "[.[].number]"); err != nil {
		t.Errorf("cleanup: %v", err)
	}
	for _, n := range own {
		if !slices.Contains(open, n) {
			open = append(open, n)
		}
	}
	for _, n := range open {
		kind, err := newWorkGh(fmt.Sprintf("repos/%s/issues/%d", queueRepo, n), "--jq",
			`if .state != "open" then "closed" elif .pull_request then "pull" else "issue" end`)
		switch {
		case err != nil:
			t.Errorf("cleanup: %v", err)
		case kind == "issue":
			if _, err := newWorkGh(fmt.Sprintf("repos/%s/issues/%d", queueRepo, n), "-X", "PATCH", "-f", "state=closed"); err != nil {
				t.Errorf("cleanup: %v", err)
			}
		case kind == "pull":
			pull := fmt.Sprintf("repos/%s/pulls/%d", queueRepo, n)
			branch, err := newWorkGh(pull, "--jq", ".head.ref")
			if err == nil {
				_, err = newWorkGh(pull, "-X", "PATCH", "-f", "state=closed")
			}
			if err == nil {
				_, err = newWorkGh("repos/"+queueRepo+"/git/refs/heads/"+branch, "-X", "DELETE")
			}
			if err != nil {
				t.Errorf("cleanup of the pull request #%d: %v", n, err)
			}
		}
	}
}

// newWorkGh runs gh api with args, the path first, and returns what it printed, trimmed, or an error that holds what
// it wrote to stderr.
func newWorkGh(args ...string) (string, error) {
	cmd := exec.Command("gh", append([]string{"api"}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("gh api %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// newWorkJSON runs gh api with args, as newWorkGh does, and decodes what it printed into v.
func newWorkJSON(v any, args ...string) error {
	out, err := newWorkGh(args...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(out), v); err != nil {
		return fmt.Errorf("gh api %s: %w", args[0], err)
	}
	return nil
}

// newWorkAPI runs gh api with args, as newWorkGh does, and fails the test when it fails.
func newWorkAPI(t *testing.T, args ...string) string {
	t.Helper()
	out, err := newWorkGh(args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// selectedWorkItems lists the work items the work_item.selected lines of the session's progress.jsonl name, in order.
func selectedWorkItems(t *testing.T, dir string) []string {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "progress.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var items []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var line struct {
			Type     string `json:"type"`
			WorkItem string `json:"workItem"`
		}
		if json.Unmarshal(sc.Bytes(), &line) == nil && line.Type == "work_item.selected" {
			items = append(items, line.WorkItem)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return items
}
