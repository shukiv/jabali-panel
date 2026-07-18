<?php
/**
 * Jabali Cache — persistent object cache engine.
 *
 * Implements the WordPress object-cache contract on top of
 * Jabali_Cache_Client. Instantiated by the wp-content/object-cache.php
 * drop-in as $GLOBALS['wp_object_cache'].
 *
 * Failure model: if Redis is unreachable the engine still works as a
 * per-request in-memory cache (exactly what core's default cache does), so a
 * down/blocked Redis never breaks the site — it only removes persistence.
 *
 * @package Jabali_Cache
 */

defined( 'ABSPATH' ) || exit; // wp.org: prevent direct file access.

if ( ! class_exists( 'Jabali_Cache_Client' ) ) {
	require_once __DIR__ . '/lib.php';
}

if ( class_exists( 'Jabali_Cache_Object_Cache' ) ) {
	return;
}

class Jabali_Cache_Object_Cache {

	/** @var array<string,array<string,mixed>> runtime (per-request) cache. */
	private $cache = array();

	/** @var Jabali_Cache_Client */
	private $client;

	/** @var array<string,mixed> */
	private $cfg;

	/** @var string */
	private $prefix;

	/** @var int */
	private $maxttl = 0;

	/** @var bool */
	public $is_persistent = false;

	/** @var int */
	public $cache_hits = 0;

	/** @var int */
	public $cache_misses = 0;

	/** @var int */
	public $redis_calls = 0;

	/** @var array<string,bool> groups that are shared across all sites in a network. */
	private $global_groups = array();

	/** @var array<string,bool> groups that must never hit Redis. */
	private $non_persistent_groups = array();

	/** @var int */
	private $blog_prefix = 0;

	/** @var bool */
	private $multisite = false;

	/** @var string 'igbinary' | 'php' */
	private $serializer = 'php';

	public function __construct() {
		$this->cfg    = Jabali_Cache_Config::load();
		$this->prefix = $this->cfg['prefix'];
		$this->maxttl = max( 0, (int) $this->cfg['maxttl'] );

		$serializer = isset( $this->cfg['serializer'] ) ? $this->cfg['serializer'] : 'auto';
		if ( 'igbinary' === $serializer || ( 'auto' === $serializer && function_exists( 'igbinary_serialize' ) ) ) {
			$this->serializer = 'igbinary';
		}

		foreach ( (array) $this->cfg['global_groups'] as $g ) {
			$this->global_groups[ $g ] = true;
		}
		foreach ( (array) $this->cfg['ignored_groups'] as $g ) {
			$this->non_persistent_groups[ $g ] = true;
		}

		$this->multisite   = ( function_exists( 'is_multisite' ) && is_multisite() );
		$this->blog_prefix = $this->multisite ? (int) get_current_blog_id() : 1;

		$this->client = new Jabali_Cache_Client( $this->cfg );
		// Connection is lazy — only dial Redis when a persistent op needs it.
	}

	// ------------------------------------------------------------------
	// WordPress object-cache API.
	// ------------------------------------------------------------------

	/**
	 * @param int|string $key
	 * @param mixed      $data
	 * @param string     $group
	 * @param int        $expire
	 * @return bool
	 */
	public function add( $key, $data, $group = 'default', $expire = 0 ) {
		if ( function_exists( 'wp_suspend_cache_addition' ) && wp_suspend_cache_addition() ) {
			return false;
		}
		$id = $this->key( $key, $group );
		if ( isset( $this->cache[ $group ][ $id ] ) ) {
			return false;
		}
		if ( $this->is_non_persistent( $group ) ) {
			$this->cache[ $group ][ $id ] = is_object( $data ) ? clone $data : $data;
			return true;
		}
		if ( $this->ensure() ) {
			$this->redis_calls++;
			if ( ! $this->client->add( $id, $this->serialize( $data ), $this->ttl( $expire ) ) ) {
				return false;
			}
		}
		$this->cache[ $group ][ $id ] = is_object( $data ) ? clone $data : $data;
		return true;
	}

