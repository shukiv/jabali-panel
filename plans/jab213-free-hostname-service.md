# JAB-213 — Jabali free hostname service (cprapid.com equivalent)

Status: blueprint. Design research done 2026-08-02 (JAB-213 + memory
`project_free_hostname_service`). Code is deliberately NOT started: the PSL
submission is the long pole and its clock only starts once the base domain is
registered — do phases 0–1 first.

## Architecture (decided)

Copy the cprapid model, never sslip.io:

- **Base domain on the Public Suffix List (PRIVATE section).** Load-bearing
  twice: (1) LE rate limits — without it the whole fleet shares one
  50-certs/week bucket; with it every `<label>.<base>` is its own registered
  domain; (2) cookie isolation — without it any customer can set a `.<base>`
  cookie that every other customer's panel sends back.
- **Service** (operator's server): PowerDNS authoritative for `<base>` (already
  in the jabali stack) + small API + DB:
  `label -> install_id, IPv4/IPv6, token_hash, last_seen`.
- **ACME DNS-01 broker**: a box asks the service to set `_acme-challenge` TXT
  under its OWN label only, then issues `<label>.<base>` +
  `*.<label>.<base>` in ONE cert — covers panel, mail names, and every
  preview URL. `models.EffectivePreviewBase` (#824/#826/#848) works unchanged.
- **Panel side**: install.sh up-front prompt (free hostname vs own domain),
  registration client, token at 0600 (the `/etc/jabali` DIR stays 0755 —
  SSH-lockout scar), heartbeat for label reclamation.

## Security invariants (review-gated before the service takes traffic)

1. A token writes `_acme-challenge` ONLY under its own label — else any
   customer mints certs for another customer's hostname. Dedicated tests.
2. **Labels are IP-derived, SERVER-SIDE, from the observed public source IP
   of the registering box** (decision 2026-08-07, cPanel cprapid parity:
   `192-0-2-7.jabalihosted.com`). Never client-chosen — a completed TCP
   handshake is the proof of IP control; kills squatting and the
   arbitrary-label API surface. A records never point at RFC1918 space
   (DNS-rebinding refusal by construction). NAT collision → short suffix
   (`-b`). IP move → label re-derives on next check-in (cPanel behaviour).
3. Per-label revocation + rate limits; abuse blocklists the shared base.
4. Base = SEPARATE registration, never a subdomain of jabali-panel.com
   (PSL-listing under it would change the main site's cookie behaviour).

## Phases

0. **Operator: pick + register the base domain** — DONE 2026-08-07:
   **jabalihosted.com**, expires 2030-08-07 (4y — clears the PSL >2y rule).
   NS currently Namecheap defaults (registrar-servers.com).
1. **PSL submission** — `_psl.<base>` TXT pointing at the PR URL, PR against
   publicsuffix/list (PRIVATE section) with rationale + example subdomains
   (cprapid precedent). "NO SLAs ON TIME" — open it first, build during review.
2. **Service MVP** on the operator's server: pdns + API (register label,
   heartbeat, set/clear `_acme-challenge`), token-hash auth, per-label rate
   limit. Reuse claim-code #735 shape (jabali-hosted endpoint the panel calls).

   **Email-verified registration (decision 2026-08-08):** a label is only
   issued to a verified email address.
   - Flow: installer prompts for an email → `POST /register {email}` → the
     service mails a 6-digit code (sent from jabalihosted.com infra — the box
     being installed has no mail yet, so no chicken-and-egg) → operator types
     the code into the installer → `POST /claim {email, code}` from the box →
     service derives the label from the observed source IP and binds
     `label ↔ verified email ↔ token`.
   - Why: accountability anchor for the abuse desk (every label has a
     reachable owner — pairs with the /abuse page commitments), revocation
     contact, expiry/reclamation notices, mass-registration friction, and an
     honest "distinct users" count for the PSL.
   - Limits: per-email label cap (e.g. 10, raisable), per-IP + per-email
     request rate limits, codes expire in 15 min, resend throttled.
   - The email is stored for operational contact only — state that in the
     install prompt (one line, no marketing).
   - Installer UX: keep the "own domain" path zero-prompt; the free-hostname
     path adds exactly two fields (email, code).
3. **Panel integration (v1 — BUILT 2026-08-08)**: install.sh gated free-host
   flow + client helper + heartbeat timer. **v1 cut (advisor):** claim writes
   `<label>` + `*.<label>` A (both DNS-only → box IP), so `mail.<label>`,
   `autoconfig.<label>`, previews resolve to the box and the panel's EXISTING
   HTTP-01 cert machinery issues for them — **no DNS-01 hook, no wildcard cert
   in v1.** `install/hostname/jabali-hostname.sh` (register→code→claim, token
   at `/etc/jabali-panel/hostname.env` 0600, never logged), gated on
   `JABALI_FREE_HOSTNAME=1` + TTY. Falls back to the manual prompt on any
   failure (bad_source/rate_limited/etc handled distinctly).
   - Deferred to **3b** (verify-first): wildcard *certificate* for previews on
     one cert via DNS-01, using the already-built `/v1/acme/{present,cleanup}`
     endpoints. Read `acme_shared_certs.go` + panel-cert issuance BEFORE
     choosing certbot `--manual` hook vs reconciler-internal. `EffectivePreview
     Base` default when a free hostname is active also lands in 3b.
   - Known gap: server-side reclamation (`reclaimAfterMove` constant) has no
     reaper yet — heartbeat reports `ip_moved`, nothing reclaims. Not a v1 blocker.
4. **E2E (phase 4)**: GENUINELY FRESH VM running real `curl | bash` with
   `JABALI_FREE_HOSTNAME=1` — testserver/.165 can't simulate the first-install
   TTY flow. Assert: hostname resolves, panel + `mail.<label>` HTTP-01 certs
   issue, cross-label `_acme-challenge` write REFUSED (invariant test).
5. **Rollout — GATED ON PSL MERGE, not just E2E.** Until PR #3127 merges (+ the
   weeks browsers/LE take to pick up the list), every `<label>` cert counts
   against ONE registered-domain LE bucket (~50/wk = ~25 installs/wk ceiling)
   and cookie isolation doesn't exist. So: default-on installer prompt waits
   for the merge. Then JAB-213 comment; docs.

