package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// harmflow-app#826: a custom entry is probed as a path set by hand, every check run with the entry's assignments added
// to the environment, so a CLI signed in only under them reads ready. The result names the entry's binary alone and
// keeps its assignments, and its home is the CLAUDE_CONFIG_DIR they set, or the configuration directory in use when
// they set none. The adapter it was probed from keeps its own environment.
func TestProbeEntryRunsEachCheckWithItsAssignments(t *testing.T) {
	path := standIn(t, `echo "2.1.239 (Claude Code)"`,
		`if [ "$ENTRY_MARK" = on ]; then echo '{"loggedIn": true}'; else echo '{"loggedIn": false, "authMethod": "none"}'; exit 1; fi`)
	inUse, entryDir := t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", inUse)
	env := []string{"PATH=/usr/bin:/bin"}
	a := &Adapter{Home: t.TempDir(), PathEnv: t.TempDir(), KnownLocations: []string{}, Env: slices.Clone(env)}
	for _, c := range []struct {
		name          string
		assignments   []string
		wantHome      string
		wantReadiness string
	}{
		{"its own configuration directory", []string{"CLAUDE_CONFIG_DIR=" + entryDir, "ENTRY_MARK=on"}, entryDir, "ready"},
		{"the configuration directory in use", []string{"ENTRY_MARK=on"}, inUse, "ready"},
		{"an empty configuration directory", []string{"CLAUDE_CONFIG_DIR=", "ENTRY_MARK=on"}, inUse, "ready"},
		{"signed in only under other assignments", []string{"ENTRY_MARK=off"}, inUse, "not_ready"},
	} {
		t.Run(c.name, func(t *testing.T) {
			info := a.ProbeEntry(context.Background(), path, c.assignments)
			if real := resolved(t, path); !info.Detected || info.Path != real || info.Readiness != c.wantReadiness ||
				!slices.Equal(info.Installations, []string{path}) {
				t.Errorf("ProbeEntry(%s, %q): detected %v, path %q, readiness %s, installations %q; want detected, %q, "+
					"%s, [%q]", path, c.assignments, info.Detected, info.Path, info.Readiness, info.Installations, real,
					c.wantReadiness, path)
			}
			if info.Home != c.wantHome || !slices.Equal(info.EntryEnv, c.assignments) {
				t.Errorf("ProbeEntry(%s, %q): home %q, assignments %q; want %q, %q", path, c.assignments, info.Home,
					info.EntryEnv, c.wantHome, c.assignments)
			}
		})
	}
	if !slices.Equal(a.Env, env) || a.entryConfigDir != "" {
		t.Errorf("after ProbeEntry the adapter has environment %q and configuration directory %q; want %q and none",
			a.Env, a.entryConfigDir, env)
	}
}

// harmflow-app#826: an attempt on a custom entry runs the entry's arguments before the adapter's own, with its
// assignments added after the run's environment, so each wins over a variable of the same name; an installation with
// no entry runs as before.
func TestPrepareRunsACustomEntryWithItsArgumentsAndAssignments(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	a := &Adapter{Home: t.TempDir()}
	perm := *domain.TaskDocument{}.Effective().Spec.Permissions
	entryArgs := []string{"--settings", "/opt/entry/settings.json"}
	entryEnv := []string{"CLAUDE_CONFIG_DIR=/opt/entry", "ENTRY_MARK=on"}
	runEnv := []string{"PATH=/usr/bin:/bin", "ENTRY_MARK=off"}
	for _, c := range []struct {
		name      string
		info      ports.HarnessInfo
		wantFirst []string
		wantEnv   []string
	}{
		{"a custom entry", ports.HarnessInfo{Path: "/bin/claude", Features: map[string]bool{}, EntryArgs: entryArgs,
			EntryEnv: entryEnv}, append(slices.Clone(entryArgs), "-p"), append(slices.Clone(runEnv), entryEnv...)},
		{"no entry", ports.HarnessInfo{Path: "/bin/claude", Features: map[string]bool{}}, []string{"-p"}, runEnv},
	} {
		t.Run(c.name, func(t *testing.T) {
			inv, _, err := a.Prepare(c.info, ports.RunRequest{WorkDir: t.TempDir(), Permissions: perm,
				Env: slices.Clone(runEnv)})
			if err != nil {
				t.Fatalf("Prepare() error %v, want none", err)
			}
			if len(inv.Args) < len(c.wantFirst) || !slices.Equal(inv.Args[:len(c.wantFirst)], c.wantFirst) {
				t.Errorf("Prepare() args %q, want them to start with %q", inv.Args, c.wantFirst)
			}
			if !slices.Equal(inv.Env, c.wantEnv) {
				t.Errorf("Prepare() environment %q, want %q", inv.Env, c.wantEnv)
			}
		})
	}
}

