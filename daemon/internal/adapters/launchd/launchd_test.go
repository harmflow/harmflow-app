package launchd

import (
	"bytes"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// Autostart is off until Enable writes the user agent, which starts this daemon, with its data directory, at login.
// Each value the agent holds is escaped: the executable, an argument, the environment, and the log path. The agent is
// the user's own, in the given home's Library/LaunchAgents, and holds no key beyond the ones below: a UserName,
// GroupName, or InitGroups key would have launchd run the daemon as another user.
func TestEnableWritesTheUserAgent(t *testing.T) {
	var calls [][]string
	home := t.TempDir()
	a := NewAgent(home, filepath.Join(home, "logs <1>", "daemon.log"))
	a.Launchctl = func(args ...string) error { calls = append(calls, args); return nil }
	if want := filepath.Join(home, "Library", "LaunchAgents", Label+".plist"); a.PlistPath != want {
		t.Fatalf("NewAgent(%q, …).PlistPath = %q, want %q", home, a.PlistPath, want)
	}
	if a.Enabled() {
		t.Fatal("enabled before Enable")
	}

	exe := "/Applications/Tom & Jerry/HarmFlow.app/Contents/MacOS/harmflowd"
	args := []string{"--home", "/Users/me/Library/Application Support/R&D <data>", "--builtins", "/opt/harmflow/builtins"}
	env := map[string]string{"PATH": "/opt/homebrew/bin:/usr/bin:/bin", "HARMFLOW_TOKEN_STORE": "file&<keychain>"}
	if err := a.Enable(exe, args, env); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(a.PlistPath)
	if err != nil {
		t.Fatalf("Enable wrote no user agent: %v", err)
	}
	for _, raw := range []string{"Tom &amp; Jerry", "R&amp;D &lt;data&gt;", "file&amp;&lt;keychain&gt;", "logs &lt;1&gt;"} {
		if !bytes.Contains(data, []byte(raw)) {
			t.Errorf("the user agent does not hold %q escaped:\n%s", raw, data)
		}
	}
	plist, err := decodePlist(data)
	if err != nil {
		t.Fatalf("the user agent is not a property list: %v\n%s", err, data)
	}
	// launchd reads the file with the system's parser, which plutil runs.
	if plutil, err := exec.LookPath("plutil"); err == nil {
		if out, err := exec.Command(plutil, "-lint", a.PlistPath).CombinedOutput(); err != nil {
			t.Errorf("plutil rejects the user agent: %v: %s", err, out)
		}
	}
	want := map[string]any{
		"Label":                Label,
		"ProgramArguments":     []any{exe, args[0], args[1], args[2], args[3]},
		"EnvironmentVariables": map[string]any{"PATH": env["PATH"], "HARMFLOW_TOKEN_STORE": env["HARMFLOW_TOKEN_STORE"]},
		"RunAtLoad":            true,
		"KeepAlive":            map[string]any{"SuccessfulExit": false},
		"ProcessType":          "Standard",
		"StandardOutPath":      a.LogPath,
		"StandardErrorPath":    a.LogPath,
	}
	for key, v := range want {
		if got := plist[key]; !reflect.DeepEqual(got, v) {
			t.Errorf("%s: %#v, want %#v", key, got, v)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(plist)) {
		if _, ok := want[key]; !ok {
			t.Errorf("the user agent holds the key %s = %#v, which it should not:\n%s", key, plist[key], data)
		}
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
	if len(calls) > 0 {
		t.Errorf("launchctl called %d times, want none: %q", len(calls), calls)
	}
}

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

// harmflow-app#1042: the daemon's CODEX_HOME names a Codex installation, whose environment reaches no other process, so
// launchctl starts without it and keeps the daemon's other variables. The test builds the command and never runs it,
// so nothing reaches the user's launchd session.
func TestLaunchctlStartsWithoutCodexHome(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("HARMFLOW_TEST_KEPT", "kept")
	args := []string{"print", "gui/501/" + Label}

	cmd := launchctlCommand(args...)
	if want := append([]string{"/bin/launchctl"}, args...); cmd.Path != "/bin/launchctl" || !slices.Equal(cmd.Args, want) {
		t.Errorf("launchctlCommand(%q) = %s %q, want /bin/launchctl %q", args, cmd.Path, cmd.Args, want)
	}
	if cmd.Env == nil {
		t.Fatalf("launchctlCommand(%q).Env = nil, want the daemon's environment without CODEX_HOME", args)
	}
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "CODEX_HOME=") {
			t.Errorf("launchctlCommand(%q).Env holds %q, want no CODEX_HOME", args, kv)
		}
	}
	if !slices.Contains(cmd.Env, "HARMFLOW_TEST_KEPT=kept") {
		t.Errorf("launchctlCommand(%q).Env = %q, want HARMFLOW_TEST_KEPT=kept kept", args, cmd.Env)
	}
}

// harmflow-app#907: the user agent gives the daemon the Standard process class, and at start the daemon brings an agent
// an earlier version wrote up to date, so it takes that class at the next login. Refresh rewrites only an agent that
// starts this daemon, with the same program, arguments, and variables, and leaves any other as it is: a test or
// development daemon never takes over the user's agent. It writes no agent autostart did not turn on, and calls no
// launchctl, so the daemon running now goes on.
func TestRefreshRewritesOnlyThisDaemonsAgent(t *testing.T) {
	const logPath = "/logs/daemon.log"
	exe := "/Applications/HarmFlow.app/Contents/MacOS/harmflowd"
	args := []string{"--home", "/Users/me/Library/Application Support/HarmFlow"}
	env := map[string]string{"PATH": "/opt/homebrew/bin:/usr/bin:/bin", "HARMFLOW_TOKEN_STORE": "file"}
	current := renderPlist(exe, args, env, logPath)
	// background is the agent an earlier version wrote for a daemon: the same, with the Background process class.
	background := func(exe string, args []string, env map[string]string) string {
		plist := renderPlist(exe, args, env, logPath)
		return strings.Replace(plist, "<string>Standard</string>", "<string>Background</string>", 1)
	}
	tests := []struct {
		name      string
		agent     string // the agent on disk before Refresh; none when empty
		dir       bool   // a folder stands where the agent goes
		rewritten bool
		wantErr   bool
	}{
		{name: "a Background agent of this daemon", agent: background(exe, args, env), rewritten: true},
		{name: "an agent already current", agent: current},
		{name: "no agent"},
		{name: "an agent of another executable", agent: background("/Users/me/src/harmflow-app/build/harmflowd", args, env)},
		{name: "an agent of this executable with another home",
			agent: background(exe, []string{"--home", "/var/folders/xy/T/harmflow-test"}, env)},
		{name: "an agent of this executable with other arguments",
			agent: background(exe, append(slices.Clone(args), "--builtins", "/Users/me/src/harmflow-app"), env)},
		{name: "an agent with another value of a variable",
			agent: background(exe, args, map[string]string{"PATH": env["PATH"], "HARMFLOW_TOKEN_STORE": "keychain"})},
		{name: "an agent with a variable more", agent: background(exe, args,
			map[string]string{"PATH": env["PATH"], "HARMFLOW_TOKEN_STORE": "file", "HARMFLOW_KEYCHAIN": "/tmp/test.keychain"})},
		{name: "an agent with a variable less", agent: background(exe, args, map[string]string{"PATH": env["PATH"]})},
		{name: "an agent with no variables", agent: background(exe, args, nil)},
		{name: "an agent whose program holds a value that is no string",
			agent: strings.Replace(background(exe, args, env), "<string>--home</string>", "<true/>", 1)},
		{name: "an agent whose variables are no dict", agent: strings.Replace(background(exe, args, nil),
			"  <key>RunAtLoad</key>", "  <key>EnvironmentVariables</key>\n  <string>PATH=/bin</string>\n  <key>RunAtLoad</key>", 1)},
		{name: "an agent with a value no property list of this daemon holds", wantErr: true,
			agent: strings.Replace(background(exe, args, env), "<true/>", "<integer>1</integer>", 1)},
		{name: "an agent with a key and no value", wantErr: true,
			agent: strings.Replace(background(exe, args, env), "<key>RunAtLoad</key>\n  <true/>\n", "<key>RunAtLoad</key>\n", 1)},
		{name: "an agent cut short", wantErr: true, agent: current[:strings.Index(current, "<key>RunAtLoad</key>")]},
		{name: "a property list whose root is no dict", wantErr: true, agent: "<plist version=\"1.0\"><array/></plist>\n"},
		{name: "a file that is no property list", wantErr: true, agent: "not a property list\n"},
		{name: "a folder where the agent goes", dir: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls [][]string
			a := &Agent{PlistPath: filepath.Join(t.TempDir(), "LaunchAgents", Label+".plist"), LogPath: logPath,
				Launchctl: func(args ...string) error { calls = append(calls, args); return nil }}
			written := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
			if tt.dir {
				if err := os.MkdirAll(a.PlistPath, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if tt.agent != "" {
				if err := os.MkdirAll(filepath.Dir(a.PlistPath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(a.PlistPath, []byte(tt.agent), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(a.PlistPath, written, written); err != nil {
					t.Fatal(err)
				}
			}

			rewritten, err := a.Refresh(exe, args, env)
			if (err != nil) != tt.wantErr {
				t.Errorf("Refresh(…) error = %v, want an error %v", err, tt.wantErr)
			}
			if rewritten != tt.rewritten {
				t.Errorf("Refresh(…) = %v, want %v", rewritten, tt.rewritten)
			}
			data, err := os.ReadFile(a.PlistPath)
			switch {
			case tt.dir:
				if st, err := os.Stat(a.PlistPath); err != nil || !st.IsDir() {
					t.Errorf("Refresh replaced the folder where the agent goes: %v", err)
				}
			case tt.agent == "":
				if !os.IsNotExist(err) {
					t.Errorf("Refresh with no user agent wrote one: %v\n%s", err, data)
				}
			case tt.rewritten:
				if string(data) != current {
					t.Errorf("Refresh wrote:\n%s\nwant what Enable writes now:\n%s", data, current)
				}
				plist, err := decodePlist(data)
				if err != nil {
					t.Fatalf("the rewritten user agent is not a property list: %v", err)
				}
				if got := plist["ProcessType"]; got != "Standard" {
					t.Errorf("the rewritten user agent's ProcessType = %#v, want \"Standard\"", got)
				}
			default:
				if string(data) != tt.agent {
					t.Errorf("Refresh changed the user agent to:\n%s\nwant it as it was:\n%s", data, tt.agent)
				}
				st, err := os.Stat(a.PlistPath)
				if err != nil {
					t.Fatal(err)
				}
				if !st.ModTime().Equal(written) {
					t.Errorf("Refresh wrote the user agent again: modified %v, want %v", st.ModTime(), written)
				}
			}
			if len(calls) > 0 {
				t.Errorf("launchctl called %d times, want none: %q", len(calls), calls)
			}
		})
	}
}
