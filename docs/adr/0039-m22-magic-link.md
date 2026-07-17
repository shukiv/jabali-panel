# ADR-0039: Magic-Link Token for Panel→WordPress Admin Single Sign-On

**Status**: superseded by [ADR-0040](./0040-m22-sso-file.md) (2026-04-21)

**Date**: 2026-04-21

**Deciders**: shuki

**Related**:
- [ADR-0036: OIDC for WordPress Admin Access (M16)](./0036-m16-hydra-identity.md) — superseded by this ADR
- [ADR-0038: Rollback of M16 OIDC (M16R)](./0038-m16-rollback.md) — immediate predecessor
- [ADR-0040: Self-Deleting SSO File (M22 Rework)](./0040-m22-sso-file.md) — supersedes this ADR
- M16 Hydra Deployment (runbook decommissioned) — decommissioned by M16R
- [M22 Magic-Link Plan](../../plans/m16-rollback-and-m22-magic-link.md)
- [M22 Rework Plan](../../plans/m22-rework-sso-file.md)

## Why superseded

This design shipped on 2026-04-21 in 11 steps. End-to-end verification on test VM `192.168.100.150` the same day exposed five separate connectivity / lifecycle gaps in one validation session, all caused by the same root pattern: the design requires a **persistent panel-side WordPress plugin** and an **HTTPS callback from WP back to the panel**.

The five gaps: (1) the mu-plugin's "did sed run?" guard contained the literal placeholder strings sed targets — the install-time global sed mutated the guard into a self-comparison that always evaluated true → silent no-op on every request; (2) `panel-api/cmd/server/update.go` doesn't sync the canonical mu-plugin into `/usr/local/lib/jabali/wp-mu-plugins/`, so `jabali update` rebuilds binaries but never deploys the plugin source; (3) `installMagicLinkMUPlugin` only runs during a fresh `wp install`, leaving every pre-M22 WordPress install without the plugin; (4) nginx's default vhost on `:443` returns 444 for any path it doesn't explicitly route, so the WP plugin's `POST /applications/.../magic-link/validate` request is silently dropped; (5) the panel's self-signed cert isn't in the OS CA bundle, so `wp_remote_post sslverify=true` fails with `X509_V_ERR_SELF_SIGNED_CERT_IN_CHAIN`.

All five disappear under the Installatron / Softaculous self-deleting `sso-<nonce>.php` pattern documented in [ADR-0040](./0040-m22-sso-file.md): no persistent WP-side code, no callback, no signing key, no mu-plugin, no nginx routing change, no CA trust setup. This ADR is preserved as historical context — the M22 rework plan at `plans/m22-rework-sso-file.md` documents the migration steps.

## Context

After rolling back the M16 OIDC stack (ADR-0038), we need a simpler mechanism for one-click admin login from the Jabali Panel to a specific WordPress installation. The operator wants: click an "Admin" button on the Applications row → new tab opens → lands signed in as the install's admin user. No OIDC, no consent screens, no federated identity needed—this is same-host, single-user delegation.

The original M16 approach (Hydra + WordPress plugin) added maintenance burden (Hydra binary, database, OIDC spec compliance) for a use case that does not need federation or multi-provider identity. A magic-link token (opaque, single-use, short-TTL, cryptographically signed) is simpler, smaller, and sufficient.

## Decision

We replace M16's OIDC path with a magic-link token mechanism that:

1. **Token Format**: Opaque base64url string, not JWT
   - Shape: `<token_id_b64>.<signature_b64>` where token_id is 16 bytes of random data and signature is 32-byte HMAC-SHA256
   - Rationale for not using JWT:
     - Smaller wire footprint (no JSON, no header overhead)
     - No `alg: none` confusion or algorithm-swap attacks
     - No dependency on a JWT library (one less attack surface)
     - Simpler code path for single-use enforcement
     - Base64url-encoding is enough for URL safety; no cryptographic claim verification needed

2. **Token Cryptography**
   - Hash function: HMAC-SHA256 over `(token_id || install_id || expires_at_unix_le)` using a 32-byte server-side key
   - Server stores SHA256(token_id) in the database, not the full token or signature
   - Verification is constant-time comparison to prevent timing attacks

