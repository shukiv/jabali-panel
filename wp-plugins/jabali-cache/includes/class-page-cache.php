<?php

defined( 'ABSPATH' ) || exit; // wp.org: prevent direct file access.
/**
 * Jabali Cache — full-page cache engine.
 *
 * Driven by the wp-content/advanced-cache.php drop-in (which WordPress loads
 * before almost everything else when WP_CACHE is true). Caches anonymous,
 * cacheable GET responses in Redis and serves them on subsequent requests
 * without booting the rest of WordPress.
 *
 * This is OPTIONAL and OFF by default: jabali ships an nginx-level fastcgi
 * microcache (ADR-0108) that already covers anonymous edge caching. The WP
 * page cache is for operators who keep the nginx microcache disabled or want
 * WordPress-aware invalidation hooks.
 *
 * Safety: only ever serves to clearly-anonymous requests. Any login,
 * comment-author, WooCommerce/EDD cart, or password cookie disables both the
 * serve and the store path.
 *
 * @package Jabali_Cache
 */

if ( ! class_exists( 'Jabali_Cache_Client' ) ) {
	require_once __DIR__ . '/lib.php';
}

if ( class_exists( 'Jabali_Cache_Page_Cache' ) ) {
	return;
}

class Jabali_Cache_Page_Cache {

	/** @var array<string,mixed> */
	private $cfg;

	/** @var Jabali_Cache_Client */
	private $client;

	/** @var string */
	private $key = '';

	/** @var int */
	private $ttl = 300;

	/** @var bool */
	private $store = false;

	/** @var string request hash (the tail of $key), reused for the lock key. */
	private $hash = '';

	/** @var bool true when this request holds the single-flight regen lock. */
	private $holds_lock = false;

	/**
	 * Stampede protection (JAB-90). A stored page carries an `expires_at`
	 * freshness boundary but the Redis key lives STALE_WINDOW seconds longer, so
	 * a stale copy can be served while exactly one request (the regen-lock
	 * winner) regenerates. LOCK_TTL bounds the lock for crash safety;
	 * TTL_JITTER_PCT spreads expiries so a warmup batch doesn't all expire on the
	 * same second.
	 */
	const STALE_WINDOW   = 30;
	const LOCK_TTL       = 20;
	const TTL_JITTER_PCT = 10;

	/**
	 * JAB-92: gzip the stored HTML body above COMPRESS_MIN_BYTES to cut shared
	 * Redis memory. Bodies larger than MAX_BODY_BYTES are not cached at all so a
	 * single huge response can't evict useful object-cache keys.
	 */
	const COMPRESS_MIN_BYTES = 8192;
	const MAX_BODY_BYTES     = 2097152;

	public function __construct() {
		$this->cfg    = Jabali_Cache_Config::load();
		$this->ttl    = max( 1, (int) $this->cfg['page_ttl'] );
		$this->client = new Jabali_Cache_Client( $this->cfg );
	}

