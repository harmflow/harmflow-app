package integration

import (
	"fmt"
	"testing"
	"time"
)

// harmflow-app#655: each way to quit the running desktop follows the preference that keeps HarmFlow in the menu bar,
// and none of them stops the daemon. With the tray on, the app menu's Quit (⌘Q) ends the shell, the tray with it, when
// the preference is off, and only hides the window when it is on; the Dock's Quit and the tray's Quit interface end the
// shell whatever the preference. The app menu is read as macOS shows it: its last item is HarmFlow's own Quit on ⌘Q,
// and no item sends macOS's terminate:, which no handler of the app sees. The test drives the shell through the debug
// build's control channel, so it needs no Accessibility or Automation permission. The Dock's Quit is macOS's terminate:
// request, which the channel sends inside the app from the run loop, where macOS delivers it (harmflow-app#805).
func TestEachWayToQuitFollowsThePreference(t *testing.T) {
	bin := realDesktop(t)
	for _, c := range []struct {
		name    string
		keep    bool   // the preference: ⌘Q keeps HarmFlow in the menu bar
		command string // the control channel's command that quits
		hides   bool   // whether the quit only hides the window, the shell and its tray still running
	}{
		{"app_menu_quit_preference_off", false, "choose-app-menu app:quit", false},
		{"app_menu_quit_preference_on", true, "choose-app-menu app:quit", true},
		{"dock_quit_preference_on", true, "terminate", false},
		{"tray_quit_interface_preference_on", true, "choose-tray quit", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := startDaemon(t, t.TempDir()).in(t)
			// The shell reads both settings from the daemon at start: a debug build loads no interface to send them.
			d.call("PATCH", "/settings", map[string]any{"trayEnabled": true, "keepTrayOnQuit": c.keep}, 200)
			health := d.call("GET", "/health", nil, 200)

			s := startShell(t, bin, d)
			s.waitState(true, true)
			s.waitKeepTrayOnQuit(c.keep)
			checkAppMenu(t, s.send("app-menu"))

			s.act(c.command)
			if c.hides {
				s.waitState(false, true)
				if !s.running() {
					t.Fatalf("%s ended the shell with the tray and the preference on", c.command)
				}
				s.send("open-dashboard")
				s.waitState(true, true)
			} else {
				select {
				case <-s.exited:
				case <-time.After(10 * time.Second):
					t.Fatalf("the shell still runs 10s after %s", c.command)
				}
			}

			now := d.call("GET", "/health", nil, 200)
			if now["pid"] != health["pid"] || now["startedAt"] != health["startedAt"] {
				t.Errorf("GET /health after %s = pid %v, startedAt %v, want the daemon from before: pid %v, startedAt %v",
					c.command, now["pid"], now["startedAt"], health["pid"], health["startedAt"])
			}
		})
	}
}

// waitKeepTrayOnQuit waits until the shell holds the preference as keep says: it reads it from the daemon at start,
// after the window may already show.
func (s *shell) waitKeepTrayOnQuit(keep bool) {
	s.t.Helper()
	var state map[string]any
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if state = s.send("state"); state["keepTrayOnQuit"] == keep {
			return
		}
	}
	s.t.Fatalf("the shell's state is %v, want keepTrayOnQuit %v", state, keep)
}

// checkAppMenu checks the app menu macOS shows, as the control channel's app-menu answers it: its last item, past the
// separators, is HarmFlow's Quit on ⌘Q, and no item sends terminate:, which the predefined Quit sends.
func checkAppMenu(t *testing.T, answer map[string]any) {
	t.Helper()
	items, _ := answer["items"].([]any)
	var last map[string]any
	for _, it := range items {
		item, _ := it.(map[string]any)
		if item["action"] == "terminate:" {
			t.Errorf("app-menu: item %v sends terminate:, want no predefined Quit", item)
		}
		if item["separator"] != true {
			last = item
		}
	}
	if last == nil {
		t.Fatalf("app-menu = %v, want items", answer)
	}
	if last["id"] != "app:quit" || last["key"] != "q" || fmt.Sprint(last["modifiers"]) != "[command]" {
		t.Errorf("app-menu: last item = %v, want id app:quit on key q with modifiers [command]", last)
	}
}