	/**
	 * @param int|string $key
	 * @param mixed      $data
	 * @param string     $group
	 * @param int        $expire
	 * @return bool
	 */
	public function set( $key, $data, $group = 'default', $expire = 0 ) {
		$id                           = $this->key( $key, $group );
		$this->cache[ $group ][ $id ] = is_object( $data ) ? clone $data : $data;

		if ( $this->is_non_persistent( $group ) ) {
			return true;
		}
		if ( $this->ensure() ) {
			$this->redis_calls++;
			return $this->client->set( $id, $this->serialize( $data ), $this->ttl( $expire ) );
		}
		return true; // runtime-only when Redis is unavailable.
	}

	/**
	 * @param int|string $key
	 * @param mixed      $data
	 * @param string     $group
	 * @param int        $expire
	 * @return bool
	 */
	public function replace( $key, $data, $group = 'default', $expire = 0 ) {
		$id = $this->key( $key, $group );
		if ( ! $this->has( $key, $group ) ) {
			return false;
		}
		return $this->set( $key, $data, $group, $expire );
	}

	/**
	 * @param int|string $key
	 * @param string     $group
	 * @param bool       $force
	 * @param bool       $found
	 * @return mixed
	 */
	public function get( $key, $group = 'default', $force = false, &$found = null ) {
		$id = $this->key( $key, $group );

		if ( ! $force && isset( $this->cache[ $group ] ) && array_key_exists( $id, $this->cache[ $group ] ) ) {
			$found = true;
			$this->cache_hits++;
			$val = $this->cache[ $group ][ $id ];
			return is_object( $val ) ? clone $val : $val;
		}

		if ( $this->is_non_persistent( $group ) || ! $this->ensure() ) {
			$found = false;
			$this->cache_misses++;
			return false;
		}

		$this->redis_calls++;
		$raw = $this->client->get( $id );
		if ( false === $raw ) {
			$found = false;
			$this->cache_misses++;
			return false;
		}
		$val                          = $this->unserialize( $raw );
		$this->cache[ $group ][ $id ] = $val;
		$found                        = true;
		$this->cache_hits++;
		return is_object( $val ) ? clone $val : $val;
	}

	/**
	 * @param array<int,int|string> $keys
	 * @param string                $group
	 * @param bool                  $force
	 * @return array<int|string,mixed>
	 */
	public function get_multiple( $keys, $group = 'default', $force = false ) {
		$result  = array();
		$to_load = array(); // id => original key.

		foreach ( $keys as $key ) {
			$id = $this->key( $key, $group );
			if ( ! $force && isset( $this->cache[ $group ] ) && array_key_exists( $id, $this->cache[ $group ] ) ) {
				$val            = $this->cache[ $group ][ $id ];
				$result[ $key ] = is_object( $val ) ? clone $val : $val;
				$this->cache_hits++;
			} else {
				$to_load[ $id ] = $key;
				$result[ $key ] = false;
			}
		}

		if ( empty( $to_load ) ) {
			return $result;
		}
		if ( $this->is_non_persistent( $group ) || ! $this->ensure() ) {
			$this->cache_misses += count( $to_load );
			return $result;
		}

		$this->redis_calls++;
		$fetched = $this->client->mget( array_keys( $to_load ) );
		foreach ( $to_load as $id => $key ) {
			$raw = isset( $fetched[ $id ] ) ? $fetched[ $id ] : false;
			if ( false === $raw ) {
				$this->cache_misses++;
				continue;
			}
			$val                          = $this->unserialize( $raw );
			$this->cache[ $group ][ $id ] = $val;
			$result[ $key ]               = is_object( $val ) ? clone $val : $val;
			$this->cache_hits++;
		}
		return $result;
	}

