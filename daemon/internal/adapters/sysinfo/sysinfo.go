// Package sysinfo reads the gauges shown at the sidebar foot: CPU, memory, disk, and the size of HarmFlow's own
// directory. Values are best effort; a gauge that cannot be read is left out.
package sysinfo

import (
	"io/fs"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

type Reader struct {
	mu       sync.Mutex
	dirBytes int64
	dirAt    time.Time
	cpu      float64
	cpuAt    time.Time
}

var _ ports.SystemInfo = (*Reader)(nil)

func (r *Reader) Snapshot(dir string) map[string]any {
	out := map[string]any{}
	if cpu, ok := r.cpuPercent(); ok {
		out["cpuPercent"] = cpu
	}
	if total, used, ok := memory(); ok {
		out["memoryTotalBytes"] = total
		out["memoryUsedBytes"] = used
	}
	var st syscall.Statfs_t
	if syscall.Statfs(dir, &st) == nil {
		total := int64(st.Blocks) * int64(st.Bsize)
		free := int64(st.Bavail) * int64(st.Bsize)
		out["diskTotalBytes"] = total
		out["diskUsedBytes"] = total - free
	}
	out["harmflowDirBytes"] = r.dirSize(dir)
	return out
}

func (r *Reader) cpuPercent() (float64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.cpuAt) < 3*time.Second {
		return r.cpu, true
	}
	out, err := exec.Command("/bin/ps", "-A", "-o", "%cpu=").Output()
	if err != nil {
		return 0, false
	}
	var sum float64
	for _, f := range strings.Fields(string(out)) {
		v, err := strconv.ParseFloat(strings.ReplaceAll(f, ",", "."), 64)
		if err == nil {
			sum += v
		}
	}
	pct := sum / float64(runtime.NumCPU())
	if pct > 100 {
		pct = 100
	}
	r.cpu, r.cpuAt = pct, time.Now()
	return pct, true
}

func memory() (int64, int64, bool) {
	out, err := exec.Command("/usr/sbin/sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return 0, 0, false
	}
	total, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return 0, 0, false
	}
	vm, err := exec.Command("/usr/bin/vm_stat").Output()
	if err != nil {
		return total, 0, true
	}
	page := int64(4096)
	pages := map[string]int64{}
	for _, line := range strings.Split(string(vm), "\n") {
		if strings.Contains(line, "page size of") {
			for _, f := range strings.Fields(line) {
				if n, err := strconv.ParseInt(f, 10, 64); err == nil {
					page = n
				}
			}
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(v), "."), 10, 64)
		if err == nil {
			pages[strings.TrimSpace(k)] = n
		}
	}
	used := (pages["Pages active"] + pages["Pages wired down"] + pages["Pages occupied by compressor"]) * page
	return total, used, true
}

func (r *Reader) dirSize(dir string) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.dirAt) < time.Minute {
		return r.dirBytes
	}
	var total int64
	// The walk skips what it cannot read and its callback returns no error, so it returns none.
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	r.dirBytes, r.dirAt = total, time.Now()
	return total
}
