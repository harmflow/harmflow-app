package procutil

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// harmflow-app#543, harmflow-app#572: the log of a sign-in's output masks its URL, every line a terminal wrapped it
// onto, an e-mail address, and a token, and keeps the output's words; a long output keeps its end, cut where a
// character starts.
func TestMaskOutput(t *testing.T) {
	out := "Starting local login server on http://localhost:1455.\n" +
		"If your browser did not open, navigate to this URL to authenticate:\n\n" +
		"https://auth.openai.com/oauth/authorize?response_type=code&client_id=app_x&code_challe\n" +
		"nge=WntYRw&state=36AJ2C0P\n\nLogged in as someone@example.com\nkey sk-0123456789abcdefghijklmnop\n"
	masked := MaskOutput(out)
	for _, secret := range []string{"client_id", "WntYRw", "36AJ2C0P", "someone@example.com", "0123456789abcdef"} {
		if strings.Contains(masked, secret) {
			t.Errorf("MaskOutput = %q, which still holds %q", masked, secret)
		}
	}
	if !strings.Contains(masked, "navigate to this URL to authenticate") || !strings.Contains(masked, "Logged in as") {
		t.Errorf("MaskOutput = %q, want the output's words kept", masked)
	}

	long := MaskOutput(strings.Repeat("é ", 1500) + "the end")
	if len(long) > MaxMaskedOutput || !utf8.ValidString(long) || !strings.HasSuffix(long, "the end") {
		t.Errorf("MaskOutput of a long output = %d bytes, valid %v, ending %q; want its valid end within %d bytes",
			len(long), utf8.ValidString(long), long[max(0, len(long)-10):], MaxMaskedOutput)
	}
}
