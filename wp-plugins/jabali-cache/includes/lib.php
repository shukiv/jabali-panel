<?php

defined( 'ABSPATH' ) || exit; // wp.org: prevent direct file access.
/**
 * Jabali Cache — shared low-level library.
 *
 * Loaded by BOTH the plugin and the wp-content drop-ins (object-cache.php,
 * advanced-cache.php). Must be self-sufficient: no WordPress functions are
 * required to load this file, because advanced-cache.php runs before WP is
 * bootstrapped.
 *
 * Contents:
 *   - Jabali_Cache_Config : connection + behaviour configuration resolver.
 *   - Jabali_Cache_Client : Redis client. Uses the phpredis extension when
 *                           present, otherwise a dependency-free pure-PHP
 *                           RESP client over a stream socket.
 *
 * Design constraints (jabali panel, ADR-0059):
 *   - Redis is a SHARED instance reachable at unix:///run/redis/redis.sock.
 *   - Logical DB 1 is reserved for WordPress object cache.
 *   - The instance runs maxmemory-policy allkeys-lru: any key can vanish at
 *     any time, so every read is treated as best-effort.
 *   - DB 1 is shared across all tenants on the host. Isolation is by key
 *     PREFIX only. We therefore NEVER issue FLUSHDB (would wipe other
 *     tenants); flushing is always scoped to our own prefix via SCAN + DEL.
 *
 * @package Jabali_Cache
 */

if ( defined( 'JABALI_CACHE_LIB_LOADED' ) ) {
	return;
}
define( 'JABALI_CACHE_LIB_LOADED', true );

if ( ! defined( 'JABALI_CACHE_VERSION' ) ) {
	define( 'JABALI_CACHE_VERSION', '1.1.0' );
}

/**
 * Resolves runtime configuration from (in priority order):
 *   1. PHP constants (typically set in wp-config.php),
 *   2. built-in jabali defaults.
 *
 * Resolution is done WITHOUT touching the database or any generated file: the
 * object-cache / advanced-cache drop-ins load before the options API is safe to
 * call, so constants are the only early-safe source. The admin screen persists
 * preferences to the options table for display; the drop-ins are driven by the
 * wp-config.php JABALI_CACHE_* constants. Jabali stamps them automatically; a
 * manual install sets them in wp-config.php (see readme).
 */
class Jabali_Cache_Config {

	/** @var array<string,mixed>|null */
	private static $cache = null;

	/**
	 * @return array<string,mixed>
	 */
	public static function load() {
		if ( null !== self::$cache ) {
			return self::$cache;
		}

		// Config comes from DEFAULTS overridden by wp-config.php JABALI_CACHE_*
		// constants (apply_constants() below). This runs from the object-cache /
		// advanced-cache DROP-INS, which load before the options API is usable
		// (get_option() there recurses into the half-initialised object cache),
		// so settings must NOT be read from the DB here — constants are the only
		// safe early source. Jabali stamps the constants automatically; a manual
		// install sets them in wp-config.php (see readme). The admin screen and
		// wp-cli persist preferences to the options table for display, but the
		// drop-ins are driven by constants. (wp.org: no generated PHP config file.)
		$defaults = array(
			// Connection. Socket is preferred and is the jabali default.
			'scheme'   => 'unix',                     // 'unix' or 'tcp'.
			'socket'   => '/run/redis/redis.sock',
			'host'     => '127.0.0.1',
			'port'     => 6379,
			'database' => 1,                          // ADR-0059: DB 1 for WP.
			'password' => '',                         // jabali socket has no AUTH.
			'username' => '',                         // Redis ACL user (AUTH <user> <pass>); empty = legacy AUTH.
			'timeout'  => 1.0,                        // connect/read timeout (seconds).

			// Behaviour.
			'enabled'        => true,
			'prefix'         => '',                   // resolved below if empty.
			'maxttl'         => 0,                    // object-cache key TTL; 0 = rely on LRU.
			'page_cache'     => false,                // off by default (nginx microcache exists).
			'page_ttl'       => 300,
			'serializer'     => 'auto',               // auto|igbinary|php.
			'ignored_groups' => array( 'counts', 'plugins', 'themes' ),
			'global_groups'  => array(
				'blog-details', 'blog-id-cache', 'blog-lookup', 'global-posts',
				'networks', 'rss', 'sites', 'site-details', 'site-lookup',
				'site-options', 'site-transient', 'users', 'useremail',
				'userlogins', 'usermeta', 'user_meta', 'userslugs',
			),
			'page_exclusions' => array( '/wp-admin/', '/wp-json/', '/feed/', '/sitemap' ),
		);

		$cfg = $defaults;
		$cfg = self::apply_constants( $cfg );

		if ( '' === $cfg['prefix'] ) {
			$cfg['prefix'] = self::derive_prefix();
		}

		// Normalise the prefix: keep it short, deterministic and key-safe.
		$cfg['prefix'] = 'jc:' . preg_replace( '/[^A-Za-z0-9_.:-]/', '', (string) $cfg['prefix'] ) . ':';

		self::$cache = $cfg;
		return $cfg;
	}

	/**
	 * Allow explicit override (used by the admin "test connection" flow).
	 *
	 * @param array<string,mixed> $cfg
	 */
	public static function set( array $cfg ) {
		self::$cache = $cfg;
	}

	public static function reset() {
		self::$cache = null;
	}

	/**
	 * @return string
	 */
	public static function config_file_path() {
		// Retained ONLY to locate and delete a legacy jabali-cache-config.php
		// from older versions (config now lives in the options table). Never
		// written or read for runtime config.
		if ( defined( 'WP_CONTENT_DIR' ) ) {
			return WP_CONTENT_DIR . '/jabali-cache-config.php';
		}
		return '';
	}

