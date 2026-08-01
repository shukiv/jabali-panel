<?php
/**
 * Plugin Name:       Jabali Cache
 * Plugin URI:        https://jabali-panel.com/
 * Description:        Redis-backed object cache (and optional full-page cache) tuned for the Jabali hosting panel. Uses the shared panel Redis over a unix socket with per-site key isolation. Works with or without the phpredis extension.
 * Version:           1.1.0
 * Requires at least: 5.6
 * Requires PHP:      7.4
 * Author:            Jabali Panel
 * License:           GPL-2.0-or-later
 * License URI:       https://www.gnu.org/licenses/gpl-2.0.html
 * Text Domain:       jabali-cache
 *
 * @package Jabali_Cache
 */

defined( 'ABSPATH' ) || exit;

if ( ! defined( 'JABALI_CACHE_VERSION' ) ) {
	define( 'JABALI_CACHE_VERSION', '1.1.0' );
}
if ( ! defined( 'JABALI_CACHE_PLUGIN_FILE' ) ) {
	define( 'JABALI_CACHE_PLUGIN_FILE', __FILE__ );
}
if ( ! defined( 'JABALI_CACHE_PLUGIN_DIR' ) ) {
	define( 'JABALI_CACHE_PLUGIN_DIR', __DIR__ );
}
if ( ! defined( 'JABALI_CACHE_NGINX_SPOOL' ) ) {
	define( 'JABALI_CACHE_NGINX_SPOOL', '/run/jabali-wp-purge' );
}

require_once __DIR__ . '/includes/lib.php';
require_once __DIR__ . '/includes/class-settings.php';
require_once __DIR__ . '/includes/class-dropin-manager.php';
require_once __DIR__ . '/includes/class-page-cache.php';

/**
 * Activation: seed settings, write the runtime config file, and install the
 * wp-content drop-ins. Drop-in install is best-effort — if wp-content is not
 * writable, the admin screen offers a manual "Install / repair" button.
 */
function jabali_cache_activate() {
	$settings = Jabali_Cache_Settings::get();
	Jabali_Cache_Settings::save( $settings ); // normalises + derives prefix + writes config file.

	$mgr = new Jabali_Cache_Dropin_Manager( JABALI_CACHE_PLUGIN_DIR );
	$mgr->install();

	set_transient( 'jabali_cache_activated', 1, 60 );
}
register_activation_hook( __FILE__, 'jabali_cache_activate' );

/**
 * Deactivation: remove our drop-ins (so WordPress falls back to core caching)
 * and drop the generated config file. Settings in the options table are kept
 * so re-activating restores the previous configuration.
 */
function jabali_cache_deactivate() {
	$mgr = new Jabali_Cache_Dropin_Manager( JABALI_CACHE_PLUGIN_DIR );
	$mgr->remove();
	Jabali_Cache_Settings::delete_config_file();
}
register_deactivation_hook( __FILE__, 'jabali_cache_deactivate' );

/**
 * Admin wiring.
 */
function jabali_cache_boot_admin() {
	require_once JABALI_CACHE_PLUGIN_DIR . '/includes/class-admin.php';
	$admin = new Jabali_Cache_Admin( JABALI_CACHE_PLUGIN_FILE );
	$admin->hooks();

	// One-time post-activation nudge if wp-config.php lacks WP_CACHE (only
	// matters for the optional page cache).
	if ( get_transient( 'jabali_cache_activated' ) ) {
		delete_transient( 'jabali_cache_activated' );
	}
}
if ( is_admin() ) {
	add_action( 'init', 'jabali_cache_boot_admin' );
}

/**
 * Conditional-request validators (Last-Modified + ETag) on cacheable front-end
 * responses, so returning visitors + CDNs revalidate with a 304 instead of a
 * full re-download. Gated on caching being enabled; never on admin, logged-in,
 * non-GET, feeds, REST, or error pages. nginx caches these on a MISS and honours
 * If-Modified-Since on HITs (no PHP-side 304 short-circuit, to stay clear of
 * REST/feed edge cases).
 */
