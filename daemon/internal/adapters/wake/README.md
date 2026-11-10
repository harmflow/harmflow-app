# Wake-monitor adapter

## Overview

This package detects machine wake events for scheduler misfire handling.

## Interface

- `Monitor` implements the wake-monitor port and is constructed by `NewMonitor`. `[wake.go:Monitor]` `[wake.go:NewMonitor]` `[wake.go:LastWake]`
- `Threshold` defines the minimum wall-versus-monotonic clock gap that counts as a suspension. `[wake.go:Threshold]`

## Invariants

- A gap larger than `Threshold` updates the remembered wake time; ordinary slow checks do not. `[wake.go:LastWake]`

## Side Effects

- The monitor reads clocks and updates only in-memory state. `[wake.go:NewMonitor]` `[wake.go:LastWake]`

## Verification

- Run: `go -C daemon test ./...`
