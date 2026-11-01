// Package secret keeps the local API token: in the macOS Keychain, through the security CLI so the desktop
// reads the same item without a prompt, or in a file only the user can read.
package secret

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/fsstore"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// KeychainService and KeychainAccount name the Keychain item of the token, the one the desktop reads too.
const (
	KeychainService = "com.harmflow.local-api"
	KeychainAccount = "token"
)

// NewToken returns a random per-installation token.
func NewToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// Keychain keeps the token in the default keychain, or in the keychain file at Path when it is set: a test points it at
// a throwaway keychain, so it never reaches the user's login keychain (#79).
type Keychain struct {
	Service, Account string
	Path             string
}

var _ ports.SecretStore = Keychain{}

// Kind implements ports.SecretStore: keychain.
func (k Keychain) Kind() string { return "keychain" }

// ErrNoToken means the store holds no token, which is a first start, and not a store it failed to read.
var ErrNoToken = errors.New("no token stored")

// Get implements ports.SecretStore: it reads the item through security, ErrNoToken when there is none.
func (k Keychain) Get() (string, error) {
	args := []string{"find-generic-password", "-s", k.Service, "-a", k.Account, "-w"}
	if k.Path != "" {
		if err := k.checkPath(); err != nil {
			return "", err
		}
		args = append(args, k.Path)
	}
	out, err := exec.Command("/usr/bin/security", args...).Output()
	if err != nil {
		return "", keychainError(err)
	}
	return strings.TrimSpace(string(out)), nil
}

// keychainError maps a failed security command. Only exit 44, errSecItemNotFound, means no token is stored: a locked
// keychain or a refused prompt exits with another code, which must not read as a first start that makes a new token.
// The command's message says why it failed.
func keychainError(err error) error {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return fmt.Errorf("keychain: %w", err)
	}
	if ee.ExitCode() == 44 {
		return ErrNoToken
	}
	return securityFailure(err, ee.Stderr)
}

// securityFailure is a failed security command's error: what it wrote to stderr, when anything, then its cause.
func securityFailure(err error, stderr []byte) error {
	if msg := strings.TrimSuffix(strings.TrimSpace(string(stderr)), "."); msg != "" {
		return fmt.Errorf("keychain: %s: %w", msg, err)
	}
	return fmt.Errorf("keychain: %w", err)
}

// checkPath refuses a keychain file that is missing or that security's interactive mode cannot read quoted: given a
// keychain it cannot open, add-generic-password writes to the default keychain instead, and would replace the user's
// token there (#79).
func (k Keychain) checkPath() error {
	if strings.ContainsAny(k.Path, "\"\\\n") {
		return fmt.Errorf("keychain: the path %q cannot be passed to security", k.Path)
	}
	if _, err := os.Stat(k.Path); err != nil {
		return fmt.Errorf("keychain: %w", err)
	}
	return nil
}

// Set writes through security's interactive mode so the token never appears in a process's arguments.
func (k Keychain) Set(token string) error {
	keychain := ""
	if k.Path != "" {
		if err := k.checkPath(); err != nil {
			return err
		}
		keychain = ` "` + k.Path + `"`
	}
	cmd := exec.Command("/usr/bin/security", "-i")
	cmd.Stdin = strings.NewReader(fmt.Sprintf("add-generic-password -U -s %s -a %s -l HarmFlow -w %s%s\n",
		k.Service, k.Account, token, keychain))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return securityFailure(err, stderr.Bytes())
	}
	got, err := k.Get()
	if err != nil {
		return err
	}
	if got != token {
		return errors.New("keychain: the stored token does not match")
	}
	return nil
}

// Delete implements ports.SecretStore: it removes the item through security; an item already gone is no error.
func (k Keychain) Delete() error {
	args := []string{"delete-generic-password", "-s", k.Service, "-a", k.Account}
	if k.Path != "" {
		if err := k.checkPath(); err != nil {
			return err
		}
		args = append(args, k.Path)
	}
	// Output, not Run, so a failure carries security's stderr.
	if _, err := exec.Command("/usr/bin/security", args...).Output(); err != nil {
		if err := keychainError(err); !errors.Is(err, ErrNoToken) {
			return err
		}
	}
	return nil
}

// File keeps the token in the file at Path, which only the user can read.
type File struct {
	Path string
}

var _ ports.SecretStore = File{}

// Kind implements ports.SecretStore: file.
func (f File) Kind() string { return "file" }

// Get implements ports.SecretStore: it reads the file, ErrNoToken when it does not exist.
func (f File) Get() (string, error) {
	data, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNoToken
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// Set implements ports.SecretStore: it writes the token to the file atomically, readable by the user alone.
func (f File) Set(token string) error {
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
		return err
	}
	return fsstore.WriteAtomic(f.Path, []byte(token+"\n"), 0o600)
}

// Delete implements ports.SecretStore: it removes the file; a file already gone is no error.
func (f File) Delete() error {
	if err := os.Remove(f.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
