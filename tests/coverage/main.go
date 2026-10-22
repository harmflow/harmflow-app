// Command coverage reads the daemon's coverage profile — the unit and integration data make coverage-daemon merges —
// and prints, per Go package, the statements its tests reach (ADR-015). It never adds packages up: each is read on
// its own.
//
// Usage: go -C tests run ./coverage <profile>
package main

import (
	"bufio"
	"fmt"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
)

const module = "github.com/harmflow/harmflow-app/daemon/"

// Packages nobody writes by hand stay out: internal/contracts holds the schemas make generate copies from contracts/.
var excluded = map[string]bool{"internal/contracts": true}

type block struct {
	statements int
	covered    bool
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: coverage <profile>")
		os.Exit(2)
	}
	blocks, err := read(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "coverage:", err)
		os.Exit(1)
	}
	type tally struct{ covered, total int }
	packages := map[string]*tally{}
	for key, b := range blocks {
		pkg := strings.TrimPrefix(path.Dir(key[:strings.LastIndex(key, ":")]), module)
		if excluded[pkg] {
			continue
		}
		t := packages[pkg]
		if t == nil {
			t = &tally{}
			packages[pkg] = t
		}
		t.total += b.statements
		if b.covered {
			t.covered += b.statements
		}
	}
	names := make([]string, 0, len(packages))
	for name, t := range packages {
		if t.total > 0 {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	fmt.Println("| Package | Covered | Total | Ratio |")
	fmt.Println("|---|---:|---:|---:|")
	for _, name := range names {
		t := packages[name]
		fmt.Printf("| `%s` | %d | %d | %s%% |\n", name, t.covered, t.total, ratio(t.covered, t.total))
	}
}

// read returns the profile's blocks, keyed by file and range. A block listed more than once — by several test
// binaries, or by both data sets — counts once, as covered when any listing reached it.
func read(name string) (map[string]block, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	blocks := map[string]block{}
	scanner := bufio.NewScanner(f)
	for line := 1; scanner.Scan(); line++ {
		text := scanner.Text()
		if text == "" || strings.HasPrefix(text, "mode:") {
			continue
		}
		fields := strings.Fields(text)
		if len(fields) != 3 || !strings.Contains(fields[0], ":") {
			return nil, fmt.Errorf("%s:%d: not a profile line: %q", name, line, text)
		}
		statements, err1 := strconv.Atoi(fields[1])
		count, err2 := strconv.Atoi(fields[2])
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("%s:%d: not a profile line: %q", name, line, text)
		}
		b, seen := blocks[fields[0]]
		if seen && b.statements != statements {
			return nil, fmt.Errorf("%s:%d: block %s has %d statements, %d before", name, line, fields[0], statements, b.statements)
		}
		blocks[fields[0]] = block{statements: statements, covered: b.covered || count > 0}
	}
	return blocks, scanner.Err()
}

// ratio is covered over total as a percentage truncated, never rounded, to two decimals.
func ratio(covered, total int) string {
	hundredths := covered * 10000 / total
	return fmt.Sprintf("%d.%02d", hundredths/100, hundredths%100)
}