	/**
	 * @param array<string,mixed> $cfg
	 * @return array<string,mixed>
	 */
	private static function apply_constants( array $cfg ) {
		$map = array(
			'JABALI_CACHE_DISABLED' => array( 'enabled', true ),  // inverted below.
			'JABALI_CACHE_SOCKET'   => array( 'socket', false ),
			'WP_REDIS_PATH'         => array( 'socket', false ),
			'JABALI_CACHE_HOST'     => array( 'host', false ),
			'JABALI_CACHE_PORT'     => array( 'port', false ),
			'JABALI_CACHE_DB'       => array( 'database', false ),
			'WP_REDIS_DATABASE'     => array( 'database', false ),
			'JABALI_CACHE_PASSWORD' => array( 'password', false ),
			'JABALI_CACHE_USER'     => array( 'username', false ),
			'JABALI_CACHE_PREFIX'   => array( 'prefix', false ),
			'JABALI_CACHE_MAXTTL'   => array( 'maxttl', false ),
			'JABALI_CACHE_SCHEME'   => array( 'scheme', false ),
			// GH #625: full-page cache is now constant-configurable too, so the
			// early drop-ins honor it (and the panel can stamp it) like the rest.
			'JABALI_CACHE_PAGE_CACHE' => array( 'page_cache', false ),
			'JABALI_CACHE_PAGE_TTL'   => array( 'page_ttl', false ),
		);
		foreach ( $map as $const => $spec ) {
			if ( ! defined( $const ) ) {
				continue;
			}
			list( $key, $invert ) = $spec;
			$val = constant( $const );
			if ( $invert ) {
				$cfg[ $key ] = ! $val;
			} else {
				$cfg[ $key ] = $val;
			}
		}
		// If a TCP host is explicitly set without a scheme override, switch to tcp.
		if ( defined( 'JABALI_CACHE_HOST' ) && ! defined( 'JABALI_CACHE_SCHEME' ) ) {
			$cfg['scheme'] = 'tcp';
		}
		return $cfg;
	}

	/**
	 * Per-site key prefix. Stable for the lifetime of an install, unique per
	 * site URL + filesystem location so two WP installs on the same tenant
	 * (and on the shared DB) never collide.
	 *
	 * @return string
	 */
	private static function derive_prefix() {
		$seed = '';
		if ( defined( 'ABSPATH' ) ) {
			$seed .= ABSPATH;
		}
		if ( defined( 'DB_NAME' ) ) {
			$seed .= '|' . DB_NAME;
		}
		if ( defined( 'WP_HOME' ) ) {
			$seed .= '|' . WP_HOME;
		} elseif ( isset( $_SERVER['HTTP_HOST'] ) ) {
			$seed .= '|' . $_SERVER['HTTP_HOST']; // phpcs:ignore
		}
		if ( '' === $seed ) {
			$seed = __FILE__;
		}
		return substr( md5( $seed ), 0, 12 );
	}

	/**
	 * Shallow merge that preserves array-typed defaults when override omits them.
	 *
	 * @param array<string,mixed> $base
	 * @param array<string,mixed> $over
	 * @return array<string,mixed>
	 */
	private static function merge( array $base, array $over ) {
		foreach ( $over as $k => $v ) {
			$base[ $k ] = $v;
		}
		return $base;
	}
}

/**
 * Redis client used by the object cache and page cache. Wraps the phpredis
 * extension when available and falls back to a self-contained pure-PHP RESP
 * implementation otherwise (jabali does not install php-redis by default).
 *
 * All methods are failure-tolerant: a connection or protocol error flips the
 * client into a permanently-disconnected state for the remainder of the
 * request and every operation returns a benign miss. The caller (object cache)
 * then behaves as a non-persistent cache — the site keeps working.
 */
class Jabali_Cache_Client {

	/** @var array<string,mixed> */
	private $cfg;

	/** @var \Redis|null phpredis instance. */
	private $redis = null;

	/** @var resource|null raw stream for the pure-PHP path. */
	private $stream = null;

	/** @var bool */
	private $connected = false;

	/** @var bool true once a fatal error disables the client for this request. */
	private $dead = false;

	/** @var string */
	private $last_error = '';

	/** @var string 'phpredis' | 'pure-php' | '' */
	private $driver = '';

	/** @var bool true when the phpredis connection was opened via pconnect (JAB-96). */
	private $persistent = false;

	/**
	 * @var bool When true, ignore the cross-request circuit breaker and always
	 * attempt a live Redis connection. Set by diagnostics (wp jabali-cache
	 * verify, panel health probes) so an open breaker can't mask a real test.
	 * JAB-89.
	 */
	private $force_probe = false;

	/**
	 * Circuit breaker (JAB-89). After a connection/auth/socket failure Redis is
	 * marked "known down" for CIRCUIT_TTL_BASE + up-to-JITTER seconds (jitter
	 * spreads the recovery probe across a fleet). While open, connect() returns
	 * runtime-only immediately instead of paying the per-request connect
	 * timeout on every uncached page.
	 */
	const CIRCUIT_TTL_BASE   = 15;
	const CIRCUIT_TTL_JITTER = 10;

	/**
	 * @var bool Set by count_keys() when a bounded scan was cut short before
	 * traversing the whole keyspace — the returned count is a floor, not exact.
	 * JAB-94.
	 */
	private $last_count_approx = false;

	/** Stats-cache TTL (seconds) + scan-step ceiling for the UI/stats path. */
	const STATS_CACHE_TTL   = 45;
	const STATS_SCAN_STEPS  = 40; // ~40k keys @ COUNT 1000 before going approximate.

	/**
	 * Max excess keys trim_to_budget() will collect (and thus delete) in one
	 * run (JAB-94). Bounds the scan + the delete so an over-budget site trims
	 * incrementally across timer runs instead of collecting a huge array and
	 * issuing one big delete spike on the shared Redis.
	 */
	const TRIM_MAX_PER_RUN = 5000;

	/**
	 * @param array<string,mixed>|null $cfg
	 */
	public function __construct( $cfg = null ) {
		$this->cfg = is_array( $cfg ) ? $cfg : Jabali_Cache_Config::load();
	}

	public function driver() {
		return $this->driver;
	}

	public function last_error() {
		return $this->last_error;
	}

