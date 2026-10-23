package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

// issueTask is a GitHub issue task whose one repository's local path is localPath, with extra under spec.
func issueTask(id, localPath, extra string) string {
	return fmt.Sprintf(`apiVersion: harmflow/v1alpha1
kind: Task
metadata:
  id: %s
  name: Task %s
spec:
  type: github-issue-implementation
  prompt: Implement the oldest eligible issue.
  harness:
    preferred:
      provider: claude-code
  githubIssueImplementation:
    repositories:
      - name: octo/repo
        localPath: %s
%s`, id, id, localPath, extra)
}

func mkdir(t *testing.T, p string) string {
	t.Helper()
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

// errorAt returns the key of the validation error at path, or "" when there is none. A file's load error quotes the
// error it wraps, whose key it carries as detailKey.
func errorAt(r *ValidationResult, path string) string {
	for _, is := range r.Errors {
		if is.Path == path {
			if k := is.Text.Params["detailKey"]; is.Text.Key == "task_file_error" && k != "" {
				return k
			}
			return is.Text.Key
		}
	}
	return ""
}

// D-113, FS-009 FR-014, AC-005: a path that lets a harness write is checked once expanded and resolved, whatever the
// task's profile; one outside the allowed area, or in a place no harness may write, is an error naming the field.
func TestTaskPathsAreChecked(t *testing.T) {
	e := newEnv(t)
	home := e.app.Paths().UserHome
	repo := mkdir(t, filepath.Join(home, "Developer", "app"))
	mkdir(t, filepath.Join(home, "Library", "Application Support"))
	outside := mkdir(t, filepath.Join(t.TempDir(), "srv"))
	if err := os.Symlink(outside, filepath.Join(home, "Developer", "elsewhere")); err != nil {
		t.Fatal(err)
	}
	workspace := func(p string) string { return taskYAML("t", "  workspace:\n    path: "+p+"\n") }
	writable := func(p string) string {
		return taskYAML("t", "  permissions:\n    profile: custom\n    custom:\n      writablePaths: ["+p+"]\n")
	}
	const ws, wp = "/spec/workspace/path", "/spec/permissions/custom/writablePaths/0"
	const lp = "/spec/githubIssueImplementation/repositories/0/localPath"
	cases := []struct {
		name, yaml, path, key string
	}{
		{"a workspace inside home", workspace("~/Developer/app"), ws, ""},
		{"a workspace not created yet inside home", workspace(home + "/Developer/new"), ws, ""},
		{"a workspace of /", workspace("/"), ws, "path_forbidden_home"},
		{"the home folder", workspace(`"~"`), ws, "path_forbidden_home"}, // a bare ~ is YAML's null
		{"inside HarmFlow's data directory", workspace(e.app.Paths().Tasks), ws, "path_forbidden_place"},
		{"a .. segment into ~/Library", workspace(home + "/Developer/../Library/Application Support"), ws,
			"path_forbidden_place"},
		{"a symlink out of the allowed area", workspace(home + "/Developer/elsewhere/x"), ws, "path_outside_allowed_area"},
		{"a read-only task too", taskYAML("t", "  permissions:\n    profile: read_only\n  workspace:\n    path: /\n"), ws,
			"path_forbidden_home"},
		{"a writable path in a hidden folder", writable("~/.ssh"), wp, "path_forbidden_place"},
		{"a writable path inside home", writable("~/Developer/app"), wp, ""},
		{"a local path inside home", issueTask("t", repo, ""), lp, ""},
		{"a local path outside", issueTask("t", outside, ""), lp, "path_outside_allowed_area"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := e.app.Validate(t.Context(), TaskInput{YAML: c.yaml})
			if got := errorAt(r, c.path); got != c.key {
				t.Errorf("error at %s = %q, want %q (errors %+v)", c.path, got, c.key, r.Errors)
			}
		})
	}
	// An allowed folder of Settings opens the area to it.
	if _, err := e.app.UpdateSettings(SettingsPatch{AllowedFolders: []string{outside}}); err != nil {
		t.Fatal(err)
	}
	if r := e.app.Validate(t.Context(), TaskInput{YAML: issueTask("t", outside, "")}); errorAt(r, lp) != "" {
		t.Errorf("a local path inside an allowed folder: %+v", r.Errors)
	}
}

// FS-013 FR-003, FR-004 (D-113): a script's program is a name looked up on PATH or an absolute path, never a relative
// one, and its working directory is absolute; neither is held to the area, since the program runs with the user's rights.
func TestScriptProgramAndWorkingDirectoryForm(t *testing.T) {
	e := newEnv(t)
	const cmd, interp, wd = "/spec/script/command/0", "/spec/script/inline/interpreter/0", "/spec/script/workingDirectory"
	cases := []struct {
		name, body, path, key string
	}{
		{"a bare name", "  script:\n    command: [echo, hi]\n", cmd, ""},
		{"an absolute path outside home", "  script:\n    command: [/bin/echo]\n", cmd, ""},
		{"a path under ~", "  script:\n    command: [~/bin/tool]\n", cmd, ""},
		{"a relative path", "  script:\n    command: [./run.sh]\n", cmd, "script_program_relative"},
		{"a relative path with no dot", "  script:\n    command: [bin/tool]\n", cmd, "script_program_relative"},
		{"a relative interpreter", "  script:\n    inline:\n      interpreter: [venv/bin/python]\n      source: print(1)\n",
			interp, "script_program_relative"},
		{"a relative working directory", "  script:\n    command: [echo]\n    workingDirectory: jobs\n", wd,
			"script_working_directory_relative"},
		{"a working directory of /", "  script:\n    command: [echo]\n    workingDirectory: /\n", wd, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := e.app.Validate(t.Context(), TaskInput{YAML: scriptYAML("s", c.body)})
			if got := errorAt(r, c.path); got != c.key {
				t.Errorf("error at %s = %q, want %q (errors %+v)", c.path, got, c.key, r.Errors)
			}
		})
	}
}

