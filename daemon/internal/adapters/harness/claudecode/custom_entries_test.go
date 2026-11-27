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
		t.Errorf("UserItems(skill) three times, the entry's twice, ran %d listings %q; want two, one for each installation",
			len(got), got)
	}
	data, err := os.ReadFile(marks)
	if err != nil {
		t.Fatalf("os.ReadFile(%s) = %v, want the listings' marks", marks, err)
	}
	if got, want := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n"), []string{"mark=", "mark=entry"}; !slices.Equal(got, want) {
		t.Errorf("UserItems(skill) ran the listings with %q, want %q: the entry's alone with its assignments", got, want)
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
		t.Run(c.name, func(t *testing.T) {
			if it, found, _, err := a.UserItem(ctx, c.info, "skill", "review-notes", ws, nil); !found ||
				it.Path != c.wantSkill || err != nil {
				t.Errorf("UserItem(skill review-notes) = %q, %v, %v; want path %q, found", it.Path, found, err, c.wantSkill)
			}
			if agents, _, err := a.UserItems(ctx, c.info, "agent", ws, nil); !slices.Equal(names(agents), c.wantAgents) ||
				err != nil {
				t.Errorf("UserItems(agent) = %q, %v; want %q", names(agents), err, c.wantAgents)
			}
		})
	}
}

// harmflow-app#826: the long-lived token saved for Claude Code signs in the installation in use, and a custom entry
// that reads the same configuration directory; an entry with a CLAUDE_CONFIG_DIR of its own is another account, run and
// probed on that directory's own sign-in, and an entry's own token assignment is the only token it gets.
func TestTheSavedTokenSignsInOnlyItsOwnAccount(t *testing.T) {
	inUse, otherDir := t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", inUse)
	path := standIn(t, `echo "2.1.239 (Claude Code)"`,
		`if [ "$`+tokenVariable+`" = saved ]; then echo '{"loggedIn": true}'; else echo '{"loggedIn": false, "authMethod": "none"}'; exit 1; fi`)
	a := &Adapter{Home: t.TempDir(), PathEnv: t.TempDir(), KnownLocations: []string{}, Env: []string{"PATH=/usr/bin:/bin"}}
	a.UseToken("saved")
	perm := *domain.TaskDocument{}.Effective().Spec.Permissions
	for _, c := range []struct {
		name        string
		assignments []string
		wantTokens  []string
		wantReady   bool
	}{
		{"no assignments, as the installation in use", nil, []string{"saved"}, true},
		{"an entry on the configuration directory in use", []string{"ENTRY_MARK=on"}, []string{"saved"}, true},
		{"an entry naming the directory in use", []string{"CLAUDE_CONFIG_DIR=" + inUse}, []string{"saved"}, true},
		{"an entry with its own configuration directory", []string{"CLAUDE_CONFIG_DIR=" + otherDir}, nil, false},
		{"an entry with its own token", []string{tokenVariable + "=own"}, []string{"own"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			info := ports.HarnessInfo{Path: path, Features: map[string]bool{}, EntryEnv: c.assignments}
			inv, _, err := a.Prepare(info, ports.RunRequest{WorkDir: t.TempDir(), Permissions: perm,
				Env: []string{"PATH=/usr/bin:/bin"}})
			if err != nil {
				t.Fatalf("Prepare(assignments %q) error = %v, want none", c.assignments, err)
			}
			var tokens []string
			for _, kv := range inv.Env {
				if v, ok := strings.CutPrefix(kv, tokenVariable+"="); ok {
					tokens = append(tokens, v)
				}
			}
			if !slices.Equal(tokens, c.wantTokens) {
				t.Errorf("Prepare(assignments %q) gives the tokens %q, want %q", c.assignments, tokens, c.wantTokens)
			}
			if probed := a.ProbeEntry(t.Context(), path, c.assignments); probed.Ready() != c.wantReady {
				t.Errorf("ProbeEntry(%s, %q) reads readiness %s, want ready %t", path, c.assignments, probed.Readiness,
					c.wantReady)
			}
		})
	}
}

// harmflow-app#826: a custom entry's quota is its own account's: `claude -p "/usage"` runs with the entry's
// assignments, and the plain installation's with none of them.
func TestReadQuotaOfACustomEntryRunsWithItsAssignments(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	path := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\nif [ \"$ENTRY_MARK\" = on ]; then echo 'Current session: 10% used'; else echo 'Current session: 90% used'; fi\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("os.WriteFile(%s) = %v, want no error", path, err)
	}
	a := &Adapter{Home: t.TempDir(), Env: []string{"PATH=/usr/bin:/bin"}}
	for _, c := range []struct {
		name        string
		assignments []string
		wantUsed    float64
	}{
		{"the entry", []string{"ENTRY_MARK=on"}, 10},
		{"the plain installation", nil, 90},
	} {
		t.Run(c.name, func(t *testing.T) {
			q, err := a.ReadQuota(t.Context(), ports.HarnessInfo{Path: path, EntryEnv: c.assignments})
			if err != nil || len(q.Windows) != 1 || q.Windows[0].UsedPercent != c.wantUsed {
				t.Errorf("ReadQuota(assignments %q) = %+v, %v; want one window at %v%%", c.assignments, q, err, c.wantUsed)
			}
		})
	}
}

