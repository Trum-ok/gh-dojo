#!/usr/bin/env bash
# Prints the notes of release <tag>: the user-facing changes since the
# previous v* tag, grouped by kind, then the compare link.
#
# A user-facing change is a `Changelog: <kind>: <text>` trailer in a commit
# message (CONTRIBUTING.md, Release notes); a commit may have several. The
# kinds, in the order printed: breaking, added, changed, fixed. A trailer of
# another kind goes under "Other" with a warning on stderr, so a typo never
# fails a release. A trailer that names no issue gets the issues its commit
# names after a keyword (`Refs #12`, `Fixes #12`, as gh dojo release finds
# them). Commits without a trailer are internal: counted, not listed. Merges
# are left out.
#
# `script/release-notes.sh HEAD` previews the notes of the next release.
#
# GitHub's generated notes list merged pull requests only, and most commits
# here go to main directly, so .github/workflows/release.yml replaces them
# with this text. Needs the full history and tags (fetch-depth: 0).
set -euo pipefail

tag="${1:?usage: $0 <tag>   (HEAD for the next release)}"
repo="${GITHUB_REPOSITORY:-Trum-ok/gh-dojo}"

prev=$(git describe --tags --abbrev=0 --match 'v*' "$tag^" 2>/dev/null || true)
range="$tag"
if [[ -n "$prev" ]]; then
  range="$prev..$tag"
fi

breaking="" added="" changed="" fixed="" other=""
internal=0

# refs <message>: the issues the message names after a keyword, as
# "#12, #15". Text in backticks is an example, not a reference.
refs() {
  # The backticks are literal (SC2016). sed, not ${1//...} (SC2001): it works
  # line by line, so an unpaired backtick never swallows the lines after it,
  # and a bash pattern has no [^`]* without extglob.
  # shellcheck disable=SC2016,SC2001
  sed 's/`[^`]*`//g' <<<"$1" |
    grep -oiE '\b(close[sd]?|fix(e[sd])?|resolve[sd]?|refs)[[:space:]]+#[0-9]+([[:space:]]*,[[:space:]]*#[0-9]+)*' |
    grep -oE '#[0-9]+' | awk '!seen[$0]++' | paste -sd, - | sed 's/,/, /g' || true
}

# changelog <message>: the values of its Changelog lines, one per line, an
# indented line after one joined to it. Read here rather than with git's
# %(trailers): git takes the last paragraph for trailers only when every line
# of it is one, so a `Fixes #12` line among them would drop them silently.
changelog() {
  awk '
    /^[Cc]hangelog:/ { if (v != "") print v; v = $0; sub(/^[Cc]hangelog:[ \t]*/, "", v); next }
    /^[ \t]+[^ \t]/ && v != "" { s = $0; sub(/^[ \t]+/, "", s); v = v " " s; next }
    { if (v != "") print v; v = "" }
    END { if (v != "") print v }
  ' <<<"$1"
}

for sha in $(git rev-list --no-merges --reverse "$range"); do
  message="$(git log -1 --format=%B "$sha")"
  trailers="$(changelog "$message")"
  if [[ -z "${trailers//[[:space:]]/}" ]]; then
    internal=$((internal + 1))
    continue
  fi
  issues="$(refs "$message")"
  while IFS= read -r line; do
    [[ -n "${line//[[:space:]]/}" ]] || continue
    kind="${line%%:*}"
    text="${line#*:}"
    text="${text#"${text%%[![:space:]]*}"}"
    if [[ "$kind" == "$line" || -z "$text" ]]; then
      kind="" text="$line"
    fi
    kind="$(tr '[:upper:]' '[:lower:]' <<<"$kind" | tr -d '[:space:]')"
    if [[ -n "$issues" && ! "$text" =~ \#[0-9]+ ]]; then
      text="$text ($issues)"
    fi
    entry="- $text"$'\n'
    case "$kind" in
      breaking) breaking+="$entry" ;;
      added) added+="$entry" ;;
      changed) changed+="$entry" ;;
      fixed) fixed+="$entry" ;;
      *)
        echo "warning: $(git rev-parse --short "$sha"): Changelog kind \"$kind\" is none of breaking, added, changed, fixed; listed under Other" >&2
        other+="$entry"
        ;;
    esac
  done <<<"$trailers"
done

printed=no
section() {
  if [[ -n "$2" ]]; then
    if [[ "$printed" == yes ]]; then
      echo
    fi
    printf '### %s\n\n%s' "$1" "$2"
    printed=yes
  fi
}
section "Breaking changes" "$breaking"
section "Added" "$added"
section "Changed" "$changed"
section "Fixed" "$fixed"
section "Other" "$other"

if [[ "$printed" == no ]]; then
  echo "No user-facing changes."
fi
if ((internal > 0)); then
  s="s"
  if ((internal == 1)); then
    s=""
  fi
  printf '\nAlso %d internal commit%s.\n' "$internal" "$s"
fi
if [[ -n "$prev" ]]; then
  printf '\n**Full Changelog**: https://github.com/%s/compare/%s...%s\n' "$repo" "$prev" "$tag"
fi
