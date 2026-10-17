package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
)

// --- Tenant key browser (GH #2165) --------------------------------------------
//
// A tenant can't list its own keys: SCAN walks the whole keyspace and ignores
// the ACL key fence, so granting it would list every tenant's key names. The
// panel lists them instead, the same way it flushes (GH #2003):
//
//   - it SCANs for the tenant's prefix as jabali_panel, with MATCH anchored on
//     the escaped prefix and every name re-checked against the prefix;
//   - it reads each key's TYPE and PTTL, and deletes keys, with the tenant's
//     OWN t_<osuser> credential, so Redis checks every key against the
//     tenant's ~jt:<osuser>:* fence even if the match were wrong.
//
// One listing call does a bounded amount of scanning and hands back a cursor
// to carry on from, so a page costs the same however big the keyspace is.

const (
	// tenantRedisKeysDefaultLimit / MaxLimit: how many keys a listing call
	// looks for before it stops scanning. One SCAN batch can overshoot it.
	tenantRedisKeysDefaultLimit = 100
	tenantRedisKeysMaxLimit     = 500
	// tenantRedisKeysScanCount bounds the SCAN COUNT hint of one batch (see
	// tenantRedisScanCount).
	tenantRedisKeysScanCountMin = 100
	tenantRedisKeysScanCount    = 1000
	// tenantRedisKeysMaxScans bounds the SCAN calls of one listing call (at
	// most about 50,000 keyspace entries), however few of them are the tenant's.
	tenantRedisKeysMaxScans = 50
	// tenantRedisKeysBudget bounds one listing call's scanning time.
	tenantRedisKeysBudget = 5 * time.Second
	// tenantRedisKeysMaxMatch caps the tenant's MATCH pattern. Redis glob
	// matching backtracks, and a crafted pattern used to cost exponential time
	// (fixed in Redis 7.0.9); a short pattern plus the per-user scan rate limit
	// bounds what one tenant can make Redis do.
	tenantRedisKeysMaxMatch = 128
	// tenantRedisKeysMaxDelete is the most keys one delete call takes.
	tenantRedisKeysMaxDelete = 500
)

// redisKeyRow is one of the tenant's keys.
type redisKeyRow struct {
	DB int `json:"db"`
	// Key is the full key name, prefix included; Name is the part after the
	// prefix (what an app with the prefix option set calls it).
	Key  string `json:"key"`
	Name string `json:"name"`
	Type string `json:"type"`
	// TTLMs is the remaining time to live in milliseconds, -1 for no expiry.
	TTLMs int64 `json:"ttl_ms"`
	// ExpiresAt is when the key expires (UTC), null for no expiry.
	ExpiresAt *time.Time `json:"expires_at"`
	// Binary marks a name that isn't valid UTF-8. JSON can't carry it exactly,
	// so the name shown is approximate and the key can't be deleted by name
	// here (redis-cli with the tenant's credential can).
	Binary bool `json:"binary,omitempty"`
}

type redisKeysResponse struct {
	Data []redisKeyRow `json:"data"`
	// NextCursor continues the listing; "" when every database was scanned.
	NextCursor string `json:"next_cursor"`
}

// redisKeysQuery is a validated listing request.
type redisKeysQuery struct {
	match  string // glob, relative to the tenant's prefix
	limit  int
	db     int    // first database to scan
	cursor uint64 // SCAN cursor within db
	onlyDB bool   // scan db only
}

// parseRedisKeysQuery validates the listing query. On failure it returns the
// error code and detail for a 422.
func parseRedisKeysQuery(c *gin.Context) (redisKeysQuery, string, string) {
	q := redisKeysQuery{match: "*", limit: tenantRedisKeysDefaultLimit}

	if v := c.Query("match"); v != "" {
		if len(v) > tenantRedisKeysMaxMatch {
			return q, "invalid_match", fmt.Sprintf("match is at most %d bytes", tenantRedisKeysMaxMatch)
		}
		for i := 0; i < len(v); i++ {
			if v[i] < 0x20 || v[i] == 0x7f {
				return q, "invalid_match", "match must not contain control characters"
			}
		}
		q.match = v
	}
	if v := c.Query("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > tenantRedisKeysMaxLimit {
			return q, "invalid_limit", fmt.Sprintf("limit is 1 to %d", tenantRedisKeysMaxLimit)
		}
		q.limit = n
	}
	if v := c.Query("db"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n >= tenantRedisDatabases {
			return q, "invalid_db", fmt.Sprintf("db is 0 to %d", tenantRedisDatabases-1)
		}
		q.db, q.onlyDB = n, true
	}
	if v := c.Query("cursor"); v != "" {
		dbPart, curPart, found := strings.Cut(v, ":")
		db, dbErr := strconv.Atoi(dbPart)
		cur, curErr := strconv.ParseUint(curPart, 10, 64)
		if !found || dbErr != nil || curErr != nil || db < 0 || db >= tenantRedisDatabases {
			return q, "invalid_cursor", "cursor is the next_cursor of the previous page"
		}
		if q.onlyDB && db != q.db {
			return q, "invalid_cursor", "cursor belongs to another database"
		}
		q.db, q.cursor = db, cur
	}
	return q, "", ""
}

