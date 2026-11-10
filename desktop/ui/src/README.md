# Interface source

## Overview

This package mounts the React root and composes the dashboard application.

## Interface

- `main.tsx` mounts `Root` into the DOM root element. `[main.tsx:root]` `[App.tsx:Root]`
- `Root` and `App` compose error boundaries, screens, native capabilities, localization, and state synchronization. `[App.tsx:Root]` `[App.tsx:App]`
- [api](./api/README.md) — typed daemon access and transport selection.
- [components](./components/README.md) — reusable dashboard components.
- [design](./design/README.md) — visual tokens.
- [i18n](./i18n/README.md) — translations and language selection.
- [lib](./lib/README.md) — domain presentation helpers.
- [screens](./screens/README.md) — top-level dashboard screens.
- [state](./state/README.md) — external store and synchronization.

## Invariants

- The root is only rendered when the expected DOM element exists. `[main.tsx:root]`
- The application waits for initial rendering and zoom application before signaling native interface readiness. `[App.tsx:App]`

## Side Effects

- Startup opens synchronization, asks for notification permission, writes UI preferences to browser storage, and invokes native zoom/readiness commands. `[App.tsx:App]`

## Verification

- Run: `pnpm -C desktop/ui test`
