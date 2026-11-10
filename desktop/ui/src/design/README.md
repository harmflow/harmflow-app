# Design tokens

## Overview

This package centralizes the interface's typed visual tokens and CSS token layers.

## Interface

- `color`, `toneColor`, `font`, and `Tone` expose the typed design-token surface. `[theme.ts:color]` `[theme.ts:toneColor]` `[theme.ts:font]` `[theme.ts:Tone]`

## Invariants

- Tone values are constrained to the named union declared by `Tone`. `[theme.ts:Tone]`

## Side Effects

- The TypeScript token module only exports in-memory values; the imported CSS files apply presentation rules at interface startup. `[theme.ts:color]` `[../main.tsx:root]`

## Verification

- Run: `pnpm -C desktop/ui test`
