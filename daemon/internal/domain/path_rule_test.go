package domain

import (
	"os"
	"path/filepath"
	"testing"
)

// fixture lays out a home directory with a data directory under ~/Library, a hidden folder, a project folder, a
// mounted volume, and an allowed folder outside them all, each resolved as the rule sees it.
type fixture struct {
	root, home, data, volumes, allowed string
	rule                               PathRule
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{root: root, home: filepath.Join(root, "home"), volumes: filepath.Join(root, "Volumes"),
		allowed: filepath.Join(root, "srv", "repos")}
	f.data = filepath.Join(f.home, "Library", "Application Support", "HarmFlow")
	for _, d := range []string{f.data, filepath.Join(f.home, ".ssh"), filepath.Join(f.home, "Developer", "app"),
		filepath.Join(f.volumes, "Disk", "repos"), f.allowed, filepath.Join(root, "etc")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f.rule = NewPathRule(f.home, f.data, f.volumes, []string{f.allowed})
	return f
}

func (f fixture) check(t *testing.T, p string) error {
	t.Helper()
	real, err := ResolvePath(p)
	if err != nil {
		t.Fatalf("ResolvePath(%q): %v", p, err)
	}
	return f.rule.Check(real)
}

func keyOf(err error) string {
	if err == nil {
		return ""
	}
	return TextOf(err).Key
}

// The allowed area and the forbidden places, for paths that exist and paths not created yet.
func TestPathRuleAllowsAndForbids(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		name, path, key string
	}{
		{"a project folder inside home", filepath.Join(f.home, "Developer", "app"), ""},
		{"a folder not created yet inside home", filepath.Join(f.home, "Developer", "new", "deep"), ""},
		{"inside a mounted volume", filepath.Join(f.volumes, "Disk", "repos"), ""},
		{"inside an allowed folder", filepath.Join(f.allowed, "one"), ""},
		{"an allowed folder itself", f.allowed, ""},
		{"the home folder", f.home, "path_forbidden_home"},
		{"an ancestor of home", f.root, "path_forbidden_home"},
		{"the filesystem root", "/", "path_forbidden_home"},
		{"HarmFlow's data directory", f.data, "path_forbidden_place"},
		{"inside the data directory", filepath.Join(f.data, "tasks"), "path_forbidden_place"},
		{"a folder containing the data directory", filepath.Join(f.home, "Library", "Application Support"),
			"path_forbidden_place"},
		{"~/Library", filepath.Join(f.home, "Library"), "path_forbidden_place"},
		{"a hidden folder at the top of home", filepath.Join(f.home, ".ssh"), "path_forbidden_place"},
		{"inside a hidden folder", filepath.Join(f.home, ".config", "gh"), "path_forbidden_place"},
		{"a volume's root", filepath.Join(f.volumes, "Disk"), "path_outside_allowed_area"},
		{"outside every allowed place", filepath.Join(f.root, "etc"), "path_outside_allowed_area"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := keyOf(f.check(t, c.path)); got != c.key {
				t.Errorf("Check(%s) = %q, want %q", c.path, got, c.key)
			}
		})
	}
}

// Symlinks and directory traversal: a .. segment or a symlink that leads out is refused where it leads.
func TestPathRuleResolvesTraversalAndSymlinks(t *testing.T) {
	f := newFixture(t)
	out := filepath.Join(f.home, "Developer", "out")
	if err := os.Symlink(filepath.Join(f.root, "etc"), out); err != nil {
		t.Fatal(err)
	}
	lib := filepath.Join(f.home, "Developer", "lib")
	if err := os.Symlink(filepath.Join(f.home, "Library"), lib); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, path, key string
	}{
		// Written as strings, since filepath.Join would clean the .. away before the rule sees it.
		{"a .. segment that climbs into ~/Library", f.home + "/Developer/../Library/x", "path_forbidden_place"},
		{"a .. segment that stays inside", f.home + "/Developer/app/../app", ""},
		{"a symlink out of the allowed area", out, "path_outside_allowed_area"},
		{"a folder not created yet behind that symlink", out + "/new", "path_outside_allowed_area"},
		{"a symlink into ~/Library", lib + "/Application Support", "path_forbidden_place"},
		// The system applies .. after the symlink: out/../etc is <root>/etc, where cleaning first would read
		// ~/Developer/etc.
		{"a .. after a symlink follows the symlink", out + "/../etc", "path_outside_allowed_area"},
		{"a .. after a folder not created yet", f.home + "/Developer/new/../../.ssh", "path_forbidden_place"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := keyOf(f.check(t, c.path)); got != c.key {
				t.Errorf("Check(%s) = %q, want %q", c.path, got, c.key)
			}
		})
	}
}

func TestResolvePathNeedsAnAbsolutePath(t *testing.T) {
	if _, err := ResolvePath("repos/app"); keyOf(err) != "path_not_absolute" {
		t.Errorf("ResolvePath of a relative path: %v", err)
	}
}

// An allowed folder in Settings may be none of the forbidden places, and anything else.
func TestPathRuleForbiddenAppliesToAllowedFolders(t *testing.T) {
	f := newFixture(t)
	for p, key := range map[string]string{
		"/":                               "path_forbidden_home",
		f.home:                            "path_forbidden_home",
		filepath.Join(f.home, "Library"):  "path_forbidden_place",
		filepath.Join(f.home, ".codex"):   "path_forbidden_place",
		filepath.Join(f.root, "etc"):      "",
		filepath.Join(f.root, "srv", "x"): "",
	} {
		real, _ := ResolvePath(p)
		if got := keyOf(f.rule.Forbidden(real)); got != key {
			t.Errorf("Forbidden(%s) = %q, want %q", p, got, key)
		}
	}
}
