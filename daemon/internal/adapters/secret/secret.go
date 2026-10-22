// Package secret keeps the local API token (D-090): in the macOS Keychain, through the security CLI so the desktop
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
)

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

type Keychain struct {
	Service, Account string
}

func (k Keychain) Kind() string { return "keychain" }

var ErrNoToken = errors.New("no token stored")

func (k Keychain) Get() (string, error) {
	out, err := exec.Command("/usr/bin/security", "find-generic-password", "-s", k.Service, "-a", k.Account, "-w").Output()
	if err != nil {
		return "", keychainError(err)
	}
	return strings.TrimSpace(string(out)), nil
}

// keychainError maps a failed security command. Only exit 44, errSecItemNotFound, means no token is stored: a locked
// keychain or a refused prompt exits with another code, which must not read as a first start that makes a new token
// (FS-009 FR-013). The command's message says why it failed.
func keychainError(err error) error {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return fmt.Errorf("keychain: %w", err)
	}
	if ee.ExitCode() == 44 {
		return ErrNoToken
	}
	if msg := strings.TrimSpace(string(ee.Stderr)); msg != "" {
		return fmt.Errorf("keychain: %w: %s", err, msg)
	}
	return fmt.Errorf("keychain: %w", err)
}

// Set writes through security's interactive mode so the token never appears in a process's arguments.
func (k Keychain) Set(token string) error {
	cmd := exec.Command("/usr/bin/security", "-i")
	cmd.Stdin = strings.NewReader(fmt.Sprintf("add-generic-password -U -s %s -a %s -l HarmFlow -w %s\n",
		k.Service, k.Account, token))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("keychain: %v %s", err, strings.TrimSpace(stderr.String()))
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

type File struct {
	Path string
}

func (f File) Kind() string { return "file" }

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

func (f File) Set(token string) error {
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
		return err
	}
	return fsstore.WriteAtomic(f.Path, []byte(token+"\n"), 0o600)
}
