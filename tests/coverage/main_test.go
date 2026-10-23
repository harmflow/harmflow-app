package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReadCountsABlockOnceCoveredWhenAnyListingReachedIt(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "daemon.out")
	err := os.WriteFile(profile, []byte(`mode: atomic
github.com/harmflow/harmflow-app/daemon/internal/app/a.go:1.1,2.2 3 0
github.com/harmflow/harmflow-app/daemon/internal/app/a.go:1.1,2.2 3 5
github.com/harmflow/harmflow-app/daemon/internal/app/a.go:3.1,4.2 2 0
github.com/harmflow/harmflow-app/daemon/internal/contracts/c.go:1.1,2.2 9 1
github.com/harmflow/harmflow-app/daemon/internal/ports/p.go:1.1,2.2 0 0
`), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := read(profile)
	if err != nil {
		t.Fatal(err)
	}
	got := tallies(blocks)
	want := map[string]tally{"internal/app": {covered: 3, total: 5}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tallies = %v, want %v", got, want)
	}
}

func TestRatioIsTruncatedNeverRounded(t *testing.T) {
	if got := percent(tally{covered: 2, total: 3}.hundredths()); got != "66.66" {
		t.Fatalf("2 of 3 = %s%%, want 66.66%%", got)
	}
}

func TestReadFloors(t *testing.T) {
	floors, err := readFloors(strings.NewReader("# a comment\n\ncmd/harmflowd 69.83\ninternal/app 78.2\ninternal/ports 75\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"cmd/harmflowd": 6983, "internal/app": 7820, "internal/ports": 7500}
	if !reflect.DeepEqual(floors, want) {
		t.Fatalf("floors = %v, want %v", floors, want)
	}
	for _, bad := range []string{"internal/app\n", "internal/app 78.213\n", "internal/app 100.01\n", "a 1\na 2\n"} {
		if _, err := readFloors(strings.NewReader(bad)); err == nil {
			t.Errorf("readFloors(%q) accepted it", bad)
		}
	}
}

func TestCheckNamesThePackageTheCounterTheFloorAndTheRatio(t *testing.T) {
	packages := map[string]tally{
		"internal/app":    {covered: 7811, total: 10000},
		"internal/domain": {covered: 1, total: 2},
		"internal/new":    {covered: 0, total: 10},
	}
	floors := map[string]int{"internal/app": 7812, "internal/domain": 5000, "internal/gone": 1000}
	got := check(packages, floors)
	want := []string{
		"internal/app: statements 78.11% (7811 of 10000) is below its floor of 78.12%",
		"internal/gone has a floor but no statements in the profile: remove or rename its floor",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("check = %q, want %q", got, want)
	}
}

func TestHoldFalseNamesAPackageBelowItsFloorAndFailsOnNoFigure(t *testing.T) {
	dir := t.TempDir()
	profile, floors := filepath.Join(dir, "daemon.out"), filepath.Join(dir, "floors.txt")
	if err := os.WriteFile(profile, []byte("mode: atomic\ngithub.com/harmflow/harmflow-app/daemon/internal/app/a.go:1.1,2.2 3 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(floors, []byte("internal/app 50\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const violation = "coverage: internal/app: statements 0.00% (0 of 3) is below its floor of 50.00%\n"
	for _, tc := range []struct {
		args []string
		want int
	}{
		{[]string{"-floors", floors, profile}, 1},
		{[]string{"-floors", floors, "-hold=false", profile}, 0},
	} {
		var stdout, stderr strings.Builder
		if got := run(tc.args, &stdout, &stderr); got != tc.want {
			t.Errorf("run(%q) = %d, want %d", tc.args, got, tc.want)
		}
		if !strings.Contains(stdout.String(), "| `internal/app` | 0 | 3 | 0.00% | 50.00% |") {
			t.Errorf("run(%q) printed no row beside the floor:\n%s", tc.args, stdout.String())
		}
		if stderr.String() != violation {
			t.Errorf("run(%q) wrote %q to stderr, want %q", tc.args, stderr.String(), violation)
		}
	}
	// A profile it cannot read still fails.
	var discard strings.Builder
	if got := run([]string{"-hold=false", filepath.Join(dir, "missing.out")}, &discard, &discard); got != 1 {
		t.Errorf("run with a missing profile = %d, want 1", got)
	}
}
