package api

// WP-cache cleanup (ADR-0173). Since jabali-cache 1.2.0 a flush bumps the
// site's generation counter instead of deleting keys, so keys from older
// generations stay in Redis until something removes them. The panel removes
// them: one SCAN over Redis DB 1 per pass, as jabali_panel (which may SCAN),
// and every read and delete as the install's own wp_<osuser>_<install> user,
// which Redis fences to that install's keys. The same pass enforces the
// per-site key budget (GH #612) and records each site's key count for the
// cache drawer.
//
// It also reaps per-install ACL users whose install row no longer exists (an
// app deleted before deletes revoked them): their keys are removed first,
// through the user itself, then the user.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

const (
	wpCacheGCInterval   = 15 * time.Minute
	wpCacheGCFirstDelay = 2 * time.Minute
	// wpCacheGCBudget bounds one pass. A pass that runs out stops where it is;
	// the next pass starts over.
	wpCacheGCBudget = 5 * time.Minute
	// wpCacheGCBatch is the SCAN COUNT hint and the most keys one UNLINK carries.
	wpCacheGCBatch = 1000
	// wpCacheTrimMaxPerPass caps the keys the budget trim removes from one
	// install per pass (the plugin's TRIM_MAX_PER_RUN).
	wpCacheTrimMaxPerPass = 5000
	// wpCacheKeysPerMB turns the redis_maxmemory_mb budget into a key count,
	// the same ~2 KiB per key estimate the plugin's trim uses.
	wpCacheKeysPerMB = 512
	// wpCacheDB is the Redis database WordPress caches live in (ADR-0059).
	wpCacheDB = 1
)

// WPCacheGCConfig is what the cleanup pass needs. Any nil dependency, or an
// empty secret, disables it.
type WPCacheGCConfig struct {
	Redis    *redis.Client
	Installs repository.ApplicationInstallRepository
	Users    repository.UserRepository
	Salts    repository.CacheTokenSaltRepository
	Secret   string
}

func (c WPCacheGCConfig) ready() bool {
	return c.Redis != nil && c.Installs != nil && c.Users != nil && c.Secret != ""
}

// wpCachePrincipal is the part of an install's own Redis connection the
// cleanup reads and deletes through.
type wpCachePrincipal interface {
	Get(ctx context.Context, key string) *redis.StringCmd
	Unlink(ctx context.Context, keys ...string) *redis.IntCmd
	Close() error
}

// wpCachePrincipalConn opens a connection to DB 1 as user. A seam so tests can
// enforce the install's key fence (miniredis has no ACLs).
var wpCachePrincipalConn = func(panel *redis.Client, user, password string) wpCachePrincipal {
	return redis.NewClient(redisDBClientOptions(panel.Options(), user, password, wpCacheDB))
}

// wpCacheACLUsers lists the Redis ACL usernames; wpCacheACL runs one ACL
// command. Seams for the same reason.
var (
	wpCacheACLUsers = func(ctx context.Context, panel *redis.Client) ([]string, error) {
		lines, err := panel.Do(ctx, "ACL", "LIST").StringSlice()
		if err != nil {
			return nil, err
		}
		var names []string
		for _, line := range lines {
			if f := strings.Fields(line); len(f) >= 2 && f[0] == "user" {
				names = append(names, f[1])
			}
		}
		return names, nil
	}
	wpCacheACL = func(ctx context.Context, panel *redis.Client, args ...any) error {
		return panel.Do(ctx, append([]any{"ACL"}, args...)...).Err()
	}
)

// wpCacheSiteStats is what the last pass saw for one install.
type wpCacheSiteStats struct {
	Keys     int64     // object keys of the current generation, plus pages
	At       time.Time // when the pass finished with this install
	Complete bool      // the pass scanned the whole database
}

var wpCacheStats sync.Map // install ID -> wpCacheSiteStats

// wpCacheStatsFor returns the last pass's key count for an install.
func wpCacheStatsFor(installID string) (wpCacheSiteStats, bool) {
	v, ok := wpCacheStats.Load(installID)
	if !ok {
		return wpCacheSiteStats{}, false
	}
	return v.(wpCacheSiteStats), true
}

// StartWPCacheGC runs the cleanup pass every wpCacheGCInterval until ctx is
// cancelled.
func StartWPCacheGC(ctx context.Context, cfg WPCacheGCConfig) {
	if !cfg.ready() {
		return
	}
	slog.Info("wp-cache gc starting", "interval", wpCacheGCInterval.String())
	select {
	case <-ctx.Done():
		return
	case <-time.After(wpCacheGCFirstDelay):
	}
	runWPCacheGC(ctx, cfg)
	t := time.NewTicker(wpCacheGCInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			runWPCacheGC(ctx, cfg)
		}
	}
}

