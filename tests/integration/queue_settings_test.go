package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// queueEditLabel marks the fixture issues TestSessionSelectsByAnEditedQueue opens in queueRepo. The test owns it alone,
// so that its cleanup never closes another test's fixtures.
const queueEditLabel = "queue-edit-test"

// A GitHub task's queue, edited as the dashboard's editor saves it, reaches the next execution: the context.json the
// daemon gives each session holds the task's queue under task.parameters, the defaults before the edit and the edited
// values after it. An edit the task schema refuses changes neither the task file nor what the next execution gets. The
// daemon enforces none of these settings; the session reads them there. The stand-in Claude Code runs each execution.
func TestEditedQueueReachesTheNextExecution(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	d := startDaemon(t, home)
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// The repository is outside the home folder, so an allowed folder.
	d.call("PATCH", "/settings", map[string]any{"allowedFolders": []any{repo}}, 200)
	const id = "queue-edit"
	d.call("POST", "/tasks", map[string]any{"yaml": issueTask(id, repo)}, 201)
	defaults := map[string]any{"ordering": "oldest_first", "maxDiscoveredPerCycle": 10.0, "maxItemsPerCycle": 1.0,
		"resumeStrategy": "resume_first", "maxResumeAttempts": 3.0, "maxFailedRetries": 3.0}
	if got := nextQueue(t, d, id); !reflect.DeepEqual(got, defaults) {
		t.Errorf("the first execution's context.json gives task.parameters.queue = %v, want the defaults %v", got, defaults)
	}

	edited := issueTask(id, repo) + "    queue:\n      ordering: newest_first\n      maxDiscoveredPerCycle: 4\n" +
		"      maxItemsPerCycle: 2\n      resumeStrategy: new_first\n"
	want := map[string]any{"ordering": "newest_first", "maxDiscoveredPerCycle": 4.0, "maxItemsPerCycle": 2.0,
		"resumeStrategy": "new_first", "maxResumeAttempts": 3.0, "maxFailedRetries": 3.0}
	rev := d.call("GET", "/tasks/"+id, nil, 200)["revision"]
	d.call("PUT", "/tasks/"+id, map[string]any{"yaml": edited, "baseRevision": rev}, 200)
	if got := nextQueue(t, d, id); !reflect.DeepEqual(got, want) {
		t.Errorf("after the edit, the next execution's context.json gives task.parameters.queue = %v, want %v", got, want)
	}

	files := taskFiles(t, home)
	rev = d.call("GET", "/tasks/"+id, nil, 200)["revision"]
	for _, c := range []struct {
		name, from, to string
		key, path      string
	}{
		{"no item a cycle", "maxItemsPerCycle: 2", "maxItemsPerCycle: 0", "schema_minimum",
			"/spec/github/queue/maxItemsPerCycle"},
		{"an order the schema does not list", "ordering: newest_first", "ordering: random", "schema_enum",
			"/spec/github/queue/ordering"},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := d.in(t)
			invalid := strings.Replace(edited, c.from, c.to, 1)
			if key, path := d.refusal("PUT", "/tasks/"+id, map[string]any{"yaml": invalid, "baseRevision": rev}); key != c.key ||
				path != c.path {
				t.Errorf("PUT /tasks/%s with %q refused with %s at %s, want %s at %s", id, c.to, key, path, c.key, c.path)
			}
		})
	}
	if after := taskFiles(t, home); !reflect.DeepEqual(after, files) {
		t.Errorf("the refused edits changed the task files: %v, was %v", after, files)
	}
	if got := nextQueue(t, d, id); !reflect.DeepEqual(got, want) {
		t.Errorf("after the refused edits, the next execution's context.json gives task.parameters.queue = %v, want the "+
			"saved edit's %v", got, want)
	}
}

// nextQueue runs the task once and returns the queue its execution's context.json gives the session, once the
// execution has completed: a task runs one execution at a time.
func nextQueue(t *testing.T, d *daemon, taskID string) map[string]any {
	t.Helper()
	id := d.call("POST", "/tasks/"+taskID+"/run", nil, 202)["executionId"].(string)
	x := d.waitStatus(id, "completed")
	return contextQueue(t, x["executionDir"].(string))
}

// contextQueue is the queue the execution's context.json gives its session, task.parameters.queue.
func contextQueue(t *testing.T, dir string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "context.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ctx struct {
		Task struct {
			Parameters struct {
				Queue map[string]any `json:"queue"`
			} `json:"parameters"`
		} `json:"task"`
	}
	if err := json.Unmarshal(data, &ctx); err != nil {
		t.Fatalf("context.json: %v", err)
	}
	return ctx.Task.Parameters.Queue
}

