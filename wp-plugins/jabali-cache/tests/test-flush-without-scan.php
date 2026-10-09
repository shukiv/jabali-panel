<?php
/**
 * Jabali Cache — flushes work without Redis SCAN.
 *
 * Before 1.2.0 a flush found the site's keys with SCAN and deleted them.
 * Since 1.2.0 a flush bumps a generation instead, and deleting the old keys is
 * only a best-effort reclaim. This test proves correctness doesn't depend on
 * the reclaim: it runs against a live Redis as a user that can't SCAN, and
 * checks that object and page flushes still take effect.
 *
 *   JABALI_TEST_REDIS=/run/redis/redis.sock \
 *   JABALI_TEST_REDIS_USER=<acl user without SCAN> JABALI_TEST_REDIS_PASS=<pw> \
 *   JABALI_TEST_PREFIX=jc:nwtest: php tests/test-flush-without-scan.php
 *
 * The ACL user needs the WP-cache command set minus SCAN, fenced to
 * ~<JABALI_TEST_PREFIX>*. Skips (exit 0) without a live socket. Without SCAN
 * the test can't delete the object keys it wrote; they sit under its own
 * per-run prefix.
 *
 * @package Jabali_Cache
 */

error_reporting( E_ALL );

$tests  = 0;
$failed = 0;
function fs_assert( $cond, $msg ) {
	global $tests, $failed;
	$tests++;
	echo ( $cond ? '  ok   - ' : '  FAIL - ' ) . $msg . "\n";
	if ( ! $cond ) {
		$failed++;
	}
}

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

$live = getenv( 'JABALI_TEST_REDIS' );
if ( ! $live ) {
	echo "Flush without SCAN: SKIP (set JABALI_TEST_REDIS and a no-SCAN ACL user to enable)\n";
	exit( 0 );
}

require __DIR__ . '/../includes/lib.php';

$base   = (string) ( getenv( 'JABALI_TEST_PREFIX' ) ?: 'jc:nwtest:' );
$prefix = $base . 'fs' . getmypid() . ':';

$cfg               = Jabali_Cache_Config::load();
$cfg['scheme']     = 'unix';
$cfg['socket']     = $live;
$cfg['database']   = 1;
$cfg['timeout']    = 1.0;
$cfg['username']   = (string) getenv( 'JABALI_TEST_REDIS_USER' );
$cfg['password']   = (string) getenv( 'JABALI_TEST_REDIS_PASS' );
$cfg['prefix']     = $prefix;
$cfg['page_cache'] = true;
Jabali_Cache_Config::set( $cfg );

require __DIR__ . '/../includes/class-object-cache.php';
require __DIR__ . '/../includes/class-page-cache.php';

echo "Flush without SCAN (live: {$live}, prefix {$prefix}):\n";

$probe = new Jabali_Cache_Client( $cfg );
fs_assert( $probe->connect(), 'connected as the test user' );
// Precondition: this user really can't SCAN, or the test proves nothing.
// A SCAN-based delete must find nothing, leaving the probe key in place.
$probe->set( $prefix . 'probe', '1', 60 );
$swept = $probe->delete_by_pattern( $prefix . '*' );
fs_assert( 0 === $swept && '1' === $probe->get( $prefix . 'probe' ), 'precondition: this user cannot SCAN (a pattern delete removed nothing)' );
$probe->del( $prefix . 'probe' );

$raw = new Jabali_Cache_Client( $cfg );
$raw->connect();

// --- Object cache -----------------------------------------------------------
$a = new Jabali_Cache_Object_Cache();
$a->set( 'k1', 'v1', 'posts' );
$a->set( 'k2', 'v2', 'options' );
$b = new Jabali_Cache_Object_Cache();
fs_assert( 'v1' === $b->get( 'k1', 'posts', true ), 'a second request reads the cached value' );

fs_assert( true === $a->flush(), 'flush() reports success' );
$c = new Jabali_Cache_Object_Cache();
fs_assert( false === $c->get( 'k1', 'posts', true ), 'after flush() a new request misses k1' );
fs_assert( false === $c->get( 'k2', 'options', true ), 'after flush() a new request misses k2' );

$gen = $raw->get( $prefix . 'gen:o' );
fs_assert( is_string( $gen ) && (int) $gen > 1000000000000000, 'the object generation is seeded from the time in microseconds (' . var_export( $gen, true ) . ')' );

// flush_group isn't advertised; a direct call still never leaves stale data.
fs_assert( false === $c->supports( 'flush_group' ), "supports( 'flush_group' ) is false" );
$c->set( 'g1', 'x', 'posts' );
$c->set( 'g2', 'y', 'options' );
fs_assert( true === $c->flush_group( 'posts' ), 'flush_group() reports success' );
$d = new Jabali_Cache_Object_Cache();
fs_assert( false === $d->get( 'g1', 'posts', true ), 'after flush_group() the flushed group misses' );
fs_assert( false === $d->get( 'g2', 'options', true ), 'flush_group() flushes the whole object cache (never stale)' );

