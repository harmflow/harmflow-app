# Settings sections

## Overview

This folder contains the sections the settings screen opens, one module per section, and the pieces they share.

## Interface

- Each built-in section is a component the settings screen renders when it is open. `[GeneralSection.tsx:GeneralSection]` `[NotificationsSection.tsx:NotificationsSection]` `[ShortcutsSection.tsx:ShortcutsSection]` `[AgentsSection.tsx:AgentsSection]` `[PathsSection.tsx:PathsSection]` `[DaemonSection.tsx:DaemonSection]` `[StorageSection.tsx:StorageSection]` `[SecuritySection.tsx:SecuritySection]` `[AboutSection.tsx:AboutSection]`
- `SettingRow` lays out one setting, and `usePatch` saves a change to the settings, for every section. `[SettingRow.tsx:SettingRow]` `[usePatch.ts:usePatch]`
- `DIRECTORIES` lists the daemon's folders and files Storage shows; the settings screen re-exports it. `[directories.ts:DIRECTORIES]`

## Invariants

- A module here never imports from the settings screen; what a section shares lives in this folder. `[SettingRow.tsx:SettingRow]` `[usePatch.ts:usePatch]` `[directories.ts:DIRECTORIES]`
- A section that holds a draft reports it through the `report` callback it receives, so leaving the screen asks first. `[AgentsSection.tsx:AgentsSection]` `[PathsSection.tsx:PathsSection]` `[StorageSection.tsx:StorageSection]`

## Side Effects

- Sections save settings through the local API and read them again, and toast the result. `[usePatch.ts:usePatch]`
- Sections call the native shell to change the tray and login item, start the daemon, ask for notification permission, and open folders. `[GeneralSection.tsx:GeneralSection]` `[DaemonSection.tsx:DaemonSection]` `[NotificationsSection.tsx:NotificationsSection]` `[StorageSection.tsx:StorageSection]`
- Security rotates the local API token after a confirmation. `[SecuritySection.tsx:SecuritySection]`

## Verification

- Run: `pnpm -C desktop/ui test`
