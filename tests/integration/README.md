# integration

Tests across the daemon's process boundary: each one starts the real `harmflowd` and drives it over its local API.

## Running them

```bash
make test-integration
```

It runs `go -C tests test -count=1 ./...`. The test binary builds its own `harmflowd` from `daemon/` before the first
test. By default the tests use the stand-in harnesses in [`testdata`](testdata), `fake-claude` and `fake-codex`, which
speak the real harnesses' output formats, so they need no harness installed and no account.

## Opt-in tests

A few tests start a real harness instead of a stand-in. Each one starts a real session and spends a little of the
account's usage, so it skips unless you opt in. The helper `realClaude` (`claude_code_test.go`) decides: it skips the
test unless `HARMFLOW_TEST_CLAUDE_CODE` is exactly `1` and `claude` is on the `PATH`. Every test that calls it belongs
in this table.

| Test | Opt in with | Needs |
|---|---|---|
| `TestClaudeCodeRefusesMakeInATask` (`claude_code_test.go`) | `HARMFLOW_TEST_CLAUDE_CODE=1` | A signed-in `claude` on the `PATH` |
| `TestFailedIssueIsRetriedUntilItsLimit` (`queue_retry_test.go`) | `HARMFLOW_TEST_CLAUDE_CODE=1` | A signed-in `claude` on the `PATH`, and an installed `gh` signed in with access to the sandbox repository its fixtures live in, which `queueRepo` names. The test also clones that repository over HTTPS, so `git` needs access too (for example through `gh auth setup-git`). The repository holds three open issues labelled `queue-test`, the oldest of them linked by an unmanaged draft pull request; the test fails when it finds anything else |

Run one of them on its own:

```bash
HARMFLOW_TEST_CLAUDE_CODE=1 go -C tests test -count=1 -run TestClaudeCodeRefusesMakeInATask ./integration/
```

**They run only on a local development machine** where a harness is installed and signed in. The GitHub Actions
runners have no real harness, so in CI these tests always skip: a green CI run says nothing about them.

## Under load

The nightly [Stress workflow](../../.github/workflows/stress.yml) runs the daemon's unit tests, with `-race`, and
these tests three times each, beside busy processes twice the runner's cores. **A test that fails there is a defect of
that test, and that load is the heaviest the suites are held to.** A development machine adds no load beside a test:
a change is proved with the tests it touches, a test of timing also alone and repeated, and the whole suites are the
pull request's checks. The workflow never runs on a pull request and gates nothing. Its run summary shows each suite's
result, duration, and load average, and the failing tests with their messages. Start it by hand with
`gh workflow run stress.yml`.

`make test-daemon` and `make test-integration` take more `go test` flags in `TEST_FLAGS`, passed after their own so
that a repeated flag wins. The workflow sets its `-count` and `-timeout` there, and a test of timing is repeated alone
the same way:

```bash
make test-integration TEST_FLAGS="-run TestRunStreamAndReplay -count=10"
```

## Internal variables

The other `HARMFLOW_TEST_*` variables are internal: a test binary sets them for another one it runs as a helper
process. Do not set them yourself.

| Variable | Tells the test binary it runs |
|---|---|
| `HARMFLOW_TEST_STARTER`, `HARMFLOW_TEST_STARTER_PID` | As the short-lived parent that starts a daemon for `TestDaemonOutlivesItsStarter` (`daemon_test.go`) |
| `HARMFLOW_TEST_WATCHDOG`, `HARMFLOW_TEST_WATCHDOG_GROUP` | As the watchdog that ends a daemon once its test process exits (`watchdog_test.go`) |
| `HARMFLOW_TEST_BINARY` | Against a `harmflowd` already built, instead of building its own (`watchdog_test.go`) |
| `HARMFLOW_TEST_HOLD`, `HARMFLOW_TEST_HOLD_HOME`, `HARMFLOW_TEST_HOLD_DETACHED` | `TestHoldADaemon`, the run `TestNoDaemonOutlivesAnInterruptedRun` interrupts (`watchdog_test.go`) |

The daemon's unit tests have their own, set the same way: `HARMFLOW_TEST_CRASH_POINT`, `HARMFLOW_TEST_CRASH_DIR`,
`HARMFLOW_TEST_CRASH_ID`, and `HARMFLOW_TEST_CRASH_CONTENT` (`daemon/internal/adapters/fsstore`), and
`HARMFLOW_TEST_TASK_FILE_CRASH_DIR` and `HARMFLOW_TEST_TASK_FILE_CRASH_CONTENT` (`daemon/internal/app`).
