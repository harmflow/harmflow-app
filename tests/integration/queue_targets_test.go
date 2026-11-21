package integration

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The repositories of TestDiscoveryTakesIssuesOfTheTaskTargetsOnly. The task targets queueRepo and targetsSecond, and
// targetsOther holds an issue the task's prompt would make eligible, though the task does not target it. No repository
// is created for the test: targetsSecond and targetsOther are existing ones, where the test keeps no fixture open once
// it ends. targetsLabel marks the fixture issues it opens in all three. The test owns it alone, so that its sweep never
// closes another test's fixtures. targetsOther is public, so the label, the titles, and the bodies read as ordinary
// work, and the label carries no description. targetsLimit is the discovery limit of the second run.
const (
	targetsSecond = "harmflow/harmflow-full"
	targetsOther  = "harmflow/harmflow-site"
	targetsLabel  = "note-files"
	targetsLimit  = 2
)

// A task with two repositories discovers work in its targets only, and its discovery limit is one for the cycle, across
// both. The real Claude Code runs the built-in agent's copy and the built-in skills of this checkout, with queueRepo
// and targetsSecond as the task's repositories. The test opens five fixture issues under targetsLabel, one after the
// other, each assigned to the account gh is signed in with, so that the owner's scheduled queues, which skip what is
// assigned to the owner, leave them alone: first one in targetsOther, then one in queueRepo, one in targetsSecond, a
// second in queueRepo, and a second in targetsSecond. The task's prompt makes every open issue labelled targetsLabel
// eligible, in any repository, and the task reads them oldest first.
//
// With two items a cycle, its session must select the oldest issue of each target. The issue of targetsOther is older
// than both, so a session that read that repository would take it first; one that read one target after the other
// would take that target's two. With four items a cycle and at most two issues discovered, the session must select the
// same two, the first two of one list across both targets: a limit counted per repository would let all four through,
// and one repository read after the other would give queueRepo's two. In both runs the daemon must track no work item
// of targetsOther.
//
// Each run has a fresh daemon home, so that the first run's history keeps nothing out of the second, and runs until
// its selection is whole, as TestDiscoveryLimitCutsTheEligibleIssuesInOrder's does; it is then stopped with its daemon
// unless it ended first. Its clones have no push URL, so no branch and no pull request of its reaches GitHub.
func TestDiscoveryTakesIssuesOfTheTaskTargetsOnly(t *testing.T) {
	claude := realClaude(t)
	f := targetsFixtures(t)
	want := []string{targetKey(queueRepo, f.first[0]), targetKey(targetsSecond, f.second[0])}
	for _, run := range []struct {
		name string
		// discovered is the task's maxDiscoveredPerCycle, or 0 for the default.
		discovered int
		items      int
		reason     string
	}{
		{
			name:   "two items a cycle",
			items:  2,
			reason: "the oldest issue of each target, and not the older issue of a repository the task does not target",
		},
		{
			name:       "one discovery limit across both targets",
			discovered: targetsLimit,
			items:      4,
			reason: fmt.Sprintf("only the first %d of one list across both targets are discovered, so no other issue can "+
				"be taken", targetsLimit),
		},
	} {
		t.Run(run.name, func(t *testing.T) {
			parent, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			first := shallowCloneIn(t, parent, queueRepo)
			second := shallowCloneIn(t, parent, targetsSecond)
			home := t.TempDir()
			d := startDaemon(t, home)
			useClaude(d, claude, first)
			d.call("POST", "/tasks", map[string]any{"yaml": targetsTask(d, first, second, run.discovered, run.items)}, 201)

			id := d.call("POST", "/tasks/queue/run", nil, 202)["executionId"].(string)
			dir := executionDir(d, id)
			selected := awaitSelection(t, d, id, dir, len(want))
			items, _ := d.call("GET", "/queue", nil, 200)["workItems"].([]any)
			if x := d.call("GET", "/executions/"+id, nil, 200); x["endedAt"] == nil {
				// Stopped as TestFailedIssueIsRetriedUntilItsLimit stops its sessions. Nothing follows the stop, so the
				// daemon is not started again.
				crashMidRun(t, d)
			}

			keys, garbled := selectedKeys(selected)
			if len(garbled) > 0 {
				t.Logf("progress.jsonl names work items with text after the issue number, read as the issues they name: %q",
					garbled)
			}
			if !sameWorkItems(keys, want) {
				t.Errorf("oldest first, %d items a cycle, discovery limit %d: progress.jsonl selects %q, read as %v, want "+
					"%v alone, in any order, never %s: %s\n%s", run.items, run.discovered, selected, keys, want,
					targetKey(targetsOther, f.other), run.reason, runFiles(dir))
			}
			for _, x := range items {
				key, _ := x.(map[string]any)["key"].(string)
				if strings.HasPrefix(strings.ToLower(key), strings.ToLower(targetsOther)+"#") {
					t.Errorf("GET /queue tracks %s as a work item, though the task does not target %s: %v", key,
						targetsOther, x)
				}
			}
		})
	}
}

// targetsTask is the task issueQueueTask makes, oldest first, with its eligible-issues line taking every open issue
// labelled targetsLabel, whatever its repository, and secondClone, a clone of targetsSecond, as its second repository.
// Its queue takes at most items issues a cycle and, when discovered is above 0, discovers at most that many.
func targetsTask(d *daemon, firstClone, secondClone string, discovered, items int) string {
	d.t.Helper()
	task := issueQueueTask(d, firstClone,
		fmt.Sprintf("open issues labelled %s; every other issue is excluded.", targetsLabel), "oldest_first")
	queue := fmt.Sprintf("maxItemsPerCycle: %d", items)
	if discovered > 0 {
		queue = fmt.Sprintf("maxDiscoveredPerCycle: %d\n      %s", discovered, queue)
	}
	for _, r := range []struct{ from, to string }{
		{
			fmt.Sprintf("        localPath: %s\n", firstClone),
			fmt.Sprintf("        localPath: %s\n      - name: %s\n        localPath: %s\n", firstClone, targetsSecond,
				secondClone),
		},
		{"maxItemsPerCycle: 1", queue},
	} {
		if !strings.Contains(task, r.from) {
			d.t.Fatalf("issueQueueTask's task has no %q to replace:\n%s", r.from, task)
		}
		task = strings.Replace(task, r.from, r.to, 1)
	}
	return task
}

