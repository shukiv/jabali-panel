<?php
/**
 * Jabali Cache — WP-CLI commands.
 *
 *   wp jabali-cache status
 *   wp jabali-cache enable
 *   wp jabali-cache disable
 *   wp jabali-cache flush [--pages]
 *   wp jabali-cache update-dropins
 *   wp jabali-cache remove-dropins
 *   wp jabali-cache diagnose
 *
 * @package Jabali_Cache
 */

defined( 'ABSPATH' ) || exit;

if ( ! defined( 'WP_CLI' ) || ! WP_CLI ) {
	return;
}

class Jabali_Cache_CLI {

	/** @var string */
	private $plugin_dir;

	public function __construct( $plugin_dir = '' ) {
		$this->plugin_dir = $plugin_dir ? $plugin_dir : dirname( __DIR__ );
	}

	/**
	 * Show cache status and live Redis connectivity.
	 *
	 * @when after_wp_load
	 */
	public function status() {
		$s   = Jabali_Cache_Settings::get();
		$cfg = Jabali_Cache_Config::load();
		$c   = new Jabali_Cache_Client( $cfg );
		$ok  = $c->connect();

		// GH #602: report the RUNTIME state. When the Jabali panel manages this
		// site (JABALI_CACHE_* constants in wp-config.php), the drop-ins read
		// those constants, NOT the plugin options — so status must too, or it
		// lies. Fall back to the option only on a non-panel-managed install.
		$managed = defined( 'JABALI_CACHE_SOCKET' ) || defined( 'JABALI_CACHE_MAXTTL' ) || defined( 'JABALI_CACHE_DISABLED' );
		$enabled = $managed
			? ! ( defined( 'JABALI_CACHE_DISABLED' ) && JABALI_CACHE_DISABLED )
			: (bool) $s['enabled'];
		$page = $managed
			? ( defined( 'JABALI_CACHE_PAGE_CACHE' ) && JABALI_CACHE_PAGE_CACHE )
			: (bool) $s['page_cache'];

		$rows = array(
			array( 'field' => 'managed_by', 'value' => $managed ? 'jabali panel (wp-config constants)' : 'plugin options' ),
			array( 'field' => 'enabled', 'value' => $enabled ? 'yes' : 'no' ),
			array( 'field' => 'connected', 'value' => $ok ? 'yes' : 'no' ),
			array( 'field' => 'driver', 'value' => $ok ? $c->driver() : '-' ),
			array( 'field' => 'target', 'value' => ( 'unix' === $cfg['scheme'] ? $cfg['socket'] : $cfg['host'] . ':' . $cfg['port'] ) ),
			array( 'field' => 'database', 'value' => (string) $cfg['database'] ),
			array( 'field' => 'prefix', 'value' => $cfg['prefix'] ),
			array( 'field' => 'keys', 'value' => $ok ? (string) $c->count_keys( $cfg['prefix'] ) : '-' ),
			array( 'field' => 'page_cache', 'value' => $page ? 'on' : 'off' ),
		);
		if ( ! $ok ) {
			$rows[] = array( 'field' => 'error', 'value' => $c->last_error() );
			// JAB-89: surface the circuit breaker so an operator sees Redis is
			// held down (fail-fast) rather than being re-probed every request.
			$open = $c->circuit_open_until();
			if ( $open > 0 ) {
				$rows[] = array( 'field' => 'circuit_open_until', 'value' => gmdate( 'c', $open ) . ' (' . max( 0, $open - time() ) . 's)' );
			}
		}
		$c->close();
		\WP_CLI\Utils\format_items( 'table', $rows, array( 'field', 'value' ) );
	}

	/**
	 * Enable caching.
	 *
	 * @when after_wp_load
	 */
	public function enable() {
		// GH #602: on a panel-managed site the option is inert (drop-ins read
		// constants), so don't persist a misleading value — just warn.
		if ( ! ( defined( 'JABALI_CACHE_SOCKET' ) || defined( 'JABALI_CACHE_MAXTTL' ) || defined( 'JABALI_CACHE_DISABLED' ) ) ) {
			$s            = Jabali_Cache_Settings::get();
			$s['enabled'] = true;
			Jabali_Cache_Settings::save( $s );
		}
		$this->ensure_dropins();
		\WP_CLI::success( 'Object-cache drop-ins installed/repaired.' );
		// GH #602: on/off + connection are constant-driven (panel-managed), not
		// gated by this option — be honest so "enabled" isn't a false claim.
		\WP_CLI::warning( 'Runtime caching is driven by the JABALI_CACHE_* constants the Jabali panel writes to wp-config.php. This installed the drop-ins, but the Redis connection + on/off state are panel-managed — enable/disable caching from the Jabali panel.' );
	}

