# Automation API

`/jabali-admin/automation`. Tokens for the server-wide Automation API.

The Automation API (`/api/v1/automation/`) is for systems that manage the whole
server: a billing panel that creates and suspends hosting accounts, or a fleet
manager that watches health and mail. It is not the per-user API. A user's own
scripts use **API Tokens** in their shell, which act as that user and see only
what the user owns. An automation token is server-wide, and its **scopes** limit
it instead.

The API answers on the panel's `:8443` port. Some billing hosts cannot reach
`:8443` because their outbound firewall blocks it (CSF's default `TCP_OUT`, for
example). For them, turn on **Also serve the API on port 443** on this page. It
serves only the signed `/api/v1/automation/` routes on `:443`; everything else
stays on `:8443`.

## Minting a token

Click **Create token**, give it a name, pick its scopes, and click **Generate**.
The secret is shown once, as a 64-character hex string. Copy it: the panel keeps
it encrypted and never shows it again.

From the command line:

```sh
jabali automation-token mint billing --scope read:users --scope write:users
jabali automation-token list
jabali automation-token revoke billing
```

## Scopes

Grant only what the caller needs. A read scope never implies a write scope, and
a write scope never implies a delete scope.

| Scope | Allows |
|---|---|
| `read:domains` | list domains |
| `read:users` | list and look up accounts; disk and bandwidth usage |
| `read:applications` | list installed applications |
| `read:packages` | list hosting packages |
| `read:mail` | mailboxes, mail domains, forwarders, groups, autoresponder status, totals |
| `read:status` | panel health and raw host metrics (`/automation/status`) |
| `read:metrics` | normalised host metrics for a fleet monitor (`/automation/server-status`) |
| `read:*` | every read scope (cannot be combined with individual read scopes) |
| `write:users` | create accounts, set passwords, assign packages, suspend and unsuspend, mint one-time login links |
| `write:domains` | suspend and unsuspend domains; create a primary domain with a new account |
| `write:cache` | purge the web cache |
| `write:backups` | start a server backup |
| `write:services` | restart a panel service on the allow-list |
| `write:*` | every write scope |
| `delete:users` | delete an account and everything it owns |
| `delete:*` | every delete scope |
| `assert:domain_ownership` | a domain the token creates (for example with a new account from a billing system) is verified at once instead of waiting for its owner's DNS proof (GH #1816). The billing system vouches that its customer owns the name. It is its own scope family: `write:*` does not include it, so grant it only to a billing system you trust. |

**Writes enabled** is a switch on each write-scoped token. Turn it off to pause a
token's writes without revoking it.

## Signing a request

Every request carries an `Authorization` header signed with the secret:

```
Authorization: Jabali-HMAC kid=<token id>, ts=<unix seconds>, sig=<hex>
```

`sig` is the hex HMAC-SHA256, keyed with the secret exactly as shown at mint,
over four lines joined by `\n`:

1. the method, e.g. `GET`
2. the full request URI, path and query, e.g. `/api/v1/automation/users?email=alice%40example.com`
3. `ts`
4. the hex SHA-256 of the request body (of the empty string when there is none)

`ts` must be within 5 minutes of the panel's clock, and no more than 30 seconds
ahead. Each signature is accepted once, so a captured request cannot be
replayed. Bodies are limited to 1 MiB.

```sh
KID=01K...                      # token id
SECRET=...                      # the 64-character secret
URI='/api/v1/automation/capabilities'
BODY=''
TS=$(date +%s)
BODY_HASH=$(printf '%s' "$BODY" | sha256sum | cut -d' ' -f1)
SIG=$(printf 'GET\n%s\n%s\n%s' "$URI" "$TS" "$BODY_HASH" \
  | openssl dgst -sha256 -hmac "$SECRET" -hex | awk '{print $NF}')
curl -s "https://panel.example.com:8443$URI" \
  -H "Authorization: Jabali-HMAC kid=$KID, ts=$TS, sig=$SIG"
```

A bad header, timestamp, token or signature answers `401`. A token without the
route's scope answers `403`.

## Endpoints

Every route, with its scope, body and responses, is in the panel's **API Docs**
page under the **Automation** tag (also at `/api/v1/_meta/openapi.json`). Start
with `GET /api/v1/automation/capabilities`: any valid token may call it, and it
lists the write and billing actions this panel serves.

Behaviour worth knowing before you build on it:

- **Writes are rate-limited** to 30 a minute per token, with bursts of up to 10.
- **Every write is audited** under the token's id, and creating, suspending or
  deleting an account raises a panel notification.
- **Account creation is safe to retry.** When the email and username already
  belong to the same account, the call answers `200` with `status: exists` and
  that account's id.
- **Deleting an account** needs `confirm: true`. Send `dry_run: true` first to
  see what would be deleted.
- **Admin accounts are out of reach.** The API never creates one, and refuses
  to delete, suspend, change the password of, or mint a login link for one.
- **Backups are asynchronous.** `POST /automation/backups` answers `202` with an
  operation id; poll `GET /automation/operations/{id}`. Send an
  `Idempotency-Key` header so a retry does not start a second backup.
- **Some lists are capped** at 200 rows: domains, users and applications.

## Fleet monitor metrics

`GET /api/v1/automation/server-status` (scope `read:metrics`) returns a thinned,
normalised host-metrics envelope, so a fleet monitor gets live health without
host topology:

```json
{
  "as_of": "2026-08-24T00:00:00Z",
  "cpu":  { "usage_percent": 12.5, "iowait_percent": 1.2 },
  "host": {
    "mem_used_kb": 4000000,
    "mem_total_kb": 8000000,
    "load_avg": [0.5, 0.4, 0.3],
    "partitions": [ { "mount_point": "/", "used_bytes": 40, "total_bytes": 100 } ]
  }
}
```

Each slice degrades on its own: if one collector fails, the rest still render
and the failure is reported under `errors`. Results are cached for a few seconds,
so frequent polling does not load the server.

## Revocation

Revoke from this page or with `jabali automation-token revoke`. The token stops
working on its next request.
