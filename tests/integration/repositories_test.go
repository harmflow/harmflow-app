package integration

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// taskRepository is one repository of a GitHub task: its name, the local path of its clone, and its base branch, empty
// for the default one.
type taskRepository struct {
	Name       string `json:"name"`
	LocalPath  string `json:"localPath"`
	BaseBranch string `json:"baseBranch"`
}

// repositoriesTask is a GitHub task, id, on repos, in their order, whose prompt is prompt.
func repositoriesTask(id, prompt string, repos ...taskRepository) string {
	var b strings.Builder
	b.WriteString(`apiVersion: harmflow/v1alpha1
kind: Task
metadata:
  id: ` + id + `
  name: Task ` + id + `
spec:
  type: github
  prompt: ` + strconv.Quote(prompt) + `
  harness:
    preferred:
      provider: claude-code
  github:
    repositories:
`)
	for _, r := range repos {
		b.WriteString("      - name: " + r.Name + "\n        localPath: " + strconv.Quote(r.LocalPath) + "\n")
		if r.BaseBranch != "" {
			b.WriteString("        baseBranch: " + r.BaseBranch + "\n")
		}
	}
	return b.String()
}

// localRepositories makes a folder of the test's own, with its symlinks resolved as the daemon resolves a local path,
// and in it a Git repository on main for each name, which it returns by name. The daemon is given the folder as its
// one allowed folder, since a temporary folder is outside the home folder.
func localRepositories(t *testing.T, d *daemon, names ...string) (root string, repos map[string]string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repos = map[string]string{}
	for _, name := range names {
		repos[name] = filepath.Join(root, name)
		git(t, root, "init", "-q", "-b", "main", repos[name])
	}
	d.call("PATCH", "/settings", map[string]any{"allowedFolders": []any{root}}, 200)
	return root, repos
}

// repositoriesContext is what the tests read of the context.json the daemon gave a session: the task's repositories,
// as the task names them, and the local path of each, as the session works in it.
type repositoriesContext struct {
	Task struct {
		Parameters struct {
			Repositories []taskRepository `json:"repositories"`
		} `json:"parameters"`
	} `json:"task"`
	Paths struct {
		Repositories map[string]string `json:"repositories"`
	} `json:"paths"`
}

// harmflow-app#1071: a GitHub task with two repositories, each with its own local path, hands both to the session. Its
// one execution, through the stand-in Claude Code, completes; its context.json lists both repositories under
// task.parameters.repositories, with their names, local paths, and base branches, the default one for the first, and
// both local paths under paths.repositories; the session's context names both under its repositories by local path;
// and Claude Code is given both as added directories, the paths its session may work in beside its workspace.
func TestTaskWithTwoRepositoriesHandsBothToTheSession(t *testing.T) {
	t.Parallel()
	d := startDaemon(t, t.TempDir())
	d.call("PUT", "/harnesses/claude-code", map[string]any{"path": recordingStandIn(t, "fake-claude", "claude")}, 200)
	_, local := localRepositories(t, d, "first", "second")
	first := taskRepository{Name: "o/first", LocalPath: local["first"]}
	second := taskRepository{Name: "o/second", LocalPath: local["second"], BaseBranch: "develop"}
	d.call("POST", "/tasks", map[string]any{"yaml": repositoriesTask("two", "Say hi.", first, second)}, 201)

	x := d.completedRun("two")
	data, err := os.ReadFile(filepath.Join(x["executionDir"].(string), "context.json"))
	var ctx repositoriesContext
	if err == nil {
		err = json.Unmarshal(data, &ctx)
	}
	if err != nil {
		t.Fatalf("context.json of %v: %v", x["id"], err)
	}
	first.BaseBranch = "main"
	if got, want := ctx.Task.Parameters.Repositories, []taskRepository{first, second}; !slices.Equal(got, want) {
		t.Errorf("context.json task.parameters.repositories = %+v, want %+v", got, want)
	}
	if got, want := ctx.Paths.Repositories, map[string]string{first.Name: first.LocalPath,
		second.Name: second.LocalPath}; !maps.Equal(got, want) {
		t.Errorf("context.json paths.repositories = %v, want %v", got, want)
	}

	args := recordedArgs(t, x)
	i := slices.Index(args, "--append-system-prompt")
	if i < 0 || i+1 == len(args) {
		t.Fatalf("the session's arguments = %q, want an --append-system-prompt", args)
	}
	for _, r := range []taskRepository{first, second} {
		if line := "- " + r.Name + ": " + r.LocalPath + "\n"; !strings.Contains(args[i+1], line) {
			t.Errorf("the session's context names no %q:\n%s", line, args[i+1])
		}
		if !passedPair(args, "--add-dir", r.LocalPath) {
			t.Errorf("the session's arguments = %q, want --add-dir %s", args, r.LocalPath)
		}
	}
}

// harmflow-app#1071: a GitHub task whose second repository's local path does not exist saves, with a warning, but its
// execution is refused as it prepares, before any attempt starts a harness: it fails with an error that names that
// repository and its path, and the stand-in Claude Code never runs a session for it.
func TestTaskWhoseSecondRepositoryIsMissingIsRefusedBeforeItStarts(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	d := startDaemon(t, home)
	d.call("PUT", "/harnesses/claude-code", map[string]any{"path": recordingStandIn(t, "fake-claude", "claude")}, 200)
	root, local := localRepositories(t, d, "first")
	missing := filepath.Join(root, "missing")
	d.call("POST", "/tasks", map[string]any{"yaml": repositoriesTask("missing", "Say hi.",
		taskRepository{Name: "o/first", LocalPath: local["first"]},
		taskRepository{Name: "o/missing", LocalPath: missing})}, 201)

	id := d.call("POST", "/tasks/missing/run", nil, 202)["executionId"].(string)
	x := d.waitStatus(id, "completed", "failed")
	attempts, _ := x["attempts"].([]any)
	message, _ := x["errorMessage"].(string)
	if x["status"] != "failed" || len(attempts) != 0 || !strings.Contains(message, "o/missing") ||
		!strings.Contains(message, missing) {
		t.Errorf("execution %s: status %v, attempts %v, error %q; want it failed with no attempt, naming o/missing "+
			"and %s", id, x["status"], attempts, message, missing)
	}
	if _, err := os.Stat(filepath.Join(home, "runs", id, "args")); !os.IsNotExist(err) {
		t.Errorf("the stand-in recorded a session for execution %s (%v), want none", id, err)
	}
}
