package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// signingHarness is a fake Claude Code, or Codex, the daemon can sign in. It reads signed out until a sign-in of it
// succeeds, and each sign-in waits for the test to say how it ends, through end, or for its context.
type signingHarness struct {
	*fakeHarness
	end chan ports.SignInResult // unbuffered: the test hands each sign-in its end
	// unverified makes its check unable to tell whether it is signed in: it reads unknown and ready, as the real adapter
	// does when its sign-in probe fails. Set before the daemon starts.
	unverified bool

	mu       sync.Mutex
	signedIn bool
	signIns  int                 // the sign-ins started
	dirs     []string            // the folder of each
	infos    []ports.HarnessInfo // the installation each signed in
	left     []time.Duration     // the time each one's context had left when it started; 0 for one with no deadline
}

var _ ports.SignInDriver = (*signingHarness)(nil)
var _ ports.HarnessAdapter = (*signingHarness)(nil)
var _ ports.ExtraUsageReporter = (*signingHarness)(nil)
var _ ports.QuotaReader = (*signingHarness)(nil)
var _ ports.SessionKeeper = (*signingHarness)(nil)

func newSigningHarness(signedIn bool) *signingHarness {
	return &signingHarness{fakeHarness: &fakeHarness{id: domain.HarnessClaudeCode}, end: make(chan ports.SignInResult),
		signedIn: signedIn}
}

// newCodexSigningHarness is a signingHarness read as Codex.
func newCodexSigningHarness(signedIn bool) *signingHarness {
	s := newSigningHarness(signedIn)
	s.fakeHarness = &fakeHarness{id: domain.HarnessCodex}
	return s
}

func (s *signingHarness) Detect(ctx context.Context, manual string) ports.HarnessInfo {
	info := s.fakeHarness.Detect(ctx, manual)
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.unverified:
		info.Authentication = "unknown"
		info.SetDiagnostic(domain.T("authentication_unverified", "authentication could not be verified"))
	case !s.signedIn && s.id == domain.HarnessCodex:
		info.Readiness, info.Authentication = "not_ready", "invalid"
		info.SetDiagnostic(domain.T("not_signed_in", "{{name}} is not signed in; run `{{command}}` in a terminal",
			"name", "Codex", "command", "codex login"))
	case !s.signedIn:
		info.Readiness, info.Authentication = "not_ready", "invalid"
		info.SetDiagnostic(domain.T("claude_not_signed_in", "Claude Code is not signed in; run `claude` in a terminal and sign in"))
	}
	return info
}

// SignIn signs in the installation it is given: a home or an entry's home the fake lists in signedOut, by its
// identifier or its home, reads signed in from then on, and any other installation is the one in use.
func (s *signingHarness) SignIn(ctx context.Context, info ports.HarnessInfo, dir string) ports.SignInResult {
	var left time.Duration
	if deadline, ok := ctx.Deadline(); ok {
		left = time.Until(deadline)
	}
	s.mu.Lock()
	s.signIns++
	s.dirs = append(s.dirs, dir)
	s.infos = append(s.infos, info)
	s.left = append(s.left, left)
	s.mu.Unlock()
	select {
	case res := <-s.end:
		if res.Outcome == ports.SignInSucceeded {
			s.fakeHarness.mu.Lock()
			own := s.signedOut[info.InstallationID] || info.Home != "" && s.signedOut[info.Home]
			delete(s.signedOut, info.InstallationID)
			delete(s.signedOut, info.Home)
			s.fakeHarness.mu.Unlock()
			if !own {
				s.mu.Lock()
				s.signedIn = true
				s.mu.Unlock()
			}
		}
		return res
	case <-ctx.Done():
		return ports.SignInResult{Outcome: ports.SignInStopped}
	}
}

func (s *signingHarness) started() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.signIns
}

// signedInto are the installations the sign-ins started for, in order.
func (s *signingHarness) signedInto() []ports.HarnessInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.infos)
}

// entrySigner is a signingHarness that takes custom entries, as entryHarness does.
type entrySigner struct{ *signingHarness }

var (
	_ ports.EntryProber       = entrySigner{}
	_ ports.HarnessIdentifier = entrySigner{}
)

func (s entrySigner) Identify(ctx context.Context, path string) error {
	return entryHarness{s.fakeHarness}.Identify(ctx, path)
}

