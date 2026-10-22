package launchd

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// harmflow-app#81: turning autostart on or off changes only what the next login starts. Enable writes the user agent
// and Disable deletes it; neither asks launchctl to stop the job, which is the daemon serving the request when launchd
// started it.
func TestAutostartLeavesTheRunningDaemon(t *testing.T) {
	var calls [][]string
	a := &Agent{PlistPath: filepath.Join(t.TempDir(), "LaunchAgents", Label+".plist"), LogPath: "/logs/daemon.log",
		Launchctl: func(args ...string) error { calls = append(calls, args); return nil }}
	if a.Enabled() {
		t.Fatal("enabled before Enable")
	}

	if err := a.Enable("/Applications/HarmFlow.app/harmflowd", []string{"--home", "/data"}, map[string]string{"PATH": "/bin"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(a.PlistPath)
	if err != nil {
		t.Fatalf("Enable wrote no user agent: %v", err)
	}
	if !strings.Contains(string(data), "<string>/Applications/HarmFlow.app/harmflowd</string>") {
		t.Errorf("the user agent does not start the daemon:\n%s", data)
	}
	if !a.Enabled() {
		t.Error("not enabled after Enable")
	}

	if err := a.Disable(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.PlistPath); !os.IsNotExist(err) {
		t.Errorf("Disable left the user agent: %v", err)
	}
	if a.Enabled() {
		t.Error("enabled after Disable")
	}
	if err := a.Disable(); err != nil {
		t.Errorf("Disable with no user agent: %v", err)
	}

	for _, call := range calls {
		if len(call) > 0 && slices.Contains([]string{"bootout", "remove", "unload", "kill"}, call[0]) {
			t.Errorf("launchctl %s stops the running daemon", strings.Join(call, " "))
		}
	}
	if len(calls) > 0 {
		t.Errorf("launchctl called %d times, want none: %q", len(calls), calls)
	}
}
