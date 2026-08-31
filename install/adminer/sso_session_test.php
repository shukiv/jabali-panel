<?php
// GH #1406 — drive the Adminer SSO plugin's token/session branching without a
// browser. Stubs the socket validation; asserts fresh-token-wins, cache-on-same
// -token, logout-clears, and that a failed new token drops the cached identity.
error_reporting(E_ALL & ~E_WARNING & ~E_NOTICE & ~E_DEPRECATED);
require __DIR__ . '/jabali-sso-plugin.php';

use Adminer\JabaliAdminerSSO;

// Test double: canned creds per token, and a call counter so we can prove a
// consumed token is served from cache (not re-validated).
class StubSSO extends JabaliAdminerSSO {
    public static $map = [];
    public static $calls = 0;
    protected function validateToken($token) {
        self::$calls++;
        return isset(self::$map[$token]) ? self::$map[$token] : null;
    }
}

// 40+ char tokens to satisfy the plugin's format regex.
$TA = str_repeat('a', 44);
$TB = str_repeat('b', 44);
$TC = str_repeat('c', 44);
StubSSO::$map = [
    $TA => ['driver'=>'pgsql','server'=>'/run/postgresql','username'=>'alice_pgadmin','password'=>'pa','db'=>'alice_db'],
    $TB => ['driver'=>'pgsql','server'=>'/run/postgresql','username'=>'bob_pgadmin','password'=>'pb','db'=>'bob_db'],
    // $TC intentionally absent → validateToken returns null.
];

$fail = 0;
function check($label, $cond) {
    global $fail;
    echo ($cond ? "  PASS " : "  FAIL ") . $label . "\n";
    if (!$cond) $fail++;
}
// Simulate one HTTP request: set superglobals, build a fresh plugin (as Adminer
// does per request), return credentials().
function req($get, $post = []) {
    $_GET = $get; $_POST = $post;
    $p = new StubSSO('/run/jabali-panel/api.sock');
    return $p->credentials();
}

// 1. First SSO entry for Alice.
$c = req(['token'=>$TA]);
check('alice entry -> alice creds', $c && $c[1] === 'alice_pgadmin');
$after1 = StubSSO::$calls;

// 2. Adminer auto-submit re-POSTs the SAME token -> served from cache, NOT re-validated.
$c = req(['token'=>$TA]);
check('same token -> still alice (cache)', $c && $c[1] === 'alice_pgadmin');
check('same token did NOT re-validate (single-use safe)', StubSSO::$calls === $after1);

// 3. In-UI navigation (no token) -> cache.
$c = req([]);
check('no token -> alice from cache', $c && $c[1] === 'alice_pgadmin');

// 4. Bob opens in the SAME browser (new token) -> fresh token WINS, Alice gone.
$c = req(['token'=>$TB]);
check('bob new token -> bob creds (fresh-token-wins)', $c && $c[1] === 'bob_pgadmin');
$c = req([]);
check('no token after bob -> bob (not alice)', $c && $c[1] === 'bob_pgadmin');

// 5. Logout clears the session -> no creds afterwards.
req([], ['logout'=>1]);           // the logout POST (constructor clears)
$c = req([]);
check('after logout -> no creds (logout actually ends session)', $c === null);

// 6. Re-enter as Alice, then a NEW token that FAILS validation must drop the
//    cached identity (never continue as Alice).
req(['token'=>$TA]);
$c = req(['token'=>$TC]);          // $TC not in map -> validateToken null
check('failed new token -> null', $c === null);
$c = req([]);
check('failed new token dropped cached identity', $c === null);

// 7. GH #1406: the admin all-databases handoff returns an EMPTY db. The plugin
//    must NOT pin the view/dropdown to it, or Adminer shows no database list.
$TD = str_repeat('d', 44);
StubSSO::$map[$TD] = ['driver'=>'pgsql','server'=>'/run/postgresql','username'=>'postgres','password'=>'pp','db'=>''];
$_GET = ['token'=>$TD]; $_POST = [];
$admin = new StubSSO('/run/jabali-panel/api.sock');
check('admin all-DBs: database() not pinned (null)', $admin->database() === null);
check('admin all-DBs: databases() lists all (null, not [""])', $admin->databases() === null);
// A normal per-DB tenant handoff still pins to its one database.
$_GET = ['token'=>$TA]; $_POST = [];
$tenant = new StubSSO('/run/jabali-panel/api.sock');
check('tenant: databases() still pinned to its db', $tenant->databases() === ['alice_db']);

echo ($fail === 0 ? "ALL SSO SESSION CHECKS PASSED\n" : "FAILURES: $fail\n");
exit($fail === 0 ? 0 : 1);
