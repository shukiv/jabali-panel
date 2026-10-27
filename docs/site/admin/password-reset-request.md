# Request Password Reset (Admin)

`/auth/recovery`. Kratos recovery flow.

## Flow

1. Enter email address.
2. Kratos emails a recovery link (uses the panel's own Stalwart for delivery).
3. Click the link → land on [Reset Password](./password-reset-confirm.md).
4. Set new password → log in.

## Where the email comes from

`webmaster@<panel-hostname>` by default. Override under [Server Settings](./server-settings.md) → Mail → Recovery sender.

## What if no mail?

If outbound mail is broken (Stalwart down, no DNS reverse, MX rejecting), use the CLI:

```bash
jabali user password <email>
```

…prints a new generated password once. Communicate it out-of-band.

## Rate limit

CrowdSec scenario `kratos-recovery-flood` triggers on >5 recovery requests / hour from one IP → 1-hour BAN.
