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
| `display_errors` | Show PHP errors to the browser. Off by default, shown as `Off (Default)`, even when the PHP pool turns it on: a domain that sets no value always runs with it off. Only enable it temporarily in development. |
| `error_reporting` | Which PHP errors are reported (None, Production, or All). |
| `date.timezone` | Default time zone for date / time functions. |
| `log_errors` | Record PHP errors in the error log. Visitors never see logged errors. |
| `file_uploads` | Let the domain's PHP accept uploaded files. Turning it off breaks uploads in WordPress and most applications. |
| `short_open_tag` | Treat `<?` as a PHP opening tag. Only for old code that needs it: files that start with `<?xml` stop working. |

Leave a value at **Use pool default** to inherit the value of your PHP pool; the option shows that value, for example `256M (Default)` or `On (Default)`.

## Using the page

- Each setting is tagged **Custom** when the domain sets its own value, or **Pool default** when it inherits one. The tag follows your edits before you save. A changed setting is also tagged **Unsaved** until you save it.
- **Reset to default** next to a custom setting puts it back on the inherited value, the same as picking **Use pool default**. Save to apply it.
- The settings are in collapsible sections. **Resource Limits**, **Execution Limits** and **Error Handling & Runtime** start open. **Security** starts collapsed unless the domain sets one of its values. A section header shows how many of its settings are custom and how many are unsaved.
- The bar under the sections counts the unsaved changes. **Discard** puts the saved values back. While there are unsaved changes, the bar stays at the bottom of the window, so **Save Changes** is in view on a long page.
- With unsaved changes, the panel asks before you lose them when you switch to another tab of the domain, pick another domain on the PHP Settings page, or close or reload the browser tab. The sidebar links and the browser's Back button do not ask.

Every setting on this page is set on every request of every PHP domain, including domains left on the default. So a value you set on one domain never carries over to another domain that runs on the same PHP pool.

## Security settings

Two more settings limit what a domain's PHP code can reach. They are **admin only** unless your hosting package lets you change them; the server checks every value either way.

| Key | Purpose |
|---|---|
| `open_basedir` | The folders this domain's PHP may open files in, separated by `:`. Use `{DOCROOT}` for the domain's folder, `{WEBSPACEROOT}` for your home folder and `{TMP}` for the temp folders (`/tmp` and `/var/tmp`), or absolute paths. The default is your home folder plus the temp folders. You may list only folders inside your home folder, so you can narrow the default but never widen it; an administrator may add other folders, but never another account's home. The database socket and the page-cache purge folder are always added, so `localhost` database connections and WordPress cache purges keep working. Leave out `{TMP}` only if your application does not handle uploads: WordPress and most applications read uploaded files from the temp folder. |
| `allow_url_fopen` | Let file functions such as `file_get_contents()` read `http://` and `ftp://` URLs. cURL works either way. |

Like the other settings, both are set on every request of every PHP domain, so a narrower `open_basedir` on one domain never applies to another domain on the same PHP pool.

## Disabled functions and paths

The **Disabled functions and paths** section at the bottom of the page is read-only. Open it to see what this domain's PHP really runs with:

- **PHP functions**: each command-execution function (`exec`, `shell_exec`, `proc_open`, …) and any other function that is disabled or blocked, with its status: disabled by your hosting package, disabled server-wide, blocked by PHP Defense, allowed but logged by PHP Defense, allowed, or allowed but not in this PHP build.
  - **Allowed, not in this PHP build** means your hosting package allows the function, but the PHP that runs websites (PHP-FPM) does not include it, so a call fails. Common cases are the `pcntl_` functions, whose extension is often built into PHP's command-line version only, and `dl()`, which exists only in the command-line version. If a later PHP build includes the function, it becomes available without any change to your package.
  - **Starts only the shell and cat** appears next to an allowed function that starts another program (`exec`, `shell_exec`, `system`, `passthru`, `proc_open`, `popen`, `pcntl_exec`). It means the server protects PHP with an AppArmor profile that lets these functions start the shell (`sh`) and `cat`, but no other program: commands such as `df`, `ls`, `grep` or `id` fail with "Permission denied", and `cat` can read only some system files, such as `/proc/meminfo`, `/proc/cpuinfo` and `/proc/stat`. For server details, use PHP's own functions instead: `disk_free_space()` and `disk_total_space()` for disk space, `sys_getloadavg()` for load, and `php_uname()` for the operating system. PHP's `mail()` is not affected.
  - **Can start any program** appears instead when that AppArmor profile is not enforcing on the server: it is in complain mode, which only logs, or it is not loaded. The function can then run any program your site's user can run, so for anyone who takes over the site through a vulnerable app it is the same as shell access. An administrator can switch the profile to enforce.
- **Paths**: `include_path`, where PHP looks for included files, and `session.save_path`, where PHP stores sessions. These apply to every site on the same PHP pool; an empty `session.save_path` means PHP's temp folder.

Your hosting package decides which functions are disabled. Ask your administrator if your application needs one of them.

## Settings your administrator controls

Your hosting package decides which of these values you may change. A value your administrator keeps for themselves shows the tag **Set by your administrator** and cannot be changed from your panel; the value shown still applies to the domain. Ask your administrator if you need it changed. An API request that changes such a value is refused with `php_setting_not_permitted`, naming the setting.

## Application

A change saved from the panel or the API is applied to the domain within a few seconds. A change an administrator makes with `jabali domain php-settings set` on the server is applied on the next reconciler pass (within about a minute). A change applies to that domain only, whatever PHP version the domain runs.

Switching the domain to another PHP version also takes effect within a few seconds. When that version has no running PHP pool for your account yet, the pool is started first and the domain moves to it after the pool runs.

## OpCache

OPcache is enabled per-version with operator-chosen defaults (typically 128 MiB cache, 10000 files). Tenant tuning of OpCache is not exposed; ask the operator if you need a larger cache for a code base with many files.

**Reset OPcache** on the domain's **PHP Settings** tab clears the cached code after you deploy files that PHP does not pick up on its own. The reset restarts the PHP pool that serves the domain, so every site on that pool sees a short restart; the panel asks you to confirm first. The button shows only when your hosting package lets you edit PHP-FPM settings. Administrators, also while impersonating, can always reset.

## JIT

JIT is off by default. Your application is unlikely to benefit from JIT for typical web-request workloads (the per-request setup cost outweighs the runtime gain on short requests). Enable per-version under the operator's [PHP Manager](../admin/php-manager.md); the operator decides at the version level.

## Choosing the right values

Most CMSes (WordPress, Drupal, Moodle) ship recommendations:

- WordPress: `memory_limit 256M`, `upload_max_filesize 64M`, `post_max_size 64M`, `max_execution_time 300`.
- Moodle: `memory_limit 512M`, `upload_max_filesize 1024M` (when accepting large coursework uploads).

Start with the recommendation, then raise specific values only when you hit an error in the app's error log. **View error log** on the domain's **PHP Settings** tab opens that domain's error log live, on the same page.


## Command line, Composer, and cron

Your selected PHP version now also applies on the **command line** — an
interactive SSH session, Composer, wp-cli, and your cron jobs all run your
pinned PHP version (and its enabled extensions), not the server default
(ADR-0126). Run `php -v` over SSH to confirm; Composer works because it
runs through that same `php`. If `php -m` is missing an extension, enable
it for your PHP version (an admin manages extensions in PHP Manager), then
re-check.
