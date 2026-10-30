package integration

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// selectionLabel marks the two fixture issues TestNamedIssueAndNoSelectionFindTheirWork opens in queueRepo. The test
// owns it alone, so that its cleanup never closes another test's fixtures.
const selectionLabel = "issue-selection-test"

// A task's prompt may select its work by criteria, by one issue it names, or not at all. The criteria are the first
// firing of TestFailedIssueIsRetriedUntilItsLimit; this test runs the two other modes with the real Claude Code, on the
// built-in agent's copy and the built-in skills of this checkout, against queueRepo, where it opens two fixture issues.
// A prompt that names the newer fixture takes it, though the older one comes first in the queue's order. A prompt that
// states no selection, in a task that sets no queue order, takes the issue the default policy gives: the oldest open
// issue that no pull request links. Each session is stopped once the daemon tracks the issue it selected, and its clone
// has no push URL, so no branch and no pull request of its reaches GitHub.
func TestNamedIssueAndNoSelectionFindTheirWork(t *testing.T) {
	claude := realClaude(t)
	older, newer := selectionFixtures(t)
	for _, mode := range []struct {
		name string
		// eligible gives the copy's eligible-issues line, and ordering the queue's order; empty leaves either out.
		eligible, ordering string
		// want is the issue the session must select, read just before the run.
		want   func(t *testing.T) int
		reason string
	}{
		{
			name:     "a named issue",
			eligible: fmt.Sprintf("issue #%d of %s.", newer, queueRepo),
			ordering: "oldest_first",
			want:     func(*testing.T) int { return newer },
			reason:   fmt.Sprintf("the prompt names it, though #%d, the older fixture, comes first", older),
		},
		{
			name:   "no selection",
			want:   firstUnlinkedIssue,
			reason: "with no criterion and no order, the default policy gives the oldest open issue no pull request links",
		},
	} {
		t.Run(mode.name, func(t *testing.T) {
			clone := shallowClone(t)
			home := t.TempDir()
			d := startDaemon(t, home)
			useClaude(d, claude, clone)
			d.call("POST", "/tasks", map[string]any{"yaml": issueQueueTask(d, clone, mode.eligible, mode.ordering)}, 201)
			want := mode.want(t)

			id := d.call("POST", "/tasks/queue/run", nil, 202)["executionId"].(string)
			dir := executionDir(d, id)
			got := selectedIssue(t, d, id, dir)
			waitTracked(t, d, home, id, dir, got)
			d = restartMidRun(t, d, home, claude, clone)
			if x := d.call("GET", "/executions/"+id, nil, 200); x["status"] != "interrupted" {
				t.Errorf("execution %s after the stop at selection: status %v, want interrupted", id, x["status"])
			}
			if got != want {
				t.Errorf("the session selected #%d, want #%d: %s\n%s", got, want, mode.reason, runFiles(dir))
			}
		})
	}
}

// selectionFixtures opens two fixture issues in queueRepo, labelled selectionLabel, one after the other, and returns
// their numbers, the older first. It creates the label when it is missing. An open issue under the label is one an
// earlier run left when it was stopped before its cleanup, so it is closed first. When the test ends, failed or not,
// the label's open issues are closed, and the two fixtures with them: GitHub lists a new issue under its label only
// some seconds after it opens it.
func selectionFixtures(t *testing.T) (older, newer int) {
	t.Helper()
	if exec.Command("gh", "api", "repos/"+queueRepo+"/labels/"+selectionLabel, "--silent").Run() != nil {
		if out, err := exec.Command("gh", "label", "create", selectionLabel, "--repo", queueRepo).CombinedOutput(); err != nil {
			t.Fatalf("gh label create %s: %v\n%s", selectionLabel, err, out)
		}
	}
	closeSelectionFixtures(t)
	run := runSuffix()
	var opened []int
	t.Cleanup(func() { closeSelectionFixtures(t, opened...) })
	for _, which := range []string{"older", "newer"} {
		file := fmt.Sprintf("%s/%s-%s.txt", selectionLabel, run, which)
		body := fmt.Sprintf("A fixture of HarmFlow's issue selection test, which closes it when it ends.\n\n"+
			"Add the file `%s` with the single line `%s`, and change nothing else. The repository has no tests.\n", file, run)
		out, err := exec.Command("gh", "issue", "create", "--repo", queueRepo, "--label", selectionLabel,
			"--title", "Issue selection test "+run+": add "+file, "--body", body).Output()
		if err != nil {
			t.Fatalf("gh issue create: %v", err)
		}
		url := strings.TrimSpace(string(out))
		n, err := strconv.Atoi(url[strings.LastIndex(url, "/")+1:])
		if err != nil {
			t.Fatalf("gh issue create printed %q, not an issue's URL", url)
		}
		opened = append(opened, n)
	}
	return opened[0], opened[1]
}

