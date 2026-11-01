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

A few tests start a real harness instead of a stand-in. Most of them start a real session and spend a little of the
account's usage; the detection tests of `daemon/internal/app` only run the installed CLI's own checks, but need it
installed. Each one skips unless you opt in. A helper named for the CLI decides:

- `realClaude` (`claude_code_test.go`) skips the test unless `HARMFLOW_TEST_CLAUDE_CODE` is exactly `1` and `claude`
  is on the `PATH`;
- `realCodex` (`codex_test.go`) skips the test unless `HARMFLOW_TEST_CODEX` is exactly `1` and `codex` is on the
  `PATH`;
- `realDesktop` (`window_close_test.go`) skips the test unless it runs on macOS, `HARMFLOW_TEST_DESKTOP` is exactly
  `1`, and `cargo` is on the `PATH`. It starts no harness: it builds the desktop shell's debug build, the only one with
  the control channel a test drives it through, and opens its real window.

A daemon package cannot import these helpers, so `daemon/internal/app` has its own `realClaude` and `realCodex`, which
read the same variables the same way. Every test that calls one of them belongs in this table.

| Test | Opt in with | Needs |
|---|---|---|
| `TestClaudeCodeRefusesMakeInATask` (`claude_code_test.go`) | `HARMFLOW_TEST_CLAUDE_CODE=1` | A signed-in `claude` on the `PATH` |
| `TestCitedUserItemsReachARealClaudeCode` (`claude_code_test.go`) | `HARMFLOW_TEST_CLAUDE_CODE=1` | A signed-in `claude` on the `PATH`. The test writes two agents and a skill into Claude Code's own configuration directory (`CLAUDE_CONFIG_DIR`, or `~/.claude`), where the signed-in CLI reads them, under names unique to the run, and removes them when it ends |
| `TestCitedUserItemsReachARealCodex` (`codex_test.go`) | `HARMFLOW_TEST_CODEX=1` | A signed-in `codex` on the `PATH`. The test runs the daemon with Codex's own home (`CODEX_HOME`, or `~/.codex`), writes an agent into its `agents/` under a name unique to the run, and removes it when it ends. The skill it cites is in the workspace's `.agents/skills/` |
| `TestSignedOutRealClaudeCodeOnlyProbes` (`detection_test.go`) | `HARMFLOW_TEST_CLAUDE_CODE=1` | An installed `claude` on the `PATH`, signed in or not. The test starts no session and spends no usage: the daemon runs with a configuration directory of the test's own (`CLAUDE_CONFIG_DIR`), where `claude` reads signed out, and with no API key or token in its environment. It never signs in or out, so the user's own sign-in stays as it is |
| `TestSignedOutRealCodexOnlyProbes` (`detection_test.go`) | `HARMFLOW_TEST_CODEX=1` | An installed `codex` on the `PATH`, signed in or not. The test starts no session and spends no usage: the daemon runs with a Codex home of the test's own (`CODEX_HOME`), where `codex` reads signed out, and with no API key or token in its environment. It never signs in or out, so the user's own sign-in stays as it is |
| `TestExcludedAndManagedIssuesAreNotNewWork` (`new_work_test.go`) | `HARMFLOW_TEST_CLAUDE_CODE=1` | What `TestFailedIssueIsRetriedUntilItsLimit` needs but its fixtures, and write access to the sandbox repository, to push a branch and open issues, labels, and a pull request. The test opens three issues labelled `new-work-test`, the first also labelled `new-work-test-excluded`, and, from a branch of its own under `new-work-test/`, a draft pull request labelled `harmflow` and `new-work-test` that links the second; it creates those labels when they are missing. When it ends, failed or not, it closes the pull request, deletes its branch, and closes the issues, found by that label; before it opens its fixtures, it does the same to what an earlier run left open under it. It never changes the `queue-test` fixtures |
| `TestResumePriorityDecidesBetweenADraftAndANewIssue` (`resume_priority_test.go`) | `HARMFLOW_TEST_CLAUDE_CODE=1` | What `TestFailedIssueIsRetriedUntilItsLimit` needs but its fixtures, and write access to the sandbox repository, to push a branch and open issues, labels, and a pull request. Each of its two subtests, `resume_first` and `new_first`, opens two issues labelled `resume-priority-test` and, from a branch of its own, a draft pull request labelled `harmflow` and `resume-priority-test` that links the first; it creates those labels when they are missing. A first execution through a copy of the stand-in `fake-claude` gives the first issue's work item a draft whose tests failed, so each subtest starts one real session, which is stopped once it has selected its issue. When a subtest ends, failed or not, it closes the pull request, deletes its branch, and closes the issues, found by that label; before it opens its fixtures, it does the same to what an earlier run left open under it. It never changes the other fixture sets |
| `TestResumePriorityDecidesBetweenWorkToRetryAndANewIssue` (`resume_priority_test.go`) | `HARMFLOW_TEST_CLAUDE_CODE=1` | What `TestFailedIssueIsRetriedUntilItsLimit` needs but its fixtures, and the right to open and close issues in the sandbox repository. Each of its two subtests, `resume_first` and `new_first`, opens two issues labelled `retry-priority-test`, creating that label when it is missing, and no pull request. A first execution through a copy of the stand-in `fake-claude` reports the first issue's work item failed with no pull request, so each subtest starts one real session, which is stopped once it has selected its issue. When a subtest ends, failed or not, it closes the issues, found by that label; before it opens its fixtures, it does the same to what an earlier run left open under it. It never changes the other fixture sets |
| `TestFailedIssueIsRetriedUntilItsLimit` (`queue_retry_test.go`) | `HARMFLOW_TEST_CLAUDE_CODE=1` | A signed-in `claude` on the `PATH`, and an installed `gh` signed in with access to the sandbox repository its fixtures live in, which `queueRepo` names. The test also clones that repository over HTTPS, so `git` needs access too (for example through `gh auth setup-git`). The repository holds three open issues labelled `queue-test`, the oldest of them linked by an unmanaged draft pull request; the test fails when it finds anything else |
| `TestEligibleIssueBecomesALinkedDraftPullRequest` (`draft_pull_request_test.go`) | `HARMFLOW_TEST_CLAUDE_CODE=1` | What `TestFailedIssueIsRetriedUntilItsLimit` needs, and write access to the sandbox repository: its clone has a push URL, and its session pushes a work branch and opens a draft pull request. The test opens a fixture issue labelled `draft-pr-test`, creating that label and `harmflow` when they are missing. When it ends, failed or not, it closes the pull request linking the issue, deletes its branch, and closes the issue; before it opens its fixture, it does the same to what an earlier run left open under that label. It never reads or changes the `queue-test` fixtures |
| `TestGitHubTaskStopsAtItsCheckWhenGhIsSignedOut` (`signed_out_gh_test.go`) | `HARMFLOW_TEST_CLAUDE_CODE=1` | A signed-in `claude` on the `PATH`, and an installed `gh`. The test clones the sandbox repository `queueRepo` names over HTTPS, as `TestFailedIssueIsRetriedUntilItsLimit` does, so `git` needs access to it. It signs `gh` out for its own daemon only: the daemon runs with an empty `GH_CONFIG_DIR` of the test's own, and without `GH_TOKEN`, `GITHUB_TOKEN`, `GH_ENTERPRISE_TOKEN`, or `GITHUB_ENTERPRISE_TOKEN`. It never runs `gh auth logout`, so the user's own sign-in stays as it is |
| `TestNoWorkExecutionRemovesEndedWorktrees` (`worktree_removal_test.go`) | `HARMFLOW_TEST_CLAUDE_CODE=1` | What `TestFailedIssueIsRetriedUntilItsLimit` needs, its fixtures included. The test also clones `harmflow/harmflow-app`, whose latest merged pull requests give it an issue that ended, and writes nothing to either repository |
| `TestDraftAtItsResumeLimitIsNotResumed` (`resume_limit_test.go`) | `HARMFLOW_TEST_CLAUDE_CODE=1` | What `TestFailedIssueIsRetriedUntilItsLimit` needs but its fixtures, and write access to the sandbox repository, to push a branch and open issues, labels, and a pull request. The test opens a fixture issue labelled `resume-limit-test` and, from a branch of its own whose one commit adds a file under `reminders/`, a draft pull request labelled `harmflow` that links the issue; it creates those labels when they are missing. Two executions through the stand-in `fake-claude` bring the issue's work item to the task's limit of one resumption, and the real session runs in a third, from a clone with no push URL. When it ends, failed or not, it closes the pull request linking the issue, deletes its branch, and closes the issue; before it opens its fixtures, it does the same to what an earlier run left open under that label. It never reads or changes the other fixture sets |
| `TestInventoryReportsARealClaudeCodeAndNoCodex` (`daemon/internal/app/detection_test.go`) | `HARMFLOW_TEST_CLAUDE_CODE=1` | An installed `claude` on the `PATH`, signed in or not. The test starts no session: detection runs the CLI's version, help, and sign-in checks with a temporary `HOME`, so they leave the user's configuration and sign-in as they are |
| `TestInventoryReportsARealCodexAndNoClaudeCode` (`daemon/internal/app/detection_test.go`) | `HARMFLOW_TEST_CODEX=1` | An installed `codex` on the `PATH`, signed in or not, and the `node` that runs an npm install of it. The test starts no session: detection runs the CLI's version, help, and sign-in checks with a temporary `HOME`, so they leave the user's configuration and sign-in as they are |
| `TestFailingTestsStillOpenADraftPullRequest` (`failing_tests_draft_test.go`) | `HARMFLOW_TEST_CLAUDE_CODE=1` | What `TestFailedIssueIsRetriedUntilItsLimit` needs, `node` and `npm` on the `PATH`, and write access to the sandbox repository: its session pushes a work branch and opens a draft pull request. The task's clone fetches from a local mirror of the sandbox whose default branch holds one more commit, the Node.js suite in [`testdata/sandbox-suite`](testdata/sandbox-suite), and pushes to GitHub, so the suite reaches the session's worktree and the pull request, and never the sandbox's default branch. The test opens a fixture issue labelled `retry-limit-test` whose change fails one of the suite's tests, creating that label and `harmflow` when they are missing. When it ends, failed or not, it closes the pull request linking the issue, deletes its branch, and closes the issue; before it opens its fixture, it does the same to what an earlier run left open under that label. It never reads or changes the `queue-test` fixtures |
| `TestRepairsOfFailingTestsStopAtTheTaskLimit` (`repair_limit_test.go`) | `HARMFLOW_TEST_CLAUDE_CODE=1` | What `TestFailingTestsStillOpenADraftPullRequest` needs, with the suite in [`testdata/repair-limit-suite`](testdata/repair-limit-suite) in place of its own: no `settings.json` passes all of that suite's tests, so they fail whatever the session changes in the code, and its task allows one test repair. The test counts the runs of `npm test`, the command the suite's `CONTRIBUTING.md` documents, in the harness's own tool events. Its fixture issue is labelled `repair-limit-test`, and it creates that label and `harmflow` when they are missing. When it ends, failed or not, it closes the pull request linking the issue, deletes its branch, and closes the issue; before it opens its fixture, it does the same to what an earlier run left open under that label. It never reads or changes the other fixture sets |
| `TestBuiltinSkillLoadsInARealClaudeCode` (`claude_code_test.go`) | `HARMFLOW_TEST_CLAUDE_CODE=1` | A signed-in `claude` on the `PATH`. The daemon's built-ins are a folder of the test's own, so the test writes nothing into Claude Code's own configuration directory (`CLAUDE_CONFIG_DIR`, or `~/.claude`). It reads that directory's `settings.json`, `CLAUDE.md`, `agents/`, `skills/`, and `plugins/` before and after the run, and fails if any file there was added, removed, or changed meanwhile, by the run or by anything else on the machine. It leaves out `plugins/synced/`, the plugins an organization manages, which Claude Code fetches again whenever a session starts |
| `TestBuiltinSkillLoadsInARealCodex` (`codex_test.go`) | `HARMFLOW_TEST_CODEX=1` | A signed-in `codex` on the `PATH`. The test runs the daemon with Codex's own home (`CODEX_HOME`, or `~/.codex`), and its built-ins are a folder of the test's own, so it writes nothing there. It reads that home's `config.toml`, `agents/`, and `skills/` before and after the run, and fails if any file there was added, removed, or changed meanwhile, by the run or by anything else on the machine |
| `TestRealClaudeCodeAndGhLeaveNoCredentialInTheHome` (`own_sign_in_test.go`) | `HARMFLOW_TEST_CLAUDE_CODE=1` | A signed-in `claude` on the `PATH`, and an installed `gh` signed in on the machine: the test reads the token `gh auth token` prints, and never prints it or any piece of it. Its one session, on `sonnet` at `low` effort, runs `gh api user --jq .login` and writes nothing to GitHub. Afterwards it reads every file under the daemon's fresh home, and fails on one that holds the token or a piece of it of eight characters or more, naming the file and the piece's offset alone |
| `TestRestartDuplicatesNoBranchOrPullRequest` (`restart_reconcile_test.go`) | `HARMFLOW_TEST_CLAUDE_CODE=1` | What `TestFailedIssueIsRetriedUntilItsLimit` needs, and write access to the sandbox repository: its clone keeps its push URL, and its sessions push a branch and open a draft pull request. For each of its two kill points it opens a fixture issue labelled `restart-test`, creating that label and `harmflow` when they are missing. When it ends, failed or not, it closes the pull requests of the issue, deletes their branches, and closes the issue. It never reads or changes the `queue-test` fixtures |
| `TestDraftWhoseTestsFailedIsResumedOnItsPullRequest` (`resume_draft_test.go`) | `HARMFLOW_TEST_CLAUDE_CODE=1` | What `TestFailedIssueIsRetriedUntilItsLimit` needs, and write access to the sandbox repository, to push a branch and open issues, labels, and a pull request: its clone keeps its push URL, and its session pushes to the draft pull request's branch and edits the pull request. The test opens a fixture issue labelled `resume-draft-test` and, from a branch of its own whose one commit adds a file under `messages/`, a draft pull request labelled `harmflow` that links the issue; it creates those labels when they are missing. A first execution through the stand-in `fake-claude` reports the draft's tests failed, and the real session runs in the second. When it ends, failed or not, it closes the pull requests linking the issue, deletes their branches and every other branch that holds `messages/`, and closes the issue; before it opens its fixtures, it does the same to what an earlier run left open under that label. It never reads or changes the other fixture sets |
| `TestNamedIssueAndNoSelectionFindTheirWork` (`issue_selection_test.go`) | `HARMFLOW_TEST_CLAUDE_CODE=1` | What `TestFailedIssueIsRetriedUntilItsLimit` needs but its fixtures, and the right to open and close issues in the sandbox repository: the test opens two issues labelled `issue-selection-test`, creating that label when it is missing, and closes them when it ends, failed or not. Before it opens them, it closes what an earlier run left open under that label. Its prompt with no selection reads every open issue of the sandbox, and may select a `queue-test` fixture, which it leaves as it is: each session is stopped once it has selected its issue |
| `TestPromptMarkingAndBranchNamingAreFollowed` (`prompt_marking_test.go`) | `HARMFLOW_TEST_CLAUDE_CODE=1` | What `TestFailedIssueIsRetriedUntilItsLimit` needs but its fixtures, and write access to the sandbox repository, to push a branch and open an issue, a label, and a pull request: its clone keeps its push URL, and its session pushes a work branch and opens a draft pull request. The test opens a fixture issue labelled `prompt-marking-test`, and its task's prompt names the branch `auto/<issue-number>` and the label `bot:auto` alone as the marking, in place of `harmflow`; it creates both labels when they are missing. When it ends, failed or not, it closes the pull request linking the issue, deletes its branch, and closes the issue, found by the issue and its label, never by the branch name; before it opens its fixture, it does the same to what an earlier run left open under that label. It never reads or changes the other fixture sets |
| `TestIssueAssignedMeanwhileIsLoggedAsAConflict` (`conflicting_activity_test.go`) | `HARMFLOW_TEST_CLAUDE_CODE=1` | What `TestFailedIssueIsRetriedUntilItsLimit` needs but its fixtures, and write access to the sandbox repository, to push a branch, open an issue, a label, and a pull request, and assign the issue: its clone keeps its push URL, and its session pushes a work branch and opens a draft pull request. The test opens a fixture issue labelled `conflict-test`, creating that label and `harmflow` when they are missing, and assigns it to the account `gh` is signed in with once the daemon tracks it as the execution's work item. When it ends, failed or not, it closes the pull request linking the issue, deletes its branch, and closes the issue, found by the issue and its label, never by the branch name; before it opens its fixture, it does the same to what an earlier run left open under that label. It never reads or changes the other fixture sets |
| `TestReadOnlyRealCodexWritesNothing` (`permissions_test.go`) | `HARMFLOW_TEST_CODEX=1` | A signed-in `codex` on the `PATH`. The daemon runs with Codex's own home (`CODEX_HOME`, or `~/.codex`). The test makes a folder of its own in the home folder, `~/harmflow-permissions-<random>`, outside `$TMPDIR` and `/tmp`, which a sandbox may leave writable, and outside `~/Library` and the home's dot-folders, where the daemon refuses a workspace: a repository, a worktree of it as the task's workspace, and a folder beside it. The session is asked to write one file in each. The test removes that folder when it ends |
| `TestWorkspaceWriteRealCodexWritesOnlyItsWorkspace` (`permissions_test.go`) | `HARMFLOW_TEST_CODEX=1` | What `TestReadOnlyRealCodexWritesNothing` needs |
| `TestReadOnlyRealClaudeCodeWritesNothing` (`permissions_test.go`) | `HARMFLOW_TEST_CLAUDE_CODE=1` | A signed-in `claude` on the `PATH`, and the folders `TestReadOnlyRealCodexWritesNothing` makes. The task runs on `sonnet` at the effort `low` |
| `TestWorkspaceWriteRealClaudeCodeEditsOnlyItsWorkspace` (`permissions_test.go`) | `HARMFLOW_TEST_CLAUDE_CODE=1` | What `TestReadOnlyRealClaudeCodeWritesNothing` needs. An editing allow rule of Claude Code's own settings that reaches the folder beside the workspace lets the Write tool through, and the test fails, as it should. A file there that a Bash call wrote is logged, not failed: Claude Code does not confine its shell to the workspace, as the execution's warning says |

