# Command packages

## Overview

This directory groups executable command packages.

## Subdirectories & Boundaries

- [harmflowd](./harmflowd/README.md) — the daemon executable.

## Invariants

- Command packages remain the boundary that wires internal packages into executables. `[harmflowd/main.go:main]`

## Side Effects

- The contained command starts the daemon process and its local services through package `harmflowd`. `[harmflowd/main.go:main]` `[../harmflowd/harmflowd.go:Run]`

## Verification

- Run: `go -C daemon test ./...`
