package integration

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The fixture set of TestFailingTestsStillOpenADraftPullRequest in queueRepo, apart from the other sets, which it never
// reads or changes: one issue labelled retryLimitLabel, opened by each run and closed when it ends.
const (
	retryLimitLabel = "retry-limit-test"
	// sandboxSuite is a Node.js package whose npm test runs node --test: nothing to install and nothing to build, and
	// node --test prints each failing test by its name.
	sandboxSuite = "sandbox-suite"
	// failingTestName is the test of sandboxSuite that the fixture issue's change makes fail, as node --test prints it.
	failingTestName = "every retry fits within the timeout"
)

// A session whose tests fail still delivers its work: it pushes its branch and opens a draft pull request whose body
// names the test that failed, and the execution ends completed, with its tests failed. The real Claude Code runs the
// built-in agent's copy and the built-in skills of this checkout against queueRepo, from a clone that may push, on a
// fixture issue whose change breaks a test of the suite its base branch holds. The task allows no test repair, so the
// result does not depend on how a session repairs tests. The base branch on GitHub is where it was: nothing was pushed
// to it, and nothing was merged.
func TestFailingTestsStillOpenADraftPullRequest(t *testing.T) {
	claude := realClaude(t)
	for _, label := range []string{retryLimitLabel, "harmflow"} {
		sandboxLabel(t, label)
	}
	base := sandboxBase(t)
	sweepFailingTestsFixtures(t, base)
	before := branchHead(t, base)
	clone := cloneWithSuite(t, base, sandboxSuite)
	issue := suiteFixture(t, retryLimitLabel, base, "Raise the retry limit to 5",
		"Jobs give up too early when the network is slow.\n\n"+
			"In `settings.json`, raise `retries` from 3 to 5, and change nothing else.\n")
	t.Cleanup(func() { sweepFailingTestsFixtures(t, base, issue) })
	home := t.TempDir()
	d := startDaemon(t, home)
	useClaude(d, claude, clone)
	d.call("POST", "/tasks", map[string]any{"yaml": failingTestsTask(d, clone, base, retryLimitLabel, 0)}, 201)

	id := d.call("POST", "/tasks/failing-tests/run", nil, 202)["executionId"].(string)
	dir := executionDir(d, id)
	// Longer than the task's timeout, so that the execution's own end says why a session ran too long.
	x := d.waitEnd(id, withinDeadline(t, 13*time.Minute))
	if x["status"] != "completed" {
		t.Errorf("execution %s = %v (%v), want completed\n%s", id, x["status"], x["errorMessage"], runFiles(dir))
	}

	key := issueKey(issue)
	var results []string
	var created []map[string]any
	for _, e := range d.events(id) {
		payload, _ := e["payload"].(map[string]any)
		switch e["type"] {
		case "test.completed":
			if item, _ := e["workItemId"].(string); strings.EqualFold(item, key) {
				status, _ := payload["status"].(string)
				results = append(results, status)
			}
		case "pull_request.created":
			created = append(created, e)
		}
	}
	if !slices.Contains(results, "failed") || slices.Contains(results, "passed") {
		t.Errorf("the test.completed events of %s give %q, want failed and never passed\n%s", key, results, runFiles(dir))
	}
	if len(created) != 1 {
		t.Fatalf("execution %s logged %d pull_request.created events, want 1: %v\n%s", id, len(created), created, runFiles(dir))
	}
	payload, _ := created[0]["payload"].(map[string]any)
	url, _ := payload["url"].(string)
	if item, _ := created[0]["workItemId"].(string); !strings.EqualFold(item, key) || url == "" {
		t.Errorf("pull_request.created is for %q with the URL %q, want %s with its pull request's URL", item, url, key)
	}
	if outcome, tests := reportedResult(t, dir, issue); outcome != "completed" || tests != "failed" {
		t.Errorf("report.json gives %s the outcome %q and the tests %q, want completed and failed\n%s", key, outcome,
			tests, runFiles(dir))
	}
	if pr := reportedPullRequest(t, dir, issue); pr == nil || pr.URL != url || !pr.Draft || !pr.Created {
		t.Errorf("report.json gives %s the pull request %+v, want %s, a draft this execution created\n%s", key, pr, url,
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
	if tests := markdownSection(pr.Body, "## Tests"); !strings.Contains(tests, failingTestName) {
		t.Errorf("the ## Tests section of %s does not name the test %q that failed:\n%s\n\nthe body:\n%s", pr.URL,
			failingTestName, tests, pr.Body)
	}
	if after := branchHead(t, base); after != before {
		t.Errorf("the head of %s moved from %s to %s during the run, want it unchanged", base, before, after)
	}
}

// failingTestsTask is a task made of the built-in issue implementer as selecting it copies it, with the open issues of
// queueRepo labelled label as its only eligible ones, and with repairs as its limit of test repairs. Its permissions
// allow what the built-in skills run to select, implement, test with npm, push, and open the draft pull request, and
// deny gh pr merge: a denied tool wins over every allow rule, the signed-in Claude Code's own included.
func failingTestsTask(d *daemon, clone, base, label string, repairs int) string {
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
  id: failing-tests
  name: Failing tests
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
        "Bash(jq:*)", "Bash(ls:*)", "Bash(date:*)", "Bash(cd:*)", "Bash(git:*)", "Bash(npm:*)", "Bash(node:*)",
        "Bash(gh auth status:*)", "Bash(gh issue list:*)", "Bash(gh issue view:*)", "Bash(gh pr list:*)",
        "Bash(gh pr view:*)", "Bash(gh pr edit:*)", "Bash(gh pr create:*)", "Bash(gh label:*)", "Bash(gh api:*)"]
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
    delivery:
      testRepairAttempts: %[6]d
`, queueRepo, clone, base, strings.ReplaceAll(prompt, "\n", "\n    "), copied["hash"], repairs)
}

// cloneWithSuite is the repository the task targets: a clone of queueRepo that fetches from a local mirror, whose base
// branch holds one more commit than GitHub's, adding the files of testdata/<suite>, and that pushes to GitHub. The
// sandbox's default branch holds no test suite, and a test pushes nothing to it, so the suite reaches the session's
// worktree through the clone alone: the session branches from the mirror's base branch, and pushes its work branch,
// with the suite's commit under its own, to GitHub. A pull request into a branch other than the default one would
// close no issue on GitHub, so the base branch stays the default one. The mirror sits beside the clone, inside the
// folder the daemon allows.
func cloneWithSuite(t *testing.T, base, suite string) string {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mirror := filepath.Join(parent, "sandbox-mirror.git")
	// git's own configuration, which git() leaves out, holds the credentials the clone needs.
	if out, err := exec.Command("git", "clone", "--quiet", "--bare", "https://github.com/"+queueRepo, mirror).CombinedOutput(); err != nil {
		t.Fatalf("git clone --bare: %v\n%s", err, out)
	}
	clone := filepath.Join(parent, filepath.Base(queueRepo))
	git(t, parent, "clone", "--quiet", "--branch", base, mirror, clone)
	suite = filepath.Join("testdata", suite)
	err = filepath.WalkDir(suite, func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		rel, err := filepath.Rel(suite, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		to := filepath.Join(clone, rel)
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return err
		}
		return os.WriteFile(to, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copy %s into the clone: %v", suite, err)
	}
	git(t, clone, "add", "-A")
	git(t, clone, "commit", "--quiet", "-m", "chore: add the retry settings and their tests")
	git(t, clone, "push", "--quiet", "origin", base)
	// Only now, once the suite's commit is in the mirror: from here on, a push goes to GitHub.
	git(t, clone, "remote", "set-url", "--push", "origin", "https://github.com/"+queueRepo)
	return clone
}

// suiteFixture opens the fixture issue, labelled label, with the title and the body of a change to the suite's
// repository, and returns its number. When the test ends, failed or not, the fixtures under the label are swept.
func suiteFixture(t *testing.T, label, base, title, body string) int {
	t.Helper()
	out, err := exec.Command("gh", "issue", "create", "--repo", queueRepo, "--label", label,
		"--title", title, "--body", body).Output()
	if err != nil {
		t.Fatalf("gh issue create: %v", err)
	}
	url := strings.TrimSpace(string(out))
	issue, err := strconv.Atoi(url[strings.LastIndex(url, "/")+1:])
	if err != nil {
		t.Fatalf("gh issue create printed %q, not an issue's URL", url)
	}
	t.Cleanup(func() { sweepSuiteFixtures(t, label, base, issue) })
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

// sweepSuiteFixtures removes from queueRepo what the runs of a test left, found by the fixture issues and their label,
// never by the branch names the sessions chose: it closes the open pull requests that link one of the issues, deletes
// their branches, and closes the issues. The issues are the given ones and the open ones labelled label, which a run
// stopped before its cleanup leaves, and which the task's prompt would make eligible. The base branch is never deleted.
func sweepSuiteFixtures(t *testing.T, label, base string, own ...int) {
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
	for _, n := range issues {
		prs, err := linkingPullRequests(n)
		if err != nil {
			t.Errorf("cleanup of #%d: %v", n, err)
		}
		for _, pr := range prs {
			if pr.State == "OPEN" {
				if out, err := exec.Command("gh", "pr", "close", strconv.Itoa(pr.Number), "--repo", queueRepo).CombinedOutput(); err != nil {
					t.Errorf("cleanup: gh pr close %d: %v\n%s", pr.Number, err, out)
				}
			}
			if pr.Head == base {
				t.Errorf("cleanup: %s comes from %s, which stays: it is the base branch", pr.URL, base)
				continue
			}
			if exec.Command("gh", "api", "repos/"+queueRepo+"/git/ref/heads/"+pr.Head, "--silent").Run() != nil {
				continue // already deleted
			}
			if out, err := exec.Command("gh", "api", "-X", "DELETE", "repos/"+queueRepo+"/git/refs/heads/"+pr.Head).CombinedOutput(); err != nil {
				t.Errorf("cleanup: deleting the branch %s: %v\n%s", pr.Head, err, out)
			}
		}
		if out, err := exec.Command("gh", "issue", "close", strconv.Itoa(n), "--repo", queueRepo).CombinedOutput(); err != nil {
			t.Errorf("cleanup: gh issue close %d: %v\n%s", n, err, out)
		}
	}
}

// sweepFailingTestsFixtures removes from queueRepo what the test's runs left: what sweepSuiteFixtures finds by the
// fixture issues and their label, and then every branch but base that holds the package.json of sandboxSuite, such as
// one whose pull request went into a base other than the default branch, where GitHub links it to no issue. Every work
// branch of a run holds that file, which only the clone's base branch adds, and no other fixture set's branch holds it.
// Deleting a branch closes the pull request still open from it.
func sweepFailingTestsFixtures(t *testing.T, base string, own ...int) {
	t.Helper()
	sweepSuiteFixtures(t, retryLimitLabel, base, own...)
	branches, err := branchesHoldingSuiteFile(sandboxSuite, "package.json")
	if err != nil {
		t.Errorf("cleanup: %v", err)
	}
	for _, b := range branches {
		if b == base {
			t.Errorf("cleanup: %s holds the package.json of %s, and stays: it is the base branch", base, sandboxSuite)
			continue
		}
		if out, err := exec.Command("gh", "api", "-X", "DELETE", "repos/"+queueRepo+"/git/refs/heads/"+b).CombinedOutput(); err != nil {
			t.Errorf("cleanup: deleting the branch %s: %v\n%s", b, err, out)
		}
	}
}

// branchesHoldingSuiteFile lists the branches of queueRepo whose file at path is the one testdata/<suite> holds at the
// same path, byte for byte: GitHub gives each file the SHA of its Git blob, which the fixture's own bytes give too.
func branchesHoldingSuiteFile(suite, path string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join("testdata", suite, path))
	if err != nil {
		return nil, fmt.Errorf("read the %s of %s: %w", path, suite, err)
	}
	sum := sha1.Sum(fmt.Appendf(nil, "blob %d\x00%s", len(data), data))
	blob := hex.EncodeToString(sum[:])
	out, err := exec.Command("gh", "api", "--paginate", "repos/"+queueRepo+"/branches?per_page=100", "--jq", ".[].name").Output()
	if err != nil {
		return nil, fmt.Errorf("list the branches of %s: %w", queueRepo, err)
	}
	var branches []string
	for _, b := range strings.Fields(string(out)) {
		got, err := exec.Command("gh", "api", "repos/"+queueRepo+"/contents/"+path+"?ref="+b, "--jq", ".sha").Output()
		var exit *exec.ExitError
		switch {
		case err == nil:
			if strings.TrimSpace(string(got)) == blob {
				branches = append(branches, b)
			}
		case !errors.As(err, &exit):
			return nil, fmt.Errorf("read %s on %s: %w", path, b, err)
		}
	}
	return branches, nil
}

// markdownSection is the text under the heading line of body, up to the next heading of the same level, or "" when no
// line is the heading.
func markdownSection(body, heading string) string {
	level := heading[:strings.Index(heading, " ")+1]
	var section []string
	in := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.TrimSpace(line) == heading:
			in = true
		case in && strings.HasPrefix(line, level):
			return strings.Join(section, "\n")
		case in:
			section = append(section, line)
		}
	}
	return strings.Join(section, "\n")
}

// reportedResult is the outcome and the tests' status the execution's report.json gives the issue's work item, both
// "" when the report lists no such item.
func reportedResult(t *testing.T, dir string, issue int) (outcome, tests string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		t.Errorf("report.json: %v", err)
		return "", ""
	}
	var report struct {
		WorkItems []struct {
			Key     string `json:"key"`
			Outcome string `json:"outcome"`
			Tests   struct {
				Status string `json:"status"`
			} `json:"tests"`
		} `json:"workItems"`
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Errorf("report.json: %v", err)
		return "", ""
	}
	for _, w := range report.WorkItems {
		if strings.EqualFold(w.Key, issueKey(issue)) {
			return w.Outcome, w.Tests.Status
		}
	}
	return "", ""
}
