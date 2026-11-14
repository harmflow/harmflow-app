# Design tokens

## Overview

This package centralizes the interface's typed visual tokens and CSS token layers.

## Interface

- `color`, `toneColor`, `font`, and `Tone` expose the typed design-token surface. `[theme.ts:color]` `[theme.ts:toneColor]` `[theme.ts:font]` `[theme.ts:Tone]`
- `applyTypeface` switches the interface between the bundled fonts and the system's, and `applyStoredTypeface` applies the last choice before the settings arrive. `[typeface.ts:applyTypeface]` `[typeface.ts:applyStoredTypeface]`

## Invariants

- Tone values are constrained to the named union declared by `Tone`. `[theme.ts:Tone]`
- A missing or unknown typeface reads as the bundled fonts. `[typeface.ts:resolveTypeface]`

## Side Effects

- The TypeScript token module only exports in-memory values; the imported CSS files apply presentation rules at interface startup. `[theme.ts:color]` `[../main.tsx:root]`
- `applyTypeface` sets or removes the root element's `data-typeface` attribute, which `theme.css` reads, and keeps the choice in local storage for the first paint. `[typeface.ts:applyTypeface]`

## Verification

- Run: `pnpm -C desktop/ui test`
