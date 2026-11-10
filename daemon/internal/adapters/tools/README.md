# Tool-probing adapter

## Overview

This package discovers Git and the GitHub CLI for built-in GitHub work.

## Interface

- `Prober` implements the tool-probing port and exposes `Probe`. `[tools.go:Prober]` `[tools.go:Probe]`

## Invariants

- The probe reports Git first and then `gh`, checking their versions before reporting availability. `[tools.go:Probe]` `[tools.go:git]` `[tools.go:gh]`
- An unverified `gh` sign-in remains unknown unless its output explicitly says it is not logged in. `[tools.go:gh]`

## Side Effects

- The adapter searches executable paths and runs version and authentication commands; it does not install or sign in tools. `[tools.go:find]` `[tools.go:git]` `[tools.go:gh]`

## Verification

- Run: `go -C daemon test ./...`
