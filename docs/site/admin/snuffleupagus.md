# Snuffleupagus (PHP Defense)

Security → PHP Defense. Snuffleupagus is a PHP extension that stops dangerous function calls inside PHP itself. The panel builds it for every installed PHP version and loads it into PHP-FPM and the PHP command line, so cron jobs and shell users get the same rules as web requests.

## Modes

| Mode | Effect |
|---|---|
| **Off** (default) | The extension is loaded with an empty rule set. |
| **Simulation** | Each rule logs what it would have stopped; nothing is stopped. |
| **Enforce** | A matching call ends the request or script with an error. |

Change it on the page or with `jabali php-defense mode <off|simulation|enforce>`. Every PHP-FPM pool reloads to pick up the change.

## Loaded rules

- Session hardening: SameSite=Lax on the session cookie, an HMAC on `unserialize()` data, a hardened random-number generator, and XXE protection when the XML extension is loaded.
- Command execution: `system`, `exec`, `shell_exec`, `passthru`, `popen`, `proc_open`, `pcntl_exec`.
- `assert` (evaluates a string as code), and the information leaks `show_source`, `highlight_file`, `phpinfo`.

In enforce mode these also stop code that calls them for harmless reasons. Symfony Console reads the terminal size through `proc_open`, so Composer and the command-line installers that use it (Drupal, phpBB) stop with an error. A Composer run started from the panel loads rules without these bans (see [Composer runs from the panel](#composer-runs-from-the-panel)).

## Rules not loaded yet

The shipped bundle also contains rules for `putenv`, writing `.php` files, `eval`, uploads, `mail()` parameters, `curl` with `file://`, `extract`, `include`/`require`, `ini_set` and `call_user_func`, plus overlays for WordPress, Drupal, Joomla, PrestaShop and Magento. From July 2026 a character in a comment stopped Snuffleupagus 0.13 from reading them, so no box ran them. Loaded as written they break WordPress, Drupal, phpBB and Laravel apps, so they wait in `install/snuffleupagus/rules/pending/` and come back one at a time after testing.

## Packages that allow command execution

A hosting package can allow some command-execution functions (**Disabled PHP functions** on the package, GH #1701). For the sites on that package, PHP Defense does not ban the functions the package allows: each of their PHP pools loads its own copy of the rules, `/etc/jabali/snuffleupagus/pools/<pool>.rules`, with only those bans commented out. Every other rule, including the `eval` blacklist, still applies, and the server-wide rules apply to every other site. The copies are rebuilt whenever the mode or a rule changes. A domain's PHP Settings page shows, read-only, which functions PHP Defense blocks or logs for that domain. Command-line PHP (cron, SSH) keeps the server-wide rules.

## Composer runs from the panel

A Composer run that a user starts from the panel (GH #2276, ADR-0176) loads its own copy of the rules, `/etc/jabali/snuffleupagus/composer.rules`.
- **Lifted bans:** the copy comments out the seven command-execution bans and the `phpinfo` ban. Composer runs `git`, `unzip` and composer.json scripts through `proc_open`. Symfony Process calls `phpinfo()` before it starts any process.
- **Logging:** the copy logs to PHP's error log instead of syslog, so a blocked call shows in the run's own log. The run is then marked failed.
- **Selection:** an ini in `/etc/jabali/snuffleupagus/composer.d/` selects the copy. The run sets `PHP_INI_SCAN_DIR`, so the PHP processes that Composer starts load it too.
- **Rebuilds:** the agent rebuilds both files from `active.rules` before every run.

Every other rule still applies, and cron and SSH PHP keep the server-wide rules. Composer runs only for a user whose hosting package includes it (**Composer**, off by default). The run executes in that user's SSH sandbox, as their own user.

## Turning one rule off

Open the rules list, choose **Disable** on a rule and give a reason. The panel comments that rule out of `/etc/jabali/snuffleupagus/active.rules` and reloads every pool. The CLI equivalent is `jabali php-defense rule-toggle`.

## Incidents

Stopped and simulated calls appear under **Recent incidents** within a minute, with the rule and the full log line. `jabali php-defense incidents` lists them on the command line.

PHP in SSH sessions and cron jobs runs in the SSH sandbox, which has no system log socket, so its blocks leave no line by default. Turn on **Log PHP Defense blocks from the sandbox** (Server Settings → SSH → Shell Sandbox, ADR-0177) to list them too. With it on, any program in the sandbox can write to the system log, as the user's website PHP already can. Each line carries the user's ID, and an incident counts for a site only when it comes from that site's owner (or root). Composer runs keep their blocks in the run's own log either way.

## Related

- [PHP Manager](./php-manager.md) for per-version PHP configuration.
