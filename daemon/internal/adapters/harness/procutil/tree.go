package procutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"syscall"
	"time"
)

// Tree names what one execution's harness started, for EndTree to find and end: the harness by its pid and start
// time, the process group it leads, and the execution's id, which the harness's environment carries in
// HARMFLOW_EXECUTION_ID and every process it starts inherits unless that process clears it.
type Tree struct {
	Pid         int
	Start       time.Time
	Group       int
	ExecutionID string
}

// Ended is one process EndTree ended.
type Ended struct {
	Pid int
	// Executable is the name of the program the process ran, as the kernel records it: at most 16 bytes on macOS.
	Executable string
	// OutsideGroup is set for a process that was not in the harness's process group, which a signal to the group
	// would have missed.
	OutsideGroup bool
	// LeftRunning is set for a process the harness left running when it exited on its own. Process.Stop sets it;
	// EndTree, which cannot tell how the harness ended, never does.
	LeftRunning bool
}

const (
	// killRounds bounds the scans before SIGKILL: each round ends what the previous one found, and the rounds stop
	// once one finds no new process.
	killRounds = 5
	// reapWait bounds the wait for the killed processes to be gone, so the caller sees the tree ended.
	reapWait     = 2 * time.Second
	pollInterval = 25 * time.Millisecond
)

// executionIDVariable is the one variable of a process's environment the scan reads.
const executionIDVariable = "HARMFLOW_EXECUTION_ID"

// ReadsProcessTable reports whether this build reads the kernel's process table, which EndTree needs to find what a
// harness left outside its group. Without it, EndTree ends the harness's group alone.
func ReadsProcessTable() bool { return readsProcessTable }

// EndTree ends every process the harness started, wherever it moved, and returns those it ended. It
// finds them three ways: the harness's descendants by parent pid, kept from a snapshot taken before SIGTERM and added
// to by another before SIGKILL, so a descendant that left the group or the session is found while its parent runs;
// the processes of the user whose environment, as they started, carries the execution's id, so one that daemonized
// and that launchd adopted is found too; and the descendants of what the scan finds. The harness's group is searched
// as well. Every process found gets SIGTERM, and the grace period applies to all of them: once all have exited, or the
// grace period has elapsed, each one still alive gets SIGKILL, even when the harness exited first. A process forked
// meanwhile is found by scanning again before SIGKILL, up to killRounds times, until a scan finds no new process. A
// harness still running gets each signal before the others, so it ends with the signal's exit, and it gets SIGTERM once.
//
// A process is known by its pid and start time, both checked again before every signal, so a reused pid is never
// signalled. The daemon and the processes it runs under are never signalled.
//
// Known limit: on macOS, KERN_PROCARGS2 returns no environment for Apple platform binaries, such as /bin/sh or
// /usr/bin/ssh. Such a process, or one that cleared the variable, survives once it has daemonized, since then neither
// the snapshot nor the scan finds it. The limit is larger once the harness has exited on its own: launchd has adopted
// every process it left, so the snapshot finds none of them, and such a process survives once it has left the
// harness's group, as Claude Code runs each Bash tool command in a session of its own, unless it descends from a process
// found. No snapshot is taken while the harness runs.
func EndTree(t Tree, grace time.Duration) []Ended {
	if _, err := processTable(); err != nil || t.Pid <= 1 || t.Start.IsZero() {
		endGroup(t.Group, grace)
		return nil
	}
	return endTree(t, grace)
}

// EndStoredTree is EndTree for a tree that a record kept after the daemon that started the harness is gone, as a daemon
// that restarts after a crash finds it. It finds and ends the processes as EndTree does, but never signals the group
// alone: it ends nothing unless the record names the harness by its pid and start time, and the system has not
// restarted since the harness started, so a group whose leader is gone can still only be the harness's. It returns an
// error when the process table or the system's boot time cannot be read, and then ends nothing.
func EndStoredTree(ctx context.Context, t Tree, grace time.Duration) ([]Ended, error) {
	if _, err := processTable(); err != nil {
		return nil, err
	}
	if t.Pid <= 1 || t.Start.IsZero() {
		return nil, nil
	}
	boot, err := bootTime(ctx)
	if err != nil {
		return nil, err
	}
	booted, err := strconv.ParseInt(boot, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("read the boot time %q: %w", boot, err)
	}
	if t.Start.Unix() < booted {
		return nil, nil
	}
	return endTree(t, grace), nil
}

