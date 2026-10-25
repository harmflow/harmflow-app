#!/usr/bin/env bash
# Tests of update-app.sh's launch-agent check and install, with a fake launchctl and a fake agent plist in a home
# directory of its own, so nothing reaches the user's launchd session or ~/Applications (harmflow-app#188), and of its
# build skip and wait for executions, with a fake make and a fake local API (harmflow-app#189).
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
	cargo_out=$root/desktop/src-tauri/target.noindex
	built=$cargo_out/release/bundle/macos/HarmFlow.app
	installed=$work/Applications/HarmFlow.app
	daemon_exe=$installed/Contents/MacOS/harmflowd
	default_home=$HOME/Library/Application\ Support/HarmFlow
	home=$default_home
	commit=abc1234
	tmp=$work/tmp
	mkdir -p "$tmp"
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
debug=$cargo_out/debug/bundle/macos/HarmFlow.app
old=$root/desktop/src-tauri/target/release/bundle/macos/HarmFlow.app
beside=$root/desktop/src-tauri/target-other/release/bundle/macos/HarmFlow.app
other="$work/other worktree/target/release/bundle/macos/HarmFlow.app"
mkdir -p "$installed" "$debug" "$old" "$beside" "$other"
printf '{"productName": "HarmFlow", "identifier": "com.harmflow.desktop"}\n' >"$root/desktop/src-tauri/tauri.conf.json"
{
	dump_record "$installed" com.harmflow.desktop
	dump_record "$built" com.harmflow.desktop
	dump_record "$debug" com.harmflow.desktop
	dump_record "$old" com.harmflow.desktop
	dump_record "$beside" com.harmflow.desktop
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
-u $old
-u /Volumes/dmg.gone0/HarmFlow.app"
[[ $(cat "$work/calls") == "$want" ]] || fail "lsregister calls:"$'\n'"$(cat "$work/calls")"
[[ $out == *"unregistered $debug (a build in this checkout)"* ]] || fail "does not name the checkout's build: $out"
[[ $out == *"unregistered /Volumes/dmg.gone0/HarmFlow.app (no longer exists)"* ]] || fail "does not name the gone one: $out"
[[ $out == *"$root/desktop/src-tauri/target is Cargo's output from before target.noindex"*"rm -rf"* ]] ||
	fail "does not name the old target/ to delete: $out"
[[ -d $debug && -d $old && -d $beside && -d $other && -d $installed ]] || fail "a bundle was deleted"

setup "register_app names no old target/ when there is none"
fake_lsregister
mkdir -p "$root/desktop/src-tauri" "$installed"
printf '{"identifier": "com.harmflow.desktop"}\n' >"$root/desktop/src-tauri/tauri.conf.json"
dump_record "$installed" com.harmflow.desktop >"$work/dump"
out=$(register_app 2>&1) || fail "register_app failed: $out"
[[ $out != *"before target.noindex"* ]] || fail "names an old target/: $out"

# fake_checkout makes $root a git repository whose update-app.sh is a stub that prints how it was started, and commits
# it; stub_commit SAYS changes the stub and commits again. Each prints the new commit, 7 characters as main reads it.
fake_checkout() {
	mkdir -p "$root/packaging/macos"
	git -C "$root" init -q
	git -C "$root" config user.email test@example.com
	git -C "$root" config user.name test
	stub_commit first
}
stub_commit() {
	printf '#!/bin/sh\necho "%s rerun from $UPDATE_APP_RERUN_FROM with $*"\n' "$1" >"$root/packaging/macos/update-app.sh"
	chmod +x "$root/packaging/macos/update-app.sh"
	git -C "$root" add -A
	git -C "$root" commit -q -m "$1"
	git -C "$root" rev-parse HEAD | cut -c1-7
}

setup "a pull that changes update-app.sh runs the new one once, with the same arguments"
before=$(fake_checkout)
stub_commit second >/dev/null
unset UPDATE_APP_RERUN_FROM
rc=0
out=$(rerun_if_changed "$before" one "two words") || rc=$?
[[ $rc == 0 ]] || fail "rc $rc: $out"
[[ $out == *"the pull changed packaging/macos/update-app.sh; running the new one"* ]] || fail "does not say so: $out"
[[ $out == *"second rerun from $before with one two words" ]] || fail "did not run the new script: $out"
[[ ! -e $tmp ]] || fail "left its temporary directory"

setup "a pull that leaves update-app.sh as it was goes on with this script"
before=$(fake_checkout)
echo other >"$root/other"
git -C "$root" add -A
git -C "$root" commit -q -m other
out=$(rerun_if_changed "$before"; echo "went on") || fail "rerun_if_changed failed: $out"
[[ $out == "went on" ]] || fail "did something: $out"

setup "the rerun never runs itself again"
before=$(fake_checkout)
stub_commit second >/dev/null
export UPDATE_APP_RERUN_FROM=$before
out=$(rerun_if_changed "$before"; echo "went on") || fail "rerun_if_changed failed: $out"
[[ $out == "went on" ]] || fail "ran again: $out"
unset UPDATE_APP_RERUN_FROM

# fake_make makes $MAKE a script that logs its arguments to $work/make; fake_build STAMP leaves a build in $built and,
# unless STAMP is empty, the commit make app writes beside it.
fake_make() {
	MAKE=$work/bin/make
	printf '#!/bin/sh\necho "$*" >>"%s"\n' "$work/make" >"$MAKE"
	chmod +x "$MAKE"
}
fake_build() {
	mkdir -p "$built/Contents/MacOS"
	printf '#!/bin/sh\n' >"$built/Contents/MacOS/harmflowd"
	chmod +x "$built/Contents/MacOS/harmflowd"
	[[ -z $1 ]] || printf '%s\n' "$1" >"$built.commit"
}

setup "a build of the pulled commit is not built again"
fake_make
fake_build "$commit"
unset FORCE_BUILD
out=$(build_app 2>&1) || fail "build_app failed: $out"
[[ ! -e $work/make ]] || fail "ran make: $(cat "$work/make")"
[[ $out == *"already a build of $commit; skipping make app"* ]] || fail "does not say it skips: $out"

setup "FORCE_BUILD=1 builds a build of the pulled commit again"
fake_make
fake_build "$commit"
out=$(FORCE_BUILD=1 build_app 2>&1) || fail "build_app failed: $out"
[[ $(cat "$work/make" 2>&1) == "-C $root app" ]] || fail "did not run make app: $out"

for stamp in 1234abc "$commit-dirty" ''; do
	setup "a build stamped '$stamp' is built again"
	fake_make
	fake_build "$stamp"
	out=$(build_app 2>&1) || fail "build_app failed: $out"
	[[ $(cat "$work/make" 2>&1) == "-C $root app" ]] || fail "did not run make app: $out"
done

setup "a stamp without a bundle is built again"
fake_make
mkdir -p "${built%/*}"
printf '%s\n' "$commit" >"$built.commit"
out=$(build_app 2>&1) || fail "build_app failed: $out"
[[ $(cat "$work/make" 2>&1) == "-C $root app" ]] || fail "did not run make app: $out"

# From here on, api answers from $work/responses, one "STATUS BODY" line per call, and logs each call to $work/calls;
# sleep only moves SECONDS on, so a wait takes no time.
fake_api() {
	printf '%s\n' "$@" >"$work/responses"
	: >"$work/calls"
}
api() {
	printf '%s\n' "$*" >>"$work/calls"
	local next
	next=$(head -n 1 "$work/responses")
	tail -n +2 "$work/responses" >"$work/responses.next"
	mv "$work/responses.next" "$work/responses"
	printf '%s' "${next#* }" >"$tmp/body"
	printf '%s' "${next%% *}"
}
sleep() { SECONDS=$((SECONDS + ${1%.*})); }
busy='409 {"error": {"code": "executions_running", "text": "1 executions are running"}}'
stats() { printf '200 {"running": %s, "queued": 0}' "$1"; }

# stop runs stop_daemon in a subshell, since a refusal exits; it sets rc and out.
stop() {
	rc=0
	out=$(stop_daemon 2>&1) || rc=$?
}

setup "a daemon with no execution stops at once"
wait_timeout=3600
unset WAIT
fake_api '202 {"ok": true}'
stop
[[ $rc == 0 ]] || fail "rc $rc: $out"
[[ $(cat "$work/calls") == "POST /daemon/stop {}" ]] || fail "calls:"$'\n'"$(cat "$work/calls")"

setup "without WAIT=1 running executions refuse the update, built but not running"
wait_timeout=3600
unset WAIT
fake_api "$busy"
stop
[[ $rc == 1 ]] || fail "rc $rc, want the refusal's 1"
[[ $out == *"nothing was stopped and $installed was left as it was"* ]] || fail "does not say nothing stopped: $out"
[[ $out == *"the new build is at $built, not running"* ]] || fail "does not say the build is not running: $out"
[[ $out == *"make update-app WAIT=1"* ]] || fail "gives no WAIT=1 hint: $out"
[[ $(cat "$work/calls") == "POST /daemon/stop {}" ]] || fail "calls:"$'\n'"$(cat "$work/calls")"

setup "WAIT=1 says the count while executions run and stops once they end"
wait_timeout=3600
WAIT=1
fake_api "$busy" "$(stats 2)" "$(stats 1)" "$(stats 0)" '202 {"ok": true}'
stop
[[ $rc == 0 ]] || fail "rc $rc: $out"
[[ $out == *"executions running: 2; waiting"*"executions running: 1; waiting"* ]] || fail "does not say the count: $out"
want="POST /daemon/stop {}
GET /stats
GET /stats
GET /stats
POST /daemon/stop {}"
[[ $(cat "$work/calls") == "$want" ]] || fail "calls:"$'\n'"$(cat "$work/calls")"

setup "WAIT=1 waits for an execution that starts between the count and the stop, never forcing"
wait_timeout=3600
WAIT=1
fake_api "$busy" "$(stats 0)" "$busy" "$(stats 1)" "$(stats 0)" '202 {"ok": true}'
stop
[[ $rc == 0 ]] || fail "rc $rc: $out"
[[ $(grep -c '^POST /daemon/stop {}$' "$work/calls") == 3 ]] || fail "calls:"$'\n'"$(cat "$work/calls")"
[[ $(grep -c force "$work/calls") == 0 ]] || fail "asked for force:"$'\n'"$(cat "$work/calls")"

setup "WAIT=1 gives up after WAIT_TIMEOUT with the refusal"
wait_timeout=12
WAIT=1
fake_api "$busy" "$(stats 1)" "$(stats 1)" "$(stats 1)" "$(stats 1)" "$(stats 1)"
stop
[[ $rc == 1 ]] || fail "rc $rc, want 1"
[[ $out == *"still running after WAIT_TIMEOUT=12s"* ]] || fail "does not say it timed out: $out"
[[ $out == *"nothing was stopped and $installed was left as it was; the new build is at $built, not running"* ]] ||
	fail "does not give the refusal: $out"
# Polls at 0, 5, and 10 seconds; at 15 the deadline of 12 has passed.
[[ $(grep -c '^GET /stats$' "$work/calls") == 3 ]] || fail "calls:"$'\n'"$(cat "$work/calls")"

setup "WAIT=1 does not wait out a refusal for another reason"
wait_timeout=3600
WAIT=1
fake_api '409 {"error": {"code": "conflict", "text": "something else"}}'
stop
[[ $rc == 1 && $out == *"the daemon refused to stop"*"something else"* ]] || fail "rc $rc: $out"
[[ $(cat "$work/calls") == "POST /daemon/stop {}" ]] || fail "calls:"$'\n'"$(cat "$work/calls")"

setup "WAIT=1 stops waiting when the daemon no longer answers"
wait_timeout=3600
WAIT=1
fake_api "$busy" '000 '
stop
[[ $rc == 1 && $out == *"did not answer GET /stats while waiting; nothing was stopped"* ]] || fail "rc $rc: $out"

# warned runs warn_running with a daemon found, in a subshell; it sets out.
warned() {
	out=$(
		find_daemon() { :; }
		warn_running 2>&1
	) || fail "warn_running failed: $out"
}

setup "running executions are named before the build, with the WAIT=1 hint"
unset WAIT
fake_api "$(stats 2)"
warned
[[ $out == *"executions running: 2; the restart will be refused unless they end"*"WAIT=1"* ]] || fail "$out"

setup "with WAIT=1 running executions are named before the build as waited for"
WAIT=1
fake_api "$(stats 2)"
warned
[[ $out == *"executions running: 2; WAIT=1 waits for them to end after the build"* ]] || fail "$out"

setup "no running execution says nothing before the build"
unset WAIT
fake_api "$(stats 0)"
warned
[[ -z $out ]] || fail "said: $out"

if ((failures > 0)); then
	printf '%d failed\n' "$failures" >&2
	exit 1
fi
echo "update-app: all tests passed"
