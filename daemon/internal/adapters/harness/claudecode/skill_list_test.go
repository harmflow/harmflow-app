package claudecode

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// listingClaude is a stand-in Claude Code for the skill list: each run appends its folder and arguments to the file
// $LOG, one line each, then runs body. Its folder is read with pwd -P, so a test compares it resolved.
func listingClaude(t *testing.T, body string) (path, log string) {
	t.Helper()
	dir := t.TempDir()
	path, log = filepath.Join(dir, "claude"), filepath.Join(dir, "runs.log")
	script := "#!/bin/sh\nprintf '%s|%s\\n' \"$(pwd -P)\" \"$*\" >> \"$LOG\"\n" + body
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("os.WriteFile(%s) = %v, want no error", path, err)
	}
	return path, log
}

// runs are the stand-in's runs so far, each its folder and its arguments.
func runs(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("os.ReadFile(%s) = %v, want no error", log, err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func resolved(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("filepath.EvalSymlinks(%s) = %v, want no error", path, err)
	}
	return real
}

func names(items []ports.ResolvedItem) []string {
	out := []string{}
	for _, it := range items {
		out = append(out, it.Name)
	}
	return out
}

// The skills a task can cite are the ones Claude Code's init line lists for the workspace and the added folders, sorted
// and each once: its bundled skills and a plugin's among them, and a skill on disk it does not list not found. The
// listing session runs in the workspace, as an execution does, with stream-json output and each added folder, and is
// stopped at the init line, before the model would answer. Agents are still read from the configuration directory.
func TestSkillsComeFromTheInitLine(t *testing.T) {
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	writeSettings(t, filepath.Join(config, "agents", "reviewer.md"), "---\nname: reviewer\n---\n")
	marker := filepath.Join(t.TempDir(), "answered")
	path, log := listingClaude(t, `cat > /dev/null
echo '{"type":"system","subtype":"hook_started"}'
echo '{"type":"system","subtype":"init","skills":["simplify","review-notes","pdf:pdf","simplify"]}'
sleep 1
touch "$MARKER"
`)
	ws, added := t.TempDir(), t.TempDir()
	writeSettings(t, filepath.Join(ws, ".claude", "skills", "on-disk", "SKILL.md"), "---\nname: on-disk\n---\n")
	a := &Adapter{Home: t.TempDir(), Env: []string{"PATH=/usr/bin:/bin", "LOG=" + log, "MARKER=" + marker}}
	info := ports.HarnessInfo{Detected: true, Path: path, Features: map[string]bool{"--no-session-persistence": true}}
	ctx := t.Context()

	items, problems, err := a.UserItems(ctx, info, "skill", ws, []string{added})
	if want := []string{"pdf:pdf", "review-notes", "simplify"}; !slices.Equal(names(items), want) || problems != nil || err != nil {
		t.Errorf("UserItems(skill, %s, [%s]) = %q, %v, %v; want %q", ws, added, names(items), problems, err, want)
	}
	want := resolved(t, ws) + "|-p --output-format stream-json --verbose --no-session-persistence --add-dir " + added
	if got := runs(t, log); !slices.Equal(got, []string{want}) {
		t.Errorf("the listing ran as %q, want %q", got, want)
	}
	for _, c := range []struct {
		name  string
		found bool
	}{{"simplify", true}, {"pdf:pdf", true}, {"review-notes", true}, {"on-disk", false}} {
		if _, found, _, err := a.UserItem(ctx, info, "skill", c.name, ws, []string{added}); found != c.found || err != nil {
			t.Errorf("UserItem(skill %s) = %v, %v; want %v", c.name, found, err, c.found)
		}
	}
	if agents, _, err := a.UserItems(ctx, info, "agent", ws, nil); !slices.Equal(names(agents), []string{"reviewer"}) || err != nil {
		t.Errorf("UserItems(agent) = %q, %v; want [reviewer] from the configuration directory", names(agents), err)
	}
	if skill, agent := a.ListsPerWorkspace("skill"), a.ListsPerWorkspace("agent"); !skill || agent {
		t.Errorf("ListsPerWorkspace(skill), ListsPerWorkspace(agent) = %v, %v; want true, false", skill, agent)
	}
	if skill, agent := a.AllowsUnverified("skill"), a.AllowsUnverified("agent"); !skill || agent {
		t.Errorf("AllowsUnverified(skill), AllowsUnverified(agent) = %v, %v; want true, false", skill, agent)
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("the listing session wrote %s after its init line, want it stopped there", marker)
	}
}

// A list is kept per workspace and set of added folders, in any order, so a second lookup starts no session; another
// set of folders, another workspace, and a lookup after ReloadUserItems ask again. A workspace not made yet is listed
// in the user's home folder.
func TestSkillListIsCachedPerWorkspaceAndAddedFolders(t *testing.T) {
	path, log := listingClaude(t, `echo '{"type":"system","subtype":"init","skills":["simplify"]}'`)
	home, ws, other, x, y := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	a := &Adapter{Home: home, Env: []string{"PATH=/usr/bin:/bin", "LOG=" + log}}
	info := ports.HarnessInfo{Detected: true, Path: path}
	ctx := t.Context()
	lookups := []struct {
		workspace string
		added     []string
		runs      int
	}{
		{ws, []string{x, y}, 1},
		{ws, []string{y, x}, 1},
		{ws, []string{x}, 2},
		{other, []string{x}, 3},
		{filepath.Join(ws, "not-made"), nil, 4},
	}
	for _, l := range lookups {
		if _, _, err := a.UserItems(ctx, info, "skill", l.workspace, l.added); err != nil {
			t.Fatalf("UserItems(skill, %s, %q) error %v, want none", l.workspace, l.added, err)
		}
		if got := len(runs(t, log)); got != l.runs {
			t.Errorf("after UserItems(skill, %s, %q), %d listing sessions ran, want %d", l.workspace, l.added, got, l.runs)
		}
	}
	if got := runs(t, log); !strings.HasPrefix(got[len(got)-1], resolved(t, home)+"|") {
		t.Errorf("the listing for a workspace not made ran as %q, want in the home folder %s", got[len(got)-1], home)
	}
	a.ReloadUserItems()
	if _, _, err := a.UserItems(ctx, info, "skill", ws, []string{x, y}); err != nil || len(runs(t, log)) != 5 {
		t.Errorf("UserItems after ReloadUserItems: %v, %d sessions; want a new session", err, len(runs(t, log)))
	}
}

// When Claude Code cannot answer — not detected, ending before its init line, printing an init line with no skill
// list, or reaching the timeout — the lookup fails with an error, never an empty list, and nothing is kept, so the
// next lookup asks again.
func TestSkillListThatClaudeCodeCannotGive(t *testing.T) {
	for _, c := range []struct {
		name, body, wantKey, wantDetail string
		detected                        bool
	}{
		{"not detected", "", "harness_not_detected", "", false},
		{"a failing CLI", "echo 'Not logged in' >&2\nexit 1", "command_failed", "Not logged in", true},
		{"no skill list", `echo '{"type":"system","subtype":"init","model":"x"}'`, "claude_skill_list_unrecognized", "", true},
		{"no init line in time", "sleep 30", "claude_skill_list_timed_out", "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			path, log := listingClaude(t, c.body)
			a := &Adapter{Home: t.TempDir(), Env: []string{"PATH=/usr/bin:/bin", "LOG=" + log},
				skillListTimeout: 3 * time.Second}
			info := ports.HarnessInfo{Detected: c.detected, Path: path}
			for i := range 2 {
				items, _, err := a.UserItems(t.Context(), info, "skill", t.TempDir(), nil)
				text := domain.TextOf(err)
				if items != nil || text.Key != c.wantKey || !strings.Contains(text.Params["detail"], c.wantDetail) {
					t.Errorf("UserItems(skill) #%d = %q, %+v; want no list and %s with %q", i+1, names(items), text,
						c.wantKey, c.wantDetail)
				}
				if _, found, _, err := a.UserItem(t.Context(), info, "skill", "simplify", t.TempDir(), nil); found || err == nil {
					t.Errorf("UserItem(skill simplify) #%d = %v, %v; want not found and an error", i+1, found, err)
				}
			}
			if want := map[bool]int{true: 4, false: 0}[c.detected]; len(runs(t, log)) != want {
				t.Errorf("%d listing sessions ran, want %d: a failed list is not kept", len(runs(t, log)), want)
			}
		})
	}
}
