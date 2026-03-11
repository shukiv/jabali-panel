<?php
/**
 * Plugin Name: Jabali Cache
 * Plugin URI: https://github.com/shukiv/jabali-panel
 * Description: High-performance caching for WordPress - Redis object cache, nginx page cache, lazy loading, and more.
 * Version: 2.6.0
 * Author: Jabali Panel
 * Author URI: https://github.com/shukiv/jabali-panel
 * License: GPL-2.0+
 * License URI: https://www.gnu.org/licenses/gpl-2.0.html
 * Text Domain: jabali-cache
 */
defined('ABSPATH') || exit;

class Jabali_Cache_Plugin
{
    const VERSION = '2.6.0';

    const OPTION_KEY = 'jabali_cache_settings';

    private static $instance = null;

    private $settings = [];

    public static function get_instance()
    {
        if (self::$instance === null) {
            self::$instance = new self;
        }

        return self::$instance;
    }

    private function __construct()
    {
        $this->settings = $this->get_settings();

        add_action('admin_menu', [$this, 'add_admin_menu']);
        add_action('admin_init', [$this, 'register_settings']);
        add_action('admin_init', [$this, 'handle_actions']);
        add_action('admin_bar_menu', [$this, 'add_admin_bar_menu'], 100);
        add_filter('plugin_action_links_'.plugin_basename(__FILE__), [$this, 'add_action_links']);
        add_action('admin_enqueue_scripts', [$this, 'admin_styles']);

        // Initialize optimization features
        $this->init_optimizations();

        // Initialize plugin compatibility hooks for cache invalidation
        $this->init_plugin_compatibility();

        // Initialize smart page cache purge hooks
        $this->init_page_cache_purge_hooks();
    }

    /**
     * Initialize hooks for smart page cache purging
     * Automatically purges nginx cache when content changes
     */
    private function init_page_cache_purge_hooks()
    {
        // Only initialize if page cache is enabled
        if (! $this->settings['page_cache']) {
            return;
        }

        // Post changes
        add_action('save_post', [$this, 'purge_post_cache'], 10, 3);
        add_action('wp_trash_post', [$this, 'purge_post_cache_on_delete']);
        add_action('delete_post', [$this, 'purge_post_cache_on_delete']);
        add_action('edit_post', [$this, 'purge_post_cache_on_edit'], 10, 2);

        // Comment changes
        add_action('comment_post', [$this, 'purge_comment_cache'], 10, 2);
        add_action('edit_comment', [$this, 'purge_comment_cache']);
        add_action('wp_set_comment_status', [$this, 'purge_comment_cache']);

        // Menu changes
        add_action('wp_update_nav_menu', [$this, 'purge_all_cache']);

        // Theme/Customizer changes
        add_action('switch_theme', [$this, 'purge_all_cache']);
        add_action('customize_save_after', [$this, 'purge_all_cache']);
        add_action('update_option_theme_mods_'.get_template(), [$this, 'purge_all_cache']);

        // Sidebar/Widget changes
        add_action('update_option_sidebars_widgets', [$this, 'purge_all_cache']);

        // Permalink structure changes
        add_action('update_option_permalink_structure', [$this, 'purge_all_cache']);

        // WooCommerce specific hooks
        if (class_exists('WooCommerce')) {
            add_action('woocommerce_product_set_stock', [$this, 'purge_woocommerce_product']);
            add_action('woocommerce_variation_set_stock', [$this, 'purge_woocommerce_product']);
            add_action('woocommerce_product_set_stock_status', [$this, 'purge_woocommerce_product_stock_status'], 10, 3);
            add_action('woocommerce_update_product', [$this, 'purge_woocommerce_product_by_id']);
            add_action('woocommerce_new_product', [$this, 'purge_woocommerce_product_by_id']);
            add_action('woocommerce_delete_product', [$this, 'purge_woocommerce_product_by_id']);
            add_action('woocommerce_trash_product', [$this, 'purge_woocommerce_product_by_id']);
        }
    }

    /**
     * Purge cache for a specific post and related pages
     */
    public function purge_post_cache($post_id, $post = null, $update = true)
    {
        // Skip autosaves and revisions
        if (defined('DOING_AUTOSAVE') && DOING_AUTOSAVE) {
            return;
        }

        if (wp_is_post_revision($post_id) || wp_is_post_autosave($post_id)) {
            return;
        }

        if (! $post) {
            $post = get_post($post_id);
        }

        if (! $post || $post->post_status === 'auto-draft') {
            return;
        }

        $paths_to_purge = $this->get_post_related_paths($post);
        $this->purge_paths($paths_to_purge);
    }

    /**
     * Purge cache when post is deleted or trashed
     */
    public function purge_post_cache_on_delete($post_id)
    {
        $post = get_post($post_id);
        if (! $post) {
            return;
        }

        $paths_to_purge = $this->get_post_related_paths($post);
        $this->purge_paths($paths_to_purge);
    }

    /**
     * Purge cache on post edit
     */
    public function purge_post_cache_on_edit($post_id, $post)
    {
        $this->purge_post_cache($post_id, $post, true);
    }

    /**
     * Purge cache when a comment changes
     */
    public function purge_comment_cache($comment_id, $approved = null)
    {
        $comment = get_comment($comment_id);
        if (! $comment) {
            return;
        }

        $post = get_post($comment->comment_post_ID);
        if ($post) {
            $paths_to_purge = [get_permalink($post)];
            $this->purge_paths($paths_to_purge);
        }
    }

    /**
     * Get all paths related to a post that should be purged
     */
    private function get_post_related_paths($post)
    {
        $paths = [];

        // Post URL
        $permalink = get_permalink($post);
        if ($permalink) {
            $paths[] = $permalink;
        }

        // Home page
        $paths[] = home_url('/');

        // Blog page (if different from home)
        $blog_page_id = get_option('page_for_posts');
        if ($blog_page_id) {
            $paths[] = get_permalink($blog_page_id);
        }

        // Category archives
        $categories = get_the_category($post->ID);
        if ($categories) {
            foreach ($categories as $category) {
                $paths[] = get_category_link($category->term_id);
            }
        }

        // Tag archives
        $tags = get_the_tags($post->ID);
        if ($tags) {
            foreach ($tags as $tag) {
                $paths[] = get_tag_link($tag->term_id);
            }
        }

        // Author archive
        $paths[] = get_author_posts_url($post->post_author);

        // Date archives
        $post_date = strtotime($post->post_date);
        $paths[] = get_year_link(date('Y', $post_date));
        $paths[] = get_month_link(date('Y', $post_date), date('m', $post_date));
        $paths[] = get_day_link(date('Y', $post_date), date('m', $post_date), date('d', $post_date));

        // Post type archive (for custom post types)
        if ($post->post_type !== 'post' && $post->post_type !== 'page') {
            $archive_link = get_post_type_archive_link($post->post_type);
            if ($archive_link) {
                $paths[] = $archive_link;
            }
        }

        // Taxonomy archives for custom post types
        $taxonomies = get_object_taxonomies($post->post_type);
        foreach ($taxonomies as $taxonomy) {
            if ($taxonomy !== 'category' && $taxonomy !== 'post_tag') {
                $terms = get_the_terms($post->ID, $taxonomy);
                if ($terms && ! is_wp_error($terms)) {
                    foreach ($terms as $term) {
                        $paths[] = get_term_link($term);
                    }
                }
            }
        }

        // Remove duplicates and filter
        $paths = array_unique(array_filter($paths, function ($path) {
            return ! is_wp_error($path) && ! empty($path);
        }));

        return $paths;
    }

    /**
     * Purge cache for WooCommerce product (from stock change)
     */
    public function purge_woocommerce_product($product)
    {
        if (is_numeric($product)) {
            $product_id = $product;
        } else {
            $product_id = $product->get_id();
        }
        $this->purge_woocommerce_product_by_id($product_id);
    }

    /**
     * Purge cache for WooCommerce product stock status change
     */
    public function purge_woocommerce_product_stock_status($product_id, $stock_status, $product)
    {
        $this->purge_woocommerce_product_by_id($product_id);
    }

    /**
     * Purge cache for WooCommerce product by ID
     */
    public function purge_woocommerce_product_by_id($product_id)
    {
        $paths = [];

        // Product URL
        $permalink = get_permalink($product_id);
        if ($permalink) {
            $paths[] = $permalink;
        }

        // Home page
        $paths[] = home_url('/');

        // Shop page
        $shop_page_id = wc_get_page_id('shop');
        if ($shop_page_id > 0) {
            $paths[] = get_permalink($shop_page_id);
        }

        // Product categories
        $terms = get_the_terms($product_id, 'product_cat');
        if ($terms && ! is_wp_error($terms)) {
            foreach ($terms as $term) {
                $paths[] = get_term_link($term);
            }
        }

        // Product tags
        $tags = get_the_terms($product_id, 'product_tag');
        if ($tags && ! is_wp_error($tags)) {
            foreach ($tags as $tag) {
                $paths[] = get_term_link($tag);
            }
        }

        $paths = array_unique(array_filter($paths, function ($path) {
            return ! is_wp_error($path) && ! empty($path);
        }));

        $this->purge_paths($paths);
    }

    /**
     * Purge all cache (for site-wide changes)
     */
    public function purge_all_cache()
    {
        $this->purge_paths(['/']);
    }

    /**
     * Send purge request to Jabali Panel API
     */
    private function purge_paths(array $paths)
    {
        if (empty($paths)) {
            return false;
        }

        // Get domain from site URL
        $site_url = get_site_url();
        $parsed = parse_url($site_url);
        $domain = $parsed['host'] ?? '';

        if (empty($domain)) {
            return false;
        }

        // Generate secret from AUTH_KEY
        $secret = defined('AUTH_KEY') ? hash('sha256', AUTH_KEY) : '';
        if (empty($secret)) {
            return false;
        }

        // Convert full URLs to paths
        $normalized_paths = [];
        foreach ($paths as $url) {
            if ($url === '/') {
                $normalized_paths[] = '/';
            } else {
                $parsed_url = parse_url($url);
                $path = $parsed_url['path'] ?? '/';
                $normalized_paths[] = $path;
            }
        }

        $normalized_paths = array_unique($normalized_paths);

        // Check if we should purge all (if home is in the list)
        $purge_all = in_array('/', $normalized_paths) && count($normalized_paths) > 5;

        // Call Jabali Panel internal API
        $response = wp_remote_post('https://127.0.0.1/api/internal/page-cache-purge', [
            'timeout' => 10,
            'sslverify' => false,
            'headers' => [
                'Content-Type' => 'application/json',
                'Accept' => 'application/json',
            ],
            'body' => json_encode([
                'domain' => $domain,
                'paths' => $purge_all ? [] : $normalized_paths,
                'purge_all' => $purge_all,
                'secret' => $secret,
            ]),
        ]);

        if (is_wp_error($response)) {
            if (defined('WP_DEBUG') && WP_DEBUG) {
                error_log('Jabali Cache: Failed to purge page cache - '.$response->get_error_message());
            }

            return false;
        }

        $code = wp_remote_retrieve_response_code($response);
        if ($code !== 200) {
            if (defined('WP_DEBUG') && WP_DEBUG) {
                $body = wp_remote_retrieve_body($response);
                error_log('Jabali Cache: Failed to purge page cache - HTTP '.$code.' - '.$body);
            }

            return false;
        }

        if (defined('WP_DEBUG') && WP_DEBUG) {
            error_log('Jabali Cache: Purged '.count($normalized_paths).' paths');
        }

        return true;
    }

