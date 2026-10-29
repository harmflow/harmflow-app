package schema

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/harmflow/harmflow-app/daemon/internal/contracts"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

const valid = `apiVersion: harmflow/v1alpha1
kind: Task
metadata:
  id: weekly-notes
  name: Weekly notes
spec:
  type: general
  prompt: Summarize the week.
  harness:
    preferred:
      provider: claude-code
`

func TestValidTask(t *testing.T) {
	v, err := New()
	if err != nil {
		t.Fatal(err)
	}
	doc, node, issues := ParseYAML([]byte(valid))
	if len(issues) > 0 {
		t.Fatal(issues)
	}
	if got := v.ValidateTask(doc, node); len(got) > 0 {
		t.Fatalf("valid task rejected: %+v", got)
	}
}

// A syntax error names the line.
func TestSyntaxErrorLine(t *testing.T) {
	_, _, issues := ParseYAML([]byte("apiVersion: harmflow/v1alpha1\nkind: Task\nmetadata:\n  id: [unclosed\n"))
	if len(issues) != 1 || issues[0].Line == 0 || issues[0].Code != "syntax" {
		t.Fatalf("got %+v", issues)
	}
}

// A document that parses but cannot be validated is refused with a text of its own: an empty one, and one holding a
// value JSON cannot carry, such as YAML's not-a-number.
func TestUnreadableDocument(t *testing.T) {
	cases := []struct{ name, yaml, key string }{
		{"empty", "", "document_empty"},
		{"not a number", valid + "  limit: .nan\n", "document_not_representable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc, _, issues := ParseYAML([]byte(c.yaml))
			if doc != nil || len(issues) != 1 || issues[0].Key != c.key || issues[0].Code != "syntax" {
				t.Errorf("ParseYAML(%q) = %v, %+v; want no document and one syntax issue %s", c.yaml, doc, issues, c.key)
			}
		})
	}
}

// A key YAML reads as a number keeps its text, so the schema names it as written.
func TestNumericKeyKeepsItsText(t *testing.T) {
	v, err := New()
	if err != nil {
		t.Fatalf("New() = %v, want no error", err)
	}
	text := valid + "  1: one\n"
	doc, node, issues := ParseYAML([]byte(text))
	if len(issues) > 0 {
		t.Fatalf("ParseYAML(%q) issues = %+v, want none", text, issues)
	}
	got := v.ValidateTask(doc, node)
	if len(got) != 1 || got[0].Key != "schema_additional_properties" || got[0].Params["properties"] != `"1"` {
		t.Errorf("ValidateTask(%q) = %+v, want one schema_additional_properties issue for \"1\"", text, got)
	}
}

// versionSecond is the valid task with kind first, so apiVersion sits on line 2 and a location that fell back to the
// document's start would show.
var versionSecond = strings.Replace(valid, "apiVersion: harmflow/v1alpha1\nkind: Task\n",
	"kind: Task\napiVersion: harmflow/v1alpha1\n", 1)

// A task file names its schema version, and one with no version or another version is refused.
func TestSchemaVersionIsRequired(t *testing.T) {
	v, _ := New()
	doc, node, _ := ParseYAML([]byte(versionSecond))
	if issues := v.ValidateTask(doc, node); len(issues) > 0 {
		t.Fatalf("the versioned task was rejected: %+v", issues)
	}
	doc, node, _ = ParseYAML([]byte(strings.Replace(versionSecond, "apiVersion: harmflow/v1alpha1\n", "", 1)))
	issues := v.ValidateTask(doc, node)
	if len(issues) != 1 || issues[0].Key != "schema_required" || issues[0].Params["properties"] != `"apiVersion"` ||
		issues[0].Path != "/apiVersion" {
		t.Errorf("a task with no apiVersion: got %+v, want apiVersion required at /apiVersion", issues)
	}
	doc, node, _ = ParseYAML([]byte(strings.Replace(versionSecond, "harmflow/v1alpha1", "harmflow/v1", 1)))
	issues = v.ValidateTask(doc, node)
	if len(issues) != 1 || issues[0].Key != "schema_const" || issues[0].Path != "/apiVersion" || issues[0].Line != 2 ||
		issues[0].Params["value"] != `"harmflow/v1alpha1"` {
		t.Errorf("apiVersion harmflow/v1: got %+v, want it to be harmflow/v1alpha1 at /apiVersion on line 2", issues)
	}
}

// A general task takes no repository; a GitHub task needs them; a schema error is located.
func TestTypeParametersAndLocation(t *testing.T) {
	v, _ := New()
	gh := strings.Replace(valid, "type: general", "type: github", 1)
	doc, node, _ := ParseYAML([]byte(gh))
	issues := v.ValidateTask(doc, node)
	if len(issues) == 0 || !strings.Contains(issues[0].Message, `"github"`) {
		t.Fatalf("a GitHub task without repositories passed: %+v", issues)
	}
	withRepo := valid + "  github:\n    repositories:\n      - name: o/r\n        localPath: /x\n"
	doc, node, _ = ParseYAML([]byte(withRepo))
	if issues := v.ValidateTask(doc, node); len(issues) == 0 {
		t.Fatal("a general task accepted repositories")
	}
	bad := strings.Replace(valid, "provider: claude-code", "provider: claude-code\n      effort: 3", 1)
	doc, node, _ = ParseYAML([]byte(bad))
	issues = v.ValidateTask(doc, node)
	if len(issues) == 0 || issues[0].Line != 12 || issues[0].Path != "/spec/harness/preferred/effort" {
		t.Fatalf("got %+v, want the effort on line 12", issues)
	}
}

