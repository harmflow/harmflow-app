package integration

import (
	"net/http"
	"testing"
	"time"
)

// showFallback is how long a normal start waits for the interface's readiness before it shows the window anyway: the
// shell's window::SHOW_FALLBACK.
const showFallback = 10 * time.Second

// waitDecided waits until the interface's readiness or the fallback has decided the start's show, as the shell's state
// says. A hidden start schedules no fallback, so until the test sends fallback-show, only the interface's readiness
// decides it.
func (s *shell) waitDecided(within time.Duration) {
	s.t.Helper()
	var state map[string]any
	for deadline := time.Now().Add(within); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if state = s.send("state"); state["startShowDecided"] == true {
			return
		}
	}
	s.t.Fatalf("the shell's state is %v after %v, want startShowDecided true: the interface never said it was ready",
		state, within)
}

// keepsHidden fails the test unless, at every read for the duration d, the window is hidden with the tray enabled and
// macOS shows the tray's status item. A tray with no status item fails the read itself.
func (s *shell) keepsHidden(d time.Duration) {
	s.t.Helper()
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
		if state := s.send("state"); state["visible"] != false || state["trayEnabled"] != true {
			s.t.Fatalf("the shell's state is %v, want the window hidden and the tray enabled throughout %v", state, d)
		}
		if tray := s.send("tray"); tray["visible"] != true {
			s.t.Fatalf("the tray reads %v, want its status item shown throughout %v", tray, d)
		}
	}
}

// harmflow-app#508: a hidden start, the one the login item makes with --hidden, keeps the dashboard's real window
// hidden with the tray up, through the interface's readiness and the fallback's show, until the tray's Open dashboard
// shows it; a normal start shows it once, on the interface's readiness. The test reads the window's visibility, the
// start's decided show, and whether macOS shows the tray's status item through the debug build's control channel,
// which also sends Open dashboard as a click on the tray's item does, so it needs no Accessibility permission. A hidden
// start schedules no fallback, so the channel's fallback-show calls what the fallback calls, as does a normal start's
// once its ten seconds elapse.
//
// Each of the two paths that keep a hidden start hidden gets its own start. Before the interface is served, nothing
// says it is ready, so fallback-show is the first to decide the show, and only the hidden start stops it. With the
// interface served, its readiness decides the show first, the hidden start alone stopping it, and fallback-show then
// finds the show decided. The normal start shows the window on the interface's readiness, before the fallback could;
// once the test hides it, neither fallback-show nor the start's own fallback shows it again. The run spends no usage
// and starts no harness.
func TestAHiddenStartKeepsTheWindowHiddenWithTheTrayUp(t *testing.T) {
	bin := realDesktop(t)
	d := startDaemon(t, t.TempDir())

	t.Run("hidden_before_the_interface", func(t *testing.T) {
		if resp, err := http.Get(dashboardURL); err == nil {
			_ = resp.Body.Close()
			t.Fatalf("something answers at %s: stop it, the shell's debug build would load it and say it is ready",
				dashboardURL)
		}
		s := startShell(t, bin, d.in(t), "--hidden")
		s.keepsHidden(time.Second)
		if state := s.send("state"); state["startShowDecided"] != false {
			t.Fatalf("the shell's state is %v before any fallback-show, want startShowDecided false: no interface is "+
				"served, and a hidden start schedules no fallback", state)
		}
		s.send("fallback-show")
		if state := s.send("state"); state["startShowDecided"] != true {
			t.Fatalf("the shell's state is %v after fallback-show, want startShowDecided true", state)
		}
		s.keepsHidden(2 * time.Second)
	})

	serveDashboard(t, d)

	t.Run("hidden", func(t *testing.T) {
		s := startShell(t, bin, d.in(t), "--hidden")
		s.keepsHidden(time.Second)
		// The interface loads in the hidden window and says it is ready.
		s.waitDecided(60 * time.Second)
		s.keepsHidden(2 * time.Second)
		s.send("fallback-show")
		s.keepsHidden(2 * time.Second)

		s.send("choose-tray open")
		s.waitState(true, true)
	})

	t.Run("normal", func(t *testing.T) {
		start := time.Now()
		s := startShell(t, bin, d.in(t))
		s.waitState(true, true)
		if shown := time.Since(start); shown >= showFallback {
			t.Fatalf("the window showed %v after the start, when the fallback shows it: the interface's readiness did "+
				"not show it", shown.Round(100*time.Millisecond))
		}
		// With the tray on, closing the window only hides it.
		s.send("close")
		s.waitState(false, true)
		s.send("fallback-show")
		s.keepsHidden(2 * time.Second)
		// The start's own fallback elapses meanwhile, and changes nothing either.
		if left := time.Until(start.Add(showFallback + 2*time.Second)); left > 0 {
			s.keepsHidden(left)
		}
	})
}
