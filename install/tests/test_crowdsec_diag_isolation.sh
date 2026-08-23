#!/usr/bin/env bash
# install/tests/test_crowdsec_diag_isolation.sh — regression coverage for
# JAB-368. CrowdSec's prometheus listener on 127.0.0.1:6060 is UNAUTHENTICATED
# and serves Go /debug/pprof (heap/goroutine dumps + CPU profiling) alongside
# /metrics. It cannot be disabled (`cscli metrics`, the panel's metrics source,
# requires it), so a packet-layer rule must block tenant-uid access to it while
# leaving root (cscli) + system users reachable.
#
# Asserts, source-level (no host mutation):
#   1. the shipped nft ruleset blocks skuid>=1000 → 127.0.0.1/::1 :6060 (v4+v6),
#      via the idempotent add/flush idiom (a re-apply must not duplicate rules);
#   2. the load unit exists, applies the file, and has NO ConditionPathExists
#      (skuid rule has no cgroup path → must load on every boot);
#   3. install.sh defines ensure_crowdsec_diag_isolation and calls it from BOTH
#      the fresh-install path (install_crowdsec) AND the update path
#      (provision_new_software) — a fleet host must converge on `jabali update`.
#
# Run from repo root:
#     bash install/tests/test_crowdsec_diag_isolation.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0
nft=install/crowdsec/jabali-diag-loopback-isolation.nft
unit=install/systemd/jabali-diag-loopback-isolation-load.service

# --- 1. nft ruleset: uid-based :6060 drop, v4 + v6, flush idiom ---
if [[ ! -f "$nft" ]]; then
  echo "FAIL: missing nft ruleset $nft"; exit 1
fi
if ! grep -qE 'meta skuid >= 1000 ip daddr 127\.0\.0\.1 tcp dport 6060 drop' "$nft"; then
  echo "FAIL: $nft must drop skuid>=1000 → 127.0.0.1 tcp/6060 (JAB-368)"; fail=1
fi
if ! grep -qE 'meta skuid >= 1000 ip6 daddr ::1 tcp dport 6060 drop' "$nft"; then
  echo "FAIL: $nft must also drop the IPv6 ::1 :6060 path (JAB-368)"; fail=1
fi
# Idempotent re-apply: the add/flush idiom must be present (a bare `table {...}`
# re-declaration would append duplicate rules on every `jabali update`).
if ! grep -qE '^flush table inet jabali_diag_isolation' "$nft"; then
  echo "FAIL: $nft must use the add/flush idiom so a re-apply replaces, not duplicates, the rules"; fail=1
fi
# skuid, not cgroup: a cgroup match would miss FPM (system.slice) + shells
# (user.slice). Check RULE lines only — the rationale comment mentions cgroupv2.
if grep -vE '^[[:space:]]*#' "$nft" | grep -qE 'cgroupv2'; then
  echo "FAIL: $nft must match on skuid, not cgroupv2 (FPM/shells are not in the tenant slice) (JAB-368)"; fail=1
fi

# --- 2. load unit: applies the file, no ConditionPathExists ---
if [[ ! -f "$unit" ]]; then
  echo "FAIL: missing load unit $unit"; exit 1
fi
if ! grep -qE '^ExecStart=/usr/sbin/nft -f /etc/nftables.d/jabali-diag-loopback-isolation\.nft' "$unit"; then
  echo "FAIL: $unit must ExecStart nft -f the installed ruleset"; fail=1
fi
if grep -qE '^ConditionPathExists=' "$unit"; then
  echo "FAIL: $unit must NOT gate on ConditionPathExists — the skuid rule has no cgroup path and must load every boot (JAB-368)"; fail=1
fi

# --- 3. install.sh wiring: defined + called on BOTH install and update ---
if ! grep -qE '^ensure_crowdsec_diag_isolation\(\) \{' install.sh; then
  echo "FAIL: install.sh must define ensure_crowdsec_diag_isolation"; fail=1
fi
# fresh-install path: install_crowdsec calls it (provision_new_software is update-only).
cs=$(awk '/^install_crowdsec\(\) \{/{f=1} f{print} /^}/&&f{exit}' install.sh)
if ! grep -q 'ensure_crowdsec_diag_isolation' <<<"$cs"; then
  echo "FAIL: install_crowdsec must call ensure_crowdsec_diag_isolation (fresh-install coverage)"; fail=1
fi
# update path: provision_new_software calls it (existing fleet hosts converge).
pn=$(awk '/^provision_new_software\(\) \{/{f=1} f{print} /^}/&&f{exit}' install.sh)
if ! grep -q 'ensure_crowdsec_diag_isolation' <<<"$pn"; then
  echo "FAIL: provision_new_software must call ensure_crowdsec_diag_isolation (jabali update convergence)"; fail=1
fi

if [[ "$fail" -eq 0 ]]; then
  echo "PASS: crowdsec :6060 diagnostics tenant-isolation is shipped + wired on both paths (JAB-368)"
else
  exit 1
fi
