# JAB-230 — PHP mail() path: jabali-sendmail shim + per-domain relay identities

## Problem

install.sh purges all traditional MTAs (postfix/exim4/sendmail-bin/nullmailer) because
Stalwart owns :25, but nothing provides `/usr/sbin/sendmail` and the FPM pools leave
`sendmail_path` unset. PHP `mail()` → `sh: /usr/sbin/sendmail: not found` → `wp_mail()`
returns false. Every WP form, password reset, and WooCommerce transactional email fails
fleet-wide until an operator hand-configures SMTP per site.

Constraints that shape the design:

- Stalwart rejects `MAIL FROM` ≠ authenticated identity (`501 5.5.4`), so the default
  `wordpress@<domain>` envelope needs mapping to a real, authed identity.
- Loopback is NOT trusted on a multi-tenant box — any tenant can reach 127.0.0.1:587.
  No global relay credential, no unauthenticated localhost listener, no loosening of
  Stalwart's sender check (security-over-functionality rule).
- Per-user FPM runs on the host under AppArmor profile `jabali-fpm-app`
  (install/apparmor/usr.local.libexec.jabali.fpm-exec) — the shim needs an exec
  transition + child profile, verified by `make aa-smoke`.
- Stalwart auths against the panel DB (`mailboxes`, bcrypt, ADR-0042/0045); `SendOnly`
  mailboxes (GH #371) authenticate for submission but never receive/store.

## Decisions (user-approved 2026-08-07)

1. **Custom Go shim** (`jabali-sendmail`), not msmtp-mta: identity derived from
   `getuid()` (unforgeable) + From-header domain used only to SELECT among cred files
   that uid can already read (POSIX enforces isolation, not the header). Same
   `sendmail_path` for every pool — no per-user templating.
2. **Per-domain relay identity**: `noreply@<domain>` SendOnly mailbox, auto-provisioned.
   Clean lifecycle (dies with the domain), DMARC-alignable, no Stalwart config changes.
3. **EmailEnabled=false domains get one too** — SendOnly needs no MX/DNS. The internal
   provisioning path bypasses the EmailEnabled guard; the UI/CLI guard stays.
4. **Reconciler-driven provisioning** (idempotent per-tick): covers new domains,
   migration restores, AND existing-fleet backfill via the daily auto-update, with no
   separate one-shot backfill tool.
5. **No WP mu-plugin in v1** — wp_mail() defaults to the mail() transport, so the shim
   alone satisfies acceptance 1+2. Revisit only if E2E shows header-From rejection.

## Components

### 1. Shim — `panel-agent/cmd/jabali-sendmail/`

Sendmail-compatible CLI, runs as the tenant uid, exec'd by PHP `mail()`:

- Flags: `-t` (recipients from To/Cc/Bcc), `-i`/`-oi`, `-f <addr>` (accepted, but the
  envelope sender is ALWAYS overridden by the cred identity), tolerant of `-O*`/`-o*`
  noise; positional recipients. Message on stdin, hard cap 25 MiB.
- Header handling: parse From to get the domain hint; strip Bcc from the transmitted
  message; if header From ≠ cred identity, add `Sender:` = cred identity (header From is
  left intact — DMARC-relevant rewriting only if E2E proves Stalwart requires it).
- Cred lookup: `/etc/jabali-panel/sendmail/<user>/<from-domain>.cred`; missing/unreadable
  → `<user>/default.cred` (symlink maintained by provisioner). No cred at all → exit 78
  (EX_CONFIG) with one clear syslog line.
- Cred file format (key=value): `email=`, `password=`, `host=<mail FQDN for TLS SNI>`.
- Submission: dial 127.0.0.1:587, STARTTLS with `ServerName=<host>` (verified — never
  InsecureSkipVerify), AUTH PLAIN, `MAIL FROM=<cred email>`. Timeout ~30s.
- Exit codes: 0 ok, 75 tempfail (connect/5xx-transient), 77 perm denied auth,
  78 config missing, 64 usage. Syslog tag `jabali-sendmail`; never log the password or
  message body.
- Treats argv + stdin as hostile (it IS the tenant's exec vector into a root-provisioned
  path): no shell, no file writes, bounded parsing.

### 2. Provisioning

- **Agent command `sendmail.cred.ensure`** (new, panel-agent/internal/commands/):
  params `{username, domain, email, password, host}`. Writes
  `/etc/jabali-panel/sendmail/<user>/<domain>.cred` atomically, dir 0750 root:<usergroup>,
  file 0640 root:<usergroup>; maintains `default.cred` symlink (first/oldest domain).
  Idempotent: rewrites only when content differs. Companion `sendmail.cred.remove`
  for domain deletion. Contract tests in agentwire (panel↔agent JSON, both sides).
- **Panel-api reconciler ensure-loop** (per-tick, idempotent): for every active domain —
  1. Ensure `noreply@<domain>` mailbox row exists: SendOnly=true, min quota, generated
     password (`ids.NewSecret()`), bcrypt hash, `PasswordEnc` sealed with sso.key.
     Internal path skips the EmailEnabled guard (SendOnly ⇒ no delivery/storage).
  2. Ensure cred file matches: unseal PasswordEnc → `sendmail.cred.ensure`. Rows with
     NULL PasswordEnc (pre-000056) get a one-time password rotate to make the plaintext
     recoverable.
  3. `mailbox.create` agent notify for the Stalwart JMAP registry (existing flow).
- **Domain delete**: cred removal + symlink fixup wired into the SHARED cascade path —
  both delete flows (feedback_delete_cascade_vhost_parity).

### 3. Wiring

- `install/php/jabali-php-pool.conf.tmpl`: fixed line
  `php_admin_value[sendmail_path] = /usr/local/libexec/jabali/jabali-sendmail -t -i`
  (+ assertion in the pool-template repo test).
- Pool re-render: verify what re-applies pools after a template update lands via
  `jabali update`; if nothing does, add a template-hash trigger to the reconciler so
  fleet pools converge without operator action.
- CLI PHP (cron jobs use mail() too): add `-d sendmail_path=...` to the per-user PHP
  CLI wrapper.
- AppArmor: `jabali-fpm-app` gets `px` transition to new child profile
  `jabali-sendmail` (read own cred subtree, `network inet stream`, TLS CA certs,
  nothing else). Verify with `make aa-smoke`.
- install.sh: install the shim binary + `/etc/jabali-panel/sendmail/` dir, AppArmor
  load; postconditions validated (install.sh is truth). Update path: shim ships with
  the agent deploy artifacts — check rebuild-hash inputs so `jabali update` actually
  deploys it (feedback_update_rebuild_hash_inputs, feedback_jabali_update_trimmed_path).

### 4. Testing

- Unit: shim flag/header parsing, Bcc strip, cred selection + fallback, exit codes;
  SMTP against an in-test fake server. Provisioning idempotency (second tick = no
  writes). Pool template assertion. Contract tests for the new agent command.
- E2E on testserver (NOT newzaps): fresh WP site → `wp_mail()` + raw `mail()` →
  assert Stalwart accepted AND delivered (log/maildir content, not just `true` —
  feedback_verify_via_user_facing_path); `wordpress@<domain>` From does not 501;
  mail-off domain sends; CLI/cron `mail()` works.
- Inline security review pass (no agents): cred perms, no secret logging, TLS verify,
  no relay widening, hostile-input handling in the shim.

## Phases

1. Shim binary + unit tests.
2. Agent command (`sendmail.cred.ensure`/`.remove`) + contract tests + delete cascade.
3. Panel: relay-mailbox ensure in reconciler + PasswordEnc rotate path.
4. Wiring: pool template + CLI + AppArmor (aa-smoke) + install.sh/update path +
   pool re-render trigger.
5. E2E on testserver, fleet-converge verification, JAB-230 comment (comment only —
   never close).

Branch: `jab230-php-mail-path` off latest `origin/main`.

## Behavioural notes (documented, accepted)

- **Human `noreply@` is never touched.** Reuse/rotate only applies to
  `SendOnly=true` rows. A migrated human `noreply@<domain>` pushes the relay to
  `jabali-noreply@<domain>`; both taken → domain skipped (shim `default.cred`
  fallback still gives it outbound) with one warn per panel boot.
- **Fingerprint cache staleness:** rotating/deleting a relay mailbox through the
  panel UI leaves the cred file stale (535s on send) until panel-api restarts —
  bounded by the daily fleet auto-update. Accepted for v1.
- Tenants can no longer create their own mailbox named `noreply@` on a
  panel-provisioned domain (name taken by the relay).
- SSH-shell nspawn sessions don't see the shim/creds (host paths not in the
  image) — CLI mail() inside the jailed shell stays dead, as before. Cron runs
  on the host and IS covered.

## Open items to verify during implementation

- Does Stalwart 0.16.x also validate the HEADER From on submission, or only MAIL FROM?
  (E2E decides whether the shim must rewrite From vs just add Sender.)
- Exact reconciler trigger for `php.pool.apply` re-render after template change.
- Whether snuffleupagus rules touch mail() (feedback_sp_fpm_destructor_defer scar —
  unrelated area, but SP is loaded in tenant PHP).
