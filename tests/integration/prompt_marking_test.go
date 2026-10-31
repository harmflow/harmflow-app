package integration

import (
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

// The fixture set of TestPromptMarkingAndBranchNamingAreFollowed in queueRepo, apart from the other sets, which it
// never reads or changes: one issue labelled promptMarkingLabel, opened by each run and closed when it ends, which asks
// for a file in the folder of the same name.
const (
	promptMarkingLabel = "prompt-marking-test"
	// autoLabel is the marking the task's prompt gives its pull requests in place of the built-in agent's harmflow. It
	// is a label apart from promptMarkingLabel, so that which issues are eligible does not depend on the marking under
	// test.
	autoLabel = "bot:auto"
)

// A GitHub task marks its pull requests and names its work branches as its prompt says, not by a convention of
// HarmFlow's own. The real Claude Code runs the built-in agent's copy, whose This task section marks pull requests with
// the label autoLabel alone and names work branches auto/<issue-number>, and the built-in skills of this checkout,
// against queueRepo, on a fixture issue the test opens, from a clone that may push. The issue then has exactly one pull
// request: a draft from auto/<n> that carries autoLabel, and not harmflow, the marking the built-in agent gives by
// default.
func TestPromptMarkingAndBranchNamingAreFollowed(t *testing.T) {
	claude := realClaude(t)
	for _, label := range []string{promptMarkingLabel, autoLabel} {
		sandboxLabel(t, label)
	}
	base := sandboxBase(t)
	sweepSuiteFixtures(t, promptMarkingLabel, base)
	run := runSuffix()
	file := promptMarkingLabel + "/" + run + ".txt"
	issue := suiteFixture(t, promptMarkingLabel, base, "Prompt marking test "+run+": add "+file, fmt.Sprintf(
		"A fixture of HarmFlow's prompt marking test, which closes it when it ends.\n\n"+
			"Add the file `%s` with the single line `%s`, and change nothing else. The repository has no tests.\n", file, run))
	clone := pushableClone(t)
	d := startDaemon(t, t.TempDir())
	useClaude(d, claude, clone)
	d.call("POST", "/tasks", map[string]any{"yaml": promptMarkingTask(d, clone, base)}, 201)

	id := d.call("POST", "/tasks/prompt-marking/run", nil, 202)["executionId"].(string)
	dir := executionDir(d, id)
	// Longer than the task's timeout, so that the execution's own end says why a session ran too long.
	x := d.waitEnd(id, withinDeadline(t, 13*time.Minute))
	if x["status"] != "completed" {
		t.Errorf("execution %s = %v (%v), want completed\n%s", id, x["status"], x["errorMessage"], runFiles(dir))
	}

	key, branch := issueKey(issue), fmt.Sprintf("auto/%d", issue)
	prs, err := issuePullRequests(issue, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 1 {
		t.Fatalf("%d pull requests of %s close #%d, want 1: %+v\n%s", len(prs), queueRepo, issue, prs, runFiles(dir))
	}
	pr := prs[0]
	if pr.Head != branch {
		t.Errorf("%s comes from the branch %s, want %s, as the task's prompt names it", pr.URL, pr.Head, branch)
	}
	if labels := pullRequestLabels(t, pr.Number); !slices.Contains(labels, autoLabel) || slices.Contains(labels, "harmflow") {
		t.Errorf("%s carries the labels %q, want %s and not harmflow: the task's prompt names %s alone", pr.URL, labels,
			autoLabel, autoLabel)
	}
	if !pr.Draft {
		t.Errorf("%s is not a draft, want a draft", pr.URL)
	}

	if got := reportedBranch(t, dir, issue); got != branch {
		t.Errorf("report.json gives %s the branch %q, want %q\n%s", key, got, branch, runFiles(dir))
	}
	if reported := reportedPullRequest(t, dir, issue); reported == nil || reported.URL != pr.URL || !reported.Draft ||
		!reported.Created {
		t.Errorf("report.json gives %s the pull request %+v, want %s, a draft this execution created\n%s", key, reported,
			pr.URL, runFiles(dir))
	}
}

// promptMarkingTask is a task made of the built-in issue implementer as selecting it copies it, with three lines of its
// This task section changed: the open issues of queueRepo labelled promptMarkingLabel are its only eligible ones, its
// work branches are named auto/<issue-number>, and its pull requests are marked with the label autoLabel alone. Its
// permissions allow what the built-in skills run to select, implement, push, and open the draft pull request, and deny
// gh pr merge: a denied tool wins over every allow rule, the signed-in Claude Code's own included.
func promptMarkingTask(d *daemon, clone, base string) string {
	d.t.Helper()
	copied := d.builtinAgentCopy("hf-github-issue-implementer")
	prompt, _ := copied["prompt"].(string)
	for _, line := range [][2]string{
		{"- **Eligible issues:** open issues labeled `agent-ready` that are not labeled `blocked`.\n",
			fmt.Sprintf("- **Eligible issues:** open issues of %s labelled %s; every other issue is excluded.\n", queueRepo,
				promptMarkingLabel)},
		{"- **Work branch:** `harmflow/<issue-number>-<short-slug>`.\n", "- **Work branch:** `auto/<issue-number>`.\n"},
		{"- **Pull request marking:** the label `harmflow`, and the line `Managed by HarmFlow task <task-id>.` in the pull\n",
			"- **Pull request marking:** the label `" + autoLabel + "`, and the line `Managed by HarmFlow task <task-id>.` in " +
				"the pull\n"},
	} {
		if !strings.Contains(prompt, line[0]) {
			d.t.Fatalf("the copy of hf-github-issue-implementer has no line %q to customize:\n%s", line[0], prompt)
		}
		prompt = strings.Replace(prompt, line[0], line[1], 1)
	}
	return fmt.Sprintf(`apiVersion: harmflow/v1alpha1
kind: Task
metadata:
  id: prompt-marking
  name: Prompt marking
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
`, queueRepo, clone, base, strings.ReplaceAll(prompt, "\n", "\n    "), copied["hash"])
}

// pullRequestLabels lists the names of the labels the pull request of queueRepo carries, as GitHub gives them.
func pullRequestLabels(t *testing.T, number int) []string {
	t.Helper()
	out, err := exec.Command("gh", "pr", "view", strconv.Itoa(number), "--repo", queueRepo, "--json", "labels",
		"--jq", "[.labels[].name]").Output()
	if err != nil {
		t.Fatalf("gh pr view %d --json labels: %v", number, err)
	}
	var labels []string
	if err := json.Unmarshal(out, &labels); err != nil {
		t.Fatalf("gh pr view %d --json labels: %v\n%s", number, err, out)
	}
	return labels
}

// reportedBranch is the branch the execution's report.json gives the issue's work item: "" when the report lists no
// such item, or gives it no branch.
func reportedBranch(t *testing.T, dir string, issue int) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		t.Errorf("report.json: %v", err)
		return ""
	}
	var report struct {
		WorkItems []struct {
			Key    string `json:"key"`
			Branch string `json:"branch"`
		} `json:"workItems"`
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Errorf("report.json: %v", err)
		return ""
	}
	for _, w := range report.WorkItems {
		if strings.EqualFold(w.Key, issueKey(issue)) {
			return w.Branch
		}
	}
	return ""
}