3. **Token Lifecycle**
   - TTL: 60 seconds from mint
   - Single-use: `used_at` column tracks first successful validation
   - Subsequent validations with the same token return HTTP 410 Gone (not 401)
   - Atomic CAS: `UPDATE magic_link_tokens SET used_at = NOW() WHERE id = ? AND used_at IS NULL` returns row-count; if 0, the token was already used

4. **Storage Model**
   - New table: `magic_link_tokens(id, application_install_id, panel_user_id, token_hash, expires_at, used_at, created_at)`
   - `id`: ULID or equivalent (26 chars, collision-free)
   - `application_install_id`: FK to the install being signed into (cascade delete)
   - `panel_user_id`: which panel user issued this token (for audit)
   - `token_hash`: SHA256(token_id); unique index, only this is stored (DB read leak ≠ valid token)
   - `expires_at`: token expiry time (Unix timestamp, microsecond precision)
   - `used_at`: NULL until validated, then set to current time (prevents replay)
   - `created_at`: audit timestamp

5. **Signing Key Management**
   - Location: `/etc/jabali-panel/magic-link.key` (analog to `sso.key` for phpMyAdmin SSO)
   - Format: comma-separated list of base64-encoded 32-byte keys, newest first
   - Rotation procedure:
     1. Generate new 32-byte key, base64-encode it
     2. Prepend to the key list (new key is now keys[0])
     3. Restart panel-api (graceful; new requests pick up the new key)
     4. Wait ~5 minutes (well past 60s token TTL so all old tokens expire)
     5. Remove the old key from the tail of the list and restart again
   - Sign operation: always uses keys[0]
   - Verify operation: tries all keys in order until one succeeds (backwards-compatible with older tokens)
   - Emergency compromise: if a key leaks mid-flight, operator can immediately remove it (revoking all outstanding tokens issued under that key); acceptable cost for a 60s TTL flow