// legacyGitHub is a GitHub task under the type's legacy names, its block's localPath on line 15.
const legacyGitHub = `apiVersion: harmflow/v1alpha1
kind: Task
metadata:
  id: implement-issues
  name: Implement issues
spec:
  type: github-issue-implementation
  prompt: Implement the oldest ready issue.
  harness:
    preferred:
      provider: claude-code
  githubIssueImplementation:
    repositories:
      - name: octo/app
        localPath: /src/app
`

// A task file with the GitHub type's legacy names, or with either of them, reads as a github task with a github block,
// in the value the schema validates and in the node tree a save encodes, even when the type or the block's key is an
// alias. A spec that has both blocks keeps them, and the schema refuses the legacy one; a document with the current
// names, or with no spec mapping, is left as it is.
func TestRenameLegacy(t *testing.T) {
	v, err := New()
	if err != nil {
		t.Fatalf("New() = %v, want no error", err)
	}
	current := strings.NewReplacer("type: github-issue-implementation", "type: github",
		"githubIssueImplementation:", "github:").Replace(legacyGitHub)
	cases := []struct {
		name, text string
		renamed    bool
		// keeps is whether the legacy block stays beside a github block.
		keeps bool
		// tree is whether the node tree, decoded again, reads as the renamed value: a spec reached through an alias or
		// a merge key keeps its legacy names in the tree.
		tree bool
		// task is whether the text is a whole task, which issues are the keys of what validation finds in.
		task   bool
		issues []string
	}{
		{"legacy type and block", legacyGitHub, true, false, true, true, nil},
		{"legacy block only", strings.Replace(legacyGitHub, "type: github-issue-implementation", "type: github", 1),
			true, false, true, true, nil},
		{"legacy type only", strings.Replace(legacyGitHub, "githubIssueImplementation:", "github:", 1),
			true, false, true, true, nil},
		{"current names", current, false, false, true, true, nil},
		{"both blocks", legacyGitHub + "  github:\n    repositories:\n      - name: o/r\n        localPath: /x\n",
			true, true, true, true, []string{"schema_additional_properties"}},
		{"no spec", "apiVersion: harmflow/v1alpha1\n", false, false, true, false, nil},
		{"a spec that is no mapping", "spec: text\n", false, false, true, false, nil},
		{"a type through an alias", strings.NewReplacer("name: Implement issues", "name: &t github-issue-implementation",
			"type: github-issue-implementation", "type: *t").Replace(legacyGitHub), true, false, true, true, nil},
		{"a block key through an alias", strings.NewReplacer("name: Implement issues", "name: &k githubIssueImplementation",
			"githubIssueImplementation:", "*k :").Replace(legacyGitHub), true, false, true, true, nil},
		{"a spec through an alias", "x: &s\n  type: github-issue-implementation\nspec: *s\n",
			true, false, false, false, nil},
		{"a spec through a merge key", "x: &top\n  spec:\n    type: github-issue-implementation\n<<: *top\n",
			true, false, false, false, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc, node, issues := ParseYAML([]byte(c.text))
			if len(issues) > 0 {
				t.Fatalf("ParseYAML(%q) issues = %+v, want none", c.text, issues)
			}
			if got := RenameLegacy(doc, node); got != c.renamed {
				t.Errorf("RenameLegacy(%q) = %v, want %v", c.text, got, c.renamed)
			}
			spec, _ := doc.(map[string]any)["spec"].(map[string]any)
			if c.renamed && spec["type"] != domain.TypeGitHub {
				t.Errorf("RenameLegacy(%q) left spec.type %v, want %s", c.text, spec["type"], domain.TypeGitHub)
			}
			if _, kept := spec[domain.LegacyGitHubKey]; kept != c.keeps {
				t.Errorf("RenameLegacy(%q) kept the %s block: %v, want %v", c.text, domain.LegacyGitHubKey, kept, c.keeps)
			}
			out, err := yaml.Marshal(node)
			if err != nil {
				t.Fatalf("yaml.Marshal(the renamed tree) = %v, want no error", err)
			}
			again, _, _ := ParseYAML(out)
			if same := reflect.DeepEqual(again, doc); same != c.tree {
				t.Errorf("reflect.DeepEqual(ParseYAML(yaml.Marshal(RenameLegacy(%q))), the renamed value) = %v, "+
					"want %v; the tree reads %v, the value %v", c.text, same, c.tree, again, doc)
			}
			if !c.task {
				return
			}
			var keys []string
			for _, is := range v.ValidateTask(doc, node) {
				keys = append(keys, is.Key)
			}
			if !slices.Equal(keys, c.issues) {
				t.Errorf("ValidateTask(the renamed %q) issues %v, want %v", c.name, keys, c.issues)
			}
		})
	}
}

