// Package archtest holds the daemon's import rules as a test, in the spirit of ArchUnit: the hexagonal layers and the
// daemon structure's constraints, checked over every production file of daemon/internal/ and daemon/cmd/. It has no
// production file, so it is a package no rule needs to cover.
package archtest

import (
	"bufio"
	"cmp"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const (
	ruleDomain    = "domain imports the standard library only, and never os/exec"
	rulePorts     = "ports imports only domain from daemon/internal/, and never os/exec"
	ruleApp       = "app imports only domain, ports, and contracts from daemon/internal/ (no adapters/...), and never os/exec"
	ruleContracts = "contracts imports nothing from daemon/internal/"
	ruleOutbound  = "an outbound adapter never imports app"
)

// rule is what the production files of one package, and of the packages under it, may import. An import is given
// relative to the module root when it is in the module, as "internal/domain", and as written otherwise.
type rule struct {
	scope  string
	text   string
	allows func(imp string, inModule bool) bool
}

// rules are tried in order; the first whose scope holds the package applies. A package no rule covers fails, so a new
// package gets its rule when it is added.
var rules = []rule{
	{"internal/domain", ruleDomain, func(imp string, in bool) bool { return !in && stdlib(imp) && imp != "os/exec" }},
	{"internal/ports", rulePorts, func(imp string, in bool) bool {
		return in && under(imp, "internal/domain") || !in && imp != "os/exec"
	}},
	{"internal/app", ruleApp, func(imp string, in bool) bool {
		return in && (under(imp, "internal/domain") || under(imp, "internal/ports") || under(imp, "internal/contracts")) ||
			!in && imp != "os/exec"
	}},
	{"internal/contracts", ruleContracts, func(_ string, in bool) bool { return !in }},
	// The inbound adapter and the composition root wire the use cases to everything else.
	{"internal/adapters/httpapi", "", func(string, bool) bool { return true }},
	{"internal/adapters", ruleOutbound, func(imp string, in bool) bool { return !in || !under(imp, "internal/app") }},
	{"cmd/harmflowd", "", func(string, bool) bool { return true }},
}

// allowance is a violation on record, with why it is there. Each entry is removed when its violation is resolved; an
// entry that matches no violating import fails the test.
type allowance struct{ file, imp, reason string }

// allowlist holds the violations on main when the import rules landed (harmflow/harmflow-app#27). How each one is
// resolved — moving a package, adding a port, or keeping a documented exception — is decided per entry.
var allowlist = []allowance{
	{"internal/app/app.go", "internal/adapters/schema", "App holds the task validator, a *schema.Validator"},
	{"internal/app/ended.go", "internal/adapters/harness/procutil", "logEnded reads procutil.Ended, the processes an execution's end stopped, and recovery ends what a crash left with procutil.EndStoredTree"},
	{"internal/app/executor.go", "internal/adapters/eventlog", "an attempt's input and the harness's events are redacted with eventlog.Redact and RedactFields"},
	{"internal/app/executor.go", "internal/adapters/harness/procutil", "the use case starts the harness with procutil.Start instead of through a port"},
	{"internal/app/executor.go", "internal/adapters/schema", "the agent's report is parsed with schema.FromJSON"},
	{"internal/app/harnesses.go", "internal/adapters/catalog", "the built-in agents and skills are resolved on disk with catalog.Dir"},
	{"internal/app/harnesses.go", "internal/adapters/harness/procutil", "a harness path is checked with procutil.Executable"},
	{"internal/app/harnesses.go", "internal/adapters/schema", "the harness checks report schema.Issue"},
	{"internal/app/ledger.go", "internal/adapters/eventlog", "the ledger's records are redacted with eventlog.RedactFields"},
	{"internal/app/maintenance.go", "os/exec", "git worktree remove and prune are run from a use case; worktree work has no port"},
	{"internal/app/script.go", "internal/adapters/eventlog", "a script's source, arguments, and environment are redacted with eventlog.Redact"},
	{"internal/app/script.go", "internal/adapters/harness/procutil", "a script's program is found, started, identified, and ended with procutil, not through a port"},
	{"internal/app/tasks.go", "internal/adapters/eventlog", "an exported task is redacted with eventlog.Redact"},
	{"internal/app/tasks.go", "internal/adapters/schema", "an imported script task's YAML is parsed again with schema.ParseYAML"},
	{"internal/app/validate.go", "internal/adapters/schema", "task and template validation return schema.Issue and use ParseYAML, FromJSON, and LocatePointer"},
}

// violation is one failure: an import against its package's rule, a stale allowlist entry, or a package no rule
// covers (imp empty). file and imp are relative to the module root, as in the allowlist.
type violation struct {
	file, imp string
	msg       string
}

// check parses the imports of every production .go file under root/internal and root/cmd, root being the directory of
// the module named module, and returns the violations not in allowed, then the entries of allowed that match none,
// sorted by file and import.
func check(root, module string, allowed []allowance) ([]violation, error) {
	used := make([]bool, len(allowed))
	uncovered := map[string]bool{}
	var vs []violation
	fset := token.NewFileSet()
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			name := d.Name()
			if d.IsDir() {
				// The go command ignores these directories too.
				if name == "testdata" || name != top && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			file, pkg := filepath.ToSlash(rel), filepath.ToSlash(filepath.Dir(rel))
			r, ok := ruleFor(pkg)
			if !ok {
				if !uncovered[pkg] {
					uncovered[pkg] = true
					vs = append(vs, violation{file: file,
						msg: fmt.Sprintf("%s: no rule covers package %s; add one to rules", file, pkg)})
				}
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, spec := range f.Imports {
				imp, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					return err
				}
				in := imp == module || strings.HasPrefix(imp, module+"/")
				if in {
					imp = strings.TrimPrefix(strings.TrimPrefix(imp, module), "/")
				}
				if r.allows(imp, in) {
					continue
				}
				if i := slices.IndexFunc(allowed, func(a allowance) bool { return a.file == file && a.imp == imp }); i >= 0 {
					used[i] = true
					continue
				}
				vs = append(vs, violation{file: file, imp: imp, msg: fmt.Sprintf("%s:%d imports %q, which breaks the rule: %s",
					file, fset.Position(spec.Pos()).Line, imp, r.text)})
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	for i, a := range allowed {
		if !used[i] {
			vs = append(vs, violation{file: a.file, imp: a.imp, msg: fmt.Sprintf(
				"allowlist entry %s → %q (%s) matches no violating import; remove it", a.file, a.imp, a.reason)})
		}
	}
	slices.SortFunc(vs, func(a, b violation) int { return cmp.Or(cmp.Compare(a.file, b.file), cmp.Compare(a.imp, b.imp)) })
	return vs, nil
}

func ruleFor(pkg string) (rule, bool) {
	for _, r := range rules {
		if under(pkg, r.scope) {
			return r, true
		}
	}
	return rule{}, false
}

// under reports whether path is pkg or a package below it.
func under(path, pkg string) bool { return path == pkg || strings.HasPrefix(path, pkg+"/") }

// stdlib reports whether imp is a standard-library package: its first element has no dot, as the go command decides.
func stdlib(imp string) bool {
	first, _, _ := strings.Cut(imp, "/")
	return !strings.Contains(first, ".")
}

// modulePath reads the module path from a go.mod file.
func modulePath(gomod string) (string, error) {
	f, err := os.Open(gomod)
	if err != nil {
		return "", err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(s.Text()), "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`), nil
		}
	}
	if err := s.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("%s: no module line", gomod)
}
