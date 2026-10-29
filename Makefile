# Task runner for the harmflow-app monorepo. Run `make help` for the list.

SHELL := /bin/bash
VERSION := 0.1.0
# The commit a build comes from, "-dirty" with uncommitted changes, and empty outside a checkout. The daemon gets it
# here because go build stamps none in a git worktree; the interface reads it itself (desktop/ui/vite.config.ts).
COMMIT := $(shell git rev-parse --verify --quiet HEAD 2>/dev/null | cut -c1-7)
COMMIT := $(COMMIT)$(if $(COMMIT),$(if $(shell git status --porcelain),-dirty))
# The time make runs, in UTC RFC 3339, as the daemon's build time: go build stamps only vcs.time, the commit's time.
# The interface takes its own when Vite loads its config (desktop/ui/vite.config.ts).
BUILT_AT := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
TRIPLE := $(shell rustc -vV 2>/dev/null | sed -n 's/^host: //p')
DEV_HOME := $(CURDIR)/.harmflow-dev/home
SIDECAR := desktop/src-tauri/binaries/harmflowd-$(TRIPLE)
# The commit of the app bundle, which make update-app reads to skip a build of the commit it pulled. make app deletes it
# before building and writes it once the build succeeds, so it never vouches for a bundle a failed build left half made.
APP_STAMP := desktop/src-tauri/target.noindex/release/bundle/macos/HarmFlow.app.commit
LDFLAGS := -s -w -X main.Version=$(VERSION) -X main.Commit=$(COMMIT) -X main.BuiltAt=$(BUILT_AT)
COVERAGE := $(CURDIR)/build/coverage
# The time limit of a run of the tests module, which test-integration and coverage-daemon both pass to go test, so a
# local run and CI run under the same one. go test's default of 10 minutes is too short for the integration package on
# a heavily loaded machine, where a full run reached it less than halfway through; this one is sized from full runs
# measured under load, with a margin. Each test still fails within its own waits: the limit only bounds the package.
INTEGRATION_TIMEOUT := 30m
# The shell's coverage floors, in percent; COVERAGE.md holds the reading they come from.
RUST_FLOOR_LINES := 31.19
RUST_FLOOR_FUNCTIONS := 29.09
# cargo llvm-cov fails below a floor without saying which, so its summary is read again to name the counter that fell,
# its floor, and its ratio, truncated to two decimals as the floors are.
RUST_FLOOR_VIOLATIONS := def pct: "\(. / 100 | floor).\(. % 100 | tostring | if length < 2 then "0" + . else . end)"; \
	.data[0].totals | {lines, functions} | to_entries[] | .key as $$counter | .value as $$v \
	| ($$v.covered * 10000 / $$v.count | floor) as $$ratio | ({$$lines, $$functions}[$$counter] * 100 | round) as $$floor \
	| select($$ratio < $$floor) \
	| "coverage: desktop/src-tauri: \($$counter) \($$ratio | pct)% (\($$v.covered) of \($$v.count)) is below its floor of \($$floor | pct)%"
# HOLD_FLOORS= (empty) reads the coverage beside its floors without holding it: a figure below one fails nothing, a
# failing test still does. The nightly Coverage workflow reads main that way; CI holds the floors.
HOLD_FLOORS ?= 1
RUST_FLOOR_FLAGS := $(if $(HOLD_FLOORS),--fail-under-lines $(RUST_FLOOR_LINES) --fail-under-functions $(RUST_FLOOR_FUNCTIONS))
VITEST_FLOOR_FLAGS := $(if $(HOLD_FLOORS),,--coverage.thresholds.statements=0 --coverage.thresholds.branches=0 \
	--coverage.thresholds.functions=0 --coverage.thresholds.lines=0)
# More go test flags for test-daemon and test-integration, passed after their own so that a repeated flag wins: the
# nightly Stress workflow sets its -count and -timeout here. Empty, the two targets run as they always did.
TEST_FLAGS ?=

.PHONY: help
help: ## List the targets
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "  %-18s %s\n", $$1, $$2}'

.PHONY: generate
generate: ## Refresh the code generated from contracts/
	cp contracts/schemas/task.schema.json contracts/schemas/event.schema.json \
		contracts/schemas/execution-report.schema.json contracts/schemas/context.schema.json daemon/internal/contracts/
	pnpm -C desktop generate

