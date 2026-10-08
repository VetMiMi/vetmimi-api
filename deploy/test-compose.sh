#!/usr/bin/env bash
set -euo pipefail

# Guards on the live stack's configuration, run in CI:
# only Caddy publishes ports and only coturn shares the host's network, and
# turnserver.conf keeps relays off private networks and holds no secret.
# docker compose config only parses files; it starts nothing.

here=$(cd "$(dirname "$0")/compose" && pwd)
status=0
fail() { echo "FAIL: $1" >&2; status=1; }

conf=$here/coturn/turnserver.conf
for line in \
  denied-peer-ip=0.0.0.0-0.255.255.255 \
  denied-peer-ip=10.0.0.0-10.255.255.255 \
  denied-peer-ip=100.64.0.0-100.127.255.255 \
  denied-peer-ip=127.0.0.0-127.255.255.255 \
  denied-peer-ip=169.254.0.0-169.254.255.255 \
  denied-peer-ip=172.16.0.0-172.31.255.255 \
  denied-peer-ip=192.168.0.0-192.168.255.255 \
  denied-peer-ip=::1 \
  denied-peer-ip=fc00::-fdff:ffff:ffff:ffff:ffff:ffff:ffff:ffff \
  denied-peer-ip=fe80::-febf:ffff:ffff:ffff:ffff:ffff:ffff:ffff \
  no-multicast-peers no-cli use-auth-secret; do
  grep -qxF "$line" "$conf" || fail "turnserver.conf lacks $line"
done
if grep -Eq '^[[:space:]]*(static-auth-secret|user=|lt-cred-mech)' "$conf"; then
  fail "turnserver.conf holds a secret or enables static users"
fi

if [[ ${1:-} != --conf-only ]]; then
  work=$(mktemp -d)
  trap 'rm -rf "$work"' EXIT
  cp -R "$here/." "$work/"
  sed 's/=$/=placeholder/' "$here/.env.example" > "$work/.env"
  touch "$work/api.env" "$work/.env.tags"
  config=$(docker compose -f "$work/docker-compose.yml" --env-file "$work/.env" config --format json)

  published=$(jq -r '[.services | to_entries[] | select((.value.ports // []) | length > 0) | .key] | join(" ")' <<< "$config")
  [[ $published == caddy ]] || fail "services publishing ports: '$published', want only caddy"
  host=$(jq -r '[.services | to_entries[] | select(.value.network_mode == "host") | .key] | join(" ")' <<< "$config")
  [[ $host == coturn ]] || fail "services on the host network: '$host', want only coturn"
fi

[[ $status == 0 ]] && echo "test-compose.sh: all checks passed"
exit $status
