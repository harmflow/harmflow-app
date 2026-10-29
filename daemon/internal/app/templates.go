package app

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/text/unicode/norm"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// TemplateBuiltin is the source of a template that ships with HarmFlow, in its built-ins directory.
const TemplateBuiltin = "builtin"

// templateLanguages are the languages of the interface, each of which names a built-in template.
var templateLanguages = []string{"pt-BR", "en-US", "es-ES"}

// Template is a task template as the daemon lists it: its identifier, source, file, and validity, and, when it is
// valid, its task type, its name and description in each language, and the agent its tasks select.
type Template struct {
	ID            string            `json:"id"`
	Source        string            `json:"source"`
	Path          string            `json:"path"`
	Type          string            `json:"type,omitempty"`
	Name          map[string]string `json:"name,omitempty"`
	Description   map[string]string `json:"description,omitempty"`
	SelectedAgent string            `json:"selectedAgent,omitempty"`
	Validation    *ValidationResult `json:"validation"`

	spec map[string]any // the fields a new task starts with, the selected agent left out; nil when invalid
}

// NewTask is the document of a task not yet saved.
type NewTask struct {
	Document map[string]any `json:"document"`
}

// loadTemplates reads the built-in templates. They ship with HarmFlow and change only with it, so they are read once,
// at start; an invalid one is logged and kept, to be listed with its errors.
func (a *App) loadTemplates() {
	var loaded []*Template
	for _, f := range a.builtinDir().Templates() {
		t := a.readTemplate(f)
		if !t.Validation.Valid {
			a.log.Printf("built-in template %s is invalid: %s", f.Path, t.Validation.Errors[0].Message)
		}
		loaded = append(loaded, t)
	}
	a.mu.Lock()
	a.templates = loaded
	a.mu.Unlock()
}

// readTemplate validates a built-in template's file: against template.schema.json, then for the rules a schema cannot
// express, which are that its identifier is its file's name and carries the hf- prefix.
func (a *App) readTemplate(f ports.TemplateFile) *Template {
	t := &Template{ID: f.Name, Source: TemplateBuiltin, Path: f.Path}
	r, doc := a.validateTemplate(f.Content, f.Err)
	t.Validation = r
	metadata, _ := doc["metadata"].(map[string]any)
	spec, _ := doc["spec"].(map[string]any)
	for i, is := range r.Errors {
		if is.Path == "/spec/type" && is.Key == "schema_enum" {
			r.Errors[i].Text = domain.T("template_type_unknown", `"{{type}}" is not a task type; a template's type is one of {{types}}`,
				"type", fmt.Sprint(spec["type"]), "types", is.Params["values"])
		}
	}
	if id, ok := metadata["id"].(string); ok {
		if id != f.Name {
			r.addError("/metadata/id", domain.T("id_file_mismatch", `metadata.id "{{id}}" must match the file name {{file}}.yaml`,
				"id", id, "file", f.Name))
		}
		if !strings.HasPrefix(id, BuiltinPrefix) {
			r.addError("/metadata/id", domain.T("template_id_not_builtin",
				`a built-in template's identifier starts with hf-, and "{{id}}" does not`, "id", id))
		}
	}
	r.Valid = len(r.Errors) == 0
	if !r.Valid {
		return t
	}
	t.Type, _ = spec["type"].(string)
	t.Name, t.Description = texts(metadata["name"]), texts(metadata["description"])
	t.SelectedAgent, _ = spec["selectedAgent"].(string)
	t.spec = maps.Clone(spec)
	delete(t.spec, "selectedAgent")
	return t
}

// texts reads a text the schema checked in each language.
func texts(v any) map[string]string {
	out := map[string]string{}
	m, _ := v.(map[string]any)
	for lang, text := range m {
		out[lang], _ = text.(string)
	}
	return out
}

// Templates lists the task templates, each with its validity: the built-in ones, sorted by identifier.
func (a *App) Templates() []Template {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []Template{}
	for _, t := range a.templates {
		out = append(out, *t)
	}
	return out
}

func (a *App) template(id string) (*Template, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, t := range a.templates {
		if t.ID == id {
			return t, nil
		}
	}
	return nil, newError(CodeNotFound, domain.T("template_not_found", "no template {{template}}", "template", id))
}

// UpdateTemplate changes a template. Built-in templates are read-only, and no other template exists, so it always
// refuses: an identifier a built-in template has, or one with the hf- prefix that none has yet, is a conflict, and any
// other is not found.
func (a *App) UpdateTemplate(id string) error {
	return a.refuseTemplateWrite(id)
}

// DeleteTemplate deletes a template; it refuses as UpdateTemplate does.
func (a *App) DeleteTemplate(id string) error {
	return a.refuseTemplateWrite(id)
}

func (a *App) refuseTemplateWrite(id string) error {
	if _, err := a.template(id); err != nil && !strings.HasPrefix(id, BuiltinPrefix) {
		return err
	}
	return newError(CodeConflict, domain.T("template_read_only",
		"{{template}} is the identifier of a built-in template: built-in templates ship with HarmFlow, which never writes one",
		"template", id))
}

