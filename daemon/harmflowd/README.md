# Package harmflowd

## Overview

This package starts the daemon and is its registration point: command `harmflowd` runs it with nothing registered, and a build that adds to the daemon calls `Run` from a main of its own with its registrations.

## Interface

- `Run` starts the daemon from a `Config` and returns once it has stopped. `[harmflowd.go:Run]` `[harmflowd.go:Config]`
- A `Registration` receives the starting `Daemon`, which exposes the installation's `Paths`, the daemon's `Version`, its `Event` stream through `Subscribe`, and `Handle` to add local API routes. `[harmflowd.go:Registration]` `[harmflowd.go:Daemon]` `[harmflowd.go:Paths]` `[harmflowd.go:Event]` `[harmflowd.go:Subscribe]` `[harmflowd.go:Handle]`
- A change to `Run`, `Config`, `Registration`, `Daemon`, `Paths`, or `Event` is a breaking change. `[harmflowd.go:harmflowd]`

## Invariants

- A running instance holds the daemon lock before it initializes the application, so a second daemon on the same data directory fails to start. `[harmflowd.go:Run]` `[harmflowd.go:acquireLock]`
- Registrations run once each, in order, before the local API listens and the use cases start; one that fails stops the start with nothing running. `[harmflowd.go:Run]`
- `Handle` adds routes only while the registrations run, behind the same Host, Origin, and token checks as every other route. `[harmflowd.go:Handle]`
- The local API token is read from its credential store and made only when the store holds none; a store that stays unreadable after the retries stops the start instead of replacing the token. `[token.go:startupToken]` `[token.go:tokenStore]`
- Runtime connection metadata is written with user-only permissions. `[harmflowd.go:writeRuntime]`

## Side Effects

- `Run` creates the data, runtime, and log directories, appends to `daemon.log`, brings the login agent up to date, starts the HTTP server and system sampler, and removes the runtime file at shutdown. `[harmflowd.go:Run]`
- `writeRuntime` persists the local server address and process metadata. `[harmflowd.go:writeRuntime]`
- SIGINT and SIGTERM stop the daemon at once; SIGQUIT asks for a draining stop. `[harmflowd.go:awaitStop]`

## Verification

- Run: `go -C daemon test ./harmflowd/...`
