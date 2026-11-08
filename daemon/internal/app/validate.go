package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/schema"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// TaskInput is the YAML text (the editor and import) or the document (the form); both are validated the same way.
type TaskInput struct {
	YAML         string          `json:"yaml,omitempty"`
	Document     json.RawMessage `json:"document,omitempty"`
	BaseRevision string          `json:"baseRevision,omitempty"`
}

// ValidationResult is the outcome of validating a task: its errors and warnings, located in the YAML when possible, and
// the document as read, with its defaults applied, and as YAML. Valid means it has no error.
type ValidationResult struct {
	Valid     bool           `json:"valid"`
	Errors    []schema.Issue `json:"errors"`
	Warnings  []schema.Issue `json:"warnings"`
	Document  any            `json:"document,omitempty"`
	Effective any            `json:"effective,omitempty"`
	YAML      string         `json:"yaml,omitempty"`

	doc     *domain.TaskDocument
	content []byte
	node    *yaml.Node
	items   []domain.ItemParameters // what the cited agents and skills declare, as validation read them
}

// catalogMode decides whether a missing agent or skill is an error (saving) or a warning (import and external edits),
// and whether the harnesses are asked at all (start).
type catalogMode int

const (
	catalogStrict catalogMode = iota
	catalogLenient
	// catalogStart is catalogLenient without the checks that ask a harness: its detection, its models, its readiness,
	// and its lists of agents and skills. The daemon loads its task files with it, so that it starts without waiting for
	// a harness, and checks them again with catalogLenient once it has started (checkTasks).
	catalogStart
)

// Validate checks a task without writing anything.
func (a *App) Validate(ctx context.Context, in TaskInput) *ValidationResult {
	return a.validate(ctx, in, catalogStrict)
}

func (a *App) validate(ctx context.Context, in TaskInput, mode catalogMode) *ValidationResult {
	r := &ValidationResult{Errors: []schema.Issue{}, Warnings: []schema.Issue{}}
	var value any
	switch {
	case strings.TrimSpace(in.YAML) != "":
		v, node, issues := schema.ParseYAML([]byte(in.YAML))
		if len(issues) > 0 {
			r.Errors = issues
			return r
		}
		value, r.node, r.content = v, node, []byte(in.YAML)
		r.YAML = in.YAML
	case len(in.Document) > 0 && string(in.Document) != "null":
		v, err := schema.FromJSON(in.Document)
		if err != nil {
			r.Errors = append(r.Errors, schema.Issue{Code: "syntax",
				Text: domain.T("document_not_json", "the document is not valid JSON: {{detail}}", "detail", err.Error())})
			return r
		}
		value = v
		// The editor shows YAML even for a document the form cannot save yet.
		var plain any
		if json.Unmarshal(in.Document, &plain) == nil {
			if out, err := yaml.Marshal(plain); err == nil {
				r.YAML = string(out)
			}
		}
	default:
		r.Errors = append(r.Errors, schema.Issue{Text: domain.T("task_input_missing", "send the task as yaml or as document"),
			Code: "syntax"})
		return r
	}
	r.Document = value
	r.Errors = append(r.Errors, a.d.Validator.ValidateTask(value, r.node)...)
	if len(r.Errors) > 0 {
		return r
	}
	var doc domain.TaskDocument
	if r.content != nil {
		if err := yaml.Unmarshal(r.content, &doc); err != nil {
			r.Errors = append(r.Errors, schema.Issue{Text: domain.TextOf(err), Code: "syntax"})
			return r
		}
	} else {
		if err := json.Unmarshal(in.Document, &doc); err != nil {
			r.Errors = append(r.Errors, schema.Issue{Text: domain.TextOf(err), Code: "syntax"})
			return r
		}
		out, err := marshalYAML(doc)
		if err != nil {
			r.Errors = append(r.Errors, schema.Issue{Text: domain.TextOf(err), Code: "syntax"})
			return r
		}
		r.content, r.YAML = out, string(out)
		_, r.node, _ = schema.ParseYAML(out)
	}
	r.doc = &doc
	a.semanticChecks(ctx, r, doc, mode)
	r.Effective = doc.Effective()
	r.Valid = len(r.Errors) == 0
	return r
}

