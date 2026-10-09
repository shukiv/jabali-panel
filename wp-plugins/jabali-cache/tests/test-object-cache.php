<?php
/**
 * Jabali Cache — object-cache engine test (no PHPUnit, no WordPress).
 *
 * Stubs the handful of WordPress functions the engine touches, then exercises
 * the full WP_Object_Cache contract. Runs purely in-memory unless a live Redis
 * socket is provided, in which case persistence is verified across two engine
 * instances (simulating two requests).
 *
 *   php tests/test-object-cache.php
 *   JABALI_TEST_REDIS=/run/redis/redis.sock php tests/test-object-cache.php
 *
 * @package Jabali_Cache
 */

error_reporting( E_ALL );

$tests  = 0;
$failed = 0;
function oc_assert( $cond, $msg ) {
	global $tests, $failed;
	$tests++;
	echo ( $cond ? "  ok   - " : "  FAIL - " ) . $msg . "\n";
	if ( ! $cond ) {
		$failed++;
	}
}

// Minimal WP stubs used by the engine.
if ( ! function_exists( 'wp_suspend_cache_addition' ) ) {
	function wp_suspend_cache_addition( $suspend = null ) {
		return false; }
}
if ( ! function_exists( 'is_multisite' ) ) {
	function is_multisite() {
		return false; }
}
if ( ! function_exists( 'get_current_blog_id' ) ) {
	function get_current_blog_id() {
		return 1; }
}

if ( ! defined( 'ABSPATH' ) ) {
	define( 'ABSPATH', sys_get_temp_dir() . '/' );
}
require __DIR__ . '/../includes/lib.php';

// Point config at a live socket if available, else a dead one (in-memory mode).
$live = getenv( 'JABALI_TEST_REDIS' );
$cfg  = Jabali_Cache_Config::load();
$cfg['socket']   = $live ? $live : '/nonexistent/jc.sock';
$cfg['database'] = 1;
$cfg['timeout']  = 1.0;
$cfg['prefix']   = 'jc:octest' . getmypid() . ':';
// Optional ACL login (JABALI_TEST_REDIS_USER/PASS), to run as a fenced user.
$cfg['username'] = (string) getenv( 'JABALI_TEST_REDIS_USER' );
$cfg['password'] = (string) getenv( 'JABALI_TEST_REDIS_PASS' );
Jabali_Cache_Config::set( $cfg );

require __DIR__ . '/../includes/class-object-cache.php';

echo $live ? "Object cache (live: {$live}):\n" : "Object cache (in-memory; no live Redis):\n";

$c = new Jabali_Cache_Object_Cache();

// Basic set/get.
oc_assert( true === $c->set( 'k', 'v', 'g' ), 'set returns true' );
$found = null;
oc_assert( 'v' === $c->get( 'k', 'g', false, $found ) && true === $found, 'get hit, found=true' );
oc_assert( false === $c->get( 'nope', 'g', false, $found ) && false === $found, 'get miss, found=false' );

// add() semantics.
oc_assert( false === $c->add( 'k', 'v2', 'g' ), 'add existing key returns false' );
oc_assert( true === $c->add( 'fresh', 1, 'g' ), 'add new key returns true' );

// replace() semantics.
oc_assert( false === $c->replace( 'absent', 'x', 'g' ), 'replace missing returns false' );
oc_assert( true === $c->replace( 'k', 'v3', 'g' ), 'replace existing returns true' );
oc_assert( 'v3' === $c->get( 'k', 'g' ), 'replace updated value' );

// Data types survive serialization.
$arr = array( 'a' => 1, 'b' => array( 2, 3 ), 'o' => (object) array( 'x' => 9 ) );
$c->set( 'complex', $arr, 'g' );
$got = $c->get( 'complex', 'g' );
oc_assert( is_array( $got ) && 9 === $got['o']->x && 3 === $got['b'][1], 'nested array/object round-trips' );

// incr/decr.
$c->set( 'ctr', 5, 'g' );
oc_assert( 7 === $c->incr( 'ctr', 2, 'g' ), 'incr by 2' );
oc_assert( 4 === $c->decr( 'ctr', 3, 'g' ), 'decr by 3' );
oc_assert( 0 === $c->decr( 'ctr', 100, 'g' ), 'decr clamps at 0' );

// get_multiple.
$c->set( 'm1', 'one', 'g' );
$c->set( 'm2', 'two', 'g' );
$multi = $c->get_multiple( array( 'm1', 'm2', 'm3' ), 'g' );
oc_assert( 'one' === $multi['m1'] && 'two' === $multi['m2'] && false === $multi['m3'], 'get_multiple mixed' );

// delete.
oc_assert( true === $c->delete( 'm1', 'g' ), 'delete returns true' );
oc_assert( false === $c->get( 'm1', 'g' ), 'deleted key is gone' );

// Non-persistent group never needs Redis.
$c->add_non_persistent_groups( array( 'transient_np' ) );
oc_assert( true === $c->set( 'x', 'y', 'transient_np' ), 'set in non-persistent group' );
oc_assert( 'y' === $c->get( 'x', 'transient_np' ), 'get from non-persistent group' );

// supports().
oc_assert( true === $c->supports( 'get_multiple' ) && false === $c->supports( 'nonsense' ), 'supports() reports features' );

// Cross-instance persistence (only meaningful with live Redis).
if ( $live ) {
	$stats = $c->stats();
	oc_assert( true === $stats['connected'], 'engine connected to live Redis (driver=' . $stats['driver'] . ')' );

	$c2    = new Jabali_Cache_Object_Cache();
	$found = null;
	// 'k' was set to 'v3' on the first instance; a second instance must read it from Redis.
	oc_assert( 'v3' === $c2->get( 'k', 'g', true, $found ) && true === $found, 'second instance reads persisted value from Redis' );

	// flush() must remove only our prefix.
	$c2->flush();
	$c3 = new Jabali_Cache_Object_Cache();
	oc_assert( false === $c3->get( 'k', 'g', true ), 'flush cleared persisted keys' );

	// The flush is the generation bump; the counters must survive the reclaim.
	$raw = new Jabali_Cache_Client( $cfg );
	$raw->connect();
	$gen_o = $raw->get( $cfg['prefix'] . 'gen:o' );
	oc_assert( false !== $gen_o && false !== $raw->get( $cfg['prefix'] . 'gen:p' ), 'flush keeps the generation counters' );
	oc_assert( 2 === $raw->count_keys( $cfg['prefix'] ), 'flush reclaims the old keys (only the counters remain)' );

	// The budget trim never deletes a generation counter.
	for ( $i = 0; $i < 5; $i++ ) {
		$c3->set( 't' . $i, 'x', 'g' );
	}
	$raw->trim_to_budget( $cfg['prefix'], 1 );
	oc_assert( $gen_o === $raw->get( $cfg['prefix'] . 'gen:o' ) && false !== $raw->get( $cfg['prefix'] . 'gen:p' ), 'trim_to_budget keeps the generation counters' );

	$raw->delete_by_pattern( $cfg['prefix'] . '*' );
	$raw->close();
	$c2->close();
	$c3->close();
} else {
	oc_assert( true, 'persistence checks skipped (no live Redis)' );
}

$c->close();
echo "\n{$tests} checks, {$failed} failed\n";
exit( $failed > 0 ? 1 : 0 );
