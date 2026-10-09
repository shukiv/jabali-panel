package api

// ADR-0173: the panel's WP-cache cleanup. miniredis has no ACLs, so the
// install's own Redis user is a fake that enforces the user's key fence and
// password the way Redis would; the fence itself is drill-tested on a real
// Redis. These tests pin what the cleanup deletes, what it keeps, and through
// which credential.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

const (
	gcSecret = "s3cret"
	gcA      = "01AAAAAAAAAAAAAAAAAAAAAAAA" // cache on, 1.2.0
	gcB      = "01BBBBBBBBBBBBBBBBBBBBBBBB" // cache on, still 1.1.0 (no gen:o)
	gcC      = "01CCCCCCCCCCCCCCCCCCCCCCCC" // cache off
	gcD      = "01DDDDDDDDDDDDDDDDDDDDDDDD" // still installing
	gcF      = "01FFFFFFFFFFFFFFFFFFFFFFFF" // cache on, 1 MB budget
	gcGone   = "01GGGGGGGGGGGGGGGGGGGGGGGG" // no install row
)

func gcPrefix(id string) string { return "jc:bob:" + id + ":" }

type gcInstalls struct {
	repository.ApplicationInstallRepository
	rows  []models.ApplicationInstall
	extra int64 // added to total: a list that didn't return every row
}

func (r *gcInstalls) List(context.Context, repository.ListOptions) ([]models.ApplicationInstall, int64, error) {
	return r.rows, int64(len(r.rows)) + r.extra, nil
}

type gcUsers struct{ repository.UserRepository }

func (gcUsers) FindByID(_ context.Context, id string) (*models.User, error) {
	if id == "u1" {
		return &models.User{ID: "u1", Username: ptr("bob")}, nil
	}
	return nil, repository.ErrNotFound
}

type gcSalts struct {
	repository.CacheTokenSaltRepository
}

func (gcSalts) GetOrCreate(_ context.Context, userID string) (string, error) {
	return "salt-" + userID, nil
}

func gcInstall(id string, cache bool, status string, settings string) models.ApplicationInstall {
	in := models.ApplicationInstall{ID: id, UserID: "u1", AppType: "wordpress", CacheEnabled: cache, Status: status}
	if settings != "" {
		in.CacheSettings = json.RawMessage(settings)
	}
	return in
}

func gcToken(id string) string { return cacheInstallToken(gcSecret, "bob", id, "salt-u1") }

// gcRedis is the fake ACL side of Redis: which users exist with which
// password, and every ACL command and principal call the cleanup made.
type gcRedis struct {
	mr     *miniredis.Miniredis
	users  map[string]string // ACL user -> password Redis accepts
	acl    []string          // ACL commands, e.g. "DELUSER wp_bob_x"
	unlink []gcUnlink
}

type gcUnlink struct {
	user, password string
	keys           []string
}

type gcPrincipal struct {
	g              *gcRedis
	rdb            *redis.Client
	user, password string
}

func (p *gcPrincipal) check(keys ...string) error {
	if pw, ok := p.g.users[p.user]; !ok || pw != p.password {
		return errors.New("WRONGPASS invalid username-password pair")
	}
	m := installACLUserRe.FindStringSubmatch(p.user)
	if m == nil {
		return errors.New("NOPERM not a per-install user")
	}
	fence := "jc:" + m[1] + ":" + m[2] + ":"
	for _, k := range keys {
		if !strings.HasPrefix(k, fence) {
			return fmt.Errorf("NOPERM key %q outside the fence", k)
		}
	}
	return nil
}

func (p *gcPrincipal) Get(ctx context.Context, key string) *redis.StringCmd {
	if err := p.check(key); err != nil {
		cmd := redis.NewStringCmd(ctx)
		cmd.SetErr(err)
		return cmd
	}
	return p.rdb.Get(ctx, key)
}

func (p *gcPrincipal) Unlink(ctx context.Context, keys ...string) *redis.IntCmd {
	if err := p.check(keys...); err != nil {
		cmd := redis.NewIntCmd(ctx)
		cmd.SetErr(err)
		return cmd
	}
	p.g.unlink = append(p.g.unlink, gcUnlink{user: p.user, password: p.password, keys: append([]string(nil), keys...)})
	return p.rdb.Unlink(ctx, keys...)
}