// validateTemplate checks a template file against template.schema.json, and gives the document it decodes to; readErr
// is the error that kept the file from being read.
func (a *App) validateTemplate(content []byte, readErr error) (*ValidationResult, map[string]any) {
	r := &ValidationResult{Errors: []schema.Issue{}, Warnings: []schema.Issue{}}
	if readErr != nil {
		r.Errors = append(r.Errors, schema.Issue{Text: domain.TextOf(readErr)})
		return r, nil
	}
	value, node, issues := schema.ParseYAML(content)
	if len(issues) > 0 {
		r.Errors = issues
		return r, nil
	}
	r.node = node
	r.Errors = append(r.Errors, a.d.Validator.ValidateTemplate(value, node)...)
	doc, _ := value.(map[string]any)
	return r, doc
}

func (r *ValidationResult) addError(path string, text domain.Text) {
	is := schema.Issue{Path: path, Text: text, Code: "semantic"}
	is.Line, is.Column = schema.LocatePointer(r.node, path)
	r.Errors = append(r.Errors, is)
}

func (r *ValidationResult) addWarning(path, code string, text domain.Text) {
	is := schema.Issue{Path: path, Text: text, Code: code}
	is.Line, is.Column = schema.LocatePointer(r.node, path)
	r.Warnings = append(r.Warnings, is)
}

