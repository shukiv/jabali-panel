# ADR-0115 — Mail bruteforce via CrowdSec parser + Stalwart tracer (vendored bu5hm4nn)

**Status:** Proposed
**Date:** 2026-06-03
**Supersedes:** [ADR-0114](./0114-stalwart-auth-fail-bruteforce.md)
**Relates-to:** ADR-0061 (CrowdSec extensions), PR #173 (panel-bf), PR #174 (webmail/api-token-bf)

## Context

The mail-bf gap from ADR-0114 still stands: Stalwart exposes AUTH
over the internet on 587/465/993/995/4190, and none of the stock
CrowdSec scenarios understand Stalwart's log format. The original
webhook-based design was rejected once two pieces of context surfaced:

1. **Stalwart ships a built-in auto-ban** (`security.authBanRate`
   = 100/day default, `security.scanBanRate` = 30/day, etc.). The
   detection logic — including the tricky "track distributed IPs
   against one login name" case — already exists and is
   upstream-supported. Enforcement is in-process only — Stalwart
   drops subsequent connections but the firewall never sees the
   attacker — so it covers detection but not L3 propagation.
2. **<https://github.com/bu5hm4nn/crowdsec-stalwart>** provides a
   ready CrowdSec integration: one parser for Stalwart's plaintext
   key-value log format plus five scenarios covering authentication
   bruteforce, user enumeration, SMTP scanning, rate limiting, and
   HTTP vulnerability probing. MIT licensed. Tested against Stalwart
   0.10+ against CrowdSec 1.7.6.

Maturity caveat on the upstream: 2 commits, 1 star, 0 forks, no
releases. It is a one-person experiment, not a community-blessed
artefact. We will treat it as a starting template, not a runtime
dependency.

Live investigation on puzzle (2026-06-03) also reconfirmed:

- Stalwart 0.16 default tracer = `tracer.store` (writes structured
  events into the RocksDb). Plaintext log files do not exist on disk
  unless explicitly configured via the admin API.
- `stalwart-cli` (already in scope via ADR-0110 / `internal/stalwartadmin`)
  is the supported way to set config keys without going through the
  interactive console.

## Decision

1. **Vendor bu5hm4nn/crowdsec-stalwart under `install/crowdsec/stalwart/`**
   in the jabali repo. License preserved, attribution kept in a
   `UPSTREAM.md` alongside. Rationale: the upstream is one commit
   away from disappearing, and we cannot ship a feature that depends
   on a pin to it. Vendoring means we own the regex maintenance.

2. **Live-validate the parser against Stalwart 0.16 on puzzle BEFORE
   wiring it into install.sh**. Capture 20+ real auth-failure log
   lines via a temporary file-tracer (see point 3), diff against the
   bu5hm4nn parser's regex anchors, patch any drift. The result is a
   jabali-owned parser that is known to work against the Stalwart
   version we actually ship.

3. **Stalwart side**: `install.sh install_stalwart_jabali_tracer()`
   calls `stalwart-cli config set` to:
   - `tracer.log.type = log`
   - `tracer.log.level = warn` (auth failures + scans are warn-level)
   - `tracer.log.path = /var/log/stalwart`
   - `tracer.log.prefix = stalwart`
   - `tracer.log.rotate = daily`
   - `tracer.log.enable = true`
   Idempotent (write-on-diff). Owned by jabali install.sh; never
   hand-edited.

4. **Disable Stalwart's built-in auto-ban** for the four categories
   CrowdSec now covers, to avoid double-bookkeeping (the operator
   should see one ban list, not two):
   - `security.authBanRate = "0/1d"`
   - `security.abuseBanRate = "0/1d"`
   - `security.loiterBanRate = "0/1d"`
   - `security.scanBanRate = "0/1d"`
   `loiterBanRate` is a judgement call — CrowdSec doesn't have a
   matching scenario today, so leaving Stalwart's loiter ban on as
   defence-in-depth is reasonable. Initial cut: disable all four,
   re-enable loiter if we see no CrowdSec-side equivalent shipped
   inside 30 days.