// closeSelectionFixtures closes the open issues of queueRepo labelled selectionLabel, and the own ones.
func closeSelectionFixtures(t *testing.T, own ...int) {
	t.Helper()
	out, err := exec.Command("gh", "api", "repos/"+queueRepo+"/issues?state=open&per_page=100&labels="+selectionLabel,
		"--jq", "[.[] | select(.pull_request | not) | .number]").Output()
	var issues []int
	if err == nil {
		err = json.Unmarshal(out, &issues)
	}
	if err != nil {
		t.Errorf("cleanup: list the open issues of %s labelled %s: %v", queueRepo, selectionLabel, err)
	}
	for _, n := range own {
		if !slices.Contains(issues, n) {
			issues = append(issues, n)
		}
	}
	for _, n := range issues {
		if out, err := exec.Command("gh", "issue", "close", strconv.Itoa(n), "--repo", queueRepo).CombinedOutput(); err != nil {
			t.Errorf("cleanup: gh issue close %d: %v\n%s", n, err, out)
		}
	}
}

// firstUnlinkedIssue is the issue the default policy gives a prompt that states no selection, read as
// hf-github-issue-queue reads it: queueRepo's open issues through the REST listing, oldest first, pull requests left
// out, and the first of them that no pull request links, in any state. The daemon's home is fresh, so no work item or
// recent execution of its own makes an issue already processed.
func firstUnlinkedIssue(t *testing.T) int {
	t.Helper()
	for page := 1; ; page++ {
		out, err := exec.Command("gh", "api", fmt.Sprintf(
			"repos/%s/issues?state=open&sort=created&direction=asc&per_page=100&page=%d", queueRepo, page),
			"--jq", "{fetched: length, issues: [.[] | select(.pull_request | not) | .number]}").Output()
		if err != nil {
			t.Fatalf("list the open issues of %s: %v", queueRepo, err)
		}
		var listed struct {
			Fetched int   `json:"fetched"`
			Issues  []int `json:"issues"`
		}
		if err := json.Unmarshal(out, &listed); err != nil {
			t.Fatalf("list the open issues of %s: %v", queueRepo, err)
		}
		for _, n := range listed.Issues {
			if !linkedByAPullRequest(t, n) {
				return n
			}
		}
		if listed.Fetched < 100 {
			break
		}
	}
	t.Fatalf("a pull request links every open issue of %s, so a prompt with no selection has no work", queueRepo)
	return 0
}

// linkedByAPullRequest says whether a pull request of queueRepo, in any state, links the issue as closing it: the
// issue's closedByPullRequestsReferences, closed pull requests included.
func linkedByAPullRequest(t *testing.T, issue int) bool {
	t.Helper()
	const query = `query($owner: String!, $repo: String!, $n: Int!) { repository(owner: $owner, name: $repo) {
  issue(number: $n) { closedByPullRequestsReferences(first: 10, includeClosedPrs: true) { nodes { number } } } } }`
	owner, name, _ := strings.Cut(queueRepo, "/")
	out, err := exec.Command("gh", "api", "graphql", "-F", "owner="+owner, "-F", "repo="+name, "-F", "n="+strconv.Itoa(issue),
		"-f", "query="+query, "--jq", ".data.repository.issue.closedByPullRequestsReferences.nodes | length").Output()
	if err != nil {
		t.Fatalf("read the pull requests linking #%d: %v", issue, err)
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("read the pull requests linking #%d: %q is not a count", issue, out)
	}
	return count > 0
}