func (p *gcPrincipal) Close() error { return p.rdb.Close() }

func stubGCRedis(t *testing.T, mr *miniredis.Miniredis, users map[string]string) *gcRedis {
	t.Helper()
	g := &gcRedis{mr: mr, users: users}
	origConn, origUsers, origACL := wpCachePrincipalConn, wpCacheACLUsers, wpCacheACL
	t.Cleanup(func() {
		wpCachePrincipalConn, wpCacheACLUsers, wpCacheACL = origConn, origUsers, origACL
		wpCacheStats.Range(func(k, _ any) bool { wpCacheStats.Delete(k); return true })
	})
	wpCachePrincipalConn = func(_ *redis.Client, user, password string) wpCachePrincipal {
		return &gcPrincipal{g: g, rdb: redis.NewClient(&redis.Options{Addr: mr.Addr(), DB: wpCacheDB}), user: user, password: password}
	}
	wpCacheACLUsers = func(context.Context, *redis.Client) ([]string, error) {
		names := []string{"default", "jabali_panel", "wp_bob", "t_bob", "wp_carol_notaulid"}
		for u := range g.users {
			names = append(names, u)
		}
		return names, nil
	}
	wpCacheACL = func(_ context.Context, _ *redis.Client, args ...any) error {
		parts := make([]string, len(args))
		for i, a := range args {
			parts[i] = fmt.Sprint(a)
		}
		cmd := strings.Join(parts, " ")
		switch parts[0] {
		case "SETUSER":
			g.users[parts[1]] = strings.TrimPrefix(parts[2], ">")
			cmd = "SETUSER " + parts[1] + " >(password)"
		case "DELUSER":
			delete(g.users, parts[1])
		}
		g.acl = append(g.acl, cmd)
		return nil
	}
	return g
}

func setKeys(t *testing.T, mr *miniredis.Miniredis, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		if err := mr.DB(wpCacheDB).Set(k, v); err != nil {
			t.Fatal(err)
		}
	}
}

func gcConfig(rdb *redis.Client, rows ...models.ApplicationInstall) WPCacheGCConfig {
	return WPCacheGCConfig{Redis: rdb, Installs: &gcInstalls{rows: rows}, Users: gcUsers{}, Salts: gcSalts{}, Secret: gcSecret}
}

