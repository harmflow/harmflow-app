# integration

Tests across the daemon's process boundary: each one starts the real `harmflowd` and drives it over its local API.

## Running them

```bash
make test-integration
```

It runs `go -C tests test -count=1 -timeout 30m ./...`, the time limit the `Makefile`'s `INTEGRATION_TIMEOUT` sets for
this target and `make coverage-daemon` alike. The test binary builds its own `harmflowd` from `daemon/` before the first
test. By default the tests use the stand-in harnesses in [`testdata`](testdata), `fake-claude` and `fake-codex`, which
speak the real harnesses' output formats, so they need no harness installed and no account.

## In parallel

Every test calls `t.Parallel()` first, so `go test` runs as many at once as its `-parallel` flag allows, one per CPU
by default. Most of a test's time is waiting on its daemon (for a scheduler tick, a stand-in harness, a restart), and
the waits overlap. Each test starts its own daemon on a temporary home. The daemon picks its own port and keeps its
lock under that home, so no test sees another's tasks, executions, or settings. A new test does the same, and sets
nothing the whole test process shares, such as its environment.

These tests run alone, without `t.Parallel()`:

- **The tests that call `needProcessTable` or `throwawayKeychain`, which run on macOS only.** Neither CI nor the Stress
  workflow runs them, so nothing would show whether they hold up beside the others. Each throwaway keychain is also on
  the user's keychain search list while it lasts.
- **The opt-in tests below.** CI never runs them, they spend a real account's usage, and two of them use the same
  fixtures in the sandbox repository.
- **`TestHoldADaemon`**, which runs only as the helper process of the watchdog tests.

On a machine already busy, a run of the whole package can fail to start a daemon in time. To run fewer at once, use
`make test-integration TEST_FLAGS="-parallel 2"`.

## Opt-in tests

A few tests start a real harness instead of a stand-in. Each one starts a real session and spends a little of the
account's usage, so it skips unless you opt in. A helper named for the CLI decides:

- `realClaude` (`claude_code_test.go`) skips the test unless `HARMFLOW_TEST_CLAUDE_CODE` is exactly `1` and `claude`
  is on the `PATH`;
- `realCodex` (`codex_test.go`) skips the test unless `HARMFLOW_TEST_CODEX` is exactly `1` and `codex` is on the
  `PATH`.

Every test that calls one of them belongs in this table.

| Test | Opt in with | Needs |
|---|---|---|
| `TestClaudeCodeRefusesMakeInATask` (`claude_code_test.go`) | `HARMFLOW_TEST_CLAUDE_CODE=1` | A signed-in `claude` on the `PATH` |
| `TestCitedUserItemsReachARealClaudeCode` (`claude_code_test.go`) | `HARMFLOW_TEST_CLAUDE_CODE=1` | A signed-in `claude` on the `PATH`. The test writes two agents and a skill into Claude Code's own configuration directory (`CLAUDE_CONFIG_DIR`, or `~/.claude`), where the signed-in CLI reads them, under names unique to the run, and removes them when it ends |
| `TestCitedUserItemsReachARealCodex` (`codex_test.go`) | `HARMFLOW_TEST_CODEX=1` | A signed-in `codex` on the `PATH`. The test runs the daemon with Codex's own home (`CODEX_HOME`, or `~/.codex`), writes an agent into its `agents/` under a name unique to the run, and removes it when it ends. The skill it cites is in the workspace's `.agents/skills/` |
| `TestFailedIssueIsRetriedUntilItsLimit` (`queue_retry_test.go`) | `HARMFLOW_TEST_CLAUDE_CODE=1` | A signed-in `claude` on the `PATH`, and an installed `gh` signed in with access to the sandbox repository its fixtures live in, which `queueRepo` names. The test also clones that repository over HTTPS, so `git` needs access too (for example through `gh auth setup-git`). The repository holds three open issues labelled `queue-test`, the oldest of them linked by an unmanaged draft pull request; the test fails when it finds anything else |
| `TestNoWorkExecutionRemovesEndedWorktrees` (`worktree_removal_test.go`) | `HARMFLOW_TEST_CLAUDE_CODE=1` | What `TestFailedIssueIsRetriedUntilItsLimit` needs, its fixtures included. The test also clones `harmflow/harmflow-app`, whose latest merged pull requests give it an issue that ended, and writes nothing to either repository |

Run one of them on its own:

```bash
HARMFLOW_TEST_CLAUDE_CODE=1 go -C tests test -count=1 -run TestClaudeCodeRefusesMakeInATask ./integration/
```

A Codex test runs the same way with `HARMFLOW_TEST_CODEX=1`.

**They run only on a local development machine** where a harness is installed and signed in. The GitHub Actions
runners have no real harness, so in CI these tests always skip: a green CI run says nothing about them.

## Repeated and under load

The nightly [Stress workflow](../../.github/workflows/stress.yml) runs the daemon's unit tests, with `-race`, and
these tests in two jobs: ten times each with nothing beside them, which catches a test whose result depends on timing,
and three times each beside busy processes twice the runner's cores. **A test that fails there is a defect of that
test, and those runs are the repetitions and the heaviest load the suites are held to.** A development machine runs a
change's tests once and adds no load beside them: a change is proved with the tests it touches, and the whole suites
are the pull request's checks. The workflow never runs on a pull request and gates nothing. Each job's run summary
shows each suite's result, duration, and load average, and the failing tests with their messages. Start it by hand
with `gh workflow run stress.yml`.

`make test-daemon` and `make test-integration` take more `go test` flags in `TEST_FLAGS`, passed after their own so
that a repeated flag wins. The workflow sets its `-count` and `-timeout` there, and one test runs on its own the same
way:

```bash
make test-integration TEST_FLAGS="-run TestRunStreamAndReplay"
```

## Internal variables

The other `HARMFLOW_TEST_*` variables are internal: a test binary sets them for another one it runs as a helper
process. Do not set them yourself.

| Variable | Tells the test binary it runs |
|---|---|
| `HARMFLOW_TEST_STARTER`, `HARMFLOW_TEST_STARTER_PID`, `HARMFLOW_TEST_STARTER_LATE` | As the short-lived parent that starts a daemon for `TestDaemonOutlivesItsStarter` (`daemon_test.go`), writing its PID late for `TestNoDaemonOutlivesALatePID` (`watchdog_test.go`) |
| `HARMFLOW_TEST_WATCHDOG`, `HARMFLOW_TEST_WATCHDOG_GROUP` | As the watchdog that ends a daemon once its test process exits (`watchdog_test.go`) |
| `HARMFLOW_TEST_BINARY` | Against a `harmflowd` already built, instead of building its own (`watchdog_test.go`) |
| `HARMFLOW_TEST_HOLD`, `HARMFLOW_TEST_HOLD_HOME`, `HARMFLOW_TEST_HOLD_DETACHED` | `TestHoldADaemon`, the run `TestNoDaemonOutlivesAnInterruptedRun` interrupts and `TestNoDaemonOutlivesALatePID` lets fail (`watchdog_test.go`) |

The daemon's unit tests have their own, set the same way: `HARMFLOW_TEST_CRASH_POINT`, `HARMFLOW_TEST_CRASH_DIR`,
`HARMFLOW_TEST_CRASH_ID`, and `HARMFLOW_TEST_CRASH_CONTENT` (`daemon/internal/adapters/fsstore`), and
`HARMFLOW_TEST_TASK_FILE_CRASH_DIR` and `HARMFLOW_TEST_TASK_FILE_CRASH_CONTENT` (`daemon/internal/app`).
