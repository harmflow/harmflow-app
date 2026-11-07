#!/usr/bin/env bash
# Prints the daemon's dead code as Markdown, for the run summary of the nightly Dead code workflow: the functions that
# nothing reaches from main or from a test (deadcode -test), the unexported names that nothing uses (staticcheck's
# U1000, tests included), and, folded, the functions that only a test reaches (deadcode without -test, less the first
# list). It reads the daemon as macOS and as Linux build it: a function is listed when every platform that builds its
# file finds it dead, and staticcheck merges its two readings the same way. A finding is a lead for a person, never a
# failure: the script fails only when a tool cannot read the code.
#
# Usage: .github/scripts/dead-code.sh
# DEADCODE and STATICCHECK name each tool as go install takes it, path@version; make dead-code sets both.
set -euo pipefail

: "${DEADCODE:?set DEADCODE to deadcode's path@version, as make dead-code does}"
: "${STATICCHECK:?set STATICCHECK to staticcheck's path@version, as make dead-code does}"

root=$(cd "$(dirname "$0")/../.." && pwd)
work=$root/build/dead-code
platforms=(darwin linux)

rm -rf "$work"
mkdir -p "$work/bin"
# Built once, for this machine: go run under another GOOS would build a binary that this machine cannot run.
GOBIN=$work/bin go install "$DEADCODE" >&2
GOBIN=$work/bin go install "$STATICCHECK" >&2
cd "$root/daemon"
# The daemon has no cgo, and a build for another platform turns cgo off, so every platform is read without it.
export CGO_ENABLED=0

# Runs a tool into a file. When the tool cannot read the code, prints why into the summary and fails the run.
run() {
  local what=$1 out=$2
  shift 2
  if ! "$@" >"$out" 2>"$out.err"; then
    echo "> [!CAUTION]"
    echo "> $what could not read the daemon, so this run reports nothing and fails."
    echo
    echo '```'
    cat "$out.err" "$out"
    echo '```'
    exit 1
  fi
}

# One "<platform>\tfile\t<file>" line per Go file the platform builds, tests included, relative to daemon/.
files() {
  GOOS=$1 go list -f '{{$d := .Dir}}{{range .GoFiles}}{{$d}}/{{.}}{{"\n"}}{{end}}{{range .TestGoFiles}}{{$d}}/{{.}}{{"\n"}}{{end}}{{range .XTestGoFiles}}{{$d}}/{{.}}{{"\n"}}{{end}}' ./...
}

# One "<platform>\tdead\t<function>\t<file>\t<line>" line per function in deadcode's JSON.
funcs() {
  jq -r --arg os "$1" --arg pwd "$PWD/" \
    '.[]?.Funcs[]? | [$os, "dead", .Name, (.Position.File | ltrimstr($pwd)), .Position.Line] | @tsv' "$2"
}

for os in "${platforms[@]}"; do
  run "go list" "$work/files-$os" files "$os"
  run "deadcode -test ($os)" "$work/test-$os.json" env GOOS="$os" "$work/bin/deadcode" -json -test ./...
  run "deadcode ($os)" "$work/main-$os.json" env GOOS="$os" "$work/bin/deadcode" -json ./...
done

# The functions of one deadcode reading that every platform building their file finds dead, one
# "<function>\t<file>\t<line>\t<platforms>" line each, sorted. A file no platform lists is taken as built by those that
# report it, so a path that does not match is shown rather than dropped.
merged() {
  local reading=$1 os
  for os in "${platforms[@]}"; do
    awk -v os="$os" -v pwd="$PWD/" 'index($0, pwd) == 1 { print os "\tfile\t" substr($0, length(pwd) + 1) }' \
      "$work/files-$os"
    funcs "$os" "$work/$reading-$os.json"
  done | awk 'BEGIN { FS = OFS = "\t"; name["darwin"] = "macOS"; name["linux"] = "Linux" }
    $2 == "file" { builds[$3]++; next }
    {
      key = $3 FS $4 FS $5
      if (!(key in found)) order[++n] = key
      found[key]++
      on[key] = on[key] (on[key] == "" ? "" : ", ") name[$1]
      file[key] = $4
    }
    END { for (i = 1; i <= n; i++) { k = order[i]; if (found[k] >= builds[file[k]]) print k, on[k] } }' | sort
}

merged test >"$work/unreachable.tsv"
merged main >"$work/main.tsv"
# Dead from main, less dead from the tests: what only a test reaches.
awk -F'\t' 'NR == FNR { dead[$1 FS $2 FS $3]; next } !(($1 FS $2 FS $3) in dead)' \
  "$work/unreachable.tsv" "$work/main.tsv" >"$work/tests-only.tsv"

# staticcheck reads every platform of the matrix and merges the readings itself. -fail= keeps a finding from failing it,
# so a failure is code it cannot read.
for os in "${platforms[@]}"; do echo "$os: GOOS=$os"; done >"$work/matrix"
unused() { "$work/bin/staticcheck" -matrix -f json -checks U1000 -fail= ./... <"$work/matrix"; }
run "staticcheck" "$work/unused.json" unused
jq -r --arg pwd "$PWD/" 'select(.code == "U1000") | [.message, (.location.file | ltrimstr($pwd)), .location.line] | @tsv' \
  "$work/unused.json" | sort >"$work/unused.tsv"

# A file and line of daemon/, linked to the run's commit when the run is GitHub's.
where='{ path = "daemon/" $2 ":" $3 }
  base != "" { path = "[`" path "`](" base "/daemon/" $2 "#L" $3 ")" }
  base == "" { path = "`" path "`" }'
base=
if [ -n "${GITHUB_SHA:-}" ]; then
  base=${GITHUB_SERVER_URL:-https://github.com}/${GITHUB_REPOSITORY}/blob/$GITHUB_SHA
fi

# Prints a section's table, or says it has no row.
functions() {
  if [ ! -s "$1" ]; then
    echo "None."
    return
  fi
  echo "| Function | Where | Platforms |"
  echo "|---|---|---|"
  awk -F'\t' -v base="$base" "$where"' { printf "| `%s` | %s | %s |\n", $1, path, $4 }' "$1"
}

count() { awk 'END { print NR }' "$1"; }

echo "## Daemon — dead code"
echo
echo "The daemon at this run's commit, as macOS and Linux build it. A finding is a lead, not a verdict: reflection, a"
echo "build that adds to the daemon, or a platform read nowhere here may still reach it. No finding fails this run."
echo
echo "### Unreachable functions: $(count "$work/unreachable.tsv")"
echo
echo "Nothing reaches them, from \`main\` or from a test (\`deadcode -test\`)."
echo
functions "$work/unreachable.tsv"
echo
echo "### Unused names: $(count "$work/unused.tsv")"
echo
echo "Unexported names that nothing uses, tests included (\`staticcheck\`, check U1000). An exported name is left out,"
echo "since another package may use it."
echo
if [ -s "$work/unused.tsv" ]; then
  echo "| Finding | Where |"
  echo "|---|---|"
  awk -F'\t' -v base="$base" "$where"' { printf "| %s | %s |\n", $1, path }' "$work/unused.tsv"
else
  echo "None."
fi
echo
echo "<details><summary>Reached only from tests: $(count "$work/tests-only.tsv")</summary>"
echo
echo "\`main\` reaches none of them, but a test does (\`deadcode\` without \`-test\`, less the unreachable functions). A"
echo "test may be all that keeps one alive, or it may be there on purpose: for a test, or for a build that adds to the"
echo "daemon."
echo
functions "$work/tests-only.tsv"
echo
echo "</details>"
