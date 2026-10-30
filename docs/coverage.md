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
| Daemon, each Go package | [`daemon/coverage-floors.txt`](/daemon/coverage-floors.txt) | `make coverage-daemon` | `integration` |
| Interface, `desktop/ui` | `thresholds` in [`desktop/ui/vite.config.ts`](/desktop/ui/vite.config.ts) | `pnpm -C desktop test:coverage` | `desktop` |
| Shell, `desktop/src-tauri` | `RUST_FLOOR_LINES` and `RUST_FLOOR_FUNCTIONS` in the [`Makefile`](/Makefile) | `make coverage-desktop` | `desktop` |

A Go package with no line in `daemon/coverage-floors.txt` is held at 0, so a new package never fails the build for
want of a floor. A floor whose package no longer has statements fails until its line is removed or renamed. A
violation names the part or package, the counter, the floor, and the ratio it read:

```text
coverage: internal/app: statements 84.92% (2056 of 2421) is below its floor of 99.00%
ERROR: Coverage for lines (67.7%) does not meet global threshold (99%)
coverage: desktop/src-tauri: lines 27.49% (218 of 793) is below its floor of 99.00%
```

The nightly [Coverage workflow](/.github/workflows/coverage.yml) reads `main` and shows each reading beside its floor,
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

## Reading of 2026-10-28, on Linux

