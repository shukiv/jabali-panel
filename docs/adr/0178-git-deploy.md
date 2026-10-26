# ADR-0178: Deploying a domain from a Git repository

**Status:** Accepted (2026-10-26). Part 1, the repository settings, is built; deployments, rollback and the webhook follow in later changes (see Next parts).
**Driven by:** GH #2277 (a reporter asked to deploy PHP applications from GitHub, GitLab, Bitbucket or any Git host, private repositories included, with branch selection, manual and webhook deployments, deploy commands, releases with rollback, environment files kept out of the repository, and a deployment history, all under the hosting account; the maintainer approved building it on 2026-10-25).
**Related:** ADR-0176 (Composer runs), ADR-0067 (SSH shell sandbox), GH #1458 (cron in the sandbox), GH #2032 (a tenant's sealed Cloudflare token), GH #282 (no-package policy), GH #658 (the sandbox never jails root).

## Context

A tenant who deploys from Git today connects over SSH, pulls, runs Composer and
switches files by hand. A tenant without SSH can't do it at all. A deployment
runs the project's own code (composer.json scripts, migrations, cache
commands), so it needs the same confinement Composer runs got in ADR-0176.

A private repository needs a credential on the server. The token must stay out
of every place another local user can read: a transient unit's `ExecStart` and
environment are readable by any local user through `systemctl show`, and a
URL with a token in it ends up in git's config, its errors and the journal.

The server makes the connection, not the user, so a repository URL is also a
way to make the server connect to an address of the user's choosing.

## Decision

1. **A package entitlement, off by default.** `hosting_packages.git_deploy_enabled`
   (migration 338), default 0 for every package, existing ones included, set
   with `jabali package create/edit --git-deploy`. A user with no package can't
   use Git deploy (GH #282: a privileged feature). Deployments run the user's
   own commands in their SSH sandbox, so grant it as you would SSH access. An
   API token limited to scopes uses the `ssh` scope: `read:ssh` to read the
   settings and branches, `write:ssh` to change them.
2. **One repository per domain.** `domain_git_repos` holds the repository URL,
   the branch, a username and the token. The URL must be `https://`, with a
   host, an optional port and a path, and no username, token, query or
   fragment; it may not contain `%` or `$` (systemd expands both in a unit's
   `ExecStart`). Saving reads the repository's branches first (`git ls-remote`)
   and stores nothing when that fails, so stored settings always worked once.
   An empty branch means the repository's default branch, which is stored.
3. **The token is sealed and bound to its host.** The panel seals it with the
   sso key, as it does a tenant's Cloudflare token (GH #2032), and never
   returns it: the API says only `has_token`. Saving another host (or port)
   without sending the token again is refused (`token_required_for_new_host`),
   so whoever can edit the settings, an admin acting for the user or a
   `write:ssh` token, can't send the stored token to a host of their choice.
   Removing the settings needs no entitlement, so a user whose package lost
   Git deploy can still remove the token. The settings belong to the owner
   who saved them: changing the domain's owner deletes them in the same
   transaction, and a row saved by anyone but the current owner is treated
   as absent.
4. **git runs as the user, in their SSH sandbox.** The agent runs git through
   `systemd-run` in the user's slice, inside `jabali-ssh-shell --exec` (as
   cron and Composer runs do), and only for a hosting user (uid 1000–59999),
   never root or a system account (GH #658). git starts with no system or user
   config (`GIT_CONFIG_NOSYSTEM`, `GIT_CONFIG_GLOBAL=/dev/null`; a user's
   `~/.gitconfig` could add a credential helper or rewrite the URL), never
   prompts, and may use https only (`GIT_ALLOW_PROTOCOL`, which covers
   submodules too). It runs from `/` (`git -C /`): the sandbox starts in the
   home directory, and a repository there could bring its own config. A
   branch listing is capped at 20 seconds.
5. **The token reaches git through a file, never argv or the environment.**
   For each call the agent writes a git credential-store file,
   `~/.jabali-git/<random>/credentials`, 0600 and owned by the user, and
   removes it when the call ends. It creates the directory and file through
   directory descriptors and follows no link. The empty `credential.helper`
   comes first, so no other helper is asked. With no username, the agent
   sends `x-access-token`, the name GitHub App installation tokens use.
   Other tokens need the name their host expects, which the user enters:
   their GitHub username for a GitHub personal token, any name for a GitLab
   personal or project token, the token's own username for a GitLab deploy
   token, `x-token-auth` for a Bitbucket repository token.
6. **The connection goes only where it was checked.** The panel refuses a host
   that resolves to a loopback, link-local, private or CGNAT address
   (`ssrfguard`). The agent resolves the host again, once, refuses those
   addresses and a few more (0.0.0.0/8, 198.18.0.0/15, 240.0.0.0/4, IPv6
   site-local, and the IPv4 address inside a NAT64 or 6to4 address), and pins
   git to the address it checked (`http.curloptResolve`) with redirects and
   proxies off (`http.followRedirects=false`, `http.proxy=`). git gets the URL
   rebuilt from the checked host, so the pin is the entry git looks up. A name
   that changes its answer after the check, or a redirect, can't move the
   connection. Errors show git's own `fatal:` line, never what the remote
   server printed. Both settings are newer than some git releases, and an older
   git ignores them without a word, so the agent refuses a git older than
   2.37. Debian 13 ships 2.47 and Ubuntu 24.04 2.43.

## Next parts

These follow the design above; each lands with its own change.

- **Deployments.** A deployment is a transient unit like a Composer run
  (ADR-0176), with a run row, a log and a result. It clones the branch into a
  new release directory under the domain, links in files kept outside the
  repository (`.env`, storage directories), runs Composer when the project has
  a `composer.lock`, runs the user's deploy commands (checked as cron commands
  are), and only then switches a relative `current` link to the new release.
  A failure before the switch leaves the live release alone. The newest
  releases are kept, so a rollback switches `current` back. The domain's
  document root points into `current` once a first release exists.
- **Webhook.** A per-domain secret checked against the Git host's signature,
  JSON bodies only, a size limit, its own rate limit, and delivery IDs
  remembered so a retried delivery deploys once.
- **The Git tab** in the domain's page.

## Consequences

- A user can make the server connect to any public https host. That is what a
  website's PHP can already do; private, loopback and link-local addresses are
  refused.
- A Git server on a private network can't be used.
- A repository that has moved (an http redirect) needs its new URL.
- The repository URL, which holds no credentials, is in the unit's
  `ExecStart` while git runs.
- A backup doesn't carry the repository settings or the token. After a
  restore, enter them again.
