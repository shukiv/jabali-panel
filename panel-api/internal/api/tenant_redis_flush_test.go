package api

// GH #2003: the tenant-scoped Redis flush. A tenant can't run FLUSHALL, so the
// panel SCANs for the tenant's prefix and deletes the keys with the tenant's own
// credential. miniredis has no ACLs, so the fence itself is drill-tested on a
// real Redis; these tests pin what the panel scans, what it deletes, in which
// databases, and through which credential.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// fenceUnlinker stands in for the tenant's Redis connection. Like Redis's ACL
// check, it refuses a key outside the tenant's prefix. It records every call so
// a test can see the deletes went through the tenant's credential.
type fenceUnlinker struct {
	rdb    *redis.Client
	prefix string
	calls  *[]unlinkCall
	db     int
}

type unlinkCall struct {
	osUser, token string
	db            int
	keys          []string
}

func (f *fenceUnlinker) Unlink(ctx context.Context, keys ...string) *redis.IntCmd {
	for _, k := range keys {
		if !strings.HasPrefix(k, f.prefix) {
			cmd := redis.NewIntCmd(ctx)
			cmd.SetErr(fmt.Errorf("NOPERM key %q outside the fence", k))
			return cmd
		}
	}
	(*f.calls)[len(*f.calls)-1].keys = append((*f.calls)[len(*f.calls)-1].keys, keys...)
	return f.rdb.Unlink(ctx, keys...)
}

func (f *fenceUnlinker) Close() error { return f.rdb.Close() }

// stubFlushSeams replaces the provisioning and tenant-connection seams. The
// fake connection deletes for real, in the database it was opened for.
func stubFlushSeams(t *testing.T, mr *miniredis.Miniredis) *[]unlinkCall {
	t.Helper()
	origProv, origUnl := tenantRedisProvision, tenantRedisUnlinker
	t.Cleanup(func() { tenantRedisProvision, tenantRedisUnlinker = origProv, origUnl })
	tenantRedisProvision = func(*redisAccessHandler, context.Context, string, string) error { return nil }
	calls := &[]unlinkCall{}
	tenantRedisUnlinker = func(_ *redis.Client, osUser, token string, db int) redisUnlinker {
		*calls = append(*calls, unlinkCall{osUser: osUser, token: token, db: db})
		return &fenceUnlinker{
			rdb:    redis.NewClient(&redis.Options{Addr: mr.Addr(), DB: db}),
			prefix: tenantRedisKeyPrefix(osUser),
			calls:  calls,
			db:     db,
		}
	}
	return calls
}

func seed(t *testing.T, mr *miniredis.Miniredis, db int, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if err := mr.DB(db).Set(k, "v"); err != nil {
			t.Fatal(err)
		}
	}
}

func postFlush(r *gin.Engine, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
	return rec
}

func TestMeRedisFlush_DeletesOnlyOwnKeysInEveryDatabase(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	defer mr.Close()
	calls := stubFlushSeams(t, mr)

	seed(t, mr, 0, "jt:bob:a", "jt:bob:b", "jt:bobby:x", "jt:alice:y", "jabali:queue", "automation:z")
	seed(t, mr, 3, "jt:bob:c", "jc:bob:01INSTALL:posts:1")
	seed(t, mr, 15, "jt:bob:d")

	cfg := ApplicationHandlerConfig{
		Redis:            rdb,
		CacheTokenSecret: "test-secret",
		Users:            &redisAccessStubUsers{user: &models.User{ID: "u1", Username: ptr("bob")}},
	}
	r := newRedisAccessRouter(t, cfg, &auth.AccessClaims{UserID: "u1"})
	rec := postFlush(r, "/api/v1/me/redis-access/flush")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d %s", rec.Code, rec.Body.String())
	}
	var got redisFlushResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Deleted != 4 || !got.Complete {
		t.Fatalf("got %+v, want deleted=4 complete=true", got)
	}

	for db, k := range map[int]string{0: "jt:bob:a", 3: "jt:bob:c", 15: "jt:bob:d"} {
		if mr.DB(db).Exists(k) {
			t.Errorf("db %d: %s survived the flush", db, k)
		}
	}
	for db, keys := range map[int][]string{
		0: {"jt:bobby:x", "jt:alice:y", "jabali:queue", "automation:z"},
		3: {"jc:bob:01INSTALL:posts:1"},
	} {
		for _, k := range keys {
			if !mr.DB(db).Exists(k) {
				t.Errorf("db %d: %s is not the tenant's key but was deleted", db, k)
			}
		}
	}

	// Every delete went through bob's own credential, in the right database.
	want := tenantRedisToken("test-secret", "bob", "")
	var deletedVia int
	for _, c := range *calls {
		if c.osUser != "bob" || c.token != want {
			t.Errorf("tenant connection opened as (%q, token ok=%v), want bob + derived token", c.osUser, c.token == want)
		}
		deletedVia += len(c.keys)
	}
	if deletedVia != 4 {
		t.Errorf("%d keys deleted through the tenant connection, want 4", deletedVia)
	}
	if len(*calls) != tenantRedisDatabases {
		t.Errorf("opened %d tenant connections, want one per database (%d)", len(*calls), tenantRedisDatabases)
	}
}

