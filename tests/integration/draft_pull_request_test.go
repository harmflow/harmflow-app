package integration

import (
	"encoding/json"
	"errors"
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

// draftPRLabel marks the fixture issue TestEligibleIssueBecomesALinkedDraftPullRequest opens in queueRepo, apart from
// the queue-test fixtures, which it never reads or changes. The file the fixture asks for goes in draftPRDir, which the
// base branch never holds: a branch that holds it is one a session pushed for a fixture.
const (
	draftPRLabel = "draft-pr-test"
	draftPRDir   = "draft-pr-test"
)

// A GitHub task, run once, turns an eligible issue into a draft pull request from a work branch, linked to the issue.
// The real Claude Code runs the built-in agent's copy and the built-in skills of this checkout against queueRepo, on a
// fixture issue the test opens, from a clone that may push. The issue then has exactly one pull request: an open draft
// from a branch other than the base branch, into the base branch. The base branch is where it was: nothing was pushed
// to it, and nothing was merged. The work came from a worktree of the session's own: the pull request's head branch is
// checked out under the execution's workspace, at the pull request's head commit, and the task's clone is still on the
// base branch, at the commit it had before the run, with nothing changed in it.
func TestEligibleIssueBecomesALinkedDraftPullRequest(t *testing.T) {
	claude := realClaude(t)
	base := sandboxBase(t)
	for _, label := range []string{draftPRLabel, "harmflow"} {
		sandboxLabel(t, label)
	}
	run := runSuffix()
	file := draftPRDir + "/" + run + ".txt"
	issue := labelledFixture(t, draftPRLabel, draftPRDir, base, "Add the release note "+file,
		fmt.Sprintf("Add the file `%s` with the single line `%s`, and change nothing else. The repository has no tests.\n",
			file, run))
	before := branchHead(t, base)
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clone := shallowCloneIn(t, parent, queueRepo)
	// The clone shallowClone makes, with a push URL for this test's clone only: its session pushes the work branch.
	if out, err := exec.Command("git", "-C", clone, "remote", "set-url", "--push", "origin", "https://github.com/"+queueRepo).CombinedOutput(); err != nil {
		t.Fatalf("git remote set-url --push: %v\n%s", err, out)
	}
	checkedOut, cloneHead := git(t, clone, "rev-parse", "--abbrev-ref", "HEAD"), git(t, clone, "rev-parse", "HEAD")
	home := t.TempDir()
	d := startDaemon(t, home)
	useClaude(d, claude, clone)
	d.call("POST", "/tasks", map[string]any{"yaml": labelledIssuesTask(d, "draft-pr", draftPRLabel, clone, base)}, 201)

	id := d.call("POST", "/tasks/draft-pr/run", nil, 202)["executionId"].(string)
	dir := executionDir(d, id)
	// Longer than the task's timeout, so that the execution's own end says why a session ran too long.
	x := d.waitEnd(id, withinDeadline(t, 13*time.Minute))
	if x["status"] != "completed" {
		t.Errorf("execution %s = %v (%v), want completed\n%s", id, x["status"], x["errorMessage"], runFiles(dir))
	}

	events := d.events(id)
	var created []map[string]any
	for _, e := range events {
		if e["type"] == "pull_request.created" {
			created = append(created, e)
		}
	}
	if len(created) != 1 {
		t.Fatalf("execution %s logged %d pull_request.created events, want 1: %v\n%s", id, len(created), created, runFiles(dir))
	}
	payload, _ := created[0]["payload"].(map[string]any)
	url, _ := payload["url"].(string)
	if item, _ := created[0]["workItemId"].(string); !strings.EqualFold(item, issueKey(issue)) || url == "" {
		t.Errorf("pull_request.created is for %q with the URL %q, want %s with its pull request's URL", item, url,
			issueKey(issue))
	}
	if pr := reportedPullRequest(t, dir, issue); pr == nil || pr.URL != url || !pr.Draft || !pr.Created {
		t.Errorf("report.json gives #%d the pull request %+v, want %s, a draft this execution created\n%s", issue, pr, url,
			runFiles(dir))
	}

	prs, err := linkingPullRequests(issue)
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 1 {
		t.Fatalf("%d pull requests of %s link #%d, want 1: %+v\n%s", len(prs), queueRepo, issue, prs, runFiles(dir))
	}
	pr := prs[0]
	if pr.URL != url {
		t.Errorf("the pull request linking #%d is %s, want %s, the execution's", issue, pr.URL, url)
	}
	if pr.State != "OPEN" || !pr.Draft || pr.Merged {
		t.Errorf("%s is %s, draft %v, merged %v; want an open draft, not merged", pr.URL, pr.State, pr.Draft, pr.Merged)
	}
	if pr.Base != base || pr.Head == base {
		t.Errorf("%s goes from %s into %s, want a work branch into %s", pr.URL, pr.Head, pr.Base, base)
	}
	if after := branchHead(t, base); after != before {
		t.Errorf("the head of %s moved from %s to %s during the run, want it unchanged", base, before, after)
	}

	workspace := contextWorkspace(t, dir)
	var own []worktree
	for _, w := range worktrees(t, clone)[1:] {
		if w.Branch == pr.Head && within(t, workspace, w.Path) {
			own = append(own, w)
		}
	}
	if len(own) != 1 {
		t.Errorf("git worktree list in the clone gives %d worktrees of %s under the workspace %s, want 1: %+v", len(own),
			pr.Head, workspace, worktrees(t, clone))
	} else if own[0].Head != pr.HeadOID {
		t.Errorf("the worktree %s is at %s, want %s, the head of %s on GitHub", own[0].Path, own[0].Head, pr.HeadOID, pr.URL)
	}
	if b, head := git(t, clone, "rev-parse", "--abbrev-ref", "HEAD"), git(t, clone, "rev-parse", "HEAD"); b != base ||
		b != checkedOut || head != cloneHead {
		t.Errorf("the clone's checkout is %s at %s after the run, want %s at %s, as before it, on the base branch %s", b,
			head, checkedOut, cloneHead, base)
	}
	if status := git(t, clone, "status", "--porcelain"); status != "" {
		t.Errorf("git status --porcelain in the clone printed %q after the run, want nothing", status)
	}
	changed := false
	for _, e := range events {
		p, _ := e["payload"].(map[string]any)
		item, _ := e["workItemId"].(string)
		if e["type"] == "workspace.changed" && strings.EqualFold(item, issueKey(issue)) && p["branch"] == pr.Head {
			changed = true
		}
	}
	if !changed {
		t.Errorf("execution %s logged no workspace.changed event for %s with the branch %s\n%s", id, issueKey(issue), pr.Head,
			runFiles(dir))
	}
}

// worktree is an entry of git worktree list --porcelain: its path, its head commit, and its branch, without
// refs/heads/, empty when the worktree is detached.
type worktree struct {
	Path, Head, Branch string
}

// worktrees lists the worktrees of the clone, the clone's own checkout first, as git worktree list --porcelain gives
// them.
func worktrees(t *testing.T, clone string) []worktree {
	t.Helper()
	var list []worktree
	for _, entry := range strings.Split(git(t, clone, "worktree", "list", "--porcelain"), "\n\n") {
		var w worktree
		for _, line := range strings.Split(entry, "\n") {
			key, value, _ := strings.Cut(line, " ")
			switch key {
			case "worktree":
				w.Path = value
			case "HEAD":
				w.Head = value
			case "branch":
				w.Branch = strings.TrimPrefix(value, "refs/heads/")
			}
		}
		list = append(list, w)
	}
	return list
}

// within reports whether path is dir or lies under it, both read with their symbolic links resolved: git gives a
// worktree's real path, and the daemon's home may be under a link, such as macOS's /var.
func within(t *testing.T, dir, path string) bool {
	t.Helper()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Errorf("filepath.EvalSymlinks(%q): %v", dir, err)
		return false
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Errorf("filepath.EvalSymlinks(%q): %v", path, err)
		return false
	}
	rel, err := filepath.Rel(realDir, realPath)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// contextWorkspace is the workspace the execution's context.json gives its session, paths.workspace.
func contextWorkspace(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "context.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ctx struct {
		Paths struct {
			Workspace string `json:"workspace"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(data, &ctx); err != nil {
		t.Fatalf("context.json: %v", err)
	}
	if ctx.Paths.Workspace == "" {
		t.Fatalf("context.json gives no paths.workspace:\n%s", data)
	}
	return ctx.Paths.Workspace
}

// labelledIssuesTask is the task id, made of the built-in issue implementer as selecting it copies it, with the open
// issues of queueRepo labelled label as its only eligible ones. Its permissions allow what the built-in skills run to
// select, implement, push, and open the draft pull request, and deny gh pr merge: a denied tool wins over every allow
// rule, the signed-in Claude Code's own included.
func labelledIssuesTask(d *daemon, id, label, clone, base string) string {
	d.t.Helper()
	copied := d.builtinAgentCopy("hf-github-issue-implementer")
	const eligible = "- **Eligible issues:** open issues labeled `agent-ready` that are not labeled `blocked`.\n"
	prompt, _ := copied["prompt"].(string)
	if !strings.Contains(prompt, eligible) {
		d.t.Fatalf("the copy of hf-github-issue-implementer has no line %q to customize:\n%s", eligible, prompt)
	}
	prompt = strings.Replace(prompt, eligible, fmt.Sprintf(
		"- **Eligible issues:** open issues of %s labelled %s; every other issue is excluded.\n", queueRepo, label), 1)
	return fmt.Sprintf(`apiVersion: harmflow/v1alpha1
kind: Task
metadata:
  id: %[6]s
  name: %[6]s
spec:
  type: github
  prompt: |
    %[4]s
  origin:
    agent: hf-github-issue-implementer
    hash: %[5]s
  skills: [hf-github-issue-queue, hf-github-worktree, hf-github-draft-pr]
  harness:
    preferred:
      provider: claude-code
      model: sonnet
      effort: low
  permissions:
    profile: custom
    network: allow
    custom:
      # gh api can also merge, or push to the base branch; the checks of the pull request and of the base branch's head
      # catch either.
      allowedTools: [Read, Glob, Grep, Write, Edit, Skill, TodoWrite, "Bash(echo:*)", "Bash(printf:*)", "Bash(cat:*)",
        "Bash(jq:*)", "Bash(ls:*)", "Bash(date:*)", "Bash(cd:*)", "Bash(git:*)", "Bash(gh auth status:*)",
        "Bash(gh issue list:*)", "Bash(gh issue view:*)", "Bash(gh pr list:*)", "Bash(gh pr view:*)",
        "Bash(gh pr edit:*)", "Bash(gh pr create:*)", "Bash(gh label:*)", "Bash(gh api:*)"]
      deniedTools: ["Bash(gh pr merge:*)"]
  execution:
    timeoutMinutes: 12
  github:
    repositories:
      - name: %[1]s
        localPath: %[2]s
        baseBranch: %[3]s
    queue:
      ordering: oldest_first
      maxItemsPerCycle: 1
      resumeStrategy: resume_first
`, queueRepo, clone, base, strings.ReplaceAll(prompt, "\n", "\n    "), copied["hash"], id)
}

// withinDeadline is want, or less when the test binary's -timeout would stop the test first: a test stopped that way
// runs no cleanup, so a minute is kept for them.
func withinDeadline(t *testing.T, want time.Duration) time.Duration {
	t.Helper()
	if deadline, ok := t.Deadline(); ok {
		return min(want, time.Until(deadline)-time.Minute)
	}
	return want
}

// sandboxBase is queueRepo's default branch, the base branch of its pull requests.
func sandboxBase(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("gh", "api", "repos/"+queueRepo, "--jq", ".default_branch").Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		t.Fatalf("gh api repos/%s: %q, %v", queueRepo, out, err)
	}
	return strings.TrimSpace(string(out))
}

// sandboxLabel creates the label in queueRepo unless it is there already: the fixture's label, and the marking of the
// draft pull requests the built-in skills open.
func sandboxLabel(t *testing.T, name string) {
	t.Helper()
	if exec.Command("gh", "api", "repos/"+queueRepo+"/labels/"+name, "--silent").Run() == nil {
		return
	}
	if out, err := exec.Command("gh", "label", "create", name, "--repo", queueRepo).CombinedOutput(); err != nil {
		t.Fatalf("gh label create %s: %v\n%s", name, err, out)
	}
}

// branchHead is the commit the branch of queueRepo points at.
func branchHead(t *testing.T, branch string) string {
	t.Helper()
	out, err := exec.Command("gh", "api", "repos/"+queueRepo+"/git/ref/heads/"+branch, "--jq", ".object.sha").Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		t.Fatalf("read the head of %s in %s: %q, %v", branch, queueRepo, out, err)
	}
	return strings.TrimSpace(string(out))
}

// labelledFixture opens a fixture issue in queueRepo, labelled label, with the title and the body, which ask for a
// change in dir alone, and returns its number. An open issue under the label is one an earlier run left when it was
// stopped before its cleanup, and the task's prompt would make it eligible too, so it is swept first. When the test
// ends, failed or not, the sweep runs again.
func labelledFixture(t *testing.T, label, dir, base, title, body string) int {
	t.Helper()
	sweepLabelledFixtures(t, label, dir, base)
	out, err := exec.Command("gh", "issue", "create", "--repo", queueRepo, "--label", label, "--title", title,
		"--body", body).Output()
	if err != nil {
		t.Fatalf("gh issue create: %v", err)
	}
	url := strings.TrimSpace(string(out))
	issue, err := strconv.Atoi(url[strings.LastIndex(url, "/")+1:])
	if err != nil {
		t.Fatalf("gh issue create printed %q, not an issue's URL", url)
	}
	t.Cleanup(func() { sweepLabelledFixtures(t, label, dir, base, issue) })
	// GitHub lists a new issue under its label some seconds after it opens it, and the session may list it that way.
	var open []int
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(time.Second) {
		if open, err = labelledIssues(label); err == nil && slices.Contains(open, issue) {
			break
		}
	}
	if err != nil || !slices.Equal(open, []int{issue}) {
		t.Fatalf("the open issues of %s labelled %s are %v (%v), want #%d alone: the task's prompt makes each of them "+
			"eligible", queueRepo, label, open, err, issue)
	}
	return issue
}

// sweepLabelledFixtures removes from queueRepo what a test's runs made, found by the fixtures' label and their issues,
// never by the branch names the sessions chose. The issues are the own ones and the open ones labelled label: GitHub
// lists an issue under its label only some seconds after it opens it. It closes the open pull requests that link one of
// them, deletes their branches and every other branch that holds dir, which closes a pull request still open from one,
// and closes the issues. The base branch is never deleted.
func sweepLabelledFixtures(t *testing.T, label, dir, base string, own ...int) {
	t.Helper()
	issues, err := labelledIssues(label)
	if err != nil {
		t.Errorf("cleanup: %v", err)
	}
	for _, n := range own {
		if !slices.Contains(issues, n) {
			issues = append(issues, n)
		}
	}
	branches, err := branchesHolding(dir)
	if err != nil {
		t.Errorf("cleanup: %v", err)
	}
	for _, n := range issues {
		prs, err := linkingPullRequests(n)
		if err != nil {
			t.Errorf("cleanup of #%d: %v", n, err)
		}
		for _, pr := range prs {
			if !slices.Contains(branches, pr.Head) {
				branches = append(branches, pr.Head)
			}
			if pr.State != "OPEN" {
				continue
			}
			if out, err := exec.Command("gh", "pr", "close", strconv.Itoa(pr.Number), "--repo", queueRepo).CombinedOutput(); err != nil {
				t.Errorf("cleanup: gh pr close %d: %v\n%s", pr.Number, err, out)
			}
		}
	}
	for _, b := range branches {
		if b == base {
			t.Errorf("cleanup: %s holds a fixture's work, and stays: it is the base branch", base)
			continue
		}
		if out, err := exec.Command("gh", "api", "-X", "DELETE", "repos/"+queueRepo+"/git/refs/heads/"+b).CombinedOutput(); err != nil {
			t.Errorf("cleanup: deleting the branch %s: %v\n%s", b, err, out)
		}
	}
	for _, n := range issues {
		if out, err := exec.Command("gh", "issue", "close", strconv.Itoa(n), "--repo", queueRepo).CombinedOutput(); err != nil {
			t.Errorf("cleanup: gh issue close %d: %v\n%s", n, err, out)
		}
	}
}

// labelledIssues lists the open issues of queueRepo that carry the label, oldest first, read from the repository
// rather than from GitHub's search index, which can lag behind a new issue.
func labelledIssues(label string) ([]int, error) {
	out, err := exec.Command("gh", "api", "repos/"+queueRepo+"/issues?state=open&sort=created&direction=asc&per_page=100&labels="+label,
		"--jq", "[.[] | select(.pull_request | not) | .number]").Output()
	if err != nil {
		return nil, fmt.Errorf("list the open issues of %s labelled %s: %w", queueRepo, label, err)
	}
	var issues []int
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("list the open issues of %s labelled %s: %w", queueRepo, label, err)
	}
	return issues, nil
}

// branchesHolding lists the branches of queueRepo that hold the folder.
func branchesHolding(folder string) ([]string, error) {
	out, err := exec.Command("gh", "api", "--paginate", "repos/"+queueRepo+"/branches?per_page=100", "--jq", ".[].name").Output()
	if err != nil {
		return nil, fmt.Errorf("list the branches of %s: %w", queueRepo, err)
	}
	var branches []string
	for _, b := range strings.Fields(string(out)) {
		err := exec.Command("gh", "api", "repos/"+queueRepo+"/contents/"+folder+"?ref="+b, "--silent").Run()
		var exit *exec.ExitError
		switch {
		case err == nil:
			branches = append(branches, b)
		case !errors.As(err, &exit):
			return nil, fmt.Errorf("read %s on %s: %w", folder, b, err)
		}
	}
	return branches, nil
}

// linkedPR is a pull request of queueRepo that GitHub links to an issue.
type linkedPR struct {
	Number  int    `json:"number"`
	URL     string `json:"url"`
	State   string `json:"state"`
	Draft   bool   `json:"isDraft"`
	Merged  bool   `json:"merged"`
	Base    string `json:"baseRefName"`
	Head    string `json:"headRefName"`
	HeadOID string `json:"headRefOid"`
	Body    string `json:"body"`
}

// linkingPullRequests lists the pull requests of queueRepo, in any state, that GitHub links to the issue as closing
// it: the link is GitHub's, not a line of the pull request's body.
func linkingPullRequests(issue int) ([]linkedPR, error) {
	const query = `query($owner: String!, $repo: String!, $n: Int!) { repository(owner: $owner, name: $repo) {
  issue(number: $n) { closedByPullRequestsReferences(first: 10, includeClosedPrs: true) {
    nodes { number url state isDraft merged baseRefName headRefName headRefOid body } } } } }`
	owner, name, _ := strings.Cut(queueRepo, "/")
	out, err := exec.Command("gh", "api", "graphql", "-F", "owner="+owner, "-F", "repo="+name, "-F", "n="+strconv.Itoa(issue),
		"-f", "query="+query, "--jq", ".data.repository.issue.closedByPullRequestsReferences.nodes").Output()
	if err != nil {
		return nil, fmt.Errorf("read the pull requests linking #%d: %w", issue, err)
	}
	var prs []linkedPR
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, fmt.Errorf("read the pull requests linking #%d: %w", issue, err)
	}
	return prs, nil
}

// reportedPR is a work item's pull request as the session's report.json gives it.
type reportedPR struct {
	URL     string `json:"url"`
	Draft   bool   `json:"draft"`
	Created bool   `json:"created"`
}

// reportedPullRequest is the pull request the execution's report.json gives the issue's work item: nil when the report
// lists no such item, or the item has no pull request.
func reportedPullRequest(t *testing.T, dir string, issue int) *reportedPR {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		t.Errorf("report.json: %v", err)
		return nil
	}
	var report struct {
		WorkItems []struct {
			Key         string      `json:"key"`
			PullRequest *reportedPR `json:"pullRequest"`
		} `json:"workItems"`
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Errorf("report.json: %v", err)
		return nil
	}
	for _, w := range report.WorkItems {
		if strings.EqualFold(w.Key, issueKey(issue)) {
			return w.PullRequest
		}
	}
	return nil
}
