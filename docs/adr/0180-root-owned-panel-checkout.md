# ADR-0180: The panel checkout is root-owned

**Status:** Proposed (2026-11-05). Accepted when the branch merges.
**Related:** ADR-0067 / ADR-0127 (SSH sandbox), ADR-0175 (Node.js apps), GH #760 (low-RAM build flags).

## Context

`jabali update` and install.sh run as root. They do three things with the
checkout at `/opt/jabali-panel`:

- install.sh sources `install/scripts/socket-helpers.sh` and `install.sh`
  itself, and `jabali update` runs `source install.sh && provision_new_software`
  as root (`panel-api/cmd/server/update.go`, provision steps);
- root installs files from `install/` into `/etc` and `/usr/local/libexec`;
- root builds `jabali-agent`, `jabali-ssh-shell`, `jabali-mailhook`,
  `jabali-sendmail` and `jabali-webdav` from source and installs them.

## Decision

1. **Root owns the whole checkout.** Every path under `/opt/jabali-panel` is
   owned by root, `.git` and `panel-ui/` included. The service user does not
   write under it. This includes `panel-ui/embed.go`, which is Go code in the
   tree.
2. **Git runs as root with the repo's config neutralised.** Root's git gets
   `core.hooksPath=/dev/null`, `core.fsmonitor=false`, `core.pager=cat`, and
   `GIT_CONFIG_NOSYSTEM` / `GIT_CONFIG_GLOBAL=/dev/null`.
3. **Go and npm run as root,** with caches under `/var/cache/jabali-build`,
   outside the checkout. npm runs with `--ignore-scripts`, so package lifecycle
   hooks never run as root. `panel-ui` declares none, and its build was verified
   with `--ignore-scripts`.
4. **An old checkout is never reused.** If `.git` is not root-owned, the
   checkout is moved to `/opt/jabali-panel.quarantine-<UTC>` (kept for
   inspection) and re-cloned from the canonical URL. An existing non-root-owned
   `.git` is never used. The re-clone forces a full rebuild.
5. **The security converger archives the converger commit as root** from the
   checkout. It no longer runs `git archive` as `jabali`.
6. **`convergerManualCommand` and `liveSecurityFeedHost`** run git as root.

Implementation: `panel-api/cmd/server/update_repo_owner.go`
(`enforceRepoOwnership`, `gitRootCmd`), `update.go` (the sync block and the
root build step), `security_feed*.go`, and `install.sh` (`root_git`,
`root_repo_layout`, `clone_or_update_repo`, `build_backend`, the panel-ui build).

## Consequences

- Go and npm builds run as root and write their caches to
  `/var/cache/jabali-build`.
- `git clone` as root needs no `chown` of `.git` any more; the update's
  self-heal `chown -R` steps are removed.
