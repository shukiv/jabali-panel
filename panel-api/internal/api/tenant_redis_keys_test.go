package api

// GH #2165: the tenant key browser. A tenant can't SCAN (it ignores the ACL key
// fence), so the panel scans for the tenant's prefix and reads and deletes the
// keys with the tenant's own credential. miniredis has no ACLs, so the fence
// itself is drill-tested on a real Redis; these tests pin what the panel lists,
// in which databases, through which credential, and what it refuses.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// fenceKeyConn stands in for the tenant's Redis connection. Like Redis's ACL
// check, it refuses a key outside the tenant's prefix, and it records which
// credential opened it and which keys went through it.
type fenceKeyConn struct {
	inner  *redisTenantKeyConn
	prefix string
	call   *keyConnCall
	gone   map[string]bool // keys to report as expired since the scan
}

type keyConnCall struct {
	osUser, token string
	db            int
	described     []string
	unlinked      []string
}

func (f *fenceKeyConn) fence(keys []string) error {
	for _, k := range keys {
		if !strings.HasPrefix(k, f.prefix) {
			return fmt.Errorf("NOPERM key %q outside the fence", k)
		}
	}
	return nil
}

func (f *fenceKeyConn) Describe(ctx context.Context, keys []string) ([]tenantKeyInfo, error) {
	if err := f.fence(keys); err != nil {
		return nil, err
	}
	f.call.described = append(f.call.described, keys...)
	infos, err := f.inner.Describe(ctx, keys)
	for i, k := range keys {
		if err == nil && f.gone[k] {
			infos[i] = tenantKeyInfo{Type: "none", PTTL: -2}
		}
	}
	return infos, err
}

func (f *fenceKeyConn) Unlink(ctx context.Context, keys ...string) (int64, error) {
	if err := f.fence(keys); err != nil {
		return 0, err
	}
	f.call.unlinked = append(f.call.unlinked, keys...)
	return f.inner.Unlink(ctx, keys...)
}

func (f *fenceKeyConn) Close() error { return f.inner.Close() }

// stubKeySeams replaces provisioning and the tenant connection. The fake
// connection reads and deletes for real, in the database it was opened for.
func stubKeySeams(t *testing.T, mr *miniredis.Miniredis, gone ...string) *[]*keyConnCall {
	t.Helper()
	origProv, origConn := tenantRedisProvision, tenantRedisKeyConnFor
	t.Cleanup(func() { tenantRedisProvision, tenantRedisKeyConnFor = origProv, origConn })
	tenantRedisProvision = func(*redisAccessHandler, context.Context, string, string) error { return nil }
	goneSet := map[string]bool{}
	for _, k := range gone {
		goneSet[k] = true
	}
	calls := &[]*keyConnCall{}
	tenantRedisKeyConnFor = func(_ *redis.Client, osUser, token string, db int) tenantKeyConn {
		call := &keyConnCall{osUser: osUser, token: token, db: db}
		*calls = append(*calls, call)
		return &fenceKeyConn{
			inner:  &redisTenantKeyConn{c: redis.NewClient(&redis.Options{Addr: mr.Addr(), DB: db})},
			prefix: tenantRedisKeyPrefix(osUser),
			call:   call,
			gone:   goneSet,
		}
	}
	return calls
}

// seedTenantKeys puts bob's keys next to keys he must never see.
func seedTenantKeys(t *testing.T, mr *miniredis.Miniredis) {
	t.Helper()
	seed(t, mr, 0, "jt:bob:a", "jt:bobby:x", "jt:alice:y", "jabali:queue", "automation:z")
	if err := mr.DB(0).Set("jt:bob:b", "v"); err != nil {
		t.Fatal(err)
	}
	mr.DB(0).SetTTL("jt:bob:b", 90*time.Second)
	mr.DB(3).HSet("jt:bob:c", "f", "v")
	seed(t, mr, 3, "jc:bob:01INSTALL:posts:1")
	if _, err := mr.DB(15).Lpush("jt:bob:d", "v"); err != nil {
		t.Fatal(err)
	}
}

func bobKeysConfig(rdb *redis.Client) ApplicationHandlerConfig {
	return ApplicationHandlerConfig{
		Redis:            rdb,
		CacheTokenSecret: "test-secret",
		Users:            &redisAccessStubUsers{user: &models.User{ID: "u1", Username: ptr("bob")}},
	}
}

