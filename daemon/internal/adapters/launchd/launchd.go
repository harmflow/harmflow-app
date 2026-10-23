// Package launchd starts the daemon with the user's session as a launchd user agent (D-062). It is off until the
// user opts in (D-008).
package launchd

import (
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

const Label = "com.harmflow.daemon"

type Agent struct {
	PlistPath string
	LogPath   string
	// Launchctl runs launchctl with its arguments. Enable and Disable make no call through it; tests replace it to show
	// that, and so none reaches the user's launchd session.
	Launchctl func(args ...string) error
}

var _ ports.HostServiceManager = (*Agent)(nil)

func NewAgent(home, logPath string) *Agent {
	return &Agent{PlistPath: filepath.Join(home, "Library", "LaunchAgents", Label+".plist"), LogPath: logPath,
		Launchctl: runLaunchctl}
}

func runLaunchctl(args ...string) error { return exec.Command("/bin/launchctl", args...).Run() }

func (a *Agent) Mechanism() string { return "launchd" }
func (a *Agent) Location() string  { return a.PlistPath }

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
  <string>Background</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
  <key>StandardOutPath</key>
  <string>%s</string>
</dict>
</plist>
`, esc(logPath), esc(logPath))
	return b.String()
}
