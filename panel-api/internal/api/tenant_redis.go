package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
)

// Tenant Redis access (GH #1016). Jabali locks Redis behind ACL auth (ADR-0148):
// the `default` no-AUTH user is disabled, so a migrated app that used to connect
// unauthenticated (`127.0.0.1:6379`, raw keys) now gets `NOAUTH Authentication
// required` with no obvious way to obtain credentials. This endpoint hands each
// tenant a scoped, ready-to-use Redis credential.
//
// It generalises the per-install WP-cache ACL primitive (applications_cache.go)
// to a per-TENANT credential:
//
//   - user      t_<osuser>
//   - keyspace  ~jt:<osuser>:*   (namespaced so it can never overlap the panel's
//               own jabali:* / automation:* keys, nor the WP-cache jc:* keys)
//   - commands  a curated allowlist of the data-type commands a cache / session /
//               queue app needs — deliberately WITHOUT the keyspace-level
//               enumeration/admin commands (KEYS, SCAN, RANDOMKEY, DBSIZE,
//               FLUSH*, CONFIG, scripting, pub/sub). Those either ignore the
//               key-pattern fence entirely (Redis does not pattern-scope SCAN /
//               RANDOMKEY / DBSIZE — Gitea #413) or let one tenant enumerate
//               another's key names. Per-key scans (HSCAN/SSCAN/ZSCAN) operate
//               on a single fenced key and ARE safe, so they stay.
//
// The password is an HMAC of (global secret, osuser, per-tenant salt) — never
// stored, recomputed on demand, so "show me my credentials" and "re-apply the
// ACL" are the same idempotent operation. Rotating one tenant = rotating its
// salt, no effect on any other tenant.
//
// Transport is the unix socket only: jabali Redis has NO TCP listener (port 0,
// ADR-0059), and every tenant OS user is already in jabali-redis-clients, so the
// socket is reachable from their PHP-FPM / CLI without any further grant.

// tenantRedisACLUser is the per-tenant Redis ACL username.
func tenantRedisACLUser(osUser string) string { return "t_" + osUser }

// tenantRedisKeyPattern fences the tenant to its own namespace. The `jt:` prefix
// (jabali-tenant) guarantees no overlap with jabali:* / automation:* / jc:*,
// even for an osuser that happened to equal one of those words.
func tenantRedisKeyPattern(osUser string) string { return "~jt:" + osUser + ":*" }

// tenantRedisKeyPrefix is the string the tenant must set as their client's key
// prefix so their keys land inside the fence.
func tenantRedisKeyPrefix(osUser string) string { return "jt:" + osUser + ":" }

// tenantRedisToken derives the per-tenant ACL password. Mirrors
// cacheInstallToken but with its own domain-separation label so a tenant's
// general credential and its WP-cache install credential never collide.
func tenantRedisToken(secret, osUser, salt string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte("tenant-redis:" + osUser + ":" + salt))
	return hex.EncodeToString(m.Sum(nil))
}

