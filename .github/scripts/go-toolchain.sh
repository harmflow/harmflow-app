#!/usr/bin/env bash
# Keeps the Go toolchain of daemon/ and tests/ on the newest patch of its minor. setup-go installs exactly the
# toolchain line of go.mod, so a patch that fixes the standard library reaches CI only through it, and Dependabot
# updates the modules a go.mod requires but not that line. The releases are read from the Go module proxy, where the go
# command finds the toolchains it downloads.
#
# Usage: .github/scripts/go-toolchain.sh check|update
#   check   fails when a newer patch of the minor exists, or when Go no longer supports the minor
#   update  raises the toolchain line of both modules to the newest patch of the minor
# A new minor (1.26 to 1.27) is a change of its own: check names the end of support, but nothing here moves to one.
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
modules=(daemon tests)

toolchain() { sed -n 's/^toolchain go//p' "$root/$1/go.mod"; }

current=$(toolchain daemon)
[ -n "$current" ] || { echo "go-toolchain: daemon/go.mod names no toolchain" >&2; exit 1; }
for module in "${modules[@]}"; do
  [ "$(toolchain "$module")" = "$current" ] \
    || { echo "go-toolchain: $module/go.mod names go$(toolchain "$module"), daemon/go.mod go$current" >&2; exit 1; }
done

# Every release on the proxy, as 1.<minor>.<patch>; release candidates are left out.
releases=$(go list -m -versions golang.org/toolchain | tr ' ' '\n' \
  | sed -n 's/^v0\.0\.1-go\(1\.[0-9]*\.[0-9]*\)\.linux-amd64$/\1/p')
minor=${current%.*}
latest=$(echo "$releases" | grep "^${minor//./\\.}\." | sort -t. -k3 -n | tail -1)
[ -n "$latest" ] || { echo "go-toolchain: no release of Go $minor on the module proxy" >&2; exit 1; }
# Go supports its two newest minors: once 1.<minor + 2>.0 is out, the minor gets no more patches.
successor=1.$((${minor#1.} + 2)).0

case ${1:-} in
  check)
    if echo "$releases" | grep -qx "${successor//./\\.}"; then
      echo "go-toolchain: Go $minor is no longer supported since go$successor; move both modules to a newer minor" >&2
      exit 1
    fi
    if [ "$current" != "$latest" ]; then
      echo "go-toolchain: go$current is behind go$latest; run make go-toolchain" >&2
      exit 1
    fi
    echo "go-toolchain: go$current is the newest patch of Go $minor" ;;
  update)
    for module in "${modules[@]}"; do go -C "$root/$module" mod edit -toolchain="go$latest"; done
    echo "go-toolchain: go$current -> go$latest" ;;
  *)
    echo "usage: $0 check|update" >&2
    exit 2 ;;
esac
