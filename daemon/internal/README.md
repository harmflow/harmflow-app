# Internal daemon packages

## Overview

This directory separates the daemon's core model and use cases from infrastructure adapters.

## Subdirectories & Boundaries

- [app](./app/README.md) — orchestration use cases and in-memory lifecycle state.
- [domain](./domain/README.md) — task, execution, scheduling, and policy model.
- [ports](./ports/README.md) — interfaces implemented by infrastructure adapters.
- [contracts](./contracts/README.md) — generated, embedded schema copies.
- [adapters](./adapters/README.md) — filesystem, HTTP, harness, OS, and tool integrations.

## Invariants

- Use cases depend on abstractions supplied in `Deps`, rather than directly on vendor implementations. `[app/app.go:Deps]`

## Side Effects

- Concrete effects are isolated behind the ports that the application wires into `App`. `[app/app.go:Deps]` `[ports/ports.go:TaskRepository]`

## Verification

- Run: `go -C daemon test ./...`
