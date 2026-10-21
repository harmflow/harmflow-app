package app

import (
	"context"
	"os"
	"sort"
	"strings"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/catalog"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/schema"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

type schemaIssue = schema.Issue

// Harnesses returns the inventory in a stable order (FS-004 FR-007). Credentials are never part of it.
func (a *App) Harnesses() []ports.HarnessInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []ports.HarnessInfo{}
	for _, id := range a.adapterIDs() {
		if info, ok := a.inventory[id]; ok {
			out = append(out, info)
		} else {
			ad := a.adapters[id]
			out = append(out, ports.HarnessInfo{ID: id, Name: ad.Name(), Authentication: "unknown", Readiness: "not_installed",
				Installations: []string{}, Models: []ports.ModelInfo{}, Efforts: []string{}, EnforceableControls: []string{},
				Diagnostic: "detection is running"})
		}
	}
	return out
}

func (a *App) adapterIDs() []string {
	ids := sortedKeys(a.adapters)
	// Claude Code first, as the dashboard lists it.
	sort.SliceStable(ids, func(i, j int) bool { return ids[i] == domain.HarnessClaudeCode && ids[j] != domain.HarnessClaudeCode })
	return ids
}

// RefreshHarnesses detects every harness again.
func (a *App) RefreshHarnesses(ctx context.Context) []ports.HarnessInfo {
	for _, id := range sortedKeys(a.adapters) {
		a.detect(ctx, id)
	}
	return a.Harnesses()
}

func (a *App) refreshHarnessesAsync() {
	for _, id := range sortedKeys(a.adapters) {
		go a.detect(context.Background(), id)
	}
}

// detect revalidates one harness and stores what it found, including the resolved path (FS-004 FR-006).
func (a *App) detect(ctx context.Context, id string) ports.HarnessInfo {
	ad := a.adapters[id]
	a.mu.Lock()
	manual := a.manualPaths[id]
	a.mu.Unlock()
	info := ad.Detect(ctx, manual)
	info.LastCheckedAt = a.now()
	a.mu.Lock()
	a.inventory[id] = info
	a.mu.Unlock()
	return info
}

func (a *App) harnessInfo(id string) ports.HarnessInfo {
	a.mu.Lock()
	info, ok := a.inventory[id]
	a.mu.Unlock()
	if ok {
		return info
	}
	if _, known := a.adapters[id]; !known {
		return ports.HarnessInfo{ID: id}
	}
	return a.detect(context.Background(), id)
}

// SetHarnessPath chooses an installation or a path set by hand; an empty path returns to detection (FS-004 FR-001).
func (a *App) SetHarnessPath(ctx context.Context, id string, path *string) (ports.HarnessInfo, error) {
	if _, ok := a.adapters[id]; !ok {
		return ports.HarnessInfo{}, newError(CodeNotFound, domain.T("harness_not_found", "no harness {{harness}}", "harness", id))
	}
	a.mu.Lock()
	if path == nil || *path == "" {
		delete(a.manualPaths, id)
	} else {
		if !strings.HasPrefix(*path, "/") {
			a.mu.Unlock()
			return ports.HarnessInfo{}, newError(CodeValidationFailed,
				domain.T("harness_path_not_absolute", "the path must be absolute"))
		}
		if st, err := os.Stat(*path); err != nil || st.IsDir() {
			a.mu.Unlock()
			return ports.HarnessInfo{}, newError(CodeValidationFailed,
				domain.T("harness_path_not_executable", "no executable file at {{path}}", "path", *path))
		}
		a.manualPaths[id] = *path
	}
	paths := map[string]string{}
	for k, v := range a.manualPaths {
		paths[k] = v
	}
	a.mu.Unlock()
	_ = a.d.State.Save("harnesses", paths)
	return a.detect(ctx, id), nil
}

// Tools reports git and gh, which the built-in GitHub skills use.
func (a *App) Tools(ctx context.Context) []ports.ToolInfo {
	if a.d.Tools == nil {
		return []ports.ToolInfo{}
	}
	return a.d.Tools.Probe(ctx)
}

