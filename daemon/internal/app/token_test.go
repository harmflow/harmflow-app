package app

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// tokenHarness is a fake Claude Code that takes a long-lived token. It reads signed in on its own sign-in when
// ownSignIn is set, and with any token UseToken set unless rejects is: then it reads signed out with one, as a CLI
// whose check tells a rejected token would.
type tokenHarness struct {
	*fakeHarness

	mu        sync.Mutex
	token     string
	uses      []string // each token UseToken set, in order
	ownSignIn bool
	rejects   bool
	// hold, when set, is the next check to hold once it has read its answer: it is closed to say so, and the check
	// returns once release is closed.
	hold, release chan struct{}
}

var _ ports.TokenSignIn = (*tokenHarness)(nil)

func newTokenHarness() *tokenHarness {
	return &tokenHarness{fakeHarness: &fakeHarness{id: domain.HarnessClaudeCode}}
}

func (h *tokenHarness) UseToken(token string) {
	h.mu.Lock()
	h.token = token
	h.uses = append(h.uses, token)
	h.mu.Unlock()
}

func (h *tokenHarness) Detect(ctx context.Context, manual string) ports.HarnessInfo {
	info := h.fakeHarness.Detect(ctx, manual)
	h.mu.Lock()
	defer h.mu.Unlock()
	if (h.token == "" && !h.ownSignIn) || (h.token != "" && h.rejects) {
		info.Readiness, info.Authentication = "not_ready", "invalid"
		info.SetDiagnostic(domain.T("claude_not_signed_in", "Claude Code is not signed in; run `claude` in a terminal and sign in"))
	}
	if hold, release := h.hold, h.release; hold != nil {
		h.hold = nil
		h.mu.Unlock()
		close(hold)
		<-release
		h.mu.Lock()
	}
	return info
}

// holdNextCheck holds the next check once it has read its answer; held is closed then, and release lets it return.
func (h *tokenHarness) holdNextCheck() (held <-chan struct{}, release func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hold, h.release = make(chan struct{}), make(chan struct{})
	r := h.release
	return h.hold, func() { close(r) }
}

func (h *tokenHarness) using() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.token
}

func (h *tokenHarness) set(own, rejects bool) {
	h.mu.Lock()
	h.ownSignIn, h.rejects = own, rejects
	h.mu.Unlock()
}

// fakeStore is a credential store in memory; setErr fails its writes.
type fakeStore struct {
	mu     sync.Mutex
	value  string
	setErr error
}

var errNoSecret = errors.New("no secret stored")

func (f *fakeStore) Get() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.value == "" {
		return "", errNoSecret
	}
	return f.value, nil
}

func (f *fakeStore) Set(token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setErr != nil {
		return f.setErr
	}
	f.value = token
	return nil
}

func (f *fakeStore) Delete() error {
	f.mu.Lock()
	f.value = ""
	f.mu.Unlock()
	return nil
}

func (f *fakeStore) Kind() string { return "keychain" }

func (f *fakeStore) held() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.value
}

// tokenEnv starts a daemon whose Claude Code is claude, and whose tokens are kept in store.
func tokenEnv(t *testing.T, claude *tokenHarness, store *fakeStore) *env {
	t.Helper()
	return newEnv(t, func(e *env) {
		e.signer = claude
		e.harnessTokens = func(string) ports.SecretStore { return store }
	})
}

// secretToken is a token the tests save; nothing under the daemon's home may hold it.
const secretToken = "sk-ant-oat01-test-token-0123456789"

