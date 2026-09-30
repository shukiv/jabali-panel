# PHP Settings

`/jabali-panel/php-settings`. PHP configuration for your domains. The PHP limits below are set **per domain**: open the domain's **PHP Settings** tab on its page, or pick the domain here.

## Editable values

| Key | Purpose |
|---|---|
| `memory_limit` | Maximum memory a PHP script may allocate. Larger values let memory-hungry CMS plugins run; too large causes the host to OOM under load. |
| `upload_max_filesize` | Maximum single uploaded file size. |
| `post_max_size` | Maximum total POST body size. Must be ≥ `upload_max_filesize`. |
| `max_execution_time` | Maximum CPU time per request. |
| `max_input_time` | Maximum time PHP spends parsing input data. |
| `max_input_vars` | Maximum POST variables per request. |
| `display_errors` | Show PHP errors to the browser. Off by default; only enable temporarily in development. |
| `error_reporting` | Which PHP errors are reported (None, Production, or All). |
| `date.timezone` | Default time zone for date / time functions. |

Leave a value at **Use pool default** to inherit the value of your PHP pool; the option shows that value, for example `256M (Default)`.

## Settings your administrator controls

Your hosting package decides which of these values you may change. A value your administrator keeps for themselves shows the tag **Set by your administrator** and cannot be changed from your panel; the value shown still applies to the domain. Ask your administrator if you need it changed. An API request that changes such a value is refused with `php_setting_not_permitted`, naming the setting.

## Application

A saved change is applied to the domain on the next reconciler pass (within about a minute). It applies to that domain only, whatever PHP version the domain runs.

## OpCache

OPcache is enabled per-version with operator-chosen defaults (typically 128 MiB cache, 10000 files). Tenant tuning of OpCache is not exposed; ask the operator if you need a larger cache for a code base with many files.

## JIT

JIT is off by default. Your application is unlikely to benefit from JIT for typical web-request workloads (the per-request setup cost outweighs the runtime gain on short requests). Enable per-version under the operator's [PHP Manager](../admin/php-manager.md); the operator decides at the version level.

## Choosing the right values

Most CMSes (WordPress, Drupal, Moodle) ship recommendations:

- WordPress: `memory_limit 256M`, `upload_max_filesize 64M`, `post_max_size 64M`, `max_execution_time 300`.
- Moodle: `memory_limit 512M`, `upload_max_filesize 1024M` (when accepting large coursework uploads).

Start with the recommendation, then raise specific values only when you hit an error in the app's error log.


## Command line, Composer, and cron

Your selected PHP version now also applies on the **command line** — an
interactive SSH session, Composer, wp-cli, and your cron jobs all run your
pinned PHP version (and its enabled extensions), not the server default
(ADR-0126). Run `php -v` over SSH to confirm; Composer works because it
runs through that same `php`. If `php -m` is missing an extension, enable
it for your PHP version (an admin manages extensions in PHP Manager), then
re-check.