func (s entrySigner) EntryExecutable(name string) string {
	return entryHarness{s.fakeHarness}.EntryExecutable(name)
}

func (s entrySigner) ProbeEntry(ctx context.Context, path string, assignments []string) ports.HarnessInfo {
	return entryHarness{s.fakeHarness}.ProbeEntry(ctx, path, assignments)
}

// homesSigningEnv starts a daemon whose Codex is a signingHarness, signed in where it is in use, with two homes,
// ~/.codex, in use, and ~/.codex-b, and the homes listed in signedOut read signed out.
func homesSigningEnv(t *testing.T, signedOut ...string) (*env, *signingHarness) {
	t.Helper()
	signer := newCodexSigningHarness(true)
	signer.homes = []string{"~/.codex", "~/.codex-b"}
	signer.signedOut = map[string]bool{}
	for _, h := range signedOut {
		signer.signedOut[h] = true
	}
	return signInEnv(t, signer, 0), signer
}

// homeNamed is the entry of Homes, or of CustomEntries, of the harness with the identifier.
func homeNamed(info ports.HarnessInfo, id string) ports.HarnessInfo {
	inst, _ := info.InstallationNamed(id)
	return inst
}

// signInEnv starts a daemon whose harness of signer's ID, Claude Code or Codex, is signer.
func signInEnv(t *testing.T, signer *signingHarness, timeout time.Duration) *env {
	t.Helper()
	return newEnv(t, func(e *env) { e.signer, e.signInTimeout = signer, timeout })
}

// waitSignIns waits until n sign-ins of signer started.
func (e *env) waitSignIns(signer *signingHarness, n int) {
	e.t.Helper()
	if !e.poll(10*time.Second, func() bool { return signer.started() >= n }) {
		e.t.Fatalf("%d sign-ins started, want %d", signer.started(), n)
	}
}

// waitPublished waits until the daemon published an event of the type, and returns the last one.
func (e *env) waitPublished(eventType string) domain.Event {
	e.t.Helper()
	var got []domain.Event
	if !e.poll(10*time.Second, func() bool { got = e.published(eventType); return len(got) > 0 }) {
		e.t.Fatalf("no %s event", eventType)
	}
	return got[len(got)-1]
}

func (e *env) claudeInfo() ports.HarnessInfo {
	return e.harnessInfo(domain.HarnessClaudeCode)
}

// harnessInfo is the harness of the ID as Harnesses() lists it.
func (e *env) harnessInfo(id string) ports.HarnessInfo {
	for _, h := range e.app.Harnesses() {
		if h.ID == id {
			return h
		}
	}
	return ports.HarnessInfo{}
}

// harmflow-app#543: a harness that says it is signed out starts its sign-in by itself, with a notice that the browser
// waits; once the harness says it is signed in and its check agrees, it is ready, and the sign-in says so.
func TestSignInStartsWhenSignedOutAndEndsSignedIn(t *testing.T) {
	signer := newSigningHarness(false)
	e := signInEnv(t, signer, 0)
	e.waitSignIns(signer, 1)
	started := e.waitPublished(domain.EventHarnessSignInStarted)
	if started.Harness != domain.HarnessClaudeCode || started.MessageKey != "harness_sign_in_started" {
		t.Errorf("%s = %+v, want Claude Code's, keyed harness_sign_in_started", domain.EventHarnessSignInStarted, started)
	}
	if got := e.claudeInfo().SignIn; got != ports.SignInRunning {
		t.Errorf("Harnesses() sign-in = %q while one runs, want %q", got, ports.SignInRunning)
	}
	var notice Notification
	if !e.poll(5*time.Second, func() bool {
		for _, n := range e.app.Notifications(false) {
			if n.EventType == domain.EventHarnessSignInStarted {
				notice = n
				return true
			}
		}
		return false
	}) || notice.TaskID != "" || notice.Params["harnessName"] != domain.HarnessClaudeCode || notice.Severity != "warning" {
		t.Errorf("sign-in notice = %+v, want a warning naming the harness and no task", notice)
	}
	signer.end <- ports.SignInResult{Outcome: ports.SignInSucceeded}
	e.waitPublished(domain.EventHarnessSignInCompleted)
	if info := e.claudeInfo(); info.Readiness != "ready" || info.SignIn != ports.SignInAvailable {
		t.Errorf("after the sign-in, readiness %q and sign-in %q; want ready and %q", info.Readiness, info.SignIn,
			ports.SignInAvailable)
	}
	if want := e.paths.Run + "/sign-in/" + domain.HarnessClaudeCode; signer.dirs[0] != want {
		t.Errorf("the sign-in ran in %q, want %q, under the daemon's runtime folder", signer.dirs[0], want)
	}
}

