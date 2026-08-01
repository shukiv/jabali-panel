<?php
/**
 * Jabali Cache — admin screen.
 *
 * Settings page under Settings → Jabali Cache: connection status, live Redis
 * health, flush button, drop-in install/repair, and the connection form.
 *
 * @package Jabali_Cache
 */

defined( 'ABSPATH' ) || exit;

class Jabali_Cache_Admin {

	const SLUG  = 'jabali-cache';
	const NONCE = 'jabali_cache_action';

	/** @var string */
	private $plugin_dir;

	/** @var string */
	private $plugin_file;

	public function __construct( $plugin_file ) {
		$this->plugin_file = $plugin_file;
		$this->plugin_dir  = dirname( $plugin_file );
	}

	public function hooks() {
		add_action( 'admin_menu', array( $this, 'menu' ) );
		add_action( 'admin_post_jabali_cache_save', array( $this, 'handle_save' ) );
		add_action( 'admin_post_jabali_cache_flush', array( $this, 'handle_flush' ) );
		add_action( 'admin_post_jabali_cache_dropins', array( $this, 'handle_dropins' ) );
		add_action( 'admin_notices', array( $this, 'notices' ) );
		add_action( 'admin_bar_menu', array( $this, 'admin_bar' ), 100 );
		add_filter(
			'plugin_action_links_' . plugin_basename( $this->plugin_file ),
			array( $this, 'action_links' )
		);
	}

	public function menu() {
		add_options_page(
			'Jabali Cache',
			'Jabali Cache',
			'manage_options',
			self::SLUG,
			array( $this, 'render' )
		);
	}

	public function action_links( $links ) {
		$url = admin_url( 'options-general.php?page=' . self::SLUG );
		array_unshift( $links, '<a href="' . esc_url( $url ) . '">' . esc_html__( 'Settings', 'jabali-cache' ) . '</a>' );
		return $links;
	}

	public function admin_bar( $bar ) {
		if ( ! current_user_can( 'manage_options' ) ) {
			return;
		}
		$bar->add_node(
			array(
				'id'    => 'jabali-cache-flush',
				'title' => 'Flush Cache',
				'href'  => wp_nonce_url( admin_url( 'admin-post.php?action=jabali_cache_flush' ), self::NONCE ),
				'meta'  => array( 'title' => 'Flush the Jabali object cache' ),
			)
		);
	}

	// ------------------------------------------------------------------
	// Actions.
	// ------------------------------------------------------------------

	public function handle_save() {
		$this->guard();
		$in = array(
			'enabled'    => isset( $_POST['enabled'] ),
			'page_cache' => isset( $_POST['page_cache'] ),
			'page_ttl'   => isset( $_POST['page_ttl'] ) ? (int) $_POST['page_ttl'] : 300,
			'maxttl'     => isset( $_POST['maxttl'] ) ? (int) $_POST['maxttl'] : 0,
			'scheme'     => isset( $_POST['scheme'] ) ? sanitize_text_field( wp_unslash( $_POST['scheme'] ) ) : 'unix',
			'socket'     => isset( $_POST['socket'] ) ? sanitize_text_field( wp_unslash( $_POST['socket'] ) ) : '',
			'host'       => isset( $_POST['host'] ) ? sanitize_text_field( wp_unslash( $_POST['host'] ) ) : '',
			'port'       => isset( $_POST['port'] ) ? (int) $_POST['port'] : 6379,
			'database'   => isset( $_POST['database'] ) ? (int) $_POST['database'] : 1,
			'password'   => isset( $_POST['password'] ) ? sanitize_text_field( wp_unslash( $_POST['password'] ) ) : '',
		);
		Jabali_Cache_Settings::save( $in );
		$this->redirect( 'saved' );
	}

	public function handle_flush() {
		$this->guard();
		$n = 0;
		if ( function_exists( 'wp_cache_flush' ) ) {
			wp_cache_flush();
		}
		// Also purge page cache entries.
		if ( class_exists( 'Jabali_Cache_Page_Cache' ) ) {
			$pc = new Jabali_Cache_Page_Cache();
			$n  = $pc->purge_all();
		}
		$this->redirect( 'flushed', array( 'pages' => $n ) );
	}

	public function handle_dropins() {
		$this->guard();
		$mgr    = new Jabali_Cache_Dropin_Manager( $this->plugin_dir );
		$action = isset( $_POST['dropin_action'] ) ? sanitize_text_field( wp_unslash( $_POST['dropin_action'] ) ) : 'install';
		if ( 'remove' === $action ) {
			$mgr->remove();
			$this->redirect( 'dropins_removed' );
		}
		$res = $mgr->install();
		$this->redirect( ( $res['object'] ? 'dropins_installed' : 'dropins_failed' ) );
	}

	// ------------------------------------------------------------------
	// Notices.
	// ------------------------------------------------------------------

