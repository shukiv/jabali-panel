"""Jabali Security CLI entry point (click-based)."""

from __future__ import annotations

import asyncio
import dataclasses
import fcntl
import json
import logging
import os
import signal
import sys
from logging.handlers import RotatingFileHandler
from pathlib import Path
from urllib.error import URLError
from urllib.request import Request, urlopen

import click

from lib.config import JabaliConfig, load_config, update_conf_key
from lib.constants import APP_NAME, CONFIG_FILE, LOG_DIR

logger = logging.getLogger(APP_NAME)

_PID_DIR_ROOT = Path("/var/run/jabali-security")
_PID_FILENAME = "jabali-security.pid"
_MAX_PID = 4194304  # Linux default pid_max

_LOG_FORMAT = "%(asctime)s [%(levelname)s] %(name)s: %(message)s"
_LOG_MAX_BYTES = 5 * 1024 * 1024  # 5 MB
_LOG_BACKUP_COUNT = 3


# -- PID helpers -------------------------------------------------------------

def _pid_dir() -> Path:
    from lib.constants import _service_context
    if _service_context:
        return _PID_DIR_ROOT
    # Use XDG_RUNTIME_DIR (e.g. /run/user/<uid>) to avoid /tmp symlink attacks
    runtime = os.environ.get("XDG_RUNTIME_DIR")
    if runtime:
        return Path(runtime) / "jabali-security"
    return Path.home() / ".cache" / "jabali-security"


def _pid_file() -> Path:
    d = _pid_dir()
    d.mkdir(parents=True, exist_ok=True)
    return d / _PID_FILENAME


