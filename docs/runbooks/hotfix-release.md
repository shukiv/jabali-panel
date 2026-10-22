# Hotfix release: a fix on stable servers before the monthly promotion

`stable` moves about once a month. A hotfix moves it sooner, to the stable
build plus one or more fixes that are already on main. The hotfix gets a
prebuilt release like any main commit, so stable servers install it in the
usual time, without building it from source.

## Symptom

A security or data-loss fix is merged to main, and stable servers must not
wait for the next monthly promotion.

## Diagnosis

Decide where the fix lives before you start.

- **A vendor package** (PHP from Sury, nginx, OpenSSL, CrowdSec, Node, Docker
  CLI, containerd): no hotfix needed. unattended-upgrades installs it within a
  day, and `jabali-service-refresh` restarts the services that use it. To
  install it now on a server:

  ```
  unattended-upgrade -v
  journalctl -t jabali-service-refresh --since -10min
  ```

  The Docker daemon (`docker-ce`) is the exception: it is never upgraded
  unattended, because live-restore keeps containers running only across a
  patch release. Upgrade it by hand.

- **Jabali's own code or install.sh**: make a hotfix release (below).

Check what stable is now:

```
git fetch origin --force main '+refs/tags/stable:refs/tags/stable'
git log --oneline -1 stable
```

## Fix

1. **Land the fix on main first**, through a normal PR. Note the commit it
   merged as (`<fix>`). Every hotfix commit comes from main, so the next
   monthly promotion still has the fix.

2. **Make the hotfix branch at the stable commit** and cherry-pick the fix with
   `-x`. The `-x` line is what the release workflow checks.

   ```
   git switch -c hotfix/<name> stable
   git cherry-pick -x <fix>
   ```

   If the cherry-pick conflicts, resolve it and keep the `(cherry picked from
   commit …)` line in the message. A change that main does not need (a
   stable-only workaround) instead needs a line `Hotfix-only: <reason>` in its
   message, and no merge commits are allowed.

3. **Check the branch** the way the workflow will:

   ```
   tools/check-hotfix-branch.sh refs/tags/stable HEAD origin/main
   ```

4. **Push it, then start CI and the release build.** Both run by hand.
   - CI (the full test suite) runs on the hotfix branch itself.
   - Release (hotfix) runs from main, naming the hotfix. It runs the same check,
     applies the migrations to MariaDB, builds the commit it checked, and
     publishes it as `release-<short_sha>`. That release is a pre-release and
     never Latest, so fresh installs and servers on main do not get it.

   A push alone starts neither: the hotfix tree has stable's workflow files.

   ```
   git push origin hotfix/<name>
   gh workflow run ci.yml --ref hotfix/<name>
   gh workflow run release-hotfix.yml --ref main -f ref=hotfix/<name>
   gh run list --workflow ci.yml --branch hotfix/<name>
   gh run list --workflow release-hotfix.yml
   ```

   Wait until both runs are green.

5. **Sign the release** if release signing is in force (ADR-0162), on the
   machine that holds the key:

   ```
   tools/sign-release.sh release-<short_sha>
   ```

6. **Promote it.** This moves `stable` and with it every stable server, so it
   needs the maintainer's go-ahead. `promote` refuses a commit whose release is
   not published yet.

   ```
   JABALI_REPO_DIR=$PWD go run ./panel-api/cmd/server release promote <full_sha>
   ```

   Stable servers with auto-update take it at their next timer run (daily,
   04:30). To apply it now on a server:

   ```
   systemctl start --no-block jabali-autoupdate.service
   ```

   A server that runs a newer build than stable (it followed main) stays on
   its build: `jabali update` never moves a server backwards.

7. **Merge the hotfix branch back into main.** Do this every time. The hotfix
   commit is not on main, so the next monthly promotion is not a descendant of
   it. An update rule that moves servers only forward along history (planned
   with release signing) would refuse that promotion. Merge without changing
   main's files, as a merge commit, not a squash:

   ```
   git switch -c merge-hotfix-<name> origin/main
   git merge -s ours --no-ff origin/hotfix/<name> -m "Merge hotfix/<name>: main already has its fixes"
   git push origin merge-hotfix-<name>
   gh pr create --base main --head merge-hotfix-<name> --title "Merge hotfix/<name> into main's history" --body "No file changes: main already has every fix on hotfix/<name>."
   gh pr merge <pr> --merge
   ```

   `-s ours` keeps main's files as they are. That is right because every
   hotfix commit is a cherry-pick from main. A `Hotfix-only:` commit is
   dropped from main here, which is what that line means.

### Rollback

Servers never move backwards, so moving `stable` back does not undo a hotfix
on a server that already installed it. To undo it, revert the fix on main and
ship the revert as a new hotfix (steps 1–7).

## Verification

On a stable server after its update:

```
jabali version
journalctl -u jabali-autoupdate --since today | grep -E 'release: (found|installed)'
```

`jabali version` shows the hotfix commit. The journal shows
`release: found release-<short_sha> for this commit` and the installed binaries.
That means the server used the prebuilt release, not a source build.
