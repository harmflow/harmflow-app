package integration

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A saved Claude Code task that cites a user's agent, a user's skill, and a built-in skill runs with its prompt on
// stdin and no main agent, whether the prompt is the copy of a user's agent or the task's own. The user's items are
// left to Claude Code, which loads them by name from the configuration directory validation read: none is defined for
// the session or copied into the execution's plugin. The built-in skill is in that plugin.
func TestCitedItemsReachClaudeCodeByName(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	// The user's items are in the test's own configuration of Claude Code, as in TestFormDocumentIsTheFile.
	claudeConfig := filepath.Join(home, "claude")
	for file, content := range map[string]string{
		"agents/reviewer.md":           "---\nname: reviewer\ndescription: Reviews a change.\n---\nReview the change, and have the tester try it.\n",
		"agents/tester.md":             "---\nname: tester\ndescription: Tries a change.\n---\nTry the change.\n",
		"skills/review-notes/SKILL.md": "---\nname: review-notes\ndescription: Writes review notes.\n---\nWrite the review notes.\n",
	} {
		path := filepath.Join(claudeConfig, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	d := startDaemon(t, home, "CLAUDE_CONFIG_DIR="+claudeConfig)
	d.call("PUT", "/harnesses/claude-code", map[string]any{"path": recordingStandIn(t, "fake-claude", "claude")}, 200)
	copied, _ := d.userItem("agent", "reviewer")["copy"].(map[string]any)
	if copied == nil {
		t.Fatal("GET /catalog gives no copy of the user's agent reviewer")
	}
	d.userItem("agent", "tester")
	d.userItem("skill", "review-notes")

	cases := []struct {
		id, prompt, description string
		origin                  map[string]any
	}{
		{id: "agent-copy", prompt: copied["prompt"].(string), description: copied["description"].(string),
			origin: map[string]any{"agent": "reviewer", "hash": copied["hash"]}},
		{id: "no-agent", prompt: "Review the change with review notes, and have the tester try it."},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			d := d.in(t)

			spec := map[string]any{
				"type":    "general",
				"prompt":  c.prompt,
				"agents":  []any{"tester"},
				"skills":  []any{"review-notes", "hf-github-draft-pr"},
				"harness": map[string]any{"preferred": map[string]any{"provider": "claude-code"}},
			}
			metadata := map[string]any{"id": c.id, "name": "Task " + c.id}
			if c.origin != nil {
				spec["origin"] = c.origin
				metadata["description"] = c.description
			}
			d.call("POST", "/tasks", map[string]any{"document": map[string]any{"apiVersion": "harmflow/v1alpha1",
				"kind": "Task", "metadata": metadata, "spec": spec}}, 201)
			data, err := os.ReadFile(filepath.Join(home, "tasks", c.id+".yaml"))
			if err != nil {
				t.Fatal(err)
			}
			if v := d.call("POST", "/tasks/validate", map[string]any{"yaml": string(data)}, 200); v["valid"] != true {
				t.Errorf("POST /tasks/validate of the file of %s = %v, want valid", c.id, v["errors"])
			}

			x := d.completedRun(c.id)
			if got := recorded(t, x, "stdin"); got != c.prompt {
				t.Errorf("completedRun(%s) stdin = %q, want the task's prompt %q", c.id, got, c.prompt)
			}
			args := recordedArgs(t, x)
			for _, a := range args {
				if strings.HasPrefix(a, "--agent") {
					t.Errorf("completedRun(%s) passed %s, want neither --agent nor --agents", c.id, a)
				}
			}
			if i := slices.Index(args, "--plugin-dir"); i < 0 || i+1 == len(args) {
				t.Errorf("completedRun(%s) args = %q, want a --plugin-dir", c.id, args)
			} else {
				checkPlugin(t, args[i+1])
			}
			got := recorded(t, x, "claude-config-dir")
			if got != claudeConfig {
				t.Errorf("completedRun(%s) CLAUDE_CONFIG_DIR = %q, want %q", c.id, got, claudeConfig)
			}
			for _, file := range []string{"agents/tester.md", "skills/review-notes/SKILL.md"} {
				if _, err := os.Stat(filepath.Join(got, filepath.FromSlash(file))); err != nil {
					t.Errorf("completedRun(%s) CLAUDE_CONFIG_DIR %q holds no %s: %v", c.id, got, file, err)
				}
			}
		})
	}
}

// userItem is the user's agent or skill that GET /catalog lists under the name, which must be found for Claude Code.
func (d *daemon) userItem(kind, name string) map[string]any {
	d.t.Helper()
	for _, x := range d.call("GET", "/catalog", nil, 200)[kind+"s"].([]any) {
		if it := x.(map[string]any); it["name"] == name && it["source"] == "user" {
			if harnesses, _ := it["harnesses"].([]any); !slices.Contains(harnesses, any("claude-code")) {
				d.t.Fatalf("GET /catalog finds the user's %s %s for %v, want claude-code among them", kind, name, harnesses)
			}
			return it
		}
	}
	d.t.Fatalf("GET /catalog lists no user's %s %s", kind, name)
	return nil
}

// checkPlugin fails the test unless the execution's plugin folder holds the built-in skill hf-github-draft-pr and
// nothing named for one of the user's items.
func checkPlugin(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, "skills", "hf-github-draft-pr", "SKILL.md")); err != nil {
		t.Errorf("the plugin %s holds no built-in skill hf-github-draft-pr: %v", dir, err)
	}
	user := []string{"review-notes", "reviewer", "tester"}
	err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if slices.Contains(user, strings.TrimSuffix(e.Name(), ".md")) {
			t.Errorf("the plugin holds the user's item %s", path)
		}
		return nil
	})
	if err != nil {
		t.Errorf("reading the plugin %s: %v", dir, err)
	}
}