// tenantRedisCommands is the curated allowlist. Covers strings, hashes, lists,
// sets, sorted sets, bitmaps, hyperloglog, geo, per-key scans, per-key expiry,
// transactions, and connection — the commands a caching / session / queue
// workload needs. It intentionally omits: keyspace-level SCAN/KEYS/RANDOMKEY/
// DBSIZE (not pattern-scoped → cross-tenant enumeration), FLUSHDB/FLUSHALL/
// SWAPDB, CONFIG/CLIENT/ACL/CLUSTER/DEBUG/SHUTDOWN/SAVE (admin), EVAL/FUNCTION
// (scripting), and SUBSCRIBE/PUBLISH (channels are reset). MULTI/EXEC only queue
// the commands above, which stay individually ACL-checked.
var tenantRedisCommands = []string{
	// strings
	"+GET", "+SET", "+SETEX", "+PSETEX", "+SETNX", "+GETSET", "+GETDEL", "+GETEX",
	"+APPEND", "+STRLEN", "+GETRANGE", "+SETRANGE", "+MGET", "+MSET", "+MSETNX",
	"+INCR", "+INCRBY", "+INCRBYFLOAT", "+DECR", "+DECRBY",
	// generic key ops (all fenced to the tenant's pattern)
	"+DEL", "+UNLINK", "+EXISTS", "+TYPE", "+EXPIRE", "+PEXPIRE", "+EXPIREAT",
	"+PEXPIREAT", "+EXPIRETIME", "+PEXPIRETIME", "+TTL", "+PTTL", "+PERSIST",
	"+RENAME", "+RENAMENX", "+TOUCH", "+DUMP", "+RESTORE", "+OBJECT",
	// hashes
	"+HSET", "+HSETNX", "+HGET", "+HMGET", "+HMSET", "+HDEL", "+HGETALL",
	"+HKEYS", "+HVALS", "+HLEN", "+HEXISTS", "+HINCRBY", "+HINCRBYFLOAT",
	"+HSTRLEN", "+HRANDFIELD", "+HSCAN",
	// lists
	"+LPUSH", "+RPUSH", "+LPUSHX", "+RPUSHX", "+LPOP", "+RPOP", "+LRANGE",
	"+LLEN", "+LINDEX", "+LSET", "+LREM", "+LTRIM", "+LINSERT", "+RPOPLPUSH",
	"+LMOVE", "+LPOS",
	// sets
	"+SADD", "+SREM", "+SMEMBERS", "+SISMEMBER", "+SMISMEMBER", "+SCARD",
	"+SPOP", "+SRANDMEMBER", "+SSCAN", "+SUNION", "+SINTER", "+SDIFF",
	"+SUNIONSTORE", "+SINTERSTORE", "+SDIFFSTORE", "+SMOVE",
	// sorted sets
	"+ZADD", "+ZREM", "+ZRANGE", "+ZREVRANGE", "+ZRANGEBYSCORE",
	"+ZREVRANGEBYSCORE", "+ZRANGEBYLEX", "+ZREVRANGEBYLEX", "+ZCARD", "+ZSCORE",
	"+ZMSCORE", "+ZRANK", "+ZREVRANK", "+ZINCRBY", "+ZCOUNT", "+ZLEXCOUNT",
	"+ZPOPMIN", "+ZPOPMAX", "+ZRANDMEMBER", "+ZSCAN", "+ZRANGESTORE",
	"+ZREMRANGEBYRANK", "+ZREMRANGEBYSCORE", "+ZREMRANGEBYLEX",
	// bitmaps / hyperloglog / geo (all key-scoped)
	"+SETBIT", "+GETBIT", "+BITCOUNT", "+BITPOS", "+BITOP", "+BITFIELD",
	"+PFADD", "+PFCOUNT", "+PFMERGE",
	"+GEOADD", "+GEOPOS", "+GEODIST", "+GEOSEARCH", "+GEOSEARCHSTORE",
	// transactions (only queue the above)
	"+MULTI", "+EXEC", "+DISCARD", "+WATCH", "+UNWATCH",
	// connection / handshake
	"+PING", "+AUTH", "+HELLO", "+SELECT", "+ECHO", "+RESET", "+COMMAND",
}

// redisAccessResponse is what a tenant needs to configure their Redis client.
type redisAccessResponse struct {
	Socket          string   `json:"socket"`
	Host            string   `json:"host"` // "" — no TCP listener (socket only)
	Port            int      `json:"port"` // 0 — no TCP listener
	Username        string   `json:"username"`
	Password        string   `json:"password"`
	Database        int      `json:"database"`
	KeyPrefix       string   `json:"key_prefix"`
	AllowedCommands []string `json:"allowed_commands"`
	Note            string   `json:"note"`
}

// redisAccessHandler serves the tenant Redis-access endpoints. Reuses
// ApplicationHandlerConfig for Redis + the cache-token secret/salts + Users.
type redisAccessHandler struct{ cfg ApplicationHandlerConfig }

// RegisterRedisAccessRoutes wires the tenant + admin endpoints off the v1 group
// (which already carries RequireKratosSession, mirroring RegisterAuditRoutes).
// /me/redis-access is session-scoped to the caller; the admin view of a specific
// tenant's credentials adds RequireAdmin on its own route.
func RegisterRedisAccessRoutes(g *gin.RouterGroup, cfg ApplicationHandlerConfig) {
	h := &redisAccessHandler{cfg: cfg}
	g.GET("/me/redis-access", h.meRedisAccess)
	g.GET("/users/:id/redis-access", middleware.RequireAdmin(), h.userRedisAccess)
}