// The flush must make sure the tenant's ACL user exists (and has the current
// password) before deleting through it.
func TestMeRedisFlush_ProvisionsFirst(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	defer mr.Close()
	stubFlushSeams(t, mr)
	var provisioned string
	tenantRedisProvision = func(_ *redisAccessHandler, _ context.Context, osUser, _ string) error {
		provisioned = osUser
		return nil
	}
	cfg := ApplicationHandlerConfig{
		Redis: rdb,
		Users: &redisAccessStubUsers{user: &models.User{ID: "u1", Username: ptr("bob")}},
	}
	r := newRedisAccessRouter(t, cfg, &auth.AccessClaims{UserID: "u1"})
	if rec := postFlush(r, "/api/v1/me/redis-access/flush"); rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d %s", rec.Code, rec.Body.String())
	}
	if provisioned != "bob" {
		t.Fatalf("ACL not provisioned for bob before the flush (got %q)", provisioned)
	}
}

func TestMeRedisFlush_Refusals(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	defer mr.Close()
	stubFlushSeams(t, mr)

	t.Run("no session", func(t *testing.T) {
		r := newRedisAccessRouter(t, ApplicationHandlerConfig{Redis: rdb}, nil)
		if rec := postFlush(r, "/api/v1/me/redis-access/flush"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("want 401, got %d", rec.Code)
		}
	})
	t.Run("admin has no Linux user", func(t *testing.T) {
		cfg := ApplicationHandlerConfig{
			Redis: rdb,
			Users: &redisAccessStubUsers{user: &models.User{ID: "a1", IsAdmin: true}},
		}
		r := newRedisAccessRouter(t, cfg, &auth.AccessClaims{UserID: "a1", IsAdmin: true})
		rec := postFlush(r, "/api/v1/me/redis-access/flush")
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no_linux_user") {
			t.Fatalf("want 409 no_linux_user, got %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("redis not configured", func(t *testing.T) {
		cfg := ApplicationHandlerConfig{
			Users: &redisAccessStubUsers{user: &models.User{ID: "u1", Username: ptr("bob")}},
		}
		r := newRedisAccessRouter(t, cfg, &auth.AccessClaims{UserID: "u1"})
		if rec := postFlush(r, "/api/v1/me/redis-access/flush"); rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("want 503, got %d", rec.Code)
		}
	})
	t.Run("admin route needs an admin", func(t *testing.T) {
		cfg := ApplicationHandlerConfig{
			Redis: rdb,
			Users: &redisAccessStubUsers{user: &models.User{ID: "u1", Username: ptr("bob")}},
		}
		r := newRedisAccessRouter(t, cfg, &auth.AccessClaims{UserID: "u2"})
		if rec := postFlush(r, "/api/v1/users/u1/redis-access/flush"); rec.Code != http.StatusForbidden {
			t.Fatalf("want 403 for a non-admin, got %d", rec.Code)
		}
	})
}

func TestUserRedisFlush_AdminFlushesThatTenant(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	defer mr.Close()
	stubFlushSeams(t, mr)
	seed(t, mr, 0, "jt:bob:a", "jt:alice:a")
	cfg := ApplicationHandlerConfig{
		Redis: rdb,
		Users: &redisAccessStubUsers{user: &models.User{ID: "u1", Username: ptr("bob")}},
	}
	r := newRedisAccessRouter(t, cfg, &auth.AccessClaims{UserID: "a1", IsAdmin: true})
	if rec := postFlush(r, "/api/v1/users/u1/redis-access/flush"); rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d %s", rec.Code, rec.Body.String())
	}
	if mr.Exists("jt:bob:a") || !mr.Exists("jt:alice:a") {
		t.Fatal("admin flush must delete only bob's keys")
	}
}

