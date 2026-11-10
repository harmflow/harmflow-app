# Webview interface

## Overview

This directory packages the React/Vite interface rendered inside the Tauri webview or a development browser.

## Subdirectories & Boundaries

- [src](./src/README.md) — application source and UI contracts.

## Invariants

- The package scripts define Vite development/build, Vitest tests, and TypeScript checking. `[package.json:scripts]`

## Side Effects

- The derived API type file under `src/api` is overwritten by the sibling `codegen` package's generation script, not by these scripts. `[../codegen/package.json:generate]`

## Verification

- Run: `pnpm -C desktop/ui test`
