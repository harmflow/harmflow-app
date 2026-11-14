// Package tools reports Git and the GitHub CLI, which the built-in GitHub skills use with the user's own login, and
// reads GitHub's count of that login's hourly rate limits. HarmFlow never installs or signs in to them.
package tools

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/procutil"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// Prober is the ToolProber: it looks for git and gh on PathEnv, then in Dirs, and runs them with Env, or the daemon's
// own environment when Env is nil, without the CODEX_HOME that names a Codex installation (ports.WithoutCodexHome).
type Prober struct {
	Home    string
	PathEnv string
	Env     []string
	// Dirs are the folders searched after PathEnv; nil for the usual Homebrew and system locations, usualDirs.
	Dirs []string
}

var (
	_ ports.ToolProber            = (*Prober)(nil)
	_ ports.GitHubRateLimitReader = (*Prober)(nil)
)

// usualDirs are the folders searched after PathEnv when Prober.Dirs is nil.
var usualDirs = []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin"}

var versionRe = regexp.MustCompile(`(\d+\.\d+(\.\d+)?)`)

// Probe implements ports.ToolProber: git, then gh, each with its path and version, and gh's sign-in.
func (p *Prober) Probe(ctx context.Context) []ports.ToolInfo {
	return []ports.ToolInfo{p.git(ctx), p.gh(ctx)}
}

func (p *Prober) find(name string) string {
	dirs := p.Dirs
	if dirs == nil {
		dirs = usualDirs
	}
	var known []string
	for _, d := range dirs {
		known = append(known, d+"/"+name)
	}
	found := procutil.LookPath(name, p.PathEnv, known)
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
	out, _, err := procutil.Output(ctx, 10*time.Second, ports.WithoutCodexHome(p.Env), t.Path, "--version")
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
		t.SetDiagnostic(domain.T("gh_not_found", "the GitHub CLI (gh) was not found; the GitHub task type needs it"))
		return t
	}
	env := ports.WithoutCodexHome(p.Env)
	out, _, err := procutil.Output(ctx, 10*time.Second, env, t.Path, "--version")
	if err != nil {
		t.SetDiagnostic(commandFailed("gh --version", err))
		return t
	}
	t.Detected = true
	if m := versionRe.FindStringSubmatch(out); m != nil {
		t.Version = m[1]
	}
	// Only a gh that says it is signed out reads invalid. Any other failure, a probe killed at its timeout included,
	// leaves the sign-in unknown, as the harnesses' sign-in reads (harmflow-app#71).
	stdout, stderr, err := procutil.Output(ctx, 15*time.Second, env, t.Path, "auth", "status")
	switch {
	case err == nil:
		t.Authentication = "valid"
	case strings.Contains(stdout+stderr, "not logged"):
		t.Authentication = "invalid"
		t.SetDiagnostic(domain.T("not_signed_in", "{{name}} is not signed in; run `{{command}}` in a terminal",
			"name", "gh", "command", "gh auth login"))
	default:
		t.SetDiagnostic(domain.T("authentication_unverified", "authentication could not be verified"))
	}
	return t
}

// RateLimit implements ports.GitHubRateLimitReader: it runs `gh api rate_limit`, with the gh Probe finds and the same
// environment, and reads REST from resources.core and GraphQL from resources.graphql, never the older rate field, which
// GitHub keeps only for older clients. GitHub counts no request to that endpoint against the limit, and HarmFlow keeps
// no count of its own. With gh missing, signed out, or failing, or an answer it cannot read, both buckets are left out.
func (p *Prober) RateLimit(ctx context.Context) ports.GitHubRateLimit {
	path := p.find("gh")
	if path == "" {
		return ports.GitHubRateLimit{}
	}
	out, _, err := procutil.Output(ctx, 10*time.Second, ports.WithoutCodexHome(p.Env), path, "api", "rate_limit")
	if err != nil {
		return ports.GitHubRateLimit{}
	}
	var answer struct {
		Resources struct {
			Core    *rateBucket `json:"core"`
			GraphQL *rateBucket `json:"graphql"`
		} `json:"resources"`
	}
	if err := json.Unmarshal([]byte(out), &answer); err != nil {
		return ports.GitHubRateLimit{}
	}
	return ports.GitHubRateLimit{REST: answer.Resources.Core.bucket(), GraphQL: answer.Resources.GraphQL.bucket()}
}

// rateBucket is one bucket of the answer of `gh api rate_limit`, in GitHub's names; Reset is in Unix seconds.
type rateBucket struct {
	Limit int64 `json:"limit"`
	Used  int64 `json:"used"`
	Reset int64 `json:"reset"`
}

// bucket is b as the local API serves it; nil when the answer has no such bucket, or one with no limit.
func (b *rateBucket) bucket() *ports.RateLimitBucket {
	if b == nil || b.Limit <= 0 {
		return nil
	}
	return &ports.RateLimitBucket{Limit: b.Limit, Used: b.Used, ResetAt: time.Unix(b.Reset, 0).UTC()}
}

// commandFailed says a check could not run; the command's own error is quoted as it is.
func commandFailed(command string, err error) domain.Text {
	return domain.T("command_failed", "{{command}} failed: {{detail}}", "command", command, "detail", err.Error())
}
