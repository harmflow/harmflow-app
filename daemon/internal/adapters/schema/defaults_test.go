package schema

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/harmflow/harmflow-app/daemon/internal/contracts"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

// githubTask sets no key the schema gives a default, and holds every object a default belongs to that the general task
// in valid lacks: a schedule, a fallback candidate, and a repository.
const githubTask = `apiVersion: harmflow/v1alpha1
kind: Task
metadata:
  id: implement-issues
  name: Implement issues
spec:
  type: github-issue-implementation
  prompt: Implement the oldest ready issue.
  schedule:
    cron: "0 9 * * 1-5"
  harness:
    preferred:
      provider: claude-code
    fallback:
      candidates:
        - provider: codex
  githubIssueImplementation:
    repositories:
      - name: octo/app
        localPath: /src/app
`

// The defaults task.schema.json documents are the ones the daemon applies. Every property with a default, found by
// following the schema's $refs, gets that value from TaskDocument.Effective when a task leaves it out, so a default
// changed on one side only fails here, naming its key.
func TestSchemaDefaultsAreTheDaemons(t *testing.T) {
	defaults := collectDefaults(t, contracts.TaskSchema)
	// The walk also reaches the defaults beyond the decided default values, through $refs and list items.
	for _, p := range []string{
		"/spec/enabled", "/spec/permissions/profile", "/spec/permissions/restrictToWorkspace", "/spec/permissions/network",
		"/spec/concurrency/overlappingRuns", "/spec/schedule/missedRun/policy", "/spec/harness/fallback/on",
		"/spec/notifications/native/events", "/spec/githubIssueImplementation/repositories/*/baseBranch",
		"/spec/harness/preferred/model", "/spec/harness/preferred/effort", "/spec/harness/fallback/candidates/*/model",
		"/spec/harness/fallback/candidates/*/effort",
	} {
		if !slices.ContainsFunc(defaults, func(d schemaDefault) bool { return d.pointer() == p }) {
			t.Errorf("the walk of task.schema.json missed the default at %s", p)
		}
	}
	v, err := New()
	if err != nil {
		t.Fatal(err)
	}
	checked := map[string]bool{}
	for _, task := range []string{valid, githubTask} {
		source, effective := taskDocuments(t, v, task)
		got, problems := checkDefaults(defaults, source, effective)
		for _, p := range problems {
			t.Errorf("%s task: %s", schemaAt(source, "spec", "type"), p)
		}
		maps.Copy(checked, got)
	}
	for _, d := range defaults {
		if !checked[d.pointer()] {
			t.Errorf("%s: neither task holds the object this default belongs to, so nothing shows Effective fills it in",
				d.pointer())
		}
	}

	t.Run("a default changed on one side fails, naming its key", func(t *testing.T) {
		source, effective := taskDocuments(t, v, githubTask)
		changed := slices.Clone(defaults)
		for i := range changed {
			if changed[i].pointer() == "/spec/permissions/network" {
				changed[i].value = domain.NetworkDeny
			}
		}
		if _, problems := checkDefaults(changed, source, effective); len(problems) != 1 ||
			!strings.HasPrefix(problems[0], "/spec/permissions/network:") {
			t.Errorf("a schema default changed alone: %q", problems)
		}
		candidate, _ := lookupPath(effective, []string{"spec", "harness", "fallback", "candidates", "0"})
		candidate.(map[string]any)["effort"] = "high"
		if _, problems := checkDefaults(defaults, source, effective); len(problems) != 1 ||
			!strings.HasPrefix(problems[0], "/spec/harness/fallback/candidates/0/effort:") {
			t.Errorf("an effective value changed alone: %q", problems)
		}
	})
}

// schemaDefault is a default a schema documents: the keys from the document's root to its property, "*" standing for
// every item of a list, and its value.
type schemaDefault struct {
	path  []string
	value any
}

func (d schemaDefault) pointer() string { return "/" + strings.Join(d.path, "/") }

