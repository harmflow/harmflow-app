package sysinfo

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// blockingReader is a Reader whose sources give one sample each, then block until the sampling stops. started is
// closed once every source has entered its blocking sample.
func blockingReader(t *testing.T, dir string) (r *Reader, started <-chan struct{}) {
	t.Helper()
	blocked := make(chan struct{}, 3) // one per gauge, so no source waits to report it blocked
	block := func(ctx context.Context, calls *int) bool {
		*calls++
		if *calls == 1 {
			return true
		}
		blocked <- struct{}{}
		<-ctx.Done()
		return false
	}
	var cpuCalls, memCalls, sizeCalls int
	r = New(dir)
	r.cpuSource = func(ctx context.Context) (float64, bool) { return 42.5, block(ctx, &cpuCalls) }
	r.memSource = func(ctx context.Context) (memSample, bool) {
		return memSample{total: 1000, used: 400}, block(ctx, &memCalls)
	}
	r.sizeSource = func(ctx context.Context, _ string) (int64, bool) { return 7, block(ctx, &sizeCalls) }
	r.fastEvery, r.sizeEvery = time.Millisecond, time.Millisecond
	all := make(chan struct{})
	go func() {
		for range 3 {
			<-blocked
		}
		close(all)
	}()
	return r, all
}

// snapshotWithin calls Snapshot and fails the test when it does not answer within a second.
func snapshotWithin(t *testing.T, r *Reader, dir string) map[string]any {
	t.Helper()
	got := make(chan map[string]any, 1)
	go func() { got <- r.Snapshot(dir) }()
	select {
	case s := <-got:
		return s
	case <-time.After(time.Second):
		t.Fatal("Snapshot did not answer within a second while a sample was under way")
		return nil
	}
}

func TestSnapshotAnswersWithLastSampleWhileSamplesBlock(t *testing.T) {
	dir := t.TempDir()
	r, started := blockingReader(t, dir)
	r.Start(context.Background())
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		r.Stop()
		t.Fatal("the sources were not called a second time")
	}

	got := snapshotWithin(t, r, dir)
	want := map[string]any{"cpuPercent": 42.5, "memoryTotalBytes": int64(1000), "memoryUsedBytes": int64(400), "harmflowDirBytes": int64(7)}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("Snapshot(%q)[%q] = %v, want %v", dir, k, got[k], v)
		}
	}
	if _, ok := got["diskTotalBytes"]; !ok {
		t.Errorf("Snapshot(%q) has no diskTotalBytes, want the disk read at the request", dir)
	}

	stopped := make(chan struct{})
	go func() { r.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return while a sample was under way")
	}
}

func TestSnapshotLeavesOutGaugesNotSampledYet(t *testing.T) {
	dir := t.TempDir()
	r := New(dir)
	release := make(chan struct{})
	r.cpuSource = func(context.Context) (float64, bool) { <-release; return 1, true }
	r.memSource = func(context.Context) (memSample, bool) { <-release; return memSample{}, true }
	r.sizeSource = func(context.Context, string) (int64, bool) { <-release; return 1, true }
	r.Start(context.Background())
	defer r.Stop()
	defer close(release)

	got := snapshotWithin(t, r, dir)
	for _, k := range []string{"cpuPercent", "memoryTotalBytes", "memoryUsedBytes", "harmflowDirBytes"} {
		if v, ok := got[k]; ok {
			t.Errorf("Snapshot(%q)[%q] = %v, want it left out before its first sample", dir, k, v)
		}
	}
}

func TestStopEndsSamplingWhenTheContextEnds(t *testing.T) {
	r := New(t.TempDir())
	r.cpuSource = func(ctx context.Context) (float64, bool) { <-ctx.Done(); return 0, false }
	r.memSource = func(ctx context.Context) (memSample, bool) { <-ctx.Done(); return memSample{}, false }
	r.sizeSource = func(ctx context.Context, _ string) (int64, bool) { <-ctx.Done(); return 0, false }
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	r.Start(ctx) // a second Start starts nothing more
	cancel()
	r.Stop()
}

func TestStopWithoutStart(t *testing.T) {
	New(t.TempDir()).Stop()
}

func TestDirSize(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "a", "b"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, size := range map[string]int{"one": 3, filepath.Join("a", "two"): 5, filepath.Join("a", "b", "three"): 11} {
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got, ok := dirSize(context.Background(), dir); got != 19 || !ok {
		t.Errorf("dirSize(%q) = %d, %v, want 19, true", dir, got, ok)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := dirSize(ctx, dir); ok {
		t.Errorf("dirSize(ended context, %q) gave a sample, want none", dir)
	}
}

func TestUsedMemory(t *testing.T) {
	vmStat := "Mach Virtual Memory Statistics: (page size of 16384 bytes)\n" +
		"Pages free:                               100.\n" +
		"Pages active:                             10.\n" +
		"Pages wired down:                         2.\n" +
		"Pages occupied by compressor:             3.\n"
	if got, want := usedMemory(vmStat), int64(15*16384); got != want {
		t.Errorf("usedMemory(vm_stat output) = %d, want %d", got, want)
	}
}
