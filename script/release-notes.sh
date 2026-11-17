#!/usr/bin/env bash
# Prints the notes of release <tag>: the subjects of the commits since the
# previous v* tag, merges left out, then the compare link.
#
# GitHub's generated notes list merged pull requests only, and most commits
# here go to main directly, so .github/workflows/release.yml replaces them
# with this text. Needs the full history and tags (fetch-depth: 0).
set -euo pipefail

tag="${1:?usage: $0 <tag>}"
repo="${GITHUB_REPOSITORY:-Trum-ok/gh-dojo}"

prev=$(git describe --tags --abbrev=0 --match 'v*' "$tag^" 2>/dev/null || true)
range="$tag"
if [[ -n "$prev" ]]; then
  range="$prev..$tag"
fi

git log --no-merges --format='- %s (%h)' "$range"
if [[ -n "$prev" ]]; then
  printf '\n**Full Changelog**: https://github.com/%s/compare/%s...%s\n' "$repo" "$prev" "$tag"
fi