// harmflow-app#826: a custom entry is another installation of the same CLI. Its skill list comes from a listing of its
// own, run with its assignments and kept apart from the plain installation's; a listed skill of the user's own and the
// user's agents are read from the CLAUDE_CONFIG_DIR its assignments set, never from the configuration directory in use.
func TestACustomEntryListsItsOwnSkillsAndAgents(t *testing.T) {
	inUse, entryDir := t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", inUse)
	writeSettings(t, filepath.Join(inUse, "agents", "daemon-agent.md"), "---\nname: daemon-agent\n---\n")
	writeSettings(t, filepath.Join(entryDir, "agents", "reviewer.md"), "---\nname: reviewer\n---\n")
	writeSettings(t, filepath.Join(entryDir, "skills", "review-notes", "SKILL.md"), "---\nname: review-notes\n---\n")
	marks := filepath.Join(t.TempDir(), "marks.log")
	path, log := listingClaude(t, `echo "mark=$ENTRY_MARK" >> "$MARKS"
echo '{"type":"system","subtype":"init","skills":["review-notes"]}'`)
	a := &Adapter{Home: t.TempDir(), Env: []string{"PATH=/usr/bin:/bin", "LOG=" + log, "MARKS=" + marks}}
	plain := ports.HarnessInfo{Detected: true, Path: path}
	entry := ports.HarnessInfo{Detected: true, Path: path,
		EntryEnv: []string{"CLAUDE_CONFIG_DIR=" + entryDir, "ENTRY_MARK=entry"}}
	ctx, ws := t.Context(), t.TempDir()

	for _, info := range []ports.HarnessInfo{plain, entry, entry} {
		if _, _, err := a.UserItems(ctx, info, "skill", ws, nil); err != nil {
			t.Fatalf("UserItems(skill, assignments %q) error %v, want none", info.EntryEnv, err)
		}
	}
	if got := runs(t, log); len(got) != 2 {
		t.Errorf("three lookups, the entry's twice, ran %d listings %q; want two, one for each installation", len(got), got)
	}
	data, err := os.ReadFile(marks)
	if err != nil {
		t.Fatalf("os.ReadFile(%s) = %v, want the listings' marks", marks, err)
	}
	if got, want := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n"), []string{"mark=", "mark=entry"}; !slices.Equal(got, want) {
		t.Errorf("the listings ran with %q, want %q: the entry's alone with its assignments", got, want)
	}
	for _, c := range []struct {
		name       string
		info       ports.HarnessInfo
		wantSkill  string
		wantAgents []string
	}{
		{"the entry", entry, filepath.Join(entryDir, "skills", "review-notes"), []string{"reviewer"}},
		{"the plain installation", plain, "", []string{"daemon-agent"}},
	} {
		if it, found, _, err := a.UserItem(ctx, c.info, "skill", "review-notes", ws, nil); !found || it.Path != c.wantSkill ||
			err != nil {
			t.Errorf("%s: UserItem(skill review-notes) = %q, %v, %v; want path %q, found", c.name, it.Path, found, err,
				c.wantSkill)
		}
		if agents, _, err := a.UserItems(ctx, c.info, "agent", ws, nil); !slices.Equal(names(agents), c.wantAgents) ||
			err != nil {
			t.Errorf("%s: UserItems(agent) = %q, %v; want %q", c.name, names(agents), err, c.wantAgents)
		}
	}
}