	/**
	 * Force live Redis probes, bypassing the circuit breaker. Chainable.
	 * Used by `wp jabali-cache verify` + panel health checks. JAB-89.
	 *
	 * @param bool $on
	 * @return $this
	 */
	public function force_probe( $on = true ) {
		$this->force_probe = (bool) $on;
		return $this;
	}

	/**
	 * Unix timestamp the circuit breaker stays open until, or 0 if closed.
	 * Surfaced in status/diagnostics so the panel can show "Redis down until X".
	 * JAB-89.
	 *
	 * @return int
	 */
	public function circuit_open_until() {
		$st = $this->shared_get( $this->breaker_key() );
		return ( is_array( $st ) && isset( $st['until'] ) && (int) $st['until'] > time() ) ? (int) $st['until'] : 0;
	}

	public function is_connected() {
		return $this->connected && ! $this->dead;
	}

	/**
	 * @return bool
	 */
	public function connect() {
		if ( $this->connected ) {
			return true;
		}
		if ( $this->dead ) {
			return false;
		}
		if ( empty( $this->cfg['enabled'] ) ) {
			$this->fail( 'cache disabled by configuration' );
			return false;
		}

		// JAB-89: cross-request circuit breaker. If a recent request already
		// found Redis down, skip the connect attempt (and its timeout) and go
		// straight to runtime-only caching until the breaker expires. Bypassed
		// by force_probe() for diagnostics so `verify` always tests live.
		if ( ! $this->force_probe && $this->breaker_is_open() ) {
			$this->fail( 'redis unavailable (circuit breaker open): ' . $this->breaker_last_error() );
			return false;
		}

		$ok = false;
		if ( class_exists( 'Redis' ) ) {
			// Fall through to pure-PHP only if phpredis is present but failed
			// for a non-protocol reason — usually it just means unavailable,
			// in which case pure-PHP will fail too. Try anyway; it is cheap.
			$ok = $this->connect_phpredis();
		}
		if ( ! $ok ) {
			$ok = $this->connect_pure();
		}

		if ( $ok ) {
			// Recovery self-heals immediately on the first successful connect,
			// not only after the breaker TTL.
			$this->breaker_clear();
			return true;
		}
		// Both drivers failed → trip the breaker so the next requests fail fast.
		$this->breaker_open( $this->last_error );
		return false;
	}

	/** Storage key for this client's breaker, unique per socket/host + DB. */
	private function breaker_key() {
		return 'jabali_cache_cb:' . md5(
			$this->cfg['scheme'] . '|' . $this->cfg['socket'] . '|' .
			$this->cfg['host'] . '|' . (int) $this->cfg['port'] . '|' . (int) $this->cfg['database']
		);
	}

	/** @return bool true while Redis is marked down. */
	private function breaker_is_open() {
		$st = $this->shared_get( $this->breaker_key() );
		return is_array( $st ) && isset( $st['until'] ) && (int) $st['until'] > time();
	}

	/** @return string last error recorded when the breaker tripped. */
	private function breaker_last_error() {
		$st = $this->shared_get( $this->breaker_key() );
		return ( is_array( $st ) && isset( $st['err'] ) ) ? (string) $st['err'] : '';
	}

	/**
	 * Trip the breaker for CIRCUIT_TTL_BASE + jitter seconds.
	 *
	 * @param string $err
	 */
	private function breaker_open( $err ) {
		$ttl = self::CIRCUIT_TTL_BASE + mt_rand( 0, self::CIRCUIT_TTL_JITTER );
		$this->shared_set(
			$this->breaker_key(),
			array( 'until' => time() + $ttl, 'err' => (string) $err ),
			$ttl
		);
	}

	/** Clear the breaker (Redis is healthy again). */
	private function breaker_clear() {
		$this->shared_del( $this->breaker_key() );
	}

	/**
	 * Shared cross-request KV store used by the circuit breaker (JAB-89) and
	 * the stats-count cache (JAB-94). APCu when available (shared across the
	 * pool's FPM workers, cheap in the early object-cache drop-in); otherwise a
	 * small JSON file under wp-content/cache/jabali-cache. When neither is
	 * available the caller degrades to per-request behaviour (still correct).
	 *
	 * @param string $key
	 * @return array<string,mixed>|null
	 */
	private function shared_get( $key ) {
		if ( function_exists( 'apcu_enabled' ) && apcu_enabled() ) {
			$ok = false;
			$v  = apcu_fetch( $key, $ok );
			return ( $ok && is_array( $v ) ) ? $v : null;
		}
		$f = $this->shared_file( $key );
		if ( '' !== $f && is_file( $f ) ) {
			$raw = @file_get_contents( $f ); // phpcs:ignore
			$v   = $raw ? json_decode( $raw, true ) : null;
			// The file fallback has no native TTL — honour an embedded expiry.
			if ( is_array( $v ) && isset( $v['__exp'] ) && (int) $v['__exp'] < time() ) {
				@unlink( $f ); // phpcs:ignore
				return null;
			}
			return is_array( $v ) ? $v : null;
		}
		return null;
	}

	/**
	 * @param string              $key
	 * @param array<string,mixed> $val
	 * @param int                 $ttl
	 */
	private function shared_set( $key, array $val, $ttl ) {
		if ( function_exists( 'apcu_enabled' ) && apcu_enabled() ) {
			@apcu_store( $key, $val, (int) $ttl + 5 ); // phpcs:ignore
			return;
		}
		$f = $this->shared_file( $key );
		if ( '' !== $f ) {
			$val['__exp'] = time() + (int) $ttl; // embed expiry for the fileless-TTL fallback.
			@file_put_contents( $f, json_encode( $val ), LOCK_EX ); // phpcs:ignore
		}
	}

	/** @param string $key */
	private function shared_del( $key ) {
		if ( function_exists( 'apcu_enabled' ) && apcu_enabled() ) {
			@apcu_delete( $key ); // phpcs:ignore
			return;
		}
		$f = $this->shared_file( $key );
		if ( '' !== $f && is_file( $f ) ) {
			@unlink( $f ); // phpcs:ignore
		}
	}

