package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// baseCheckLabel marks the fixture issue TestUnprotectedBaseBranchIsReportedInTheSkillsWords opens in queueRepo, apart
// from the other fixture sets, which it never reads or changes. The file the fixture asks for goes in baseCheckDir,
// which the base branch never holds; the session is stopped before it writes it.
const (
	baseCheckLabel = "base-branch-check-test"
	baseCheckDir   = "base-branch-check-test"
)

// Before it works on a repository, a GitHub task checks whether the base branch is protected against merging without
// review (hf-github-worktree §1); when it is not, the session reports the skill's own message, filled with the branch's
// name alone, and goes on. The real Claude Code runs the built-in agent's copy and the built-in skills of this checkout
// against queueRepo, whose base branch is not protected, on a fixture issue the test opens, and is stopped once it has
// prepared its worktree. Before that, its log holds exactly one repository.unprotected event, a warning whose message
// is the skill's, verbatim, and its record one repository_unprotected warning with that message, about queueRepo. A
// session that words the message itself, adds to it, reports it twice, or stops after it fails here.
//
// The protected answer is not run with a real session: TestBaseBranchProtection in tests/skills covers it against a
// stub of the API.
func TestUnprotectedBaseBranchIsReportedInTheSkillsWords(t *testing.T) {
	claude := realClaude(t)
	base := sandboxBase(t)
	section := protectionSection(t)
	// queueRepo is private, on a plan that cannot protect a private branch: both of the check's calls answer 403, which
	// the skill reads as none. Without the plan's limit, the branch has no protection and no ruleset, which reads as none
	// too. Another answer is a change to the sandbox or its plan, not a session's mistake, so it fails as a fixture error
	// before any session starts.
	if got := protectionAnswer(t, section, base); got != "none" {
		t.Fatalf("the check's calls on %s's %s answer %s, want none: the sandbox's protection or its plan changed, and "+
			"this test needs a base branch not protected against merging without review", queueRepo, base, got)
	}
	want := strings.ReplaceAll(protectionMessages(t, section)["none"], "BASE", base)
	if want == "" {
		t.Fatalf("the table of hf-github-worktree §1 gives the answer none no message")
	}

	sandboxLabel(t, baseCheckLabel)
	run := runSuffix()
	file := baseCheckDir + "/" + run + ".txt"
	issue := labelledFixture(t, baseCheckLabel, baseCheckDir, base, "Add the note "+file,
		fmt.Sprintf("Add the file `%s` with the single line `%s`, and change nothing else. The repository has no tests.\n",
			file, run))
	clone := shallowClone(t)
	home := t.TempDir()
	// Registered after the daemon's home, so it runs before that home is removed, and before the daemon starts, so it
	// runs after the daemon stops.
	t.Cleanup(func() { removeFixtureWorktrees(t, clone, issue) })
	d := startDaemon(t, home)
	useClaude(d, claude, clone)
	d.call("POST", "/tasks", map[string]any{"yaml": baseCheckTask(d, clone)}, 201)

	id := d.call("POST", "/tasks/queue/run", nil, 202)["executionId"].(string)
	dir := executionDir(d, id)
	waitWorkspaceChanged(t, d, id, dir, issue, withinDeadline(t, 12*time.Minute))
	d.call("POST", "/executions/"+id+"/cancel", nil, 202)
	d.waitEnd(id, 2*time.Minute)

	events := d.events(id)
	// The wait saw it, and the log keeps it.
	changed := slices.IndexFunc(events, func(e map[string]any) bool { return workspaceChangedFor(e, issue) })
	if changed < 0 {
		t.Fatalf("the log of %s no longer holds workspace.changed for %s\n%s", id, issueKey(issue), runFiles(dir))
	}
	var before, after []map[string]any
	for i, e := range events {
		if e["type"] != "repository.unprotected" {
			continue
		}
		if i < changed {
			before = append(before, e)
		} else {
			after = append(after, e)
		}
	}
	if len(before) != 1 {
		t.Fatalf("before workspace.changed for %s, execution %s logged %d repository.unprotected events, want 1: %v\n%s",
			issueKey(issue), id, len(before), before, runFiles(dir))
	}
	if e := before[0]; e["severity"] != "warning" || e["message"] != want {
		t.Errorf("repository.unprotected is %v with the message %q, want warning with the skill's message %q", e["severity"],
			e["message"], want)
	}
	if len(after) != 0 {
		t.Errorf("execution %s logged repository.unprotected again after workspace.changed: %v", id, after)
	}

	x := d.call("GET", "/executions/"+id, nil, 200)
	var warnings []map[string]any
	list, _ := x["warnings"].([]any)
	for _, w := range list {
		if w, _ := w.(map[string]any); w["code"] == "repository_unprotected" {
			warnings = append(warnings, w)
		}
	}
	if len(warnings) != 1 {
		t.Fatalf("the record of %s holds %d repository_unprotected warnings, want 1: %v", id, len(warnings), list)
	}
	if subject, _ := warnings[0]["subject"].(string); warnings[0]["message"] != want || !strings.EqualFold(subject, queueRepo) {
		t.Errorf("the record's repository_unprotected warning says %q about %q, want %q about %s", warnings[0]["message"],
			subject, want, queueRepo)
	}
}

