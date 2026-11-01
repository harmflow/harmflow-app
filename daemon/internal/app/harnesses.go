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
			out = append(out, a.withSignInLocked(id, info))
		} else {
			ad := a.adapters[id]
			info := ports.HarnessInfo{ID: id, Name: ad.Name(), Authentication: "unknown", Readiness: "checking",
				Installations: []string{}, Models: []ports.ModelInfo{}, Efforts: []string{}, EnforceableControls: []string{}}
			info.SetDiagnostic(domain.T("detection_running", "detection is running"))
			out = append(out, a.withSignInLocked(id, info))
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
// neither stored nor announced: it was cut short, and the inventory keeps what it had. A harness that says it is
// signed out starts its sign-in, unless one runs or one that failed holds it back.
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
	a.signInDiagnosticLocked(id, &info)
	a.inventory[id] = info
	info = a.withSignInLocked(id, info)
	a.mu.Unlock()
	a.emit(domain.Event{Type: domain.EventHarnessChecked, Harness: id, Severity: domain.SeverityInfo,
		Payload: map[string]any{"readiness": info.Readiness}})
	if info.Authentication == "invalid" {
		a.signInWhenSignedOut(id)
	}
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
// with none. What a new task of the type starts with is a template's.
type TaskTypeInfo struct {
	ID            string  `json:"id"`
	ParametersKey *string `json:"parametersKey"`
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
// finds them for workspace and the folders a session of the task adds.
func (a *App) resolveItem(ctx context.Context, kind, name, harness, workspace string, added []string) itemLookup {
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
	if u, ok := ad.(ports.UnverifiedItemsAllower); ok && u.AllowsUnverified(kind) {
		m.unverifiable = true
	}
	it, found, problems, err := ad.UserItem(ctx, a.harnessInfo(ctx, harness), kind, name, workspace, added)
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
	path := ""
	if w := doc.Spec.Workspace; w != nil {
		path = w.Path
	}
	return a.taskFolder(path, doc.Metadata.ID)
}

// addedFolders are the folders a session of the task adds beside its workspace, which its harness may find items in
// too: each repository's local path and each writable path of a custom profile, resolved as an execution resolves
// them. A folder that does not resolve is left out, as validation leaves it out of the harness's restrictions.
func (a *App) addedFolders(doc domain.TaskDocument) []string {
	eff := doc.Effective()
	e := a.currentPathEnv()
	_, repos := a.taskFolders(eff, doc.Metadata.ID, e)
	var out []string
	for _, f := range repos {
		if f.err == nil {
			out = append(out, f.path)
		}
	}
	return append(out, a.resolvedFolders(e, customWritablePaths(eff))...)
}

// customWritablePaths are the writable paths a session of the task adds, those of a custom profile; none for another
// profile.
func customWritablePaths(eff domain.TaskDocument) []string {
	if p := eff.Spec.Permissions; p != nil && p.Profile == domain.ProfileCustom && p.Custom != nil {
		return p.Custom.WritablePaths
	}
	return nil
}

// resolvedFolders are paths resolved as an execution resolves a task's writable paths, without those that do not
// resolve.
func (a *App) resolvedFolders(e pathEnv, paths []string) []string {
	var out []string
	for _, p := range paths {
		if r, err := e.writablePath(a, p); err == nil {
			out = append(out, r)
		}
	}
	return out
}

// taskFolder is the folder of a task with this workspace path and ID: the path, expanded, when it is set and expands,
// otherwise the folder HarmFlow runs the task in, which for no ID is HarmFlow's workspace root.
func (a *App) taskFolder(path, id string) string {
	if path != "" {
		if p, err := a.expand(path); err == nil {
			return p
		}
	}
	return filepath.Join(a.d.Paths.Workspace, id)
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
// that find it for the folder of a task with this workspace path and ID, sorted by name. That folder is the one
// validation and an execution look the task's items up for: the workspace path, expanded, otherwise the folder HarmFlow
// runs the task in, and HarmFlow's workspace root for neither. A user's item is left out when a task cannot cite it:
// its name has the hf- prefix, or is not a name the task schema gives its kind. A skill's is an identifier or a
// plugin's `<plugin>:<skill>`, listed as the harness reports it; an agent's is an identifier. A user's agent that
// more than one harness holds is copied from the first of them by name. What a harness could not list is in Problems.
// The added paths are the task's repositories' local paths and writable paths, which a harness may find items in as
// well: resolved as validation resolves them, without those that do not resolve.
func (a *App) Catalog(ctx context.Context, workspace, task string, added ...string) Catalog {
	c := Catalog{Agents: []CatalogEntry{}, Skills: []CatalogEntry{}, Problems: []CatalogProblem{}}
	workspace = a.taskFolder(workspace, task)
	folders := a.resolvedFolders(a.currentPathEnv(), added)
	all := sortedKeys(a.adapters)
	for _, kind := range []string{"agent", "skill"} {
		entries := map[string]*CatalogEntry{}
		for _, it := range a.builtinDir().Items(kind) {
			entries[it.Name] = &CatalogEntry{Name: it.Name, Kind: kind, Source: "builtin", Description: it.Description,
				Harnesses: all, Copy: a.agentCopy(it)}
		}
		for _, id := range all {
			ad := a.adapters[id]
			items, problems, err := ad.UserItems(ctx, a.harnessInfo(ctx, id), kind, workspace, folders)
			if err != nil {
				c.Problems = append(c.Problems, CatalogProblem{Harness: id, Kind: kind,
					Text: listUnreadable(ad.Name(), workspace, err)})
			}
			for _, p := range problems {
				c.Problems = append(c.Problems, CatalogProblem{Harness: id, Kind: kind, Path: p.Path,
					Text: domain.Text{Message: p.Message}})
			}
			for _, it := range items {
				if strings.HasPrefix(it.Name, BuiltinPrefix) || !a.citable(kind, it.Name) {
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
		{ID: domain.TypeGitHub, ParametersKey: &key},
		{ID: domain.TypeGeneral, ParametersKey: nil},
		{ID: domain.TypeScriptTask, ParametersKey: &scriptKey},
	}
	return c
}

// citable says whether a task can cite an item of kind by name, as the task schema names it.
func (a *App) citable(kind, name string) bool {
	if kind == "skill" {
		return a.d.Validator.ValidSkillName(name)
	}
	return a.d.Validator.ValidIdentifier(name)
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
