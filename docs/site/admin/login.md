# Admin Login

`/jabali-admin` redirects to the Kratos login flow at `/auth/login`.

## Flow

1. Username + password. The **username** is the login identifier (M54); the email is not used to sign in.
2. If TOTP 2FA is enrolled: redirect to [Two-Factor Challenge](./two-factor-challenge.md).
3. Session cookie set; redirect to `/jabali-admin/dashboard`.

Sessions are managed by **Kratos** (M20). The panel itself does not store passwords or session tokens.

> **Log in via the panel hostname, not the server IP.** Kratos is same-origin — its login cookies, CSRF tokens, and allowed return URLs are bound to the panel hostname (FQDN), so opening the panel by raw IP fails the flow with "could not reach identity service". The login page shows a hint when you reach it by IP.

## First admin

Created by the installer. The panel URL, the admin username and a generated password are printed at the end of `bash install.sh`. If you missed them:

```bash
jabali user password <username>           # sets and prints a new password
jabali user password <username> --link    # or prints a recovery URL, valid 24 h
```

Sign in, change the password if you used the printed one, and optionally enrol 2FA.

## Locked out

If the only admin lost their password or 2FA:

```bash
jabali user password <admin-email>          # generate a new password
jabali user 2fa-reset <admin-email>         # strip TOTP + recovery codes
```

Both bypass HTTP auth (direct DB + Kratos) so they work even when the panel UI is unreachable.

## Bruteforce protection

CrowdSec watches `kratos.public` and `nginx.access` for failed-auth patterns. Repeated failures from one IP earn a 4-hour BAN decision. See [CrowdSec Decisions](./crowdsec-decisions.md).

## No OIDC

The panel does **not** act as an OIDC provider (M16 rolled back; see [removed-features](../removed-features.md)). Login is local-account-only via Kratos.
