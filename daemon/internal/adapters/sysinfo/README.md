# System-information adapter

## Overview

This package supplies sampled machine and HarmFlow-directory gauges.

## Interface

- `Reader` is constructed with `New` and implements start, stop, and snapshot operations. `[sysinfo.go:Reader]` `[sysinfo.go:New]` `[sysinfo.go:Start]` `[sysinfo.go:Stop]` `[sysinfo.go:Snapshot]`

## Invariants

- Sampling starts once until `Stop`; requests read cached samples rather than initiating a new process or directory walk. `[sysinfo.go:Start]` `[sysinfo.go:Snapshot]`
- Missing or unavailable gauge values are represented as absent fields in the returned `SystemGauges`. `[sysinfo.go:Snapshot]`

## Side Effects

- Sampling runs its gauge readers in background goroutines; `Snapshot` synchronously reads filesystem statistics for its requested directory. `[sysinfo.go:Start]` `[sysinfo.go:cpuPercent]` `[sysinfo.go:Snapshot]`

## Verification

- Run: `go -C daemon test ./...`
