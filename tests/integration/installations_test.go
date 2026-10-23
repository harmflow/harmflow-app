package integration

import (
	"os"
	"path/filepath"
	"testing"
)

// installed counts how often each path is among a harness's installations in GET /harnesses.
func installed(h map[string]any) map[string]int {
	counts := map[string]int{}
	list, _ := h["installations"].([]any)
	for _, p := range list {
		counts[jsonString(p)]++
	}
	return counts
}

// FS-004 FR-001, AC-002 (harmflow-app#70): with one Codex on the daemon's PATH and a second outside it, setting the
// second by hand offers both, and a run uses the second. Choosing the first then makes the next run use the first.
// Only the stand-ins are checked: a real Codex on the login shell's PATH can be offered too.
func TestChooseAmongInstallations(t *testing.T) {
	first, second := standIn(t, "fake-codex", "codex"), standIn(t, "fake-codex", "codex")
	d := startDaemon(t, t.TempDir(), "PATH="+filepath.Dir(first.path)+":"+os.Getenv("PATH"))

	h := d.setHarness("codex", second.path)
	if n := installed(h); n[first.path] != 1 || n[second.path] != 1 || h["installation"] != second.path ||
		h["pathSource"] != "manual" {
		t.Fatalf("with the second set by hand: installations %v, installation %v (%v); want both once, the second in use",
			h["installations"], h["installation"], h["pathSource"])
	}
	if x := d.runOn("onsecond", "codex"); x["status"] != "completed" {
		t.Fatalf("on the second: status %v, error %v", x["status"], x["errorMessage"])
	}
	if runs := second.only(t, append(codexProbes, codexRun)...)[codexRun]; runs != 1 {
		t.Errorf("the second ran %d times, want once", runs)
	}
	if runs := first.only(t, append(codexProbes, codexRun)...)[codexRun]; runs != 0 {
		t.Errorf("the first ran %d times before it was chosen", runs)
	}

	h = d.setHarness("codex", first.path)
	if n := installed(h); n[first.path] != 1 || h["installation"] != first.path || h["pathSource"] != "manual" {
		t.Fatalf("with the first chosen: installations %v, installation %v (%v); want the first once, in use",
			h["installations"], h["installation"], h["pathSource"])
	}
	if x := d.runOn("onfirst", "codex"); x["status"] != "completed" {
		t.Fatalf("on the first: status %v, error %v", x["status"], x["errorMessage"])
	}
	if runs := first.only(t, append(codexProbes, codexRun)...)[codexRun]; runs != 1 {
		t.Errorf("the first ran %d times, want once", runs)
	}
	if runs := second.only(t, append(codexProbes, codexRun)...)[codexRun]; runs != 1 {
		t.Errorf("the second ran %d times in all, want only the first run", runs)
	}
}
