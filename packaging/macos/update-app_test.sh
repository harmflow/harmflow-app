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
	root=$work/checkout
	built=$root/desktop/src-tauri/target/release/bundle/macos/HarmFlow.app
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

# fake_lsregister makes $lsregister a script that prints $work/dump for -dump and logs every other call to $work/calls.
fake_lsregister() {
	lsregister=$work/bin/lsregister
	cat >"$lsregister" <<EOF
#!/bin/sh
[ "\$1" = -dump ] && exec cat "$work/dump"
printf '%s\n' "\$*" >>"$work/calls"
EOF
	chmod +x "$lsregister"
	: >"$work/calls"
}

# dump_record PATH ID prints a bundle record as lsregister -dump does, trimmed to the lines registered_paths reads.
dump_record() {
	printf -- '--------------------------------------------------------------------------------\n'
	printf 'bundle id:                  HarmFlow (0xc6c4)\n'
	printf 'path:                       %s (0xf4a4)\n' "$1"
	printf 'name:                       HarmFlow\n'
	printf 'identifier:                 %s\n' "$2"
	printf 'executable:                 Contents/MacOS/harmflow-desktop\n'
}

setup "register_app registers the installed copy and unregisters the builds that compete with it"
fake_lsregister
mkdir -p "$root/desktop/src-tauri" "$installed" "$root/desktop/src-tauri/target/debug/bundle/macos/HarmFlow.app" \
	"$work/other worktree/target/release/bundle/macos/HarmFlow.app"
printf '{"productName": "HarmFlow", "identifier": "com.harmflow.desktop"}\n' >"$root/desktop/src-tauri/tauri.conf.json"
debug=$root/desktop/src-tauri/target/debug/bundle/macos/HarmFlow.app
other="$work/other worktree/target/release/bundle/macos/HarmFlow.app"
{
	dump_record "$installed" com.harmflow.desktop
	dump_record "$built" com.harmflow.desktop
	dump_record "$debug" com.harmflow.desktop
	dump_record /Volumes/dmg.gone0/HarmFlow.app com.harmflow.desktop
	dump_record "$other" com.harmflow.desktop
	dump_record /Volumes/dmg.gone1/Other.app com.example.other
	printf -- '--------------------------------------------------------------------------------\n'
	printf 'claim id:                   HarmFlow task (0x1)\n'
	printf 'identifier:                 com.harmflow.desktop\n'
} >"$work/dump"
out=$(register_app 2>&1) || fail "register_app failed: $out"
want="-f $installed
-u $built
-u $debug
-u /Volumes/dmg.gone0/HarmFlow.app"
[[ $(cat "$work/calls") == "$want" ]] || fail "lsregister calls:"$'\n'"$(cat "$work/calls")"
[[ $out == *"unregistered $debug (a build in this checkout)"* ]] || fail "does not name the checkout's build: $out"
[[ $out == *"unregistered /Volumes/dmg.gone0/HarmFlow.app (no longer exists)"* ]] || fail "does not name the gone one: $out"
[[ -d $debug && -d $other && -d $installed ]] || fail "a bundle was deleted"

if ((failures > 0)); then
	printf '%d failed\n' "$failures" >&2
	exit 1
fi
echo "update-app: all tests passed"