	public function notices() {
		if ( ! current_user_can( 'manage_options' ) ) {
			return;
		}
		$screen = function_exists( 'get_current_screen' ) ? get_current_screen() : null;
		$on_page = $screen && false !== strpos( (string) $screen->id, self::SLUG );

		$health = $this->health_cached();
		if ( ! $health['enabled'] ) {
			return;
		}
		// Surface a connection problem anywhere in admin so it isn't silent.
		if ( ! $health['connected'] ) {
			echo '<div class="notice notice-warning"><p><strong>Jabali Cache:</strong> ';
			echo 'Redis is not reachable, so caching is currently inactive (the site still works, just without acceleration). ';
			echo esc_html( $health['hint'] );
			echo ' <a href="' . esc_url( admin_url( 'options-general.php?page=' . self::SLUG ) ) . '">Open settings</a>.</p></div>';
			return;
		}
		if ( $on_page && ! $health['dropin_ok'] ) {
			echo '<div class="notice notice-warning"><p><strong>Jabali Cache:</strong> ';
			echo 'The object-cache drop-in is not installed yet. Use “Install / repair drop-ins” below to enable persistent caching.</p></div>';
		}
	}

	// ------------------------------------------------------------------
	// Render.
	// ------------------------------------------------------------------

	public function render() {
		if ( ! current_user_can( 'manage_options' ) ) {
			wp_die( esc_html__( 'You do not have permission to view this page.', 'jabali-cache' ) );
		}
		$s      = Jabali_Cache_Settings::get();
		$health = $this->health();
		$mgr    = new Jabali_Cache_Dropin_Manager( $this->plugin_dir );
		$dstat  = $mgr->status();
		$flush  = wp_nonce_url( admin_url( 'admin-post.php?action=jabali_cache_flush' ), self::NONCE );
		$page   = $this->probe_page_cache();

		// Derived request-cache hit/miss ratio.
		$oc   = isset( $GLOBALS['wp_object_cache'] ) ? $GLOBALS['wp_object_cache'] : null;
		$hits = null;
		if ( $oc && isset( $oc->cache_hits ) ) {
			$h    = (int) $oc->cache_hits;
			$m    = (int) $oc->cache_misses;
			$tot  = $h + $m;
			$hits = array( 'h' => $h, 'm' => $m, 'rate' => $tot ? (int) round( 100 * $h / $tot ) : 0 );
		}
		$page_active = ! empty( $page['status'] );
		$page_probe  = $page_active ? strtoupper( $page['status'] ) : '';
		$dropins_ok  = ! empty( $dstat['object_ours'] );

		$this->styles();
		echo '<div class="wrap jc-wrap">';
		echo '<h1 class="jc-title">Jabali Cache</h1>';
		// GH #602: be honest about the source of truth. The early drop-ins read
		// JABALI_CACHE_* constants that the Jabali panel writes into wp-config.php
		// (they load before WordPress boots, so they never read the settings
		// option). Saving here does NOT change runtime — the panel does.
		echo '<div class="notice notice-info" style="margin:12px 0;"><p><strong>Managed by the Jabali panel.</strong> '
			. 'Runtime caching (Redis connection, on/off, namespace) is driven by the <code>JABALI_CACHE_*</code> constants the panel writes into <code>wp-config.php</code>; the cache drop-ins read those constants, not the fields below. '
			. 'Change caching from the <strong>Jabali panel</strong> — values entered here are advisory and do not override the panel constants.</p></div>';
		$this->render_flash();

		// Hero drop-in status banner.
		if ( $dropins_ok ) {
			echo '<div class="jc-alert jc-alert-ok">' . $this->icon( 'check' ) . '<span>Drop-ins installed.</span></div>';
		} else {
			echo '<div class="jc-alert jc-alert-warn">' . $this->icon( 'warn' ) . '<span>Object-cache drop-in is not installed. Use “Install / repair drop-ins” below to enable persistent caching.</span></div>';
		}

		// ---- Top metric cards ---------------------------------------------
		echo '<div class="jc-metrics">';
		$this->metric(
			'toggle',
			'Caching enabled',
			$s['enabled'] ? '<span class="jc-ok">Yes</span>' : '<span class="jc-muted">No</span>'
		);
		$this->metric(
			'db',
			'Redis connection',
			$health['connected'] ? '<span class="jc-ok">Connected</span>' : '<span class="jc-bad">Not reachable</span>'
		);
		$this->metric(
			'doc',
			'Server page cache',
			$page_active ? '<span class="jc-ok">Active</span>' : '<span class="jc-muted">Off</span>',
			$page_active ? '(last probe: ' . esc_html( $page_probe ) . ')' : 'no server cache detected'
		);
		if ( null !== $hits ) {
			$this->metric(
				'trend',
				'Cache hits (this request)',
				esc_html( number_format_i18n( $hits['h'] ) ) . ' <span class="jc-metric-unit">hits</span>',
				'/ ' . esc_html( number_format_i18n( $hits['m'] ) ) . ' misses (' . (int) $hits['rate'] . '%)'
			);
		} else {
			$this->metric( 'trend', 'Cache hits (this request)', '<span class="jc-muted">—</span>', 'not measured this request' );
		}
		echo '</div>'; // .jc-metrics

		// ---- Lower two-column layout --------------------------------------
		echo '<div class="jc-cols">';

		// Left: Cache Status.
		echo '<div class="jc-card jc-card-status">';
		echo '<div class="jc-card-head">' . $this->icon( 'clipboard' ) . '<h2>Cache Status</h2></div>';
		echo '<div class="jc-kv-list">';
		$this->kv( 'Caching enabled', $s['enabled'] ? '<span class="jc-ok">Yes</span>' : '<span class="jc-muted">No</span>' );
		$this->kv( 'Redis connection', $health['connected'] ? '<span class="jc-ok">Connected</span>' : '<span class="jc-bad">Not reachable</span>' );
		$this->kv( 'Driver', $health['connected'] ? esc_html( $health['driver'] ) : '—' );
		$this->kv( 'Serializer', esc_html( $health['serializer'] ) );
		$this->kv( 'Target', '<code>' . esc_html( $health['target'] ) . '</code> (db ' . (int) $s['database'] . ')' );
		$this->kv( 'Key prefix', '<code class="jc-ellipsis">' . esc_html( $health['prefix'] ) . '</code>' );
		$this->kv( 'Keys for this site', $health['connected'] ? ( (int) $health['keys'] . ( ! empty( $health['keys_approx'] ) ? '+' : '' ) ) : '—' );
		if ( null !== $hits ) {
			$this->kv( 'Cache hits (this request)', esc_html( $hits['h'] . ' hits / ' . $hits['m'] . ' misses (' . $hits['rate'] . '%)' ) );
		}
		$ttl = (int) $s['maxttl'];
		$this->kv( 'Object TTL', $ttl > 0 ? esc_html( $ttl . 's' ) : 'none (Redis LRU eviction)' );
		$this->kv( 'Object-cache drop-in', $this->dropin_label( $dstat['object_installed'], $dstat['object_ours'], $dstat['object_foreign'] ) );
		if ( $health['connected'] && ! empty( $health['server'] ) ) {
			$sv    = $health['server'];
			$parts = array();
			if ( ! empty( $sv['redis_version'] ) ) { $parts[] = 'v' . $sv['redis_version']; }
			if ( ! empty( $sv['used_memory_human'] ) ) { $parts[] = $sv['used_memory_human'] . ' used'; }
			if ( ! empty( $sv['maxmemory_policy'] ) ) { $parts[] = $sv['maxmemory_policy']; }
			if ( isset( $sv['evicted_keys'] ) ) { $parts[] = (int) $sv['evicted_keys'] . ' evicted'; }
			if ( $parts ) { $this->kv( 'Redis server', esc_html( implode( ' · ', $parts ) ) ); }
		}
		if ( ! $health['connected'] && '' !== $health['last_error'] ) {
			$this->kv( 'Last error', '<code>' . esc_html( $health['last_error'] ) . '</code>' );
		}
		echo '</div>'; // .jc-kv-list
		if ( ! $health['connected'] && $s['enabled'] ) {
			echo '<div class="jc-hint">' . esc_html( $health['hint'] ) . '</div>';
		}
		echo '</div>'; // .jc-card-status

		// Right column: Technical Details + Actions.
		echo '<div class="jc-col-right">';

		echo '<div class="jc-card">';
		echo '<div class="jc-card-head">' . $this->icon( 'gear' ) . '<h2>Technical Details</h2></div>';
		echo '<div class="jc-kv-list">';
		if ( empty( $s['page_cache'] ) ) {
			$this->kv( 'Advanced-cache drop-in', '<span class="jc-muted">Off — page caching handled by the server</span>' );
		} else {
			$this->kv( 'Advanced-cache drop-in', $this->dropin_label( $dstat['advanced_installed'], $dstat['advanced_ours'], false ) . ( $dstat['wp_cache_const'] ? '' : ' <em>(WP_CACHE not defined in wp-config.php)</em>' ) );
		}
		if ( $page_active ) {
			$this->kv( 'Server page cache', '<span class="jc-ok">Active</span> <span class="jc-muted">(last probe: ' . esc_html( $page_probe ) . ')</span>' );
			if ( '' !== $page['cache_control'] ) {
				$this->kv( 'Edge Cache-Control', '<code>' . esc_html( $page['cache_control'] ) . '</code>' );
			}
			$val = array();
			if ( 'yes' === $page['etag'] ) { $val[] = 'ETag'; }
			if ( 'yes' === $page['last_modified'] ) { $val[] = 'Last-Modified'; }
			if ( $val ) {
				$this->kv( 'Revalidation', esc_html( implode( ' + ', $val ) ) . ' <span class="jc-muted">(304 on returning visits)</span>' );
			}
		} else {
			$this->kv( 'Server page cache', '<span class="jc-muted">Not detected</span>' );
		}
		echo '</div></div>'; // .jc-kv-list .jc-card

		// Actions card.
		echo '<div class="jc-card">';
		echo '<div class="jc-card-head">' . $this->icon( 'bolt' ) . '<h2>Actions</h2></div>';
		echo '<div class="jc-actions">';
		echo '<a href="' . esc_url( $flush ) . '" class="button button-primary jc-btn">' . $this->icon( 'trash' ) . 'Flush cache now</a>';
		echo '<a href="' . esc_url( admin_url( 'options-general.php?page=' . self::SLUG ) ) . '" class="button jc-btn">' . $this->icon( 'refresh' ) . 'Refresh status</a>';
		echo '<a href="https://jabali-panel.com/" target="_blank" rel="noopener" class="button jc-btn">' . $this->icon( 'book' ) . 'View documentation</a>';
		echo '</div></div>'; // .jc-actions .jc-card

		echo '</div>'; // .jc-col-right
		echo '</div>'; // .jc-cols

		echo '<p class="jc-footnote">' . $this->icon( 'info' ) . 'Jabali Cache drop-ins are active. Changes to caching settings may take effect immediately.</p>';

		// ---- GH #614: Drop-ins & numbered configuration cards -------------
		// Drop-ins card (its own form): install/repair + remove.
		echo '<div class="jc-card jc-card-wide">';
		echo '<div class="jc-card-head">' . $this->icon( 'gear' ) . '<h2>Drop-ins &amp; Settings</h2></div>';
		echo '<form method="post" action="' . esc_url( admin_url( 'admin-post.php' ) ) . '" class="jc-dropin-form">';
		wp_nonce_field( self::NONCE );
		echo '<input type="hidden" name="action" value="jabali_cache_dropins">';
		echo '<button class="button button-primary jc-btn" name="dropin_action" value="install">Install / repair drop-ins</button> ';
		echo '<button class="button jc-btn" name="dropin_action" value="remove" onclick="return confirm(\'Remove the Jabali Cache drop-ins?\')">Remove drop-ins</button>';
		echo '</form>';
		echo '</div>';

		// Settings form wraps the numbered configuration cards + Save.
		echo '<form method="post" action="' . esc_url( admin_url( 'admin-post.php' ) ) . '">';
		wp_nonce_field( self::NONCE );
		echo '<input type="hidden" name="action" value="jabali_cache_save">';
		// GH #602: when the panel manages this site (JABALI_CACHE_* constants
		// present), the drop-ins read those constants, not these option fields —
		// so DISABLE the whole form. The notice above already explains why; this
		// stops an admin from typing values that silently do nothing.
		$jc_managed = defined( 'JABALI_CACHE_SOCKET' ) || defined( 'JABALI_CACHE_MAXTTL' ) || defined( 'JABALI_CACHE_DISABLED' );
		echo '<fieldset' . ( $jc_managed ? ' disabled' : '' ) . '>';

		echo '<div class="jc-cols">';

		// Left column: ① General + ③ Full-page.
		echo '<div class="jc-col-left">';
		echo '<div class="jc-card">' . $this->cfgHead( 1, 'General Cache Settings' ) . '<div class="jc-sets">';
		$this->setRow( 'Enable caching',
			'<input type="checkbox" name="enabled" ' . checked( $s['enabled'], true, false ) . '>',
			'Master switch for object + page caching.' );
		$this->setRow( 'Full-page cache',
			'<input type="checkbox" name="page_cache" ' . checked( $s['page_cache'], true, false ) . '>',
			'Optional. Off by default — the jabali nginx microcache already caches anonymous pages at the edge. Enable only if you disabled that.' );
		$this->setRow( 'Page cache TTL (seconds)',
			'<input type="number" name="page_ttl" value="' . esc_attr( (int) $s['page_ttl'] ) . '" class="small-text">' );
		$this->setRow( 'Object max TTL (seconds, 0 = none)',
			'<input type="number" name="maxttl" value="' . esc_attr( (int) $s['maxttl'] ) . '" class="small-text">' );
		echo '</div></div>';

		echo '<div class="jc-card">' . $this->cfgHead( 3, 'Full-page Cache' ) . '<div class="jc-sets">';
		echo '<p class="jc-muted" style="margin:2px 0 0;font-size:13px">On Jabali, anonymous pages are cached at the edge by the nginx FastCGI microcache — WordPress full-page caching stays off by default to avoid double-caching. Enable it above (General) only if you disabled the edge cache; the Page cache TTL then applies.</p>';
		echo '</div></div>';
		echo '</div>'; // .jc-col-left

		// Right column: ② Redis Connection.
		echo '<div class="jc-col-right">';
		echo '<div class="jc-card">' . $this->cfgHead( 2, 'Redis Connection' ) . '<div class="jc-sets">';
		$this->setRow( 'Connection',
			'<label class="jc-radio"><input type="radio" name="scheme" value="unix" ' . checked( $s['scheme'], 'unix', false ) . '> Unix socket</label>' .
			'<label class="jc-radio"><input type="radio" name="scheme" value="tcp" ' . checked( $s['scheme'], 'tcp', false ) . '> TCP</label>' );
		$this->setRow( 'Socket path',
			'<input type="text" name="socket" value="' . esc_attr( $s['socket'] ) . '" placeholder="/run/redis/redis.sock" class="regular-text">' );
		$this->setRow( 'TCP host',
			'<input type="text" name="host" value="' . esc_attr( $s['host'] ) . '" placeholder="127.0.0.1" class="regular-text">' );
		$this->setRow( 'TCP port',
			'<input type="number" name="port" value="' . esc_attr( (int) $s['port'] ) . '" class="regular-text">' );
		$this->setRow( 'Redis database',
			'<input type="number" name="database" value="' . esc_attr( (int) $s['database'] ) . '" class="regular-text">' );
		$this->setRow( 'Password (optional)',
			'<span class="jc-pw"><input type="password" id="jc-pw" name="password" value="' . esc_attr( $s['password'] ) . '" autocomplete="new-password" class="regular-text">' .
			'<button type="button" class="jc-pw-toggle" onclick="var f=document.getElementById(\'jc-pw\');f.type=f.type===\'password\'?\'text\':\'password\';" aria-label="Show/hide password">' . $this->icon( 'eye' ) . '</button></span>' );
		echo '</div></div>';
		echo '</div>'; // .jc-col-right
		echo '</div>'; // .jc-cols

		// ④ Advanced / Notes (full-width).
		echo '<div class="jc-card jc-card-wide">' . $this->cfgHead( 4, 'Advanced / Notes' );
		echo '<div class="jc-hint">' . $this->icon( 'info' ) . '<span>Jabali Cache uses the shared panel Redis (ADR-0059): unix socket <code>/run/redis/redis.sock</code>, database 1. Cache entries are isolated per site by key prefix and survive Redis LRU eviction by design.</span></div>';
		echo '</div>';

		submit_button( 'Save settings' );
		echo '</fieldset>';
		echo '</form>';

		echo '</div>'; // .wrap
	}