	/**
	 * Entry point called by advanced-cache.php. Serves a cached page (and
	 * exits) on a hit, or arms output buffering to store on a miss.
	 */
	public function run() {
		if ( empty( $this->cfg['enabled'] ) || empty( $this->cfg['page_cache'] ) ) {
			return;
		}
		if ( ! $this->is_cacheable_request() ) {
			return;
		}
		if ( ! $this->client->connect() ) {
			return;
		}

		$this->hash = $this->request_hash();
		$this->key  = $this->cfg['prefix'] . 'page:' . $this->hash;

		$payload = $this->read_payload( $this->client->get( $this->key ) );
		if ( null !== $payload ) {
			if ( $this->payload_is_fresh( $payload ) ) {
				$this->serve( $payload ); // fresh HIT — serve() exits.
			}
			// Stale but present (within STALE_WINDOW). Single-flight: the lock
			// winner regenerates; everyone else serves the stale copy so only ONE
			// request hits PHP/MySQL. Lock failure just means we serve stale.
			if ( $this->acquire_regen_lock() ) {
				$this->store = true;
				ob_start( array( $this, 'on_flush' ) );
				return;
			}
			$this->serve( $payload, 'STALE' ); // serve() exits.
		}

		// Hard miss (no copy at all). Try to be the single generator.
		if ( $this->acquire_regen_lock() ) {
			$this->store = true;
			ob_start( array( $this, 'on_flush' ) );
			return;
		}
		// Another request holds the lock but there's no stale copy yet: wait a
		// small bounded time and retry once (it may have just stored), else fall
		// through and render normally (rare cold-start race — correctness over a
		// perfect single-flight here).
		usleep( 50000 ); // 50ms.
		$payload = $this->read_payload( $this->client->get( $this->key ) );
		if ( null !== $payload && $this->payload_is_fresh( $payload ) ) {
			$this->serve( $payload ); // serve() exits.
		}
		$this->store = true;
		ob_start( array( $this, 'on_flush' ) );
	}

	/**
	 * JAB-92: true when a body is small enough to be worth caching. Very large
	 * responses are skipped so they don't evict useful keys from shared Redis.
	 *
	 * @param string $body
	 * @return bool
	 */
	private static function is_storable_size( $body ) {
		return strlen( (string) $body ) <= self::MAX_BODY_BYTES;
	}

	/**
	 * JAB-92: gzip-compress the payload body when zlib is available and the
	 * body clears COMPRESS_MIN_BYTES and actually shrinks. Marks the payload
	 * with compressed=gzip + the original length. No-op otherwise.
	 *
	 * @param array<string,mixed> $payload
	 * @return array<string,mixed>
	 */
	public static function compress_payload_body( array $payload ) {
		if ( ! isset( $payload['body'] ) || ! is_string( $payload['body'] ) ) {
			return $payload;
		}
		if ( ! function_exists( 'gzencode' ) ) {
			return $payload;
		}
		$body = $payload['body'];
		if ( strlen( $body ) <= self::COMPRESS_MIN_BYTES ) {
			return $payload;
		}
		$gz = @gzencode( $body, 6 ); // phpcs:ignore
		if ( false === $gz || strlen( $gz ) >= strlen( $body ) ) {
			return $payload; // incompressible — keep the plain body.
		}
		$payload['body']       = $gz;
		$payload['compressed'] = 'gzip';
		$payload['body_len']   = strlen( $body );
		return $payload;
	}

	/**
	 * JAB-92: reverse compress_payload_body(). Returns the payload with a plain
	 * body, or FALSE when a gzip payload can't be decoded (corrupt / zlib gone)
	 * so the caller can treat it as a miss and delete the bad key.
	 *
	 * @param array<string,mixed> $payload
	 * @return array<string,mixed>|false
	 */
	public static function decompress_payload_body( array $payload ) {
		if ( empty( $payload['compressed'] ) ) {
			return $payload;
		}
		if ( 'gzip' !== $payload['compressed'] || ! function_exists( 'gzdecode' ) ) {
			return false;
		}
		$plain = @gzdecode( $payload['body'] ); // phpcs:ignore
		if ( false === $plain ) {
			return false;
		}
		$payload['body'] = $plain;
		unset( $payload['compressed'], $payload['body_len'] );
		return $payload;
	}

	/**
	 * Decode a stored page payload, or null when absent/corrupt.
	 *
	 * @param string|false $raw
	 * @return array<string,mixed>|null
	 */
	private function read_payload( $raw ) {
		if ( false === $raw ) {
			return null;
		}
		$payload = @unserialize( $raw, array( 'allowed_classes' => false ) ); // phpcs:ignore
		if ( ! is_array( $payload ) || ! isset( $payload['body'] ) ) {
			return null;
		}
		// JAB-92: transparently inflate a gzip payload. A corrupt one is a miss —
		// drop the bad key so the next request regenerates it.
		$decoded = self::decompress_payload_body( $payload );
		if ( false === $decoded ) {
			if ( '' !== $this->key ) {
				$this->client->del( $this->key );
			}
			return null;
		}
		return $decoded;
	}

