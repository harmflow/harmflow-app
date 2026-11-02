package ports

import (
	"os"
	"strings"
)

// SessionEnv is the environment a harness session starts with: base, or the daemon's own environment when base is nil,
// without the variables that would make the session one nested in a Claude Code session that started the daemon
// (CLAUDECODE, CLAUDE_CODE_ENTRYPOINT, CLAUDE_CODE_SSE_PORT) and without HarmFlow's own (HARMFLOW_*), which only an
// execution sets, for itself. An execution's session and the session that reads Claude Code's skill list both start
// with it, so the list is the one the execution loads. The result is never nil, which would start the process with the
// daemon's environment as it is.
func SessionEnv(base []string) []string {
	if base == nil {
		base = os.Environ()
	}
	out := []string{}
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case k == "CLAUDECODE", k == "CLAUDE_CODE_ENTRYPOINT", k == "CLAUDE_CODE_SSE_PORT", strings.HasPrefix(k, "HARMFLOW_"):
			continue
		}
		out = append(out, kv)
	}
	return out
}
