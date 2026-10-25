#!/usr/bin/env bash
# install/tests/test_nginx_server_names_hash_on_update.sh — the JAB-214
# server_names_hash bucket reaches updated boxes, not only fresh installs.
#
# install_nginx_server_names_hash ran only from main(), so a box installed
# before it kept nginx's 64-byte bucket and rejected every server_name longer
# than ~46 characters ("could not build server_names_hash, you should
# increase server_names_hash_bucket_size: 64"). A preview URL on a free
# jabalihosted.com hostname (<slug>.<label>.jabalihosted.com) is nearly always
# that long, and its domain vhost then failed nginx -t and was rolled back.
#
# Source-level: both main() and provision_new_software() (the update path)
# call it.
#
# Run from repo root:
#     bash install/tests/test_nginx_server_names_hash_on_update.sh
set -uo pipefail

cd "$(dirname "$0")/../.."

fail=0
fn() { awk -v n="$1" '$0 == n "() {" {p=1} p {print} p && /^}$/ {exit}' install.sh; }

if [[ -z "$(fn install_nginx_server_names_hash)" ]]; then
  echo "FAIL: install_nginx_server_names_hash not defined in install.sh"
  fail=1
fi
for caller in main provision_new_software; do
  if ! fn "$caller" | grep -qx '  install_nginx_server_names_hash'; then
    echo "FAIL: $caller() does not call install_nginx_server_names_hash"
    fail=1
  fi
done

if [[ "$fail" -eq 0 ]]; then
  echo "PASS: server_names_hash bucket on install and update"
fi
exit "$fail"