	/**
	 * Disable caching (settings kept; drop-ins left in place but inert).
	 *
	 * @when after_wp_load
	 */
	public function disable() {
		if ( ! ( defined( 'JABALI_CACHE_SOCKET' ) || defined( 'JABALI_CACHE_MAXTTL' ) || defined( 'JABALI_CACHE_DISABLED' ) ) ) {
			$s            = Jabali_Cache_Settings::get();
			$s['enabled'] = false;
			Jabali_Cache_Settings::save( $s );
		}
		if ( function_exists( 'wp_cache_flush' ) ) {
			wp_cache_flush();
		}
		\WP_CLI::success( 'Local cache flushed and setting recorded.' );
		\WP_CLI::warning( 'Runtime caching is driven by the JABALI_CACHE_* constants the Jabali panel writes to wp-config.php; this option does not gate the drop-ins. To actually turn caching off, disable it in the Jabali panel (which strips the constants + drop-ins).' );
	}

	/**
	 * Flush the cache.
	 *
	 * [--pages]
	 * : Also purge full-page cache entries.
	 *
	 * @when after_wp_load
	 */
	public function flush( $args, $assoc ) {
		if ( function_exists( 'wp_cache_flush' ) ) {
			wp_cache_flush();
		}
		if ( isset( $assoc['pages'] ) && class_exists( 'Jabali_Cache_Page_Cache' ) ) {
			$pc = new Jabali_Cache_Page_Cache();
			$n  = $pc->purge_all();
			\WP_CLI::log( "Purged {$n} page cache entries." );
		}
		\WP_CLI::success( 'Cache flushed.' );
	}

	/**
	 * Install or repair the wp-content drop-ins.
	 *
	 * @when after_wp_load
	 */
	public function update_dropins() {
		$res = $this->ensure_dropins();
		if ( $res['object'] ) {
			\WP_CLI::success( 'Drop-ins installed/updated.' );
		} else {
			\WP_CLI::error( 'Could not install object-cache.php (a foreign drop-in may be present, or wp-content is not writable).' );
		}
	}

	/**
	 * Remove the Jabali drop-ins.
	 *
	 * @when after_wp_load
	 */
	public function remove_dropins() {
		$mgr = new Jabali_Cache_Dropin_Manager( $this->plugin_dir );
		$mgr->remove();
		\WP_CLI::success( 'Drop-ins removed.' );
	}

	/**
	 * Verify live Redis connectivity with a real write/read/delete round-trip
	 * under the site's ACL-scoped prefix. Exits non-zero (WP_CLI::error) on any
	 * failure so a caller (the Jabali agent) can gate "cache enabled" on actual
	 * health instead of a bare exit-0. A plain connect()/PING is NOT enough — a
	 * wrong-prefix or mis-derived ACL token still connects but NOPERMs on the
	 * first key op, which only a real SET/GET surfaces (GH #410).
	 *
	 * @when after_wp_load
	 */
	public function verify() {
		$cfg = Jabali_Cache_Config::load();
		// JAB-89: force a live probe so an open circuit breaker never masks a
		// real diagnostic run.
		$c = ( new Jabali_Cache_Client( $cfg ) )->force_probe();
		if ( ! $c->connect() ) {
			\WP_CLI::error( 'Redis connect failed: ' . $c->last_error() );
		}
		$key = $cfg['prefix'] . '__jabali_verify';
		if ( ! $c->set( $key, '1', 10 ) ) {
			$err = $c->last_error();
			$c->close();
			\WP_CLI::error( 'Redis SET failed (NOPERM / keyspace denied for ' . $cfg['prefix'] . '*): ' . $err );
		}
		$val = $c->get( $key );
		$c->del( $key );
		$c->close();
		if ( '1' !== (string) $val ) {
			\WP_CLI::error( 'Redis round-trip mismatch (set 1, got ' . var_export( $val, true ) . ')' );
		}
		\WP_CLI::success( 'Cache verified: live Redis read/write under ' . $cfg['prefix'] );
	}