// An issue in a renamed block is named by its current path and located in the text as written.
func TestRenameLegacyKeepsTheLocation(t *testing.T) {
	v, err := New()
	if err != nil {
		t.Fatalf("New() = %v, want no error", err)
	}
	text := strings.Replace(legacyGitHub, "localPath: /src/app", `localPath: ""`, 1)
	doc, node, _ := ParseYAML([]byte(text))
	RenameLegacy(doc, node)
	issues := v.ValidateTask(doc, node)
	if len(issues) != 1 || issues[0].Path != "/spec/github/repositories/0/localPath" || issues[0].Line != 15 {
		t.Errorf("ValidateTask(the renamed task with an empty localPath) = %+v, want one issue at "+
			"/spec/github/repositories/0/localPath on line 15", issues)
	}
}

// The form's document, decoded from JSON and with no node tree, is renamed the same way.
func TestRenameLegacyDocument(t *testing.T) {
	doc, err := FromJSON([]byte(`{"spec": {"type": "github-issue-implementation",
		"githubIssueImplementation": {"repositories": []}}}`))
	if err != nil {
		t.Fatalf("FromJSON = %v, want no error", err)
	}
	if !RenameLegacy(doc, nil) {
		t.Fatal("RenameLegacy(the form's legacy document, nil) = false, want true")
	}
	got, _ := json.Marshal(doc)
	if want := `{"spec":{"github":{"repositories":[]},"type":"github"}}`; string(got) != want {
		t.Errorf("RenameLegacy(the form's legacy document) = %s, want %s", got, want)
	}
}

// The rules of a type apply only to a task that names it, so a task with no type is told its type is
// missing, and not that its prompt and harness are not allowed.
func TestTypeRulesNeedTheType(t *testing.T) {
	v, _ := New()
	doc, node, _ := ParseYAML([]byte(strings.Replace(valid, "  type: general\n", "", 1)))
	issues := v.ValidateTask(doc, node)
	missing := false
	for _, is := range issues {
		if is.Key == "schema_false" {
			t.Errorf("a task with no type got %s at %s", is.Message, is.Path)
		}
		missing = missing || (is.Key == "schema_required" && strings.Contains(is.Message, `"type"`))
	}
	if !missing {
		t.Errorf("issues %+v", issues)
	}
}

// Every task has a type. A task with none is told so at its type, on the line of its spec, and an empty
// type is not one of the types.
func TestTaskNeedsAType(t *testing.T) {
	v, _ := New()
	doc, node, _ := ParseYAML([]byte(strings.Replace(valid, "  type: general\n", "", 1)))
	issues := v.ValidateTask(doc, node)
	i := slices.IndexFunc(issues, func(is Issue) bool {
		return is.Key == "schema_required" && is.Params["properties"] == `"type"`
	})
	if i < 0 || issues[i].Path != "/spec/type" || issues[i].Line != 6 {
		t.Errorf("a task with no type: got %+v, want schema_required naming \"type\" at /spec/type, on line 6", issues)
	}
	doc, node, _ = ParseYAML([]byte(strings.Replace(valid, "type: general", `type: ""`, 1)))
	issues = v.ValidateTask(doc, node)
	i = slices.IndexFunc(issues, func(is Issue) bool { return is.Key == "schema_enum" && is.Path == "/spec/type" })
	if i < 0 || issues[i].Line != 7 {
		t.Errorf("a task with an empty type: got %+v, want schema_enum at /spec/type, on line 7", issues)
	}
}

// The contracts name things one way. Property names are camelCase; the identifiers of
// named things, task types and harnesses, are kebab-case like any identifier; event types are object.action in
// snake_case, in the event schema and in the events a task is notified about; every other enum value is snake_case.
// Each name that breaks its rule is reported at its JSON pointer.
func TestNamingConventions(t *testing.T) {
	task, event := schemaJSON(t, contracts.TaskSchema), schemaJSON(t, contracts.EventSchema)
	identifier := regexp.MustCompile(schemaAt(task, "$defs", "identifier", "pattern").(string))
	for _, v := range namingViolations("task.schema.json", task, identifier, map[string]string{
		"/$defs/taskType": "identifier", "/$defs/harnessProvider": "identifier", "/$defs/notifiableEvent": "event",
	}) {
		t.Error(v)
	}
	for _, v := range namingViolations("event.schema.json", event, identifier, map[string]string{
		"/$defs/eventType": "event",
	}) {
		t.Error(v)
	}
}

