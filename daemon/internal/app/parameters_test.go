package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/schema"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// The built-ins below declare parameters as a user's skill or agent would: two skills that declare the same name, one
// of them with a required text and a number with a default, a skill with an object whose schema it gives, and an agent
// with a boolean.
const (
	releaseNotesSkill = "---\nname: hf-release-notes\ndescription: Writes release notes\nparameters:\n" +
		"  - {name: owner, type: text, description: Who owns the repository, required: true}\n" +
		"  - {name: limit, type: number, description: How many changes, default: 3}\n---\n\nWrite the notes.\n"
	digestSkill = "---\nname: hf-digest\ndescription: Writes a digest\nparameters:\n" +
		"  - {name: owner, type: text, description: Who reads it}\n---\n\nWrite the digest.\n"
	queuesSkill = "---\nname: hf-queues\ndescription: Works the queues\nparameters:\n" +
		"  - name: repositories\n    type: object\n    description: The repositories and their flags\n    required: true\n" +
		"    schema:\n      type: object\n      additionalProperties: {type: object, required: [public], properties: {public: {type: boolean}}}\n" +
		"---\n\nWork the queues.\n"
	reviewerAgent = "---\nname: hf-reviewer\ndescription: Reviews\nparameters:\n" +
		"  - {name: strict, type: boolean, description: Refuse on any finding}\n---\n\nReview.\n"
)

// declaringBuiltins writes the built-ins of the tests below beside the env's own.
func (e *env) declaringBuiltins() {
	e.t.Helper()
	e.writeBuiltin("skills/hf-release-notes/SKILL.md", releaseNotesSkill)
	e.writeBuiltin("skills/hf-digest/SKILL.md", digestSkill)
	e.writeBuiltin("skills/hf-queues/SKILL.md", queuesSkill)
	e.writeBuiltin("agents/hf-reviewer.md", reviewerAgent)
}

