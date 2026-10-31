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
	"strings"
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
	s := &shell{t: t, cmd: cmd, in: in, answers: make(chan map[string]any, 16), exited: make(chan struct{})}
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

// close closes the window. A shell that ends on the close may exit before it answers, which is no failure.
func (s *shell) close() {
	s.t.Helper()
	if _, err := fmt.Fprintln(s.in, "close"); err != nil {
		s.t.Fatalf("close: %v", err)
	}
	select {
	case answer := <-s.answers:
		if answer["error"] != nil {
			s.t.Fatalf("close: %v", answer["error"])
		}
	case <-s.exited:
	case <-time.After(10 * time.Second):
		s.t.Fatal("close: no answer within 10s")
	}
}

// running says whether the shell's process is still running.
func (s *shell) running() bool {
	select {
	case <-s.exited:
		return false
	default:
		return true
	}
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

// harmflow-app#464: closing the dashboard's real window, while an execution runs, follows the tray setting. With the
// tray on, the window only hides, the shell and its tray stay, and the tray's Open dashboard shows it again; with the
// tray off, the shell ends. In both, the daemon is the same process throughout, and the execution completes with no
// interruption, cancel, or stop in its log. The window is closed through the debug build's control channel, which raises
// the same close request as the window's close button, so the test needs no Accessibility permission.
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

			s.close()
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