	/**
	 * @param array<int,array{0:int|string,1:mixed}>|array<int|string,mixed> $data
	 * @param string                                                          $group
	 * @param int                                                             $expire
	 * @return array<int|string,bool>
	 */
	public function set_multiple( array $data, $group = 'default', $expire = 0 ) {
		$out = array();
		if ( empty( $data ) ) {
			return $out;
		}
		// Runtime cache is always updated. Non-persistent groups stop there.
		if ( $this->is_non_persistent( $group ) ) {
			foreach ( $data as $key => $value ) {
				$id                           = $this->key( $key, $group );
				$this->cache[ $group ][ $id ] = is_object( $value ) ? clone $value : $value;
				$out[ $key ]                  = true;
			}
			return $out;
		}
		// GH #607: one pipelined batch instead of a Redis round-trip per key.
		$ttl   = $this->ttl( $expire );
		$items = array();
		$keys  = array();
		foreach ( $data as $key => $value ) {
			$id                           = $this->key( $key, $group );
			$this->cache[ $group ][ $id ] = is_object( $value ) ? clone $value : $value;
			$items[]                      = array( $id, $this->serialize( $value ), $ttl );
			$keys[]                       = $key;
			$out[ $key ]                  = true; // runtime-only default when Redis is unavailable.
		}
		if ( $this->ensure() ) {
			$this->redis_calls++;
			$res = $this->client->set_many( $items );
			foreach ( $keys as $i => $key ) {
				if ( array_key_exists( $i, $res ) ) {
					$out[ $key ] = (bool) $res[ $i ];
				}
			}
		}
		return $out;
	}

	/**
	 * @param array<int|string,mixed> $data
	 * @param string                  $group
	 * @param int                     $expire
	 * @return array<int|string,bool>
	 */
	public function add_multiple( array $data, $group = 'default', $expire = 0 ) {
		$out = array();
		if ( function_exists( 'wp_suspend_cache_addition' ) && wp_suspend_cache_addition() ) {
			foreach ( $data as $key => $value ) {
				$out[ $key ] = false;
			}
			return $out;
		}
		if ( empty( $data ) ) {
			return $out;
		}
		if ( $this->is_non_persistent( $group ) ) {
			foreach ( $data as $key => $value ) {
				$id = $this->key( $key, $group );
				if ( isset( $this->cache[ $group ][ $id ] ) ) {
					$out[ $key ] = false;
					continue;
				}
				$this->cache[ $group ][ $id ] = is_object( $value ) ? clone $value : $value;
				$out[ $key ]                  = true;
			}
			return $out;
		}
		// GH #607: pipeline the NX writes for candidates not already in runtime.
		$ttl   = $this->ttl( $expire );
		$items = array();
		$meta  = array(); // parallel to $items: [origKey, id, value].
		foreach ( $data as $key => $value ) {
			$id = $this->key( $key, $group );
			if ( isset( $this->cache[ $group ][ $id ] ) ) {
				$out[ $key ] = false;
				continue;
			}
			$meta[]  = array( $key, $id, $value );
			$items[] = array( $id, $this->serialize( $value ), $ttl );
		}
		if ( empty( $items ) ) {
			return $out;
		}
		$res = null;
		if ( $this->ensure() ) {
			$this->redis_calls++;
			$res = $this->client->add_many( $items );
		}
		foreach ( $meta as $i => $m ) {
			// Redis down (null) => runtime-only add succeeds; else honour NX result.
			$stored = ( null === $res ) ? true : ( array_key_exists( $i, $res ) ? (bool) $res[ $i ] : true );
			if ( $stored ) {
				$this->cache[ $group ][ $m[1] ] = is_object( $m[2] ) ? clone $m[2] : $m[2];
				$out[ $m[0] ]                   = true;
			} else {
				$out[ $m[0] ] = false;
			}
		}
		return $out;
	}

	/**
	 * @param array<int,int|string> $keys
	 * @param string                $group
	 * @return array<int|string,bool>
	 */
	public function delete_multiple( array $keys, $group = 'default' ) {
		$out = array();
		if ( empty( $keys ) ) {
			return $out;
		}
		$ids = array();
		foreach ( $keys as $key ) {
			$id = $this->key( $key, $group );
			unset( $this->cache[ $group ][ $id ] );
			$ids[]       = $id;
			$out[ $key ] = true; // delete is idempotent — absent key still "deleted".
		}
		// GH #607: one native multi-key UNLINK instead of a DEL per key.
		if ( ! $this->is_non_persistent( $group ) && $this->ensure() ) {
			$this->redis_calls++;
			$this->client->unlink( $ids );
		}
		return $out;
	}

