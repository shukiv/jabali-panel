<?php
/**
 * Jabali Cache — standalone test harness (no PHPUnit, no WordPress).
 *
 *   php tests/test-lib.php
 *   JABALI_TEST_REDIS=/run/redis/redis.sock php tests/test-lib.php   # live round-trip
 *
 * Exits non-zero on the first failure.
 *
 * @package Jabali_Cache
 */

error_reporting( E_ALL );

$tests  = 0;
$failed = 0;

function jc_assert( $cond, $msg ) {
	global $tests, $failed;
	$tests++;
	if ( $cond ) {
		echo "  ok   - {$msg}\n";
	} else {
		$failed++;
		echo "  FAIL - {$msg}\n";
	}
}

// lib.php guards direct access with `defined( 'ABSPATH' ) || exit;`. Define a
// dummy so the standalone harness can require it without WordPress loaded.
if ( ! defined( 'ABSPATH' ) ) {
	define( 'ABSPATH', __DIR__ . '/' );
}

require __DIR__ . '/../includes/lib.php';

// ---------------------------------------------------------------------------
// Config resolution.
// ---------------------------------------------------------------------------
echo "Config:\n";
Jabali_Cache_Config::reset();
$cfg = Jabali_Cache_Config::load();

jc_assert( '/run/redis/redis.sock' === $cfg['socket'], 'default socket is the jabali panel socket' );
jc_assert( 1 === (int) $cfg['database'], 'default database is 1 (ADR-0059)' );
jc_assert( 'unix' === $cfg['scheme'], 'default scheme is unix' );
jc_assert( 0 === strpos( $cfg['prefix'], 'jc:' ), 'prefix is namespaced with jc: ' . $cfg['prefix'] );
jc_assert( ':' === substr( $cfg['prefix'], -1 ), 'prefix ends with a separator' );
jc_assert( is_array( $cfg['global_groups'] ) && in_array( 'users', $cfg['global_groups'], true ), 'global groups include users' );

// Prefix must be deterministic across calls.
Jabali_Cache_Config::reset();
$cfg2 = Jabali_Cache_Config::load();
jc_assert( $cfg['prefix'] === $cfg2['prefix'], 'prefix is deterministic' );

// ---------------------------------------------------------------------------
// Client construction / graceful failure (no Redis required).
// ---------------------------------------------------------------------------
echo "Client (offline):\n";
$bad = new Jabali_Cache_Client(
	array_merge(
		$cfg,
		array(
			'scheme'  => 'unix',
			'socket'  => '/nonexistent/jabali-test.sock',
			'timeout' => 0.2,
		)
	)
);
jc_assert( false === $bad->connect(), 'connect() to a missing socket returns false' );
jc_assert( false === $bad->is_connected(), 'is_connected() is false after a failed connect' );
jc_assert( false === $bad->get( 'anything' ), 'get() returns false when disconnected' );
jc_assert( false === $bad->set( 'k', 'v' ), 'set() returns false when disconnected' );
jc_assert( 0 === $bad->del( 'k' ), 'del() returns 0 when disconnected' );
jc_assert( '' !== $bad->last_error(), 'last_error() is populated on failure' );

$mg = $bad->mget( array( 'a', 'b' ) );
jc_assert( is_array( $mg ) && false === $mg['a'] && false === $mg['b'], 'mget() returns all-miss map when disconnected' );

// ---------------------------------------------------------------------------
// Live round-trip (only when a real socket is provided).
// ---------------------------------------------------------------------------
$live = getenv( 'JABALI_TEST_REDIS' );
if ( $live ) {
	echo "Client (live: {$live}):\n";
	$c = new Jabali_Cache_Client(
		array_merge(
			$cfg,
			array(
				'scheme'   => 'unix',
				'socket'   => $live,
				'database' => 1,
				'timeout'  => 1.0,
			)
		)
	);
	if ( ! $c->connect() ) {
		echo "  SKIP - could not connect: " . $c->last_error() . "\n";
	} else {
		$pfx = 'jc:test' . getmypid() . ':';
		$k1  = $pfx . 'alpha';
		$k2  = $pfx . 'beta';

		jc_assert( $c->set( $k1, 'hello', 30 ), 'set k1 with TTL' );
		jc_assert( 'hello' === $c->get( $k1 ), 'get k1 round-trips' );
		jc_assert( false === $c->get( $pfx . 'missing' ), 'get of missing key is false' );

		jc_assert( true === $c->add( $k2, 'first', 30 ), 'add new key succeeds' );
		jc_assert( false === $c->add( $k2, 'second', 30 ), 'add existing key fails (NX)' );

		$m = $c->mget( array( $k1, $k2, $pfx . 'missing' ) );
		jc_assert( 'hello' === $m[ $k1 ] && 'first' === $m[ $k2 ] && false === $m[ $pfx . 'missing' ], 'mget mixed hit/miss' );

		$c->set( $pfx . 'ctr', '10' );
		jc_assert( 12 === $c->incr( $pfx . 'ctr', 2 ), 'incr by 2' );
		jc_assert( 11 === $c->decr( $pfx . 'ctr', 1 ), 'decr by 1' );

		$n = $c->delete_by_pattern( $pfx . '*' );
		jc_assert( $n >= 3, "delete_by_pattern removed our keys ({$n})" );
		jc_assert( 0 === $c->count_keys( $pfx ), 'no keys remain for the test prefix' );

		$c->close();
	}
} else {
	echo "Client (live): SKIP (set JABALI_TEST_REDIS=/run/redis/redis.sock to enable)\n";
}