// The test above fails on a key, an enum value, an identifier, or an event type that breaks its rule, naming each by
// its pointer; a default that holds the same value is no name and is left out, and so is a rule for an enum that is
// not there.
func TestNamingViolationsNameThePointer(t *testing.T) {
	raw := string(contracts.TaskSchema)
	for _, r := range [][2]string{
		{`"timeoutMinutes"`, `"timeout_minutes"`},
		{`"oldest_first"`, `"oldestFirst"`},
		{`"claude-code"`, `"claude_code"`},
		{`"execution.needs_attention"`, `"execution.needs-attention"`},
	} {
		if !strings.Contains(raw, r[0]) {
			t.Fatalf("task.schema.json has no %s to rename", r[0])
		}
		raw = strings.ReplaceAll(raw, r[0], r[1])
	}
	task := schemaJSON(t, []byte(raw))
	identifier := regexp.MustCompile(schemaAt(task, "$defs", "identifier", "pattern").(string))
	got := namingViolations("task.schema.json", task, identifier, map[string]string{
		"/$defs/taskType": "identifier", "/$defs/harnessProvider": "identifier", "/$defs/notifiableEvent": "event",
		"/$defs/gone": "identifier",
	})
	want := []string{
		`task.schema.json#/$defs/execution/properties/timeout_minutes: property "timeout_minutes" is not camelCase`,
		`task.schema.json#/$defs/github/properties/queue/properties/ordering/enum/0: "oldestFirst" is not snake_case`,
		`task.schema.json#/$defs/harnessProvider/enum/0: "claude_code" is not a kebab-case identifier`,
		`task.schema.json#/$defs/notifiableEvent/enum/4: "execution.needs-attention" is not an object.action event type in snake_case`,
		`task.schema.json#/$defs/gone: no enum to check as identifier`,
	}
	sort.Strings(got)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Errorf("violations:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// event.schema.json is the one list of event types. A task is notified only about event types
// it lists, and the daemon's Event constants are exactly that list, with EventSchemaVersion its schemaVersion.
func TestEventTypesHaveOneSource(t *testing.T) {
	task, event := schemaJSON(t, contracts.TaskSchema), schemaJSON(t, contracts.EventSchema)
	types := enumOf(t, event, "$defs", "eventType")
	for _, name := range enumOf(t, task, "$defs", "notifiableEvent") {
		if !slices.Contains(types, name) {
			t.Errorf("task.schema.json#/$defs/notifiableEvent: %q is not an eventType of event.schema.json", name)
		}
	}
	consts := eventConstants(t, filepath.Join("..", "..", "domain", "event.go"))
	byValue := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(consts)) {
		value := consts[name]
		if other, ok := byValue[value]; ok {
			t.Errorf("domain.%s and domain.%s are both %q", other, name, value)
		}
		byValue[value] = name
		if !slices.Contains(types, value) {
			t.Errorf("domain.%s = %q is not an eventType of event.schema.json", name, value)
		}
	}
	for _, ty := range types {
		if _, ok := byValue[ty]; !ok {
			t.Errorf("event.schema.json eventType %q has no Event constant in domain/event.go", ty)
		}
	}
	if got := schemaAt(event, "properties", "schemaVersion", "const"); got != domain.EventSchemaVersion {
		t.Errorf("event.schema.json schemaVersion is %v, domain.EventSchemaVersion %q", got, domain.EventSchemaVersion)
	}
}

// The effective definition the daemon returns, every default filled in, is a valid task: for
// one that sets every field of its type, beside a fallback and a repository that leave theirs to the defaults, and for
// one that sets only what it must, of a harness type and of the script type. The daemon reads the file into a
// TaskDocument and returns Effective() as JSON.
func TestEffectiveDocumentValidates(t *testing.T) {
	const full = `apiVersion: harmflow/v1alpha1
kind: Task
metadata:
  id: every-field
  name: Every field
  description: Sets every field a github task takes.
spec:
  enabled: false
  type: github
  prompt: Implement the oldest eligible issue.
  agents: [issue-reviewer]
  skills: [hf-github-issue-queue, hf-github-draft-pr]
  workspace:
    path: ~/work
  schedule:
    cron: "0 9 * * 1-5"
    timezone: Europe/Lisbon
    missedRun:
      policy: skip
  concurrency:
    overlappingRuns: skip
  harness:
    preferred:
      provider: claude-code
      model: sonnet
      effort: high
    fallback:
      on: [quota_exhausted]
      candidates:
        - provider: codex
          model: gpt-5
          effort: medium
        - provider: codex
      maxSwitches: 2
  permissions:
    profile: custom
    restrictToWorkspace: false
    network: deny
    custom:
      allowedTools: [Read]
      deniedTools: [Bash]
      writablePaths: [~/work/out]
  execution:
    timeoutMinutes: 90
    cancelGraceSeconds: 10
    rateLimit:
      maxRetries: 2
      initialWaitSeconds: 30
      maxWaitSeconds: 600
  notifications:
    native:
      enabled: false
      events: [execution.failed, pull_request.created]
  github:
    repositories:
      - name: octo/repo
        localPath: ~/work/repo
        baseBranch: develop
      - name: octo/other
        localPath: ~/work/other
    queue:
      ordering: newest_first
      maxDiscoveredPerCycle: 20
      maxItemsPerCycle: 2
      resumeStrategy: new_first
      maxResumeAttempts: 4
      maxFailedRetries: 5
    delivery:
      testRepairAttempts: 1
`
	const fullScript = `apiVersion: harmflow/v1alpha1
kind: Task
metadata:
  id: every-script-field
  name: Every script field
  description: Sets every field a script task takes.
spec:
  enabled: false
  type: script
  schedule:
    interval: 2h
    timezone: Europe/Lisbon
    missedRun:
      policy: skip
  concurrency:
    overlappingRuns: skip
  permissions:
    profile: custom
    restrictToWorkspace: false
    network: deny
    custom:
      allowedTools: [Read]
      deniedTools: [Bash]
      writablePaths: [~/work/out]
  execution:
    timeoutMinutes: 5
    cancelGraceSeconds: 10
    rateLimit:
      maxRetries: 2
      initialWaitSeconds: 30
      maxWaitSeconds: 600
  notifications:
    native:
      enabled: false
      events: [execution.failed]
  script:
    command: [/usr/bin/env, echo, hi]
    workingDirectory: ~/work
    environment:
      GREETING: hi
    systems: [macos, linux]
`
	const inlineScript = `apiVersion: harmflow/v1alpha1
kind: Task
metadata:
  id: inline-script
  name: Inline script
spec:
  type: script
  script:
    inline:
      interpreter: [/bin/sh, -e]
      source: echo hi
`
	v, _ := New()
	for name, text := range map[string]string{"every field": full, "the minimal task": valid,
		"every script field": fullScript, "the inline script": inlineScript} {
		doc, node, _ := ParseYAML([]byte(text))
		if issues := v.ValidateTask(doc, node); len(issues) > 0 {
			t.Fatalf("%s is not a valid task: %+v", name, issues)
		}
		var d domain.TaskDocument
		if err := yaml.Unmarshal([]byte(text), &d); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(d.Effective())
		if err != nil {
			t.Fatal(err)
		}
		effective, err := FromJSON(data)
		if err != nil {
			t.Fatal(err)
		}
		for _, is := range v.ValidateTask(effective, nil) {
			t.Errorf("the effective definition of %s: %s: %s", name, is.Path, is.Message)
		}
	}
}

func schemaJSON(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// schemaAt reads a nested key of a decoded schema, nil when one is missing.
func schemaAt(v any, keys ...string) any {
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

// enumOf returns the string values of the enum of the schema at keys.
func enumOf(t *testing.T, schema map[string]any, keys ...string) []string {
	t.Helper()
	values, ok := schemaAt(schema, append(keys, "enum")...).([]any)
	if !ok {
		t.Fatalf("no enum at /%s", strings.Join(keys, "/"))
	}
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, fmt.Sprint(v))
	}
	return out
}

// eventConstants reads the Event constants of a Go file, by name, with their values; EventSchemaVersion is the
// version of the event schema, not an event type.
func eventConstants(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, decl := range f.Decls {
		g, ok := decl.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		for _, spec := range g.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "Event") || name.Name == "EventSchemaVersion" {
					continue
				}
				var lit *ast.BasicLit
				if i < len(vs.Values) {
					lit, _ = vs.Values[i].(*ast.BasicLit)
				}
				if lit == nil || lit.Kind != token.STRING {
					t.Errorf("domain.%s is not a string literal", name.Name)
					continue
				}
				out[name.Name], _ = strconv.Unquote(lit.Value)
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("no Event constants in %s", path)
	}
	return out
}