func getKeys(t *testing.T, r *gin.Engine, path string, q url.Values) (*httptest.ResponseRecorder, redisKeysResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	target := path
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	var got redisKeysResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
	}
	return rec, got
}

func postKeysDelete(r *gin.Engine, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	return rec
}

func rowNames(rows []redisKeyRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, fmt.Sprintf("%d/%s", r.DB, r.Key))
	}
	sort.Strings(out)
	return out
}

func TestMeRedisKeys_ListsOnlyOwnKeysInEveryDatabase(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	defer mr.Close()
	calls := stubKeySeams(t, mr)
	seedTenantKeys(t, mr)

	r := newRedisAccessRouter(t, bobKeysConfig(rdb), &auth.AccessClaims{UserID: "u1"})
	rec, got := getKeys(t, r, "/api/v1/me/redis-access/keys", url.Values{"limit": {"500"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d %s", rec.Code, rec.Body.String())
	}
	want := []string{"0/jt:bob:a", "0/jt:bob:b", "15/jt:bob:d", "3/jt:bob:c"}
	if g := rowNames(got.Data); strings.Join(g, ",") != strings.Join(want, ",") {
		t.Fatalf("listed %v, want %v", g, want)
	}
	if got.NextCursor != "" {
		t.Errorf("next_cursor = %q, want \"\" after every database", got.NextCursor)
	}

	byKey := map[string]redisKeyRow{}
	for _, row := range got.Data {
		byKey[row.Key] = row
	}
	if a := byKey["jt:bob:a"]; a.Name != "a" || a.Type != "string" || a.TTLMs != -1 || a.ExpiresAt != nil {
		t.Errorf("jt:bob:a = %+v, want name a, string, no expiry", a)
	}
	b := byKey["jt:bob:b"]
	if b.TTLMs <= 0 || b.TTLMs > 90_000 || b.ExpiresAt == nil {
		t.Errorf("jt:bob:b = %+v, want a TTL up to 90s and an expiry time", b)
	} else if d := time.Until(*b.ExpiresAt); d < 80*time.Second || d > 91*time.Second {
		t.Errorf("jt:bob:b expires in %s, want about 90s", d)
	}
	if byKey["jt:bob:c"].Type != "hash" || byKey["jt:bob:d"].Type != "list" {
		t.Errorf("types: c=%q d=%q, want hash and list", byKey["jt:bob:c"].Type, byKey["jt:bob:d"].Type)
	}

	// Every read went through bob's own credential.
	wantTok := tenantRedisToken("test-secret", "bob", "")
	for _, c := range *calls {
		if c.osUser != "bob" || c.token != wantTok {
			t.Errorf("tenant connection opened as (%q, token ok=%v), want bob + derived token", c.osUser, c.token == wantTok)
		}
		if len(c.unlinked) != 0 {
			t.Errorf("a listing deleted %v", c.unlinked)
		}
	}
}

// Paging with a small limit walks every database exactly once.
func TestMeRedisKeys_PagesThroughEveryDatabase(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	defer mr.Close()
	stubKeySeams(t, mr)
	seedTenantKeys(t, mr)
	r := newRedisAccessRouter(t, bobKeysConfig(rdb), &auth.AccessClaims{UserID: "u1"})

	var all []redisKeyRow
	cursor := ""
	for page := 0; ; page++ {
		if page > 40 {
			t.Fatal("listing never finished")
		}
		q := url.Values{"limit": {"1"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		rec, got := getKeys(t, r, "/api/v1/me/redis-access/keys", q)
		if rec.Code != http.StatusOK {
			t.Fatalf("page %d: %d %s", page, rec.Code, rec.Body.String())
		}
		all = append(all, got.Data...)
		if got.NextCursor == "" {
			break
		}
		cursor = got.NextCursor
	}
	want := []string{"0/jt:bob:a", "0/jt:bob:b", "15/jt:bob:d", "3/jt:bob:c"}
	if g := rowNames(all); strings.Join(g, ",") != strings.Join(want, ",") {
		t.Fatalf("paged %v, want each of %v once", g, want)
	}
}

func TestMeRedisKeys_MatchAndDatabaseFilter(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	defer mr.Close()
	stubKeySeams(t, mr)
	seedTenantKeys(t, mr)
	r := newRedisAccessRouter(t, bobKeysConfig(rdb), &auth.AccessClaims{UserID: "u1"})

	_, got := getKeys(t, r, "/api/v1/me/redis-access/keys", url.Values{"match": {"[ab]"}})
	if g := rowNames(got.Data); strings.Join(g, ",") != "0/jt:bob:a,0/jt:bob:b" {
		t.Errorf("match [ab] listed %v", g)
	}
	// The match is relative to the prefix: "*" never reaches past it.
	_, got = getKeys(t, r, "/api/v1/me/redis-access/keys", url.Values{"match": {"*y"}})
	if len(got.Data) != 0 {
		t.Errorf("match *y listed %v; jt:alice:y and jt:bobby:x are not bob's", rowNames(got.Data))
	}
	_, got = getKeys(t, r, "/api/v1/me/redis-access/keys", url.Values{"db": {"3"}})
	if g := rowNames(got.Data); strings.Join(g, ",") != "3/jt:bob:c" || got.NextCursor != "" {
		t.Errorf("db 3 listed %v next=%q", g, got.NextCursor)
	}
}

// A key that expired between the scan and the read is left out, not listed
// as a type-"none" row. A name that isn't UTF-8 is marked binary.
func TestMeRedisKeys_DropsVanishedKeysAndMarksBinaryNames(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	defer mr.Close()
	stubKeySeams(t, mr, "jt:bob:a")
	seed(t, mr, 0, "jt:bob:a", "jt:bob:\xff\xfe")
	r := newRedisAccessRouter(t, bobKeysConfig(rdb), &auth.AccessClaims{UserID: "u1"})

	_, got := getKeys(t, r, "/api/v1/me/redis-access/keys", nil)
	if len(got.Data) != 1 || !got.Data[0].Binary {
		t.Fatalf("got %+v, want only the binary-named key, marked binary", got.Data)
	}
}

func TestMeRedisKeys_QueryValidation(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	defer mr.Close()
	stubKeySeams(t, mr)
	r := newRedisAccessRouter(t, bobKeysConfig(rdb), &auth.AccessClaims{UserID: "u1"})

	for _, tc := range []struct {
		q    url.Values
		code string
	}{
		{url.Values{"match": {strings.Repeat("a", tenantRedisKeysMaxMatch+1)}}, "invalid_match"},
		{url.Values{"match": {"a\nb"}}, "invalid_match"},
		{url.Values{"match": {"a\x7fb"}}, "invalid_match"},
		{url.Values{"limit": {"0"}}, "invalid_limit"},
		{url.Values{"limit": {"501"}}, "invalid_limit"},
		{url.Values{"limit": {"ten"}}, "invalid_limit"},
		{url.Values{"db": {"16"}}, "invalid_db"},
		{url.Values{"db": {"-1"}}, "invalid_db"},
		{url.Values{"cursor": {"12"}}, "invalid_cursor"},
		{url.Values{"cursor": {"16:0"}}, "invalid_cursor"},
		{url.Values{"cursor": {"1:x"}}, "invalid_cursor"},
		{url.Values{"cursor": {"2:0"}, "db": {"3"}}, "invalid_cursor"},
	} {
		rec, _ := getKeys(t, r, "/api/v1/me/redis-access/keys", tc.q)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"`+tc.code+`"`) {
			t.Errorf("%v: want 422 %s, got %d %s", tc.q, tc.code, rec.Code, rec.Body.String())
		}
	}
	rec, _ := getKeys(t, r, "/api/v1/me/redis-access/keys", url.Values{"match": {strings.Repeat("a", tenantRedisKeysMaxMatch)}})
	if rec.Code != http.StatusOK {
		t.Errorf("a %d-byte match: want 200, got %d", tenantRedisKeysMaxMatch, rec.Code)
	}
}

// One call stops scanning once it has its keys and hands back where to carry
// on; a call out of time does the same, without an error. (miniredis answers a
// whole database in one SCAN, so here the stop falls between databases; the
// cursor inside a database is drill-tested on a real Redis.)
func TestTenantRedisListKeys_StopsAndResumes(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	defer mr.Close()
	stubKeySeams(t, mr)
	seed(t, mr, 0, "jt:bob:a", "other:1")
	seed(t, mr, 1, "jt:bob:b", "other:2")

	rows, next, err := tenantRedisListKeys(context.Background(), rdb, "bob", "tok", redisKeysQuery{match: "*", limit: 1})
	if err != nil || strings.Join(rowNames(rows), ",") != "0/jt:bob:a" || next != "1:0" {
		t.Fatalf("got %v next=%q err=%v, want db 0's key and 1:0", rowNames(rows), next, err)
	}
	rows, next, err = tenantRedisListKeys(context.Background(), rdb, "bob", "tok", redisKeysQuery{match: "*", limit: 1, db: 1})
	if err != nil || strings.Join(rowNames(rows), ",") != "1/jt:bob:b" || next != "2:0" {
		t.Fatalf("resumed: got %v next=%q err=%v, want db 1's key and 2:0", rowNames(rows), next, err)
	}
	rows, next, err = tenantRedisListKeys(context.Background(), rdb, "bob", "tok", redisKeysQuery{match: "*", limit: 1, db: 2})
	if err != nil || len(rows) != 0 || next != "" {
		t.Fatalf("last page: got %v next=%q err=%v, want nothing more and the end", rowNames(rows), next, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rows, next, err = tenantRedisListKeys(ctx, rdb, "bob", "tok", redisKeysQuery{match: "*", limit: 100, db: 4, cursor: 0})
	if err != nil || len(rows) != 0 || next != "4:0" {
		t.Fatalf("out of time: got %d rows next=%q err=%v, want none, 4:0, nil", len(rows), next, err)
	}
}

// The real tenant connection authenticates as t_<osuser> with the tenant's
// token. A wrong token fails the listing instead of falling back to the panel.
func TestTenantRedisListKeys_ReadsThroughTenantCredential(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	const token = "bob-token"
	mr.RequireAuth("panel-pw")
	mr.RequireUserAuth("t_bob", token)
	panel := redis.NewClient(&redis.Options{Addr: mr.Addr(), Password: "panel-pw"})
	defer panel.Close()
	seed(t, mr, 2, "jt:bob:a", "jt:alice:a")

	rows, next, err := tenantRedisListKeys(context.Background(), panel, "bob", token, redisKeysQuery{match: "*", limit: 100})
	if err != nil || next != "" || len(rows) != 1 || rows[0].Key != "jt:bob:a" || rows[0].DB != 2 {
		t.Fatalf("got %+v next=%q err=%v, want jt:bob:a in db 2", rows, next, err)
	}
	if _, _, err := tenantRedisListKeys(context.Background(), panel, "bob", "wrong", redisKeysQuery{match: "*", limit: 100}); err == nil {
		t.Fatal("listing with a wrong tenant token succeeded")
	}
}

// The listing SELECTs other databases; the panel's own pooled connection must
// stay on its database.
func TestTenantRedisListKeys_PanelPoolStaysOnItsDatabase(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	stubKeySeams(t, mr)
	panel := redis.NewClient(&redis.Options{Addr: mr.Addr(), PoolSize: 1})
	defer panel.Close()
	seed(t, mr, 0, "jabali:panel-key")
	seed(t, mr, 15, "jt:bob:a")

	if _, _, err := tenantRedisListKeys(context.Background(), panel, "bob", "tok", redisKeysQuery{match: "*", limit: 100}); err != nil {
		t.Fatal(err)
	}
	if v, err := panel.Get(context.Background(), "jabali:panel-key").Result(); err != nil || v != "v" {
		t.Fatalf("panel client lost its database: %q %v", v, err)
	}
}

func TestMeRedisKeysDelete_DeletesOnlyTheListedOwnKeys(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	defer mr.Close()
	calls := stubKeySeams(t, mr)
	seedTenantKeys(t, mr)
	r := newRedisAccessRouter(t, bobKeysConfig(rdb), &auth.AccessClaims{UserID: "u1"})

	rec := postKeysDelete(r, "/api/v1/me/redis-access/keys/delete",
		`{"keys":[{"db":0,"key":"jt:bob:a"},{"db":3,"key":"jt:bob:c"},{"db":0,"key":"jt:bob:missing"}]}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"deleted":2`) {
		t.Fatalf("want 200 deleted 2, got %d %s", rec.Code, rec.Body.String())
	}
	if mr.DB(0).Exists("jt:bob:a") || mr.DB(3).Exists("jt:bob:c") {
		t.Error("listed keys survived")
	}
	for db, k := range map[int]string{0: "jt:bob:b", 15: "jt:bob:d"} {
		if !mr.DB(db).Exists(k) {
			t.Errorf("db %d: %s was not listed but was deleted", db, k)
		}
	}
	wantTok := tenantRedisToken("test-secret", "bob", "")
	for _, c := range *calls {
		if c.osUser != "bob" || c.token != wantTok {
			t.Errorf("delete went through (%q, token ok=%v), want bob's credential", c.osUser, c.token == wantTok)
		}
	}
}

func TestMeRedisKeysDelete_Refusals(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	defer mr.Close()
	calls := stubKeySeams(t, mr)
	seedTenantKeys(t, mr)
	r := newRedisAccessRouter(t, bobKeysConfig(rdb), &auth.AccessClaims{UserID: "u1"})

	many := make([]string, tenantRedisKeysMaxDelete+1)
	for i := range many {
		many[i] = fmt.Sprintf(`{"db":0,"key":"jt:bob:k%d"}`, i)
	}
	for _, tc := range []struct {
		name, body string
		status     int
		code       string
	}{
		{"another tenant's key, with an own key", `{"keys":[{"db":0,"key":"jt:bob:a"},{"db":0,"key":"jt:alice:y"}]}`, 422, "key_outside_prefix"},
		{"a look-alike prefix", `{"keys":[{"db":0,"key":"jt:bobby:x"}]}`, 422, "key_outside_prefix"},
		{"the panel's key", `{"keys":[{"db":0,"key":"jabali:queue"}]}`, 422, "key_outside_prefix"},
		{"the bare prefix", `{"keys":[{"db":0,"key":"jt:bob:"}]}`, 422, "key_outside_prefix"},
		{"database out of range", `{"keys":[{"db":16,"key":"jt:bob:a"}]}`, 422, "invalid_db"},
		{"no keys", `{"keys":[]}`, 422, "invalid_keys"},
		{"too many keys", `{"keys":[` + strings.Join(many, ",") + `]}`, 422, "invalid_keys"},
		{"malformed JSON", `{"keys":`, 400, "invalid_request"},
	} {
		rec := postKeysDelete(r, "/api/v1/me/redis-access/keys/delete", tc.body)
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), `"`+tc.code+`"`) {
			t.Errorf("%s: want %d %s, got %d %s", tc.name, tc.status, tc.code, rec.Code, rec.Body.String())
		}
	}
	for _, c := range *calls {
		if len(c.unlinked) != 0 {
			t.Errorf("a refused delete unlinked %v", c.unlinked)
		}
	}
	if !mr.DB(0).Exists("jt:bob:a") || !mr.DB(0).Exists("jt:alice:y") {
		t.Error("a refused delete removed keys")
	}
}

func TestRedisKeys_AdminRoutesAndRefusals(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	defer mr.Close()
	stubKeySeams(t, mr)
	seedTenantKeys(t, mr)

	admin := newRedisAccessRouter(t, bobKeysConfig(rdb), &auth.AccessClaims{UserID: "a1", IsAdmin: true})
	rec, got := getKeys(t, admin, "/api/v1/users/u1/redis-access/keys", nil)
	if rec.Code != http.StatusOK || len(got.Data) != 4 {
		t.Fatalf("admin listing: %d, %d keys, want 200 and bob's 4", rec.Code, len(got.Data))
	}
	if rec := postKeysDelete(admin, "/api/v1/users/u1/redis-access/keys/delete", `{"keys":[{"db":0,"key":"jt:bob:a"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("admin delete: %d %s", rec.Code, rec.Body.String())
	}

	tenant := newRedisAccessRouter(t, bobKeysConfig(rdb), &auth.AccessClaims{UserID: "u2"})
	if rec, _ := getKeys(t, tenant, "/api/v1/users/u1/redis-access/keys", nil); rec.Code != http.StatusForbidden {
		t.Errorf("non-admin on the admin listing: want 403, got %d", rec.Code)
	}
	if rec := postKeysDelete(tenant, "/api/v1/users/u1/redis-access/keys/delete", `{"keys":[{"db":0,"key":"jt:bob:b"}]}`); rec.Code != http.StatusForbidden {
		t.Errorf("non-admin on the admin delete: want 403, got %d", rec.Code)
	}

	noSession := newRedisAccessRouter(t, bobKeysConfig(rdb), nil)
	if rec, _ := getKeys(t, noSession, "/api/v1/me/redis-access/keys", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("no session: want 401, got %d", rec.Code)
	}

	adminSelf := ApplicationHandlerConfig{Redis: rdb, Users: &redisAccessStubUsers{user: &models.User{ID: "a1", IsAdmin: true, Username: ptr("admin")}}}
	r := newRedisAccessRouter(t, adminSelf, &auth.AccessClaims{UserID: "a1", IsAdmin: true})
	if rec, _ := getKeys(t, r, "/api/v1/me/redis-access/keys", nil); rec.Code != http.StatusConflict {
		t.Errorf("admin's own listing: want 409 no_linux_user, got %d", rec.Code)
	}
}

// Both listing routes sit behind the scan limit; the delete doesn't scan, so it
// doesn't.
func TestRedisKeys_RateLimited(t *testing.T) {
	limited := func(c *gin.Context) { c.AbortWithStatus(http.StatusTooManyRequests) }
	r := newRedisAccessRouter(t, ApplicationHandlerConfig{RedisKeysRateLimit: limited},
		&auth.AccessClaims{UserID: "a1", IsAdmin: true})
	for _, p := range []string{"/api/v1/me/redis-access/keys", "/api/v1/users/u1/redis-access/keys"} {
		if rec, _ := getKeys(t, r, p, nil); rec.Code != http.StatusTooManyRequests {
			t.Errorf("GET %s: want 429, got %d", p, rec.Code)
		}
	}
	if rec := postKeysDelete(r, "/api/v1/me/redis-access/keys/delete", `{"keys":[]}`); rec.Code == http.StatusTooManyRequests {
		t.Error("the delete must not be behind the scan limit")
	}
}

// read:redis lists keys and nothing else: not the credential, not a delete.
// write:redis deletes and flushes but can't list.
func TestRedisKeysScope(t *testing.T) {
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
		v1.GET("/me/redis-access/keys", ok)
		v1.POST("/me/redis-access/keys/delete", ok)
		return r
	}
	readTok := &models.UserAPIToken{Scopes: models.UserAPIScopes{ScopeRedisRead}}
	writeTok := &models.UserAPIToken{Scopes: models.UserAPIScopes{ScopeRedisFlush}}
	readAll := &models.UserAPIToken{Scopes: models.UserAPIScopes{"read:*"}}

	cases := []struct {
		name   string
		tok    *models.UserAPIToken
		method string
		path   string
		want   int
	}{
		{"read:redis lists keys", readTok, "GET", "/api/v1/me/redis-access/keys", http.StatusOK},
		{"read:redis can't read the credential", readTok, "GET", "/api/v1/me/redis-access", http.StatusForbidden},
		{"read:redis can't delete keys", readTok, "POST", "/api/v1/me/redis-access/keys/delete", http.StatusForbidden},
		{"read:redis can't flush", readTok, "POST", "/api/v1/me/redis-access/flush", http.StatusForbidden},
		{"write:redis deletes keys", writeTok, "POST", "/api/v1/me/redis-access/keys/delete", http.StatusOK},
		{"write:redis can't list keys", writeTok, "GET", "/api/v1/me/redis-access/keys", http.StatusForbidden},
		{"read:* can't read the credential", readAll, "GET", "/api/v1/me/redis-access", http.StatusForbidden},
	}
	for _, tc := range cases {
		if got := reqStatus(route(tc.tok), tc.method, tc.path); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
	if bad, ok := ValidateUserScopes(models.UserAPIScopes{ScopeRedisRead, ScopeRedisFlush}); !ok {
		t.Errorf("redis scopes rejected at mint (%q)", bad)
	}
}
