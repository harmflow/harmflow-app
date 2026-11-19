package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// TemplateUser is the source of a template of the user's, in the templates directory beside the tasks.
const TemplateUser = "user"

// kindTemplate is the kind of every template file.
const kindTemplate = "TaskTemplate"

// userTemplate is a template of the user's as the daemon last read or wrote its file.
type userTemplate struct {
	t    *Template
	file ports.TaskFile
}

// SaveAsTemplateInput is what a template saved from a task takes from the user: its identifier, its name, and what it
// is for, each written in every language as the user gives it.
type SaveAsTemplateInput struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// loadUserTemplates reads the user's template files at start; one that fails validation is listed with its errors,
// never offered, and announced as the watcher announces one.
func (a *App) loadUserTemplates() {
	if a.d.Templates == nil {
		return
	}
	files, err := a.d.Templates.List()
	if err != nil {
		a.log.Printf("listing the user's templates; none is offered: %v", err)
		return
	}
	for _, f := range files {
		t := a.readUserTemplate(f)
		a.mu.Lock()
		a.userTemplates[f.ID] = &userTemplate{t: t, file: f}
		a.mu.Unlock()
		if !t.Validation.Valid {
			a.emit(templateEvent(domain.EventTemplateInvalid, f, t, domain.T("template_file_invalid",
				"the template file {{file}} fails validation", "file", taskFileName(f))))
		}
	}
}

// readUserTemplate validates a template file of the user's.
func (a *App) readUserTemplate(f ports.TaskFile) *Template {
	return a.loadTemplate(&Template{ID: f.ID, Source: TemplateUser, Path: f.Path}, f.Content, nil)
}

// templateEvent announces a change to a template of the user's: its identifier and its file's name, and the errors of
// a file that fails validation.
func templateEvent(eventType string, f ports.TaskFile, t *Template, text domain.Text) domain.Event {
	e := domain.Event{Type: eventType, Payload: map[string]any{"template": f.ID, "file": taskFileName(f)}}
	if t != nil && !t.Validation.Valid {
		e.Severity = domain.SeverityWarning
		e.Payload["errors"] = t.Validation.Errors
	}
	if f.Revision != "" {
		e.Payload["revision"] = f.Revision
	}
	return e.Says(text)
}

// SaveAsTemplate saves a task as a new template of the user's, which holds the task's type and fields but its
// identifier, name, enabled state, and origin: a prompt copied from an agent stays as the task has it, and the template
// selects no agent. The template takes in's identifier, and its name and description in every language. The task is
// left as it is, and holds no reference to the template. An identifier with the hf- prefix, which only built-in
// templates carry, fails validation; one a template of the user's has, its file valid or not, is a conflict.
func (a *App) SaveAsTemplate(taskID string, in SaveAsTemplateInput) (Template, error) {
	e, err := a.entry(taskID)
	if err != nil {
		return Template{}, err
	}
	if a.d.Templates == nil {
		return Template{}, errors.New("this daemon keeps no templates of the user's")
	}
	a.mu.Lock()
	content, invalid := e.file.Content, e.invalid
	a.mu.Unlock()
	if invalid != nil {
		return Template{}, a.invalidError(invalid)
	}
	out, err := templateFromTask(content, in)
	if err != nil {
		return Template{}, err
	}
	return a.createUserTemplate(in.ID, out)
}

// createUserTemplate writes content as the file of a new template of the user's, id, once it is valid. The identifier
// is checked as a file's is, before it names any file: one with the hf- prefix fails validation, and one a template of
// the user's has, its file valid or not, is a conflict.
func (a *App) createUserTemplate(id string, content []byte) (Template, error) {
	if a.d.Templates == nil {
		return Template{}, errors.New("this daemon keeps no templates of the user's")
	}
	t := a.loadTemplate(&Template{ID: id, Source: TemplateUser}, content, nil)
	if !t.Validation.Valid {
		return Template{}, a.invalidError(t.Validation)
	}
	a.mu.Lock()
	_, exists := a.userTemplates[id]
	a.mu.Unlock()
	if _, err := a.d.Templates.Get(id); exists || err == nil {
		return Template{}, newError(CodeConflict,
			domain.T("template_exists", "a template with the identifier {{template}} already exists", "template", id))
	}
	f, err := a.d.Templates.Save(id, content, "")
	if err != nil {
		return Template{}, err
	}
	return a.afterTemplateWrite(t, f, domain.EventTemplateCreated), nil
}

