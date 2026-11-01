package integration

import (
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

// restartLabel marks the fixture issues TestRestartDuplicatesNoBranchOrPullRequest opens in queueRepo, one for each
// kill point, apart from the queue-test fixtures, which it never reads or changes.
const restartLabel = "restart-test"

// A restart of the daemon duplicates no branch and no pull request. The real Claude Code runs the built-in agent's copy
// and the built-in skills of this checkout against queueRepo, on a fixture issue the test opens, from a clone that may
// push. The daemon is killed at the two points the specification names, once the work branch is pushed and before its
// pull request is opened, and once the draft pull request is opened; then the task's next firing runs to its end. Each
// time, the fixture issue ends with exactly one branch and one draft pull request. After the kill once the draft pull
// request is opened, the next execution also found that pull request and updated it, as checkUpdatedPullRequest reads.
// The next session also reconciled the worktree the killed one left in the clone: the clone ends with one worktree for
// the issue, on the branch the first execution pushed, and no other local branch; after the kill once the push
// completed, the pull request's head descends from the commit the first execution pushed.
func TestRestartDuplicatesNoBranchOrPullRequest(t *testing.T) {
	claude := realClaude(t)
	for _, label := range []string{restartLabel, "harmflow"} {
		ensureLabel(t, label)
	}
	for _, point := range []struct {
		name string
		// afterPush kills the daemon once the push completes. Its first firing is denied gh pr create, so that no pull
		// request is open when it is killed; the next firing may run it.
		afterPush bool
	}{
		{"killed after the push", true},
		{"killed after the pull request", false},
	} {
		t.Run(point.name, func(t *testing.T) {
			issue, file := restartFixture(t)
			clone := pushableClone(t)
			base := git(t, clone, "symbolic-ref", "--short", "HEAD")
			home := t.TempDir()
			d := startDaemon(t, home)
			useClaude(d, claude, clone)
			created := d.call("POST", "/tasks", map[string]any{"yaml": restartTask(d, clone, issue, !point.afterPush)}, 201)

			first := d.call("POST", "/tasks/restart/run", nil, 202)["executionId"].(string)
			dir := executionDir(d, first)
			var op, opened string
			if point.afterPush {
				op = awaitPush(t, d, first, dir)
			} else {
				opened = awaitPullRequest(t, d, first, dir)
				op = "pull_request.created " + opened
			}
			d = restartMidRun(t, d, home, claude, clone)

			if x := d.call("GET", "/executions/"+first, nil, 200); x["status"] != "interrupted" {
				t.Errorf("the first execution after the restart: status %v, want interrupted", x["status"])
			}
			pushed, prs := issueWork(t, issue, file)
			if len(pushed) != 1 {
				t.Fatalf("at the kill, %s holds branches %v for #%d, want one\n%s", queueRepo, pushed, issue, runFiles(dir))
			}
			if point.afterPush && len(prs) != 0 {
				t.Fatalf("a pull request was open for #%d when the daemon was killed after the push: %+v\nthe session ran:\n%s",
					issue, prs, commandsOf(d, first))
			}
			left := cloneWorktrees(t, clone)
			var killed cloneWorktree
			if at := slices.IndexFunc(left, func(w cloneWorktree) bool { return w.Branch == pushed[0] }); at >= 0 {
				killed = left[at]
			} else {
				t.Errorf("at the kill, the clone holds no worktree on %s, the branch the first execution pushed\n%s\n%s",
					pushed[0], cloneState(t, clone), runFiles(dir))
			}
			var pushedCommit string
			if point.afterPush {
				pushedCommit = branchHead(t, pushed[0])
				d.call("PUT", "/tasks/restart", map[string]any{"yaml": restartTask(d, clone, issue, true),
					"baseRevision": created["revision"]}, 200)
			}

			next := d.call("POST", "/tasks/restart/run", nil, 202)["executionId"].(string)
			nextDir := executionDir(d, next)
			// Longer than the task's timeout, so that the execution's own end says why a session ran too long.
			x := d.waitEnd(next, 13*time.Minute)
			if x["status"] != "completed" {
				t.Errorf("the next execution %s = %v (%v), want completed\n%s", next, x["status"], x["errorMessage"],
					runFiles(nextDir))
			}
			ctx := readContext(t, x)
			if want := (interruptedEntry{first, issueKey(issue), op}); !slices.Contains(ctx.Interrupted, want) {
				t.Errorf("the next context's interruptedExecutions %+v, want one %+v", ctx.Interrupted, want)
			}

			branches, prs := issueWork(t, issue, file)
			if len(branches) != 1 || len(prs) != 1 {
				t.Fatalf("after the next firing, %s holds branches %v and pull requests %+v for #%d, want one of each\n%s",
					queueRepo, branches, prs, issue, runFiles(nextDir))
			}
			pr := prs[0]
			if pr.Head != pushed[0] || !pr.Draft {
				t.Errorf("the pull request %s is from %s, draft %v; want a draft from %s, the branch the first execution "+
					"pushed", pr.URL, pr.Head, pr.Draft, pushed[0])
			}
			if x["pullRequestUrl"] != pr.URL {
				t.Errorf("the next execution's pullRequestUrl is %v, want %s", x["pullRequestUrl"], pr.URL)
			}
			if !point.afterPush && pr.URL != opened {
				t.Errorf("the pull request for #%d is %s, want %s, the one the first execution opened", issue, pr.URL, opened)
			}
			checkReconciledWorktree(t, clone, base, pushed[0], killed, nextDir)
			if point.afterPush {
				// GitHub's status is ahead or identical when the pushed commit is an ancestor of the head.
				if status, _ := compareCommits(t, pushedCommit, pr.HeadOID); status != "ahead" && status != "identical" {
					t.Errorf("GitHub compares %s, the commit the first execution pushed, with %s, the head of %s: %q, want "+
						"ahead or identical, a head built on the pushed work\n%s\n%s", pushedCommit, pr.HeadOID, pr.URL, status,
						cloneState(t, clone), runFiles(nextDir))
				}
			}
			if !point.afterPush {
				checkUpdatedPullRequest(t, d, next, nextDir, issue, pr)
			}
		})
	}
}

// checkUpdatedPullRequest checks that the execution found the fixture issue's existing pull request and updated it,
// rather than leaving it as an earlier session left it. Its events hold a pull_request.updated for the issue with the
// pull request's URL, and no pull_request.created. Its report.json gives the issue the kind resume and that pull
// request, a draft it did not create. On GitHub, the record of the body's edits puts the last one after the
// execution's execution.started event and before the event that ended it.
func checkUpdatedPullRequest(t *testing.T, d *daemon, id, dir string, issue int, pr restartPR) {
	t.Helper()
	key := issueKey(issue)
	var updated []string
	var created []map[string]any
	var started, ended time.Time
	for _, e := range d.events(id) {
		at, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(e["timestamp"]))
		switch e["type"] {
		case "pull_request.updated":
			if item, _ := e["workItemId"].(string); strings.EqualFold(item, key) {
				payload, _ := e["payload"].(map[string]any)
				url, _ := payload["url"].(string)
				updated = append(updated, url)
			}
		case "pull_request.created":
			created = append(created, e)
		case "execution.started":
			if started.IsZero() {
				started = at
			}
		case "execution.completed", "execution.no_work", "execution.failed", "execution.cancelled",
			"execution.interrupted", "execution.needs_attention":
			if ended.IsZero() {
				ended = at
			}
		}
	}
	if !slices.Contains(updated, pr.URL) || len(created) != 0 {
		t.Errorf("execution %s logged pull_request.updated for %s with %q and pull_request.created %v, want an update "+
			"of %s and no creation\n%s", id, key, updated, created, pr.URL, runFiles(dir))
	}

	kind := reportedKind(t, dir, issue)
	if reported := reportedPullRequest(t, dir, issue); kind != "resume" || reported == nil || reported.URL != pr.URL ||
		!reported.Draft || reported.Created {
		t.Errorf("report.json gives %s the kind %q and the pull request %+v, want resume and %s, a draft it did not "+
			"create\n%s", key, kind, reported, pr.URL, runFiles(dir))
	}

	if started.IsZero() || ended.IsZero() {
		t.Fatalf("execution %s logged no start or no end: started %v, ended %v", id, started, ended)
	}
	edited := lastBodyEdit(t, pr.Number)
	// GitHub records an edit to the second, so the start is compared to the second too.
	if edited.Before(started.Truncate(time.Second)) || edited.After(ended) {
		t.Errorf("GitHub records the last edit of the body of %s at %v, want it within execution %s, from %v to %v",
			pr.URL, edited, id, started, ended)
	}
}

