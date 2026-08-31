#!/usr/bin/env bash
# Verify every pinned third-party download in install.sh points at a PUBLISHED
# artifact (GH #670). A version bumped AHEAD of upstream — tagged before its
# release files exist, or a CDN propagation gap — 404s here, so the PR that
# introduces it fails CI instead of bricking real installs 10 minutes in.
#
# Versions are read FROM install.sh (single source of truth — no drift). Adding
# a new pinned download to install.sh should add a line here too; that coupling
# is deliberate, so coverage can't silently lapse.
#
# Usage: scripts/check-pinned-downloads.sh [path/to/install.sh]
set -uo pipefail
INSTALL="${1:-install.sh}"
[[ -f "$INSTALL" ]] || { echo "no such file: $INSTALL" >&2; exit 2; }
fail=0

# pin <regex> — pull the first dotted version number matched by <regex> from install.sh
pin() { grep -oE "$1" "$INSTALL" | head -1 | grep -oE '[0-9]+(\.[0-9]+)+' | head -1; }

# curl HEAD; treat any 2xx/3xx as published (GitHub asset URLs 302 to the CDN)
_head() { curl -gsSL -I --retry 3 --retry-delay 3 --connect-timeout 20 -o /dev/null -w '%{http_code}' "$1" 2>/dev/null; }

# GitHub release tag exists? (API — rate-limit-safe with GITHUB_TOKEN in CI)
_gh_release() {
  local repo="$1" tag="$2" code auth=()
  [[ -n "${GITHUB_TOKEN:-}" ]] && auth=(-H "Authorization: Bearer ${GITHUB_TOKEN}")
  code="$(curl -gsSL -o /dev/null -w '%{http_code}' "${auth[@]}" \
    -H 'Accept: application/vnd.github+json' \
    --retry 3 --retry-delay 3 --connect-timeout 20 \
    "https://api.github.com/repos/${repo}/releases/tags/${tag}" 2>/dev/null)"
  [[ "$code" == "200" ]]
}

ok=0
report() { # label  ok|fail  detail
  if [[ "$2" == ok ]]; then printf '  \033[32mok\033[0m   %-22s %s\n' "$1" "$3"; ((ok++))
  else printf '  \033[31mFAIL\033[0m %-22s %s\n' "$1" "$3"; fail=1; fi
}

check_url() { # label url
  local code; code="$(_head "$2")"
  if [[ "$code" =~ ^(2|3) ]]; then report "$1" ok "$2 [$code]"; else report "$1" fail "$2 [$code] — NOT PUBLISHED"; fi
}
check_gh() { # label owner/repo tag
  if _gh_release "$2" "$3"; then report "$1" ok "$2 @ $3"; else report "$1" fail "$2 @ $3 — release tag missing"; fi
}

echo "verifying pinned downloads in $INSTALL"

GO_V="$(pin 'JABALI_GO_VERSION:-[0-9.]+')"
check_url "go $GO_V"          "https://go.dev/dl/go${GO_V}.linux-amd64.tar.gz"

WP_V="$(pin 'wp_version="[0-9.]+"')"
check_url "wp-cli $WP_V"      "https://github.com/wp-cli/wp-cli/releases/download/v${WP_V}/wp-cli-${WP_V}.phar"

LMD_V="$(pin 'LMD_VERSION="[0-9.]+"')"
check_url "linux-malware $LMD_V" "https://github.com/rfxn/linux-malware-detect/archive/refs/tags/v${LMD_V}.tar.gz"

YX_V="$(pin 'YARAX_VERSION="[0-9.]+"')"
check_url "yara-x $YX_V"      "https://github.com/VirusTotal/yara-x/releases/download/v${YX_V}/yara-x-v${YX_V}-x86_64-unknown-linux-gnu.tar.gz"

# signature-base is pinned by commit SHA (JAB-248), not a release tag —
# verify the pinned commit actually exists upstream so a typo'd or
# invented SHA fails CI instead of bricking the clone on every box.
# pin() only extracts dotted versions, so pull the 40-hex SHA directly.
SB_C="$(grep -oE 'SIGBASE_COMMIT="[0-9a-f]{40}"' "$INSTALL" | head -1 | grep -oE '[0-9a-f]{40}')"
_gh_commit() { # repo sha
  local code auth=()
  [[ -n "${GITHUB_TOKEN:-}" ]] && auth=(-H "Authorization: Bearer ${GITHUB_TOKEN}")
  code="$(curl -gsSL -o /dev/null -w '%{http_code}' "${auth[@]}" \
    -H 'Accept: application/vnd.github+json' \
    --retry 3 --retry-delay 3 --connect-timeout 20 \
    "https://api.github.com/repos/$1/commits/$2" 2>/dev/null)"
  [[ "$code" == "200" ]]
}
if [[ -z "$SB_C" ]]; then
  report "signature-base" fail "SIGBASE_COMMIT not found in $INSTALL (expected a 40-hex pin — JAB-248)"
elif _gh_commit "Neo23x0/signature-base" "$SB_C"; then
  report "signature-base ${SB_C:0:12}" ok "Neo23x0/signature-base @ ${SB_C:0:12}"
else
  report "signature-base ${SB_C:0:12}" fail "commit not found upstream — typo'd or unpublished pin"
fi

SW_V="$(pin 'STALWART_VERSION="[0-9.]+"')"
check_gh  "stalwart $SW_V"    "stalwartlabs/stalwart"    "v${SW_V}"

CLI_V="$(pin 'cli_version="[0-9.]+"')"
check_gh  "stalwart-cli $CLI_V" "stalwartlabs/cli"       "v${CLI_V}"

BW_V="$(pin 'bulwark_version="[0-9.]+"')"
check_gh  "bulwark-webmail $BW_V" "bulwarkmail/webmail"  "${BW_V}"   # no v-prefix

KR_V="$(pin 'kratos_version="[0-9.]+"')"
check_url "kratos $KR_V"      "https://github.com/ory/kratos/releases/download/v${KR_V}/kratos_${KR_V}-linux_64bit.tar.gz"

check_url "adminer 6.0.1"     "https://github.com/vrana/adminer/releases/download/v6.0.1/adminer-6.0.1.php"

echo ""
if [[ $fail -eq 0 ]]; then
  echo "all $ok pinned downloads are published ✓"
else
  echo "one or more pins are AHEAD of upstream (unpublished) — see FAIL above."
  echo "wait for the upstream release, or lower the pin, before merging."
  exit 1
fi