function jabali_cache_send_validators() {
	$cfg = Jabali_Cache_Config::load();
	if ( empty( $cfg['enabled'] ) ) {
		return;
	}
	if ( is_admin() || is_user_logged_in() || is_feed() || is_404() ) {
		return;
	}
	$method = isset( $_SERVER['REQUEST_METHOD'] ) ? $_SERVER['REQUEST_METHOD'] : 'GET';
	if ( 'GET' !== $method && 'HEAD' !== $method ) {
		return;
	}
	if ( defined( 'REST_REQUEST' ) && REST_REQUEST ) {
		return;
	}
	$lm = get_lastpostmodified( 'GMT' );
	if ( ! $lm ) {
		return;
	}
	$ts      = strtotime( $lm . ' GMT' );
	$lastmod = gmdate( 'D, d M Y H:i:s', $ts ) . ' GMT';
	$uri     = isset( $_SERVER['REQUEST_URI'] ) ? sanitize_text_field( wp_unslash( $_SERVER['REQUEST_URI'] ) ) : '/';
	$etag    = '"' . md5( $lastmod . '|' . $uri ) . '"';
	header( 'Last-Modified: ' . $lastmod );
	header( 'ETag: ' . $etag );
}
add_action( 'template_redirect', 'jabali_cache_send_validators', 9 );

/**
 * WP-CLI wiring.
 */
if ( defined( 'WP_CLI' ) && WP_CLI ) {
	require_once JABALI_CACHE_PLUGIN_DIR . '/includes/class-cli.php';
	WP_CLI::add_command( 'jabali-cache', new Jabali_Cache_CLI( JABALI_CACHE_PLUGIN_DIR ) );
}

/**
 * Page-cache invalidation. When content changes, purge the full-page cache so
 * visitors never see a stale page. Object cache is invalidated by WordPress
 * core's normal wp_cache_delete calls, so it needs no extra hooks here.
 */
function jabali_cache_register_purge_hooks() {
	// Register whenever caching is enabled — NOT gated on page_cache. #611: the
	// panel-managed nginx FastCGI microcache is active independently of the
	// optional WP Redis full-page cache, and content changes must purge it. The
	// Redis full-page purge inside these hooks self-gates on page_cache (#603),
	// and the nginx purge (jabali_cache_purge_nginx) is a cheap no-op off Jabali.
	$cfg = Jabali_Cache_Config::load();
	// #611 intent: nginx micro-cache purges must fire even when the plugin's
	// master (object-cache) toggle is off — the spool dir existing is the
	// "panel page cache present" signal. The Redis-side purges keep
	// self-gating on enabled/page_cache inside their helpers.
	if ( empty( $cfg['enabled'] ) && ! @is_dir( JABALI_CACHE_NGINX_SPOOL ) ) {
		return;
	}
	$purge = 'jabali_cache_purge_pages';
	// Post edits: targeted purge of the post URL + home (GH #611/#619).
	add_action( 'save_post', 'jabali_cache_purge_post' );
	add_action( 'deleted_post', 'jabali_cache_purge_post' );
	add_action( 'trashed_post', 'jabali_cache_purge_post' );
	// Site-wide changes: whole-domain purge.
	add_action( 'comment_post', $purge );
	add_action( 'edit_comment', $purge );
	add_action( 'wp_set_comment_status', $purge );
	add_action( 'switch_theme', $purge );
	add_action( 'customize_save_after', $purge );
	add_action( 'updated_option', 'jabali_cache_maybe_purge_on_option', 10, 1 );
}
add_action( 'init', 'jabali_cache_register_purge_hooks' );

/**
 * Purge this site's Redis full-page cache entries.
 */
function jabali_cache_purge_redis() {
	// Only the optional WP Redis full-page cache lives here; skip when it's off.
	$cfg = Jabali_Cache_Config::load();
	if ( empty( $cfg['page_cache'] ) || ! class_exists( 'Jabali_Cache_Page_Cache' ) ) {
		return;
	}
	$pc = new Jabali_Cache_Page_Cache();
	$pc->purge_all();
}

