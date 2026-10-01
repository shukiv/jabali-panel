# Pending Snuffleupagus rules (not loaded)

The renderer reads only `../*.rules`. Nothing in this directory reaches
`/etc/jabali/snuffleupagus/active.rules`, and `jabali update` does not mirror
it to `/usr/share/jabali/snuffleupagus/rules/`.

## Why these rules wait here

Up to v0.13, Snuffleupagus stops parsing at the first byte above 0x7f, even
inside a comment, and drops every rule after it. Em-dashes in `00-base.rules`
comments (2026-07-02, GH #335; 2026-08-04, GH #897) stopped the parser at line
49. From then on, boxes in simulation or enforce mode loaded only the rules
that are still in `../00-base.rules`. Everything here was never loaded.

Fixing the parser (v0.14) or the comments would load these rules as written.
A soak on the test box (2026-10-01, enforce mode, `jabali app e2e` across the
catalog) showed what that breaks:

| Rule | Blocked call | Effect |
|---|---|---|
| include/require allowlist `\.(inc\|phtml\|php)$` | `require` in `/usr/local/bin/composer` (loads `phar://composer.phar/bin/composer`) | composer dies: Drupal and phpBB installs fail |
| include/require allowlist | `require_once` inside the wp-cli phar (`symfony/polyfill-mbstring/bootstrap.php`) | wp-cli dies: WordPress install fails |
| `ini_set` drops (`display_errors`, `error_reporting`, `error_log`, `memory_limit`, ...) | `ini_set('display_errors', ...)` | OpenCart install fails; every Laravel request (InvoiceShelf), PrestaShop and Moodle abort |
| `10-wordpress.rules` `allow` exceptions | - | Never apply: Snuffleupagus uses the FIRST matching rule, and these come after the base drops. An exception must come before the drop it overrides |
| `call_user_func` / `call_user_func_array` `.param("function")` | - | Never match: PHP 8 calls the parameter `callback` (Snuffleupagus logs "the parameter does not exists") |

## Bringing a rule back

1. Fix it here: a scoped exception placed before its drop, a narrower
   pattern, or the right parameter name.
2. Move it into `../00-base.rules` (or a `../10-<cms>.rules` overlay).
3. Run `jabali app e2e --domain <test domain> --keep` in enforce mode, then
   load each app's front page and admin pages, and check
   `journalctl -t snuffleupagus` for drops.
4. Keep every file ASCII-only; `TestSnuffleupagusBundle_IsASCII` checks this
   directory too.
