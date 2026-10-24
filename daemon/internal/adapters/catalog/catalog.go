// Package catalog reads agents and skills in the HarmFlow format (D-092): a skill is a folder with a SKILL.md, an
// agent is a Markdown file with a front-matter header. Built-in ones ship with HarmFlow under the hf- prefix.
package catalog

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// FrontMatter is the header of an agent or a SKILL.md.
type FrontMatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Tools       any    `yaml:"tools"`
	Model       string `yaml:"model"`
}

// Parse splits a Markdown file into its front matter and body.
func Parse(content []byte) (FrontMatter, string) {
	var fm FrontMatter
	text := string(content)
	if !strings.HasPrefix(text, "---") {
		return fm, text
	}
	rest := text[3:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return fm, text
	}
	// A front matter that does not parse leaves fm empty: the item is still listed, with no description or tools.
	_ = yaml.Unmarshal([]byte(rest[:end]), &fm)
	body := strings.TrimPrefix(rest[end+4:], "\n")
	return fm, body
}

// ToolList returns the tools of a front matter as a list, from a list or a comma-separated string.
func (f FrontMatter) ToolList() []string {
	switch t := f.Tools.(type) {
	case string:
		var out []string
		for _, s := range strings.Split(t, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []any:
		var out []string
		for _, s := range t {
			if str, ok := s.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

// Dir lists agents (<dir>/agents/*.md) and skills (<dir>/skills/*/SKILL.md) of one root.
type Dir struct {
	Root    string
	Builtin bool
}

// Item looks up the agent or skill of that name under the root, its description the first line of its front matter's;
// false for another kind or when its file cannot be read.
func (d Dir) Item(kind, name string) (ports.ResolvedItem, bool) {
	var path, file string
	switch kind {
	case "agent":
		path = filepath.Join(d.Root, "agents", name+".md")
		file = path
	case "skill":
		path = filepath.Join(d.Root, "skills", name)
		file = filepath.Join(path, "SKILL.md")
	default:
		return ports.ResolvedItem{}, false
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return ports.ResolvedItem{}, false
	}
	fm, _ := Parse(data)
	return ports.ResolvedItem{Name: name, Kind: kind, Builtin: d.Builtin, Path: path, Description: firstLine(fm.Description)}, true
}

// Items lists the agents or skills under the root, sorted by name, leaving out any Item cannot read.
func (d Dir) Items(kind string) []ports.ResolvedItem {
	var names []string
	switch kind {
	case "agent":
		entries, _ := os.ReadDir(filepath.Join(d.Root, "agents")) // a missing or unreadable directory has no items
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				names = append(names, strings.TrimSuffix(e.Name(), ".md"))
			}
		}
	case "skill":
		entries, _ := os.ReadDir(filepath.Join(d.Root, "skills")) // a missing or unreadable directory has no items
		for _, e := range entries {
			if e.IsDir() || e.Type()&os.ModeSymlink != 0 {
				names = append(names, e.Name())
			}
		}
	}
	sort.Strings(names)
	var out []ports.ResolvedItem
	for _, n := range names {
		if it, ok := d.Item(kind, n); ok {
			out = append(out, it)
		}
	}
	return out
}

func firstLine(s string) string {
	sc := bufio.NewScanner(bytes.NewBufferString(strings.TrimSpace(s)))
	if sc.Scan() {
		return sc.Text()
	}
	return ""
}

// CopyTree copies a skill folder or an agent file into an execution's directory, so the built-ins are delivered
// per execution without touching the user's harness configuration (D-092).
func CopyTree(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyFile(src, dst, info.Mode())
	}
	return filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p) // the walk gives only paths under src, which Rel always relates
		target := filepath.Join(dst, rel)
		if fi.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		return copyFile(p, target, fi.Mode())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	return os.WriteFile(dst, data, mode.Perm()|0o600)
}
