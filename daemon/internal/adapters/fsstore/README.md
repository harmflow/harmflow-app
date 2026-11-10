# Filesystem store adapter

## Overview

This package implements file-backed task, state, and execution persistence.

## Interface

- `WriteAtomic` and `Revision` provide shared atomic-write and revision helpers. `[fsstore.go:WriteAtomic]` `[fsstore.go:Revision]`
- `Tasks`, `State`, and `Executions` implement the corresponding persistence ports. `[fsstore.go:Tasks]` `[fsstore.go:State]` `[fsstore.go:Executions]`

## Invariants

- `WriteAtomic` writes, syncs, closes, chmods, and renames a same-directory temporary file so a crash leaves complete old or new content. `[fsstore.go:WriteAtomic]`
- Task files are listed only when they are readable, non-hidden YAML files, ordered by identifier. `[fsstore.go:Tasks]`
- Task saves compare the expected revision before replacing content. `[fsstore.go:Save]`

## Side Effects

- Constructors create their backing directories; stores read, write, rename, and delete files. `[fsstore.go:NewTasks]` `[fsstore.go:NewState]` `[fsstore.go:NewExecutions]` `[fsstore.go:WriteAtomic]`

## Verification

- Run: `go -C daemon test ./...`
