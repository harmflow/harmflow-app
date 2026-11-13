package ports

import (
	"slices"
	"testing"
)

// A session's environment leaves out the variables of a Claude Code session that started the daemon, HarmFlow's own,
// and CODEX_HOME, keeps the rest in order, is the daemon's own when given none, and is never nil, which would start the
// process with the daemon's environment as it is.
func TestSessionEnv(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("HARMFLOW_TASK_ID", "of-the-daemon")
	t.Setenv("CODEX_HOME", "/codex-of-the-daemon")
	t.Setenv("SESSION_ENV_KEPT", "yes")
	for _, c := range []struct {
		name string
		base []string
		want []string
	}{
		{"the given environment", []string{"PATH=/usr/bin", "CLAUDECODE=1", "CLAUDE_CODE_ENTRYPOINT=cli",
			"CLAUDE_CODE_SSE_PORT=50123", "HARMFLOW_EXECUTION_ID=x", "HARMFLOW_WORKSPACE=/w", "CLAUDECODE_LIKE=kept",
			"CODEX_HOME=/c", "CODEX_HOME_LIKE=kept", "HOME=/h"},
			[]string{"PATH=/usr/bin", "CLAUDECODE_LIKE=kept", "CODEX_HOME_LIKE=kept", "HOME=/h"}},
		{"only what is left out", []string{"CLAUDECODE=1", "HARMFLOW_TASK_ID=t", "CODEX_HOME="}, []string{}},
		{"none", []string{}, []string{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := SessionEnv(c.base); got == nil || !slices.Equal(got, c.want) {
				t.Errorf("SessionEnv(%q) = %#v, want %#v", c.base, got, c.want)
			}
		})
	}
	got := SessionEnv(nil)
	if !slices.Contains(got, "SESSION_ENV_KEPT=yes") || slices.Contains(got, "CLAUDECODE=1") ||
		slices.Contains(got, "HARMFLOW_TASK_ID=of-the-daemon") || slices.Contains(got, "CODEX_HOME=/codex-of-the-daemon") {
		t.Errorf("SessionEnv(nil) = %q, want the daemon's own environment without CLAUDECODE, HARMFLOW_TASK_ID, and "+
			"CODEX_HOME", got)
	}
}

// harmflow-app#1028: a process that is no Codex probe or attempt starts without CODEX_HOME, whether it comes in the
// environment given or in the daemon's own, and with the rest of it in order; the result is never nil.
func TestWithoutCodexHome(t *testing.T) {
	t.Setenv("CODEX_HOME", "/codex-of-the-daemon")
	t.Setenv("WITHOUT_CODEX_HOME_KEPT", "yes")
	for _, c := range []struct {
		name string
		base []string
		want []string
	}{
		{"the given environment", []string{"PATH=/usr/bin", "CODEX_HOME=/c", "CODEX_HOME_LIKE=kept", "CLAUDECODE=1",
			"HARMFLOW_TASK_ID=t"}, []string{"PATH=/usr/bin", "CODEX_HOME_LIKE=kept", "CLAUDECODE=1", "HARMFLOW_TASK_ID=t"}},
		{"only CODEX_HOME", []string{"CODEX_HOME=/c"}, []string{}},
		{"none", []string{}, []string{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := WithoutCodexHome(c.base); got == nil || !slices.Equal(got, c.want) {
				t.Errorf("WithoutCodexHome(%q) = %#v, want %#v", c.base, got, c.want)
			}
		})
	}
	got := WithoutCodexHome(nil)
	if !slices.Contains(got, "WITHOUT_CODEX_HOME_KEPT=yes") || slices.Contains(got, "CODEX_HOME=/codex-of-the-daemon") {
		t.Errorf("WithoutCodexHome(nil) = %q, want the daemon's own environment without CODEX_HOME", got)
	}
}
