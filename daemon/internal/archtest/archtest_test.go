package archtest

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestImportRules checks every production file of the daemon against the rules. Each failure names the file, the
// import, and the rule it breaks; an allowlist entry that matches no import fails too, so the list only shrinks.
func TestImportRules(t *testing.T) {
	root := filepath.Join("..", "..")
	module, err := modulePath(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	vs, err := check(root, module, allowlist)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vs {
		t.Error(v.msg)
	}
}

// TestCheckFixtures runs the checker over the fixture module in testdata/tree, which breaks each rule once and keeps
// to it once.
func TestCheckFixtures(t *testing.T) {
	allowed := []allowance{
		{"internal/app/allowed.go", "internal/adapters/store", "an allowlisted violation is not reported"},
		{"internal/app/app.go", "internal/adapters/gone", "a stale entry is reported"},
	}
	vs, err := check(filepath.Join("testdata", "tree"), "example.com/fixture", allowed)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, v := range vs {
		got = append(got, v.file+" "+v.imp)
	}
	want := []string{
		"internal/adapters/store/store.go internal/app",
		"internal/app/app.go internal/adapters/gone",
		"internal/app/app.go internal/adapters/store",
		"internal/app/app.go os/exec",
		"internal/domain/domain.go example.org/vendor/lib",
		"internal/domain/domain.go internal/adapters/store",
		"internal/domain/domain.go os/exec",
		"internal/extra/extra.go ",
		"internal/ports/ports.go internal/adapters/store",
		"internal/ports/ports.go os/exec",
	}
	if !slices.Equal(got, want) {
		t.Errorf("violations:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// TestViolationNamesFileImportAndRule checks that a failure says where, what, and which rule.
func TestViolationNamesFileImportAndRule(t *testing.T) {
	vs, err := check(filepath.Join("testdata", "tree"), "example.com/fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vs {
		if v.file == "internal/app/app.go" && v.imp == "os/exec" {
			for _, part := range []string{"internal/app/app.go:5", `"os/exec"`, ruleApp} {
				if !strings.Contains(v.msg, part) {
					t.Errorf("%q does not name %q", v.msg, part)
				}
			}
			return
		}
	}
	t.Fatal("no violation for os/exec in internal/app/app.go")
}

func TestModulePath(t *testing.T) {
	gomod := filepath.Join(t.TempDir(), "go.mod")
	if err := os.WriteFile(gomod, []byte("// comment\nmodule example.com/m\n\ngo 1.24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := modulePath(gomod); err != nil || got != "example.com/m" {
		t.Errorf("modulePath = %q, %v; want example.com/m", got, err)
	}
}
