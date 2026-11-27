package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/claudecode"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/codex"
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

	// The task editor's YAML tab shows the YAML validation writes for the form's document: each value under its own
	// skill or agent.
	var plain any
	if err := yaml.Unmarshal([]byte(doc), &plain); err != nil {
		t.Fatal(err)
	}
	form, err := json.Marshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	r := e.app.Validate(t.Context(), TaskInput{Document: form})
	var shown struct {
		Spec struct {
			Parameters domain.DeclaredParameters `yaml:"parameters"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal([]byte(r.YAML), &shown); err != nil || !reflect.DeepEqual(shown.Spec.Parameters, p) {
		t.Errorf("Validate(the form's document) YAML =\n%s\n(%v), want spec.parameters %+v", r.YAML, err, p)
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
	cases := []struct {
		name, parameters, key, path string
	}{
		{"no owner", "  parameters:\n    skills:\n" + queues, "parameter_required", "/spec/parameters/skills/hf-release-notes/owner"},
		{"a limit in words", "  parameters:\n    skills:\n      hf-release-notes: {owner: ana, limit: three}\n" + queues,
			"parameter_wrong_type", "/spec/parameters/skills/hf-release-notes/limit"},
		{"repositories its schema rejects", "  parameters:\n    skills:\n      hf-release-notes: {owner: ana}\n" +
			"      hf-queues:\n        repositories: {octo/repo: {public: maybe}}\n",
			"parameter_schema_mismatch", "/spec/parameters/skills/hf-queues/repositories"},
	}
	// names says whether an issue is the case's error, naming the parameter and its skill: as validation gives it, or
	// as the detail of a task file's error.
	names := func(key, path string) func(schemaIssue) bool {
		return func(is schemaIssue) bool {
			return (is.Key == key || is.Params["detailKey"] == key) && is.Path == path &&
				strings.Contains(is.Message, `"`+lastSegment(path)+`"`) && strings.Contains(is.Message, "hf-")
		}
	}

	// A valid saved task that every refused update below starts from; its limit is not the default.
	keptParams := "  parameters:\n    skills:\n      hf-release-notes: {owner: ana, limit: 5}\n" + queues
	kept, err := e.app.CreateTask(t.Context(), TaskInput{YAML: taskYAML("kept", cites+keptParams)})
	if err != nil {
		t.Fatalf("CreateTask(kept) = %v", err)
	}
	keptFile, err := os.ReadFile(kept.Path)
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			refused := func(how string, err error) {
				t.Helper()
				var ae *Error
				if !errors.As(err, &ae) || ae.Validation == nil || !slices.ContainsFunc(ae.Validation.Errors, names(c.key, c.path)) {
					t.Errorf("%s = %v, want %s at %s, naming the parameter and its skill", how, err, c.key, c.path)
				}
			}
			for _, id := range []string{"checked", "kept"} {
				doc := taskYAML(id, cites+c.parameters)
				var plain any
				if err := yaml.Unmarshal([]byte(doc), &plain); err != nil {
					t.Fatal(err)
				}
				form, err := json.Marshal(plain)
				if err != nil {
					t.Fatal(err)
				}
				if id == "checked" {
					_, err = e.app.CreateTask(t.Context(), TaskInput{YAML: doc})
					refused("CreateTask(yaml)", err)
					_, err = e.app.CreateTask(t.Context(), TaskInput{Document: form})
					refused("CreateTask(document)", err)
					continue
				}
				_, err = e.app.UpdateTask(t.Context(), id, TaskInput{YAML: doc, BaseRevision: kept.Revision})
				refused("UpdateTask(kept, yaml)", err)
				_, err = e.app.UpdateTask(t.Context(), id, TaskInput{Document: form, BaseRevision: kept.Revision})
				refused("UpdateTask(kept, document)", err)
			}

			// The refused updates leave the earlier valid definition, on disk and as the daemon reads it.
			if data, err := os.ReadFile(kept.Path); err != nil || string(data) != string(keptFile) {
				t.Errorf("the task file after refused updates =\n%s\n(%v), want the earlier valid one\n%s", data, err, keptFile)
			}
			view, err := e.app.GetTask("kept")
			if err != nil {
				t.Fatalf("GetTask(kept) = %v", err)
			}
			if view.Invalid || view.Revision != kept.Revision || view.YAML != string(keptFile) {
				t.Errorf("GetTask(kept) after refused updates = invalid %v, revision %s, YAML\n%s\nwant valid at %s, the earlier YAML",
					view.Invalid, view.Revision, view.YAML, kept.Revision)
			}
			want := map[string]any{"owner": "ana", "limit": 5}
			if got := valueAt(view.Document, "spec", "parameters", "skills", "hf-release-notes"); !jsonEqual(got, want) {
				t.Errorf("GetTask(kept) values of hf-release-notes = %v, want %v", got, want)
			}
		})
	}

	// A run of the task the updates were refused for starts from its earlier valid definition.
	var q requests
	e.claude.setScript(func(req ports.RunRequest) string { q.add(req); return "echo 'END ok'" })
	r, err := e.app.RunTask("kept")
	if err != nil {
		t.Fatalf("RunTask(kept) after refused updates = %v, want it started", err)
	}
	e.wait(r.ExecutionID, domain.StatusCompleted)
	ctx := e.readContextOf(q.all())
	if got, want := valueAt(ctx, "task", "declaredParameters", "skills", "hf-release-notes"),
		map[string]any{"owner": "ana", "limit": 5}; !jsonEqual(got, want) {
		t.Errorf("context.json's values of hf-release-notes after refused updates = %v, want %v", got, want)
	}

	// A file written with a text editor over a valid task, with each case in turn, loads as invalid with the case's
	// error, its last valid version is kept, and the task does not run.
	good := taskYAML("edited", cites+"  parameters:\n    skills:\n      hf-release-notes: {owner: ana}\n"+queues)
	v, err := e.app.CreateTask(t.Context(), TaskInput{YAML: good})
	if err != nil {
		t.Fatalf("CreateTask(edited) = %v", err)
	}
	for _, c := range cases {
		t.Run("edited with "+c.name, func(t *testing.T) {
			if err := os.WriteFile(v.Path, []byte(taskYAML("edited", cites+c.parameters)), 0o600); err != nil {
				t.Fatal(err)
			}
			e.app.scanTaskFiles(t.Context())
			edited, err := e.app.GetTask("edited")
			if err != nil {
				t.Fatalf("GetTask(edited) = %v", err)
			}
			if !edited.Invalid || edited.Validation == nil || !slices.ContainsFunc(edited.Validation.Errors, names(c.key, c.path)) {
				t.Errorf("GetTask(edited) after an outside edit = invalid %v, validation %+v; want invalid, %s at %s naming "+
					"the parameter and its skill", edited.Invalid, edited.Validation, c.key, c.path)
			}
			want := map[string]any{"owner": "ana"}
			if got := valueAt(edited.Effective, "spec", "parameters", "skills", "hf-release-notes"); !jsonEqual(got, want) {
				t.Errorf("GetTask(edited) effective values of hf-release-notes = %v, want the last valid %v", got, want)
			}
			if _, err := e.app.RunTask("edited"); err == nil {
				t.Error("RunTask(edited) = no error, want an invalid task refused")
			}
		})
	}
}

// lookupHarness is a fake harness whose user's agents and skills of the kinds it names are the ones a real adapter of
// the same harness finds, with info as that adapter's detection: the daemon reads their declarations from the file the
// real lookup gives, and the session still runs on the fake.
type lookupHarness struct {
	*fakeHarness
	real  ports.HarnessAdapter
	info  ports.HarnessInfo
	kinds []string
}

var _ ports.HarnessAdapter = lookupHarness{}

func (h lookupHarness) UserItem(ctx context.Context, info ports.HarnessInfo, kind, name, workspace string, added []string) (ports.ResolvedItem, bool, []ports.DiscoveryError, error) {
	if !slices.Contains(h.kinds, kind) {
		return h.fakeHarness.UserItem(ctx, info, kind, name, workspace, added)
	}
	return h.real.UserItem(ctx, h.info, kind, name, workspace, added)
}

func (h lookupHarness) UserItems(ctx context.Context, info ports.HarnessInfo, kind, workspace string, added []string) ([]ports.ResolvedItem, []ports.DiscoveryError, error) {
	if !slices.Contains(h.kinds, kind) {
		return h.fakeHarness.UserItems(ctx, info, kind, workspace, added)
	}
	return h.real.UserItems(ctx, h.info, kind, workspace, added)
}

// offered is what the catalog offers for the agent or skill of that name, each parameter as name:type; nil when it
// offers no parameters for it.
func offered(c Catalog, kind, name string) []string {
	entries := c.Skills
	if kind == "agent" {
		entries = c.Agents
	}
	for _, entry := range entries {
		if entry.Name != name || entry.Parameters == nil {
			continue
		}
		out := []string{}
		for _, d := range *entry.Parameters {
			out = append(out, d.Name+":"+d.Type)
		}
		return out
	}
	return nil
}

// refusedNaming checks that err refuses a save with key at path, its message naming the parameter and the item.
func refusedNaming(t *testing.T, how string, err error, key, path, item string) {
	t.Helper()
	var ae *Error
	if !errors.As(err, &ae) || ae.Validation == nil || !slices.ContainsFunc(ae.Validation.Errors, func(is schemaIssue) bool {
		return is.Key == key && is.Path == path && strings.Contains(is.Message, `"`+lastSegment(path)+`"`) &&
			strings.Contains(is.Message, `"`+item+`"`)
	}) {
		t.Errorf("%s = %v, want %s at %s, naming the parameter and %s", how, err, key, path, item)
	}
}

const reviewerDeclaring = "---\nname: reviewer\ndescription: Reviews\nparameters:\n" +
	"  - {name: strict, type: boolean, description: Refuse on any finding}\n---\n\nReview.\n"

// A user's agent declares its parameters in the front matter of the Markdown file its harness's lookup finds: in a
// Claude Code configuration directory, and in a Codex home's agents folder. The catalog offers them, a task citing the
// agent saves its value under the agent, a value of the wrong type is refused naming the parameter and the agent, and
// the execution's context.json carries the value.
func TestUsersAgentDeclaresParametersThroughItsHarness(t *testing.T) {
	for _, c := range []struct {
		name  string
		codex bool
	}{{"Claude Code", false}, {"Codex", true}} {
		t.Run(c.name, func(t *testing.T) {
			folder := t.TempDir()
			var configure func(*env)
			if c.codex {
				t.Setenv("CODEX_HOME", folder)
				cx := &codex.Adapter{Home: t.TempDir(), KnownLocations: []string{}}
				t.Cleanup(cx.Close)
				configure = func(e *env) { e.signer = lookupHarness{fakeHarness: e.codex, real: cx, kinds: []string{"agent"}} }
			} else {
				t.Setenv("CLAUDE_CONFIG_DIR", folder)
				cc := &claudecode.Adapter{Home: t.TempDir(), PathEnv: t.TempDir(), KnownLocations: []string{}}
				configure = func(e *env) { e.signer = lookupHarness{fakeHarness: e.claude, real: cc, kinds: []string{"agent"}} }
			}
			writeFile(t, filepath.Join(folder, "agents", "reviewer.md"), reviewerDeclaring)
			e := newEnv(t, configure)
			h, task := e.claude, taskYAML
			if c.codex {
				h, task = e.codex, codexTaskYAML
			}
			ctx := t.Context()

			if got, want := offered(e.app.Catalog(ctx, "", ""), "agent", "reviewer"), []string{"strict:boolean"}; !slices.Equal(got, want) {
				t.Errorf("Catalog reviewer parameters = %q, want %q", got, want)
			}

			v, err := e.app.CreateTask(ctx, TaskInput{YAML: task("review",
				"  agents: [reviewer]\n  parameters:\n    agents:\n      reviewer: {strict: true}\n")})
			if err != nil {
				t.Fatalf("CreateTask(review) = %v, want it saved", err)
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
			if got := file.Spec.Parameters.Agents["reviewer"]["strict"]; got != true {
				t.Errorf("the saved file's spec.parameters.agents.reviewer.strict = %v, want true", got)
			}

			_, err = e.app.CreateTask(ctx, TaskInput{YAML: task("maybe",
				"  agents: [reviewer]\n  parameters:\n    agents:\n      reviewer: {strict: maybe}\n")})
			refusedNaming(t, "CreateTask(maybe)", err, "parameter_wrong_type", "/spec/parameters/agents/reviewer/strict", "reviewer")

			var q requests
			h.setScript(func(req ports.RunRequest) string { q.add(req); return "echo 'END ok'" })
			r, err := e.app.RunTask("review")
			if err != nil {
				t.Fatalf("RunTask(review) = %v", err)
			}
			e.wait(r.ExecutionID, domain.StatusCompleted)
			got := valueAt(e.readContextOf(q.all()), "task", "declaredParameters", "agents", "reviewer")
			if want := map[string]any{"strict": true}; !jsonEqual(got, want) {
				t.Errorf("context.json task.declaredParameters.agents.reviewer = %v, want %v", got, want)
			}
		})
	}
}

const releaseNotesDeclaring = "---\nname: release-notes\ndescription: Writes release notes\nparameters:\n" +
	"  - {name: owner, type: text, description: Who owns the repository, required: true}\n" +
	"  - {name: limit, type: number, description: How many changes, default: 3}\n---\n\nWrite the notes.\n"

// A Codex user's skill declares its parameters in the front matter of the SKILL.md Codex's skill list reports. The
// catalog offers them, a task with no required owner is refused naming the parameter and the skill, and an execution
// with the owner set carries the limit's default in its context.json.
func TestCodexUsersSkillDeclaresParametersThroughItsSkillList(t *testing.T) {
	skill := filepath.Join(t.TempDir(), "skills", "release-notes", "SKILL.md")
	writeFile(t, skill, releaseNotesDeclaring)
	bin := filepath.Join(t.TempDir(), "codex")
	received := filepath.Join(t.TempDir(), "received.jsonl")
	if err := os.WriteFile(bin, []byte(skillListCodex(received, listAnswer([]string{"release-notes", skill}, ""))), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", t.TempDir())
	cx := &codex.Adapter{Home: t.TempDir(), KnownLocations: []string{}, ProbeTimeout: time.Minute}
	t.Cleanup(cx.Close)
	info := cx.Detect(context.Background(), bin)
	if !info.Ready() {
		t.Fatalf("Detect(%s) = %+v, want the stand-in Codex ready", bin, info)
	}
	e := newEnv(t, func(e *env) {
		e.signer = lookupHarness{fakeHarness: e.codex, real: cx, info: info, kinds: []string{"skill"}}
	})
	ctx := t.Context()

	if got, want := offered(e.app.Catalog(ctx, "", ""), "skill", "release-notes"), []string{"owner:text", "limit:number"}; !slices.Equal(got, want) {
		t.Errorf("Catalog release-notes parameters = %q, want %q", got, want)
	}

	_, err := e.app.CreateTask(ctx, TaskInput{YAML: codexTaskYAML("unowned",
		"  skills: [release-notes]\n  parameters:\n    skills:\n      release-notes: {limit: 5}\n")})
	refusedNaming(t, "CreateTask(unowned)", err, "parameter_required", "/spec/parameters/skills/release-notes/owner", "release-notes")

	if _, err := e.app.CreateTask(ctx, TaskInput{YAML: codexTaskYAML("owned",
		"  skills: [release-notes]\n  parameters:\n    skills:\n      release-notes: {owner: ana}\n")}); err != nil {
		t.Fatalf("CreateTask(owned) = %v, want it saved", err)
	}
	var q requests
	e.codex.setScript(func(req ports.RunRequest) string { q.add(req); return "echo 'END ok'" })
	r, err := e.app.RunTask("owned")
	if err != nil {
		t.Fatalf("RunTask(owned) = %v", err)
	}
	e.wait(r.ExecutionID, domain.StatusCompleted)
	got := valueAt(e.readContextOf(q.all()), "task", "declaredParameters", "skills", "release-notes")
	if want := map[string]any{"owner": "ana", "limit": 3}; !jsonEqual(got, want) {
		t.Errorf("context.json task.declaredParameters.skills.release-notes = %v, want %v", got, want)
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

// pdfSkill is a plugin's skill that declares parameters as a user's skill would: a required text and a number with a
// default.
const pdfSkill = "---\nname: pdf\ndescription: Reads PDFs\nparameters:\n" +
	"  - {name: owner, type: text, description: Who owns the document, required: true}\n" +
	"  - {name: limit, type: number, description: How many pages, default: 3}\n---\n\nRead the PDF.\n"

// pluginEnv is an env whose Claude Code is the real adapter on a stand-in CLI that lists pdf:pdf, the skill pdf of a
// plugin pdf that Claude Code's record of the plugins installed holds for the user. The skill's SKILL.md, which the
// returned skillFile names, holds skill, and is not written when skill is empty. Each session of an execution copies
// its context.json to the returned context and succeeds.
func pluginEnv(t *testing.T, skill string) (e *env, skillFile, context string) {
	t.Helper()
	context = filepath.Join(t.TempDir(), "context.json")
	e = newClaudeEnvOn(t, claudeListing(`case "$*" in
    "-p /usage") echo 'Current session: 4% used' ;;
    *--permission-mode*) cp "$HARMFLOW_EXECUTION_DIR/context.json" '`+context+`'
      echo '{"type":"result","subtype":"success","is_error":false,"result":"done"}' ;;
    *) echo '{"type":"system","subtype":"init","skills":["simplify","pdf:pdf"]}' ;;
  esac`), 0)
	plugin := t.TempDir()
	record := `{"version":2,"plugins":{"pdf@market":[{"scope":"user","installPath":"` + plugin + `","version":"1.0.0"}]}}`
	writeFile(t, filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "plugins", "installed_plugins.json"), record)
	skillFile = filepath.Join(plugin, "skills", "pdf", "SKILL.md")
	if skill != "" {
		writeFile(t, skillFile, skill)
	}
	return e, skillFile, context
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// hasUnverified reports whether issues hold the warning that pdf:pdf's values are passed through unchecked.
func hasUnverified(issues []schemaIssue) bool {
	return slices.ContainsFunc(issues, func(is schemaIssue) bool { return is.Key == "parameters_unverified" })
}

// A skill of an installed plugin declares its parameters as a user's skill does: the catalog offers them, a missing
// required value and a value of the wrong type are refused, each naming the parameter and the skill, with no warning
// that the values are unchecked, and an execution's context.json carries the values with the default of the one left
// unset.
func TestPluginSkillDeclaresParameters(t *testing.T) {
	e, _, context := pluginEnv(t, pdfSkill)
	ctx := t.Context()

	var offered []string
	for _, entry := range e.app.Catalog(ctx, "", "").Skills {
		if entry.Name == "pdf:pdf" && entry.Parameters != nil {
			offered = []string{}
			for _, d := range *entry.Parameters {
				offered = append(offered, d.Name+":"+d.Type)
			}
		}
	}
	if want := []string{"owner:text", "limit:number"}; !slices.Equal(offered, want) {
		t.Errorf("Catalog pdf:pdf parameters = %q, want %q", offered, want)
	}

	for _, c := range []struct {
		name, values, key, path string
	}{
		{"no owner", "{limit: 2}", "parameter_required", "/spec/parameters/skills/pdf:pdf/owner"},
		{"a limit in words", "{owner: ana, limit: three}", "parameter_wrong_type", "/spec/parameters/skills/pdf:pdf/limit"},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc := taskYAML("checked-"+strings.ReplaceAll(c.key, "_", "-"), "  skills: [pdf:pdf]\n  parameters:\n    skills:\n      pdf:pdf: "+c.values+"\n")
			_, err := e.app.CreateTask(ctx, TaskInput{YAML: doc})
			var ae *Error
			if !errors.As(err, &ae) || ae.Validation == nil || !slices.ContainsFunc(ae.Validation.Errors, func(is schemaIssue) bool {
				return is.Key == c.key && is.Path == c.path && strings.Contains(is.Message, `"`+lastSegment(c.path)+`"`) &&
					strings.Contains(is.Message, `"pdf:pdf"`)
			}) {
				t.Errorf("CreateTask(%s) = %v, want %s at %s, naming the parameter and its skill", c.name, err, c.key, c.path)
			}
			if ae != nil && ae.Validation != nil && hasUnverified(ae.Validation.Warnings) {
				t.Errorf("CreateTask(%s) warnings = %+v, want no parameters_unverified", c.name, ae.Validation.Warnings)
			}
		})
	}

	e.create("pdf", "  skills: [pdf:pdf]\n  parameters:\n    skills:\n      pdf:pdf: {owner: ana}\n")
	if r := e.app.Validate(ctx, TaskInput{YAML: taskYAML("pdf", "  skills: [pdf:pdf]\n  parameters:\n    skills:\n"+
		"      pdf:pdf: {owner: ana}\n")}); !r.Valid || hasUnverified(r.Warnings) {
		t.Errorf("Validate(pdf) = valid %v, warnings %+v; want valid, with no parameters_unverified", r.Valid, r.Warnings)
	}
	r, err := e.app.RunTask("pdf")
	if err != nil {
		t.Fatalf("RunTask(pdf) = %v", err)
	}
	e.wait(r.ExecutionID, domain.StatusCompleted)
	got := valueAt(e.readContextFile(context), "task", "declaredParameters", "skills", "pdf:pdf")
	if want := map[string]any{"owner": "ana", "limit": 3}; !jsonEqual(got, want) {
		t.Errorf("context.json's values of pdf:pdf = %v, want %v", got, want)
	}
}

// A value whose declaration the plugin's skill dropped warns, naming the parameter and the skill, is left out of the
// execution's context.json, and is dropped on the next save.
func TestPluginSkillUndeclaredParameterWarnsAndIsDropped(t *testing.T) {
	e, skillFile, context := pluginEnv(t, pdfSkill)
	ctx := t.Context()
	doc := taskYAML("pdf", "  skills: [pdf:pdf]\n  parameters:\n    skills:\n      pdf:pdf: {owner: ana, limit: 5}\n")
	if _, err := e.app.CreateTask(ctx, TaskInput{YAML: doc}); err != nil {
		t.Fatalf("CreateTask(pdf) = %v", err)
	}
	writeFile(t, skillFile, "---\nname: pdf\nparameters:\n  - {name: owner, type: text, required: true}\n---\n\nRead the PDF.\n")

	undeclared := func(is schemaIssue) bool {
		return is.Key == "parameter_undeclared" && is.Path == "/spec/parameters/skills/pdf:pdf/limit" &&
			strings.Contains(is.Message, `"limit"`) && strings.Contains(is.Message, `"pdf:pdf"`)
	}
	if r := e.app.Validate(ctx, TaskInput{YAML: doc}); !r.Valid || !slices.ContainsFunc(r.Warnings, undeclared) {
		t.Errorf("Validate(pdf) = valid %v, warnings %+v; want valid, warning of limit", r.Valid, r.Warnings)
	}

	run, err := e.app.RunTask("pdf")
	if err != nil {
		t.Fatalf("RunTask(pdf) = %v", err)
	}
	x := e.wait(run.ExecutionID, domain.StatusCompleted)
	if !slices.ContainsFunc(x.Warnings, func(w domain.Warning) bool {
		return w.Text.Key == "parameter_undeclared" && w.Text.Params["name"] == "limit" && w.Text.Params["item"] == "pdf:pdf"
	}) {
		t.Errorf("the execution's warnings = %+v, want one of limit and pdf:pdf", x.Warnings)
	}
	got := valueAt(e.readContextFile(context), "task", "declaredParameters", "skills", "pdf:pdf")
	if want := map[string]any{"owner": "ana"}; !jsonEqual(got, want) {
		t.Errorf("context.json's values of pdf:pdf = %v, want %v", got, want)
	}

	view, _ := e.app.GetTask("pdf")
	saved, err := e.app.UpdateTask(ctx, "pdf", TaskInput{YAML: doc, BaseRevision: view.Revision})
	if err != nil {
		t.Fatalf("UpdateTask(pdf) = %v", err)
	}
	if data, _ := os.ReadFile(saved.Path); strings.Contains(string(data), "limit") || !strings.Contains(string(data), "owner: ana") {
		t.Errorf("the saved file =\n%s\nwant limit dropped and the owner kept", data)
	}
}

// A plugin's skill whose SKILL.md cannot be found runs as before: its values pass through as the task gives them,
// behind the warning that they are unchecked.
func TestPluginSkillWithNoFilePassesValuesThrough(t *testing.T) {
	e, _, context := pluginEnv(t, "")
	ctx := t.Context()
	doc := taskYAML("pdf", "  skills: [pdf:pdf]\n  parameters:\n    skills:\n      pdf:pdf: {limit: three}\n")
	r := e.app.Validate(ctx, TaskInput{YAML: doc})
	if !r.Valid || !slices.ContainsFunc(r.Warnings, func(is schemaIssue) bool {
		return is.Key == "parameters_unverified" && is.Path == "/spec/parameters/skills/pdf:pdf"
	}) {
		t.Errorf("Validate(pdf) = valid %v, errors %+v, warnings %+v; want valid, with parameters_unverified on pdf:pdf",
			r.Valid, r.Errors, r.Warnings)
	}
	for _, entry := range e.app.Catalog(ctx, "", "").Skills {
		if entry.Name == "pdf:pdf" && entry.Parameters != nil {
			t.Errorf("Catalog pdf:pdf parameters = %+v, want none known", *entry.Parameters)
		}
	}

	if _, err := e.app.CreateTask(ctx, TaskInput{YAML: doc}); err != nil {
		t.Fatalf("CreateTask(pdf) = %v", err)
	}
	run, err := e.app.RunTask("pdf")
	if err != nil {
		t.Fatalf("RunTask(pdf) = %v", err)
	}
	e.wait(run.ExecutionID, domain.StatusCompleted)
	got := valueAt(e.readContextFile(context), "task", "declaredParameters", "skills", "pdf:pdf")
	if want := map[string]any{"limit": "three"}; !jsonEqual(got, want) {
		t.Errorf("context.json's values of pdf:pdf = %v, want %v, as the task gives them", got, want)
	}
}

// readContextOf reads the context.json of the first request's execution, and checks it against its schema.
func (e *env) readContextOf(sent []ports.RunRequest) map[string]any {
	e.t.Helper()
	if len(sent) == 0 {
		e.t.Fatal("no request reached the harness")
	}
	return e.readContextFile(filepath.Join(sent[0].ExecutionDir, "context.json"))
}

// readContextFile reads the context.json at path, and checks it against its schema.
func (e *env) readContextFile(path string) map[string]any {
	e.t.Helper()
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
