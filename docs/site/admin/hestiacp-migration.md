# HestiaCP Migration

The HestiaCP ingest path. Status: partial — files, databases, DNS, and a subset of mail are supported. Complex Exim ACL rules require manual re-implementation.

## Source archive

HestiaCP's per-user backup produced by:

```bash
v-backup-user <user>
```

The resulting archive lands under `/backup/<user>.<timestamp>.tar`.

## What gets migrated

| Asset | Behavior |
|---|---|
| User account | Recreated. |
| Home directory | Copied. |
| Web domains | Created as Domain rows. The vhost is rendered fresh from the panel template — Apache/nginx fragments from the source are not preserved. |
| DNS zones | BIND zones translated to PowerDNS rows. |
| MySQL / PostgreSQL databases | Restored with password hashes. |
| Email accounts | Created in Stalwart with generated passwords. |
| Forwarders / autoresponders | Translated to Stalwart. |
| Cron jobs | Translated to systemd-user timers via the allowlist filter. |

## What requires manual work

- **Exim ACL rules** — HestiaCP often carries non-trivial Exim acl_smtp_data / acl_check_recipient rules. These do not translate directly to Stalwart's expression filter syntax; rewrite under [Server Settings](./server-settings.md) → Mail → Stalwart expressions.
- **Per-domain webmail identities** — Roundcube identities on the HestiaCP source are not migrated; the destination serves its own **Bulwark** webmail instead.
- **Spamassassin / rspamd thresholds** — Stalwart spam scoring is independent; recalibrate if your Hestia setup had custom thresholds.

## Operator workflow

1. Produce a per-user backup on the Hestia host.
2. Upload to `/jabali-admin/migrations`.
3. Analyze → review the report for any Exim ACL warnings.
4. Restore.
5. For each Exim ACL warning, manually re-author the equivalent Stalwart expression filter.
6. Communicate generated mail passwords to mailbox owners.
7. Issue SSL via the per-domain SSL toggle.

## Limitations

- **Hestia Apache+nginx fronted setups** — Hestia frequently runs nginx in front of Apache. Jabali serves nginx directly with PHP-FPM. Apache-specific directives in `.htaccess` files that depend on `mod_rewrite` translate; directives that depend on `mod_php` or `mod_setenvif` do not and must be rewritten.
- **Hestia firewall (iptables) rules** — not migrated. Use [UFW](./ufw-baseline.md) plus [CrowdSec](./crowdsec-decisions.md) for the equivalent surface.

## Audit

Standard per-phase audit rows.