    /**
     * Initialize hooks for theme/plugin asset regeneration
     * Uses general WordPress hooks instead of plugin-specific ones
     */
    private function init_plugin_compatibility()
    {
        // General: Catch ALL option updates that might trigger CSS/JS regeneration
        add_action('updated_option', [$this, 'on_option_updated'], 10, 3);

        // General: Theme/plugin updates
        add_action('upgrader_process_complete', [$this, 'flush_all_builder_cache'], 10, 2);
        add_action('switch_theme', [$this, 'flush_all_builder_cache']);
        add_action('customize_save_after', [$this, 'flush_all_builder_cache']);

        // General: When any cache clear action is triggered by popular builders
        // These are catch-all hooks that most builders fire
        add_action('wp_cache_flush', [$this, 'flush_all_builder_cache']);
        add_action('litespeed_purge_all', [$this, 'flush_all_builder_cache']);

        // WooCommerce product changes (affects displayed content)
        add_action('woocommerce_delete_product_transients', [$this, 'flush_woocommerce_cache']);
        add_action('woocommerce_clear_product_transients', [$this, 'flush_woocommerce_cache']);
    }

    /**
     * Patterns in option names that indicate CSS/JS regeneration needed
     */
    private function get_builder_option_patterns(): array
    {
        return [
            // Page builders
            'elementor', 'uagb', 'spectra', 'fl_builder', 'beaver', 'et_divi', 'et_builder',
            'vc_', 'wpb_', 'generateblocks', 'kadence', 'brizy', 'oxygen', 'breakdance',
            // Themes
            'astra', 'theme_mods_', 'flavor',
            // Optimization plugins
            'autoptimize', 'wp_rocket', 'sg_optimizer', 'litespeed', 'w3tc', 'wp_optimize',
            // CSS/JS settings
            '_css', '_js', '_style', '_script', '_assets', '_cache',
        ];
    }

    /**
     * Check if option update should trigger cache flush
     */
    public function on_option_updated($option, $old_value, $new_value)
    {
        // Skip if values are identical
        if ($old_value === $new_value) {
            return;
        }

        $option_lower = strtolower($option);
        foreach ($this->get_builder_option_patterns() as $pattern) {
            if (strpos($option_lower, $pattern) !== false) {
                $this->flush_all_builder_cache();

                return;
            }
        }
    }

    /**
     * Flush ALL builder-related transients and page cache
     * Single method that handles all builders/themes
     */
    public function flush_all_builder_cache($upgrader = null, $options = null)
    {
        // Prevent multiple flushes in same request
        static $flushed = false;
        if ($flushed) {
            return;
        }
        $flushed = true;

        // Flush Redis transients matching any builder pattern
        $patterns = [
            // Page builders
            'elementor_', '_elementor', 'uagb_', '_uagb', 'spectra_', '_spectra',
            'fl_builder_', '_fl_builder', 'et_core_', 'et_builder_', '_et_core', '_et_builder',
            'vc_', '_vc_', 'generateblocks_', 'kadence_', 'kt_', 'brizy_', 'oxygen_', 'breakdance_',
            // Themes
            'astra_', '_astra', 'theme_mod_',
            // Optimization
            'autoptimize_', 'wprocket_', 'litespeed_',
            // Generic
            '_css_', '_js_', '_style_', '_assets_',
        ];

        $this->flush_transients_by_patterns($patterns);

        // Purge nginx page cache
        if ($this->settings['page_cache']) {
            $this->purge_all_cache();
        }

        $this->log_cache_flush('Builder/Theme');
    }

    /**
     * Flush WooCommerce related transients
     */
    public function flush_woocommerce_cache()
    {
        $this->flush_transients_by_patterns(['wc_', '_wc_', 'woocommerce_']);
        $this->log_cache_flush('WooCommerce');
    }

    /**
     * Flush transients matching specific patterns from Redis
     *
     * @param  array  $patterns  Array of patterns to match (e.g., ['uagb_', 'spectra_'])
     * @return int Number of keys deleted
     */
    private function flush_transients_by_patterns(array $patterns)
    {
        if (! $this->is_redis_connected()) {
            return 0;
        }

        try {
            $redis = new Redis;
            $host = defined('JABALI_CACHE_HOST') ? JABALI_CACHE_HOST : '127.0.0.1';
            $port = defined('JABALI_CACHE_PORT') ? JABALI_CACHE_PORT : 6379;
            $db = defined('JABALI_CACHE_DATABASE') ? JABALI_CACHE_DATABASE : 0;
            $prefix = defined('JABALI_CACHE_PREFIX') ? JABALI_CACHE_PREFIX : '';

            if (! $redis->connect($host, $port, 2)) {
                return 0;
            }

            // Authenticate if credentials are defined
            if (defined('JABALI_CACHE_REDIS_USER') && defined('JABALI_CACHE_REDIS_PASS')) {
                $redis->auth(['user' => JABALI_CACHE_REDIS_USER, 'pass' => JABALI_CACHE_REDIS_PASS]);
            } elseif (defined('JABALI_CACHE_REDIS_PASS') && JABALI_CACHE_REDIS_PASS) {
                $redis->auth(JABALI_CACHE_REDIS_PASS);
            }

            $redis->select($db);

            $deleted = 0;
            foreach ($patterns as $pattern) {
                // Search for transient keys matching the pattern
                // WordPress stores transients as: {prefix}transient_{name} and {prefix}_transient_{name}
                $scan_patterns = [
                    $prefix.'*transient*'.$pattern.'*',
                    $prefix.'*'.$pattern.'*transient*',
                    $prefix.'*'.$pattern.'*',
                ];

                foreach ($scan_patterns as $scan_pattern) {
                    $cursor = null;
                    do {
                        $keys = $redis->scan($cursor, $scan_pattern, 100);
                        if ($keys !== false && ! empty($keys)) {
                            foreach ($keys as $key) {
                                $redis->del($key);
                                $deleted++;
                            }
                        }
                    } while ($cursor > 0);
                }
            }

            $redis->close();

            return $deleted;

        } catch (Exception $e) {
            error_log('Jabali Cache: Failed to flush transients - '.$e->getMessage());

            return 0;
        }
    }

    /**
     * Log cache flush event for debugging
     */
    private function log_cache_flush($plugin_name)
    {
        if (defined('WP_DEBUG') && WP_DEBUG) {
            error_log(sprintf('Jabali Cache: Flushed Redis transients triggered by %s asset regeneration', $plugin_name));
        }
    }

    public function get_settings()
    {
        $defaults = [
            'page_cache' => true,
            'object_cache' => true,
            'html_minify' => false,
            'minify_css' => true,
            'minify_js' => true,
            'lazy_load' => false,
            'lazy_load_iframes' => false,
            'browser_cache' => false,
            'expired_cache' => false,
            'remove_query_strings' => false,
            'disable_emojis' => false,
            'disable_embeds' => false,
            'defer_js' => false,
            'cache_debug' => true,
            'local_google_fonts' => false,
            // LCP Optimization settings
            'lcp_preload' => true,
            'preconnect_hints' => true,
            'preload_fonts' => true,
            'delay_third_party' => false,
        ];
        $saved = get_option(self::OPTION_KEY, []);

        return array_merge($defaults, $saved);
    }

    public function init_optimizations()
    {
        // Page Cache bypass - send no-cache headers when disabled
        if (! $this->settings['page_cache'] && ! is_admin()) {
            add_action('send_headers', [$this, 'disable_page_cache_headers'], 1);
        }

        // HTML Minification
        if ($this->settings['html_minify'] && ! is_admin()) {
            add_action('template_redirect', [$this, 'start_html_buffer'], 1);
        }

        // CSS Minification
        if ($this->settings['minify_css'] && ! is_admin()) {
            add_filter('style_loader_tag', [$this, 'minify_inline_css'], 10, 4);
            add_action('wp_head', [$this, 'start_css_capture'], 1);
            add_action('wp_head', [$this, 'end_css_capture'], 999);
        }

        // JS Minification
        if ($this->settings['minify_js'] && ! is_admin()) {
            add_filter('script_loader_tag', [$this, 'minify_inline_js'], 10, 3);
        }

        // Defer JS
        if ($this->settings['defer_js'] && ! is_admin()) {
            add_filter('script_loader_tag', [$this, 'defer_js'], 10, 3);
        }

        // Lazy Loading
        if ($this->settings['lazy_load'] && ! is_admin()) {
            add_filter('the_content', [$this, 'add_lazy_loading']);
            add_filter('post_thumbnail_html', [$this, 'add_lazy_loading']);
            add_filter('get_avatar', [$this, 'add_lazy_loading']);
            add_filter('widget_text', [$this, 'add_lazy_loading']);
        }

        // Lazy Load iframes
        if ($this->settings['lazy_load_iframes'] && ! is_admin()) {
            add_filter('the_content', [$this, 'add_lazy_loading_iframes']);
            add_filter('embed_oembed_html', [$this, 'add_lazy_loading_iframes']);
        }

        // Remove Query Strings
        if ($this->settings['remove_query_strings'] && ! is_admin()) {
            add_filter('script_loader_src', [$this, 'remove_query_strings'], 15);
            add_filter('style_loader_src', [$this, 'remove_query_strings'], 15);
        }

        // Disable Emojis
        if ($this->settings['disable_emojis']) {
            $this->disable_emojis();
        }

        // Disable Embeds
        if ($this->settings['disable_embeds']) {
            $this->disable_embeds();
        }

        // Expired Cache (No-Cache Headers)
        if ($this->settings['expired_cache'] && ! is_admin()) {
            add_action('template_redirect', [$this, 'send_expired_cache_headers'], 1);
            add_action('send_headers', [$this, 'send_expired_cache_headers']);
        }

        // Debug mode - add debug info for admins
        if ($this->settings['cache_debug'] && ! is_admin()) {
            add_action('wp_footer', [$this, 'output_debug_info'], 999);
        }

        // Local Google Fonts
        if ($this->settings['local_google_fonts'] && ! is_admin()) {
            add_filter('style_loader_src', [$this, 'localize_google_fonts_css'], 10, 2);
            add_filter('wp_resource_hints', [$this, 'remove_google_fonts_preconnect'], 10, 2);
        }

        // LCP Image Optimization (preload + fetchpriority)
        if ($this->settings['lcp_preload'] && ! is_admin()) {
            add_filter('the_content', [$this, 'optimize_lcp_image'], 5);
            add_filter('post_thumbnail_html', [$this, 'optimize_lcp_image'], 5);
            add_action('wp_head', [$this, 'output_lcp_preload'], 1);
        }

        // Preconnect Hints
        if ($this->settings['preconnect_hints'] && ! is_admin()) {
            add_action('wp_head', [$this, 'output_preconnect_hints'], 1);
        }

        // Font Preload
        if ($this->settings['preload_fonts'] && ! is_admin()) {
            add_action('wp_head', [$this, 'output_font_preload'], 2);
        }

        // Delay Third-Party Scripts
        if ($this->settings['delay_third_party'] && ! is_admin()) {
            add_filter('script_loader_tag', [$this, 'delay_third_party_scripts'], 10, 3);
        }
    }

    /**
     * CSS Minification - minify inline styles in style tags
     */
    public function minify_inline_css($html, $handle, $href, $media)
    {
        return $html; // Return unchanged, we don't minify external CSS files
    }

    private $css_buffer_active = false;

    public function start_css_capture()
    {
        // Not implemented - inline CSS minification would require output buffering
        // which conflicts with page cache. External files are better cached by nginx.
    }

    public function end_css_capture()
    {
        // Not implemented
    }

    /**
     * Minify inline JS within script tags
     */
    public function minify_inline_js($tag, $handle, $src)
    {
        // Don't touch external scripts
        if (! empty($src)) {
            return $tag;
        }

        // Skip certain handles that shouldn't be minified
        $skip_handles = ['jquery-core', 'jquery-migrate', 'wp-embed'];
        if (in_array($handle, $skip_handles)) {
            return $tag;
        }

        // Simple minification for inline scripts - just collapse whitespace
        // Being conservative to avoid breaking scripts
        $tag = preg_replace_callback('/<script([^>]*)>(.*?)<\/script>/is', function ($match) {
            $attrs = $match[1];
            $content = $match[2];

            // Skip if it contains JSON or data
            if (strpos($content, 'application/json') !== false || strpos($content, 'application/ld+json') !== false) {
                return $match[0];
            }

            // Very conservative minification - only collapse multiple spaces/newlines
            $content = preg_replace('/\s{2,}/', ' ', $content);
            $content = trim($content);

            return '<script'.$attrs.'>'.$content.'</script>';
        }, $tag);

        return $tag;
    }

