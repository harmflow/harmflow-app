package domain

import (
	"errors"
	"reflect"
	"slices"
	"testing"
)

// releaseNotes, digest, and reviewer are the items of the tests below: two skills that declare the same name, and an
// agent.
func declaredItems() []ItemParameters {
	return []ItemParameters{
		{Kind: "agent", Name: "reviewer", Known: true, Declarations: []ParameterDeclaration{
			{Name: "strict", Type: ParamBoolean},
		}},
		{Kind: "skill", Name: "release-notes", Known: true, Declarations: []ParameterDeclaration{
			{Name: "owner", Type: ParamText, Required: true},
			{Name: "limit", Type: ParamNumber, Default: 3},
		}},
		{Kind: "skill", Name: "digest", Known: true, Declarations: []ParameterDeclaration{
			{Name: "owner", Type: ParamText},
		}},
	}
}

func problemKeys(problems []ParameterProblem) []string {
	var out []string
	for _, p := range problems {
		kind := "warning"
		if p.Error {
			kind = "error"
		}
		out = append(out, kind+" "+p.Text.Key+" "+p.Path)
	}
	return out
}

func TestResolveParametersKeepsEachItemsValues(t *testing.T) {
	values := &DeclaredParameters{
		Skills: map[string]map[string]any{"release-notes": {"owner": "ana"}, "digest": {"owner": "bia"}},
		Agents: map[string]map[string]any{"reviewer": {"strict": true}},
	}
	got, problems := ResolveParameters(values, declaredItems(), nil)
	if len(problems) > 0 {
		t.Errorf("ResolveParameters problems = %v, want none", problemKeys(problems))
	}
	want := DeclaredParameters{
		Skills: map[string]map[string]any{"release-notes": {"owner": "ana", "limit": 3}, "digest": {"owner": "bia"}},
		Agents: map[string]map[string]any{"reviewer": {"strict": true}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ResolveParameters = %v, want %v", got, want)
	}
}

func TestResolveParametersRefusesMissingAndIllTypedValues(t *testing.T) {
	for _, c := range []struct {
		name  string
		given map[string]any
		want  []string
	}{
		{"no owner", map[string]any{}, []string{"error parameter_required /spec/parameters/skills/release-notes/owner"}},
		{"a null owner", map[string]any{"owner": nil}, []string{"error parameter_required /spec/parameters/skills/release-notes/owner"}},
		{"a limit in words", map[string]any{"owner": "ana", "limit": "three"},
			[]string{"error parameter_wrong_type /spec/parameters/skills/release-notes/limit"}},
		{"an owner that is a number", map[string]any{"owner": 7},
			[]string{"error parameter_wrong_type /spec/parameters/skills/release-notes/owner"}},
		{"a limit from JSON", map[string]any{"owner": "ana", "limit": 4.0}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			values := &DeclaredParameters{Skills: map[string]map[string]any{"release-notes": c.given}}
			_, problems := ResolveParameters(values, declaredItems()[1:2], nil)
			if got := problemKeys(problems); !slices.Equal(got, c.want) {
				t.Errorf("ResolveParameters(%v) = %v, want %v", c.given, got, c.want)
			}
		})
	}
}

func TestParameterTypes(t *testing.T) {
	for _, c := range []struct {
		typ  string
		ok   []any
		fail []any
	}{
		{ParamText, []any{"", "ana"}, []any{1, true, []any{"a"}}},
		{ParamNumber, []any{1, int64(2), uint64(3), 4.5}, []any{"4", true}},
		{ParamBoolean, []any{true, false}, []any{"true", 1}},
		{ParamTextList, []any{[]any{}, []any{"a", "b"}, []string{"c"}}, []any{"a", []any{"a", 1}}},
		{ParamRepository, []any{"harmflow/harmflow-app", "a/b.c_d"}, []any{"harmflow", "a/b/c", "/b", 3}},
		{ParamObject, []any{map[string]any{}, "any value its schema allows"}, nil},
	} {
		d := ParameterDeclaration{Name: "p", Type: c.typ}
		for _, v := range c.ok {
			if !d.accepts(v) {
				t.Errorf("%s accepts(%#v) = false, want true", c.typ, v)
			}
		}
		for _, v := range c.fail {
			if d.accepts(v) {
				t.Errorf("%s accepts(%#v) = true, want false", c.typ, v)
			}
		}
	}
}

