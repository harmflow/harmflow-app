# Test coverage

What the tests of `harmflow-app` reach, as `make coverage` reads it, and the floors that hold it. Coverage is held by a
ratchet per package, not by a target: each floor is a reading of this file, and CI fails the job that runs a
part's tests when the part reads below one.

A ratio is covered over total, as a percentage **truncated, never rounded, to two decimals**, computed from the counts
rather than from a percentage a tool prints. No figure is added up: each Go package, the interface, and the shell are
read and held on their own, and the three instruments are never averaged.

## The floors

| Part | Floors | Held by | CI job |
|---|---|---|---|
| Daemon, each Go package | [`daemon/coverage-floors.txt`](daemon/coverage-floors.txt) | `make coverage-daemon` | `integration` |
| Interface, `desktop/ui` | `thresholds` in [`desktop/ui/vite.config.ts`](desktop/ui/vite.config.ts) | `pnpm -C desktop test:coverage` | `desktop` |
| Shell, `desktop/src-tauri` | `RUST_FLOOR_LINES` and `RUST_FLOOR_FUNCTIONS` in the [`Makefile`](Makefile) | `make coverage-desktop` | `desktop` |

A Go package with no line in `daemon/coverage-floors.txt` is held at 0, so a new package never fails the build for
want of a floor. A floor whose package no longer has statements fails until its line is removed or renamed. A
violation names the part or package, the counter, the floor, and the ratio it read:

```text
coverage: internal/app: statements 84.92% (2056 of 2421) is below its floor of 99.00%
ERROR: Coverage for lines (67.7%) does not meet global threshold (99%)
coverage: desktop/src-tauri: lines 27.49% (218 of 793) is below its floor of 99.00%
```

The nightly [Coverage workflow](.github/workflows/coverage.yml) reads `main` and shows each reading beside its floor,
with the headroom above it, in its run's summary. It is a view, not the check: it runs with `HOLD_FLOORS=` empty, so it
fails only when a test does, and the floors stay held by CI.

### Moving a floor

A floor moves by hand, never by itself: Vitest's `autoUpdate` is not used.

- **Raising** one, when tests reach more: take a reading, set the floor to the new ratio, and edit its row in the
  tables below, in one commit.
- **Lowering** one is the same commit, plus the reason in writing, under *Floors lowered* below. A lowering is the
  only sanctioned way past a floor.

Either way, the floor is the lower of two readings of the same tree, so a reading that varies between runs never sets
a floor the next honest build misses.

## Reading of 2026-10-23

The floors come from this reading. Each floor equals its ratio. Where the runs differ, the table holds the lower:
`internal/adapters/harness/procutil` read 342 statements in every run on the Mac, and 341, 341, and 340 on the runner,
which is the figure here: its clean-up of a harness's process tree takes paths that depend on timing. And
`internal/adapters/httpapi` read 270 on the runner and 269, recorded, on the Mac. Every other figure was the same in
every run.

| | |
|---|---|
| Code | `8430c5a` for the daemon and the shell; `1ace9c7` for the interface. The change that sets the floors touches none of the measured code. |
| Machines | macOS 26.7.1, arm64, three runs; and CI's `macos-latest` runner, three runs ([1](https://github.com/harmflow/harmflow-app/actions/runs/39960071580), [2](https://github.com/harmflow/harmflow-app/actions/runs/39961213526), [3](https://github.com/harmflow/harmflow-app/actions/runs/39963361192)) |
| Commands | `make coverage-daemon`, `pnpm -C desktop test:coverage`, and `cargo llvm-cov` in `desktop/src-tauri` — together, `make coverage` |
| Tools | Go 1.24.4 · Node 24.2.0, pnpm 10.6.5, Vitest 5.0.5 with `@vitest/coverage-v8` 5.0.5 · rustc 1.93.0, cargo-llvm-cov 0.9.1 |

### Daemon — statements per package

The unit and integration data, merged. The unit tests run with `-coverpkg=./...`, so a statement any test of the
module reaches counts for its own package. The integration tests run the real `harmflowd`, built with `-cover`, and
each daemon writes its counters when it exits. Both sides count in `atomic` mode. `internal/contracts`, the schemas
`make generate` copies, is left out.

