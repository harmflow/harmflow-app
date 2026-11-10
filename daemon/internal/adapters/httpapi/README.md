# Local HTTP API adapter

## Overview

This package serves the daemon's authenticated loopback HTTP API and event stream.

## Interface

- `Server` exposes listener setup, serving, shutdown, address reporting, token updates, and request handling. `[server.go:Server]` `[server.go:Listen]` `[server.go:Serve]` `[server.go:Shutdown]` `[server.go:Address]` `[server.go:SetToken]` `[server.go:Handler]`
- `Handler` registers the documented health, task, execution, settings, and SSE routes. `[server.go:Handler]`

## Invariants

- `Listen` binds only to `127.0.0.1`. `[server.go:Listen]`
- The handler routes requests through Host, Origin, and token checks; an unset token rejects every request. `[server.go:Handler]` `[server.go:SetToken]`

## Side Effects

- The server opens a TCP listener, serves HTTP/SSE traffic, and writes rejected-request diagnostics through its logger. `[server.go:Listen]` `[server.go:Serve]` `[server.go:Shutdown]`

## Verification

- Run: `go -C daemon test ./...`
