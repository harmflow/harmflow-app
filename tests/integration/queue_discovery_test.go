package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// discoveryLabel marks the fixture issues TestDiscoveryLimitCutsTheEligibleIssuesInOrder opens in queueRepo. The test
// owns it alone, so that its sweep never closes another test's fixtures. It opens discoveryIssues of them, more than
// discoveryLimit, the task's maxDiscoveredPerCycle.
const (
	discoveryLabel  = "queue-discovery-test"
	discoveryIssues = 8
	discoveryLimit  = 5
)

// selectionSettle is how long a session that has selected as many work items as the test expects may go on, without
// selecting another or ending, before its selection is taken as whole. The session works each item before it selects
// the next, and here an item's work ends at its push, which the clone's missing push URL refuses, well within it.
const selectionSettle = 2 * time.Minute

// The discovery limit cuts the eligible issues, in the task's order, before the session takes its work. The real
// Claude Code runs the built-in agent's copy and the built-in skills of this checkout against queueRepo, where the test
// opens eight fixture issues under discoveryLabel, oldest first, which the task's prompt makes eligible. The task reads
// them newest first, discovers at most five a cycle, and takes new work before work to retry. With two items a cycle,
// its session selects the two newest fixtures and no other issue. With eight items a cycle, more than the discovery
// limit, it selects exactly the five newest, which are the issues it considered, and none of the three oldest. Each run
// has a fresh daemon home, so that the first run's history keeps nothing out of the second. A session selects each
// item as it takes it up, so each runs until its selection is whole, and is then stopped with its daemon unless it
// ended first. Its clone has no push URL, so the push of each item it works fails, and no branch and no pull request
// of its reaches GitHub. What a session selected is read as the issues its keys name, as selectedKeys says.
func TestDiscoveryLimitCutsTheEligibleIssuesInOrder(t *testing.T) {
	claude := realClaude(t)
	fixtures := labelledFixtures(t, discoveryLabel, "Queue discovery test", discoveryIssues)
	newest := func(n int) []string {
		var keys []string
		for _, f := range fixtures[len(fixtures)-n:] {
			keys = append(keys, issueKey(f))
		}
		return keys
	}
	for _, run := range []struct {
		name  string
		items int
		// want is the work items the session must select, in any order.
		want   []string
		reason string
	}{
		{
			name:   "two items a cycle",
			items:  2,
			want:   newest(2),
			reason: "the five newest are considered, and the two newest of them taken",
		},
		{
			name:   "more items a cycle than the discovery limit",
			items:  discoveryIssues,
			want:   newest(discoveryLimit),
			reason: "only the five newest are considered, so none of the three oldest can be taken",
		},
	} {
		t.Run(run.name, func(t *testing.T) {
			clone := shallowClone(t)
			home := t.TempDir()
			d := startDaemon(t, home)
			useClaude(d, claude, clone)
			d.call("POST", "/tasks", map[string]any{"yaml": discoveryTask(d, clone, run.items)}, 201)

			id := d.call("POST", "/tasks/queue/run", nil, 202)["executionId"].(string)
			dir := executionDir(d, id)
			selected := awaitSelection(t, d, id, dir, len(run.want))
			if x := d.call("GET", "/executions/"+id, nil, 200); x["endedAt"] == nil {
				// Stopped as TestFailedIssueIsRetriedUntilItsLimit stops its sessions. Nothing follows the stop, so the
				// daemon is not started again: the selection was read from the execution's files.
				crashMidRun(t, d)
			}
			keys, garbled := selectedKeys(selected)
			if len(garbled) > 0 {
				t.Logf("progress.jsonl names work items with text after the issue number, read as the issues they name: %q",
					garbled)
			}
			if !sameWorkItems(keys, run.want) {
				t.Errorf("newest first, %d issues discovered and %d items a cycle: progress.jsonl selects %q, read as %v, "+
					"want %v alone, in any order: %s\n%s", discoveryLimit, run.items, selected, keys, run.want, run.reason,
					runFiles(dir))
			}
		})
	}
}

// discoveryTask is the task issueQueueTask makes, newest first, with its eligible-issues line taking the open issues of
// queueRepo labelled discoveryLabel, and its queue discovering at most discoveryLimit issues a cycle, taking at most
// items of them, and taking new work before work to retry.
func discoveryTask(d *daemon, clone string, items int) string {
	d.t.Helper()
	task := issueQueueTask(d, clone,
		fmt.Sprintf("open issues of %s labelled %s; every other issue is excluded.", queueRepo, discoveryLabel), "newest_first")
	for _, r := range []struct{ from, to string }{
		{"maxItemsPerCycle: 1", fmt.Sprintf("maxDiscoveredPerCycle: %d\n      maxItemsPerCycle: %d", discoveryLimit, items)},
		{"resumeStrategy: resume_first", "resumeStrategy: new_first"},
	} {
		if !strings.Contains(task, r.from) {
			d.t.Fatalf("issueQueueTask's task has no %q to replace:\n%s", r.from, task)
		}
		task = strings.Replace(task, r.from, r.to, 1)
	}
	return task
}

