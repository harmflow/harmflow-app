#!/usr/bin/env bash
# Brings the local app to the latest main and restarts what was running (harmflow-app#102). `make update-app` runs it.
#
# It works on the data directory of $HARMFLOW_HOME, or ~/Library/Application Support/HarmFlow, and on the window of
# this checkout's bundle. It finds the daemon only through that directory's run/daemon.json, never by process name, and
# stops it through the local API without force, so it never interrupts an execution nor touches another directory's
# daemon.

set -euo pipefail

readonly LABEL=com.harmflow.daemon
readonly KEYCHAIN_SERVICE=com.harmflow.local-api
readonly KEYCHAIN_ACCOUNT=token

say() { printf 'update-app: %s\n' "$*"; }
die() {
	printf 'update-app: %s\n' "$*" >&2
	exit 1
}

# json_get FILE KEY prints a top-level value of a JSON file, or nothing when it is absent.
json_get() { /usr/bin/plutil -extract "$2" raw -o - "$1" 2>/dev/null || true; }

alive() { kill -0 "$1" 2>/dev/null; }

# wait_gone PID SECONDS waits for a process to exit.
wait_gone() {
	local i
	for ((i = 0; i < $2 * 5; i++)); do
		alive "$1" || return 0
		sleep 0.2
	done
	return 1
}

# canon PATH is the physical path of a directory, or PATH itself without a trailing slash when it does not exist.
canon() { (cd "$1" 2>/dev/null && pwd -P) || printf '%s' "${1%/}"; }

# read_token STORE prints the local API token the way the desktop's read_token does: run/token when the store is
# "file", else the Keychain item, in $HARMFLOW_KEYCHAIN when that names a keychain file.
read_token() {
	if [[ $1 == file ]]; then
		tr -d '[:space:]' <"$home/run/token"
		return
	fi
	local args=(find-generic-password -s "$KEYCHAIN_SERVICE" -a "$KEYCHAIN_ACCOUNT" -w)
	[[ -n ${HARMFLOW_KEYCHAIN:-} ]] && args+=("$HARMFLOW_KEYCHAIN")
	/usr/bin/security "${args[@]}" </dev/null
}

