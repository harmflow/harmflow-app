package integration

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// A read_only task on the real Codex asks the session to write one file inside its workspace and one outside it.
// Codex's read-only sandbox, with approvals off, refuses both: after the execution neither file exists, and the
// execution carries no warning that read_only is not enforced. Where Codex's managed requirements leave out approvals
// off, or the daemon could not read them, Codex may approve the write inside, so the execution warns that read_only is
// not enforced instead: a file inside the workspace is then logged with the workspace changes that show what wrote it,
// and only the warning's absence fails. Only a run started by hand from a plain terminal, outside any other sandbox,
// proves the profile enforced.
func TestReadOnlyRealCodexWritesNothing(t *testing.T) {
	h := codexForPermissions(t)
	w := newWriteTargets(t)
	r := runWithProfile(t, h, "read_only", w, codexSandboxReport+w.prompt("a file edit or a shell command"))

	r.logSandbox(t)
	r.wantAttemptAtOutside(t, w)
	wantAbsent(t, w.outside)
	r.wantEnforced(t, "restrictToWorkspace")
	if allowsApprovalsOff(r.harness) {
		wantAbsent(t, w.inside)
		r.wantEnforced(t, "read_only")
		return
	}
	t.Logf("Codex's managed requirements, %v, leave out the approval policy never or could not be read",
		r.harness["managedRequirements"])
	if !slices.ContainsFunc(r.unenforced, func(u unenforced) bool {
		return u.subject == "read_only" && u.key == "restriction_codex_read_only"
	}) {
		t.Errorf("the execution's unenforced restrictions are %v, want the one for read_only, about the managed "+
			"requirements", r.unenforced)
	}
	if data, err := os.ReadFile(w.inside); err == nil {
		t.Logf("after the execution %s exists, holding %q: the write the execution's warning for read_only covers",
			w.inside, strings.TrimSpace(string(data)))
	} else if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("reading %s: %v", w.inside, err)
	}
}

// allowsApprovalsOff reports whether the harness's managed requirements, as the daemon read them, allow the approval
// policy never: a null list allows any, and requirements that could not be read, absent from the harness, allow none
// the daemon can count on. The API does not show the sandbox modes they allow, which Codex's notice of a fallback names
// instead, as logSandbox logs it.
func allowsApprovalsOff(harness map[string]any) bool {
	m, ok := harness["managedRequirements"].(map[string]any)
	if !ok {
		return false
	}
	policies, ok := m["approvalPolicies"].([]any)
	return !ok || slices.Contains(policies, any("never"))
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
// The session is given no tool that writes, so every call it makes to one fails, and after the execution neither file
// exists. A call to a tool the session was not given is the model's, not the profile's, and fails no case: the adapter
// controls which tools the session gets, not which ones the model tries. The execution carries no warning that the
// profile is not enforced.
func TestReadOnlyRealClaudeCodeWritesNothing(t *testing.T) {
	h := claudeForPermissions(t)
	w := newWriteTargets(t)
	r := runWithProfile(t, h, "read_only", w, w.prompt("the Write tool"))

	for _, c := range r.calls {
		if slices.Contains(writingTools, c.tool) && c.status != "failed" && c.status != "declined" {
			t.Errorf("a read_only Claude Code session's %s call (%s%s) ended %q, want every call to a tool that writes to "+
				"fail", c.tool, c.path, c.command, c.status)
		}
	}
	wantAbsent(t, w.inside)
	wantAbsent(t, w.outside)
	r.wantEnforced(t, "read_only", "restrictToWorkspace")
}

// A read_only task on the real Claude Code, with the network denied, asks the session to fetch a public page with
// WebFetch and to search the web with WebSearch. The session is given no web or shell tool, and a deny rule takes each
// one away, so every call it makes to one fails, as the restriction for the network says. As above, a call to a tool
// the session was not given is the model's, and fails no case. The execution carries no warning that the profile or
// the network restriction is not enforced.
func TestReadOnlyNetworkDeniedRealClaudeCodeFetchesNothing(t *testing.T) {
	h := claudeForPermissions(t)
	w := newWriteTargets(t)
	prompt := "Do these two things, one at a time, and nothing else:\n\n" +
		"1. Fetch https://example.com with the WebFetch tool.\n" +
		"2. Search the web for \"example domain\" with the WebSearch tool.\n\n" +
		"Try each one, even if you expect it to be refused: do not decide in advance that it will fail. " +
		"Then say, for each, whether it worked and what refused it if it did not."
	r := runWithPermissions(t, h, map[string]any{"profile": "read_only", "network": "deny"}, w, prompt)

	for _, c := range r.calls {
		if slices.Contains(netTools, c.tool) && c.status != "failed" && c.status != "declined" {
			t.Errorf("a read_only Claude Code session's %s call with the network denied (%s%s) ended %q, want every call "+
				"to a web or shell tool to fail", c.tool, c.path, c.command, c.status)
		}
	}
	r.wantEnforced(t, "read_only", "network", "restrictToWorkspace")
}

// A workspace_write task on the real Claude Code asks the session to write, with the Write tool, one file inside its
// workspace and one outside it. The file inside is written, and no editing tool's call to the file outside succeeds.
// Below Claude Code 2.1.309, the adapter leaves the sandbox off, so Claude Code does not confine its shell to the
// workspace, which the execution's warning for restrictToWorkspace says: a Bash call that wrote the file outside is
// logged as that shell, and only when no Bash call touched it must the file outside be absent. From 2.1.309 the shell
// is confined, which TestWorkspaceWriteRealClaudeCodeSandboxRefusesAShellWriteOutside proves, and the warning is not
// expected.
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
	if version, _ := r.harness["version"].(string); sandboxedClaude(version) {
		t.Logf("Claude Code %s runs with its sandbox on: the shell's warning is not expected", version)
		return
	}
	if !slices.ContainsFunc(r.unenforced, func(u unenforced) bool {
		return u.subject == "restrictToWorkspace" && u.key == "restriction_claude_shell_workspace"
	}) {
		t.Errorf("the execution's unenforced restrictions are %v, want the one for restrictToWorkspace, about the shell",
			r.unenforced)
	}
}