func (e *env) writeBuiltin(rel, content string) {
	e.t.Helper()
	path := filepath.Join(e.paths.Builtins, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

const citesDeclaring = "  agents: [hf-reviewer]\n  skills: [hf-release-notes, hf-digest]\n"

// A task keeps the values of its skills' and agents' parameters in one map per skill or agent: two skills that declare
// the same name keep a value each, and the agent's apart from them.
func TestDeclaredParametersAreSavedPerItem(t *testing.T) {
	e := newEnv(t)
	e.declaringBuiltins()
	doc := taskYAML("notes", citesDeclaring+"  parameters:\n    skills:\n      hf-release-notes: {owner: ana}\n"+
		"      hf-digest: {owner: bia}\n    agents:\n      hf-reviewer: {strict: true}\n")
	v, err := e.app.CreateTask(t.Context(), TaskInput{YAML: doc})
	if err != nil {
		t.Fatalf("CreateTask(notes) = %v, want it saved", err)
	}
	if len(v.Validation.Warnings) > 0 {
		t.Errorf("CreateTask(notes) warnings = %+v, want none", v.Validation.Warnings)
	}
	data, err := os.ReadFile(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Spec struct {
			Parameters domain.DeclaredParameters `yaml:"parameters"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	p := file.Spec.Parameters
	if p.Skills["hf-release-notes"]["owner"] != "ana" || p.Skills["hf-digest"]["owner"] != "bia" || p.Agents["hf-reviewer"]["strict"] != true {
		t.Errorf("the saved file's spec.parameters = %+v, want each owner under its skill and strict under the agent", p)
	}
	if _, ok := p.Skills["hf-release-notes"]["limit"]; ok {
		t.Errorf("the saved file's spec.parameters = %+v, want no default written", p)
	}

	// A task file with no spec.parameters loads and runs as before.
	e.create("plain", "")
	if r, err := e.app.RunTask("plain"); err != nil {
		t.Fatalf("RunTask(plain) = %v", err)
	} else {
		e.wait(r.ExecutionID, domain.StatusCompleted)
	}
}

// A missing required value, a value of the wrong type, and an object its schema rejects are refused, from the YAML
// editor, from the form, and in a file written outside the dashboard, each naming the parameter and its skill; the
// previous valid version is kept, and the file does not run.
func TestDeclaredParametersAreValidated(t *testing.T) {
	e := newEnv(t)
	e.declaringBuiltins()
	cites := "  skills: [hf-release-notes, hf-queues]\n"
	queues := "      hf-queues:\n        repositories: {octo/repo: {public: true}}\n"
	for _, c := range []struct {
		name, parameters, key, path string
	}{
		{"no owner", "  parameters:\n    skills:\n" + queues, "parameter_required", "/spec/parameters/skills/hf-release-notes/owner"},
		{"a limit in words", "  parameters:\n    skills:\n      hf-release-notes: {owner: ana, limit: three}\n" + queues,
			"parameter_wrong_type", "/spec/parameters/skills/hf-release-notes/limit"},
		{"repositories its schema rejects", "  parameters:\n    skills:\n      hf-release-notes: {owner: ana}\n" +
			"      hf-queues:\n        repositories: {octo/repo: {public: maybe}}\n",
			"parameter_schema_mismatch", "/spec/parameters/skills/hf-queues/repositories"},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc := taskYAML("checked", cites+c.parameters)
			refused := func(how string, err error) {
				t.Helper()
				var ae *Error
				if !errors.As(err, &ae) || ae.Validation == nil || !slices.ContainsFunc(ae.Validation.Errors, func(is schemaIssue) bool {
					return is.Key == c.key && is.Path == c.path && strings.Contains(is.Message, `"`+lastSegment(c.path)+`"`) &&
						strings.Contains(is.Message, "hf-")
				}) {
					t.Errorf("%s = %v, want %s at %s, naming the parameter and its skill", how, err, c.key, c.path)
				}
			}
			_, err := e.app.CreateTask(t.Context(), TaskInput{YAML: doc})
			refused("CreateTask(yaml)", err)

			var plain any
			if err := yaml.Unmarshal([]byte(doc), &plain); err != nil {
				t.Fatal(err)
			}
			form, err := json.Marshal(plain)
			if err != nil {
				t.Fatal(err)
			}
			_, err = e.app.CreateTask(t.Context(), TaskInput{Document: form})
			refused("CreateTask(document)", err)
		})
	}

	// A file written with a text editor over a valid task loads as invalid, and its last valid version is kept.
	good := taskYAML("edited", cites+"  parameters:\n    skills:\n      hf-release-notes: {owner: ana}\n"+queues)
	v, err := e.app.CreateTask(t.Context(), TaskInput{YAML: good})
	if err != nil {
		t.Fatalf("CreateTask(edited) = %v", err)
	}
	bad := strings.Replace(good, "{owner: ana}", "{owner: ana, limit: three}", 1)
	if err := os.WriteFile(v.Path, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	e.app.scanTaskFiles(t.Context())
	edited, _ := e.app.GetTask("edited")
	if !edited.Invalid {
		t.Errorf("GetTask(edited) after an outside edit with a limit in words = %+v, want it invalid", edited)
	}
	if _, err := e.app.RunTask("edited"); err == nil {
		t.Error("RunTask(edited) = no error, want an invalid task refused")
	}
}

// A value whose declaration is gone does not stop the task: validation and the execution warn, naming the parameter and
// its skill, the session's context.json leaves it out, and the next save drops it from the file, the rest kept as
// written.
func TestUndeclaredParameterWarnsAndIsDropped(t *testing.T) {
	e := newEnv(t)
	e.declaringBuiltins()
	doc := taskYAML("notes", "  skills: [hf-release-notes]\n  parameters:\n    skills:\n      hf-release-notes:\n"+
		"        owner: ana # the repository's owner\n        limit: 5\n")
	if _, err := e.app.CreateTask(t.Context(), TaskInput{YAML: doc}); err != nil {
		t.Fatalf("CreateTask(notes) = %v", err)
	}
	e.writeBuiltin("skills/hf-release-notes/SKILL.md", "---\nname: hf-release-notes\nparameters:\n"+
		"  - {name: owner, type: text, required: true}\n---\n\nWrite the notes.\n")

	undeclared := func(is schemaIssue) bool {
		return is.Key == "parameter_undeclared" && is.Path == "/spec/parameters/skills/hf-release-notes/limit" &&
			strings.Contains(is.Message, `"limit"`) && strings.Contains(is.Message, `"hf-release-notes"`)
	}
	r := e.app.Validate(t.Context(), TaskInput{YAML: doc})
	if !r.Valid || !slices.ContainsFunc(r.Warnings, undeclared) {
		t.Errorf("Validate(notes) = valid %v, warnings %+v; want valid, warning of limit", r.Valid, r.Warnings)
	}

	var q requests
	e.claude.setScript(func(req ports.RunRequest) string { q.add(req); return "echo 'END ok'" })
	run, err := e.app.RunTask("notes")
	if err != nil {
		t.Fatalf("RunTask(notes) = %v", err)
	}
	x := e.wait(run.ExecutionID, domain.StatusCompleted)
	if !slices.ContainsFunc(x.Warnings, func(w domain.Warning) bool {
		return w.Text.Key == "parameter_undeclared" && w.Text.Params["name"] == "limit" && w.Text.Params["item"] == "hf-release-notes"
	}) {
		t.Errorf("the execution's warnings = %+v, want one of limit and hf-release-notes", x.Warnings)
	}
	ctx := e.readContextOf(q.all())
	if got := valueAt(ctx, "task", "declaredParameters", "skills", "hf-release-notes"); !jsonEqual(got, map[string]any{"owner": "ana"}) {
		t.Errorf("context.json's values of hf-release-notes = %v, want owner alone", got)
	}

	view, _ := e.app.GetTask("notes")
	saved, err := e.app.UpdateTask(t.Context(), "notes", TaskInput{YAML: doc, BaseRevision: view.Revision})
	if err != nil {
		t.Fatalf("UpdateTask(notes) = %v", err)
	}
	data, _ := os.ReadFile(saved.Path)
	if strings.Contains(string(data), "limit") || !strings.Contains(string(data), "owner: ana # the repository's owner") {
		t.Errorf("the saved file =\n%s\nwant limit dropped and the owner kept as written", data)
	}
	if slices.ContainsFunc(saved.Validation.Warnings, undeclared) {
		t.Errorf("UpdateTask(notes) warnings = %+v, want none of a value no longer in the file", saved.Validation.Warnings)
	}

	// A value of a skill the task no longer cites is dropped too, and the block with it once it is empty.
	view, _ = e.app.GetTask("notes")
	uncited := taskYAML("notes", "  parameters:\n    skills:\n      hf-release-notes: {owner: ana}\n")
	saved, err = e.app.UpdateTask(t.Context(), "notes", TaskInput{YAML: uncited, BaseRevision: view.Revision})
	if err != nil {
		t.Fatalf("UpdateTask(notes, no skill) = %v", err)
	}
	if data, _ := os.ReadFile(saved.Path); strings.Contains(string(data), "parameters") {
		t.Errorf("the saved file =\n%s\nwant no parameters block", data)
	}
}

// Every execution gives the session the declared values in context.json, in the task's shape, a parameter's default
// for one the task leaves unset, and points the session at the file even when they are its only context. A GitHub
// task's type block stays task.parameters beside them.
func TestContextCarriesDeclaredParameters(t *testing.T) {
	e := newEnv(t)
	e.declaringBuiltins()
	var q requests
	e.claude.setScript(func(req ports.RunRequest) string { q.add(req); return "echo 'END ok'" })
	e.create("general", "  agents: [hf-reviewer]\n  skills: [hf-release-notes]\n  parameters:\n"+
		"    skills:\n      hf-release-notes: {owner: ana}\n    agents:\n      hf-reviewer: {strict: true}\n")
	r, err := e.app.RunTask("general")
	if err != nil {
		t.Fatal(err)
	}
	e.wait(r.ExecutionID, domain.StatusCompleted)
	sent := q.all()
	ctx := e.readContextOf(sent)
	want := map[string]any{"skills": map[string]any{"hf-release-notes": map[string]any{"owner": "ana", "limit": 3}},
		"agents": map[string]any{"hf-reviewer": map[string]any{"strict": true}}}
	if got := valueAt(ctx, "task", "declaredParameters"); !jsonEqual(got, want) {
		t.Errorf("context.json task.declaredParameters = %v, want %v", got, want)
	}
	if valueAt(ctx, "task", "parameters") != nil {
		t.Errorf("context.json task.parameters = %v, want null for a general task", valueAt(ctx, "task", "parameters"))
	}
	if !strings.Contains(sent[0].Context, "- context.json:") {
		t.Errorf("the session's context does not point at context.json, whose declared values are its only context:\n%s", sent[0].Context)
	}

	repo := mkdir(t, filepath.Join(e.app.Paths().UserHome, "repo"))
	if _, err := e.app.CreateTask(t.Context(), TaskInput{YAML: issueTask("issues", repo,
		"  skills: [hf-release-notes]\n  parameters:\n    skills:\n      hf-release-notes: {owner: octo}\n")}); err != nil {
		t.Fatal(err)
	}
	r, err = e.app.RunTask("issues")
	if err != nil {
		t.Fatal(err)
	}
	e.wait(r.ExecutionID, domain.StatusCompleted)
	ctx = e.readContextOf(q.all()[1:])
	if name := firstRepositoryName(valueAt(ctx, "task", "parameters")); name != "octo/repo" {
		t.Errorf("context.json task.parameters' first repository = %v, want octo/repo", name)
	}
	if got := valueAt(ctx, "task", "declaredParameters", "skills", "hf-release-notes", "owner"); got != "octo" {
		t.Errorf("context.json's owner of hf-release-notes = %v, want octo", got)
	}
}

// A task that cites a skill declaring nothing still gets the declared values' object, empty, and the session is not
// pointed at a context.json that holds nothing else.
func TestContextWithNoDeclaredValues(t *testing.T) {
	e := newEnv(t)
	var q requests
	e.claude.setScript(func(req ports.RunRequest) string { q.add(req); return "echo 'END ok'" })
	e.create("bare", "  skills: [hf-github-draft-pr]\n")
	r, err := e.app.RunTask("bare")
	if err != nil {
		t.Fatal(err)
	}
	e.wait(r.ExecutionID, domain.StatusCompleted)
	sent := q.all()
	ctx := e.readContextOf(sent)
	want := map[string]any{"skills": map[string]any{"hf-github-draft-pr": map[string]any{}}, "agents": map[string]any{}}
	if got := valueAt(ctx, "task", "declaredParameters"); !jsonEqual(got, want) {
		t.Errorf("context.json task.declaredParameters = %v, want %v", got, want)
	}
	if strings.Contains(sent[0].Context, "- context.json:") {
		t.Errorf("the session's context points at a context.json with no parameters or history:\n%s", sent[0].Context)
	}
}

// A skill changed after the task was saved, so that the task's values no longer meet its declaration, keeps the
// execution from running the harness: the attempt ends as the task's error, naming the parameter.
func TestExecutionRefusesValuesTheDeclarationRejects(t *testing.T) {
	e := newEnv(t)
	e.declaringBuiltins()
	e.create("notes", "  skills: [hf-release-notes]\n  parameters:\n    skills:\n      hf-release-notes: {owner: ana}\n")
	e.writeBuiltin("skills/hf-release-notes/SKILL.md", "---\nname: hf-release-notes\nparameters:\n"+
		"  - {name: owner, type: number, required: true}\n---\n\nWrite the notes.\n")
	r, err := e.app.RunTask("notes")
	if err != nil {
		t.Fatal(err)
	}
	x := e.wait(r.ExecutionID, domain.StatusFailed)
	if e.claude.promptCount() != 0 {
		t.Errorf("the harness was prepared %d times, want none", e.claude.promptCount())
	}
	if len(x.Attempts) == 0 || x.Attempts[0].ErrorKey != "parameter_wrong_type" {
		t.Errorf("the execution's attempts = %+v, want the first ended by parameter_wrong_type", x.Attempts)
	}
}

// The editor's catalog offers what each built-in declares, the declarations HarmFlow cannot use left out.
func TestCatalogOffersDeclaredParameters(t *testing.T) {
	e := newEnv(t)
	e.declaringBuiltins()
	e.writeBuiltin("skills/hf-odd/SKILL.md", "---\nname: hf-odd\nparameters:\n  - {name: when, type: date}\n"+
		"  - {name: config, type: object}\n  - {name: note, type: text}\n---\n")
	c := e.app.Catalog(t.Context(), "", "")
	params := map[string][]string{}
	for _, entry := range append(c.Skills, c.Agents...) {
		if entry.Parameters == nil {
			params[entry.Name] = nil
			continue
		}
		names := []string{}
		for _, d := range *entry.Parameters {
			names = append(names, d.Name+":"+d.Type)
		}
		params[entry.Name] = names
	}
	for name, want := range map[string][]string{
		"hf-release-notes":   {"owner:text", "limit:number"},
		"hf-queues":          {"repositories:object"},
		"hf-reviewer":        {"strict:boolean"},
		"hf-odd":             {"note:text"},
		"hf-github-draft-pr": {},
	} {
		if got := params[name]; !slices.Equal(got, want) || got == nil {
			t.Errorf("Catalog %s parameters = %q, want %q", name, got, want)
		}
	}
}

// readContextOf reads the context.json of the first request's execution, and checks it against its schema.
func (e *env) readContextOf(sent []ports.RunRequest) map[string]any {
	e.t.Helper()
	if len(sent) == 0 {
		e.t.Fatal("no request reached the harness")
	}
	path := filepath.Join(sent[0].ExecutionDir, "context.json")
	data, err := os.ReadFile(path)
	if err != nil {
		e.t.Fatalf("os.ReadFile(%s) = %v", path, err)
	}
	doc, err := schema.FromJSON(data)
	if err != nil {
		e.t.Fatal(err)
	}
	if issues := e.app.d.Validator.ValidateContext(doc); len(issues) > 0 {
		e.t.Errorf("context.json does not fit context.schema.json: %+v\n%s", issues, data)
	}
	var ctx map[string]any
	if err := json.Unmarshal(data, &ctx); err != nil {
		e.t.Fatalf("json.Unmarshal(%s) = %v", path, err)
	}
	return ctx
}

func jsonEqual(got, want any) bool {
	a, err1 := json.Marshal(got)
	b, err2 := json.Marshal(want)
	return err1 == nil && err2 == nil && string(a) == string(b)
}

func lastSegment(pointer string) string {
	return pointer[strings.LastIndex(pointer, "/")+1:]
}
