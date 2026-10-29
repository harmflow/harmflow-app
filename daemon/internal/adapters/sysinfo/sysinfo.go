// Package sysinfo reads the gauges shown at the sidebar foot: CPU, memory, disk, and the size of HarmFlow's own
// directory. Values are best effort; a gauge that cannot be read is left out.
package sysinfo

import (
	"context"
	"io/fs"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// Reader is the SystemInfo. Between Start and Stop it samples CPU and memory every 3 seconds and the size of its
// directory every minute, each in a goroutine of its own, and Snapshot returns the last samples: a request never starts
// a process or walks the directory, and a slow sample holds up neither a request nor another gauge.
type Reader struct {
	dir string

	// The sources and intervals of the samples; New sets the real ones, and tests replace them.
	cpuSource  func(ctx context.Context) (float64, bool)
	memSource  func(ctx context.Context) (memSample, bool)
	sizeSource func(ctx context.Context, dir string) (int64, bool)
	fastEvery  time.Duration
	sizeEvery  time.Duration

	cpu  atomic.Pointer[float64]
	mem  atomic.Pointer[memSample]
	size atomic.Pointer[int64]

	mu      sync.Mutex
	stop    context.CancelFunc
	running sync.WaitGroup
}

// memSample is the memory's size and the part in use, which is 0 when vm_stat cannot be read.
type memSample struct {
	total, used int64
}

var _ ports.SystemInfo = (*Reader)(nil)

// New returns a Reader that samples the size of dir, HarmFlow's data directory. It samples nothing until Start.
func New(dir string) *Reader {
	return &Reader{
		dir:        dir,
		cpuSource:  cpuPercent,
		memSource:  memory,
		sizeSource: dirSize,
		fastEvery:  3 * time.Second,
		sizeEvery:  time.Minute,
	}
}

// Start begins sampling, at once and then at each gauge's interval, until ctx ends or Stop is called. A second Start
// before Stop does nothing.
func (r *Reader) Start(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stop != nil {
		return
	}
	ctx, r.stop = context.WithCancel(ctx)
	r.running.Add(3)
	go r.sample(ctx, r.fastEvery, func(ctx context.Context) {
		if v, ok := r.cpuSource(ctx); ok {
			r.cpu.Store(&v)
		}
	})
	go r.sample(ctx, r.fastEvery, func(ctx context.Context) {
		if v, ok := r.memSource(ctx); ok {
			r.mem.Store(&v)
		}
	})
	go r.sample(ctx, r.sizeEvery, func(ctx context.Context) {
		if v, ok := r.sizeSource(ctx, r.dir); ok {
			r.size.Store(&v)
		}
	})
}

// Stop ends the sampling and waits for its goroutines, a sample under way included, which its context cancels.
func (r *Reader) Stop() {
	r.mu.Lock()
	stop := r.stop
	r.mu.Unlock()
	if stop != nil {
		stop()
	}
	r.running.Wait()
}

func (r *Reader) sample(ctx context.Context, every time.Duration, take func(context.Context)) {
	defer r.running.Done()
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		take(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Snapshot implements ports.SystemInfo: CPU use, memory, the disk that holds dir, and the size of the Reader's
// directory, each under its key. A gauge it cannot read, or has not sampled yet, is left out.
func (r *Reader) Snapshot(dir string) map[string]any {
	out := map[string]any{}
	if cpu := r.cpu.Load(); cpu != nil {
		out["cpuPercent"] = *cpu
	}
	if mem := r.mem.Load(); mem != nil {
		out["memoryTotalBytes"] = mem.total
		out["memoryUsedBytes"] = mem.used
	}
	var st syscall.Statfs_t
	if syscall.Statfs(dir, &st) == nil {
		total := int64(st.Blocks) * int64(st.Bsize)
		free := int64(st.Bavail) * int64(st.Bsize)
		out["diskTotalBytes"] = total
		out["diskUsedBytes"] = total - free
	}
	if size := r.size.Load(); size != nil {
		out["harmflowDirBytes"] = *size
	}
	return out
}

func cpuPercent(ctx context.Context) (float64, bool) {
	out, err := exec.CommandContext(ctx, "/bin/ps", "-A", "-o", "%cpu=").Output()
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
	return min(sum/float64(runtime.NumCPU()), 100), true
}

func memory(ctx context.Context) (memSample, bool) {
	out, err := exec.CommandContext(ctx, "/usr/sbin/sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return memSample{}, false
	}
	total, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return memSample{}, false
	}
	vm, err := exec.CommandContext(ctx, "/usr/bin/vm_stat").Output()
	if err != nil {
		return memSample{total: total}, true
	}
	return memSample{total: total, used: usedMemory(string(vm))}, true
}

// usedMemory is the memory in use that vm_stat's output reports: the active, wired, and compressed pages.
func usedMemory(vmStat string) int64 {
	page := int64(4096)
	pages := map[string]int64{}
	for _, line := range strings.Split(vmStat, "\n") {
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
	return (pages["Pages active"] + pages["Pages wired down"] + pages["Pages occupied by compressor"]) * page
}

// dirSize is the size of the files under dir, skipping what it cannot read. A walk that ctx ends gives no sample.
func dirSize(ctx context.Context, dir string) (int64, bool) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
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
	return total, err == nil
}