func appErrorKey(err error) (string, string) {
	var ae *Error
	if !errors.As(err, &ae) {
		return "", ""
	}
	return ae.Code, ae.Key
}

// FS-009 FR-014, AC-005: a change of Settings that would put a saved task's path outside the rule is refused, naming
// the task, and so is an allowed folder in a place no harness may write; nothing is saved.
func TestSettingsChangeThatBreaksATaskIsRefused(t *testing.T) {
	e := newEnv(t)
	home := e.app.Paths().UserHome
	mkdir(t, filepath.Join(home, "Developer", "app"))
	outside := mkdir(t, filepath.Join(t.TempDir(), "srv"))
	if _, err := e.app.UpdateSettings(SettingsPatch{PathVariables: map[string]string{"REPOS": home + "/Developer"},
		AllowedFolders: []string{outside}}); err != nil {
		t.Fatal(err)
	}
	e.create("uses-var", "  workspace:\n    path: ${REPOS}/app\n")
	if _, err := e.app.CreateTask(t.Context(), TaskInput{YAML: issueTask("uses-folder", outside, "")}); err != nil {
		t.Fatal(err)
	}
	before := e.app.GetSettings()

	_, err := e.app.UpdateSettings(SettingsPatch{PathVariables: map[string]string{"REPOS": "/"}})
	if code, key := appErrorKey(err); code != CodeValidationFailed || key != "settings_change_breaks_task" ||
		!strings.Contains(err.Error(), "uses-var") {
		t.Errorf("REPOS set to /: %s %s %v", code, key, err)
	}
	_, err = e.app.UpdateSettings(SettingsPatch{AllowedFolders: []string{}})
	if code, key := appErrorKey(err); code != CodeValidationFailed || key != "settings_change_breaks_task" ||
		!strings.Contains(err.Error(), "uses-folder") {
		t.Errorf("removing the allowed folder a task uses: %s %s %v", code, key, err)
	}
	for _, f := range []string{"/", "~", "~/Library", "~/.config"} {
		_, err = e.app.UpdateSettings(SettingsPatch{AllowedFolders: []string{outside, f}})
		if code, key := appErrorKey(err); code != CodeValidationFailed || key != "settings_allowed_folder_invalid" {
			t.Errorf("allowed folder %s: %s %s %v", f, code, key, err)
		}
	}
	after := e.app.GetSettings()
	if after.PathVariables["REPOS"] != before.PathVariables["REPOS"] ||
		strings.Join(after.AllowedFolders, ",") != strings.Join(before.AllowedFolders, ",") {
		t.Errorf("a refused change was saved: %+v, was %+v", after.Settings, before.Settings)
	}
	// A change that breaks nothing, and one to another setting, still apply.
	if _, err := e.app.UpdateSettings(SettingsPatch{PathVariables: map[string]string{"REPOS": home + "/Developer",
		"OTHER": "/"}}); err != nil {
		t.Errorf("a variable no task uses: %v", err)
	}
	lang := "en-US"
	if _, err := e.app.UpdateSettings(SettingsPatch{Language: &lang}); err != nil {
		t.Errorf("the language: %v", err)
	}
}

// FS-009 FR-014, AC-014: a task file written outside the dashboard loads as invalid when a path breaks the rule, and
// an execution checks its task's paths again before its harness starts, so a symlink retargeted after saving fails it.
func TestSavedTaskOutsideTheRuleDoesNotRun(t *testing.T) {
	e := newEnv(t)
	home := e.app.Paths().UserHome
	target := mkdir(t, filepath.Join(home, "Developer", "app"))
	link := filepath.Join(home, "Developer", "current")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	e.create("linked", "  workspace:\n    path: "+link+"\n")

	file := filepath.Join(e.app.Paths().Tasks, "rooted.yaml")
	if err := os.WriteFile(file, []byte(taskYAML("rooted", "  workspace:\n    path: /\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	e.app.scanTaskFiles(t.Context())
	v, err := e.app.GetTask("rooted")
	if err != nil || !v.Invalid || v.Validation == nil || errorAt(v.Validation, "/spec/workspace/path") != "path_forbidden_home" {
		t.Errorf("a task file with workspace /: invalid %v, validation %+v, %v", v.Invalid, v.Validation, err)
	}

	etc := mkdir(t, filepath.Join(t.TempDir(), "etc"))
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(etc, link); err != nil {
		t.Fatal(err)
	}
	r, err := e.app.RunTask("linked")
	if err != nil {
		t.Fatal(err)
	}
	x := e.wait(r.ExecutionID, domain.StatusFailed)
	if x.ErrorKey != "task_path_refused" || !strings.Contains(x.ErrorMessage, "/spec/workspace/path") {
		t.Errorf("the execution failed with %s: %s", x.ErrorKey, x.ErrorMessage)
	}
	e.claude.mu.Lock()
	defer e.claude.mu.Unlock()
	if len(e.claude.prompts) != 0 {
		t.Errorf("the harness started: %v", e.claude.prompts)
	}
}
