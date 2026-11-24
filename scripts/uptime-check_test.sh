#!/usr/bin/env bash
set -euo pipefail

# Runs uptime-check.sh and uptime-issues.sh with fake curl, openssl, timeout
# and gh commands on PATH; nothing here reaches a network or GitHub. The
# certificates are real, made here, so the expiry check is the real one.

here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
openssl=$(command -v openssl)

mkdir "$work/bin"
cat > "$work/bin/curl" << EOF
#!/bin/sh
for url; do :; done
if [ "\${url##*/}" = readyz ] && [ -e "$work/readyz-down" ]; then
  printf 503
  exit 22
fi
printf 200
EOF
cat > "$work/bin/openssl" << EOF
#!/bin/sh
if [ "\$1" = s_client ]; then
  cat "$work/served.pem"
  exit
fi
exec "$openssl" "\$@"
EOF
cat > "$work/bin/timeout" << 'EOF'
#!/bin/sh
shift
exec "$@"
EOF
cat > "$work/bin/gh" << EOF
#!/bin/sh
echo "\$*" >> "$work/gh.log"
[ "\$1 \$2" != "issue list" ] || cat "$work/open-issues"
EOF
chmod +x "$work/bin/"*
export PATH="$work/bin:$PATH" SITE_URL=https://site.test API_URL=https://api.test

fail() { echo "FAIL: $1" >&2; exit 1; }
certificate() {
  "$openssl" req -x509 -newkey rsa:2048 -nodes -keyout /dev/null -subj /CN=site.test -days "$1" 2> /dev/null
}

certificate 90 > "$work/served.pem"
"$here/uptime-check.sh" > "$work/out" || fail "a healthy site and API failed: $(cat "$work/out")"
[[ $(wc -l < "$work/out") -eq 6 ]] || fail "did not print one line per check: $(cat "$work/out")"

touch "$work/readyz-down"
"$here/uptime-check.sh" > "$work/out" && fail "passed while /readyz answered 503"
grep -q '^FAIL api-readyz  *HTTP 503$' "$work/out" || fail "did not name readyz: $(cat "$work/out")"
[[ $(grep -c '^FAIL' "$work/out") -eq 1 ]] || fail "failed more than readyz: $(cat "$work/out")"

echo "site 41" > "$work/open-issues"
DRY_RUN=1 "$here/uptime-issues.sh" < "$work/out" > "$work/issues"
grep -qF "'gh' 'issue' 'create' '--title' 'Outage: api-readyz' '--label' 'type: chore' '--label' 'area: infrastructure' '--label' 'priority: high' '--milestone' 'M5 — Launch' '--body'" "$work/issues" ||
  fail "did not open the readyz issue with exactly the three labels and the milestone: $(cat "$work/issues")"
[[ $(grep -o "'--label'" "$work/issues" | wc -l) -eq 3 ]] || fail "used a label beyond the three: $(cat "$work/issues")"
grep -qF "'gh' 'issue' 'close' '41' '--comment' 'Recovered at " "$work/issues" || fail "did not close the recovered site issue"
! grep -qv '^issue list ' "$work/gh.log" || fail "a dry run ran an issue command: $(cat "$work/gh.log")"

rm "$work/readyz-down"
certificate 5 > "$work/served.pem"
"$here/uptime-check.sh" > "$work/out" && fail "passed with a certificate 5 days from expiry"
grep -q '^FAIL site-certificate .*within 14 days$' "$work/out" || fail "did not fail the certificate: $(cat "$work/out")"

echo "uptime-check.sh: all checks passed"
