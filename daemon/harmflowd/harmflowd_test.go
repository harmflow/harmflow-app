package harmflowd

import (
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// A lock another holder has means a daemon is running on the data directory: ErrAlreadyRunning, which command
// harmflowd matches to pick its exit status (harmflow-app#978).
func TestAcquireLockSeesARunningDaemon(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	held, err := acquireLock(path, syscall.Flock)
	if err != nil {
		t.Fatalf("acquireLock(%s) = %v, want the lock", path, err)
	}
	defer held.Close()
	if f, err := acquireLock(path, syscall.Flock); err == nil {
		f.Close()
		t.Fatalf("acquireLock(%s) took a lock already held", path)
	} else if !errors.Is(err, ErrAlreadyRunning) || !strings.Contains(err.Error(), "already running") {
		t.Errorf("acquireLock(%s) = %v, want %v", path, err, ErrAlreadyRunning)
	}
}

// Any other lock failure, such as a file system without locks, names the lock file and keeps its cause, rather than
// send the user looking for a daemon that is not there.
func TestAcquireLockReportsOtherFailures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	noLocks := func(int, int) error { return syscall.ENOTSUP }
	f, err := acquireLock(path, noLocks)
	if err == nil {
		f.Close()
		t.Fatalf("acquireLock(%s) took a lock the file system refused", path)
	}
	if !errors.Is(err, syscall.ENOTSUP) || !strings.Contains(err.Error(), path) || errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("acquireLock(%s) = %v, want an error naming the file and wrapping %v", path, err, syscall.ENOTSUP)
	}
}

// The built-ins directory is the one whose skills/ holds an hf- skill or whose agents/ holds an hf- agent, either one
// alone; any other directory named skills or agents is not it.
func TestHoldsBuiltins(t *testing.T) {
	cases := []struct {
		name string
		dirs []string
		file string
		want bool
	}{
		{"a built-in skill", []string{"skills/hf-github-worktree"}, "", true},
		{"a built-in agent", []string{"agents"}, "agents/hf-github-issue-implementer.md", true},
		{"empty skills and agents directories", []string{"skills", "agents"}, "", false},
		{"only the user's skills", []string{"skills/review-notes", "skills/harmflow-github-worktree"}, "", false},
		{"only the user's agents", []string{"agents"}, "agents/reviewer.md", false},
		{"a file with the prefix in skills", []string{"skills"}, "skills/hf-notes.md", false},
		{"a folder with the prefix in agents", []string{"agents/hf-reviewer"}, "", false},
		{"an agent that is not Markdown", []string{"agents"}, "agents/hf-reviewer.txt", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, d := range c.dirs {
				if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if c.file != "" {
				if err := os.WriteFile(filepath.Join(dir, c.file), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got := holdsBuiltins(dir); got != c.want {
				t.Errorf("holdsBuiltins(%v, %q) = %v, want %v", c.dirs, c.file, got, c.want)
			}
		})
	}
}

// harmflow-app#791: HARMFLOW_SIGN_IN_TIMEOUT shortens the sign-in's wait only with a positive duration. Unset, or set
// to anything else, it gives zero, which leaves the sign-in its ten-minute default.
func TestSignInTimeout(t *testing.T) {
	cases := []struct {
		name  string
		value string // "" leaves the variable unset
		want  time.Duration
	}{
		{"unset", "", 0},
		{"not a duration", "ten minutes", 0},
		{"a number without a unit", "8", 0},
		{"zero", "0s", 0},
		{"negative", "-5s", 0},
		{"positive", "8s", 8 * time.Second},
		{"positive in minutes", "2m30s", 150 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(signInTimeoutVariable, c.value) // restores the variable when the subtest ends
			if c.value == "" {
				if err := os.Unsetenv(signInTimeoutVariable); err != nil {
					t.Fatal(err)
				}
			}
			if got := signInTimeout(log.New(io.Discard, "", 0)); got != c.want {
				t.Errorf("signInTimeout() with %s=%q = %v, want %v", signInTimeoutVariable, c.value, got, c.want)
			}
		})
	}
}

// harmflow-app#1042: the daemon's CODEX_HOME names a Codex installation, whose environment reaches no other process, so
// the login shell userPath starts to read the user's PATH runs without it and keeps the daemon's other variables. A
// stand-in shell records the environment it started with and prints a PATH, which leads userPath's result.
func TestLoginShellStartsWithoutCodexHome(t *testing.T) {
	dir := t.TempDir()
	shell, envFile := filepath.Join(dir, "shell"), filepath.Join(dir, "env")
	script := "#!/bin/sh\n/usr/bin/env > '" + envFile + "'\nprintf %s /harmflow-test/login/bin:/harmflow-test/login/sbin\n"
	if err := os.WriteFile(shell, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", shell)
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("HARMFLOW_TEST_KEPT", "kept")

	got := userPath(t.TempDir())
	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("os.ReadFile(%s) = %v, want the environment the login shell started with", envFile, err)
	}
	env := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	for _, kv := range env {
		if strings.HasPrefix(kv, "CODEX_HOME=") {
			t.Errorf("the login shell started with %q, want no CODEX_HOME", kv)
		}
	}
	if !slices.Contains(env, "HARMFLOW_TEST_KEPT=kept") {
		t.Errorf("the login shell started with %q, want HARMFLOW_TEST_KEPT=kept kept", env)
	}
	if want := "/harmflow-test/login/bin:/harmflow-test/login/sbin:"; !strings.HasPrefix(got, want) {
		t.Errorf("userPath() = %q, want it to start with the login shell's PATH %q", got, want)
	}
}

// Run serves the local API on a goroutine; none may outlive the test that started it.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
