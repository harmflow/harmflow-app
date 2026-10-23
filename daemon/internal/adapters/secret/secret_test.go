package secret

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Only security's "item not found" reads as no token stored; any other failure keeps its exit status and message, so
// a locked keychain never makes a new token (#15).
func TestKeychainError(t *testing.T) {
	cases := []struct {
		name, script string
		noToken      bool
		want         string
	}{
		{"item not found", "echo 'The specified item could not be found in the keychain.' >&2; exit 44", true, ""},
		{"locked keychain", "echo 'User interaction is not allowed.' >&2; exit 36", false,
			"keychain: exit status 36: User interaction is not allowed."},
		{"no message", "exit 51", false, "keychain: exit status 51"},
	}
	for _, c := range cases {
		_, err := exec.Command("/bin/sh", "-c", c.script).Output()
		got := keychainError(err)
		if errors.Is(got, ErrNoToken) != c.noToken {
			t.Errorf("%s: got %v", c.name, got)
		}
		if c.want != "" && got.Error() != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	if got := keychainError(exec.ErrNotFound); errors.Is(got, ErrNoToken) || !strings.HasPrefix(got.Error(), "keychain: ") {
		t.Errorf("security missing: got %v", got)
	}
}

// A keychain file that is missing, or whose path security's interactive mode cannot read quoted, is refused before
// security runs: given a keychain it cannot open, add-generic-password writes to the default keychain instead (#79).
func TestKeychainPathChecked(t *testing.T) {
	dir := t.TempDir()
	keychain := func(name string) Keychain {
		path := filepath.Join(dir, name)
		if !strings.Contains(name, "missing") {
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return Keychain{Service: KeychainService, Account: KeychainAccount, Path: path}
	}
	if err := keychain("throwaway keychain.keychain-db").checkPath(); err != nil {
		t.Errorf("a keychain file whose path has a space: %v", err)
	}
	if err := keychain("missing.keychain-db").checkPath(); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a missing keychain file: got %v", err)
	}
	for _, name := range []string{`quote".keychain-db`, `back\slash.keychain-db`, "new\nline.keychain-db"} {
		if err := keychain(name).checkPath(); err == nil || !strings.HasPrefix(err.Error(), "keychain: ") {
			t.Errorf("%q: got %v", name, err)
		}
	}
}