// semanticChecks are the rules a JSON Schema cannot express: schedules, the harness's capabilities, and the agents
// and skills each harness of the chain can load.
func (a *App) semanticChecks(ctx context.Context, r *ValidationResult, doc domain.TaskDocument, mode catalogMode) {
	if s := doc.Spec.Schedule; s != nil {
		if s.Cron == "" && s.Interval == "" {
			r.addError("/spec/schedule", domain.T("schedule_missing",
				"a schedule needs a cron expression or an interval; remove it for a manual-only task"))
		} else if _, err := domain.ParseSchedule(*s, a.d.Location); err != nil {
			path := "/spec/schedule/cron"
			if s.Interval != "" {
				path = "/spec/schedule/interval"
			}
			if strings.Contains(err.Error(), "time zone") {
				path = "/spec/schedule/timezone"
			}
			r.addError(path, domain.TextOf(err))
		}
	}
	profiles := doc.Profiles()
	for i, p := range profiles {
		path := "/spec/harness/preferred"
		if i > 0 {
			path = fmt.Sprintf("/spec/harness/fallback/candidates/%d", i-1)
		}
		ad, ok := a.adapters[p.Provider]
		if !ok {
			r.addError(path+"/provider", domain.T("harness_unknown", `unknown harness "{{harness}}"`, "harness", p.Provider))
			continue
		}
		asks := mode != catalogStart
		var info ports.HarnessInfo
		if asks {
			info = a.profileInstallation(ctx, r, ad, p, path)
			if err := ad.ValidateProfile(info, p.Model, p.Effort); err != nil {
				r.addError(path, domain.TextOf(err))
			} else if w, ok := ad.(ports.ProfileWarner); ok {
				if text, unchecked := w.ProfileWarning(info, p.Model, p.Effort); unchecked {
					r.addWarning(path, domain.WarningOther, text)
				}
			}
		}
		if p.KeepsSession() {
			if keepsSessions(ad) {
				r.addWarning(path+"/ephemeral", domain.WarningOther, sessionKeptNote(ad.Name()))
			} else {
				r.addError(path+"/ephemeral", domain.T("harness_session_not_kept",
					"{{harness}} cannot keep its session, so ephemeral cannot be false on {{profile}}",
					"harness", ad.Name(), "profile", path))
			}
		}
		if !doc.AllowsExtraUsage() && !reportsExtraUsage(ad) {
			r.addWarning(path+"/provider", domain.WarningPolicyUnenforced, extraUsageUnenforced(ad.Name()))
		}
		if !asks {
			continue
		}
		if !info.Detected {
			r.addWarning(path+"/provider", domain.WarningHarnessNotReady,
				domain.T("harness_not_detected", "{{harness}} was not detected on this machine", "harness", ad.Name()))
		} else if !info.Ready() {
			r.addWarning(path+"/provider", domain.WarningHarnessNotReady, domain.T("harness_not_ready",
				"{{harness}} is not ready: {{detail}}", "harness", ad.Name()).Nest("detail", info.DiagnosticText()))
		}
		// Agents and skills must be available to every harness of the chain. Those a harness loads itself, whose list it
		// could not give, are unverified on the preferred harness and on a fallback alike.
		missing := a.missingItems(ctx, doc, p.Provider, p.Installation, a.itemWorkspace(doc), a.addedFolders(doc))
		for _, m := range missing {
			code := domain.WarningMissingSkill
			kindPath := "/spec/skills"
			if m.kind == "agent" {
				code, kindPath = domain.WarningMissingAgent, "/spec/agents"
			}
			switch {
			case i == 0 && m.leftUnverified():
				r.addWarning(kindPath, code, m.unverified(ad.Name()))
			case m.leftUnverified():
				r.addWarning(path, code, m.unverified(ad.Name()))
			case i == 0 && mode == catalogStrict:
				r.addError(kindPath, m.notFound(ad.Name()))
			case i == 0:
				r.addWarning(kindPath, code, m.missing(ad.Name()))
			default:
				r.addWarning(path, domain.WarningInvalidFallback, m.invalidFallback(ad.Name()))
			}
		}
	}
	if len(profiles) > 1 && profiles[0].Provider != "" {
		for i, p := range profiles[1:] {
			// Another installation of the same harness is another account, so it is no repeat.
			if p.Provider == profiles[0].Provider && p.Model == profiles[0].Model && p.Installation == profiles[0].Installation {
				r.addWarning(fmt.Sprintf("/spec/harness/fallback/candidates/%d", i), domain.WarningOther,
					domain.T("fallback_repeats_preferred", "this fallback repeats the preferred harness and model"))
			}
		}
	}
	// The paths that let a harness write, and a script's program, are errors in every mode: a task file written outside
	// the dashboard is checked too, and loads as invalid.
	refused := map[string]bool{}
	for _, is := range a.taskPathIssues(doc, a.currentPathEnv()) {
		r.addError(is.path, is.text)
		refused[is.path] = true
	}
	for i, repo := range doc.Repositories() {
		path := fmt.Sprintf("/spec/github/repositories/%d/localPath", i)
		p, err := a.expand(repo.LocalPath)
		if err != nil {
			r.addWarning(path, domain.WarningOther, domain.TextOf(err))
			continue
		}
		if refused[path] {
			continue
		}
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			r.addWarning(path, domain.WarningOther,
				domain.T("local_path_missing", "the local path {{path}} does not exist on this machine", "path", p))
		}
	}
	if w := doc.Spec.Workspace; w != nil && w.Path != "" {
		if _, err := a.expand(w.Path); err != nil {
			r.addWarning("/spec/workspace/path", domain.WarningOther, domain.TextOf(err))
		}
	}
	// A read path that does not expand, as one whose variable Settings does not define, is refused: the paths the rule
	// refuses are among the errors above, and this one cannot be checked against it.
	if p := doc.Spec.Permissions; p != nil && p.Custom != nil {
		for i, rp := range p.Custom.ReadablePaths {
			if _, err := a.expand(rp); err != nil {
				r.addError(fmt.Sprintf("/spec/permissions/custom/readablePaths/%d", i), domain.TextOf(err))
			}
		}
	}
	a.scriptChecks(r, doc)
	a.parameterChecks(ctx, r, doc, mode)
	if mode == catalogStart {
		return // the restrictions a harness enforces depend on what its detection found
	}
	for _, w := range a.policyWarnings(ctx, doc) {
		r.addWarning("/spec/permissions", w.Code, w.Text)
	}
}

// parameterChecks checks the values the task gives the parameters its agents and skills declare, as its preferred
// harness finds them: a missing required value, a value of the wrong type, and an object its schema rejects are errors
// in every mode, and a value no cited item declares a warning. When the daemon loads its task files, only the
// built-ins' declarations are read, and a value for another item is checked once the harnesses are asked.
func (a *App) parameterChecks(ctx context.Context, r *ValidationResult, doc domain.TaskDocument, mode catalogMode) {
	if doc.IsScript() {
		return
	}
	r.items = a.declaredItems(ctx, doc, mode != catalogStart)
	_, problems := domain.ResolveParameters(doc.Spec.Parameters, r.items, a.d.Validator.CheckDeclared)
	for _, p := range problems {
		switch {
		case p.Error:
			r.addError(p.Path, p.Text)
		case mode == catalogStart && p.Text.Key == "parameters_unverified":
			// not read yet: checkTasks reads it once the harnesses can be asked
		default:
			r.addWarning(p.Path, domain.WarningOther, p.Text)
		}
	}
}

