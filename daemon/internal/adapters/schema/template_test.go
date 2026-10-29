package schema

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/harmflow/harmflow-app/daemon/internal/contracts"
)

const templateHead = `apiVersion: harmflow/v1alpha1
kind: TaskTemplate
metadata:
  id: hf-notes
  name:
    pt-BR: Notas
    en-US: Notes
    es-ES: Notas
  description:
    pt-BR: Resume.
    en-US: Sums up.
    es-ES: Resume.
spec:
`

// A template sets fields of a task with values valid for them, and may leave unset, at any level, a value a task
// requires; it never sets a task's enabled state or a copied agent's origin, nor both a prompt and a selected agent,
// nor what its type does not take.
func TestValidateTemplate(t *testing.T) {
	v, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, spec, key, path string
	}{
		{"a type alone", "  type: general\n", "", ""},
		{"a GitHub template with neither prompt nor repository name", "  type: github\n  selectedAgent: hf-github-issue-implementer\n" +
			"  harness:\n    preferred:\n      model: opus\n  github:\n    repositories:\n      - baseBranch: main\n", "", ""},
		{"a script template with no program", "  type: script\n  script: {}\n", "", ""},
		{"an inline script with no source", "  type: script\n  script:\n    inline:\n      interpreter: [/bin/sh]\n", "", ""},
		{"no type", "  prompt: Sum up.\n", "schema_required", "/spec/type"},
		{"an unknown type", "  type: email\n", "schema_enum", "/spec/type"},
		{"a prompt and a selected agent", "  type: general\n  prompt: Sum up.\n  selectedAgent: hf-reviewer\n", "schema_not", "/spec"},
		{"an enabled state", "  type: general\n  enabled: true\n", "schema_additional_properties", "/spec"},
		{"an origin", "  type: general\n  origin:\n    agent: hf-reviewer\n    hash: sha256:" + strings.Repeat("0", 64) + "\n",
			"schema_additional_properties", "/spec"},
		{"an empty prompt", "  type: general\n  prompt: \"\"\n", "schema_min_length", "/spec/prompt"},
		{"a repository name that is no owner/repo", "  type: github\n  github:\n    repositories:\n      - name: example\n",
			"schema_pattern", "/spec/github/repositories/0/name"},
		{"no repository", "  type: github\n  github:\n    repositories: []\n", "schema_min_items", "/spec/github/repositories"},
		{"a cron and an interval", "  type: general\n  schedule:\n    cron: \"0 9 * * *\"\n    interval: 1h\n", "schema_not", "/spec/schedule"},
		{"a command and an inline script", "  type: script\n  script:\n    command: [ls]\n    inline:\n      source: ls\n",
			"schema_not", "/spec/script"},
		{"a prompt on a script template", "  type: script\n  prompt: Sum up.\n", "schema_false", "/spec/prompt"},
		{"a selected agent on a script template", "  type: script\n  selectedAgent: hf-reviewer\n", "schema_false", "/spec/selectedAgent"},
		{"a script on a general template", "  type: general\n  script: {}\n", "schema_false", "/spec/script"},
		{"GitHub parameters on a general template", "  type: general\n  github: {}\n", "schema_not", "/spec"},
		{"a selected agent that is no identifier", "  type: general\n  selectedAgent: Reviewer\n", "schema_pattern", "/spec/selectedAgent"},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc, node, issues := ParseYAML([]byte(templateHead + c.spec))
			if len(issues) > 0 {
				t.Fatalf("ParseYAML = %+v", issues)
			}
			issues = v.ValidateTemplate(doc, node)
			if c.key == "" {
				if len(issues) > 0 {
					t.Errorf("ValidateTemplate(%s) = %+v, want no issue", c.name, issues)
				}
				return
			}
			if !slices.ContainsFunc(issues, func(is Issue) bool { return is.Key == c.key && is.Path == c.path && is.Line > 0 }) {
				t.Errorf("ValidateTemplate(%s) = %+v, want %s at %s, located", c.name, issues, c.key, c.path)
			}
		})
	}
	doc, node, _ := ParseYAML([]byte(strings.Replace(templateHead, "    es-ES: Notas\n", "", 1) + "  type: general\n"))
	if issues := v.ValidateTemplate(doc, node); len(issues) != 1 || issues[0].Path != "/metadata/name/es-ES" {
		t.Errorf("ValidateTemplate(a name with no es-ES) = %+v, want es-ES required", issues)
	}
	doc, node, _ = ParseYAML([]byte(strings.Replace(templateHead, "kind: TaskTemplate", "kind: Task", 1) + "  type: general\n"))
	if issues := v.ValidateTemplate(doc, node); len(issues) != 1 || issues[0].Key != "schema_const" || issues[0].Path != "/kind" {
		t.Errorf("ValidateTemplate(kind Task) = %+v, want TaskTemplate", issues)
	}
}

