<?php

/**
 * Jabali Cache - Redis Object Cache Drop-in
 *
 * @version 1.4.0
 */
defined('ABSPATH') || exit;

// Check if Redis extension is available
if (! class_exists('Redis')) {
    return;
}

/**
 * Object Cache API
 */
function wp_cache_init()
{
    global $wp_object_cache;
    $wp_object_cache = new Jabali_Redis_Object_Cache;
}

function wp_cache_add($key, $data, $group = 'default', $expire = 0)
{
    global $wp_object_cache;

    return $wp_object_cache->add($key, $data, $group, $expire);
}

function wp_cache_add_multiple(array $data, $group = 'default', $expire = 0)
{
    global $wp_object_cache;
    $results = [];
    foreach ($data as $key => $value) {
        $results[$key] = $wp_object_cache->add($key, $value, $group, $expire);
    }

    return $results;
}

function wp_cache_replace($key, $data, $group = 'default', $expire = 0)
{
    global $wp_object_cache;

    return $wp_object_cache->replace($key, $data, $group, $expire);
}

function wp_cache_set($key, $data, $group = 'default', $expire = 0)
{
    global $wp_object_cache;

    return $wp_object_cache->set($key, $data, $group, $expire);
}

function wp_cache_set_multiple(array $data, $group = 'default', $expire = 0)
{
    global $wp_object_cache;
    $results = [];
    foreach ($data as $key => $value) {
        $results[$key] = $wp_object_cache->set($key, $value, $group, $expire);
    }

    return $results;
}

function wp_cache_get($key, $group = 'default', $force = false, &$found = null)
{
    global $wp_object_cache;

    return $wp_object_cache->get($key, $group, $force, $found);
}

function wp_cache_get_multiple($keys, $group = 'default', $force = false)
{
    global $wp_object_cache;
    $results = [];
    foreach ($keys as $key) {
        $results[$key] = $wp_object_cache->get($key, $group, $force);
    }

    return $results;
}

function wp_cache_delete($key, $group = 'default')
{
    global $wp_object_cache;

    return $wp_object_cache->delete($key, $group);
}

function wp_cache_delete_multiple(array $keys, $group = 'default')
{
    global $wp_object_cache;
    $results = [];
    foreach ($keys as $key) {
        $results[$key] = $wp_object_cache->delete($key, $group);
    }

    return $results;
}

function wp_cache_incr($key, $offset = 1, $group = 'default')
{
    global $wp_object_cache;

    return $wp_object_cache->incr($key, $offset, $group);
}

function wp_cache_decr($key, $offset = 1, $group = 'default')
{
    global $wp_object_cache;

    return $wp_object_cache->decr($key, $offset, $group);
}

function wp_cache_flush()
{
    global $wp_object_cache;

    return $wp_object_cache->flush();
}

function wp_cache_flush_group($group)
{
    global $wp_object_cache;

    return $wp_object_cache->flush_group($group);
}