// claudeSandboxMinimum is the oldest Claude Code on which the daemon turns its sandbox on.
const claudeSandboxMinimum = "2.1.309"

// sandboxedClaude reports whether a Claude Code version, dotted numbers, is at least claudeSandboxMinimum.
func sandboxedClaude(version string) bool {
	got, want := strings.Split(version, "."), strings.Split(claudeSandboxMinimum, ".")
	for i := range want {
		var g, w int
		if i < len(got) {
			_, _ = fmt.Sscanf(got[i], "%d", &g)
		}
		_, _ = fmt.Sscanf(want[i], "%d", &w)
		if g != w {
			return g > w
		}
	}
	return version != ""
}

// claudeForTheSandbox is claudeForPermissions on a Mac, where Claude Code's sandbox was checked, with a Claude Code of
// at least claudeSandboxMinimum; an older one fails the test, naming what it needs.
func claudeForTheSandbox(t *testing.T) realHarness {
	t.Helper()
	h := claudeForPermissions(t)
	if runtime.GOOS != "darwin" {
		t.Skipf("Claude Code's sandbox was checked on macOS only, not on %s", runtime.GOOS)
	}
	out, err := exec.Command(h.path, "--version").Output()
	if err != nil {
		t.Fatalf("%s --version: %v", h.path, err)
	}
	version := strings.Fields(string(out))
	if len(version) == 0 || !sandboxedClaude(version[0]) {
		t.Fatalf("Claude Code %q is older than %s, the sandbox's minimum: the test needs %s or later",
			strings.TrimSpace(string(out)), claudeSandboxMinimum, claudeSandboxMinimum)
	}
	return h
}

