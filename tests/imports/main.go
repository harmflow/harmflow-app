// Command imports checks that the imports of every Go file under the given directories are in the three groups of the
// Go code guide: the standard library, then third-party modules, then the module's own packages, separated by
// blank lines. gofmt sorts imports within a group but neither splits nor orders groups, so CI runs this beside it.
//
// Usage: go -C tests run ./imports <dir>...
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// own is the prefix of the module's own packages, in every Go module of the repository.
const own = "github.com/harmflow/harmflow-app"

// group is one of the three import groups, in the order they appear.
type group int

const (
	standard group = iota
	thirdParty
	module
)

var groupNames = [...]string{standard: "standard-library", thirdParty: "third-party", module: "module"}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: imports <dir>...")
		os.Exit(2)
	}
	var faults []string
	for _, dir := range os.Args[1:] {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			found, err := check(path, src)
			if err != nil {
				return err
			}
			faults = append(faults, found...)
			return nil
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "imports:", err)
			os.Exit(1)
		}
	}
	for _, f := range faults {
		fmt.Println(f)
	}
	if len(faults) > 0 {
		fmt.Fprintf(os.Stderr, "imports: %d imports are outside the guide's three groups; goimports -local %s fixes most\n",
			len(faults), own)
		os.Exit(1)
	}
}

// classify names the group an import path belongs to: the standard library's paths have no dot in their first
// element, and the module's own start with its prefix.
func classify(path string) group {
	switch {
	case path == own || strings.HasPrefix(path, own+"/"):
		return module
	case !strings.Contains(strings.Split(path, "/")[0], "."):
		return standard
	}
	return thirdParty
}

// check returns a line for each import of src that sits outside its group, and for each group out of order.
func check(filename string, src []byte) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.ImportsOnly|parser.ParseComments)
	if err != nil {
		return nil, err
	}
	var faults []string
	fault := func(pos token.Pos, format string, args ...any) {
		faults = append(faults, fmt.Sprintf("%s: %s", fset.Position(pos), fmt.Sprintf(format, args...)))
	}
	// A blank line, or a new import declaration, starts a new run of imports. Each run must hold one group, and the
	// runs must follow the groups' order, each group in one run.
	type run struct {
		pos    token.Pos
		groups []group
		paths  []string
		starts []token.Pos
	}
	var runs []*run
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.IMPORT {
			continue
		}
		var cur *run
		lastLine := 0
		for _, spec := range gd.Specs {
			is := spec.(*ast.ImportSpec)
			start := is.Pos()
			if is.Doc != nil {
				start = is.Doc.Pos()
			}
			line := fset.Position(start).Line
			if cur == nil || line > lastLine+1 {
				cur = &run{pos: start}
				runs = append(runs, cur)
			}
			lastLine = fset.Position(is.End()).Line
			path, err := strconv.Unquote(is.Path.Value)
			if err != nil {
				return nil, err
			}
			cur.groups = append(cur.groups, classify(path))
			cur.paths = append(cur.paths, path)
			cur.starts = append(cur.starts, is.Pos())
		}
	}
	seen := map[group]bool{}
	last := group(-1)
	for _, r := range runs {
		// A run takes the group of its first import; any other import in it is out of place.
		g := r.groups[0]
		for i, other := range r.groups[1:] {
			if other != g {
				fault(r.starts[i+1], "%s is a %s import in the %s group; a blank line goes between groups",
					strconv.Quote(r.paths[i+1]), groupNames[other], groupNames[g])
			}
		}
		switch {
		case seen[g]:
			fault(r.pos, "a second %s group; each group appears once", groupNames[g])
		case g < last:
			fault(r.pos, "the %s group comes after the %s group; the order is standard library, third party, module",
				groupNames[g], groupNames[last])
		}
		seen[g] = true
		last = max(last, g)
	}
	return faults, nil
}
