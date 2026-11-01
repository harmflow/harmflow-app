package integration

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The fixture set of TestDraftIntoAnotherBaseBranchIsResumedAsItsIssues in queueRepo, apart from the other sets, which
// it never reads or changes: a base branch of its own, named otherBasePrefix and the run, one issue labelled
// otherBaseLabel, and the draft pull request into that base branch that closes the issue, all made by each run and
// removed when it ends. The issue's two files go in otherBaseDir, which no base branch holds: a branch that holds it is
// one pushed for a fixture.
const (
	otherBaseLabel  = "other-base-test"
	otherBasePrefix = "other-base-test/"
	otherBaseDir    = "banners"
)

// A draft pull request whose tests failed, into a base branch other than the repository's default one, is resumed as
// its issue's by a later execution, on that same pull request, and no second pull request is opened for the issue.
// GitHub links a pull request's Closes line to its issue only into the default branch, so here the session can match
// the draft to its issue only by the issue's number in the draft's work branch, or by the pull request the ledger
// hands it. The test pushes a base branch of its own from the default branch's head, and the task's repository names
// it as its base branch. The fixture issue asks for a change in two parts, and the test opens a managed draft pull
// request into that base branch that closes the issue, from a work branch named as the built-in agent names one, whose
// one commit makes the first part, and checks that GitHub links it to no issue. A first execution, through a stand-in
// Claude Code, reports the issue's item completed with its tests failed and that pull request, so the daemon's ledger
// keeps the item in progress. Then the real Claude Code runs the built-in agent's copy and the built-in skills of this
// checkout against queueRepo once, from a clone that may push. It selects the fixture issue alone and reports it
// resumed, with the test's pull request, which it updated during the execution and left an open draft into the test's
// base branch whose branch holds a commit beyond the test's. GitHub is read by base branch, head branch, and body,
// never by its closing link.
func TestDraftIntoAnotherBaseBranchIsResumedAsItsIssues(t *testing.T) {
	claude := realClaude(t)
	for _, label := range []string{otherBaseLabel, "harmflow"} {
		sandboxLabel(t, label)
	}
	sweepOtherBaseFixtures(t)
	base := otherBasePrefix + runSuffix()
	var issue int
	// Registered before the fixture issue's own sweep, which closes the issue, so that it runs after it: the base
	// branch is deleted last of all.
	t.Cleanup(func() { sweepOtherBaseFixtures(t, issue) })
	sha := newWorkAPI(t, "repos/"+queueRepo+"/git/ref/heads/"+sandboxBase(t), "--jq", ".object.sha")
	newWorkAPI(t, "repos/"+queueRepo+"/git/refs", "-f", "ref=refs/heads/"+base, "-f", "sha="+sha)
	if def := sandboxBase(t); def == base {
		t.Fatalf("the default branch of %s is %s, want a branch other than the test's base branch", queueRepo, def)
	}
	issue = suiteFixture(t, otherBaseLabel, base, "Add the beta and maintenance banners",
		"Nothing tells users that a feature is in beta, or that the service is down for maintenance. Add two short "+
			"banners under `"+otherBaseDir+"/`, one file each:\n\n"+
			"1. `"+otherBaseDir+"/beta.txt`, with the single line `This feature is in beta.`\n"+
			"2. `"+otherBaseDir+"/maintenance.txt`, with the single line `We are down for maintenance.`\n\n"+
			"Change nothing else. The repository has no tests.\n")
	draft, pushed := otherBaseDraft(t, base, issue)
	key := issueKey(issue)

	clone := pushableClone(t)
	// The clone is shallow, which also makes it fetch its default branch alone. The base branch and the draft's branch
	// were pushed from elsewhere, and the session works on them as origin/<branch>, which a fetch writes only under a
	// refspec of every branch.
	git(t, clone, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	home := t.TempDir()
	d := startDaemon(t, home)
	stand := standIn(t, "fake-claude", "claude",
		"\nSCENARIO=ok\n", "\nSCENARIO=report\n",
		`{"type":"test.completed","status":"passed","message":"12 passed"}`,
		`{"type":"test.completed","status":"failed","message":"1 failed"}`,
		standInReport, failedDraftReport(t, issue, draft))
	useClaude(d, stand.path, clone)
	d.call("POST", "/tasks", map[string]any{"yaml": failingTestsTask(d, clone, base, otherBaseLabel, 0)}, 201)

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
	if item := queuedItem(d, key); item == nil || item["resumes"] != 1.0 {
		t.Errorf("after execution %s, GET /queue gives %s as %v, want it resumed once", next, key, item)
	}

	prs, err := otherBasePullRequests([]string{base}, []int{issue})
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 1 || prs[0].Number != draft.Number {
		t.Fatalf("the pull requests of %s into %s or whose body names #%d are %+v, want %s alone, the one the test "+
			"opened\n%s", queueRepo, base, issue, prs, draft.URL, runFiles(dir))
	}
	pr := prs[0]
	if pr.State != "OPEN" || !pr.Draft || pr.Base != base {
		t.Errorf("%s is %s, draft %v, into %s; want an open draft into %s, not merged", pr.URL, pr.State, pr.Draft,
			pr.Base, base)
	}
	if status, ahead := compareCommits(t, pushed, pr.HeadOID); status != "ahead" || ahead < 1 {
		t.Errorf("the head %s of %s is %s of the test's commit %s by %d commits, want ahead by one or more\n%s", pr.HeadOID,
			pr.URL, status, pushed, ahead, commandsOf(d, next))
	}
}

// otherBaseDraft opens a draft pull request of queueRepo into base, and returns it with the one commit its branch holds
// beyond base, which adds the first of the fixture issue's two files. Its branch is named as hf-github-worktree names a
// work branch, its last segment starting with the issue's number and a hyphen. The pull request closes the issue with
// Closes #<issue>, and carries the marking of the task failingTestsTask makes: the label harmflow, and the line naming
// the task resumeDraftTask. It fails the test when GitHub links the pull request to an issue, as it does only into the
// default branch: the session must not find the draft through that link.
func otherBaseDraft(t *testing.T, base string, issue int) (restartPR, string) {
	t.Helper()
	branch := fmt.Sprintf("harmflow/%d-add-banners", issue)
	sha := newWorkAPI(t, "repos/"+queueRepo+"/git/ref/heads/"+base, "--jq", ".object.sha")
	newWorkAPI(t, "repos/"+queueRepo+"/git/refs", "-f", "ref=refs/heads/"+branch, "-f", "sha="+sha)
	commit, err := newWorkGh("repos/"+queueRepo+"/contents/"+otherBaseDir+"/beta.txt", "-X", "PUT",
		"-f", "branch="+branch, "-f", "message=Add the beta banner",
		"-f", "content="+base64.StdEncoding.EncodeToString([]byte("This feature is in beta.\n")), "--jq", ".commit.sha")
	if err != nil {
		// With no file of otherBaseDir, nothing leads the cleanup to the branch.
		_, _ = newWorkGh("repos/"+queueRepo+"/git/refs/heads/"+branch, "-X", "DELETE")
		t.Fatalf("commit the first file on %s: %v", branch, err)
	}
	body := fmt.Sprintf("Adds `%s/beta.txt`. The maintenance banner is still to do.\n\nCloses #%d\n\n"+
		"Managed by HarmFlow task %s.\n", otherBaseDir, issue, resumeDraftTask)
	out := newWorkAPI(t, "repos/"+queueRepo+"/pulls", "-f", "title=Add the beta and maintenance banners",
		"-f", "head="+branch, "-f", "base="+base, "-F", "draft=true", "-f", "body="+body, "--jq", "{number, url: .html_url}")
	pr := restartPR{Head: branch, State: "OPEN", Draft: true}
	if err := json.Unmarshal([]byte(out), &pr); err != nil || pr.Number == 0 || pr.URL == "" {
		t.Fatalf("open the draft pull request from %s: %q, %v", branch, out, err)
	}
	newWorkAPI(t, fmt.Sprintf("repos/%s/issues/%d/labels", queueRepo, pr.Number), "-f", "labels[]=harmflow")
	// Into the default branch, GitHub links a pull request to the issue it closes some seconds after it opens it, so
	// the link is looked for over half a minute, not once.
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(time.Second) {
		out, err := exec.Command("gh", "pr", "view", strconv.Itoa(pr.Number), "--repo", queueRepo, "--json",
			"closingIssuesReferences", "--jq", ".closingIssuesReferences | length").Output()
		if err != nil {
			t.Fatalf("gh pr view %d --json closingIssuesReferences: %v", pr.Number, err)
		}
		if n := strings.TrimSpace(string(out)); n != "0" {
			t.Fatalf("GitHub gives %s into %s %s closing references, want none: the session could find the draft "+
				"through them", pr.URL, base, n)
		}
	}
	return pr, commit
}

// sweepOtherBaseFixtures removes from queueRepo what the test's runs left, found by the fixture issues and their label,
// by the base branches named otherBasePrefix, and by what the branches hold, never by GitHub's closing link or by the
// branch names a session chose. It closes every open pull request into one of those base branches and every one whose
// body names one of the issues, deletes the branches they came from and every other branch that holds otherBaseDir,
// closes the issues, and deletes the base branches last: deleting a base branch first closes its pull requests and
// leaves their branches behind. The issues are the own ones and the open ones labelled otherBaseLabel, which a run
// stopped before its cleanup leaves, and which the task's prompt would make eligible.
func sweepOtherBaseFixtures(t *testing.T, own ...int) {
	t.Helper()
	issues, err := labelledIssues(otherBaseLabel)
	if err != nil {
		t.Errorf("cleanup: %v", err)
	}
	for _, n := range own {
		if n != 0 && !slices.Contains(issues, n) {
			issues = append(issues, n)
		}
	}
	def, err := newWorkGh("repos/"+queueRepo, "--jq", ".default_branch")
	if err != nil {
		t.Errorf("cleanup: %v", err)
		return
	}
	names, err := newWorkGh("repos/"+queueRepo+"/branches?per_page=100", "--paginate", "--jq", ".[].name")
	if err != nil {
		t.Errorf("cleanup: %v", err)
	}
	var bases []string
	for _, b := range strings.Fields(names) {
		if strings.HasPrefix(b, otherBasePrefix) {
			bases = append(bases, b)
		}
	}
	branches, err := branchesHolding(otherBaseDir)
	if err != nil {
		t.Errorf("cleanup: %v", err)
	}
	prs, err := otherBasePullRequests(bases, issues)
	if err != nil {
		t.Errorf("cleanup: %v", err)
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
	for _, b := range branches {
		switch {
		case b == def:
			t.Errorf("cleanup: %s holds a fixture's work, and stays: it is the default branch", def)
		case slices.Contains(bases, b):
			// A base branch goes last, below.
		default:
			deleteSandboxBranch(t, b)
		}
	}
	for _, n := range issues {
		if out, err := exec.Command("gh", "issue", "close", strconv.Itoa(n), "--repo", queueRepo).CombinedOutput(); err != nil {
			t.Errorf("cleanup: gh issue close %d: %v\n%s", n, err, out)
		}
	}
	for _, b := range bases {
		deleteSandboxBranch(t, b)
	}
}

// deleteSandboxBranch deletes the branch of queueRepo, unless it is already gone.
func deleteSandboxBranch(t *testing.T, branch string) {
	t.Helper()
	if _, err := newWorkGh("repos/"+queueRepo+"/git/ref/heads/"+branch, "--silent"); err != nil {
		return // already deleted
	}
	if _, err := newWorkGh("repos/"+queueRepo+"/git/refs/heads/"+branch, "-X", "DELETE"); err != nil {
		t.Errorf("cleanup: deleting the branch %s: %v", branch, err)
	}
}

// otherBasePullRequests lists the pull requests of queueRepo, in any state, into one of the base branches or whose body
// names one of the issues, read from the repository's pull requests rather than from GitHub's closing link, which a
// pull request into a branch other than the default one never gets.
func otherBasePullRequests(bases []string, issues []int) ([]linkedPR, error) {
	out, err := exec.Command("gh", "pr", "list", "--repo", queueRepo, "--state", "all", "--limit", "1000", "--json",
		"number,url,state,isDraft,baseRefName,headRefName,headRefOid,body").Output()
	if err != nil {
		return nil, fmt.Errorf("list the pull requests of %s: %w", queueRepo, err)
	}
	var all []linkedPR
	if err := json.Unmarshal(out, &all); err != nil {
		return nil, fmt.Errorf("list the pull requests of %s: %w", queueRepo, err)
	}
	var named []*regexp.Regexp
	for _, n := range issues {
		named = append(named, regexp.MustCompile(`#`+strconv.Itoa(n)+`\b`))
	}
	var prs []linkedPR
	for _, pr := range all {
		names := slices.ContainsFunc(named, func(re *regexp.Regexp) bool { return re.MatchString(pr.Body) })
		if names || slices.Contains(bases, pr.Base) {
			prs = append(prs, pr)
		}
	}
	return prs, nil
}
