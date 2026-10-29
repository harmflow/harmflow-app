// Package catalog reads agents and skills in the HarmFlow format: a skill is a folder with a SKILL.md, an
// agent is a Markdown file with a front-matter header. Built-in ones ship with HarmFlow under the hf- prefix.
package catalog

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	Skills      any    `yaml:"skills"`
}

// Parse splits a Markdown file into its front matter and body.
func Parse(content []byte) (FrontMatter, string) {
	var fm FrontMatter
	header, body, ok := split(string(content))
	if !ok {
		return fm, body
	}
	// A front matter that does not parse leaves fm empty: the item is still listed, with no description or tools.
	_ = yaml.Unmarshal([]byte(header), &fm)
	return fm, body
}

// split cuts a Markdown file into its front-matter header and its body; false, with the whole text as the body, when it
// has no front matter.
func split(text string) (header, body string, ok bool) {
	if !strings.HasPrefix(text, "---") {
		return "", text, false
	}
	rest := text[3:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", text, false
	}
	return rest[:end], strings.TrimPrefix(rest[end+4:], "\n"), true
}

// AgentCopy is what selecting an agent copies into a task: its instructions, description, model, and skills. Omitted
// names the front-matter fields a task has no place for, in the file's order, and Hash is a digest of what is copied,
// so a task can tell when the agent has changed since.
type AgentCopy struct {
	Prompt      string
	Description string
	Model       string
	Skills      []string
	Omitted     []string
	Hash        string
}

// copiedFields are the front-matter fields a task takes from an agent; the name only identifies the agent.
var copiedFields = map[string]bool{"name": true, "description": true, "model": true, "skills": true}

// ReadAgentCopy reads the agent file at path as a task copies it.
func ReadAgentCopy(path string) (AgentCopy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return AgentCopy{}, err
	}
	fm, body := Parse(data)
	c := AgentCopy{Prompt: strings.TrimSpace(body), Description: strings.TrimSpace(fm.Description), Model: fm.Model,
		Skills: listOf(fm.Skills), Omitted: []string{}}
	if c.Skills == nil {
		c.Skills = []string{}
	}
	if header, _, ok := split(string(data)); ok {
		var node yaml.Node
		// A header that does not parse copies nothing from it, so nothing is left out either.
		if yaml.Unmarshal([]byte(header), &node) == nil && len(node.Content) == 1 && node.Content[0].Kind == yaml.MappingNode {
			m := node.Content[0]
			for i := 0; i+1 < len(m.Content); i += 2 {
				if k := m.Content[i].Value; !copiedFields[k] {
					c.Omitted = append(c.Omitted, k)
				}
			}
		}
	}
	js, err := json.Marshal(struct {
		Prompt, Description, Model string
		Skills                     []string
	}{c.Prompt, c.Description, c.Model, c.Skills})
	if err != nil {
		return AgentCopy{}, err
	}
	sum := sha256.Sum256(js)
	c.Hash = "sha256:" + hex.EncodeToString(sum[:])
	return c, nil
}

// ToolList returns the tools of a front matter as a list, from a list or a comma-separated string.
func (f FrontMatter) ToolList() []string { return listOf(f.Tools) }

// listOf reads a front-matter field that holds names as a list, from a list or a comma-separated string.
func listOf(v any) []string {
	switch t := v.(type) {
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

// Dir lists agents (<dir>/agents/*.md), skills (<dir>/skills/*/SKILL.md), and task templates
// (<dir>/templates/*.yaml) of one root.
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

// Templates lists the task template files under the root, sorted by name: each with its content, or with the error
// that kept it from being read. A missing or unreadable directory has none.
func (d Dir) Templates() []ports.TemplateFile {
	dir := filepath.Join(d.Root, "templates")
	entries, _ := os.ReadDir(dir) // sorted by name; a missing or unreadable directory has no templates
	var out []ports.TemplateFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		f := ports.TemplateFile{Name: strings.TrimSuffix(e.Name(), ".yaml"), Path: filepath.Join(dir, e.Name())}
		f.Content, f.Err = os.ReadFile(f.Path)
		out = append(out, f)
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
// per execution without touching the user's harness configuration.
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
