#!/usr/bin/env bash
# Tests scripts/contract-version.sh against throwaway git repositories in a
# temporary directory, so this repository's own history is never touched.
# Usage: scripts/contract-version_test.sh

set -euo pipefail

script="$(cd "$(dirname "$0")" && pwd)/contract-version.sh"
real_spec="$(cd "$(dirname "$0")/.." && pwd)/openapi.yaml"

tmp=$(mktemp -d "${TMPDIR:-/tmp}/contract-version-test.XXXXXX")
trap 'rm -rf "$tmp"' EXIT

# Keep the developer's git configuration (signing, hooks, default branch) out
# of the throwaway repositories, and stop git from climbing into a parent repo.
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null
export GIT_CEILING_DIRECTORIES="$tmp"
export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.com
export GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.com

failures=0
pass() { echo "ok   $1"; }
fail() {
  echo "FAIL $1"
  failures=$((failures + 1))
}

# spec <version> [extra line]: an openapi.yaml whose info.version is <version>.
spec() {
  cat <<EOF
openapi: "3.1.0"
info:
  title: Test
  version: "$1"
  description: >-
    Mentions version: 9.9.9 in prose, which must not count.
paths: {}
${2:-}
EOF
}

# new_repo <name> <base version>: a repository with main holding the spec and a
# branch "pr" checked out from it.
new_repo() {
  local dir="$tmp/$1"
  git init --quiet -b main "$dir"
  cd "$dir"
  spec "$2" >openapi.yaml
  echo "readme" >README.md
  git add . && git commit --quiet -m "chore: base"
  git checkout --quiet -b pr
}

commit_all() {
  git add -A && git commit --quiet -m "$1"
}

# expect_check <name> pass|fail [pattern]: run the check from branch pr against
# main; when failing, the message must match pattern.
expect_check() {
  local name=$1 want=$2 pattern=${3:-} out rc=0
  out=$("$script" check main 2>&1) || rc=$?
  if [ "$want" = pass ] && [ $rc -eq 0 ]; then
    pass "$name"
  elif [ "$want" = fail ] && [ $rc -ne 0 ] && printf '%s' "$out" | grep -q -- "$pattern"; then
    pass "$name"
  else
    fail "$name (want $want, exit $rc): $out"
  fi
}

# --- check ----------------------------------------------------------------

new_repo spec_unchanged 0.1.0
echo "more" >>README.md && commit_all "docs: readme"
expect_check spec_unchanged pass

new_repo spec_changed_version_raised 0.1.0
spec 0.1.1 "x-note: patch" >openapi.yaml && commit_all "feat: patch"
expect_check spec_changed_version_raised pass

new_repo spec_changed_minor_raised 0.1.3
spec 0.2.0 "x-note: breaking" >openapi.yaml && commit_all "feat: break"
expect_check spec_changed_minor_raised pass

new_repo spec_changed_version_same 0.1.0
spec 0.1.0 "x-note: change" >openapi.yaml && commit_all "feat: change"
expect_check spec_changed_version_same fail "still 0.1.0"

new_repo version_lowered 0.2.0
spec 0.1.9 >openapi.yaml && commit_all "feat: lower"
expect_check version_lowered fail "went down from 0.2.0 to 0.1.9"

new_repo multi_digit_raised 0.9.0
spec 0.10.0 >openapi.yaml && commit_all "feat: ten"
expect_check multi_digit_raised pass

new_repo multi_digit_lowered 0.10.0
spec 0.9.0 >openapi.yaml && commit_all "feat: nine"
expect_check multi_digit_lowered fail "went down from 0.10.0 to 0.9.0"

new_repo major_raised 0.12.4
spec 1.0.0 >openapi.yaml && commit_all "feat: one"
expect_check major_raised pass

new_repo version_not_semver 0.1.0
spec 0.2 >openapi.yaml && commit_all "feat: bad"
expect_check version_not_semver fail "is not MAJOR.MINOR.PATCH"

new_repo version_leading_zero 0.1.0
spec 0.08.0 >openapi.yaml && commit_all "feat: octal"
expect_check version_leading_zero fail "is not MAJOR.MINOR.PATCH"

new_repo spec_deleted 0.1.0
git rm --quiet openapi.yaml && commit_all "chore: drop"
expect_check spec_deleted fail "deletes openapi.yaml"

# main moved on (another spec change and bump) after the branch left it: the
# branch's own change must still rise above main's new version.
new_repo base_moved_on 0.1.0
spec 0.1.1 "x-note: mine" >openapi.yaml && commit_all "feat: mine"
git checkout --quiet main
spec 0.1.1 "x-note: theirs" >openapi.yaml && commit_all "feat: theirs"
git checkout --quiet pr
expect_check base_moved_on fail "still 0.1.1"

