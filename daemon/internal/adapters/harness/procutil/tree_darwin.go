package procutil

import (
	"bytes"
	"encoding/binary"
	"time"

	"golang.org/x/sys/unix"
)

// readsProcessTable is true on macOS, whose process table sysctl reads.
const readsProcessTable = true

// sZomb is the state of a process that has exited and waits for its parent to reap it (SZOMB in <sys/proc.h>).
const sZomb = 5

// processTable reads every process from the kernel, without cgo.
func processTable() (map[int]procInfo, error) {
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	table := make(map[int]procInfo, len(kps))
	for i := range kps {
		p := infoOf(&kps[i])
		table[p.pid] = p
	}
	return table, nil
}

// processInfo reads one process, and reports false once it is gone.
func processInfo(pid int) (procInfo, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(kp.Proc.P_pid) != pid {
		return procInfo{}, false
	}
	return infoOf(kp), true
}

func infoOf(kp *unix.KinfoProc) procInfo {
	return procInfo{
		pid:    int(kp.Proc.P_pid),
		ppid:   int(kp.Eproc.Ppid),
		pgid:   int(kp.Eproc.Pgid),
		uid:    kp.Eproc.Ucred.Uid,
		start:  kp.Proc.P_starttime.Sec*1_000_000 + int64(kp.Proc.P_starttime.Usec),
		name:   unix.ByteSliceToString(kp.Proc.P_comm[:]),
		zombie: kp.Proc.P_stat == sZomb,
	}
}

// startTime is when the process started, as the kernel records it.
func startTime(pid int) (time.Time, error) {
	p, ok := processInfo(pid)
	if !ok {
		return time.Time{}, unix.ESRCH
	}
	return time.UnixMicro(p.start), nil
}

// carries is whether the environment the process started with holds the variable want, "NAME=value", exactly. It
// reads that environment with sysctl(KERN_PROCARGS2), and keeps nothing else from it. Any failure reads as no: the
// process is gone, is another user's, or is an Apple platform binary, whose environment macOS does not return.
func carries(pid int, want []byte) bool {
	buf, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(buf) < 4 {
		return false
	}
	// argc, the executable's path, NUL padding, argc arguments, then the environment up to an empty string.
	argc := int(binary.NativeEndian.Uint32(buf))
	rest := buf[4:]
	i := bytes.IndexByte(rest, 0)
	if i < 0 {
		return false
	}
	rest = rest[i:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	for ; argc > 0; argc-- {
		i := bytes.IndexByte(rest, 0)
		if i < 0 {
			return false
		}
		rest = rest[i+1:]
	}
	for len(rest) > 0 {
		i := bytes.IndexByte(rest, 0)
		if i <= 0 {
			return false
		}
		if bytes.Equal(rest[:i], want) {
			return true
		}
		rest = rest[i+1:]
	}
	return false
}
