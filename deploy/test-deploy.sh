#!/usr/bin/env bash
set -euo pipefail

# Runs deploy.sh against a throwaway clone with fake docker, curl and sleep
# commands on PATH; nothing here starts a container or reaches a network.

source_script=$(cd "$(dirname "$0")" && pwd)/deploy.sh
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
unset SSH_ORIGINAL_COMMAND

mkdir -p "$work/bin" "$work/origin/deploy/compose"
cat > "$work/bin/docker" << EOF
#!/bin/sh
echo "\$* [\$(tr '\n' ' ' < .env.tags 2>/dev/null)]" >> "$work/docker.log"
EOF
cat > "$work/bin/curl" << EOF
#!/bin/sh
for last; do :; done
echo "\$last" >> "$work/curl.log"
case \$(cat "$work/health") in
  ok) exit 0 ;;
  third) [ "\$(wc -l < "$work/curl.log")" -ge 3 ] ;;
  *) exit 22 ;;
esac
EOF
printf '#!/bin/sh\n' > "$work/bin/sleep"
chmod +x "$work/bin/"*
export PATH="$work/bin:$PATH"

git init --quiet -b main "$work/origin"
cp "$source_script" "$work/origin/deploy/"
touch "$work/origin/deploy/compose/docker-compose.yml"
git -C "$work/origin" add deploy
commit() { git -C "$work/origin" -c user.name=test -c user.email=test@example.com commit --quiet --allow-empty -m "$1"; }
commit first
first=$(git -C "$work/origin" rev-parse HEAD)
commit second
second=$(git -C "$work/origin" rev-parse HEAD)
git clone --quiet --single-branch --branch main "$work/origin" "$work/server"
git -C "$work/server" checkout --quiet --detach "$first"
compose_dir=$work/server/deploy/compose
deploy=$work/server/deploy/deploy.sh
web_sha=$(printf 'web' | git hash-object --stdin)

fail() { echo "FAIL: $1" >&2; exit 1; }
head_is() { [[ $(git -C "$work/server" rev-parse HEAD) == "$1" ]]; }
tags_are() { [[ $(cat "$compose_dir/.env.tags") == "$1" ]]; }
logged() { grep -qxF -- "$1" "$work/docker.log"; }
reset_logs() { rm -f "$work/docker.log" "$work/curl.log"; }
compose="compose --env-file .env --env-file .env.tags"

echo ok > "$work/health"
"$deploy" api "$first" > /dev/null 2>&1 && fail "deployed without the env files"
[[ ! -e $work/docker.log ]] || fail "called docker without the env files"

printf 'API_DOMAIN=api.test\nSITE_DOMAIN=site.test\n' > "$compose_dir/.env"
touch "$compose_dir/api.env"
for bad in "" "api" "api ${first:0:7}" "db $first" "api $first extra" "api $first;true" "web $(tr a-f A-F <<< "$first")"; do
  SSH_ORIGINAL_COMMAND=$bad "$deploy" > /dev/null 2>&1 && fail "accepted \"$bad\""
done
[[ ! -e $work/docker.log ]] || fail "called docker for a rejected command"

echo third > "$work/health"
SSH_ORIGINAL_COMMAND="api $second" "$deploy" > /dev/null || fail "a release healthy on the third poll failed"
head_is "$second" || fail "did not check out the api sha"
tags_are "API_TAG=$second" || fail "did not record the released api tag"
logged "$compose pull --quiet api worker [API_TAG=$second ]" || fail "did not pull api and worker"
logged "$compose up -d --wait --wait-timeout 120 api worker [API_TAG=$second ]" || fail "did not start api and worker"
logged "$compose exec -T caddy caddy reload --config /etc/caddy/Caddyfile [API_TAG=$second ]" || fail "did not reload caddy"
logged "image prune -f [API_TAG=$second ]" || fail "did not prune old images"
grep -qxF "https://api.test/healthz" "$work/curl.log" || fail "did not poll the api health url"

reset_logs
echo down > "$work/health"
"$deploy" web "$web_sha" > /dev/null 2>&1 && fail "an unhealthy first web release succeeded"
head_is "$second" || fail "a web release moved the checkout"
tags_are "API_TAG=$second" || fail "kept the tag of a failed first web release"
logged "$compose stop web [API_TAG=$second ]" || fail "did not stop a failed first web release"
grep -qxF "https://site.test/api/health" "$work/curl.log" || fail "did not poll the site health url"

reset_logs
echo ok > "$work/health"
"$deploy" web "$web_sha" > /dev/null || fail "a healthy web release failed"
tags_are "API_TAG=$second
WEB_TAG=$web_sha" || fail "did not record the web tag beside the api tag"

reset_logs
echo down > "$work/health"
"$deploy" api "$first" > /dev/null 2>&1 && fail "an unhealthy api release succeeded"
head_is "$second" || fail "did not check the previous api sha out again"
tags_are "WEB_TAG=$web_sha
API_TAG=$second" || fail "did not restore the previous api tag"
logged "$compose up -d --wait --wait-timeout 120 api worker [WEB_TAG=$web_sha API_TAG=$first ]" ||
  fail "did not start the new release"
logged "$compose up -d --wait --wait-timeout 120 api worker [WEB_TAG=$web_sha API_TAG=$second ]" ||
  fail "did not roll back to the previous release"
! grep -q 'image prune' "$work/docker.log" || fail "pruned images after a failed release"

echo "deploy.sh: all checks passed"
