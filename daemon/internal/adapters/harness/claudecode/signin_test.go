package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// signInStandIn draws the screens Claude Code 2.1.309 showed when it was signed in again in a configuration folder of
// its own, with the same mix of spaces and cursor moves between words, and reads each key line by line from its
// terminal. SIGN_IN picks how it goes: "ok" says the sign-in succeeded once its method is chosen, "wait" keeps the
// browser waiting, "exit" ends at the session's header, and "unknown" shows a screen the adapter does not know. Its
// working folder is trusted once the trust question is answered with the arrow down and Enter.
const signInStandIn = `#!/bin/bash
if [ "$SIGN_IN" = unknown ]; then
  printf 'Something\x1b[11Gnew\r\nPress any key, token sk-ant-%s\r\n' 0123456789abcdefghijklmnop
  sleep 60
  exit 0
fi
if [ ! -f .trusted ]; then
  printf 'Accessing\x1b[12Gworkspace:\r\n\r\n Quick\x1b[8Gsafety check: Is this a project you created or one you trust?\r\n\r\n'
  printf ' \x1b[38;5;153m\xe2\x9d\xaf\x1b[4GNo,\x1b[8Gexit\x1b[39m\r\n\x1b[4GYes,\x1b[9GI\x1b[11Gtrust\x1b[17Gthis\x1b[22Gfolder\r\n'
  IFS= read -r answer
  [ "$answer" = $'\x1b[B' ] || exit 3
  : > .trusted
fi
printf ' \x1b[1mClaude\x1b[19GCode\x1b[24G\x1b[22m\x1b[38;5;246mv2.1.309\x1b[39m\r\n'
printf '\x1b[173GNot\x1b[177Glogged\x1b[184Gin\x1b[187G\xc2\xb7\x1b[189GRun\x1b[193G/login\r\n'
[ "$SIGN_IN" = exit ] && exit 0
IFS= read -r command
[ "$command" = /login ] || exit 4
printf 'Select login method:\r\n\r\n\xe2\x9d\xaf 1. Claude account with subscription \xc2\xb7 Pro, Max, Team, or Enterprise\r\n'
printf ' 2. Anthropic\x1b[14GConsole\x1b[22Gaccount\r\n'
IFS= read -r choice
[ -z "$choice" ] || exit 5
printf 'Opening\x1b[9Gbrowser\x1b[17Gto\x1b[20Gsign\x1b[25Gin\xe2\x80\xa6\r\n'
printf "Browser didn't open? Use the url below to sign in (c to copy)\r\n"
printf 'https://claude.com/cai/oauth/authorize?code=true&state=%s\r\n\r\nPaste code here if prompted >\r\n' STATESECRET0123456789abcdef
if [ "$SIGN_IN" = wait ]; then
  sleep 60
  exit 0
fi
sleep 0.2
printf ' Logged in as someone@example.com\r\n Login successful. Press Enter to continue\xe2\x80\xa6\r\n'
sleep 60
`

// signInAdapter is an adapter whose CLI is the sign-in stand-in, run as how.
func signInAdapter(t *testing.T, how string) (*Adapter, ports.HarnessInfo) {
	t.Helper()
	path := filepath.Join(tempDir(t), "claude")
	if err := os.WriteFile(path, []byte(signInStandIn), 0o755); err != nil {
		t.Fatal(err)
	}
	a := &Adapter{Home: tempDir(t), Env: append(os.Environ(), "SIGN_IN="+how), signInStep: 5 * time.Second}
	return a, ports.HarnessInfo{ID: ID, Path: path, Detected: true}
}

// harmflow-app#543: the sign-in trusts its folder when asked, runs /login, takes the Claude account, and ends once
// the CLI says the sign-in succeeded; a folder it trusted is not asked about again.
func TestSignInSucceeds(t *testing.T) {
	a, info := signInAdapter(t, "ok")
	dir := filepath.Join(tempDir(t), "sign-in")
	for _, run := range []string{"first, asked to trust its folder", "again, in a folder trusted already"} {
		res := a.SignIn(context.Background(), info, dir)
		if res.Outcome != ports.SignInSucceeded {
			t.Fatalf("SignIn %s = %+v, want %s", run, res, ports.SignInSucceeded)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".trusted")); err != nil {
		t.Errorf("the stand-in recorded no trusted folder in %s: %v", dir, err)
	}
}

