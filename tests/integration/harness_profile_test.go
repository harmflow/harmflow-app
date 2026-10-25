package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Codex's models cache is read from the daemon's CODEX_HOME, <home>/codex. Without it, a save with a Codex model the
// daemon cannot check goes through, with a warning at the profile's path; once the cache is there, the same model is
// refused.
func TestUncheckedCodexModelSavesWithAWarning(t *testing.T) {
	d := startDaemon(t, t.TempDir())
	created := d.call("POST", "/tasks", map[string]any{"yaml": task("pick", "Do it.", "")}, 201)
	unknown := strings.Replace(task("pick", "Do it.", "      model: no-such-model\n"),
		"provider: claude-code", "provider: codex", 1)
	saved := d.call("PUT", "/tasks/pick", map[string]any{"yaml": unknown, "baseRevision": created["revision"]}, 200)
	warned := false
	for _, w := range saved["warnings"].([]any) {
		w := w.(map[string]any)
		message, _ := w["message"].(string)
		if w["messageKey"] == "codex_profile_unchecked" && w["subject"] == "/spec/harness/preferred" && message != "" {
			warned = true
		}
	}
	if !warned {
		t.Errorf("the unchecked model saved with no warning: %v", saved["warnings"])
	}
	path, _ := d.call("GET", "/tasks/pick", nil, 200)["path"].(string)
	if onDisk, err := os.ReadFile(path); err != nil || !strings.Contains(string(onDisk), "model: no-such-model") {
		t.Errorf("the file at %q does not have the model: %v %s", path, err, onDisk)
	}

	cache := filepath.Join(d.home, "codex", "models_cache.json")
	if err := os.MkdirAll(filepath.Dir(cache), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, []byte(`{"models":[
		{"slug":"gpt-test","supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if status, body := d.request("POST", "/harnesses/refresh", nil, nil); status != 200 {
		t.Fatalf("refresh: %d %s", status, body)
	}
	refused := d.call("PUT", "/tasks/pick", map[string]any{"yaml": unknown, "baseRevision": saved["revision"]}, 422)
	if e, _ := refused["error"].(map[string]any); e["messageKey"] != "codex_model_unknown" {
		t.Errorf("with the cache: %v", refused)
	}
}
