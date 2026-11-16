#!/usr/bin/env bash
# Builds the release binaries that `gh extension install` picks from:
# dist/gh-dojo_<tag>_<os>-<arch>[.exe], with <tag> stamped into
# `gh dojo --version`.
#
# cli/gh-extension-precompile runs it with the tag as the only argument
# (build_script_override in .github/workflows/release.yml); `make dist` runs it
# with the git describe version.
set -euo pipefail

tag="${1:?usage: $0 <tag>}"

platforms=(
  darwin-amd64
  darwin-arm64
  linux-amd64
  linux-arm64
  windows-amd64
  windows-arm64
)

rm -rf dist
mkdir -p dist
for p in "${platforms[@]}"; do
  goos="${p%-*}"
  goarch="${p#*-}"
  ext=""
  if [ "$goos" = windows ]; then
    ext=".exe"
  fi
  GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=$tag" \
    -o "dist/gh-dojo_${tag}_${p}${ext}" ./cmd/gh-dojo
done

# SHA-256 sums of the binaries, in the format `sha256sum -c` and
# `shasum -a 256 -c` read. gh picks a binary by its <os>-<arch> suffix, so
# this file is never mistaken for one.
sums="gh-dojo_${tag}_checksums.txt"
(
  cd dist
  if command -v sha256sum >/dev/null; then
    sha256sum gh-dojo_* >"$sums"
  else
    shasum -a 256 gh-dojo_* >"$sums"
  fi
)
ls dist
