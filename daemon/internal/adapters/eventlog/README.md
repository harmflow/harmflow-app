# Event-log adapter

## Overview

This package persists execution events, supplies replay, and publishes live event streams.

## Interface

- `Log` implements the event-log port; `New` constructs it. `[eventlog.go:Log]` `[eventlog.go:New]`
- Append, replay, subscription, raw capture, and cleanup operations are exposed on `Log`. `[eventlog.go:Append]` `[eventlog.go:Replay]` `[eventlog.go:Subscribe]` `[eventlog.go:RawWriter]` `[eventlog.go:ReadRaw]` `[eventlog.go:DeleteLog]`
- `Redact` and `RedactFields` sanitize text and reachable mutable string fields. `[eventlog.go:Redact]` `[eventlog.go:RedactFields]`

## Invariants

- `Append` assigns monotonically increasing event IDs and applies redaction before persistence or publication. `[eventlog.go:Append]`
- A live subscriber that cannot keep up is closed rather than blocking the log. `[eventlog.go:Append]`

## Side Effects

- The adapter creates log directories, writes JSONL and raw-capture files, and sends events to subscribers. `[eventlog.go:New]` `[eventlog.go:Append]` `[eventlog.go:RawWriter]`

## Verification

- Run: `go -C daemon test ./...`
