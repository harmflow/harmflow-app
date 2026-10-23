// Command coverage reads the daemon's coverage profile — the unit and integration data make coverage-daemon merges —
// and prints, per Go package, the statements its tests reach beside the package's floor (ADR-015). It fails when a
// package reads below its floor. It never adds packages up: each is read, and held, on its own.
//
// Usage: go -C tests run ./coverage [-floors <file>] <profile>
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
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

type tally struct{ covered, total int }

// hundredths is the ratio as a whole number of hundredths of a percent, truncated, never rounded.
func (t tally) hundredths() int { return t.covered * 10000 / t.total }

func main() {
	floorsFile := flag.String("floors", "", "file of per-package floors; without it nothing fails on a figure")
	flag.Usage = func() { fmt.Fprintln(os.Stderr, "usage: coverage [-floors <file>] <profile>") }
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	blocks, err := read(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "coverage:", err)
		os.Exit(1)
	}
	floors := map[string]int{}
	if *floorsFile != "" {
		if floors, err = readFloorsFile(*floorsFile); err != nil {
			fmt.Fprintln(os.Stderr, "coverage:", err)
			os.Exit(1)
		}
	}
	packages := tallies(blocks)
	printTable(os.Stdout, packages, floors)
	if violations := check(packages, floors); len(violations) > 0 {
		for _, v := range violations {
			fmt.Fprintln(os.Stderr, "coverage:", v)
		}
		os.Exit(1)
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

// tallies adds the blocks up per package, leaving out the excluded packages and those with no statements.
func tallies(blocks map[string]block) map[string]tally {
	packages := map[string]tally{}
	for key, b := range blocks {
		pkg := strings.TrimPrefix(path.Dir(key[:strings.LastIndex(key, ":")]), module)
		if excluded[pkg] {
			continue
		}
		t := packages[pkg]
		t.total += b.statements
		if b.covered {
			t.covered += b.statements
		}
		packages[pkg] = t
	}
	for pkg, t := range packages {
		if t.total == 0 {
			delete(packages, pkg)
		}
	}
	return packages
}

var floorLine = regexp.MustCompile(`^(\S+)\s+(\d{1,3})(?:\.(\d{1,2}))?$`)

func readFloorsFile(name string) (map[string]int, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	floors, err := readFloors(f)
	if err != nil {
		return nil, fmt.Errorf("%s:%w", name, err)
	}
	return floors, nil
}

// readFloors reads one "<package> <ratio>" line per package, the ratio a percentage with at most two decimals, into
// hundredths of a percent. Blank lines and lines starting with # are skipped.
func readFloors(r io.Reader) (map[string]int, error) {
	floors := map[string]int{}
	scanner := bufio.NewScanner(r)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		m := floorLine.FindStringSubmatch(text)
		if m == nil {
			return nil, fmt.Errorf("%d: not a floor line, want \"<package> <ratio>\": %q", line, text)
		}
		whole, _ := strconv.Atoi(m[2])
		decimals, _ := strconv.Atoi((m[3] + "00")[:2])
		floor := whole*100 + decimals
		if floor > 10000 {
			return nil, fmt.Errorf("%d: floor of %s is above 100%%: %s%%", line, m[1], percent(floor))
		}
		if _, seen := floors[m[1]]; seen {
			return nil, fmt.Errorf("%d: %s has a floor already", line, m[1])
		}
		floors[m[1]] = floor
	}
	return floors, scanner.Err()
}

// check returns one line per package that reads below its floor, naming the counter, the floor, and the ratio, and
// one per floor whose package the profile does not have. A package with no floor is held at 0.
func check(packages map[string]tally, floors map[string]int) []string {
	var violations []string
	for _, name := range sortedKeys(floors) {
		t, ok := packages[name]
		if !ok {
			violations = append(violations, fmt.Sprintf("%s has a floor but no statements in the profile: remove or rename its floor", name))
			continue
		}
		if t.hundredths() < floors[name] {
			violations = append(violations, fmt.Sprintf("%s: statements %s%% (%d of %d) is below its floor of %s%%",
				name, percent(t.hundredths()), t.covered, t.total, percent(floors[name])))
		}
	}
	return violations
}

func printTable(w io.Writer, packages map[string]tally, floors map[string]int) {
	fmt.Fprintln(w, "| Package | Covered | Total | Ratio | Floor |")
	fmt.Fprintln(w, "|---|---:|---:|---:|---:|")
	for _, name := range sortedKeys(packages) {
		t := packages[name]
		floor := "none, held at 0"
		if f, ok := floors[name]; ok {
			floor = percent(f) + "%"
		}
		fmt.Fprintf(w, "| `%s` | %d | %d | %s%% | %s |\n", name, t.covered, t.total, percent(t.hundredths()), floor)
	}
}

// percent prints hundredths of a percent with two decimals.
func percent(hundredths int) string { return fmt.Sprintf("%d.%02d", hundredths/100, hundredths%100) }

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