// harmflow-app#543: a sign-in nobody approves times out, the harness says the browser sign-in was not completed, and
// no sign-in starts again by itself, however often the harness is checked; the button starts one.
func TestSignInTimesOutAndWaitsForTheButton(t *testing.T) {
	signer := newSigningHarness(false)
	e := signInEnv(t, signer, 300*time.Millisecond)
	timedOut := e.waitPublished(domain.EventHarnessSignInTimedOut)
	if timedOut.MessageKey != "harness_sign_in_timed_out" {
		t.Errorf("%s = %+v, want keyed harness_sign_in_timed_out", domain.EventHarnessSignInTimedOut, timedOut)
	}
	var info ports.HarnessInfo
	if !e.poll(5*time.Second, func() bool { info = e.claudeInfo(); return info.DiagnosticKey == "harness_sign_in_not_completed" }) ||
		info.Readiness != "not_ready" {
		t.Errorf("after the timeout, readiness %q and diagnostic %q; want not_ready, harness_sign_in_not_completed",
			info.Readiness, info.DiagnosticKey)
	}
	e.app.RefreshHarnesses(t.Context())
	e.app.RefreshHarnesses(t.Context())
	time.Sleep(200 * time.Millisecond)
	if n := signer.started(); n != 1 {
		t.Fatalf("%d sign-ins after the timeout and two checks, want the first alone", n)
	}
	got, err := e.app.SignIn(domain.HarnessClaudeCode, "")
	if err != nil || got.SignIn != ports.SignInRunning {
		t.Fatalf("SignIn by hand = %+v, %v; want its sign-in running", got, err)
	}
	e.waitSignIns(signer, 2)
}

// harmflow-app#791: with no timeout set, a sign-in waits ten minutes for the browser: the context its adapter drives it
// under ends ten minutes after it started.
func TestSignInWaitsTenMinutesByDefault(t *testing.T) {
	waitsTenMinutesByDefault(t, newSigningHarness(false))
}

// harmflow-app#876: a Codex sign-in, with no timeout set, waits ten minutes for the browser, as Claude Code's does.
func TestCodexSignInWaitsTenMinutesByDefault(t *testing.T) {
	waitsTenMinutesByDefault(t, newCodexSigningHarness(false))
}

// waitsTenMinutesByDefault starts a daemon with no sign-in timeout set, whose signer reads signed out, and checks that
// the context of the sign-in it starts ends ten minutes after its start.
func waitsTenMinutesByDefault(t *testing.T, signer *signingHarness) {
	t.Helper()
	e := signInEnv(t, signer, 0)
	e.waitSignIns(signer, 1)
	signer.mu.Lock()
	left := signer.left[0]
	signer.mu.Unlock()
	if left == 0 {
		t.Fatal("the sign-in's context has no deadline, want one ten minutes after its start")
	}
	if d := left - 10*time.Minute; d < -time.Second || d > time.Second {
		t.Errorf("the sign-in's context had %v left at its start, want 10m within a second", left)
	}
}

// harmflow-app#543: a trigger while a sign-in runs — a check, an attempt signed out, the button — starts no second one.
func TestSignInRunsOneAtATime(t *testing.T) {
	signer := newSigningHarness(false)
	e := signInEnv(t, signer, 0)
	e.waitSignIns(signer, 1)
	e.app.RefreshHarnesses(t.Context())
	e.app.signInWhenSignedOut(domain.HarnessClaudeCode, "")
	if got, err := e.app.SignIn(domain.HarnessClaudeCode, ""); err != nil || got.SignIn != ports.SignInRunning {
		t.Errorf("SignIn by hand during a sign-in = %+v, %v; want the one running", got, err)
	}
	time.Sleep(200 * time.Millisecond)
	if n := signer.started(); n != 1 {
		t.Errorf("%d sign-ins started, want one", n)
	}
	signer.end <- ports.SignInResult{Outcome: ports.SignInSucceeded}
	e.waitPublished(domain.EventHarnessSignInCompleted)
}

