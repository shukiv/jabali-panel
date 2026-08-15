#!/usr/bin/env bash
# install/tests/test_ftp_module_optin.sh — regression coverage for GH #1053
# (FTP module opt-in). The FTP daemon is the panel's only deliberately-OFF
# network service: server_settings.ftp_enabled=0 must mean "vsftpd masked,
# ports closed" on every update path, and when it IS on, the config must
# never silently downgrade to cleartext.
#
# Asserts, source-level (no host mutation):
#   1. --install-module dispatcher knows ftp.
#   2. converge_ftp_masking exists, defaults OFF on unreadable DB, masks
#      vsftpd + deletes the firewall rules when off, and provision_new_software
#      (the `jabali update` path) calls it — an enforce function nobody calls
#      is not enforcement (the #1006 ensure_tmp_hardening lesson).
#   3. The rendered vsftpd config requires TLS unless ftp_allow_plaintext,
#      chroots local users, disables anonymous, and _dies rather than
#      serving passwordful cleartext when TLS is required but no cert exists.
#   4. The PAM service gates on jabali-ftp membership and OMITS pam_shells
#      (subaccounts ship /usr/sbin/nologin — step-3 review).
#   5. The UFW passive range matches the vsftpd pasv_min/max range — a
#      drifted pair silently breaks every passive transfer.
#
# Run from repo root:
#     bash install/tests/test_ftp_module_optin.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0

# --- 1. dispatcher case ---
if ! grep -qE '^\s+ftp\)\s+install_module_ftp' install.sh; then
  echo "FAIL: --install-module dispatcher has no ftp case"
  fail=1
fi

# --- 2. masking converger ---
masking=$(awk '/^converge_ftp_masking\(\)/,/^}/' install.sh)
if [[ -z "$masking" ]]; then
  echo "FAIL: converge_ftp_masking not defined"
  fail=1
else
  grep -q 'ftp_on=0' <<<"$masking" \
    || { echo "FAIL: converge_ftp_masking must default OFF"; fail=1; }
  grep -q 'systemctl mask vsftpd' <<<"$masking" \
    || { echo "FAIL: converge_ftp_masking must mask vsftpd when off"; fail=1; }
  grep -q 'ufw delete allow 21/tcp' <<<"$masking" \
    || { echo "FAIL: converge_ftp_masking must close port 21 when off"; fail=1; }
fi
provision=$(awk '/^provision_new_software\(\)/,/^}/' install.sh)
if ! grep -q 'converge_ftp_masking' <<<"$provision"; then
  echo "FAIL: provision_new_software does not call converge_ftp_masking — the update path would never enforce the opt-in"
  fail=1
fi

# --- 3. vsftpd config safety ---
cfgfn=$(awk '/^install_vsftpd_config\(\)/,/^}/' install.sh)
if [[ -z "$cfgfn" ]]; then
  echo "FAIL: install_vsftpd_config not defined"
  fail=1
else
  grep -q 'anonymous_enable=NO' <<<"$cfgfn" \
    || { echo "FAIL: anonymous FTP must be disabled"; fail=1; }
  grep -q 'chroot_local_user=YES' <<<"$cfgfn" \
    || { echo "FAIL: local users must be chrooted"; fail=1; }
  grep -q 'force_local_logins_ssl=\${force_ssl}' <<<"$cfgfn" \
    || { echo "FAIL: TLS requirement must be wired to ftp_allow_plaintext"; fail=1; }
  # The no-cert + TLS-required combination must abort, not downgrade.
  grep -q '_die "ftp module: TLS is required' <<<"$cfgfn" \
    || { echo "FAIL: missing cert with TLS required must _die, never downgrade"; fail=1; }
fi

# --- 4. PAM: jabali-ftp gate, no pam_shells ---
pamfn=$(awk '/^install_vsftpd_pam\(\)/,/^}/' install.sh)
if [[ -z "$pamfn" ]]; then
  echo "FAIL: install_vsftpd_pam not defined"
  fail=1
