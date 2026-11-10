# Process utilities

## Overview

This package provides process, pseudo-terminal, executable-discovery, and process-tree helpers for harness adapters.

## Interface

- `Process` is created through `Start`, `StartLines`, or `StartHeld` and exposes lifecycle and output methods. `[procutil.go:Process]` `[procutil.go:Start]` `[procutil.go:StartLines]` `[procutil.go:StartHeld]` `[procutil.go:Wait]`
- Executable discovery and command-output helpers are provided by `LookPath`, `Executable`, `IdentifyHarness`, `Output`, and `Help`. `[procutil.go:LookPath]` `[procutil.go:Executable]` `[procutil.go:IdentifyHarness]` `[procutil.go:Output]` `[procutil.go:Help]`
- `Terminal`, `Tree`, and `Identity` support interactive sessions and controlled process-tree cleanup. `[terminal.go:Terminal]` `[tree.go:Tree]` `[leftovers.go:Identity]`

## Invariants

- Process lifecycle state is exposed through a completion channel, exit code, signal, and a one-time release mechanism. `[procutil.go:Done]` `[procutil.go:ExitCode]` `[procutil.go:Signal]` `[procutil.go:Release]`
- Process-tree termination uses recorded process identity to avoid ending unrelated reused process identifiers. `[tree.go:EndStoredTree]` `[leftovers.go:Identify]`

## Side Effects

- The package starts commands and terminals, reads process information, and can signal process trees or leftover processes. `[procutil.go:Start]` `[terminal.go:StartTerminal]` `[tree.go:EndTree]` `[leftovers.go:EndLeftovers]`

## Verification

- Run: `go -C daemon test ./...`
