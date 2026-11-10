# Desktop application

## Overview

This blueprint indexes the JavaScript tooling, Tauri shell, and React interface of the desktop client.

## Subdirectories & Boundaries

- [scripts](./scripts/README.md) — icon rendering and local diagnostic utilities.
- [src-tauri](./src-tauri/README.md) — Rust/Tauri native shell.
- [ui](./ui/README.md) — webview interface package.

## Invariants

- The desktop package delegates interface development, tests, generation, icons, and type checks to its declared scripts. `[package.json:scripts]`

## Side Effects

- The package scripts can build the interface, generate API types, invoke Tauri, and render icon artifacts. `[package.json:scripts]`

## Verification

- Run: `pnpm -C desktop typecheck && pnpm -C desktop test`
