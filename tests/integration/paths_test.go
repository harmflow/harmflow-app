package integration

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

// refusal sends a request that must be refused with 422 and returns the error's message key and the path of its first
// validation issue.
func (d *daemon) refusal(method, path string, body any) (string, string) {
	d.t.Helper()
	e, _ := d.call(method, path, body, 422)["error"].(map[string]any)
	if e["code"] != "validation_failed" {
		d.t.Errorf("%s %s: error %v", method, path, e)
	}
	key, _ := e["messageKey"].(string)
	validation, _ := e["validation"].(map[string]any)
	errs, _ := validation["errors"].([]any)
	if len(errs) == 0 {
		return key, ""
	}
	first := errs[0].(map[string]any)
	if k, _ := first["messageKey"].(string); k != "" && key == "task_invalid" {
		key = k
	}
	field, _ := first["path"].(string)
	return key, field
}

// harmflow-app#77: a payload carrying a path or a program the rule refuses gets 422 naming its field, and nothing
// changes: the harness inventory's state, the task files, and the Settings read as before.
func TestPathsInRequestsAreChecked(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	d := startDaemon(t, home)
	root := t.TempDir()
	d.call("PATCH", "/settings", map[string]any{"allowedFolders": []any{root},
		"pathVariables": map[string]any{"E3ROOT": root}}, 200)
	d.call("POST", "/tasks", map[string]any{"yaml": task("kept", "p", "  workspace:\n    path: ${E3ROOT}/app\n")}, 201)
	harnesses, _ := os.ReadFile(filepath.Join(home, "state", "harnesses.json"))
	files := taskFiles(t, home)
	settings := d.call("GET", "/settings", nil, 200)

	cases := []struct {
		name, method, path string
		body               any
		key, field         string
	}{
		{"a shell as Codex", "PUT", "/harnesses/codex", map[string]any{"path": "/bin/sh"}, "harness_not_identified", "/path"},
		{"a workspace of /", "POST", "/tasks", map[string]any{"yaml": task("root", "p", "  workspace:\n    path: /\n")},
			"path_forbidden_home", "/spec/workspace/path"},
		// Not created: it resolves through the home folder, which exists, into ~/Library.
		{"a local path whose .. leads into ~/Library", "POST", "/tasks", map[string]any{"yaml": issueTask("dotdot",
			"~/harmflow-not-created/../Library/Application Support")}, "path_forbidden_place",
			"/spec/github/repositories/0/localPath"},
		{"a relative program", "POST", "/tasks", map[string]any{"yaml": scriptTask("relative",
			"  script:\n    command: [./run.sh]\n")}, "script_program_relative", "/spec/script/command/0"},
		{"a variable a saved task uses set to /", "PATCH", "/settings",
			map[string]any{"pathVariables": map[string]any{"E3ROOT": "/"}}, "settings_change_breaks_task",
			"/spec/workspace/path"},
		{"the allowed folder a saved task uses removed", "PATCH", "/settings", map[string]any{"allowedFolders": []any{}},
			"settings_change_breaks_task", "/spec/workspace/path"},
		{"/ as an allowed folder", "PATCH", "/settings", map[string]any{"allowedFolders": []any{root, "/"}},
			"settings_allowed_folder_invalid", "/allowedFolders/1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.name == "a shell as Codex" && runtime.GOOS != "darwin" {
				t.Skip("macOS's /bin/sh is bash, which answers --version as a program that is not Codex; on Linux it " +
					"is often dash, which fails --version")
			}
			if key, field := d.refusal(c.method, c.path, c.body); key != c.key || field != c.field {
				t.Errorf("refused with %s at %s, want %s at %s", key, field, c.key, c.field)
			}
		})
	}
	if after, _ := os.ReadFile(filepath.Join(home, "state", "harnesses.json")); string(after) != string(harnesses) {
		t.Errorf("state/harnesses.json changed:\n%s\nwas\n%s", after, harnesses)
	}
	if after := taskFiles(t, home); !reflect.DeepEqual(after, files) {
		t.Errorf("the task files changed: %v, was %v", after, files)
	}
	if after := d.call("GET", "/settings", nil, 200); !reflect.DeepEqual(after, settings) {
		t.Errorf("the settings changed: %v, was %v", after, settings)
	}
}

// issueTask is a GitHub issue task whose one repository's local path is localPath.
func issueTask(id, localPath string) string {
	return `apiVersion: harmflow/v1alpha1
kind: Task
metadata:
  id: ` + id + `
  name: Task ` + id + `
spec:
  type: github
  prompt: Implement the oldest eligible issue.
  harness:
    preferred:
      provider: claude-code
  github:
    repositories:
      - name: octo/repo
        localPath: "` + localPath + `"
`
}
