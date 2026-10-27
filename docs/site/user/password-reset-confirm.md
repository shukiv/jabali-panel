# Reset Password (User)

The destination page reached from the recovery email link.

## Flow

1. The link in your recovery email contains a single-use token. Open the link in the same browser if possible.
2. Choose a new password and confirm it.
3. Submit to set the new password and return to the login page.

## Password policy

- Minimum length: 12 characters.
- At least one of each character class: lowercase, uppercase, digit, symbol.
- The password is checked against the `haveibeenpwned` k-anonymity database; widely-leaked passwords are rejected.

## Token expiry

The recovery token is valid for 60 minutes from the moment the email was sent. Expired tokens return you to the request page with a notice; request a new email and try again.

## Token already used

Each recovery token works once. If you opened the email link twice (for example, your browser preview opened it before you did), the second click reports "already used". Request a new recovery email.

## After reset

Existing sessions on your account remain valid. To force every session out, go to Profile → Security → Active Sessions and revoke each.
