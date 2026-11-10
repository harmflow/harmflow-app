# Desktop scripts

## Overview

This package holds local developer scripts for icon derivation and black-screen diagnostics.

## Interface

- `render-icons.mjs` exposes the icon renderer and ICO encoder used by the desktop icon script. `[render-icons.mjs:render]` `[render-icons.mjs:writeIco]`
- `capture-black-screen.sh` is a command-line diagnostic capture script. `[capture-black-screen.sh:epoch]`

## Invariants

- Icon rendering derives outputs from the SVG masters and cleans its temporary working directory in `finally`. `[render-icons.mjs:appIcon]` `[render-icons.mjs:render]`

## Side Effects

- The renderer calls external image tools and writes image files; the diagnostic script captures process, socket, and WebKit diagnostics into its output directory. `[render-icons.mjs:render]` `[render-icons.mjs:writeIco]` `[capture-black-screen.sh:epoch]`

## Verification

- Run: `pnpm -C desktop typecheck && pnpm -C desktop test`