# api METHOD PATH [BODY] calls the local API at $addr with $token and prints the status code; the body lands in
# $tmp/body. The token goes through stdin, so it never shows in the process list.
api() {
	local data=()
	[[ $# -gt 2 ]] && data=(-H 'Content-Type: application/json' --data "$3")
	printf 'Authorization: Bearer %s\n' "$token" |
		curl -sS --noproxy '*' --max-time 5 -o "$tmp/body" -w '%{http_code}' -H @- -X "$1" ${data[@]+"${data[@]}"} \
			"http://$addr/api/v1$2" 2>/dev/null || true
}

# find_daemon sets pid, addr, and token when run/daemon.json names a daemon that is alive and answers; a file left by
# a daemon that is gone counts as none.
find_daemon() {
	pid='' addr='' token=''
	[[ -f $runtime ]] || return 1
	local p store
	p=$(json_get "$runtime" pid)
	[[ $p =~ ^[0-9]+$ ]] && alive "$p" || return 1
	[[ $(basename "$(ps -o comm= -p "$p")") == harmflowd ]] || return 1
	addr=$(json_get "$runtime" address)
	store=$(json_get "$runtime" tokenStore)
	token=$(read_token "$store") || die "cannot read the local API token (store: ${store:-keychain}); nothing was changed"
	[[ $(api GET /health) == 200 ]] ||
		die "daemon pid $p in $runtime does not answer at $addr; nothing was changed. See $home/logs/daemon.log"
	pid=$p
}

# window_pids prints the pids of the harmflow-desktop processes whose executable is this checkout's bundle.
window_pids() { ps -axww -o pid=,comm= | awk -v exe="$window_exe" '{ p = $1; sub(/^ *[0-9]+ +/, ""); if ($0 == exe) print p }'; }

# use_launch_agent succeeds when the loaded launch agent serves this data directory, so launchd keeps supervising the
# daemon it restarts. The agent is one per user, at ~/Library/LaunchAgents, whatever the data directory: its
# ProgramArguments say which directory it serves, by --home, or the default one without it.
use_launch_agent() {
	local plist="$HOME/Library/LaunchAgents/$LABEL.plist" pb=/usr/libexec/PlistBuddy
	if ! launchctl print "gui/$(id -u)/$LABEL" >/dev/null 2>&1; then
		say "launch agent $LABEL: not loaded; harmflowd starts directly"
		return 1
	fi
	if [[ ! -f $plist ]]; then
		say "launch agent $LABEL: loaded, but $plist is gone; harmflowd starts directly"
		return 1
	fi
	local exe agent_home="$default_home" i arg
	exe=$("$pb" -c 'Print :ProgramArguments:0' "$plist")
	for ((i = 1; ; i++)); do
		arg=$("$pb" -c "Print :ProgramArguments:$i" "$plist" 2>/dev/null) || break
		[[ $arg == --home ]] && agent_home=$("$pb" -c "Print :ProgramArguments:$((i + 1))" "$plist")
	done
	if [[ $(canon "$agent_home") != $(canon "$home") ]]; then
		say "launch agent $LABEL: serves $agent_home, not $home; harmflowd starts directly"
		return 1
	fi
	[[ $exe == "$daemon_exe" ]] ||
		die "launch agent $LABEL runs $exe, not this checkout's $daemon_exe; a kickstart would start that binary. Nothing was stopped"
	say "launch agent $LABEL: serves $home; harmflowd starts through launchctl kickstart"
}

# start_daemon starts harmflowd as the desktop's spawn_daemon does: the bundle's binary, in a session of its own, with
# its standard streams on /dev/null and --home only when $HARMFLOW_HOME is set. Or through launchd, when it supervises.
start_daemon() {
	if [[ $launchd == yes ]]; then
		launchctl kickstart "gui/$(id -u)/$LABEL"
		return
	fi
	local args=()
	[[ -n ${HARMFLOW_HOME:-} ]] && args=(--home "$HARMFLOW_HOME")
	(cd / && exec /usr/bin/perl -MPOSIX -e 'POSIX::setsid() or die "setsid: $!\n"; exec @ARGV or die "exec: $!\n"' \
		"$daemon_exe" ${args[@]+"${args[@]}"} </dev/null >/dev/null 2>&1 &)
}

# verify_daemon waits up to 10 seconds for a new pid in run/daemon.json, then checks that it answers with $commit.
verify_daemon() {
	local old=$1 i p health_commit=''
	for ((i = 0; i < 50; i++)); do
		p=$(json_get "$runtime" pid)
		if [[ $p =~ ^[0-9]+$ && $p != "$old" ]] && alive "$p"; then
			addr=$(json_get "$runtime" address)
			if token=$(read_token "$(json_get "$runtime" tokenStore)") && [[ $(api GET /health) == 200 ]]; then
				health_commit=$(json_get "$tmp/body" commit)
				break
			fi
		fi
		sleep 0.2
	done
	if [[ -z $health_commit ]]; then
		say "the new daemon did not answer within 10 seconds. See $home/logs/daemon.log" >&2
		return 1
	fi
	if [[ $health_commit != "$commit" ]]; then
		say "the daemon answers with commit $health_commit, not $commit. See $home/logs/daemon.log" >&2
		return 1
	fi
	say "daemon running commit $commit, pid $p, at $addr"
}

open_window() {
	local env=() name i
	for name in HARMFLOW_HOME HARMFLOW_TOKEN_STORE HARMFLOW_KEYCHAIN; do
		[[ -n ${!name:-} ]] && env+=(--env "$name=${!name}")
	done
	# -n: another copy of the app, with the same bundle id, may be running; a plain open would bring that one forward.
	open -n ${env[@]+"${env[@]}"} "$bundle"
	for ((i = 0; i < 50; i++)); do
		if [[ -n $(window_pids) ]]; then
			say "window reopened, pid $(window_pids | tr '\n' ' ')"
			return
		fi
		sleep 0.2
	done
	say "the window did not start within 10 seconds: open $bundle" >&2
	return 1
}

main() {
	root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)
	bundle="$root/desktop/src-tauri/target/release/bundle/macos/HarmFlow.app"
	window_exe="$bundle/Contents/MacOS/harmflow-desktop"
	daemon_exe="$bundle/Contents/MacOS/harmflowd"
	default_home="$HOME/Library/Application Support/HarmFlow"
	home="${HARMFLOW_HOME:-$default_home}"
	runtime="$home/run/daemon.json"
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT

	# 1. Update: refuse before anything changes.
	local branch
	branch=$(git -C "$root" symbolic-ref --short -q HEAD) || die "HEAD is detached, not on main; nothing was changed"
	[[ $branch == main ]] || die "the checkout is on $branch, not main; nothing was changed"
	[[ -z $(git -C "$root" status --porcelain) ]] ||
		die "the working tree has uncommitted changes; commit or stash them first. Nothing was changed"
	local out before
	before=$(git -C "$root" rev-parse HEAD | cut -c1-7)
	out=$(git -C "$root" pull --ff-only origin main 2>&1) ||
		die "git pull --ff-only origin main cannot fast-forward; nothing was changed:"$'\n'"$out"
	# The Makefile's COMMIT for the new HEAD: a fixed 7 characters, where --short=7 may give more.
	commit=$(git -C "$root" rev-parse HEAD | cut -c1-7)
	if [[ $commit == "$before" ]]; then say "main is up to date at $commit"; else say "main fast-forwarded from $before to $commit"; fi

	# 2. Build, in a make of its own, so its COMMIT is read after the pull.
	say "building $commit with make app"
	"${MAKE:-make}" -C "$root" app || die "make app failed; the window and the daemon were left as they were"

	# 3. What runs now.
	local daemon_was='' window_was old_pid=''
	launchd=no
	if find_daemon; then
		daemon_was=yes old_pid=$pid
		say "daemon pid $pid at $addr, data directory $home"
		if use_launch_agent; then launchd=yes; fi
	else
		say "no daemon runs for $home"
	fi
	window_was=$(window_pids | tr '\n' ' ')
	if [[ -n $window_was ]]; then say "window pid $window_was"; else say "no window of $bundle runs"; fi

	# 4. Stop the daemon, never with force, before the window, so a refusal leaves both running.
	if [[ -n $daemon_was ]]; then
		case $(api POST /daemon/stop '{}') in
		202) ;;
		409)
			die "executions are running, so nothing was stopped. The new bundle is built at $bundle but not running;" \
				"run make update-app again when they end"
			;;
		*) die "the daemon refused to stop: $(cat "$tmp/body"). Nothing was stopped" ;;
		esac
		wait_gone "$old_pid" 30 || die "daemon pid $old_pid did not exit within 30 seconds. See $home/logs/daemon.log"
		say "daemon pid $old_pid stopped"
	fi

	# 5. Quit the window. SIGTERM ends it also with the tray on, where a plain quit is prevented.
	local w
	for w in $window_was; do
		kill -TERM "$w" 2>/dev/null || true
		wait_gone "$w" 15 || die "window pid $w did not exit within 15 seconds"
		say "window pid $w quit"
	done

	# 6. Bring back what ran, the daemon first, so the window connects to it at once.
	local status=0
	if [[ -n $daemon_was ]]; then
		start_daemon
		verify_daemon "$old_pid" || status=1
	fi
	if [[ -n $window_was ]]; then open_window || status=1; fi
	if [[ -z $daemon_was && -z $window_was ]]; then
		say "nothing was running, so nothing was started; the new bundle is at $bundle"
	fi
	return $status
}

main "$@"
