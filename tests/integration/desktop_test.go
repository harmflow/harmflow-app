package integration

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// desktopEnv opts in to the tests that start the real desktop shell, which opens a window on the screen.
const desktopEnv = "HARMFLOW_TEST_DESKTOP"

// realDesktop skips the test unless it runs on macOS with HARMFLOW_TEST_DESKTOP exactly 1 and cargo on the PATH, and
// otherwise builds the shell in its debug build, the only one with the test's control channel, and returns its path.
// The window needs a display: a session with none opens no window, and the test fails waiting for it.
func realDesktop(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "darwin" || os.Getenv(desktopEnv) != "1" {
		t.Skipf("needs macOS, a display, and %s=1: it opens the desktop's real window", desktopEnv)
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("needs cargo on the PATH, to build the desktop shell")
	}
	root := repoRoot()
	sidecars, _ := filepath.Glob(filepath.Join(root, "desktop", "src-tauri", "binaries", "harmflowd-*"))
	if len(sidecars) == 0 {
		t.Fatal("the desktop shell's build needs its sidecar: run make sidecar first")
	}
	// From the repository's root, so that .cargo/config.toml puts the build in target.noindex.
	build := exec.Command(cargo, "build", "--manifest-path", filepath.Join("desktop", "src-tauri", "Cargo.toml"))
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the desktop shell: %v\n%s", err, out)
	}
	return filepath.Join(root, "desktop", "src-tauri", "target.noindex", "debug", "harmflow-desktop")
}

// shell is a running desktop shell, driven through its test control channel: one command per line on its standard
// input, one JSON answer per line on its standard output.
type shell struct {
	t       *testing.T
	cmd     *exec.Cmd
	in      io.Writer
	answers chan map[string]any
	exited  chan struct{}
}

// startShell starts the desktop shell on the daemon's home, with its test control channel on.
func startShell(t *testing.T, bin string, d *daemon) *shell {
	t.Helper()
	cmd := exec.Command(bin)
	cmd.Env = daemonEnv([]string{"HARMFLOW_HOME=" + d.home, "HARMFLOW_TEST_SHELL_CONTROL=1", "HARMFLOW_DAEMON_BIN=" + binary})
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// One answer at most is left unread: a close's, when the shell exited first.
	s := &shell{t: t, cmd: cmd, in: in, answers: make(chan map[string]any, 1), exited: make(chan struct{})}
	// Both goroutines stop once the shell exits, which the cleanup below makes sure of.
	go func() {
		// The shell may print lines of its own: only a JSON object is an answer.
		lines := bufio.NewScanner(out)
		for lines.Scan() {
			var answer map[string]any
			if json.Unmarshal(lines.Bytes(), &answer) == nil {
				s.answers <- answer
			}
		}
	}()
	go func() { _ = cmd.Wait(); close(s.exited) }()
	t.Cleanup(func() {
		select {
		case <-s.exited:
		default:
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-s.exited:
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				<-s.exited
			}
		}
	})
	return s
}

// send writes a command and returns its answer, failing the test on an error or no answer within ten seconds.
func (s *shell) send(command string) map[string]any {
	s.t.Helper()
	if _, err := fmt.Fprintln(s.in, command); err != nil {
		s.t.Fatalf("%s: %v", command, err)
	}
	select {
	case answer := <-s.answers:
		if answer["error"] != nil {
			s.t.Fatalf("%s: %v", command, answer["error"])
		}
		return answer
	case <-s.exited:
		s.t.Fatalf("%s: the shell exited", command)
	case <-time.After(10 * time.Second):
		s.t.Fatalf("%s: no answer within 10s", command)
	}
	return nil
}

// waitState waits until the shell's window is visible or not, as visible says, with the tray enabled as the stored
// setting says. A start shows the window once the interface is ready or after its ten-second fallback, and reads the
// tray's setting from the daemon, so both take a moment.
func (s *shell) waitState(visible, tray bool) {
	s.t.Helper()
	var state map[string]any
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if state = s.send("state"); state["visible"] == visible && state["trayEnabled"] == tray {
			return
		}
	}
	s.t.Fatalf("the shell's state is %v, want visible %v and trayEnabled %v", state, visible, tray)
}