// collectDefaults walks a schema from its root, following $ref, properties, items, and the subschemas a document must
// also match, and returns every property that has a default.
func collectDefaults(t *testing.T, raw []byte) []schemaDefault {
	t.Helper()
	root := schemaJSON(t, raw)
	defs, _ := root["$defs"].(map[string]any)
	var out []schemaDefault
	seen := map[string]bool{}
	var walk func(node any, path, refs []string)
	walk = func(node any, path, refs []string) {
		s, ok := node.(map[string]any)
		if !ok {
			return // a boolean schema holds no default
		}
		if value, ok := s["default"]; ok {
			d := schemaDefault{path: slices.Clone(path), value: value}
			if !seen[d.pointer()] {
				seen[d.pointer()] = true
				out = append(out, d)
			}
		}
		if ref, ok := s["$ref"].(string); ok {
			name, local := strings.CutPrefix(ref, "#/$defs/")
			if !local || defs[name] == nil {
				t.Fatalf("/%s: $ref %s names no definition of the schema", strings.Join(path, "/"), ref)
			}
			if !slices.Contains(refs, name) { // a definition met again on its own path adds nothing new
				walk(defs[name], path, append(slices.Clone(refs), name))
			}
		}
		props, _ := s["properties"].(map[string]any)
		for _, k := range slices.Sorted(maps.Keys(props)) {
			walk(props[k], append(slices.Clone(path), k), refs)
		}
		if items, ok := s["items"]; ok {
			walk(items, append(slices.Clone(path), "*"), refs)
		}
		for _, k := range []string{"allOf", "anyOf", "oneOf"} {
			subs, _ := s[k].([]any)
			for _, sub := range subs {
				walk(sub, path, refs)
			}
		}
		walk(s["then"], path, refs)
		walk(s["else"], path, refs)
	}
	walk(root, nil, nil)
	return out
}

// taskDocuments validates a task, then returns it as written and its effective definition, each decoded as the daemon
// reads it: the file through yaml.v3, Effective through encoding/json as the API sends it.
func taskDocuments(t *testing.T, v *Validator, text string) (source, effective any) {
	t.Helper()
	doc, node, issues := ParseYAML([]byte(text))
	if len(issues) == 0 {
		issues = v.ValidateTask(doc, node)
	}
	if len(issues) > 0 {
		t.Fatalf("the task is invalid: %+v", issues)
	}
	var task domain.TaskDocument
	if err := yaml.Unmarshal([]byte(text), &source); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal([]byte(text), &task); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(task.Effective())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &effective); err != nil {
		t.Fatal(err)
	}
	return source, effective
}

// checkDefaults compares each default with the effective document's value, wherever that document holds the object
// the default's key belongs to. The source document, as written, must leave the key out. It returns the defaults it
// checked, by pointer, and one problem per mismatch, each starting with the key's pointer.
func checkDefaults(defaults []schemaDefault, source, effective any) (checked map[string]bool, problems []string) {
	checked = map[string]bool{}
	for _, d := range defaults {
		parent, key := d.path[:len(d.path)-1], d.path[len(d.path)-1]
		for _, at := range objectsAt(effective, parent, nil) {
			path := append(slices.Clone(at), key)
			p := "/" + strings.Join(path, "/")
			checked[d.pointer()] = true
			if _, set := lookupPath(source, path); set {
				problems = append(problems, p+": the task sets it, so it cannot show the default")
				continue
			}
			got, ok := lookupPath(effective, path)
			if !ok || !reflect.DeepEqual(got, d.value) {
				problems = append(problems, fmt.Sprintf("%s: the schema documents %s, Effective gives %s", p,
					showJSON(d.value, true), showJSON(got, ok)))
			}
		}
	}
	return checked, problems
}

// objectsAt returns the concrete paths of the objects a path pattern reaches in v, one per list item where it says "*".
func objectsAt(v any, pattern, at []string) [][]string {
	if len(pattern) == 0 {
		if _, ok := v.(map[string]any); ok {
			return [][]string{at}
		}
		return nil
	}
	if pattern[0] == "*" {
		items, _ := v.([]any)
		var out [][]string
		for i, item := range items {
			out = append(out, objectsAt(item, pattern[1:], append(slices.Clone(at), strconv.Itoa(i)))...)
		}
		return out
	}
	m, _ := v.(map[string]any)
	child, ok := m[pattern[0]]
	if !ok {
		return nil
	}
	return objectsAt(child, pattern[1:], append(slices.Clone(at), pattern[0]))
}

// lookupPath reads the value at a concrete path of a decoded document, and whether it is there.
func lookupPath(v any, path []string) (any, bool) {
	for _, k := range path {
		switch c := v.(type) {
		case map[string]any:
			x, ok := c[k]
			if !ok {
				return nil, false
			}
			v = x
		case []any:
			i, err := strconv.Atoi(k)
			if err != nil || i < 0 || i >= len(c) {
				return nil, false
			}
			v = c[i]
		default:
			return nil, false
		}
	}
	return v, true
}

func showJSON(v any, present bool) string {
	if !present {
		return "nothing"
	}
	data, _ := json.Marshal(v)
	return string(data)
}
