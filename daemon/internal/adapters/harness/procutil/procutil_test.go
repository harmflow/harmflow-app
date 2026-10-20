package procutil

import (
	"context"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// FS-003 AC-007: cancelling ends the whole process tree after the grace period, even a child that ignores SIGTERM.
func TestStopKillsTheProcessTree(t *testing.T) {
	pidFile := t.TempDir() + "/child.pid"
	script := `trap '' TERM; sh -c 'trap "" TERM; sleep 300' & echo $! > ` + pidFile + `; echo started; wait`
	var lines []string
	p, err := Start(ports.Invocation{Path: "/bin/sh", Args: []string{"-c", script}, Env: os.Environ()}, nil,
		func(b []byte) { lines = append(lines, string(b)) })
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var child int
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(pidFile); err == nil && len(strings.TrimSpace(string(data))) > 0 {
			child, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if child == 0 {
		t.Fatal("the child did not start")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	p.Wait(ctx, 300*time.Millisecond)
	if time.Since(start) > 5*time.Second {
		t.Fatal("stopping took too long")
	}
	time.Sleep(100 * time.Millisecond)
	if err := syscall.Kill(child, 0); err == nil {
		t.Errorf("child %d survived", child)
	}
	if len(lines) == 0 || lines[0] != "started" {
		t.Errorf("stdout lines: %v", lines)
	}
}

func TestExitCode(t *testing.T) {
	p, err := Start(ports.Invocation{Path: "/bin/sh", Args: []string{"-c", "echo out; echo err >&2; exit 3"}}, nil, func([]byte) {})
	if err != nil {
		t.Fatal(err)
	}
	<-p.Done()
	if p.ExitCode() != 3 || !strings.Contains(p.StderrTail(), "err") {
		t.Errorf("exit %d stderr %q", p.ExitCode(), p.StderrTail())
	}
}
