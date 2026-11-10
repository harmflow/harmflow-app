# Domain model

## Overview

This package defines the daemon's task, execution, scheduling, quota, and policy model.

## Interface

- Task documents, harness profiles, and permissions are defined by `TaskDocument` and related types. `[task.go:TaskDocument]`
- Execution lifecycle records and status helpers are defined by `Execution`, `Attempt`, and `ExecutionStatus`. `[execution.go:Execution]` `[execution.go:Attempt]` `[execution.go:ExecutionStatus]`
- Scheduling, failover, path policy, quota, events, and reports are exposed through their respective value types and functions. `[schedule.go:ParseSchedule]` `[failover.go:DecideAfterAttempt]` `[path_rule.go:PathRule]` `[quota.go:Quota]` `[event.go:Event]` `[report.go:ExecutionReport]`

## Invariants

- Terminal executions cannot transition further, and active executions cannot return to `queued`. `[execution.go:CanTransition]`
- A schedule accepts either a cron expression or an interval, never both. `[schedule.go:ParseSchedule]`
- `PathRule.Check` admits only resolved paths in allowed areas and rejects protected locations. `[path_rule.go:Check]` `[path_rule.go:Forbidden]`
- Failover switches only for configured quota exhaustion or harness unavailability; other classified failures do not switch. `[failover.go:DecideAfterAttempt]`

## Side Effects

- `NewExecutionID` draws random bytes to make sortable execution identifiers. `[execution.go:NewExecutionID]`

## Verification

- Run: `go -C daemon test ./...`
