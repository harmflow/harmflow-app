# Localization

## Overview

This package defines supported languages, translation resources, and runtime language selection.

## Interface

- `LANGUAGES`, `resources`, `systemLanguage`, `resolveLanguage`, and `applyLanguage` make up the localization API. `[index.ts:LANGUAGES]` `[index.ts:resources]` `[index.ts:systemLanguage]` `[index.ts:resolveLanguage]` `[index.ts:applyLanguage]`
- English, Spanish, and Brazilian Portuguese translation resources are exported by their locale modules. `[en-US.ts:enUS]` `[es-ES.ts:esES]` `[pt-BR.ts:ptBR]`

## Invariants

- Unsupported or absent language settings resolve through the operating-system candidates, with English as fallback. `[index.ts:resolveLanguage]` `[index.ts:systemLanguage]`
- Runtime i18next initialization uses the defined resources and English fallback. `[index.ts:i18n]`

## Side Effects

- `applyLanguage` changes i18next, updates the document language, and persists the preference when storage is available. `[index.ts:applyLanguage]`

## Verification

- Run: `pnpm -C desktop/ui test`