	/**
	 * @param array<string,mixed> $payload
	 * @return bool true while the copy is within its (jittered) freshness window.
	 */
	private function payload_is_fresh( array $payload ) {
		$expires = isset( $payload['expires_at'] ) ? (int) $payload['expires_at'] : 0;
		// Legacy payloads without expires_at (pre-JAB-90) are treated as fresh —
		// they'll be rewritten with the new field on their next regeneration.
		return 0 === $expires || time() < $expires;
	}

	/** @return string the per-page regen lock key. */
	private function lock_key() {
		return $this->cfg['prefix'] . 'lock:page:' . $this->hash;
	}

	/** @return bool whether this request won the single-flight regen lock. */
	private function acquire_regen_lock() {
		// SET NX EX — client->add() is exactly that. Failure (e.g. Redis blip)
		// returns false and the caller degrades to normal render / stale serve.
		$this->holds_lock = (bool) $this->client->add( $this->lock_key(), '1', self::LOCK_TTL );
		return $this->holds_lock;
	}

	/** Best-effort lock release; the LOCK_TTL is the crash-safety backstop. */
	private function release_regen_lock() {
		if ( $this->holds_lock ) {
			$this->client->del( $this->lock_key() );
			$this->holds_lock = false;
		}
	}

	/**
	 * Page TTL with ±TTL_JITTER_PCT jitter so a warmup batch of pages doesn't all
	 * expire on the same second (which would itself cause a mini-stampede).
	 *
	 * @return int
	 */
	private function jittered_ttl() {
		$base = $this->ttl;
		$span = (int) round( $base * self::TTL_JITTER_PCT / 100 );
		if ( $span < 1 ) {
			return max( 1, $base );
		}
		return max( 1, $base + mt_rand( -$span, $span ) );
	}

	/**
	 * Output-buffer callback. Returns the (unmodified) body so WordPress still
	 * sends it; stores a copy in Redis when the response is cacheable.
	 *
	 * @param string $body
	 * @param int    $phase
	 * @return string
	 */
	public function on_flush( $body, $phase = 0 ) {
		if ( ! $this->store || '' === $this->key ) {
			return $body;
		}
		// Only cache the final, complete buffer.
		if ( 0 === ( $phase & PHP_OUTPUT_HANDLER_FINAL ) && 0 === ( $phase & PHP_OUTPUT_HANDLER_END ) ) {
			return $body;
		}
		if ( ! $this->is_cacheable_response( $body ) ) {
			return $body;
		}
		// JAB-92: don't cache an oversized body — it would evict useful keys.
		if ( ! self::is_storable_size( $body ) ) {
			return $body;
		}

		// JAB-90: freshness window is jittered; the Redis key lives STALE_WINDOW
		// seconds beyond that so a stale copy can be served during regeneration.
		$fresh_ttl = $this->jittered_ttl();
		$payload   = array(
			'body'       => $body,
			'type'       => $this->detect_content_type(),
			'created'    => time(),
			'expires_at' => time() + $fresh_ttl,
			'gen'        => defined( 'JABALI_CACHE_VERSION' ) ? JABALI_CACHE_VERSION : '1',
		);
		// JAB-92: gzip large bodies before storing.
		$payload = self::compress_payload_body( $payload );
		// Best-effort store; failure just means no caching this time.
		$this->client->set( $this->key, serialize( $payload ), $fresh_ttl + self::STALE_WINDOW ); // phpcs:ignore
		// Release the single-flight lock now that the fresh copy is in place.
		$this->release_regen_lock();
		return $body;
	}

