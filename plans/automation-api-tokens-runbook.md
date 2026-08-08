# M44 Automation API — operator + caller runbook

Reference for both sides of the bearer-token contract: admins
operating the panel and automations (CI scripts, monitoring) that
hit `/api/v1/automation/*`.

## Operator — minting a token

1. Navigate to `/jabali-admin/automation`.
2. Click **Mint Token**.
3. Enter a human-readable name (unique). Examples:
   `monitoring-bot`, `ci-deploy`, `partner-checkout-uptime`.
4. Tick the narrowest scope set the caller actually needs.
   - `read:*` is the wildcard shortcut. Tick it only when the
     caller really does need every read.
   - Otherwise pick from `read:domains`, `read:users`,
     `read:applications`, `read:status`, `read:mail`, `read:packages`.
   - Write/delete scopes (`write:users`, `delete:users`, …) gate the
     JAB-140/ADR-0164 mutation endpoints — treat any token holding them
     as an admin credential (IP allowlist + expiry + `writes_enabled`
     kill switch; see ADR-0157). Billing panels (WHMCS/Blesta/WiseCP)
     need `read:status`, `read:users`, `write:users` and, for
     termination, `delete:users`.
5. Click **Mint**.
6. The one-time-secret modal pops with the plaintext token.
   **Copy it now**: the server only keeps an AES-GCM-encrypted
   copy. If you lose it, your only recourse is **Revoke + remint**.

## Operator — revoking a token

1. Navigate to `/jabali-admin/automation`.
2. Locate the row.
3. Click **Revoke**, confirm in the popconfirm.
4. The row stays visible with a red `revoked at <ts>` tag for
   audit. External calls using the token start failing within
   the next request (no cache window).

## Caller — signing a request

```
Authorization: Jabali-HMAC kid=<token-id>, ts=<unix-seconds>, sig=<hex>

sig = hex(HMAC_SHA256(secret,
                       METHOD || "\n"
                    || PATH   || "\n"  // includes query string
                    || ts     || "\n"
                    || hex(sha256(BODY))))
```

`ts` must be within 5 minutes of the server clock; sync your
caller via NTP or risk intermittent 401s.

`PATH` includes the query string. `/api/v1/automation/domains?page=1`
and `/api/v1/automation/domains?page=2` produce different signatures.

`BODY` is the raw request body. For GET requests with no body, hash
the empty string (`sha256("")` = `e3b0c44...`).

### Bash + curl + openssl

```bash
TOKEN_ID="01K..."
TOKEN_SECRET="9d30e1edeb89fac42974a73fda1238de394d073785ac4959..."
PANEL="https://panel.example.com"

METHOD=GET
PATH_Q=/api/v1/automation/status
TS=$(date +%s)
BODY=""

BODY_HASH=$(printf "%s" "$BODY" | openssl dgst -sha256 -hex | awk '{print $2}')
TO_SIGN=$(printf "%s\n%s\n%s\n%s" "$METHOD" "$PATH_Q" "$TS" "$BODY_HASH")
SIG=$(printf "%s" "$TO_SIGN" | openssl dgst -sha256 -hmac "$TOKEN_SECRET" -hex | awk '{print $2}')

curl -fsS \
  -H "Authorization: Jabali-HMAC kid=${TOKEN_ID}, ts=${TS}, sig=${SIG}" \
  "${PANEL}${PATH_Q}"
```

### Python (stdlib only)

```python
import hmac, hashlib, time, urllib.request, json

TOKEN_ID = "01K..."
SECRET = b"9d30e1edeb89fac42974a73fda1238de394d073785ac4959..."
PANEL = "https://panel.example.com"

method = "GET"
path = "/api/v1/automation/domains"
ts = str(int(time.time()))
body = b""

body_hash = hashlib.sha256(body).hexdigest()
to_sign = "\n".join([method, path, ts, body_hash]).encode()
sig = hmac.new(SECRET, to_sign, hashlib.sha256).hexdigest()

req = urllib.request.Request(
    PANEL + path,
    headers={
        "Authorization": f"Jabali-HMAC kid={TOKEN_ID}, ts={ts}, sig={sig}",
    },
)
with urllib.request.urlopen(req) as resp:
    print(json.load(resp))
```

## Caller — endpoint reference

All return `{"data": [...], "total": N}` shape unless noted.

| Endpoint                               | Scope                | Notes |
|----------------------------------------|----------------------|-------|
| GET /api/v1/automation/status          | read:status          | `{healthy, time}`. Cheap uptime probe. |
| GET /api/v1/automation/domains         | read:domains         | Flat: id, name, user_id, is_enabled. |
| GET /api/v1/automation/users           | read:users           | Flat: id, email, username, package_id, is_admin. |
| GET /api/v1/automation/applications    | read:applications    | Flat: id, app_type, domain_id, status. |

Wildcard `read:*` matches every `read:...` capability.

## Failure modes

| HTTP | Body                                 | Cause |
|------|--------------------------------------|-------|
| 401  | missing or invalid authorization header | No `Jabali-HMAC` prefix or required params absent. |
| 401  | invalid timestamp                    | `ts` not parseable as integer. |
| 401  | timestamp outside window             | `ts` more than 5 minutes off server clock. |
| 401  | invalid token                        | kid not found, or revoked. |
| 401  | invalid signature                    | HMAC mismatch (wrong secret, body alteration, etc.). |
| 403  | scope X not granted to token        | Token doesn't carry the required scope. |
| 413  | body too large                       | Body exceeds 1 MiB cap. |
| 503  | automation api disabled              | `AutomationTokens` repo not wired (early-boot, dev). |

## Threat model + secret hygiene

- Treat the token plaintext like a password. Store in your
  caller's secret manager (Vault, AWS Secrets Manager, etc.).
- Rotate by **revoke + remint** — no in-place rotation in v1.
- The 5-minute timestamp window doesn't prevent replay *inside*
  the window; if the token leaks, revoke immediately. Future v2
  will add a nonce/JTI store if leak-tolerance is relaxed.
- The panel never logs the plaintext secret. The one-time-reveal
  modal is the only place it appears.

## Operational notes

- The `last_used_at` + `last_used_ip` columns are best-effort
  audit; if the panel's MariaDB is unreachable when the audit
  goroutine runs, the verified request still completes (we don't
  fail the response on audit-write failure).
- The token list view is admin-only; users can't see other
  tenants' tokens.
- Revoked tokens are not auto-reaped. Future operator CLI can
  prune them; for now they're forensic breadcrumbs.
