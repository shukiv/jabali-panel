<?php
/**
 * Plugin Name:       Jabali Migrator
 * Plugin URI:        https://jabali-panel.com/
 * Description:       Migrate this WordPress site into a Jabali panel — no SSH. Generates a one-time token; the Jabali panel PULLS the database + files over an authenticated REST API. Pairs with the Jabali panel's WordPress-plugin migration flow (GH #648).
 * Version:           0.1.0
 * Requires at least: 5.6
 * Requires PHP:      7.4
 * Author:            Jabali
 * License:           GPL-2.0-or-later
 * Text Domain:       jabali-migrator
 *
 * SECURITY MODEL (pull): the panel authenticates every request with a Bearer
 * token whose SHA-256 hash is stored in an option (plaintext shown ONCE). All
 * endpoints require it (constant-time compare). DB credentials are NEVER sent to
 * the browser; the DB export runs server-side via mysqldump with a temp
 * defaults-file (password never on the command line). This is a FIRST CUT:
 * resumable chunked transport + a pure-PHP export fallback are follow-ups.
 */

if ( ! defined( 'ABSPATH' ) ) {
	exit;
}

define( 'JABALI_MIGRATOR_VERSION', '0.1.0' );
define( 'JABALI_MIGRATOR_NS', 'jabali-migrator/v1' );
define( 'JABALI_MIGRATOR_TOKEN_OPTION', 'jabali_migrator_token_hash' );

/**
 * Generate a fresh 256-bit token, store ONLY its hash, return the plaintext once.
 */
function jabali_migrator_generate_token() {
	$token = bin2hex( random_bytes( 32 ) );
	update_option( JABALI_MIGRATOR_TOKEN_OPTION, hash( 'sha256', $token ), false );
	return $token;
}

/**
 * Constant-time Bearer-token check — the permission callback for every route.
 */
function jabali_migrator_authorized( WP_REST_Request $request ) {
	$stored = get_option( JABALI_MIGRATOR_TOKEN_OPTION, '' );
	if ( ! is_string( $stored ) || '' === $stored ) {
		return new WP_Error( 'jabali_no_token', 'No migration token configured.', array( 'status' => 403 ) );
	}
	$header = (string) $request->get_header( 'authorization' );
	if ( 0 !== stripos( $header, 'Bearer ' ) ) {
		return new WP_Error( 'jabali_no_bearer', 'Missing Bearer token.', array( 'status' => 401 ) );
	}
	$presented = trim( substr( $header, 7 ) );
	if ( ! hash_equals( $stored, hash( 'sha256', $presented ) ) ) {
		return new WP_Error( 'jabali_bad_token', 'Invalid migration token.', array( 'status' => 401 ) );
	}
	return true;
}

add_action( 'rest_api_init', 'jabali_migrator_register_routes' );
function jabali_migrator_register_routes() {
	$auth = 'jabali_migrator_authorized';

	register_rest_route( JABALI_MIGRATOR_NS, '/ping', array(
		'methods'             => 'GET',
		'permission_callback' => $auth,
		'callback'            => 'jabali_migrator_ping',
	) );

	register_rest_route( JABALI_MIGRATOR_NS, '/manifest', array(
		'methods'             => 'GET',
		'permission_callback' => $auth,
		'callback'            => 'jabali_migrator_manifest',
	) );

	register_rest_route( JABALI_MIGRATOR_NS, '/db-export', array(
		'methods'             => 'GET',
		'permission_callback' => $auth,
		'callback'            => 'jabali_migrator_db_export',
	) );

	register_rest_route( JABALI_MIGRATOR_NS, '/files-manifest', array(
		'methods'             => 'GET',
		'permission_callback' => $auth,
		'callback'            => 'jabali_migrator_files_manifest',
	) );

	register_rest_route( JABALI_MIGRATOR_NS, '/file', array(
		'methods'             => 'GET',
		'permission_callback' => $auth,
		'callback'            => 'jabali_migrator_file',
		'args'                => array(
			'path' => array( 'required' => true, 'type' => 'string' ),
		),
	) );
}

/** ping — cheap reachability + version probe. */
function jabali_migrator_ping() {
	return array(
		'ok'          => true,
		'plugin'      => 'jabali-migrator',
		'version'     => JABALI_MIGRATOR_VERSION,
		'wp_version'  => get_bloginfo( 'version' ),
		'php_version' => PHP_VERSION,
		'site_url'    => site_url(),
	);
}

