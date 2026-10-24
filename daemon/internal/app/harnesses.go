package app

import (
	"context"
	"sort"
	"strings"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/catalog"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/procutil"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/schema"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

type schemaIssue = schema.Issue

// Harnesses returns the inventory in a stable order (FS-004 FR-007). Credentials are never part of it. A harness whose
// first check is still running is checking, never not installed; a later check keeps the previous result until the new
// one lands (FS-004 FR-006, D-102).
func (a *App) Harnesses() []ports.HarnessInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []ports.HarnessInfo{}
	for _, id := range a.adapterIDs() {
		if info, ok := a.inventory[id]; ok {
			out = append(out, info)
		} else {
			ad := a.adapters[id]
			info := ports.HarnessInfo{ID: id, Name: ad.Name(), Authentication: "unknown", Readiness: "checking",
				Installations: []string{}, Models: []ports.ModelInfo{}, Efforts: []string{}, EnforceableControls: []string{}}
			info.SetDiagnostic(domain.T("detection_running", "detection is running"))
			out = append(out, info)
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

// refreshHarnessesAsync detects every harness, each on a goroutine of its own that a.detection counts, and that ends
// with ctx.
func (a *App) refreshHarnessesAsync(ctx context.Context) {
	for _, id := range sortedKeys(a.adapters) {
		a.detection.Go(func() { a.detect(ctx, id) })
	}
}

// detect revalidates one harness and stores what it found, including the resolved path (FS-004 FR-006), then
// announces it, so the dashboard reads the inventory again (FS-004 FR-007). A detection whose ctx ended, or that ends
// once the daemon is stopping, is neither stored nor announced: it was cut short, and the inventory keeps what it had.
func (a *App) detect(ctx context.Context, id string) ports.HarnessInfo {
	ad := a.adapters[id]
	a.mu.Lock()
	manual := a.manualPaths[id]
	a.mu.Unlock()
	info := ad.Detect(ctx, manual)
	info.LastCheckedAt = a.now()
	// Taken only once Detect returns, so a slow check never holds up Stop; see announceMu.
	a.announceMu.RLock()
	defer a.announceMu.RUnlock()
	a.mu.Lock()
	if ctx.Err() != nil || a.stopping {
		a.mu.Unlock()
		return info
	}
	a.inventory[id] = info
	a.mu.Unlock()
	a.emit(domain.Event{Type: domain.EventHarnessChecked, Harness: id, Severity: domain.SeverityInfo,
		Payload: map[string]any{"readiness": info.Readiness}})
	return info
}

func (a *App) harnessInfo(ctx context.Context, id string) ports.HarnessInfo {
	a.mu.Lock()
	info, ok := a.inventory[id]
	a.mu.Unlock()
	if ok {
		return info
	}
	if _, known := a.adapters[id]; !known {
		return ports.HarnessInfo{ID: id}
	}
	return a.detect(ctx, id)
}

// SetHarnessPath chooses an installation or a path set by hand; an empty path returns to detection (FS-004 FR-001). A
// path set by hand must be an executable file whose --version output names the harness, or nothing is saved (D-113).
func (a *App) SetHarnessPath(ctx context.Context, id string, path *string) (ports.HarnessInfo, error) {
	ad, ok := a.adapters[id]
	if !ok {
		return ports.HarnessInfo{}, newError(CodeNotFound, domain.T("harness_not_found", "no harness {{harness}}", "harness", id))
	}
	if path != nil && *path != "" {
		if !strings.HasPrefix(*path, "/") {
			return ports.HarnessInfo{}, newError(CodeValidationFailed,
				domain.T("harness_path_not_absolute", "the path must be absolute"))
		}
		var err error
		if identifier, ok := ad.(ports.HarnessIdentifier); ok {
			err = identifier.Identify(ctx, *path)
		} else if !procutil.Executable(*path) {
			err = domain.TextErr("harness_path_not_executable", "no executable file at {{path}}", "path", *path)
		}
		if err != nil {
			return ports.HarnessInfo{}, &Error{Code: CodeValidationFailed, Text: domain.TextOf(err),
				Validation: fieldIssue("/path", domain.TextOf(err))}
		}
	}
	a.mu.Lock()
	if path == nil || *path == "" {
		delete(a.manualPaths, id)
	} else {
		a.manualPaths[id] = *path
	}
	paths := map[string]string{}
	for k, v := range a.manualPaths {
		paths[k] = v
	}
	a.mu.Unlock()
	if err := a.d.State.Save("harnesses", paths); err != nil {
		a.log.Printf("saving the harness paths set by hand; they hold until the daemon stops: %v", err)
	}
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

// TaskTypeInfo is a task type as the editor offers it: the key of its parameters block in a task's spec, nil for a type
// with none, and the agents, skills, and prompt a new task of the type starts with.
type TaskTypeInfo struct {
	ID            string   `json:"id"`
	ParametersKey *string  `json:"parametersKey"`
	DefaultAgents []string `json:"defaultAgents"`
	DefaultSkills []string `json:"defaultSkills"`
	DefaultPrompt string   `json:"defaultPrompt,omitempty"`
}

// Catalog is what a task can cite: the agents and skills, built in or the user's, and the task types.
type Catalog struct {
	Agents    []CatalogEntry `json:"agents"`
	Skills    []CatalogEntry `json:"skills"`
	TaskTypes []TaskTypeInfo `json:"taskTypes"`
}

// BuiltinPrefix starts the name of every built-in agent or skill, and of no user's one HarmFlow lists.
const BuiltinPrefix = "hf-"

// BuiltinIssueSkills are the built-in skills of the GitHub issue implementation type, the MVP's only built-in set: the
// first carries out the task and uses the others.
var BuiltinIssueSkills = []string{"hf-github-issue-implementer", "hf-github-issue-queue", "hf-github-worktree", "hf-github-draft-pr"}

const defaultIssuePrompt = `Use the skill hf-github-issue-implementer.
Select one open issue labeled agent-ready that is not labeled blocked, oldest first.
Implement it following the repository's conventions and run its tests.
Name the work branch harmflow/<issue-number>-<short-slug> and mark the pull request as managed by HarmFlow with the label harmflow.
Open or update a draft pull request that reports what was implemented, the tests run, what passed and what failed, and what is pending.`

func (a *App) builtinDir() catalog.Dir { return catalog.Dir{Root: a.d.Paths.Builtins, Builtin: true} }

// resolveItem finds an agent or skill by name for a harness: built-ins by the hf- prefix, the user's in the harness
// configuration. where says where it was looked for.
func (a *App) resolveItem(kind, name, harness string) (ports.ResolvedItem, string, bool) {
	if strings.HasPrefix(name, BuiltinPrefix) {
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

// Catalog lists the built-in agents and skills, offered on every harness, then the user's, each with the harnesses
// whose configuration holds it, sorted by name; a user's item named with the hf- prefix is left out.
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
				if strings.HasPrefix(it.Name, BuiltinPrefix) {
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
	key, scriptKey := "githubIssueImplementation", "script"
	c.TaskTypes = []TaskTypeInfo{
		{ID: domain.TypeGitHubIssueImplementation, ParametersKey: &key, DefaultAgents: []string{},
			DefaultSkills: append([]string{}, BuiltinIssueSkills...), DefaultPrompt: defaultIssuePrompt},
		{ID: domain.TypeGeneral, ParametersKey: nil, DefaultAgents: []string{}, DefaultSkills: []string{}},
		{ID: domain.TypeScriptTask, ParametersKey: &scriptKey, DefaultAgents: []string{}, DefaultSkills: []string{}},
	}
	return c
}
