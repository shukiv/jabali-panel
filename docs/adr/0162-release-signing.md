# ADR-0162 — Release artifact signing and the update trust root

- Status: Accepted
- Date: 2026-08-02
- Tracks: JAB-190

## Context

Both install paths verified the release tarball against a SHA-256 sidecar
fetched from **the same origin as the tarball itself**:

- `bootstrap.sh` — fresh install via `curl -fsSL https://get.jabali-panel.com | sudo bash`
- `panel-api/cmd/server/update_release.go` — every `jabali update`

Both hard-fail on mismatch, and the update path explicitly refuses to fall back
to a source build when the checksum fails. The control was implemented
correctly. It just answers a different question than the one that matters.

A same-origin checksum detects **corruption and truncation**. It cannot detect a
**malicious publisher**, because anyone who can publish a release can publish a
matching `.sha256` next to a trojaned tarball. The tarball ships prebuilt
binaries including `bin/jabali-agent`, which runs as root. A trojaned release is
therefore root on every host that installs or updates, with no further
exploitation required.

This was never a coding defect — it was an unstated trust assumption. The reason
to resolve it is that the entire update path rested on it and it was written
down nowhere.

## Decision

**Releases are signed with an Ed25519 key held offline, and `jabali update`
verifies that signature before installing anything.**

1. **Format: minisign.** The operator signs with the standard, audited minisign
   CLI, which keeps the private key password-protected. Only the *verifier*
   lives in this repository (`internal/releasesig`) — roughly a hundred lines
   that parse the signature, check the key ID, and check two Ed25519 signatures.

2. **The private key never enters CI.** Nothing in this repository and nothing
   in GitHub Actions can produce a valid signature. This is the property that
   makes the signature worth having: it survives a compromise of the Actions
   pipeline, the release storage, or a maintainer's GitHub account, none of
   which the checksum survived.

3. **Verification is mandatory, and its state is set at compile time.** The
   public key is a constant in the panel binary. While it is empty, verification
   is off and the tarball is accepted on its checksum alone — the pre-JAB-190
   posture. Once it is populated, verification is unconditional: a **missing**
   `.minisig` is a hard failure, not a skip.

   That distinction is the point. The issue warned that "a signature check that
   silently skips when the signature is absent provides no defense", and it is
   right — an attacker substituting the tarball also controls whether a
   signature is present, so *absent* must never mean *fine*. Compile-time is not
   reachable by an attacker; runtime absence is.

4. **Signatures are checked before extraction.** Extracting first would run
   `tar` over attacker-controlled bytes and write them to disk on the strength
   of a checksum alone.

## What this does not cover

**Fresh installs remain trust-on-first-use.** `curl | bash` on a bare Debian box
has no verifier: to check a signature you must first fetch minisign or cosign,
which itself needs trust. Fetching a verifier from an independent origin
(sigstore, Debian) does raise the bar — two separate compromises instead of one
— but it does not eliminate TOFU, it relocates it. Stating that plainly is
better than implying `bootstrap.sh` is verified when it is not.

The asymmetry is deliberate. The install happens once, with an operator present,
often on a host they just created. The **update** path runs unattended on every
box forever, and that is where the recurring exposure is. It is also the path
that can be fixed cleanly: `jabali update` runs an already-installed,
already-trusted binary, so the key can be embedded and the check done with the
standard library — no external tool, no bootstrap problem.

**A compromised release with no signature is refused, not silently accepted** —
but only on builds that have the key. A box still running an older binary
verifies nothing, so the fleet becomes protected as it updates, not at the
moment this lands.

## Release procedure

1. CI builds and publishes the release as it does today (tarball + `.sha256`).
2. On the machine holding the key: `tools/sign-release.sh release-<short-sha>`.
   It re-verifies the checksum locally before signing — signing a corrupted
   artifact is worse than not signing — signs every `.tar.gz` on the release,
   uploads each `.minisig`, and verifies its own output against the public key.
3. Both the per-commit asset and the fixed-name `jabali-release.tar.gz` copy get
   signed. `jabali update` fetches one or the other depending on how it resolved
   the release, so signing only one would leave part of the fleet unable to
   verify.

**There is a window between publish and signature upload.** A box updating in
that window fetches a tarball whose `.minisig` does not exist yet and refuses to
install — it stays on its current build and retries later, which is the safe
failure. Keep the window short; publishing as a draft and releasing after
signing removes it entirely and is the better long-term shape.

## Key ceremony

```
minisign -G -p jabali-release.pub -s jabali-release.key   # offline machine
```

- Private key: offline, password-protected, backed up separately from any
  machine that can publish a release.
- Public key: the base64 line from the `.pub` file goes into
  `releaseSigningPublicKey` in `panel-api/cmd/server/update_release_signature.go`.

**Rotation:** a box trusts exactly one key, so a rotation must ship in a binary
before the first release signed with the new key. Sequence: publish the new
public key in a release signed with the *old* key, let the fleet update onto it,
then switch signing. Rotating in the other order strands every box on
`ErrKeyMismatch` — which the error message calls out explicitly, since a
rotation that skipped a step and an attack look identical from the box.

## Alternatives considered

**Sigstore / cosign keyless.** No key custody and a public transparency log,
which is genuinely attractive. Rejected as the primary control because it signs
with the Actions OIDC identity — so a compromised workflow or token produces
valid signatures, and that is precisely the scenario driving this ADR. Worth
revisiting *in addition* for the transparency log.

**Key in a GitHub secret.** No release-time manual step and no publish/sign
window. Rejected for the same reason: it puts the key inside the blast radius it
is supposed to survive. Releases here are infrequent enough that the manual step
is cheap.

**Accept and document only.** A legitimate option, and the honest default for
most self-hosted panels. Rejected because the marginal cost here was small: the
verifier is stdlib-only, the signing tool is a shell script, and the update path
is high-value enough to justify both.