    /**
     * Output debug info for admins
     */
    public function output_debug_info()
    {
        // Only show for admins with cache_debug param or setting enabled
        if (! current_user_can('manage_options')) {
            return;
        }

        // Check if ?cache_debug=1 is set
        if (! isset($_GET['cache_debug']) && ! $this->settings['cache_debug']) {
            return;
        }

        // Only output if explicitly requested via query param
        if (! isset($_GET['cache_debug']) || $_GET['cache_debug'] !== '1') {
            return;
        }

        $debug_info = [];
        $debug_info['plugin_version'] = self::VERSION;
        $debug_info['page_cache'] = $this->settings['page_cache'] ? 'enabled' : 'disabled';
        $debug_info['object_cache'] = $this->is_drop_in_installed() ? 'enabled' : 'disabled';
        $debug_info['redis_connected'] = $this->is_redis_connected() ? 'yes' : 'no';
        $debug_info['minify_css'] = $this->settings['minify_css'] ? 'enabled' : 'disabled';
        $debug_info['minify_js'] = $this->settings['minify_js'] ? 'enabled' : 'disabled';
        $debug_info['defer_js'] = $this->settings['defer_js'] ? 'enabled' : 'disabled';
        $debug_info['lazy_load'] = $this->settings['lazy_load'] ? 'enabled' : 'disabled';
        $debug_info['logged_in'] = is_user_logged_in() ? 'yes' : 'no';
        $debug_info['bypass_reason'] = $this->get_cache_bypass_reason();
        $debug_info['page_generated'] = date('Y-m-d H:i:s');
        $debug_info['queries'] = get_num_queries();
        $debug_info['memory_peak'] = size_format(memory_get_peak_usage(true));

        echo "\n<!-- Jabali Cache Debug Info\n";
        foreach ($debug_info as $key => $value) {
            echo "  {$key}: {$value}\n";
        }
        echo "-->\n";
    }

    /**
     * Get reason why page cache might be bypassed
     */
    private function get_cache_bypass_reason()
    {
        if (is_user_logged_in()) {
            return 'logged_in_user';
        }

        if (isset($_POST) && ! empty($_POST)) {
            return 'post_request';
        }

        if (is_admin()) {
            return 'admin_page';
        }

        // Check for WooCommerce cart/checkout
        if (function_exists('is_cart') && is_cart()) {
            return 'woocommerce_cart';
        }
        if (function_exists('is_checkout') && is_checkout()) {
            return 'woocommerce_checkout';
        }
        if (function_exists('is_account_page') && is_account_page()) {
            return 'woocommerce_account';
        }

        // Check for cookies that trigger bypass
        if (isset($_COOKIE['wordpress_logged_in']) || isset($_COOKIE['wp-postpass'])) {
            return 'bypass_cookie';
        }
        if (isset($_COOKIE['woocommerce_cart_hash']) || isset($_COOKIE['woocommerce_items_in_cart'])) {
            return 'woocommerce_cookie';
        }

        return 'none';
    }

    // HTML Minification
    public function start_html_buffer()
    {
        ob_start([$this, 'minify_html']);
    }

    public function minify_html($html)
    {
        if (empty($html)) {
            return $html;
        }

        // Don't minify feeds, sitemaps, or admin
        if (is_feed() || is_admin()) {
            return $html;
        }

        // Preserve scripts and styles
        $preserved = [];
        $html = preg_replace_callback('/<(script|style|pre|textarea)[^>]*>.*?<\/\1>/is', function ($match) use (&$preserved) {
            $key = '<!--PRESERVE:'.count($preserved).'-->';
            $preserved[$key] = $match[0];

            return $key;
        }, $html);

        // Remove HTML comments (except IE conditionals and PRESERVE markers)
        $html = preg_replace('/<!--(?!\[if|PRESERVE:).*?-->/s', '', $html);

        // Remove whitespace between tags
        $html = preg_replace('/>\s+</', '><', $html);

        // Remove extra whitespace
        $html = preg_replace('/\s{2,}/', ' ', $html);

        // Remove whitespace around block elements
        $html = preg_replace('/\s*(<\/?(?:html|head|body|div|section|article|header|footer|nav|aside|main|p|h[1-6]|ul|ol|li|table|tr|td|th|form|fieldset|dl|dt|dd|blockquote|hr|br)[^>]*>)\s*/i', '$1', $html);

        // Restore preserved content
        $html = str_replace(array_keys($preserved), array_values($preserved), $html);

        return trim($html);
    }

    // Lazy Loading
    public function add_lazy_loading($content)
    {
        if (empty($content)) {
            return $content;
        }

        // Add loading="lazy" to images that don't have it
        $content = preg_replace_callback('/<img([^>]+)>/i', function ($match) {
            $img = $match[0];

            // Skip if already has loading attribute
            if (strpos($img, 'loading=') !== false) {
                return $img;
            }

            // Skip base64 images and SVGs
            if (strpos($img, 'data:image') !== false || strpos($img, '.svg') !== false) {
                return $img;
            }

            // Add loading="lazy"
            return str_replace('<img', '<img loading="lazy"', $img);
        }, $content);

        return $content;
    }

    public function add_lazy_loading_iframes($content)
    {
        if (empty($content)) {
            return $content;
        }

        // Add loading="lazy" to iframes
        $content = preg_replace_callback('/<iframe([^>]+)>/i', function ($match) {
            $iframe = $match[0];

            if (strpos($iframe, 'loading=') !== false) {
                return $iframe;
            }

            return str_replace('<iframe', '<iframe loading="lazy"', $iframe);
        }, $content);

        return $content;
    }

    // Remove Query Strings
    public function remove_query_strings($src)
    {
        if (strpos($src, '?ver=') !== false) {
            $src = remove_query_arg('ver', $src);
        }

        return $src;
    }

    // Disable Emojis
    public function disable_emojis()
    {
        remove_action('wp_head', 'print_emoji_detection_script', 7);
        remove_action('admin_print_scripts', 'print_emoji_detection_script');
        remove_action('wp_print_styles', 'print_emoji_styles');
        remove_action('admin_print_styles', 'print_emoji_styles');
        remove_filter('the_content_feed', 'wp_staticize_emoji');
        remove_filter('comment_text_rss', 'wp_staticize_emoji');
        remove_filter('wp_mail', 'wp_staticize_emoji_for_email');
        add_filter('tiny_mce_plugins', function ($plugins) {
            return is_array($plugins) ? array_diff($plugins, ['wpemoji']) : [];
        });
        add_filter('wp_resource_hints', function ($urls, $relation_type) {
            if ($relation_type === 'dns-prefetch') {
                $urls = array_filter($urls, function ($url) {
                    return strpos($url, 'https://s.w.org/images/core/emoji/') === false;
                });
            }

            return $urls;
        }, 10, 2);
    }

    // Disable Embeds
    public function disable_embeds()
    {
        remove_action('rest_api_init', 'wp_oembed_register_route');
        remove_filter('oembed_dataparse', 'wp_filter_oembed_result', 10);
        remove_action('wp_head', 'wp_oembed_add_discovery_links');
        remove_action('wp_head', 'wp_oembed_add_host_js');
        add_filter('embed_oembed_discover', '__return_false');
        add_filter('tiny_mce_plugins', function ($plugins) {
            return array_diff($plugins, ['wpembed']);
        });
        add_filter('rewrite_rules_array', function ($rules) {
            foreach ($rules as $rule => $rewrite) {
                if (strpos($rewrite, 'embed=true') !== false) {
                    unset($rules[$rule]);
                }
            }

            return $rules;
        });
    }

    // =========================================================================
    // LCP OPTIMIZATION
    // =========================================================================

    private $lcp_image_url = null;

    private $lcp_image_srcset = null;

    private $lcp_image_sizes = null;

    private $lcp_processed = false;

    /**
     * Detect and optimize LCP image
     * Adds fetchpriority="high" and loading="eager" to the first large image
     * Also excludes it from lazy loading
     */
    public function optimize_lcp_image($content)
    {
        if (empty($content) || $this->lcp_processed) {
            return $content;
        }

        // Find all images in content
        preg_match_all('/<img[^>]+>/i', $content, $matches);

        if (empty($matches[0])) {
            return $content;
        }

        $first_image = true;
        foreach ($matches[0] as $img) {
            // Skip small images (icons, avatars, etc.)
            if ($this->is_small_image($img)) {
                continue;
            }

            // Skip images that already have fetchpriority
            if (strpos($img, 'fetchpriority') !== false) {
                continue;
            }

            // Skip lazy-loaded images from other plugins
            if (strpos($img, 'data-src') !== false && strpos($img, ' src=') === false) {
                continue;
            }

            if ($first_image) {
                // This is our LCP candidate
                $optimized_img = $img;

                // Add fetchpriority="high"
                if (strpos($optimized_img, 'fetchpriority') === false) {
                    $optimized_img = str_replace('<img', '<img fetchpriority="high"', $optimized_img);
                }

                // Change loading="lazy" to loading="eager" or add it
                if (strpos($optimized_img, 'loading="lazy"') !== false) {
                    $optimized_img = str_replace('loading="lazy"', 'loading="eager"', $optimized_img);
                } elseif (strpos($optimized_img, 'loading=') === false) {
                    $optimized_img = str_replace('<img', '<img loading="eager"', $optimized_img);
                }

                // Extract URL for preload
                if (preg_match('/src=["\']([^"\']+)["\']/i', $optimized_img, $src_match)) {
                    $this->lcp_image_url = $src_match[1];
                }

                // Extract srcset for preload
                if (preg_match('/srcset=["\']([^"\']+)["\']/i', $optimized_img, $srcset_match)) {
                    $this->lcp_image_srcset = $srcset_match[1];
                }

                // Extract sizes for preload
                if (preg_match('/sizes=["\']([^"\']+)["\']/i', $optimized_img, $sizes_match)) {
                    $this->lcp_image_sizes = $sizes_match[1];
                }

                $content = str_replace($img, $optimized_img, $content);
                $this->lcp_processed = true;
                $first_image = false;
            }
        }

        return $content;
    }

    /**
     * Check if image is likely small (icon, avatar, etc.)
     */
    private function is_small_image($img)
    {
        // Check for explicit small dimensions
        if (preg_match('/width=["\']?(\d+)/i', $img, $w_match)) {
            if ((int) $w_match[1] < 150) {
                return true;
            }
        }

        if (preg_match('/height=["\']?(\d+)/i', $img, $h_match)) {
            if ((int) $h_match[1] < 150) {
                return true;
            }
        }

        // Check for common small image classes
        $small_classes = ['avatar', 'icon', 'logo', 'emoji', 'thumbnail', 'wp-smiley'];
        foreach ($small_classes as $class) {
            if (stripos($img, $class) !== false) {
                return true;
            }
        }

        // Check for small image sizes in filename
        if (preg_match('/-(\d+)x(\d+)\./i', $img, $size_match)) {
            if ((int) $size_match[1] < 150 || (int) $size_match[2] < 150) {
                return true;
            }
        }

        return false;
    }

    /**
     * Output LCP image preload link in head
     */
    public function output_lcp_preload()
    {
        // If we haven't processed content yet, try to get featured image
        if (! $this->lcp_image_url && is_singular()) {
            $post_id = get_the_ID();
            if ($post_id && has_post_thumbnail($post_id)) {
                $thumbnail_id = get_post_thumbnail_id($post_id);
                $this->lcp_image_url = wp_get_attachment_image_url($thumbnail_id, 'large');

                // Get srcset for responsive preload
                $this->lcp_image_srcset = wp_get_attachment_image_srcset($thumbnail_id, 'large');
                $this->lcp_image_sizes = wp_get_attachment_image_sizes($thumbnail_id, 'large');
            }
        }

        if (! $this->lcp_image_url) {
            return;
        }

        // Build preload link
        $preload = '<link rel="preload" as="image" href="'.esc_url($this->lcp_image_url).'"';

        if ($this->lcp_image_srcset) {
            $preload .= ' imagesrcset="'.esc_attr($this->lcp_image_srcset).'"';
        }

        if ($this->lcp_image_sizes) {
            $preload .= ' imagesizes="'.esc_attr($this->lcp_image_sizes).'"';
        }

        $preload .= ' fetchpriority="high">';

        echo $preload."\n";
    }

