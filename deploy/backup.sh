#!/usr/bin/env bash
set -euo pipefail

# Nightly from /etc/cron.d/vetmimi-backup (bootstrap.sh): a custom-format
# pg_dump, checked, uploaded to the backups bucket with the instance role,
# and kept on disk as the newest seven. Logs the object name and size only.

umask 077
cd "$(dirname "$0")/compose"
compose() { docker compose --env-file .env --env-file .env.tags "$@"; }

bucket=$(sed -n 's/^BACKUP_BUCKET=//p' .env)
if [[ -z $bucket ]]; then
  echo "backup: set BACKUP_BUCKET in deploy/compose/.env" >&2
  exit 1
fi

mkdir -p ../../backups
name="vetmimi-$(date -u +%Y%m%dT%H%M%SZ).dump"
file="../../backups/$name"
trap 'rm -f "$file.partial"' EXIT

# Dumped to disk and checked before upload, so a dump cut short by a failure
# never lands in the bucket looking like a good one.
compose exec -T postgres pg_dump --format=custom -U vetmimi vetmimi > "$file.partial"
compose exec -T postgres pg_restore --list < "$file.partial" > /dev/null
mv "$file.partial" "$file"
aws s3 cp --only-show-errors "$file" "s3://$bucket/postgres/$name"
echo "backup: uploaded postgres/$name ($(stat -c %s "$file") bytes)"

find ../../backups -name 'vetmimi-*.dump' | sort -r | tail -n +8 | xargs -r rm --