	/**
	 * kv emits one label/value row inside a Cache Status / Technical Details
	 * card. $value is pre-escaped HTML (callers escape).
	 *
	 * @param string $label
	 * @param string $value
	 */
	private function kv( $label, $value ) {
		echo '<div class="jc-kv"><span class="jc-k">' . esc_html( $label ) . '</span><span class="jc-v">' . $value . '</span></div>'; // phpcs:ignore WordPress.Security.EscapeOutput.OutputNotEscaped
	}

	/**
	 * metric emits one top-row metric card.
	 *
	 * @param string $icon  Icon key for $this->icon().
	 * @param string $label Card label.
	 * @param string $value Pre-escaped primary value HTML.
	 * @param string $sub   Optional pre-escaped sub-line.
	 */
	// cfgHead renders a numbered configuration-card header (GH #614): a small
	// circled number + the section title.
	private function cfgHead( $n, $title ) {
		return '<div class="jc-card-head jc-numbered"><span class="jc-num">' . (int) $n . '</span><h2>' . esc_html( $title ) . '</h2></div>';
	}

	// setRow renders one configuration row: label left, control (+ optional
	// helper text) right. $control/$help are pre-built HTML (caller escapes).
	private function setRow( $label, $control, $help = '' ) {
		echo '<div class="jc-set"><div class="jc-set-label">' . esc_html( $label ) . '</div>';
		echo '<div class="jc-set-ctl">' . $control; // phpcs:ignore WordPress.Security.EscapeOutput.OutputNotEscaped
		if ( '' !== $help ) {
			echo '<span class="jc-set-help">' . esc_html( $help ) . '</span>';
		}
		echo '</div></div>';
	}