	/**
	 * File path backing shared_* for one key, or '' when no writable cache dir
	 * is available.
	 *
	 * @param string $key
	 * @return string
	 */
	private function shared_file( $key ) {
		if ( ! defined( 'WP_CONTENT_DIR' ) ) {
			return '';
		}
		$dir = WP_CONTENT_DIR . '/cache/jabali-cache';
		if ( ! is_dir( $dir ) ) {
			@mkdir( $dir, 0755, true ); // phpcs:ignore
		}
		if ( ! is_dir( $dir ) || ! is_writable( $dir ) ) {
			return '';
		}
		return $dir . '/kv-' . md5( $key ) . '.json';
	}

	/**
	 * @return bool
	 */
	private function connect_phpredis() {
		try {
			$redis   = new \Redis();
			$timeout = (float) $this->cfg['timeout'];
			// GH #606: persistent connection — PHP-FPM keeps the Redis socket
			// open across requests, so steady-state traffic skips the connect
			// handshake. The persistent_id is keyed on the ACL user + DB so a
			// pooled connection is never reused across tenants or logical DBs
			// (jabali runs per-user FPM pools, so this is same-tenant already —
			// the id is belt-and-braces).
			// JAB-96: persistent (pconnect) by default so PHP-FPM keeps the
			// socket across requests; operators can disable it with the
			// persistent_connections config flag (then close() severs normally).
			$persistent = ! isset( $this->cfg['persistent_connections'] ) || $this->cfg['persistent_connections'];
			$pid = 'jc:' . ( ! empty( $this->cfg['username'] ) ? $this->cfg['username'] : 'default' ) . ':' . (int) $this->cfg['database'];
			if ( 'unix' === $this->cfg['scheme'] ) {
				$ok = $persistent
					? $redis->pconnect( $this->cfg['socket'], 0, $timeout, $pid )
					: $redis->connect( $this->cfg['socket'], 0, $timeout );
			} else {
				$ok = $persistent
					? $redis->pconnect( $this->cfg['host'], (int) $this->cfg['port'], $timeout, $pid )
					: $redis->connect( $this->cfg['host'], (int) $this->cfg['port'], $timeout );
			}
			if ( ! $ok ) {
				return false;
			}
			$this->persistent = $persistent;
			if ( ! empty( $this->cfg['password'] ) ) {
				// Redis 6+ ACL needs AUTH <user> <pass>; fall back to legacy single-arg.
				if ( ! empty( $this->cfg['username'] ) ) {
					$redis->auth( array( $this->cfg['username'], $this->cfg['password'] ) );
				} else {
					$redis->auth( $this->cfg['password'] );
				}
			}
			$redis->select( (int) $this->cfg['database'] );
			$this->redis     = $redis;
			$this->driver    = 'phpredis';
			$this->connected = true;
			return true;
		} catch ( \Throwable $e ) {
			// JAB-96: an auth/select failure after pconnect leaves a bad socket
			// in the persistent pool — force-close it so the next request never
			// reuses a half-open / unauthed connection.
			if ( isset( $redis ) && $redis instanceof \Redis ) {
				try {
					$redis->close();
				} catch ( \Throwable $e2 ) { // phpcs:ignore
					unset( $e2 );
				}
			}
			$m                = $e->getMessage();
			$this->last_error = 'phpredis: ' . $m;
			if ( false !== stripos( $m, 'permission denied' ) ) {
				$this->last_error .= ' — PHP-FPM user likely missing from the jabali-redis-clients group (the Jabali panel re-adds it automatically; retry shortly).';
			}
			return false;
		}
	}

	/**
	 * @return bool
	 */
	private function connect_pure() {
		$timeout = (float) $this->cfg['timeout'];
		if ( 'unix' === $this->cfg['scheme'] ) {
			$target = 'unix://' . $this->cfg['socket'];
		} else {
			$target = 'tcp://' . $this->cfg['host'] . ':' . (int) $this->cfg['port'];
		}
		$errno  = 0;
		$errstr = '';
		// @ to keep open_basedir / connection warnings out of the page; the
		// boolean result and graceful-disable path handle the failure.
		$stream = @stream_socket_client( $target, $errno, $errstr, $timeout ); // phpcs:ignore
		if ( ! $stream ) {
			$msg = sprintf( 'connect %s failed: %s (%d)', $target, $errstr, $errno );
			// A socket-level EACCES means the PHP-FPM user isn't in the
			// jabali-redis-clients group (migrated/reprovisioned accounts). The
			// panel reconciler now re-adds it automatically (user.slice.ensure)
			// and restarts the pool, so this self-resolves within a tick.
			if ( 13 === (int) $errno || false !== stripos( (string) $errstr, 'permission denied' ) ) {
				$msg .= ' — the PHP-FPM user is likely missing from the "jabali-redis-clients" group; the Jabali panel re-adds it automatically and restarts the pool, so retry shortly.';
			}
			$this->fail( $msg );
			return false;
		}
		stream_set_timeout( $stream, (int) $timeout, (int) ( ( $timeout - (int) $timeout ) * 1e6 ) );
		$this->stream = $stream;
		$this->driver = 'pure-php';

		if ( ! empty( $this->cfg['password'] ) ) {
			$jabali_auth = ! empty( $this->cfg['username'] )
				? array( 'AUTH', $this->cfg['username'], $this->cfg['password'] )
				: array( 'AUTH', $this->cfg['password'] );
			// Redis replies +OK to AUTH, which read_reply() returns as the string
			// "OK" (not bool true). A wrong cred yields -WRONGPASS -> null.
			if ( 'OK' !== $this->cmd( $jabali_auth ) ) {
				$this->fail( 'AUTH rejected' );
				return false;
			}
		}
		$sel = $this->cmd( array( 'SELECT', (string) (int) $this->cfg['database'] ) );
		if ( 'OK' !== $sel && true !== $sel ) {
			// An -ERR/-NOPERM reply must be fatal: continuing would silently
			// operate on the connection's default DB 0 (the panel's) instead of ours.
			$this->fail( 'SELECT ' . (int) $this->cfg['database'] . ' rejected: ' . $this->last_error );
			return false;
		}
		$pong = $this->cmd( array( 'PING' ) );
		if ( 'PONG' !== $pong && true !== $pong ) {
			$this->fail( 'PING did not return PONG' );
			return false;
		}
		$this->connected = true;
		return true;
	}

