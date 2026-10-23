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
that window finds no `.minisig` for the release yet and does not update. It
stays on its current build without downloading the tarball, and retries on the
next run, which is the safe failure.

**Promoting `stable`: sign first, then move the tag.** A stable box moves to the
promoted commit only once that commit's release is signed. Moving the tag first
is safe, since the fleet waits. But every box then logs "no signed release" on
each run until the signature lands.

**The development channel follows signed releases only.** Every push to main
publishes a release, but a box moves only to a commit whose release is signed.
On a key-carrying build, a development-channel box therefore stays put until
the newest main release is signed. *Superseded on 2026-10-23: the development
channel streams main unsigned (see the amendment below).*

## Amendment (2026-10-20): the checkout is verified too

As first built, the signature covered only the tarball. But `jabali update`
resets the git checkout to the channel's commit and then runs `install.sh`
functions, unit files and templates from it as root, before any binary is
installed. Whoever could push to the repository could change `install.sh`, and
every box would run it on its next update, signature or not.

**On a key-carrying build, the commit is now verified before it becomes the
working tree** (`verifyResetTarget`, `panel-api/cmd/server/update_release_staged.go`):

1. After `git fetch`, the update resolves the commit it would reset to.
2. It fetches that commit's release **by commit**, checks the sha256 and the
   signature, and extracts it. The release's `MANIFEST` must name exactly that
   commit (`full_sha`). The signature covers the MANIFEST, and the commit ID
   covers every tracked file, so one signature vouches for the whole checkout.
3. Only then does it run `git reset --hard`. The release step installs that same
   verified tarball; it is not downloaded a second time.
4. If the commit has no published release, or the release has no `.minisig`
   yet, the checkout is not moved and the box stays on its current build.
   The same happens on every channel when the commit does not build on the
   one the box runs (it is not a descendant of HEAD). A signature says a build
   is genuine, not that it is current, so an old signed release must not move
   a box back onto code whose holes have since been fixed. That covers an
   ancestor of HEAD and a commit on a line that split off before it.
5. A signature that fails, a key mismatch, a checksum mismatch, or a signed
   release whose MANIFEST names another commit stops the update. The last case
   is an old signed release served under a new name, or a publishing mistake.

The "latest" download shortcut is not used on a key-carrying build. It names
whatever main published last. On a stable box that is a different build,
usually unsigned, so its missing `.minisig` would have failed every stable
update before the MANIFEST check could redirect to the by-commit release.

`--from-source` still verifies the commit first, then builds it.

## Amendment (2026-10-23): the development channel streams main

GH #2240: with every main release waiting for a signature, development boxes
stopped at 35d7d468d, the release that embedded the key, and stayed there. Main
publishes several releases a day. Signing each one is not what the development
channel is for.

Decided by the maintainer: three lanes.

- **Stable**: monthly. The release is signed before the `stable` tag moves.
  Unchanged.
- **Development**: every main release, installed as CI publishes it, on its
  sha256 alone. `jabali update` on the development channel skips the commit
  check and the tarball signature (`releaseSignatureRequiredOn`,
  `panel-api/cmd/server/update_release_signature.go`), and uses the "latest"
  download shortcut again. Every other channel value, and an unreadable
  channel, keeps the signature.
- **Critical**: a security fix between stable releases goes out as a signed
  hotfix release with an advisory in the signed security feed. Every box,
  on either channel, applies it within the hour. Unchanged.

The trade-off is accepted: a development box trusts GitHub and CI again, as it
did before this ADR. Development is for test hosts, and the Updates page says
so. Stable boxes, and what the security feed makes every box do, still require
the offline key.

Boxes on a build from 35d7d468d up to this change require the signature on
every channel. Their `jabali update` checks the target with the old rule, so a
development box on one of them moves only to a signed release. Signing the
first release that carries this change moves them once, and from there they
stream main.

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
