<?php
/**
 * Jabali Adminer SSO plugin.
 *
 * Ported to the Adminer 6 plugin API (GH #1405): plugins live in the
 * `Adminer\` namespace and are registered via a global adminer_object()
 * that returns an Adminer\Plugins instance (see index.php). The dispatcher
 * calls each hook a plugin defines and takes the first non-null result,
 * falling back to Adminer's own default — so returning null here lets
 * Adminer render its standard login form when a token is missing/expired.
 *
 * Token flow:
 *   1. Browser hits /jabali-adminer/?token=<base64url>&engine=<eng>&db=<name>
 *   2. Plugin POSTs token to /sso/adminer/validate over the panel-api
 *      Unix socket; server consumes the token (FOR UPDATE + DELETE)
 *      and returns {driver, server, username, password, db}.
 *   3. Plugin caches creds in $_SESSION so subsequent requests within
 *      the same browser session don't need a new token (Adminer
 *      navigates inside its UI via more requests; without the cache
 *      the token would be replay-rejected on the very next click).
 *   4. Plugin auto-submits the Adminer login form with the cached
 *      creds so the user lands directly on the database view.
 *
 * Engine driver mapping (unchanged in Adminer 6 — verified against the
 * 6.0.1 driver tables):
 *   - mariadb  → driver "server"  (Adminer's MySQLi/PDO_MySQL backend)
 *   - postgres → driver "pgsql"   (libpq via pg_connect)
 *
 * @see panel-api/internal/api/sso_adminer_validate.go
 */

namespace Adminer;

class JabaliAdminerSSO {
    /** @var string Path to the panel-api Unix socket. */
    private $socket_path;
    /** @var array|null Validated credentials (lazy). */
    private $creds;

    public function __construct($socket_path) {
        $this->socket_path = $socket_path;
        // Adminer doesn't always start a session; we want to cache
        // validated SSO creds for the life of the browser session
        // so the user can navigate inside Adminer without burning
        // a fresh token on every click. Idempotent: if Adminer
        // already started a session, this is a no-op.
        if (session_status() === PHP_SESSION_NONE) {
            // Cookie-only sessions, scoped to /jabali-adminer/ so
            // they don't leak into other apps on the same vhost.
            session_set_cookie_params([
                'lifetime' => 0,
                'path'     => '/jabali-adminer/',
                'domain'   => '',
                'secure'   => true,
                'httponly' => true,
                'samesite' => 'Lax',
            ]);
            session_name('jabali_adminer_sid');
            @session_start();
        }

        // GH #1406: Adminer's Logout ($_POST['logout']) clears Adminer's own
        // auth but NOT our cached SSO creds — so the plugin would re-authenticate
        // on the post-logout redirect and Logout would appear to do nothing.
        // Drop our creds (and the validated-token marker) here, before Adminer
        // runs, so Logout actually ends the session.
        if (!empty($_POST['logout'])) {
            unset($_SESSION['jabali_adminer_creds'], $_SESSION['jabali_adminer_token']);
        }
    }

    /**
     * Lazy fetch of the SSO creds for this request.
     *
     * GH #1406 — fresh-token-wins. A `?token=` that is NOT the one already
     * validated for this session is a NEW SSO entry: it is always re-validated
     * and REPLACES any cached identity. This is what stops a second panel user
     * (or a tenant opening Adminer in a browser that previously opened the admin
     * all-DBs view, which caches the `postgres` superuser) from silently landing
     * in the PREVIOUS user's session. Adminer's own auto-submit re-POSTs the
     * SAME (already-consumed) token, so only a *different* token counts as a new
     * entry — the consumed one is served from cache, never re-validated.
     *
     * A new token that FAILS validation drops the cached creds and returns null,
     * so a failed entry never continues as the previously cached identity.
     * With no token at all, the session cache is reused (Adminer's in-UI
     * navigation). Returns null when nothing yields creds, so Adminer falls
     * through to its default form and the failure is visible.
     */
    private function fetchCreds() {
        if ($this->creds !== null) {
            return $this->creds === false ? null : $this->creds;
        }
        $token = isset($_GET['token']) ? $_GET['token'] : '';
        $tokenValid = ($token !== '' && preg_match('/^[A-Za-z0-9_-]{40,200}$/', $token));

        if ($tokenValid &&
            (empty($_SESSION['jabali_adminer_token']) || $_SESSION['jabali_adminer_token'] !== $token)) {
            $creds = $this->validateToken($token);
            if ($creds === null) {
                // A new token that fails MUST NOT fall back to stale creds.
                unset($_SESSION['jabali_adminer_creds'], $_SESSION['jabali_adminer_token']);
                $this->creds = false;
                return null;
            }
            // New identity — rotate the session id so it can't be fixated to the
            // prior user's, then replace the cached creds + validated-token marker.
            @session_regenerate_id(true);
            $_SESSION['jabali_adminer_creds'] = $creds;
            $_SESSION['jabali_adminer_token'] = $token;
            $this->creds = $creds;
            return $creds;
        }

        // No new token: reuse the session-cached creds (Adminer navigates its UI
        // with more requests, and the auto-submit POST re-includes the already-
        // validated token — neither should burn or re-validate a token).
        if (!empty($_SESSION['jabali_adminer_creds'])) {
            $this->creds = $_SESSION['jabali_adminer_creds'];
            return $this->creds;
        }
        $this->creds = false;
        return null;
    }