	/**
	 * Diagnose connectivity and print an actionable hint on failure.
	 *
	 * @when after_wp_load
	 */
	public function diagnose() {
		$cfg = Jabali_Cache_Config::load();
		\WP_CLI::log( 'phpredis extension: ' . ( class_exists( 'Redis' ) ? 'present' : 'absent (using pure-PHP client)' ) );
		\WP_CLI::log( 'igbinary extension: ' . ( function_exists( 'igbinary_serialize' ) ? 'present' : 'absent' ) );
		\WP_CLI::log( 'scheme/target: ' . $cfg['scheme'] . ' ' . ( 'unix' === $cfg['scheme'] ? $cfg['socket'] : $cfg['host'] . ':' . $cfg['port'] ) );

		// JAB-89: diagnose forces a live probe too (bypass the breaker).
		$c = ( new Jabali_Cache_Client( $cfg ) )->force_probe();
		if ( $c->connect() ) {
			\WP_CLI::success( 'Connected via ' . $c->driver() . '. Keys for this site: ' . $c->count_keys( $cfg['prefix'] ) );
			$c->close();
			return;
		}
		\WP_CLI::warning( 'Not connected: ' . $c->last_error() );
		\WP_CLI::log( 'Hints:' );
		\WP_CLI::log( '  - open_basedir must include /run/redis (or the socket path).' );
		\WP_CLI::log( '  - the PHP-FPM system user needs read/write on the Redis socket (the Jabali panel grants this automatically).' );
		\WP_CLI::log( '  - redis-server must be running on the panel host.' );
	}

	/**
	 * Cache stats as JSON (GH #617). keyspace hits/misses + used_memory are
	 * Redis-instance-wide (shared); `keys` is this site's prefix only.
	 *
	 * @when after_wp_load
	 */
	public function stats() {
		$cfg = Jabali_Cache_Config::load();
		$out = array(
			'connected'   => false,
			'driver'      => '-',
			'keys'        => 0,
			'hits'        => 0,
			'misses'      => 0,
			'hit_ratio'   => 0.0,
			'used_memory' => 0,
		);
		$c = new Jabali_Cache_Client( $cfg );
		if ( $c->connect() ) {
			$out['connected'] = true;
			$out['driver']    = $c->driver();
			$out['keys']      = (int) $c->count_keys( $cfg['prefix'] );
			$info             = $c->info();
			$h                = $this->info_int( $info, 'keyspace_hits' );
			$m                = $this->info_int( $info, 'keyspace_misses' );
			$out['hits']      = $h;
			$out['misses']    = $m;
			$out['hit_ratio'] = ( $h + $m ) > 0 ? round( 100 * $h / ( $h + $m ), 1 ) : 0.0;
			$out['used_memory'] = $this->info_int( $info, 'used_memory' );
			$c->close();
		}
		\WP_CLI::log( wp_json_encode( $out ) );
	}

	/**
	 * Enforce the per-site object-cache budget (GH #612). Deletes keys over the
	 * JABALI_CACHE_MAXMEMORY_MB budget (~2KB/key estimate). Shared Redis has no
	 * per-user maxmemory, so this bounds the site's key footprint; the instance
	 * allkeys-lru still handles global eviction. Safe to run on a timer.
	 *
	 * @when after_wp_load
	 */
	public function trim() {
		$cfg = Jabali_Cache_Config::load();
		$mb  = defined( 'JABALI_CACHE_MAXMEMORY_MB' ) ? (int) JABALI_CACHE_MAXMEMORY_MB : 0;
		if ( $mb <= 0 ) {
			\WP_CLI::log( wp_json_encode( array( 'trimmed' => 0, 'budget_mb' => 0, 'note' => 'no budget set' ) ) );
			return;
		}
		$max_keys = $mb * 512; // ~2 KiB per object-cache key.
		$c        = new Jabali_Cache_Client( $cfg );
		$deleted  = 0;
		if ( $c->connect() ) {
			$deleted = (int) $c->trim_to_budget( $cfg['prefix'], $max_keys );
			$c->close();
		}
		\WP_CLI::log( wp_json_encode( array( 'trimmed' => $deleted, 'budget_mb' => $mb, 'max_keys' => $max_keys ) ) );
	}

	private function info_int( $info, $key ) {
		if ( preg_match( '/^' . preg_quote( $key, '/' ) . ':(\\d+)/m', (string) $info, $mm ) ) {
			return (int) $mm[1];
		}
		return 0;
	}

	private function ensure_dropins() {
		$mgr = new Jabali_Cache_Dropin_Manager( $this->plugin_dir );
		return $mgr->install();
	}
}