    // =========================================================================
    // PRECONNECT HINTS
    // =========================================================================

    /**
     * Output preconnect hints for external domains
     */
    public function output_preconnect_hints()
    {
        $domains = $this->get_preconnect_domains();

        foreach ($domains as $domain) {
            echo '<link rel="preconnect" href="'.esc_url($domain).'" crossorigin>'."\n";
        }
    }

    /**
     * Get list of domains that should have preconnect hints
     */
    private function get_preconnect_domains()
    {
        $domains = [];
        $site_host = parse_url(home_url(), PHP_URL_HOST);

        // Common CDN and font domains
        $common_domains = [
            'fonts.gstatic.com',
            'cdnjs.cloudflare.com',
            'cdn.jsdelivr.net',
            'unpkg.com',
            'ajax.googleapis.com',
            'www.googletagmanager.com',
            'www.google-analytics.com',
            'connect.facebook.net',
            'platform.twitter.com',
        ];

        // Check if local Google Fonts is disabled (then we need preconnect for Google)
        if (! $this->settings['local_google_fonts']) {
            $domains[] = 'https://fonts.googleapis.com';
            $domains[] = 'https://fonts.gstatic.com';
        }

        // Check for common CDN usage in theme
        $template_uri = get_template_directory_uri();
        $template_host = parse_url($template_uri, PHP_URL_HOST);
        if ($template_host && $template_host !== $site_host) {
            $domains[] = 'https://'.$template_host;
        }

        // Check WooCommerce
        if (class_exists('WooCommerce')) {
            // WooCommerce uses some external resources
            // Usually handled locally, but check for PayPal, Stripe, etc.
        }

        // Remove duplicates
        $domains = array_unique($domains);

        // Limit to 4 most important (too many preconnects hurt performance)
        return array_slice($domains, 0, 4);
    }

    // =========================================================================
    // FONT PRELOAD
    // =========================================================================

    /**
     * Preload critical fonts
     */
    public function output_font_preload()
    {
        // If using local Google Fonts, preload the first cached font file
        if ($this->settings['local_google_fonts']) {
            $cache_dir = WP_CONTENT_DIR.'/cache/jabali/fonts';
            $cache_url = content_url('/cache/jabali/fonts');

            if (is_dir($cache_dir)) {
                $fonts = glob($cache_dir.'/*.woff2');
                if (! empty($fonts)) {
                    // Preload first 2 font files (usually regular and bold)
                    $count = 0;
                    foreach ($fonts as $font) {
                        if ($count >= 2) {
                            break;
                        }
                        $font_url = $cache_url.'/'.basename($font);
                        echo '<link rel="preload" as="font" type="font/woff2" href="'.esc_url($font_url).'" crossorigin>'."\n";
                        $count++;
                    }
                }
            }
        }
    }

    // =========================================================================
    // IMPROVED DEFER JS WITH EXCLUSIONS
    // =========================================================================

    /**
     * Default list of script handles that should NOT be deferred
     */
    private function get_defer_js_exclusions()
    {
        return [
            // Core jQuery - many inline scripts depend on it
            'jquery-core',
            'jquery-migrate',
            'jquery',

            // WordPress core
            'wp-polyfill',
            'wp-hooks',

            // WooCommerce critical scripts
            'wc-cart-fragments',
            'wc-checkout',
            'wc-add-to-cart',
            'wc-single-product',
            'woocommerce',

            // Elementor critical
            'elementor-frontend',
            'elementor-pro-frontend',

            // Spectra/UAG critical
            'uagb-block-positioning-js',
            'uagb-js',

            // Astra theme
            'astra-theme-js',
            'astra-addon-js',

            // Common sliders that need to run early
            'swiper',
            'slick',
            'owl-carousel',

            // Critical UI libraries
            'lodash',
            'underscore',
            'backbone',
        ];
    }

    /**
     * Defer non-critical JavaScript with proper exclusions
     */
    public function defer_js($tag, $handle, $src)
    {
        // Skip if no src (inline script)
        if (empty($src)) {
            return $tag;
        }

        // Get exclusion list
        $exclusions = $this->get_defer_js_exclusions();

        // Allow filtering exclusions
        $exclusions = apply_filters('jabali_cache_defer_js_exclusions', $exclusions);

        // Don't defer excluded scripts
        if (in_array($handle, $exclusions)) {
            return $tag;
        }

        // Don't defer if already has defer or async
        if (strpos($tag, ' defer') !== false || strpos($tag, ' async') !== false) {
            return $tag;
        }

        // Don't defer scripts with dependencies that aren't deferred
        // (this is a simplified check)
        if (strpos($handle, 'jquery') !== false && strpos($handle, 'jquery-core') === false) {
            // jQuery-dependent scripts should check if jQuery is available
            // For safety, we let them through but they'll have defer
        }

        // Add defer attribute
        return str_replace(' src=', ' defer src=', $tag);
    }

    // =========================================================================
    // DELAY THIRD-PARTY SCRIPTS
    // =========================================================================

    /**
     * Third-party script domains that should be delayed
     */
    private function get_third_party_domains()
    {
        return [
            'google-analytics.com',
            'googletagmanager.com',
            'facebook.net',
            'connect.facebook.com',
            'platform.twitter.com',
            'snap.licdn.com',
            'static.hotjar.com',
            'script.hotjar.com',
            'widget.intercom.io',
            'js.intercomcdn.com',
            'cdn.segment.com',
            'js.hs-scripts.com',
            'js.hs-analytics.net',
            'cdn.heapanalytics.com',
            'cdn.mxpnl.com',
            'rum-static.pingdom.net',
            'newrelic.com',
            'nr-data.net',
            'fullstory.com',
            'clarity.ms',
            'browser.sentry-cdn.com',
            'cdn.onesignal.com',
            'cdn.pushwoosh.com',
            'cdn.tawk.to',
            'embed.tawk.to',
            'static.zdassets.com',
            'widget.drift.com',
            'js.driftt.com',
            'beacon-v2.helpscout.net',
            'app.chatwoot.com',
        ];
    }

    /**
     * Delay third-party scripts until user interaction
     */
    public function delay_third_party_scripts($tag, $handle, $src)
    {
        if (empty($src)) {
            return $tag;
        }

        $third_party_domains = $this->get_third_party_domains();
        $is_third_party = false;

        foreach ($third_party_domains as $domain) {
            if (strpos($src, $domain) !== false) {
                $is_third_party = true;
                break;
            }
        }

        if (! $is_third_party) {
            return $tag;
        }

        // Convert to delayed loading
        // Replace src with data-src and add class for delayed loading
        $tag = str_replace(' src=', ' data-jabali-delay-src=', $tag);
        $tag = str_replace('<script', '<script type="text/plain" data-jabali-delayed', $tag);

        // Add the delay loader script if not already added
        static $loader_added = false;
        if (! $loader_added) {
            add_action('wp_footer', [$this, 'output_delay_loader_script'], 999);
            $loader_added = true;
        }

        return $tag;
    }

    /**
     * Output script that loads delayed scripts on user interaction
     */
    public function output_delay_loader_script()
    {
        ?>
        <script>
        (function() {
            var loaded = false;
            function loadDelayedScripts() {
                if (loaded) return;
                loaded = true;
                document.querySelectorAll('script[data-jabali-delayed]').forEach(function(script) {
                    var newScript = document.createElement('script');
                    if (script.getAttribute('data-jabali-delay-src')) {
                        newScript.src = script.getAttribute('data-jabali-delay-src');
                    }
                    newScript.async = true;
                    Array.from(script.attributes).forEach(function(attr) {
                        if (attr.name !== 'type' && attr.name !== 'data-jabali-delayed' && attr.name !== 'data-jabali-delay-src') {
                            newScript.setAttribute(attr.name, attr.value);
                        }
                    });
                    if (script.innerHTML) {
                        newScript.innerHTML = script.innerHTML;
                    }
                    script.parentNode.replaceChild(newScript, script);
                });
            }
            ['scroll', 'mousemove', 'touchstart', 'keydown', 'click'].forEach(function(event) {
                window.addEventListener(event, loadDelayedScripts, {once: true, passive: true});
            });
            setTimeout(loadDelayedScripts, 5000);
        })();
        </script>
        <?php
    }

    /**
     * Localize Google Fonts - intercept and serve locally
     */
    public function localize_google_fonts_css($src, $handle)
    {
        // Only process Google Fonts CSS URLs
        if (strpos($src, 'fonts.googleapis.com/css') === false) {
            return $src;
        }

        // Generate a cache key based on the URL
        $cache_key = 'jabali_fonts_'.md5($src);
        $cache_dir = WP_CONTENT_DIR.'/cache/jabali/fonts';
        $cache_url = content_url('/cache/jabali/fonts');

        // Check if we have a cached version
        $cached_file = $cache_dir.'/'.$cache_key.'.css';
        if (file_exists($cached_file) && (time() - filemtime($cached_file)) < WEEK_IN_SECONDS) {
            return $cache_url.'/'.$cache_key.'.css';
        }

        // Create cache directory if it doesn't exist
        if (! is_dir($cache_dir)) {
            wp_mkdir_p($cache_dir);
        }

        // Fetch Google Fonts CSS with a modern user agent to get woff2
        $response = wp_remote_get($src, [
            'timeout' => 15,
            'user-agent' => 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36',
        ]);

        if (is_wp_error($response) || wp_remote_retrieve_response_code($response) !== 200) {
            return $src; // Fall back to original URL
        }

        $css = wp_remote_retrieve_body($response);

        // Parse and download font files
        $css = $this->download_and_replace_font_urls($css, $cache_dir, $cache_url);

        // Save the modified CSS
        file_put_contents($cached_file, $css);

        return $cache_url.'/'.$cache_key.'.css';
    }

    /**
     * Download font files and replace URLs in CSS
     */
    private function download_and_replace_font_urls($css, $cache_dir, $cache_url)
    {
        // Find all font URLs in the CSS
        preg_match_all('/url\((https:\/\/fonts\.gstatic\.com\/[^)]+)\)/i', $css, $matches);

        if (empty($matches[1])) {
            return $css;
        }

        $font_urls = array_unique($matches[1]);

        foreach ($font_urls as $font_url) {
            // Generate local filename
            $font_hash = md5($font_url);
            $extension = pathinfo(parse_url($font_url, PHP_URL_PATH), PATHINFO_EXTENSION) ?: 'woff2';
            $local_filename = $font_hash.'.'.$extension;
            $local_path = $cache_dir.'/'.$local_filename;
            $local_url = $cache_url.'/'.$local_filename;

            // Download font if not cached
            if (! file_exists($local_path)) {
                $font_response = wp_remote_get($font_url, [
                    'timeout' => 30,
                ]);

                if (! is_wp_error($font_response) && wp_remote_retrieve_response_code($font_response) === 200) {
                    file_put_contents($local_path, wp_remote_retrieve_body($font_response));
                } else {
                    continue; // Skip this font, keep original URL
                }
            }

            // Replace URL in CSS
            $css = str_replace($font_url, $local_url, $css);
        }

        return $css;
    }

    /**
     * Remove Google Fonts preconnect hints
     */
    public function remove_google_fonts_preconnect($hints, $relation_type)
    {
        if ($relation_type === 'preconnect' || $relation_type === 'dns-prefetch') {
            $hints = array_filter($hints, function ($hint) {
                $url = is_array($hint) ? ($hint['href'] ?? '') : $hint;

                return strpos($url, 'fonts.googleapis.com') === false
                    && strpos($url, 'fonts.gstatic.com') === false;
            });
        }

        return $hints;
    }