| Package | Covered | Total | Ratio | Floor |
|---|---:|---:|---:|---:|
| `cmd/harmflowd` | 148 | 195 | 75.89% | 75.89% |
| `internal/adapters/catalog` | 39 | 74 | 52.70% | 52.70% |
| `internal/adapters/eventlog` | 190 | 217 | 87.55% | 87.55% |
| `internal/adapters/fsstore` | 94 | 130 | 72.30% | 72.30% |
| `internal/adapters/harness/classify` | 9 | 19 | 47.36% | 47.36% |
| `internal/adapters/harness/claudecode` | 335 | 369 | 90.78% | 90.78% |
| `internal/adapters/harness/codex` | 276 | 312 | 88.46% | 88.46% |
| `internal/adapters/harness/procutil` | 340 | 394 | 86.29% | 86.29% |
| `internal/adapters/httpapi` | 269 | 358 | 75.13% | 75.13% |
| `internal/adapters/launchd` | 28 | 30 | 93.33% | 93.33% |
| `internal/adapters/schema` | 124 | 153 | 81.04% | 81.04% |
| `internal/adapters/secret` | 46 | 54 | 85.18% | 85.18% |
| `internal/adapters/sysinfo` | 0 | 70 | 0.00% | 0.00% |
| `internal/adapters/tools` | 0 | 39 | 0.00% | 0.00% |
| `internal/adapters/wake` | 14 | 14 | 100.00% | 100.00% |
| `internal/app` | 2056 | 2421 | 84.92% | 84.92% |
| `internal/domain` | 512 | 554 | 92.41% | 92.41% |
| `internal/ports` | 3 | 4 | 75.00% | 75.00% |

### Interface — `desktop/ui`

The unit tests. Every source file under `src/` counts, those no test imports included, except
`src/api/generated.ts`, `src/main.tsx`, and the test files.

Read again at `1ace9c7`, two runs on the Mac, identical: `790fde7` added code to the interface and its reading fell
below the one of `8430c5a` (1531 of 2341 statements), before any floor held. The interface's counts were the same on
the Mac and the runner at `8430c5a`. At `1ace9c7` the daemon reads at or above every floor, higher in several packages,
and the shell is unchanged; their floors stay those of `8430c5a`, and the daemon's headroom waits for the next reading.

| Counter | Covered | Total | Ratio | Floor |
|---|---:|---:|---:|---:|
| Statements | 1540 | 2357 | 65.33% | 65.33% |
| Branches | 1333 | 2173 | 61.34% | 61.34% |
| Functions | 519 | 875 | 59.31% | 59.31% |
| Lines | 1330 | 1966 | 67.65% | 67.65% |

### Shell — `desktop/src-tauri`

The unit tests. Covered is the total less the missed count `cargo llvm-cov` reports. Branch counts need a nightly
toolchain and are not read.

| Counter | Covered | Total | Ratio | Floor |
|---|---:|---:|---:|---:|
| Lines | 218 | 793 | 27.49% | 27.49% |
| Functions | 36 | 144 | 25.00% | 25.00% |

### Floors lowered

None yet.

## Taking a reading

`make coverage` needs `cargo-llvm-cov`, Rust's `llvm-tools-preview` (`cargo install cargo-llvm-cov`,
`rustup component add llvm-tools-preview`), and `jq`; the shell also needs the sidecar and the built interface, as for
`make test-desktop`. It fails at the first part that reads below a floor, after printing that part's figures, and
writes:

- `build/coverage/daemon.out`, the merged profile, and prints the daemon's table above from it
  (`go -C tests run ./coverage -floors daemon/coverage-floors.txt build/coverage/daemon.out`);
- `desktop/ui/coverage/`, with `coverage-summary.json` holding the interface's counts and `index.html` the report by
  file;
- the shell's table, on the terminal.

Readings vary between identical runs. A daemon that the integration helper has to kill, after 10 seconds without
exiting on SIGTERM, writes no counters, so a slow run can read lower than a fast one; V8 can also count a branch
differently from one run to the next. That is why a floor is the lower of two readings of the same tree. A floor at
the edge can still fail an honest build: the remedy is a new reading, not a flag.

The first reading, of 2026-10-22 at `15ff200`, taken before any floor held, is in this file's history.