	private function fail( $msg ) {
		$this->last_error = $msg;
		$this->dead       = true;
		$this->connected  = false;
		if ( is_resource( $this->stream ) ) {
			@fclose( $this->stream ); // phpcs:ignore
		}
		$this->stream = null;
		$this->redis  = null;
	}

	/**
	 * GET. Returns the raw string value or false on miss/error.
	 *
	 * @param string $key
	 * @return string|false
	 */
	public function get( $key ) {
		if ( ! $this->is_connected() ) {
			return false;
		}
		if ( 'phpredis' === $this->driver ) {
			try {
				return $this->redis->get( $key );
			} catch ( \Throwable $e ) {
				$this->fail( $e->getMessage() );
				return false;
			}
		}
		$res = $this->cmd( array( 'GET', $key ) );
		return ( null === $res ) ? false : $res;
	}

	/**
	 * MGET. Returns a map key => value|false (false = miss).
	 *
	 * @param string[] $keys
	 * @return array<string,string|false>
	 */
	public function mget( array $keys ) {
		$out = array();
		if ( empty( $keys ) || ! $this->is_connected() ) {
			foreach ( $keys as $k ) {
				$out[ $k ] = false;
			}
			return $out;
		}
		if ( 'phpredis' === $this->driver ) {
			try {
				$vals = $this->redis->mget( array_values( $keys ) );
			} catch ( \Throwable $e ) {
				$this->fail( $e->getMessage() );
				$vals = array();
			}
			$i = 0;
			foreach ( $keys as $k ) {
				$out[ $k ] = isset( $vals[ $i ] ) ? $vals[ $i ] : false;
				$i++;
			}
			return $out;
		}
		$args = array( 'MGET' );
		foreach ( $keys as $k ) {
			$args[] = $k;
		}
		$vals = $this->cmd( $args );
		$i    = 0;
		foreach ( $keys as $k ) {
			$v         = ( is_array( $vals ) && array_key_exists( $i, $vals ) && null !== $vals[ $i ] ) ? $vals[ $i ] : false;
			$out[ $k ] = $v;
			$i++;
		}
		return $out;
	}

	/**
	 * SET with optional TTL (seconds). Returns bool.
	 *
	 * @param string $key
	 * @param string $value
	 * @param int    $ttl
	 * @return bool
	 */
	public function set( $key, $value, $ttl = 0, $keepttl = false ) {
		if ( ! $this->is_connected() ) {
			return false;
		}
		if ( 'phpredis' === $this->driver ) {
			try {
				if ( $ttl > 0 ) {
					return (bool) $this->redis->setex( $key, $ttl, $value );
				}
				// KEEPTTL (Redis 6.2+): rewrite the value without clearing an
				// existing expiry (GH #604 incr/decr must not immortalise a key).
				if ( $keepttl ) {
					return (bool) $this->redis->set( $key, $value, array( 'KEEPTTL' => true ) );
				}
				return (bool) $this->redis->set( $key, $value );
			} catch ( \Throwable $e ) {
				$this->fail( $e->getMessage() );
				return false;
			}
		}
		if ( $ttl > 0 ) {
			$res = $this->cmd( array( 'SETEX', $key, (string) $ttl, $value ) );
		} elseif ( $keepttl ) {
			$res = $this->cmd( array( 'SET', $key, $value, 'KEEPTTL' ) );
		} else {
			$res = $this->cmd( array( 'SET', $key, $value ) );
		}
		return ( true === $res || 'OK' === $res );
	}

	/**
	 * Conditional SET (NX) — used by add(). Returns true only if stored.
	 *
	 * @param string $key
	 * @param string $value
	 * @param int    $ttl
	 * @return bool
	 */
	public function add( $key, $value, $ttl = 0 ) {
		if ( ! $this->is_connected() ) {
			return false;
		}
		if ( 'phpredis' === $this->driver ) {
			try {
				$opts = array( 'nx' );
				if ( $ttl > 0 ) {
					$opts = array( 'nx', 'ex' => $ttl );
				}
				return (bool) $this->redis->set( $key, $value, $opts );
			} catch ( \Throwable $e ) {
				$this->fail( $e->getMessage() );
				return false;
			}
		}
		$args = array( 'SET', $key, $value, 'NX' );
		if ( $ttl > 0 ) {
			$args[] = 'EX';
			$args[] = (string) $ttl;
		}
		$res = $this->cmd( $args );
		return ( true === $res || 'OK' === $res );
	}

	/**
	 * Pipeline a batch of raw RESP commands: write them all, then read one
	 * reply per command in order (GH #607). read_reply() frames exactly one
	 * reply, so N sequential reads consume N replies. Pure-PHP path only.
	 *
	 * @param array<int,array<int,string>> $commands
	 * @return array<int,mixed>|null replies in order, or null on write failure.
	 */
	private function pipeline_resp( array $commands ) {
		if ( ! is_resource( $this->stream ) || empty( $commands ) ) {
			return array();
		}
		$payload = '';
		foreach ( $commands as $args ) {
			$payload .= '*' . count( $args ) . "\r\n";
			foreach ( $args as $a ) {
				$a        = (string) $a;
				$payload .= '$' . strlen( $a ) . "\r\n" . $a . "\r\n";
			}
		}
		if ( false === $this->write( $payload ) ) {
			$this->fail( 'pipeline write failed' );
			return null;
		}
		$out = array();
		foreach ( $commands as $_ ) {
			$out[] = $this->read_reply();
			if ( $this->dead ) {
				return $out;
			}
		}
		return $out;
	}

