package skills

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The removal of hf-github-worktree §3, run as a session runs it: each gh api command the skill writes, through the
// real gh, against a stub that answers for each work item as GitHub would, and each git command it writes, on real
// worktrees of a real clone. Of the specification's three worktrees, the one whose pull request was merged goes, while
// the draft's still in progress and the failed item's stay; the other cases are the other ways an item ends, or does
// not.
func TestEndedItemWorktreeRemoval(t *testing.T) {
	gh, err := exec.LookPath("gh")
	if err != nil {
		t.Fatalf("the removal runs gh api, and gh is not on PATH: %v", err)
	}
	section := removalSection(t)
	pulls, issue, remove := removalCommands(t, section)

	items := []struct {
		name   string
		status string // the item's status in context.json
		pulls  string // what GitHub lists for its branch
		issue  string // its issue's state
		ended  bool
	}{
		{"a merged pull request", "completed", `[{"state":"closed","draft":false,"merged_at":"2026-10-28T12:00:00Z"}]`, "closed", true},
		{"a draft still in progress", "completed", `[{"state":"open","draft":true,"merged_at":null}]`, "open", false},
		{"a failed item", "failed", `[]`, "open", false},
		{"a pull request closed without merging", "needs_attention", `[{"state":"closed","draft":true,"merged_at":null}]`, "open", true},
		{"a pull request opened again from the same branch", "in_progress",
			`[{"state":"open","draft":true,"merged_at":null},{"state":"closed","draft":true,"merged_at":null}]`, "open", false},
		{"an issue closed with no pull request", "completed", `[]`, "closed", true},
	}
	clone, workspace := cloneWithWorktrees(t, len(items))
	// What the stub answers, by the head a pull request listing asks for and by an issue's path.
	pullsOf, issueOf := map[string]string{}, map[string]string{}
	for i, it := range items {
		if it.status == "failed" {
			continue // the skill skips a failed item: GitHub is never asked about it
		}
		n := strconv.Itoa(i + 1)
		pullsOf["harmflow:"+branchOf(i+1)] = it.pulls
		issueOf["/repos/harmflow/example/issues/"+n] = `{"number":` + n + `,"state":"` + it.issue + `"}`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := issueOf[r.URL.Path]
		if q := r.URL.Query(); r.URL.Path == "/repos/harmflow/example/pulls" && q.Get("state") == "all" {
			body, ok = pullsOf[q.Get("head")]
		}
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			t.Errorf("gh asked for %s, which the removal does not read", r.URL)
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	for i, it := range items {
		t.Run(it.name, func(t *testing.T) {
			n := i + 1
			wt := filepath.Join(workspace, "example-"+strconv.Itoa(n))
			ended := false
			if it.status != "failed" {
				pr := ask(t, gh, srv.URL, pulls, n, "none", "open", "merged", "closed")
				ended = pr == "merged" || pr == "closed" || ask(t, gh, srv.URL, issue, n, "open", "closed") == "closed"
			}
			if ended != it.ended {
				t.Fatalf("the item ended = %v, want %v", ended, it.ended)
			}
			if ended {
				for _, args := range remove {
					git(t, fill(args, map[string]string{"<local path>": clone, "<worktree path>": wt})...)
				}
			}
			_, statErr := os.Stat(wt)
			listed := strings.Contains(gitRun(t, clone, "worktree", "list", "--porcelain"), "worktree "+wt+"\n")
			if gone := os.IsNotExist(statErr) && !listed; gone != it.ended {
				t.Errorf("the worktree is gone = %v (on disk: %v, listed by git: %v), want %v", gone, statErr == nil,
					listed, it.ended)
			}
			if !strings.Contains(gitRun(t, clone, "branch", "--list", branchOf(n)), branchOf(n)) {
				t.Errorf("the removal deleted the branch %s, which stays", branchOf(n))
			}
		})
	}
}

// removalSection is §3 of the skill.
func removalSection(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(worktreeSkill)
	if err != nil {
		t.Fatal(err)
	}
	const heading = "## 3. Remove worktrees whose item ended\n"
	_, section, ok := strings.Cut(string(data), heading)
	if !ok {
		t.Fatalf("%s has no heading %q", worktreeSkill, strings.TrimSpace(heading))
	}
	section, _, _ = strings.Cut(section, "\n## ")
	return section
}

// removalCommands are the arguments of the section's commands, in its two bash blocks: after gh, the gh api command
// that reads an item's pull requests and the one that reads its issue; after git, the commands that remove a worktree.
func removalCommands(t *testing.T, section string) (pulls, issue []string, remove [][]string) {
	t.Helper()
	var blocks []string
	for rest := section; ; {
		_, after, ok := strings.Cut(rest, "```bash\n")
		if !ok {
			break
		}
		block, tail, _ := strings.Cut(after, "```")
		blocks, rest = append(blocks, block), tail
	}
	if len(blocks) != 2 {
		t.Fatalf("§3 of %s has %d bash blocks, want 2: the item's state and the removal", worktreeSkill, len(blocks))
	}
	var reads [][]string
	for _, line := range strings.Split(blocks[0], "\n") {
		if strings.HasPrefix(line, "gh api ") {
			reads = append(reads, words(t, line)[1:])
		}
	}
	if len(reads) != 2 {
		t.Fatalf("§3 of %s reads with %d gh api commands, want 2: the pull requests and the issue", worktreeSkill, len(reads))
	}
	for _, line := range strings.Split(blocks[1], "\n") {
		if strings.HasPrefix(line, "git ") {
			remove = append(remove, words(t, line)[1:])
		}
	}
	if len(remove) == 0 {
		t.Fatalf("§3 of %s removes a worktree with no git command", worktreeSkill)
	}
	return reads[0], reads[1], remove
}

// ask runs one of the skill's gh api commands for item n of the repository harmflow/example, against the stub at base,
// and returns what it printed, which must be one of the words the skill reads.
func ask(t *testing.T, gh, base string, args []string, n int, want ...string) string {
	t.Helper()
	args = append([]string{}, args...)
	endpoint := strings.NewReplacer("OWNER/REPO", "harmflow/example", "OWNER:", "harmflow:", "BRANCH", branchOf(n),
		"ISSUE", strconv.Itoa(n)).Replace(args[1])
	args[1] = base + "/" + endpoint
	cmd := exec.Command(gh, args...)
	cmd.Env = append(os.Environ(), "GH_CONFIG_DIR="+t.TempDir(), "GH_TOKEN=stub", "GH_HOST=github.com",
		"GH_NO_UPDATE_NOTIFIER=1", "GH_PROMPT_DISABLED=1", "NO_COLOR=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("gh %s: %v", strings.Join(args, " "), err)
	}
	got := strings.TrimSpace(string(out))
	for _, w := range want {
		if got == w {
			return got
		}
	}
	t.Fatalf("gh %s printed %q, want one of %v", strings.Join(args, " "), got, want)
	return ""
}

// branchOf is the work branch of item n.
func branchOf(n int) string { return "fix/" + strconv.Itoa(n) + "-example" }

// cloneWithWorktrees is a clone of harmflow/example and the workspace beside it, where items 1 to n each have the
// worktree hf-github-worktree §2 gives them, <workspace>/example-<n>, on the item's branch.
func cloneWithWorktrees(t *testing.T, n int) (clone, workspace string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// A space in both paths, as in the workspace HarmFlow gives a task on macOS.
	clone, workspace = filepath.Join(root, "repos", "example"), filepath.Join(root, "Application Support", "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "init", "--quiet", "-b", "main", clone)
	gitRun(t, clone, "-c", "user.name=HarmFlow", "-c", "user.email=tests@harmflow.invalid", "commit", "--quiet",
		"--allow-empty", "-m", "chore: start")
	for i := 1; i <= n; i++ {
		gitRun(t, clone, "worktree", "add", "--quiet", "-b", branchOf(i), filepath.Join(workspace, "example-"+strconv.Itoa(i)), "main")
	}
	return clone, workspace
}

// gitRun runs git in dir and returns what it printed.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return git(t, append([]string{"-C", dir}, args...)...)
}

// git runs git with args, with no global or system configuration, and returns what it printed.
func git(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// fill is a copy of args with each word that is a key of values replaced by its value.
func fill(args []string, values map[string]string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if v, ok := values[a]; ok {
			a = v
		}
		out[i] = a
	}
	return out
}
