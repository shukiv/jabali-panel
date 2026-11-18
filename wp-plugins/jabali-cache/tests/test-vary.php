<?php
/**
 * Jabali Cache — Jetpack Vary rewrite tests.
 *
 *   php tests/test-vary.php
 *
 * Exits non-zero on any failure. No PHPUnit / WordPress needed: the decision
 * is a static, array-injected function.
 *
 * @package Jabali_Cache
 */

error_reporting( E_ALL );

define( 'ABSPATH', __DIR__ . '/' );

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

require __DIR__ . '/../includes/class-vary.php';

$f = function ( array $headers, $alt = false ) {
	return Jabali_Cache_Vary::filtered( $headers, $alt );
};

echo "Jetpack's pair is removed from browser responses:\n";
jc_assert( '' === $f( array( 'Vary: accept, content-type' ) ), 'exact Jetpack header -> Vary removed' );
jc_assert( '' === $f( array( 'Vary: Accept, Content-Type' ) ), 'case-insensitive' );
jc_assert( '' === $f( array( 'vary:accept,content-type' ) ), 'no spaces' );
jc_assert( 'accept-encoding' === $f( array( 'Vary: Accept-Encoding, accept, content-type' ) ), 'Accept-Encoding kept' );
jc_assert( 'cookie' === $f( array( 'Vary: accept, content-type', 'Vary: Cookie' ) ), 'a second Vary header is merged and its token kept' );
jc_assert( 'user-agent' === $f( array( 'X-Powered-By: PHP', 'Vary: accept, content-type, User-Agent', 'Content-Type: text/html' ) ), 'other headers ignored, other tokens kept' );

echo "Left alone:\n";
jc_assert( null === $f( array( 'Vary: accept, content-type' ), true ), 'JSON/XML request keeps the pair (its variant is never stored)' );
jc_assert( null === $f( array( 'Vary: Accept' ) ), 'lone Accept (real content negotiation) untouched' );
jc_assert( null === $f( array( 'Vary: Content-Type' ) ), 'lone Content-Type untouched' );
jc_assert( null === $f( array( 'Vary: *, accept, content-type' ) ), 'Vary * untouched' );
jc_assert( null === $f( array( 'Vary: Accept-Encoding' ) ), 'Accept-Encoding only untouched' );
jc_assert( null === $f( array( 'Content-Type: text/html; charset=UTF-8', 'Cache-Control: no-cache' ) ), 'no Vary header -> nothing to do' );
jc_assert( null === $f( array() ), 'no headers -> nothing to do' );

echo "Only on a server with the Jabali page cache:\n";
$spool = sys_get_temp_dir() . '/jabali-vary-test-' . getmypid();
define( 'JABALI_CACHE_NGINX_SPOOL', $spool );
jc_assert( ! Jabali_Cache_Vary::page_cache_present(), 'no spool dir -> not a Jabali page-cache server' );
mkdir( $spool, 0700 );
jc_assert( Jabali_Cache_Vary::page_cache_present(), 'spool dir present -> Jabali page-cache server' );
rmdir( $spool );

echo "\n{$tests} tests, {$failed} failed\n";
exit( $failed > 0 ? 1 : 0 );