// DraftTask builds the document of a new task from a template, and writes nothing. The task is named after the
// template in language, with an identifier no task has. When the template selects an agent, the document is first that
// agent's copy, as selecting it in the task editor makes it, and then takes the fields the template sets, which keep
// the template's values; when this machine lacks the agent, the document holds no prompt. The document is not
// validated: the values the template leaves unset are the editor's to ask for, and saving validates the task.
func (a *App) DraftTask(ctx context.Context, id, language string) (*NewTask, error) {
	if !slices.Contains(templateLanguages, language) {
		return nil, newError(CodeBadRequest, domain.T("template_language_invalid",
			`language must be pt-BR, en-US, or es-ES, not "{{language}}"`, "language", language))
	}
	t, err := a.template(id)
	if err != nil {
		return nil, err
	}
	if !t.Validation.Valid {
		e := newError(CodeValidationFailed, domain.T("template_invalid", "template {{template}} is invalid: {{file}}",
			"template", id, "file", t.Path))
		e.Validation = t.Validation
		return nil, e
	}
	name := t.Name[language]
	taskID := a.freeTaskID(slugify(name))
	metadata := map[string]any{"id": taskID, "name": name}
	spec := map[string]any{"enabled": true, "type": t.Type}
	if t.SelectedAgent != "" {
		a.copyAgent(ctx, metadata, spec, t, taskID)
	}
	merge(spec, t.spec)
	return &NewTask{Document: map[string]any{"apiVersion": domain.APIVersion, "kind": domain.KindTask,
		"metadata": metadata, "spec": spec}}, nil
}

// copyAgent copies the agent a template selects into a new task, as the task editor does when the agent is selected:
// its instructions become the prompt, its description the task's, its model the preferred harness's when that harness
// accepts it, its skills are added, and the agent and its hash are the task's origin. The agent is looked up for the
// template's preferred harness and the folder the task would run in; one this machine lacks copies nothing.
func (a *App) copyAgent(ctx context.Context, metadata, spec map[string]any, t *Template, taskID string) {
	harness, _ := lookup(t.spec, "harness", "preferred", "provider").(string)
	if harness == "" {
		harness = domain.HarnessClaudeCode
	}
	workspace, _ := lookup(t.spec, "workspace", "path").(string)
	l := a.resolveItem(ctx, "agent", t.SelectedAgent, harness, a.taskFolder(workspace, taskID))
	if !l.found {
		return
	}
	c := a.agentCopy(l.item)
	if c == nil {
		return
	}
	spec["prompt"] = c.Prompt
	if c.Description != "" {
		metadata["description"] = c.Description
	}
	if c.Model != "" && slices.Contains(c.ModelHarnesses, harness) {
		merge(spec, map[string]any{"harness": map[string]any{"preferred": map[string]any{"model": c.Model}}})
	}
	if len(c.Skills) > 0 {
		skills := []any{}
		for _, s := range c.Skills {
			if !slices.Contains(skills, any(s)) {
				skills = append(skills, s)
			}
		}
		spec["skills"] = skills
	}
	spec["origin"] = map[string]any{"agent": t.SelectedAgent, "hash": c.Hash}
}

// lookup is the value at path in nested objects, nil when a step is missing or not an object.
func lookup(v any, path ...string) any {
	for _, key := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[key]
	}
	return v
}

// merge sets into dst every field src sets, as a copy: an object merges into the object dst holds there, and any other
// value, a list included, replaces dst's.
func merge(dst, src map[string]any) {
	for k, v := range src {
		if sv, ok := v.(map[string]any); ok {
			if dv, ok := dst[k].(map[string]any); ok {
				merge(dv, sv)
				continue
			}
			dv := map[string]any{}
			merge(dv, sv)
			dst[k] = dv
			continue
		}
		dst[k] = clone(v)
	}
}

// clone copies a value decoded from JSON, so that a document built from it never shares a list or an object with it.
func clone(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := map[string]any{}
		merge(out, t)
		return out
	case []any:
		out := []any{}
		for _, x := range t {
			out = append(out, clone(x))
		}
		return out
	}
	return v
}

// freeTaskID is base when no task has it, otherwise base with the first of -2, -3, … that no task has, cut so that it
// stays an identifier.
func (a *App) freeTaskID(base string) string {
	if base == "" {
		base = "task"
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	id := base
	for n := 2; a.tasks[id] != nil; n++ {
		suffix := "-" + strconv.Itoa(n)
		id = strings.TrimRight(base[:min(len(base), maxIDLength-len(suffix))], "-") + suffix
	}
	return id
}

// maxIDLength is the longest identifier the task schema accepts.
const maxIDLength = 64

var notSlug = regexp.MustCompile(`[^a-z0-9]+`)

// slugify makes an identifier of a name as the task editor does, so that a task created from a template has the
// identifier its name would give it there: accents are dropped, letters lowered, every other run of characters becomes
// one hyphen, and the result is cut to an identifier's length.
func slugify(name string) string {
	var b strings.Builder
	for _, r := range norm.NFKD.String(name) {
		if r < 0x300 || r > 0x36f { // combining diacritical marks
			b.WriteRune(r)
		}
	}
	s := strings.Trim(notSlug.ReplaceAllString(strings.ToLower(b.String()), "-"), "-")
	return strings.TrimRight(s[:min(len(s), maxIDLength)], "-")
}