// dropUndeclared removes from a task about to be saved each value no cited agent or skill declares, which validation
// warned about, keeping the rest of the file as written.
func dropUndeclared(r *ValidationResult) error {
	if r.doc == nil || !domain.DropUndeclared(r.doc.Spec.Parameters, r.items) {
		return nil
	}
	if r.doc.Spec.Parameters.Empty() {
		r.doc.Spec.Parameters = nil
	}
	out, err := pruneParameters(r.content, r.doc.Spec.Parameters)
	if err != nil {
		return err
	}
	r.content, r.YAML = out, string(out)
	_, r.node, _ = schema.ParseYAML(out)
	// The values the warnings named are no longer in the file.
	r.Warnings = slices.DeleteFunc(r.Warnings, func(is schema.Issue) bool { return is.Key == "parameter_undeclared" })
	return nil
}

// pruneParameters removes from the YAML text each value of spec.parameters that values no longer holds, and each map
// left empty, keeping the rest of the file, its comments included, as written.
func pruneParameters(content []byte, values *domain.DeclaredParameters) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(content, &root); err != nil {
		return nil, err
	}
	if len(root.Content) == 0 {
		return content, nil
	}
	spec := mappingValue(root.Content[0], "spec")
	params := mappingValue(spec, "parameters")
	if params == nil {
		return content, nil
	}
	keep := domain.DeclaredParameters{}
	if values != nil {
		keep = *values
	}
	for key, kept := range map[string]map[string]map[string]any{"skills": keep.Skills, "agents": keep.Agents} {
		filterMapping(mappingValue(params, key), func(item string, v *yaml.Node) bool {
			vals, ok := kept[item]
			if !ok {
				return false
			}
			filterMapping(v, func(name string, _ *yaml.Node) bool {
				_, ok := vals[name]
				return ok
			})
			return v.Kind != yaml.MappingNode || len(v.Content) > 0
		})
	}
	filterMapping(params, func(_ string, v *yaml.Node) bool { return v.Kind != yaml.MappingNode || len(v.Content) > 0 })
	filterMapping(spec, func(k string, v *yaml.Node) bool { return k != "parameters" || len(v.Content) > 0 })
	return encodeNode(&root)
}

// mappingValue is the value of key in a mapping node; nil for none, or for a node that is no mapping.
func mappingValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// filterMapping keeps the pairs of a mapping node that keep accepts, in their order.
func filterMapping(m *yaml.Node, keep func(key string, value *yaml.Node) bool) {
	if m == nil || m.Kind != yaml.MappingNode {
		return
	}
	out := m.Content[:0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		if keep(m.Content[i].Value, m.Content[i+1]) {
			out = append(out, m.Content[i], m.Content[i+1])
		}
	}
	m.Content = out
}

// missingItem is an agent or skill a harness cannot load, and where it was looked for. When unreadable is set, the
// harness's list itself could not be read, which says nothing of the item.
type missingItem struct {
	kind, name, where string
	source            string // builtin, harness, or <kind>_list, with _errors when the list reported discovery errors
	errors            string // the discovery errors, as one line
	unreadable        error
	// unverifiable says the harness loads the task's items of this kind itself, so a list it could not give leaves them
	// unverified: validation warns instead of refusing.
	unverifiable bool
}

// leftUnverified says the harness's list could not be read and the harness loads the items of this kind itself, so the
// item is not missing but unverified: the harness keeps its place in the chain.
func (m missingItem) leftUnverified() bool { return m.unreadable != nil && m.unverifiable }

// unverified says a harness could not give the list of skills its installation loads, so the task's skills are not
// verified, and why.
func (m missingItem) unverified(harness string) domain.Text {
	return domain.T("skill_list_unverified",
		"{{harness}} could not say which skills it loads, so the task's skills are not verified: {{detail}}",
		"harness", harness).Nest("detail", domain.TextOf(m.unreadable))
}