	private function metric( $icon, $label, $value, $sub = '' ) {
		echo '<div class="jc-metric">';
		echo '<div class="jc-metric-icon">' . $this->icon( $icon ) . '</div>';
		echo '<div class="jc-metric-body"><div class="jc-metric-label">' . esc_html( $label ) . '</div>';
		echo '<div class="jc-metric-value">' . $value . '</div>'; // phpcs:ignore WordPress.Security.EscapeOutput.OutputNotEscaped
		if ( '' !== $sub ) {
			echo '<div class="jc-metric-sub">' . $sub . '</div>'; // phpcs:ignore WordPress.Security.EscapeOutput.OutputNotEscaped
		}
		echo '</div></div>';
	}

	/**
	 * icon returns a static inline SVG (16–20px, currentColor stroke). No user
	 * data — safe to echo.
	 *
	 * @param string $name
	 * @return string
	 */
	private function icon( $name ) {
		$p = '<svg class="jc-svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">';
		$paths = array(
			'check'     => '<circle cx="12" cy="12" r="9"/><path d="M8.5 12.5l2.5 2.5 4.5-5"/>',
			'warn'      => '<path d="M12 3l9 16H3z"/><path d="M12 10v4"/><path d="M12 17h.01"/>',
			'toggle'    => '<rect x="3" y="8" width="18" height="8" rx="4"/><circle cx="9" cy="12" r="2.4" fill="currentColor" stroke="none"/>',
			'db'        => '<ellipse cx="12" cy="6" rx="7" ry="3"/><path d="M5 6v6c0 1.7 3.1 3 7 3s7-1.3 7-3V6"/><path d="M5 12v6c0 1.7 3.1 3 7 3s7-1.3 7-3v-6"/>',
			'doc'       => '<path d="M7 3h7l4 4v14H7z"/><path d="M14 3v4h4"/>',
			'trend'     => '<path d="M4 17l5-5 3 3 7-7"/><path d="M15 8h5v5"/>',
			'clipboard' => '<rect x="6" y="4" width="12" height="17" rx="2"/><path d="M9 4V3h6v1"/><path d="M9 10h6M9 14h6"/>',
			'gear'      => '<circle cx="12" cy="12" r="3"/><path d="M12 3v3M12 18v3M3 12h3M18 12h3M5.6 5.6l2.1 2.1M16.3 16.3l2.1 2.1M18.4 5.6l-2.1 2.1M7.7 16.3l-2.1 2.1"/>',
			'bolt'      => '<path d="M13 2L4 14h7l-1 8 9-12h-7z"/>',
			'trash'     => '<path d="M4 7h16"/><path d="M9 7V5h6v2"/><path d="M6 7l1 13h10l1-13"/>',
			'refresh'   => '<path d="M20 11a8 8 0 10-2 6"/><path d="M20 5v6h-6"/>',
			'book'      => '<path d="M4 5a2 2 0 012-2h13v16H6a2 2 0 00-2 2z"/><path d="M4 19a2 2 0 012-2h13"/>',
			'info'      => '<circle cx="12" cy="12" r="9"/><path d="M12 11v5"/><path d="M12 8h.01"/>',
			'eye'       => '<path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7-10-7-10-7z"/><circle cx="12" cy="12" r="3"/>',
		);
		$body = isset( $paths[ $name ] ) ? $paths[ $name ] : $paths['info'];
		return $p . $body . '</svg>';
	}

