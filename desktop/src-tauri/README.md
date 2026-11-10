# Tauri shell package

## Overview

This package configures and builds the native desktop shell.

## Interface

- The Cargo package defines the `harmflow-desktop` library and binary build configuration. `[Cargo.toml:package]` `[Cargo.toml:lib]`
- The build entry invokes Tauri's build integration. `[build.rs:main]`
- [src](./src/README.md) — application runtime, daemon client, SSE bridge, tray, menu, and window behavior.

## Invariants

- The crate is not published and sets its Rust edition and minimum Rust version in the package manifest. `[Cargo.toml:package]`

## Side Effects

- The Tauri build script generates framework build artifacts. `[build.rs:main]`

## Verification

- Run: `cargo test --manifest-path desktop/src-tauri/Cargo.toml`