var (
	camelCaseName = regexp.MustCompile(`^[a-z][a-z0-9]*([A-Z][a-z0-9]*)*$`)
	snakeCaseName = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	eventTypeName = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*\.[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
)

// namingViolations walks a schema and returns each name that breaks a naming rule, at its JSON pointer: a property
// name that is not camelCase, and an enum value that is not snake_case, or, for the enums kinds names by the pointer
// of their schema, not an identifier or not an event type. Keywords and the names of $defs are not document keys, and
// const, default, and examples hold values, not names, so the walk skips them.
func namingViolations(file string, schema map[string]any, identifier *regexp.Regexp, kinds map[string]string) []string {
	w := &namingWalker{file: file, identifier: identifier, kinds: kinds, seen: map[string]bool{}}
	w.schema("", schema)
	for _, ptr := range slices.Sorted(maps.Keys(kinds)) {
		if !w.seen[ptr] {
			w.report(ptr, "no enum to check as %s", kinds[ptr])
		}
	}
	return w.violations
}

type namingWalker struct {
	file       string
	identifier *regexp.Regexp
	kinds      map[string]string
	seen       map[string]bool
	violations []string
}

func (w *namingWalker) report(ptr, format string, args ...any) {
	w.violations = append(w.violations, w.file+"#"+ptr+": "+fmt.Sprintf(format, args...))
}

// schema walks one schema; true and false schemas hold no names.
func (w *namingWalker) schema(ptr string, s any) {
	m, ok := s.(map[string]any)
	if !ok {
		return
	}
	for _, key := range slices.Sorted(maps.Keys(m)) {
		at := ptr + "/" + pointerToken(key)
		switch key {
		case "properties", "$defs":
			sub, _ := m[key].(map[string]any)
			for _, name := range slices.Sorted(maps.Keys(sub)) {
				if key == "properties" && !camelCaseName.MatchString(name) {
					w.report(at+"/"+pointerToken(name), "property %q is not camelCase", name)
				}
				w.schema(at+"/"+pointerToken(name), sub[name])
			}
		case "enum":
			w.enum(ptr, m[key])
		case "const", "default", "examples":
		default:
			switch v := m[key].(type) {
			case map[string]any:
				w.schema(at, v)
			case []any:
				for i, x := range v {
					w.schema(at+"/"+strconv.Itoa(i), x)
				}
			}
		}
	}
}

// enum checks the values of the enum of the schema at ptr against the rule kinds gives it, snake_case by default.
func (w *namingWalker) enum(ptr string, values any) {
	kind := w.kinds[ptr]
	w.seen[ptr] = true
	list, _ := values.([]any)
	for i, v := range list {
		s, ok := v.(string)
		if !ok {
			continue
		}
		at := ptr + "/enum/" + strconv.Itoa(i)
		switch {
		case kind == "identifier" && !w.identifier.MatchString(s):
			w.report(at, "%q is not a kebab-case identifier", s)
		case kind == "event" && !eventTypeName.MatchString(s):
			w.report(at, "%q is not an object.action event type in snake_case", s)
		case kind == "" && !snakeCaseName.MatchString(s):
			w.report(at, "%q is not snake_case", s)
		}
	}
}

// pointerToken escapes a key for a JSON pointer (RFC 6901).
func pointerToken(key string) string {
	return strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
}

// Notifications cite the schema's event names.
func TestNotificationEventNames(t *testing.T) {
	v, _ := New()
	bad := valid + "  notifications:\n    native:\n      events: [needs-attention]\n"
	doc, node, _ := ParseYAML([]byte(bad))
	if issues := v.ValidateTask(doc, node); len(issues) == 0 {
		t.Fatal("needs-attention accepted")
	}
	ok := valid + "  notifications:\n    native:\n      events: [execution.needs_attention]\n"
	doc, node, _ = ParseYAML([]byte(ok))
	if issues := v.ValidateTask(doc, node); len(issues) > 0 {
		t.Fatalf("schema name rejected: %+v", issues)
	}
}

func TestSchedulesAreExclusive(t *testing.T) {
	v, _ := New()
	both := valid + "  schedule:\n    cron: \"0 9 * * *\"\n    interval: 30m\n"
	doc, node, _ := ParseYAML([]byte(both))
	if issues := v.ValidateTask(doc, node); len(issues) == 0 {
		t.Fatal("cron and interval together were accepted")
	}
}

// A cron is 5 fields or one of the seven macros, in lowercase; another macro, or one in another case, is refused at
// the cron.
func TestCronMacros(t *testing.T) {
	v, _ := New()
	withCron := func(cron string) string { return valid + "  schedule:\n    cron: \"" + cron + "\"\n" }
	for _, cron := range []string{"0 9 * * 1-5", "@yearly", "@annually", "@monthly", "@weekly", "@daily", "@midnight",
		"@hourly", " @daily "} {
		doc, node, _ := ParseYAML([]byte(withCron(cron)))
		if issues := v.ValidateTask(doc, node); len(issues) > 0 {
			t.Errorf("ValidateTask(cron %q) = %+v, want no issue", cron, issues)
		}
	}
	for _, cron := range []string{"@reboot", "@DAILY", "@Daily", "@daily *", "0 9 * *"} {
		doc, node, _ := ParseYAML([]byte(withCron(cron)))
		issues := v.ValidateTask(doc, node)
		if len(issues) != 1 || issues[0].Key != "schema_pattern" || issues[0].Path != "/spec/schedule/cron" {
			t.Errorf("ValidateTask(cron %q) = %+v, want schema_pattern at /spec/schedule/cron", cron, issues)
		}
	}
}

// A task's origin is an agent's name and a sha256 hash, both required; a script task, which has no prompt, has none.
func TestOrigin(t *testing.T) {
	v, _ := New()
	hash := "sha256:" + strings.Repeat("0a", 32)
	withOrigin := func(origin string) string { return valid + "  origin:\n" + origin }
	doc, node, _ := ParseYAML([]byte(withOrigin("    agent: reviewer\n    hash: " + hash + "\n")))
	if issues := v.ValidateTask(doc, node); len(issues) > 0 {
		t.Errorf("ValidateTask(origin) = %+v, want no issue", issues)
	}
	for _, c := range []struct {
		name, origin string
	}{
		{"no hash", "    agent: reviewer\n"},
		{"no agent", "    hash: " + hash + "\n"},
		{"another digest", "    agent: reviewer\n    hash: md5:0a0a\n"},
		{"an unknown key", "    agent: reviewer\n    hash: " + hash + "\n    path: /tmp/reviewer.md\n"},
		{"a name in caps", "    agent: Reviewer\n    hash: " + hash + "\n"},
		{"an upper digest", "    agent: reviewer\n    hash: sha256:" + strings.Repeat("0A", 32) + "\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc, node, _ := ParseYAML([]byte(withOrigin(c.origin)))
			if issues := v.ValidateTask(doc, node); len(issues) == 0 {
				t.Errorf("ValidateTask(origin with %s) = no issue, want one", c.name)
			}
		})
	}
	script := "apiVersion: harmflow/v1alpha1\nkind: Task\nmetadata:\n  id: audit\n  name: Audit\nspec:\n  type: script\n" +
		"  script:\n    command: [/bin/date]\n  origin:\n    agent: reviewer\n    hash: " + hash + "\n"
	doc, node, _ = ParseYAML([]byte(script))
	if issues := v.ValidateTask(doc, node); len(issues) == 0 {
		t.Error("ValidateTask(script task with an origin) = no issue, want one")
	}
}

