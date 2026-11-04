package procutil

import (
	"regexp"
	"strings"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/eventlog"
)

// What MaskOutput masks before a harness's output is logged: a URL and the lines a terminal wrapped it onto, which hold
// no space; an e-mail address; and any long run of a token's characters, wherever the terminal cut it.
var (
	wrappedURL = regexp.MustCompile(`https?://\S*(\n\S+)*`)
	emailText  = regexp.MustCompile(`[\w.+-]+@[\w-]+(\.[\w-]+)+`)
	tokenRun   = regexp.MustCompile(`[A-Za-z0-9%+=&_~./-]{24,}`)
	blankLines = regexp.MustCompile(`\n\s*\n+`)
)

// MaxMaskedOutput is the end of an output that MaskOutput keeps for the log, in bytes.
const MaxMaskedOutput = 2000

// MaskOutput is what a harness printed during a sign-in, as the daemon's log keeps it: its end, without blank lines,
// its URLs, e-mail addresses, and token-like runs masked, and then what eventlog.Redact masks.
func MaskOutput(s string) string {
	s = wrappedURL.ReplaceAllString(s, "[URL]")
	s = emailText.ReplaceAllString(s, "[EMAIL]")
	s = tokenRun.ReplaceAllString(s, "[REDACTED]")
	s = strings.TrimSpace(blankLines.ReplaceAllString(s, "\n"))
	if len(s) > MaxMaskedOutput {
		s = s[len(s)-MaxMaskedOutput:]
		for len(s) > 0 && !utf8Start(s[0]) {
			s = s[1:]
		}
	}
	return eventlog.Redact(s)
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
