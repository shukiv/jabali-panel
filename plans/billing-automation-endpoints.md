# Billing Automation Endpoints (JAB-190)

Panel-side milestone backing the `jabali-integration` billing modules
(WHMCS / Blesta / WiseCP). Adds the account-lifecycle write endpoints and
billing read surfaces to the M44/JAB-140 Automation API. Contract source:
`jabali-integration/docs/API-CONTRACT.md` §3. ADR: `docs/adr/0164`.

Everything rides the JAB-140 spine: HMAC → `requireWriteScope` → per-token
write rate limit → `bindSigned` (validate the SIGNED bytes) → SAME
userops/repo/agent path the GUI uses → `auditWrite` (success AND failure)
→ `notifyWrite` for high-impact actions.

## Scope decisions

- **Login-token SSO: shipped as ADR-0165** (Kratos admin recovery links — see that ADR; the rest of this bullet is the original deferral analysis kept for history). Originally: No panel one-time-login mechanism
  exists (Kratos is sole auth; impersonation is cookie-session; M22
  magic-link is app-level). Minting a Kratos session from a one-time URL
  is a standalone auth milestone. Reserved: capability `users.login_token`,
  endpoint `POST /automation/users/:id/login-token {client_ip, ttl_s}` —
  the minting caller is the billing server but the REDEEMER is the end
  user's browser, so the mint request must carry the end-user IP if the
  token is IP-bound. Open design question: session minting mechanics
  against Kratos (investigate `session` admin API; do not assume).
- **Admin targets are refused outright** on delete/password/package (and
  create can never produce one: `is_admin` is not exposed and forced
  false). Blanket refusal beats last-admin counting for a headless key.
- **Create idempotency = by target state, keyed on username.** Post-M54
  the username is the panel's only unique identity; email is deliberately
  NOT unique (one client, many accounts — live-verified on mx). A retry
  whose (supplied or email-derived) username hits an existing user with
  the same email returns `200 {ok:true, status:"exists", user_id}`; a
  username collision with a different email → `409 conflict`; same email
  with a new username is a legitimate second account (201). Rationale:
  billing panels retry; the replay nonce only covers identical signatures
  inside the skew window.
- **`bindSigned` is plain json.Unmarshal — gin `binding` tags are dead
  here.** Handlers validate explicitly: password min-10 (mirror the REST
  contract), email via mail.ParseAddress when non-empty, username via
  userops' own regex (delegated).
- **Bulk usage never touches the agent.** `GET /usage` serves the stored
  disk-usage snapshots (`computed_at` may be null/stale — honest) + one
  batched month-to-date `BWDaily.SumByDomainIDs` + package quotas. One
  query per table (M13.1 no-N+1 rule). Per-user `GET /users/:id/usage`
  reads the same stores.
- **`dry_run` delete = read-only enumeration** (counts of domains,
  databases, docker apps that the cascade would remove), never a
  rehearsal.
- **New scope `read:packages`** in `AllowedAutomationScopes` (covered by
  `read:*` via the prefix wildcard). Everything else reuses
  `write:users` / `delete:users` / `read:users` — all already allowlisted.

## Steps

1. **Blueprint + ADR-0164** (this file; no schema changes — no migration).
2. **Extraction commit (zero behavior change).** Move GUI-handler logic
   into `userops` so both callers share one write path:
   - `userops.RotatePassword(ctx, d, user, password)` — hash → Kratos
     SetPassword → agent `user.password` (non-admin w/ username) → DB.
     Order is load-bearing (Kratos failure leaves old password working).
     Typed sentinels preserve the REST handler's per-failure JSON.
   - `userops.SetPackage(ctx, d, user, packageID *string)` — validate →
     assign → `Reconciler.ReconcileUserLimits` kick on actual change.
   - `userops.DeleteCascade(ctx, d, target, actor)` — the whole delete
     cascade (Stalwart purge → domains → db.drop/db_user.drop →
     mysqladmin shadow → docker teardown → Kratos identity → Redis ACLs
     → users row → OS teardown). `ErrDockerTeardownFailed` carries the
     failed slugs (handler → 409). Redis ACL revoke passed as a callback
     dep so userops does not import the redis client.
   - Rewire `users.go` update/delete + verify the three delete-cascade
     regression tests stay green unmodified.
3. **Write endpoints.** `POST /automation/users`,
   `DELETE /automation/users/:id` (confirm:true + dry_run),
   `POST /automation/users/:id/password`,
   `PUT /automation/users/:id/package`. Create responds
   `201 {ok, status:"created", user_id, message}` (write envelope +
   user_id). notifyWrite on create + delete.
4. **Read endpoints.** `GET /automation/users/:id/usage`,
   `GET /automation/usage` (bulk; snapshot ListAll added to the
   snapshot repo w/ sqlmock test), `GET /automation/packages`
   (thin, secrets-free rows), and `GET /automation/users` grows
   `?email=&username=` filters + `suspended` in the row shape
   (backward-compatible additions).
5. **Capabilities + scopes.** Add `users.create/delete/password/package`,
   `usage.read`, `packages.read` entries; add `read:packages` to
   `AllowedAutomationScopes`. Update the JAB-140 runbook table.
6. **Tests.** Handler tests per automation_write_test.go patterns
   (scope denial, admin-target refusal, confirm/dry-run, idempotent
   create, envelope shapes), userops extraction tests, `go test -race
   ./panel-api/...`.
7. **Docs + wrap.** BLUEPRINT.md milestone table, memory file, rebase on
   origin/main, detect_changes, final report. NEVER push (dispatcher
   pushes).

## Verification

- `go test -race ./panel-api/...` green; cascade tests green UNMODIFIED.
- Live (post-merge, operator): mint `write:users delete:users read:*`
  token on a test box; jabali-integration smoke script exercises
  create → password → package → usage → disable/enable → delete dry-run
  → delete; confirm audit rows + admin bell notifications.
