# Platform — Agent

`jabali-agent.service`. Root-privileged process; the only thing that performs privileged host operations. Callers (the panel API, the CLI, the reconciler) reach it over the Unix socket `/run/jabali/agent.sock`.

## Why a separate process

- **One privilege boundary** — the panel runs as the unprivileged `jabali` user; only the agent has root.
- **One auditable surface** — every privileged op goes through one of N handlers under `panel-agent/internal/commands/`. The audit log records the agent's call, not the panel's intent.
- **Restart-safe** — restarting the panel doesn't kill in-flight host ops; restarting the agent doesn't lose panel state.

## Wire protocol

Newline-delimited JSON over the Unix socket, one request per connection. The envelope types live in `agentwire/wire.go`. Each request:

```json
{ "id": "01J...", "command": "domain.create", "params": { … }, "deadline": "2026-09-28T12:00:00Z" }
```

`deadline` is optional. Each response echoes the request `id`:

```json
{ "id": "01J...", "ok": true,  "data": { … } }
{ "id": "01J...", "ok": false, "error": { "code": "invalid_argument", "message": "human-readable" } }
```

## Handler catalogue (representative; see `panel-agent/internal/commands/`)

**Domains / nginx**
- `domain.create`, `domain.update`, `domain.delete`
- `nginx.reload`, `nginx.cache.purge`, `nginx.ratelimits.apply`

**SSL**
- `ssl.issue`, `ssl.issue_dns01`, `ssl.renew`, `ssl.revoke`, `ssl.self_sign`, `ssl.cert_info`
- `ssl.install_custom`, `ssl.install_shared`, `ssl.delete_shared`
- `ssl.mail.issue`, `ssl.mail.delete` (per-domain `mail.<domain>` certificates)
- `ssl.panel.issue`, `ssl.panel.selfsign` (the panel hostname and panel mail certificates)
- `ssl.panel.mail_served` (checks that IMAPS and SMTPS serve the new mail certificate before a [mail hostname](../admin/mail-hostname.md) change is applied), `ssl.panel.lineage_delete` (removes the certbot lineage of a replaced custom mail hostname)

**Webmail**
- `webmail.vhost_apply`, `webmail.vhost_remove`, `webmail.branding.apply`
- `webmail.jmap_url.apply`, `nginx.webmail_redirect.apply` (move webmail to the applied mail hostname)

**DNS**
- `dns.zone.upsert`, `dns.zone.delete`
- `dns.dnssec.enable`, `dns.dnssec.disable`

**Mail (Stalwart)**
- `mail.mailbox.create`, `mail.mailbox.passwd`, `mail.mailbox.set_quota`, `mail.mailbox.delete`
- `mail.auth_cache.flush` (clears Stalwart's HTTP login cache). Stalwart answers webmail and JMAP logins from this cache, not the mailbox table, so without a flush an old password or a disabled mailbox kept working there. The panel calls it when a mailbox is disabled. The password, mailbox delete, domain purge and domain rename verbs flush it themselves. It runs at most once every 5 seconds; a change inside that gap gets one flush at its end.
- `mail.forwarder.upsert`, `mail.forwarder.delete`
- `mail.autoresponder.upsert`, `mail.autoresponder.delete`
- `mail.catchall.set`
- `mail.disclaimer.set`
- `mail.shared_folder.*`
- `mail.directory.apply` (a mail domain's read-only [directory](../user/email.md#the-domain-directory) address book, ADR-0171)
- `mailbox.trusted_senders.apply` (a mailbox's [trusted senders](../user/mailboxes.md#trusted-senders), as contact cards in its Stalwart account, GH #2017)
- `mail.mtasts.*`

**DB**
- `db.create`, `db.delete`
- `db.user.create`, `db.user.delete`, `db.user.passwd`
- `db.root.password.rotate`
- `db.config.apply`
- `db.maintenance.run` (OPTIMIZE / ANALYZE / CHECK / REPAIR)
- `db.processlist`, `db.kill`
- `db.pma.admin.ensure`

**System / OS**
- `user.create`, `user.delete`
- `user.limits.apply`, `user.limits.clear`
- `user.egress.apply`, `user.egress.clear`
- `sshkey.write`
- `quota.set`

**PHP**
- `php.pool.write`, `php.pool.delete`
- `php.ext.enable`, `php.ext.disable`
- `php.reload`

**Backups**
- `backup.destination.test`
- `backup.run` (kind=`account_full` / `system_backup`)
- `backup.restore`

**Apps**
- `app.install`, `app.delete`, `app.clone`, `app.update`
- `app.sso.write` (writes the self-deleting magic SSO file)

**Security**
- `crowdsec.allowlist.*`
- `appsec.policy.apply`
- `aide.scan`
- `malware.scan.run`
- `quarantine.move`

**Misc**
- `nginx.cache.purge`, `nspawn.*`, `systemd.restart`, `systemd.start`, `systemd.stop`

## What the agent never does

- Receive HTTP. The agent does not have an HTTP listener. The panel API is the HTTP entry point.
- Talk to Kratos directly. Identity lives in the panel.
- Make outbound calls to third parties without the panel telling it to. (Even certbot is invoked with `--standalone-only=false` against the existing nginx vhost; no rogue outbound.)

## Hardening

- **Two checks on every connection to the main socket:**
  - Socket permissions. `/run/jabali/` is `root:jabali 0750` and the socket is `root:jabali 0660`, so only root and the `jabali` group can connect.
  - Caller UID (JAB-366, JAB-357). The agent reads the connecting process's UID (`SO_PEERCRED`) and accepts only the UIDs in `-allowed-uids`. install.sh passes the panel user and root. The agent refuses to start when the list is empty or unparsable; only the test flag `-insecure-allow-any-uid` turns the check off. So a service account left in the `jabali` group by mistake still cannot drive the agent.
- Only the UIDs in `-admin-uids` may request the root-scoped File Manager. By default this is the `-allowed-uids` list.
- No other service is a member of the `jabali` group, and a restore never adds one back (JAB-357).
- The agent has no AppArmor profile. It was removed in M40.3 because an AppArmor 4.x complain-mode bug blocked the agent's MariaDB socket connection. See [AppArmor](../admin/apparmor.md).
- Each handled request is logged at debug level with `id`, `command` and `ok`.

## Adding a new handler

1. New file `panel-agent/internal/commands/foo_bar.go` with the command name and param struct.
2. Wire-contract golden test (mirror `security_crowdsec_geoblock_golden_test.go`).
3. Call site on the panel side (an API handler or a reconciler).
4. ADR if the decision is load-bearing.
