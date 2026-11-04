package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// signInStandIn prints what Codex 0.163.0 printed when `codex login` ran with its input empty and its output on pipes,
// and fails on any other command, on input it is given, and on a terminal. SIGN_IN picks how it goes: "ok" says the
// sign-in succeeded, writes its credential into its Codex home, and exits 0; "wait" keeps the browser waiting; "error"
// fails at the browser's step as the CLI does, exiting 1; "unknown" prints output the adapter does not know, a token in
// it; "exit" ends before the browser's step.
const signInStandIn = `#!/bin/bash
[ "$*" = login ] || exit 2
if IFS= read -r _; then exit 3; fi
[ -t 1 ] && exit 4
if [ "$SIGN_IN" = unknown ]; then
  printf 'Something new\nPaste the token sk-%s\n' 0123456789abcdefghijklmnop
  sleep 60
  exit 0
fi
[ "$SIGN_IN" = exit ] && exit 1
echo "Starting local login server on http://localhost:1455."
echo "If your browser did not open, navigate to this URL to authenticate:"
echo
echo "https://auth.openai.com/oauth/authorize?response_type=code&state=STATESECRET0123456789abcdef"
echo
echo 'On a remote or headless machine? Use ` + "`codex login --device-auth`" + ` instead.'
case "$SIGN_IN" in
  wait) sleep 60; exit 0 ;;
  error) echo "Error logging in: the login server stopped" >&2; exit 1 ;;
esac
sleep 0.2
printf '{"auth_mode":"chatgpt"}' > "$CODEX_HOME/auth.json"
echo "Successfully logged in." >&2
`

// signInAdapter is an adapter whose CLI is the sign-in stand-in, run as how, with a Codex home of its own.
func signInAdapter(t *testing.T, how string) (*Adapter, ports.HarnessInfo, string) {
	t.Helper()
	path := filepath.Join(tempDir(t), "codex")
	if err := os.WriteFile(path, []byte(signInStandIn), 0o755); err != nil {
		t.Fatal(err)
	}
	home := tempDir(t)
	a := &Adapter{Home: tempDir(t), Env: append(os.Environ(), "SIGN_IN="+how, "CODEX_HOME="+home),
		signInStep: 5 * time.Second}
	return a, ports.HarnessInfo{ID: ID, Path: path, Detected: true}, home
}

// harmflow-app#572: the sign-in runs `codex login` with no input and no terminal, waits at the browser's step, and
// ends once the CLI exits 0 after the user approved it; the sign-in lands in the Codex home the environment names.
func TestSignInSucceeds(t *testing.T) {
	a, info, home := signInAdapter(t, "ok")
	for i, run := range []string{"with a bound of its own", "with the default bound"} {
		if i == 1 {
			a.signInStep = 0
		}
		if res := a.SignIn(context.Background(), info, filepath.Join(tempDir(t), "sign-in")); res.Outcome != ports.SignInSucceeded {
			t.Fatalf("SignIn %s = %+v, want %s", run, res, ports.SignInSucceeded)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "auth.json")); err != nil {
		t.Errorf("the stand-in wrote no sign-in into its Codex home %s: %v", home, err)
	}
}

// harmflow-app#572: output that never reaches the browser's step stops the sign-in and comes back with its token
// masked.
func TestSignInStopsAtAnUnknownScreen(t *testing.T) {
	a, info, _ := signInAdapter(t, "unknown")
	res := a.SignIn(context.Background(), info, filepath.Join(tempDir(t), "sign-in"))
	if res.Outcome != ports.SignInUnknownScreen {
		t.Fatalf("SignIn = %+v, want %s", res, ports.SignInUnknownScreen)
	}
	if !strings.Contains(res.Screen, "Something new") || strings.Contains(res.Screen, "0123456789abcdefghijklmnop") {
		t.Errorf("SignIn screen = %q, want the stand-in's text with its token masked", res.Screen)
	}
}

// harmflow-app#572: the sign-in ends with its context while the browser waits, and when the CLI exits before the
// browser's step or fails at it.
func TestSignInEndsWithItsContextOrTheCLI(t *testing.T) {
	t.Run("context", func(t *testing.T) {
		a, info, _ := signInAdapter(t, "wait")
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if res := a.SignIn(ctx, info, filepath.Join(tempDir(t), "sign-in")); res.Outcome != ports.SignInStopped {
			t.Errorf("SignIn = %+v, want %s", res, ports.SignInStopped)
		}
	})
	for _, how := range []string{"exit", "error"} {
		t.Run(how, func(t *testing.T) {
			a, info, _ := signInAdapter(t, how)
			if res := a.SignIn(context.Background(), info, filepath.Join(tempDir(t), "sign-in")); res.Outcome != ports.SignInEnded {
				t.Errorf("SignIn = %+v, want %s", res, ports.SignInEnded)
			}
		})
	}
}

// harmflow-app#572: a sign-in that cannot start the CLI ends at once, saying why when there is a reason.
func TestSignInCannotStart(t *testing.T) {
	notExecutable := filepath.Join(tempDir(t), "codex")
	if err := os.WriteFile(notExecutable, []byte("not a program"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		path    string
		dir     string
		wantErr bool
	}{
		{"no CLI", "", filepath.Join(tempDir(t), "sign-in"), false},
		{"a folder that cannot be made", notExecutable, filepath.Join(notExecutable, "sign-in"), true},
		{"a CLI that cannot start", notExecutable, filepath.Join(tempDir(t), "sign-in"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &Adapter{Home: tempDir(t)}
			res := a.SignIn(context.Background(), ports.HarnessInfo{ID: ID, Path: c.path}, c.dir)
			if res.Outcome != ports.SignInEnded || (res.Err != nil) != c.wantErr {
				t.Errorf("SignIn = %+v, want %s with an error %v", res, ports.SignInEnded, c.wantErr)
			}
		})
	}
}

// harmflow-app#572: an attempt that ends on Codex's own words that it is signed out says so; one that ends on an API
// key that failed, which a browser sign-in does not mend, does not.
func TestFinishSaysSignedOut(t *testing.T) {
	cases := []struct {
		stderr string
		want   bool
	}{
		{"Error: Not logged in. Please run `codex login`", true},
		{"Your access token could not be refreshed because your refresh token has expired. Please log in again.", true},
		{"unexpected status 401 Unauthorized: invalid api key", false},
		{"the task failed", false},
	}
	for _, c := range cases {
		end := (&parser{}).Finish(1, c.stderr, false, false)
		if end.SignedOut != c.want {
			t.Errorf("Finish(%q).SignedOut = %v, class %v, want %v", c.stderr, end.SignedOut, end.Class, c.want)
		}
	}
}
