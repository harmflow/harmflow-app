package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// notesTask is a task worth repeating: a prompt copied from an agent, with its origin, a schedule, and a skill.
const notesTask = `apiVersion: harmflow/v1alpha1
kind: Task
metadata:
  id: weekly
  name: Weekly notes
  description: Sums up my week.
spec:
  enabled: true
  type: general
  # The prompt, customized from the agent's.
  prompt: Sum up the week's notes.
  origin:
    agent: hf-notes-writer
    hash: sha256:` + "5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e" + `
  skills: [hf-pr-notes]
  schedule:
    cron: "0 9 * * 1"
  harness:
    preferred:
      provider: claude-code
`

// withUserTemplates writes template files into the env's templates directory before the App starts, by file name.
func withUserTemplates(files map[string]string) func(*env) {
	return func(e *env) {
		if err := os.MkdirAll(e.paths.Templates, 0o700); err != nil {
			e.t.Fatal(err)
		}
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(e.paths.Templates, name), []byte(content), 0o600); err != nil {
				e.t.Fatal(err)
			}
		}
	}
}

// newNotesEnv starts an App with one built-in template and the notes task.
func newNotesEnv(t *testing.T, configure ...func(*env)) *env {
	t.Helper()
	configure = append([]func(*env){withTemplates(map[string]string{
		"hf-general.yaml": template("hf-general", "  type: general\n"),
	})}, configure...)
	e := newEnv(t, configure...)
	if _, err := e.app.CreateTask(t.Context(), TaskInput{YAML: notesTask}); err != nil {
		t.Fatalf("CreateTask(weekly) = %v, want the task", err)
	}
	return e
}

