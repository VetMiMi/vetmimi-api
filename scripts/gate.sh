#!/bin/sh
# Runs a heavy command (build, test, type-check) behind a machine-wide lock so
# that concurrent agent tracks — in this repository, its worktrees, and
# vetmimi-next — never compile or test at the same time. The owner's laptop
# has 8 GB of RAM; two Go test runs beside a Next.js build will swap it.
#
# Usage: scripts/gate.sh <command> [args...]
# A lock older than 30 minutes is treated as abandoned and removed.

set -eu

LOCK="${VETMIMI_GATE_LOCK:-/tmp/vetmimi-gate.lock}"
MAX_AGE_MINUTES=30

stale() {
  [ -d "$LOCK" ] && [ -n "$(find "$LOCK" -maxdepth 0 -mmin +"$MAX_AGE_MINUTES" 2>/dev/null)" ]
}

waited=0
until mkdir "$LOCK" 2>/dev/null; do
  if stale; then
    echo "gate: removing abandoned lock $LOCK" >&2
    rmdir "$LOCK" 2>/dev/null || true
    continue
  fi
  if [ "$waited" -eq 0 ]; then
    echo "gate: waiting for $LOCK (another track is building)" >&2
  fi
  waited=$((waited + 5))
  sleep 5
done
trap 'rmdir "$LOCK" 2>/dev/null || true' EXIT INT TERM HUP

exec "$@"