// checkReconciledWorktree checks that the next session reconciled the worktree the killed one left in the clone, as
// hf-github-worktree's "Create or reuse the worktree" says: the clone holds exactly one worktree besides its own
// checkout, on the branch the first execution pushed, and no local branch but that one and base. The worktree is the
// one the killed session left, reused, or that one is gone from the disk too, replaced by it.
func checkReconciledWorktree(t *testing.T, clone, base, branch string, killed cloneWorktree, dir string) {
	t.Helper()
	kept := cloneWorktrees(t, clone)
	if len(kept) != 1 || kept[0].Branch != branch {
		t.Errorf("after the next firing, the clone holds the worktrees %+v, want one on %s, the branch the first "+
			"execution pushed\n%s\n%s", kept, branch, cloneState(t, clone), runFiles(dir))
	} else if killed.Path != "" && kept[0].Path != killed.Path {
		if _, err := os.Stat(killed.Path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("after the next firing, %s, the worktree the killed session left, is still on the disk beside %s "+
				"(stat: %v), want it reused or gone\n%s\n%s", killed.Path, kept[0].Path, err, cloneState(t, clone),
				runFiles(dir))
		}
	}
	var others []string
	for _, b := range strings.Fields(git(t, clone, "for-each-ref", "--format=%(refname:short)", "refs/heads")) {
		if b != base && b != branch {
			others = append(others, b)
		}
	}
	if len(others) != 0 {
		t.Errorf("after the next firing, the clone holds the local branches %v besides %s and %s, want none\n%s\n%s",
			others, base, branch, cloneState(t, clone), runFiles(dir))
	}
}