	/**
	 * Batch SET with per-item TTL (GH #607). $items is a list of
	 * [key, value, ttl] ; ttl<=0 means no expiry. Returns a list of bool
	 * (stored?) in the same order.
	 *
	 * @param array<int,array{0:string,1:string,2:int}> $items
	 * @return array<int,bool>
	 */
	public function set_many( array $items ) {
		if ( ! $this->is_connected() || empty( $items ) ) {
			return array();
		}
		if ( 'phpredis' === $this->driver ) {
			try {
				$pipe = $this->redis->multi( \Redis::PIPELINE );
				foreach ( $items as $it ) {
					if ( $it[2] > 0 ) {
						$pipe->setEx( $it[0], (int) $it[2], $it[1] );
					} else {
						$pipe->set( $it[0], $it[1] );
					}
				}
				$res = $pipe->exec();
				$out = array();
				foreach ( (array) $res as $r ) {
					$out[] = (bool) $r;
				}
				return $out;
			} catch ( \Throwable $e ) {
				$this->fail( $e->getMessage() );
				return array();
			}
		}
		$cmds = array();
		foreach ( $items as $it ) {
			$cmds[] = ( $it[2] > 0 )
				? array( 'SETEX', $it[0], (string) (int) $it[2], $it[1] )
				: array( 'SET', $it[0], $it[1] );
		}
		$replies = $this->pipeline_resp( $cmds );
		$out     = array();
		foreach ( (array) $replies as $r ) {
			$out[] = ( true === $r || 'OK' === $r );
		}
		return $out;
	}

	/**
	 * Batch conditional SET (NX) with per-item TTL (GH #607). Same shape as
	 * set_many; each returns true only if the key did not exist.
	 *
	 * @param array<int,array{0:string,1:string,2:int}> $items
	 * @return array<int,bool>
	 */
	public function add_many( array $items ) {
		if ( ! $this->is_connected() || empty( $items ) ) {
			return array();
		}
		if ( 'phpredis' === $this->driver ) {
			try {
				$pipe = $this->redis->multi( \Redis::PIPELINE );
				foreach ( $items as $it ) {
					$opts = ( $it[2] > 0 ) ? array( 'nx', 'ex' => (int) $it[2] ) : array( 'nx' );
					$pipe->set( $it[0], $it[1], $opts );
				}
				$res = $pipe->exec();
				$out = array();
				foreach ( (array) $res as $r ) {
					$out[] = (bool) $r;
				}
				return $out;
			} catch ( \Throwable $e ) {
				$this->fail( $e->getMessage() );
				return array();
			}
		}
		$cmds = array();
		foreach ( $items as $it ) {
			$c = array( 'SET', $it[0], $it[1], 'NX' );
			if ( $it[2] > 0 ) {
				$c[] = 'EX';
				$c[] = (string) (int) $it[2];
			}
			$cmds[] = $c;
		}
		$replies = $this->pipeline_resp( $cmds );
		$out     = array();
		foreach ( (array) $replies as $r ) {
			// SET NX returns OK/true when stored, null when the key existed.
			$out[] = ( true === $r || 'OK' === $r );
		}
		return $out;
	}

	/**
	 * @param string|string[] $keys
	 * @return int number of keys deleted.
	 */
	public function del( $keys ) {
		if ( ! $this->is_connected() ) {
			return 0;
		}
		$keys = is_array( $keys ) ? array_values( $keys ) : array( $keys );
		if ( empty( $keys ) ) {
			return 0;
		}
		if ( 'phpredis' === $this->driver ) {
			try {
				return (int) $this->redis->del( $keys );
			} catch ( \Throwable $e ) {
				$this->fail( $e->getMessage() );
				return 0;
			}
		}
		$args = array_merge( array( 'DEL' ), $keys );
		$res  = $this->cmd( $args );
		return is_int( $res ) ? $res : 0;
	}

	/**
	 * UNLINK is DEL that reclaims memory on a background thread (Redis 4+),
	 * so a large flush/purge doesn't block the Redis event loop and spike
	 * latency for every other tenant on the shared instance (GH #608).
	 * Semantically identical to del() for the caller.
	 *
	 * @param string|array<int,string> $keys
	 * @return int number of keys unlinked.
	 */
	public function unlink( $keys ) {
		if ( ! $this->is_connected() ) {
			return 0;
		}
		$keys = is_array( $keys ) ? array_values( $keys ) : array( $keys );
		if ( empty( $keys ) ) {
			return 0;
		}
		if ( 'phpredis' === $this->driver ) {
			try {
				return (int) $this->redis->unlink( $keys );
			} catch ( \Throwable $e ) {
				$this->fail( $e->getMessage() );
				return 0;
			}
		}
		$args = array_merge( array( 'UNLINK' ), $keys );
		$res  = $this->cmd( $args );
		return is_int( $res ) ? $res : 0;
	}

	/**
	 * @param string $key
	 * @param int    $by
	 * @return int|false new value or false.
	 */
	public function incr( $key, $by = 1 ) {
		if ( ! $this->is_connected() ) {
			return false;
		}
		if ( 'phpredis' === $this->driver ) {
			try {
				return $this->redis->incrBy( $key, $by );
			} catch ( \Throwable $e ) {
				$this->fail( $e->getMessage() );
				return false;
			}
		}
		$res = $this->cmd( array( 'INCRBY', $key, (string) $by ) );
		return is_int( $res ) ? $res : false;
	}

	/**
	 * @param string $key
	 * @param int    $by
	 * @return int|false
	 */
	public function decr( $key, $by = 1 ) {
		if ( ! $this->is_connected() ) {
			return false;
		}
		if ( 'phpredis' === $this->driver ) {
			try {
				return $this->redis->decrBy( $key, $by );
			} catch ( \Throwable $e ) {
				$this->fail( $e->getMessage() );
				return false;
			}
		}
		$res = $this->cmd( array( 'DECRBY', $key, (string) $by ) );
		return is_int( $res ) ? $res : false;
	}

