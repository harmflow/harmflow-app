// Package launchd starts the daemon with the user's session as a launchd user agent. It is off until the user opts in.
package launchd

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/fsstore"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// Label is the launchd label of the user agent, and the name of its plist.
const Label = "com.harmflow.daemon"

// Agent is the HostServiceManager on macOS: a launchd user agent whose plist at PlistPath starts the daemon, its stdout
// and stderr going to LogPath.
type Agent struct {
	PlistPath string
	LogPath   string
	// Launchctl runs launchctl with its arguments. Enable and Disable make no call through it; tests replace it to show
	// that, and so none reaches the user's launchd session.
	Launchctl func(args ...string) error
}

var _ ports.HostServiceManager = (*Agent)(nil)

// NewAgent returns the user agent of the user whose home directory is home, its plist in ~/Library/LaunchAgents.
func NewAgent(home, logPath string) *Agent {
	return &Agent{PlistPath: filepath.Join(home, "Library", "LaunchAgents", Label+".plist"), LogPath: logPath,
		Launchctl: runLaunchctl}
}

// runLaunchctl runs launchctl without the CODEX_HOME that names a Codex installation (ports.WithoutCodexHome).
func runLaunchctl(args ...string) error {
	cmd := exec.Command("/bin/launchctl", args...)
	cmd.Env = ports.WithoutCodexHome(nil)
	return cmd.Run()
}

// Mechanism implements ports.HostServiceManager: launchd.
func (a *Agent) Mechanism() string { return "launchd" }

// Location implements ports.HostServiceManager: the path of the plist.
func (a *Agent) Location() string { return a.PlistPath }

// Enabled implements ports.HostServiceManager: the plist exists.
func (a *Agent) Enabled() bool {
	_, err := os.Stat(a.PlistPath)
	return err == nil
}

// Enable writes the user agent, and launchd loads it at the next login. RunAtLoad starts the daemon at login; it does
// not restart a daemon that was stopped on purpose, so KeepAlive restarts it only after a crash. Enable calls no
// launchctl: the daemon running now goes on, whoever started it (harmflow-app#81).
func (a *Agent) Enable(executable string, args []string, env map[string]string) error {
	plist := renderPlist(executable, args, env, a.LogPath)
	return fsstore.WriteAtomic(a.PlistPath, []byte(plist), 0o644)
}

// Refresh brings the user agent up to date with what Enable would write now, so an agent an earlier version wrote, with
// the Background process class say, takes the current one at the next login (harmflow-app#907). It rewrites only an
// agent that starts this daemon: one whose ProgramArguments and EnvironmentVariables are the ones Enable would write
// for executable, args, and env. An agent that starts another executable, or this one with other arguments or
// variables, is left as it is, so a test or development daemon never takes over the user's agent. With no agent,
// autostart is off and Refresh writes none. It reports whether it rewrote the agent, and calls no launchctl: the daemon
// running now goes on.
func (a *Agent) Refresh(executable string, args []string, env map[string]string) (bool, error) {
	data, err := os.ReadFile(a.PlistPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	plist := renderPlist(executable, args, env, a.LogPath)
	if string(data) == plist {
		return false, nil
	}
	written, err := decodePlist(data)
	if err != nil {
		return false, fmt.Errorf("read the user agent %q: %w", a.PlistPath, err)
	}
	if !startsDaemon(written, append([]string{executable}, args...), env) {
		return false, nil
	}
	if err := fsstore.WriteAtomic(a.PlistPath, []byte(plist), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// startsDaemon reports whether a decoded user agent runs program with the variables env and no others: the same
// ProgramArguments, and the same EnvironmentVariables, an agent without them having none.
func startsDaemon(plist map[string]any, program []string, env map[string]string) bool {
	got, ok := plist["ProgramArguments"].([]any)
	if !ok || len(got) != len(program) {
		return false
	}
	for i, v := range got {
		if s, ok := v.(string); !ok || s != program[i] {
			return false
		}
	}
	gotEnv := map[string]any{}
	if v, ok := plist["EnvironmentVariables"]; ok {
		if gotEnv, ok = v.(map[string]any); !ok {
			return false
		}
	}
	if len(gotEnv) != len(env) {
		return false
	}
	for k, v := range env {
		if s, ok := gotEnv[k].(string); !ok || s != v {
			return false
		}
	}
	return true
}

// Disable deletes the user agent, so the next login does not start the daemon. It calls no launchctl: a bootout would
// stop the daemon launchd started, the very one serving the request, and end its executions (harmflow-app#81). The job
// launchd already loaded stays until logout, so until then KeepAlive may still restart a daemon that crashes.
func (a *Agent) Disable() error {
	if err := os.Remove(a.PlistPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func esc(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s)) // a strings.Builder never fails a write
	return b.String()
}

func renderPlist(executable string, args []string, env map[string]string, logPath string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>` + Label + `</string>
  <key>ProgramArguments</key>
  <array>
`)
	for _, a := range append([]string{executable}, args...) {
		fmt.Fprintf(&b, "    <string>%s</string>\n", esc(a))
	}
	b.WriteString("  </array>\n")
	if len(env) > 0 {
		b.WriteString("  <key>EnvironmentVariables</key>\n  <dict>\n")
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "    <key>%s</key>\n    <string>%s</string>\n", esc(k), esc(env[k]))
		}
		b.WriteString("  </dict>\n")
	}
	fmt.Fprintf(&b, `  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>
  <key>ProcessType</key>
  <string>Standard</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
  <key>StandardOutPath</key>
  <string>%s</string>
</dict>
</plist>
`, esc(logPath), esc(logPath))
	return b.String()
}

// decodePlist reads an XML property list whose root is a dictionary: a dict is a map, an array a slice, a string a
// string, and true and false a bool. Any other value is an error.
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
