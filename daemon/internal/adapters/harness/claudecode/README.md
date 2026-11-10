# Claude Code adapter

## Overview

This package adapts the installed Claude Code CLI to the harness port.

## Interface

- `Adapter` implements detection, profile validation, item lookup, invocation preparation, stream parsing, quota reading, and sign-in support. `[claudecode.go:Adapter]` `[claudecode.go:Detect]` `[claudecode.go:Prepare]` `[claudecode.go:UseToken]` `[quota.go:ReadQuota]` `[signin.go:SignIn]`
- The adapter publishes its harness identifier, display name, supported-version floor, and required CLI flags. `[claudecode.go:ID]` `[claudecode.go:Name]` `[claudecode.go:MinVersion]` `[claudecode.go:requiredFlags]`

## Invariants

- Detection verifies a discovered CLI's identity, version, required flags, capabilities, and sign-in state before declaring readiness. `[claudecode.go:Detect]`
- `Prepare` delivers built-in skills as an execution-scoped plugin and translates permissions into CLI controls. `[claudecode.go:Prepare]`
- A saved token is applied through the adapter's token environment variable rather than exposed by inventory values. `[claudecode.go:UseToken]` `[claudecode.go:withToken]`

## Side Effects

- Detection and prepared invocations run the Claude CLI; preparation can create plugin files under an execution directory. `[claudecode.go:Detect]` `[claudecode.go:Prepare]`

## Verification

- Run: `go -C daemon test ./...`
