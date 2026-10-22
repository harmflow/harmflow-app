package secret

import (
	"errors"
	"os/exec"
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
