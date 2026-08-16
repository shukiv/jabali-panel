#!/usr/bin/env bash
# JAB-248 rule-promotion gate: a signature-base pin bump (SIGBASE_COMMIT
# in install.sh) is the ONLY way new YARA rules reach production, so the
# pin-bump PR is the promotion step — and this script is its test:
#
#   1. COMPILE  — the pinned commit's full yara/ tree must compile under
#      yara-x (the engine maldet runs) with the standard signature-base
#      externals defined. A rule that doesn't compile is silently skipped
#      in production, shrinking coverage without anyone noticing.
#   2. BROAD-MATCH — the ruleset is run over this repository's own tree
#      as a known-clean corpus. A handful of hits is normal (test
#      fixtures, security-tooling strings); a rule matching MANY clean
#      files is exactly the mass-quarantine failure JAB-248 describes,
#      and must never merge.
#
# Runs in CI only when the diff touches SIGBASE_COMMIT; run locally with:
#   scripts/check-sigbase-rules.sh [install.sh] [corpus-dir]
set -euo pipefail

INSTALL="${1:-install.sh}"
CORPUS="${2:-.}"
BROAD_MATCH_MAX=10

[[ -f "$INSTALL" ]] || { echo "no such file: $INSTALL" >&2; exit 2; }

SIGBASE_COMMIT="$(grep -oE 'SIGBASE_COMMIT="[0-9a-f]{40}"' "$INSTALL" | head -1 | grep -oE '[0-9a-f]{40}')"
[[ -n "$SIGBASE_COMMIT" ]] || { echo "SIGBASE_COMMIT not found in $INSTALL" >&2; exit 2; }

# yara-x: use the PATH binary when present, else install the exact
# version install.sh pins (CI path — GitHub-hosted runners start bare).
if ! command -v yr >/dev/null 2>&1; then
  YARAX_VERSION="$(grep -oE 'YARAX_VERSION="[0-9.]+"' "$INSTALL" | head -1 | grep -oE '[0-9]+(\.[0-9]+)+')"
  YARAX_SHA256="$(grep -oE 'YARAX_SHA256="[0-9a-f]{64}"' "$INSTALL" | head -1 | grep -oE '[0-9a-f]{64}')"
  [[ -n "$YARAX_VERSION" && -n "$YARAX_SHA256" ]] || { echo "cannot resolve pinned yara-x from $INSTALL" >&2; exit 2; }
  tmp_yr="$(mktemp -d)"
  trap 'rm -rf "$tmp_yr"' EXIT
  curl -fsSL "https://github.com/VirusTotal/yara-x/releases/download/v${YARAX_VERSION}/yara-x-v${YARAX_VERSION}-x86_64-unknown-linux-gnu.tar.gz" -o "$tmp_yr/yrx.tar.gz"
  echo "${YARAX_SHA256}  $tmp_yr/yrx.tar.gz" | sha256sum -c - >/dev/null
  tar -xzf "$tmp_yr/yrx.tar.gz" -C "$tmp_yr"
  export PATH="$tmp_yr:$PATH"
fi
echo "yara-x: $(yr --version)"

# Fetch the pinned signature-base tree (fetch-by-SHA, same as install.sh).
SB_DIR="$(mktemp -d)"
trap 'rm -rf "$SB_DIR" ${tmp_yr:-}' EXIT
git init --quiet "$SB_DIR"
git -C "$SB_DIR" remote add origin https://github.com/Neo23x0/signature-base.git
git -C "$SB_DIR" fetch --depth=1 --quiet origin "$SIGBASE_COMMIT"
git -C "$SB_DIR" checkout --quiet --detach FETCH_HEAD
RULES="$SB_DIR/yara"
[[ -d "$RULES" ]] || { echo "pinned commit has no yara/ directory" >&2; exit 1; }
echo "signature-base @ ${SIGBASE_COMMIT:0:12}: $(ls "$RULES"/*.yar 2>/dev/null | wc -l) rule files"

# The externals maldet/signature-base rules expect. Values are empty —
# only their EXISTENCE matters for compilation.
DEFS=(-d 'filename=""' -d 'filepath=""' -d 'extension=""' -d 'filetype=""' -d 'owner=""')

# Gate 1 — compile. Scanning an empty file forces a full compile; any
# compile error lands on stderr as "error:" and flips the exit code.
probe="$(mktemp)"
if ! yr scan "${DEFS[@]}" "$RULES" "$probe" >/dev/null 2>"$probe.err"; then
  echo "FAIL: pinned ruleset does not compile under yara-x:" >&2
  grep -E "^error" "$probe.err" | head -20 >&2
  rm -f "$probe" "$probe.err"
  exit 1
fi
rm -f "$probe" "$probe.err"
echo "compile: ok"

# Gate 2 — broad-match over the clean corpus (this repo). Output is
# "RULE FILE" per line; count distinct matched files and the top rules.
matches="$(yr scan "${DEFS[@]}" "$RULES" "$CORPUS" 2>/dev/null || true)"
matched_files="$(printf '%s\n' "$matches" | awk 'NF{print $2}' | sort -u | grep -c . || true)"
echo "broad-match: $matched_files distinct corpus files matched (max $BROAD_MATCH_MAX)"
if (( matched_files > BROAD_MATCH_MAX )); then
  echo "FAIL: ruleset matches $matched_files clean-corpus files — overbroad rule(s):" >&2
  printf '%s\n' "$matches" | awk 'NF{print $1}' | sort | uniq -c | sort -rn | head -10 >&2
  exit 1
fi
echo "sigbase rule gate: PASS"
