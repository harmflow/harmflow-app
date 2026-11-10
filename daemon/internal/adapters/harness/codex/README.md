# Codex adapter

## Overview

This package adapts the Codex CLI and its app server to the harness port.

## Interface

- `Adapter` supplies detection, profile validation, item resolution, invocation preparation, output parsing, quota reading, and app-server lifecycle operations. `[codex.go:Adapter]` `[codex.go:Detect]` `[codex.go:Prepare]` `[codex.go:ValidateProfile]` `[app_server.go:ReadQuota]` `[app_server.go:Close]`
- The adapter publishes the Codex harness identifier, display name, tested version ceiling, and required CLI flags. `[codex.go:ID]` `[codex.go:Name]` `[codex.go:TestedUpTo]` `[codex.go:requiredFlags]`

## Invariants

- Detection finds and identifies the CLI before reading advertised flags and skills. `[codex.go:Detect]`
- `Prepare` runs `codex exec --json`, translates permissions into sandbox controls, and keeps sessions only when requested and supported. `[codex.go:Prepare]` `[codex.go:KeepsSessions]`

## Side Effects

- Detection and invocations execute Codex processes; skills listing can retain an app-server process until `Close`. `[codex.go:Detect]` `[codex.go:Prepare]` `[app_server.go:Close]`

## Verification

- Run: `go -C daemon test ./...`
