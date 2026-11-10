# launchd adapter

## Overview

This package implements daemon autostart as a macOS user agent.

## Interface

- `Agent` implements the host-service-management port. `[launchd.go:Agent]` `[launchd.go:NewAgent]` `[launchd.go:Mechanism]` `[launchd.go:Location]` `[launchd.go:Enabled]` `[launchd.go:Enable]` `[launchd.go:Disable]`
- `Label` names the launchd user agent. `[launchd.go:Label]`

## Invariants

- Autostart is opt-in: `Enable` writes a user-agent plist, while `Disable` removes it for subsequent logins. `[launchd.go:Enable]` `[launchd.go:Disable]`
- Enable and disable deliberately do not invoke `launchctl`, so a running daemon is not stopped by its own request. `[launchd.go:Enable]` `[launchd.go:Disable]`

## Side Effects

- The adapter writes or removes the user's launch-agent plist. `[launchd.go:Enable]` `[launchd.go:Disable]`

## Verification

- Run: `go -C daemon test ./...`
