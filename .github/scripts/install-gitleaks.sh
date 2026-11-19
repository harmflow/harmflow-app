#!/usr/bin/env bash
# Installs the gitleaks CLI for the secret scans of the CI and Security workflows, on a Linux x64 runner: a release
# binary, checked against its SHA-256 before it runs, and put on the PATH of the job's next steps. The CLI is MIT and
# needs no key; gitleaks/gitleaks-action needs a license key for a repository an organization owns.
#
# Dependabot cannot see this pin: refresh the version and the checksum by hand, from the release's
# gitleaks_<version>_checksums.txt.
#
# The archive is kept in $RUNNER_TEMP/gitleaks-archive, which the workflows keep in the Actions cache: run with
# --cache-key first, which writes that folder and a key made of the version and the checksum to the step's outputs,
# then restore the cache, then run with no argument. A restored archive is checked against the checksum as a
# downloaded one is; one that fails the check is downloaded again. A download retries over about ten minutes, with a
# growing delay, so a short outage of the release host does not fail the scan.
set -euo pipefail

version=8.30.1
sha256=551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb
archive=gitleaks_${version}_linux_x64.tar.gz
cache=$RUNNER_TEMP/gitleaks-archive
dir=$RUNNER_TEMP/gitleaks

if [[ ${1:-} == --cache-key ]]; then
  {
    echo "path=$cache"
    echo "key=gitleaks-$version-linux-x64-$sha256"
  } >> "$GITHUB_OUTPUT"
  exit 0
fi

check() { echo "$sha256  $cache/$archive" | sha256sum --check; }

mkdir -p "$cache" "$dir"
if [[ -f $cache/$archive ]] && check; then
  echo "gitleaks $version restored from the cache"
else
  rm -f "$cache/$archive"
  # With no --retry-delay, curl doubles its wait after each failed try, from one second up to ten minutes.
  curl --fail --silent --show-error --location --retry 10 --retry-all-errors --retry-max-time 600 \
    --connect-timeout 30 --output "$cache/$archive" \
    "https://github.com/gitleaks/gitleaks/releases/download/v$version/$archive"
  check
fi
tar -xzf "$cache/$archive" -C "$dir"
echo "$dir" >> "$GITHUB_PATH"
"$dir/gitleaks" version