// A session selects its work by the queue an edit gave its task. The real Claude Code runs the built-in agent's copy
// and the built-in skills of this checkout against queueRepo, where the test opens four fixture issues labelled
// queueEditLabel, oldest first. The task starts as queueTask makes one, oldest first and one item a cycle, and its first
// session selects the oldest fixture alone. The test then edits the task through the local API, as the dashboard's
// editor saves it, to newest first, two items a cycle, and new work before work to retry, and runs it again. That
// session selects the two newest fixtures, and no other issue: oldest first would give the second and the third, one
// item a cycle the newest alone, and work to retry first the oldest, which the first session's stop left failed. Each
// session is stopped once it has selected its issues, and its clone has no push URL, so no branch and no pull request
// of its reaches GitHub.
func TestSessionSelectsByAnEditedQueue(t *testing.T) {
	claude := realClaude(t)
	fixtures := queueEditFixtures(t)
	clone := shallowClone(t)
	home := t.TempDir()
	d := startDaemon(t, home)
	useClaude(d, claude, clone)
	task := issueQueueTask(d, clone,
		fmt.Sprintf("open issues of %s labelled %s; every other issue is excluded.", queueRepo, queueEditLabel), "oldest_first")
	d.call("POST", "/tasks", map[string]any{"yaml": task}, 201)

	d, _, selected := selectAndStop(t, d, home, claude, clone, 1, true)
	if want := []string{issueKey(fixtures[0])}; !equalKeys(selected, want) {
		t.Fatalf("before the edit, oldest first and one item a cycle, progress.jsonl selects %v, want %v", selected, want)
	}

	edited := task
	for _, r := range []struct{ from, to string }{
		{"ordering: oldest_first", "ordering: newest_first"},
		{"maxItemsPerCycle: 1", "maxItemsPerCycle: 2"},
		{"resumeStrategy: resume_first", "resumeStrategy: new_first"},
	} {
		if !strings.Contains(edited, r.from) {
			t.Fatalf("the task has no %q to edit:\n%s", r.from, edited)
		}
		edited = strings.Replace(edited, r.from, r.to, 1)
	}
	rev := d.call("GET", "/tasks/queue", nil, 200)["revision"]
	d.call("PUT", "/tasks/queue", map[string]any{"yaml": edited, "baseRevision": rev}, 200)

	// Nothing follows this session, so its daemon is not started again: what it selected is read from its files.
	_, dir, selected := selectAndStop(t, d, home, claude, clone, 2, false)
	if q := contextQueue(t, dir); q["ordering"] != "newest_first" || q["maxItemsPerCycle"] != 2.0 ||
		q["resumeStrategy"] != "new_first" {
		t.Errorf("after the edit, the execution's context.json gives task.parameters.queue = %v, want newest_first, two "+
			"items a cycle, and new_first", q)
	}
	if want := []string{issueKey(fixtures[3]), issueKey(fixtures[2])}; !equalKeys(selected, want) {
		t.Errorf("after the edit, newest first and two items a cycle, progress.jsonl selects %v, want %v alone, in any "+
			"order\n%s", selected, want, runFiles(dir))
	}
}

// queueEditFixtures opens four fixture issues in queueRepo, labelled queueEditLabel, one after the other, and returns
// their numbers, oldest first. It creates the label when it is missing. What an earlier run left open under the label,
// when it was stopped before its cleanup, would be eligible too, so it is swept first; when the test ends, failed or
// not, the same sweep closes this run's fixtures, with any pull request that links one and its branch. It returns once
// GitHub lists the fixtures under the label as the session reads them.
func queueEditFixtures(t *testing.T) []int {
	t.Helper()
	base := sandboxBase(t)
	sandboxLabel(t, queueEditLabel)
	sweepSuiteFixtures(t, queueEditLabel, base)
	var own []int
	t.Cleanup(func() { sweepSuiteFixtures(t, queueEditLabel, base, own...) })
	run := runSuffix()
	for i := 1; i <= 4; i++ {
		if i > 1 {
			// GitHub stamps an issue's creation to the second, and the queue reads the issues in that order.
			time.Sleep(time.Second)
		}
		file := fmt.Sprintf("%s/%s-%d.txt", queueEditLabel, run, i)
		n, err := strconv.Atoi(newWorkAPI(t, "repos/"+queueRepo+"/issues",
			"-f", fmt.Sprintf("title=Queue edit test %s: add %s", run, file),
			"-f", fmt.Sprintf("body=A fixture of HarmFlow's queue edit test, which closes it when it ends.\n\n"+
				"Add the file `%s` with the single line `%s`, and change nothing else. The repository has no tests.\n", file, run),
			"-f", "labels[]="+queueEditLabel, "--jq", ".number"))
		if err != nil {
			t.Fatalf("open the fixture issue %d: %v", i, err)
		}
		own = append(own, n)
	}
	var listed []int
	var err error
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(time.Second) {
		if listed, err = labelledIssues(queueEditLabel); err == nil && slices.Equal(listed, own) {
			return own
		}
	}
	t.Fatalf("after a minute, the open issues of %s labelled %s are %v (%v), want %v", queueRepo, queueEditLabel, listed,
		err, own)
	return nil
}