def _write_pid():
    """Write PID and acquire an exclusive lock.

    Returns the open file object -- the caller MUST keep it alive so the
    ``fcntl.flock`` lock persists for the lifetime of the process.
    """
    pf = _pid_file()
    # Use O_CREAT|O_WRONLY|O_TRUNC without following symlinks:
    # unlink first to avoid writing through a symlink
    try:
        if pf.is_symlink():
            pf.unlink()
    except OSError:
        pass
    fd = os.open(str(pf), os.O_CREAT | os.O_WRONLY | os.O_TRUNC | os.O_NOFOLLOW, 0o644)
    f = os.fdopen(fd, "w")
    try:
        fcntl.flock(f.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
    except OSError:
        f.close()
        click.echo("Another instance of jabali-security is already running.", err=True)
        sys.exit(1)
    f.write(str(os.getpid()))
    f.flush()
    return f


def _remove_pid() -> None:
    try:
        _pid_file().unlink(missing_ok=True)
    except OSError:
        pass


def _read_pid() -> int | None:
    pf = _pid_file()
    if not pf.exists() or pf.is_symlink():
        return None
    text = pf.read_text().strip()
    if not text.isdigit():
        return None
    pid = int(text)
    if pid < 1 or pid > _MAX_PID:
        return None
    return pid


def _verify_pid_is_ours(pid: int) -> bool:
    """Check that a PID belongs to a jabali-security process via /proc."""
    try:
        cmdline = Path("/proc/%d/cmdline" % pid).read_bytes()
        return b"jabali-security" in cmdline or b"daemon" in cmdline
    except OSError:
        return False


# -- Logging setup -----------------------------------------------------------

def _setup_logging(foreground: bool, level: str) -> None:
    root = logging.getLogger()
    root.setLevel(getattr(logging, level.upper(), logging.INFO))

    if foreground:
        handler: logging.Handler = logging.StreamHandler(sys.stderr)
    else:
        log_dir = Path(str(LOG_DIR))
        log_dir.mkdir(parents=True, exist_ok=True)
        handler = RotatingFileHandler(
            log_dir / "jabali-security.log",
            maxBytes=_LOG_MAX_BYTES,
            backupCount=_LOG_BACKUP_COUNT,
        )

    handler.setFormatter(logging.Formatter(_LOG_FORMAT))
    root.addHandler(handler)


# -- API client helpers ------------------------------------------------------

def _api_request(config: JabaliConfig, method: str, path: str, body: dict | None = None) -> dict:
    """Make an API request to the running daemon via Unix socket or TCP."""
    import http.client
    import socket as socket_mod

    data = json.dumps(body).encode() if body else None
    headers = {"Content-Type": "application/json"}
    if config.api_key:
        headers["X-API-Key"] = config.api_key

    socket_path = config.api_socket
    if socket_path and os.path.exists(socket_path):
        # Connect via Unix socket
        conn = http.client.HTTPConnection("localhost")
        sock = socket_mod.socket(socket_mod.AF_UNIX, socket_mod.SOCK_STREAM)
        sock.settimeout(30)
        try:
            sock.connect(socket_path)
            conn.sock = sock
            conn.request(method, path, body=data, headers=headers)
            resp = conn.getresponse()
            result = json.loads(resp.read().decode())
            return result
        finally:
            conn.close()
    else:
        # TCP fallback
        if not config.api_bind:
            _daemon_not_running()
        url = "http://%s:%d%s" % (config.api_bind, config.api_port, path)
        req = Request(url, data=data, headers=headers, method=method)  # noqa: S310
        with urlopen(req, timeout=30) as resp:  # noqa: S310
            return json.loads(resp.read().decode())


def _daemon_not_running() -> None:
    """Print daemon-not-running message and exit."""
    click.echo("Daemon is not running. Use 'jabali-security start' first.", err=True)
    sys.exit(1)


def _api_call(config: JabaliConfig, method: str, path: str, body: dict | None = None) -> dict:
    """Wrapper around _api_request that handles connection errors.

    Unwraps the standard API envelope: {"success": ..., "data": ..., "error": ...}
    Returns the "data" payload directly.
    """
    try:
        resp = _api_request(config, method, path, body)
    except (URLError, ConnectionRefusedError, OSError):
        _daemon_not_running()
        return {}  # unreachable, satisfies type checker

    if isinstance(resp, dict) and "data" in resp:
        if not resp.get("success"):
            click.echo("Error: %s" % resp.get("error", "unknown"), err=True)
            sys.exit(1)
        return resp["data"] if resp["data"] is not None else {}
    return resp


# -- Standalone scan helpers -------------------------------------------------

def _standalone_scan(file_path: str, config: JabaliConfig) -> dict:
    """Scan a single file without the daemon running."""
    from lib.filter import PreFilter
    from lib.models import FileEvent
    from lib.scanner import ScanOrchestrator
    from lib.scoring import ScoringEngine
    from lib.tenant import resolve_user

    p = Path(file_path)
    if not p.is_file():
        click.echo("File not found: %s" % file_path, err=True)
        return {}

    pre_filter = PreFilter(config)
    if not pre_filter.should_scan(str(p)):
        return {}

    content = p.read_bytes()
    scanner = ScanOrchestrator(config)
    scoring = ScoringEngine(config)

    event = FileEvent(
        event_type="scan",
        path=str(p.resolve()),
        username=resolve_user(str(p)),
        size=len(content),
    )
    findings = asyncio.run(scanner.scan(str(p), content))
    score = scoring.evaluate(event, findings)

    return {
        "path": str(p.resolve()),
        "findings": [f.model_dump() for f in findings],
        "score": score.total,
        "action": score.action,
        "severity": ScoringEngine.severity_from_score(score.total),
    }


def _scan_directory(dir_path: str, config: JabaliConfig) -> list[dict]:
    """Recursively scan a directory, returning results for each matching file."""
    from lib.filter import PreFilter

    results: list[dict] = []
    pre_filter = PreFilter(config)
    for root, _dirs, files in os.walk(dir_path):
        for f in files:
            full = os.path.join(root, f)
            if pre_filter.should_scan(full):
                result = _standalone_scan(full, config)
                if result:
                    results.append(result)
    return results


def _print_scan_result(result: dict) -> None:
    """Print a single scan result in human-readable format."""
    click.echo("Scanning: %s" % result["path"])
    click.echo("  Score: %d (%s) -> %s" % (result["score"], result.get("severity", "unknown"), result.get("action", "unknown")))
    findings = result.get("findings", [])
    if findings:
        click.echo("  Findings:")
        for f in findings:
            click.echo("    [%s] %s (%d) %s" % (f["scanner"], f["rule"], f["score"], f["description"]))


def _daemon_is_reachable(config: JabaliConfig) -> bool:
    """Check if the daemon API is reachable."""
    try:
        _api_request(config, "GET", "/api/v1/status")
        return True
    except (URLError, ConnectionRefusedError, OSError):
        return False


# -- CLI ---------------------------------------------------------------------

@click.group()
def cli():
    """Jabali Security -- event-driven security suite for Linux shared hosting."""


@cli.command()
@click.option("--foreground", is_flag=True, help="Run in foreground with console logging")
@click.option("--config", "config_path", type=click.Path(exists=True), default=None, help="Path to config file")
def start(foreground: bool, config_path: str | None) -> None:
    """Start the security daemon."""
    cfg_path = Path(config_path) if config_path else None
    config = load_config(cfg_path)

    _setup_logging(foreground, config.log_level)

    from daemon.server import SecurityDaemon

    daemon = SecurityDaemon(config)

    pid_fh = _write_pid()
    try:
        asyncio.run(daemon.run())
    finally:
        pid_fh.close()
        _remove_pid()


def _generate_bouncer_key(config_file: str) -> None:
    """Generate CrowdSec bouncer API key and save to config. Skips if already set."""
    import subprocess
    if not os.path.isfile(config_file):
        return
    with open(config_file) as fh:
        conf_content = fh.read()
    if 'CROWDSEC_BOUNCER_KEY=""' not in conf_content and "CROWDSEC_BOUNCER_KEY" in conf_content:
        return  # already has a key
    # Delete stale bouncer entry first (reinstall leaves it, add fails silently)
    subprocess.run(  # noqa: S603
        ["cscli", "bouncers", "delete", "jabali-security"],
        capture_output=True, timeout=10,
    )
    result = subprocess.run(  # noqa: S603
        ["cscli", "bouncers", "add", "jabali-security", "-o", "raw"],
        capture_output=True, text=True, timeout=10,
    )
    key = result.stdout.strip() if result.returncode == 0 else ""
    if key:
        if "CROWDSEC_BOUNCER_KEY" in conf_content:
            conf_content = conf_content.replace(
                'CROWDSEC_BOUNCER_KEY=""',
                'CROWDSEC_BOUNCER_KEY="%s"' % key,
            )
        else:
            conf_content += '\nCROWDSEC_BOUNCER_KEY="%s"\n' % key
        with open(config_file, "w") as fh:
            fh.write(conf_content)
        click.echo("  CrowdSec bouncer key generated.")


def _write_bouncer_config() -> None:
    """Write bouncer YAML config with API key before package install.

    The bouncer package post-install starts the service immediately.
    Without a valid config, it fails and leaves dpkg in a broken state.
    """
    import subprocess
    yaml_path = "/etc/crowdsec/bouncers/crowdsec-firewall-bouncer.yaml"
    if os.path.isfile(yaml_path):
        return  # already configured
    # Read the API key we just generated via cscli
    result = subprocess.run(  # noqa: S603
        ["cscli", "bouncers", "list", "-o", "raw"],
        capture_output=True, text=True, timeout=10,
    )
    api_key = ""
    for line in result.stdout.splitlines():
        if "jabali" in line.lower():
            parts = line.split(",")
            if len(parts) >= 2:
                api_key = parts[0].strip()
                break
    if not api_key:
        return
    os.makedirs("/etc/crowdsec/bouncers", exist_ok=True)
    with open(yaml_path, "w") as fh:
        fh.write(
            "mode: nftables\n"
            "api_url: http://127.0.0.1:8080/\n"
            "api_key: %s\n"
            "disable_ipv6: false\n"
            "update_frequency: 10s\n"
            "deny_action: DROP\n"
            "deny_log: false\n" % api_key
        )
    os.chmod(yaml_path, 0o600)


@cli.command()
@click.option("--force", is_flag=True, help="Update even if no new commits are available.")
def update(force: bool) -> None:
    """Update jabali-security to the latest version."""
    import subprocess

    REPO_URL = "https://github.com/shukiv/jabali-security.git"
    INSTALL_DIR = "/usr/local/jabali-security"
    VERSION_FILE = os.path.join(INSTALL_DIR, ".version")

    click.echo("Updating Jabali Security...")

    # Skip work when the remote HEAD matches the installed version.
    if not force:
        remote_sha = ""
        try:
            ls_remote = subprocess.run(  # noqa: S603
                ["/usr/bin/git", "ls-remote", REPO_URL, "HEAD"],
                capture_output=True, timeout=30, text=True,
            )
            if ls_remote.returncode == 0 and ls_remote.stdout.strip():
                remote_sha = ls_remote.stdout.split()[0]
        except subprocess.TimeoutExpired:
            remote_sha = ""
        installed_sha = ""
        if os.path.isfile(VERSION_FILE):
            try:
                with open(VERSION_FILE) as fh:
                    installed_sha = fh.read().strip()
            except OSError:
                installed_sha = ""
        if remote_sha and installed_sha and remote_sha == installed_sha:
            click.echo(
                "Already up to date (%s). Use --force to update anyway."
                % remote_sha[:8]
            )
            return

    # Clone latest to temp dir
    import tempfile
    tmp_dir = tempfile.mkdtemp()
    result = subprocess.run(  # noqa: S603
        ["/usr/bin/git", "clone", "--depth", "1", "--quiet", REPO_URL, tmp_dir],
        capture_output=True, timeout=60,
    )
    if result.returncode != 0:
        click.echo("Failed to clone repository.", err=True)
        sys.exit(1)

    # Capture the cloned SHA so we can persist it once the update succeeds.
    _rev = subprocess.run(  # noqa: S603
        ["/usr/bin/git", "-C", tmp_dir, "rev-parse", "HEAD"],
        capture_output=True, timeout=10, text=True,
    )
    new_sha = _rev.stdout.strip() if _rev.returncode == 0 else ""

    # Copy updated files
    import shutil
    for subdir in ["daemon", "lib", "api", "web", "rules", "etc"]:
        src = os.path.join(tmp_dir, subdir)
        dst = os.path.join(INSTALL_DIR, subdir)
        if os.path.isdir(src):
            if os.path.exists(dst):
                shutil.rmtree(dst)
            shutil.copytree(src, dst)

    # Copy bin wrapper
    bin_src = os.path.join(tmp_dir, "bin", "jabali-security")
    if os.path.exists(bin_src):
        shutil.copy2(bin_src, os.path.join(INSTALL_DIR, "bin", "jabali-security"))

    # Ensure Python dependencies are up to date
    venv_pip = os.path.join(INSTALL_DIR, "venv", "bin", "pip")
    if os.path.isfile(venv_pip):
        pip_pkgs = ["pydantic>=2.0", "yara-x>=0.11", "click>=8.0", "aiohttp>=3.9",
                     "pyyaml>=6.0", "aiosqlite>=0.20", "maxminddb>=2.0"]
        venv_python = os.path.join(INSTALL_DIR, "venv", "bin", "python")
        if shutil.which("uv"):
            subprocess.run(  # noqa: S603
                ["uv", "pip", "install", "--python", venv_python] + pip_pkgs,
                capture_output=True, timeout=120,
            )
        else:
            subprocess.run(  # noqa: S603
                [venv_pip, "install", "--quiet"] + pip_pkgs,
                capture_output=True, timeout=120,
            )

    # Deploy shared challenge page and njs validator
    challenge_dir = "/etc/nginx/jabali/challenge"
    njs_dir = "/etc/nginx/jabali-security"
    os.makedirs(challenge_dir, exist_ok=True)
    os.makedirs(njs_dir, exist_ok=True)
    ws_dir = os.path.join(INSTALL_DIR, "etc", "webshield")
    challenge_src = os.path.join(ws_dir, "challenge.html")
    njs_src = os.path.join(ws_dir, "jabali_challenge.js")
    if os.path.isfile(challenge_src):
        shutil.copy2(challenge_src, os.path.join(challenge_dir, "jabali-challenge.html"))
    if os.path.isfile(njs_src):
        shutil.copy2(njs_src, os.path.join(njs_dir, "jabali_challenge.js"))

    # Update Jabali Panel plugin if panel exists
    panel_dir = "/var/www/jabali/app/JabaliSecurity"
    panel_src = os.path.join(tmp_dir, "panel")
    if os.path.isdir("/var/www/jabali/app/Filament") and os.path.isdir(panel_src):
        os.makedirs(os.path.join(panel_dir, "Pages"), exist_ok=True)
        os.makedirs(os.path.join(panel_dir, "Widgets"), exist_ok=True)
        os.makedirs(os.path.join(panel_dir, "views"), exist_ok=True)
        for f in ["JabaliSecurityPlugin.php", "JabaliSecurityClient.php"]:
            src = os.path.join(panel_src, f)
            if os.path.exists(src):
                shutil.copy2(src, os.path.join(panel_dir, f))
        for subdir in ["Pages", "Widgets", "views"]:
            src_dir = os.path.join(panel_src, subdir)
            if os.path.isdir(src_dir):
                for f in os.listdir(src_dir):
                    shutil.copy2(os.path.join(src_dir, f), os.path.join(panel_dir, subdir, f))
        # Register plugin in AdminPanelProvider if not already registered
        provider = "/var/www/jabali/app/Providers/Filament/AdminPanelProvider.php"
        if os.path.isfile(provider):
            with open(provider, "r") as fh:
                content = fh.read()
            if "JabaliSecurityPlugin" not in content and "->middleware([" in content:
                plugin_block = (
                    "            ->plugins(array_filter([\n"
                    "                class_exists(\\App\\JabaliSecurity\\JabaliSecurityPlugin::class)\n"
                    "                    ? \\App\\JabaliSecurity\\JabaliSecurityPlugin::make()\n"
                    "                    : null,\n"
                    "            ]))\n"
                )
                content = content.replace(
                    "            ->middleware([",
                    plugin_block + "            ->middleware([",
                    1,  # only first occurrence
                )
                with open(provider, "w") as fh:
                    fh.write(content)
                click.echo("Security plugin registered in AdminPanelProvider.")
        click.echo("Jabali Panel plugin updated.")

    # Cleanup
    shutil.rmtree(tmp_dir, ignore_errors=True)

    # Migrate config to Unix socket if not yet configured
    config_file = "/etc/jabali-security/jabali-security.conf"
    if os.path.isfile(config_file):
        with open(config_file) as fh:
            conf_content = fh.read()
        migrated = False
        if "API_SOCKET=" not in conf_content:
            conf_content += '\nAPI_SOCKET="/run/jabali-security/jabali-security.sock"\n'
            migrated = True
        if 'API_BIND="127.0.0.1"' in conf_content:
            conf_content = conf_content.replace('API_BIND="127.0.0.1"', 'API_BIND=""')
            migrated = True
        if migrated:
            with open(config_file, "w") as fh:
                fh.write(conf_content)
            click.echo("Config migrated to Unix socket.")

    # Install CrowdSec if not present
    if not shutil.which("cscli"):
        click.echo("Installing CrowdSec (this may take a minute)...")
        try:
            click.echo("  Adding CrowdSec repository...")
            subprocess.run(  # noqa: S603
                ["/bin/bash", "-c",
                 "curl -fsSL https://install.crowdsec.net 2>/dev/null | bash 2>/dev/null"],
                capture_output=True, timeout=60,
            )
            click.echo("  Installing crowdsec package...")
            subprocess.run(  # noqa: S603
                ["/bin/bash", "-c",
                 "DEBIAN_FRONTEND=noninteractive apt-get install -y -qq crowdsec 2>/dev/null"
                 " && systemctl start crowdsec 2>/dev/null && sleep 2"],
                capture_output=True, timeout=90,
            )
            # Generate bouncer API key and write bouncer config BEFORE
            # installing the package — its post-install starts the service
            # immediately and needs a valid API key or it fails and breaks dpkg.
            if shutil.which("cscli"):
                _generate_bouncer_key(config_file)
                _write_bouncer_config()
            click.echo("  Installing firewall bouncer...")
            result = subprocess.run(  # noqa: S603
                ["/bin/bash", "-c",
                 "DEBIAN_FRONTEND=noninteractive apt-get install -y -qq"
                 " crowdsec-firewall-bouncer-nftables 2>/dev/null"],
                capture_output=True, timeout=60,
            )
            if result.returncode != 0:
                # Fix broken dpkg state so subsequent apt operations work
                subprocess.run(  # noqa: S603
                    ["dpkg", "--configure", "-a"],
                    capture_output=True, timeout=30,
                )
                click.echo("  Firewall bouncer install failed (non-critical).")
        except subprocess.TimeoutExpired:
            click.echo("  CrowdSec install timed out — skipping (will retry on next update).")

    # Install CrowdSec collections only on first setup (skip if already installed)
    if shutil.which("cscli"):
        try:
            result = subprocess.run(  # noqa: S603
                ["cscli", "collections", "list", "-o", "raw"],
                capture_output=True, text=True, timeout=10,
            )
            installed = result.stdout if result.returncode == 0 else ""
            for col in ["linux", "sshd", "nginx", "base-http-scenarios"]:
                if "crowdsecurity/%s" % col not in installed:
                    click.echo("  Installing collection crowdsecurity/%s..." % col)
                    subprocess.run(  # noqa: S603
                        ["cscli", "collections", "install", "crowdsecurity/%s" % col],
                        capture_output=True, timeout=30,
                    )
        except subprocess.TimeoutExpired:
            click.echo("  CrowdSec collections timed out — skipping.")

    # Generate CrowdSec bouncer key if missing (idempotent — skips if already set)
    if shutil.which("cscli"):
        _generate_bouncer_key(config_file)

    # -- Upgrade non-apt components --

    # ClamAV virus definitions
    if shutil.which("freshclam"):
        click.echo("Updating ClamAV virus definitions...")
        result = subprocess.run(  # noqa: S603
            ["freshclam", "--quiet"],
            capture_output=True, timeout=120,
        )
        if result.returncode == 0:
            click.echo("  Virus definitions updated.")
        else:
            click.echo("  freshclam skipped (may already be running).")

    # OWASP CRS rules (git-based)
    crs_dir = "/usr/local/share/owasp-crs"
    if os.path.isdir(os.path.join(crs_dir, ".git")):
        click.echo("Updating OWASP CRS rules...")
        result = subprocess.run(  # noqa: S603
            ["/usr/bin/git", "-C", crs_dir, "pull", "--quiet"],
            capture_output=True, timeout=30,
        )
        if result.returncode == 0:
            click.echo("  OWASP CRS rules updated.")
        else:
            click.echo("  OWASP CRS update skipped.")

    # GeoIP database via geoipupdate CLI (preferred) or Python fallback
    if shutil.which("geoipupdate") and os.path.isfile("/etc/GeoIP.conf"):
        click.echo("Updating GeoIP database (geoipupdate)...")
        result = subprocess.run(  # noqa: S603
            ["geoipupdate", "-v"],
            capture_output=True, timeout=60,
        )
        if result.returncode == 0:
            click.echo("  GeoIP database updated.")
        else:
            click.echo("  GeoIP update skipped (check /etc/GeoIP.conf).")
    elif os.path.isfile(config_file):
        with open(config_file) as fh:
            _conf = fh.read()
        if 'GEOIP_MAXMIND_LICENSE_KEY="' in _conf and 'GEOIP_MAXMIND_LICENSE_KEY=""' not in _conf:
            click.echo("Updating GeoIP database...")
            try:
                import re as _re

                from lib.webshield.geoip import GeoIPManager
                _key_match = _re.search(r'GEOIP_MAXMIND_LICENSE_KEY="([^"]+)"', _conf)
                _db_match = _re.search(r'GEOIP_DB_PATH="([^"]+)"', _conf)
                if _key_match:
                    _geoip = GeoIPManager(
                        db_path=_db_match.group(1) if _db_match else "/var/lib/jabali-security/GeoLite2-Country.mmdb",
                        license_key=_key_match.group(1),
                    )
                    import asyncio as _aio
                    _ok_geo, _msg = _aio.get_event_loop().run_until_complete(_geoip.download_database())
                    if _ok_geo:
                        click.echo("  GeoIP database updated.")
                    else:
                        click.echo("  GeoIP update skipped: %s" % _msg)
            except Exception:
                click.echo("  GeoIP update skipped.")

    # CrowdSec hub index (fast — just refreshes the index, no downloads)
    if shutil.which("cscli"):
        click.echo("Updating CrowdSec hub index...")
        result = subprocess.run(  # noqa: S603
            ["cscli", "hub", "update"],
            capture_output=True, timeout=30,
        )
        if result.returncode == 0:
            click.echo("  CrowdSec hub index updated.")
        else:
            click.echo("  CrowdSec hub update skipped.")

    # -- Privilege separation migration --
    import grp
    import pwd as _pwd

    # 1. Create jabali-security system user if missing
    try:
        _pwd.getpwnam("jabali-security")
        click.echo("  Service user jabali-security exists.")
    except KeyError:
        click.echo("Creating jabali-security service user...")
        result = subprocess.run(  # noqa: S603
            ["useradd", "--system", "--no-create-home",
             "--shell", "/usr/sbin/nologin",
             "--home-dir", INSTALL_DIR, "jabali-security"],
            capture_output=True, timeout=10,
        )
        if result.returncode == 0:
            click.echo("  Created system user: jabali-security")
        else:
            click.echo("  WARNING: failed to create user: %s" % result.stderr.decode().strip())

    # 2. Add to www-data group for socket access
    try:
        grp.getgrnam("www-data")
        subprocess.run(  # noqa: S603
            ["usermod", "-aG", "www-data", "jabali-security"],
            capture_output=True, timeout=10,
        )
    except KeyError:
        pass

    # 3. Install sudoers rules
    sudoers_src = os.path.join(INSTALL_DIR, "etc", "sudoers.d", "jabali-security")
    sudoers_dst = "/etc/sudoers.d/jabali-security"
    if os.path.isfile(sudoers_src) and os.path.isdir("/etc/sudoers.d"):
        import shutil as _su
        _su.copy2(sudoers_src, sudoers_dst)
        os.chmod(sudoers_dst, 0o440)
        os.chown(sudoers_dst, 0, 0)
        # Validate with visudo
        vcheck = subprocess.run(  # noqa: S603
            ["visudo", "-cf", sudoers_dst],
            capture_output=True, timeout=5,
        )
        if vcheck.returncode == 0:
            click.echo("  Sudoers rules installed.")
        else:
            os.unlink(sudoers_dst)
            click.echo("  WARNING: sudoers validation failed, removed.")

    # 4. Update systemd service file
    svc_src = os.path.join(INSTALL_DIR, "etc", "jabali-security.service")
    svc_dst = "/etc/systemd/system/jabali-security.service"
    if os.path.isfile(svc_src):
        import shutil as _su
        _su.copy2(svc_src, svc_dst)
        subprocess.run(  # noqa: S603
            ["/usr/bin/systemctl", "daemon-reload"],
            capture_output=True, timeout=10,
        )
        click.echo("  Service file updated.")

    # 5. Fix directory ownership — jabali-security:www-data
    try:
        sec_uid = _pwd.getpwnam("jabali-security").pw_uid
        www_gid = grp.getgrnam("www-data").gr_gid
        for d in ["/etc/jabali-security", "/var/log/jabali-security",
                  "/var/lib/jabali-security", "/var/security/quarantine"]:
            if os.path.isdir(d):
                os.chown(d, sec_uid, www_gid if d == "/etc/jabali-security" else sec_uid)
                os.chmod(d, 0o750 if d == "/etc/jabali-security" else 0o700)
        if os.path.isfile(config_file):
            os.chown(config_file, sec_uid, www_gid)
            os.chmod(config_file, 0o640)
        # Nginx config dirs
        for d in ["/etc/nginx/jabali", "/etc/nginx/jabali-security"]:
            if os.path.isdir(d):
                for root_dir, dirs, files in os.walk(d):
                    os.chown(root_dir, sec_uid, 0)
                    for f in files:
                        os.chown(os.path.join(root_dir, f), sec_uid, 0)
        click.echo("  Directory ownership updated.")
    except (KeyError, PermissionError) as exc:
        click.echo("  WARNING: could not update ownership: %s" % exc)

    # Restart services
    click.echo("Restarting services...")
    try:
        subprocess.run(["/usr/bin/systemctl", "restart", "jabali-security"], capture_output=True, timeout=30)  # noqa: S603
    except subprocess.TimeoutExpired:
        click.echo("  Daemon restart timed out (will finish in background).")
    # Clear Laravel caches + restart panel (route/view cache has stale references)
    if os.path.isdir("/var/www/jabali/app/JabaliSecurity"):
        # Regenerate autoload so Filament discovers the updated plugin classes
        subprocess.run(  # noqa: S603
            ["/usr/local/bin/composer", "dump-autoload", "-q"],
            cwd="/var/www/jabali", capture_output=True, timeout=15,
        )
        # Clear Laravel route/view cache + Filament component cache
        for artisan_cmd in [["filament:cache-components"], ["view:clear"]]:
            subprocess.run(  # noqa: S603
                ["/usr/bin/php", "artisan"] + artisan_cmd,
                cwd="/var/www/jabali", capture_output=True, timeout=15,
            )
        try:
            subprocess.run(["/usr/bin/systemctl", "restart", "jabali-panel"], capture_output=True, timeout=60)  # noqa: S603
        except subprocess.TimeoutExpired:
            click.echo("  Panel restart timed out (will finish in background).")

    # Persist the installed SHA so the next invocation can no-op when unchanged.
    if new_sha:
        try:
            os.makedirs(INSTALL_DIR, exist_ok=True)
            with open(VERSION_FILE, "w") as fh:
                fh.write(new_sha + "\n")
        except OSError as exc:
            click.echo("  WARNING: could not write version marker: %s" % exc)

    click.echo("Updated successfully. Services restarted.")


@cli.command()
def stop() -> None:
    """Stop the running daemon."""
    pid = _read_pid()
    if pid is None:
        click.echo("Jabali Security is not running (no PID file found).")
        sys.exit(1)

    if not _verify_pid_is_ours(pid):
        click.echo("PID %d does not belong to jabali-security; removing stale PID file." % pid)
        _remove_pid()
        sys.exit(1)

    try:
        os.kill(pid, signal.SIGTERM)
        click.echo("Sent SIGTERM to PID %d." % pid)
    except ProcessLookupError:
        click.echo("Process %d not found; removing stale PID file." % pid)
        _remove_pid()
        sys.exit(1)
    except PermissionError:
        click.echo("Permission denied sending signal to PID %d." % pid)
        sys.exit(1)


@cli.command()
@click.option("--json", "as_json", is_flag=True, help="Output as JSON")
def status(as_json: bool) -> None:
    """Show daemon status."""
    pid = _read_pid()
    if pid is None:
        if as_json:
            click.echo(json.dumps({"running": False}))
        else:
            click.echo("Jabali Security is not running.")
        sys.exit(0)

    config = load_config()

    try:
        data = _api_call(config, "GET", "/api/v1/status")
    except SystemExit:
        if as_json:
            click.echo(json.dumps({"running": False, "pid": pid, "error": "API unreachable"}))
        else:
            click.echo("Jabali Security PID %d found but API is unreachable." % pid)
        sys.exit(1)

    if as_json:
        click.echo(json.dumps(data, indent=2))
    else:
        click.echo("Jabali Security v%s" % data.get("version", "?"))
        click.echo("  Status:     running (PID %d)" % pid)
        uptime = int(data.get("uptime_seconds", 0))
        hours, remainder = divmod(uptime, 3600)
        minutes, seconds = divmod(remainder, 60)
        click.echo("  Uptime:     %dh %dm %ds" % (hours, minutes, seconds))
        click.echo("  Workers:    %s" % data.get("workers", "?"))
        click.echo("  Queue:      %s pending" % data.get("scan_queue_size", "?"))
        click.echo("  Watched:    %s dirs" % data.get("watched_dirs", "?"))
        click.echo("  Incidents:  %s (24h)" % data.get("incidents_24h", "?"))
        click.echo("  Quarantine: %s files" % data.get("quarantined_count", "?"))
        click.echo("  Memory:     %s MB" % data.get("memory_mb", "?"))


# -- scan command ------------------------------------------------------------

@cli.command()
@click.argument("path")
@click.option("--recursive", "-r", is_flag=True, help="Scan directory recursively")
@click.option("--json", "as_json", is_flag=True, help="Output as JSON")
def scan(path: str, recursive: bool, as_json: bool) -> None:
    """Scan a file or directory for threats."""
    config = load_config()
    target = Path(path)

    if not target.exists():
        click.echo("Path not found: %s" % path, err=True)
        sys.exit(1)

    # Try the daemon first; fall back to standalone
    use_daemon = _daemon_is_reachable(config)

    if use_daemon:
        data = _api_call(config, "POST", "/api/v1/scan", {
            "path": str(target.resolve()),
            "recursive": recursive,
        })
        if as_json:
            click.echo(json.dumps(data, indent=2))
        else:
            results = data.get("results", [data]) if isinstance(data, dict) else data
            for r in results:
                _print_scan_result(r)
        return

    # Standalone scan
    if target.is_dir():
        if not recursive:
            click.echo("Target is a directory. Use --recursive / -r to scan recursively.", err=True)
            sys.exit(1)
        results = _scan_directory(str(target), config)
    else:
        result = _standalone_scan(str(target), config)
        results = [result] if result else []

    if as_json:
        click.echo(json.dumps(results, indent=2))
    else:
        if not results:
            click.echo("No scannable files found.")
        for r in results:
            _print_scan_result(r)


# -- incidents group ---------------------------------------------------------

@cli.group()
def incidents():
    """Manage security incidents."""


@incidents.command("list")
@click.option("--limit", "-n", default=20, help="Max results")
@click.option("--user", default=None, help="Filter by username")
@click.option("--severity", default=None, type=click.Choice(["low", "medium", "high", "critical"]))
@click.option("--json", "as_json", is_flag=True)
def incidents_list(limit: int, user: str | None, severity: str | None, as_json: bool) -> None:
    """List security incidents."""
    config = load_config()
    params: list[str] = ["limit=%d" % limit]
    if user:
        params.append("user=%s" % user)
    if severity:
        params.append("severity=%s" % severity)
    query = "&".join(params)
    data = _api_call(config, "GET", "/api/v1/incidents?%s" % query)

    if as_json:
        click.echo(json.dumps(data, indent=2))
        return

    items = data if isinstance(data, list) else data.get("incidents", [])
    if not items:
        click.echo("No incidents found.")
        return

    click.echo("%-16s  %-8s  %5s  %-10s  %-30s  %s" % ("ID", "Severity", "Score", "User", "Path", "Action"))
    for item in items:
        click.echo("%-16s  %-8s  %5s  %-10s  %-30s  %s" % (
            item.get("id", "")[:16],
            item.get("severity", ""),
            item.get("total_score", ""),
            item.get("username", "") or "",
            item.get("file_event", {}).get("path", "")[:30],
            item.get("action_taken", ""),
        ))


# -- quarantine group --------------------------------------------------------

@cli.group()
def quarantine():
    """Manage quarantined files."""


@quarantine.command("list")
@click.option("--user", default=None, help="Filter by username")
@click.option("--json", "as_json", is_flag=True)
def quarantine_list(user: str | None, as_json: bool) -> None:
    """List quarantined files."""
    config = load_config()
    query = "?user=%s" % user if user else ""
    data = _api_call(config, "GET", "/api/v1/quarantine%s" % query)

    if as_json:
        click.echo(json.dumps(data, indent=2))
        return

    items = data if isinstance(data, list) else data.get("records", [])
    if not items:
        click.echo("No quarantined files found.")
        return

    click.echo("%-16s  %-10s  %-30s  %s" % ("ID", "User", "Original Path", "Quarantined At"))
    for item in items:
        click.echo("%-16s  %-10s  %-30s  %s" % (
            item.get("id", "")[:16],
            item.get("username", "") or "",
            item.get("original_path", "")[:30],
            item.get("timestamp", ""),
        ))


@quarantine.command()
@click.argument("record_id")
def restore(record_id: str) -> None:
    """Restore a quarantined file."""
    config = load_config()
    data = _api_call(config, "POST", "/api/v1/quarantine/%s/restore" % record_id)
    click.echo(data.get("message", "Restored successfully."))


@quarantine.command()
@click.argument("record_id")
def delete(record_id: str) -> None:
    """Delete a quarantined file permanently."""
    config = load_config()
    data = _api_call(config, "DELETE", "/api/v1/quarantine/%s" % record_id)
    click.echo(data.get("message", "Deleted successfully."))


# -- config group ------------------------------------------------------------

@cli.group("config")
def config_group():
    """View and update configuration."""


@config_group.command("show")
def config_show() -> None:
    """Show current configuration."""
    config = load_config()

    if _daemon_is_reachable(config):
        data = _api_call(config, "GET", "/api/v1/config")
        for key, value in sorted(data.items()):
            click.echo("%s = %s" % (key, value))
    else:
        for key, value in sorted(dataclasses.asdict(config).items()):
            click.echo("%s = %s" % (key, value))


@config_group.command("set")
@click.argument("key")
@click.argument("value")
def config_set(key: str, value: str) -> None:
    """Set a configuration value."""
    config = load_config()

    # Always persist to config file
    conf_key = key.upper()
    update_conf_key(CONFIG_FILE, conf_key, value)
    click.echo("Updated %s in config file." % conf_key)

    # Also push to daemon if running
    if _daemon_is_reachable(config):
        try:
            _api_request(config, "PATCH", "/api/v1/config", {conf_key: value})
            click.echo("Applied to running daemon.")
        except (URLError, ConnectionRefusedError, OSError):
            click.echo("Note: daemon not reachable, change will apply on next start.")


@config_group.command("test")
def config_test() -> None:
    """Validate the configuration file."""
    try:
        config = load_config()
    except Exception as exc:
        click.echo("Configuration error: %s" % exc, err=True)
        sys.exit(1)

    click.echo("Configuration file: %s" % CONFIG_FILE)
    click.echo("  Log level:     %s" % config.log_level)
    click.echo("  API bind:      %s:%d" % (config.api_bind, config.api_port))
    click.echo("  Workers:       %d" % config.workers)
    click.echo("  Watch dirs:    %s" % ", ".join(config.watch_dirs))
    click.echo("  Scan ext:      %s" % ", ".join(config.scan_extensions))
    click.echo("  Max file size: %d bytes" % config.max_file_size)
    click.echo("  ClamAV:        %s" % config.clamav_enabled)

    warnings: list[str] = []
    if not config.api_key:
        warnings.append("API_KEY is empty -- API will be unauthenticated")
    if config.api_bind and config.api_bind not in ("127.0.0.1", "::1", "localhost"):
        warnings.append("API_BIND is not loopback -- API exposed to network")
    if config.auto_suspend:
        warnings.append("AUTO_SUSPEND is enabled -- accounts may be suspended automatically")

    if warnings:
        click.echo("")
        click.echo("Warnings:")
        for w in warnings:
            click.echo("  - %s" % w)
    else:
        click.echo("")
        click.echo("Configuration OK.")


# -- rules group -------------------------------------------------------------

@cli.group()
def rules():
    """Manage detection rules."""


@rules.command("list")
def rules_list() -> None:
    """List loaded detection rules."""
    config = load_config()
    data = _api_call(config, "GET", "/api/v1/rules")

    scanners = data.get("scanners", [])
    yara_rules = data.get("yara_rules", [])

    click.echo("Active scanners: %s" % ", ".join(scanners) if scanners else "none")
    click.echo("YARA rules dir:  %s" % data.get("yara_rules_dir", "?"))
    click.echo("ClamAV enabled:  %s" % data.get("clamav_enabled", False))
    click.echo("")
    if yara_rules:
        click.echo("YARA rule files:")
        for rule in yara_rules:
            click.echo("  %s (%d bytes)" % (rule.get("name", ""), rule.get("size", 0)))
    else:
        click.echo("No YARA rules loaded.")


@rules.command("update")
def rules_update() -> None:
    """Reload YARA rules and update ClamAV signatures."""
    config = load_config()
    data = _api_call(config, "POST", "/api/v1/rules/reload")
    click.echo(data.get("message", "Rules reloaded."))


# -- user group --------------------------------------------------------------

@cli.group()
def user():
    """User risk management."""


@user.command("list")
@click.option("--min-score", default=0, help="Minimum risk score")
@click.option("--json", "as_json", is_flag=True)
def user_list(min_score: int, as_json: bool) -> None:
    """List users with their risk scores."""
    config = load_config()
    data = _api_call(config, "GET", "/api/v1/users")

    users = data if isinstance(data, list) else data.get("users", [])
    if min_score > 0:
        users = [u for u in users if u.get("risk_score", 0) >= min_score]

    if as_json:
        click.echo(json.dumps(users, indent=2))
        return

    if not users:
        click.echo("No users found.")
        return

    click.echo("%-15s  %5s  %s" % ("Username", "Score", "Status"))
    for u in users:
        click.echo("%-15s  %5d  %s" % (
            u.get("username", ""),
            u.get("risk_score", 0),
            u.get("status", ""),
        ))


@user.command("risk")
@click.argument("username")
@click.option("--json", "as_json", is_flag=True)
def user_risk(username: str, as_json: bool) -> None:
    """Show risk profile for a specific user."""
    config = load_config()
    data = _api_call(config, "GET", "/api/v1/users/%s" % username)

    if as_json:
        click.echo(json.dumps(data, indent=2))
        return

    click.echo("User:       %s" % data.get("username", username))
    click.echo("Risk score: %d" % data.get("risk_score", 0))
    click.echo("Status:     %s" % data.get("status", ""))
    click.echo("Incidents:  %d" % data.get("incident_count", 0))

    recent = data.get("recent_incidents", [])
    if recent:
        click.echo("")
        click.echo("Recent incidents:")
        for inc in recent:
            click.echo("  [%s] %s - %s (%s)" % (
                inc.get("severity", ""),
                inc.get("id", "")[:16],
                inc.get("action_taken", ""),
                inc.get("timestamp", ""),
            ))


# -- brute-force group -------------------------------------------------------

@cli.group()
def whitelist():
    """IP whitelist management (IPs that are never blocked)."""


@whitelist.command("list")
@click.option("--json", "as_json", is_flag=True, help="Output as JSON")
def whitelist_list(as_json: bool) -> None:
    """List whitelisted IPs."""
    config = load_config()
    data = _api_call(config, "GET", "/api/v1/bruteforce/whitelist")
    ips = data.get("whitelist", [])
    if as_json:
        click.echo(json.dumps(data, indent=2))
        return
    if not ips:
        click.echo("No whitelisted IPs.")
        return
    click.echo("Whitelisted IPs (%d):" % len(ips))
    for ip in ips:
        click.echo("  %s" % ip)


@whitelist.command("add")
@click.argument("ip")
def whitelist_add(ip: str) -> None:
    """Add an IP to the whitelist."""
    config = load_config()
    _api_call(config, "POST", "/api/v1/bruteforce/whitelist", {"ip": ip})
    click.echo("IP %s added to whitelist." % ip)


@whitelist.command("remove")
@click.argument("ip")
def whitelist_remove(ip: str) -> None:
    """Remove an IP from the whitelist."""
    config = load_config()
    _api_call(config, "DELETE", "/api/v1/bruteforce/whitelist/%s" % ip)
    click.echo("IP %s removed from whitelist." % ip)


# -- WAF group ---------------------------------------------------------------

@cli.group()
def waf():
    """WAF (ModSecurity) management."""


@waf.command("events")
@click.option("--limit", "-n", default=20, help="Max results")
@click.option("--ip", default=None, help="Filter by client IP")
@click.option("--rule-id", default=None, type=int, help="Filter by rule ID")
@click.option("--json", "as_json", is_flag=True)
def waf_events(limit: int, ip: str | None, rule_id: int | None, as_json: bool) -> None:
    """List recent WAF events."""
    config = load_config()
    params: list[str] = ["limit=%d" % limit]
    if ip:
        params.append("ip=%s" % ip)
    if rule_id is not None:
        params.append("rule_id=%d" % rule_id)
    query = "&".join(params)
    data = _api_call(config, "GET", "/api/v1/waf/events?%s" % query)

    if as_json:
        click.echo(json.dumps(data, indent=2))
        return

    items = data if isinstance(data, list) else []
    if not items:
        click.echo("No WAF events found.")
        return

    click.echo("%-18s  %-6s  %-8s  %-8s  %-30s  %s" % ("Client IP", "Method", "Rule ID", "Action", "URI", "Time"))
    for item in items:
        click.echo("%-18s  %-6s  %-8s  %-8s  %-30s  %s" % (
            item.get("client_ip", ""),
            item.get("method", ""),
            item.get("rule_id", ""),
            item.get("action", ""),
            item.get("uri", "")[:30],
            item.get("created_at", ""),
        ))


@waf.command("rules")
@click.option("--json", "as_json", is_flag=True)
def waf_rules(as_json: bool) -> None:
    """List CRS rule files and disabled rules."""
    config = load_config()
    data = _api_call(config, "GET", "/api/v1/waf/rules")

    if as_json:
        click.echo(json.dumps(data, indent=2))
        return

    click.echo("Web server: %s" % data.get("web_server", "unknown"))
    click.echo("")

    rule_files = data.get("rule_files", [])
    if rule_files:
        click.echo("CRS rule files:")
        for rf in rule_files:
            click.echo("  %s (%d bytes)" % (rf.get("file", ""), rf.get("size", 0)))
    else:
        click.echo("No CRS rule files found.")

    disabled = data.get("disabled_rules", [])
    click.echo("")
    if disabled:
        click.echo("Disabled rules: %s" % ", ".join(str(r) for r in disabled))
    else:
        click.echo("No rules disabled.")


@waf.command("disable")
@click.argument("rule_id", type=int)
def waf_disable(rule_id: int) -> None:
    """Disable a ModSecurity rule by ID."""
    config = load_config()
    data = _api_call(config, "POST", "/api/v1/waf/rules/%d/disable" % rule_id)
    reloaded = data.get("web_server_reloaded", False)
    click.echo("Rule %d disabled.%s" % (rule_id, " Web server reloaded." if reloaded else ""))


@waf.command("enable")
@click.argument("rule_id", type=int)
def waf_enable(rule_id: int) -> None:
    """Enable a previously disabled ModSecurity rule."""
    config = load_config()
    data = _api_call(config, "POST", "/api/v1/waf/rules/%d/enable" % rule_id)
    reloaded = data.get("web_server_reloaded", False)
    click.echo("Rule %d enabled.%s" % (rule_id, " Web server reloaded." if reloaded else ""))


@waf.command("stats")
@click.option("--json", "as_json", is_flag=True)
def waf_stats(as_json: bool) -> None:
    """Show WAF statistics."""
    config = load_config()
    data = _api_call(config, "GET", "/api/v1/waf/stats")

    if as_json:
        click.echo(json.dumps(data, indent=2))
        return

    click.echo("WAF statistics (last 24 hours):")
    click.echo("  Total events:  %s" % data.get("total_events_24h", 0))
    click.echo("  Blocked:       %s" % data.get("blocked_24h", 0))
    click.echo("")

    top_ips = data.get("top_ips", [])
    if top_ips:
        click.echo("Top IPs:")
        for entry in top_ips:
            click.echo("  %-18s  %d events" % (entry.get("ip", ""), entry.get("count", 0)))

    top_rules = data.get("top_rules", [])
    if top_rules:
        click.echo("")
        click.echo("Top rules:")
        for entry in top_rules:
            click.echo("  %-8s  %d hits  %s" % (
                entry.get("rule_id", ""),
                entry.get("count", 0),
                entry.get("rule_msg", ""),
            ))


@waf.command("update")
def waf_update() -> None:
    """Update OWASP Core Rule Set."""
    config = load_config()
    click.echo("Updating OWASP CRS...")
    data = _api_call(config, "POST", "/api/v1/waf/crs/update")
    if data.get("success"):
        click.echo("CRS updated to %s (%d rule files)." % (
            data.get("version", "?"),
            data.get("rules_count", 0),
        ))
    else:
        click.echo("CRS update failed: %s" % data.get("error", "unknown error"), err=True)


# -- proactive group ---------------------------------------------------------

@cli.group()
def proactive():
    """Proactive defense management."""


@proactive.command("status")
@click.option("--json", "as_json", is_flag=True, help="Output as JSON")
def proactive_status(as_json: bool) -> None:
    """Show proactive defense status."""
    config = load_config()
    data = _api_call(config, "GET", "/api/v1/proactive/status")

    if as_json:
        click.echo(json.dumps(data, indent=2))
        return

    click.echo("Proactive defense status:")
    click.echo("  Process kill enabled:  %s" % data.get("process_kill_enabled", False))
    click.echo("  Process kill count:    %s" % data.get("process_kill_count", 0))


@proactive.command("kills")
@click.option("--json", "as_json", is_flag=True, help="Output as JSON")
def proactive_kills(as_json: bool) -> None:
    """List recent process kills."""
    config = load_config()
    data = _api_call(config, "GET", "/api/v1/proactive/kills")

    if as_json:
        click.echo(json.dumps(data, indent=2))
        return

    records = data if isinstance(data, list) else []
    if not records:
        click.echo("No process kills recorded.")
        return

    click.echo("%-16s  %6s  %6s  %-10s  %5s  %-8s  %s" % (
        "ID", "PID", "PPID", "User", "Score", "Success", "Reason",
    ))
    for rec in records:
        click.echo("%-16s  %6d  %6d  %-10s  %5d  %-8s  %s" % (
            rec.get("id", "")[:16],
            rec.get("pid", 0),
            rec.get("ppid", 0),
            rec.get("username", "") or "",
            rec.get("score", 0),
            "yes" if rec.get("success") else "FAIL",
            rec.get("reason", "")[:40],
        ))


# -- block / unblock / blocklist commands ------------------------------------

@cli.command()
@click.argument("ip")
@click.option("--reason", default="manual", help="Reason for blocking")
@click.option("--duration", default=0, type=int, help="Duration in seconds (0=permanent)")
def block(ip: str, reason: str, duration: int) -> None:
    """Block an IP address."""
    config = load_config()
    data = _api_call(config, "POST", "/api/v1/block", {
        "ip": ip,
        "reason": reason,
        "duration": duration,
    })
    click.echo(data.get("message", "IP %s blocked." % ip))


@cli.command()
@click.argument("ip")
def unblock(ip: str) -> None:
    """Unblock an IP address."""
    config = load_config()
    data = _api_call(config, "DELETE", "/api/v1/block/%s" % ip)
    click.echo(data.get("message", "IP %s unblocked." % ip))


@cli.command()
@click.option("--json", "as_json", is_flag=True)
def blocklist(as_json: bool) -> None:
    """List blocked IP addresses."""
    config = load_config()
    data = _api_call(config, "GET", "/api/v1/blocklist")

    if as_json:
        click.echo(json.dumps(data, indent=2))
        return

    items = data.get("blocked_ips", []) if isinstance(data, dict) else data
    if not items:
        click.echo("No blocked IPs.")
        return

    click.echo("%-18s  %-12s  %-10s  %s" % ("IP", "Reason", "Duration", "Blocked At"))
    for item in items:
        dur = item.get("duration", 0)
        dur_str = "permanent" if dur == 0 else "%ds" % dur
        click.echo("%-18s  %-12s  %-10s  %s" % (
            item.get("ip", ""),
            item.get("reason", ""),
            dur_str,
            item.get("blocked_at", ""),
        ))


# -- cleanup group -----------------------------------------------------------

@cli.group()
def cleanup():
    """Malware cleanup management."""


@cleanup.command("records")
@click.option("--json", "as_json", is_flag=True, help="Output as JSON")
def cleanup_records(as_json: bool) -> None:
    """List recent cleanup operations."""
    config = load_config()
    data = _api_call(config, "GET", "/api/v1/cleanup/records")

    if as_json:
        click.echo(json.dumps(data, indent=2))
        return

    items = data if isinstance(data, list) else []
    if not items:
        click.echo("No cleanup records found.")
        return

    click.echo("%-16s  %-8s  %-10s  %-8s  %-30s  %s" % ("ID", "Strategy", "User", "Success", "Path", "Time"))
    for item in items:
        click.echo("%-16s  %-8s  %-10s  %-8s  %-30s  %s" % (
            item.get("id", "")[:16],
            item.get("strategy", ""),
            item.get("username", "") or "",
            "yes" if item.get("success") else "FAIL",
            item.get("path", "")[:30],
            item.get("created_at", ""),
        ))


@cleanup.command("file")
@click.argument("path")
@click.option("--json", "as_json", is_flag=True, help="Output as JSON")
def cleanup_file(path: str, as_json: bool) -> None:
    """Manually clean a specific file."""
    config = load_config()
    p = Path(path)
    if not p.is_file():
        click.echo("File not found: %s" % path, err=True)
        sys.exit(1)

    if _daemon_is_reachable(config):
        data = _api_call(config, "POST", "/api/v1/cleanup/file", {"path": str(p.resolve())})
        if as_json:
            click.echo(json.dumps(data, indent=2))
        else:
            success = data.get("success", False)
            click.echo("Cleanup %s: %s" % ("succeeded" if success else "failed", str(p.resolve())))
            if data.get("changes_made"):
                click.echo("  Changes: %d" % len(data["changes_made"]))
            if data.get("error"):
                click.echo("  Error: %s" % data["error"])
    else:
        click.echo("Daemon is not running. Start the daemon to use cleanup.", err=True)
        sys.exit(1)


@cleanup.command("cms")
@click.argument("path")
@click.option("--json", "as_json", is_flag=True, help="Output as JSON")
def cleanup_cms(path: str, as_json: bool) -> None:
    """Check CMS integrity and clean infections."""
    config = load_config()
    p = Path(path)
    if not p.is_dir():
        click.echo("Directory not found: %s" % path, err=True)
        sys.exit(1)

    if _daemon_is_reachable(config):
        data = _api_call(config, "POST", "/api/v1/cleanup/file", {"path": str(p.resolve())})
        if as_json:
            click.echo(json.dumps(data, indent=2))
        else:
            click.echo("CMS cleanup for: %s" % str(p.resolve()))
            success = data.get("success", False)
            click.echo("  Result: %s" % ("clean" if success else "failed"))
    else:
        click.echo("Daemon is not running. Start the daemon to use CMS cleanup.", err=True)
        sys.exit(1)


# -- threat-intel group ------------------------------------------------------

@cli.group("threat-intel")
def threat_intel():
    """Threat intelligence feed management."""


@threat_intel.command("feeds")
@click.option("--json", "as_json", is_flag=True, help="Output as JSON")
def threat_intel_feeds(as_json: bool) -> None:
    """List threat intelligence feed statuses."""
    config = load_config()
    data = _api_call(config, "GET", "/api/v1/threat-intel/feeds")

    if as_json:
        click.echo(json.dumps(data, indent=2))
        return

    items = data if isinstance(data, list) else []
    if not items:
        click.echo("No threat intelligence feeds configured.")
        return

    click.echo("%-25s  %-6s  %8s  %s" % ("Feed", "Type", "Entries", "Last Update"))
    for item in items:
        click.echo("%-25s  %-6s  %8s  %s" % (
            item.get("name", ""),
            item.get("feed_type", ""),
            item.get("entry_count", 0),
            item.get("last_update", "never") or "never",
        ))


@threat_intel.command("update")
def threat_intel_update() -> None:
    """Trigger an immediate update of all enabled feeds."""
    config = load_config()
    click.echo("Updating threat intelligence feeds...")
    data = _api_call(config, "POST", "/api/v1/threat-intel/update")
    success = data.get("success_count", 0)
    total = data.get("total_count", 0)
    click.echo("Feed update complete: %d/%d succeeded." % (success, total))

    updated = data.get("updated", {})
    for name, ok in updated.items():
        status = "OK" if ok else "FAILED"
        click.echo("  %s: %s" % (name, status))


@threat_intel.command("check-ip")
@click.argument("ip")
@click.option("--json", "as_json", is_flag=True, help="Output as JSON")
def threat_intel_check_ip(ip: str, as_json: bool) -> None:
    """Check an IP address against threat intelligence feeds."""
    config = load_config()
    data = _api_call(config, "GET", "/api/v1/threat-intel/check/ip/%s" % ip)

    if as_json:
        click.echo(json.dumps(data, indent=2))
        return

    is_bad = data.get("is_malicious", False)
    score = data.get("score", 0)
    feeds = data.get("feeds", [])

    if is_bad:
        click.echo("MALICIOUS: %s (score: %d)" % (ip, score))
        click.echo("  Matched feeds: %s" % ", ".join(feeds))
    else:
        click.echo("CLEAN: %s (score: %d)" % (ip, score))


@threat_intel.command("check-hash")
@click.argument("sha256")
@click.option("--remote", is_flag=True, help="Also check remote APIs (slower)")
@click.option("--json", "as_json", is_flag=True, help="Output as JSON")
def threat_intel_check_hash(sha256: str, remote: bool, as_json: bool) -> None:
    """Check a SHA-256 hash against threat intelligence feeds."""
    config = load_config()
    query = "?remote=1" if remote else ""
    data = _api_call(config, "GET", "/api/v1/threat-intel/check/hash/%s%s" % (sha256, query))

    if as_json:
        click.echo(json.dumps(data, indent=2))
        return

    is_bad = data.get("is_malicious", False)
    score = data.get("score", 0)
    feeds = data.get("feeds", [])

    if is_bad:
        click.echo("MALICIOUS: %s (score: %d)" % (sha256, score))
        click.echo("  Matched feeds: %s" % ", ".join(feeds))
        details = data.get("details", {})
        if details:
            click.echo("  Signature: %s" % details.get("signature", "unknown"))
            click.echo("  File type: %s" % details.get("file_type", "unknown"))
    else:
        click.echo("CLEAN: %s (score: %d)" % (sha256, score))


# -- crowdsec group ----------------------------------------------------------

@cli.group("crowdsec")
def crowdsec_cli():
    """CrowdSec community threat intelligence."""


@crowdsec_cli.command("status")
@click.option("--json", "as_json", is_flag=True, help="Output as JSON")
def crowdsec_status(as_json: bool) -> None:
    """Show CrowdSec integration status."""
    config = load_config()
    data = _api_call(config, "GET", "/api/v1/crowdsec/status")
    if as_json:
        click.echo(json.dumps(data, indent=2))
        return
    connected = data.get("connected", False)
    click.echo("CrowdSec integration:")
    click.echo("  Enabled:     %s" % ("yes" if data.get("enabled") else "no"))
    click.echo("  Connected:   %s" % ("yes" if connected else "no"))
    click.echo("  LAPI URL:    %s" % data.get("lapi_url", "?"))
    click.echo("  Decisions:   %d" % data.get("active_decisions", 0))
    click.echo("  Blocked IPs: %d" % data.get("blocked_ips", 0))
    click.echo("  Last poll:   %s" % (data.get("last_poll") or "never"))
    if data.get("error"):
        click.echo("  Error:       %s" % data["error"])


@crowdsec_cli.command("decisions")
@click.option("--json", "as_json", is_flag=True, help="Output as JSON")
def crowdsec_decisions(as_json: bool) -> None:
    """List active CrowdSec decisions (banned IPs)."""
    config = load_config()
    data = _api_call(config, "GET", "/api/v1/crowdsec/decisions")
    decisions = data.get("decisions", [])
    if as_json:
        click.echo(json.dumps(decisions, indent=2))
        return
    if not decisions:
        click.echo("No active CrowdSec decisions.")
        return
    click.echo("Active CrowdSec decisions (%d):" % len(decisions))
    for d in decisions:
        click.echo("  %-18s %-6s %-12s %s" % (
            d.get("value", "?"),
            d.get("type", "?"),
            d.get("duration", "?")[:12],
            d.get("scenario", "?"),
        ))


@crowdsec_cli.command("check")
@click.argument("ip")
@click.option("--json", "as_json", is_flag=True, help="Output as JSON")
def crowdsec_check(ip: str, as_json: bool) -> None:
    """Check an IP against CrowdSec decisions."""
    config = load_config()
    data = _api_call(config, "GET", "/api/v1/crowdsec/check/%s" % ip)
    if as_json:
        click.echo(json.dumps(data, indent=2))
        return
    click.echo("IP: %s" % data.get("ip", ip))
    click.echo("Score: %d" % data.get("score", 0))
    click.echo("Blocked: %s" % ("yes" if data.get("is_blocked") else "no"))
    cached = data.get("cached_decisions", [])
    if cached:
        click.echo("Cached decisions:")
        for d in cached:
            click.echo("  %s — %s (%s)" % (d.get("type", "?"), d.get("scenario", "?"), d.get("duration", "?")))


@cli.command("scan-full")
def scan_full() -> None:
    """Trigger a full scheduled scan now."""
    config = load_config()
    data = _api_call(config, "POST", "/api/v1/scan/full")
    started = data.get("started", False)
    if started:
        click.echo("Full scan started.")
    else:
        click.echo("Failed to start full scan.", err=True)
        sys.exit(1)


# -- scan-db command ---------------------------------------------------------

_VALID_DB_NAME_CLI = __import__("re").compile(r"^[a-zA-Z0-9_]+$")


@cli.command("scan-db")
@click.argument("database")
@click.option("--user", default="root", help="MySQL user")
@click.option("--host", default="localhost", help="MySQL host")
@click.option("--cms", default="wordpress", type=click.Choice(["wordpress", "joomla"]))
@click.option("--prefix", default="wp_", help="Table prefix")
@click.option("--json", "as_json", is_flag=True)
def scan_db(database: str, user: str, host: str, cms: str, prefix: str, as_json: bool) -> None:
    """Scan a MySQL database for malware."""
    if not _VALID_DB_NAME_CLI.match(database):
        click.echo("Invalid database name (alphanumeric and underscores only).", err=True)
        sys.exit(1)

    config = load_config()

    if _daemon_is_reachable(config):
        data = _api_call(config, "POST", "/api/v1/scan/database", {
            "database": database,
            "user": user,
            "host": host,
            "cms_type": cms,
            "table_prefix": prefix,
        })
        if as_json:
            click.echo(json.dumps(data, indent=2))
        else:
            count = data.get("findings_count", 0)
            click.echo("Database: %s" % database)
            click.echo("Findings: %d" % count)
            for f in data.get("findings", []):
                click.echo("  [%s] %s.%s row %s: %s" % (
                    f.get("pattern", ""),
                    f.get("table", ""),
                    f.get("column", ""),
                    f.get("row_id", ""),
                    f.get("description", ""),
                ))
        return

    # Standalone scan without daemon
    from lib.scanner.database import DatabaseScanner
    scanner = DatabaseScanner(enabled=True)
    findings = asyncio.run(scanner.scan_database(database, user, host, cms, prefix))

    if as_json:
        click.echo(json.dumps({"database": database, "findings_count": len(findings), "findings": findings}, indent=2))
    else:
        click.echo("Database: %s" % database)
        click.echo("Findings: %d" % len(findings))
        for f in findings:
            click.echo("  [%s] %s.%s row %s: %s" % (
                f.get("pattern", ""),
                f.get("table", ""),
                f.get("column", ""),
                f.get("row_id", ""),
                f.get("description", ""),
            ))


# -- scan-rapid command ------------------------------------------------------

@cli.command("scan-rapid")
@click.argument("path")
@click.option("--workers", "-w", default=4, help="Parallel workers")
@click.option("--json", "as_json", is_flag=True)
def scan_rapid(path: str, workers: int, as_json: bool) -> None:
    """Fast parallel scan with mtime cache."""
    target = Path(path)
    if not target.is_dir():
        click.echo("Directory not found: %s" % path, err=True)
        sys.exit(1)

    config = load_config()

    if _daemon_is_reachable(config):
        data = _api_call(config, "POST", "/api/v1/scan/rapid", {"path": str(target.resolve())})
        if as_json:
            click.echo(json.dumps(data, indent=2))
        else:
            click.echo("RapidScan: %s" % data.get("directory", path))
            click.echo("  Files scanned:  %d" % data.get("files_scanned", 0))
            click.echo("  Files skipped:  %d (unchanged)" % data.get("files_skipped", 0))
            click.echo("  Threats found:  %d" % data.get("threats_found", 0))
            for r in data.get("results", []):
                click.echo("  [%d] %s -> %s" % (r.get("score", 0), r.get("path", ""), r.get("action", "")))
        return

    # Standalone scan without daemon
    from lib.rapidscan import RapidScanEngine
    from lib.scanner import ScanOrchestrator
    from lib.scoring import ScoringEngine

    scanner = ScanOrchestrator(config)
    scoring = ScoringEngine(config)
    engine = RapidScanEngine(config, workers=workers)
    if config.rapidscan_mtime_cache:
        cache_path = Path(config.data_dir) / "rapidscan_mtime.json"
        engine.set_cache_path(cache_path)

    result = asyncio.run(engine.scan_directory(str(target.resolve()), scanner, scoring))

    if as_json:
        click.echo(json.dumps(result, indent=2))
    else:
        click.echo("RapidScan: %s" % result.get("directory", path))
        click.echo("  Files scanned:  %d" % result.get("files_scanned", 0))
        click.echo("  Files skipped:  %d (unchanged)" % result.get("files_skipped", 0))
        click.echo("  Threats found:  %d" % result.get("threats_found", 0))
        for r in result.get("results", []):
            click.echo("  [%d] %s -> %s" % (r.get("score", 0), r.get("path", ""), r.get("action", "")))


# -- webshield group ---------------------------------------------------------

@cli.group()
def webshield():
    """WebShield bot filtering management."""


@webshield.command("status")
@click.option("--json", "as_json", is_flag=True, help="Output as JSON")
def webshield_status(as_json: bool) -> None:
    """Show WebShield installation status."""
    config = load_config()
    data = _api_call(config, "GET", "/api/v1/webshield/status")

    if as_json:
        click.echo(json.dumps(data, indent=2))
        return

    click.echo("WebShield status:")
    click.echo("  Installed:      %s" % ("yes" if data.get("installed") else "no"))
    click.echo("  Nginx available: %s" % ("yes" if data.get("nginx_available") else "no"))
    click.echo("  Rate limiting:  %s" % ("active" if data.get("rate_limiting") else "inactive"))
    click.echo("  Bot filtering:  %s" % ("active" if data.get("bot_filtering") else "inactive"))
    click.echo("  Challenge:      %s" % ("enabled" if data.get("challenge_enabled") else "disabled"))
    click.echo("  Blocked IPs:    %s" % data.get("blocked_ips_count", 0))
    click.echo("  Config dir:     %s" % data.get("config_dir", ""))


@webshield.command("install")
def webshield_install() -> None:
    """Install WebShield nginx configuration files."""
    config = load_config()
    click.echo("Installing WebShield nginx configs...")
    data = _api_call(config, "POST", "/api/v1/webshield/install")

    files = data.get("files_written", [])
    if files:
        click.echo("Files written:")
        for f in files:
            click.echo("  %s" % f)

    valid = data.get("nginx_config_valid")
    if valid is True:
        click.echo("Nginx config test: OK")
    elif valid is False:
        click.echo("Nginx config test: FAILED (check nginx -t output)")

    note = data.get("note", "")
    if note:
        click.echo("")
        click.echo("Note: %s" % note)


@webshield.command("uninstall")
def webshield_uninstall() -> None:
    """Remove WebShield nginx configuration files."""
    config = load_config()
    data = _api_call(config, "POST", "/api/v1/webshield/uninstall")

    removed = data.get("files_removed", [])
    if removed:
        click.echo("Files removed:")
        for f in removed:
            click.echo("  %s" % f)
    else:
        click.echo("No WebShield files found to remove.")


@webshield.command("rules")
@click.option("--json", "as_json", is_flag=True, help="Output as JSON")
def webshield_rules(as_json: bool) -> None:
    """List bot detection rules."""
    config = load_config()
    data = _api_call(config, "GET", "/api/v1/webshield/rules")

    if as_json:
        click.echo(json.dumps(data, indent=2))
        return

    items = data if isinstance(data, list) else []
    if not items:
        click.echo("No bot rules configured.")
        return

    click.echo("%-20s  %-10s  %-12s  %-25s  %s" % ("Name", "Action", "Category", "Pattern", "Enabled"))
    for rule in items:
        click.echo("%-20s  %-10s  %-12s  %-25s  %s" % (
            rule.get("name", ""),
            rule.get("action", ""),
            rule.get("category", ""),
            rule.get("pattern", "")[:25],
            "yes" if rule.get("enabled") else "no",
        ))


# ── GeoIP (WebShield sub-commands) ──────────────────────────────────────────

@webshield.command("geo-status")
def webshield_geo_status() -> None:
    """Show GeoIP database status."""
    config = load_config()
    data = _api_call(config, "GET", "/api/v1/webshield/geo-status")
    if not data.get("enabled"):
        click.echo("GeoIP: disabled")
        return
    click.echo("GeoIP: enabled")
    click.echo("  Database: %s" % ("available" if data.get("available") else "not found"))
    click.echo("  Path: %s" % data.get("path", ""))
    if data.get("database_type"):
        click.echo("  Type: %s" % data.get("database_type"))


@webshield.command("geo-list")
@click.option("--json", "as_json", is_flag=True, help="JSON output")
def webshield_geo_list(as_json: bool) -> None:
    """List GeoIP country rules."""
    config = load_config()
    data = _api_call(config, "GET", "/api/v1/webshield/geo-rules")
    if as_json:
        click.echo(json.dumps(data, indent=2))
        return
    rules = data.get("rules", [])
    mode = data.get("mode", "none")
    click.echo("Mode: %s" % mode)
    if not rules:
        click.echo("No country rules configured.")
        return
    click.echo("%-6s  %-20s  %s" % ("Code", "Country", "Action"))
    for r in rules:
        click.echo("%-6s  %-20s  %s" % (r.get("country_code", ""), r.get("country_name", ""), r.get("action", "")))


@webshield.command("geo-block")
@click.argument("country_codes", nargs=-1, required=True)
@click.option("--action", default="block", type=click.Choice(["block", "challenge", "log"]), help="Action to take")
def webshield_geo_block(country_codes: tuple[str, ...], action: str) -> None:
    """Block one or more countries by ISO code (e.g., CN RU KP)."""
    config = load_config()
    codes = [cc.upper() for cc in country_codes]
    data = _api_call(config, "POST", "/api/v1/webshield/geo-rules", {"countries": codes, "action": action, "mode": "blocklist"})
    if data:
        click.echo("Blocked: %s (action=%s)" % (", ".join(codes), action))


@webshield.command("geo-unblock")
@click.argument("country_code")
def webshield_geo_unblock(country_code: str) -> None:
    """Remove a country from the block list."""
    config = load_config()
    data = _api_call(config, "DELETE", "/api/v1/webshield/geo-rules/%s" % country_code.upper())
    if data:
        click.echo("Removed: %s" % country_code.upper())


@webshield.command("geo-update-db")
def webshield_geo_update_db() -> None:
    """Download/update the MaxMind GeoIP database."""
    config = load_config()
    data = _api_call(config, "POST", "/api/v1/webshield/geo-update-db")
    if data:
        click.echo(data.get("message", "Database updated."))
    else:
        click.echo("Failed to update GeoIP database.", err=True)


# ── Attack Mode ─────────────────────────────────────────────────────────────

@cli.group("attack-mode")
def attack_mode_cli() -> None:
    """Manage attack mode (panic button)."""


@attack_mode_cli.command("status")
def attack_mode_status() -> None:
    """Show attack mode status."""
    config = load_config()
    data = _api_call(config, "GET", "/attack-mode")
    if data.get("enabled"):
        click.echo("Attack mode: ENABLED (since %s)" % data.get("enabled_at", "unknown"))
    else:
        click.echo("Attack mode: disabled")


@attack_mode_cli.command("enable")
def attack_mode_enable() -> None:
    """Enable attack mode — activates aggressive defenses."""
    config = load_config()
    data = _api_call(config, "POST", "/attack-mode/enable")
    if data.get("enabled"):
        click.echo("Attack mode ENABLED.")
        for action in data.get("actions", []):
            click.echo("  %s" % action)
    else:
        click.echo("Failed to enable attack mode.", err=True)


@attack_mode_cli.command("disable")
def attack_mode_disable() -> None:
    """Disable attack mode — restore previous settings."""
    config = load_config()
    data = _api_call(config, "POST", "/attack-mode/disable")
    click.echo("Attack mode disabled.")
    for action in data.get("actions", []):
        click.echo("  %s" % action)


# ── Incidents: resolve ──────────────────────────────────────────────────────

@incidents.command("resolve")
@click.argument("incident_id")
@click.option("--notes", default="", help="Resolution notes")
def incidents_resolve(incident_id: str, notes: str) -> None:
    """Mark an incident as resolved."""
    config = load_config()
    data = _api_call(config, "POST", "/incidents/%s/resolve" % incident_id, {"notes": notes})
    if data:
        click.echo("Incident %s resolved." % incident_id)
    else:
        click.echo("Failed to resolve incident.", err=True)


# ── SSH Management ──────────────────────────────────────────────────────────

# ── UFW Firewall ────────────────────────────────────────────────────────────

@cli.group()
def firewall() -> None:
    """UFW firewall management."""


@firewall.command("status")
@click.option("--json", "as_json", is_flag=True, help="JSON output")
def firewall_status(as_json: bool) -> None:
    """Show UFW firewall status and rules."""
    config = load_config()
    data = _api_call(config, "GET", "/firewall/ufw/status")
    if as_json:
        click.echo(json.dumps(data, indent=2))
        return
    click.echo("UFW: %s" % ("active" if data.get("active") else "inactive"))
    click.echo("Default: incoming=%s outgoing=%s" % (data.get("default_incoming", ""), data.get("default_outgoing", "")))
    for rule in data.get("rules", []):
        click.echo("  %s" % rule.get("raw", rule))


@firewall.command("enable")
def firewall_enable() -> None:
    """Enable UFW firewall."""
    config = load_config()
    _api_call(config, "POST", "/firewall/ufw/enable")
    click.echo("Firewall enabled.")


@firewall.command("disable")
def firewall_disable() -> None:
    """Disable UFW firewall."""
    config = load_config()
    _api_call(config, "POST", "/firewall/ufw/disable")
    click.echo("Firewall disabled.")


@firewall.command("reload")
def firewall_reload() -> None:
    """Reload UFW firewall rules."""
    config = load_config()
    _api_call(config, "POST", "/firewall/ufw/reload")
    click.echo("Firewall reloaded.")


@firewall.command("allow")
@click.argument("port")
@click.option("--proto", default="tcp", help="Protocol (tcp, udp, any)")
@click.option("--from", "from_ip", default=None, help="Source IP")
@click.option("--comment", default="", help="Rule comment")
def firewall_allow(port: str, proto: str, from_ip: str | None, comment: str) -> None:
    """Allow a port through the firewall."""
    config = load_config()
    body: dict = {"port": port, "protocol": proto, "action": "allow"}
    if from_ip:
        body["from_ip"] = from_ip
    if comment:
        body["comment"] = comment
    _api_call(config, "POST", "/firewall/ufw/rules", body)
    click.echo("Rule added: allow %s/%s" % (port, proto))


@firewall.command("deny")
@click.argument("port")
@click.option("--proto", default="tcp", help="Protocol (tcp, udp, any)")
@click.option("--from", "from_ip", default=None, help="Source IP")
def firewall_deny(port: str, proto: str, from_ip: str | None) -> None:
    """Deny a port through the firewall."""
    config = load_config()
    body: dict = {"port": port, "protocol": proto, "action": "deny"}
    if from_ip:
        body["from_ip"] = from_ip
    _api_call(config, "POST", "/firewall/ufw/rules", body)
    click.echo("Rule added: deny %s/%s" % (port, proto))


@firewall.command("delete-rule")
@click.argument("rule_number", type=int)
def firewall_delete_rule(rule_number: int) -> None:
    """Delete a firewall rule by number."""
    config = load_config()
    _api_call(config, "DELETE", "/firewall/ufw/rules/%d" % rule_number)
    click.echo("Rule %d deleted." % rule_number)


# ── CrowdSec: delete decision ──────────────────────────────────────────────

@crowdsec_cli.command("unban")
@click.argument("ip")
def crowdsec_unban(ip: str) -> None:
    """Remove a CrowdSec decision for an IP."""
    config = load_config()
    _api_call(config, "DELETE", "/crowdsec/decisions/%s" % ip)
    click.echo("Decision removed for %s." % ip)


# ── Daemon restart ──────────────────────────────────────────────────────────

@cli.command()
def restart() -> None:
    """Restart the jabali-security daemon."""
    import subprocess as sp
    sp.run(["/usr/bin/systemctl", "restart", "jabali-security"])  # noqa: S603
    click.echo("Daemon restarted.")


if __name__ == "__main__":
    cli()