	/**
	 * @param int|string $key
	 * @param string     $group
	 * @return bool
	 */
	public function delete( $key, $group = 'default' ) {
		$id = $this->key( $key, $group );
		unset( $this->cache[ $group ][ $id ] );
		if ( $this->is_non_persistent( $group ) ) {
			return true;
		}
		if ( $this->ensure() ) {
			$this->redis_calls++;
			$this->client->del( $id );
		}
		return true;
	}

	/**
	 * @param int|string $key
	 * @param int        $offset
	 * @param string     $group
	 * @return int|false
	 */
	public function incr( $key, $offset = 1, $group = 'default' ) {
		return $this->step( $key, (int) $offset, $group );
	}

	/**
	 * @param int|string $key
	 * @param int        $offset
	 * @param string     $group
	 * @return int|false
	 */
	public function decr( $key, $offset = 1, $group = 'default' ) {
		return $this->step( $key, -( (int) $offset ), $group );
	}

	/**
	 * Shared incr/decr implementation. WordPress counters are stored like any
	 * other value (serialized), so a native Redis INCRBY cannot be used —
	 * it would choke on the serialized payload. We read-modify-write instead,
	 * which also matches core's non-atomic incr/decr semantics, and clamp at 0.
	 *
	 * @param int|string $key
	 * @param int        $delta
	 * @param string     $group
	 * @return int|false
	 */
	private function step( $key, $delta, $group ) {
		$id = $this->key( $key, $group );

		if ( $this->is_non_persistent( $group ) || ! $this->ensure() ) {
			// Non-persistent group, OR Redis is unavailable: runtime-only counter
			// (mirrors set()'s runtime-only fallback), so incr/decr keep working
			// within the request instead of returning false when Redis is down.
			$cur                          = isset( $this->cache[ $group ][ $id ] ) ? (int) $this->cache[ $group ][ $id ] : 0;
			$cur                          = max( 0, $cur + $delta );
			$this->cache[ $group ][ $id ] = $cur;
			return $cur;
		}

		// Existence + stored form. WordPress incr/decr return false on a missing
		// persistent key, so we confirm the key exists first (a bare native
		// INCRBY would auto-create it at $delta). $raw is the runtime value when
		// cached, else the raw Redis string.
		if ( isset( $this->cache[ $group ] ) && array_key_exists( $id, $this->cache[ $group ] ) ) {
			$raw = $this->cache[ $group ][ $id ];
		} else {
			$this->redis_calls++;
			$raw = $this->client->get( $id );
			if ( false === $raw ) {
				return false; // core returns false when the key does not exist.
			}
		}

		// JAB-97: native atomic INCRBY/DECRBY when the stored value is a raw
		// integer counter (untagged — our own counter format). This makes
		// concurrent incr/decr lossless: no read-modify-write lost-update
		// window. INCRBY reads the live Redis value, so a stale runtime copy
		// can't skew it.
		if ( $this->is_raw_counter( $raw ) ) {
			$this->redis_calls++;
			$new = $this->client->incr( $id, $delta ); // INCRBY handles a negative delta.
			if ( false !== $new ) {
				$new = (int) $new;
				if ( $new < 0 ) {
					// WordPress clamps at zero. Reset the raw counter (no
					// serialize, so it stays a native counter). The reset race
					// is no worse than the pre-JAB-97 non-atomic path.
					$this->redis_calls++;
					$this->client->set( $id, '0', 0, true );
					$new = 0;
				}
				$this->cache[ $group ][ $id ] = $new;
				return $new;
			}
			// Native op failed unexpectedly — fall through to read-modify-write.
		}

		// Read-modify-write fallback for serialized / legacy / non-integer
		// values. Store the result as a RAW integer (no serialize) so the next
		// step() goes native, migrating the counter lazily. KEEPTTL preserves
		// any existing expiry (GH #604).
		$cur = (int) $this->unserialize( $raw );
		$new = max( 0, $cur + $delta );
		$this->redis_calls++;
		if ( ! $this->client->set( $id, (string) $new, 0, true ) ) {
			return false;
		}
		$this->cache[ $group ][ $id ] = $new;
		return $new;
	}

