# Codebase Audit Report — Jabali Security

**Date:** 2026-03-28
**Codebase:** jabali-security (Python 3.12+, asyncio daemon)
**Size:** ~15,000 lines across 67 Python files + PHP panel plugin
**Overall Score: 6.1 / 10**

---

## Executive Summary

Jabali Security is a well-architected asyncio security daemon with clean module separation, strong input validation at API boundaries, and consistent patterns. The codebase demonstrates good security awareness (no `shell=True`, parameterized SQL, Jinja2 auto-escaping). However, significant issues exist in **concurrency** (blocking I/O in async context), **security** (SQL interpolation, path traversal), and **observability** (no metrics, no tracing). The daemon's lifecycle management has gaps in single-instance locking and graceful shutdown. These issues should be addressed before production deployment on shared hosting.

## Category Scores

| # | Category | Worker | Score | Findings |
|---|----------|--------|-------|----------|
| 1 | **Security** | ln-621 | **4/10** | 1 CRITICAL, 6 HIGH, 6 MEDIUM |
| 2 | **Build Health** | ln-622 | **7/10** | 0 CRITICAL, 1 HIGH, 3 MEDIUM |
| 3 | **Code Principles** | ln-623 | **7/10** | 2 HIGH, 6 MEDIUM, 5 LOW |
| 4 | **Code Quality** | ln-624 | **7/10** | 7 HIGH, 16 MEDIUM, 12 LOW |
| 5 | **Dependencies** | ln-625 | **7/10** | 0 HIGH, 3 MEDIUM, 2 LOW |
| 6 | **Dead Code** | ln-626 | **7/10** | 2 HIGH, 15 MEDIUM, 4 LOW |
| 7 | **Observability** | ln-627 | **5/10** | 3 HIGH, 5 MEDIUM, 4 LOW |
| 8 | **Concurrency** | ln-628 | **5/10** | 8 HIGH, 10 MEDIUM, 8 LOW |
| 9 | **Lifecycle** | ln-629 | **6/10** | 4 HIGH, 8 MEDIUM, 6 LOW |

---

## Strengths

- **Clean architecture**: Clear separation between daemon, lib, API, web, and panel layers
- **Strong input validation**: UFW validators, SSH jail username sanitization, API auth middleware
- **No shell=True**: All subprocess calls use list args — critical for a security tool
- **Parameterized SQL**: IncidentStore uses `?` placeholders throughout (except one database scanner issue)
- **Modern Python**: Type hints, dataclasses, asyncio TaskGroup, Python 3.12+ features
- **Comprehensive test suite**: 359 tests passing in 0.34s
- **Protocol-based scanner interface**: Clean extensibility pattern for adding new scanning engines

---

## Critical & High Findings

### CRITICAL

| File | Line | Finding | Category |
|------|------|---------|----------|
| `lib/scanner/database.py` | 62 | SQL table/column names built via `%` string formatting with API-derived `table_prefix`. Regex validation exists but pattern is fragile. | Security |

### HIGH — Security

| File | Line | Finding |
|------|------|---------|
| `api/routes/scanning.py` | 17 | Path traversal: `/api/v1/scan` accepts arbitrary path with no restriction to allowed directories |
| `api/routes/cleanup.py` | (POST) | Path traversal: `/api/v1/cleanup/file` accepts arbitrary file path |

### HIGH — Concurrency

| File | Line | Finding |
|------|------|---------|
| `lib/log_tailer.py` | - | `readline()` and `open()` synchronous on event loop — blocks all coroutines during log tailing |
| `lib/cleanup/scheduler.py` | - | `Path.read_bytes()` synchronous across thousands of files during scans |
| `lib/cleanup/cms_cleaner.py` | - | Synchronous `read_bytes`/`write_bytes` in async cleanup methods |
| `lib/quarantine.py` | - | All file operations (shutil.move, os.chmod, os.walk) synchronous in async methods |
| `api/routes/scanning.py` | - | `p.read_bytes()` synchronous in API handler — stalls concurrent requests |
| `lib/behavior_tracker.py` | - | Shared mutable dict accessed from multiple scan workers without `asyncio.Lock` |
| `lib/hash_cache.py` | - | Shared mutable set accessed from multiple workers without lock |
| `lib/proactive/process_killer.py` | - | `time.sleep(0.5)` loop (up to 5s) blocks event loop |

### HIGH — Lifecycle