The daemon's and the shell's floors come from this reading, taken on CI's Linux runner, where CI holds them: this
repository's CI checks Linux only. Each floor equals its ratio. Where the runs differ, the table holds the lower:
`internal/app` read 2490 statements in the first two runs and 2489 in the third, which is the figure here, since its
timing-dependent paths do not run the same way every time. Every other figure was the same in every run. A test that
needs another platform skips on Linux (`go-code-guide.md`, "Tests by
platform"), so what only that platform runs is not counted here; *Floors lowered* below says which floors fell for it.
The interface's floors keep their reading, of 2026-10-23, below. The readings before this one, on macOS, are in this
file's history.

| | |
|---|---|
| Code | `243f308`, the merge of `db33f7d` onto `main` at `a26bb4e` that the pull request's checks run, and `b2e36f7`, the same merge of `d733ab5`, which changed only the floors. The changes that set the floors touch none of the measured code. |
| Machines | CI's `ubuntu-latest` runner, three runs: at `243f308`, [CI](https://github.com/harmflow/harmflow-app/actions/runs/40715413147) and [Coverage](https://github.com/harmflow/harmflow-app/actions/runs/40715413000); at `b2e36f7`, [CI](https://github.com/harmflow/harmflow-app/actions/runs/40716810888) |
| Commands | `make coverage-daemon` and `make coverage-desktop`, as CI runs them |
| Tools | Go 1.26.10 · Node 26.12.1 · rustc 1.99.1, cargo-llvm-cov 0.9.1 |

### Daemon — statements per package

The unit and integration data, merged. The unit tests run with `-coverpkg=./...`, so a statement any test of the
module reaches counts for its own package. The integration tests run the real `harmflowd`, built with `-cover`, and
each daemon writes its counters when it exits. Both sides count in `atomic` mode. `internal/contracts`, the schemas
`make generate` copies, is left out.

| Package | Covered | Total | Ratio | Floor |
|---|---:|---:|---:|---:|
| `cmd/harmflowd` | 165 | 213 | 77.46% | 77.46% |
| `internal/adapters/catalog` | 89 | 97 | 91.75% | 91.75% |
| `internal/adapters/eventlog` | 192 | 216 | 88.88% | 88.88% |
| `internal/adapters/fsstore` | 96 | 132 | 72.72% | 72.72% |
| `internal/adapters/harness/classify` | 10 | 19 | 52.63% | 52.63% |
| `internal/adapters/harness/claudecode` | 437 | 458 | 95.41% | 95.41% |
| `internal/adapters/harness/codex` | 455 | 492 | 92.47% | 92.47% |
| `internal/adapters/harness/procutil` | 248 | 426 | 58.21% | 58.21% |
| `internal/adapters/httpapi` | 285 | 365 | 78.08% | 78.08% |
| `internal/adapters/launchd` | 28 | 30 | 93.33% | 93.33% |
| `internal/adapters/schema` | 175 | 198 | 88.38% | 88.38% |
| `internal/adapters/secret` | 26 | 55 | 47.27% | 47.27% |
| `internal/adapters/sysinfo` | 0 | 70 | 0.00% | 0.00% |
| `internal/adapters/tools` | 44 | 44 | 100.00% | 100.00% |
| `internal/adapters/wake` | 14 | 14 | 100.00% | 100.00% |
| `internal/adapters/workspace` | 107 | 126 | 84.92% | 84.92% |
| `internal/app` | 2489 | 2851 | 87.30% | 87.30% |
| `internal/domain` | 587 | 627 | 93.62% | 93.62% |
| `internal/ports` | 3 | 4 | 75.00% | 75.00% |

`internal/adapters/tools` was read again on 2026-10-30, when its first tests came (harmflow-app#363), as the rest of
the table was: `make coverage-daemon` on CI's `ubuntu-latest` runner, the unit and integration data merged, at
`50b9960`, the merge of `6e4730d` onto `main` at `d1c8047` that the pull request's checks run. Two runs of that tree,
[CI](https://github.com/harmflow/harmflow-app/actions/runs/41116302061/job/124271575579) and its
[re-run](https://github.com/harmflow/harmflow-app/actions/runs/41116302061/job/124336997722), each read 44 of 44. The
change that set the floor touches none of the measured code.

### Shell — `desktop/src-tauri`

The unit tests. Covered is the total less the missed count `cargo llvm-cov` reports. Branch counts need a nightly
toolchain and are not read. The shell's code for macOS alone (`#[cfg(target_os = "macos")]`) does not build on Linux,
and is in neither count.

| Counter | Covered | Total | Ratio | Floor |
|---|---:|---:|---:|---:|
| Lines | 287 | 920 | 31.19% | 31.19% |
| Functions | 48 | 165 | 29.09% | 29.09% |

### Floors lowered

Both on 2026-10-28, when the floors moved to Linux, where this repository's CI checks. Every other floor rose with the
reading.

| Package | Was | Now | Why |
|---|---:|---:|---|
| `internal/adapters/harness/procutil` | 86.29% | 58.21% | The daemon reads the process table on macOS only. On Linux `tree_darwin.go` does not build, and the tests that find and end what a harness left outside its group skip (`needProcessTable`), so the clean-up paths that read the table run on macOS alone |
| `internal/adapters/secret` | 85.18% | 47.27% | The Keychain store calls `/usr/bin/security`, which only macOS has. The integration tests that run it against a throwaway keychain skip on Linux, so only the checks that need no keychain count |

## The interface's reading, of 2026-10-23

The interface's floors come from this reading. Its tests run no code of one platform alone, and on Linux at `243f308`
it reads above every floor (79.01% of statements).

| | |
|---|---|
| Code | `1ace9c7` |
| Machines | macOS 26.7.1, arm64, two runs, identical. The interface's counts were the same on the Mac and CI's runner at `8430c5a`, the reading before |
| Commands | `pnpm -C desktop test:coverage` |
| Tools | Node 24.2.0, pnpm 10.6.5, Vitest 5.0.5 with `@vitest/coverage-v8` 5.0.5 |

### Interface — `desktop/ui`

The unit tests. Every source file under `src/` counts, those no test imports included, except
`src/api/generated.ts`, `src/main.tsx`, and the test files.

`790fde7` added code to the interface and its reading fell below the one of `8430c5a` (1531 of 2341 statements),
before any floor held, so it was read again at `1ace9c7`.

| Counter | Covered | Total | Ratio | Floor |
|---|---:|---:|---:|---:|
| Statements | 1540 | 2357 | 65.33% | 65.33% |
| Branches | 1333 | 2173 | 61.34% | 61.34% |
| Functions | 519 | 875 | 59.31% | 59.31% |
| Lines | 1330 | 1966 | 67.65% | 67.65% |

## Taking a reading

`make coverage` needs `cargo-llvm-cov`, Rust's `llvm-tools-preview` (`cargo install cargo-llvm-cov`,
`rustup component add llvm-tools-preview`), and `jq`; it builds the sidecar the shell needs first, as
`make test-desktop` does. It fails at the first part that reads below a floor, after printing that part's figures, and
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