	/**
	 * JAB-97: true when $v is one of our raw integer counters. Every value
	 * written through set() carries a ph:/ig: serializer tag, so a bare integer
	 * (runtime int, or an untagged all-digit Redis string) can only be a native
	 * counter — safe to drive INCRBY/DECRBY against.
	 *
	 * @param mixed $v
	 * @return bool
	 */
	private function is_raw_counter( $v ) {
		if ( is_int( $v ) ) {
			return true;
		}
		return is_string( $v ) && '' !== $v && (bool) preg_match( '/^-?\\d+$/', $v );
	}

	/**
	 * Flush only THIS site's keys. Never FLUSHDB — DB 1 is shared across
	 * every tenant on the host (ADR-0059).
	 *
	 * @return bool
	 */
	public function flush() {
		$this->cache = array();
		if ( $this->ensure() ) {
			$this->redis_calls++;
			$this->client->delete_by_pattern( $this->prefix . '*' );
		}
		return true;
	}

	/**
	 * @param string $group
	 * @return bool
	 */
	public function flush_group( $group ) {
		unset( $this->cache[ $group ] );
		if ( $this->is_non_persistent( $group ) ) {
			return true;
		}
		if ( $this->ensure() ) {
			$this->redis_calls++;
			$this->client->delete_by_pattern( $this->prefix . $this->group_token( $group ) . ':*' );
		}
		return true;
	}

	/**
	 * Drop the in-request cache without touching Redis (WP 6.0+).
	 *
	 * @return bool
	 */
	public function flush_runtime() {
		$this->cache = array();
		return true;
	}

	/**
	 * @param string $feature
	 * @return bool
	 */
	public function supports( $feature ) {
		switch ( $feature ) {
			case 'add_multiple':
			case 'set_multiple':
			case 'get_multiple':
			case 'delete_multiple':
			case 'flush_runtime':
			case 'flush_group':
				return true;
			default:
				return false;
		}
	}

	/**
	 * @param string|string[] $groups
	 */
	public function add_global_groups( $groups ) {
		foreach ( (array) $groups as $g ) {
			$this->global_groups[ $g ] = true;
		}
	}

	/**
	 * @param string|string[] $groups
	 */
	public function add_non_persistent_groups( $groups ) {
		foreach ( (array) $groups as $g ) {
			$this->non_persistent_groups[ $g ] = true;
		}
	}

	/**
	 * @param int $blog_id
	 */
	public function switch_to_blog( $blog_id ) {
		$this->blog_prefix = $this->multisite ? (int) $blog_id : 1;
	}

	public function close() {
		$this->client->close();
	}

	/**
	 * Diagnostics for the admin screen / WP-CLI.
	 *
	 * @return array<string,mixed>
	 */
	public function stats() {
		$connected = $this->client->is_connected();
		return array(
			'connected'    => $connected,
			'driver'       => $this->client->driver(),
			'serializer'   => $this->serializer,
			'prefix'       => $this->prefix,
			'database'     => (int) $this->cfg['database'],
			'hits'         => $this->cache_hits,
			'misses'       => $this->cache_misses,
			'redis_calls'  => $this->redis_calls,
			'last_error'   => $this->client->last_error(),
			'is_persistent' => $this->is_persistent,
		);
	}

	public function client() {
		return $this->client;
	}

	// ------------------------------------------------------------------
	// Internals.
	// ------------------------------------------------------------------

	private function ensure() {
		if ( $this->client->is_connected() ) {
			return true;
		}
		$ok                  = $this->client->connect();
		$this->is_persistent = $ok;
		return $ok;
	}

	private function has( $key, $group ) {
		$found = null;
		$this->get( $key, $group, false, $found );
		return (bool) $found;
	}

	private function is_non_persistent( $group ) {
		return isset( $this->non_persistent_groups[ $group ] );
	}

	private function ttl( $expire ) {
		$expire = (int) $expire;
		if ( $expire <= 0 ) {
			return $this->maxttl; // 0 = no expiry (LRU governs).
		}
		if ( $this->maxttl > 0 && $expire > $this->maxttl ) {
			return $this->maxttl;
		}
		return $expire;
	}

