# ADR-0043: M6 DKIM — Ed25519 primary, RSA-2048 escape hatch, 365-day + 72h coexistence

**Date**: 2026-04-21
**Status**: accepted
**Deciders**: shuki + Claude
**Related**: ADR-0002 (DB is truth), ADR-0041 (Stalwart auto-DNS disabled), ADR-0042 (`domains.dkim_public_key` column), plan §1 decision 4

## Context

Every outbound message from a panel-hosted domain needs a DKIM signature for modern deliverability. The blueprint input asked for DKIM keys owned by the panel at `/etc/jabali-panel/dkim/<domain>.key` with the public key published via PowerDNS (panel's M4 code path) — *not* via Stalwart's v0.16.0 native DNS auto-publish feature, because ADR-0002 pins one DNS writer (PowerDNS).

Three sub-decisions follow from that:

1. **Key algorithm** — Ed25519 (operator choice 2026-04-21) versus RSA-2048 default.
2. **On-disk format** — what exactly lives in `/etc/jabali-panel/dkim/<domain>.key`.
3. **Rotation cadence + selector naming** — how often keys roll and how receiving servers find both keys during the rollover window.

### Ed25519 vs RSA-2048

| Property | Ed25519 | RSA-2048 |
|---|---|---|
| Private key on disk | 32 bytes (raw seed) | ~1700 bytes (PEM-encoded) |
| Public key in DNS | 32 bytes → 44 chars base64 → fits a single DNS TXT string | ~270 bytes → 360+ chars → requires `"…" "…"` multi-string TXT split |
| Signing speed | ≈ 5× faster per message | slower, more CPU under burst |
| Standard | RFC 8463 (Sept 2018) | RFC 6376 (2011), universally supported |
| Receiver support | Gmail, M365, Fastmail, Zoho, modern Postfix w/ OpenDKIM ≥ 2.11 | Everyone ever |
| Long-tail receiver risk | Older corporate / gov / ISP servers may silently skip the signature (treat message as unsigned) | None meaningful |

With `DMARC p=quarantine` default (plan §1) and SPF still passing, a skipped Ed25519 signature at a long-tail receiver lands the message in spam/junk, not a full reject. Not catastrophic, but real.

### On-disk format: raw seed base64, not PKCS#8 PEM

The plan specified **raw 32-byte seed, base64-encoded**. Not PKCS#8-wrapped. Stalwart's DKIM signer expects the key in the seed format; PKCS#8 PEM-wrapped Ed25519 keys require the signer to parse the DER wrapper and extract the inner `CurvePrivateKey` OCTET STRING. Stalwart's config surface exposes `key-type = "Ed25519"` + `key = "<base64-seed>"` — that's the contract.

(An earlier drift in the step's first-pass implementation wrapped the key in PKCS#8 PEM. That would have silently broken Stalwart's key loader. Recorded here so the fix doesn't regress.)

### Selector naming

`jabali-YYYY-MM` (e.g. `jabali-2026-04._domainkey.example.com`). Rationale: selector names are rotation history — an operator looking at `dig TXT _domainkey.example.com` output should be able to tell at a glance which key is current and how old it is. `jabali-new` / `jabali-old` doesn't encode that.

## Decision

### 1. Primary key type = Ed25519 (RFC 8463)

`internal/dkim/GenerateEd25519()` returns:

- `privateRawBase64 []byte` — standard-base64 encoding of the 32-byte Ed25519 seed (output of `crypto/ed25519.NewKeyFromSeed(seed)` is the 64-byte expanded private; we store the 32-byte seed since that's what re-derives the same key).
- `publicDKIMTxt []byte` — ASCII `v=DKIM1; k=ed25519; p=<base64-32B-pub>` with no trailing newline and no surrounding quotes. One DNS string, no split.

On-disk: `/etc/jabali-panel/dkim/<domain>.key`, 0600 `jabali:jabali`, atomic `os.CreateTemp` + `chmod 0600` + `os.Rename`. Content is the base64 seed followed by a single `\n`. No PEM wrapping. No private-key serialisation format version.

Public key copy: `domains.dkim_public_key TEXT` (set by the reconciler when `domain.email_enable` returns) — the UI's "Copy DKIM public key" button reads from here without a filesystem round-trip, and the reconciler can re-inject the DNS record on restore-from-backup without regenerating.

### 2. Rotation cadence — 365 days, 72-hour coexistence

Per domain:

- Day 0 (email enable): generate `<domain>.key` (selector `jabali-<YYYY-MM>` at enable time). DNS TXT at `jabali-<YYYY-MM>._domainkey.<domain>`.
- Day 365: reconciler (or CLI trigger) generates `<domain>.key.next`, assigns selector `jabali-<YYYY-MM>` for the new month. Both DNS records present. Stalwart signs with the *new* key from T+0 of rotation day.
- Day 365 + 72h: reconciler removes the old DNS TXT + renames `<domain>.key.next → <domain>.key`, deletes the old keyfile. Any message signed with the old key during the last 72h verifies (old DNS record still present for the 72h window).

Rotation itself is **CLI-triggered in v1** (`jabali domain email-dkim-rotate <domain>`), not automatic. M6.1 adds a reconciler-driven 30-day-before-365 warning + auto-rotate toggle. The CLI path is simpler to reason about and to abort if something surprises us in the first 6 months of production traffic.

### 3. RSA-2048 escape hatch (not v1 default; pre-committed design)

If Ed25519 long-tail delivery failures surface in operation, operators add a second selector `jabali-rsa-<YYYY-MM>` alongside the Ed25519 primary:

- `internal/dkim/GenerateRSA2048()` (stub in v1; implemented in M6.1 or ad-hoc when needed) mirrors the Ed25519 shape but writes `<domain>.rsa.key` + populates `domains.dkim_rsa_selector` + `domains.dkim_rsa_public_key` (columns added only when the feature is implemented — not pre-created, kept off the v1 schema to avoid unused columns).
- Stalwart's config learns a second `[signature."rsa"]` block with `key-type = "RSA"`.
- Both selectors publish to DNS; Stalwart signs with both; receivers pick whichever they understand.

v1 ships the runbook recipe (plan Step 9) for doing this by hand against a specific domain — panel support arrives in a later milestone once we know whether the long-tail risk actually materialises.

### 4. Stalwart's native DKIM + auto-DNS stay disabled

Stalwart v0.16.0 can generate its own keys and publish DNS records via its DNS-management layer. We disable both in `/etc/stalwart/config.toml` (plan Step 2):

- Stalwart's DKIM key source is `file:/etc/jabali-panel/dkim/<domain>.key` per domain.
- Stalwart's `config.dns.providers = []` explicitly empty. Any `MX`/`TXT` write from Stalwart is a configuration regression and the reconciler will rewrite it on next tick.

## Consequences

### Positive

- Smallest DNS payload per key — no `s=` multi-string wrangling, no 512-byte UDP DNS response truncation risk.
- Single DNS writer (PowerDNS via M4) — backup, audit, and disaster-recovery stories are one thing, not two.
- Fast signing reduces SMTP submission latency (Ed25519 is ≈ 5× faster than RSA-2048 per signature).
- On-disk raw-seed format is the simplest possible thing — one `base64 -d` + one `ed25519.NewKeyFromSeed` round-trips the key. No OpenSSL, no BoringSSL, no x509 library in the hot path.
- 72-hour coexistence window is longer than any reasonable DNS negative-cache TTL (we publish all records at 300s TTL; 72h = 864× the TTL).

### Negative

- Long-tail corporate / gov receivers may silently skip Ed25519 DKIM. Mitigation is the RSA-2048 second-selector recipe. Until M6.1 ships automation, this is a runbook intervention.
- 365-day cadence is aggressive by historical DKIM standards (many operators rotate never). Picked because the key is small and rotation cost is low; if rotation turns out to be surprisingly painful operationally, we re-evaluate to 2 or 3 years.
- Rotation is manual in v1. Automation risk is low (the reconciler already runs DKIM-related code for M4 DNS injection), but committing to reconciler-driven rotation without first watching at least one manual cycle in production is premature.

### Rejected alternatives

- **RSA-2048 as primary** (blueprint's original default). Rejected per operator's explicit choice 2026-04-21 and per the DNS/performance trade-off above. Kept available as runbook escape hatch.
- **PKCS#8 PEM on-disk key**. Breaks Stalwart's key loader contract. Logged here because the first sub-agent pass drifted into this shape.
- **Auto-rotation in v1**. Deferred. Reconciler infrastructure is in place (ADR-0042), adding a rotation tick is trivial; value of watching one manual cycle first is higher than the operator burden of one CLI command.
- **Dual Ed25519 + RSA from day one**. Rejected as unnecessary complexity. If the escape hatch turns out to be the common case, we flip the default in M6.1 and the operator cost is one `ALTER TABLE` + a reconciler pass.

## Related

- ADR-0002 — DB is truth (which means DNS source is PowerDNS, not Stalwart).
- ADR-0041 — Stalwart auto-DNS explicitly disabled in config.
- ADR-0042 — `domains.dkim_public_key` column used by the reconciler + UI.
- Plan: `plans/m6-email-stalwart.md` §1 decision 4, Step 1 `internal/dkim/`, Step 3 `domain.email_enable` command, Step 5 DNS record template, Step 9 runbook escape hatch.
- RFC 8463 — Ed25519 DKIM (2018).
- RFC 6376 — Generic DKIM (2011).

## Amendment 2026-11-10 — what rotation does on Stalwart 1.0

Parts of this ADR no longer describe the system:

- **Stalwart does not read the key files.** §4's `file:/etc/jabali-panel/dkim/<domain>.key` key source is from the 0.16 config. Stalwart 1.0 signs with the private keys stored in its registry (`x:DkimSignature`, PKCS#8 PEM); the files are the panel's copy, from which the published TXT is derived. A rotation that wrote only the file therefore published a key nothing signed with, until GH #2475's follow-up changed it.
- **Rotation keeps the selectors.** The `jabali-YYYY-MM` selectors and the 72-hour coexistence of §2 were never built: the selectors are fixed (`jabali`, and `jabalirsa` for the RSA key). A rotation sets the new key on the domain's Stalwart signatures (DKIM1, and DKIM2 for the Ed25519 key), then replaces the file and republishes the same TXT names. Receivers that cached the old record fail the new signatures until it expires (the server's default DNS TTL, 300 s unless changed), and a message signed before the rotation and still queued for retry fails at its receiver. `<domain>.key.old` and `<domain>.rsa.key.old` keep the previous private keys as a record; they are not a rollback.
- **RSA is signed alongside Ed25519 by default** (GH #2479/#2475, migration 349 `domains.dkim_rsa_public_key`), not as the escape hatch of §3, and the RSA key is rotated with the Ed25519 one. If Stalwart refuses the RSA key, the Ed25519 rotation still completes and the RSA key, its file and its published record stay as they were.

Selector-per-rotation with a coexistence window remains the better scheme for a scheduled rotation; it is not built.
