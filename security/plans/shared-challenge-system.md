# Plan: Shared JS Challenge System

## Objective

Build a self-hosted proof-of-work challenge page shared by GeoIP, WebShield, and Under Attack mode. Visitors prove they have a real browser by solving a SHA-256 PoW puzzle. On success, an HMAC-signed cookie bypasses future challenges for a configurable TTL.

## Current State

- `etc/webshield/challenge.html` exists but is trivial — a 1M loop with `btoa(timestamp)` cookie (no crypto, easily spoofable)
- WebShield `config_generator.py` already generates `error_page 503` + challenge location block
- GeoIP `geoip.py` generates `return 503` for challenge action but has NO error_page or challenge file
- `geo.conf` is included in server{} blocks, `geoip.conf` in http{} block
- nginx on the server has no WebShield includes (WebShield disabled), but does include `geo.conf`
- Cookie name `jabali_verified` used but not validated server-side (nginx doesn't check it)

## Architecture

```
Trigger (any of):                    Challenge flow:
  GeoIP challenge ─┐
  WebShield bot   ─┼──→ nginx checks cookie ──→ valid? ──→ pass through
  Under Attack    ─┘         │ no
                             ▼
                      serve challenge.html (503)
                             │
                      browser solves SHA-256 PoW
                             │
                      sets HMAC-signed cookie
                             │
                      auto-redirect → pass through
```

### Cookie Design

- Name: `jabali_passed`
- Value: `{timestamp}:{nonce}:{hmac_sha256(timestamp:nonce, secret)}`
- TTL: configurable (default 24h), set via `max-age`
- Secret: derived from `API_KEY` in config (already exists, never exposed to client)
- nginx validates via a lua/njs snippet OR the daemon validates via a small auth_request subrequest

### Decision: njs vs auth_request vs map-only

| Approach | Pros | Cons |
|----------|------|------|
| **njs (nginx JS)** | Fast, inline, no subrequest | Requires `libnginx-mod-http-js` package |
| **auth_request** | No extra modules, daemon validates | Subrequest per uncached request, latency |
| **map + cookie check** | Zero deps, pure nginx | Can't verify HMAC, only checks cookie exists |

**Recommendation**: Use **njs** for HMAC validation. It's already in most nginx repos, tiny module, and validates inline with zero latency. Fallback: if njs not available, use cookie-exists check (weaker but functional).

## Steps

---

### Step 1: Build the PoW challenge page

**Files**: `etc/webshield/challenge.html`

**Context**: Replace the trivial loop with a real SHA-256 proof-of-work challenge. The page must:
- Be fully self-contained (zero external deps, inline CSS/JS)
- Look professional (Jabali branding, dark/light theme support)
- Run a SHA-256 PoW puzzle: find a nonce where `SHA-256(challenge + nonce)` starts with N zero bits
- Challenge string = current timestamp + random salt (embedded in page by nginx)
- Difficulty: tunable, default ~0.5s on modern hardware
- On success: set `jabali_passed` cookie with `timestamp:nonce:hmac` format
- Auto-redirect to original URL after solving
- Show progress (% done, spinner)
- Graceful failure message if JS disabled

**Tasks**:
- [ ] Write SHA-256 PoW solver using Web Crypto API (`crypto.subtle.digest`)
- [ ] Generate challenge string from timestamp + difficulty
- [ ] Cookie value: `base64(timestamp:nonce:difficulty:hash)`
- [ ] Clean, minimal UI (~20kb total)
- [ ] Test in Chrome, Firefox, Safari

**Verification**:
```bash
# File exists and is valid HTML
python3 -c "from html.parser import HTMLParser; HTMLParser().feed(open('etc/webshield/challenge.html').read()); print('OK')"
# Size under 25kb
test $(wc -c < etc/webshield/challenge.html) -lt 25000
```

**Exit criteria**: Challenge page solves PoW and sets cookie in <2s on modern browser.

---

### Step 2: Cookie validation in nginx (njs module)

**Files**: `etc/webshield/jabali_challenge.js` (new), config generators updated

**Context**: nginx needs to check the `jabali_passed` cookie before triggering the challenge. This runs on every request to challenged resources — must be fast.

**Tasks**:
- [ ] Write njs script `jabali_challenge.js` with `validate(r)` function
- [ ] Validate cookie format: `base64(timestamp:nonce:difficulty:hash)`
- [ ] Check timestamp is not expired (configurable TTL, default 24h)
- [ ] Verify the SHA-256 hash: `SHA-256(timestamp + nonce)` must have leading zeros matching difficulty
- [ ] Return `1` (valid) or `0` (invalid/missing/expired)
- [ ] Add `js_import` and `js_set` to http-level config
- [ ] Add fallback for servers without njs: just check cookie exists

**nginx config pattern**:
```nginx
# http-level
js_import jabali from /etc/nginx/jabali-security/jabali_challenge.js;
js_set $jabali_challenge_valid jabali.validate;

# In the map or server block
if ($jabali_challenge_valid = "1") {
    # skip challenge, let through
}
```

**Verification**:
```bash
# njs syntax check
njs -c etc/webshield/jabali_challenge.js 2>&1 | grep -v error
nginx -t
```

**Exit criteria**: Cookie set by step 1 is validated by njs; expired cookies are rejected.

---

### Step 3: Update GeoIP config generator

**Files**: `lib/webshield/geoip.py`

**Context**: `geo.conf` currently does `return 503` for challenge but has no `error_page` directive and no cookie bypass. Need to add both.

**Tasks**:
- [ ] Add cookie bypass check to server-level geo.conf
- [ ] Add `error_page 503 /jabali-challenge.html` directive
- [ ] Add `location = /jabali-challenge.html` block pointing to shared challenge file
- [ ] Copy `challenge.html` to a location accessible by nginx (e.g. `/etc/nginx/jabali/challenge/`)
- [ ] Ensure challenge file is deployed during install/update

**Generated geo.conf should look like**:
```nginx
# Cookie bypass — if already verified, skip GeoIP challenge
if ($jabali_challenge_valid = "1") {
    set $jabali_geo_action 'pass';
}

if ($jabali_geo_action = 'block') {
    return 403;
}

if ($jabali_geo_action = 'challenge') {
    return 503;
}

error_page 503 /jabali-challenge.html;
location = /jabali-challenge.html {
    root /etc/nginx/jabali/challenge;
    internal;
}
```

**Verification**:
```bash
# Block a country with challenge action, verify 503 serves the challenge page
curl -s -o /dev/null -w "%{http_code}" https://jabali.site/
# Should be 503 with challenge HTML body
```

**Exit criteria**: GeoIP challenge serves the PoW page and accepts the cookie to let through.

---

### Step 4: Update WebShield config generator

**Files**: `lib/webshield/config_generator.py`, `lib/webshield/manager.py`

**Context**: WebShield already generates challenge blocks but uses the old trivial page and doesn't validate cookies. Update to use the shared challenge system.

**Tasks**:
- [ ] Update `generate_server_config()` to include cookie bypass before bot challenge
- [ ] Point `error_page 503` to the shared challenge location
- [ ] Remove `_default_challenge_page()` from manager.py (replaced by shared file)
- [ ] Update `install()` to copy challenge.html to shared location instead of config_dir
- [ ] Clean up legacy geoip params from manager constructor (already using **kwargs)

**Verification**:
```bash
nginx -t
# Bot challenge should serve PoW page, not raw 503
```

**Exit criteria**: WebShield challenge uses the same PoW page and cookie as GeoIP.

---

### Step 5: Add config keys and CLI commands

**Files**: `lib/config.py`, `daemon/__main__.py`

**Context**: Need configurable challenge TTL and difficulty.

**New config keys**:
- `CHALLENGE_TTL` — cookie lifetime in seconds (default: `86400` = 24h)
- `CHALLENGE_DIFFICULTY` — PoW difficulty bits (default: `18` = ~0.5s solve time)

**CLI commands**:
- `jabali-security challenge status` — show challenge config and cookie TTL
- `jabali-security challenge test` — verify challenge page is deployed and nginx is configured

**Tasks**:
- [ ] Add config defaults
- [ ] Wire into config dataclass
- [ ] Add CLI commands
- [ ] Pass difficulty/TTL into challenge page generation (template substitution)

**Verification**:
```bash
jabali-security challenge status
jabali-security challenge test
```

**Exit criteria**: Challenge TTL and difficulty are configurable; CLI reports status.

---

### Step 6: Panel integration

**Files**: `panel/Pages/Security.php`

**Context**: Show challenge status in the panel and allow configuring TTL/difficulty.

**Tasks**:
- [ ] Add challenge status card to GeoIP tab (e.g. "Challenge: Active, TTL: 24h")
- [ ] Add CHALLENGE_TTL and CHALLENGE_DIFFICULTY to Settings tab if needed
- [ ] Or add inline controls in the GeoIP tab's MaxMind Configuration section

**Verification**: Visual check — GeoIP tab shows challenge status.

**Exit criteria**: User can see and configure challenge settings from the panel.

---

### Step 7: Install/update deployment

**Files**: `install.sh`, `daemon/__main__.py`

**Context**: The challenge page and njs script need to be deployed to the right locations during install and update.

**Tasks**:
- [ ] Install: copy `challenge.html` to `/etc/nginx/jabali/challenge/`
- [ ] Install: copy `jabali_challenge.js` to `/etc/nginx/jabali-security/`
- [ ] Install: ensure `libnginx-mod-http-js` is installed (apt)
- [ ] Update: copy both files on update
- [ ] Uninstall: clean up challenge files

**Verification**:
```bash
jabali-security update
ls /etc/nginx/jabali/challenge/challenge.html
ls /etc/nginx/jabali-security/jabali_challenge.js
dpkg -l libnginx-mod-http-js
```

**Exit criteria**: Fresh install and update both deploy challenge files.

---

## Step Dependencies

```
Step 1 (PoW page) ──────────────┐
Step 2 (njs validation) ────────┤
                                ├──→ Step 3 (GeoIP) ──┐
                                ├──→ Step 4 (WebShield)├──→ Step 5 (config) ──→ Step 6 (panel) ──→ Step 7 (deploy)
                                └─────────────────────┘
```

- Steps 1 and 2 can run in **parallel**
- Steps 3 and 4 can run in **parallel** (after 1+2)
- Steps 5, 6, 7 are **serial**

## Risks

| Risk | Mitigation |
|------|------------|
| njs module not available on all servers | Fallback to cookie-exists check (map-only) |
| PoW too slow on mobile/old devices | Low default difficulty (18 bits), configurable |
| Challenge page cached by CDN/proxy | `Cache-Control: no-store` header |
| Cookie replay from different IP | Optional: encode client IP in cookie (breaks NAT/VPN users) |
| SHA-256 Web Crypto API not available | Fallback to pure JS SHA-256 implementation |

## Invariants (verified after every step)

- `nginx -t` passes
- Existing WebShield bot blocking still works
- Existing GeoIP blocking (403) still works
- No new Python dependencies
- Challenge page is <25kb