// cloneWorktree is a worktree of a clone, as git worktree list --porcelain prints it: its path, and its branch, ""
// when its HEAD is detached.
type cloneWorktree struct {
	Path   string
	Branch string
}

// cloneWorktrees lists the clone's worktrees besides its own checkout, which git lists first. A worktree whose folder
// is gone, which git marks prunable, is listed too: it is still registered in the clone.
func cloneWorktrees(t *testing.T, clone string) []cloneWorktree {
	t.Helper()
	var list []cloneWorktree
	for _, block := range strings.Split(git(t, clone, "worktree", "list", "--porcelain"), "\n\n") {
		var w cloneWorktree
		for _, line := range strings.Split(block, "\n") {
			if path, ok := strings.CutPrefix(line, "worktree "); ok {
				w.Path = path
			} else if ref, ok := strings.CutPrefix(line, "branch "); ok {
				w.Branch = strings.TrimPrefix(ref, "refs/heads/")
			}
		}
		list = append(list, w)
	}
	return list[1:]
}

// cloneState is the clone's worktree list and local branches, for a failure message.
func cloneState(t *testing.T, clone string) string {
	t.Helper()
	return fmt.Sprintf("the clone's git worktree list --porcelain:\n%s\nits local branches:\n%s\n",
		git(t, clone, "worktree", "list", "--porcelain"),
		git(t, clone, "for-each-ref", "--format=%(refname:short) %(objectname)", "refs/heads"))
}

// reportedKind is the kind the execution's report.json gives the issue's work item, new or resume: "" when the report
// lists no such item, or gives it no kind.
func reportedKind(t *testing.T, dir string, issue int) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		t.Errorf("report.json: %v", err)
		return ""
	}
	var report struct {
		WorkItems []struct {
			Key  string `json:"key"`
			Kind string `json:"kind"`
		} `json:"workItems"`
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Errorf("report.json: %v", err)
		return ""
	}
	for _, w := range report.WorkItems {
		if strings.EqualFold(w.Key, issueKey(issue)) {
			return w.Kind
		}
	}
	return ""
}