// An evicted generation counter must not bring back flushed data.
$d->set( 'ev', 'before', 'posts' );
$e1 = new Jabali_Cache_Object_Cache();
$e1->flush();
$raw->del( $prefix . 'gen:o' ); // as if Redis evicted it
$e2 = new Jabali_Cache_Object_Cache();
fs_assert( false === $e2->get( 'ev', 'posts', true ), 'a re-created generation does not resurrect flushed data' );

// A long-running process sees a flush made by another process.
$long = new Jabali_Cache_Object_Cache();
$long->set( 'lp', 'old', 'posts' );
$other = new Jabali_Cache_Object_Cache();
$other->flush();
if ( property_exists( 'Jabali_Cache_Object_Cache', 'gen_at' ) ) {
	$rp = new ReflectionProperty( 'Jabali_Cache_Object_Cache', 'gen_at' );
	$rp->setAccessible( true );
	$rp->setValue( $long, microtime( true ) - 60 ); // its generation is now stale
}
$long->flush_runtime();
fs_assert( false === $long->get( 'lp', 'posts', true ), 'a long-running process re-reads the generation' );

// While the generation can't be read, nothing is written to Redis: a key
// built without it could be read back once the counter is readable again.
$off = new Jabali_Cache_Object_Cache();
$off->get( 'warm', 'posts' ); // reads the generation.
$raw->set( $prefix . 'gen:o', 'unreadable' );
if ( isset( $rp ) ) {
	$rp->setValue( $off, microtime( true ) - 60 );
}
$off->set( 'off', 'v', 'posts' );
$raw->set( $prefix . 'gen:o', '0' );
$after = new Jabali_Cache_Object_Cache();
fs_assert( false === $after->get( 'off', 'posts', true ), 'nothing is written to Redis while the generation is unreadable' );
$raw->del( $prefix . 'gen:o' );

// --- Page cache ---------------------------------------------------------------
if ( ! method_exists( 'Jabali_Cache_Page_Cache', 'read_gen' ) ) {
	fs_assert( false, 'the page cache keeps a generation' );
	echo "\n{$tests} checks, {$failed} failed\n";
	exit( 1 );
}
$rk = new ReflectionProperty( 'Jabali_Cache_Page_Cache', 'pgen' );
$rk->setAccessible( true );
$rg = new ReflectionMethod( 'Jabali_Cache_Page_Cache', 'read_gen' );
$rg->setAccessible( true );
$rc = new ReflectionMethod( 'Jabali_Cache_Page_Cache', 'current' );
$rc->setAccessible( true );

// A page cache object as one request sees it: generation read, then checked.
function fs_page_request( $rk, $rg ) {
	$pc  = new Jabali_Cache_Page_Cache();
	$cli = new ReflectionProperty( 'Jabali_Cache_Page_Cache', 'client' );
	$cli->setAccessible( true );
	$client = $cli->getValue( $pc );
	$client->connect();
	$cfg = Jabali_Cache_Config::load();
	$rk->setValue( $pc, $rg->invoke( $pc, $client->get( $cfg['prefix'] . 'gen:p' ) ) );
	return $pc;
}

$raw->del( $prefix . 'gen:p' ); // the page cache creates its own counter.
$pc   = fs_page_request( $rk, $rg );
$pgen = $rk->getValue( $pc );
fs_assert( is_int( $pgen ) && $pgen > 1000000000000000, 'a missing page generation is created, seeded from the time in microseconds' );
$page = array( 'body' => '<p>hi</p>', 'expires_at' => time() + 60, 'pgen' => $pgen );
fs_assert( null !== $rc->invoke( $pc, $page ), 'a page from the current generation is served' );
fs_assert( null === $rc->invoke( $pc, array( 'body' => 'x' ) ), 'a page without a generation (pre-1.2.0) is a miss' );

$pc->purge_all();
fs_assert( null === $rc->invoke( fs_page_request( $rk, $rg ), $page ), 'after purge_all() the page is a miss' );

// A flush clears cached pages too, as it always has.
$pc2   = fs_page_request( $rk, $rg );
$page2 = array( 'body' => '<p>hi</p>', 'expires_at' => time() + 60, 'pgen' => $rk->getValue( $pc2 ) );
( new Jabali_Cache_Object_Cache() )->flush();
fs_assert( null === $rc->invoke( fs_page_request( $rk, $rg ), $page2 ), 'an object-cache flush also clears cached pages' );

// Clean up the counters (the object keys can't be found without SCAN).
$raw->del( $prefix . 'gen:o' );
$raw->del( $prefix . 'gen:p' );

echo "\n{$tests} checks, {$failed} failed\n";
exit( $failed > 0 ? 1 : 0 );
