# Secret-store adapter

## Overview

This package manages per-installation and harness tokens through Keychain or a user-only file.

## Interface

- `NewToken` creates local API tokens; `Keychain` and `File` implement the secret-store port. `[secret.go:NewToken]` `[secret.go:Keychain]` `[secret.go:File]` `[secret.go:Get]` `[secret.go:Set]` `[secret.go:Delete]`
- `Keychain.Exists` tells whether an item is there, for a harness that keeps its sign-in in the Keychain. `[secret.go:Exists]`
- Keychain item names and the absent-token error are exported. `[secret.go:KeychainService]` `[secret.go:KeychainAccount]` `[secret.go:ErrNoToken]`

## Invariants

- `NewToken` uses 32 random bytes and URL-safe base64 encoding. `[secret.go:NewToken]`
- File-backed tokens are written atomically with mode `0600`; Keychain writes pass the token on standard input rather than command arguments. `[secret.go:Set]`
- A missing item maps to `ErrNoToken`, while other Keychain failures remain errors. `[secret.go:keychainError]`
- `Exists` never asks `security` for the secret (`-w` or `-g`) and drops the attributes it prints. `[secret.go:existsArgs]`

## Side Effects

- Keychain storage invokes the macOS `security` tool; file storage creates, reads, writes, and removes the token file. `[secret.go:Get]` `[secret.go:Set]` `[secret.go:Delete]`

## Verification

- Run: `go -C daemon test ./...`
