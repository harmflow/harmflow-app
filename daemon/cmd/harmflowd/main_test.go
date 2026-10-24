package main

import (
	"errors"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// The commit go build stamps tells builds of the same version apart in Settings → About.
func TestCommitOf(t *testing.T) {
	const revision = "45a57bb0c3f1d2e4a5b6c7d8e9f0a1b2c3d4e5f6"
	cases := []struct {
		name     string
		settings []debug.BuildSetting
		want     string
	}{
		{"clean checkout", []debug.BuildSetting{{Key: "vcs", Value: "git"}, {Key: "vcs.revision", Value: revision},
			{Key: "vcs.modified", Value: "false"}}, "45a57bb"},
		{"uncommitted changes", []debug.BuildSetting{{Key: "vcs.modified", Value: "true"},
			{Key: "vcs.revision", Value: revision}}, "45a57bb-dirty"},
		{"no VCS stamp", []debug.BuildSetting{{Key: "-trimpath", Value: "true"}, {Key: "GOOS", Value: "darwin"}}, ""},
		{"modified without a revision", []debug.BuildSetting{{Key: "vcs.modified", Value: "true"}}, ""},
		{"short revision", []debug.BuildSetting{{Key: "vcs.revision", Value: "45a5"}}, "45a5"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {

			if got := commitOf(c.settings); got != c.want {
				t.Errorf("commitOf(%v) = %q, want %q", c.settings, got, c.want)
			}
		})
	}
}

// The commit make sets wins: go build stamps none in a git worktree.
func TestBuildCommitPrefersMake(t *testing.T) {
	defer func(saved string) { Commit = saved }(Commit)
	Commit = "45a57bb-dirty"
	if got := buildCommit(); got != "45a57bb-dirty" {
		t.Errorf("got %q", got)
	}
}

// The build time make stamps tells two builds of the same commit apart; a stamp that is missing or not RFC 3339 is none.
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

// A lock another holder has means a daemon is running on the data directory.
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
	} else if !strings.Contains(err.Error(), "already running") {
		t.Errorf("acquireLock(%s) = %v, want that another daemon is running", path, err)
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
	if !errors.Is(err, syscall.ENOTSUP) || !strings.Contains(err.Error(), path) || strings.Contains(err.Error(), "already running") {
		t.Errorf("acquireLock(%s) = %v, want an error naming the file and wrapping %v", path, err, syscall.ENOTSUP)
	}
}

// run serves the local API on a goroutine; none may outlive the test that started it (D-132).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
