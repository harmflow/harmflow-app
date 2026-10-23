package launchd

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// FS-010 FR-002 (D-008, D-062): autostart is off until Enable writes the user agent, which starts this daemon, with
// its data directory, at login. Each value the agent holds is escaped: the executable, an argument, the environment,
// and the log path.
func TestEnableWritesTheUserAgent(t *testing.T) {
	var calls [][]string
	dir := t.TempDir()
	a := &Agent{PlistPath: filepath.Join(dir, "LaunchAgents", Label+".plist"),
		LogPath:   filepath.Join(dir, "logs <1>", "daemon.log"),
		Launchctl: func(args ...string) error { calls = append(calls, args); return nil }}
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
		"StandardOutPath":      a.LogPath,
		"StandardErrorPath":    a.LogPath,
	}
	for key, v := range want {
		if got := plist[key]; !reflect.DeepEqual(got, v) {
			t.Errorf("%s: %#v, want %#v", key, got, v)
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

// decodePlist reads an XML property list whose root is a dictionary: a dict is a map, an array a slice, a string a
// string, and true and false a bool.
func decodePlist(data []byte) (map[string]any, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := d.Token()
		if err != nil {
			return nil, err
		}
		if start, ok := tok.(xml.StartElement); ok && start.Name.Local != "plist" {
			v, err := plistValue(d, start)
			if err != nil {
				return nil, err
			}
			dict, ok := v.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("the root is a %s, not a dict", start.Name.Local)
			}
			return dict, nil
		}
	}
}

func plistValue(d *xml.Decoder, start xml.StartElement) (any, error) {
	switch start.Name.Local {
	case "string":
		var s string
		err := d.DecodeElement(&s, &start)
		return s, err
	case "true", "false":
		return start.Name.Local == "true", d.Skip()
	case "array", "dict":
		var items []any
		var keys []string
		for {
			tok, err := d.Token()
			if err != nil {
				return nil, err
			}
			switch tok := tok.(type) {
			case xml.StartElement:
				if start.Name.Local == "dict" && tok.Name.Local == "key" {
					var k string
					if err := d.DecodeElement(&k, &tok); err != nil {
						return nil, err
					}
					keys = append(keys, k)
					continue
				}
				v, err := plistValue(d, tok)
				if err != nil {
					return nil, err
				}
				items = append(items, v)
			case xml.EndElement:
				if start.Name.Local == "array" {
					return items, nil
				}
				if len(keys) != len(items) {
					return nil, fmt.Errorf("a dict with %d keys and %d values", len(keys), len(items))
				}
				dict := map[string]any{}
				for i, k := range keys {
					dict[k] = items[i]
				}
				return dict, nil
			}
		}
	}
	return nil, fmt.Errorf("unexpected <%s>", start.Name.Local)
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
