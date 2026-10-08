#!/usr/bin/env bash
set -euo pipefail

# Releases one service on the live host: deploy.sh <api|web> <sha>.
# The image ghcr.io/vetmimi/vetmimi-<api|next>:<sha> must already be pushed.
# A release that does not answer its public health URL within a minute is
# rolled back to the previous tag, and the script exits 1.

compose() { docker compose --env-file .env --env-file .env.tags "$@"; }

tag_of() { sed -n "s/^$1=//p" .env.tags; }

set_tag() {
  grep -v "^$1=" .env.tags > .env.tags.new || true
  if [[ -n $2 ]]; then
    printf '%s=%s\n' "$1" "$2" >> .env.tags.new
  fi
  mv .env.tags.new .env.tags
}

# Called as an if condition, where set -e is off, so every step is chained.
start() {
  compose pull --quiet "$@" &&
    compose up -d --wait --wait-timeout 120 "$@" &&
    compose up -d --no-deps caddy coturn || return 1
  # A reload, never a restart, so open video WebSockets survive (Caddyfile).
  compose exec -T caddy caddy reload --config /etc/caddy/Caddyfile ||
    echo "deploy: caddy reload failed; Caddy keeps its running configuration" >&2
}

healthy() {
  local try
  for ((try = 0; try < 30; try++)); do
    if curl -fsS -o /dev/null --max-time 2 "$1"; then
      return 0
    fi
    sleep 2
  done
  return 1
}

# Wrapped in a function so bash has read the whole script before git checkout rewrites it.
main() {
  cd "$(dirname "$0")/.."

  # SECURITY CONTROL: the deploy key's forced command passes its SSH argument through here.
  local service="" sha="" extra=""
  read -r service sha extra <<< "${*:-${SSH_ORIGINAL_COMMAND:-}}" || true
  if [[ ! $service =~ ^(api|web)$ || ! $sha =~ ^[0-9a-f]{40}$ || -n $extra ]]; then
    echo "deploy: expected <api|web> and a full 40-character commit sha" >&2
    exit 1
  fi

  if [[ ! -f deploy/compose/.env || ! -f deploy/compose/api.env ]]; then
    echo "deploy: fill deploy/compose/.env and deploy/compose/api.env first (deploy/README.md)" >&2
    exit 1
  fi

  git fetch --quiet origin main
  if [[ $service == api ]]; then
    git checkout --quiet --detach "$sha"
  fi

  cd deploy/compose
  touch .env.tags
  local var url services
  if [[ $service == api ]]; then
    var=API_TAG
    services=(api worker)
    url="https://$(sed -n 's/^API_DOMAIN=//p' .env)/healthz"
  else
    var=WEB_TAG
    services=(web)
    url="https://$(sed -n 's/^SITE_DOMAIN=//p' .env)/api/health"
  fi
  local previous
  previous=$(tag_of "$var")
  echo "deploy: $service $sha (previous: ${previous:-none})"

  set_tag "$var" "$sha"
  if start "${services[@]}" && healthy "$url"; then
    docker image prune -f > /dev/null
    echo "deploy: $service $sha is live"
    return 0
  fi

  echo "deploy: $service $sha failed its health check" >&2
  if [[ -z $previous ]]; then
    set_tag "$var" ""
    compose stop "${services[@]}"
    echo "deploy: no previous $service release; it is stopped" >&2
    exit 1
  fi
  set_tag "$var" "$previous"
  if [[ $service == api && $previous =~ ^[0-9a-f]{40}$ ]]; then
    git checkout --quiet --detach "$previous"
  fi
  if start "${services[@]}" && healthy "$url"; then
    echo "deploy: rolled $service back to $previous" >&2
  else
    echo "deploy: rolled $service back to $previous, which is not healthy either" >&2
  fi
  exit 1
}

main "$@"
