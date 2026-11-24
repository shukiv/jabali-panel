# ADR-0153: Auto-allowlist successful panel + SSH login IPs in CrowdSec

**Status:** Accepted (2026-07-01).
**Driven by:** GH #598.
**Blueprint:** `plans/m598-login-allowlist.md`.

---

## Context

A legitimate operator who authenticates to the panel or over SSH can still be
banned by CrowdSec afterwards — a false positive, or activity from the same IP
(WordPress/admin endpoints, probing scenarios) trips a scenario and the source
IP is bounced, locking the operator out of the host they just logged into.

The panel-login half already shipped (`middleware.WhitelistLoginIP`): on the
first authenticated request per session it time-boxes the client IP into the
single jabali CrowdSec allowlist. #598 asks to extend this to SSH logins and to
make it configurable/disableable.

## Decision

One server setting drives both halves; the source IP is only ever captured by a
trusted component.

1. **Setting** (`server_settings`, migration `000205`):
   `crowdsec_login_allowlist_enabled BOOL DEFAULT TRUE` +
   `crowdsec_login_allowlist_ttl_hours INT DEFAULT 168`. Enabled by default
   matches the pre-#598 shipped panel behaviour. TTL default **168h (7 days)** —
   the issue title says "7 days, configurable"; the AI-triage body said 14. We
   take 7d + admin-configurable (1..8760h); because it is configurable the
   default is not load-bearing.

2. **Panel half**: `WhitelistLoginIP` reads the setting *after* the existing
   Redis dedup (`SetNX`, 24h) gate, so the uncached `server_settings.Get` runs
   at most once per 24h per session, never per request. When disabled it returns
   without deleting the dedup key (deleting it would reintroduce a per-request
   DB read); re-enabling therefore reaches an already-active session within the
   dedup window (≤24h), new sessions immediately.

3. **SSH half — trusted capture, no auth-path change**: a root-side agent
   background worker (`StartLoginAllowlistWatcher`) tails sshd's own success log
   (`journalctl -u ssh/sshd -f`, `tail -F /var/log/auth.log` fallback), parses
   `Accepted … for <user> from <ip> port`, and time-boxes the IP via the shared
   `addToJabaliAllowlist`. **Rejected alternatives:** the tenant login shell
   (`jabali-ssh-shell`) is unprivileged, sandboxed, and tenant-controlled — it
   could forge IPs and cannot reach the agent socket; a PAM `pam_exec` hook sits
   in the auth path (lockout risk). The log watcher is read-only and mirrors how
   CrowdSec itself acquires — worst-case failure is "no allowlist entry", never
   a lockout. `--since now` avoids re-scanning history on restart; an in-memory
   24h dedup (pruned on access) bounds cscli churn; the `journalctl` child is
   supervised (restarted on exit — journald restart / log rotation).

4. **Toggle propagation**: the agent has no DB access, so the panel PATCH pushes
   `{enabled, ttl_hours}` to `/etc/jabali-panel/login-allowlist.conf` via
   `security.crowdsec.login_allowlist.apply` (atomic write); the watcher reads
   the file per hit so a toggle takes effect without an agent restart.
   `install.sh` seeds a default (enabled, 168h) conf so a fresh host works before
   the first PATCH. Both halves skip loopback/private/link-local IPs.

## Consequences / limitations

- **Security trade-off (accepted, surfaced in the UI):** this is a *broad*
  CrowdSec exemption (all bouncers, per the AC). A successful login from a stolen
  key or compromised account shields that source IP from all CrowdSec decisions
  for the TTL — up to a week of ban-immunity to scan/brute other accounts from
  that IP. Failed logins are out of scope, so pure brute-force is not exempted,
  but a single success is. Admins who find the window unacceptable lower the TTL
  or disable the feature; the Security UI card states this explicitly.
- **Conf convergence is PATCH-driven, not boot-reconciled.** The push is
  best-effort in a goroutine; if the agent is down during a *disable* PATCH the
  DB flips but the conf stays enabled until the next successful PATCH re-pushes.
  Self-heals on the next settings save; we accept this rather than add a
  boot-time reconcile. `install.sh` only seeds the conf when absent, so it never
  clobbers a pushed override on re-install/update.

## Amendment (2026-11-24): ban-only, not allowlisted

A CrowdSec allowlist entry also makes AppSec skip the request entirely, so a
login IP was exempt from the WAF as well as from bans. Login IPs now only skip
bans:

- The agent keeps them in `/var/lib/jabali/login-ban-exempt.json` and renders
  them into a postoverflow whitelist,
  `/etc/crowdsec/postoverflows/s01-whitelist/zz-jabali-login-ips.yaml`. An
  alert a scenario raises for one of them is dropped before it becomes a ban;
  the WAF still inspects every request. Bans that do not come from this
  server's scenarios (the community blocklist, a ban added by hand) still
  apply. The extra blocklists leave these IPs out, as they did when
  allowlisted.
- Verbs `security.crowdsec.login_ips.{add,list,remove}`; the panel's admin
  login calls `add`, the SSH watcher adds directly. `allowlists.add` with an
  `auto-whitelist:` reason (an older panel) goes to the same list.
- `crowdsec -t` runs before every reload and a file it rejects is put back,
  so the reload's restart fallback never meets a config CrowdSec cannot start
  with. CrowdSec reloads only when the IP set changes, not on a refresh.
- 100.64.0.0/10 is never exempted.
- Switching the setting off empties the list; hourly upkeep drops expired IPs
  and keeps the list empty while the pushed conf says off.
- At agent start every `auto-whitelist:` entry is removed from the jabali
  allowlist. The panel's Redis dedup key was renamed so an active admin
  session is exempted again on its next request.
- Admin → Security → CrowdSec lists the exempt IPs and removes one
  (`GET`/`DELETE /admin/security/crowdsec/login-ips`).

The trade-off above narrows: a stolen key still buys its IP up to the window
without bans, but no longer a WAF bypass.
