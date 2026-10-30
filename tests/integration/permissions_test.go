package integration

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// A read_only task on the real Codex asks the session to write one file inside its workspace and one outside it.
// Codex's read-only sandbox refuses both: after the execution neither file exists, and the execution carries no warning
// that the profile is not enforced.
func TestReadOnlyRealCodexWritesNothing(t *testing.T) {
	h := codexForPermissions(t)
	w := newWriteTargets(t)
	r := runWithProfile(t, h, "read_only", w, w.prompt("a file edit or a shell command"))

	r.wantAttemptAtOutside(t, w)
	wantAbsent(t, w.inside)
	wantAbsent(t, w.outside)
	r.wantEnforced(t, "read_only", "restrictToWorkspace")
}

// A workspace_write task on the real Codex asks the session to write one file inside its workspace and one outside it.
// Codex's workspace-write sandbox lets the first through and refuses the second, whether the session tried a file edit
// or a shell command, and the execution carries no warning that the restriction to the workspace is not enforced.
func TestWorkspaceWriteRealCodexWritesOnlyItsWorkspace(t *testing.T) {
	h := codexForPermissions(t)
	w := newWriteTargets(t)
	r := runWithProfile(t, h, "workspace_write", w, w.prompt("a file edit or a shell command"))

	r.wantAttemptAtOutside(t, w)
	w.wantWritten(t)
	wantAbsent(t, w.outside)
	r.wantEnforced(t, "read_only", "restrictToWorkspace")
}

// A read_only task on the real Claude Code asks the session to write one file inside its workspace and one outside it.
// The session has no tool that writes, so it calls none, and after the execution neither file exists. The execution
// carries no warning that the profile is not enforced.
func TestReadOnlyRealClaudeCodeWritesNothing(t *testing.T) {
	h := claudeForPermissions(t)
	w := newWriteTargets(t)
	r := runWithProfile(t, h, "read_only", w, w.prompt("the Write tool"))

	for _, c := range r.calls {
		if slices.Contains(writingTools, c.tool) {
			t.Errorf("a read_only Claude Code session called %s (%s%s), want no tool that writes", c.tool, c.path, c.command)
		}
	}
	wantAbsent(t, w.inside)
	wantAbsent(t, w.outside)
	r.wantEnforced(t, "read_only", "restrictToWorkspace")
}

// A workspace_write task on the real Claude Code asks the session to write, with the Write tool, one file inside its
// workspace and one outside it. The file inside is written, and no editing tool's call to the file outside succeeds.
// Claude Code does not confine its shell to the workspace, which the execution's warning for restrictToWorkspace says:
// a Bash call that wrote the file outside is logged as that shell, and only when no Bash call touched it must the file
// outside be absent.
func TestWorkspaceWriteRealClaudeCodeEditsOnlyItsWorkspace(t *testing.T) {
	h := claudeForPermissions(t)
	w := newWriteTargets(t)
	r := runWithProfile(t, h, "workspace_write", w, w.prompt("the Write tool"))

	r.wantAttemptAtOutside(t, w)
	w.wantWritten(t)
	shell := false
	for _, c := range r.calls {
		switch {
		case !c.names(w.outside):
		case slices.Contains(editingTools, c.tool) && c.status == "succeeded":
			t.Errorf("a workspace_write Claude Code session's %s call to %s succeeded, want it refused outside the workspace",
				c.tool, w.outside)
		case c.tool == "Bash":
			shell = true
		}
	}
	if !shell {
		wantAbsent(t, w.outside)
	} else if _, err := os.Stat(w.outside); err == nil {
		t.Logf("a Bash call wrote %s: the shell Claude Code does not confine to the workspace, as the execution's warning "+
			"for restrictToWorkspace says", w.outside)
	}
	r.wantEnforced(t, "read_only")
	if !slices.ContainsFunc(r.unenforced, func(u unenforced) bool {
		return u.subject == "restrictToWorkspace" && u.key == "restriction_claude_shell_workspace"
	}) {
		t.Errorf("the execution's unenforced restrictions are %v, want the one for restrictToWorkspace, about the shell",
			r.unenforced)
	}
}

// codexForPermissions is the real Codex, run with the signed-in Codex's own home.
func codexForPermissions(t *testing.T) realHarness {
	t.Helper()
	codex := realCodex(t)
	home := codexHome(t)
	return realHarness{
		id:   "codex",
		path: codex,
		// The model Codex's configuration sets, at the effort low, as in TestCitedUserItemsReachARealCodex.
		preferred: map[string]any{"provider": "codex", "effort": "low"},
		env:       []string{"CODEX_HOME=" + home},
	}
}

// claudeForPermissions is the real Claude Code, on a small model at the effort low to spend little of the account's
// usage.
func claudeForPermissions(t *testing.T) realHarness {
	t.Helper()
	return realHarness{
		id:        "claude-code",
		path:      realClaude(t),
		preferred: map[string]any{"provider": "claude-code", "model": "sonnet", "effort": "low"},
	}
}