func saveAsTemplate(t *testing.T, e *env, taskID, id, name string) Template {
	t.Helper()
	tp, err := e.app.SaveAsTemplate(taskID, SaveAsTemplateInput{ID: id, Name: name, Description: "Sums up the week."})
	if err != nil {
		t.Fatalf("SaveAsTemplate(%s, %s) = %v, want a template", taskID, id, err)
	}
	return tp
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// mustJSON is v as JSON, the document a form sends.
func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// templateByID is the template Templates lists with the identifier id; ok is false when it lists none.
func templateByID(e *env, id string) (tp Template, ok bool) {
	for _, tp := range e.app.Templates(e.t.Context()) {
		if tp.ID == id {
			return tp, true
		}
	}
	return Template{}, false
}

// Saving a task as a template writes one YAML file in the templates directory, holding the task's type and fields but
// its identifier, name, enabled state, and origin, with the name and description in every language as the user wrote
// them. The catalog lists it after the built-in templates, and a task created from it takes the same new-task route,
// named after it, with the task's prompt and the rest of its fields.
func TestSaveTaskAsTemplate(t *testing.T) {
	e := newNotesEnv(t)
	taskFile := filepath.Join(e.paths.Tasks, "weekly.yaml")
	before := readFile(t, taskFile)
	tp := saveAsTemplate(t, e, "weekly", "weekly-notes", "Notas semanais")
	if !tp.Validation.Valid || tp.Source != TemplateUser || tp.Type != "general" ||
		tp.Path != filepath.Join(e.paths.Templates, "weekly-notes.yaml") {
		t.Errorf("SaveAsTemplate = %+v, want a valid template of the user's at its file", tp)
	}
	for _, lang := range templateLanguages {
		if tp.Name[lang] != "Notas semanais" || tp.Description[lang] != "Sums up the week." {
			t.Errorf("SaveAsTemplate %s name %q, description %q, want them as written", lang, tp.Name[lang], tp.Description[lang])
		}
	}
	file := readFile(t, tp.Path)
	for _, want := range []string{"kind: TaskTemplate", "id: weekly-notes", "prompt: Sum up the week's notes.",
		"# The prompt, customized from the agent's.", "cron: \"0 9 * * 1\"", "skills: [hf-pr-notes]"} {
		if !strings.Contains(file, want) {
			t.Errorf("the template file lacks %q:\n%s", want, file)
		}
	}
	for _, left := range []string{"enabled", "origin", "hf-notes-writer", "id: weekly\n", "Weekly notes", "Sums up my week",
		"selectedAgent"} {
		if strings.Contains(file, left) {
			t.Errorf("the template file holds %q, want it left out:\n%s", left, file)
		}
	}
	if after := readFile(t, taskFile); after != before {
		t.Errorf("the task file after SaveAsTemplate = %q, want it unchanged", after)
	}
	var ids []string
	for _, tp := range e.app.Templates(t.Context()) {
		ids = append(ids, tp.Source+":"+tp.ID)
	}
	if want := []string{"builtin:hf-general", "user:weekly-notes"}; !slices.Equal(ids, want) {
		t.Errorf("Templates() = %v, want %v", ids, want)
	}
	created := e.published(domain.EventTemplateCreated)
	if len(created) != 1 || created[0].Payload["template"] != "weekly-notes" || created[0].Payload["file"] != "weekly-notes.yaml" ||
		created[0].Message != "Notas semanais" {
		t.Errorf("template.created events = %+v, want one for weekly-notes", created)
	}

	doc := draft(t, e, "weekly-notes", "es-ES")
	md, spec := doc["metadata"].(map[string]any), doc["spec"].(map[string]any)
	if md["id"] != "notas-semanais" || md["name"] != "Notas semanais" {
		t.Errorf("DraftTask(weekly-notes) metadata = %v, want the template's name and an identifier from it", md)
	}
	if spec["enabled"] != true || spec["prompt"] != "Sum up the week's notes." || spec["origin"] != nil ||
		!reflect.DeepEqual(spec["skills"], []any{"hf-pr-notes"}) ||
		!reflect.DeepEqual(spec["schedule"], map[string]any{"cron": "0 9 * * 1"}) {
		t.Errorf("DraftTask(weekly-notes) spec = %v, want the task's fields with no origin", spec)
	}
	v, err := e.app.CreateTask(t.Context(), TaskInput{Document: mustJSON(t, doc)})
	if err != nil || v.ID != "notas-semanais" {
		t.Fatalf("CreateTask(the draft of weekly-notes) = %+v, %v, want the task notas-semanais", v, err)
	}
}

// A user template's identifier never carries the hf- prefix, and is unique among templates: a request that gives one
// with the prefix fails validation, and one a template of the user's already has, its file valid or not, is a
// conflict, both naming the identifier, and nothing is written. An identifier that is no identifier never names a
// file, and a task whose file fails validation, or that does not exist, gives no template.
func TestSaveAsTemplateRefusesItsIdentifier(t *testing.T) {
	e := newNotesEnv(t, withUserTemplates(map[string]string{"broken.yaml": "kind: [unclosed\n"}))
	saveAsTemplate(t, e, "weekly", "weekly-notes", "Notas")
	entries := func() []string {
		var names []string
		files, _ := os.ReadDir(e.paths.Templates)
		for _, f := range files {
			names = append(names, f.Name())
		}
		return names
	}
	written := entries()
	in := func(id string) SaveAsTemplateInput {
		return SaveAsTemplateInput{ID: id, Name: "Notas", Description: "Notas."}
	}

	_, err := e.app.SaveAsTemplate("weekly", in("hf-notes"))
	if ae := appError(t, "SaveAsTemplate(hf-notes)", err, CodeValidationFailed, "template_id_reserved"); ae != nil {
		i := slices.IndexFunc(ae.Validation.Errors, func(is schemaIssue) bool { return is.Key == "template_id_reserved" })
		if i < 0 || ae.Validation.Errors[i].Params["id"] != "hf-notes" || ae.Validation.Errors[i].Path != "/metadata/id" {
			t.Errorf("SaveAsTemplate(hf-notes) errors = %+v, want one naming hf-notes at /metadata/id", ae.Validation.Errors)
		}
	}
	for _, id := range []string{"weekly-notes", "broken"} {
		_, err := e.app.SaveAsTemplate("weekly", in(id))
		if ae := appError(t, "SaveAsTemplate("+id+")", err, CodeConflict, "template_exists"); ae != nil && ae.Params["template"] != id {
			t.Errorf("SaveAsTemplate(%s) params = %v, want it named", id, ae.Params)
		}
	}
	_, err = e.app.SaveAsTemplate("weekly", in("../outside"))
	appError(t, "SaveAsTemplate(../outside)", err, CodeValidationFailed, "schema_pattern")
	_, err = e.app.SaveAsTemplate("weekly", SaveAsTemplateInput{ID: "unnamed", Description: "Notas."})
	if err == nil {
		t.Error("SaveAsTemplate(unnamed, with no name) = nil, want a validation error")
	}
	_, err = e.app.SaveAsTemplate("none", in("mine"))
	appError(t, "SaveAsTemplate(none)", err, CodeNotFound, "task_not_found")

	if err := os.WriteFile(filepath.Join(e.paths.Tasks, "weekly.yaml"), []byte("kind: [unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.app.scanTaskFiles(t.Context())
	_, err = e.app.SaveAsTemplate("weekly", in("from-invalid"))
	appError(t, "SaveAsTemplate(weekly, its file invalid)", err, CodeValidationFailed, "task_file_error")

	if after := entries(); !slices.Equal(after, written) {
		t.Errorf("the templates directory after the refusals = %v, want %v", after, written)
	}
	if _, err := os.Stat(filepath.Join(e.paths.Home, "outside.yaml")); err == nil {
		t.Error("SaveAsTemplate(../outside) wrote outside the templates directory")
	}
}

// A user template file that fails validation is listed with its errors and never offered, and the other templates
// stay loaded: a file it cannot parse, one whose identifier is not its file's name, and one whose identifier carries
// the hf- prefix, named in the error. Each is announced at start.
func TestInvalidUserTemplateIsListed(t *testing.T) {
	e := newEnv(t, withUserTemplates(map[string]string{
		"broken.yaml":  "apiVersion: harmflow/v1alpha1\nkind: [unclosed\n",
		"moved.yaml":   template("other", "  type: general\n"),
		"hf-mine.yaml": template("hf-mine", "  type: general\n"),
		"good.yaml":    template("good", "  type: general\n  prompt: Sum up.\n"),
	}))
	cases := []struct{ id, key, path string }{
		{"broken", "yaml_syntax", ""},
		{"moved", "id_file_mismatch", "/metadata/id"},
		{"hf-mine", "template_id_reserved", "/metadata/id"},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			tp, ok := templateByID(e, c.id)
			if !ok || tp.Validation.Valid || tp.Source != TemplateUser {
				t.Fatalf("Templates() %s = %+v, want it listed as an invalid template of the user's", c.id, tp)
			}
			i := slices.IndexFunc(tp.Validation.Errors, func(is schemaIssue) bool { return is.Key == c.key })
			if i < 0 || tp.Validation.Errors[i].Path != c.path {
				t.Fatalf("Templates() %s errors = %+v, want %s at %q", c.id, tp.Validation.Errors, c.key, c.path)
			}
			if c.key == "template_id_reserved" && tp.Validation.Errors[i].Params["id"] != "hf-mine" {
				t.Errorf("Templates() %s errors = %+v, want the identifier named", c.id, tp.Validation.Errors)
			}
			_, err := e.app.DraftTask(t.Context(), c.id, "en-US")
			appError(t, "DraftTask("+c.id+")", err, CodeValidationFailed, "template_invalid")
		})
	}
	if tp, ok := templateByID(e, "good"); !ok || !tp.Validation.Valid {
		t.Errorf("Templates() good = %+v, want it valid beside the invalid ones", tp)
	}
	var announced []string
	for _, ev := range e.published(domain.EventTemplateInvalid) {
		announced = append(announced, ev.Payload["template"].(string))
		if ev.MessageKey != "template_file_invalid" || ev.Severity != domain.SeverityWarning {
			t.Errorf("template.invalid event = %+v, want template_file_invalid as a warning", ev)
		}
	}
	slices.Sort(announced)
	if want := []string{"broken", "hf-mine", "moved"}; !slices.Equal(announced, want) {
		t.Errorf("template.invalid events at start name %v, want %v", announced, want)
	}
}

// A user template file changed outside HarmFlow is read again, and the catalog shows the change; one changed into an
// invalid file is listed with its errors and no longer offered, and one removed leaves the catalog. Each change is
// announced once, and a file the daemon wrote itself is no outside change.
func TestUserTemplateChangedOutside(t *testing.T) {
	e := newNotesEnv(t)
	tp := saveAsTemplate(t, e, "weekly", "weekly-notes", "Notas semanais")
	e.app.scanTemplateFiles()
	if got := e.published(domain.EventTemplateChanged); len(got) != 0 {
		t.Fatalf("template.changed_externally after the daemon's own write = %+v, want none", got)
	}

	edited := strings.Replace(readFile(t, tp.Path), "Notas semanais", "Weekly notes", -1)
	if err := os.WriteFile(tp.Path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	e.app.scanTemplateFiles()
	e.app.scanTemplateFiles()
	if got, _ := templateByID(e, "weekly-notes"); !got.Validation.Valid || got.Name["pt-BR"] != "Weekly notes" {
		t.Errorf("Templates() weekly-notes after the edit = %+v, want the new name", got)
	}
	if got := e.published(domain.EventTemplateChanged); len(got) != 1 || got[0].MessageKey != "template_changed_externally" ||
		got[0].Payload["template"] != "weekly-notes" {
		t.Errorf("template.changed_externally events = %+v, want one for weekly-notes", got)
	}
	if md := draft(t, e, "weekly-notes", "pt-BR")["metadata"].(map[string]any); md["name"] != "Weekly notes" {
		t.Errorf("DraftTask(weekly-notes) after the edit metadata = %v, want the new name", md)
	}

	if err := os.WriteFile(tp.Path, []byte(strings.Replace(edited, "type: general", "type: email", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	e.app.scanTemplateFiles()
	if got, _ := templateByID(e, "weekly-notes"); got.Validation.Valid || got.Name != nil {
		t.Errorf("Templates() weekly-notes after an invalid edit = %+v, want it listed as invalid", got)
	}
	if got := e.published(domain.EventTemplateInvalid); len(got) != 1 || got[0].MessageKey != "template_changed_externally_invalid" ||
		got[0].Payload["errors"] == nil {
		t.Errorf("template.invalid events = %+v, want one with its errors", got)
	}
	_, err := e.app.DraftTask(t.Context(), "weekly-notes", "en-US")
	appError(t, "DraftTask(weekly-notes, invalid)", err, CodeValidationFailed, "template_invalid")

	if err := os.Remove(tp.Path); err != nil {
		t.Fatal(err)
	}
	e.app.scanTemplateFiles()
	if got, ok := templateByID(e, "weekly-notes"); ok {
		t.Errorf("Templates() after the file was removed lists %+v, want it gone", got)
	}
	if got := e.published(domain.EventTemplateDeleted); len(got) != 1 || got[0].MessageKey != "template_removed_externally" ||
		got[0].Payload["file"] != "weekly-notes.yaml" {
		t.Errorf("template.deleted events = %+v, want one for weekly-notes.yaml", got)
	}
	_, err = e.app.DraftTask(t.Context(), "weekly-notes", "en-US")
	appError(t, "DraftTask(weekly-notes, removed)", err, CodeNotFound, "template_not_found")
}

// A task created from a user template is its own copy: editing the template, then deleting it, changes nothing in the
// task, whose file holds no reference to the template, and which still loads after a restart.
func TestTaskDoesNotDependOnItsTemplate(t *testing.T) {
	e := newNotesEnv(t)
	tp := saveAsTemplate(t, e, "weekly", "weekly-notes", "Notas semanais")
	v, err := e.app.CreateTask(t.Context(), TaskInput{Document: mustJSON(t, draft(t, e, "weekly-notes", "en-US"))})
	if err != nil {
		t.Fatalf("CreateTask(the draft of weekly-notes) = %v", err)
	}
	taskFile := filepath.Join(e.paths.Tasks, v.ID+".yaml")
	before := readFile(t, taskFile)
	for _, ref := range []string{"weekly-notes", "TaskTemplate", "template"} {
		if strings.Contains(before, ref) {
			t.Errorf("the task file holds %q, want no reference to its template:\n%s", ref, before)
		}
	}

	edited := strings.Replace(readFile(t, tp.Path), "Sum up the week's notes.", "Something else.", 1)
	if err := os.WriteFile(tp.Path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	e.app.scanTemplateFiles()
	e.app.scanTaskFiles(t.Context())
	if err := os.Remove(tp.Path); err != nil {
		t.Fatal(err)
	}
	e.app.scanTemplateFiles()
	e.app.scanTaskFiles(t.Context())
	if after := readFile(t, taskFile); after != before {
		t.Errorf("the task file after its template was edited and deleted = %q, want %q", after, before)
	}
	e = e.restart(t)
	got, err := e.app.GetTask(v.ID)
	if err != nil || got.Invalid || got.Document.(map[string]any)["spec"].(map[string]any)["prompt"] != "Sum up the week's notes." {
		t.Errorf("GetTask(%s) after a restart = %+v, %v, want the task as it was created", v.ID, got, err)
	}
	if _, ok := templateByID(e, "weekly-notes"); ok {
		t.Error("Templates() after a restart lists the deleted template")
	}
}

// notesBuiltin is a built-in template that sets a prompt, which a copy of it starts with.
var notesBuiltin = template("hf-notes", "  type: general\n  prompt: Sum up the meeting.\n"+
	"  harness:\n    preferred:\n      provider: claude-code\n")

// newEditingEnv starts an App with the built-in template hf-notes and no template of the user's.
func newEditingEnv(t *testing.T, configure ...func(*env)) *env {
	t.Helper()
	return newEnv(t, append([]func(*env){withTemplates(map[string]string{"hf-notes.yaml": notesBuiltin})}, configure...)...)
}

func duplicateTemplate(t *testing.T, e *env, id string) Template {
	t.Helper()
	tp, err := e.app.DuplicateTemplate(id)
	if err != nil {
		t.Fatalf("DuplicateTemplate(%s) = %v, want a copy", id, err)
	}
	return tp
}

func templateFiles(t *testing.T, e *env) []string {
	t.Helper()
	var names []string
	files, _ := os.ReadDir(e.paths.Templates)
	for _, f := range files {
		names = append(names, f.Name())
	}
	return names
}

// specOf is the spec of a template's document.
func specOf(t *testing.T, tp Template) map[string]any {
	t.Helper()
	doc, _ := tp.Document.(map[string]any)
	spec, ok := doc["spec"].(map[string]any)
	if !ok {
		t.Fatalf("template %s document = %v, want one with a spec", tp.ID, tp.Document)
	}
	return spec
}

// Duplicating a built-in template makes a template of the user's that holds its type and fields, named after it with
// " (copy)" in each language, under its identifier without the hf- prefix and with -copy after it. The copy is edited
// in the form, which sends a document, and in the YAML editor, which sends the file's text; each edit starts from the
// revision the previous one wrote, and the YAML is written as sent. A task created from the copy keeps every value it
// took when the copy is deleted, and the built-in template is unchanged throughout.
func TestDuplicateBuiltinThenEditAndDelete(t *testing.T) {
	e := newEditingEnv(t)
	builtinFile := filepath.Join(e.paths.Builtins, "templates", "hf-notes.yaml")

	cp := duplicateTemplate(t, e, "hf-notes")
	if cp.ID != "notes-copy" || cp.Source != TemplateUser || !cp.Validation.Valid || cp.Type != "general" ||
		cp.Path != filepath.Join(e.paths.Templates, "notes-copy.yaml") || cp.Revision == "" || cp.YAML != readFile(t, cp.Path) {
		t.Fatalf("DuplicateTemplate(hf-notes) = %+v, want a valid template of the user's, notes-copy, with its file", cp)
	}
	wantNames := map[string]string{"pt-BR": "Notas da reunião (copy)", "en-US": "Meeting notes (copy)", "es-ES": "Notas de la reunión (copy)"}
	if !reflect.DeepEqual(cp.Name, wantNames) || cp.Description["en-US"] != "Sums up the meeting." {
		t.Errorf("DuplicateTemplate(hf-notes) name %v, description %v, want the built-in's with (copy)", cp.Name, cp.Description)
	}
	if spec := specOf(t, cp); spec["prompt"] != "Sum up the meeting." {
		t.Errorf("DuplicateTemplate(hf-notes) spec = %v, want the built-in's prompt", spec)
	}
	if created := e.published(domain.EventTemplateCreated); len(created) != 1 || created[0].Payload["template"] != "notes-copy" {
		t.Errorf("template.created events = %+v, want one for notes-copy", created)
	}

	// The form: the document with a new prompt.
	doc := cp.Document.(map[string]any)
	doc["spec"].(map[string]any)["prompt"] = "Sum up the standup."
	formed, err := e.app.UpdateTemplate("notes-copy", TemplateInput{Document: mustJSON(t, doc), BaseRevision: cp.Revision})
	if err != nil {
		t.Fatalf("UpdateTemplate(notes-copy, from the form) = %v, want it written", err)
	}
	if formed.Revision == cp.Revision || specOf(t, formed)["prompt"] != "Sum up the standup." ||
		!strings.Contains(readFile(t, cp.Path), "prompt: Sum up the standup.") {
		t.Errorf("UpdateTemplate(notes-copy, from the form) = %+v, want the new prompt at a new revision", formed)
	}

	// The YAML editor: the file's text with a schedule, and a comment, which the file keeps.
	edited := formed.YAML + "    # Mondays.\n  schedule:\n    cron: \"0 9 * * 1\"\n"
	yamled, err := e.app.UpdateTemplate("notes-copy", TemplateInput{YAML: edited, BaseRevision: formed.Revision})
	if err != nil {
		t.Fatalf("UpdateTemplate(notes-copy, from YAML) = %v, want it written", err)
	}
	if got := readFile(t, cp.Path); got != edited || yamled.YAML != edited {
		t.Errorf("notes-copy.yaml after the YAML edit = %q, want the text sent, %q", got, edited)
	}
	if updated := e.published(domain.EventTemplateUpdated); len(updated) != 2 || updated[1].Payload["template"] != "notes-copy" {
		t.Errorf("template.updated events = %+v, want two for notes-copy", updated)
	}
	e.app.scanTemplateFiles()
	if got := e.published(domain.EventTemplateChanged); len(got) != 0 {
		t.Errorf("template.changed_externally after the daemon's own writes = %+v, want none", got)
	}

	spec := draft(t, e, "notes-copy", "en-US")["spec"].(map[string]any)
	if spec["prompt"] != "Sum up the standup." || !reflect.DeepEqual(spec["schedule"], map[string]any{"cron": "0 9 * * 1"}) {
		t.Fatalf("DraftTask(notes-copy) spec = %v, want both edits", spec)
	}
	v, err := e.app.CreateTask(t.Context(), TaskInput{Document: mustJSON(t, draft(t, e, "notes-copy", "en-US"))})
	if err != nil {
		t.Fatalf("CreateTask(the draft of notes-copy) = %v", err)
	}
	taskFile := filepath.Join(e.paths.Tasks, v.ID+".yaml")
	before := readFile(t, taskFile)

	if err := e.app.DeleteTemplate("notes-copy"); err != nil {
		t.Fatalf("DeleteTemplate(notes-copy) = %v, want it deleted", err)
	}
	if _, err := os.Stat(cp.Path); !os.IsNotExist(err) {
		t.Errorf("notes-copy.yaml after DeleteTemplate: %v, want it removed", err)
	}
	if got, ok := templateByID(e, "notes-copy"); ok {
		t.Errorf("Templates() after DeleteTemplate lists %+v, want it gone", got)
	}
	if deleted := e.published(domain.EventTemplateDeleted); len(deleted) != 1 || deleted[0].Payload["file"] != "notes-copy.yaml" {
		t.Errorf("template.deleted events = %+v, want one for notes-copy.yaml", deleted)
	}
	e.app.scanTemplateFiles()
	if got := e.published(domain.EventTemplateDeleted); len(got) != 1 {
		t.Errorf("template.deleted events after a scan = %+v, want the one DeleteTemplate sent", got)
	}
	e.app.scanTaskFiles(t.Context())
	if after := readFile(t, taskFile); after != before {
		t.Errorf("the task file after its template was deleted = %q, want %q", after, before)
	}
	if got, err := e.app.GetTask(v.ID); err != nil || got.Invalid {
		t.Errorf("GetTask(%s) after its template was deleted = %+v, %v, want it valid", v.ID, got, err)
	}
	if got := readFile(t, builtinFile); got != notesBuiltin {
		t.Errorf("hf-notes.yaml = %q, want the built-in unchanged", got)
	}
}

// An edit that fails validation is refused with its errors, and leaves the file, its revision, and the catalog as they
// were, whether it came from the form or the YAML editor: a field a template never sets, an identifier that is not the
// file's name, and a type the task schema does not define. Validating the same edit gives the same errors and writes
// nothing.
func TestInvalidTemplateEditKeepsThePreviousVersion(t *testing.T) {
	e := newEditingEnv(t)
	cp := duplicateTemplate(t, e, "hf-notes")
	before := readFile(t, cp.Path)

	withSpec := func(key string, value any) TemplateInput {
		doc := cp.Document.(map[string]any)
		spec := map[string]any{}
		for k, v := range doc["spec"].(map[string]any) {
			spec[k] = v
		}
		spec[key] = value
		return TemplateInput{Document: mustJSON(t, map[string]any{"apiVersion": doc["apiVersion"], "kind": doc["kind"],
			"metadata": doc["metadata"], "spec": spec}), BaseRevision: cp.Revision}
	}
	cases := []struct {
		name string
		in   TemplateInput
		key  string
	}{
		{"enabled from the form", withSpec("enabled", true), "schema_additional_properties"},
		{"origin from the form", withSpec("origin", map[string]any{"agent": "hf-notes-writer"}), "schema_additional_properties"},
		{"identifier from YAML", TemplateInput{YAML: strings.Replace(before, "id: notes-copy", "id: other", 1), BaseRevision: cp.Revision},
			"id_file_mismatch"},
		{"type from YAML", TemplateInput{YAML: strings.Replace(before, "type: general", "type: email", 1), BaseRevision: cp.Revision},
			"template_type_unknown"},
		{"no input", TemplateInput{BaseRevision: cp.Revision}, "template_input_missing"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := e.app.UpdateTemplate("notes-copy", c.in)
			ae := appError(t, "UpdateTemplate(notes-copy, "+c.name+")", err, CodeValidationFailed, c.key)
			if ae != nil && !slices.ContainsFunc(ae.Validation.Errors, func(is schemaIssue) bool { return is.Key == c.key }) {
				t.Errorf("UpdateTemplate(notes-copy, %s) errors = %+v, want %s", c.name, ae.Validation.Errors, c.key)
			}
			r := e.app.ValidateTemplate("notes-copy", c.in)
			if r.Valid || !slices.ContainsFunc(r.Errors, func(is schemaIssue) bool { return is.Key == c.key }) {
				t.Errorf("ValidateTemplate(notes-copy, %s) = %+v, want %s", c.name, r, c.key)
			}
			if got := readFile(t, cp.Path); got != before {
				t.Errorf("notes-copy.yaml after the refused edit = %q, want %q", got, before)
			}
		})
	}
	if tp, _ := templateByID(e, "notes-copy"); !tp.Validation.Valid || tp.Name["en-US"] != "Meeting notes (copy)" {
		t.Errorf("Templates() notes-copy after the refused edits = %+v, want the previous version", tp)
	}
	if got := e.published(domain.EventTemplateUpdated); len(got) != 0 {
		t.Errorf("template.updated events after refused edits = %+v, want none", got)
	}
	opened, err := e.app.OpenTemplate("notes-copy")
	if err != nil || opened.Revision != cp.Revision || opened.YAML != before {
		t.Errorf("OpenTemplate(notes-copy) after the refused edits = %+v, %v, want the previous version", opened, err)
	}
}

// An update names the revision it started from: without one it is refused, and from one the file no longer has it is a
// conflict that gives the current revision, never a silent overwrite.
func TestTemplateUpdateStartsFromARevision(t *testing.T) {
	e := newEditingEnv(t)
	cp := duplicateTemplate(t, e, "hf-notes")
	_, err := e.app.UpdateTemplate("notes-copy", TemplateInput{YAML: cp.YAML})
	appError(t, "UpdateTemplate(notes-copy, no revision)", err, CodeBadRequest, "template_base_revision_required")

	outside := strings.Replace(cp.YAML, "Sum up the meeting.", "Changed outside.", 1)
	if err := os.WriteFile(cp.Path, []byte(outside), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = e.app.UpdateTemplate("notes-copy", TemplateInput{YAML: cp.YAML, BaseRevision: cp.Revision})
	if ae := appError(t, "UpdateTemplate(notes-copy, a stale revision)", err, CodeConflict, "template_changed_on_disk"); ae != nil &&
		(ae.CurrentRevision == "" || ae.CurrentRevision == cp.Revision) {
		t.Errorf("UpdateTemplate(notes-copy, a stale revision) current revision %q, want the file's", ae.CurrentRevision)
	}
	if got := readFile(t, cp.Path); got != outside {
		t.Errorf("notes-copy.yaml after the conflict = %q, want the outside edit kept", got)
	}
}

// Validating an edit converts it for the other view: a document comes back as YAML, and YAML as a document.
func TestValidateTemplateConvertsBetweenViews(t *testing.T) {
	e := newEditingEnv(t)
	cp := duplicateTemplate(t, e, "hf-notes")
	fromDoc := e.app.ValidateTemplate("notes-copy", TemplateInput{Document: mustJSON(t, cp.Document)})
	if !fromDoc.Valid || !strings.Contains(fromDoc.YAML, "prompt: Sum up the meeting.") || fromDoc.Document == nil {
		t.Errorf("ValidateTemplate(notes-copy, a document) = %+v, want it valid with its YAML", fromDoc)
	}
	fromYAML := e.app.ValidateTemplate("notes-copy", TemplateInput{YAML: cp.YAML})
	if !fromYAML.Valid || !reflect.DeepEqual(fromYAML.Document, cp.Document) || fromYAML.YAML != cp.YAML {
		t.Errorf("ValidateTemplate(notes-copy, YAML) = %+v, want it valid with its document", fromYAML)
	}
	invalid := e.app.ValidateTemplate("notes-copy", TemplateInput{YAML: strings.Replace(cp.YAML, "type: general", "type: email", 1)})
	if invalid.Valid || invalid.Document == nil {
		t.Errorf("ValidateTemplate(notes-copy, an unknown type) = %+v, want it invalid with its document", invalid)
	}
	if got := templateFiles(t, e); !slices.Equal(got, []string{"notes-copy.yaml"}) {
		t.Errorf("the templates directory after validating = %v, want only notes-copy.yaml", got)
	}
}

// Opening a template gives its document and its file's text: a built-in one with no revision, since it is never
// updated, one of the user's with its file's revision, and one whose file fails validation with its errors and its
// text, so that it can be fixed. An identifier no template has is not found.
func TestOpenTemplate(t *testing.T) {
	e := newEditingEnv(t, withUserTemplates(map[string]string{
		"broken.yaml": "apiVersion: harmflow/v1alpha1\nkind: [unclosed\n",
		"mine.yaml":   template("mine", "  type: general\n"),
	}))
	builtin, err := e.app.OpenTemplate("hf-notes")
	if err != nil || builtin.YAML != notesBuiltin || builtin.Revision != "" || specOf(t, builtin)["prompt"] != "Sum up the meeting." {
		t.Errorf("OpenTemplate(hf-notes) = %+v, %v, want its document and text", builtin, err)
	}
	mine, err := e.app.OpenTemplate("mine")
	if err != nil || mine.Revision == "" || mine.YAML != template("mine", "  type: general\n") || specOf(t, mine)["type"] != "general" {
		t.Errorf("OpenTemplate(mine) = %+v, %v, want its document, text, and revision", mine, err)
	}
	broken, err := e.app.OpenTemplate("broken")
	if err != nil || broken.Validation.Valid || broken.YAML != "apiVersion: harmflow/v1alpha1\nkind: [unclosed\n" || broken.Revision == "" {
		t.Errorf("OpenTemplate(broken) = %+v, %v, want its errors and text", broken, err)
	}
	_, err = e.app.OpenTemplate("none")
	appError(t, "OpenTemplate(none)", err, CodeNotFound, "template_not_found")
	for _, tp := range e.app.Templates(t.Context()) {
		if tp.Document != nil || tp.YAML != "" {
			t.Errorf("Templates() %s carries its document or text, want them only when one template is opened", tp.ID)
		}
	}
}

// A template of the user's whose file fails validation can still be fixed and deleted, but is not duplicated.
func TestInvalidUserTemplateIsFixedOrDeleted(t *testing.T) {
	e := newEditingEnv(t, withUserTemplates(map[string]string{
		"broken.yaml": "apiVersion: harmflow/v1alpha1\nkind: [unclosed\n",
		"typed.yaml":  template("typed", "  type: email\n"),
	}))
	_, err := e.app.DuplicateTemplate("broken")
	appError(t, "DuplicateTemplate(broken)", err, CodeValidationFailed, "template_invalid")

	typed, _ := e.app.OpenTemplate("typed")
	fixed, err := e.app.UpdateTemplate("typed", TemplateInput{YAML: template("typed", "  type: general\n"), BaseRevision: typed.Revision})
	if err != nil || !fixed.Validation.Valid || fixed.Type != "general" {
		t.Errorf("UpdateTemplate(typed, fixed) = %+v, %v, want it valid", fixed, err)
	}
	if tp, _ := templateByID(e, "typed"); !tp.Validation.Valid {
		t.Errorf("Templates() typed after the fix = %+v, want it offered", tp)
	}
	if err := e.app.DeleteTemplate("broken"); err != nil {
		t.Errorf("DeleteTemplate(broken) = %v, want it deleted", err)
	}
	if got := templateFiles(t, e); !slices.Equal(got, []string{"typed.yaml"}) {
		t.Errorf("the templates directory = %v, want only typed.yaml", got)
	}
}

// A copy takes the first identifier no template has: -copy, then -copy-2, and so on, whether the template copied is
// built in or the user's. An identifier no template has is not found.
func TestDuplicateTakesAFreeIdentifier(t *testing.T) {
	e := newEditingEnv(t)
	var ids []string
	for _, id := range []string{"hf-notes", "hf-notes", "notes-copy", "hf-notes"} {
		ids = append(ids, duplicateTemplate(t, e, id).ID)
	}
	if want := []string{"notes-copy", "notes-copy-2", "notes-copy-copy", "notes-copy-3"}; !slices.Equal(ids, want) {
		t.Errorf("DuplicateTemplate identifiers = %v, want %v", ids, want)
	}
	if tp, _ := templateByID(e, "notes-copy-copy"); tp.Name["en-US"] != "Meeting notes (copy) (copy)" {
		t.Errorf("Templates() notes-copy-copy name = %v, want the copy's with (copy)", tp.Name)
	}
	_, err := e.app.DuplicateTemplate("none")
	appError(t, "DuplicateTemplate(none)", err, CodeNotFound, "template_not_found")
}

// An empty template of a task type holds its identifier, name, description, and type, and no other field; a task
// created from it starts with no prompt. Its identifier is refused as a saved task's is: one with the hf- prefix, or
// one a template has; so is a type the task schema does not define. Nothing is written for a refusal.
func TestCreateEmptyTemplate(t *testing.T) {
	e := newEditingEnv(t)
	tp, err := e.app.CreateTemplate(NewTemplateInput{ID: "triage", Name: "Triage", Description: "Sorts the issues.", Type: "github"})
	if err != nil || !tp.Validation.Valid || tp.Type != "github" || tp.Source != TemplateUser || tp.Name["es-ES"] != "Triage" ||
		tp.Revision == "" {
		t.Fatalf("CreateTemplate(triage) = %+v, %v, want a valid github template of the user's", tp, err)
	}
	if spec := specOf(t, tp); !reflect.DeepEqual(spec, map[string]any{"type": "github"}) {
		t.Errorf("CreateTemplate(triage) spec = %v, want only its type", spec)
	}
	if spec := draft(t, e, "triage", "en-US")["spec"].(map[string]any); spec["type"] != "github" || spec["prompt"] != nil {
		t.Errorf("DraftTask(triage) spec = %v, want a github task with no prompt", spec)
	}
	written := templateFiles(t, e)
	for _, c := range []struct {
		in        NewTemplateInput
		code, key string
	}{
		{NewTemplateInput{ID: "hf-triage", Name: "Triage", Description: "Sorts.", Type: "github"}, CodeValidationFailed, "template_id_reserved"},
		{NewTemplateInput{ID: "triage", Name: "Triage", Description: "Sorts.", Type: "github"}, CodeConflict, "template_exists"},
		{NewTemplateInput{ID: "mail", Name: "Mail", Description: "Sorts.", Type: "email"}, CodeValidationFailed, "template_type_unknown"},
	} {
		_, err := e.app.CreateTemplate(c.in)
		appError(t, "CreateTemplate("+c.in.ID+", "+c.in.Type+")", err, c.code, c.key)
	}
	if got := templateFiles(t, e); !slices.Equal(got, written) {
		t.Errorf("the templates directory after the refusals = %v, want %v", got, written)
	}
}

// signInTask is a task worth repeating whose prompt and comments hold secrets, and which cites the skill review-notes,
// which Claude Code's configuration holds on this machine.
const signInTask = `apiVersion: harmflow/v1alpha1
kind: Task
metadata:
  id: sign-in
  name: Sign in
  description: Signs in to staging.
spec:
  enabled: true
  type: general
  # the old one was password=comment-s3cret
  prompt: |
    Sign in with password: hunter2 and keep the session.
    Send Cookie: sessionid=abc123def456; theme=dark
  skills: [review-notes]
  harness:
    preferred:
      provider: claude-code
`

// exportedSignIn saves the sign-in task as the template sign-in-notes, creates a task from it, runs both tasks to
// completion with output that holds a secret, and exports the template.
func exportedSignIn(t *testing.T, e *env) (exported string, executions []string) {
	t.Helper()
	if _, err := e.app.CreateTask(t.Context(), TaskInput{YAML: signInTask}); err != nil {
		t.Fatalf("CreateTask(sign-in) = %v, want the task", err)
	}
	saveAsTemplate(t, e, "sign-in", "sign-in-notes", "Sign in")
	v, err := e.app.CreateTask(t.Context(), TaskInput{Document: mustJSON(t, draft(t, e, "sign-in-notes", "en-US"))})
	if err != nil {
		t.Fatalf("CreateTask(the draft of sign-in-notes) = %v, want the task", err)
	}
	e.claude.setScript(func(ports.RunRequest) string { return "echo 'signed in, session tok-live-7781'; echo 'END ok'" })
	for _, id := range []string{"sign-in", v.ID} {
		res, err := e.app.RunTask(id)
		if err != nil || res.ExecutionID == "" {
			t.Fatalf("RunTask(%s) = %+v, %v, want an execution", id, res, err)
		}
		e.wait(res.ExecutionID, domain.StatusCompleted)
		e.idle(id)
		executions = append(executions, res.ExecutionID)
	}
	data, err := e.app.ExportTemplate("sign-in-notes")
	if err != nil {
		t.Fatalf("ExportTemplate(sign-in-notes) = %v, want its YAML", err)
	}
	return string(data), executions
}

// Exporting a template of the user's whose tasks have run gives its file's definition alone, as a task's export does:
// its secrets masked, and none of the runs' executions, logs, output, or the file's revision. The export is a valid
// template, which imports on another machine. A built-in template, which every HarmFlow has, is not exported, and an
// identifier no template has is not found.
func TestExportTemplateHoldsNoSecretsLogsOrState(t *testing.T) {
	e := newEnv(t, withTemplates(map[string]string{"hf-notes.yaml": notesBuiltin}))
	exported, executions := exportedSignIn(t, e)
	tp, _ := e.app.OpenTemplate("sign-in-notes")
	for _, held := range append([]string{"hunter2", "abc123def456", "theme=dark", "comment-s3cret", "tok-live-7781",
		"signed in", "END ok", tp.Revision, "enabled", "origin", "Signs in to staging", "revision", "status"}, executions...) {
		if strings.Contains(exported, held) {
			t.Errorf("ExportTemplate(sign-in-notes) holds %q:\n%s", held, exported)
		}
	}
	for _, kept := range []string{"kind: TaskTemplate", "id: sign-in-notes", "Sign in with password: [REDACTED] and keep",
		"Send Cookie: [REDACTED]\n", "# the old one was password=[REDACTED]", "skills: [review-notes]"} {
		if !strings.Contains(exported, kept) {
			t.Errorf("ExportTemplate(sign-in-notes) lacks %q:\n%s", kept, exported)
		}
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(exported), &doc); err != nil {
		t.Fatalf("ExportTemplate(sign-in-notes) is no YAML: %v", err)
	}
	if keys := sortedKeys(doc); !slices.Equal(keys, []string{"apiVersion", "kind", "metadata", "spec"}) {
		t.Errorf("ExportTemplate(sign-in-notes) keys = %v, want the template's definition alone", keys)
	}
	other := newEnv(t)
	if got, err := other.app.ImportTemplate(t.Context(), TemplateImport{YAML: exported}); err != nil || !got.Validation.Valid {
		t.Errorf("ImportTemplate(the export) on another machine = %+v, %v, want a valid template", got, err)
	}

	_, err := e.app.ExportTemplate("hf-notes")
	appError(t, "ExportTemplate(hf-notes)", err, CodeConflict, "template_read_only")
	_, err = e.app.ExportTemplate("none")
	appError(t, "ExportTemplate(none)", err, CodeNotFound, "template_not_found")
}

// A template exported from a machine where its skill review-notes exists imports on one where it does not, and where
// a template of the user's has its identifier: the import asks before replacing it, refusing with template_exists and
// writing nothing until the user chooses to; a dry run checks the same and writes nothing; once confirmed, the
// template is saved as the export gives it, announced as updated, and its answer names review-notes as missing. A new
// identifier is saved and announced as created.
func TestImportTemplateNamesWhatThisMachineLacks(t *testing.T) {
	exported, _ := exportedSignIn(t, newEnv(t))
	e := newEnv(t, func(e *env) { e.claude.lacks = map[string]bool{"review-notes": true} },
		withUserTemplates(map[string]string{"sign-in-notes.yaml": template("sign-in-notes", "  type: general\n  prompt: Mine.\n")}))
	path := filepath.Join(e.paths.Templates, "sign-in-notes.yaml")
	before := readFile(t, path)
	missing := []TemplateCited{{Kind: "skill", Name: "review-notes"}}
	written := func() int {
		return len(e.published(domain.EventTemplateCreated)) + len(e.published(domain.EventTemplateUpdated))
	}

	for _, dryRun := range []bool{false, true} {
		_, err := e.app.ImportTemplate(t.Context(), TemplateImport{YAML: exported, DryRun: dryRun})
		if ae := appError(t, "ImportTemplate(sign-in-notes, not replacing)", err, CodeConflict, "template_exists"); ae != nil &&
			ae.Params["template"] != "sign-in-notes" {
			t.Errorf("ImportTemplate(sign-in-notes, not replacing) params = %v, want it named", ae.Params)
		}
	}
	preview, err := e.app.ImportTemplate(t.Context(), TemplateImport{YAML: exported, Overwrite: true, DryRun: true})
	if err != nil || preview.ID != "sign-in-notes" || preview.Type != "general" || preview.YAML != exported ||
		specOf(t, preview)["prompt"] == nil || preview.Needs == nil || !reflect.DeepEqual(preview.Needs.Missing, missing) {
		t.Errorf("ImportTemplate(dry run) = %+v, %v, want the template as it would be saved, review-notes missing", preview, err)
	}
	if got := readFile(t, path); got != before || written() != 0 {
		t.Fatalf("the template file after the refusals and the dry run = %q, %d events, want it unchanged", got, written())
	}

	tp, err := e.app.ImportTemplate(t.Context(), TemplateImport{YAML: exported, Overwrite: true})
	if err != nil || !tp.Validation.Valid || tp.Source != TemplateUser || tp.Revision == "" || tp.Needs == nil ||
		!reflect.DeepEqual(tp.Needs.Missing, missing) {
		t.Fatalf("ImportTemplate(replacing) = %+v, %v, want the template saved, review-notes missing", tp, err)
	}
	if got := readFile(t, path); got != exported {
		t.Errorf("the template file after the import = %q, want the export", got)
	}
	if updated := e.published(domain.EventTemplateUpdated); len(updated) != 1 || updated[0].Payload["template"] != "sign-in-notes" {
		t.Errorf("template.updated events = %+v, want one for sign-in-notes", updated)
	}
	if listed, _ := templateByID(e, "sign-in-notes"); !strings.Contains(fmt.Sprint(listed.spec["prompt"]), "[REDACTED]") {
		t.Errorf("Templates() sign-in-notes prompt = %v, want the imported one", listed.spec["prompt"])
	}

	renamed := strings.Replace(exported, "id: sign-in-notes", "id: staging-sign-in", 1)
	if _, err := e.app.ImportTemplate(t.Context(), TemplateImport{YAML: renamed}); err != nil {
		t.Fatalf("ImportTemplate(staging-sign-in) = %v, want it saved", err)
	}
	if created := e.published(domain.EventTemplateCreated); len(created) != 1 || created[0].Payload["template"] != "staging-sign-in" {
		t.Errorf("template.created events = %+v, want one for staging-sign-in", created)
	}
}

// An import that fails the template schema, or whose identifier carries the hf- prefix, a built-in template's or not,
// is rejected with the error, dry run or not, and nothing is saved: the templates directory and the built-in template
// stay as they were, and no template is announced. An identifier that is no identifier never names a file.
func TestImportTemplateRejectsAnInvalidOrReservedOne(t *testing.T) {
	e := newEditingEnv(t)
	builtinFile := filepath.Join(e.paths.Builtins, "templates", "hf-notes.yaml")
	builtin := readFile(t, builtinFile)
	cases := []struct {
		name, yaml, key string
	}{
		{"a built-in template's identifier", notesBuiltin, "template_id_reserved"},
		{"an identifier with the hf- prefix", template("hf-mine", "  type: general\n"), "template_id_reserved"},
		{"a type the task schema does not define", template("mail", "  type: email\n"), "template_type_unknown"},
		{"an identifier that is no identifier", template("../outside", "  type: general\n"), "schema_pattern"},
		{"a task's file", taskYAML("mine", ""), ""},
		{"no YAML", "kind: [unclosed\n", ""},
		{"nothing", "", ""},
	}
	for _, c := range cases {
		for _, dryRun := range []bool{false, true} {
			_, err := e.app.ImportTemplate(t.Context(), TemplateImport{YAML: c.yaml, Overwrite: true, DryRun: dryRun})
			call := fmt.Sprintf("ImportTemplate(%s, dry run %v)", c.name, dryRun)
			if c.key != "" {
				appError(t, call, err, CodeValidationFailed, c.key)
			} else if code, _ := appErrorKey(err); code != CodeValidationFailed {
				t.Errorf("%s = %v, want %s", call, err, CodeValidationFailed)
			}
		}
	}
	if got := templateFiles(t, e); len(got) != 0 {
		t.Errorf("the templates directory after the refusals = %v, want it empty", got)
	}
	if got := readFile(t, builtinFile); got != builtin {
		t.Errorf("the built-in template after the refusals = %q, want it unchanged", got)
	}
	if _, err := os.Stat(filepath.Join(e.paths.Home, "outside.yaml")); err == nil {
		t.Error("ImportTemplate(../outside) wrote outside the templates directory")
	}
	if n := len(e.published(domain.EventTemplateCreated)) + len(e.published(domain.EventTemplateUpdated)); n != 0 {
		t.Errorf("template events after the refusals = %d, want none", n)
	}
}

// A dry run of a script template's import gives what the template would run, for the import to show before anything
// is saved: its interpreter's argument list, its whole inline script, its working directory, its environment
// variables, and its permission profile. It writes nothing, and a script template needs no harness.
func TestImportScriptTemplateDryRunGivesItsProgram(t *testing.T) {
	e := newEnv(t)
	const source = "set -e\necho \"$NOTES\" >> notes.txt\n"
	file := template("nightly-notes", "  type: script\n  script:\n    inline:\n      interpreter: [/bin/sh, -e]\n"+
		"      source: |\n        set -e\n        echo \"$NOTES\" >> notes.txt\n    workingDirectory: ~/notes\n"+
		"    environment:\n      NOTES: nightly\n  permissions:\n    profile: read_only\n")
	tp, err := e.app.ImportTemplate(t.Context(), TemplateImport{YAML: file, DryRun: true})
	if err != nil {
		t.Fatalf("ImportTemplate(nightly-notes, dry run) = %v, want its preview", err)
	}
	spec := specOf(t, tp)
	script, _ := spec["script"].(map[string]any)
	inline, _ := script["inline"].(map[string]any)
	if tp.Type != "script" || !reflect.DeepEqual(inline["interpreter"], []any{"/bin/sh", "-e"}) || inline["source"] != source ||
		script["workingDirectory"] != "~/notes" || !reflect.DeepEqual(script["environment"], map[string]any{"NOTES": "nightly"}) ||
		!reflect.DeepEqual(spec["permissions"], map[string]any{"profile": "read_only"}) || tp.Needs != nil {
		t.Errorf("ImportTemplate(nightly-notes, dry run) = %+v, spec %v, want its program and profile", tp, spec)
	}
	if got := templateFiles(t, e); len(got) != 0 {
		t.Errorf("the templates directory after the dry run = %v, want it empty", got)
	}
}
