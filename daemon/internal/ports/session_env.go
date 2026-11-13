package ports

import (
	"os"
	"strings"
)

// codexHome names a Codex installation's home. It is that installation's environment, which goes to its probes and
// attempts alone: the Codex adapter sets it on each, to the installation's own home.
const codexHome = "CODEX_HOME"

// SessionEnv is the environment a harness session starts with: base, or the daemon's own environment when base is nil,
// without the variables that would make the session one nested in a Claude Code session that started the daemon
// (CLAUDECODE, CLAUDE_CODE_ENTRYPOINT, CLAUDE_CODE_SSE_PORT), without HarmFlow's own (HARMFLOW_*), which only an
// execution sets, for itself, and without CODEX_HOME, as WithoutCodexHome says. An execution's session, a script task's
// program, and the session that reads Claude Code's skill list all start with it, so the list is the one the execution
// loads. The result is never nil, which would start the process with the daemon's environment as it is.
func SessionEnv(base []string) []string {
	return without(base, func(k string) bool {
		return k == "CLAUDECODE" || k == "CLAUDE_CODE_ENTRYPOINT" || k == "CLAUDE_CODE_SSE_PORT" ||
			strings.HasPrefix(k, "HARMFLOW_") || k == codexHome
	})
}

// WithoutCodexHome is the environment of a process the daemon starts that is no Codex probe or attempt: base, or the
// daemon's own environment when base is nil, without CODEX_HOME. The home the daemon's CODEX_HOME names is the Codex
// installation in use, whose environment no other process receives, so a program that runs codex does not work under
// that installation's account. The result is never nil, which would start the process with the daemon's environment as
// it is.
func WithoutCodexHome(base []string) []string {
	return without(base, func(k string) bool { return k == codexHome })
}

// without is base, or the daemon's own environment when base is nil, without the variables drop names, in order and
// never nil.
func without(base []string, drop func(key string) bool) []string {
	if base == nil {
		base = os.Environ()
	}
	out := []string{}
	for _, kv := range base {
		if k, _, _ := strings.Cut(kv, "="); !drop(k) {
			out = append(out, kv)
		}
	}
	return out
}
