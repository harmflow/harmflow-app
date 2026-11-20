# Gemini CLI adapter

## Overview

This package adapts Google's Gemini CLI to the harness port.

## Interface

- `Adapter` supplies detection, identification, profile validation, invocation preparation, and output parsing. `[geminicli.go:Adapter]` `[geminicli.go:Detect]` `[geminicli.go:Identify]` `[geminicli.go:ValidateProfile]` `[geminicli.go:Prepare]`
- The adapter publishes the Gemini CLI's harness identifier, display name, and required CLI flags. `[geminicli.go:ID]` `[geminicli.go:Name]` `[geminicli.go:requiredFlags]`

## Invariants

- A file is the Gemini CLI when it is named `gemini` and its `--version` output is a version number, since that output names no harness. `[geminicli.go:Identify]`
- Only the CLI's own sign-in reads as ready: detection checks that its credentials file exists in the CLI's `.gemini` folder, never reads it, and never starts a sign-in. `[geminicli.go:Detect]`
- The API-key variables are removed from every probe and run, never read. `[geminicli.go:withoutKeys]`
- `Prepare` runs the CLI non-interactively with `--output-format stream-json`, the prompt on stdin, and reports every permission setting it cannot enforce as not enforced. `[geminicli.go:Prepare]`
- No release has been tested against the real CLI yet, so every version runs with the untested-version warning. `[geminicli.go:Detect]`

## Side Effects

- Detection and invocations execute Gemini CLI processes. `[geminicli.go:Detect]` `[geminicli.go:Prepare]`

## Verification

- Run: `go -C daemon test ./internal/adapters/harness/geminicli/`
