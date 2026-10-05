#!/bin/sh
# Runs a heavy command (build, test, type-check) behind a machine-wide lock so
# that concurrent agent tracks — in this repository, its worktrees, and the
# sibling VetMiMi repository — never compile or test at the same time. The
# owner's laptop has 8 GB of RAM; two Go test runs beside a Next.js build will
# swap it.
#
# Usage: scripts/gate.sh <command> [args...]
#
# The lock is a directory holding the holder's PID. A lock whose PID is no
# longer running is abandoned and taken over, so a killed run never blocks the
# next one. The command runs as a child rather than through exec, because exec
# would discard the trap that releases the lock.

set -eu

LOCK="${VETMIMI_GATE_LOCK:-/tmp/vetmimi-gate.lock}"

holder_alive() {
  pid=$(cat "$LOCK/pid" 2>/dev/null || true)
  [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null
}

announced=0
until mkdir "$LOCK" 2>/dev/null; do
  if ! holder_alive; then
    # Give a holder that has just created the directory time to write its PID.
    sleep 1
    if ! holder_alive; then
      echo "gate: taking over abandoned lock $LOCK" >&2
      rm -rf "$LOCK"
      continue
    fi
  fi
  if [ "$announced" -eq 0 ]; then
    echo "gate: waiting for $LOCK (held by PID $(cat "$LOCK/pid" 2>/dev/null))" >&2
    announced=1
  fi
  sleep 3
done
echo $$ >"$LOCK/pid"
trap 'rm -rf "$LOCK"' EXIT
trap 'exit 130' INT TERM HUP

"$@"