// afterTemplateWrite makes f, which the daemon just wrote, the file of the user's template t, so that the watcher sees
// no outside change in it, announces the write, and gives t as it was written.
func (a *App) afterTemplateWrite(t *Template, f ports.TaskFile, eventType string) Template {
	t.Path = f.Path
	a.mu.Lock()
	a.userTemplates[f.ID] = &userTemplate{t: t, file: f}
	a.mu.Unlock()
	ev := templateEvent(eventType, f, t, domain.Text{})
	ev.Message = t.Name["en-US"]
	a.emit(ev)
	return opened(*t, f.Revision)
}

// opened is t with its file's text, the document that text decodes to, and revision.
func opened(t Template, revision string) Template {
	t.Revision, t.YAML, t.Document = revision, string(t.content), yamlValue(t.content)
	return t
}

// TemplateInput is an edit of a template: its YAML text (the YAML editor) or its document (the form), both validated
// the same way, and the revision of the file the edit started from.
type TemplateInput struct {
	YAML         string          `json:"yaml,omitempty"`
	Document     json.RawMessage `json:"document,omitempty"`
	BaseRevision string          `json:"baseRevision,omitempty"`
}

// NewTemplateInput is what an empty template of the user's takes: its identifier, its name and what it is for, each
// written in every language as the user gives it, and its task type.
type NewTemplateInput struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"`
}

// templateDocument is a template file, in the order it is written.
type templateDocument struct {
	APIVersion string           `yaml:"apiVersion"`
	Kind       string           `yaml:"kind"`
	Metadata   templateMetadata `yaml:"metadata"`
	Spec       struct {
		Type string `yaml:"type"`
	} `yaml:"spec"`
}