// lastBodyEdit is when the body of the pull request of queueRepo was last edited, by GitHub's record of the body's
// edits: the latest editedAt of its userContentEdits, never what a session reports. It is the zero time when the body
// was never edited.
func lastBodyEdit(t *testing.T, number int) time.Time {
	t.Helper()
	const query = `query($owner: String!, $repo: String!, $n: Int!) { repository(owner: $owner, name: $repo) {
  pullRequest(number: $n) { userContentEdits(first: 100) { nodes { editedAt } } } } }`
	owner, name, _ := strings.Cut(queueRepo, "/")
	out, err := exec.Command("gh", "api", "graphql", "-F", "owner="+owner, "-F", "repo="+name, "-F", "n="+strconv.Itoa(number),
		"-f", "query="+query, "--jq", "[.data.repository.pullRequest.userContentEdits.nodes[].editedAt]").Output()
	if err != nil {
		t.Fatalf("read the body's edits of %s#%d: %v", queueRepo, number, err)
	}
	var edits []time.Time
	if err := json.Unmarshal(out, &edits); err != nil {
		t.Fatalf("read the body's edits of %s#%d: %v\n%s", queueRepo, number, err, out)
	}
	var last time.Time
	for _, at := range edits {
		if at.After(last) {
			last = at
		}
	}
	return last
}

// ensureLabel creates the label in queueRepo unless it is there already: the fixtures' label, and the marking of the
// draft pull requests the built-in skills open.
func ensureLabel(t *testing.T, name string) {
	t.Helper()
	if exec.Command("gh", "api", "repos/"+queueRepo+"/labels/"+name, "--silent").Run() == nil {
		return
	}
	if out, err := exec.Command("gh", "label", "create", name, "--repo", queueRepo).CombinedOutput(); err != nil {
		t.Fatalf("gh label create %s: %v\n%s", name, err, out)
	}
}

// restartFixture opens a fixture issue labelled restartLabel, which asks for one new file, unique to the run, and
// returns its number and the file. When the test ends, failed or not, it closes the pull requests linking the issue or
// coming from a branch that holds the file, deletes those branches, and closes the issue: what the sessions made is
// found by the issue and its file, never by the branch names they chose.
func restartFixture(t *testing.T) (issue int, file string) {
	t.Helper()
	run := runSuffix()
	file = "restart-test/" + run + ".txt"
	body := fmt.Sprintf("A fixture of HarmFlow's restart test, which closes it when it ends.\n\n"+
		"Add the file `%s` with the single line `%s`, and change nothing else. The repository has no tests.\n", file, run)
	out, err := exec.Command("gh", "issue", "create", "--repo", queueRepo, "--label", restartLabel,
		"--title", "Restart test "+run+": add "+file, "--body", body).Output()
	if err != nil {
		t.Fatalf("gh issue create: %v", err)
	}
	url := strings.TrimSpace(string(out))
	issue, err = strconv.Atoi(url[strings.LastIndex(url, "/")+1:])
	if err != nil {
		t.Fatalf("gh issue create printed %q, not an issue's URL", url)
	}
	t.Cleanup(func() {
		branches, err := issueBranches(file)
		if err != nil {
			t.Errorf("cleanup of #%d: %v", issue, err)
		}
		prs, err := issuePullRequests(issue, branches)
		if err != nil {
			t.Errorf("cleanup of #%d: %v", issue, err)
		}
		for _, pr := range prs {
			if pr.State != "OPEN" {
				continue
			}
			if out, err := exec.Command("gh", "pr", "close", strconv.Itoa(pr.Number), "--repo", queueRepo).CombinedOutput(); err != nil {
				t.Errorf("cleanup: gh pr close %d: %v\n%s", pr.Number, err, out)
			}
		}
		for _, b := range branches {
			if out, err := exec.Command("gh", "api", "-X", "DELETE", "repos/"+queueRepo+"/git/refs/heads/"+b).CombinedOutput(); err != nil {
				t.Errorf("cleanup: deleting the branch %s: %v\n%s", b, err, out)
			}
		}
		if out, err := exec.Command("gh", "issue", "close", strconv.Itoa(issue), "--repo", queueRepo).CombinedOutput(); err != nil {
			t.Errorf("cleanup: gh issue close %d: %v\n%s", issue, err, out)
		}
	})
	return issue, file
}

// pushableClone is the repository the task targets, cloned as shallowClone does, but with its push URL: the sessions
// push their work branch from it.
func pushableClone(t *testing.T) string {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, filepath.Base(queueRepo))
	if out, err := exec.Command("git", "clone", "--quiet", "--depth", "1", "https://github.com/"+queueRepo, dir).CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, out)
	}
	return dir
}

