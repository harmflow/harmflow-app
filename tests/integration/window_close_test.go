package integration

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// harmflow-app#464: closing the dashboard's real window, while an execution runs, follows the tray setting. With the
// tray on, the window only hides, the shell and its tray stay, and the tray's Open dashboard shows it again; with the
// tray off, the shell ends. In both, the daemon is the same process throughout, and the execution completes with no
// interruption, cancel, or stop in its log. The window is closed through the debug build's control channel, which
// raises the same close request as the window's close button, so the test needs no Accessibility permission.
func TestClosingTheWindowFollowsTheTraySetting(t *testing.T) {
	bin := realDesktop(t)
	for _, tray := range []bool{true, false} {
		t.Run(map[bool]string{true: "tray_on", false: "tray_off"}[tray], func(t *testing.T) {
			d := startDaemon(t, t.TempDir()).in(t)
			if !tray {
				d.call("PATCH", "/settings", map[string]any{"trayEnabled": false}, 200)
			}
			d.call("POST", "/tasks", map[string]any{"yaml": task("ticking", "SCENARIO=ticks", "")}, 201)
			health := d.call("GET", "/health", nil, 200)

			s := startShell(t, bin, d)
			s.waitState(true, tray)
			id := d.call("POST", "/tasks/ticking/run", nil, 202)["executionId"].(string)
			d.waitStatus(id, "running")

			// With the tray off, the shell may end on the close before it answers.
			s.act("close")
			if x := d.call("GET", "/executions/"+id, nil, 200); x["status"] != "running" {
				t.Errorf("the execution is %v right after the close, want running: it no longer spans the close", x["status"])
			}
			if tray {
				s.waitState(false, true)
				if !s.running() {
					t.Fatal("the shell ended on a close with the tray on")
				}
				s.send("open-dashboard")
				s.waitState(true, true)
			} else {
				select {
				case <-s.exited:
				case <-time.After(10 * time.Second):
					t.Fatal("the shell still runs 10s after a close with the tray off")
				}
			}

			d.waitStatus(id, "completed", "failed", "cancelled", "interrupted")
			if x := d.call("GET", "/executions/"+id, nil, 200); x["status"] != "completed" {
				t.Errorf("the execution ended %v, want completed", x["status"])
			}
			now := d.call("GET", "/health", nil, 200)
			if now["pid"] != health["pid"] || now["startedAt"] != health["startedAt"] {
				t.Errorf("GET /health = pid %v, startedAt %v, want the same daemon as before the shell started: pid %v, startedAt %v",
					now["pid"], now["startedAt"], health["pid"], health["startedAt"])
			}
			var types []string
			for _, line := range readLog(t, filepath.Join(d.home, "logs", id+".jsonl")) {
				types = append(types, line.Type)
			}
			for _, unwanted := range []string{"execution.interrupted", "execution.cancelled", "daemon.stopping"} {
				for _, got := range types {
					if got == unwanted {
						t.Errorf("the execution's log holds %s: %s", unwanted, strings.Join(types, ", "))
					}
				}
			}
		})
	}
}