## Phase 2 concrete design (locked 2026-08-08)

- **Host**: `hostsclick` (182.54.236.100, Debian 13). Port 53 free; existing
  nginx terminates 443 → API vhost `api.jabalihosted.com` proxies to the
  service on loopback. ⚠ disk was 95% full at survey — clean before go-live.
- **Code**: jabali2 repo, `hostedsvc/` (service internals) +
  `cmd` under it for the `jabalihosted-svc` binary. Same-repo contract
  tests with the panel-side client (phase 3).
- **Storage**: SQLite (modernc.org/sqlite, pure Go) at
  `/var/lib/jabalihosted/svc.db`. Single-box service; MariaDB is overkill.
  Tables: `labels(label PK, ipv4, email, token_hash, created_at, last_seen,
  revoked_at)`, `email_codes(email, code_hash, expires_at, attempts)`,
  `email_labels(email, count)` (cap enforcement), `audit(...)`.
- **DNS**: Debian-native PowerDNS authoritative + sqlite3 backend, zone
  `jabalihosted.com`; service writes records through the pdns HTTP API on
  loopback (never raw DB), then purges cache — the jabali
  pdns-cache-after-backend-write lesson baked in from day one.
  MVP nameservers: ns1 + ns2.jabalihosted.com both → 182.54.236.100 (glue via
  Namecheap API `domains.ns.create` + `setCustom`); true second box later.
- **API** (JSON over HTTPS, all POST):
  `/v1/register {email}` → mail 6-digit code (15 min TTL, resend-throttled).
  `/v1/claim {email, code}` → label derived from OBSERVED source IP
  (dash-encoded, `-b` suffix on NAT collision), bind label↔email↔token,
  create A record, return `{label, fqdn, token}`.
  `/v1/heartbeat {token}` → last_seen; if source IP changed, return
  `relabel: {new_label...}` (old label kept 7 days then reclaimed).
  `/v1/acme/present {token, txt}` / `/v1/acme/cleanup {token}` → TXT at
  `_acme-challenge.<own label>` ONLY (token→label lookup server-side; the
  label is never a request parameter — invariant 1 by construction).
  `/v1/release {token}`.
- **Auth**: 32-byte random token at claim; stored SHA-256. Constant-time
  compare. Per-IP + per-email rate limits (token bucket in SQLite).
- **Code email**: SMTP submission via configured relay (riva —
  mail.reeva.me:587) with creds in `/etc/jabalihosted/svc.env` (0600).
- **Ops**: systemd unit (hardened: DynamicUser=no, dedicated `jabalihosted`
  user, ProtectSystem=strict, ReadWritePaths=/var/lib/jabalihosted),
  deploy script in repo (`hostedsvc/deploy/`), `jabalihosted-svc admin
  revoke <label>` CLI for the abuse desk.

### Traps (advisor review 2026-08-08 — all three are build/deploy gates)

1. **NS cutover deletes `_psl`.** The `_psl.jabalihosted.com` TXT lives in
   Namecheap DNS today; switching NS to our pdns makes it vanish — breaking a
   live PSL-PR attestation. The pdns zone bootstrap MUST carry
   `_psl TXT "https://github.com/publicsuffix/list/pull/3127"`, and the deploy
   script's post-cutover gate is `dig TXT _psl.jabalihosted.com @8.8.8.8`.
