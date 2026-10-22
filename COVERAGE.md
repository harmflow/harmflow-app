# Test coverage

What the tests of `harmflow-app` reach, as `make coverage` reads it. Coverage is held by a ratchet per package, not by
a target ([ADR-015](https://github.com/harmflow/harmflow-docs/blob/main/adr/ADR-015-coverage-held-by-a-ratchet.md)),
and this file keeps the reading the floors come from. **No floor holds yet**: nothing fails on a figure until the
second pull request of [harmflow/harmflow-app#99](https://github.com/harmflow/harmflow-app/issues/99) sets them.

A ratio is covered over total, as a percentage **truncated, never rounded, to two decimals**, computed from the counts
rather than from a percentage a tool prints. No figure is added up: each Go package, the interface, and the shell are
read on their own, and the three instruments are never averaged.

## Reading of 2026-10-22

| | |
|---|---|
| Code | `15ff200`. The change that adds this file touches none of the measured code. |
| Machine | macOS 26.7.1, arm64 |
| Commands | `make coverage-daemon`, `pnpm -C desktop test:coverage`, and `cargo llvm-cov` in `desktop/src-tauri` — together, `make coverage` |
| Tools | Go 1.24.4 · Node 24.2.0, pnpm 10.6.5, Vitest 5.0.5 with `@vitest/coverage-v8` 5.0.5 · rustc 1.93.0, cargo-llvm-cov 0.9.1 |

### Daemon — statements per package

The unit and integration data, merged. The unit tests run with `-coverpkg=./...`, so a statement any test of the
module reaches counts for its own package. The integration tests run the real `harmflowd`, built with `-cover`, and
each daemon writes its counters when it exits. Both sides count in `atomic` mode. `internal/contracts`, the schemas
`make generate` copies, is left out.

| Package | Covered | Total | Ratio |
|---|---:|---:|---:|
| `cmd/harmflowd` | 125 | 179 | 69.83% |
| `internal/adapters/catalog` | 37 | 74 | 50.00% |
| `internal/adapters/eventlog` | 160 | 187 | 85.56% |
| `internal/adapters/fsstore` | 90 | 126 | 71.42% |
| `internal/adapters/harness/classify` | 9 | 19 | 47.36% |
| `internal/adapters/harness/claudecode` | 284 | 358 | 79.32% |
| `internal/adapters/harness/codex` | 246 | 301 | 81.72% |
| `internal/adapters/harness/procutil` | 166 | 192 | 86.45% |
| `internal/adapters/httpapi` | 186 | 301 | 61.79% |
| `internal/adapters/launchd` | 1 | 33 | 3.03% |
| `internal/adapters/schema` | 116 | 148 | 78.37% |
| `internal/adapters/secret` | 11 | 34 | 32.35% |
| `internal/adapters/sysinfo` | 0 | 70 | 0.00% |
| `internal/adapters/tools` | 0 | 39 | 0.00% |
| `internal/app` | 1777 | 2272 | 78.21% |
| `internal/domain` | 492 | 553 | 88.96% |
| `internal/ports` | 3 | 4 | 75.00% |

### Interface — `desktop/ui`

The unit tests. Every source file under `src/` counts, those no test imports included, except
`src/api/generated.ts`, `src/main.tsx`, and the test files.

| Counter | Covered | Total | Ratio |
|---|---:|---:|---:|
| Statements | 1375 | 2295 | 59.91% |
| Branches | 1198 | 2131 | 56.21% |
| Functions | 442 | 840 | 52.61% |
| Lines | 1190 | 1916 | 62.10% |

### Shell — `desktop/src-tauri`

The unit tests: 8, in `daemon.rs`, `notice.rs`, and `sse.rs`. Covered is the total less the missed count
`cargo llvm-cov` reports. Branch counts need a nightly toolchain and are not read.

| Counter | Covered | Total | Ratio |
|---|---:|---:|---:|
| Lines | 127 | 695 | 18.27% |
| Functions | 21 | 124 | 16.93% |

## Taking a reading

`make coverage` needs `cargo-llvm-cov` and Rust's `llvm-tools-preview` (`cargo install cargo-llvm-cov`,
`rustup component add llvm-tools-preview`); the shell also needs the sidecar and the built interface, as for
`make test-desktop`. It writes:

- `build/coverage/daemon.out`, the merged profile, and prints the daemon's table above from it
  (`go -C tests run ./coverage build/coverage/daemon.out`);
- `desktop/ui/coverage/`, with `coverage-summary.json` holding the interface's counts and `index.html` the report by
  file;
- the shell's table, on the terminal.

Readings vary between identical runs. A daemon that the integration helper has to kill, after 10 seconds without
exiting on SIGTERM, writes no counters, so a slow run can read lower than a fast one; V8 can also count a branch
differently from one run to the next. A floor will be the lower of two readings of the same tree (ADR-015).
