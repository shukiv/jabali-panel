# Reset Password (Admin)

The destination page reached from the email link sent by [Request Password Reset](./password-reset-request.md).

## Flow

1. The link contains a single-use, time-bounded recovery token issued by Kratos.
2. The page validates the token, then prompts for a new password (twice for confirmation).
3. On submit, Kratos updates the credential and returns the user to the login page.

## Password policy

- Minimum length: 12 characters.
- At least one character of each class: lowercase, uppercase, digit, symbol.
- The password must not be present in the `haveibeenpwned` k-anonymity lookup performed at submit time.

## Token lifetime

Recovery tokens expire after 60 minutes. After expiry the link returns the user to the request page with an explanatory notice; a new recovery email may be requested immediately.

## Failure modes

| Symptom | Cause | Resolution |
|---|---|---|
| "Recovery link expired" | More than 60 minutes elapsed since email was sent. | Request a new recovery email. |
| "Recovery link already used" | The link was clicked twice; the first click consumed the token. | Request a new recovery email. |
| "Password does not meet policy" | One or more policy rules failed. | Adjust and resubmit. |
| Page does not load | Kratos service unhealthy. | Check `systemctl status kratos`. |

## Related operator command

```bash
jabali user password <email|username|user-id>
```

Generates and prints a new password. Useful when the operator must reset a credential without relying on outbound email delivery.
