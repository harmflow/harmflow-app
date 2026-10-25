#!/usr/bin/env bash
# Installs the gitleaks CLI for the secret scans of the CI and Security workflows, on a Linux x64 runner: a release
# binary, checked against its SHA-256 before it runs, and put on the PATH of the job's next steps. The CLI is MIT and
# needs no key; gitleaks/gitleaks-action needs a license key for a repository an organization owns.
#
# Dependabot cannot see this pin: refresh the version and the checksum by hand, from the release's
# gitleaks_<version>_checksums.txt.
set -euo pipefail

version=8.30.1
sha256=551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb
archive=gitleaks_${version}_linux_x64.tar.gz
dir=$RUNNER_TEMP/gitleaks

mkdir -p "$dir"
curl --fail --silent --show-error --location --retry 3 --retry-all-errors --retry-delay 2 \
  --output "$dir/$archive" "https://github.com/gitleaks/gitleaks/releases/download/v$version/$archive"
echo "$sha256  $dir/$archive" | sha256sum --check
tar -xzf "$dir/$archive" -C "$dir"
echo "$dir" >> "$GITHUB_PATH"
"$dir/gitleaks" version
