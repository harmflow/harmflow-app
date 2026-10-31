package integration

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"gopkg.in/yaml.v3"
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
	sameTaskFiles(t, home, files)
	if after := d.call("GET", "/settings", nil, 200); !reflect.DeepEqual(after, settings) {
		t.Errorf("the settings changed: %v, was %v", after, settings)
	}
}

// sameTaskFiles fails the test when the task files under home are not those of before: a refused request saves
// nothing.
func sameTaskFiles(t *testing.T, home string, before map[string]string) {
	t.Helper()
	if after := taskFiles(t, home); !reflect.DeepEqual(after, before) {
		t.Errorf("the task files changed: %v, was %v", after, before)
	}
}

// invalid sends body to POST /tasks/validate, which must answer it not valid, and returns the message key and the path
// of its first error.
func (d *daemon) invalid(body any) (string, string) {
	d.t.Helper()
	v := d.call("POST", "/tasks/validate", body, 200)
	errs, _ := v["errors"].([]any)
	if v["valid"] != false || len(errs) == 0 {
		d.t.Errorf("POST /tasks/validate = %v, want not valid", v)
		return "", ""
	}
	first, _ := errs[0].(map[string]any)
	key, _ := first["messageKey"].(string)
	field, _ := first["path"].(string)
	return key, field
}

// document is the form's document of a task file: the same task, sent as JSON.
func document(t *testing.T, file string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(file), &doc); err != nil {
		t.Fatalf("parsing %s: %v", file, err)
	}
	return doc
}

// pathRuleDaemon starts a daemon whose one allowed folder is root, named by the variable E3ROOT, and saves there a
// valid task kept. It returns the daemon and kept's revision.
func pathRuleDaemon(t *testing.T, home, root string) (*daemon, string) {
	t.Helper()
	d := startDaemon(t, home)
	d.call("PATCH", "/settings", map[string]any{"allowedFolders": []any{root},
		"pathVariables": map[string]any{"E3ROOT": root}}, 200)
	created := d.call("POST", "/tasks", map[string]any{"yaml": task("kept", "p", "  workspace:\n    path: ${E3ROOT}/app\n")}, 201)
	rev, _ := created["revision"].(string)
	return d, rev
}

// harmflow-app#446: the form's document reaches the same path rule as the YAML editor, whether it is saved or only
// validated, and a save it refuses writes nothing.
func TestFormDocumentPathsAreChecked(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	d, _ := pathRuleDaemon(t, home, t.TempDir())
	files := taskFiles(t, home)

	cases := []struct {
		name, file string
		key, field string
	}{
		{"a workspace of /", task("root", "p", "  workspace:\n    path: /\n"), "path_forbidden_home", "/spec/workspace/path"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := d.in(t)
			body := map[string]any{"document": document(t, c.file)}
			if key, field := d.refusal("POST", "/tasks", body); key != c.key || field != c.field {
				t.Errorf("POST /tasks refused with %s at %s, want %s at %s", key, field, c.key, c.field)
			}
			if key, field := d.invalid(body); key != c.key || field != c.field {
				t.Errorf("POST /tasks/validate found %s at %s, want %s at %s", key, field, c.key, c.field)
			}
		})
	}
	sameTaskFiles(t, home, files)
}

// harmflow-app#446: an edit of a saved task reaches the path rule, as YAML and as the form's document, and an edit it
// refuses leaves the task's file and its revision as they were.
func TestEditPathsAreChecked(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	d, rev := pathRuleDaemon(t, home, t.TempDir())
	files := taskFiles(t, home)
	// Not created: it resolves through the home folder, which exists, into ~/Library.
	edited := task("kept", "p", "  workspace:\n    path: \"~/harmflow-not-created/../Library/Application Support\"\n")

	cases := []struct {
		name string
		body map[string]any
	}{
		{"as YAML", map[string]any{"yaml": edited, "baseRevision": rev}},
		{"as a document", map[string]any{"document": document(t, edited), "baseRevision": rev}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := d.in(t)
			key, field := d.refusal("PUT", "/tasks/kept", c.body)
			if key != "path_forbidden_place" || field != "/spec/workspace/path" {
				t.Errorf("PUT /tasks/kept refused with %s at %s, want path_forbidden_place at /spec/workspace/path",
					key, field)
			}
		})
	}
	sameTaskFiles(t, home, files)
	if got := d.call("GET", "/tasks/kept", nil, 200)["revision"]; got != rev {
		t.Errorf("GET /tasks/kept revision = %v, want %s", got, rev)
	}
}

// harmflow-app#446: import reaches the path rule, whose refusal stays an error where import's lenient mode turns a
// missing agent or skill into a warning, for a new task and over a saved one alike.
func TestImportPathsAreChecked(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	d, _ := pathRuleDaemon(t, home, t.TempDir())
	files := taskFiles(t, home)
	root := "  workspace:\n    path: /\n"

	cases := []struct {
		name      string
		file      string
		overwrite bool
	}{
		{"a new task", task("imported", "p", root), false},
		{"over a saved task", task("kept", "p", root), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := d.in(t)
			key, field := d.refusal("POST", "/tasks/import", map[string]any{"yaml": c.file, "overwrite": c.overwrite})
			if key != "path_forbidden_home" || field != "/spec/workspace/path" {
				t.Errorf("POST /tasks/import refused with %s at %s, want path_forbidden_home at /spec/workspace/path",
					key, field)
			}
		})
	}
	sameTaskFiles(t, home, files)
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
