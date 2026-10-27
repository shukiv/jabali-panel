# Directory Privacy

Domains → Edit → **Security** tab → **Directory Privacy**. Per-subdirectory HTTP Basic Auth (the cPanel "Directory Privacy" equivalent).

## What it does

Adds an `auth_basic` realm on top of a subdirectory of a docroot. Browsers see the standard Basic-Auth popup; only callers presenting valid credentials reach the content.

Credentials are hashed with bcrypt and never read back. Multiple credential pairs may be added under one rule. A rule with **zero credentials** denies all access by design ,useful for taking a directory offline without removing files.

## Adding a rule

1. Open Domains → pick the domain → **Security** tab → **Directory Privacy** section.
2. Click **Add rule**.
3. **Path** ,relative to the site docroot.
   - `/` protects the whole site.
   - `/staging/` protects only the `staging` subdirectory.
   - Use the **folder picker** to browse the docroot tree instead of typing.
4. **Authentication name** ,the realm string shown in the browser popup ("Sign in to <name>").
5. **Credentials** ,at least one `username` + `password` pair. Add more pairs to allow more accounts under the same realm.
6. **Save**.

The reconciler applies the change within seconds.

## How it ships on the server

- htpasswd files live at `/etc/jabali-panel/dir-privacy/<rule-id>.htpasswd` ,readable only by the nginx user.
- For a path like `/staging/`, nginx renders a `location ^~ /staging/ { auth_basic "Staging"; auth_basic_user_file …; }` block.
- For path `/` (whole site), the directive lives at server scope. The ACME challenge location (`/.well-known/acme-challenge/`) is explicitly excluded with `auth_basic off;` so Let's Encrypt renewal continues to work.

## Removing protection

Two ways:

- **Delete the rule** ,protection is removed; nginx config is regenerated.
- **Remove all credentials** ,the rule remains but denies everything. Useful when re-adding credentials later.

## Notes

- Basic Auth sends credentials base64-encoded, not encrypted. Use HTTPS (the default).
- Passwords cannot be displayed after saving. Reset by editing the credential pair.
- Applies to the file-level nginx layer; downstream application auth is independent (you can stack Basic Auth in front of a WordPress login, for example).
