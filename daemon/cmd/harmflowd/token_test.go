package main

import (
	"bytes"
	"errors"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/secret"
	"github.com/harmflow/harmflow-app/daemon/internal/app"
)

// fakeKeychain answers each Get with the next of reads, the last one repeating, and records what Set keeps.
type fakeKeychain struct {
	reads  []fakeRead
	gets   int
	set    []string
	setErr error
}

type fakeRead struct {
	token string
	err   error
}

func (f *fakeKeychain) Kind() string { return "keychain" }

func (f *fakeKeychain) Get() (string, error) {
	r := f.reads[min(f.gets, len(f.reads)-1)]
	f.gets++
	return r.token, r.err
}

func (f *fakeKeychain) Set(token string) error {
	f.set = append(f.set, token)
	return f.setErr
}

var errLocked = errors.New("keychain: exit status 36: User interaction is not allowed")

// startup runs startupToken with a leftover path in a fresh run directory, and records the waits and the log.
func startup(t *testing.T, store *fakeKeychain, leftover string) (token string, err error, waits []time.Duration, logged string) {
	t.Helper()
	var out bytes.Buffer
	token, err = startupToken(store, leftover, log.New(&out, "", 0), func(d time.Duration) { waits = append(waits, d) })
	return token, err, waits, out.String()
}

func runToken(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "run", "token")
}

// A Keychain that cannot be read is retried, then stops the start: it never moves the token to a file or makes a new
// one (#15, FS-009 FR-011, FR-013).
func TestStartupTokenKeychainErrorMakesNoTokenAndNoFile(t *testing.T) {
	store := &fakeKeychain{reads: []fakeRead{{err: errLocked}}}
	leftover := runToken(t)
	token, err, waits, logged := startup(t, store, leftover)
	if err == nil {
		t.Fatalf("got token %q and no error", token)
	}
	if errors.Is(err, secret.ErrNoToken) || !errors.Is(err, errLocked) {
		t.Errorf("error %v, want the Keychain's", err)
	}
	if store.gets != len(tokenRetries)+1 {
		t.Errorf("read the Keychain %d times, want %d", store.gets, len(tokenRetries)+1)
	}
	if !slices.Equal(waits, tokenRetries) {
		t.Errorf("waited %v, want %v", waits, tokenRetries)
	}
	if len(store.set) != 0 {
		t.Errorf("kept a new token %q in the Keychain", store.set)
	}
	if _, err := os.Stat(leftover); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s: %v, want no file", leftover, err)
	}
	if !strings.Contains(logged, "User interaction is not allowed") || !strings.Contains(logged, "launchd") {
		t.Errorf("log %q does not say why the daemon stops", logged)
	}
}

// A file an earlier fallback left stays while the Keychain cannot be read: the daemon touches nothing before it stops.
func TestStartupTokenKeychainErrorLeavesLeftoverFile(t *testing.T) {
	store := &fakeKeychain{reads: []fakeRead{{err: errLocked}}}
	leftover := runToken(t)
	writeFile(t, leftover, "file-token\n")
	if _, err, _, _ := startup(t, store, leftover); err == nil {
		t.Fatal("no error")
	}
	if got := readFile(t, leftover); got != "file-token\n" {
		t.Errorf("%s holds %q", leftover, got)
	}
}

// A read that fails, then succeeds, starts with the token the Keychain holds.
func TestStartupTokenRetriesTheKeychain(t *testing.T) {
	store := &fakeKeychain{reads: []fakeRead{{err: errLocked}, {err: errLocked}, {token: "kc-token"}}}
	token, err, waits, _ := startup(t, store, runToken(t))
	if err != nil || token != "kc-token" {
		t.Fatalf("got %q, %v", token, err)
	}
	if !slices.Equal(waits, tokenRetries[:2]) {
		t.Errorf("waited %v, want %v", waits, tokenRetries[:2])
	}
	if len(store.set) != 0 {
		t.Errorf("kept a new token %q in the Keychain", store.set)
	}
}

// Once the token is read from the Keychain, the run/token an earlier fallback left is deleted.
func TestStartupTokenRemovesLeftoverFile(t *testing.T) {
	store := &fakeKeychain{reads: []fakeRead{{token: "kc-token"}}}
	leftover := runToken(t)
	writeFile(t, leftover, "file-token\n")
	token, err, waits, _ := startup(t, store, leftover)
	if err != nil || token != "kc-token" {
		t.Fatalf("got %q, %v", token, err)
	}
	if _, err := os.Stat(leftover); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s: %v, want it deleted", leftover, err)
	}
	if len(waits) != 0 || len(store.set) != 0 {
		t.Errorf("waited %v and kept %q", waits, store.set)
	}
}

// The first start, with no token stored, makes one and keeps it in the Keychain.
func TestStartupTokenFirstStartMakesOne(t *testing.T) {
	store := &fakeKeychain{reads: []fakeRead{{err: secret.ErrNoToken}}}
	leftover := runToken(t)
	token, err, waits, _ := startup(t, store, leftover)
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || !slices.Equal(store.set, []string{token}) {
		t.Errorf("got %q, kept %q", token, store.set)
	}
	if len(waits) != 0 {
		t.Errorf("waited %v", waits)
	}
	if _, err := os.Stat(leftover); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s: %v, want no file", leftover, err)
	}
}

// A Keychain that refuses the first token stops the start too, with no file.
func TestStartupTokenFirstStartKeychainRefuses(t *testing.T) {
	store := &fakeKeychain{reads: []fakeRead{{err: secret.ErrNoToken}}, setErr: errLocked}
	leftover := runToken(t)
	if token, err, _, _ := startup(t, store, leftover); !errors.Is(err, errLocked) {
		t.Fatalf("got %q, %v", token, err)
	}
	if _, err := os.Stat(leftover); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s: %v, want no file", leftover, err)
	}
}

// With HARMFLOW_TOKEN_STORE=file the file is the store: a restart keeps its token and its file.
func TestStartupTokenFileStoreKeepsItsFile(t *testing.T) {
	t.Setenv("HARMFLOW_TOKEN_STORE", "file")
	paths := app.NewPaths(t.TempDir(), t.TempDir(), "")
	store, leftover := tokenStore(paths)
	if store.Kind() != "file" || leftover != "" {
		t.Fatalf("got a %s store and leftover %q", store.Kind(), leftover)
	}
	logger := log.New(&bytes.Buffer{}, "", 0)
	first, err := startupToken(store, leftover, logger, func(time.Duration) {})
	if err != nil {
		t.Fatal(err)
	}
	again, err := startupToken(store, leftover, logger, func(time.Duration) {})
	if err != nil || again != first {
		t.Fatalf("restart got %q, %v; want %q", again, err, first)
	}
	if got := readFile(t, filepath.Join(paths.Run, "token")); got != first+"\n" {
		t.Errorf("run/token holds %q", got)
	}
}

// By default the token lives in the Keychain, and run/token is only the file an earlier fallback may have left.
func TestTokenStoreDefaultsToKeychain(t *testing.T) {
	t.Setenv("HARMFLOW_TOKEN_STORE", "")
	paths := app.NewPaths(t.TempDir(), t.TempDir(), "")
	store, leftover := tokenStore(paths)
	if store.Kind() != "keychain" || leftover != filepath.Join(paths.Run, "token") {
		t.Errorf("got a %s store and leftover %q", store.Kind(), leftover)
	}
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