// wpCacheGCResult summarises one pass (logged; returned for tests).
type wpCacheGCResult struct {
	Sites        int
	Stale        int64
	Trimmed      int64
	Complete     bool
	ReapedUsers  int
	SkippedSites int
}

// wpCacheGCSite is one cache-enabled install during a pass.
type wpCacheGCSite struct {
	installID, userID, osUser string
	prefix                    string // jc:<osuser>:<install>:
	budget                    int64  // max object keys of the current generation; 0 = none

	loaded bool   // principal + generation looked up
	failed bool   // no usable principal this pass: delete nothing
	token  string // the install's ACL password
	gen    string // current gen:o; "" = none yet (the site isn't on 1.2.0+)

	live, pages    int64
	stale, trimmed int64
	pending        []string
}

// wpCacheBucket splits a DB 1 key into its install prefix parts. ok is false
// for keys that aren't jc:<osuser>:<ULID>:<rest>.
func wpCacheBucket(key string) (osUser, installID, rest string, ok bool) {
	if !strings.HasPrefix(key, "jc:") {
		return "", "", "", false
	}
	parts := strings.SplitN(key[len("jc:"):], ":", 3)
	if len(parts) != 3 || parts[0] == "" || !ulidRe.MatchString(parts[1]) {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

var (
	ulidRe           = regexp.MustCompile(`^[0-9A-Z]{26}$`)
	installACLUserRe = regexp.MustCompile(`^wp_(.+)_([0-9A-Z]{26})$`)
	objectGenRe      = regexp.MustCompile(`^o([0-9]+):`)
)

type wpCacheKeyKind int

const (
	wpKeyKeep  wpCacheKeyKind = iota // counters, locks, probes: never counted or deleted
	wpKeyPage                        // page cache: left to its TTL
	wpKeyLive                        // object key of the current (or a newer) generation
	wpKeyStale                       // object key nothing will read again
)

// classifyWPCacheKey sorts the part of a key after the install prefix. gen is
// the install's current gen:o, "" when it has none.
func classifyWPCacheKey(rest, gen string) wpCacheKeyKind {
	switch {
	case strings.HasPrefix(rest, "gen:"), strings.HasPrefix(rest, "lock:"), strings.HasPrefix(rest, "__jabali"):
		return wpKeyKeep
	case strings.HasPrefix(rest, "page:"):
		return wpKeyPage
	}
	if m := objectGenRe.FindStringSubmatch(rest); m != nil {
		// Only an older generation is stale. A newer one means the site
		// flushed after this pass read gen:o.
		if gen != "" && genOlder(m[1], gen) {
			return wpKeyStale
		}
		return wpKeyLive
	}
	// A 1.1.0 key (no generation). Once gen:o exists the site runs 1.2.0+ and
	// never reads these again; without it the site may still be on 1.1.0,
	// where every key is live.
	if gen != "" {
		return wpKeyStale
	}
	return wpKeyLive
}

// genOlder compares two generations (digit strings, no leading zeros).
func genOlder(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

// runWPCacheGC is one cleanup pass.
func runWPCacheGC(parent context.Context, cfg WPCacheGCConfig) wpCacheGCResult {
	ctx, cancel := context.WithTimeout(parent, wpCacheGCBudget)
	defer cancel()
	var res wpCacheGCResult

	installs, total, err := cfg.Installs.List(ctx, repository.ListOptions{})
	if err != nil {
		slog.WarnContext(ctx, "wp-cache gc: list installs", "err", err)
		return res
	}
	exists := make(map[string]bool, len(installs))
	sites := map[string]*wpCacheGCSite{}
	osUsers := map[string]string{} // user ID -> linux username
	for i := range installs {
		in := &installs[i]
		exists[in.ID] = true
		if in.AppType != "wordpress" || !in.CacheEnabled || in.Status != "ready" {
			continue
		}
		osUser, ok := osUsers[in.UserID]
		if !ok {
			if u, uErr := cfg.Users.FindByID(ctx, in.UserID); uErr == nil && u != nil && u.Username != nil {
				osUser = *u.Username
			}
			osUsers[in.UserID] = osUser
		}
		if osUser == "" {
			continue
		}
		sites[in.ID] = &wpCacheGCSite{
			installID: in.ID,
			userID:    in.UserID,
			osUser:    osUser,
			prefix:    "jc:" + osUser + ":" + in.ID + ":",
			budget:    wpCacheBudgetKeys(in),
		}
	}
	res.Sites = len(sites)

	res.Complete = wpCacheScan(ctx, cfg, sites)
	now := time.Now().UTC()
	for _, s := range sites {
		s.flush(ctx, cfg)
		if s.failed {
			res.SkippedSites++
			continue
		}
		res.Stale += s.stale
		res.Trimmed += s.trimmed
		wpCacheStats.Store(s.installID, wpCacheSiteStats{Keys: s.live + s.pages, At: now, Complete: res.Complete})
	}
	// Forget installs that are gone or no longer cached.
	wpCacheStats.Range(func(k, _ any) bool {
		if _, ok := sites[k.(string)]; !ok {
			wpCacheStats.Delete(k)
		}
		return true
	})

	// Reaping decides from the install list, so it needs the whole list.
	if int64(len(installs)) == total {
		res.ReapedUsers = wpCacheReapOrphans(ctx, cfg, exists)
	}
	slog.InfoContext(ctx, "wp-cache gc", "sites", res.Sites, "stale_deleted", res.Stale,
		"trimmed", res.Trimmed, "complete", res.Complete, "reaped_users", res.ReapedUsers,
		"skipped_sites", res.SkippedSites)
	return res
}

// wpCacheBudgetKeys is the install's object-key budget (GH #612); 0 = none.
func wpCacheBudgetKeys(in *models.ApplicationInstall) int64 {
	settings, _ := in.ParseCacheSettings()
	if settings.RedisMaxMemoryMB <= 0 {
		return 0
	}
	return int64(settings.RedisMaxMemoryMB) * wpCacheKeysPerMB
}

// wpCacheScan walks DB 1 once and sorts every install's keys. It returns
// whether it reached the end.
func wpCacheScan(ctx context.Context, cfg WPCacheGCConfig, sites map[string]*wpCacheGCSite) bool {
	// Its own client on DB 1: never SELECT on the panel's pooled client (the
	// connection would go back to the pool on DB 1; see tenantRedisFlushDB).
	base := cfg.Redis.Options()
	conn := redis.NewClient(redisDBClientOptions(base, base.Username, base.Password, wpCacheDB))
	defer conn.Close()

	var cursor uint64
	for {
		keys, next, err := conn.Scan(ctx, cursor, "jc:*", wpCacheGCBatch).Result()
		if err != nil {
			if ctx.Err() == nil {
				slog.WarnContext(ctx, "wp-cache gc: scan", "err", err)
			}
			return false
		}
		for _, k := range keys {
			osUser, installID, rest, ok := wpCacheBucket(k)
			if !ok {
				continue
			}
			s := sites[installID]
			if s == nil || s.osUser != osUser {
				continue
			}
			s.add(ctx, cfg, k, rest)
		}
		if next == 0 {
			return true
		}
		cursor = next
	}
}

// load looks up the install's credential and current generation once per pass.
func (s *wpCacheGCSite) load(ctx context.Context, cfg WPCacheGCConfig) {
	s.loaded = true
	salt := ""
	if cfg.Salts != nil {
		v, err := cfg.Salts.GetOrCreate(ctx, s.userID)
		if err != nil {
			slog.WarnContext(ctx, "wp-cache gc: salt", "install_id", s.installID, "err", err)
			s.failed = true
			return
		}
		salt = v
	}
	s.token = cacheInstallToken(cfg.Secret, s.osUser, s.installID, salt)
	p := wpCachePrincipalConn(cfg.Redis, installACLUser(s.osUser, s.installID), s.token)
	defer p.Close()
	gen, err := p.Get(ctx, s.prefix+"gen:o").Result()
	switch {
	case errors.Is(err, redis.Nil):
		s.gen = ""
	case err != nil:
		slog.WarnContext(ctx, "wp-cache gc: read generation", "install_id", s.installID, "err", err)
		s.failed = true
	case !isDigits(gen):
		s.failed = true // not a generation the plugin wrote: leave the site alone
	default:
		s.gen = gen
	}
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// add sorts one of the install's keys.
func (s *wpCacheGCSite) add(ctx context.Context, cfg WPCacheGCConfig, key, rest string) {
	if !s.loaded {
		s.load(ctx, cfg)
	}
	if s.failed {
		return
	}
	switch classifyWPCacheKey(rest, s.gen) {
	case wpKeyPage:
		s.pages++
	case wpKeyStale:
		s.stale++
		s.queue(ctx, cfg, key)
	case wpKeyLive:
		if s.budget > 0 && s.live >= s.budget && s.trimmed < wpCacheTrimMaxPerPass {
			s.trimmed++
			s.queue(ctx, cfg, key)
			return
		}
		s.live++
	}
}

func (s *wpCacheGCSite) queue(ctx context.Context, cfg WPCacheGCConfig, key string) {
	s.pending = append(s.pending, key)
	if len(s.pending) >= wpCacheGCBatch {
		s.flush(ctx, cfg)
	}
}

// flush deletes the queued keys as the install itself.
func (s *wpCacheGCSite) flush(ctx context.Context, cfg WPCacheGCConfig) {
	if len(s.pending) == 0 || s.failed {
		s.pending = s.pending[:0]
		return
	}
	p := wpCachePrincipalConn(cfg.Redis, installACLUser(s.osUser, s.installID), s.token)
	defer p.Close()
	if err := p.Unlink(ctx, s.pending...).Err(); err != nil {
		slog.WarnContext(ctx, "wp-cache gc: unlink", "install_id", s.installID, "err", err)
		s.failed = true
	}
	s.pending = s.pending[:0]
}

// wpCachePurgeInstall deletes every key under one install's prefix, through
// that install's own ACL user. Used before the user is removed: once it's gone
// nothing can delete those keys (jabali_panel can't touch jc:* keys).
func wpCachePurgeInstall(ctx context.Context, panel *redis.Client, osUser, installID, password string) (int64, error) {
	prefix := "jc:" + osUser + ":" + installID + ":"
	base := panel.Options()
	conn := redis.NewClient(redisDBClientOptions(base, base.Username, base.Password, wpCacheDB))
	defer conn.Close()
	p := wpCachePrincipalConn(panel, installACLUser(osUser, installID), password)
	defer p.Close()

	var deleted int64
	var cursor uint64
	match := redisGlobEscape(prefix) + "*"
	for {
		keys, next, err := conn.Scan(ctx, cursor, match, wpCacheGCBatch).Result()
		if err != nil {
			return deleted, err
		}
		own := keys[:0]
		for _, k := range keys {
			if strings.HasPrefix(k, prefix) {
				own = append(own, k)
			}
		}
		if len(own) > 0 {
			n, err := p.Unlink(ctx, own...).Result()
			deleted += n
			if err != nil {
				return deleted, err
			}
		}
		if next == 0 {
			return deleted, nil
		}
		cursor = next
	}
}

// purgeAndRevokeInstallCache removes an install's cache keys and then its ACL
// user. Best-effort: a failed purge still revokes (the keys are then left to
// Redis's LRU). Used when a site's cache is disabled and when the app is
// deleted.
func purgeAndRevokeInstallCache(ctx context.Context, rdb *redis.Client, secret string, salts repository.CacheTokenSaltRepository, userID, osUser, installID string) error {
	if rdb == nil || osUser == "" || installID == "" {
		return nil
	}
	wpCacheStats.Delete(installID)
	if secret != "" {
		salt := ""
		saltOK := true
		if salts != nil {
			v, err := salts.GetOrCreate(ctx, userID)
			if err != nil {
				saltOK = false
				slog.WarnContext(ctx, "wp-cache: salt for purge", "install_id", installID, "err", err)
			}
			salt = v
		}
		if saltOK {
			token := cacheInstallToken(secret, osUser, installID, salt)
			if n, err := wpCachePurgeInstall(ctx, rdb, osUser, installID, token); err != nil {
				slog.WarnContext(ctx, "wp-cache: purge keys before revoke", "install_id", installID, "deleted", n, "err", err)
			}
		}
	}
	return revokeInstallACL(ctx, rdb, osUser, installID)
}

// wpCacheReapOrphans removes per-install ACL users whose install row no longer
// exists. Only a missing row counts: a cache enable provisions the user before
// it records cache_enabled, so the flag can lag the user. Each orphan's keys go
// first, through the orphan itself (given a fresh one-off password, since its
// token can't be derived without the row).
func wpCacheReapOrphans(ctx context.Context, cfg WPCacheGCConfig, exists map[string]bool) int {
	names, err := wpCacheACLUsers(ctx, cfg.Redis)
	if err != nil {
		slog.WarnContext(ctx, "wp-cache gc: acl list", "err", err)
		return 0
	}
	reaped := 0
	for _, name := range names {
		m := installACLUserRe.FindStringSubmatch(name)
		if m == nil || exists[m[2]] {
			continue
		}
		osUser, installID := m[1], m[2]
		pw, err := randomACLPassword()
		if err != nil {
			return reaped
		}
		if err := wpCacheACL(ctx, cfg.Redis, "SETUSER", name, ">"+pw); err == nil {
			if n, pErr := wpCachePurgeInstall(ctx, cfg.Redis, osUser, installID, pw); pErr != nil {
				slog.WarnContext(ctx, "wp-cache gc: purge orphan keys", "user", name, "deleted", n, "err", pErr)
			}
		}
		if err := wpCacheACL(ctx, cfg.Redis, "DELUSER", name); err != nil {
			slog.WarnContext(ctx, "wp-cache gc: remove orphan acl user", "user", name, "err", err)
			continue
		}
		slog.InfoContext(ctx, "wp-cache gc: removed orphan acl user", "user", name)
		reaped++
	}
	if reaped > 0 {
		if err := wpCacheACL(ctx, cfg.Redis, "SAVE"); err != nil {
			slog.WarnContext(ctx, "wp-cache gc: acl save", "err", err)
		}
	}
	return reaped
}

func randomACLPassword() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
