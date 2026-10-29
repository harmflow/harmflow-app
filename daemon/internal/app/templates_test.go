package app

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

// template is a template file's YAML: its identifier, its names, and spec, the lines under its spec.
func template(id, spec string) string {
	return "apiVersion: harmflow/v1alpha1\nkind: TaskTemplate\nmetadata:\n  id: " + id + "\n" +
		"  name:\n    pt-BR: Notas da reunião\n    en-US: Meeting notes\n    es-ES: Notas de la reunión\n" +
		"  description:\n    pt-BR: Resume a reunião.\n    en-US: Sums up the meeting.\n    es-ES: Resume la reunión.\n" +
		"spec:\n" + spec
}

// withTemplates writes template files into the env's built-ins before the App starts, by file name.
func withTemplates(files map[string]string) func(*env) {
	return func(e *env) {
		dir := filepath.Join(e.paths.Builtins, "templates")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			e.t.Fatal(err)
		}
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
				e.t.Fatal(err)
			}
		}
	}
}

// withBuiltinAgent writes a built-in agent into the env's built-ins.
func withBuiltinAgent(name, content string) func(*env) {
	return func(e *env) {
		if err := os.WriteFile(filepath.Join(e.paths.Builtins, "agents", name+".md"), []byte(content), 0o600); err != nil {
			e.t.Fatal(err)
		}
	}
}

// withShippedTemplates copies this checkout's built-in templates, and the built-in agent the GitHub one selects, into
// the env's built-ins, as a build of HarmFlow ships them.
func withShippedTemplates(e *env) {
	root := filepath.Join("..", "..", "..")
	files := map[string]string{}
	entries, err := os.ReadDir(filepath.Join(root, "templates"))
	if err != nil {
		e.t.Fatal(err)
	}
	for _, f := range entries {
		data, err := os.ReadFile(filepath.Join(root, "templates", f.Name()))
		if err != nil {
			e.t.Fatal(err)
		}
		files[f.Name()] = string(data)
	}
	withTemplates(files)(e)
	agent, err := os.ReadFile(filepath.Join(root, "agents", "hf-github-issue-implementer.md"))
	if err != nil {
		e.t.Fatal(err)
	}
	withBuiltinAgent("hf-github-issue-implementer", string(agent))(e)
}

// appError is err as an *Error with that code and key, or a failure naming what was called.
func appError(t *testing.T, call string, err error, code, key string) *Error {
	t.Helper()
	var ae *Error
	if !errors.As(err, &ae) || ae.Code != code || ae.Key != key {
		t.Errorf("%s = %v, want %s %s", call, err, code, key)
		return nil
	}
	return ae
}

func draft(t *testing.T, e *env, id, language string) map[string]any {
	t.Helper()
	nt, err := e.app.DraftTask(t.Context(), id, language)
	if err != nil {
		t.Fatalf("DraftTask(%s, %s) = %v, want a document", id, language, err)
	}
	return nt.Document
}

// The built-in templates load at start, sorted by identifier, each valid one with its type, its name and description
// in each language, and the agent it selects.
func TestTemplatesLoad(t *testing.T) {
	e := newEnv(t, withTemplates(map[string]string{
		"hf-notes.yaml":  template("hf-notes", "  type: general\n  prompt: Sum up the meeting.\n"),
		"hf-agent.yaml":  template("hf-agent", "  type: general\n  selectedAgent: hf-reviewer\n"),
		"hf-readme.md":   "not a template",
		"hf-script.yaml": template("hf-script", "  type: script\n  script: {}\n"),
	}))
	got := e.app.Templates()
	var ids []string
	for _, tp := range got {
		ids = append(ids, tp.ID)
		if !tp.Validation.Valid || tp.Source != TemplateBuiltin || !strings.HasSuffix(tp.Path, filepath.Join("templates", tp.ID+".yaml")) {
			t.Errorf("Templates() %s = %+v, want a valid built-in template at its file", tp.ID, tp)
		}
		if tp.Name["pt-BR"] != "Notas da reunião" || tp.Name["en-US"] != "Meeting notes" || tp.Name["es-ES"] != "Notas de la reunión" ||
			tp.Description["en-US"] != "Sums up the meeting." || len(tp.Description) != 3 {
			t.Errorf("Templates() %s: name %v, description %v, want each in three languages", tp.ID, tp.Name, tp.Description)
		}
	}
	if want := []string{"hf-agent", "hf-notes", "hf-script"}; !slices.Equal(ids, want) {
		t.Fatalf("Templates() = %v, want %v", ids, want)
	}
	if got[0].SelectedAgent != "hf-reviewer" || got[1].SelectedAgent != "" || got[0].Type != "general" || got[2].Type != "script" {
		t.Errorf("Templates() selected agents %q, %q and types %q, %q", got[0].SelectedAgent, got[1].SelectedAgent,
			got[0].Type, got[2].Type)
	}
}