func TestResolveParametersChecksAnObjectAgainstItsSchema(t *testing.T) {
	schema := map[string]any{"type": "object"}
	items := []ItemParameters{{Kind: "skill", Name: "queues", Known: true, Declarations: []ParameterDeclaration{
		{Name: "repositories", Type: ParamObject, Schema: schema},
	}}}
	values := &DeclaredParameters{Skills: map[string]map[string]any{"queues": {"repositories": "harmflow-app"}}}
	var gotSchema, gotValue any
	_, problems := ResolveParameters(values, items, func(s, v any) ([]string, error) {
		gotSchema, gotValue = s, v
		return []string{"/: expected object, got string"}, nil
	})
	if !reflect.DeepEqual(gotSchema, schema) || gotValue != "harmflow-app" {
		t.Errorf("check(%v, %v), want the declared schema and the value", gotSchema, gotValue)
	}
	if got, want := problemKeys(problems), []string{"error parameter_schema_mismatch /spec/parameters/skills/queues/repositories"}; !slices.Equal(got, want) {
		t.Errorf("ResolveParameters problems = %v, want %v", got, want)
	}
	if problems[0].Text.Params["detail"] != "/: expected object, got string" {
		t.Errorf("detail = %q, want the check's problem", problems[0].Text.Params["detail"])
	}

	got, problems := ResolveParameters(values, items, func(any, any) ([]string, error) { return nil, errors.New("bad $ref") })
	if keys := problemKeys(problems); !slices.Equal(keys, []string{"warning parameter_schema_invalid /spec/parameters/skills/queues"}) {
		t.Errorf("ResolveParameters with a schema that cannot be used = %v, want one warning", keys)
	}
	if got.Skills["queues"]["repositories"] != "harmflow-app" {
		t.Errorf("ResolveParameters = %v, want the value passed unchecked", got)
	}
}

func TestResolveParametersWarnsOfValuesNoItemDeclares(t *testing.T) {
	values := &DeclaredParameters{
		Skills: map[string]map[string]any{"release-notes": {"owner": "ana", "since": "monday"}, "gone": {"x": 1}},
	}
	got, problems := ResolveParameters(values, declaredItems()[1:2], nil)
	want := []string{
		"warning parameter_undeclared /spec/parameters/skills/release-notes/since",
		"warning parameter_undeclared /spec/parameters/skills/gone/x",
	}
	if keys := problemKeys(problems); !slices.Equal(keys, want) {
		t.Errorf("ResolveParameters problems = %v, want %v", keys, want)
	}
	if _, ok := got.Skills["release-notes"]["since"]; ok {
		t.Errorf("ResolveParameters = %v, want since left out", got)
	}
	if _, ok := got.Skills["gone"]; ok {
		t.Errorf("ResolveParameters = %v, want the uncited skill left out", got)
	}
}

func TestResolveParametersPassesUnknownDeclarationsThrough(t *testing.T) {
	items := []ItemParameters{{Kind: "skill", Name: "pdf:pdf"}, {Kind: "skill", Name: "quiet:skill"}}
	values := &DeclaredParameters{Skills: map[string]map[string]any{"pdf:pdf": {"pages": 3}}}
	got, problems := ResolveParameters(values, items, nil)
	if keys := problemKeys(problems); !slices.Equal(keys, []string{"warning parameters_unverified /spec/parameters/skills/pdf:pdf"}) {
		t.Errorf("ResolveParameters problems = %v, want one unverified warning", keys)
	}
	if !reflect.DeepEqual(got.Skills, map[string]map[string]any{"pdf:pdf": {"pages": 3}}) {
		t.Errorf("ResolveParameters = %v, want the values as given", got.Skills)
	}
}

func TestResolveParametersIgnoresDeclarationsItCannotUse(t *testing.T) {
	items := []ItemParameters{{Kind: "skill", Name: "odd", Known: true, Declarations: []ParameterDeclaration{
		{Name: "when", Type: "date", Required: true},
		{Name: "config", Type: ParamObject, Required: true},
		{Type: ParamText, Required: true},
		{Name: "a/b", Type: ParamText, Required: true},
		{Name: "a/b", Type: ParamNumber},
	}}}
	_, problems := ResolveParameters(nil, items, nil)
	want := []string{
		"warning parameter_type_unknown /spec/parameters/skills/odd",
		"warning parameter_schema_missing /spec/parameters/skills/odd",
		"error parameter_required /spec/parameters/skills/odd/a~1b",
	}
	if keys := problemKeys(problems); !slices.Equal(keys, want) {
		t.Errorf("ResolveParameters problems = %v, want %v", keys, want)
	}
}

func TestDropUndeclared(t *testing.T) {
	values := &DeclaredParameters{
		Skills: map[string]map[string]any{
			"release-notes": {"owner": "ana", "since": "monday"}, "gone": {"x": 1}, "pdf:pdf": {"pages": 3},
		},
		Agents: map[string]map[string]any{"reviewer": {"loud": true}},
	}
	items := append(declaredItems(), ItemParameters{Kind: "skill", Name: "pdf:pdf"})
	if !DropUndeclared(values, items) {
		t.Fatal("DropUndeclared = false, want true")
	}
	want := &DeclaredParameters{Skills: map[string]map[string]any{"release-notes": {"owner": "ana"}, "pdf:pdf": {"pages": 3}}}
	if !reflect.DeepEqual(values, want) {
		t.Errorf("DropUndeclared left %v, want %v", values, want)
	}
	if DropUndeclared(values, items) {
		t.Error("DropUndeclared again = true, want false")
	}
	if DropUndeclared(nil, items) {
		t.Error("DropUndeclared(nil) = true, want false")
	}
}

func TestTaskCloneCopiesDeclaredParameters(t *testing.T) {
	d := TaskDocument{Spec: TaskSpec{Parameters: &DeclaredParameters{Skills: map[string]map[string]any{"s": {"a": 1}}}}}
	c := d.clone()
	c.Spec.Parameters.Skills["s"]["a"] = 2
	if d.Spec.Parameters.Skills["s"]["a"] != 1 {
		t.Errorf("clone shares its values with the original: %v", d.Spec.Parameters)
	}
}