// harmflow-app#1136: the sign-in of a custom entry starts claude with the entry's assignments added to the daemon's
// environment, each winning over a variable of the same name, the CLAUDE_CONFIG_DIR they set included, so the sign-in
// lands in the entry's configuration directory; the daemon's CODEX_HOME is left out, as for the installation in use.
func TestSignInOfACustomEntryRunsWithItsAssignments(t *testing.T) {
	path := filepath.Join(tempDir(t), "claude")
	// The stand-in writes the environment it was started with into the file ENV_OUT names, one assignment a line, and
	// ends before any screen of the sign-in.
	if err := os.WriteFile(path, []byte("#!/bin/bash\nenv > \"$ENV_OUT\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	inUse, entryDir, out := tempDir(t), tempDir(t), filepath.Join(tempDir(t), "env")
	a := &Adapter{Home: tempDir(t), Env: []string{"PATH=/usr/bin:/bin", "ENV_OUT=" + out,
		"CLAUDE_CONFIG_DIR=" + inUse, "CODEX_HOME=" + tempDir(t), "ENTRY_MARK=off"}}
	assignments := []string{"CLAUDE_CONFIG_DIR=" + entryDir, "ENTRY_MARK=on"}
	info := ports.HarnessInfo{ID: ID, Path: path, Detected: true, Home: entryDir, EntryEnv: slices.Clone(assignments)}
	if res := a.SignIn(t.Context(), info, filepath.Join(tempDir(t), "sign-in")); res.Outcome != ports.SignInEnded {
		t.Fatalf("SignIn(assignments %q) = %+v, want %s once the stand-in exits", assignments, res, ports.SignInEnded)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("os.ReadFile(%s) = %v, want the environment the stand-in was started with", out, err)
	}
	env := map[string][]string{} // the values the environment gives each name, so a name given twice shows twice
	for line := range strings.SplitSeq(strings.TrimSuffix(string(data), "\n"), "\n") {
		if name, value, ok := strings.Cut(line, "="); ok {
			env[name] = append(env[name], value)
		}
	}
	for name, want := range map[string][]string{"CLAUDE_CONFIG_DIR": {entryDir}, "ENTRY_MARK": {"on"}, "CODEX_HOME": nil} {
		if !slices.Equal(env[name], want) {
			t.Errorf("SignIn(assignments %q) started claude with %s = %q, want %q", assignments, name, env[name], want)
		}
	}
}

// harmflow-app#826: the user settings a run's restrictions are checked against are the installation's own: a custom
// entry with a CLAUDE_CONFIG_DIR of its own has its allow rules read from that directory's settings.json, never from
// the configuration directory in use, and the plain installation keeps reading the one in use.
func TestACustomEntrysRestrictionsReadItsOwnUserSettings(t *testing.T) {
	inUse, entryDir := t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", inUse)
	writeSettings(t, filepath.Join(entryDir, "settings.json"), `{"permissions":{"allow":["Bash(*)"]}}`)
	perm := *domain.TaskDocument{}.Effective().Spec.Permissions
	perm.Profile = domain.ProfileCustom
	perm.Custom.AllowedTools = []string{"Read", "Bash(jq:*)"}
	a := &Adapter{Home: t.TempDir()}
	for _, c := range []struct {
		name         string
		assignments  []string
		wantEnforced bool
	}{
		{"an entry with its own configuration directory", []string{"CLAUDE_CONFIG_DIR=" + entryDir}, false},
		{"the plain installation", nil, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			info := ports.HarnessInfo{Path: "/bin/claude", Features: map[string]bool{}, EntryEnv: c.assignments}
			inv, _, err := a.Prepare(info, ports.RunRequest{WorkDir: t.TempDir(), Permissions: perm,
				ExecutionDir: t.TempDir(), PolicyCheck: true})
			if err != nil {
				t.Fatalf("Prepare(assignments %q) error = %v, want none", c.assignments, err)
			}
			r, ok := restriction(inv.Restrictions, "tool_allowlist")
			if !ok || r.Enforced != c.wantEnforced ||
				!c.wantEnforced && (r.Text.Key != "restriction_claude_allow_rules" || r.Text.Params["sources"] != "user") {
				t.Errorf("Prepare(assignments %q) gives tool_allowlist %+v (found %t), want enforced %t, naming the user "+
					"settings when not", c.assignments, r, ok, c.wantEnforced)
			}
		})
	}
}