	/**
	 * styles emits the scoped stylesheet for the redesigned dashboard (GH #609).
	 */
	private function styles() {
		echo '<style>
.jc-wrap{max-width:1180px}
.jc-title{font-size:28px;font-weight:600;margin:.2em 0 .6em}
.jc-svg{width:18px;height:18px;flex:0 0 auto}
.jc-ok{color:#1a7f37;font-weight:600}
.jc-bad{color:#b32d2e;font-weight:600}
.jc-muted{color:#646970}
.jc-alert{display:flex;align-items:center;gap:10px;padding:14px 18px;border-radius:10px;border:1px solid transparent;margin:0 0 18px;font-size:14px}
.jc-alert span{font-weight:500}
.jc-alert-ok{background:#eef7f0;border-color:#c6e6cf;color:#1a7f37}
.jc-alert-warn{background:#fcf6e6;border-color:#f0dfa8;color:#8a6d1a}
.jc-metrics{display:grid;grid-template-columns:repeat(4,1fr);gap:16px;margin-bottom:18px}
@media (max-width:1100px){.jc-metrics{grid-template-columns:repeat(2,1fr)}}
@media (max-width:640px){.jc-metrics{grid-template-columns:1fr}}
.jc-metric{display:flex;align-items:center;gap:14px;background:#fff;border:1px solid #e4e6eb;border-radius:12px;padding:18px 20px;box-shadow:0 1px 2px rgba(0,0,0,.04)}
.jc-metric-icon{display:flex;align-items:center;justify-content:center;width:42px;height:42px;border-radius:50%;background:#eef7f0;color:#1a7f37}
.jc-metric-icon .jc-svg{width:20px;height:20px}
.jc-metric-label{color:#646970;font-size:13px;margin-bottom:3px}
.jc-metric-value{font-size:20px;font-weight:600;line-height:1.1}
.jc-metric-unit{font-size:14px;font-weight:500;color:#646970}
.jc-metric-sub{color:#8a8f98;font-size:12px;margin-top:2px}
.jc-cols{display:grid;grid-template-columns:1fr 1fr;gap:16px;align-items:start}
@media (max-width:960px){.jc-cols{grid-template-columns:1fr}}
.jc-col-right{display:flex;flex-direction:column;gap:16px}
.jc-card{background:#fff;border:1px solid #e4e6eb;border-radius:12px;box-shadow:0 1px 2px rgba(0,0,0,.04);padding:6px 22px 20px}
.jc-card-head{display:flex;align-items:center;gap:10px;padding:16px 0 8px;border-bottom:1px solid #eceef1;margin-bottom:8px;color:#3c434a}
.jc-card-head .jc-svg{width:22px;height:22px;color:#646970}
.jc-card-head h2{font-size:16px;font-weight:600;margin:0;padding:0}
.jc-kv-list{display:flex;flex-direction:column}
.jc-kv{display:flex;justify-content:space-between;gap:16px;padding:9px 0;border-bottom:1px solid #f2f3f5;font-size:14px}
.jc-kv:last-child{border-bottom:0}
.jc-k{color:#3c434a;font-weight:500;flex:0 0 auto}
.jc-v{color:#1d2327;text-align:right;min-width:0;word-break:break-word}
.jc-v code{background:#f4f5f7;border-radius:4px;padding:1px 6px;font-size:12px}
.jc-ellipsis{display:inline-block;max-width:220px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;vertical-align:bottom}
.jc-actions{display:flex;flex-wrap:wrap;gap:10px;padding-top:6px}
.jc-btn{display:inline-flex!important;align-items:center;gap:8px;height:auto!important;padding:8px 16px!important;border-radius:8px!important}
.jc-btn .jc-svg{width:16px;height:16px}
.jc-hint{margin-top:12px;padding:10px 14px;background:#eef4fb;border:1px solid #cfe0f2;border-radius:8px;color:#2c3e50;font-size:13px}
.jc-footnote{display:flex;align-items:center;gap:8px;color:#646970;font-size:13px;margin:18px 0}
.jc-footnote .jc-svg{width:16px;height:16px}
.jc-card-wide{margin-top:16px}
.jc-col-left{display:flex;flex-direction:column;gap:16px}
.jc-numbered .jc-num{display:inline-flex;align-items:center;justify-content:center;width:24px;height:24px;border-radius:50%;border:1.5px solid #2271b1;color:#2271b1;font-size:13px;font-weight:600;flex:0 0 auto}
.jc-sets{display:flex;flex-direction:column;padding-top:4px}
.jc-set{display:flex;gap:16px;align-items:flex-start;padding:12px 0;border-bottom:1px solid #f2f3f5}
.jc-set:last-child{border-bottom:0}
.jc-set-label{flex:0 0 42%;color:#3c434a;font-weight:500;font-size:14px;padding-top:4px}
.jc-set-ctl{flex:1 1 auto;min-width:0;display:flex;flex-direction:column;gap:4px}
.jc-set-ctl input[type=text],.jc-set-ctl input[type=number],.jc-set-ctl input[type=password]{width:100%;max-width:320px}
.jc-set-help{color:#646970;font-size:12px;line-height:1.5}
.jc-radio{margin-right:16px}
.jc-pw{display:inline-flex;align-items:center;gap:6px;max-width:320px}
.jc-pw input{flex:1 1 auto}
.jc-pw-toggle{background:none;border:1px solid #c3c4c7;border-radius:6px;padding:5px 8px;cursor:pointer;color:#646970;display:inline-flex;align-items:center}
.jc-dropin-form{padding-top:6px}
.jc-dropin-form{margin:8px 0 4px}
.jc-note{color:#646970;font-size:13px;max-width:820px}
</style>';
	}

	// ------------------------------------------------------------------
	// Helpers.
	// ------------------------------------------------------------------

	/**
	 * 60s-cached health for admin_notices: the full health() does live Redis
	 * work (INFO + a key scan) and admin_notices runs on EVERY wp-admin
	 * request, so the uncached path must not run per-pageview.
	 */
	private function health_cached() {
		$t = get_transient( 'jabali_cache_admin_health' );
		if ( is_array( $t ) ) {
			return $t;
		}
		$h = $this->health();
		set_transient( 'jabali_cache_admin_health', $h, MINUTE_IN_SECONDS );
		return $h;
	}

	/**
	 * @return array<string,mixed>
	 */
	private function health() {
		$s   = Jabali_Cache_Settings::get();
		$cfg = Jabali_Cache_Config::load();
		$out = array(
			'enabled'    => (bool) $s['enabled'],
			'connected'  => false,
			'driver'     => '',
			'serializer' => function_exists( 'igbinary_serialize' ) ? 'igbinary' : 'php',
			'prefix'     => $cfg['prefix'],
			'target'     => ( 'unix' === $s['scheme'] ) ? $s['socket'] : ( $s['host'] . ':' . $s['port'] ),
			'keys'       => 0,
			'last_error' => '',
			'dropin_ok'  => false,
			'hint'       => '',
			'server'     => array(),
		);

		$mgr              = new Jabali_Cache_Dropin_Manager( $this->plugin_dir );
		$dstat            = $mgr->status();
		$out['dropin_ok'] = $dstat['object_ours'];

		$client = new Jabali_Cache_Client( $cfg );
		if ( $client->connect() ) {
			$out['connected']  = true;
			$out['driver']     = $client->driver();
			$kc                 = $client->stats_key_count( $cfg['prefix'] );
			$out['keys']        = (int) $kc['count'];
			$out['keys_approx'] = ! empty( $kc['approx'] );
			$out['server']     = $this->parse_info( $client->info() );
			$client->close();
		} else {
			$out['last_error'] = $client->last_error();
			$out['hint']       = $this->diagnose_hint( $cfg, $out['last_error'] );
		}
		return $out;
	}

	/**
	 * Detect a server-side full-page cache (the jabali nginx micro-cache) by a
	 * single anonymous loopback HEAD to the home URL, reading X-Jabali-Cache.
	 * Result is cached in a transient so the status screen never blocks on it.
	 * Returns array() when no server page cache is detected (e.g. off-jabali).
	 *
	 * @return array<string,string>
	 */
	private function probe_page_cache() {
		$cached = get_transient( 'jabali_cache_page_probe' );
		if ( is_array( $cached ) ) {
			return $cached;
		}
		$out  = array();
		// GET, not HEAD: the nginx micro-cache key embeds $request_method, so a
		// HEAD probe can never observe a HIT for the GET entries real visitors use.
		$resp = wp_remote_get(
			home_url( '/' ),
			array( 'timeout' => 3, 'redirection' => 0, 'sslverify' => false, 'limit_response_size' => 1048576, 'headers' => array( 'Accept' => 'text/html' ) )
		);
		if ( ! is_wp_error( $resp ) ) {
			$xjc = wp_remote_retrieve_header( $resp, 'x-jabali-cache' );
			if ( '' !== $xjc ) {
				$out['status']        = is_array( $xjc ) ? reset( $xjc ) : (string) $xjc;
				$out['cache_control'] = (string) wp_remote_retrieve_header( $resp, 'cache-control' );
				$out['etag']          = '' !== wp_remote_retrieve_header( $resp, 'etag' ) ? 'yes' : 'no';
				$out['last_modified'] = '' !== wp_remote_retrieve_header( $resp, 'last-modified' ) ? 'yes' : 'no';
			}
		}
		set_transient( 'jabali_cache_page_probe', $out, 5 * MINUTE_IN_SECONDS );
		return $out;
	}

	/**
	 * Pull a few operationally-useful fields out of a Redis INFO dump.
	 *
	 * @param string $raw
	 * @return array<string,string>
	 */
	private function parse_info( $raw ) {
		$want = array( 'redis_version', 'used_memory_human', 'maxmemory_policy', 'evicted_keys', 'connected_clients' );
		$map  = array();
		foreach ( preg_split( '/\r?\n/', (string) $raw ) as $line ) {
			$kv = explode( ':', $line, 2 );
			if ( 2 === count( $kv ) && in_array( $kv[0], $want, true ) ) {
				$map[ $kv[0] ] = trim( $kv[1] );
			}
		}
		return $map;
	}

	/**
	 * Translate a low-level connection failure into an operator-actionable hint
	 * specific to the jabali shared-hosting environment.
	 */
	private function diagnose_hint( array $cfg, $err ) {
		$err = strtolower( (string) $err );
		if ( 'unix' === $cfg['scheme'] ) {
			if ( false !== strpos( $err, 'open_basedir' ) || false !== strpos( $err, 'operation not permitted' ) ) {
				return 'PHP open_basedir is blocking the Redis socket. Add "/run/redis/redis.sock" (or "/run/redis") to this site\'s open_basedir, or ask the panel admin to allow it for the PHP-FPM pool.';
			}
			if ( false !== strpos( $err, 'permission denied' ) ) {
				return 'The PHP-FPM user cannot open the Redis socket. On the Jabali panel this is provisioned automatically when you enable caching for the site (Applications → cache toggle) — re-run that if you see this. On a standalone host, grant the site\'s PHP user read/write on the socket.';
			}
			if ( false !== strpos( $err, 'no such file' ) || false !== strpos( $err, 'connection refused' ) ) {
				return 'Redis socket /run/redis/redis.sock not found. Confirm redis-server is running on the panel host.';
			}
			return 'Could not connect to the Redis unix socket. Verify open_basedir includes /run/redis and that the site\'s PHP user can read the socket (the Jabali panel grants this automatically when caching is enabled).';
		}
		return 'Could not connect to Redis over TCP. Note: the jabali panel runs Redis on a unix socket only (no TCP) — prefer the Unix socket option.';
	}

	private function dropin_label( $installed, $ours, $foreign ) {
		if ( ! $installed ) {
			return '<span style="color:#b32d2e">Not installed</span>';
		}
		if ( $foreign ) {
			return '<span style="color:#b32d2e">A different object-cache.php is present (not ours)</span>';
		}
		return $ours ? '<span style="color:#1a7f37">Installed</span>' : 'Present';
	}

	private function guard() {
		if ( ! current_user_can( 'manage_options' ) ) {
			wp_die( esc_html__( 'Permission denied.', 'jabali-cache' ) );
		}
		check_admin_referer( self::NONCE );
	}

	private function redirect( $flash, array $extra = array() ) {
		$args = array_merge( array( 'page' => self::SLUG, 'jc_flash' => $flash ), $extra );
		wp_safe_redirect( add_query_arg( $args, admin_url( 'options-general.php' ) ) );
		exit;
	}

	private function render_flash() {
		if ( empty( $_GET['jc_flash'] ) ) { // phpcs:ignore WordPress.Security.NonceVerification.Recommended
			return;
		}
		$flash = sanitize_text_field( wp_unslash( $_GET['jc_flash'] ) ); // phpcs:ignore WordPress.Security.NonceVerification.Recommended
		$map   = array(
			'saved'             => array( 'success', 'Settings saved.' ),
			'flushed'           => array( 'success', 'Cache flushed.' ),
			'dropins_installed' => array( 'success', 'Drop-ins installed.' ),
			'dropins_removed'   => array( 'success', 'Drop-ins removed.' ),
			'dropins_failed'    => array( 'error', 'Could not install drop-ins (a foreign object-cache.php may be present, or wp-content is not writable).' ),
		);
		if ( isset( $map[ $flash ] ) ) {
			printf( '<div class="notice notice-%s is-dismissible"><p>%s</p></div>', esc_attr( $map[ $flash ][0] ), esc_html( $map[ $flash ][1] ) );
		}
	}

	private function row( $label, $value ) {
		echo '<tr><td style="width:220px"><strong>' . esc_html( $label ) . '</strong></td><td>' . $value . '</td></tr>'; // phpcs:ignore WordPress.Security.EscapeOutput.OutputNotEscaped
	}

	private function checkbox_row( $name, $label, $checked, $desc = '' ) {
		echo '<tr><th scope="row">' . esc_html( $label ) . '</th><td><label><input type="checkbox" name="' . esc_attr( $name ) . '" ' . checked( $checked, true, false ) . '> ' . esc_html( $desc ) . '</label></td></tr>';
	}

	private function text_row( $name, $label, $value, $ph = '' ) {
		echo '<tr><th scope="row">' . esc_html( $label ) . '</th><td><input type="text" class="regular-text" name="' . esc_attr( $name ) . '" value="' . esc_attr( $value ) . '" placeholder="' . esc_attr( $ph ) . '"></td></tr>';
	}

	private function password_row( $name, $label, $value ) {
		echo '<tr><th scope="row">' . esc_html( $label ) . '</th><td><input type="password" class="regular-text" name="' . esc_attr( $name ) . '" value="' . esc_attr( $value ) . '" autocomplete="new-password"></td></tr>';
	}

	private function number_row( $name, $label, $value ) {
		echo '<tr><th scope="row">' . esc_html( $label ) . '</th><td><input type="number" name="' . esc_attr( $name ) . '" value="' . esc_attr( (int) $value ) . '" class="small-text"></td></tr>';
	}
}