func TestWPCacheGC_DeletesOnlyKeysNothingReadsAgain(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	defer mr.Close()
	g := stubGCRedis(t, mr, map[string]string{
		installACLUser("bob", gcA): gcToken(gcA),
		installACLUser("bob", gcB): gcToken(gcB),
		installACLUser("bob", gcC): gcToken(gcC),
	})
	pA, pB, pC, pD := gcPrefix(gcA), gcPrefix(gcB), gcPrefix(gcC), gcPrefix(gcD)
	stale := []string{
		pA + "o1791000000000001:posts:1:a", // older generation
		pA + "o999:posts:1:short",          // older (fewer digits)
		pA + "posts:1:old",                 // 1.1.0 key on a 1.2.0 site
	}
	kept := []string{
		pA + "gen:o", pA + "gen:p",
		pA + "o1791000000000002:posts:1:b", // current
		pA + "o1791000000000003:posts:1:c", // newer: the site flushed mid-pass
		pA + "page:abc", pA + "lock:page:abc", pA + "__jabali_verify",
		pB + "options:1:x", pB + "o5:posts:1:y", // 1.1.0 site: every key is live
		pC + "o1:posts:1:z", pC + "posts:1:old", // cache off: not the cleanup's
		pD + "posts:1:q",                    // not ready
		"jc:alice:" + gcA + ":o1:posts:1:x", // another user's name on the ID
		"jt:bob:a", "jabali:queue",
	}
	kv := map[string]string{pA + "gen:o": "1791000000000002", pA + "gen:p": "7"}
	for _, k := range append(append([]string{}, stale...), kept...) {
		if _, ok := kv[k]; !ok {
			kv[k] = "v"
		}
	}
	setKeys(t, mr, kv)
	if err := mr.DB(0).Set(pA+"o1:posts:1:otherdb", "v"); err != nil {
		t.Fatal(err)
	}

	res := runWPCacheGC(context.Background(), gcConfig(rdb,
		gcInstall(gcA, true, "ready", ""), gcInstall(gcB, true, "ready", ""),
		gcInstall(gcC, false, "ready", ""), gcInstall(gcD, true, "installing", "")))

	for _, k := range stale {
		if mr.DB(wpCacheDB).Exists(k) {
			t.Errorf("stale key %q survived", k)
		}
	}
	for _, k := range kept {
		if !mr.DB(wpCacheDB).Exists(k) {
			t.Errorf("key %q was deleted", k)
		}
	}
	if !mr.DB(0).Exists(pA + "o1:posts:1:otherdb") {
		t.Error("the cleanup must only touch DB 1")
	}
	if res.Stale != 3 || !res.Complete || res.Sites != 2 {
		t.Errorf("result = %+v, want 3 stale, complete, 2 sites", res)
	}
	for _, u := range g.unlink {
		if u.user != installACLUser("bob", gcA) || u.password != gcToken(gcA) {
			t.Errorf("unlink went through %s, want the install's own user and token", u.user)
		}
	}
	if st, ok := wpCacheStatsFor(gcA); !ok || st.Keys != 3 || !st.Complete {
		t.Errorf("stats A = %+v %v, want 3 keys (2 current objects + 1 page)", st, ok)
	}
	if st, ok := wpCacheStatsFor(gcB); !ok || st.Keys != 2 {
		t.Errorf("stats B = %+v %v, want 2 keys", st, ok)
	}
	if _, ok := wpCacheStatsFor(gcC); ok {
		t.Error("no stats for a site whose cache is off")
	}
	if len(g.acl) != 0 {
		t.Errorf("no ACL changes for live installs, got %v", g.acl)
	}
}

func TestWPCacheGC_EnforcesTheSiteKeyBudget(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	defer mr.Close()
	stubGCRedis(t, mr, map[string]string{installACLUser("bob", gcF): gcToken(gcF)})
	p := gcPrefix(gcF)
	kv := map[string]string{p + "gen:o": "10", p + "gen:p": "3"}
	for i := 0; i < 600; i++ {
		kv[fmt.Sprintf("%so10:posts:1:k%d", p, i)] = "v"
	}
	for i := 0; i < 3; i++ {
		kv[fmt.Sprintf("%so9:posts:1:s%d", p, i)] = "v"
	}
	setKeys(t, mr, kv)

	res := runWPCacheGC(context.Background(), gcConfig(rdb, gcInstall(gcF, true, "ready", `{"redis_maxmemory_mb":1}`)))

	live := 0
	for _, k := range mr.DB(wpCacheDB).Keys() {
		if strings.HasPrefix(k, p+"o10:") {
			live++
		}
		if strings.HasPrefix(k, p+"o9:") {
			t.Errorf("stale key %q survived", k)
		}
	}
	if live != 512 {
		t.Errorf("live keys = %d, want the 1 MB budget of 512", live)
	}
	if res.Trimmed != 88 || res.Stale != 3 {
		t.Errorf("result = %+v, want 88 trimmed, 3 stale", res)
	}
	if !mr.DB(wpCacheDB).Exists(p+"gen:o") || !mr.DB(wpCacheDB).Exists(p+"gen:p") {
		t.Error("the trim must never delete the counters")
	}
	if st, _ := wpCacheStatsFor(gcF); st.Keys != 512 {
		t.Errorf("stats = %d keys, want 512", st.Keys)
	}
}