5. **CrowdSec side**: install.sh drops:
   - `parsers/s01-parse/jabali-stalwart-logs.yaml` (vendored parser)
   - `scenarios/jabali-stalwart-*.yaml` (vendored scenarios, prefixed
     `jabali/` to match the panel-bf naming convention)
   - `acquis.d/jabali-stalwart.yaml` pointing at
     `/var/log/stalwart/stalwart*.log`
   File-tail acquisition, not journal — Stalwart's stdout is empty
   in the default systemd unit (tracer goes to the file path we set
   above).

6. **Sensitivity preset writer extends to mail scenarios**. The five
   bf scenarios get capacity / blackhole overrides per preset (mirror
   the existing pattern in `security_crowdsec_sensitivity.go`).
   Tuning table:

   | Preset | auth-bf capacity | scan-bf capacity | blackhole |
   |--------|------------------|------------------|-----------|
   | relaxed | 15 / 60s | 30 / 60s | 30m |
   | balanced | 5 / 60s | 10 / 60s | 4h |
   | strict | 3 / 60s | 5 / 60s | 24h |

   The user-enumeration / rate-limit / HTTP-vuln scenarios use
   shape-similar overrides; full table in the implementation PR.

7. **CAPI contribution stays on**. Mail-bf decisions feed the
   community blocklist via the existing CrowdSec sharing setup. We
   don't bypass this path — net win for the ecosystem.

8. **Source-IP semantics**: parsed from Stalwart's log line
   `remote.ip` field directly. No reverse-proxy in front of Stalwart
   today; if mail-proxy ever lands (M52?) the parser will need a
   `forward-for` decision then.

9. **Out of scope**:
   - Per-mailbox lockout (Stalwart's `auth.authBanRate` keys on the
     login name as well — we'd be giving that up by setting it to
     `0/1d`. Acceptable: CrowdSec catches the source-IP side, which
     is what attackers can rotate. The "one account, many IPs"
     attack pattern survives this trade-off because each IP also
     accumulates fails fast enough to trip the auth-bf scenario at
     `capacity=5 / 60s`.)
   - Geo-fence of mail submission (separate concern, lives under
     AppSec geoblock if added later).
   - POP3 specifically (Stalwart unifies auth events across IMAP /
     POP3 / SMTP / JMAP / ManageSieve, so one scenario covers them).

## Consequences

### Positive

- **Zero new panel-api code**. No socket route, no in-memory bucket,
  no cscli wrapper to maintain. The CrowdSec pipeline takes the
  signal end-to-end.
- **Five scenarios for the price of one**. user-enum, SMTP scan,
  rate-limit, HTTP vuln scan come along for free with the bu5hm4nn
  fork — we'd have written separate ADRs and PRs for each under the
  webhook path.
- **L3 ban via firewall bouncer**. Same as the original webhook
  proposal — attacker drops at SYN, not at AUTH parse.
- **Unified HTTP + mail surface**. CrowdSec firewall bouncer bans an
  IP across every port jabali serves.
- **CAPI sharing**. Our mail-bf detections contribute to and benefit
  from the community blocklist.
- **Vendored**: upstream disappearance does not break us.

### Negative

- **Plaintext parser fragility**. Stalwart could change log format
  in a minor version and silently break detection. Mitigation: the
  bu5hm4nn project is small enough that we maintain the regex
  ourselves; we add a smoke test in CI that pipes a known Stalwart
  log line through the parser to catch drift before deploy. Owned
  by `install/crowdsec/stalwart/parser_test.sh`.
- **Lose Stalwart's per-login distributed-IP tracking**. CrowdSec's
  grouping is per-IP only. If an attacker truly rotates IPs (e.g.
  residential proxy network) while targeting one account, neither
  layer catches them on a low total per-IP volume. Acceptable for
  jabali's threat model; documented in the runbook.
