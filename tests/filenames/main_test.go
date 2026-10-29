package main

import (
	"errors"
	"io/fs"
	"slices"
	"testing"
)

// files is a stand-in for the tracked files' contents, read by check.
type files map[string]string

func (f files) read(p string) ([]byte, error) {
	content, ok := f[p]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return []byte(content), nil
}

func TestCheck(t *testing.T) {
	skill := func(name string) string { return "---\nname: " + name + "\ndescription: d\n---\nbody\n" }
	cases := []struct {
		name     string
		paths    []string
		contents files
		want     []string
	}{
		{
			name: "names that keep every rule",
			paths: []string{
				".github/workflows/ci.yml", ".cargo/config.toml", "CLAUDE.md", "docs/go-code-guide.md", "LICENSE", "Makefile",
				"README.md", "contracts/README.md", "contracts/schemas/execution-report.schema.json",
				"daemon/go.mod", "daemon/coverage-floors.txt", "daemon/cmd/harmflowd/main_test.go",
				"daemon/internal/domain/path_rule.go", "daemon/internal/adapters/harness/procutil/tree_darwin.go",
				"daemon/internal/archtest/testdata/tree/internal/Odd_Name/x.go", "tests/integration/testdata/fake-claude",
				"desktop/src-tauri/Cargo.toml", "desktop/src-tauri/build.rs", "desktop/src-tauri/src/sse.rs",
				"desktop/src-tauri/icons/128x128@2x.png", "desktop/src-tauri/icons/Square30x30Logo.png",
				"desktop/src-tauri/icons/source/app-icon.svg", "desktop/ui/vite.config.ts",
				"desktop/ui/src/App.tsx", "desktop/ui/src/App.test.tsx", "desktop/ui/src/main.tsx",
				"desktop/ui/src/vite-env.d.ts", "desktop/ui/src/design/app.css", "desktop/ui/src/i18n/pt-BR.ts",
				"desktop/ui/src/i18n/index.ts", "desktop/ui/src/i18n/i18n.test.ts", "desktop/ui/src/lib/daemonText.ts",
				"desktop/ui/src/lib/daemonText.test.ts", "desktop/ui/src/components/ui.tsx",
				"desktop/ui/src/screens/ImportDialog.tsx", "desktop/ui/src/screens/ImportDialog.test.tsx",
				"desktop/ui/src/screens/screens.test.tsx", "packaging/macos/update-app.sh",
				"packaging/macos/update-app-test.sh", "skills/hf-github-worktree/SKILL.md", "agents/hf-reviewer.md",
			},
			contents: files{
				"skills/hf-github-worktree/SKILL.md": skill("hf-github-worktree"),
				"agents/hf-reviewer.md":              skill(`"hf-reviewer"`),
			},
		},
		{
			name:  "a script's test with an underscore",
			paths: []string{"packaging/macos/update-app.sh", "packaging/macos/update-app_test.sh"},
			want:  []string{"packaging/macos/update-app_test.sh: " + ruleKebab},
		},
		{
			name:  "a script's test with no script beside it",
			paths: []string{"packaging/macos/foo-test.sh", "packaging/bar.sh"},
			want:  []string{"packaging/macos/foo-test.sh: " + ruleScriptTest},
		},
		{
			name:  "a Go folder with an underscore, named once for its two files",
			paths: []string{"daemon/internal/path_rule/rule.go", "daemon/internal/path_rule/rule_test.go"},
			want:  []string{"daemon/internal/path_rule/: " + ruleGoFolder},
		},
		{
			name:  "a Go file in camelCase",
			paths: []string{"tests/integration/queueRetry_test.go"},
			want:  []string{"tests/integration/queueRetry_test.go: " + ruleGoFile},
		},
		{
			name:  "a PascalCase interface file that is not .tsx",
			paths: []string{"desktop/ui/src/lib/Thing.ts"},
			want:  []string{"desktop/ui/src/lib/Thing.ts: " + ruleUIPascal},
		},
		{
			name:  "an interface file in snake_case",
			paths: []string{"desktop/ui/src/screens/task_list.tsx"},
			want:  []string{"desktop/ui/src/screens/task_list.tsx: " + ruleUIFile},
		},
		{
			name:  "an interface test with no file beside it, not named after its folder",
			paths: []string{"desktop/ui/src/screens/TasksScreen.tsx", "desktop/ui/src/screens/ImportDialog.test.tsx"},
			want:  []string{"desktop/ui/src/screens/ImportDialog.test.tsx: " + ruleUITest},
		},
		{
			name:  "an interface folder in PascalCase",
			paths: []string{"desktop/ui/src/Lib/format.ts"},
			want:  []string{"desktop/ui/src/Lib/: " + ruleUIFolder},
		},
		{
			name:  "a locale file outside i18n",
			paths: []string{"desktop/ui/src/lib/pt-BR.ts"},
			want:  []string{"desktop/ui/src/lib/pt-BR.ts: " + ruleUIFile},
		},
		{
			name:  "a Rust file in PascalCase",
			paths: []string{"desktop/src-tauri/src/Tray.rs"},
			want:  []string{"desktop/src-tauri/src/Tray.rs: " + ruleRust},
		},
		{
			name:  "a lower-case document at the root",
			paths: []string{"notes.md"},
			want:  []string{"notes.md: " + ruleRootMD},
		},
		{
			name:  "a document in a folder in upper case",
			paths: []string{"packaging/RELEASE-NOTES.md"},
			want:  []string{"packaging/RELEASE-NOTES.md: " + ruleKebab},
		},
		{
			name:  "a file in snake_case outside the Go and Rust parts",
			paths: []string{"contracts/schemas/execution_report.schema.json"},
			want:  []string{"contracts/schemas/execution_report.schema.json: " + ruleKebab},
		},
		{
			name:  "a folder in camelCase outside the Go and interface parts",
			paths: []string{"desktop/srcTauri/build.rs"},
			want:  []string{"desktop/srcTauri/: " + ruleKebab},
		},
		{
			name:     "a skill folder without the hf- prefix",
			paths:    []string{"skills/github-worktree/SKILL.md"},
			contents: files{"skills/github-worktree/SKILL.md": skill("github-worktree")},
			want:     []string{"skills/github-worktree/: " + ruleSkill},
		},
		{
			name:     "a skill that declares another name",
			paths:    []string{"skills/hf-github-worktree/SKILL.md"},
			contents: files{"skills/hf-github-worktree/SKILL.md": skill("hf-worktree")},
			want: []string{"skills/hf-github-worktree/SKILL.md: " +
				"declares name: hf-worktree; want name: hf-github-worktree, the name of its folder"},
		},
		{
			name:     "a skill with no name in its header",
			paths:    []string{"skills/hf-x/SKILL.md"},
			contents: files{"skills/hf-x/SKILL.md": "---\ndescription: d\n---\nname: hf-x\n"},
			want:     []string{"skills/hf-x/SKILL.md: declares no name; want name: hf-x"},
		},
		{
			name:     "an agent without the hf- prefix that declares another name",
			paths:    []string{"agents/reviewer.md"},
			contents: files{"agents/reviewer.md": skill("hf-reviewer")},
			want: []string{
				"agents/reviewer.md: " + ruleAgent,
				"agents/reviewer.md: declares name: hf-reviewer; want name: reviewer, the name of its file",
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := check(c.paths, c.contents.read)
			if err != nil {
				t.Fatalf("check(%q) error: %v", c.paths, err)
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("check(%q) = %q, want %q", c.paths, got, c.want)
			}
		})
	}
}

func TestCheckReportsAnUnreadableSkill(t *testing.T) {
	_, err := check([]string{"skills/hf-x/SKILL.md"}, files{}.read)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("check(an unreadable SKILL.md) error = %v, want one wrapping %v", err, fs.ErrNotExist)
	}
}