	/**
	 * Invalidate every cached page for this site (called from WP hooks on
	 * post publish/update, comment, etc.).
	 *
	 * @return int
	 */
	public function purge_all() {
		if ( ! $this->client->connect() ) {
			return 0;
		}
		return $this->client->delete_by_pattern( $this->cfg['prefix'] . 'page:*' );
	}

	/**
	 * Build the exact page-cache keys for a set of URL paths (JAB-91). Mirrors
	 * request_hash(): one key per path per variant (desktop 'd' + mobile 'm'),
	 * with any query string stripped so /p and /p?utm_x map to the same entry.
	 * Pure + prefix-scoped so it is unit-testable without Redis.
	 *
	 * @param array<int,string> $paths
	 * @param string            $scheme 'http' | 'https'
	 * @param string            $host   host[:port], as it appears in HTTP_HOST
	 * @return array<int,string> deduped page-cache keys
	 */
	public function keys_for_paths( array $paths, $scheme, $host ) {
		$keys = array();
		foreach ( $paths as $path ) {
			$path = (string) $path;
			$qpos = strpos( $path, '?' );
			if ( false !== $qpos ) {
				$path = substr( $path, 0, $qpos );
			}
			if ( '' === $path ) {
				continue;
			}
			foreach ( array( 'd', 'm' ) as $variant ) {
				$key            = $this->cfg['prefix'] . 'page:' . md5( $scheme . '|' . $host . '|' . $path . '|' . $variant );
				$keys[ $key ] = true;
			}
		}
		return array_keys( $keys );
	}

	/**
	 * Targeted purge (JAB-91): delete only the page-cache entries for $paths, in
	 * both desktop and mobile variants, instead of nuking the whole site's warm
	 * cache on a single post edit. Falls back to purge_all() when the canonical
	 * scheme/host can't be derived (correctness over precision).
	 *
	 * @param array<int,string> $paths
	 * @return int keys deleted (or the purge_all() count on fallback)
	 */
	public function purge_paths( array $paths ) {
		if ( ! $this->client->connect() ) {
			return 0;
		}
		// The stored key used the visitor's HTTP_HOST + REQUEST_URI (see
		// request_hash()). home_url() gives the site's own scheme+host; on the
		// common single-host install they match. If we can't resolve a host,
		// fall back to a whole-site purge rather than silently missing.
		$home = function_exists( 'home_url' ) ? home_url( '/' ) : '';
		$u    = $home ? wp_parse_url( $home ) : false;
		if ( ! is_array( $u ) || empty( $u['host'] ) ) {
			return $this->purge_all();
		}
		$scheme = ( isset( $u['scheme'] ) && 'https' === $u['scheme'] ) ? 'https' : 'http';
		$host   = $u['host'] . ( isset( $u['port'] ) ? ':' . $u['port'] : '' );

		$keys = $this->keys_for_paths( $paths, $scheme, $host );
		if ( empty( $keys ) ) {
			return 0;
		}
		return (int) $this->client->del( $keys );
	}

	// ------------------------------------------------------------------
	// Decisions.
	// ------------------------------------------------------------------

	private function is_cacheable_request() {
		$method = isset( $_SERVER['REQUEST_METHOD'] ) ? strtoupper( (string) $_SERVER['REQUEST_METHOD'] ) : 'GET'; // phpcs:ignore
		if ( 'GET' !== $method ) {
			return false;
		}
		if ( defined( 'DOING_CRON' ) && DOING_CRON ) {
			return false;
		}
		if ( defined( 'WP_CLI' ) && WP_CLI ) {
			return false;
		}
		// JAB-60: never serve/store the page cache for an authenticated request.
		// nginx already bypasses on $http_authorization; the WP layer must match
		// or a bearer/basic-authed GET could be cached and replayed without its
		// auth context.
		if ( self::has_auth_credentials( isset( $_SERVER ) ? (array) $_SERVER : array() ) ) { // phpcs:ignore
			return false;
		}
		$uri = isset( $_SERVER['REQUEST_URI'] ) ? (string) $_SERVER['REQUEST_URI'] : '/'; // phpcs:ignore

		// Query strings: skip by default (dynamic). Allow a short safelist
		// (tracking params that don't change content).
		$qpos = strpos( $uri, '?' );
		if ( false !== $qpos ) {
			$qs = substr( $uri, $qpos + 1 );
			if ( '' !== $qs && ! $this->only_safe_query_params( $qs ) ) {
				return false;
			}
		}

		foreach ( (array) $this->cfg['page_exclusions'] as $needle ) {
			if ( '' !== $needle && false !== strpos( $uri, $needle ) ) {
				return false;
			}
		}
		if ( $this->has_identity_cookie() ) {
			return false;
		}
		return true;
	}

