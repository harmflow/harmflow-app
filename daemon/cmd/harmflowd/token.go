package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/secret"
	"github.com/harmflow/harmflow-app/daemon/internal/app"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// tokenRetries are the waits between reads of the credential store that fail at start; after the last, the daemon
// exits with an error and launchd's KeepAlive starts it again.
var tokenRetries = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}

// tokenStore is the credential store the token lives in: the Keychain, or a user-only file when
// HARMFLOW_TOKEN_STORE=file chooses it. HARMFLOW_KEYCHAIN names the keychain file to use instead of the default
// keychain, so a test keeps its token in a throwaway keychain (#79). The second value is the run/token an earlier
// daemon fell back to, to delete once the Keychain is read; it is empty when that file is the store.
func tokenStore(p app.Paths) (ports.SecretStore, string) {
	file := filepath.Join(p.Run, "token")
	if os.Getenv("HARMFLOW_TOKEN_STORE") == "file" {
		return secret.File{Path: file}, ""
	}
	return secret.Keychain{Service: secret.KeychainService, Account: secret.KeychainAccount,
		Path: os.Getenv("HARMFLOW_KEYCHAIN")}, file
}

// startupToken reads the local API token from store, and makes one only on a first start, when store holds none. A
// read that fails otherwise is retried after each of tokenRetries, then returned: the token never moves to another
// store and is never replaced by a start (FS-009 FR-011, FR-013).
func startupToken(store ports.SecretStore, leftover string, logger *log.Logger, sleep func(time.Duration)) (string, error) {
	token, err := store.Get()
	for _, wait := range tokenRetries {
		if err == nil || errors.Is(err, secret.ErrNoToken) {
			break
		}
		logger.Printf("credential store %s unreadable (%v); trying again in %s", store.Kind(), err, wait)
		sleep(wait)
		token, err = store.Get()
	}
	if errors.Is(err, secret.ErrNoToken) {
		if token, err = secret.NewToken(); err == nil {
			err = store.Set(token)
		}
	}
	if err != nil {
		return "", fmt.Errorf("token: credential store %s unavailable; exiting so launchd starts the daemon again: %w",
			store.Kind(), err)
	}
	if leftover != "" {
		if err := os.Remove(leftover); err == nil {
			logger.Printf("deleted %s, a token file an earlier start fell back to", leftover)
		} else if !errors.Is(err, os.ErrNotExist) {
			logger.Printf("could not delete %s: %v", leftover, err)
		}
	}
	return token, nil
}
