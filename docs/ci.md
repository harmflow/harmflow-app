# CI and automation

> [!NOTE]
> This page describes the [`.github`](/.github) folder of harmflow-app. The repository's overview — what HarmFlow is,
> its architecture, the quick start, and the commands — is the [`README.md`](/README.md) at the root.

What GitHub reads from this folder: the workflows that check every change and every night, the scripts they run,
Dependabot's configuration, and the images the root README shows.

## Workflows

| Workflow | When it runs | What it does |
|---|---|---|
| [`ci.yml`](/.github/workflows/ci.yml) | Every pull request, every push to `main`, and by hand | Runs the jobs of the parts a pull request touches — daemon, integration, desktop, and the generated code — and every one of them on `main` or when `contracts/`, the `Makefile`, or `.github/` changes. The file-name check and the secret scan run on every change |
| [`coverage.yml`](/.github/workflows/coverage.yml) | Every night at 03:17 in São Paulo, by hand, and on a pull request that changes it | Reads the coverage of the daemon, the interface, and the shell on `main`, and shows each reading beside its floor in the run's summary. It never fails on a figure: the floors are held by CI |
| [`security.yml`](/.github/workflows/security.yml) | Mondays at 00:17 in São Paulo, by hand, and on a pull request that changes the scan | Scans the whole history for secrets, every branch and tag included, audits every part's dependencies, and checks that the Go toolchain is the newest patch of its minor |
| [`stress.yml`](/.github/workflows/stress.yml) | Every night at 03:47 in São Paulo, and by hand | Runs the daemon's unit tests and the integration suite ten times each with nothing beside them, and three times each beside a fixed CPU load, and shows in the run's summary how each suite did. It is no check: it never runs on a pull request or a push |
| [`release.yml`](/.github/workflows/release.yml) | A pushed version tag, `vMAJOR.MINOR.PATCH` with digits only | Checks that the tag is the version of all six version fields, then creates the tag's GitHub Release with generated notes. It builds and attaches nothing: the macOS build reaches the App Store separately |

Every job runs on Linux: CI checks the code as it is published here, and every other platform is built and checked
outside this repository. A test that needs another platform skips on Linux and says why.

## Scripts

| Script | Run by | What it does |
|---|---|---|
| [`check-version.sh`](/.github/scripts/check-version.sh) | `release.yml`, and `make check-version` | Fails, naming each one, when a version field of the desktop or the daemon differs from the tag |
| [`check-version-test.sh`](/.github/scripts/check-version-test.sh) | `make test-packaging` | Tests `check-version.sh` against a scratch checkout with one field changed at a time, and against this repository's own files |
| [`coverage-summary.sh`](/.github/scripts/coverage-summary.sh) | `coverage.yml` | Prints one part's coverage reading as Markdown, beside the floors that hold it, and writes the part's HTML report into `build/coverage` |
| [`go-toolchain.sh`](/.github/scripts/go-toolchain.sh) | `make check-go-toolchain` in `security.yml`, and `make go-toolchain` | `check` fails when a newer patch of the Go minor exists or Go no longer supports the minor; `update` raises the toolchain line of `daemon/go.mod` and `tests/go.mod` to that patch |
| [`install-gitleaks.sh`](/.github/scripts/install-gitleaks.sh) | `ci.yml` and `security.yml` | Installs a pinned gitleaks release on a Linux runner, checked against its SHA-256 before it runs |
| [`stress.sh`](/.github/scripts/stress.sh) | `stress.yml` | Starts and stops the CPU load, runs each suite beside it, and prints the run's summary from what it wrote to `build/stress` |

## Dependabot

[`dependabot.yml`](/.github/dependabot.yml) proposes monthly updates to the pinned actions and to the Go, pnpm, and
Cargo dependencies. In the Go, pnpm, and Cargo ecosystems, the minor and patch updates come in one pull request and each
major update comes alone, reviewed on its own; the actions come in one pull request whatever the update. The `tauri*` crates and the `@tauri-apps/*` packages come in two pull requests that are
merged together: Tauri's build checks that the two share a major and minor, so a half-merged pair fails `make app`.

## Assets

[`assets/`](/.github/assets) holds the banner and the architecture diagram the root README shows.

## Rules

- **Pin every action to a full commit SHA**, with the release it corresponds to in a comment, so a moved tag cannot
  change what runs. Dependabot proposes the updates, except for two pins it cannot see, which are refreshed by hand:
  `dtolnay/rust-toolchain`, a commit of its `stable` branch, and the gitleaks version and checksum in
  `install-gitleaks.sh`.
- **Every workflow reads only:** `permissions: contents: read`. The one job that writes, `release.yml`'s GitHub
  Release, asks for `contents: write` on itself and nowhere else.
- **A change here runs every CI job**, since `ci.yml` treats `.github/` as touching every part.
- **A new or changed workflow or script updates its row here.**
