package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The fixture set of TestResumedSessionReadsTheDraftsDiffCommentsAndTests in queueRepo, apart from the other sets,
// which it never reads or changes: one issue labelled resumeCommentLabel, and the draft pull request that links it with
// a comment on it, all opened by each run and closed when it ends.
const (
	resumeCommentLabel = "resume-comment-test"
	// resumeCommentTimeout is the timeoutSeconds the fixture's comment asks for. The issue asks for no timeout, and a
	// repair of the failing test has no reason to pick this one: a branch that holds it holds the comment's change.
	resumeCommentTimeout = 75
)

// resumeCommentText is the fixture's conversation comment on the draft pull request: one more change within the issue,
// which the issue's text does not ask for.
var resumeCommentText = fmt.Sprintf("Five retries 10 s apart no longer fit within the 30 s timeout. While you are in "+
	"`settings.json`, please also raise `timeoutSeconds` to %d, so that a slow network keeps some headroom after the "+
	"last retry.", resumeCommentTimeout)

// A resumed draft's session reads what the draft already holds before it changes any code: its commits and its diff
// against the base branch, its last test report, and its relevant comments, as the subsection "Read a resumed draft's
// context" of hf-github-worktree gives. It then makes the change a comment asks for, and updates the same pull request.
//
// The fixture issue asks for a change to the suite cloneWithSuite adds. From a branch of its own, the test pushes one
// commit that makes the change and breaks one test of the suite, and opens a managed draft pull request that links the
// issue, whose ## Tests section names that test. It then leaves on the draft a conversation comment, after that push,
// asking for one more change within the issue. A first execution, through a stand-in Claude Code, reports the issue's
// item completed with its tests failed and that pull request, so the daemon's ledger keeps the item in progress. Then
// the real Claude Code runs the built-in agent's copy and the built-in skills of this checkout once. Its log shows the
// commands of that subsection for the draft, each before the session's first edit of a file; it reports the issue
// resumed with the test's pull request, which it updated and left an open draft whose branch holds a commit beyond the
// test's and the change the comment asked for. No other pull request links the issue.
func TestResumedSessionReadsTheDraftsDiffCommentsAndTests(t *testing.T) {
	claude := realClaude(t)
	for _, label := range []string{resumeCommentLabel, "harmflow"} {
		sandboxLabel(t, label)
	}
	base := sandboxBase(t)
	sweepResumeCommentFixtures(t, base)
	clone := cloneWithSuite(t, base, sandboxSuite)
	issue := suiteFixture(t, resumeCommentLabel, base, "Let jobs retry five times on a slow network",
		"Jobs give up too early when the network is slow.\n\nIn `settings.json`, raise `retries` from 3 to 5.\n")
	t.Cleanup(func() { sweepResumeCommentFixtures(t, base, issue) })
	draft, pushed := commentedDraft(t, clone, base, issue)
	key := issueKey(issue)

	home := t.TempDir()
	d := startDaemon(t, home)
	stand := standIn(t, "fake-claude", "claude",
		"\nSCENARIO=ok\n", "\nSCENARIO=report\n",
		`{"type":"test.completed","status":"passed","message":"12 passed"}`,
		`{"type":"test.completed","status":"failed","message":"1 failed"}`,
		standInReport, failedDraftReport(t, issue, draft))
	useClaude(d, stand.path, clone)
	d.call("POST", "/tasks", map[string]any{"yaml": failingTestsTask(d, clone, base, resumeCommentLabel, 0)}, 201)

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
	checkDraftContextRead(t, d, next, dir, base, draft)
	checkUpdatedPullRequest(t, d, next, dir, issue, draft)

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
	if got := headSettings(t, pr.HeadOID); got.Retries != 5 || got.TimeoutSeconds != resumeCommentTimeout {
		t.Errorf("the settings.json of %s at %s has retries %d and timeoutSeconds %d, want 5, the issue's change, and %d, "+
			"the comment's\nthe session ran:\n%s", pr.URL, pr.HeadOID, got.Retries, got.TimeoutSeconds,
			resumeCommentTimeout, commandsOf(d, next))
	}
}

