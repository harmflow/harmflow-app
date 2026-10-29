// Package templates holds the built-in task templates to their files: none of their identifiers, names, descriptions,
// or prompts is written in the daemon's or the desktop's source, which read every template from templates/.
package templates

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

// text is one thing a template says, and the regular expression that finds it as a whole in a source file.
type text struct {
	template, what, value string
	re                    *regexp.Regexp
}

// texts reads the identifier, the name and description in each language, and the prompt of each built-in template. An
// identifier is found whole between characters an identifier cannot hold; any other text, between characters that are
// no letter or digit.
func texts(t *testing.T) []text {
	t.Helper()
	dir := filepath.Join(repoRoot(), "templates")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []text
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Metadata struct {
				ID          string            `yaml:"id"`
				Name        map[string]string `yaml:"name"`
				Description map[string]string `yaml:"description"`
			} `yaml:"metadata"`
			Spec struct {
				Prompt string `yaml:"prompt"`
			} `yaml:"spec"`
		}
		if err := yaml.Unmarshal(data, &doc); err != nil || doc.Metadata.ID == "" {
			t.Fatalf("%s is no template with an identifier (%v)", e.Name(), err)
		}
		add := func(what, value, edge string) {
			if value = strings.TrimSpace(value); value != "" {
				re := regexp.MustCompile(`(^|[^` + edge + `])` + regexp.QuoteMeta(value) + `([^` + edge + `]|$)`)
				out = append(out, text{e.Name(), what, value, re})
			}
		}
		add("identifier", doc.Metadata.ID, `\p{L}\p{N}_-`)
		for lang, v := range doc.Metadata.Name {
			add("name in "+lang, v, `\p{L}\p{N}`)
		}
		for lang, v := range doc.Metadata.Description {
			add("description in "+lang, v, `\p{L}\p{N}`)
		}
		add("prompt", doc.Spec.Prompt, `\p{L}\p{N}`)
	}
	if len(out) == 0 {
		t.Fatal("templates/ holds no template")
	}
	return out
}

// source says whether path, relative to the repository's root, is source of the daemon or the desktop that the rule
// holds: tests, their fixtures, and the code generated from contracts/ are left out.
func source(path string) bool {
	switch {
	case strings.HasPrefix(path, "daemon/"):
		return strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") && !strings.Contains(path, "/testdata/")
	case strings.HasPrefix(path, "desktop/ui/src/"):
		return (strings.HasSuffix(path, ".ts") || strings.HasSuffix(path, ".tsx")) && !strings.Contains(path, ".test.") &&
			path != "desktop/ui/src/api/generated.ts"
	case strings.HasPrefix(path, "desktop/src-tauri/src/"):
		return strings.HasSuffix(path, ".rs")
	}
	return false
}

// No built-in template's identifier, name, description, or prompt is written in the daemon's or the desktop's source:
// the daemon serves every template from its file, and the desktop shows what the daemon serves.
func TestTemplateTextsStayInTheirFiles(t *testing.T) {
	want := texts(t)
	root := repoRoot()
	checked := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(strings.TrimPrefix(p, root+string(filepath.Separator)))
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "target", "target.noindex", "build", "dist", ".claude":
				return filepath.SkipDir
			}
			return nil
		}
		if !source(rel) {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		checked++
		for _, x := range want {
			if loc := x.re.FindIndex(data); loc != nil {
				line := 1 + strings.Count(string(data[:loc[0]]), "\n")
				t.Errorf("%s:%d writes the %s of %s, %q, which only that file holds", rel, line, x.what, x.template, x.value)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("no source file of the daemon or the desktop was read")
	}
}

// No built-in template's identifier is the name of a built-in agent or skill, so that a name says which it is, and the
// rule above tells a template's identifier from an agent's in the source.
func TestTemplateIdentifiersAreNoAgentOrSkill(t *testing.T) {
	root := repoRoot()
	builtins := map[string]string{}
	agents, err := os.ReadDir(filepath.Join(root, "agents"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range agents {
		builtins[strings.TrimSuffix(e.Name(), ".md")] = "agents/" + e.Name()
	}
	skills, err := os.ReadDir(filepath.Join(root, "skills"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range skills {
		if e.IsDir() {
			builtins[e.Name()] = "skills/" + e.Name()
		}
	}
	if len(builtins) == 0 {
		t.Fatal("agents/ and skills/ hold no built-in")
	}
	for _, x := range texts(t) {
		if x.what == "identifier" && builtins[x.value] != "" {
			t.Errorf("%s has the identifier %s, the name of %s", x.template, x.value, builtins[x.value])
		}
	}
}

// The rule finds a template's text written whole in a source file, and leaves out a longer word that starts with it, a
// longer identifier, a test, and the generated code.
func TestTheRuleFindsAWholeText(t *testing.T) {
	re := func(value, edge string) *regexp.Regexp {
		return regexp.MustCompile(`(^|[^` + edge + `])` + regexp.QuoteMeta(value) + `([^` + edge + `]|$)`)
	}
	id, name := re("hf-general", `\p{L}\p{N}_-`), re("General task", `\p{L}\p{N}`)
	for _, c := range []struct {
		re   *regexp.Regexp
		src  string
		want bool
	}{
		{id, `api.draftTask("hf-general", lang)`, true},
		{id, "`/templates/hf-general/draft`", true},
		{id, `"hf-general-purpose"`, false},
		{name, `title: "General task",`, true},
		{name, `"General tasks run anywhere"`, false},
		{name, `"A General taskforce"`, false},
	} {
		if got := c.re.MatchString(c.src); got != c.want {
			t.Errorf("%s in %s = %v, want %v", c.re, c.src, got, c.want)
		}
	}
	for path, want := range map[string]bool{
		"daemon/internal/app/templates.go":           true,
		"daemon/internal/app/templates_test.go":      false,
		"daemon/internal/app/testdata/fixture.go":    false,
		"desktop/ui/src/components/Templates.tsx":    true,
		"desktop/ui/src/App.test.tsx":                false,
		"desktop/ui/src/api/generated.ts":            false,
		"desktop/ui/src/i18n/pt-BR.ts":               true,
		"desktop/src-tauri/src/lib.rs":               true,
		"templates/hf-general.yaml":                  false,
		"tests/integration/templates_test.go":        false,
		"desktop/ui/src/design/app.css":              false,
		"daemon/internal/contracts/task.schema.json": false,
	} {
		if got := source(path); got != want {
			t.Errorf("source(%q) = %v, want %v", path, got, want)
		}
	}
}
