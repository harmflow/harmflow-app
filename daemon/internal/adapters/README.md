# Infrastructure adapters

## Overview

This directory groups concrete implementations of the daemon's output ports.

## Subdirectories & Boundaries

- [catalog](./catalog/README.md) — built-in and user item discovery.
- [eventlog](./eventlog/README.md) — event persistence, replay, and redaction.
- [fsstore](./fsstore/README.md) — task, state, and execution filesystem persistence.
- [harness](./harness/README.md) — harness CLI adapters and process utilities.
- [httpapi](./httpapi/README.md) — authenticated loopback HTTP and SSE server.
- [launchd](./launchd/README.md) — macOS user-agent integration.
- [schema](./schema/README.md) — schema compilation and validation.
- [secret](./secret/README.md) — token stores.
- [sysinfo](./sysinfo/README.md) — sampled system gauges.
- [tools](./tools/README.md) — Git and GitHub CLI probing.
- [wake](./wake/README.md) — suspension detection.
- [workspace](./workspace/README.md) — Git-aware workspace checkpoints.

## Invariants

- Adapter implementations satisfy application-facing port interfaces rather than defining new use cases. `[../ports/ports.go:HarnessAdapter]`

## Side Effects

- This boundary owns filesystem, process, network, credential-store, and operating-system integration effects. `[../ports/ports.go:TaskRepository]` `[../ports/ports.go:EventLog]` `[../ports/ports.go:SecretStore]`

## Verification

- Run: `go -C daemon test ./...`
