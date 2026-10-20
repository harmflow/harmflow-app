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
	"strconv"
	"strings"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/fsstore"
)

const Label = "com.harmflow.daemon"

type Agent struct {
	PlistPath string
	LogPath   string
}

func NewAgent(home, logPath string) *Agent {
	return &Agent{PlistPath: filepath.Join(home, "Library", "LaunchAgents", Label+".plist"), LogPath: logPath}
}

func (a *Agent) Mechanism() string { return "launchd" }
func (a *Agent) Location() string  { return a.PlistPath }

func (a *Agent) Enabled() bool {
	_, err := os.Stat(a.PlistPath)
	return err == nil
}

// Enable writes the user agent and loads it for the next login. RunAtLoad starts the daemon at login; it does not
// restart a daemon that was stopped on purpose, so KeepAlive restarts it only after a crash.
func (a *Agent) Enable(executable string, args []string, env map[string]string) error {
	plist := renderPlist(executable, args, env, a.LogPath)
	if err := fsstore.WriteAtomic(a.PlistPath, []byte(plist), 0o644); err != nil {
		return err
	}
	// Register it with the current session without starting a second daemon: launchd starts it at the next login.
	_ = exec.Command("/bin/launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid())+"/"+Label).Run()
	return nil
}

func (a *Agent) Disable() error {
	_ = exec.Command("/bin/launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid())+"/"+Label).Run()
	if err := os.Remove(a.PlistPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func esc(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
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