    /**
     * Get local Google Fonts cache stats
     */
    public function get_local_fonts_stats()
    {
        $cache_dir = WP_CONTENT_DIR.'/cache/jabali/fonts';
        $stats = [
            'enabled' => $this->settings['local_google_fonts'],
            'cached_files' => 0,
            'cache_size' => 0,
        ];

        if (is_dir($cache_dir)) {
            $files = glob($cache_dir.'/*');
            $stats['cached_files'] = count($files);
            foreach ($files as $file) {
                $stats['cache_size'] += filesize($file);
            }
        }

        return $stats;
    }

    // Expired Cache - Send no-cache headers
    public function send_expired_cache_headers()
    {
        // Skip if headers already sent
        if (headers_sent()) {
            return;
        }

        // Skip admin pages
        if (is_admin()) {
            return;
        }

        // Set headers to prevent caching
        header('Cache-Control: no-store, no-cache, must-revalidate, max-age=0', true);
        header('Pragma: no-cache', true);
        header('Expires: Thu, 01 Jan 1970 00:00:00 GMT', true);
        header('X-Jabali-Cache: no-cache', true);
    }

    // Disable Page Cache - Send headers to bypass nginx fastcgi_cache
    public function disable_page_cache_headers()
    {
        // Skip if headers already sent
        if (headers_sent()) {
            return;
        }

        // Skip admin pages
        if (is_admin()) {
            return;
        }

        // Set headers to tell nginx to not cache this response
        // and to bypass the cache when serving
        header('Cache-Control: no-store, no-cache, must-revalidate, private, max-age=0', true);
        header('Pragma: no-cache', true);
        header('X-Jabali-Skip-Cache: 1', true);
        header('X-Accel-Expires: 0', true); // nginx-specific: don't cache
    }

    // Admin Menu
    public function add_admin_menu()
    {
        add_options_page(
            __('Jabali Cache', 'jabali-cache'),
            __('Jabali Cache', 'jabali-cache'),
            'manage_options',
            'jabali-cache',
            [$this, 'render_admin_page']
        );
    }

    public function register_settings()
    {
        register_setting(self::OPTION_KEY, self::OPTION_KEY, [
            'sanitize_callback' => [$this, 'sanitize_settings'],
        ]);
    }

    public function sanitize_settings($input)
    {
        $sanitized = [];
        $checkboxes = [
            'page_cache', 'object_cache', 'html_minify', 'minify_css', 'minify_js',
            'lazy_load', 'lazy_load_iframes', 'browser_cache', 'expired_cache',
            'remove_query_strings', 'disable_emojis', 'disable_embeds', 'defer_js', 'cache_debug',
            'local_google_fonts',
            // LCP Optimization settings
            'lcp_preload', 'preconnect_hints', 'preload_fonts', 'delay_third_party',
        ];

        foreach ($checkboxes as $key) {
            $sanitized[$key] = ! empty($input[$key]);
        }

        // Sync page cache with Jabali Panel (nginx FastCGI cache)
        $old_settings = $this->get_settings();
        if ($sanitized['page_cache'] !== $old_settings['page_cache']) {
            $this->sync_page_cache_with_jabali($sanitized['page_cache']);
        }

        // Clear object cache for this option to ensure fresh read on next load
        wp_cache_delete(self::OPTION_KEY, 'options');
        wp_cache_delete('alloptions', 'options');

        return $sanitized;
    }

    /**
     * Sync page cache setting with Jabali Panel
     * This enables/disables nginx FastCGI cache for this domain
     */
    private function sync_page_cache_with_jabali($enabled)
    {
        // Get domain from site URL
        $site_url = get_site_url();
        $parsed = parse_url($site_url);
        $domain = $parsed['host'] ?? '';

        if (empty($domain)) {
            return false;
        }

        // Generate secret from AUTH_KEY
        $secret = defined('AUTH_KEY') ? hash('sha256', AUTH_KEY) : '';
        if (empty($secret)) {
            return false;
        }

        // Call Jabali Panel internal API (HTTPS with SSL verify disabled for localhost)
        $response = wp_remote_post('https://127.0.0.1/api/internal/page-cache', [
            'timeout' => 10,
            'sslverify' => false,
            'headers' => [
                'Content-Type' => 'application/json',
                'Accept' => 'application/json',
            ],
            'body' => json_encode([
                'domain' => $domain,
                'enabled' => $enabled,
                'secret' => $secret,
            ]),
        ]);

        if (is_wp_error($response)) {
            error_log('Jabali Cache: Failed to sync page cache - '.$response->get_error_message());

            return false;
        }

        $code = wp_remote_retrieve_response_code($response);
        if ($code !== 200) {
            $body = wp_remote_retrieve_body($response);
            error_log('Jabali Cache: Failed to sync page cache - HTTP '.$code.' - '.$body);

            return false;
        }

        return true;
    }

    /**
     * Disable page cache when plugin is deactivated
     */
    public function disable_page_cache_on_deactivation()
    {
        return $this->sync_page_cache_with_jabali(false);
    }

    /**
     * Enable page cache when plugin is activated (if enabled in settings)
     */
    public function enable_page_cache_on_activation()
    {
        return $this->sync_page_cache_with_jabali(true);
    }