6. **Validation Contract**
   - Endpoint: `POST /api/v1/applications/:install_id/magic-link/validate`
   - Request: `{ "token": "<base64url signed token>" }`
   - Response (200 OK): `{ "admin_user": "<wordpress username>", "expires_in": <seconds remaining of original 60s TTL> }`
   - Response (410 Gone): token already used or expired
   - Response (400 Bad Request): malformed token or signature verification failed
   - Response (429 Too Many Requests): rate-limited per client IP (Gin's rate-limit middleware)
   - Auth: unauthenticated (token is the credential); WP plugin is the caller (server-to-server POST via TLS)

7. **WordPress Must-Use Plugin Contract**
   - Activation: shipped as a must-use plugin bundled with every WordPress install
   - Detection: `init` hook checks for `$_GET['jabali_admin_login']` query parameter
   - Behavior on malformed token (empty, wrong length, etc.): silently `unset($_GET['jabali_admin_login'])` and return; do not `wp_die()`
   - Validation flow:
     1. Extract token from `$_GET['jabali_admin_login']`
     2. POST to `https://<panel-api-host>/api/v1/applications/<install_id>/magic-link/validate` with JSON body
     3. On 200 response: extract `admin_user` field, call `wp_set_auth_cookie($user_id, $remember = false)`
     4. On 4xx/5xx response: log response code and short reason (e.g., "expired" or "used") to PHP error_log; do not log the token
   - Security: must set `Referrer-Policy: no-referrer` in response to prevent leaked referrer on redirect
   - Install ID: passed to plugin at install time via a PHP constant (set by install.sh); plugin uses this on every validate POST

## Threat Model

### 1. Token Leak in URL

**Threat**: Attacker intercepts the magical URL in transit (e.g., MITM on cleartext WiFi, proxy logging).

**Mitigations**:
- Token is single-use; attacker gets one attempt within 60 seconds
- 60-second TTL means the window is very narrow
- Token is install-scoped; attacker can only log in to that specific WordPress install
- TLS everywhere (enforced by infrastructure; no cleartext panel URLs)
- Must-use plugin sets `Referrer-Policy: no-referrer` so referrer leakage to third-party resources is blocked

**Residual risk**: Acceptable. An attacker with live MITM within 60 seconds gets one admin login to a specific install. Operator can detect via WordPress audit logs. Consequence is same as operator clicking the button during MITM (admin access to that install only, not privilege escalation).

---

### 2. Replay (Same Token, Second Validation)

**Threat**: Attacker captures the signed token and replays it after first use.

**Mitigations**:
- `used_at` atomic column prevents second use
- Validation runs inside a serialized transaction: `BEGIN`, `SELECT … FOR UPDATE NOWAIT WHERE id = ?`, then `UPDATE magic_link_tokens SET used_at = NOW() WHERE id = ? AND used_at IS NULL`, `COMMIT`
- If `FOR UPDATE NOWAIT` cannot acquire the lock (another validate is in flight), return HTTP 429 Too Many Requests immediately rather than block — caller (WP plugin) treats this as a hard fail, not a retry
- If the `UPDATE` returns zero rows, the token was already used — return 410 Gone
- Both losers (NOWAIT-blocked + zero-rows) carry distinct status codes so operator audit log makes the race observable

**Residual risk**: Low. Serialised CAS prevents duplicate logins under the WP-honest case. If WordPress is fully compromised and controlled by an attacker, the attacker can already execute arbitrary PHP — the marginal risk of an extra wp_set_auth_cookie via parallel validates is dwarfed by the underlying breach. Single-use guarantees at most one extra login per token; audit log captures both. Acceptable for single-operator self-hosted use.

---

### 3. Token Forgery

**Threat**: Attacker crafts a valid token without knowing the signing key.

**Mitigations**:
- Token is signed with HMAC-SHA256 using a 32-byte server-side key
- Signature is over `(token_id || install_id || expires_at)` concatenated as binary
- Signature length-extension attacks on HMAC-SHA256 are cryptographically impossible (HMAC is a PRF, not vulnerable to length extension)
- Verification is constant-time comparison (no timing leaks on signature mismatch)

**Residual risk**: Negligible. HMAC-SHA256 is a standard, well-audited construction. No known attacks exist.

---

### 4. Signature Key Leak

**Threat**: Operator's `/etc/jabali-panel/magic-link.key` is read by an attacker (e.g., via file disclosure in panel-api).

**Mitigations**:
- Key file is owned by `jabali-panel` user, mode `0600` (readable only by panel-api process)
- **Boot-time guard**: panel-api startup reads `/etc/jabali-panel/magic-link.key`, validates that it exists, is parseable as comma-separated base64url 32B values, and has mode exactly `0600`. On any failure, panel-api refuses to start with an explicit error naming the file and the required mode. No silent fallback to an empty key list, no default key. Operator runbook documents recovery: install.sh's `install_magic_link_key` step is idempotent and safe to re-run.
- Key rotation procedure allows operator to retire a compromised key
- Outstanding tokens issued under the old key continue to work (single-use + 60s TTL), but new tokens use the new key
- Emergency key compromise: operator immediately removes the leaked key, revoking all outstanding tokens (acceptable cost for 60s TTL)
- Key format is base64url-encoded 32 bytes; no human-readable secrets in the file

**Residual risk**: Medium (depends on how quickly operator detects and acts). Acceptable because:
- All tokens expire within 60 seconds
- Operator can rotate and revoke immediately
- Token is scoped to a single install; attacker can't escalate to other installs
- Audit logs will show unexpected logins during compromise window

---

### 5. Cross-Install Replay

**Threat**: Attacker captures a magic-link token for Install A and tries to use it to log in to Install B.

**Mitigations**:
- The HMAC is computed over the binary concatenation `(token_id || install_id || expires_at_unix_le)`
- On validation, the endpoint extracts `install_id` from the URL path (`/api/v1/applications/:install_id/magic-link/validate`), looks up the token row by `token_hash = SHA256(token_id)`, retrieves the row's stored `application_install_id`, and checks both: (a) the URL `install_id` matches the row's `application_install_id`, and (b) the HMAC verifies under the same `install_id`. Either mismatch fails validation. An attacker cannot reuse a token across installs without re-signing, which requires the key.

**Residual risk**: Negligible. Signature binding to install_id is cryptographically enforced AND independently checked against the URL path on every request.

---

### 6. CSRF on Validate Endpoint

**Threat**: A malicious website tricks the WP server into calling the validate endpoint with a crafted token.

**Mitigations**:
- Validate endpoint is unauthenticated (no panel session cookie to steal)
- Auth is the token itself; only the token holder can validate
- **Rate-limit is per `token_id`, not per client IP**: a token can only succeed once anyway, so excess validates to the same `token_id` are bounded (limit: 10 attempts per `token_id` per 60s window). Rejected per-IP rate-limiting because all WordPress installs on a host share the same panel-facing IP, so per-IP would let one compromised install DoS validates for every other install on the same host.
- WP plugin POSTs to the endpoint (not GET); browser cannot trigger this via `<img>` or `<iframe>`
- Endpoint returns JSON, not HTML; browser cannot interpret it as a page redirect

**Compromised-WP escalation note**: A compromised WordPress install can extract tokens from referer/log/history sources within its own install and call validate before the operator's browser does. This is single-use guarded (only the first validate succeeds) and the underlying breach (PHP RCE on the WP install) is more severe than the marginal one-extra-login it enables. Documented here for completeness; not a separate mitigation surface.

**Residual risk**: Low. The WP server must explicitly POST a valid, in-flight token; an attacker cannot force this via CSRF from a browser. Per-token rate-limit prevents same-host noisy-neighbor DoS.

---

### 7. Phishing the Operator

**Threat**: Operator is tricked into clicking a malicious link that looks like a magic-link but isn't.

**Mitigations**:
- Magic-link URL pattern is recognizable: `https://<install-wordpress-domain>/?jabali_admin_login=<token>`
- The domain in the URL is the WordPress install being logged into (not hidden)
- Panel UI button must display the target domain before the operator clicks (e.g., "Log in to admin.example.com?")
- Operator can see the URL before clicking and verify it's the intended install

**Residual risk**: Medium (user education). Acceptable because operator sees the target domain before click and can audit the URL. This is no worse than any other single-sign-on link.

---

### 8. Token Leak via Logs and APM

**Threat**: Full token appears in server logs, APM traces, or error messages, allowing attacker to reconstruct a valid token.

**Mitigations**:
- Panel logger (Gin): registers a `LogFormatter` that scrubs query parameters matching `jabali_admin_login` and POST body fields matching certain patterns; replaces with `<redacted>`
- Must-use plugin: logs only response status code and reason string (e.g., "expired"), never the token itself, to PHP error_log
- Operator runbook instructs: if WordPress install has NewRelic, Datadog, or other APM, add `jabali_admin_login` to the APM's URL parameter denylist
- Database layer: panel-api stores token_hash (SHA256), not the token; database dumps don't leak live tokens

**Residual risk**: Low if all logging/APM scrubbing is implemented. If scrubbing is skipped, tokens can leak. This is an implementation detail captured in Step 11 runbook.

---

### 9. Collision with Legitimate Query String

**Threat**: Legitimate WordPress page has a `?jabali_admin_login=value` query parameter (collision). Attacker can break the page by appending `?jabali_admin_login=junk` to any public URL.

**Mitigations**:
- Must-use plugin checks token format before validation (length, base64url characters)
- On malformed token, plugin silently `unset($_GET['jabali_admin_login'])` and allows normal page load
- Plugin does NOT call `wp_die()` or throw an error; the page renders normally with the query param removed
- Collision risk is negligible (`jabali_admin_login` is a namespaced key not commonly used by other plugins)

**Residual risk**: Very low. Plugin graceful-fails on malformed input.

---

### 10. Clock Skew Between Panel and WordPress

**Threat**: Panel system clock and WordPress system clock drift apart (NTP issue, VM clock jump). Token minted at panel-time `T=0` with `expires_at = T+60` may appear already-expired to a WordPress install running 15 seconds ahead.

**Mitigations**:
- Validator applies a 10-second backwards clock-skew tolerance: token is accepted if `NOW() <= expires_at + 10`
- Real-world threshold chosen because typical NTP drift is sub-second; >10s skew almost certainly indicates a broken NTP daemon (which the operator should fix anyway)
- Forward direction is unaffected (a clock running behind makes tokens live slightly longer, which is acceptable for a 60s window)

**Residual risk**: Negligible if NTP is functional. If clock skew >10s, operator sees consistent "token expired immediately" UX and is pushed to fix the underlying clock problem.

---

### 11. Browser Prefetch Consumes the Token

**Threat**: Operator clicks the panel UI button. Modern browsers (Chrome, Firefox) speculatively prefetch the URL via HEAD or background GET *before* the operator's full navigation. The must-use plugin's `wp_loaded` hook fires on the prefetch request, validates the token, and consumes `used_at`. By the time the operator's actual navigation arrives, the token is already used → 410 Gone in their face.

**Mitigations**:
- Must-use plugin only validates on `$_SERVER['REQUEST_METHOD'] === 'GET'`. HEAD, OPTIONS, and similar prefetch methods skip validation entirely (let WP serve a normal head response).
- Plugin checks for prefetch hint headers — `Sec-Purpose: prefetch`, `Purpose: prefetch`, `X-Moz: prefetch`, `X-Purpose: preview`. If any is present, skip validation.
- Plugin hooks to `wp_loaded` (after WP user/session infrastructure is fully initialised) instead of `init` so a partial-load environment doesn't half-consume the token.

**Residual risk**: Low. The remaining edge case is a browser that prefetches with a full GET and no Sec-Purpose header — uncommon and can be addressed in a follow-up if seen in the wild.

---

## Alternatives Considered

### Alternative 1: Keep M16 OIDC (Hydra + WordPress Plugin)

**Pros**:
- Federation-ready (can add other OIDC providers later)
- Standard protocol (operator may understand OIDC better than custom tokens)

**Cons**:
- Maintenance burden: Hydra binary (binary updates, CVEs), Hydra database (MariaDB schema, migrations), Hydra API (config, client registration)
- Federation is not needed for same-host admin access
- Adds operator cognitive load (consent screens, PKCE, token refresh)
- M16 is already rolled back (ADR-0038); re-implementing it is not an option

**Decision**: Rejected. OIDC is overengineered for this use case.

---

### Alternative 2: Signed JWT Cookies

**Pros**:
- JWT is standard
- No database row per token (JWT is self-contained)

**Cons**:
- WordPress plugin cannot consume a JWT cookie in a browser-safe way (would need to set the cookie via JavaScript, which opens XSS surface)
- No single-use enforcement without server-side state (defeating the stateless JWT benefit)
- JWT signature algorithm confusion risk (same as why we rejected JWT for this token)
- WordPress plugin must implement JWT validation (add dependency on JWT library or write custom verification)

**Decision**: Rejected. Complexity gains (JWT verification, cookie handling) outweigh the benefit of not hitting the database for a single 60-second token.

---

### Alternative 3: SSH-Key Derived Tokens

**Pros**:
- Asymmetric (operator keeps private key, panel only stores public key)
- No shared secret to rotate

**Cons**:
- Operator UX is bad (how does operator generate an SSH key just to log into WordPress?)
- Not a standard pattern for web sign-on
- Signature is larger than HMAC (RSA 2048 is ~256 bytes vs HMAC's 32 bytes)

**Decision**: Rejected. Overcomplicates operator workflow.

---

### Alternative 4: SAML 2.0

**Pros**:
- Enterprise-standard protocol

**Cons**:
- Absurd complexity for same-host, single-user delegation
- WordPress plugin would need to implement SAML SP (multiple libraries, dozens of files)
- XML parsing adds attack surface

**Decision**: Rejected. Complete overkill.

---

## Consequences

### Positive

1. **Simpler mental model** for operators: "magic link" is intuitive (compare: "OIDC grant flow with Hydra consent screen")
2. **Smaller code footprint** in panel-api: ~200 lines of signing/verifying logic + ~100 lines of repository
3. **No external binary dependency**: Hydra binary, Docker image, binary updates gone
4. **Faster to implement**: Steps 9–11 are ~400 lines vs ~1000+ lines for Hydra integration
5. **Easier to debug**: tokens are opaque, not carrying claims; failure mode is "expired" or "invalid", not "claim mismatch"

### Negative

1. **Single-use tokens require database hit**: every validate query touches the database (not a hot path, acceptable)
2. **No federation ready**: if future requirement is to add other OIDC providers, this does not help (not an intended use case)
3. **Key rotation ceremony required**: operator must manually edit the key file and restart panel-api (acceptable; keys rotate rarely)
4. **Token is short-lived (60s)**: operator must complete login flow quickly or re-request token (acceptable; UX is "click button, new tab opens, sign in happens immediately")

## Acceptance Criteria

- [x] ADR-0039 Status changed to `accepted` after adversarial review (Opus-tier security review of threat model and cryptography — 2026-04-21)
- [ ] Token format enforced: base64url(token_id).base64url(hmac), exactly 2 dot-separated components
- [ ] TTL enforced with 10s backwards clock-skew tolerance: token expires exactly 60 seconds after mint per panel clock; validator accepts up to `expires_at + 10s`; integration test mocks WP clock +15s ahead and asserts validate succeeds
- [ ] Single-use enforced via serialised transaction: validate runs `SELECT … FOR UPDATE NOWAIT` then atomic `UPDATE … WHERE used_at IS NULL`; concurrent validates → first wins 200, second gets 429 (NOWAIT-blocked) or 410 (zero rows)
- [ ] Signature verification is constant-time (`crypto/subtle.ConstantTimeCompare`, verified via code inspection; no timing side-channel)
- [ ] Multi-key rotation documented: key file format (base64 CSV, newest first), Sign uses keys[0], Verify tries all; operator runbook covers rotation ceremony
- [ ] **Boot-time key guard**: panel-api startup fails fast with explicit error if `/etc/jabali-panel/magic-link.key` is missing, unparseable, or has mode != `0600`; no silent fallback; integration test covers all three failure modes
- [ ] Must-use plugin silent no-op on malformed tokens: appending `?jabali_admin_login=junk` to any page renders the page normally (no error, no redirect)
- [ ] **Browser-prefetch protection**: must-use plugin skips validation on HEAD requests and on requests carrying `Sec-Purpose: prefetch` / `Purpose: prefetch` / `X-Moz: prefetch` / `X-Purpose: preview`; integration test: `curl -I` to magic-link URL does NOT consume the token, subsequent GET succeeds
- [ ] **Plugin hook timing**: must-use plugin hooks `wp_loaded` (not `init`) so `wp_set_auth_cookie()` runs against a fully-initialised WP environment
- [ ] Log scrubbing verified: integration test confirms `jabali_admin_login` parameter and token never appear in panel-api access log; gin LogFormatter redacts the value to `<redacted>`
- [ ] Plugin error_log scrubbing: must-use plugin logs only HTTP status code + short reason on validate failure; never the token, never the response body
- [ ] **Referrer-Policy header**: must-use plugin emits `header('Referrer-Policy: no-referrer')` before the redirect to `/wp-admin/` so the URL is not leaked via Referer to wp-admin's third-party assets
- [ ] Cross-install replay rejected: token signed with install_id; URL path install_id mismatch fails verification with 400 (not 410, not 401 — distinct status so the audit log is unambiguous)
- [ ] **Per-token rate-limit**: validate endpoint allows ≤10 attempts per `token_id` per 60s window (NOT per-IP, which would let one compromised same-host install DoS others)

## Implementation Notes for Steps 9–11

- **Step 9** (Token Model + Migration): Schema, GORM model, signer/verifier, repository
- **Step 10** (API Handlers + WP Plugin): POST /api/v1/applications/:id/magic-link (issue token), POST /api/v1/applications/:id/magic-link/validate (consume token), must-use plugin
- **Step 11** (Runbook + E2E): `/etc/jabali-panel/magic-link.key` provisioning, key rotation ceremony, operator runbook, WordPress admin one-click test
