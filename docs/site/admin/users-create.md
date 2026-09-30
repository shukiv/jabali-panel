# Create User

Reached from **Users → Create User**. Single-form wizard that provisions every piece of per-user state.

## Required fields

- **Username** — the login identifier (M54: you sign in with the username, not the email). Lowercase, alphanumeric plus `-` and `_`, 3–32 characters. Becomes the Linux account name, the PHP pool name, and the SFTP login. An admin needs a username too, because it is how they sign in, but gets no Linux account, PHP pool or SFTP login.
- **Role** — `user` (hosting customer) or `admin` (full operator access).
- **Package** — selected from [Hosting Packages](./hosting-packages.md). Determines quotas and limits.
- **Primary domain** — the first hosted domain for this user. Created in the same transaction.

## Optional fields

- **Password** — leave blank to auto-generate (recommended). The generated password is displayed once on the success page and never stored in cleartext.
- **Email** — optional contact address. Since usernames are the login, email is informational only; it may be shared across accounts and is not used to sign in. (There is no self-service password recovery — admins reset passwords.)
- **Name** — a single display name shown in the panel header. Can be a person's name or a company name.
- **Enable SFTP** — defaults to on; users without an SSH key cannot SFTP until they add one under SSH Keys.
- **Send welcome email** — sends the credential to the user's email address via Stalwart (only if an email was provided).

## What happens at submit

1. Validate uniqueness of username and primary domain (email is not unique).
2. Create the Kratos identity with the (generated or supplied) password.
3. Insert the `users` row with package and quota links.
4. Create the Linux account with `useradd -m -s /usr/sbin/nologin -G www-data,jabali-sftp`.
5. Enable systemd lingering (`loginctl enable-linger <username>`) so per-user timers fire without an active session.
6. Schedule the reconciler to converge: PHP pool drop-in, quota, slice limits, per-user nftables egress, default mail account for the primary domain.

Most steps complete within five seconds; PHP pool and nginx vhost converge on the next reconciler tick (within 60 seconds).

## Failure modes

| Symptom | Cause | Resolution |
|---|---|---|
| "Username already in use" | A previous user with the same name was deleted but `/home/<name>` was not removed. | `trash /home/<name>` and retry, or pick another name. |
| "Primary domain already exists" | Another user already owns the domain. | Delete the existing domain first or pick another. |
| Quota cannot be set | Filesystem mounted without `usrquota` / `grpquota` options. | See [troubleshooting](../troubleshooting.md). |

## CLI

```bash
jabali user create \
  --username alice \
  --email alice@example.com \
  --display-name "Alice Smith" \
  --role user \
  --package standard \
  --primary-domain alicesite.com
```

Omit `--password` to auto-generate.
