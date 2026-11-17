#!/usr/bin/env bash
# Runs go test ./... (arguments go to go test, such as -race) and then
# fails on golden files no test read: a test that was removed or renamed
# leaves its golden file behind. GOLDEN_USED (see internal/testutil) records
# the files read; -count=1, since a cached test result reads nothing.
set -euo pipefail

cd "$(dirname "$0")/.."
used=$(mktemp)
trap 'rm -f "$used"' EXIT

GOLDEN_USED="$used" go test -count=1 "$@" ./...

root=$(pwd -P)
unused=$(comm -23 \
  <(find . -name '*.golden' -not -path './.claude/*' | sed "s|^\.|$root|" | sort -u) \
  <(sort -u "$used"))
if [[ -n "$unused" ]]; then
  echo "golden files no test reads; delete them or the test that lost them:"
  echo "${unused//$root\//}"
  exit 1
fi