.PHONY: icons
icons: ## Render the app and tray icons from desktop/src-tauri/icons/source (needs librsvg)
	pnpm -C desktop icons

.PHONY: check-generated
check-generated: generate ## Fail when a generated copy is stale
	git diff --exit-code -- daemon/internal/contracts desktop/ui/src/api/generated.ts

.PHONY: daemon
daemon: ## Build the daemon into build/harmflowd
	go -C daemon build -trimpath -ldflags "$(LDFLAGS)" -o ../build/harmflowd ./cmd/harmflowd

.PHONY: sidecar
sidecar: daemon ## Place the daemon where the desktop bundle expects it
	mkdir -p desktop/src-tauri/binaries
	cp build/harmflowd $(SIDECAR)

.PHONY: deps
deps: ## Install the desktop's JavaScript dependencies
	pnpm -C desktop install --frozen-lockfile

.PHONY: test
test: test-daemon test-desktop test-integration test-packaging ## Run every test

.PHONY: test-daemon
test-daemon: ## Unit tests of the daemon
	go -C daemon vet ./...
	go -C daemon test $(TEST_FLAGS) ./...

.PHONY: test-desktop
test-desktop: sidecar ## Type check and unit tests of the desktop
	pnpm -C desktop typecheck
	pnpm -C desktop test
	cd desktop/src-tauri && cargo test

.PHONY: check-imports
check-imports: ## Fail on Go imports outside the Go guide's three groups, as CI does
	go -C tests run ./imports "$(CURDIR)/daemon" "$(CURDIR)/tests"

.PHONY: check-file-names
check-file-names: ## Fail on tracked file and folder names outside CLAUDE.md's File names rules, as CI does
	go -C tests run ./filenames "$(CURDIR)"

.PHONY: test-integration
test-integration: daemon ## Tests across the daemon's process boundary, with fake harnesses
	go -C tests test -count=1 -timeout $(INTEGRATION_TIMEOUT) $(TEST_FLAGS) ./...

# Coverage is held by a ratchet, never by a target: each target fails when a part reads below its floor.
# COVERAGE.md holds the reading the floors come from.
.PHONY: coverage
coverage: coverage-daemon coverage-desktop ## Read the tests' coverage of the daemon, the interface, and the shell

# The unit tests run beside the integration package, which takes longest, so the target lasts about as long as that
# package alone; the suites are held to heavier load in the nightly Stress workflow. The integration output comes as it
# runs, and the unit tests' is held in a file and printed once both have ended, so neither is interleaved with the
# other. Either run failing fails the target, after both have ended.
.PHONY: coverage-daemon
coverage-daemon: ## Statements per package of the daemon, unit and integration data merged, held at their floors
	rm -rf "$(COVERAGE)/daemon" "$(COVERAGE)/daemon.out"
	mkdir -p "$(COVERAGE)/daemon/unit" "$(COVERAGE)/daemon/integration"
	go -C daemon test -count=1 -covermode=atomic -coverpkg=./... ./... -args -test.gocoverdir="$(COVERAGE)/daemon/unit" \
		> "$(COVERAGE)/daemon/unit.log" 2>&1 & unit=$$!; \
	GOCOVERDIR="$(COVERAGE)/daemon/integration" go -C tests test -count=1 -timeout $(INTEGRATION_TIMEOUT) ./...; \
	integration=$$?; wait $$unit; unit=$$?; cat "$(COVERAGE)/daemon/unit.log"; \
	test $$unit -eq 0 && test $$integration -eq 0
	go tool covdata textfmt -i="$(COVERAGE)/daemon/unit,$(COVERAGE)/daemon/integration" -o "$(COVERAGE)/daemon.out"
	go -C tests run ./coverage -floors "$(CURDIR)/daemon/coverage-floors.txt" -hold=$(if $(HOLD_FLOORS),true,false) \
		"$(COVERAGE)/daemon.out"

.PHONY: coverage-desktop
coverage-desktop: sidecar ## Unit tests of the interface and the shell, held at their floors (needs cargo-llvm-cov)
	pnpm -C desktop test:coverage $(VITEST_FLOOR_FLAGS)
	cd desktop/src-tauri && { cargo llvm-cov $(RUST_FLOOR_FLAGS) \
		|| { cargo llvm-cov report --summary-only --json | jq -r --argjson lines $(RUST_FLOOR_LINES) \
			--argjson functions $(RUST_FLOOR_FUNCTIONS) '$(RUST_FLOOR_VIOLATIONS)' >&2; exit 1; }; }