func TestWPCacheGC_WithoutTheSiteCredentialDeletesNothing(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	defer mr.Close()
	g := stubGCRedis(t, mr, map[string]string{installACLUser("bob", gcA): "rotated-elsewhere"})
	p := gcPrefix(gcA)
	setKeys(t, mr, map[string]string{p + "gen:o": "5", p + "o4:posts:1:a": "v", p + "posts:1:old": "v"})

	res := runWPCacheGC(context.Background(), gcConfig(rdb, gcInstall(gcA, true, "ready", "")))

	if !mr.DB(wpCacheDB).Exists(p+"o4:posts:1:a") || !mr.DB(wpCacheDB).Exists(p+"posts:1:old") {
		t.Error("with no usable credential the site's keys must be left alone")
	}
	if res.SkippedSites != 1 || len(g.unlink) != 0 {
		t.Errorf("result = %+v unlinks = %d, want 1 skipped, none", res, len(g.unlink))
	}
	if _, ok := wpCacheStatsFor(gcA); ok {
		t.Error("a skipped site gets no fresh stats")
	}
}

func TestWPCacheGC_ReapsUsersWhoseInstallIsGone(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	defer mr.Close()
	g := stubGCRedis(t, mr, map[string]string{
		installACLUser("bob", gcA):    gcToken(gcA),
		installACLUser("bob", gcC):    gcToken(gcC), // cache off, row exists: not an orphan
		installACLUser("bob", gcGone): "unknown",    // no row
	})
	pG := gcPrefix(gcGone)
	setKeys(t, mr, map[string]string{pG + "o1:posts:1:a": "v", pG + "posts:1:b": "v", gcPrefix(gcC) + "posts:1:c": "v"})

	res := runWPCacheGC(context.Background(), gcConfig(rdb, gcInstall(gcA, true, "ready", ""), gcInstall(gcC, false, "ready", "")))

	want := []string{"SETUSER wp_bob_" + gcGone + " >(password)", "DELUSER wp_bob_" + gcGone, "SAVE"}
	if strings.Join(g.acl, "|") != strings.Join(want, "|") {
		t.Errorf("acl = %v, want %v", g.acl, want)
	}
	if res.ReapedUsers != 1 {
		t.Errorf("reaped = %d, want 1", res.ReapedUsers)
	}
	if mr.DB(wpCacheDB).Exists(pG+"o1:posts:1:a") || mr.DB(wpCacheDB).Exists(pG+"posts:1:b") {
		t.Error("the orphan's keys must go before the user")
	}
	for _, u := range g.unlink {
		if u.user != installACLUser("bob", gcGone) {
			t.Errorf("orphan keys deleted through %s, want the orphan user", u.user)
		}
	}
	if !mr.DB(wpCacheDB).Exists(gcPrefix(gcC) + "posts:1:c") {
		t.Error("a disabled site's keys are not an orphan's")
	}
}

func TestWPCacheGC_NoReapWithoutTheWholeInstallList(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	defer mr.Close()
	g := stubGCRedis(t, mr, map[string]string{installACLUser("bob", gcGone): "unknown"})
	cfg := gcConfig(rdb, gcInstall(gcA, true, "ready", ""))
	cfg.Installs.(*gcInstalls).extra = 1 // one row the list didn't return

	runWPCacheGC(context.Background(), cfg)

	if len(g.acl) != 0 {
		t.Errorf("a partial install list must not reap anyone, got %v", g.acl)
	}
}

func TestPurgeAndRevokeInstallCache_KeysFirstThroughTheUserThenTheUser(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	defer mr.Close()
	g := stubGCRedis(t, mr, map[string]string{installACLUser("bob", gcA): gcToken(gcA)})
	pA := gcPrefix(gcA)
	setKeys(t, mr, map[string]string{pA + "gen:o": "5", pA + "o5:posts:1:a": "v", pA + "page:x": "v", gcPrefix(gcB) + "o1:x": "v"})
	wpCacheStats.Store(gcA, wpCacheSiteStats{Keys: 3, At: time.Now()})

	if err := purgeAndRevokeInstallCache(context.Background(), rdb, gcSecret, gcSalts{}, "u1", "bob", gcA); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{pA + "gen:o", pA + "o5:posts:1:a", pA + "page:x"} {
		if mr.DB(wpCacheDB).Exists(k) {
			t.Errorf("key %q survived the purge", k)
		}
	}
	if !mr.DB(wpCacheDB).Exists(gcPrefix(gcB) + "o1:x") {
		t.Error("another install's keys must stay")
	}
	if len(g.unlink) == 0 || g.unlink[0].user != installACLUser("bob", gcA) || g.unlink[0].password != gcToken(gcA) {
		t.Errorf("purge unlinks = %+v, want the install's own user and token", g.unlink)
	}
	if strings.Join(g.acl, "|") != "DELUSER wp_bob_"+gcA+"|SAVE" {
		t.Errorf("acl = %v, want the user removed after the purge", g.acl)
	}
	if _, ok := wpCacheStatsFor(gcA); ok {
		t.Error("stats for a revoked site must be dropped")
	}
}

