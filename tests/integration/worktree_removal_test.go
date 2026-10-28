package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// endedRepo is the public repository whose merged pull request gives the item that ended: its own history, which only
// grows, so the test reads it and writes nothing there.
const endedRepo = "harmflow/harmflow-app"

// An execution that finds no eligible work still removes the worktree of a work item that ended, and reports the item
// closed, so that Clean workspaces agrees. Of the specification's three worktrees, the merged item's goes, while the
// draft's still in progress and the failed item's stay. The real Claude Code runs the built-in agent's copy and the
// built-in skills of this checkout, on the queue's fixtures and on a merged pull request of endedRepo.
func TestNoWorkExecutionRemovesEndedWorktrees(t *testing.T) {
	claude := realClaude(t)
	linked, failing, _ := queueFixtures(t)
	draftBranch := draftOf(t, linked)
	merged, mergedBranch := mergedItem(t)
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sandbox, app := shallowCloneIn(t, parent, queueRepo), shallowCloneIn(t, parent, endedRepo)
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace", "queue")
	// The ledger's items, as earlier executions left them: the session reads them in context.json.
	items := []struct {
		repo, clone, branch, status string
		issue, failures             int
		kept                        bool
	}{
		{endedRepo, app, mergedBranch, "completed", merged, 0, false},
		{queueRepo, sandbox, draftBranch, "completed", linked, 0, true},
		{queueRepo, sandbox, fmt.Sprintf("fix/%d-retry", failing), "failed", failing, 1, true},
	}
	ledger := map[string]map[string]any{}
	for _, it := range items {
		wt := filepath.Join(workspace, fmt.Sprintf("%s-%d", filepath.Base(it.repo), it.issue))
		if out, err := exec.Command("git", "-C", it.clone, "worktree", "add", "--quiet", "-b", it.branch, wt).CombinedOutput(); err != nil {
			t.Fatalf("git worktree add: %v\n%s", err, out)
		}
		key := fmt.Sprintf("%s#%d", it.repo, it.issue)
		ledger[key] = map[string]any{"taskId": "queue", "key": key, "repository": it.repo, "status": it.status,
			"resumes": 0, "failures": it.failures, "branch": it.branch,
			"lastExecutionId": "exec-earlier", "updatedAt": time.Now().UTC().Format(time.RFC3339)}
	}
	seedLedger(t, home, ledger)

	d := startDaemon(t, home)
	useClaude(d, claude, sandbox)
	d.call("POST", "/tasks", map[string]any{"yaml": endedTask(d, sandbox, app)}, 201)
	id := d.call("POST", "/tasks/queue/run", nil, 202)["executionId"].(string)
	dir := executionDir(d, id)
	if x := d.waitEnd(id, 14*time.Minute); x["status"] != "no_work" {
		t.Fatalf("execution %s ended %v, want no_work: %v\n%s", id, x["status"], x["errorMessage"], runFiles(dir))
	}

	got := readLedger(t, home)
	clean := d.call("POST", "/workspaces/clean", nil, 200)
	removed, _ := clean["removed"].([]any)
	for i, it := range items {
		wt := filepath.Join(workspace, fmt.Sprintf("%s-%d", filepath.Base(it.repo), it.issue))
		_, statErr := os.Stat(wt)
		if onDisk := statErr == nil; onDisk != it.kept {
			t.Errorf("item %d, %s#%d (%s): its worktree is on disk = %v, want %v\n%s", i+1, it.repo, it.issue, it.status,
				onDisk, it.kept, runFiles(dir))
		}
		want := it.status
		if !it.kept {
			want = "closed"
		}
		if w := got[fmt.Sprintf("%s#%d", it.repo, it.issue)]; w.Status != want {
			t.Errorf("the ledger's %s#%d = %+v, want %s", it.repo, it.issue, w, want)
		}
		if it.kept && slices.Contains(removed, any(wt)) {
			t.Errorf("Clean workspaces removed %s, which the session kept: %v", wt, clean)
		}
	}
}

