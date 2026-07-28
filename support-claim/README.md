# support-claim

A tiny operator-run service that turns a Jabali **diagnostic bundle**'s
`{enclosed link + password}` into a short, **public-safe claim code** — so a user
can paste `JAB-7QX9K2P4` into a GitHub issue instead of a long URL + password.

## Why it exists

`enclosed` (the diagnostic-bundle host) is **end-to-end encrypted** — its server
never sees the decryption password. So it can't hand a password back on
redemption, which means it can't be the thing that resolves a short code to the
bundle. Something operator-side has to hold `{url, password}` and release it
**only to the authenticated support team**. That is this service — nothing else.

## Security model

- **Issue** (`POST /claims`) is open. A caller can only shorten data they already
  hold (their own bundle's url+password), so there's no inbound secret to guard.
  Bounded by a 16 KiB body cap, a per-IP rate limit, and a short TTL.
- **Redeem** (`GET /claims/{code}`) requires `Authorization: Bearer <token>`.
  **The code alone is useless** — that's what makes it safe to paste in public.
  The token is constant-time compared.
- **Storage is in-memory** with a TTL sweeper. A restart drops pending claims
  (fine — short-lived + regenerable). No secrets touch disk.

## API

```
POST /claims
  body: {"url","password","host","panel_sha","note_id","byte_count","file_count"}
  200:  {"code":"JAB-7QX9K2P4","expires_at":"2026-08-11T02:24:58Z"}

GET /claims/{code}          Authorization: Bearer <SUPPORT_CLAIM_TOKEN>
  200: {"url","password","host","panel_sha","note_id","byte_count","file_count"}
  401: missing/wrong token
  404: unknown or expired code

GET /healthz  ->  200 ok
```

## Run

```bash
go build -o support-claim ./...
SUPPORT_CLAIM_TOKEN=<long-random-team-secret> \
SUPPORT_CLAIM_LISTEN=:8088 \
SUPPORT_CLAIM_TTL_HOURS=336 \
SUPPORT_CLAIM_RATE_PER_MIN=30 \
  ./support-claim
```

Put it behind TLS (a reverse proxy) next to your enclosed deployment, e.g.
`https://claims.jabali-panel.com`.

## Wire the panel to it

Set `JABALI_CLAIM_URL` in the **jabali-agent** environment (drop-in on
`jabali-agent.service`):

```
Environment=JABALI_CLAIM_URL=https://claims.jabali-panel.com
```

When set, `jabali system diagnostic` (and Support → Send Diagnostic Report)
register a claim after uploading the bundle and show the short code as the
primary hand-off. When unset, they fall back to the raw link + password — no
request is made to a non-existent service.

## Redeem (support side)

```bash
curl -s -H "Authorization: Bearer $SUPPORT_CLAIM_TOKEN" \
  https://claims.jabali-panel.com/claims/JAB-7QX9K2P4 | jq
```
