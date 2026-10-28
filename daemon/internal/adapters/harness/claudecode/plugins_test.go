package claudecode

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// pluginStandIn writes a stand-in for the claude CLI that appends the folder it runs in and its arguments to calls, then
// runs the given case arms on "$1 $2".
func pluginStandIn(t *testing.T, calls, arms string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/bash\necho \"$(pwd -P) $*\" >> '" + calls + "'\ncase \"$1 $2\" in\n" + arms + "\nesac\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// realPath is path with its symbolic links resolved, as `pwd -P` prints it.
func realPath(t *testing.T, path string) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// pluginCalls are the plugin commands the stand-in ran, each the folder it ran in and its arguments.
func pluginCalls(t *testing.T, calls string) []string {
	t.Helper()
	data, err := os.ReadFile(calls)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// errKey is the message key of err, empty for no error.
func errKey(err error) string {
	if err == nil {
		return ""
	}
	return domain.TextOf(err).Key
}

func itemNames(items []ports.ResolvedItem) []string {
	var names []string
	for _, it := range items {
		names = append(names, it.Name)
	}
	return names
}

// A plugin's skills are the ones Claude Code's own plugin commands list for the workspace, each named
// `<plugin>:<skill>` as Claude Code names it, uppercase letters included: the plugins its user settings enable, and
// those its project enables when the list runs in that workspace, never a disabled one. A skill the plugin keeps in its
// skills folder has that folder and its description; another, such as a command, has the plugin's folder. What the list
// reports an enabled plugin cannot load is a discovery error. The listing is read once per workspace until
// ReloadUserItems, and a workspace not made yet is listed from the home folder.
func TestPluginSkillsComeFromClaudeCodesPluginCommands(t *testing.T) {
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	writeSettings(t, filepath.Join(config, "skills", "review-notes", "SKILL.md"), "---\nname: review-notes\ndescription: Mine.\n---\n")
	home, ws, other, pdf := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	writeSettings(t, filepath.Join(pdf, "skills", "pdf", "SKILL.md"), "---\nname: pdf\ndescription: Fills PDFs.\n---\n")
	calls := filepath.Join(t.TempDir(), "calls")
	cli := pluginStandIn(t, calls, `  "plugin list")
    project=false
    [ "$(pwd -P)" = '`+realPath(t, ws)+`' ] && project=true
    echo '[{"id":"pdf@market","enabled":true,"projectEnabled":false,"installPath":"`+pdf+`","errors":["a hook failed"]},'
    echo ' {"id":"off@market","enabled":false,"projectEnabled":false,"installPath":"/off","errors":["blocked"]},'
    echo ' {"id":"proj@market","enabled":false,"projectEnabled":'$project',"installPath":"/proj"}]' ;;
  "plugin details")
    case "$3" in
      pdf@market) printf 'pdf 1.0.0\n\nComponent inventory\n  Skills (2)  pdf, Forms\n  Agents (1)  helper\n' ;;
      proj@market) printf 'proj\n\nComponent inventory\n  Skills (1)  lint\n  Agents (0)\n' ;;
      *) printf 'Plugin "%s" not found.\n' "$3" >&2; exit 1 ;;
    esac ;;`)
	a := &Adapter{Home: home}
	info := ports.HarnessInfo{Detected: true, Path: cli}
	ctx := t.Context()

	for _, c := range []struct {
		name, workspace string
		want            []string
	}{
		{"the workspace whose project enables a plugin", ws, []string{"pdf:Forms", "pdf:pdf", "proj:lint", "review-notes"}},
		{"another workspace", other, []string{"pdf:Forms", "pdf:pdf", "review-notes"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			items, problems, err := a.UserItems(ctx, info, "skill", c.workspace)
			want := []ports.DiscoveryError{{Path: pdf, Message: "a hook failed"}}
			if got := itemNames(items); !slices.Equal(got, c.want) || !slices.Equal(problems, want) || err != nil {
				t.Errorf("UserItems(skill, %s) = %q, %v, %v; want %q, %v", c.workspace, got, problems, err, c.want, want)
			}
		})
	}

	for _, c := range []struct {
		name, skill, workspace, wantPath, wantDescription string
	}{
		{"a skill of its skills folder", "pdf:pdf", other, filepath.Join(pdf, "skills", "pdf"), "Fills PDFs."},
		{"a skill named in caps, with no folder", "pdf:Forms", other, pdf, ""},
		{"a plugin its project enables, in that project", "proj:lint", ws, "/proj", ""},
		{"a plugin its project enables, elsewhere", "proj:lint", other, "", ""},
		{"a disabled plugin", "off:hidden", ws, "", ""},
		{"a skill the plugin does not have", "pdf:missing", ws, "", ""},
		{"a plugin's name without its skill's", "pdf", ws, "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			it, found, _, err := a.UserItem(ctx, info, "skill", c.skill, c.workspace)
			if found != (c.wantPath != "") || it.Path != c.wantPath || it.Description != c.wantDescription || err != nil {
				t.Errorf("UserItem(skill %s, %s) = %+v, %v, %v; want found at %q, described %q", c.skill, c.workspace, it,
					found, err, c.wantPath, c.wantDescription)
			}
		})
	}

	lists := func() []string {
		var out []string
		for _, c := range pluginCalls(t, calls) {
			if strings.HasSuffix(c, " plugin list --json") {
				out = append(out, strings.TrimSuffix(c, " plugin list --json"))
			}
		}
		return out
	}
	if got, want := lists(), []string{realPath(t, ws), realPath(t, other)}; !slices.Equal(got, want) {
		t.Errorf("the plugin list ran in %q, want once in each workspace, %q", got, want)
	}
	a.ReloadUserItems()
	if _, _, err := a.UserItems(ctx, info, "skill", ws); err != nil || len(lists()) != 3 {
		t.Errorf("UserItems(skill, %s) after ReloadUserItems = %v, ran the plugin list %d times; want it run again, 3",
			ws, err, len(lists()))
	}
	if _, _, err := a.UserItems(ctx, info, "skill", filepath.Join(ws, "not-yet")); err != nil {
		t.Errorf("UserItems(skill, a workspace not made yet) = %v, want no error", err)
	}
	if _, _, err := a.UserItems(ctx, info, "skill", filepath.Join(ws, "not-yet-either")); err != nil {
		t.Errorf("UserItems(skill, another workspace not made yet) = %v, want no error", err)
	}
	if got := lists(); len(got) != 4 || got[3] != realPath(t, home) {
		t.Errorf("the plugin list for two workspaces not made yet ran in %q, want once, in the home folder %q", got,
			realPath(t, home))
	}
	if a.ListsPlugins("skill") != true || a.ListsPlugins("agent") != false {
		t.Errorf("ListsPlugins(skill), ListsPlugins(agent) = %v, %v; want true, false", a.ListsPlugins("skill"),
			a.ListsPlugins("agent"))
	}
}

// A CLI that was not detected lists no plugin: the other skills are listed with no error, and a plugin's skill cannot be
// looked up, which is said so, never a missing skill.
func TestPluginSkillsWithNoCLI(t *testing.T) {
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	writeSettings(t, filepath.Join(config, "skills", "review-notes", "SKILL.md"), "---\nname: review-notes\n---\n")
	a := &Adapter{Home: t.TempDir()}
	items, problems, err := a.UserItems(t.Context(), ports.HarnessInfo{}, "skill", t.TempDir())
	if got := itemNames(items); !slices.Equal(got, []string{"review-notes"}) || problems != nil || err != nil {
		t.Errorf("UserItems(skill) with no CLI = %q, %v, %v; want [review-notes], no problem, no error", got, problems, err)
	}
	_, found, _, err := a.UserItem(t.Context(), ports.HarnessInfo{}, "skill", "pdf:pdf", t.TempDir())
	if found || errKey(err) != "harness_not_detected" {
		t.Errorf("UserItem(skill pdf:pdf) with no CLI = %v, %v; want not found, harness_not_detected", found, err)
	}
}

// A plugin list that cannot be read is the lookup's error, beside the other skills, and is asked for again on the next
// lookup; output HarmFlow cannot read is said so; a plugin whose details fail is a discovery error in Claude Code's
// words, and the other plugins' skills are still listed.
func TestPluginListThatCannotBeRead(t *testing.T) {
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	writeSettings(t, filepath.Join(config, "skills", "review-notes", "SKILL.md"), "---\nname: review-notes\n---\n")
	enabled := `echo '[{"id":"pdf@market","enabled":true,"installPath":"/pdf"},{"id":"gone@market","enabled":true,"installPath":"/gone"}]'`
	for _, c := range []struct {
		name, arms, wantKey string
		wantNames           []string
		wantProblems        []ports.DiscoveryError
	}{
		{"a list that fails", `  "plugin list") echo 'cannot reach the marketplace' >&2; exit 1 ;;`, "command_failed",
			[]string{"review-notes"}, nil},
		{"a list that is not JSON", `  "plugin list") echo 'No plugins installed' ;;`, "claude_plugin_output_unrecognized",
			[]string{"review-notes"}, nil},
		{"details with no skill line", `  "plugin list") ` + enabled + ` ;;
  "plugin details") echo 'pdf 1.0.0' ;;`, "claude_plugin_output_unrecognized", []string{"review-notes"}, nil},
		{"details that count other skills than they name", `  "plugin list") ` + enabled + ` ;;
  "plugin details") printf '  Skills (3)  pdf, forms\n' ;;`, "claude_plugin_output_unrecognized", []string{"review-notes"}, nil},
		{"details of one plugin that fail", `  "plugin list") ` + enabled + ` ;;
  "plugin details")
    [ "$3" = gone@market ] && { echo 'Plugin "gone@market" not found.' >&2; exit 1; }
    printf '  Skills (1)  pdf\n' ;;`, "", []string{"pdf:pdf", "review-notes"},
			[]ports.DiscoveryError{{Path: "/gone", Message: `Plugin "gone@market" not found.`}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			calls := filepath.Join(t.TempDir(), "calls")
			a := &Adapter{Home: t.TempDir()}
			info := ports.HarnessInfo{Detected: true, Path: pluginStandIn(t, calls, c.arms)}
			ws := t.TempDir()
			items, problems, err := a.UserItems(t.Context(), info, "skill", ws)
			if got := itemNames(items); !slices.Equal(got, c.wantNames) || !slices.Equal(problems, c.wantProblems) ||
				errKey(err) != c.wantKey {
				t.Errorf("UserItems(skill) = %q, %v, %v; want %q, %v, key %q", got, problems, err, c.wantNames,
					c.wantProblems, c.wantKey)
			}
			_, _, _, err = a.UserItem(t.Context(), info, "skill", "pdf:pdf", ws)
			if errKey(err) != c.wantKey {
				t.Errorf("UserItem(skill pdf:pdf) error %v, want key %q", err, c.wantKey)
			}
			if c.wantKey == "" {
				return
			}
			var lists int
			for _, call := range pluginCalls(t, calls) {
				if strings.HasSuffix(call, " plugin list --json") {
					lists++
				}
			}
			if lists != 2 {
				t.Errorf("the plugin list ran %d times for two lookups, want 2: a failed listing is not kept", lists)
			}
		})
	}
}

// The details' skill line is read by its count: none, some, or a line that names another number than it counts.
func TestDetailedSkills(t *testing.T) {
	for _, c := range []struct {
		out  string
		want []string
		ok   bool
	}{
		{"harmflow 0.1.0\n\nComponent inventory\n  Skills (3)  a, b-c, Docs\n  Agents (0)\n", []string{"a", "b-c", "Docs"}, true},
		{"x\n  Skills (2)  pdf, Fill Forms\n", []string{"pdf", "Fill Forms"}, true},
		{"x\n  Skills (0)\n  Agents (1)  helper\n", nil, true},
		{"x\n  Skills (2)  a\n", []string{"a"}, false},
		{"x\n  Agents (1)  helper\n", nil, false},
	} {
		got, ok := detailedSkills(c.out)
		if !slices.Equal(got, c.want) || ok != c.ok {
			t.Errorf("detailedSkills(%q) = %q, %v; want %q, %v", c.out, got, ok, c.want, c.ok)
		}
	}
}