// workspaceChangedFor reports whether the event is a workspace.changed for the issue's work item.
func workspaceChangedFor(e map[string]any, issue int) bool {
	item, _ := e["workItemId"].(string)
	return e["type"] == "workspace.changed" && strings.EqualFold(item, issueKey(issue))
}

// waitWorkspaceChanged waits until the execution's log holds workspace.changed for the issue: the session has prepared
// its worktree. An execution that ends first fails the test, with the repository.unprotected events it logged.
func waitWorkspaceChanged(t *testing.T, d *daemon, id, dir string, issue int, timeout time.Duration) {
	t.Helper()
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(time.Second) {
		events := d.events(id)
		if slices.ContainsFunc(events, func(e map[string]any) bool { return workspaceChangedFor(e, issue) }) {
			return
		}
		if x := d.call("GET", "/executions/"+id, nil, 200); x["endedAt"] != nil {
			var unprotected []map[string]any
			for _, e := range events {
				if e["type"] == "repository.unprotected" {
					unprotected = append(unprotected, e)
				}
			}
			t.Fatalf("execution %s ended %v (%v) before it prepared its worktree for %s; its repository.unprotected "+
				"events: %v\n%s", id, x["status"], x["errorMessage"], issueKey(issue), unprotected, runFiles(dir))
		}
	}
	t.Fatalf("execution %s logged no workspace.changed for %s in %s\n%s", id, issueKey(issue), timeout, runFiles(dir))
}

// baseCheckTask is the task queueTask builds, with the open issues of queueRepo labelled baseCheckLabel as its only
// eligible ones, and what hf-github-worktree §2 runs to prepare a worktree added to its permissions: cd, and git
// worktree. git push, gh pr create, and gh pr merge are denied, and a push would fail anyway on the clone's missing
// push URL. gh api is allowed whole, as queueTask allows it, since the queue skill reads issues through it, so it can
// still write: a pull request, a merge, or a ref through the REST or GraphQL API. Only the early stop at
// workspace.changed guards against that.
func baseCheckTask(d *daemon, clone string) string {
	d.t.Helper()
	task := issueQueueTask(d, clone,
		fmt.Sprintf("open issues of %s labelled %s; every other issue is excluded.", queueRepo, baseCheckLabel), "oldest_first")
	const tools = `"Bash(gh api:*)"]` + "\n"
	if !strings.Contains(task, tools) {
		d.t.Fatalf("issueQueueTask's task has no %q to add the worktree's commands to:\n%s", tools, task)
	}
	return strings.Replace(task, tools, `"Bash(gh api:*)", "Bash(ls:*)", "Bash(cd:*)", "Bash(git worktree:*)",
        "Bash(git rev-parse:*)", "Bash(git branch:*)", "Bash(git ls-remote:*)", "Bash(git status:*)"]
      deniedTools: ["Bash(git push:*)", "Bash(gh pr create:*)", "Bash(gh pr merge:*)"]
`, 1)
}

