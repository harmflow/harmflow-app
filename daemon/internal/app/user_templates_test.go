package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
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
  skills: [hf-github-draft-pr]
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
		"# The prompt, customized from the agent's.", "cron: \"0 9 * * 1\"", "skills: [hf-github-draft-pr]"} {
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
		!reflect.DeepEqual(spec["skills"], []any{"hf-github-draft-pr"}) ||
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