// labelledFixtures opens n fixture issues in queueRepo, labelled label, one after the other, and returns their numbers,
// oldest first. Each asks for one new file, unique to the run, and names the test, which is what the titles start
// with. It creates the label when it is missing. What an earlier run left open under the label, when it was stopped
// before its cleanup, would be eligible too, so it is swept first; when the test ends, failed or not, the same sweep
// closes this run's fixtures, with any pull request that links one and its branch. It returns once GitHub lists the
// fixtures under the label as the session reads them.
func labelledFixtures(t *testing.T, label, test string, n int) []int {
	t.Helper()
	base := sandboxBase(t)
	sandboxLabel(t, label)
	sweepSuiteFixtures(t, label, base)
	var own []int
	t.Cleanup(func() { sweepSuiteFixtures(t, label, base, own...) })
	run := runSuffix()
	for i := 1; i <= n; i++ {
		if i > 1 {
			// GitHub stamps an issue's creation to the second, and the queue reads the issues in that order.
			time.Sleep(time.Second)
		}
		file := fmt.Sprintf("%s/%s-%d.txt", label, run, i)
		number, err := strconv.Atoi(newWorkAPI(t, "repos/"+queueRepo+"/issues",
			"-f", fmt.Sprintf("title=%s %s: add %s", test, run, file),
			"-f", fmt.Sprintf("body=A fixture of HarmFlow's %s, which closes it when it ends.\n\n"+
				"Add the file `%s` with the single line `%s`, and change nothing else. The repository has no tests.\n",
				strings.ToLower(test), file, run),
			"-f", "labels[]="+label, "--jq", ".number"))
		if err != nil {
			t.Fatalf("open the fixture issue %d: %v", i, err)
		}
		own = append(own, number)
	}
	var listed []int
	var err error
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(time.Second) {
		if listed, err = labelledIssues(label); err == nil && slices.Equal(listed, own) {
			return own
		}
	}
	t.Fatalf("after a minute, the open issues of %s labelled %s are %v (%v), want %v", queueRepo, label, listed, err, own)
	return nil
}

// awaitSelection waits until the session's selection is whole, and returns the work items its work_item.selected lines
// name, in order. A session writes an item's line as it takes the item up, once it has worked the one before, so the
// selection is whole only once the execution has ended, once it names more than want items, or once selectionSettle
// has passed since it named want of them, each counted once, as selectedKeys reads them. A stop at want lines would
// miss a selection longer than want, the defect the discovery cut exists to show. It stops waiting a minute before the
// test binary's -timeout would stop the test, so that the fixtures' cleanup still runs, and returns what the session
// selected by then.
func awaitSelection(t *testing.T, d *daemon, id, dir string, want int) []string {
	t.Helper()
	var selected []string
	var reached time.Time
	for deadline := time.Now().Add(withinDeadline(t, 10*time.Minute)); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		ended := d.call("GET", "/executions/"+id, nil, 200)["endedAt"] != nil
		// Read once the end is known, so that no line written before it is missed.
		if _, err := os.Stat(filepath.Join(dir, "progress.jsonl")); err == nil {
			selected = selectedWorkItems(t, dir)
		}
		keys, _ := selectedKeys(selected)
		switch {
		case ended || len(keys) > want:
			return selected
		case len(keys) == want && reached.IsZero():
			reached = time.Now()
		case len(keys) == want && time.Since(reached) > selectionSettle:
			return selected
		}
	}
	return selected
}

// selectedKeys reads the work items the session selected as the distinct issues they name, in the order it first named
// each. A key that starts with queueRepo, #, and a number names that issue, whatever follows the number: a session's
// own script has written the key "harmflow/harmflow-sandbox#81 8", and then the key as it should be, so the issue's
// selection is read, and the defect of the key is #569's. Any other key, of another repository or with no number,
// stays as it is, so that it fails the comparison. garbled lists the keys read past their text.
func selectedKeys(selected []string) (keys, garbled []string) {
	for _, s := range selected {
		k := strings.ToLower(s)
		if rest, ok := strings.CutPrefix(k, strings.ToLower(queueRepo)+"#"); ok {
			digits := rest[:len(rest)-len(strings.TrimLeft(rest, "0123456789"))]
			if n, err := strconv.Atoi(digits); err == nil {
				k = issueKey(n)
				if digits != rest {
					garbled = append(garbled, s)
				}
			}
		}
		if !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	return keys, garbled
}

// sameWorkItems reports whether got and want name the same work items, in any order, each key read without regard to
// case.
func sameWorkItems(got, want []string) bool {
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
