package integration

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTool writes an executable shell script at path.
func writeTool(t *testing.T, path, script string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/bash\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// tool is the entry of id in GET /tools.
func (d *daemon) tool(id string) map[string]any {
	d.t.Helper()
	for _, tool := range d.list("/tools") {
		if tool["id"] == id {
			return tool
		}
	}
	d.t.Fatalf("GET /tools has no %s", id)
	return nil
}

// harmflow-app#363: the daemon's tools probe, read through GET /tools, finds the git and gh first on its PATH with
// their versions, and tells a signed-in gh from one that says it is signed out. The stand-in gh is signed in while a
// file beside it exists.
func TestToolsProbeReadsGhsSignIn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	git, gh, signedIn := filepath.Join(dir, "git"), filepath.Join(dir, "gh"), filepath.Join(dir, "signed-in")
	writeTool(t, git, `echo "git version 2.47.1"`+"\n")
	writeTool(t, gh, `case "$1" in
  --version) echo "gh version 2.83.0 (2026-10-01)" ;;
  auth)
    if [ -e "$(dirname "$0")/signed-in" ]; then
      echo "github.com"; echo "  ✓ Logged in to github.com account octocat (keyring)"
    else
      echo "You are not logged into any GitHub hosts. To log in, run: gh auth login" >&2; exit 1
    fi ;;
esac
`)
	if err := os.WriteFile(signedIn, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// The daemon puts its login shell's PATH before its own, and that one can hold a real git and gh, as /usr/bin on
	// CI's runner does. A shell that fails leaves the daemon's own PATH first, which starts with the stand-ins.
	shell := filepath.Join(t.TempDir(), "shell")
	writeTool(t, shell, "exit 1\n")
	d := startDaemon(t, t.TempDir(), "SHELL="+shell, "PATH="+dir+":"+os.Getenv("PATH"))

	g := d.tool("git")
	if g["detected"] != true || g["path"] != git || g["version"] != "2.47.1" || g["diagnosticKey"] != nil {
		t.Errorf("GET /tools git: detected %v, path %v, version %v, diagnostic %v; want detected, %s, 2.47.1, none",
			g["detected"], g["path"], g["version"], g["diagnosticKey"], git)
	}

	h := d.tool("gh")
	if h["detected"] != true || h["path"] != gh || h["version"] != "2.83.0" || h["authentication"] != "valid" ||
		h["diagnosticKey"] != nil {
		t.Errorf("GET /tools gh signed in: detected %v, path %v, version %v, authentication %v, diagnostic %v; "+
			"want detected, %s, 2.83.0, valid, none", h["detected"], h["path"], h["version"], h["authentication"],
			h["diagnosticKey"], gh)
	}

	if err := os.Remove(signedIn); err != nil {
		t.Fatal(err)
	}
	h = d.tool("gh")
	params, _ := h["diagnosticParams"].(map[string]any)
	if h["detected"] != true || h["path"] != gh || h["version"] != "2.83.0" || h["authentication"] != "invalid" ||
		h["diagnosticKey"] != "not_signed_in" || params["name"] != "gh" || params["command"] != "gh auth login" {
		t.Errorf("GET /tools gh signed out: detected %v, path %v, version %v, authentication %v, diagnostic %v %v; "+
			"want detected, %s, 2.83.0, invalid, not_signed_in naming gh and gh auth login", h["detected"], h["path"],
			h["version"], h["authentication"], h["diagnosticKey"], params, gh)
	}
}