func (h *redisAccessHandler) meRedisAccess(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil || claims.UserID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	h.serve(c, claims.UserID)
}

func (h *redisAccessHandler) userRedisAccess(c *gin.Context) {
	h.serve(c, c.Param("id"))
}

// serve resolves the tenant, provisions (idempotently) their Redis ACL, and
// returns the ready-to-use credentials.
func (h *redisAccessHandler) serve(c *gin.Context, userID string) {
	ctx := c.Request.Context()
	if h.cfg.Redis == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "redis_unavailable", "detail": "Redis is not configured on this host"})
		return
	}
	u, err := h.cfg.Users.FindByID(ctx, userID)
	if err != nil || u == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user_not_found"})
		return
	}
	// Redis access is a TENANT feature: it needs a Linux account to scope the
	// keyspace to and to reach the socket. Admins have no OS user.
	if u.Username == nil || *u.Username == "" {
		c.JSON(http.StatusConflict, gin.H{"error": "no_linux_user", "detail": "Redis access is only available for hosting users (admins have no Linux account)"})
		return
	}
	osUser := *u.Username

	salt := ""
	if h.cfg.CacheTokenSalts != nil {
		s, sErr := h.cfg.CacheTokenSalts.GetOrCreate(ctx, userID)
		if sErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		salt = s
	}
	token := tenantRedisToken(h.cfg.CacheTokenSecret, osUser, salt)

	if err := tenantRedisProvision(h, ctx, osUser, token); err != nil {
		slog.ErrorContext(ctx, "tenant redis access: ACL provision failed", "user_id", userID, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "acl_provision_failed"})
		return
	}

	c.JSON(http.StatusOK, redisAccessResponse{
		Socket:          "/run/redis/redis.sock",
		Host:            "",
		Port:            0,
		Username:        tenantRedisACLUser(osUser),
		Password:        token,
		Database:        0,
		KeyPrefix:       tenantRedisKeyPrefix(osUser),
		AllowedCommands: tenantRedisCommands,
		Note: "Redis is reachable ONLY over the unix socket (no TCP). Authenticate with the " +
			"username + password above, and set your client's key prefix to \"" + tenantRedisKeyPrefix(osUser) +
			"\" — your credential can only read and write keys under that prefix. " +
			"Keyspace-scanning and admin commands (KEYS, SCAN, FLUSHDB, CONFIG, …) are not permitted.",
	})
}

// tenantRedisSetUserArgs builds the exact `ACL SETUSER` argument vector for a
// tenant. Pure + unit-tested: `reset` makes the rule absolute on every re-apply
// (idempotent), `resetchannels` denies all pub/sub, the fence is the tenant's
// namespace, and only the curated commands are granted.
func tenantRedisSetUserArgs(osUser, token string) []any {
	args := []any{"ACL", "SETUSER", tenantRedisACLUser(osUser),
		"reset",
		"on", ">" + token,
		"resetchannels",
		tenantRedisKeyPattern(osUser),
	}
	for _, cmd := range tenantRedisCommands {
		args = append(args, cmd)
	}
	return args
}

// provisionTenantRedisACL creates/updates the per-tenant ACL user, then persists
// the aclfile so it survives a redis restart.
func (h *redisAccessHandler) provisionTenantRedisACL(ctx context.Context, osUser, token string) error {
	if err := h.cfg.Redis.Do(ctx, tenantRedisSetUserArgs(osUser, token)...).Err(); err != nil {
		return err
	}
	return h.cfg.Redis.Do(ctx, "ACL", "SAVE").Err()
}

// tenantRedisProvision is a seam: handler tests override it to exercise serve()
// without a Redis that implements ACL SETUSER (miniredis does not). Production
// wiring is the real method.
var tenantRedisProvision = func(h *redisAccessHandler, ctx context.Context, osUser, token string) error {
	return h.provisionTenantRedisACL(ctx, osUser, token)
}

// The t_<osuser> user is torn down on account delete by revokeAllUserCacheACLs
// (applications_cache.go), which the userops delete cascade already invokes as
// RevokeCacheACLs — so a recycled username can't inherit the old principal.