func (h *redisAccessHandler) meRedisKeys(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil || claims.UserID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	h.listKeys(c, claims.UserID)
}

func (h *redisAccessHandler) userRedisKeys(c *gin.Context) {
	h.listKeys(c, c.Param("id"))
}

// listKeys returns one page of the tenant's keys.
func (h *redisAccessHandler) listKeys(c *gin.Context, userID string) {
	q, code, detail := parseRedisKeysQuery(c)
	if code != "" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": code, "detail": detail})
		return
	}
	osUser, token, ok := h.provisionTenant(c, userID)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	rows, next, err := tenantRedisListKeys(ctx, h.cfg.Redis, osUser, token, q)
	if err != nil {
		slog.ErrorContext(ctx, "tenant redis key listing failed", "user_id", userID, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list_failed"})
		return
	}
	c.JSON(http.StatusOK, redisKeysResponse{Data: rows, NextCursor: next})
}

// tenantKeyConn is the tenant-credential connection the key browser reads and
// deletes through.
type tenantKeyConn interface {
	// Describe returns each key's TYPE and PTTL, in order.
	Describe(ctx context.Context, keys []string) ([]tenantKeyInfo, error)
	Unlink(ctx context.Context, keys ...string) (int64, error)
	Close() error
}

type tenantKeyInfo struct {
	Type string
	// PTTL is the remaining time to live; go-redis keeps Redis's -1 (no
	// expiry) and -2 (no such key) as raw values.
	PTTL  time.Duration
	Found bool
}

// tenantRedisKeyConnFor opens a connection to database db authenticated as
// the tenant. A seam so tests can watch which credential the reads and
// deletes go through.
var tenantRedisKeyConnFor = func(panel *redis.Client, osUser, token string, db int) tenantKeyConn {
	return &redisTenantKeyConn{c: redis.NewClient(tenantRedisClientOptions(panel.Options(), osUser, token, db))}
}

type redisTenantKeyConn struct{ c *redis.Client }