2. **Proxy vs source-IP invariant.** Service sits behind nginx on loopback:
   accept `X-Real-IP` ONLY when RemoteAddr is 127.0.0.1; never honor
   client-supplied X-Forwarded-For (forged header ⇒ chosen labels ⇒ the
   squatting/rebinding surface returns). Claim endpoint must never sit behind
   a CDN. Dedicated forged-header test.
3. **Code-email sender = the JAB-230 lesson.** Riva's Stalwart 501s
   MAIL FROM ≠ authed identity. Sender identity must be decided up front
   (simplest: an authenticated reeva.me mailbox — SPF/DKIM already good, and
   codes MUST land in Gmail inboxes at install time). Publish SPF (`-all` or
   sender policy) + DMARC reject for jabalihosted.com in the zone — a
   PSL-listed base is a spoofing magnet.

Go-live ordering (hard): disk cleanup on hostsclick → pdns + full zone
(incl. `_psl`, SPF/DMARC, ns/api records) → Namecheap glue → NS switch →
public `_psl` verify → LE cert for api vhost → service live. Nightly SQLite
dump off-box from day one (lost DB = every token unrecoverable = every fleet
cert dies at next renewal).

## DNS/edge architecture (decided 2026-08-08 — Cloudflare)

User chose CF for DNS HA + API DDoS/hide-origin + free TLS. The DNS layer is
behind the `DNSBackend` interface, so this is a config swap, not a rewrite.
Two backends exist: `cloudflare` (default, `cfdns.go`) and `pdns`
(self-hosted alternative, `pdns.go`).

- **DNS**: zone `jabalihosted.com` hosted at **Cloudflare** (anycast — kills
  the single-box DNS failure domain). Registrar NS → CF's assigned pair.
  Service writes A + `_acme-challenge` TXT via the CF API
  (`CF_API_TOKEN` scoped Zone:DNS:Edit on this zone only, `CF_ZONE_ID`).
  Every label record is **proxied=false (DNS-only / grey)** — a label's A
  must resolve to the CUSTOMER's real box; proxying breaks their panel/mail
  ports and hides the IP the label encodes. Enforced + tested in `cfdns.go`.
- **API edge**: `api.jabalihosted.com` **proxied (orange)** → hostsclick.
  CF edge cert (free public TLS) + **CF Origin CA** cert on nginx (free,
  15-year) with zone SSL = Full (strict). No certbot on the box.
- **Source-IP invariant preserved at the edge** (supersedes trap 2's "never
  behind a CDN"): origin nginx's `real_ip` module unwraps `CF-Connecting-IP`
  from CF ranges → `$remote_addr` becomes the true client IP → passed as
  `X-Real-IP` to the loopback service, which is UNCHANGED (still loopback-
  trust). CF overwrites any client-supplied `CF-Connecting-IP`, so with the
  origin firewall locked to CF ranges it is unforgeable. Config +
  requirements in `deploy/nginx-api.conf`. Trust root moves from raw TCP
  handshake to "CF edge + CF-only firewall + fresh CF ranges" — accepted.
- **`_psl` + SPF `-all` + DMARC reject** live as records in the CF zone
  (trap 1 + 3 unchanged; just authored at CF instead of pdns).

**CF go-live ordering**: create CF zone + import all records (`_psl`, SPF,
DMARC, api A proxied) → registrar NS → CF assigned pair → verify
`dig TXT _psl.jabalihosted.com @8.8.8.8` = PR URL → issue CF Origin CA cert,
drop on hostsclick → lock host firewall :443 to CF ranges → nginx vhost +
service (`DNS_BACKEND=cloudflare`) → E2E a claim. Disk cleanup on hostsclick
still gated (SQLite + logs). Nightly off-box SQLite dump unchanged.
CF-ranges refresh cron on the origin (stale ranges silently break real-IP).

## Open decisions (operator)

| # | Decision | Recommendation (from JAB-213) |
|---|---|---|
| 1 | Base domain name | operator picks + registers, 3+ years |
| 2 | Scope | panel + previews + mail TLS only (cPanel parity, smallest abuse surface) — NOT customer nameservers |
| 3 | HA | two nameservers from day one (a DNS outage takes every free-hostname panel + previews down at once) |
| 4 | Labels | DECIDED 2026-08-07: IP-derived server-side (see invariant 2) — cPanel/DA parity |

## Related

GH #836 / PR #848 (configurable preview_base; sslip.io was the stopgap this
replaces), #824/#826 preview infra, #735 claim-code endpoint prior art.
