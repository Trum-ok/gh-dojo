#!/usr/bin/env bash
# Releases gh-dojo as <tag>, stopping at the first failure:
#
#   1. checks: on main, no uncommitted changes, main == origin/main, the tag
#      is newer than every v* tag;
#   2. tags HEAD (annotated) and pushes the tag;
#   3. waits for the release.yml run of that tag, then checks the release's
#      assets and that it is published, warning if it is not immutable;
#   4. installs the release with gh extension install, and gh dojo --version
#      must print the tag;
#   5. gh dojo release <tag> --verb done --comment <tag>.
#
# Step 5 moves the release's issues to Done, so run the script only on the
# user's word for the tag and for that list: `gh dojo release main --verb
# done --dry-run` shows it before tagging (CONTRIBUTING.md, Releases).
#
# Run it again after a failure: a tag that already exists at HEAD is not
# created again, and every later step is safe to repeat.
#
# Usage: script/release.sh <tag>   (or: make release TAG=<tag>)
set -euo pipefail

tag="${1:-}"
if [[ $# -ne 1 || -z "$tag" ]]; then
  echo "usage: $0 <tag>   (for example: $0 v0.7.0)" >&2
  exit 1
fi

cd "$(dirname "$0")/.."

# The assets build-release.sh writes, as release.yml uploads them.
platforms=(darwin-amd64 darwin-arm64 linux-amd64 linux-arm64 windows-amd64 windows-arm64)

# How long to wait for release.yml: for its run to appear, and to finish.
appear_timeout=120
run_timeout=1800

step() { printf '\n==> %s\n' "$*"; }

# fail <error> [<fix>]: prints them the way gh dojo does and exits 1.
fail() {
  echo "error: $1" >&2
  if [[ $# -gt 1 ]]; then
    echo "fix: $2" >&2
  fi
  exit 1
}

semver='^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'

# newer A B: whether tag A comes after tag B. A release comes after its own
# prereleases (v0.7.0 after v0.7.0-rc.2), which sort -V alone gets wrong.
newer() {
  local a="$1" b="$2" ca="${1%%-*}" cb="${2%%-*}"
  if [[ "$ca" != "$cb" ]]; then
    [[ "$(printf '%s\n%s\n' "$ca" "$cb" | sort -V | tail -n 1)" == "$ca" ]]
    return
  fi
  if [[ "$a" == "$ca" ]]; then # a is the release
    [[ "$b" != "$cb" ]]
    return
  fi
  if [[ "$b" == "$cb" ]]; then # b is the release, a its prerelease
    return 1
  fi
  [[ "$a" != "$b" && "$(printf '%s\n%s\n' "$a" "$b" | sort -V | tail -n 1)" == "$a" ]]
}

# --- 1. checks -------------------------------------------------------------

step "checks"

[[ "$tag" =~ $semver ]] ||
  fail "$tag is not a release tag" "use vMAJOR.MINOR.PATCH, or vMAJOR.MINOR.PATCH-rc.N for a prerelease"

branch="$(git symbolic-ref --quiet --short HEAD || true)"
[[ "$branch" == main ]] ||
  fail "on ${branch:-a detached HEAD}, not main" "git switch main"

# Untracked files are left alone: they are in neither the tag nor
# git describe's --dirty.
[[ -z "$(git status --porcelain --untracked-files=no)" ]] ||
  fail "uncommitted changes" "commit or drop them: git status"

git fetch --quiet --tags origin
repo="$(gh repo view --json nameWithOwner --jq .nameWithOwner)"
head="$(git rev-parse HEAD)"
[[ "$head" == "$(git rev-parse origin/main)" ]] ||
  fail "main is not origin/main: $(git rev-list --count origin/main..HEAD) ahead, $(git rev-list --count HEAD..origin/main) behind" \
    "push or pull main first, so the tag is a commit CI has seen"

if git rev-parse --quiet --verify "refs/tags/$tag" >/dev/null; then
  [[ "$(git rev-parse "$tag^{commit}")" == "$head" ]] ||
    fail "tag $tag already exists, at $(git rev-parse --short "$tag^{commit}"), not at HEAD" \
      "choose a newer tag"
  echo "tag $tag already exists at HEAD: going on from it"
  tagged=yes
else
  for t in $(git tag --list 'v*'); do
    [[ "$t" =~ $semver ]] || continue
    newer "$tag" "$t" ||
      fail "$tag is not newer than the existing tag $t" "choose a tag after $t"
  done
  tagged=no
fi
echo "main is at $(git rev-parse --short HEAD), the same as origin/main"

# --- 2. tag ----------------------------------------------------------------

step "tag $tag"

if [[ "$tagged" == no ]]; then
  git tag -a "$tag" -m "$tag"
fi
git push --quiet origin "refs/tags/$tag"
echo "pushed $tag"

# --- 3. release.yml --------------------------------------------------------

step "release.yml for $tag"

run=""
waited=0
while [[ -z "$run" ]]; do
  run="$(gh run list --workflow release.yml --branch "$tag" --commit "$head" --event push \
    --limit 1 --json databaseId --jq '.[0].databaseId // empty')"
  if [[ -z "$run" ]]; then
    ((waited >= appear_timeout)) &&
      fail "no release.yml run for $tag after ${appear_timeout}s" \
        "check the Actions tab, then run this script again"
    sleep 5
    waited=$((waited + 5))
  fi
done
echo "run $(gh run view "$run" --json url --jq .url)"

# A loop of our own rather than gh run watch, which redraws every step every
# few seconds: one line when it finishes is enough.
waited=0
while :; do
  state="$(gh run view "$run" --json status,conclusion --jq '.status + " " + .conclusion')"
  [[ "$state" == completed* ]] && break
  ((waited >= run_timeout)) &&
    fail "run $run is still ${state% *} after ${run_timeout}s" \
      "gh run watch $run, then run this script again"
  sleep 15
  waited=$((waited + 15))
done
conclusion="${state#completed }"
if [[ "$conclusion" != success ]]; then
  gh run view "$run" --log-failed | tail -n 40 >&2 || true
  fail "run $run ended with $conclusion" \
    "gh run view $run --log-failed; the tag is pushed, so a fix is released under a new tag"
fi
echo "run $run: success"

want="gh-dojo_${tag}_checksums.txt"
for p in "${platforms[@]}"; do
  ext=""
  if [[ "$p" == windows-* ]]; then
    ext=".exe"
  fi
  want+=$'\n'"gh-dojo_${tag}_${p}${ext}"
done
want="$(sort <<<"$want")"
got="$(gh release view "$tag" --json assets --jq '.assets[].name' | sort)"
[[ "$got" == "$want" ]] ||
  fail "the assets of release $tag are not the expected ones:"$'\n'"$(diff <(echo "$want") <(echo "$got") || true)" \
    "gh release view $tag"
echo "release $tag has its $(wc -l <<<"$want" | tr -d ' ') assets"

# release.yml publishes the draft last; immutable releases are a setting of
# the repository, so their absence only warns: the release is out already.
state="$(gh release view "$tag" --json isDraft,isImmutable --jq '"\(.isDraft) \(.isImmutable)"')"
[[ "$state" != true* ]] ||
  fail "release $tag is still a draft" \
    "gh run view $run --log; then gh release edit $tag --draft=false"
if [[ "$state" != *true ]]; then
  echo "warning: release $tag is not immutable; turn immutable releases on: gh api -X PUT repos/$repo/immutable-releases" >&2
fi

# --- 4. install ------------------------------------------------------------

# The release as users get it, through gh, not a build of the working copy.
# gh keeps one extension per name, so the dojo installed now (an earlier
# release, or make install's source build) is removed first. A prerelease
# is pinned, since gh otherwise installs the latest release.
step "gh extension install $repo ($tag)"

if [[ -n "$(gh extension list | awk -F'\t' '$1 == "gh dojo"')" ]]; then
  gh extension remove dojo
fi
if [[ "$tag" == *-* ]]; then
  gh extension install "$repo" --pin "$tag"
else
  gh extension install "$repo"
fi
installed="$(gh dojo --version)"
[[ "$installed" == "gh dojo version $tag" ]] ||
  fail "the installed gh dojo prints \"$installed\", not \"gh dojo version $tag\"" \
    "gh extension list; gh release view $tag, then run this script again"
echo "$installed"

# --- 5. Done ---------------------------------------------------------------

step "gh dojo release $tag --verb done"

code=0
gh dojo release "$tag" --verb "done" --comment "$tag" || code=$?
if [[ "$code" -ne 0 ]]; then
  echo >&2
  echo "$tag is released and installed; moving its issues to Done ended with exit code $code." >&2
  echo "Read the output above (CONTRIBUTING.md, Releases): exit 2 is a refusal for the user; on 4 or 5 run this script again." >&2
  exit "$code"
fi