// selectAndStop runs the task queue once, waits until its session has selected n work items, and stops the session
// there, as TestFailedIssueIsRetriedUntilItsLimit stops its sessions: the daemon is killed, as a crash would, with the
// session's process group. With again, the daemon first tracks the last item selected, so that the stop counts against
// it, and then starts again on the same home, where the execution must read as interrupted. Without it, nothing follows
// the stop, which comes as soon as the items are selected, and the caller checks them. It returns the daemon, the
// execution's directory, and the work items its progress.jsonl selects, in order.
func selectAndStop(t *testing.T, d *daemon, home, claude, clone string, n int, again bool) (*daemon, string, []string) {
	t.Helper()
	id := d.call("POST", "/tasks/queue/run", nil, 202)["executionId"].(string)
	dir := executionDir(d, id)
	selected := awaitSelected(t, d, id, dir, n)
	if !again {
		killMidRun(t, d)
		return d, dir, selectedWorkItems(t, dir)
	}
	_, num, _ := strings.Cut(selected[len(selected)-1], "#")
	last, err := strconv.Atoi(num)
	if err != nil {
		t.Fatalf("progress.jsonl selects %q, not %s#<issue>\n%s", selected[len(selected)-1], queueRepo, runFiles(dir))
	}
	waitTracked(t, d, home, id, dir, last)
	d = restartMidRun(t, d, home, claude, clone)
	if x := d.call("GET", "/executions/"+id, nil, 200); x["status"] != "interrupted" {
		t.Errorf("execution %s after the stop at selection: status %v, want interrupted", id, x["status"])
	}
	return d, dir, selectedWorkItems(t, dir)
}

// killMidRun kills the daemon while a session runs, as a crash would, then ends the session's process group, which a
// daemon killed this way leaves running: restartMidRun's stop, without the start that follows it.
func killMidRun(t *testing.T, d *daemon) {
	t.Helper()
	out, _ := exec.Command("pgrep", "-P", strconv.Itoa(d.cmd.Process.Pid)).Output()
	var groups []int
	for _, f := range strings.Fields(string(out)) {
		pid, _ := strconv.Atoi(f)
		if pgid, err := syscall.Getpgid(pid); err == nil && pgid > 1 && pgid != syscall.Getpgrp() {
			groups = append(groups, pgid)
		}
	}
	if len(groups) == 0 {
		t.Fatal("found no session process group under the daemon")
	}
	d.kill()
	for _, g := range groups {
		_ = syscall.Kill(-g, syscall.SIGKILL)
	}
}

// awaitSelected waits until the session's progress.jsonl selects n work items, and returns them in order. It fails the
// test when the execution ends first, and stops waiting a minute before the test binary's -timeout would stop the
// test, so that the fixtures' cleanup still runs.
func awaitSelected(t *testing.T, d *daemon, id, dir string, n int) []string {
	t.Helper()
	var selected []string
	for deadline := time.Now().Add(withinDeadline(t, 10*time.Minute)); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(dir, "progress.jsonl")); err == nil {
			if selected = selectedWorkItems(t, dir); len(selected) >= n {
				return selected
			}
		}
		if x := d.call("GET", "/executions/"+id, nil, 200); x["endedAt"] != nil {
			t.Fatalf("execution %s ended %v (%v) having selected %v, want %d work items\n%s", id, x["status"],
				x["errorMessage"], selected, n, runFiles(dir))
		}
	}
	t.Fatalf("execution %s selected %v in the time it had, want %d work items\n%s", id, selected, n, runFiles(dir))
	return nil
}

// equalKeys reports whether got and want hold the same work items, in any order, each key read without regard to case.
func equalKeys(got, want []string) bool {
	lower := func(keys []string) []string {
		out := make([]string, len(keys))
		for i, k := range keys {
			out[i] = strings.ToLower(k)
		}
		slices.Sort(out)
		return out
	}
	return slices.Equal(lower(got), lower(want))
}