// A name is an identifier as the task schema defines one: what a task can cite. A plugin's skill, a name in caps, and
// one too long are not.
func TestValidIdentifier(t *testing.T) {
	v, err := New()
	if err != nil {
		t.Fatalf("New() = %v, want no error", err)
	}
	for name, want := range map[string]bool{
		"my-skill": true, "hf-github-worktree": true, "imagegen": true, "a": true, strings.Repeat("a", 64): true,
		"pdf:pdf": false, "My-Skill": false, "-skill": false, "skill-": false, "": false, strings.Repeat("a", 65): false,
	} {
		if got := v.ValidIdentifier(name); got != want {
			t.Errorf("ValidIdentifier(%q) = %v, want %v", name, got, want)
		}
	}
}

// A skill name is an identifier, or a plugin's skill exactly as its harness reports it, `<plugin>:<skill>`, uppercase
// letters and inner whitespace included and each part at most 64 characters. Any other name is not: an identifier
// holds no whitespace, a part neither begins nor ends with it, and a name is never a path.
func TestValidSkillName(t *testing.T) {
	v, err := New()
	if err != nil {
		t.Fatalf("New() = %v, want no error", err)
	}
	long := strings.Repeat("a", 64)
	spaced := strings.Repeat("a", 31) + " " + strings.Repeat("a", 32)
	for name, want := range map[string]bool{
		"my-skill": true, "hf-github-worktree": true, "a": true, long: true,
		"pdf:pdf": true, "presentations:Presentations": true, "spreadsheets:excel-live-control": true,
		"documents:documents": true, long + ":" + long: true, "harmflow:hf-github-draft-pr": true,
		"pdf:my skill": true, "pdf:Fill Forms": true, "my plugin:x": true, "pdf:a\tb": true, spaced + ":" + spaced: true,
		"My-Skill": false, "-skill": false, "": false, strings.Repeat("a", 65): false, "my skill": false,
		"My Skill": false, "pdf:": false, ":pdf": false, ":": false, "a:b:c": false, "pdf:Fill Forms:x": false,
		"pdf :pdf": false, "pdf: pdf": false, " pdf:pdf": false, "pdf:pdf ": false, "x:\tpdf": false, "pdf: ": false,
		"x/y:z": false, "x:y/z": false, `x\y:z`: false, "x:..": true, "x:my/skill": false, `x:my\skill`: false,
		strings.Repeat("a", 65) + ":pdf": false, "pdf:" + strings.Repeat("a", 65): false, "pdf:" + spaced + "a": false,
	} {
		if got := v.ValidSkillName(name); got != want {
			t.Errorf("ValidSkillName(%q) = %v, want %v", name, got, want)
		}
	}
}