// restartTask is a task made of the built-in issue implementer as selecting it copies it, with the fixture issue as its
// one eligible issue. Its permissions allow what the built-in skills run to select, implement, push, and report, and
// deny gh pr create unless openPullRequest is set. Leaving gh pr create out of the allowed tools is not enough where
// the signed-in Claude Code's own settings allow more, as a rule Bash(*) does: a session there ran the push and
// gh pr create in one call and opened its pull request. A denied tool wins over every allow rule, the user's own
// included, and holds a command chained in the same call.
func restartTask(d *daemon, clone string, issue int, openPullRequest bool) string {
	d.t.Helper()
	copied := d.builtinAgentCopy("hf-github-issue-implementer")
	const eligible = "- **Eligible issues:** open issues labeled `agent-ready` that are not labeled `blocked`.\n"
	prompt, _ := copied["prompt"].(string)
	if !strings.Contains(prompt, eligible) {
		d.t.Fatalf("the copy of hf-github-issue-implementer has no line %q to customize:\n%s", eligible, prompt)
	}
	prompt = strings.Replace(prompt, eligible, fmt.Sprintf(
		"- **Eligible issues:** issue #%d of %s alone; every other issue is excluded.\n", issue, queueRepo), 1)
	denied := ""
	if !openPullRequest {
		denied = "\n      deniedTools: [\"Bash(gh pr create:*)\"]"
	}
	return fmt.Sprintf(`apiVersion: harmflow/v1alpha1
kind: Task
metadata:
  id: restart
  name: Restart reconciliation
spec:
  type: github
  prompt: |
    %[3]s
  origin:
    agent: hf-github-issue-implementer
    hash: %[4]s
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
      # gh api can also open a pull request, which the check at the kill after the push catches.
      allowedTools: [Read, Glob, Grep, Write, Edit, Skill, TodoWrite, "Bash(echo:*)", "Bash(printf:*)", "Bash(cat:*)",
        "Bash(jq:*)", "Bash(ls:*)", "Bash(date:*)", "Bash(cd:*)", "Bash(git:*)", "Bash(gh auth status:*)",
        "Bash(gh issue list:*)", "Bash(gh issue view:*)", "Bash(gh pr list:*)", "Bash(gh pr view:*)",
        "Bash(gh pr edit:*)", "Bash(gh pr create:*)", "Bash(gh label:*)", "Bash(gh api:*)"]%[5]s
  execution:
    timeoutMinutes: 12
  github:
    repositories:
      - name: %[1]s
        localPath: %[2]s
    queue:
      ordering: oldest_first
      maxItemsPerCycle: 1
      resumeStrategy: resume_first
`, queueRepo, clone, strings.ReplaceAll(prompt, "\n", "\n    "), copied["hash"], denied)
}

// awaitPush waits until the session's push of a branch has completed, and returns the command as the daemon records it
// for the execution's last operation.
func awaitPush(t *testing.T, d *daemon, id, dir string) string {
	t.Helper()
	var push string
	awaitFirst(t, d, id, dir, "a completed git push", func(events []map[string]any) bool {
		commands := map[any]string{}
		for _, e := range events {
			p, _ := e["payload"].(map[string]any)
			switch e["type"] {
			case "tool.started":
				if c, _ := p["command"].(string); strings.Contains(c, "git ") && strings.Contains(c, " push ") {
					commands[p["toolId"]] = c
				}
			case "tool.completed":
				if c, ok := commands[p["toolId"]]; ok && p["status"] == "succeeded" {
					push = c
					return true
				}
			}
		}
		return false
	})
	// What the daemon keeps of a command: executor.go trims it to 300 bytes.
	if push = strings.TrimSpace(push); len(push) > 300 {
		push = push[:300] + "…"
	}
	return push
}

// awaitPullRequest waits until the execution's log has its pull_request.created event, and returns the pull request's
// URL.
func awaitPullRequest(t *testing.T, d *daemon, id, dir string) string {
	t.Helper()
	var url string
	awaitFirst(t, d, id, dir, "a pull_request.created event", func(events []map[string]any) bool {
		for _, e := range events {
			if p, _ := e["payload"].(map[string]any); e["type"] == "pull_request.created" {
				url, _ = p["url"].(string)
				return url != ""
			}
		}
		return false
	})
	return url
}