function wp_cache_supports($feature)
{
    switch ($feature) {
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

function wp_cache_close()
{
    global $wp_object_cache;

    return $wp_object_cache->close();
}

function wp_cache_add_global_groups($groups)
{
    global $wp_object_cache;
    $wp_object_cache->add_global_groups($groups);
}

function wp_cache_add_non_persistent_groups($groups)
{
    global $wp_object_cache;
    $wp_object_cache->add_non_persistent_groups($groups);
}

function wp_cache_switch_to_blog($blog_id)
{
    global $wp_object_cache;
    $wp_object_cache->switch_to_blog($blog_id);
}

function wp_cache_flush_runtime()
{
    global $wp_object_cache;

    return $wp_object_cache->flush_runtime();
}

/**
 * Jabali Redis Object Cache Class
 */
class Jabali_Redis_Object_Cache
{
    /**
     * Redis connection
     *
     * @var Redis|null
     */
    private $redis = null;

    /**
     * Whether Redis is connected
     *
     * @var bool
     */
    private $connected = false;

    /**
     * In-memory cache for current request
     *
     * @var array
     */
    private $cache = [];

    /**
     * Global cache groups
     *
     * @var array
     */
    private $global_groups = [];

    /**
     * Non-persistent groups (not stored in Redis)
     *
     * @var array
     */
    private $non_persistent_groups = [];

    /**
     * Current blog ID for multisite
     *
     * @var int
     */
    private $blog_prefix = '';

    /**
     * Redis key prefix
     *
     * @var string
     */
    private $prefix = '';

    /**
     * Cache hits counter
     *
     * @var int
     */
    public $cache_hits = 0;

    /**
     * Cache misses counter
     *
     * @var int
     */
    public $cache_misses = 0;

    /**
     * Constructor
     */
    public function __construct()
    {
        $this->prefix = defined('JABALI_CACHE_PREFIX') ? JABALI_CACHE_PREFIX : 'jc_';
        $this->blog_prefix = $this->get_blog_prefix();
        $this->connect();
    }

    /**
     * Connect to Redis
     */
    private function connect()
    {
        try {
            $this->redis = new Redis;

            $host = defined('JABALI_CACHE_HOST') ? JABALI_CACHE_HOST : '127.0.0.1';
            $port = defined('JABALI_CACHE_PORT') ? JABALI_CACHE_PORT : 6379;
            $timeout = defined('JABALI_CACHE_TIMEOUT') ? JABALI_CACHE_TIMEOUT : 1;
            $database = defined('JABALI_CACHE_DATABASE') ? JABALI_CACHE_DATABASE : 0;

            $this->connected = $this->redis->connect($host, $port, $timeout);

            if ($this->connected) {
                // Authenticate with Redis ACL (username + password) or simple password
                if (defined('JABALI_CACHE_REDIS_USER') && defined('JABALI_CACHE_REDIS_PASS')) {
                    $this->redis->auth(['user' => JABALI_CACHE_REDIS_USER, 'pass' => JABALI_CACHE_REDIS_PASS]);
                } elseif (defined('JABALI_CACHE_REDIS_PASS') && JABALI_CACHE_REDIS_PASS) {
                    $this->redis->auth(JABALI_CACHE_REDIS_PASS);
                } elseif (defined('JABALI_CACHE_PASSWORD') && JABALI_CACHE_PASSWORD) {
                    // Legacy support
                    $this->redis->auth(JABALI_CACHE_PASSWORD);
                }

                // Select database
                if ($database) {
                    $this->redis->select($database);
                }

                $this->redis->setOption(Redis::OPT_SERIALIZER, Redis::SERIALIZER_PHP);
            }
        } catch (Exception $e) {
            $this->connected = false;
            $this->redis = null;
        }
    }

    /**
     * Get blog prefix for multisite
     */
    private function get_blog_prefix()
    {
        global $blog_id;
        if (is_multisite()) {
            return (int) $blog_id.':';
        }

        return '';
    }

    /**
     * Build cache key
     */
    private function build_key($key, $group = 'default')
    {
        if (empty($group)) {
            $group = 'default';
        }

        $prefix = in_array($group, $this->global_groups) ? '' : $this->blog_prefix;

        return $this->prefix.$prefix.$group.':'.$key;
    }

    /**
     * Check if group is non-persistent
     */
    private function is_non_persistent($group)
    {
        return in_array($group, $this->non_persistent_groups);
    }

    /**
     * Add data to cache if it doesn't exist
     */
    public function add($key, $data, $group = 'default', $expire = 0)
    {
        if (wp_suspend_cache_addition()) {
            return false;
        }

        $cache_key = $this->build_key($key, $group);

        if (isset($this->cache[$cache_key])) {
            return false;
        }

        return $this->set($key, $data, $group, $expire);
    }

    /**
     * Replace data in cache if it exists
     */
    public function replace($key, $data, $group = 'default', $expire = 0)
    {
        $cache_key = $this->build_key($key, $group);

        if (! isset($this->cache[$cache_key]) && ! $this->redis_exists($cache_key)) {
            return false;
        }

        return $this->set($key, $data, $group, $expire);
    }

    /**
     * Check if key exists in Redis
     */
    private function redis_exists($cache_key)
    {
        if (! $this->connected) {
            return false;
        }
        try {
            return $this->redis->exists($cache_key);
        } catch (Exception $e) {
            return false;
        }
    }

    /**
     * Set data in cache
     */
    public function set($key, $data, $group = 'default', $expire = 0)
    {
        $cache_key = $this->build_key($key, $group);

        // Always store in local cache
        $this->cache[$cache_key] = $data;

        // Store in Redis if persistent
        if (! $this->is_non_persistent($group) && $this->connected) {
            try {
                $expire = (int) $expire;
                if ($expire > 0) {
                    $this->redis->setex($cache_key, $expire, $data);
                } else {
                    $this->redis->set($cache_key, $data);
                }
            } catch (Exception $e) {
                return false;
            }
        }

        return true;
    }

    /**
     * Get data from cache
     */
    public function get($key, $group = 'default', $force = false, &$found = null)
    {
        $cache_key = $this->build_key($key, $group);

        // Check local cache first (unless forced)
        if (! $force && isset($this->cache[$cache_key])) {
            $found = true;
            $this->cache_hits++;

            return $this->cache[$cache_key];
        }

        // For non-persistent groups, only check local cache
        if ($this->is_non_persistent($group)) {
            $found = false;
            $this->cache_misses++;

            return false;
        }

        // Check Redis
        if ($this->connected) {
            try {
                $data = $this->redis->get($cache_key);
                if ($data !== false) {
                    $found = true;
                    $this->cache_hits++;
                    $this->cache[$cache_key] = $data;

                    return $data;
                }
            } catch (Exception $e) {
                // Fall through to miss
            }
        }

        $found = false;
        $this->cache_misses++;

        return false;
    }

    /**
     * Delete data from cache
     */
    public function delete($key, $group = 'default')
    {
        $cache_key = $this->build_key($key, $group);

        unset($this->cache[$cache_key]);

        if (! $this->is_non_persistent($group) && $this->connected) {
            try {
                $this->redis->del($cache_key);
            } catch (Exception $e) {
                return false;
            }
        }

        return true;
    }

    /**
     * Increment numeric value
     */
    public function incr($key, $offset = 1, $group = 'default')
    {
        $cache_key = $this->build_key($key, $group);

        if (! isset($this->cache[$cache_key])) {
            $this->cache[$cache_key] = 0;
        }

        $this->cache[$cache_key] += $offset;

        if ($this->cache[$cache_key] < 0) {
            $this->cache[$cache_key] = 0;
        }

        if (! $this->is_non_persistent($group) && $this->connected) {
            try {
                $this->redis->incrBy($cache_key, $offset);
            } catch (Exception $e) {
                // Continue with local cache
            }
        }

        return $this->cache[$cache_key];
    }

    /**
     * Decrement numeric value
     */
    public function decr($key, $offset = 1, $group = 'default')
    {
        return $this->incr($key, -$offset, $group);
    }

    /**
     * Flush entire cache
     */
    public function flush()
    {
        $this->cache = [];

        if ($this->connected) {
            try {
                // Only flush keys with our prefix
                $keys = $this->redis->keys($this->prefix.'*');
                if (! empty($keys)) {
                    $this->redis->del($keys);
                }
            } catch (Exception $e) {
                return false;
            }
        }

        return true;
    }

    /**
     * Flush a specific group
     */
    public function flush_group($group)
    {
        $pattern = $this->build_key('*', $group);

        // Clear from local cache
        foreach (array_keys($this->cache) as $key) {
            if (strpos($key, $this->prefix.$this->blog_prefix.$group.':') === 0) {
                unset($this->cache[$key]);
            }
        }

        if ($this->connected) {
            try {
                $keys = $this->redis->keys($pattern);
                if (! empty($keys)) {
                    $this->redis->del($keys);
                }
            } catch (Exception $e) {
                return false;
            }
        }

        return true;
    }

    /**
     * Flush runtime (local) cache only
     */
    public function flush_runtime()
    {
        $this->cache = [];

        return true;
    }

    /**
     * Close Redis connection
     */
    public function close()
    {
        if ($this->connected && $this->redis) {
            try {
                $this->redis->close();
            } catch (Exception $e) {
                // Ignore
            }
        }

        return true;
    }

    /**
     * Add global groups
     */
    public function add_global_groups($groups)
    {
        $groups = (array) $groups;
        $this->global_groups = array_merge($this->global_groups, $groups);
        $this->global_groups = array_unique($this->global_groups);
    }

    /**
     * Add non-persistent groups
     */
    public function add_non_persistent_groups($groups)
    {
        $groups = (array) $groups;
        $this->non_persistent_groups = array_merge($this->non_persistent_groups, $groups);
        $this->non_persistent_groups = array_unique($this->non_persistent_groups);
    }

    /**
     * Switch to blog (multisite)
     */
    public function switch_to_blog($blog_id)
    {
        $this->blog_prefix = (int) $blog_id.':';
    }

    /**
     * Get stats
     */
    public function stats()
    {
        $stats = [
            'hits' => $this->cache_hits,
            'misses' => $this->cache_misses,
            'ratio' => $this->cache_hits + $this->cache_misses > 0
                ? round($this->cache_hits / ($this->cache_hits + $this->cache_misses) * 100, 2)
                : 0,
            'connected' => $this->connected,
            'prefix' => $this->prefix,
        ];

        if ($this->connected) {
            try {
                $info = $this->redis->info();
                $stats['redis_version'] = $info['redis_version'] ?? 'unknown';
                $stats['used_memory'] = $info['used_memory_human'] ?? 'unknown';
                $stats['connected_clients'] = $info['connected_clients'] ?? 0;
                $stats['total_keys'] = count($this->redis->keys($this->prefix.'*'));
            } catch (Exception $e) {
                // Ignore
            }
        }

        return $stats;
    }

    /**
     * Check if connected
     */
    public function is_connected()
    {
        return $this->connected;
    }
}
