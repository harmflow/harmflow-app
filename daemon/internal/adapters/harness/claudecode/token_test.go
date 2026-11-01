package claudecode

import (
	"context"
	"slices"
	"testing"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// harmflow-app#733: with a token set, the sign-in check and a session run with it in CLAUDE_CODE_OAUTH_TOKEN, and
// with it cleared they run without it again.
func TestUseTokenReachesChecksAndSessions(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "") // the user's settings are read from Home, which has none
	const token = "sk-ant-oat01-test-token-0123456789"
	// The stand-in reads signed in only with the token in its environment, as a CLI with no sign-in of its own does.
	path := standIn(t, `echo "2.1.309 (Claude Code)"`,
		`if [ "$CLAUDE_CODE_OAUTH_TOKEN" = `+token+` ]; then echo '{"loggedIn": true}'; else echo '{"loggedIn": false}'; exit 1; fi`)
	a := &Adapter{Home: t.TempDir(), Env: []string{"PATH=/usr/bin:/bin"}}
	if got := a.Detect(context.Background(), path).Authentication; got != "invalid" {
		t.Fatalf("Detect with no token: authentication %q, want invalid", got)
	}
	a.UseToken(token)
	info := a.Detect(context.Background(), path)
	if info.Authentication != "valid" || info.Readiness != "ready" {
		t.Errorf("Detect with the token: authentication %q, readiness %q; want valid, ready", info.Authentication, info.Readiness)
	}
	perm := domain.TaskDocument{}.Effective().Spec.Permissions
	inv, _, err := a.Prepare(info, ports.RunRequest{WorkDir: "/w", Permissions: *perm, ExecutionDir: t.TempDir(), Prompt: "hi",
		Env: []string{"PATH=/usr/bin:/bin"}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(inv.Env, tokenVariable+"="+token) {
		t.Errorf("Prepare with the token: env %q, want %s set", inv.Env, tokenVariable)
	}
	if slices.Contains(a.Env, tokenVariable+"="+token) {
		t.Errorf("the adapter's own Env took the token: %q", a.Env)
	}
	a.UseToken("")
	if got := a.Detect(context.Background(), path).Authentication; got != "invalid" {
		t.Errorf("Detect once the token is cleared: authentication %q, want invalid", got)
	}
}
