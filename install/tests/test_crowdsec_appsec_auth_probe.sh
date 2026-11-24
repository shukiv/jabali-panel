#!/usr/bin/env bash
# check_crowdsec_appsec_auth warns when CrowdSec AppSec answers the nginx
# bouncer's key with 401 (the WAF then inspects nothing while pages load), stays
# quiet on 200, notes a silent :7422 without failing, and skips a box whose
# bouncer has no AppSec. The key goes to curl on stdin, never on its argv.
#
# Run from repo root:
#     bash install/tests/test_crowdsec_appsec_auth_probe.sh
set -uo pipefail

INSTALL_SH="${INSTALL_SH:-install.sh}"
fail=0
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

body=$(awk '$0 == "check_crowdsec_appsec_auth() {", /^\}$/' "$INSTALL_SH")
if [[ -z "$body" ]]; then
  echo "FAIL: check_crowdsec_appsec_auth not defined in $INSTALL_SH"
  exit 1
fi
body="${body//\/etc\/crowdsec\/bouncers\/crowdsec-nginx-bouncer.conf/$tmp/bouncer.conf}"

# Fake curl: records argv and stdin, prints the HTTP code in $tmp/code.
mkdir -p "$tmp/bin"
cat >"$tmp/bin/curl" <<EOF
#!/usr/bin/env bash
echo "\$*" >>"$tmp/argv"
cat >>"$tmp/stdin"
printf '%s' "\$(cat "$tmp/code")"
[[ "\$(cat "$tmp/code")" == 000 ]] && exit 7
exit 0
EOF
chmod 0755 "$tmp/bin/curl"

KEY="testkeyvalue"

# run <code> <conf body>: prints the _warn/_log lines.
run() {
  rm -f "$tmp/argv" "$tmp/stdin"
  printf '%s' "$1" >"$tmp/code"
  printf '%s\n' "$2" >"$tmp/bouncer.conf"
  (
    set -euo pipefail
    PATH="$tmp/bin:$PATH"
    _log() { echo "LOG: $*"; }; _warn() { echo "WARN: $*"; }; sleep() { :; }
    eval "$body"
    check_crowdsec_appsec_auth
    echo "rc=$?"
  )
}

CONF=$'API_KEY='"$KEY"$'\nAPPSEC_URL=http://127.0.0.1:7422'

out=$(run 401 "$CONF")
[[ "$out" == *"WARN: CrowdSec AppSec refuses the nginx bouncer's key (401)"* && "$out" == *"rc=0"* ]] \
  || { echo "FAIL: 401 must warn and return 0: $out"; fail=1; }
grep -q "$KEY" "$tmp/argv" && { echo "FAIL: the key is on curl's command line"; fail=1; }
grep -q "X-Crowdsec-Appsec-Api-Key: $KEY" "$tmp/stdin" || { echo "FAIL: the key header is not on curl's stdin"; fail=1; }
[[ "$out" == *"$KEY"* ]] && { echo "FAIL: the key is printed"; fail=1; }

out=$(run 200 "$CONF")
[[ "$out" == "rc=0" ]] || { echo "FAIL: 200 must be silent: $out"; fail=1; }

out=$(run 000 "$CONF")
[[ "$out" == *"LOG: CrowdSec AppSec did not answer"* && "$out" == *"rc=0"* ]] \
  || { echo "FAIL: a silent :7422 must log and return 0: $out"; fail=1; }
[[ "$(wc -l <"$tmp/argv")" -eq 15 ]] || { echo "FAIL: expected 15 tries, got $(wc -l <"$tmp/argv")"; fail=1; }

out=$(run 401 "API_KEY=$KEY")
[[ "$out" == "rc=0" && ! -e "$tmp/argv" ]] || { echo "FAIL: no APPSEC_URL must skip the probe: $out"; fail=1; }

[[ $fail -eq 0 ]] && echo "OK: AppSec bouncer-key probe"
exit $fail