// endTree is EndTree once the process table can be read and the harness is known by its pid and start time.
func endTree(t Tree, grace time.Duration) []Ended {
	e := &ender{tree: t, start: t.Start.UnixMicro(), found: map[ident]*target{}, uid: uint32(os.Getuid())}
	e.find()
	e.signal(syscall.SIGTERM)
	e.await(grace)
	for round := 0; round < killRounds; round++ {
		if e.find() == 0 && round > 0 {
			break
		}
		e.signal(syscall.SIGKILL)
	}
	e.await(reapWait)
	return e.ended()
}

// endGroup is EndTree where the process table cannot be read: the harness's group gets SIGTERM, then SIGKILL once it
// is empty or the grace period has elapsed.
func endGroup(group int, grace time.Duration) {
	if group <= 1 {
		return
	}
	// A group that is already gone cannot be signaled, which is what was wanted; so for SIGKILL below.
	_ = syscall.Kill(-group, syscall.SIGTERM)
	for deadline := time.Now().Add(grace); time.Now().Before(deadline); time.Sleep(pollInterval) {
		if errors.Is(syscall.Kill(-group, 0), syscall.ESRCH) {
			return
		}
	}
	_ = syscall.Kill(-group, syscall.SIGKILL)
}

// procInfo is one row of the process table.
type procInfo struct {
	pid, ppid, pgid int
	uid             uint32
	start           int64 // microseconds since the epoch
	name            string
	zombie          bool
}

// ident is a process as EndTree knows it, by pid and start time.
type ident struct {
	pid   int
	start int64
}

type target struct {
	name        string
	outside     bool
	signalled   bool
	unsignalled bool // the kernel refused a signal: the process is another user's
}

type ender struct {
	tree  Tree
	start int64
	uid   uint32
	found map[ident]*target
	// group is set while the harness's process group is still its own, for signal to reach it in one call.
	group bool
}

// find takes a snapshot of the process table and adds what it finds to the processes already known. It returns how
// many it added.
func (e *ender) find() int {
	table, err := processTable()
	if err != nil {
		return 0
	}
	never := e.protected(table)
	added := 0
	var roots []int
	add := func(p procInfo) {
		if p.zombie || never[p.pid] {
			return
		}
		id := ident{p.pid, p.start}
		t, ok := e.found[id]
		if !ok {
			t = &target{name: p.name}
			e.found[id] = t
			added++
		}
		t.outside = t.outside || p.pgid != e.tree.Group
		roots = append(roots, p.pid)
	}
	if h, ok := table[e.tree.Pid]; ok && h.start == e.start {
		add(h)
	}
	for id := range e.found {
		if p, ok := table[id.pid]; ok && p.start == id.start {
			add(p)
		}
	}
	// The group is the harness's unless its id is now another process's pid: an id in use as a group's is never handed
	// out, so that process came after the group emptied, and any group of that id is a new one.
	leader, ok := table[e.tree.Group]
	e.group = e.tree.Group > 1 && (!ok || leader.pid == e.tree.Pid && leader.start == e.start)
	if e.group {
		for _, p := range table {
			if p.pgid == e.tree.Group {
				add(p)
			}
		}
	}
	if e.tree.ExecutionID != "" {
		want := []byte(executionIDVariable + "=" + e.tree.ExecutionID)
		for _, p := range table {
			if p.uid != e.uid || p.zombie || never[p.pid] {
				continue
			}
			if _, ok := e.found[ident{p.pid, p.start}]; ok {
				continue
			}
			if carries(p.pid, want) {
				add(p)
			}
		}
	}
	children := map[int][]procInfo{}
	for _, p := range table {
		children[p.ppid] = append(children[p.ppid], p)
	}
	for len(roots) > 0 {
		pid := roots[len(roots)-1]
		roots = roots[:len(roots)-1]
		for _, c := range children[pid] {
			if _, ok := e.found[ident{c.pid, c.start}]; !ok {
				add(c)
			}
		}
	}
	return added
}