// A task cites a plugin's skill by its harness's name, but an agent still by an identifier only, and a skill name that
// is neither form is refused at the skill, once. A plugin's skill cited twice is refused at the list, as any skill is.
func TestTaskCitesAPluginSkill(t *testing.T) {
	v, err := New()
	if err != nil {
		t.Fatalf("New() = %v, want no error", err)
	}
	doc, node, _ := ParseYAML([]byte(valid +
		"  skills: [pdf:pdf, presentations:Presentations, \"pdf:Fill Forms\", release-notes]\n"))
	if issues := v.ValidateTask(doc, node); len(issues) > 0 {
		t.Errorf("ValidateTask(plugin skills) = %+v, want no issue", issues)
	}
	for _, c := range []struct {
		name, yaml, key, path string
	}{
		{"an agent in the plugin form", valid + "  agents: [pdf:pdf]\n", "schema_pattern", "/spec/agents/0"},
		{"a skill whose part begins with a space", valid + "  skills: [\"pdf: pdf\"]\n", "schema_pattern", "/spec/skills/0"},
		{"a skill with a space and no plugin", valid + "  skills: [\"my skill\"]\n", "schema_pattern", "/spec/skills/0"},
		{"a skill with two colons", valid + "  skills: [\"a:b:c\"]\n", "schema_pattern", "/spec/skills/0"},
		{"a skill in caps without a plugin", valid + "  skills: [My-Skill]\n", "schema_pattern", "/spec/skills/0"},
		{"a plugin skill twice", valid + "  skills: [pdf:pdf, pdf:pdf]\n", "schema_unique_items", "/spec/skills"},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc, node, _ := ParseYAML([]byte(c.yaml))
			issues := v.ValidateTask(doc, node)
			if len(issues) != 1 || issues[0].Key != c.key || issues[0].Path != c.path {
				t.Errorf("ValidateTask(%s) = %+v, want one %s at %s", c.name, issues, c.key, c.path)
			}
		})
	}
}

