package schema

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/harmflow/harmflow-app/daemon/internal/contracts"
)

// A task's spec.parameters keeps one map per cited skill and agent, by the name the task cites it by, a plugin's skill
// included; a key that is no skill or agent name, and a script task's parameters, are refused.
func TestDeclaredParametersInATask(t *testing.T) {
	v, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, spec, path string
	}{
		{"skills and agents", "  parameters:\n    skills:\n      release-notes: {owner: ana, limit: 3}\n      pdf:pdf: {pages: 2}\n" +
			"    agents:\n      reviewer: {strict: true}\n", ""},
		{"a skill name that is none", "  parameters:\n    skills:\n      Release Notes: {owner: ana}\n", "/spec/parameters/skills"},
		{"an agent name that is none", "  parameters:\n    agents:\n      pdf:pdf: {strict: true}\n", "/spec/parameters/agents"},
		{"values that are no map", "  parameters:\n    skills:\n      release-notes: [ana]\n", "/spec/parameters/skills/release-notes"},
		{"another key", "  parameters:\n    tools: {}\n", "/spec/parameters"},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc, node, issues := ParseYAML([]byte(valid + c.spec))
			if len(issues) > 0 {
				t.Fatal(issues)
			}
			issues = v.ValidateTask(doc, node)
			if c.path == "" {
				if len(issues) > 0 {
					t.Errorf("ValidateTask(%s) = %+v, want no issue", c.name, issues)
				}
				return
			}
			if !slices.ContainsFunc(issues, func(is Issue) bool { return is.Path == c.path }) {
				t.Errorf("ValidateTask(%s) = %+v, want an issue at %s", c.name, issues, c.path)
			}
		})
	}
}

// The names spec.parameters takes are the task schema's skill names and identifiers, which its patterns restate.
func TestDeclaredParametersNameTheCitedItems(t *testing.T) {
	var task struct {
		Defs map[string]struct {
			Pattern    string `json:"pattern"`
			Properties map[string]struct {
				PatternProperties map[string]any `json:"patternProperties"`
			} `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(contracts.TaskSchema, &task); err != nil {
		t.Fatal(err)
	}
	for kind, def := range map[string]string{"skills": "skillName", "agents": "identifier"} {
		got := slices.Collect(maps.Keys(task.Defs["declaredParameters"].Properties[kind].PatternProperties))
		if want := task.Defs[def].Pattern; len(got) != 1 || got[0] != want {
			t.Errorf("declaredParameters.%s patterns = %q, want the %s pattern %q", kind, got, def, want)
		}
	}
}

func TestCheckDeclared(t *testing.T) {
	v, err := New()
	if err != nil {
		t.Fatal(err)
	}
	var schema any
	// A schema as YAML decodes it from a front matter, with an integer where JSON would have a number.
	if err := yaml.Unmarshal([]byte("type: object\nrequired: [limit]\nproperties:\n  limit: {type: integer, maximum: 5}\n"), &schema); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		value any
		want  []string
	}{
		{"a value that matches", map[string]any{"limit": 3}, nil},
		{"a number from JSON", map[string]any{"limit": 3.0}, nil},
		{"a missing property", map[string]any{}, []string{"/limit: missing property"}},
		{"a value above the maximum", map[string]any{"limit": 9}, []string{"/limit: must be at most 5, got 9"}},
		{"a value of another type", "three", []string{"/: expected object, got string"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := v.CheckDeclared(schema, c.value)
			if err != nil {
				t.Fatalf("CheckDeclared(%v) error = %v, want none", c.value, err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("CheckDeclared(%v) = %q, want %q", c.value, got, c.want)
			}
			for i := range got {
				if got[i] == "" || c.want[i][:2] != got[i][:2] {
					t.Errorf("CheckDeclared(%v) = %q, want %q", c.value, got, c.want)
				}
			}
		})
	}
	if _, err := v.CheckDeclared(map[string]any{"type": 7}, "x"); err == nil {
		t.Error("CheckDeclared(a schema that does not compile) = no error, want one")
	}
}
