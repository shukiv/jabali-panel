# ADR-0178: Deploying a domain from a Git repository

**Status:** Accepted (2026-10-26). Built: the repository settings (part 1), deployments, releases and rollback (part 2), the deploy-on-push webhook (part 3). The Git tab follows (see Next parts).
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
   that resolves to a loopback, link-local, private, CGNAT or other non-public
   address (`ssrfguard`): 0.0.0.0/8, 198.18.0.0/15, 240.0.0.0/4, IPv6
   site-local and IPv4-compatible space, the local-use NAT64 prefix, and a
   NAT64 or 6to4 address that carries one of those IPv4 addresses. The agent
   resolves the host again, once, refuses the same addresses, and pins
   git to the address it checked (`http.curloptResolve`) with redirects and
   proxies off (`http.followRedirects=false`, `http.proxy=`). git gets the URL
   rebuilt from the checked host, so the pin is the entry git looks up. A name
   that changes its answer after the check, or a redirect, can't move the
   connection. Errors show git's own `fatal:` line, never what the remote
   server printed. Both settings are newer than some git releases, and an older
   git ignores them without a word, so the agent refuses a git older than
   2.37. Debian 13 ships 2.47 and Ubuntu 24.04 2.43.

7. **A deployment is a transient unit, like a Composer run (ADR-0176).**
   `jabali-gitdeploy-<run id>.service` runs in the user's slice, inside
   `jabali-ssh-shell --exec`, as `jabali-agent git-deploy <settings>`: a
   oneshot with a 30-minute limit, 2 GiB of memory and 256 tasks. A domain
   has one run at a time (`domain_git_deployments`, migration 339). The agent
   keeps the unit's result until the panel has stored it, from the API or a
   reconciler pass. The settings travel in the unit's argv, written by root
   and holding no secret (the token is in the credential file), never in a
   file the user could change before the run reads it. The run checks every
   setting again when it decodes them: the URL, the pin, the branch, the
   paths and the commands.
8. **The steps, in order.** The run clones the branch one commit deep
   (`--depth=1 --single-branch --no-tags`), pinned as in 6, into
   `domains/<domain>/releases/<run id>`, and logs the commit. It then removes
   its credential file (`~/.jabali-git/deploy-<run id>/`) before any of the
   repository's code runs; releasing the unit removes it too, so a run lost to
   a restart doesn't leave it behind. It removes `.git` (submodules are not
   fetched), links the shared paths, runs `composer install` and the deploy
   commands, switches `domains/<domain>/current`, a relative link, to the new
   release with a rename, and removes releases beyond the newest five, never
   the live one. Any failure before the switch removes the new release: the
   site keeps serving the one it served.
9. **Composer needs the Composer entitlement too.** `composer install` runs
   when the project has a `composer.json` and a `composer.lock` and no
   `vendor/`, and only when the package also includes Composer runs. It runs
   with the Composer runs' PHP Defense rules (ADR-0176), which apply to it
   alone. A project that needs it on a package without Composer runs fails
   before going live. Git deploy alone never lifts the exec functions.
