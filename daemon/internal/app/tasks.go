package app

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/eventlog"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/schema"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// TaskView is a task as the API returns it.
type TaskView struct {
	ID                string           `json:"id"`
	Name              string           `json:"name"`
	Description       string           `json:"description,omitempty"`
	Type              string           `json:"type"`
	Enabled           bool             `json:"enabled"`
	Revision          string           `json:"revision"`
	Harness           string           `json:"harness"`
	FallbackHarnesses []string         `json:"fallbackHarnesses"`
	Schedule          string           `json:"schedule,omitempty"`
	Timezone          string           `json:"timezone,omitempty"`
	NextFireAt        *time.Time       `json:"nextFireAt,omitempty"`
	Running           bool             `json:"running"`
	Queued            bool             `json:"queued"`
	DeletePending     bool             `json:"deletePending,omitempty"`
	Repositories      []string         `json:"repositories"`
	LastExecution     *ExecutionRef    `json:"lastExecution,omitempty"`
	Warnings          []domain.Warning `json:"warnings"`
	Invalid           bool             `json:"invalid,omitempty"`

	Document   any               `json:"document,omitempty"`
	Effective  any               `json:"effective,omitempty"`
	YAML       string            `json:"yaml,omitempty"`
	Path       string            `json:"path,omitempty"`
	Validation *ValidationResult `json:"validation,omitempty"`
}

type ExecutionRef struct {
	ID        string                 `json:"id"`
	Status    domain.ExecutionStatus `json:"status"`
	CreatedAt time.Time              `json:"createdAt"`
	EndedAt   *time.Time             `json:"endedAt,omitempty"`
}

// loadTasks reads every task file; a file that fails validation is listed as invalid and never fires.
func (a *App) loadTasks() error {
	files, err := a.d.Tasks.List()
	if err != nil {
		return err
	}
	for _, f := range files {
		a.loadTaskFile(f)
	}
	return nil
}

func (a *App) loadTaskFile(f ports.TaskFile) *taskEntry {
	r := a.validate(TaskInput{YAML: string(f.Content)}, catalogLenient)
	a.mu.Lock()
	defer a.mu.Unlock()
	e := a.tasks[f.ID]
	if e == nil {
		e = &taskEntry{}
		a.tasks[f.ID] = e
	}
	e.file = f
	if r.Valid && r.doc.Metadata.ID != f.ID {
		r.Valid = false
		r.addError("/metadata/id", domain.T("id_file_mismatch", `metadata.id "{{id}}" must match the file name {{file}}.yaml`,
			"id", r.doc.Metadata.ID, "file", f.ID))
	}
	if r.Valid {
		e.doc = *r.doc
		e.invalid = nil
		e.warnings = issuesToWarnings(r.Warnings)
	} else {
		e.invalid = r
		if e.doc.Metadata.ID == "" {
			// Never valid since it was loaded: keep a skeleton so the dashboard can list it and show the errors.
			e.doc = domain.TaskDocument{Metadata: domain.TaskMetadata{ID: f.ID, Name: f.ID}}
			e.doc.Spec.Enabled = boolPtr(false)
		}
		text := domain.T("task_file_invalid", "the task file fails validation")
		if len(r.Errors) > 0 {
			text = r.Errors[0].Text
		}
		e.warnings = []domain.Warning{{Code: domain.WarningExternalEditInvalid, Text: text}}
	}
	return e
}

func boolPtr(b bool) *bool { return &b }

func issuesToWarnings(issues []schemaIssue) []domain.Warning {
	out := []domain.Warning{}
	for _, is := range issues {
		code := is.Code
		switch code {
		case domain.WarningPolicyUnenforced, domain.WarningMissingAgent, domain.WarningMissingSkill,
			domain.WarningInvalidFallback, domain.WarningHarnessNotReady:
		default:
			code = domain.WarningOther
		}
		out = append(out, domain.Warning{Code: code, Text: is.Text, Subject: is.Path})
	}
	return out
}

func (a *App) entry(id string) (*taskEntry, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.tasks[id]
	if !ok {
		return nil, newError(CodeNotFound, domain.T("task_not_found", "no task {{task}}", "task", id))
	}
	return e, nil
}

// ListTasks returns every task with its next firing and latest execution.
func (a *App) ListTasks() []TaskView {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]TaskView, 0, len(a.tasks))
	for _, id := range sortedKeys(a.tasks) {
		out = append(out, a.viewLocked(id, false))
	}
	return out
}

func (a *App) GetTask(id string) (TaskView, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.tasks[id]; !ok {
		return TaskView{}, newError(CodeNotFound, domain.T("task_not_found", "no task {{task}}", "task", id))
	}
	return a.viewLocked(id, true), nil
}

