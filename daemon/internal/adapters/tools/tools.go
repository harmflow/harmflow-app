// Package tools reports Git and the GitHub CLI, which the built-in GitHub skills use with the user's own login
// (FS-006 FR-002). HarmFlow never installs or signs in to them.
package tools

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/procutil"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

type Prober struct {
	Home    string
	PathEnv string
	Env     []string
}

var versionRe = regexp.MustCompile(`(\d+\.\d+(\.\d+)?)`)

func (p *Prober) Probe(ctx context.Context) []ports.ToolInfo {
	return []ports.ToolInfo{p.git(ctx), p.gh(ctx)}
}

func (p *Prober) find(name string) string {
	found := procutil.LookPath(name, p.PathEnv, []string{"/opt/homebrew/bin/" + name, "/usr/local/bin/" + name, "/usr/bin/" + name})
	if len(found) == 0 {
		return ""
	}
	return found[0]
}

func (p *Prober) git(ctx context.Context) ports.ToolInfo {
	t := ports.ToolInfo{ID: "git", Authentication: "unknown"}
	t.Path = p.find("git")
	if t.Path == "" {
		t.SetDiagnostic(domain.T("git_not_found", "git was not found"))
		return t
	}
	out, _, err := procutil.Output(ctx, 10*time.Second, p.Env, t.Path, "--version")
	if err != nil {
		t.SetDiagnostic(commandFailed("git --version", err))
		return t
	}
	t.Detected = true
	if m := versionRe.FindStringSubmatch(out); m != nil {
		t.Version = m[1]
	}
	return t
}

func (p *Prober) gh(ctx context.Context) ports.ToolInfo {
	t := ports.ToolInfo{ID: "gh", Authentication: "unknown"}
	t.Path = p.find("gh")
	if t.Path == "" {
		t.SetDiagnostic(domain.T("gh_not_found", "the GitHub CLI (gh) was not found; the GitHub issue type needs it"))
		return t
	}
	out, _, err := procutil.Output(ctx, 10*time.Second, p.Env, t.Path, "--version")
	if err != nil {
		t.SetDiagnostic(commandFailed("gh --version", err))
		return t
	}
	t.Detected = true
	if m := versionRe.FindStringSubmatch(out); m != nil {
		t.Version = m[1]
	}
	stdout, stderr, err := procutil.Output(ctx, 15*time.Second, p.Env, t.Path, "auth", "status")
	switch {
	case err == nil:
		t.Authentication = "valid"
	case strings.Contains(stdout+stderr, "not logged"):
		t.Authentication = "invalid"
		t.SetDiagnostic(domain.T("not_signed_in", "{{name}} is not signed in; run `{{command}}` in a terminal",
			"name", "gh", "command", "gh auth login"))
	default:
		t.Authentication = "invalid"
		t.SetDiagnostic(domain.T("gh_auth_problem", "gh auth status reported a problem"))
	}
	return t
}

// commandFailed says a check could not run; the command's own error is quoted as it is.
func commandFailed(command string, err error) domain.Text {
	return domain.T("command_failed", "{{command}} failed: {{detail}}", "command", command, "detail", err.Error())
}
