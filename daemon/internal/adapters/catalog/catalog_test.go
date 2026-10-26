package catalog

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeAgent(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "reviewer.md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("os.WriteFile(%q) = %v, want no error", path, err)
	}
	return path
}

// An agent's instructions, description, model, and skills are what a task copies; every other front-matter field is
// named as left out, in the file's order.
func TestReadAgentCopy(t *testing.T) {
	path := writeAgent(t, "---\nname: reviewer\ndescription: Reviews a change\ntools: Read, Grep\nmodel: sonnet\n"+
		"color: blue\nskills: [review-notes, style]\npermissionMode: plan\n---\n\nYou review the change.\n")
	c, err := ReadAgentCopy(path)
	if err != nil {
		t.Fatalf("ReadAgentCopy(%q) = %v, want no error", path, err)
	}
	if c.Prompt != "You review the change." || c.Description != "Reviews a change" || c.Model != "sonnet" {
		t.Errorf("ReadAgentCopy(%q) = prompt %q, description %q, model %q, want %q, %q, %q", path, c.Prompt,
			c.Description, c.Model, "You review the change.", "Reviews a change", "sonnet")
	}
	if want := []string{"review-notes", "style"}; !slices.Equal(c.Skills, want) {
		t.Errorf("ReadAgentCopy(%q).Skills = %v, want %v", path, c.Skills, want)
	}
	if want := []string{"tools", "color", "permissionMode"}; !slices.Equal(c.Omitted, want) {
		t.Errorf("ReadAgentCopy(%q).Omitted = %v, want %v", path, c.Omitted, want)
	}
	if !strings.HasPrefix(c.Hash, "sha256:") || len(c.Hash) != len("sha256:")+64 {
		t.Errorf("ReadAgentCopy(%q).Hash = %q, want sha256:<64 hex digits>", path, c.Hash)
	}
}

// The hash follows what is copied: a field left out does not change it, and the instructions do.
func TestReadAgentCopyHash(t *testing.T) {
	hash := func(content string) string {
		t.Helper()
		path := writeAgent(t, content)
		c, err := ReadAgentCopy(path)
		if err != nil {
			t.Fatalf("ReadAgentCopy(%q) = %v, want no error", path, err)
		}
		return c.Hash
	}
	base := hash("---\nname: reviewer\ndescription: d\ncolor: blue\n---\nYou review.")
	if got := hash("---\nname: reviewer\ndescription: d\ncolor: red\n---\nYou review."); got != base {
		t.Errorf("ReadAgentCopy hash with color red = %s, want %s, the hash with color blue", got, base)
	}
	if got := hash("---\nname: reviewer\ndescription: d\ncolor: blue\n---\nYou review twice."); got == base {
		t.Errorf("ReadAgentCopy hash with changed instructions = %s, want a hash other than %s", got, base)
	}
}

// A file with no front matter is all instructions, and a comma-separated skills field reads as a list.
func TestReadAgentCopyForms(t *testing.T) {
	bare := writeAgent(t, "Only instructions.")
	c, err := ReadAgentCopy(bare)
	if err != nil {
		t.Fatalf("ReadAgentCopy(%q) = %v, want no error", bare, err)
	}
	if c.Prompt != "Only instructions." || len(c.Skills) != 0 || len(c.Omitted) != 0 {
		t.Errorf("ReadAgentCopy(%q) = %+v, want prompt %q with no skills and nothing omitted", bare, c,
			"Only instructions.")
	}
	listed := writeAgent(t, "---\nname: a\nskills: one, two\n---\nGo.")
	c, err = ReadAgentCopy(listed)
	if err != nil {
		t.Fatalf("ReadAgentCopy(%q) = %v, want no error", listed, err)
	}
	if want := []string{"one", "two"}; !slices.Equal(c.Skills, want) {
		t.Errorf("ReadAgentCopy(%q).Skills = %v, want %v", listed, c.Skills, want)
	}
	if _, err := ReadAgentCopy(filepath.Join(t.TempDir(), "missing.md")); err == nil {
		t.Error("ReadAgentCopy of a missing file = nil error, want one")
	}
}