	/**
	 * Delete every key matching a glob-style pattern, using SCAN to avoid a
	 * blocking KEYS sweep. NEVER use FLUSHDB — DB 1 is shared across tenants.
	 *
	 * @param string $pattern
	 * @return int number of keys deleted.
	 */
	public function delete_by_pattern( $pattern ) {
		if ( ! $this->is_connected() ) {
			return 0;
		}
		$deleted = 0;
		if ( 'phpredis' === $this->driver ) {
			try {
				$it = null;
				$this->redis->setOption( \Redis::OPT_SCAN, \Redis::SCAN_RETRY );
				while ( false !== ( $keys = $this->redis->scan( $it, $pattern, 500 ) ) ) {
					if ( ! empty( $keys ) ) {
						$deleted += (int) $this->redis->unlink( $keys );
					}
					if ( 0 === (int) $it ) {
						break;
					}
				}
			} catch ( \Throwable $e ) {
				$this->fail( $e->getMessage() );
			}
			return $deleted;
		}
		$cursor = '0';
		do {
			$res = $this->cmd( array( 'SCAN', $cursor, 'MATCH', $pattern, 'COUNT', '500' ) );
			if ( ! is_array( $res ) || count( $res ) < 2 ) {
				break;
			}
			$cursor = (string) $res[0];
			$batch  = is_array( $res[1] ) ? $res[1] : array();
			if ( ! empty( $batch ) ) {
				$deleted += $this->unlink( $batch );
			}
		} while ( '0' !== $cursor && ! $this->dead );
		return $deleted;
	}

	/**
	 * @return string raw INFO output (or '' on failure).
	 */
	public function info() {
		if ( ! $this->is_connected() ) {
			return '';
		}
		if ( 'phpredis' === $this->driver ) {
			try {
				$info = $this->redis->info();
				$out  = '';
				// phpredis 6.x can return nested section arrays; flatten one level
				// so keyspace_hits / used_memory etc. are always top-level lines.
				foreach ( (array) $info as $k => $v ) {
					if ( is_array( $v ) ) {
						foreach ( $v as $k2 => $v2 ) {
							if ( ! is_array( $v2 ) ) {
								$out .= $k2 . ':' . $v2 . "\n";
							}
						}
						continue;
					}
					$out .= $k . ':' . $v . "\n";
				}
				return $out;
			} catch ( \Throwable $e ) {
				$this->fail( $e->getMessage() );
				return '';
			}
		}
		$res = $this->cmd( array( 'INFO' ) );
		return is_string( $res ) ? $res : '';
	}

	/**
	 * @return int number of keys in the current DB matching our prefix.
	 */
	/**
	 * Count keys under $prefix via SCAN.
	 *
	 * JAB-94: pass $max_steps > 0 to bound the scan to that many SCAN
	 * iterations — on a huge, shared keyspace an unbounded full scan on every
	 * UI poll creates host-wide latency. When the cap is hit before the cursor
	 * wraps, the returned count is a floor and last_count_was_approx() is true.
	 * $max_steps = 0 (default) keeps the exact, unbounded behaviour for
	 * diagnostics.
	 *
	 * @param string $prefix
	 * @param int    $max_steps
	 * @return int
	 */
	public function count_keys( $prefix, $max_steps = 0 ) {
		$this->last_count_approx = false;
		if ( ! $this->is_connected() ) {
			return 0;
		}
		$count   = 0;
		$pattern = $prefix . '*';
		$cursor  = '0';
		$steps   = 0;
		if ( 'phpredis' === $this->driver ) {
			try {
				// SCAN_RETRY: without it scan() returns false on an EMPTY iteration
				// and the loop exits early with an undercount. (pconnect pools the
				// handle, so never rely on another method having set this option.)
				$this->redis->setOption( \Redis::OPT_SCAN, \Redis::SCAN_RETRY );
				$it = null;
				while ( false !== ( $keys = $this->redis->scan( $it, $pattern, 1000 ) ) ) {
					$count += count( $keys );
					++$steps;
					if ( $max_steps > 0 && $steps >= $max_steps ) {
						if ( 0 !== (int) $it ) {
							$this->last_count_approx = true;
						}
						break;
					}
					if ( 0 === (int) $it ) {
						break;
					}
				}
			} catch ( \Throwable $e ) {
				$this->fail( $e->getMessage() );
			}
			return $count;
		}
		do {
			$res = $this->cmd( array( 'SCAN', $cursor, 'MATCH', $pattern, 'COUNT', '1000' ) );
			if ( ! is_array( $res ) || count( $res ) < 2 ) {
				break;
			}
			$cursor = (string) $res[0];
			$count += is_array( $res[1] ) ? count( $res[1] ) : 0;
			++$steps;
			if ( $max_steps > 0 && $steps >= $max_steps ) {
				if ( '0' !== $cursor ) {
					$this->last_count_approx = true;
				}
				break;
			}
		} while ( '0' !== $cursor && ! $this->dead );
		return $count;
	}

	/** @return bool whether the last count_keys() was cut short (JAB-94). */
	public function last_count_was_approx() {
		return $this->last_count_approx;
	}

	/**
	 * Key count for the UI/stats path: short-TTL cached + bounded scan (JAB-94).
	 * Repeated UI polls read the cached value instead of re-scanning the whole
	 * shared keyspace each time; a cold read is capped at STATS_SCAN_STEPS.
	 * Returns array( 'count' => int, 'approx' => bool, 'cached' => bool ).
	 *
	 * @param string $prefix
	 * @return array{count:int,approx:bool,cached:bool}
	 */
	public function stats_key_count( $prefix ) {
		$ck  = 'jabali_cache_kc:' . md5( (string) $prefix );
		$hit = $this->shared_get( $ck );
		if ( is_array( $hit ) && isset( $hit['count'] ) ) {
			return array( 'count' => (int) $hit['count'], 'approx' => ! empty( $hit['approx'] ), 'cached' => true );
		}
		$count  = $this->count_keys( $prefix, self::STATS_SCAN_STEPS );
		$approx = $this->last_count_was_approx();
		$this->shared_set( $ck, array( 'count' => $count, 'approx' => $approx ), self::STATS_CACHE_TTL );
		return array( 'count' => $count, 'approx' => $approx, 'cached' => false );
	}

