<?php
/**
 * Jetpack's Vary header vs the Jabali page cache.
 *
 * With its Subscriptions module on, Jetpack answers every front-end request
 * with `Vary: accept, content-type`: Automattic\Jetpack\Status\Request::
 * is_frontend() renders blocks differently for JSON and XML requests and marks
 * the response for caches. The Jabali nginx page cache never stores a
 * response that varies on anything but Accept-Encoding, so on such a site no
 * page is ever cached, and every visit renders in PHP.
 *
 * On a server with the Jabali page cache, for a request that does not ask for
 * JSON or XML (the variant Jetpack calls "frontend", which browsers get), this
 * removes exactly that pair from the response's Vary. A JSON or XML request
 * keeps it, so nginx never stores that variant. Every other Vary token stays,
 * so a response that really varies on a cookie or the user agent still
 * bypasses the cache. A lone `Vary: Accept` is left alone: that is somebody's
 * own content negotiation, not Jetpack's.
 *
 * @package Jabali_Cache
 */

defined( 'ABSPATH' ) || exit;

/**
 * Rewrites the response's Vary header for the Jabali page cache.
 */
class Jabali_Cache_Vary {

	/**
	 * The Vary value to send instead, or null to leave the headers alone.
	 * An empty string means: remove the Vary header. Pure, for unit tests.
	 *
	 * @param array $headers     headers_list()-style "Name: value" strings.
	 * @param bool  $alt_variant True when the request asks for JSON or XML.
	 * @return string|null
	 */
	public static function filtered( array $headers, $alt_variant ) {
		if ( $alt_variant ) {
			return null;
		}
		$tokens = array();
		$found  = false;
		foreach ( $headers as $h ) {
			$h    = (string) $h;
			$cpos = strpos( $h, ':' );
			if ( false === $cpos || 'vary' !== strtolower( trim( substr( $h, 0, $cpos ) ) ) ) {
				continue;
			}
			$found = true;
			foreach ( explode( ',', substr( $h, $cpos + 1 ) ) as $t ) {
				$t = strtolower( trim( $t ) );
				if ( '' !== $t ) {
					$tokens[ $t ] = true;
				}
			}
		}
		if ( ! $found || isset( $tokens['*'] ) || ! isset( $tokens['accept'], $tokens['content-type'] ) ) {
			return null;
		}
		unset( $tokens['accept'], $tokens['content-type'] );
		return implode( ', ', array_keys( $tokens ) );
	}

	/**
	 * True on a server with the Jabali nginx page cache: the purge spool
	 * directory exists (the same signal the purge hooks use, GH #611).
	 *
	 * @return bool
	 */
	public static function page_cache_present() {
		return defined( 'JABALI_CACHE_NGINX_SPOOL' ) && @is_dir( JABALI_CACHE_NGINX_SPOOL ); // phpcs:ignore WordPress.PHP.NoSilencedErrors
	}

	/**
	 * Rewrite the response's Vary header while headers can still change.
	 */
	public static function apply() {
		if ( headers_sent() || ! self::page_cache_present() ) {
			return;
		}
		/**
		 * Filters whether Jabali Cache removes Jetpack's `Vary: accept,
		 * content-type` from browser responses so the page cache can store them.
		 *
		 * @param bool $strip Default true.
		 */
		if ( ! apply_filters( 'jabali_cache_strip_jetpack_vary', true ) ) {
			return;
		}
		$alt  = ( function_exists( 'wp_is_json_request' ) && wp_is_json_request() )
			|| ( function_exists( 'wp_is_xml_request' ) && wp_is_xml_request() );
		$vary = self::filtered( headers_list(), $alt );
		if ( null === $vary ) {
			return;
		}
		header_remove( 'Vary' );
		if ( '' !== $vary ) {
			header( 'Vary: ' . $vary );
		}
	}

	/**
	 * Jetpack sends its Vary on init (priority 10), and a Jetpack block can
	 * send it again while a block theme renders the page, which happens before
	 * wp_head. So: last on init, last on template_redirect, first on wp_head.
	 */
	public static function hooks() {
		add_action( 'init', array( __CLASS__, 'apply' ), PHP_INT_MAX );
		add_action( 'template_redirect', array( __CLASS__, 'apply' ), PHP_INT_MAX );
		add_action( 'wp_head', array( __CLASS__, 'apply' ), PHP_INT_MIN );
	}
}