func (a *App) viewLocked(id string, full bool) TaskView {
	e := a.tasks[id]
	doc := e.doc
	eff := doc.Effective()
	v := TaskView{
		ID: id, Name: doc.Metadata.Name, Description: doc.Metadata.Description, Type: doc.Spec.Type,
		Enabled: doc.IsEnabled() && e.invalid == nil, Revision: e.file.Revision, Harness: eff.Spec.Harness.Preferred.Provider,
		FallbackHarnesses: []string{}, Schedule: doc.ScheduleRule(), Repositories: []string{},
		DeletePending: e.deletePending, Warnings: append([]domain.Warning{}, e.warnings...), Invalid: e.invalid != nil,
	}
	if e.invalid != nil && e.doc.Spec.Type == "" {
		v.Enabled = false
	}
	if f := eff.Spec.Harness.Fallback; f != nil { // a script task has no harness
		for _, c := range f.Candidates {
			v.FallbackHarnesses = append(v.FallbackHarnesses, c.Provider)
		}
	}
	if doc.Spec.Schedule != nil {
		v.Timezone = doc.Spec.Schedule.Timezone
		if v.Timezone == "" {
			v.Timezone = a.d.Location.String()
		}
	}
	if s := a.sched[id]; s != nil && v.Enabled && s.NextFireAt != nil {
		t := *s.NextFireAt
		v.NextFireAt = &t
	}
	for _, r := range doc.Repositories() {
		v.Repositories = append(v.Repositories, r.Name)
	}
	_, v.Running = a.running[id]
	var last *domain.Execution
	for _, x := range a.executions {
		if x.TaskID != id {
			continue
		}
		if x.Status == domain.StatusQueued {
			v.Queued = true
		}
		if last == nil || x.CreatedAt.After(last.CreatedAt) {
			last = x
		}
	}
	if last != nil {
		v.LastExecution = &ExecutionRef{ID: last.ID, Status: last.Status, CreatedAt: last.CreatedAt, EndedAt: last.EndedAt}
	}
	if full {
		if e.invalid != nil {
			v.Validation = e.invalid
			v.Document = e.invalid.Document
		} else {
			v.Document = toMap(doc)
		}
		v.Effective = toMap(eff)
		v.YAML = string(e.file.Content)
		v.Path = e.file.Path
	}
	return v
}

// invalidError says what the first problem is; the validation it carries lists them all.
func (a *App) invalidError(r *ValidationResult) *Error {
	text := domain.T("task_invalid", "the task fails validation")
	if len(r.Errors) > 0 {
		text = r.Errors[0].Text
		if r.Errors[0].Path != "" {
			text.Message = r.Errors[0].Path + ": " + text.Message
		}
	}
	return &Error{Code: CodeValidationFailed, Text: text, Validation: r}
}

// CreateTask validates and writes a new task file; the daemon is its only writer (FS-001 FR-004).
func (a *App) CreateTask(in TaskInput) (TaskView, error) {
	return a.createTask(in, catalogStrict, false, false)
}

// ImportTask accepts a portable task, warning about agents and skills this machine lacks (FS-001 FR-018). An imported
// script task is saved disabled, whatever its file says: it runs a program with the user's rights, and runs only once
// the user has seen it and enabled it (FS-013 SEC-001).
func (a *App) ImportTask(yamlText string, overwrite bool) (TaskView, error) {
	return a.createTask(TaskInput{YAML: yamlText}, catalogLenient, overwrite, true)
}

func (a *App) createTask(in TaskInput, mode catalogMode, overwrite, imported bool) (TaskView, error) {
	r := a.validate(in, mode)
	if !r.Valid {
		return TaskView{}, a.invalidError(r)
	}
	if imported && r.doc.IsScript() {
		if err := disableImported(r); err != nil {
			return TaskView{}, err
		}
	}
	id := r.doc.Metadata.ID
	a.mu.Lock()
	_, exists := a.tasks[id]
	_, running := a.running[id]
	a.mu.Unlock()
	if exists && (!overwrite || running) {
		return TaskView{}, newError(CodeConflict,
			domain.T("task_exists", "a task with the identifier {{task}} already exists", "task", id))
	}
	if _, err := a.d.Tasks.Get(id); err == nil && !overwrite {
		return TaskView{}, newError(CodeConflict,
			domain.T("task_file_exists", "a task file {{task}}.yaml already exists", "task", id))
	}
	f, err := a.d.Tasks.Save(id, r.content, "")
	if err != nil {
		return TaskView{}, err
	}
	a.afterWrite(f, domain.EventTaskCreated, r)
	v, _ := a.GetTask(id)
	v.Validation = r
	return v, nil
}