else
  grep -q 'pam_succeed_if.so user ingroup jabali-ftp' <<<"$pamfn" \
    || { echo "FAIL: PAM must gate on jabali-ftp membership"; fail=1; }
  # Directive lines only — comments may (and do) mention pam_shells to
  # explain WHY it's absent.
  if grep -vE '^\s*#' <<<"$pamfn" | grep -q 'pam_shells\.so'; then
    echo "FAIL: PAM must omit pam_shells.so (subaccounts use nologin)"
    fail=1
  fi
fi

# --- 4b. no invented vsftpd directives (live incident 2026-08-15) ---
# Debian's vsftpd exits status 2 with NO error output on any unknown config
# option, and systemd reports the start as successful before the crash.
# ssl_tlsv1_2 / ssl_tlsv1_3 looked plausible and killed the daemon on a
# real box. Pin the ssl directive set to the empirically-verified one.
for bad in ssl_tlsv1_2 ssl_tlsv1_3; do
  if grep -q "^${bad}=" <<<"$cfgfn"; then
    echo "FAIL: vsftpd config uses unsupported directive ${bad} (daemon exits 2 silently)"
    fail=1
  fi
done
grep -q '^ssl_tlsv1=YES' <<<"$cfgfn" \
  || { echo "FAIL: expected supported ssl_tlsv1=YES directive"; fail=1; }
# The installer must not trust systemctl start's exit code (vsftpd dies
# post-start on config errors) — require the settle + is-active gate.
modfn=$(awk '/^install_module_ftp\(\)/,/^}/' install.sh)
grep -q 'systemctl is-active --quiet vsftpd' <<<"$modfn" \
  || { echo "FAIL: install_module_ftp must verify vsftpd is ACTIVE after start"; fail=1; }

# --- 4c. tuning knobs rendered from DB (GH #1053 follow-up) ---
# max_clients / max_per_ip / local_max_rate must come from server_settings
# (with sane defaults for pre-000265 boxes). All are REAL vsftpd directives
# — same invented-name hazard class as 4b.
for want in 'max_clients=\$\{max_clients\}' 'max_per_ip=\$\{max_per_ip\}'; do
  if ! grep -qE "^${want}" <<<"$cfgfn"; then
    echo "FAIL: vsftpd config missing DB-driven directive ${want}"
    fail=1
  fi
done
grep -q 'local_max_rate' <<<"$cfgfn" \
  || { echo "FAIL: local_max_rate not rendered"; fail=1; }
grep -q 'max_rate_kbs \* 1024' <<<"$cfgfn" \
  || { echo "FAIL: local_max_rate must convert the panel's KB/s to bytes/s"; fail=1; }

# --- 5. passive range consistency (vsftpd conf vs UFW rule) ---
pasv_min=$(grep -oE '^pasv_min_port=[0-9]+' <<<"$cfgfn" | cut -d= -f2 || true)
pasv_max=$(grep -oE '^pasv_max_port=[0-9]+' <<<"$cfgfn" | cut -d= -f2 || true)
fwfn=$(awk '/^install_ftp_firewall_rules\(\)/,/^}/' install.sh)
if [[ -z "$pasv_min" || -z "$pasv_max" ]]; then
  echo "FAIL: pasv_min_port/pasv_max_port not found in vsftpd config"
  fail=1
elif ! grep -q "ufw allow ${pasv_min}:${pasv_max}/tcp" <<<"$fwfn"; then
  echo "FAIL: UFW passive range does not match vsftpd ${pasv_min}:${pasv_max}"
  fail=1
fi
if ! grep -q "ufw delete allow ${pasv_min}:${pasv_max}/tcp" <<<"$masking"; then
  echo "FAIL: converge_ftp_masking must close the same passive range"
  fail=1
fi

if [[ "$fail" -eq 0 ]]; then
  echo "OK: ftp module opt-in guards hold"
else
  exit 1
fi
