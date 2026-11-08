package domain

import (
	"maps"
	"regexp"
	"slices"
	"strings"
)

// The types a skill or an agent may declare a parameter with.
const (
	ParamText       = "text"
	ParamNumber     = "number"
	ParamBoolean    = "boolean"
	ParamTextList   = "text_list"
	ParamRepository = "repository"
	ParamObject     = "object"
)

// ParamTypes are the types a declaration may name, in the order the task editor offers them.
var ParamTypes = []string{ParamText, ParamNumber, ParamBoolean, ParamTextList, ParamRepository, ParamObject}

// repositoryName is a GitHub repository's owner/name, the value of a repository parameter.
var repositoryName = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})/[A-Za-z0-9._-]{1,100}$`)

// ParameterDeclaration is one parameter a skill or an agent declares in the front matter of its file, which a task
// that cites it gives a value. An object parameter carries the JSON Schema its value must match.
type ParameterDeclaration struct {
	Name        string `yaml:"name" json:"name"`
	Type        string `yaml:"type" json:"type"`
	Description string `yaml:"description" json:"description,omitempty"`
	Default     any    `yaml:"default" json:"default,omitempty"`
	Required    bool   `yaml:"required" json:"required"`
	Schema      any    `yaml:"schema" json:"schema,omitempty"`
}

// DeclaredParameters are the values a task gives the parameters its skills and agents declare: one map per skill or
// agent, by the name the task cites it by, each map by the names its declaration gives. Skills and agents are kept
// apart, so that a skill and an agent of the same name never meet.
type DeclaredParameters struct {
	Skills map[string]map[string]any `yaml:"skills,omitempty" json:"skills,omitempty"`
	Agents map[string]map[string]any `yaml:"agents,omitempty" json:"agents,omitempty"`
}

// of is the map of the kind, "skill" or "agent".
func (p DeclaredParameters) of(kind string) map[string]map[string]any {
	if kind == "agent" {
		return p.Agents
	}
	return p.Skills
}

func (p *DeclaredParameters) clone() *DeclaredParameters {
	if p == nil {
		return nil
	}
	c := &DeclaredParameters{Skills: cloneValues(p.Skills), Agents: cloneValues(p.Agents)}
	return c
}

func cloneValues(m map[string]map[string]any) map[string]map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]map[string]any, len(m))
	for k, v := range m {
		out[k] = maps.Clone(v)
	}
	return out
}

// ItemParameters is what a cited skill or agent declares, as the harness that runs the task finds it. Known is false
// when the harness gives no file to read the declaration from, as for a plugin's skill: its values cannot be checked.
type ItemParameters struct {
	Kind         string
	Name         string
	Declarations []ParameterDeclaration
	Known        bool
}

// ParameterProblem is what checking a task's values against the declarations found: an error, which keeps the task
// from being saved or run, or a warning. Path is the JSON pointer of the task's field it is about.
type ParameterProblem struct {
	Path  string
	Text  Text
	Error bool
}

// SchemaCheck checks a value against the JSON Schema an object parameter declares: the problems it finds, in en-US,
// none for a value that matches, or an error when the schema itself cannot be used.
type SchemaCheck func(schema, value any) ([]string, error)

// ResolveParameters checks the values a task gives against what its cited skills and agents declare, and gives the
// values a session receives, in the task's shape: each value the task gives, and the default of each parameter it gives
// none. A required parameter with neither, and a value of the wrong type, are errors. A value no cited item declares,
// or that an item no longer cited holds, is a warning, and is left out. An item whose declaration is not Known passes
// its values as the task gives them, unchecked, with a warning; with no values it says nothing. A declaration HarmFlow
// cannot use is a warning, and the parameter is ignored.
func ResolveParameters(values *DeclaredParameters, items []ItemParameters, check SchemaCheck) (DeclaredParameters, []ParameterProblem) {
	out := DeclaredParameters{Skills: map[string]map[string]any{}, Agents: map[string]map[string]any{}}
	var problems []ParameterProblem
	given := DeclaredParameters{}
	if values != nil {
		given = *values
	}
	cited := map[string]bool{}
	for _, it := range items {
		cited[it.Kind+"\x00"+it.Name] = true
		have := given.of(it.Kind)[it.Name]
		base := "/spec/parameters/" + it.Kind + "s/" + pointerSegment(it.Name)
		if !it.Known {
			if len(have) > 0 {
				out.of(it.Kind)[it.Name] = maps.Clone(have)
				problems = append(problems, ParameterProblem{Path: base, Text: T("parameters_unverified",
					`the file of {{kind}} "{{item}}" cannot be read, so its parameter values are passed as the task gives them, unchecked`,
					"kind", it.Kind, "item", it.Name)})
			}
			continue
		}
		resolved := map[string]any{}
		declared := map[string]bool{}
		for _, d := range it.Declarations {
			if d.Name == "" || declared[d.Name] {
				continue
			}
			declared[d.Name] = true
			path := base + "/" + pointerSegment(d.Name)
			params := []string{"kind", it.Kind, "item", it.Name, "name", d.Name, "type", d.Type}
			if !slices.Contains(ParamTypes, d.Type) {
				problems = append(problems, ParameterProblem{Path: base, Text: T("parameter_type_unknown",
					`{{kind}} "{{item}}" declares its parameter "{{name}}" with the unknown type "{{type}}", so the parameter is ignored`,
					params...)})
				continue
			}
			if d.Type == ParamObject && d.Schema == nil {
				problems = append(problems, ParameterProblem{Path: base, Text: T("parameter_schema_missing",
					`{{kind}} "{{item}}" declares its object parameter "{{name}}" with no schema, so the parameter is ignored`,
					params...)})
				continue
			}
			v, ok := have[d.Name]
			if !ok || v == nil {
				switch {
				case d.Default != nil:
					resolved[d.Name] = d.Default
				case d.Required:
					problems = append(problems, ParameterProblem{Path: path, Error: true, Text: T("parameter_required",
						`{{kind}} "{{item}}" needs a value for its parameter "{{name}}"`, params...)})
				}
				continue
			}
			if !d.accepts(v) {
				problems = append(problems, ParameterProblem{Path: path, Error: true, Text: T("parameter_wrong_type",
					`the parameter "{{name}}" of {{kind}} "{{item}}" must be of type {{type}}`, params...)})
				continue
			}
			if d.Type == ParamObject && check != nil {
				found, err := check(d.Schema, v)
				if err != nil {
					problems = append(problems, ParameterProblem{Path: base, Text: T("parameter_schema_invalid",
						`the schema {{kind}} "{{item}}" declares for its parameter "{{name}}" cannot be used, so its value is not checked: {{detail}}`,
						append(params, "detail", err.Error())...)})
				} else if len(found) > 0 {
					problems = append(problems, ParameterProblem{Path: path, Error: true, Text: T("parameter_schema_mismatch",
						`the parameter "{{name}}" of {{kind}} "{{item}}" does not match its schema: {{detail}}`,
						append(params, "detail", strings.Join(found, "; "))...)})
					continue
				}
			}
			resolved[d.Name] = v
		}
		for _, name := range slices.Sorted(maps.Keys(have)) {
			if !declared[name] {
				problems = append(problems, undeclared(it.Kind, it.Name, name))
			}
		}
		out.of(it.Kind)[it.Name] = resolved
	}
	for _, kind := range []string{"agent", "skill"} {
		m := given.of(kind)
		for _, item := range slices.Sorted(maps.Keys(m)) {
			if cited[kind+"\x00"+item] {
				continue
			}
			for _, name := range slices.Sorted(maps.Keys(m[item])) {
				problems = append(problems, undeclared(kind, item, name))
			}
		}
	}
	return out, problems
}

// undeclared is the warning about a value no cited skill or agent declares.
func undeclared(kind, item, name string) ParameterProblem {
	return ParameterProblem{Path: "/spec/parameters/" + kind + "s/" + pointerSegment(item) + "/" + pointerSegment(name),
		Text: T("parameter_undeclared",
			`no cited {{kind}} "{{item}}" declares the parameter "{{name}}", so its value is left out of the session and dropped on the next save`,
			"kind", kind, "item", item, "name", name)}
}

// DropUndeclared removes from values each value no item declares, and the maps left empty, as a save does; an item
// whose declaration is not Known keeps its values. It reports whether it removed anything.
func DropUndeclared(values *DeclaredParameters, items []ItemParameters) bool {
	if values == nil {
		return false
	}
	known := map[string]map[string]bool{}
	for _, it := range items {
		set := map[string]bool{}
		if !it.Known {
			set = nil
		} else {
			for _, d := range it.Declarations {
				set[d.Name] = true
			}
		}
		known[it.Kind+"\x00"+it.Name] = set
	}
	dropped := false
	for _, kind := range []string{"agent", "skill"} {
		m := values.of(kind)
		for item, vals := range m {
			set, cited := known[kind+"\x00"+item]
			if cited && set == nil {
				continue
			}
			for name := range vals {
				if !set[name] {
					delete(vals, name)
					dropped = true
				}
			}
			if len(vals) == 0 {
				delete(m, item)
				dropped = true
			}
		}
	}
	if len(values.Skills) == 0 {
		values.Skills = nil
	}
	if len(values.Agents) == 0 {
		values.Agents = nil
	}
	return dropped
}

// Empty says whether no value is given, for a skill or an agent.
func (p *DeclaredParameters) Empty() bool {
	return p == nil || len(p.Skills) == 0 && len(p.Agents) == 0
}

// accepts says whether v is a value of the declaration's type, as YAML or JSON decodes it. An object parameter takes
// any value its schema allows, which the caller checks.
func (p ParameterDeclaration) accepts(v any) bool {
	switch p.Type {
	case ParamText:
		_, ok := v.(string)
		return ok
	case ParamNumber:
		switch v.(type) {
		case int, int64, uint64, float64:
			return true
		}
		return false
	case ParamBoolean:
		_, ok := v.(bool)
		return ok
	case ParamTextList:
		switch l := v.(type) {
		case []string:
			return true
		case []any:
			for _, e := range l {
				if _, ok := e.(string); !ok {
					return false
				}
			}
			return true
		}
		return false
	case ParamRepository:
		s, ok := v.(string)
		return ok && repositoryName.MatchString(s)
	case ParamObject:
		return true
	}
	return false
}

// pointerSegment escapes a name for one segment of a JSON pointer.
func pointerSegment(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}