// harmflow-app#543: a screen the adapter does not recognize ends the sign-in, which keeps the harness's own diagnostic
// and starts no other by itself until the daemon restarts.
func TestSignInStopsAtAnUnknownScreen(t *testing.T) {
	signer := newSigningHarness(false)
	e := signInEnv(t, signer, 0)
	e.waitSignIns(signer, 1)
	signer.end <- ports.SignInResult{Outcome: ports.SignInUnknownScreen, Screen: "Something new"}
	failed := e.waitPublished(domain.EventHarnessSignInFailed)
	if failed.MessageKey != "harness_sign_in_unknown_screen" || failed.Payload["outcome"] != string(ports.SignInUnknownScreen) {
		t.Errorf("%s = %+v, want keyed harness_sign_in_unknown_screen, outcome unknown_screen", domain.EventHarnessSignInFailed, failed)
	}
	e.app.RefreshHarnesses(t.Context())
	if info := e.claudeInfo(); info.DiagnosticKey != "claude_not_signed_in" {
		t.Errorf("diagnostic %q after an unknown screen, want the harness's own claude_not_signed_in", info.DiagnosticKey)
	}
	time.Sleep(200 * time.Millisecond)
	if n := signer.started(); n != 1 {
		t.Fatalf("%d sign-ins after an unknown screen and a check, want one", n)
	}
	restarted := e.restart(t, func(n *env) { n.signer = signer })
	restarted.waitSignIns(signer, 2)
}

// harmflow-app#543: an attempt that ends with the harness's words that it is signed out starts the sign-in for the
// next executions, while the execution fails as it would without it.
func TestSignedOutAttemptStartsTheSignIn(t *testing.T) {
	signer := newSigningHarness(true)
	e := signInEnv(t, signer, 0)
	e.create("signed-out", "")
	signer.setScript(func(ports.RunRequest) string { return "echo 'END unavailable'" })
	r, err := e.app.RunTask("signed-out")
	if err != nil {
		t.Fatal(err)
	}
	e.wait(r.ExecutionID, domain.StatusFailed)
	e.waitSignIns(signer, 1)
	signer.end <- ports.SignInResult{Outcome: ports.SignInSucceeded}
	e.waitPublished(domain.EventHarnessSignInCompleted)
}

// harmflow-app#791: a Claude Code whose check cannot tell whether it is signed in reads unknown and stays ready, and
// the daemon starts no sign-in for it: not once the daemon starts, not at a check, and not when a task runs on it. Only
// a harness that says it is signed out gets one.
func TestUnknownSignInStartsNone(t *testing.T) {
	unknownSignInStartsNone(t, newSigningHarness(false), taskYAML("unknown", ""))
}

// harmflow-app#876: a Codex whose check cannot tell whether it is signed in reads unknown and stays ready, and the
// daemon starts no sign-in for it, as for Claude Code: not once the daemon starts, not at a check, and not when a task
// runs on it.
func TestCodexUnknownSignInStartsNone(t *testing.T) {
	unknownSignInStartsNone(t, newCodexSigningHarness(false), codexTaskYAML("unknown", ""))
}

// unknownSignInStartsNone starts a daemon whose signer cannot tell whether it is signed in, and checks that it reads
// unknown, ready, and its sign-in available, with no sign-in started or announced: once the daemon started, after two
// checks, and after the task "unknown", which yaml defines on signer, completed.
func unknownSignInStartsNone(t *testing.T, signer *signingHarness, yaml string) {
	t.Helper()
	signer.unverified = true
	e := signInEnv(t, signer, 0)
	noSignIn := func(when string) {
		t.Helper()
		time.Sleep(200 * time.Millisecond) // a sign-in starts on a goroutine of its own
		info := e.harnessInfo(signer.ID())
		if info.Authentication != "unknown" || info.Readiness != "ready" || info.DiagnosticKey != "authentication_unverified" {
			t.Errorf("%s, authentication %q, readiness %q, diagnostic %q; want unknown, ready, authentication_unverified",
				when, info.Authentication, info.Readiness, info.DiagnosticKey)
		}
		if info.SignIn != ports.SignInAvailable {
			t.Errorf("%s, Harnesses() sign-in = %q, want %q", when, info.SignIn, ports.SignInAvailable)
		}
		if n := signer.started(); n != 0 {
			t.Errorf("%s, %d sign-ins started, want none", when, n)
		}
		if got := e.published(domain.EventHarnessSignInStarted); len(got) > 0 {
			t.Errorf("%s, %s events %+v, want none", when, domain.EventHarnessSignInStarted, got)
		}
	}
	if !e.poll(10*time.Second, func() bool { return e.harnessInfo(signer.ID()).Readiness != "checking" }) {
		t.Fatalf("%s's first check never ended", signer.ID())
	}
	noSignIn("after the daemon started")
	e.app.RefreshHarnesses(t.Context())
	e.app.RefreshHarnesses(t.Context())
	noSignIn("after two checks")
	if _, err := e.app.CreateTask(t.Context(), TaskInput{YAML: yaml}); err != nil {
		t.Fatalf("create unknown: %v", err)
	}
	r, err := e.app.RunTask("unknown")
	if err != nil {
		t.Fatal(err)
	}
	x := e.wait(r.ExecutionID, domain.StatusCompleted)
	if len(x.Attempts) != 1 || x.Attempts[0].Harness != signer.ID() {
		t.Errorf("attempts %+v, want one, on %s", x.Attempts, signer.ID())
	}
	noSignIn("after a task ran on it")
}

