#!/usr/bin/env bash
# Tests of check-version.sh: against a checkout of its own holding the six version files, each case with one of them
# changed, and against this repository's own files at its own version. `make test-packaging` runs it.
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
repo=$(cd "$here/../.." && pwd -P)
script=$here/check-version.sh
files=(Makefile daemon/cmd/harmflowd/main.go desktop/src-tauri/tauri.conf.json desktop/src-tauri/Cargo.toml
  desktop/package.json desktop/ui/package.json)

scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
failures=0
fail() {
  printf 'FAIL %s: %s\n' "$case_name" "$*" >&2
  failures=$((failures + 1))
}

# write FILE VERSION writes one version file of $root, laid out as the repository's own, at VERSION. Cargo.toml's
# dependency tables carry a version of their own, which the check must not read.
write() {
  local path=$root/$1
  mkdir -p "$(dirname "$path")"
  case $1 in
    Makefile) printf 'SHELL := /bin/bash\nVERSION := %s\n' "$2" >"$path" ;;
    *.go) printf 'package main\n\n// Version is set at build time.\nvar Version = "%s"\n' "$2" >"$path" ;;
    *.json) printf '{\n  "name": "fixture",\n  "version": "%s",\n  "private": true\n}\n' "$2" >"$path" ;;
    *.toml) printf '[package]\nname = "fixture"\nversion = "%s"\n\n[dependencies.serde]\nversion = "9.9.9"\n' "$2" >"$path" ;;
  esac
}

# tree NAME starts a case: a checkout of its own at $root, its six files at 1.2.3.
tree() {
  case_name=$1
  root=$scratch/$1
  local file
  for file in "${files[@]}"; do write "$file" 1.2.3; done
}

# run TAG [ROOT] runs the check on $root, or ROOT; it sets rc and err.
run() {
  rc=0
  err=$("$script" "$1" "${2:-$root}" 2>&1 >/dev/null) || rc=$?
}

# named FILE...: the check's output names each FILE as differing, and no other.
named() {
  local file count=0
  for file in "$@"; do
    grep -qF "check-version: $file: " <<<"$err" || fail "does not name $file: $err"
  done
  count=$(grep -c '^check-version: [^ ]*: ' <<<"$err" || true)
  [ "$count" -eq $# ] || fail "names $count fields, not $#: $err"
}

tree agree
run v1.2.3
[ "$rc" -eq 0 ] || fail "rc $rc: $err"

for file in "${files[@]}"; do
  tree "differs-${file//\//-}"
  write "$file" 1.2.4
  run v1.2.3
  [ "$rc" -eq 1 ] || fail "rc $rc, not 1"
  named "$file"
done

tree all-differ
run v2.0.0
[ "$rc" -eq 1 ] || fail "rc $rc, not 1"
named "${files[@]}"

# A [package] table with no version: the dependency's version, which is the tag's, must not count.
tree package-without-version
printf '[package]\nname = "fixture"\n\n[dependencies.serde]\nversion = "1.2.3"\n' >"$root/desktop/src-tauri/Cargo.toml"
run v1.2.3
[ "$rc" -eq 1 ] || fail "rc $rc, not 1"
named desktop/src-tauri/Cargo.toml
grep -qF 'found no version of [package]' <<<"$err" || fail "does not say the version is missing: $err"

tree missing-file
rm "$root/desktop/ui/package.json"
run v1.2.3
[ "$rc" -eq 1 ] || fail "rc $rc, not 1"
named desktop/ui/package.json

for bad in '' v1.2 1.2.3 v1.2.3-rc1 v1.2.3.4 v1.2.x; do
  tree "tag-${bad:-empty}"
  run "$bad"
  [ "$rc" -eq 2 ] || fail "rc $rc, not 2, for \"$bad\""
  grep -qF 'is not a version tag' <<<"$err" || fail "does not refuse \"$bad\": $err"
done

# The workflow reads the repository's own files: they hold the six fields where the check looks, and agree.
case_name=repository
run "v$(sed -n 's/^VERSION := //p' "$repo/Makefile")" "$repo"
[ "$rc" -eq 0 ] || fail "rc $rc: $err"

if [ "$failures" -gt 0 ]; then
  echo "check-version-test: $failures failures" >&2
  exit 1
fi
echo "check-version-test: ok"
