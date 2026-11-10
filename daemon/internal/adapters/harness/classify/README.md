# Harness classification helpers

## Overview

This package classifies harness error text and bounds displayed text safely.

## Interface

- `Patterns.Text` maps configured error patterns to domain error classes. `[classify.go:Patterns]` `[classify.go:Text]`
- `Truncate` and `Tail` bound the start or end of UTF-8 text. `[classify.go:Truncate]` `[classify.go:Tail]`

## Invariants

- Classification checks quota before rate limits and returns task error when no configured pattern matches. `[classify.go:Text]`
- Text truncation and tailing avoid splitting UTF-8 sequences. `[classify.go:Truncate]` `[classify.go:Tail]`

## Side Effects

- The helpers only transform in-memory strings. `[classify.go:Text]` `[classify.go:Truncate]`

## Verification

- Run: `go -C daemon test ./...`
