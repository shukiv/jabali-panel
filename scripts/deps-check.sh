#!/usr/bin/env bash
# deps-check.sh — one monthly pass over EVERY dependency surface (GH #613/#614).
#
# jabali pins dependencies in three places that upgrade differently:
#   1. install.sh  — upstream binaries/tools as a bash VAR="x.y" plus a
#      companion install/<tool>.sha256 checksum file. Off-the-shelf bots
#      (Dependabot/Renovate) cannot see these, so they're the ones that end up
#      as one-off "bump X" issues.
#   2. go.mod      — Go modules.
#   3. panel-ui/package.json — npm deps.
#
# This script reports current-vs-latest across all three so the monthly review
# is a single table, and can re-capture an install.sh pin's .sha256 in lockstep
# (`--refresh-sha <VAR>`) so a bump is: edit the version var -> refresh -> done.
#
# Usage:
#   scripts/deps-check.sh                 # human table on stdout
#   scripts/deps-check.sh --markdown      # GitHub-flavored markdown (for the issue)
#   scripts/deps-check.sh --refresh-sha STALWART_VERSION [--version 0.16.15]
#
# Needs: gh (authenticated), go, npm. A missing tool degrades to a skipped
# section with a note, never a hard failure.
set -uo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
INSTALL_SH="$REPO_DIR/install.sh"

# ---- manifest -------------------------------------------------------------
# One row per install.sh pin: VAR|repo|tag_prefix|sha_file|url_template
#  - VAR         : the bash variable in install.sh (upper- or lower-case).
#  - repo        : GitHub owner/name whose releases/latest is the source of truth.
#  - tag_prefix  : stripped from the release tag to get the bare version
#                  ("v", "", or "RELEASE_" for phpMyAdmin's RELEASE_5_2_3).
#  - sha_file    : install/*.sha256 to refresh in lockstep, or "-" if none.
#  - url_template: release asset URL for --refresh-sha ({V}=version), or "-"
#                  when the asset name varies by arch (report-only; the table
#                  prints the manual capture command instead).
MANIFEST=(
  "STALWART_VERSION|stalwartlabs/stalwart|v|install/stalwart.sha256|https://github.com/stalwartlabs/stalwart/releases/download/v{V}/stalwart-x86_64-unknown-linux-gnu.tar.gz"
  "cli_version|stalwartlabs/cli|v|install/stalwart-cli.sha256|-"
  "bulwark_version|bulwarkmail/webmail||install/bulwark.sha256|-"
  "kratos_version|ory/kratos|v|install/kratos.sha256|https://github.com/ory/kratos/releases/download/v{V}/kratos_{V}-linux_64bit.tar.gz"
  "wp_version|wp-cli/wp-cli|v|install/wp-cli.sha256|https://github.com/wp-cli/wp-cli/releases/download/v{V}/wp-cli-{V}.phar"
  "pma_version|phpmyadmin/phpmyadmin|RELEASE_|install/phpmyadmin.sha256|-"
  "snuf_version|jvoisin/snuffleupagus|v|-|-"
  "LMD_VERSION|rfxn/linux-malware-detect|v|-|-"
  "YARAX_VERSION|VirusTotal/yara-x|v|-|-"
)

MODE="table"
REFRESH_VAR=""
REFRESH_VERSION=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --markdown) MODE="markdown"; shift ;;
    --refresh-sha) MODE="refresh"; REFRESH_VAR="${2:-}"; shift 2 ;;
    --version) REFRESH_VERSION="${2:-}"; shift 2 ;;
    -h|--help) grep -E '^#( |$)' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

# current_pin VAR -> the value assigned in install.sh (first match wins).
current_pin() {
  grep -oE "${1}=\"[^\"]+\"" "$INSTALL_SH" 2>/dev/null | head -1 | sed -E "s/.*=\"([^\"]+)\"/\1/"
}

# latest_release repo tag_prefix -> bare latest version, or "" on failure.
# Prefers gh (authenticated locally); falls back to curl + the REST API so it
# also works on the self-hosted CI runner, which has curl + a token but no gh.
latest_release() {
  local repo="$1" prefix="$2" tag=""
  if command -v gh >/dev/null 2>&1; then
    tag="$(gh api "repos/${repo}/releases/latest" --jq '.tag_name' 2>/dev/null)"
  fi
  if [[ -z "$tag" ]]; then
    local -a auth=()
    local tok="${GH_TOKEN:-${GITHUB_TOKEN:-}}"
    [[ -n "$tok" ]] && auth=(-H "Authorization: Bearer ${tok}")
    tag="$(curl -fsSL "${auth[@]}" -H "Accept: application/vnd.github+json" \
      "https://api.github.com/repos/${repo}/releases/latest" 2>/dev/null \
      | grep -oE '"tag_name"[[:space:]]*:[[:space:]]*"[^"]+"' | head -1 \
      | sed -E 's/.*"([^"]+)"$/\1/')"
  fi
  [[ -z "$tag" ]] && return 0
  # phpMyAdmin tags are RELEASE_5_2_3 -> 5.2.3; underscores to dots after strip.
  tag="${tag#"$prefix"}"
  [[ "$prefix" == "RELEASE_" ]] && tag="${tag//_/.}"
  printf '%s' "$tag"
}

