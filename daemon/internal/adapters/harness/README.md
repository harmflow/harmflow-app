# Harness adapters

## Overview

This directory groups CLI-specific harness adapters and shared process helpers.

## Subdirectories & Boundaries

- [classify](./classify/README.md) — error-classification and text truncation helpers.
- [claudecode](./claudecode/README.md) — Claude Code CLI adapter.
- [codex](./codex/README.md) — Codex CLI and app-server adapter.
- [procutil](./procutil/README.md) — process, terminal, executable, and process-tree helpers.

## Invariants

- A harness adapter implements detection, profile validation, invocation preparation, and structured-output parsing. `[../../ports/ports.go:HarnessAdapter]` `[../../ports/ports.go:OutputParser]`

## Side Effects

- Prepared invocations are executed by the application through the declared invocation fields. `[../../ports/ports.go:Invocation]`

## Verification

- Run: `go -C daemon test ./...`