// targetsIssues is what targetsFixtures opened: the issues of each target, oldest first, and the issue of targetsOther.
type targetsIssues struct {
	first, second []int
	other         int
}

// targetsFixtures opens the five fixture issues, in the order the test's doc comment gives, and returns their numbers.
// It creates targetsLabel in each repository that lacks it. What an earlier run left open under the label, when it was
// stopped before its cleanup, would be eligible too, so it is closed first; when the test ends, failed or not, the same
// cleanup closes this run's fixtures in all three repositories. It returns once GitHub lists each repository's
// fixtures under the label as the session reads them.
func targetsFixtures(t *testing.T) targetsIssues {
	t.Helper()
	assignee := newWorkAPI(t, "user", "--jq", ".login")
	repos := []string{queueRepo, targetsSecond, targetsOther}
	for _, r := range repos {
		if _, err := newWorkGh("repos/" + r + "/labels/" + targetsLabel); err != nil {
			newWorkAPI(t, "repos/"+r+"/labels", "-f", "name="+targetsLabel)
		}
		closeTargetsFixtures(t, r)
	}
	own := map[string][]int{}
	t.Cleanup(func() {
		for _, r := range repos {
			closeTargetsFixtures(t, r, own[r]...)
		}
	})
	run := runSuffix()
	for i, r := range []string{targetsOther, queueRepo, targetsSecond, queueRepo, targetsSecond} {
		if i > 0 {
			// GitHub stamps an issue's creation to the second, and the queue reads the issues in that order.
			time.Sleep(time.Second)
		}
		n, assignees := targetsIssue(t, r, run, i+1, assignee)
		own[r] = append(own[r], n)
		if !slices.Contains(strings.Split(assignees, ","), assignee) {
			t.Fatalf("%s#%d is assigned to %q, want %s, whom the scheduled queues leave alone", r, n, assignees, assignee)
		}
	}
	for _, r := range repos {
		awaitTargetsListed(t, r, own[r])
	}
	return targetsIssues{first: own[queueRepo], second: own[targetsSecond], other: own[targetsOther][0]}
}

// targetsIssue opens the run's which-th fixture issue in repo, labelled targetsLabel and assigned to assignee, and
// returns its number and the logins GitHub assigned it, separated by commas.
func targetsIssue(t *testing.T, repo, run string, which int, assignee string) (int, string) {
	t.Helper()
	file := fmt.Sprintf("notes/%s-%d.txt", run, which)
	out := newWorkAPI(t, "repos/"+repo+"/issues", "-f", "title=Add the note "+file,
		"-f", fmt.Sprintf("body=Add the file `%s` with the single line `%s`, and change nothing else.\n", file, run),
		"-f", "labels[]="+targetsLabel, "-f", "assignees[]="+assignee,
		"--jq", `"\(.number) \([.assignees[].login] | join(","))"`)
	number, assignees, _ := strings.Cut(out, " ")
	n, err := strconv.Atoi(number)
	if err != nil {
		t.Fatalf("open the fixture issue %d in %s: gh api printed %q", which, repo, out)
	}
	return n, assignees
}

// awaitTargetsListed waits until the open issues of repo labelled targetsLabel are want, in the order they were opened.
// GitHub lists a new issue under its label only some seconds after it opens it.
func awaitTargetsListed(t *testing.T, repo string, want []int) {
	t.Helper()
	var listed []int
	var err error
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(time.Second) {
		if err = newWorkJSON(&listed, targetsOpenIssues(repo), "--jq",
			"[.[] | select(.pull_request | not) | .number]"); err == nil && slices.Equal(listed, want) {
			return
		}
	}
	t.Fatalf("after a minute, the open issues of %s labelled %s are %v (%v), want %v", repo, targetsLabel, listed, err,
		want)
}

// closeTargetsFixtures closes, as not planned, the open issues of repo labelled targetsLabel, and the own ones, which
// GitHub may list under the label only some seconds after it opens them.
func closeTargetsFixtures(t *testing.T, repo string, own ...int) {
	t.Helper()
	var open []int
	if err := newWorkJSON(&open, targetsOpenIssues(repo), "--jq", "[.[] | select(.pull_request | not) | .number]"); err != nil {
		t.Errorf("cleanup of %s: %v", repo, err)
	}
	for _, n := range own {
		if !slices.Contains(open, n) {
			open = append(open, n)
		}
	}
	for _, n := range open {
		if _, err := newWorkGh(fmt.Sprintf("repos/%s/issues/%d", repo, n), "-X", "PATCH", "-f", "state=closed",
			"-f", "state_reason=not_planned"); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	}
}

// targetsOpenIssues is the gh api path that lists the open issues and pull requests of repo labelled targetsLabel,
// oldest first, read from the repository rather than from GitHub's search index, which can lag behind a new issue.
func targetsOpenIssues(repo string) string {
	return "repos/" + repo + "/issues?state=open&sort=created&direction=asc&per_page=100&labels=" + targetsLabel
}

func targetKey(repo string, n int) string { return fmt.Sprintf("%s#%d", repo, n) }