| `TestClosingTheWindowFollowsTheTraySetting` (`window_close_test.go`) | `HARMFLOW_TEST_DESKTOP=1` | A Mac with a display, in a signed-in user session, `cargo` on the `PATH`, and the desktop's sidecar (`make sidecar`). The test builds the shell's debug build and starts it on its own daemon's home, with the stand-in `fake-claude`, so it spends no usage. The window opens on the screen for each of its two subtests, `tray_on` and `tray_off`; the test closes it through the shell's test control channel, which raises the same close request as the window's close button, so it needs no Accessibility permission. In CI and on Linux it skips |

Run one of them on its own:

```bash
HARMFLOW_TEST_CLAUDE_CODE=1 go -C tests test -count=1 -run TestClaudeCodeRefusesMakeInATask ./integration/
HARMFLOW_TEST_CLAUDE_CODE=1 go -C daemon test -count=1 -run '^TestInventoryReportsARealClaudeCodeAndNoCodex$' ./internal/app/
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
`HARMFLOW_TEST_TASK_FILE_CRASH_DIR` and `HARMFLOW_TEST_TASK_FILE_CRASH_CONTENT` (`daemon/internal/app`). The desktop
shell has its own, `HARMFLOW_TEST_SHELL_CONTROL`, which turns on its debug build's control channel
(`desktop/src-tauri/src/test_control.rs`) and which `TestClosingTheWindowFollowsTheTraySetting` sets.
