# Security advisory: make every server act on a fix within the hour

Every Jabali server checks the advisory feed, `security/advisories.json` on
main, once an hour (`jabali-security-feed.timer`, running
`jabali security feed-check`). Without an advisory, a fix waits for the
server's daily runs:

- vendor packages are installed at the time set on the Updates page;
- a panel update runs once a night, and only where panel self-update is on.

For a new advisory, each server does what the advisory asks, at most once,
unless its admin turned **Security advisories** off on the Updates page (it is
on by default, also on servers installed before the setting existed):

- **`"apt": true`**: refresh the package lists and run unattended-upgrades now,
  if OS security auto-updates are on (the default).
- **`"jabali"`**: if the server's build already contains one of the `fixed_in`
  commits, nothing. Otherwise it starts the panel update now, even where panel
  self-update is off.
  - Some servers on the stable channel cannot get the fix that way, because
    `jabali update` keeps them where they are: one that runs a build *ahead*
    of stable, and one whose database is newer than the stable release (a
    main build, next to a hotfix that main has not merged back yet). There
    the advisory's `converger` can apply the fix's server-side part: one
    install.sh function from a newer main commit.
  - That is the only action that runs code the server is not on. Like every
    action, it runs only from a feed signed with the release key (ADR-0162).
    A build older than the key gives the command to run by hand instead.
- **Always**: alert the admins (notification "Security advisory", on by
  default), saying what was done and what still needs them.

## Symptom

A security fix must be applied across servers now, not at their next daily
run: for example, a vendor package release that closes a remote code
execution, or a hotfix release that was just promoted.

## Diagnosis

Decide what the advisory asks for.

- **A vendor package** (PHP from Sury, OpenSSL, nginx, CrowdSec, Node): set
  `"apt": true`. Check that the fixed version is already in the vendor's
  repository; servers install whatever is there when they run.
- **Jabali's own code**: ship the fix first, then publish the advisory.
  1. Merge the fix to main.
  2. Ship it as a hotfix release and promote it (`docs/runbooks/hotfix-release.md`).
  3. Then publish the advisory with `jabali.fixed_in`.

  An advisory published before the promotion starts updates that have
  nothing new to install. Each server starts the update only once per
  advisory.

## Fix

1. **Add the advisory** to `security/advisories.json` in a PR. For example:

   ```json
   {
     "schema": 1,
     "advisories": [
       {
         "id": "JSA-2026-001",
         "published": "2026-10-22",
         "severity": "critical",
         "title": "PHP security release",
         "summary": "The October 21 PHP releases fix two remote code execution bugs.",
         "url": "https://github.com/shukiv/jabali-panel/discussions/2212",
         "apt": true,
         "jabali": {
           "fixed_in": ["<main commit, 40 hex>", "<hotfix commit, 40 hex>"],
           "converger": "ensure_security_updates",
           "converger_commit": "<main commit whose install.sh has the converger>"
         }
       }
     ]
   }
   ```

   The fields:
   - `id`: the next number, `JSA-<year>-<NNN>`.
   - `severity`: `critical` or `high`.
   - `title`: up to 120 characters; `summary`: up to 600, on one line.
   - `url`: a page in this repository, or leave it out.
   - Set `apt`, `jabali`, or both.
   - `fixed_in`: every commit that contains the fix, at least the main one
     and the hotfix one.
   - `converger` is optional. Name one only if the function applies the fix on
     its own on a server ahead of stable, and `converger_commit` is on main.

   Keep the wording plain for a fix that is still private (no exploit
   details): the feed is public.

2. **Let CI check it.** `TestSecurityFeedFileInRepoParses` fails the PR if the
   feed does not parse. A server rejects a feed it cannot parse completely,
   so a bad entry would silently stop every advisory, not only the new one.

3. **Sign the feed** on the machine that holds the release key. Commit the
   `.minisig` in the same PR, after the last edit to the feed:

   ```
   minisign -S -s ~/.minisign/jabali-release.key -m security/advisories.json -t "jabali security feed"
   ```

   Servers ignore a feed without a valid signature, even an empty one, and
   then act on no advisory at all. `TestSecurityFeedFileInRepoIsSigned` fails
   the PR when the signature does not match the feed.

4. **Merge it.** Servers act within about 75 minutes: the hourly timer, its
   15-minute spread, and a few minutes of GitHub's raw-file cache.

### Rollback

Each server acts on an advisory once and records it, so editing or removing
an advisory does not undo what servers already did. To correct one, publish a
new advisory.

## Verification

On a server, run the check now and read what it did:

```
systemctl start jabali-security-feed.service
journalctl -u jabali-security-feed --since -10min
cat /var/lib/jabali-security-feed/state.json
```

The journal names each advisory and the actions taken. The state file records
them per advisory: `apt_done`, `update_done`, `converger_done`, `fixed`. The
admins get a "Security advisory" notification. For the apt action:

```
journalctl -u jabali-security-feed-apt
```
