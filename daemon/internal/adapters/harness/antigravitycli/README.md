# Antigravity CLI adapter

## Overview

This package adapts Google's Antigravity CLI (`agy`) to the harness port.

## Interface

- `Adapter` supplies detection, identification, profile validation, invocation preparation, and output parsing. `[antigravitycli.go:Adapter]` `[antigravitycli.go:Detect]` `[antigravitycli.go:Identify]` `[antigravitycli.go:ValidateProfile]` `[antigravitycli.go:Prepare]`
- The adapter publishes the CLI's harness identifier, display name, required flags, and the Keychain item that holds its sign-in. `[antigravitycli.go:ID]` `[antigravitycli.go:Name]` `[antigravitycli.go:requiredFlags]` `[antigravitycli.go:KeychainService]` `[antigravitycli.go:KeychainAccount]`

## Invariants

- A file is the Antigravity CLI when it is named `agy` and its `--version` output is a version number, since that output names no harness. `[antigravitycli.go:Identify]`
- Only the CLI's own sign-in reads as ready. `SignedIn`, which the daemon sets to the Keychain item's presence on macOS, never reads the secret; with no check, the sign-in reads unknown and the first run proves it. No sign-in is ever started. `[antigravitycli.go:Detect]`
- The credential variables (API keys, Google Cloud credentials) are removed from every probe and run, never read, and the CLI's self-update is off. `[antigravitycli.go:runEnv]`
- `Prepare` runs the CLI headless, reading one stream-json user message from stdin and writing stream-json, with its mode pinned to `accept-edits`. It never passes `--dangerously-skip-permissions`: the profile becomes `--settings` rules for that session alone. `[antigravitycli.go:Prepare]`
- With no one to approve, the CLI refuses an action no rule allows, so file tools stay in the workspace and its added folders, which are passed by their real paths. What a command may reach is not sandboxed and is reported as not enforced. `[antigravitycli.go:Prepare]` `[antigravitycli.go:resolved]`
- `read_only` denies file writes and commands outright, since the CLI adds the user's own allow rules to the session's and a denial wins. `[antigravitycli.go:Prepare]`
- A run that headless mode cut short, with no answer, is a task error carrying the CLI's notice. An account the CLI will not serve is an unavailable harness, not a sign-out, so the task fails over and no sign-in starts. `[antigravitycli.go:Finish]` `[antigravitycli.go:unusableTexts]`
- Only 1.5.3 and 1.5.15 have been run against, so every version runs with the untested-version warning. `[antigravitycli.go:Detect]`

## Side Effects

- Detection and invocations execute Antigravity CLI processes. `[antigravitycli.go:Detect]` `[antigravitycli.go:Prepare]`

## Verification

- Run: `go -C daemon test ./internal/adapters/harness/antigravitycli/`
