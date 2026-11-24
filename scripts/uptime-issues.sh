#!/usr/bin/env bash
set -euo pipefail

# Reads uptime-check.sh's lines on stdin and keeps one issue per failing
# check: opens "Outage: <check>", comments while it keeps failing, and closes
# it on the next success. With DRY_RUN=1 the issue commands are printed instead.

now=$(date -u '+%Y-%m-%d %H:%M UTC')

show() {
  printf "'%s' " "$@"
  printf '\n'
}

run() {
  if [[ ${DRY_RUN:-} == 1 ]]; then
    show "$@"
    return
  fi
  "$@"
}

set_field() {
  GH_TOKEN=$PROJECT_TOKEN gh project item-edit --project-id PVT_kwDOEzlLUc4Bia-x --id "$1" \
    --field-id "$2" --single-select-option-id "$3"
}

add_to_board() {
  local item
  if [[ -z ${PROJECT_TOKEN:-} ]]; then
    echo "uptime: no PROJECT_TOKEN secret; set Area Launch, Priority High and Size XS on the board by hand"
    return
  fi
  item=$(GH_TOKEN=$PROJECT_TOKEN gh project item-add 1 --owner VetMiMi --url "$1" --format json --jq .id)
  set_field "$item" PVTSSF_lADOEzlLUc4Bia-xzhhTHPM fb5a83ac # Area: Launch
  set_field "$item" PVTSSF_lADOEzlLUc4Bia-xzhhTHPQ db241d04 # Priority: High
  set_field "$item" PVTSSF_lADOEzlLUc4Bia-xzhhTHPI dd20554c # Size: XS
}

open_issue() {
  local url issue=(gh issue create --title "Outage: $1"
    --label "type: chore" --label "area: infrastructure" --label "priority: high"
    --milestone "M5 — Launch" --body "$1 failed at $now: $2.")
  if [[ ${DRY_RUN:-} == 1 ]]; then
    show "${issue[@]}"
    return
  fi
  url=$("${issue[@]}")
  echo "uptime: opened $url"
  add_to_board "$url"
}

# One "<check> <number>" line per open outage issue.
open=$(gh issue list --state open --label "area: infrastructure" --limit 100 --json number,title \
  --jq '.[] | select(.title | startswith("Outage: ")) | "\(.title | ltrimstr("Outage: ")) \(.number)"')

while read -r result check status; do
  number=$(sed -n "s/^$check //p" <<< "$open")
  if [[ $result == ok && -n $number ]]; then
    run gh issue close "$number" --comment "Recovered at $now."
  elif [[ $result == FAIL && -n $number ]]; then
    run gh issue comment "$number" --body "Still failing at $now: $status."
  elif [[ $result == FAIL ]]; then
    open_issue "$check" "$status"
  fi
done
