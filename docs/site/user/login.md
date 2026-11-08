# Login (User)

`/jabali-panel` redirects to the Kratos login flow at `/auth/login`.

## Flow

1. Enter the email address or username and password registered on your account.
2. If two-factor authentication is enrolled, you are taken to the [Two-Factor Challenge](./two-factor-challenge.md) page.
3. On success, you land on the [Dashboard](./dashboard.md).

## Forgot password

The login page has a **Forgot password?** link → [Request Password Reset](./password-reset-request.md).

## Locked out

If you cannot complete login and the recovery email is unavailable, contact the administrator of your hosting environment. Administrators have an operator command (`jabali user password`) that can generate a new password and a `2fa-reset` command that can strip TOTP.

## Browser support

Login pages are tested on the current major browsers (Chrome, Edge, Firefox, Safari). Cookies must be enabled. The session cookie is `SameSite=Lax`, `Secure`, `HttpOnly`.

## Why no social sign-in

The panel does not integrate with third-party identity providers (Google, GitHub, Microsoft, etc.) by default. The intent is to keep the panel installable on hosts that cannot reach external OIDC endpoints (air-gapped, restricted-egress). Administrators who want federated login can pair the panel's Kratos instance with an identity provider at the Kratos level, but that path is operator-configured, not panel-managed.

## Brute-force protection

Repeated failed logins from one IP cause CrowdSec to record a `kratos-bf` decision and ban the IP for four hours. Recovery requests are rate-limited similarly.

Sign-in attempts are also counted per account, from whatever address they come: the same budget one address has (10 at once, then one every four seconds). Past it, sign-in to that account answers "Too many attempts — try again in a minute" until the budget refills. This doesn't ban any address, so the account's owner isn't banned while someone else tries the account. Password recovery requests are counted per email address the same way, apart from sign-in. The second sign-in step (an authenticator code or a recovery code) is counted per address only.
