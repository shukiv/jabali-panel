<?php
/**
 * Jabali Adminer entry-point (Adminer 6 — GH #1405).
 *
 * Adminer 6 dropped the 4.x `AdminerPlugin` wrapper + `plugins/plugin.php`
 * loader. Plugins now live in the `Adminer\` namespace and are registered
 * through a global `adminer_object()` that returns an `Adminer\Plugins`
 * instance; Adminer honours it at
 *   Adminer::$instance = function_exists('adminer_object') ? adminer_object() : …
 * (verified against adminer-6.0.1.php). The Plugins dispatcher appends a
 * default Adminer as the final hook, so any method our plugin does NOT
 * override falls through to Adminer's own behaviour.
 *
 * Our plugin:
 *  1. reads ?token=<base64url> from the URL,
 *  2. POSTs it to /sso/adminer/validate over the panel-api UDS,
 *  3. supplies the engine-specific credentials to Adminer,
 *  4. forces a single-database scope (no schema browser leakage).
 *
 * Engine routing (driver ids unchanged in Adminer 6):
 *   - mariadb  → driver "server" (Adminer's MySQLi/PDO_MySQL)
 *   - postgres → driver "pgsql"  (Adminer's PostgreSQL via libpq)
 *
 * Layout:
 *   /var/www/jabali-adminer/index.php             — this file
 *   /var/www/jabali-adminer/adminer.php           — upstream single-file (6.x)
 *   /var/www/jabali-adminer/jabali-sso-plugin.php — plugin (namespace Adminer)
 */

require_once __DIR__ . '/jabali-sso-plugin.php';

// Global (unnamespaced) so Adminer's function_exists('adminer_object') check
// finds it. Returns the plugins dispatcher with our SSO plugin registered.
// \Adminer\Plugins is defined inside adminer.php, which is required below —
// this function is only CALLED once Adminer runs, by which point the class
// exists.
function adminer_object() {
    return new \Adminer\Plugins([
        new \Adminer\JabaliAdminerSSO('/run/jabali-panel/sso.sock'),
    ]);
}

require_once __DIR__ . '/adminer.php';
