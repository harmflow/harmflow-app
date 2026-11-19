# Codex adapter

## Overview

This package adapts the Codex CLI and its app server to the harness port.

## Interface

- `Adapter` supplies detection, profile validation, item resolution, invocation preparation, output parsing, quota reading, and app-server lifecycle operations. `[codex.go:Adapter]` `[codex.go:Detect]` `[codex.go:Prepare]` `[codex.go:ValidateProfile]` `[app_server.go:ReadQuota]` `[app_server.go:Close]`
- The adapter publishes the Codex harness identifier, display name, tested version ceiling, and required CLI flags. `[codex.go:ID]` `[codex.go:Name]` `[codex.go:TestedUpTo]` `[codex.go:requiredFlags]`

## Invariants

- Detection finds and identifies the CLI before reading advertised flags and skills. `[codex.go:Detect]`
- `Prepare` runs every attempt through `codex app-server`: the attempt's parser starts a thread with the sandbox the permissions translate to and approvals off, then one turn with the instructions, and keeps the session only when requested. `[codex.go:Prepare]` `[run.go:parser]` `[codex.go:KeepsSessions]`
- An approval request Codex makes waits for the owner, and the parser sends back exactly the owner's decision — accept, accept for the session, or decline; Codex's `cancel` is not offered. A request of any other kind is refused, and Codex's automatic review is recorded, never answered. `[run.go:Answer]` `[run.go:request]`

## Side Effects

- Detection and invocations execute Codex processes; skills listing can retain an app-server process until `Close`. An attempt's app server ends when the parser closes its stdin, once the turn ended. `[codex.go:Detect]` `[codex.go:Prepare]` `[app_server.go:Close]` `[run.go:end]`

## Verification

- Run: `go -C daemon test ./...`