// ---------------------------------------------------------------------------
// Circuit breaker (JAB-89): a Redis outage must fail fast across requests.
// Define a temp WP_CONTENT_DIR so the file-fallback store persists across
// client instances (APCu is usually disabled in CLI).
// ---------------------------------------------------------------------------
echo "Circuit breaker (JAB-89):\n";
$cb_root = sys_get_temp_dir() . '/jc-cb-test-' . getmypid();
@mkdir( $cb_root, 0755, true );
if ( ! defined( 'WP_CONTENT_DIR' ) ) {
	define( 'WP_CONTENT_DIR', $cb_root );
}
$cb_cfg = array_merge(
	$cfg,
	array( 'scheme' => 'unix', 'socket' => '/nonexistent/jc-cb.sock', 'timeout' => 0.2 )
);

// First client: connect to a dead socket fails → breaker trips.
$cb1 = new Jabali_Cache_Client( $cb_cfg );
jc_assert( false === $cb1->connect(), 'breaker: first connect to a dead socket fails' );
jc_assert( $cb1->circuit_open_until() > time(), 'breaker: circuit is open after the failure' );
jc_assert(
	$cb1->circuit_open_until() <= time() + Jabali_Cache_Client::CIRCUIT_TTL_BASE + Jabali_Cache_Client::CIRCUIT_TTL_JITTER,
	'breaker: open window is within the TTL bounds'
);

// Second client (same target): breaker open → fail fast with the breaker error.
$cb2 = new Jabali_Cache_Client( $cb_cfg );
jc_assert( false === $cb2->connect(), 'breaker: second connect fails while open' );
jc_assert( false !== strpos( $cb2->last_error(), 'circuit breaker open' ), 'breaker: fail-fast error names the breaker' );

// force_probe() bypasses the breaker → real connect attempt (still fails on a
// dead socket, but the error is the real one, not the breaker short-circuit).
$cb3 = ( new Jabali_Cache_Client( $cb_cfg ) )->force_probe();
jc_assert( false === $cb3->connect(), 'breaker: force_probe still fails on a dead socket' );
jc_assert( false === strpos( $cb3->last_error(), 'circuit breaker open' ), 'breaker: force_probe bypasses the breaker' );

// JAB-94: stats_key_count() caches its (bounded) result in the shared store,
// so repeated UI polls don't re-scan. On a disconnected client the count is 0,
// but the cache path is still exercised: cold read is uncached, the next read
// is served from cache.
jc_assert( false === $cb1->last_count_was_approx(), 'stats: count_keys approx flag defaults false' );
$sc1 = $cb1->stats_key_count( 'jc:sctest:' );
jc_assert( 0 === $sc1['count'] && false === $sc1['cached'], 'stats: cold key-count read is uncached' );
$sc2 = $cb1->stats_key_count( 'jc:sctest:' );
jc_assert( true === $sc2['cached'], 'stats: second key-count read is served from the cache' );

// Cleanup temp breaker artifacts.
$cb_glob = glob( $cb_root . '/cache/jabali-cache/*' );
if ( is_array( $cb_glob ) ) {
	array_map( 'unlink', $cb_glob );
}
@rmdir( $cb_root . '/cache/jabali-cache' );
@rmdir( $cb_root . '/cache' );
@rmdir( $cb_root );

// ---------------------------------------------------------------------------
// Targeted page-cache purge key derivation (JAB-91).
// ---------------------------------------------------------------------------
echo "Page-cache targeted purge (JAB-91):\n";
require __DIR__ . '/../includes/class-page-cache.php';
$pc  = new Jabali_Cache_Page_Cache();
$pfx = $cfg['prefix'];

// One path → desktop + mobile keys, matching request_hash()'s md5 format.
$k_home = $pc->keys_for_paths( array( '/' ), 'https', 'example.com' );
jc_assert( 2 === count( $k_home ), 'purge_paths: one path yields desktop + mobile keys' );
$expect_d = $pfx . 'page:' . md5( 'https|example.com|/|d' );
$expect_m = $pfx . 'page:' . md5( 'https|example.com|/|m' );
jc_assert(
	in_array( $expect_d, $k_home, true ) && in_array( $expect_m, $k_home, true ),
	'purge_paths: keys match the request_hash format for both variants'
);

// Query string is stripped: /p and /p?utm=1 map to the same keys.
$k_q   = $pc->keys_for_paths( array( '/p?utm=1' ), 'https', 'example.com' );
$k_noq = $pc->keys_for_paths( array( '/p' ), 'https', 'example.com' );
jc_assert( $k_q === $k_noq, 'purge_paths: query string is stripped' );

// Two distinct paths → 4 keys; duplicates deduped.
$k2 = $pc->keys_for_paths( array( '/', '/hello/', '/hello/' ), 'https', 'example.com' );
jc_assert( 4 === count( $k2 ), 'purge_paths: two distinct paths -> 4 keys, dupes deduped' );

// Empty / query-only paths yield nothing (guards the whole-site fallback path).
$k_empty = $pc->keys_for_paths( array( '', '?only-query' ), 'http', 'example.com' );
jc_assert( 0 === count( $k_empty ), 'purge_paths: empty / query-only paths produce no keys' );

echo "\n{$tests} checks, {$failed} failed\n";
exit( $failed > 0 ? 1 : 0 );
