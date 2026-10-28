#!/usr/bin/env bash
# Checks that a version tag is the version of all six version fields. The desktop and the daemon ship at one version,
# in one app, and a tag whose fields disagree would release an app that reports two. The release workflow runs it on a
# pushed tag, before it creates the release; `make check-version` runs it locally.
#
# Usage: .github/scripts/check-version.sh <tag> [<root>]
#   <tag>   vMAJOR.MINOR.PATCH, digits only
#   <root>  the checkout to read; this script's own by default
# It names every field that differs from the tag, or holds no version, then fails. A tag of another form fails too.
set -euo pipefail

tag=${1:-}
root=${2:-$(cd "$(dirname "$0")/../.." && pwd)}

if ! [[ $tag =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "check-version: \"$tag\" is not a version tag, vMAJOR.MINOR.PATCH with digits only" >&2
  exit 2
fi
version=${tag#v}
failures=0

# check FILE FIELD VALUE compares one field with the tag. In a workflow run, a difference is also an error annotation
# on the file, so the run's summary names it.
check() {
  local file=$1 field=$2 value=$3 message
  if [ -z "$value" ]; then
    message="$file: found no $field"
  elif [ "$value" != "$version" ]; then
    message="$file: $field is $value, not $version as the tag $tag says"
  else
    return 0
  fi
  echo "check-version: $message" >&2
  [ "${GITHUB_ACTIONS:-}" != true ] || echo "::error file=$file::$message"
  failures=$((failures + 1))
}

json_version() { jq -r '.version // empty' "$root/$1"; }

check Makefile VERSION "$(sed -n 's/^VERSION := //p' "$root/Makefile")"
check daemon/cmd/harmflowd/main.go Version \
  "$(sed -n 's/^var Version = "\(.*\)"$/\1/p' "$root/daemon/cmd/harmflowd/main.go")"
check desktop/src-tauri/tauri.conf.json version "$(json_version desktop/src-tauri/tauri.conf.json)"
# Only the [package] table's version: a dependency's table carries one too.
check desktop/src-tauri/Cargo.toml "version of [package]" "$(awk '
  /^\[/ { in_package = ($0 == "[package]") }
  in_package && /^version *=/ { sub(/^version *= *"/, ""); sub(/".*$/, ""); print; exit }
' "$root/desktop/src-tauri/Cargo.toml")"
check desktop/package.json version "$(json_version desktop/package.json)"
check desktop/ui/package.json version "$(json_version desktop/ui/package.json)"

if [ "$failures" -gt 0 ]; then
  echo "check-version: the tag $tag differs from $failures of the 6 version fields" >&2
  exit 1
fi
echo "check-version: the six version fields are $version, as the tag $tag says"