- **Two operator surfaces existed briefly** during the transition
  (Stalwart built-in still on for a deploy window before
  install.sh's `0/1d` setting reaches the host). Mitigation: the
  install.sh ordering applies the disable BEFORE the CrowdSec
  acquis is added, so the only window is "neither side bans" not
  "both sides ban".

### Neutral

- bu5hm4nn maintenance is now jabali's responsibility. We carry the
  vendored copy in `install/crowdsec/stalwart/UPSTREAM.md` with a
  note on when we forked + what we changed.

## Implementation notes

Files this ADR commits us to creating in the follow-up PR:

- `install/crowdsec/stalwart/parsers/s01-parse/jabali-stalwart-logs.yaml`
  (vendored from upstream; jabali-prefix the parser name)
- `install/crowdsec/stalwart/scenarios/jabali-stalwart-auth-bf.yaml`
- `install/crowdsec/stalwart/scenarios/jabali-stalwart-user-enum.yaml`
- `install/crowdsec/stalwart/scenarios/jabali-stalwart-smtp-scan.yaml`
- `install/crowdsec/stalwart/scenarios/jabali-stalwart-rate-limit.yaml`
- `install/crowdsec/stalwart/scenarios/jabali-stalwart-http-vuln.yaml`
- `install/crowdsec/stalwart/acquis.d/jabali-stalwart.yaml`
- `install/crowdsec/stalwart/UPSTREAM.md` — attribution + diff notes
- `install/crowdsec/stalwart/parser_test.sh` — smoke test against
  captured Stalwart 0.16 log fixtures
- `install.sh`:
    - `install_stalwart_jabali_tracer()` — `stalwart-cli` writes
      tracer config + disables auto-ban categories
    - `install_crowdsec_jabali_stalwart_scenarios()` — drops the
      vendored files into `/etc/crowdsec/{parsers,scenarios,acquis.d}/`
- `panel-agent/internal/commands/security_crowdsec_sensitivity.go`
  — extend preset writer to override the five new scenario capacities

## Validation gate before merge

1. On puzzle (Stalwart 0.16 in production): enable tracer,
   collect 50+ auth-fail lines + 20+ scan-attempt lines via
   `tail -F /var/log/stalwart/stalwart.log`.
2. Run the vendored parser against them, confirm field extraction
   matches the scenarios' `filter:` expressions.
3. Patch the parser regex if fields shifted between 0.10 and 0.16.
4. Live curl-loop test: `for i in {1..10}; do openssl s_client
   -connect localhost:993 ... bad creds; done` should produce a
   ban in cscli decisions within ~6s.
5. Smoke-test the disable: confirm Stalwart's `auto-ban` panel in
   its admin UI shows "0/1d" for all four categories post-install.

## Amendment 2026-11-03 — field order and the tracer on older boxes

What shipped differs from the plan above in two places, and both broke
the feed without any error:

- **The tracer.** The feed is a Stdout tracer at debug level (journal,
  `acquis.d/jabali-stalwart.yaml`), not a Log tracer at warn: Stalwart
  0.16's Log tracer wrote nothing, and auth failures are debug events.
  The apply plan creates the tracer, but only on a fresh install, and
  the entry was added on 2026-06-03 (#178). Boxes installed before that
  never had it, so CrowdSec saw no Stalwart line on them.
  `converge_stalwart_stdout_tracer` in install.sh now creates it, or puts
  back `enable`, `level` (debug or trace) and `ansi = false`, on every
  install and update.
- **Field order.** Stalwart does not keep the order of the fields after
  `(event)`, and 1.0 moves `remoteIp` to the end of an auth failure. The
  client address, which every scenario bans, is read only from a fixed
  position Stalwart writes before any client text: right after
  `listenerId, localPort` at the start of the list, or, for
  `auth.failed`, at the end of the line before `id`. The header is
  pinned so the event name is always Stalwart's. Quoted values can hold
  any client text, so they never decide where a key is; a line without
  the address at a fixed position is dropped. The other fields are read
  one by one, and `accountName`, `details` and `path` are now captured
  (the user-enumeration and HTTP-scan scenarios read them). Checked with
  `cscli explain` on 0.16.27 and 1.0.2 lines, on 2000 real log lines,
  and with a live failed IMAP login on a 0.16.27 box; regression test
  `install/tests/test_crowdsec_stalwart_parser.sh`.
- **Open for the 1.0 switch:** the end-of-line position is safe only
  while Stalwart writes a newline inside a value as `\n` (seen on
  0.16.27). The 1.0 drill checks this before the switch. `auth.error`
  and `auth.too-many-attempts` count only when 1.0 writes the address at
  the start; their 1.0 shape is checked in the same drill.