    public function admin_styles($hook)
    {
        if ($hook !== 'settings_page_jabali-cache') {
            return;
        }
        ?>
        <style>
            .jabali-header {
                display: flex;
                align-items: center;
                gap: 16px;
                margin: 20px 0;
                padding-bottom: 20px;
                border-bottom: 1px solid #c3c4c7;
            }
            .jabali-logo {
                width: 48px;
                height: 48px;
                flex-shrink: 0;
            }
            .jabali-header-text h1 {
                margin: 0 0 4px;
                font-size: 23px;
                font-weight: 400;
                line-height: 1.3;
            }
            .jabali-header-text p {
                margin: 0;
                color: #646970;
            }
            .jabali-grid {
                display: grid;
                grid-template-columns: repeat(auto-fit, minmax(400px, 1fr));
                gap: 20px;
                margin-top: 20px;
            }
            @media (max-width: 782px) {
                .jabali-grid {
                    grid-template-columns: 1fr;
                }
            }
            .jabali-card {
                background: #fff;
                border: 1px solid #c3c4c7;
                box-shadow: 0 1px 1px rgba(0,0,0,.04);
            }
            .jabali-card-header {
                display: flex;
                align-items: center;
                gap: 10px;
                padding: 12px 16px;
                background: #f6f7f7;
                border-bottom: 1px solid #c3c4c7;
            }
            .jabali-card-header .dashicons {
                color: #2271b1;
                font-size: 20px;
                width: 20px;
                height: 20px;
            }
            .jabali-card-header h2 {
                margin: 0;
                font-size: 14px;
                font-weight: 600;
            }
            .jabali-card-body {
                padding: 16px;
            }
            .jabali-setting-row {
                display: flex;
                align-items: flex-start;
                justify-content: space-between;
                padding: 12px 0;
                border-bottom: 1px solid #f0f0f1;
            }
            .jabali-setting-row:last-child {
                border-bottom: none;
                padding-bottom: 0;
            }
            .jabali-setting-row:first-child {
                padding-top: 0;
            }
            .jabali-setting-info {
                flex: 1;
                padding-right: 16px;
            }
            .jabali-setting-label {
                display: block;
                font-weight: 600;
                margin-bottom: 4px;
                color: #1d2327;
            }
            .jabali-setting-desc {
                font-size: 13px;
                color: #646970;
                line-height: 1.5;
            }
            .jabali-toggle {
                position: relative;
                width: 36px;
                height: 18px;
                flex-shrink: 0;
                margin-top: 2px;
            }
            .jabali-toggle input {
                opacity: 0;
                width: 0;
                height: 0;
            }
            .jabali-toggle-slider {
                position: absolute;
                cursor: pointer;
                inset: 0;
                background: #8c8f94;
                border-radius: 18px;
                transition: 0.2s;
            }
            .jabali-toggle-slider:before {
                content: '';
                position: absolute;
                width: 14px;
                height: 14px;
                left: 2px;
                bottom: 2px;
                background: #fff;
                border-radius: 50%;
                transition: 0.2s;
            }
            .jabali-toggle input:checked + .jabali-toggle-slider {
                background: #2271b1;
            }
            .jabali-toggle input:checked + .jabali-toggle-slider:before {
                transform: translateX(18px);
            }
            .jabali-toggle input:focus + .jabali-toggle-slider {
                box-shadow: 0 0 0 2px #fff, 0 0 0 4px #2271b1;
            }
            .jabali-stats-grid {
                display: grid;
                grid-template-columns: repeat(3, 1fr);
                gap: 12px;
                margin-bottom: 16px;
            }
            .jabali-stat-box {
                text-align: center;
                padding: 12px 8px;
                background: #f6f7f7;
                border: 1px solid #c3c4c7;
            }
            .jabali-stat-value {
                font-size: 20px;
                font-weight: 600;
                color: #1d2327;
            }
            .jabali-stat-value.success { color: #00a32a; }
            .jabali-stat-value.warning { color: #dba617; }
            .jabali-stat-value.error { color: #d63638; }
            .jabali-stat-label {
                font-size: 11px;
                color: #646970;
                text-transform: uppercase;
                margin-top: 4px;
            }
            .jabali-actions {
                display: flex;
                gap: 8px;
                margin-top: 16px;
                padding-top: 16px;
                border-top: 1px solid #f0f0f1;
            }
            .jabali-info-notice {
                background: #f0f6fc;
                border-left: 4px solid #72aee6;
                padding: 12px;
                margin-top: 12px;
            }
            .jabali-info-notice p {
                margin: 0;
                font-size: 13px;
            }
            .jabali-warning-notice {
                background: #fcf9e8;
                border-left: 4px solid #dba617;
                padding: 12px;
                margin-bottom: 12px;
            }
            .jabali-warning-notice p {
                margin: 0;
                font-size: 13px;
            }
            .jabali-submit-row {
                display: flex;
                align-items: center;
                gap: 12px;
                margin-top: 20px;
                padding-top: 20px;
                border-top: 1px solid #c3c4c7;
            }
            .jabali-footer {
                margin-top: 30px;
                padding: 20px;
                background: #fff;
                border: 1px solid #c3c4c7;
            }
            .jabali-footer h3 {
                margin: 0 0 8px;
                font-size: 14px;
            }
            .jabali-footer p {
                margin: 0 0 8px;
                color: #646970;
            }
            .jabali-footer p:last-child {
                margin-bottom: 0;
            }
        </style>
        <?php
    }

    public function add_admin_bar_menu($wp_admin_bar)
    {
        if (! current_user_can('manage_options')) {
            return;
        }

        $connected = $this->is_redis_connected();

        $wp_admin_bar->add_node([
            'id' => 'jabali-cache',
            'title' => sprintf(
                '<span style="color: %s;">●</span> %s',
                $connected ? '#46b450' : '#dc3232',
                __('Jabali Cache', 'jabali-cache')
            ),
            'href' => admin_url('options-general.php?page=jabali-cache'),
        ]);

        $wp_admin_bar->add_node([
            'parent' => 'jabali-cache',
            'id' => 'jabali-cache-clear-all',
            'title' => __('Clear All Caches', 'jabali-cache'),
            'href' => wp_nonce_url(
                admin_url('options-general.php?page=jabali-cache&action=clear_all'),
                'jabali_cache_clear_all'
            ),
        ]);

        $wp_admin_bar->add_node([
            'parent' => 'jabali-cache',
            'id' => 'jabali-cache-flush',
            'title' => __('Flush Object Cache', 'jabali-cache'),
            'href' => wp_nonce_url(
                admin_url('options-general.php?page=jabali-cache&action=flush'),
                'jabali_cache_flush'
            ),
        ]);

        $wp_admin_bar->add_node([
            'parent' => 'jabali-cache',
            'id' => 'jabali-cache-purge-page',
            'title' => __('Purge Page Cache', 'jabali-cache'),
            'href' => wp_nonce_url(
                admin_url('options-general.php?page=jabali-cache&action=purge_page'),
                'jabali_cache_purge_page'
            ),
        ]);
    }

    public function add_action_links($links)
    {
        $settings_link = sprintf(
            '<a href="%s">%s</a>',
            admin_url('options-general.php?page=jabali-cache'),
            __('Settings', 'jabali-cache')
        );
        array_unshift($links, $settings_link);

        return $links;
    }

    public function handle_actions()
    {
        if (! isset($_GET['page']) || $_GET['page'] !== 'jabali-cache' || ! isset($_GET['action'])) {
            return;
        }

        $action = sanitize_text_field($_GET['action']);

        switch ($action) {
            case 'flush':
                if (! wp_verify_nonce($_GET['_wpnonce'] ?? '', 'jabali_cache_flush')) {
                    wp_die(__('Security check failed', 'jabali-cache'));
                }
                wp_cache_flush();
                wp_redirect(admin_url('options-general.php?page=jabali-cache&flushed=1'));
                exit;

            case 'enable':
                if (! wp_verify_nonce($_GET['_wpnonce'] ?? '', 'jabali_cache_enable')) {
                    wp_die(__('Security check failed', 'jabali-cache'));
                }
                $this->enable_drop_in();
                wp_redirect(admin_url('options-general.php?page=jabali-cache&enabled=1'));
                exit;

            case 'disable':
                if (! wp_verify_nonce($_GET['_wpnonce'] ?? '', 'jabali_cache_disable')) {
                    wp_die(__('Security check failed', 'jabali-cache'));
                }
                $this->disable_drop_in();
                wp_redirect(admin_url('options-general.php?page=jabali-cache&disabled=1'));
                exit;

            case 'clear_all':
                if (! wp_verify_nonce($_GET['_wpnonce'] ?? '', 'jabali_cache_clear_all')) {
                    wp_die(__('Security check failed', 'jabali-cache'));
                }
                $this->clear_all_caches();
                wp_redirect(admin_url('options-general.php?page=jabali-cache&cleared=1'));
                exit;

            case 'purge_page':
                if (! wp_verify_nonce($_GET['_wpnonce'] ?? '', 'jabali_cache_purge_page')) {
                    wp_die(__('Security check failed', 'jabali-cache'));
                }
                $this->purge_all_cache();
                wp_redirect(admin_url('options-general.php?page=jabali-cache&purged=1'));
                exit;
        }
    }

    /**
     * Clear all caches - combined CSS/JS files and object cache
     */
    public function clear_all_caches()
    {
        // Clear combined CSS/JS cache files
        $cache_dir = WP_CONTENT_DIR.'/cache/jabali';
        if (is_dir($cache_dir)) {
            $files = glob($cache_dir.'/*');
            foreach ($files as $file) {
                if (is_file($file)) {
                    @unlink($file);
                }
            }
        }

        // Flush object cache (Redis)
        if (function_exists('wp_cache_flush')) {
            wp_cache_flush();
        }

        // Purge page cache
        $this->purge_all_cache();
    }

    public function is_drop_in_installed()
    {
        $drop_in = WP_CONTENT_DIR.'/object-cache.php';
        if (! file_exists($drop_in)) {
            return false;
        }
        $content = file_get_contents($drop_in);

        return strpos($content, 'Jabali_Redis_Object_Cache') !== false;
    }

    public function enable_drop_in()
    {
        $source = plugin_dir_path(__FILE__).'object-cache.php';
        $dest = WP_CONTENT_DIR.'/object-cache.php';
        if (file_exists($source)) {
            @copy($source, $dest);
        }
    }

    public function disable_drop_in()
    {
        $drop_in = WP_CONTENT_DIR.'/object-cache.php';
        if (file_exists($drop_in)) {
            @unlink($drop_in);
        }
    }

    public function is_redis_connected()
    {
        if (! class_exists('Redis')) {
            return false;
        }
        try {
            $redis = new Redis;
            $host = defined('JABALI_CACHE_HOST') ? JABALI_CACHE_HOST : '127.0.0.1';
            $port = defined('JABALI_CACHE_PORT') ? JABALI_CACHE_PORT : 6379;
            $db = defined('JABALI_CACHE_DATABASE') ? JABALI_CACHE_DATABASE : 0;

            if (! $redis->connect($host, $port, 1)) {
                return false;
            }

            // Authenticate if credentials are defined
            if (defined('JABALI_CACHE_REDIS_USER') && defined('JABALI_CACHE_REDIS_PASS')) {
                $redis->auth(['user' => JABALI_CACHE_REDIS_USER, 'pass' => JABALI_CACHE_REDIS_PASS]);
            } elseif (defined('JABALI_CACHE_REDIS_PASS') && JABALI_CACHE_REDIS_PASS) {
                $redis->auth(JABALI_CACHE_REDIS_PASS);
            }

            // Select database
            $redis->select($db);

            return $redis->ping() ? true : false;
        } catch (Exception $e) {
            return false;
        }
    }

    public function get_cache_stats()
    {
        global $wp_object_cache;

        $stats = [
            'hits' => 0,
            'misses' => 0,
            'ratio' => 0,
            'connected' => false,
            'total_keys' => 0,
            'memory' => 'N/A',
        ];

        if (method_exists($wp_object_cache, 'stats')) {
            $cache_stats = $wp_object_cache->stats();
            if (is_array($cache_stats)) {
                $stats = array_merge($stats, $cache_stats);
            }
        } elseif (isset($wp_object_cache->cache_hits)) {
            $stats['hits'] = $wp_object_cache->cache_hits;
            $stats['misses'] = $wp_object_cache->cache_misses ?? 0;
        }

        // Calculate ratio
        $total = $stats['hits'] + $stats['misses'];
        if ($total > 0) {
            $stats['ratio'] = round(($stats['hits'] / $total) * 100, 1);
        }

        return $stats;
    }

    public function render_admin_page()
    {
        $stats = $this->get_cache_stats();
        $drop_in_installed = $this->is_drop_in_installed();
        $redis_available = class_exists('Redis');
        $redis_connected = $this->is_redis_connected();
        ?>
        <div class="wrap">
            <?php if (isset($_GET['flushed'])) { ?>
            <div class="notice notice-success is-dismissible">
                <p><?php _e('Object cache flushed successfully.', 'jabali-cache'); ?></p>
            </div>
            <?php } ?>

            <?php if (isset($_GET['cleared'])) { ?>
            <div class="notice notice-success is-dismissible">
                <p><?php _e('All caches cleared successfully.', 'jabali-cache'); ?></p>
            </div>
            <?php } ?>

            <?php if (isset($_GET['purged'])) { ?>
            <div class="notice notice-success is-dismissible">
                <p><?php _e('Page cache purged successfully.', 'jabali-cache'); ?></p>
            </div>
            <?php } ?>

            <?php if (isset($_GET['enabled'])) { ?>
            <div class="notice notice-success is-dismissible">
                <p><?php _e('Object cache enabled successfully.', 'jabali-cache'); ?></p>
            </div>
            <?php } ?>

            <?php if (isset($_GET['disabled'])) { ?>
            <div class="notice notice-success is-dismissible">
                <p><?php _e('Object cache disabled successfully.', 'jabali-cache'); ?></p>
            </div>
            <?php } ?>

            <?php if (isset($_GET['settings-updated'])) { ?>
            <div class="notice notice-success is-dismissible">
                <p><?php _e('Settings saved successfully.', 'jabali-cache'); ?></p>
            </div>
            <?php } ?>

            <!-- Header with Logo -->
            <div class="jabali-header">
                <svg class="jabali-logo" viewBox="0 0 147.26 96.573" xmlns="http://www.w3.org/2000/svg">
                    <g transform="translate(-34.391 -47.118)">
                        <g transform="translate(-3.541 -25.332)">
                            <path fill="#2271b1" d="m79.472 73.244c-4.0065 0.95163-8.277 4.0636-9.2604 8.2021h1.8521v0.52917c-5.293 0.24188-10.217 5.6941-15.081 7.7231-1.114 0.46467-4.676 1.2393-5.1043 2.4417-0.38218 1.073 1.0399 2.7743 1.5975 3.5935 2.4662 3.6227 5.8771 5.0144 10.121 3.9687-1.5506 5.3455-7.6205 6.9269-11.24 2.3802-3.2616-4.0969-3.312-10.11-3.312-15.08h0.26458v0.52917h0.26458l-1.0583-2.9104h-0.26458c-0.70254 1.8212-1.2726 3.6092-1.5118 5.5562-0.11276 0.91811 0.18619 2.2623-0.34767 3.0641-1.4776 2.2192-6.4692 0.93492-8.4593 0.37544 0.20517 2.3499 1.304 8.2501 3.0454 9.9049 1.8549 1.7628 6.4774 1.7367 8.8609 1.7367v0.26459l-3.175 0.52916v0.26459c7.2825 4.4949 15.112 11.721 18.785 19.579h0.26458c-0.01183-4.2856-2.0574-7.5834-4.2333-11.112 4.5342 3.0843 9.258 7.8497 14.552 9.525l-1.5875-5.2917h0.26458c0.84444 4.7343 6.8962 10.084 11.377 11.377-1.2956-5.6515-1.6033-9.8179 0.52917-15.346h0.26458c-2.5132 10.202 3.9699 16.869 9.2604 24.606l-14.023-5.5226-9.7896-7.1774c1.5844 2.7814 4.9469 6.0214 5.4293 9.2604 0.40039 2.6886-0.9934 6.31-2.0295 8.7312-2.8259 6.6039-7.6431 11.451-13.189 15.927-2.8919 2.334-6.6625 4.4647-8.9958 7.3563 4.0856 0 8.6528 1.3244 10.054-3.4396h0.26458v2.3812l6.6146-4.986 10.236-8.6639 1.5197-3.2003 5.9717-2.2681 4.4979-2.888 3.175-2.6291 5.5562 0.55836-3.9687-7.6729c3.7228 2.2978 7.9806 5.5165 10.332 9.2604 1.5432 2.4574 1.9452 5.5757 3.5984 7.9375 2.1589 3.0843 5.4308 5.7197 8.1059 8.3544 1.9816 1.9517 4.3458 5.5336 7.0692 6.4219 1.6243 0.52978 3.1232-1.294 2.7832-2.87-0.2334-1.0821-1.2485-1.8971-1.9905-2.6458l2.3812 0.52917c-0.0648-2.6792-1.8771-2.9045-3.7042-4.3366-2.004-1.5708-4.6598-4.1729-5.7406-6.5113-0.71228-1.5411-0.50959-3.8743-0.63288-5.5562-0.29051-3.9631-0.28284-8.2826-1.1249-12.171-0.40549-1.8724-2.2649-3.3993-3.2295-5.0271-1.422-2.3996-2.1467-5.1916-2.5013-7.9375h0.26458c0.93579 2.8711 2.3848 5.5274 4.2064 7.9375 1.0104 1.3367 2.6091 2.627 3.1628 4.2333 0.62267 1.8067 0.73913 4.1838 0.82253 6.0854 0.0342 0.77985-0.23556 2.0716 0.43464 2.6489 1.1663 1.0046 4.7066 0.78333 6.1904 0.79049 5.8026 0.028 12.063-0.25522 17.462-2.6456-0.77197-2.0367-2.2275-3.7778-3.0552-5.8208-1.5912-3.928-1.4637-7.2919-0.91359-11.377h0.26458c0 4.5242 1.0259 8.7475 3.2881 12.7 2.2975 4.0142 6.1699 8.2606 5.6616 13.229-0.88324 8.6337-11.013 11.804-12.918 19.844 3.1087 0 5.5125 0.6513 6.8792-2.6458h0.26458v1.8521c2.9712-1.5415 3.1185-4.5507 5.0836-6.8792 2.8528-3.3803 8.621-6.0668 10.516-10.054 0.93566-1.9692-1.193-4.2544-5e-3 -6.35 1.0544-1.8601 3.0177-3.2355 4.2421-5.0271 3.2956-4.822 5.503-10.318 6.0481-16.14 0.21876-2.3368-0.37598-5.0017-1.2819-7.1438-0.44877-1.0612-1.8038-2.5236-1.6796-3.7042 0.1146-1.0894 1.3436-2.0376 1.9205-2.9104 1.0226-1.5471 1.8056-3.4197 1.8682-5.2917 0.0962-2.8782-1.4065-5.0444-3.432-6.9241-0.85752-0.79578-2.9299-2.2711-2.5035-3.6541 0.34257-1.1112 1.5139-1.3081 2.5074-1.3268 1.6838-0.03161 3.4012 0.16952 5.0271-0.36094 2.3027-0.75128 3.8049-2.2333 6.35-2.2861-1.7653-2.0607-4.7845-2.9032-7.4083-2.9104l1.3229-1.0583v-0.26458c-1.7525 0-3.572-0.1527-5.2917 0.23498-1.0777 0.24295-2.2263 0.49727-3.175 1.0865-5.9935 3.7225-0.17836 9.6913 1.4638 14.024 1.0121 2.6706-0.24648 6.0015-3.0513 7.0055-1.3226 0.47346-2.5954-0.66607-3.7042-1.2419-2.3547-1.2228-4.8913-2.1202-7.4083-2.9406-8.2756-2.6974-17.5-4.146-26.194-4.146v-0.26458l4.4979-1.8521c-3.7368-2.2824-9.3016-1.8521-13.494-1.8521v-0.26458l7.4083-3.175v-0.26458l-14.552-0.79375v-0.26458l5.8208-2.3812v-0.26458c-4.5419-0.36146-9.4692-1.8779-14.023-0.79375l7.6729-4.4979c-3.1942-1.2051-9.5711-1.7051-12.666 0.08023-1.0502 0.60585-1.5876 1.9605-2.4392 2.8055-1.8354 1.8212-4.3154 2.6495-6.8553 2.6705v-0.26458c7.3819-1.0902 9.4631-9.4281 7.1438-15.61-3.6173 0.74663-7.4063 2.0646-10.315 4.4103-1.2534 1.0109-2.7138 4.3127-4.257 4.5994-1.0995 0.20428-1.4526-1.0575-1.6183-1.8662-0.45208-2.2056 0.32443-4.3161 1.109-6.3498m-11.112 15.875c-0.98324 2.0589-2.9464 2.3799-5.0271 2.3812 0.97375-2.0991 2.7849-3.4588 5.0271-2.3812m-6.8792 25.4-0.26458 0.26458 0.26458-0.26458m108.48 17.727c-1.2104 4.4255-4.1608 9.5105-7.9375 12.171v0.52916c3.1505 1.7947 7.6489 3.4777 10.315 5.8698 4.7761 4.2855 6.0439 12.285 5.5601 18.207 4.1709 0 7.4428-0.0408 5.8208-5.0271l1.3229 0.79375c0.87387-3.6809-2.2376-6.5639-3.5443-9.7896-1.5868-3.9171-1.5007-8.3848-2.9653-12.16-0.67252-1.7337-3.2676-2.9299-4.5271-4.2674-1.79-1.9009-2.4102-4.4741-4.0445-6.3263m13.758 31.485-0.26459 0.26459 0.26459-0.26459m-38.629 1.8521-0.26458 0.26458z"/>
                        </g>
                    </g>
                </svg>
                <div class="jabali-header-text">
                    <h1><?php _e('Jabali Cache', 'jabali-cache'); ?></h1>
                    <p><?php _e('High-performance caching and optimization for WordPress', 'jabali-cache'); ?></p>
                </div>
            </div>

            <form method="post" action="options.php">
                <?php settings_fields(self::OPTION_KEY); ?>

                <div class="jabali-grid">
                    <!-- Page Cache Card -->
                    <div class="jabali-card">
                        <div class="jabali-card-header">
                            <span class="dashicons dashicons-database"></span>
                            <h2><?php _e('Page Cache', 'jabali-cache'); ?></h2>
                        </div>
                        <div class="jabali-card-body">
                            <div class="jabali-setting-row">
                                <div class="jabali-setting-info">
                                    <span class="jabali-setting-label"><?php _e('Enable Page Cache', 'jabali-cache'); ?></span>
                                    <span class="jabali-setting-desc"><?php _e('Full page caching via nginx. Dramatically speeds up page load times by serving cached HTML directly from the server.', 'jabali-cache'); ?></span>
                                </div>
                                <label class="jabali-toggle">
                                    <input type="checkbox" name="<?php echo self::OPTION_KEY; ?>[page_cache]" value="1" <?php checked($this->settings['page_cache']); ?>>
                                    <span class="jabali-toggle-slider"></span>
                                </label>
                            </div>
                            <div class="jabali-info-notice">
                                <p><strong><?php _e('Note:', 'jabali-cache'); ?></strong> <?php _e('Cache is automatically bypassed for logged-in users, POST requests, and WooCommerce cart/checkout pages. Content changes trigger automatic cache purging.', 'jabali-cache'); ?></p>
                            </div>
                        </div>
                    </div>

                    <!-- Object Cache Card -->
                    <div class="jabali-card">
                        <div class="jabali-card-header">
                            <span class="dashicons dashicons-performance"></span>
                            <h2><?php _e('Object Cache (Redis)', 'jabali-cache'); ?></h2>
                        </div>
                        <div class="jabali-card-body">
                            <div class="jabali-stats-grid">
                                <div class="jabali-stat-box">
                                    <div class="jabali-stat-value <?php echo $redis_connected ? 'success' : 'error'; ?>">
                                        <?php echo $redis_connected ? __('Connected', 'jabali-cache') : __('Offline', 'jabali-cache'); ?>
                                    </div>
                                    <div class="jabali-stat-label"><?php _e('Status', 'jabali-cache'); ?></div>
                                </div>
                                <div class="jabali-stat-box">
                                    <div class="jabali-stat-value <?php echo $stats['ratio'] >= 80 ? 'success' : ($stats['ratio'] >= 50 ? 'warning' : 'error'); ?>">
                                        <?php echo $stats['ratio']; ?>%
                                    </div>
                                    <div class="jabali-stat-label"><?php _e('Hit Ratio', 'jabali-cache'); ?></div>
                                </div>
                                <div class="jabali-stat-box">
                                    <div class="jabali-stat-value">
                                        <?php echo number_format($stats['hits']); ?>
                                    </div>
                                    <div class="jabali-stat-label"><?php _e('Hits', 'jabali-cache'); ?></div>
                                </div>
                                <div class="jabali-stat-box">
                                    <div class="jabali-stat-value">
                                        <?php echo number_format($stats['misses']); ?>
                                    </div>
                                    <div class="jabali-stat-label"><?php _e('Misses', 'jabali-cache'); ?></div>
                                </div>
                            </div>

                            <?php if ($stats['ratio'] < 50 && ($stats['hits'] + $stats['misses']) > 100) { ?>
                            <div class="jabali-warning-notice">
                                <p><strong><?php _e('Low Hit Ratio:', 'jabali-cache'); ?></strong> <?php _e('Consider reviewing your caching strategy. High misses may indicate frequent cache flushes or too many unique cache keys.', 'jabali-cache'); ?></p>
                            </div>
                            <?php } ?>

                            <div class="jabali-actions">
                                <?php if ($drop_in_installed) { ?>
                                    <a href="<?php echo wp_nonce_url(admin_url('options-general.php?page=jabali-cache&action=flush'), 'jabali_cache_flush'); ?>" class="button button-primary">
                                        <?php _e('Flush Cache', 'jabali-cache'); ?>
                                    </a>
                                    <a href="<?php echo wp_nonce_url(admin_url('options-general.php?page=jabali-cache&action=disable'), 'jabali_cache_disable'); ?>" class="button">
                                        <?php _e('Disable', 'jabali-cache'); ?>
                                    </a>
                                <?php } elseif ($redis_available) { ?>
                                    <a href="<?php echo wp_nonce_url(admin_url('options-general.php?page=jabali-cache&action=enable'), 'jabali_cache_enable'); ?>" class="button button-primary">
                                        <?php _e('Enable Object Cache', 'jabali-cache'); ?>
                                    </a>
                                <?php } else { ?>
                                    <p class="description"><?php _e('Redis PHP extension is not installed.', 'jabali-cache'); ?></p>
                                <?php } ?>
                            </div>
                        </div>
                    </div>

                    <!-- Minification Card -->
                    <div class="jabali-card">
                        <div class="jabali-card-header">
                            <span class="dashicons dashicons-editor-code"></span>
                            <h2><?php _e('Minification', 'jabali-cache'); ?></h2>
                        </div>
                        <div class="jabali-card-body">
                            <div class="jabali-setting-row">
                                <div class="jabali-setting-info">
                                    <span class="jabali-setting-label"><?php _e('HTML Minification', 'jabali-cache'); ?></span>
                                    <span class="jabali-setting-desc"><?php _e('Remove whitespace and comments from HTML output.', 'jabali-cache'); ?></span>
                                </div>
                                <label class="jabali-toggle">
                                    <input type="checkbox" name="<?php echo self::OPTION_KEY; ?>[html_minify]" value="1" <?php checked($this->settings['html_minify']); ?>>
                                    <span class="jabali-toggle-slider"></span>
                                </label>
                            </div>
                            <div class="jabali-setting-row">
                                <div class="jabali-setting-info">
                                    <span class="jabali-setting-label"><?php _e('CSS Minification', 'jabali-cache'); ?></span>
                                    <span class="jabali-setting-desc"><?php _e('Minify inline CSS styles.', 'jabali-cache'); ?></span>
                                </div>
                                <label class="jabali-toggle">
                                    <input type="checkbox" name="<?php echo self::OPTION_KEY; ?>[minify_css]" value="1" <?php checked($this->settings['minify_css']); ?>>
                                    <span class="jabali-toggle-slider"></span>
                                </label>
                            </div>
                            <div class="jabali-setting-row">
                                <div class="jabali-setting-info">
                                    <span class="jabali-setting-label"><?php _e('JS Minification', 'jabali-cache'); ?></span>
                                    <span class="jabali-setting-desc"><?php _e('Minify inline JavaScript (conservative).', 'jabali-cache'); ?></span>
                                </div>
                                <label class="jabali-toggle">
                                    <input type="checkbox" name="<?php echo self::OPTION_KEY; ?>[minify_js]" value="1" <?php checked($this->settings['minify_js']); ?>>
                                    <span class="jabali-toggle-slider"></span>
                                </label>
                            </div>
                        </div>
                    </div>

                    <!-- Media Card -->
                    <div class="jabali-card">
                        <div class="jabali-card-header">
                            <span class="dashicons dashicons-format-image"></span>
                            <h2><?php _e('Media', 'jabali-cache'); ?></h2>
                        </div>
                        <div class="jabali-card-body">
                            <div class="jabali-setting-row">
                                <div class="jabali-setting-info">
                                    <span class="jabali-setting-label"><?php _e('Lazy Load Images', 'jabali-cache'); ?></span>
                                    <span class="jabali-setting-desc"><?php _e('Defer loading of images until they enter the viewport.', 'jabali-cache'); ?></span>
                                </div>
                                <label class="jabali-toggle">
                                    <input type="checkbox" name="<?php echo self::OPTION_KEY; ?>[lazy_load]" value="1" <?php checked($this->settings['lazy_load']); ?>>
                                    <span class="jabali-toggle-slider"></span>
                                </label>
                            </div>
                            <div class="jabali-setting-row">
                                <div class="jabali-setting-info">
                                    <span class="jabali-setting-label"><?php _e('Lazy Load iframes', 'jabali-cache'); ?></span>
                                    <span class="jabali-setting-desc"><?php _e('Defer loading of embedded videos and iframes.', 'jabali-cache'); ?></span>
                                </div>
                                <label class="jabali-toggle">
                                    <input type="checkbox" name="<?php echo self::OPTION_KEY; ?>[lazy_load_iframes]" value="1" <?php checked($this->settings['lazy_load_iframes']); ?>>
                                    <span class="jabali-toggle-slider"></span>
                                </label>
                            </div>
                            <div class="jabali-setting-row">
                                <div class="jabali-setting-info">
                                    <span class="jabali-setting-label"><?php _e('Local Google Fonts', 'jabali-cache'); ?></span>
                                    <span class="jabali-setting-desc"><?php _e('Download and serve Google Fonts locally for faster loading and GDPR compliance.', 'jabali-cache'); ?></span>
                                </div>
                                <label class="jabali-toggle">
                                    <input type="checkbox" name="<?php echo self::OPTION_KEY; ?>[local_google_fonts]" value="1" <?php checked($this->settings['local_google_fonts']); ?>>
                                    <span class="jabali-toggle-slider"></span>
                                </label>
                            </div>
                        </div>
                    </div>

                    <!-- JavaScript Card -->
                    <div class="jabali-card">
                        <div class="jabali-card-header">
                            <span class="dashicons dashicons-media-code"></span>
                            <h2><?php _e('JavaScript', 'jabali-cache'); ?></h2>
                        </div>
                        <div class="jabali-card-body">
                            <div class="jabali-setting-row">
                                <div class="jabali-setting-info">
                                    <span class="jabali-setting-label"><?php _e('Defer JavaScript', 'jabali-cache'); ?></span>
                                    <span class="jabali-setting-desc"><?php _e('Add defer attribute to non-critical scripts for faster page rendering.', 'jabali-cache'); ?></span>
                                </div>
                                <label class="jabali-toggle">
                                    <input type="checkbox" name="<?php echo self::OPTION_KEY; ?>[defer_js]" value="1" <?php checked($this->settings['defer_js']); ?>>
                                    <span class="jabali-toggle-slider"></span>
                                </label>
                            </div>
                            <div class="jabali-setting-row">
                                <div class="jabali-setting-info">
                                    <span class="jabali-setting-label"><?php _e('Remove Query Strings', 'jabali-cache'); ?></span>
                                    <span class="jabali-setting-desc"><?php _e('Remove version query strings from static resources.', 'jabali-cache'); ?></span>
                                </div>
                                <label class="jabali-toggle">
                                    <input type="checkbox" name="<?php echo self::OPTION_KEY; ?>[remove_query_strings]" value="1" <?php checked($this->settings['remove_query_strings']); ?>>
                                    <span class="jabali-toggle-slider"></span>
                                </label>
                            </div>
                        </div>
                    </div>

                    <!-- Cleanup Card -->
                    <div class="jabali-card">
                        <div class="jabali-card-header">
                            <span class="dashicons dashicons-trash"></span>
                            <h2><?php _e('Cleanup', 'jabali-cache'); ?></h2>
                        </div>
                        <div class="jabali-card-body">
                            <div class="jabali-setting-row">
                                <div class="jabali-setting-info">
                                    <span class="jabali-setting-label"><?php _e('Disable Emojis', 'jabali-cache'); ?></span>
                                    <span class="jabali-setting-desc"><?php _e('Remove WordPress emoji scripts and styles.', 'jabali-cache'); ?></span>
                                </div>
                                <label class="jabali-toggle">
                                    <input type="checkbox" name="<?php echo self::OPTION_KEY; ?>[disable_emojis]" value="1" <?php checked($this->settings['disable_emojis']); ?>>
                                    <span class="jabali-toggle-slider"></span>
                                </label>
                            </div>
                            <div class="jabali-setting-row">
                                <div class="jabali-setting-info">
                                    <span class="jabali-setting-label"><?php _e('Disable oEmbed', 'jabali-cache'); ?></span>
                                    <span class="jabali-setting-desc"><?php _e('Disable WordPress embed discovery and related scripts.', 'jabali-cache'); ?></span>
                                </div>
                                <label class="jabali-toggle">
                                    <input type="checkbox" name="<?php echo self::OPTION_KEY; ?>[disable_embeds]" value="1" <?php checked($this->settings['disable_embeds']); ?>>
                                    <span class="jabali-toggle-slider"></span>
                                </label>
                            </div>
                        </div>
                    </div>

                    <!-- LCP Optimization Card -->
                    <div class="jabali-card">
                        <div class="jabali-card-header">
                            <span class="dashicons dashicons-performance"></span>
                            <h2><?php _e('LCP Optimization', 'jabali-cache'); ?></h2>
                        </div>
                        <div class="jabali-card-body">
                            <div class="jabali-setting-row">
                                <div class="jabali-setting-info">
                                    <span class="jabali-setting-label"><?php _e('Preload LCP Image', 'jabali-cache'); ?></span>
                                    <span class="jabali-setting-desc"><?php _e('Detect and preload the largest image above the fold. Adds fetchpriority="high" and loading="eager" to speed up LCP.', 'jabali-cache'); ?></span>
                                </div>
                                <label class="jabali-toggle">
                                    <input type="checkbox" name="<?php echo self::OPTION_KEY; ?>[lcp_preload]" value="1" <?php checked($this->settings['lcp_preload']); ?>>
                                    <span class="jabali-toggle-slider"></span>
                                </label>
                            </div>
                            <div class="jabali-setting-row">
                                <div class="jabali-setting-info">
                                    <span class="jabali-setting-label"><?php _e('Preconnect Hints', 'jabali-cache'); ?></span>
                                    <span class="jabali-setting-desc"><?php _e('Add preconnect hints for external domains (fonts, CDN) to reduce connection time.', 'jabali-cache'); ?></span>
                                </div>
                                <label class="jabali-toggle">
                                    <input type="checkbox" name="<?php echo self::OPTION_KEY; ?>[preconnect_hints]" value="1" <?php checked($this->settings['preconnect_hints']); ?>>
                                    <span class="jabali-toggle-slider"></span>
                                </label>
                            </div>
                            <div class="jabali-setting-row">
                                <div class="jabali-setting-info">
                                    <span class="jabali-setting-label"><?php _e('Preload Fonts', 'jabali-cache'); ?></span>
                                    <span class="jabali-setting-desc"><?php _e('Preload critical web fonts to prevent late text rendering. Works best with Local Google Fonts enabled.', 'jabali-cache'); ?></span>
                                </div>
                                <label class="jabali-toggle">
                                    <input type="checkbox" name="<?php echo self::OPTION_KEY; ?>[preload_fonts]" value="1" <?php checked($this->settings['preload_fonts']); ?>>
                                    <span class="jabali-toggle-slider"></span>
                                </label>
                            </div>
                            <div class="jabali-setting-row">
                                <div class="jabali-setting-info">
                                    <span class="jabali-setting-label"><?php _e('Delay Third-Party Scripts', 'jabali-cache'); ?></span>
                                    <span class="jabali-setting-desc"><?php _e('Delay analytics, chat widgets, and tracking scripts until user interaction. Improves initial page load.', 'jabali-cache'); ?></span>
                                </div>
                                <label class="jabali-toggle">
                                    <input type="checkbox" name="<?php echo self::OPTION_KEY; ?>[delay_third_party]" value="1" <?php checked($this->settings['delay_third_party']); ?>>
                                    <span class="jabali-toggle-slider"></span>
                                </label>
                            </div>
                            <div class="jabali-info-notice">
                                <p><strong><?php _e('LCP Tips:', 'jabali-cache'); ?></strong> <?php _e('LCP (Largest Contentful Paint) measures when the main content is visible. Enable Page Cache + LCP Preload for best results. Test with GTmetrix or PageSpeed Insights.', 'jabali-cache'); ?></p>
                            </div>
                        </div>
                    </div>

                    <!-- Developer Card -->
                    <div class="jabali-card">
                        <div class="jabali-card-header">
                            <span class="dashicons dashicons-admin-tools"></span>
                            <h2><?php _e('Developer', 'jabali-cache'); ?></h2>
                        </div>
                        <div class="jabali-card-body">
                            <div class="jabali-setting-row">
                                <div class="jabali-setting-info">
                                    <span class="jabali-setting-label"><?php _e('Development Mode', 'jabali-cache'); ?></span>
                                    <span class="jabali-setting-desc"><?php _e('Disable all caching. Use during development to always see changes immediately.', 'jabali-cache'); ?></span>
                                </div>
                                <label class="jabali-toggle">
                                    <input type="checkbox" name="<?php echo self::OPTION_KEY; ?>[expired_cache]" value="1" <?php checked($this->settings['expired_cache']); ?>>
                                    <span class="jabali-toggle-slider"></span>
                                </label>
                            </div>
                            <div class="jabali-setting-row">
                                <div class="jabali-setting-info">
                                    <span class="jabali-setting-label"><?php _e('Debug Mode', 'jabali-cache'); ?></span>
                                    <span class="jabali-setting-desc"><?php _e('Show cache debug info in HTML comments when ?cache_debug=1 is added to URL.', 'jabali-cache'); ?></span>
                                </div>
                                <label class="jabali-toggle">
                                    <input type="checkbox" name="<?php echo self::OPTION_KEY; ?>[cache_debug]" value="1" <?php checked($this->settings['cache_debug']); ?>>
                                    <span class="jabali-toggle-slider"></span>
                                </label>
                            </div>
                            <div class="jabali-info-notice">
                                <p><strong><?php _e('Tip:', 'jabali-cache'); ?></strong> <?php _e('Check response headers for X-Cache (HIT/MISS) and X-Cache-Reason to diagnose caching behavior.', 'jabali-cache'); ?></p>
                            </div>
                        </div>
                    </div>
                </div>

                <div class="jabali-submit-row">
                    <?php submit_button(__('Save Settings', 'jabali-cache'), 'primary', 'submit', false); ?>
                    <a href="<?php echo wp_nonce_url(admin_url('options-general.php?page=jabali-cache&action=clear_all'), 'jabali_cache_clear_all'); ?>" class="button">
                        <?php _e('Clear All Caches', 'jabali-cache'); ?>
                    </a>
                    <a href="<?php echo wp_nonce_url(admin_url('options-general.php?page=jabali-cache&action=purge_page'), 'jabali_cache_purge_page'); ?>" class="button">
                        <?php _e('Purge Page Cache', 'jabali-cache'); ?>
                    </a>
                </div>
            </form>

            <div class="jabali-footer">
                <h3><?php _e('About Jabali Cache', 'jabali-cache'); ?></h3>
                <p><?php _e('Jabali Cache provides comprehensive caching and optimization for WordPress sites hosted on Jabali Panel. It includes Redis object caching, nginx page caching with smart auto-purging, HTML/CSS/JS minification, lazy loading, local Google Fonts, and various performance optimizations.', 'jabali-cache'); ?></p>
                <p>
                    <strong><?php _e('Version:', 'jabali-cache'); ?></strong> <?php echo self::VERSION; ?> &nbsp;|&nbsp;
                    <strong><?php _e('Smart Cache Purge:', 'jabali-cache'); ?></strong> <?php echo $this->settings['page_cache'] ? '<span style="color:#00a32a;">'.__('Active', 'jabali-cache').'</span>' : '<span style="color:#646970;">'.__('Inactive', 'jabali-cache').'</span>'; ?>
                </p>
            </div>
        </div>
        <?php
    }
}

// Initialize
Jabali_Cache_Plugin::get_instance();

// Activation - enable object cache and page cache
register_activation_hook(__FILE__, function () {
    $plugin = Jabali_Cache_Plugin::get_instance();

    // Enable Redis object cache if available
    if (class_exists('Redis')) {
        $plugin->enable_drop_in();
    }

    // Always enable nginx page cache when plugin is activated
    // This is the main purpose of the plugin - users activate it because they want caching
    $plugin->enable_page_cache_on_activation();

    // Initialize default settings if they don't exist
    $settings = get_option(Jabali_Cache_Plugin::OPTION_KEY);
    if ($settings === false) {
        update_option(Jabali_Cache_Plugin::OPTION_KEY, ['page_cache' => true]);
    }
});

// Deactivation - disable object cache AND page cache
register_deactivation_hook(__FILE__, function () {
    $plugin = Jabali_Cache_Plugin::get_instance();
    $plugin->disable_drop_in();

    // Also disable nginx page cache when plugin is deactivated
    $plugin->disable_page_cache_on_deactivation();
});
