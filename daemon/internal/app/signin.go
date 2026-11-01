package app

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// defaultSignInTimeout is how long a sign-in waits for the user to approve it in the browser.
const defaultSignInTimeout = 10 * time.Minute

// signInState is the sign-in of one harness: whether one runs, and how to stop it, and whether one that ended without
// signing the harness in holds the next back. A held sign-in starts again only when the user asks for it, or once the
// daemon restarts, which forgets this state; timedOut says the last one ended because the browser step was not done.
type signInState struct {
	running  bool
	stop     context.CancelFunc
	held     bool
	timedOut bool
}

// signInEvents are the events of a sign-in, which concern a harness, not a task.
var signInEvents = map[string]bool{
	domain.EventHarnessSignInStarted: true, domain.EventHarnessSignInCompleted: true,
	domain.EventHarnessSignInTimedOut: true, domain.EventHarnessSignInFailed: true,
}

// SignIn starts the sign-in of a harness by hand, as the Settings button does: it starts even after one that did not
// sign the harness in, and a sign-in already running is the one the request waits for, never a second. It returns the
// harness as the inventory has it, its sign-in running.
func (a *App) SignIn(id string) (ports.HarnessInfo, error) {
	ad, ok := a.adapters[id]
	if !ok {
		return ports.HarnessInfo{}, newError(CodeNotFound, domain.T("harness_not_found", "no harness {{harness}}", "harness", id))
	}
	d, ok := ad.(ports.SignInDriver)
	if !ok {
		return ports.HarnessInfo{}, newError(CodeConflict, domain.T("harness_sign_in_unsupported",
			"HarmFlow cannot sign {{harness}} in; sign it in from a terminal", "harness", ad.Name()))
	}
	a.mu.Lock()
	if a.stopping || a.stopLoops == nil {
		a.mu.Unlock()
		return ports.HarnessInfo{}, newError(CodeConflict, domain.T("daemon_stopping", "the daemon is stopping"))
	}
	st := a.signInLocked(id)
	st.held, st.timedOut = false, false
	launch := a.startSignInLocked(id, d)
	info := a.withSignInLocked(id, a.inventory[id])
	a.mu.Unlock()
	launch()
	return info, nil
}

// signInWhenSignedOut starts the sign-in of a harness that said it is signed out, unless one runs, or one that ended
// without signing it in holds it back, or the daemon has not started or is stopping.
func (a *App) signInWhenSignedOut(id string) {
	d, ok := a.adapters[id].(ports.SignInDriver)
	if !ok {
		return
	}
	a.mu.Lock()
	launch := func() {}
	if !a.stopping && a.stopLoops != nil && !a.signInLocked(id).held {
		launch = a.startSignInLocked(id, d)
	}
	a.mu.Unlock()
	launch()
}

func (a *App) signInLocked(id string) *signInState {
	st := a.signIns[id]
	if st == nil {
		st = &signInState{}
		a.signIns[id] = st
	}
	return st
}

// startSignInLocked marks the harness's sign-in running, unless one runs already, and returns what starts it, which
// the caller calls once it has released a.mu; a sign-in already running gives one that does nothing. a.wg counts the
// sign-in, which Stop ends and waits for.
func (a *App) startSignInLocked(id string, d ports.SignInDriver) func() {
	st := a.signInLocked(id)
	if st.running {
		return func() {}
	}
	ctx, stop := context.WithCancel(context.Background())
	st.running, st.stop = true, stop
	a.wg.Add(1)
	return func() { go a.runSignIn(ctx, id, d) }
}

// withSignInLocked is info with the state of the harness's sign-in.
func (a *App) withSignInLocked(id string, info ports.HarnessInfo) ports.HarnessInfo {
	if _, ok := a.adapters[id].(ports.SignInDriver); !ok {
		return info
	}
	info.SignIn = ports.SignInAvailable
	if st := a.signIns[id]; st != nil && st.running {
		info.SignIn = ports.SignInRunning
	}
	return info
}

// signInDiagnosticLocked replaces the diagnostic of a harness still signed out after a sign-in that timed out: the
// browser step was not done, and the button starts the sign-in again.
func (a *App) signInDiagnosticLocked(id string, info *ports.HarnessInfo) {
	if st := a.signIns[id]; st != nil && st.held && st.timedOut && info.Authentication == "invalid" {
		info.SetDiagnostic(domain.T("harness_sign_in_not_completed",
			"{{harness}} is not signed in: the browser sign-in HarmFlow started was not completed; press Sign in again",
			"harness", info.Name))
	}
}