// protected is the daemon and every process it runs under: none is ever signalled, even one that carries the
// execution's id because the daemon itself runs inside a HarmFlow execution.
func (e *ender) protected(table map[int]procInfo) map[int]bool {
	never := map[int]bool{0: true, 1: true}
	for pid := os.Getpid(); pid > 1 && !never[pid]; {
		never[pid] = true
		p, ok := table[pid]
		if !ok {
			break
		}
		pid = p.ppid
	}
	return never
}

// signal sends sig to every process found that still runs, after checking its pid and start time again.
//
// A harness still running gets it alone, before any other process. The kernel signals a group's members one at a time,
// so a child signalled first could die and let the harness, a shell waiting for it, run its next command, such as one
// that reports success, and exit 0 before its own signal came; with the signal already pending, the harness ends with
// the signal's exit, or runs its handler, before anything its children do reaches it.
//
// SIGKILL then reaches the harness's group in one call, which a process forked into the group since the last scan
// cannot miss, and each process found outside the group next. SIGTERM, which a harness may handle, must reach it once,
// and a signal to its group would bring it again: while the harness runs, every other process found gets SIGTERM on its
// own, the older before the younger, so a process gets it before the processes it started, as the harness does. A
// process forked into the group after the last scan misses SIGTERM, and the scans before SIGKILL find it. Once the
// harness has exited, its group gets SIGTERM in one call, as it gets SIGKILL.
func (e *ender) signal(sig syscall.Signal) {
	harness := ident{e.tree.Pid, e.start}
	alone := false
	if t, ok := e.found[harness]; ok && !t.unsignalled && running(harness) {
		alone = t.send(harness.pid, sig)
	}
	group := e.group && (sig == syscall.SIGKILL || !alone)
	// The group's members found running just before its signal get it, though one may die of it, and be reaped, before
	// the loop below reads it again.
	var members []*target
	if group {
		for id, t := range e.found {
			if p, ok := processInfo(id.pid); ok && p.start == id.start && !p.zombie && p.pgid == e.tree.Group {
				members = append(members, t)
			}
		}
	}
	grouped := group && syscall.Kill(-e.tree.Group, sig) == nil
	if grouped {
		for _, t := range members {
			t.signalled = true
		}
	}
	var ids []ident
	for id := range e.found {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if ids[i].start != ids[j].start {
			return ids[i].start < ids[j].start
		}
		return ids[i].pid < ids[j].pid
	})
	for _, id := range ids {
		t := e.found[id]
		if t.unsignalled || alone && sig == syscall.SIGTERM && id == harness {
			continue
		}
		p, ok := processInfo(id.pid)
		if !ok || p.start != id.start || p.zombie {
			continue
		}
		if grouped && p.pgid == e.tree.Group {
			t.signalled = true
			continue
		}
		t.send(id.pid, sig)
	}
}

// send sends sig to the process, whose pid and start time the caller has just checked, and reports whether it did.
func (t *target) send(pid int, sig syscall.Signal) bool {
	switch err := syscall.Kill(pid, sig); {
	case err == nil:
		t.signalled = true
		return true
	case errors.Is(err, syscall.EPERM):
		t.unsignalled = true
	}
	return false
}

// await waits until no process found runs any more, or until d has elapsed.
func (e *ender) await(d time.Duration) {
	for deadline := time.Now().Add(d); ; time.Sleep(pollInterval) {
		table, err := processTable()
		if err != nil {
			return
		}
		alive := false
		for id, t := range e.found {
			if p, ok := table[id.pid]; ok && !t.unsignalled && p.start == id.start && !p.zombie {
				alive = true
				break
			}
		}
		if !alive || !time.Now().Before(deadline) {
			return
		}
	}
}

// ended lists the processes signalled that no longer run, by pid.
func (e *ender) ended() []Ended {
	var out []Ended
	for id, t := range e.found {
		if t.signalled && !running(id) {
			out = append(out, Ended{Pid: id.pid, Executable: t.name, OutsideGroup: t.outside})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pid < out[j].Pid })
	return out
}

// running is whether the process still runs: its pid exists, with the same start time, and it is not a zombie.
func running(id ident) bool {
	p, ok := processInfo(id.pid)
	return ok && p.start == id.start && !p.zombie
}