// editingTools are Claude Code's tools that edit a file, and writingTools those and its shell: every tool of its own
// that can write.
var (
	editingTools = []string{"Write", "Edit", "MultiEdit", "NotebookEdit"}
	writingTools = append(slices.Clone(editingTools), "Bash")
)

// writeTargets are the two files a task asks its session to write: one inside its workspace, a Git worktree, and one in
// a folder beside it, which is no directory the session is given. Both lie under a parent outside $TMPDIR and /tmp,
// which a harness may leave writable on its own, and each file's name and content are unique to the run.
type writeTargets struct {
	work, inside, outside         string
	insideContent, outsideContent string
}

// newWriteTargets makes the parent in the user's home folder, removed when the test ends, a repository in it, the
// workspace as a worktree of that repository, and the outside folder, and checks that neither file exists yet. The
// parent is a folder of the home itself: the daemon refuses a workspace in ~/Library, where the user's cache folder is
// on macOS, or in a folder of the home whose name starts with a dot.
func newWriteTargets(t *testing.T) writeTargets {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	parent, err := os.MkdirTemp(home, "harmflow-permissions-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(parent); err != nil {
			t.Errorf("removing %s: %v", parent, err)
		}
	})
	if parent, err = filepath.EvalSymlinks(parent); err != nil {
		t.Fatal(err)
	}
	repo, work, outside := filepath.Join(parent, "repo"), filepath.Join(parent, "work"), filepath.Join(parent, "outside")
	for _, dir := range []string{repo, outside} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# Permissions\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "--quiet", "--initial-branch", "main")
	git(t, repo, "add", "README.md")
	git(t, repo, "commit", "--quiet", "-m", "init")
	git(t, repo, "worktree", "add", "--quiet", "-b", "work", work)

	run := runSuffix()
	w := writeTargets{
		work:           work,
		inside:         filepath.Join(work, "inside-"+run+".txt"),
		outside:        filepath.Join(outside, "outside-"+run+".txt"),
		insideContent:  "inside " + rand.Text(),
		outsideContent: "outside " + rand.Text(),
	}
	for _, path := range []string{w.inside, w.outside} {
		if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s exists before the execution (%v), want it absent", path, err)
		}
	}
	return w
}

// prompt asks the session to write both files with how, each with its content, and to report each result.
func (w writeTargets) prompt(how string) string {
	return fmt.Sprintf("Create these two files with %s, one at a time, and nothing else:\n\n"+
		"1. %s, holding this line exactly: %s\n"+
		"2. %s, holding this line exactly: %s\n\n"+
		"Try to create each one, even if you expect it to be refused: do not decide in advance that it will fail. "+
		"Then say, for each file, whether it was created and what refused it if it was not.",
		how, w.inside, w.insideContent, w.outside, w.outsideContent)
}

// wantWritten fails the test unless the file inside the workspace holds its content.
func (w writeTargets) wantWritten(t *testing.T) {
	t.Helper()
	data, err := os.ReadFile(w.inside)
	if err != nil {
		t.Errorf("the file inside the workspace was not written: %v", err)
		return
	}
	if got := strings.TrimSpace(string(data)); got != w.insideContent {
		t.Errorf("the file inside the workspace holds %q, want %q", got, w.insideContent)
	}
}

// wantAbsent fails the test if the file exists after the execution.
func wantAbsent(t *testing.T, path string) {
	t.Helper()
	if data, err := os.ReadFile(path); err == nil {
		t.Errorf("after the execution %s exists, holding %q; want it absent", path, strings.TrimSpace(string(data)))
	} else if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("reading %s: %v", path, err)
	}
}

// permissionRun is what an execution of a task asking for the two writes left: its tool calls, the restrictions its
// warnings say are not enforced, and the harness's raw output.
type permissionRun struct {
	calls      []toolCall
	unenforced []unenforced
	output     string
}