const gcI = "01IIIIIIIIIIIIIIIIIIIIIIII"

func TestRunAppDelete_WordPressRemovesTheCacheUser(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	defer mr.Close()
	g := stubGCRedis(t, mr, map[string]string{installACLUser("alice", gcI): cacheInstallToken(gcSecret, "alice", gcI, "salt-u1")})
	setKeys(t, mr, map[string]string{"jc:alice:" + gcI + ":o1:posts:1:a": "v"})
	_, _, _, _, _, _, deps := newAppDeleteFakes()
	deps.Redis, deps.CacheTokenSecret, deps.CacheTokenSalts = rdb, gcSecret, gcSalts{}
	args := appDeleteArgs()
	args.InstallID = gcI

	if err := RunAppDelete(args, deps); err != nil {
		t.Fatal(err)
	}
	if mr.DB(wpCacheDB).Exists("jc:alice:" + gcI + ":o1:posts:1:a") {
		t.Error("the site's cache keys must go with it")
	}
	if strings.Join(g.acl, "|") != "DELUSER wp_alice_"+gcI+"|SAVE" {
		t.Errorf("acl = %v, want the site's cache user removed", g.acl)
	}
}

func TestRunAppDelete_OtherAppsLeaveRedisAlone(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	defer mr.Close()
	g := stubGCRedis(t, mr, map[string]string{})
	_, _, _, _, _, _, deps := newAppDeleteFakes()
	deps.Redis, deps.CacheTokenSecret, deps.CacheTokenSalts = rdb, gcSecret, gcSalts{}
	args := appDeleteArgs()
	args.AppType = "joomla"

	if err := RunAppDelete(args, deps); err != nil {
		t.Fatal(err)
	}
	if len(g.acl) != 0 {
		t.Errorf("acl = %v, want none for a non-WordPress app", g.acl)
	}
}

func TestClassifyWPCacheKey(t *testing.T) {
	cases := []struct {
		rest, gen string
		want      wpCacheKeyKind
	}{
		{"gen:o", "5", wpKeyKeep},
		{"gen:p", "", wpKeyKeep},
		{"lock:page:x", "5", wpKeyKeep},
		{"__jabali_verify", "5", wpKeyKeep},
		{"page:abc", "5", wpKeyPage},
		{"o5:posts:1:a", "5", wpKeyLive},
		{"o6:posts:1:a", "5", wpKeyLive},
		{"o4:posts:1:a", "5", wpKeyStale},
		{"o99:posts:1:a", "100", wpKeyStale},
		{"o100:posts:1:a", "99", wpKeyLive},
		{"o4:posts:1:a", "", wpKeyLive},
		{"posts:1:a", "5", wpKeyStale},
		{"posts:1:a", "", wpKeyLive},
	}
	for _, c := range cases {
		if got := classifyWPCacheKey(c.rest, c.gen); got != c.want {
			t.Errorf("classify(%q, gen %q) = %d, want %d", c.rest, c.gen, got, c.want)
		}
	}
}

