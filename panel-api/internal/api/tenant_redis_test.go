package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// --- Security invariants (no Redis needed) ------------------------------------

// The tenant credential must NEVER equal the WP-cache install credential for the
// same tenant — different domain-separation labels — or a leaked general
// credential would also unlock the WP-cache keyspace.
func TestTenantRedisToken_DomainSeparatedFromWPCache(t *testing.T) {
	const secret, osUser, salt = "s3cr3t", "bob", "saltysalt"
	gen := tenantRedisToken(secret, osUser, salt)
	wp := cacheInstallToken(secret, osUser, "01ABCDEFGHIJKLMNOPQRSTUVWX", salt)
	if gen == wp {
		t.Fatal("tenant token collides with a WP-cache install token")
	}
	// Deterministic: same inputs → same token (so "show creds" == "re-apply ACL").
	if gen != tenantRedisToken(secret, osUser, salt) {
		t.Fatal("tenant token is not deterministic")
	}
	// Salt changes the token (per-tenant rotation without a global secret change).
	if gen == tenantRedisToken(secret, osUser, "different-salt") {
		t.Fatal("salt does not affect the token")
	}
}

// The keyspace fence must be namespaced under jt: so it can never overlap the
// panel's own keys (jabali:*, automation:*) or the WP-cache keys (jc:*), even
// for an osuser literally named "jabali".
func TestTenantRedisKeyspace_NamespacedAndNonOverlapping(t *testing.T) {
	for _, osUser := range []string{"bob", "jabali", "automation", "jc"} {
		pat := tenantRedisKeyPattern(osUser)
		if !strings.HasPrefix(pat, "~jt:") {
			t.Fatalf("fence %q for %q is not under the jt: namespace", pat, osUser)
		}
		if strings.HasPrefix(pat, "~jabali:") || strings.HasPrefix(pat, "~automation:") || strings.HasPrefix(pat, "~jc:") {
			t.Fatalf("fence %q overlaps a reserved namespace", pat)
		}
		if tenantRedisKeyPrefix(osUser) != "jt:"+osUser+":" {
			t.Fatalf("prefix mismatch for %q: %q", osUser, tenantRedisKeyPrefix(osUser))
		}
	}
	if tenantRedisACLUser("bob") != "t_bob" {
		t.Fatalf("unexpected ACL user %q", tenantRedisACLUser("bob"))
	}
}

// The allowlist must exclude every command that ignores the key-pattern fence
// (keyspace enumeration) or is administrative — a tenant must not be able to
// read other tenants' key names or touch server state.
func TestTenantRedisCommands_ExcludeDangerous(t *testing.T) {
	banned := []string{
		"KEYS", "SCAN", "RANDOMKEY", "DBSIZE", "FLUSHDB", "FLUSHALL", "SWAPDB",
		"CONFIG", "SHUTDOWN", "DEBUG", "SAVE", "BGSAVE", "BGREWRITEAOF",
		"CLIENT", "ACL", "CLUSTER", "SLAVEOF", "REPLICAOF", "MONITOR", "SLOWLOG",
		"EVAL", "EVALSHA", "FUNCTION", "FCALL", "SUBSCRIBE", "PUBLISH", "MOVE",
		"MIGRATE", "WAIT", "LASTSAVE", "LATENCY", "MEMORY", "FAILOVER",
	}
	have := map[string]bool{}
	for _, c := range tenantRedisCommands {
		have[strings.ToUpper(strings.TrimPrefix(c, "+"))] = true
	}
	for _, b := range banned {
		if have[b] {
			t.Errorf("allowlist must NOT contain the dangerous/enumeration command %q", b)
		}
	}
	// Sanity: the everyday caching commands ARE present.
	for _, want := range []string{"GET", "SET", "DEL", "EXPIRE", "HSET", "PING", "AUTH", "MULTI"} {
		if !have[want] {
			t.Errorf("allowlist is missing the expected command %q", want)
		}
	}
	// Per-key cursor scans are safe (single fenced key) and should be allowed…
	for _, want := range []string{"HSCAN", "SSCAN", "ZSCAN"} {
		if !have[want] {
			t.Errorf("per-key scan %q should be allowed", want)
		}
	}
	// …but the keyspace-level SCAN must not be.
	if have["SCAN"] {
		t.Error("keyspace-level SCAN must not be in the allowlist")
	}
}

// --- Handler behaviour --------------------------------------------------------

type redisAccessStubUsers struct {
	repository.UserRepository
	user *models.User
	err  error
}

func (s *redisAccessStubUsers) FindByID(_ context.Context, _ string) (*models.User, error) {
	return s.user, s.err
}

