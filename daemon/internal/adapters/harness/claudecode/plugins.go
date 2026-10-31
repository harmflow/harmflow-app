package claudecode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/catalog"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/procutil"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// pluginTimeout bounds one `claude plugin` command; `claude plugin list --json` took about 5 s on a loaded Mac.
const pluginTimeout = 30 * time.Second

// listedPlugin is one plugin of `claude plugin list --json`, as Claude Code 2.1.309 writes it: its ID,
// <name>@<marketplace>, whether the user's settings or the project's of the folder it ran in enable it, where it is
// installed, and what Claude Code reports keeps it from loading.
type listedPlugin struct {
	ID             string   `json:"id"`
	Enabled        bool     `json:"enabled"`
	ProjectEnabled bool     `json:"projectEnabled"`
	InstallPath    string   `json:"installPath"`
	Errors         []string `json:"errors"`
}

// detailsSkills is the line of `claude plugin details` that lists a plugin's skills, its count and then its names, as
// Claude Code 2.1.309 writes it: `  Skills (2)  draft-pr, worktree`, or `  Skills (0)` for none.
var detailsSkills = regexp.MustCompile(`(?m)^[ \t]*Skills \((\d+)\)(.*)$`)

// pluginListing is what the plugins Claude Code has enabled for one workspace hold: their skills, and what Claude Code
// reported it could not load.
type pluginListing struct {
	skills   []ports.ResolvedItem
	problems []ports.DiscoveryError
}

// pluginSkills are the skills of the plugins Claude Code has enabled for workspace, each named <plugin>:<skill> as
// Claude Code names it, in the order of its plugin list and then of each plugin's skills. Claude Code lists them
// itself: `claude plugin list --json`, run in the workspace so that the plugins its project enables count, then
// `claude plugin details` for each plugin enabled. A workspace not made yet has no project settings, so its list is
// asked for in the user's home folder, where only the user's settings count. A CLI that was not detected has none. The
// listing of each folder the list runs in is kept until ReloadUserItems, since reading it starts processes that take
// seconds; one that failed is not kept.
func (a *Adapter) pluginSkills(ctx context.Context, info ports.HarnessInfo, workspace string) ([]ports.ResolvedItem, []ports.DiscoveryError, error) {
	if !info.Detected || info.Path == "" {
		return nil, nil, nil
	}
	dir := workspace
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		dir = a.Home
	}
	key := info.Path + "\x00" + dir
	a.mu.Lock()
	defer a.mu.Unlock()
	if l, ok := a.plugins[key]; ok {
		return l.skills, l.problems, nil
	}
	l, err := a.listPlugins(ctx, info, dir)
	if err != nil {
		return nil, nil, err
	}
	if a.plugins == nil {
		a.plugins = map[string]pluginListing{}
	}
	a.plugins[key] = l
	return l.skills, l.problems, nil
}

// listPlugins asks Claude Code, run in dir, for the plugins it has enabled there and for their skills. A plugin whose
// details Claude Code cannot give, or that it reports it cannot load, is a discovery error, in Claude Code's own words;
// output HarmFlow cannot read fails the listing.
func (a *Adapter) listPlugins(ctx context.Context, info ports.HarnessInfo, dir string) (pluginListing, error) {
	out, stderr, err := procutil.OutputIn(ctx, pluginTimeout, a.Env, dir, info.Path, "plugin", "list", "--json")
	if err != nil {
		return pluginListing{}, pluginCommandFailed("claude plugin list --json", stderr, err)
	}
	var plugins []listedPlugin
	if json.Unmarshal([]byte(out), &plugins) != nil {
		return pluginListing{}, pluginOutputUnrecognized("claude plugin list --json")
	}
	var l pluginListing
	for _, p := range plugins {
		if !p.Enabled && !p.ProjectEnabled {
			continue
		}
		for _, e := range p.Errors {
			l.problems = append(l.problems, ports.DiscoveryError{Path: p.InstallPath, Message: e})
		}
		out, stderr, err := procutil.OutputIn(ctx, pluginTimeout, a.Env, dir, info.Path, "plugin", "details", p.ID)
		if err != nil {
			l.problems = append(l.problems, ports.DiscoveryError{Path: p.InstallPath, Message: ownWords(stderr, err)})
			continue
		}
		names, ok := detailedSkills(out)
		if !ok {
			return pluginListing{}, pluginOutputUnrecognized("claude plugin details " + p.ID)
		}
		plugin, _, _ := strings.Cut(p.ID, "@")
		for _, name := range names {
			l.skills = append(l.skills, pluginSkill(plugin, name, p.InstallPath))
		}
	}
	return l, nil
}

// pluginSkill is the skill name of plugin, installed at root: its folder and description are those of its SKILL.md
// when the plugin keeps one in its skills folder, and the plugin's folder otherwise, as for a command.
func pluginSkill(plugin, name, root string) ports.ResolvedItem {
	it := ports.ResolvedItem{Name: plugin + ":" + name, Kind: "skill", Path: root}
	if filepath.Base(name) != name || name == ".." {
		return it
	}
	if found, ok := (catalog.Dir{Root: root}).Item("skill", name); ok {
		it.Path, it.Description = found.Path, found.Description
	}
	return it
}

// detailedSkills reads the names of a plugin's skills from its `claude plugin details`; false when no line lists them,
// or when the line names another number of skills than it counts.
func detailedSkills(out string) ([]string, bool) {
	m := detailsSkills.FindStringSubmatch(out)
	if m == nil {
		return nil, false
	}
	count, err := strconv.Atoi(m[1])
	if err != nil {
		return nil, false
	}
	var names []string
	for _, s := range strings.Split(m[2], ",") {
		if s = strings.TrimSpace(s); s != "" {
			names = append(names, s)
		}
	}
	return names, len(names) == count
}

// ownWords is what a failed command said, its standard error, or else how it failed.
func ownWords(stderr string, err error) string {
	if s := strings.TrimSpace(stderr); s != "" {
		return s
	}
	return err.Error()
}

func pluginCommandFailed(command, stderr string, err error) error {
	return domain.TextErr("command_failed", "{{command}} failed: {{detail}}", "command", command,
		"detail", ownWords(stderr, err))
}

func pluginOutputUnrecognized(command string) error {
	return domain.TextErr("claude_plugin_output_unrecognized", "{{command}} printed nothing HarmFlow can read",
		"command", command)
}

// ListsPlugins implements ports.PluginLister: a plugin's skills come from Claude Code's list of the plugins it has
// enabled for the workspace, not from a folder.
func (a *Adapter) ListsPlugins(kind string) bool { return kind == "skill" }

// ReloadUserItems implements ports.ItemReloader: the next lookup of a plugin's skills asks Claude Code for its plugins
// again.
func (a *Adapter) ReloadUserItems() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.plugins = nil
}