	private function is_cacheable_response( $body ) {
		if ( '' === trim( (string) $body ) ) {
			return false;
		}
		if ( defined( 'DONOTCACHEPAGE' ) && DONOTCACHEPAGE ) {
			return false;
		}
		// Respect a non-200 status set during the request.
		if ( function_exists( 'http_response_code' ) ) {
			$code = http_response_code();
			if ( $code && 200 !== (int) $code ) {
				return false;
			}
		}
		// Never cache an admin/login page that slipped through.
		if ( function_exists( 'is_admin' ) && is_admin() ) {
			return false;
		}
		if ( function_exists( 'is_user_logged_in' ) && is_user_logged_in() ) {
			return false;
		}
		if ( function_exists( 'is_404' ) && is_404() ) {
			return false;
		}
		// A Set-Cookie emitted during render usually means session state.
		foreach ( headers_list() as $h ) {
			if ( 0 === stripos( $h, 'Set-Cookie:' ) ) {
				return false;
			}
		}
		// JAB-61: honor the response's own cache-control opt-outs, matching
		// nginx's private/no-store/no-cache bypass. A plugin may protect a page
		// with Cache-Control: private / no-store (or Pragma/Expires/Vary) without
		// setting a cookie on every response.
		if ( self::response_opts_out( headers_list() ) ) {
			return false;
		}
		return true;
	}

	/**
	 * JAB-60: true when the request carries any HTTP auth credential. Static and
	 * array-injected so it is unit-testable without the $_SERVER superglobal.
	 *
	 * @param array $server $_SERVER-style map.
	 * @return bool
	 */
	public static function has_auth_credentials( array $server ) {
		foreach ( array( 'HTTP_AUTHORIZATION', 'REDIRECT_HTTP_AUTHORIZATION', 'PHP_AUTH_USER', 'PHP_AUTH_PW', 'PHP_AUTH_DIGEST' ) as $k ) {
			if ( isset( $server[ $k ] ) && '' !== (string) $server[ $k ] ) {
				return true;
			}
		}
		return false;
	}

	/**
	 * JAB-61: true when the RESPONSE opted out of shared caching via its headers.
	 * Mirrors nginx's no-store/private bypass so the WP page cache never persists
	 * a response the application marked private. Static + array-injected for
	 * unit testing.
	 *
	 * @param array $headers headers_list()-style "Name: value" strings.
	 * @return bool
	 */
	public static function response_opts_out( array $headers ) {
		foreach ( $headers as $h ) {
			$lower = strtolower( (string) $h );
			$cpos  = strpos( $lower, ':' );
			if ( false === $cpos ) {
				continue;
			}
			$name = trim( substr( $lower, 0, $cpos ) );
			$val  = trim( substr( $lower, $cpos + 1 ) );
			switch ( $name ) {
				case 'cache-control':
					if ( false !== strpos( $val, 'private' )
						|| false !== strpos( $val, 'no-store' )
						|| false !== strpos( $val, 'no-cache' )
						|| preg_match( '/max-age\s*=\s*0(\D|$)/', $val ) ) {
						return true;
					}
					break;
				case 'pragma':
					if ( false !== strpos( $val, 'no-cache' ) ) {
						return true;
					}
					break;
				case 'vary':
					if ( '*' === $val || false !== strpos( $val, 'cookie' ) || false !== strpos( $val, 'authorization' ) ) {
						return true;
					}
					break;
				case 'expires':
					$exp = strtotime( trim( substr( (string) $h, $cpos + 1 ) ) );
					if ( false !== $exp && $exp <= time() ) {
						return true;
					}
					break;
			}
		}
		return false;
	}

