#!/usr/bin/env bash
# Tests of update-app.sh's launch-agent check and install, with a fake launchctl and a fake agent plist in a home
# directory of its own, so nothing reaches the user's launchd session or ~/Applications (harmflow-app#188).
# `make test-packaging` runs it.

set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
# shellcheck source=update-app.sh
source "$here/update-app.sh"

base_path=$PATH
failures=0
fail() {
	printf 'FAIL %s: %s\n' "$case_name" "$*" >&2
	failures=$((failures + 1))
}

# setup gives each case a home of its own, a fake launchctl first on PATH, and the globals update-app.sh's main sets.
setup() {
	case_name=$1
	work=$(mktemp -d "$scratch/case.XXXXXX")
	HOME=$work/home
	mkdir -p "$HOME/Library/LaunchAgents" "$work/bin"
	cat >"$work/bin/launchctl" <<'EOF'
#!/bin/sh
# Loaded when $FAKE_LOADED_PROGRAM is set: print shows the program launchd loaded.
[ "$1" = print ] && [ -n "${FAKE_LOADED_PROGRAM:-}" ] || exit 113
printf 'gui/501/com.harmflow.daemon = {\n\tprogram = %s\n}\n' "$FAKE_LOADED_PROGRAM"
EOF
	chmod +x "$work/bin/launchctl"
	PATH=$work/bin:$base_path
	unset FAKE_LOADED_PROGRAM
	built=$work/checkout/desktop/src-tauri/target/release/bundle/macos/HarmFlow.app
	installed=$work/Applications/HarmFlow.app
	daemon_exe=$installed/Contents/MacOS/harmflowd
	default_home=$HOME/Library/Application\ Support/HarmFlow
	home=$default_home
	commit=abc1234
	staging=''
}

# agent_plist EXE [ARG...] writes the agent plist as the daemon's launchd adapter does, its ProgramArguments only.
agent_plist() {
	local plist=$HOME/Library/LaunchAgents/$LABEL.plist arg i=0
	/usr/libexec/PlistBuddy -c "Add :Label string $LABEL" -c 'Add :ProgramArguments array' "$plist" >/dev/null
	for arg in "$@"; do
		/usr/libexec/PlistBuddy -c "Add :ProgramArguments:$i string $arg" "$plist"
		i=$((i + 1))
	done
}

# check runs use_launch_agent in a subshell, since a refusal exits; it sets rc and out.
check() {
	rc=0
	out=$(use_launch_agent 2>&1) || rc=$?
}

scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT

setup "an agent that is not loaded starts the daemon directly"
check
[[ $rc == 1 && $out == *"not loaded"* ]] || fail "rc $rc: $out"

setup "an agent whose plist runs a build in target/ refuses before stopping anything"
agent_plist "$built/Contents/MacOS/harmflowd"
export FAKE_LOADED_PROGRAM=$built/Contents/MacOS/harmflowd
check
[[ $rc == 1 ]] || fail "rc $rc, want the refusal's 1"
[[ $out == *"runs $built/Contents/MacOS/harmflowd, not the installed $daemon_exe"* ]] || fail "names no binaries: $out"
[[ $out == *"Nothing was stopped"* ]] || fail "does not say nothing stopped: $out"
[[ $out == *"turn Autostart off"*"turn Autostart on"* ]] || fail "does not say how to move it: $out"
[[ $(/usr/libexec/PlistBuddy -c 'Print :ProgramArguments:0' "$HOME/Library/LaunchAgents/$LABEL.plist") == \
	"$built/Contents/MacOS/harmflowd" ]] || fail "the plist changed"

setup "an agent of another data directory starts the daemon directly"
agent_plist "$built/Contents/MacOS/harmflowd" --home "$work/other"
export FAKE_LOADED_PROGRAM=$built/Contents/MacOS/harmflowd
check
[[ $rc == 1 && $out == *"serves $work/other"* ]] || fail "rc $rc: $out"

setup "an agent loaded before its plist moved to the installed copy starts the daemon directly"
agent_plist "$daemon_exe"
export FAKE_LOADED_PROGRAM=$built/Contents/MacOS/harmflowd
check
[[ $rc == 1 && $out == *"keeps it until the next login"* ]] || fail "rc $rc: $out"

setup "an agent that runs the installed copy restarts the daemon through launchd"
agent_plist "$daemon_exe"
export FAKE_LOADED_PROGRAM=$daemon_exe
check
[[ $rc == 0 && $out == *"launchctl kickstart"* ]] || fail "rc $rc: $out"

setup "the install replaces the installed copy and leaves nothing beside it"
mkdir -p "$built/Contents/MacOS" "$installed/Contents/MacOS"
echo new >"$built/Contents/MacOS/harmflowd"
echo old >"$installed/Contents/MacOS/harmflowd"
echo stale >"$installed/Contents/MacOS/removed-since"
(stage_app && install_app) >/dev/null || fail "the install failed"
[[ $(cat "$daemon_exe") == new ]] || fail "the installed copy is not the build"
[[ ! -e $installed/Contents/MacOS/removed-since ]] || fail "a file of the old copy is left"
[[ $(ls -A "$work/Applications") == HarmFlow.app ]] || fail "left beside it: $(ls -A "$work/Applications")"

setup "the first install creates the install directory"
mkdir -p "$built/Contents/MacOS"
echo new >"$built/Contents/MacOS/harmflowd"
(stage_app && install_app) >/dev/null || fail "the install failed"
[[ $(cat "$daemon_exe") == new ]] || fail "the installed copy is not the build"

if ((failures > 0)); then
	printf '%d failed\n' "$failures" >&2
	exit 1
fi
echo "update-app: all tests passed"
