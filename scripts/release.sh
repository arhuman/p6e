#!/usr/bin/env bash
# release.sh: cut a release with minimal ceremony.
#
# Derives the next semantic version from the Conventional Commits since the last
# v* tag (feat -> minor, fix/other -> patch, ! or BREAKING CHANGE -> major,
# capped to minor while on 0.x), lets the caller confirm or override it, runs
# `make ci`, then tags and pushes. Pushing the tag is what triggers
# .github/workflows/release.yml, so nobody types a version by hand unless they
# mean to.
#
# Three deviations from the standard template, each forced by this repo:
#
#   1. It resolves the commit to tag through jj, not `git rev-parse HEAD`. This
#      repo is jj colocated, where git HEAD tracks the *parent* of the working
#      copy. Tagging git HEAD would silently ship one commit behind.
#   2. It does not check the git branch. Under jj, git HEAD is detached and
#      `git rev-parse --abbrev-ref HEAD` always answers "HEAD", so that check
#      can only ever fail. What is checked instead is that the commit being
#      tagged is described and non-empty.
#   3. It stamps CHANGELOG.md and commits through jj, not git. `jj commit`
#      describes the working copy and opens a fresh change, which is the jj
#      equivalent of `git commit`; the tag then points at that release commit.
#      Note the root CHANGELOG.md is a different document from
#      .claude/CHANGELOG.md: the root one is succinct and public, the private one
#      is the author's decision log and is never promoted into it.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

die() { echo "release: $*" >&2; exit 1; }

command -v jj >/dev/null || die "jj not found; this repo is jj colocated"

# The working copy must be an empty change. jj's working copy is itself a
# commit, so "clean" means empty rather than unmodified: if @ already held
# described work, stamping the changelog into it and then running `jj commit`
# would overwrite that commit's own message with "chore(release)".
jj_empty() { jj log -r "$1" --no-graph -T 'if(empty, "yes", "no")'; }
[ "$(jj_empty @)" = yes ] || \
  die "working copy has changes; describe them and run 'jj new' before releasing"

# --- what are we tagging -----------------------------------------------------
# The newest ancestor of the working copy that carries a description. jj's
# working copy is itself a commit and is often an empty scratch change, so
# "the tip" is not always what should be released.
target=$(jj log -r 'heads(::@ & ~empty() & ~description(exact:""))' \
  --no-graph -T 'commit_id ++ "\n"' 2>/dev/null | head -n1)
[ -n "$target" ] || die "no described, non-empty commit to release"

subject=$(jj log -r "$target" --no-graph -T 'description.first_line()')
if jj log -r "$target" --no-graph -T 'if(conflict, "yes", "")' | grep -q yes; then
  die "commit $subject has conflicts"
fi

git remote get-url origin >/dev/null 2>&1 || \
  die "no 'origin' remote; a release publishes to GitHub and there is nowhere to push"

# --- last tag + bump detection ----------------------------------------------
last=$(git tag --list 'v*' --sort=-v:refname | head -n1)
if [ -z "$last" ]; then
  last="v0.0.0"; range="$target"
else
  range="${last}..${target}"
fi
IFS=. read -r major minor patch <<<"${last#v}"

subjects=$(git log "$range" --no-merges --format='%s' 2>/dev/null || true)
[ -n "$subjects" ] || die "no commits since $last; nothing to release"
bodies=$(git log "$range" --no-merges --format='%B' 2>/dev/null || true)

bump=patch
if printf '%s\n' "$subjects" | grep -qE '^[a-z]+(\([^)]+\))?!:' \
   || printf '%s\n' "$bodies" | grep -qE '^BREAKING CHANGE'; then
  bump=major
elif printf '%s\n' "$subjects" | grep -qE '^feat(\([^)]+\))?:'; then
  bump=minor
fi
# SemVer 0.x: a breaking change bumps minor, not major, until the first 1.0.0.
[ "$major" -eq 0 ] && [ "$bump" = major ] && bump=minor

case "$bump" in
  major) major=$((major + 1)); minor=0; patch=0 ;;
  minor) minor=$((minor + 1)); patch=0 ;;
  patch) patch=$((patch + 1)) ;;
esac
suggested="v${major}.${minor}.${patch}"

# --- confirm / override ------------------------------------------------------
n_all=$(printf '%s\n' "$subjects" | grep -c . || true)
n_feat=$(printf '%s\n' "$subjects" | grep -cE '^feat' || true)
n_fix=$(printf '%s\n' "$subjects" | grep -cE '^fix' || true)
echo "Tagging        : ${target:0:12}  $subject"
echo "Last tag       : $last"
echo "Commits since  : $n_all ($n_feat feat, $n_fix fix)  ->  bump = $bump"
printf 'Version [%s]: ' "$suggested"
read -r chosen </dev/tty || true
version=${chosen:-$suggested}
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "invalid version '$version' (want vMAJOR.MINOR.PATCH)"
git rev-parse "$version" >/dev/null 2>&1 && die "tag $version already exists"

# --- gate --------------------------------------------------------------------
echo "Running make ci ..."
make ci

# `tidy` runs go fmt and go mod tidy, either of which can rewrite a file. The
# working copy was empty a moment ago, so anything in it now came from the gate,
# and tagging would point at code that never passed the gate that just ran.
[ "$(jj_empty @)" = yes ] || \
  die "the gate modified files (fmt or tidy?); commit them and re-run"

# --- stamp the changelog -----------------------------------------------------
# Promote [Unreleased]: keep an empty one on top and open a dated version
# heading beneath it, over the accumulated entries.
changelog="CHANGELOG.md"
[ -f "$changelog" ] || die "$changelog not found"

today=$(date +%F)
tmp=$(mktemp)
awk -v ver="${version#v}" -v date="$today" '
  !stamped && /^## \[Unreleased\]/ {
    print "## [Unreleased]"; print "";
    print "## [" ver "] - " date;
    stamped = 1; next
  }
  { print }
' "$changelog" > "$tmp" && mv "$tmp" "$changelog"
grep -qF "## [${version#v}] - $today" "$changelog" || \
  die "failed to stamp $changelog (no '## [Unreleased]' heading?)"

# --- commit, tag, push -------------------------------------------------------
# jj commit describes the working copy (which now holds only the stamp) and
# opens a fresh change on top. The tag goes on that release commit, so what is
# tagged is exactly what was gated plus the changelog entry describing it.
jj commit -m "chore(release): $version"
release_commit=$(jj log -r @- --no-graph -T 'commit_id')

# Export so the git side of the colocated repo knows the commit before a git
# tag points at it. jj has no tag-creating command, so the tag is made with git.
jj git export
git tag -a "$version" -m "$version" "$release_commit"
git push origin "$version"

echo "Pushed $version. .github/workflows/release.yml is now building."
