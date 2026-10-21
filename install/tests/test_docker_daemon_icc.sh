#!/usr/bin/env bash
# install/tests/test_docker_daemon_icc.sh — both daemon.json variants
# install_docker_engine writes (with and without userns-remap) are valid JSON
# and turn off inter-container communication on the default bridge
# ("icc": false, CIS Docker 2.1), while keeping live-restore so the one restart
# that adding the key causes does not stop running containers.
#
# Run from repo root:
#     bash install/tests/test_docker_daemon_icc.sh
#
# Exit 0 = pass.
set -euo pipefail

cd "$(dirname "$0")/../.."

# The function body cannot be cut at the first "}" line: the JSON heredocs
# inside it end with one. Read from the function to the next function instead.
body=$(awk '/^install_docker_engine\(\) \{$/ {on=1; print; next} on && /^[a-z_]+\(\) \{$/ {exit} on {print}' install.sh)
if [[ -z "$body" ]]; then
  echo "FAIL: install_docker_engine not found in install.sh"
  exit 1
fi

# Each DOCKER_EOF heredoc inside the function is one daemon.json variant.
mapfile -t docs < <(awk '/<<.DOCKER_EOF.$/ {on=1; doc=""; next} /^DOCKER_EOF$/ {on=0; gsub(/\n/, " ", doc); print doc; next} on {doc = doc $0 "\n"}' <<<"$body")
if (( ${#docs[@]} != 2 )); then
  echo "FAIL: expected 2 daemon.json variants, found ${#docs[@]}"
  exit 1
fi

fail=0
for d in "${docs[@]}"; do
  if ! python3 -c 'import json,sys; c=json.loads(sys.argv[1]); assert c.get("icc") is False, "icc"; assert c.get("live-restore") is True, "live-restore"' "$d" 2>/dev/null; then
    echo "FAIL: daemon.json variant is not valid JSON with icc false + live-restore true: $d"
    fail=1
  fi
done
(( fail == 0 )) && echo "PASS: both daemon.json variants set icc false and keep live-restore"
exit "$fail"
