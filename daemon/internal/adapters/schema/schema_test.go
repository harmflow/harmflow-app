package schema

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"path/filepath"
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

// FS-001 AC-009: a syntax error names the line.
func TestSyntaxErrorLine(t *testing.T) {
	_, _, issues := ParseYAML([]byte("apiVersion: harmflow/v1alpha1\nkind: Task\nmetadata:\n  id: [unclosed\n"))
	if len(issues) != 1 || issues[0].Line == 0 || issues[0].Code != "syntax" {
		t.Fatalf("got %+v", issues)
	}
}

// FS-001 AC-010, AC-013: a general task takes no repository; a GitHub task needs them; a schema error is located.
func TestTypeParametersAndLocation(t *testing.T) {
	v, _ := New()
	gh := strings.Replace(valid, "type: general", "type: github-issue-implementation", 1)
	doc, node, _ := ParseYAML([]byte(gh))
	issues := v.ValidateTask(doc, node)
	if len(issues) == 0 || !strings.Contains(issues[0].Message, "githubIssueImplementation") {
		t.Fatalf("a GitHub task without repositories passed: %+v", issues)
	}
	withRepo := valid + "  githubIssueImplementation:\n    repositories:\n      - name: o/r\n        localPath: /x\n"
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

// FS-013 FR-002: the rules of a type apply only to a task that names it, so a task with no type is told its type is
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

// FS-001 FR-008, AC-010: every task has a type. A task with none is told so at its spec, on that line, and an empty
// type is not one of the types.
func TestTaskNeedsAType(t *testing.T) {
	v, _ := New()
	doc, node, _ := ParseYAML([]byte(strings.Replace(valid, "  type: general\n", "", 1)))
	issues := v.ValidateTask(doc, node)
	i := slices.IndexFunc(issues, func(is Issue) bool {
		return is.Key == "schema_required" && is.Params["properties"] == `"type"`
	})
	if i < 0 || issues[i].Path != "/spec" || issues[i].Line != 6 {
		t.Errorf("a task with no type: got %+v, want schema_required naming \"type\" at /spec, on line 6", issues)
	}
	doc, node, _ = ParseYAML([]byte(strings.Replace(valid, "type: general", `type: ""`, 1)))
	issues = v.ValidateTask(doc, node)
	i = slices.IndexFunc(issues, func(is Issue) bool { return is.Key == "schema_enum" && is.Path == "/spec/type" })
	if i < 0 || issues[i].Line != 7 {
		t.Errorf("a task with an empty type: got %+v, want schema_enum at /spec/type, on line 7", issues)
	}
}

// FS-001 FR-019, AC-022 (D-094): the contracts name things one way. Property names are camelCase; the identifiers of
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
		`task.schema.json#/$defs/githubIssueImplementation/properties/queue/properties/ordering/enum/0: "oldestFirst" is not snake_case`,
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

// FS-001 FR-019, D-094: event.schema.json is the one list of event types. A task is notified only about event types
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

// FS-001 FR-012, FR-019: the effective definition the daemon returns, every default filled in, is a valid task: for
// one that sets every field of its type, beside a fallback and a repository that leave theirs to the defaults, and for
// one that sets only what it must. The daemon reads the file into a TaskDocument and returns Effective() as JSON.
func TestEffectiveDocumentValidates(t *testing.T) {
	const full = `apiVersion: harmflow/v1alpha1
kind: Task
metadata:
  id: every-field
  name: Every field
  description: Sets every field a github-issue-implementation task takes.
spec:
  enabled: false
  type: github-issue-implementation
  prompt: Implement the oldest eligible issue.
  agents: [harmflow-github-issue-implementer]
  skills: [harmflow-github-draft-pr]
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
  githubIssueImplementation:
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
	v, _ := New()
	for name, text := range map[string]string{"every field": full, "the minimal task": valid} {
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

// namingViolations walks a schema and returns each name that breaks FS-001 FR-019, at its JSON pointer: a property
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

// FS-001 AC-022: notifications cite the schema's event names.
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

// FS-012 FR-005: a schema error carries a key and params the desktop translates, beside its en-US text.
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
		doc, node, _ := ParseYAML([]byte(c.yaml))
		issues := v.ValidateTask(doc, node)
		if len(issues) == 0 {
			t.Errorf("%s: no issue", c.key)
			continue
		}
		got := issues[0]
		if got.Key != c.key || got.Params[c.param] != c.want || !strings.Contains(got.Message, c.want) {
			t.Errorf("%s: got %+v", c.key, got)
		}
	}
	_, _, issues := ParseYAML([]byte("kind: [unclosed\n"))
	if len(issues) != 1 || issues[0].Key != "yaml_syntax" || issues[0].Params["detail"] == "" {
		t.Errorf("syntax error: %+v", issues)
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

// FS-008 FR-007: an event follows the event model. A type the schema does not list, or a timestamp that is not a
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
		doc, _ := FromJSON([]byte(strings.Replace(event, c.from, c.to, 1)))
		issues := v.ValidateEvent(doc)
		if len(issues) != 1 || issues[0].Path != c.path || issues[0].Key != c.key {
			t.Errorf("%s: got %+v, want one issue at %s", c.to, issues, c.path)
		}
	}
}
