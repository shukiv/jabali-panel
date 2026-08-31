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
    }

    /**
     * Lazy fetch — first checks $_SESSION, then mints a new validation
     * call when a fresh `?token=` is in the URL. Returns null when
     * neither path yields creds (Adminer falls through to its
     * default form so the failure surface is visible).
     */
    private function fetchCreds() {
        if ($this->creds !== null) {
            return $this->creds === false ? null : $this->creds;
        }
        if (!empty($_SESSION['jabali_adminer_creds'])) {
            $this->creds = $_SESSION['jabali_adminer_creds'];
            return $this->creds;
        }
        $token = isset($_GET['token']) ? $_GET['token'] : '';
        if ($token === '' || !preg_match('/^[A-Za-z0-9_-]{40,200}$/', $token)) {
            $this->creds = false;
            return null;
        }
        $body = json_encode(['token' => $token]);
        $fp = @stream_socket_client('unix://' . $this->socket_path, $errno, $errstr, 5);
        if (!$fp) {
            $this->creds = false;
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
            $this->creds = false;
            return null;
        }
        $headers = substr($resp, 0, $sep);
        $payload = substr($resp, $sep + 4);
        if (!preg_match('#^HTTP/1\\.\\d\\s+200\\b#', $headers)) {
            $this->creds = false;
            return null;
        }
        if (stripos($headers, 'Transfer-Encoding: chunked') !== false) {
            $payload = $this->dechunk($payload);
        }
        $j = json_decode($payload, true);
        if (!is_array($j) || !isset($j['driver']) || !isset($j['username']) ||
            !isset($j['password']) || !isset($j['db'])) {
            $this->creds = false;
            return null;
        }
        $this->creds = $j;
        $_SESSION['jabali_adminer_creds'] = $j;
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