// OpenTemplate gives a template, built in or the user's, with its document and its file's text, and, for one of the
// user's, its file's revision. A template whose file fails validation is given with its errors and its text, so that
// it can be fixed.
func (a *App) OpenTemplate(id string) (Template, error) {
	t, err := a.template(id)
	if err != nil {
		return Template{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var revision string
	if u, ok := a.userTemplates[id]; ok && u.t == t {
		revision = u.file.Revision
	}
	return opened(*t, revision), nil
}

// CreateTemplate writes an empty template of the user's: its identifier, its name and description in every language
// as in gives them, and its task type, which the task schema defines, and no other field. The values a task of the
// type requires stay unset, for the template's editor to fill or leave to the task editor.
func (a *App) CreateTemplate(in NewTemplateInput) (Template, error) {
	doc := templateDocument{APIVersion: domain.APIVersion, Kind: kindTemplate,
		Metadata: templateMetadata{ID: in.ID, Name: map[string]string{}, Description: map[string]string{}}}
	for _, lang := range templateLanguages {
		doc.Metadata.Name[lang], doc.Metadata.Description[lang] = in.Name, in.Description
	}
	doc.Spec.Type = in.Type
	out, err := encodeYAML(doc)
	if err != nil {
		return Template{}, err
	}
	return a.createUserTemplate(in.ID, out)
}

// UpdateTemplate writes an edit of a template of the user's that started from in.BaseRevision, once it is valid: an
// invalid edit leaves the file as it was. A file changed meanwhile is a conflict, never a silent overwrite. The
// identifier stays the file's name. Built-in templates are read-only. No task created from the template changes.
func (a *App) UpdateTemplate(id string, in TemplateInput) (Template, error) {
	if _, err := a.userTemplate(id); err != nil {
		return Template{}, err
	}
	if in.BaseRevision == "" {
		return Template{}, newError(CodeBadRequest,
			domain.T("template_base_revision_required", "baseRevision is required to update a template"))
	}
	t := a.templateEdit(id, in)
	if !t.Validation.Valid {
		return Template{}, a.invalidError(t.Validation)
	}
	f, err := a.d.Templates.Save(id, t.content, in.BaseRevision)
	if errors.Is(err, ports.ErrConflict) {
		return Template{}, &Error{Code: CodeConflict, CurrentRevision: f.Revision, Text: domain.T("template_changed_on_disk",
			"the template file changed since this edit started; reload it before saving")}
	}
	if err != nil {
		return Template{}, err
	}
	return a.afterTemplateWrite(t, f, domain.EventTemplateUpdated), nil
}

// DeleteTemplate removes a template of the user's, its file valid or not. Built-in templates are read-only. No task
// created from the template changes: each holds its own copy of its values.
func (a *App) DeleteTemplate(id string) error {
	u, err := a.userTemplate(id)
	if err != nil {
		return err
	}
	if err := a.d.Templates.Delete(id); err != nil && !errors.Is(err, ports.ErrNotFound) {
		return err
	}
	a.mu.Lock()
	delete(a.userTemplates, id)
	a.mu.Unlock()
	f := u.file
	f.Revision = ""
	a.emit(templateEvent(domain.EventTemplateDeleted, f, nil, domain.Text{}))
	return nil
}

// DuplicateTemplate copies a template, built in or the user's, into a new template of the user's that holds its type
// and fields, as its file writes them, and its name in each language with " (copy)" after it. The copy's identifier is
// the template's without the hf- prefix and with -copy after it, or the first of -copy-2, -copy-3, … that no template
// has. A template whose file fails validation is refused.
func (a *App) DuplicateTemplate(id string) (Template, error) {
	t, err := a.template(id)
	if err != nil {
		return Template{}, err
	}
	if !t.Validation.Valid {
		e := newError(CodeValidationFailed, domain.T("template_invalid", "template {{template}} is invalid: {{file}}",
			"template", id, "file", t.Path))
		e.Validation = t.Validation
		return Template{}, e
	}
	md := templateMetadata{ID: a.freeTemplateID(strings.TrimPrefix(id, BuiltinPrefix) + "-copy"),
		Name: map[string]string{}, Description: map[string]string{}}
	for _, lang := range templateLanguages {
		md.Name[lang], md.Description[lang] = t.Name[lang]+" (copy)", t.Description[lang]
	}
	out, err := withMetadata(t.content, md)
	if err != nil {
		return Template{}, err
	}
	return a.createUserTemplate(md.ID, out)
}

// ValidateTemplate checks an edit of the template id as UpdateTemplate does, and writes nothing. The result carries the
// edit both as a document and as YAML, so that the template's form and its YAML editor each show the other's.
func (a *App) ValidateTemplate(id string, in TemplateInput) *ValidationResult {
	return a.templateEdit(id, in).Validation
}

// userTemplate is the user's template id. A built-in template's identifier, or one with the hf- prefix that none has,
// is a conflict, since built-in templates are read-only; any other no template of the user's has is not found.
func (a *App) userTemplate(id string) (*userTemplate, error) {
	if a.builtinTemplate(id) != nil || strings.HasPrefix(id, BuiltinPrefix) {
		return nil, newError(CodeConflict, domain.T("template_read_only",
			"{{template}} is the identifier of a built-in template: built-in templates ship with HarmFlow, which never writes one",
			"template", id))
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if u, ok := a.userTemplates[id]; ok {
		return u, nil
	}
	return nil, newError(CodeNotFound, domain.T("template_not_found", "no template {{template}}", "template", id))
}

// freeTemplateID is base when no template has it, otherwise base with the first of -2, -3, … that none has, cut so that
// it stays an identifier.
func (a *App) freeTemplateID(base string) string {
	taken := func(id string) bool {
		if a.builtinTemplate(id) != nil {
			return true
		}
		a.mu.Lock()
		_, ok := a.userTemplates[id]
		a.mu.Unlock()
		return ok
	}
	id := strings.TrimRight(base[:min(len(base), maxIDLength)], "-")
	for n := 2; taken(id); n++ {
		suffix := "-" + strconv.Itoa(n)
		id = strings.TrimRight(base[:min(len(base), maxIDLength-len(suffix))], "-") + suffix
	}
	return id
}

// withMetadata is a template file whose metadata gives way to md, its spec kept as the file writes it, comments
// included.
func withMetadata(content []byte, md templateMetadata) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(content, &root); err != nil {
		return nil, err
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("the template file is not a mapping")
	}
	var mdNode yaml.Node
	if err := mdNode.Encode(md); err != nil {
		return nil, err
	}
	if v := mappingValue(root.Content[0], "metadata"); v != nil {
		*v = mdNode
	}
	return encodeNode(&root)
}

// templateMetadata is a template's metadata, in the order a template file writes it.
type templateMetadata struct {
	ID          string            `yaml:"id"`
	Name        map[string]string `yaml:"name"`
	Description map[string]string `yaml:"description"`
}

// templateFromTask turns a task file into the file of a template that in names. The task's spec is kept as its file
// writes it, comments included, but its enabled state and its origin; its metadata gives way to the template's.
func templateFromTask(content []byte, in SaveAsTemplateInput) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(content, &root); err != nil {
		return nil, err
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("the task file is not a mapping")
	}
	md := templateMetadata{ID: in.ID, Name: map[string]string{}, Description: map[string]string{}}
	for _, lang := range templateLanguages {
		md.Name[lang], md.Description[lang] = in.Name, in.Description
	}
	var mdNode yaml.Node
	if err := mdNode.Encode(md); err != nil {
		return nil, err
	}
	m := root.Content[0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		switch m.Content[i].Value {
		case "kind":
			m.Content[i+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: kindTemplate}
		case "metadata":
			m.Content[i+1] = &mdNode
		case "spec":
			spec := m.Content[i+1]
			if spec.Kind != yaml.MappingNode {
				continue
			}
			var kept []*yaml.Node
			for j := 0; j+1 < len(spec.Content); j += 2 {
				if k := spec.Content[j].Value; k != "enabled" && k != "origin" {
					kept = append(kept, spec.Content[j], spec.Content[j+1])
				}
			}
			spec.Content = kept
		}
	}
	return encodeNode(&root)
}

// scanTemplateFiles detects the changes made to the user's template files outside HarmFlow: a file added or changed
// is read again, and one that fails validation is listed with its errors and no longer offered; a file removed takes
// its template out of the catalog. A task created from a template is its own copy, so no task changes with it.
func (a *App) scanTemplateFiles() {
	if a.d.Templates == nil {
		return
	}
	files, err := a.d.Templates.List()
	if err != nil {
		return
	}
	seen := map[string]bool{}
	for _, f := range files {
		seen[f.ID] = true
		a.mu.Lock()
		u := a.userTemplates[f.ID]
		changed := u == nil || u.file.Revision != f.Revision
		a.mu.Unlock()
		if !changed {
			continue
		}
		t := a.readUserTemplate(f)
		a.mu.Lock()
		a.userTemplates[f.ID] = &userTemplate{t: t, file: f}
		a.mu.Unlock()
		if !t.Validation.Valid {
			a.emit(templateEvent(domain.EventTemplateInvalid, f, t, domain.T("template_changed_externally_invalid",
				"the template file {{file}} was changed outside HarmFlow and fails validation", "file", taskFileName(f))))
			continue
		}
		a.emit(templateEvent(domain.EventTemplateChanged, f, t, domain.T("template_changed_externally",
			"the template file {{file}} was changed outside HarmFlow", "file", taskFileName(f))))
	}
	a.mu.Lock()
	var gone []ports.TaskFile
	for id, u := range a.userTemplates {
		if !seen[id] {
			gone = append(gone, u.file)
			delete(a.userTemplates, id)
		}
	}
	a.mu.Unlock()
	slices.SortFunc(gone, func(x, y ports.TaskFile) int { return strings.Compare(x.ID, y.ID) })
	for _, f := range gone {
		f.Revision = ""
		a.emit(templateEvent(domain.EventTemplateDeleted, f, nil, domain.T("template_removed_externally",
			"the template file {{file}} was removed outside HarmFlow", "file", taskFileName(f))))
	}
}