func (r *redisTenantKeyConn) Describe(ctx context.Context, keys []string) ([]tenantKeyInfo, error) {
	types := make([]*redis.StatusCmd, len(keys))
	ttls := make([]*redis.DurationCmd, len(keys))
	_, err := r.c.Pipelined(ctx, func(p redis.Pipeliner) error {
		for i, k := range keys {
			types[i] = p.Type(ctx, k)
			ttls[i] = p.PTTL(ctx, k)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]tenantKeyInfo, len(keys))
	for i := range keys {
		typ, ttl := types[i].Val(), ttls[i].Val()
		out[i] = tenantKeyInfo{Type: typ, PTTL: ttl, Found: typ != "none" && ttl != -2}
	}
	return out, nil
}

func (r *redisTenantKeyConn) Unlink(ctx context.Context, keys ...string) (int64, error) {
	return r.c.Unlink(ctx, keys...).Result()
}

func (r *redisTenantKeyConn) Close() error { return r.c.Close() }

// tenantRedisScanCount sizes the next SCAN batch. COUNT is how many keyspace
// entries Redis looks at, and every one that matches comes back, so on a
// keyspace that is mostly the tenant's a 1000 batch returns 1000 keys for a
// page of 100. The batch is sized from how dense the tenant's keys were so far:
// about what is still wanted, growing to the maximum on a sparse keyspace.
func tenantRedisScanCount(want, found, examined int) int64 {
	n := tenantRedisKeysScanCount
	if found > 0 && examined > 0 {
		n = want * examined / found
	} else if examined == 0 {
		n = want
	}
	if n < tenantRedisKeysScanCountMin {
		n = tenantRedisKeysScanCountMin
	}
	if n > tenantRedisKeysScanCount {
		n = tenantRedisKeysScanCount
	}
	return int64(n)
}

// tenantRedisListKeys scans for the tenant's keys from q's position and
// describes what it found. next is where the following call starts, "" when
// every database (or q's one database) was scanned.
func tenantRedisListKeys(ctx context.Context, panel *redis.Client, osUser, token string, q redisKeysQuery) ([]redisKeyRow, string, error) {
	prefix := tenantRedisKeyPrefix(osUser)
	match := redisGlobEscape(prefix) + q.match
	endDB := tenantRedisDatabases
	if q.onlyDB {
		endDB = q.db + 1
	}

	scanCtx, cancel := context.WithTimeout(ctx, tenantRedisKeysBudget)
	defer cancel()

	found := map[int][]string{}
	var order []int
	total, scans, examined := 0, 0, 0
	db, cursor := q.db, q.cursor
	next := ""
	stop := func() bool {
		return total >= q.limit || scans >= tenantRedisKeysMaxScans || scanCtx.Err() != nil
	}

scan:
	for db < endDB {
		// A separate client on database db with the panel's credential: never
		// SELECT on the panel's pooled client (see tenantRedisFlushDB).
		base := panel.Options()
		conn := redis.NewClient(redisDBClientOptions(base, base.Username, base.Password, db))
		for {
			count := tenantRedisScanCount(q.limit-total, total, examined)
			keys, nextCursor, err := conn.Scan(scanCtx, cursor, match, count).Result()
			scans++
			if err != nil {
				_ = conn.Close()
				if scanCtx.Err() != nil {
					// Out of time mid-call: carry on from the last cursor.
					next = fmt.Sprintf("%d:%d", db, cursor)
					break scan
				}
				return nil, "", fmt.Errorf("scan db %d: %w", db, err)
			}
			for _, k := range keys {
				if strings.HasPrefix(k, prefix) {
					if len(found[db]) == 0 {
						order = append(order, db)
					}
					found[db] = append(found[db], k)
					total++
				}
			}
			examined += int(count)
			cursor = nextCursor
			if cursor == 0 {
				break
			}
			if stop() {
				_ = conn.Close()
				next = fmt.Sprintf("%d:%d", db, cursor)
				break scan
			}
		}
		_ = conn.Close()
		db, cursor = db+1, 0
		if db < endDB && stop() {
			next = fmt.Sprintf("%d:0", db)
			break
		}
	}

	rows := make([]redisKeyRow, 0, total)
	now := time.Now().UTC()
	for _, d := range order {
		keys := found[d]
		kc := tenantRedisKeyConnFor(panel, osUser, token, d)
		infos, err := kc.Describe(ctx, keys)
		_ = kc.Close()
		if err != nil {
			return nil, "", fmt.Errorf("describe db %d: %w", d, err)
		}
		for i, k := range keys {
			info := infos[i]
			if !info.Found {
				continue // expired or deleted since the scan
			}
			row := redisKeyRow{
				DB:     d,
				Key:    k,
				Name:   strings.TrimPrefix(k, prefix),
				Type:   info.Type,
				TTLMs:  -1,
				Binary: !utf8.ValidString(k),
			}
			if info.PTTL >= 0 {
				row.TTLMs = info.PTTL.Milliseconds()
				exp := now.Add(info.PTTL).Truncate(time.Second)
				row.ExpiresAt = &exp
			}
			rows = append(rows, row)
		}
	}
	return rows, next, nil
}

// --- Delete -------------------------------------------------------------------

type redisKeysDeleteRequest struct {
	Keys []struct {
		DB  int    `json:"db"`
		Key string `json:"key"`
	} `json:"keys"`
}

type redisKeysDeleteResponse struct {
	Deleted int64 `json:"deleted"`
}

func (h *redisAccessHandler) meRedisKeysDelete(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil || claims.UserID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	h.deleteKeys(c, claims.UserID)
}

func (h *redisAccessHandler) userRedisKeysDelete(c *gin.Context) {
	h.deleteKeys(c, c.Param("id"))
}

// deleteKeys deletes the listed keys with the tenant's own credential. Every
// key must carry the tenant's prefix: a key outside it refuses the whole call
// before anything is deleted (Redis would refuse it anyway).
func (h *redisAccessHandler) deleteKeys(c *gin.Context, userID string) {
	var req redisKeysDeleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	if len(req.Keys) == 0 || len(req.Keys) > tenantRedisKeysMaxDelete {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_keys", "detail": fmt.Sprintf("send 1 to %d keys", tenantRedisKeysMaxDelete)})
		return
	}
	osUser, token, ok := h.provisionTenant(c, userID)
	if !ok {
		return
	}
	prefix := tenantRedisKeyPrefix(osUser)
	byDB := map[int][]string{}
	var order []int
	for _, k := range req.Keys {
		if k.DB < 0 || k.DB >= tenantRedisDatabases {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_db", "detail": fmt.Sprintf("db is 0 to %d", tenantRedisDatabases-1)})
			return
		}
		if len(k.Key) <= len(prefix) || !strings.HasPrefix(k.Key, prefix) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "key_outside_prefix", "detail": "every key must start with " + prefix})
			return
		}
		if len(byDB[k.DB]) == 0 {
			order = append(order, k.DB)
		}
		byDB[k.DB] = append(byDB[k.DB], k.Key)
	}

	ctx := c.Request.Context()
	var deleted int64
	for _, db := range order {
		kc := tenantRedisKeyConnFor(h.cfg.Redis, osUser, token, db)
		n, err := kc.Unlink(ctx, byDB[db]...)
		_ = kc.Close()
		deleted += n
		if err != nil {
			slog.ErrorContext(ctx, "tenant redis key delete failed", "user_id", userID, "db", db, "deleted", deleted, "err", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "delete_failed", "deleted": deleted})
			return
		}
	}
	slog.InfoContext(ctx, "tenant redis keys deleted", "user_id", userID, "requested", len(req.Keys), "deleted", deleted)
	c.JSON(http.StatusOK, redisKeysDeleteResponse{Deleted: deleted})
}
