# Application use cases

## Overview

This package coordinates task management, scheduling, execution, recovery, notifications, and settings.

## Interface

- `App`, `Deps`, and `New` define the application assembly and injectable dependencies. `[app.go:App]` `[app.go:Deps]` `[app.go:New]`
- Lifecycle methods start, stop, and request shutdown of the daemon. `[app.go:Start]` `[app.go:Stop]` `[app.go:RequestShutdown]`
- Task, execution, scheduling, template, settings, harness, notification, and spend use cases are exposed on `App`. `[tasks.go:ListTasks]` `[views.go:ListExecutions]` `[scheduler.go:RunTask]` `[templates.go:Templates]` `[settings.go:UpdateSettings]` `[harnesses.go:Harnesses]` `[notifications.go:Notifications]` `[spend.go:Spend]`

## Invariants

- `New` supplies defaults and creates the writable runtime directories before returning an `App`. `[app.go:New]`
- `Start` loads persisted state before background detection and loops; `Stop` stops detection and interrupts active work. `[app.go:Start]` `[app.go:Stop]`
- A shutdown without force is refused while an execution is active. `[app.go:RequestShutdown]`

## Side Effects

- Lifecycle methods load and persist state through injected ports, emit events, and run scheduler, watcher, and maintenance goroutines. `[app.go:Start]` `[app.go:Stop]`
- Settings updates can enable or disable the host service and persist settings. `[settings.go:UpdateSettings]`

## Verification

- Run: `go -C daemon test ./...`
