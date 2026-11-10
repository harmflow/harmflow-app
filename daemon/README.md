# Daemon

## Overview

This blueprint indexes the Go daemon's entry point and layered implementation packages.

## Subdirectories & Boundaries

- [cmd](./cmd/README.md) — executable composition root.
- [harmflowd](./harmflowd/README.md) — daemon startup and registration point.
- [internal](./internal/README.md) — domain, use cases, ports, contracts, and adapters.

## Invariants

- The module's declared Go version and toolchain define the daemon build baseline. `[go.mod:go]` `[go.mod:toolchain]`

## Side Effects

- Runtime effects are implemented by the daemon and adapter packages rather than the module manifest itself. `[harmflowd/harmflowd.go:Run]` `[internal/adapters/httpapi/server.go:Listen]`

## Verification

- Run: `go -C daemon test ./...`