// harmflow-app#791: an execution never waits for the sign-in. Whether Claude Code reads signed out before the task
// fires or says so at the end of its attempt, the execution goes on to its Codex fallback and completes there while the
// one sign-in still runs, which the test ends only after the execution has.
func TestFailoverBesideARunningSignIn(t *testing.T) {
	for _, c := range []struct {
		name     string
		signedIn bool     // whether Claude Code reads signed in when the task fires
		harness  []string // the harness of each attempt
	}{
		{"signed out at the check", false, []string{domain.HarnessCodex}},
		{"signed out during the attempt", true, []string{domain.HarnessClaudeCode, domain.HarnessCodex}},
	} {
		t.Run(c.name, func(t *testing.T) {
			signer := newSigningHarness(c.signedIn)
			e := signInEnv(t, signer, 0)
			e.create("beside", codexFallback)
			if c.signedIn {
				signer.setScript(func(ports.RunRequest) string { return "echo working; echo 'END unavailable'" })
			} else {
				e.waitSignIns(signer, 1)
			}
			r, err := e.app.RunTask("beside")
			if err != nil {
				t.Fatal(err)
			}
			x := e.wait(r.ExecutionID, domain.StatusCompleted)
			var harness []string
			for _, at := range x.Attempts {
				harness = append(harness, at.Harness)
			}
			if !slices.Equal(harness, c.harness) || x.Attempts[len(x.Attempts)-1].Outcome != domain.AttemptSucceeded {
				t.Errorf("attempts %+v, want attempts on %v, the last succeeding", x.Attempts, c.harness)
			}
			// An attempt that ends signed out switches to the fallback once.
			if counts, _ := e.eventCounts(r.ExecutionID); c.signedIn &&
				(x.HarnessSwitches != 1 || counts[domain.EventHarnessSwitched] != 1) {
				t.Errorf("%d switches and %d %s events, want one", x.HarnessSwitches, counts[domain.EventHarnessSwitched],
					domain.EventHarnessSwitched)
			}
			e.waitSignIns(signer, 1)
			if n := signer.started(); n != 1 {
				t.Errorf("%d sign-ins started once the execution ended, want one", n)
			}
			if got := e.claudeInfo().SignIn; got != ports.SignInRunning {
				t.Errorf("Harnesses() sign-in = %q once the execution ended, want %q", got, ports.SignInRunning)
			}
			signer.end <- ports.SignInResult{Outcome: ports.SignInSucceeded}
			e.waitPublished(domain.EventHarnessSignInCompleted)
		})
	}
}

// harmflow-app#543: a sign-in the daemon's stop cuts short ends with it and announces nothing more.
func TestStopEndsASignIn(t *testing.T) {
	signer := newSigningHarness(false)
	e := signInEnv(t, signer, 0)
	e.waitSignIns(signer, 1)
	e.app.Stop(domain.Text{Message: "test stops"})
	for _, kind := range []string{domain.EventHarnessSignInCompleted, domain.EventHarnessSignInTimedOut,
		domain.EventHarnessSignInFailed} {
		if got := e.published(kind); len(got) > 0 {
			t.Errorf("%s after the stop: %+v", kind, got)
		}
	}
}

