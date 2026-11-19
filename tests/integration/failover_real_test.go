package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The fixture set of TestFailoverFromRealClaudeCodeToRealCodex in queueRepo, apart from the other sets, which it never
// reads or changes: one issue labelled failoverRealLabel, opened by each run and closed when it ends. The file the
// fixture asks for goes in failoverRealDir, which the base branch never holds: a branch that holds it is one a session
// pushed for a fixture.
const (
	failoverRealLabel = "failover-real-test"
	failoverRealDir   = "failover-real-test"
	// quotaReplayNote is the line the wrapper of the real Claude Code prints in its stream, which the execution's log
	// keeps, so that the run's record states how its quota was triggered.
	quotaReplayNote = "HarmFlow test: the real Claude Code session was stopped once it had selected its work item, and a " +
		"recorded Claude Code quota ending was replayed to its adapter."
)

// A GitHub issue implementation that starts on the real Claude Code fails over to the real Codex on a quota signal and
// ends with a single draft pull request. The real Claude Code runs the built-in agent's copy against queueRepo, on a
// fixture issue the test opens, from a clone that may push, through a wrapper the test writes: a real session runs
// until it has selected its work item and the daemon has logged the selection, and the wrapper then stops it and prints
// the quota ending Claude Code prints when its usage limit is reached, a rate limit event the CLI rejected and a result
// line in error, which the daemon's adapter classifies as an exhausted quota. No account's quota is spent to the end.
// The wrapper prints quotaReplayNote first, so the execution's log says how the quota was triggered.
//
// The daemon then starts the real Codex as the task's fallback, in the same execution: its attempt works in the first
// one's workspace and on its work item, which the context.json it gets names as the previous attempt's, and it opens
// the draft. The execution ends completed with two attempts, the first claude-code with its quota exhausted and the
// second codex that succeeded; the log shows the switch; the issue has exactly one pull request, an open draft from a
// work branch into the base branch; and the base branch is where it was.
func TestFailoverFromRealClaudeCodeToRealCodex(t *testing.T) {
	claude := realClaude(t)
	codex := realCodex(t)
	base := sandboxBase(t)
	for _, label := range []string{failoverRealLabel, "harmflow"} {
		sandboxLabel(t, label)
	}
	run := runSuffix()
	file := failoverRealDir + "/" + run + ".txt"
	issue := labelledFixture(t, failoverRealLabel, failoverRealDir, base, "Add the release note "+file,
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
	wrapper := quotaAfterSelection(t, claude)

	home := t.TempDir()
	d := startDaemon(t, home, "CODEX_HOME="+codexHome(t))
	useClaude(d, wrapper, clone)
	d.call("PUT", "/harnesses/codex", map[string]any{"path": codex}, 200)
	d.call("POST", "/tasks", map[string]any{"yaml": failoverRealTask(d, clone, base)}, 201)

	id := d.call("POST", "/tasks/failover-real/run", nil, 202)["executionId"].(string)
	dir := executionDir(d, id)
	// Longer than the task's timeout, so that the execution's own end says why a session ran too long.
	x := d.waitEnd(id, withinDeadline(t, 22*time.Minute))
	if x["status"] != "completed" {
		t.Errorf("execution %s = %v (%v), want completed\n%s", id, x["status"], x["errorMessage"], runFiles(dir))
	}
	attempts, _ := x["attempts"].([]any)
	if len(attempts) != 2 {
		t.Fatalf("execution %s has %d attempts, want 2: %v\n%s", id, len(attempts), attempts, runFiles(dir))
	}
	a1, a2 := attempts[0].(map[string]any), attempts[1].(map[string]any)
	if a1["harness"] != "claude-code" || a1["outcome"] != "quota_exhausted" || a2["harness"] != "codex" ||
		a2["outcome"] != "succeeded" {
		t.Errorf("attempts %v and %v, want claude-code quota_exhausted, then codex succeeded", a1, a2)
	}
	t.Logf("harnesses: claude-code %v, codex %v", a1["harnessVersion"], a2["harnessVersion"])

	key := issueKey(issue)
	var order, created []string
	var noted bool
	for _, e := range d.events(id) {
		typ, _ := e["type"].(string)
		switch typ {
		case "harness.started", "harness.quota_exhausted", "harness.switched":
			order = append(order, typ)
		case "pull_request.created":
			payload, _ := e["payload"].(map[string]any)
			url, _ := payload["url"].(string)
			if item, _ := e["workItemId"].(string); !strings.EqualFold(item, key) || url == "" {
				t.Errorf("pull_request.created is for %q with the URL %q, want %s with its pull request's URL", item, url, key)
			}
			created = append(created, url)
		}
		if data, err := json.Marshal(e); err == nil && strings.Contains(string(data), quotaReplayNote) {
			noted = true
		}
	}
	if strings.Join(order, ",") != "harness.started,harness.quota_exhausted,harness.switched,harness.started" {
		t.Errorf("the execution's harness events are %v, want the start, the quota, the switch, and the second start", order)
	}
	if !noted {
		t.Errorf("the execution's log does not hold the line saying how the quota was triggered: %q", quotaReplayNote)
	}
	if len(created) != 1 {
		t.Fatalf("execution %s logged %d pull_request.created events, want 1: %v\n%s", id, len(created), created, runFiles(dir))
	}

	// The second attempt works in the first one's workspace, and on its work item, which the context.json it got names
	// as the first attempt's.
	data, err := os.ReadFile(filepath.Join(dir, "context.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ctx struct {
		Execution struct {
			Attempt int `json:"attempt"`
		} `json:"execution"`
		Paths struct {
			Workspace string `json:"workspace"`
		} `json:"paths"`
		PreviousAttempts []struct {
			Number    int `json:"number"`
			WorkItems []struct {
				Key string `json:"key"`
			} `json:"workItems"`
		} `json:"previousAttempts"`
	}
	if err := json.Unmarshal(data, &ctx); err != nil {
		t.Fatal(err)
	}
	prev := ctx.PreviousAttempts
	if ctx.Execution.Attempt != 2 || ctx.Paths.Workspace == "" || x["workspacePath"] != ctx.Paths.Workspace ||
		len(prev) != 1 || prev[0].Number != 1 || len(prev[0].WorkItems) != 1 ||
		!strings.EqualFold(prev[0].WorkItems[0].Key, key) || !strings.EqualFold(fmt.Sprint(x["workItem"]), key) {
		t.Errorf("the second attempt's context.json does not give it the first one's workspace %v and work item %s "+
			"(the execution's is %v):\n%s", x["workspacePath"], key, x["workItem"], data)
	}

	prs, err := linkingPullRequests(issue)
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 1 {
		t.Fatalf("%d pull requests of %s link #%d, want 1: %+v\n%s", len(prs), queueRepo, issue, prs, runFiles(dir))
	}
	pr := prs[0]
	if pr.URL != created[0] {
		t.Errorf("the pull request linking #%d is %s, want %s, the execution's", issue, pr.URL, created[0])
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
}

// failoverRealTask is the task labelledIssuesTask makes on the fixture's label, with Codex as its fallback, at the
// effort low and the model its configuration sets, and a timeout that fits two sessions.
func failoverRealTask(d *daemon, clone, base string) string {
	d.t.Helper()
	doc := labelledIssuesTask(d, "failover-real", failoverRealLabel, clone, base)
	const preferred = "      effort: low\n  permissions:\n"
	const timeout = "    timeoutMinutes: 12\n"
	if !strings.Contains(doc, preferred) || !strings.Contains(doc, timeout) {
		d.t.Fatalf("the task of labelledIssuesTask has no lines %q and %q to customize:\n%s", preferred, timeout, doc)
	}
	doc = strings.Replace(doc, preferred,
		"      effort: low\n    fallback:\n      candidates:\n        - provider: codex\n          effort: low\n  permissions:\n", 1)
	return strings.Replace(doc, timeout, "    timeoutMinutes: 20\n", 1)
}

// quotaAfterSelection writes a wrapper of the real Claude Code at claude, under the CLI's name, and returns its path.
// Every invocation but a session's, which the daemon starts with a permission mode, runs the real CLI as it is. A
// session runs the real CLI too, its output passed through line by line, until the session has written its
// work_item.selected step to progress.jsonl and the daemon has logged it; the wrapper then stops the real CLI, prints
// quotaReplayNote, and prints the quota ending fake-claude prints for SCENARIO=quota, the lines Claude Code prints when
// its usage limit is reached, and exits 1. A session that ends before it selects ends as the real CLI did, and the test
// then finds no failover.
func quotaAfterSelection(t *testing.T, claude string) string {
	t.Helper()
	dir := t.TempDir()
	note, err := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{
		"content": []any{map[string]any{"type": "text", "text": quotaReplayNote}}}})
	if err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/bin/bash
REAL=%q
case " $* " in
  *" --permission-mode "*) ;;
  *) exec "$REAL" "$@" ;;
