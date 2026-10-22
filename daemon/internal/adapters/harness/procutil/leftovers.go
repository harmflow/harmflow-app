package procutil

import (
	"context"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Identity recognizes a process group after the daemon that started it is gone: the group's id, its leader's start
// time, and the system's boot time, as the operating system reports them (FS-013 FR-010).
type Identity struct {
	Group       int
	LeaderStart string
	Boot        string
}

type listedProcess struct {
	pid, group int
	start      string
}

// Identify records the identity of the process group pid leads; the process must still be running.
func Identify(pid int) (Identity, error) {
	procs, err := listProcesses()
	if err != nil {
		return Identity{}, err
	}
	boot, err := bootTime()
	if err != nil {
		return Identity{}, err
	}
	for _, p := range procs {
		if p.pid == pid {
			return Identity{Group: p.group, LeaderStart: p.start, Boot: boot}, nil
		}
	}
	return Identity{}, os.ErrNotExist
}

// EndLeftovers kills every process still in the identified group and returns how many there were. It ends nothing
// unless the group is provably the one identified: the system has not restarted since, and the group's leader, when it
// still runs, started at the recorded time. A group whose leader is gone is ended when the boot matches: an id is not
// handed out again while a group of that id exists, so it is the program's own unless every one of its processes
// ended and the id came back within the same boot.
func EndLeftovers(id Identity) (int, error) {
	if id.Group <= 1 || id.LeaderStart == "" || id.Boot == "" {
		return 0, nil
	}
	boot, err := bootTime()
	if err != nil || boot != id.Boot {
		return 0, err
	}
	procs, err := listProcesses()
	if err != nil {
		return 0, err
	}
	var members []int
	for _, p := range procs {
		if p.group != id.Group {
			continue
		}
		if p.pid == id.Group && p.start != id.LeaderStart {
			return 0, nil // the id now leads another group
		}
		members = append(members, p.pid)
	}
	if len(members) == 0 {
		return 0, nil
	}
	_ = syscall.Kill(-id.Group, syscall.SIGKILL)
	for _, pid := range members {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	return len(members), nil
}

// listProcesses asks ps for every process with its group and start time, in a fixed locale and time zone, so the start
// time reads the same however the daemon is configured.
func listProcesses() ([]listedProcess, error) {
	out, _, err := Output(context.Background(), 10*time.Second, append(os.Environ(), "LC_ALL=C", "TZ=UTC"),
		systemTool("ps", "/bin/ps"), "-A", "-o", "pid=,pgid=,lstart=")
	if err != nil {
		return nil, err
	}
	var procs []listedProcess
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		group, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		procs = append(procs, listedProcess{pid: pid, group: group, start: strings.Join(f[2:], " ")})
	}
	return procs, nil
}

var bootSeconds = regexp.MustCompile(`sec = (\d+)`)

// bootTime identifies the current boot: kern.boottime on macOS, btime in /proc/stat on Linux.
func bootTime() (string, error) {
	if data, err := os.ReadFile("/proc/stat"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if rest, ok := strings.CutPrefix(line, "btime "); ok {
				return strings.TrimSpace(rest), nil
			}
		}
	}
	out, _, err := Output(context.Background(), 10*time.Second, nil, systemTool("sysctl", "/usr/sbin/sysctl", "/sbin/sysctl"),
		"-n", "kern.boottime")
	if err != nil {
		return "", err
	}
	if m := bootSeconds.FindStringSubmatch(out); m != nil {
		return m[1], nil
	}
	return "", os.ErrNotExist
}

// systemTool is the first of the known paths that exists, or the bare name for PATH to find: a user service's PATH
// may leave out the system directories.
func systemTool(name string, known ...string) string {
	for _, k := range known {
		if _, err := os.Stat(k); err == nil {
			return k
		}
	}
	return name
}