| File | Line | Finding |
|------|------|---------|
| `daemon/__main__.py` | 51-63 | No single-instance lock (fcntl.flock) — two daemons can start simultaneously |
| `daemon/server.py` | 179-183 | Shutdown via `raise KeyboardInterrupt` inside TaskGroup — can interrupt finally blocks |
| `daemon/server.py` | 80-85 | `except* KeyboardInterrupt: pass` swallows entire exception groups — other task errors lost |
| `lib/incidents.py` | (19 occurrences) | `assert self._db is not None` guards stripped under `python -O` |

### HIGH — Code Quality

| File | Line | Finding |
|------|------|---------|
| `daemon/__main__.py` | - | God module: 1,520 lines with 20+ click commands, PID management, logging, API helpers |
| `lib/incidents.py` | - | God class: 544 lines, 20 methods mixing incidents, quarantine, WAF events, blocked IPs, cleanup |
| `lib/config.py` | - | `load_config()`: 87 lines of manual field mapping for 56 config keys |

### HIGH — Observability

| File | Finding |
|------|---------|
| `api/routes/core.py` | Health endpoint always returns `{"status": "ok"}` with no component checks |
| (all modules) | Zero metrics collection (no prometheus, statsd, or counters) |
| (all modules) | Zero request tracing (no correlation IDs) |

### HIGH — Dead Code / Bugs

| File | Line | Finding |
|------|------|---------|
| `lib/bruteforce/firewall.py` | 208-209 | Double `@staticmethod` decorator — will cause runtime error |
| `api/routes/sshjail.py` | 192, 224 | Return type mismatch: `enable_shell()` returns `bool` but handler unpacks as tuple — will crash |

---

## Medium Findings Summary

| Category | Count | Top issues |
|----------|-------|------------|
| Security | 6 | SSRF via webhook URL, missing SESSION_COOKIE_SECURE, API auth bypass when API_KEY unset |
| Concurrency | 10 | Single shared aiosqlite connection, fire-and-forget tasks losing exceptions, TOCTOU races |
| Lifecycle | 8 | No sd_notify integration, no SIGHUP config reload, stale PID file on crash |
| Code Quality | 16 | Deep nesting, long functions, missing type hints (94 functions, 121 params) |
| Dead Code | 15 | 5 unused Pydantic models, 7 unused methods, orphaned WafCorrelator module |
| Observability | 5 | 15 silent exception swallows, missing loggers in scanner modules |
| Code Principles | 6 | DRY violations (_utcnow/_hex_id copied 5x), encapsulation breaks in API routes |
| Dependencies | 3 | pyyaml phantom dependency, no upper version bounds, dual web framework |
| Build | 3 | 46 blocking file I/O calls in async functions, 9 subprocess.run without check=True |

---

## Recommended Priority Actions

### P0 — Fix before production
1. Fix SQL interpolation in `lib/scanner/database.py` — use parameterized queries for table names or whitelist
2. Fix double `@staticmethod` bug in `lib/bruteforce/firewall.py:208`
3. Fix tuple unpacking crash in `api/routes/sshjail.py:192,224`
4. Add path validation to `/api/v1/scan` and `/api/v1/cleanup/file` endpoints
5. Replace `assert self._db` with proper runtime checks in `lib/incidents.py`

### P1 — Fix soon
6. Wrap blocking file I/O in `asyncio.to_thread()` across quarantine, cleanup, log_tailer, scanning
7. Add `asyncio.Lock` to shared mutable state (behavior_tracker, hash_cache, bruteforce detector)
8. Replace `time.sleep()` with `asyncio.sleep()` in process_killer
9. Fix shutdown: replace `raise KeyboardInterrupt` with `asyncio.Event`-based shutdown signal
10. Add single-instance file lock with `fcntl.flock()`
### P2 — Improve quality
12. Split `daemon/__main__.py` into a `cli/` package
13. Decompose `IncidentStore` into domain-specific repositories
14. Add meaningful health checks to `/api/v1/health`
15. Remove phantom `pyyaml` dependency
16. Remove orphaned `lib/waf/correlator.py` and unused Pydantic models

---

## Sources Consulted

- [aiohttp docs](https://docs.aiohttp.org) — graceful shutdown, cleanup_ctx, lifecycle patterns
- [Pydantic v2 docs](https://docs.pydantic.dev) — model validation, ConfigDict best practices
- Context7 MCP — aiohttp and Pydantic library documentation
