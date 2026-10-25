// Command filenames checks the name of every file and folder the repository tracks against the File names section of
// CLAUDE.md, and prints one line for each name that breaks a rule a pattern can test. The rules the section marks as
// held by review are not checked here.
//
// Usage: go -C tests run ./filenames <repository root>
package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// The patterns of the rules. Each is a fixed pattern, compiled once.
var (
	kebab      = regexp.MustCompile(`^\.?[a-z0-9]+(-[a-z0-9]+)*(\.[a-z0-9]+(-[a-z0-9]+)*)*$`)
	upperMD    = regexp.MustCompile(`^[A-Z][A-Z0-9]*(-[A-Z0-9]+)*\.md$`)
	goFolder   = regexp.MustCompile(`^[a-z0-9]+$`)
	goFile     = regexp.MustCompile(`^[a-z0-9]+(_[a-z0-9]+)*\.go$`)
	uiFolder   = regexp.MustCompile(`^[a-z][a-z0-9]*$`)
	uiSource   = regexp.MustCompile(`\.tsx?$`)
	uiTest     = regexp.MustCompile(`^(.+)\.test\.tsx?$`)
	pascal     = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*\.tsx?$`)
	camel      = regexp.MustCompile(`^[a-z][A-Za-z0-9]*\.tsx?$`)
	locale     = regexp.MustCompile(`^[a-z]{2}-[A-Z]{2}\.ts$`)
	rustModule = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*\.rs$`)
	builtin    = regexp.MustCompile(`^hf-[a-z0-9]+(-[a-z0-9]+)*$`)
)

// fixed holds the names a tool or a format sets, kept in any folder.
var fixed = map[string]bool{"Makefile": true, "LICENSE": true, "Cargo.toml": true, "Cargo.lock": true, "README.md": true, "SKILL.md": true}

// The rules, in the words of CLAUDE.md, as a fault names them.
const (
	ruleKebab      = "a name is lower case, words separated by hyphens, dots only before extensions"
	ruleRootMD     = "a document at the root is upper case, words separated by hyphens"
	ruleGoFolder   = "a Go folder is one lower-case word, as its package is"
	ruleGoFile     = "a Go file is lower case, words separated by _"
	ruleUIFolder   = "an interface folder is one lower-case word"
	ruleUIFile     = "an interface file is PascalCase .tsx for components, or camelCase for any other module"
	ruleUIPascal   = "a PascalCase interface file holds components, so it is .tsx"
	ruleUITest     = "an interface test is <name>.test.ts(x) beside <name>.ts(x), or named after its folder"
	ruleRust       = "a Rust file is lower case, words separated by _"
	ruleScriptTest = "a script's test is <script>-test.sh beside <script>.sh"
	ruleSkill      = "a built-in skill's folder is hf-<name>, lower case with hyphens"
	ruleAgent      = "a built-in agent is agents/hf-<name>.md, lower case with hyphens"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: filenames <repository root>")
		os.Exit(2)
	}
	root := os.Args[1]
	out, err := exec.Command("git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "filenames: git ls-files:", err)
		os.Exit(1)
	}
	var paths []string
	for p := range strings.SplitSeq(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	read := func(p string) ([]byte, error) { return os.ReadFile(filepath.Join(root, filepath.FromSlash(p))) }
	faults, err := check(paths, read)
	if err != nil {
		fmt.Fprintln(os.Stderr, "filenames:", err)
		os.Exit(1)
	}
	for _, f := range faults {
		fmt.Println(f)
	}
	if len(faults) > 0 {
		fmt.Fprintf(os.Stderr, "filenames: %d names break the File names rules of CLAUDE.md\n", len(faults))
		os.Exit(1)
	}
}

// check returns a line for each name among paths that breaks a rule, sorted, each folder named once however many
// paths it holds. paths are slash-separated and relative to the repository's root. read returns a tracked file's
// content, for the name a built-in skill or agent declares.
func check(paths []string, read func(string) ([]byte, error)) ([]string, error) {
	tracked := make(map[string]bool, len(paths))
	for _, p := range paths {
		tracked[p] = true
	}
	var faults []string
	seen := map[string]bool{}
	for _, p := range paths {
		parts := strings.Split(p, "/")
		// A testdata folder is checked, and what it holds is a fixture, named as what it stands in for.
		folders := parts[:len(parts)-1]
		inFixture := false
		if i := slices.Index(folders, "testdata"); i >= 0 {
			folders, inFixture = folders[:i+1], true
		}
		for i := range folders {
			dir := strings.Join(folders[:i+1], "/")
			if seen[dir] {
				continue
			}
			seen[dir] = true
			if rule := folderFault(folders[:i+1]); rule != "" {
				faults = append(faults, dir+"/: "+rule)
			}
		}
		if inFixture {
			continue
		}
		if rule := fileFault(parts, tracked); rule != "" {
			faults = append(faults, p+": "+rule)
		}
		fault, err := declaredName(parts, read)
		if err != nil {
			return nil, err
		}
		if fault != "" {
			faults = append(faults, p+": "+fault)
		}
	}
	slices.Sort(faults)
	return faults, nil
}