	/**
	 * Trim this site's prefix to at most $maxKeys keys, deleting the excess
	 * (GH #612). Redis has no per-USER maxmemory on a shared instance, so this
	 * is the applied per-tenant footprint control: bound the key count (a proxy
	 * for memory). The shared instance's allkeys-lru still handles fine-grained
	 * eviction; this stops one site's object cache from dominating the DB.
	 * Bounded collect (100k) so the SCAN itself can't run away. Returns deleted.
	 *
	 * @param string $prefix
	 * @param int    $maxKeys
	 * @return int
	 */
	public function trim_to_budget( $prefix, $maxKeys ) {
		if ( ! $this->is_connected() || $maxKeys <= 0 ) {
			return 0;
		}
		$pattern = $prefix . '*';
		$keys    = array();
		if ( 'phpredis' === $this->driver ) {
			try {
				$this->redis->setOption( \Redis::OPT_SCAN, \Redis::SCAN_RETRY ); // see count_keys().
				$it = null;
				while ( false !== ( $batch = $this->redis->scan( $it, $pattern, 1000 ) ) ) {
					foreach ( $batch as $k ) {
						$keys[] = $k;
					}
					if ( count( $keys ) >= $maxKeys + self::TRIM_MAX_PER_RUN || 0 === (int) $it ) {
						break;
					}
				}
			} catch ( \Throwable $e ) {
				$this->fail( $e->getMessage() );
				return 0;
			}
		} else {
			$cursor = '0';
			do {
				$res = $this->cmd( array( 'SCAN', $cursor, 'MATCH', $pattern, 'COUNT', '1000' ) );
				if ( ! is_array( $res ) || count( $res ) < 2 ) {
					break;
				}
				$cursor = (string) $res[0];
				if ( is_array( $res[1] ) ) {
					foreach ( $res[1] as $k ) {
						$keys[] = $k;
					}
				}
			} while ( '0' !== $cursor && ! $this->dead && count( $keys ) < $maxKeys + self::TRIM_MAX_PER_RUN );
		}
		$n = count( $keys );
		if ( $n <= $maxKeys ) {
			return 0;
		}
		$excess  = array_slice( $keys, 0, $n - $maxKeys );
		$removed = 0;
		// UNLINK in bounded chunks (GH #608): async memory reclaim, and no
		// single mega-command against the shared instance.
		foreach ( array_chunk( $excess, 1000 ) as $chunk ) {
			$removed += (int) $this->unlink( $chunk );
		}
		return $removed;
	}

	public function close( $force = false ) {
		if ( 'phpredis' === $this->driver && $this->redis ) {
			// JAB-96: don't sever a persistent (pconnect) socket on a normal
			// wp_cache_close() — that would defeat pooling across FPM requests.
			// Only close when non-persistent or forced (e.g. a bad connection).
			if ( ! $this->persistent || $force ) {
				try {
					$this->redis->close();
				} catch ( \Throwable $e ) {
					// ignore.
				}
			}
		}
		if ( is_resource( $this->stream ) ) {
			@fclose( $this->stream ); // phpcs:ignore
		}
		$this->stream     = null;
		$this->redis      = null;
		$this->connected  = false;
		$this->persistent = false;
	}

	// ---------------------------------------------------------------------
	// Pure-PHP RESP protocol.
	// ---------------------------------------------------------------------

	/**
	 * Send one command (array of arguments) and read one reply.
	 *
	 * @param string[] $args
	 * @return mixed string|int|array|true|null  (null = nil reply / error)
	 */
	private function cmd( array $args ) {
		if ( ! is_resource( $this->stream ) ) {
			return null;
		}
		$payload = '*' . count( $args ) . "\r\n";
		foreach ( $args as $a ) {
			$a        = (string) $a;
			$payload .= '$' . strlen( $a ) . "\r\n" . $a . "\r\n";
		}
		if ( false === $this->write( $payload ) ) {
			$this->fail( 'write failed' );
			return null;
		}
		return $this->read_reply();
	}

	private function write( $buf ) {
		$len     = strlen( $buf );
		$written = 0;
		while ( $written < $len ) {
			$n = @fwrite( $this->stream, substr( $buf, $written ) ); // phpcs:ignore
			if ( false === $n || 0 === $n ) {
				$meta = stream_get_meta_data( $this->stream );
				if ( ! empty( $meta['timed_out'] ) ) {
					return false;
				}
				return false;
			}
			$written += $n;
		}
		return true;
	}

	/**
	 * @return mixed
	 */
	private function read_reply() {
		$line = $this->read_line();
		if ( false === $line || '' === $line ) {
			$this->fail( 'empty reply' );
			return null;
		}
		$type    = $line[0];
		$payload = substr( $line, 1 );
		switch ( $type ) {
			case '+': // simple string.
				return ( 'PONG' === $payload ) ? 'PONG' : $payload;
			case '-': // error.
				$this->last_error = 'redis error: ' . $payload;
				return null;
			case ':': // integer.
				return (int) $payload;
			case '$': // bulk string.
				$len = (int) $payload;
				if ( -1 === $len ) {
					return null;
				}
				$data = $this->read_bytes( $len + 2 ); // include trailing CRLF.
				if ( false === $data ) {
					$this->fail( 'short bulk read' );
					return null;
				}
				return substr( $data, 0, $len );
			case '*': // array.
				$count = (int) $payload;
				if ( -1 === $count ) {
					return null;
				}
				$arr = array();
				for ( $i = 0; $i < $count; $i++ ) {
					$arr[] = $this->read_reply();
					if ( $this->dead ) {
						return null;
					}
				}
				return $arr;
			default:
				$this->fail( 'unknown reply type: ' . $type );
				return null;
		}
	}

	private function read_line() {
		$line = @fgets( $this->stream ); // phpcs:ignore
		if ( false === $line ) {
			return false;
		}
		return rtrim( $line, "\r\n" );
	}

	private function read_bytes( $n ) {
		$buf = '';
		while ( strlen( $buf ) < $n ) {
			$chunk = @fread( $this->stream, $n - strlen( $buf ) ); // phpcs:ignore
			if ( false === $chunk || '' === $chunk ) {
				$meta = stream_get_meta_data( $this->stream );
				if ( ! empty( $meta['timed_out'] ) ) {
					return false;
				}
				return false;
			}
			$buf .= $chunk;
		}
		return $buf;
	}
}