// removeFixtureWorktrees removes the clone's worktrees of the fixture issue: those whose branch's last segment starts
// with the issue's number, as hf-github-worktree §2 names it, or whose folder is named for the issue, as it places it.
func removeFixtureWorktrees(t *testing.T, clone string, issue int) {
	t.Helper()
	n := strconv.Itoa(issue)
	own := regexp.MustCompile(`^` + n + `(-|$)`)
	for _, w := range worktrees(t, clone)[1:] {
		if !own.MatchString(filepath.Base(w.Branch)) && filepath.Base(w.Path) != filepath.Base(queueRepo)+"-"+n {
			continue
		}
		if out, err := exec.Command("git", "-C", clone, "worktree", "remove", "--force", w.Path).CombinedOutput(); err != nil {
			t.Errorf("cleanup: git worktree remove %s: %v\n%s", w.Path, err, out)
		}
	}
	if out, err := exec.Command("git", "-C", clone, "worktree", "prune").CombinedOutput(); err != nil {
		t.Errorf("cleanup: git worktree prune: %v\n%s", err, out)
	}
}

// protectionStrength orders the check's answers: the stronger of its two calls' answers decides.
var protectionStrength = map[string]int{"none": 0, "pull_request": 1, "review": 2}

// protectionSection is §1 of the built-in hf-github-worktree of this checkout, up to the next section.
func protectionSection(t *testing.T) string {
	t.Helper()
	path := filepath.Join(repoRoot(), "skills", "hf-github-worktree", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const heading = "## 1. Check the base branch's protection\n"
	_, section, ok := strings.Cut(string(data), heading)
	if !ok {
		t.Fatalf("%s has no heading %q", path, strings.TrimSpace(heading))
	}
	section, _, _ = strings.Cut(section, "\n## ")
	return section
}

// protectionAnswer runs the section's two gh api commands, as the skill writes them, on queueRepo and its base branch,
// with the user's gh, and returns the stronger of their answers: what a command printed, or none when it failed, as the
// skill reads a failed call.
func protectionAnswer(t *testing.T, section, base string) string {
	t.Helper()
	_, block, ok := strings.Cut(section, "```bash\n")
	if !ok {
		t.Fatal("§1 of hf-github-worktree has no bash block")
	}
	block, _, _ = strings.Cut(block, "```")
	strongest, calls := "none", 0
	for _, line := range strings.Split(block, "\n") {
		if !strings.HasPrefix(line, "gh api ") {
			continue
		}
		calls++
		// The commands quote their arguments, and none holds a ;, |, or & outside its quotes, which shellWords would split.
		args := shellWords(line)[1:]
		args[1] = strings.NewReplacer("OWNER/REPO", queueRepo, "BASE", base).Replace(args[1])
		out, err := exec.Command("gh", args...).Output()
		got := strings.TrimSpace(string(out))
		if err != nil {
			got = "none"
		}
		if _, ok := protectionStrength[got]; !ok {
			t.Fatalf("gh %s printed %q, want review, pull_request, or none", strings.Join(args, " "), got)
		}
		if protectionStrength[got] > protectionStrength[strongest] {
			strongest = got
		}
	}
	if calls != 2 {
		t.Fatalf("§1 of hf-github-worktree has %d gh api commands, want 2: classic protection and the rules", calls)
	}
	return strongest
}

var protectionRow = regexp.MustCompile("^\\| `([a-z_]+)` \\| (.*) \\|$")

// protectionMessages are the messages of the section's table by answer, empty for an answer that reports nothing: a
// cell that is not one quoted message.
func protectionMessages(t *testing.T, section string) map[string]string {
	t.Helper()
	messages := map[string]string{}
	for _, line := range strings.Split(section, "\n") {
		m := protectionRow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if cell := m[2]; strings.HasPrefix(cell, "`") && strings.HasSuffix(cell, "`") && strings.Count(cell, "`") == 2 {
			messages[m[1]] = strings.Trim(cell, "`")
		} else {
			messages[m[1]] = ""
		}
	}
	for answer := range protectionStrength {
		if _, ok := messages[answer]; !ok {
			t.Fatalf("the table of §1 of hf-github-worktree has no row for %s", answer)
		}
	}
	return messages
}