// UpdateTask writes an edit that started from baseRevision; a file changed meanwhile is a conflict, never a silent
// overwrite (FS-001 FR-005). A running execution keeps the definition it started with (D-083).
func (a *App) UpdateTask(id string, in TaskInput) (TaskView, error) {
	e, err := a.entry(id)
	if err != nil {
		return TaskView{}, err
	}
	if in.BaseRevision == "" {
		return TaskView{}, newError(CodeBadRequest,
			domain.T("base_revision_required", "baseRevision is required to update a task"))
	}
	r := a.validate(in, catalogStrict)
	if !r.Valid {
		return TaskView{}, a.invalidError(r)
	}
	if r.doc.Metadata.ID != id {
		return TaskView{}, a.invalidError(withError(r, "/metadata/id",
			domain.T("id_immutable", "the identifier cannot change; duplicate the task instead")))
	}
	f, err := a.d.Tasks.Save(id, r.content, in.BaseRevision)
	if errors.Is(err, ports.ErrConflict) {
		return TaskView{}, &Error{Code: CodeConflict, CurrentRevision: f.Revision, Text: domain.T("task_changed_on_disk",
			"the task file changed since this edit started; reload it before saving")}
	}
	if err != nil {
		return TaskView{}, err
	}
	_ = e
	a.afterWrite(f, domain.EventTaskUpdated, r)
	v, _ := a.GetTask(id)
	v.Validation = r
	return v, nil
}

func withError(r *ValidationResult, path string, text domain.Text) *ValidationResult {
	r.Valid = false
	r.addError(path, text)
	return r
}

func (a *App) afterWrite(f ports.TaskFile, eventType string, r *ValidationResult) {
	a.mu.Lock()
	e := a.tasks[f.ID]
	if e == nil {
		e = &taskEntry{}
		a.tasks[f.ID] = e
	}
	e.file = f
	e.doc = *r.doc
	e.invalid = nil
	e.warnings = issuesToWarnings(r.Warnings)
	e.selfRevision = f.Revision
	enabled := e.doc.IsEnabled()
	a.mu.Unlock()
	a.rescheduleTask(f.ID)
	if !enabled {
		a.dropQueued(f.ID, domain.T("task_was_disabled", "the task was disabled"))
	}
	a.emit(domain.Event{Type: eventType, TaskID: f.ID, Message: r.doc.Metadata.Name,
		Payload: map[string]any{"revision": f.Revision}})
}

// DeleteTask removes a task; a running one goes once its execution finishes (D-083). Its records stay.
func (a *App) DeleteTask(id string) (bool, error) {
	a.mu.Lock()
	e, ok := a.tasks[id]
	if !ok {
		a.mu.Unlock()
		return false, newError(CodeNotFound, domain.T("task_not_found", "no task {{task}}", "task", id))
	}
	if _, running := a.running[id]; running {
		e.deletePending = true
		a.mu.Unlock()
		a.dropQueued(id, domain.T("task_being_deleted", "the task is being deleted"))
		a.emit(domain.Event{Type: domain.EventTaskUpdated, TaskID: id, Payload: map[string]any{"deletePending": true}}.Says(
			domain.T("task_delete_pending", "deletion waits for the running execution")))
		return true, nil
	}
	a.mu.Unlock()
	a.dropQueued(id, domain.T("task_was_deleted", "the task was deleted"))
	return false, a.removeTask(id)
}

func (a *App) removeTask(id string) error {
	if err := a.d.Tasks.Delete(id); err != nil && !errors.Is(err, ports.ErrNotFound) {
		return err
	}
	a.mu.Lock()
	delete(a.tasks, id)
	delete(a.sched, id)
	a.mu.Unlock()
	a.saveSchedule()
	a.emit(domain.Event{Type: domain.EventTaskDeleted, TaskID: id})
	return nil
}

// DuplicateTask copies a task under a new identifier, disabled (FS-001 AC-002).
func (a *App) DuplicateTask(id string) (TaskView, error) {
	e, err := a.entry(id)
	if err != nil {
		return TaskView{}, err
	}
	a.mu.Lock()
	doc := e.doc
	newID := id + "-copy"
	for n := 2; a.tasks[newID] != nil; n++ {
		newID = fmt.Sprintf("%s-copy-%d", id, n)
	}
	a.mu.Unlock()
	if len(newID) > 64 {
		newID = newID[len(newID)-64:]
		newID = strings.TrimLeft(newID, "-")
	}
	doc.Metadata.ID = newID
	doc.Metadata.Name = doc.Metadata.Name + " (copy)"
	doc.Spec.Enabled = boolPtr(false)
	out, err := marshalYAML(doc)
	if err != nil {
		return TaskView{}, err
	}
	return a.createTask(TaskInput{YAML: string(out)}, catalogLenient, false, false)
}