esac
PROGRESS="$HARMFLOW_EXECUTION_DIR/progress.jsonl"
# The daemon keeps each execution's log in the logs directory beside the runs directory that holds the execution's.
RUNS=${HARMFLOW_EXECUTION_DIR%%/*}
LOG="${RUNS%%/*}/logs/$HARMFLOW_EXECUTION_ID.jsonl"
OUT="$HARMFLOW_EXECUTION_DIR/real-claude.fifo"
rm -f "$OUT" && mkfifo "$OUT" || exit 1
# A background command of a shell without job control reads /dev/null unless its input is given: the prompt is on
# this script's input.
exec 3<&0
"$REAL" "$@" <&3 >"$OUT" &
PID=$!
exec 3<&-
SELECTED=0
while IFS= read -r LINE; do
  printf '%%s\n' "$LINE"
  if grep -qF '"type":"work_item.selected"' "$PROGRESS" 2>/dev/null; then
    SELECTED=1
    break
  fi
done <"$OUT"
if [ "$SELECTED" = 0 ]; then
  wait "$PID"
  exit $?
fi
for _ in $(seq 1200); do
  grep -qF '"type":"work_item.selected"' "$LOG" 2>/dev/null && break
  sleep 0.05
done
kill -TERM "$PID" 2>/dev/null
wait "$PID" 2>/dev/null
rm -f "$OUT"
printf '%%s\n' %q
echo '{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","rateLimitType":"five_hour","resetsAt":1}}'
echo '{"type":"result","subtype":"error_during_execution","is_error":true,"result":"Claude AI usage limit reached"}'
exit 1
`, claude, string(note))
	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
