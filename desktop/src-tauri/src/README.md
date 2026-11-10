# Native shell runtime

## Overview

This package implements the Tauri runtime that mediates between the webview and the local daemon.

## Interface

- `run` configures the Tauri application, commands, plugins, tray, event stream, and window lifecycle. `[lib.rs:run]`
- Tauri commands proxy daemon requests and expose daemon, tray, file, and URL actions to the webview. `[lib.rs:api_request]` `[lib.rs:daemon_start]` `[lib.rs:set_tray_enabled]` `[lib.rs:write_text_file]` `[lib.rs:open_path]` `[lib.rs:open_url]`
- `DaemonClient`, `SseParser`, `AppState`, window functions, tray functions, and menu actions provide native subsystems. `[daemon.rs:DaemonClient]` `[sse.rs:SseParser]` `[state.rs:AppState]` `[window.rs:show_main]` `[tray.rs:create]` `[menu.rs:Action]`

## Invariants

- Daemon requests validate relative API paths and retry once after an unauthorized response or refused connection. `[daemon.rs:validate_path]` `[daemon.rs:send]`
- The desktop keeps the daemon token in Rust rather than exposing it to the webview. `[daemon.rs:Connection]` `[daemon.rs:connection]`
- The SSE bridge resumes from the last delivered event ID and emits a replay-gap signal when needed. `[sse.rs:stream_once]` `[state.rs:record_event_id]`
- A tray-enabled close hides the window while leaving the daemon independent of the UI process. `[window.rs:close_hides]` `[window.rs:exit_prevented]`

## Side Effects

- The runtime reads local daemon metadata and tokens, starts a sidecar daemon, performs local HTTP/SSE requests, writes selected files, invokes macOS `open`, and creates native tray/window/notification resources. `[daemon.rs:connection]` `[daemon.rs:spawn_daemon]` `[sse.rs:spawn]` `[lib.rs:write_text_file]` `[lib.rs:open_path]` `[tray.rs:create]`

## Verification

- Run: `cargo test --manifest-path desktop/src-tauri/Cargo.toml`
