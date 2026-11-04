package codex

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/procutil"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

var _ ports.SignInDriver = (*Adapter)(nil)

// defaultSignInStep bounds the wait for `codex login` to print the URL of its browser sign-in: output that does not
// name it by then is a screen the adapter does not recognize.
const defaultSignInStep = 30 * time.Second

// signInGrace is what `codex login` has to end on SIGTERM before SIGKILL.
const signInGrace = 2 * time.Second

// browserStep is what `codex login` prints once its browser sign-in waits for the user, as Codex 0.163.0 prints it
// with its input empty and its output on pipes, no terminal: "Starting local login server on http://localhost:1455.",
// then "If your browser did not open, navigate to this URL to authenticate:" and the URL it opened. Once the user
// approved the sign-in in the browser, it prints "Successfully logged in." and exits 0; on an error it prints
// "Error logging in: …" and exits 1.
const browserStep = "navigate to this URL to authenticate"

// maxSignInOutput bounds what the sign-in keeps of the CLI's output: its end.
const maxSignInOutput = 64 * 1024

// SignIn implements ports.SignInDriver: it runs the detected codex's own browser sign-in, `codex login`, in dir with
// the user's environment, the Codex home it names included, its input empty and its output on pipes. It waits for the
// CLI to say its browser sign-in waits for the user, then for the CLI to exit, which it does once the user approved
// the sign-in in the browser it opened: an exit of 0 is a sign-in that succeeded. It drives no other method, neither
// a device code nor an API key, and types nothing. Output that does not reach the browser's step is returned, its
// secrets masked.
func (a *Adapter) SignIn(ctx context.Context, info ports.HarnessInfo, dir string) ports.SignInResult {
	if info.Path == "" {
		return ports.SignInResult{Outcome: ports.SignInEnded}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ports.SignInResult{Outcome: ports.SignInEnded, Err: err}
	}
	out := &signInOutput{changed: make(chan struct{}, 1)}
	p, err := procutil.StartLines(ports.Invocation{Path: info.Path, Args: []string{"login"}, Env: a.Env, Dir: dir}, nil,
		out.add, out.add)
	if err != nil {
		return ports.SignInResult{Outcome: ports.SignInEnded, Err: err}
	}
	defer func() {
		p.Stop(signInGrace)
		<-p.Done()
	}()
	step := a.signInStep
	if step == 0 {
		step = defaultSignInStep
	}
	limit := time.NewTimer(step)
	defer limit.Stop()
	for !out.has(browserStep) {
		select {
		case <-ctx.Done():
			return ports.SignInResult{Outcome: ports.SignInStopped}
		case <-limit.C:
			return ports.SignInResult{Outcome: ports.SignInUnknownScreen, Screen: procutil.MaskOutput(out.text())}
		case <-p.Done():
			// Done comes once the output is recorded, so what the CLI printed last is read here.
			if !out.has(browserStep) {
				return ports.SignInResult{Outcome: ports.SignInEnded}
			}
		case <-out.changed:
		}
	}
	// The user approves in the browser: the wait has no bound of its own, only ctx's.
	select {
	case <-ctx.Done():
		return ports.SignInResult{Outcome: ports.SignInStopped}
	case <-p.Done():
	}
	if p.ExitCode() != 0 {
		return ports.SignInResult{Outcome: ports.SignInEnded}
	}
	return ports.SignInResult{Outcome: ports.SignInSucceeded}
}

// signInOutput is what `codex login` printed on both streams, in the order the lines arrived, its end kept within
// maxSignInOutput. changed receives when a line arrives.
type signInOutput struct {
	mu      sync.Mutex
	lines   strings.Builder
	changed chan struct{}
}

func (o *signInOutput) add(line []byte) {
	o.mu.Lock()
	o.lines.Write(line)
	o.lines.WriteByte('\n')
	if over := o.lines.Len() - maxSignInOutput; over > 0 {
		kept := o.lines.String()[over:]
		o.lines.Reset()
		o.lines.WriteString(kept)
	}
	o.mu.Unlock()
	select {
	case o.changed <- struct{}{}:
	default: // one wake is pending already, and it reads every line
	}
}

func (o *signInOutput) text() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.lines.String()
}

func (o *signInOutput) has(s string) bool { return strings.Contains(o.text(), s) }
