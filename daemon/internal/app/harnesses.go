package app

import (
	"context"
	"path/filepath"
	"sort"
	"strings"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/catalog"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/procutil"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/schema"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

type schemaIssue = schema.Issue

// Harnesses returns the inventory in a stable order. Credentials are never part of it. A harness whose first check is
// still running is checking, never not installed; a later check keeps the previous result until the new one lands.
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

// RefreshHarnesses detects every harness again, and has the harnesses that cache the user's items read them again.
func (a *App) RefreshHarnesses(ctx context.Context) []ports.HarnessInfo {
	for _, id := range sortedKeys(a.adapters) {
		if r, ok := a.adapters[id].(ports.ItemReloader); ok {
			r.ReloadUserItems()
		}
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

// detect revalidates one harness and stores what it found, including the resolved path, then announces it, so the
// dashboard reads the inventory again. A detection whose ctx ended, or that ends once the daemon is stopping, is
// neither stored nor announced: it was cut short, and the inventory keeps what it had.
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

// SetHarnessPath chooses an installation or a path set by hand; an empty path returns to detection. A
// path set by hand must be an executable file whose --version output names the harness, or nothing is saved.
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

// CatalogEntry is an agent or skill a task can cite. An agent whose file reads carries what selecting it copies.
type CatalogEntry struct {
	Name        string     `json:"name"`
	Kind        string     `json:"kind"`
	Source      string     `json:"source"`
	Description string     `json:"description,omitempty"`
	Harnesses   []string   `json:"harnesses"`
	Copy        *AgentCopy `json:"copy,omitempty"`
}

// AgentCopy is what selecting an agent copies into a task, the harnesses its model is valid for, the front-matter
// fields left out, and the hash the task keeps as its origin.
type AgentCopy struct {
	Prompt         string   `json:"prompt"`
	Description    string   `json:"description,omitempty"`
	Model          string   `json:"model,omitempty"`
	ModelHarnesses []string `json:"modelHarnesses"`
	Skills         []string `json:"skills"`
	OmittedFields  []string `json:"omittedFields"`
	Hash           string   `json:"hash"`
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

// Catalog is what a task can cite: the agents and skills, built in or the user's, and the task types; Problems are what
// a harness could not list.
type Catalog struct {
	Agents    []CatalogEntry   `json:"agents"`
	Skills    []CatalogEntry   `json:"skills"`
	TaskTypes []TaskTypeInfo   `json:"taskTypes"`
	Problems  []CatalogProblem `json:"problems"`
}

// BuiltinPrefix starts the name of every built-in agent or skill, and of no user's one HarmFlow lists.
const BuiltinPrefix = "hf-"

// BuiltinIssueSkills are the built-in skills of the issue implementer, the MVP's only built-in set, which a new GitHub
// task selects by default and need not keep: the first carries out the task and uses the others.
var BuiltinIssueSkills = []string{"hf-github-issue-implementer", "hf-github-issue-queue", "hf-github-worktree", "hf-github-draft-pr"}

const defaultIssuePrompt = `Use the skill hf-github-issue-implementer.
Select one open issue labeled agent-ready that is not labeled blocked, oldest first.
Implement it following the repository's conventions and run its tests.
Name the work branch harmflow/<issue-number>-<short-slug> and mark the pull request as managed by HarmFlow with the label harmflow.
Open or update a draft pull request that reports what was implemented, the tests run, what passed and what failed, and what is pending.`

func (a *App) builtinDir() catalog.Dir { return catalog.Dir{Root: a.d.Paths.Builtins, Builtin: true} }

// itemLookup is what looking an agent or skill up for a harness found: the item, or where it was looked for and what
// the harness reported it could not load on the way, or that the harness's list could not be read.
type itemLookup struct {
	item       ports.ResolvedItem
	found      bool
	missing    missingItem
	unreadable error // the harness's list could not be read
}

// resolveItem finds an agent or skill by name for a harness: built-ins by the hf- prefix, the user's as the harness
// finds them for workspace.
func (a *App) resolveItem(ctx context.Context, kind, name, harness, workspace string) itemLookup {
	m := missingItem{kind: kind, name: name, source: "harness"}
	if strings.HasPrefix(name, BuiltinPrefix) {
		it, ok := a.builtinDir().Item(kind, name)
		m.where, m.source = "built in", "builtin"
		return itemLookup{item: it, found: ok, missing: m}
	}
	ad, ok := a.adapters[harness]
	if !ok {
		m.where = "unknown harness"
		return itemLookup{missing: m}
	}
	m.where = "the " + ad.Name() + " configuration"
	if l, ok := ad.(ports.WorkspaceLister); ok && l.ListsPerWorkspace(kind) {
		m.where, m.source, m.workspace = ad.Name()+"'s "+kind+" list for "+workspace, kind+"_list", workspace
	}
	it, found, problems, err := ad.UserItem(ctx, a.harnessInfo(ctx, harness), kind, name, workspace)
	if len(problems) > 0 {
		m.errors = discoveryErrors(problems)
		m.where += ", which could not load " + m.errors
		m.source += "_errors"
	}
	return itemLookup{item: it, found: found, missing: m, unreadable: err}
}

// discoveryErrors reads a harness's discovery errors as one line: each file and the harness's message.
func discoveryErrors(problems []ports.DiscoveryError) string {
	parts := make([]string, 0, len(problems))
	for _, p := range problems {
		parts = append(parts, p.Path+": "+p.Message)
	}
	return strings.Join(parts, "; ")
}

// itemWorkspace is the folder a task's own agents and skills are looked up for: its workspace path, expanded, when it
// sets one, otherwise the folder HarmFlow runs it in.
func (a *App) itemWorkspace(doc domain.TaskDocument) string {
	if w := doc.Spec.Workspace; w != nil && w.Path != "" {
		if p, err := a.expand(w.Path); err == nil {
			return p
		}
	}
	return filepath.Join(a.d.Paths.Workspace, doc.Metadata.ID)
}

// CatalogProblem is what kept a harness from listing the user's items in full: a file it reported it could not load,
// in its own words, or its list that could not be read.
type CatalogProblem struct {
	Harness string `json:"harness"`
	Kind    string `json:"kind"`
	Path    string `json:"path,omitempty"`
	domain.Text
}

// Catalog lists the built-in agents and skills, offered on every harness, then the user's, each with the harnesses
// that find it for workspace, sorted by name. A user's item is left out when a task cannot cite it: its name has the
// hf- prefix, or is not an identifier of the task schema, as a plugin's `<plugin>:<skill>` is not. A user's agent that
// more than one harness holds is copied from the first of them by name. An empty workspace is HarmFlow's workspace
// root. What a harness could not list is in Problems.
func (a *App) Catalog(ctx context.Context, workspace string) Catalog {
	c := Catalog{Agents: []CatalogEntry{}, Skills: []CatalogEntry{}, Problems: []CatalogProblem{}}
	if workspace != "" {
		if p, err := a.expand(workspace); err == nil {
			workspace = p
		} else {
			workspace = ""
		}
	}
	if workspace == "" {
		workspace = a.d.Paths.Workspace
	}
	all := sortedKeys(a.adapters)
	for _, kind := range []string{"agent", "skill"} {
		entries := map[string]*CatalogEntry{}
		for _, it := range a.builtinDir().Items(kind) {
			entries[it.Name] = &CatalogEntry{Name: it.Name, Kind: kind, Source: "builtin", Description: it.Description,
				Harnesses: all, Copy: a.agentCopy(it)}
		}
		for _, id := range all {
			ad := a.adapters[id]
			items, problems, err := ad.UserItems(ctx, a.harnessInfo(ctx, id), kind, workspace)
			if err != nil {
				c.Problems = append(c.Problems, CatalogProblem{Harness: id, Kind: kind,
					Text: listUnreadable(ad.Name(), workspace, err)})
			}
			for _, p := range problems {
				c.Problems = append(c.Problems, CatalogProblem{Harness: id, Kind: kind, Path: p.Path,
					Text: domain.Text{Message: p.Message}})
			}
			for _, it := range items {
				if strings.HasPrefix(it.Name, BuiltinPrefix) || !a.d.Validator.ValidIdentifier(it.Name) {
					continue
				}
				e := entries[it.Name]
				if e == nil {
					e = &CatalogEntry{Name: it.Name, Kind: kind, Source: "user", Description: it.Description, Harnesses: []string{},
						Copy: a.agentCopy(it)}
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
	key, scriptKey := "github", "script"
	c.TaskTypes = []TaskTypeInfo{
		{ID: domain.TypeGitHub, ParametersKey: &key, DefaultAgents: []string{},
			DefaultSkills: append([]string{}, BuiltinIssueSkills...), DefaultPrompt: defaultIssuePrompt},
		{ID: domain.TypeGeneral, ParametersKey: nil, DefaultAgents: []string{}, DefaultSkills: []string{}},
		{ID: domain.TypeScriptTask, ParametersKey: &scriptKey, DefaultAgents: []string{}, DefaultSkills: []string{}},
	}
	return c
}

// agentCopy is what selecting the agent it copies into a task; nil for a skill, or an agent whose file cannot be read.
func (a *App) agentCopy(it ports.ResolvedItem) *AgentCopy {
	if it.Kind != "agent" {
		return nil
	}
	ac, err := catalog.ReadAgentCopy(it.Path)
	if err != nil {
		return nil
	}
	return &AgentCopy{Prompt: ac.Prompt, Description: ac.Description, Model: ac.Model,
		ModelHarnesses: a.modelHarnesses(ac.Model), Skills: ac.Skills, OmittedFields: ac.Omitted, Hash: ac.Hash}
}

// modelHarnesses lists the harnesses that accept the model as a task's, as validation checks it with what detection
// last found; none for an empty model.
func (a *App) modelHarnesses(model string) []string {
	out := []string{}
	if model == "" {
		return out
	}
	for _, id := range sortedKeys(a.adapters) {
		a.mu.Lock()
		info := a.inventory[id]
		a.mu.Unlock()
		if a.adapters[id].ValidateProfile(info, model, "") == nil {
			out = append(out, id)
		}
	}
	return out
}
