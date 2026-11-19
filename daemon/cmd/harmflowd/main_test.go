package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/harmflowd"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/launchd"
)

// runMainEnv, set to 1, makes this test binary run main instead of its tests, so a test can start it as harmflowd and
// read the status it exits with.
const runMainEnv = "HARMFLOWD_TEST_RUN_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(runMainEnv) == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// harmflow-app#978: only a daemon the login agent started, that found the data directory taken, exits with 0, which
// launchd's KeepAlive does not restart. Any other failure, or the same one in a process launchd did not start as the
// agent, still exits with 1.
func TestExitStatus(t *testing.T) {
	taken := fmt.Errorf("start: %w", harmflowd.ErrAlreadyRunning)
	other := errors.New("locking run/daemon.lock: operation not supported")
	cases := []struct {
		name    string
		err     error
		service string
		want    int
	}{
		{"taken, started by the agent", taken, launchd.Label, 0},
		{"taken, started by no launchd job", taken, "", 1},
		{"taken, another job's label", taken, "application.com.apple.Terminal.1234", 1},
		{"taken, a label that only begins with the agent's", taken, launchd.Label + ".test", 1},
		{"another failure, started by the agent", other, launchd.Label, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := exitStatus(c.err, c.service); got != c.want {
				t.Errorf("exitStatus(%v, %q) = %d, want %d", c.err, c.service, got, c.want)
			}
		})
	}
}

// harmflow-app#978: harmflowd started on a data directory whose lock another process holds exits with 0 when
// $XPC_SERVICE_NAME is the login agent's label, and with 1 otherwise, saying in both cases that a daemon already runs.
func TestHarmflowdOnATakenDataDirectory(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(home, "run", "daemon.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("flock(%s) = %v, want the lock", lock.Name(), err)
	}
	for _, c := range []struct {
		service string
		want    int
	}{
		{launchd.Label, 0},
		{"", 1},
		{"application.com.apple.Terminal.1234", 1},
	} {
		t.Run(fmt.Sprintf("XPC_SERVICE_NAME=%q", c.service), func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "--home", home)
			// exec keeps the last of two values of a variable, so these override the test's own environment.
			cmd.Env = append(os.Environ(), runMainEnv+"=1", "XPC_SERVICE_NAME="+c.service, "HARMFLOW_HOME=")
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			err := cmd.Run()
			got := 0
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				got = exit.ExitCode()
			} else if err != nil {
				t.Fatalf("running harmflowd: %v", err)
			}
			if got != c.want {
				t.Errorf("harmflowd exited with %d, want %d; stderr: %s", got, c.want, stderr.String())
			}
			if !strings.Contains(stderr.String(), harmflowd.ErrAlreadyRunning.Error()) {
				t.Errorf("harmflowd stderr = %q, want %q", stderr.String(), harmflowd.ErrAlreadyRunning)
			}
		})
	}
}

// The commit go build stamps tells builds of the same version apart: in full in each execution's record, and short in
// Settings → About, whose commit is a prefix of the full one and marks -dirty exactly when the record's boolean is
// true.
func TestRevisionOf(t *testing.T) {
	const revision = "45a57bb0c3f1d2e4a5b6c7d8e9f0a1b2c3d4e5f6"
	cases := []struct {
		name      string
		settings  []debug.BuildSetting
		revision  string
		dirty     bool
		wantShort string
	}{
		{"clean checkout", []debug.BuildSetting{{Key: "vcs", Value: "git"}, {Key: "vcs.revision", Value: revision},
			{Key: "vcs.modified", Value: "false"}}, revision, false, "45a57bb"},
		{"uncommitted changes", []debug.BuildSetting{{Key: "vcs.modified", Value: "true"},
			{Key: "vcs.revision", Value: revision}}, revision, true, "45a57bb-dirty"},
		{"no VCS stamp", []debug.BuildSetting{{Key: "-trimpath", Value: "true"}, {Key: "GOOS", Value: "darwin"}}, "",
			false, ""},
		{"modified without a revision", []debug.BuildSetting{{Key: "vcs.modified", Value: "true"}}, "", false, ""},
		{"short revision", []debug.BuildSetting{{Key: "vcs.revision", Value: "45a5"}}, "45a5", false, "45a5"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotRevision, gotDirty := revisionOf(c.settings)
			if gotRevision != c.revision || gotDirty != c.dirty {
				t.Errorf("revisionOf(%v) = %q, %v, want %q, %v", c.settings, gotRevision, gotDirty, c.revision, c.dirty)
			}
			short := shortCommit(gotRevision, gotDirty)
			if short != c.wantShort {
				t.Errorf("shortCommit(%q, %v) = %q, want %q", gotRevision, gotDirty, short, c.wantShort)
			}
			if !strings.HasPrefix(gotRevision, strings.TrimSuffix(short, "-dirty")) ||
				strings.HasSuffix(short, "-dirty") != gotDirty {
				t.Errorf("shortCommit(%q, %v) = %q, not the revision's prefix with -dirty exactly when dirty",
					gotRevision, gotDirty, short)
			}
		})
	}
}

// The revision make sets wins: go build stamps none in a git worktree.
func TestBuildRevisionPrefersMake(t *testing.T) {
	defer func(revision, modified string) { Revision, Modified = revision, modified }(Revision, Modified)
	cases := []struct {
		name      string
		modified  string
		wantDirty bool
	}{
		{"uncommitted changes", "true", true},
		{"clean checkout", "false", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			Revision, Modified = "45a57bb0c3f1d2e4a5b6c7d8e9f0a1b2c3d4e5f6", c.modified
			if revision, dirty := buildRevision(); revision != Revision || dirty != c.wantDirty {
				t.Errorf("buildRevision() with Modified %q = %q, %v, want %q, %v", c.modified, revision, dirty, Revision,
					c.wantDirty)
			}
		})
	}
}

// The build time make stamps tells two builds of the same commit apart; a stamp that is missing or not RFC 3339 is
// none.
func TestBuildTime(t *testing.T) {
	if got, want := buildTime("2026-10-22T20:30:05Z"), time.Date(2026, 10, 22, 20, 30, 5, 0, time.UTC); !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
	for _, stamp := range []string{"", "2026-10-22", "22/10/2026 17:30", "$(BUILT_AT)"} {
		if got := buildTime(stamp); !got.IsZero() {
			t.Errorf("%q: got %v, want none", stamp, got)
		}
	}
}