/** manifest — everything the panel needs to plan capacity + the import. */
function jabali_migrator_manifest() {
	global $wpdb, $table_prefix;

	// DB size estimate from information_schema (no full scan).
	$db_bytes = 0;
	$rows = $wpdb->get_results(
		$wpdb->prepare(
			'SELECT SUM(data_length + index_length) AS b FROM information_schema.TABLES WHERE table_schema = %s',
			DB_NAME
		)
	);
	if ( ! empty( $rows ) && isset( $rows[0]->b ) ) {
		$db_bytes = (int) $rows[0]->b;
	}

	list( $file_count, $file_bytes ) = jabali_migrator_dir_stats( ABSPATH );

	return array(
		'home'          => get_option( 'home' ),
		'siteurl'       => get_option( 'siteurl' ),
		'table_prefix'  => $table_prefix,
		'wp_version'    => get_bloginfo( 'version' ),
		'php_version'   => PHP_VERSION,
		'multisite'     => is_multisite(),
		'db_bytes'      => $db_bytes,
		'file_count'    => $file_count,
		'file_bytes'    => $file_bytes,
		'wp_root'       => untrailingslashit( ABSPATH ),
		'active_theme'  => (string) get_option( 'stylesheet' ),
		'active_plugins'=> (array) get_option( 'active_plugins', array() ),
	);
}

/**
 * db-export — stream a mysqldump of this site's DB. Credentials come from
 * wp-config (DB_* constants); the password goes into a 0600 temp
 * defaults-extra-file, NEVER onto the command line (argv is world-readable via
 * /proc). The temp file is unlinked immediately after the process starts.
 * FIRST CUT: whole dump in one response (no range chunking yet); requires
 * shell_exec + mysqldump. A pure-PHP $wpdb fallback is a follow-up.
 */
function jabali_migrator_db_export() {
	if ( ! function_exists( 'proc_open' ) ) {
		return new WP_Error( 'jabali_no_procopen', 'proc_open is disabled; the pure-PHP export fallback is not yet available.', array( 'status' => 501 ) );
	}
	$mysqldump = jabali_migrator_find_bin( 'mysqldump' );
	if ( '' === $mysqldump ) {
		return new WP_Error( 'jabali_no_mysqldump', 'mysqldump not found; the pure-PHP export fallback is not yet available.', array( 'status' => 501 ) );
	}

	$cnf = tempnam( sys_get_temp_dir(), 'jabmig' );
	if ( false === $cnf ) {
		return new WP_Error( 'jabali_tmp', 'Could not create temp file.', array( 'status' => 500 ) );
	}
	chmod( $cnf, 0600 );
	// password only in the (0600) file, never in argv.
	file_put_contents( $cnf, "[client]\npassword=" . DB_PASSWORD . "\n" );

	$host = DB_HOST;
	$port = '';
	if ( false !== strpos( $host, ':' ) ) {
		list( $host, $port ) = explode( ':', $host, 2 );
	}
	$args = array(
		$mysqldump,
		'--defaults-extra-file=' . $cnf,
		'--user=' . DB_USER,
		'--host=' . $host,
		'--single-transaction', '--quick', '--skip-lock-tables', '--no-tablespaces',
	);
	if ( '' !== $port && is_numeric( $port ) ) {
		$args[] = '--port=' . $port;
	}
	$args[] = DB_NAME;

	// Stream stdout straight to the client; unlink the cnf once running.
	nocache_headers();
	header( 'Content-Type: application/sql; charset=utf-8' );
	header( 'Content-Disposition: attachment; filename="jabali-migrator-db.sql"' );

	$descriptors = array( 1 => array( 'pipe', 'w' ), 2 => array( 'pipe', 'w' ) );
	$proc = proc_open( jabali_migrator_argv_to_cmd( $args ), $descriptors, $pipes );
	@unlink( $cnf );
	if ( ! is_resource( $proc ) ) {
		return new WP_Error( 'jabali_dump_spawn', 'Could not start mysqldump.', array( 'status' => 500 ) );
	}
	while ( ! feof( $pipes[1] ) ) {
		echo fread( $pipes[1], 65536 );
		flush();
	}
	fclose( $pipes[1] );
	fclose( $pipes[2] );
	proc_close( $proc );
	exit; // raw stream — bypass the REST JSON envelope.
}