// An invalid template file is listed with its errors, located by line and column, and never offered; the other
// templates stay loaded. A type the task schema does not define is named; so are a prompt beside a selected agent, an
// identifier that is not the file's name, and one without the hf- prefix.
func TestInvalidTemplateIsListed(t *testing.T) {
	e := newEnv(t, withTemplates(map[string]string{
		"hf-broken.yaml": "apiVersion: harmflow/v1alpha1\nkind: [unclosed\n",
		"hf-typed.yaml":  template("hf-typed", "  type: email\n"),
		"hf-both.yaml":   template("hf-both", "  type: general\n  prompt: Sum up.\n  selectedAgent: hf-reviewer\n"),
		"hf-moved.yaml":  template("hf-other", "  type: general\n"),
		"mine.yaml":      template("mine", "  type: general\n"),
		"hf-valid.yaml":  template("hf-valid", "  type: general\n"),
	}))
	byID := map[string]Template{}
	for _, tp := range e.app.Templates() {
		byID[tp.ID] = tp
	}
	cases := []struct {
		id, key, path string
		line          int
	}{
		{"hf-broken", "yaml_syntax", "", 1},
		{"hf-typed", "template_type_unknown", "/spec/type", 14},
		{"hf-both", "schema_not", "/spec", 13},
		{"hf-moved", "id_file_mismatch", "/metadata/id", 4},
		{"mine", "template_id_not_builtin", "/metadata/id", 4},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			tp, ok := byID[c.id]
			if !ok || tp.Validation.Valid {
				t.Fatalf("Templates() %s = %+v, want it listed as invalid", c.id, tp)
			}
			i := slices.IndexFunc(tp.Validation.Errors, func(is schemaIssue) bool { return is.Key == c.key })
			// The YAML parser gives the line of a syntax error, and no column.
			if i < 0 || tp.Validation.Errors[i].Path != c.path || tp.Validation.Errors[i].Line != c.line ||
				(c.key != "yaml_syntax" && tp.Validation.Errors[i].Column == 0) {
				t.Fatalf("Templates() %s errors = %+v, want %s at %q, line %d, with a column", c.id, tp.Validation.Errors,
					c.key, c.path, c.line)
			}
			if tp.Name != nil || tp.Type != "" {
				t.Errorf("Templates() %s offers name %v and type %q, want neither", c.id, tp.Name, tp.Type)
			}
			_, err := e.app.DraftTask(t.Context(), c.id, "en-US")
			if ae := appError(t, "DraftTask("+c.id+")", err, CodeValidationFailed, "template_invalid"); ae != nil &&
				(ae.Validation != tp.Validation || ae.Params["file"] != tp.Path) {
				t.Errorf("DraftTask(%s) = %+v, want the template's validation and file", c.id, ae)
			}
		})
	}
	typed := byID["hf-typed"].Validation.Errors
	if i := slices.IndexFunc(typed, func(is schemaIssue) bool { return is.Key == "template_type_unknown" }); i < 0 ||
		typed[i].Params["type"] != "email" || !strings.Contains(typed[i].Message, `"email"`) {
		t.Errorf("hf-typed errors = %+v, want one naming the type email", typed)
	}
	if tp := byID["hf-valid"]; !tp.Validation.Valid || tp.Type != "general" {
		t.Errorf("Templates() hf-valid = %+v, want it valid beside the invalid ones", tp)
	}
}