// harmflow-app#543: a screen the adapter does not recognize stops the sign-in, typing nothing, and comes back with
// its text masked.
func TestSignInStopsAtAnUnknownScreen(t *testing.T) {
	a, info := signInAdapter(t, "unknown")
	a.signInStep = time.Second
	res := a.SignIn(context.Background(), info, filepath.Join(tempDir(t), "sign-in"))
	if res.Outcome != ports.SignInUnknownScreen {
		t.Fatalf("SignIn = %+v, want %s", res, ports.SignInUnknownScreen)
	}
	if !strings.Contains(res.Screen, "Something new") || strings.Contains(res.Screen, "0123456789abcdefghijklmnop") {
		t.Errorf("SignIn screen = %q, want the stand-in's text with its token masked", res.Screen)
	}
}

// harmflow-app#543: the sign-in ends with its context, and when the session ends before it finished.
func TestSignInEndsWithItsContextOrSession(t *testing.T) {
	t.Run("context", func(t *testing.T) {
		a, info := signInAdapter(t, "wait")
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if res := a.SignIn(ctx, info, filepath.Join(tempDir(t), "sign-in")); res.Outcome != ports.SignInStopped {
			t.Errorf("SignIn = %+v, want %s", res, ports.SignInStopped)
		}
	})
	t.Run("session", func(t *testing.T) {
		a, info := signInAdapter(t, "exit")
		if res := a.SignIn(context.Background(), info, filepath.Join(tempDir(t), "sign-in")); res.Outcome != ports.SignInEnded {
			t.Errorf("SignIn = %+v, want %s", res, ports.SignInEnded)
		}
	})
	t.Run("no CLI", func(t *testing.T) {
		a := &Adapter{Home: tempDir(t)}
		res := a.SignIn(context.Background(), ports.HarnessInfo{ID: ID}, filepath.Join(tempDir(t), "sign-in"))
		if res.Outcome != ports.SignInEnded {
			t.Errorf("SignIn with no CLI = %+v, want %s", res, ports.SignInEnded)
		}
	})
}

// harmflow-app#543: a screen keeps its words apart where the CLI moved the cursor between them, and its log masks the
// sign-in URL, every line the terminal wrapped it onto, an e-mail address, and a token.
func TestScreenTextAndMask(t *testing.T) {
	raw := "\x1b]0;\xe2\x9c\xb3 Claude Code\x07\x1b[2GSelect\x1b[9Glogin\x1b[15Gmethod:\x1b[39m\r\r\n" +
		"Use the url below\r\nhttps://claude.com/cai/oauth/authorize?code=true&client_id=abc&scope=org%3A\r\n" +
		"create_api_key+user%3Aprofile&code_challe\r\nnge_method=S256&state=8ptAF\r\n\r\n" +
		"Logged in as someone@example.com\r\nkey sk-ant-0123456789abcdefghijklmnop\r\n"
	text := screenText([]byte(raw))
	if !strings.Contains(text, " Select login method:") || !strings.Contains(squeeze(text), loginMenu) {
		t.Errorf("screenText = %q, want the words of the menu apart", text)
	}
	masked := maskScreen(text)
	for _, secret := range []string{"client_id", "create_api_key", "8ptAF", "someone@example.com", "0123456789abcdef"} {
		if strings.Contains(masked, secret) {
			t.Errorf("maskScreen = %q, which still holds %q", masked, secret)
		}
	}
	if !strings.Contains(masked, "Use the url below") || !strings.Contains(masked, "Logged in as") {
		t.Errorf("maskScreen = %q, want the screen's words kept", masked)
	}
}

// harmflow-app#543: an attempt that ends on the CLI's own words that it is signed out says so; one that ends on an
// invalid API key, which a sign-in does not mend, does not.
func TestFinishSaysSignedOut(t *testing.T) {
	cases := []struct {
		stderr string
		want   bool
	}{
		{"Not logged in · Please run /login", true},
		{"API Error: 401 OAuth token has expired", true},
		{"Invalid API key · Fix external API key", false},
		{"the task failed", false},
	}
	for _, c := range cases {
		end := (&parser{}).Finish(1, c.stderr, false, false)
		if end.SignedOut != c.want {
			t.Errorf("Finish(%q).SignedOut = %v, want %v", c.stderr, end.SignedOut, c.want)
		}
	}
}
