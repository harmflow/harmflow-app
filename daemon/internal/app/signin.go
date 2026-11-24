package app

import (
	"context"
	"errors"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// defaultSignInTimeout is how long a sign-in waits for the user to approve it in the browser.
const defaultSignInTimeout = 10 * time.Minute

// signInKey names the installation a sign-in signs in: its harness, and its installation identifier, empty for the
// installation in use, which a home of Homes with the identifier of the one in use names too.
type signInKey struct {
	harness      string
	installation string
}

// signInState is the sign-in of one installation: whether one runs, and how to stop it, and whether one that ended
// without signing the installation in holds the next back. A held sign-in starts again only when the user asks for it,
// or once the daemon restarts, which forgets this state; timedOut says the last one ended because the browser step was
// not done.
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

// SignIn starts the sign-in of an installation of a harness by hand, as the Settings buttons do: the installation in
// use for an empty installation, otherwise the entry of its Homes or CustomEntries with that identifier, which is
// signed in with its own environment. It starts even after one that did not sign the installation in, and a sign-in
// already running for the same installation is the one the request waits for, never a second. It returns the harness
// as the inventory has it, the installation's sign-in running.
func (a *App) SignIn(id, installation string) (ports.HarnessInfo, error) {
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
	key, found := a.signInKeyLocked(id, installation)
	if !found {
		a.mu.Unlock()
		return ports.HarnessInfo{}, newError(CodeNotFound, installationMissing(ad.Name(), installation))
	}
	if a.stopping || a.stopLoops == nil {
		a.mu.Unlock()
		return ports.HarnessInfo{}, newError(CodeConflict, domain.T("daemon_stopping", "the daemon is stopping"))
	}
	st := a.signInLocked(key)
	st.held, st.timedOut = false, false
	launch := a.startSignInLocked(key, d)
	info := a.withSignInLocked(id, a.inventory[id])
	a.mu.Unlock()
	launch()
	return info, nil
}

// signInWhenSignedOut starts the sign-in of an installation of a harness that said it is signed out, the one in use
// for an empty installation, unless one runs for it, or one that ended without signing it in holds it back, or the
// daemon has not started or is stopping.
func (a *App) signInWhenSignedOut(id, installation string) {
	d, ok := a.adapters[id].(ports.SignInDriver)
	if !ok {
		return
	}
	a.mu.Lock()
	launch := func() {}
	if key, found := a.signInKeyLocked(id, installation); found && !a.stopping && a.stopLoops != nil && !a.signInLocked(key).held {
		launch = a.startSignInLocked(key, d)
	}
	a.mu.Unlock()
	launch()
}

// signInKeyLocked is the key of the sign-in of an installation of a harness: the installation in use for an empty
// installation or for the identifier of the one in use, otherwise the entry of Homes or CustomEntries the inventory
// lists with that identifier. found is false when the inventory lists none. a.mu is held.
func (a *App) signInKeyLocked(id, installation string) (key signInKey, found bool) {
	info := a.inventory[id]
	if installation == "" || installation == info.InstallationID {
		return signInKey{harness: id}, true
	}
	if _, found := info.InstallationNamed(installation); !found {
		return signInKey{}, false
	}
	return signInKey{harness: id, installation: installation}, true
}

func (a *App) signInLocked(key signInKey) *signInState {
	st := a.signIns[key]
	if st == nil {
		st = &signInState{}
		a.signIns[key] = st
	}
	return st
}

// startSignInLocked marks the installation's sign-in running, unless one runs already, and returns what starts it,
// which the caller calls once it has released a.mu; a sign-in already running gives one that does nothing. a.wg counts
// the sign-in, which Stop ends and waits for.
func (a *App) startSignInLocked(key signInKey, d ports.SignInDriver) func() {
	st := a.signInLocked(key)
	if st.running {
		return func() {}
	}
	ctx, stop := context.WithCancel(context.Background())
	st.running, st.stop = true, stop
	a.wg.Add(1)
	return func() { go a.runSignIn(ctx, key, d) }
}

// withSignInLocked is info with the state of the sign-in of each of its installations: the one in use, and each entry
// of its Homes and CustomEntries. a.mu is held.
func (a *App) withSignInLocked(id string, info ports.HarnessInfo) ports.HarnessInfo {
	if _, ok := a.adapters[id].(ports.SignInDriver); !ok {
		return info
	}
	state := func(installation string) string {
		if installation == info.InstallationID {
			installation = ""
		}
		if st := a.signIns[signInKey{harness: id, installation: installation}]; st != nil && st.running {
			return ports.SignInRunning
		}
		return ports.SignInAvailable
	}
	info.SignIn = state("")
	withStates := func(list []ports.HarnessInfo) []ports.HarnessInfo {
		if len(list) == 0 {
			return list
		}
		list = slices.Clone(list)
		for i := range list {
			list[i].SignIn = state(list[i].InstallationID)
		}
		return list
	}
	info.Homes, info.CustomEntries = withStates(info.Homes), withStates(info.CustomEntries)
	return info
}

// signInDiagnosticLocked replaces the diagnostic of each installation of a harness still signed out after a sign-in
// that timed out: the browser step was not done, and the button starts the sign-in again. a.mu is held.
func (a *App) signInDiagnosticLocked(id string, info *ports.HarnessInfo) {
	diagnose := func(installation string, inst *ports.HarnessInfo) {
		if installation == info.InstallationID {
			installation = ""
		}
		st := a.signIns[signInKey{harness: id, installation: installation}]
		if st != nil && st.held && st.timedOut && inst.Authentication == "invalid" {
			inst.SetDiagnostic(domain.T("harness_sign_in_not_completed",
				"{{harness}} is not signed in: the browser sign-in HarmFlow started was not completed; press Sign in again",
				"harness", installationName(*inst)))
		}
	}
	diagnose("", info)
	info.Homes, info.CustomEntries = slices.Clone(info.Homes), slices.Clone(info.CustomEntries)
	for _, list := range [][]ports.HarnessInfo{info.Homes, info.CustomEntries} {
		for i := range list {
			diagnose(list[i].InstallationID, &list[i])
		}
	}
}

// signInWhenAnySignedOut starts the sign-in of each installation of a harness detection read signed out: the one in
// use, and each entry of its Homes and CustomEntries, each with its own environment.
func (a *App) signInWhenAnySignedOut(id string, info ports.HarnessInfo) {
	if info.Authentication == "invalid" {
		a.signInWhenSignedOut(id, "")
	}
	for _, inst := range append(slices.Clone(info.Homes), info.CustomEntries...) {
		if inst.Authentication == "invalid" && inst.InstallationID != "" && inst.InstallationID != info.InstallationID {
			a.signInWhenSignedOut(id, inst.InstallationID)
		}
	}
}

// installationName is the name a sign-in's texts give an installation: a custom entry's own name, never what it
// assigns, and otherwise its harness's.
func installationName(inst ports.HarnessInfo) string {
	if inst.Custom() && inst.EntryName != "" {
		return inst.EntryName
	}
	return inst.Name
}

// runSignIn runs one sign-in to its end: it announces it, lets the adapter drive it, with the installation as detection
// last found it, for at most the sign-in's timeout, checks the harness again once the adapter says it is signed in, and
// announces how it ended. A sign-in that ends without signing the installation in holds the next back. One the daemon
// stopped announces nothing more. Its events name the installation by its identifier and, for a custom entry, by its
// name, never by what the entry assigns.
func (a *App) runSignIn(ctx context.Context, key signInKey, d ports.SignInDriver) {
	defer a.wg.Done()
	id := key.harness
	inst, found := a.harnessInfo(ctx, id).InstallationNamed(key.installation)
	if !found {
		// The installation left the inventory since: its sign-in has nothing to run with, and ends at once.
		inst = ports.HarnessInfo{ID: id}
	}
	name := a.adapters[id].Name()
	if inst.Custom() {
		name = installationName(inst)
	}
	// with is the payload of a sign-in's event: the installation's identifier and a custom entry's name, then extra.
	with := func(extra map[string]any) map[string]any {
		p := map[string]any{}
		if inst.InstallationID != "" {
			p["installationId"] = inst.InstallationID
		}
		if inst.Custom() {
			p["entryName"] = inst.EntryName
		}
		maps.Copy(p, extra)
		if len(p) == 0 {
			return nil
		}
		return p
	}
	a.emit(domain.Event{Type: domain.EventHarnessSignInStarted, Harness: id, Severity: domain.SeverityWarning,
		Payload: with(nil)}.Says(domain.T("harness_sign_in_started",
		"{{harness}} is signed out: approve the sign-in HarmFlow started in your browser", "harness", name)))
	timeout := a.d.SignInTimeout
	if timeout == 0 {
		timeout = defaultSignInTimeout
	}
	dir := filepath.Join(a.d.Paths.Run, "sign-in", id)
	if key.installation != "" {
		dir = filepath.Join(a.d.Paths.Run, "sign-in", id+"-"+key.installation)
	}
	flow, cancel := context.WithTimeout(ctx, timeout)
	res := d.SignIn(flow, inst, dir)
	timedOut := ctx.Err() == nil && errors.Is(flow.Err(), context.DeadlineExceeded) && res.Outcome != ports.SignInSucceeded
	cancel()
	signedIn := false
	if ctx.Err() == nil && res.Outcome == ports.SignInSucceeded {
		// Still marked running, so this check starts no other sign-in for the installation.
		checked, ok := a.detect(ctx, id).InstallationNamed(key.installation)
		signedIn = ok && checked.Authentication == "valid"
	}
	a.mu.Lock()
	st := a.signInLocked(key)
	st.running, st.stop = false, nil
	st.held, st.timedOut = !signedIn, timedOut
	a.mu.Unlock()
	if ctx.Err() != nil {
		return // the daemon stops
	}
	switch {
	case signedIn:
		a.emit(domain.Event{Type: domain.EventHarnessSignInCompleted, Harness: id, Severity: domain.SeverityInfo,
			Payload: with(nil)}.Says(domain.T("harness_sign_in_completed", "{{harness}} is signed in again", "harness", name)))
	case timedOut:
		a.emit(domain.Event{Type: domain.EventHarnessSignInTimedOut, Harness: id, Severity: domain.SeverityError,
			Payload: with(nil)}.Says(domain.T("harness_sign_in_timed_out",
			"the browser sign-in for {{harness}} was not completed within {{minutes}} minutes; press Sign in again to try again",
			"harness", name, "minutes", strconv.Itoa(int(timeout.Round(time.Minute)/time.Minute)))))
		a.detect(ctx, id) // its diagnostic now says the browser sign-in was not completed
	default:
		why := signInFailure(name, res)
		if res.Outcome == ports.SignInUnknownScreen {
			a.log.Printf("the %s sign-in stopped at a screen it does not recognize:\n%s", name, res.Screen)
		}
		a.emit(domain.Event{Type: domain.EventHarnessSignInFailed, Harness: id, Severity: domain.SeverityError,
			Payload: with(map[string]any{"outcome": string(res.Outcome)})}.Says(why))
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
