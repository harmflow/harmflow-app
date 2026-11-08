package catalog

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

// A skill declares its parameters in the front matter of its SKILL.md, and an agent in its file's; one that declares
// none has none.
func TestDeclarations(t *testing.T) {
	dir := t.TempDir()
	skill := filepath.Join(dir, "skills", "release-notes")
	if err := os.MkdirAll(skill, 0o700); err != nil {
		t.Fatal(err)
	}
	skillMD := "---\nname: release-notes\ndescription: Writes the notes\nparameters:\n" +
		"  - name: owner\n    type: text\n    description: Who owns the repository\n    required: true\n" +
		"  - name: limit\n    type: number\n    description: How many\n    default: 3\n" +
		"  - name: repositories\n    type: object\n    description: The repositories\n" +
		"    schema:\n      type: object\n      additionalProperties: {type: boolean}\n---\n\nWrite the notes.\n"
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte(skillMD), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Declarations("skill", skill)
	if err != nil {
		t.Fatalf("Declarations(skill) error = %v", err)
	}
	want := []domain.ParameterDeclaration{
		{Name: "owner", Type: "text", Description: "Who owns the repository", Required: true},
		{Name: "limit", Type: "number", Description: "How many", Default: 3},
		{Name: "repositories", Type: "object", Description: "The repositories",
			Schema: map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "boolean"}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Declarations(skill) = %#v, want %#v", got, want)
	}
	if it, ok := (Dir{Root: dir}).Item("skill", "release-notes"); !ok || it.Description != "Writes the notes" {
		t.Errorf("Item(release-notes) = %+v, %v, want its description read beside its parameters", it, ok)
	}

	agent := writeAgent(t, "---\nname: reviewer\nparameters:\n  - {name: strict, type: boolean, required: false}\n---\n\nReview.\n")
	if got, err := Declarations("agent", agent); err != nil || !reflect.DeepEqual(got, []domain.ParameterDeclaration{{Name: "strict", Type: "boolean"}}) {
		t.Errorf("Declarations(agent) = %#v, %v, want strict", got, err)
	}
	plain := writeAgent(t, "---\nname: reviewer\n---\n\nReview.\n")
	if got, err := Declarations("agent", plain); err != nil || got != nil {
		t.Errorf("Declarations(an agent that declares none) = %#v, %v, want none", got, err)
	}
	if _, err := Declarations("skill", filepath.Join(dir, "skills", "gone")); err == nil {
		t.Error("Declarations(a missing skill) = no error, want one")
	}
}
