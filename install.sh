#!/usr/bin/env bash
#
# Jabali Panel installer — Phase 1 scope.
#
# What it does on a fresh Debian 13 or Ubuntu 24.04 root shell:
#   1. Installs base OS packages (git, curl, ca-certificates, build-essential).
#   2. Installs Go 1.25.1 into /usr/local/go (idempotent).
#   3. Creates a `jabali` system user (no login) + /opt/jabali-panel state dir.
#   4. Clones (or pulls) https://github.com/shukiv/jabali-panel
#      into /opt/jabali-panel. If the repo is private, pass a Gitea token via
#      JABALI_GITHUB_TOKEN env var or the first positional arg.
#   5. Builds panel-api and installs the binary at /usr/local/bin/jabali-panel.
#   6. Writes + starts the `jabali-panel.service` systemd unit bound to
#      127.0.0.1:8443 (configurable via PANEL_ADDR in /etc/jabali/panel.env).
#   7. Smoke-tests GET /health.
#
# Later phases (2+) will extend this file to provision MariaDB, build the
# React SPA, install nginx, wire SSL, etc. For now this is deliberately
# scoped to what Phase 1 actually ships.
#
# Usage (public repo):
#   curl -fsSL https://raw.githubusercontent.com/shukiv/jabali-panel/main/install.sh | bash
#
# Flags (all optional, can be combined):
#   --hostname <fqdn>  Server hostname; skips the TTY prompt. Equivalent
#                      to setting JABALI_HOSTNAME. --hostname=<fqdn> also works.
#   --token <github>   Private-repo access token. Equivalent to
#                      setting JABALI_GITHUB_TOKEN.
#   --debug            Verbose mode: disable the _spin progress spinner
#                      (stream every wrapped command's stdout+stderr live
#                      so you can see exactly where apt / systemctl / curl
#                      is stalled), and enable `set -x` with a line-tagged
#                      PS4 so every shell command is traced. Equivalent to
#                      setting JABALI_DEBUG=1. Use when install.sh appears
#                      to hang — the last xtrace line names the stalled cmd.
#
# Examples:
#   curl -fsSL <...>/install.sh | bash -s -- --hostname=panel.example.com
#   curl -fsSL <...>/install.sh | bash -s -- --hostname panel.example.com --token <GITHUB_TOKEN>
#
# Legacy: `bash -s -- <GITHUB_TOKEN>` (positional token) still works.

# ---------- locale: pin to C.UTF-8 (MUST run before anything else) --------
# Operators SSH in with their own LANG (often a locale not yet generated on
# the target host — e.g. LANG=he_IL.UTF-8). Perl-using apt postinst scripts
# then spam "Setting locale failed" warnings and fall back to "C", which is
# fine behaviourally but noisy. C.UTF-8 is always available on glibc (no
# locale-gen needed) and gives UTF-8 I/O. Unset every LC_* variant so perl
# doesn't retry the un-generated locale chain. Run this BEFORE `set -e` so
# a hostile env var (e.g. LC_ALL unset to empty) can't trip the script on
# its first line.
unset LANGUAGE LC_CTYPE LC_NUMERIC LC_COLLATE LC_TIME LC_MESSAGES LC_MONETARY LC_ADDRESS LC_IDENTIFICATION LC_MEASUREMENT LC_PAPER LC_TELEPHONE LC_NAME
export LANG=C.UTF-8
export LC_ALL=C.UTF-8
# Keep apt from prompting for debconf mid-run.
export DEBIAN_FRONTEND=noninteractive

set -Eeuo pipefail

# ---------- fail-loud: ERR trap -------------------------------------------
# set -e exits on the first non-zero command. The default behavior prints
# nothing — whatever step failed looks identical to a clean exit, and the
# operator sees only the previous step's success log. This trap prints the
# line number + failing command + exit code on any non-zero exit in the
# script, including sub-shells. Don't use _err() yet (logger is defined
# further down and bash loads top-to-bottom); printf inline so the trap
# works regardless of which section triggers it.
__on_err() {
  local rc=$?
  local cmd="$BASH_COMMAND"
  # BASH_LINENO[0], not LINENO. Inside a trap handler $LINENO is the handler's
  # OWN line, so this always reported a line inside __on_err rather than the
  # failing one -- every "install.sh died" diagnostic pointed at the same dead
  # spot. BASH_LINENO[0] is the line of the caller frame, i.e. where the failure
  # actually happened. (Cost real time chasing a phantom "line 71" during the
  # GH #731 work; install.sh is ~13k lines, so a wrong line number is worse than
  # none.) FUNCNAME[1] was already correct -- FUNCNAME[0] is __on_err itself.
  local line="${BASH_LINENO[0]:-0}"
  local func="${FUNCNAME[1]:-main}"
  printf "\033[1;31m[jabali-install]\033[0m install.sh died:\n" >&2
  printf "    exit_code : %d\n" "$rc" >&2
  printf "    function  : %s\n" "$func" >&2
  printf "    line      : %d\n" "$line" >&2
  printf "    command   : %s\n" "$cmd" >&2
  # Call trace. The handler exists to make an operator's bug report actionable,
  # and in a file this size one frame rarely is: the same helper gets called
  # from a dozen places and the interesting question is which caller. Frame 0 is
  # __on_err, so start at 1.
  if (( ${#FUNCNAME[@]} > 2 )); then
    printf "    trace     :\n" >&2
    local _i
    for (( _i = 1; _i < ${#FUNCNAME[@]} - 1 && _i <= 8; _i++ )); do
      printf "        %s() at line %s\n" "${FUNCNAME[$_i]}" "${BASH_LINENO[$((_i - 1))]}" >&2
    done
  fi
  # When the failing command was a systemctl reload/restart/start,
  # dump journalctl + status for the unit so the operator sees the
  # real reason without having to ask for log files.
  if [[ "$cmd" =~ systemctl[[:space:]]+(reload|restart|start|enable)[[:space:]]+([A-Za-z0-9._@-]+) ]]; then
    local unit="${BASH_REMATCH[2]}"
    printf "\n[diagnostic] systemctl status %s --no-pager:\n" "$unit" >&2
    systemctl status "$unit" --no-pager 2>&1 | sed 's/^/    /' >&2 || true
    printf "\n[diagnostic] journalctl -u %s -n 20 --no-pager:\n" "$unit" >&2
    journalctl -u "$unit" -n 20 --no-pager 2>&1 | sed 's/^/    /' >&2 || true
  fi
  # If the install logger has been initialised, point the operator
  # at the file so the GitHub issue starts with full context.
  if [[ -n "${LOG_FILE:-}" && -f "$LOG_FILE" ]]; then
    printf "\nFull install log at: %s\n" "$LOG_FILE" >&2
    printf "[diagnostic] last 30 lines of install log:\n" >&2
    tail -n 30 "$LOG_FILE" 2>/dev/null | sed 's/^/    /' >&2 || true
  fi
  exit "$rc"
}
trap '__on_err' ERR

# ---------- config (override via env) ---------------------------------------

REPO_URL="${JABALI_REPO_URL:-https://github.com/shukiv/jabali-panel.git}"
REPO_BRANCH="${JABALI_REPO_BRANCH:-main}"
REPO_DIR="${JABALI_REPO_DIR:-/opt/jabali-panel}"

# DNS forwarder escape hatch for restricted-network labs where outbound
# UDP/53 is blocked by the upstream firewall (common on corporate / lab
# / VirtualBox-NAT-DNS-proxy setups). When set, install.sh:
#   - masks systemd-resolved
#   - writes a plain /etc/resolv.conf with `nameserver <forwarder>` and
#     `options use-vc` so glibc forces TCP/53 (which firewalls usually
#     allow when UDP/53 is blocked)
#   - chattr +i's the file so apt postinst's `ln -sf` symlink attempts
#     are deflected (and install.sh's own resolv.conf-rewrite block is
#     short-circuited)
#   - sets pdns-recursor `forward-zones-recurse=.=<forwarder>` so the
#     recursor forwards instead of recursing to public roots over UDP
#   - skips the resolved→recursor→public chain sanity probe
# Unset (default) preserves the original behaviour: systemd-resolved
# owns /etc/resolv.conf and pdns-recursor recurses through 1.1.1.1 +
# 9.9.9.9 via UDP.
DNS_FORWARDER="${JABALI_DNS_FORWARDER:-}"
GO_VERSION="${JABALI_GO_VERSION:-1.26.5}"
# SHA-256 of the pinned Go tarballs, from https://go.dev/dl/?mode=json.
# Pinned HERE rather than in install/ because install_go runs BEFORE
# clone_or_update_repo — on a fresh install $REPO_DIR does not exist yet, so a
# pin under install/ could never be read (and the ordering guard test would
# rightly flag the attempt). install.sh itself is the bootstrap artifact and is
# sha256-verified by bootstrap.sh, so a pin here carries the same weight.
# Bump together with GO_VERSION. An unpinned version (JABALI_GO_VERSION
# override, or the CDN-gap fallback) verifies against go.dev's published
# checksum instead — see install_go.
GO_SHA256_AMD64="${JABALI_GO_SHA256_AMD64:-5c2c3b16caefa1d968a94c1daca04a7ca301a496d9b086e17ad77bb81393f053}"
GO_SHA256_ARM64="${JABALI_GO_SHA256_ARM64:-fe4789e92b1f33358680864bbe8704289e7bb5fc207d80623c308935bd696d49}"
GO_ROOT="${JABALI_GO_ROOT:-/usr/local/go}"
SERVICE_USER="${JABALI_SERVICE_USER:-jabali}"
SERVICE_NAME="${JABALI_SERVICE_NAME:-jabali-panel}"
# Default binds all interfaces. This is intentional: during development and
# testing we want the panel reachable over the LAN without needing nginx.
# In production, flip this to 127.0.0.1:8443 and put nginx in front so TLS
# termination and rate limiting happen at the proxy (blueprint §5.1).
# M25 Step 4: default bind is the Unix socket. nginx terminates TLS on
# :8443 and proxies to /run/jabali-panel/api.sock (see
# install/nginx/jabali-panel-vhost.conf.tmpl + ADR-0050). Pre-M25 the
# default was 0.0.0.0:8443 — leaving that default here meant fresh
# installs seeded config.toml + the env file with a TCP bind, and the
# Step 4 in-place migration had to sweep it back out. Keeping the
# override so operators who really need TCP (debug, split-host) can
# still set JABALI_PANEL_ADDR=127.0.0.1:8443 explicitly.
PANEL_ADDR="${JABALI_PANEL_ADDR:-unix:/run/jabali-panel/api.sock}"
BIN_PATH="/usr/local/bin/jabali-panel"
AGENT_BIN_PATH="/usr/local/bin/jabali-agent"
AGENT_SOCKET="/run/jabali/agent.sock"
AGENT_SERVICE_NAME="jabali-agent"
ENV_FILE="/etc/jabali/panel.env"

# ---------- CLI flag parsing ------------------------------------------------
#
# We support --hostname and --token as named flags, and keep the legacy
# positional arg ($1 = github token) working by deferring it until after flag
# parsing. This way `bash -s -- --hostname=foo` and the old
# `bash -s -- <TOKEN>` both do the right thing.

usage() {
  cat <<'USAGE_EOF'
Jabali Panel installer.

USAGE
  install.sh [INSTALL FLAGS]
      Install or re-run (idempotent) on a fresh Debian 13 / Ubuntu 24.04
      root shell. Typically piped:
        curl -fsSL <repo>/install.sh | bash -s -- [flags]

  install.sh --uninstall [--purge-packages] [--yes]
      Remove Jabali: jabali-* units + drop-ins, /usr/local/bin/jabali*,
      /etc/jabali*, /etc/stalwart, /var/lib/jabali-*, /opt/jabali*, AppArmor
      profiles, crowdsec/audit/nftables/sshd drop-ins, etc. Prompts before
      destroying unless --yes.

INSTALL FLAGS (all optional, combinable)
  --hostname <fqdn>    Server hostname; skips the TTY prompt.
                       (env: JABALI_HOSTNAME). --hostname=<fqdn> also works.
  --token <github>     Private-repo access token (env: JABALI_GITHUB_TOKEN).
                       Legacy: the first positional arg is also a token.
  --debug              Verbose: no spinner, stream wrapped commands,
                       set -x with line-tagged PS4 (env: JABALI_DEBUG=1).
  -y, --yes            Assume "yes" for prompts (non-interactive).

UNINSTALL FLAGS
  --dry-run            Print the module install plan (which optional modules
                       would install vs skip for JABALI_MODULES) and exit
                       without changing anything.
  --uninstall          Remove Jabali (see above). Confirms unless --yes.
  --install-module KEY Install one optional module (quota|dns|mail|security|ftp)
                       onto an already-installed host, then exit.
  --purge-packages     With --uninstall: also apt-purge the OS packages
                       Jabali pulled in (nginx, mariadb, crowdsec, php...).
  -y, --yes            Skip the uninstall confirmation prompt.

ENV EQUIVALENTS
  JABALI_HOSTNAME, JABALI_GITHUB_TOKEN, JABALI_DEBUG=1

EXAMPLES
  curl -fsSL <repo>/install.sh | bash -s -- --hostname=panel.example.com
  curl -fsSL <repo>/install.sh | bash -s -- --hostname p.ex.com --token <TOK>
  bash install.sh --uninstall                      # interactive confirm
  bash install.sh --uninstall --purge-packages --yes
  bash install.sh --help
USAGE_EOF
}

_cli_hostname=""
_cli_token=""
_cli_debug=""
_cli_uninstall=""
_cli_dry_run=""
_cli_yes=""
_cli_purge_packages=""
_cli_install_module=""
_positional=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --hostname=*) _cli_hostname="${1#*=}"; shift ;;
    --hostname)   _cli_hostname="${2:-}"; shift 2 ;;
    --token=*)    _cli_token="${1#*=}"; shift ;;
    --token)      _cli_token="${2:-}"; shift 2 ;;
    --debug)      _cli_debug=1; shift ;;
    --uninstall)  _cli_uninstall=1; shift ;;
    --purge-packages) _cli_purge_packages=1; shift ;;
    --install-module=*) _cli_install_module="${1#*=}"; shift ;;
    --install-module)   _cli_install_module="${2:-}"; shift 2 ;;
    --yes|-y)     _cli_yes=1; shift ;;
    --dry-run)    _cli_dry_run=1; shift ;;
    -h|--help)    usage; exit 0 ;;
    --)           shift; while [[ $# -gt 0 ]]; do _positional+=("$1"); shift; done ;;
    --*)          printf 'install.sh: unknown flag: %s\n' "$1" >&2; exit 64 ;;
    *)            _positional+=("$1"); shift ;;
  esac
done

# --hostname CLI arg wins over JABALI_HOSTNAME env; re-export so downstream
# functions (notably prompt_server_settings) pick it up via the same env var.
if [[ -n "$_cli_hostname" ]]; then
  JABALI_HOSTNAME="$_cli_hostname"
  export JABALI_HOSTNAME
fi

# --token precedence: CLI flag > JABALI_GITHUB_TOKEN env (JABALI_GITEA_TOKEN = deprecated alias) > legacy positional.
REPO_TOKEN="${_cli_token:-${JABALI_GITHUB_TOKEN:-${JABALI_GITEA_TOKEN:-${_positional[0]:-}}}}"

# --debug: CLI flag > JABALI_DEBUG env. Exported so any sub-shell scripts
# this installer invokes can honour it too. When set, _spin below skips
# the spinner + log-capture and streams stdout+stderr live, AND we enable
# `set -x` with a PS4 that tags every trace line with the source file +
# line number + function so the last trace line before a stall names
# exactly what command is waiting.
JABALI_DEBUG="${_cli_debug:-${JABALI_DEBUG:-}}"
export JABALI_DEBUG

# Per-run log file — always on, independent of --debug. Captures every
# _log/_ok/_warn/_err/_die line AND every _spin wrapped command's output,
# so a post-mortem after install stalls or errors doesn't depend on
# scrollback. Filename is timestamped so reruns don't clobber. Lives in
# /root because install.sh already requires root (preflight() asserts
# EUID==0); if touch fails (fallback for weird jails / testing) we try
# /tmp and emit a warning later. Mode 0600 — the log may echo hostnames,
# IPs, and package lists but never secrets (we redact/avoid passwords).
# Canonical log dir for everything jabali writes locally. logrotate
# drop-in (install/logrotate/jabali) rotates /var/log/jabali/*.log
# weekly, keeps 8. Falls back to /tmp on a weird jail where /var/log
# isn't writable so we still get a post-mortem trail.
LOG_DIR="/var/log/jabali"
mkdir -p "$LOG_DIR" 2>/dev/null || true
chmod 0750 "$LOG_DIR" 2>/dev/null || true
LOG_FILE="$LOG_DIR/install-$(date +%Y-%m-%d_%H-%M-%S).log"
if ! touch "$LOG_FILE" 2>/dev/null; then
  LOG_FILE="/tmp/jabali_install-$(date +%Y-%m-%d_%H-%M-%S).log"
  touch "$LOG_FILE" 2>/dev/null || LOG_FILE=""
fi
if [[ -n "$LOG_FILE" ]]; then
  chmod 0600 "$LOG_FILE" 2>/dev/null || true
  # Keep a stable "latest" symlink so docs/runbooks can reference
  # /var/log/jabali/install.log without a timestamp.
  if [[ "$LOG_FILE" == "$LOG_DIR/"* ]]; then
    ln -sfn "$(basename "$LOG_FILE")" "$LOG_DIR/install.log" 2>/dev/null || true
  fi
fi

if [[ -n "$JABALI_DEBUG" ]]; then
  # Dim + line-tagged so xtrace output is visually distinct from the
  # _log/_ok/etc. column. ${FUNCNAME[0]:-main} names the caller function;
  # 'main' when xtrace fires at top level.
  #
  # Script name is hardcoded "install.sh" rather than
  # ${BASH_SOURCE##*/} because under `curl | bash`, BASH_SOURCE is
  # unset (bash reads the script from stdin, no filename), and pattern-
  # substitution on an unset array under `set -u` errors mid-xtrace
  # with "BASH_SOURCE: unbound variable" on the NEXT statement after
  # `set -x`. The `${VAR:-default}` trick doesn't help here because
  # `##*/` and `:-` can't compose in one expansion.
  export PS4='+ install.sh:${LINENO}:${FUNCNAME[0]:-main}() '
  # Route xtrace to the log file only — NOT to terminal-stderr — so the
  # operator's screen stays readable (banners, prompts, _log/_ok lines
  # uninterrupted) while every shell command is still captured for
  # post-mortem. Operator who wants a live trace runs
  # `tail -f /var/log/jabali/install.log` in another shell.
  # ${LOG_FILE:-/dev/stderr} keeps the old "trace to stderr" behaviour
  # only when the log dir couldn't be created (the fallback case in the
  # block above), so --debug stays useful even in fully broken envs.
  if [[ -n "${LOG_FILE:-}" ]]; then
    exec {_XTRACE_FD}>>"$LOG_FILE"
  else
    exec {_XTRACE_FD}>&2
  fi
  export BASH_XTRACEFD=$_XTRACE_FD
  set -x
fi

# ---------- tiny logger -----------------------------------------------------

# _log_to_file mirrors every logger line into $LOG_FILE with an ISO
# timestamp prefix — so a grep through the log after an install can
# reconstruct the timing. No-op when LOG_FILE is empty (touch failed
# during early bootstrap on a non-root/weird jail). Uses plain echo
# redirection rather than `tee` to avoid spawning per-line.
_log_to_file() {
  [[ -n "${LOG_FILE:-}" ]] || return 0
  printf '%s %s\n' "$(date -Iseconds)" "$*" >> "$LOG_FILE" 2>/dev/null || true
}
_log()  { printf '\033[1;34m[i]\033[0m %s\n' "$*"; _log_to_file "[i] $*"; }
_ok()   { printf '\033[1;32m[✓]\033[0m %s\n' "$*"; _log_to_file "[✓] $*"; }
_warn() { printf '\033[1;33m[!]\033[0m %s\n' "$*" >&2; _log_to_file "[!] $*"; }
# _err prints in red on stderr — callers still control exit behavior.
# M18's configure_disk_quota relied on this silently; define it once
# so any future caller has a matching pair to _warn.
_err()  { printf '\033[1;31m[✗]\033[0m %s\n' "$*" >&2; _log_to_file "[✗] $*"; }
_die()  { printf '\033[1;31m[✗]\033[0m %s\n' "$*" >&2; _log_to_file "[✗] $*"; exit 1; }

# is_module_enabled — M353 (GH #353) modular install. Returns 0 (enabled) when
# the given optional-module key should be installed. JABALI_MODULES is a comma
# list emitted by the TUI (installer/) or set for a headless install.
#   - UNSET entirely  → every module on (backward-compatible default: plain
#     curl|bash, CI, cloud-init, jabali update).
#   - SET but EMPTY   → the operator chose a minimal install (TUI "Minimal")
#     → NO optional modules.
# ${JABALI_MODULES+x} expands to "x" only when the var is set (even if empty),
# so this distinguishes unset from set-but-empty. Core modules (nginx, MariaDB,
# panel, Kratos) are never gated by this.
is_module_enabled() {
  local key="$1"
  [[ -z "${JABALI_MODULES+x}" ]] && return 0
  case ",${JABALI_MODULES}," in
    *",${key},"*) return 0 ;;
    *) return 1 ;;
  esac
}

# apply_default_modules — GH #760. A plain `curl | bash` install (no TUI, no
# explicit JABALI_MODULES) historically installed EVERYTHING (the "full"
# profile), giving a ~2.5 GB baseline including the mail stack (Stalwart +
# Bulwark + Node) many operators don't want. Default a FRESH install to the
# "webhost" profile (dns + security + quota) so mail / docker / python_apps /
# postgres / api are opt-in.
#
# Only fires when JABALI_MODULES is UNSET (the curl|bash default). It does NOT
# touch the two other cases or the update path:
#   - SET (TUI or headless)      → operator's explicit selection wins.
#   - SET-but-EMPTY (Minimal)    → stays minimal.
#   - `jabali update`            → never runs main() (it sources specific
#     functions), so the is_module_enabled "unset = all-on" contract that
#     update relies on (GH #727) is untouched.
# Called at the very top of main() so --dry-run reflects the real default too.
# Keep the key list in sync with the "webhost" profile in
# installer/internal/modules/profiles.go.
apply_default_modules() {
  if [[ -z "${JABALI_MODULES+x}" ]]; then
    export JABALI_MODULES="dns,security,quota"
    _log "no module selection (JABALI_MODULES unset) → defaulting to the 'webhost' profile: ${JABALI_MODULES}. mail/docker/python_apps/postgres/api are opt-in — pass JABALI_MODULES=dns,mail,security,… or use the installer TUI to choose."
  fi
}

# run_if_module <module-key> <install-fn> [args…] — run the install function only
# when its module is enabled (see is_module_enabled), else log a skip. Used to
# gate the OPTIONAL install steps in the fresh-install flow. On `jabali update`
# JABALI_MODULES is unset, so every module reads enabled and the update path is
# unchanged.
run_if_module() {
  local key="$1"; shift
  if is_module_enabled "$key"; then
    "$@"
  else
    _log "module '${key}' disabled (JABALI_MODULES) — skipping ${1}"
  fi
}

# mail_module_active — is the mail stack actually meant to run on THIS host?
# GH #727: run_if_module reads JABALI_MODULES, which is UNSET on `jabali update`
# and therefore treats every module as enabled. On a host installed WITHOUT mail
# (e.g. the Basic profile) that made `jabali update` re-run the Stalwart
# bootstrap, which died in _install_spam_rules at `install -g jabali-mail`
# because the jabali-mail group never existed. The persisted truth on an
# installed host is server_settings.mail_enabled (seed_module_flags wrote it
# from the install selection); consult that instead. Fall back to the module
# selection only when the DB has no opinion yet (fresh install before the
# panel's server_settings row exists — where seed_module_flags has just made DB
# truth and the selection agree anyway).
mail_module_active() {
  local db_val=""
  if command -v mariadb >/dev/null 2>&1; then
    # `|| true` mirrors converge_pdns_masking: on a fresh install the
    # server_settings table may not exist yet, and a bare command-substitution
    # failure under `set -e` + the err trap would kill the installer silently.
    db_val="$(mariadb jabali_panel -N -B -e \
      "SELECT mail_enabled FROM server_settings WHERE id=1;" 2>/dev/null || true)"
  fi
  if [[ -n "$db_val" ]]; then
    [[ "$db_val" == "0" ]] && return 1
    return 0
  fi
  is_module_enabled mail
}

# run_if_mail <install-fn> [args…] — like run_if_module mail, but gated on the
# host's actual mail state (mail_module_active) so `jabali update` on a no-mail
# host does not re-run the mail bootstrap (GH #727).
run_if_mail() {
  if mail_module_active; then
    "$@"
  else
    _log "mail module not active (server_settings.mail_enabled=0) — skipping ${1}"
  fi
}

# install_module <key> — M353 runtime module install. Runs ONLY the given
# module's install functions on an ALREADY-INSTALLED host (Server Settings ->
# Modules toggles this via the agent: `install.sh --install-module <key>`). It
# does NOT run the core install path — the panel/nginx/mariadb are already here.
#
# SAFETY: each install_module_<key> below must be standalone-runnable +
# idempotent. Only modules whose functions have been AUDITED for that are wired
# here (all optional modules: quota, dns, mail, security). dns + mail use
# reconstruct_server_env_from_db below to repopulate the identity env from the
# server_settings DB row (main() gets it from prompts). mail additionally
# requires the dns module (pdns self-zone), asserted at the top of
# install_module_mail. security needs no identity env (server-wide) and no
# cross-module dependency.
install_module() {
  local key="${1:-}"
  # Refuse to run if the panel isn't installed — this mode adds a module to an
  # existing host, never bootstraps one.
  if [[ ! -d "${REPO_DIR:-/opt/jabali-panel}" ]]; then
    _die "install.sh --install-module: no panel install found at ${REPO_DIR:-/opt/jabali-panel}; run a full install first"
  fi
  case "$key" in
    quota)    install_module_quota ;;
    dns)      install_module_dns ;;
    mail)     install_module_mail ;;
    security) install_module_security ;;
    ftp)      install_module_ftp ;;
    *)        _die "install.sh --install-module: unknown module '$key' (want: quota|dns|mail|security|ftp)" ;;
  esac
  _ok "module '$key' install complete"
}

# install_module_quota — configure_disk_quota is standalone-safe: it probes the
# /home filesystem, has no server-settings env dependency, is idempotent, and
# returns 0 gracefully on a fs that can't do POSIX quota. Keep this list in sync
# with main()'s `run_if_module quota …` calls.
install_module_quota() {
  configure_disk_quota
}

# reconstruct_server_env_from_db — repopulate the JABALI_SRV_* identity vars from
# the server_settings DB row (the source of truth: panel-api seeds it from
# config.toml on first boot, then owns it). main() gets these from
# prompt_server_settings; a runtime `--install-module` invocation on an
# already-installed host has no prompts, so dns/mail config functions that read
# these vars (bootstrap_pdns_self_zone, install_panel_primary_domain, …) would
# otherwise see them empty. Only sets a var when it is currently unset/empty, so
# it never overrides values main() already exported.
reconstruct_server_env_from_db() {
  command -v mariadb >/dev/null 2>&1 || return 0
  local row
  row="$(mariadb jabali_panel -Ns -e \
    "SELECT hostname, public_ipv4, public_ipv6, ns1_name, ns1_ipv4, ns2_name, ns2_ipv4 \
     FROM server_settings WHERE id=1;" 2>/dev/null || true)"
  [[ -z "$row" ]] && return 0
  local h i4 i6 n1 n1i n2 n2i
  IFS=$'\t' read -r h i4 i6 n1 n1i n2 n2i <<<"$row"
  : "${JABALI_SRV_HOSTNAME:=$h}"
  : "${JABALI_SRV_IPV4:=$i4}"
  : "${JABALI_SRV_IPV6:=$i6}"
  : "${JABALI_SRV_NS1_NAME:=$n1}"
  : "${JABALI_SRV_NS1_IPV4:=$n1i}"
  : "${JABALI_SRV_NS2_NAME:=$n2}"
  : "${JABALI_SRV_NS2_IPV4:=$n2i}"
  export JABALI_SRV_HOSTNAME JABALI_SRV_IPV4 JABALI_SRV_IPV6 \
         JABALI_SRV_NS1_NAME JABALI_SRV_NS1_IPV4 JABALI_SRV_NS2_NAME JABALI_SRV_NS2_IPV4
}

# ---------- policy-rc.d guard ------------------------------------------------
# dpkg's start-suppression shim (/usr/sbin/policy-rc.d, exit 101) is written
# around apt batches so package postinsts don't auto-start half-configured
# services. A LEAKED shim is a host-wide outage in slow motion: every
# invoke-rc.d service action is denied SILENTLY (invoke-rc.d exits 0), so
# nginx's logrotate postrotate never signals a log reopen and every vhost
# access log flatlines at 0 bytes until some unrelated reload. Found on
# jabalitests 2026-08-12: 23 vhost logs empty, the shim months old.
#
# The old inline mv/rm pairs restored the shim only on the happy path: _die,
# _spin's `exit`, and the ERR trap all exit(1) mid-batch without reaching the
# restore lines placed after the apt call. These helpers arm an EXIT trap
# instead, so the shim is removed on EVERY exit path.
_POLICY_RC_MARKER="jabali-panel install.sh"
_policy_rc_path=/usr/sbin/policy-rc.d
_policy_rc_armed=0
_policy_rc_had_foreign=0

_policy_rc_restore() {
  [[ "$_policy_rc_armed" == "1" ]] || return 0
  rm -f "$_policy_rc_path"
  if [[ "$_policy_rc_had_foreign" == "1" && -e "${_policy_rc_path}.jabali-bak" ]]; then
    mv "${_policy_rc_path}.jabali-bak" "$_policy_rc_path"
  else
    rm -f "${_policy_rc_path}.jabali-bak"
  fi
  _policy_rc_armed=0
  _policy_rc_had_foreign=0
  trap - EXIT
}

_policy_rc_install() {
  # $1 — short "why" note embedded in the shim for anyone who finds it live.
  local why="${1:-package batch}"
  if [[ -e "$_policy_rc_path" ]]; then
    if grep -q "$_POLICY_RC_MARKER" "$_policy_rc_path" 2>/dev/null; then
      # Our own shim leaked from an earlier run — replace it, don't preserve
      # it as an "operator original". That earlier run may itself have
      # displaced a real operator file into .jabali-bak before dying: keep
      # a foreign backup for restore, discard a jabali-authored one.
      rm -f "$_policy_rc_path"
      if [[ -e "${_policy_rc_path}.jabali-bak" ]]; then
        if grep -q "$_POLICY_RC_MARKER" "${_policy_rc_path}.jabali-bak" 2>/dev/null; then
          rm -f "${_policy_rc_path}.jabali-bak"
        else
          _policy_rc_had_foreign=1
        fi
      fi
    else
      _policy_rc_had_foreign=1
      mv "$_policy_rc_path" "${_policy_rc_path}.jabali-bak"
    fi
  fi
  cat > "$_policy_rc_path" <<POLICYEOF
#!/bin/sh
# TEMPORARY deny-all shim written by ${_POLICY_RC_MARKER} (${why}).
# Suppresses dpkg service auto-starts during a package batch; install.sh
# removes it on every exit path. If no install is currently running, this
# file has leaked — delete it: it silently denies ALL invoke-rc.d service
# actions host-wide (including nginx's logrotate reopen).
exit 101
POLICYEOF
  chmod 0755 "$_policy_rc_path"
  _policy_rc_armed=1
  trap '_policy_rc_restore' EXIT
}

# sweep_leaked_policy_rcd — heal a host where an earlier install.sh died
# mid-batch (before the EXIT-trap fix) and left its policy-rc.d behind.
# Runs from provision_new_software so every `jabali update` sweeps it.
# Only touches files carrying our marker — an operator's own policy-rc.d
# is none of our business.
sweep_leaked_policy_rcd() {
  local f=/usr/sbin/policy-rc.d
  if [[ -f "$f" ]] && grep -q "$_POLICY_RC_MARKER" "$f" 2>/dev/null; then
    # Age-gate: a young shim belongs to a concurrently-running install.sh
    # (agent-triggered --install-module runs re-enter this script). Real
    # batches live minutes; only treat an hour-old shim as leaked.
    if [[ -n "$(find "$f" -mmin +60 2>/dev/null)" ]]; then
      _warn "leaked policy-rc.d found (denies ALL invoke-rc.d service actions) — removing"
      rm -f "$f"
      if [[ -f "${f}.jabali-bak" ]]; then
        if grep -q "$_POLICY_RC_MARKER" "${f}.jabali-bak" 2>/dev/null; then
          rm -f "${f}.jabali-bak"
        else
          _log "restoring operator policy-rc.d displaced by the leaked run"
          mv "${f}.jabali-bak" "$f"
        fi
      fi
      # nginx has likely been writing to rotated .1 files since the leak
      # (logrotate's `invoke-rc.d nginx rotate` was denied) — reopen its
      # logs now rather than waiting for the next full reload.
      if systemctl is-active --quiet nginx 2>/dev/null; then
        nginx -s reopen 2>/dev/null || true
        _ok "nginx signalled to reopen logs (leaked policy-rc.d had blocked logrotate)"
      fi
    fi
  fi
}

# _install_dns_packages — apt-install the PowerDNS packages on a host that didn't
# get them in the base batch (a --minimal install). The pdns postinst would try
# to start pdns before its MySQL backend is wired, so a policy-rc.d start-
# suppression shim is dropped for the duration (identical to install_base_packages).
# CRITICAL: the shim must not outlive this function — a leftover `exit 101`
# policy-rc.d blocks ALL service starts host-wide. The _policy_rc_* guard
# owns that via an EXIT trap; we still restore explicitly on the happy path.
_install_dns_packages() {
  _log "installing PowerDNS packages (dns module runtime install)"
  _policy_rc_install "--install-module dns"

  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq || _warn "apt update failed; proceeding with cached package lists"
  local _rc=0
  apt-get install -y -qq --no-install-recommends \
    pdns-server pdns-backend-mysql pdns-recursor bind9-dnsutils || _rc=$?

  # Restore policy-rc.d before any exit path (the EXIT trap also covers
  # _die and set -e exits).
  _policy_rc_restore

  [[ $_rc -eq 0 ]] || _die "apt install of PowerDNS packages failed (exit $_rc)"

  # pdns-server postinst creates the `pdns` group; the config fns chown to it.
  if ! getent group pdns >/dev/null; then
    _die "pdns group missing after apt-install — pdns-server postinst failed; run 'apt-get install -f'"
  fi
  _ok "PowerDNS packages installed"
}

# install_module_dns — runtime install of the DNS module on an already-installed
# host. Standalone equivalent of main()'s `run_if_module dns …` block, which
# assumes install_base_packages already apt-installed the pdns packages and
# prompt_server_settings already exported the identity env. Here we reconstruct
# that env from the DB and apt-install the packages ourselves, then run the SAME
# three config functions main() runs. Those functions own service convergence
# (enable + restart-on-change + start-if-inactive, no needless bounce), so this
# is idempotent: on a converged host it is a fast no-op; on the reported
# "DNS On / pdns inactive" host it installs + starts pdns.
install_module_dns() {
  reconstruct_server_env_from_db
  if ! dpkg -s pdns-server        >/dev/null 2>&1 \
     || ! dpkg -s pdns-backend-mysql >/dev/null 2>&1 \
     || ! dpkg -s pdns-recursor    >/dev/null 2>&1; then
    _install_dns_packages
  fi
  install_powerdns
  bootstrap_pdns_self_zone
  install_pdns_recursor
}

# install_module_mail — runtime install of the mail module (Stalwart + Bulwark)
# on an already-installed host. Standalone equivalent of main()'s mail block.
# No apt / policy-rc.d: Stalwart is a pinned GitHub-release binary (install_stalwart
# skips re-download when the installed --version already matches) and Bulwark is a
# Node app on the core nodejs.
#
# HARD dependency on the dns module: install_panel_primary_domain FK-asserts the
# pdns self-zone, and the whole mail stack needs the jabali_pdns database that
# install_powerdns creates — so mail-on/dns-off is incoherent. We assert the
# self-zone up front (before any mail work) so an unmet dependency fails clean
# with NO partial install, rather than dying halfway through install_stalwart.
install_module_mail() {
  reconstruct_server_env_from_db
  if [[ -z "${JABALI_SRV_HOSTNAME:-}" ]]; then
    _die "install.sh --install-module mail: panel hostname unknown (server_settings not populated); cannot configure the primary mail domain"
  fi
  # dns precondition: the pdns self-zone row for the panel hostname must exist.
  # The query tolerates the jabali_pdns DB being absent entirely (dns never
  # installed) — any error/empty result => treat as "dns not ready".
  local self_zone
  self_zone="$(mariadb jabali_pdns -Ns -e \
    "SELECT id FROM domains WHERE name='$(_sql_escape "$JABALI_SRV_HOSTNAME")';" 2>/dev/null || true)"
  if [[ -z "$self_zone" ]]; then
    _die "install.sh --install-module mail: the DNS module must be installed first (pdns self-zone for '$JABALI_SRV_HOSTNAME' not found). Enable DNS, then mail."
  fi

  install_stalwart
  install_stalwart_apply
  install_jabali_mailhook
  # install_panel_primary_domain is `run_if_module dns` in main() (it writes a
  # DNS zone), but at mail-install time we deliberately run it here: it needs
  # Stalwart ready (the reconciler fires a domain-add) and the mail stack is
  # useless without the panel's own domain mail-enabled. dns is asserted present
  # above, so the self-zone FK holds.
  install_panel_primary_domain
  install_bulwark
}

# install_module_security — runtime install of the security module (CrowdSec +
# AppSec + nginx bouncer + login-allowlist + jabali scenarios + blocklists +
# malware/ClamAV/YARA stack + UFW + AppArmor + AIDE) on an already-installed
# host. Standalone equivalent of main()'s `run_if_module security …` block, minus
# the intervening non-security steps (cleanup_modsecurity, clone_or_update_repo,
# etc. — already done on an installed host). No hostname/server env dependency
# (security is server-wide) and no cross-module dependency. Each fn apt-installs
# its own packages (guarded by dpkg -s); those services are safe to auto-start,
# so NO policy-rc.d shim is needed (unlike dns). install_crowdsec self-adds the
# packagecloud apt source and MUST run first (install_crowdsec_appsec renders
# config that needs the crowdsec binary — GH#109 ordering scar). appsec runs ONCE
# here (main() calls it twice only because a fresh install has no panel binary
# until build_backend; at runtime /usr/local/bin/jabali-panel already exists).
# install_apparmor / install_aide read profile/unit files from $REPO_DIR/install/
# which is present on an installed host.
#
# Idempotent: guarded package installs, soft (|| _warn) hub/blocklist/malware
# downloads, `ufw allow` is idempotent and `ufw --force enable` runs LAST +
# interrupt-safe (default-deny isn't enforced until enable), `aide --init` is
# backgrounded (nohup). SAFETY: install_ufw enables a default-deny firewall — the
# allow-list (22/80/443/8443/25/465/587/993/995/4190/53) covers the panel + mail
# + dns listeners and 22 is hard-asserted, so a --minimal host converging under
# the reconciler cannot lock itself out.
#
# CONVERGENCE COMPLETENESS (deliberate): system.module.status probes crowdsec as
# the security marker. A run that installs crowdsec then hard-fails a later fn
# would report installed=true and the convergence pass would stop re-dispatching,
# so full-stack completeness relies on this function running to completion. The
# fns are crowdsec-first and mostly soft-failing, so one successful run installs
# the whole stack; a hard mid-sequence failure surfaces in the agent log + is
# retryable from the operator toggle.
install_module_security() {
  install_crowdsec
  configure_crowdsec_mariadb
  install_crowdsec_appsec
  install_crowdsec_nginx_bouncer
  install_crowdsec_profiles
  install_login_allowlist_default_conf
  install_crowdsec_jabali_scenarios
  install_crowdsec_jabali_stalwart_scenarios
  install_crowdsec_jabali_kratos_scenarios
  install_crowdsec_blocklists
  install_malware_stack
  install_ufw
  install_apparmor
  install_aide
}

# install_module_ftp — runtime install of the FTP module (vsftpd), GH #1053.
# STRICTLY opt-in: server_settings.ftp_enabled defaults 0 and nothing here
# runs until the admin flips it (panel PATCH dispatches --install-module ftp;
# the module reconciler converges hosts where the flag is on but vsftpd
# isn't). SFTP subaccounts work without this module.
#
# Auth model: PAM service vsftpd-jabali (pam_service_name below) gates
# logins on jabali-ftp group membership. The file deliberately OMITS
# pam_shells.so — subaccounts ship shell=/usr/sbin/nologin (step-3 review:
# blocks the no-Match-block ssh shell path) and pam_shells would reject
# them. Do NOT "fix" that by adding nologin to /etc/shells: that widens
# every other pam_shells consumer on the host.
install_module_ftp() {
  # jabali-ftp is the PAM allowlist group; the agent also creates it on
  # demand (first ftp_access grant can precede the module install).
  if ! getent group jabali-ftp >/dev/null; then
    groupadd --system jabali-ftp
    _ok "jabali-ftp system group created"
  fi

  if ! dpkg -s vsftpd >/dev/null 2>&1; then
    _log "installing vsftpd (ftp module runtime install)"
    _policy_rc_install "--install-module ftp"
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq || _warn "apt update failed; proceeding with cached package lists"
    local _rc=0
    apt-get install -y -qq --no-install-recommends vsftpd || _rc=$?
    _policy_rc_restore
    [[ $_rc -eq 0 ]] || _die "apt install of vsftpd failed (exit $_rc)"
  fi

  install_vsftpd_config
  install_vsftpd_pam
  install_ftp_firewall_rules
  install_ftp_crowdsec

  systemctl unmask vsftpd >/dev/null 2>&1 || true
  systemctl enable vsftpd >/dev/null 2>&1 || true
  if systemctl is-active --quiet vsftpd; then
    systemctl restart vsftpd || _die "vsftpd restart failed — check 'journalctl -u vsftpd'"
  else
    systemctl start vsftpd || _die "vsftpd start failed — check 'journalctl -u vsftpd'"
  fi
  # `systemctl start` returning 0 is NOT proof of life: vsftpd exits
  # status 2 in ~10ms on any unknown config directive with NO stderr, and
  # systemd has already reported the start as successful by then (caught
  # live: the invented ssl_tlsv1_2 option). Settle, then require active.
  sleep 1
  systemctl is-active --quiet vsftpd \
    || _die "vsftpd died right after start (usually a bad /etc/vsftpd.conf directive) — check 'journalctl -u vsftpd' and 'vsftpd /etc/vsftpd.conf'"
  _ok "ftp module installed (vsftpd active, FTPS required unless ftp_allow_plaintext)"
}

# install_vsftpd_config — render /etc/vsftpd.conf from DB truth. Reads
# ftp_allow_plaintext + ftp_pasv_address from server_settings; both default
# to the safe value when the row/columns are unreadable (fresh install
# ordering — same tolerance as converge_pdns_masking).
install_vsftpd_config() {
  local allow_plaintext="0" pasv_address="" max_clients="50" max_per_ip="8" max_rate_kbs="0"
  if command -v mariadb >/dev/null 2>&1; then
    allow_plaintext="$(mariadb jabali_panel -N -B -e \
      "SELECT ftp_allow_plaintext FROM server_settings WHERE id=1;" 2>/dev/null || true)"
    pasv_address="$(mariadb jabali_panel -N -B -e \
      "SELECT ftp_pasv_address FROM server_settings WHERE id=1;" 2>/dev/null || true)"
    # Tuning knobs (GH #1053 follow-up); empty (pre-000265 box) keeps the
    # shipped defaults above. 0 = unlimited for all three.
    local _v
    _v="$(mariadb jabali_panel -N -B -e \
      "SELECT ftp_max_clients FROM server_settings WHERE id=1;" 2>/dev/null || true)"
    [[ "$_v" =~ ^[0-9]+$ ]] && max_clients="$_v"
    _v="$(mariadb jabali_panel -N -B -e \
      "SELECT ftp_max_per_ip FROM server_settings WHERE id=1;" 2>/dev/null || true)"
    [[ "$_v" =~ ^[0-9]+$ ]] && max_per_ip="$_v"
    _v="$(mariadb jabali_panel -N -B -e \
      "SELECT ftp_local_max_rate_kbs FROM server_settings WHERE id=1;" 2>/dev/null || true)"
    [[ "$_v" =~ ^[0-9]+$ ]] && max_rate_kbs="$_v"
  fi
  local force_ssl="YES"
  [[ "$allow_plaintext" == "1" ]] && force_ssl="NO"

  # TLS: the panel-hostname cert (LE when routable, else self-signed —
  # M32). FTPS has no workable SNI story across clients; docs point
  # tenants at the panel hostname, not their own domain.
  local tls_cert="/etc/jabali/tls/panel.crt"
  local tls_key="/etc/jabali/tls/panel.key"
  local ssl_enable="YES"
  if [[ ! -s "$tls_cert" || ! -s "$tls_key" ]]; then
    # No cert at all (pre-cert install ordering): keep the daemon usable
    # but only if the operator explicitly allowed plaintext; otherwise
    # fail loudly — silently serving passwordful cleartext FTP because a
    # cert file was missing is exactly the downgrade we never ship.
    if [[ "$force_ssl" == "YES" ]]; then
      # Fail closed (JAB-260): if vsftpd is CURRENTLY serving, an operator just
      # tightened plaintext->TLS but no cert exists yet — stop + mask it rather
      # than leave the old plaintext config running and accepting cleartext
      # credentials. Self-healing: ftp_enabled is still on, so the reconciler's
      # convergeModule re-runs this install once the panel cert lands -> the
      # cert check passes, vsftpd is unmasked and started with TLS. A fresh
      # (inactive) install has nothing serving, so it just dies here as before.
      if systemctl is-active --quiet vsftpd 2>/dev/null; then
        systemctl stop vsftpd >/dev/null 2>&1 || true
        systemctl mask vsftpd >/dev/null 2>&1 || true
      fi
      _die "ftp module: TLS is required but $tls_cert is missing — run the panel-cert step first or (explicitly) allow plaintext"
    fi
    ssl_enable="NO"
    _warn "ftp module: no panel cert — FTPS disabled, plaintext explicitly allowed by operator"
  fi

  cat > /etc/vsftpd.conf <<VSFTPDEOF
# Managed by jabali-panel install.sh (ftp module, GH #1053). Do not edit —
# rewritten on every module install/update from server_settings.
listen=YES
listen_ipv6=NO
anonymous_enable=NO
local_enable=YES
# JAB-264: 0007 strips OTHER read/traverse bits from uploads (files 0660, dirs
# 0770), matching the cross-tenant privacy model (ADR-0030). The group bit is
# preserved so nginx (www-data on a setgid docroot) still reads served content;
# OTHER local accounts get nothing.
local_umask=0007
dirmessage_enable=NO
use_localtime=YES
xferlog_enable=YES
xferlog_std_format=NO
log_ftp_protocol=NO
vsftpd_log_file=/var/log/vsftpd.log
connect_from_port_20=YES
idle_session_timeout=300
data_connection_timeout=120
pam_service_name=vsftpd-jabali
# JAB-263 phase D: open a PAM session so the vsftpd-jabali `session` stack runs
# — that is what fires jabali-ftp-session-cgroup to place the worker in the
# tenant slice. vsftpd defaults session_support=NO and then SKIPS the session
# stack entirely, so the hook never runs without this.
session_support=YES
# Every login chroots to the passwd home (the subaccount home_path). The
# home is tenant-writable by design, hence allow_writeable_chroot — the
# accounts are file-transfer-only aliases, not shell users.
chroot_local_user=YES
allow_writeable_chroot=YES
hide_ids=YES
# Passive range must match install_ftp_firewall_rules + the runbook.
pasv_enable=YES
pasv_min_port=40000
pasv_max_port=40100
# Operator tuning (Server Settings -> SSH & FTP); 0 = unlimited.
max_clients=${max_clients}
max_per_ip=${max_per_ip}
VSFTPDEOF
  # Appended via printf, not the heredoc: a line-leading `write_enable`
  # token trips lint-install-sh's phantom-function scan (write_ prefix).
  printf 'write_enable=YES\n' >> /etc/vsftpd.conf
  # local_max_rate is bytes/sec; the panel stores KB/s.
  printf 'local_max_rate=%s\n' "$(( max_rate_kbs * 1024 ))" >> /etc/vsftpd.conf
  if [[ -n "$pasv_address" ]]; then
    printf 'pasv_address=%s\n' "$pasv_address" >> /etc/vsftpd.conf
  fi
  if [[ "$ssl_enable" == "YES" ]]; then
    # Protocol floor: NO ssl_tlsv1_2/ssl_tlsv1_3 directives — Debian 13's
    # vsftpd exits status 2 (silently) on those option names; proven live
    # on jabalitests 2026-08-15. ssl_tlsv1/sslv2/sslv3 are the supported
    # knobs, and OpenSSL 3's system security level already floors real
    # negotiation at TLS 1.2.
    cat >> /etc/vsftpd.conf <<VSFTPDEOF
ssl_enable=YES
rsa_cert_file=${tls_cert}
rsa_private_key_file=${tls_key}
force_local_logins_ssl=${force_ssl}
force_local_data_ssl=${force_ssl}
ssl_tlsv1=YES
ssl_sslv2=NO
ssl_sslv3=NO
# JAB-270: require the TLS close_notify before treating an upload as complete.
# vsftpd's default (strict_ssl_read_eof=NO) accepts a data stream terminated by
# a forged/plain TCP FIN, so an on-path attacker can truncate an uploaded file
# (application code, config, deploy artifact) and the server publishes the
# partial as a successful upload. YES fails the transfer closed instead.
strict_ssl_read_eof=YES
# JAB-257: require the TLS data connection to prove it knows the control
# channel's master secret (vsftpd's secure default). require_ssl_reuse=NO
# let an attacker sharing the victim's NAT race an unrelated TLS session
# onto the small passive range (40000-40100) and hijack the transfer.
# YES is correct for FTPS clients that reuse the control session's TLS
# (lftp, FileZilla, WinSCP, modern curl); a client that cannot is a
# deliberate operator downgrade, not the shipped default.
require_ssl_reuse=YES
ssl_ciphers=HIGH
VSFTPDEOF
  else
    printf 'ssl_enable=NO\n' >> /etc/vsftpd.conf
  fi
  chmod 0644 /etc/vsftpd.conf
  _ok "vsftpd.conf rendered (force_ssl=${force_ssl}, pasv_address='${pasv_address:-auto}', max_clients=${max_clients}, max_per_ip=${max_per_ip}, rate=${max_rate_kbs}KB/s)"
}

# install_vsftpd_pam — own PAM service so the stock /etc/pam.d/vsftpd
# conffile stays untouched (no dpkg conffile prompts on upgrade). Gates on
# jabali-ftp membership; NO pam_shells (see install_module_ftp header).
install_vsftpd_pam() {
  # JAB-263 phase D: the PAM session hook that places the FTPS worker in the
  # owning tenant's cgroup slice.
  install -d -m 0755 /usr/local/libexec/jabali
  install -m 0755 "$REPO_DIR/install/ftp/jabali-ftp-session-cgroup" \
    /usr/local/libexec/jabali/jabali-ftp-session-cgroup
  cat > /etc/pam.d/vsftpd-jabali <<'PAMEOF'
# Managed by jabali-panel install.sh (ftp module, GH #1053).
# Only members of jabali-ftp may authenticate; membership is the
# ftp_access toggle. No pam_shells: subaccounts use /usr/sbin/nologin.
auth    required pam_succeed_if.so user ingroup jabali-ftp quiet
auth    required pam_unix.so
account required pam_succeed_if.so user ingroup jabali-ftp quiet
account required pam_unix.so
session required pam_permit.so
# JAB-263: move the FTPS worker into the owning tenant's jabali-user-<t>.slice
# so per-tenant CPU/memory/task limits apply. `optional` + the helper always
# exits 0 => this can never block a login (fail open — it is accounting, not
# access control).
session optional pam_exec.so quiet /usr/local/libexec/jabali/jabali-ftp-session-cgroup
PAMEOF
  chmod 0644 /etc/pam.d/vsftpd-jabali
  _ok "vsftpd PAM service installed (jabali-ftp members only, cgroup-placed)"
}

# install_ftp_firewall_rules — open 21 + the passive range. Only ever
# called from the ftp module path (i.e. ftp_enabled); converge_ftp_masking
# removes the rules when the module is toggled off.
install_ftp_firewall_rules() {
  command -v ufw >/dev/null 2>&1 || { _warn "ufw not present — skipping ftp firewall rules"; return 0; }
  ufw allow 21/tcp >/dev/null 2>&1 || true
  ufw allow 40000:40100/tcp >/dev/null 2>&1 || true
  _ok "ufw: 21/tcp + 40000:40100/tcp (ftp passive range) allowed"
}

# install_ftp_crowdsec — brute-force coverage from day one. Best-effort:
# the security module may be off, and an optional component never aborts
# the panel.
install_ftp_crowdsec() {
  command -v cscli >/dev/null 2>&1 || { _log "crowdsec not present — skipping vsftpd collection"; return 0; }
  cscli collections install crowdsecurity/vsftpd >/dev/null 2>&1 \
    || _warn "cscli collections install crowdsecurity/vsftpd failed (non-fatal)"
  cat > /etc/crowdsec/acquis.d/jabali-vsftpd.yaml <<'ACQEOF'
# Managed by jabali-panel install.sh (ftp module, GH #1053).
filenames:
  - /var/log/vsftpd.log
labels:
  type: vsftpd
ACQEOF
  systemctl reload crowdsec >/dev/null 2>&1 || true
  _ok "crowdsec vsftpd collection + acquisition installed"
}

# converge_ftp_masking — DB-truth enforcement of the ftp opt-in on every
# `jabali update` (GH #1053; same pattern as converge_pdns_masking /
# GH #447). Off (or unknown/fresh): vsftpd masked, ports closed — the
# module reconciler installs+unmasks when the admin opts in.
converge_ftp_masking() {
  command -v systemctl >/dev/null 2>&1 || return 0
  local ftp_on=0 db_val=""
  if command -v mariadb >/dev/null 2>&1; then
    db_val="$(mariadb jabali_panel -N -B -e \
      "SELECT ftp_enabled FROM server_settings WHERE id=1;" 2>/dev/null || true)"
  fi
  [[ "$db_val" == "1" ]] && ftp_on=1
  if [[ "$ftp_on" -eq 1 ]]; then
    systemctl unmask vsftpd >/dev/null 2>&1 || true
    # Heal rules on boxes where the module installed before a ufw reset.
    dpkg -s vsftpd >/dev/null 2>&1 && install_ftp_firewall_rules
  else
    if dpkg -s vsftpd >/dev/null 2>&1 || systemctl list-unit-files vsftpd.service >/dev/null 2>&1; then
      systemctl stop vsftpd >/dev/null 2>&1 || true
      systemctl mask vsftpd >/dev/null 2>&1 || true
    fi
    if command -v ufw >/dev/null 2>&1; then
      ufw delete allow 21/tcp >/dev/null 2>&1 || true
      ufw delete allow 40000:40100/tcp >/dev/null 2>&1 || true
    fi
  fi
}

# seed_module_flags — M353 (GH #353). Write the per-module server_settings flags
# to match the install selection so the panel's page-gating (serverCapabilities →
# nav/route hide + 409 guards) reflects what was actually installed. No-op unless
# JABALI_MODULES is set (a modular install); a plain install keeps the default-on
# flags. Best-effort: a SQL failure warns but never aborts the install.
seed_module_flags() {
  [[ -z "${JABALI_MODULES+x}" ]] && return 0
  command -v mariadb >/dev/null 2>&1 || return 0
  local flag key sets=""
  # map: server_settings column -> JABALI_MODULES key
  for pair in "dns_enabled:dns" "mail_enabled:mail" "security_enabled:security" \
              "quota_enabled:quota" "api_enabled:api"; do
    flag="${pair%%:*}"; key="${pair##*:}"
    if is_module_enabled "$key"; then val=1; else val=0; fi
    sets+="${flag}=${val},"
  done
  sets="${sets%,}"
  if mariadb jabali_panel -e "UPDATE server_settings SET ${sets} WHERE id=1;" 2>/dev/null; then
    _ok "module flags seeded from JABALI_MODULES (${JABALI_MODULES})"
  else
    _warn "could not seed module flags (server_settings) — set them in Server Settings → Modules"
  fi
}

# seed_default_local_backups — GH #1240. Optional opt-in: automatic daily local
# backups for EVERY user. OFF by default (the operator prefers remote; local uses
# disk + IO). Enable via JABALI_DEFAULT_LOCAL_BACKUPS=1 (non-interactive / TUI) or
# a y/N prompt on an interactive install. Only writes when opted in — the DB
# default (0) covers the off case. Best-effort: a SQL failure warns, never aborts.
seed_default_local_backups() {
  command -v mariadb >/dev/null 2>&1 || return 0
  local enable=0
  if [[ "${JABALI_DEFAULT_LOCAL_BACKUPS:-}" == "1" ]]; then
    enable=1
  elif [[ "${_cli_yes:-}" != "1" && -t 0 ]]; then
    local ans=""
    # 30s timeout so an unattended TTY install falls through to OFF (the default)
    # instead of blocking forever on this new question.
    read -t 30 -rp "Enable automatic daily local backups for all users? (local disk; off by default) [y/N]: " ans || true
    [[ "$ans" =~ ^[Yy] ]] && enable=1
  fi
  [[ "$enable" == "1" ]] || return 0
  if mariadb jabali_panel -e "UPDATE server_settings SET default_local_backups_enabled=1 WHERE id=1;" 2>/dev/null; then
    _ok "automatic daily local backups enabled (GH #1240) — manage in Server Settings → Backups"
  else
    _warn "could not enable default local backups (server_settings) — set it in Server Settings → Backups"
  fi
}

# print_module_plan — dry-run output (--dry-run). Shows exactly which optional
# modules would be installed vs skipped for the current JABALI_MODULES, and the
# server_settings flags that would be seeded — WITHOUT touching the system. Lets
# an operator verify the selection before committing to a real install.
print_module_plan() {
  printf '\n=== Jabali install plan (dry run) ===\n'
  if [[ -z "${JABALI_MODULES+x}" ]]; then
    printf 'JABALI_MODULES: (unset) → ALL modules enabled (default install)\n'
  elif [[ -z "${JABALI_MODULES}" ]]; then
    printf 'JABALI_MODULES: (empty) → minimal — no optional modules\n'
  else
    printf 'JABALI_MODULES: %s\n' "${JABALI_MODULES}"
  fi
  printf '\nCore (always installed): nginx + PHP-FPM, MariaDB, panel-api, agent, Kratos\n'
  printf '\nOptional modules:\n'
  local key label
  for pair in "dns:PowerDNS (DNS server)" \
              "mail:Stalwart + Bulwark (mail)" \
              "security:CrowdSec + malware/ClamAV + AppArmor" \
              "python_apps:Python app runtime" \
              "quota:Filesystem quota" \
              "api:REST API (API keys)"; do
    key="${pair%%:*}"; label="${pair#*:}"
    if is_module_enabled "$key"; then
      printf '  [x] %-10s %s\n' "$key" "$label"
    else
      printf '  [ ] %-10s %s (skipped)\n' "$key" "$label"
    fi
  done
  printf '\nserver_settings flags that would be seeded:\n'
  local flag k
  for pair in "dns_enabled:dns" "mail_enabled:mail" "security_enabled:security" \
              "quota_enabled:quota" "api_enabled:api"; do
    flag="${pair%%:*}"; k="${pair##*:}"
    if is_module_enabled "$k"; then printf '  %-18s = 1\n' "$flag"; else printf '  %-18s = 0\n' "$flag"; fi
  done
  printf '\nNo changes made. Re-run without --dry-run to install.\n\n'
}

# is_container returns 0 inside LXC/Docker/Podman/systemd-nspawn etc.
# Gates kernel-LSM-touching steps (auditd, AppArmor) and host-kernel-
# only services (systemd-timesyncd) that warn noisily in a container
# where the host owns the resource. Defensive about
# systemd-detect-virt being absent so the script still runs in
# minimal environments.
is_container() {
  command -v systemd-detect-virt >/dev/null 2>&1 && \
    systemd-detect-virt --container --quiet 2>/dev/null
}

# Announce where logs are going so the operator can tail -f in another
# shell if the install stalls. Printed via _log so it's itself captured.
if [[ -n "${LOG_FILE:-}" ]]; then
  _log "install log: $LOG_FILE (includes every step + wrapped command output)"
else
  _warn "could not open install log file — post-mortem only via scrollback"
fi

# Socket-perm + bind helpers used by install_kratos and Step 2-5
# verify blocks. Sourced at top of file so EVERY caller (including
# agent `bash -c "source install.sh && install_<fn>"` invocations)
# has the helpers in scope. Earlier the source line lived inside
# main(), which meant `jabali update`'s sync kratos step always
# failed with "verify_socket_perms: command not found".
if [[ -r "$REPO_DIR/install/scripts/socket-helpers.sh" ]]; then
  # shellcheck source=install/scripts/socket-helpers.sh
  source "$REPO_DIR/install/scripts/socket-helpers.sh"
fi

# ensure_dbus brings the D-Bus system bus up if dpkg installed it but the
# socket never activated (common on minimal LXC/OpenVZ VPS images: dbus is
# present but dbus.socket is static and nothing pulls it in at boot, leaving
# /run/dbus/system_bus_socket missing). Without the bus, `resolvectl`,
# `systemctl restart` over D-Bus, and any other org.freedesktop.* client
# fails with `sd_bus_open_system: No such file or directory` — the install
# probes that depend on those calls then false-die.
#
# Returns 0 once the socket exists, 1 with a _warn if dbus is genuinely not
# installed or refuses to start. Callers should gate D-Bus-dependent probes
# on this and degrade gracefully (rely on direct dig/curl/file checks).
ensure_dbus() {
  if [[ -S /run/dbus/system_bus_socket ]]; then
    return 0
  fi
  if ! command -v dbus-daemon >/dev/null 2>&1 && ! dpkg -s dbus >/dev/null 2>&1; then
    # dbus is a hard dependency (systemd-user cron, resolvectl, machinectl) but
    # minimal Debian KVM / LXC images ship without it. Install it (+ the user
    # session integration `systemctl --user` needs) rather than giving up. GH #296.
    _log "dbus not installed — installing dbus + dbus-user-session (required for cron + resolvectl)"
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq dbus dbus-user-session >/dev/null 2>&1 || {
      _warn "dbus install failed — system bus unavailable"
      return 1
    }
  fi
  _log "activating dbus.socket (was dormant; common on minimal images)"
  systemctl start dbus.socket dbus.service >/dev/null 2>&1 || true
  # Persist across reboots: on minimal images dbus.socket is static and nothing
  # pulls it in at boot, so a start-only activation is lost on the next reboot
  # and cron/systemd-user break again (GH #296). enable is best-effort.
  systemctl enable dbus.socket dbus.service >/dev/null 2>&1 || true
  local attempt
  for attempt in 1 2 3 4 5; do
    [[ -S /run/dbus/system_bus_socket ]] && return 0
    sleep 1
  done
  _warn "D-Bus still unavailable after start attempt (/run/dbus/system_bus_socket missing)"
  return 1
}

# verify_runtime_health is an end-of-install canary for the runtime
# capabilities jabali silently depends on: a live system D-Bus and the
# ability to start systemd transient units. The classic failure (GH #296)
# is a minimal image with no dbus, where systemd-user cron + transient
# backup/cron units only break at runtime with a cryptic "connect to system
# scope bus" days later. Catch it at install: ensure the bus, then run a
# throwaway transient unit. Warn-only (the install already succeeded) with
# the exact follow-up the operator needs.
verify_runtime_health() {
  _log "verifying runtime health (D-Bus + systemd transient units)"
  if ensure_dbus; then
    _ok "system D-Bus is up (/run/dbus/system_bus_socket)"
  else
    _warn "system D-Bus unavailable — cron, resolvectl + machinectl will not work until dbus is installed and the socket is up"
  fi
  if systemd-run --scope --quiet --collect -- /bin/true >/dev/null 2>&1; then
    _ok "systemd transient units OK — cron execution path healthy"
  else
    _warn "systemd-run failed — per-user cron + transient units may not run. Check: systemctl is-system-running; systemctl status dbus.socket"
  fi
}

# _flush_spin_log appends a wrapped command's captured output into the
# main $LOG_FILE with a header so the post-mortem log reads top-to-bottom
# as a sequence of {step, output} blocks. No-op when LOG_FILE is empty
# or when the captured log has nothing to show.
_flush_spin_log() {
  local label="$1" log="$2"
  [[ -n "${LOG_FILE:-}" && -s "$log" ]] || return 0
  {
    printf '\n### %s ###\n' "$label"
    cat "$log"
  } >> "$LOG_FILE" 2>/dev/null || true
}

# _spin runs the given command with stdout+stderr captured to a temp log
# and a live spinner + elapsed counter on the terminal. On success, the
# captured output is flushed into $LOG_FILE for post-mortem diagnostics
# and an _ok line prints. On failure, the last 60 captured lines dump to
# stderr too so the operator sees them in scrollback, then the original
# exit code propagates. Usage: _spin "label" cmd args…
#
# Non-TTY stdout (CI, tee'd logs) falls back to a simple start/end pair
# with no spinner so the scrollback stays readable.
_spin() {
  local label="$1"; shift
  local log; log="$(mktemp /tmp/jabali-spin.XXXXXX.log)"

  # --debug / JABALI_DEBUG: skip the spinner, stream child output live so
  # hangs surface immediately (the default path swallows stdout+stderr
  # into $log and only reveals them on failure — perfect for clean
  # installs, useless when apt/systemctl/curl is stalled mid-run and
  # you want to watch the last line the stuck child printed). `tee -a`
  # mirrors the live stream into $LOG_FILE so the post-mortem still has
  # everything even though no separate capture happens.
  #
  # rc capture: `cmd | tee || true` would resolve `true` LAST and reset
  # PIPESTATUS to (0) — masking apt failures. Capture inside the `||`
  # clause where PIPESTATUS still reflects the failing pipeline, BEFORE
  # any subsequent simple command can rewrite it.
  if [[ -n "${JABALI_DEBUG:-}" ]]; then
    _log "$label…"
    local rc=0
    if [[ -n "${LOG_FILE:-}" ]]; then
      "$@" 2>&1 | tee -a "$LOG_FILE" || rc="${PIPESTATUS[0]}"
    else
      "$@" || rc=$?
    fi
    if [[ $rc -ne 0 ]]; then
      _err "$label FAILED (exit $rc)"
      rm -f "$log"
      exit "$rc"
    fi
    _ok "$label"
    rm -f "$log"
    return 0
  fi

  if [[ ! -t 1 ]]; then
    _log "$label…"
    if ! "$@" >"$log" 2>&1; then
      local rc=$?
      _err "$label FAILED (exit $rc) — tail of log:"
      tail -n 60 "$log" >&2
      _flush_spin_log "$label" "$log"
      rm -f "$log"
      exit "$rc"
    fi
    _flush_spin_log "$label" "$log"
    _ok "$label"
    rm -f "$log"
    return 0
  fi

  # Braille spinner — each frame is two glyphs wide. Array form is
  # required: bash's ${var:i:1} does BYTE slicing, which shreds
  # multi-byte UTF-8. Frames chosen for a smooth left-to-right sweep.
  local -a spinners=('⢎ ' '⠎⠁' '⠊⠑' '⠈⠱' ' ⡱' '⢀⡰' '⢄⡠' '⢆⡀')
  local n=${#spinners[@]}
  local i=0
  local start; start=$(date +%s)

  # Paint the first frame BEFORE forking the command. Sub-100ms commands
  # (warm apt cache, already-installed packages) would otherwise exit
  # before the loop's first `kill -0` check and the user would see only
  # the final [✓] line with no spinner at all. This guarantees at least
  # one spinner frame prints for every _spin call.
  #
  # Bracketed spinner mirrors the [✓]/[i]/[!]/[✗] column the logger uses
  # — when the process finishes, _ok overwrites the same column with
  # [✓], so the eye tracks the status glyph in one fixed place.
  printf '\033[1;36m[%s]\033[0m %s (0s)' "${spinners[i++ % n]}" "$label"

  "$@" >"$log" 2>&1 &
  local pid=$!
  while kill -0 "$pid" 2>/dev/null; do
    sleep 0.1
    local elapsed=$(( $(date +%s) - start ))
    printf '\r\033[K\033[1;36m[%s]\033[0m %s (%ds)' \
      "${spinners[i++ % n]}" "$label" "$elapsed"
  done
  # set -e gotcha: `wait $pid; local rc=$?` are two statements. If wait
  # returns non-zero (apt dpkg-lock contention, apt-get exit 100 from a
  # post-firstboot unattended-upgrades run, etc.), set -e fires AFTER
  # wait but BEFORE `local rc=$?`, so the failure-tail dump never runs
  # and bash exits silently with no log entry. Capture rc inside the
  # `||` clause where set -e is suppressed. (Memory: feedback_sigpipe_silent_exit.md)
  local rc=0
  wait "$pid" || rc=$?
  printf '\r\033[K'

  if [[ $rc -ne 0 ]]; then
    _err "$label FAILED (exit $rc) — tail of log:"
    tail -n 60 "$log" >&2
    _flush_spin_log "$label" "$log"
    rm -f "$log"
    exit "$rc"
  fi
  _flush_spin_log "$label" "$log"
  _ok "$label"
  rm -f "$log"
}

# ---------- banner ----------------------------------------------------------
# Prints the jabali ASCII art at install start. Uses ANSI colour (yellow)
# for visibility without being garish. Unicode block characters require
# a UTF-8 terminal — every modern ssh/console has this by default.
print_banner() {
  printf '\033[1;33m'
  cat <<'BANNER'
      ▀██▀         ▀██              ▀██   ██
       ██   ▄▄▄▄    ██ ▄▄▄   ▄▄▄▄    ██  ▄▄▄
       ██  ▀▀ ▄██   ██▀  ██ ▀▀ ▄██   ██   ██
       ██  ▄█▀ ██   ██    █ ▄█▀ ██   ██   ██
   ██ ▄█▀  ▀█▄▄▀█▀  ▀█▄▄▄▀  ▀█▄▄▀█▀ ▄██▄ ▄██▄
    ▀▀▀
      J A B A L I   P A N E L   ·   v0.2.10
         Linux Web Hosting Control Panel
BANNER
  printf '\033[0m\n'
}

# ---------- preflight -------------------------------------------------------

preflight() {
  _log "preflight checks"

  [[ $EUID -eq 0 ]] || _die "must run as root (sudo bash install.sh)"

  if [[ -f /etc/os-release ]]; then
    # shellcheck disable=SC1091
    . /etc/os-release
    case "${ID:-}" in
      debian|ubuntu) _ok "OS: $PRETTY_NAME" ;;
      *) _warn "untested OS: ${PRETTY_NAME:-unknown}. Continuing anyway." ;;
    esac
  else
    _warn "no /etc/os-release; continuing blind"
  fi

  # Architecture gate. jabali-panel ships amd64 only — Kratos, Stalwart,
  # Stalwart-CLI, Bulwark webmail, and YARA-X are vendored as pinned
  # SHA-256 amd64 release tarballs (no arm64 SHA pins, no fallback build
  # path). On a non-amd64 host the install used to silently download the
  # wrong-arch Kratos binary and only die ~8 steps later with a cryptic
  # `Kratos database migrations failed` masked by `cannot execute binary
  # file: Exec format error` from the bash log. Fail fast at preflight
  # with a clear, actionable message instead.
  local arch
  arch="$(uname -m)"
  case "$arch" in
    x86_64)
      GO_ARCH="amd64"
      ;;
    aarch64|arm64)
      _die "this host is arm64 (uname -m=$arch). jabali-panel only ships amd64 binaries today (Kratos, Stalwart, Stalwart-CLI, Bulwark, YARA-X are all pinned-SHA amd64 tarballs). Provision an amd64/x86_64 VPS image and re-run install.sh."
      ;;
    *)
      _die "unsupported arch: $arch. jabali-panel requires x86_64/amd64. Provision an amd64 VPS image and re-run install.sh."
      ;;
  esac
  export GO_ARCH

  # systemd is assumed end-to-end: per-user FPM slices, systemd-user cron
  # timers, transient backup/cron units, and service management all need
  # systemctl + a live system bus. A non-systemd host (legacy OpenVZ, some
  # minimal container images) cannot run jabali — fail fast with a clear
  # message instead of cryptic systemctl errors several steps in.
  if [[ ! -d /run/systemd/system ]]; then
    _die "this host is not systemd-managed (/run/systemd/system missing). jabali requires systemd as PID 1 — provision a systemd-based VPS image."
  fi
}

# ---------- step 0.5: server identity prompts -------------------------------
#
# Capture hostname, public IPs, and nameserver names before any install
# step runs. Values are exported so write_config_file can seed config.toml
# and the app can read them on first boot. Idempotent: if the existing
# config.toml already contains [server].hostname, the prompts are skipped
# so `install.sh` is safe to re-run for updates.

# Read the primary interface IPs straight from the kernel. We pick the
# interface that owns the default route and take its first global-scope
# address. This matches what the panel will serve customers with and
# behaves sensibly behind NAT (returns the LAN IP; operators correct
# via the admin Server Settings page if the server actually sits behind
# 1:1 NAT with a different public IP).
_detect_main_iface() {
  ip route show default 2>/dev/null | awk '/^default/ {print $5; exit}'
}

_detect_public_ipv4() {
  local iface
  iface="$(_detect_main_iface)"
  if [[ -n "$iface" ]]; then
    ip -4 -o addr show dev "$iface" scope global 2>/dev/null \
      | awk '{print $4}' | cut -d/ -f1 | head -n1
    return 0
  fi
  # No default route — take any global IPv4.
  ip -4 -o addr show scope global 2>/dev/null | awk '{print $4}' | cut -d/ -f1 | head -n1
}

_detect_public_ipv6() {
  local iface
  iface="$(_detect_main_iface)"
  if [[ -n "$iface" ]]; then
    # -preferred drops deprecated/tentative addresses so we never pick a
    # stale SLAAC temp that's about to expire.
    ip -6 -o addr show dev "$iface" scope global -preferred 2>/dev/null \
      | awk '{print $4}' | cut -d/ -f1 | head -n1
    return 0
  fi
  ip -6 -o addr show scope global 2>/dev/null | awk '{print $4}' | cut -d/ -f1 | head -n1
}

# ---- JAB-213 free-hostname install-time flow (inline; runs pre-clone) --------
# Defined in install.sh itself, not sourced from $REPO_DIR/install/, because
# prompt_server_settings runs BEFORE clone_or_update_repo (ordering guard in
# install_repo_dir_ordering_test.go). The token is parsed out and written to
# hostname.env directly — never echoed to the install log, which wraps output.
JH_API="${JABALI_HOSTNAME_API:-https://api.jabalihosted.com}"
JH_TOKEN_FILE=/etc/jabali-panel/hostname.env

jh_post() {
  local path="$1" body="$2"
  curl -sS --max-time 30 -w $'\n%{http_code}' \
    -H 'Content-Type: application/json' \
    -d "$body" "${JH_API}${path}" 2>/dev/null || printf '\n000'
}
jh_field() { printf '%s' "$1" | python3 -c 'import json,sys; print(json.load(sys.stdin).get(sys.argv[1],""))' "$2" 2>/dev/null; }

jh_free_hostname_flow() {
  local fd="$1" email code body fqdn label token
  [[ -z "$fd" ]] && { echo "free hostname needs an interactive terminal" >&2; return 1; }
  {
    printf '\nFree Jabali hostname\n'
    printf '  A public hostname like 203-0-113-7.jabalihosted.com with automatic\n'
    printf '  DNS + TLS. We email a one-time code to verify the address (stored\n'
    printf '  only to contact you about the hostname).\n\n'
  } > /dev/tty 2>/dev/null || true
  local email_re='^[^@[:space:]]+@[^@[:space:]]+\.[^@[:space:]]+$'
  while true; do
    printf 'Email for verification: ' > /dev/tty 2>/dev/null || printf 'Email for verification: '
    read -r -u "$fd" email || true
    [[ "$email" =~ $email_re ]] && break
    echo "  please enter a valid email address" > /dev/tty 2>/dev/null || true
  done
  local resp jcode attempt
  for attempt in 1 2; do
    resp="$(jh_post /v1/register "{\"email\":\"${email}\"}")"
    jcode="${resp##*$'\n'}"; body="${resp%$'\n'*}"
    case "$jcode" in
      200) break ;;
      429) echo "  a code was just sent — wait a minute and re-run" >&2; return 1 ;;
      000|5*) [[ $attempt == 1 ]] && { sleep 3; continue; }
             echo "  hostname service unreachable ($jcode) — using a manual hostname" >&2; return 1 ;;
      *)   echo "  could not send a code ($(jh_field "$body" error)) — using a manual hostname" >&2; return 1 ;;
    esac
  done
  printf '  code sent to %s\n' "$email" > /dev/tty 2>/dev/null || true
  local tries
  for tries in 1 2 3; do
    printf 'Enter the 6-digit code: ' > /dev/tty 2>/dev/null || printf 'Enter the 6-digit code: '
    read -r -u "$fd" code || true
    code="${code//[^0-9]/}"
    resp="$(jh_post /v1/claim "{\"email\":\"${email}\",\"code\":\"${code}\"}")"
    jcode="${resp##*$'\n'}"; body="${resp%$'\n'*}"
    case "$jcode" in
      200) break ;;
      403) echo "  wrong or expired code, try again" > /dev/tty 2>/dev/null || true
           [[ $tries == 3 ]] && { echo "  no valid code entered — using a manual hostname" >&2; return 1; }
           continue ;;
      429) echo "  too many attempts — re-run the installer for a fresh code" >&2; return 1 ;;
      422) echo "  this server's public IP can't take a free hostname ($(jh_field "$body" message))" >&2
           echo "  falling back to a manual hostname" >&2; return 1 ;;
      *)   echo "  claim failed ($jcode) — using a manual hostname" >&2; return 1 ;;
    esac
  done
  fqdn="$(jh_field "$body" fqdn)"; label="$(jh_field "$body" label)"; token="$(jh_field "$body" token)"
  [[ -z "$fqdn" || -z "$token" ]] && { echo "  malformed claim response — using a manual hostname" >&2; return 1; }
  # Validate every field before it lands in hostname.env. The readers no longer
  # `source` that file, but this is the boundary where remote data enters the
  # box, and a value carrying shell metacharacters or a newline could still
  # break or spoof a later parse. Reject rather than sanitize so a
  # compromised/MITM'd service can't smuggle anything through.
  [[ "$token" =~ ^[A-Za-z0-9_-]+$ ]] || { echo "  claim response token has an unexpected format — using a manual hostname" >&2; return 1; }
  [[ "$label" =~ ^[A-Za-z0-9-]+$ ]]  || { echo "  claim response label has an unexpected format — using a manual hostname" >&2; return 1; }
  [[ "$email" =~ ^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+$ ]] || { echo "  email has an unexpected format — using a manual hostname" >&2; return 1; }
  umask 077
  {
    printf 'LABEL=%s\nFQDN=%s\nEMAIL=%s\nTOKEN=%s\nAPI=%s\n' "$label" "$fqdn" "$email" "$token" "$JH_API"
  } > "$JH_TOKEN_FILE"
  chmod 0600 "$JH_TOKEN_FILE"
  printf '%s' "$fqdn"   # only stdout — install.sh captures it
  return 0
}

prompt_server_settings() {
  local config_file="/etc/jabali-panel/config.toml"

  # Hostname is applied up front by apply_system_hostname() in main(); re-run it
  # idempotently here in case a re-run path set JABALI_HOSTNAME after that call.
  apply_system_hostname

  # Strip every `127.0.1.1 ...` line in /etc/hosts. Debian seeds this
  # on first boot via `hostnamectl`, but on a public VPS it shadows
  # real DNS -- net.LookupHost respects /etc/hosts before DNS, and the
  # M32 panel-cert routability gate compares the lookup result against
  # public_ipv4. With 127.0.1.1 in the way, the check sees loopback
  # and refuses to attempt LE issuance ("dns points elsewhere"). Take
  # the loopback resolution loss -- `hostname -f` falls back to DNS
  # which is what the operator needs anyway.
  #
  # Runs UNCONDITIONALLY (not gated on --hostname / JABALI_HOSTNAME):
  # operators who pre-set the system hostname and ran install.sh
  # without --hostname were still bitten. Incident 2026-04-26 on
  # mx.jabali-panel.com (gated strip shipped); revisited 2026-06-04
  # on vpsjournal.com fresh install (gated strip missed because
  # JABALI_HOSTNAME was unset).
  #
  # Blanket-strip every 127.0.1.1 line: that prefix has no legitimate
  # use beyond hostname shadow (127.0.0.1 covers localhost). The
  # matching public-IP entry for hostname + mail/autoconfig
  # subdomain gets added later in the same function.
  if [[ -f /etc/hosts ]]; then
    sed -i '/^127\.0\.1\.1\([[:space:]]\|$\)/d' /etc/hosts
  fi

  if [[ -f "$config_file" ]] && grep -q '^[[:space:]]*hostname[[:space:]]*=' "$config_file"; then
    _log "server settings already configured in $config_file — skipping prompt"
    # Re-export for downstream use so write_config_file is a no-op on re-run.
    JABALI_SERVER_CONFIGURED=1
    export JABALI_SERVER_CONFIGURED
    return 0
  fi

  local sys_hostname detected_ipv4 detected_ipv6
  sys_hostname="$(hostname -f 2>/dev/null || hostname 2>/dev/null || echo '')"

  _log "detecting primary interface IPv4…"
  detected_ipv4="$(_detect_public_ipv4 || true)"
  if [[ -z "$detected_ipv4" ]]; then
    # Auto-detect can come up empty on hosts with no default route or only
    # non-global addresses (bridged containers, some VPS setups). The error
    # told the operator to set JABALI_PUBLIC_IPV4 — so honour it here instead
    # of dying before it is ever read (the value is otherwise consumed below at
    # inp_ipv4="${JABALI_PUBLIC_IPV4:-$detected_ipv4}").
    if [[ -n "${JABALI_PUBLIC_IPV4:-}" ]]; then
      _warn "could not auto-detect an IPv4 address — using JABALI_PUBLIC_IPV4=${JABALI_PUBLIC_IPV4}"
      detected_ipv4="$JABALI_PUBLIC_IPV4"
    else
      _die "could not auto-detect an IPv4 address. Set JABALI_PUBLIC_IPV4 and re-run."
    fi
  fi
  _ok "primary IPv4: $detected_ipv4"

  _log "detecting primary interface IPv6 (optional)…"
  detected_ipv6="$(_detect_public_ipv6 || true)"
  if [[ -n "$detected_ipv6" ]]; then
    _ok "primary IPv6: $detected_ipv6"
  else
    _log "no IPv6 detected — skipping (zones won't get AAAA records)"
  fi

  # `curl | bash` consumes stdin for the script itself, so `read` would
  # hit EOF instantly. Fix: read from /dev/tty (the controlling terminal)
  # if one exists. If it doesn't — CI / cloud-init / no TTY at all —
  # fall back to env-var overrides / auto-detected defaults.
  #
  # Note: `[[ -r /dev/tty ]]` lies on non-interactive SSH (the device
  # node exists and looks readable to the test, but `exec 3</dev/tty`
  # fails with "No such device or address" because the session has no
  # controlling terminal). So we don't pre-test — we try the exec
  # directly inside an `if`, which neutralises errexit and lets us
  # fall through to the stdin-TTY branch on failure.
  # JABALI_NONINTERACTIVE=1 (set by the TUI installer, which owns the terminal
  # and streams our output) forces the defaults/env path so we never open
  # /dev/tty — a raw read there would block behind the TUI's screen. All values
  # come from env (JABALI_HOSTNAME/NS1/NS2) or auto-detected defaults.
  local input_fd
  if [[ -n "${JABALI_NONINTERACTIVE:-}" ]]; then
    input_fd=""
  elif exec 3</dev/tty 2>/dev/null; then
    input_fd=3
  elif [[ -t 0 ]]; then
    input_fd=0
  else
    input_fd=""
  fi

  local inp_hostname inp_ipv4 inp_ipv6 inp_ns1_name inp_ns1_ip inp_ns2_name inp_ns2_ip

  # IPs always come from detection / env override — never prompted.
  inp_ipv4="${JABALI_PUBLIC_IPV4:-$detected_ipv4}"
  inp_ipv6="${JABALI_PUBLIC_IPV6:-$detected_ipv6}"

  # If the hostname was pre-supplied (via --hostname flag or JABALI_HOSTNAME
  # env), skip the prompt entirely — even when a TTY is available. This
  # enables non-interactive provisioning (Ansible, CI images, etc.).
  local _hostname_regex='^[a-zA-Z0-9][a-zA-Z0-9.-]*[a-zA-Z0-9]$'
  # JAB-213: opt-in free jabalihosted.com hostname. Gated on the env flag AND
  # an interactive TTY (the email+code dance is inherently interactive) AND no
  # explicit --hostname. On success it sets inp_hostname to the claimed FQDN
  # and skips the manual prompt; on any failure it falls through to the normal
  # prompt with a reason printed. Never runs non-interactively.
  if [[ "${JABALI_FREE_HOSTNAME:-}" == "1" && -z "${JABALI_HOSTNAME:-}" && -n "$input_fd" ]]; then
    local _jh_fqdn
    # jh_free_hostname_flow is defined inline below (NOT sourced from $REPO_DIR
    # — this runs before clone_or_update_repo, so the repo isn't on disk yet;
    # the install-repo-dir-ordering guard enforces that).
    if _jh_fqdn="$(jh_free_hostname_flow "$input_fd")" && [[ "$_jh_fqdn" =~ $_hostname_regex ]]; then
      inp_hostname="$_jh_fqdn"
      _ok "using free Jabali hostname: $inp_hostname"
      JABALI_FREE_HOSTNAME_ACTIVE=1
    else
      _warn "free hostname not set up — continuing with a manual hostname"
    fi
  fi
  if [[ -n "${JABALI_FREE_HOSTNAME_ACTIVE:-}" ]]; then
    : # hostname already resolved via the free-hostname flow; skip prompts below
  elif [[ -n "${JABALI_HOSTNAME:-}" ]]; then
    if [[ ! "$JABALI_HOSTNAME" =~ $_hostname_regex ]]; then
      _die "invalid JABALI_HOSTNAME: '$JABALI_HOSTNAME' (use letters/digits/dots/hyphens)"
    fi
    inp_hostname="$JABALI_HOSTNAME"
    _ok "using hostname from flag/env: $inp_hostname"
    # Don't close the TTY FD yet — ns1/ns2 prompts below may still
    # need it if JABALI_NS1_NAME / JABALI_NS2_NAME weren't supplied.
  elif [[ -z "$input_fd" ]]; then
    _warn "no TTY available — using auto-detected defaults + env vars."
    _warn "override hostname via --hostname flag or JABALI_HOSTNAME env"
    inp_hostname="$sys_hostname"
    if [[ ! "$inp_hostname" =~ $_hostname_regex ]]; then
      _die "no TTY and no --hostname given (detected: '$inp_hostname')"
    fi
  else
    # Structured preamble so the operator knows exactly what this
    # hostname controls before typing it. Printed to /dev/tty along
    # with the prompt itself so bash's stderr buffering (an issue
    # under `curl | bash`) can't swallow any of it. Falls back to
    # stdout if /dev/tty is unavailable (shouldn't happen since we
    # already proved we have a TTY via exec 3</dev/tty above, but
    # the guard is cheap).
    {
      printf '\n'
      printf 'Enter the fully qualified domain name (FQDN) for this server.\n'
      printf 'This name will be used for:\n'
      printf '  - System hostname (hostnamectl set-hostname)\n'
      printf '  - Panel URL (https://<hostname>:8443)\n'
      printf '  - Mail server config (stalwart + per-domain vhosts)\n'
      if is_module_enabled dns; then
        printf '  - Nameserver records (ns1.<hostname>, ns2.<hostname>)\n'
      fi
      printf '\n'
      printf 'Current hostname: \033[1m%s\033[0m\n' "$sys_hostname"
      printf 'Server IPv4:      \033[1m%s\033[0m\n' "$inp_ipv4"
      if [[ -n "$inp_ipv6" ]]; then
        printf 'Server IPv6:      \033[1m%s\033[0m\n' "$inp_ipv6"
      fi
      printf '\n'
    } > /dev/tty 2>/dev/null || {
      printf '\n'
      printf 'Current hostname: %s\n' "$sys_hostname"
      printf 'Server IPv4:      %s\n' "$inp_ipv4"
      [[ -n "$inp_ipv6" ]] && printf 'Server IPv6:      %s\n' "$inp_ipv6"
      printf '\n'
    }

    while true; do
      # Write the prompt directly to /dev/tty, bypassing stdout/stderr
      # entirely. `read -p` and `printf >&2` both failed to render this
      # line under `curl | bash` on Debian 13 — likely bash's own
      # block-buffering of stderr when the parent pipe (curl) is still
      # live. Writing to /dev/tty hits the same device the user is
      # looking at with zero intermediaries.
      printf "Enter hostname [%s]: " "$sys_hostname" > /dev/tty 2>/dev/null \
        || printf "Enter hostname [%s]: " "$sys_hostname"
      read -r -u "$input_fd" inp_hostname || true
      inp_hostname="${inp_hostname:-$sys_hostname}"
      [[ "$inp_hostname" =~ $_hostname_regex ]] && break
      _warn "invalid hostname; use letters/digits/dots/hyphens"
    done
  fi

  # NS IPs are always the server's own IPv4 at install time. The
  # operator can point ns2 at a separate server later via the admin
  # Server Settings page (triggers a zone re-push automatically).
  inp_ns1_ip="${inp_ipv4}"
  inp_ns2_ip="${inp_ipv4}"

  # NS names default to ns1/ns2.<hostname> but can be overridden via
  # env (JABALI_NS1_NAME / JABALI_NS2_NAME) for non-interactive
  # provisioning or via interactive prompts when running on a TTY.
  #
  # GH #353 (tester feedback): when the dns module is deselected the
  # nameservers are never served by this host (PowerDNS isn't installed and
  # bootstrap_pdns_self_zone only runs under `run_if_module dns`), so don't
  # ask for them — external-DNS operators (Cloudflare etc.) have nothing to
  # answer. The silent ns1/ns2.<hostname> defaults still land in
  # config.toml/server_settings as placeholders; enabling dns later
  # (Server Settings -> Modules) reconstructs from that row and the names are
  # editable in Server Settings, which triggers a zone re-push.
  local _default_ns1="ns1.${inp_hostname}"
  local _default_ns2="ns2.${inp_hostname}"

  if [[ -n "${JABALI_NS1_NAME:-}" ]]; then
    if [[ ! "$JABALI_NS1_NAME" =~ $_hostname_regex ]]; then
      _die "invalid JABALI_NS1_NAME: '$JABALI_NS1_NAME'"
    fi
    inp_ns1_name="$JABALI_NS1_NAME"
    _ok "using ns1 name from env: $inp_ns1_name"
  elif ! is_module_enabled dns; then
    inp_ns1_name="$_default_ns1"
    _log "dns module disabled — skipping ns1 prompt (placeholder: $inp_ns1_name)"
  elif [[ -z "$input_fd" ]]; then
    inp_ns1_name="$_default_ns1"
  else
    while true; do
      printf "Enter ns1 name [%s]: " "$_default_ns1" > /dev/tty 2>/dev/null         || printf "Enter ns1 name [%s]: " "$_default_ns1"
      read -r -u "$input_fd" inp_ns1_name || true
      inp_ns1_name="${inp_ns1_name:-$_default_ns1}"
      [[ "$inp_ns1_name" =~ $_hostname_regex ]] && break
      _warn "invalid ns1 name; use letters/digits/dots/hyphens"
    done
  fi

  if [[ -n "${JABALI_NS2_NAME:-}" ]]; then
    if [[ ! "$JABALI_NS2_NAME" =~ $_hostname_regex ]]; then
      _die "invalid JABALI_NS2_NAME: '$JABALI_NS2_NAME'"
    fi
    inp_ns2_name="$JABALI_NS2_NAME"
    _ok "using ns2 name from env: $inp_ns2_name"
  elif ! is_module_enabled dns; then
    inp_ns2_name="$_default_ns2"
    _log "dns module disabled — skipping ns2 prompt (placeholder: $inp_ns2_name)"
  elif [[ -z "$input_fd" ]]; then
    inp_ns2_name="$_default_ns2"
  else
    while true; do
      printf "Enter ns2 name [%s]: " "$_default_ns2" > /dev/tty 2>/dev/null         || printf "Enter ns2 name [%s]: " "$_default_ns2"
      read -r -u "$input_fd" inp_ns2_name || true
      inp_ns2_name="${inp_ns2_name:-$_default_ns2}"
      [[ "$inp_ns2_name" =~ $_hostname_regex ]] && break
      _warn "invalid ns2 name; use letters/digits/dots/hyphens"
    done
  fi

  # Close the TTY FD now that every prompt is done.
  [[ "$input_fd" == "3" ]] && exec 3<&-

  # Apply hostname at the OS layer now so later steps see the right name.
  hostnamectl set-hostname "$inp_hostname" 2>/dev/null || true
  if ! grep -q "[[:space:]]${inp_hostname}\([[:space:]]\|$\)" /etc/hosts 2>/dev/null; then
    printf '%s\t%s\n' "$inp_ipv4" "$inp_hostname" >> /etc/hosts
  fi
  # M6.6 — Bulwark talks to Stalwart via https://mail.<hostname>/jmap
  # (per JMAP_SERVER_URL in bulwark.env). The per-domain mail vhost
  # binds to ${inp_ipv4}:443 specifically (M24 listen-IP pinning), so
  # the box must resolve mail.<hostname> + autoconfig.<hostname> to
  # ${inp_ipv4} for the localhost-originated Bulwark HTTPS fetch to
  # land on the right vhost. Without these lines, Bulwark fetch fails
  # with "fetch failed" → webmail SSO 500s after the SPA loads.
  if ! grep -q "[[:space:]]mail\.${inp_hostname}\([[:space:]]\|$\)" /etc/hosts 2>/dev/null; then
    printf '%s\tmail.%s\tautoconfig.%s\n' "$inp_ipv4" "$inp_hostname" "$inp_hostname" >> /etc/hosts
  fi

  # Export for write_config_file. Not using a file because we write to
  # /etc/jabali-panel/config.toml later in the install flow anyway.
  JABALI_SRV_HOSTNAME="$inp_hostname"
  JABALI_SRV_IPV4="$inp_ipv4"
  JABALI_SRV_IPV6="$inp_ipv6"
  JABALI_SRV_NS1_NAME="$inp_ns1_name"
  JABALI_SRV_NS1_IPV4="$inp_ns1_ip"
  JABALI_SRV_NS2_NAME="$inp_ns2_name"
  JABALI_SRV_NS2_IPV4="$inp_ns2_ip"
  JABALI_SERVER_CONFIGURED=0
  export JABALI_SRV_HOSTNAME JABALI_SRV_IPV4 JABALI_SRV_IPV6 \
         JABALI_SRV_NS1_NAME JABALI_SRV_NS1_IPV4 \
         JABALI_SRV_NS2_NAME JABALI_SRV_NS2_IPV4 \
         JABALI_SERVER_CONFIGURED

  _ok "captured server identity: ${inp_hostname} (${inp_ipv4})"
}

# ---------- step 1: base packages -------------------------------------------

install_base_packages() {
  _log "installing all system packages in one batch"
  export DEBIAN_FRONTEND=noninteractive

  # Re-runs on a host whose previous install crashed mid-way leave
  # /etc/apt/sources.list.d/sury-php.list (and the matching
  # NodeSource list) behind. The bootstrap apt update below would
  # then re-fetch their indexes — and Sury's Fastly edge returns 418
  # to flagged datacenter IPs, killing the whole install before
  # _install_sury_source has a chance to write the UA workaround.
  #
  # Wipe the stale third-party lists upfront so the bootstrap update
  # only touches the distro mirror (no 418 risk). The repos are
  # re-added immediately below by _install_sury_source /
  # _install_nodesource_source with the UA workaround in place.
  #
  # Also clear any deb822 *.sources files dropped by a previous
  # `add-apt-repository ppa:ondrej/php` (or equivalent). Those embed
  # an inline `Signed-By: <pgp>` block that conflicts with our
  # signed-by=/usr/share/keyrings/sury-php.gpg, making apt error out
  # with "Conflicting values set for option Signed-By regarding
  # source ...".
  rm -f /etc/apt/sources.list.d/sury-php.list \
        /etc/apt/sources.list.d/nodesource.list \
        /etc/apt/sources.list.d/ondrej-ubuntu-php-*.sources \
        /etc/apt/sources.list.d/ondrej-ubuntu-php-*.list \
        /etc/apt/sources.list.d/ondrej-ubuntu-nginx-*.sources \
        /etc/apt/sources.list.d/ondrej-ubuntu-nginx-*.list \
        /etc/apt/sources.list.d/ondrej-nginx*.list

  # Bootstrap: `gpg` (from gnupg) + `curl` + `ca-certificates` must be
  # present BEFORE we add third-party repos (Sury, NodeSource) and verify
  # their GPG keys. Minimal LXC containers often ship without gnupg. Two
  # apt runs total (this bootstrap + the big install below) is still a
  # huge win over the pre-consolidation 6 calls.
  # Debian installed from a DVD/ISO leaves a `deb cdrom:` line in
  # /etc/apt/sources.list. Once the media is ejected, `apt-get update`
  # fails with exit 100 ("The repository 'cdrom://[...] trixie Release'
  # does not have a Release file"), which kills the whole install before
  # a single package is fetched — nothing ends up listening on :8443
  # (GH #207). Comment out any cdrom sources so apt only hits the network
  # mirrors. Covers both the legacy one-line format and deb822 *.sources.
  if grep -rqsE '^[[:space:]]*deb(-src)?[[:space:]]+cdrom:' /etc/apt/sources.list /etc/apt/sources.list.d/ 2>/dev/null; then
    _log "disabling cdrom apt source(s) left by the DVD install (GH #207)"
    sed -i -E 's|^([[:space:]]*deb(-src)?[[:space:]]+cdrom:)|#\1|' /etc/apt/sources.list 2>/dev/null || true
    for _f in /etc/apt/sources.list.d/*.list; do
      [[ -e "$_f" ]] || continue
      sed -i -E 's|^([[:space:]]*deb(-src)?[[:space:]]+cdrom:)|#\1|' "$_f" 2>/dev/null || true
    done
  fi
  for _f in /etc/apt/sources.list.d/*.sources; do
    [[ -e "$_f" ]] || continue
    if grep -qiE '^[[:space:]]*URIs:[[:space:]]*cdrom:' "$_f" 2>/dev/null; then
      _log "disabling deb822 cdrom apt source $_f (GH #207)"
      mv "$_f" "${_f}.disabled-by-jabali" 2>/dev/null || true
    fi
  done

  _spin "apt update (bootstrap)" \
    apt-get update -qq
  _spin "apt install bootstrap (gnupg + ca-certificates + curl)" \
    apt-get install -y -qq --no-install-recommends gnupg ca-certificates curl

  # Third-party repos added BEFORE the big install so one `apt-get update`
  # sees them and one `apt-get install` resolves everything together. Each
  # adder is idempotent (bails out if the source file already exists).
  _install_sury_source
  _install_nodesource_source

  _spin "apt update (with Sury + NodeSource)" \
    apt-get update -qq

  # PowerDNS's postinst would restart pdns before its MySQL backend is
  # configured (fails loudly with exit 99 + a systemctl status dump). Drop
  # a policy-rc.d that tells dpkg to skip service starts during this
  # install — every service in the batch (nginx, php-fpm, pdns) is
  # explicitly enabled+started later in its own step, so "don't auto-
  # start" is harmless across the board. The guard arms an EXIT trap so
  # the shim cannot leak if the apt batch dies (_die/_spin/ERR trap all
  # exit mid-function — that leak silently killed logrotate's nginx log
  # reopen host-wide on jabalitests).
  _policy_rc_install "one-shot base package batch"

  # Sury's PHP extension packaging drifts between versions (8.5 ships
  # OPcache inside -common instead of as a standalone -opcache package).
  # Probe apt-cache for each optional extension per PHP version and
  # include only what's actually available.
  local php_versions="${JABALI_PHP_VERSIONS:-8.4}"
  local -a php_extensions=()
  local version
  for version in $php_versions; do
    php_extensions+=("php${version}-fpm" "php${version}-cli")
    local ext
    # GH #606: redis (phpredis) + igbinary so WordPress object caching uses the
    # fast native client with persistent pconnect() instead of the pure-PHP RESP
    # fallback. igbinary gives compact/fast object-cache serialization.
    for ext in mysql mbstring zip gd curl xml intl bcmath opcache redis igbinary sqlite3; do
      if apt-cache show "php${version}-${ext}" >/dev/null 2>&1; then
        php_extensions+=("php${version}-${ext}")
      else
        _log "php${version}-${ext} not in apt sources — skipping (bundled elsewhere or unavailable)"
      fi
    done
  done

  # M39 (2026-04-30) removed Tetragon — bpftool is no longer required
  # at install time. Empty optional_pkgs preserved so the apt invocation
  # below still expands cleanly.
  local optional_pkgs=()

  # One big install. Downstream functions (install_nginx, _install_php_version,
  # install_node, install_powerdns, setup_certbot) short-circuit on
  # `command -v` / package-present checks now that the packages land here.
  local -a _base_pkgs=(
    git curl ca-certificates build-essential tar bzip2 unzip openssl gnupg sudo
    mariadb-server mariadb-client
    rsync acl
    systemd-resolved
    quota quotatool xfsprogs
    nginx
    certbot python3-certbot-nginx
    nodejs
    pdns-server pdns-backend-mysql pdns-recursor
    bind9-dnsutils
    ufw yq
    whois
    redis-server redis-tools
    dbus dbus-user-session
    bubblewrap debootstrap systemd-container
    yara
    ed inotify-tools
    logrotate
    acl
    unattended-upgrades
    restic zstd
    sshpass
    "${php_extensions[@]}"
    "${optional_pkgs[@]}"
  )
  if [[ -n "${JABALI_NONINTERACTIVE:-}" ]]; then
    # TUI install: emit apt's machine-readable progress (dlstatus/pmstatus lines)
    # on stdout via APT::Status-Fd=1 so the installer can drive its progress bar
    # with the real download/unpack percentage. The TUI parses + hides these
    # lines. _spin would swallow all output into a temp file, so bypass it here.
    _log "apt install system packages (this is the long one)…"
    local _aptrc=0
    DEBIAN_FRONTEND=noninteractive apt-get install -y -q -o APT::Status-Fd=1 \
      --no-install-recommends "${_base_pkgs[@]}" || _aptrc=$?
    [[ $_aptrc -eq 0 ]] || _die "apt install system packages FAILED (exit $_aptrc)"
    _ok "apt install system packages (this is the long one)"
  else
    _spin "apt install system packages (this is the long one)" \
      apt-get install -y -qq --no-install-recommends "${_base_pkgs[@]}"
  fi

  # Happy-path restore; the EXIT trap armed by _policy_rc_install covers
  # every other exit (die/_spin/ERR/signal).
  _policy_rc_restore

  # M6.3 Debian packaging fact-check (2026-04-22): pdns-server and
  # pdns-recursor both run as `pdns:pdns` on Debian — the recursor
  # package does NOT create its own `pdns-recursor` user/group. Our
  # recursor.conf below sets `setuid=pdns setgid=pdns` to match, and
  # recursor.forwards is chowned root:pdns so the daemon can read it.
  # The earlier hard-fail check against a `pdns-recursor` group was
  # wrong — it killed every clean install because the group never
  # existed. `pdns` group is guaranteed by pdns-server's postinst
  # (pdns-server is in the same apt batch above).
  if ! getent group pdns >/dev/null; then
    _die "pdns group missing after apt-install — pdns-server postinst failed; run 'apt-get install -f' and re-run install.sh"
  fi

  # Make systemd-resolved actually usable by the panel's DNS Resolvers
  # feature. Historically the installer just apt-installed the package
  # and left state untouched "so the admin's existing DNS isn't
  # disrupted" — but on a dedicated jabali-panel host there is no
  # pre-existing DNS-manager to preserve, and the effect of the
  # hands-off stance was that clicking "Save Resolvers" in the UI
  # appeared to succeed (drop-in written to disk) while doing nothing
  # useful (nobody reads the drop-in because resolved isn't running).
  #
  # So: normalize to "unmasked + enabled + running" on every install.
  # Only rewire /etc/resolv.conf if it's a plain regular file today —
  # if it's already a symlink, another tool (resolvconf, NetworkManager,
  # or a prior systemd-resolved setup) owns it and we must not fight
  # that. Idempotent across reinstalls.
  if [[ -n "$DNS_FORWARDER" ]]; then
    _log "DNS forwarder mode active — leaving systemd-resolved masked, /etc/resolv.conf direct to ${DNS_FORWARDER} over TCP"
    systemctl mask systemd-resolved.service 2>/dev/null || true
    systemctl stop systemd-resolved.service 2>/dev/null || true
    # Re-assert the plain resolv.conf in case a postinst clobbered it.
    chattr -i /etc/resolv.conf 2>/dev/null || true
    cat > /etc/resolv.conf <<EOF
# Managed by jabali install.sh (JABALI_DNS_FORWARDER=${DNS_FORWARDER}).
nameserver ${DNS_FORWARDER}
options use-vc timeout:5 attempts:2
EOF
    chattr +i /etc/resolv.conf 2>/dev/null || true
    # Drop the resolved NSS shim so glibc goes straight to dns -> resolv.conf -> use-vc.
    if grep -q "resolve \[!UNAVAIL=return\]" /etc/nsswitch.conf 2>/dev/null; then
      sed -i 's/ resolve \[!UNAVAIL=return\]//' /etc/nsswitch.conf
    fi
  else
    local resolved_state
    resolved_state="$(systemctl is-enabled systemd-resolved.service 2>/dev/null || true)"

    if [[ "$resolved_state" == "masked" ]]; then
      _log "unmasking systemd-resolved (was masked; image default or prior admin action)"
      systemctl unmask systemd-resolved.service
      resolved_state="disabled"
    fi

    if [[ "$resolved_state" != "enabled" ]] || ! systemctl is-active --quiet systemd-resolved.service; then
      _log "enabling + starting systemd-resolved"
      if ! systemctl enable --now systemd-resolved.service; then
        _warn "systemd-resolved failed to start — panel DNS Resolvers page will be non-functional until fixed manually (check 'journalctl -u systemd-resolved')"
      fi
    fi
  fi

  # Pre-seed the panel DNS drop-in when the host has no upstream
  # configured via /etc/resolv.conf AND no upstream advertised on any
  # link in resolvectl. Happens on Debian 13 minimal where resolv.conf
  # ships pre-symlinked to the stub but no link ever pushes DNS (static
  # IP install, or the QEMU/LXC DHCP dropped the DNS option). Without
  # this, any resolved restart later in install.sh (disable_llmnr does
  # one) exposes the "stub with zero upstream" state and every curl in
  # the rest of the script SERVFAILs.
  #
  # Only seeds when jabali.conf is missing — if the admin already wrote
  # one via the panel UI we do not clobber it.
  if systemctl is-active --quiet systemd-resolved.service; then
    local panel_dropin_early="/etc/systemd/resolved.conf.d/jabali.conf"
    if [[ ! -f "$panel_dropin_early" ]]; then
      # Active-resolution probe: does a well-known hostname actually
      # resolve right now? Cheaper + more reliable than parsing
      # `resolvectl status` (which has exit-code + output-format quirks
      # across systemd versions and can kill the script under
      # `set -euo pipefail`). If getent fails, we know any curl later
      # in install.sh will also fail — seed the fallback drop-in.
      if ! getent hosts deb.debian.org >/dev/null 2>&1; then
        _warn "no upstream DNS resolves (deb.debian.org lookup failed) — seeding ${panel_dropin_early} with Cloudflare + Quad9 (override via Admin → DNS)"
        install -d -m 0755 /etc/systemd/resolved.conf.d
        cat > "$panel_dropin_early" <<'EARLYDNS'
# Managed by jabali-panel — edits via /jabali-admin/settings → DNS.
# install.sh found no working upstream DNS and seeded these public
# defaults so curl/apt steps later in install.sh don't SERVFAIL.
[Resolve]
DNS=1.1.1.1 9.9.9.9
EARLYDNS
        chmod 0644 "$panel_dropin_early"
        systemctl restart systemd-resolved.service 2>/dev/null || true
        # Give resolved a beat to accept the drop-in before the next
        # step hits the network.
        sleep 1
      fi
    fi
  fi

  # Hand /etc/resolv.conf over to systemd-resolved's stub so queries
  # actually traverse the drop-in the panel writes. Gated on:
  #   1. resolv.conf is a plain file (not already a symlink — symlink
  #      means another manager owns it; don't stomp)
  #   2. systemd-resolved started successfully above (checking is-active
  #      as the cheapest post-start health probe)
  if [[ -z "$DNS_FORWARDER" ]] \
     && [[ ! -L /etc/resolv.conf && -e /etc/resolv.conf ]] \
     && systemctl is-active --quiet systemd-resolved.service; then

    # Before flipping the symlink, migrate the admin's existing DNS
    # config into resolved so the host doesn't go dark. If /etc/resolv.conf
    # has (say) corporate DNS at 10.0.0.1 + search corp.example.com,
    # a raw symlink flip would point all lookups at resolved, which
    # has no upstreams configured → every query SERVFAILs until the
    # admin visits the panel UI.
    #
    # Write harvested values to /etc/systemd/resolved.conf.d/jabali.conf
    # (the panel's own drop-in) — NOT a separate migrated.conf file —
    # so the panel UI shows Source: drop-in with the admin's previous
    # upstreams pre-filled, giving them a one-click point to modify.
    # Skip if jabali.conf already exists so re-running install.sh on a
    # host where the admin has already saved via the UI doesn't clobber
    # their panel-managed config.
    local panel_dropin="/etc/systemd/resolved.conf.d/jabali.conf"
    if [[ ! -f "$panel_dropin" ]]; then
      # Harvest nameservers: exclude only 127.0.0.53 (self-reference
      # once we symlink to the stub). Preserve everything else including
      # 127.0.0.1 (local dnsmasq/unbound) and RFC 1918 addresses
      # (corporate resolvers).
      local migrated_ns migrated_search
      migrated_ns="$(awk '/^nameserver[[:space:]]+/{print $2}' /etc/resolv.conf \
                     | grep -v '^127\.0\.0\.53$' \
                     | paste -sd' ' -)"
      # Take first search/domain directive (resolv.conf's older 'domain'
      # keyword is equivalent to a single-entry 'search').
      migrated_search="$(awk '/^(search|domain)[[:space:]]+/{print $2; exit}' /etc/resolv.conf)"

      # Fallback: if the host's /etc/resolv.conf had no harvestable
      # upstream (empty file, comments-only, or only 127.0.0.53),
      # seed the drop-in with Cloudflare + Quad9. Without this, the
      # symlink flip below points /etc/resolv.conf at a resolved stub
      # that has ZERO upstreams configured and the host goes dark —
      # exactly the "lost all DNS after install" failure we hit on
      # Debian 13 minimal images.
      local seed_source="migrated"
      if [[ -z "$migrated_ns" ]]; then
        migrated_ns="1.1.1.1 9.9.9.9"
        seed_source="fallback"
        _warn "no upstream harvested from /etc/resolv.conf — seeding panel drop-in with Cloudflare + Quad9 defaults (override via Admin → DNS)"
      fi

      _log "writing panel DNS drop-in (${seed_source}): ${migrated_ns}${migrated_search:+ (search: ${migrated_search})}"
      install -d -m 0755 /etc/systemd/resolved.conf.d
      {
        echo "# Managed by jabali-panel — edits via /jabali-admin/settings → DNS."
        if [[ "$seed_source" == "migrated" ]]; then
          echo "# Seeded by install.sh from the host's previous /etc/resolv.conf"
          echo "# so the host's DNS stayed working when install.sh handed"
          echo "# /etc/resolv.conf over to systemd-resolved's stub."
        else
          echo "# install.sh found no usable upstream in the host's previous"
          echo "# /etc/resolv.conf and seeded these public defaults so the"
          echo "# host didn't go dark after the symlink flip below."
        fi
        echo "# The admin can modify these upstreams via the panel UI at any"
        echo "# time; changes land in this same file."
        echo "[Resolve]"
        echo "DNS=${migrated_ns}"
        [[ -n "$migrated_search" ]] && echo "Domains=${migrated_search}"
      } > "$panel_dropin"
      chmod 0644 "$panel_dropin"
      systemctl restart systemd-resolved.service 2>/dev/null || true
    fi

    _log "linking /etc/resolv.conf → /run/systemd/resolve/stub-resolv.conf (was plain file)"
    ln -sf /run/systemd/resolve/stub-resolv.conf /etc/resolv.conf

    # Post-flip sanity probe. If DNS is broken after our changes, the
    # admin needs to know BEFORE we move on to 700 more lines of apt/
    # systemd work that might try to reach package registries. Warn
    # loudly but don't die — they can still fix it manually via
    # /etc/systemd/resolved.conf.d/jabali.conf.
    if ! getent hosts deb.debian.org >/dev/null 2>&1; then
      _warn "DNS still broken after resolved setup: 'getent hosts deb.debian.org' failed."
      _warn "Check: resolvectl status; cat /etc/systemd/resolved.conf.d/jabali.conf"
    fi
  fi

  # After the batch install, pin `php` / `php-config` / `phpize`
  # update-alternatives to the jabali-configured version.  The Debian
  # `php-cli` meta-package (pulled in transitively by some packages) registers
  # `php.default → php8.4` at priority 100 which silently wins over Sury's
  # php8.5 at priority 85, breaking WP-CLI, Composer, and any other `php`
  # invocation. Explicit --set overrides priority arithmetic entirely.
  local primary_version
  primary_version="$(echo "$php_versions" | awk '{print $NF}')"
  _log "pinning php alternatives to php${primary_version}"
  for _alt in php phar; do
    if [[ -f "/usr/bin/${_alt}${primary_version}" ]]; then
      update-alternatives --set "$_alt" "/usr/bin/${_alt}${primary_version}" 2>/dev/null || true
    fi
  done
  for _alt in php-config phpize; do
    if [[ -f "/usr/bin/${_alt}${primary_version}" ]]; then
      update-alternatives --set "$_alt" "/usr/bin/${_alt}${primary_version}" 2>/dev/null || true
    fi
  done

  # If php8.4 was pulled in as a transitive dependency (e.g., old Debian
  # `php-cli` meta-package) and 8.4 is NOT in JABALI_PHP_VERSIONS, purge it
  # now so no stale binaries remain.
  local _purge_versions=("8.4" "8.3" "8.2" "8.1" "8.0" "7.4")
  for _pv in "${_purge_versions[@]}"; do
    # Skip if this version is in the configured set
    if echo "$php_versions" | grep -qw "$_pv"; then
      continue
    fi
    # In-use guard: a version with a configured FPM tree is panel-managed and a
    # tenant pool uses it — provision_php_extensions reinstalls its ext packages
    # on the same signal, so purging here causes an install/uninstall flap on
    # every update. Only purge pure transitive php-cli pulls (no FPM tree).
    if [[ -d "/etc/php/${_pv}/fpm" ]]; then
      _log "keeping php${_pv} (configured FPM version in use)"
      continue
    fi
    if dpkg -l "php${_pv}-cli" 2>/dev/null | grep -q "^ii"; then
      # Preserve versions an admin installed on purpose via the panel PHP
      # version manager (GH #302): those packages are apt-"manual". Only purge
      # transitive/auto pulls (e.g. php8.4-cli dragged in by a meta-package).
      if apt-mark showmanual "php${_pv}-cli" 2>/dev/null | grep -qx "php${_pv}-cli"; then
        _log "keeping admin-installed php${_pv} (apt-manual, not auto-purged)"
        continue
      fi
      _log "purging stale php${_pv}-cli (auto/transitive, not in JABALI_PHP_VERSIONS)"
      apt-get purge -y -qq "php${_pv}*" 2>/dev/null || true
      apt-get autoremove -y -qq 2>/dev/null || true
    fi
  done

  # Install Composer from getcomposer.org using the configured PHP binary.
  # Do NOT use the Debian `composer` apt package — it depends on php-cli meta
  # which re-installs php8.4-cli and fights our update-alternatives settings.
  if ! command -v composer >/dev/null 2>&1; then
    _log "downloading composer installer from getcomposer.org"
    local _composer_tmp
    _composer_tmp="$(mktemp)"
    if curl -fsSL -o "$_composer_tmp" https://getcomposer.org/installer; then
      # Verify the installer's SHA-384 before executing it as root — this is
      # Composer's own documented procedure, and without it a MITM or a
      # compromise of getcomposer.org is immediate root code execution during
      # install/update. The signature is served from composer.github.io, a
      # DIFFERENT host than the installer, so an attacker has to compromise
      # both for the check to pass. Not pinned in-repo on purpose: the
      # installer is rebuilt upstream regularly and a stale pin would fail
      # every install.
      local _composer_sig _composer_sum
      _composer_sig="$(curl -fsSL --max-time 20 https://composer.github.io/installer.sig || true)"
      _composer_sum="$(sha384sum "$_composer_tmp" | awk '{print $1}')"
      if [[ -z "$_composer_sig" ]]; then
        rm -f "$_composer_tmp"
        _warn "could not fetch composer installer signature — refusing to run an unverified installer as root; composer unavailable"
      elif [[ "$_composer_sig" != "$_composer_sum" ]]; then
        rm -f "$_composer_tmp"
        _err "composer installer checksum mismatch (expected $_composer_sig, got $_composer_sum) — NOT executing it"
      else
        "php${primary_version}" "$_composer_tmp" \
          --install-dir=/usr/local/bin --filename=composer --quiet
        rm -f "$_composer_tmp"
        _ok "composer installed at /usr/local/bin/composer (installer signature verified)"
      fi
    else
      rm -f "$_composer_tmp"
      _warn "failed to download composer installer — composer will be unavailable"
    fi
  else
    _ok "composer already present"
  fi

  _ok "base packages ready"
}

# Note: systemd-resolved is installed, unmasked, enabled, started, and
# (when /etc/resolv.conf was previously a plain file) has
# /etc/resolv.conf pointed at its stub so the panel's DNS Resolvers
# page works end-to-end on a fresh install. Hosts where /etc/resolv.conf
# is a symlink to another manager's output are left untouched to avoid
# fighting that manager.

# ---------- step 1c.5: time sync (NTP via systemd-timesyncd) -----------------
#
# TOTP relies on the server clock matching real time within ±30s. Without
# NTP, drift on a long-uptime VM eventually invalidates every code the
# user generates and 2FA enrolment quietly stops working. Enforce
# systemd-timesyncd is up at install time and on every `jabali update`.
#
# Idempotent. Operator can switch to chrony / ntpd manually; this
# function detects that case (any time-sync service active) and skips.
# Timezone is left to the operator's existing /etc/timezone unless
# JABALI_TIMEZONE is exported (e.g. JABALI_TIMEZONE=UTC) to override.

install_time_sync() {
  _log "ensuring NTP time sync (TOTP-critical)"

  # If a non-default time-sync daemon is already running (chrony, ntpd,
  # openntpd), respect the operator's choice and skip.
  if systemctl is-active --quiet chrony 2>/dev/null \
      || systemctl is-active --quiet chronyd 2>/dev/null \
      || systemctl is-active --quiet ntp 2>/dev/null \
      || systemctl is-active --quiet ntpd 2>/dev/null \
      || systemctl is-active --quiet openntpd 2>/dev/null; then
    _ok "alternative time-sync daemon already active — leaving as-is"
  else
    # systemd-timesyncd ships with systemd on every Debian/Ubuntu host
    # but isn't always enabled on minimal cloud images.
    if ! systemctl is-enabled --quiet systemd-timesyncd 2>/dev/null; then
      systemctl enable --quiet systemd-timesyncd 2>/dev/null || true
    fi
    if ! systemctl is-active --quiet systemd-timesyncd 2>/dev/null; then
      if is_container; then
        # Host owns the clock inside a container; the unit cannot bind
        # to the kernel's clock-discipline syscalls and start-fail is
        # expected. Skip silently — clock will still be host-synced.
        :
      else
        systemctl start systemd-timesyncd 2>/dev/null || _warn "systemd-timesyncd failed to start"
      fi
    fi
    timedatectl set-ntp true 2>/dev/null || true
    _ok "systemd-timesyncd enabled"
  fi

  # Optional timezone override.
  if [[ -n "${JABALI_TIMEZONE:-}" ]]; then
    if [[ -f "/usr/share/zoneinfo/${JABALI_TIMEZONE}" ]]; then
      timedatectl set-timezone "$JABALI_TIMEZONE" || _warn "set-timezone $JABALI_TIMEZONE failed"
      _ok "timezone set to $JABALI_TIMEZONE"
    else
      _warn "JABALI_TIMEZONE='$JABALI_TIMEZONE' has no zoneinfo entry — leaving timezone unchanged"
    fi
  fi

  # Wait briefly for sync — fresh installs may show 'no' for the first
  # few seconds. Don't block forever; warn-and-continue if still off
  # so the install doesn't stall on a host with no internet.
  local i
  for i in 1 2 3 4 5 6; do
    if timedatectl show -p NTPSynchronized --value 2>/dev/null | grep -q '^yes$'; then
      _ok "system clock synchronized (NTPSynchronized=yes)"
      return 0
    fi
    sleep 2
  done

  _warn "system clock not yet NTPSynchronized after 12s — TOTP enrolment may fail until sync completes"
  _warn "  current state: $(timedatectl status 2>&1 | grep -E 'Local time|System clock|NTP' | head -3 | tr '\n' '|')"
}

# ---------- step 1d: M18 — cgroups v2 probe + disk quota + /tmp tmpfs -------
#
# Three idempotent setup steps that make the M18 per-user limits
# enforcement surfaces available:
#
# 1. Assert cgroups v2 unified hierarchy is the one in use. Debian 13's
#    default, but a host with a custom kernel command-line could have
#    systemd.unified_cgroup_hierarchy=0 which breaks every slice drop-in
#    we emit. Detect now, fail loud.
#
# 2. POSIX user quota on /home. Only runs on fresh hosts where we're
#    adding the mount option for the first time — on existing hosts
#    with a live /home we refuse to remount (would kill running FPM
#    workers). Branches by filesystem type: ext4 is a fstab edit +
#    quotacheck + quotaon; xfs also needs xfs_quota enable; btrfs/zfs
#    fail loud with the upgrade-path message.
#
# 3. /tmp on tmpfs with a size cap. Prevents a single user from filling
#    the host disk via /tmp bypassing their home quota. Default 1 GB,
#    configurable via JABALI_TMP_SIZE env.
configure_cgroups_v2() {
  local fstype
  fstype="$(stat -fc %T /sys/fs/cgroup 2>/dev/null || echo '')"
  if [[ "$fstype" != "cgroup2fs" ]]; then
    _err "cgroups v2 unified hierarchy is not active (/sys/fs/cgroup is $fstype)."
    _err "Boot with systemd.unified_cgroup_hierarchy=1 or remove the override."
    exit 1
  fi
  _ok "cgroups v2 unified hierarchy active"
}

# configure_disk_quota sets up POSIX user quota on /home. Idempotent,
# branches on filesystem type, prompts (TTY) or fails loud (unattended)
# when it can't make progress.
configure_disk_quota() {
  local home_mount home_fs
  # Find the mount /home lives on.
  home_mount="$(stat -c%m /home 2>/dev/null || echo /)"
  # Use findmnt for the precise fstype — `stat -fc %T` returns the
  # composite "ext2/ext3" label for the entire ext family because the
  # kernel's statfs f_type values are shared. findmnt asks the mount
  # table directly and returns "ext4" / "ext3" / "ext2" / "xfs"
  # exactly. Fall back to stat if findmnt isn't available (rare on
  # Debian 13).
  home_fs="$(findmnt -no FSTYPE "$home_mount" 2>/dev/null || stat -fc %T /home 2>/dev/null || echo unknown)"
  _log "quota probe: /home is on mount $home_mount (fs=$home_fs)"

  # Filesystem support matrix — ADR-0032 §2.
  # NB: `stat -fc %T` on Debian 13 reports "ext2/ext3" (composite label)
  # for ext3/ext4 filesystems — the kernel's statfs f_type values for
  # ext2/3/4 are merged for historical reasons. Match that label too.
  case "$home_fs" in
    ext4|ext3|ext2|"ext2/ext3")
      # ext4-family works with fstab usrquota + quotacheck + quotaon.
      ;;
    xfs)
      # xfs also works but needs xfs_quota enable after mount.
      ;;
    btrfs|zfs|tmpfs|ramfs)
      # Unsupported filesystems: warn and skip quota setup. cgroups v2
      # enforcement still works (cpu / memory / io / tasks) — we just
      # don't block the whole install on a disk-quota issue. The panel's
      # reconciler will log `quota_applied=false` on each apply; operator
      # reads the runbook to migrate /home when convenient.
      _warn "filesystem '$home_fs' on /home does not support POSIX quota; skipping disk-quota setup."
      _warn "cgroups limits (cpu/memory/io/tasks) will still apply. See plans/m18-resource-limits-runbook.md §4 to migrate."
      return 0
      ;;
    *)
      _warn "unknown filesystem '$home_fs' on /home; skipping disk-quota setup (cgroups still active)."
      return 0
      ;;
  esac

  # /home on / is supported (matches cPanel/DirectAdmin behavior). The
  # M18 reconciler only ever calls setquota for hosting UIDs (>=1000); root
  # and system daemons (UID < 1000) are exempt by absence — they never get
  # a setquota call, so EDQUOT can't trip them. ext4 supports online quota
  # via tune2fs -O quota + remount, no offline quotacheck needed.
  #
  # Operator can still opt out by setting JABALI_SKIP_QUOTA=1 in the env
  # before running install.sh (e.g. for hosts where the operator wants to
  # rely on cgroup IO caps only).
  if [[ "${JABALI_SKIP_QUOTA:-0}" == "1" ]]; then
    _warn "JABALI_SKIP_QUOTA=1 — skipping POSIX quota setup (cgroups still active)."
    return 0
  fi

  # Check whether fstab already has usrquota on this mount.
  if grep -E "^[^#]*\s$home_mount\s" /etc/fstab | grep -q "usrquota"; then
    _log "fstab: $home_mount already has usrquota set"
  else
    _log "adding usrquota,grpquota to /etc/fstab entry for $home_mount"
    # Preserve the original line; append ",usrquota,grpquota" to the
    # 4th field (options) for the mount-point row. Uses a unique marker
    # to avoid double-patching on reinstall.
    #
    # Prior awk implementation used `sub(regex, "\\1\\2,usrquota,…")`
    # relying on backreference expansion inside the replacement string.
    # POSIX awk does NOT support backrefs in sub/gsub replacements;
    # gawk supports `\1`…`\9` but only via --posix off and even then
    # treats `\1\2` inside a double-quoted shell string as literal.
    # The old code wrote the literal 10 characters `\1\2,usrquota,grpquota`
    # into fstab line 12, bricking the mount entry (systemd ignored the
    # line, / stayed mounted WITHOUT usrquota, every subsequent
    # quotacheck/quotaon failed with "Mountpoint has no quota enabled").
    #
    # Replacement: split the matched line by field index in awk so we
    # rebuild it explicitly. Preserves original whitespace collapsed to
    # single spaces, which systemd accepts.
    if ! grep -q "# jabali-m18-quota" /etc/fstab; then
      cp -p /etc/fstab /etc/fstab.jabali-m18.bak
      awk -v mnt="$home_mount" '
        !/^#/ && $2 == mnt {
          # $4 = current options field. Append ",usrquota,grpquota".
          $4 = $4 ",usrquota,grpquota"
          print $0 " # jabali-m18-quota"
          next
        }
        { print }
      ' /etc/fstab.jabali-m18.bak > /etc/fstab
      _ok "fstab patched; remount $home_mount for changes to take effect"
    fi
  fi

  # Remount to pick up the new options. We pass usrquota,grpquota on
  # the cmdline explicitly (not just "remount") so the kernel honors
  # them immediately — the fstab path alone depends on the line being
  # parsed cleanly, and any syntax drift would silently leave quota
  # off. Cmdline options are authoritative.
  if ! mount -o remount,usrquota,grpquota "$home_mount" 2>/dev/null; then
    _warn "remount of $home_mount failed (busy). Reboot to apply quota, then re-run this step."
    return 0
  fi

  # Filesystem-specific activation. "xfs" is the only branch that
  # needs the extra xfs_quota enable; the ext* family (including the
  # composite "ext2/ext3" label) falls through to the standard
  # quotacheck + quotaon sequence below.
  if [[ "$home_fs" == "xfs" ]]; then
    # xfs's mount option alone doesn't flip accounting on — need
    # xfs_quota's enable command.
    xfs_quota -x -c "enable -u" "$home_mount" || {
      _err "xfs_quota enable failed on $home_mount"
      exit 1
    }
    _ok "xfs user quota enabled on $home_mount"
  else
    # ext2/ext3/ext4.
    #
    # Two kernel paths:
    #
    # 1. Hidden-inode quota (modern, default on Debian 13 mkfs.ext4 since
    #    enable_quota=true is the /etc/mke2fs.conf default). The `quota`
    #    feature is baked into the superblock at format time, quota
    #    inodes live at fixed reserved inode numbers, and the kernel
    #    keeps accounting inline. No aquota.user file. No quotacheck
    #    scan. `quotaon` simply flips enforcement on — works live on a
    #    busy root filesystem.
    #
    # 2. External-file quota (legacy, pre-Debian-12 or custom mkfs). Uses
    #    aquota.user / aquota.group at the mount root, rebuilt by
    #    quotacheck. Fragile on a busy `/` because quotacheck wants to
    #    scan every inode without concurrent writes; kernel refuses on
    #    live root FS with certain version combos.
    #
    # Detection: tune2fs -l on the backing block device. If the
    # Filesystem features list contains `quota`, use path 1; else
    # fall back to path 2 (works on dedicated /home partitions).
    local block_dev
    block_dev="$(findmnt -no SOURCE "$home_mount" 2>/dev/null || true)"
    local has_sb_quota=0
    if [[ -n "$block_dev" ]] \
        && tune2fs -l "$block_dev" 2>/dev/null \
           | awk -F: '/^Filesystem features:/{print $2}' \
           | tr ' ' '\n' | grep -qw 'quota'; then
      has_sb_quota=1
    fi

    if (( has_sb_quota == 1 )); then
      # Hidden-inode path. quotaon uses the SB feature directly — no
      # aquota.user required. Works on a live `/` because the kernel
      # has been tracking usage since mount time; quotaon just flips
      # the enforce bit.
      if ! quotaon -v "$home_mount" >/dev/null 2>&1; then
        # quotaon returns non-zero when quota is already on some versions —
        # probe to tell "already on" apart from "real failure".
        if quotaon -pu "$home_mount" 2>/dev/null | grep -qi 'is on'; then
          :
        else
          _warn "quotaon $home_mount failed despite SB quota feature; manual intervention required (try 'quotaon -vu $home_mount')"
          return 0
        fi
      fi
      _ok "POSIX user quota active on $home_mount (hidden inodes)"
    else
      # Legacy external-file path. Fragile on busy `/`; reliable on
      # dedicated /home partitions.
      local quota_file="$home_mount/aquota.user"
      [[ "$home_mount" == "/" ]] && quota_file="/aquota.user"
      if [[ ! -f "$quota_file" ]]; then
        _log "building $quota_file via quotacheck -mcug (may take time on large filesystems)"
        if ! quotacheck -mcugF vfsv1 "$home_mount"; then
          _warn "quotacheck failed on $home_mount; quota plumbing left inactive (cgroups still enforce cpu/mem/io/tasks)"
          return 0
        fi
      fi
      if ! quotaon -v "$home_mount" >/dev/null 2>&1; then
        _warn "quotaon $home_mount failed; quota plumbing left inactive"
        return 0
      fi
      _ok "POSIX user quota active on $home_mount (external aquota.user)"
    fi
  fi
}

# configure_tmp_tmpfs mounts /tmp as tmpfs with a size cap so a user
# can't bypass their home quota via /tmp writes. Default 1 GB, override
# via JABALI_TMP_SIZE (passed as a tmpfs-compatible size string, e.g.
# "2G" or "512M").
configure_tmp_tmpfs() {
  local size="${JABALI_TMP_SIZE:-1G}"

  # If /tmp is already tmpfs, nothing to do.
  if [[ "$(stat -fc %T /tmp 2>/dev/null)" == "tmpfs" ]]; then
    _log "/tmp already on tmpfs; leaving as-is"
    return 0
  fi

  # Add fstab entry idempotently; reboot or remount picks it up.
  if ! grep -q "# jabali-m18-tmp" /etc/fstab; then
    _log "adding tmpfs mount for /tmp (size=$size) to /etc/fstab"
    echo "tmpfs /tmp tmpfs rw,nosuid,nodev,size=$size,mode=1777 0 0 # jabali-m18-tmp" >> /etc/fstab
  fi

  # Do NOT remount /tmp automatically on an existing host — running
  # processes often hold open file handles in /tmp (package managers,
  # editors, systemd timers) and remounting would corrupt them. Leave
  # the fstab change for the next reboot.
  _warn "/tmp fstab entry added; reboot to activate tmpfs mount with size=$size cap"
}

# ---------- step 1b: nginx ----------------------------------------------------

install_nginx() {
  # nginx is installed in install_base_packages's one-shot apt batch.
  # This function owns the post-install config (vhost dirs, include
  # line, enable+start). Kept as a separate step so the ordering in
  # main() stays readable and so reinstalls re-run the config checks.
  if ! command -v nginx >/dev/null 2>&1; then
    _die "nginx binary not found — install_base_packages should have installed it"
  fi
  _ok "nginx present ($(nginx -v 2>&1 | awk -F/ '{print $2}'))"

  # Ensure sites-available / sites-enabled dirs exist (some minimal
  # nginx packages skip them).
  install -d -m 0755 /etc/nginx/sites-available
  install -d -m 0755 /etc/nginx/sites-enabled

  # Enable the include if not already present.
  if ! grep -q 'sites-enabled' /etc/nginx/nginx.conf 2>/dev/null; then
    _log "adding sites-enabled include to nginx.conf"
    sed -i '/http {/a \    include /etc/nginx/sites-enabled/*.conf;' /etc/nginx/nginx.conf
  fi

  systemctl enable --quiet nginx
  systemctl start nginx 2>/dev/null || true
}

# ---------- step 1b2: PHP/FPM (multi-version via Sury) -------------------------

_install_sury_source() {
  # Sury GPG fingerprint validation. Source: https://packages.sury.org/php/README.txt
  # Last verified: 2026-04-17 (DPA CA Certificate, Ondřej Surý)
  local SURY_GPG_FINGERPRINT="15058500A0235D97F5D10063B188E2B695BD4743"
  # The launchpad PPA hosts the SAME upstream packages (Ondřej Surý
  # maintains both) signed by the same key, but is served from
  # launchpad.net rather than Fastly — bypassing the datacenter-IP
  # 418 false-positives. We prefer it on Ubuntu and fall back to
  # packages.sury.org for Debian (no PPA there).
  local LP_GPG_FINGERPRINT="14AA40EC0831756756D7F66C4F4EA0AAE5267A6C"

  # Always write the Fastly 418 UA workaround, even when the .list
  # below short-circuits — earlier installs from before this fix
  # landed have the source list but not the apt.conf.d override, and
  # they crash on every apt-get update. Idempotent: writing the same
  # bytes is a noop.
  _install_sury_apt_ua_workaround

  [[ -f /etc/apt/sources.list.d/sury-php.list ]] && { _ok "Sury PHP source already configured"; return; }

  # Derive the distro id + codename without depending on lsb_release
  # (not installed on minimal Debian 13). /etc/os-release is a
  # systemd-era standard and is always present.
  local distro_id codename
  if [[ -r /etc/os-release ]]; then
    # shellcheck disable=SC1091
    distro_id=$(. /etc/os-release && echo "${ID:-}")
    # shellcheck disable=SC1091
    codename=$(. /etc/os-release && echo "${VERSION_CODENAME:-}")
  fi
  [[ -n "$codename" ]] || _die "cannot determine distro codename (no VERSION_CODENAME in /etc/os-release)"

  # Ensure target dir exists on minimal Debian images.
  install -d -m 0755 /usr/share/keyrings

  if [[ "$distro_id" == "ubuntu" ]]; then
    # ppa:ondrej/php — same packages as packages.sury.org, served by
    # launchpad. No Fastly in front, no 418 risk. The launchpad
    # signing key has its own fingerprint distinct from Sury's.
    _log "fetching Ubuntu PPA signing key for ondrej/php"
    curl -fsSL --connect-timeout 15 --max-time 60 \
      "https://keyserver.ubuntu.com/pks/lookup?op=get&search=0x${LP_GPG_FINGERPRINT}" \
      | gpg --no-default-keyring --no-tty --batch --dearmor --yes \
        -o /usr/share/keyrings/sury-php.gpg \
      || _die "failed to fetch ondrej/php signing key from keyserver.ubuntu.com"

    local lp_gpg_out
    if ! lp_gpg_out="$(GNUPGHOME="$(mktemp -d)" gpg --no-default-keyring --no-tty --batch --show-keys /usr/share/keyrings/sury-php.gpg 2>&1)"; then
      _err "gpg --show-keys failed; output was:"
      printf '%s\n' "$lp_gpg_out" >&2
      _die "cannot parse PPA key at /usr/share/keyrings/sury-php.gpg"
    fi
    if ! grep -q "$LP_GPG_FINGERPRINT" <<< "$lp_gpg_out"; then
      _err "gpg parsed the key but the fingerprint doesn't match. gpg output:"
      printf '%s\n' "$lp_gpg_out" >&2
      _die "ondrej/php PPA key fingerprint mismatch. Expected: $LP_GPG_FINGERPRINT"
    fi
    _ok "ondrej/php PPA signing key validated"

    cat > /etc/apt/sources.list.d/sury-php.list <<EOF
deb [signed-by=/usr/share/keyrings/sury-php.gpg] https://ppa.launchpadcontent.net/ondrej/php/ubuntu ${codename} main
EOF
    _ok "added ondrej/php PPA for ${codename} (launchpad mirror — bypasses Fastly)"
    return
  fi

  # Debian: packages.sury.org is the only option. The Fastly 418
  # affects fewer Debian-on-VPS installs in practice; if it bites,
  # the operator is currently the one to debug.
  _log "downloading Sury GPG key (curl: connect 15s, total 60s)"
  curl -fsSL --connect-timeout 15 --max-time 60 \
    https://packages.sury.org/php/apt.gpg -o /usr/share/keyrings/sury-php.gpg \
    || _die "curl failed to fetch Sury GPG key from packages.sury.org — check egress / DNS from this host"

  _log "verifying Sury GPG key fingerprint"
  # Capture gpg output + exit code independently so we can surface both
  # to the operator if anything goes wrong. The `if ! cmd` form disables
  # set -e just for the capture. GNUPGHOME=mktemp skips any ~/.gnupg /
  # gpg-agent startup, which hangs silently on first-gpg invocation
  # inside minimal LXC containers.
  local sury_gpg_out
  if ! sury_gpg_out="$(GNUPGHOME="$(mktemp -d)" gpg --no-default-keyring --no-tty --batch --show-keys /usr/share/keyrings/sury-php.gpg 2>&1)"; then
    _err "gpg --show-keys failed; output was:"
    printf '%s\n' "$sury_gpg_out" >&2
    _die "cannot parse Sury GPG key at /usr/share/keyrings/sury-php.gpg"
  fi
  if ! grep -q "$SURY_GPG_FINGERPRINT" <<< "$sury_gpg_out"; then
    _err "gpg parsed the key but the fingerprint doesn't match. gpg output:"
    printf '%s\n' "$sury_gpg_out" >&2
    _die "Sury GPG key fingerprint mismatch. Expected: $SURY_GPG_FINGERPRINT"
  fi
  _ok "Sury GPG key validated"

  cat > /etc/apt/sources.list.d/sury-php.list <<EOF
deb [signed-by=/usr/share/keyrings/sury-php.gpg] https://packages.sury.org/php/ ${codename} main
EOF
  _ok "added Sury PHP repository for ${codename}"
}

# packages.sury.org sits behind Fastly; the Fastly edge returns HTTP
# 418 ("I'm a teapot") when apt's default User-Agent
# ("Debian APT-HTTP/1.3 (...)") arrives from a flagged datacenter IP.
# Override the User-Agent for Sury fetches only — keep the default
# elsewhere so we don't muddy other repos' bot heuristics. Fastly
# accepts a plain Mozilla string. Also bumps Acquire::Retries so
# transient network blips don't crash the whole install.
#
# Split out from _install_sury_source so it always runs (the source
# function early-returns when the .list exists, but a re-run on a
# half-installed host still needs this conf written).
_install_sury_apt_ua_workaround() {
  cat > /etc/apt/apt.conf.d/98-jabali-sury-ua.conf <<'APTEOF'
// Workaround Fastly 418 on packages.sury.org (Debian/Ubuntu
// datacenter-IP false positives). Per-host User-Agent overrides
// in apt's syntax are unreliable across versions; setting it
// globally is the only form that consistently works. Other
// archives don't care what the apt client identifies as.
Acquire::http::User-Agent "Mozilla/5.0 (X11; Linux x86_64) jabali-panel-installer";
Acquire::https::User-Agent "Mozilla/5.0 (X11; Linux x86_64) jabali-panel-installer";
Acquire::Retries "3";
APTEOF
}

_install_php_version() {
  local version="$1"
  # PHP packages (php<v>-fpm, php<v>-cli, optional extensions) are
  # installed in install_base_packages's one-shot apt batch. This
  # function owns the per-version post-install config: placeholder
  # pool, FPM mask, default-pool disable.
  if ! command -v "php${version}" >/dev/null 2>&1; then
    _die "php${version} binary not found — install_base_packages should have installed it (check JABALI_PHP_VERSIONS=\"${JABALI_PHP_VERSIONS:-8.4}\")"
  fi
  _ok "PHP ${version} present"

  local pool_file="/etc/php/${version}/fpm/pool.d/www.conf"
  [[ -f "$pool_file" ]] && { mv "$pool_file" "${pool_file}.disabled"; _log "disabled default pool for PHP ${version}"; }

  # Install a placeholder pool so php-fpm can start with no hosting
  # users yet. Without it, an empty pool.d/ causes FPM init to fail
  # ("No pool defined"). Inlined via heredoc because install_php runs
  # before clone_or_update_repo — we don't yet have the repo tree to
  # copy from. A copy also exists at install/php/_jabali-placeholder.conf
  # for reference; the heredoc here is the source of truth the installer
  # actually uses.
  cat > "/etc/php/${version}/fpm/pool.d/_jabali-placeholder.conf" <<'PLACEHOLDER_EOF'
; Placeholder pool installed by install.sh so php-fpm can start on a
; fresh host with no hosting users yet. No-op ondemand pool listening
; on an unused loopback socket. Safe to leave in place. Moot once
; slices plan step 6 masks the global php<v>-fpm.service in favor of
; per-user masters (jabali-fpm@<user>.service).

[_jabali_placeholder]
user = www-data
group = www-data
listen = /run/php/php-fpm-jabali-placeholder.sock
listen.owner = www-data
listen.group = www-data
listen.mode = 0600
pm = ondemand
pm.max_children = 1
pm.process_idle_timeout = 10s
PLACEHOLDER_EOF
  chmod 0644 "/etc/php/${version}/fpm/pool.d/_jabali-placeholder.conf"
  _ok "installed placeholder pool for PHP ${version}"

  # GH #1332 item 9: Xdebug is opt-in PER POOL via a per-slug PHP_INI_SCAN_DIR
  # (fpm-exec adds /etc/php/<v>/jabali-ext/<slug>). Install the .so but keep it
  # GLOBALLY DISABLED — enabled globally it would tax every request on every
  # site fleet-wide. The base scan-dir parent is pre-created so the agent can
  # drop per-slug ini files into it.
  install -d -m 0755 "/etc/php/${version}/jabali-ext"
  if DEBIAN_FRONTEND=noninteractive apt-get install -y "php${version}-xdebug" >/dev/null 2>&1; then
    phpdismod -v "${version}" xdebug 2>/dev/null || true
    _ok "PHP ${version} Xdebug installed (globally off; opt-in per pool)"
  else
    _warn "php${version}-xdebug unavailable — per-pool Xdebug will be off for ${version}"
  fi


  # Mask the distro's global php<v>-fpm.service — per ADR-0025 we run
  # one FPM master per hosting user (jabali-fpm@<user>.service) inside
  # the per-user systemd slice, and a dedicated jabali-fpm@pma.service
  # for phpMyAdmin. The global unit must never run: it reads every
  # .conf in /etc/php/<v>/fpm/pool.d/ (including jabali-pma.conf and
  # jabali-<user>.conf), so on any apt transaction its postinst would
  # restart it and race the per-user masters for their UDS sockets,
  # leaving dpkg in a permanently half-configured state.
  #
  # apt's postinst unconditionally enables + starts the service, so
  # mask AFTER the package install has run and reset-failed any
  # residual failed state from a prior half-configured boot.
  systemctl reset-failed "php${version}-fpm.service" 2>/dev/null || true
  systemctl disable --now --quiet "php${version}-fpm.service" 2>/dev/null || true
  systemctl mask --quiet "php${version}-fpm.service"
  _ok "PHP ${version} installed; global php${version}-fpm.service masked (per-user jabali-fpm@<user>.service takes over)"
}

install_python_apps_runtime() {
  # ADR-0131 / GH #203: prerequisites for the Python Application Manager.
  # Installs the default python3 venv tooling + build toolchain so user
  # virtualenvs can compile C-extension wheels (psycopg2, cryptography, lxml).
  # Cheap + idempotent; the feature itself stays opt-in (python_apps_enabled).
  #
  # GH #1352: mysqlclient — the standard Django/Wagtail MySQL/MariaDB driver —
  # ships NO Linux wheels, so it always builds from source and needs the MariaDB
  # client dev headers + `mysql_config`. Since the panel ships MariaDB as the
  # default DB, a tenant Python app talking to it is mainstream; without these
  # the build dies with "OSError: mysql_config not found". default-libmysqlclient-dev
  # Depends: (not Recommends) libmariadb-dev + libmariadb-dev-compat, so the
  # mysql_config symlink comes in even under --no-install-recommends. pkg-config
  # is what newer mysqlclient (2.2+) uses to locate the client library.
  _log "installing Python app runtime prerequisites (venv + build toolchain)"
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
    python3 python3-venv python3-dev build-essential libffi-dev libssl-dev \
    default-libmysqlclient-dev pkg-config \
    >>"${LOG_FILE:-/dev/null}" 2>&1 || {
      _warn "Python app runtime prereqs failed to install — Python apps may not build until resolved"
      return 0
    }
  _ok "Python app runtime prerequisites installed"
}

install_php() {
  _log "configuring PHP/FPM (packages installed in base batch; this runs per-version post-install config)"
  # Default install is PHP 8.4 — phpMyAdmin 5.2.x cannot run on PHP 8.5
  # (GH#111), and the panel's dedicated pma FPM pool reuses this default.
  # Sury supports 7.4–8.5; set JABALI_PHP_VERSIONS to install additional
  # versions side-by-side, e.g. JABALI_PHP_VERSIONS="7.4 8.2 8.4" bash install.sh
  local php_versions="${JABALI_PHP_VERSIONS:-8.4}"
  local version
  for version in $php_versions; do
    _install_php_version "$version"
  done

  # Per-user CLI PHP wrapper on PATH for ALL login shells (GH #184).
  # The agent writes /home/<user>/.jabali/bin/php -> /usr/bin/php<pinned>,
  # and jabali-ssh-shell already puts that dir on PATH inside its sandbox.
  # But users whose login shell is a plain bash (migrated / manually
  # created), or access via `su -`, `sudo -u`, or an IDE remote, bypass
  # the sandbox and resolve a bare `php` to the host default — so the
  # panel-selected version + its extensions looked "missing" at the CLI.
  # This profile.d snippet prepends the per-user wrapper dir in EVERY
  # login shell; it is a no-op for users without a pin (no .jabali/bin).
  cat >/etc/profile.d/jabali-php-cli.sh <<'EOF'
# jabali: prepend the per-user pinned PHP CLI wrapper (GH #184) so php,
# composer, and wp-cli use the panel-selected PHP version in every login
# shell, not just the jabali-ssh-shell sandbox. No-op without a pin.
if [ -d "$HOME/.jabali/bin" ]; then
  case ":$PATH:" in
    *":$HOME/.jabali/bin:"*) ;;
    *) PATH="$HOME/.jabali/bin:$PATH" ;;
  esac
fi
EOF
  chmod 0644 /etc/profile.d/jabali-php-cli.sh
  _ok "per-user CLI PHP wrapper wired into login-shell PATH (/etc/profile.d/jabali-php-cli.sh)"
}


# ---------- systemd slices: jabali root + user container ----------------------

# Install the top-of-hierarchy slice units and the FPM template service unit.
# Must run AFTER clone_or_update_repo because the unit files and shim scripts
# live under $REPO_DIR. No per-user provisioning yet (that happens in step 3).
install_jabali_slices() {
  _log "installing jabali systemd slices and FPM template"

  install -d -m 0755 /usr/local/libexec/jabali
  install -m 0755 "$REPO_DIR/install/systemd/fpm-pre-start" /usr/local/libexec/jabali/fpm-pre-start
  install -m 0755 "$REPO_DIR/install/systemd/fpm-exec" /usr/local/libexec/jabali/fpm-exec
  install -m 0755 "$REPO_DIR/install/systemd/fpm-post-start" /usr/local/libexec/jabali/fpm-post-start
  # JAB-213: free-hostname heartbeat helper (always installed; the timer below
  # is only enabled when the box actually uses a free jabalihosted.com name).
  install -m 0755 "$REPO_DIR/install/hostname/jabali-hostname-heartbeat.sh" /usr/local/libexec/jabali/jabali-hostname-heartbeat.sh
  # JAB-213 phase 3b: wildcard-cert DNS-01 hooks + issuance wrapper.
  install -m 0755 "$REPO_DIR/install/hostname/certbot-auth-hook.sh" /usr/local/libexec/jabali/certbot-auth-hook.sh
  install -m 0755 "$REPO_DIR/install/hostname/certbot-cleanup-hook.sh" /usr/local/libexec/jabali/certbot-cleanup-hook.sh
  install -m 0755 "$REPO_DIR/install/hostname/jabali-hostname-cert.sh" /usr/local/libexec/jabali/jabali-hostname-cert.sh
  # cron-precheck is the ExecStartPre guard generated cron .service units
  # reference (panel-agent buildCronServiceContent). Without it the unit
  # dies 203/EXEC and scheduled crons never run.
  install -m 0755 "$REPO_DIR/install/systemd/cron-precheck" /usr/local/libexec/jabali/cron-precheck

  install -m 0644 "$REPO_DIR/install/systemd/jabali.slice" /etc/systemd/system/jabali.slice
  install -m 0644 "$REPO_DIR/install/systemd/jabali-user.slice" /etc/systemd/system/jabali-user.slice
  install -m 0644 "$REPO_DIR/install/systemd/jabali-fpm@.service" /etc/systemd/system/jabali-fpm@.service
  # GH #1146: per-subaccount WebDAV worker + its activation socket (templates
  # only; the agent writes per-instance drop-ins on webdav_access grant). Core,
  # not gated on the ftp module — WebDAV is HTTPS-served and also covers SFTP
  # subaccounts.
  install -m 0644 "$REPO_DIR/install/systemd/jabali-webdav@.service" /etc/systemd/system/jabali-webdav@.service
  install -m 0644 "$REPO_DIR/install/systemd/jabali-webdav@.socket" /etc/systemd/system/jabali-webdav@.socket
  # GH #1146 step 4: the privileged auth_request authenticator (enabled + started
  # later in install_nginx_panel_vhost, once the binary + /run dir + group exist).
  install -m 0644 "$REPO_DIR/install/systemd/jabali-webdav-auth.service" /etc/systemd/system/jabali-webdav-auth.service
  # jabali-webdav is the Phase-4 auth gate (nginx authenticator checks
  # membership); the agent also creates it on demand, but seed it here so a host
  # that grants webdav_access before the agent redeploys still has it.
  if ! getent group jabali-webdav >/dev/null; then
    groupadd --system jabali-webdav
    _ok "jabali-webdav system group created"
  fi
  # /run/jabali-webdav holds the per-instance activation sockets. /run is tmpfs,
  # so a tmpfiles.d line recreates it on boot; --create makes it now. 0755
  # root:root — nginx (www-data) traverses via o+x; each socket inside is 0660
  # group jabali-sockets (SocketGroup in the .socket unit), so only nginx and
  # the worker can connect.
  local webdav_tf=/etc/tmpfiles.d/jabali-webdav.conf
  local webdav_tf_want='# Managed by jabali install.sh — GH #1146 WebDAV activation socket dir.
d /run/jabali-webdav 0755 root root -'
  if [[ ! -f "$webdav_tf" ]] || ! cmp -s <(printf '%s\n' "$webdav_tf_want") "$webdav_tf"; then
    printf '%s\n' "$webdav_tf_want" >"$webdav_tf"
    chmod 0644 "$webdav_tf"
  fi
  systemd-tmpfiles --create "$webdav_tf" 2>/dev/null || install -d -m 0755 /run/jabali-webdav
  # JAB-213: heartbeat timer units (enabled conditionally below).
  install -m 0644 "$REPO_DIR/install/hostname/jabali-hostname-heartbeat.service" /etc/systemd/system/jabali-hostname-heartbeat.service
  install -m 0644 "$REPO_DIR/install/hostname/jabali-hostname-heartbeat.timer" /etc/systemd/system/jabali-hostname-heartbeat.timer
  # Enable the daily heartbeat only when this box uses a free hostname.
  if [[ -r /etc/jabali-panel/hostname.env ]]; then
    systemctl enable --now jabali-hostname-heartbeat.timer >/dev/null 2>&1 \
      && _ok "free-hostname heartbeat timer enabled" \
      || _warn "could not enable free-hostname heartbeat timer (non-fatal)"
  fi

  systemctl daemon-reload
  systemctl start jabali.slice jabali-user.slice

  _ok "jabali slices installed"
}

# Install the FPM pool config template. Must run AFTER
# clone_or_update_repo because the template file lives under $REPO_DIR.
# The agent reads this path at runtime via php.pool.apply.
install_php_pool_template() {
  mkdir -p /etc/jabali-panel
  install -d -m 0755 -o root -g root /etc/jabali-panel/fpm
  install -d -m 0755 -o root -g root /etc/jabali-panel/user-phpver
  # JAB-230: relay-credential tree for the jabali-sendmail shim. 0711 — tenant
  # PHP must traverse into its own 0750 root:<usergroup> subdir but must not
  # enumerate other users. Subdirs + creds are written by the agent.
  install -d -m 0711 -o root -g root /etc/jabali-panel/sendmail
  local template_src="$REPO_DIR/install/php/jabali-php-pool.conf.tmpl"
  local template_dst="/etc/jabali-panel/php-pool.conf.tmpl"
  if [[ ! -f "$template_src" ]]; then
    _die "pool template missing at $template_src (is the repo clone complete?)"
  fi
  local template_changed=0
  if [[ ! -f "$template_dst" ]] || ! cmp -s "$template_src" "$template_dst"; then
    template_changed=1
  fi
  install -m 0644 "$template_src" "$template_dst"
  _ok "installed pool config template at $template_dst"
  # GH #253: when the template content changed, flag existing pools so the
  # reconciler re-renders them with the new defaults — otherwise ReconcilePHPPools
  # skips already-active pools and only NEW pools would pick up the change.
  if [[ "$template_changed" == 1 ]] && command -v mariadb >/dev/null 2>&1; then
    if mariadb -N -e "SELECT 1 FROM information_schema.tables WHERE table_schema='jabali_panel' AND table_name='php_pools' LIMIT 1" 2>/dev/null | grep -q 1; then
      mariadb jabali_panel -e "UPDATE php_pools SET status='pending' WHERE status='active';" 2>/dev/null \
        && _ok "pool template changed — flagged active pools for re-render" \
        || _warn "could not flag pools for re-render (non-fatal)"
    fi
  fi
}

# ---------- step 1c: disabled page -------------------------------------------

install_disabled_page() {
  _log "installing branded disabled page"

  # Create the directories with proper permissions
  install -d -m 0755 /var/www/jabali-disabled
  # GH error pages: shared docroot for the branded 404/403/500 pages
  # (vhost error_page -> /var/www/jabali-errors). Files are converged from
  # the editable page_template rows by the reconciler within one tick.
  install -d -m 0755 /var/www/jabali-errors
  # GH #860: shared docroot for the opt-in "domain not configured" page,
  # served by the default catch-all only when the admin enables it.
  install -d -m 0755 /var/www/jabali-unconfigured

  # GH #860: the pages are editable page_template rows converged by the
  # reconciler, so only SEED them here — an unconditional write would
  # clobber the operator's edit on every `jabali update`.
  if [[ ! -f /var/www/jabali-disabled/index.html ]]; then
    install -m 0644 /dev/stdin /var/www/jabali-disabled/index.html <<'EOF'
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>Site Disabled</title>
  <style>
    body { font-family: system-ui, -apple-system, Segoe UI, Roboto, sans-serif; max-width: 640px; margin: 4rem auto; padding: 0 1.25rem; color: #222; line-height: 1.5; }
    h1 { color: #d32f2f; margin-bottom: 0.25em; }
    .muted { color: #666; margin-top: 0; }
  </style>
</head>
<body>
  <h1>Site Disabled</h1>
  <p class="muted">This site has been disabled by its owner. Please check back later.</p>
</body>
</html>
EOF
  fi

  if [[ ! -f /var/www/jabali-unconfigured/index.html ]]; then
    install -m 0644 /dev/stdin /var/www/jabali-unconfigured/index.html <<'EOF'
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>Domain Not Configured</title>
  <style>
    body { font-family: system-ui, -apple-system, Segoe UI, Roboto, sans-serif; max-width: 640px; margin: 4rem auto; padding: 0 1.25rem; color: #222; line-height: 1.5; }
    h1 { margin-bottom: 0.25em; }
    .muted { color: #666; margin-top: 0; }
  </style>
</head>
<body>
  <h1>Domain not configured</h1>
  <p class="muted">This domain points at this server, but no website has been set up for it yet.</p>
</body>
</html>
EOF
  fi

  # GH #860: catch-all mode include for the default vhost. Seed the shipped
  # `return 444` drop; the agent rewrites this file when the admin flips the
  # unconfigured-domain page toggle, so never overwrite an existing one.
  if [[ ! -f /etc/nginx/jabali-catchall.conf ]]; then
    install -m 0644 /dev/stdin /etc/nginx/jabali-catchall.conf <<'EOF'
# Managed by jabali-agent (page_templates.sync_error_pages) — do not edit.
# Unconfigured-domain page is DISABLED (default): unknown hosts are dropped
# without a response, so scanners get nothing to fingerprint.
return 444;
EOF
  fi

  _ok "disabled page installed at /var/www/jabali-disabled/"
}

# ---------- step 1d: Node.js 22 LTS (for panel-ui) --------------------------

_install_nodesource_source() {
  # Idempotent NodeSource repo add. Called from install_base_packages
  # before the one-shot apt batch so nodejs resolves against
  # deb.nodesource.com/node_22.x instead of Debian's older nodejs.
  [[ -f /etc/apt/sources.list.d/nodesource.list ]] && { _ok "NodeSource repo already configured"; return; }

  _log "adding NodeSource repo for Node.js 22 (curl: connect 15s, total 60s)"
  install -d -m 0755 /etc/apt/keyrings
  # Fetch → tmp file so a network error surfaces distinctly from a gpg
  # parsing error. Same hang/diagnostic story as _install_sury_source.
  local ns_armored
  ns_armored="$(mktemp)"
  curl -fsSL --connect-timeout 15 --max-time 60 \
    https://deb.nodesource.com/gpgkey/nodesource-repo.gpg.key -o "$ns_armored" \
    || _die "curl failed to fetch NodeSource GPG key from deb.nodesource.com — check egress / DNS from this host"
  local ns_gpg_out
  if ! ns_gpg_out="$(GNUPGHOME="$(mktemp -d)" gpg --no-default-keyring --no-tty --batch --dearmor --yes -o /etc/apt/keyrings/nodesource.gpg "$ns_armored" 2>&1)"; then
    _err "gpg --dearmor failed on NodeSource key; output was:"
    printf '%s\n' "$ns_gpg_out" >&2
    _die "cannot dearmor NodeSource key"
  fi
  rm -f "$ns_armored"
  chmod 0644 /etc/apt/keyrings/nodesource.gpg
  echo 'deb [signed-by=/etc/apt/keyrings/nodesource.gpg] https://deb.nodesource.com/node_22.x nodistro main' \
    >/etc/apt/sources.list.d/nodesource.list
  _ok "NodeSource repo configured"
}

install_node() {
  # nodejs is installed in install_base_packages's one-shot apt batch
  # (NodeSource repo added by _install_nodesource_source before the
  # install). This function is now just a version-check + log.
  if ! command -v node >/dev/null 2>&1; then
    _die "node binary not found — install_base_packages should have installed it"
  fi
  local cur_major
  cur_major="$(node -v | sed -E 's/^v([0-9]+).*/\1/')"
  if [[ "$cur_major" -lt 22 ]]; then
    _warn "Node $cur_major is older than expected v22 — NodeSource repo may not have taken effect"
  fi
  _ok "Node $(node -v) / npm $(npm -v) present"
}

# ---------- step 2.5: MariaDB DB + scoped user ------------------------------

provision_mariadb() {
  _log "provisioning MariaDB database + user"

  systemctl enable --quiet --now mariadb
  # Wait briefly for the socket to appear on a freshly-installed box.
  for i in 1 2 3 4 5; do
    if mariadb -e 'SELECT 1' >/dev/null 2>&1; then break; fi
    sleep 1
  done
  if ! mariadb -e 'SELECT 1' >/dev/null 2>&1; then
    _die "MariaDB unreachable via unix_socket auth as root"
  fi

  local db_name="jabali_panel"
  local db_user="jabali_panel_app"
  local pw_file="/etc/jabali/db-password"

  if [[ -f "$pw_file" ]]; then
    _ok "DB password already generated at $pw_file"
  else
    _log "generating DB password → $pw_file"
    install -d -m 0750 -o root -g "$SERVICE_USER" "$(dirname "$pw_file")"
    umask 077
    openssl rand -hex 32 >"$pw_file"
    chmod 0640 "$pw_file"
    chown root:"$SERVICE_USER" "$pw_file"
  fi
  local db_pass
  db_pass="$(cat "$pw_file")"

  # Create DB and user. Privileges are scoped to the panel's own DB — the
  # panel user has no rights over customer-hosted databases that will live
  # on the same MariaDB instance.
  mariadb -e "
    CREATE DATABASE IF NOT EXISTS \`${db_name}\`
      CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
    CREATE USER IF NOT EXISTS '${db_user}'@'localhost' IDENTIFIED BY '${db_pass}';
    ALTER USER '${db_user}'@'localhost' IDENTIFIED BY '${db_pass}';
    GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, DROP, INDEX, ALTER,
          REFERENCES, LOCK TABLES, TRIGGER
      ON \`${db_name}\`.* TO '${db_user}'@'localhost';
    FLUSH PRIVILEGES;
  "

  # Expose the DSN via /etc/jabali/panel.env so the service picks it up.
  # M25 Step 6: switch from TCP loopback (127.0.0.1:3306) to MariaDB's
  # Debian-default Unix socket /var/run/mysqld/mysqld.sock. The
  # @unix(...) form is the native go-sql-driver/mysql syntax — no
  # mysql:// URL prefix needed (and prefix would break: net/url.Parse
  # rejects parens in host). Existing dsn.go ToDriverDSN passes native
  # form through unchanged.
  #
  # `skip-networking` now drops in via install_mariadb_skip_networking()
  # below — every MariaDB consumer on this host (panel-api, Kratos,
  # pdns, phpMyAdmin SSO) has been flipped to /var/run/mysqld/mysqld.sock.
  # The my.cnf knob closes TCP :3306 entirely; the unix socket remains
  # the sole ingress. Rollback: remove the drop-in file and restart.
  local dsn="${db_user}:${db_pass}@unix(/var/run/mysqld/mysqld.sock)/${db_name}?parseTime=true&charset=utf8mb4&loc=UTC"

  # Rewrite the line without sed (DSNs contain `&` which sed would expand
  # as the matched text). We strip any existing DATABASE_URL line and
  # append a fresh one.
  local tmp
  tmp="$(mktemp --tmpdir jabali-env.XXXXXX)"
  grep -v '^DATABASE_URL=' "$ENV_FILE" >"$tmp" || true
  echo "DATABASE_URL=${dsn}" >>"$tmp"
  install -m 0640 -o root -g "$SERVICE_USER" "$tmp" "$ENV_FILE"
  rm -f "$tmp"

  _ok "MariaDB provisioned: DB=${db_name}, user=${db_user}"
}

# ---------- step 2.5b: MariaDB loopback-only (M25.1, amended) ---------------
#
# Drops a tiny drop-in conf that pins mariadbd to 127.0.0.1 only.
#
# Original M25.1 used `skip-networking` to close TCP :3306 entirely,
# but Stalwart's SqlDirectory backend is TCP-only — there's no
# unix-socket option in the upstream MySql store struct, so disabling
# TCP broke webmail JMAP auth (502 "Failed to verify JMAP session").
# The current setting binds to loopback only: external 3306 stays
# closed (UFW + this binding), all panel-managed consumers (panel-api,
# Kratos, pdns, phpMyAdmin SSO) keep using /var/run/mysqld/mysqld.sock,
# and Stalwart can dial 127.0.0.1:3306.
#
# Why a drop-in under mariadb.conf.d/ rather than editing 50-server.cnf:
# 50-server.cnf is package-owned. Every apt upgrade that ships a new
# 50-server.cnf would either clobber our edit or prompt dpkg for
# manual resolution. Drop-ins survive upgrades unchanged.
install_mariadb_skip_networking() {
  local dropin="/etc/mysql/mariadb.conf.d/99-jabali-skip-networking.cnf"
  local desired=$'# Managed by jabali install.sh — M25.1 (amended).\n# Stalwart\'s SqlDirectory backend is TCP-only (no unix-socket support\n# in the upstream MySql store), so we can\'t fully skip-networking.\n# Bind to loopback only — UFW + jabali.slice still keep external 3306\n# closed, and every other consumer on this host (panel-api, pdns,\n# phpMyAdmin SSO) reaches MariaDB via /var/run/mysqld/mysqld.sock.\n[mysqld]\nbind-address=127.0.0.1\n'

  if [[ -f "$dropin" ]] && cmp -s <(printf '%s' "$desired") "$dropin"; then
    _log "MariaDB loopback-only drop-in already current"
    return
  fi

  _log "installing MariaDB loopback-only drop-in → $dropin"
  local tmp
  tmp="$(mktemp --tmpdir jabali-mariadb-dropin.XXXXXX)"
  printf '%s' "$desired" >"$tmp"
  install -m 0644 -o root -g root "$tmp" "$dropin"
  rm -f "$tmp"

  systemctl restart mariadb

  # Wait for socket to come back up before returning — downstream
  # steps (kratos migrations, pdns start) will fail if we race the
  # restart.
  local i
  for i in 1 2 3 4 5 6 7 8 9 10; do
    if mariadb -e 'SELECT 1' >/dev/null 2>&1; then break; fi
    sleep 1
  done
  if ! mariadb -e 'SELECT 1' >/dev/null 2>&1; then
    _die "MariaDB did not come back up after loopback-only drop-in; rollback: trash $dropin && systemctl restart mariadb"
  fi

  # Defensive: verify :3306 is loopback-only. A 0.0.0.0:3306 listener
  # means the drop-in didn't take (wrong path, syntax error) and the
  # port is exposed externally — fail loud.
  if ss -tlnH 'sport = :3306' | awk '{print $4}' | grep -vqE '^127\.0\.0\.1:|^\[::1\]:'; then
    _die "MariaDB :3306 is not loopback-only after drop-in — bind-address did not take effect"
  fi
  _ok "MariaDB :3306 bound to 127.0.0.1 only (loopback-only mode)"

  # Belt-and-braces ACL — grant jabali rwx on the socket dir + file
  # regardless of group membership state.
  ensure_mariadb_socket_acl_for_jabali
}

# ensure_jabali_sendmail_binary — JAB-230 two-hop-update closer. The shim
# binary is normally installed by build_backend / update.go, but the FIRST
# `jabali update` onto a JAB-230 build runs the PREVIOUS binary's update
# code, which knows nothing about the shim — the new pool template lands
# (sendmail_path set) while /usr/local/libexec/jabali/jabali-sendmail is
# still absent, and mail() stays broken until some later release touches
# panel-agent again. Seen live on the .165 stable box. This runs from the
# NEW repo's install.sh self-heal block on every update, so the gap closes
# in the same run. SHA-marker-gated: a no-op when the installed shim was
# built from the current checkout.
ensure_jabali_sendmail_binary() {
  local dst=/usr/local/libexec/jabali/jabali-sendmail
  local marker=/usr/local/libexec/jabali/.jabali-sendmail.sha
  local src_pkg="$REPO_DIR/panel-agent/cmd/jabali-sendmail"
  [[ -d "$src_pkg" ]] || return 0   # pre-JAB-230 checkout — nothing to build
  local want
  want="$(sudo -u "$SERVICE_USER" git -C "$REPO_DIR" rev-parse HEAD 2>/dev/null || echo unknown)"
  if [[ -x "$dst" && -f "$marker" && "$(cat "$marker" 2>/dev/null)" == "$want" ]]; then
    return 0
  fi
  local gobin="${GO_ROOT:-/usr/local/go}/bin"
  [[ -x "$gobin/go" ]] || gobin="$(dirname "$(command -v go 2>/dev/null || echo /usr/local/go/bin/go)")"
  if sudo -u "$SERVICE_USER" -H env \
      PATH="$gobin:/usr/bin:/bin" HOME="$REPO_DIR" \
      GOCACHE="$REPO_DIR/.cache/go-build" GOMODCACHE="$REPO_DIR/.cache/go-mod" \
      bash -c "cd '$REPO_DIR' && go build -trimpath -ldflags '-s -w' -o bin/jabali-sendmail.new ./panel-agent/cmd/jabali-sendmail"; then
    install -d -m 0755 /usr/local/libexec/jabali
    install -m 0755 -o root -g root "$REPO_DIR/bin/jabali-sendmail.new" "$dst"
    rm -f "$REPO_DIR/bin/jabali-sendmail.new"
    printf '%s\n' "$want" > "$marker"
    _ok "jabali-sendmail shim installed at $dst (JAB-230)"
  else
    _warn "jabali-sendmail build failed — PHP mail() stays broken until the next update (non-fatal)"
  fi
}

# install_php_cli_sendmail_path — JAB-230: point CLI PHP's mail() at the
# jabali-sendmail shim. The FPM pools get sendmail_path from the pool template;
# CLI php (cron jobs, wp-cli, artisan) reads its own per-version cli/conf.d, so
# without this dropin every cron-driven mail() still dies on the purged
# /usr/sbin/sendmail. Idempotent; loops every installed minor. Callers:
# fresh install, every `jabali update` (self-heal), and php.version.install
# (agent) for minors added after install day.
install_php_cli_sendmail_path() {
  local dir minor ini
  for dir in /etc/php/*/; do
    [[ -d "${dir}cli" ]] || continue
    minor="$(basename "$dir")"
    ini="/etc/php/${minor}/cli/conf.d/99-jabali-sendmail.ini"
    install -d -m 0755 "/etc/php/${minor}/cli/conf.d"
    if [[ ! -f "$ini" ]] || ! grep -q "jabali-sendmail" "$ini" 2>/dev/null; then
      cat > "$ini" <<'SENDMAIL_EOF'
; Managed by jabali-panel (JAB-230). CLI mail() submits via the jabali shim
; (per-user relay credentials; Stalwart on 127.0.0.1:587).
sendmail_path = /usr/local/libexec/jabali/jabali-sendmail -t -i
SENDMAIL_EOF
      chmod 0644 "$ini"
      _log "php-cli sendmail_path: wrote $ini"
    fi
  done
  _ok "CLI PHP mail() routed through jabali-sendmail (JAB-230)"
}

# install_php_opcache_tuning — multi-tenant WordPress opcache defaults (#597).
# The stock 10-opcache.ini (10000 files / 128 MB) can't hold WP + a big plugin
# set (Elementor/JetEngine/Essential-Addons/…), so files get evicted and
# recompiled per request → slow render on every PHP page and every page-cache
# miss/background-refresh. We drop a jabali-owned override into each installed
# PHP minor's FPM conf.d, sized to host RAM, reconciled on every `jabali update`.
#
# Idempotent: write-on-diff; reloads the per-user FPM masters only on change.
# validate_timestamps stays ON (tenant edits must be seen) with revalidate_freq
# raised to 60s. JIT is intentionally left OFF (plugin-breakage risk per #597).
install_php_opcache_tuning() {
  local mem_kb mem_mb opc_mem
  mem_kb=$(awk '/^MemTotal:/ {print $2}' /proc/meminfo 2>/dev/null || echo 0)
  mem_mb=$((mem_kb / 1024))
  # memory_consumption = RAM/32, clamped [256, 512] MB (per #597).
  opc_mem=$((mem_mb / 32))
  if   [[ $opc_mem -lt 256 ]]; then opc_mem=256
  elif [[ $opc_mem -gt 512 ]]; then opc_mem=512
  fi

  local desired changed=0 minor
  desired=$(cat <<OPC_EOF
; Managed by jabali (#597) — multi-tenant WordPress opcache tuning.
; Do NOT hand-edit; install.sh reconciles this on every \`jabali update\`.
; Sized to ${mem_mb} MB host RAM.
opcache.enable=1
opcache.max_accelerated_files=50000
opcache.memory_consumption=${opc_mem}
opcache.interned_strings_buffer=16
opcache.validate_timestamps=1
opcache.revalidate_freq=60
OPC_EOF
)

  for dir in /etc/php/*/; do
    [[ -d "${dir}fpm" ]] || continue
    minor="$(basename "$dir")"
    local ini="/etc/php/${minor}/mods-available/jabali-opcache.ini"
    local link="/etc/php/${minor}/fpm/conf.d/15-jabali-opcache.ini"
    # Some minors have fpm/ but not yet mods-available/ (partial Sury layout);
    # create both targets so the write + symlink never trip the ERR trap.
    install -d -m 0755 "/etc/php/${minor}/mods-available" "/etc/php/${minor}/fpm/conf.d"
    if [[ ! -f "$ini" ]] || ! cmp -s <(printf '%s\n' "$desired") "$ini"; then
      printf '%s\n' "$desired" > "$ini"
      chmod 0644 "$ini"   # 0644: unprivileged per-user FPM master parses conf.d
      changed=1
      _log "opcache-tune: wrote $ini (files=50000 mem=${opc_mem}M)"
    fi
    ln -sf "../../mods-available/jabali-opcache.ini" "$link"
  done

  if [[ $changed -eq 1 ]]; then
    # Reload every running per-user FPM master so the new opcache config takes
    # effect (the global phpX-fpm.service is masked; ADR-0025).
    systemctl reload 'jabali-fpm@*.service' 2>/dev/null || true
    _ok "opcache-tune: applied (max_accelerated_files=50000, memory=${opc_mem}M, interned=16M)"
  else
    _log "opcache-tune: ini already current, no reload"
  fi
}

# tune_mariadb_for_ram writes an innodb_buffer_pool_size drop-in
# sized to host total RAM, plus an OOMScoreAdjust drop-in so mariadbd
# stops being the kernel OOM-killer's default victim on small VMs.
#
# Why: MariaDB 11.x defaults assume a dedicated DB host. On a 3.8 GB
# VPS sharing RAM with panel-api, agent, Stalwart, Bulwark, Kratos,
# PDNS, CrowdSec, and tenant PHP-FPM pools, the 512 MB default pool
# walks the box into kernel memory pressure. Kernel picks the biggest
# RSS (mariadbd) and kills it. Caught 2026-06-05 on puzzle:
# NRestarts=2 in one morning.
#
# Sizing (#597): small VMs stay conservative (OOM history above); real hosts
# get ~35 % of RAM with a 1 GB floor + buffer_pool_instances, leaving headroom
# for the per-user PHP-FPM pools + Stalwart/Kratos/etc on a shared DB+web box.
#   <=2 GB  -> 128 MB pool  (tiny; OOM-critical)
#   <=4 GB  -> 256 MB pool  (puzzle class; keep conservative)
#   >4 GB   -> 35 % of RAM, floor 1024 MB   (e.g. 12 GB -> ~4.2 GB)
#
# OOMScoreAdjust=-500 demotes mariadbd in the OOM order without
# making it un-killable (-1000 would).
#
# Idempotent: only writes when desired != on-disk; only restarts
# when something actually changed.
# mariadb_pool_size_mb <host-ram-mb> — prints the innodb_buffer_pool_size in MB.
#
# Split from tune_mariadb_for_ram so the brackets can be tested by running them
# at every interesting host size. The test used to grep install.sh for bracket
# literals, which silently stopped covering anything when #597 replaced the
# bracket ladder with a percentage.
mariadb_pool_size_mb() {
  local mem_mb="$1" pool_mb

  if   [[ $mem_mb -le 2048 ]]; then pool_mb=128
  elif [[ $mem_mb -le 4096 ]]; then pool_mb=256
  else
    pool_mb=$((mem_mb * 35 / 100))
    [[ $pool_mb -lt 1024 ]] && pool_mb=1024   # #597 floor
  fi

  printf '%s\n' "$pool_mb"
}

tune_mariadb_for_ram() {
  local tuning_dropin="/etc/mysql/mariadb.conf.d/99-jabali-mariadb-tuning.cnf"
  local oom_dropin="/etc/systemd/system/mariadb.service.d/15-jabali-oom.conf"
  local mem_kb mem_mb pool_mb
  mem_kb=$(awk '/^MemTotal:/ {print $2}' /proc/meminfo 2>/dev/null || echo 0)
  mem_mb=$((mem_kb / 1024))

  pool_mb=$(mariadb_pool_size_mb "$mem_mb")
  # NOTE: innodb_buffer_pool_instances was REMOVED in MariaDB 11.x (single
  # resizable buffer pool) — do NOT set it, MariaDB rejects the unknown var.
  _log "mariadb-tune: host=${mem_mb}MB RAM -> innodb_buffer_pool_size=${pool_mb}M"

  local tuning_desired
  tuning_desired=$(cat <<TUNING_EOF
# Managed by jabali install.sh -- sized to ${mem_mb} MB host RAM.
# Do NOT hand-edit; install.sh rewrites on every run.
# See install.sh:tune_mariadb_for_ram for the sizing brackets.
[mysqld]
innodb_buffer_pool_size = ${pool_mb}M
innodb_buffer_pool_size_auto_min = ${pool_mb}M
TUNING_EOF
)

  local oom_desired
  oom_desired=$(cat <<'OOM_EOF'
# Managed by jabali install.sh -- keep mariadbd from being
# the kernel OOM-killer's default victim on small VMs.
# -500 demotes it without making it un-killable (-1000 would).
[Service]
OOMScoreAdjust=-500
OOM_EOF
)

  local restart_needed=0
  if [[ ! -f "$tuning_dropin" ]] || ! cmp -s <(printf '%s\n' "$tuning_desired") "$tuning_dropin"; then
    local tmp
    tmp="$(mktemp --tmpdir jabali-mariadb-tuning.XXXXXX)"
    printf '%s\n' "$tuning_desired" >"$tmp"
    install -m 0644 -o root -g root "$tmp" "$tuning_dropin"
    rm -f "$tmp"
    restart_needed=1
    _log "mariadb-tune: wrote $tuning_dropin"
  fi

  mkdir -p /etc/systemd/system/mariadb.service.d
  if [[ ! -f "$oom_dropin" ]] || ! cmp -s <(printf '%s\n' "$oom_desired") "$oom_dropin"; then
    local tmp
    tmp="$(mktemp --tmpdir jabali-mariadb-oom.XXXXXX)"
    printf '%s\n' "$oom_desired" >"$tmp"
    install -m 0644 -o root -g root "$tmp" "$oom_dropin"
    rm -f "$tmp"
    systemctl daemon-reload
    restart_needed=1
    _log "mariadb-tune: wrote $oom_dropin"
  fi

  if [[ $restart_needed -eq 1 ]]; then
    systemctl restart mariadb
    local i
    for i in 1 2 3 4 5 6 7 8 9 10; do
      if mariadb -e 'SELECT 1' >/dev/null 2>&1; then break; fi
      sleep 1
    done
    if ! mariadb -e 'SELECT 1' >/dev/null 2>&1; then
      _die "MariaDB did not come back up after tuning drop-in; rollback: trash $tuning_dropin $oom_dropin && systemctl daemon-reload && systemctl restart mariadb"
    fi
    _ok "mariadb-tune: applied innodb_buffer_pool_size=${pool_mb}M + OOMScoreAdjust=-500"
  else
    _log "mariadb-tune: drop-ins already current, no restart"
  fi
}

# bound_stalwart_memory caps how much memory Stalwart can take before the
# kernel starts killing whatever else is on the box (JAB-216).
#
# Why: serial cPanel restores on a 7.7 GB host drove it into repeated global
# OOM kills over four hours and finally into a full userspace wedge — sshd
# stopped answering, nginx stopped serving, and the box needed a provider-
# console reboot. The kernel's last kill was stalwart at ~888 MB anon-rss
# while it ingested migrated mail. Stalwart runs unbounded (MemoryHigh and
# MemoryMax both infinity), so a heavy ingest has nothing between it and the
# host's last free page.
#
# The two limits do different jobs and BOTH are needed:
#   MemoryHigh — soft. The kernel throttles allocation and reclaims harder
#     above it. Nothing is killed; a busy server gets slower. This is the one
#     that would have applied back-pressure long before the box died.
#   MemoryMax — hard. Exceeding it OOM-kills inside Stalwart's OWN cgroup, so
#     a genuine runaway costs mail delivery (and systemd restarts it) instead
#     of costing the operator console access. Containing the blast radius is
#     the entire point; a mail outage is recoverable over SSH, a wedged host
#     is not.
#
# Sizing, mirroring tune_mariadb_for_ram: a fraction of host RAM so small VMs
# stay conservative, with a floor so a tiny box still gets a workable mail
# server, and a CEILING because past a few GB any further growth is runaway
# rather than legitimate working set. Stalwart idles near 100 MB.
#   MemoryHigh = 25 % of RAM, floor 512 MB,  ceiling 4096 MB
#   MemoryMax  = 40 % of RAM, floor 768 MB,  ceiling 6144 MB
# On the 7.7 GB box from the incident that is High=1927M / Max=3084M — the
# fatal kill happened at 888 MB anon-rss, so reclaim pressure would have
# started well before the host ran out.
#
# OOMScoreAdjust is deliberately NOT set here. With MemoryMax in place a
# runaway is contained in-cgroup, and the global OOM order already prefers
# tenant slices (oom_score_adj 100). sshd needs nothing either: OpenSSH sets
# its listener to -1000 itself, verified on a live host, and a systemd
# OOMScoreAdjust on ssh.service would apply to the whole cgroup — making
# tenant SSH sessions un-killable, which is worse than the problem.
#
# Idempotent: only writes when desired != on-disk. Uses `set-property
# --runtime` afterwards so the new bounds apply WITHOUT restarting a running
# mail server; the drop-in is what makes them survive a reboot.
# stalwart_mem_bounds_mb <host-ram-mb> — prints "<high_mb> <max_mb>".
#
# Split from bound_stalwart_memory so the brackets can be tested directly at
# every interesting host size instead of only grepped for. Pure arithmetic:
# no filesystem, no systemctl.
stalwart_mem_bounds_mb() {
  local mem_mb="$1" high_mb max_mb

  high_mb=$((mem_mb * 25 / 100))
  [[ $high_mb -lt 512  ]] && high_mb=512
  [[ $high_mb -gt 4096 ]] && high_mb=4096
  max_mb=$((mem_mb * 40 / 100))
  [[ $max_mb -lt 768  ]] && max_mb=768
  [[ $max_mb -gt 6144 ]] && max_mb=6144

  # Guard, not a live branch: with the brackets above the soft limit is always
  # under the hard one. It exists because that is a property of the CONSTANTS,
  # not of the shape — raising the MemoryHigh floor past 768 would invert them,
  # and systemd would then kill at Max having never throttled at High, silently
  # removing the only mechanism that degrades gracefully. Cheaper to keep than
  # to rediscover.
  if [[ $high_mb -ge $max_mb ]]; then
    high_mb=$((max_mb * 80 / 100))
  fi

  printf '%s %s\n' "$high_mb" "$max_mb"
}

bound_stalwart_memory() {
  local dropin_dir="/etc/systemd/system/jabali-stalwart.service.d"
  local dropin="${dropin_dir}/20-jabali-memory.conf"
  local mem_kb mem_mb high_mb max_mb

  # Nothing to bound on a host without the mail unit (modular install with
  # mail disabled). Not an error.
  if ! systemctl list-unit-files jabali-stalwart.service >/dev/null 2>&1; then
    return 0
  fi
  if [[ ! -f /etc/systemd/system/jabali-stalwart.service ]]; then
    return 0
  fi

  mem_kb=$(awk '/^MemTotal:/ {print $2}' /proc/meminfo 2>/dev/null || echo 0)
  mem_mb=$((mem_kb / 1024))
  if [[ $mem_mb -le 0 ]]; then
    _warn "stalwart-mem: cannot read MemTotal; leaving Stalwart unbounded"
    return 0
  fi

  read -r high_mb max_mb < <(stalwart_mem_bounds_mb "$mem_mb")

  local desired
  desired=$(cat <<STALWART_MEM_EOF
# Managed by jabali install.sh -- sized to ${mem_mb} MB host RAM.
# Do NOT hand-edit; install.sh rewrites on every run.
# See install.sh:bound_stalwart_memory for the sizing brackets (JAB-216).
[Service]
MemoryAccounting=yes
MemoryHigh=${high_mb}M
MemoryMax=${max_mb}M
STALWART_MEM_EOF
)

  if [[ -f "$dropin" ]] && cmp -s <(printf '%s\n' "$desired") "$dropin"; then
    _log "stalwart-mem: drop-in already current (High=${high_mb}M Max=${max_mb}M)"
    return 0
  fi

  mkdir -p "$dropin_dir"
  local tmp
  tmp="$(mktemp --tmpdir jabali-stalwart-mem.XXXXXX)"
  printf '%s\n' "$desired" >"$tmp"
  install -m 0644 -o root -g root "$tmp" "$dropin"
  rm -f "$tmp"
  systemctl daemon-reload
  _log "stalwart-mem: wrote $dropin"

  # Apply to the RUNNING unit without a restart. Bouncing Stalwart drops live
  # IMAP/JMAP sessions and interrupts mail delivery, which is too much
  # collateral for a limit change — and mid-migration, restarting the very
  # service that is ingesting mail is exactly the wrong move.
  if systemctl is-active --quiet jabali-stalwart.service; then
    if systemctl set-property --runtime jabali-stalwart.service \
         "MemoryHigh=${high_mb}M" "MemoryMax=${max_mb}M" >/dev/null 2>&1; then
      _ok "stalwart-mem: MemoryHigh=${high_mb}M MemoryMax=${max_mb}M (host ${mem_mb}MB, applied live)"
    else
      _warn "stalwart-mem: drop-in written but live apply failed; bounds take effect on next restart"
    fi
  else
    _ok "stalwart-mem: MemoryHigh=${high_mb}M MemoryMax=${max_mb}M (host ${mem_mb}MB, applies on next start)"
  fi
}



# ensure_mariadb_socket_acl_for_jabali — POSIX ACL fallback so the
# jabali user can connect to /run/mysqld/mysqld.sock even when the
# usermod -aG mysql route hasn't taken effect on already-running
# processes. systemd's SupplementaryGroups=mysql in
# jabali-kratos.service is the primary path; this ACL is fallback.
ensure_mariadb_socket_acl_for_jabali() {
  command -v setfacl >/dev/null 2>&1 || { _warn "acl tools missing — apt install acl"; return 0; }
  [[ -d /run/mysqld ]] && setfacl -m u:"$SERVICE_USER":rx /run/mysqld 2>/dev/null || true
  [[ -S /run/mysqld/mysqld.sock ]] && setfacl -m u:"$SERVICE_USER":rw /run/mysqld/mysqld.sock 2>/dev/null || true

  # tmpfiles.d so the ACL reapplies on boot (tmpfs wipes ACLs).
  local tmpfiles=/etc/tmpfiles.d/jabali-mariadb-socket-acl.conf
  local desired=$'# Managed by jabali install.sh — M25.1 socket ACL fallback.\nA+ /run/mysqld         - - - - u:'"$SERVICE_USER"':rx\n'
  if [[ ! -f "$tmpfiles" ]] || ! cmp -s <(printf '%s' "$desired") "$tmpfiles"; then
    printf '%s' "$desired" > "$tmpfiles"
    systemd-tmpfiles --create "$tmpfiles" 2>/dev/null || true
  fi
}

# ---------- step 2.5: Redis (notification dispatcher + future WP cache) ------
#
# ADR-0056 + ADR-0059. Unix-socket-only Redis at /run/redis/redis.sock,
# mode 0660, group jabali-sockets (same pattern as every other service
# under ADR-0050). AOF on (dispatcher queue survives restart).
# 128 MB maxmemory with allkeys-lru (safe for both dispatcher queue
# and future WP object-cache).
#
# db 0 → panel-api notification dispatcher
# db 1 → reserved for future WordPress object-cache
#
# install_docker_engine sets up Docker CE + docker-compose-plugin (M48
# Phase 1, ADR-0116). Installs idempotent; skips when `docker` is
# already present. Writes /etc/docker/daemon.json drop-in with
# defensive defaults (journald log driver, live-restore so daemon
# restarts dont kill running containers, sane default ulimits).
# Creates the per-app data root at /var/lib/jabali/docker-apps and
# adds the jabali user to the `docker` group so an operator console
# can issue `docker` CLI without sudo.
#
# Why a separate function (not folded into install_base_packages):
# Docker apt repo lives at https://download.docker.com/linux/debian
# which we add lazily here so a no-M48 install never pulls 200MB of
# docker-ce dependencies it will never run.
install_docker_engine() {
  _log "installing Docker CE + compose plugin (M48 marketplace)"

  if command -v docker >/dev/null 2>&1 \
    && docker info >/dev/null 2>&1 \
    && [[ -d /etc/docker ]] \
    && dpkg -s docker-compose-plugin >/dev/null 2>&1; then
    _log "docker engine + compose plugin already installed; skipping apt"
  else
    # Repo + signing key. Docker hosts separate apt repos for Debian
    # and Ubuntu; ID from /etc/os-release picks the right tree, and
    # VERSION_CODENAME drives the suite (trixie/bookworm/noble/jammy/
    # ...). Falls back to debian/trixie when /etc/os-release is empty.
    local os_id os_codename docker_distro
    os_id="$(. /etc/os-release; echo "${ID:-debian}")"
    os_codename="$(. /etc/os-release; echo "${VERSION_CODENAME:-trixie}")"
    case "$os_id" in
      ubuntu) docker_distro="ubuntu" ;;
      debian|*) docker_distro="debian" ;;
    esac

    install -d -m 0755 /etc/apt/keyrings
    if [[ ! -f /etc/apt/keyrings/docker.asc ]]; then
      curl -fsSL "https://download.docker.com/linux/${docker_distro}/gpg" -o /etc/apt/keyrings/docker.asc
      chmod a+r /etc/apt/keyrings/docker.asc
    fi
    local docker_list="/etc/apt/sources.list.d/docker.list"
    local desired
    desired="deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/${docker_distro} ${os_codename} stable"
    if [[ ! -f "$docker_list" ]] || ! grep -qF "$desired" "$docker_list"; then
      printf '%s\n' "$desired" > "$docker_list"
      _log "wrote $docker_list (distro=${docker_distro}, codename=${os_codename})"
    fi
    apt-get update -y -o Dir::Etc::sourcelist="$docker_list" -o Dir::Etc::sourceparts="-" -o APT::Get::List-Cleanup="0"
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  fi

  # /etc/docker/daemon.json drop-in. Use byte-comparison rewrite so
  # we dont restart docker on every install.sh run.
  install -d -m 0755 /etc/docker
  local dropin="/etc/docker/daemon.json"

  # SECURITY (#506): preserve userns-remap. `jabali docker enable-tenant` adds
  # "userns-remap":"default" so tenant containers map container-root to the
  # unprivileged dockremap range. A plain whole-file rewrite here would drop it
  # on the next install/update while /etc/jabali/docker-tenant-enabled stays —
  # silently running tenant containers as host root. Re-add it whenever the
  # tenant flag exists OR the current daemon.json already has it.
  local want_userns="no"
  if [[ -f /etc/jabali/docker-tenant-enabled ]] || { [[ -f "$dropin" ]] && grep -q '"userns-remap"' "$dropin"; }; then
    want_userns="yes"
  fi
  local desired_json
  if [[ "$want_userns" == "yes" ]]; then
    desired_json=$(cat <<'DOCKER_EOF'
{
  "userns-remap": "default",
  "live-restore": true,
  "log-driver": "journald",
  "default-ulimits": {
    "nofile": { "Name": "nofile", "Soft": 8192, "Hard": 8192 }
  }
}
DOCKER_EOF
)
  else
    desired_json=$(cat <<'DOCKER_EOF'
{
  "live-restore": true,
  "log-driver": "journald",
  "default-ulimits": {
    "nofile": { "Name": "nofile", "Soft": 8192, "Hard": 8192 }
  }
}
DOCKER_EOF
)
  fi
  if [[ ! -f "$dropin" ]] || ! cmp -s <(printf '%s\n' "$desired_json") "$dropin"; then
    local tmp
    tmp="$(mktemp --tmpdir jabali-docker-daemon.XXXXXX)"
    printf '%s\n' "$desired_json" > "$tmp"
    install -m 0644 -o root -g root "$tmp" "$dropin"
    rm -f "$tmp"
    _log "wrote $dropin (userns-remap=$want_userns); restarting docker"
    systemctl restart docker
  else
    _log "$dropin already current"
  fi

  # SECURITY (#506): fail closed. If tenant Docker is enabled the live daemon
  # MUST be running with userns-remap — never leave tenant containers mapped to
  # host root. We re-added the key above, so a restart should restore it; this
  # verifies the live daemon (not just the config) and screams if it didn't.
  if [[ -f /etc/jabali/docker-tenant-enabled ]]; then
    if ! docker info --format '{{json .SecurityOptions}}' 2>/dev/null | grep -q userns; then
      _err "tenant Docker is enabled (/etc/jabali/docker-tenant-enabled) but the Docker daemon is NOT running with userns-remap. Refusing to leave tenant containers mapped to host root — re-run 'jabali docker enable-tenant', or remove the flag if tenant Docker is no longer wanted."
      return 1
    fi
    _log "verified live userns-remap active (tenant Docker enabled)"
  fi

  # SECURITY (#507): block Docker container egress to link-local / cloud
  # metadata (169.254.0.0/16, fe80::/10). Container traffic is FORWARDed, so the
  # M34 per-user egress firewall (OUTPUT, socket cgroupv2) never sees it — a
  # tenant app could otherwise SSRF the cloud metadata endpoint. We drop ONLY
  # link-local: the compose internal networks are RFC1918 (172.x bridge), so
  # blocking private ranges here would break app<->db. DOCKER-USER is recreated
  # empty on every docker daemon start, so the rules are (re)applied via a
  # docker.service ExecStartPost as well as here.
  cat > /usr/local/bin/jabali-docker-egress-rules <<'EGRESS_EOF'
#!/bin/sh
# Auto-generated by jabali (Gitea #507). Drop Docker container egress to
# link-local / cloud-metadata ranges. Idempotent (-C check before -I).
iptables  -C DOCKER-USER -d 169.254.0.0/16 -j DROP 2>/dev/null || iptables  -I DOCKER-USER -d 169.254.0.0/16 -j DROP
ip6tables -C DOCKER-USER -d fe80::/10       -j DROP 2>/dev/null || ip6tables -I DOCKER-USER -d fe80::/10       -j DROP 2>/dev/null || true
exit 0
EGRESS_EOF
  chmod 0755 /usr/local/bin/jabali-docker-egress-rules
  install -d -m 0755 /etc/systemd/system/docker.service.d
  cat > /etc/systemd/system/docker.service.d/jabali-egress.conf <<'DROPIN_EOF'
[Service]
# Re-apply jabali container-egress drops after docker recreates DOCKER-USER
# (Gitea #507). Leading "-" so a failure never blocks docker startup.
ExecStartPost=-/usr/local/bin/jabali-docker-egress-rules
DROPIN_EOF
  systemctl daemon-reload 2>/dev/null || true
  /usr/local/bin/jabali-docker-egress-rules || _warn "could not apply docker egress drops now (will retry on docker restart)"
  _ok "docker container egress drops applied (link-local/metadata blocked)"

  # SECURITY (#519): isolate tenant Docker app loopback ports. Apps publish on
  # 127.0.0.1:<10000-19999>, which every local process can otherwise reach — so
  # one tenant could curl another tenant's app backend. Drop traffic from any
  # tenant slice (cgroupv2 level 2 = jabali.slice/jabali-user.slice, matches
  # every jabali-user-<u>.slice) to that loopback range. nginx/panel run OUTSIDE
  # the tenant slices, so reverse-proxying still works; tenants reach their app
  # via the domain, never the raw loopback port. Universal (not per-user M34
  # state). nftables.d/*.nft is re-applied at boot via the load unit below.
  install -d -m 0755 /etc/nftables.d
  cat >/etc/nftables.d/jabali-docker-loopback-isolation.nft <<'NFT'
# Generated by jabali (Gitea #519) — do not edit.
table inet jabali_docker_isolation {
  chain output {
    type filter hook output priority filter; policy accept;
    socket cgroupv2 level 2 "jabali.slice/jabali-user.slice" ip daddr 127.0.0.1 tcp dport 10000-19999 drop
    socket cgroupv2 level 2 "jabali.slice/jabali-user.slice" ip6 daddr ::1 tcp dport 10000-19999 drop
  }
}
NFT
  cat >/etc/systemd/system/jabali-docker-loopback-isolation-load.service <<'UNIT'
[Unit]
Description=Apply jabali tenant Docker loopback-port isolation nftables rules
After=network-pre.target
Before=jabali-panel.service
# Only load once the tenant slice exists (nft rejects a missing cgroupv2 path).
ConditionPathExists=/sys/fs/cgroup/jabali.slice/jabali-user.slice

[Service]
Type=oneshot
ExecStart=/usr/sbin/nft -f /etc/nftables.d/jabali-docker-loopback-isolation.nft
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
UNIT
  systemctl daemon-reload 2>/dev/null || true
  systemctl enable jabali-docker-loopback-isolation-load.service >/dev/null 2>&1 || true
  # Best-effort apply now (no-op if the tenant slice doesn't exist yet — the
  # boot unit + next update will apply it once a tenant exists).
  if [[ -d /sys/fs/cgroup/jabali.slice/jabali-user.slice ]]; then
    nft -f /etc/nftables.d/jabali-docker-loopback-isolation.nft 2>/dev/null \
      && _ok "tenant Docker loopback-port isolation applied" \
      || _warn "could not apply docker loopback isolation now (will retry at boot)"
  fi

  # Per-app data root.
  install -d -m 0750 -o root -g "$SERVICE_USER" /var/lib/jabali/docker-apps

  # SECURITY (#487): the panel service user must NOT be in the `docker` group.
  # Docker group membership is root-equivalent, and panel-api (which runs as
  # $SERVICE_USER) never issues docker commands directly -- the privileged
  # agent (root) does all docker work. Strip the legacy membership on existing
  # hosts; the end-of-install panel restart drops it from the running process.
  if id -nG "$SERVICE_USER" 2>/dev/null | tr ' ' '\n' | grep -qx docker; then
    gpasswd -d "$SERVICE_USER" docker >/dev/null 2>&1 || true
    _log "removed $SERVICE_USER from docker group (#487: docker group is root-equivalent)"
  fi

  # Ensure restic is present -- the per-app backup flow (Phase 8) relies
  # on it. install_base_packages already installs restic for M30, but
  # repeat the gate here so a fresh M48-only run does not skip it.
  if ! command -v restic >/dev/null 2>&1; then
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends restic
  fi
  # zstd binary: backup download streams `tar -I zstd` (GH #266). Without it
  # the tar child exits 127 and the download is a 0-byte file. Heal existing
  # hosts on `jabali update` (install_base_packages covers fresh installs).
  if ! command -v zstd >/dev/null 2>&1; then
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends zstd
  fi

  _ok "docker engine ready (live-restore + journald; panel user NOT in docker group)"
}

# Package redis-server is installed in install_base_packages' one-shot
# apt batch; this runs post-install config only.
install_redis() {
  _log "configuring Redis (package installed in base batch; this runs post-install config)"

  if ! command -v redis-cli >/dev/null 2>&1; then
    _die "redis-cli binary not found — install_base_packages should have installed redis-server + redis-tools"
  fi

  # Debian's /etc/redis/redis.conf ends with `include /etc/redis/redis.conf.d/*.conf`
  # from redis 7.x. Verify presence + patch once if the distro variant
  # doesn't ship that include — defensive rather than clever.
  local main_conf="/etc/redis/redis.conf"
  if [[ ! -f "$main_conf" ]]; then
    _die "$main_conf missing — redis-server install did not create the config"
  fi
  if ! grep -qE '^[[:space:]]*include[[:space:]]+/etc/redis/redis\.conf\.d/\*\.conf' "$main_conf"; then
    _log "patching $main_conf to include /etc/redis/redis.conf.d/*.conf"
    printf '\n# Added by jabali install.sh — load drop-ins.\ninclude /etc/redis/redis.conf.d/*.conf\n' >> "$main_conf"
  fi

  install -d -m 0755 -o root -g root /etc/redis/redis.conf.d

  local dropin="/etc/redis/redis.conf.d/10-jabali-socket.conf"
  local desired=$'# Managed by jabali install.sh — M14 / ADR-0059. Do NOT hand-edit.\n# Unix socket only; no TCP listener (port 0 disables TCP). Mode 0660 with\n# group jabali-sockets lets panel-api + future WP-cache clients\n# connect while keeping the socket out of reach of unrelated users.\nport 0\nunixsocket /run/redis/redis.sock\nunixsocketperm 660\n\n# Persistence: AOF on with everysec fsync. Lets the notification\n# dispatcher queue survive systemctl restart redis-server.\nappendonly yes\nappendfsync everysec\n\n# Bounded memory with safe eviction. 128MB is the starting floor; WP\n# cache load may warrant a higher-numbered drop-in later.\nmaxmemory 128mb\nmaxmemory-policy allkeys-lru\n'

  local restart_needed=0
  if [[ ! -f "$dropin" ]] || ! cmp -s <(printf '%s' "$desired") "$dropin"; then
    _log "installing Redis drop-in → $dropin"
    local tmp
    tmp="$(mktemp --tmpdir jabali-redis-dropin.XXXXXX)"
    printf '%s' "$desired" >"$tmp"
    install -m 0644 -o root -g root "$tmp" "$dropin"
    rm -f "$tmp"
    restart_needed=1
  else
    _log "Redis drop-in already current"
  fi

  # systemd drop-in: RuntimeDirectory=redis gives Redis its own
  # /run/redis/ on every boot. Belt-and-suspenders chmod/chgrp match
  # ADR-0050 F-C-3. SupplementaryGroups=jabali-sockets so Redis can
  # set group-write on the socket file when it creates it (primary
  # group stays `redis` for AOF file permissions).
  install -d -m 0755 -o root -g root /etc/systemd/system/redis-server.service.d
  local unit_dropin="/etc/systemd/system/redis-server.service.d/10-jabali-socket.conf"
  # Flip the service's primary Group= from stock `redis` to
  # `jabali-sockets`. This cascades cleanly:
  #   - systemd creates /run/redis as redis:jabali-sockets (matching the
  #     service's User:Group), mode 0750 → redis owner rwx, jabali-
  #     sockets members rx (traverse), others blocked. panel-api (uid
  #     jabali, gid jabali-sockets) can open the dir.
  #   - redis process egid = jabali-sockets → the socket it creates
  #     inherits group = jabali-sockets automatically. `unixsocketperm 660`
  #     in the conf drop-in sets the mode. No ExecStartPost chgrp/chmod
  #     dance needed.
  #   - redis still owns /var/lib/redis and /var/log/redis by UID, so
  #     flipping the primary group doesn't break its data-dir access
  #     (file access on owner-match ignores egid).
  # Earlier iterations tried ExecStartPost chgrp with the `+` prefix but
  # systemd re-asserts RuntimeDirectory ownership after the hook runs,
  # so the dir reverted to redis:redis every restart. Setting Group=
  # at the service level makes the systemd-managed ownership correct
  # on its own.
  local unit_desired=$'# Managed by jabali install.sh — M14 / ADR-0059. Do NOT hand-edit.\n[Service]\nGroup=jabali-sockets\nRuntimeDirectory=redis\nRuntimeDirectoryMode=0750\n'

  if [[ ! -f "$unit_dropin" ]] || ! cmp -s <(printf '%s' "$unit_desired") "$unit_dropin"; then
    _log "installing Redis systemd drop-in → $unit_dropin"
    local tmp
    tmp="$(mktemp --tmpdir jabali-redis-unit.XXXXXX)"
    printf '%s' "$unit_desired" >"$tmp"
    install -m 0644 -o root -g root "$tmp" "$unit_dropin"
    rm -f "$tmp"
    systemctl daemon-reload
    restart_needed=1
  else
    _log "Redis systemd drop-in already current"
  fi

  # Make sure the jabali-sockets group exists (M25 install creates it,
  # but we may run before that wave on fresh installs if ordering
  # changes in the future). Idempotent.
  if ! getent group jabali-sockets >/dev/null; then
    _log "creating jabali-sockets system group (M25 boundary; ADR-0050)"
    groupadd --system jabali-sockets
  fi

  systemctl enable redis-server >/dev/null 2>&1 || true
  if [[ "$restart_needed" == "1" ]]; then
    systemctl restart redis-server
  else
    systemctl start redis-server
  fi

  # Ping via the socket; fail loud if Redis didn't actually come up on
  # the expected path (wrong config, SELinux, etc.). A locked `default` user
  # (install_redis_acl, #406, runs on the NEXT step and on every re-run) makes
  # a bare PING answer NOAUTH/NOPERM instead of PONG — that still proves the
  # server is up and listening on the socket, which is all this check needs, so
  # accept those replies too (otherwise `jabali update` false-fails here).
  local i _reply
  for i in 1 2 3 4 5 6 7 8 9 10; do
    _reply="$(redis-cli -s /run/redis/redis.sock ping 2>&1)"
    if printf '%s' "$_reply" | grep -qE 'PONG|NOAUTH|NOPERM|WRONGPASS'; then
      break
    fi
    sleep 1
  done
  if ! printf '%s' "$_reply" | grep -qE 'PONG|NOAUTH|NOPERM|WRONGPASS'; then
    _die "Redis did not respond on /run/redis/redis.sock (last reply: ${_reply:-none}) — check 'journalctl -u redis-server'"
  fi

  # Verify no TCP listener. Same invariant check as MariaDB's
  # skip-networking step — config ingest errors are easier to catch
  # here than debug at runtime.
  if ss -tlnH 'sport = :6379' | grep -q LISTEN; then
    _die "Redis still LISTENs on :6379 — port 0 directive didn't take effect"
  fi

  # Verify socket permissions match ADR-0059 contract.
  local mode owner group
  read -r mode owner group < <(stat -c '%a %U %G' /run/redis/redis.sock)
  if [[ "$mode" != "660" ]]; then
    _warn "Redis socket mode is $mode (expected 660) — ExecStartPost hook may have raced; fix with 'chmod 0660 /run/redis/redis.sock'"
  fi
  if [[ "$group" != "jabali-sockets" ]]; then
    _die "Redis socket group is $group (expected jabali-sockets) — ExecStartPost chgrp did not run"
  fi

  _ok "Redis listening on unix socket /run/redis/redis.sock mode 0660 ${owner}:${group}"
}

# install_redis_acl — #406 / ADR-0148. Lock the no-AUTH default user and give
# panel-api a scoped ACL user, so reaching the socket no longer grants full
# access (the prerequisite for ever letting tenants connect for WP caching).
#
# Idempotent + token-stable: the panel token is generated once and persisted in
# panel.env; re-runs (jabali update) reuse it and re-assert the locked state.
# Sequenced so the dispatcher never loses access: write the users with `default`
# still ON, reload, restart panel (it now authenticates as jabali_panel), THEN
# lock default.
install_redis_acl() {
  # Fast-path convergence guard (#595): if every piece of ADR-0148 state is
  # already in place, return WITHOUT restarting redis-server / jabali-panel.
  # Lets provision_new_software call install_redis_acl on EVERY `jabali update`
  # so existing (pre-ADR-0148) hosts self-heal — without flushing the WP cache
  # or bouncing the panel on already-provisioned hosts.
  if getent group jabali-redis-clients >/dev/null 2>&1 \
     && [[ -f /etc/systemd/system/redis-server.service.d/20-jabali-redis-clients.conf ]] \
     && grep -q '^JABALI_REDIS_PANEL_TOKEN=' "$ENV_FILE" 2>/dev/null \
     && grep -q '^JABALI_WP_CACHE_HMAC_SECRET=' "$ENV_FILE" 2>/dev/null \
     && [[ -f /etc/redis/users.acl ]] \
     && grep -q '^user jabali_panel ' /etc/redis/users.acl 2>/dev/null \
     && grep -q '^user default off' /etc/redis/users.acl 2>/dev/null \
     && [[ -f /etc/redis/redis.conf.d/20-jabali-acl.conf ]]; then
    return 0
  fi

  _log "configuring Redis multi-tenant ACLs (#406 / ADR-0148)"
  local sock="/run/redis/redis.sock"
  local aclfile="/etc/redis/users.acl"

  # ADR-0148 socket access: a dedicated group fronts ONLY the Redis socket so
  # WP-cache tenants (#406) can reach Redis without joining jabali-sockets
  # (which also fronts the root agent socket). Granted via POSIX ACL in an
  # ExecStartPost, so redis's primary Group=jabali-sockets — load-bearing for
  # panel-api — is left untouched (zero blast radius on existing installs).
  # Re-applied every start (RuntimeDirectory + socket are recreated each boot).
  getent group jabali-redis-clients >/dev/null || groupadd --system jabali-redis-clients
  install -d -m 0755 -o root -g root /etc/systemd/system/redis-server.service.d
  local acl_unit="/etc/systemd/system/redis-server.service.d/20-jabali-redis-clients.conf"
  local acl_unit_desired=$'# Managed by jabali - #406 / ADR-0148. Do NOT hand-edit.\n[Service]\nExecStartPost=+/bin/sh -c \'for i in 1 2 3 4 5; do [ -S /run/redis/redis.sock ] && break; sleep 1; done; setfacl -m g:jabali-redis-clients:rx /run/redis 2>/dev/null; setfacl -m g:jabali-redis-clients:rw /run/redis/redis.sock 2>/dev/null; true\'\n'
  if [[ ! -f "$acl_unit" ]] || ! cmp -s <(printf '%s' "$acl_unit_desired") "$acl_unit"; then
    printf '%s' "$acl_unit_desired" > "$acl_unit"; chmod 0644 "$acl_unit"
    systemctl daemon-reload
  fi

  # 1. Panel token — read-or-create in panel.env (never rotate: a rotate would
  #    orphan the running panel until restart).
  local panel_token
  panel_token="$(sed -n 's/^JABALI_REDIS_PANEL_TOKEN=//p' "$ENV_FILE" 2>/dev/null | head -1)"
  if [[ -z "$panel_token" ]]; then
    panel_token="$(openssl rand -hex 32)"
    printf 'JABALI_REDIS_PANEL_TOKEN=%s
' "$panel_token" >> "$ENV_FILE"
    _log "generated JABALI_REDIS_PANEL_TOKEN"
  fi

  # 1b. WP-cache HMAC secret (GH #407) — DEDICATED secret for deriving per-tenant
  #     cache ACL tokens. Kept SEPARATE from JABALI_REDIS_PANEL_TOKEN so the
  #     panel's master Redis credential can be rotated without stranding every
  #     tenant's wp-config cache token. read-or-create; never rotate here.
  if ! grep -q '^JABALI_WP_CACHE_HMAC_SECRET=' "$ENV_FILE" 2>/dev/null; then
    printf 'JABALI_WP_CACHE_HMAC_SECRET=%s
' "$(openssl rand -hex 32)" >> "$ENV_FILE"
    _log "generated JABALI_WP_CACHE_HMAC_SECRET"
  fi

  # 2. ACL file — panel user scoped to its keyspaces (jabali:* + automation:*)
  #    with +acl so it owns the per-tenant ACL lifecycle. `default` starts ON so
  #    the running dispatcher keeps working until we restart panel + lock below.
  # NOTE: the external aclfile accepts ONLY `user` lines — redis aborts startup
  # on any comment/blank line ("should start with user keyword"). Keep it pure.
  # Runtime tenant users (wp_<osuser>) are appended by panel-api via ACL SETUSER.
  cat > "$aclfile" <<ACL
user default on nopass ~* &* +@all
user jabali_panel on >${panel_token} ~jabali:* ~automation:* resetchannels +@all -@dangerous +acl +@connection
ACL
  chown redis:redis "$aclfile"; chmod 0640 "$aclfile"
  printf 'aclfile %s
' "$aclfile" > /etc/redis/redis.conf.d/20-jabali-acl.conf
  chmod 0644 /etc/redis/redis.conf.d/20-jabali-acl.conf

  systemctl restart redis-server
  sleep 1

  # 3. Restart panel so it authenticates as jabali_panel BEFORE we lock default.
  #    On a fresh install the panel isn't up yet — it starts later with the token.
  if systemctl is-active --quiet jabali-panel 2>/dev/null; then
    systemctl restart jabali-panel
    sleep 2
    if ! redis-cli -s "$sock" --user jabali_panel --pass "$panel_token" --no-auth-warning PING >/dev/null 2>&1; then
      _warn "jabali_panel Redis auth check failed — leaving default ON (notifications safe); investigate before relying on tenant isolation"
      return 0
    fi
  fi

  # 4. Lock default. Socket access alone is now inert (NOAUTH).
  sed -i 's|^user default on nopass.*|user default off nopass ~* resetchannels -@all|' "$aclfile"
  redis-cli -s "$sock" --user jabali_panel --pass "$panel_token" --no-auth-warning ACL LOAD >/dev/null 2>&1 || systemctl restart redis-server
  _ok "Redis ACLs configured: default locked, jabali_panel scoped, per-tenant users ready"
}

# ---------- step 2.5c: PostgreSQL 16 (M37 Phase 1) ---------------------------
#
# Installs PostgreSQL 16 from Debian's archive (matches our M7 stance
# of using stock distro packages, not vendor PGDG repos). Bound to
# loopback only via a drop-in conf — same pattern as MariaDB's
# skip-networking. The `postgres` superuser DSN credential is generated
# once and stashed at /etc/jabali-panel/postgres.password (root:jabali
# 0640) so panel-api (running as the jabali user) can read it without
# `sudo -u postgres`.
#
# Service stays disabled until server_settings.postgres_enabled is
# flipped on by the operator — fresh installs don't pay the resident
# memory cost of an unused DB engine. ADR-0091.

install_postgres() {
  _log "installing PostgreSQL (M37)"

  # Pre-create /run/postgresql ALWAYS (not gated on psql presence).
  # postgresql-common.postinst runs
  #   install -d -m 02775 -o postgres -g postgres /var/run/postgresql
  # which fails when invoked from the jabali-agent's mount namespace
  # (PrivateTmp=yes + various ProtectKernel*). Creating the dir up
  # front makes that postinst a no-op. The previous code gated
  # this behind `! command -v psql` which skipped pre-create on
  # partial-install state, wedging dpkg.
  install -d -m 02775 /run/postgresql 2>/dev/null || true

  # Debian meta — tracks whichever major version the release ships
  # (15 on bookworm, 17 on trixie). Hardcoding 16 broke trixie.
  if ! command -v psql >/dev/null 2>&1 || ! dpkg -s postgresql-common >/dev/null 2>&1; then
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      postgresql postgresql-contrib postgresql-client postgresql-common \
      || _die "postgres apt install failed"
  fi
  # Converge any partial install state (kernel-image race, namespace
  # failure, etc) — apt-get install -f re-runs postinst for every
  # half-configured package.
  DEBIAN_FRONTEND=noninteractive apt-get install -f -y >/dev/null 2>&1 || true
  if getent passwd postgres >/dev/null 2>&1; then
    chown postgres:postgres /run/postgresql 2>/dev/null || true
  fi

  # Discover installed major. First sub-directory under /etc/postgresql
  # is the cluster version (e.g. 17/main); fall back to pg_lsclusters.
  local pg_ver=""
  if [[ -d /etc/postgresql ]]; then
    pg_ver="$(ls -1 /etc/postgresql 2>/dev/null | head -n1)"
  fi
  if [[ -z "$pg_ver" ]] && command -v pg_lsclusters >/dev/null 2>&1; then
    pg_ver="$(pg_lsclusters -h 2>/dev/null | awk 'NR==1 {print $1}')"
  fi
  if [[ -z "$pg_ver" ]]; then
    _die "could not determine installed PostgreSQL major version"
  fi

  # Loopback-only listener. Default postgresql.conf already ships
  # listen_addresses='localhost'; explicit is better than implicit
  # (ADR-0050 unix-socket lockdown).
  local pg_dropin_dir="/etc/postgresql/${pg_ver}/main/conf.d"
  install -d -m 0755 -o postgres -g postgres "$pg_dropin_dir"
  local pg_dropin="$pg_dropin_dir/jabali.conf"
  cat >"$pg_dropin" <<'PG_DROPIN'
# Managed by jabali install.sh (M37). Do NOT hand-edit — the file
# is rewritten on every `jabali update`. To override, drop a higher-
# numbered file in this directory; postgresql.conf reads them in
# alphabetical order so 50-* + 90-* take precedence.
listen_addresses = 'localhost'
unix_socket_directories = '/run/postgresql'
unix_socket_permissions = 0775
PG_DROPIN
  chown postgres:postgres "$pg_dropin"
  chmod 0644 "$pg_dropin"

  # /etc/postgresql/${pg_ver}/main/pg_hba.conf — keep the default Debian
  # pattern (peer auth on socket, scram-sha-256 on TCP loopback).
  # No customisation needed for Phase 1; we never expose TCP and
  # peer-auth on the socket lets panel-api connect as `postgres`
  # via group membership (jabali in postgres group, set below).

  # Enroll the jabali service user in the postgres group so it can
  # read the unix socket (mode 0775 means rwx to postgres group).
  if getent group postgres >/dev/null 2>&1; then
    usermod -aG postgres "$SERVICE_USER" 2>/dev/null || true
  fi

  # Persisted password file for the postgres superuser. We don't
  # actually use password auth (peer auth wins on the socket), but
  # the file is the contract panel-api reads to discover whether
  # postgres is provisioned + which DSN to use.
  local pg_pw_file=/etc/jabali-panel/postgres.password
  if [[ ! -f "$pg_pw_file" ]]; then
    # 0755 (not 0750): /etc/jabali-panel must be traversable by every
    # OS hosting user. fpm-pre-start/agent read per-user config under it
    # while running as User=<hosting-user> (not root, not group jabali).
    # Sensitive children carry their own restrictive modes
    # (postgres.password 0640, migration-secrets 0750, restic-remotes
    # 0700, sso.key 0600, dkim 0750), so a traversable parent leaks
    # nothing. A 0750 parent crash-loops every per-user FPM (see
    # ensure_jabali_panel_dir_traversable + tools/test-fpm-exec-traversal.sh).
    install -d -m 0755 -o root -g "$SERVICE_USER" /etc/jabali-panel
    umask 077
    openssl rand -hex 32 >"$pg_pw_file"
    chmod 0640 "$pg_pw_file"
    chown root:"$SERVICE_USER" "$pg_pw_file"
    # Set the postgres role's password (so password auth works as a
    # backup if peer auth is ever broken). Idempotent — ALTER ROLE
    # is harmless on re-runs.
    local pg_pass
    pg_pass="$(cat "$pg_pw_file")"
    sudo -u postgres psql -tAc \
      "ALTER ROLE postgres WITH PASSWORD '${pg_pass//\'/\'\'}';" \
      >/dev/null 2>&1 || _warn "ALTER ROLE postgres password failed (psql may have been down)"
  fi

  # Reload to pick up the drop-in conf. Don't enable the service
  # at install time — server_settings.postgres_enabled gates it.
  if systemctl is-active --quiet postgresql; then
    systemctl reload postgresql || systemctl restart postgresql || true
  fi
  systemctl disable postgresql 2>/dev/null || true

  # Install PHP pgsql + pdo_pgsql for every installed PHP major so
  # Adminer + WordPress + tenant apps can speak Postgres. Without
  # these, Adminer's pgsql driver renders "No PHP plugin available
  # (PgSQL, PDO_PgSQL)". Idempotent: apt is a no-op when present.
  if [[ -d /etc/php ]]; then
    local php_pkgs=()
    for ver_dir in /etc/php/*/; do
      local ver
      ver="$(basename "$ver_dir")"
      [[ "$ver" =~ ^[0-9]+\.[0-9]+$ ]] || continue
      php_pkgs+=("php${ver}-pgsql")
    done
    if [[ ${#php_pkgs[@]} -gt 0 ]]; then
      DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        "${php_pkgs[@]}" \
        || _warn "php-pgsql install failed for ${php_pkgs[*]}"
      systemctl reload jabali-fpm@pma 2>/dev/null || true
    fi
  fi

  _ok "PostgreSQL installed + php-pgsql for every PHP major (service disabled — operator flips server_settings.postgres_enabled)"
}

# ---------- step 2.6: PowerDNS authoritative nameserver ----------------------

# converge_pdns_masking — GH #447. The base package batch apt-installs the
# pdns + pdns-recursor units on EVERY host, but their post-install config only
# runs when the DNS module is enabled (run_if_module dns install_powerdns). On a
# no-DNS host the units are therefore left unconfigured and sit in "failed",
# surfacing as a bogus "pdns.service is failed" warning on the dashboard and the
# Server Status page. Mask them when DNS is off so systemd reports "masked"
# (the capability-aware health paths omit masked/not-found units) instead of
# "failed"; unmask when DNS is on so install_powerdns can start them.
# Idempotent + convergent: safe on fresh install and on every `jabali update`.
converge_pdns_masking() {
  command -v systemctl >/dev/null 2>&1 || return 0
  # Desired DNS state: on an installed host the DB (server_settings.dns_enabled)
  # is truth — `jabali update` runs with JABALI_MODULES unset, so is_module_enabled
  # would wrongly report "on". Only fall back to the module selection on a fresh
  # install where the settings row doesn't exist yet.
  local dns_on=1 db_val=""
  if command -v mariadb >/dev/null 2>&1; then
    # `|| true` is load-bearing: on a FRESH install the server_settings table
    # doesn't exist yet (panel migrations run much later), so this query exits
    # non-zero. Under `set -e` + the __on_err trap a bare command-substitution
    # assignment would kill the installer silently right after the Redis step
    # (GH #545/#544: "Install failed, exit status 1", log ending at Redis). The
    # `|| true` makes the substitution succeed with empty output → the
    # fresh-install fallback (is_module_enabled) below takes over.
    db_val="$(mariadb jabali_panel -N -B -e \
      "SELECT dns_enabled FROM server_settings WHERE id=1;" 2>/dev/null || true)"
  fi
  if [[ -n "$db_val" ]]; then
    [[ "$db_val" == "0" ]] && dns_on=0
  elif ! is_module_enabled dns; then
    dns_on=0
  fi
  if [[ "$dns_on" -eq 1 ]]; then
    systemctl unmask pdns.service pdns-recursor.service >/dev/null 2>&1 || true
  else
    systemctl mask pdns.service pdns-recursor.service >/dev/null 2>&1 || true
    _log "DNS module off — masked pdns/pdns-recursor so they don't report failed (GH #447)"
  fi
}

install_powerdns() {
  _log "configuring PowerDNS (packages installed in base batch; this runs post-install config)"

  # pdns-server + pdns-backend-mysql are installed in install_base_packages's
  # one-shot apt batch. The policy-rc.d trap that prevents pdns from
  # auto-starting before its MySQL backend is wired up ALSO lives in
  # install_base_packages — the trap wraps the entire batch so every
  # service defers its start to its own config function (here, for pdns).

  if ! dpkg -s pdns-server >/dev/null 2>&1; then
    _die "pdns-server not installed — install_base_packages should have installed it"
  fi

  # The config directory for our env/cred files must exist before we
  # try to write into it. The panel's own config.toml lives here too;
  # write_config_file would normally create it, but install_powerdns
  # runs first.
  mkdir -p /etc/jabali-panel
  chmod 0755 /etc/jabali-panel

  # The Debian package drops a default /etc/powerdns/pdns.d/*.conf that
  # wires up the bind backend. We don't want that — replace the whole
  # conf directory with our own minimal config pointing at the MySQL
  # backend + our dedicated database.
  local conf_d="/etc/powerdns/pdns.d"
  mkdir -p "$conf_d"
  # Delete the Debian package's default backend configs, but PRESERVE our own
  # 01-jabali-mysql.conf — deleting it here would defeat the byte-identical
  # cmp guard below (the file would always be absent at the `[[ -f ]]` check),
  # forcing a pdns.conf rewrite + service restart on EVERY install.sh run
  # (every `jabali update`, every `--install-module dns`). Keeping it lets the
  # guard skip the restart when nothing changed. (M353 idempotency fix.)
  find "$conf_d" -maxdepth 1 -type f -name '*.conf' ! -name '01-jabali-mysql.conf' -delete

  # Credentials for the pdns DB user. Generated once, stored in
  # /etc/jabali-panel/pdns.env so the panel-api can read the same
  # password when it opens a connection.
  local pdns_env_file="/etc/jabali-panel/pdns.env"
  local pdns_password
  if [[ -f "$pdns_env_file" ]] && grep -q '^PDNS_DB_PASSWORD=' "$pdns_env_file"; then
    pdns_password="$(. "$pdns_env_file"; printf '%s' "$PDNS_DB_PASSWORD")"
    _log "reusing existing PowerDNS DB password from $pdns_env_file"
  else
    pdns_password="$(openssl rand -hex 24)"
    install -m 0640 -o root -g "$SERVICE_USER" /dev/null "$pdns_env_file"
    cat > "$pdns_env_file" <<PDNSEOF
# PowerDNS database credentials. Generated by install.sh.
# Consumed by the panel-api reconciler and by pdns.conf below.
PDNS_DB_NAME=jabali_pdns
PDNS_DB_USER=jabali_pdns
PDNS_DB_PASSWORD=${pdns_password}
PDNSEOF
    chmod 0640 "$pdns_env_file"
    _ok "generated PowerDNS DB password → $pdns_env_file"
  fi

  # Provision the jabali_pdns database + user. Idempotent: CREATE
  # DATABASE IF NOT EXISTS; CREATE USER IF NOT EXISTS.
  mariadb -uroot <<SQL
CREATE DATABASE IF NOT EXISTS jabali_pdns CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER IF NOT EXISTS 'jabali_pdns'@'localhost' IDENTIFIED BY '${pdns_password}';
ALTER USER 'jabali_pdns'@'localhost' IDENTIFIED BY '${pdns_password}';
GRANT ALL PRIVILEGES ON jabali_pdns.* TO 'jabali_pdns'@'localhost';
FLUSH PRIVILEGES;
SQL

  # Load PowerDNS's native schema (domains, records, supermasters,
  # comments, domainmetadata, cryptokeys, tsigkeys). File ships with the
  # pdns-backend-mysql package; path has been stable for years.
  local schema_file
  if [[ -f /usr/share/pdns-backend-mysql/schema/schema.mysql.sql ]]; then
    schema_file=/usr/share/pdns-backend-mysql/schema/schema.mysql.sql
  elif [[ -f /usr/share/doc/pdns-backend-mysql/schema.mysql.sql ]]; then
    schema_file=/usr/share/doc/pdns-backend-mysql/schema.mysql.sql
  else
    schema_file="$(find /usr/share -name 'schema.mysql.sql' -path '*pdns*' 2>/dev/null | head -n1)"
  fi
  if [[ -z "$schema_file" || ! -f "$schema_file" ]]; then
    _die "can't find PowerDNS MySQL schema; aborting. Check pdns-backend-mysql install."
  fi

  # Only load if the domains table isn't already present (idempotent).
  local table_exists
  table_exists="$(mariadb -uroot -Ns -e "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='jabali_pdns' AND table_name='domains';")"
  if [[ "$table_exists" == "0" ]]; then
    _log "loading PowerDNS schema from $schema_file"
    mariadb -uroot jabali_pdns < "$schema_file"
    _ok "PowerDNS schema loaded"
  else
    _log "PowerDNS schema already present in jabali_pdns — skipping reload"
  fi

  # Write pdns.conf. Single file, minimal surface.
  #
  # Idempotency (M6.3): render to $pdns_conf.new first; if byte-identical
  # to the live file, skip the write + service restart. This keeps a
  # re-run of install.sh on an already-converged host silent (no DNS
  # service bounce, no config rewrite, journal clean).
  local pdns_conf=/etc/powerdns/pdns.d/01-jabali-mysql.conf
  local pdns_conf_new="${pdns_conf}.new"

  # Enumerate every global-scope IPv4 + IPv6 on the host. 'scope global'
  # automatically excludes 127.0.0.0/8 (which is host scope) and
  # fe80::/10 (link scope). Sorted for deterministic output so the .new
  # vs live file comparison stays stable across re-runs that didn't
  # actually change the IP layout. IPv6 addresses get bracketed for
  # pdns's "addr:port" parser.
  local pdns_local_addresses
  # paste -sd takes one char per separator + cycles when given more,
  # so use a single comma here and let pdns trim whitespace itself.
  # Skip virtual-bridge interfaces — LXC's lxcbr0 (10.0.3.1/24) ships
  # with its own dnsmasq on :53, libvirt's virbr0 same idea, Docker's
  # docker0 / br-* same. Binding pdns there collides with the
  # bridge's resolver and crashes pdns at boot. Public + loopback
  # addresses only.
  local skip_iface_re='^(lxcbr[0-9]+|virbr[0-9]+|docker[0-9]+|br-[0-9a-f]+|cni[0-9]+|veth.*|tailscale.*|wg[0-9]+)$'
  pdns_local_addresses="$({
    ip -4 -o addr show scope global 2>/dev/null \
      | awk -v re="$skip_iface_re" '$2 !~ re { split($4,a,"/"); print a[1] ":53" }'
    ip -6 -o addr show scope global 2>/dev/null \
      | awk -v re="$skip_iface_re" '$2 !~ re { split($4,a,"/"); print "[" a[1] "]:53" }'
    printf '127.0.0.1:5300\n[::1]:5300\n'
  } | sort -u | paste -sd ',' -)"
  if [[ -z "$pdns_local_addresses" ]]; then
    # Defensive: should never happen because the loopback entries are
    # always emitted, but guard against an unexpected empty value
    # producing an invalid pdns.conf line.
    _die "pdns local-address enumeration produced empty string"
  fi
  cat > "$pdns_conf_new" <<PDNSCONF
# Managed by Jabali Panel install.sh. Hand edits will be overwritten
# the next time install.sh runs.
launch=gmysql
# M25 Step 6: dial MariaDB over its Debian-default Unix socket. PDNS's
# gmysql backend honors gmysql-socket for client-mode connections; when
# set, host/port are ignored. Lower latency than TCP loopback and
# (post-skip-networking, M25.1) the only available path. Keeping
# host/port out entirely is intentional — having both sometimes confuses
# packagings that don't try the socket first.
gmysql-socket=/var/run/mysqld/mysqld.sock
gmysql-dbname=jabali_pdns
gmysql-user=jabali_pdns
gmysql-password=${pdns_password}

# DNSSEC (M15, ADR-0076): enable the gmysql-backed DNSSEC data path.
# Without this, pdnsutil secure-zone errors out with "no DNSSEC capable
# backends". The schema already provisions cryptokeys / tsigkeys /
# domainmetadata tables for exactly this path.
gmysql-dnssec=yes

# Split-port binding (M6.3, ADR-0047): port 53 on the host's public IP
# keeps serving authoritative queries from the open internet, while
# loopback moves to port 5300 — reserved for pdns-recursor to forward
# local queries into. pdns-recursor owns 127.0.0.1:53 + [::1]:53, which
# is what systemd-resolved points at via zz-jabali-recursor.conf.
#
# Every entry lists port explicitly. pdns-server defaults local-port to
# 53 when unspecified — DO NOT rely on that default here; a future
# port flip would otherwise break silently on only part of the binds.
# Syntax pinned to pdns-server 4.9+ (Debian 13 default package).
#
# We deliberately do NOT bind 0.0.0.0: systemd-resolved's stub listens
# on 127.0.0.53:53 and recursor listens on 127.0.0.1:53, so any
# wildcard bind would collide with one of them (EADDRINUSE). Instead
# enumerate every global-scope IP on the host (IPv4 + IPv6) at install
# time and bind each on :53 explicitly. fe80::/10 + 127/8 + ::1 are
# excluded by 'scope global'. Operators who add IPs after install
# re-run install.sh (or jabali update -f triggers it) to widen.
local-address=${pdns_local_addresses}

# socket-dir is intentionally not set — Debian's pdns.service has
# RuntimeDirectory=powerdns which auto-creates /run/powerdns with the
# right ownership. Overriding socket-dir here collides with pdns's own
# attempt to create the directory (it fails under LXC drop-ins).

# AXFR authorization (JAB-350). Per-zone allow-lists + NOTIFY targets are
# managed via the panel's domainmetadata table (ALLOW-AXFR-FROM and
# ALSO-NOTIFY kinds), which authorize any configured secondary nameserver.
#
# The GLOBAL ACL is pinned EXPLICITLY to loopback here — it is NOT left to
# PowerDNS's built-in default. Relying on the default is unsafe: a permissive
# global value from an operator edit, a differing package/build default, or a
# stale hand-written /etc/powerdns/pdns.conf would otherwise let ANY internet
# client transfer every managed zone (bulk enumeration of hostnames, MX/service
# topology, and TXT metadata — verified reproducible). This drop-in is read
# after the main pdns.conf, so this line overrides such a permissive value and
# repairs the host on the next `jabali update` (config change → pdns restart).
# Configured secondaries still transfer via their per-zone ALLOW-AXFR-FROM
# metadata, which is unioned with this global; loopback stays allowed for local
# operator troubleshooting (dig AXFR @127.0.0.1).
allow-axfr-ips=127.0.0.0/8,::1
disable-axfr-rectify=no

# GH #896: a freshly created domain gets its OWN pdns zone, inserted
# straight into the gmysql backend. PowerDNS caches the LIST of zones
# (the "zone cache") and only re-reads it every zone-cache-refresh-
# interval seconds — default 300. Until that refresh, the new zone does
# not exist as far as pdns is concerned: queries fall through to the
# parent zone and answer NXDOMAIN, so a brand-new (sub)domain is dead
# for up to five minutes even though the SQL rows are all there.
# Measured live: 161 s of NXDOMAIN on a fresh subdomain.
#
# No control-channel fix exists: rediscover DOES refresh this cache on
# 4.9 (verified: DLRediscoverHandler → UeberBackend::updateZoneCache in
# the 4.9.x source), but calling it is what trips PowerDNS/pdns#11416 —
# a concurrent rediscover + periodic refresh run replace() at the same
# time and AuthZoneCache asserts (`pending->d_replacePending`), SIGABRT,
# pdns down. Observed in production on 4.9.16 during bulk zone upserts.
#
# 0 disables the zone cache entirely: every zone lookup then hits the
# gmysql backend directly (the pre-4.5 behaviour), so new zones are
# servable IMMEDIATELY — better than the old =10 bound — and
# updateZoneCache early-returns on !isEnabled(), so the assertion can
# never fire. At panel scale (tens-hundreds of zones over a local
# MariaDB socket, with the packet cache in front) the per-query backend
# hit is unmeasurable; the zone cache exists for million-zone operators.
zone-cache-refresh-interval=0
PDNSCONF

  # Idempotency: if .new matches live, skip write + restart.
  local pdns_changed=0
  if [[ -f "$pdns_conf" ]] && cmp -s "$pdns_conf" "$pdns_conf_new"; then
    rm -f "$pdns_conf_new"
    _log "pdns.conf already current; skipping write + restart"
  else
    mv "$pdns_conf_new" "$pdns_conf"
    chmod 0640 "$pdns_conf"
    chown root:pdns "$pdns_conf" 2>/dev/null || true
    pdns_changed=1
  fi

  systemctl enable pdns >/dev/null 2>&1 || true
  if [[ "$pdns_changed" == "1" ]]; then
    _log "restarting pdns (config changed)"
    systemctl restart pdns
    # Quick sanity probe — if pdns isn't running after restart something
    # is broken and install.sh should fail fast rather than continue past it.
    sleep 2
    if ! systemctl is-active --quiet pdns; then
      systemctl status pdns --no-pager || true
      _die "pdns failed to start; check 'journalctl -u pdns' for details"
    fi
  elif ! systemctl is-active --quiet pdns; then
    # Unchanged config but service isn't running (crashed, disabled, etc.).
    # Start it without a restart so we don't bounce a working service, but
    # do converge the "service should be active" invariant.
    _log "pdns config unchanged but service inactive — starting"
    systemctl start pdns
    sleep 2
    systemctl is-active --quiet pdns \
      || _die "pdns failed to start; check 'journalctl -u pdns' for details"
  fi
  _ok "PowerDNS running on ${pdns_local_addresses} (authoritative + recursor forward target on :5300)"
}

# ---------- step 2.6c: pdns-recursor for local self-resolution (M6.3) -------
#
# pdns-recursor owns loopback :53 (both v4+v6). It forwards authoritative
# zones that the panel owns into pdns-server at 127.0.0.1:5300 via
# /etc/powerdns/recursor.forwards (one line per zone, reconciler-owned),
# and recurses everything else to public upstream (1.1.1.1, 9.9.9.9).
#
# systemd-resolved's stub at 127.0.0.53:53 forwards into this recursor via
# the zz-jabali-recursor.conf drop-in (written below). Net effect: every
# /etc/resolv.conf-based resolver call (glibc NSS, every app) goes
# stub → recursor → either authoritative (for panel zones) or public
# (for everything else).
#
# Security: allow-from is explicitly loopback-only. Debian's package
# default (127.0.0.0/8 + RFC1918) would open the resolver to every
# container on an LXC bridge. install.sh hard-fails if the rendered
# local-address or allow-from drift away from loopback.
#
# See docs/adr/0047-pdns-recursor-local-self-resolution.md for the full
# decision record and plans/m6.3-pdns-recursor.md for the plan.

# harvest_reachable_upstream — echo ONE real external resolver this host can
# actually reach, for use as the recursor's PRIMARY forward target. GH #545:
# locked-down VPS (OVH/Proxmox) often block outbound UDP 53 to public anycast
# resolvers (1.1.1.1/9.9.9.9) and only allow DNS via the provider's own
# resolver. That provider resolver survives our systemd-resolved global drop-in
# as a per-link (DHCP) DNS server, so resolvectl still knows it. We test each
# candidate with a real dig before trusting it, and skip loopback stubs
# (127.0.0.0/8 / ::1) so we can never forward the recursor back at resolved and
# create a self-loop (the pdns-recursor-self-loop scar). Prints nothing +
# returns 1 when no external resolver answers (caller keeps the public default).
harvest_reachable_upstream() {
  local ns
  local candidates=""
  # 1) systemd-resolved's per-link (DHCP-provided) upstreams — the provider's
  #    resolver, which keeps working on UDP-53-blocked carriers.
  if command -v resolvectl >/dev/null 2>&1; then
    # "Current DNS Server:" + the per-link "DNS Servers:" lines are the
    # DHCP/provider resolvers. Exclude "Fallback DNS Servers:" (that's jabali's
    # own 1.1.1.1;9.9.9.9 fallback, which is exactly what's blocked here) by
    # anchoring the second pattern so a leading "Fallback" doesn't match.
    candidates+=" $(resolvectl status 2>/dev/null \
      | awk '/Current DNS Server:/{print $NF} /^[[:space:]]*DNS Servers:/{for(i=3;i<=NF;i++)print $i}')"
  fi
  # 2) any non-loopback nameserver still visible in /etc/resolv.conf.
  candidates+=" $(awk '/^[[:space:]]*nameserver[[:space:]]/{print $2}' /etc/resolv.conf 2>/dev/null)"
  # 3) the upstream we harvested EARLY into resolved's drop-in, BEFORE flipping
  #    resolv.conf at the recursor. GH #545: on a masked-resolved / LXC host the
  #    provider resolver is gone from resolvectl + resolv.conf by probe-time
  #    (both now point at the loopback recursor), but this drop-in still records
  #    it — this is what makes the auto-heal actually recover on a locked-down box.
  candidates+=" $(awk -F= '/^[[:space:]]*DNS=/{print $2}' /etc/systemd/resolved.conf.d/jabali.conf 2>/dev/null)"
  # 4) systemd-networkd per-interface DNS= (DHCP/static provider resolver).
  candidates+=" $(awk -F= '/^[[:space:]]*DNS=/{print $2}' /etc/systemd/network/*.network 2>/dev/null)"
  local seen=" "
  for ns in $candidates; do
    case "$ns" in 127.*|::1|"") continue ;; esac
    case "$seen" in *" $ns "*) continue ;; esac
    seen+="$ns "
    if dig +short +timeout=2 +tries=1 "@${ns}" deb.debian.org 2>/dev/null | grep -qE '^[0-9.]+$'; then
      echo "$ns"
      return 0
    fi
  done
  return 1
}

# repoint_recursor_forward <upstream> — rewrite the pdns-recursor forward target
# to <upstream> and restart it. GH #545 auto-heal (called only when the default
# public forward is unreachable). Returns non-zero if the restart fails.
repoint_recursor_forward() {
  local up="$1" conf=/etc/powerdns/recursor.conf
  [[ -n "$up" && -f "$conf" ]] || return 1
  sed -i -E "s|^forward-zones-recurse=\\.=.*|forward-zones-recurse=.=${up}|" "$conf"
  timeout 30 systemctl restart pdns-recursor || return 1
  systemctl is-active --quiet pdns-recursor || return 1
  return 0
}

# mark_dns_degraded <reason> — GH #545. The resolved→recursor→public DNS chain is
# broken and auto-heal (harvest_reachable_upstream + repoint) could not recover
# it. Historically this called _die and aborted the whole install right at the DNS
# step — the single most-reported fresh-install blocker on locked-down VPS. Per
# operator decision we DO NOT abort: record a marker the panel surfaces as a
# persistent warning, log the fix, and let the install finish so the user gets a
# working panel to repair DNS from (Admin → DNS / JABALI_DNS_FORWARDER, ADR-0047).
# Downstream steps that need DNS may still fail on a truly-offline box, but a
# recoverable DNS hiccup must never cost the entire install.
mark_dns_degraded() {
  local reason="$1"
  mkdir -p /etc/jabali 2>/dev/null || true
  {
    printf 'reason=%s\n' "$reason"
    printf 'hint=re-run with JABALI_DNS_FORWARDER=<a resolver this box can reach>, or fix the recursor forward in Admin -> DNS (ADR-0047)\n'
    printf 'since=install\n'
  } > /etc/jabali/dns-degraded 2>/dev/null || true
  chmod 0644 /etc/jabali/dns-degraded 2>/dev/null || true
  _warn "DNS chain unresolved (${reason}). CONTINUING the install so you get a working panel — DNS is DEGRADED. Marker: /etc/jabali/dns-degraded. Fix: re-run with JABALI_DNS_FORWARDER=<a reachable resolver> (ADR-0047) or repair from Admin -> DNS. The panel shows a warning until this resolves."
  return 0
}

install_pdns_recursor() {
  _log "configuring pdns-recursor"

  # Package must already be installed via install_base_packages' apt batch.
  # (The getent-group hard-fail earlier catches postinst failures.)
  if ! dpkg -s pdns-recursor >/dev/null 2>&1; then
    _die "pdns-recursor not installed — install_base_packages should have installed it"
  fi

  # --- recursor.conf ------------------------------------------------
  local rec_conf=/etc/powerdns/recursor.conf
  local rec_conf_new="${rec_conf}.new"

  # Managed-header in line 1 is the "did install.sh write this?" marker
  # downstream idempotency guards test against.
  local _rec_recurse_upstream
  if [[ -n "$DNS_FORWARDER" ]]; then
    # Forwarder mode: skip systemd-resolved and talk directly to the
    # operator-supplied forwarder (typically a corporate / internal
    # resolver reachable on the LXC bridge).
    _rec_recurse_upstream="$DNS_FORWARDER"
  else
    # Default: forward direct to public DNS over UDP/853.
    #
    # An earlier attempt (2026-06-01) routed recursor through the
    # resolved stub at 127.0.0.53 to get DoT/853 for free on
    # UDP/53-blocked LXC carriers. That works only when resolved's
    # upstream is a real public resolver. On every standard install
    # the zz-jabali-recursor.conf drop-in below resets resolved's
    # DNS to 127.0.0.1 (= the recursor), so chaining recursor back
    # into 127.0.0.53 closes a fatal loop:
    #
    #   client -> resolved (127.0.0.53)
    #     -> upstream=127.0.0.1 (recursor)
    #       -> forward-zones-recurse=.=127.0.0.53 (resolved)
    #         -> ... timeout -> Probe 1 dies -> installer aborts
    #
    # Caught on a fresh Debian 13 host (GH-126, 2026-06-03).
    #
    # For carriers that genuinely block UDP/53 egress, the escape
    # hatch stays the JABALI_DNS_FORWARDER env var (see ADR-0047):
    # operator points it at a reachable TCP-capable resolver.
    _rec_recurse_upstream="1.1.1.1;9.9.9.9"
  fi
  cat > "$rec_conf_new" <<RECCONF
# Managed by jabali-panel install.sh (M6.3). Hand edits will be overwritten
# on the next install.sh run. See docs/adr/0047-pdns-recursor-local-self-resolution.md
local-address=127.0.0.1, ::1
local-port=53

# Amplification defense (ADR-0047): loopback-only. NARROWER than the
# Debian package default (127.0.0.0/8 + RFC1918) because LXC bridge
# interfaces live in RFC1918 ranges and the default would expose the
# resolver to every co-tenant container.
allow-from=127.0.0.0/8, ::1/128

# Per-zone forward-to-authoritative file. One line per zone, format:
#   <zone>=127.0.0.1:5300
# Reconciler-owned via panel-agent's pdns.recursor_add_zone /
# pdns.recursor_remove_zone commands (atomic write + strict validator
# + rec_control reload-zones + SOA post-probe + rollback-on-fail).
# Never hand-edit on a live host; use 'jabali pdns backfill --yes'
# if a stale state needs reconverging.
forward-zones-file=/etc/powerdns/recursor.forwards

# Everything else recurses through public upstream. We DO NOT chain
# through jabali.conf's DNS= — that config lives in systemd-resolved
# and is only consulted by the stub, not by the recursor.
forward-zones-recurse=.=${_rec_recurse_upstream}

# DNSSEC: off. systemd-resolved validates DNSSEC upstream already.
# Doubling up costs CPU per query for no security benefit on a
# single-host panel.
dnssec=off

# Conservative defaults for a single-tenant panel. max-cache-entries
# tuned to 50000 to hold a few thousand domains' worth of NXDOMAIN
# + short-TTL answers without thrashing.
threads=2
max-cache-entries=50000
quiet=yes
loglevel=4
setuid=pdns
setgid=pdns
RECCONF

  # Pre-install validator (hard-fail): confirm the just-rendered
  # local-address + allow-from are loopback-only. This is a defense
  # against someone (operator, future install.sh edit, merge accident)
  # widening the bind without realizing the blast radius.
  local rec_local_address rec_allow_from
  rec_local_address="$(awk -F= '/^local-address=/{print $2; exit}' "$rec_conf_new" | tr -d '[:space:]')"
  rec_allow_from="$(awk -F= '/^allow-from=/{print $2; exit}' "$rec_conf_new" | tr -d '[:space:]')"
  # Split on commas and verify every entry.
  local IFS_save="$IFS"
  IFS=','
  local addr
  for addr in $rec_local_address; do
    case "$addr" in
      127.0.0.1|::1) : ;;
      *) IFS="$IFS_save"; _die "recursor.conf local-address contains non-loopback '$addr' — would expose open resolver publicly" ;;
    esac
  done
  for addr in $rec_allow_from; do
    case "$addr" in
      127.0.0.0/8|"::1/128") : ;;
      *) IFS="$IFS_save"; _die "recursor.conf allow-from contains non-loopback CIDR '$addr' — amplification defense breach" ;;
    esac
  done
  IFS="$IFS_save"

  # Idempotency: skip rewrite + restart if live file is byte-identical.
  local recursor_changed=0
  if [[ -f "$rec_conf" ]] && cmp -s "$rec_conf" "$rec_conf_new"; then
    rm -f "$rec_conf_new"
    _log "recursor.conf already current; skipping write + restart"
  else
    mv "$rec_conf_new" "$rec_conf"
    chmod 0644 "$rec_conf"
    chown root:root "$rec_conf"
    recursor_changed=1
  fi

  # --- recursor.forwards (seed empty; reconciler owns content) ------
  # Group `pdns` (not `pdns-recursor`) — the recursor process runs as
  # `pdns:pdns` per setuid/setgid in recursor.conf above, matching the
  # Debian package's user model (pdns-server and pdns-recursor share
  # the `pdns` account — pdns-recursor does NOT create its own user).
  local rec_forwards=/etc/powerdns/recursor.forwards
  if [[ ! -f "$rec_forwards" ]]; then
    install -m 0640 -o root -g pdns /dev/null "$rec_forwards"
    _log "seeded empty /etc/powerdns/recursor.forwards (reconciler populates)"
  fi

  # --- systemd ordering drop-in: pdns-recursor After=pdns ----------
  install -d -m 0755 /etc/systemd/system/pdns-recursor.service.d
  local rec_after_dropin=/etc/systemd/system/pdns-recursor.service.d/10-jabali-after.conf
  local rec_after_dropin_new="${rec_after_dropin}.new"
  cat > "$rec_after_dropin_new" <<'AFTEREOF'
# Managed by jabali-panel install.sh (M6.3, ADR-0047). Ensures
# pdns-recursor doesn't start before pdns-server — recursor forwards
# authoritative zones into pdns:5300, so starting recursor first would
# hit connection-refused until pdns comes up.
[Unit]
After=pdns.service
Wants=pdns.service
AFTEREOF
  if [[ -f "$rec_after_dropin" ]] && cmp -s "$rec_after_dropin" "$rec_after_dropin_new"; then
    rm -f "$rec_after_dropin_new"
  else
    mv "$rec_after_dropin_new" "$rec_after_dropin"
    chmod 0644 "$rec_after_dropin"
    recursor_changed=1
    _log "wrote pdns-recursor.service.d/10-jabali-after.conf"
  fi

  # --- ExecStart override for --enable-old-settings ---------------
  # PowerDNS Recursor 5.2 (Debian 13 trixie ships 5.2.8) flipped the
  # config-file default from old-style `key=value` to YAML. Our
  # recursor.conf above is still key=value, and without this flag
  # 5.2+ dies at startup with:
  #   error="invalid type: string \"local-address=127.0.0.1, ::1\"
  #   ... Old-style settings syntax not enabled by default anymore.
  #   Use YAML or enable with --enable-old-settings on the command line
  # The flag is the official escape hatch until we do the YAML
  # conversion (tracked as an M6.3 follow-up).
  #
  # Older releases (Ubuntu 24.04 noble ships pdns-recursor 4.9) don't
  # know the flag at all and reject startup with:
  #   error="Trying to set unknown setting 'enable-old-settings'"
  # so the override has to be conditional. Check the binary's
  # version and only add --enable-old-settings on 5.2+.
  local rec_version rec_major rec_minor rec_needs_old_flag=0
  rec_version="$(pdns_recursor --version 2>&1 | grep -oE 'PowerDNS Recursor [0-9]+\.[0-9]+' | awk '{print $NF}' | head -1)"
  rec_major="${rec_version%%.*}"
  rec_minor="${rec_version#*.}"
  if [[ -n "$rec_major" && -n "$rec_minor" ]]; then
    if (( rec_major > 5 )) || (( rec_major == 5 && rec_minor >= 2 )); then
      rec_needs_old_flag=1
    fi
  fi

  local rec_exec_dropin=/etc/systemd/system/pdns-recursor.service.d/20-jabali-old-settings.conf
  if (( rec_needs_old_flag == 1 )); then
    local rec_exec_dropin_new="${rec_exec_dropin}.new"
    cat > "$rec_exec_dropin_new" <<'EXECEOF'
# Managed by jabali-panel install.sh (M6.3). pdns-recursor 5.2+ made
# YAML the default config format; we still emit old-style key=value
# in /etc/powerdns/recursor.conf. --enable-old-settings keeps the
# old parser until a later M6.3.x converts our config to YAML. See
# docs/adr/0047-pdns-recursor-local-self-resolution.md for context.
[Service]
ExecStart=
ExecStart=/usr/sbin/pdns_recursor --daemon=no --write-pid=no --disable-syslog --log-timestamp=no --enable-old-settings
EXECEOF
    if [[ -f "$rec_exec_dropin" ]] && cmp -s "$rec_exec_dropin" "$rec_exec_dropin_new"; then
      rm -f "$rec_exec_dropin_new"
    else
      mv "$rec_exec_dropin_new" "$rec_exec_dropin"
      chmod 0644 "$rec_exec_dropin"
      recursor_changed=1
      _log "wrote pdns-recursor.service.d/20-jabali-old-settings.conf (recursor ${rec_version})"
    fi
  else
    # 4.x doesn't know the flag — drop any leftover override from a
    # previous install on a host that has since been downgraded or
    # the binary swapped (e.g. Sury → Ubuntu repo).
    if [[ -f "$rec_exec_dropin" ]]; then
      rm -f "$rec_exec_dropin"
      recursor_changed=1
      _log "removed stale pdns-recursor.service.d/20-jabali-old-settings.conf (recursor ${rec_version:-<unknown>} doesn't support --enable-old-settings)"
    fi
  fi

  # --- zz-jabali-recursor.conf drop-in for systemd-resolved --------
  #
  # Alphabetically AFTER the panel-UI-managed jabali.conf. Per
  # systemd-resolved.conf(5): "Setting this variable to an empty list
  # (as in DNS=) resets the list of servers to the empty list, all prior
  # assignments will be cleared." So `DNS=` (reset) + `DNS=127.0.0.1`
  # makes 127.0.0.1 the only resolver, regardless of what jabali.conf
  # contributed. install.sh gates on resolvectl showing DNS Servers:
  # 127.0.0.1 only — if the merge semantics differ, install fails
  # loudly so the fallback (consolidate into jabali.conf) can be
  # invoked per the M6.3 plan.
  install -d -m 0755 /etc/systemd/resolved.conf.d
  local resolved_dropin=/etc/systemd/resolved.conf.d/zz-jabali-recursor.conf
  local resolved_dropin_new="${resolved_dropin}.new"
  cat > "$resolved_dropin_new" <<'RESOLVEDEOF'
# Managed by jabali-panel install.sh (M6.3, ADR-0047). Do not hand-edit:
# the panel DNS Resolvers UI continues to manage
# /etc/systemd/resolved.conf.d/jabali.conf (admin upstream DNS); this
# file layers on top alphabetically-last to force every /etc/resolv.conf
# query through the local pdns-recursor at 127.0.0.1, which forwards
# panel-authoritative zones to pdns-server:5300 and recurses everything
# else to public upstream.
[Resolve]
DNS=
DNS=127.0.0.1
FallbackDNS=1.1.1.1 9.9.9.9
DNSSEC=no
RESOLVEDEOF

  local resolved_changed=0
  if [[ -f "$resolved_dropin" ]] && cmp -s "$resolved_dropin" "$resolved_dropin_new"; then
    rm -f "$resolved_dropin_new"
  else
    mv "$resolved_dropin_new" "$resolved_dropin"
    chmod 0644 "$resolved_dropin"
    chown root:root "$resolved_dropin"
    resolved_changed=1
    _log "wrote resolved.conf.d/zz-jabali-recursor.conf"
  fi

  # --- systemd-resolved After=pdns-recursor drop-in REMOVED ---------
  # Earlier installs wrote /etc/systemd/system/systemd-resolved.service.d/
  # 10-jabali-after.conf with `After=pdns-recursor.service` to delay
  # resolved until the recursor was ready. That drop-in caused a fatal
  # boot-time ordering cycle on every reboot:
  #
  #   resolved (Before=network.target via vendor unit)
  #     ← After=pdns-recursor (this drop-in)
  #     ← pdns-recursor.service
  #         ← After=pdns.service (10-jabali-after.conf on the recursor)
  #         ← pdns.service
  #             ← network-online.target
  #             ← network.target
  #             ← systemd-resolved.service  *** cycle closes here ***
  #
  # systemd breaks cycles by deleting jobs from the start transaction.
  # On the fleet host (mx.jabali-panel.com 2026-05-09 21:29 boot) this
  # deleted the start jobs for systemd-resolved, mariadb, postgresql,
  # AND network-online — bricking every dependent service (panel-api,
  # Kratos, pdns itself, nginx 502s up the stack).
  #
  # Cure: don't override resolved's ordering at all. The resolv.conf
  # stub points at 127.0.0.1 (recursor) via zz-jabali-recursor.conf;
  # recursor typically resolves DNS in sub-second on cold boot, and
  # any queries in that gap retry at the NSS layer. The "live stub
  # with a dead recursor" race window is tolerable; a permanent boot
  # failure is not.
  #
  # Below: unconditionally remove the legacy file on every install so
  # `jabali update` heals already-broken hosts.
  local resolved_legacy_dropin=/etc/systemd/system/systemd-resolved.service.d/10-jabali-after.conf
  if [[ -e "$resolved_legacy_dropin" ]]; then
    rm -f "$resolved_legacy_dropin"
    # If the dir is now empty, drop it too — leaving an empty
    # service.d/ confuses `systemd-delta` output.
    rmdir /etc/systemd/system/systemd-resolved.service.d 2>/dev/null || true
    resolved_changed=1
    _log "removed legacy systemd-resolved.service.d/10-jabali-after.conf (boot ordering cycle)"
  fi

  # --- daemon-reload only if ordering drop-ins changed --------------
  if [[ "$recursor_changed" == "1" || "$resolved_changed" == "1" ]]; then
    systemctl daemon-reload
  fi

  # --- start + restart sequence ------------------------------------
  # Order: recursor FIRST (so resolved has something to forward to).
  # Use restart-if-changed / start-if-inactive, never a blind restart.
  #
  # Every systemctl call below is wrapped in `timeout 30` — bare
  # systemctl restart BLOCKS until the unit stabilises, which means a
  # bad config + Restart=on-failure loop (as happened with the 5.2
  # YAML-default incident) hangs the install script indefinitely
  # instead of surfacing the error. 30s is well above any legitimate
  # start time for these units; if we hit it, something is wrong and
  # we dump the journal + _die so the operator sees the real cause.
  timeout 30 systemctl enable pdns-recursor >/dev/null 2>&1 || true
  if [[ "$recursor_changed" == "1" ]]; then
    _log "restarting pdns-recursor (config changed)"
    timeout 30 systemctl restart pdns-recursor \
      || { journalctl -u pdns-recursor --no-pager -n 50 >&2
           _die "pdns-recursor restart failed or timed out (30s); see journal above"; }
  elif ! systemctl is-active --quiet pdns-recursor; then
    _log "starting pdns-recursor (was inactive)"
    timeout 30 systemctl start pdns-recursor \
      || { journalctl -u pdns-recursor --no-pager -n 50 >&2
           _die "pdns-recursor start failed or timed out (30s); see journal above"; }
  fi
  sleep 1
  systemctl is-active --quiet pdns-recursor \
    || { journalctl -u pdns-recursor --no-pager -n 50 >&2; _die "pdns-recursor failed to start; see journal above"; }

  if [[ "$resolved_changed" == "1" ]]; then
    _log "restarting systemd-resolved (drop-in changed)"
    timeout 30 systemctl restart systemd-resolved \
      || { journalctl -u systemd-resolved --no-pager -n 50 >&2
           _die "systemd-resolved restart failed or timed out (30s); see journal above"; }
    sleep 1
    systemctl is-active --quiet systemd-resolved \
      || { journalctl -u systemd-resolved --no-pager -n 50 >&2
           _die "systemd-resolved failed to restart; see journal above"; }
  fi

  # --- post-install probe matrix -----------------------------------
  # Fail the install rather than shipping a half-working DNS chain.
  #
  # Probes retry with backoff: a freshly-restarted pdns-recursor's first
  # recursive query can take several seconds (cold cache, root hint
  # fetch, upstream round-trip to 1.1.1.1). A single 3s shot is brittle
  # — legitimate cold starts were flunking the probe. 8 tries × 2s
  # backoff covers ~15s of startup cost while still failing loud if
  # the chain is genuinely broken.
  _probe_dns() {
    local addr="$1" host="$2" attempt
    for attempt in 1 2 3 4 5 6 7 8; do
      if dig +short +timeout=3 +tries=1 "@${addr}" "$host" 2>/dev/null | grep -qE '^[0-9.]+$'; then
        return 0
      fi
      sleep 2
    done
    return 1
  }

  # DNS forwarder mode: systemd-resolved is masked and /etc/resolv.conf
  # points straight at the operator-supplied JABALI_DNS_FORWARDER, so
  # the resolved→recursor→public chain doesn't exist and the probes
  # below would 100%-of-the-time false-positive. Recursor still serves
  # the panel-internal vhost lookups via forward-zones-recurse=$DNS_FORWARDER
  # set in install_pdns_recursor.
  if [[ -n "$DNS_FORWARDER" ]]; then
    _ok "pdns-recursor running on 127.0.0.1:53 — chain probes skipped (DNS forwarder ${DNS_FORWARDER} in effect)"
    return 0
  fi

  # Probe 1: stub → recursor → public. Proves the full chain end-to-end.
  if ! _probe_dns 127.0.0.53 deb.debian.org; then
    # GH #545 auto-heal: the recursor's public forward targets (1.1.1.1;9.9.9.9)
    # are unreachable from this host — common on locked-down VPS that block
    # outbound UDP 53 or only allow the provider's own resolver. Before aborting
    # (which forced a manual JABALI_DNS_FORWARDER + full re-run), look for a
    # resolver this box CAN reach and re-point the recursor's forward at it, then
    # re-probe. Happy-path installs pass Probe 1 above and never enter this branch.
    _hu_heal=""
    if _hu_heal="$(harvest_reachable_upstream)"; then
      _warn "public DNS unreachable from this host — re-pointing pdns-recursor to forward through ${_hu_heal} (GH #545 locked-down-VPS auto-heal)"
      if repoint_recursor_forward "$_hu_heal" && _probe_dns 127.0.0.53 deb.debian.org; then
        _ok "DNS chain recovered: recursor now forwards through ${_hu_heal}"
      else
        mark_dns_degraded "resolved->recursor->public chain broken and auto-heal via ${_hu_heal} did not recover it"
        return 0
      fi
    else
      mark_dns_degraded "resolved->recursor->public chain broken and no reachable upstream resolver was found to auto-heal with (this host cannot reach public DNS -- common on locked-down VPS that block outbound 53 or only allow the provider's own resolver)"
      return 0
    fi
  fi

  # Probe 2: recursor → public directly. Isolates recursor from stub.
  if ! _probe_dns 127.0.0.1 deb.debian.org; then
    mark_dns_degraded "recursor->public chain broken (dig @127.0.0.1 deb.debian.org failed after retries) -- the recursor cannot reach public DNS from this host"
    return 0
  fi

  # Probe 3: drop-in merge sanity (ADVISORY, non-fatal). resolvectl SHOULD
  # show the global DNS server as 127.0.0.1 only. If jabali.conf's
  # DNS=1.1.1.1 9.9.9.9 bleeds through, the man-page claim about DNS= reset
  # semantics doesn't hold on this system — but Probes 1+2 above ALREADY
  # proved the resolved->recursor->public chain works end-to-end via dig, so
  # this is a _warn, never a _die: a cosmetic resolvectl-format mismatch must
  # not abort an otherwise-healthy install. (It used to _die and bit real
  # hosts — per-link "DNS Servers:" line ordering, SIGPIPE, dormant D-Bus.)
  # The fallback (consolidate jabali.conf into zz-jabali-recursor.conf) stays
  # documented in the M6.3 plan for operators who hit actual self-resolution
  # problems.
  #
  # Requires D-Bus. On minimal LXC/OpenVZ VPS images dbus is installed
  # but dbus.socket is dormant — `resolvectl` then dies with
  # `sd_bus_open_system: No such file or directory` and our case-glob
  # below sees an empty string and false-dies. ensure_dbus activates
  # the bus when possible; when it can't (truly no dbus), Probes 1+2
  # already verified the chain end-to-end via dig, so we skip Probe 3
  # with a _warn and trust those.
  if ensure_dbus; then
    # The `|| true` suffixes below defend against a set -e + pipefail + SIGPIPE
    # interaction: awk's `exit` closes stdout before resolvectl finishes writing
    # its multi-line status block, resolvectl is SIGPIPE'd (exit 141), pipefail
    # surfaces 141, and — because these are bare assignments, not `local var=…`
    # which would mask the command-substitution exit — set -e kills the script
    # silently (no _die, no _ok, no trap output). Saw it on 192.168.100.150 with
    # systemd 257 / resolvectl ~258.3. The `|| true` keeps the assignment
    # happy; the subsequent `case` on $dns_servers is the real gate.
    local dns_servers
    # Read the GLOBAL merged resolver view deterministically. `resolvectl dns`
    # prints a single "Global:" line — that's the global scope. We do NOT key
    # off the first "DNS Servers:" line in `resolvectl status`, because that
    # output also lists per-interface views (e.g. "DNS Servers: 1.1.1.1 fe80::1")
    # and the first match isn't guaranteed to be the global one — the original
    # cause of spurious aborts on healthy hosts. `|| true` guards the set -e +
    # pipefail + SIGPIPE interaction (awk's exit closes stdout before resolvectl
    # finishes writing; saw exit 141 on systemd 257 / resolvectl ~258.3).
    dns_servers="$(resolvectl dns 2>/dev/null | awk '/^Global:/{$1=""; sub(/^[[:space:]]+/,""); print; exit}')" || true
    if [[ -z "$dns_servers" ]]; then
      # Fallback for systemd versions whose `resolvectl dns` omits Global:.
      dns_servers="$(resolvectl status 2>/dev/null | awk '/^[[:space:]]*Global/{f=1} f&&/DNS Servers:/{sub(/^.*DNS Servers:[[:space:]]*/,""); print; exit}')" || true
    fi
    # Expected: 127.0.0.1 only (the recursor), optionally repeated. Anything
    # else (1.1.1.1 / 9.9.9.9 / an interface upstream) means the
    # zz-jabali-recursor.conf reset didn't take globally — WARN, do not die:
    # Probes 1+2 already verified the chain functionally via dig.
    case "$dns_servers" in
      "127.0.0.1"|"127.0.0.1 127.0.0.1")
        _ok "pdns-recursor running on 127.0.0.1:53 — stub + recursor + public chain verified" ;;
      *)
        _warn "resolvectl global DNS='${dns_servers:-<unreadable>}' (expected '127.0.0.1'); the dig probes above already confirmed resolved->recursor->public works, so continuing. If panel-zone self-resolution misbehaves later, see plans/m6.3-pdns-recursor.md §Step 2 fallback (consolidate jabali.conf into zz-jabali-recursor.conf; panel UI edits FallbackDNS= instead of DNS=)."
        _ok "pdns-recursor running on 127.0.0.1:53 — dig chain verified (resolvectl global view differs; warned, not fatal)" ;;
    esac
  else
    _warn "skipping resolvectl drop-in merge check (no D-Bus); chain verified via dig only"
    _ok "pdns-recursor running on 127.0.0.1:53 — dig probes passed (resolvectl skipped)"
  fi
}

# ---------- step 2.6b: bootstrap the panel's own hostname zone --------------
#
# User domains created via the panel declare ns1.<hostname> / ns2.<hostname>
# as their authoritative nameservers. For anyone delegating to those NS
# records to actually reach our PowerDNS, PowerDNS must be authoritative
# for <hostname> itself — otherwise `host ns1.<hostname>` returns REFUSED
# and the whole DNS infrastructure is broken from day one.
#
# We create the zone exactly once at install time with the minimum record
# set PowerDNS needs to serve itself: SOA, NS×2, A for the hostname, and
# A for each NS name. On subsequent install.sh runs the zone is left
# alone — an admin may have edited it via the panel UI and re-installing
# shouldn't clobber their customizations. To refresh defaults, delete the
# zone manually and re-run install.sh.
#
# We use direct SQL INSERTs rather than a `jabali` CLI call because this
# phase runs before build_backend — there's no jabali binary yet. The
# PDNS schema has been stable for years; the column set here matches
# what panel-agent/internal/pdns/client.go upserts for user domains.
bootstrap_pdns_self_zone() {
  local hostname="$JABALI_SRV_HOSTNAME"
  local ipv4="$JABALI_SRV_IPV4"
  local ipv6="${JABALI_SRV_IPV6:-}"
  local ns1_name="$JABALI_SRV_NS1_NAME"
  local ns1_ipv4="$JABALI_SRV_NS1_IPV4"
  local ns2_name="$JABALI_SRV_NS2_NAME"
  local ns2_ipv4="$JABALI_SRV_NS2_IPV4"

  if [[ -z "$hostname" || -z "$ipv4" || -z "$ns1_name" || -z "$ns2_name" ]]; then
    _warn "bootstrap_pdns_self_zone: server settings env vars missing; skipping"
    return 0
  fi

  # Sanity-warn (don't fail) on non-routable identities. An admin running
  # a lab/dev install with hostname=jabali-panel.local gets a working
  # PDNS but the NS delegation will only work on hosts that explicitly
  # resolve through this PDNS — it won't work from public resolvers.
  case "$hostname" in
    *.local|*.localdomain|localhost)
      _warn "hostname '$hostname' ends in a non-routable TLD — public NS delegation will not work"
      ;;
  esac
  if [[ "$ipv4" =~ ^(10\.|172\.(1[6-9]|2[0-9]|3[01])\.|192\.168\.|127\.) ]]; then
    _warn "IPv4 '$ipv4' is a private/loopback range — public NS delegation will not reach this host"
  fi

  # Idempotent check: if the domain row exists, leave everything alone.
  local existing_id
  existing_id="$(mariadb -uroot -Ns jabali_pdns -e \
    "SELECT id FROM domains WHERE name = '$(_sql_escape "$hostname")';" 2>/dev/null || true)"
  if [[ -n "$existing_id" ]]; then
    _log "self-zone '$hostname' already exists in jabali_pdns (id=$existing_id); leaving untouched"
    return 0
  fi

  _log "bootstrapping PowerDNS self-zone '$hostname' (SOA + NS + A × 3${ipv6:+ + AAAA × 3})"

  # Build the SQL as a heredoc. We can't interpolate arbitrary admin
  # input directly into SQL without escaping, but these values came from
  # prompt_server_settings which validates them as RFC-1123 hostnames /
  # IP addresses. Still, run each through _sql_escape as defense in depth.
  local h_esc ipv4_esc ns1_esc ns1_ipv4_esc ns2_esc ns2_ipv4_esc ipv6_esc
  h_esc="$(_sql_escape "$hostname")"
  ipv4_esc="$(_sql_escape "$ipv4")"
  ns1_esc="$(_sql_escape "$ns1_name")"
  ns1_ipv4_esc="$(_sql_escape "$ns1_ipv4")"
  ns2_esc="$(_sql_escape "$ns2_name")"
  ns2_ipv4_esc="$(_sql_escape "$ns2_ipv4")"
  ipv6_esc="$(_sql_escape "$ipv6")"

  # SOA content: primary-ns hostmaster.<hostname> serial refresh retry expire minimum
  # Matches RFC 1035 SOA RDATA; 300s min TTL for faster negative caching recovery.
  local soa_content="$ns1_esc hostmaster.$h_esc 1 86400 7200 604800 300"

  mariadb -uroot jabali_pdns <<SQL
INSERT INTO domains (name, type) VALUES ('$h_esc', 'NATIVE');
SET @zid = LAST_INSERT_ID();
INSERT INTO records (domain_id, name, type, content, ttl, prio, disabled, auth) VALUES
  (@zid, '$h_esc',     'SOA', '$soa_content', 3600, 0, 0, 1),
  (@zid, '$h_esc',     'NS',  '$ns1_esc',     3600, 0, 0, 1),
  (@zid, '$h_esc',     'NS',  '$ns2_esc',     3600, 0, 0, 1),
  (@zid, '$h_esc',     'A',   '$ipv4_esc',    300,  0, 0, 1),
  (@zid, '$ns1_esc',   'A',   '$ns1_ipv4_esc',300,  0, 0, 1),
  (@zid, '$ns2_esc',   'A',   '$ns2_ipv4_esc',300,  0, 0, 1);
SQL

  # AAAA records only if IPv6 is configured. Separate statement so the
  # common IPv4-only case doesn't pay for a conditional in the heredoc.
  if [[ -n "$ipv6" ]]; then
    mariadb -uroot jabali_pdns <<SQL
SET @zid = (SELECT id FROM domains WHERE name = '$h_esc');
INSERT INTO records (domain_id, name, type, content, ttl, prio, disabled, auth) VALUES
  (@zid, '$h_esc',   'AAAA', '$ipv6_esc', 300, 0, 0, 1),
  (@zid, '$ns1_esc', 'AAAA', '$ipv6_esc', 300, 0, 0, 1),
  (@zid, '$ns2_esc', 'AAAA', '$ipv6_esc', 300, 0, 0, 1);
SQL
  fi

  # Tell pdns to drop its cache for this zone so subsequent queries see
  # the new records immediately. NOTIFY also pings any configured slaves;
  # with type=NATIVE and no slaves configured, this is a pure cache poke.
  # Ignore exit — pdns_control may not be on PATH on minimal Debian
  # installs, and the SQL rows are committed either way; the next
  # scheduled reload (or pdns restart) will pick them up.
  pdns_control notify "$hostname" >/dev/null 2>&1 || true

  _ok "self-zone '$hostname' created in jabali_pdns"
}

# install_panel_primary_domain auto-registers the panel hostname as a
# first-class email-enabled domain so `/webmail` bounces to a working
# Bulwark instance on fresh installs and `admin@<hostname>` is a viable
# mailbox. The real work — idempotent INSERT/UPDATE/no-op decision tree,
# ULID generation, at-most-one enforcement — is in the Go CLI
# (`jabali-panel panel-primary ensure`). install.sh only has to:
#   1. Verify hostname is set (fatal if not — no working mail without it).
#   2. Verify the pdns self-zone exists (FK assertion — reconciler later
#      writes DNS records scoped to this zone).
#   3. Invoke the CLI.
#
# See ADR-0048 for the design rationale and plans/m6.4-panel-hostname-
# mail-domain.md Step 3 for the full task list.
install_panel_primary_domain() {
  if [[ -z "${JABALI_SRV_HOSTNAME:-}" ]]; then
    _die "JABALI_SRV_HOSTNAME not set — cannot configure panel primary mail domain"
  fi

  # Self-zone FK assertion. `install_panel_primary_domain` inserts rows
  # into jabali_panel.domains, and the reconciler will later insert DNS
  # records into jabali_pdns that reference a zone keyed by hostname.
  # If bootstrap_pdns_self_zone hasn't run, those later inserts fail at
  # reconciler tick time with FK violations. Catch it here, not there.
  local pdns_zone_id
  pdns_zone_id="$(mariadb -uroot -Ns jabali_pdns -e \
    "SELECT id FROM domains WHERE name = '$(_sql_escape "$JABALI_SRV_HOSTNAME")';" 2>/dev/null || true)"
  if [[ -z "$pdns_zone_id" ]]; then
    _die "pdns self-zone '$JABALI_SRV_HOSTNAME' not found — bootstrap_pdns_self_zone must run before install_panel_primary_domain; check main() ordering"
  fi

  _log "ensuring panel-primary domain row for $JABALI_SRV_HOSTNAME"
  if "$BIN_PATH" panel-primary ensure --hostname "$JABALI_SRV_HOSTNAME"; then
    _ok "panel-primary domain ensured for $JABALI_SRV_HOSTNAME"
  else
    # Non-fatal — the CLI may defer when no admin user exists yet. That
    # message is already logged by the CLI. On next install.sh run (e.g.
    # after the operator completes admin bootstrap), the CLI will INSERT
    # the row. A hard failure (DB down, config missing) would have
    # returned non-zero; we do NOT _die because defer-on-no-admin is
    # a valid path.
    _warn "panel-primary ensure reported non-success; review output above"
  fi
}

# Minimal SQL string escaper: replaces ' with '' and strips backslashes
# that MariaDB would otherwise interpret in string literals. Not a
# general-purpose escaper — adequate for hostname / IPv4 / IPv6 values
# that have already passed RFC-1123 / netip.ParseAddr validation earlier
# in prompt_server_settings. Defense in depth, not primary trust.
_sql_escape() {
  # shellcheck disable=SC2001
  printf '%s' "$1" | sed -e "s/'/''/g" -e 's/\\//g'
}

# ---------- step 2.7: Certbot (Let's Encrypt SSL) ---------------------------
setup_certbot() {
  _log "configuring Certbot (packages installed in base batch; this runs post-install config)"

  # certbot + python3-certbot-nginx are installed in install_base_packages's
  # one-shot apt batch. This function owns the letsencrypt directory
  # layout the agent + nginx both expect.
  if ! command -v certbot &>/dev/null; then
    _die "certbot binary not found — install_base_packages should have installed it"
  fi

  local version
  version="$(certbot --version 2>/dev/null | head -n1)"
  _ok "Certbot present: $version"

  # Pre-create the letsencrypt directories with correct ownership.
  # The panel-agent will write certificates here; nginx may also read them.
  mkdir -p /etc/letsencrypt/{archive,live,renewal}
  chmod 0755 /etc/letsencrypt
  chmod 0755 /etc/letsencrypt/{archive,live,renewal}

  _ok "Certbot ready for SSL certificate management"
}

# ---------- step 2: Go toolchain --------------------------------------------

install_go() {
  if [[ -x "$GO_ROOT/bin/go" ]]; then
    # `go version` can fail silently on a half-installed or libc-mismatched
    # binary (observed on mx.jabali-panel.local 2026-05-12: existing
    # /usr/local/go/bin/go was -x but `go version` exited non-zero with
    # empty stdout, killing install.sh via `set -e` + pipefail at the
    # command substitution below). Run the version probe with `|| true`
    # AND short-circuit on empty result so we always reach the reinstall
    # path instead of bailing the whole script.
    local cur=""
    cur="$("$GO_ROOT/bin/go" version 2>/dev/null | awk '{print $3}' || true)"
    if [[ -n "$cur" && "$cur" == "go$GO_VERSION" ]]; then
      _ok "Go $GO_VERSION already installed at $GO_ROOT"
      return
    fi
    if [[ -n "$cur" ]]; then
      _log "replacing existing Go ($cur) with $GO_VERSION"
    else
      _warn "$GO_ROOT/bin/go present but 'go version' empty/failed — reinstalling"
    fi
    rm -rf "$GO_ROOT"
  fi

  _log "installing Go $GO_VERSION ($GO_ARCH)"
  local tarball="/tmp/go${GO_VERSION}.linux-${GO_ARCH}.tar.gz"
  local go_curl=(curl -fsSL --connect-timeout 20 --retry 3 --retry-delay 5 --retry-connrefused --speed-limit 1024 --speed-time 30)
  if ! "${go_curl[@]}" -o "$tarball" "https://go.dev/dl/go${GO_VERSION}.linux-${GO_ARCH}.tar.gz"; then
    # GH #670: the pinned tarball can 404 on a given host even though go.dev
    # LISTS the version -- a regional dl.google.com CDN-propagation gap for a
    # freshly-cut release. The earlier fallback resolved the "latest published
    # stable", but for a pin-to-latest that IS $GO_VERSION, so it retried the
    # identical failing URL and died. Instead, walk the published stable list
    # (newest first) and take the FIRST version that (a) differs from the one
    # that just failed and (b) actually downloads. go.mod needs only go 1.25.0,
    # so an older published stable still builds the panel.
    local failed_pin="$GO_VERSION" _ver _got=""
    _warn "Go $failed_pin not downloadable from go.dev (unpublished pin or CDN propagation gap) -- trying other published stable releases"
    # mode=json lists only releases whose files are actually published -- unlike
    # go.dev/VERSION, which reports a version the instant it is tagged, before its
    # tarballs exist (the original GH #670 regression).
    local _go_list
    _go_list="$("${go_curl[@]}" "https://go.dev/dl/?mode=json" 2>/dev/null \
      | grep -oE '"version"[[:space:]]*:[[:space:]]*"go[0-9.]+"' \
      | grep -oE 'go[0-9.]+' | sed 's/^go//' | awk '!seen[$0]++')"
    for _ver in $_go_list; do
      [[ "$_ver" == "$failed_pin" ]] && continue
      _log "trying published stable go${_ver}"
      tarball="/tmp/go${_ver}.linux-${GO_ARCH}.tar.gz"
      if "${go_curl[@]}" -o "$tarball" "https://go.dev/dl/go${_ver}.linux-${GO_ARCH}.tar.gz"; then
        GO_VERSION="$_ver"; _got=1; break
      fi
    done
    [[ -n "$_got" ]] || _die "failed to download Go: pinned go${failed_pin} and every published stable fallback 404'd from go.dev -- check egress to go.dev / dl.google.com from this host"
    # A downgrade is a security-relevant event, not a detail: the host is now
    # building the panel with an older toolchain that may carry known CVEs, and
    # simply making the pinned URL fail is enough to trigger it. Say so loudly.
    _warn "GO TOOLCHAIN DOWNGRADE: pinned go${failed_pin} was unavailable; installing go${GO_VERSION} instead. The panel/agent binaries will be built with this older toolchain — re-run once go${failed_pin} is reachable."
  fi
  # Verify BEFORE extracting into /usr/local. The tarball builds the panel and
  # agent binaries, so a tampered toolchain compromises every artifact this
  # host produces — and nothing downstream would notice. Prefer the in-repo
  # pin (protects even against a compromised go.dev); fall back to go.dev's own
  # published sha256 for a version the pin can't know in advance (the CDN-gap
  # fallback, or a JABALI_GO_VERSION override). Refuse to install unverified.
  local go_tar_name go_expected go_actual
  go_tar_name="$(basename "$tarball")"
  go_expected=""
  # Only the PINNED version has a checksum baked in; the fallback path lands on
  # a version chosen at runtime, so it verifies against go.dev instead.
  if [[ "$go_tar_name" == "go${GO_VERSION}.linux-${GO_ARCH}.tar.gz" ]]; then
    case "$GO_ARCH" in
      amd64) go_expected="$GO_SHA256_AMD64" ;;
      arm64) go_expected="$GO_SHA256_ARM64" ;;
    esac
  fi
  if [[ -z "$go_expected" ]]; then
    _log "no pinned checksum for ${go_tar_name} — verifying against go.dev's published checksum"
    # go.dev's listing is pretty-printed with "sha256" a few lines below the
    # matching "filename", so take the first sha256 in that window.
    go_expected="$("${go_curl[@]}" "https://go.dev/dl/?mode=json" 2>/dev/null \
      | grep -A 5 "\"filename\": \"${go_tar_name}\"" \
      | grep -oE '"sha256": "[a-f0-9]{64}"' \
      | grep -oE '[a-f0-9]{64}' | head -1)"
  fi
  go_actual="$(sha256sum "$tarball" | awk '{print $1}')"
  if [[ -z "$go_expected" ]]; then
    rm -f "$tarball"
    _die "could not determine an expected checksum for ${go_tar_name} — refusing to install an unverified Go toolchain"
  fi
  if [[ "$go_expected" != "$go_actual" ]]; then
    rm -f "$tarball"
    _die "Go toolchain checksum mismatch for ${go_tar_name}: expected $go_expected, got $go_actual — NOT extracting"
  fi
  _ok "Go toolchain checksum verified (${go_tar_name})"
  tar -C /usr/local -xzf "$tarball"
  rm -f "$tarball"

  # Make `go` available for interactive shells.
  cat >/etc/profile.d/jabali-go.sh <<'EOF'
export PATH="/usr/local/go/bin:$PATH"
EOF
  chmod 0644 /etc/profile.d/jabali-go.sh

  _ok "Go installed: $("$GO_ROOT/bin/go" version)"
}

# ---------- step 3: service user + dirs -------------------------------------

ensure_user_and_dirs() {
  if ! id "$SERVICE_USER" >/dev/null 2>&1; then
    _log "creating system user '$SERVICE_USER'"
    useradd --system --home-dir "$REPO_DIR" --shell /usr/sbin/nologin --groups www-data \
      --comment "Jabali Panel service user" "$SERVICE_USER" \
      || _die "useradd $SERVICE_USER failed — check 'cat /etc/passwd | grep $SERVICE_USER' and 'getent group www-data'"
    # Defence-in-depth: confirm the user actually landed. Some host
    # NSS configs (LDAP-backed passwd) accept useradd but the local
    # /etc/passwd row never persists; downstream sudo -u jabali fails.
    id "$SERVICE_USER" >/dev/null 2>&1 \
      || _die "$SERVICE_USER missing after useradd — check NSS chain in /etc/nsswitch.conf"
  else
    _ok "user '$SERVICE_USER' exists"
    # Ensure service user is in www-data group so it can stat
    # per-user FPM sockets under /run/php/jabali-<user>/ (mode 0750).
    usermod -aG www-data "$SERVICE_USER" 2>/dev/null || true
  fi

  # systemd-journal group lets the panel-api ssh.login event source tail
  # the sshd journal without elevating to root. Group exists on every
  # systemd distro; ignore failure on the rare init that doesn't ship it.
  if getent group systemd-journal >/dev/null 2>&1; then
    usermod -aG systemd-journal "$SERVICE_USER" 2>/dev/null || true
  fi

  # adm group lets the log-streaming WS handler tail the system-wide
  # nginx logs (/var/log/nginx/{access,error}.log are mode 0640
  # www-data:adm). Per-domain logs are 0644 root:jabali so this is
  # only needed for the "All Domains (system)" stream variant.
  if getent group adm >/dev/null 2>&1; then
    usermod -aG adm "$SERVICE_USER" 2>/dev/null || true
  fi

  # mysql group: Kratos + panel-api connect to MariaDB via
  # /run/mysqld/mysqld.sock (M25.1 unix-socket lockdown). On Ubuntu
  # the socket is 0660 mysql:mysql by default — Debian relaxes it to
  # 0777 so this isn't strictly needed there, but adding the
  # supplementary group is the only thing that lets Ubuntu hosts
  # connect at all. jabali-kratos.service already lists
  # SupplementaryGroups=mysql; this usermod is belt-and-braces for
  # any future caller (psql/pgloader/migration tooling) that runs
  # as the bare jabali user outside a systemd unit.
  if getent group mysql >/dev/null 2>&1; then
    usermod -aG mysql "$SERVICE_USER" 2>/dev/null || true
  fi

  install -d -m 0755 -o "$SERVICE_USER" -g "$SERVICE_USER" "$REPO_DIR"
  install -d -m 0750 -o "$SERVICE_USER" -g "$SERVICE_USER" "$(dirname "$ENV_FILE")"
  # Working folder root (server_settings.working_folder default).
  # Migration staging + backup repo subdirs live underneath; admin can
  # retarget via Settings → Storage after install.
  install -d -m 0755 -o "$SERVICE_USER" -g "$SERVICE_USER" /var/lib/jabali
  install -d -m 0750 -o "$SERVICE_USER" -g "$SERVICE_USER" /var/lib/jabali/migrations
  install -d -m 0700 -o "$SERVICE_USER" -g "$SERVICE_USER" /var/lib/jabali/backups
  install -d -m 0700 -o "$SERVICE_USER" -g "$SERVICE_USER" /var/lib/jabali/restore
  # M28 — operator-uploaded panel logos. Owned by the service user so
  # the POST /admin/settings/branding/logo handler can mkdir + atomic
  # rename on upload. 0755 so nginx (proxied GET falls back to panel-
  # api anyway, but keep it world-readable for future direct serving).
  install -d -m 0755 -o "$SERVICE_USER" -g "$SERVICE_USER" /var/lib/jabali-panel
  install -d -m 0755 -o "$SERVICE_USER" -g "$SERVICE_USER" /var/lib/jabali-panel/branding
  # GH #1145 — parent of every per-subaccount SFTP/FTPS isolation jail. Must be
  # root:root and NOT group/other-writable so it satisfies sshd's root-owned-
  # chroot-chain rule and vsftpd's secure-chroot rule; the agent creates the
  # per-account jail (<tenant>/<alias>/) beneath it on isolated-account create.
  # Declared here (not gated on the ftp/vsftpd module) because SFTP isolation
  # works without vsftpd. sshd/vsftpd are unconfined by AppArmor, so no profile
  # rule is needed for the jail path.
  install -d -m 0755 -o root -g root /var/lib/jabali-ftp-jails
  # M35 — migration importers. Legacy path /var/lib/jabali-migrations
  # is kept as a symlink to the new working_folder/migrations subdir so
  # existing callsites that hardcode the old path keep working. Real
  # storage lives under <working_folder>/migrations.
  if [ ! -L /var/lib/jabali-migrations ] && [ ! -e /var/lib/jabali-migrations ]; then
    ln -s /var/lib/jabali/migrations /var/lib/jabali-migrations
  elif [ ! -L /var/lib/jabali-migrations ] && [ -d /var/lib/jabali-migrations ]; then
    # Pre-existing real dir from older installs — keep it in place; the
    # working_folder helper resolves to legacy when admin hasn't changed
    # the default. Operator may rsync + relink manually.
    :
  fi
  # Legacy backup path symlink — same treatment so M30 callsites that
  # hardcode /var/lib/jabali-backups keep working.
  if [ ! -L /var/lib/jabali-backups ] && [ ! -e /var/lib/jabali-backups ]; then
    ln -s /var/lib/jabali/backups /var/lib/jabali-backups
  fi
  # M35 ADR-0094 §"tracked risks": per-job source credentials live at
  # /etc/jabali-panel/migration-secrets/<job-id>.env (root:jabali 0640).
  # Wiped on job terminal state by the future-shipped reaper. Mode 0750
  # on the parent dir prevents other users discovering the file list;
  # files themselves are 0640 root:jabali so only the panel can read.
  install -d -m 0750 -o root -g "$SERVICE_USER" /etc/jabali-panel/migration-secrets
}

# ---------- M25 step 1: jabali-sockets group --------------------------------
#
# `jabali-sockets` is the cross-service group that gates connect(2) on every
# Unix-domain backend socket M25 introduces (Kratos admin, Kratos public,
# panel-api, Bulwark webmail). nginx (running as www-data) is a member so it
# can reach those sockets; the panel and webmail service users are members so
# the sockets they create end up in the right group.
#
# `jabali-mail` is intentionally NOT a member. Stalwart talks to its own ports
# (SMTP, IMAP, JMAP) — it doesn't consume our internal HTTP sockets, and the
# group should only carry users that genuinely need socket reach. See
# install/scripts/socket-helpers.sh for the RuntimeDirectory + ExecStartPost
# pattern Steps 2–5 layer on top.
#
# Idempotent: re-running on an existing install is a no-op (group already
# exists; usermod -aG silently no-ops when the user is already a member).
# Members not yet created (e.g. jabali-webmail before install_bulwark) are
# skipped this pass; the function is called again later — see main().
ensure_jabali_sockets_group() {
  if ! getent group jabali-sockets >/dev/null 2>&1; then
    _log "creating jabali-sockets system group"
    groupadd --system jabali-sockets
    _ok "jabali-sockets group created"
  fi

  local user added=0
  for user in "$SERVICE_USER" www-data jabali-webmail; do
    if ! getent passwd "$user" >/dev/null 2>&1; then
      # User not provisioned yet — a later main()-flow call picks it up.
      continue
    fi
    if id -nG "$user" | tr ' ' '\n' | grep -qx jabali-sockets; then
      continue
    fi
    usermod -aG jabali-sockets "$user"
    _ok "added $user to jabali-sockets"
    added=$((added + 1))
  done

  if (( added == 0 )); then
    _log "jabali-sockets membership already current"
  fi
}

# ---------- M25 step 1: LLMNR disable ---------------------------------------
#
# LLMNR (Link-Local Multicast Name Resolution) listens on UDP+TCP :5355 and
# is enabled by default on systemd-resolved. We don't use it — datacenter
# DNS resolution flows through pdns-recursor (M6.3) — and it's another
# unauthenticated wire-protocol surface on every interface. Disable via a
# drop-in so operators on LAN-heavy environments can override by writing
# a higher-numbered drop-in (e.g. 99-operator-keep-llmnr.conf).
disable_llmnr() {
  local conf=/etc/systemd/resolved.conf.d/10-jabali-disable-llmnr.conf
  install -d -m 0755 /etc/systemd/resolved.conf.d
  cat >"$conf" <<'EOF'
# Managed by jabali install.sh (M25). Override by adding a higher-numbered
# drop-in like /etc/systemd/resolved.conf.d/99-operator-keep-llmnr.conf
# with [Resolve]\nLLMNR=yes\n if you genuinely need LLMNR on this host.
[Resolve]
LLMNR=no
EOF
  chmod 0644 "$conf"
  systemctl reload-or-restart systemd-resolved 2>/dev/null \
    || _warn "systemd-resolved reload failed; LLMNR config will take effect on next restart"
  _ok "LLMNR disabled via $conf"
}

# ---------- step 4: clone / update repo -------------------------------------

clone_or_update_repo() {
  # Hard gate: sudo must be installed before we reach for sudo -u jabali.
  # install_base_packages adds it to the apt batch, but a minimal LXC /
  # docker image without sudo + a half-completed install batch can land
  # us here without the binary. Surface a clear error instead of the
  # opaque "git clone failed (check connectivity, cert trust, ...)"
  # message _die emits later.
  if ! command -v sudo >/dev/null 2>&1; then
    _log "sudo not found — installing on demand"
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends sudo \
      || _die "sudo missing and apt-get install sudo failed — install sudo manually + re-run"
  fi

  # Re-verify DNS before reaching for a git remote. Earlier steps in the
  # install (ufw activate, systemd-resolved restart during install_kratos'
  # config flip, crowdsec profile reload) have been observed to drop the
  # recursor → public chain on fresh installs — git clone then SERVFAILs
  # with "Could not resolve host" and under `set -e` aborts silently.
  # Probe the full chain one more time with the same 8×2s retry logic as
  # the post-recursor-install probe so transient restarts don't brick the
  # install.
  local _clone_dns_ok=0
  local attempt
  for attempt in 1 2 3 4 5 6 7 8; do
    if getent hosts "${REPO_HOST:-github.com}" >/dev/null 2>&1; then
      _clone_dns_ok=1
      break
    fi
    sleep 2
  done
  if [[ "$_clone_dns_ok" != "1" ]]; then
    _warn "DNS not resolving for the git remote host — restarting pdns-recursor + systemd-resolved and retrying"
    systemctl restart pdns-recursor 2>/dev/null || true
    sleep 1
    systemctl restart systemd-resolved 2>/dev/null || true
    sleep 2
    if ! getent hosts "${REPO_HOST:-github.com}" >/dev/null 2>&1; then
      _die "cannot resolve $REPO_URL — check 'systemctl status pdns-recursor systemd-resolved' and 'dig @127.0.0.1 <host>'"
    fi
  fi

  # For both clone and fetch, pass the token via a transient credential
  # helper instead of baking it into the saved remote URL. That keeps
  # `git remote -v` and `.git/config` free of secrets.
  local git_args=()
  if [[ -n "$REPO_TOKEN" ]]; then
    # shellcheck disable=SC2016
    git_args+=(
      -c "credential.helper="
      -c "credential.helper=!f() { echo username=oauth2; echo password=$REPO_TOKEN; }; f"
    )
  fi

  if [[ -d "$REPO_DIR/.git" ]]; then
    _log "pulling latest $REPO_BRANCH into $REPO_DIR"
    # Self-heal for a classic footgun: operator (or a prior debug
    # session) ran `git fetch`/`git pull` as root inside the repo,
    # silently re-chowning .git/FETCH_HEAD and friends to root. The
    # next install.sh fetch (run as $SERVICE_USER) then dies with
    # "cannot open '.git/FETCH_HEAD': Permission denied". Mirror the
    # fix already in panel-api update.go — re-chown the .git dir
    # before pulling so the install self-heals instead of leaving a
    # half-installed host needing a magic chown from the operator.
    # Scope intentionally narrow: just .git/, so node_modules or
    # other trees that may legitimately be group-owned differently
    # don't get clobbered.
    chown -R "$SERVICE_USER:$SERVICE_USER" "$REPO_DIR/.git"
    # Self-heal a stale remote URL. Boxes provisioned before the codeberg→GitHub
    # source-of-truth switch still have origin pointing at the DROPPED codeberg
    # remote, which is frozen: `git fetch origin` there silently pulls
    # long-obsolete code (e.g. missing install/hostname/* and other newer
    # files), then a later install step hard-fails on a file the current
    # install.sh references. Force origin to the canonical $REPO_URL before
    # fetching so the pull always tracks the real source. Idempotent — a no-op
    # when origin is already correct.
    sudo -u "$SERVICE_USER" -H git -C "$REPO_DIR" remote set-url origin "$REPO_URL" 2>/dev/null \
      || _warn "could not reset origin URL to $REPO_URL — continuing with the existing remote"
    # No --quiet: under `set -e` a failed fetch/clone aborts install.sh
    # without any output because --quiet suppresses git's stderr, leaving
    # the operator with a silent exit. Let git's error reach the trace.
    sudo -u "$SERVICE_USER" -H git "${git_args[@]}" -C "$REPO_DIR" fetch origin "$REPO_BRANCH" \
      || _die "git fetch origin $REPO_BRANCH failed (run manually as $SERVICE_USER to see full error)"
    sudo -u "$SERVICE_USER" -H git -C "$REPO_DIR" reset --hard "origin/$REPO_BRANCH" \
      || _die "git reset --hard origin/$REPO_BRANCH failed"
  else
    _log "cloning $REPO_URL into $REPO_DIR"
    sudo -u "$SERVICE_USER" -H git "${git_args[@]}" clone --branch "$REPO_BRANCH" \
      "$REPO_URL" "$REPO_DIR" \
      || _die "git clone $REPO_URL failed (check connectivity, cert trust, and that $REPO_DIR is writable by $SERVICE_USER)"
  fi
  _ok "repo at $(sudo -u "$SERVICE_USER" -H git -C "$REPO_DIR" rev-parse --short HEAD)"
}

protect_panel_docs() {
  # Claude Code / AI-assistant config files (AGENTS.md, CLAUDE.md, .claude/)
  # contain system architecture and agent orchestration rules. The repo clone
  # is owned by the jabali service user (jabali:jabali 0644 by default), so
  # any PHP webshell or compromised service user can read them. Restrict to
  # root:root so neither the service user nor tenant PHP pools can access them.
  for node in AGENTS.md CLAUDE.md .claude; do
    local p="$REPO_DIR/$node"
    [[ -e "$p" ]] || continue
    chown -R root:root "$p"
    if [[ -d "$p" ]]; then
      find "$p" -type f -exec chmod 0600 {} \;
      find "$p" -type d -exec chmod 0700 {} \;
    else
      chmod 0600 "$p"
    fi
  done
}

# ---------- step 5: build backend -------------------------------------------

# ---------- step 5a: build React SPA -----------------------------------

ensure_swap() {
  # Frontend build (vite + node) peaks at ~1.5 GB resident. On a low-RAM
  # host without swap the OOM killer fires mid-build, leaving the install
  # half-complete + the operator with a cryptic 'Killed' from npm. Add a
  # 2 GB swap file ONLY when the host has ≤2 GB RAM and <2 GB swap is
  # active. Idempotent: re-runs skip when swap is sufficient.
  local mem_kb want_swap_kb cur_swap_kb mem_mb
  mem_kb=$(awk '/^MemTotal:/ {print $2}' /proc/meminfo 2>/dev/null || echo 0)
  cur_swap_kb=$(awk '/^SwapTotal:/ {print $2}' /proc/meminfo 2>/dev/null || echo 0)
  mem_mb=$((mem_kb / 1024))
  # Threshold widened 2026-06-05 from 2 GB -> 4 GB. A 3.8 GB-RAM
  # VPS (puzzle.linux-hosting.net) had no swap and the kernel
  # OOM-killed mariadb twice (NRestarts=2 in systemd) -- mariadbd was
  # the biggest RSS so it won the lottery. Below ~4 GB the panel +
  # mail stack + a few tenants pushes the box into kernel pressure;
  # swap gives a safety net before OOM fires.
  if [[ $mem_mb -gt 4096 ]]; then
    _log "build-swap: ${mem_mb}MB RAM > 4GB, no swap needed"
    return 0
  fi
  want_swap_kb=2097152  # 2 GB
  if [[ $cur_swap_kb -ge $want_swap_kb ]]; then
    _log "build-swap: ${cur_swap_kb}kB swap already active, sufficient"
    return 0
  fi
  local swap_file=/var/swap.jabali
  _log "build-swap: provisioning 2 GB swap at $swap_file (this can take 30-60s on slow disks)"
  # Clean up any half-baked prior attempt before re-creating.
  if [[ -e "$swap_file" ]]; then
    swapoff "$swap_file" 2>/dev/null || true
    rm -f "$swap_file"
  fi
  if ! fallocate -l 2G "$swap_file" 2>/dev/null; then
    # fallocate fails on tmpfs / unsupported FS — fall back to dd.
    dd if=/dev/zero of="$swap_file" bs=1M count=2048 status=none \
      || { _warn "swap provision failed — vite build may OOM on this host"; return 0; }
  fi
  chmod 0600 "$swap_file"
  mkswap "$swap_file" >/dev/null 2>&1 \
    || { _warn "mkswap on $swap_file failed — vite build may OOM on this host"; rm -f "$swap_file"; return 0; }
  swapon "$swap_file" \
    || { _warn "swapon $swap_file failed — vite build may OOM on this host"; rm -f "$swap_file"; return 0; }
  # /etc/fstab entry so swap survives reboot.
  if ! grep -qF "$swap_file" /etc/fstab 2>/dev/null; then
    echo "$swap_file none swap sw 0 0" >> /etc/fstab
  fi
  # vm.swappiness=10 — only swap when really under pressure. Default 60
  # is too aggressive on a low-RAM VPS: kernel pages anonymous memory
  # out while file cache is still warm, causing the panel to stall on
  # paging-in mid-request. 10 keeps the swap file as an OOM safety net
  # without trading interactive latency for it.
  echo 'vm.swappiness=10' > /etc/sysctl.d/99-jabali-swappiness.conf
  sysctl -w vm.swappiness=10 >/dev/null 2>&1 || true
  _ok "build-swap: 2 GB active at $swap_file (persists via /etc/fstab); vm.swappiness=10"
}

# ensure_fleet_swap — JAB-273. Distinct from the build-time ensure_swap above,
# which only helps ≤4 GB build hosts. newaramaapp (18 GB RAM, 0 swap) wedged
# into a kswapd death-spiral: once RAM filled on a swapless box the kernel had
# nowhere to evict to, load hit ~385 on 8 cores, and sshd could not even
# complete a login handshake — the operator was locked out mid-incident. Swap
# gives kswapd an exit so a memory spike degrades gracefully instead of
# wedging. Runs on EVERY box (any RAM size), on fresh install AND
# `jabali update`, so the existing fleet self-heals.
#
# Conservative + idempotent:
#   - Acts only when active swap < 2 GB — never resizes or swapoffs an
#     operator's existing swap on a live box.
#   - Sizes min(RAM, 8 GB), then halves until it fits behind a 10 GB free-disk
#     margin (floor 1 GB); warns + skips if it still will not fit.
#   - Reuses ensure_swap's mechanics: fallocate→dd fallback, 0600 before
#     mkswap, rm-on-any-failure (no half state), grep -qF fstab idempotence,
#     and swapon-failure tolerance as the container/btrfs guard.
ensure_fleet_swap() {
  local mem_kb cur_swap_kb mem_mb want_mb free_mb
  mem_kb=$(awk '/^MemTotal:/ {print $2}' /proc/meminfo 2>/dev/null || echo 0)
  cur_swap_kb=$(awk '/^SwapTotal:/ {print $2}' /proc/meminfo 2>/dev/null || echo 0)
  mem_mb=$((mem_kb / 1024))
  if [[ $cur_swap_kb -ge 2097152 ]]; then
    _log "fleet-swap: $((cur_swap_kb / 1024))MB swap already active — skip (JAB-273)"
    return 0
  fi
  want_mb=8192
  [[ $mem_mb -gt 0 && $mem_mb -lt $want_mb ]] && want_mb=$mem_mb
  free_mb=$(df -Pm /var 2>/dev/null | awk 'NR==2 {print $4}' || echo 0)
  while [[ $want_mb -ge 1024 && $((free_mb - want_mb)) -lt 10240 ]]; do
    want_mb=$((want_mb / 2))
  done
  if [[ $want_mb -lt 1024 || $((free_mb - want_mb)) -lt 10240 ]]; then
    _warn "fleet-swap: only ${free_mb}MB free on /var — need swap + 10GB margin; skipping (JAB-273). Provision swap manually."
    return 0
  fi
  local swap_file=/var/swap.jabali-fleet
  _log "fleet-swap: provisioning ${want_mb}MB swap at $swap_file (JAB-273; can take a minute on slow disks)"
  if [[ -e "$swap_file" ]]; then
    swapoff "$swap_file" 2>/dev/null || true
    rm -f "$swap_file"
  fi
  if ! fallocate -l "${want_mb}M" "$swap_file" 2>/dev/null; then
    dd if=/dev/zero of="$swap_file" bs=1M count="$want_mb" status=none \
      || { _warn "fleet-swap: allocate failed — box stays swapless (JAB-273)"; rm -f "$swap_file"; return 0; }
  fi
  chmod 0600 "$swap_file"
  mkswap "$swap_file" >/dev/null 2>&1 \
    || { _warn "fleet-swap: mkswap failed — box stays swapless"; rm -f "$swap_file"; return 0; }
  swapon "$swap_file" \
    || { _warn "fleet-swap: swapon failed (containerized/btrfs host?) — box stays swapless"; rm -f "$swap_file"; return 0; }
  if ! grep -qF "$swap_file" /etc/fstab 2>/dev/null; then
    echo "$swap_file none swap sw 0 0" >> /etc/fstab
  fi
  # Big-RAM fleet boxes never ran ensure_swap, so they never got the swappiness
  # tune. Keep swap as an OOM safety net, not an eager pager.
  echo 'vm.swappiness=10' > /etc/sysctl.d/99-jabali-swappiness.conf
  sysctl -w vm.swappiness=10 >/dev/null 2>&1 || true
  _ok "fleet-swap: ${want_mb}MB active at $swap_file (persists via /etc/fstab); vm.swappiness=10 (JAB-273)"
}

# ensure_maintenance_isolation — JAB-273. Installs jabali-maintenance.slice and
# (re)installs the all-tenant maintenance units so they run inside it
# (MemoryMax=50%, Nice=19, idle IO). The per-unit install_* functions are
# full-install-only, so without this converger the hardened units + the slice
# would never reach the existing fleet — the template-only-change-never-reaches-
# update lesson (see ensure_pdns_zone_cache). Runs on fresh install AND update.
ensure_maintenance_isolation() {
  install -m 0644 -o root -g root \
    "${REPO_DIR}/install/systemd/jabali-maintenance.slice" \
    /etc/systemd/system/jabali-maintenance.slice
  local u src
  for u in jabali-cache-doctor jabali-disk-maintenance jabali-backup-retention \
           jabali-aide-check jabali-sso-reaper jabali-retention-sweep; do
    src="${REPO_DIR}/install/systemd/${u}.service"
    [[ -f "$src" ]] || continue
    # Only re-install a unit that already exists, so this never resurrects a
    # service an operator deliberately removed. On fresh install the per-unit
    # functions lay them down first; this then hardens them in place.
    if [[ -f "/etc/systemd/system/${u}.service" ]]; then
      install -m 0644 -o root -g root "$src" "/etc/systemd/system/${u}.service"
    fi
  done
  systemctl daemon-reload 2>/dev/null || true
}

# ensure_pdns_zone_cache — converge PowerDNS's zone cache OFF on EXISTING
# boxes (GH #896 for new-zone blindness; PowerDNS/pdns#11416 for the crash
# the old rediscover workaround triggered on 4.9).
#
# Every panel domain gets its own pdns zone, written straight into the
# gmysql backend. With the zone cache enabled (default refresh 300 s) a
# freshly created (sub)domain answers NXDOMAIN until the next refresh —
# measured live: 161 s on a fresh subdomain. On 4.9 `pdns_control
# rediscover` DOES refresh the cache, but concurrent rediscover + refresh
# runs race AuthZoneCache::replace() and trip the
# `pending->d_replacePending` assertion → SIGABRT → pdns down (observed
# in production on 4.9.16 during bulk zone upserts). =0 disables the
# cache: zone lookups hit gmysql directly (pre-4.5 behaviour), new zones
# are servable immediately, and updateZoneCache() early-returns so the
# assertion can never fire.
#
# Fresh installs get the setting from the 01-jabali-mysql.conf template in
# install_powerdns. That function does NOT run on `jabali update`
# (provision_new_software), so this converger carries the setting to the
# existing fleet — the ensure_tmp_hardening / #1006 lesson: a template-only
# change never reaches a box that only ever updates. Idempotent; restarts
# pdns only when the line actually changed, and only if the unit is active
# (a dns-module-off box keeps pdns masked).
ensure_pdns_zone_cache() {
  local conf=/etc/powerdns/pdns.d/01-jabali-mysql.conf
  [[ -f "$conf" ]] || return 0   # dns module never installed here
  if grep -q '^zone-cache-refresh-interval=0$' "$conf"; then
    return 0
  fi
  if grep -q '^zone-cache-refresh-interval=' "$conf"; then
    sed -i 's/^zone-cache-refresh-interval=.*/zone-cache-refresh-interval=0/' "$conf"
  else
    cat >> "$conf" <<'ZONECACHE'

# GH #896 + PowerDNS/pdns#11416: zone cache OFF — new zones visible
# immediately, and no zone-cache replace() for rediscover to race.
# See install_powerdns for the full rationale. Appended by
# ensure_pdns_zone_cache on update.
zone-cache-refresh-interval=0
ZONECACHE
  fi
  if systemctl is-active --quiet pdns 2>/dev/null; then
    systemctl restart pdns \
      && _ok "pdns zone-cache-refresh-interval=0 applied (zone cache off — new zones visible immediately, pdns#11416 crash path closed)" \
      || _warn "pdns restart failed after zone-cache config — new setting applies on next pdns restart"
  else
    _log "pdns not active — zone-cache setting staged in $conf for next start"
  fi
}

# JAB-350: pin the GLOBAL AXFR ACL to loopback on EXISTING boxes. The fresh-
# install template (install_powerdns) writes allow-axfr-ips=127.0.0.0/8,::1, but
# install_powerdns does NOT run on `jabali update`, so a host that only ever
# updates would keep whatever global ACL it has. If that global is permissive —
# an operator edit, a package/build default, or a stale hand-written
# /etc/powerdns/pdns.conf — ANY internet client can transfer every managed zone
# (bulk hostname/MX/TXT enumeration). This drop-in is read after the main
# pdns.conf, so the pinned line overrides such a value. Configured secondaries
# still transfer via their per-zone ALLOW-AXFR-FROM metadata (unioned with this
# global); loopback stays allowed for local operator troubleshooting.
ensure_pdns_axfr_deny() {
  local conf=/etc/powerdns/pdns.d/01-jabali-mysql.conf
  [[ -f "$conf" ]] || return 0   # dns module never installed here
  if grep -Fxq 'allow-axfr-ips=127.0.0.0/8,::1' "$conf"; then
    return 0
  fi
  if grep -q '^allow-axfr-ips=' "$conf"; then
    sed -i 's|^allow-axfr-ips=.*|allow-axfr-ips=127.0.0.0/8,::1|' "$conf"
  else
    cat >> "$conf" <<'AXFRDENY'

# JAB-350: pin the global AXFR ACL to loopback so a permissive main pdns.conf
# (operator edit / package default) can't leave zones internet-transferable.
# Secondaries still transfer via per-zone ALLOW-AXFR-FROM metadata. Appended by
# ensure_pdns_axfr_deny on update.
allow-axfr-ips=127.0.0.0/8,::1
AXFRDENY
  fi
  if systemctl is-active --quiet pdns 2>/dev/null; then
    systemctl restart pdns \
      && _ok "pdns global AXFR ACL pinned to loopback (JAB-350 — external zone transfers denied; secondaries via per-zone metadata)" \
      || _warn "pdns restart failed after AXFR config — new setting applies on next pdns restart"
  else
    _log "pdns not active — AXFR ACL staged in $conf for next start"
  fi
}

# ensure_tmp_hardening — assert the kernel's /tmp symlink + hardlink
# protections, and persist them.
#
# install.sh downloads several artifacts to PREDICTABLE root-owned paths in
# world-writable /tmp (the Go tarball, wp-cli, phpMyAdmin, Kratos, Stalwart,
# the maldet log). Their contents are sha256-verified, so a content swap
# fails — but an unprivileged local user who pre-creates one of those names as
# a symlink can still redirect a root write. Some of those paths are shared
# ON PURPOSE (concurrent installer re-entry races on the same download), so
# converting them to mktemp would break that design; hardening the directory
# semantics fixes the whole class without touching any of them.
#
# fs.protected_symlinks / protected_hardlinks default to 1 on Debian, but the
# installer never checked, and a host with a hand-rolled sysctl.conf can have
# them off. protected_regular / protected_fifos extend the same idea to
# O_CREAT writes into sticky world-writable dirs. Idempotent.
ensure_tmp_hardening() {
  cat >/etc/sysctl.d/60-jabali-tmp-hardening.conf <<'SYSCTL_TMP'
# Managed by jabali install.sh. Refuse to follow symlinks/hardlinks in
# world-writable sticky dirs (/tmp) when the follower and the owner differ —
# the classic root-writes-through-a-planted-symlink hazard.
fs.protected_symlinks = 1
fs.protected_hardlinks = 1
fs.protected_regular = 2
fs.protected_fifos = 1
SYSCTL_TMP
  sysctl -w fs.protected_symlinks=1  >/dev/null 2>&1 || true
  sysctl -w fs.protected_hardlinks=1 >/dev/null 2>&1 || true
  sysctl -w fs.protected_regular=2   >/dev/null 2>&1 || true
  sysctl -w fs.protected_fifos=1     >/dev/null 2>&1 || true
  local sym
  sym="$(sysctl -n fs.protected_symlinks 2>/dev/null || echo '?')"
  if [[ "$sym" == "1" ]]; then
    _ok "tmp hardening: fs.protected_symlinks/hardlinks enforced"
  else
    _warn "tmp hardening: fs.protected_symlinks is '$sym' — root downloads to /tmp are not symlink-protected on this kernel"
  fi
}

# JAB-159 phase 3: echo the host deploy profile. "demo" selects a demo build
# (-tags demo + VITE_DEMO=1); absent/unreadable = production (empty output).
_deploy_profile() {
  local f=/etc/jabali/deploy-profile
  [ -r "$f" ] && tr -d "[:space:]" < "$f" 2>/dev/null || true
}

# GH #731: the release tarball already ships compiled linux/amd64 binaries, and
# jabali-panel embeds the built SPA (//go:embed all:dist in panel-ui/embed.go).
# bootstrap.sh hands us their directory in JABALI_PREBUILT_BIN. Using them lets a
# small host skip `npm ci` + `vite build` + four `go build`s entirely — the vite
# step alone is what OOM-kills 2 GB VPSes (see ensure_swap's own warning), and
# ensure_swap soft-returns 0 on containerized hosts where swapon is blocked, so
# on those there is no swap to save it.
#
# Fail-SAFE by design: any doubt returns non-zero and we build from source, which
# is exactly what every existing host already does. The smoke test runs the real
# binary, because a truncated download or corrupt tarball would otherwise only
# surface at first boot.
_prebuilt_ready() {
  local d="${JABALI_PREBUILT_BIN:-}"
  [[ -n "$d" && -d "$d" ]] || return 1
  local b
  for b in jabali-panel jabali-agent jabali-ssh-shell jabali-mailhook jabali-sendmail jabali-webdav; do
    [[ -s "$d/$b" && -x "$d/$b" ]] || return 1
  done
  "$d/jabali-panel" version >/dev/null 2>&1 || return 1
  return 0
}

# _prebuilt_version reads the release short SHA from the tarball MANIFEST, so the
# installed binary reports the commit it was actually built from. The git clone
# can sit on a newer commit than the release, so rev-parse would lie here.
_prebuilt_version() {
  local mf="${JABALI_RELEASE_MANIFEST:-}"
  if [[ -n "$mf" && -r "$mf" ]]; then
    local v
    v="$(awk -F= '/^short_sha=/{print $2; exit}' "$mf" 2>/dev/null)"
    [[ -n "$v" ]] && { printf '%s' "$v"; return 0; }
  fi
  printf 'prebuilt'
}

build_frontend() {
  if _prebuilt_ready; then
    _ok "prebuilt release binaries present — skipping npm ci + vite build (the SPA is already embedded in jabali-panel)"
    return 0
  fi
  ensure_swap
  _log "building panel-ui (npm ci + npm run build)"
  # npm ci needs lock + no partial node_modules. Run as the service user so
  # the node_modules cache sits in the project dir, not /root.
  sudo -u "$SERVICE_USER" -H env \
    HOME="$REPO_DIR" \
    PATH="/usr/bin:/bin" \
    bash -c "cd '$REPO_DIR/panel-ui' && npm ci --no-audit --no-fund --prefer-offline"

  # Wipe Vite's dep pre-bundling cache. When a previous install/update
  # left a node_modules/.vite dir, its cached resolutions for packages
  # like react-dom can point at paths invalidated by the fresh npm ci,
  # and vite build fails with "Failed to resolve entry for package X".
  # Cheap to regenerate (seconds).
  rm -rf "$REPO_DIR/panel-ui/node_modules/.vite"

  # Cap V8 heap at 2048 MB so the build can't push past total host
  # RAM + trigger the OOM killer on small VPSes. Vite peaks around
  # 1.2 GB on the panel-ui bundle; 1 GB ceiling leaves V8 enough
  # room for the bundle graph + closures, and the swap file picks
  # up the spillover.
  #
  # nice -n 19 + ionice -c 3 (idle): the build is interactive-only
  # during install, so a low CPU + I/O priority lets in-flight
  # service installs (MariaDB postinst, CrowdSec hub-data fetch,
  # PHP-FPM enable) win contention. Adds ~5-10s to total build
  # time on a fully-loaded box; ~0s on an idle one.
  local vite_demo=""
  [ "$(_deploy_profile)" = "demo" ] && { vite_demo="VITE_DEMO=1"; _log "demo profile active: building panel-ui with VITE_DEMO=1"; }
  # Cap the V8 old-space RAM-aware (GH #760). A flat 2 GB heap lets node
  # outgrow a small VPS and get OOM-killed mid-build even with the swap file
  # above — the operator sees a cryptic 'Killed'. Scale the ceiling to host
  # RAM so a 2 GB box keeps node under RAM+swap; big hosts keep the roomy cap.
  #   ≤2 GB → 1024 MB   ≤4 GB → 1536 MB   >4 GB → 2048 MB
  local node_heap_mb=2048 mem_mb_fe
  mem_mb_fe=$(awk '/^MemTotal:/{print int($2/1024)}' /proc/meminfo 2>/dev/null || echo 0)
  if   [[ $mem_mb_fe -gt 0 && $mem_mb_fe -le 2048 ]]; then node_heap_mb=1024
  elif [[ $mem_mb_fe -gt 0 && $mem_mb_fe -le 4096 ]]; then node_heap_mb=1536
  fi
  _log "panel-ui build: host=${mem_mb_fe}MB RAM → NODE --max-old-space-size=${node_heap_mb}"
  sudo -u "$SERVICE_USER" -H env \
    HOME="$REPO_DIR" \
    PATH="/usr/bin:/bin" \
    NODE_OPTIONS="--max-old-space-size=${node_heap_mb}" \
    $vite_demo \
    bash -c "cd '$REPO_DIR/panel-ui' && nice -n 19 ionice -c 3 npm run build"
  _ok "panel-ui built → $REPO_DIR/panel-ui/dist/"

  # JAB-156: cap the npm cache. `npm ci` leaves every downloaded tarball in
  # $HOME/.npm/_cacache (HOME=$REPO_DIR here → /opt/jabali-panel/.npm), which
  # grows unbounded across updates as deps churn (630 MB on a 16-day host).
  # The build is already done, so purge it — the next update's npm ci
  # re-populates only what that build needs. Best-effort; never fail the build.
  sudo -u "$SERVICE_USER" -H env HOME="$REPO_DIR" PATH="/usr/bin:/bin" \
    bash -c "npm cache clean --force" >/dev/null 2>&1 || true
}

# sync_app_catalogs rsyncs the docker-app + py-framework marketplace catalogs
# from the repo into the production paths the panel-api + CLI read at startup,
# then reloads panel-api so the in-memory catalogs pick up the change. Called
# from build_backend (full install) AND provision_new_software (jabali update),
# so new entries reach every box, not just fresh installs.
sync_app_catalogs() {
  # M48: docker-app catalog.
  if [[ -d "$REPO_DIR/install/docker-apps" ]]; then
    install -d -m 0755 /usr/local/share/jabali/docker-apps
    rsync -a --delete --exclude=".git" \
      "$REPO_DIR/install/docker-apps/" \
      /usr/local/share/jabali/docker-apps/
    _ok "synced docker-app catalog -> /usr/local/share/jabali/docker-apps/"
  fi

  # JAB-164: Python framework marketplace catalog (framework.yaml + template/
  # starters + patch.py).
  if [[ -d "$REPO_DIR/install/py-frameworks" ]]; then
    install -d -m 0755 /usr/local/share/jabali/py-frameworks
    rsync -a --delete --exclude=".git" \
      "$REPO_DIR/install/py-frameworks/" \
      /usr/local/share/jabali/py-frameworks/
    _ok "synced py-framework catalog -> /usr/local/share/jabali/py-frameworks/"
  fi

  # panel-api loads both catalogs into memory at startup; reload so a running
  # panel picks up the just-synced entries (idempotent; try-restart no-ops if
  # the unit isn't up yet).
  if [[ -d /usr/local/share/jabali/py-frameworks || -d /usr/local/share/jabali/docker-apps ]]; then
    systemctl try-restart jabali-panel 2>/dev/null || true
    _ok "reloaded panel-api so the app catalogs pick up the sync"
  fi
}

build_backend() {
  _log "building panel-api + jabali-agent"
  local version full_sha btime
  version="$(sudo -u "$SERVICE_USER" -H git -C "$REPO_DIR" rev-parse --short HEAD)"
  full_sha="$(sudo -u "$SERVICE_USER" -H git -C "$REPO_DIR" rev-parse HEAD)"
  # GH #731: with prebuilt binaries the version must come from the release, not
  # from the clone -- the clone tracks the branch and can already be ahead of the
  # tarball, which would make `jabali version` report a commit the running binary
  # was never built from.
  if _prebuilt_ready; then
    version="$(_prebuilt_version)"
    full_sha="$version"
  fi
  btime="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

  install -d -m 0755 -o "$SERVICE_USER" -g "$SERVICE_USER" "$REPO_DIR/bin"
  local tmp_panel="$REPO_DIR/bin/jabali-panel.new"
  local tmp_agent="$REPO_DIR/bin/jabali-agent.new"
  local tmp_sshshell="$REPO_DIR/bin/jabali-ssh-shell.new"
  local tmp_mailhook="$REPO_DIR/bin/jabali-mailhook.new"
  local tmp_sendmail="$REPO_DIR/bin/jabali-sendmail.new"
  local tmp_webdav="$REPO_DIR/bin/jabali-webdav.new"

  # Build-info ldflags: panel-api exposes api.Version (short SHA),
  # api.Commit (full SHA) and api.BuildTime (RFC3339) through
  # `jabali version` + /health. Must match panel-api/cmd/server/
  # update.go (gitRevParseAsUser + ldflagsAPI) so the version string
  # survives both install.sh AND every `jabali update` cycle.
  local panel_ld="-s -w"
  local panel_tags=""
  [ "$(_deploy_profile)" = "demo" ] && { panel_tags="-tags demo"; _log "demo profile active: building panel-api with -tags demo"; }
  panel_ld+=" -X git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api.Version=$version"
  panel_ld+=" -X git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api.Commit=$full_sha"
  panel_ld+=" -X git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api.BuildTime=$btime"

  # Low-RAM hosts (GH #760): serialize package compilation (-p=1) and soft-cap
  # the Go heap so building panel-api — a large binary whose default build
  # parallelism (=nproc) fans out several compiler processes — can't OOM a 2 GB
  # VPS. -p=1 is the big lever (one compile at a time); GOMEMLIMIT + a lower
  # GOGC make each go tool's GC more aggressive. No effect >4 GB, so CI and
  # real hosts build exactly as before (just slower on the tiny boxes).
  local go_lowmem="" mem_mb_be
  mem_mb_be=$(awk '/^MemTotal:/{print int($2/1024)}' /proc/meminfo 2>/dev/null || echo 0)
  if [[ $mem_mb_be -gt 0 && $mem_mb_be -le 4096 ]]; then
    if [[ $mem_mb_be -le 2560 ]]; then
      go_lowmem="GOFLAGS=-p=1 GOMEMLIMIT=900MiB GOGC=40"
    else
      go_lowmem="GOFLAGS=-p=1 GOMEMLIMIT=1500MiB GOGC=50"
    fi
    _log "build-backend: low-RAM host (${mem_mb_be}MB) → serialize go build ($go_lowmem)"
  fi
  if _prebuilt_ready; then
    # GH #731: copy the release binaries into the same .new paths the source
    # build would have produced, so every step below -- install, backup pruning,
    # catalog sync, wp-plugin bundling, the `jabali` symlink -- runs unchanged.
    _log "installing prebuilt release binaries (version=$version) — no on-box compile"
    install -m 0755 "$JABALI_PREBUILT_BIN/jabali-panel"     "$tmp_panel"
    install -m 0755 "$JABALI_PREBUILT_BIN/jabali-agent"     "$tmp_agent"
    install -m 0755 "$JABALI_PREBUILT_BIN/jabali-ssh-shell" "$tmp_sshshell"
    install -m 0755 "$JABALI_PREBUILT_BIN/jabali-mailhook"  "$tmp_mailhook"
    install -m 0755 "$JABALI_PREBUILT_BIN/jabali-sendmail"  "$tmp_sendmail"
    install -m 0755 "$JABALI_PREBUILT_BIN/jabali-webdav"    "$tmp_webdav"
  else
    # One invocation of go, three binaries — shared module, shared build cache.
    sudo -u "$SERVICE_USER" -H env \
      PATH="$GO_ROOT/bin:/usr/bin:/bin" \
      HOME="$REPO_DIR" \
      GOCACHE="$REPO_DIR/.cache/go-build" \
      GOMODCACHE="$REPO_DIR/.cache/go-mod" \
      $go_lowmem \
      bash -c "cd '$REPO_DIR' && \
        go build -trimpath $panel_tags -ldflags '$panel_ld' -o '$tmp_panel' ./panel-api/cmd/server && \
        go build -trimpath -ldflags '-s -w -X main.version=$version' -o '$tmp_agent' ./panel-agent/cmd/jabali-agent && \
        go build -trimpath -ldflags '-s -w' -o '$tmp_sshshell' ./panel-agent/cmd/jabali-ssh-shell && \
        go build -trimpath -ldflags '-s -w -X main.version=$version' -o '$tmp_mailhook' ./panel-agent/cmd/jabali-mailhook && \
        go build -trimpath -ldflags '-s -w' -o '$tmp_sendmail' ./panel-agent/cmd/jabali-sendmail && \
        go build -trimpath -ldflags '-s -w' -o '$tmp_webdav' ./panel-agent/cmd/jabali-webdav"
  fi

  install -m 0755 "$tmp_panel" "$BIN_PATH"
  install -m 0755 "$tmp_agent" "$AGENT_BIN_PATH"

  # Codeberg #14: prune accumulated binary backups so /usr/local/bin doesn't
  # grow unbounded (a host was found with 19 jabali-panel.bak.* ~ 1.3 GB on
  # $PATH). Keep the 3 most recent of each — these are legacy/operator backups
  # (the install above uses `install` with no backup), but old ones linger.
  local _bakbase _old _pruned=0
  for _bakbase in jabali-panel jabali-agent; do
    while IFS= read -r _old; do
      [[ -n "$_old" ]] && rm -f "$_old" && _pruned=$((_pruned + 1))
    # `|| true`: with no .bak.* files the glob stays literal and ls exits 2.
    # Under `set -o pipefail` (line 58) that becomes the pipeline's status, and
    # `set -E` propagates ERR into this process substitution — so __on_err fired
    # and printed a full "install.sh died" report on every host that had no old
    # backups to prune, which is most of them (the install above uses `install`,
    # which leaves none). The install then carried on and succeeded, so the
    # message was pure noise — and noise on the one line operators are supposed
    # to trust when something really does fail.
    done < <({ ls -1t "/usr/local/bin/${_bakbase}".bak.* 2>/dev/null || true; } | tail -n +4)
  done
  (( _pruned > 0 )) && _ok "pruned $_pruned old binary backup(s) from /usr/local/bin"
  # M13 Step 1: jabali-ssh-shell ships at 0755 root:root. The wrapper
  # falls back to /usr/sbin/nologin when sandbox dispatch isn't
  # wired (Step 1 = skeleton; Step 2 + 3 wire bwrap + nspawn argv).
  install -m 0755 "$tmp_sshshell" /usr/local/bin/jabali-ssh-shell
  install -m 0755 "$tmp_mailhook" /usr/local/bin/jabali-mailhook
  # GH #1146: the per-subaccount WebDAV worker. root:root 0755 like the other
  # /usr/local/bin agents; the agent binds it into each #1145 chroot at start.
  install -m 0755 -o root -g root "$tmp_webdav" /usr/local/bin/jabali-webdav
  # JAB-230: the PHP mail() submission shim lives in libexec (exec'd by FPM
  # workers via sendmail_path, never on an operator's PATH).
  install -d -m 0755 /usr/local/libexec/jabali
  install -m 0755 -o root -g root "$tmp_sendmail" /usr/local/libexec/jabali/jabali-sendmail
  rm -f "$tmp_panel" "$tmp_agent" "$tmp_sshshell" "$tmp_mailhook" "$tmp_sendmail" "$tmp_webdav"

  # Sync the docker-app + py-framework catalogs into the production paths the
  # panel reads at startup, then reload it. Also runs from provision_new_software
  # so `jabali update` (which does NOT call build_backend) keeps the catalogs
  # current — otherwise new marketplace entries never reach a box that only ever
  # runs `jabali update` (JAB-164: the Python catalog showed just the 3 original
  # frameworks + blank icons on such boxes).
  sync_app_catalogs

  # #406: bundle the jabali-cache WordPress plugin read-only into the
  # production path the agent installs FROM (wordpress.cache_set). Tenants
  # never supply plugin code; re-synced on every `jabali update`.
  if [[ -d "$REPO_DIR/wp-plugins/jabali-cache" ]]; then
    install -d -m 0755 /usr/local/share/jabali/wp-plugins
    rsync -a --delete --exclude=".git" \
      "$REPO_DIR/wp-plugins/jabali-cache/" \
      /usr/local/share/jabali/wp-plugins/jabali-cache/
    chown -R root:root /usr/local/share/jabali/wp-plugins/jabali-cache
    _ok "bundled jabali-cache -> /usr/local/share/jabali/wp-plugins/jabali-cache/"
  fi

  # Ergonomic alias: `jabali ...` works the same as `jabali-panel ...`.
  # The cobra root command is already named "jabali"; this just saves
  # the "-panel" typing for operators. Symlink is idempotent.
  ln -sf "$BIN_PATH" /usr/local/bin/jabali

  _ok "installed $BIN_PATH (version=$version)"
  _ok "installed $AGENT_BIN_PATH (version=$version)"
  _ok "installed /usr/local/bin/jabali-ssh-shell (M13 Step 1 wrapper)"
  _ok "symlinked /usr/local/bin/jabali -> $BIN_PATH"
}

# install_jabali_mailhook — loopback MTA-hook service that appends per-domain
# disclaimers by rewriting the MIME body in Go (GH #233, ADR-0143). It is the
# one TCP-loopback exception to M25 (ADR-0050): bound to 127.0.0.1, Bearer-token
# authenticated, single POST endpoint, read-only DB access. Sieve could not
# append to HTML without corrupting it.
install_jabali_mailhook() {
  local token_file="/etc/jabali-panel/mailhook.token"
  if [[ ! -s "$token_file" ]]; then
    install -m 0640 -o root -g "$SERVICE_USER" /dev/null "$token_file"
    head -c 32 /dev/urandom | base64 | tr -d '/+=' | head -c 43 > "$token_file"
    chmod 0640 "$token_file"
    chown root:"$SERVICE_USER" "$token_file"
    _ok "generated mailhook bearer token at $token_file"
  fi

  local unit="/etc/systemd/system/jabali-mailhook.service"
  cat > "$unit" <<EOF
[Unit]
Description=Jabali mail disclaimer MTA hook (loopback, ADR-0143)
After=network.target mariadb.service jabali-stalwart.service
Wants=mariadb.service

[Service]
Type=simple
User=${SERVICE_USER}
# jabali-mail group: read /etc/jabali-panel/stalwart-mariadb.password (root:jabali-mail 0640).
# SupplementaryGroups (not Group=) so the jabali primary group — which owns
# mailhook.token — is retained (systemd-Group-drops-primary scar).
SupplementaryGroups=jabali-mail
ExecStart=/usr/local/bin/jabali-mailhook
Environment=MAILHOOK_ADDR=127.0.0.1:8462
Restart=on-failure
RestartSec=2
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ProtectControlGroups=true
ProtectKernelTunables=true
ProtectKernelModules=true
RestrictAddressFamilies=AF_INET AF_INET6
IPAddressAllow=localhost
IPAddressDeny=any

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable jabali-mailhook.service >/dev/null 2>&1 || true
  systemctl restart jabali-mailhook.service
  _ok "jabali-mailhook.service installed (127.0.0.1:8462)"
}

# ---------- M13 SSH shell sandbox prerequisites (ADR-pending) ------------
#
# install_ssh_sandbox_prereqs apt-installs the bubblewrap + systemd-
# container packages M13 needs. Default-mode file
# /etc/jabali/ssh-sandbox-mode lands at first install with mode
# 'bubblewrap'; operator can flip to 'nspawn' via SQL or future CLI.
# Idempotent on re-run. Per plan §0 #4: missing tooling → wrapper
# falls back to nologin (never bash).
install_ssh_sandbox_prereqs() {
  _log "installing M13 SSH sandbox prerequisites (bubblewrap, systemd-container)"
  apt-get install -y -qq --no-install-recommends bubblewrap systemd-container >/dev/null 2>&1 || \
    _warn "bubblewrap / systemd-container apt install failed — wrapper will fall back to nologin until fixed"

  install -d -m 0755 -o root -g root /etc/jabali
  if [[ ! -f /etc/jabali/ssh-sandbox-mode ]]; then
    echo "bubblewrap" > /etc/jabali/ssh-sandbox-mode
    chmod 0644 /etc/jabali/ssh-sandbox-mode
    _ok "created /etc/jabali/ssh-sandbox-mode (default: bubblewrap)"
  fi

  # Per-user image-pin dir for nspawn mode (currently unused; Step 3
  # follow-up reads /etc/jabali/users/<user>/nspawn-image).
  install -d -m 0755 -o root -g root /etc/jabali/users

  # Ubuntu 24.04+ ships kernel.apparmor_restrict_unprivileged_userns=1, which
  # blocks bwrap from creating the user namespace the sandbox needs — every
  # tenant SSH session would die with "bwrap: setting up uid map: Permission
  # denied". Grant userns to jabali-ssh-shell via a scoped, ENFORCE-mode
  # AppArmor profile (NOT the host-wide sysctl, NOT all bwrap callers). Only
  # acts when the restriction is active; a no-op on Debian. Runs after
  # install_apparmor, so apparmor_parser/aa-enforce are present. (GH #184
  # follow-up; loaded enforce via apparmor_parser -r — flags=(unconfined)
  # keeps it allow-all, so do NOT aa-enforce it.)
  if [[ "$(sysctl -n kernel.apparmor_restrict_unprivileged_userns 2>/dev/null)" == "1" ]]; then
    if command -v apparmor_parser >/dev/null 2>&1; then
      cat > /etc/apparmor.d/jabali-ssh-shell <<'AAPROF'
abi <abi/4.0>,
include <tunables/global>

# Grant unprivileged user-namespace creation to the M13 SSH sandbox wrapper on
# hosts that restrict userns (Ubuntu 24.04+). flags=(unconfined) adds no
# confinement — the bwrap sandbox is the actual boundary; the exec'd bwrap
# inherits this profile and may create the namespace. Scoped to this binary
# only, so other bwrap callers stay restricted.
profile jabali-ssh-shell /usr/local/bin/jabali-ssh-shell flags=(unconfined) {
  userns,

  include if exists <local/jabali-ssh-shell>
}
AAPROF
      # apparmor_parser -r loads in ENFORCE by default, which is required
      # (complain mode does NOT satisfy the kernel userns check). Do NOT
      # aa-enforce afterwards: that strips the flags=(unconfined) allow-all and
      # turns the profile restrictive, denying the wrapper's own file reads.
      if apparmor_parser -r /etc/apparmor.d/jabali-ssh-shell 2>/dev/null; then
        _ok "AppArmor userns profile for jabali-ssh-shell (Ubuntu noble bwrap fix)"
      else
        _warn "failed to load AppArmor userns profile for jabali-ssh-shell — SSH sandbox may fail on this host"
      fi
    else
      _warn "kernel restricts unprivileged userns but apparmor_parser is missing — SSH sandbox will fail until a userns AppArmor profile is loaded"
    fi
  fi
}

# ---------- step 6: env file + systemd unit ---------------------------------

write_env_file() {
  if [[ -f "$ENV_FILE" ]]; then
    _ok "env file exists: $ENV_FILE (not overwriting)"
    return
  fi
  local jwt_secret
  jwt_secret="$(openssl rand -hex 32)"
  _log "writing env file: $ENV_FILE (generating JWT_SECRET)"
  cat >"$ENV_FILE" <<EOF
# Jabali Panel — environment for jabali-panel.service
# Generated $(date -Iseconds). Edit as needed, then: systemctl restart $SERVICE_NAME
# Secrets belong here (DATABASE_URL, JWT_SECRET). Non-secret config goes in
# $(dirname "$ENV_FILE")/config.toml.

PANEL_ADDR=$PANEL_ADDR
PANEL_ENV=production
JWT_SECRET=$jwt_secret
EOF
  chmod 0640 "$ENV_FILE"
  chown root:"$SERVICE_USER" "$ENV_FILE"
}

# ---------- step 6a: self-signed TLS cert ------------------------------------

provision_tls_cert() {
  local cert_dir="/etc/jabali/tls"
  local cert_file="$cert_dir/panel.crt"
  local key_file="$cert_dir/panel.key"

  # Grab the machine's hostname and first non-loopback IP for SANs.
  local cn
  cn="$(hostname -f 2>/dev/null || hostname)"
  local ip
  ip="$(hostname -I 2>/dev/null | awk '{print $1}')"

  # M6.4 (ADR-0048): detect hostname drift between a prior install's cert
  # and the current $JABALI_SRV_HOSTNAME / `hostname -f`. If the cert's
  # CN no longer matches, force regeneration — otherwise the admin lands
  # on a cert for the OLD hostname, which every browser will reject.
  # Also detect missing mail.<hostname> SAN on an existing cert (common
  # on upgrade from pre-M6.4 installs).
  #
  # Regression guard (2026-05-09): on a host with a deployed Let's
  # Encrypt panel cert (M32 / ADR-0066), the existing cert at
  # $cert_file is the LE fullchain copied by
  # /etc/letsencrypt/renewal-hooks/deploy/jabali-panel-cert.sh. That
  # cert's issuer is "Let's Encrypt", not "Jabali Panel". The regen
  # branch below was clobbering that LE cert with a fresh self-signed
  # one any time the SAN-check failed (LE certs typically don't carry
  # `mail.<cn>` SAN unless explicitly requested), turning a valid
  # browser-trusted panel cert into a Firefox warning every install.sh
  # run. Detect issuer first; if the active cert was issued by anyone
  # other than the self-signed bootstrap (O="Jabali Panel"), leave it
  # alone. The LE deploy-hook owns it from issuance through renewal.
  local need_regen=0
  if [[ -f "$cert_file" ]]; then
    local cert_issuer_o=""
    cert_issuer_o="$(openssl x509 -in "$cert_file" -noout -issuer 2>/dev/null \
      | sed -n 's/.*O *= *\([^,/]*\).*/\1/p' | sed 's/[[:space:]]*$//')"
    if [[ -n "$cert_issuer_o" && "$cert_issuer_o" != "Jabali Panel" ]]; then
      _ok "panel cert issued by '$cert_issuer_o' (not self-signed) — preserving"
      # Still drop a TLS_CERT line into ENV_FILE if missing so a fresh
      # install on a host with a pre-existing LE cert wires Go's TLS
      # listener correctly.
      if ! grep -q '^TLS_CERT=' "$ENV_FILE" 2>/dev/null; then
        cat >>"$ENV_FILE" <<EOF

# TLS — Let's Encrypt cert deployed by jabali-panel-cert.sh hook.
TLS_CERT=$cert_file
TLS_KEY=$key_file
EOF
      fi
      return 0
    fi
    local cert_cn=""
    cert_cn="$(openssl x509 -in "$cert_file" -noout -subject 2>/dev/null \
      | sed -n 's/.*CN *= *\([^,/]*\).*/\1/p' | tr -d ' ')"
    if [[ -n "$cert_cn" && "$cert_cn" != "$cn" ]]; then
      _warn "panel cert CN=$cert_cn != current hostname $cn — hostname drift, regenerating"
      need_regen=1
    elif ! openssl x509 -in "$cert_file" -noout -ext subjectAltName 2>/dev/null \
        | grep -qE "DNS:mail\.${cn}(,|$)"; then
      _log "panel cert missing mail.${cn} SAN — regenerating"
      need_regen=1
    fi
    if (( need_regen == 1 )); then
      rm -f "$cert_file" "$key_file"
    fi
  fi

  if [[ -f "$cert_file" && -f "$key_file" ]]; then
    _ok "TLS cert exists with mail.${cn} SAN: $cert_file"
  else
    _log "generating self-signed TLS certificate"
    # Dir traversable by www-data so nginx can open the key file below.
    install -d -m 0755 -o root -g root "$cert_dir"

    # M6.4: include mail.<hostname> so the panel-primary domain's
    # Bulwark vhost (served on mail.<panel-hostname>) presents a cert
    # Firefox accepts. Other per-tenant mail vhosts have their own
    # LE cert (M6.1); this SAN is panel-hostname-only.
    local san="DNS:${cn},DNS:mail.${cn},DNS:localhost,IP:127.0.0.1"
    [[ -n "$ip" ]] && san+=",IP:${ip}"

    openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 \
      -keyout "$key_file" -out "$cert_file" \
      -days 3650 -nodes \
      -subj "/CN=${cn}/O=Jabali Panel" \
      -addext "subjectAltName=${san}" \
      2>/dev/null

    _ok "self-signed TLS cert created ($cert_file) with SAN ${san}"

    # M6.4: nginx re-reads the cert on next handshake via reload; panel-api
    # is a Go HTTP server that caches the cert in memory at startup and
    # does NOT SIGHUP-reread — full restart required. try-reload-or-restart
    # is the wrong signal for Go servers because reload silently succeeds
    # as a no-op, masking that the cert wasn't rotated. Accept the ~100ms
    # TLS downtime as the cost of cert rotation.
    systemctl reload nginx 2>/dev/null \
      || _warn "nginx reload failed; check 'journalctl -u nginx'"
    # Skip if jabali-panel isn't installed yet (first-time install: cert
    # runs before start_and_verify).
    if systemctl list-unit-files "${SERVICE_NAME}.service" >/dev/null 2>&1; then
      systemctl restart "$SERVICE_NAME" 2>/dev/null \
        || _warn "$SERVICE_NAME restart failed; check 'systemctl status $SERVICE_NAME'"
    fi
  fi

  # Always enforce ownership+mode (even on existing certs, in case an
  # older installer run left them root:jabali 0640, which nginx can't
  # read). Cert is public → 0644 root:root; key is shared between the
  # panel (jabali, supplementary group www-data) and nginx (www-data)
  # via group read.
  chown root:root "$cert_file"
  chmod 0644 "$cert_file"
  chown root:www-data "$key_file"
  chmod 0640 "$key_file"

  # Write TLS paths to env file if not already present.
  if ! grep -q '^TLS_CERT=' "$ENV_FILE" 2>/dev/null; then
    cat >>"$ENV_FILE" <<EOF

# TLS — self-signed; replace with Certbot cert for production.
TLS_CERT=$cert_file
TLS_KEY=$key_file
EOF
  fi
}

# ---------- step 6b: panel-hostname Let's Encrypt webroot + deploy-hook ----
#
# M32 (ADR-0066). These two functions ensure the LE machinery is in
# place on every install. They DO NOT trigger an actual issuance —
# the admin UI's "Use Let's Encrypt" toggle drives that, and the
# routability gate skips lab/dev hostnames silently. So even on a
# .local install, the webroot directory and deploy-hook script exist
# (forward-compat) but stay dormant.

bootstrap_panel_acme_webroot() {
  local webroot="/var/www/jabali-panel-acme"
  if [[ -d "$webroot" ]]; then
    _ok "panel-acme webroot exists: $webroot"
  else
    _log "creating panel-acme webroot at $webroot"
    install -d -m 0750 -o root -g www-data "$webroot"
    _ok "panel-acme webroot ready: $webroot (root:www-data 0750)"
  fi
  # Always enforce ownership/mode in case an older run left it
  # root:root 0755 — nginx (www-data) needs group read+exec to
  # serve the challenge file.
  chown root:www-data "$webroot"
  chmod 0750 "$webroot"

  # Per-domain MAIL certs (M6.6) use a SEPARATE webroot — every mail vhost
  # serves /.well-known/acme-challenge/ from /var/www/jabali-acme (see
  # jabali-mail-vhost.conf.tmpl). It was never created, so every mail cert
  # failed certbot with "webroot does not exist" (GH#132). Provision it the
  # same way as the panel webroot.
  local mail_webroot="/var/www/jabali-acme"
  if [[ ! -d "$mail_webroot" ]]; then
    install -d -m 0750 -o root -g www-data "$mail_webroot"
    _ok "mail-acme webroot ready: $mail_webroot (root:www-data 0750)"
  fi
  chown root:www-data "$mail_webroot"
  chmod 0750 "$mail_webroot"
}

install_jabali_panel_cert_hook() {
  local hook_dir="/etc/letsencrypt/renewal-hooks/deploy"
  local hook_dst="${hook_dir}/jabali-panel-cert.sh"
  local hook_src="$REPO_DIR/install/letsencrypt/jabali-panel-cert.sh"

  if [[ ! -f "$hook_src" ]]; then
    _warn "panel-cert deploy-hook source missing at $hook_src — skipping"
    return 0
  fi

  install -d -m 0755 -o root -g root "$hook_dir"
  install -m 0755 -o root -g root "$hook_src" "$hook_dst"
  _ok "panel-cert deploy-hook installed at $hook_dst"
}

write_config_file() {
  local dest="$(dirname "$ENV_FILE")/config.toml"
  local src="$REPO_DIR/config.example.toml"
  if [[ -f "$dest" ]]; then
    # M25 Step 6: in-place migrate the [pdns] dsn from TCP to socket on
    # an existing install. (Step 4 already migrated the panel addr; this
    # block is the analogue for pdns.) Idempotent — if the file already
    # has the unix form (or any other custom value), the grep misses
    # and nothing happens.
    if grep -qE '^\s*dsn\s*=\s*"[^"]*@tcp\(127\.0\.0\.1:3306\)/jabali_pdns' "$dest"; then
      _log "migrating config.toml [pdns] dsn from TCP to unix socket (M25 Step 6)"
      sed -i 's|@tcp(127\.0\.0\.1:3306)/jabali_pdns|@unix(/var/run/mysqld/mysqld.sock)/jabali_pdns|' "$dest"
      _ok "config.toml [pdns] dsn migrated"
    fi
    _ok "config file exists: $dest (not overwriting)"
    return
  fi
  if [[ ! -f "$src" ]]; then
    _warn "no $src; skipping config seed"
    return
  fi
  _log "seeding config file: $dest"
  install -m 0640 -o root -g "$SERVICE_USER" "$src" "$dest"

  # Write the [server] block with runtime + identity keys. The panel reads
  # these on first boot to seed the server_settings DB row; the DB is the
  # source of truth afterwards (see docs/adr/0002). config.example.toml no
  # longer declares [server] itself, so this is the sole writer.
  local srv_env="production"
  [[ "${JABALI_DEV:-0}" == "1" ]] && srv_env="development"
  {
    printf '\n[server]\n'
    # M25 Step 4: panel-api listens on a Unix-domain socket. nginx
    # terminates TLS upstream of us via the jabali-panel-vhost (port
    # 8443). Operators can flip back to TCP by editing this line to
    # `127.0.0.1:8443` and dropping the unit-file Group=jabali-sockets;
    # see plans/m25-unix-sockets-runbook.md for the exact rollback.
    printf 'addr        = "unix:/run/jabali-panel/api.sock"\n'
    printf 'env         = "%s"\n' "$srv_env"
    if [[ "${JABALI_SERVER_CONFIGURED:-1}" == "0" ]]; then
      printf 'hostname    = "%s"\n' "${JABALI_SRV_HOSTNAME}"
      printf 'public_ipv4 = "%s"\n' "${JABALI_SRV_IPV4}"
      printf 'public_ipv6 = "%s"\n' "${JABALI_SRV_IPV6}"
      printf 'ns1_name    = "%s"\n' "${JABALI_SRV_NS1_NAME}"
      printf 'ns1_ipv4    = "%s"\n' "${JABALI_SRV_NS1_IPV4}"
      printf 'ns2_name    = "%s"\n' "${JABALI_SRV_NS2_NAME}"
      printf 'ns2_ipv4    = "%s"\n' "${JABALI_SRV_NS2_IPV4}"
    fi
  } >> "$dest"
  _ok "seeded [server] block in $dest"

  # PowerDNS backend DSN for the reconciler. Reads creds from pdns.env
  # so the two files stay in sync. If prompt_server_settings was
  # skipped (re-run), the env file must already exist.
  if [[ -f "${ENV_FILE%/*}/pdns.env" ]]; then
    # shellcheck disable=SC1091
    . "${ENV_FILE%/*}/pdns.env"
    cat >> "$dest" <<EOF

[pdns]
# MySQL DSN for the PowerDNS backend database. Reconciler opens a
# direct connection here to push zones/records in the same transaction
# as the NOTIFY signal.
dsn = "${PDNS_DB_USER}:${PDNS_DB_PASSWORD}@unix(/var/run/mysqld/mysqld.sock)/${PDNS_DB_NAME}?charset=utf8mb4&parseTime=true"
EOF
    _ok "seeded [pdns] block in $dest"
  fi
}

write_agent_systemd_unit() {
  _log "writing systemd unit: /etc/systemd/system/${AGENT_SERVICE_NAME}.service"
  # The agent runs as root because its whole purpose is to perform
  # privileged operations (create Linux users, manage services, etc).
  # Access control is enforced via socket permissions: RuntimeDirectory
  # creates /run/jabali owned root:jabali 0750, and the agent itself
  # chowns its socket to root:jabali 0660 so only the panel (jabali group)
  # can connect. Hardening knobs that make sense for a root daemon:
  #   - ProtectKernel*/LockPersonality keep the agent out of kernel and
  #     exec-mode bystander state (RestrictSUIDSGID is NOT used — it would
  #     block domain.create's setgid chmod on docroots, see below)
  #   - NoNewPrivileges stays false because future commands may need
  #     capabilities-aware subprocess spawns (package install etc).
  #
  # ProtectSystem= and ProtectHome= are INTENTIONALLY NOT SET. The agent
  # writes to /etc (nginx confs, /etc/passwd via useradd, /etc/php,
  # /etc/jabali-panel/dkim, /etc/letsencrypt), /home (user web roots,
  # WordPress, ~/.my.cnf), /var (jabali spool dirs, cron), and /opt
  # (phpMyAdmin, wp-cli). ProtectSystem=strict + ProtectHome=yes (as
  # previously configured) silently turned every such write into EROFS
  # and made domain.create, user.create, domain.email_enable,
  # webmail.vhost_apply, php.pool.apply and the nginx-ratelimits
  # reconciler all fail on a fresh install. Filesystem sandboxing
  # fundamentally doesn't fit a daemon whose job IS OS mutation; our
  # access-control boundary is the Unix socket, not the FS namespace.
  local jabali_gid
  jabali_gid="$(getent group "$SERVICE_USER" | cut -d: -f3)"
  [[ -n "$jabali_gid" ]] || _die "can't resolve gid of $SERVICE_USER"

  # GH #515 / JAB-169: the PTY broker's SO_PEERCRED gate must match panel-api's
  # PRIMARY gid, which is jabali-sockets (panel-api unit sets Group=jabali-sockets
  # so nginx can reach its listen socket). -gid above is the jabali gid for the
  # MAIN agent socket; -pty-gid is jabali-sockets for the terminal broker. One
  # -gid for both refused panel-api and left the web terminal with no keyboard
  # input on every install.
  local jabali_sockets_gid
  jabali_sockets_gid="$(getent group jabali-sockets | cut -d: -f3)"
  [[ -n "$jabali_sockets_gid" ]] || _die "can't resolve gid of jabali-sockets"

  # JAB-366 / JAB-357: SO_PEERCRED allow-list for the MAIN agent socket. The
  # socket's 0660 root:jabali perms are only a GROUP gate — a service account
  # accidentally left in the jabali group (JAB-351/357: webmail) could connect
  # and drive every privileged command. Gate by the connecting UID too: only
  # the panel-api user ($SERVICE_USER) + root (operator CLI) may connect, so a
  # future group regression can never silently re-grant agent root.
  local panel_uid
  panel_uid="$(id -u "$SERVICE_USER" 2>/dev/null)"
  [[ -n "$panel_uid" ]] || _die "can't resolve uid of $SERVICE_USER"

  cat >"/etc/systemd/system/${AGENT_SERVICE_NAME}.service" <<EOF
[Unit]
Description=Jabali Agent (privileged host operations)
After=network-online.target
Wants=network-online.target
# Panel depends on us via Requires= in its unit, so ordering is enforced both ways.

[Service]
Type=simple
User=root
# Group=jabali makes RuntimeDirectory=jabali land as root:jabali (systemd
# always creates the dir matching the service's User:Group). The agent
# still runs with UID=0 so it retains full root for privileged ops — GID
# doesn't gate root. The panel (member of the jabali group) can therefore
# traverse /run/jabali/ and connect to the socket.
Group=$SERVICE_USER
RuntimeDirectory=jabali
RuntimeDirectoryMode=0750
RuntimeDirectoryPreserve=no
ExecStart=$AGENT_BIN_PATH -socket $AGENT_SOCKET -gid $jabali_gid -pty-gid $jabali_sockets_gid -allowed-uids ${panel_uid},0
Restart=on-failure
RestartSec=3
TimeoutStopSec=10

# Hardening for a root daemon. We can't NoNewPrivileges because future
# commands may need to re-exec tooling that escalates (chpasswd, useradd
# etc). See the comment block above the cat << for why ProtectSystem=
# and ProtectHome= are deliberately omitted.
# PrivateTmp + ProtectKernel* + ProtectControlGroups intentionally
# OFF: each one breaks libdbus auth with PID 1's systemd, which the
# agent has to talk to constantly (systemctl daemon-reload, service
# start/stop/restart, set-property for slice limits). Symptom of
# leaving them on: cascade of "Failed to connect to bus: Permission
# denied" across user.limits.apply, domain.create's nginx reload,
# app.install's database create, webmail.start, dns.zone.upsert, and
# every other agent command that touches systemd. Net hardening lost
# is minimal — the agent runs as UID 0 with full capability set.
# RestrictSUIDSGID intentionally OMITTED: domain.create chmods tenant
# docroots to 2750 — the setgid bit makes PHP-FPM uploads inherit the
# www-data group so nginx can read them (avoids 403 on fresh media).
# RestrictSUIDSGID=yes blocks that setgid chmod, so domain.create aborts
# BEFORE writing the vhost; fresh installs/reinstalls then have no website
# vhost and ACME HTTP-01 challenges 404 (GH #213). Weak hardening for a
# root daemon anyway.
LockPersonality=yes

[Install]
WantedBy=multi-user.target
EOF
}

# GH #611: sticky spool dir for WordPress -> nginx cache purge requests. Tenant
# PHP (jabali-cache plugin) drops a purge-request JSON here; the agent's
# StartWpCachePurgeWatcher validates host ownership and purges. Mode 1777 (like
# /tmp) lets any tenant create a request but only its owner (or root) remove it.
# tmpfiles.d recreates it on boot (/run is tmpfs); --create makes it now.
install_wp_purge_spool() {
  local tf=/etc/tmpfiles.d/jabali-wp-purge.conf
  local want='# Managed by jabali install.sh — GH #611 WP->nginx cache purge spool.
d /run/jabali-wp-purge 1777 root root -'
  if [[ ! -f "$tf" ]] || ! cmp -s <(printf '%s\n' "$want") "$tf"; then
    printf '%s\n' "$want" >"$tf"
    chmod 0644 "$tf"
  fi
  systemd-tmpfiles --create "$tf" 2>/dev/null || install -d -m 1777 /run/jabali-wp-purge
  _ok "WP->nginx cache purge spool ready (/run/jabali-wp-purge)"
}

write_systemd_unit() {
  _log "writing systemd unit: /etc/systemd/system/${SERVICE_NAME}.service"
  cat >"/etc/systemd/system/${SERVICE_NAME}.service" <<EOF
[Unit]
Description=Jabali Panel API
After=network-online.target ${AGENT_SERVICE_NAME}.service redis-server.service mariadb.service
Wants=network-online.target mariadb.service
# Panel hard-requires the agent at boot; without the socket we can't do
# privileged ops. If the agent crashes post-boot the panel stays up —
# individual handlers will return 503 with agent:unavailable.
#
# Redis is a hard dep too (M14 / ADR-0056): the notification dispatcher
# can't run without its stream. systemd will restart panel-api if
# redis-server stops, so the ordering is symmetric with mariadb's.
#
# mariadb is in After=/Wants= but deliberately NOT in Requires=. The
# first thing main() does is ping the DB and exit(1) if it fails, so
# without the ordering panel-api reliably loses the race on a slow or
# small host: measured on a 2 GB / 2 vCPU box, panel-api started 44s
# before mariadb was ready and died with
#
#   Error: ping: dial unix /var/run/mysqld/mysqld.sock: no such file
#
# every single boot, recovering only via Restart=on-failure. Requires=
# is the wrong tool for that — it would also stop the panel whenever
# mariadb restarts, which is a worse outcome than handlers returning
# errors for a few seconds. Ordering is the actual bug; coupling isn't.
Requires=${AGENT_SERVICE_NAME}.service redis-server.service

[Service]
Type=simple
User=$SERVICE_USER
# M25 Step 4: panel-api listens on /run/jabali-panel/api.sock owned
# jabali:jabali-sockets so nginx (member of jabali-sockets) can connect.
# The User= stays jabali (privileged ops happen via the agent's separate
# socket); only the Group= flips to expose the listen socket to nginx.
Group=jabali-sockets
# When Group= is set explicitly alongside User=, systemd replaces the
# primary GID and does NOT inherit the user's /etc/passwd primary group
# as supplementary. Without this line panel-api runs as
# jabali:jabali-sockets with no \`jabali\` supplementary, and can't read
# its own EnvironmentFile ($ENV_FILE, root:jabali 0640). See
# install/systemd/jabali-kratos.service for the identical fix reasoning.
SupplementaryGroups=$SERVICE_USER systemd-journal www-data
# /run/jabali-panel — systemd creates owned $SERVICE_USER:$SERVICE_USER 0755
# on service start and tears down on stop. The SSO UDS listener binds
# \${runtime}/sso.sock here; unlike /run/jabali (owned by root, used by
# the privileged agent), /run/jabali-panel is safe for the unprivileged
# panel to write to.
RuntimeDirectory=jabali-panel
# M25 Step 4: 0750 (down from 0755) so non-jabali-sockets users can't list
# /run/jabali-panel/. nginx (www-data) is in jabali-sockets and can still
# traverse via group permission. The api.sock file inside is mode 0660,
# pinned by the panel-api listener helper after net.Listen() returns.
RuntimeDirectoryMode=0750
EnvironmentFile=$ENV_FILE
# GH #705 re-applied after the #730 soak: confine the DAEMON under the
# jabali-panel AppArmor profile via aa-exec. The original #705 502 was a
# disconnected-path denial — the unit runs ProtectSystem=strict +
# ReadWritePaths=/run/mysqld, so systemd bind-mounts the socket dir into a
# private mount namespace and AppArmor sees /run/mysqld/mysqld.sock as a
# "disconnected path" and denies connect() (a file-class name-lookup failure
# complain mode can't downgrade → crash-loop). Fixed by
# flags=(attach_disconnected) in the profile; the socket + external-tool rules
# were completed via a live complain soak on mx (2026-07-05) and validated in
# enforce (attr=jabali-panel(enforce), DB OK, 0 denials). The profile is
# name-only so direct CLI (jabali update / repair / apparmor flip-mature, run by
# the operator as root) stays UNCONFINED — only this serve process picks it up.
# The wrapper PROBES \`aa-exec -p jabali-panel -- true\` (needs the /{usr,}/bin/true
# rix rule) and falls back to a plain exec when the profile isn't loaded or
# applicable, so a skip-bias kernel, a container, or a missing aa-exec can never
# stop the daemon from starting. Mode follows the loaded profile: install_apparmor
# sets COMPLAIN on first install; the operator flips enforce via
# \`jabali apparmor flip-mature\` after their own soak window.
ExecStart=/bin/sh -c 'if command -v aa-exec >/dev/null 2>&1 && aa-exec -p jabali-panel -- true 2>/dev/null; then exec aa-exec -p jabali-panel -- $BIN_PATH serve; else exec $BIN_PATH serve; fi'
Restart=on-failure
RestartSec=3
TimeoutStopSec=10

# Hardening (minimal but real)
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictNamespaces=true
RestrictSUIDSGID=true
LockPersonality=true
# MemoryDenyWriteExecute INTENTIONALLY OMITTED — yara-x (M33.2 mailscan)
# JIT-compiles YARA rules to WASM and requires PROT_EXEC mmap pages.
# Defense-in-depth still covered by NoNewPrivileges + ProtectSystem +
# RestrictAddressFamilies + ProtectKernel* above. ADR-0079.
ReadWritePaths=$REPO_DIR /var/lib/jabali-uploads /var/lib/jabali-migrations /var/lib/jabali-panel /var/lib/jabali/restore /etc/jabali-panel/migration-secrets /run/mysqld
# GH #355: systemd creates + chowns /var/lib/jabali-uploads to the panel
# service user on EVERY start, so a drifted owner (e.g. root, from an old
# install) self-heals on a plain restart — not only on \`jabali update\`.
# Uploads failed with "open …/jabali-upload-…: permission denied" when the
# dir was owned by someone the service user couldn't write as.
StateDirectory=jabali-uploads
StateDirectoryMode=0750

[Install]
WantedBy=multi-user.target
EOF

  # Upload staging dir — both panel-api and panel-agent run with
  # PrivateTmp=yes, so /tmp is per-unit and a staged upload written
  # by panel-api is invisible to the agent's files.ingest. The shared
  # /var/lib/jabali-uploads/ lives outside the tmp sandbox so both
  # units see the same on-disk path. Owned by SERVICE_USER 0750 so
  # panel-api can write under ProtectSystem=strict (covered by the
  # ReadWritePaths entry above); agent runs as root and reads without
  # restriction. Same scar story as the app-install staging dir
  # (commands/staging_tmp.go, commit 29823c3).
  install -d -m 0750 -o "$SERVICE_USER" -g "$SERVICE_USER" /var/lib/jabali-uploads

  # DB restore staging (GH #1045): panel-api writes an uploaded .sql dump
  # here (databases.go POST /databases/:id/restore), then the root agent
  # reads + unlinks it (db.restore). Same cross-unit path problem as the
  # upload staging dir above — the panel's ProtectSystem=strict needs the
  # ReadWritePaths entry, and systemd refuses to start the unit when a
  # ReadWritePaths entry doesn't exist, so create it BEFORE the reload.
  # Owned by SERVICE_USER 0700: only the panel writes, the agent is root.
  install -d -m 0700 -o "$SERVICE_USER" -g "$SERVICE_USER" /var/lib/jabali/restore

  # GH #425: reap abandoned upload staging files (chunked uploads with no final
  # chunk) so a tenant can't slowly fill the service partition shared with
  # MariaDB + panel state. systemd-tmpfiles-clean.timer runs daily; 'e' removes
  # files older than the age from the (existing) dir. Active uploads are written
  # on every chunk so their mtime stays young until the upload is abandoned.
  local uploads_reaper=/etc/tmpfiles.d/jabali-uploads-reaper.conf
  local uploads_reaper_rule='e /var/lib/jabali-uploads - - - 12h'
  if [[ ! -f "$uploads_reaper" ]] || ! cmp -s <(printf '%s\n' "$uploads_reaper_rule") "$uploads_reaper"; then
    printf '%s\n' "$uploads_reaper_rule" > "$uploads_reaper"
    systemd-tmpfiles --clean "$uploads_reaper" 2>/dev/null || true
  fi

  systemctl daemon-reload
  systemctl enable --quiet "$AGENT_SERVICE_NAME.service"
  systemctl enable --quiet "$SERVICE_NAME.service"
}

# ---------- step 7: start + smoke test --------------------------------------

# _derive_admin_username mirrors panel-api auth.deriveBootstrapUsername exactly:
# the M54 login username derived from the admin email (local-part, lowercased,
# non [a-z0-9_-] -> _, trimmed of -, must start [a-z_], max 32). Kept in lockstep
# so the installer banner shows the SAME username the operator logs in with
# (GH#175: banner used to print the email, which is no longer the login).
_derive_admin_username() {
  local email="$1" lp
  lp="${email%%@*}"
  lp="$(printf '%s' "$lp" | tr '[:upper:]' '[:lower:]' | sed -E 's/[^a-z0-9_-]/_/g; s/^-+//; s/-+$//')"
  [[ -z "$lp" ]] && lp="admin"
  case "${lp:0:1}" in
    [a-z_]) : ;;
    *) lp="u${lp}" ;;
  esac
  printf '%s' "${lp:0:32}"
}

# ---------- step 6b: seed admin credentials ---------------------------------

# prompt_admin_account — collect the panel admin LOGIN email up-front,
# right after the hostname prompt, so the operator answers every
# interactive question before the long unattended phase instead of being
# surprised by a prompt deep in the run (after crowdsec/etc — the puzzle
# operator hit exactly that). Stores it in JABALI_ADMIN_EMAIL; the late
# seed_admin_env consumes it without re-prompting. No-op when an env
# override is set, when the box was already seeded (re-run/update), or
# when there is no TTY (seed_admin_env applies the no-TTY default later).
prompt_admin_account() {
  [[ -n "${JABALI_BOOTSTRAP_ADMIN_EMAIL:-}${JABALI_ADMIN_EMAIL:-}" ]] && return 0
  [[ -f /etc/jabali/.admin-seeded ]] && return 0
  grep -q '^JABALI_BOOTSTRAP_ADMIN_EMAIL=' "${ENV_FILE:-/nonexistent}" 2>/dev/null && return 0

  local _def_host _def_email _ans _email_re='^[^@[:space:]]+@[^@[:space:]]+\.[^@[:space:]]+$'
  _def_host="${JABALI_HOSTNAME:-$(hostname -f 2>/dev/null || hostname 2>/dev/null || echo localhost)}"
  _def_email="admin@${_def_host}"
  # Non-interactive (TUI installer owns the terminal): use the default email
  # instead of opening /dev/tty, which would block behind the TUI screen.
  if [[ -n "${JABALI_NONINTERACTIVE:-}" ]]; then
    JABALI_ADMIN_EMAIL="$_def_email"
    export JABALI_ADMIN_EMAIL
    _log "panel admin login email set to $JABALI_ADMIN_EMAIL (non-interactive default)"
    return 0
  fi
  if exec 3</dev/tty 2>/dev/null; then
    {
      printf '\n'
      printf '  +-------------------------------------------------------+\n'
      printf '  |  PANEL ADMIN ACCOUNT (first install only)             |\n'
      printf '  +-------------------------------------------------------+\n'
      printf '  This is the email you will use to LOG IN to the jabali\n'
      printf '  web panel at https://%s:8443/\n' "$_def_host"
      printf '  It is NOT an SSL / ACME / Let'"'"'s Encrypt contact address.\n'
      printf '  Press Enter to accept the default.\n\n'
      printf 'Panel admin login email [%s]: ' "$_def_email"
    } > /dev/tty
    read -r _ans <&3 || true
    exec 3<&-
    JABALI_ADMIN_EMAIL="${_ans:-$_def_email}"
    if [[ ! "$JABALI_ADMIN_EMAIL" =~ $_email_re ]]; then
      _warn "admin email '$JABALI_ADMIN_EMAIL' looks invalid — using $_def_email"
      JABALI_ADMIN_EMAIL="$_def_email"
    fi
    export JABALI_ADMIN_EMAIL
    _log "panel admin login email set to $JABALI_ADMIN_EMAIL (applied at first boot)"
  fi
}

seed_admin_env() {
  # The panel-admin account is seeded ONCE, on a genuine first install.
  # A marker (`.admin-seeded`) plus the panel.env var make every later
  # run — including a re-run of this installer or `jabali update --force`
  # on an already-provisioned box — a silent no-op. Without the marker,
  # boxes whose panel.env predates this var (installed before the
  # interactive prompt shipped) would re-prompt on every re-provision;
  # the operator on `puzzle` hit exactly that and mistook the prompt
  # for an SSL/ACME email.
  local _marker="/etc/jabali/.admin-seeded"
  if grep -q '^JABALI_BOOTSTRAP_ADMIN_EMAIL=' "$ENV_FILE" 2>/dev/null \
     || [[ -f "$_marker" ]]; then
    _ok "panel admin already seeded — skipping (re-run / update is a no-op)"
    mkdir -p "$(dirname "$_marker")" 2>/dev/null || true
    : > "$_marker" 2>/dev/null || true
    return
  fi

  # Admin email: env override > interactive /dev/tty prompt > a sane
  # default of admin@<hostname>. NEVER admin@jabali.local — `.local` is
  # an invalid ACME account email AND mismatched bootstrap email caused
  # the mx 502 crash-loop (BootstrapAdmin then re-mints + 409s).
  local _def_host admin_email _email_re='^[^@[:space:]]+@[^@[:space:]]+\.[^@[:space:]]+$'
  _def_host="$(hostname -f 2>/dev/null || hostname 2>/dev/null || echo localhost)"
  admin_email="${JABALI_BOOTSTRAP_ADMIN_EMAIL:-${JABALI_ADMIN_EMAIL:-}}"
  if [[ -z "$admin_email" ]]; then
    local _def_email="admin@${_def_host}"
    if [[ -n "${JABALI_NONINTERACTIVE:-}" ]]; then
      admin_email="$_def_email"
    elif exec 3</dev/tty 2>/dev/null; then
      local _ans
      # Unambiguous wording + a visually separated banner so this is
      # never mistaken for the SSL/ACME contact email that the cert
      # steps just printed right above it.
      {
        printf '\n'
        printf '  +-------------------------------------------------------+\n'
        printf '  |  PANEL ADMIN ACCOUNT (first install only)             |\n'
        printf '  +-------------------------------------------------------+\n'
        printf '  This is the email you will use to LOG IN to the jabali\n'
        printf '  web panel at https://%s:8443/\n' "$_def_host"
        printf '  It is NOT an SSL / ACME / Let'"'"'s Encrypt contact address.\n'
        printf '  Press Enter to accept the default.\n\n'
        printf 'Panel admin login email [%s]: ' "$_def_email"
      } > /dev/tty
      read -r _ans <&3 || true
      exec 3<&-
      admin_email="${_ans:-$_def_email}"
    else
      admin_email="$_def_email"
      _log "no TTY — admin email defaulting to $admin_email (override: JABALI_BOOTSTRAP_ADMIN_EMAIL)"
    fi
  fi
  if [[ ! "$admin_email" =~ $_email_re ]]; then
    _warn "admin email '$admin_email' looks invalid — using admin@${_def_host}"
    admin_email="admin@${_def_host}"
  fi
  local admin_pass
  admin_pass="$(openssl rand -base64 18)"

  _log "seeding panel admin bootstrap credentials (login username: $(_derive_admin_username "$admin_email"), contact email: $admin_email)"
  cat >>"$ENV_FILE" <<EOF

# Admin bootstrap (consumed once on first boot, safe to leave).
JABALI_BOOTSTRAP_ADMIN_EMAIL=$admin_email
JABALI_BOOTSTRAP_ADMIN_PASSWORD=$admin_pass
EOF
  # Drop the marker so no future re-provision re-prompts, even if
  # panel.env is later rewritten.
  mkdir -p "$(dirname "$_marker")" 2>/dev/null || true
  : > "$_marker" 2>/dev/null || true

  # Store the generated password so the final banner can display it.
  JABALI_SEED_EMAIL="$admin_email"
  JABALI_SEED_PASS="$admin_pass"
}

# bootstrap_tenant_env appends JABALI_BOOTSTRAP_TENANT_* to panel.env
# when the operator set them in the install environment. Pattern matches
# seed_admin_env so panel-api reads both via os.Getenv on first boot.
#
# Marker prevents re-write on jabali update — same idempotency model as
# the admin seed.
bootstrap_tenant_env() {
  local _marker="/etc/jabali/.tenant-seeded"
  if grep -q '^JABALI_BOOTSTRAP_TENANT_EMAIL=' "$ENV_FILE" 2>/dev/null \
     || [[ -f "$_marker" ]]; then
    return
  fi
  local tenant_email="${JABALI_BOOTSTRAP_TENANT_EMAIL:-}"
  local tenant_domain="${JABALI_BOOTSTRAP_TENANT_DOMAIN:-}"
  if [[ -z "$tenant_email" ]]; then
    return
  fi
  local tenant_pass="${JABALI_BOOTSTRAP_TENANT_PASSWORD:-}"
  if [[ -z "$tenant_pass" ]]; then
    tenant_pass="$(openssl rand -base64 18)"
    _log "tenant bootstrap: generated random password for $tenant_email (panel log will print it on first boot)"
  fi
  _log "seeding tenant bootstrap: user=$tenant_email domain=${tenant_domain:-<none>}"
  cat >>"$ENV_FILE" <<EOF

# Tenant bootstrap (GH#120 — consumed once on first boot, safe to leave).
JABALI_BOOTSTRAP_TENANT_EMAIL=$tenant_email
JABALI_BOOTSTRAP_TENANT_PASSWORD=$tenant_pass
EOF
  if [[ -n "$tenant_domain" ]]; then
    echo "JABALI_BOOTSTRAP_TENANT_DOMAIN=$tenant_domain" >>"$ENV_FILE"
  fi
  mkdir -p "$(dirname "$_marker")" 2>/dev/null || true
  : > "$_marker" 2>/dev/null || true
  JABALI_TENANT_SEED_EMAIL="$tenant_email"
  JABALI_TENANT_SEED_PASS="$tenant_pass"
  JABALI_TENANT_SEED_DOMAIN="$tenant_domain"
}

start_and_verify_agent() {
  _log "starting $AGENT_SERVICE_NAME"
  systemctl restart "$AGENT_SERVICE_NAME"

  # Give the socket a moment to appear. Agents boot in <100ms usually but
  # we don't want to race.
  local ok=0
  for i in 1 2 3 4 5 6 7 8 9 10; do
    if [[ -S "$AGENT_SOCKET" ]]; then ok=1; break; fi
    sleep 0.3
  done
  if (( ok == 0 )); then
    _warn "agent socket never appeared; dumping last 20 log lines"
    journalctl -u "$AGENT_SERVICE_NAME" -n 20 --no-pager || true
    _die "$AGENT_SERVICE_NAME did not come up"
  fi

  # Sanity-check: socket must be root:jabali 0660 — anything else and the
  # panel won't be able to connect.
  local sock_perms
  sock_perms="$(stat -c '%a %U:%G' "$AGENT_SOCKET")"
  _ok "agent socket ready ($AGENT_SOCKET, perms=$sock_perms)"
}

start_and_verify() {
  _log "starting $SERVICE_NAME"
  systemctl restart "$SERVICE_NAME"

  _log "waiting for /health"
  # M25 Step 4: panel-api now listens on /run/jabali-panel/api.sock.
  # curl --unix-socket reaches it directly; nginx-via-:8443 also works
  # but adds nothing for a localhost smoke. The socket path matches the
  # config seed in write_config_file().
  #
  # First-run migrations can take a while on a fresh InnoDB (45s+
  # observed). Give the service up to 120s before declaring defeat.
  local ok=0
  local deadline=$((SECONDS + 120))
  while (( SECONDS < deadline )); do
    if curl -fsS -m 2 --unix-socket /run/jabali-panel/api.sock http://panel/health >/tmp/jabali-health.json 2>/dev/null; then
      ok=1; break
    fi
    sleep 1
  done

  if (( ok == 0 )); then
    _warn "health probe failed after 120s; dumping last 40 log lines"
    journalctl -u "$SERVICE_NAME" -n 40 --no-pager || true
    _die "$SERVICE_NAME did not come up"
  fi

  _ok "health OK: $(cat /tmp/jabali-health.json)"
  rm -f /tmp/jabali-health.json

  # M25 Step 4 verification: socket must be jabali:jabali-sockets 0660.
  # (Pre-M25 we also asserted no all-interface bind on :8443 — that check
  # was correct when panel-api itself terminated TLS on :8443. Post-M25,
  # nginx owns :8443 as the public-facing TLS terminator and must bind
  # 0.0.0.0 + [::]: by design. panel-api not being on :8443 is implied
  # by it having successfully bound the unix socket above; asserting
  # nothing-on-8443 would fail on every correct install.)
  if ! verify_socket_perms /run/jabali-panel/api.sock jabali jabali-sockets 660; then
    _die "panel-api socket has wrong perms — see message above"
  fi

  # In-place migration: rewrite an existing config.toml's addr from any
  # TCP form (127.0.0.1:8443, 0.0.0.0:8443, [::]:8443, :8443) to the unix
  # form. The legacy PANEL_ADDR default was 0.0.0.0:8443, so installs
  # seeded before M25 have that — the narrower 127.0.0.1-only match
  # (M25 ship 2026-04-23) missed them and panel-api crash-looped on :8443
  # EADDRINUSE (each restart raced its predecessor's TIME_WAIT close).
  # Guarded by "is currently TCP AND not already unix:" so rerunning on
  # a migrated box is a no-op.
  local panel_config="/etc/jabali-panel/config.toml"
  if [[ -f "$panel_config" ]] \
     && grep -qE '^\s*addr\s*=\s*"[^"]*:8443"' "$panel_config" \
     && ! grep -qE '^\s*addr\s*=\s*"unix:' "$panel_config"; then
    _log "migrating config.toml addr from TCP to unix socket (M25 Step 4)"
    sed -i -E 's|^(\s*addr\s*=\s*)"[^"]*:8443"|\1"unix:/run/jabali-panel/api.sock"|' "$panel_config"
    _ok "config.toml addr migrated"
    _log "restarting $SERVICE_NAME after addr migration"
    systemctl restart "$SERVICE_NAME"
  fi
}

# ---------- step: seed last-built-sha --------------------------------------
# Matches the contract in panel-api/cmd/server/update.go: that file tracks
# the SHA of the last fully-rebuilt + restarted commit so `jabali update`
# can tell "no-op, skip rebuild" from "HEAD moved or a prior build failed
# mid-flow, must rebuild". Fresh install is by definition a successful
# build against the current HEAD, so we seed it here.

seed_last_built_sha() {
  local sha
  sha="$(sudo -u "$SERVICE_USER" git -C "$REPO_DIR" rev-parse HEAD 2>/dev/null || true)"
  if [[ -z "$sha" ]]; then
    _warn "could not resolve HEAD in $REPO_DIR; skipping last-built-sha seed"
    return 0
  fi
  # M28 aligned: panel-api writes operator logos into
  # /var/lib/jabali-panel/branding as $SERVICE_USER, so the parent
  # must be owned by $SERVICE_USER too. install -d on an existing
  # dir still applies -o/-g/-m, so this converges whichever of the
  # two install.sh steps runs last.
  install -d -m 0755 -o "$SERVICE_USER" -g "$SERVICE_USER" /var/lib/jabali-panel
  printf '%s\n' "$sha" >/var/lib/jabali-panel/last-built-sha
  chown "$SERVICE_USER:$SERVICE_USER" /var/lib/jabali-panel/last-built-sha
  chmod 0644 /var/lib/jabali-panel/last-built-sha
  _ok "last-built-sha seeded to ${sha:0:7}"
}

# ---------- step: SSO key generation ----------------------------------------


# ---------- nginx WebSocket upgrade map ----

# harden_proc_hidepid mounts /proc with hidepid=2 so a tenant can't read another
# process's /proc/<pid>/cmdline (Gitea #499): several app installers + DB tools
# must pass secrets on the command line because the upstream CLIs offer no
# env/stdin alternative. The privileged agent + systemd run as root and bypass
# hidepid; panel-api (jabali) never reads other procs' /proc, so nothing in the
# panel breaks. Idempotent; persisted via fstab; remount is best-effort
# (unprivileged containers may deny it, in which case fstab applies on reboot).
harden_proc_hidepid() {
  if findmnt -no OPTIONS /proc 2>/dev/null | grep -q 'hidepid'; then
    _ok "/proc hidepid already enabled"
  else
    # Persist: add a /proc fstab line if none exists, else append hidepid=2 to
    # the existing one (guarded against double-adding).
    if ! grep -qE '^[^#]*[[:space:]]/proc[[:space:]]+proc[[:space:]]' /etc/fstab; then
      echo 'proc /proc proc defaults,hidepid=2 0 0' >> /etc/fstab
      _log "added /proc hidepid=2 to /etc/fstab"
    elif ! grep -E '^[^#]*[[:space:]]/proc[[:space:]]+proc[[:space:]]' /etc/fstab | grep -q hidepid; then
      sed -i -E '/^[^#]*[[:space:]]\/proc[[:space:]]+proc[[:space:]]/ s/([[:space:]]proc[[:space:]]+[^[:space:]]+)/\1,hidepid=2/' /etc/fstab
      _log "added hidepid=2 to the existing /proc fstab entry"
    fi
    if mount -o remount,hidepid=2 /proc 2>/dev/null; then
      _ok "/proc hidepid=2 applied (tenants cannot read other processes' cmdline)"
    else
      _warn "/proc hidepid remount denied (container?) — applies on next boot via fstab"
    fi
  fi
}

install_nginx_server_names_hash() {
  # JAB-214: nginx's default server_names_hash_bucket_size is 64 bytes. A
  # 44-character domain plus its `www.` alias overflows that, and nginx -t then
  # fails the whole config with:
  #
  #   could not build server_names_hash, you should increase
  #   server_names_hash_bucket_size: 64
  #
  # The agent's vhost render correctly rolls back, but a MIGRATION then treats
  # the failed domain create as a skip and cascades: no DNS zone, mailbox rows
  # report "domain not found in panel", email never enabled — and the batch
  # still reported success. Long compound and IDN domains are ordinary in
  # migrations, so 64 is simply too small a default to ship with.
  #
  # 128 is the next power of two and costs a few KB of hash table.
  _log "writing nginx server_names_hash_bucket_size (long-domain support)"
  local dst="/etc/nginx/conf.d/06-jabali-server-names-hash.conf"

  # Idempotency guard, same lesson as jabali-ssl-curves.conf: a second
  # server_names_hash_bucket_size at http scope is a hard duplicate-directive
  # error that fails nginx -t and takes the whole web server down. Operators hit
  # this exact overflow and hand-add the directive (the JAB-214 reporter did),
  # so purge it from every OTHER conf.d file before writing ours — drop
  # single-directive files outright, strip just the line from multi-directive
  # ones. Debian's nginx.conf ships it commented out, so that needs no edit.
  local f
  for f in /etc/nginx/conf.d/*.conf; do
    [[ -e "$f" ]] || continue
    [[ "$f" == "$dst" ]] && continue
    grep -q '^[[:space:]]*server_names_hash_bucket_size' "$f" 2>/dev/null || continue
    if [[ $(grep -cvE '^[[:space:]]*(#|$)' "$f") -le 1 ]]; then
      _warn "removing stray server_names_hash_bucket_size file $f (jabali manages this in $dst)"
      rm -f "$f"
    else
      _warn "stripping server_names_hash_bucket_size from $f (jabali manages it in $dst)"
      sed -i '/^[[:space:]]*server_names_hash_bucket_size/d' "$f"
    fi
  done

  printf '%s\n' \
    '# Managed by jabali (JAB-214). Do not edit — rewritten on every update.' \
    '# nginx defaults to 64, which a ~44-char domain plus its www. alias' \
    '# overflows, failing nginx -t for the ENTIRE config.' \
    'server_names_hash_bucket_size 128;' > "$dst"
  chmod 0644 "$dst"

  if command -v nginx >/dev/null 2>&1 && ! nginx -t >/dev/null 2>&1; then
    _warn "nginx -t failed after writing $dst — reverting"
    rm -f "$dst"
    nginx -t 2>&1 | tail -3 >&2 || true
  else
    _ok "server_names_hash_bucket_size 128 active"
  fi
}

install_nginx_ssl_hardening() {
  # OpenSSL 3.5 enabled post-quantum hybrid key exchange (X25519MLKEM768,
  # group 0x11ec) by DEFAULT for TLS 1.3. Cloudflare's origin pull (and
  # some other middleboxes/older clients) don't support it: the TLS 1.3
  # key_share negotiation fails with illegal_parameter and the connection
  # is reset — surfaced to visitors of a CF-proxied site as error 525,
  # even though the origin, cert, DNS and firewall are all fine (two
  # OpenSSL-3.5 peers negotiate PQ happily, so it's invisible from a
  # browser/curl and only breaks the CF<->origin hop). Pin the TLS 1.3
  # groups to classical curves so every vhost stays compatible.
  _log "writing nginx TLS curve hardening (disable OpenSSL 3.5 PQ groups)"
  local dst="/etc/nginx/conf.d/jabali-ssl-curves.conf"

  # Idempotency guard: a second ssl_ecdh_curve at conf.d/http scope is ALWAYS a
  # hard nginx error ("directive is duplicate"), which fails nginx -t and makes
  # the panel report nginx down. Strays appear from an old-named managed file or
  # a hand-patch left during debugging (puzzle 2026-06-10: a manual
  # zz-classical-curves.conf collided with this managed file). jabali owns the
  # curve policy in $dst, so purge ssl_ecdh_curve from every OTHER conf.d file
  # before writing: drop curve-only files outright, strip just the line from
  # multi-directive ones.
  local f
  for f in /etc/nginx/conf.d/*.conf; do
    [[ -f "$f" ]] || continue
    [[ "$f" == "$dst" ]] && continue
    grep -qE '^[[:space:]]*ssl_ecdh_curve' "$f" 2>/dev/null || continue
    # Count directive lines (start with a letter) that are NOT ssl_ecdh_curve.
    # >0 -> the file has other config, so strip only the curve line; else the
    # file is curve-only -> remove it. (grep -qv with a $-alternation is
    # unreliable, so count explicitly.)
    local _other
    _other=$(grep -E '^[[:space:]]*[a-zA-Z]' "$f" | grep -vcE '^[[:space:]]*ssl_ecdh_curve')
    if [[ "${_other:-0}" -gt 0 ]]; then
      sed -i '/^[[:space:]]*ssl_ecdh_curve/d' "$f"
      _warn "stripped stray ssl_ecdh_curve from $f (jabali manages it in $dst)"
    else
      rm -f "$f"
      _warn "removed stray curve-only file $f (jabali manages curves in $dst)"
    fi
  done

  cat > "$dst" <<'CURVEEOF'
# Managed by jabali. Pin TLS 1.3 groups to classical curves: OpenSSL 3.5's
# default post-quantum group (X25519MLKEM768) breaks Cloudflare-fronted
# origins (error 525) and some legacy clients. http{} scope -> all vhosts.
ssl_ecdh_curve X25519:prime256v1:secp384r1;
CURVEEOF
  chmod 0644 "$dst"
  if ! nginx -t 2>&1 | grep -q "successful"; then
    nginx -t 2>&1 >&2 || true
    _die "nginx configuration test failed (ssl curves)"
  fi
  systemctl reload nginx || systemctl restart nginx
  _ok "nginx TLS curve hardening installed: $dst"
}

install_nginx_tunables() {
  # M55 Server Settings -> Nginx. Seed the http{}-scope tunables fragment with
  # the same defaults as migration 000165 / the agent's nginx.tunables.apply
  # render, so a fresh install has the secure/upload-friendly defaults live
  # (client_max_body_size 50m + sane timeouts) before any UI save. The
  # server_tokens/gzip/keepalive/worker knobs live in nginx.conf and are
  # patched there by the agent on first save (Debian's defaults already match
  # ours, so a fresh install needs no nginx.conf edit).
  #
  # SEED-IF-ABSENT only: once an admin edits Server Settings -> Nginx the agent
  # owns this file; install.sh must not clobber their values on `jabali update`.
  # The byte content below matches BuildNginxTunablesFragment's default output
  # so the agent's idempotent compare sees no-change on the first save.
  local dst="/etc/nginx/conf.d/05-jabali-tunables.conf"
  if [[ -f "$dst" ]]; then
    _log "nginx tunables fragment already present; leaving admin-managed $dst alone"
    return 0
  fi
  _log "seeding nginx tunables fragment (Server Settings -> Nginx defaults)"
  cat > "$dst" <<'TUNEOF'
# Auto-generated by jabali — do not edit.
# Server-wide nginx tunables (Server Settings → Nginx, M55).
# http{}-scope defaults; individual vhosts may override per-directive.

client_max_body_size 512m;
client_body_timeout 60s;
client_header_timeout 60s;
send_timeout 60s;
proxy_connect_timeout 300s;
proxy_read_timeout 300s;
proxy_send_timeout 300s;
TUNEOF
  chmod 0644 "$dst"
  if ! nginx -t 2>&1 | grep -q "successful"; then
    nginx -t 2>&1 >&2 || true
    rm -f "$dst"
    _die "nginx configuration test failed (tunables fragment)"
  fi
  systemctl reload nginx || systemctl restart nginx
  _ok "nginx tunables fragment seeded: $dst"
}

install_nginx_websocket_map() {
  _log "installing nginx WebSocket upgrade map snippet"

  local src="${REPO_DIR}/install/nginx/jabali-websocket-map.conf"
  local dst="/etc/nginx/conf.d/jabali-websocket-map.conf"

  if [[ ! -f "$src" ]]; then
    _die "websocket map snippet not found at $src"
  fi

  install -m 0644 "$src" "$dst"

  if ! nginx -t 2>&1 | grep -q "successful"; then
    nginx -t 2>&1 >&2 || true
    _die "nginx configuration test failed (websocket map)"
  fi

  systemctl reload nginx || systemctl restart nginx

  _ok "nginx WebSocket map installed: $dst"
}

# ---------- ADR-0108: per-domain FastCGI micro-cache keyzone ----------
# Ships the shared keyzone (http context). Per-domain vhosts reference
# `fastcgi_cache jabali_fcgi;` only when the domain opts in; the zone
# MUST exist first or `nginx -t` fails. Idempotent; also re-applied on
# every `jabali update` (update.go) — the recurring "jabali update
# doesn't refresh host config" scar (PR#45/#49).
install_nginx_fastcgi_cache() {
  _log "installing nginx FastCGI micro-cache keyzone (ADR-0108)"

  local src="${REPO_DIR}/install/nginx/jabali-fastcgi-cache.conf"
  local dst="/etc/nginx/conf.d/jabali-fastcgi-cache.conf"
  if [[ ! -f "$src" ]]; then
    _die "fastcgi-cache snippet not found at $src"
  fi

  # Cache storage: nginx workers run as www-data; 0700 owner-only is
  # enough (only nginx reads/writes it). install -d is idempotent.
  install -d -m 0700 -o www-data -g www-data /var/cache/nginx/jabali

  install -m 0644 "$src" "$dst"

  if ! nginx -t 2>&1 | grep -q "successful"; then
    nginx -t 2>&1 >&2 || true
    _die "nginx configuration test failed (fastcgi cache keyzone)"
  fi

  systemctl reload nginx || systemctl restart nginx

  _ok "nginx FastCGI cache keyzone installed: $dst"
}

# ---------- M25 Step 4: nginx panel vhost (TLS terminator on :8443) -----

# GH #292: pick the HTTP/2 syntax for the host's nginx. >=1.25.1 wants the
# standalone `http2 on;` directive (and warns on `listen ... http2`); older nginx
# (jammy 1.18 / bookworm 1.22 / noble 1.24) only understands the listen
# parameter. Sets _NGX_H2_PARAM (" http2" or "") + _NGX_H2_DIR ("http2 on;" or "").
_nginx_http2_form() {
  _NGX_H2_PARAM=" http2"
  _NGX_H2_DIR=""
  local ver maj min pat
  ver="$(nginx -v 2>&1 | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1)"
  [[ -z "$ver" ]] && return 0
  IFS=. read -r maj min pat <<<"$ver"
  if (( maj > 1 )) || (( maj == 1 && min > 25 )) || (( maj == 1 && min == 25 && pat >= 1 )); then
    _NGX_H2_PARAM=""
    _NGX_H2_DIR="http2 on;"
  fi
  return 0
}

install_nginx_panel_vhost() {
  _log "installing nginx panel vhost (M25 Step 4 — TLS terminator on :8443)"

  local nginx_sites_dir="/etc/nginx/sites-available"
  local nginx_enabled_dir="/etc/nginx/sites-enabled"
  local panel_vhost_file="${nginx_sites_dir}/jabali-panel.conf"
  local tmpl="${REPO_DIR}/install/nginx/jabali-panel-vhost.conf.tmpl"
  local tls_cert="/etc/jabali/tls/panel.crt"
  local tls_key="/etc/jabali/tls/panel.key"

  if [[ ! -f "$tmpl" ]]; then
    _die "panel vhost template not found at $tmpl"
  fi
  if [[ ! -f "$tls_cert" || ! -f "$tls_key" ]]; then
    _die "TLS cert missing at $tls_cert — provision_tls_cert must run first"
  fi

  # Render the template by substituting ${SSL_CERT_PATH} + ${SSL_KEY_PATH}
  # via sed. envsubst would be cleaner but isn't a dependency we want to
  # add solely for two substitutions.
  _nginx_http2_form
  # JAB-144: /assets/ is served from the on-disk build output (panel-ui/dist),
  # the same tree the panel-api binary embedded, with a proxy fallback.
  local panel_dist_dir="${REPO_DIR}/panel-ui/dist"
  sed \
    -e "s|\${SSL_CERT_PATH}|${tls_cert}|g" \
    -e "s|\${SSL_KEY_PATH}|${tls_key}|g" \
    -e "s|\${NGX_H2_PARAM}|${_NGX_H2_PARAM}|g" \
    -e "s|\${NGX_H2_DIR}|${_NGX_H2_DIR}|g" \
    -e "s|\${PANEL_DIST_DIR}|${panel_dist_dir}|g" \
    "$tmpl" > "$panel_vhost_file"

  if grep -q '\${' "$panel_vhost_file"; then
    _die "unsubstituted \${VAR} placeholders left in $panel_vhost_file — template drift?"
  fi

  # JAB-159 phase 6: on a demo host, block Kratos self-service credential +
  # recovery WRITES. The Go demo middleware only guards /api/v1; /.ory is
  # reverse-proxied to Kratos, so without this a shared-cred demo visitor could
  # change the password (bricking login) or trigger recovery. Reads (viewing the
  # settings page) still work. A regex location wins over the prefix `location /`.
  if [[ "$(_deploy_profile)" == "demo" ]]; then
    local guard_file; guard_file="$(mktemp)"
    cat > "$guard_file" <<'GUARD'
  location ~ ^/\.ory/self-service/(settings|recovery) {
    limit_except GET HEAD OPTIONS { deny all; }
    proxy_pass http://jabali_panel_api;
    proxy_set_header Host $http_host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto https;
    proxy_set_header X-Forwarded-Host $http_host;
    proxy_set_header X-Forwarded-Port $server_port;
    proxy_http_version 1.1;
  }
GUARD
    sed -i -e "/# __JAB159_DEMO_SELFSERVICE_GUARD__/{r ${guard_file}
d}" "$panel_vhost_file"
    rm -f "$guard_file"
    _log "demo profile: nginx blocks Kratos self-service settings/recovery writes"
  else
    sed -i '/# __JAB159_DEMO_SELFSERVICE_GUARD__/d' "$panel_vhost_file"
  fi

  ln -sf "$panel_vhost_file" "${nginx_enabled_dir}/jabali-panel.conf"

  # The vhost template hard-`include`s the phpMyAdmin + Adminer snippets.
  # nginx fails `nginx -t` on a missing literal include, so an optional
  # component that hasn't installed yet (or failed — e.g. phpMyAdmin's CDN
  # was unreachable) would otherwise take the whole :8443 panel vhost down
  # with it (GH #217). Guarantee both include targets exist; an empty file
  # is a no-op include that install_phpmyadmin / install_adminer overwrite
  # with the real block when they run.
  mkdir -p /etc/nginx/sites-available/includes /etc/nginx/snippets
  [[ -f /etc/nginx/sites-available/includes/phpmyadmin.conf ]] || : > /etc/nginx/sites-available/includes/phpmyadmin.conf
  [[ -f /etc/nginx/snippets/jabali-adminer.conf ]] || : > /etc/nginx/snippets/jabali-adminer.conf

  # GH #1146: the panel vhost's /dav/ location references limit_req zone
  # `jabali_webdav`, which is declared at http{} scope — install it into conf.d
  # BEFORE the nginx -t below or the test fails on the unknown zone.
  install -m 0644 "$REPO_DIR/install/nginx/jabali-webdav-ratelimit.conf" /etc/nginx/conf.d/jabali-webdav-ratelimit.conf

  _log "testing nginx configuration"
  if ! nginx -t 2>&1 | grep -q "successful"; then
    nginx -t 2>&1 >&2 || true
    _die "nginx configuration test failed (panel vhost)"
  fi

  _log "reloading nginx"
  systemctl reload nginx || {
    _warn "nginx reload failed; trying restart"
    systemctl restart nginx
  }

  # GH #1146 step 4: enable + start the WebDAV auth_request authenticator. Done
  # here (not in install_jabali_slices) because it runs after build_backend, so
  # the jabali-webdav binary, /run/jabali-webdav (tmpfiles), and the
  # jabali-sockets group are all present. Non-fatal: WebDAV is opt-in per
  # subaccount and must never abort the panel install.
  if [[ -x /usr/local/bin/jabali-webdav ]]; then
    systemctl daemon-reload
    if systemctl enable --now jabali-webdav-auth.service >/dev/null 2>&1; then
      _ok "jabali-webdav auth authenticator enabled"
    else
      _warn "jabali-webdav-auth.service failed to start — WebDAV auth unavailable until fixed (non-fatal)"
    fi
  fi

  _ok "panel nginx vhost installed: ${panel_vhost_file}"
}

# ---------- step 6.4: nginx default vhost for phpMyAdmin SSO -----

install_nginx_default_vhost() {
  _nginx_http2_form
  _log "creating default nginx vhost (80 -> 443 redirect, 443 with panel TLS cert)"

  local nginx_sites_dir="/etc/nginx/sites-available"
  local nginx_enabled_dir="/etc/nginx/sites-enabled"
  local default_vhost_file="${nginx_sites_dir}/jabali-default.conf"
  local tls_cert="/etc/jabali/tls/panel.crt"
  local tls_key="/etc/jabali/tls/panel.key"

  # Sanity: the cert must exist (provision_tls_cert runs earlier in main()).
  if [[ ! -f "$tls_cert" || ! -f "$tls_key" ]]; then
    _die "TLS cert missing: $tls_cert — provision_tls_cert must run first"
  fi

  # GH #860: both location / blocks below include the catch-all mode file.
  # install_disabled_page seeds it earlier in main(); this is the defensive
  # re-seed — nginx -t fails box-wide on a missing include file.
  if [[ ! -f /etc/nginx/jabali-catchall.conf ]]; then
    printf 'return 444;\n' > /etc/nginx/jabali-catchall.conf
    chmod 0644 /etc/nginx/jabali-catchall.conf
  fi

  # Default vhost:
  #   - :80 force-redirects everything to https:// (panel is https-only)
  #   - :443 terminates TLS with the panel's self-signed cert and serves
  #     phpMyAdmin at /phpmyadmin/ (panel itself is on :8443, separate).
  _log "writing ${default_vhost_file}"
  cat > "${default_vhost_file}" << VHOSTEOF
# Jabali default vhost. The panel is https-only — port 80 exists purely
# to redirect any stray http request to https. phpMyAdmin is served on
# :443 alongside the panel (panel runs on :8443 directly, phpMyAdmin is
# fronted by nginx here on :443 using the same self-signed cert).

server {
    listen 80 default_server;
    listen [::]:80 default_server;
    # M24-aware: per-domain vhosts bind explicitly with \`listen
    # \${IPv4}:80\` (when ListenIPv4 is non-empty in the vhost render
    # path), which moves them into nginx's specific-IP listener pool.
    # Wildcard listeners (\`listen 80\`) are NEVER consulted for an
    # IP+port that has at least one specific-IP listener — so without
    # this explicit \${JABALI_SRV_IPV4}:80 default_server line the
    # default vhost would be invisible for traffic to the public IP,
    # and HTTP-01 for the panel hostname would land on whichever
    # tenant vhost happened to be alphabetically first. Render the
    # explicit binding so the panel stays the de-facto default for
    # its public IP. Incident 2026-04-26 on mx.jabali-panel.com.
    listen ${JABALI_SRV_IPV4}:80 default_server;
    server_name _;

    # M32 (ADR-0066): serve LE HTTP-01 challenges for the panel
    # hostname out of /var/www/jabali-panel-acme. The ^~ modifier
    # makes this location take precedence over any future regex
    # locations and over the catch-all return 444 below. Customer
    # domain vhosts have their own ACME location at user-webroot
    # paths and match BEFORE this default block, so this only
    # fires for the panel hostname (and for any stray host that
    # doesn't have its own :80 server block but happens to be
    # validating against this VPS).
    location ^~ /.well-known/acme-challenge/ {
        default_type "text/plain";
        root /var/www/jabali-panel-acme;
        try_files \$uri =404;
    }

    # Catch-all mode (GH #860): the include holds either \`return 444\`
    # (shipped default — close without response so we don't leak "this
    # server runs nginx" to random scanners) or the branded unconfigured-
    # domain page when the admin enables it in Server Settings. The agent
    # rewrites the include; this vhost never changes. Domains with their
    # own vhost match BEFORE this default block, so this only fires for
    # hosts nginx has no server{} for.
    #
    # Scoped to location / instead of server-level so the ^~ ACME
    # location above wins for challenge paths. Server-scoped return
    # would fire in SERVER_REWRITE phase BEFORE FIND_CONFIG and
    # short-circuit the location match (incident 2026-04-26: panel
    # cert HTTP-01 failed on mx.jabali-panel.com because the default
    # vhost's server-level return 444 won the rewrite race).
    location / {
        include /etc/nginx/jabali-catchall.conf;
    }
}

server {
    listen 443 ssl default_server${_NGX_H2_PARAM};
    listen [::]:443 ssl default_server${_NGX_H2_PARAM};
    # GH#135: mirror the \${JABALI_SRV_IPV4}:80 binding above onto :443.
    # Per-domain vhosts bind \`listen \${IPv4}:443 ssl\` (M24), moving them
    # into nginx's specific-IP listener pool. The wildcard \`listen 443 ssl
    # default_server\` is NEVER consulted for an IP+port that already has a
    # specific-IP listener — so without this explicit line an unknown SNI
    # on the public IP (the panel hostname itself, which has no tenant
    # vhost) falls to the alphabetically-first tenant vhost and is served
    # that tenant's self-signed cert instead of the panel's LE cert at
    # \${tls_cert}. Same incident class as 2026-04-26 (then fixed for :80
    # only); the panel hostname showed a 123123.com cert warning on
    # https://<hostname>/ while :8443 served valid LE.
    listen ${JABALI_SRV_IPV4}:443 ssl default_server${_NGX_H2_PARAM};
    ${_NGX_H2_DIR}
    server_name _;

    ssl_certificate     ${tls_cert};
    ssl_certificate_key ${tls_key};
    ssl_protocols       TLSv1.2 TLSv1.3;
    # JAB-69: Mozilla-intermediate ciphers (no 3DES/Sweet32, no weak CBC).
    # Kept in sync with install/nginx/jabali-panel-vhost.conf.tmpl and the
    # per-domain vhost template in panel-agent/internal/commands/domain_create.go.
    ssl_ciphers         ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384:ECDHE-ECDSA-CHACHA20-POLY1305:ECDHE-RSA-CHACHA20-POLY1305:DHE-RSA-AES128-GCM-SHA256:DHE-RSA-AES256-GCM-SHA384;
    ssl_prefer_server_ciphers on;

    access_log /var/log/nginx/default.access.log;
    error_log  /var/log/nginx/default.error.log;

    # phpMyAdmin stays reachable on the panel hostname for admin use.
    # The include's server_name-less /phpmyadmin/ location is matched
    # before the catch-all location / below.
    include /etc/nginx/sites-available/includes/phpmyadmin.conf;

    # M6.4 (ADR-0048): /webmail bounces to the panel-primary domain's
    # Bulwark instance on mail.<hostname>. Target is interpolated here
    # at install.sh render time (heredoc expands \${JABALI_SRV_HOSTNAME});
    # hostname changes propagate on the next install.sh run because the
    # whole default vhost is rewritten unconditionally.
    #
    # No graceful fallback on pre-convergence — the ~30s window where
    # mail.<hostname> isn't yet served is documented in ADR-0048 Decision
    # 4 as acceptable; operators who want a 503 page see M6.4.4 follow-up.
    location = /webmail {
        return 301 https://mail.${JABALI_SRV_HOSTNAME}/;
    }
    location = /webmail/ {
        return 301 https://mail.${JABALI_SRV_HOSTNAME}/;
    }

    # Everything else on an unknown host follows the catch-all mode
    # include (GH #860): \`return 444\` by default, or the branded
    # unconfigured-domain page when enabled. The prior hardcoded
    # behaviour (try_files on /var/www/html → 403) leaked a default
    # vhost for domains without an SSL cert yet and sent users a
    # confusing "403 Forbidden" with the panel's self-signed cert.
    location / {
        include /etc/nginx/jabali-catchall.conf;
    }
}

# GH#135: dedicated :443 vhost for the panel hostname itself. Without a
# server{} of its own the hostname falls to the default block above,
# whose default return-444 location closes the connection with no body --
# browsers show ERR_HTTP2_PROTOCOL_ERROR / "Secure Connection Failed"
# even though the LE cert is correct. Serve a real, admin-replaceable
# landing page from /var/www/${JABALI_SRV_HOSTNAME} (static index.html or
# the admin's own index.php). The panel UI itself stays on :8443.
server {
    listen 443 ssl${_NGX_H2_PARAM};
    listen [::]:443 ssl${_NGX_H2_PARAM};
    listen ${JABALI_SRV_IPV4}:443 ssl${_NGX_H2_PARAM};
    ${_NGX_H2_DIR}
    server_name ${JABALI_SRV_HOSTNAME};

    ssl_certificate     ${tls_cert};
    ssl_certificate_key ${tls_key};
    ssl_protocols       TLSv1.2 TLSv1.3;
    # JAB-69: Mozilla-intermediate ciphers (no 3DES/Sweet32, no weak CBC).
    # Kept in sync with install/nginx/jabali-panel-vhost.conf.tmpl and the
    # per-domain vhost template in panel-agent/internal/commands/domain_create.go.
    ssl_ciphers         ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384:ECDHE-ECDSA-CHACHA20-POLY1305:ECDHE-RSA-CHACHA20-POLY1305:DHE-RSA-AES128-GCM-SHA256:DHE-RSA-AES256-GCM-SHA384;
    ssl_prefer_server_ciphers on;

    root  /var/www/${JABALI_SRV_HOSTNAME};
    index index.php index.html;

    access_log /var/log/nginx/jabali-hostname.access.log;
    error_log  /var/log/nginx/jabali-hostname.error.log;

    # Parity with the default block: phpMyAdmin + webmail stay reachable
    # on the panel hostname for admin use.
    include /etc/nginx/sites-available/includes/phpmyadmin.conf;
    location = /webmail  { return 301 https://mail.${JABALI_SRV_HOSTNAME}/; }
    location = /webmail/ { return 301 https://mail.${JABALI_SRV_HOSTNAME}/; }

    # GH #1161: opt-in Automation API on :443. Empty by default (the API is
    # :8443-only); the agent (nginx.automation_public_set, driven by
    # server_settings.automation_api_public_enabled) fills this with the
    # /api/v1/automation/ proxy when an admin opts in. Seeded empty below so
    # nginx -t never fails on a missing include. Only the HMAC-gated
    # automation tree is ever proxied here; internal endpoints stay :8443-only.
    include /etc/nginx/snippets/jabali-automation-443.conf;

    location / {
        try_files \$uri \$uri/ =404;
    }

    # Admin-supplied index.php (optional). Runs in the shared pma FPM pool
    # (www-data) — the same admin-context pool phpMyAdmin uses.
    location ~ \.php\$ {
        try_files \$uri =404;
        fastcgi_pass unix:/run/php/jabali-pma/fpm.sock;
        fastcgi_index index.php;
        include fastcgi_params;
        fastcgi_param SCRIPT_FILENAME \$request_filename;
    }

    # Never serve dotfiles (ACME for the hostname is handled on :80).
    location ~ /\.(?!well-known) { deny all; }
}
VHOSTEOF

  _ok "default vhost config written"

  # GH #1161: seed the opt-in Automation-API-on-443 include EMPTY. The API
  # stays :8443-only until an admin enables it in Server Settings, at which
  # point the reconciler has the agent (nginx.automation_public_set) rewrite
  # this file with the /api/v1/automation/ proxy location. Created only when
  # absent so a reinstall never clobbers an agent-written ON block (which
  # would silently disable the feature). Its own include line above (and the
  # repair self-heal) guarantee nginx -t has a target.
  local automation443_conf="/etc/nginx/snippets/jabali-automation-443.conf"
  mkdir -p /etc/nginx/snippets
  if [[ ! -f "$automation443_conf" ]]; then
    _log "seeding empty ${automation443_conf} (automation API opt-in, default off)"
    printf '# Managed by jabali-agent (nginx.automation_public_set). Empty = API on :8443 only.\n' > "$automation443_conf"
    chmod 0644 "$automation443_conf"
  fi

  # GH#135: docroot for the panel-hostname landing page served by the
  # dedicated vhost above. Created idempotently; the default index.html is
  # written ONLY when the admin hasn't already dropped their own
  # index.html / index.php — never clobbers admin content.
  local hostname_docroot="/var/www/${JABALI_SRV_HOSTNAME}"
  mkdir -p "$hostname_docroot"
  chown www-data:www-data "$hostname_docroot"
  chmod 0755 "$hostname_docroot"
  if [[ ! -e "${hostname_docroot}/index.html" && ! -e "${hostname_docroot}/index.php" ]]; then
    _log "writing default landing page to ${hostname_docroot}/index.html"
    # Render the branded landing template (install/templates/hostname-landing.html),
    # substituting the panel hostname. Shipped as a file rather than an inline
    # heredoc so the markup (logo SVG + styling) stays maintainable. Falls back
    # to a minimal page if the template is missing (e.g. partial checkout).
    local landing_tmpl="${REPO_DIR}/install/templates/hostname-landing.html"
    if [[ -f "$landing_tmpl" ]]; then
      sed "s/__JABALI_HOSTNAME__/${JABALI_SRV_HOSTNAME}/g" "$landing_tmpl" > "${hostname_docroot}/index.html"
    else
      _warn "landing template missing at $landing_tmpl; writing minimal fallback"
      printf '<!doctype html><meta charset="utf-8"><title>%s</title><h1>%s</h1><p>Control panel on port 8443. <a href="https://%s:8443/">Open control panel</a></p>\n' \
        "${JABALI_SRV_HOSTNAME}" "${JABALI_SRV_HOSTNAME}" "${JABALI_SRV_HOSTNAME}" > "${hostname_docroot}/index.html"
    fi
    chown www-data:www-data "${hostname_docroot}/index.html"
    chmod 0644 "${hostname_docroot}/index.html"
    _ok "default landing page written"
  fi

  # Debian's default nginx.conf includes both `sites-enabled/*` and (since
  # our install_nginx step) `sites-enabled/*.conf`, so we must ensure the
  # stock `default` symlink is removed — otherwise both the stock default
  # vhost and our new jabali-default.conf bind `listen 80 default_server`
  # and nginx -t fails with "duplicate default server".
  if [[ -L "${nginx_enabled_dir}/default" || -e "${nginx_enabled_dir}/default" ]]; then
    _log "removing stock ${nginx_enabled_dir}/default symlink"
    rm -f "${nginx_enabled_dir}/default"
  fi

  # Create symlink (.conf extension so it's picked up by either include pattern).
  _log "creating symlink ${nginx_enabled_dir}/default.conf -> ${default_vhost_file}"
  ln -sf "${default_vhost_file}" "${nginx_enabled_dir}/default.conf"

  # Test nginx configuration
  _log "testing nginx configuration"
  if ! nginx -t 2>&1 | grep -q "successful"; then
    nginx -t 2>&1 >&2 || true
    _die "nginx configuration test failed"
  fi

  # Reload nginx
  _log "reloading nginx"
  systemctl reload nginx || {
    _warn "nginx reload failed; trying restart"
    systemctl restart nginx
  }

  _ok "default nginx vhost installed and activated"
}


# ---------- step 6.5: phpMyAdmin dedicated FPM pool -----

install_phpmyadmin_fpm_pool() {
  _log "installing dedicated FPM pool for phpMyAdmin"

  local pma_user="www-data"
  local pma_pool="pma"
  local pma_phpver="8.4"
  local pma_root="/opt/phpmyadmin/current"

  # GH #217: phpMyAdmin is optional and its CDN is flaky. If it is not
  # installed, do NOT create/start its FPM pool — the pool config sets
  # chdir=/opt/phpmyadmin/current, so jabali-fpm@pma fails ("chdir path does
  # not exist") and the _die at the end of this function would abort the whole
  # install over an optional component (panel :8443 never comes up). Skip the
  # pool, tear down any stale failing instance, and let a later run (once
  # phpMyAdmin is present) install it. Pairs with install_phpmyadmin's
  # graceful CDN-failure skip and the panel vhost's placeholder include.
  if [[ ! -d "$pma_root" ]]; then
    _warn "phpMyAdmin not installed ($pma_root absent) — skipping its FPM pool (panel works without it; re-run after 'jabali update' once phpMyAdmin is reachable)"
    systemctl stop jabali-fpm@pma.service 2>/dev/null || true
    rm -f /etc/php/*/fpm/pool.d/jabali-pma.conf 2>/dev/null || true
    return 0
  fi

  # Create version pin for pma pool
  _log "pinning PHP version for pma pool"
  mkdir -p /etc/jabali-panel/user-phpver
  echo "$pma_phpver" > /etc/jabali-panel/user-phpver/pma
  chmod 0644 /etc/jabali-panel/user-phpver/pma
  _ok "PHP version pinned: $pma_phpver"

  # Create pool directory for FPM config
  mkdir -p /etc/php/${pma_phpver}/fpm/pool.d
  chmod 0755 /etc/php/${pma_phpver}/fpm/pool.d

  # Write pool config: jabali-pma.conf
  _log "writing pool config for jabali-pma"
  cat > /etc/php/${pma_phpver}/fpm/pool.d/jabali-pma.conf <<'POOLEOF'
[jabali-pma]
user = www-data
group = www-data
listen = /run/php/jabali-pma/fpm.sock
listen.owner = www-data
listen.group = www-data
listen.mode = 0660
pm = ondemand
pm.max_children = 10
pm.process_idle_timeout = 60s
chdir = /opt/phpmyadmin/current
security.limit_extensions = .php

; phpMyAdmin needs access to its own code, /tmp for sessions; the
; same pool also serves Adminer at /var/www/jabali-adminer (M37).
; sso.key is out of scope — creds via the UDS SSO validator only.
php_admin_value[open_basedir] = /opt/phpmyadmin:/var/www/jabali-adminer:/tmp:/var/tmp

; Import limits (GH #285): the PHP defaults (upload_max_filesize=2M,
; post_max_size=8M, memory_limit=128M, max_execution_time=30s) make
; phpMyAdmin/Adminer SQL-file uploads fail on anything but tiny dumps.
; Raise them so normal database imports work; very large dumps can still
; use phpMyAdmin's chunked UploadDir.
php_admin_value[upload_max_filesize] = 256M
php_admin_value[post_max_size] = 256M
php_value[memory_limit] = 512M
php_value[max_execution_time] = 600
php_value[max_input_time] = 600
POOLEOF
  chmod 0644 /etc/php/${pma_phpver}/fpm/pool.d/jabali-pma.conf
  _ok "pool config written"

  # Remove any stale jabali-pma pool left in a different PHP version's
  # pool.d (e.g. an 8.5 pool from before the phpMyAdmin PHP pin moved to
  # 8.4). phpMyAdmin 5.2.x cannot run on PHP 8.5 (GH#111); a leftover pool
  # would let the FPM master keep serving the wrong version after the pin
  # changes. Keep only the pinned version's pool.
  local _stale_pool
  for _stale_pool in /etc/php/*/fpm/pool.d/jabali-pma.conf; do
    [[ -e "$_stale_pool" ]] || continue
    [[ "$_stale_pool" == "/etc/php/${pma_phpver}/fpm/pool.d/jabali-pma.conf" ]] && continue
    rm -f "$_stale_pool"
    _ok "removed stale pma pool: $_stale_pool"
  done

  # Write per-pool FPM master config: /etc/jabali-panel/fpm/pma.conf
  _log "writing per-pool FPM master config"
  mkdir -p /etc/jabali-panel/fpm
  cat > /etc/jabali-panel/fpm/pma.conf <<FPMEOF
[global]
pid = /run/php/jabali-pma/fpm.pid
error_log = /var/log/php-fpm-pma.log
daemonize = no
include=/etc/php/${pma_phpver}/fpm/pool.d/jabali-pma.conf
FPMEOF
  chmod 0644 /etc/jabali-panel/fpm/pma.conf
  _ok "per-pool FPM master config written"

  # Pre-create the FPM error log file with www-data ownership
  _log "pre-creating FPM error log"
  if [[ ! -e "/var/log/php-fpm-pma.log" ]]; then
    install -m 0640 -o www-data -g www-data /dev/null /var/log/php-fpm-pma.log
  else
    chown www-data:www-data /var/log/php-fpm-pma.log
    chmod 0640 /var/log/php-fpm-pma.log
  fi
  _ok "FPM error log pre-created"

  # Create systemd drop-in for the FPM service (sets Slice)
  _log "creating systemd drop-in for jabali-fpm@pma.service"
  mkdir -p /etc/systemd/system/jabali-fpm@pma.service.d
  cat > /etc/systemd/system/jabali-fpm@pma.service.d/slice.conf <<DROPINEOF
[Service]
User=www-data
Group=www-data
ExecStart=
ExecStart=/usr/sbin/php-fpm${pma_phpver} --nodaemonize --fpm-config=/etc/jabali-panel/fpm/pma.conf
SyslogIdentifier=php-fpm-pma
Slice=jabali.slice
DROPINEOF
  chmod 0644 /etc/systemd/system/jabali-fpm@pma.service.d/slice.conf
  _ok "systemd drop-in created"

  # Reload systemd and start the service
  _log "reloading systemd daemon"
  systemctl daemon-reload

  _log "starting and verifying jabali-fpm@pma service"
  systemctl reset-failed jabali-fpm@pma.service 2>/dev/null || true
  systemctl enable jabali-fpm@pma.service
  systemctl restart jabali-fpm@pma.service

  # Poll for the FPM socket. ondemand mode still creates the listening
  # socket at master start, but there's a race between systemctl returning
  # and FPM finishing pool initialization. Observed on LXC containers
  # where FPM takes ~5s after "Started" to bind the socket; 5s of polling
  # (the old budget) was clipping the race. 30s gives cold-start LXC
  # hosts headroom without meaningfully delaying healthy installs
  # (fast hosts break out on tries 1-5).
  local sock="/run/php/jabali-pma/fpm.sock"
  local tries=0
  while (( tries < 120 )); do
    [[ -S "$sock" ]] && break
    sleep 0.25
    tries=$((tries + 1))
  done
  if [[ ! -S "$sock" ]]; then
    _warn "FPM socket $sock was not created — dumping status for diagnosis:"
    systemctl status jabali-fpm@pma.service --no-pager -l | sed 's/^/  /' >&2 || true
    journalctl -u jabali-fpm@pma.service -n 30 --no-pager | sed 's/^/  /' >&2 || true
    _die "jabali-fpm@pma failed to create socket $sock"
  fi

  _ok "phpMyAdmin FPM pool (jabali-pma) installed and running"
}

# ---------- step 6.4: Adminer SSO bridge -------------------------------------
# M37 Phase 4. Single-PHP-file Adminer drop with the jabali-sso plugin.
# Reuses the jabali-pma FPM pool (www-data) so the existing Unix socket at
# /run/jabali-panel/sso.sock is reachable. nginx vhost lives at
# /jabali-adminer/ on the panel hostname (same as phpMyAdmin's /phpmyadmin).
install_adminer() {
  _log "installing Adminer (multi-engine DB admin) — M37 Phase 4"

  local adminer_dir="/var/www/jabali-adminer"
  local adminer_url="https://github.com/vrana/adminer/releases/download/v4.8.1/adminer-4.8.1.php"
  local adminer_plugin_url="https://raw.githubusercontent.com/vrana/adminer/v4.8.1/plugins/plugin.php"

  mkdir -p "${adminer_dir}"

  # Upstream single-file Adminer build. Pin v4.8.1 for reproducibility.
  #
  # Checksum-verified like every other third-party artifact in this file
  # (wp-cli, phpMyAdmin, Stalwart, Kratos, Bulwark, maldet, yara-x). Adminer is
  # served on the panel vhost behind jabali SSO with full database access, so a
  # tampered or swapped upstream asset is arbitrary PHP running as www-data
  # with the operator's DB credentials — and nothing in install or CI would
  # notice. Download to a temp path first so a mismatch can never leave a
  # partially-verified file in the docroot.
  if [[ ! -f "${adminer_dir}/adminer.php" ]]; then
    _log "downloading adminer.php"
    local adminer_tmp
    adminer_tmp="$(mktemp)"
    if ! curl -fsSL --retry 4 --retry-delay 2 --retry-connrefused -o "$adminer_tmp" "${adminer_url}"; then
      rm -f "$adminer_tmp"
      _err "failed to download adminer from ${adminer_url}"
      return 1
    fi
    local adminer_expected adminer_actual
    adminer_expected="$(grep -v '^#' "${REPO_DIR}/install/adminer.sha256" | awk '{print $1}')"
    adminer_actual="$(sha256sum "$adminer_tmp" | awk '{print $1}')"
    if [[ -z "$adminer_expected" || "$adminer_expected" != "$adminer_actual" ]]; then
      rm -f "$adminer_tmp"
      _err "adminer checksum mismatch: expected ${adminer_expected:-<missing pin>}, got $adminer_actual"
      return 1
    fi
    mv "$adminer_tmp" "${adminer_dir}/adminer.php"
    _ok "adminer.php checksum verified"
  else
    _ok "adminer.php already present"
  fi

  # Adminer's plugin loader (separate from the main file). Checksum-verified
  # too: it is loaded by adminer.php, so it executes with the same privileges.
  if [[ ! -f "${adminer_dir}/plugin.php" ]]; then
    _log "downloading Adminer plugin loader"
    local plugin_tmp
    plugin_tmp="$(mktemp)"
    if ! curl -fsSL --retry 4 --retry-delay 2 --retry-connrefused -o "$plugin_tmp" "${adminer_plugin_url}"; then
      # Non-fatal: the plugin loader only enhances Adminer (jabali-sso plugin).
      # A transient GitHub-raw hiccup shouldn't brick the whole install; the DB
      # admin tool still works, and `jabali update` / repair can refetch it.
      rm -f "$plugin_tmp"
      _warn "failed to download adminer plugin loader (non-fatal) — Adminer SSO plugin disabled until next update"
    else
      local plugin_expected plugin_actual
      plugin_expected="$(grep -v '^#' "${REPO_DIR}/install/adminer-plugin.sha256" | awk '{print $1}')"
      plugin_actual="$(sha256sum "$plugin_tmp" | awk '{print $1}')"
      if [[ -z "$plugin_expected" || "$plugin_expected" != "$plugin_actual" ]]; then
        # A MISMATCH is not the same as a failed download: the file served is
        # not the pinned one, so refuse it rather than degrade quietly.
        rm -f "$plugin_tmp"
        _warn "adminer plugin loader checksum mismatch (expected ${plugin_expected:-<missing pin>}, got $plugin_actual) — refusing it; SSO plugin disabled"
      else
        mv "$plugin_tmp" "${adminer_dir}/plugin.php"
        _ok "adminer plugin loader checksum verified"
      fi
    fi
  else
    _ok "adminer plugin loader already present"
  fi

  # Drop our index.php + jabali-sso plugin from the repo.
  install -m 0644 "${REPO_DIR}/install/adminer/index.php"            "${adminer_dir}/index.php"
  install -m 0644 "${REPO_DIR}/install/adminer/jabali-sso-plugin.php" "${adminer_dir}/jabali-sso-plugin.php"

  chown -R www-data:www-data "${adminer_dir}"
  chmod 0755 "${adminer_dir}"

  # nginx location block — same vhost as phpMyAdmin (jabali-panel-vhost.conf).
  local nginx_inc_dir="/etc/nginx/snippets"
  cat > "${nginx_inc_dir}/jabali-adminer.conf" <<'NGINXEOF'
# Jabali Adminer (M37 Phase 4) — engine-aware DB admin via SSO.
# `^~` prefix wins over the SPA `location /` catch-all. `root`
# (not alias) keeps $document_root in scope so FPM
# SCRIPT_FILENAME = $document_root$fastcgi_script_name resolves
# correctly. The earlier alias+regex variant 502'd with PHP's
# "No input file specified".
location ^~ /jabali-adminer/ {
    root /var/www;
    index index.php;
    try_files $uri $uri/ /jabali-adminer/index.php?$args;

    location ~ ^/jabali-adminer/.+\.php$ {
        root /var/www;
        fastcgi_pass unix:/run/php/jabali-pma/fpm.sock;
        include fastcgi_params;
        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
        fastcgi_param PATH_INFO $fastcgi_path_info;
        fastcgi_read_timeout 60s;
    }
}
NGINXEOF

  # Snippet wiring into the panel vhost is handled by the template
  # render in install_nginx_panel_vhost (jabali-panel-vhost.conf.tmpl
  # has explicit `include` lines for both phpmyadmin.conf and
  # jabali-adminer.conf). No sed-into-installed-conf needed; the next
  # `jabali update` rerenders the vhost.

  if nginx -t >/dev/null 2>&1; then
    systemctl reload nginx 2>/dev/null || true
    _ok "Adminer installed at /jabali-adminer/"
  else
    _warn "nginx -t failed after Adminer install — review /etc/nginx/snippets/jabali-adminer.conf"
  fi
}

# ---------- step 6.5: wp-cli provisioning ------------------------------------

install_wp_cli() {
  _log "installing wp-cli"

  # Version pin — must match the checksum file below.
  # Update both when upgrading wp-cli.
  local wp_version="2.12.0"
  local wp_root="/opt/wp-cli"
  local wp_phar="${wp_root}/wp-cli-${wp_version}.phar"
  local wp_link="${wp_root}/current"
  local wp_archive="/tmp/wp-cli-${wp_version}.phar"

  # Ensure the root directory exists
  mkdir -p "$wp_root"
  chmod 0755 "$wp_root"

  # Download + verify the phar only when the pinned version isn't already on
  # disk. The old idempotency guard skipped the whole function whenever the
  # symlinks merely EXISTED (-L), so a dangling/mispointed `current` or
  # /usr/local/bin/wp from a partial earlier install was never repaired —
  # `wp` resolved to a missing target (GH #298). Download is gated on the phar;
  # the symlinks are always re-pointed below.
  if [[ ! -f "$wp_phar" ]]; then
    _log "downloading wp-cli $wp_version"
    if ! curl -fsSL -o "$wp_archive" \
      "https://github.com/wp-cli/wp-cli/releases/download/v${wp_version}/wp-cli-${wp_version}.phar"; then
      _die "failed to download wp-cli $wp_version phar"
    fi
    _log "verifying wp-cli checksum"
    local expected_sum
    expected_sum="$(grep -v '^#' "${REPO_DIR}/install/wp-cli.sha256" | awk '{print $1}')"
    local actual_sum
    actual_sum="$(sha256sum "$wp_archive" | awk '{print $1}')"
    if [[ "$expected_sum" != "$actual_sum" ]]; then
      rm -f "$wp_archive"
      _die "wp-cli checksum mismatch: expected $expected_sum, got $actual_sum"
    fi
    _ok "checksum verified"
    mv "$wp_archive" "$wp_phar"
    chmod 0755 "$wp_phar"
  else
    _ok "wp-cli $wp_version phar already present"
  fi

  # ALWAYS (re)point the symlinks — idempotent and self-healing for a missing
  # or dangling `current` / /usr/local/bin/wp (GH #298). ln -sfn forces the
  # replacement even when the link already exists or points at a directory.
  ln -sfn "$wp_phar" "$wp_link"
  ln -sfn "$wp_link" /usr/local/bin/wp

  _ok "wp-cli $wp_version installed; wp -> $wp_phar"
}

# ensure_snuffleupagus_bundle_synced — mirror the repo rule bundle into
# /usr/share so SnuffleupagusReconciler (which prefers /usr/share and
# only falls back to the repo when /usr/share is ABSENT) renders the
# CURRENT rules on an existing host. install_snuffleupagus does this on
# fresh installs only; without this provision heal a shipped rule change
# (e.g. the phar:// wrappers_whitelist fix) never reaches existing hosts
# on `jabali update`. panel-api restart later in the update re-runs the
# reconciler, regenerating active.rules from the refreshed bundle.
# Args: $1 repo rules dir, $2 /usr/share bundle dir (defaults wired for
# prod; parameterised for sandbox unit testing).
ensure_snuffleupagus_bundle_synced() {
  local src="${1:-${REPO_DIR:-/opt/jabali-panel}/install/snuffleupagus/rules}"
  local dst="${2:-/usr/share/jabali/snuffleupagus/rules}"
  [[ -d "$src" ]] || return 0
  mkdir -p "$dst"
  local changed=0 f base
  for f in "$src"/*.rules; do
    [[ -e "$f" ]] || continue
    base="$(basename "$f")"
    if [[ ! -f "$dst/$base" ]] || ! cmp -s "$f" "$dst/$base"; then
      install -m 0644 "$f" "$dst/$base" && changed=1
    fi
  done
  if [[ -f "$src/README.md" ]]; then
    install -m 0644 "$src/README.md" "$dst/README.md" 2>/dev/null || true
  fi
  [[ "$changed" == "1" ]] && _log "snuffleupagus rule bundle re-synced to $dst"
  return 0
}

# ensure_wpcli_symlink — (re)create the PATH-visible wp symlink.
# install_wpcli's idempotency guard only checks /opt/wp-cli/{phar,
# current}; if the external <bindir>/wp link is deleted (uninstall
# residue, manual rm) it early-returns and never rebuilds it, and
# install_wpcli isn't called from provision_new_software so a plain
# `jabali update` can't self-heal -> every app install fails at
# `wp core download` ("Failed to find executable wp"). This heal IS
# called from provision so existing hosts recover on update.
# Args: $1 wp_root (default /opt/wp-cli), $2 bindir (default
# /usr/local/bin) — parameterised for sandbox unit testing.
ensure_wpcli_symlink() {
  local wp_root="${1:-/opt/wp-cli}"
  local bindir="${2:-/usr/local/bin}"
  local cur="$wp_root/current"
  # Heal a missing/dangling /opt/wp-cli/current, not just the outer PATH link.
  # `-e` follows the symlink, so this fires when `current` is absent OR points at
  # a removed phar. Repoint it to the newest pinned phar; if no phar exists
  # either, re-provision wp-cli fully. Previously this bailed here, so a host
  # that lost /opt/wp-cli/current could never self-heal on update and wp stayed
  # broken on the host AND inside the bubblewrap SSH sandbox that ro-binds it
  # (GH #298).
  if [[ ! -e "$cur" ]]; then
    local phar
    phar="$(ls -1 "$wp_root"/wp-cli-*.phar 2>/dev/null | sort -V | tail -1)"
    if [[ -n "$phar" && -f "$phar" ]]; then
      ln -sf "$phar" "$cur"
      _log "healed wp-cli: $cur -> $phar (current was missing)"
    elif [[ "$wp_root" == "/opt/wp-cli" ]] && declare -f install_wp_cli >/dev/null 2>&1; then
      _log "wp-cli phar missing under $wp_root — reinstalling"
      install_wp_cli   # re-downloads + recreates current + the outer link
      return 0
    else
      return 0
    fi
  fi
  if [[ -L "$bindir/wp" && "$(readlink "$bindir/wp")" == "$cur" ]]; then
    return 0
  fi
  mkdir -p "$bindir"
  ln -sf "$cur" "$bindir/wp"
  _log "healed wp-cli PATH symlink: $bindir/wp -> $cur"
}

# ---------- step 7: phpMyAdmin + SSO support --------------------------------

install_phpmyadmin() {
  _log "installing phpMyAdmin with SSO support"

  # Version pin — must match the checksum file below.
  # Update both when upgrading phpMyAdmin.
  local pma_version="5.2.3"
  local pma_root="/opt/phpmyadmin"
  local pma_extract="${pma_root}/phpMyAdmin-${pma_version}-all-languages"
  local pma_link="${pma_root}/current"
  local pma_archive="/tmp/phpMyAdmin-${pma_version}-all-languages.tar.gz"

  # Serialize the whole function behind an flock (GH #545/#574 concurrent
  # re-entry class): install.sh's own call and the panel agent re-entering via
  # provision_new_software both reach install_phpmyadmin, and race on the shared
  # /tmp/phpMyAdmin-*.tar.gz download + the /opt/phpmyadmin extract. The losing
  # racer's `tar -xzf` reads a tarball the winner's `rm -f "$pma_archive"` has
  # already removed -> `tar` exit 2 -> `set -e` kills the install (same failure
  # as install_bulwark #574 / install_stalwart #555). The idempotency check
  # below then makes the second caller a fast no-op. Best-effort: proceed
  # unlocked if flock is unavailable.
  local _pma_lockfd=""
  if command -v flock >/dev/null 2>&1; then
    exec {_pma_lockfd}>/run/lock/jabali-phpmyadmin-install.lock 2>/dev/null || _pma_lockfd=""
    if [[ -n "$_pma_lockfd" ]]; then
      flock -w 600 "$_pma_lockfd" \
        || _warn "waited 600s for the phpMyAdmin install lock -- proceeding without exclusion"
    fi
  fi

  # Idempotency: if already extracted, skip the download + extract.
  if [[ -d "$pma_extract" && -L "$pma_link" ]]; then
    _ok "phpMyAdmin $pma_version already installed at $pma_root"
    # Still need to ensure config.inc.php and sso.php are in place
    # (they may have been missing in an older install run).
  else
    # Ensure the root directory exists
    mkdir -p "$pma_root"
    chmod 0755 "$pma_root"

    # Download the tarball. files.phpmyadmin.net's CDN occasionally
    # closes the TLS connection mid-transfer (OpenSSL errno 0,
    # "unexpected eof while reading"). --retry covers that class of
    # transient failure; --retry-all-errors (curl 7.71+) means we also
    # retry on HTTP 5xx and non-network glitches. --max-time 300 caps
    # total wall-time so the installer doesn't stall forever on a
    # dead upstream.
    _log "downloading phpMyAdmin $pma_version"
    if ! curl -fsSL --retry 5 --retry-delay 3 --retry-all-errors \
         --max-time 300 -o "$pma_archive" \
         "https://files.phpmyadmin.net/phpMyAdmin/${pma_version}/phpMyAdmin-${pma_version}-all-languages.tar.gz"; then
      # Non-fatal: phpMyAdmin is optional and its CDN is flaky. Don't abort
      # the whole install (and leave the panel down) over it — skip it,
      # keep the placeholder include so the :8443 vhost stays valid, and
      # let a later `jabali update` retry once upstream is reachable.
      _warn "failed to download phpMyAdmin $pma_version after 5 retries — skipping (panel works without it; re-run 'jabali update' once https://www.phpmyadmin.net/downloads/ is reachable)"
      mkdir -p /etc/nginx/sites-available/includes
      [[ -f /etc/nginx/sites-available/includes/phpmyadmin.conf ]] || : > /etc/nginx/sites-available/includes/phpmyadmin.conf
      [[ -n "$_pma_lockfd" ]] && exec {_pma_lockfd}>&- || true
      return 0
    fi

    # Verify checksum
    _log "verifying phpMyAdmin checksum"
    local expected_sum
    expected_sum="$(grep -v '^#' "${REPO_DIR}/install/phpmyadmin.sha256" | head -1)"
    local actual_sum
    actual_sum="$(sha256sum "$pma_archive" | awk '{print $1}')"
    if [[ "$expected_sum" != "$actual_sum" ]]; then
      rm -f "$pma_archive"
      _die "phpMyAdmin checksum mismatch: expected $expected_sum, got $actual_sum"
    fi
    _ok "checksum verified"

    # Extract
    _log "extracting phpMyAdmin to $pma_root"
    tar -C "$pma_root" -xzf "$pma_archive"
    rm -f "$pma_archive"

    # Create symlink for easy access
    rm -f "$pma_link"
    ln -s "$pma_extract" "$pma_link"
    _ok "phpMyAdmin extracted and symlinked"
  fi

  # --- jabali patch (GH#111): PHP 8.4-safe phpMyAdmin DI container ---
  # phpMyAdmin 5.2.3 bundles symfony/dependency-injection 5.4, whose
  # ContainerConfigurator DSL (driven by libraries/services_loader.php from
  # Core::getContainerBuilder) defers service registration to configurator
  # __destruct. On PHP 8.4+ that drops service definitions (notably "config")
  # -> ServiceNotFoundException, breaking phpMyAdmin entirely. 5.2.3 is the
  # latest release and symfony 5.4 is EOL, so there is no upstream fix.
  # Rewrite getContainerBuilder to register via the direct ContainerBuilder
  # API (eager, unaffected by the __destruct timing change). Re-applied every
  # run because the extract above replaces Core.php; idempotent via marker.
  _log "patching phpMyAdmin DI for PHP 8.4 (GH#111)"
  local pma_core="${pma_link}/libraries/classes/Core.php"
  local pma_patcher
  pma_patcher="$(mktemp --tmpdir jabali-pma-patch.XXXXXX.php)"
  cat > "$pma_patcher" <<'PHPPATCH'
<?php
$p = $argv[1] ?? '';
if ($p === '' || !is_file($p)) { fwrite(STDERR, "Core.php not found\n"); exit(2); }
$s = file_get_contents($p);
if (strpos($s, 'jabali patch (GH#111)') !== false) { exit(0); }
$old = <<<'OLD'
        $containerBuilder = new ContainerBuilder();
        $loader = new PhpFileLoader($containerBuilder, new FileLocator(ROOT_PATH . 'libraries'));
        $loader->load('services_loader.php');

        return $containerBuilder;
OLD;
$new = <<<'NEW'
        $containerBuilder = new ContainerBuilder();
        // jabali patch (GH#111): build via the direct ContainerBuilder API
        // instead of the ContainerConfigurator DSL (PhpFileLoader +
        // services_loader.php). The DSL defers registration to configurator
        // __destruct, which on PHP 8.4+ drops service definitions (e.g.
        // "config") -> ServiceNotFoundException. Direct API registers eagerly.
        foreach (['libraries/services.php', 'libraries/services_controllers.php'] as $jabaliServicesFile) {
            $jabaliServices = include ROOT_PATH . $jabaliServicesFile;
            foreach ($jabaliServices['services'] as $jabaliName => $jabaliService) {
                if (is_string($jabaliService)) {
                    $containerBuilder->setAlias($jabaliName, new \Symfony\Component\DependencyInjection\Alias($jabaliService, true));
                    continue;
                }
                $jabaliDef = new \Symfony\Component\DependencyInjection\Definition($jabaliService['class'] ?? null);
                $jabaliDef->setPublic(true);
                if (isset($jabaliService['arguments'])) {
                    $jabaliArgs = [];
                    foreach ($jabaliService['arguments'] as $jabaliArg) {
                        $jabaliArgs[] = (is_string($jabaliArg) && isset($jabaliArg[0]) && $jabaliArg[0] === '@')
                            ? new \Symfony\Component\DependencyInjection\Reference(substr($jabaliArg, 1))
                            : $jabaliArg;
                    }
                    $jabaliDef->setArguments($jabaliArgs);
                }
                if (isset($jabaliService['factory'])) {
                    $jabaliDef->setFactory($jabaliService['factory']);
                }
                $containerBuilder->setDefinition($jabaliName, $jabaliDef);
            }
        }

        return $containerBuilder;
NEW;
if (substr_count($s, $old) !== 1) { fwrite(STDERR, "getContainerBuilder anchor not found/unique\n"); exit(1); }
file_put_contents($p, str_replace($old, $new, $s));
exit(0);
PHPPATCH
  if ! php "$pma_patcher" "$pma_core"; then
    rm -f "$pma_patcher"
    _die "failed to patch phpMyAdmin Core.php (GH#111)"
  fi
  rm -f "$pma_patcher"
  if ! php -l "$pma_core" >/dev/null 2>&1; then
    _die "phpMyAdmin Core.php patch produced invalid PHP (GH#111)"
  fi
  _ok "phpMyAdmin DI patched for PHP 8.4 (GH#111)"

  # Write config.inc.php (idempotent: overwrite on every run to stay in sync with code)
  _log "writing phpMyAdmin config.inc.php"
  cat > "${pma_link}/config.inc.php" <<'CONFIGEOF'
<?php
/**
 * phpMyAdmin configuration file (auto-generated by install.sh).
 *
 * This config uses phpMyAdmin's signon authentication mode, which expects
 * the frontend to populate the SignonSession with MySQL credentials and
 * redirect to index.php. The SSO handler (sso.php) does this.
 */

// Authentication method
$cfg['Servers'][1]['auth_type'] = 'signon';

// SSO handler endpoint (relative to phpMyAdmin root)
$cfg['Servers'][1]['SignonURL'] = '/phpmyadmin/sso.php';

// Session name used for signon credentials
$cfg['Servers'][1]['SignonSession'] = 'SignonSession';

// Disable the login form (we use SSO only)
$cfg['Servers'][1]['SignonLogoutURL'] = '/logout';

// MySQL connection details
// Note: sso.php will override these with the per-user values from the panel API.
// These defaults are NOT used for authentication; they're fallbacks.
// M25.1: default to Unix socket so that even without SSO (e.g. a direct
// /phpmyadmin/index.php visit) phpMyAdmin's connect path matches what
// skip-networking in my.cnf permits. Port kept for wire-level
// compatibility with older signon plugins; with connect_type=socket
// phpMyAdmin ignores it.
$cfg['Servers'][1]['host'] = 'localhost';
$cfg['Servers'][1]['port'] = 3306;
$cfg['Servers'][1]['connect_type'] = 'socket';
$cfg['Servers'][1]['socket'] = '/var/run/mysqld/mysqld.sock';
$cfg['Servers'][1]['compress'] = false;

// No control connection. phpMyAdmin uses the "controluser" for its
// optional pmadb features (bookmarks, history, designer, etc.) — we
// disable pmadb below, so no second connection is needed. Leaving
// controluser = 'root' here would make phpMyAdmin try to authenticate
// as root@localhost on every page load and surface "Access denied for
// user 'root'@'localhost'" + "Connection for controluser failed"
// banners, even on SSO sessions that work fine for the data
// connection. Omitting these keys entirely makes PMA skip it.

// Allow no password (some test/dev servers may have unprotected root)
$cfg['Servers'][1]['AllowNoPassword'] = false;

// Appearance
$cfg['PmaAbsoluteUri'] = 'https://' . $_SERVER['HTTP_HOST'] . '/phpmyadmin/';
$cfg['Servers'][1]['only_db'] = '';

// Session settings
$cfg['SessionSavePath'] = '/tmp';
$cfg['SendErrorReports'] = 'always';
$cfg['ErrorHandler'] = 'default';

// Allow extensions (for bookmarks, query history, etc.)
$cfg['Servers'][1]['pmadb'] = false;  // Disable to avoid per-user pma__* tables
$cfg['Servers'][1]['bookmarktable'] = false;
$cfg['Servers'][1]['relation'] = false;
$cfg['Servers'][1]['table_info'] = false;
$cfg['Servers'][1]['table_coords'] = false;
$cfg['Servers'][1]['pdf_pages'] = false;
$cfg['Servers'][1]['column_info'] = false;
$cfg['Servers'][1]['history'] = false;
$cfg['Servers'][1]['recent'] = false;
$cfg['Servers'][1]['favorite'] = false;
$cfg['Servers'][1]['users'] = false;
$cfg['Servers'][1]['usergroups'] = false;
$cfg['Servers'][1]['navigationhiding'] = false;
$cfg['Servers'][1]['savedsearches'] = false;
$cfg['Servers'][1]['central_columns'] = false;
$cfg['Servers'][1]['designer_settings'] = false;
$cfg['Servers'][1]['export_templates'] = false;

// Security: hide password in PhpMyAdmin interface
$cfg['Servers'][1]['hide_dbs'] = '';

// SSL settings for secure connections (if needed)
$cfg['Servers'][1]['ssl'] = false;
$cfg['Servers'][1]['ssl_key'] = '';
$cfg['Servers'][1]['ssl_cert'] = '';
$cfg['Servers'][1]['ssl_ca'] = '';
$cfg['Servers'][1]['ssl_capath'] = '';
$cfg['Servers'][1]['ssl_ciphers'] = '';

// Miscellaneous
$cfg['CookiePath'] = '/phpmyadmin/';
$cfg['CookieSameSite'] = 'Lax';
$cfg['CookieSecure'] = true;
$cfg['CookieHttpOnly'] = true;

?>
CONFIGEOF
  # `cat >` inherits the invoking shell's umask, which under systemd or
  # sudo is often 0077/0027, leaving the file as 0600 root:root. phpMyAdmin
  # (running in the jabali-pma pool as www-data) then greets the user with
  # "Existing configuration file ... is not readable." Force readable perms.
  chown root:www-data "${pma_link}/config.inc.php"
  chmod 0640 "${pma_link}/config.inc.php"
  _ok "config.inc.php written"

  # Deploy sso.php from the install directory
  _log "deploying SSO handler"
  if [[ ! -f "${REPO_DIR}/install/phpmyadmin/sso.php" ]]; then
    _die "sso.php not found at ${REPO_DIR}/install/phpmyadmin/sso.php"
  fi
  cp "${REPO_DIR}/install/phpmyadmin/sso.php" "${pma_link}/sso.php"
  chown root:www-data "${pma_link}/sso.php"
  chmod 0640 "${pma_link}/sso.php"
  _ok "sso.php deployed"

  # Ensure the nginx config directory exists
  local nginx_inc_dir="/etc/nginx/sites-available/includes"
  mkdir -p "$nginx_inc_dir"
  chmod 0755 "$nginx_inc_dir"

  # Write the http-scope map + log_format to /etc/nginx/conf.d/. Debian's
  # nginx.conf already includes conf.d/*.conf at http{} scope, so this is
  # the right place for directives that can't live inside server{}.
  _log "writing jabali-pma http-scope log format"
  mkdir -p /etc/nginx/conf.d
  cat > /etc/nginx/conf.d/jabali-pma-logformat.conf <<'LOGFMTEOF'
# jabali phpMyAdmin log format — redacts query strings so SSO tokens
# don't leak into access logs. Referenced by the /phpmyadmin/ location
# block at /etc/nginx/sites-available/includes/phpmyadmin.conf.
map $args $jabali_pma_logargs {
    ""      "-";
    default "[REDACTED]";
}
log_format jabali_pma '$remote_addr - $remote_user [$time_local] '
                      '"$request_method $uri $server_protocol" '
                      '$status $body_bytes_sent '
                      'args=$jabali_pma_logargs "$http_referer" '
                      '"$http_user_agent"';
LOGFMTEOF
  chmod 0644 /etc/nginx/conf.d/jabali-pma-logformat.conf
  _ok "jabali-pma log format written"

  # Write the phpMyAdmin nginx location block (reusable include for the panel vhost)
  _log "writing phpMyAdmin nginx location block"
  cat > "${nginx_inc_dir}/phpmyadmin.conf" <<'NGINXEOF'
# phpMyAdmin location block for nginx.
# Designed to be included inside a server{} block (port 80 default vhost
# or the panel vhost). The jabali_pma log_format used below is defined
# at http{} scope in /etc/nginx/conf.d/jabali-pma-logformat.conf.

# phpMyAdmin location (matches /phpmyadmin/* requests)
location ^~ /phpmyadmin/ {
    # Redirect to the location symlink
    alias /opt/phpmyadmin/current/;

    # Log with redacted query string (no tokens in access log)
    access_log /var/log/nginx/jabali-pma.access.log jabali_pma;
    error_log  /var/log/nginx/jabali-pma.error.log warn;

    # Deny access to sensitive files
    location ~ /\.ht {
        deny all;
    }
    location ~ /install {
        deny all;
    }

    # Pass PHP files to the appropriate FPM pool
    # This will be templated at vhost render time with the domain owner's pool socket
    location ~ \.php$ {
        # NOTE: nginx templater must replace {PHP_POOL_SOCKET} with the actual pool socket
        # Example: fastcgi_pass unix:/run/php/jabali-user123/fpm.sock;
        fastcgi_pass unix:{PHP_POOL_SOCKET};
        fastcgi_index index.php;
        include fastcgi_params;
        fastcgi_param SCRIPT_FILENAME $request_filename;
    }

    # Static files (CSS, JS, images) — cache them
    location ~ \.(js|css|png|jpg|jpeg|gif|ico|svg|woff|woff2|ttf|eot)$ {
        expires 30d;
        add_header Cache-Control "public, immutable";
    }
}
NGINXEOF

  # Substitute {PHP_POOL_SOCKET} placeholder with actual pma socket
  _log "substituting PHP_POOL_SOCKET in phpmyadmin.conf"
  sed -i "s|{PHP_POOL_SOCKET}|/run/php/jabali-pma/fpm.sock|g" "${nginx_inc_dir}/phpmyadmin.conf"
  _ok "phpMyAdmin nginx config ready"
  _ok "nginx location block written"

  # Create log directory for phpMyAdmin nginx logs
  mkdir -p /var/log/nginx
  touch /var/log/nginx/jabali-pma.access.log
  touch /var/log/nginx/jabali-pma.error.log
  chmod 0640 /var/log/nginx/jabali-pma.{access,error}.log
  chown www-data:www-data /var/log/nginx/jabali-pma.{access,error}.log

  _ok "phpMyAdmin installed and configured"

  # Release the concurrency lock (see the flock at the top of the function).
  # _die paths exit the process so the kernel drops the fd; this covers the
  # normal fall-through so a later legitimate caller can proceed.
  [[ -n "$_pma_lockfd" ]] && exec {_pma_lockfd}>&- || true
}

install_sftp_group() {
  _log "creating jabali-sftp system group"

  # Check if group exists using getent.
  if getent group jabali-sftp >/dev/null; then
    _ok "jabali-sftp group already exists"
  else
    # Create the group as a system group.
    groupadd --system jabali-sftp 2>/dev/null || true
    _ok "jabali-sftp system group created"
  fi
}

install_ssh_sandbox() {
  _log "installing SSH shell sandbox (M13)"

  # Group whose members are allowed to sudo-exec jabali-nspawn-enter.
  # Reconciler manages membership in lockstep with package.ssh_enabled.
  if ! getent group jabali-ssh-sandbox >/dev/null; then
    groupadd --system jabali-ssh-sandbox 2>/dev/null || true
    _ok "jabali-ssh-sandbox system group created"
  fi

  # GH #1229: members of jabali-ssh-forward are EXCLUDED from the JAB-352
  # forwarding lockdown (opt-in, default empty) so VS Code Remote-SSH can forward
  # to its own loopback VS Code Server. The sensitive loopback services stay
  # firewall-blocked per-uid, so this never re-opens the tunneling vector.
  if ! getent group jabali-ssh-forward >/dev/null; then
    groupadd --system jabali-ssh-forward 2>/dev/null || true
    _ok "jabali-ssh-forward system group created"
  fi

  # Directories required by the wrapper / agent + reconciler.
  install -d -m 0755 -o root -g root /etc/jabali
  install -d -m 0755 -o root -g root /etc/jabali/users
  install -d -m 0755 -o root -g root /var/lib/jabali-nspawn
  install -d -m 0755 -o root -g root /var/lib/jabali-nspawn/images

  # The login-shell wrapper (/usr/local/bin/jabali-ssh-shell) is the Go
  # binary built + installed by install_ssh_sandbox_prereqs (and shipped
  # in the release tarball / rebuilt by `jabali update`). The old bash
  # script that used to be installed here was retired — it lacked SSH
  # command (-c) forwarding (scp/git/rsync broke) and didn't filter
  # /etc/passwd. Single source of truth = panel-agent/cmd/jabali-ssh-shell.

  # Sudo-bridged nspawn entry helper. Runs as root after sudoers gate.
  install -m 0755 -o root -g root \
    "$REPO_DIR/install/ssh/jabali-nspawn-enter" \
    /usr/local/bin/jabali-nspawn-enter

  # Sudoers entry (NOPASSWD locked to absolute path). visudo -cf checks
  # parse — abort install if the file is malformed before placement.
  if ! visudo -cf "$REPO_DIR/install/ssh/jabali-nspawn-sudoers" >/dev/null; then
    _die "jabali-nspawn-sudoers failed visudo -cf"
  fi
  install -m 0440 -o root -g root \
    "$REPO_DIR/install/ssh/jabali-nspawn-sudoers" \
    /etc/sudoers.d/jabali-nspawn

  # Default mode = bubblewrap (ADR-0067 §0.2). Don't clobber on rerun.
  if [ ! -f /etc/jabali/ssh-sandbox-mode ]; then
    echo "bubblewrap" > /etc/jabali/ssh-sandbox-mode
    chmod 0644 /etc/jabali/ssh-sandbox-mode
  fi

  # Default nspawn image pin. Image itself is built below by
  # build_default_nspawn_image() (debootstrap from snapshot.debian.org).
  if [ ! -f /etc/jabali/default-nspawn-image ]; then
    echo "debian-13-v1" > /etc/jabali/default-nspawn-image
    chmod 0644 /etc/jabali/default-nspawn-image
  fi

  # Verify bwrap is setuid root (Debian/Ubuntu default). Without it the
  # bubblewrap branch falls through to nologin — fail loudly at install
  # time instead.
  if [ ! -u /usr/bin/bwrap ]; then
    _warn "/usr/bin/bwrap is not setuid root — bubblewrap mode will deny shell access until fixed"
  fi

  _ok "SSH shell sandbox installed (mode=bubblewrap; default nspawn image=debian-13-v1)"
}

# Default nspawn image: trixie + wp-cli. Built from a pinned
# snapshot.debian.org timestamp so rebuilds are byte-identical. Skipped
# if the sealed image already exists (idempotent across reruns).
build_default_nspawn_image() {
  local image="debian-13-v1"
  local snapshot="20260301T000000Z"
  local image_dir="/var/lib/jabali-nspawn/images/${image}"

  if [ -d "${image_dir}" ]; then
    _ok "nspawn image ${image} already built at ${image_dir}"
    return 0
  fi

  if ! command -v jabali >/dev/null 2>&1; then
    _warn "jabali CLI not found; skipping nspawn image build (rerun installer or run 'jabali nspawn build' manually)"
    return 0
  fi

  if ! command -v debootstrap >/dev/null 2>&1; then
    _warn "debootstrap missing; cannot auto-build nspawn image"
    return 0
  fi

  _log "building default nspawn image ${image} (snapshot=${snapshot}); this takes 3-5 minutes"
  if jabali nspawn build --codename debian-13 --version v1 --snapshot "${snapshot}" --suite trixie; then
    _ok "nspawn image ${image} built and sealed"
  else
    _warn "nspawn image build failed — bubblewrap mode still works; rerun 'jabali nspawn build --version v1 --snapshot ${snapshot}' to retry"
  fi
}

install_sftp_sshd_config() {
  _log "installing SFTP sshd drop-in configuration"

  # Install the sshd drop-in configuration file with correct permissions.
  # Path is resolved against $REPO_DIR (clone target) so this works under
  # `curl | bash` where CWD has no ./install/ tree.
  install -m 0644 -o root -g root "$REPO_DIR/install/ssh/jabali-sftp.conf" /etc/ssh/sshd_config.d/jabali-sftp.conf
  _ok "SFTP sshd drop-in installed"

  # Validate sshd configuration before reloading.
  _log "validating sshd configuration"
  if ! sshd -t; then
    _die "sshd configuration validation failed. Check /etc/ssh/sshd_config.d/jabali-sftp.conf for errors."
  fi
  _ok "sshd configuration is valid"

  # Reload sshd to apply the new configuration. Debian and Ubuntu
  # ship the unit under different names: Debian and pre-24.04 Ubuntu
  # use `ssh.service`, while older docs and some derivatives still
  # alias `sshd.service`. Ubuntu 24.04 dropped the `sshd` alias in
  # favour of socket-activated `ssh.service` only. Try the canonical
  # `ssh` first and fall through to `sshd` so both worlds work.
  _log "reloading sshd"
  # Debian 13 + Ubuntu 24.04 socket-activate sshd: ssh.socket is the
  # listener; sshd-session is spawned PER CONNECTION and reads
  # /etc/ssh/sshd_config (+ includes) fresh on every spawn. So when
  # the SFTP drop-in we just installed needs to take effect, the
  # next inbound ssh connection automatically picks it up -- a
  # reload of the long-lived listener isn't required.
  #
  # Worse: `systemctl reload ssh.service` on socket-activated mode
  # FAILS (it tries to reload a unit that has no PID, then puts the
  # service into a 'failed' state). Set -e then kills the installer
  # right after the "reloading sshd" log line (GH #126).
  #
  # Detect socket-activated mode and skip the reload block in that
  # case. Otherwise (classic sshd.service that owns its own PID),
  # walk the canonical reload chain.
  # Socket-activated means the host actually USES ssh.socket as the
  # listener — i.e. it is active or enabled. Merely shipping the unit
  # file isn't enough: Debian also ships ssh.socket on classic hosts
  # where it's installed-but-disabled and the long-lived ssh.service
  # owns :22. Testing `list-unit-files` (file exists) misclassified
  # those classic hosts as socket-activated and SKIPPED the reload, so
  # the SFTP drop-in we just wrote silently never applied until a manual
  # sshd restart (GH#133). Test is-active/is-enabled instead.
  if systemctl is-active --quiet ssh.socket \
     || systemctl is-enabled --quiet ssh.socket; then
    _ok "sshd is socket-activated; new config applies on next connection (no reload needed)"
  else
    sshd_reloaded=0
    if systemctl list-unit-files ssh.service >/dev/null 2>&1; then
      if systemctl reload ssh 2>/dev/null; then sshd_reloaded=1; fi
    fi
    if [[ $sshd_reloaded -eq 0 ]] && systemctl list-unit-files sshd.service >/dev/null 2>&1; then
      if systemctl reload sshd 2>/dev/null; then sshd_reloaded=1; fi
    fi
    if [[ $sshd_reloaded -eq 0 ]]; then
      if pgrep -x sshd >/dev/null 2>&1; then
        pkill -HUP -x sshd && sshd_reloaded=1
      fi
    fi
    if [[ $sshd_reloaded -eq 0 ]]; then
      _die "sshd reload failed: systemctl reload + SIGHUP both failed -- check 'systemctl status ssh' and 'sshd -t'"
    fi
    _ok "sshd reloaded"
  fi

  # GH #133: prior install runs (especially the buggy reload path
  # that ran `systemctl reload ssh` on socket-activated hosts) could
  # leave ssh.service in 'failed' or 'inactive'. The new SFTP drop-in
  # is irrelevant if sshd isn't listening at all -- the operator can't
  # even log back in to fix it. So always re-assert sshd reachability
  # before returning. Order: prefer ssh.socket (Debian 13 / Ubuntu
  # 24.04 socket-activation), fall back to ssh.service then
  # sshd.service. Idempotent on a healthy host.
  ensure_sshd_running
}

# ensure_ssh_forwarding_lockdown — JAB-352. SSH-enabled hosting users (the
# jabali-ssh-sandbox group) get the restricted jabali-ssh-shell as their login
# shell, but sshd sets up TCP / Unix-socket / agent / X11 / tunnel forwarding
# BEFORE the shell runs, so the shell sandbox can't stop a tenant tunnelling
# into loopback-only services (MariaDB, PostgreSQL, the panel Agent socket,
# unauthenticated profilers). A `Match Group jabali-ssh-sandbox` drop-in
# disables every forwarding channel at the sshd layer; root/operator SSH is
# untouched (different group). Idempotent: it repairs/installs the drop-in and
# reloads sshd ONLY when the rendered file actually changed, so it is safe to
# call on every `jabali update` — mirroring the ensure_pdns_* convergers, since
# install_sftp_sshd_config runs on fresh install only.
ensure_ssh_forwarding_lockdown() {
  local src="${REPO_DIR:-/opt/jabali-panel}/install/ssh/jabali-ssh-sandbox.conf"
  local dst=/etc/ssh/sshd_config.d/jabali-ssh-sandbox.conf
  if [[ ! -f "$src" ]]; then
    _warn "ssh forwarding-lockdown source missing at $src — skipping (JAB-352 not applied)"
    return 0
  fi
  # Idempotent: already current → no rewrite, no sshd reload.
  if [[ -f "$dst" ]] && cmp -s "$src" "$dst"; then
    return 0
  fi
  install -m 0644 -o root -g root "$src" "$dst"
  # Validate BEFORE relying on it. If the host's sshd rejects a directive,
  # remove the drop-in rather than leaving sshd unparseable (a broken
  # sshd_config would lock the operator out on the next restart). Every
  # directive here is standard OpenSSH and box-verified on Debian 13, so this
  # is a defensive backstop, not the expected path.
  if ! sshd -t 2>/dev/null; then
    _warn "sshd -t rejected $dst — removing it (JAB-352 forwarding lockdown NOT applied; check OpenSSH directive support)"
    rm -f "$dst"
    return 0
  fi
  # Socket-activated sshd (stock Debian 13 / Ubuntu 24.04) spawns a fresh
  # sshd-session per connection that re-reads the config, so no reload is
  # needed. Jabali normally converges to classic ssh.service (ensure_sshd_
  # running / GH #133), which must be reloaded. Try both, fall back to SIGHUP.
  if systemctl is-active --quiet ssh.socket || systemctl is-enabled --quiet ssh.socket; then
    _ok "SSH forwarding lockdown installed (JAB-352); applies on next connection (socket-activated sshd)"
  elif systemctl reload ssh 2>/dev/null || systemctl reload sshd 2>/dev/null \
       || { pgrep -x sshd >/dev/null 2>&1 && pkill -HUP -x sshd; }; then
    _ok "SSH forwarding lockdown installed + sshd reloaded (JAB-352 — tenant tunnels into loopback services blocked)"
  else
    _warn "installed $dst but sshd reload failed — new policy applies on next sshd restart"
  fi
}

# ensure_sshd_running — converge the host onto a single classic ssh.service
# listener and retire ssh.socket (GH #133). Delegates to the shared,
# lockout-safe normalizer so a fresh install and a later `jabali update`
# behave identically. Falls back to a best-effort start if the script is
# somehow absent.
ensure_sshd_running() {
  local norm="${REPO_DIR:-/opt/jabali-panel}/install/ssh/normalize-ssh-classic.sh"
  if [[ -r "$norm" ]]; then
    _log "normalizing SSH to classic ssh.service (masking ssh.socket)"
    if bash "$norm"; then
      _ok "sshd is reachable (classic ssh.service)"
    else
      _warn "SSH normalization reported a problem -- check 'systemctl status ssh.service ssh.socket'"
    fi
    return 0
  fi
  # Fallback: normalizer missing -- best-effort start of whatever unit exists.
  if systemctl list-unit-files ssh.service >/dev/null 2>&1; then
    systemctl is-active --quiet ssh.service || { systemctl reset-failed ssh.service 2>/dev/null || true; systemctl start ssh.service 2>/dev/null || true; }
  elif systemctl list-unit-files sshd.service >/dev/null 2>&1; then
    systemctl is-active --quiet sshd.service || { systemctl reset-failed sshd.service 2>/dev/null || true; systemctl start sshd.service 2>/dev/null || true; }
  fi
  if systemctl is-active --quiet ssh.service || systemctl is-active --quiet sshd.service; then
    _ok "sshd is reachable"
  else
    _warn "no active sshd unit found after install -- check 'systemctl status ssh' / 'ssh.socket' / 'sshd'"
  fi
}

install_sso_key() {
  local sso_key_path="/etc/jabali-panel/sso.key"

  # Always enforce ownership+mode, even when the file already exists —
  # earlier installer versions wrote it mode 0600 owned by root, which
  # the panel service user cannot read. Fix in place on every run.
  mkdir -p /etc/jabali-panel
  chmod 0755 /etc/jabali-panel

  if [[ -f "$sso_key_path" ]]; then
    chown "$SERVICE_USER:$SERVICE_USER" "$sso_key_path"
    chmod 0600 "$sso_key_path"
    _ok "SSO key already exists at $sso_key_path (ownership refreshed)"
    return
  fi

  _log "generating SSO envelope key (32 bytes AES-256-GCM)"

  # Generate 32 random bytes and write to file with restrictive permissions,
  # owned by the service user so the panel process can read it.
  dd if=/dev/urandom of="$sso_key_path" bs=1 count=32 2>/dev/null
  chown "$SERVICE_USER:$SERVICE_USER" "$sso_key_path"
  chmod 0600 "$sso_key_path"

  _ok "SSO key created at $sso_key_path"
}

install_cache_doctor_timer() {
  # GH #605: hourly WordPress cache health-drift auto-repair. Sweeps
  # cache-enabled installs, re-provisions any whose `wp jabali-cache verify`
  # fails. Same install shape as install_sso_reaper_timer.
  _log "installing cache-doctor systemd timer"
  local svc_src="${REPO_DIR}/install/systemd/jabali-cache-doctor.service"
  local timer_src="${REPO_DIR}/install/systemd/jabali-cache-doctor.timer"
  local svc_dst="/etc/systemd/system/jabali-cache-doctor.service"
  local timer_dst="/etc/systemd/system/jabali-cache-doctor.timer"

  if [[ ! -f "$svc_src" || ! -f "$timer_src" ]]; then
    _err "cache-doctor systemd units missing at $svc_src / $timer_src"
    exit 1
  fi

  install -m 0644 -o root -g root "$svc_src" "$svc_dst"
  install -m 0644 -o root -g root "$timer_src" "$timer_dst"

  _log "cache-doctor: systemctl daemon-reload"
  systemctl daemon-reload
  _log "cache-doctor: enable --now jabali-cache-doctor.timer"
  systemctl enable --now jabali-cache-doctor.timer

  _ok "cache-doctor timer enabled (hourly)"
}

install_sso_reaper_timer() {
  # M22 rework (ADR-0040): the self-deleting sso-file design uses a
  # systemd timer to sweep stranded jabali-sso-<nonce>.php files older
  # than 60s. Defence in depth — the PHP file unlinks itself after
  # successful login, so the reaper only catches files that didn't get
  # to that step (PHP fatal mid-execution, web server crash, etc.).
  _log "installing sso reaper systemd timer"
  # install.sh never cd's into $REPO_DIR — every other function anchors
  # source paths against ${REPO_DIR} explicitly (see install_jabali_slices,
  # install_php_pool_template, install_kratos). A relative path like
  # "install/systemd/..." resolves against $PWD, which is /root when the
  # script runs via `curl | bash`, and the file-exists check below fires
  # with _err → exit 1. Fix: match the pattern used everywhere else.
  local svc_src="${REPO_DIR}/install/systemd/jabali-sso-reaper.service"
  local timer_src="${REPO_DIR}/install/systemd/jabali-sso-reaper.timer"
  local svc_dst="/etc/systemd/system/jabali-sso-reaper.service"
  local timer_dst="/etc/systemd/system/jabali-sso-reaper.timer"

  if [[ ! -f "$svc_src" || ! -f "$timer_src" ]]; then
    _err "sso reaper systemd units missing at $svc_src / $timer_src"
    exit 1
  fi

  install -m 0644 -o root -g root "$svc_src" "$svc_dst"
  install -m 0644 -o root -g root "$timer_src" "$timer_dst"

  # daemon-reload + enable --now are the two places this function has
  # historically stalled. Log before each so a bash `set -e` exit pins
  # the culprit — previous regression showed "SSO key created" as the
  # last line because every step in this function was silent.
  _log "sso reaper: systemctl daemon-reload"
  systemctl daemon-reload

  _log "sso reaper: enable --now jabali-sso-reaper.timer"
  systemctl enable --now jabali-sso-reaper.timer

  _ok "sso reaper timer enabled (every 30s)"
}

# ---------- JAB-158: journald size cap ------------------------------------
#
# install_journald_cap drops a bounded SystemMaxUse/retention config so the
# persistent journal can never balloon to ~10% of the root filesystem (the
# systemd default) and fill a small VM's disk. Idempotent file copy; a
# restart of systemd-journald applies it live.
install_journald_cap() {
  _log "installing journald size cap"
  local src="${REPO_DIR}/install/systemd/journald-jabali.conf"
  local dst="/etc/systemd/journald.conf.d/jabali.conf"
  if [[ ! -f "$src" ]]; then
    _warn "journald cap template missing at $src — skipping"
    return 0
  fi
  install -d -m 0755 /etc/systemd/journald.conf.d
  install -m 0644 -o root -g root "$src" "$dst"
  # Apply live; a failed restart must not abort the install (journald is
  # socket-activated and comes back on next log write regardless).
  systemctl restart systemd-journald 2>/dev/null || true
  _ok "journald capped (SystemMaxUse=400M, 1 month retention)"
}

# ---------- JAB-153 + JAB-157: daily disk maintenance ---------------------
#
# install_disk_maintenance_timer wires a daily oneshot that prunes Stalwart's
# dated tracer logs and stale nspawn sandbox images (see install/systemd/
# disk-maintenance for the sweep). Same install shape as install_sso_reaper_timer.
install_disk_maintenance_timer() {
  local script_src="${REPO_DIR}/install/systemd/disk-maintenance"
  local svc_src="${REPO_DIR}/install/systemd/jabali-disk-maintenance.service"
  local timer_src="${REPO_DIR}/install/systemd/jabali-disk-maintenance.timer"
  local svc_dst="/etc/systemd/system/jabali-disk-maintenance.service"
  local timer_dst="/etc/systemd/system/jabali-disk-maintenance.timer"

  if [[ ! -f "$script_src" || ! -f "$svc_src" || ! -f "$timer_src" ]]; then
    _err "disk-maintenance units missing at $script_src / $svc_src / $timer_src"
    exit 1
  fi

  install -d -m 0755 /usr/local/libexec/jabali
  install -m 0755 -o root -g root "$script_src" /usr/local/libexec/jabali/disk-maintenance
  install -m 0644 -o root -g root "$svc_src" "$svc_dst"
  install -m 0644 -o root -g root "$timer_src" "$timer_dst"

  _log "disk maintenance: systemctl daemon-reload"
  systemctl daemon-reload
  _log "disk maintenance: enable --now jabali-disk-maintenance.timer"
  systemctl enable --now jabali-disk-maintenance.timer

  _ok "disk maintenance timer enabled (daily)"
}

# ---------- retention sweep (JAB-100/101/103/122/123/125) ------------------
#
# install_retention_sweep_timer wires the daily oneshot that prunes expired
# rows from the log/report tables that had no retention. Same install shape
# as install_sso_reaper_timer.
install_retention_sweep_timer() {
  local svc_src="${REPO_DIR}/install/systemd/jabali-retention-sweep.service"
  local timer_src="${REPO_DIR}/install/systemd/jabali-retention-sweep.timer"
  local svc_dst="/etc/systemd/system/jabali-retention-sweep.service"
  local timer_dst="/etc/systemd/system/jabali-retention-sweep.timer"

  if [[ ! -f "$svc_src" || ! -f "$timer_src" ]]; then
    _err "retention-sweep units missing at $svc_src / $timer_src"
    exit 1
  fi

  install -m 0644 -o root -g root "$svc_src" "$svc_dst"
  install -m 0644 -o root -g root "$timer_src" "$timer_dst"

  _log "retention sweep: systemctl daemon-reload"
  systemctl daemon-reload
  _log "retention sweep: enable --now jabali-retention-sweep.timer"
  systemctl enable --now jabali-retention-sweep.timer

  _ok "retention sweep timer enabled (daily)"
}

# ---------- M35 migration-secrets reaper (ADR-0094) ----------------------
#
# install_migration_secrets_reaper writes the daily timer + service
# that wipes /etc/jabali-panel/migration-secrets/<job-id>.env files
# for terminal-state migration_jobs (done/failed/cancelled). Closes
# the ADR-0094 §"tracked risks" gap: per-job credentials previously
# persisted across job-terminal state with no scheduled wipe.
install_migration_secrets_reaper() {
  _log "installing migration-secrets reaper systemd timer"
  local svc_src="${REPO_DIR}/install/systemd/jabali-migration-secrets-reap.service"
  local timer_src="${REPO_DIR}/install/systemd/jabali-migration-secrets-reap.timer"
  local svc_dst="/etc/systemd/system/jabali-migration-secrets-reap.service"
  local timer_dst="/etc/systemd/system/jabali-migration-secrets-reap.timer"
  if [[ ! -f "$svc_src" || ! -f "$timer_src" ]]; then
    _warn "migration-secrets reaper units missing at $svc_src / $timer_src — skipping"
    return 0
  fi
  install -m 0644 -o root -g root "$svc_src" "$svc_dst"
  install -m 0644 -o root -g root "$timer_src" "$timer_dst"
  systemctl daemon-reload
  systemctl enable --now jabali-migration-secrets-reap.timer >/dev/null 2>&1 || \
    _warn "jabali-migration-secrets-reap.timer enable failed — check 'journalctl -u jabali-migration-secrets-reap.timer'"
  _ok "migration-secrets reaper timer enabled (daily 04:30 UTC + 15min jitter)"
}

# ---------- M30 backup foundation (ADR-0075) -------------------------------
#
# install_backup_foundation lays the restic-backed backup foundation:
#   1. /var/lib/jabali-backups + /var/lib/jabali-backups/repo/  (root:jabali 0750)
#   2. /etc/jabali-panel/restic-repo.password                   (root:jabali 0640)
#   3. `restic init` against the repo (idempotent — restic refuses to
#      re-init an existing repo; that exit code is swallowed).
#   4. jabali-backup-retention.{service,timer} drop-ins enabled --now.
#
# Apt-installed `restic` lives in the base-packages batch so this function
# never touches apt itself. Steps 2-12 of M30 add the actual backup +
# restore code paths; this function just guarantees the foundation is in
# place on every fresh install AND on every `jabali update`.
install_backup_foundation() {
  _log "installing M30 backup foundation (restic repo + retention timer)"

  # Sanity: the apt batch should have provided restic. If not, the host
  # is too old or the install_base_packages step was skipped — bail loud
  # rather than silently disable backups.
  if ! command -v restic >/dev/null 2>&1; then
    _err "restic binary not found on PATH after install_base_packages"
    exit 1
  fi
  local restic_version
  restic_version="$(restic version 2>/dev/null | awk '/^restic /{print $2; exit}')"
  _log "restic available: ${restic_version:-unknown}"

  # Backup repo lives outright under root:root 0700. Both the agent
  # (writes packs during backup) and the retention timer (forget+prune)
  # run as root, so single-identity ownership avoids the file-mode
  # chase that earlier jabali-group attempts produced (restic writes
  # 0600 packs that the jabali user could not read after a root-owned
  # backup left files behind). systemd hardening on the retention unit
  # (PrivateTmp, ProtectSystem=strict, ReadWritePaths) limits blast
  # radius without needing a separate user.
  install -d -m 0700 -o root -g root /var/lib/jabali-backups
  install -d -m 0700 -o root -g root /var/lib/jabali-backups/repo
  # Cache dir for the retention timer. ProtectHome=read-only on the
  # unit blocks /root/.cache, so RESTIC_CACHE_DIR points here instead.
  install -d -m 0700 -o root -g root /var/lib/jabali-backups/.cache
  install -d -m 0700 -o root -g root /var/lib/jabali-backups/.cache/restic
  # M30.1 (ADR-0078): per-destination restic creds env files live here,
  # one file per backup_destinations.id with restic backend env vars
  # (AWS_*, B2_*, AZURE_*, etc.). Mode 0600 root:root per-file; the
  # panel writes them via the destinations REST handler.
  install -d -m 0700 -o root -g root /etc/jabali-panel/restic-remotes

  # Password file. Generated once; never rotated automatically (rotating
  # the restic password would invalidate every existing snapshot — it's
  # a deliberate manual operation documented in the M30 runbook).
  local pw_file="/etc/jabali-panel/restic-repo.password"
  mkdir -p /etc/jabali-panel
  if [[ ! -s "$pw_file" ]]; then
    _log "generating restic repo password (32 bytes base64)"
    local tmp
    tmp="$(mktemp)"
    openssl rand -base64 32 > "$tmp"
    install -m 0600 -o root -g root "$tmp" "$pw_file"
    rm -f "$tmp"
  else
    # Re-enforce ownership + mode on every run (matches install_sso_key
    # idempotency). Earlier versions wrote 0640 root:jabali — flip back
    # to 0600 root:root now that retention runs as root.
    chown root:root "$pw_file"
    chmod 0600 "$pw_file"
  fi

  # restic init is the only step that meaningfully fails on a re-run:
  # restic exits 1 with `repository already exists` when the repo dir
  # has a `config` blob. Detect that case explicitly so set -e doesn't
  # kill the install, and surface any OTHER failure loud.
  if [[ -s /var/lib/jabali-backups/repo/config ]]; then
    _log "restic repo already initialized at /var/lib/jabali-backups/repo"
  else
    _log "running restic init (root:root)"
    if ! restic --repo /var/lib/jabali-backups/repo \
                --password-file "$pw_file" \
                init >/dev/null; then
      _err "restic init failed; backup foundation incomplete"
      exit 1
    fi
    _ok "restic repo initialized"
  fi
  # Re-enforce ownership on existing repos that earlier install.sh
  # revisions chowned to jabali — flip back to root:root so retention
  # (which runs as root) and the agent (also root) share one identity.
  chown -R root:root /var/lib/jabali-backups

  # Timer + service drop-ins. Same install pattern as install_sso_reaper_timer.
  local svc_src="${REPO_DIR}/install/systemd/jabali-backup-retention.service"
  local timer_src="${REPO_DIR}/install/systemd/jabali-backup-retention.timer"
  local svc_dst="/etc/systemd/system/jabali-backup-retention.service"
  local timer_dst="/etc/systemd/system/jabali-backup-retention.timer"

  if [[ ! -f "$svc_src" || ! -f "$timer_src" ]]; then
    _err "backup retention systemd units missing at $svc_src / $timer_src"
    exit 1
  fi

  install -m 0644 -o root -g root "$svc_src" "$svc_dst"
  install -m 0644 -o root -g root "$timer_src" "$timer_dst"

  systemctl daemon-reload
  systemctl enable --now jabali-backup-retention.timer

  _ok "M30 backup foundation installed (restic repo + retention timer)"
}

# ---------- step 8a: M26 security foundation (CrowdSec + UFW) ---------------
#
# Two idempotent installs that land BEFORE Stalwart so:
#   - CrowdSec LAPI binds on /run/crowdsec/api.sock (NOT 127.0.0.1:8080,
#     which Stalwart owns per ADR-0050).
#   - UFW is `enable`d ONCE (idempotent guard) before Stalwart's first
#     bind so the iptables/nftables reload cannot race Stalwart startup.
#
# Both are wired into main() between install_pdns_recursor and
# install_stalwart. Apt packages (crowdsec, ufw, yq) are in the
# install_base_packages batch; the CrowdSec firewall bouncer is detected
# + installed at runtime here because the package name varies by Debian
# release (nftables on trixie, iptables on bookworm) and apt-cache
# fallback is the safer model.
#
# cleanup_modsecurity also runs in this block — removes the dead M26
# ModSecurity stack on hosts upgraded from older installs (ADR-0055
# SUPERSEDED 2026-04-26).

add_crowdsec_apt_source() {
  # CrowdSec upstream apt source. Debian-stock crowdsec on trixie is
  # 1.4.6 — too old to support `api.server.listen_socket` (added in
  # CrowdSec 1.5.x; ADR-0050 requires socket binding). Upstream repo
  # ships current 1.7.x with socket support PLUS both bouncer variants
  # (crowdsec-firewall-bouncer-{iptables,nftables}) which Debian's
  # repo does not provide. See ADR-0053 for rationale.
  local key_url="https://packagecloud.io/crowdsec/crowdsec/gpgkey"
  local keyring="/etc/apt/keyrings/crowdsec.gpg"
  local sources_file="/etc/apt/sources.list.d/crowdsec.list"
  local source_line='deb [signed-by=/etc/apt/keyrings/crowdsec.gpg] https://packagecloud.io/crowdsec/crowdsec/any/ any main'

  install -d -m 0755 /etc/apt/keyrings

  if [[ ! -s "$keyring" ]]; then
    _log "fetching CrowdSec upstream signing key → $keyring"
    local tmp_key
    tmp_key="$(mktemp --tmpdir jabali-cs-key.XXXXXX)"
    if ! curl -fsSL --connect-timeout 10 -o "$tmp_key" "$key_url"; then
      rm -f "$tmp_key"
      _die "failed to fetch CrowdSec signing key from $key_url"
    fi
    gpg --batch --yes --dearmor -o "$keyring" "$tmp_key"
    rm -f "$tmp_key"
    chmod 0644 "$keyring"
  fi

  if [[ ! -f "$sources_file" ]] || ! grep -qF "$source_line" "$sources_file"; then
    _log "writing $sources_file"
    printf '%s\n' "$source_line" > "$sources_file"
    apt-get update -qq
  fi
}

install_crowdsec() {
  _log "configuring CrowdSec (upstream apt source for socket support, ADR-0053)"

  add_crowdsec_apt_source

  # If Debian-stock 1.4.x crowdsec was installed by an older install.sh
  # run (the previous deps batch listed `crowdsec` directly), upgrade to
  # the upstream version. apt-get install with the upstream repo enabled
  # picks the candidate from packagecloud automatically.
  # crowdsec's postinst runs `cscli setup unattended`, which downloads hub
  # parsers + the GeoLite2 mmdb from hub-data.crowdsec.net. That CDN
  # intermittently drops the connection mid-transfer ("http2: server sent
  # GOAWAY"), failing the postinst and leaving the package half-configured
  # -> the whole apt run errors out and install.sh dies. Retry with
  # backoff: `apt-get install -f` re-runs the unfinished postinst (which
  # re-attempts the download) without re-fetching the .deb. Also covers a
  # previous install left in `unpacked` state.
  local _cs_attempt _cs_ok=0 _cs_installed
  for _cs_attempt in 1 2 3 4 5; do
    if ! dpkg -s crowdsec >/dev/null 2>&1; then
      _log "apt install crowdsec (upstream, try ${_cs_attempt}/5)…"
      apt-get install -y -qq --no-install-recommends crowdsec >>"${LOG_FILE:-/dev/null}" 2>&1 || true
    else
      _cs_installed="$(dpkg-query -W -f='${Version}\n' crowdsec 2>/dev/null)"
      if [[ "$_cs_installed" == 1.4.* ]] || [[ "$_cs_installed" == 1.3.* ]]; then
        _log "upgrading crowdsec from $_cs_installed (Debian-stock) → upstream"
        _log "apt upgrade crowdsec (try ${_cs_attempt}/5)…"
        apt-get install -y -qq --only-upgrade crowdsec >>"${LOG_FILE:-/dev/null}" 2>&1 || true
      fi
    fi
    # Finish any half-done postinst (re-attempts the hub/geoip download).
    if [[ "$(dpkg-query -W -f='${Status}' crowdsec 2>/dev/null || true)" != *"installed"* ]]; then
      _log "apt -f install (finish crowdsec postinst, try ${_cs_attempt}/5)…"
      apt-get install -f -y -qq >>"${LOG_FILE:-/dev/null}" 2>&1 || true
    fi
    if [[ "$(dpkg-query -W -f='${Status}' crowdsec 2>/dev/null || true)" == *"installed"* ]]; then
      _cs_ok=1; _ok "crowdsec installed + configured"; break
    fi
    _warn "crowdsec not fully configured (attempt ${_cs_attempt}/5) — likely a transient hub-data.crowdsec.net download (http2 GOAWAY); retrying in $((_cs_attempt * 10))s"
    sleep $(( _cs_attempt * 10 ))
  done
  [[ "$_cs_ok" == 1 ]] || _die "crowdsec failed to configure after 5 attempts — last failure almost certainly a transient hub download (http2 GOAWAY). Re-run install.sh; if it persists, check connectivity to hub-data.crowdsec.net."

  if ! command -v cscli >/dev/null 2>&1; then
    _die "cscli missing after upstream crowdsec install"
  fi

  # The hub index (/var/lib/crowdsec/hub/.index.json) must exist before
  # the agent starts — without it crowdsec FATALs with "invalid hub
  # index: unable to read index file". The package postinst tries to
  # download it but only on a successful first start; since our drop-in
  # may cause a chicken-and-egg restart loop, fetch the index explicitly
  # here. `cscli hub update` is idempotent — re-runs are a no-op when
  # the index is fresh.
  if [[ ! -f /var/lib/crowdsec/hub/.index.json ]]; then
    _log "downloading CrowdSec hub index (first install)"
    cscli hub update --error 2>&1 | sed 's/^/    /' || _warn "cscli hub update non-zero — surface via 'cscli hub update' for details"
  fi

  # Pre-flight: install appsec collections that jabali-appsec.yaml references
  # via inband_rules wildcards (vpatch-*, generic-*). CrowdSec refuses to
  # start if any referenced rule pattern matches zero files. These cscli calls
  # are LAPI-independent — they write hub files locally. Must run before the
  # start/restart attempt below.
  # Check actual rule FILES on disk, not cscli list metadata — the list can
  # report a collection as installed while the files are absent (partial
  # install, aborted hub sync, etc.). In that case --force alone won't
  # re-download; purge the stale metadata first.
  # History: crowdsecurity/appsec-crs (OWASP CRS) was excluded because
  # hub-data.crowdsec.net/appsec/crs/crs-setup.conf returned HTTP 500,
  # failing every fresh install; and base-config used to live in
  # appsec-configs/ (referencing it as an inband rule crashed startup).
  # 2026-05-16: both resolved upstream — crs installs cleanly and
  # base-config is now an appsec-RULE. CRS-class generic LFI/SQLi/XSS
  # coverage is re-enabled, but every step below is best-effort +
  # presence-gated: a future hub regression degrades to vpatch+generic
  # and CrowdSec still starts (never references a missing rule).
  # Track any cscli hub mutation in this function; we reload crowdsec
  # once at the end (see end of function) instead of after every step.
  # Without this `jabali update` left cscli's "Run reload" hint to the
  # operator after every hub change.
  local _cs_dirty=0
  # Flat per-rule layout — enabled rules symlink into
  # /etc/crowdsec/appsec-rules/<name>.yaml (no crowdsecurity/ subdir).
  # The previous path pointed at a non-existent subdir, so the
  # presence-gate ALWAYS failed and every `jabali update` purged then
  # re-downloaded all 170 vpatch rules — minutes of work for no diff.
  # Fixed 2026-05-20.
  local _appsec_rules_dir="/etc/crowdsec/appsec-rules"
  if ! compgen -G "${_appsec_rules_dir}/vpatch-*" >/dev/null 2>&1; then
    cscli collections remove crowdsecurity/appsec-virtual-patching --purge 2>/dev/null || true
    _spin "cscli collections install appsec-virtual-patching (pre-flight)" \
      cscli collections install crowdsecurity/appsec-virtual-patching
    _cs_dirty=1
  fi
  if ! compgen -G "${_appsec_rules_dir}/generic-*" >/dev/null 2>&1; then
    cscli collections remove crowdsecurity/appsec-generic-rules --purge 2>/dev/null || true
    _spin "cscli collections install appsec-generic-rules (pre-flight)" \
      cscli collections install crowdsecurity/appsec-generic-rules
    _cs_dirty=1
  fi
  # 2026-05-16: CRS-class generic LFI/SQLi/XSS coverage. The hub-data
  # 500 that previously broke crs installs is resolved and base-config
  # moved from appsec-configs/ to appsec-rules/. Best-effort: a hub
  # regression must degrade to vpatch+generic, never crash the install
  # (the desired_config builder below is presence-gated to match).
  # NB: enabled appsec-rules are symlinked FLAT into
  # /etc/crowdsec/appsec-rules/<name>.yaml (no crowdsecurity/ subdir);
  # -f follows the symlink. (The legacy ${_appsec_rules_dir} a few
  # lines up points at a non-existent crowdsecurity/ subdir — that is a
  # separate latent path bug that merely forces a harmless reinstall.)
  local _ard="/etc/crowdsec/appsec-rules"
  if [[ ! -f "$_ard/crs.yaml" ]]; then
    if cscli appsec-rules install crowdsecurity/crs 2>/dev/null; then
      _cs_dirty=1
    else
      _warn "appsec crowdsecurity/crs install failed (hub?) — AppSec degrades to vpatch+generic"
    fi
  fi
  if [[ ! -f "$_ard/base-config.yaml" ]]; then
    if cscli appsec-rules install crowdsecurity/base-config 2>/dev/null; then
      _cs_dirty=1
    else
      _warn "appsec crowdsecurity/base-config install failed — AppSec degrades to vpatch+generic"
    fi
  fi
  if [[ ! -f "$_ard/crs-exclusion-plugin-wordpress.yaml" ]]; then
    if cscli appsec-rules install crowdsecurity/crs-exclusion-plugin-wordpress 2>/dev/null; then
      _cs_dirty=1
    else
      _warn "appsec crs WordPress exclusion install failed — CRS may false-positive on wp-admin"
    fi
  fi

  # Pick the firewall bouncer matching the kernel backend. Trixie+
  # defaults to nftables; bookworm uses iptables. apt-cache check guards
  # against packaging drift (both variants exist in upstream repo).
  local debian_rel bouncer_pkg
  debian_rel="$(lsb_release -rs 2>/dev/null | cut -d. -f1)"
  if [[ "$debian_rel" -ge 13 ]] && apt-cache show crowdsec-firewall-bouncer-nftables >/dev/null 2>&1; then
    bouncer_pkg="crowdsec-firewall-bouncer-nftables"
  else
    bouncer_pkg="crowdsec-firewall-bouncer-iptables"
  fi
  if ! dpkg -s "$bouncer_pkg" >/dev/null 2>&1; then
    _spin "apt install $bouncer_pkg" \
      apt-get install -y -qq --no-install-recommends "$bouncer_pkg"
  else
    _log "$bouncer_pkg already installed"
  fi

  # Patch /etc/crowdsec/config.yaml so LAPI binds on a Unix socket
  # (ADR-0050) at /run/crowdsec/api.sock instead of 127.0.0.1:8080
  # (which conflicts with Stalwart admin-http per ADR-0050). yq is the
  # Python jq-wrapper flavor (kislyuk/yq) on Debian — `-y -i` for
  # in-place YAML output.
  local cs_cfg="/etc/crowdsec/config.yaml"
  if [[ ! -f "$cs_cfg" ]]; then
    _die "crowdsec base package did not write $cs_cfg — abort before patching"
  fi
  local socket_path="/run/crowdsec/api.sock"
  # M27 fix — LAPI must ALSO listen on TCP loopback. The AppSec engine
  # authenticates incoming bouncer keys by calling LAPI itself via the
  # client URL in local_api_credentials.yaml. CrowdSec's HTTP client
  # doesn't parse a raw socket path as a URL — it concatenates as
  # `<socket>v1/decisions/stream` and bombs out with "unsupported
  # protocol scheme \"\"". Result: every nginx-bouncer → AppSec call
  # 401's silently. Adding a TCP listener + pointing the client URL
  # there fixes auth without removing the socket (cscli still works
  # over TCP, panel-agent still uses cscli unchanged).
  local lapi_tcp="127.0.0.1:8081"
  local current_socket current_uri
  current_socket="$(yq -r '.api.server.listen_socket // ""' "$cs_cfg" 2>/dev/null || echo "")"
  current_uri="$(yq -r '.api.server.listen_uri // ""' "$cs_cfg" 2>/dev/null || echo "")"
  if [[ "$current_socket" != "$socket_path" || "$current_uri" != "$lapi_tcp" ]]; then
    _log "patching $cs_cfg: listen_socket=$socket_path + listen_uri=$lapi_tcp"
    yq -y -i ".api.server.listen_socket = \"$socket_path\" | .api.server.listen_uri = \"$lapi_tcp\"" "$cs_cfg"
  else
    _log "$cs_cfg already pinned to socket $socket_path + tcp $lapi_tcp"
  fi

  # cscli + the in-process watcher both read
  # /etc/crowdsec/local_api_credentials.yaml for the LAPI endpoint. The
  # base package writes `url: http://127.0.0.1:8080` (Stalwart's port —
  # would crash the agent). M27 fix: point at the TCP loopback above so
  # the AppSec engine can parse it as a real URL. cscli works fine over
  # TCP loopback (verified on VM 192.168.100.150).
  local creds="/etc/crowdsec/local_api_credentials.yaml"
  local lapi_url="http://${lapi_tcp}/"
  if [[ -f "$creds" ]]; then
    local current_url
    current_url="$(yq -r '.url // ""' "$creds" 2>/dev/null || echo "")"
    if [[ "$current_url" != "$lapi_url" ]]; then
      _log "patching $creds: url = $lapi_url"
      yq -y -i ".url = \"$lapi_url\"" "$creds"
    fi
  fi

  # systemd drop-in: RuntimeDirectory creates /run/crowdsec (cleared on
  # stop), Group=jabali so panel-api (group jabali) can talk to LAPI
  # via cscli. Mode 0750 on the runtime dir + service-managed socket
  # mode (CrowdSec sets 0660 on the socket itself).
  local dropin_dir="/etc/systemd/system/crowdsec.service.d"
  local dropin="$dropin_dir/10-jabali-socket.conf"
  local desired_dropin=$'# Managed by jabali install.sh — M26. Do NOT hand-edit.\n# Pins CrowdSec LAPI to /run/crowdsec/api.sock so panel-api (group\n# jabali) can reach it via cscli without TCP loopback (ADR-0050).\n# ExecStartPost pins the socket to 0660 jabali (CrowdSec creates it\n# at 0755 by default which leaks connect(2) reach to any local user).\n[Service]\nRuntimeDirectory=crowdsec\nRuntimeDirectoryMode=0750\nGroup=jabali\nExecStartPost=/bin/sh -c \'until [ -S /run/crowdsec/api.sock ]; do sleep 0.1; done\'\nExecStartPost=/bin/chmod 0660 /run/crowdsec/api.sock\nExecStartPost=/bin/chgrp jabali /run/crowdsec/api.sock\n'
  install -d -m 0755 "$dropin_dir"

  # Pre-clean bad appsec config before any start/restart attempt.
  # jabali-appsec.yaml from a prior partial install may carry
  # crowdsecurity/base-config in inband_rules. base-config is an
  # appsec-CONFIG, not an appsec-rule — CrowdSec rejects it and fails
  # to start. The definitive migration lives in install_crowdsec_appsec(),
  # but that runs AFTER us; clean it here so the first start succeeds.
  local _appsec_cfg="/etc/crowdsec/appsec-configs/jabali-appsec.yaml"
  if [[ -f "$_appsec_cfg" ]] && grep -q 'crowdsecurity/base-config' "$_appsec_cfg"; then
    _log "pre-cleaning crowdsecurity/base-config from $_appsec_cfg"
    sed -i '/crowdsecurity\/base-config/d' "$_appsec_cfg"
  fi

  if [[ ! -f "$dropin" ]] || ! cmp -s <(printf '%s' "$desired_dropin") "$dropin"; then
    _log "writing $dropin"
    local tmp
    tmp="$(mktemp --tmpdir jabali-cs-dropin.XXXXXX)"
    printf '%s' "$desired_dropin" >"$tmp"
    install -m 0644 -o root -g root "$tmp" "$dropin"
    rm -f "$tmp"
    systemctl daemon-reload
    if ! systemctl restart crowdsec; then
      _err "CrowdSec failed to restart after drop-in update — last 30 journal lines:"
      journalctl -u crowdsec -n 30 --no-pager >&2 || true
      return 1
    fi
  elif ! systemctl is-active --quiet crowdsec; then
    if ! systemctl start crowdsec; then
      _err "CrowdSec failed to start — last 30 journal lines:"
      journalctl -u crowdsec -n 30 --no-pager >&2 || true
      return 1
    fi
  else
    _log "crowdsec drop-in already current — no restart needed"
  fi

  # Wait for the LAPI socket to come up. crowdsec.service reports active
  # the moment the systemd cgroup spawns; the LAPI socket appears a beat
  # later as the agent goroutine binds.
  local i
  for i in 1 2 3 4 5 6 7 8 9 10; do
    if [[ -S "$socket_path" ]]; then break; fi
    sleep 1
  done
  if [[ ! -S "$socket_path" ]]; then
    _die "$socket_path did not appear after CrowdSec restart; check journalctl -u crowdsec"
  fi

  if cscli lapi status >/dev/null 2>&1; then
    _ok "CrowdSec LAPI live at $socket_path"
  else
    _warn "cscli lapi status non-zero — surface via 'cscli lapi status' for details"
  fi

  # ---- idempotent firewall-bouncer API key management ----
  # The package postinst auto-registers a bouncer against 127.0.0.1:8080
  # (Stalwart's port). By the time postinst runs, LAPI is already moved to
  # the Unix socket + $lapi_tcp, so auto-registration silently fails and the
  # bouncer starts with a stale/empty key → "bouncer stream halted" on boot.
  # Prune auto-created bouncers, mint a stable 'jabali-firewall' key, and
  # patch the YAML config so the bouncer points at $lapi_tcp.
  local fw_bouncer_conf="/etc/crowdsec/bouncers/${bouncer_pkg}.yaml"
  if [[ -f "$fw_bouncer_conf" ]]; then
    # Postinst auto-names follow "cs-firewall-bouncer-<epoch>" or
    # "crowdsec-firewall-bouncer-<epoch>". Prune them — keeps
    # `cscli bouncers list` honest and avoids stale-key accumulation.
    while IFS= read -r stale; do
      [[ -z "$stale" ]] && continue
      _log "deleting auto-registered firewall bouncer '$stale'"
      cscli bouncers delete "$stale" >/dev/null 2>&1 || true
    done < <(
      cscli bouncers list -o json 2>/dev/null \
        | python3 -c 'import json,re,sys; [print(b["name"]) for b in json.load(sys.stdin) if re.match(r"^(cs|crowdsec)-firewall-bouncer-\w+$", b.get("name",""))]' 2>/dev/null
    )

    local fw_bouncer_name="jabali-firewall"
    local fw_api_key
    if cscli bouncers list -o json 2>/dev/null \
        | python3 -c "import json,sys; [sys.exit(0) for b in json.load(sys.stdin) if b.get('name')=='$fw_bouncer_name'] or sys.exit(1)" 2>/dev/null; then
      # Bouncer exists — reuse key from config; rotate if missing/blank.
      fw_api_key="$(yq -r '.api_key // ""' "$fw_bouncer_conf" 2>/dev/null | tr -d '[:space:]')"
      if [[ -z "$fw_api_key" ]]; then
        _log "bouncer '$fw_bouncer_name' exists but api_key blank in conf — rotating"
        cscli bouncers delete "$fw_bouncer_name" >/dev/null 2>&1 || true
        fw_api_key="$(cscli bouncers add "$fw_bouncer_name" -o raw 2>/dev/null)"
      fi
    else
      _log "registering '$fw_bouncer_name' bouncer with LAPI"
      fw_api_key="$(cscli bouncers add "$fw_bouncer_name" -o raw 2>/dev/null)"
    fi

    if [[ -z "$fw_api_key" ]]; then
      _warn "cscli bouncers add failed — $fw_bouncer_conf left unmanaged; check 'cscli bouncers list'"
    else
      yq -y -i ".api_key = \"$fw_api_key\" | .api_url = \"http://${lapi_tcp}/\"" "$fw_bouncer_conf"
      systemctl restart "${bouncer_pkg}.service" 2>/dev/null \
        || _warn "${bouncer_pkg}.service restart failed — check 'journalctl -u ${bouncer_pkg}'"
      # Post-restart health check: if the bouncer is still failing 3 s after
      # restart (stale key from a previous install), rotate the key and retry.
      sleep 3
      if ! systemctl is-active --quiet "${bouncer_pkg}.service"; then
        _warn "${bouncer_pkg}.service failed after restart — rotating LAPI key and retrying"
        cscli bouncers delete "$fw_bouncer_name" >/dev/null 2>&1 || true
        fw_api_key="$(cscli bouncers add "$fw_bouncer_name" -o raw 2>/dev/null)"
        if [[ -n "$fw_api_key" ]]; then
          yq -y -i ".api_key = \"$fw_api_key\" | .api_url = \"http://${lapi_tcp}/\"" "$fw_bouncer_conf"
          systemctl restart "${bouncer_pkg}.service" 2>/dev/null || true
          sleep 2
          if systemctl is-active --quiet "${bouncer_pkg}.service"; then
            _ok "crowdsec-firewall-bouncer recovered after key rotation"
          else
            _warn "crowdsec-firewall-bouncer still failing after key rotation — run 'jabali repair --auto' for diagnostics"
          fi
        else
          _warn "cscli bouncers add failed during rotation — run 'jabali repair --auto'"
        fi
      fi
      _ok "crowdsec-firewall-bouncer configured (jabali-firewall key, LAPI=$lapi_tcp)"
    fi
  else
    _warn "$fw_bouncer_conf missing after package install — firewall bouncer may need manual key setup"
  fi

  # ---- dpkg post-invoke heal hook ----
  # The crowdsec-firewall-bouncer-nftables package postinst rewrites
  # api_url back to 127.0.0.1:8080 (its built-in default) on every apt
  # upgrade. install.sh fixes it during `jabali update`, but apt runs
  # outside `jabali update` (unattended-upgrades, manual `apt upgrade`)
  # leave the bouncer crash-looping with "bouncer stream halted" until
  # the next install.sh run. Install a dpkg Post-Invoke hook so the
  # heal runs after every apt/dpkg operation.
  install_crowdsec_bouncer_apt_heal_hook

  # JAB-368: block tenant-uid access to the unauthenticated prometheus/pprof
  # listener (127.0.0.1:6060). provision_new_software is jabali-update-only, so
  # the fresh-install path calls it here too (idempotent).
  ensure_crowdsec_diag_isolation
}

# install_crowdsec_bouncer_apt_heal_hook — write the heal script +
# apt Post-Invoke wiring. Idempotent; safe to call on every install.
install_crowdsec_bouncer_apt_heal_hook() {
  local script=/usr/local/sbin/jabali-crowdsec-bouncer-apt-heal
  local apt_conf=/etc/apt/apt.conf.d/99jabali-crowdsec-bouncer-heal
  install -d -m 0755 /usr/local/sbin
  cat >"$script" <<'JABALI_BOUNCER_HEAL_EOF'
#!/bin/bash
# jabali-crowdsec-bouncer-apt-heal: re-pin crowdsec bouncer api_url after apt
# operations that may have reset it (package postinst rewrites 8080).
# Idempotent. Cheap. Never blocks apt.
set -u
fw=/etc/crowdsec/bouncers/crowdsec-firewall-bouncer.yaml
ngx=/etc/crowdsec/bouncers/crowdsec-nginx-bouncer.conf
changed_fw=0
changed_ngx=0
if [[ -f "$fw" ]] && grep -qE '^api_url:[[:space:]]+http://127\.0\.0\.1:8080/?$' "$fw"; then
  sed -i 's|^api_url:[[:space:]]\+http://127\.0\.0\.1:8080/\?|api_url: http://127.0.0.1:8081/|' "$fw"
  changed_fw=1
fi
if [[ -f "$ngx" ]] && grep -qE '^API_URL=[[:space:]]*$' "$ngx"; then
  sed -i 's|^API_URL=[[:space:]]*$|API_URL=http://127.0.0.1:8081/|' "$ngx"
  changed_ngx=1
fi
if [[ "$changed_fw" == 1 ]]; then
  systemctl restart crowdsec-firewall-bouncer.service 2>/dev/null || true
fi
if [[ "$changed_ngx" == 1 ]]; then
  if nginx -t >/dev/null 2>&1; then
    systemctl reload nginx 2>/dev/null || true
  fi
fi
exit 0
JABALI_BOUNCER_HEAL_EOF
  chmod 0755 "$script"

  install -d -m 0755 /etc/apt/apt.conf.d
  cat >"$apt_conf" <<JABALI_APT_HOOK_EOF
// Installed by jabali install.sh. Runs after every apt/dpkg operation
// to re-pin crowdsec bouncer api_url that the bouncer package postinst
// resets to its built-in default (127.0.0.1:8080) — clashes with our
// Stalwart admin pin. See install_crowdsec_bouncer_apt_heal_hook in
// install.sh. Idempotent + non-blocking.
DPkg::Post-Invoke { "[ -x $script ] && $script || true"; };
JABALI_APT_HOOK_EOF
  chmod 0644 "$apt_conf"
  _ok "crowdsec bouncer apt-heal hook installed ($script + $apt_conf)"
}

install_crowdsec_appsec() {
  # Optional AppSec layer — lets the admin Security tab push a
  # server-wide country allow/deny list enforced at L7. See
  # https://doc.crowdsec.net/docs/next/appsec/rules_examples/#5-geoblocking.
  # Install side:
  #   1. geoip-enrich parser so the runtime has Country.IsoCode
  #   2. appsec-virtual-patching collection (vpatch-* CVE rules + base-config)
  #   3. /etc/crowdsec/appsec-configs/jabali-appsec.yaml — our own
  #      appsec-CONFIG that loads vpatch-* AND carries the geoblock
  #      pre_eval hook. M27 fix: pre_eval lives in appsec-CONFIG (not
  #      appsec-rules) per upstream docs:
  #      https://doc.crowdsec.net/docs/next/appsec/rules_examples/
  #      Earlier shipped a /etc/crowdsec/appsec-rules/jabali-geoblock.yaml
  #      with pre_eval — it loaded as a rule but the hook never fired
  #      because rules use a different schema.
  #   4. /etc/crowdsec/acquis.d/jabali-appsec.yaml — AppSec listener on
  #      127.0.0.1:7422 (TCP loopback, same posture as Stalwart admin-http
  #      127.0.0.1:8080). Unix socket would be stricter but the upstream
  #      crowdsec-nginx-bouncer's Lua HTTP client doesn't speak unix.
  # Nginx enforcement ships via install_crowdsec_nginx_bouncer below
  # (the upstream crowdsec-nginx-bouncer package). Every vhost gets
  # AppSec evaluation automatically — no per-vhost snippet required.
  _log "configuring CrowdSec AppSec (server-wide geoblock rule)"
  # Path to the appsec-CONFIG referenced by the acquis written below.
  # install_crowdsec() declared the same path as a `local` on line 5410,
  # which is function-scoped and invisible from here — referencing it
  # under `set -u` aborted the installer at line 5701 (the [[ -f $cfg ]]
  # presence-gate added by the GH#109 "appsec-before-binary" fix). The
  # gate is load-bearing on a fresh install (it runs before
  # clone_or_update_repo + build_backend, so the binary that renders
  # this config does not exist yet); the variable just needs to be
  # re-declared in this scope so set -u doesn't kill us before the
  # gate can fire.
  local _appsec_cfg="/etc/crowdsec/appsec-configs/jabali-appsec.yaml"

  # Flat rules dir — fixed 2026-05-20. Stale crowdsecurity/ subdir
  # path made every `jabali update` purge+reinstall 170 vpatch rules.
  local _appsec_rules_dir="/etc/crowdsec/appsec-rules"

  # 1. GeoIP enricher — prereq for GeoIPEnrich expr.
  if ! cscli parsers list 2>/dev/null | grep -q 'crowdsecurity/geoip-enrich'; then
    _spin "cscli parsers install geoip-enrich" \
      cscli parsers install crowdsecurity/geoip-enrich
  fi

  # 2. AppSec base collections — virtual-patching gives us vpatch-*
  #    CVE rules + base-config plumbing; appsec-generic-rules adds CRS-style
  #    SSTI / WordPress upload / no-user-agent detection (enabled by default
  #    2026-04-26 — see plans/m27-crowdsec-extensions.md). Both are free
  #    upstream collections.
  if ! compgen -G "${_appsec_rules_dir}/vpatch-*" >/dev/null 2>&1; then
    cscli collections remove crowdsecurity/appsec-virtual-patching --purge 2>/dev/null || true
    _spin "cscli collections install appsec-virtual-patching" \
      cscli collections install crowdsecurity/appsec-virtual-patching
  fi
  if ! compgen -G "${_appsec_rules_dir}/generic-*" >/dev/null 2>&1; then
    cscli collections remove crowdsecurity/appsec-generic-rules --purge 2>/dev/null || true
    _spin "cscli collections install appsec-generic-rules" \
      cscli collections install crowdsecurity/appsec-generic-rules
  fi

  # WordPress collection — wp-login brute force, xmlrpc abuse,
  # plugin/theme CVE exploits. Installed by default since jabali ships
  # WordPress as the primary 1-click app (M10) and ~80% of tenant sites
  # are WP-based. Operators can remove via the Recommended hub picker
  # if they don't run WP.
  if ! cscli collections list 2>/dev/null | grep -q 'crowdsecurity/wordpress'; then
    _spin "cscli collections install wordpress" \
      cscli collections install crowdsecurity/wordpress --force
  fi

  # nginx collection — access log parsers required for the jabali-nginx-logs
  # acquisition (step 5 below). Installs crowdsecurity/nginx-logs parser so
  # CrowdSec can read COMBINED-format access logs.
  if ! cscli collections list 2>/dev/null | grep -q 'crowdsecurity/nginx'; then
    _spin "cscli collections install nginx" \
      cscli collections install crowdsecurity/nginx
  fi

  # sshd collection — SSH brute-force detection (M26). Debian 13 ships sshd
  # as a non-socket-activated service whose logs go to journald only (no
  # /var/log/auth.log). The journalctl acquisition below feeds the parser.
  if ! cscli collections list 2>/dev/null | grep -q 'crowdsecurity/sshd'; then
    _spin "cscli collections install sshd" \
      cscli collections install crowdsecurity/sshd
  fi

  if ! cscli collections list 2>/dev/null | grep -q 'crowdsecurity/linux'; then
    _spin "cscli collections install linux" \
      cscli collections install crowdsecurity/linux
  fi

  if ! cscli collections list 2>/dev/null | grep -q 'crowdsecurity/mysql'; then
    _spin "cscli collections install mysql" \
      cscli collections install crowdsecurity/mysql
  fi

  # Extra WP scenarios not bundled in the wordpress collection.
  # http-bf-wordpress_bf_xmlrpc: Hub warns some plugins use xmlrpc (not in
  # collection by default). Jabali blocks xmlrpc.php at nginx (M43), so this
  # scenario provides CrowdSec signal if that block is lifted or bypassed.
  if ! cscli scenarios list 2>/dev/null | grep -q 'crowdsecurity/http-bf-wordpress_bf_xmlrpc'; then
    _spin "cscli scenarios install http-bf-wordpress_bf_xmlrpc" \
      cscli scenarios install crowdsecurity/http-bf-wordpress_bf_xmlrpc
  fi

  # WordPress AppSec WAF rules — virtual patching for 24+ WP CVEs. Inband
  # rules are loaded automatically via the vpatch-* wildcard in jabali-appsec.yaml.
  if ! cscli collections list 2>/dev/null | grep -q 'crowdsecurity/appsec-wordpress'; then
    _spin "cscli collections install appsec-wordpress" \
      cscli collections install crowdsecurity/appsec-wordpress
  fi

  # HTTP DoS detection — cache bypass, random URI flooding, UA switching.
  # Behavioral (log-based); complements AppSec inband rules.
  if ! cscli collections list 2>/dev/null | grep -q 'crowdsecurity/http-dos'; then
    _spin "cscli collections install http-dos" \
      cscli collections install crowdsecurity/http-dos
  fi

  # Generic HTTP probe detection — crawling, bad UAs, path traversal,
  # sensitive-file access, SQLi/XSS probing, backdoor probing, admin
  # interface probing, technology probing, CVE probing. Catches what's
  # not WordPress-specific (Joomla/phpMyAdmin/custom PHP/static probes).
  # Parser-side off nginx logs we already ingest — zero extra cost.
  if ! cscli collections list 2>/dev/null | grep -q 'crowdsecurity/base-http-scenarios'; then
    _spin "cscli collections install base-http-scenarios" \
      cscli collections install crowdsecurity/base-http-scenarios
  fi

  # Refresh every installed parser/scenario/collection to the hub's
  # latest tag. Idempotent: skips items already at the newest version
  # and only re-downloads what changed. Critical for sshd-logs which
  # upstream rewrote v3.1+ to handle journald MESSAGE fields directly
  # — an older shipped parser sees the bare journal text as a non-
  # syslog line and emits 0/N parsed. Observed on mx.jabali-panel.local
  # 2026-05-12 (45 lines read, 0 parsed). Pulling --force ensures the
  # journald-aware parser is loaded.
  # Gated on a 24h marker file the daily timer (`jabali-crowdsec-hub-
  # refresh.timer`) also touches. install.sh re-runs that fall inside
  # the same day skip the hub-data download (~5-15 MB across mmdb +
  # whitelists + appsec rules), so `jabali update` stays cheap. The
  # timer guarantees freshness; install.sh only runs the inline
  # refresh on fresh installs OR when the timer hasn't fired yet
  # (e.g., immediately after first boot before the 03:15 UTC slot).
  _stamp=/var/lib/jabali/crowdsec-hub-refreshed.stamp
  if [[ -f "$_stamp" ]] && [[ $(find "$_stamp" -mmin -1440 2>/dev/null | wc -l) -ge 1 ]]; then
    _log "CrowdSec hub last refreshed $(stat -c %y "$_stamp" 2>/dev/null | cut -d. -f1) — skipping (daily timer covers it)"
  else
    _log "refreshing CrowdSec hub items (parsers/scenarios/collections)"
    cscli hub update --error 2>&1 | sed 's/^/    /' || true
    cscli hub upgrade --force 2>&1 | sed 's/^/    /' || \
      _warn "cscli hub upgrade non-zero — operator can re-run manually"
    install -d -m 0755 /var/lib/jabali
    touch "$_stamp"
  fi

  # Install daily hub-refresh timer so future updates don't have to
  # re-download the data files on every `jabali update`. Persistent=true
  # catches up on missed runs (host was off during the slot).
  local _cs_hub_svc_src="${REPO_DIR}/install/systemd/jabali-crowdsec-hub-refresh.service"
  local _cs_hub_tmr_src="${REPO_DIR}/install/systemd/jabali-crowdsec-hub-refresh.timer"
  if [[ -f "$_cs_hub_svc_src" && -f "$_cs_hub_tmr_src" ]]; then
    install -m 0644 -o root -g root "$_cs_hub_svc_src" /etc/systemd/system/jabali-crowdsec-hub-refresh.service
    install -m 0644 -o root -g root "$_cs_hub_tmr_src" /etc/systemd/system/jabali-crowdsec-hub-refresh.timer
    systemctl daemon-reload
    systemctl enable --now jabali-crowdsec-hub-refresh.timer >/dev/null 2>&1 || \
      _warn "jabali-crowdsec-hub-refresh.timer enable failed — check 'journalctl -u jabali-crowdsec-hub-refresh.timer'"
    _ok "CrowdSec hub refresh timer installed (daily 03:15 UTC + 4h jitter)"
  fi

  # 3. Jabali AppSec config (ADR-0102 / ADR-0107 / ADR-0083 single-
  # source). The whole schema — header, presence-gated inband_rules,
  # the panel-API on_match allowlist, the per-mode geoblock pre_eval —
  # is rendered by ONE Go function (`internal/appseccfg.Render`,
  # called via `jabali appsec render-config --reconcile`). The agent
  # geoblock handler calls the SAME Render so the two writers can
  # never drift (the original `feedback_cross_boundary_contracts` scar
  # plans/appsec-config-single-source.md was written for).
  #
  # --reconcile reads the operator header (jabali-mode/jabali-countries)
  # from the existing file and preserves it; on a fresh install the
  # defaults are mode=off / no countries. Inband is presence-gated by
  # stat()ing /etc/crowdsec/appsec-rules/. Write-on-diff (prints
  # "unchanged" when nothing changed); `_cs_dirty=1` triggers the
  # crowdsec reload at the end of this function (PR #55).
  install -d -m 0755 /etc/crowdsec/appsec-configs
  local _appsec_out
  if _appsec_out="$(/usr/local/bin/jabali-panel appsec render-config --reconcile 2>&1)"; then
    _log "jabali-appsec config: ${_appsec_out}"
    if [[ "$_appsec_out" != "unchanged" ]]; then
      _cs_dirty=1
    fi
  else
    _warn "jabali-panel appsec render-config failed: ${_appsec_out}"
  fi

  # Cleanup: M26-era /etc/crowdsec/appsec-rules/jabali-geoblock.yaml is
  # superseded by the appsec-config above. Schema was wrong (pre_eval in
  # a rule file is silently ignored).
  if [[ -f /etc/crowdsec/appsec-rules/jabali-geoblock.yaml ]]; then
    _log "removing legacy /etc/crowdsec/appsec-rules/jabali-geoblock.yaml"
    rm -f /etc/crowdsec/appsec-rules/jabali-geoblock.yaml
  fi

  # 4. AppSec acquisition — listener on 127.0.0.1:7422 (the CrowdSec
  #    convention; bouncer talks to it over loopback). Points at our
  #    jabali-appsec config (NOT virtual-patching) so the agent can
  #    inject the geoblock pre_eval hook.
  #
  # GATE (fresh-install ordering fix, GH discussion #109): the acquis
  # references appsec_config: crowdsecurity/jabali-appsec. If that
  # config wasn't written above — which happens on a FRESH install
  # because install_crowdsec_appsec runs BEFORE clone_or_update_repo +
  # the Go binary build, so `jabali-panel appsec render-config` can't
  # run yet — writing the acquis makes crowdsec fail to start
  # ("datasource of type appsec: unable to load appsec_config: no
  # appsec-config found"), and `set -e` kills the whole installer.
  # Skip the acquis (+ remove any stale one) when the config is
  # absent; `jabali update` re-runs this function after the binary is
  # built and wires AppSec then.
  local acquis_dir="/etc/crowdsec/acquis.d"
  install -d -m 0755 "$acquis_dir"
  local acquis_file="$acquis_dir/jabali-appsec.yaml"
  if [[ ! -f "$_appsec_cfg" ]]; then
    _warn "appsec config not present yet (binary not built — fresh install); skipping AppSec acquis. 'jabali update' will wire it after the build."
    rm -f "$acquis_file"   # drop any stale acquis from a prior partial run
  else
  local desired_acquis=$'# Managed by jabali install.sh — M27 AppSec geoblock.\n# TCP loopback listener. crowdsec-nginx-bouncer dials this via\n# APPSEC_URL=http://127.0.0.1:7422. Not exposed outside the host.\nappsec_config: crowdsecurity/jabali-appsec\nlabels:\n  type: appsec\nlisten_addr: 127.0.0.1:7422\nsource: appsec\n'
  if [[ ! -f "$acquis_file" ]] || ! cmp -s <(printf '%s' "$desired_acquis") "$acquis_file"; then
    _log "writing $acquis_file"
    local tmp
    tmp="$(mktemp --tmpdir jabali-appsec-acquis.XXXXXX)"
    printf '%s' "$desired_acquis" >"$tmp"
    install -m 0644 -o root -g root "$tmp" "$acquis_file"
    rm -f "$tmp"
  fi
  fi

  # 5. Nginx access-log acquisition — feeds *.access.log (COMBINED format)
  #    to the crowdsecurity/nginx parser installed above. Error logs are
  #    intentionally excluded: they use a different format and caused ~96%
  #    parse failures when the default acquis.yaml glob matched both
  #    *.access.log and *.error.log.
  local nginx_acquis_file="$acquis_dir/jabali-nginx-logs.yaml"
  # JAB-250: per-domain tenant logs are written as <domain>-access.log
  # (hyphen) by the panel vhost template; the old dot-only glob
  # (*.access.log) matched ONLY the infra logs and NONE of the customer
  # sites, so every log-based scenario (WordPress brute-force, http-dos,
  # http-cve, base-http) was starved. Both spellings are globbed now:
  # *-access.log catches tenant + preview vhosts, *.access.log keeps the
  # infra logs. Do NOT collapse back to one pattern. jcache-*.log and
  # *error.log stay excluded — different (non-COMBINED) formats that only
  # produce unparsed lines.
  local desired_nginx_acquis=$'# Managed by jabali install.sh.\n# Per-domain nginx access logs in COMBINED format (JAB-250).\n# Tenant/preview vhosts write <domain>-access.log (hyphen); infra logs\n# write <name>.access.log (dot). Both are globbed. *error.log and\n# jcache-*.log are excluded — different formats.\nfilenames:\n  - /var/log/nginx/*-access.log\n  - /var/log/nginx/*.access.log\nlabels:\n  type: nginx\n'
  if [[ ! -f "$nginx_acquis_file" ]] || ! cmp -s <(printf '%s' "$desired_nginx_acquis") "$nginx_acquis_file"; then
    _log "writing $nginx_acquis_file"
    local tmp2
    tmp2="$(mktemp --tmpdir jabali-nginx-acquis.XXXXXX)"
    printf '%s' "$desired_nginx_acquis" >"$tmp2"
    install -m 0644 -o root -g root "$tmp2" "$nginx_acquis_file"
    rm -f "$tmp2"
  fi
  # 6. sshd journalctl acquisition — feeds sshd log events from journald.
  #    Debian 13: sshd logs to journald only (no /var/log/auth.log).
  #    Debian 13 ships OpenSSH 9.x in split mode: the listener logs as
  #    SYSLOG_IDENTIFIER=sshd, but per-connection workers log as
  #    SYSLOG_IDENTIFIER=sshd-session (where failed-password, invalid-user
  #    and preauth events live). Without sshd-session in the filter,
  #    CrowdSec sees only the listener's bind/exit lines (~2-5% of events)
  #    and misses ~95-98% of brute-force attempts. journalctl OR-combines
  #    repeated identifier filters at the same field, so listing both
  #    captures full coverage. type: syslog so crowdsecurity/sshd parser fires.
  local sshd_acquis_file="$acquis_dir/jabali-sshd.yaml"
  local desired_sshd_acquis=$'# Managed by jabali install.sh — M26 SSH brute-force detection.\n# Debian 13 OpenSSH split mode: listener = sshd, per-connection worker\n# = sshd-session (where Failed/Invalid/preauth events live). Repeated\n# SYSLOG_IDENTIFIER= entries are OR-combined by journalctl, so both\n# identifiers must be listed to catch every brute-force attempt.\nsource: journalctl\njournalctl_filter:\n  - "SYSLOG_IDENTIFIER=sshd"\n  - "SYSLOG_IDENTIFIER=sshd-session"\nlabels:\n  type: syslog\n'
  if [[ ! -f "$sshd_acquis_file" ]] || ! cmp -s <(printf '%s' "$desired_sshd_acquis") "$sshd_acquis_file"; then
    _log "writing $sshd_acquis_file"
    local tmp3
    tmp3="$(mktemp --tmpdir jabali-sshd-acquis.XXXXXX)"
    printf '%s' "$desired_sshd_acquis" >"$tmp3"
    install -m 0644 -o root -g root "$tmp3" "$sshd_acquis_file"
    rm -f "$tmp3"
  fi

  # Remove auto-generated setup.*.yaml acquis files — they duplicate jabali's
  # own jabali-*.yaml configs and cause double-processing of SSH/nginx logs.
  # cscli setup generates these on initial install but jabali owns acquis config.
  for _setup_f in "$acquis_dir"/setup.*.yaml; do
    [[ -f "$_setup_f" ]] || continue
    _log "removing duplicate cscli-generated acquis: $_setup_f"
    rm -f "$_setup_f"
  done

  # JAB-250: self-IP whitelist. Now that tenant nginx logs are actually
  # parsed, the host's OWN public IP appears in those logs (wp-cron and
  # other server-to-server self-calls hit the site over its public
  # address), so without this the host can ban ITSELF the moment a
  # volumetric scenario fires. crowdsecurity/whitelists already covers
  # localhost + RFC1918; this adds only the public IPs, which it does not.
  # Operators add monitoring/uptime probes to the local override file
  # noted below. Union of DB-confirmed (server_settings) and live-detected
  # addresses so a NAT'd box whose iface IP differs from its public IP is
  # still covered.
  local wl_parser_dir="/etc/crowdsec/parsers/s02-enrich"
  local wl_file="$wl_parser_dir/jabali-self-whitelist.yaml"
  local -a wl_ips=()
  local _ip
  for _ip in "${JABALI_SRV_IPV4:-}" "${JABALI_SRV_IPV6:-}" \
             "$(_detect_public_ipv4 2>/dev/null || true)" \
             "$(_detect_public_ipv6 2>/dev/null || true)"; do
    _ip="${_ip%%/*}"                       # strip any /prefix
    [[ -z "$_ip" || "$_ip" == "null" ]] && continue
    # dedupe
    local _seen=0 _e
    for _e in "${wl_ips[@]}"; do [[ "$_e" == "$_ip" ]] && _seen=1 && break; done
    [[ "$_seen" == 0 ]] && wl_ips+=("$_ip")
  done
  if (( ${#wl_ips[@]} > 0 )); then
    install -d -m 0755 "$wl_parser_dir"
    local wl_body _line
    wl_body=$'# Managed by jabali install.sh (JAB-250). Do NOT hand-edit.\n'
    wl_body+=$'# Whitelists this host\'s own public IPs: server-to-server\n'
    wl_body+=$'# self-calls (wp-cron etc.) appear with the public IP in\n'
    wl_body+=$'# tenant nginx logs, and would otherwise trip a scenario and\n'
    wl_body+=$'# ban the host. localhost + RFC1918 are covered by\n'
    wl_body+=$'# crowdsecurity/whitelists. Add monitoring/uptime-probe IPs to\n'
    wl_body+=$'# /etc/crowdsec/parsers/s02-enrich/jabali-local-whitelist.yaml\n'
    wl_body+=$'# (never overwritten by jabali update).\n'
    wl_body+=$'name: jabali/self-whitelist\n'
    wl_body+=$'description: "Whitelist the host\'s own public IPs (JAB-250)"\n'
    wl_body+=$'whitelist:\n'
    wl_body+=$'  reason: "jabali host self-calls (public IP in tenant logs)"\n'
    wl_body+=$'  ip:\n'
    for _line in "${wl_ips[@]}"; do
      wl_body+="    - \"${_line}\""$'\n'
    done
    if [[ ! -f "$wl_file" ]] || ! cmp -s <(printf '%s' "$wl_body") "$wl_file"; then
      _log "writing $wl_file (${#wl_ips[@]} self IPs)"
      local wl_tmp
      wl_tmp="$(mktemp --tmpdir jabali-cs-wl.XXXXXX)"
      printf '%s' "$wl_body" >"$wl_tmp"
      install -m 0644 -o root -g root "$wl_tmp" "$wl_file"
      rm -f "$wl_tmp"
    fi
  else
    _warn "JAB-250: could not determine host public IP — self-whitelist skipped (add IPs to jabali-local-whitelist.yaml by hand)"
  fi

  # Narrow default CrowdSec acquis.yaml nginx glob if it still matches *.log
  # (access + error together). Error log format breaks the nginx parser.
  # JAB-250: narrow to *-access.log AND *.access.log (both spellings) so
  # tenant logs (<domain>-access.log) are covered here too if this dormant
  # default glob ever fires — narrowing to the dot-only pattern re-broke
  # exactly the coverage this ticket restores.
  local default_acquis="/etc/crowdsec/acquis.yaml"
  if [[ -f "$default_acquis" ]] && grep -q '/var/log/nginx/\*\.log' "$default_acquis"; then
    _log "narrowing nginx glob in $default_acquis to *-access.log + *.access.log"
    sed -i 's|/var/log/nginx/\*\.log|/var/log/nginx/*-access.log\n  - /var/log/nginx/*.access.log|g' "$default_acquis"
  fi

  # Remove legacy appsec.sock ExecStartPost lines if the previous
  # socket-based install left them — they'd block startup now that
  # AppSec binds TCP (the `until [ -S ... ]` loop would never fire).
  local dropin="/etc/systemd/system/crowdsec.service.d/10-jabali-socket.conf"
  if [[ -f "$dropin" ]] && grep -q 'appsec.sock' "$dropin"; then
    _log "purging legacy appsec.sock ExecStartPost from $dropin"
    sed -i '/appsec\.sock/d' "$dropin"
    systemctl daemon-reload
  fi

  # Validate the config (parsers + acquisition) BEFORE reloading, so a
  # malformed whitelist/acquis drop-in can't take log parsing down — a
  # broken s02-enrich parser is worse than the coverage bug (JAB-250).
  if command -v crowdsec >/dev/null 2>&1 && ! crowdsec -t >/dev/null 2>&1; then
    _err "CrowdSec config test (crowdsec -t) failed — NOT reloading. Errors:"
    crowdsec -t 2>&1 | tail -20 >&2 || true
    return 1
  fi

  # Reload or restart to pick up acquis + config changes.
  if ! { systemctl reload crowdsec 2>/dev/null || systemctl restart crowdsec; }; then
    _err "CrowdSec failed to reload/restart — last 30 journal lines:"
    journalctl -u crowdsec -n 30 --no-pager >&2 || true
    return 1
  fi

  # Wait for AppSec TCP listener to come up — `ss -lnt sport = :7422`
  # is the signal the goroutine bound. Cap at 10s.
  local i
  for i in 1 2 3 4 5 6 7 8 9 10; do
    if ss -lnt 'sport = :7422' 2>/dev/null | grep -q 127.0.0.1; then break; fi
    sleep 1
  done
  if ss -lnt 'sport = :7422' 2>/dev/null | grep -q 127.0.0.1; then
    _ok "CrowdSec AppSec live at 127.0.0.1:7422"
  else
    _warn "CrowdSec AppSec listener did not appear on :7422 — check journalctl -u crowdsec"
  fi

  # Reload crowdsec once if any hub mutation happened in this function.
  # cscli writes hub state but does not signal crowdsec; without this
  # the operator was left running `systemctl reload crowdsec` by hand
  # after every `jabali update` that touched the hub (recurring scar:
  # PR #45 deploy-hook / #49 appsec-config / #54 db-user-import).
  if [[ ${_cs_dirty:-0} -eq 1 ]]; then
    _log "crowdsec: hub mutated by install_crowdsec_appsec — reloading"
    systemctl reload crowdsec 2>/dev/null || systemctl restart crowdsec 2>/dev/null || true
  fi
}

install_crowdsec_nginx_bouncer() {
  # Upstream crowdsec-nginx-bouncer (Lua-based access_by_lua_block)
  # wires every HTTP request into the AppSec engine automatically —
  # no per-vhost auth_request snippet needed. See ADR-0060.
  #
  # Configuration uses `API_URL=""` + `APPSEC_URL=http://127.0.0.1:7422`
  # so the bouncer is AppSec-only. LAPI-sourced L3/L4 decisions stay
  # the province of crowdsec-firewall-bouncer-nftables (installed in
  # install_crowdsec) — running nginx-bouncer alongside firewall-
  # bouncer for LAPI decisions would double-enforce with no added
  # benefit.
  _log "configuring crowdsec-nginx-bouncer (AppSec enforcement)"

  # crowdsec-nginx-bouncer is Lua-based (lua_package_path +
  # access_by_lua_block in /etc/nginx/conf.d/crowdsec_nginx.conf). The
  # nginx Lua module + lua-resty libs are only apt *Recommends*, so an
  # apt-autoremove (notably after a prior --uninstall --purge-packages)
  # strips them; a reinstall with --no-install-recommends then leaves
  # nginx -t failing with `unknown directive "lua_package_path"` and
  # aborts the install. Install them explicitly + idempotently,
  # independent of the bouncer's own install state — the bouncer can be
  # "already installed" while the Lua module was autoremoved out from
  # under it (feedback_deps_in_installer).
  _spin "apt install nginx Lua deps (crowdsec bouncer)" \
    apt-get install -y -qq --no-install-recommends \
      libnginx-mod-http-lua lua-resty-core lua-resty-lrucache

  if ! dpkg -s crowdsec-nginx-bouncer >/dev/null 2>&1; then
    _spin "apt install crowdsec-nginx-bouncer" \
      apt-get install -y -qq --no-install-recommends crowdsec-nginx-bouncer
  else
    _log "crowdsec-nginx-bouncer already installed"
  fi

  local bouncer_conf="/etc/crowdsec/bouncers/crowdsec-nginx-bouncer.conf"
  if [[ ! -f "$bouncer_conf" ]]; then
    _warn "$bouncer_conf missing after package install — bouncer postinst may have failed"
    return
  fi

  # The crowdsec-nginx-bouncer postinst auto-registers a bouncer with
  # a random `crowdsec-nginx-bouncer-<epoch>` name and writes its key
  # into the conf file. We don't use that bouncer (we use 'jabali-nginx'
  # below), so prune any auto-created ones to keep `cscli bouncers list`
  # honest. Only delete bouncers that match the upstream auto-name
  # pattern — never touch operator-added ones.
  while IFS= read -r stale; do
    [[ -z "$stale" ]] && continue
    _log "deleting auto-registered upstream bouncer '$stale'"
    cscli bouncers delete "$stale" >/dev/null 2>&1 || true
  done < <(
    cscli bouncers list -o json 2>/dev/null \
      | python3 -c 'import json,re,sys; [print(b["name"]) for b in json.load(sys.stdin) if re.match(r"^crowdsec-nginx-bouncer-\d+$", b.get("name",""))]' 2>/dev/null
  )

  # Mint an API key via cscli if one isn't already registered for us.
  # Bouncer name pinned (not SUFFIX-randomised) so repeated install
  # runs don't accumulate stale bouncers.
  local bouncer_name="jabali-nginx"
  local api_key
  if cscli bouncers list -o json 2>/dev/null | python3 -c "import json,sys; [sys.exit(0) for b in json.load(sys.stdin) if b.get('name')=='$bouncer_name'] or sys.exit(1)" 2>/dev/null; then
    # Bouncer exists — reuse the key already in the config file (the
    # package postinst or our previous run set it).
    api_key="$(awk -F= '/^API_KEY=/{print $2; exit}' "$bouncer_conf" | tr -d '[:space:]')"
    if [[ -z "$api_key" ]]; then
      _log "bouncer '$bouncer_name' exists but API_KEY missing in conf — rotating"
      cscli bouncers delete "$bouncer_name" >/dev/null 2>&1 || true
      api_key="$(cscli bouncers add "$bouncer_name" -o raw 2>/dev/null)"
    fi
  else
    _log "registering '$bouncer_name' bouncer with LAPI"
    api_key="$(cscli bouncers add "$bouncer_name" -o raw 2>/dev/null)"
  fi
  if [[ -z "$api_key" ]]; then
    _warn "cscli bouncers add failed — $bouncer_conf left unmanaged"
    return
  fi

  # Rewrite bouncer config. API_URL points at LAPI loopback so the
  # bouncer heartbeats up to CrowdSec Central (otherwise console flags
  # "no working remediation components"). APPSEC_URL +
  # ALWAYS_SEND_TO_APPSEC=true = every request goes through AppSec
  # pre_eval too. APPSEC_FAILURE_ACTION=passthrough means an AppSec
  # outage doesn't 403 every request.
  local desired_conf
  desired_conf=$(cat <<EOF
# Managed by jabali install.sh — M26 AppSec enforcement.
# DO NOT hand-edit. Re-run install.sh to rotate API_KEY.
ENABLED=true
API_URL=http://127.0.0.1:8081/
API_KEY=$api_key
USE_TLS_AUTH=false
CACHE_EXPIRATION=1
# Enforce ONLY active remediation types. "all" causes the bouncer to
# treat ANY decision present for an IP (including type=whitelist) as
# grounds for the fallback remediation (ban) — every operator-issued
# \`cscli decisions add --type whitelist\` then BLOCKS the IP instead of
# allowing it. Reproduced 2026-05-25 on testserver: whitelisting an
# admin's home IP returned HTTP 403 CrowdSec Ban on every request until
# this value was narrowed.
#
# The nginx Lua bouncer takes a SINGLE value here (ban | captcha | all),
# NOT the firewall bouncer's comma-list — \`ban,captcha\` is rejected as
# "unsupported value" and silently falls back to \`ban\`, spamming the
# nginx error log on every reload (GH #212). We use \`ban\` directly:
# identical effective behaviour, no log spam. (Captcha remediation also
# needs recaptcha keys the panel doesn't provision, so it was never
# actually enforced via this list.)
BOUNCING_ON_TYPE=ban
FALLBACK_REMEDIATION=ban
REQUEST_TIMEOUT=3000
# UPDATE_FREQUENCY is in seconds. Default 10s = 6 LAPI polls/min;
# with CAPI loaded (~30k decisions on production hosts) each poll
# does a full SQLite scan-and-diff. Sustained ~6-8% crowdsec CPU at
# idle on puzzle 2026-06-03 (84k pread64/5s) was traced to this
# coupled with the firewall bouncer's identical 10s default. 60s
# is the upstream-recommended production value and matches the
# firewall bouncer override below — keeps mean response delay at
# ~30s while cutting LAPI load by 6x.
UPDATE_FREQUENCY=60
ENABLE_INTERNAL=false
# MODE — see panel Server-Settings -> CrowdSec -> Settings tab. Default
# 'stream' = bouncer caches all decisions in nginx Lua shared_dict and
# polls LAPI every UPDATE_FREQUENCY (60s). Cuts crowdsec CPU ~23% -> ~10%
# on small VMs by killing the per-request SQLite hit. Up-to-60s L7 ban
# lag, symmetric with firewall bouncer. Operator can flip to 'live' from
# the UI for instant-block scenarios; agent's
# security.crowdsec.bouncer.mode.apply rewrites this line in-place.
MODE=stream
EXCLUDE_LOCATION=
BAN_TEMPLATE_PATH=/var/lib/crowdsec/lua/templates/ban.html
REDIRECT_LOCATION=
RET_CODE=
CAPTCHA_PROVIDER=
SECRET_KEY=
SITE_KEY=
CAPTCHA_TEMPLATE_PATH=/var/lib/crowdsec/lua/templates/captcha.html
CAPTCHA_EXPIRATION=3600
APPSEC_URL=http://127.0.0.1:7422
APPSEC_FAILURE_ACTION=passthrough
APPSEC_CONNECT_TIMEOUT=1000
APPSEC_SEND_TIMEOUT=1000
APPSEC_PROCESS_TIMEOUT=2000
ALWAYS_SEND_TO_APPSEC=true
SSL_VERIFY=false
EOF
  )
  local tmp
  tmp="$(mktemp --tmpdir jabali-nginx-bouncer.XXXXXX)"
  printf '%s\n' "$desired_conf" >"$tmp"
  if ! cmp -s "$tmp" "$bouncer_conf"; then
    _log "writing $bouncer_conf"
    install -m 0600 -o root -g root "$tmp" "$bouncer_conf"
  fi
  rm -f "$tmp"

  # Validate + reload nginx. The bouncer package drops
  # /etc/nginx/conf.d/crowdsec_nginx.conf; nginx-t catches any
  # postinst that didn't run (Debian quirk after kernel update).
  if nginx -t >/dev/null 2>&1; then
    systemctl reload nginx || _warn "nginx reload failed — check 'systemctl status nginx'"
    _ok "crowdsec-nginx-bouncer configured (AppSec enforcement on every vhost)"
  else
    _warn "nginx -t failed after bouncer install — surface via 'nginx -t'"
  fi

  # M27 — captcha template sanity check. crowdsec-nginx-bouncer ships
  # ban.html + captcha.html in /var/lib/crowdsec/lua/templates/.
  # If they're missing the Step 5 captcha toggle still writes the
  # bouncer conf but remediation would render blank; surface as warn
  # rather than fail (package regression is upstream's problem).
  local tmpl_dir="/var/lib/crowdsec/lua/templates"
  for tmpl in ban.html captcha.html; do
    if [[ ! -f "$tmpl_dir/$tmpl" ]]; then
      _warn "$tmpl_dir/$tmpl missing — captcha remediation will render empty until reinstalled"
    fi
  done
}

ensure_crowdsec_db_ordering() {
  # JAB-217. crowdsec ships
  # `After=syslog.target network.target remote-fs.target nss-lookup.target` —
  # nothing about a database, because upstream assumes SQLite. Once
  # configure_crowdsec_mariadb repoints db_config at the MariaDB socket, boot
  # becomes a race, and when crowdsec wins it dies with
  #   FATAL unable to create database client: … dial unix
  #   /run/mysqld/mysqld.sock: connect: no such file or directory
  # It recovers only through the Restart=on-failure loop in
  # 10-jabali-restart.conf — i.e. by luck, after a window in which the LAPI is
  # down and no fresh decisions reach the edge. Same shape as the panel-api
  # After=mariadb gap; both only bite on slower boxes where MariaDB's own
  # startup is long enough to lose the race.
  #
  # Wants=, not Requires=: on a box whose MariaDB is deliberately stopped,
  # crowdsec should still start and fail loudly rather than be a unit systemd
  # silently refuses to schedule.
  local dropin_dir="/etc/systemd/system/crowdsec.service.d"
  local dropin="$dropin_dir/20-jabali-db-ordering.conf"
  local cs_cfg="/etc/crowdsec/config.yaml"

  [[ -f "$cs_cfg" ]] || return 0
  command -v yq >/dev/null 2>&1 || return 0

  local cur_type
  cur_type="$(yq -r '.db_config.type // ""' "$cs_cfg" 2>/dev/null || echo "")"
  if [[ "$cur_type" != "mysql" ]]; then
    # Someone moved back to SQLite by hand — drop the ordering rather than
    # leave crowdsec pinned to a database it no longer uses.
    if [[ -f "$dropin" ]]; then
      rm -f "$dropin"
      systemctl daemon-reload
      _log "removed crowdsec mariadb ordering drop-in (db_config is ${cur_type:-sqlite})"
    fi
    return 0
  fi

  local desired_dropin=$'# Managed by jabali install.sh — JAB-217. Do NOT hand-edit.\n# CrowdSec\'s LAPI database lives in MariaDB (configure_crowdsec_mariadb), but\n# the stock unit orders against nothing that provides it, so on boot crowdsec\n# can reach for /run/mysqld/mysqld.sock before mariadbd has created it.\n[Unit]\nAfter=mariadb.service\nWants=mariadb.service\n'

  install -d -m 0755 "$dropin_dir"
  if [[ ! -f "$dropin" ]] || ! cmp -s <(printf '%s' "$desired_dropin") "$dropin"; then
    local tmp
    tmp="$(mktemp --tmpdir jabali-cs-order.XXXXXX)"
    printf '%s' "$desired_dropin" >"$tmp"
    install -m 0644 -o root -g root "$tmp" "$dropin"
    rm -f "$tmp"
    systemctl daemon-reload
    _ok "crowdsec ordered after mariadb.service (boot race fix)"
  fi
}

ensure_crowdsec_bouncer_names() {
  # JAB-217. Heal boxes that ran the earlier revision of
  # configure_crowdsec_mariadb, which re-registered bouncers as
  # jabali-firewall-bouncer / jabali-nginx-bouncer. No other code path uses
  # those names — install_crowdsec_bouncer registers "jabali-firewall" and
  # install_crowdsec_nginx_bouncer registers "jabali-nginx" — so the box ends up
  # with two rows per bouncer sharing one API key. The LAPI only advances
  # last_pull on the row it resolves the key to, leaving the other frozen at
  # whenever it was created. An operator reading `cscli bouncers list` sees a
  # bouncer that has not pulled in weeks and concludes edge enforcement is dead.
  #
  # The order below is load-bearing. Bouncers authenticate by KEY, and
  # `cscli bouncers delete` removes that key registration — so on a box where
  # the legacy row is the only one holding the key, deleting first would break
  # authentication outright. Register the canonical name with the SAME key
  # first (the running bouncer never notices), verify it landed, then prune.
  command -v cscli >/dev/null 2>&1 || return 0

  local pair legacy canonical key
  for pair in "jabali-firewall-bouncer:jabali-firewall" "jabali-nginx-bouncer:jabali-nginx"; do
    legacy="${pair%%:*}"
    canonical="${pair##*:}"

    cscli bouncers list -o json 2>/dev/null \
      | python3 -c 'import json,sys; sys.exit(0 if any(b.get("name")==sys.argv[1] for b in json.load(sys.stdin)) else 1)' "$legacy" 2>/dev/null \
      || continue

    if [[ "$canonical" == "jabali-firewall" ]]; then
      key="$(yq -r '.api_key // ""' /etc/crowdsec/bouncers/crowdsec-firewall-bouncer.yaml 2>/dev/null || true)"
    else
      key="$(grep -oP '^API_KEY=\K.*' /etc/crowdsec/bouncers/crowdsec-nginx-bouncer.conf 2>/dev/null || true)"
    fi
    if [[ -z "$key" || "$key" == "null" ]]; then
      _warn "cannot read the $canonical API key — leaving duplicate bouncer '$legacy' in place"
      continue
    fi

    cscli bouncers add "$canonical" -k "$key" >/dev/null 2>&1 || true
    if cscli bouncers list -o json 2>/dev/null \
      | python3 -c 'import json,sys; sys.exit(0 if any(b.get("name")==sys.argv[1] for b in json.load(sys.stdin)) else 1)' "$canonical" 2>/dev/null; then
      if cscli bouncers delete "$legacy" >/dev/null 2>&1; then
        _ok "pruned duplicate crowdsec bouncer '$legacy' ('$canonical' holds the key)"
      fi
    else
      _warn "could not register canonical bouncer '$canonical' — leaving '$legacy' alone"
    fi
  done
}

configure_crowdsec_mariadb() {
  # Move CrowdSec's LAPI database from SQLite to the panel MariaDB (unix
  # socket, M25). SQLite pegged crowdsec at high CPU under the CAPI community
  # blocklist (~15k decisions): every bouncer /v1/decisions/stream poll ran
  # sqlite3_step in-process and serialized on SQLite's single global lock
  # (profiled: cgo sqlite3_step 87% + futex contention). MariaDB/InnoDB does
  # row-level locking and runs the queries in mariadbd, off the crowdsec
  # process. Decisions are ephemeral (re-pulled from CAPI in minutes) so no
  # data migration — crowdsec's ent auto-migrate builds the schema fresh on
  # first start against MariaDB. Idempotent: no-op once db_config.type=mysql.
  local cs_cfg="/etc/crowdsec/config.yaml"
  [[ -f "$cs_cfg" ]] || { _warn "crowdsec config.yaml missing — skip MariaDB db_config"; return 0; }
  command -v mariadb >/dev/null 2>&1 || { _warn "mariadb client missing — skip crowdsec MariaDB migration"; return 0; }
  command -v yq >/dev/null 2>&1 || { _warn "yq missing — skip crowdsec MariaDB migration"; return 0; }

  # Password store (hex, no YAML-special chars). Kept in db.env for stable
  # idempotent re-runs; the actual value is written INTO config.yaml (below)
  # rather than referenced as ${ENV}, because jabali runs cscli heavily from
  # panel-agent (allowlists/decisions/hub/blocklist-refresh) and those calls
  # read config.yaml directly — an EnvironmentFile only reaches crowdsec.service,
  # so an env-ref would break every non-service cscli call on MariaDB. config.yaml
  # is locked to 0640 root:root so the inlined secret isn't world-readable.
  local cs_db_env="/etc/crowdsec/db.env" cs_db_pass
  if [[ -f "$cs_db_env" ]] && grep -q '^CROWDSEC_DB_PASSWORD=' "$cs_db_env"; then
    cs_db_pass="$(. "$cs_db_env"; printf '%s' "$CROWDSEC_DB_PASSWORD")"
  else
    cs_db_pass="$(openssl rand -hex 24)"
    install -m 0640 -o root -g root /dev/null "$cs_db_env"
    printf 'CROWDSEC_DB_PASSWORD=%s\n' "$cs_db_pass" > "$cs_db_env"
    chmod 0640 "$cs_db_env"
    _ok "generated CrowdSec DB password → $cs_db_env"
  fi

  # Provision the crowdsec DB + user (idempotent). Socket connections are
  # 'localhost'; password auth (the OS user is 'crowdsec', not a DB match).
  mariadb -uroot <<SQL || { _warn "crowdsec MariaDB provisioning failed (non-fatal; retries next update)"; return 0; }
CREATE DATABASE IF NOT EXISTS crowdsec CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER IF NOT EXISTS 'crowdsec'@'localhost' IDENTIFIED BY '${cs_db_pass}';
ALTER USER 'crowdsec'@'localhost' IDENTIFIED BY '${cs_db_pass}';
GRANT ALL PRIVILEGES ON crowdsec.* TO 'crowdsec'@'localhost';
FLUSH PRIVILEGES;
SQL

  # Retire any prior env-ref drop-in from an earlier revision of this fix.
  if [[ -f /etc/systemd/system/crowdsec.service.d/20-jabali-db.conf ]]; then
    rm -f /etc/systemd/system/crowdsec.service.d/20-jabali-db.conf
    systemctl daemon-reload
  fi

  # Patch db_config → mysql over the MariaDB socket, password inlined. Protect
  # config.yaml (0640 root:root) so the secret isn't world-readable. Idempotent.
  local changed=0 cur_type cur_pass
  cur_type="$(yq -r '.db_config.type // ""' "$cs_cfg" 2>/dev/null || echo "")"
  cur_pass="$(yq -r '.db_config.password // ""' "$cs_cfg" 2>/dev/null || echo "")"
  if [[ "$cur_type" != "mysql" || "$cur_pass" != "$cs_db_pass" ]]; then
    _log "migrating crowdsec db_config: ${cur_type:-sqlite} → mysql (MariaDB socket)"
    # MERGE the mysql keys (don't replace the whole object) so an operator's
    # existing db_config.flush retention + log_level survive. Drop use_wal
    # (sqlite-only); db_path is repurposed as the mysql socket path.
    JC_CS_DBPASS="$cs_db_pass" yq -y -i '.db_config.type="mysql" | .db_config.db_path="/run/mysqld/mysqld.sock" | .db_config.user="crowdsec" | .db_config.password=env.JC_CS_DBPASS | .db_config.db_name="crowdsec" | .db_config.max_open_conns=15 | del(.db_config.use_wal)' "$cs_cfg"
    changed=1
  fi
  chown root:root "$cs_cfg" 2>/dev/null || true
  chmod 0640 "$cs_cfg" 2>/dev/null || true

  # Unconditional, and deliberately BEFORE the restart below: the boxes that
  # need the boot ordering and the bouncer-name heal are exactly the ones that
  # migrated long ago and take the "no change" branch, so gating either on
  # $changed would never reach them (JAB-217).
  ensure_crowdsec_db_ordering
  ensure_crowdsec_bouncer_names

  if [[ "$changed" == 1 ]]; then
    # cscli reads the inlined password from config.yaml (0640 root) — no env
    # needed, and the same is true for every panel-agent cscli call on MariaDB.
    # First start builds the (empty) schema via ent auto-migrate, then FATALs
    # on "machine not found" — the local agent + bouncer credentials only
    # existed in the old SQLite DB. Expected; the tables now exist so we can
    # re-seed the SAME credentials (no other config file changes).
    systemctl start crowdsec >/dev/null 2>&1 || true
    sleep 3
    local creds="/etc/crowdsec/local_api_credentials.yaml" m_login m_pass
    m_login="$(yq -r '.login // ""' "$creds" 2>/dev/null)"
    m_pass="$(yq -r '.password // ""' "$creds" 2>/dev/null)"
    if [[ -n "$m_login" && "$m_login" != "null" && -n "$m_pass" && "$m_pass" != "null" ]]; then
      cscli machines add "$m_login" --password "$m_pass" --force >/dev/null 2>&1 \
        || _warn "cscli machines add failed — crowdsec agent may not authenticate"
    fi
    # Re-register bouncers with their existing API keys so their configs are
    # untouched (bouncers authenticate by key, name is just a label).
    #
    # The names MUST match the ones install_crowdsec_bouncer /
    # install_crowdsec_nginx_bouncer register ($fw_bouncer_name="jabali-firewall",
    # $bouncer_name="jabali-nginx"). An earlier revision used "-bouncer"-suffixed
    # names that match nothing else, which left a second row per bouncer holding
    # the same key. Only one row's last_pull advances, so the other sits frozen
    # forever in `cscli bouncers list` — the one place an operator checks whether
    # edge enforcement is alive. Observed on testserver: jabali-nginx pulling
    # every 60s while jabali-nginx-bouncer showed a three-week-old pull, which
    # reads exactly like a wedged bouncer (JAB-217).
    local fb_key nb_key
    fb_key="$(yq -r '.api_key // ""' /etc/crowdsec/bouncers/crowdsec-firewall-bouncer.yaml 2>/dev/null)"
    if [[ -n "$fb_key" && "$fb_key" != "null" ]]; then
      cscli bouncers add jabali-firewall -k "$fb_key" >/dev/null 2>&1 || true
    fi
    nb_key="$(grep -oP '^API_KEY=\K.*' /etc/crowdsec/bouncers/crowdsec-nginx-bouncer.conf 2>/dev/null || true)"
    if [[ -n "$nb_key" ]]; then
      cscli bouncers add jabali-nginx -k "$nb_key" >/dev/null 2>&1 || true
    fi
    if systemctl restart crowdsec; then
      _ok "CrowdSec LAPI DB → MariaDB (schema migrated; agent+bouncers re-registered)"
    else
      _err "CrowdSec failed to restart on MariaDB — last 30 journal lines:"
      journalctl -u crowdsec -n 30 --no-pager >&2 || true
      return 1
    fi
  else
    _log "crowdsec already on MariaDB db_config — no change"
  fi
}

install_crowdsec_profiles() {
  # M27 — defensive stub. The crowdsec Debian package ships
  # /etc/crowdsec/profiles.yaml with five upstream default profiles
  # (default_ip_remediation, default_range_remediation, etc.). If
  # it's missing we seed a minimal ban-all profile so Step 6's
  # marker-bounded rewrite always has a base file to slot into.
  # Idempotent — no-op when file exists.
  local profiles=/etc/crowdsec/profiles.yaml
  if [[ -f "$profiles" ]]; then return 0; fi
  _warn "$profiles missing after crowdsec package install — seeding minimal fallback"
  local tmp
  tmp="$(mktemp --tmpdir jabali-profiles.XXXXXX)"
  cat >"$tmp" <<'EOF'
name: default_ip_remediation
filters:
 - Alert.Remediation == true && Alert.GetScope() == "Ip"
decisions:
 - type: ban
   duration: 4h
on_success: break
EOF
  install -m 0644 -o root -g root "$tmp" "$profiles"
  rm -f "$tmp"
  systemctl reload crowdsec 2>/dev/null || true
}

# install_crowdsec_blocklists — REMOVED 2026-05-14, scope re-narrowed
# 2026-06-03 after the console drop.
#
# Originally this function fetched ~6 Firehol blocklists into local
# files for nft import. It was removed when CrowdSec Console started
# offering a managed blocklist catalog. With the console itself now
# dropped (alert quota was unusable), jabali relies on a single
# CAPI-served list:
#
#   crowdsecurity/community-blocklist — ~21k IPs/day, free, no
#   enrollment, pulled via the same CAPI cred that authenticates
#   blocklist downloads.
#
# Operators who want the richer console-managed catalog can re-enroll
# manually (`cscli console enroll <key>`), but that path is no longer
# default and the UI no longer surfaces it.
#

install_login_allowlist_default_conf() {
  # GH #598 — seed the default login-allowlist policy so the agent's SSH
  # watcher works on a fresh host before the admin ever opens the Security
  # settings (which would PATCH-push a new value). Enabled + 7d TTL matches the
  # server_settings column defaults. Idempotent: only write if absent, so we
  # never clobber an admin's pushed override on re-install/update.
  local conf=/etc/jabali-panel/login-allowlist.conf
  install -d -m 0755 -o root -g root /etc/jabali-panel
  if [[ -f "$conf" ]]; then
    _ok "login-allowlist policy already present ($conf)"
    return 0
  fi
  printf '{"enabled":true,"ttl":"168h"}\n' >"$conf"
  chmod 0644 "$conf"
  _ok "seeded default login-allowlist policy ($conf)"
}

install_crowdsec_jabali_stalwart_scenarios() {
  # Drop the vendored bu5hm4nn parser + 5 scenarios + acquis into
  # the CrowdSec config tree so mail-bf / scan / rate-limit /
  # user-enum / http-scan events fire IP bans via the firewall
  # bouncer. See install/crowdsec/stalwart/UPSTREAM.md for the
  # provenance and the changes vs. upstream.
  local src="${REPO_DIR}/install/crowdsec/stalwart"
  if [[ ! -d "$src" ]]; then
    _warn "install/crowdsec/stalwart not present in repo — skipping Stalwart CrowdSec wiring"
    return 0
  fi

  install -d -m 0755 /etc/crowdsec/parsers/s01-parse
  install -d -m 0755 /etc/crowdsec/scenarios
  install -d -m 0755 /etc/crowdsec/acquis.d

  local changed=0 f base dst
  # parser + acquis: write-on-diff (jabali-owned, never touched at runtime).
  for f in "$src"/parsers/s01-parse/jabali-stalwart-*.yaml \
           "$src"/acquis.d/jabali-stalwart*.yaml; do
    [[ -e "$f" ]] || continue
    base="$(basename "$f")"
    case "$f" in
      */parsers/*) dst="/etc/crowdsec/parsers/s01-parse/$base" ;;
      */acquis.d/*) dst="/etc/crowdsec/acquis.d/$base" ;;
    esac
    if [[ ! -f "$dst" ]] || ! cmp -s "$f" "$dst"; then
      install -m 0644 -o root -g root "$f" "$dst"
      changed=1
      _log "wrote $dst"
    fi
  done
  # scenarios: SEED-ONLY. The panel-agent's
  # security.crowdsec.sensitivity.apply verb rewrites these at runtime
  # to apply relaxed/strict thresholds; write-on-diff here would clobber
  # the operator's preset on every `jabali update`. Create only when
  # missing.
  for f in "$src"/scenarios/jabali-stalwart-*.yaml; do
    [[ -e "$f" ]] || continue
    base="$(basename "$f")"
    dst="/etc/crowdsec/scenarios/$base"
    if [[ ! -f "$dst" ]]; then
      install -m 0644 -o root -g root "$f" "$dst"
      changed=1
      _log "wrote $dst"
    fi
  done

  if (( changed )); then
    systemctl reload crowdsec 2>/dev/null || systemctl restart crowdsec 2>/dev/null || true
  fi
}

install_crowdsec_jabali_kratos_scenarios() {
  # JAB-106: surface the panel's Kratos-flow rate-limit marker
  # (marker=kratos_flow_rate_limited, from JAB-4 / PR #139) in the
  # Security -> CrowdSec dashboard. Drops the panel acquis + parser +
  # leaky scenario into the CrowdSec config tree, with the same
  # write-on-diff (parser/acquis) + seed-only (scenario) discipline as
  # install_crowdsec_jabali_stalwart_scenarios.
  local src="${REPO_DIR}/install/crowdsec/panel"
  if [[ ! -d "$src" ]]; then
    _warn "install/crowdsec/panel not present in repo — skipping panel/Kratos CrowdSec wiring"
    return 0
  fi

  install -d -m 0755 /etc/crowdsec/parsers/s01-parse
  install -d -m 0755 /etc/crowdsec/scenarios
  install -d -m 0755 /etc/crowdsec/acquis.d

  local changed=0 f base dst
  # parser + acquis: write-on-diff (jabali-owned, never touched at runtime).
  for f in "$src"/parsers/s01-parse/jabali-panel-*.yaml \
           "$src"/acquis.d/jabali-panel*.yaml; do
    [[ -e "$f" ]] || continue
    base="$(basename "$f")"
    case "$f" in
      */parsers/*) dst="/etc/crowdsec/parsers/s01-parse/$base" ;;
      */acquis.d/*) dst="/etc/crowdsec/acquis.d/$base" ;;
    esac
    if [[ ! -f "$dst" ]] || ! cmp -s "$f" "$dst"; then
      install -m 0644 -o root -g root "$f" "$dst"
      changed=1
      _log "wrote $dst"
    fi
  done
  # scenarios: SEED-ONLY. The panel-agent's security.crowdsec.sensitivity.apply
  # verb rewrites these at runtime to apply relaxed/strict thresholds; write-on-
  # diff here would clobber the operator's preset on every `jabali update`.
  for f in "$src"/scenarios/jabali-kratos-*.yaml; do
    [[ -e "$f" ]] || continue
    base="$(basename "$f")"
    dst="/etc/crowdsec/scenarios/$base"
    if [[ ! -f "$dst" ]]; then
      install -m 0644 -o root -g root "$f" "$dst"
      changed=1
      _log "wrote $dst"
    fi
  done

  if (( changed )); then
    systemctl reload crowdsec 2>/dev/null || systemctl restart crowdsec 2>/dev/null || true
  fi
}

install_crowdsec_jabali_scenarios() {
  # Seed jabali-owned CrowdSec scenarios that target the panel itself.
  # Three rules:
  #
  #   jabali-panel-login-bf.yaml      Kratos /self-service/login POST 4xx
  #   jabali-panel-recovery-bf.yaml   Kratos /self-service/recovery POST 4xx
  #   jabali-panel-whoami-probe.yaml  /sessions/whoami unauth bursts
  #
  # Thresholds match the 'balanced' sensitivity preset (5/60s login,
  # 3/300s recovery, 20/60s whoami). The agent's
  # security.crowdsec.sensitivity.apply verb overwrites these files
  # with relaxed/strict tunings when the operator picks a different
  # preset in the UI. We seed at balanced here so a fresh host has
  # protection from minute zero even if the admin never visits the
  # CrowdSec settings tab.
  #
  # Why not install via cscli hub: these scenarios are jabali-specific
  # (Kratos URI paths, panel session contract) and the upstream
  # CrowdSec hub has no equivalent. Drop-in scenarios under
  # /etc/crowdsec/scenarios/ are picked up by crowdsec's startup scan
  # exactly like hub scenarios.

  local dir=/etc/crowdsec/scenarios
  install -d -m 0755 "$dir"

  local changed=0

  _write_scenario() {
    # Seed-only: the panel-agent's security.crowdsec.sensitivity.apply
    # verb is the runtime owner of these files (rewrites them with
    # per-preset capacities). install.sh must not clobber an operator-
    # tuned preset on every `jabali update`, so this helper only
    # writes the file when it doesn't already exist.
    local path="$1" body="$2"
    if [[ ! -f "$path" ]]; then
      local tmp
      tmp="$(mktemp --tmpdir jabali-cs-scenario.XXXXXX)"
      printf '%s' "$body" >"$tmp"
      install -m 0644 -o root -g root "$tmp" "$path"
      rm -f "$tmp"
      changed=1
      _log "wrote $path"
    fi
  }

  _write_scenario "$dir/jabali-panel-login-bf.yaml" "$(cat <<'EOF'
# Managed by jabali install.sh — Security → CrowdSec → Sensitivity tunes
# capacity/blackhole via security.crowdsec.sensitivity.apply.
type: leaky
name: jabali/panel-login-bf
description: "Brute-force on the jabali panel login (Kratos /self-service/login)"
filter: |
  evt.Meta.log_type == 'http_access-log' &&
  evt.Meta.http_path contains '/self-service/login' &&
  evt.Meta.http_verb == 'POST' &&
  evt.Meta.http_status in ['400','401','403','422']
distinct: evt.Meta.source_ip
leakspeed: 60s
capacity: 5
groupby: evt.Meta.source_ip
blackhole: 4h
labels:
  service: jabali-panel
  type: bruteforce
  remediation: true
EOF
)"

  _write_scenario "$dir/jabali-panel-recovery-bf.yaml" "$(cat <<'EOF'
# Managed by jabali install.sh.
type: leaky
name: jabali/panel-recovery-bf
description: "Brute-force on the jabali panel recovery flow (Kratos /self-service/recovery)"
filter: |
  evt.Meta.log_type == 'http_access-log' &&
  evt.Meta.http_path contains '/self-service/recovery' &&
  evt.Meta.http_verb == 'POST' &&
  evt.Meta.http_status in ['400','401','403','422']
distinct: evt.Meta.source_ip
leakspeed: 300s
capacity: 3
groupby: evt.Meta.source_ip
blackhole: 4h
labels:
  service: jabali-panel
  type: bruteforce
  remediation: true
EOF
)"

  _write_scenario "$dir/jabali-panel-whoami-probe.yaml" "$(cat <<'EOF'
# Managed by jabali install.sh.
type: leaky
name: jabali/panel-whoami-probe
description: "Unauth whoami burst (session probing on Kratos /sessions/whoami)"
filter: |
  evt.Meta.log_type == 'http_access-log' &&
  evt.Meta.http_path contains '/sessions/whoami' &&
  evt.Meta.http_status == '401'
distinct: evt.Meta.source_ip
leakspeed: 60s
capacity: 20
groupby: evt.Meta.source_ip
blackhole: 4h
labels:
  service: jabali-panel
  type: probing
  remediation: true
EOF
)"

  _write_scenario "$dir/jabali-webmail-bf.yaml" "$(cat <<'EOF'
# Managed by jabali install.sh.
type: leaky
name: jabali/webmail-bf
description: "Brute-force on the jabali webmail login (Bulwark POST /webmail/auth)"
filter: |
  evt.Meta.log_type == 'http_access-log' &&
  evt.Meta.http_path startsWith '/webmail/auth' &&
  evt.Meta.http_verb == 'POST' &&
  evt.Meta.http_status startsWith '4'
distinct: evt.Meta.source_ip
leakspeed: 60s
capacity: 5
groupby: evt.Meta.source_ip
blackhole: 4h
labels:
  service: jabali-webmail
  type: bruteforce
  remediation: true
EOF
)"

  _write_scenario "$dir/jabali-api-token-bf.yaml" "$(cat <<'EOF'
# Managed by jabali install.sh.
type: leaky
name: jabali/api-token-bf
description: "Burst of unauthorized API hits (panel-api /api/v1/* 401)"
filter: |
  evt.Meta.log_type == 'http_access-log' &&
  evt.Meta.http_path startsWith '/api/v1/' &&
  evt.Meta.http_status == '401'
distinct: evt.Meta.source_ip
leakspeed: 60s
capacity: 50
groupby: evt.Meta.source_ip
blackhole: 4h
labels:
  service: jabali-panel
  type: bruteforce
  remediation: true
EOF
)"

  unset -f _write_scenario

  if (( changed )); then
    systemctl reload crowdsec 2>/dev/null || systemctl restart crowdsec 2>/dev/null || true
  fi
}

# This function ONLY tears down the legacy
# jabali-firehol-blocklists.{timer,service} on hosts that ran an
# earlier install.sh.
install_crowdsec_blocklists() {
  local svc=/etc/systemd/system/jabali-firehol-blocklists.service
  local tmr=/etc/systemd/system/jabali-firehol-blocklists.timer
  local script=/usr/local/bin/jabali-fetch-firehol-blocklists
  if [[ -f "$tmr" || -f "$svc" || -f "$script" ]]; then
    _log "removing legacy jabali-firehol-blocklists.{timer,service} (CrowdSec console catalog replaces it)"
    systemctl disable --now jabali-firehol-blocklists.timer 2>/dev/null || true
    systemctl stop jabali-firehol-blocklists.service 2>/dev/null || true
    rm -f "$tmr" "$svc" "$script"
    systemctl daemon-reload 2>/dev/null || true
    # Drop existing firehol decisions so they expire immediately
    # rather than lingering 26h. Best-effort; not critical.
    if command -v cscli >/dev/null 2>&1; then
      cscli decisions delete --origin "firehol" >/dev/null 2>&1 || true
    fi
    _ok "legacy firehol blocklist stack removed"
  fi
}


cleanup_modsecurity() {
  # ADR-0055 SUPERSEDED 2026-04-26 — CrowdSec AppSec covers the WAF role.
  # Active cleanup so existing hosts that installed M26 ModSecurity drop
  # the dead nginx module + CRS rules + main include on `jabali update`.
  # Idempotent: bails fast when packages already gone and no leftover files.
  local pkgs_present=0
  # Match the full ModSecurity package set, not just the nginx module:
  # libmodsecurity3*, libmodsecurity-dev, libnginx-mod-http-modsecurity,
  # modsecurity-crs. The t64 ABI rename (libmodsecurity3 → libmodsecurity3t64
  # on Debian 13 / Ubuntu 24.04) means the version-suffixed name varies —
  # match the libmodsecurity3 prefix.
  if dpkg -l 2>/dev/null | grep -qE '^ii\s+(libnginx-mod-http-modsecurity|modsecurity-crs|libmodsecurity-dev|libmodsecurity3)'; then
    pkgs_present=1
  fi
  local leftover_files=0
  if [[ -d /etc/nginx/modsec ]] \
      || [[ -e /etc/nginx/modsecurity.conf ]] \
      || [[ -e /etc/nginx/modsecurity_includes.conf ]] \
      || [[ -e /etc/nginx/sites-available/default-modsecurity.conf ]]; then
    leftover_files=1
  fi
  if [[ "$pkgs_present" == "0" && "$leftover_files" == "0" ]]; then
    return 0
  fi

  _log "removing ModSecurity (ADR-0055 superseded by CrowdSec AppSec)"
  if [[ "$pkgs_present" == "1" ]]; then
    # libmodsecurity3* is the runtime lib; the t64 variant name differs
    # per distro, so resolve the installed name dynamically and purge
    # the whole set in one apt transaction.
    local modsec_pkgs
    modsec_pkgs="$(dpkg-query -W -f='${Package}\n' 2>/dev/null \
      | grep -E '^(libnginx-mod-http-modsecurity|modsecurity-crs|libmodsecurity-dev|libmodsecurity3)' \
      | tr '\n' ' ')"
    if [[ -n "${modsec_pkgs// }" ]]; then
      DEBIAN_FRONTEND=noninteractive apt-get -y \
        -o Dpkg::Lock::Timeout=300 \
        remove --purge $modsec_pkgs >/dev/null 2>&1 || true
      apt-get -y autoremove >/dev/null 2>&1 || true
    fi
  fi

  # Sweep leftover nginx config + module symlinks. The apt purge usually
  # handles modules-enabled/*.conf already, but operators sometimes
  # symlinked manually — wipe both. The *_includes.conf and
  # sites-available/default-modsecurity.conf are M26-era jabali drops
  # that the package purge does NOT own.
  rm -f /etc/nginx/modules-enabled/*modsecurity* 2>/dev/null || true
  rm -f /etc/nginx/modsecurity.conf 2>/dev/null || true
  rm -f /etc/nginx/modsecurity_includes.conf 2>/dev/null || true
  rm -f /etc/nginx/sites-available/default-modsecurity.conf 2>/dev/null || true
  rm -f /etc/nginx/sites-enabled/default-modsecurity.conf 2>/dev/null || true
  rm -rf /etc/nginx/modsec 2>/dev/null || true
  rm -rf /etc/modsecurity 2>/dev/null || true

  if nginx -t >/dev/null 2>&1; then
    if systemctl is-active --quiet nginx; then
      systemctl reload nginx
    fi
    _ok "ModSecurity removed; nginx config still valid"
  else
    _warn "nginx -t failed after ModSecurity cleanup — first relevant error:"
    nginx -t 2>&1 | head -10 >&2
  fi
}

install_malware_stack() {
  # M33 — Linux Malware Detect + YARA-X + signature-base stack.
  # ADR-0072 (Tetragon removed 2026-04-30 by M39 — see ADR-0072
  # amendment + ADR-0085).
  #
  # ClamAV is REMOVED (2026-04-29 amendment 3): maldet 2.0's native HEX +
  # MD5 + SHA-256 scanner using sigs/{hex,md5,sha256v2}.dat from rfxn
  # replaces clamscan. Web-threat focused, no Windows/macOS coverage we
  # don't need on shared PHP hosting. Saves 1.5GB peak RAM during scans
  # and 99% CPU spikes. YARA via yr (yara-x) covers pattern matching.
  # Realtime-monitor unit (jabali-maldet-monitor.service) is OPT-IN.
  # Admin enables it via /jabali-admin/security?tab=malware Settings.
  # Reconciler (panel-api → agent) starts/stops the unit on toggle.
  #
  # Idempotent: every step is guarded so jabali update re-runs cleanly.

  _log "installing malware detection stack (LMD 2.0 native scanner + YARA-X)"

  # ClamAV is no longer installed. maldet 2.0 ships its own native HEX +
  # MD5 + SHA-256 scanner using sigs/{hex,md5,sha256v2}.dat (rfxn pack,
  # web-threat focused). YARA via yr (yara-x) covers pattern matching.
  # Together they replace clamscan entirely for shared PHP hosting:
  # 1.5GB peak RAM during scans → gone, 99% CPU spike → gone, 150MB
  # /var/lib/clamav → gone, daily freshclam bandwidth → gone.
  #
  # One-time cleanup: purge clamav from hosts that ran prior M33 builds
  # which installed it. Gated by a marker so we don't run apt purge on
  # every jabali update (slow + apt-lock contention with other ops).
  if [[ ! -f /etc/jabali/.clamav-purged-v2 ]]; then
    if dpkg -l clamav 2>/dev/null | grep -q '^ii'; then
      _log "purging clamav (M33 amendment: maldet 2.0 native scanner replaces it)"
      systemctl stop clamav-daemon.service clamav-freshclam.service >/dev/null 2>&1 || true
      systemctl disable clamav-daemon.service clamav-freshclam.service >/dev/null 2>&1 || true
      systemctl unmask clamav-daemon.service clamav-freshclam.service >/dev/null 2>&1 || true
      DEBIAN_FRONTEND=noninteractive apt-get -y -qq purge \
        'clamav*' >/dev/null 2>&1 || \
        _warn "apt purge clamav failed — manual cleanup may be needed"
      rm -rf /var/lib/clamav 2>/dev/null || true
      systemctl stop jabali-freshclam.timer jabali-freshclam.service >/dev/null 2>&1 || true
      systemctl disable jabali-freshclam.timer >/dev/null 2>&1 || true
      rm -f /etc/systemd/system/jabali-freshclam.service \
            /etc/systemd/system/jabali-freshclam.timer 2>/dev/null || true
      systemctl daemon-reload >/dev/null 2>&1 || true
    fi
    install -d -m 0755 /etc/jabali
    touch /etc/jabali/.clamav-purged-v2
    _ok "clamav purged + jabali-freshclam units removed"
  fi
  # Drop the legacy purge marker (older M33 amendment) — keeping it would
  # block this branch from running on hosts that previously purged.
  rm -f /etc/jabali/clamav-purged 2>/dev/null || true

  # Linux Malware Detect (LMD / maldet) — install from upstream GitHub
  # tarball. Pinned to v2.0.1-rc4 (Apr 20 2026 prerelease) which adds:
  #   - native YARA scanner (scan_yara=1, no clamscan dependency for YARA)
  #   - prefers `yr` (YARA-X) binary, falls back to libyara4
  #   - drop-in custom YARA at sigs/custom.yara.d/*.yar
  #   - post_scan_hook system (replaces our 5s sessionwatcher polling)
  #   - 43x faster native scan (Aho-Corasick parallel workers)
  # Pin bumps require a PR review of both LMD_VERSION and LMD_SHA256.
  local LMD_VERSION="2.0.1"
  local LMD_SHA256="f60171c6b095aaacd0a509199d417abf5bf768ec941b94e000e3b12439565192"
  local lmd_marker="/usr/local/maldetect/.jabali-installed-${LMD_VERSION}"

  if [[ -f "$lmd_marker" ]] && command -v maldet >/dev/null 2>&1; then
    _log "maldet ${LMD_VERSION} already installed (marker present)"
  else
    local tmp_lmd
    tmp_lmd=$(mktemp -d -t lmd-XXXXXX)
    if (
      cd "$tmp_lmd" && \
      curl -fsSL "https://github.com/rfxn/linux-malware-detect/archive/refs/tags/v${LMD_VERSION}.tar.gz" -o lmd.tar.gz && \
      echo "${LMD_SHA256}  lmd.tar.gz" | sha256sum -c - >/dev/null && \
      tar -xzf lmd.tar.gz && \
      cd "linux-malware-detect-${LMD_VERSION}" && \
      bash ./install.sh >/tmp/lmd-install.log 2>&1
    ); then
      mkdir -p /usr/local/maldetect
      # Drop the old 1.6.x marker so re-runs of jabali update on hosts
      # that ran prior amendments don't short-circuit the upgrade.
      rm -f /usr/local/maldetect/.jabali-installed-1.6.6 \
            /usr/local/maldetect/.jabali-installed-1.6.6.1 2>/dev/null || true
      touch "$lmd_marker"
      _ok "maldet ${LMD_VERSION} installed (log: /tmp/lmd-install.log)"
    else
      _warn "maldet install failed (download/sha/install) — see /tmp/lmd-install.log; continuing with stack provisioning"
    fi
    rm -rf "$tmp_lmd"
  fi

  # YARA-X (the `yr` binary) — Rust rewrite of YARA, full module support
  # including the `hash` module that libclamav YARA can't load. maldet
  # 2.0.1+ prefers `yr` over libyara when both are present.
  local YARAX_VERSION="1.19.0"
  local YARAX_SHA256="a97d78189e3548797ac45b7b4a5fd8975783861875c594f772ec9b8bb5fa4d72"
  if ! command -v yr >/dev/null 2>&1 || \
     [[ "$(yr --version 2>/dev/null | awk '{print $2}')" != "$YARAX_VERSION" ]]; then
    local tmp_yrx
    tmp_yrx=$(mktemp -d -t yarax-XXXXXX)
    if (
      cd "$tmp_yrx" && \
      curl -fsSL "https://github.com/VirusTotal/yara-x/releases/download/v${YARAX_VERSION}/yara-x-v${YARAX_VERSION}-x86_64-unknown-linux-gnu.tar.gz" -o yrx.tar.gz && \
      echo "${YARAX_SHA256}  yrx.tar.gz" | sha256sum -c - >/dev/null && \
      tar -xzf yrx.tar.gz && \
      install -m 0755 -o root -g root yr /usr/local/bin/yr
    ); then
      _ok "yara-x ${YARAX_VERSION} installed at /usr/local/bin/yr"
    else
      _warn "yara-x install failed — maldet will fall back to libyara4 if present"
    fi
    rm -rf "$tmp_yrx"
  else
    _log "yara-x ${YARAX_VERSION} already installed"
  fi
  # libyara4 fallback for hosts that didn't get yara-x (apt). Cheap.
  if ! command -v yara >/dev/null 2>&1; then
    DEBIAN_FRONTEND=noninteractive apt-get -y -qq install --no-install-recommends \
      yara >/dev/null 2>&1 || true
  fi

  # Jabali drop-in config — overrides upstream conf.maldet defaults.
  # Loaded by merge_maldet_config which appends our values to the
  # upstream-shipped file. Idempotent: pure rewrite of drop-in dir.
  install -d -m 0755 /etc/jabali/maldet/conf.maldet.d
  cat >/etc/jabali/maldet/conf.maldet.d/00-jabali.conf <<'MALDET_DROPIN'
# Jabali maldet drop-in — managed by install.sh; do not edit by hand.
# Rerun jabali update to regenerate.
quarantine_hits="1"
quarantine_clean="0"
email_alert="0"
# Native YARA scanner (maldet 2.0.1+). Prefers `yr` (YARA-X) when present,
# falls back to libyara4. Loads sigs from:
#   - sigs/rfxn.yara (upstream YARA-Forge pack, refreshed by `maldet -u`)
#   - sigs/custom.yara (single file)
#   - sigs/custom.yara.d/*.yar (drop-in dir — Jabali symlinks signature-base
#     webshells/crime + admin uploads here)
# scope=all means native YARA handles everything (no clamscan dependency).
scan_yara="1"
scan_yara_scope="all"
# clamscan is not installed on this stack — maldet 2.0 native HEX/MD5/SHA256
# scanner (sigs/{hex,md5,sha256v2}.dat from rfxn) replaces it. Web-threat
# focused signatures, no Windows/macOS coverage we don't need on shared
# PHP hosting. Saves 1.5GB peak RAM during scans and ~99% CPU spike.
scan_clamscan="0"
# SHA-256 hashing with hardware acceleration (auto-detected).
scan_hashtype="auto"
scan_user_access="1"
scan_user_access_minuid="1000"
# Jabali user docroot layout is /home/<user>/domains/<domain>/public_html.
# LMD --monitor USERS joins inotify_docroot onto /home/<user>/<docroot>
# with no wildcard expansion, so we point at `domains` and rely on
# inotifywait recursion to pick up every vhost public_html underneath.
inotify_docroot="domains"
inotify_minuid="1000"
scan_max_filesize="2048k"

# Post-scan hook — replaces the 5s sessionwatcher polling. Hook receives
# JSON on stdin (post_scan_hook_format=json) describing the scan result;
# it parses LMD_SESSION_FILE (TSV: sig\tfilepath\tquarpath) and POSTs to
# panel-api over the local UDS. async so a panel-api outage doesn't block
# scan completion. min_hits=0 means we get scan_completed events for every
# scan (not just hits).
post_scan_hook="/etc/jabali/maldet/post-scan-hook.sh"
post_scan_hook_format="json"
post_scan_hook_exec="async"
post_scan_hook_timeout="30"
post_scan_hook_on="all"
post_scan_hook_min_hits="0"

# digest_escalate_hits=1 makes monitor mode fire the post-scan hook on
# EVERY cycle that finds ≥1 hit, instead of waiting for the 24h digest
# timer. Without this, real-time monitor catches a webshell upload in
# inotify, quarantines it, and then sits silent for up to 24h before
# notifying. Smoke caught this on first VPS install: EICAR planted at
# 05:06, quarantined at 05:06:23, but no malware_events row + no M14
# notification because the digest interval hadn't elapsed.
digest_escalate_hits="1"
# digest_interval is the "all-clear heartbeat" frequency; keep it long
# (default 24h) so we don't spam admins with hourly empty-digest events.
# Real-time alerts go through the escalation path above.

# Remote YARA + hash imports — `maldet -u` fetches these daily. We also
# maintain signature-base via git-clone timer (more rules + more frequent
# refresh than what fits in a single sig_import_yara_url).
# (Empty by default — operators can override via /etc/jabali/maldet/conf.maldet.d/99-local.conf.)
MALDET_DROPIN

  if [[ -f /usr/local/maldetect/conf.maldet ]]; then
    # Strip prior Jabali block (markers) and append fresh from drop-in.
    local conf="/usr/local/maldetect/conf.maldet"
    if grep -q '# JABALI-DROPIN-BEGIN' "$conf"; then
      sed -i '/# JABALI-DROPIN-BEGIN/,/# JABALI-DROPIN-END/d' "$conf"
    fi
    {
      echo ""
      echo "# JABALI-DROPIN-BEGIN — managed by install.sh"
      # Lexical order, ALL drop-ins: conf.maldet is sourced shell where the
      # last assignment wins, so 99-jabali-breaker.conf (the JAB-248
      # quarantine circuit breaker, agent-written) keeps overriding
      # 00-jabali.conf across every update re-merge.
      for _dropin in /etc/jabali/maldet/conf.maldet.d/*.conf; do
        [[ -f "$_dropin" ]] && cat "$_dropin"
      done
      echo "# JABALI-DROPIN-END"
    } >> "$conf"
    _ok "maldet conf merged with Jabali drop-in"
  fi

  # Admin-editable YARA rule drop-in — populated via /admin/security/malware/yara.
  install -d -m 0755 -o root -g root /etc/jabali/yara
  if [[ ! -f /etc/jabali/yara/00-example.yar.disabled ]]; then
    cat >/etc/jabali/yara/00-example.yar.disabled <<'YARA_EX'
// Example YARA rule (disabled). Rename to .yar via /admin/security/malware/yara
// to enable, or upload custom rules via the same UI.
rule jabali_example {
  meta:
    description = "Trivial example — matches the literal string 'jabali-yara-example'"
    author = "jabali install.sh"
  strings:
    $a = "jabali-yara-example"
  condition:
    $a
}
YARA_EX
  fi

  # signature-base (Florian Roth / Neo23x0) — ~600 actively-maintained
  # YARA rules covering webshells (PHP/ASP/JSP), crimeware, exploit kits,
  # APT samples.
  #
  # JAB-248: PINNED to a reviewed commit, never a moving branch. These
  # rules feed a scanner that AUTO-QUARANTINES across every tenant home
  # (quarantine_hits=1); tracking origin/master meant an upstream
  # compromise — or one overbroad rule — reached mass-quarantine on every
  # box within a day, with no Jabali review in the loop. The daily
  # refresh timer now self-heals to this same pinned commit; new rules
  # arrive only through a reviewed pin bump here (drift is surfaced by
  # scripts/deps-check.sh in the monthly deps issue). signature-base has
  # no tagged releases, so the pin is a commit SHA. Pin bumps require a
  # PR review, same as LMD_VERSION/LMD_SHA256.
  local SIGBASE_COMMIT="e737ebd96c27a52ee99485d4d3e02e9c256d1d3a" # 2026-08-03 "fix: FPs"
  #
  # Custom YARA scanner picks up rules via the maldet 2.0.1 drop-in dir
  # at /usr/local/maldetect/sigs/custom.yara.d/. We symlink:
  #   custom.yara.d/signature-base/  → /opt/jabali/signature-base/yara/  (subset)
  #   custom.yara.d/jabali/          → /etc/jabali/yara/                  (admin uploads)
  install -d -m 0755 /opt/jabali
  # Fetch-by-SHA (GitHub serves reachable SHAs to shallow fetches) +
  # detached checkout: works for both fresh installs and existing hosts
  # that previously tracked master — one idempotent path, no branch ref.
  if [[ ! -d /opt/jabali/signature-base/.git ]]; then
    _log "cloning signature-base (Neo23x0/signature-base; pinned ${SIGBASE_COMMIT:0:12})"
    rm -rf /opt/jabali/signature-base
    git init --quiet /opt/jabali/signature-base 2>/dev/null && \
      git -C /opt/jabali/signature-base remote add origin \
        https://github.com/Neo23x0/signature-base.git 2>/dev/null || true
  fi
  if [[ -d /opt/jabali/signature-base/.git ]] && \
     [[ "$(git -C /opt/jabali/signature-base rev-parse HEAD 2>/dev/null)" != "$SIGBASE_COMMIT" ]]; then
    ( cd /opt/jabali/signature-base && \
      git fetch --depth=1 --quiet origin "$SIGBASE_COMMIT" && \
      git checkout --quiet --detach FETCH_HEAD ) || \
      _warn "signature-base pin fetch failed — will retry on next jabali-signature-base-update.timer"
  fi
  # Drop-in dir + symlinks. maldet 2.0.1 native YARA loads anything ending
  # in .yar / .yara from custom.yara.d/. Symlinks survive `maldet -u`
  # (only the rfxn.* sig files get rewritten by signature update).
  install -d -m 0755 /usr/local/maldetect/sigs/custom.yara.d
  # Grant the panel-api `jabali` user search+read on the sigs dir so the
  # M33.2 mailscan tick's JIT-spawned yr subprocess can load rfxn.yara +
  # /etc/jabali/yara/*.yar. We use POSIX ACLs (not chmod o+rx) because
  # LMD's lmd_init.sh runs `chmod 750 "$sigdir"` on EVERY maldet invocation
  # (sigup timer, scan, --version) — chmod alone gets reset on the next
  # daily timer fire and the tick silently false-cleans. Named-user ACL
  # entries survive `chmod` (only the mask gets clamped). Files inside
  # stay 0644; this only opens parent-dir traversal for one user. Safe:
  # rule sources are admin-editable via the panel UI, and the maldet
  # quarantine dir (which holds detected malware) lives at a sibling
  # path with locked-down 0700.
  if command -v setfacl >/dev/null 2>&1; then
    setfacl -m u:jabali:rx /usr/local/maldetect/sigs 2>/dev/null || true
    setfacl -m u:jabali:rx /usr/local/maldetect/sigs/custom.yara.d 2>/dev/null || true
  else
    DEBIAN_FRONTEND=noninteractive apt-get -y -qq install --no-install-recommends acl >/dev/null 2>&1 || true
    setfacl -m u:jabali:rx /usr/local/maldetect/sigs 2>/dev/null || true
    setfacl -m u:jabali:rx /usr/local/maldetect/sigs/custom.yara.d 2>/dev/null || true
  fi
  # signature-base subset: webshells/ + crime/ are the highest-relevance
  # for shared-PHP hosting. Could add more (apt/, exploit_kits/, etc.) but
  # webshells is the load-bearing one.
  if [[ -d /opt/jabali/signature-base/yara ]]; then
    rm -f /usr/local/maldetect/sigs/custom.yara.d/signature-base 2>/dev/null
    ln -sf /opt/jabali/signature-base/yara \
      /usr/local/maldetect/sigs/custom.yara.d/signature-base
  fi
  # git clone runs under install.sh umask 077 -> the tree is 0700 dirs /
  # 0600 files. The M33.2 mailscan tick spawns a `yr` subprocess as the
  # unprivileged `jabali` user against these rules; a 0700 tree makes
  # that scan silently load zero custom rules. Make the rule tree
  # world-traversable/readable (public Neo23x0 signatures, no secrets).
  # Unconditional + idempotent: the clone above is skipped when .git
  # exists, so an existing 0700 host only heals here. a+rX = dirs +rx,
  # files +r, never +x on data files.
  if [[ -d /opt/jabali/signature-base ]]; then
    chmod -R a+rX /opt/jabali/signature-base 2>/dev/null || true
  fi
  # admin-editable YARA dir — /etc/jabali/yara/ already exists from
  # earlier install (legacy path used by upload UI). Symlink so files
  # written there are scanned without an extra copy step.
  install -d -m 0755 -o root -g root /etc/jabali/yara
  rm -f /usr/local/maldetect/sigs/custom.yara.d/jabali 2>/dev/null
  ln -sf /etc/jabali/yara /usr/local/maldetect/sigs/custom.yara.d/jabali

  # Cleanup of M33 amendment artifacts that this stack replaces.
  rm -f /etc/jabali/maldet/build-rfxn-yara.sh 2>/dev/null
  rm -rf /etc/jabali/pmf 2>/dev/null
  rm -f /etc/systemd/system/jabali-pmf-update.service \
        /etc/systemd/system/jabali-pmf-update.timer 2>/dev/null
  systemctl disable --now jabali-pmf-update.timer >/dev/null 2>&1 || true
  # Strip any prior JABALI-PMF-BEGIN/END block from rfxn.yara — it was
  # only needed because libclamav YARA couldn't load PMF natively. Now
  # native YARA loads custom.yara.d/ rules directly, no inlining hack.
  if [[ -f /usr/local/maldetect/sigs/rfxn.yara ]] && \
     grep -q '^// JABALI-PMF-BEGIN' /usr/local/maldetect/sigs/rfxn.yara; then
    sed -i '/^\/\/ JABALI-PMF-BEGIN/,/^\/\/ JABALI-PMF-END/d' \
      /usr/local/maldetect/sigs/rfxn.yara
  fi

  # Lift inotify watch ceiling — LMD's per-user docroot watches add up
  # fast on a busy shared host (10k domains × a few hundred files each).
  cat >/etc/sysctl.d/60-jabali-malware.conf <<'SYSCTL_MALWARE'
# M33 inotify ceiling — maldet --monitor USERS adds many watches per
# tenant docroot. Default 8192 is too low for shared hosting.
fs.inotify.max_user_watches = 524288
SYSCTL_MALWARE
  sysctl --system >/dev/null 2>&1 || true

  # M33 inotify monitor unit — runs `maldet --monitor USERS` to watch
  # tenant docroots in real time. maldet --monitor forks an inotifywait
  # child then exits 0; Type=oneshot+RemainAfterExit lets systemd accept
  # that lifecycle. ExecStop calls --kill-monitor for clean teardown.
  #
  # Pre-create /var/log/maldet — the dir doesn't exist on a fresh LMD
  # install and the unit's pre-M33 hardening (ProtectSystem=strict +
  # ReadWritePaths) requires every entry to exist or namespacing fails
  # with status 226/NAMESPACE. Smoke caught this on first VPS install.
  install -d -m 0755 -o root -g root /var/log/maldet
  cat >/etc/systemd/system/jabali-maldet-monitor.service <<'MONITOR_UNIT'
[Unit]
Description=Jabali maldet inotify monitor (M33)
Documentation=file:///etc/jabali/maldet/conf.maldet.d/00-jabali.conf
After=jabali-agent.service

[Service]
# `maldet --monitor USERS` is a long-running foreground event loop —
# it forks inotifywait then sits in a wait-for-events cycle scanning
# changed files. Type=oneshot waits for ExecStart to RETURN before
# marking the unit Active, so the unit hung in `activating (start)`
# forever even though inotify watches were live and scans were firing.
# Type=simple matches the actual lifecycle: process running == unit
# active. ExecStop still drives `--kill-monitor` to tear down the
# child inotifywait + temp paths cleanly.
Type=simple
ExecStart=/usr/local/maldetect/maldet --monitor USERS
ExecReload=/usr/local/maldetect/maldet --monitor RELOAD
ExecStop=/usr/local/maldetect/maldet --kill-monitor
Restart=on-failure
RestartSec=10s
User=root
Group=root

[Install]
WantedBy=multi-user.target
MONITOR_UNIT

  systemctl daemon-reload >/dev/null 2>&1 || true
  # jabali-maldet-monitor.service is OPT-IN (per ADR-0072 amendment 2).
  # Admin enables it via /jabali-admin/security?tab=malware Settings →
  # "Real-time scanning"; panel-api → agent reconciles the unit on toggle.
  #
  # ONE-TIME force-stop+disable: needed once per host to migrate older
  # M33 builds that shipped the monitor as enabled-by-default. Marker
  # below records that we did the migration. Subsequent jabali updates
  # SKIP the disable so an admin who deliberately enabled the monitor
  # via the UI doesn't see it silently turned off on every update.
  # Without this guard, every `jabali update` would clobber the admin's
  # opt-in and the UI's Realtime monitor tile would flip to "stopped"
  # (DB says enabled, host says dead).
  local mon_default_marker="/etc/jabali/maldet/.monitor-default-applied"
  if [[ ! -f "$mon_default_marker" ]]; then
    systemctl disable --now jabali-maldet-monitor.service >/dev/null 2>&1 || true
    install -d -m 0750 /etc/jabali/maldet
    touch "$mon_default_marker"
  fi

  # post-scan-hook — invoked by maldet 2.0.1 after every scan completion
  # (cli, monitor digest, or manual). Replaces the panel-agent
  # sessionwatcher 5s-poll loop that we used in M33 amendments. Hook
  # contract (verified from rc4 source files/internals/lmd_hook.sh):
  #   - argv: $1=SCANID $2=HITS $3=FILES $4=EXIT_CODE $5=SCAN_TYPE $6=PATH
  #   - env:  LMD_SCANID, LMD_HITS, LMD_FILES, LMD_PATH, LMD_SESSION_FILE,
  #           LMD_SCAN_TYPE, LMD_ENGINE, LMD_VERSION, ...
  #   - stdin (when post_scan_hook_format=json): JSON envelope with
  #     {version, scan_type, scan_id, hits, files, cleaned, quarantined,
  #      scan_start, elapsed, exit_code, engine, path, session_file}
  # Session TSV columns (verified from files/internals/lmd_quarantine.sh):
  #     sig\tfilepath\tquarpath  (quarpath is "-" for non-quarantined hits)
  install -d -m 0750 /etc/jabali/maldet
  cat >/etc/jabali/maldet/post-scan-hook.sh <<'POST_SCAN_HOOK'
#!/usr/bin/env bash
# Jabali post-scan hook. Forwards maldet scan results to panel-api over
# the local UDS. Triggered by maldet 2.0.1 post_scan_hook config.
#
# Validation: maldet refuses to invoke this script unless it's owned by
# root and not world-writable. Permissions enforced at install time.
set -uo pipefail

# Don't fail the hook on a parse glitch — maldet prints a {hook} ERROR
# but a panel-api ingest miss is recoverable on the next scan. Better
# than blocking the next monitor cycle.
trap 'exit 0' ERR

PANEL_SOCK="/run/jabali-panel/api.sock"
ENDPOINT="http://localhost/api/v1/admin/security/malware/event"

# Sanity-check the panel-api UDS — on a host where panel-api is down,
# silently exit 0 instead of timing out the hook (maldet has its own
# 30s wrapper, but we'd rather not eat that on every cycle).
[[ -S "$PANEL_SOCK" ]] || exit 0

# Read the JSON envelope from stdin (when post_scan_hook_format=json).
# Buffer it before reading TSV rows so we can attach hits[] to the same
# request body.
HOOK_JSON=$(cat)
HITS_JSON="[]"

if [[ -n "${LMD_SESSION_FILE:-}" && -f "${LMD_SESSION_FILE}" ]]; then
  HITS_JSON=$(awk -F'\t' '
    BEGIN { print "["; first = 1 }
    /^[^#]/ {
      sig = $1; fp = $2; qp = $3
      gsub(/"/, "\\\"", sig); gsub(/"/, "\\\"", fp); gsub(/"/, "\\\"", qp)
      if (first) first = 0; else printf ","
      printf "{\"signature\":\"%s\",\"original_path\":\"%s\",\"quarantine_path\":\"%s\"}",
        sig, fp, (qp == "-" ? "" : qp)
    }
    END { print "]" }
  ' "${LMD_SESSION_FILE}")
fi

# Compose the panel-api ingest payload. event_type derives from hits>0;
# severity stays warn for hits, info for clean scans.
HITS_COUNT="${LMD_HITS:-0}"
EVENT_TYPE="scan_completed"
SEVERITY="info"
if [[ "${HITS_COUNT}" -gt 0 ]]; then
  EVENT_TYPE="file_hit"
  SEVERITY="warn"
fi

# Wrap into the panel-api ingest envelope. raw_json carries the hook
# JSON for forensics; hits[] is the parsed TSV.
PAYLOAD=$(printf '{"source":"maldet","event_type":"%s","severity":"%s","hits":%s,"raw_json":%s,"occurred_at":"%s"}' \
  "${EVENT_TYPE}" "${SEVERITY}" "${HITS_JSON}" "${HOOK_JSON:-{}}" \
  "$(date -u +%Y-%m-%dT%H:%M:%SZ)")

# POST via curl over UDS. Silent on success; log to journal on HTTP error
# so the operator can see it via `journalctl -u jabali-maldet-monitor`.
RESP=$(curl -sS --max-time 10 \
  --unix-socket "${PANEL_SOCK}" \
  -X POST "${ENDPOINT}" \
  -H 'Content-Type: application/json' \
  --data-raw "${PAYLOAD}" 2>&1) || {
  printf '{hook} panel-api POST failed: %s\n' "${RESP}" >&2
  exit 0
}
exit 0
POST_SCAN_HOOK
  # maldet enforces these — root-owned + not world-writable.
  chown root:root /etc/jabali/maldet/post-scan-hook.sh
  chmod 0750 /etc/jabali/maldet/post-scan-hook.sh

  # Both signature refreshes reach out over the network, and both are
  # Type=oneshot — for which systemd forbids Restart=. So a single unreachable
  # endpoint left the unit failed until the next day's timer:
  #
  #   {sigup} could not download https://cdn.rfxn.com/downloads/maldet.sigs.ver
  #   fatal: unable to access '.../signature-base.git/': Send failure: Broken pipe
  #
  # Both endpoints answered normally minutes later. That costs a day of
  # malware-signature freshness and leaves a red unit nobody is watching — a
  # blip worth retrying, not reporting. Written inline rather than shipped in
  # install/systemd/ so it carries no $REPO_DIR dependency and therefore no
  # ordering constraint against clone_or_update_repo.
  install -d -m 0755 /usr/local/libexec/jabali
  cat >/usr/local/libexec/jabali/net-retry <<'NET_RETRY'
#!/usr/bin/env bash
# jabali net-retry — run a network-dependent maintenance command, retrying
# transient failures before letting the unit go red.
#
# Managed by jabali-panel install.sh. Hand edits will be overwritten.
#
# Usage: net-retry <label> -- <command> [args...]
# Env:   JABALI_RETRY_ATTEMPTS (default 3)
#        JABALI_RETRY_BACKOFF  (default 60 seconds, doubled after each failure)
#
# Deliberately no `set -e`: the whole point is to inspect a non-zero exit and
# decide, and `set -e` plus arithmetic evaluation is a well-known way to make a
# bash script die silently mid-loop.
set -uo pipefail

label="${1:-command}"
shift || true
if [[ "${1:-}" == "--" ]]; then
  shift
fi

if [[ $# -eq 0 ]]; then
  echo "net-retry: no command given" >&2
  exit 2
fi

attempts="${JABALI_RETRY_ATTEMPTS:-3}"
backoff="${JABALI_RETRY_BACKOFF:-60}"
if ! [[ "$attempts" =~ ^[0-9]+$ ]] || (( attempts < 1 )); then
  attempts=3
fi
if ! [[ "$backoff" =~ ^[0-9]+$ ]]; then
  backoff=60
fi

rc=0
for (( i=1; i<=attempts; i++ )); do
  "$@"
  rc=$?
  if (( rc == 0 )); then
    if (( i > 1 )); then
      echo "net-retry: ${label} succeeded on attempt ${i}/${attempts}"
    fi
    exit 0
  fi
  if (( i < attempts )); then
    echo "net-retry: ${label} failed (exit ${rc}) on attempt ${i}/${attempts}; retrying in ${backoff}s" >&2
    sleep "${backoff}"
    backoff=$(( backoff * 2 ))
  fi
done

# Every attempt failed. Exit non-zero on purpose: a source that is unreachable
# for the whole retry window is a real outage, and stale malware signatures
# must not look healthy.
echo "net-retry: ${label} failed ${attempts} time(s); giving up (exit ${rc})" >&2
exit "${rc}"
NET_RETRY
  chmod 0755 /usr/local/libexec/jabali/net-retry

  # M33 systemd timers — daily signature update + daily scan + retention purge.
  # maldet -u refreshes rfxn.yara + hex/md5 sigs; custom.yara.d/ drop-ins
  # (jabali/ + signature-base/) load automatically with maldet v2.0.1+.
  cat >/etc/systemd/system/jabali-maldet-update-signatures.service <<'SIG_UNIT'
[Unit]
Description=Jabali maldet signature update (M33)
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/libexec/jabali/net-retry maldet-sigup -- /usr/local/maldetect/maldet -u
# Type=oneshot defaults to TimeoutStartSec=infinity, so a hung fetch would
# block every later run forever — the timer will not start a second instance
# while one is live. An hour is far more than 3 attempts plus 180s of backoff
# ever need, and far less than the daily period, so it can only ever fire on a
# genuine hang and never on a slow but healthy run.
TimeoutStartSec=3600
Nice=15
IOSchedulingClass=idle
SIG_UNIT

  cat >/etc/systemd/system/jabali-maldet-update-signatures.timer <<'SIG_TIMER'
[Unit]
Description=Daily Jabali maldet signature pull (M33)

[Timer]
OnCalendar=*-*-* 02:30:00
Persistent=true
RandomizedDelaySec=15m

[Install]
WantedBy=timers.target
SIG_TIMER

  # signature-base (Florian Roth / Neo23x0) — daily git pull keeps the
  # ~600 webshell/crime YARA rules in sync. The repo is symlinked into
  # /usr/local/maldetect/sigs/custom.yara.d/signature-base, so a fast-
  # forward update is picked up by the inotify watcher's next scan.
  # JAB-248: the refresh unit is now SELF-HEAL ONLY — it converges the
  # tree onto the pinned commit baked in at install/update time, never a
  # branch. A moved upstream master cannot change rules on this host; new
  # rules ship via a reviewed SIGBASE_COMMIT bump + jabali update (which
  # rewrites this unit with the new SHA). Unquoted heredoc ON PURPOSE so
  # ${SIGBASE_COMMIT} interpolates; every other $ must stay escaped.
  cat >/etc/systemd/system/jabali-signature-base-update.service <<SIGB_UNIT
[Unit]
Description=Jabali signature-base YARA rule pack refresh (M33, pinned — JAB-248)
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/libexec/jabali/net-retry signature-base -- /usr/bin/bash -c '\\
  set -e; \\
  if [[ ! -d /opt/jabali/signature-base/.git ]]; then \\
    rm -rf /opt/jabali/signature-base; \\
    git init --quiet /opt/jabali/signature-base; \\
    git -C /opt/jabali/signature-base remote add origin \\
      https://github.com/Neo23x0/signature-base.git; \\
  fi; \\
  cd /opt/jabali/signature-base; \\
  if [[ "\$\$(git rev-parse HEAD 2>/dev/null)" != "${SIGBASE_COMMIT}" ]]; then \\
    git fetch --depth=1 --quiet origin ${SIGBASE_COMMIT}; \\
    git checkout --quiet --detach FETCH_HEAD; \\
    chmod -R a+rX /opt/jabali/signature-base; \\
  fi'
# See the maldet unit above: guards against a hung git fetch wedging every
# future run, without being tight enough to kill a slow clone.
TimeoutStartSec=3600
Nice=15
IOSchedulingClass=idle
SIGB_UNIT

  cat >/etc/systemd/system/jabali-signature-base-update.timer <<'SIGB_TIMER'
[Unit]
Description=Daily Jabali signature-base YARA rule refresh (M33)

[Timer]
OnCalendar=*-*-* 02:50:00
Persistent=true
RandomizedDelaySec=15m

[Install]
WantedBy=timers.target
SIGB_TIMER

  cat >/etc/systemd/system/jabali-maldet-scan-daily.service <<'SCAN_UNIT'
[Unit]
Description=Jabali maldet daily scan of all user homes (M33)

[Service]
Type=oneshot
ExecStart=/usr/local/maldetect/maldet -b -r /home 1
Nice=19
IOSchedulingClass=idle
SCAN_UNIT

  cat >/etc/systemd/system/jabali-maldet-scan-daily.timer <<'SCAN_TIMER'
[Unit]
Description=Daily Jabali maldet scan (M33)

[Timer]
OnCalendar=*-*-* 03:00:00
Persistent=true
RandomizedDelaySec=30m

[Install]
WantedBy=timers.target
SCAN_TIMER

  cat >/etc/systemd/system/jabali-malware-quarantine-purge.service <<'PURGE_UNIT'
[Unit]
Description=Jabali malware quarantine retention purge (M33)

[Service]
Type=oneshot
ExecStart=/usr/local/bin/jabali malware-purge
Nice=15
PURGE_UNIT

  cat >/etc/systemd/system/jabali-malware-quarantine-purge.timer <<'PURGE_TIMER'
[Unit]
Description=Daily Jabali malware quarantine retention purge (M33)

[Timer]
OnCalendar=*-*-* 04:00:00
Persistent=true
RandomizedDelaySec=15m

[Install]
WantedBy=timers.target
PURGE_TIMER

  systemctl daemon-reload >/dev/null 2>&1 || true
  systemctl enable --now jabali-maldet-update-signatures.timer >/dev/null 2>&1 || true
  systemctl enable --now jabali-signature-base-update.timer >/dev/null 2>&1 || true
  systemctl enable --now jabali-maldet-scan-daily.timer >/dev/null 2>&1 || true
  systemctl enable --now jabali-malware-quarantine-purge.timer >/dev/null 2>&1 || true

  cleanup_tetragon_legacy
  install_audit_exec

  _ok "malware stack provisioned: maldet $(/usr/local/maldetect/maldet --version 2>/dev/null | head -1 || echo 'pending'), yara-x $(yr --version 2>/dev/null | head -1 || echo 'pending')"
}

# cleanup_tetragon_legacy — M39 (2026-04-30) removes Tetragon. On hosts
# that previously installed M33 Tetragon, sweep the units, binaries,
# config, and log paths. Idempotent: safe on a fresh host (every
# branch short-circuits on the absence test).
cleanup_tetragon_legacy() {
  local removed=0
  for unit in tetragon.service jabali-tetragon-relay.service; do
    if systemctl list-unit-files "$unit" 2>/dev/null | grep -q "$unit"; then
      systemctl disable --now "$unit" >/dev/null 2>&1 || true
      systemctl mask "$unit" >/dev/null 2>&1 || true
      removed=1
    fi
  done
  for path in \
    /etc/systemd/system/tetragon.service \
    /etc/systemd/system/jabali-tetragon-relay.service \
    /usr/local/bin/tetragon \
    /usr/local/bin/tetra \
    /usr/local/bin/jabali-tetragon-relay \
    /opt/tetragon \
    /etc/tetragon \
    /var/log/tetragon \
    /usr/local/lib/tetragon \
    /etc/jabali/tetragon-disabled \
    /sys/fs/bpf/tetragon
  do
    if [[ -e "$path" ]]; then
      rm -rf "$path" 2>/dev/null || true
      removed=1
    fi
  done
  if [[ $removed -eq 1 ]]; then
    systemctl daemon-reload >/dev/null 2>&1 || true
    _ok "tetragon legacy footprint removed (M39)"
  fi
}

# install_audit_exec — M39 (2026-04-30) narrow-scoped suspicious-binary
# execve audit via auditd. Replaces the L3 forensic audit promise that
# Tetragon was supposed to fill. NOT blanket "-S execve" — only 11
# suspicious binaries, per-user via auid>=1000 filter, single key.
# See ADR-0085 + plans/m39-remove-tetragon-narrow-auditd.md Step 3.
install_audit_exec() {
  if ! dpkg -s auditd >/dev/null 2>&1; then
    _spin "apt install auditd + audispd-plugins" \
      apt-get install -y -qq --no-install-recommends auditd audispd-plugins
  fi

  local rules_file=/etc/audit/rules.d/jabali-exec.rules
  local rules_tmp
  rules_tmp=$(mktemp)
  cat >"$rules_tmp" <<'AUDIT_RULES'
# Jabali — narrow-scoped suspicious-binary execve audit.
# jabali_susp_exec: real PAM-login users (auid>=1000, excludes auid sentinel).
# jabali_web_exec:  web workers (PHP-FPM, cron) that never get a login auid;
#                   match by effective uid>=1000 with no auid constraint.
# On Debian 12 /bin is a symlink to usr/bin; audit rules match the path the
# kernel sees at execve() time, which may be either prefix — list both.

# --- login-session users (auid>=1000, auid!=4294967295) ---
-a always,exit -F arch=b64 -S execve -F path=/bin/bash         -F auid>=1000 -F auid!=4294967295 -k jabali_susp_exec
-a always,exit -F arch=b64 -S execve -F path=/bin/sh           -F auid>=1000 -F auid!=4294967295 -k jabali_susp_exec
-a always,exit -F arch=b64 -S execve -F path=/bin/dash         -F auid>=1000 -F auid!=4294967295 -k jabali_susp_exec
-a always,exit -F arch=b64 -S execve -F path=/usr/bin/bash     -F auid>=1000 -F auid!=4294967295 -k jabali_susp_exec
-a always,exit -F arch=b64 -S execve -F path=/usr/bin/sh       -F auid>=1000 -F auid!=4294967295 -k jabali_susp_exec
-a always,exit -F arch=b64 -S execve -F path=/usr/bin/dash     -F auid>=1000 -F auid!=4294967295 -k jabali_susp_exec
-a always,exit -F arch=b64 -S execve -F path=/usr/bin/wget     -F auid>=1000 -F auid!=4294967295 -k jabali_susp_exec
-a always,exit -F arch=b64 -S execve -F path=/usr/bin/curl     -F auid>=1000 -F auid!=4294967295 -k jabali_susp_exec
-a always,exit -F arch=b64 -S execve -F path=/usr/bin/nc       -F auid>=1000 -F auid!=4294967295 -k jabali_susp_exec
-a always,exit -F arch=b64 -S execve -F path=/usr/bin/ncat     -F auid>=1000 -F auid!=4294967295 -k jabali_susp_exec
-a always,exit -F arch=b64 -S execve -F path=/usr/bin/socat    -F auid>=1000 -F auid!=4294967295 -k jabali_susp_exec
-a always,exit -F arch=b64 -S execve -F path=/usr/bin/python3  -F auid>=1000 -F auid!=4294967295 -k jabali_susp_exec
-a always,exit -F arch=b64 -S execve -F path=/usr/bin/perl     -F auid>=1000 -F auid!=4294967295 -k jabali_susp_exec
-a always,exit -F arch=b64 -S execve -F path=/usr/bin/php      -F auid>=1000 -F auid!=4294967295 -k jabali_susp_exec_phpcli

# --- web workers (PHP-FPM, cron): uid>=1000, no auid constraint ---
-a always,exit -F arch=b64 -S execve -F path=/bin/bash         -F uid>=1000 -F auid=4294967295 -k jabali_web_exec
-a always,exit -F arch=b64 -S execve -F path=/bin/sh           -F uid>=1000 -F auid=4294967295 -k jabali_web_exec
-a always,exit -F arch=b64 -S execve -F path=/bin/dash         -F uid>=1000 -F auid=4294967295 -k jabali_web_exec
-a always,exit -F arch=b64 -S execve -F path=/usr/bin/bash     -F uid>=1000 -F auid=4294967295 -k jabali_web_exec
-a always,exit -F arch=b64 -S execve -F path=/usr/bin/sh       -F uid>=1000 -F auid=4294967295 -k jabali_web_exec
-a always,exit -F arch=b64 -S execve -F path=/usr/bin/dash     -F uid>=1000 -F auid=4294967295 -k jabali_web_exec
-a always,exit -F arch=b64 -S execve -F path=/usr/bin/wget     -F uid>=1000 -F auid=4294967295 -k jabali_web_exec
-a always,exit -F arch=b64 -S execve -F path=/usr/bin/curl     -F uid>=1000 -F auid=4294967295 -k jabali_web_exec
-a always,exit -F arch=b64 -S execve -F path=/usr/bin/nc       -F uid>=1000 -F auid=4294967295 -k jabali_web_exec
-a always,exit -F arch=b64 -S execve -F path=/usr/bin/ncat     -F uid>=1000 -F auid=4294967295 -k jabali_web_exec
-a always,exit -F arch=b64 -S execve -F path=/usr/bin/socat    -F uid>=1000 -F auid=4294967295 -k jabali_web_exec
-a always,exit -F arch=b64 -S execve -F path=/usr/bin/python3  -F uid>=1000 -F auid=4294967295 -k jabali_web_exec
-a always,exit -F arch=b64 -S execve -F path=/usr/bin/perl     -F uid>=1000 -F auid=4294967295 -k jabali_web_exec

# --- jabali daemon binary integrity: detect tamper (write) + unexpected exec ---
-w /usr/local/bin/jabali            -p wx -k jabali_bin_tamper
-w /usr/local/bin/jabali-agent      -p wx -k jabali_bin_tamper
-w /usr/local/bin/jabali-panel-api  -p wx -k jabali_bin_tamper
-w /usr/local/bin/jabali-kratos     -p wx -k jabali_bin_tamper
AUDIT_RULES

  # Idempotent: only re-render + reload if checksum changed.
  if [[ ! -f "$rules_file" ]] || ! cmp -s "$rules_tmp" "$rules_file"; then
    install -m 0640 -o root -g root "$rules_tmp" "$rules_file"
    if command -v augenrules >/dev/null 2>&1; then
      augenrules --load >/dev/null 2>&1 || \
        _warn "augenrules --load failed — auditd may need a restart"
    fi
    _ok "auditd jabali-exec.rules installed (32 rules: susp_exec + web_exec + bin_tamper)"
  fi
  rm -f "$rules_tmp"

  # Kernel cmdline: without audit=1, a host booted while systemd (PID 1)
  # held the audit netlink registers systemd/journald as the audit
  # consumer; auditd starts + loads rules + reports `enabled 1` but the
  # kernel never routes syscall records to it -> ZERO syscall events
  # despite a correct-looking config (observed live mx.jabali-panel.local
  # 2026-05-16: 0 SYSCALL today, fresh -w watch silent, no never/exclude,
  # /proc/cmdline lacked audit=1). audit_backlog_limit raises the early-
  # boot queue so events aren't lost before auditd attaches. Mirrors the
  # AppArmor GRUB pattern; reboot-gated via a sentinel (cannot take
  # effect until the operator reboots). Idempotent.
  if [[ -f /etc/default/grub ]] && ! grep -qE 'audit=1' /etc/default/grub; then
    sed -i 's/^GRUB_CMDLINE_LINUX_DEFAULT="\([^"]*\)"/GRUB_CMDLINE_LINUX_DEFAULT="\1 audit=1 audit_backlog_limit=8192"/' /etc/default/grub
    update-grub >/dev/null 2>&1 || true
    touch /etc/jabali/.audit-grub-pending
    _warn "auditd: audit=1 added to GRUB — REBOOT required for syscall auditing (sentinel /etc/jabali/.audit-grub-pending)"
  elif grep -qE 'audit=1' /proc/cmdline 2>/dev/null; then
    rm -f /etc/jabali/.audit-grub-pending
  fi

  if is_container; then
    _log "skipping auditd enable/start (container — host kernel owns audit)"
  else
    systemctl enable --now auditd >/dev/null 2>&1 || \
      _warn "auditd enable/start failed — check 'systemctl status auditd'"
  fi
}

install_ufw() {
  _log "configuring UFW (package installed in base batch)"

  if ! command -v ufw >/dev/null 2>&1; then
    _die "ufw missing after apt install — install_base_packages did not install it"
  fi

  # Default policies. `ufw default <verb>` is idempotent — re-running a
  # second time prints "Default incoming policy changed to 'deny'" only
  # on actual change.
  ufw default deny incoming >/dev/null
  ufw default allow outgoing >/dev/null

  # GH #415: SSH port 22 is OPERATOR-DISCRETIONARY. It's allowed on the FIRST
  # install (so a fresh host isn't locked out the moment default-deny activates,
  # and tenant SFTP works out of the box), but must NEVER be re-asserted on
  # `jabali update`. An operator who deliberately closed 22 (jump host, moved
  # SSH, IP-restricted it) reported the update silently re-opening it — so on an
  # already-active firewall we leave 22 exactly as the operator left it and only
  # keep the panel/mail/DNS service ports in sync.
  local ufw_was_active=0
  if ufw status 2>/dev/null | grep -q '^Status: active'; then
    ufw_was_active=1
  fi
  # Service ports the panel itself needs (always kept open). Port 22 is added
  # only on first install.
  local svc_ports=(80 443 8443 25 465 587 993 995 4190 53)
  local manage_ports=("${svc_ports[@]}")
  if [[ "$ufw_was_active" -eq 0 ]]; then
    manage_ports=(22 "${svc_ports[@]}")
  fi

  # Clean up legacy protocol-agnostic rules (e.g. "allow 8443" which
  # opens both TCP and UDP) that may exist from pre-N/tcp install.sh
  # runs. These create duplicate entries alongside the "allow N/tcp"
  # rules added below, unnecessarily opening UDP on TCP-only ports.
  # `ufw delete allow N` exits non-zero with "Could not delete
  # non-existent rule" if absent — silence the error; idempotent.
  local port
  for port in "${manage_ports[@]}"; do
    ufw delete allow "$port" >/dev/null 2>&1 || true
  done

  # Allow-list: web (panel + nginx), mail (Stalwart), DNS (PowerDNS
  # authoritative, TCP for AXFR + large UDP responses, UDP for normal
  # queries), plus SSH on first install only. MUST be in place BEFORE
  # `ufw enable` runs in the same install — otherwise default-deny locks
  # out SSH the moment the firewall activates.
  for port in "${manage_ports[@]}"; do
    # `ufw allow N/tcp` is idempotent — second invocation prints
    # "Skipping adding existing rule" but exits 0.
    ufw allow "${port}/tcp" >/dev/null
  done
  # DNS authoritative also needs UDP/53. Recursor + systemd-resolved
  # bind loopback only, so the wildcard UFW rule won't expose them.
  ufw allow 53/udp >/dev/null

  # Idempotent enable: bare `ufw --force enable` reloads the firewall
  # mid-install which can race in-flight TCP (apt mirror reuse, the
  # Stalwart bind that happens later in this same script). Guard on
  # `ufw status` reporting the firewall as actually active — NOT on
  # `systemctl is-active ufw`, because the ufw.service unit can be
  # reported active by systemd while the firewall itself is Status:
  # inactive (the service only loads rules at boot; a fresh host where
  # ufw was never enabled has the unit "active" but no rules applied).
  # Observed on Debian 13 minimal where the rules-sync block above
  # appended to user.rules while iptables stayed empty.
  if ! ufw status 2>/dev/null | grep -q '^Status: active'; then
    _log "enabling UFW for the first time"
    ufw --force enable >/dev/null
  else
    _log "UFW already active — skipping enable (rules synced above)"
  fi

  # Verify the allow-list landed and SSH is still in it — but ONLY on the first
  # install, where we just added the 22 rule and default-deny is going live for
  # the first time. On update (GH #415) the operator owns the SSH policy: if
  # they intentionally closed 22 we must not die here, or `jabali update` would
  # fail on exactly the hosts that tightened their firewall. Grep is lenient
  # across UFW versions: "22/tcp ALLOW ...", "22 (v6) ALLOW ...", "22 ALLOW ...".
  if [[ "$ufw_was_active" -eq 0 ]]; then
    if ! ufw status verbose 2>/dev/null \
         | awk '/ALLOW/ && ($1 == "22" || $1 == "22/tcp" || $1 ~ /^22\/tcp/)' \
         | grep -q .; then
      ufw status verbose 2>&1 >&2 || true
      _die "UFW allow rule for port 22 missing after first install — refusing to leave operator at risk of SSH lockout (status dumped above)"
    fi
  fi
  if [[ "$ufw_was_active" -eq 0 ]]; then
    _ok "UFW active; default-deny incoming with allow-list (22, 80, 443, 8443, 25, 465, 587, 993, 995, 4190, 53/tcp+udp)"
  else
    _ok "UFW allow-list synced (panel/mail/DNS ports; SSH port 22 left as operator configured — GH #415)"
  fi
}

# ---------- step 8a.0.5: M34 per-user PHP-FPM egress firewall ----------
#
# Renders /etc/nftables.d/jabali-per-user-egress.nft from
# user_egress_policies every reconciler tick (panel-api side), enforces
# at the kernel packet layer via nftables `socket cgroupv2 level 3` +
# vmap dispatch keyed on the M18 slice path. Co-exists with UFW because
# UFW writes its own iptables-nft tables; our jabali_per_user table is
# separate. Reload uses `nft -f <file>` only — never `systemctl restart
# nftables` (would flush UFW + crowdsec-firewall-bouncer chains).
#
# /etc/nftables.conf is not active under UFW (verified: nftables.service
# is disabled-by-preset on Debian 13 with UFW), so we wire a oneshot
# systemd unit `jabali-per-user-egress-load.service` that re-applies
# the rendered file on boot before panel-api starts. That closes the
# 60s "no filter" window between boot and the first reconciler tick.
#
# Default mode is ENFORCED on fresh installs. Hosts upgrading from a
# build without this feature start in LEARNING for 7 days (Step 8 timer
# matures the rows). Operator pin via /etc/jabali/per-user-egress.mode
# pauses the auto-flip indefinitely.
install_per_user_egress() {
  _log "configuring per-user PHP-FPM egress firewall (M34)"

  if ! command -v nft >/dev/null 2>&1; then
    # nftables binary ships with the base UFW dependency on Debian 13.
    # If absent, the operator built a custom image; skip silently rather
    # than abort the rest of the install.
    _warn "nft binary missing — skipping per-user egress firewall (operator must install nftables manually)"
    return 0
  fi

  install -d -m 0755 /etc/nftables.d
  install -d -m 0750 /etc/jabali

  # First-run gate: choose default mode. New install (no /etc/jabali/installed
  # marker yet at this point in the script) → ENFORCED. Existing host
  # being upgraded → LEARNING for 7 days. Marker prevents subsequent
  # `jabali update` runs from flipping the mode back.
  if [[ ! -f /etc/jabali/.per-user-egress-installed ]]; then
    if [[ -f /etc/jabali/installed ]]; then
      echo "learning" > /etc/jabali/per-user-egress.mode
      _log "per-user egress: LEARNING mode for 7 days (existing host upgrade)"
    else
      echo "enforced" > /etc/jabali/per-user-egress.mode
      _log "per-user egress: ENFORCED on first install"
    fi
    touch /etc/jabali/.per-user-egress-installed
  fi

  # Empty seed files. Reconciler fills both as soon as panel-api boots.
  # Idempotent: only write when missing so a live host's rendered file
  # is preserved across `jabali update`.
  #
  # Two files, because the boot unit cannot load the live one. The live
  # ruleset dispatches on cgroupv2 paths, and nftables resolves those to
  # inodes at load time — at boot no tenant process has started, so
  # jabali.slice/jabali-user.slice does not exist and `nft -f` rejects the
  # WHOLE file (it applies as a single transaction). The boot file carries
  # the same policy dispatched by uid, so it has no cgroup paths to resolve.
  for _f in /etc/nftables.d/jabali-per-user-egress.nft \
            /etc/nftables.d/jabali-per-user-egress-boot.nft; do
    if [[ ! -f "$_f" ]]; then
      cat >"$_f" <<'NFT'
# Generated by jabali — do not edit. Reconciler will overwrite on first tick.
table inet jabali_per_user { }
NFT
    fi
  done
  unset _f
  nft -f /etc/nftables.d/jabali-per-user-egress.nft 2>/dev/null || true

  # Boot-time re-apply unit. Runs once after the network is up, before
  # panel-api starts (panel-api's reconciler will then converge state
  # from the DB on its first tick).
  #
  # Points at the -boot.nft variant deliberately — see above. Replaying the
  # cgroup ruleset here fails on any host that has ever had a tenant, which
  # left this unit guaranteeing the no-filter window it exists to close.
  cat >/etc/systemd/system/jabali-per-user-egress-load.service <<'UNIT'
[Unit]
Description=Re-apply jabali per-user egress nftables rules at boot
After=network-pre.target
Before=jabali-panel.service
ConditionPathExists=/etc/nftables.d/jabali-per-user-egress-boot.nft

[Service]
Type=oneshot
ExecStart=/usr/sbin/nft -f /etc/nftables.d/jabali-per-user-egress-boot.nft
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
UNIT
  systemctl daemon-reload
  systemctl enable --now jabali-per-user-egress-load.service >/dev/null 2>&1 || true

  # Step 8: LEARNING -> ENFORCED daily auto-flip timer.
  # `jabali per-user-egress flip-mature` lists policies in LEARNING for
  # ≥7 days and flips them to ENFORCED unless the operator pin file
  # /etc/jabali/per-user-egress.mode contains "learning". Idempotent;
  # safe to re-run on every `jabali update`.
  cat >/etc/systemd/system/jabali-per-user-egress-flip.service <<'UNIT'
[Unit]
Description=Flip mature jabali per-user egress LEARNING policies to ENFORCED
After=jabali-panel.service
Requires=jabali-panel.service

[Service]
Type=oneshot
ExecStart=/usr/local/bin/jabali per-user-egress flip-mature --soak-days 7
User=root
UNIT
  cat >/etc/systemd/system/jabali-per-user-egress-flip.timer <<'UNIT'
[Unit]
Description=Daily run of jabali-per-user-egress-flip.service

[Timer]
OnCalendar=*-*-* 03:30:00
Persistent=true
RandomizedDelaySec=15min

[Install]
WantedBy=timers.target
UNIT
  systemctl daemon-reload
  systemctl enable --now jabali-per-user-egress-flip.timer >/dev/null 2>&1 || true

  _ok "per-user egress firewall installed (mode: $(cat /etc/jabali/per-user-egress.mode 2>/dev/null || echo unknown))"
}

# ---------- step 8a.5: AppArmor jabali daemon profiles (M40) ---------------
#
# AppArmor is in Debian 13 main, default-installed on every official
# image. M40 (ADR-0086) adds path-based MAC profiles for the jabali-
# owned daemons (panel-api, panel-agent, jabali-bulwark) plus a small
# set of system services (mariadb, stalwart, redis, pdns, kratos).
#
# Default mode on FRESH installs: complain (audit-only). Operator
# flips per-profile to enforce after a 7-day soak via
#   jabali apparmor flip-mature [--profile name]
# On UPGRADE (existing host), the function preserves the operator's
# current mode — re-applying the canonical profile content but NOT
# changing complain/enforce state.
#
# Profile sources live under install/apparmor/usr.local.bin.jabali-*
# (Debian filename convention: dots replace slashes). install.sh
# copies them to /etc/apparmor.d/ and reloads via apparmor_parser -r.

install_apparmor() {
  if ! dpkg -s apparmor >/dev/null 2>&1; then
    _spin "apt install apparmor + apparmor-utils" \
      apt-get install -y -qq --no-install-recommends apparmor apparmor-utils
  fi
  # apparmor-profiles-extra ships distro-curated profiles for mariadb,
  # postfix, etc. Best-effort install — Debian 13 includes it; if a
  # cloud minimal image lacks the package we just skip system-daemon
  # profile activation in apply_apparmor_system_profiles().
  if ! dpkg -s apparmor-profiles-extra >/dev/null 2>&1; then
    apt-get install -y -qq --no-install-recommends apparmor-profiles-extra >/dev/null 2>&1 || \
      _warn "apparmor-profiles-extra not installable — system-daemon profiles skipped"
  fi

  if [[ ! -d /sys/kernel/security/apparmor ]]; then
    _warn "AppArmor LSM not active in kernel — skipping profile install"
    touch /etc/jabali/.apparmor-disabled
    return 0
  fi

  if ! grep -q apparmor /sys/kernel/security/lsm 2>/dev/null; then
    _warn "AppArmor not in kernel LSM list — adding apparmor=1 security=apparmor to GRUB"
    if [[ -f /etc/default/grub ]] && ! grep -q "apparmor=1" /etc/default/grub; then
      sed -i 's/^GRUB_CMDLINE_LINUX_DEFAULT="\([^"]*\)"/GRUB_CMDLINE_LINUX_DEFAULT="\1 apparmor=1 security=apparmor"/' /etc/default/grub
      update-grub >/dev/null 2>&1 || true
      touch /etc/jabali/.apparmor-grub-pending
      _warn "AppArmor: reboot required to activate (sentinel /etc/jabali/.apparmor-grub-pending)"
    fi
    return 0
  fi

  rm -f /etc/jabali/.apparmor-disabled /etc/jabali/.apparmor-grub-pending

  # Detect kernels missing unix-socket peer-label mediation. On Ubuntu
  # 24.04 HWE (kernel 6.8) + AppArmor 4.0.1, the kernel ships af_unix
  # network mediation but NOT the dedicated unix/ feature directory
  # that enables proper peer-label checks. Symptom: attaching ANY
  # profile (even one with only `capability, network, file,`) blocks
  # connect() to unconfined unix-socket peers — kratos can't reach
  # /run/mysqld/mysqld.sock, panel-api can't reach Kratos admin, etc.
  # The daemon profiles are net-negative on these kernels: they break
  # the daemons without providing meaningful confinement. Skip them.
  # ADR-0086 amended 2026-05-11.
  # Single authoritative signal: /sys/kernel/security/apparmor/features/
  # unix/ absent ⇒ the kernel cannot do unix-socket peer-label
  # mediation, so attaching ANY profile silently EACCESes connect()
  # to unconfined unix peers (mysqld.sock, kratos admin, …). The
  # earlier "&& kernel < 6.10" narrowing was added to suppress what
  # looked like a false positive on Debian 13's 6.12 — but db.create
  # under the enforced jabali-agent profile on that exact kernel
  # (6.12.74+deb13, no features/unix dir) proves 6.12 is broken too.
  # The kernel-version band is a wrong proxy; the sysfs feature dir
  # is the real capability bit. Trust it alone. ADR-0086 stance:
  # daemon profiles are net-negative without working mediation —
  # skipping when the kernel lacks the feature is the safe bias.
  local apparmor_unix_bug=0
  if [[ ! -d /sys/kernel/security/apparmor/features/unix ]]; then
    # GH #705-followup: this kernel lacks working unix-socket mediation. The
    # jabali daemon profiles are now pinned to `abi <abi/3.0>` (predates unix
    # mediation), so the kernel SKIPS unix mediation for them — attaching them
    # can no longer EACCES a unix-socket connect(). Verified on the AA-4.x
    # broken-mediation kernel (6.12.x, no features/unix): panel DB access, the
    # FPM listen socket, and a confined mysql connect all work. So instead of
    # SKIPPING (the old bias, which left every daemon fully unconfined), we now
    # LOAD the profiles in complain here; the soak surfaces any real denial in
    # journalctl -k before an operator flips a profile to enforce.
    _log "AppArmor kernel lacks unix/ mediation ($(uname -r)) — profiles are abi/3.0-pinned; loading in complain (unix mediation skipped by the ABI pin, files/caps/network still confined)"
    # Undo any durable-disable a prior (skip-bias) install left behind, so the
    # profiles actually load below.
    apparmor_reenable_jabali /etc/apparmor.d
    rm -f /etc/jabali/.apparmor-unix-broken
  else
    rm -f /etc/jabali/.apparmor-unix-broken
  fi

  local first_install=0
  if [[ ! -f /etc/jabali/.apparmor-installed ]]; then
    first_install=1
  fi

  cleanup_apparmor_legacy
  if [[ $apparmor_unix_bug -eq 0 ]]; then
    apply_apparmor_profiles "$first_install"
  fi
  apply_apparmor_system_profiles "$first_install"

  if [[ $first_install -eq 1 ]]; then
    date -u +%Y-%m-%dT%H:%M:%SZ > /etc/jabali/.apparmor-installed
  fi

  if [[ $apparmor_unix_bug -eq 1 ]]; then
    _ok "AppArmor: jabali daemon profiles skipped (kernel missing unix/ mediation); $(aa-status 2>/dev/null | grep -c '^\s*/') system profiles active"
  else
    _ok "AppArmor profiles applied ($(aa-status 2>/dev/null | grep -c 'jabali-') jabali profiles loaded)"
  fi

  # JAB-349: install the daily auto-promotion timer so soak-clean complain
  # profiles flip to enforce on their own. Reached on every install AND update
  # (install_apparmor runs on both). Only meaningful when AppArmor is active —
  # the early returns above bail before here on kernels without it.
  install_apparmor_flip_timer
}

# apply_apparmor_system_profiles activates distro-supplied profiles for
# the system daemons jabali leans on (mariadb / redis / pdns). Profile
# files come from the upstream packages (apparmor-profiles-extra,
# redis-server, pdns-server, pdns-recursor) — we don't author them.
# We only flip them to complain mode on first install (so an enforce-by-
# default Debian profile doesn't bork an existing setup mid-upgrade).
# Operator promotes individual profiles to enforce via
# `jabali apparmor flip-mature --profile <name>` after the soak window.
#
# php-fpm + nginx are intentionally absent: tenant code surface is too
# dynamic for a path-based profile without a long-tail of FPs.
#
# Arg: $1 — 1 if first install (set complain), 0 to preserve existing
# mode.
# apparmor_durably_disable_jabali — make every jabali daemon profile
# (incl. stalwart-mail and any stray *.test variant) survive a system
# apparmor.service reload as DISABLED. `apparmor_parser -R` alone is
# transient: the profile file stays in /etc/apparmor.d/ so the next
# boot / `systemctl reload apparmor` / apt apparmor-trigger re-parses
# and re-loads it, silently re-EACCESing unix-socket connect() on a
# kernel that lacks features/unix (the broken-mediation gate's whole
# reason). The durable mechanism is apparmor's standard skip dir:
# a symlink /etc/apparmor.d/disable/<name> -> ../<name> makes the
# parser AND the service init skip that profile on every future parse.
#
# Arg $1: apparmor.d dir (default /etc/apparmor.d) — parameterised so
# the contract is unit-testable in a sandbox without root.
# apparmor_reenable_jabali removes the durable-disable symlinks a prior
# skip-bias install created, so the profiles load again (GH #705-followup —
# now safe via the abi/3.0 pin). Mirrors the globs in the disable fn below,
# plus the libexec fpm-exec profile.
apparmor_reenable_jabali() {
  local aad="${1:-/etc/apparmor.d}"
  rm -f "$aad"/disable/usr.local.bin.jabali-* \
        "$aad"/disable/usr.local.bin.stalwart-mail \
        "$aad"/disable/usr.local.libexec.jabali.* 2>/dev/null || true
}

apparmor_durably_disable_jabali() {
  local aad="${1:-/etc/apparmor.d}"
  local prof base
  mkdir -p "$aad/disable"
  for prof in "$aad"/usr.local.bin.jabali-* \
              "$aad"/usr.local.bin.stalwart-mail \
              "$aad"/usr.local.bin.jabali-*.test; do
    [[ -e "$prof" ]] || continue
    base="$(basename "$prof")"
    # Stray *.test variants exist only to confuse the parser — the
    # apparmor.service globs *.test too on some distros. Delete them
    # outright rather than disable-symlinking a throwaway.
    if [[ "$base" == *.test ]]; then
      rm -f "$prof"
      continue
    fi
    # Durable skip: relative symlink back to the profile file.
    ln -sf "../$base" "$aad/disable/$base"
    # Immediate in-kernel unload (transient, but the symlink above
    # keeps it off across reloads). Stubbed in the unit test.
    apparmor_parser -R "$prof" 2>/dev/null || true
  done
}

# cleanup_apparmor_legacy removes profiles that were shipped in earlier
# releases but have since been superseded.
#
# M40.1 (ADR-0086 amended): all five jabali daemon profiles were
# re-authored for AA 4.x unix-socket mediation and re-enabled; the
# old *.disabled stubs are inert (apply_apparmor_profiles skips them).
# No per-profile deletions needed here anymore — apply_apparmor_profiles
# overwrites the on-disk files and apparmor_parser -r refreshes the
# in-kernel policy.
#
# aa-remove-unknown is kept as a sweep for any prior-run stale state:
# profiles whose on-disk file was removed in an earlier jabali update
# but whose in-kernel entry was never unloaded. Idempotent.
cleanup_apparmor_legacy() {
  if is_container || [[ -f /etc/jabali/.apparmor-disabled ]]; then
    return 0
  fi
  # M40.2 — drop the kratos AppArmor profile. AA 4.x on Ubuntu 24.04
  # (kernel 6.8) has a complain-mode regression where unix-stream
  # mediation returns EACCES on connect() even though the profile is
  # complain and audit.log is empty. Reproduces 100% on a host where
  # the profile is loaded; aa-disable instantly fixes it. We've
  # decided the security upside of confining kratos (already in a
  # systemd Group=jabali-sockets + NoNewPrivileges sandbox + DSN-
  # over-unix-socket) is smaller than the operational pain of every
  # fresh Ubuntu 24.04 install crash-spinning on db ping. Revisit
  # when AA 4.x complain-mode unix mediation is fixed upstream.
  local stale_kratos=/etc/apparmor.d/usr.local.bin.jabali-kratos
  if [[ -f "$stale_kratos" ]]; then
    # aa-disable creates a symlink under /etc/apparmor.d/disable/
    # which apparmor_parser then skips. Remove the on-disk profile
    # afterwards so a future apply_apparmor_profiles pass doesn't
    # re-install it.
    aa-disable "$stale_kratos" 2>/dev/null || true
    rm -f /etc/apparmor.d/disable/usr.local.bin.jabali-kratos
    rm -f "$stale_kratos"
    # Force-unload the in-kernel profile too; aa-disable on its own
    # only blocks future loads.
    apparmor_parser -R /dev/stdin <<<'profile jabali-kratos /usr/local/bin/kratos { }' 2>/dev/null || true
  fi
  # M40.3 — drop the jabali-agent AppArmor profile, SAME AA 4.x
  # Debian-13/Ubuntu-24.04 complain-mode bug as kratos (M40.2): a
  # unix-stream connect() to MariaDB's mysqld.sock returns EACCES even
  # though the profile is loaded in complain mode WITH an explicit
  # `unix (connect,send,receive) type=stream peer=(label=unconfined)`
  # rule and mariadbd is unconfined. Verified on mx 2026-05-19:
  # `pdns.ReadEnvAndConnect()` -> "connect: permission denied" -> the
  # agent's pdns client stays nil -> every dns.zone.upsert/delete
  # returns "powerdns backend not available", so ALL DNS edits fail.
  # aa-disable + restart instantly fixed it. complain mode enforces
  # nothing (logs only), so dropping a complain profile is ZERO real
  # protection loss vs the prior state — it only removed a liability
  # that broke MariaDB connect. Agent stays hardened by its systemd
  # unit (NoNewPrivileges, ProtectSystem, socket-group gating). Revert
  # when AA 4.x complain-mode unix mediation is fixed upstream.
  local stale_agent=/etc/apparmor.d/usr.local.bin.jabali-agent
  if [[ -f "$stale_agent" ]]; then
    aa-disable "$stale_agent" 2>/dev/null || true
    rm -f /etc/apparmor.d/disable/usr.local.bin.jabali-agent
    rm -f "$stale_agent"
    apparmor_parser -R /dev/stdin <<<'profile jabali-agent /usr/local/bin/jabali-agent { }' 2>/dev/null || true
  fi
  # aa-remove-unknown sweeps every in-kernel profile whose backing file
  # no longer exists (stale from previous jabali update runs).
  if command -v aa-remove-unknown >/dev/null 2>&1; then
    aa-remove-unknown >/dev/null 2>&1 || true
  fi
}

# install_apparmor_flip_timer — JAB-349. Without this, jabali profiles load in
# complain mode and stay there forever unless an operator manually runs
# `jabali apparmor flip-mature`, so an Internet-facing panel keeps
# audit-only (non-enforcing) MAC indefinitely. This daily timer promotes
# SOAK-CLEAN complain profiles to enforce automatically.
#
# It is SAFE to auto-run: `jabali apparmor flip-mature` flips ONLY a profile
# with ZERO AppArmor DENIED events in the soak window and SKIPS any profile
# still denying (surfacing the pending denials) — so it can never enforce a
# profile that would then EACCES its daemon (the #705 crash-loop). Mirrors the
# per-user-egress flip-mature timer. Idempotent; runs on every install + update
# (install_apparmor is on both paths).
install_apparmor_flip_timer() {
  cat >/etc/systemd/system/jabali-apparmor-flip-mature.service <<'UNIT'
[Unit]
Description=Flip soak-clean jabali AppArmor profiles from complain to enforce (JAB-349)
After=jabali-agent.service jabali-panel.service
Requires=jabali-agent.service

[Service]
Type=oneshot
ExecStart=/usr/local/bin/jabali apparmor flip-mature --soak-days 7
User=root
UNIT
  cat >/etc/systemd/system/jabali-apparmor-flip-mature.timer <<'UNIT'
[Unit]
Description=Daily promotion of soak-clean jabali AppArmor profiles to enforce (JAB-349)

[Timer]
OnCalendar=*-*-* 04:10:00
Persistent=true
RandomizedDelaySec=15min

[Install]
WantedBy=timers.target
UNIT
  systemctl daemon-reload >/dev/null 2>&1 || true
  systemctl enable --now jabali-apparmor-flip-mature.timer >/dev/null 2>&1 \
    && _ok "AppArmor auto-promotion timer enabled (soak-clean profiles flip to enforce daily)" \
    || _warn "could not enable jabali-apparmor-flip-mature.timer — profiles stay in their current mode until 'jabali apparmor flip-mature' is run"
}

apply_apparmor_system_profiles() {
  local first_install=${1:-0}
  # Skip silently in containers (LXC/Docker/Podman) — the host kernel
  # owns AppArmor, apparmor_parser -r against the container's
  # securityfs returns EPERM, and the noise drowns out real failures.
  if is_container || [[ -f /etc/jabali/.apparmor-disabled ]]; then
    return 0
  fi
  local sys_profiles=(
    /etc/apparmor.d/usr.sbin.mysqld
    /etc/apparmor.d/usr.bin.redis-server
    /etc/apparmor.d/usr.sbin.pdns_server
    /etc/apparmor.d/usr.sbin.pdns_recursor
  )
  local p
  for p in "${sys_profiles[@]}"; do
    [[ -f "$p" ]] || continue
    apparmor_parser -r "$p" 2>/dev/null || {
      _warn "apparmor_parser -r failed for $(basename "$p") — skipping"
      continue
    }
    # First install: park in complain mode so a vendor profile mismatch
    # with our M25 unix-socket / M6.3 split-port setup logs but doesn't
    # break the daemon. Upgrade: leave whatever mode the operator chose.
    if [[ $first_install -eq 1 ]]; then
      aa-complain "$p" >/dev/null 2>&1 || true
    fi
  done
}

# apply_apparmor_profiles renders + reloads every jabali profile under
# install/apparmor/. On first install all profiles default to complain
# mode (operator burn-in soak). On subsequent runs the current mode of
# each profile is preserved.
#
# Arg: $1 — 1 if this is the first install (set complain on every
# profile), 0 to preserve existing mode.
apply_apparmor_profiles() {
  local first_install=${1:-0}
  local src_dir="${REPO_DIR}/install/apparmor"
  if [[ ! -d "$src_dir" ]]; then
    _warn "AppArmor profile source dir missing: $src_dir"
    return 0
  fi
  # Skip silently in containers (LXC/Docker/Podman) — the host kernel
  # owns AppArmor, apparmor_parser -r against the container's
  # securityfs returns EPERM, and the noise drowns out real failures.
  if is_container || [[ -f /etc/jabali/.apparmor-disabled ]]; then
    return 0
  fi

  local profile
  # Glob covers BOTH `usr.local.bin.jabali-*` (panel-api/agent/bulwark/
  # kratos) AND every other profile we author (stalwart-mail, future
  # additions). Earlier `jabali-*` glob silently dropped stalwart-mail.
  for profile in "$src_dir"/usr.local.bin.* "$src_dir"/usr.local.libexec.*; do
    [[ -e "$profile" ]] || continue
    # Skip *.disabled stubs — these are the old M40 profiles that lacked
    # AA 4.x unix-socket mediation rules. M40.1 re-authored all 5 profiles
    # with explicit `unix (...) type=stream,` rules; the active (non-disabled)
    # versions are the ones that should be loaded.
    case "$profile" in
      *.disabled) continue ;;
    esac
    local name
    name=$(basename "$profile")
    local prev_mode=""

    # Detect prior mode (complain/enforce) before we overwrite.
    if [[ -f "/etc/apparmor.d/$name" ]] && command -v aa-status >/dev/null 2>&1; then
      local profile_label
      profile_label=$(awk '/^profile / {print $2; exit}' "/etc/apparmor.d/$name" 2>/dev/null)
      if [[ -n "$profile_label" ]] && aa-status --json 2>/dev/null | grep -q "\"$profile_label\""; then
        if aa-status --json 2>/dev/null | python3 -c "import json,sys; d=json.load(sys.stdin); ps={**d.get('profiles',{}), **{p['name']:p['status'] for s in d.get('processes',{}).values() for p in s}}; print(ps.get('$profile_label','complain'))" 2>/dev/null | grep -q enforce; then
          prev_mode=enforce
        else
          prev_mode=complain
        fi
      fi
    fi

    # Remove any stale aa-disable symlink before reloading. aa-disable
    # creates /etc/apparmor.d/disable/$name → ../etc/apparmor.d/$name;
    # on upgrade runs where the profile was previously disabled the
    # symlink causes "Followed too many links" when apparmor_parser or
    # aa-complain try to re-read the profile.
    rm -f "/etc/apparmor.d/disable/$name"

    install -m 0644 -o root -g root "$profile" "/etc/apparmor.d/$name"
    apparmor_parser -r "/etc/apparmor.d/$name" 2>/dev/null || \
      _warn "apparmor_parser -r failed for $name — check 'apparmor_parser -d /etc/apparmor.d/$name'"

    if [[ $first_install -eq 1 ]] || [[ "$prev_mode" == "complain" ]] || [[ -z "$prev_mode" ]]; then
      aa-complain "/etc/apparmor.d/$name" >/dev/null 2>&1 || true
    elif [[ "$prev_mode" == "enforce" ]]; then
      aa-enforce "/etc/apparmor.d/$name" >/dev/null 2>&1 || true
    fi
  done

  systemctl daemon-reload >/dev/null 2>&1 || true
}

# ---------- step 8a.6: AIDE file integrity monitoring (M42) ----------------
#
# AIDE (Advanced Intrusion Detection Environment) is the FIM layer
# that LMD doesn't cover. LMD watches user docroots; AIDE watches
# system binaries + configs (/bin /sbin /usr/bin /usr/sbin /lib /etc
# /boot /root). Daily check via systemd timer; M14 event source
# fires on any diff. See ADR-0087 + plans/m42-aide-fim-system-integrity.md.

install_aide() {
  if ! dpkg -s aide >/dev/null 2>&1; then
    _spin "apt install aide + aide-common" \
      apt-get install -y -qq --no-install-recommends aide aide-common
  fi

  local conf=/etc/aide/aide.conf
  local conf_tmp
  conf_tmp=$(mktemp)
  cat >"$conf_tmp" <<'AIDE_CONF'
# Jabali — system-file integrity. ADR-0087.
# Excludes paths the panel writes to + ephemeral state.
#
# AIDE 0.19 removed `database=` (renamed to `database_in=`). Both
# database_in / database_out / report_url take URL values — keep
# the `file:` scheme prefix on all three. Bare paths are rejected
# as 'unknown URL-type'.
database_in=file:/var/lib/aide/aide.db
database_out=file:/var/lib/aide/aide.db.new
gzip_dbout=yes
report_url=file:/var/log/aide/aide.report.log
report_url=stdout

# Strong default rule: hash + meta but skip atime (mtime+ctime catch tamper).
JABRULE = p+i+n+u+g+s+m+c+sha256

# WATCH:
/bin            JABRULE
/sbin           JABRULE
/usr/bin        JABRULE
/usr/sbin       JABRULE
/usr/local/bin  JABRULE
/usr/local/sbin JABRULE
/lib            JABRULE
/lib64          JABRULE
/usr/lib        JABRULE
/etc            JABRULE
/boot           JABRULE
/root           JABRULE

# EXCLUDE — paths jabali or its dependencies write to:
!/etc/jabali
# GH #704: egress nftables (/etc/nftables.d/jabali-*), audit rules
# (/etc/audit/rules.d/jabali-*), and AppArmor profiles
# (/etc/apparmor.d/usr.local.bin.jabali-*) are NO LONGER excluded — they are
# static security-enforcement configs, so AIDE must flag tampering. Run
# `aide --update` (jabali aide rebuild) after any legitimate change to them.
!/etc/letsencrypt/live
!/etc/letsencrypt/archive
!/etc/letsencrypt/csr
!/etc/letsencrypt/keys
!/etc/letsencrypt/renewal
!/etc/letsencrypt/accounts
!/etc/nginx/sites-available/jabali-.*
!/etc/nginx/sites-enabled/jabali-.*
!/etc/php/.*/fpm/pool.d/jabali-.*
!/etc/systemd/system/jabali-.*
!/etc/systemd/system/user-.*\.slice\.d
!/etc/systemd/system/multi-user\.target\.wants
!/etc/cron\.d/jabali-.*
!/etc/aliases
!/etc/aliases\.db
!/etc/group-?
!/etc/passwd-?
!/etc/shadow-?
!/etc/gshadow-?
!/etc/mtab
!/etc/resolv\.conf
!/etc/adjtime
!/etc/machine-id
!/etc/ssh/ssh_host_.*_key.*

# Ephemeral state — never auditable:
!/var
!/run
!/proc
!/sys
!/tmp
!/home
!/dev
!/mnt
!/media
!/lost\+found
AIDE_CONF

  if [[ ! -f "$conf" ]] || ! cmp -s "$conf_tmp" "$conf"; then
    install -m 0640 -o root -g root "$conf_tmp" "$conf"
    _ok "AIDE config installed at $conf"
  fi
  rm -f "$conf_tmp"

  install -d -m 0755 /var/log/aide

  # Remove aide-common's stock conf.d fragments — they cover Apache, Dovecot,
  # Postfix, and other services Jabali doesn't run. Our standalone aide.conf
  # has no @@include, so they're dead files; purging avoids confusion.
  if compgen -G '/etc/aide/aide.conf.d/*' >/dev/null 2>&1; then
    rm -f /etc/aide/aide.conf.d/*
    _ok "removed stock /etc/aide/aide.conf.d/* fragments (unused by jabali config)"
  fi

  # Disable Debian's stock /etc/cron.daily/aide. aide-common ships a
  # cron job that runs aide check as the `_aide` user, which fails on
  # our 0755 root:root log dir (Permission denied on aide.report.log
  # — exit 17 every run). Our jabali-aide-check.timer covers the
  # daily check + runs as root with the right ProtectSystem +
  # ReadWritePaths hardening. Removing the stock cron prevents the
  # competing failure path.
  if [[ -f /etc/cron.daily/aide ]]; then
    rm -f /etc/cron.daily/aide
    _ok "removed Debian stock /etc/cron.daily/aide (jabali-aide-check.timer covers it)"
  fi

  # Disable + mask Debian's stock systemd AIDE units. Newer aide-common
  # (trixie) ships dailyaidecheck.timer + dailyaidecheck.service +
  # dailyaidecheck-buildcache.service IN ADDITION to the cron job. They run
  # the stock check, which depends on the /etc/aide/aide.conf.d/* fragments
  # we purge above (Apache/Dovecot/LVM/etc. rules jabali doesn't use) — so on
  # a non-LVM host dailyaidecheck-buildcache.service fails every run
  # referencing a non-existent 10_aide_lvm fragment (GH report 2026-06-09).
  # jabali-aide-check.timer already owns the daily check, so neutralize the
  # stock units to stop the competing failure (mirrors the cron removal).
  local _stock_aide_unit
  for _stock_aide_unit in dailyaidecheck.timer dailyaidecheck.service dailyaidecheck-buildcache.service; do
    if systemctl list-unit-files "$_stock_aide_unit" >/dev/null 2>&1        && systemctl list-unit-files "$_stock_aide_unit" 2>/dev/null | grep -q "$_stock_aide_unit"; then
      systemctl disable --now "$_stock_aide_unit" >/dev/null 2>&1 || true
      systemctl mask "$_stock_aide_unit" >/dev/null 2>&1 || true
      _ok "masked Debian stock $_stock_aide_unit (jabali-aide-check.timer covers it)"
    fi
  done

  # Initial DB build — only if missing AND no in-progress marker.
  # AIDE 0.19.1 requires an explicit --config (no implicit /etc/aide/
  # aide.conf default any more), or --init exits with
  # 'ERROR: missing configuration'. Pass it on every spawn.
  if [[ ! -f /var/lib/aide/aide.db ]] && [[ ! -f /var/lib/aide/.init-in-progress ]]; then
    install -d -m 0750 /var/lib/aide
    touch /var/lib/aide/.init-in-progress
    _log "AIDE: initial DB build (background — takes 2-5 min)"
    nohup bash -c '
      /usr/bin/aide --init --config=/etc/aide/aide.conf >/var/log/aide/init.log 2>&1
      if [[ -f /var/lib/aide/aide.db.new ]]; then
        mv /var/lib/aide/aide.db.new /var/lib/aide/aide.db
        chmod 0600 /var/lib/aide/aide.db
        date -u +%Y-%m-%dT%H:%M:%SZ > /var/lib/aide/.jabali-installed
      fi
      rm -f /var/lib/aide/.init-in-progress
    ' >/dev/null 2>&1 &
  fi

  # systemd units. Copied from install/systemd/.
  local aide_svc_src="${REPO_DIR}/install/systemd/jabali-aide-check.service"
  local aide_tmr_src="${REPO_DIR}/install/systemd/jabali-aide-check.timer"
  if [[ -f "$aide_svc_src" && -f "$aide_tmr_src" ]]; then
    install -m 0644 -o root -g root "$aide_svc_src" /etc/systemd/system/jabali-aide-check.service
    install -m 0644 -o root -g root "$aide_tmr_src" /etc/systemd/system/jabali-aide-check.timer
    systemctl daemon-reload >/dev/null 2>&1 || true
    systemctl enable --now jabali-aide-check.timer >/dev/null 2>&1 || \
      _warn "jabali-aide-check.timer enable failed — check 'journalctl -u jabali-aide-check.timer'"
  else
    _warn "AIDE systemd units missing at $aide_svc_src / $aide_tmr_src"
  fi

  _ok "AIDE installed (daily check via jabali-aide-check.timer)"
}

# ---------- step 8a.1: auto-restart drop-ins for critical services ----------
#
# Third-party packages ship with inconsistent Restart= defaults — some have
# `Restart=on-failure`, some `on-abnormal` (mariadb: restarts only on crash
# signals, NOT on non-zero exit), some omit it entirely. A stock Debian 13
# install can leave nginx, pdns, pdns-recursor, redis-server, crowdsec, and
# the crowdsec-firewall-bouncer with NO auto-restart at all, so a transient
# crash (OOM, disk spike, config reload race) bricks the service until the
# operator notices.
#
# Write a uniform drop-in that:
#   - Restart=on-failure   → restart on non-zero exit, NOT on manual stop
#   - RestartSec=5s        → short backoff, same as our jabali-* units
#   - StartLimitBurst=10   → tolerate 10 failures in the burst window
#                             (default 5 gave up too fast during a flap)
#   - StartLimitIntervalSec=60s → reset counter after 60s of stability
#
# Drop-in only — does NOT overwrite the package unit, so apt upgrades keep
# working. Idempotent: only daemon-reloads if the file content changed.
#
# sshd intentionally excluded — a bad sshd config shouldn't auto-retry
# forever and trap the operator (who may have just pushed a broken
# sshd_config.d drop-in). Manual restart is the correct failure mode there.
install_restart_drop_ins() {
  _log "installing Restart=on-failure drop-ins for critical services"

  local units=(
    nginx.service
    mariadb.service
    pdns.service
    pdns-recursor.service
    redis-server.service
    crowdsec.service
    systemd-resolved.service
    # Jabali daemons — OnFailure=jabali-notify@%N routes a service.down
    # M14 envelope when StartLimit is hit. Unit files already declare
    # Restart=on-failure; the drop-in's Restart=/RestartSec= lines are
    # redundant-but-harmless. Real win: native systemd → notification
    # bridge complementing the polling service_down event source.
    jabali-agent.service
    jabali-panel.service
    jabali-stalwart.service
    jabali-webmail.service
    jabali-kratos.service
  )

  # crowdsec-firewall-bouncer is package-variant-named. Pick whichever
  # exists (iptables/nftables/pf variants ship as different unit names).
  local cs_bouncer
  for cs_bouncer in crowdsec-firewall-bouncer-iptables.service \
                    crowdsec-firewall-bouncer-nftables.service \
                    crowdsec-firewall-bouncer.service; do
    if systemctl cat "$cs_bouncer" >/dev/null 2>&1; then
      units+=("$cs_bouncer")
      break
    fi
  done

  local changed=0 unit dropin_dir dropin dropin_new
  for unit in "${units[@]}"; do
    # Skip units the host doesn't have (e.g. a fresh box where one of
    # these wasn't installed for some reason — don't fail the install
    # over an optional dependency).
    if ! systemctl cat "$unit" >/dev/null 2>&1; then
      _warn "unit $unit not present on host; skipping auto-restart drop-in"
      continue
    fi
    dropin_dir="/etc/systemd/system/${unit}.d"
    dropin="${dropin_dir}/10-jabali-restart.conf"
    dropin_new="${dropin}.new"
    install -d -m 0755 "$dropin_dir"
    cat > "$dropin_new" <<'RESTARTCONF'
# Managed by jabali-panel install.sh. Uniform auto-restart policy for
# critical third-party services so a transient crash self-heals instead
# of waiting for the operator to notice. See install_restart_drop_ins()
# in install.sh for rationale. Hand edits will be overwritten on the
# next install.sh / `jabali update` run.
#
# OnFailure=jabali-notify@%N.service hooks the M14 notification path:
# when this unit hits StartLimit and gives up, systemd starts
# jabali-notify@<unit-name>.service which POSTs a service.down envelope
# to the panel-api enqueue endpoint. The notifier never blocks the
# restart loop (Type=oneshot, exits 0 even on transport failure).
[Unit]
StartLimitBurst=10
StartLimitIntervalSec=60s
OnFailure=jabali-notify@%N.service

[Service]
Restart=on-failure
RestartSec=5s
RESTARTCONF
    if [[ -f "$dropin" ]] && cmp -s "$dropin" "$dropin_new"; then
      rm -f "$dropin_new"
    else
      mv "$dropin_new" "$dropin"
      chmod 0644 "$dropin"
      changed=1
      _log "wrote ${dropin}"
    fi

    # Remove stale drop-ins from earlier install.sh versions. These were
    # superseded by 10-jabali-restart.conf above. Leaving them in place
    # is non-fatal except for ensure-logs.conf, whose ExecStartPre points
    # at /usr/local/bin/nginx-ensure-logs (script never shipped to repo,
    # so 203/EXEC crash-loops nginx on hosts that picked it up — incident
    # 2026-04-26 on mx.jabali-panel.com).
    local stale stale_drop
    for stale in ensure-logs.conf jabali-restart.conf; do
      stale_drop="${dropin_dir}/${stale}"
      if [[ -f "$stale_drop" ]]; then
        rm -f "$stale_drop"
        changed=1
        _log "removed stale drop-in ${stale_drop}"
      fi
    done
  done

  # Drop the orphaned ExecStartPre helper script too, if any host still
  # has it from the pre-2026-04-26 install.sh layout.
  if [[ -f /usr/local/bin/nginx-ensure-logs ]]; then
    rm -f /usr/local/bin/nginx-ensure-logs
    _log "removed stale /usr/local/bin/nginx-ensure-logs"
  fi

  if [[ "$changed" == "1" ]]; then
    systemctl daemon-reload
  fi
  _ok "auto-restart drop-ins installed for ${#units[@]} critical services"
}

# ---------- step 8a.2: OnFailure notifier template + helper (M14) ----------
#
# Receives the failed-unit name from systemd's %i, decodes it, and POSTs
# a service.down envelope to /api/v1/internal/notifications/enqueue over
# the panel-api unix socket. Wired up by the OnFailure= line in
# 10-jabali-restart.conf above so every critical drop-in fires the
# notifier on permanent failure (StartLimit hit).
#
# Idempotent: writes the helper script + template unit unconditionally,
# only daemon-reloads when content changed.
install_logrotate() {
  _log "installing logrotate drop-in"
  local src="${REPO_DIR}/install/logrotate/jabali"
  local dst="/etc/logrotate.d/jabali"
  if [[ ! -f "$src" ]]; then
    _warn "logrotate template missing at $src — skipping"
    return 0
  fi
  # Ensure /var/log/jabali exists with sane perms — install.sh's own
  # bootstrap mkdir runs before this function may have re-fired (jabali
  # update path), so make this idempotent here too.
  install -d -m 0750 -o root -g adm /var/log/jabali 2>/dev/null || \
    install -d -m 0750 -o root -g root /var/log/jabali
  # M45: root-terminal recordings dir. The agent PTY broker also
  # mkdirs this at session open; pre-creating it root:root 0750 gives
  # the logrotate /var/log/jabali/terminal/*.cast stanza a valid
  # parent on a host that has never opened a session.
  install -d -m 0750 -o root -g root /var/log/jabali/terminal 2>/dev/null || true
  if [[ ! -f "$dst" ]] || ! cmp -s "$src" "$dst"; then
    install -m 0644 -o root -g root "$src" "$dst"
    _ok "wrote $dst"
  fi
  # Validate syntax now so a broken drop-in surfaces at install time,
  # not 24 hours later on cron tick. -d = debug mode (parse only).
  # Skip when the binary is absent — base apt batch installs it but
  # this function runs on update paths that predate that addition.
  #
  # Parse the WHOLE config, not just "$dst". A duplicate log entry — the same
  # path declared by us and by a package's own drop-in — is invisible when each
  # file is parsed alone, and it is a hard error that makes logrotate skip an
  # entire config file and exit 1. That is how maldet's event_log silently
  # stopped rotating while logrotate.service sat failed (JAB-181). Warn only:
  # a pre-existing problem in someone else's drop-in must not abort an install.
  local lr_err=""
  if ! command -v logrotate >/dev/null 2>&1; then
    _warn "logrotate binary missing — skipping parse validation"
  else
    # -d is parse-only. Errors go to stderr alongside the verbose trace, so
    # keep stderr and drop stdout, then filter. `|| true` because grep exits 1
    # on no match, which is the good case. No `head` in the pipeline: under
    # `set -o pipefail` an early-exiting reader SIGPIPEs the producer and the
    # assignment fails, which has silently killed installs before.
    lr_err="$(logrotate -d /etc/logrotate.conf 2>&1 >/dev/null | grep -E '^error:' || true)"
    if [[ -n "$lr_err" ]]; then
      _warn "logrotate config has errors — logrotate.service will exit non-zero and SKIP the offending file, so those logs stop rotating:"
      while IFS= read -r line; do
        [[ -n "$line" ]] && _warn "  $line"
      done <<< "$lr_err"
    fi
  fi

  # JAB-104: the install logger falls back to /tmp/jabali_install-<ts>.log
  # when /var/log/jabali can't be created (see LOG_FILE bootstrap). Those
  # fall outside every logrotate policy, so age them out opportunistically on
  # each install/update tick — the only events that create them — so they
  # can't accumulate forever on hosts with weak /tmp cleanup.
  find /tmp -maxdepth 1 -name 'jabali_install-*.log' -type f -mtime +14 -delete 2>/dev/null || true
}

install_pdns_local_address_helper() {
  # PowerDNS treats a bind failure as fatal, and install.sh bakes the host's
  # addresses into local-address= at install time. When the address later
  # changes — DHCP lease, restore onto a new host, cloud reassignment,
  # failover — pdns dies on every start:
  #
  #   Fatal error: Unable to bind to UDP socket
  #   pdns.service: Start request repeated too quickly.
  #
  # and authoritative DNS stays down until an operator re-runs install.sh.
  # Seen on a host restored from an image onto a new address: 27 restarts,
  # then the start limit, then silence.
  #
  # ExecStartPre re-derives the list in the one moment it matters, with no
  # dependency on the database or the panel (both of which start after pdns).
  #
  # Reads $REPO_DIR/install/systemd/, so this must stay after
  # clone_or_update_repo — TestNoRepoDirReadsBeforeClone enforces that.
  local src="${REPO_DIR}/install/systemd/pdns-local-address"
  local dst=/usr/local/libexec/jabali/pdns-local-address
  local dropin_dir=/etc/systemd/system/pdns.service.d
  local dropin="${dropin_dir}/20-jabali-local-address.conf"

  if [[ ! -f "$src" ]]; then
    _warn "pdns-local-address helper missing at $src — skipping"
    return 0
  fi
  # No jabali pdns config means this host does not run our PowerDNS layout.
  if [[ ! -f /etc/powerdns/pdns.d/01-jabali-mysql.conf ]]; then
    return 0
  fi

  local changed=0
  install -d -m 0755 /usr/local/libexec/jabali
  if [[ ! -f "$dst" ]] || ! cmp -s "$src" "$dst"; then
    install -m 0755 -o root -g root "$src" "$dst"
    changed=1
  fi

  install -d -m 0755 "$dropin_dir"
  local want
  want="$(cat <<DROPIN
# Managed by jabali-panel install.sh. Hand edits will be overwritten.
#
# Re-point local-address= at the addresses this host currently has, before
# pdns binds them. Without this, any address change makes pdns fatal on
# start and takes authoritative DNS down until install.sh is re-run.
#
# The leading '+' is load-bearing. Debian's pdns.service runs User=pdns with
# ProtectSystem=full, so a plain ExecStartPre would run unprivileged with
# /etc read-only and exit 1 — and a failed ExecStartPre keeps the service
# down even when the config is fine, turning a self-heal into a second way
# to break DNS. '+' runs the helper as root outside the sandbox.
[Service]
ExecStartPre=+${dst}
DROPIN
)"
  if [[ ! -f "$dropin" ]] || [[ "$(cat "$dropin")" != "$want" ]]; then
    printf '%s\n' "$want" > "$dropin"
    chmod 0644 "$dropin"
    changed=1
  fi

  if (( changed )); then
    systemctl daemon-reload
    _log "pdns local-address self-heal installed"
  fi
  _ok "pdns local-address helper ready"
}

install_notify_template() {
  _log "installing OnFailure notifier (M14)"

  local helper_src="${REPO_DIR}/install/scripts/jabali-notify-onfailure"
  local helper_dst="/usr/local/bin/jabali-notify-onfailure"
  local unit_src="${REPO_DIR}/install/systemd/jabali-notify@.service"
  local unit_dst="/etc/systemd/system/jabali-notify@.service"
  local changed=0

  if [[ ! -f "$helper_src" || ! -f "$unit_src" ]]; then
    _warn "notifier template sources missing — skipping"
    return 0
  fi

  if [[ ! -f "$helper_dst" ]] || ! cmp -s "$helper_src" "$helper_dst"; then
    install -m 0755 -o root -g root "$helper_src" "$helper_dst"
    changed=1
    _log "wrote ${helper_dst}"
  fi
  if [[ ! -f "$unit_dst" ]] || ! cmp -s "$unit_src" "$unit_dst"; then
    install -m 0644 -o root -g root "$unit_src" "$unit_dst"
    changed=1
    _log "wrote ${unit_dst}"
  fi

  if [[ "$changed" == "1" ]]; then
    systemctl daemon-reload
  fi
  _ok "OnFailure notifier ready (jabali-notify@.service)"
}

# ---------- step 8b: Stalwart Mail Server + Bulwark webmail (M6) -------------
#
# Two functions, one tool each. Both are disabled-by-default — systemd units
# are installed but the services are enabled on first panel
# domain.email_enable call (from the agent). install.sh re-runs are
# idempotent: binaries are re-downloaded only on version bump, service-
# account users + data dirs + secrets are preserved across runs.
#
# Layout established here (plan §1 Step 1, ADR-0041):
#   /opt/stalwart/                      — extracted Stalwart binary
#   /usr/local/bin/stalwart             — symlink
#   /var/lib/stalwart/                  — RocksDB mail storage (jabali-mail:jabali-mail, 0750)
#   /etc/stalwart/config.toml           — rendered skeleton (Step 2 fills directory block)
#   /etc/jabali-panel/dkim/             — Ed25519 DKIM keys (jabali:jabali, 0750)
#   /etc/jabali-panel/stalwart-admin.token — JMAP admin bearer (jabali:jabali-mail, 0640)
#   /opt/jabali-webmail/                — Bulwark Next.js source + build output
#   /var/lib/jabali-webmail/settings/   — Bulwark settings-sync data dir
#   /etc/jabali-panel/bulwark.env       — Bulwark runtime env (jabali-webmail:jabali-webmail, 0640)
#   /etc/jabali-panel/bulwark-session.key — Bulwark SESSION_SECRET (jabali-webmail:jabali-webmail, 0640)

# STALWART_VERSION is the single pin for the Stalwart Mail server binary,
# consumed by both install_stalwart (fresh install) and upgrade_stalwart_binary
# (the jabali update path). Bump here + install/stalwart.sha256 together.
STALWART_VERSION="0.16.15"

# upgrade_stalwart_binary is the jabali-update entry point for the Stalwart
# server binary (GH #525). install.sh's full install_stalwart runs on fresh
# installs only; the update path re-copies the unit + push-cert but never
# re-downloads the SERVER binary, so a version bump never reached existing
# hosts (they stayed on the originally-installed version). This narrow function
# does JUST the binary: compare the installed version to the pinned target and,
# on mismatch, download + checksum-verify + atomically swap it (via the shared
# _install_stalwart_binary), then restart jabali-stalwart so the new binary goes
# live. Idempotent — a no-op when already at STALWART_VERSION. Mirrors how
# install_kratos / _install_bulwark self-upgrade on update.
upgrade_stalwart_binary() {
  local stalwart_binary="/usr/local/bin/stalwart"
  if [[ -x "$stalwart_binary" ]]; then
    local installed
    installed=$("$stalwart_binary" --version 2>&1 | grep -oP '[0-9]+\.[0-9]+\.[0-9]+' | head -n1 || echo "unknown")
    if [[ "$installed" == "$STALWART_VERSION" ]]; then
      _ok "Stalwart already at $STALWART_VERSION"
      return 0
    fi
    _log "upgrading Stalwart $installed -> $STALWART_VERSION"
  else
    _log "installing Stalwart $STALWART_VERSION (binary absent)"
  fi
  _install_stalwart_binary "$STALWART_VERSION"
  if systemctl is-active --quiet jabali-stalwart 2>/dev/null; then
    systemctl restart jabali-stalwart \
      || _warn "jabali-stalwart restart failed after binary upgrade — check 'journalctl -u jabali-stalwart'"
    _ok "jabali-stalwart restarted on Stalwart $STALWART_VERSION"
  fi
}

install_stalwart() {
  local stalwart_version="$STALWART_VERSION"
  _log "installing Stalwart Mail Server (v${stalwart_version})"

  # M353 / GH #545 concurrency guard. install_stalwart runs from TWO paths
  # that overlap on a fresh install: install.sh's own inline call, and the
  # panel agent re-entering via `source install.sh && install_stalwart` to
  # provision the mail module once the panel is up. Both race on the shared
  # /tmp tarball, the /opt/stalwart atomic swap, and the service restart --
  # the losing racer dies at `sha256sum` when the winner's cleanup deletes
  # the tarball out from under it (observed on a fresh Debian 13 install,
  # GH #545 repro: install-<A>.log died at _install_stalwart_binary while
  # install-<B>.log completed the module 3s earlier). Serialize the whole
  # function behind an flock so the second caller waits, then the version /
  # dir / config idempotency below turns it into a fast no-op. Best-effort:
  # if flock is unavailable we proceed unlocked rather than block the install.
  local _sw_lockfd=""
  if command -v flock >/dev/null 2>&1; then
    exec {_sw_lockfd}>/run/lock/jabali-stalwart-install.lock 2>/dev/null || _sw_lockfd=""
    if [[ -n "$_sw_lockfd" ]]; then
      flock -w 600 "$_sw_lockfd" \
        || _warn "waited 600s for the Stalwart install lock -- proceeding without exclusion"
    fi
  fi

  # Purge any preinstalled MTA that would conflict with Stalwart on
  # port 25 (postfix, exim4, sendmail). Proxmox's Debian LXC template
  # ships postfix; Hetzner and OVH bare-metal Debian / Ubuntu commonly
  # ship exim4-base. Stalwart binds :25 directly; an MTA already
  # listening there silently steals every inbound mail until Stalwart
  # fails the bind on next restart. GH #129.
  for mta in postfix sendmail-bin exim4 exim4-base exim4-config exim4-daemon-light nullmailer; do
    if dpkg -s "$mta" >/dev/null 2>&1; then
      _log "purging conflicting MTA: $mta"
      systemctl stop "$mta" 2>/dev/null || true
      systemctl disable "$mta" 2>/dev/null || true
      DEBIAN_FRONTEND=noninteractive apt-get purge -y -qq "$mta" >/dev/null 2>&1 ||         _warn "could not purge $mta -- check manually"
    fi
  done
  # Apt may leave config dirs after a purge if other packages depend on
  # them. /etc/postfix is safe to remove since Stalwart writes its own
  # /etc/stalwart and /var/lib/stalwart.
  rm -rf /etc/postfix /etc/exim4 /etc/sendmail 2>/dev/null || true

  # Ensure service user + group exist. JAB-357: jabali-mail must NOT join the
  # broad `$SERVICE_USER` (jabali) group — that group owns the root Agent socket
  # and panel secrets, so a mail compromise must not hold it. Stalwart reads its
  # admin token via its own primary group (token is 0640 jabali:jabali-mail) and
  # signs DKIM from Stalwart's registry (seeded by the root agent), so the broad
  # group is dead weight. ensure_stalwart_not_in_panel_group() converges
  # already-installed hosts that still carry the legacy membership.
  if ! getent passwd jabali-mail >/dev/null 2>&1; then
    _log "creating jabali-mail service user"
    useradd --system --no-create-home --shell /usr/sbin/nologin \
      --user-group jabali-mail
  fi

  # Data + config dirs. `install -d` only changes the dir itself's
  # ownership — any sub-files left from a prior install / migration
  # carry their old owner and Stalwart's RocksDB will refuse to open
  # the column families. Recursive chown is the durable fix
  # (verified on QA round 3 where /var/lib/stalwart had a
  # mixed-owner tree from a previous install attempt).
  install -d -m 0750 -o jabali-mail -g jabali-mail /var/lib/stalwart
  install -d -m 0750 -o jabali-mail -g jabali-mail /etc/stalwart
  # ADR-0115 — Stalwart Log tracer writes mail-bf-relevant events here so
  # CrowdSec can tail them. AppArmor profile must grant /var/log/stalwart/
  # rw separately (install/apparmor/usr.local.bin.stalwart-mail).
  install -d -m 0750 -o jabali-mail -g jabali-mail /var/log/stalwart
  install -d -m 0750 -o "$SERVICE_USER" -g "$SERVICE_USER" /etc/jabali-panel/dkim
  chown -R jabali-mail:jabali-mail /var/lib/stalwart 2>/dev/null || true
  chown -R jabali-mail:jabali-mail /etc/stalwart 2>/dev/null || true

  local stalwart_binary="/usr/local/bin/stalwart"
  local stalwart_install_dir="/opt/stalwart"

  # Idempotence: skip re-download if the installed binary reports the
  # target version. Stalwart's version command output format is stable
  # across 0.14.x-0.16.x ("Stalwart Mail Server v0.16.0").
  if [[ -x "$stalwart_binary" ]]; then
    local installed_version
    # `stalwart --version` prints a bare semver ("0.16.14", no `v` prefix), so
    # match the plain version — an anchored `v\K` matched nothing here, leaving
    # installed_version="unknown" and re-downloading the binary on EVERY run
    # (every `jabali update` / `--install-module mail`). (M353 idempotency fix.)
    installed_version=$("$stalwart_binary" --version 2>&1 | grep -oP '[0-9]+\.[0-9]+\.[0-9]+' | head -n1 || echo "unknown")
    if [[ "$installed_version" == "$stalwart_version" ]]; then
      _ok "Stalwart $stalwart_version already installed"
    else
      _warn "upgrading Stalwart $installed_version -> $stalwart_version"
      _install_stalwart_binary "$stalwart_version"
    fi
  else
    _install_stalwart_binary "$stalwart_version"
  fi

  # stalwart-cli is the v0.16 management surface (ADR-0045). Install
  # alongside the server so apply-plan.json can be provisioned during
  # the bootstrap step (follow-up commit). Version-pin independently of
  # the server binary.
  _install_stalwart_cli

  # Vendor the spam-filter rules bundle into /opt/stalwart/share before
  # apply-plan references it via file://. Pinned to a known SHA for
  # reproducibility; the auto-refresh timer (jabali-spam-rules-update)
  # pulls /releases/latest in-place AFTER bootstrap.
  _install_spam_rules

  # JMAP admin token — used later for panel <-> Stalwart management auth
  # (JMAP basic auth with stored credential). Generated once + preserved
  # across re-runs so a re-install doesn't break the panel-agent's auth.
  local admin_token_file="/etc/jabali-panel/stalwart-admin.token"
  if [[ ! -f "$admin_token_file" ]]; then
    _log "generating Stalwart admin token -> $admin_token_file"
    umask 077
    openssl rand -base64 32 >"$admin_token_file"
    chmod 0640 "$admin_token_file"
    chown "$SERVICE_USER":jabali-mail "$admin_token_file"
  else
    _ok "Stalwart admin token already present"
  fi

  # MariaDB read-only password for Stalwart's SQL directory lookups.
  # Generated here (needed for config.json template rendering below), but
  # the actual CREATE USER + GRANT happens in install_stalwart_apply after
  # start_and_verify — migration 000054 creates the mailboxes table and
  # GRANT SELECT on a non-existent table is a fatal error (ERROR 1146).
  local stalwart_db_pw_file="/etc/jabali-panel/stalwart-mariadb.password"
  if [[ ! -f "$stalwart_db_pw_file" ]]; then
    _log "generating Stalwart MariaDB password -> $stalwart_db_pw_file"
    umask 077
    openssl rand -hex 32 >"$stalwart_db_pw_file"
    chmod 0640 "$stalwart_db_pw_file"
    chown root:jabali-mail "$stalwart_db_pw_file"
  fi
  local stalwart_db_pass
  stalwart_db_pass="$(cat "$stalwart_db_pw_file")"

  # Render /etc/stalwart/config.json from template. v0.16's config.json
  # is just a single tagged-enum `DataStore` descriptor for the REGISTRY
  # store (ADR-0045); it holds settings, directories, listeners, DKIM
  # etc. All mail storage / SQL directory backends are JMAP objects
  # inside that registry, applied via `stalwart-cli apply`. Template
  # therefore has no mustaches — but install.sh still runs the mustache
  # sanity check to protect against future template drift.
  local stalwart_config="/etc/stalwart/config.json"
  if [[ ! -f "${REPO_DIR}/install/stalwart/config.json.tmpl" ]]; then
    _die "Stalwart config template not found at ${REPO_DIR}/install/stalwart/config.json.tmpl"
  fi
  install -m 0640 -o jabali-mail -g jabali-mail \
    "${REPO_DIR}/install/stalwart/config.json.tmpl" "$stalwart_config"

  if grep -q '{{\..*}}' "$stalwart_config"; then
    _die "unsubstituted mustaches in $stalwart_config — template drift?"
  fi
  _ok "Stalwart datastore config at $stalwart_config"

  # stalwart.env — systemd EnvironmentFile. Populated with
  # STALWART_RECOVERY_ADMIN=admin:<stalwart-admin.token> so Stalwart
  # accepts Basic-auth calls from the panel-agent (ADR-0045 §Bootstrap).
  # Written/rewritten on every install run so a rotated admin token
  # propagates into the unit after a `jabali update`.
  local stalwart_env="/etc/jabali-panel/stalwart.env"
  local stalwart_admin_token
  stalwart_admin_token="$(cat "$admin_token_file")"
  cat >"$stalwart_env" <<EOF
# Stalwart Mail Server — systemd EnvironmentFile.
# Managed by install.sh. Do NOT hand-edit.
# STALWART_RECOVERY_ADMIN seeds an admin principal Stalwart accepts for
# HTTP Basic auth against /jmap; paired with the token at
# ${admin_token_file} the panel-agent uses for every management call.
STALWART_RECOVERY_ADMIN=admin:${stalwart_admin_token}
EOF
  chmod 0640 "$stalwart_env"
  chown root:jabali-mail "$stalwart_env"
  _ok "Stalwart env written (admin seed) at $stalwart_env"

  # Render /etc/jabali-panel/stalwart-apply-plan.json from template.
  # This is the JMAP declarative plan (ADR-0045) that seeds the
  # SqlDirectory + listeners + Authentication pointer. Rendered every
  # run; stalwart-cli apply is idempotent against already-applied state.
  local stalwart_apply_plan="/etc/jabali-panel/stalwart-apply-plan.json"
  if [[ ! -f "${REPO_DIR}/install/stalwart/apply-plan.json.tmpl" ]]; then
    _die "Stalwart apply plan template not found at ${REPO_DIR}/install/stalwart/apply-plan.json.tmpl"
  fi
  sed -e "s|{{\.MariaDBPassword}}|${stalwart_db_pass}|g" \
    "${REPO_DIR}/install/stalwart/apply-plan.json.tmpl" >"$stalwart_apply_plan"
  chown root:jabali-mail "$stalwart_apply_plan"
  chmod 0640 "$stalwart_apply_plan"
  if grep -q '{{\..*}}' "$stalwart_apply_plan"; then
    _die "unsubstituted mustaches in $stalwart_apply_plan — template drift?"
  fi
  _ok "Stalwart apply plan at $stalwart_apply_plan"

  # Systemd unit — installed then started + applied. We start on install
  # (not lazy on first domain.email_enable) because applying the plan
  # requires a running /jmap endpoint; the bootstrap sequence is:
  #
  #   1. install/update the unit
  #   2. systemctl daemon-reload
  #   3. systemctl enable --now jabali-stalwart
  #   4. poll 127.0.0.1:8446/jmap/session until 2xx/4xx (ready)
  #   5. stalwart-cli apply --file <plan>
  #
  # Ports 25/465/587/993 will bind on step 3. On a host with no
  # email-enabled domains this is an idle listener — Stalwart 550s
  # any inbound recipient until a Domain object exists in the registry
  # (which domain.email_enable creates via JMAP on first enable).
  if [[ ! -f "${REPO_DIR}/install/systemd/jabali-stalwart.service" ]]; then
    _die "Stalwart systemd unit not found at ${REPO_DIR}/install/systemd/jabali-stalwart.service"
  fi
  install -m 0644 -o root -g root "${REPO_DIR}/install/systemd/jabali-stalwart.service" \
    /etc/systemd/system/jabali-stalwart.service

  # JAB-216: bound the mail server's memory before step 3 starts it, so a
  # fresh host never runs an unbounded Stalwart even for one boot. Must come
  # after the unit is on disk (the drop-in directory is named for it) and
  # before the enable --now below.
  bound_stalwart_memory || _warn "stalwart memory bounds not applied (non-fatal)"

  # Spam-filter rules weekly refresh. Refresh script + timer + service.
  # Enabled+started here so a fresh install ends with the timer armed.
  local refresh_src="${REPO_DIR}/install/stalwart/jabali-spam-rules-refresh"
  local refresh_dst="/usr/local/bin/jabali-spam-rules-refresh"
  if [[ ! -f "$refresh_src" ]]; then
    _die "spam-rules refresh script missing at $refresh_src"
  fi
  install -m 0755 -o root -g root "$refresh_src" "$refresh_dst"

  for unit in jabali-spam-rules-update.service jabali-spam-rules-update.timer; do
    if [[ ! -f "${REPO_DIR}/install/systemd/${unit}" ]]; then
      _die "${unit} not found at ${REPO_DIR}/install/systemd/${unit}"
    fi
    install -m 0644 -o root -g root "${REPO_DIR}/install/systemd/${unit}" \
      "/etc/systemd/system/${unit}"
  done

  systemctl daemon-reload
  systemctl enable --now jabali-spam-rules-update.timer >/dev/null 2>&1 || \
    _warn "could not enable jabali-spam-rules-update.timer — re-run install.sh or 'systemctl enable --now jabali-spam-rules-update.timer'"
  _ok "jabali-stalwart.service installed (apply deferred to install_stalwart_apply); spam-rules weekly refresh timer armed"

  # Release the concurrency lock (see the flock at the top of the function).
  # _die paths exit the process, so the kernel drops the fd there; this only
  # covers the normal fall-through so a later legitimate caller can proceed.
  [[ -n "$_sw_lockfd" ]] && exec {_sw_lockfd}>&- || true
}

# install_stalwart_apply — second phase of Stalwart bootstrap. Runs AFTER
# start_and_verify so that jabali-panel.service has applied migration
# 000054 (which creates jabali_panel.mailboxes + jabali_panel.domains).
# This phase:
#   1. Creates the jabali-stalwart-ro MariaDB user + SELECT grants
#   2. Enables + starts jabali-stalwart.service
#   3. Polls /jmap until ready
#   4. Runs stalwart-cli apply against the rendered plan
#
# Split out from install_stalwart (ADR-0045 bootstrap flow) because step 1
# requires the mailboxes table to already exist — migrations run inside
# the panel service on first start, not up-front in install.sh.
install_stalwart_apply() {
  _log "provisioning Stalwart MariaDB user + applying JMAP plan"

  # M25 Step 7: Stalwart seeds factory NetworkListeners into RocksDB on
  # first start (http at [::]:8080, https at [::]:443). stalwart-cli
  # apply is create-only and cannot remove them. _install_stalwart_apply_plan
  # calls _delete_stalwart_factory_listeners to remove them via the API
  # before restarting. See ADR-0050 §"Factory listener problem".

  local stalwart_db_user="jabali-stalwart-ro"
  local stalwart_db_pw_file="/etc/jabali-panel/stalwart-mariadb.password"
  if [[ ! -f "$stalwart_db_pw_file" ]]; then
    _die "Stalwart MariaDB password file missing at $stalwart_db_pw_file (install_stalwart must run first)"
  fi
  local stalwart_db_pass
  stalwart_db_pass="$(cat "$stalwart_db_pw_file")"

  # SELECT-only grant. Stalwart never writes to the source-of-truth
  # directory; on-every-auth `synchronize_account` writes into its own
  # registry (ADR-0045 §"Cache/invalidation model").
  mariadb -e "
    CREATE USER IF NOT EXISTS '${stalwart_db_user}'@'localhost' IDENTIFIED BY '${stalwart_db_pass}';
    ALTER USER '${stalwart_db_user}'@'localhost' IDENTIFIED BY '${stalwart_db_pass}';
    GRANT SELECT ON jabali_panel.mailboxes         TO '${stalwart_db_user}'@'localhost';
    GRANT SELECT ON jabali_panel.domains           TO '${stalwart_db_user}'@'localhost';
    GRANT SELECT ON jabali_panel.email_forwarders  TO '${stalwart_db_user}'@'localhost';
    GRANT SELECT ON jabali_panel.mail_groups        TO '${stalwart_db_user}'@'localhost';
    GRANT SELECT ON jabali_panel.mail_group_members TO '${stalwart_db_user}'@'localhost';
    FLUSH PRIVILEGES;
  "
  _ok "Stalwart MariaDB user provisioned: ${stalwart_db_user} (SELECT on mailboxes, domains, email_forwarders, mail_groups, mail_group_members)"

  local admin_token_file="/etc/jabali-panel/stalwart-admin.token"
  if [[ ! -f "$admin_token_file" ]]; then
    _die "Stalwart admin token missing at $admin_token_file (install_stalwart must run first)"
  fi
  local stalwart_admin_token
  stalwart_admin_token="$(cat "$admin_token_file")"

  local stalwart_apply_plan="/etc/jabali-panel/stalwart-apply-plan.json"
  if [[ ! -f "$stalwart_apply_plan" ]]; then
    _die "Stalwart apply plan missing at $stalwart_apply_plan (install_stalwart must run first)"
  fi

  _install_stalwart_apply_plan "$stalwart_apply_plan" "$stalwart_admin_token"
  _ok "jabali-stalwart.service started + plan applied"

  # Push the panel-mail LE cert into Stalwart so IMAPS/SMTP submission
  # serve a browser-trusted cert instead of Stalwart's rcgen
  # self-signed fallback (CN=rcgen self signed cert, SAN=localhost).
  # No-op when the cert isn't on disk yet (fresh install before the
  # ACME hook has run) — the deploy-hook re-runs this on first issue.
  if [[ -x /usr/local/bin/jabali-stalwart-push-cert ]]; then
    /usr/local/bin/jabali-stalwart-push-cert || \
      _warn "jabali-stalwart-push-cert returned non-zero (continuing — deploy-hook will retry)"
  fi

  # M25 Step 7 verification: post-apply, Stalwart's localhost-only
  # listeners (admin-http on 8080, JMAP on 8446, internal/training on
  # 18181) MUST NOT be bound to 0.0.0.0 or [::]. The public listeners
  # (smtp 25/465/587, imap 993) are intentionally wildcard and skipped.
  # Each verify_no_all_interface_binds returns 0 if no listener exists
  # OR all listeners are loopback — a freshly-restarted Stalwart that
  # hasn't bound 8080 yet still passes (because the helper is "no
  # wildcard binds", not "must be present").
  _log "verifying Stalwart bind state (M25 Step 7)"
  if ! verify_no_all_interface_binds 8080; then
    _die "Stalwart factory http listener still bound on :8080 — _delete_stalwart_factory_listeners may have failed; check 'journalctl -u jabali-stalwart'"
  fi
  if ! verify_no_all_interface_binds 8446; then
    _die "Stalwart JMAP on :8446 is bound 0.0.0.0/[::] — apply-plan listener corrupt"
  fi
  if ! verify_no_all_interface_binds 18181; then
    _die "Stalwart internal listener on :18181 is bound 0.0.0.0/[::] — apply-plan listener corrupt"
  fi
  # Belt-and-braces: if the legacy ephemeral :35181 is still up (an
  # operator who hasn't restarted Stalwart since install) flag it as
  # WARN, not DIE. The runbook tells them to restart jabali-stalwart.
  if ss -lntp 2>/dev/null | grep -qE '(\*|0\.0\.0\.0|\[::\]):35181'; then
    _warn "Stalwart's legacy ephemeral :35181 is still bound on a wildcard — restart jabali-stalwart to pick up the M25 Step 7 #internal-loopback listener"
  fi

  _verify_spam_filter_loaded "$stalwart_admin_token"
}

# _verify_spam_filter_loaded queries the SpamSettings singleton via
# stalwart-cli and asserts:
#   - enable == true
#   - spamFilterRulesUrl points at our pinned file:// path
# Smoke-only: WARN on miss (don't _die) because Stalwart's spam filter
# is a feature flag, not a bootstrap blocker. Drift here means the next
# operator-visible event (mail with a spam tag) will trip; failing the
# install over it would be too aggressive.
_verify_spam_filter_loaded() {
  local admin_token="$1"
  local expected_url="file:///opt/stalwart/share/spam-filter-rules.json.gz"
  # `get x:SpamSettings` (no id) — singletons default to id "singleton"
  # per stalwart-cli help. `query` rejects singletons with
  # "SpamSettings is a singleton and does not support query".
  local out
  out="$(STALWART_URL="http://127.0.0.1:8446" \
    STALWART_USER="admin" \
    STALWART_PASSWORD="$admin_token" \
    /usr/local/bin/stalwart-cli get x:SpamSettings --json 2>&1 || true)"
  if [[ -z "$out" ]] || ! printf '%s' "$out" | python3 -c 'import sys,json; json.load(sys.stdin)' 2>/dev/null; then
    _warn "spam filter smoke: SpamSettings get returned non-JSON — apply-plan may not have landed; check 'journalctl -u jabali-stalwart' (raw: ${out:0:120})"
    return
  fi
  local enable rules_url
  enable="$(printf '%s\n' "$out" | python3 -c \
    'import sys,json
try:
    obj=json.load(sys.stdin)
    print(str(obj.get("enable","")).lower())
except Exception: pass' 2>/dev/null || true)"
  rules_url="$(printf '%s\n' "$out" | python3 -c \
    'import sys,json
try:
    obj=json.load(sys.stdin)
    print(obj.get("spamFilterRulesUrl",""))
except Exception: pass' 2>/dev/null || true)"
  if [[ "$enable" != "true" ]]; then
    _warn "spam filter smoke: enable=${enable:-<unset>} (expected 'true')"
  fi
  if [[ "$rules_url" != "$expected_url" ]]; then
    _warn "spam filter smoke: rules URL '${rules_url:-<unset>}' (expected '${expected_url}') — apply-plan SpamSettings update may not have stuck; try 'stalwart-cli get x:SpamSettings --json'"
  fi
  if [[ "$enable" == "true" ]] && [[ "$rules_url" == "$expected_url" ]]; then
    _ok "spam filter smoke: enabled + rules pinned to $expected_url"
  fi
}

# _delete_stalwart_factory_listeners removes Stalwart's built-in factory
# NetworkListeners that bind to [::] (all interfaces). Stalwart seeds
# these into RocksDB on first start; stalwart-cli apply is create-only
# and cannot delete or replace them. We explicitly delete them before
# restarting so Stalwart does not rebind to all-interface ports we don't
# want (e.g. [::]:8080 web UI, [::]:443 HTTPS web UI).
#
# Arguments: $1 = jmap_port (8080 or 8446), $2 = admin_token
#
# Factory listeners removed: http ([::]:8080), https ([::]:443)
_delete_stalwart_factory_listeners() {
  local jmap_port="$1"
  local admin_token="$2"
  local -a factory_names=("http" "https")
  for fname in "${factory_names[@]}"; do
    local query_out id=""
    query_out="$(STALWART_URL="http://127.0.0.1:${jmap_port}" \
      STALWART_USER="admin" \
      STALWART_PASSWORD="$admin_token" \
      /usr/local/bin/stalwart-cli query x:NetworkListener \
        --where "name=${fname}" --json 2>/dev/null || true)"
    # stalwart-cli query --where name=X returns a bare object for a single
    # match (NOT a one-element list). The previous parser only handled the
    # list case → silently dropped the id → caller logged "already removed"
    # and skipped the delete API call → factory http listener stayed bound
    # on [::]:8080, tripping verify_no_all_interface_binds at the next
    # gate. Handle both list and dict shapes; fall through to empty if
    # the payload is anything else (null, error envelope, etc.).
    id="$(printf '%s\n' "$query_out" \
      | python3 -c '
import sys, json
try:
    d = json.load(sys.stdin)
except Exception:
    sys.exit(0)
if isinstance(d, list) and d and isinstance(d[0], dict) and d[0].get("id"):
    print(d[0]["id"])
elif isinstance(d, dict) and d.get("id"):
    print(d["id"])
' 2>/dev/null || true)"
    if [[ -z "$id" ]] || [[ "$id" == "None" ]]; then
      _log "factory NetworkListener '${fname}' not found — already removed"
      continue
    fi
    _log "deleting factory NetworkListener '${fname}' (id=${id})"
    local del_rc=0
    STALWART_URL="http://127.0.0.1:${jmap_port}" \
      STALWART_USER="admin" \
      STALWART_PASSWORD="$admin_token" \
      /usr/local/bin/stalwart-cli delete x:NetworkListener --ids "$id" 2>/dev/null || del_rc=$?
    if (( del_rc == 0 )); then
      _ok "factory NetworkListener '${fname}' (id=${id}) deleted"
    else
      _warn "stalwart-cli delete x:NetworkListener --ids '${id}' failed (rc=${del_rc})"
    fi
  done
}

# _install_stalwart_apply_plan starts Stalwart (if not already running),
# waits for /jmap to be reachable, runs stalwart-cli apply against the
# rendered plan, then deletes factory listeners and restarts. Idempotent:
# on re-runs where Stalwart is already on :8446, apply is skipped but
# factory listener deletion and the restart still run to converge state.
_install_stalwart_apply_plan() {
  local plan_file="$1"
  local admin_token="$2"

  if ! systemctl is-enabled --quiet jabali-stalwart.service 2>/dev/null; then
    _log "enabling + starting jabali-stalwart.service"
    systemctl enable --now jabali-stalwart.service
  elif ! systemctl is-active --quiet jabali-stalwart.service; then
    _log "starting jabali-stalwart.service"
    systemctl start jabali-stalwart.service
  fi

  # Poll /jmap/session until Stalwart is serving HTTP. A 401 counts as
  # "ready" — it means the HTTP layer is up and rejecting our missing
  # Authorization header, which is exactly what we want before we try
  # to run an authenticated apply. Only 2xx/3xx/4xx are accepted; 5xx
  # means "server exists but is broken" and we keep polling. 000 means
  # curl couldn't connect (daemon not listening yet).
  #
  # Port probing: on first run the apply-plan has NOT created the
  # `jmap-loopback` NetworkListener yet, so Stalwart falls back to its
  # built-in default HTTP port 8080. On every subsequent run the
  # registry holds the plan's `127.0.0.1:8446` listener, so 8446 is the
  # management port. We probe both and apply against whichever answers.
  local jmap_port=""
  local jmap_status=""
  local waited=0
  local max_wait=30
  while (( waited < max_wait )); do
    for p in 8446 8080; do
      local status
      # `|| true` is load-bearing: curl exits 7 on "connection refused"
      # which is expected while Stalwart is still binding its listeners.
      # Under `set -euo pipefail`, the bare assignment would abort the
      # script on the first refused port before we ever get to try :8080.
      status="$(curl -sS -o /dev/null -w '%{http_code}' --connect-timeout 2 -m 3 \
        "http://127.0.0.1:${p}/jmap/session" 2>/dev/null || true)"
      status="${status:-000}"
      if [[ "$status" =~ ^[234][0-9][0-9]$ ]]; then
        jmap_port="$p"
        jmap_status="$status"
        break 2
      fi
    done
    sleep 1
    waited=$((waited + 1))
  done
  if [[ -z "$jmap_port" ]]; then
    _err "Stalwart /jmap did not come up on 8446 or 8080 within ${max_wait}s — check 'journalctl -u jabali-stalwart'"
    _die "Stalwart bootstrap timed out"
  fi
  _ok "Stalwart /jmap ready on :${jmap_port} (HTTP ${jmap_status}) after ${waited}s"

  # If Stalwart is serving on :8446 we know the plan is already applied
  # (that's the whole point of the 8080→restart→8446 dance below). Re-
  # running `stalwart-cli apply` against an already-applied plan would
  # CREATE A DUPLICATE Directory because @type=create has no name-based
  # dedup — Stalwart returns a fresh autogenerated id and the system
  # carries two parallel directories until the operator cleans up.
  # Schema-evolution fields (queryRecipient, queryEmailAliases per
  # ADR-0073) instead converge via a separate post-apply
  # stalwart-cli update step that runs unconditionally.
  local skip_apply=0
  if [[ "$jmap_port" == "8446" ]]; then
    _ok "Stalwart plan already applied (serving on :8446) — skipping re-apply"
    skip_apply=1
  fi

  if (( skip_apply == 0 )); then
  # Idempotent apply: stalwart-cli apply uses @type: create for every
  # NetworkListener, which fails primaryKeyViolation on re-run because
  # there's no first-class upsert verb. Two re-run scenarios cause this:
  #
  #   (a) Full re-apply: every object in RocksDB from a prior successful
  #       apply. Every create step reports primaryKeyViolation. No harm
  #       done — nothing to converge.
  #
  #   (b) Partial re-apply: a subset of objects in RocksDB from a prior
  #       apply that completed some creates before being interrupted
  #       (operator Ctrl-C, VM reboot, crash in a later install step).
  #       Re-running MUST succeed for the missing objects OR we ship an
  #       incomplete config to the host (e.g. M25 Step 7 adding new
  #       listeners to a plan whose older siblings are already applied).
  #
  # Use --continue-on-error so apply reports every failure at the end
  # but keeps going through the rest. Then filter the failure lines: if
  # every failure is primaryKeyViolation (pre-existing object), treat
  # as idempotent success; anything else is a real error (schema drift,
  # auth failure, RocksDB corruption) and we _die.
  _log "applying plan via stalwart-cli against :${jmap_port} (--continue-on-error; primaryKeyViolation on pre-existing objects is idempotent)"
  # stalwart-cli v1.0.7 dropped the legacy JSON-array plan format and
  # now only accepts NDJSON (one top-level object per line). The plan
  # template stays a pretty-printed JSON array for operator
  # readability — feed it through `jq -c '.[]'` to flatten on the way
  # in. Without this the CLI errors out with "invalid plan NDJSON on
  # line 1: EOF while parsing a list" and (because the parse error
  # produces no per-operation ✗ lines) the categorizer below mistakes
  # the failure for an idempotent re-apply and prints _ok — silently
  # shipping an empty Stalwart config (no Directory, no listeners, no
  # SpamSettings). Caught 2026-05-24 on testserver after the
  # 1.0.0 → 1.0.7 bump.
  if ! command -v jq >/dev/null; then
    _die "jq is required to feed the apply-plan as NDJSON (stalwart-cli >=1.0.7 dropped JSON-array support)"
  fi
  local apply_out apply_rc=0
  apply_out="$(jq -c '.[]' "$plan_file" | STALWART_URL="http://127.0.0.1:${jmap_port}" \
    STALWART_USER="admin" \
    STALWART_PASSWORD="$admin_token" \
    /usr/local/bin/stalwart-cli apply --continue-on-error --stdin 2>&1)" || apply_rc=$?

  # Detect a plan-parse failure (top-level "error: invalid plan ...")
  # that produced zero per-operation ✗ lines, otherwise the idempotent-
  # success branch below silently masks it. The 1.0.7 NDJSON migration
  # surfaced this gap; keep it generic so future CLI format breakage
  # fails loud.
  if (( apply_rc != 0 )) && printf '%s\n' "$apply_out" | grep -qE '^error: (invalid plan|failed to parse)'; then
    _err "stalwart-cli rejected the plan before any operation ran:"
    printf '%s\n' "$apply_out" | grep -E '^error:' >&2
    _die "Stalwart apply plan parse failed (CLI format change?)"
  fi

  # Print the CLI's summary to the install log so operators can see what
  # was created vs already-existed in a single line. Trim to last 20
  # lines to keep the install transcript readable on large plans.
  printf '%s\n' "$apply_out" | tail -20

  if (( apply_rc != 0 )); then
    # Inspect every per-operation failure line (starts with ✗) and
    # categorize: primaryKeyViolation = idempotent (object already
    # exists from a prior apply), anything else = real error. Ignore
    # the trailing `error: apply completed with N failed operation(s)`
    # summary — it's just a restatement of rc!=0 and tells us nothing
    # about whether the underlying failures are idempotent.
    local non_idempotent_errs
    non_idempotent_errs="$(printf '%s\n' "$apply_out" \
      | grep '^✗' \
      | grep -v 'primaryKeyViolation' || true)"
    if [[ -n "$non_idempotent_errs" ]]; then
      _err "stalwart-cli apply reported non-idempotent failures:"
      printf '  %s\n' "$non_idempotent_errs" >&2
      _err "inspect the plan at $plan_file; re-verify against the upstream schema (ADR-0045 §Schema-pull)"
      _die "Stalwart apply failed"
    fi
    _ok "Stalwart apply: only primaryKeyViolation errors (pre-existing objects) — idempotent success"
  else
    _ok "Stalwart plan applied (SqlDirectory + listeners + Authentication)"
  fi
  fi # end: if (( skip_apply == 0 ))

  # Schema-evolution converger (ADR-0073). The base apply-plan only
  # creates each x:Directory once; once it exists, subsequent template
  # edits to its query fields can't land via apply (no upsert). Update
  # them directly here on every install/update run, regardless of
  # skip_apply, so the live config tracks the template.
  _log "converging Stalwart Directory query fields (ADR-0073)"
  local sql_dir_id
  sql_dir_id="$(STALWART_URL="http://127.0.0.1:${jmap_port}" \
    STALWART_USER="admin" \
    STALWART_PASSWORD="$admin_token" \
    /usr/local/bin/stalwart-cli query Directory --json 2>/dev/null \
    | python3 -c 'import json,sys
# stalwart-cli >=1.0.7 emits NDJSON (one object per line), not a JSON array —
# json.load() would raise "Extra data" and the id never resolved, silently
# skipping query-field convergence. Parse line-by-line.
data = [json.loads(l) for l in sys.stdin if l.strip()]
sql = [d for d in data if d.get("@type") == "Sql"]
print(sql[0]["id"] if sql else "")' 2>/dev/null || true)"
  if [[ -z "$sql_dir_id" ]]; then
    _warn "could not resolve SQL Directory id — skipping query-field convergence"
  else
    # GH #371: `AND m.send_only = 0` excludes send-only mailboxes from the
    # recipient/delivery lookup while queryLogin (auth) still returns them —
    # so a send-only account can submit mail but inbound gets a 550 (no valid
    # recipient) and nothing is ever stored. Same gate shape as is_disabled.
    local query_recipient="SELECT m.email_cached, m.password_hash FROM (SELECT ? AS lookup) input JOIN mailboxes m ON m.is_disabled = 0 AND m.send_only = 0 AND (m.email_cached = input.lookup OR m.id = (SELECT f.mailbox_id FROM email_forwarders f JOIN domains d ON d.id = f.domain_id WHERE f.enabled = 1 AND f.type = 'alias' AND f.mailbox_id IS NOT NULL AND CONCAT(f.local_part, '@', d.name) = input.lookup LIMIT 1))"
    local query_aliases="SELECT CONCAT(f.local_part, '@', d.name) AS alias FROM email_forwarders f JOIN domains d ON d.id = f.domain_id JOIN mailboxes m ON m.id = f.mailbox_id WHERE f.enabled = 1 AND f.type = 'alias' AND m.email_cached = ?"
    local patch_json
    patch_json="$(python3 -c 'import json,sys; print(json.dumps({"queryRecipient": sys.argv[1], "queryEmailAliases": sys.argv[2]}))' "$query_recipient" "$query_aliases")"
    if STALWART_URL="http://127.0.0.1:${jmap_port}" \
      STALWART_USER="admin" \
      STALWART_PASSWORD="$admin_token" \
      /usr/local/bin/stalwart-cli update Directory "$sql_dir_id" --json "$patch_json" >/dev/null 2>&1; then
      _ok "Stalwart Directory query fields converged (id=${sql_dir_id})"
    else
      _warn "Stalwart Directory update failed for id ${sql_dir_id} — aliases may not resolve"
    fi
  fi

  # SpamSettings convergence — same pattern as the Directory query-field
  # convergence above. The base apply-plan's `update x:SpamSettings`
  # entry only lands on a fresh Stalwart instance; on idempotent re-
  # installs (skip_apply=1 because :8446 is already serving) the entire
  # plan is skipped, so newly-pinned spam-filter rules never reach the
  # singleton. Run an explicit update here every install/update run.
  # All fields are mutable per `stalwart-cli describe SpamSettings`, so
  # this is safe to re-issue.
  _log "converging Stalwart SpamSettings (pinned rules URL + score thresholds)"
  local spam_patch
  spam_patch='{"enable":true,"trustContacts":true,"trustReplies":true,"scoreSpam":5.0,"scoreReject":15.0,"scoreDiscard":20.0,"spamFilterRulesUrl":"file:///opt/stalwart/share/spam-filter-rules.json.gz"}'
  if STALWART_URL="http://127.0.0.1:${jmap_port}" \
    STALWART_USER="admin" \
    STALWART_PASSWORD="$admin_token" \
    /usr/local/bin/stalwart-cli update x:SpamSettings --json "$spam_patch" >/dev/null 2>&1; then
    _ok "Stalwart SpamSettings converged (rules pinned to file:///opt/stalwart/share/spam-filter-rules.json.gz)"
  else
    _warn "Stalwart SpamSettings update failed — spam filter will keep current settings (probably default github URL); inspect with 'stalwart-cli get x:SpamSettings --json'"
  fi

  # Delete factory NetworkListeners ([::]:8080, [::]:443) before restart.
  # stalwart-cli apply is create-only; only an explicit API delete removes
  # factory-seeded objects from RocksDB. Must happen while Stalwart is
  # still up so the delete API call succeeds.
  _delete_stalwart_factory_listeners "$jmap_port" "$admin_token"

  # Restart so Stalwart rebinds to plan-defined listeners and drops any
  # factory [::] binds just removed. Required on both paths:
  #   8080 (fresh install) — activate newly-created plan listeners
  #   8446 (jabali-update) — drop stale factory binds
  _log "restarting jabali-stalwart to activate plan listeners and drop factory binds"
  systemctl restart jabali-stalwart.service
  waited=0
  while (( waited < 15 )); do
    local s
    # Same `|| true` rationale as the pre-apply probe loop above:
    # curl exits 7 on "connection refused" while Stalwart re-binds,
    # which would abort the script under `set -euo pipefail`.
    s="$(curl -sS -o /dev/null -w '%{http_code}' --connect-timeout 2 -m 3 \
      http://127.0.0.1:8446/jmap/session 2>/dev/null || true)"
    s="${s:-000}"
    if [[ "$s" =~ ^[234][0-9][0-9]$ ]]; then
      _ok "Stalwart now serving plan-defined listener on :8446 (HTTP $s)"
      return
    fi
    sleep 1
    waited=$((waited + 1))
  done
  _die "Stalwart did not come up on :8446 after restart — check 'journalctl -u jabali-stalwart'"
}

# _install_stalwart_binary is a private helper: download the release
# tarball, verify SHA-256 against install/stalwart.sha256, extract, symlink.
_install_stalwart_binary() {
  local version="$1"
  local arch="x86_64-unknown-linux-gnu"
  local tarball="stalwart-${arch}.tar.gz"
  local tarball_path="/tmp/${tarball}"
  local url="https://github.com/stalwartlabs/stalwart/releases/download/v${version}/${tarball}"
  local sha_file="${REPO_DIR}/install/stalwart.sha256"

  _log "downloading Stalwart $version from GitHub"
  if ! curl -fsSL --connect-timeout 20 --retry 3 --retry-delay 5 --retry-connrefused --speed-limit 1024 --speed-time 30 "$url" -o "$tarball_path"; then
    _die "failed to download Stalwart from $url"
  fi

  if [[ ! -f "$sha_file" ]]; then
    _die "Stalwart SHA-256 checksum file not found at $sha_file"
  fi

  local expected_sha
  expected_sha="$(awk '/^[[:space:]]*#/ || NF==0 { next } { print $1; exit }' "$sha_file")"
  if [[ -z "$expected_sha" ]]; then
    _die "no checksum line found in $sha_file (comments only?)"
  fi
  if [[ "$expected_sha" == "PLACEHOLDER_CAPTURE_ON_FIRST_DEPLOY" ]]; then
    _die "Stalwart SHA-256 placeholder in $sha_file — capture the real checksum on first deploy and bump the file (see file header)"
  fi

  local actual_sha
  actual_sha="$(sha256sum "$tarball_path" | awk '{print $1}')"
  if [[ "$expected_sha" != "$actual_sha" ]]; then
    _die "Stalwart SHA-256 mismatch. Expected: $expected_sha, got: $actual_sha"
  fi

  # Atomic swap: extract to a sibling dir, rename, clean up.
  #
  # --no-same-owner: tar by default preserves uid/gid from the archive
  # (Stalwart's CI packages with uid 1001:1001), so without this flag
  # the binary lands owned by whoever happens to have uid 1001 on the
  # target host — typically the first hosting user. That uid then gets
  # the 256 MB binary charged against its POSIX disk quota, immediately
  # putting them over limit. Force root:root on extraction so the
  # binary always lives outside any hosting user's quota scope.
  local new_dir="/opt/stalwart.new"
  rm -rf "$new_dir"
  install -d -m 0755 -o root -g root "$new_dir"
  tar -xzf "$tarball_path" -C "$new_dir" --strip-components=0 --no-same-owner
  chown -R root:root "$new_dir"
  rm -f "$tarball_path"

  # Stalwart tarball layout: top-level `stalwart` binary. v0.16.0 ships
  # it mode 0644 (no exec bit) — the installer must chmod it +x before
  # use. Defensive find in case upstream nests the binary in a future
  # release.
  local bin_in_tar
  bin_in_tar="$(find "$new_dir" -maxdepth 2 -type f -name stalwart | head -n1)"
  if [[ -z "$bin_in_tar" ]]; then
    rm -rf "$new_dir"
    _die "Stalwart binary not found in tarball at $new_dir"
  fi
  chmod 0755 "$bin_in_tar"

  rm -rf /opt/stalwart.prev
  if [[ -d /opt/stalwart ]]; then
    mv /opt/stalwart /opt/stalwart.prev
  fi
  mv "$new_dir" /opt/stalwart
  # Recompute the path under its final location — $bin_in_tar still
  # points at the old /opt/stalwart.new tree.
  bin_in_tar="$(find /opt/stalwart -maxdepth 2 -type f -name stalwart | head -n1)"
  ln -sfn "$bin_in_tar" /usr/local/bin/stalwart
  rm -rf /opt/stalwart.prev
  _ok "Stalwart $version installed at /opt/stalwart (symlinked to /usr/local/bin/stalwart)"
}

# _install_stalwart_cli downloads + verifies the stalwart-cli release
# tarball (separate repo github.com/stalwartlabs/cli) and drops the binary
# at /usr/local/bin/stalwart-cli. ADR-0045 explains the role: the CLI
# speaks the v0.16 JMAP management API, used by install.sh bootstrap and
# the reconciler. Idempotent against version reported by --version.
_install_stalwart_cli() {
  local cli_version="1.0.12"
  local cli_binary="/usr/local/bin/stalwart-cli"
  local arch="x86_64-unknown-linux-gnu"
  local tarball="stalwart-cli-${arch}.tar.xz"
  local tarball_path="/tmp/${tarball}"
  local url="https://github.com/stalwartlabs/cli/releases/download/v${cli_version}/${tarball}"
  local sha_file="${REPO_DIR}/install/stalwart-cli.sha256"

  # Companion script that uses stalwart-cli to push LE certs into
  # Stalwart's Certificate object — the panel-mail cert (kind=mail) AND
  # every per-domain mail cert (kind=mail-domain, M6.6, via env
  # overrides JABALI_STALWART_CERT_*). Installed BEFORE the cli
  # version-skip gate below: when stalwart-cli is already current the
  # function early-returns, so an end-of-function copy would never
  # refresh this script on `jabali update`. That stranded the stale
  # env-less push-cert on hosts installed before M6.6, so per-domain
  # mail certs (GH #132) pushed under the panel cert name + path
  # instead of their own — Stalwart kept serving the panel cert for
  # every SNI. Hoisted here so the refresh always runs.
  local push_src="${REPO_DIR}/install/stalwart/jabali-stalwart-push-cert.sh"
  local push_dst="/usr/local/bin/jabali-stalwart-push-cert"
  if [[ -f "$push_src" ]]; then
    install -m 0755 -o root -g root "$push_src" "$push_dst"
    _ok "jabali-stalwart-push-cert installed at $push_dst"
  else
    _warn "$push_src missing — Stalwart will keep serving its rcgen self-signed cert"
  fi

  if [[ -x "$cli_binary" ]]; then
    local installed_version
    installed_version="$("$cli_binary" --version 2>&1 | grep -oP 'v?\K[0-9]+\.[0-9]+\.[0-9]+' | head -n1 || echo unknown)"
    if [[ "$installed_version" == "$cli_version" ]]; then
      _ok "stalwart-cli $cli_version already installed"
      return 0
    fi
    _warn "upgrading stalwart-cli $installed_version -> $cli_version"
  fi

  _log "downloading stalwart-cli $cli_version"
  if ! curl -fsSL --connect-timeout 20 --retry 3 --retry-delay 5 --retry-connrefused --speed-limit 1024 --speed-time 30 "$url" -o "$tarball_path"; then
    _die "failed to download stalwart-cli from $url"
  fi

  if [[ ! -f "$sha_file" ]]; then
    _die "stalwart-cli SHA-256 checksum file not found at $sha_file"
  fi
  local expected_sha
  expected_sha="$(awk '/^[[:space:]]*#/ || NF==0 { next } { print $1; exit }' "$sha_file")"
  if [[ -z "$expected_sha" ]]; then
    _die "no checksum line found in $sha_file (comments only?)"
  fi
  if [[ "$expected_sha" == "PLACEHOLDER_CAPTURE_ON_FIRST_DEPLOY" ]]; then
    _die "stalwart-cli SHA-256 placeholder in $sha_file — capture the real checksum on first deploy and bump the file"
  fi
  local actual_sha
  actual_sha="$(sha256sum "$tarball_path" | awk '{print $1}')"
  if [[ "$expected_sha" != "$actual_sha" ]]; then
    _die "stalwart-cli SHA-256 mismatch. Expected: $expected_sha, got: $actual_sha"
  fi

  # .tar.xz — extract to a tmp dir, find the binary, atomic swap.
  local new_dir="/tmp/stalwart-cli.new"
  rm -rf "$new_dir"
  install -d -m 0755 -o root -g root "$new_dir"
  tar -xJf "$tarball_path" -C "$new_dir"
  rm -f "$tarball_path"

  local bin_in_tar
  bin_in_tar="$(find "$new_dir" -maxdepth 3 -type f -name stalwart-cli -perm -u+x | head -n1)"
  if [[ -z "$bin_in_tar" ]]; then
    rm -rf "$new_dir"
    _die "stalwart-cli binary not found in tarball"
  fi

  install -m 0755 -o root -g root "$bin_in_tar" "$cli_binary"
  rm -rf "$new_dir"
  _ok "stalwart-cli $cli_version installed at $cli_binary"

}

# _install_spam_rules vendors the Stalwart spam-filter rules bundle into
# /opt/stalwart/share so apply-plan.json can point at file:// instead of
# the upstream `/releases/latest` URL Stalwart fetches by default. Why:
#   - reproducibility: a known-good SHA on every fresh install
#   - supply-chain: pin shifts via deliberate repo bump, not silent on github
#   - reachability: Stalwart's first-start fetch silently degrades when
#     github is blocked (corp egress, ufw, etc.); local file always loads
#
# Pinned version + SHA live in install/stalwart-spam-filter-rules.sha256.
# Idempotent: skips download when the on-disk file already matches the
# pinned SHA.
#
# The auto-refresh timer (jabali-spam-rules-update.timer) overwrites this
# same file with /releases/latest on a weekly cadence — so the pin is the
# bootstrap floor, not an upper bound.
_install_spam_rules() {
  local sha_file="${REPO_DIR}/install/stalwart-spam-filter-rules.sha256"
  local share_dir="/opt/stalwart/share"
  local dst="${share_dir}/spam-filter-rules.json.gz"
  if [[ ! -f "$sha_file" ]]; then
    _die "Stalwart spam-filter SHA file not found at $sha_file"
  fi

  local version
  version="$(awk -F= '/^VERSION=/ {print $2; exit}' "$sha_file")"
  if [[ -z "$version" ]]; then
    _die "no VERSION= line in $sha_file"
  fi
  local expected_sha
  expected_sha="$(awk '/^[[:space:]]*#/ || /^VERSION=/ || NF==0 { next } { print $1; exit }' "$sha_file")"
  if [[ -z "$expected_sha" ]]; then
    _die "no checksum line found in $sha_file (comments only?)"
  fi

  install -d -m 0755 -o root -g root "$share_dir"

  # Idempotence: if the existing file already matches the pinned SHA, do
  # nothing. Avoids a network hit on every install.sh re-run.
  if [[ -f "$dst" ]]; then
    local current_sha
    current_sha="$(sha256sum "$dst" | awk '{print $1}')"
    if [[ "$current_sha" == "$expected_sha" ]]; then
      _ok "Stalwart spam-filter rules v${version} already installed (sha matches)"
      return 0
    fi
  fi

  local url="https://github.com/stalwartlabs/spam-filter/releases/download/v${version}/spam-filter-rules.json.gz"
  local tmp="/tmp/spam-filter-rules.json.gz.$$"
  _log "downloading Stalwart spam-filter rules v${version}"
  if ! curl -fsSL "$url" -o "$tmp"; then
    rm -f "$tmp"
    _die "failed to download spam-filter rules from $url"
  fi

  local actual_sha
  actual_sha="$(sha256sum "$tmp" | awk '{print $1}')"
  if [[ "$expected_sha" != "$actual_sha" ]]; then
    rm -f "$tmp"
    _die "spam-filter rules SHA-256 mismatch. Expected: $expected_sha, got: $actual_sha"
  fi

  # Validate the gzip envelope before swapping in. A truncated file
  # passes sha256 (in theory it can't, but defense in depth) but Stalwart
  # rejects on parse and disables the filter — silent regression.
  if ! gzip -t "$tmp" 2>/dev/null; then
    rm -f "$tmp"
    _die "spam-filter rules gzip integrity check failed"
  fi

  # 0640 + jabali-mail group: same posture as stalwart-admin.token.
  install -m 0640 -o root -g jabali-mail "$tmp" "$dst"
  rm -f "$tmp"
  _ok "Stalwart spam-filter rules v${version} pinned at $dst"
}

install_bulwark() {
  local bulwark_version="1.8.0"
  local arch="linux-amd64"
  local tarball="bulwark-standalone-${bulwark_version}-${arch}.tar.gz"
  local url="https://github.com/bulwarkmail/webmail/releases/download/${bulwark_version}/${tarball}"
  _log "installing Bulwark webmail (standalone tarball ${bulwark_version})"

  # Serialize the whole function behind an flock (GH #545/#555 concurrent
  # re-entry class): two install.sh invocations overlap on a fresh install --
  # install.sh's own inline call, and the panel agent re-entering via
  # `source install.sh && install_bulwark` to provision the mail module once
  # the panel is up. Both race on the shared /tmp/${tarball} download, the
  # /opt/jabali-webmail.stage extract, and the atomic swap; the losing racer
  # dies with `tar` exit 2 when the winner's cleanup removes the tarball / stage
  # out from under its extract (observed on 10.0.3.14: install-<A>.log died
  # right after "downloading" while install-<B>.log completed the module 3s
  # later). The version/dir idempotency below then turns the second caller into
  # a fast no-op. Best-effort: if flock is unavailable proceed unlocked rather
  # than block the install. Mirrors the install_stalwart guard.
  local _bw_lockfd=""
  if command -v flock >/dev/null 2>&1; then
    exec {_bw_lockfd}>/run/lock/jabali-bulwark-install.lock 2>/dev/null || _bw_lockfd=""
    if [[ -n "$_bw_lockfd" ]]; then
      flock -w 600 "$_bw_lockfd" \
        || _warn "waited 600s for the Bulwark install lock -- proceeding without exclusion"
    fi
  fi

  if ! getent passwd jabali-webmail >/dev/null 2>&1; then
    _log "creating jabali-webmail service user"
    useradd --system --no-create-home --shell /usr/sbin/nologin \
      --user-group jabali-webmail
  fi

  # JAB-351/357: jabali-webmail must NOT belong to the broad "$SERVICE_USER"
  # (jabali) group. That group can read the panel DB password + TLS private keys
  # under /etc/jabali (JAB-351) AND owns the root Agent socket
  # /run/jabali/agent.sock (JAB-357), so an internet-facing webmail compromise
  # would reach panel secrets and host root. Webmail's own secrets (session key,
  # impersonate JWT, bulwark.env) are all jabali-webmail-owned; it needs only
  # jabali-sockets (primary) + jabali-webmail. Fresh installs no longer add the
  # membership; this converges upgraded hosts by dropping the legacy one. The
  # unit's SupplementaryGroups=jabali-webmail is the runtime belt; this removes
  # the /etc/group state so nothing re-leaks it.
  if id -nG jabali-webmail 2>/dev/null | tr ' ' '\n' | grep -qx "$SERVICE_USER"; then
    if gpasswd -d jabali-webmail "$SERVICE_USER" >/dev/null 2>&1; then
      _ok "removed jabali-webmail from the broad $SERVICE_USER group (JAB-351/357)"
      systemctl restart jabali-webmail.service >/dev/null 2>&1 || true
    else
      _warn "could not remove jabali-webmail from $SERVICE_USER — run: gpasswd -d jabali-webmail $SERVICE_USER"
    fi
  fi

  install -d -m 0755 -o jabali-webmail -g jabali-webmail /opt/jabali-webmail
  install -d -m 0750 -o jabali-webmail -g jabali-webmail /var/lib/jabali-webmail
  install -d -m 0750 -o jabali-webmail -g jabali-webmail /var/lib/jabali-webmail/settings
  # jabali-webmail.service lists /opt/jabali-webmail/.next/cache in its
  # ReadWritePaths. systemd refuses to enter mount namespacing when a
  # ReadWritePaths entry doesn't exist yet, so Bulwark fails to start on
  # a fresh install until Next.js first writes to its own cache dir —
  # a chicken-and-egg. Pre-create the dir so systemd is happy. The
  # tarball ships .next/ without the cache subdir.
  install -d -m 0755 -o jabali-webmail -g jabali-webmail /opt/jabali-webmail/.next/cache

  # SESSION_SECRET — generate once, preserve across re-runs (rotating it
  # would invalidate every existing "remember me" cookie).
  local session_key_file="/etc/jabali-panel/bulwark-session.key"
  if [[ ! -f "$session_key_file" ]]; then
    _log "generating Bulwark SESSION_SECRET -> $session_key_file"
    umask 077
    openssl rand -base64 32 >"$session_key_file"
    chmod 0640 "$session_key_file"
    chown jabali-webmail:jabali-webmail "$session_key_file"
  else
    _ok "Bulwark SESSION_SECRET already present"
  fi

  # Idempotence: skip re-download if VERSION file already matches target.
  local version_file="/opt/jabali-webmail/VERSION"
  if [[ -f "$version_file" ]] && [[ "$(cat "$version_file")" == "$bulwark_version" ]]; then
    _ok "Bulwark $bulwark_version already installed"
    _install_bulwark_systemd
    # Config steps are idempotent and must re-apply on every `jabali update`,
    # not only on a Bulwark version bump — otherwise bulwark.env changes and
    # the bundled Libravatar plugin never reach an already-current host.
    _install_bulwark_env
    _install_bulwark_libravatar_plugin
    _install_bulwark_impersonate_secrets
    # Release the concurrency lock on the idempotent fast-path return too.
    [[ -n "$_bw_lockfd" ]] && exec {_bw_lockfd}>&- || true
    return
  fi

  # Pinned SHA of the release tarball (not a git commit — v1.4.14 ships
  # a prebuilt standalone Next.js bundle).
  local sha_file="${REPO_DIR}/install/bulwark.sha256"
  if [[ ! -f "$sha_file" ]]; then
    _die "Bulwark SHA-256 checksum file not found at $sha_file"
  fi
  local expected_sha
  expected_sha="$(awk '/^[[:space:]]*#/ || NF==0 { next } { print $1; exit }' "$sha_file")"
  if [[ -z "$expected_sha" ]]; then
    _die "no checksum line found in $sha_file (comments only?)"
  fi
  if [[ "$expected_sha" == "PLACEHOLDER_CAPTURE_ON_FIRST_DEPLOY" ]]; then
    _die "Bulwark SHA-256 placeholder in $sha_file — capture with: curl -sSL $url | sha256sum, then bump the file"
  fi

  local tarball_path="/tmp/${tarball}"
  _log "downloading $tarball"
  if ! curl -fsSL --connect-timeout 20 --retry 3 --retry-delay 5 --retry-connrefused --speed-limit 1024 --speed-time 30 "$url" -o "$tarball_path"; then
    _die "failed to download Bulwark from $url"
  fi

  local actual_sha
  actual_sha="$(sha256sum "$tarball_path" | awk '{print $1}')"
  if [[ "$expected_sha" != "$actual_sha" ]]; then
    rm -f "$tarball_path"
    _die "Bulwark SHA-256 mismatch. Expected: $expected_sha, got: $actual_sha"
  fi

  # Extract into a sibling directory, then atomic swap. The tarball's
  # top-level dir is `bulwark-standalone/`, so we extract into a staging
  # parent and then move the inner dir into place.
  local stage="/opt/jabali-webmail.stage"
  rm -rf "$stage"
  install -d -m 0755 -o jabali-webmail -g jabali-webmail "$stage"
  tar -xzf "$tarball_path" -C "$stage"
  rm -f "$tarball_path"

  local inner_dir="$stage/bulwark-standalone"
  if [[ ! -d "$inner_dir" ]]; then
    rm -rf "$stage"
    _die "Bulwark tarball did not contain bulwark-standalone/ directory"
  fi
  if [[ ! -f "$inner_dir/server.js" ]]; then
    rm -rf "$stage"
    _die "Bulwark tarball missing server.js entry — layout may have changed in a newer release"
  fi

  echo "$bulwark_version" >"$inner_dir/VERSION"
  chown -R jabali-webmail:jabali-webmail "$inner_dir"

  # Atomic swap.
  rm -rf /opt/jabali-webmail.prev
  if [[ -d /opt/jabali-webmail ]] && [[ "$(ls -A /opt/jabali-webmail 2>/dev/null)" ]]; then
    mv /opt/jabali-webmail /opt/jabali-webmail.prev
  else
    rmdir /opt/jabali-webmail 2>/dev/null || rm -rf /opt/jabali-webmail
  fi
  mv "$inner_dir" /opt/jabali-webmail

  # GH #200: ship jabali's default mail logo into Bulwark's public branding
  # dir so a fresh install is jabali-branded out of the box. The agent's
  # webmail.branding.apply overrides these with the operator's uploaded
  # Server-Settings logo when one is set (and reverts here when cleared).
  install -d -m 0755 /opt/jabali-webmail/public/branding
  if [[ -f "${REPO_DIR}/panel-ui/public/images/jabali_logo.svg" ]]; then
    install -m 0644 "${REPO_DIR}/panel-ui/public/images/jabali_logo.svg" \
      /opt/jabali-webmail/public/branding/jabali-mail-light.svg
  fi
  if [[ -f "${REPO_DIR}/panel-ui/public/images/jabali_logo_dark.svg" ]]; then
    install -m 0644 "${REPO_DIR}/panel-ui/public/images/jabali_logo_dark.svg" \
      /opt/jabali-webmail/public/branding/jabali-mail-dark.svg
  fi
  chown -R jabali-webmail:jabali-webmail /opt/jabali-webmail/public/branding
  rm -rf "$stage" /opt/jabali-webmail.prev

  _ok "Bulwark $bulwark_version installed at /opt/jabali-webmail"

  _install_bulwark_systemd

  # Release the concurrency lock (see the flock at the top of the function).
  # _die paths exit the process, so the kernel drops the fd there; this only
  # covers the normal fall-through so a later legitimate caller can proceed.
  [[ -n "$_bw_lockfd" ]] && exec {_bw_lockfd}>&- || true
}

# _install_bulwark_systemd installs the unit file. Env file is rendered
# separately by _install_bulwark_env; the nginx per-domain vhost is
# written by the panel-agent's webmail.vhost_apply command, driven by
# the reconciler once a domain flips email_enabled=1.
_install_bulwark_systemd() {
  if [[ ! -f "${REPO_DIR}/install/systemd/jabali-webmail.service" ]]; then
    _die "Bulwark systemd unit not found at ${REPO_DIR}/install/systemd/jabali-webmail.service"
  fi
  install -m 0644 -o root -g root "${REPO_DIR}/install/systemd/jabali-webmail.service" \
    /etc/systemd/system/jabali-webmail.service

  # Re-create .next/cache after the atomic swap that ran in install_bulwark
  # (mv of inner_dir into /opt/jabali-webmail wipes the cache subdir we
  # created up front; the tarball doesn't ship one). Without this, the
  # unit crash-loops with status=226/NAMESPACE on first start because
  # systemd refuses to enter mount namespacing when a ReadWritePaths
  # entry doesn't exist on disk.
  install -d -m 0755 -o jabali-webmail -g jabali-webmail \
    /opt/jabali-webmail/.next/cache

  # M25 Step 5: deploy the unix-socket wrapper alongside Bulwark's stock
  # server.js. The systemd unit runs node /opt/jabali-webmail/server-unix.js
  # which loads Next.js's request handler and binds SOCKET_PATH instead of
  # TCP HOSTNAME:PORT. Re-deploy unconditionally so future bulwark-update
  # runs that re-extract a tarball over /opt/jabali-webmail (which would
  # remove our wrapper) restore it on the next install.sh.
  local wrapper_src="${REPO_DIR}/install/jabali-webmail/server-unix.js"
  if [[ ! -f "$wrapper_src" ]]; then
    _die "Bulwark unix wrapper not found at $wrapper_src"
  fi
  install -m 0644 -o jabali-webmail -g jabali-webmail "$wrapper_src" \
    /opt/jabali-webmail/server-unix.js

  # M25 Step 5: drop the http{}-level upstream declaration into
  # /etc/nginx/conf.d/. The per-domain mail vhosts reference it by name
  # via proxy_pass http://jabali_bulwark/;. Conf.d is loaded by Debian's
  # default nginx.conf at the http{} scope — which is where named
  # upstreams must live.
  local upstream_src="${REPO_DIR}/install/nginx/jabali-bulwark-upstream.conf"
  if [[ ! -f "$upstream_src" ]]; then
    _die "Bulwark upstream snippet not found at $upstream_src"
  fi
  install -m 0644 -o root -g root "$upstream_src" \
    /etc/nginx/conf.d/jabali-bulwark-upstream.conf
  if nginx -t >/dev/null 2>&1; then
    systemctl reload nginx 2>/dev/null || true
    _ok "Bulwark upstream wired into nginx (jabali_bulwark)"
  else
    _warn "nginx -t failed after dropping jabali-bulwark-upstream.conf — leaving in place but not reloading"
  fi

  systemctl daemon-reload
  _ok "jabali-webmail.service installed (disabled — starts on first domain.email_enable)"
  _install_bulwark_env
  _install_bulwark_libravatar_plugin
  _install_bulwark_impersonate_secrets
}

# _install_bulwark_libravatar_plugin ships the first-party Libravatar avatar
# plugin into the webmail's PLUGIN_DEV_DIR and pre-approves it. Bulwark 1.7.x
# gates dev-folder plugin bundles behind host Ed25519 signing + an admin
# approval before they load; the signing key persists under ADMIN_CONFIG_DIR
# (relocated onto the writable state volume since ProtectSystem=strict makes
# /opt/jabali-webmail/data read-only), and the approval is pre-seeded here so
# the plugin loads without a manual admin click. Idempotent; the approval is
# re-stamped with the current bundle's sha256 every run so a plugin update
# can't leave a stale (unapproved) hash. Best-effort: a failure here must not
# break the webmail install (avatars are cosmetic).
_install_bulwark_libravatar_plugin() {
  local src_dir="${REPO_DIR}/install/jabali-webmail/plugins/libravatar"
  local dev_dir="/opt/jabali-webmail/dev-plugins"
  local plugin_dir="${dev_dir}/libravatar"
  local cfg_dir="/var/lib/jabali-webmail/admin-config"
  if [[ ! -f "${src_dir}/index.js" || ! -f "${src_dir}/manifest.json" ]]; then
    _warn "Libravatar plugin source missing at ${src_dir} — skipping webmail avatar plugin"
    return 0
  fi
  install -d -m 0755 -o jabali-webmail -g jabali-webmail "$dev_dir" "$plugin_dir" "${plugin_dir}/media"
  install -d -m 0700 -o jabali-webmail -g jabali-webmail "$cfg_dir"
  install -m 0644 -o jabali-webmail -g jabali-webmail "${src_dir}/manifest.json" "${plugin_dir}/manifest.json"
  install -m 0644 -o jabali-webmail -g jabali-webmail "${src_dir}/index.js"     "${plugin_dir}/index.js"
  [[ -f "${src_dir}/media/icon.svg" ]]   && install -m 0644 -o jabali-webmail -g jabali-webmail "${src_dir}/media/icon.svg"   "${plugin_dir}/media/icon.svg"
  [[ -f "${src_dir}/media/banner.svg" ]] && install -m 0644 -o jabali-webmail -g jabali-webmail "${src_dir}/media/banner.svg" "${plugin_dir}/media/banner.svg"

  local hash now approvals
  hash=$(sha256sum "${plugin_dir}/index.js" | awk '{print $1}')
  now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  approvals="${cfg_dir}/plugin-approvals.json"
  if ! python3 - "$approvals" "$hash" "$now" <<'PYEOF'
import json, os, sys
path, h, now = sys.argv[1], sys.argv[2], sys.argv[3]
data = {"entries": []}
if os.path.exists(path):
    try:
        with open(path) as f:
            data = json.load(f)
        if not isinstance(data, dict) or not isinstance(data.get("entries"), list):
            data = {"entries": []}
    except Exception:
        data = {"entries": []}
data["entries"] = [e for e in data["entries"]
                   if not (isinstance(e, dict) and e.get("pluginId") == "libravatar")]
data["entries"].append({
    "pluginId": "libravatar", "bundleHash": h, "status": "approved",
    "manifest": {"name": "Libravatar Avatars", "version": "1.0.0",
                 "author": "Bulwark Mail Community", "permissions": ["email:read"]},
    "requestedBy": "jabali-install", "requestedAt": now,
    "decidedBy": "jabali-install", "decidedAt": now,
})
with open(path + ".tmp", "w") as f:
    json.dump(data, f, indent=2)
os.replace(path + ".tmp", path)
PYEOF
  then
    _warn "failed to pre-approve Libravatar plugin (avatars will need a manual approve in Admin -> Plugins)"
    return 0
  fi
  chown jabali-webmail:jabali-webmail "$approvals"
  chmod 0600 "$approvals"
  _ok "Libravatar webmail avatar plugin installed + pre-approved (sha256 ${hash:0:12})"
}

# _install_bulwark_env renders install/bulwark/bulwark.env.tmpl into
# /etc/jabali-panel/bulwark.env. Idempotent: writes only when the
# rendered content's SHA-256 differs from the on-disk file. Template
# variable is $JABALI_HOSTNAME (captured by the install.sh preamble).
# Invoked unconditionally from _install_bulwark_systemd so that even
# on a second run that skips the tarball re-download, the env file
# is kept in sync with the template (the one Bulwark actually reads
# at every service start).
_install_bulwark_env() {
  local src="${REPO_DIR}/install/bulwark/bulwark.env.tmpl"
  local dst="/etc/jabali-panel/bulwark.env"
  if [[ ! -f "$src" ]]; then
    _die "Bulwark env template not found at $src"
  fi

  # Resolve hostname: fresh install → JABALI_SRV_HOSTNAME; re-run →
  # parse config.toml; last resort → hostname -f. Mirrors the pattern
  # used by install_kratos so this works on jabali-update too.
  local _bwrk_host="${JABALI_SRV_HOSTNAME:-}"
  if [[ -z "$_bwrk_host" && -f /etc/jabali-panel/config.toml ]]; then
    _bwrk_host="$(awk -F'[= "]+' '/^[[:space:]]*hostname[[:space:]]*=/{print $2; exit}' \
      /etc/jabali-panel/config.toml)"
  fi
  if [[ -z "$_bwrk_host" ]]; then
    _bwrk_host="$(hostname -f 2>/dev/null || hostname 2>/dev/null || true)"
  fi
  if [[ -z "$_bwrk_host" ]]; then
    _die "cannot resolve panel hostname for Bulwark env — pass --hostname or ensure config.toml has 'hostname'"
  fi

  # Render into a tmpfile first so we can diff by hash before writing.
  # Using envsubst would pull in gettext as a dep; sed is enough for
  # the two variables this template uses.
  local tmp
  tmp=$(mktemp)
  # shellcheck disable=SC2016
  sed "s|\${JABALI_SERVER_HOSTNAME}|${_bwrk_host}|g" "$src" >"$tmp"

  local new_sha old_sha=""
  new_sha=$(sha256sum "$tmp" | awk '{print $1}')
  if [[ -f "$dst" ]]; then
    old_sha=$(sha256sum "$dst" | awk '{print $1}')
  fi
  if [[ "$new_sha" == "$old_sha" ]]; then
    rm -f "$tmp"
    _ok "Bulwark env ($dst) already up to date"
    return
  fi

  install -m 0640 -o jabali-webmail -g jabali-webmail "$tmp" "$dst"
  rm -f "$tmp"
  _ok "Bulwark env rendered -> $dst"

  # Soft reload: if the service is already running, restart so the new
  # env takes effect. If it's inactive, the next reconciler-triggered
  # start will pick up the file.
  if systemctl is-active jabali-webmail >/dev/null 2>&1; then
    systemctl restart jabali-webmail || _warn "failed to restart jabali-webmail after env update"
  fi
}

# _install_bulwark_impersonate_secrets generates + persists the
# BULWARK_JWT_AUTH_SECRET + BULWARK_STALWART_MASTER_PASSWORD that drive
# the webmail SSO flow (M6.6 — GET /api/auth/impersonate). Both secrets
# live in /etc/jabali-panel/ with 0640 root:jabali-webmail. They are
# appended to bulwark.env (diff-aware) and the service restarted only
# when the env contents actually change.
#
# Idempotent: re-runs of install.sh / jabali update keep the same
# secrets unless the operator deletes the files. Delete + re-run = rotation.
_install_bulwark_impersonate_secrets() {
  local jwt_secret_file=/etc/jabali-panel/bulwark-jwt-auth.secret
  local master_pw_file=/etc/jabali-panel/bulwark-stalwart-master.password
  local bulwark_env=/etc/jabali-panel/bulwark.env

  if [[ ! -f "$bulwark_env" ]]; then
    _die "bulwark.env missing — _install_bulwark_env must run first"
  fi

  # Resolve hostname (same logic as _install_bulwark_env above).
  local _bwrk_host="${JABALI_SRV_HOSTNAME:-}"
  if [[ -z "$_bwrk_host" && -f /etc/jabali-panel/config.toml ]]; then
    _bwrk_host="$(awk -F'[= "]+' '/^[[:space:]]*hostname[[:space:]]*=/{print $2; exit}'       /etc/jabali-panel/config.toml)"
  fi
  if [[ -z "$_bwrk_host" ]]; then
    _bwrk_host="$(hostname -f 2>/dev/null || hostname 2>/dev/null || true)"
  fi
  if [[ -z "$_bwrk_host" ]]; then
    _die "cannot resolve panel hostname for Bulwark SSO secrets"
  fi

  # Generate JWT secret if absent. 64 chars base64url-safe (well above
  # Bulwark's MIN_SECRET_LENGTH=32 from lib/impersonation/jwt.ts).
  if [[ ! -s "$jwt_secret_file" ]]; then
    # `tr -dc 'A-Za-z0-9'` (delete-COMPLEMENT) strips '/' '+' '=' AND the
    # newline that `openssl rand -base64` inserts when it wraps output at
    # 64 columns. The old `tr -d '/+='` left that '\n' in, `head -c 64`
    # kept it, and it split BULWARK_JWT_AUTH_SECRET across two lines in
    # bulwark.env -> Bulwark read a truncated key -> "Invalid signature"
    # (GH #193). base64 96 bytes => 128 chars, always >= 64 after stripping.
    # GH #701: create under umask 077 so there is no world/group-readable window
    # between creation and the chmod below (0600 -> 0640 never exposes to others).
    ( umask 077; openssl rand -base64 96 | tr -dc 'A-Za-z0-9' | head -c 64 > "$jwt_secret_file" )
    chmod 0640 "$jwt_secret_file"
    chown root:jabali-webmail "$jwt_secret_file"
    _ok "generated BULWARK_JWT_AUTH_SECRET -> $jwt_secret_file"
  fi

  # Heal secrets generated by the pre-fix gen line that captured
  # openssl's line-wrap newline (GH #193). Any byte outside [A-Za-z0-9]
  # (newline, stray whitespace) gets stripped; if that changes the file
  # we rewrite it so panel-api and Bulwark agree on the key. The env
  # resync + restart below then propagates the cleaned value.
  local _raw_secret _clean_secret
  _raw_secret="$(cat "$jwt_secret_file")"
  _clean_secret="$(tr -dc 'A-Za-z0-9' < "$jwt_secret_file")"
  if [[ "$_clean_secret" != "$_raw_secret" ]]; then
    printf '%s' "$_clean_secret" > "$jwt_secret_file"
    chmod 0640 "$jwt_secret_file"
    chown root:jabali-webmail "$jwt_secret_file"
    _warn "sanitized embedded newline in bulwark-jwt-auth.secret (GH #193)"
  fi

  # Stalwart master user: we re-use the existing Stalwart admin account
  # that install_stalwart already provisioned (token at
  # /etc/jabali-panel/stalwart-admin.token). Stalwart impersonation
  # supports any account with Admin role — verified on .14 2026-06-01:
  #   curl -H "Authorization: Basic $(b64 'test@dom.com%admin:<token>')" \
  #     http://127.0.0.1:8446/.well-known/jmap  →  200 + target's JMAP session
  # No need for a dedicated master account; one less moving piece.
  local stalwart_token_file=/etc/jabali-panel/stalwart-admin.token
  if [[ ! -s "$stalwart_token_file" ]]; then
    _die "Stalwart admin token missing at $stalwart_token_file — install_stalwart must run first"
  fi

  local jwt_secret master_pw session_secret
  jwt_secret=$(cat "$jwt_secret_file")
  master_pw=$(cat "$stalwart_token_file")

  # GH #1354: Bulwark's newer build reads SESSION_SECRET from the environment
  # directly (it no longer resolves SESSION_SECRET_FILE on the session /
  # impersonate path), so a webmail version bump broke "Login via Webmail"
  # (GET /api/auth/impersonate → HTTP 500 "SESSION_SECRET not configured")
  # even though bulwark-session.key was present and referenced. Publish the
  # value into bulwark.env alongside the file ref so both old and new builds
  # resolve it. The key is generated once (install_bulwark) and preserved.
  local session_key_file=/etc/jabali-panel/bulwark-session.key
  if [[ ! -s "$session_key_file" ]]; then
    _die "Bulwark session key missing at $session_key_file — install_bulwark must run first"
  fi
  session_secret=$(cat "$session_key_file")

  # Rebuild bulwark.env in tmp: strip any prior M6.6 lines, append fresh.
  # Diff via sha256; restart only on real change to avoid 60s reconciler
  # spam when nothing changed (memory: feedback_per_tick_idempotent_loops).
  local tmp
  tmp=$(mktemp)
  grep -v -E '^(BULWARK_JWT_AUTH_SECRET|BULWARK_STALWART_MASTER_USER|BULWARK_STALWART_MASTER_PASSWORD|BULWARK_JWT_AUTH_ISSUER|SESSION_SECRET)=' "$bulwark_env" > "$tmp" || true
  # Trim trailing blank lines off the PRESERVED portion before appending. The
  # heredoc below opens with a blank separator and `grep -v` strips only the
  # managed KEYS, never blanks -- so every run left one more blank line sitting
  # between the operator's own keys and our block. The sha therefore never
  # matched, the "already in sync" fast path below never fired, and bulwark.env
  # was rewritten (and grew a byte) on every install.sh / jabali update.
  # $(< file) drops trailing newlines; printf puts exactly one back. Guarded so
  # an empty preserved portion stays empty rather than becoming a blank line.
  if [[ -s "$tmp" ]]; then
    printf '%s\n' "$(< "$tmp")" > "${tmp}.norm" && mv "${tmp}.norm" "$tmp"
  fi
  cat >> "$tmp" <<EOF

# M6.6 — webmail SSO via GET /api/auth/impersonate (Bulwark 1.7.1+).
# Secrets backed by /etc/jabali-panel/bulwark-jwt-auth.secret +
# /etc/jabali-panel/stalwart-admin.token (read directly — same admin
# account install_stalwart provisioned). Delete bulwark-jwt-auth.secret
# + re-run install.sh to rotate the JWT signing key; Stalwart admin
# rotation is the existing 'jabali admin rotate-stalwart-token' path.
BULWARK_JWT_AUTH_SECRET=${jwt_secret}
BULWARK_STALWART_MASTER_USER=admin
BULWARK_STALWART_MASTER_PASSWORD=${master_pw}
BULWARK_JWT_AUTH_ISSUER=jabali-panel/webmail-sso
# GH #1354: value form of the session secret (kept in lockstep with
# SESSION_SECRET_FILE=/etc/jabali-panel/bulwark-session.key from the template).
SESSION_SECRET=${session_secret}
EOF

  # Login-form flags (Bulwark >=1.7.8, bulwarkmail/webmail#520). Unlike the
  # secrets above these are DEFAULTS, not managed values: seeded only when
  # absent, so an operator who deliberately sets either one keeps their choice
  # across install.sh and every jabali update.
  #
  # LOGIN_SHOW_TOTP — GH #316. Jabali runs Stalwart against an EXTERNAL SQL
  # directory (accounts live in the panel DB, read-only), and Stalwart only
  # enforces MFA for INTERNAL-directory accounts:
  # crates/common/src/auth/authentication.rs calls verify_mfa_secret_hash() on
  # that path alone. Our mailboxes therefore have no per-account TOTP and the
  # manual toggle can never succeed -- it offers a 2FA flow that silently does
  # nothing, which is worse than offering none. Server-REQUIRED TOTP is
  # unaffected: the field still auto-shows when the server returns
  # totp_required. Revisit if Stalwart gains external-directory MFA:
  # https://support.stalw.art/t/enforce-totp-2fa-for-external-directory-sql-ldap-accounts/1003
  #
  # LOGIN_SHOW_VERSION — the footer prints the exact Bulwark build version to
  # UNAUTHENTICATED visitors, handing a scanner the version to match CVEs
  # against.
  if ! grep -q '^LOGIN_SHOW_TOTP=' "$tmp"; then
    printf 'LOGIN_SHOW_TOTP=false\n' >> "$tmp"
  fi
  if ! grep -q '^LOGIN_SHOW_VERSION=' "$tmp"; then
    printf 'LOGIN_SHOW_VERSION=false\n' >> "$tmp"
  fi

  local new_sha old_sha
  new_sha=$(sha256sum "$tmp" | awk '{print $1}')
  old_sha=$(sha256sum "$bulwark_env" | awk '{print $1}')
  if [[ "$new_sha" == "$old_sha" ]]; then
    rm -f "$tmp"
    _ok "Bulwark impersonation env already in sync"
    return
  fi

  install -m 0640 -o jabali-webmail -g jabali-webmail "$tmp" "$bulwark_env"
  rm -f "$tmp"
  _ok "Bulwark impersonation env appended -> $bulwark_env"

  # panel-api (user jabali) needs to read bulwark-jwt-auth.secret which
  # is 0640 root:jabali-webmail. Add jabali as a supplementary group
  # member; idempotent (usermod -aG is a no-op when already present).
  # Supplementary-group changes don't apply to running processes — we
  # restart jabali-panel so its egid set picks up jabali-webmail.
  if id jabali >/dev/null 2>&1 && getent group jabali-webmail >/dev/null; then
    if ! id -nG jabali | tr " " "\n" | grep -qx jabali-webmail; then
      usermod -aG jabali-webmail jabali       || _warn "usermod -aG jabali-webmail jabali failed"
      _ok "added jabali user to jabali-webmail group (for impersonate secret read)"
      if systemctl is-active jabali-panel >/dev/null 2>&1; then
        systemctl restart jabali-panel         || _warn "failed to restart jabali-panel after group membership update"
      fi
    fi
  fi

  if systemctl is-active jabali-webmail >/dev/null 2>&1; then
    systemctl restart jabali-webmail       || _warn "failed to restart jabali-webmail after impersonation env update"
  fi
}

# ---------- step 8: Kratos identity provider (M20) ---------------------------

install_kratos() {
  # M25.1 DSN went unix-socket; Kratos needs mysql group on Ubuntu
  # where the socket is 0660 mysql:mysql. Do this BEFORE the unit may
  # start so the first ExecStart already has the supplementary group.
  ensure_jabali_in_mysql_group

  # Kratos binary: vendored SHA-256 verification pattern matching wp-cli + phpmyadmin.
  local kratos_version="26.2.0"
  _log "installing Ory Kratos identity provider (v${kratos_version})"

  local kratos_binary="/usr/local/bin/kratos"
  local kratos_tar="/tmp/kratos_${kratos_version}-linux_64bit.tar.gz"
  local kratos_sha_file="${REPO_DIR}/install/kratos.sha256"
  local kratos_url="https://github.com/ory/kratos/releases/download/v${kratos_version}/kratos_${kratos_version}-linux_64bit.tar.gz"

  # Check if already installed at correct version.
  if [[ -f "$kratos_binary" ]]; then
    local installed_version
    installed_version=$("$kratos_binary" version 2>&1 | grep -oP 'Version:\s+\K[^[:space:]]+' || echo "unknown")
    if [[ "$installed_version" == "v${kratos_version}" ]]; then
      _ok "Kratos $kratos_version already installed (binary check) — re-rendering config"
      _kratos_skip_binary=1
    fi
  fi

  # Download + verify SHA-256 + install binary. Skipped when binary is
  # already at target version — the config-render block further down
  # always runs so kratos.yml.tmpl edits reach existing hosts on
  # `jabali update` (the "sync kratos config" step in update.go relies
  # on this fall-through).
  if [[ "${_kratos_skip_binary:-0}" != "1" ]]; then
    _log "downloading Kratos $kratos_version from GitHub"
    if ! curl -fsSL "$kratos_url" -o "$kratos_tar"; then
      _die "failed to download Kratos from $kratos_url"
    fi

    if [[ ! -f "$kratos_sha_file" ]]; then
      _die "Kratos SHA-256 checksum file not found at $kratos_sha_file"
    fi

    # Skip comment + blank lines so the checksum file can carry provenance
    # metadata (`# Source: ...`, `# Verified: YYYY-MM-DD`) without tripping
    # the comparison — matches the sha256sum(1) convention.
    local expected_sha
    expected_sha="$(awk '/^[[:space:]]*#/ || NF==0 { next } { print $1; exit }' "$kratos_sha_file")"
    local actual_sha
    actual_sha="$(sha256sum "$kratos_tar" | awk '{print $1}')"
    if [[ -z "$expected_sha" ]]; then
      _die "no checksum line found in $kratos_sha_file (comments only?)"
    fi
    if [[ "$expected_sha" != "$actual_sha" ]]; then
      _die "Kratos SHA-256 mismatch. Expected: $expected_sha, got: $actual_sha"
    fi

    # Extract + install binary.
    tar -xzf "$kratos_tar" -C /tmp/
    install -m 0755 -o root -g root /tmp/kratos "$kratos_binary"
    rm -f "$kratos_tar" /tmp/kratos

    _ok "Kratos binary installed at $kratos_binary"
  fi

  # Provision MariaDB database + user for Kratos.
  local kratos_db_name="jabali_kratos"
  local kratos_db_user="jabali_kratos"
  local kratos_pw_file="/etc/jabali-panel/kratos-db-password"

  if [[ ! -f "$kratos_pw_file" ]]; then
    _log "generating Kratos DB password → $kratos_pw_file"
    umask 077
    openssl rand -hex 32 >"$kratos_pw_file"
    chmod 0600 "$kratos_pw_file"
    chown root:root "$kratos_pw_file"
  fi

  local kratos_db_pass
  kratos_db_pass="$(cat "$kratos_pw_file")"

  # Create database + user. Idempotent: CREATE IF NOT EXISTS.
  mariadb -e "
    CREATE DATABASE IF NOT EXISTS \`${kratos_db_name}\`
      CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
    CREATE USER IF NOT EXISTS '${kratos_db_user}'@'localhost' IDENTIFIED BY '${kratos_db_pass}';
    ALTER USER '${kratos_db_user}'@'localhost' IDENTIFIED BY '${kratos_db_pass}';
    GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, DROP, INDEX, ALTER,
          REFERENCES, LOCK TABLES, CREATE TEMPORARY TABLES
      ON \`${kratos_db_name}\`.* TO '${kratos_db_user}'@'localhost';
    FLUSH PRIVILEGES;
  "

  _ok "Kratos database provisioned: DB=${kratos_db_name}, user=${kratos_db_user}"

  # Render kratos.yml config from template + write credentials.
  local kratos_config="/etc/jabali-panel/kratos.yml"
  local kratos_secrets_dir="/etc/jabali-panel/kratos-secrets"

  # Persisted secrets directory. Each file = one long-lived secret. We
  # generate once and reuse on re-runs — rotating these on every install
  # would invalidate existing sessions + encrypted cookies and surprise
  # operators. Rotation belongs in the runbook, not here.
  install -d -m 0700 -o root -g root "$kratos_secrets_dir"

  _kratos_ensure_secret() {
    local path="$1"
    if [[ ! -f "$path" ]]; then
      umask 077
      openssl rand -hex 32 > "$path"
      chmod 0600 "$path"
      chown root:root "$path"
    fi
  }
  _kratos_ensure_secret "$kratos_secrets_dir/default"
  _kratos_ensure_secret "$kratos_secrets_dir/cookie"

  local kratos_secret_default kratos_secret_cookie
  kratos_secret_default="$(cat "$kratos_secrets_dir/default")"
  kratos_secret_cookie="$(cat "$kratos_secrets_dir/cookie")"

  # Render kratos.yml from install/kratos.yml.tmpl. The template uses
  # Go-style {{.Var}} mustaches (matches docs/ADR-0034 style). We substitute
  # via sed rather than envsubst because (a) envsubst's ${VAR} syntax doesn't
  # match the template and (b) gettext-base isn't in Debian's minimal set,
  # so depending on it adds an apt dep without saving any code.
  if [[ ! -f "${REPO_DIR}/install/kratos.yml.tmpl" ]]; then
    _die "Kratos template not found at ${REPO_DIR}/install/kratos.yml.tmpl"
  fi

  # Resolve the panel config for hostname + public port. The authoritative
  # config the panel reads is /etc/jabali/config.toml (main.go defaultConfigPath);
  # keep the legacy /etc/jabali-panel/config.toml path as a fallback.
  local panel_cfg=""
  if [[ -f /etc/jabali/config.toml ]]; then
    panel_cfg="/etc/jabali/config.toml"
  elif [[ -f /etc/jabali-panel/config.toml ]]; then
    panel_cfg="/etc/jabali-panel/config.toml"
  fi

  local panel_hostname="${JABALI_SRV_HOSTNAME:-}"
  if [[ -z "$panel_hostname" && -n "$panel_cfg" ]]; then
    panel_hostname="$(awk -F'[= "]+' '/^[[:space:]]*hostname[[:space:]]*=/{print $2; exit}' "$panel_cfg")"
  fi
  if [[ -z "$panel_hostname" ]]; then
    panel_hostname="$(hostname -f 2>/dev/null || hostname 2>/dev/null || echo 'localhost')"
  fi

  # GH #429: public port. When the panel is fronted by a reverse proxy on 443
  # (e.g. a Cloudflare tunnel), Kratos must emit PORT-LESS URLs so the browser's
  # flow/return/redirect URLs resolve on :443, not the origin :8443. Controlled
  # by [server] public_port in config.toml; absent keeps the default :8443.
  local panel_port_suffix=":8443"
  if [[ -n "$panel_cfg" ]]; then
    local pub_port
    pub_port="$(awk -F'[= "]+' '/^[[:space:]]*public_port[[:space:]]*=/{print $2; exit}' "$panel_cfg")"
    if [[ "$pub_port" == "443" ]]; then
      panel_port_suffix=""
    elif [[ "$pub_port" =~ ^[0-9]+$ ]]; then
      panel_port_suffix=":$pub_port"
    fi
  fi

  # None of these values contain `|`, so we use it as the sed delimiter
  # to avoid escaping `/` in URLs. All inputs are either generated by us
  # (hex passwords, fixed db-user/db-name) or validated DNS names — no
  # shell-metacharacter exposure.
  sed \
    -e "s|{{\.KratosDatabaseUser}}|${kratos_db_user}|g" \
    -e "s|{{\.KratosDatabasePassword}}|${kratos_db_pass}|g" \
    -e "s|{{\.KratosDatabaseName}}|${kratos_db_name}|g" \
    -e "s|{{\.PanelHostname}}|${panel_hostname}|g" \
    -e "s|{{\.PanelPortSuffix}}|${panel_port_suffix}|g" \
    -e "s|{{\.KratosSecretDefault}}|${kratos_secret_default}|g" \
    -e "s|{{\.KratosCookieSecret}}|${kratos_secret_cookie}|g" \
    "${REPO_DIR}/install/kratos.yml.tmpl" > "$kratos_config"
  chmod 0640 "$kratos_config"
  chown root:"$SERVICE_USER" "$kratos_config"

  # Fail loud if any mustache slipped through (template drift — a new
  # placeholder was added without a matching sed line).
  if grep -q '{{\..*}}' "$kratos_config"; then
    _die "unsubstituted mustaches left in $kratos_config — template drift?"
  fi

  _ok "Kratos config written to $kratos_config"

  # Copy identity schema file.
  if [[ ! -f "${REPO_DIR}/install/kratos-identity-schema.json" ]]; then
    _die "Kratos identity schema not found at ${REPO_DIR}/install/kratos-identity-schema.json"
  fi
  install -m 0644 -o root -g root "${REPO_DIR}/install/kratos-identity-schema.json" \
    /etc/jabali-panel/kratos-identity-schema.json

  _ok "Kratos identity schema installed"

  # Run database migrations.
  _log "running Kratos database migrations"
  # Kratos emits ~2 JSON-log lines per migration (one per file, bidirectional).
  # On a fresh install that's hundreds of lines — silence the chatter and
  # surface the full log only on failure.
  #
  # aa-exec -p unconfined: on Ubuntu 24.04 / AppArmor 4.0, flags=(complain)
  # still enforces unix-socket connect restrictions (EACCES on
  # /var/run/mysqld/mysqld.sock even in an empty complain-mode profile).
  # The migration is an admin one-shot, not the daemon, so run it unconfined.
  # If AppArmor or aa-exec is absent the plain invocation is used as fallback.
  local _kratos_migrate_cmd=("$kratos_binary" migrate sql -e -c "$kratos_config" --yes)
  if [[ -d /sys/kernel/security/apparmor ]] && command -v aa-exec >/dev/null 2>&1; then
    _kratos_migrate_cmd=(aa-exec -p unconfined -- "${_kratos_migrate_cmd[@]}")
  fi
  local kratos_migrate_log="/tmp/jabali-kratos-migrate.$$.log"
  if ! "${_kratos_migrate_cmd[@]}" >"$kratos_migrate_log" 2>&1; then
    _err "Kratos database migrations failed — full output:"
    cat "$kratos_migrate_log" >&2
    rm -f "$kratos_migrate_log"
    _die "Kratos database migrations failed"
  fi
  local migrate_count
  migrate_count="$(grep -c 'applied successfully' "$kratos_migrate_log" 2>/dev/null || echo 0)"
  rm -f "$kratos_migrate_log"
  _ok "Kratos database migrations completed (${migrate_count} applied)"

  _ok "Kratos migrations completed"

  # Install systemd unit file. Each step is a _log line so when this
  # function silently exits (set -e), the operator can tell which one
  # was the last to fire — the alternative (no progress output) produced
  # the bug reported by the first-install operator: script dies with
  # zero diagnostic between "migrations completed" and the shell prompt.
  _log "installing jabali-kratos systemd unit"
  if [[ ! -f "${REPO_DIR}/install/systemd/jabali-kratos.service" ]]; then
    _die "Kratos systemd unit template not found at ${REPO_DIR}/install/systemd/jabali-kratos.service"
  fi
  install -m 0644 -o root -g root "${REPO_DIR}/install/systemd/jabali-kratos.service" \
    /etc/systemd/system/jabali-kratos.service

  _log "reloading systemd daemon"
  systemctl daemon-reload

  _log "enabling jabali-kratos.service"
  systemctl enable --quiet jabali-kratos

  _log "restarting jabali-kratos.service"
  # --quiet silences success output, but systemctl restart still returns
  # non-zero if the unit fails to start (e.g. Kratos crashes on config
  # parse). set -e would kill us silently. Capture + surface the failure
  # with the last 20 log lines so the operator gets context instead of
  # a bare shell prompt.
  if ! systemctl restart --quiet jabali-kratos; then
    _warn "jabali-kratos failed to start; dumping last 20 journal lines"
    journalctl -u jabali-kratos -n 20 --no-pager || true
    _die "jabali-kratos did not start — fix /etc/jabali-panel/kratos.yml and re-run install.sh"
  fi

  # Poll for readiness. M25 Step 3: Kratos's public endpoint is now a Unix
  # socket; curl --unix-socket talks to it via /admin/health/ready (any
  # path works; we want a 2xx). Same set-e-safe arithmetic as before.
  _log "waiting for Kratos to be ready (max 30s)"
  local waited=0
  while [[ $waited -lt 30 ]]; do
    if curl -sf --unix-socket /run/jabali-kratos/public.sock http://kratos/health/ready >/dev/null 2>&1; then
      _ok "Kratos is ready"
      break
    fi
    sleep 1
    waited=$((waited + 1))
  done

  if [[ $waited -eq 30 ]]; then
    _warn "Kratos did not become ready within 30s. Check: systemctl status jabali-kratos"
  fi

  # M25 Step 2+3 verification: both endpoints must be Unix sockets at
  # /run/jabali-kratos/{admin,public}.sock with mode 0660 jabali:jabali-sockets,
  # AND the legacy TCP listeners on 4433 + 4434 must be gone. The
  # verify_socket_perms + verify_no_all_interface_binds helpers were sourced
  # from install/scripts/socket-helpers.sh at the top of main(). If any
  # check fails the installer aborts loudly so the operator doesn't
  # discover a 502 from panel-api → Kratos in production.
  if ! verify_socket_perms /run/jabali-kratos/admin.sock jabali jabali-sockets 660; then
    _die "Kratos admin socket has wrong perms — see message above"
  fi
  if ! verify_socket_perms /run/jabali-kratos/public.sock jabali jabali-sockets 660; then
    _die "Kratos public socket has wrong perms — see message above"
  fi
  if ! verify_no_all_interface_binds 4434; then
    _die "Kratos admin still bound on TCP :4434 — kratos.yml didn't apply or rolled back to TCP"
  fi
  if ! verify_no_all_interface_binds 4433; then
    _die "Kratos public still bound on TCP :4433 — kratos.yml didn't apply or rolled back to TCP"
  fi

  # Migrate an existing /etc/jabali-panel/config.toml from the legacy TCP
  # URLs to the unix-socket forms. Idempotent: a config that already has
  # the unix URLs (or any other custom value) is left untouched. This
  # lives in install.sh — not in a separate cutover CLI — because
  # `jabali update` re-runs install.sh on the operator's host; a separate
  # script would never run on managed boxes (per "install.sh is truth"
  # memory).
  local panel_config="/etc/jabali-panel/config.toml"
  if [[ -f "$panel_config" ]] && grep -qE '^\s*admin_url\s*=\s*"http://127\.0\.0\.1:4434"' "$panel_config"; then
    _log "migrating config.toml admin_url from TCP to unix socket (M25 Step 2)"
    sed -i 's|^\(\s*admin_url\s*=\s*\)"http://127\.0\.0\.1:4434"|\1"unix:/run/jabali-kratos/admin.sock"|' "$panel_config"
    _ok "config.toml admin_url migrated"
  fi
  if [[ -f "$panel_config" ]] && grep -qE '^\s*public_url\s*=\s*"http://127\.0\.0\.1:4433"' "$panel_config"; then
    _log "migrating config.toml public_url from TCP to unix socket (M25 Step 3)"
    sed -i 's|^\(\s*public_url\s*=\s*\)"http://127\.0\.0\.1:4433"|\1"unix:/run/jabali-kratos/public.sock"|' "$panel_config"
    _ok "config.toml public_url migrated"
  fi

  _ok "Kratos identity provider installed and running"
}

# ensure_jabali_in_mysql_group — usermod -aG mysql jabali, idempotent.
# Called from install_kratos preflight + provision_new_software so an
# already-installed host that pre-dates the M25.1 socket-DSN era picks
# up the membership on the next `jabali update`.
ensure_jabali_in_mysql_group() {
  if ! getent group mysql >/dev/null 2>&1; then
    return 0
  fi
  if id -nG "$SERVICE_USER" 2>/dev/null | tr ' ' '\n' | grep -qx mysql; then
    return 0
  fi
  _log "adding $SERVICE_USER to mysql group (Kratos DSN unix socket)"
  usermod -aG mysql "$SERVICE_USER" 2>/dev/null || _warn "usermod -aG mysql $SERVICE_USER failed"
  # systemd unit's SupplementaryGroups already lists mysql, but the
  # /etc/group write must be visible before we restart Kratos so the
  # next start picks up the membership.
  systemctl daemon-reload 2>/dev/null || true
  if systemctl is-active jabali-kratos >/dev/null 2>&1; then
    systemctl restart jabali-kratos 2>/dev/null || _warn "restart jabali-kratos failed"
  fi
}


# ---------- step XX: GoAccess log analyzer ----------------------------------

install_goaccess() {
  _log "installing GoAccess web log analyzer for logs & statistics"

  # Install GoAccess from official Debian repositories
  if ! command -v goaccess >/dev/null 2>&1; then
    _spin "apt install goaccess" \
      apt-get install -y -qq --no-install-recommends goaccess
  fi

  if ! command -v goaccess >/dev/null 2>&1; then
    _die "goaccess binary not found after installation"
  fi
  _ok "GoAccess present ($(goaccess --version 2>&1 | head -1))"

  # Create GoAccess configuration file
  local goaccess_config="/etc/goaccess/goaccess.conf"
  if [[ ! -f "$goaccess_config" ]]; then
    _log "creating GoAccess configuration at $goaccess_config"
    install -d -m 0755 /etc/goaccess
    cat > "$goaccess_config" << 'GOACCESS_EOF'
# GoAccess configuration for jabali panel
# Managed by install.sh - do not edit manually

# Log format for nginx access logs
log-format COMBINED

# Date format
date-format %d/%b/%Y
time-format %T

# Real-time HTML settings
real-time-html true
ws-url ws://127.0.0.1:7890

# Output settings
html-prefs {"theme":"bright","perPage":7,"layout":"horizontal","showTables":true,"showGraphs":true}

# Exclude static files
ignore-panel /\.(?:css|js|jpg|png|gif|ico|jpeg|pdf|txt|zip|tar|gz|woff|woff2|eot|ttf|svg)(?:\?.*)?$

# WebSocket settings
port 7890
addr 127.0.0.1
GOACCESS_EOF
    chmod 644 "$goaccess_config"
  else
    _log "GoAccess config already exists at $goaccess_config"
  fi

  # Create directory for GoAccess reports
  local reports_dir="/var/lib/jabali-goaccess"
  if [[ ! -d "$reports_dir" ]]; then
    _log "creating reports directory at $reports_dir"
    install -d -m 0755 -o jabali -g jabali "$reports_dir"
  fi

  # Create systemd timer for periodic report generation
  local timer_dir="/etc/systemd/system"
  local timer_unit="$timer_dir/jabali-goaccess.timer"
  local service_unit="$timer_dir/jabali-goaccess.service"

  # Service unit
  if [[ ! -f "$service_unit" ]]; then
    _log "creating GoAccess service unit"
    cat > "$service_unit" << 'SERVICE_EOF'
[Unit]
Description=Generate GoAccess reports for jabali domains
After=network.target

[Service]
Type=oneshot
User=jabali
Group=jabali
ExecStart=/usr/local/bin/jabali-goaccess-generator
PrivateTmp=yes
ProtectHome=yes
ProtectSystem=strict
ReadWritePaths=/var/lib/jabali-goaccess
ReadOnlyPaths=/var/log/nginx
SERVICE_EOF
  fi

  # Timer unit
  if [[ ! -f "$timer_unit" ]]; then
    _log "creating GoAccess timer unit"
    cat > "$timer_unit" << 'TIMER_EOF'
[Unit]
Description=Run GoAccess report generation every 15 minutes
Requires=jabali-goaccess.service

[Timer]
OnCalendar=*:0/15
Persistent=true

[Install]
WantedBy=timers.target
TIMER_EOF
  fi

  # Create the generator script
  local generator_script="/usr/local/bin/jabali-goaccess-generator"
  if [[ ! -f "$generator_script" ]]; then
    _log "creating GoAccess report generator script"
    cat > "$generator_script" << 'SCRIPT_EOF'
#!/bin/bash
set -euo pipefail

# Generate GoAccess reports for all domains
REPORTS_DIR="/var/lib/jabali-goaccess"
NGINX_LOG_DIR="/var/log/nginx"

# Ensure reports directory exists
mkdir -p "$REPORTS_DIR"

# Generate reports for each domain
for access_log in "$NGINX_LOG_DIR"/*.access.log; do
    if [[ -f "$access_log" ]]; then
        domain=$(basename "$access_log" .access.log)
        output_file="$REPORTS_DIR/${domain}.html"

        # Skip if no new log entries since last generation
        if [[ -f "$output_file" ]] && [[ ! "$access_log" -nt "$output_file" ]]; then
            continue
        fi

        # Generate HTML report
        goaccess "$access_log" -o "$output_file" --log-format=COMBINED --date-format='%d/%b/%Y' --time-format='%T' --html --real-time-html 2>/dev/null || true
    fi
done
SCRIPT_EOF
    chmod 755 "$generator_script"
  fi

  # Enable and start the timer
  systemctl daemon-reload
  systemctl enable --quiet jabali-goaccess.timer
  systemctl start jabali-goaccess.timer

  _ok "GoAccess log analyzer installed and configured"
}

# ---------- main ------------------------------------------------------------

install_snuffleupagus() {
  # Pin the upstream tag + tarball SHA256. Update both atomically when
  # bumping. SHA256 = sha256sum of the GitHub release tarball.
  local snuf_version="0.13.0"
  local snuf_sha256="350a33cd3906bdba46f5c4cf3d00edeb81eaf6a7b9a3a7e5ef47bc967492ae90"

  local build="${REPO_DIR}/install/snuffleupagus/build/build.sh"
  if [[ ! -x "$build" ]]; then
    _err "snuffleupagus build script missing: $build"
    return 1
  fi

  # Build deps. snuffleupagus needs the same toolchain as any phpize-built
  # extension. The phpX.Y-dev metapackage ships phpize + php-config + the
  # Zend headers we link against. install_base_packages does NOT pull it
  # because nothing else needs it; we must install per-minor here.
  if ! dpkg -s build-essential libpcre2-dev >/dev/null 2>&1; then
    _spin "apt install build-essential + libpcre2-dev" \
      apt-get install -y -qq --no-install-recommends build-essential libpcre2-dev
  fi
  # Build for every PHP minor that has an FPM binary on disk — not just
  # JABALI_PHP_VERSIONS. The PHP Manager UI lets the operator install
  # additional minors at runtime; without auto-detect, install_snuffleupagus
  # would only cover the bootstrap-time JABALI_PHP_VERSIONS set and
  # operator-added minors would silently lack PHP Defense (caught
  # 2026-05-04 — UI showed "1/3 installed PHP minors" with 8.5 active).
  local _detected_minors=""
  if compgen -G "/usr/sbin/php*-fpm" >/dev/null; then
    _detected_minors="$(ls -1 /usr/sbin/php*-fpm 2>/dev/null \
      | sed -E 's|.*/php([0-9]+\.[0-9]+)-fpm|\1|' \
      | sort -u | tr '\n' ' ')"
  fi
  # Union of explicit override + on-disk detection. Keeps the override
  # behavior (operator forcing a specific subset) while adding any
  # newly-installed minor automatically on the next run.
  local _php_versions="${JABALI_PHP_VERSIONS:-} ${_detected_minors}"
  _php_versions="$(echo $_php_versions | tr ' ' '\n' | sort -u | tr '\n' ' ' | sed 's/ $//')"
  if [[ -z "${_php_versions// /}" ]]; then
    _php_versions="8.4"
  fi
  local _minor _dev_pkgs=()
  for _minor in $_php_versions; do
    if ! dpkg -s "php${_minor}-dev" >/dev/null 2>&1; then
      _dev_pkgs+=("php${_minor}-dev")
    fi
  done
  if (( ${#_dev_pkgs[@]} > 0 )); then
    # When install_snuffleupagus runs via `jabali update --force`'s
    # prelude (sourcing install.sh then calling this function directly),
    # install_base_packages's apt-get update + Sury repo setup may not
    # have run in this shell. Ensure both before attempting the dev
    # install or apt errors with "Unable to locate package phpX.Y-dev"
    # because the Sury index is missing/stale (caught 2026-05-04 on the
    # mx VM after install.sh refactor — install_php had run earlier so
    # phpX.Y-fpm/cli were present, but apt cache had since aged out).
    if [[ ! -s /etc/apt/sources.list.d/sury-php.list ]] \
       && declare -F _install_sury_source >/dev/null 2>&1; then
      _install_sury_source
    fi
    _spin "apt update (refresh Sury index for php-dev)" \
      apt-get update -qq -o Acquire::Languages=none
    _spin "apt install ${_dev_pkgs[*]}" \
      apt-get install -y -qq --no-install-recommends "${_dev_pkgs[@]}"
  fi

  # Active rules dir + placeholder file. mode=off by default, so the
  # placeholder disables the module. Reconciler overwrites once state
  # flips to simulation/enforce.
  install -d -m 0755 /etc/jabali/snuffleupagus
  if [[ ! -f /etc/jabali/snuffleupagus/active.rules ]]; then
    cat > /etc/jabali/snuffleupagus/active.rules <<'EOF_RULES'
# Jabali Snuffleupagus active rules -- RENDERED, do not edit.
# mode=off
# Placeholder empty ruleset. Snuffleupagus v0.13 has no master switch;
# sp.global.enable is invalid and crashes PHP-FPM (GH #718). The reconciler
# overwrites this file when the operator flips mode to simulation or enforce.
EOF_RULES
    chmod 0644 /etc/jabali/snuffleupagus/active.rules
  fi
  # cli.ini for the jabali-php wrapper (Wave C). Pinning prevents
  # customer-supplied -c flags from sidestepping the rules file.
  if [[ ! -f /etc/jabali/snuffleupagus/cli.ini ]]; then
    cat > /etc/jabali/snuffleupagus/cli.ini <<'EOF_CLI'
; Jabali PHP-CLI wrapper config — pin sp.configuration_file so cron and
; SFTP-shell PHP cannot dodge the active rule set via custom .ini.
sp.configuration_file=/etc/jabali/snuffleupagus/active.rules
EOF_CLI
    chmod 0644 /etc/jabali/snuffleupagus/cli.ini
  fi
  if [[ ! -f /etc/jabali/snuffleupagus/mode ]]; then
    echo "enforce" > /etc/jabali/snuffleupagus/mode
    chmod 0644 /etc/jabali/snuffleupagus/mode
  fi

  # Mirror the rule bundle into /usr/share/jabali/snuffleupagus/rules so
  # the panel reconciler reads from a stable on-disk path independent of
  # the source checkout layout.
  install -d -m 0755 /usr/share/jabali/snuffleupagus/rules
  if [[ -d "${REPO_DIR}/install/snuffleupagus/rules" ]]; then
    install -m 0644 "${REPO_DIR}/install/snuffleupagus/rules/"*.rules \
      /usr/share/jabali/snuffleupagus/rules/ 2>/dev/null || true
    if [[ -f "${REPO_DIR}/install/snuffleupagus/rules/README.md" ]]; then
      install -m 0644 "${REPO_DIR}/install/snuffleupagus/rules/README.md" \
        /usr/share/jabali/snuffleupagus/rules/ 2>/dev/null || true
    fi
  fi

  # Build per minor. Same auto-detect as the dev-pkg loop above:
  # union of JABALI_PHP_VERSIONS + every phpX.Y-fpm binary on disk.
  # Operator-installed minors via the PHP Manager UI get covered on
  # the next install.sh / `jabali update --force` run without manual
  # JABALI_PHP_VERSIONS edits.
  local php_versions="$_php_versions"
  local minor
  for minor in $php_versions; do
    [[ -d "/etc/php/$minor/fpm" ]] || continue
    SNUFFLEUPAGUS_VERSION="$snuf_version" \
    SNUFFLEUPAGUS_SHA256="$snuf_sha256" \
      "$build" "$minor" || {
        _warn "snuffleupagus build failed for PHP $minor (continuing other minors)"
        continue
      }
    # mods-available + conf.d wiring (FPM + CLI both load sp.so).
    cat > "/etc/php/$minor/mods-available/jabali-snuffleupagus.ini" <<EOF_MOD
; Jabali Snuffleupagus extension load + config-file pin.
extension=/usr/lib/php/jabali-snuffleupagus/$minor/snuffleupagus.so
sp.configuration_file=/etc/jabali/snuffleupagus/active.rules
EOF_MOD
    # 0644, NOT the install.sh umask-077 default of 0600: the per-user
    # php-fpm master starts as User=<hosting-user> and parses conf.d at
    # startup. A 0600 root:root ini is silently skipped by that
    # unprivileged parse -> snuffleupagus.so never loads on web traffic
    # -> zero PHP defense regardless of mode. The ini holds no secrets
    # (extension path + config_file path), like every other conf.d ini.
    chmod 0644 "/etc/php/$minor/mods-available/jabali-snuffleupagus.ini"
    ln -sf "../../mods-available/jabali-snuffleupagus.ini" \
      "/etc/php/$minor/fpm/conf.d/30-jabali-snuffleupagus.ini"
    ln -sf "../../mods-available/jabali-snuffleupagus.ini" \
      "/etc/php/$minor/cli/conf.d/30-jabali-snuffleupagus.ini"
  done

  # Wave C: PHP-CLI bypass detection. Watch direct execve of every Sury
  # /usr/bin/phpX.Y plus /usr/bin/php so SFTP-shell users running php
  # outside the FPM pool surface in auditd logs (key=jabali_php_bypass).
  install_audit_php_bypass

  _ok "snuffleupagus installed across PHP minors (mode=off; flip via Security UI)"
}
install_audit_php_bypass() {
  if ! dpkg -s auditd >/dev/null 2>&1; then
    _warn "auditd not installed — install_audit_exec should have run earlier"
    return 0
  fi

  local rules_file=/etc/audit/rules.d/jabali-snuffleupagus.rules
  local rules_tmp
  rules_tmp=$(mktemp)
  {
    echo "# Jabali Snuffleupagus PHP-CLI bypass detection (M41, ADR-0088)."
    echo "# Tagged 'jabali_php_bypass' for ausearch -k pivots."
    echo "# auid>=1000 = real users only (excludes daemon services)."
    echo "# Catches \`php -n\` style bypass of the conf.d sp.so drop-in."
    echo
    echo "-a always,exit -F arch=b64 -S execve -F path=/usr/bin/php       -F auid>=1000 -F auid!=4294967295 -k jabali_php_bypass"
    local minor
    for minor in $(ls -1d /etc/php/[0-9]*.[0-9]* 2>/dev/null | xargs -r -n1 basename); do
      local bin="/usr/bin/php${minor}"
      [[ -x "$bin" ]] || continue
      printf -- '-a always,exit -F arch=b64 -S execve -F path=%-21s -F auid>=1000 -F auid!=4294967295 -k jabali_php_bypass\n' "$bin"
    done
  } >"$rules_tmp"

  if [[ ! -f "$rules_file" ]] || ! cmp -s "$rules_tmp" "$rules_file"; then
    install -m 0640 -o root -g root "$rules_tmp" "$rules_file"
    if command -v augenrules >/dev/null 2>&1; then
      augenrules --load >/dev/null 2>&1 || \
        _warn "augenrules --load failed — auditd may need a restart"
    fi
    _ok "auditd jabali-snuffleupagus.rules installed (key=jabali_php_bypass)"
  fi
  rm -f "$rules_tmp"
}

# provision_new_software — idempotent, called by `jabali update` (prelude step)
# so newly-required packages/collections reach existing hosts without a full
# re-install. Add new software HERE; install.sh main() still calls the full
# install_* functions, so fresh installs also get everything.
# ensure_crowdsec_firewall_bouncer_lapi_url — heal hosts that were
# installed before LAPI moved off the stock 127.0.0.1:8080 (Stalwart's
# port). The package postinst writes 8080 → bouncer crash-loops with
# "bouncer stream halted". Fresh installs are correct via
# install_crowdsec; this provision-time fix-up is for old boxes.
ensure_crowdsec_firewall_bouncer_lapi_url() {
  local cfg=/etc/crowdsec/bouncers/crowdsec-firewall-bouncer.yaml
  [[ -f "$cfg" ]] || return 0
  if grep -qE '^api_url:[[:space:]]+http://127\.0\.0\.1:8080/?$' "$cfg"; then
    _log "patching firewall-bouncer api_url 8080 -> 8081"
    sed -i 's|^api_url:[[:space:]]\+http://127\.0\.0\.1:8080/\?|api_url: http://127.0.0.1:8081/|' "$cfg"
    systemctl restart crowdsec-firewall-bouncer 2>/dev/null || \
      _warn "restart crowdsec-firewall-bouncer failed; check journalctl"
  fi
}

# ensure_crowdsec_nginx_bouncer_lapi_url — heal nginx-bouncer configs
# that shipped with API_URL= (empty). Console flags engines without a
# polling remediation component as broken. Repoint at LAPI loopback so
# the bouncer heartbeats up to CrowdSec Central.
ensure_crowdsec_nginx_bouncer_lapi_url() {
  local cfg=/etc/crowdsec/bouncers/crowdsec-nginx-bouncer.conf
  [[ -f "$cfg" ]] || return 0
  if grep -qE '^API_URL=[[:space:]]*$' "$cfg"; then
    _log "patching nginx-bouncer API_URL (empty -> http://127.0.0.1:8081/)"
    sed -i 's|^API_URL=[[:space:]]*$|API_URL=http://127.0.0.1:8081/|' "$cfg"
    if nginx -t >/dev/null 2>&1; then
      systemctl reload nginx 2>/dev/null || \
        _warn "nginx reload failed after bouncer api_url heal"
    else
      _warn "nginx -t failed after bouncer api_url heal — check config"
    fi
  fi
}

# ensure_crowdsec_bouncer_poll_frequency — bump both bouncer
# update_frequency values to 60s on existing hosts. Default 10s burns
# steady CPU on the LAPI process once CAPI has ~30k decisions loaded
# (each poll = full SQLite scan-and-diff). Symptoms: sustained 6-10%
# crowdsec CPU at idle, 80k+ pread64/sec, 90+ open fds on
# crowdsec.db. Fresh installs land 60s from install_crowdsec_bouncer
# / install_crowdsec_nginx_bouncer; this heals hosts that took the
# old defaults.
ensure_crowdsec_bouncer_poll_frequency() {
  local dirty=0
  local fw=/etc/crowdsec/bouncers/crowdsec-firewall-bouncer.yaml
  if [[ -f "$fw" ]] && grep -qE '^update_frequency:[[:space:]]+10s?$' "$fw"; then
    _log "patching firewall-bouncer update_frequency 10s -> 60s"
    sed -i 's|^update_frequency:[[:space:]]\+10s\?$|update_frequency: 60s|' "$fw"
    systemctl restart crowdsec-firewall-bouncer 2>/dev/null || \
      _warn "restart crowdsec-firewall-bouncer failed; check journalctl"
    dirty=1
  fi
  local ng=/etc/crowdsec/bouncers/crowdsec-nginx-bouncer.conf
  if [[ -f "$ng" ]] && grep -qE '^UPDATE_FREQUENCY=10$' "$ng"; then
    _log "patching nginx-bouncer UPDATE_FREQUENCY 10 -> 60"
    sed -i 's|^UPDATE_FREQUENCY=10$|UPDATE_FREQUENCY=60|' "$ng"
    if nginx -t >/dev/null 2>&1; then
      systemctl reload nginx 2>/dev/null || \
        _warn "nginx reload failed after bouncer poll-freq heal"
    else
      _warn "nginx -t failed after bouncer poll-freq heal — check config"
    fi
    dirty=1
  fi
  [[ $dirty -eq 1 ]] && _ok "crowdsec bouncers: poll frequency lowered to 60s"
  return 0
}

# ensure_crowdsec_diag_isolation — JAB-368. CrowdSec's prometheus listener on
# 127.0.0.1:6060 is UNAUTHENTICATED and (on level: full) serves Go /debug/pprof
# alongside /metrics. It cannot be disabled — `cscli metrics` (the panel's
# CrowdSec metrics source) requires it — so we make it unreachable from tenant
# code instead. JAB-352 blocked the SSH-forwarding path; this closes the
# independent direct-loopback path from a tenant's own PHP-FPM/CGI/shell.
#
# The nft ruleset matches on skuid >= 1000 (jabali's login UID_MIN): every tenant
# execution context runs as a real user, while root (cscli) and system services
# (uid < 1000, incl. the panel user) are unaffected. skuid — not cgroup — because
# tenant PHP-FPM runs under system.slice and tenant shells under user.slice, so a
# cgroup match would miss both; it also needs no cgroupv2 path, so the load unit
# has no ConditionPathExists boot window.
#
# Idempotent + runs on install AND every `jabali update` (provision_new_software),
# so existing fleet hosts converge. Never `systemctl restart nftables` (would
# flush UFW + crowdsec-bouncer chains) — `nft -f` the single file only.
ensure_crowdsec_diag_isolation() {
  command -v nft >/dev/null 2>&1 || return 0
  local src="${REPO_DIR:-/opt/jabali-panel}/install/crowdsec/jabali-diag-loopback-isolation.nft"
  local dst=/etc/nftables.d/jabali-diag-loopback-isolation.nft
  local unit_src="${REPO_DIR:-/opt/jabali-panel}/install/systemd/jabali-diag-loopback-isolation-load.service"
  local unit_dst=/etc/systemd/system/jabali-diag-loopback-isolation-load.service
  if [[ ! -f "$src" || ! -f "$unit_src" ]]; then
    _warn "crowdsec diag-isolation source missing ($src) — skipping (JAB-368 not applied)"
    return 0
  fi
  install -d -m 0755 /etc/nftables.d
  local changed=0
  if [[ ! -f "$dst" ]] || ! cmp -s "$src" "$dst"; then
    install -m 0644 -o root -g root "$src" "$dst"; changed=1
  fi
  if [[ ! -f "$unit_dst" ]] || ! cmp -s "$unit_src" "$unit_dst"; then
    install -m 0644 -o root -g root "$unit_src" "$unit_dst"
    systemctl daemon-reload 2>/dev/null || true
    changed=1
  fi
  systemctl enable jabali-diag-loopback-isolation-load.service >/dev/null 2>&1 || true
  # The add/flush idiom in the file makes a re-apply replace the rules rather
  # than append duplicates; nft -f is a single transaction.
  if nft -f "$dst" 2>/dev/null; then
    [[ $changed -eq 1 ]] && _ok "crowdsec :6060 diagnostics blocked from tenant code (JAB-368)"
  else
    _warn "could not apply crowdsec diag-isolation nft rules now — will retry at boot (JAB-368)"
  fi
  return 0
}

# install_cloudflare_realip — drop /etc/nginx/conf.d/jabali-cloudflare-realip.conf
# so nginx rewrites $remote_addr from the CF-Connecting-IP header when the
# request arrives from a Cloudflare proxy range. Without it, every
# Cloudflare-fronted hit is logged + banned + counted against the proxy
# IP (172.6x.x.x, 162.158.x.x, etc) instead of the real client, which
# defeats CrowdSec scenario matching for any site behind CF.
#
# Idempotent: fetches the current CF IPv4 + IPv6 ranges from
# https://www.cloudflare.com/ips-v4 / ips-v6 (60s timeout, ~1KB each),
# falls back to a hard-coded 2026-Q2 snapshot if both fetches fail.
# Write-on-diff via cmp; nginx -t + reload only when the file changed.
#
# Safe on hosts NOT behind Cloudflare: real_ip_header CF-Connecting-IP
# only fires when set_real_ip_from matches the proxy, so direct hits
# (any IP outside the CF ranges) pass through unchanged.
install_cloudflare_realip() {
  local cf_v4 cf_v6
  cf_v4="$(curl -fsSL --max-time 10 https://www.cloudflare.com/ips-v4 2>/dev/null || true)"
  cf_v6="$(curl -fsSL --max-time 10 https://www.cloudflare.com/ips-v6 2>/dev/null || true)"

  # Hard-coded fallback — current as of 2026-Q2. Used if both fetches
  # failed (offline install / CF outage). Operator can re-run
  # `jabali update` later to pull the live list.
  if [[ -z "$cf_v4" ]]; then
    cf_v4=$'173.245.48.0/20\n103.21.244.0/22\n103.22.200.0/22\n103.31.4.0/22\n141.101.64.0/18\n108.162.192.0/18\n190.93.240.0/20\n188.114.96.0/20\n197.234.240.0/22\n198.41.128.0/17\n162.158.0.0/15\n104.16.0.0/13\n104.24.0.0/14\n172.64.0.0/13\n131.0.72.0/22'
    _warn "cloudflare ips-v4 fetch failed; using 2026-Q2 fallback ranges"
  fi
  if [[ -z "$cf_v6" ]]; then
    cf_v6=$'2400:cb00::/32\n2606:4700::/32\n2803:f800::/32\n2405:b500::/32\n2405:8100::/32\n2a06:98c0::/29\n2c0f:f248::/32'
    _warn "cloudflare ips-v6 fetch failed; using 2026-Q2 fallback ranges"
  fi

  # Validate every line before it becomes an nginx directive. These bodies
  # come off the network, and a 200 response is not proof of content: a
  # captive portal, a TLS-intercepting middlebox, or a CF error page all
  # return 200 with HTML. Unfiltered, that HTML was written verbatim as
  # `set_real_ip_from <garbage>;` — and a line containing a ';' could inject
  # arbitrary http-context directives (e.g. flipping real_ip_header to
  # X-Forwarded-For, which would make every client IP forgeable and neuter
  # IP-based blocking). Accept only well-formed CIDRs.
  local cf_v4_clean cf_v6_clean
  cf_v4_clean="$(printf '%s\n' "$cf_v4" | grep -E '^[0-9]{1,3}(\.[0-9]{1,3}){3}/[0-9]{1,2}$' || true)"
  cf_v6_clean="$(printf '%s\n' "$cf_v6" | grep -E '^[0-9a-fA-F:]+/[0-9]{1,3}$' || true)"

  # A drastically short list means the fetch returned something that merely
  # looked like data. Keeping the existing file beats silently narrowing the
  # trusted-proxy set, which would start attributing real client IPs to the
  # Cloudflare edge again. Same guard as refresh-cf-ranges.sh.
  local v4_count v6_count
  v4_count="$(printf '%s\n' "$cf_v4_clean" | grep -c . || true)"
  v6_count="$(printf '%s\n' "$cf_v6_clean" | grep -c . || true)"
  if [[ "$v4_count" -lt 5 || "$v6_count" -lt 3 ]]; then
    _warn "cloudflare ranges look wrong after validation (v4=$v4_count v6=$v6_count) — keeping the existing real-IP config"
    return 0
  fi
  cf_v4="$cf_v4_clean"
  cf_v6="$cf_v6_clean"

  local conf_file=/etc/nginx/conf.d/jabali-cloudflare-realip.conf
  local tmp
  tmp="$(mktemp --tmpdir jabali-cf-realip.XXXXXX)"
  {
    printf '# Managed by jabali install.sh — Cloudflare real-IP rewrite.\n'
    printf '# Refreshed on every `jabali update` (CF ranges change ~yearly).\n'
    printf '# Safe on hosts NOT behind Cloudflare: real_ip_header only fires\n'
    printf '# when the request source matches a set_real_ip_from prefix.\n'
    while IFS= read -r cidr; do
      [[ -n "$cidr" ]] && printf 'set_real_ip_from %s;\n' "$cidr"
    done <<<"$cf_v4"
    while IFS= read -r cidr; do
      [[ -n "$cidr" ]] && printf 'set_real_ip_from %s;\n' "$cidr"
    done <<<"$cf_v6"
    printf 'real_ip_header CF-Connecting-IP;\n'
    printf 'real_ip_recursive on;\n'
  } >"$tmp"

  if [[ ! -f "$conf_file" ]] || ! cmp -s "$tmp" "$conf_file"; then
    _log "writing $conf_file (Cloudflare real-IP rewrite)"
    # Keep the previous file so a failed nginx -t can be ROLLED BACK. Writing
    # into conf.d and leaving a bad file behind on failure meant the next
    # nginx start — possibly a reboot much later, with nobody watching —
    # failed on a config this function wrote and warned about once.
    local prev=""
    if [[ -f "$conf_file" ]]; then
      prev="$(mktemp --tmpdir jabali-cf-realip-prev.XXXXXX)"
      cp -p "$conf_file" "$prev"
    fi
    install -m 0644 -o root -g root "$tmp" "$conf_file"
    if nginx -t >/dev/null 2>&1; then
      systemctl reload nginx 2>/dev/null || _warn "nginx reload failed after cloudflare-realip update"
      _ok "nginx Cloudflare real-IP rewrite installed/refreshed"
    else
      if [[ -n "$prev" ]]; then
        install -m 0644 -o root -g root "$prev" "$conf_file"
        _warn "nginx -t failed after cloudflare-realip write — REVERTED $conf_file to the previous version"
      else
        rm -f "$conf_file"
        _warn "nginx -t failed after cloudflare-realip write — REMOVED $conf_file (it did not exist before)"
      fi
      nginx -t >/dev/null 2>&1 || _warn "nginx -t STILL failing after rollback — the problem is elsewhere in the nginx config"
    fi
    [[ -n "$prev" ]] && rm -f "$prev"
  fi
  rm -f "$tmp"
}


# Heal the /etc/jabali-panel directory mode on every `jabali update`.
#
# The dir is created 0755 now, but earlier installer versions created
# it 0750 root:jabali, and the only places that loosened it to 0755
# were buried inside install_powerdns + the sso-key step. A partial
# provision path that skipped those left it 0750, which the unprivileged
# per-user FPM cannot traverse -> jabali-fpm@<user> crash-loops -> every
# hosted PHP site 502s. This ensure_* runs unconditionally from
# provision_new_software so existing 0750 hosts self-heal on update,
# independent of which install_* functions run. Idempotent.
# Heal Snuffleupagus loadability on every `jabali update`.
#
# The per-user php-fpm master runs as User=<hosting-user> and parses
# conf.d + dlopen's the extension at startup. Two paths get created
# under install.sh's umask 077 and must be world-traversable/readable
# or the extension SILENTLY does not load on web traffic (root-context
# `php-fpm -i` still shows it, masking the bug -> zero PHP defense):
#   - /usr/lib/php/jabali-snuffleupagus[/<minor>]  (build.sh, 0700)
#   - /etc/php/<minor>/mods-available/jabali-snuffleupagus.ini (0600)
# build.sh is idempotent (skips when the .so exists) so an existing
# 0700 host never re-runs the in-build chmod; heal here. Idempotent.
ensure_snuffleupagus_loadable() {
  [[ -d /usr/lib/php/jabali-snuffleupagus ]] || return 0
  local changed=0 d ini cur
  if [[ "$(stat -c '%a' /usr/lib/php/jabali-snuffleupagus 2>/dev/null)" != "755" ]]; then
    chmod 0755 /usr/lib/php/jabali-snuffleupagus && changed=1
  fi
  for d in /usr/lib/php/jabali-snuffleupagus/*/; do
    [[ -d "$d" ]] || continue
    if [[ "$(stat -c '%a' "$d" 2>/dev/null)" != "755" ]]; then
      chmod 0755 "$d" && changed=1
    fi
  done
  for ini in /etc/php/*/mods-available/jabali-snuffleupagus.ini; do
    [[ -f "$ini" ]] || continue
    cur="$(stat -c '%a' "$ini" 2>/dev/null)"
    if [[ "$cur" != "644" ]]; then
      chmod 0644 "$ini" && changed=1
    fi
  done
  [[ "$changed" == "1" ]] && _log "provision: healed Snuffleupagus loadability (lib dir 0755 / ini 0644)"
  return 0
}

ensure_jabali_panel_dir_traversable() {
  [[ -d /etc/jabali-panel ]] || return 0
  local cur
  cur="$(stat -c '%a' /etc/jabali-panel 2>/dev/null || echo '')"
  if [[ "$cur" != "755" ]]; then
    chmod 0755 /etc/jabali-panel
    _log "provision: healed /etc/jabali-panel mode ${cur:-?} -> 755 (per-user FPM traversal)"
  fi
}

# ensure_stalwart_not_in_panel_group — JAB-357 criterion 2. The Stalwart mail
# server (User=jabali-mail) must NOT hold the broad `$SERVICE_USER` (jabali)
# group: that group owns the root Agent socket (/run/jabali/agent.sock, 0660
# root:jabali) + panel secrets under /etc/jabali-panel, so an internet-facing
# mail-server compromise must not hold it. The agent's SO_PEERCRED gate already
# rejects non-panel UIDs; this strips the FS-layer reach too (defense in depth).
# Stalwart needs no supplementary group — its admin token is read via its own
# primary group (0640 jabali:jabali-mail), and DKIM is signed from Stalwart's
# registry (seeded by the root agent via createDkimSignature), never from the
# on-disk /etc/jabali-panel/dkim keys. Converge upgraded hosts: redeploy the
# corrected unit (drops the legacy SupplementaryGroups=jabali), strip the
# /etc/group membership, then restart so the live process sheds the gid.
# Idempotent — no-op once converged.
ensure_stalwart_not_in_panel_group() {
  getent passwd jabali-mail >/dev/null 2>&1 || return 0
  local changed=0
  local unit=/etc/systemd/system/jabali-stalwart.service
  # 1. Redeploy the unit if the legacy SupplementaryGroups=jabali still lingers
  #    (else a restart re-grants the gid from the stale unit file).
  if [[ -f "$unit" ]] && grep -qxE 'SupplementaryGroups=jabali' "$unit"; then
    if [[ -f "${REPO_DIR}/install/systemd/jabali-stalwart.service" ]]; then
      install -m 0644 -o root -g root \
        "${REPO_DIR}/install/systemd/jabali-stalwart.service" "$unit"
      systemctl daemon-reload 2>/dev/null || true
      changed=1
    fi
  fi
  # 2. Drop the legacy /etc/group membership so nothing re-leaks it.
  if id -nG jabali-mail 2>/dev/null | tr ' ' '\n' | grep -qx "$SERVICE_USER"; then
    if gpasswd -d jabali-mail "$SERVICE_USER" >/dev/null 2>&1; then
      changed=1
    else
      _warn "could not remove jabali-mail from $SERVICE_USER group — run: gpasswd -d jabali-mail $SERVICE_USER"
    fi
  fi
  # 3. Restart so the running Stalwart sheds the supplementary gid.
  if [[ "$changed" -eq 1 ]]; then
    systemctl try-restart jabali-stalwart.service >/dev/null 2>&1 || true
    _ok "Stalwart dropped the broad $SERVICE_USER group (JAB-357)"
  fi
}

# reap_orphan_nspawn_php_units — JAB-225 self-heal for orphaned per-user PHP
# nspawn units. Current code never creates systemd-nspawn@<user>-php.service
# (bubblewrap replaced per-user nspawn containers), but a box carried over from
# the M13-nspawn era can still have units enabled for accounts that were later
# deleted. With the account and its machine image gone, an enabled unit fails on
# EVERY boot ("No image for machine '<user>-php'"), permanently padding
# `systemctl --failed` — a health signal operators are trained to trust. The
# user.delete path now tears these down inline; this converger cleans up the
# ones orphaned before that shipped, on every `jabali update`.
#
# Double guard: reap only when BOTH the OS user is gone AND no machine image
# remains — a live container always has an image, so a real workload can never
# be caught here even if a username is mis-parsed.
reap_orphan_nspawn_php_units() {
  local reaped=0 link svc inst user
  shopt -s nullglob
  # Enabled template instances surface as .wants symlinks (that is literally
  # what `systemctl enable` creates) — dependency-free to enumerate.
  for link in /etc/systemd/system/*.wants/systemd-nspawn@*-php.service; do
    svc=$(basename "$link")          # systemd-nspawn@<inst>.service
    inst=${svc#systemd-nspawn@}      # <inst>.service
    inst=${inst%.service}            # <inst>  (== <user>-php)
    user=${inst%-php}                # <user>
    [[ -z "$user" || "$user" == "$inst" ]] && continue
    id "$user" >/dev/null 2>&1 && continue          # OS user still exists → keep
    [[ -e "/var/lib/machines/${inst}" ]] && continue # image present → keep
    systemctl disable --now "$svc" >/dev/null 2>&1 || true
    systemctl reset-failed "$svc" >/dev/null 2>&1 || true
    rm -f "/etc/systemd/nspawn/${inst}.nspawn" 2>/dev/null || true
    reaped=$((reaped + 1))
  done
  shopt -u nullglob
  if [[ "$reaped" -gt 0 ]]; then
    _ok "reaped $reaped orphaned per-user PHP nspawn unit(s) (JAB-225)"
  fi
}

# provision_php_extensions — self-heal PHP runtime extensions on every
# `jabali update`. install_base_packages installs the ext set only on a full
# install and only for JABALI_PHP_VERSIONS, so a newly-required extension (e.g.
# php-sqlite3 for a restored Nextcloud — JAB-39) never reaches existing hosts or
# extra PHP versions a tenant installed via the panel. Ensure the ext packages
# for EVERY installed FPM version; idempotent (already-present = skip). Keep the
# ext list in sync with install_base_packages.
provision_php_extensions() {
  command -v php >/dev/null 2>&1 || return 0
  local exts="mysql mbstring zip gd curl xml intl bcmath opcache redis igbinary sqlite3"
  local d ver e pkgs=()
  for d in /etc/php/*/fpm; do
    [[ -d "$d" ]] || continue
    ver="$(basename "$(dirname "$d")")"
    for e in $exts; do
      if ! dpkg -s "php${ver}-${e}" 2>/dev/null | grep -q '^Status: install ok installed'          && apt-cache show "php${ver}-${e}" >/dev/null 2>&1; then
        pkgs+=("php${ver}-${e}")
      fi
    done
  done
  if (( ${#pkgs[@]} > 0 )); then
    _log "installing missing PHP extensions: ${pkgs[*]}"
    if DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends "${pkgs[@]}" >/dev/null 2>&1; then
      _ok "PHP extensions ensured (${#pkgs[@]} newly installed)"
      # New .so needs the per-user FPM masters to reload it.
      local unit
      while read -r unit; do
        [[ -n "$unit" ]] && systemctl try-restart "$unit" >/dev/null 2>&1 || true
      done < <(systemctl list-units 'jabali-fpm@*' --no-legend --plain --state=active 2>/dev/null | awk '{print $1}')
    else
      _warn "some PHP extensions failed to install: ${pkgs[*]}"
    fi
  fi
}

provision_new_software() {
  # Sweep a leaked policy-rc.d BEFORE anything else — while it sits there,
  # every invoke-rc.d action this provision chain (and logrotate, and dpkg
  # postinsts) performs is silently denied. Found leaked on jabalitests
  # 2026-08-12 (nginx access logs 0-byte for months).
  sweep_leaked_policy_rcd

  # Heal /etc/jabali-panel traversal FIRST — a 0750 parent here means
  # every per-user PHP-FPM is crash-looping right now; fix it before
  # anything else in the provision chain touches PHP.
  ensure_jabali_panel_dir_traversable
  ensure_snuffleupagus_loadable
  # Also on UPDATE, not just fresh install: `jabali update` runs this trimmed
  # path, not main(), so wiring the call only into main() left every EXISTING
  # host — the ones that have been downloading to predictable /tmp paths for
  # months — without the protection. Caught on the testserver deploy: the
  # function was present in install.sh and had simply never run.
  ensure_tmp_hardening

  # Keep the app-marketplace catalogs current on `jabali update` (build_backend,
  # which also calls this, is NOT in the update path). New docker-app /
  # py-framework entries otherwise never reach an update-only box.
  sync_app_catalogs

  # GH #447: converge pdns/pdns-recursor masking to the DB's dns_enabled on
  # every update, so a host that has the DNS module off stops reporting the
  # unconfigured pdns unit as "failed" on the dashboard + Server Status.
  converge_pdns_masking
  # GH #1053: enforce the ftp opt-in on every update — vsftpd masked and
  # ports closed while server_settings.ftp_enabled=0; unmasked + rules
  # healed when on.
  converge_ftp_masking
  # GH #896 + PowerDNS/pdns#11416: carry zone-cache-refresh-interval=0 to
  # boxes that only ever update — install_powerdns (whose template now
  # ships it) does not run
  # on this path.
  ensure_pdns_zone_cache
  # JAB-350: same reasoning — pin the global AXFR ACL to loopback on update so a
  # permissive global on an existing host stops leaving zones internet-transferable.
  ensure_pdns_axfr_deny
  # JAB-352: same reasoning — carry the SSH forwarding-lockdown drop-in to boxes
  # that only ever update (install_sftp_sshd_config runs on fresh install only),
  # so an existing tenant key can't tunnel into loopback-only services.
  ensure_ssh_forwarding_lockdown

  # JAB-357: strip the broad `jabali` group from the Stalwart mail server on
  # already-installed hosts (install_stalwart runs on fresh install only). The
  # group is dead weight for mail and let a mail compromise reach the root Agent
  # socket at the FS layer.
  ensure_stalwart_not_in_panel_group

  # JAB-225: reap per-user PHP nspawn units orphaned before user.delete learned
  # to tear them down — else each fails on every boot and pads `systemctl
  # --failed` forever.
  reap_orphan_nspawn_php_units

  # JAB-273: self-heal the fleet's zero-swap fragility (kswapd death-spiral that
  # locked the operator out of newaramaapp) and contain every all-tenant
  # maintenance job in a memory-capped slice. Idempotent; the existing fleet
  # only picks these up on update, so they MUST run here, not just on install.
  ensure_fleet_swap
  ensure_maintenance_isolation

  # GH #860: retrofit the default-vhost catch-all include onto existing
  # boxes. install_disabled_page is seed-only now (never clobbers operator
  # edits), so re-running it here just ensures the docroots + catch-all
  # file exist; then swap the two hardcoded `return 444;` location bodies
  # in jabali-default.conf for the include. Without this heal the admin
  # toggle would silently no-op on every box installed before #860.
  install_disabled_page
  local _defvhost=/etc/nginx/sites-available/jabali-default.conf
  if [[ -f "$_defvhost" ]] && ! grep -q 'jabali-catchall.conf' "$_defvhost"; then
    sed -i 's|^        return 444;$|        include /etc/nginx/jabali-catchall.conf;|' "$_defvhost"
    if nginx -t >/dev/null 2>&1; then
      systemctl reload nginx 2>/dev/null || true
      _ok "provision: default vhost catch-all include retrofitted (#860)"
    else
      _warn "provision: nginx -t failed after catch-all retrofit — reverting"
      sed -i 's|^        include /etc/nginx/jabali-catchall.conf;$|        return 444;|' "$_defvhost"
    fi
  fi

  # GH #253: refresh the per-user PHP-FPM pool template so updated installs pick
  # up new shared-hosting defaults (memory_limit, upload sizes, etc.). Idempotent
  # copy; the reconciler re-renders each pool from the installed template on its
  # next tick. Without this, only fresh installs would get the new defaults.
  declare -f install_php_pool_template >/dev/null 2>&1 && install_php_pool_template

  # GH #254: whois binary backs the admin Domain "Information" modal
  # (domain.whois agent command). Fresh installs get it from
  # install_base_packages; existing hosts pick it up here on `jabali update`
  # so the new handler doesn't shell out to a missing binary. Idempotent.
  if ! command -v whois >/dev/null 2>&1; then
    _log "provision: whois missing — installing for Domain Information (#254)"
    apt-get install -y -qq --no-install-recommends whois \
      || _warn "provision: whois install had issues"
  fi

  # GH #606: converge phpredis + igbinary onto EVERY installed PHP version so a
  # host installed before #606 gets the fast native object-cache client (not the
  # pure-PHP fallback) on `jabali update`. install_base_packages only runs on
  # fresh installs; this is the existing-host path. Idempotent — apt skips
  # already-present packages. Reload the per-user FPM masters so the extension
  # is live immediately.
  _php_cache_ext_pkgs=()
  for _phpv in $(ls -1 /etc/php/ 2>/dev/null | grep -E '^[0-9]+\.[0-9]+$'); do
    for _ext in redis igbinary; do
      if ! dpkg -s "php${_phpv}-${_ext}" >/dev/null 2>&1 \
         && apt-cache show "php${_phpv}-${_ext}" >/dev/null 2>&1; then
        _php_cache_ext_pkgs+=("php${_phpv}-${_ext}")
      fi
    done
  done
  if [[ ${#_php_cache_ext_pkgs[@]} -gt 0 ]]; then
    if apt-get install -y -qq --no-install-recommends "${_php_cache_ext_pkgs[@]}"; then
      _ok "provision: installed missing PHP cache extensions: ${_php_cache_ext_pkgs[*]} (#606)"
      systemctl reload 'jabali-fpm@*.service' 2>/dev/null || true
    else
      _warn "provision: PHP redis/igbinary ensure had issues"
    fi
  fi

  # Libexec helpers (fpm-pre-start, fpm-exec, fpm-post-start, cron-precheck)
  # — generated systemd units reference these by absolute path. The
  # fresh-install path installs them, and update.go's unit-sync heredoc
  # re-copies them, but that heredoc runs the PRIOR binary embedded code on
  # the first jabali update after a release that added one (one-update lag).
  # This provision step is sourced FRESH from the just-pulled install.sh, so
  # converging the helpers here installs them on the FIRST update — no lag.
  # cron-precheck specifically: without it a tenant cron ExecStartPre dies
  # 203/EXEC and the scheduled job never runs. fpm-post-start specifically
  # (GH #302): it is the ExecStartPost that grants nginx the POSIX ACL on the
  # per-user FPM socket (#430); if it is missing the ACL never lands and
  # nginx gets "connect() ... failed (13: Permission denied)" on the socket.
  if [[ -d "$REPO_DIR/install/systemd" ]]; then
    install -d -m 0755 /usr/local/libexec/jabali
    local _h
    for _h in fpm-pre-start fpm-exec fpm-post-start cron-precheck; do
      if [[ -f "$REPO_DIR/install/systemd/$_h" ]]; then
        install -m 0755 "$REPO_DIR/install/systemd/$_h" "/usr/local/libexec/jabali/$_h"
      fi
    done
  fi

  # GH#111: install_php / install_phpmyadmin* run only in fresh-install
  # main(), so `jabali update` never installed the PHP 8.4 default nor
  # applied the phpMyAdmin DI patch — existing hosts stayed broken
  # ("ServiceNotFoundException: config", panel showing 8.4 not installed).
  # Converge them here (idempotent) so updates reach existing hosts.
  if declare -f install_phpmyadmin >/dev/null 2>&1; then
    # Ensure the configured PHP versions AND the phpMyAdmin pool version
    # (8.4) are actually installed before the configure/patch steps run
    # (_install_php_version + the pma pool _die if the binary is absent).
    # Extension list mirrors install_base_packages — keep in sync.
    local _pv
    for _pv in $(printf '%s\n' ${JABALI_PHP_VERSIONS:-8.4} 8.4 | sort -u); do
      command -v "php${_pv}" >/dev/null 2>&1 && continue
      _log "provision: php${_pv} missing — installing fpm/cli + extensions"
      local _pkgs=("php${_pv}-fpm" "php${_pv}-cli") _e
      for _e in mysql mbstring zip gd curl xml intl bcmath opcache; do
        apt-cache show "php${_pv}-${_e}" >/dev/null 2>&1 && _pkgs+=("php${_pv}-${_e}")
      done
      # Refresh the apt cache so php${_pv} from Sury is found even if
      # the index hasn't been updated recently in the provision context.
      apt-get update -qq 2>/dev/null || true
      apt-get install -y -qq --no-install-recommends "${_pkgs[@]}" \
        || _warn "provision: php${_pv} package install had issues"
    done
    declare -f install_php >/dev/null 2>&1 && install_php
    # Order matters (GH #217): phpMyAdmin must extract to
    # /opt/phpmyadmin/current BEFORE its FPM pool starts, else the pool's
    # chdir target is missing. The pool is also self-gating on that dir.
    install_phpmyadmin
    declare -f install_phpmyadmin_fpm_pool >/dev/null 2>&1 && install_phpmyadmin_fpm_pool
  fi

  # GH#114: if /etc/nginx/conf.d/jabali-bulwark-upstream.conf goes missing
  # (an update dropped it), the per-domain *-mail.conf vhosts proxy_pass to
  # an undefined `jabali_bulwark` upstream -> `nginx -t` fails with "host
  # not found in upstream" -> EVERY vhost_apply + SSL deploy fails -> all
  # certs freeze "pending". Re-drop it on update (idempotent) so one
  # missing include can't take down all of nginx + SSL, and make sure the
  # webmail service it points at is actually running.
  local _bulwark_up="${REPO_DIR:-/opt/jabali-panel}/install/nginx/jabali-bulwark-upstream.conf"
  if [[ -f "$_bulwark_up" ]] && [[ -d /opt/jabali-webmail ]]; then
    if ! cmp -s "$_bulwark_up" /etc/nginx/conf.d/jabali-bulwark-upstream.conf 2>/dev/null; then
      install -m 0644 -o root -g root "$_bulwark_up" /etc/nginx/conf.d/jabali-bulwark-upstream.conf
      _log "provision: restored jabali-bulwark-upstream.conf (GH#114)"
      if nginx -t >/dev/null 2>&1; then
        systemctl reload nginx 2>/dev/null || true
      else
        _warn "provision: nginx -t still failing after restoring bulwark upstream — check nginx config"
      fi
    fi
    systemctl is-active --quiet jabali-webmail.service 2>/dev/null \
      || systemctl restart jabali-webmail.service 2>/dev/null || true
  fi

  # Snuffleupagus: flip simulation → enforce on existing installs.
  # Fresh installs already default to enforce (install_snuffleupagus).
  local sp_mode_file="/etc/jabali/snuffleupagus/mode"
  if [[ -f "$sp_mode_file" ]] && [[ "$(cat "$sp_mode_file")" == "simulation" ]]; then
    _log "snuffleupagus: flipping simulation → enforce"
    echo "enforce" > "$sp_mode_file"
    local _sp_php_versions="${JABALI_PHP_VERSIONS:-8.4}"
    local _sp_fpm_units=()
    for _spv in $_sp_php_versions; do
      _sp_fpm_units+=("php${_spv}-fpm")
    done
    systemctl restart "${_sp_fpm_units[@]}" 2>/dev/null || true
    _ok "snuffleupagus mode set to enforce"
  fi

  # Ensure php alternatives still point at the jabali-configured version.
  # Idempotent and cheap — guards against any apt upgrade re-seeding the
  # php-cli meta-package (and its php8.4 priority-100 registration).
  local _upd_php_versions="${JABALI_PHP_VERSIONS:-8.4}"
  local _upd_primary
  _upd_primary="$(echo "$_upd_php_versions" | awk '{print $NF}')"
  for _alt in php phar php-config phpize; do
    if [[ -f "/usr/bin/${_alt}${_upd_primary}" ]]; then
      update-alternatives --set "$_alt" "/usr/bin/${_alt}${_upd_primary}" 2>/dev/null || true
    fi
  done
  # Purge any stale PHP versions not in JABALI_PHP_VERSIONS
  for _pv in 8.4 8.3 8.2 8.1 8.0 7.4; do
    if echo "$_upd_php_versions" | grep -qw "$_pv"; then continue; fi
    # In-use guard: a version with a configured FPM tree is one the panel PHP
    # Manager installed and a tenant pool/domain uses. provision_php_extensions
    # keys on this same /etc/php/<v>/fpm signal and reinstalls the ext packages,
    # so purging here just gets it reinstalled on the SAME update run — an
    # install/uninstall flap on every `jabali update`. Only pure transitive
    # php-cli pulls (no FPM tree) should be purged.
    if [[ -d "/etc/php/${_pv}/fpm" ]]; then
      _log "provision: keeping php${_pv} (configured FPM version in use)"
      continue
    fi
    if dpkg -l "php${_pv}-cli" 2>/dev/null | grep -q "^ii"; then
      # Preserve admin-installed versions (apt-manual, via the panel PHP
      # version manager) — only purge transitive/auto pulls (GH #302).
      if apt-mark showmanual "php${_pv}-cli" 2>/dev/null | grep -qx "php${_pv}-cli"; then
        _log "provision: keeping admin-installed php${_pv} (apt-manual)"
        continue
      fi
      _log "provision: purging stale php${_pv} (auto/transitive, not in JABALI_PHP_VERSIONS)"
      apt-get purge -y -qq "php${_pv}*" 2>/dev/null || true
      apt-get autoremove -y -qq 2>/dev/null || true
    fi
  done

  # AIDE: re-trigger DB init if the database is missing and no init is
  # already running. Covers hosts that had a failed or timed-out first
  # init (e.g. QA/CI environments where the background nohup was killed).
  if command -v aide >/dev/null 2>&1 \
      && [[ -f /etc/aide/aide.conf ]] \
      && [[ ! -f /var/lib/aide/aide.db ]] \
      && [[ ! -f /var/lib/aide/.init-in-progress ]]; then
    install -d -m 0750 /var/lib/aide
    touch /var/lib/aide/.init-in-progress
    _log "AIDE: DB missing — re-triggering background init (2-5 min)"
    nohup bash -c '
      /usr/bin/aide --init --config=/etc/aide/aide.conf >/var/log/aide/init.log 2>&1
      if [[ -f /var/lib/aide/aide.db.new ]]; then
        mv /var/lib/aide/aide.db.new /var/lib/aide/aide.db
        chmod 0600 /var/lib/aide/aide.db
        date -u +%Y-%m-%dT%H:%M:%SZ > /var/lib/aide/.jabali-installed
      fi
      rm -f /var/lib/aide/.init-in-progress
    ' >/dev/null 2>&1 &
  fi

  # CrowdSec collections. Guards are idempotent — safe to call even when
  # crowdsec is not yet installed (cscli exits non-zero, guards skip).
  # _cs_dirty tracks whether ANY cscli mutation happened so we end the
  # block with a `systemctl reload crowdsec` — cscli mutates the hub
  # state but does not signal crowdsec itself, and the operator was
  # left holding cscli's "Run reload" hint after every `jabali update`
  # that installed a collection. Same recurring scar class as the
  # deploy-hook / appsec-config refresh (PR #45 / #49 / #54).
  if command -v cscli >/dev/null 2>&1; then
    local _cs_dirty=0
    local _cols=(nginx sshd linux mysql)
    for _col in "${_cols[@]}"; do
      if ! cscli collections list 2>/dev/null | grep -q "crowdsecurity/${_col}"; then
        if _spin "cscli collections install ${_col}" \
             cscli collections install "crowdsecurity/${_col}"; then
          _cs_dirty=1
        fi
      fi
    done
    if [[ $_cs_dirty -eq 1 ]]; then
      _log "crowdsec: hub changed — reloading"
      systemctl reload crowdsec 2>/dev/null || systemctl restart crowdsec 2>/dev/null || true
    fi
  fi

  # CrowdSec sshd acquis: migrate stale filters to the dual-identifier
  # filter (SYSLOG_IDENTIFIER=sshd + sshd-session). Triggers on either:
  #   - legacy _SYSTEMD_UNIT=ssh filter (pre-M26 unit-name match), OR
  #   - sshd-only filter missing sshd-session (early-M26: Debian 13 OpenSSH
  #     split mode logs per-connection workers as sshd-session, so a
  #     sshd-only filter misses ~95-98% of brute-force log lines).
  # The install_crowdsec_appsec call in main() writes the correct filter
  # on fresh installs; this block patches existing hosts on jabali update.
  local _sshd_acquis="/etc/crowdsec/acquis.d/jabali-sshd.yaml"
  if [[ -f "$_sshd_acquis" ]] && \
     { grep -q "_SYSTEMD_UNIT=ssh" "$_sshd_acquis" || \
       ! grep -q "SYSLOG_IDENTIFIER=sshd-session" "$_sshd_acquis"; }; then
    _log "crowdsec: updating sshd acquis filter (sshd + sshd-session)"
    local _tmp_acquis
    _tmp_acquis="$(mktemp --tmpdir jabali-sshd-acquis.XXXXXX)"
    cat >"$_tmp_acquis" <<'EOF'
# Managed by jabali install.sh — M26 SSH brute-force detection.
# Debian 13 OpenSSH split mode: listener = sshd, per-connection worker
# = sshd-session (where Failed/Invalid/preauth events live). Repeated
# SYSLOG_IDENTIFIER= entries are OR-combined by journalctl, so both
# identifiers must be listed to catch every brute-force attempt.
source: journalctl
journalctl_filter:
  - "SYSLOG_IDENTIFIER=sshd"
  - "SYSLOG_IDENTIFIER=sshd-session"
labels:
  type: syslog
EOF
    install -m 0644 -o root -g root "$_tmp_acquis" "$_sshd_acquis"
    rm -f "$_tmp_acquis"
    systemctl reload crowdsec 2>/dev/null || systemctl restart crowdsec 2>/dev/null || true
    _ok "crowdsec sshd acquis updated — SSH brute-force detection restored"
  fi

  # M33 malware stack — re-run on every update so drop-in conf changes
  # (inotify_docroot, monitor unit, custom YARA rules) propagate to
  # existing hosts. Idempotent: marker file at
  # /usr/local/maldetect/.jabali-installed-${LMD_VERSION} short-circuits
  # the LMD tarball download; apt + drop-in steps skip when nothing
  # needs replacing.
  if declare -f install_malware_stack >/dev/null 2>&1; then
    _log "provision: re-running install_malware_stack to refresh drop-ins"
    install_malware_stack
  fi

  # stalwart-cli: historically installed only on fresh installs, so a
  # cli_version bump never reached existing hosts on `jabali update` (the CLI
  # speaks Stalwart's JMAP management API). Re-run the version-gated installer
  # here so a bump deploys. It's a no-op when already current (--version skip);
  # the subshell + `|| _warn` isolates its _die on a transient download/sha
  # failure so it can't skip the rest of provision (provision is best-effort —
  # update.go continues on a provision failure).
  if declare -f _install_stalwart_cli >/dev/null 2>&1; then
    _log "provision: ensuring stalwart-cli at the pinned version"
    ( _install_stalwart_cli ) || _warn "provision: stalwart-cli install failed (will retry next update)"
  fi

  # logrotate drop-in — refreshed every update so new log paths added in
  # later releases land on existing hosts. Cheap: cmp -s short-circuits
  # when the file is byte-identical.
  if declare -f install_logrotate >/dev/null 2>&1; then
    install_logrotate
  fi


  # crowdsec firewall-bouncer api_url heal (stale 8080 from older installs).
  if declare -f ensure_crowdsec_firewall_bouncer_lapi_url >/dev/null 2>&1; then
    ensure_crowdsec_firewall_bouncer_lapi_url
  fi

  # crowdsec nginx-bouncer api_url heal (empty -> LAPI loopback so the
  # bouncer heartbeats to console).
  if declare -f ensure_crowdsec_nginx_bouncer_lapi_url >/dev/null 2>&1; then
    ensure_crowdsec_nginx_bouncer_lapi_url
  fi

  # crowdsec bouncer poll-frequency heal (10s default -> 60s; cuts
  # sustained LAPI CPU when CAPI has many decisions loaded).
  if declare -f ensure_crowdsec_bouncer_poll_frequency >/dev/null 2>&1; then
    ensure_crowdsec_bouncer_poll_frequency
  fi

  # JAB-368: block tenant-uid access to CrowdSec's unauthenticated
  # prometheus/pprof listener (127.0.0.1:6060) at the packet layer.
  if declare -f ensure_crowdsec_diag_isolation >/dev/null 2>&1; then
    ensure_crowdsec_diag_isolation
  fi

  # Cloudflare real-IP rewrite. Without this, every CF-fronted hit logs
  # the proxy IP (172.6x.x.x, etc) as the client — breaks per-IP rate
  # limits, CrowdSec scenario matching, geoblock decisions, and the
  # CrowdSec Top sources card. Fetches current CF ranges (with fallback
  # to a Q2-2026 snapshot if offline).
  if declare -f install_cloudflare_realip >/dev/null 2>&1; then
    install_cloudflare_realip
  fi

  # Kratos↔MariaDB unix socket (M25.1) — ensure jabali user is in
  # mysql group + POSIX ACL on socket as fallback. Idempotent.
  if declare -f ensure_jabali_in_mysql_group >/dev/null 2>&1; then
    ensure_jabali_in_mysql_group
  fi
  # wp-cli: ensure the PINNED phar is present so a wp_version bump deploys on
  # update. ensure_wpcli_symlink (below) only heals a missing/dangling symlink —
  # with an intact old symlink a bumped version never downloaded, so wp stayed
  # on the old version on existing hosts (the same fresh-install-only gap as
  # stalwart-cli). install_wp_cli is version-gated (downloads only when the
  # pinned phar is absent), subshell-isolated so its _die on a transient
  # download failure can't skip the rest of provision.
  if declare -f install_wp_cli >/dev/null 2>&1; then
    ( install_wp_cli ) || _warn "provision: wp-cli install failed (will retry next update)"
  fi
  if declare -f ensure_wpcli_symlink >/dev/null 2>&1; then
    ensure_wpcli_symlink
  fi
  if declare -f ensure_snuffleupagus_bundle_synced >/dev/null 2>&1; then
    ensure_snuffleupagus_bundle_synced
  fi
  if declare -f ensure_mariadb_socket_acl_for_jabali >/dev/null 2>&1; then
    ensure_mariadb_socket_acl_for_jabali
    # Restart kratos so it picks up the ACL on its next connect
    # attempt (already-running kratos had EACCES cached at startup
    # but ping retries every few seconds, so a kick is faster than
    # waiting).
    systemctl restart jabali-kratos 2>/dev/null || true
  fi

  # CrowdSec Console — jabali no longer integrates with the cloud
  # dashboard (app.crowdsec.net). Community tier caps at 500 alerts/
  # month for the whole account which is unusable on any host that
  # takes real internet traffic, and the per-host /jabali-admin/
  # security tabs already replicate every console panel that mattered
  # (Alerts, Decisions, Blocklists, Bouncers, Alerts-over-time chart).
  #
  # cscli v1.7.8 has no `disenroll` verb — `disable -a` turns off all
  # five forwarding flags (custom/tainted/manual/context/console_
  # management), which is the only thing that actually pushes data to
  # the cloud. The engine stays bound (online_api_credentials.yaml
  # remains so CAPI blocklist pull keeps working) but no alert leaves
  # the host. Idempotent — safe to re-run every jabali update.
  if command -v cscli >/dev/null 2>&1; then
    if cscli console status -o json 2>/dev/null | grep -q '"activated":[[:space:]]*true'; then
      _log "crowdsec: disabling all console alert-forwarding flags"
      cscli console disable -a 2>/dev/null || true
      systemctl reload crowdsec 2>/dev/null || true
    fi
    rm -f /etc/jabali/.cs-console-enrolled
  fi

  # OnFailure notifier template + helper script — same logic.
  if declare -f install_notify_template >/dev/null 2>&1; then
    install_notify_template
  fi

  # M39 auditd rules — rules file write + augenrules --load. Idempotent
  # via cmp -s + 'rules unchanged' early return.
  if declare -f install_audit_exec >/dev/null 2>&1; then
    install_audit_exec
  fi

  # M40 AppArmor profiles — re-installs profile files + reloads parser.
  # Idempotent: profile compare + skip-on-identical means steady-state
  # cost is one filesystem stat per profile. Requires REPO_DIR (set by
  # clone_or_update_repo, which jabali update's full flow runs before
  # this prelude).
  if declare -f install_apparmor >/dev/null 2>&1 && [[ -d "${REPO_DIR:-/opt/jabali-panel}/install/apparmor" ]]; then
    install_apparmor
  fi

  # M42 AIDE config + systemd units — re-writes /etc/aide/aide.conf
  # + jabali-aide-check unit files when the exclude list grows. DB
  # init only fires when /var/lib/aide/aide.db is missing.
  if declare -f install_aide >/dev/null 2>&1; then
    install_aide
  fi

  # Redis multi-tenant ACL provisioning (#595 / #406 / ADR-0148). install.sh-only
  # provisioning meant existing servers never got the jabali-redis-clients group,
  # users.acl, or the JABALI_REDIS_PANEL_TOKEN / JABALI_WP_CACHE_HMAC_SECRET env —
  # so the per-app WP-cache toggle 503'd ("redis_unavailable" though Redis was up,
  # CacheTokenSecret empty). Self-heal here on every `jabali update`. The function
  # fast-returns when already converged, so steady-state cost is a few greps and
  # NO redis/panel restart. Guarded on redis-server being installed.
  if declare -f install_redis_acl >/dev/null 2>&1 && command -v redis-server >/dev/null 2>&1; then
    install_redis_acl
  fi

  # JAB-39: ensure PHP runtime extensions (incl. sqlite3/pdo_sqlite) for every
  # installed FPM version — install_base_packages doesn't run on update.
  if declare -f provision_php_extensions >/dev/null 2>&1; then
    provision_php_extensions
  fi

  # GH #346 / ADR-0042: re-apply the Stalwart JMAP plan on every `jabali update`
  # so directory config changes (queryRecipient / queryEmailAliases) actually
  # redeploy. The plan's Directory entry is an `upsert` (matchOn description), so
  # re-apply converges the query in place instead of skipping the existing object
  # (`stalwart-cli apply` create is one-shot). Guarded on a bootstrapped Stalwart
  # (cli + admin token + DB password present) so non-mail / pre-bootstrap hosts
  # skip it. Non-fatal — a failed re-apply must not abort the whole update.
  if declare -f install_stalwart_apply >/dev/null 2>&1 \
     && command -v stalwart-cli >/dev/null 2>&1 \
     && [[ -f /etc/jabali-panel/stalwart-admin.token ]] \
     && [[ -f /etc/jabali-panel/stalwart-mariadb.password ]]; then
    install_stalwart_apply || _warn "stalwart JMAP re-apply on update failed (non-fatal)"
  fi

  # Multi-tenant WP opcache tuning (#597) — self-heal on every update so the
  # stock 10000-file/128MB opcache defaults get raised on existing hosts.
  if declare -f install_php_opcache_tuning >/dev/null 2>&1; then
    install_php_opcache_tuning
  fi

  # JAB-230 — CLI mail() shim routing; self-heal on every update so existing
  # hosts' cron/wp-cli mail() converges alongside the fleet backfill.
  if declare -f install_php_cli_sendmail_path >/dev/null 2>&1; then
    install_php_cli_sendmail_path
  fi
  # JAB-230 — the shim binary itself. Closes the two-hop trap where the
  # previous panel binary's update code installs everything EXCEPT the new
  # binary it doesn't know about.
  if declare -f ensure_jabali_sendmail_binary >/dev/null 2>&1; then
    ensure_jabali_sendmail_binary
  fi

  # JAB-213 — free-hostname helper scripts + heartbeat units live under
  # install_jabali_slices, which the trimmed update path does NOT call. Without
  # this, Server-Settings activation on an UPDATED (vs freshly-installed) box
  # would set the hostname + token but silently skip the heartbeat + wildcard
  # cert. Self-heal on every update; idempotent installs.
  if [[ -d "$REPO_DIR/install/hostname" ]]; then
    install -d -m 0755 /usr/local/libexec/jabali
    install -m 0755 "$REPO_DIR/install/hostname/jabali-hostname-heartbeat.sh" /usr/local/libexec/jabali/jabali-hostname-heartbeat.sh
    install -m 0755 "$REPO_DIR/install/hostname/certbot-auth-hook.sh" /usr/local/libexec/jabali/certbot-auth-hook.sh
    install -m 0755 "$REPO_DIR/install/hostname/certbot-cleanup-hook.sh" /usr/local/libexec/jabali/certbot-cleanup-hook.sh
    install -m 0755 "$REPO_DIR/install/hostname/jabali-hostname-cert.sh" /usr/local/libexec/jabali/jabali-hostname-cert.sh
    install -m 0644 "$REPO_DIR/install/hostname/jabali-hostname-heartbeat.service" /etc/systemd/system/jabali-hostname-heartbeat.service
    install -m 0644 "$REPO_DIR/install/hostname/jabali-hostname-heartbeat.timer" /etc/systemd/system/jabali-hostname-heartbeat.timer
    systemctl daemon-reload >/dev/null 2>&1 || true
    if [[ -r /etc/jabali-panel/hostname.env ]]; then
      systemctl enable --now jabali-hostname-heartbeat.timer >/dev/null 2>&1 || true
    fi
    _ok "JAB-213 free-hostname helpers refreshed"
  fi

  # MariaDB buffer-pool sizing (#597) — reconcile the RAM-scaled drop-in on every
  # update so existing hosts pick up the raised brackets. Idempotent + restart-
  # on-change; guarded on mariadb being installed.
  if declare -f tune_mariadb_for_ram >/dev/null 2>&1 && command -v mariadb >/dev/null 2>&1; then
    tune_mariadb_for_ram
  fi

  # Stalwart memory bounds (JAB-216) — self-heal on every update so hosts
  # installed before this existed stop running an unbounded mail server. This
  # is the fix for a real wedge, so existing fleets are exactly who needs it.
  # Non-fatal: a limit that cannot be applied must not abort an update.
  if declare -f bound_stalwart_memory >/dev/null 2>&1; then
    bound_stalwart_memory || _warn "stalwart memory bounds not applied (non-fatal)"
  fi

}

# apply_dns_forwarder_override switches the host off systemd-resolved
# and onto a plain /etc/resolv.conf with `options use-vc` (force TCP)
# pointing at the operator-supplied JABALI_DNS_FORWARDER. Required on
# restricted-network labs where outbound UDP/53 is dropped but TCP/53
# to a LAN resolver still works (corporate, VirtualBox NAT with
# DNS-proxy quirks, lab firewalls). No-op when the env is unset.
#
# The chattr +i is load-bearing: several apt postinst scripts (libnss-
# resolve, openresolv, resolvconf) run `ln -sf /run/systemd/resolve/
# stub-resolv.conf /etc/resolv.conf` and propagate the failure as a
# package-install failure under set -e. Immutable defeats the rename
# silently — apt logs a non-fatal warning and moves on.
apply_dns_forwarder_override() {
  [[ -z "$DNS_FORWARDER" ]] && return 0
  _log "DNS forwarder mode: ${DNS_FORWARDER} (UDP/53 outbound assumed blocked; falling back to plain resolv.conf + TCP)"

  if systemctl list-unit-files systemd-resolved.service >/dev/null 2>&1; then
    systemctl stop systemd-resolved 2>/dev/null || true
    systemctl mask systemd-resolved 2>/dev/null || true
  fi
  chattr -i /etc/resolv.conf 2>/dev/null || true
  rm -f /etc/resolv.conf
  cat > /etc/resolv.conf <<EOF
# Managed by jabali install.sh (JABALI_DNS_FORWARDER=${DNS_FORWARDER}).
# systemd-resolved is masked to keep package postinst from re-symlinking
# this file to the stub. \`options use-vc\` forces glibc onto TCP/53 — the
# lab firewall blocks UDP/53 outbound but TCP works.
nameserver ${DNS_FORWARDER}
options use-vc timeout:5 attempts:2
EOF
  chattr +i /etc/resolv.conf 2>/dev/null || true
  if ! getent hosts deb.debian.org >/dev/null 2>&1; then
    _die "DNS forwarder ${DNS_FORWARDER} did not resolve deb.debian.org over TCP — check the forwarder is reachable on TCP/53"
  fi
  _ok "DNS forwarder ${DNS_FORWARDER} live via TCP/53 (systemd-resolved masked, /etc/resolv.conf immutable)"
}

# apply_system_hostname applies the operator-supplied hostname (--hostname /
# JABALI_HOSTNAME) at the OS layer. Called as the FIRST action in main() so every
# downstream step (certs, mail config, DNS, the panel identity bound to the
# hostname) sees the correct system hostname. No-op when the hostname is being
# prompted interactively (JABALI_HOSTNAME unset) — prompt_server_settings sets it
# after the prompt in that case. Idempotent.
apply_system_hostname() {
  [[ -z "${JABALI_HOSTNAME:-}" ]] && return 0
  local _re='^[a-zA-Z0-9][a-zA-Z0-9.-]*[a-zA-Z0-9]$'
  [[ "$JABALI_HOSTNAME" =~ $_re ]] || _die "invalid JABALI_HOSTNAME: '$JABALI_HOSTNAME' (use letters/digits/dots/hyphens)"
  local _cur
  _cur="$(hostname 2>/dev/null || echo '')"
  if [[ "$_cur" != "$JABALI_HOSTNAME" ]]; then
    _log "setting system hostname: ${_cur:-<none>} -> $JABALI_HOSTNAME"
    hostnamectl set-hostname "$JABALI_HOSTNAME" 2>/dev/null || \
      _warn "hostnamectl set-hostname failed (container without CAP_SYS_ADMIN?) — /etc/hostname may be stale"
  fi
}

# check_ptr_rdns compares the machine's reverse DNS (PTR) for its public IPv4
# against the server hostname and WARNS at the end of the install if they don't
# match. A matching PTR (rDNS) is required for reliable mail delivery — most
# receivers reject or spam-fold mail from IPs whose PTR doesn't match the sending
# hostname. The installer can't set rDNS (it is delegated to the IP owner), so
# this is advisory: the operator must set it at their IP / hosting provider.
check_ptr_rdns() {
  local want ip ptr
  want="${JABALI_HOSTNAME:-$(hostname -f 2>/dev/null || hostname 2>/dev/null || echo '')}"
  [[ -z "$want" ]] && return 0
  ip="$(_detect_public_ipv4 2>/dev/null || true)"
  [[ -z "$ip" ]] && return 0
  if command -v dig >/dev/null 2>&1; then
    ptr="$(dig +short -x "$ip" 2>/dev/null | head -n1 | sed 's/\.$//')"
  elif command -v host >/dev/null 2>&1; then
    ptr="$(host "$ip" 2>/dev/null | awk '/domain name pointer/{print $NF}' | head -n1 | sed 's/\.$//')"
  else
    _log "skipping reverse-DNS (PTR) check — no dig/host available"
    return 0
  fi
  echo ""
  if [[ -z "$ptr" ]]; then
    _warn "Reverse DNS (PTR) for $ip is NOT set. For reliable mail delivery, set the PTR / rDNS of $ip to '$want' at your IP / hosting provider."
  elif [[ "${ptr,,}" != "${want,,}" ]]; then
    _warn "Reverse DNS (PTR) MISMATCH: $ip -> '$ptr', but the server hostname is '$want'. For reliable mail delivery, set the PTR / rDNS of $ip to '$want' at your IP / hosting provider."
  else
    _ok "Reverse DNS (PTR) matches hostname: $ip -> $ptr"
  fi
}

main() {
  print_banner
  # GH #760: default a fresh, no-selection install to the lean "webhost" profile
  # (mail/docker/python/postgres/api opt-in). Must run BEFORE the dry-run print
  # so the plan shows the real default. No-op when JABALI_MODULES is set.
  apply_default_modules
  # --dry-run: print the module plan (which optional modules install vs skip for
  # the current JABALI_MODULES) and exit before any system change. M353 GH #353.
  if [[ -n "${_cli_dry_run:-}${JABALI_DRY_RUN:-}" ]]; then
    print_module_plan
    exit 0
  fi
  # Set the system hostname up front so every downstream step (certs, mail,
  # DNS, panel identity) uses it.
  apply_system_hostname
  preflight
  # Before ANY download lands in /tmp (Go toolchain, wp-cli, phpMyAdmin,
  # Kratos, Stalwart): make sure the kernel refuses to follow a symlink an
  # unprivileged user planted at one of those predictable root-owned paths.
  ensure_tmp_hardening
  # Swap MUST land before install_base_packages — apt + CrowdSec hub
  # downloads + npm ci all pull 100MB+ into RAM, OOM-killing each
  # other on a 2 GB VPS. ensure_swap is idempotent + cheap on hosts
  # with enough RAM.
  ensure_swap
  prompt_server_settings
  # DNS forwarder escape hatch — must land BEFORE install_base_packages
  # so the first apt update / GPG-key curl already runs against the
  # static /etc/resolv.conf instead of failing on systemd-resolved's
  # blocked UDP/53 path. No-op when JABALI_DNS_FORWARDER is unset, so
  # production installs see no behavioural change.
  apply_dns_forwarder_override
  prompt_admin_account
  install_base_packages
  # NTP / time sync — must run before anything that depends on accurate
  # wall-clock (TOTP enrolment, JWT/cookie expiry, certbot timestamps).
  install_time_sync
  # M25 step 1: kill the LLMNR :5355 listener once systemd-resolved is on
  # the host. Drop-in only — operator can override later.
  disable_llmnr
  # M18 — resource-limits prerequisites. cgroups v2 probe FIRST (fails
  # loud if misconfigured; every subsequent slice we ever emit depends
  # on unified hierarchy). Disk quota and /tmp tmpfs are both
  # idempotent and warn-and-skip on unsupported filesystems.
  # DNS is deliberately left alone at install time (see the block
  # following install_base_packages for rationale).
  configure_cgroups_v2
  run_if_module quota configure_disk_quota
  configure_tmp_tmpfs
  install_nginx
  install_php
  install_disabled_page
  install_node
  install_go
  ensure_user_and_dirs
  # Add swap (<=4 GB host) BEFORE any daemon starts. Without swap on
  # a 3.8 GB box the kernel OOM-killer eats mariadbd as soon as the
  # full daemon set loads (NRestarts=2 caught on puzzle 2026-06-05).
  # ensure_swap is called again by build_frontend later -- both calls
  # are idempotent.
  ensure_swap
  # Order matters: write_env_file seeds PANEL_ADDR / PANEL_ENV / JWT_SECRET
  # hooks BEFORE provision_mariadb appends DATABASE_URL. Reversing the two
  # would leave a fresh install with only the DB URL and no server config.
  write_env_file
  provision_mariadb
  install_mariadb_skip_networking
  tune_mariadb_for_ram
  install_php_opcache_tuning   # #597: opcache defaults for multi-tenant WP
  install_php_cli_sendmail_path  # JAB-230: CLI mail() through the shim
  # M48 Phase 8 (opt-in): install_docker_engine no longer runs on
  # fresh install. Operator flips server_settings.docker_marketplace_enabled
  # in Server Settings; panel-api dispatches docker.install which
  # sources install.sh and runs install_docker_engine on demand.
  install_redis
  install_redis_acl
  # M37 Phase 4: PostgreSQL is OPT-IN. install_postgres no longer runs on
  # fresh install. Operator flips server_settings.postgres_enabled in
  # the Databases tab; panel-api dispatches db.postgres.install which
  # sources install.sh and runs install_postgres on demand.
  # Mask/unmask pdns units to match the DNS module state BEFORE the config
  # step runs (unmask must precede install_powerdns starting pdns). GH #447.
  converge_pdns_masking
  run_if_module dns install_powerdns
  run_if_module dns bootstrap_pdns_self_zone
  # M6.3: recursor owns loopback :53 and forwards panel-authoritative zones
  # into pdns-server at :5300. Must run AFTER bootstrap_pdns_self_zone (the
  # self-zone has to exist in pdns before the recursor's post-install probe
  # tries to resolve it) and BEFORE setup_certbot (certbot's HTTP-01 flow
  # needs the panel's own hostname to resolve locally).
  run_if_module dns install_pdns_recursor
  setup_certbot
  # M26 Step 1 — security foundation. Wired here (after pdns/certbot,
  # before clone_or_update_repo and the long build_frontend / npm steps)
  # because:
  #   - All apt packages (crowdsec, ufw, yq) land in the install_base_packages
  #     batch above; the firewall bouncer is detected at runtime here.
  #   - CrowdSec LAPI binds on a Unix socket (ADR-0050) — must be
  #     configured BEFORE install_stalwart so it doesn't race Stalwart
  #     pinning 127.0.0.1:8080.
  #   - UFW activates with the SSH + panel + nginx + mail allow-list
  #     BEFORE Stalwart's first bind (avoids documented iptables-reload
  #     race) AND BEFORE build_frontend (so an interrupted build
  #     doesn't strand the host without a firewall).
  #   - cleanup_modsecurity removes the M26 ModSecurity stack on existing
  #     hosts that ran an earlier install (ADR-0055 superseded 2026-04-26).
  run_if_module security install_crowdsec
  run_if_module security configure_crowdsec_mariadb
  run_if_module security install_crowdsec_appsec
  run_if_module security install_crowdsec_nginx_bouncer
  run_if_module security install_crowdsec_profiles
  run_if_module security install_login_allowlist_default_conf
  run_if_module security install_crowdsec_jabali_scenarios
  # The stalwart + kratos scenario installers are NOT here with their sibling:
  # unlike install_crowdsec_jabali_scenarios, which carries its YAML inline,
  # both copy from $REPO_DIR/install/crowdsec/, so they have to wait for
  # clone_or_update_repo. They run just after it.
  run_if_module security install_crowdsec_blocklists
  cleanup_modsecurity
  run_if_module security install_malware_stack
  # UFW is part of the security suite (it fronts the CrowdSec firewall bouncer).
  # A Minimal install opted out of security, so don't enable an unprompted
  # default-deny firewall — gate it on the security module.
  run_if_module security install_ufw
  install_per_user_egress
  install_goaccess
  install_restart_drop_ins
  clone_or_update_repo
  # install_apparmor and install_aide run AFTER clone_or_update_repo because
  # both functions source profile/unit files from $REPO_DIR/install/; on a
  # fresh install that directory does not exist until the repo is cloned.
  #
  # install_logrotate and install_notify_template belong to that same group and
  # used to run just above the clone, where $REPO_DIR/install/ cannot exist yet
  # on a fresh host. Both source-check and return 0 with only a _warn, so the
  # install stayed green while silently shipping neither file.
  run_if_module security install_apparmor
  run_if_module security install_aide
  install_logrotate
  install_notify_template
  run_if_module dns install_pdns_local_address_helper
  run_if_module security install_crowdsec_jabali_stalwart_scenarios
  run_if_module security install_crowdsec_jabali_kratos_scenarios
  install_snuffleupagus
  protect_panel_docs
  # M25: source the socket-helper definitions now that the repo's install/
  # tree is on disk. Steps 2–5 will call verify_socket_perms /
  # verify_no_all_interface_binds after each service-bind change. Sourced
  # here (not earlier) because under `curl | bash` the install/scripts/
  # tree doesn't exist until clone_or_update_repo populates $REPO_DIR.
  # shellcheck source=install/scripts/socket-helpers.sh
  source "$REPO_DIR/install/scripts/socket-helpers.sh"
  # M25: bring the jabali-sockets group into existence. SERVICE_USER and
  # www-data already exist by now; jabali-webmail is created later by
  # install_bulwark — a second call after that picks it up. The function
  # is idempotent so repeating it is cheap.
  ensure_jabali_sockets_group
  install_jabali_slices
  install_kratos
  install_php_pool_template
  run_if_module python_apps install_python_apps_runtime
  build_frontend
  build_backend
  # Re-run AppSec wiring now that build_backend produced
  # /usr/local/bin/jabali-panel. On a FRESH install the earlier
  # install_crowdsec_appsec call (above, pre-clone) skipped the acquis
  # because the binary didn't exist to render the config (GH#109 fix).
  # This second call renders the config + writes the acquis + reloads
  # crowdsec with AppSec live. Idempotent (write-on-diff) so on a
  # re-install where AppSec is already wired it's a cheap no-op.
  run_if_module security install_crowdsec_appsec
  write_config_file
  provision_tls_cert
  bootstrap_panel_acme_webroot
  install_jabali_panel_cert_hook
  seed_admin_env
  bootstrap_tenant_env
  install_sso_key
  install_sso_reaper_timer
  install_cache_doctor_timer
  install_migration_secrets_reaper
  install_journald_cap
  install_disk_maintenance_timer
  install_retention_sweep_timer
  install_ssh_sandbox_prereqs
  install_backup_foundation
  # JAB-273: give the box swap headroom (no more kswapd death-spiral) and
  # contain every all-tenant maintenance job in a memory-capped slice. Both run
  # again from provision_new_software so the existing fleet self-heals on update.
  ensure_fleet_swap
  ensure_maintenance_isolation
  # Order matters: install_phpmyadmin extracts the tarball to
  # /opt/phpmyadmin/current, which the pma pool config references as
  # chdir=. Starting the FPM service before the tarball is extracted
  # causes FPM to fail with "chdir path does not exist".
  install_phpmyadmin
  install_phpmyadmin_fpm_pool
  install_adminer
  install_wp_cli
  install_sftp_group
  install_sftp_sshd_config
  # JAB-352: disable SSH forwarding for hosting users so a tenant key can't
  # tunnel into loopback-only services.
  ensure_ssh_forwarding_lockdown
  install_ssh_sandbox
  build_default_nspawn_image
  install_nginx_default_vhost
  # WebSocket map snippet — must be installed BEFORE any vhost references
  # $connection_upgrade, since nginx -t will fail otherwise.
  install_nginx_websocket_map
  install_nginx_fastcgi_cache
  install_nginx_ssl_hardening
  install_nginx_server_names_hash
  harden_proc_hidepid
  install_nginx_tunables
  # M25 Step 4: install the nginx vhost on :8443 that terminates TLS and
  # proxies to the panel-api Unix socket. Runs AFTER install_nginx_default_vhost
  # so the http{} context (defined by Debian's stock nginx.conf) and the
  # default vhost are already in place; runs BEFORE write_systemd_unit so
  # nginx -t doesn't have to wait on panel-api startup.
  install_nginx_panel_vhost
  write_agent_systemd_unit
  install_wp_purge_spool
  write_systemd_unit
  start_and_verify_agent
  start_and_verify
  # M353 (GH #353): seed the per-module server_settings flags from the install
  # selection, so the panel hides the pages + 409s the endpoints for whatever
  # wasn't installed. Runs AFTER start_and_verify (first boot migrated the schema
  # + seeded the server_settings row). Only acts when JABALI_MODULES is set; a
  # plain install leaves every flag default-on, so existing flows are unchanged.
  seed_module_flags
  # GH #1240: optional opt-in for automatic daily local backups (same point —
  # the server_settings row exists after first boot). Off unless chosen.
  seed_default_local_backups
  # First-phase Stalwart bootstrap (binary download, service user,
  # stalwart-cli, admin token, MariaDB password file, apply plan render,
  # unit file install). Safe to run after start_and_verify — doesn't
  # depend on the panel being up, just on the repo being cloned.
  run_if_mail install_stalwart
  # Second-phase Stalwart bootstrap: needs jabali_panel.{mailboxes,domains}
  # to exist, which the panel service creates via migration 000054 on its
  # first start (inside start_and_verify). Must run after, never before.
  run_if_mail install_stalwart_apply
  # GH #233 / ADR-0143: loopback MTA-hook that appends disclaimers by
  # rewriting the MIME body. Needs the stalwart-ro DB creds + jabali_panel
  # schema (install_stalwart_apply) to be in place first.
  run_if_mail install_jabali_mailhook
  # M6.4 (ADR-0048): auto-register the panel hostname as an email-enabled
  # domain. Ordering: after start_and_verify (admin user exists via
  # BootstrapAdmin) AND after bootstrap_pdns_self_zone (pdns zone row
  # exists — FK-asserted inside install_panel_primary_domain) AND after
  # install_stalwart_apply (Stalwart ready to accept the domain-add
  # command the reconciler will fire).
  run_if_module dns install_panel_primary_domain
  # Bulwark webmail. Part of the mail stack (JMAP client to Stalwart) — needs
  # the Stalwart admin token + a live JMAP backend, so gate it on the mail
  # module. Without mail there's no webmail to install.
  run_if_mail install_bulwark
  # M25: jabali-webmail user now exists; second pass over the socket group
  # picks it up. Idempotent for SERVICE_USER + www-data which were added
  # earlier (post clone_or_update_repo).
  ensure_jabali_sockets_group
  seed_last_built_sha
  _ok "jabali-panel + jabali-agent installed. Status:"
  _ok "  systemctl status $AGENT_SERVICE_NAME"
  _ok "  systemctl status $SERVICE_NAME"

  # Display credentials if this was a fresh install.
  if [[ -n "${JABALI_SEED_EMAIL:-}" ]]; then
    # The panel is reached via nginx on :8443 (TLS); PANEL_ADDR is the
    # internal unix socket and must NOT be shown to the operator.
    local panel_host panel_url
    panel_host="${JABALI_HOSTNAME:-$(hostname -f 2>/dev/null || hostname 2>/dev/null || echo localhost)}"
    panel_url="https://${panel_host}:8443/"

    # Plain ASCII, left-aligned, no right border — box-drawing glyphs are
    # multi-byte so printf %-Ns padding (counts bytes) drifts the border
    # with value length. This can't misalign and is log-safe.
    echo ""
    echo "============================================================"
    echo "  JABALI PANEL  —  installed"
    echo "============================================================"
    echo ""
    printf "  URL:       %s\n" "$panel_url"
    printf "  Username:  %s\n" "$(_derive_admin_username "$JABALI_SEED_EMAIL")"
    printf "  Password:  %s\n" "$JABALI_SEED_PASS"
    echo ""
    echo "  > Log in at the URL above using the HOSTNAME, not the server IP."
    echo "    The identity provider is bound to the hostname, so signing in via"
    echo "    the raw IP fails with \"could not reach identity service\"."
    echo "  > Change this password immediately after first login."
    echo "  > Panel is behind nginx on :8443 (HTTPS). Until a"
    echo "    Let's Encrypt cert issues for ${panel_host}, the"
    echo "    browser will warn about the self-signed fallback."
    echo "============================================================"
    echo ""
  fi

  # Runtime canary — verify the D-Bus + systemd-transient-unit capabilities
  # cron and backups depend on, so a broken minimal image is caught at
  # install time rather than via a confusing bug report later (GH #296).
  verify_runtime_health

  # End-of-install advisory: warn if reverse DNS (PTR) does not match the
  # hostname (mail deliverability).
  check_ptr_rdns
}

# ---------- uninstall flow --------------------------------------------------
# `install.sh --uninstall` tears down everything the installer creates, in
# roughly the reverse order of main(). Best-effort: every step uses `|| true`
# so a partial install (install failed mid-way) can still be cleaned up.
# OS packages are left installed by default; pass --purge-packages to also
# purge them (prompts interactively, or pair with --yes to auto-proceed).
# Destructive prompts (drop databases, remove /home users)
# ask for explicit confirmation unless --yes is given.
uninstall() {
  [[ $EUID -eq 0 ]] || { printf 'install.sh --uninstall: must run as root\n' >&2; exit 1; }

  cat <<'EOF'

============================================================
  JABALI UNINSTALL
============================================================
This will remove:
  • jabali-* systemd units and their drop-ins
  • drop-ins on crowdsec, redis-server, mariadb (jabali-written only)
  • /usr/local/bin/{jabali,jabali-panel,jabali-agent,kratos,stalwart,stalwart-cli,wp,yr,jabali-notify-onfailure}
  • /usr/local/libexec/jabali/
  • /etc/jabali-panel/, /etc/jabali/, /etc/stalwart/
  • /etc/profile.d/jabali-go.sh, /etc/apt/sources.list.d/sury-php.list + crowdsec.list
  • /etc/sysctl.d/60-jabali-malware.conf, /etc/sysctl.d/60-jabali-tmp-hardening.conf, /etc/nftables.d/jabali-per-user-egress{,-boot}.nft
  • /etc/crowdsec/acquis.d/jabali-*.yaml, /etc/crowdsec/appsec-configs/jabali-appsec.yaml
  • /etc/audit/rules.d/jabali-exec.rules
  • AppArmor profiles for jabali daemons + stalwart-mail
  • PHP snuffleupagus ini files for every installed PHP minor
  • /var/lib/jabali-*, /var/lib/stalwart, /run/jabali*, /var/lib/aide/, /var/log/aide/
  • /opt/jabali/, /opt/jabali-panel, /opt/jabali-webmail, /opt/stalwart, /opt/phpmyadmin
  • /usr/local/maldetect/, /var/log/maldet/, /var/lib/jabali-backups/
  • /var/lib/jabali-uploads/, /var/lib/jabali-migrations/
  • systemd-resolved + pdns-recursor jabali drop-ins
  • /etc/ssh/sshd_config.d/jabali-sftp.conf
  • jabali PHP-FPM pools
  • system accounts: jabali, jabali-mail, jabali-webmail, stalwart
  • system groups:  jabali, jabali-mail, jabali-webmail, jabali-sftp

Will ASK before:
  • dropping MariaDB databases (jabali_panel, jabali_pdns, jabali_kratos, stalwart_smtp)
  • removing user home directories under /home/

Will NOT remove apt packages (nginx, mariadb-server, pdns, php, node, …).

EOF

  if [[ "${_cli_yes:-}" != "1" ]]; then
    read -rp "Proceed with uninstall? [y/N]: " ans
    [[ "${ans:-}" =~ ^[yY] ]] || { _log "cancelled"; exit 0; }
  fi

  _log "stopping jabali services"
  local svc
  for svc in \
    jabali-panel.service \
    jabali-agent.service \
    jabali-kratos.service \
    jabali-stalwart.service \
    jabali-webmail.service \
    jabali-sso-reaper.timer \
    jabali-sso-reaper.service \
    jabali-maldet-monitor.service \
    jabali-maldet-update-signatures.timer \
    jabali-maldet-update-signatures.service \
    jabali-signature-base-update.timer \
    jabali-signature-base-update.service \
    jabali-maldet-scan-daily.timer \
    jabali-maldet-scan-daily.service \
    jabali-malware-quarantine-purge.timer \
    jabali-malware-quarantine-purge.service \
    jabali-per-user-egress-flip.timer \
    jabali-per-user-egress-flip.service \
    jabali-per-user-egress-load.service \
    jabali-aide-check.timer \
    jabali-aide-check.service \
    jabali-crowdsec-hub-refresh.timer \
    jabali-crowdsec-hub-refresh.service \
    jabali-notify@.service; do
    systemctl stop    "$svc" 2>/dev/null || true
    systemctl disable "$svc" 2>/dev/null || true
  done

  # All jabali-fpm@<user>.service instances (per-user slices).
  local unit
  while read -r unit; do
    [[ -n "$unit" ]] || continue
    systemctl stop    "$unit" 2>/dev/null || true
    systemctl disable "$unit" 2>/dev/null || true
  done < <(systemctl list-units --type=service --all --no-legend 'jabali-fpm@*.service' 2>/dev/null | awk '{print $1}')

  _log "removing jabali systemd unit files + drop-ins"
  rm -f  /etc/systemd/system/jabali-panel.service
  rm -f  /etc/systemd/system/jabali-agent.service
  rm -f  /etc/systemd/system/jabali-kratos.service
  rm -f  /etc/systemd/system/jabali-stalwart.service
  rm -f  /etc/systemd/system/jabali-webmail.service
  rm -f  /etc/systemd/system/jabali-sso-reaper.service
  rm -f  /etc/systemd/system/jabali-sso-reaper.timer
  rm -f  /etc/systemd/system/jabali-fpm@.service
  rm -f  /etc/systemd/system/jabali.slice
  rm -f  /etc/systemd/system/jabali-user.slice
  rm -rf /etc/systemd/system/jabali-fpm@*
  rm -rf /etc/systemd/system/jabali-panel.service.d
  rm -rf /etc/systemd/system/jabali-agent.service.d
  # jabali drops 10-jabali-restart.conf into third-party units too
  # (nginx, crowdsec, redis-server, mariadb). Sweep every one, drop
  # now-empty *.service.d dirs, reload so a leftover drop-in can't keep
  # a third-party unit pinned to jabali restart policy after uninstall.
  find /etc/systemd/system -maxdepth 2 -type f -name '10-jabali-restart.conf' -delete 2>/dev/null || true
  find /etc/systemd/system -maxdepth 1 -type d -name '*.service.d' -empty -delete 2>/dev/null || true
  systemctl daemon-reload 2>/dev/null || true
  rm -f  /etc/systemd/system/jabali-maldet-monitor.service
  rm -f  /etc/systemd/system/jabali-maldet-update-signatures.service
  rm -f  /etc/systemd/system/jabali-maldet-update-signatures.timer
  rm -f  /etc/systemd/system/jabali-signature-base-update.service
  rm -f  /etc/systemd/system/jabali-signature-base-update.timer
  # Only the file — /usr/local/libexec/jabali is shared with the pdns
  # local-address helper, so removing the directory would break DNS.
  rm -f  /usr/local/libexec/jabali/net-retry
  rm -f  /etc/systemd/system/jabali-maldet-scan-daily.service
  rm -f  /etc/systemd/system/jabali-maldet-scan-daily.timer
  rm -f  /etc/systemd/system/jabali-malware-quarantine-purge.service
  rm -f  /etc/systemd/system/jabali-malware-quarantine-purge.timer
  rm -f  /etc/systemd/system/jabali-per-user-egress-load.service
  rm -f  /etc/systemd/system/jabali-per-user-egress-flip.service
  rm -f  /etc/systemd/system/jabali-per-user-egress-flip.timer
  rm -f  /etc/systemd/system/jabali-aide-check.service
  rm -f  /etc/systemd/system/jabali-aide-check.timer
  rm -f  /etc/systemd/system/jabali-notify@.service
  rm -f  /etc/systemd/system/jabali-firehol-blocklists.service
  rm -f  /etc/systemd/system/jabali-firehol-blocklists.timer
  rm -f  /usr/local/bin/jabali-fetch-firehol-blocklists

  # Drop-ins on shared system services — remove only the files WE wrote.
  rm -f /etc/systemd/system/pdns-recursor.service.d/10-jabali-after.conf
  rm -f /etc/systemd/system/pdns-recursor.service.d/20-jabali-old-settings.conf
  rmdir --ignore-fail-on-non-empty /etc/systemd/system/pdns-recursor.service.d 2>/dev/null || true
  rm -f /etc/systemd/system/systemd-resolved.service.d/10-jabali-after.conf
  rmdir --ignore-fail-on-non-empty /etc/systemd/system/systemd-resolved.service.d 2>/dev/null || true
  rm -f /etc/systemd/resolved.conf.d/jabali.conf
  rm -f /etc/systemd/resolved.conf.d/zz-jabali-recursor.conf
  rm -f /etc/systemd/system/crowdsec.service.d/10-jabali-socket.conf
  rmdir --ignore-fail-on-non-empty /etc/systemd/system/crowdsec.service.d 2>/dev/null || true
  rm -f /etc/systemd/system/redis-server.service.d/10-jabali-socket.conf
  rmdir --ignore-fail-on-non-empty /etc/systemd/system/redis-server.service.d 2>/dev/null || true
  rm -f /etc/mysql/mariadb.conf.d/99-jabali-skip-networking.cnf

  systemctl daemon-reload 2>/dev/null || true

  # GH #690: purge jabali AppArmor profiles — unload from the kernel, delete the
  # profile files + any disable symlinks, so an uninstall leaves no jabali MAC
  # policy loaded or on disk.
  if command -v apparmor_parser >/dev/null 2>&1; then
    for _prof in /etc/apparmor.d/usr.local.bin.jabali-* \
                 /etc/apparmor.d/usr.local.bin.stalwart-mail \
                 /etc/apparmor.d/usr.local.libexec.jabali.* \
                 /etc/apparmor.d/jabali-ssh-shell; do
      [[ -e "$_prof" ]] || continue
      apparmor_parser -R "$_prof" 2>/dev/null || true
    done
  fi
  rm -f /etc/apparmor.d/usr.local.bin.jabali-* \
        /etc/apparmor.d/usr.local.bin.stalwart-mail \
        /etc/apparmor.d/usr.local.libexec.jabali.* \
        /etc/apparmor.d/jabali-ssh-shell 2>/dev/null || true
  rm -f /etc/apparmor.d/disable/usr.local.bin.jabali-* \
        /etc/apparmor.d/disable/usr.local.bin.stalwart-mail \
        /etc/apparmor.d/disable/usr.local.libexec.jabali.* 2>/dev/null || true
  _ok "removed jabali AppArmor profiles"

  # Restart shared services so they re-read without jabali drop-ins.
  systemctl restart systemd-resolved 2>/dev/null || true
  systemctl restart pdns-recursor    2>/dev/null || true
  systemctl restart mariadb          2>/dev/null || true
  systemctl restart redis-server     2>/dev/null || true

  # DNS safety net — without it the host can lose internet access.
  # /etc/resolv.conf likely still points at /run/systemd/resolve/stub-
  # resolv.conf, which forwards to a recursor we just stopped + a
  # systemd-resolved with no DNS= (we removed our drop-in). Probe a
  # known-good hostname; if it fails, replace /etc/resolv.conf with a
  # static Cloudflare + Quad9 fallback so the operator's shell still
  # works post-uninstall. Operator can put their own DNS back later.
  if ! getent hosts cloudflare.com >/dev/null 2>&1; then
    _warn "DNS broken post-uninstall — writing static fallback to /etc/resolv.conf"
    # If the path is a symlink (resolved stub), unlink it before writing.
    [[ -L /etc/resolv.conf ]] && rm -f /etc/resolv.conf
    cat > /etc/resolv.conf <<'RESOLV'
# Restored by jabali install.sh --uninstall.
# systemd-resolved no longer has jabali drop-ins, recursor is gone.
# Replace these with your operator-managed resolvers when ready.
nameserver 1.1.1.1
nameserver 9.9.9.9
options edns0
RESOLV
    chmod 0644 /etc/resolv.conf
    _ok "DNS fallback written — host now resolves via Cloudflare + Quad9"
  fi

  _log "removing binaries"
  # Glob jabali-* so helper scripts (jabali-goaccess-generator,
  # jabali-spam-rules-refresh, jabali-ssh-shell, jabali-nspawn-enter,
  # jabali-notify-onfailure, ...) are swept too — the old enumerated
  # list missed them. Non-jabali-prefixed binaries stay explicit.
  rm -f /usr/local/bin/jabali \
        /usr/local/bin/jabali-* \
        /usr/local/bin/kratos \
        /usr/local/bin/stalwart \
        /usr/local/bin/stalwart-cli \
        /usr/local/bin/wp \
        /usr/local/bin/yr
  rm -rf /usr/local/libexec/jabali

  _log "removing config files"
  rm -rf /etc/jabali-panel
  rm -rf /etc/jabali
  rm -rf /etc/stalwart
  rm -f  /etc/nginx/conf.d/jabali-pma-logformat.conf
  rm -f  /etc/logrotate.d/jabali
  rm -f  /etc/ssh/sshd_config.d/jabali-sftp.conf
  rm -f  /etc/sudoers.d/jabali-nspawn
  rm -f  /usr/local/bin/jabali-ssh-shell /usr/local/bin/jabali-nspawn-enter
  rm -rf /var/lib/jabali-nspawn
  rm -f  /etc/profile.d/jabali-go.sh
  rm -f  /etc/profile.d/jabali-php-cli.sh
  rm -f  /etc/apt/sources.list.d/sury-php.list
  rm -f  /usr/share/keyrings/sury-php.gpg
  rm -f  /etc/apt/apt.conf.d/98-jabali-sury-ua.conf
  rm -f  /etc/apt/sources.list.d/crowdsec.list
  rm -f  /etc/apt/keyrings/crowdsec.gpg
  rm -f  /etc/sysctl.d/60-jabali-malware.conf
  rm -f  /etc/sysctl.d/60-jabali-tmp-hardening.conf
  sysctl --system >/dev/null 2>&1 || true
  rm -f  /etc/nftables.d/jabali-per-user-egress.nft
  rm -f  /etc/nftables.d/jabali-per-user-egress-boot.nft
  rm -f  /etc/crowdsec/appsec-configs/jabali-appsec.yaml
  rm -f  /etc/crowdsec/acquis.d/jabali-appsec.yaml
  rm -f  /etc/crowdsec/acquis.d/jabali-nginx-logs.yaml
  rm -f  /etc/crowdsec/acquis.d/jabali-sshd.yaml
  rm -f  /etc/audit/rules.d/jabali-exec.rules
  augenrules --load >/dev/null 2>&1 || true
  rm -rf /etc/goaccess
  rm -rf /etc/redis/redis.conf.d
  # Remove the include line we appended to redis.conf.
  if [[ -f /etc/redis/redis.conf ]]; then
    sed -i '/# Added by jabali install\.sh — load drop-ins\./d' /etc/redis/redis.conf
    sed -i '/include \/etc\/redis\/redis\.conf\.d\/\*\.conf/d'  /etc/redis/redis.conf
  fi
  rm -rf /usr/share/jabali
  # Validate sshd now that our drop-in is gone — best-effort.
  sshd -t 2>/dev/null && systemctl reload ssh 2>/dev/null || true

  # Sweep jabali-specific log files outside the dirs we already rm -rf'd.
  rm -f /var/log/php-fpm-pma.log* 2>/dev/null || true
  rm -f /var/log/php-fpm-shukivaknin.log* 2>/dev/null || true
  rm -f /var/log/jabali-update.log* 2>/dev/null || true

  _log "removing PHP-FPM jabali pools"
  local pdir poolf
  for pdir in /etc/php/*/fpm/pool.d; do
    [[ -d "$pdir" ]] || continue
    for poolf in "$pdir"/jabali-*.conf "$pdir"/_jabali-*.conf; do
      [[ -f "$poolf" ]] && rm -f "$poolf"
    done
  done
  # Restart PHP-FPM so the per-version daemons drop the now-missing pool refs.
  local fpm
  for fpm in /etc/init.d/php*-fpm; do
    [[ -x "$fpm" ]] && systemctl restart "$(basename "$fpm")" 2>/dev/null || true
  done

  _log "removing AppArmor jabali profiles"
  local _aa_profile
  for _aa_profile in \
    usr.local.bin.jabali-agent \
    usr.local.bin.jabali-bulwark \
    usr.local.bin.jabali-kratos \
    usr.local.bin.jabali-panel-api \
    usr.local.bin.stalwart-mail; do
    if [[ -f "/etc/apparmor.d/$_aa_profile" ]]; then
      apparmor_parser -R "/etc/apparmor.d/$_aa_profile" 2>/dev/null || true
      rm -f "/etc/apparmor.d/$_aa_profile"
    fi
  done

  _log "removing PHP snuffleupagus ini files"
  local _phpv
  for _phpv in /etc/php/*/mods-available; do
    [[ -d "$_phpv" ]] || continue
    local _minor
    _minor="$(basename "$(dirname "$_phpv")")"
    rm -f "/etc/php/$_minor/mods-available/jabali-snuffleupagus.ini"
    rm -f "/etc/php/$_minor/fpm/conf.d/30-jabali-snuffleupagus.ini"
    rm -f "/etc/php/$_minor/cli/conf.d/30-jabali-snuffleupagus.ini"
  done

  # Disable build-swap before tearing down /var. Best-effort: a host
  # rebooted between install + uninstall may have the swap re-mounted
  # via /etc/fstab; peel it off cleanly so the rm -f below doesn't
  # fail with EBUSY.
  if [[ -e /var/swap.jabali ]]; then
    swapoff /var/swap.jabali 2>/dev/null || true
    rm -f /var/swap.jabali 2>/dev/null || true
    sed -i '\#^/var/swap\.jabali #d' /etc/fstab 2>/dev/null || true
  fi
  rm -f /etc/sysctl.d/99-jabali-swappiness.conf 2>/dev/null || true
  sysctl --system >/dev/null 2>&1 || true

  _log "removing state + install directories"
  rm -rf /var/lib/jabali        \
         /var/lib/jabali-panel  \
         /var/lib/jabali-webmail \
         /var/lib/stalwart       \
         /run/jabali             \
         /run/jabali-panel       \
         /opt/jabali             \
         /opt/jabali-panel       \
         /opt/jabali-webmail     \
         /opt/jabali-webmail.stage \
         /opt/jabali-webmail.prev  \
         /opt/stalwart            \
         /opt/stalwart.new        \
         /opt/stalwart.prev       \
         /opt/phpmyadmin          \
         /var/www/jabali-disabled \
         /var/lib/jabali-backups  \
         /var/lib/jabali-uploads  \
         /var/lib/jabali-migrations \
         /var/lib/jabali-panel-acme \
         /var/lib/jabali-restic-state \
         /var/log/jabali           \
         /var/log/jabali-panel    \
         /var/log/jabali-bulwark  \
         /var/log/jabali-agent    \
         /usr/local/maldetect     \
         /var/log/maldet          \
         /var/lib/aide            \
         /var/log/aide

  # /var/log/jabali (the active install log dir) was just rm -rf'd above;
  # blank LOG_FILE so the run-wrapper's `tee -a "$LOG_FILE"` and the
  # logger stop spamming "No such file or directory" for every remaining
  # uninstall step (accounts / home / apt purge).
  LOG_FILE=""

  # MariaDB: drop jabali databases + users. Try socket-auth first; if that
  # fails (root password set), ask for a password once.
  local mysql_root_cmd=""
  if mariadb -u root -e 'SELECT 1' >/dev/null 2>&1; then
    mysql_root_cmd="mariadb -u root"
  elif mysql -u root -e 'SELECT 1' >/dev/null 2>&1; then
    mysql_root_cmd="mysql -u root"
  fi

  if [[ -z "$mysql_root_cmd" ]]; then
    _warn "MariaDB root login (socket auth) not available — skipping database drop"
    _warn "Drop manually: DROP DATABASE jabali_panel; DROP DATABASE jabali_pdns; DROP DATABASE jabali_kratos;"
  else
    local drop_db="n"
    if [[ "${_cli_yes:-}" == "1" ]]; then
      drop_db="y"
    else
      read -rp "Drop jabali MariaDB databases + users (jabali_panel, jabali_pdns, jabali_kratos, stalwart_smtp)? [y/N]: " drop_db
    fi
    if [[ "${drop_db:-}" =~ ^[yY] ]]; then
      _log "dropping jabali databases"
      $mysql_root_cmd <<'SQL' 2>/dev/null || _warn "some DROP statements failed (may already be gone)"
DROP DATABASE IF EXISTS jabali_panel;
DROP DATABASE IF EXISTS jabali_pdns;
DROP DATABASE IF EXISTS jabali_kratos;
DROP DATABASE IF EXISTS stalwart_smtp;
DROP USER IF EXISTS 'jabali_panel_app'@'localhost';
DROP USER IF EXISTS 'jabali_pdns'@'localhost';
DROP USER IF EXISTS 'jabali_kratos'@'localhost';
DROP USER IF EXISTS 'stalwart_smtp'@'localhost';
FLUSH PRIVILEGES;
SQL
    else
      _log "skipping database drop (user declined)"
    fi
  fi

  _log "removing jabali system accounts"
  local u
  for u in jabali-webmail jabali-mail stalwart jabali; do
    if id "$u" >/dev/null 2>&1; then
      # Kill any lingering processes + systemd-logind sessions so userdel
      # doesn't refuse with 'user is currently used by process N'.
      # Best-effort: ignore errors when nothing to kill.
      loginctl terminate-user "$u" 2>/dev/null || true
      pkill -KILL -u "$u" 2>/dev/null || true
      # Tiny grace window so systemd reaps the session/scope before userdel.
      sleep 1
      # userdel -r would remove home; we pass --force for idempotence but NOT -r
      # because jabali's home is /opt/jabali-panel which we already rm -rf'd.
      userdel --force "$u" 2>/dev/null && _log "removed user $u" || _warn "could not remove user $u"
    fi
  done
  # Groups (may remain if --user-group flag wasn't used, or if the user was
  # removed but the group lingered).
  local g
  for g in jabali-ssh-sandbox jabali-sftp jabali-webmail jabali-mail jabali; do
    getent group "$g" >/dev/null 2>&1 && { groupdel "$g" 2>/dev/null && _log "removed group $g" || true; }
  done

  # Interactive: /home/ user cleanup. Jabali provisions end-user accounts
  # with home dirs under /home/<user>/. We can't distinguish jabali-created
  # accounts from pre-existing ones with certainty, so we list every
  # non-system /home entry and prompt per user.
  _log "enumerating /home/ users"
  local home_users=()
  while IFS=: read -r uname _ uid _ _ udir _; do
    [[ -d "$udir" ]] || continue
    [[ "$udir" == /home/* ]] || continue
    (( uid >= 1000 )) || continue
    home_users+=("$uname")
  done < /etc/passwd

  if [[ ${#home_users[@]} -eq 0 ]]; then
    _log "no /home/ users found"
  else
    printf '\nFound %d user(s) with home directories under /home/:\n' "${#home_users[@]}"
    printf '  %s\n' "${home_users[@]}"
    echo
    if [[ "${_cli_yes:-}" == "1" ]]; then
      _warn "--yes given: NOT removing /home users automatically (too destructive for auto-mode)."
      _warn "Remove manually if desired: userdel -r <user>"
    else
      local rm_all
      read -rp "Remove ALL listed users + their /home directories? [y/N/each]: " rm_all
      case "${rm_all:-}" in
        [yY]*)
          for u in "${home_users[@]}"; do
            userdel -r "$u" 2>/dev/null && _log "removed user $u + /home/$u" || _warn "userdel -r $u failed"
          done
          ;;
        each|EACH|e|E)
          for u in "${home_users[@]}"; do
            local ans2
            read -rp "  remove user '$u' (+ /home/$u)? [y/N]: " ans2
            if [[ "${ans2:-}" =~ ^[yY] ]]; then
              userdel -r "$u" 2>/dev/null && _log "removed $u" || _warn "userdel -r $u failed"
            fi
          done
          ;;
        *)
          _log "keeping all /home users"
          ;;
      esac
    fi
  fi

  # ── optional apt package removal ───────────────────────────────────────────
  # Pre-purge: nuke jabali-owned drop-ins under third-party /etc dirs.
  # Without this, apt-get purge nginx (etc.) bails out because dpkg
  # refuses to remove conffiles that don't match its tracking list,
  # AND leftover sites-enabled symlinks make a re-install of nginx
  # crash at boot when our drop-ins reference vanished upstreams.
  _log "stripping jabali-owned drop-ins from third-party /etc trees"
  # nginx — stop first so we don't break running config mid-purge.
  systemctl stop nginx 2>/dev/null || true
  # Per-domain vhosts + bulwark + per-domain mail + every jabali-* file.
  find /etc/nginx/sites-available -maxdepth 1 -type f \
    \( -name 'jabali-*' -o -name '*-mail.conf' -o -name '*.conf' \) \
    2>/dev/null | while read -r _vh; do
      # Only remove files that mention "jabali" or our managed
      # bulwark upstream — never touch operator-authored vhosts.
      if [[ "$(basename "$_vh")" == jabali-* ]] || grep -q -E 'jabali_(panel_api|bulwark)|jabali-' "$_vh" 2>/dev/null; then
        rm -f "$_vh"
        rm -f "/etc/nginx/sites-enabled/$(basename "$_vh")"
      fi
    done
  # jabali's default vhost is jabali-default.conf, symlinked into
  # sites-enabled as `default.conf` — the basename-symlink removal in
  # the loop misses it, leaving the `http2 on;` default that fails
  # `nginx -t` on nginx<1.25.1 and cascades the apt phase. Remove it
  # by name + sweep any dangling or jabali-targeted symlink left in
  # sites-enabled.
  rm -f /etc/nginx/sites-available/jabali-default.conf 2>/dev/null || true
  for _l in /etc/nginx/sites-enabled/*; do
    [[ -L "$_l" ]] || continue
    _t="$(readlink -f "$_l" 2>/dev/null || true)"
    if [[ -z "$_t" || ! -e "$_t" || "$_t" == */jabali-*.conf ]]; then
      rm -f "$_l"
    fi
  done
  rm -f  /etc/nginx/sites-available/includes/phpmyadmin.conf 2>/dev/null || true
  rmdir  /etc/nginx/sites-available/includes 2>/dev/null || true
  rm -f  /etc/nginx/snippets/jabali-*.conf 2>/dev/null || true
  rm -f  /etc/nginx/conf.d/jabali-*.conf 2>/dev/null || true
  # NOTE: /etc/nginx/conf.d/crowdsec_nginx.conf is OWNED by the
  # crowdsec-nginx-bouncer apt package — deleting it here breaks that
  # package's purge postrm (it warns "remove manually or NGINX will not
  # restart") and leaves nginx un-`nginx -t`-able until the bouncer is
  # gone, cascading dpkg failures through the whole purge. Let
  # `apt-get purge crowdsec-nginx-bouncer` own its own conffile.
  # Strip the sites-enabled include we added on first install — left
  # over even after we remove the symlinks under sites-enabled/.
  if [[ -f /etc/nginx/nginx.conf ]]; then
    sed -i '/include \/etc\/nginx\/sites-enabled\/\*.conf;/d' /etc/nginx/nginx.conf
  fi

  # MariaDB / mysql drop-ins beyond skip-networking (which the systemd
  # block above already removed).
  rm -f /etc/mysql/mariadb.conf.d/9?-jabali-*.cnf 2>/dev/null || true

  # CrowdSec scenarios + parsers + bouncer config we wrote.
  rm -rf /etc/crowdsec/parsers/s00-raw/jabali-*.yaml 2>/dev/null || true
  rm -rf /etc/crowdsec/scenarios/jabali-*.yaml 2>/dev/null || true
  rm -rf /etc/crowdsec/profiles.d 2>/dev/null || true
  rm -f  /etc/crowdsec/bouncers/jabali-*.yaml 2>/dev/null || true

  # PowerDNS — install_powerdns wrote a stack of drop-ins under
  # /etc/powerdns/pdns.d/ + /etc/powerdns/recursor.d/.
  rm -f /etc/powerdns/pdns.d/0?-jabali-*.conf 2>/dev/null || true
  rm -f /etc/powerdns/recursor.forwards 2>/dev/null || true
  rm -f /etc/powerdns/recursor.d/zz-jabali-recursor.conf 2>/dev/null || true

  # Redis — always purge unconditionally. jabali is the only consumer
  # on a panel host, so leaving it behind serves no operator use case.
  # Stop first so dpkg doesn't have to send SIGTERM mid-purge.
  systemctl stop redis-server 2>/dev/null || true
  systemctl disable redis-server 2>/dev/null || true
  DEBIAN_FRONTEND=noninteractive apt-get purge -y \
    -o Dpkg::Options::="--force-confdef" \
    -o Dpkg::Options::="--force-confnew" \
    redis-server redis-tools 2>/dev/null || true
  rm -rf /etc/redis /var/lib/redis /var/log/redis /run/redis 2>/dev/null || true

  # PHP per-version drop-ins outside the snuffleupagus + pool blocks.
  local _phpv2
  for _phpv2 in /etc/php/*/fpm; do
    [[ -d "$_phpv2" ]] || continue
    rm -f "$_phpv2"/conf.d/3?-jabali-*.ini 2>/dev/null || true
    rm -f "$(dirname "$_phpv2")"/cli/conf.d/3?-jabali-*.ini 2>/dev/null || true
  done

  # Build the list from what install.sh actually installs. Generic OS
  # primitives (git, curl, ca-certificates, build-essential, rsync, acl,
  # tar, bzip2, unzip, openssl, gnupg, debootstrap, systemd-container,
  # systemd-resolved) are intentionally excluded — they pre-date jabali on
  # most hosts and apt autoremove won't touch anything not in the list anyway.
  local -a _apt_pkgs=(
    mariadb-server mariadb-client
    nginx certbot python3-certbot-nginx
    nodejs
    pdns-server pdns-backend-mysql pdns-recursor bind9-dnsutils
    redis-server redis-tools
    quota quotatool xfsprogs
    ufw yq
    bubblewrap
    yara
    ed inotify-tools
    restic
    sshpass
    crowdsec crowdsec-firewall-bouncer-nftables crowdsec-firewall-bouncer-iptables crowdsec-nginx-bouncer
    auditd audispd-plugins
    apparmor apparmor-utils apparmor-profiles-extra
    aide aide-common
    goaccess
  )
  # Add per-minor PHP packages that are actually installed on this host.
  local _pv
  for _pv in /etc/php/*/fpm; do
    [[ -d "$_pv" ]] || continue
    local _minor
    _minor="$(basename "$(dirname "$_pv")")"
    _apt_pkgs+=("php${_minor}-fpm" "php${_minor}-cli")
    local _ext
    for _ext in mysql mbstring zip gd curl xml intl bcmath opcache; do
      dpkg -l "php${_minor}-${_ext}" >/dev/null 2>&1 && _apt_pkgs+=("php${_minor}-${_ext}") || true
    done
  done

  local _do_purge=0
  if [[ "${_cli_purge_packages:-}" == "1" ]]; then
    _do_purge=1
  elif [[ "${_cli_yes:-}" != "1" ]]; then
    echo
    printf 'The following OS packages were installed by jabali:\n'
    printf '  %s\n' "${_apt_pkgs[@]}" | sort -u
    echo
    local _ans_pkg
    read -rp "Purge these packages from the system? [y/N]: " _ans_pkg
    [[ "${_ans_pkg:-}" =~ ^[yY] ]] && _do_purge=1 || true
  fi

  if [[ "$_do_purge" == "1" ]]; then
    _log "purging jabali-installed apt packages"
    # Filter to only packages that are actually installed to keep the output clean.
    local -a _installed_pkgs=()
    local _p
    for _p in "${_apt_pkgs[@]}"; do
      dpkg -l "$_p" 2>/dev/null | grep -q '^ii' && _installed_pkgs+=("$_p") || true
    done
    if [[ ${#_installed_pkgs[@]} -gt 0 ]]; then
      # Recover from any half-broken package state first; otherwise
      # apt-get purge can refuse to proceed (commonly seen when a
      # previous nginx install was killed mid-config-reload).
      DEBIAN_FRONTEND=noninteractive dpkg --configure -a 2>/dev/null || true
      DEBIAN_FRONTEND=noninteractive apt-get install -f -y 2>/dev/null || true
      DEBIAN_FRONTEND=noninteractive apt-get purge -y \
        -o Dpkg::Options::="--force-confdef" \
        -o Dpkg::Options::="--force-confnew" \
        "${_installed_pkgs[@]}" 2>/dev/null || true
      # Hard fallback for any package that survived apt-get purge —
      # dpkg --purge ignores dep order + forces conffile removal.
      for _p in "${_installed_pkgs[@]}"; do
        dpkg -l "$_p" 2>/dev/null | grep -q '^.[ic]' && \
          DEBIAN_FRONTEND=noninteractive dpkg --purge --force-all "$_p" 2>/dev/null || true
      done
      DEBIAN_FRONTEND=noninteractive apt-get autoremove --purge -y 2>/dev/null || true
      _ok "apt packages purged"
    else
      _log "no jabali apt packages found installed — nothing to purge"
    fi
  else
    _ok "OS packages (nginx, mariadb, pdns, php, node, …) left INSTALLED — remove with apt if desired"
    _ok "  or re-run with: bash install.sh --uninstall --purge-packages"
  fi

  rm -f /usr/local/bin/composer

  _ok "jabali uninstall complete"
}

# Only execute main when this script is run directly (not sourced).
# Sourcing was previously a foot-gun: `source install.sh; install_x`
# re-ran the entire installer because main was unconditional. Caught
# 2026-04-27 when sourcing for ad-hoc function invocation locked SSH
# out of a live VM by re-provisioning sshd_config + authorized_keys.
#
# Default BASH_SOURCE[0] to $0 under `set -u` because it is unset when
# the script comes in over stdin (`curl … | bash`); the unguarded
# expansion errored out before main() ever ran. The defaulting also
# preserves the original semantics:
#   - direct: BASH_SOURCE[0]==$0   → run main
#   - sourced: BASH_SOURCE[0]!=$0  → skip main
#   - piped: defaults to $0==$0    → run main (the user clearly invoked
#                                    it, just via stdin)
if [[ "${BASH_SOURCE[0]:-$0}" == "${0}" ]]; then
  if [[ -n "${_cli_uninstall:-}" ]]; then
    uninstall
  elif [[ -n "${_cli_install_module:-}" ]]; then
    install_module "$_cli_install_module"
  else
    main "$@"
  fi
fi