// noTokenInHome fails the test for each file under the daemon's home that holds the token.
func noTokenInHome(t *testing.T, home string) {
	t.Helper()
	err := filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), secretToken) {
			t.Errorf("%s holds the token", path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A check that read Claude Code signed out before a token was saved, and returns after, does not reject that token:
// it never ran with it. The token stays in use, not marked rejected.
func TestACheckFromBeforeATokenDoesNotRejectIt(t *testing.T) {
	claude, store := newTokenHarness(), &fakeStore{}
	e := tokenEnv(t, claude, store)
	held, release := claude.holdNextCheck()
	checked := make(chan struct{})
	go func() {
		defer close(checked)
		e.app.detect(t.Context(), domain.HarnessClaudeCode)
	}()
	select {
	case <-held:
	case <-time.After(10 * time.Second):
		release()
		t.Fatal("no check read Claude Code within 10s, want the one started")
	}
	if _, err := e.app.SetHarnessToken(t.Context(), domain.HarnessClaudeCode, secretToken); err != nil {
		release()
		t.Fatal(err)
	}
	release()
	<-checked
	if info := e.claudeInfo(); info.Token == nil || info.Token.Rejected || claude.using() != secretToken {
		t.Errorf("after a check from before the token returned signed out, token %+v, in use %q; want the token, not "+
			"rejected, in use", info.Token, claude.using())
	}
}

// harmflow-app#733: a saved token goes to the credential store alone, and the harness's checks and sessions run with
// it, after a restart too; once removed, the harness is back on its own sign-in.
func TestHarnessTokenIsSavedUsedAndRemoved(t *testing.T) {
	claude, store := newTokenHarness(), &fakeStore{}
	e := tokenEnv(t, claude, store)
	if info := e.claudeInfo(); !info.TakesToken || info.Token != nil {
		t.Fatalf("before a token, takesToken %v and token %+v; want takesToken and no token", info.TakesToken, info.Token)
	}
	info, err := e.app.SetHarnessToken(t.Context(), domain.HarnessClaudeCode, " "+secretToken+"\n")
	if err != nil {
		t.Fatal(err)
	}
	saved := e.clock.Now()
	if info.Readiness != "ready" || info.Token == nil || !info.Token.SavedAt.Equal(saved) ||
		!info.Token.ExpiresAt.Equal(saved.Add(tokenLifetime)) || info.Token.Rejected {
		t.Errorf("SetHarnessToken = readiness %q, token %+v; want ready, saved now, expiring in a year", info.Readiness, info.Token)
	}
	if store.held() != secretToken || claude.using() != secretToken {
		t.Errorf("the store holds %q and the harness uses %q, want the token, trimmed, in both", store.held(), claude.using())
	}

	restarted := e.restart(t, func(n *env) {
		// As a new daemon's adapter starts with none. Only once the old daemon has stopped: a check of its own that ran
		// late would read the stand-in it shares with the new one signed out, and reject the token.
		claude.UseToken("")
		n.signer = claude
		n.harnessTokens = func(string) ports.SecretStore { return store }
	})
	if claude.using() != secretToken {
		t.Errorf("after a restart the harness uses %q, want the saved token", claude.using())
	}

	info, err = restarted.app.DeleteHarnessToken(t.Context(), domain.HarnessClaudeCode)
	if err != nil {
		t.Fatal(err)
	}
	if info.Token != nil || info.Authentication != "invalid" || store.held() != "" || claude.using() != "" {
		t.Errorf("after DeleteHarnessToken, token %+v, authentication %q, store %q, in use %q; want none, invalid",
			info.Token, info.Authentication, store.held(), claude.using())
	}
	restarted.app.Stop(domain.Text{Message: "test done"})
	noTokenInHome(t, e.home)
}

// harmflow-app#915: a check that began after the token was saved, and still runs when the daemon stops, reads the
// harness as that daemon left it, with the token in use. It does not reject the token, and the restarted daemon uses
// it.
func TestACheckRunningAtARestartLeavesTheTokenInUse(t *testing.T) {
	claude, store := newTokenHarness(), &fakeStore{}
	releaseStart := claude.fakeHarness.hold(t)
	e := tokenEnv(t, claude, store)
	// The start's own check began before the token: let it end first, so that the held check below is a later one.
	if !e.poll(10*time.Second, claude.fakeHarness.holding) {
		t.Fatal("the start's check of Claude Code never ran")
	}
	releaseStart()
	if !e.poll(10*time.Second, func() bool { return !claude.fakeHarness.holding() }) {
		t.Fatal("the start's check of Claude Code still waits once released")
	}
	if _, err := e.app.SetHarnessToken(t.Context(), domain.HarnessClaudeCode, secretToken); err != nil {
		t.Fatal(err)
	}
	releaseCheck := claude.fakeHarness.hold(t)
	e.app.RedetectHarnesses()
	if !e.poll(10*time.Second, claude.fakeHarness.holding) {
		t.Fatal("no check of Claude Code began after the token was saved")
	}
	// Stop ends the held check's context, and the check reads its answer then.
	restarted := e.restart(t, func(n *env) {
		releaseCheck()
		claude.UseToken("") // as a new daemon's adapter starts with none
		n.signer = claude
		n.harnessTokens = func(string) ports.SecretStore { return store }
	})
	if claude.using() != secretToken {
		t.Errorf("after a restart during a check the harness uses %q, want the saved token", claude.using())
	}
	if info := restarted.claudeInfo(); info.Token == nil || info.Token.Rejected {
		t.Errorf("after a restart during a check the token is %+v, want it saved and not rejected", info.Token)
	}
}

// harmflow-app#733: an attempt that ends signed out with a token in use rejects the token: it is no longer used, the
// daemon says so, and the harness is checked again on its own sign-in, whose own sign-in then starts. Saving a token
// again clears the mark.
func TestRejectedHarnessTokenFallsBack(t *testing.T) {
	claude, store := newTokenHarness(), &fakeStore{}
	e := tokenEnv(t, claude, store)
	if _, err := e.app.SetHarnessToken(t.Context(), domain.HarnessClaudeCode, secretToken); err != nil {
		t.Fatal(err)
	}
	e.create("rejected", "")
	claude.setScript(func(ports.RunRequest) string { return "echo 'END unavailable'" })
	r, err := e.app.RunTask("rejected")
	if err != nil {
		t.Fatal(err)
	}
	e.wait(r.ExecutionID, domain.StatusFailed)
	rejected := e.waitPublished(domain.EventHarnessTokenRejected)
	if rejected.MessageKey != "harness_token_rejected" || strings.Contains(rejected.Message, secretToken) {
		t.Errorf("%s = %+v, want keyed harness_token_rejected, without the token", domain.EventHarnessTokenRejected, rejected)
	}
	info := e.claudeInfo()
	if info.Token == nil || !info.Token.Rejected || info.Authentication != "invalid" || claude.using() != "" {
		t.Errorf("after the rejection, token %+v, authentication %q, in use %q; want rejected, invalid, none",
			info.Token, info.Authentication, claude.using())
	}
	if store.held() != secretToken {
		t.Errorf("the store holds %q after the rejection, want the token kept until it is replaced or removed", store.held())
	}
	if info, err = e.app.SetHarnessToken(t.Context(), domain.HarnessClaudeCode, secretToken+"-new"); err != nil ||
		info.Token == nil || info.Token.Rejected || info.Readiness != "ready" {
		t.Errorf("SetHarnessToken after a rejection = %+v, %v; want a token in use, ready", info.Token, err)
	}
}

// harmflow-app#792: an attempt that ends signed out rejects only the saved token it ran with. One that began on the
// harness's own sign-in, or with a token replaced while it ran, leaves the token saved meanwhile in use, not rejected.
// The attempt holds on a FIFO until the token is saved.
func TestAnAttemptRejectsOnlyTheTokenItRanWith(t *testing.T) {
	cases := []struct {
		name   string
		before string // the token saved before the attempt, "" for none
	}{
		{"no token", ""},
		{"a token since replaced", secretToken + "-old"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			claude, store := newTokenHarness(), &fakeStore{}
			claude.set(true, false) // signed in on its own sign-in, so an attempt with no token starts
			e := tokenEnv(t, claude, store)
			if c.before != "" {
				if _, err := e.app.SetHarnessToken(t.Context(), domain.HarnessClaudeCode, c.before); err != nil {
					t.Fatal(err)
				}
			}
			fifo := filepath.Join(t.TempDir(), "end")
			if err := syscall.Mkfifo(fifo, 0o600); err != nil {
				t.Fatalf("Mkfifo(%s): %v", fifo, err)
			}
			// Read-write, so the open does not wait for the shell; closing it at the end lets a held attempt end.
			feed, err := os.OpenFile(fifo, os.O_RDWR, 0)
			if err != nil {
				t.Fatalf("open %s: %v", fifo, err)
			}
			t.Cleanup(func() { _ = feed.Close() })
			claude.setScript(func(ports.RunRequest) string { return "read end < " + fifo + "; echo 'END unavailable'" })
			e.create("held", "")
			r, err := e.app.RunTask("held")
			if err != nil {
				t.Fatal(err)
			}
			e.prepared(claude.fakeHarness)
			if _, err := e.app.SetHarnessToken(t.Context(), domain.HarnessClaudeCode, secretToken); err != nil {
				t.Fatal(err)
			}
			if _, err := feed.WriteString("end\n"); err != nil {
				t.Fatalf("write %s: %v", fifo, err)
			}
			e.wait(r.ExecutionID, domain.StatusFailed)
			if got := e.published(domain.EventHarnessTokenRejected); len(got) != 0 {
				t.Errorf("%s events after the attempt = %+v, want none", domain.EventHarnessTokenRejected, got)
			}
			if info := e.claudeInfo(); info.Token == nil || info.Token.Rejected || claude.using() != secretToken {
				t.Errorf("after an attempt from before the token ended signed out, token %+v, in use %q; want the token "+
					"saved meanwhile, not rejected, in use", info.Token, claude.using())
			}
		})
	}
}

// harmflow-app#733: a check that reads the harness signed out with its token rejects the token, and checks it again on
// its own sign-in.
func TestCheckRejectsAHarnessToken(t *testing.T) {
	claude, store := newTokenHarness(), &fakeStore{}
	e := tokenEnv(t, claude, store)
	claude.set(true, false)
	if _, err := e.app.SetHarnessToken(t.Context(), domain.HarnessClaudeCode, secretToken); err != nil {
		t.Fatal(err)
	}
	claude.set(true, true)
	e.app.RefreshHarnesses(t.Context())
	if info := e.claudeInfo(); info.Token == nil || !info.Token.Rejected || info.Readiness != "ready" {
		t.Errorf("after a check that rejected the token, token %+v and readiness %q; want rejected, and ready on the "+
			"harness's own sign-in", info.Token, info.Readiness)
	}
}

// harmflow-app#733: a token is refused when it is empty or holds what no token holds, on a harness that takes none,
// on a daemon with no credential store, and when the store fails; nothing is saved then.
func TestHarnessTokenRefused(t *testing.T) {
	claude, store := newTokenHarness(), &fakeStore{}
	e := tokenEnv(t, claude, store)
	cases := []struct {
		name, harness, token, code string
		store                      func()
	}{
		{"empty", domain.HarnessClaudeCode, "  ", CodeValidationFailed, nil},
		{"a space inside", domain.HarnessClaudeCode, "sk-ant one", CodeValidationFailed, nil},
		{"a line break inside", domain.HarnessClaudeCode, "sk-ant\ndelete-keychain", CodeValidationFailed, nil},
		{"a harness that takes none", domain.HarnessCodex, secretToken, CodeConflict, nil},
		{"an unknown harness", "gemini-cli", secretToken, CodeNotFound, nil},
		{"a store that fails", domain.HarnessClaudeCode, secretToken, CodeConflict,
			func() { store.setErr = errors.New("locked") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.store != nil {
				c.store()
			}
			_, err := e.app.SetHarnessToken(t.Context(), c.harness, c.token)
			var ae *Error
			if !errors.As(err, &ae) || ae.Code != c.code {
				t.Errorf("SetHarnessToken(%q, %q) = %v, want code %s", c.harness, c.token, err, c.code)
			}
		})
	}
	if store.held() != "" || claude.using() != "" {
		t.Errorf("after refused tokens the store holds %q and the harness uses %q, want none", store.held(), claude.using())
	}
	if _, err := e.app.DeleteHarnessToken(t.Context(), domain.HarnessCodex); err == nil {
		t.Error("DeleteHarnessToken on a harness that takes none = nil, want an error")
	}

	none := newEnv(t, func(n *env) { n.signer = newTokenHarness() })
	if info := none.claudeInfo(); info.TakesToken {
		t.Error("a daemon with no credential store says Claude Code takes a token")
	}
	_, err := none.app.SetHarnessToken(t.Context(), domain.HarnessClaudeCode, secretToken)
	var ae *Error
	if !errors.As(err, &ae) || ae.Code != CodeConflict || ae.Key != "harness_token_store_unavailable" {
		t.Errorf("SetHarnessToken with no credential store = %v, want harness_token_store_unavailable", err)
	}
}

// harmflow-app#733: the daemon tells the user 30 days, then 7 days, before a token's year ends, each once; when both
// are due at once, only the nearest is sent.
func TestHarnessTokenExpiryNotices(t *testing.T) {
	claude, store := newTokenHarness(), &fakeStore{}
	e := tokenEnv(t, claude, store)
	if _, err := e.app.SetHarnessToken(t.Context(), domain.HarnessClaudeCode, secretToken); err != nil {
		t.Fatal(err)
	}
	expiring := func() []domain.Event { return e.published(domain.EventHarnessTokenExpiring) }
	e.app.noticeTokenExpiry()
	if n := len(expiring()); n != 0 {
		t.Fatalf("%d expiry notices on the day the token was saved, want none", n)
	}
	e.clock.Advance(tokenLifetime - 30*24*time.Hour + time.Hour)
	e.app.noticeTokenExpiry()
	e.app.noticeTokenExpiry()
	if got := expiring(); len(got) != 1 || got[0].Params["days"] != "29" || got[0].MessageKey != "harness_token_expiring" {
		t.Fatalf("expiry notices 30 days before = %+v, want one, 29 days left", got)
	}
	e.clock.Advance(23 * 24 * time.Hour)
	e.app.noticeTokenExpiry()
	if got := expiring(); len(got) != 2 || got[1].Params["days"] != "6" {
		t.Errorf("expiry notices 7 days before = %+v, want a second, 6 days left", got)
	}

	late := tokenEnv(t, newTokenHarness(), &fakeStore{})
	if _, err := late.app.SetHarnessToken(t.Context(), domain.HarnessClaudeCode, secretToken); err != nil {
		t.Fatal(err)
	}
	late.clock.Advance(tokenLifetime - 2*24*time.Hour)
	late.app.noticeTokenExpiry()
	if got := late.published(domain.EventHarnessTokenExpiring); len(got) != 1 || got[0].Params["days"] != "2" {
		t.Errorf("expiry notices with both due at once = %+v, want one, 2 days left", got)
	}
	var notice Notification
	for _, n := range late.app.Notifications(false) {
		if n.EventType == domain.EventHarnessTokenExpiring {
			notice = n
		}
	}
	if notice.Severity != "warning" || notice.TaskID != "" {
		t.Errorf("the expiry notice = %+v, want a warning of no task", notice)
	}
}
