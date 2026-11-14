# Tool-probing adapter

## Overview

This package discovers Git and the GitHub CLI for built-in GitHub work, and reads GitHub's count of the `gh` login's hourly rate limits for the sidebar's GitHub gauges.

## Interface

- `Prober` implements the tool-probing port and exposes `Probe`. `[tools.go:Prober]` `[tools.go:Probe]`
- `Prober` also implements the GitHub rate-limit port and exposes `RateLimit`. `[tools.go:RateLimit]`

## Invariants

- The probe reports Git first and then `gh`, checking their versions before reporting availability. `[tools.go:Probe]` `[tools.go:git]` `[tools.go:gh]`
- An unverified `gh` sign-in remains unknown unless its output explicitly says it is not logged in. `[tools.go:gh]`
- The rate-limit read takes REST from `resources.core` and GraphQL from `resources.graphql`, never the older `rate` field, and leaves out a bucket it cannot read: both when `gh` is missing, signed out, or fails. `[tools.go:RateLimit]` `[tools.go:bucket]`

## Side Effects

- The adapter searches executable paths and runs version and authentication commands; it does not install or sign in tools. `[tools.go:find]` `[tools.go:git]` `[tools.go:gh]`
- The rate-limit read runs `gh api rate_limit`, which GitHub counts against no limit; the adapter keeps no count of its own. `[tools.go:RateLimit]`

## Verification

- Run: `go -C daemon test ./...`
