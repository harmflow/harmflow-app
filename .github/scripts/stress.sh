#!/usr/bin/env bash
# The steps of a job of the nightly Stress workflow: it starts the job's CPU load, if any, runs each suite beside it,
# stops the load, and prints the run's summary as Markdown. Each suite's output, duration, exit status, and 1-minute
# load average at its start and end go to build/stress, which the summary reads.
#
# Usage: .github/scripts/stress.sh load-start|load-stop|run daemon|run integration|summary
# load-start starts LOAD_PER_CORE busy processes (`yes`) per core, 2 when unset and none at 0. run repeats the suite
# COUNT times (3 when unset), within DAEMON_TIMEOUT or INTEGRATION_TIMEOUT (go test durations), and exits with the
# suite's status.
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
out=$root/build/stress
mkdir -p "$out"

# The 1-minute load average, on Linux and on macOS.
load_average() {
  if [ -r /proc/loadavg ]; then
    awk '{print $1}' /proc/loadavg
  else
    sysctl -n vm.loadavg | awk '{print $2}'
  fi
}

load_start() {
  local cores busy
  cores=$(getconf _NPROCESSORS_ONLN)
  busy=$((cores * ${LOAD_PER_CORE:-2}))
  : >"$out/load.pids"
  printf 'cores=%s\nbusy=%s\n' "$cores" "$busy" >"$out/load.env"
  # BSD seq counts down from 1 to 0 for `seq 0`, so no load is a case of its own.
  if [ "$busy" -eq 0 ]; then
    echo "No load: the suites run alone on $cores cores."
    return
  fi
  for _ in $(seq "$busy"); do
    nohup yes >/dev/null 2>&1 &
    echo $! >>"$out/load.pids"
  done
  echo "Started $busy busy processes on $cores cores."
}

load_stop() {
  [ -f "$out/load.pids" ] || return 0
  while read -r pid; do
    kill "$pid" 2>/dev/null || true
  done <"$out/load.pids"
  rm -f "$out/load.pids"
  echo "Stopped the load."
}

run_suite() {
  local suite=$1 count=${COUNT:-3} status=0 start target flags
  case $suite in
    daemon) target=test-daemon flags="-count=$count -timeout ${DAEMON_TIMEOUT:-45m}" ;;
    integration) target=test-integration flags="-count=$count -timeout ${INTEGRATION_TIMEOUT:-90m}" ;;
    *) echo "unknown suite: $suite" >&2; exit 2 ;;
  esac
  printf 'load_start=%s\n' "$(load_average)" >"$out/$suite.env"
  start=$(date +%s)
  # The daemon's unit tests run with -race, as in CI.
  if [ "$suite" = daemon ]; then
    GOFLAGS=-race make -C "$root" "$target" TEST_FLAGS="$flags" 2>&1 | tee "$out/$suite.log" || status=$?
  else
    make -C "$root" "$target" TEST_FLAGS="$flags" 2>&1 | tee "$out/$suite.log" || status=$?
  fi
  printf 'seconds=%s\nstatus=%s\nload_end=%s\nflags=%s\n' \
    "$(($(date +%s) - start))" "$status" "$(load_average)" "$flags" >>"$out/$suite.env"
  return "$status"
}

# The value of key in a file of key=value lines, or "—".
field() {
  local value
  value=$(sed -n "s/^$2=//p" "$1" 2>/dev/null | tail -n 1 || true)
  echo "${value:-—}"
}

# Each failing test of a suite's output, with the lines go test printed under it, at most 15 a test.
failures() {
  awk '
    /^[[:space:]]*--- FAIL: / { shown = 0; keep = 1; print; next }
    /^(=== |--- |FAIL|ok |PASS|panic: )/ { keep = 0 }
    keep && shown < 15 { print; shown++ }
    /^panic: / { print; keep = 1; shown = 0 }
  ' "$1"
}

summary() {
  local suite env log seconds
  if [ "$(field "$out/load.env" busy)" = 0 ]; then
    echo "## Suites repeated, no load"
    echo
    echo "Alone on $(field "$out/load.env" cores) cores, ${COUNT:-3} times each."
  else
    echo "## Suites under load"
    echo
    echo "Beside $(field "$out/load.env" busy) busy processes on $(field "$out/load.env" cores) cores," \
      "${COUNT:-3} times each."
  fi
  echo
  echo "| Suite | Result | Duration | Load at start | Load at end | Flags |"
  echo "|---|---|---:|---:|---:|---|"
  for suite in daemon integration; do
    env=$out/$suite.env
    if [ ! -f "$env" ]; then
      echo "| $suite | not run | — | — | — | — |"
      continue
    fi
    seconds=$(field "$env" seconds)
    case $(field "$env" status) in
      0) result=passed ;;
      —) result="did not end" ;;
      *) result=failed ;;
    esac
    [ "$seconds" = — ] || seconds="$((seconds / 60)) min $((seconds % 60)) s"
    printf '| %s | %s | %s | %s | %s | `%s` |\n' "$suite" "$result" "$seconds" \
      "$(field "$env" load_start)" "$(field "$env" load_end)" "$(field "$env" flags)"
  done
  for suite in daemon integration; do
    log=$out/$suite.log
    [ -f "$log" ] && [ "$(field "$out/$suite.env" status)" != 0 ] || continue
    echo
    echo "### Failures of $suite"
    echo
    echo '```'
    failures "$log"
    echo '```'
  done
}

case ${1:-} in
  load-start) load_start ;;
  load-stop) load_stop ;;
  run) run_suite "${2:-}" ;;
  summary) summary ;;
  *) echo "usage: $0 load-start|load-stop|run daemon|run integration|summary" >&2; exit 2 ;;
esac
