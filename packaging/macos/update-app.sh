#!/usr/bin/env bash
# Brings the local app to the latest main and restarts what was running (harmflow-app#102). `make update-app` runs it.
#
# It works on the data directory of $HARMFLOW_HOME, or ~/Library/Application Support/HarmFlow, and on the window of
# the installed copy, $INSTALL_DIR/HarmFlow.app or ~/Applications/HarmFlow.app (harmflow-app#188). It finds the daemon
# only through that directory's run/daemon.json, never by process name, and stops it through the local API without
# force, so it never interrupts an execution nor touches another directory's daemon. The daemon and the window it
# restarts run from the installed copy, which it replaces only once both have stopped; OPEN=1 starts them also when
# they did not run.

set -euo pipefail

readonly LABEL=com.harmflow.daemon
readonly KEYCHAIN_SERVICE=com.harmflow.local-api
readonly KEYCHAIN_ACCOUNT=token
# Launch Services' own tool; update-app_test.sh replaces it with a fake.
lsregister=/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister

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

# window_pids prints the pids of the harmflow-desktop processes whose executable is the installed copy's or this
# checkout's build, where the window ran before the app had an installed copy.
window_pids() {
	ps -axww -o pid=,comm= | awk -v a="$window_exe" -v b="$built/Contents/MacOS/harmflow-desktop" \
		'{ p = $1; sub(/^ *[0-9]+ +/, ""); if ($0 == a || $0 == b) print p }'
}

# use_launch_agent succeeds when the loaded launch agent serves this data directory and runs the installed harmflowd,
# so launchd keeps supervising the daemon it restarts. The agent is one per user, at ~/Library/LaunchAgents, whatever
# the data directory: its ProgramArguments say which directory it serves, by --home, or the default one without it.
# The daemon writes the plist with its own executable when Autostart is turned on, and launchd keeps the program it
# loaded until the next login, so both must be the installed harmflowd. It never edits, unloads, or kickstarts the
# agent to fix either.
use_launch_agent() {
	local plist="$HOME/Library/LaunchAgents/$LABEL.plist" pb=/usr/libexec/PlistBuddy loaded
	if ! loaded=$(launchctl print "gui/$(id -u)/$LABEL" 2>/dev/null); then
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
		die "launch agent $LABEL runs $exe, not the installed $daemon_exe; a kickstart would start that binary." \
			"Nothing was stopped. To move it: turn Autostart off in Settings, which deletes $plist; run make update-app" \
			"again, which starts the installed harmflowd; then turn Autostart on, which writes $plist with it"
	local program
	program=$(printf '%s\n' "$loaded" | sed -n 's/^[[:space:]]*program = //p' | head -n 1)
	if [[ $program != "$daemon_exe" ]]; then
		say "launch agent $LABEL: $plist runs the installed harmflowd, but launchd loaded ${program:-another program}" \
			"before it changed and keeps it until the next login; harmflowd starts directly"
		return 1
	fi
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
	open -n ${env[@]+"${env[@]}"} "$installed"
	for ((i = 0; i < 50; i++)); do
		if [[ -n $(window_pids) ]]; then
			say "window opened, pid $(window_pids | tr '\n' ' ')"
			return
		fi
		sleep 0.2
	done
	say "the window did not start within 10 seconds: open $installed" >&2
	return 1
}