/** files-manifest — flat list of files under ABSPATH with size + sha1. */
function jabali_migrator_files_manifest() {
	$root  = untrailingslashit( ABSPATH );
	$out   = array();
	$it    = new RecursiveIteratorIterator(
		new RecursiveDirectoryIterator( $root, FilesystemIterator::SKIP_DOTS ),
		RecursiveIteratorIterator::LEAVES_ONLY
	);
	$skip  = array( '/wp-content/cache/', '/wp-content/object-cache.php', '/wp-content/advanced-cache.php' );
	foreach ( $it as $file ) {
		$abs = $file->getPathname();
		$rel = ltrim( substr( $abs, strlen( $root ) ), '/' );
		$hit = false;
		foreach ( $skip as $s ) {
			if ( false !== strpos( '/' . $rel, $s ) ) { $hit = true; break; }
		}
		if ( $hit || ! $file->isFile() ) {
			continue;
		}
		$out[] = array( 'path' => $rel, 'size' => $file->getSize() );
	}
	return array( 'root' => $root, 'files' => $out );
}

/**
 * file — return one file's bytes. Path is validated to stay UNDER ABSPATH
 * (realpath containment) so the token can never read outside the WP tree.
 */
function jabali_migrator_file( WP_REST_Request $request ) {
	$root = realpath( untrailingslashit( ABSPATH ) );
	$rel  = (string) $request->get_param( 'path' );
	$abs  = realpath( $root . '/' . ltrim( $rel, '/' ) );
	if ( false === $abs || 0 !== strpos( $abs, $root . '/' ) || ! is_file( $abs ) ) {
		return new WP_Error( 'jabali_bad_path', 'Path outside the WordPress root or not a file.', array( 'status' => 400 ) );
	}
	nocache_headers();
	header( 'Content-Type: application/octet-stream' );
	header( 'Content-Length: ' . filesize( $abs ) );
	readfile( $abs );
	exit;
}

/* ------------------------------- helpers -------------------------------- */

function jabali_migrator_dir_stats( $root ) {
	$count = 0; $bytes = 0;
	try {
		$it = new RecursiveIteratorIterator(
			new RecursiveDirectoryIterator( $root, FilesystemIterator::SKIP_DOTS ),
			RecursiveIteratorIterator::LEAVES_ONLY
		);
		foreach ( $it as $f ) {
			if ( $f->isFile() ) { $count++; $bytes += $f->getSize(); }
		}
	} catch ( Exception $e ) {
		// best-effort estimate
	}
	return array( $count, $bytes );
}

function jabali_migrator_find_bin( $name ) {
	foreach ( array( '/usr/bin/', '/usr/local/bin/', '/bin/' ) as $d ) {
		if ( is_executable( $d . $name ) ) { return $d . $name; }
	}
	return '';
}

/** Build a shell command line from an argv array, each arg single-quoted. */
function jabali_migrator_argv_to_cmd( array $argv ) {
	return implode( ' ', array_map( 'escapeshellarg', $argv ) );
}

/* --------------------------- admin token UI ----------------------------- */

add_action( 'admin_menu', 'jabali_migrator_admin_menu' );
function jabali_migrator_admin_menu() {
	add_management_page(
		'Jabali Migrator', 'Jabali Migrator', 'manage_options',
		'jabali-migrator', 'jabali_migrator_admin_page'
	);
}

function jabali_migrator_admin_page() {
	if ( ! current_user_can( 'manage_options' ) ) { return; }
	$new_token = '';
	if ( isset( $_POST['jabali_migrator_gen'] ) && check_admin_referer( 'jabali_migrator_gen' ) ) {
		$new_token = jabali_migrator_generate_token();
	}
	$base = esc_url( rest_url( JABALI_MIGRATOR_NS ) );
	echo '<div class="wrap"><h1>Jabali Migrator</h1>';
	echo '<p>Generate a token, then paste it (and this site URL) into your Jabali panel\'s WordPress migration screen. The panel will pull this site over the token-authenticated REST API below.</p>';
	echo '<form method="post">';
	wp_nonce_field( 'jabali_migrator_gen' );
	echo '<p><button class="button button-primary" name="jabali_migrator_gen" value="1">Generate migration token</button></p>';
	echo '</form>';
	if ( '' !== $new_token ) {
		echo '<div class="notice notice-warning"><p><strong>Copy this token now — it is shown only once:</strong></p>';
		echo '<p><code style="user-select:all;font-size:14px">' . esc_html( $new_token ) . '</code></p></div>';
	} elseif ( get_option( JABALI_MIGRATOR_TOKEN_OPTION, '' ) ) {
		echo '<p><em>A token is configured (hidden). Generate a new one to rotate; the old one stops working.</em></p>';
	}
	echo '<h2>REST base</h2><p><code>' . $base . '</code></p>';
	echo '</div>';
}

/** On deactivate, drop the token so a stale one can\'t be reused. */
register_deactivation_hook( __FILE__, function () {
	delete_option( JABALI_MIGRATOR_TOKEN_OPTION );
} );
