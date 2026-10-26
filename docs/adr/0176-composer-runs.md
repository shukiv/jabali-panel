# ADR-0176: Composer runs from the panel

**Status:** Accepted (2026-10-26)
**Driven by:** GH #2276 (a reporter asked to run Composer for a domain without SSH: install, update, audit and other commands, under the hosting account and the domain's PHP, with logs and limits; the maintainer approved building it on 2026-10-25).
**Related:** GH #2283 (Composer on the domain's PHP, `~/.composer`), ADR-0088 (PHP Defense / Snuffleupagus), GH #1701 (per-pool PHP Defense copies), GH #1458 (cron in the SSH sandbox), GH #282 (no-package policy).

## Context

A tenant without SSH can't run Composer. A tenant with SSH can, but on a server
with PHP Defense in enforce mode Composer stops at its first `proc_open` (it runs
`git`, `unzip` and composer.json scripts through it), because `00-base.rules`
bans the exec family for every PHP, CLI included.

The Drupal installer already runs Composer for a tenant, but outside any sandbox:
it runs a lock file the panel ships. A tenant's own composer.json is different.
Its scripts and plugins are the tenant's code.

Two things found on a test server shaped the design:

- Inside the SSH sandbox (`jabali-ssh-shell`, bubblewrap) `/run` is a fresh
  tmpfs, so `/dev/log` leads nowhere. `syslog()` from a sandboxed PHP returns
  true and nothing reaches the journal. PHP Defense logs to syslog, so a block
  inside the sandbox leaves no trace there.
- With `sp.log_media("php")` the same block prints
  `PHP Fatal error:  [snuffleupagus][…][disabled_function][drop] …` on stderr,
  and PHP exits 255.

## Decision

1. **Each run is a transient systemd unit.** The unit is
   `jabali-composer-<run id>.service`. It runs as the tenant's uid and gid in
   `jabali-user-<user>.slice`, so the package's CPU, memory and task limits
   apply. Its settings:
   - `Type=oneshot` with `RemainAfterExit=yes`, so the result outlives the
     process.
   - `TimeoutStartSec=30min`.
   - `MemoryMax=2G`, `TasksMax=256`, `CPUWeight=50` and `IOWeight=50`.

   It is started with `--no-block` and never with `--collect`, which would erase
   the exit status. The agent reads `Result` and `ExecMainStatus`:
   - `success` → succeeded.
   - `exit-code` → failed, with the exit code.
   - `timeout` → timed out.

   Once the panel has stored the result, the agent stops the unit and resets its
   failed state. Output goes to the journal, which journald's limits bound; a
   file would let a tenant's script fill the disk. The panel keeps the last
   256 KiB of the log with the run.
2. **Composer runs inside the tenant's SSH sandbox.** The command is
   `jabali-ssh-shell --exec /usr/bin/env … php<ver> <phar> <command>`, the
   sandbox cron uses (GH #1458), so composer.json scripts and plugins are
   confined the same way. `<ver>` is the PHP the domain's vhost passes PHP to.
   `<phar>` is the one the `/usr/local/bin/composer` dispatcher would pick for
   the user (GH #2283). `COMPOSER_HOME` is `~/.composer`.
3. **Panel-started runs use their own PHP Defense rules, without the exec
   bans.** Before each run the agent writes
   `/etc/jabali/snuffleupagus/composer.rules`: `active.rules` with the seven
   exec-family bans (system, exec, shell_exec, passthru, popen, proc_open,
   pcntl_exec) and the phpinfo ban commented out, as a GH #1701 pool copy does.
   Symfony Process calls `phpinfo()` before it starts any process, to learn
   whether PHP was built with sigchild support. On a test server in enforce
   mode every run stopped there, before its first download, and Composer
   exited 0 with no output. The copy also switches
   `sp.log_media` from syslog to php, so a block lands in the run's own log.
   Every other rule stays. An ini in `/etc/jabali/snuffleupagus/composer.d/`
   points `sp.configuration_file` at the copy. The run sets
   `PHP_INI_SCAN_DIR=:<that dir>` (the leading `:` keeps PHP's own conf.d), so
   the PHP processes Composer starts load it too. Both files live under the one
   `/etc/jabali` directory the sandbox binds. Cron and SSH PHP keep
   `active.rules`.
4. **A run blocked by PHP Defense never reports success.** The agent looks for
   a `[snuffleupagus]…[drop]` line in the log. The run is marked failed with
   `php_defense_blocked`, whatever the exit code.
5. **Only listed commands run.** The list is install, update, require, remove,
   audit, outdated, show, validate, dump-autoload and diagnose. Package names
   must be `vendor/name`, and require accepts a version constraint. No argument
   may start with `-`, or contain `$` or `%` (systemd expands those in
   `ExecStart`). The app directory is a path relative to
   `/home/<user>/domains/<domain>/` with no `..`, and it must be a real
   directory, not a link.
6. **It is a package entitlement, off by default.** The flag is
   `hosting_packages.composer_enabled`, default 0. It lifts the exec bans for
   the tenant's own code, so like tenant Docker a user with no package is
   denied (an exception to GH #282). One run at a time per domain.

   A composer.json script can run any command, so the entitlement amounts to
   running commands in the user's SSH sandbox. Grant it as you would SSH
   access. It allows less than `php_exec_enabled`, which lifts the same bans
   for the web pool, outside the sandbox.

   The agent runs a run only as a hosting user (uid 1000–59999), never as root
   or a system account, because the sandbox never jails uid 0 (GH #658).
7. **The panel collects results.** A read of a running run asks the agent, and
   a reconciler pass collects runs nobody reads, so no unit stays loaded. A run
   whose unit is gone before the panel saw it finish is marked failed.

## Consequences

- A tenant can now run `exec` and its family from their own composer.json
  scripts, and call `phpinfo()`, but only inside the sandbox, as their own
  user, and only when their package allows Composer.
- PHP Defense blocks in cron and SSH PHP still don't reach the incidents list,
  because of the sandbox's missing `/dev/log`. That is a separate change to
  the sandbox.
- Private repositories need credentials (`COMPOSER_AUTH` or `auth.json`). Not
  in this change.
- Cancelling a run stops Composer; it does not undo what Composer has already
  written. On the test box a `require` cancelled after one second had already
  added the package to `composer.json` and `composer.lock`. A user who cancels
  may need to run `install` (or restore the files) to bring `vendor/` back in
  line.
