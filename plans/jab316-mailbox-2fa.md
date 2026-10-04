# Admin-provisioned mailbox TOTP 2FA (GH #316)

**Status:** NOT BUILT. Paused on 2026-08-22 pending an upstream Stalwart change (see GH #316). The panel side shipped (webmail enable/disable; the TOTP toggle that could not persist is hidden). Kept so the design is not lost.

**Objective:** let an admin enroll TOTP two-factor auth on a Stalwart mailbox
that actually persists, working around Stalwart's external-SQL-directory limit
(self-service webmail 2FA can't persist because the directory is read-only from
Stalwart's side).

**Base branch:** `main`. **Depends on nothing external.** **Priority:** the
remaining half of GH #316 (enable/disable webmail already shipped).

---

## Background (cold-start context, verified this session)

- **Root cause (confirmed on a live host + Stalwart docs via Context7):** jabali
  runs Stalwart against an **external read-only SQL directory** — accounts come
  from the panel `mailboxes` table. Per Stalwart's docs, *"2FA management via the
  WebUI is only available with the internal directory. For external directories
  (LDAP/SQL), administrators must provision the OTP Auth URL as an account
  secret."* So the webmail lets a user toggle TOTP on but there's nowhere
  writable → it's gone next login.
- **The fix mechanism (Context7-confirmed):** a Stalwart account can hold
  **multiple secrets** ("accounts can hold more than one hash simultaneously").
  The OTP is stored as the account's `otpAuth` secret — an
  `otpauth://totp/<label>?secret=<BASE32>&issuer=<issuer>` URL. When an account
  has an otpauth secret, Stalwart **enforces** TOTP. For a SQL directory,
  Stalwart reads each returned row's `columnSecret` value as a secret — so
  returning a SECOND row carrying the otpauth URL (under the same
  `columnSecret`=`password_hash` column) adds it as a secret.
- **Current directory config** (`install/stalwart/apply-plan.json.tmpl`):
  - `columnEmail: email_cached`, `columnSecret: password_hash`
  - `queryLogin: SELECT email_cached, password_hash FROM mailboxes WHERE email_cached = ? AND is_disabled = 0`
  - `queryRecipient: SELECT ... password_hash ... UNION ...` (aliases/groups).
- **The ADR-0073 converger** (`install.sh` `_install_stalwart_apply_plan`,
  ~line 10836) re-`stalwart-cli update Directory <id>`s `queryRecipient` +
  `queryEmailAliases` on every `jabali update` (the base plan only *creates* the
  directory once; query edits redeploy here — NOT via the apply plan, because
  the directory id is server-generated). **queryLogin is NOT converged there
  today** — it's only set at create. So a query-shape change to queryLogin needs
  either adding it to the converger OR is only picked up on a fresh directory.
  **Design decision (Step 2): add queryLogin to the converger so existing hosts
  redeploy it on `jabali update`.**
- **No new dependencies:** the panel only MINTS the secret + URL (Stalwart
  validates codes). `crypto/rand` + `encoding/base32` (stdlib) generate the
  secret; build the otpauth URL by hand. Frontend: antd 5 ships `<QRCode>` — no
  QR lib. So this feature adds zero deps.
- **Reveal-once precedent to mirror:** `users_password_reset.go` (generate →
  store → return plaintext ONCE, never logged/persisted-in-plaintext-response).
  Mailbox password + db-user creds follow the same pattern.
- **Migrations:** `panel-api/internal/db/migrations`, golang-migrate
  `NNNNNN_name.up.sql` / `.down.sql`. Highest is `000217`; **next is `000218`**.
- **Mailbox surfaces:** model `panel-api/internal/models/mailbox.go`
  (`is_disabled`, `email_cached`, `password_hash`, `domain_id`); repo
  `panel-api/internal/repository/mailbox_repository.go`; API
  `panel-api/internal/api/mailboxes.go`; admin UI
  `panel-ui/src/shells/admin/domains/DomainMailboxesSection.tsx` +
  `panel-ui/src/components/mail/EditMailboxModal.tsx`; hook
  `panel-ui/src/hooks/useMailboxes.ts`.

### THE load-bearing risk (Step 2 gate)

The exact multi-secret SQL shape MUST be verified against a live Stalwart before
the query is finalized. The hypothesis: `queryLogin` returns 1–2 rows for one
email — row 1 `(email, password_hash)`, row 2 `(email, otpauth_url)` when
`otpauth_url IS NOT NULL` — and Stalwart treats both as secrets, enforcing TOTP.
If Stalwart instead expects a single row / a distinct otpauth column / a
different plaintext-vs-hash detection, the query shape changes. **Verify on
192.168.100.86 (Step 2) BEFORE writing the migration-dependent code paths.**
A wrong query silently breaks mailbox auth for EVERY mailbox (login returns the
wrong secret set) — treat it like the pdns/stalwart idempotency scars.

### Secret-at-rest note (accepted)

The TOTP secret is INHERENTLY plaintext at rest — TOTP is a shared-secret scheme,
so `otpauth_url` (which contains the base32 secret) cannot be hashed; Stalwart
must read it verbatim from the directory to validate codes. Mitigations:
`json:"-"` so it never leaves the API; audit enroll/disable; rely on DB access
controls (same DB that already holds bcrypt password hashes). This is the same
tradeoff every TOTP store makes; do not "fix" it by hashing.

### Scope note — the webmail self-service toggle still misleads (recommended add-on)

Admin-provisioning makes TOTP PERSIST, but Bulwark's webmail still shows a
self-service "enable 2FA" toggle that STILL can't persist on the external
directory — so GH #316's literal report ("I toggle it in webmail, it's gone")
is only fully closed if that toggle is also hidden/redirected. The operator
chose the full admin-provisioned path over the interim hide, but the two are
complementary. **Recommend** folding a small "hide/disable the webmail
self-service 2FA entry (point users to admin-provisioned)" task, OR tracking it
as an immediate follow-up, so the reported UX dead-end is actually removed.
Surface this to the operator when the feature ships.

### Invariants (check after every step)
- Existing mailboxes (`otpauth_url IS NULL`) authenticate with password only —
  byte-identical behavior to today. Prove with a login smoke.
- Mail delivery (SMTP/IMAP/JMAP) is unaffected by the schema + query change.
- The raw TOTP secret is returned exactly once (enroll response) and never
  logged, never persisted in a readable API response afterward.
- `go build ./...`, `go test ./...` for touched packages, `npx tsc -b`, and a
  golden regen if a CLI flag is added.

---

## Step 1 — Migration: `mailboxes.otpauth_url`

**Depends on:** nothing.

### Tasks
1. `000218_mailbox_otpauth.up.sql`:
   `ALTER TABLE mailboxes ADD COLUMN otpauth_url VARCHAR(512) NULL;`
   (512 covers `otpauth://totp/<email>?secret=<32>&issuer=<domain>&algorithm=SHA1&digits=6&period=30`.)
   `.down.sql`: `ALTER TABLE mailboxes DROP COLUMN otpauth_url;`
2. Add `OTPAuthURL *string` (gorm `column:otpauth_url`, `json:"-"` — NEVER
   serialize the secret-bearing URL to normal mailbox reads) to
   `models.Mailbox`. Add a derived `TwoFactorEnabled bool` computed field
   (`json:"two_factor_enabled"`, gorm `-`) the API sets from
   `OTPAuthURL != nil` so the UI shows on/off WITHOUT exposing the URL.

### Verify
- Migration applies + reverts against a MariaDB 11.x (CI matrix). `go build`.
- `mysql_query` shows the column nullable, default NULL, existing rows unaffected.
- **Re-confirm `000218` is still free at merge time** (concurrent work may claim
  it — the migration-numbering scar); renumber to the next free slot if taken.

### Exit
Column exists; model carries `OTPAuthURL` (never JSON-exposed) + a boolean flag.

---

## Step 2 — Stalwart SQL directory returns the otpauth secret (LOAD-BEARING; verify live first)

**Depends on:** Step 1. **This step gates the whole feature.**

### Tasks
1. **VERIFY FIRST on 192.168.100.86** (do NOT skip): hand-craft a directory
   `queryLogin` that UNIONs a second secret row for a test mailbox with a known
   otpauth URL, apply it via `stalwart-cli update Directory <id>`, then prove:
   an IMAP/JMAP login for that mailbox now requires a TOTP code, accepts a code
   from the known secret, and rejects password-only; a mailbox WITHOUT an
   otpauth row still logs in with password only. Capture the exact working query.
2. Update `install/stalwart/apply-plan.json.tmpl`:
   - `queryLogin` — **use a SINGLE `?` bind** (Stalwart passes the login value
     once; a two-`?` UNION would misbind). Wrap the UNION in a subquery filtered
     by one outer `?`, password_hash FIRST:
     ```sql
     SELECT email_cached, secret FROM (
       SELECT email_cached, password_hash AS secret, 0 AS ord FROM mailboxes WHERE is_disabled = 0
       UNION ALL
       SELECT email_cached, otpauth_url AS secret, 1 AS ord FROM mailboxes WHERE is_disabled = 0 AND otpauth_url IS NOT NULL
     ) x WHERE x.email_cached = ? ORDER BY ord
     ```
     (final shape is whatever Step 2.1 proves works — secret ORDER may matter to
     Stalwart; verify password-first.)
   - **Leave `queryRecipient` UNCHANGED.** It resolves delivery recipients, NOT
     auth secrets — adding otpauth there would return a bogus recipient. otpauth
     belongs ONLY in `queryLogin`.
3. Update the **ADR-0073 converger** in `install.sh` `_install_stalwart_apply_plan`:
   - It currently rebuilds `queryRecipient` + `queryEmailAliases`. Add
     `queryLogin` to the `--json` patch so existing hosts redeploy the new
     login query on `jabali update` (the directory is create-once; query edits
     only land via this converger). Use the verified query string verbatim.

### Verify
- `bash -n install.sh`; the rendered plan is valid JSON (`jq`).
- Live: on .86, apply the new plan/converger, then the Step 2.1 smoke passes
  through the REAL install path (not just a hand patch).

### Exit
A mailbox with `otpauth_url` set enforces TOTP; NULL keeps password-only —
proven on a live Stalwart via the install/converger path.

---

## Step 3 — panel-api: enroll / disable mailbox 2FA (reveal-once)

**Depends on:** Step 1 (column). Can develop in parallel with Step 2; both merge
before Step 5.

### Tasks
1. Repo (`mailbox_repository.go`): `SetOTPAuthURL(ctx, id, url string) error` and
   `ClearOTPAuthURL(ctx, id) error` (dedicated updaters — do NOT reuse a broad
   allowlist Update; see the domain.Update-allowlist scar). Load path sets
   `TwoFactorEnabled = OTPAuthURL != nil` in the API mapper.
2. TOTP secret: `crypto/rand` 20 bytes → `base32.StdEncoding.WithPadding(NoPadding)`
   (RFC 4648, uppercase, what authenticator apps expect). Build
   `otpauth://totp/<urlescaped label>?secret=<B32>&issuer=<issuer>&algorithm=SHA1&digits=6&period=30`.
   Label = the mailbox email; issuer = the panel brand or the mailbox domain
   (pick domain for per-account clarity). Put this in a small
   `internal/mailtotp` helper with a unit test (URL shape, base32 alphabet).
3. Endpoints (admin, under the existing mailbox admin group; RequireAdmin +
   audit like password reset):
   - `POST /admin/mailboxes/:id/2fa/enroll` → refuse if mailbox disabled or
     already has 2FA (or allow re-enroll = rotate; pick rotate-with-confirm →
     document). Generate secret+URL, `SetOTPAuthURL`, return
     `{ "secret": "<B32>", "otpauth_url": "<url>" }` **exactly once**. Audit
     `mailbox_2fa_enroll` with the mailbox id (NEVER the secret).
   - `DELETE /admin/mailboxes/:id/2fa` → `ClearOTPAuthURL`, return `{ok:true}`.
     Audit `mailbox_2fa_disable`.
4. Never log the secret/URL. The normal mailbox GET/LIST must NOT include
   `otpauth_url` (json:"-") — only the derived `two_factor_enabled` boolean.

### Verify
- `go build ./panel-api/...`; unit test the mailtotp helper; handler tests:
  enroll returns secret once + flips `two_factor_enabled`; disable clears it;
  enroll on a disabled mailbox refused; the secret never appears in a subsequent
  GET.

### Exit
Admin can enroll (reveal-once) + disable a mailbox's 2FA via the API.

---

## Step 4 — panel-ui: mailbox 2FA control

**Depends on:** Step 3 (endpoints + `two_factor_enabled`).

### Tasks
1. In the admin mailbox surface (`DomainMailboxesSection.tsx` /
   `EditMailboxModal.tsx`): a **2FA** row — a badge (`Tag` green "2FA on" /
   default "off") from `two_factor_enabled`, plus:
   - **Enroll** → calls the enroll endpoint, opens a modal showing the antd
     `<QRCode value={otpauth_url} />` + the base32 secret in a copyable field,
     with a clear "shown once — save it now" warning and a "scan in your
     authenticator" instruction. Closing invalidates the mailbox query so the
     badge flips on.
   - **Disable** → confirm modal → DELETE endpoint → badge flips off.
2. Reuse the reveal-once modal ergonomics from the existing password-reveal flow
   (copy button, no re-fetch of the secret).

### Verify
- `npx tsc -b`; a vitest that Enroll renders the QR + secret once and Disable
  calls the endpoint + flips the badge (mock apiClient).

### Exit
Admin enrolls/disables a mailbox's 2FA from the panel; QR + secret shown once.

---

## Step 5 — Live-VM validation (192.168.100.86)

**Depends on:** Steps 2 + 3 + 4 on a deployed build (or the install-path pieces
for Step 2).

### Tasks
On a mailbox with a real password:
1. Enroll 2FA via the panel → capture the secret.
2. IMAP/JMAP login with password ONLY → rejected (TOTP required).
3. Login with password + a current TOTP code (computed from the secret) →
   accepted.
4. Logout, login again with password + code → still required + accepted
   (**persists** — the original bug).
5. Disable 2FA → login reverts to password-only.
6. A DIFFERENT mailbox with no 2FA → password-only unchanged throughout
   (no collateral).
7. Mail delivery (send/receive) unaffected.

### Exit
GH #316's TOTP half is closed: webmail/IMAP TOTP now persists because it's
admin-provisioned into the read-only directory.

---

## Rollback
Each step reverts independently. Step 1 down-migration drops the column. Step 2
is the risky one: keep the previous `queryLogin` string in the commit so the
converger can be reverted; a bad query breaks ALL mailbox auth, so Step 2.1's
live verification is the gate. Step 3/4 are additive (endpoints + UI); reverting
returns to password-only mailboxes with no data loss (drop the column last).

## Sequencing
- Step 1 → Step 2 (verify-live-first) is the critical path; Step 3 can proceed
  off Step 1 in parallel. Step 4 after Step 3. Step 5 after 2+3+4.
- Ship as ~4 PRs (migration+model; directory+converger; api; ui) or fold
  migration+api+ui if small, but keep the Stalwart directory change isolated so
  its live-verification gate is auditable.
