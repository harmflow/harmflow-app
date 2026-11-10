# Catalog adapter

## Overview

This package reads and resolves HarmFlow-format agents, skills, and templates.

## Interface

- `Parse` and `FrontMatter` expose Markdown front-matter parsing. `[catalog.go:Parse]` `[catalog.go:FrontMatter]`
- `ReadAgentCopy` produces the task-copy representation of an agent. `[catalog.go:ReadAgentCopy]` `[catalog.go:AgentCopy]`
- `Dir` resolves and lists catalog items and templates. `[catalog.go:Dir]` `[catalog.go:Item]` `[catalog.go:Items]` `[catalog.go:Templates]`
- `CopyTree` copies a catalog asset tree for delivery. `[catalog.go:CopyTree]`

## Invariants

- Missing or invalid front matter leaves the body available rather than rejecting the item. `[catalog.go:Parse]`
- An agent copy hashes the copied prompt, description, model, and skills. `[catalog.go:ReadAgentCopy]`

## Side Effects

- `ReadAgentCopy` reads an agent file and `CopyTree` copies filesystem content. `[catalog.go:ReadAgentCopy]` `[catalog.go:CopyTree]`

## Verification

- Run: `go -C daemon test ./...`
