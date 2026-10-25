# Go code guide

How Go code is written in `harmflow-app`: the daemon (`daemon/`), the tests that cross parts (`tests/`), and any Go
module added later.

## Precedence

When two sources disagree, the first one in this list wins:

1. This repository's `contracts/`.
2. This file.
3. The [Google Go Style Guide](https://google.github.io/styleguide/go/): its Guide, then its Decisions, then its Best
   practices.
4. [Effective Go](https://go.dev/doc/effective_go) and
   [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments).
5. The code around the change, where every source above is silent.

The Uber Go Style Guide counts only through the answers this file takes from it.

Where the code around a change breaks a rule here, the code you write follows the rule, and the rest of that code is
left for an issue of its own.

## Checking a change

CI runs these checks on the Go code; run them before you finish.

| Check | Command |
|---|---|
| Formatting | `gofmt -l daemon tests` prints nothing |
| Import groups | `make check-imports` |
| File and folder names, as `CLAUDE.md` sets them | `make check-file-names` |
| Vet and unit tests, under the race detector | `GOFLAGS=-race make test-daemon` |
| Integration tests and coverage floors | `make coverage-daemon` |

`make test-daemon` includes the tests of `daemon/internal/archtest`, which fail on an import that breaks the layers and
on an exported name of the daemon without a doc comment. No linter checks the other rules, so review holds them. A
change is done when every rule in this file holds in the code it adds or changes.

## Set by this repository

| Topic | Rule |
|---|---|
| Layers | The domain and the use cases import no package specific to Claude Code, Codex, GitHub, Tauri, or an operating system. An adapter translates capabilities, commands, events, and errors, and defines no business policy |
| Ports | The output ports are declared in `internal/ports`, used by the application layer, and implemented by the adapters |
| Behavior | The daemon orchestrates. What a task does lives in its prompt, agents, and skills, never in Go code |
| Time | The scheduler reads the time through the injectable clock and detects a wake through the wake monitor; tests replace both with fakes |
| Contracts | Keys, enums, routes, and event names come from `contracts/`. Struct tags follow them: camelCase keys, and snake_case values from a closed set. A struct that decodes a harness's own JSON keeps that harness's names |
| Generated code | The Go copies generated from `contracts/` are committed and change only through `make generate`; CI fails when one is stale |
| Texts for the user | Every text the daemon writes for the user is in en-US and carries a key from the contract's closed `MessageKey` set, with its params. A text the daemon does not author, such as the agent's words, tool output, or a tool's own error, gets no key |
| Modules | One Go module per Go part |
| Tests | Unit tests sit next to their code; tests that cross parts sit in `tests/` |
| Coverage | Each package is held at its last measured floor in `daemon/coverage-floors.txt`, as [`COVERAGE.md`](COVERAGE.md) describes. A new package needs no floor, and lowering a floor is a committed diff that gives its reason |

## Chosen where the guides disagree

| Topic | Rule |
|---|---|
| Wrapping an error | `%w` by default, at the end of the message, as in `fmt.Errorf("load task %q: %w", id, err)`. `%v` only to hide the cause on purpose |
| Line length | No limit for code. Comments wrap at 120 columns |
| Comparing values in tests | The standard library only: `==`, `slices.Equal`, `maps.Equal`, and `reflect.DeepEqual` for nested values. The modules take no assertion library and no `go-cmp` |
| Optional parameters | An option struct whose zero values are the defaults, as `app.Deps` |
| Capacity of slices and maps | A size hint only with a measured reason |
| Atomic values | The typed values of `sync/atomic`, such as `atomic.Bool` and `atomic.Int64` |
| Unexported package-level names | Plain names, with no `_` prefix |
| Import groups | Three groups separated by blank lines: the standard library, then third-party modules, then the module's own packages. `goimports -local github.com/harmflow/harmflow-app` writes them in that order |

## Adopted from the Uber guide

| Topic | Rule |
|---|---|
| Interface compliance | A type that implements a port asserts it at compile time, beside its declaration, as in `var _ ports.TaskRepository = (*Tasks)(nil)` |
| Channel size | A channel is unbuffered or has a buffer of one. Any other size carries a comment that says why it is that size |
| Goroutine leaks | The tests of a package that starts goroutines check that none outlives them, with `go.uber.org/goleak`: `goleak.VerifyTestMain` in `TestMain`, or `goleak.VerifyNone` in a test |

## Base rules the code touches most

| Topic | Rule |
|---|---|
| Handling an error | An error is either returned or logged, never both |
| Error strings | A Go `error` string starts in lowercase, unless it starts with a proper noun or an exported name, and ends with no punctuation. Texts for the user follow the rule [above](#set-by-this-repository) instead, and some, such as step labels, start in uppercase |
| Context | `ctx context.Context` is the first parameter of a function that needs one, and lives in no struct field |
| Goroutines | Every goroutine has a known moment when it stops, and the code that started it can wait for it |
| Ending the process | Only `main` ends the process. Other code returns an error, and an ordinary failure never panics |
| Package state | Code gets what it depends on passed in, rather than from package-level variables that change at run time. A value set at link time with `-ldflags -X`, such as the version, is not package state in this sense |
| Naming | MixedCaps; an initialism keeps one case (`ID`, `URL`, `HTTP`); one short receiver name per type; an accessor has no `Get` prefix; a package is named for what it provides, never `util`, `common`, or `helper`; a name does not repeat its package (`widget.New`, not `widget.NewWidget`) |
| Doc comments | Every exported name has a doc comment, in full sentences that begin with the name |
| Empty interface | `any`, not `interface{}` |
| Test layout | A table with `t.Run` subtests when the cases share their logic; a separate test when a case needs branches of its own |
| Test failures | A failure message names the call and gives what it got before what it wanted, as in `Parse(%q) = %v, want %v`. `t.Error` to keep going, and `t.Fatal` only when the rest of the test would mean nothing. A helper calls `t.Helper()` |
| Test doubles | Hand-written fakes |

## Changing this file

A rule here changes in a change of its own, made by the maintainers. A code change follows the rules as they stand.