	/**
	 * Build the fully-qualified Redis key:
	 *   {prefix}{group-token}:{blog?}{key}
	 * Global groups are not blog-scoped.
	 *
	 * @param int|string $key
	 * @param string     $group
	 * @return string
	 */
	private function key( $key, $group ) {
		if ( '' === $group ) {
			$group = 'default';
		}
		$blog = isset( $this->global_groups[ $group ] ) ? '' : ( $this->blog_prefix . ':' );
		return $this->prefix . $this->group_token( $group ) . ':' . $blog . $key;
	}

	private function group_token( $group ) {
		return preg_replace( '/[^A-Za-z0-9_.-]/', '_', (string) $group );
	}

	private function serialize( $data ) {
		if ( 'igbinary' === $this->serializer ) {
			return 'ig:' . igbinary_serialize( $data );
		}
		return 'ph:' . serialize( $data ); // phpcs:ignore WordPress.PHP.DiscouragedPHPFunctions.serialize_serialize
	}

	private function unserialize( $raw ) {
		// JAB-97: a bare untagged integer string is a native counter (INCRBY
		// storage). Every serialized value carries a ph:/ig: tag, so an all-digit
		// string can only be one of our atomic counters — return a real int.
		if ( is_string( $raw ) && '' !== $raw && preg_match( '/^-?\\d+$/', $raw ) ) {
			return (int) $raw;
		}
		if ( ! is_string( $raw ) || strlen( $raw ) < 3 ) {
			return $raw;
		}
		$tag  = substr( $raw, 0, 3 );
		$body = substr( $raw, 3 );
		if ( 'ig:' === $tag && function_exists( 'igbinary_unserialize' ) ) {
			$val = @igbinary_unserialize( $body ); // phpcs:ignore
			return ( false === $val && '' !== $body ) ? false : $val;
		}
		if ( 'ph:' === $tag ) {
			// SECURITY (Gitea #412): restrict deserialization to a curated set of
			// WP value-object classes. Anything else becomes an inert
			// __PHP_Incomplete_Class — PHP does NOT call its __wakeup, so a
			// crafted ph: payload in the keyspace can't drive a POP gadget chain
			// to RCE. We DON'T rely on prefix-unobservability (false under
			// jabali, where panel-api pins jc:<osuser>:<installID>:) — defense in
			// depth behind the per-tenant Redis ACL. A site that caches its own
			// objects can widen this via the JABALI_CACHE_ALLOWED_CLASSES
			// constant (CSV, or the literal true to restore allow-all).
			$val = @unserialize( $body, array( 'allowed_classes' => self::allowed_unserialize_classes() ) ); // phpcs:ignore WordPress.PHP.DiscouragedPHPFunctions.serialize_unserialize
			// A disallowed class deserialized to an incomplete stub — treat the
			// whole entry as a miss rather than hand a broken object to WP.
			if ( $val instanceof \__PHP_Incomplete_Class ) {
				return false;
			}
			return $val;
		}
		return $raw;
	}

	/**
	 * Allowlist of classes the ph: object cache may deserialize (Gitea #412).
	 *
	 * @return string[]|bool array of class names, or true if the operator opted
	 *                       back into allow-all via JABALI_CACHE_ALLOWED_CLASSES.
	 */
	private static function allowed_unserialize_classes() {
		if ( defined( 'JABALI_CACHE_ALLOWED_CLASSES' ) ) {
			$c = JABALI_CACHE_ALLOWED_CLASSES;
			if ( true === $c || 'true' === $c ) {
				return true;
			}
			if ( is_array( $c ) ) {
				return $c;
			}
			if ( is_string( $c ) && '' !== $c ) {
				return array_values( array_filter( array_map( 'trim', explode( ',', $c ) ) ) );
			}
		}
		return array(
			'stdClass',
			'WP_Post', 'WP_Term', 'WP_User', 'WP_Comment', 'WP_Site', 'WP_Network',
			'WP_Post_Type', 'WP_Taxonomy', 'WP_Role', 'WP_Roles', 'WP_Error',
			'WP_Block', 'WP_Block_Type', 'WP_Theme', 'WP_Meta_Query', 'WP_Date_Query',
			'WP_Dependency', 'WP_Dependencies', 'WP_Locale',
		);
	}
}