// runSignIn runs one sign-in to its end: it announces it, lets the adapter drive it for at most the sign-in's timeout,
// checks the harness again once the adapter says it is signed in, and announces how it ended. A sign-in that ends
// without signing the harness in holds the next back. One the daemon stopped announces nothing more.
func (a *App) runSignIn(ctx context.Context, id string, d ports.SignInDriver) {
	defer a.wg.Done()
	name := a.adapters[id].Name()
	a.emit(domain.Event{Type: domain.EventHarnessSignInStarted, Harness: id, Severity: domain.SeverityWarning}.Says(
		domain.T("harness_sign_in_started",
			"{{harness}} is signed out: approve the sign-in HarmFlow started in your browser", "harness", name)))
	timeout := a.d.SignInTimeout
	if timeout == 0 {
		timeout = defaultSignInTimeout
	}
	flow, cancel := context.WithTimeout(ctx, timeout)
	res := d.SignIn(flow, a.harnessInfo(ctx, id), filepath.Join(a.d.Paths.Run, "sign-in", id))
	timedOut := ctx.Err() == nil && errors.Is(flow.Err(), context.DeadlineExceeded) && res.Outcome != ports.SignInSucceeded
	cancel()
	signedIn := false
	if ctx.Err() == nil && res.Outcome == ports.SignInSucceeded {
		// Still marked running, so this check starts no other sign-in.
		signedIn = a.detect(ctx, id).Authentication == "valid"
	}
	a.mu.Lock()
	st := a.signInLocked(id)
	st.running, st.stop = false, nil
	st.held, st.timedOut = !signedIn, timedOut
	a.mu.Unlock()
	if ctx.Err() != nil {
		return // the daemon stops
	}
	switch {
	case signedIn:
		a.emit(domain.Event{Type: domain.EventHarnessSignInCompleted, Harness: id, Severity: domain.SeverityInfo}.Says(
			domain.T("harness_sign_in_completed", "{{harness}} is signed in again", "harness", name)))
	case timedOut:
		a.emit(domain.Event{Type: domain.EventHarnessSignInTimedOut, Harness: id, Severity: domain.SeverityError}.Says(
			domain.T("harness_sign_in_timed_out",
				"the browser sign-in for {{harness}} was not completed within {{minutes}} minutes; press Sign in again to try again",
				"harness", name, "minutes", strconv.Itoa(int(timeout.Round(time.Minute)/time.Minute)))))
		a.detect(ctx, id) // its diagnostic now says the browser sign-in was not completed
	default:
		why := signInFailure(name, res)
		if res.Outcome == ports.SignInUnknownScreen {
			a.log.Printf("the %s sign-in stopped at a screen it does not recognize:\n%s", name, res.Screen)
		}
		a.emit(domain.Event{Type: domain.EventHarnessSignInFailed, Harness: id, Severity: domain.SeverityError,
			Payload: map[string]any{"outcome": string(res.Outcome)}}.Says(why))
	}
}

// signInFailure says why a sign-in ended without signing the harness in.
func signInFailure(name string, res ports.SignInResult) domain.Text {
	switch {
	case res.Outcome == ports.SignInUnknownScreen:
		return domain.T("harness_sign_in_unknown_screen",
			"{{harness}} showed a screen HarmFlow does not recognize, so the sign-in stopped; sign it in from a terminal",
			"harness", name)
	case res.Outcome == ports.SignInSucceeded:
		return domain.T("harness_sign_in_unconfirmed",
			"{{harness}} said the sign-in succeeded, but its check does not read it signed in", "harness", name)
	case res.Err != nil:
		return domain.T("harness_sign_in_start_failed", "the {{harness}} session could not start: {{detail}}",
			"harness", name, "detail", res.Err.Error())
	}
	return domain.T("harness_sign_in_ended", "the {{harness}} session ended before the sign-in finished", "harness", name)
}

// stopSignInsLocked ends every sign-in that runs; Stop then waits for them through a.wg.
func (a *App) stopSignInsLocked() {
	for _, st := range a.signIns {
		if st.stop != nil {
			st.stop()
		}
	}
}