// Built-in templates are read-only: changing or deleting one, or writing a template under an identifier with the hf-
// prefix that none has yet, is refused, and any other identifier names no template.
func TestBuiltinTemplatesAreReadOnly(t *testing.T) {
	e := newEnv(t, withTemplates(map[string]string{
		"hf-notes.yaml": template("hf-notes", "  type: general\n"),
		"notes.yaml":    template("notes", "  type: general\n"),
	}))
	before := e.app.Templates()
	for _, id := range []string{"hf-notes", "hf-new", "notes"} {
		appError(t, "UpdateTemplate("+id+")", e.app.UpdateTemplate(id), CodeConflict, "template_read_only")
		appError(t, "DeleteTemplate("+id+")", e.app.DeleteTemplate(id), CodeConflict, "template_read_only")
	}
	appError(t, "UpdateTemplate(mine)", e.app.UpdateTemplate("mine"), CodeNotFound, "template_not_found")
	appError(t, "DeleteTemplate(mine)", e.app.DeleteTemplate("mine"), CodeNotFound, "template_not_found")
	data, err := os.ReadFile(filepath.Join(e.paths.Builtins, "templates", "hf-notes.yaml"))
	if err != nil || string(data) != template("hf-notes", "  type: general\n") {
		t.Errorf("hf-notes.yaml after the refusals = %q, %v, want it unchanged", data, err)
	}
	if after := e.app.Templates(); len(after) != len(before) {
		t.Errorf("Templates() after the refusals = %v, want %v", after, before)
	}
}

const reviewer = "---\nname: hf-reviewer\ndescription: Reviews a change\nmodel: sonnet\nskills: [hf-github-draft-pr, hf-x, hf-x]\n---\nYou review."

// A template that selects an agent starts the task as that agent's copy, as selecting it in the editor makes it: its
// instructions, description, model, skills, and origin. The fields the template sets itself keep its values: its
// skills replace the agent's, and its model the agent's, while its provider merges with the agent's model.
func TestDraftTaskCopiesTheSelectedAgent(t *testing.T) {
	e := newEnv(t, withBuiltinAgent("hf-reviewer", reviewer), withTemplates(map[string]string{
		"hf-plain.yaml": template("hf-plain", "  type: general\n  selectedAgent: hf-reviewer\n"+
			"  harness:\n    preferred:\n      provider: claude-code\n"),
		"hf-custom.yaml": template("hf-custom", "  type: general\n  selectedAgent: hf-reviewer\n  skills: [hf-own]\n"+
			"  harness:\n    preferred:\n      provider: codex\n      model: gpt-5\n"),
	}))
	var hash string
	for _, a := range e.app.Catalog(t.Context(), "", "").Agents {
		if a.Name == "hf-reviewer" && a.Copy != nil {
			hash = a.Copy.Hash
		}
	}
	plain := draft(t, e, "hf-plain", "en-US")
	want := map[string]any{"apiVersion": "harmflow/v1alpha1", "kind": "Task",
		"metadata": map[string]any{"id": "meeting-notes", "name": "Meeting notes", "description": "Reviews a change"},
		"spec": map[string]any{"enabled": true, "type": "general", "prompt": "You review.",
			"skills":  []any{"hf-github-draft-pr", "hf-x"},
			"harness": map[string]any{"preferred": map[string]any{"provider": "claude-code", "model": "sonnet"}},
			"origin":  map[string]any{"agent": "hf-reviewer", "hash": hash}}}
	if hash == "" || !reflect.DeepEqual(plain, want) {
		t.Errorf("DraftTask(hf-plain) = %v, want %v", plain, want)
	}
	spec := draft(t, e, "hf-custom", "en-US")["spec"].(map[string]any)
	if !reflect.DeepEqual(spec["skills"], []any{"hf-own"}) ||
		!reflect.DeepEqual(spec["harness"], map[string]any{"preferred": map[string]any{"provider": "codex", "model": "gpt-5"}}) ||
		spec["prompt"] != "You review." || spec["origin"] == nil {
		t.Errorf("DraftTask(hf-custom) spec = %v, want the copy with the template's skills and model", spec)
	}
}