// commentedDraft opens a draft pull request of queueRepo into base for the fixture issue, and returns it with the one
// commit its branch holds beyond the base branch of the clone cloneWithSuite made. The commit makes the issue's change,
// which breaks failingTestName of the suite. The branch starts from the clone's base branch, which only the mirror
// holds, so the test commits in the clone and pushes the branch twice: to GitHub, where the pull request opens, and to
// the mirror, from which the session fetches it. The clone's own checkout goes back to the base branch, without the
// work branch, as a clone the session never worked in. The pull request carries the marking of the task
// failingTestsTask makes and a ## Tests section that names the failing test, as hf-github-draft-pr writes it. Once
// GitHub links it to the issue, the test leaves on it a conversation comment, resumeCommentText, newer than the commit:
// the subsection "Read a resumed draft's context" of hf-github-worktree reads the comments left after the last push.
func commentedDraft(t *testing.T, clone, base string, issue int) (restartPR, string) {
	t.Helper()
	branch := "slow-network-retries-" + runSuffix()
	git(t, clone, "switch", "--quiet", "-c", branch)
	path := filepath.Join(clone, "settings.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(data), `"retries": 3,`, `"retries": 5,`, 1)
	if changed == string(data) {
		t.Fatalf("the settings.json of %s holds no line with retries at 3:\n%s", sandboxSuite, data)
	}
	if err := os.WriteFile(path, []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, clone, "commit", "--quiet", "-am", "Raise the retry limit to 5")
	commit := git(t, clone, "rev-parse", "HEAD")
	committed, err := time.Parse(time.RFC3339, git(t, clone, "log", "-1", "--format=%cI"))
	if err != nil {
		t.Fatal(err)
	}
	// git's own configuration, which git() leaves out, holds the credentials the push to GitHub needs.
	if out, err := exec.Command("git", "-C", clone, "push", "--quiet", "origin", branch).CombinedOutput(); err != nil {
		t.Fatalf("git push origin %s: %v\n%s", branch, err, out)
	}
	git(t, clone, "push", "--quiet", git(t, clone, "remote", "get-url", "origin"), branch)
	git(t, clone, "switch", "--quiet", base)
	git(t, clone, "branch", "--quiet", "-D", branch)

	pr := openFixturePRFrom(t, base, issue, fixturePR{
		Branch: branch,
		Title:  "Let jobs retry five times on a slow network",
		Body: fmt.Sprintf("Raises `retries` in `settings.json` from 3 to 5.\n\nCloses #%d\n\n"+
			"Managed by HarmFlow task %s.\n\n## Tests\n\n`npm test`: 1 of 2 tests failed.\n\n"+
			"- %s: 5 retries 10 s apart take 50 s, past the 30 s timeout.\n", issue, resumeDraftTask, failingTestName),
		Draft:  true,
		Labels: []string{"harmflow"},
		Alone:  true,
	})
	// GitHub records the comment to the second, and the subsection keeps only a comment newer than the commit.
	time.Sleep(time.Until(committed.Add(2 * time.Second)))
	out := newWorkAPI(t, fmt.Sprintf("repos/%s/issues/%d/comments", queueRepo, pr.Number), "-f",
		"body="+resumeCommentText, "--jq", ".created_at")
	if created, err := time.Parse(time.RFC3339, out); err != nil || !created.After(committed) {
		t.Fatalf("GitHub gives the comment on %s the time %q (%v), want one after the commit's %v: the session reads "+
			"only the comments left after the last push, and this machine's clock may be ahead of GitHub's", pr.URL, out,
			err, committed)
	}
	return pr, commit
}

// sweepResumeCommentFixtures removes from queueRepo what the test's runs left: what sweepSuiteFixtures finds by the
// fixture issues and their label, and then, as sweepFailingTestsFixtures does, every branch but base that holds the
// package.json of sandboxSuite, such as one a session pushed for the issue with no pull request linking it, or the
// test's own when GitHub had not linked its pull request yet. Deleting a branch closes the pull request still open from
// it, and its comment goes with it.
func sweepResumeCommentFixtures(t *testing.T, base string, own ...int) {
	t.Helper()
	sweepSuiteFixtures(t, resumeCommentLabel, base, own...)
	sweepSuiteBranches(t, base)
}

// draftContextReads are the commands of the subsection "Read a resumed draft's context" of hf-github-worktree for the
// draft pull request number into base, each matched by what it reads rather than by its full text: a session fills in
// the placeholders, quotes the paths, and may change into the worktree first.
func draftContextReads(base string, number int) map[string]func(string) bool {
	match := func(patterns ...string) func(string) bool {
		var res []*regexp.Regexp
		for _, p := range patterns {
			res = append(res, regexp.MustCompile("(?s)"+p))
		}
		return func(command string) bool {
			for _, re := range res {
				if !re.MatchString(command) {
					return false
				}
			}
			return true
		}
	}
	against := regexp.QuoteMeta("origin/"+base) + `\.\.\.?HEAD\b`
	return map[string]func(string) bool{
		"git log of its commits":    match(`\bgit\b.*\blog\b.*` + against),
		"git diff against the base": match(`\bgit\b.*\bdiff\b.*` + against),
		// The pull request's number goes in the query's variable n, or straight into the query.
		"gh api graphql of its tests, threads, and comments": match(`\bgh\s+api\s+graphql\b`, `\breviewThreads\b`,
			`\bcomments\b`, `(?:\bn=["']?|\bnumber:\s*)`+strconv.Itoa(number)+`\b`),
	}
}

// checkDraftContextRead checks that the execution's log shows each command of draftContextReads for the draft pull
// request before the session's first edit of a file: the first call of an editing tool on a path in the task's
// workspace, where the worktree is, or the first git commit, which follows an edit made in any other way. A write to
// the execution's own folder, its progress.jsonl or a body file, is not an edit of the work.
func checkDraftContextRead(t *testing.T, d *daemon, id, dir, base string, pr restartPR) {
	t.Helper()
	workspace := taskWorkspace(t, dir)
	commit := regexp.MustCompile(`\bgit\b.*\bcommit\b`)
	reads := draftContextReads(base, pr.Number)
	seen := map[string]bool{}
	edit := ""
	for _, c := range toolCalls(d.events(id)) {
		switch c.tool {
		case "Edit", "MultiEdit", "Write", "NotebookEdit":
			if strings.HasPrefix(c.path, workspace+string(filepath.Separator)) &&
				!strings.HasPrefix(c.path, dir+string(filepath.Separator)) {
				edit = c.tool + " " + c.path
			}
		default:
			if commit.MatchString(c.command) {
				edit = c.command
			}
		}
		if edit != "" {
			break
		}
		for name, read := range reads {
			if read(c.command) {
				seen[name] = true
			}
		}
	}
	for name := range reads {
		if !seen[name] {
			t.Errorf("execution %s ran no %s for %s before its first edit of a file, %q\nthe session ran:\n%s", id, name,
				pr.URL, edit, commandsOf(d, id))
		}
	}
	if edit == "" {
		t.Errorf("execution %s edited no file in its workspace %s and made no commit\nthe session ran:\n%s", id, workspace,
			commandsOf(d, id))
	}
}

// taskWorkspace is the task's workspace the execution's context.json gives, where the session's worktrees are.
func taskWorkspace(t *testing.T, dir string) string {
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
	if err := json.Unmarshal(data, &ctx); err != nil || ctx.Paths.Workspace == "" {
		t.Fatalf("the context.json of %s gives no workspace: %v\n%s", dir, err, data)
	}
	return filepath.Clean(ctx.Paths.Workspace)
}

// suiteSettings is what the tests of sandboxSuite read from its settings.json.
type suiteSettings struct {
	Retries           int `json:"retries"`
	RetryDelaySeconds int `json:"retryDelaySeconds"`
	TimeoutSeconds    int `json:"timeoutSeconds"`
}

// headSettings is the settings.json of queueRepo at the commit head, as GitHub holds it.
func headSettings(t *testing.T, head string) suiteSettings {
	t.Helper()
	var s suiteSettings
	if err := newWorkJSON(&s, "repos/"+queueRepo+"/contents/settings.json?ref="+head, "-H",
		"Accept: application/vnd.github.raw+json"); err != nil {
		t.Fatalf("read settings.json at %s: %v", head, err)
	}
	return s
}
