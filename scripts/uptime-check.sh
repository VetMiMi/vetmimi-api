#!/usr/bin/env bash
set -euo pipefail

# Checks the live site and API from outside, as a visitor would: four pages
# and both certificates. Prints one line per check and exits 1 if any failed.
# .github/workflows/uptime.yml runs it every 10 minutes.

site=${SITE_URL:?set SITE_URL, e.g. https://vetmimi.duckdns.org}
api=${API_URL:?set API_URL, e.g. https://api.vetmimi.duckdns.org}
site=${site%/}
api=${api%/}
status=0

report() {
  printf '%-4s %-16s %s\n' "$1" "$2" "$3"
  if [[ $1 == FAIL ]]; then
    status=1
  fi
}

# Three tries of 10 seconds each; --fail makes any answer from 400 up a failure.
check_page() {
  local code
  if code=$(curl -fs -o /dev/null -w '%{http_code}' --max-time 10 --retry 2 --retry-delay 5 --retry-all-errors "$2"); then
    report ok "$1" "HTTP $code"
  elif [[ $code == 000 ]]; then
    report FAIL "$1" "no answer"
  else
    report FAIL "$1" "HTTP $code"
  fi
}

check_certificate() {
  local host=${2#https://} certificate expiry
  host=${host%%/*}
  certificate=$(timeout 10 openssl s_client -connect "$host:443" -servername "$host" < /dev/null 2> /dev/null |
    openssl x509 2> /dev/null) || true
  if [[ -z $certificate ]]; then
    report FAIL "$1" "no certificate"
    return
  fi
  expiry=$(openssl x509 -noout -enddate <<< "$certificate")
  expiry="expires ${expiry#notAfter=}"
  if openssl x509 -noout -checkend $((14 * 24 * 3600)) <<< "$certificate" > /dev/null; then
    report ok "$1" "$expiry"
  else
    report FAIL "$1" "$expiry, within 14 days"
  fi
}

check_page site "$site/"
check_page site-my "$site/my"
check_page api-healthz "$api/healthz"
check_page api-readyz "$api/readyz"
check_certificate site-certificate "$site"
check_certificate api-certificate "$api"
exit $status
