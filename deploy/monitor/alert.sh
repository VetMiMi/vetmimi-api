#!/usr/bin/env bash
set -euo pipefail

# Every 5 minutes in the monitor service (compose/docker-compose.yml): compares
# the api's and the worker's counters with the last run and checks the newest
# backup's age. Alerts go to stdout and to ALERT_EMAIL, naming checks and
# counts only, at most once an hour per check.

state=${STATE_DIR:-/state}
backups=${BACKUP_DIR:-/backups}
# Seconds since 1970; tests set NOW to move the clock.
now=${NOW:-$(date +%s)}
alerts=""
checks=""
unavailable=0

raise() {
  echo "monitor: $1: $2"
  alerts+="$1: $2"$'\n'
  checks+=" $1"
}

# Raises an alert unless the same check raised one within the hour.
alert() {
  local last
  last=$(sed -n "s/^$1 //p" "$state/alerted")
  if [[ -n $last ]] && ((now - last < 3600)); then
    echo "monitor: $1: $2 (alerted within the hour)"
    return
  fi
  raise "$1" "$2"
  grep -v "^$1 " "$state/alerted" > "$state/alerted.new" || true
  echo "$1 $now" >> "$state/alerted.new"
  mv "$state/alerted.new" "$state/alerted"
}

# A counter below its last value means the process restarted from zero.
check_counter() {
  local name=$1 vars=$2 filter=$3 threshold=$4 previous current rise
  previous=$(sed -n "s/^$name //p" "$state/counters")
  if ! current=$(jq -e "($filter) | numbers" <<< "$vars" 2> /dev/null); then
    unavailable=1
    if [[ -n $previous ]]; then
      echo "$name $previous" >> "$state/counters.new"
    fi
    return
  fi
  echo "$name $current" >> "$state/counters.new"
  if [[ -z $previous ]]; then
    return
  fi
  rise=$((current - previous))
  if ((current < previous)); then
    rise=$current
  fi
  if ((rise >= threshold)); then
    alert "$name" "$rise more since the last check"
  fi
}

# Sends with the API's own mail settings from api.env.
send() {
  local from=${EMAIL_FROM:-} address
  if [[ ${EMAIL_PROVIDER:-resend} == smtp ]]; then
    address=${from##*<}
    address=${address%>}
    printf 'From: %s\nTo: %s\nSubject: %s\nDate: %s\n\n%s' "$from" "$ALERT_EMAIL" "$1" "$(date -R)" "$2" |
      curl -fsS --max-time 30 --ssl-reqd --crlf --user "${SMTP_USERNAME:-}:${SMTP_PASSWORD:-}" \
        --mail-from "$address" --mail-rcpt "$ALERT_EMAIL" --upload-file - "smtp://${SMTP_HOST:-}:${SMTP_PORT:-587}"
    return
  fi
  jq -n --arg from "$from" --arg to "$ALERT_EMAIL" --arg subject "$1" --arg text "$2" \
    '{from: $from, to: [$to], subject: $subject, text: $text}' |
    curl -fsS --max-time 30 -o /dev/null -H "Authorization: Bearer ${RESEND_API_KEY:-}" \
      -H "Content-Type: application/json" --data-binary @- https://api.resend.com/emails
}

touch "$state/counters" "$state/alerted"
: > "$state/counters.new"

api=$(curl -fs --max-time 5 --retry 3 --retry-delay 5 --retry-all-errors http://api:9090/debug/vars) || true
worker=$(curl -fs --max-time 5 --retry 3 --retry-delay 5 --retry-all-errors http://worker:9090/debug/vars) || true
check_counter 5xx "$api" '[.http_requests | to_entries[] | select(.key | endswith(" 5xx")) | .value] | add // 0' 5
check_counter tasks_failed "$worker" '[.tasks_failed[]] | add // 0' 3
check_counter emails_failed "$worker" '.emails_failed' 1
mv "$state/counters.new" "$state/counters"

# Raised once until the counters return, not hourly: a release without them would mail every hour.
if ((unavailable == 0)); then
  rm -f "$state/metrics-unavailable"
elif [[ -e $state/metrics-unavailable ]]; then
  echo "monitor: metrics: still unavailable"
else
  touch "$state/metrics-unavailable"
  raise metrics "unavailable"
fi

newest=0
for dump in "$backups"/vetmimi-*.dump; do
  [[ -e $dump ]] || continue
  modified=$(date -r "$dump" +%s)
  if ((modified > newest)); then
    newest=$modified
  fi
done
if ((newest == 0)); then
  alert backup "no dump found"
elif ((now - newest > 26 * 3600)); then
  alert backup "newest dump is $(((now - newest) / 3600)) hours old"
fi

if [[ -z $alerts ]]; then
  echo "monitor: no new alerts"
  exit 0
fi
if [[ -z ${ALERT_EMAIL:-} ]]; then
  echo "monitor: ALERT_EMAIL is empty, so the alert is only logged"
  exit 0
fi
text="The VetMiMi live host raised these alerts at $(date -u '+%Y-%m-%d %H:%M UTC'):

${alerts}
The Monitoring section of the deploy README says what to look at first."
if ! send "VetMiMi alert:$checks" "$text"; then
  echo "monitor: the alert email could not be sent"
fi