/**
 * Ask the Jabali agent to purge the nginx FastCGI page cache (GH #611). Tenant
 * PHP can't touch the root-owned nginx cache, so we drop a purge request into
 * the agent's sticky spool dir; a root watcher validates host ownership and
 * runs the targeted purge (GH #619). No-op off Jabali (spool dir absent).
 *
 * @param array<int,string> $paths URL paths to purge; empty = whole domain.
 */
function jabali_cache_purge_nginx( $paths = array() ) {
	$dir = JABALI_CACHE_NGINX_SPOOL;
	if ( ! @is_dir( $dir ) || ! @is_writable( $dir ) ) { // @: open_basedir may hide /run (JAB-199) — treat as absent, no per-request warning.
		return; // not on a Jabali host, or spool not provisioned.
	}
	$host = wp_parse_url( home_url(), PHP_URL_HOST );
	if ( empty( $host ) ) {
		return;
	}
	$req = wp_json_encode(
		array(
			'host'  => $host,
			'paths' => array_values( array_unique( array_filter( array_map( 'strval', (array) $paths ) ) ) ),
		)
	);
	// Write to a temp name then atomically rename, so the watcher never reads a
	// half-written request.
	$base = $dir . '/' . uniqid( 'jc', true );
	if ( false === @file_put_contents( $base . '.tmp', $req, LOCK_EX ) ) {
		return;
	}
	@rename( $base . '.tmp', $base . '.json' );
}

/**
 * Purge every full-page cache entry for this site (Redis + whole-domain nginx).
 * Used for site-wide content changes (theme, options, comments).
 */
function jabali_cache_purge_pages() {
	jabali_cache_purge_redis();
	jabali_cache_purge_nginx();
}

/**
 * Purge caches for a single post: Redis (site-scoped) + a targeted nginx purge
 * of the post's own URL plus the home page, so a routine edit doesn't discard
 * the whole domain's hot cache.
 *
 * @param int $post_id
 */
function jabali_cache_purge_post( $post_id ) {
	// Autosaves and revisions fire save_post too (Elementor autosaves every
	// ~minute while editing). Purging on each one churns the warm cache and
	// floods the purge spool for content no anonymous visitor can see yet.
	if ( wp_is_post_revision( $post_id ) || wp_is_post_autosave( $post_id ) ) {
		return;
	}
	$jc_status = get_post_status( $post_id );
	if ( in_array( $jc_status, array( 'auto-draft', 'inherit' ), true ) ) {
		return;
	}
	// JAB-91: build the affected paths first (home + the post permalink), then
	// purge ONLY those from the Redis page cache — a single post edit must not
	// discard the whole site's warm cache. Whole-site Redis purge stays for
	// site-wide changes (jabali_cache_purge_pages via theme/customize/option
	// hooks). The nginx purge was already path-targeted.
	$paths = array( '/' );
	$link  = get_permalink( (int) $post_id );
	if ( $link ) {
		$p = wp_parse_url( $link, PHP_URL_PATH );
		if ( ! empty( $p ) ) {
			$paths[] = $p;
		}
	}
	jabali_cache_purge_redis_paths( $paths );
	jabali_cache_purge_nginx( $paths );
}

/**
 * Targeted Redis page-cache purge for specific URL paths (JAB-91). No-op when
 * the optional Redis page cache is off.
 *
 * @param array<int,string> $paths
 */
function jabali_cache_purge_redis_paths( array $paths ) {
	$cfg = Jabali_Cache_Config::load();
	if ( empty( $cfg['page_cache'] ) || ! class_exists( 'Jabali_Cache_Page_Cache' ) ) {
		return;
	}
	$pc = new Jabali_Cache_Page_Cache();
	$pc->purge_paths( $paths );
}

/**
 * Purge pages when a content-affecting option changes (e.g. permalinks,
 * blogname, site URL).
 *
 * @param string $option
 */
function jabali_cache_maybe_purge_on_option( $option ) {
	static $watched = array( 'permalink_structure', 'blogname', 'home', 'siteurl', 'page_on_front', 'show_on_front' );
	if ( in_array( $option, $watched, true ) ) {
		jabali_cache_purge_pages();
	}
}