// harmflow-app#1136: a sign-in by hand for a detected home that is not in use runs with that home, shows running on
// that home alone, and once it signs the home in, its events name the installation; the installation in use is left
// as it was.
func TestSignInOfADetectedHomeRunsWithItsHome(t *testing.T) {
	e, signer := homesSigningEnv(t)
	if !e.poll(10*time.Second, func() bool { return len(e.harnessInfo(domain.HarnessCodex).Homes) == 2 }) {
		t.Fatal("Codex never listed its two homes")
	}
	got, err := e.app.SignIn(domain.HarnessCodex, "~/.codex-b")
	if err != nil || homeNamed(got, "~/.codex-b").SignIn != ports.SignInRunning || got.SignIn != ports.SignInAvailable ||
		homeNamed(got, "~/.codex").SignIn != ports.SignInAvailable {
		t.Fatalf("SignIn(codex, ~/.codex-b) = %+v, %v; want ~/.codex-b running, the installation in use available", got, err)
	}
	e.waitSignIns(signer, 1)
	if inst := signer.signedInto()[0]; inst.InstallationID != "~/.codex-b" || inst.Home != "/homes/~/.codex-b" {
		t.Errorf("the sign-in ran with installation %q, home %q; want ~/.codex-b, /homes/~/.codex-b", inst.InstallationID,
			inst.Home)
	}
	if dir := signer.dirs[0]; dir == e.paths.Run+"/sign-in/"+domain.HarnessCodex {
		t.Errorf("the sign-in of ~/.codex-b ran in %q, the folder of the installation in use's", dir)
	}
	started := e.waitPublished(domain.EventHarnessSignInStarted)
	if started.Payload["installationId"] != "~/.codex-b" {
		t.Errorf("%s payload %+v, want installationId ~/.codex-b", domain.EventHarnessSignInStarted, started.Payload)
	}
	signer.end <- ports.SignInResult{Outcome: ports.SignInSucceeded}
	completed := e.waitPublished(domain.EventHarnessSignInCompleted)
	if completed.Payload["installationId"] != "~/.codex-b" {
		t.Errorf("%s payload %+v, want installationId ~/.codex-b", domain.EventHarnessSignInCompleted, completed.Payload)
	}
	if n := signer.started(); n != 1 {
		t.Errorf("%d sign-ins started, want the one of ~/.codex-b alone", n)
	}
}

// harmflow-app#1136: an installation the harness does not list is not found, and starts no sign-in.
func TestSignInOfAnUnknownInstallationIsNotFound(t *testing.T) {
	e, signer := homesSigningEnv(t)
	if !e.poll(10*time.Second, func() bool { return len(e.harnessInfo(domain.HarnessCodex).Homes) == 2 }) {
		t.Fatal("Codex never listed its two homes")
	}
	_, err := e.app.SignIn(domain.HarnessCodex, "~/.codex-none")
	var appErr *Error
	if !errors.As(err, &appErr) || appErr.Code != CodeNotFound {
		t.Errorf("SignIn(codex, ~/.codex-none) error = %v, want %s", err, CodeNotFound)
	}
	time.Sleep(200 * time.Millisecond)
	if n := signer.started(); n != 0 {
		t.Errorf("%d sign-ins started, want none", n)
	}
}