# Each audit fails on a known vulnerability. govulncheck reports only those the code calls, in its modules and in the
# Go that runs it. Dependabot cannot see this pin: refresh it by hand. `make -k audit` runs every audit when one fails.
GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@v1.9.0

.PHONY: audit
audit: audit-daemon audit-tests audit-desktop ## Report the known vulnerabilities in every part's dependencies

.PHONY: audit-daemon
audit-daemon: ## Known vulnerabilities the daemon calls
	go -C daemon run $(GOVULNCHECK) ./...

.PHONY: audit-tests
audit-tests: ## Known vulnerabilities the cross-part tests call
	go -C tests run $(GOVULNCHECK) ./...

.PHONY: audit-desktop
audit-desktop: audit-interface audit-shell ## Known vulnerabilities in the desktop's dependencies

.PHONY: audit-interface
audit-interface: ## Advisories against the interface's JavaScript dependencies, moderate and above
	pnpm -C desktop audit --audit-level moderate

.PHONY: audit-shell
audit-shell: ## Advisories against the shell's Rust dependencies (needs cargo-audit)
	cargo audit --file desktop/src-tauri/Cargo.lock

# The toolchain line of go.mod is what CI builds and audits with, and Dependabot does not raise it. The weekly Security
# workflow runs check-go-toolchain; a failure there is fixed with make go-toolchain and a pull request.
.PHONY: go-toolchain
go-toolchain: ## Raise the Go toolchain of daemon/ and tests/ to the newest patch of its minor
	.github/scripts/go-toolchain.sh update

.PHONY: check-go-toolchain
check-go-toolchain: ## Fail when the Go toolchain is behind its minor's newest patch, or Go no longer supports the minor
	.github/scripts/go-toolchain.sh check

# The release workflow runs it on a pushed version tag (packaging/README.md, Releasing). Without TAG it checks the tag
# of this Makefile's VERSION, so that the six version fields agree.
TAG ?= v$(VERSION)

.PHONY: check-version
check-version: ## Fail naming each version field that differs from TAG (v1.2.3), this VERSION's tag by default
	.github/scripts/check-version.sh "$(TAG)"

.PHONY: dev-daemon
dev-daemon: daemon ## Run the daemon on a development data directory
	HARMFLOW_HOME="$(DEV_HOME)" ./build/harmflowd --builtins "$(CURDIR)"

.PHONY: dev-ui
dev-ui: ## Serve the interface in a browser, proxied to the development daemon
	HARMFLOW_HOME="$(DEV_HOME)" pnpm -C desktop dev

.PHONY: dev-desktop
dev-desktop: sidecar ## Run the desktop shell against the development daemon
	HARMFLOW_HOME="$(DEV_HOME)" HARMFLOW_DAEMON_BIN="$(CURDIR)/build/harmflowd" HARMFLOW_BUILTINS_DIR="$(CURDIR)" \
		pnpm -C desktop tauri dev

.PHONY: app
app: sidecar ## Build HarmFlow.app and the DMG (unsigned until the Developer ID exists)
	@rm -f $(APP_STAMP)
	cd desktop && pnpm tauri build
	@printf '%s\n' '$(COMMIT)' >$(APP_STAMP)
	@echo "Bundles in desktop/src-tauri/target.noindex/release/bundle/"

# No prerequisite and no $(MAKE) here: the script pulls, then builds in a make of its own, whose COMMIT is read after
# the pull. A $(MAKE) in the recipe would also run it under make -n. OPEN=1, INSTALL_DIR, FORCE_BUILD=1, WAIT=1, and
# WAIT_TIMEOUT reach it through the environment, as make exports the variables of its command line.
.PHONY: update-app
update-app: ## Bring the local app to the latest main, install it to ~/Applications, and restart what was running
	@packaging/macos/update-app.sh

.PHONY: test-packaging
test-packaging: ## Tests of the packaging and release scripts
	packaging/macos/update-app-test.sh
	.github/scripts/check-version-test.sh

.PHONY: clean
clean: ## Remove build output
	rm -rf build desktop/ui/dist
