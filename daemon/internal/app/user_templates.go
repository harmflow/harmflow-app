package app

import (
	"errors"
	"fmt"
	"slices"
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
	// The identifier is checked as a file's is, before it names any file.
	t := a.loadTemplate(&Template{ID: in.ID, Source: TemplateUser}, out, nil)
	if !t.Validation.Valid {
		return Template{}, a.invalidError(t.Validation)
	}
	a.mu.Lock()
	_, exists := a.userTemplates[in.ID]
	a.mu.Unlock()
	if _, err := a.d.Templates.Get(in.ID); exists || err == nil {
		return Template{}, newError(CodeConflict,
			domain.T("template_exists", "a template with the identifier {{template}} already exists", "template", in.ID))
	}
	f, err := a.d.Templates.Save(in.ID, out, "")
	if err != nil {
		return Template{}, err
	}
	t.Path = f.Path
	a.mu.Lock()
	a.userTemplates[in.ID] = &userTemplate{t: t, file: f}
	a.mu.Unlock()
	ev := templateEvent(domain.EventTemplateCreated, f, t, domain.Text{})
	ev.Message = in.Name
	a.emit(ev)
	return *t, nil
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