// draftOf is the branch of the open draft pull request that links issue n of queueRepo.
func draftOf(t *testing.T, n int) string {
	t.Helper()
	out, err := exec.Command("gh", "pr", "list", "--repo", queueRepo, "--state", "open", "--draft", "--json",
		"headRefName,closingIssuesReferences", "--jq", fmt.Sprintf(
			`[.[] | select(any(.closingIssuesReferences[]; .number == %d)) | .headRefName] | first // ""`, n)).Output()
	if err != nil {
		t.Fatalf("gh pr list: %v", err)
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" {
		t.Fatalf("%s has no open draft pull request linking #%d", queueRepo, n)
	}
	return branch
}

// mergedItem is an issue of endedRepo that a merged pull request closed, and that pull request's branch.
func mergedItem(t *testing.T) (int, string) {
	t.Helper()
	out, err := exec.Command("gh", "pr", "list", "--repo", endedRepo, "--state", "merged", "--limit", "20", "--json",
		"headRefName,closingIssuesReferences").Output()
	if err != nil {
		t.Fatalf("gh pr list: %v", err)
	}
	var prs []struct {
		Branch string `json:"headRefName"`
		Closes []struct {
			Number int `json:"number"`
		} `json:"closingIssuesReferences"`
	}
	if err := json.Unmarshal(out, &prs); err != nil {
		t.Fatal(err)
	}
	for _, pr := range prs {
		if len(pr.Closes) == 1 {
			return pr.Closes[0].Number, pr.Branch
		}
	}
	t.Fatalf("none of the last 20 merged pull requests of %s closes one issue", endedRepo)
	return 0, ""
}

// seedLedger writes the daemon's ledger of the task queue before the daemon starts, as its earlier executions would
// have left it.
func seedLedger(t *testing.T, home string, items map[string]map[string]any) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"schemaVersion": "1", "tasks": map[string]any{"queue": items}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "state", "workitems.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// endedTask is a task made of the built-in issue implementer as selecting it copies it, on queueRepo and endedRepo,
// with no issue eligible, so that its execution finds no work.
func endedTask(d *daemon, sandbox, app string) string {
	d.t.Helper()
	copied := d.builtinAgentCopy("hf-github-issue-implementer")
	const eligible = "- **Eligible issues:** open issues labeled `agent-ready` that are not labeled `blocked`.\n"
	prompt, _ := copied["prompt"].(string)
	if !strings.Contains(prompt, eligible) {
		d.t.Fatalf("the copy of hf-github-issue-implementer has no line %q to customize:\n%s", eligible, prompt)
	}
	prompt = strings.Replace(prompt, eligible, "- **Eligible issues:** none; every issue is excluded.\n", 1)
	return fmt.Sprintf(`apiVersion: harmflow/v1alpha1
kind: Task
metadata:
  id: queue
  name: Ended worktrees
spec:
  type: github
  prompt: |
    %[5]s
  origin:
    agent: hf-github-issue-implementer
    hash: %[6]s
  skills: [hf-github-issue-queue, hf-github-worktree, hf-github-draft-pr]
  harness:
    preferred:
      provider: claude-code
      model: haiku
      effort: low
  permissions:
    profile: custom
    network: allow
    custom:
      # Enough to build the queue, remove a worktree, and report. gh api can also write, which nothing here asks for;
      # a push fails on the clones' missing push URL.
      allowedTools: [Read, Glob, Grep, Write, Edit, Skill, TodoWrite, "Bash(echo:*)", "Bash(cat:*)", "Bash(jq:*)",
        "Bash(ls:*)", "Bash(date:*)", "Bash(git --version)", "Bash(git -C:*)", "Bash(gh auth status:*)",
        "Bash(gh issue list:*)", "Bash(gh issue view:*)", "Bash(gh pr list:*)", "Bash(gh pr view:*)", "Bash(gh api:*)"]
  execution:
    timeoutMinutes: 15
  github:
    repositories:
      - name: %[1]s
        localPath: %[2]s
      - name: %[3]s
        localPath: %[4]s
    queue:
      ordering: oldest_first
      maxItemsPerCycle: 1
      resumeStrategy: resume_first
      maxFailedRetries: 1
`, queueRepo, sandbox, endedRepo, app, strings.ReplaceAll(prompt, "\n", "\n    "), copied["hash"])
}