// awaitFirst polls the execution's log until done holds, for as long as a real session takes to get there, and fails
// the test when the execution ends first.
func awaitFirst(t *testing.T, d *daemon, id, dir, what string, done func([]map[string]any) bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Minute); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if done(d.events(id)) {
			return
		}
		if x := d.call("GET", "/executions/"+id, nil, 200); x["endedAt"] != nil {
			t.Fatalf("execution %s ended %v before %s: %v\n%s", id, x["status"], what, x["errorMessage"], runFiles(dir))
		}
	}
	t.Fatalf("execution %s reached no %s in 10 minutes\n%s", id, what, runFiles(dir))
}

// commandsOf lists the commands the execution's log shows, each with how it ended, for a failure message.
func commandsOf(d *daemon, id string) string {
	d.t.Helper()
	var b strings.Builder
	ended := map[any]any{}
	events := d.events(id)
	for _, e := range events {
		if p, _ := e["payload"].(map[string]any); e["type"] == "tool.completed" {
			ended[p["toolId"]] = p["status"]
		}
	}
	for _, e := range events {
		if p, _ := e["payload"].(map[string]any); e["type"] == "tool.started" && p["command"] != nil {
			fmt.Fprintf(&b, "%v [%v]: %v\n", e["timestamp"], ended[p["toolId"]], p["command"])
		}
	}
	return b.String()
}

// issueBranches lists the branches of queueRepo that hold the fixture's file: those the sessions pushed for its issue.
func issueBranches(file string) ([]string, error) {
	out, err := exec.Command("gh", "api", "--paginate", "repos/"+queueRepo+"/branches?per_page=100", "--jq", ".[].name").Output()
	if err != nil {
		return nil, fmt.Errorf("list the branches of %s: %w", queueRepo, err)
	}
	var branches []string
	for _, b := range strings.Fields(string(out)) {
		cmd := exec.Command("gh", "api", "repos/"+queueRepo+"/contents/"+file+"?ref="+b, "--silent")
		err := cmd.Run()
		var exit *exec.ExitError
		switch {
		case err == nil:
			branches = append(branches, b)
		case !errors.As(err, &exit):
			return nil, fmt.Errorf("read %s on %s: %w", file, b, err)
		}
	}
	return branches, nil
}

// issueWork is what queueRepo holds for the fixture issue: the branches that hold its file, and the pull requests that
// link the issue or come from one of them.
func issueWork(t *testing.T, issue int, file string) ([]string, []restartPR) {
	t.Helper()
	branches, err := issueBranches(file)
	if err != nil {
		t.Fatal(err)
	}
	prs, err := issuePullRequests(issue, branches)
	if err != nil {
		t.Fatal(err)
	}
	return branches, prs
}

type restartPR struct {
	Number  int    `json:"number"`
	URL     string `json:"url"`
	Head    string `json:"headRefName"`
	HeadOID string `json:"headRefOid"`
	State   string `json:"state"`
	Draft   bool   `json:"isDraft"`
}

type issueRef struct {
	Number int `json:"number"`
}

// issuePullRequests lists the pull requests of queueRepo, in any state, that link the issue or come from one of the
// branches.
func issuePullRequests(issue int, branches []string) ([]restartPR, error) {
	out, err := exec.Command("gh", "pr", "list", "--repo", queueRepo, "--state", "all", "--limit", "100", "--json",
		"number,url,headRefName,headRefOid,state,isDraft,closingIssuesReferences").Output()
	if err != nil {
		return nil, fmt.Errorf("gh pr list: %w", err)
	}
	var all []struct {
		restartPR
		Closes []issueRef `json:"closingIssuesReferences"`
	}
	if err := json.Unmarshal(out, &all); err != nil {
		return nil, fmt.Errorf("gh pr list: %w", err)
	}
	var prs []restartPR
	for _, pr := range all {
		if slices.Contains(pr.Closes, issueRef{issue}) || slices.Contains(branches, pr.Head) {
			prs = append(prs, pr.restartPR)
		}
	}
	return prs, nil
}