// listUnreadable says the list of skills a harness's installation loads could not be read, and why. Only skills are
// listed by the harness.
func listUnreadable(harness string, err error) domain.Text {
	return domain.T("skill_list_unreadable", "{{harness}}'s skill list could not be read: {{detail}}",
		"harness", harness).Nest("detail", domain.TextOf(err))
}

// notFound, missing, and invalidFallback say an item is missing for a harness: on save, on import, and for a fallback.
// The source param names where it was looked for, for the desktop to translate. An item whose harness's list could not
// be read is said to be so instead.
func (m missingItem) notFound(harness string) domain.Text {
	if m.unreadable != nil {
		return listUnreadable(harness, m.unreadable)
	}
	if m.kind == "agent" {
		return domain.T("agent_not_found", `agent "{{name}}" cannot be found for {{harness}} ({{where}})`, m.params(harness)...)
	}
	return domain.T("skill_not_found", `skill "{{name}}" cannot be found for {{harness}} ({{where}})`, m.params(harness)...)
}

func (m missingItem) missing(harness string) domain.Text {
	if m.unreadable != nil {
		return listUnreadable(harness, m.unreadable)
	}
	if m.kind == "agent" {
		return domain.T("agent_missing", `agent "{{name}}" is missing on this machine for {{harness}} ({{where}})`,
			m.params(harness)...)
	}
	return domain.T("skill_missing", `skill "{{name}}" is missing on this machine for {{harness}} ({{where}})`,
		m.params(harness)...)
}

func (m missingItem) invalidFallback(harness string) domain.Text {
	if m.unreadable != nil {
		return domain.T("fallback_skill_list_unreadable",
			"{{harness}} is not a valid fallback: its skill list could not be read: {{detail}}",
			"harness", harness).Nest("detail", domain.TextOf(m.unreadable))
	}
	if m.kind == "agent" {
		return domain.T("fallback_agent_missing",
			`{{harness}} is not a valid fallback: agent "{{name}}" cannot be found for it ({{where}})`, m.params(harness)...)
	}
	return domain.T("fallback_skill_missing",
		`{{harness}} is not a valid fallback: skill "{{name}}" cannot be found for it ({{where}})`, m.params(harness)...)
}

func (m missingItem) params(harness string) []string {
	source := m.source
	if source == "" {
		source = "harness"
	}
	params := []string{"name", m.name, "harness", harness, "where", m.where, "source", source}
	if m.errors != "" {
		params = append(params, "errors", m.errors)
	}
	return params
}

// profileInstallation is what detection found of the installation a profile at path runs on, which its model and
// effort are checked against. An installation key the detected harness lists no installation by fails validation, and
// the profile is then checked against the installation in use; so does one that names an installation that says it is
// signed out. Both fail it in every mode that asks the harness: the save, an import, and the check of the task files
// once the daemon has started, so a task whose installation is gone, or no longer signed in, since it was saved loads
// as invalid. A harness not detected is warned about on its own.
func (a *App) profileInstallation(ctx context.Context, r *ValidationResult, ad ports.HarnessAdapter, p domain.HarnessProfile,
	path string) ports.HarnessInfo {
	info := a.harnessInfo(ctx, p.Provider)
	if p.Installation == "" || !info.Detected {
		return info
	}
	inst, found := info.InstallationNamed(p.Installation)
	if !found {
		r.addError(path+"/installation", installationMissing(ad.Name(), p.Installation))
		return info
	}
	if inst.Authentication == "invalid" {
		r.addError(path+"/installation", domain.T("harness_installation_signed_out",
			`{{harness}}'s installation "{{installation}}" is not signed in`, "harness", ad.Name(), "installation", p.Installation))
	}
	return inst
}

// missingItems are the task's agents and skills an installation of a harness, the one in use for an empty one, cannot
// load for workspace and the added folders. A list that cannot be read is one entry, and the items of its kind are not
// looked up again.
func (a *App) missingItems(ctx context.Context, doc domain.TaskDocument, harness, installation, workspace string, added []string) []missingItem {
	var out []missingItem
	for _, kind := range []string{"agent", "skill"} {
		names := doc.Spec.Agents
		if kind == "skill" {
			names = doc.Spec.Skills
		}
		for _, name := range names {
			l := a.resolveItem(ctx, kind, name, harness, installation, workspace, added)
			if l.unreadable != nil {
				l.missing.unreadable = l.unreadable
				out = append(out, l.missing)
				break
			}
			if !l.found {
				out = append(out, l.missing)
			}
		}
	}
	return out
}

