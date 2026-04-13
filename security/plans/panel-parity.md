# Plan: Panel Plugin Feature Parity

> Source PRD: `docs/PARITY.md` — feature matrix and gap analysis

## Architectural decisions

Durable decisions that apply across all phases:

- **Single page**: All features live on the `Security` page (`panel/Pages/Security.php`) as Filament Tabs — no new pages
- **Tab routing**: Each tab uses `#[Url(as: 'tab')]` with `?tab=<name>` URL params
- **Data source**: All data fetched via `JabaliSecurityClient` HTTP calls to `127.0.0.1:9876/api/v1/*` — no Eloquent models
- **Tables**: Filament `Table` with `->records(fn () => ...)` closures returning arrays from the API
- **Actions**: `Filament\Actions\Action` for both header and table row actions
- **Toggles**: Module enable/disable calls `PATCH /api/v1/config` + daemon restart via `systemctl`
- **View**: `panel/views/security.blade.php` — Filament components only, minimal Blade
- **Config writes**: Module toggles write to `/etc/jabali-security/jabali-security.conf` via the API's `PATCH /config`

---

## Phase 1: Module Toggles + Overview Enhancement

**User stories**: Admin can enable/disable any protection module from the panel. Admin sees all dashboard stats.

### What to build

Add a "Protection Modules" section to the Overview tab showing all 16 toggleable features with enable/disable switches. Each toggle calls `PATCH /api/v1/config` to flip the config key (`yes`/`no`), then triggers a daemon restart. Group toggles into "Core Modules" (heuristic, entropy, yara, process monitor, behavior tracking, auto quarantine) and "Advanced Protection" (WAF, brute-force, proactive, process killer, webshield, threat intel, cleanup, UFW, scheduled scans, auto suspend) — matching the web dashboard layout.

Also add the missing "Queue Pending" stat to the stats widget.

### Acceptance criteria

- [ ] Overview tab shows all 17 module toggles with current on/off state
- [ ] Clicking a toggle updates the config and restarts the daemon
- [ ] Toggle state reflects the actual config value after page refresh
- [ ] Queue Pending stat appears in the stats widget
- [ ] Matches the web dashboard's Protection Modules / Advanced Protection grouping

---

## Phase 2: WAF Tab

**User stories**: Admin can view WAF events, stats, and manage CRS rules from the panel.

### What to build

Add a "WAF" tab with:
- Stats cards: Events (24h), Blocked (24h) — from `GET /waf/stats`
- Events table: client_ip, method, uri, rule_id, action, timestamp — from `GET /waf/events`
- Rule files table: file name, size — from `GET /waf/rules`
- Row actions on rules: disable/enable rule — via `POST /waf/rules/{id}/disable` and `/enable`
- Header action: "Update CRS" button — via `POST /waf/crs/update`
- Disabled rules list shown below rule files

### Acceptance criteria

- [ ] WAF tab shows event stats cards (events 24h, blocked 24h)
- [ ] Events table lists WAF events with filtering by limit
- [ ] Rule files table shows CRS .conf files with sizes
- [ ] Admin can disable/enable individual WAF rules
- [ ] "Update CRS" button triggers rule update with success/failure notification
- [ ] Disabled rules are listed

---

## Phase 3: Brute-Force Tab

**User stories**: Admin can view brute-force stats, see blocked IPs, and manage the whitelist.

### What to build

Add a "Brute-Force" tab with:
- Stats cards: Tracked IPs, Blocked Count — from `GET /bruteforce/stats`
- Blocked IPs table: ip, count — from `GET /bruteforce/blocked`
- Header action: "Whitelist IP" form — via `POST /bruteforce/whitelist`
- Row action on blocked IPs: "Whitelist" — via `POST /bruteforce/whitelist`
- Whitelist removal not shown (use blocklist tab for unblocking)

### Acceptance criteria

- [ ] Brute-Force tab shows tracked IPs and blocked count
- [ ] Blocked IPs table lists currently blocked IPs
- [ ] Admin can whitelist an IP (also unblocks it)
- [ ] Shows "not enabled" message when brute-force protection is disabled

---

## Phase 4: Proactive + WebShield Tabs

**User stories**: Admin can view process kills, WebShield status, and manage bot rules.

### What to build

**Proactive tab:**
- Status cards: process_kill_enabled, kill_count — from `GET /proactive/status`
- Process kills table: pid, username, score, reason, success — from `GET /proactive/kills`

**WebShield tab:**
- Status cards: installed, rate_limiting, bot_filtering, challenge_enabled — from `GET /webshield/status`
- Bot rules table: name, pattern, action, category — from `GET /webshield/rules`
- Header actions: "Install WebShield" / "Uninstall WebShield" — via `POST /webshield/install` and `/uninstall`

### Acceptance criteria

- [ ] Process kills table shows recent kills
- [ ] WebShield tab shows install status and bot rules
- [ ] Admin can install/uninstall WebShield
- [ ] Both tabs show appropriate "not enabled" messages when features are off

---

## Phase 5: Threat Intel + Users + Cleanup Tabs

**User stories**: Admin can manage threat intelligence feeds, check IP/hash reputation, view per-user risk profiles, and see cleanup records.

### What to build

**Threat Intel tab:**
- Feeds table: name, type, entry_count, last_update — from `GET /threat-intel/feeds`
- Header action: "Update Feeds" — via `POST /threat-intel/update`
- Header action: "Check IP" form — via `GET /threat-intel/check/ip/{ip}`
- Header action: "Check Hash" form — via `GET /threat-intel/check/hash/{hash}`

**Users tab:**
- Users table: username, incident_count, max_score — from `GET /users`
- Row action: "View Details" modal showing user's incidents and quarantine — from `GET /users/{username}`

**Cleanup tab:**
- Records table: path, strategy, success, backup_path, username, created_at — from `GET /cleanup/records`
- Header action: "Clean File" form with path input — via `POST /cleanup/file`

### Acceptance criteria

- [ ] Threat Intel tab shows feeds with entry counts and last update
- [ ] Admin can trigger feed update and check IP/hash reputation
- [ ] Users tab lists users with incident counts and risk scores
- [ ] Admin can view per-user incident/quarantine details
- [ ] Cleanup tab shows cleanup history
- [ ] Admin can trigger manual file cleanup

---

## Phase 6: Rules + Scan + Polish

**User stories**: Admin can view YARA/ClamAV rules, run scans with detailed results, view incident details, and reset stats.

### What to build

**Rules tab:**
- YARA rules table: name, size — from `GET /rules`
- Scanner status: list of active scanners (heuristic, entropy, yara, clamav)
- ClamAV status indicator

**Scan tab** (replace header action with full tab):
- Path input form with scan button
- Results display: path, findings list (scanner, rule, score, description), total score, severity, action taken
- Support both file and directory scan results

**Incident detail modal:**
- Row action on incidents table opens modal with full incident data: all findings, file path, event type, timestamps

**Maintenance:**
- "Reset All Stats" action in Overview tab footer — clears incidents, quarantine, WAF events, blocked IPs, cleanup records
- UFW reload action in Firewall tab

### Acceptance criteria

- [ ] Rules tab shows YARA files and active scanners
- [ ] Scan tab provides full scan interface with detailed results
- [ ] Incident detail modal shows all findings for an incident
- [ ] "Reset All Stats" action works with confirmation dialog
- [ ] UFW reload button works in Firewall tab
- [ ] All features match web dashboard behavior
- [ ] `docs/PARITY.md` updated — all gaps marked as Parity
