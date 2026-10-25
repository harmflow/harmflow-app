package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

// pathIssue is a field of a task that breaks the path rule.
type pathIssue struct {
	path string
	text domain.Text
}

// pathEnv is what a task's paths are checked against: the path variables and the rule, from Settings as they are or as
// a change would leave them.
type pathEnv struct {
	vars map[string]string
	rule domain.PathRule
}

func (a *App) currentPathEnv() pathEnv {
	a.mu.Lock()
	s := a.settings
	a.mu.Unlock()
	return a.pathEnvFor(s)
}

func (a *App) pathEnvFor(s Settings) pathEnv {
	vars := map[string]string{}
	for k, v := range s.PathVariables {
		vars[k] = v
	}
	volumes := ""
	if a.d.OS == domain.SystemMacOS {
		volumes = "/Volumes"
	}
	var allowed []string
	for _, f := range s.AllowedFolders {
		if p, err := a.allowedFolder(f); err == nil {
			allowed = append(allowed, p)
		}
	}
	return pathEnv{vars: vars, rule: domain.NewPathRule(a.d.Paths.UserHome, a.d.Paths.Home, volumes, allowed)}
}

// allowedFolder expands an allowed folder of Settings, which takes ~ and ${HOME} but no path variable.
func (a *App) allowedFolder(f string) (string, error) {
	p, err := domain.ExpandPath(f, a.d.Paths.UserHome, nil, nil)
	if err != nil {
		return "", err
	}
	return domain.ResolvePath(p)
}

// writablePath expands p, resolves it, and checks it against the rule: what a workspace, a repository's local path, or
// a writable path of a custom profile becomes, and the path the harness is given.
func (e pathEnv) writablePath(a *App, p string) (string, error) {
	expanded, err := domain.ExpandPath(p, a.d.Paths.UserHome, e.vars, os.Getenv)
	if err != nil {
		return "", err
	}
	real, err := domain.ResolvePath(expanded)
	if err != nil {
		return "", err
	}
	if err := e.rule.Check(real); err != nil {
		return "", err
	}
	return real, nil
}

// taskPathIssues checks the paths of doc that let a harness write, whatever its permission profile, and the form of a
// script's program and working directory. A path whose variables are undefined is skipped: its
// own warning says so, and the check runs once it expands. The daemon's own workspace, used when a task sets none, is
// not checked.
func (a *App) taskPathIssues(doc domain.TaskDocument, e pathEnv) []pathIssue {
	var out []pathIssue
	writable := func(field, p string) {
		if _, err := domain.ExpandPath(p, a.d.Paths.UserHome, e.vars, os.Getenv); err != nil {
			return
		}
		if _, err := e.writablePath(a, p); err != nil {
			out = append(out, pathIssue{field, domain.TextOf(err)})
		}
	}
	if w := doc.Spec.Workspace; w != nil && w.Path != "" {
		writable("/spec/workspace/path", w.Path)
	}
	for i, repo := range doc.Repositories() {
		writable(fmt.Sprintf("/spec/githubIssueImplementation/repositories/%d/localPath", i), repo.LocalPath)
	}
	if p := doc.Spec.Permissions; p != nil && p.Custom != nil {
		for i, w := range p.Custom.WritablePaths {
			writable(fmt.Sprintf("/spec/permissions/custom/writablePaths/%d", i), w)
		}
	}
	if s := doc.Spec.Script; s != nil {
		if s.WorkingDirectory != "" {
			if wd, err := domain.ExpandPath(s.WorkingDirectory, a.d.Paths.UserHome, e.vars, os.Getenv); err == nil &&
				!filepath.IsAbs(wd) {
				out = append(out, pathIssue{"/spec/script/workingDirectory", domain.T("script_working_directory_relative",
					"the working directory {{path}} is not an absolute path", "path", wd)})
			}
		}
		field := "/spec/script/command/0"
		if len(s.Command) == 0 {
			field = "/spec/script/inline/interpreter/0"
		}
		if program := doc.ProgramName(); program != "" {
			if p, err := domain.ExpandPath(program, a.d.Paths.UserHome, e.vars, os.Getenv); err == nil &&
				strings.Contains(p, "/") && !filepath.IsAbs(p) {
				out = append(out, pathIssue{field, domain.T("script_program_relative",
					"the program {{program}} is a relative path; give a name to look up on PATH, or an absolute path",
					"program", p)})
			}
		}
	}
	return out
}

// newPathIssues are those of after that before does not have: what a change of Settings would break.
func newPathIssues(before, after []pathIssue) []pathIssue {
	had := map[string]bool{}
	for _, is := range before {
		had[is.path] = true
	}
	var out []pathIssue
	for _, is := range after {
		if !had[is.path] {
			out = append(out, is)
		}
	}
	return out
}

// refusedPath is why an execution does not start: a path of its task that now breaks the rule, found when it starts,
// so a symlink retargeted after saving does not lead out.
func refusedPath(is pathIssue) domain.Text {
	return domain.T("task_path_refused", "{{field}} of this task is refused: {{detail}}", "field", is.path).
		Nest("detail", is.text)
}
