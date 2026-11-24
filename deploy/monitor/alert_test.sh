#!/usr/bin/env bash
set -euo pipefail

# Runs alert.sh against the fixtures with a fake curl that serves them and
# records every call; nothing here reaches a network or sends an email. CI
# runs it in the monitor's Alpine image, under the service's memory limit.

here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

mkdir "$work/bin" "$work/state" "$work/backups"
cat > "$work/bin/curl" << EOF
#!/bin/sh
for url; do :; done
echo "\$*" >> "$work/calls"
case \$url in
  http://api:9090/debug/vars) cat "$work/api.json" 2> /dev/null ;;
  http://worker:9090/debug/vars) cat "$work/worker.json" 2> /dev/null ;;
  *) cat > "$work/email" ;;
esac
EOF
chmod +x "$work/bin/curl"
export PATH="$work/bin:$PATH" STATE_DIR="$work/state" BACKUP_DIR="$work/backups"
export ALERT_EMAIL=owner@example.com EMAIL_FROM="VetMiMi <hello@example.com>" RESEND_API_KEY=re_test

fail() { echo "FAIL: $1" >&2; exit 1; }
emails() { grep -cv '/debug/vars$' "$work/calls" || true; }
text() { jq -r .text "$work/email"; }
bump() {
  jq "$2" "$work/$1" > "$work/next.json"
  mv "$work/next.json" "$work/$1"
}
monitor() { "$here/alert.sh" > "$work/out" || fail "alert.sh exited non-zero: $(cat "$work/out")"; }

cp "$here/fixtures/api.json" "$here/fixtures/worker.json" "$work/"
touch "$work/backups/vetmimi-20261124T163000Z.dump"

monitor
monitor
[[ $(emails) == 0 ]] || fail "emailed although nothing changed"

bump api.json '.http_requests["/public/appointments 5xx"] += 6'
monitor
[[ $(emails) == 1 ]] || fail "a 5xx jump of 6 did not send exactly one email"
grep -q ' https://api.resend.com/emails$' "$work/calls" || fail "did not send through Resend"
text | grep -qx '5xx: 6 more since the last check' || fail "the email does not count 6 server errors: $(text)"
! text | grep -q '[/@]' || fail "the email body holds a path or an address: $(text)"

bump api.json '.http_requests["/public/appointments 5xx"] += 6'
monitor
[[ $(emails) == 1 ]] || fail "emailed the same check twice within the hour"

NOW=$(($(date +%s) + 27 * 3600)) "$here/alert.sh" > "$work/out"
[[ $(emails) == 2 ]] || fail "a backup 27 hours old did not send one alert"
text | grep -qx 'backup: newest dump is 27 hours old' || fail "the email does not name the old backup: $(text)"

rm "$work/worker.json"
monitor
monitor
[[ $(emails) == 3 ]] || fail "did not report unreachable counters exactly once"
text | grep -qx 'metrics: unavailable' || fail "the email does not say the metrics are unavailable: $(text)"

cp "$here/fixtures/worker.json" "$work/"
bump worker.json '.emails_failed += 1'
ALERT_EMAIL='' "$here/alert.sh" > "$work/out"
[[ $(emails) == 3 ]] || fail "emailed without ALERT_EMAIL"
grep -qx 'monitor: emails_failed: 1 more since the last check' "$work/out" || fail "did not log the alert: $(cat "$work/out")"

bump worker.json '.tasks_failed["comms:deliver"] += 3'
EMAIL_PROVIDER=smtp SMTP_HOST=smtp.example.com SMTP_USERNAME=hello@example.com SMTP_PASSWORD=app-password monitor
[[ $(emails) == 4 ]] || fail "the SMTP provider did not send one email"
grep -q -- '--ssl-reqd .*--mail-from hello@example.com --mail-rcpt owner@example.com .* smtp://smtp.example.com:587$' "$work/calls" ||
  fail "did not send through SMTP_HOST with TLS required: $(tail -n 1 "$work/calls")"
grep -qx 'Subject: VetMiMi alert: tasks_failed' "$work/email" || fail "the SMTP message lacks its subject: $(cat "$work/email")"

echo "alert.sh: all checks passed"
