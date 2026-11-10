# API client and transports

## Overview

This package turns the local API contract into typed webview requests and event streams.

## Interface

- `api` exposes typed API operations and `ApiError` normalizes failed responses. `[client.ts:api]` `[client.ts:ApiError]`
- `Transport`, `transport`, and `isTauri` select HTTP or Tauri-native communication. `[transport.ts:Transport]` `[transport.ts:transport]` `[transport.ts:isTauri]`
- `native` exposes desktop-only capabilities with browser fallbacks. `[native.ts:native]`
- `types.ts` aliases generated OpenAPI types; `generated.ts` is the generated OpenAPI model. `[types.ts:Health]` `[generated.ts:paths]`

## Invariants

- The browser transport retains the highest received SSE ID and reconnects from it after a closed stream. `[transport.ts:HttpTransport]`
- The Tauri transport forwards requests through Rust commands so the daemon token stays outside the webview. `[transport.ts:TauriTransport]`

## Side Effects

- HTTP mode uses `fetch` and `EventSource`; Tauri mode dynamically invokes native commands and registers native event listeners. `[transport.ts:HttpTransport]` `[transport.ts:TauriTransport]` `[native.ts:native]`

## Verification

- Run: `pnpm -C desktop/ui test`