// A schema error carries a key and params the desktop translates, beside its en-US text.
func TestSchemaErrorsAreTranslatable(t *testing.T) {
	v, _ := New()
	cases := []struct {
		yaml, key, param, want string
	}{
		{valid + "  colour: red\n", "schema_additional_properties", "properties", `"colour"`},
		{strings.Replace(valid, "  prompt: Summarize the week.\n", "", 1), "schema_required", "properties", `"prompt"`},
		{strings.Replace(valid, "id: weekly-notes", "id: Weekly_Notes", 1), "schema_pattern", "value", `"Weekly_Notes"`},
		{valid + "  enabled: maybe\n", "schema_type", "got", "string"},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {

			doc, node, _ := ParseYAML([]byte(c.yaml))
			issues := v.ValidateTask(doc, node)
			if len(issues) == 0 {
				t.Errorf("ValidateTask(%q) = no issue, want %s", c.yaml, c.key)
				return
			}
			got := issues[0]
			if got.Key != c.key || got.Params[c.param] != c.want || !strings.Contains(got.Message, c.want) {
				t.Errorf("ValidateTask(%q)[0] = %+v, want key %s with %s %s", c.yaml, got, c.key, c.param, c.want)
			}
		})
	}
	_, _, issues := ParseYAML([]byte("kind: [unclosed\n"))
	if len(issues) != 1 || issues[0].Key != "yaml_syntax" || issues[0].Params["detail"] == "" {
		t.Errorf("syntax error: %+v", issues)
	}
}

// A pointer is located at the key that names its value, or at the item it names in a list. One the tree does not hold
// falls back to the deepest ancestor found: a key the mapping lacks, an index out of range or not a number, a step past
// a scalar. The empty pointer is the document itself, and with no tree there is no location.
func TestLocatePointer(t *testing.T) {
	text := valid + "  skills:\n    - release-notes\n"
	_, node, issues := ParseYAML([]byte(text))
	if len(issues) > 0 {
		t.Fatalf("ParseYAML(%q) issues = %+v, want none", text, issues)
	}
	for _, c := range []struct {
		name, pointer string
		line, column  int
	}{
		{"an item", "/spec/skills/0", 13, 7},
		{"a key", "/spec/harness", 9, 3},
		{"a key the mapping lacks", "/spec/colour", 6, 1},
		{"an index out of range", "/spec/skills/1", 12, 3},
		{"an index that is not a number", "/spec/skills/first", 12, 3},
		{"a step past a scalar", "/spec/type/general", 7, 3},
		{"the empty pointer", "", 1, 1},
		{"the root", "/", 1, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			line, column := LocatePointer(node, c.pointer)
			if line != c.line || column != c.column {
				t.Errorf("LocatePointer(%q) = %d, %d, want %d, %d", c.pointer, line, column, c.line, c.column)
			}
		})
	}
	if line, column := LocatePointer(nil, "/spec"); line != 0 || column != 0 {
		t.Errorf("LocatePointer(nil, %q) = %d, %d, want 0, 0", "/spec", line, column)
	}
}

func TestReportSchema(t *testing.T) {
	v, _ := New()
	good, _ := FromJSON([]byte(`{"outcome":"no_work","summary":"nothing eligible"}`))
	if issues := v.ValidateReport(good); len(issues) > 0 {
		t.Fatal(issues)
	}
	bad, _ := FromJSON([]byte(`{"outcome":"done"}`))
	if issues := v.ValidateReport(bad); len(issues) == 0 {
		t.Fatal("unknown outcome accepted")
	}
}

// An event follows the event model. A type the schema does not list, or a timestamp that is not a
// date-time, fails, and the issue names that field alone.
func TestEventSchema(t *testing.T) {
	v, _ := New()
	const event = `{"id":1784534400000000,"schemaVersion":"1","timestamp":"2026-10-20T08:00:00.123456-03:00",` +
		`"taskId":"notes","executionId":"exec-20261020-080000-a1b2c3","attempt":1,"harness":"claude-code",` +
		`"type":"execution.status_changed","severity":"info","message":"running","messageKey":"execution_status",` +
		`"params":{"status":"running"},"payload":{"from":"queued","status":"running"}}`
	good, _ := FromJSON([]byte(event))
	if issues := v.ValidateEvent(good); len(issues) > 0 {
		t.Fatalf("a valid event was rejected: %+v", issues)
	}
	for _, c := range []struct{ from, to, path, key string }{
		{`"type":"execution.status_changed"`, `"type":"execution.progressed"`, "/type", "schema_enum"},
		{`"timestamp":"2026-10-20T08:00:00.123456-03:00"`, `"timestamp":"yesterday"`, "/timestamp", ""},
	} {
		t.Run(c.path, func(t *testing.T) {

			doc, _ := FromJSON([]byte(strings.Replace(event, c.from, c.to, 1)))
			issues := v.ValidateEvent(doc)
			if len(issues) != 1 || issues[0].Path != c.path || issues[0].Key != c.key {
				t.Errorf("ValidateEvent(event with %s) = %+v, want one issue at %s with key %q", c.to, issues, c.path, c.key)
			}
		})
	}
}