// CatalogEntry is an agent or skill a task can cite.
type CatalogEntry struct {
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Source      string   `json:"source"`
	Description string   `json:"description,omitempty"`
	Harnesses   []string `json:"harnesses"`
}

type TaskTypeInfo struct {
	ID            string   `json:"id"`
	ParametersKey *string  `json:"parametersKey"`
	DefaultAgents []string `json:"defaultAgents"`
	DefaultSkills []string `json:"defaultSkills"`
	DefaultPrompt string   `json:"defaultPrompt,omitempty"`
}

type Catalog struct {
	Agents    []CatalogEntry `json:"agents"`
	Skills    []CatalogEntry `json:"skills"`
	TaskTypes []TaskTypeInfo `json:"taskTypes"`
}

// Built-in agents and skills of the GitHub issue implementation type, the MVP's only built-in set (D-092).
var (
	BuiltinIssueAgent  = "harmflow-github-issue-implementer"
	BuiltinIssueSkills = []string{"harmflow-github-issue-queue", "harmflow-github-worktree", "harmflow-github-draft-pr"}
)

const defaultIssuePrompt = `Select one open issue labeled agent-ready that is not labeled blocked, oldest first.
Implement it following the repository's conventions and run its tests.
Name the work branch harmflow/<issue-number>-<short-slug> and mark the pull request as managed by HarmFlow with the label harmflow.
Open or update a draft pull request that reports what was implemented, the tests run, what passed and what failed, and what is pending.`

func (a *App) builtinDir() catalog.Dir { return catalog.Dir{Root: a.d.Paths.Builtins, Builtin: true} }

// resolveItem finds an agent or skill by name for a harness: built-ins by the harmflow- prefix, the user's in the
// harness configuration (D-092). where says where it was looked for.
func (a *App) resolveItem(kind, name, harness string) (ports.ResolvedItem, string, bool) {
	if strings.HasPrefix(name, "harmflow-") {
		it, ok := a.builtinDir().Item(kind, name)
		return it, "built in", ok
	}
	ad, ok := a.adapters[harness]
	if !ok {
		return ports.ResolvedItem{}, "unknown harness", false
	}
	it, found := ad.UserItem(kind, name)
	where := "the " + ad.Name() + " configuration"
	return it, where, found
}

func (a *App) Catalog() Catalog {
	c := Catalog{Agents: []CatalogEntry{}, Skills: []CatalogEntry{}}
	all := sortedKeys(a.adapters)
	for _, kind := range []string{"agent", "skill"} {
		entries := map[string]*CatalogEntry{}
		for _, it := range a.builtinDir().Items(kind) {
			entries[it.Name] = &CatalogEntry{Name: it.Name, Kind: kind, Source: "builtin", Description: it.Description,
				Harnesses: all}
		}
		for _, id := range all {
			for _, it := range a.adapters[id].UserItems(kind) {
				if strings.HasPrefix(it.Name, "harmflow-") {
					continue
				}
				e := entries[it.Name]
				if e == nil {
					e = &CatalogEntry{Name: it.Name, Kind: kind, Source: "user", Description: it.Description, Harnesses: []string{}}
					entries[it.Name] = e
				}
				e.Harnesses = append(e.Harnesses, id)
			}
		}
		for _, name := range sortedKeys(entries) {
			if kind == "agent" {
				c.Agents = append(c.Agents, *entries[name])
			} else {
				c.Skills = append(c.Skills, *entries[name])
			}
		}
	}
	key := "githubIssueImplementation"
	c.TaskTypes = []TaskTypeInfo{
		{ID: domain.TypeGitHubIssueImplementation, ParametersKey: &key, DefaultAgents: []string{BuiltinIssueAgent},
			DefaultSkills: append([]string{}, BuiltinIssueSkills...), DefaultPrompt: defaultIssuePrompt},
		{ID: domain.TypeGeneral, ParametersKey: nil, DefaultAgents: []string{}, DefaultSkills: []string{}},
	}
	return c
}
