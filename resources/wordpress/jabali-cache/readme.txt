=== Jabali Cache ===
Contributors: jabali
Tags: cache, redis, object cache, performance, minification, optimization, lazy load
Requires at least: 5.0
Tested up to: 6.7
Requires PHP: 7.4
Stable tag: 1.1.0
License: GPLv2 or later
License URI: https://www.gnu.org/licenses/gpl-2.0.html

High-performance caching & optimization for WordPress - Redis object cache, HTML/CSS/JS minification, lazy loading, and more.

== Description ==

Jabali Cache provides comprehensive caching and optimization for WordPress sites hosted on Jabali Panel. It dramatically improves performance through multiple optimization techniques.

= Features =

**Object Caching**
* Redis-based persistent object caching
* Reduces database queries significantly
* Cache statistics and hit ratio monitoring
* One-click cache flush

**Minification**
* HTML minification - Remove whitespace and comments
* CSS optimization - Minify stylesheets
* JavaScript optimization - Minify scripts and remove comments

**Lazy Loading**
* Lazy load images - Defer loading until visible
* Lazy load iframes - Defer embedded videos and content

**Extra Optimizations**
* Remove query strings from static resources
* Disable WordPress emojis
* Disable oEmbed discovery

= Requirements =

* PHP 7.4 or higher
* Redis PHP extension (phpredis) - for object caching
* Redis server (provided by Jabali Panel)

== Installation ==

1. Upload the `jabali-cache` folder to `/wp-content/plugins/`
2. Activate the plugin through the 'Plugins' menu in WordPress
3. Go to Settings > Jabali Cache to configure

The plugin will automatically install the Redis object-cache.php drop-in when activated.

== Configuration ==

= Object Cache =
Add these constants to your `wp-config.php` to customize the Redis connection:

    // Redis connection settings
    define('JABALI_CACHE_HOST', '127.0.0.1');
    define('JABALI_CACHE_PORT', 6379);
    define('JABALI_CACHE_PASSWORD', '');
    define('JABALI_CACHE_DATABASE', 0);
    define('JABALI_CACHE_TIMEOUT', 1);

    // Cache key prefix (unique per site)
    define('JABALI_CACHE_PREFIX', 'wp_mysite_');

= Optimization Settings =
All optimization features can be enabled/disabled from the WordPress admin:
Settings > Jabali Cache

== Frequently Asked Questions ==

= What is object caching? =

Object caching stores the results of expensive database queries in Redis. When WordPress needs the same data again, it retrieves it from the cache instead of querying the database.

= Will minification break my site? =

Minification is generally safe, but some themes or plugins may not be compatible. If you experience issues, disable minification for the affected resource type.

= Does lazy loading affect SEO? =

Modern search engines handle lazy loading well. The plugin uses the native `loading="lazy"` attribute which is recommended by Google.

= Can I use this without Jabali Panel? =

Yes, but you'll need to have Redis installed and configured on your server. The plugin works with any Redis installation.

== Screenshots ==

1. Main settings page with all optimization options
2. Object cache statistics
3. Admin bar quick access

== Changelog ==

= 1.1.0 =
* Added HTML minification
* Added CSS optimization
* Added JavaScript optimization
* Added lazy loading for images
* Added lazy loading for iframes
* Added option to remove query strings
* Added option to disable WordPress emojis
* Added option to disable oEmbed
* Redesigned settings page with modern UI
* Settings organized into logical cards

= 1.0.0 =
* Initial release
* Redis object cache drop-in
* Admin interface with statistics
* Admin bar quick access

== Upgrade Notice ==

= 1.1.0 =
Major update with minification, lazy loading, and additional optimization features.
