# Workspace adapter

## Overview

This package derives checkpoint and repository-head information from an execution workspace.

## Interface

- `Reader` implements workspace marking, change detection, and repository-head lookup. `[workspace.go:Reader]` `[workspace.go:Mark]` `[workspace.go:Changes]` `[workspace.go:Heads]`

## Invariants

- Git repositories at the workspace root or one level below use Git status and commit comparison; a modification-time scan runs only when the root repository cannot be read. `[workspace.go:Changes]`
- Checkpoint paths are reported without reading file content. `[workspace.go:Changes]`

## Side Effects

- The adapter runs Git commands and scans workspace filesystem metadata. `[workspace.go:Mark]` `[workspace.go:Changes]` `[workspace.go:Heads]`

## Verification

- Run: `go -C daemon test ./...`
