package integration

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// watchdogEnv names the PID of the daemon the test binary watches when it runs as a daemon's watchdog rather than as the
// tests; watchdogEnv+"_GROUP" is set when the daemon leads a process group of its own, which the watchdog then ends.
const watchdogEnv = "HARMFLOW_TEST_WATCHDOG"

// binaryEnv names a harmflowd already built, for a test binary that another one runs: it neither builds one nor leaves
// a build behind when it is killed.
const binaryEnv = "HARMFLOW_TEST_BINARY"

// holdEnv names the file TestHoldADaemon writes its daemon's PID to; holdEnv+"_HOME" is the daemon's home, and
// holdEnv+"_DETACHED" is set when the daemon is started through runStarter.
const holdEnv = "HARMFLOW_TEST_HOLD"

// watchDaemon ties the daemon with the PID to the test process. A watchdog, the test binary run as one, holds the read
// end of a pipe whose write end only the test process holds; the kernel closes it when the test process exits, however
// it ends, and the watchdog then ends the daemon if it still runs. The test's cleanup closes the pipe itself, so a
// watchdog registered before the daemon's stop runs after it and finds the daemon gone.
func watchDaemon(t *testing.T, pid int, group bool) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self)
	cmd.Env = append(os.Environ(), watchdogEnv+"="+strconv.Itoa(pid))
	if group {
		cmd.Env = append(cmd.Env, watchdogEnv+"_GROUP=1")
	}
	// A process group of its own, so that a signal to the test's group leaves it running. Its output stays on
	// /dev/null: a watchdog that held go test's output would keep go test waiting, and would die writing to it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	lifeline, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = lifeline.Close()
		_ = cmd.Wait()
	})
}

// runWatchdog is the test binary run as the watchdog of the daemon with the PID. It ignores the signals that end a run,
// so that only the end of its standard input ends it. Then, if the PID is still the daemon it was, it sends SIGTERM,
// and SIGKILL once a grace period has passed, to the daemon or to its process group.
func runWatchdog(pid int, group bool) int {
	signal.Ignore(syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	id := processIdentity(pid)
	_, _ = io.Copy(io.Discard, os.Stdin)
	if id == "" {
		return 0
	}
	target := pid
	if group {
		target = -pid
	}
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		if processIdentity(pid) != id {
			return 0
		}
		_ = syscall.Kill(target, sig)
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
			if processIdentity(pid) != id {
				return 0
			}
		}
	}
	return 0
}

// processIdentity is the start time and command line of the process with the PID, empty when there is none. A PID
// reused by another process, or a zombie, has another identity.
func processIdentity(pid int) string {
	out, err := exec.Command("ps", "-ww", "-o", "lstart=,args=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// TestHoldADaemon is the run TestNoDaemonOutlivesAnInterruptedRun interrupts: it starts a daemon, writes its PID, and
// blocks until it is ended.
func TestHoldADaemon(t *testing.T) {
	pidFile := os.Getenv(holdEnv)
	if pidFile == "" {
		t.Skip("run by TestNoDaemonOutlivesAnInterruptedRun")
	}
	home := os.Getenv(holdEnv + "_HOME")
	var pid int
	if os.Getenv(holdEnv+"_DETACHED") != "" {
		_, pid, _ = startDetachedDaemon(t, home)
	} else {
		pid = startDaemon(t, home).cmd.Process.Pid
	}
	if err := os.WriteFile(pidFile+".tmp", []byte(strconv.Itoa(pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(pidFile+".tmp", pidFile); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Hour)
	}
}

// A daemon a test starts never outlives the test process: a run killed, interrupted, or timed out in the middle of a
// test leaves no harmflowd behind, whether the test started it as its child or through runStarter.
func TestNoDaemonOutlivesAnInterruptedRun(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	signalling := func(sig syscall.Signal) func(*os.Process) {
		return func(p *os.Process) { _ = p.Signal(sig) }
	}
	for _, c := range []struct {
		name     string
		detached bool
		args     []string
		end      func(*os.Process) // nil when the run ends by itself
	}{
		{"killed", false, nil, signalling(syscall.SIGKILL)},
		{"killed with a detached daemon", true, nil, signalling(syscall.SIGKILL)},
		{"interrupted", false, nil, signalling(syscall.SIGINT)},
		{"timed out", false, []string{"-test.timeout=10s"}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			home, pidFile := t.TempDir(), filepath.Join(t.TempDir(), "daemon.pid")
			run := exec.Command(self, append([]string{"-test.run=^TestHoldADaemon$", "-test.count=1"}, c.args...)...)
			run.Env = append(os.Environ(), binaryEnv+"="+binary, holdEnv+"="+pidFile, holdEnv+"_HOME="+home)
			if c.detached {
				run.Env = append(run.Env, holdEnv+"_DETACHED=1")
			}
			var out bytes.Buffer
			run.Stdout, run.Stderr = &out, &out
			if err := run.Start(); err != nil {
				t.Fatal(err)
			}
			exited := make(chan struct{})
			go func() { _ = run.Wait(); close(exited) }()
			t.Cleanup(func() {
				_ = run.Process.Kill()
				<-exited
			})
			var pid int
			for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline) && pid == 0; time.Sleep(50 * time.Millisecond) {
				data, _ := os.ReadFile(pidFile)
				pid, _ = strconv.Atoi(string(data))
			}
			if pid == 0 {
				t.Fatalf("the run started no daemon:\n%s", out.String())
			}
			id := processIdentity(pid)
			// Should the daemon outlive the run, the test ends it rather than leaving it behind too.
			t.Cleanup(func() {
				if processIdentity(pid) == id {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			})
			if c.end != nil {
				c.end(run.Process)
			}
			select {
			case <-exited:
			case <-time.After(30 * time.Second):
				t.Fatalf("the run did not end:\n%s", out.String())
			}
			for deadline := time.Now().Add(15 * time.Second); processIdentity(pid) == id; time.Sleep(100 * time.Millisecond) {
				if time.Now().After(deadline) {
					t.Fatalf("the daemon %d still runs 15s after the run ended:\n%s", pid, out.String())
				}
			}
		})
	}
}
