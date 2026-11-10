# macOS packaging

## Overview

This package updates the locally installed macOS application bundle.

## Interface

- `update-app.sh` exposes `main` and supporting operations to locate the daemon, build, stop, stage, install, register, restore, and open the application. `[update-app.sh:main]` `[update-app.sh:find_daemon]` `[update-app.sh:build_app]` `[update-app.sh:stop_daemon]` `[update-app.sh:install_app]` `[update-app.sh:register_app]` `[update-app.sh:open_window]`

## Invariants

- The update flow finds the daemon through its runtime data rather than a process-name match. `[update-app.sh:find_daemon]`
- It checks running work before stopping the daemon, and restoration occurs after installation. `[update-app.sh:stats_running]` `[update-app.sh:stop_daemon]` `[update-app.sh:start_daemon]`

## Side Effects

- The script invokes Git, Make, the local API, launchctl-related logic, file moves, Launch Services registration, and app launching. `[update-app.sh:main]` `[update-app.sh:build_app]` `[update-app.sh:install_app]` `[update-app.sh:register_app]` `[update-app.sh:open_window]`

## Verification

- Run: `make test-packaging`
