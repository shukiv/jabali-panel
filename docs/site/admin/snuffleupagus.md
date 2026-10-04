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

In enforce mode these also stop code that calls them for harmless reasons. Symfony Console reads the terminal size through `proc_open`, so Composer and the command-line installers that use it (Drupal, phpBB) stop with an error.

## Rules not loaded yet

The shipped bundle also contains rules for `putenv`, writing `.php` files, `eval`, uploads, `mail()` parameters, `curl` with `file://`, `extract`, `include`/`require`, `ini_set` and `call_user_func`, plus overlays for WordPress, Drupal, Joomla, PrestaShop and Magento. From July 2026 a character in a comment stopped Snuffleupagus 0.13 from reading them, so no box ran them. Loaded as written they break WordPress, Drupal, phpBB and Laravel apps, so they wait in `install/snuffleupagus/rules/pending/` and come back one at a time after testing.

## Packages that allow command execution

A hosting package can allow some command-execution functions (**Disabled PHP functions** on the package, GH #1701). For the sites on that package, PHP Defense does not ban the functions the package allows: each of their PHP pools loads its own copy of the rules, `/etc/jabali/snuffleupagus/pools/<pool>.rules`, with only those bans commented out. Every other rule, including the `eval` blacklist, still applies, and the server-wide rules apply to every other site. The copies are rebuilt whenever the mode or a rule changes. A domain's PHP Settings page shows, read-only, which functions PHP Defense blocks or logs for that domain. Command-line PHP (cron, SSH) keeps the server-wide rules.

## Turning one rule off

Open the rules list, choose **Disable** on a rule and give a reason. The panel comments that rule out of `/etc/jabali/snuffleupagus/active.rules` and reloads every pool. The CLI equivalent is `jabali php-defense rule-toggle`.

## Incidents

Stopped and simulated calls appear under **Recent incidents** within a minute, with the rule and the full log line. `jabali php-defense incidents` lists them on the command line.

## Related

- [PHP Manager](./php-manager.md) for per-version PHP configuration.