	private function has_identity_cookie() {
		if ( empty( $_COOKIE ) || ! is_array( $_COOKIE ) ) { // phpcs:ignore
			return false;
		}
		$markers = array(
			'wordpress_logged_in_',
			'comment_author_',
			'wp-postpass_',
			'woocommerce_items_in_cart',
			'woocommerce_cart_hash',
			'wp_woocommerce_session_',
			'edd_items_in_cart',
			'jabali_nocache',
		);
		foreach ( array_keys( $_COOKIE ) as $name ) { // phpcs:ignore
			foreach ( $markers as $m ) {
				if ( 0 === strpos( (string) $name, $m ) ) {
					return true;
				}
			}
		}
		return false;
	}

	private function only_safe_query_params( $qs ) {
		$safe = array( 'utm_source', 'utm_medium', 'utm_campaign', 'utm_term', 'utm_content', 'fbclid', 'gclid', 'ref' );
		parse_str( $qs, $params );
		foreach ( array_keys( (array) $params ) as $k ) {
			if ( ! in_array( $k, $safe, true ) ) {
				return false;
			}
		}
		return true;
	}

	private function request_hash() {
		$https  = ( ! empty( $_SERVER['HTTPS'] ) && 'off' !== $_SERVER['HTTPS'] ) ? 'https' : 'http'; // phpcs:ignore
		$host   = isset( $_SERVER['HTTP_HOST'] ) ? (string) $_SERVER['HTTP_HOST'] : 'localhost'; // phpcs:ignore
		$uri    = isset( $_SERVER['REQUEST_URI'] ) ? (string) $_SERVER['REQUEST_URI'] : '/'; // phpcs:ignore
		// Strip a safe-only query string so /p and /p?utm_x share a cache entry.
		$qpos = strpos( $uri, '?' );
		if ( false !== $qpos ) {
			$uri = substr( $uri, 0, $qpos );
		}
		$mobile = $this->is_mobile() ? 'm' : 'd';
		return md5( $https . '|' . $host . '|' . $uri . '|' . $mobile );
	}

	private function is_mobile() {
		$ua = isset( $_SERVER['HTTP_USER_AGENT'] ) ? (string) $_SERVER['HTTP_USER_AGENT'] : ''; // phpcs:ignore
		return (bool) preg_match( '/(Mobile|Android|iPhone|iPad|iPod)/i', $ua );
	}

	private function detect_content_type() {
		foreach ( headers_list() as $h ) {
			if ( 0 === stripos( $h, 'Content-Type:' ) ) {
				return trim( substr( $h, strlen( 'Content-Type:' ) ) );
			}
		}
		return 'text/html; charset=UTF-8';
	}

	/**
	 * @param array<string,mixed> $payload
	 */
	private function serve( array $payload, $status = 'HIT' ) {
		$type = isset( $payload['type'] ) ? $payload['type'] : 'text/html; charset=UTF-8';
		$age  = isset( $payload['created'] ) ? max( 0, time() - (int) $payload['created'] ) : 0;
		if ( ! headers_sent() ) {
			header( 'Content-Type: ' . $type );
			header( 'X-Jabali-Cache: ' . $status ); // HIT (fresh) | STALE (served during regen).
			header( 'X-Jabali-Cache-Age: ' . $age );
		}
		echo $payload['body']; // phpcs:ignore WordPress.Security.EscapeOutput.OutputNotEscaped
		exit;
	}
}