// policyWarnings names each declared restriction the primary harness, or nothing at all for a script task's program,
// can enforce. The harness is given the task's workspace and repositories as an execution gives them, without the
// folders that do not resolve.
func (a *App) policyWarnings(ctx context.Context, doc domain.TaskDocument) []domain.Warning {
	eff := doc.Effective()
	if doc.IsScript() {
		return scriptRestrictions(*eff.Spec.Permissions)
	}
	ad, ok := a.adapters[eff.Spec.Harness.Preferred.Provider]
	if !ok {
		return nil
	}
	info, found := a.installationInfo(ctx, ad.ID(), eff.Spec.Harness.Preferred.Installation)
	if !found {
		info = a.harnessInfo(ctx, ad.ID())
	}
	work, repos := a.taskFolders(eff, doc.Metadata.ID, a.currentPathEnv())
	var added []string
	for _, f := range repos {
		if f.err == nil {
			added = append(added, f.path)
		}
	}
	inv, _, err := ad.Prepare(info, ports.RunRequest{WorkDir: work.path, AdditionalDirs: added,
		Permissions: *eff.Spec.Permissions, PolicyCheck: true})
	if err != nil {
		return nil
	}
	var out []domain.Warning
	for _, r := range inv.Restrictions {
		if !r.Enforced {
			out = append(out, domain.Warning{Code: domain.WarningPolicyUnenforced, Subject: r.Name, Text: r.Text})
		}
	}
	return out
}

// reportsExtraUsage reports whether the harness says, while it runs, that it turned to extra usage, so an attempt that
// does not allow it can be ended then.
func reportsExtraUsage(ad ports.HarnessAdapter) bool {
	r, ok := ad.(ports.ExtraUsageReporter)
	return ok && r.ReportsExtraUsage()
}

// keepsSessions reports whether the harness can keep a session for the owner, as a profile with ephemeral false asks.
func keepsSessions(ad ports.HarnessAdapter) bool {
	k, ok := ad.(ports.SessionKeeper)
	return ok && k.KeepsSessions()
}

// sessionKeptNote says what a session the harness keeps holds, and where it stays: the note the task form shows under
// the ephemeral toggle while it is off, and the warning a task saved with ephemeral false gets.
func sessionKeptNote(harness string) domain.Text {
	return domain.T("session_kept_note",
		"the kept session holds everything the session read and printed, command output included, and stays under the {{harness}} home, outside HarmFlow's log retention, until you delete it",
		"harness", harness)
}

// extraUsageUnenforced says a task that allows no extra usage can be held to it on a harness only before each attempt
// starts.
func extraUsageUnenforced(harness string) domain.Text {
	return domain.T("extra_usage_unenforced",
		"nothing stops {{harness}} once it runs, so the task's ban on extra usage is checked only before each attempt starts",
		"harness", harness)
}

func marshalYAML(doc domain.TaskDocument) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// setYAMLField sets spec.<key> in the YAML text, keeping its comments and layout elsewhere.
func setYAMLField(content []byte, section, key string, value any) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(content, &root); err != nil {
		return nil, err
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("the task file is not a mapping")
	}
	m := root.Content[0]
	var sec *yaml.Node
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == section {
			sec = m.Content[i+1]
		}
	}
	if sec == nil || sec.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("the task file has no %s block", section)
	}
	var valNode yaml.Node
	if err := valNode.Encode(value); err != nil {
		return nil, err
	}
	for i := 0; i+1 < len(sec.Content); i += 2 {
		if sec.Content[i].Value == key {
			sec.Content[i+1] = &valNode
			return encodeNode(&root)
		}
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	sec.Content = append([]*yaml.Node{keyNode, &valNode}, sec.Content...)
	return encodeNode(&root)
}

func encodeNode(n *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(n); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// toMap converts a typed document into the JSON shape the API returns.
func toMap(v any) any {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	_ = json.Unmarshal(data, &out) // data is what json.Marshal just wrote
	return out
}