// A workspace_write task on the real Claude Code, at 2.1.309 or later, asks the session to write, with a shell command,
// one file inside its workspace and one outside it. Claude Code's sandbox, which the adapter turns on, lets the first
// through and refuses the second: the file inside is written, the file outside is absent, and the execution carries no
// warning that the restriction to the workspace is not enforced. The log gives Claude Code's version.
func TestWorkspaceWriteRealClaudeCodeSandboxRefusesAShellWriteOutside(t *testing.T) {
	h := claudeForTheSandbox(t)
	w := newWriteTargets(t)
	r := runWithPermissions(t, h, map[string]any{"profile": "workspace_write", "restrictToWorkspace": true}, w,
		w.prompt("a shell command run with the Bash tool, such as printf"))

	t.Logf("Claude Code %v", r.harness["version"])
	r.wantAttemptAtOutside(t, w)
	w.wantWritten(t)
	wantAbsent(t, w.outside)
	r.wantEnforced(t, "restrictToWorkspace")
}

// privateRepoEnv names the private repository, <owner>/<name>, that TestRealClaudeCodeSandboxReadsAPrivateRepository
// reads.
const privateRepoEnv = "HARMFLOW_TEST_PRIVATE_REPO"

// A workspace_write task on the real Claude Code, at 2.1.309 or later, with the sandbox on, asks the session to read a
// private repository with gh and with git over HTTPS. The daemon passes the signed-in gh's token, masked, and the
// sandbox's proxy puts it in place on GitHub's hosts: both commands succeed, gh naming the repository and git listing
// its HEAD. The token is in no output of the execution, no event, and no file of the daemon's home; the test counts
// it there and never prints it. It needs the variables of the real Claude Code and the real gh, and privateRepoEnv.
func TestRealClaudeCodeSandboxReadsAPrivateRepository(t *testing.T) {
	h := claudeForTheSandbox(t)
	gh := realGh(t)
	repo := os.Getenv(privateRepoEnv)
	if !regexp.MustCompile(`^[\w.-]+/[\w.-]+$`).MatchString(repo) {
		t.Skipf("reads a private repository: set %s=<owner>/<name> to one the signed-in gh can read", privateRepoEnv)
	}
	out, err := exec.Command(gh, "auth", "token").Output()
	token := strings.TrimSpace(string(out))
	if err != nil || token == "" {
		t.Fatalf("gh auth token failed (%v): the test needs a signed-in gh", err)
	}
	w := newWriteTargets(t)
	prompt := "Run these two shell commands with the Bash tool, one at a time, and nothing else:\n\n" +
		"1. gh api repos/" + repo + " --jq .full_name\n" +
		"2. git ls-remote https://github.com/" + repo + ".git HEAD\n\n" +
		"Then report, for each, its exit status and its output."
	r := runWithPermissions(t, h, map[string]any{"profile": "workspace_write", "restrictToWorkspace": true}, w, prompt)

	t.Logf("Claude Code %v", r.harness["version"])
	var api, remote bool
	for _, c := range r.calls {
		switch {
		case c.tool != "Bash" || c.status != "succeeded":
		case strings.Contains(c.command, "gh api") && strings.Contains(strings.ToLower(c.output), strings.ToLower(repo)):
			api = true
		case strings.Contains(c.command, "ls-remote") && regexp.MustCompile(`\b[0-9a-f]{40}\s+HEAD\b`).MatchString(c.output):
			remote = true
		}
	}
	if !api {
		t.Errorf("no gh api call read %s inside the sandbox", repo)
	}
	if !remote {
		t.Errorf("no git ls-remote call listed the HEAD of %s inside the sandbox", repo)
	}
	found := strings.Count(r.output, token) + strings.Count(r.events, token)
	if err := filepath.WalkDir(r.home, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		found += strings.Count(string(data), token)
		return err
	}); err != nil {
		t.Errorf("reading the daemon's home folder: %v", err)
	}
	if found > 0 {
		t.Errorf("the GitHub token appears %d times in the execution's output, events, or files; want none", found)
	}
}