// folderFault returns the rule the folder dir breaks, or "" when it keeps them all. dir holds the folder's path
// elements from the root.
func folderFault(dir []string) string {
	name := dir[len(dir)-1]
	switch {
	case dir[0] == "daemon" || dir[0] == "tests":
		if !goFolder.MatchString(name) {
			return ruleGoFolder
		}
	case len(dir) > 3 && dir[0] == "desktop" && dir[1] == "ui" && dir[2] == "src":
		if !uiFolder.MatchString(name) {
			return ruleUIFolder
		}
	case len(dir) == 2 && dir[0] == "skills":
		if !builtin.MatchString(name) {
			return ruleSkill
		}
	default:
		if !kebab.MatchString(name) {
			return ruleKebab
		}
	}
	return ""
}

// fileFault returns the rule the file at parts breaks, or "" when it keeps them all. tracked tells which paths
// exist, for a test that must sit beside what it covers.
func fileFault(parts []string, tracked map[string]bool) string {
	p := strings.Join(parts, "/")
	dir, name := path.Split(p)
	switch {
	case fixed[name]:
		return ""
	case dir == "desktop/src-tauri/icons/":
		// tauri icon writes these, under the names Tauri's configuration lists.
		return ""
	case strings.HasSuffix(name, ".md"):
		return markdownFault(parts)
	case (parts[0] == "daemon" || parts[0] == "tests") && strings.HasSuffix(name, ".go"):
		if !goFile.MatchString(name) {
			return ruleGoFile
		}
	case strings.HasPrefix(dir, "desktop/ui/src/") && uiSource.MatchString(name):
		return interfaceFault(dir, name, tracked)
	case strings.HasPrefix(dir, "desktop/src-tauri/src/") && strings.HasSuffix(name, ".rs"):
		if !rustModule.MatchString(name) {
			return ruleRust
		}
	case !kebab.MatchString(name):
		return ruleKebab
	case strings.HasSuffix(name, "-test.sh"):
		if !tracked[dir+strings.TrimSuffix(name, "-test.sh")+".sh"] {
			return ruleScriptTest
		}
	}
	return ""
}

// markdownFault returns the rule the Markdown file at parts breaks, or "" when it keeps them all. README.md and
// SKILL.md are fixed names, checked before this.
func markdownFault(parts []string) string {
	name := parts[len(parts)-1]
	switch {
	case len(parts) == 1:
		if !upperMD.MatchString(name) {
			return ruleRootMD
		}
	case len(parts) == 2 && parts[0] == "agents":
		if !builtin.MatchString(strings.TrimSuffix(name, ".md")) {
			return ruleAgent
		}
	case !kebab.MatchString(name):
		return ruleKebab
	}
	return ""
}

// interfaceFault returns the rule the interface source name, in the folder dir, breaks, or "" when it keeps them
// all. dir ends with a slash.
func interfaceFault(dir, name string, tracked map[string]bool) string {
	folder := path.Base(dir)
	if m := uiTest.FindStringSubmatch(name); m != nil {
		stem := m[1]
		if tracked[dir+stem+".ts"] || tracked[dir+stem+".tsx"] || stem == folder {
			return ""
		}
		return ruleUITest
	}
	switch {
	case dir == "desktop/ui/src/" && name == "vite-env.d.ts":
		return ""
	case folder == "i18n" && locale.MatchString(name):
		return ""
	case camel.MatchString(name):
		return ""
	case pascal.MatchString(name):
		if !strings.HasSuffix(name, ".tsx") {
			return ruleUIPascal
		}
		return ""
	}
	return ruleUIFile
}

// declaredName returns how the name a built-in skill or agent declares differs from its folder or file, or "" when
// they match or parts is neither. It reads the file through read.
func declaredName(parts []string, read func(string) ([]byte, error)) (string, error) {
	var want, of string
	switch {
	case len(parts) == 3 && parts[0] == "skills" && parts[2] == "SKILL.md":
		want, of = parts[1], "folder"
	case len(parts) == 2 && parts[0] == "agents" && strings.HasSuffix(parts[1], ".md"):
		want, of = strings.TrimSuffix(parts[1], ".md"), "file"
	default:
		return "", nil
	}
	p := strings.Join(parts, "/")
	content, err := read(p)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", p, err)
	}
	got, ok := frontMatterName(content)
	switch {
	case !ok:
		return fmt.Sprintf("declares no name; want name: %s", want), nil
	case got != want:
		return fmt.Sprintf("declares name: %s; want name: %s, the name of its %s", got, want, of), nil
	}
	return "", nil
}

// frontMatterName returns the name: field of content's front-matter header, the lines between a first line of "---"
// and the next, and whether there is one.
func frontMatterName(content []byte) (string, bool) {
	lines := strings.Split(string(bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n"))), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", false
	}
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			break
		}
		if value, ok := strings.CutPrefix(line, "name:"); ok {
			return strings.Trim(strings.TrimSpace(value), `"'`), true
		}
	}
	return "", false
}