    /**
     * validateToken POSTs the single-use token to the panel over the Unix
     * socket; the server consumes it (FOR UPDATE + DELETE) and returns
     * {driver, server, username, password, db}. Returns the creds array, or
     * null on any transport/format/HTTP failure. Marked protected so a test can
     * stub the socket hop. Does NOT touch $_SESSION — the caller owns caching.
     */
    protected function validateToken($token) {
        $body = json_encode(['token' => $token]);
        $fp = @stream_socket_client('unix://' . $this->socket_path, $errno, $errstr, 5);
        if (!$fp) {
            return null;
        }
        $req = "POST /sso/adminer/validate HTTP/1.1\r\n"
             . "Host: jabali-sso\r\n"
             . "Content-Type: application/json\r\n"
             . "Content-Length: " . strlen($body) . "\r\n"
             . "Connection: close\r\n\r\n"
             . $body;
        fwrite($fp, $req);
        $resp = '';
        while (!feof($fp)) {
            $resp .= fread($fp, 4096);
        }
        fclose($fp);
        $sep = strpos($resp, "\r\n\r\n");
        if ($sep === false) {
            return null;
        }
        $headers = substr($resp, 0, $sep);
        $payload = substr($resp, $sep + 4);
        if (!preg_match('#^HTTP/1\\.\\d\\s+200\\b#', $headers)) {
            return null;
        }
        if (stripos($headers, 'Transfer-Encoding: chunked') !== false) {
            $payload = $this->dechunk($payload);
        }
        $j = json_decode($payload, true);
        if (!is_array($j) || !isset($j['driver']) || !isset($j['username']) ||
            !isset($j['password']) || !isset($j['db'])) {
            return null;
        }
        return $j;
    }

    private function dechunk($body) {
        $out = '';
        $i = 0;
        $len = strlen($body);
        while ($i < $len) {
            $crlf = strpos($body, "\r\n", $i);
            if ($crlf === false) break;
            $size = hexdec(substr($body, $i, $crlf - $i));
            $i = $crlf + 2;
            if ($size === 0) break;
            $out .= substr($body, $i, $size);
            $i += $size + 2;
        }
        return $out;
    }

    /**
     * Replace Adminer's login form with a hidden auto-submitting
     * form. Returning truthy stops Adminer (and later plugins) from
     * rendering the default form below. When there are no creds to
     * inject, return null so Adminer's standard form renders and the
     * user can see what failed.
     */
    public function loginForm() {
        $c = $this->fetchCreds();
        if ($c === null) return null;

        $h = function ($v) {
            return htmlspecialchars((string)$v, ENT_QUOTES);
        };

        // Adminer wraps loginForm() output inside its own <form
        // action="" method="post">. HTML forbids nested forms, so
        // browsers strip the inner one — output hidden <input>s
        // directly into the parent form and submit it.
        echo '<input type="hidden" name="auth[driver]"   value="' . $h($c['driver']) . '">';
        echo '<input type="hidden" name="auth[server]"   value="' . $h($c['server']) . '">';
        echo '<input type="hidden" name="auth[username]" value="' . $h($c['username']) . '">';
        echo '<input type="hidden" name="auth[password]" value="' . $h($c['password']) . '">';
        echo '<input type="hidden" name="auth[db]"       value="' . $h($c['db']) . '">';
        echo '<noscript><button type="submit">Continue</button></noscript>';
        echo '<p style="text-align:center;padding:2rem">Signing into Adminer via Jabali SSO…</p>';
        // Adminer ships a strict CSP (script-src ... nonce-... strict-dynamic).
        // Use Adminer\script(), which emits a <script> carrying the request
        // nonce, so the inline auto-submit isn't blocked (Adminer 6 helper).
        echo script('(document.querySelector("form[method=post]")||document.forms[0]).submit();');
        return true;
    }

    /**
     * credentials() supplies the connection tuple Adminer uses for
     * MySQLi/pgSQL connect(). Returning [server,user,pass] from the
     * session-cached creds means Adminer doesn't need to read its
     * own form values.
     */
    public function credentials() {
        $c = $this->fetchCreds();
        if ($c === null) return null;
        return [$c['server'], $c['username'], $c['password']];
    }

    /**
     * login() validates the submitted form. We auto-submit with the
     * exact creds we'd accept here, so this just verifies the
     * round-trip wasn't tampered with.
     */
    public function login($login, $password) {
        $c = $this->fetchCreds();
        if ($c === null) return null;
        return ($login === $c['username'] && $password === $c['password']);
    }

    /** Pin the visible DB to what was validated. */
    public function database() {
        $c = $this->fetchCreds();
        if ($c === null) return null;
        return $c['db'];
    }

    /** Restrict the dropdown to the validated DB only. */
    public function databases($flush = true) {
        $c = $this->fetchCreds();
        if ($c === null) return null;
        return [$c['db']];
    }
}
