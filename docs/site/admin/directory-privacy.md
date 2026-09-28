# Directory Privacy (Admin)

Admin-side view of the per-subdirectory Basic Auth feature (M50). User-facing copy lives at [user/directory-privacy](../user/directory-privacy.md); this page covers operator concerns.

## Where it lives

Admin: `/jabali-admin/domains/edit/:id` → **Security** tab → **Directory Privacy**. The form is the same as the tenant view.

Tenant: `/jabali-panel/domains/edit/:id` → **Security** tab → **Directory Privacy**.

## What ships on disk

| File / location | Owner / mode | Purpose |
|---|---|---|
| `/etc/jabali-panel/dir-privacy/<rule-id>.htpasswd` | `root:www-data` / `0640` | bcrypt-hashed credentials. Generated; never edit by hand. |
| `/etc/nginx/conf.d/jabali/sites/<domain>.conf` | reconciler-owned | Contains the rendered `auth_basic` / `auth_basic_user_file` directives. |

For path `/`, the directive lives at server scope. Sub-path rules render as `location ^~ /<path>/ { auth_basic …; auth_basic_user_file …; }`.

## ACME interaction

LE renewal must work while a site-wide `/` rule is active. The vhost template includes a hard override:

```nginx
location /.well-known/acme-challenge/ {
  auth_basic off;
  ...
}
```

Renewal is verified after every reconcile of a domain that has both LE certs and a `/` directory-privacy rule.

## Zero-credentials state

A rule with no credential pairs renders an htpasswd file with a single impossible entry. The realm exists; no credentials can satisfy it. Used to take a directory offline without deleting it.

## API

- `GET /api/domains/:id/directory-privacy` ,list rules.
- `POST /api/domains/:id/directory-privacy` ,add a rule.
- `PATCH /api/domains/:id/directory-privacy/:ruleId` ,update.
- `DELETE /api/domains/:id/directory-privacy/:ruleId` ,remove.

Mutations require `domain.write` on the target domain.

## Audit

Every mutation appends an audit row: `directory_privacy.rule.created`, `…updated`, `…deleted`, plus per-credential `…credential.added`, `…credential.removed`. Passwords are never logged.

## Troubleshooting

- **"Renewal failed after enabling /"** ,vhost was generated before the ACME override block was added. The reconciler rewrites the vhost on its next tick (every 60 s); check again after a minute.
- **"Browser shows realm but rejects every password"** ,confirm the rule has at least one credential pair. Zero-credential rules deny by design.
- **htpasswd missing on disk** ,usually a reconciler error; check `journalctl -u jabali-panel` for the rule ID; the reconciler runs inside the panel process.
