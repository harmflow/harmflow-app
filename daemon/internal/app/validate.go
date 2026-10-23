package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
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
}

// catalogMode decides whether a missing agent or skill is an error (saving, FS-001 FR-010) or a warning (import and
// external edits, FS-001 FR-018).
type catalogMode int

const (
	catalogStrict catalogMode = iota
	catalogLenient
)

// Validate checks a task without writing anything.
func (a *App) Validate(in TaskInput) *ValidationResult {
	return a.validate(in, catalogStrict)
}

func (a *App) validate(in TaskInput, mode catalogMode) *ValidationResult {
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
	a.semanticChecks(r, doc, mode)
	r.Effective = doc.Effective()
	r.Valid = len(r.Errors) == 0
	return r
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
func (a *App) semanticChecks(r *ValidationResult, doc domain.TaskDocument, mode catalogMode) {
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
		info := a.harnessInfo(p.Provider)
		if err := ad.ValidateProfile(info, p.Model, p.Effort); err != nil {
			r.addError(path, domain.TextOf(err))
		} else if w, ok := ad.(ports.ProfileWarner); ok {
			if text, unchecked := w.ProfileWarning(info, p.Model, p.Effort); unchecked {
				r.addWarning(path, domain.WarningOther, text)
			}
		}
		if !info.Detected {
			r.addWarning(path+"/provider", domain.WarningHarnessNotReady,
				domain.T("harness_not_detected", "{{harness}} was not detected on this machine", "harness", ad.Name()))
		} else if !info.Ready() {
			r.addWarning(path+"/provider", domain.WarningHarnessNotReady, domain.T("harness_not_ready",
				"{{harness}} is not ready: {{detail}}", "harness", ad.Name()).Nest("detail", info.DiagnosticText()))
		}
		// Agents and skills must be available to every harness of the chain (D-092).
		missing := a.missingItems(doc, p.Provider)
		for _, m := range missing {
			code := domain.WarningMissingSkill
			kindPath := "/spec/skills"
			if m.kind == "agent" {
				code, kindPath = domain.WarningMissingAgent, "/spec/agents"
			}
			switch {
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
			if p.Provider == profiles[0].Provider && p.Model == profiles[0].Model {
				r.addWarning(fmt.Sprintf("/spec/harness/fallback/candidates/%d", i), domain.WarningOther,
					domain.T("fallback_repeats_preferred", "this fallback repeats the preferred harness and model"))
			}
		}
	}
	// The paths that let a harness write, and a script's program, are errors in every mode: a task file written outside
	// the dashboard is checked too, and loads as invalid (D-113, FS-009 FR-014).
	refused := map[string]bool{}
	for _, is := range a.taskPathIssues(doc, a.currentPathEnv()) {
		r.addError(is.path, is.text)
		refused[is.path] = true
	}
	for i, repo := range doc.Repositories() {
		path := fmt.Sprintf("/spec/githubIssueImplementation/repositories/%d/localPath", i)
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
	a.scriptChecks(r, doc)
	for _, w := range a.policyWarnings(doc) {
		r.addWarning("/spec/permissions", w.Code, w.Text)
	}
}

type missingItem struct {
	kind, name, where string
}

// notFound, missing, and invalidFallback say an item is missing for a harness: on save, on import, and for a fallback.
// The source param names where it was looked for, for the desktop to translate.
func (m missingItem) notFound(harness string) domain.Text {
	if m.kind == "agent" {
		return domain.T("agent_not_found", `agent "{{name}}" cannot be found for {{harness}} ({{where}})`, m.params(harness)...)
	}
	return domain.T("skill_not_found", `skill "{{name}}" cannot be found for {{harness}} ({{where}})`, m.params(harness)...)
}

func (m missingItem) missing(harness string) domain.Text {
	if m.kind == "agent" {
		return domain.T("agent_missing", `agent "{{name}}" is missing on this machine for {{harness}} ({{where}})`,
			m.params(harness)...)
	}
	return domain.T("skill_missing", `skill "{{name}}" is missing on this machine for {{harness}} ({{where}})`,
		m.params(harness)...)
}

func (m missingItem) invalidFallback(harness string) domain.Text {
	if m.kind == "agent" {
		return domain.T("fallback_agent_missing",
			`{{harness}} is not a valid fallback: agent "{{name}}" cannot be found for it ({{where}})`, m.params(harness)...)
	}
	return domain.T("fallback_skill_missing",
		`{{harness}} is not a valid fallback: skill "{{name}}" cannot be found for it ({{where}})`, m.params(harness)...)
}

func (m missingItem) params(harness string) []string {
	source := "harness"
	if strings.HasPrefix(m.name, "harmflow-") {
		source = "builtin"
	}
	return []string{"name", m.name, "harness", harness, "where", m.where, "source", source}
}

func (a *App) missingItems(doc domain.TaskDocument, harness string) []missingItem {
	var out []missingItem
	for _, name := range doc.Spec.Agents {
		if _, where, ok := a.resolveItem("agent", name, harness); !ok {
			out = append(out, missingItem{"agent", name, where})
		}
	}
	for _, name := range doc.Spec.Skills {
		if _, where, ok := a.resolveItem("skill", name, harness); !ok {
			out = append(out, missingItem{"skill", name, where})
		}
	}
	return out
}

// policyWarnings names each declared restriction the primary harness, or nothing at all for a script task's program,
// can enforce (D-068, FS-009 FR-010, FS-013 SEC-002).
func (a *App) policyWarnings(doc domain.TaskDocument) []domain.Warning {
	eff := doc.Effective()
	if doc.IsScript() {
		return scriptRestrictions(*eff.Spec.Permissions)
	}
	ad, ok := a.adapters[eff.Spec.Harness.Preferred.Provider]
	if !ok {
		return nil
	}
	info := a.harnessInfo(ad.ID())
	inv, _, err := ad.Prepare(info, ports.RunRequest{WorkDir: os.TempDir(), Permissions: *eff.Spec.Permissions})
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

func marshalYAML(doc domain.TaskDocument) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	_ = enc.Close()
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
	_ = enc.Close()
	return buf.Bytes(), nil
}

// toMap converts a typed document into the JSON shape the API returns.
func toMap(v any) any {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	_ = json.Unmarshal(data, &out)
	return out
}