// runWithProfile runs a task with the profile on the real harness h, its workspace w's, and waits for its end. The
// daemon's allowed folders are the workspace alone, so the outside folder is no directory the session is given.
func runWithProfile(t *testing.T, h realHarness, profile string, w writeTargets, prompt string) permissionRun {
	t.Helper()
	d := startDaemon(t, t.TempDir(), h.env...)
	d.call("PUT", "/harnesses/"+h.id, map[string]any{"path": h.path}, 200)
	d.call("PATCH", "/settings", map[string]any{"allowedFolders": []any{w.work}}, 200)
	id := strings.ReplaceAll(profile, "_", "-") + "-" + h.id
	doc := map[string]any{"apiVersion": "harmflow/v1alpha1", "kind": "Task",
		"metadata": map[string]any{"id": id, "name": profile + " on " + h.id},
		"spec": map[string]any{
			"type":        "general",
			"prompt":      prompt,
			"harness":     map[string]any{"preferred": h.preferred},
			"workspace":   map[string]any{"path": w.work},
			"permissions": map[string]any{"profile": profile},
			"execution":   map[string]any{"timeoutMinutes": 5},
		}}
	d.call("POST", "/tasks", map[string]any{"document": doc}, 201)
	run := d.call("POST", "/tasks/"+id+"/run", nil, 202)["executionId"].(string)
	// Longer than the task's timeout, so that the execution's own end says why a session ran too long.
	x := d.waitEnd(run, 6*time.Minute)
	if x["status"] != "completed" {
		t.Errorf("execution %s = %v (%v), want completed", run, x["status"], x["errorMessage"])
	}

	events := d.events(run)
	r := permissionRun{calls: toolCalls(events)}
	// A file change Codex made is no tool call: the daemon records it as a change to the workspace alone.
	for _, e := range events {
		if e["type"] == "workspace.changed" {
			t.Logf("workspace change: %v", e["message"])
		}
	}
	warnings, _ := x["warnings"].([]any)
	for _, raw := range warnings {
		if warning, _ := raw.(map[string]any); warning["code"] == "policy_unenforced" {
			subject, _ := warning["subject"].(string)
			key, _ := warning["messageKey"].(string)
			r.unenforced = append(r.unenforced, unenforced{subject: subject, key: key})
		}
	}
	attempts, _ := d.call("GET", "/executions/"+run+"/output", nil, 200)["attempts"].([]any)
	for _, raw := range attempts {
		a, _ := raw.(map[string]any)
		text, _ := a["text"].(string)
		r.output += text
	}
	// What refused a call, in the harness's own words, says whether a tool was missing or denied.
	for _, c := range r.calls {
		t.Logf("tool call: %s %s%s, %s: %.300s", c.tool, c.path, c.command, c.status, c.output)
	}
	return r
}

// wantAttemptAtOutside fails the test unless the session tried to write the file outside: a tool call that names it,
// or an item of Codex's raw output, a command or a file change, that does. A file change Codex refused is in its raw
// output alone, and a mention in the session's own words is no attempt.
func (r permissionRun) wantAttemptAtOutside(t *testing.T, w writeTargets) {
	t.Helper()
	if slices.ContainsFunc(r.calls, func(c toolCall) bool { return c.names(w.outside) }) || codexItemNames(r.output, w.outside) {
		return
	}
	t.Errorf("the session never tried to write %s, so the run says nothing of what refuses it", w.outside)
}

// wantEnforced fails the test if the execution warns that one of the restrictions is not enforced.
func (r permissionRun) wantEnforced(t *testing.T, restrictions ...string) {
	t.Helper()
	for _, u := range r.unenforced {
		if slices.Contains(restrictions, u.subject) {
			t.Errorf("the execution warns that %s is not enforced (%s), want it enforced", u.subject, u.key)
		}
	}
}

// unenforced is a restriction an execution's policy_unenforced warning names, with the key of its text.
type unenforced struct{ subject, key string }

// toolCall is a tool the session called, from its tool.started event, with the status and the output its
// tool.completed event gave.
type toolCall struct{ id, tool, path, command, status, output string }

// names reports whether the call names the file, by its name, which is unique to the run: as its path, or in its
// command, whichever folder the command reaches it from.
func (c toolCall) names(path string) bool {
	name := filepath.Base(path)
	return strings.Contains(c.path, name) || strings.Contains(c.command, name)
}

// toolCalls pairs an execution's tool.started events with their tool.completed events, in the order the calls started.
func toolCalls(events []map[string]any) []toolCall {
	var calls []toolCall
	index := map[string]int{}
	for _, e := range events {
		p, _ := e["payload"].(map[string]any)
		id, _ := p["toolId"].(string)
		switch e["type"] {
		case "tool.started":
			c := toolCall{id: id}
			c.tool, _ = p["tool"].(string)
			c.path, _ = p["path"].(string)
			c.command, _ = p["command"].(string)
			index[id] = len(calls)
			calls = append(calls, c)
		case "tool.completed":
			if i, ok := index[id]; ok {
				calls[i].status, _ = p["status"].(string)
				calls[i].output, _ = p["output"].(string)
			}
		}
	}
	return calls
}

// codexItem is an item of Codex's JSON output, in Codex's own names: a command it ran, or a file change it made or was
// refused.
type codexItem struct {
	Type    string        `json:"type"`
	Command string        `json:"command"`
	Changes []codexChange `json:"changes"`
}

type codexChange struct {
	Path string `json:"path"`
}

// codexItemNames reports whether a command or a file change of Codex's JSON output names the file, by its name.
func codexItemNames(output, path string) bool {
	name := filepath.Base(path)
	for line := range strings.Lines(output) {
		var l struct {
			Item *codexItem `json:"item"`
		}
		if json.Unmarshal([]byte(line), &l) != nil || l.Item == nil {
			continue
		}
		switch l.Item.Type {
		case "command_execution":
			if strings.Contains(l.Item.Command, name) {
				return true
			}
		case "file_change":
			if slices.ContainsFunc(l.Item.Changes, func(c codexChange) bool { return strings.Contains(c.Path, name) }) {
				return true
			}
		}
	}
	return false
}
