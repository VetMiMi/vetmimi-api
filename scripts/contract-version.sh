#!/usr/bin/env bash
# Keeps openapi.yaml's info.version honest so vetmimi-next can pin the contract
# by tag (ADR-003). Runs on ubuntu-latest and on macOS's bash 3.2, and needs
# only git and awk: no yq, no sort -V.
#
# Usage:
#   scripts/contract-version.sh version [file]
#       Print info.version from file (default openapi.yaml).
#   scripts/contract-version.sh check <base-ref> [head-ref]
#       Fail when head-ref (default HEAD) changes openapi.yaml without raising
#       info.version above the version on base-ref.
#   scripts/contract-version.sh tag <remote> <commit>
#       Push tag v<info.version> at commit, unless the remote already has it.

set -euo pipefail

SPEC=openapi.yaml

die() {
  echo "contract-version: $*" >&2
  exit 1
}

# Prints info.version from the YAML on stdin. Only a direct child of the
# top-level info: mapping counts, so "version:" inside a description or a
# nested mapping is ignored. awk reads to the end of its input instead of
# exiting at the match: under pipefail an early exit would kill `git show`
# with SIGPIPE on a large spec.
read_version() {
  awk -v q="'" '
    { sub(/\r$/, "") }
    found { next }
    /^info:[ \t]*(#.*)?$/ { in_info = 1; next }
    !in_info { next }
    /^[ \t]*(#.*)?$/ { next }
    /^[^ \t]/ { in_info = 0; next }
    {
      match($0, /^ +/)
      lead = substr($0, 1, RLENGTH)
      if (indent == "") indent = lead
      if (lead != indent || $0 !~ ("^" indent "version:")) next
      v = $0
      sub(/^ *version:[ \t]*/, "", v)
      sub(/[ \t]+#.*$/, "", v)
      sub(/[ \t]+$/, "", v)
      first = substr(v, 1, 1)
      if (length(v) >= 2 && (first == "\"" || first == q) && substr(v, length(v), 1) == first)
        v = substr(v, 2, length(v) - 2)
      print v
      found = 1
    }
  '
}

is_semver() {
  printf '%s\n' "$1" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'
}

# Reads info.version of the spec at a git revision and insists it is
# MAJOR.MINOR.PATCH; leading zeros are rejected, which also keeps the numeric
# comparison below clear of octal.
version_at() {
  local version
  version=$(git show "$1:$SPEC" | read_version) || die "could not read $SPEC at $1"
  [ -n "$version" ] || die "$1: $SPEC has no info.version"
  is_semver "$version" || die "$1: info.version \"$version\" is not MAJOR.MINOR.PATCH"
  printf '%s\n' "$version"
}

# Succeeds when version $1 is greater than version $2, part by part as
# numbers, so 0.10.0 is above 0.9.0.
version_gt() {
  local a1 a2 a3 b1 b2 b3
  IFS=. read -r a1 a2 a3 <<<"$1"
  IFS=. read -r b1 b2 b3 <<<"$2"
  if [ "$a1" -ne "$b1" ]; then [ "$a1" -gt "$b1" ]; return; fi
  if [ "$a2" -ne "$b2" ]; then [ "$a2" -gt "$b2" ]; return; fi
  [ "$a3" -gt "$b3" ]
}

# Did head change the spec since it left base? Measured from the merge base so
# that changes landed on base meanwhile do not count against head.
spec_changed() {
  local fork
  fork=$(git merge-base "$1" "$2") || die "$1 and $2 share no history"
  ! git diff --quiet "$fork" "$2" -- "$SPEC"
}

check() {
  local base=$1 head=${2:-HEAD} old new
  git rev-parse --verify --quiet "$base^{commit}" >/dev/null || die "unknown base ref $base"
  git rev-parse --verify --quiet "$head^{commit}" >/dev/null || die "unknown head ref $head"

  if ! spec_changed "$base" "$head"; then
    echo "$SPEC unchanged; no version bump needed."
    return 0
  fi
  git cat-file -e "$head:$SPEC" 2>/dev/null || die "$head deletes $SPEC"
  new=$(version_at "$head")
  if ! git cat-file -e "$base:$SPEC" 2>/dev/null; then
    echo "$SPEC is new, at info.version $new."
    return 0
  fi
  old=$(version_at "$base")

  if [ "$new" = "$old" ]; then
    die "$SPEC changed but info.version is still $old. Raise it: minor for a breaking change while pre-1.0, patch otherwise."
  fi
  version_gt "$new" "$old" || die "info.version went down from $old to $new; it must rise above $old."
  echo "$SPEC changed; info.version $old -> $new."
}

# Exit status of `git ls-remote --exit-code`: 0 found, 2 not found, anything
# else is a failure to reach the remote, which must not read as "absent".
remote_has_tag() {
  local rc=0
  git ls-remote --exit-code --tags "$1" "refs/tags/$2" >/dev/null || rc=$?
  case $rc in
    0) return 0 ;;
    2) return 1 ;;
    *) die "could not list tags on $1" ;;
  esac
}

tag() {
  local remote=$1 commit=$2 name
  name="v$(version_at "$commit")"
  if remote_has_tag "$remote" "$name"; then
    echo "Tag $name already exists on $remote; nothing to do."
    return 0
  fi
  # Two runs on main can race for the same version; the loser finds the tag
  # there and has nothing left to do.
  if ! git push --quiet "$remote" "$commit:refs/tags/$name"; then
    remote_has_tag "$remote" "$name" || die "could not push $name to $remote"
    echo "Tag $name appeared on $remote meanwhile; nothing to do."
    return 0
  fi
  echo "Tagged $name at $(git rev-parse --short "$commit")."
}

case "${1:-}" in
  version)
    file=${2:-$SPEC}
    [ -f "$file" ] || die "no such file $file"
    version=$(read_version <"$file")
    [ -n "$version" ] || die "$file has no info.version"
    printf '%s\n' "$version"
    ;;
  check)
    [ $# -ge 2 ] && [ $# -le 3 ] || die "usage: $0 check <base-ref> [head-ref]"
    check "$2" "${3:-HEAD}"
    ;;
  tag)
    [ $# -eq 3 ] || die "usage: $0 tag <remote> <commit>"
    tag "$2" "$3"
    ;;
  *)
    die "usage: $0 version [file] | check <base-ref> [head-ref] | tag <remote> <commit>"
    ;;
esac