// disableImported sets spec.enabled to false in an imported script task's YAML, keeping the rest of the file, and says
// so in the warnings the import returns (FS-013 SEC-001).
func disableImported(r *ValidationResult) error {
	out, err := setYAMLField(r.content, "spec", "enabled", false)
	if err != nil {
		return err
	}
	r.content, r.YAML = out, string(out)
	_, r.node, _ = schema.ParseYAML(out)
	r.doc.Spec.Enabled = boolPtr(false)
	r.addWarning("/spec/enabled", domain.WarningOther, domain.T("script_imported_disabled",
		"an imported script task is saved disabled; check its program, then enable it"))
	return nil
}

// SetEnabled toggles spec.enabled in place, keeping the file's comments. Disabling lets a running execution finish
// and drops a waiting firing (D-083).
func (a *App) SetEnabled(id string, enabled bool) (TaskView, error) {
	e, err := a.entry(id)
	if err != nil {
		return TaskView{}, err
	}
	a.mu.Lock()
	content, rev := e.file.Content, e.file.Revision
	invalid := e.invalid
	a.mu.Unlock()
	if invalid != nil {
		return TaskView{}, a.invalidError(invalid)
	}
	out, err := setYAMLField(content, "spec", "enabled", enabled)
	if err != nil {
		return TaskView{}, err
	}
	return a.UpdateTask(id, TaskInput{YAML: string(out), BaseRevision: rev})
}

// ExportTask returns the portable YAML: the definition only, with no state, logs, or credentials (FS-001 FR-006).
func (a *App) ExportTask(id string) ([]byte, error) {
	e, err := a.entry(id)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	content := e.file.Content
	a.mu.Unlock()
	return []byte(eventlog.Redact(string(content))), nil
}

// watchLoop detects external edits of task files (FS-001 FR-005).
func (a *App) watchLoop() {
	defer a.wg.Done()
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-a.stopCh:
			return
		case <-t.C:
			a.scanTaskFiles()
		}
	}
}

func (a *App) scanTaskFiles() {
	files, err := a.d.Tasks.List()
	if err != nil {
		return
	}
	seen := map[string]bool{}
	for _, f := range files {
		seen[f.ID] = true
		a.mu.Lock()
		e := a.tasks[f.ID]
		changed := e == nil || e.file.Revision != f.Revision
		self := e != nil && e.selfRevision == f.Revision
		a.mu.Unlock()
		if !changed || self {
			continue
		}
		ne := a.loadTaskFile(f)
		a.rescheduleTask(f.ID)
		a.mu.Lock()
		invalid := ne.invalid
		a.mu.Unlock()
		if invalid != nil {
			a.emit(domain.Event{Type: domain.EventTaskInvalid, TaskID: f.ID, Severity: domain.SeverityWarning,
				Payload: map[string]any{"errors": invalid.Errors, "revision": f.Revision}}.Says(domain.T("task_changed_externally_invalid",
				"the task file was changed outside HarmFlow and fails validation; the last valid definition stays")))
			continue
		}
		a.emit(domain.Event{Type: domain.EventTaskChangedExternally, TaskID: f.ID, Payload: map[string]any{"revision": f.Revision}}.Says(
			domain.T("task_changed_externally", "the task file was changed outside HarmFlow")))
	}
	a.mu.Lock()
	var gone []string
	for id, e := range a.tasks {
		if !seen[id] {
			if _, running := a.running[id]; running {
				e.deletePending = true
				continue
			}
			gone = append(gone, id)
		}
	}
	for _, id := range gone {
		delete(a.tasks, id)
		delete(a.sched, id)
	}
	a.mu.Unlock()
	for _, id := range gone {
		a.dropQueued(id, domain.T("task_file_removed", "the task file was removed"))
		a.emit(domain.Event{Type: domain.EventTaskDeleted, TaskID: id}.Says(
			domain.T("task_removed_externally", "the task file was removed outside HarmFlow")))
	}
}

// expand resolves ${NAME} references with the path variables of Settings.
func (a *App) expand(p string) (string, error) {
	a.mu.Lock()
	vars := map[string]string{}
	for k, v := range a.settings.PathVariables {
		vars[k] = v
	}
	a.mu.Unlock()
	return domain.ExpandPath(p, a.d.Paths.UserHome, vars, os.Getenv)
}