// A workspace_write task on the real Claude Code, whose workspace's project settings name the folder beside it in
// permissions.additionalDirectories, asks the session to write, with the Write tool, one file inside its workspace and
// one in that folder. Claude Code does not add a project settings' entry to the directories where it approves edits:
// the file inside is written, no editing tool's call to the file outside succeeds, that file is absent, and the
// execution carries no warning that the edit directories are not enforced naming the project settings. The log gives
// Claude Code's version and, for each Write call outside, what refused it in the harness's own words. The entry lives
// in the workspace alone, which the test removes when it ends: the user's own settings files keep their bytes.
func TestWorkspaceWriteRealClaudeCodeRefusesAProjectAddedDirectory(t *testing.T) {
	h := claudeForPermissions(t)
	w := newWriteTargets(t)
	user := realHarness{id: "claude-code", config: claudeConfigDir(t), files: []string{"settings.json", "settings.local.json"}}
	before := snapshotConfig(t, user)
	settings := filepath.Join(w.work, ".claude", "settings.json")
	if err := os.Mkdir(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	entry := mustJSON(map[string]any{"permissions": map[string]any{"additionalDirectories": []any{filepath.Dir(w.outside)}}})
	if err := os.WriteFile(settings, []byte(entry), 0o600); err != nil {
		t.Fatal(err)
	}
	r := runWithProfile(t, h, "workspace_write", w, w.prompt("the Write tool"))

	t.Logf("Claude Code %v", r.harness["version"])
	r.wantAttemptAtOutside(t, w)
	w.wantWritten(t)
	wrote := false
	for _, c := range r.calls {
		switch {
		case !c.names(w.outside) || !slices.Contains(editingTools, c.tool):
		case c.status == "succeeded":
			t.Errorf("a workspace_write Claude Code session's %s call to %s succeeded, want the project settings' "+
				"additional directory refused", c.tool, w.outside)
		case c.tool == "Write":
			wrote = true
			why := claudeRefusal(r.output, c.id)
			if len(why) == 0 {
				t.Logf("Claude Code's output says nothing of what refused the Write call %s to %s", c.id, w.outside)
			}
			for _, line := range why {
				t.Logf("what refused the Write call %s: %.1000s", c.id, line)
			}
		}
	}
	if !wrote {
		t.Errorf("the session made no Write call to %s that was refused, so the run shows no refusal", w.outside)
	}
	wantAbsent(t, w.outside)
	if slices.ContainsFunc(r.unenforced, func(u unenforced) bool {
		return u.key == "restriction_claude_edit_rules" && slices.Contains(strings.Split(u.sources, "_"), "project")
	}) {
		t.Errorf("the execution's unenforced restrictions are %v, want none for edit_directories naming the project "+
			"settings", r.unenforced)
	}
	if after := snapshotConfig(t, user); !maps.Equal(after, before) {
		t.Errorf("after the execution, the user's Claude Code settings in %s are %v, want them unchanged, %v",
			user.config, after, before)
	}
}

// claudeRefusal is what Claude Code's JSON output says of the tool call id that it refused, in its own words: the
// reason and the message of its permission_denied notice, and the text of the tool result it gave the session. A call
// it declined carries no output of its own in the call's events, so this is where the refusal is told.
func claudeRefusal(output, id string) []string {
	var why []string
	for line := range strings.Lines(output) {
		var l struct {
			Type           string          `json:"type"`
			Subtype        string          `json:"subtype"`
			ToolUseID      string          `json:"tool_use_id"`
			DecisionReason string          `json:"decision_reason"`
			Message        json.RawMessage `json:"message"`
		}
		if json.Unmarshal([]byte(line), &l) != nil {
			continue
		}
		switch {
		case l.Type == "system" && l.Subtype == "permission_denied" && l.ToolUseID == id:
			var message string
			_ = json.Unmarshal(l.Message, &message)
			why = append(why, fmt.Sprintf("permission_denied: %s: %s", l.DecisionReason, message))
		case l.Type == "user":
			var m struct {
				Content []struct {
					Type      string          `json:"type"`
					ToolUseID string          `json:"tool_use_id"`
					Content   json.RawMessage `json:"content"`
				} `json:"content"`
			}
			if json.Unmarshal(l.Message, &m) != nil {
				continue
			}
			for _, c := range m.Content {
				if c.Type != "tool_result" || c.ToolUseID != id {
					continue
				}
				var text string
				if json.Unmarshal(c.Content, &text) != nil {
					text = string(c.Content)
				}
				why = append(why, "tool result: "+text)
			}
		}
	}
	return why
}

// codexSandboxReport asks a Codex session, before its writes, to print the variables Codex sets on a command it runs in
// its sandbox: Codex's JSON output names no sandbox mode, so the command's output is what logs whether the shell ran
// sandboxed.
const codexSandboxReport = "Before anything else, run this shell command once and report its output: " +
	"printenv CODEX_SANDBOX CODEX_SANDBOX_NETWORK_DISABLED\n\n"

// logSandbox logs what says which sandbox Codex applied: the output of the command codexSandboxReport asks for, and
// Codex's own notices, such as one saying its configuration or requirements changed a setting HarmFlow passed.
func (r permissionRun) logSandbox(t *testing.T) {
	t.Helper()
	for _, c := range r.calls {
		if strings.Contains(c.command, "CODEX_SANDBOX") {
			t.Logf("sandbox report: %s, %s: %q", c.command, c.status, c.output)
		}
	}
	for _, n := range r.notices {
		t.Logf("harness notice: %.500s", n)
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

// netTools are Claude Code's tools that can reach the network: its web tools and its shell.
var netTools = []string{"WebFetch", "WebSearch", "Bash"}

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
// warnings say are not enforced, the harness's notices, its raw output, its events as JSON, the harness as the daemon
// checked it, and the daemon's home folder, where it keeps its records and logs.
type permissionRun struct {
	calls      []toolCall
	unenforced []unenforced
	notices    []string
	output     string
	events     string
	harness    map[string]any
	home       string
}

// runWithProfile runs a task with the profile on the real harness h, its workspace w's, and waits for its end.
func runWithProfile(t *testing.T, h realHarness, profile string, w writeTargets, prompt string) permissionRun {
	t.Helper()
	return runWithPermissions(t, h, map[string]any{"profile": profile}, w, prompt)
}

// runWithPermissions runs a task with the permissions on the real harness h, its workspace w's, and waits for its end.
// The daemon's allowed folders are the workspace alone, so the outside folder is no directory the session is given.
func runWithPermissions(t *testing.T, h realHarness, permissions map[string]any, w writeTargets, prompt string) permissionRun {
	t.Helper()
	home := t.TempDir()
	d := startDaemon(t, home, h.env...)
	harness := d.call("PUT", "/harnesses/"+h.id, map[string]any{"path": h.path}, 200)
	d.call("PATCH", "/settings", map[string]any{"allowedFolders": []any{w.work}}, 200)
	profile, _ := permissions["profile"].(string)
	id := strings.ReplaceAll(profile, "_", "-") + "-" + h.id
	doc := map[string]any{"apiVersion": "harmflow/v1alpha1", "kind": "Task",
		"metadata": map[string]any{"id": id, "name": profile + " on " + h.id},
		"spec": map[string]any{
			"type":        "general",
			"prompt":      prompt,
			"harness":     map[string]any{"preferred": h.preferred},
			"workspace":   map[string]any{"path": w.work},
			"permissions": permissions,
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
	r := permissionRun{calls: toolCalls(events), harness: harness, home: home}
	if raw, err := json.Marshal(events); err == nil {
		r.events = string(raw)
	}
	// A file change Codex made is no tool call: the daemon records it as a change to the workspace alone.
	for _, e := range events {
		switch e["type"] {
		case "workspace.changed":
			t.Logf("workspace change: %v", e["message"])
		case "harness.output":
			if m, _ := e["message"].(string); m != "" {
				r.notices = append(r.notices, m)
			}
		}
	}
	warnings, _ := x["warnings"].([]any)
	for _, raw := range warnings {
		if warning, _ := raw.(map[string]any); warning["code"] == "policy_unenforced" {
			subject, _ := warning["subject"].(string)
			key, _ := warning["messageKey"].(string)
			params, _ := warning["params"].(map[string]any)
			sources, _ := params["sources"].(string)
			r.unenforced = append(r.unenforced, unenforced{subject: subject, key: key, sources: sources})
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

// unenforced is a restriction an execution's policy_unenforced warning names, with the key of its text and the settings
// sources it names, joined by "_" as in "user_project", when it names any.
type unenforced struct{ subject, key, sources string }

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
