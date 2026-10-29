#!/usr/bin/env bash
# Prints one part's coverage reading as Markdown, for the run summary of the nightly Coverage workflow, beside the
# floors that hold it, and writes the part's HTML report into build/coverage. It reads what
# `make coverage-daemon` or `make coverage-desktop` left, from the data the floors use: the checker's table for the
# daemon, desktop/ui/coverage/coverage-summary.json for the interface, and cargo llvm-cov's JSON summary for the
# shell. Ratios are truncated to two decimals and computed from the counts; no figure is added up. It fails on no
# figure: a reading below its floor is shown, not held.
#
# Usage: .github/scripts/coverage-summary.sh daemon|desktop
# With OUTCOME set to anything but "success" (the outcome of the step that ran the tests), the summary says that a
# test failed.
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
coverage=$root/build/coverage

# jq: a floor written as a percentage with at most two decimals, read into hundredths; hundredths printed with two
# decimals; and a row of a counter, its ratio truncated, beside its floor and the room above it.
jq_lib='
def hundredths: tostring | split(".") | (.[0] | tonumber) * 100 + ((.[1] // "") + "00" | .[:2] | tonumber);
def pct: "\(. / 100 | floor).\(. % 100 | tostring | if length < 2 then "0" + . else . end)";
# A parameter $x also defines a filter x, so the floor is $held here: a $floor would hide the floor built-in.
def row($name; $covered; $total; $held):
  if $total == 0 then "| \($name) | 0 | 0 | — | — | — |"
  else ($covered * 10000 / $total | floor) as $ratio
    | if $held == null then "| \($name) | \($covered) | \($total) | \($ratio | pct)% | none | — |"
      else ($held | hundredths) as $f | ($ratio - $f) as $room
        | "| \($name) | \($covered) | \($total) | \($ratio | pct)% | \($f | pct)% | \(
            if $room < 0 then "below its floor" elif $room == 0 then "0.00" else "+\($room | pct)" end) |"
      end
  end;'

header() {
  echo "| $1 | Covered | Total | Ratio | Floor | Headroom |"
  echo "|---|---:|---:|---:|---:|---:|"
}

daemon() {
  local profile=$coverage/daemon.out
  echo "## Daemon — statements per package"
  echo
  if [ ! -f "$profile" ]; then
    echo "No reading: the daemon's tests did not run to the end."
    return
  fi
  go -C "$root/daemon" tool cover -html="$profile" -o "$coverage/daemon.html"
  echo "The unit and integration data, merged, as \`make coverage-daemon\` reads them. A package with no floor is held"
  echo "at 0."
  echo
  # The checker's own table, with the room above each floor added: ratio and floor are both "<whole>.<two decimals>%".
  go -C "$root/tests" run ./coverage -floors "$root/daemon/coverage-floors.txt" -hold=false "$profile" | awk -F'|' '
    function hundredths(s) { gsub(/[ %`]/, "", s); if (s !~ /^[0-9]+\.[0-9][0-9]$/) return 0; sub(/\./, "", s); return s + 0 }
    NR == 1 { print $0 " Headroom |"; next }
    NR == 2 { print $0 "---:|"; next }
    {
      room = hundredths($5) - hundredths($6)
      if (room < 0) shown = "below its floor"
      else if (room == 0) shown = "0.00"
      else shown = sprintf("+%d.%02d", room / 100, room % 100)
      print $0 " " shown " |"
    }'
}

ui() {
  local summary=$root/desktop/ui/coverage/coverage-summary.json floors
  echo "### Interface — \`desktop/ui\`"
  echo
  if [ ! -f "$summary" ]; then
    echo "No reading: the interface's tests did not run to the end."
    return
  fi
  # The floors are the thresholds block of desktop/ui/vite.config.ts, one "<counter>: <ratio>," line each.
  floors=$(awk '
    /thresholds: \{/ { inside = 1; next }
    inside && /\}/ { exit }
    inside { gsub(/[ ,]/, ""); split($0, kv, ":"); printf "%s\"%s\":\"%s\"", sep, kv[1], kv[2]; sep = "," }
  ' "$root/desktop/ui/vite.config.ts")
  header Counter
  jq -r --argjson floors "{$floors}" "$jq_lib"'
    .total as $t | ["statements", "branches", "functions", "lines"][] as $c
    | row($c | .[:1] | ascii_upcase + ($c | .[1:]); $t[$c].covered; $t[$c].total; $floors[$c])' "$summary"
}

rust() {
  local out=$coverage/rust lines functions
  echo "### Shell — \`desktop/src-tauri\`"
  echo
  mkdir -p "$out"
  if ! (cd "$root/desktop/src-tauri" && cargo llvm-cov report --summary-only --json --output-path "$out/summary.json" \
    && cargo llvm-cov report --html --output-dir "$out") >&2; then
    echo "No reading: the shell's tests did not run."
    return
  fi
  lines=$(sed -n 's/^RUST_FLOOR_LINES := //p' "$root/Makefile")
  functions=$(sed -n 's/^RUST_FLOOR_FUNCTIONS := //p' "$root/Makefile")
  header Counter
  jq -r --arg lines "$lines" --arg functions "$functions" "$jq_lib"'
    .data[0].totals as $t | {Lines: "lines", Functions: "functions"} | to_entries[]
    | row(.key; $t[.value].covered; $t[.value].count; {$lines, $functions}[.value] | if . == "" then null else . end)' \
    "$out/summary.json"
}

if [ "${OUTCOME:-success}" != success ]; then
  echo "> [!WARNING]"
  echo "> The tests did not pass, so this job fails. A reading below comes from a run that did not finish."
  echo
fi
case "${1:-}" in
  daemon)
    daemon
    ;;
  desktop)
    echo "## Desktop"
    echo
    ui
    echo
    rust
    ;;
  *)
    echo "usage: $0 daemon|desktop" >&2
    exit 2
    ;;
esac
echo
echo "Headroom is the ratio less its floor: a floor may rise by that much, by hand, with its row in docs/coverage.md. The"
echo "HTML reports are in the run's \`coverage-$1\` artifact."
