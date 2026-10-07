#!/usr/bin/env bash
# install/tests/test_download_ipv4_retry.sh — regression coverage for GH #2041:
# on a fresh Linode the installer died at the Go step. The host reached
# dl.google.com over IPv6, which answered 404 for every Go tarball, while IPv4
# served them. curl tries IPv6 first and its --retry does not retry an HTTP
# error, so the pinned Go and every fallback "404'd from go.dev".
#
# Asserts install.sh ships:
#   1. curl_ipv4_retry, executed with a stub curl: a failed download is tried
#      once more over IPv4; a download that works is not repeated; a download
#      that fails both ways fails.
#   2. Every curl that downloads to a file goes through it, the Go step, the
#      Go update and the Composer signature included.
#   3. The Go step's error says IPv4 was tried too.
#
# Run from repo root:
#     bash install/tests/test_download_ipv4_retry.sh
#
# Exit 0 = pass.
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0

# --- 1. The helper, pulled out of install.sh and run against a stub curl. ---
fn_src=$(awk '/^curl_ipv4_retry\(\) \{$/,/^\}$/' install.sh)
if [[ -z "$fn_src" ]]; then
  echo "FAIL: curl_ipv4_retry not defined in install.sh"
  exit 1
fi
eval "$fn_src"
_log() { :; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
calls="$tmp/calls"

# curl stub: IPv6 (no -4) answers per $V6, IPv4 per $V4.
curl() {
  printf '%s\n' "$*" >> "$calls"
  local v4=0 out="" a prev=""
  for a in "$@"; do
    [[ "$a" == "-4" ]] && v4=1
    [[ "$prev" == "-o" ]] && out="$a"
    prev="$a"
  done
  if [[ "$v4" == 1 && "$V4" == ok ]] || [[ "$V6" == ok && "$v4" == 0 ]]; then
    [[ -n "$out" ]] && printf 'tarball' > "$out"
    return 0
  fi
  return 22
}

run() {
  : > "$calls"
  rm -f "$tmp/out"
  set +e
  curl_ipv4_retry -fsSL -o "$tmp/out" "https://dl.google.com/go/go1.26.8.linux-amd64.tar.gz"
  rc=$?
  set -e
  ncalls=$(wc -l < "$calls")
}

V6=404 V4=ok run
if [[ "$rc" != 0 || "$ncalls" != 2 || "$(cat "$tmp/out" 2>/dev/null)" != tarball ]]; then
  echo "FAIL: IPv6 404, IPv4 200: rc=$rc calls=$ncalls, want the IPv4 retry to fetch the file"
  fail=1
fi
if ! sed -n 2p "$calls" | grep -qE '(^| )-4( |$)'; then
  echo "FAIL: the retry was not over IPv4: $(sed -n 2p "$calls")"
  fail=1
fi
if [[ "$(sed -n 2p "$calls" | sed -E 's/(^| )-4( |$)/\1/')" != "$(sed -n 1p "$calls")" ]]; then
  echo "FAIL: the retry is not the same download plus -4: $(sed -n 2p "$calls")"
  fail=1
fi

V6=ok V4=404 run
if [[ "$rc" != 0 || "$ncalls" != 1 ]]; then
  echo "FAIL: a download that works was repeated or failed: rc=$rc calls=$ncalls"
  fail=1
fi

V6=404 V4=404 run
if [[ "$rc" == 0 || "$ncalls" != 2 ]]; then
  echo "FAIL: a download that fails both ways: rc=$rc calls=$ncalls, want a failure after one IPv4 try"
  fail=1
fi
unset -f curl

# --- 2. Every download to a file goes through the helper. ---
# Join continuation lines, drop comments, then find curl commands (the word
# curl, not curl_ipv4_retry or a path) that write to a file with -o. Probes to
# /dev/null are not downloads.
raw=$(awk '
  { line = $0 }
  /^[[:space:]]*#/ && cont == "" { next }
  {
    if (sub(/\\$/, "", line)) { cont = cont line " "; next }
    cmd = cont line; cont = ""
    n = split(cmd, parts, /\|/)
    for (i = 1; i <= n; i++) {
      p = parts[i]
      if (p ~ /(^|[[:space:];(!])curl[[:space:]]/ && p ~ /[[:space:]]-o[[:space:]]/ && p !~ /-o[[:space:]]+\/dev\/null/) print NR ": " p
    }
  }' install.sh)
if [[ -n "$raw" ]]; then
  echo "FAIL: curl downloads to a file without curl_ipv4_retry:"
  printf '%s\n' "$raw" | cut -c1-200
  fail=1
fi

go_src=$(awk '/^install_go\(\) \{$/,/^\}$/' install.sh)
if ! grep -qE 'local go_curl=\(curl_ipv4_retry ' <<<"$go_src"; then
  echo "FAIL: install_go's downloads don't go through curl_ipv4_retry"
  fail=1
fi
if grep -qE '(^|[^_[:alnum:]])curl -' <<<"$go_src"; then
  echo "FAIL: install_go calls curl directly"
  fail=1
fi
upd_src=$(awk '/^ensure_go_toolchain_current\(\) \{$/,/^\}$/' install.sh)
if ! grep -q 'curl_ipv4_retry ' <<<"$upd_src"; then
  echo "FAIL: ensure_go_toolchain_current's download doesn't go through curl_ipv4_retry"
  fail=1
fi

# The Composer installer's signature is read to stdout; it must not fail
# over a path its installer download just survived.
if ! grep -qE '_composer_sig="\$\(curl_ipv4_retry ' install.sh; then
  echo "FAIL: the Composer signature download doesn't go through curl_ipv4_retry"
  fail=1
fi

# --- 3. The error names IPv4, so an operator doesn't chase the pin. ---
if ! grep -q 'over IPv6 and IPv4' <<<"$go_src"; then
  echo "FAIL: install_go's error doesn't say IPv4 was tried"
  fail=1
fi

if [[ "$fail" == 0 ]]; then
  echo "PASS: test_download_ipv4_retry"
fi
exit "$fail"