# stage_app copies the build beside the installed copy, on its volume, so install_app replaces it by a rename. The copy
# has no .app extension, so Launch Services does not take it for a bundle.
stage_app() {
	local dir=${installed%/*}
	mkdir -p "$dir" || die "cannot create $dir; nothing was stopped"
	staging=$(mktemp -d "$dir/.HarmFlow-update.XXXXXX") || die "cannot write to $dir; nothing was stopped"
	/usr/bin/ditto "$built" "$staging/HarmFlow" || die "cannot copy $built to $dir; nothing was stopped"
}

# install_app moves the old copy aside and the staged one into its place, two renames on one volume, so the path never
# holds a partial bundle; it puts the old copy back when the second rename fails.
install_app() {
	if [[ -e $installed ]]; then
		mv "$installed" "$staging/previous.app" || die "cannot move $installed aside; it was left as it was"
	fi
	if ! mv "$staging/HarmFlow" "$installed"; then
		[[ -e $staging/previous.app ]] && mv "$staging/previous.app" "$installed"
		die "cannot move the new build to $installed; the previous copy was put back"
	fi
	rm -rf "$staging"
	staging=''
	say "installed $commit at $installed"
}

# registered_paths ID prints the path of every bundle with identifier ID in Launch Services' database, one per line.
# Each record of lsregister -dump starts after a line of dashes, its path as "path: <path> (0x...)".
registered_paths() {
	"$lsregister" -dump | awk -v id="$1" '
		/^-+$/ { p = "" }
		/^path: / { p = $0; sub(/^path: +/, "", p); sub(/ \(0x[0-9a-f]+\)$/, "", p) }
		$1 == "identifier:" && $2 == id && p != "" { print p; p = "" }'
}

# register_app makes the installed copy the one macOS launches by name (Spotlight, Launchpad, open -a), harmflow-app#186.
# It registers that copy, then unregisters the bundles with its identifier that compete with it: those in this
# checkout's target/ directories and those whose path no longer exists, such as an unmounted DMG volume. It leaves
# registered every other bundle that exists, such as a worktree's build, and it deletes no file. A failure here only
# warns: the app is already installed.
register_app() {
	local id path reason
	id=$(json_get "$root/desktop/src-tauri/tauri.conf.json" identifier)
	[[ -n $id ]] || {
		say "cannot read the bundle identifier from tauri.conf.json; Launch Services was left as it was" >&2
		return 1
	}
	"$lsregister" -f "$installed" || {
		say "lsregister -f $installed failed; launching HarmFlow by name may open another build" >&2
		return 1
	}
	say "registered $installed with Launch Services"
	while IFS= read -r path; do
		if [[ $path == "$root/desktop/src-tauri/target/"* ]]; then
			reason="a build in this checkout"
		elif [[ ! -e $path ]]; then
			reason="no longer exists"
		else
			continue
		fi
		if "$lsregister" -u "$path"; then
			say "unregistered $path ($reason)"
		else
			say "lsregister -u $path failed" >&2
		fi
	done < <(registered_paths "$id")
}

# rerun_if_changed BEFORE [ARG...] runs the update-app.sh that the pull brought, in place of this one, when it differs
# from the one at BEFORE (harmflow-app#231): bash read this whole file before the pull, so the rest of this run would
# be the old script. Nothing is stopped or built yet, so the new one decides everything. UPDATE_APP_RERUN_FROM tells
# the new one where the pull started, and that it is the rerun, so it never runs itself again.
rerun_if_changed() {
	local before=$1 self=packaging/macos/update-app.sh
	shift
	[[ -z ${UPDATE_APP_RERUN_FROM:-} ]] || return 0
	git -C "$root" diff --quiet "$before" HEAD -- "$self" && return 0
	say "the pull changed $self; running the new one"
	rm -rf "$tmp"
	UPDATE_APP_RERUN_FROM=$before exec "$root/$self" "$@"
}

main() {
	root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)
	built="$root/desktop/src-tauri/target/release/bundle/macos/HarmFlow.app"
	local install_dir=${INSTALL_DIR:-$HOME/Applications}
	[[ $install_dir == /* ]] || die "INSTALL_DIR must be an absolute path, not $install_dir; nothing was changed"
	installed="$(canon "$install_dir")/HarmFlow.app"
	window_exe="$installed/Contents/MacOS/harmflow-desktop"
	daemon_exe="$installed/Contents/MacOS/harmflowd"
	default_home="$HOME/Library/Application Support/HarmFlow"
	home="${HARMFLOW_HOME:-$default_home}"
	runtime="$home/run/daemon.json"
	tmp=$(mktemp -d)
	staging=''
	trap 'rm -rf "$tmp" ${staging:+"$staging"}' EXIT

	# 1. Update: refuse before anything changes.
	local branch
	branch=$(git -C "$root" symbolic-ref --short -q HEAD) || die "HEAD is detached, not on main; nothing was changed"
	[[ $branch == main ]] || die "the checkout is on $branch, not main; nothing was changed"
	[[ -z $(git -C "$root" status --porcelain) ]] ||
		die "the working tree has uncommitted changes; commit or stash them first. Nothing was changed"
	local out before
	# A rerun from rerun_if_changed reports the pull from the commit the first run started at.
	before=${UPDATE_APP_RERUN_FROM:-$(git -C "$root" rev-parse HEAD | cut -c1-7)}
	out=$(git -C "$root" pull --ff-only origin main 2>&1) ||
		die "git pull --ff-only origin main cannot fast-forward; nothing was changed:"$'\n'"$out"
	# The Makefile's COMMIT for the new HEAD: a fixed 7 characters, where --short=7 may give more.
	commit=$(git -C "$root" rev-parse HEAD | cut -c1-7)
	if [[ $commit == "$before" ]]; then say "main is up to date at $commit"; else say "main fast-forwarded from $before to $commit"; fi
	rerun_if_changed "$before" "$@"

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
	elif [[ ${OPEN:-} == 1 ]]; then
		say "no daemon runs for $home; OPEN=1 starts one"
		if use_launch_agent; then launchd=yes; fi
	else
		say "no daemon runs for $home"
	fi
	window_was=$(window_pids | tr '\n' ' ')
	if [[ -n $window_was ]]; then say "window pid $window_was"; else say "no window of $installed runs"; fi

	# 4. Copy the build beside the installed copy, so a copy that fails stops nothing.
	stage_app

	# 5. Stop the daemon, never with force, before the window, so a refusal leaves both running.
	if [[ -n $daemon_was ]]; then
		case $(api POST /daemon/stop '{}') in
		202) ;;
		409)
			die "executions are running, so nothing was stopped and $installed was left as it was." \
				"Run make update-app again when they end"
			;;
		*) die "the daemon refused to stop: $(cat "$tmp/body"). Nothing was stopped" ;;
		esac
		wait_gone "$old_pid" 30 || die "daemon pid $old_pid did not exit within 30 seconds. See $home/logs/daemon.log"
		say "daemon pid $old_pid stopped"
	fi

	# 6. Quit the window. SIGTERM ends it also with the tray on, where a plain quit is prevented.
	local w
	for w in $window_was; do
		kill -TERM "$w" 2>/dev/null || true
		wait_gone "$w" 15 || die "window pid $w did not exit within 15 seconds"
		say "window pid $w quit"
	done

	# 7. Replace the installed copy, now that nothing runs from it, and make it the one launched by name.
	install_app
	local status=0
	register_app || status=1

	# 8. Bring back what ran, from the installed copy, the daemon first, so the window connects to it at once. With
	# OPEN=1 both start also when they did not run: the window only offers to start a daemon that is down.
	if [[ -n $daemon_was || ${OPEN:-} == 1 ]]; then
		start_daemon
		verify_daemon "$old_pid" || status=1
	fi
	if [[ -n $window_was || ${OPEN:-} == 1 ]]; then open_window || status=1; fi
	if [[ -z $daemon_was && -z $window_was && ${OPEN:-} != 1 ]]; then
		say "nothing was running, so nothing was started; make update-app OPEN=1 opens $installed"
	fi
	return $status
}

# Sourced, as update-app_test.sh does, it only defines its functions.
if [[ ${BASH_SOURCE[0]} == "$0" ]]; then main "$@"; fi