func newRedisAccessRouter(t *testing.T, cfg ApplicationHandlerConfig, claims *auth.AccessClaims) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		if claims != nil {
			ginctx.SetClaims(c, claims)
		}
		c.Next()
	})
	RegisterRedisAccessRoutes(v1, cfg)
	return r
}

func ptr(s string) *string { return &s }

func TestMeRedisAccess_Unauthorized(t *testing.T) {
	r := newRedisAccessRouter(t, ApplicationHandlerConfig{}, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/me/redis-access", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestMeRedisAccess_AdminHasNoLinuxUser(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	defer mr.Close()
	cfg := ApplicationHandlerConfig{
		Redis: rdb,
		Users: &redisAccessStubUsers{user: &models.User{ID: "admin-1", IsAdmin: true, Username: nil}},
	}
	r := newRedisAccessRouter(t, cfg, &auth.AccessClaims{UserID: "admin-1", IsAdmin: true})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/me/redis-access", nil))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no_linux_user") {
		t.Fatalf("want 409 no_linux_user, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestMeRedisAccess_RedisUnavailable(t *testing.T) {
	cfg := ApplicationHandlerConfig{
		Redis: nil,
		Users: &redisAccessStubUsers{user: &models.User{ID: "u1", Username: ptr("bob")}},
	}
	r := newRedisAccessRouter(t, cfg, &auth.AccessClaims{UserID: "u1"})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/me/redis-access", nil))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "redis_unavailable") {
		t.Fatalf("want 503 redis_unavailable, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestMeRedisAccess_HappyPath(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	defer mr.Close()

	// miniredis has no ACL SETUSER; capture the provisioning via the seam and
	// assert it was invoked with the resolved osuser + derived token.
	orig := tenantRedisProvision
	t.Cleanup(func() { tenantRedisProvision = orig })
	var gotOSUser, gotToken string
	tenantRedisProvision = func(_ *redisAccessHandler, _ context.Context, osUser, token string) error {
		gotOSUser, gotToken = osUser, token
		return nil
	}

	cfg := ApplicationHandlerConfig{
		Redis:            rdb,
		CacheTokenSecret: "test-secret",
		Users:            &redisAccessStubUsers{user: &models.User{ID: "u1", Username: ptr("bob")}},
	}
	r := newRedisAccessRouter(t, cfg, &auth.AccessClaims{UserID: "u1"})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/me/redis-access", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d %s", rec.Code, rec.Body.String())
	}
	var got redisAccessResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Username != "t_bob" {
		t.Errorf("username = %q, want t_bob", got.Username)
	}
	if got.KeyPrefix != "jt:bob:" {
		t.Errorf("key_prefix = %q, want jt:bob:", got.KeyPrefix)
	}
	if got.Socket != "/run/redis/redis.sock" {
		t.Errorf("socket = %q", got.Socket)
	}
	if got.Host != "" || got.Port != 0 {
		t.Errorf("expected no TCP (host empty, port 0), got host=%q port=%d", got.Host, got.Port)
	}
	want := tenantRedisToken("test-secret", "bob", "")
	if got.Password == "" || got.Password != want {
		t.Errorf("password not the derived token")
	}
	if gotOSUser != "bob" || gotToken != want {
		t.Errorf("provision seam got (%q,%q), want (bob, derived token)", gotOSUser, gotToken)
	}
}

// The generated ACL SETUSER rule must reset first, enable the user with the
// token, deny channels, fence to the tenant namespace, and grant only the
// curated commands.
func TestTenantRedisSetUserArgs(t *testing.T) {
	args := tenantRedisSetUserArgs("bob", "tok")
	if len(args) < 8 {
		t.Fatalf("too few args: %v", args)
	}
	if args[0] != "ACL" || args[1] != "SETUSER" || args[2] != "t_bob" {
		t.Fatalf("head = %v", args[:3])
	}
	joined := make([]string, len(args))
	for i, a := range args {
		joined[i] = a.(string)
	}
	s := strings.Join(joined, " ")
	for _, want := range []string{"reset", "on", ">tok", "resetchannels", "~jt:bob:*", "+GET", "+SET"} {
		if !strings.Contains(s, want) {
			t.Errorf("SETUSER args missing %q: %s", want, s)
		}
	}
	// The absolute `reset` must come before any grant.
	if idxReset, idxGet := strings.Index(s, "reset"), strings.Index(s, "+GET"); idxReset > idxGet {
		t.Error("reset must precede grants for an authoritative re-apply")
	}
}

func newMiniRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	return mr, redis.NewClient(&redis.Options{Addr: mr.Addr()})
}
