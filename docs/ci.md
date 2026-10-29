# CI and automation

> [!NOTE]
> This page describes the [`.github`](/.github) folder of harmflow-app. The repository's overview — what HarmFlow is,
> its architecture, the quick start, and the commands — is the [`README.md`](/README.md) at the root.

What GitHub reads from this folder: the workflows that check every change and every night, the scripts they run,
Dependabot's configuration, and the images the root README shows.

## Workflows

| Workflow | When it runs | What it does |
|---|---|---|
| [`ci.yml`](/.github/workflows/ci.yml) | Every pull request, every push to `main`, and by hand | Runs the jobs of the parts a pull request touches — daemon, integration, desktop, packaging, and the generated code — and every one of them on `main` or when `contracts/`, the `Makefile`, or `.github/` changes. The file-name check and the secret scan run on every change |
| [`coverage.yml`](/.github/workflows/coverage.yml) | Every night at 03:17 in São Paulo, by hand, and on a pull request that changes it | Reads the coverage of the daemon, the interface, and the shell on `main`, and shows each reading beside its floor in the run's summary. It never fails on a figure: the floors are held by CI |
| [`security.yml`](/.github/workflows/security.yml) | Mondays at 00:17 in São Paulo, by hand, and on a pull request that changes the scan | Scans the whole history for secrets, every branch and tag included, audits every part's dependencies, and checks that the Go toolchain is the newest patch of its minor |
| [`stress.yml`](/.github/workflows/stress.yml) | Every night at 03:47 in São Paulo, and by hand | Runs the daemon's unit tests and the integration suite three times each beside a fixed CPU load, and shows in the run's summary how each suite did. It is no check: it never runs on a pull request or a push |

The jobs that test the app run on macOS, the MVP's only platform. The jobs that only read the repository — the parts
touched, the file names, the secret scans, the generated code, and the Go toolchain — run on Linux.

## Scripts

| Script | Run by | What it does |
|---|---|---|
| [`coverage-summary.sh`](/.github/scripts/coverage-summary.sh) | `coverage.yml` | Prints one part's coverage reading as Markdown, beside the floors that hold it, and writes the part's HTML report into `build/coverage` |
| [`go-toolchain.sh`](/.github/scripts/go-toolchain.sh) | `make check-go-toolchain` in `security.yml`, and `make go-toolchain` | `check` fails when a newer patch of the Go minor exists or Go no longer supports the minor; `update` raises the toolchain line of `daemon/go.mod` and `tests/go.mod` to that patch |
| [`install-gitleaks.sh`](/.github/scripts/install-gitleaks.sh) | `ci.yml` and `security.yml` | Installs a pinned gitleaks release on a Linux runner, checked against its SHA-256 before it runs |
| [`stress.sh`](/.github/scripts/stress.sh) | `stress.yml` | Starts and stops the CPU load, runs each suite beside it, and prints the run's summary from what it wrote to `build/stress` |

## Dependabot

[`dependabot.yml`](/.github/dependabot.yml) proposes monthly updates to the pinned actions and to the Go, pnpm, and
Cargo dependencies. In each ecosystem, the minor and patch updates come in one pull request and each major update comes
alone, reviewed on its own. The `tauri*` crates and the `@tauri-apps/*` packages come in two pull requests that are
merged together: Tauri's build checks that the two share a major and minor, so a half-merged pair fails `make app`.

## Assets

[`assets/`](/.github/assets) holds the banner and the architecture diagram the root README shows.

## Rules

- **Pin every action to a full commit SHA**, with the release it corresponds to in a comment, so a moved tag cannot
  change what runs. Dependabot proposes the updates, except for two pins it cannot see, which are refreshed by hand:
  `dtolnay/rust-toolchain`, a commit of its `stable` branch, and the gitleaks version and checksum in
  `install-gitleaks.sh`.
- **Every workflow reads only:** `permissions: contents: read`.
- **A change here runs every CI job**, since `ci.yml` treats `.github/` as touching every part.
- **A new or changed workflow or script updates its row here.**