// harmflow-app#1136: a detected home that reads signed out starts its own sign-in, with its own home, beside the
// installation in use, which starts none; a second request for the same home joins it, and the sign-in of the
// installation in use runs side by side with it. A home whose sign-in fails holds back only its own.
func TestSignInsOfInstallationsRunSideBySide(t *testing.T) {
	e, signer := homesSigningEnv(t, "~/.codex-b")
	e.waitSignIns(signer, 1)
	if inst := signer.signedInto()[0]; inst.InstallationID != "~/.codex-b" || inst.Home != "/homes/~/.codex-b" {
		t.Fatalf("the sign-in started for installation %q, home %q; want ~/.codex-b's own", inst.InstallationID, inst.Home)
	}
	if got, err := e.app.SignIn(domain.HarnessCodex, "~/.codex-b"); err != nil ||
		homeNamed(got, "~/.codex-b").SignIn != ports.SignInRunning {
		t.Errorf("SignIn(codex, ~/.codex-b) during its sign-in = %+v, %v; want it joined, running", got, err)
	}
	if _, err := e.app.SignIn(domain.HarnessCodex, ""); err != nil {
		t.Fatalf("SignIn(codex) error = %v, want none", err)
	}
	e.waitSignIns(signer, 2)
	time.Sleep(200 * time.Millisecond)
	if n := signer.started(); n != 2 {
		t.Errorf("%d sign-ins started, want two: ~/.codex-b's and the installation in use's", n)
	}
	info := e.harnessInfo(domain.HarnessCodex)
	if info.SignIn != ports.SignInRunning || homeNamed(info, "~/.codex-b").SignIn != ports.SignInRunning {
		t.Errorf("Harnesses() sign-ins %q in use, %q on ~/.codex-b; want both running", info.SignIn,
			homeNamed(info, "~/.codex-b").SignIn)
	}
	signer.end <- ports.SignInResult{Outcome: ports.SignInUnknownScreen}
	signer.end <- ports.SignInResult{Outcome: ports.SignInSucceeded}
	if !e.poll(10*time.Second, func() bool {
		return len(e.published(domain.EventHarnessSignInFailed)) > 0 &&
			len(e.published(domain.EventHarnessSignInCompleted)) > 0
	}) {
		t.Fatal("the two sign-ins never ended, one failed and one completed")
	}
	// Which of the two took which end does not matter: the one that failed holds back its own installation, and the
	// other reads signed in, so a check starts neither again.
	e.app.RefreshHarnesses(t.Context())
	time.Sleep(200 * time.Millisecond)
	if n := signer.started(); n != 2 {
		t.Errorf("%d sign-ins after a check, want the two alone: a failed one holds back its own installation", n)
	}
}

// harmflow-app#1136: a custom entry that reads signed out starts its own sign-in, run with the entry's assignments and
// home, whose events and texts name the entry by its name, never by what it assigns; once it signs the entry in, the
// entry reads signed in, and the installation in use is left as it was.
func TestSignedOutCustomEntryStartsItsOwnSignIn(t *testing.T) {
	signer := newCodexSigningHarness(true)
	signer.homes = []string{"~/.codex"}
	signer.signedOut = map[string]bool{"/entry-home": true}
	bins := t.TempDir()
	codexBin := filepath.Join(bins, "codex")
	if err := os.WriteFile(codexBin, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("WriteFile(%s) error = %v, want none", codexBin, err)
	}
	e := newEnv(t, func(e *env) { e.signer, e.signInTimeout = entrySigner{signer}, 0 })
	info, err := e.app.AddCustomEntry(t.Context(), domain.HarnessCodex, CustomEntryInput{
		Line: ptr("CODEX_HOME=/entry-home SECRET_MARK=hidden " + codexBin), Name: ptr("Second account")})
	if err != nil || len(info.CustomEntries) != 1 {
		t.Fatalf("AddCustomEntry = %+v, %v; want one entry", info.CustomEntries, err)
	}
	id := info.CustomEntries[0].InstallationID
	e.waitSignIns(signer, 1)
	inst := signer.signedInto()[0]
	if inst.InstallationID != id || inst.Home != "/entry-home" ||
		!slices.Equal(inst.EntryEnv, []string{"CODEX_HOME=/entry-home", "SECRET_MARK=hidden"}) {
		t.Errorf("the sign-in ran with installation %q, home %q, assignments %q; want %s with its own home and assignments",
			inst.InstallationID, inst.Home, inst.EntryEnv, id)
	}
	started := e.waitPublished(domain.EventHarnessSignInStarted)
	if started.Payload["installationId"] != id || started.Payload["entryName"] != "Second account" ||
		started.Params["harness"] != "Second account" {
		t.Errorf("%s = %+v, want the entry's identifier and name", domain.EventHarnessSignInStarted, started)
	}
	if strings.Contains(started.Message, "hidden") || strings.Contains(fmt.Sprint(started.Payload), "hidden") {
		t.Errorf("%s = %+v holds a value the entry assigns", domain.EventHarnessSignInStarted, started)
	}
	signer.end <- ports.SignInResult{Outcome: ports.SignInSucceeded}
	e.waitPublished(domain.EventHarnessSignInCompleted)
	got := e.harnessInfo(domain.HarnessCodex)
	if entry := homeNamed(got, id); entry.Authentication != "valid" || entry.SignIn != ports.SignInAvailable {
		t.Errorf("after its sign-in, the entry reads authentication %q, sign-in %q; want valid, available",
			entry.Authentication, entry.SignIn)
	}
	if n := signer.started(); n != 1 {
		t.Errorf("%d sign-ins started, want the entry's alone", n)
	}
}