# ---- --refresh-sha --------------------------------------------------------
if [[ "$MODE" == "refresh" ]]; then
  [[ -z "$REFRESH_VAR" ]] && { echo "--refresh-sha needs a VAR name" >&2; exit 2; }
  for row in "${MANIFEST[@]}"; do
    IFS='|' read -r var repo prefix shafile urltpl <<<"$row"
    [[ "$var" != "$REFRESH_VAR" ]] && continue
    [[ "$shafile" == "-" || "$urltpl" == "-" ]] && { echo "$var has no auto-refreshable sha (arch-varying asset) — capture by hand" >&2; exit 1; }
    local_ver="${REFRESH_VERSION:-$(current_pin "$var")}"
    [[ -z "$local_ver" ]] && { echo "could not resolve a version for $var" >&2; exit 1; }
    url="${urltpl//\{V\}/$local_ver}"
    echo "downloading $url" >&2
    sum="$(curl -fsSL "$url" | sha256sum | awk '{print $1}')" || { echo "download/hash failed for $url" >&2; exit 1; }
    [[ -z "$sum" ]] && { echo "empty checksum" >&2; exit 1; }
    tgt="$REPO_DIR/$shafile"
    { echo "# ${var} ${local_ver} — SHA-256 of $(basename "$url") (refreshed by deps-check.sh)"; echo "$sum  $(basename "$url")"; } >"$tgt"
    echo "wrote $shafile: $sum" >&2
    exit 0
  done
  echo "unknown VAR: $REFRESH_VAR" >&2; exit 2
fi

# ---- collect install.sh pin rows -----------------------------------------
declare -a ROWS_OUT   # "name|current|latest|status"
outdated=0
for row in "${MANIFEST[@]}"; do
  IFS='|' read -r var repo prefix shafile urltpl <<<"$row"
  cur="$(current_pin "$var")"; [[ -z "$cur" ]] && cur="(unset)"
  lat="$(latest_release "$repo" "$prefix")"; [[ -z "$lat" ]] && lat="(fetch failed)"
  if [[ "$lat" == "(fetch failed)" || "$cur" == "$lat" ]]; then
    status="ok"
  else
    status="UPGRADE"; outdated=$((outdated+1))
  fi
  disp="${var%_VERSION}"; disp="${disp%_version}"; disp="${disp,,}"
  ROWS_OUT+=("$disp|$cur|$lat|$status|$repo")
done

# ---- go.mod --------------------------------------------------------------
go_report() {
  command -v go >/dev/null 2>&1 || { echo "_(go not installed — skipped)_"; return; }
  local out
  out="$(cd "$REPO_DIR" && GOFLAGS=-mod=mod go list -u -m -f '{{if and .Update (not .Indirect)}}{{.Path}} {{.Version}} -> {{.Update.Version}}{{end}}' all 2>/dev/null | grep -v '^$')"
  if [[ -z "$out" ]]; then echo "All direct Go modules up to date."; else echo "$out"; fi
}

# ---- npm -----------------------------------------------------------------
npm_report() {
  command -v npm >/dev/null 2>&1 || { echo "_(npm not installed — skipped)_"; return; }
  [[ -f "$REPO_DIR/panel-ui/package.json" ]] || { echo "_(panel-ui/package.json missing)_"; return; }
  local json
  json="$(cd "$REPO_DIR/panel-ui" && npm outdated --json 2>/dev/null)"
  [[ -z "$json" || "$json" == "{}" ]] && { echo "All npm deps up to date."; return; }
  if command -v jq >/dev/null 2>&1; then
    echo "$json" | jq -r 'to_entries[] | "\(.key) \(.value.current) -> \(.value.latest)"'
  else
    echo "$json"
  fi
}

# ---- render ---------------------------------------------------------------
if [[ "$MODE" == "markdown" ]]; then
  echo "## install.sh pins (${outdated} to upgrade)"
  echo
  echo "| dependency | current | latest | status | repo |"
  echo "|---|---|---|---|---|"
  for r in "${ROWS_OUT[@]}"; do
    IFS='|' read -r n c l s repo <<<"$r"
    mark="✅"; [[ "$s" == "UPGRADE" ]] && mark="⬆️ **upgrade**"
    echo "| \`$n\` | $c | $l | $mark | [$repo](https://github.com/$repo/releases/latest) |"
  done
  echo
  echo "## Go modules (\`go.mod\`)"
  echo '```'; go_report; echo '```'
  echo
  echo "## npm (\`panel-ui\`)"
  echo '```'; npm_report; echo '```'
  echo
  echo "> Bump an install.sh pin, then \`scripts/deps-check.sh --refresh-sha <VAR>\` to re-capture its \`.sha256\`. Framework bumps (Stalwart, Bulwark, Kratos) need an E2E on a box before merge."
else
  printf '%-14s %-14s %-14s %s\n' "DEP" "CURRENT" "LATEST" "STATUS"
  for r in "${ROWS_OUT[@]}"; do
    IFS='|' read -r n c l s repo <<<"$r"
    printf '%-14s %-14s %-14s %s\n' "$n" "$c" "$l" "$s"
  done
  echo; echo "== Go (direct, outdated) =="; go_report
  echo; echo "== npm (panel-ui, outdated) =="; npm_report
  echo; echo "install.sh pins needing upgrade: $outdated"
fi
