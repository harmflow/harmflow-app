package ports

import (
	"slices"
	"testing"
)

// A session's environment leaves out the variables of a Claude Code session that started the daemon and HarmFlow's
// own, keeps the rest in order, is the daemon's own when given none, and is never nil, which would start the process
// with the daemon's environment as it is.
func TestSessionEnv(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("HARMFLOW_TASK_ID", "of-the-daemon")
	t.Setenv("SESSION_ENV_KEPT", "yes")
	for _, c := range []struct {
		name string
		base []string
		want []string
	}{
		{"the given environment", []string{"PATH=/usr/bin", "CLAUDECODE=1", "CLAUDE_CODE_ENTRYPOINT=cli",
			"CLAUDE_CODE_SSE_PORT=50123", "HARMFLOW_EXECUTION_ID=x", "HARMFLOW_WORKSPACE=/w", "CLAUDECODE_LIKE=kept",
			"HOME=/h"}, []string{"PATH=/usr/bin", "CLAUDECODE_LIKE=kept", "HOME=/h"}},
		{"only what is left out", []string{"CLAUDECODE=1", "HARMFLOW_TASK_ID=t"}, []string{}},
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
		slices.Contains(got, "HARMFLOW_TASK_ID=of-the-daemon") {
		t.Errorf("SessionEnv(nil) = %q, want the daemon's own environment without CLAUDECODE and HARMFLOW_TASK_ID", got)
	}
}