10. **Deploy commands are a closed list, like cron (GH #1458).** One per line,
    at most 20: `php <file> [args]`, `php<X.Y> <file> [args]` or `wp [args]`,
    with paths relative to the release. No PHP option may come before the
    file: `-n`, `-c` and `-d` would skip PHP Defense, and `-r` runs inline
    code. Shell metacharacters outside quotes are refused, control characters
    everywhere. Each command runs as its argv in the release directory, never
    through a shell, on the domain's PHP and its default PHP Defense rules.
    The panel checks them when they are saved, the run again before running
    them (`cronvalidate.ValidateDeployCommands`, shared by both).
11. **Shared paths stay out of the served directory when hidden.** Up to 20
    paths relative to the repository are kept across releases in
    `domains/<domain>/shared`. A path missing there is taken from the first
    release that has it; a path missing from both fails the run before going
    live. `.git`, the served directory or a directory holding it, and
    overlapping paths are refused. Tenant vhosts serve dotfiles, so a shared
    path inside the served directory may not have a hidden segment: `.env`
    must sit outside it, as Laravel's does next to `public`
    (`cronvalidate.ValidateDeployLayout`, shared by the panel and the run).
12. **The document root follows `current`.** Once a deployment has made its
    release live, the panel points the domain's document root at
    `/home/<user>/domains/<domain>/current[/<web_dir>]` and re-applies the
    vhost. It writes that one column in one statement that also checks the
    domain still belongs to the owner who started the run. Later deployments
    find it there and only switch the link. The path stays inside the
    domain's own tree, which is all a tenant's own document root change
    (`tenant_docroot_editable`) may do; the Git deploy entitlement is what
    allows this one. The agent's vhost setup resolves `current` beneath the
    home (`RESOLVE_BENEATH`, which allows a relative link that stays inside),
    and nginx hands PHP `$realpath_root`, the release's real path, so OPcache
    doesn't serve the previous release's files after a switch.
13. **Rollback switches `current` back, as its own run.** It runs as the user,
    like a deployment, and only switches the link: no git, no commands. Only
    the release of a deployment that succeeded may be made live again, never a
    directory a lost run left half-built. A run whose log says its release
    went live is recorded as a success even when it was cancelled or failed
    after that point, and the document root follows it: the site serves that
    release.
14. **A run's history belongs to the domain's owner.** The API lists and shows
    only the runs of the domain's current owner: a new owner doesn't see the
    previous owner's logs.
15. **A push deploys through a signed webhook.** The owner creates a webhook
    (`domain_git_webhooks`, removed with the Git settings) and gives its URL,
    `/api/v1/webhooks/git/<hook id>`, and secret to the Git host. The hook ID
    only finds the domain; a delivery is believed only by its signature over
    the raw body: `X-Hub-Signature-256` or `X-Hub-Signature` as `sha256=`
    (GitHub, Gitea, Forgejo, Bitbucket), GitLab's signing token (Standard
    Webhooks, timestamp within five minutes), or GitLab's older secret token
    in `X-Gitlab-Token` (the maintainer chose to accept both GitLab schemes on
    2026-10-27, since self-hosted GitLab before 19.0 has only the second).
    sha1 signatures are refused. The panel makes the secret and shows it once,
    or the user brings the host's own (GitLab makes its signing token). The
    secret is sealed with the sso key, as the token is.
    - A push to the stored branch deploys it as the domain's owner, through
      the same code as the Deploy button; the body is read for the branch
      name and nothing else. The agent fetches the branch as it is then.
    - A push that arrives during a deployment waits (`queued_at`), and the
      reconciler starts one more deployment once the run has ended; pushes
      meanwhile collapse into it (the maintainer's choice, 2026-10-27). Past
      six starts a minute a push waits the same way.
    - Each delivery is remembered for seven days by its ID and by a hash of
      its body, so a retry, or a captured delivery replayed under a new ID,
      deploys nothing. A start that failed on the panel's side forgets it, so
      the host's retry gets through.
    - GitLab turns a webhook off after four answers in a row outside 2xx.
      Every signed delivery the panel doesn't act on (another branch, a tag,
      a ping, a duplicate, a push that waits) is answered 2xx with what
      happened; 4xx is kept for what doesn't change by itself: a bad
      signature, an unknown webhook, a package without Git deploy.
    - The route has no session and sits outside the CSRF guard, like the
      automation API; a DR standby refuses it.

## Next parts

These follow the design above; each lands with its own change.

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
- Releases take disk space in the user's quota: up to five, plus the one
  being built.
- What the site writes inside a release, outside its shared paths, is gone
  after the next deployment. Uploads and caches that must persist need to be
  shared paths.
- A deploy command that PHP Defense stops is reported only when PHP writes it
  to the command's output; the `/dev/log` opt-in of ADR-0177 shows the rest.
- Removing the `current` link by hand leaves the site on a directory the
  vhost setup recreates; the next deployment refuses to replace a directory
  there until it is moved away.
- Submodules are not fetched.
- The Git host must reach the panel at the webhook's URL, with a TLS
  certificate it accepts. A panel behind a firewall that only admits its
  users, or with a self-signed certificate, gets no deliveries.
- GitHub sends form-encoded bodies unless the webhook is set to
  `application/json`; the panel refuses those (415).
- Anyone who learns a webhook's GitLab secret token can make the panel
  deploy the stored branch, at most six starts a minute; the signing token
  doesn't travel with each delivery.
- A delivery sent again within seven days deploys nothing, including the
  Git host's own "Redeliver"; the Deploy button does.
- A push that waits starts within one reconciler pass (a minute by default)
  of the run ending.
