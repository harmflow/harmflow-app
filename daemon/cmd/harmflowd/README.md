# harmflowd

## Overview

This package is the daemon executable: it reads the command line and starts the daemon through package `harmflowd`, with nothing registered.

## Interface

- `main` exposes the `version`, `--home`, `--builtins`, and `--port` command-line entry points. `[main.go:main]`
- `main` passes the flags and the build's version, commit, and build time to `harmflowd.Run`, which constructs the application, local API server, storage adapters, harness adapters, and runtime lifecycle. `[main.go:main]` `[../../harmflowd/harmflowd.go:Run]`

## Invariants

- A running instance holds the daemon lock before it initializes the application. `[../../harmflowd/harmflowd.go:Run]` `[../../harmflowd/harmflowd.go:acquireLock]`
- Runtime connection metadata is written with user-only permissions. `[../../harmflowd/harmflowd.go:writeRuntime]`

## Side Effects

- `harmflowd.Run` creates runtime and log directories, starts the HTTP server and system sampler, and removes the runtime file at shutdown. `[../../harmflowd/harmflowd.go:Run]`
- `writeRuntime` persists the local server address and process metadata. `[../../harmflowd/harmflowd.go:writeRuntime]`

## Verification

- Run: `go -C daemon test ./...`