// When this machine lacks the agent a template selects, the new task holds no prompt and no origin, for the user to
// write one; the rest of the template still applies.
func TestDraftTaskWithAMissingAgent(t *testing.T) {
	e := newEnv(t, withTemplates(map[string]string{
		"hf-lost.yaml": template("hf-lost", "  type: general\n  selectedAgent: hf-absent\n  skills: [hf-own]\n"),
	}))
	got := draft(t, e, "hf-lost", "pt-BR")
	want := map[string]any{"apiVersion": "harmflow/v1alpha1", "kind": "Task",
		"metadata": map[string]any{"id": "notas-da-reuniao", "name": "Notas da reunião"},
		"spec":     map[string]any{"enabled": true, "type": "general", "skills": []any{"hf-own"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DraftTask(hf-lost) = %v, want %v", got, want)
	}
}

// The new task is named after the template in the language asked for, and gets an identifier no task has: the one its
// name gives, or that one with the first free number. Nothing is written, and two drafts share nothing.
func TestDraftTaskIdentifierCollides(t *testing.T) {
	e := newEnv(t, withTemplates(map[string]string{
		"hf-notes.yaml": template("hf-notes", "  type: general\n  prompt: Sum up.\n  skills: [hf-own]\n"),
	}))
	e.create("notas-da-reuniao", "")
	e.create("notas-da-reuniao-2", "")
	before, _ := os.ReadDir(e.paths.Tasks)
	first := draft(t, e, "hf-notes", "pt-BR")
	if md := first["metadata"].(map[string]any); md["id"] != "notas-da-reuniao-3" || md["name"] != "Notas da reunião" {
		t.Errorf("DraftTask(hf-notes, pt-BR) metadata = %v, want notas-da-reuniao-3 named Notas da reunião", md)
	}
	if md := draft(t, e, "hf-notes", "es-ES")["metadata"].(map[string]any); md["id"] != "notas-de-la-reunion" {
		t.Errorf("DraftTask(hf-notes, es-ES) metadata = %v, want notas-de-la-reunion", md)
	}
	if after, _ := os.ReadDir(e.paths.Tasks); len(after) != len(before) {
		t.Errorf("the tasks directory holds %d files after the drafts, want %d", len(after), len(before))
	}
	first["spec"].(map[string]any)["skills"].([]any)[0] = "changed"
	if skills := draft(t, e, "hf-notes", "en-US")["spec"].(map[string]any)["skills"]; !reflect.DeepEqual(skills, []any{"hf-own"}) {
		t.Errorf("a draft after a change to the first has skills %v, want [hf-own]", skills)
	}
	_, err := e.app.DraftTask(t.Context(), "hf-notes", "fr-FR")
	appError(t, "DraftTask(hf-notes, fr-FR)", err, CodeBadRequest, "template_language_invalid")
	_, err = e.app.DraftTask(t.Context(), "hf-none", "en-US")
	appError(t, "DraftTask(hf-none)", err, CodeNotFound, "template_not_found")
}

// An identifier made of a name is the one the task editor makes of it, cut to the schema's 64 characters, with room
// for a number when that one is taken.
func TestSlugify(t *testing.T) {
	long := strings.Repeat("a", 63) + " b"
	for _, c := range []struct{ name, want string }{
		{"Implement GitHub issues", "implement-github-issues"},
		{"  Notas da Reunião — 2ª  ", "notas-da-reuniao-2a"},
		{"Ñandú, ¿qué tal?", "nandu-que-tal"},
		{long, strings.Repeat("a", 63)},
		{"日本", ""},
	} {
		if got := slugify(c.name); got != c.want {
			t.Errorf("slugify(%q) = %q, want %q", c.name, got, c.want)
		}
	}
	e := newEnv(t)
	base := strings.Repeat("a", 64)
	e.create(base, "")
	if got := e.app.freeTaskID(base); got != strings.Repeat("a", 62)+"-2" {
		t.Errorf("freeTaskID(%q) = %q, want 62 letters and -2", base, got)
	}
	if got := e.app.freeTaskID(""); got != "task" {
		t.Errorf(`freeTaskID("") = %q, want task`, got)
	}
}

// A new task from the built-in GitHub template selects the built-in implementer, whose copy becomes its prompt, with
// the implementer as its origin; the implementer is no subagent of the task, and no other built-in template selects an
// agent.
func TestGitHubTypeSelectsTheImplementer(t *testing.T) {
	e := newEnv(t, withShippedTemplates)
	for _, tp := range e.app.Templates() {
		want := ""
		if tp.Type == domain.TypeGitHub {
			want = "hf-github-issue-implementer"
		}
		if tp.SelectedAgent != want {
			t.Errorf("Templates() %s of type %s selects %q, want %q", tp.ID, tp.Type, tp.SelectedAgent, want)
		}
	}
	var copied *AgentCopy
	for _, a := range e.app.Catalog(t.Context(), "", "").Agents {
		if a.Name == "hf-github-issue-implementer" {
			copied = a.Copy
		}
	}
	if copied == nil {
		t.Fatal("Catalog() has no copy of hf-github-issue-implementer")
	}
	spec := draft(t, e, "hf-github-issues", "en-US")["spec"].(map[string]any)
	if spec["prompt"] != copied.Prompt || !reflect.DeepEqual(spec["origin"],
		map[string]any{"agent": "hf-github-issue-implementer", "hash": copied.Hash}) || spec["agents"] != nil {
		t.Errorf("DraftTask(hf-github-issues) spec = %v, want the implementer's copy, as its origin, and no subagent", spec)
	}
}

// Each built-in template builds the document the task picker built for its kind before templates were data, changing
// nothing: the same type, prompt, agents, skills, harness, and type parameters. An empty placeholder the picker wrote
// for a value to fill — an empty prompt, program, repository name, or local path — is a value left unset now, which
// the editor asks for as it asked for the placeholder.
func TestBuiltinTemplatesBuildThePickersDocuments(t *testing.T) {
	e := newEnv(t, withShippedTemplates)
	var implementer *AgentCopy
	for _, a := range e.app.Catalog(t.Context(), "", "").Agents {
		if a.Name == "hf-github-issue-implementer" {
			implementer = a.Copy
		}
	}
	if implementer == nil {
		t.Fatal("Catalog() has no copy of hf-github-issue-implementer")
	}
	claude := map[string]any{"preferred": map[string]any{"provider": "claude-code"}}
	cases := []struct {
		template, name string
		spec           map[string]any
	}{
		{"hf-general", "General task", map[string]any{"enabled": true, "type": "general", "harness": claude}},
		{"hf-github-issues", "Implement GitHub issues", map[string]any{"enabled": true, "type": "github",
			"prompt": implementer.Prompt, "origin": map[string]any{"agent": "hf-github-issue-implementer", "hash": implementer.Hash},
			"skills":  []any{"hf-github-issue-queue", "hf-github-worktree", "hf-github-draft-pr"},
			"harness": claude, "github": map[string]any{"repositories": []any{map[string]any{"baseBranch": "main"}}}}},
		{"hf-script", "Script task", map[string]any{"enabled": true, "type": "script", "script": map[string]any{}}},
	}
	var ids []string
	for _, tp := range e.app.Templates() {
		ids = append(ids, tp.ID)
	}
	if want := []string{"hf-general", "hf-github-issues", "hf-script"}; !slices.Equal(ids, want) {
		t.Fatalf("Templates() = %v, want %v", ids, want)
	}
	for _, c := range cases {
		t.Run(c.template, func(t *testing.T) {
			doc := draft(t, e, c.template, "en-US")
			md := doc["metadata"].(map[string]any)
			if md["name"] != c.name || md["id"] != slugify(c.name) {
				t.Errorf("DraftTask(%s) metadata = %v, want %s", c.template, md, c.name)
			}
			if !reflect.DeepEqual(doc["spec"], c.spec) {
				t.Errorf("DraftTask(%s) spec = %v, want %v", c.template, doc["spec"], c.spec)
			}
		})
	}
}