// The template schema restates only the task schema's definitions that require a value, without those requirements.
// Each restatement has the properties of the definition it stands for, the spec's without the enabled state and the
// origin and with the selected agent; each property is the task schema's own, or a restatement of it; and the
// restatement keeps every other keyword of the definition. A property the task schema gains fails this test until the
// template schema takes it too.
func TestTemplateSchemaRestatesTheTaskSchema(t *testing.T) {
	var task, template map[string]any
	if err := json.Unmarshal(contracts.TaskSchema, &task); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(contracts.TemplateSchema, &template); err != nil {
		t.Fatal(err)
	}
	const taskRef = "task.schema.json#" // a reference into the task schema, before its pointer
	at := func(root map[string]any, pointer string) map[string]any {
		var n any = root
		for _, seg := range strings.Split(strings.TrimPrefix(pointer, "#/"), "/") {
			n, _ = n.(map[string]any)[seg]
		}
		m, _ := n.(map[string]any)
		return m
	}
	// target is where a task schema node leads: the definition it refers to, or itself.
	target := func(pointer string) string {
		if ref, ok := at(task, pointer)["$ref"].(string); ok && strings.HasPrefix(ref, "#/") {
			return ref
		}
		return pointer
	}
	var check func(restated map[string]any, where, original string)
	check = func(restated map[string]any, where, original string) {
		orig := at(task, original)
		if orig == nil {
			t.Errorf("%s restates %s, which the task schema does not have", where, original)
			return
		}
		for k, want := range orig {
			switch k {
			case "required", "properties", "anyOf", "allOf", "description", "items":
				continue
			}
			if got := restated[k]; !reflect.DeepEqual(got, want) {
				t.Errorf("%s: %s = %v, want the task schema's %v", where, k, got, want)
			}
		}
		props, _ := restated["properties"].(map[string]any)
		origProps, _ := orig["properties"].(map[string]any)
		want := slices.Sorted(maps.Keys(origProps))
		if original == "#/$defs/spec" {
			want = slices.DeleteFunc(want, func(k string) bool { return k == "enabled" || k == "origin" })
			want = append(want, "selectedAgent")
			slices.Sort(want)
		}
		if got := slices.Sorted(maps.Keys(props)); !slices.Equal(got, want) {
			t.Errorf("%s has the properties %v, want %v", where, got, want)
		}
		for name, p := range props {
			if original == "#/$defs/spec" && name == "selectedAgent" {
				continue
			}
			check1(t, p, where+"/properties/"+name, original+"/properties/"+name, taskRef, template, target, check)
		}
		if items, ok := restated["items"].(map[string]any); ok {
			check1(t, items, where+"/items", original+"/items", taskRef, template, target, check)
		}
	}
	check(at(template, "#/$defs/spec"), "template#/$defs/spec", "#/$defs/spec")
}

// check1 checks one property of a restatement: a reference to the task schema's own property, or to the definition
// it refers to, or a restatement of it, local or inline.
func check1(t *testing.T, p any, where, original, taskRef string, template map[string]any, target func(string) string,
	check func(map[string]any, string, string)) {
	t.Helper()
	node, _ := p.(map[string]any)
	ref, _ := node["$ref"].(string)
	switch {
	case strings.HasPrefix(ref, taskRef):
		if got := "#" + strings.TrimPrefix(ref, taskRef); got != original && got != target(original) {
			t.Errorf("%s refers to the task schema's %s, want %s", where, got, original)
		}
	case strings.HasPrefix(ref, "#/$defs/"):
		var n any = template
		for _, seg := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			n = n.(map[string]any)[seg]
		}
		check(n.(map[string]any), "template"+ref, target(original))
	case node["properties"] != nil || node["items"] != nil:
		check(node, where, target(original))
	default:
		t.Errorf("%s is neither the task schema's %s nor a restatement of it", where, original)
	}
}
