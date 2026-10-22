package procutil

import (
	"bytes"
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
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
	// A loaded host can take seconds to run a shell: wait for the pid while the shell lives, up to a ceiling.
	var child int
	for deadline := time.Now().Add(3 * time.Minute); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if data, err := os.ReadFile(pidFile); err == nil && len(strings.TrimSpace(string(data))) > 0 {
			child, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			break
		}
		select {
		case <-p.Done():
			t.Fatalf("the shell exited before starting the child, exit %d: %q", p.ExitCode(), p.StderrTail())
		default:
		}
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
	// The killed child is gone once it has been reaped, which a loaded host can take a while to do.
	for deadline := time.Now().Add(10 * time.Second); syscall.Kill(child, 0) == nil && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
	}
	if err := syscall.Kill(child, 0); err == nil {
		t.Errorf("child %d survived", child)
	}
	if len(lines) == 0 || lines[0] != "started" {
		t.Errorf("stdout lines: %v", lines)
	}
}

// The stdout reader and os/exec's stderr copy both write to raw, which gets one write at a time.
func TestRawGetsOneWriteAtATime(t *testing.T) {
	script := `i=0; while [ $i -lt 500 ]; do echo out $i; echo err $i >&2; i=$((i+1)); done`
	var raw bytes.Buffer
	p, err := Start(ports.Invocation{Path: "/bin/sh", Args: []string{"-c", script}}, &raw, func([]byte) {})
	if err != nil {
		t.Fatal(err)
	}
	<-p.Done()
	if out, errs := strings.Count(raw.String(), "out "), strings.Count(raw.String(), "err "); out != 500 || errs != 500 {
		t.Errorf("raw has %d stdout and %d stderr lines, want 500 each", out, errs)
	}
}

// FS-013 FR-008, D-112: every line of both streams is recorded at one point, so each stream keeps its order and the
// raw capture holds the lines in the order the callbacks got them.
func TestStreamsAreRecordedInOneOrder(t *testing.T) {
	const n = 500
	script := `i=0; while [ $i -lt ` + strconv.Itoa(n) + ` ]; do echo out $i; echo err $i >&2; i=$((i+1)); done`
	var raw bytes.Buffer
	var mu sync.Mutex
	var got []string
	record := func(b []byte) {
		mu.Lock()
		got = append(got, string(b))
		mu.Unlock()
	}
	p, err := StartLines(ports.Invocation{Path: "/bin/sh", Args: []string{"-c", script}}, &raw, record, record)
	if err != nil {
		t.Fatal(err)
	}
	<-p.Done()
	captured := strings.Split(strings.TrimSuffix(raw.String(), "\n"), "\n")
	if len(got) != 2*n || len(captured) != 2*n {
		t.Fatalf("%d lines to the callbacks and %d in the raw capture, want %d each", len(got), len(captured), 2*n)
	}
	next := map[string]int{}
	for i, line := range got {
		stream, number, _ := strings.Cut(line, " ")
		if number != strconv.Itoa(next[stream]) {
			t.Fatalf("line %d is %q, want %s %d: a stream lost its order", i, line, stream, next[stream])
		}
		next[stream]++
		if captured[i] != line {
			t.Fatalf("line %d is %q for the callbacks and %q in the raw capture", i, line, captured[i])
		}
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
