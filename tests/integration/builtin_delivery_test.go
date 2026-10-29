package integration

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A task that cites a built-in skill runs once on Claude Code and once on Codex, each with a user's configuration that
// already holds files. Each harness is handed the skill as a copy in the execution's directory, equal to the built-in's
// source, and nothing it receives names the daemon's built-ins directory. Every file of both configurations keeps its
// bytes, from before the daemon starts to after it stops, and none is added or removed.
//
// A sign-in check of a real CLI would write into the configuration too, so the daemon never runs one: its first
// detection checks the stand-ins, set by hand before it starts.
func TestBuiltinSkillReachesEachHarnessWithoutChangingItsConfiguration(t *testing.T) {
	t.Parallel()
	const skill = "hf-github-draft-pr"
	home := t.TempDir()
	// The user's configuration of each harness is the test's own, as in TestFormDocumentIsTheFile: Claude Code's in
	// CLAUDE_CONFIG_DIR, Codex's in the daemon's CODEX_HOME, <home>/codex.
	claudeConfig, codexHome := filepath.Join(home, "claude"), filepath.Join(home, "codex")
	for file, content := range map[string]string{
		"claude/settings.json":                "{\n  \"theme\": \"dark\",\n  \"cleanupPeriodDays\": 30\n}\n",
		"claude/agents/tester.md":             "---\nname: tester\ndescription: Tries a change.\n---\nTry the change.\n",
		"claude/skills/review-notes/SKILL.md": "---\nname: review-notes\ndescription: Writes review notes.\n---\nWrite the review notes.\n",
		"codex/config.toml":                   "model_reasoning_effort = \"medium\"\n\n[history]\npersistence = \"none\"\n",
		"codex/agents/tester.md":              "---\nname: tester\ndescription: Tries a change.\n---\nTry the change.\n",
		"codex/skills/review-notes/SKILL.md":  "---\nname: review-notes\ndescription: Writes review notes.\n---\nWrite the review notes.\n",
	} {
		path := filepath.Join(home, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// The paths set by hand, which the daemon's first detection checks instead of a Claude Code or Codex the machine has
	// installed.
	standIns := map[string]string{
		"claude-code": recordingStandIn(t, "fake-claude", "claude"),
		"codex":       recordingStandIn(t, "fake-codex", "codex"),
	}
	manual, err := json.Marshal(standIns)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "state", "harnesses.json"), manual, 0o600); err != nil {
		t.Fatal(err)
	}
	before := configEntries(t, home, claudeConfig, codexHome)
	source, err := os.ReadFile(filepath.Join(repoRoot(), "skills", skill, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	// The daemon runs with --builtins naming the repository's root.
	builtins := repoRoot() + string(filepath.Separator)

	d := startDaemon(t, home, "CLAUDE_CONFIG_DIR="+claudeConfig)
	// startDaemon pointed the daemon at the stand-ins that record nothing.
	for id, path := range standIns {
		d.call("PUT", "/harnesses/"+id, map[string]any{"path": path}, 200)
	}
	cases := []struct {
		harness string
		// handed is the folder of the skill the harness was handed in the execution x, which its arguments or its
		// instructions name.
		handed func(t *testing.T, x map[string]any) string
	}{
		{harness: "claude-code", handed: func(t *testing.T, x map[string]any) string {
			args := recordedArgs(t, x)
			i := slices.Index(args, "--plugin-dir")
			if i < 0 || i+1 == len(args) {
				t.Fatalf("completedRun(claude-code) args = %q, want a --plugin-dir", args)
			}
			if dir := x["executionDir"].(string); !strings.HasPrefix(args[i+1], dir+string(filepath.Separator)) {
				t.Errorf("completedRun(claude-code) --plugin-dir = %s, want a folder inside the execution's directory %s",
					args[i+1], dir)
			}
			return filepath.Join(args[i+1], "skills", skill)
		}},
		{harness: "codex", handed: func(t *testing.T, x map[string]any) string {
			dir := filepath.Join(x["executionDir"].(string), "codex-builtins", "skills", skill)
			if stdin := recorded(t, x, "stdin"); !strings.Contains(stdin, filepath.Join(dir, "SKILL.md")) {
				t.Errorf("completedRun(codex) instructions = %q, want them to name %s", stdin, filepath.Join(dir, "SKILL.md"))
			}
			return dir
		}},
	}
	// The subtests run one after the other, so both executions have ended when the configurations are compared.
	for _, c := range cases {
		t.Run(c.harness, func(t *testing.T) {
			d := d.in(t)

			d.call("POST", "/tasks", map[string]any{"document": map[string]any{"apiVersion": "harmflow/v1alpha1",
				"kind": "Task", "metadata": map[string]any{"id": c.harness, "name": "Task " + c.harness},
				"spec": map[string]any{
					"type":    "general",
					"prompt":  "Draft the pull request.",
					"skills":  []any{skill},
					"harness": map[string]any{"preferred": map[string]any{"provider": c.harness}},
				}}}, 201)
			x := d.completedRun(c.harness)
			dir := c.handed(t, x)
			if got, err := os.ReadFile(filepath.Join(dir, "SKILL.md")); err != nil {
				t.Errorf("completedRun(%s) left no copy of the built-in skill %s: %v", c.harness, skill, err)
			} else if !bytes.Equal(got, source) {
				t.Errorf("completedRun(%s) copy of %s =\n%s\nwant the built-in's source\n%s", c.harness, skill, got, source)
			}
			for _, a := range append(recordedArgs(t, x), recorded(t, x, "stdin")) {
				if strings.Contains(a, builtins) {
					t.Errorf("completedRun(%s) handed the harness %q, which names a path in the built-ins directory %s",
						c.harness, a, builtins)
				}
			}
		})
	}

	// A write when the daemon stops is caught too.
	d.stop()
	after := configEntries(t, home, claudeConfig, codexHome)
	for path, data := range before {
		switch got, ok := after[path]; {
		case !ok:
			t.Errorf("after both executions, the user's configuration lost %s", path)
		case got != data:
			t.Errorf("after both executions, the user's %s =\n%s\nwant it unchanged\n%s", path, got, data)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			t.Errorf("after both executions, the user's configuration gained %s", path)
		}
	}
}

// configEntries is every entry under the folders, by its path relative to home: a folder under its path and a trailing
// slash, with no content, and any other entry with its bytes.
func configEntries(t *testing.T, home string, dirs ...string) map[string]string {
	t.Helper()
	entries := map[string]string{}
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(home, path)
			if err != nil {
				return err
			}
			if e.IsDir() {
				entries[filepath.ToSlash(rel)+"/"] = ""
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			entries[filepath.ToSlash(rel)] = string(data)
			return nil
		})
		if err != nil {
			t.Fatalf("reading the user's configuration in %s: %v", dir, err)
		}
	}
	return entries
}