func TestCacheStats_ThePanelKeyCountWins(t *testing.T) {
	wpRepo, domRepo, userRepo, id := seedCacheInstall(t)
	r, ag, _ := applicationsRouter(t, "user1", false, wpRepo, domRepo, userRepo, nil)
	ag.callFn = func(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
		if cmd == "wordpress.cache_stats" {
			return json.RawMessage(`{"keys":7,"keys_approx":true,"driver":"phpredis"}`), nil
		}
		return json.RawMessage(`{}`), nil
	}
	t.Cleanup(func() { wpCacheStats.Delete(id) })
	get := func() map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/applications/"+id+"/cache-stats", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
		var body struct {
			Stats map[string]any `json:"stats"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Stats
	}

	// The plugin counts with SCAN, which its Redis user no longer has, so its
	// count is never shown: no keys until the panel has counted.
	s := get()
	if _, ok := s["keys"]; ok {
		t.Errorf("before a cleanup pass: keys=%v, want none", s["keys"])
	}
	if _, ok := s["keys_approx"]; ok {
		t.Errorf("before a cleanup pass: keys_approx=%v, want none", s["keys_approx"])
	}
	if s["keys_at"] != nil || s["driver"] != "phpredis" {
		t.Errorf("before a cleanup pass: keys_at=%v driver=%v, want no keys_at and the plugin's other stats", s["keys_at"], s["driver"])
	}
	wpCacheStats.Store(id, wpCacheSiteStats{Keys: 42, At: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC), Complete: true})
	if s := get(); s["keys"] != float64(42) || s["keys_at"] != "2026-10-09T12:00:00Z" {
		t.Errorf("after a pass: keys=%v keys_at=%v, want the panel's 42 at 2026-10-09T12:00:00Z", s["keys"], s["keys_at"])
	}
	if _, ok := get()["keys_approx"]; ok {
		t.Error("after a pass: the panel's count is exact, want no keys_approx")
	}
}

func TestSetApplicationCache_DisableRemovesTheKeysThenTheUser(t *testing.T) {
	ctx := context.Background()
	mr, rdb := newMiniRedis(t)
	defer mr.Close()
	wpRepo, domRepo, userRepo := wpUserAndDomain() // user1 = alice
	if err := wpRepo.Create(ctx, &models.WordPressInstall{ID: gcI, UserID: "user1", DomainID: "domain1", AppType: "wordpress", CacheEnabled: true, Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	g := stubGCRedis(t, mr, map[string]string{installACLUser("alice", gcI): cacheInstallToken(gcSecret, "alice", gcI, "salt-user1")})
	p := "jc:alice:" + gcI + ":"
	setKeys(t, mr, map[string]string{p + "gen:o": "5", p + "o5:posts:1:a": "v", "jc:alice:" + gcA + ":o1:x": "v"})
	cfg := ApplicationHandlerConfig{
		ApplicationInstalls: wpRepo, Domains: domRepo, Users: userRepo, Agent: &mockAgent{},
		Redis: rdb, CacheTokenSecret: gcSecret, CacheTokenSalts: gcSalts{},
	}

	if err := SetApplicationCache(ctx, cfg, gcI, false, true, "user1"); err != nil {
		t.Fatal(err)
	}
	if mr.DB(wpCacheDB).Exists(p+"gen:o") || mr.DB(wpCacheDB).Exists(p+"o5:posts:1:a") {
		t.Error("disabling the cache must remove the site's keys")
	}
	if !mr.DB(wpCacheDB).Exists("jc:alice:" + gcA + ":o1:x") {
		t.Error("another install's keys must stay")
	}
	if strings.Join(g.acl, "|") != "DELUSER wp_alice_"+gcI+"|SAVE" {
		t.Errorf("acl = %v, want the site's cache user removed after its keys", g.acl)
	}
	if len(g.unlink) == 0 || g.unlink[0].user != installACLUser("alice", gcI) {
		t.Errorf("keys must be deleted through the site's own user, got %+v", g.unlink)
	}
}

func TestWPCacheGC_ForgetsStatsOfSitesNoLongerCached(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	defer mr.Close()
	stubGCRedis(t, mr, map[string]string{})
	wpCacheStats.Store(gcC, wpCacheSiteStats{Keys: 9, At: time.Now()})

	runWPCacheGC(context.Background(), gcConfig(rdb, gcInstall(gcC, false, "ready", "")))

	if _, ok := wpCacheStatsFor(gcC); ok {
		t.Error("stats of a site whose cache is off must be forgotten")
	}
}