// Both flush routes sit behind the flush rate limit; the credential GET doesn't.
func TestRedisFlush_RateLimited(t *testing.T) {
	limited := func(c *gin.Context) { c.AbortWithStatus(http.StatusTooManyRequests) }
	r := newRedisAccessRouter(t, ApplicationHandlerConfig{RedisFlushRateLimit: limited},
		&auth.AccessClaims{UserID: "a1", IsAdmin: true})
	for _, p := range []string{"/api/v1/me/redis-access/flush", "/api/v1/users/u1/redis-access/flush"} {
		if rec := postFlush(r, p); rec.Code != http.StatusTooManyRequests {
			t.Errorf("POST %s: want 429, got %d", p, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/me/redis-access", nil))
	if rec.Code == http.StatusTooManyRequests {
		t.Error("GET /me/redis-access must not be behind the flush limit")
	}
}

// The real tenant connection authenticates as t_<osuser> with the tenant's
// token. miniredis checks the credential (not the key fence).
func TestTenantRedisFlush_DeletesThroughTenantCredential(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	const token = "bob-token"
	mr.RequireAuth("panel-pw")
	mr.RequireUserAuth("t_bob", token)
	panel := redis.NewClient(&redis.Options{Addr: mr.Addr(), Password: "panel-pw"})
	seed(t, mr, 2, "jt:bob:a", "jt:alice:a")

	deleted, complete, err := tenantRedisFlushKeys(context.Background(), panel, "bob", token)
	if err != nil || deleted != 1 || !complete {
		t.Fatalf("got deleted=%d complete=%v err=%v, want 1/true/nil", deleted, complete, err)
	}
	if mr.DB(2).Exists("jt:bob:a") || !mr.DB(2).Exists("jt:alice:a") {
		t.Fatal("wrong keys deleted")
	}

	// A wrong tenant token must fail the flush, not fall back to the panel.
	seed(t, mr, 2, "jt:bob:b")
	if _, _, err := tenantRedisFlushKeys(context.Background(), panel, "bob", "wrong"); err == nil {
		t.Fatal("flush with a wrong tenant token succeeded")
	}
	if !mr.DB(2).Exists("jt:bob:b") {
		t.Fatal("key deleted although the tenant credential was wrong")
	}
}

// A flush that runs out of time reports complete=false, without an error, so
// the caller knows to call again.
func TestTenantRedisFlush_BudgetSpent(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	defer mr.Close()
	stubFlushSeams(t, mr)
	seed(t, mr, 0, "jt:bob:a")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, complete, err := tenantRedisFlushKeys(ctx, rdb, "bob", "tok")
	if err != nil || complete {
		t.Fatalf("got complete=%v err=%v, want false/nil", complete, err)
	}
}

func TestTenantRedisClientOptions(t *testing.T) {
	base := &redis.Options{Network: "unix", Addr: "/run/redis/redis.sock", Username: "jabali_panel", Password: "panel-secret", DB: 0}
	o := tenantRedisClientOptions(base, "bob", "tok", 7)
	if o.Network != "unix" || o.Addr != "/run/redis/redis.sock" {
		t.Errorf("connects to %s %s, want the panel's socket", o.Network, o.Addr)
	}
	if o.Username != "t_bob" || o.Password != "tok" || o.DB != 7 {
		t.Errorf("got user=%q pw-ok=%v db=%d, want t_bob/tok/7", o.Username, o.Password == "tok", o.DB)
	}
}

func TestRedisGlobEscape(t *testing.T) {
	if got := redisGlobEscape(`jt:a*b?[c]\d:`); got != `jt:a\*b\?\[c\]\\d:` {
		t.Fatalf("escaped = %q", got)
	}
	if got := redisGlobEscape("jt:bob:"); got != "jt:bob:" {
		t.Fatalf("plain prefix changed: %q", got)
	}
}

// A write:redis token can flush and do nothing else. In particular it can't
// fetch the Redis password from GET /me/redis-access (unmapped, so a scoped
// token is refused there). read:redis doesn't exist.
func TestRedisFlushScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	route := func(tok *models.UserAPIToken) *gin.Engine {
		r := gin.New()
		v1 := r.Group("/api/v1", func(c *gin.Context) {
			if tok != nil {
				c.Set(userTokenCtxKey, tok)
			}
			c.Next()
		}, EnforceUserTokenScopes())
		ok := func(c *gin.Context) { c.Status(http.StatusOK) }
		v1.GET("/me/redis-access", ok)
		v1.POST("/me/redis-access/flush", ok)
		v1.POST("/domains/:id/dns/records", ok)
		return r
	}
	flushTok := &models.UserAPIToken{Scopes: models.UserAPIScopes{ScopeRedisFlush}}
	dnsTok := &models.UserAPIToken{Scopes: models.UserAPIScopes{ScopeWriteDNS}}
	fullTok := &models.UserAPIToken{Scopes: models.UserAPIScopes{}}

	cases := []struct {
		name   string
		tok    *models.UserAPIToken
		method string
		path   string
		want   int
	}{
		{"write:redis flushes", flushTok, "POST", "/api/v1/me/redis-access/flush", http.StatusOK},
		{"write:redis can't read the credential", flushTok, "GET", "/api/v1/me/redis-access", http.StatusForbidden},
		{"write:redis can't write DNS", flushTok, "POST", "/api/v1/domains/d1/dns/records", http.StatusForbidden},
		{"write:dns can't flush", dnsTok, "POST", "/api/v1/me/redis-access/flush", http.StatusForbidden},
		{"full token flushes", fullTok, "POST", "/api/v1/me/redis-access/flush", http.StatusOK},
	}
	for _, tc := range cases {
		if got := reqStatus(route(tc.tok), tc.method, tc.path); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}

	if bad, ok := ValidateUserScopes(models.UserAPIScopes{ScopeRedisFlush}); !ok {
		t.Errorf("write:redis rejected at mint (%q)", bad)
	}
	if _, ok := ValidateUserScopes(models.UserAPIScopes{"read:redis"}); ok {
		t.Error("read:redis must not be a known scope")
	}
}