# The same, but the branch never touched the spec: main's changes are not its.
new_repo base_moved_on_spec_untouched 0.1.0
echo "more" >>README.md && commit_all "docs: readme"
git checkout --quiet main
spec 0.2.0 >openapi.yaml && commit_all "feat: theirs"
git checkout --quiet pr
expect_check base_moved_on_spec_untouched pass

# The real spec is large enough that `git show | awk` would die of SIGPIPE if
# awk stopped reading at the match.
new_repo real_spec_raised 0.1.0
git checkout --quiet main && cp "$real_spec" openapi.yaml && commit_all "feat: real"
git checkout --quiet pr && git reset --quiet --hard main
real_version=$("$script" version openapi.yaml)
sed "s/^  version: \"$real_version\"/  version: \"99.0.0\"/" "$real_spec" >openapi.yaml
commit_all "feat: bump real"
expect_check real_spec_raised pass

# --- version ----------------------------------------------------------------

expect_version() {
  local name=$1 want=$2 got
  got=$("$script" version "$tmp/v.yaml" 2>&1) || true
  if [ "$got" = "$want" ]; then pass "$name"; else fail "$name: want '$want', got '$got'"; fi
}

printf 'openapi: 3.1.0\ninfo:\n  title: T\n  version: 1.2.3 # comment\n' >"$tmp/v.yaml"
expect_version version_unquoted_with_comment 1.2.3

printf "info:\n    version: '4.5.6'\npaths: {}\n" >"$tmp/v.yaml"
expect_version version_single_quoted_four_spaces 4.5.6

printf 'info:\r\n  version: "7.8.9"\r\n' >"$tmp/v.yaml"
expect_version version_crlf 7.8.9

printf 'info:\n  # version: 0.0.1\n  contact:\n    version: 3.3.3\n  version: "2.0.0"\n' >"$tmp/v.yaml"
expect_version version_ignores_comment_and_nested 2.0.0

printf 'info:\n  title: T\nx-other:\n  version: 5.0.0\n' >"$tmp/v.yaml"
expect_version version_outside_info_ignored "contract-version: $tmp/v.yaml has no info.version"

expect_version_real() {
  local got
  got=$("$script" version "$real_spec")
  if is_real=$(printf '%s\n' "$got" | grep -E '^[0-9]+\.[0-9]+\.[0-9]+$'); then
    pass "version_of_this_repository_spec ($is_real)"
  else
    fail "version_of_this_repository_spec: got '$got'"
  fi
}
expect_version_real

# --- tag --------------------------------------------------------------------

git init --quiet --bare "$tmp/remote.git"
new_repo tagging 0.3.0
git checkout --quiet main
git remote add origin "$tmp/remote.git"
git push --quiet origin main
first=$(git rev-parse HEAD)

out=$("$script" tag origin "$first" 2>&1) || true
if [ "$(git ls-remote --tags origin refs/tags/v0.3.0 | cut -f1)" = "$first" ]; then
  pass "tag_created_on_commit"
else
  fail "tag_created_on_commit: $out"
fi

# A later commit at the same version leaves the tag where it is.
echo "more" >>README.md && commit_all "docs: readme"
out=$("$script" tag origin HEAD 2>&1) || true
if [ "$(git ls-remote --tags origin refs/tags/v0.3.0 | cut -f1)" = "$first" ] &&
  printf '%s' "$out" | grep -q "already exists"; then
  pass "tag_existing_skipped"
else
  fail "tag_existing_skipped: $out"
fi

# A raised version tags the new commit.
spec 0.3.1 >openapi.yaml && commit_all "feat: patch"
second=$(git rev-parse HEAD)
out=$("$script" tag origin "$second" 2>&1) || true
if [ "$(git ls-remote --tags origin refs/tags/v0.3.1 | cut -f1)" = "$second" ]; then
  pass "tag_new_version"
else
  fail "tag_new_version: $out"
fi

rc=0
out=$("$script" tag "$tmp/no-such-remote.git" HEAD 2>&1) || rc=$?
if [ $rc -ne 0 ] && printf '%s' "$out" | grep -q "could not list tags"; then
  pass "tag_unreachable_remote_fails"
else
  fail "tag_unreachable_remote_fails (exit $rc): $out"
fi

echo
if [ "$failures" -ne 0 ]; then
  echo "$failures contract-version test(s) failed"
  exit 1
fi
echo "all contract-version tests passed"
