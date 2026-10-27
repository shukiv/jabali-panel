# Request Password Reset (User)

The `/auth/recovery` page reached from the login page's **Forgot password?** link.

## Flow

1. Enter the email address registered on your panel account.
2. The panel sends a recovery email containing a single-use link valid for 60 minutes.
3. Click the link to reach [Reset Password](./password-reset-confirm.md).
4. Set a new password and log in.

## Where the email comes from

The recovery email is sent from `webmaster@<panel-hostname>` by default, using the panel's own Stalwart mail server. The exact From address may be customised by your administrator.

## What if the email does not arrive

- Check the spam folder. Recovery emails sometimes land in spam on the first email to a fresh receiving address.
- Verify the email address you entered matches the one registered on your account. The page does not confirm whether the address is registered (to avoid leaking valid addresses to attackers); it always reports "if an account exists, an email has been sent".
- If the spam folder is empty after several minutes, contact your administrator. They can reset your password directly via a CLI command.

## Rate limit

Repeated recovery requests from the same IP within a short window cause CrowdSec to record a `kratos-recovery-flood` decision and ban the IP for one hour. Wait, then try once from a different IP if needed.
