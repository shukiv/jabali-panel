#!/usr/bin/env bash
# install/tests/test_crowdsec_agent_lapi_url.sh — AppSec must be able to check
# bouncer keys.
#
# The AppSec engine validates every bouncer key through the agent's LAPI client
# URL in /etc/crowdsec/local_api_credentials.yaml, which install_crowdsec pins
# to http://127.0.0.1:8081/ (M27). configure_crowdsec_mariadb then ran
# `cscli machines add --force`, which rewrote the file with the LAPI socket
# path, so a fresh install answered every nginx-bouncer → AppSec call with 401
# and AppSec inspected nothing.
#
# 1. Functional: ensure_crowdsec_agent_lapi_url with yq + systemctl stubbed.
# 2. Static: configure_crowdsec_mariadb calls it after the re-register and
#    before its restart; provision_new_software (jabali update) calls it.
#
# Run from repo root:
#     bash install/tests/test_crowdsec_agent_lapi_url.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

body=$(awk '/^ensure_crowdsec_agent_lapi_url\(\)/{f=1} f{print} f&&/^\}/{exit}' install.sh)
if [[ -z "$body" ]]; then
  echo "FAIL: ensure_crowdsec_agent_lapi_url() not found in install.sh"
  exit 1
fi
body=${body//\/etc\/crowdsec/$tmp}

# run_case <listen_uri> <creds url or "-" for no file> [arg]; prints "url=<after> calls=<systemctl calls>"
run_case() {
  rm -f "$tmp"/*.yaml
  printf 'api:\n  server:\n    listen_socket: /run/crowdsec/api.sock\n    listen_uri: %s\n' "$1" > "$tmp/config.yaml"
  [[ "$2" == - ]] || printf 'url: %s\nlogin: abc\npassword: secret\n' "$2" > "$tmp/local_api_credentials.yaml"
  local calls
  calls=$(
    _log() { :; }
    _warn() { :; }
    systemctl() { echo "systemctl $*"; }
    yq() { # the three forms the function uses
      case "$1 ${2:-}" in
        "-r .api.server.listen_uri // \"\"") sed -n 's/^ *listen_uri: *//p' "$3" ;;
        "-r .url // \"\"") sed -n 's/^url: *//p' "$3" ;;
        "-y -i") sed -i "s|^url: .*|url: $(sed -n 's/^\.url = "\(.*\)"$/\1/p' <<<"$3")|" "$4" ;;
        *) echo "unexpected yq $*" >&2; return 1 ;;
      esac
    }
    eval "$body"
    ensure_crowdsec_agent_lapi_url ${3:-}
  )
  echo "url=$(sed -n 's/^url: *//p' "$tmp/local_api_credentials.yaml" 2>/dev/null) calls=${calls:-none}"
}

out=$(run_case 127.0.0.1:8081 /run/crowdsec/api.sock)
if [[ "$out" != "url=http://127.0.0.1:8081/ calls=systemctl restart crowdsec" ]]; then
  echo "FAIL: a socket-path url must become http://127.0.0.1:8081/ and restart crowdsec (got: $out)"; fail=1
fi
out=$(run_case 127.0.0.1:8081 /run/crowdsec/api.sock norestart)
if [[ "$out" != "url=http://127.0.0.1:8081/ calls=none" ]]; then
  echo "FAIL: norestart must patch the url without restarting crowdsec (got: $out)"; fail=1
fi
out=$(run_case 127.0.0.1:8081 http://127.0.0.1:8081/)
if [[ "$out" != "url=http://127.0.0.1:8081/ calls=none" ]]; then
  echo "FAIL: a correct url must be left alone, with no restart (got: $out)"; fail=1
fi
out=$(run_case '""' /run/crowdsec/api.sock)
if [[ "$out" != "url=/run/crowdsec/api.sock calls=none" ]]; then
  echo "FAIL: a box whose LAPI has no 127.0.0.1:8081 listener must be left alone (got: $out)"; fail=1
fi
out=$(run_case 127.0.0.1:8081 -)
if [[ "$out" != "url= calls=none" ]]; then
  echo "FAIL: no credentials file (CrowdSec not installed) must be a no-op (got: $out)"; fail=1
fi

# --- static: both call sites ---
mig=$(awk '/^configure_crowdsec_mariadb\(\)/{f=1} f{print} f&&/^\}/{exit}' install.sh)
add=$(grep -n 'cscli machines add' <<<"$mig" | head -1 | cut -d: -f1 || true)
fix=$(grep -n 'ensure_crowdsec_agent_lapi_url norestart' <<<"$mig" | head -1 | cut -d: -f1 || true)
rst=$(grep -n 'systemctl restart crowdsec' <<<"$mig" | tail -1 | cut -d: -f1 || true)
if [[ -z "$add" || -z "$fix" || -z "$rst" ]] || (( fix < add || fix > rst )); then
  echo "FAIL: configure_crowdsec_mariadb must call ensure_crowdsec_agent_lapi_url norestart after cscli machines add and before its restart"; fail=1
fi
prov=$(awk '/^provision_new_software\(\)/{f=1} f{print} f&&/^\}/{exit}' install.sh)
if ! grep -qE '^[[:space:]]+ensure_crowdsec_agent_lapi_url$' <<<"$prov"; then
  echo "FAIL: provision_new_software (jabali update) must call ensure_crowdsec_agent_lapi_url"; fail=1
fi

if [[ $fail -eq 0 ]]; then
  echo "PASS: test_crowdsec_agent_lapi_url"
fi
exit $fail
