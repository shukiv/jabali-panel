package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
)

func TestRateLimit_BlocksAfterBurst(t *testing.T) {
	t.Parallel()

	// 1 req/sec with a burst of 2 → the third request back-to-back should
	// be rejected before the rate replenishes.
	rl := middleware.NewRateLimiter(middleware.RateLimiterConfig{
		DefaultRate:  rate.Limit(1),
		DefaultBurst: 2,
	})
	r := gin.New()
	r.GET("/x", rl.Default(), func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	hit := func() int {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.RemoteAddr = "10.0.0.1:54321"
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}

	assert.Equal(t, http.StatusOK, hit())
	assert.Equal(t, http.StatusOK, hit())

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.RemoteAddr = "10.0.0.1:54321"
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	// Retry-After must be a non-negative integer.
	ra, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	require.NoError(t, err, "Retry-After must be an integer")
	assert.GreaterOrEqual(t, ra, 0)
}

func TestRateLimit_SeparateBucketsPerIP(t *testing.T) {
	t.Parallel()

	rl := middleware.NewRateLimiter(middleware.RateLimiterConfig{
		DefaultRate:  rate.Limit(1),
		DefaultBurst: 1,
	})
	r := gin.New()
	r.GET("/x", rl.Default(), func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	hit := func(ip string) int {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.RemoteAddr = ip + ":1000"
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}

	assert.Equal(t, http.StatusOK, hit("10.0.0.1"))
	assert.Equal(t, http.StatusTooManyRequests, hit("10.0.0.1"))
	// Different IP gets its own bucket — still has budget.
	assert.Equal(t, http.StatusOK, hit("10.0.0.2"))
}

// JAB-266: StrictPerActor keys the strict bucket on the authenticated user,
// not the client IP, so an actor cannot multiply its budget across addresses.
func TestStrictPerActor_KeyedByUserNotIP(t *testing.T) {
	t.Parallel()

	rl := middleware.NewRateLimiter(middleware.RateLimiterConfig{
		DefaultRate:  rate.Limit(100),
		DefaultBurst: 100,
		StrictRate:   rate.Limit(1),
		StrictBurst:  1,
	})
	r := gin.New()
	withUser := func(uid string) gin.HandlerFunc {
		return func(c *gin.Context) { ginctx.SetClaims(c, &auth.AccessClaims{UserID: uid}); c.Next() }
	}
	r.POST("/u1", withUser("u1"), rl.StrictPerActor(), func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	r.POST("/u2", withUser("u2"), rl.StrictPerActor(), func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	hit := func(path, ip string) int {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.RemoteAddr = ip + ":1000"
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}

	// Same user from a NEW IP each time still shares one bucket.
	assert.Equal(t, http.StatusOK, hit("/u1", "10.0.0.1"))
	assert.Equal(t, http.StatusTooManyRequests, hit("/u1", "10.0.0.2"))
	assert.Equal(t, http.StatusTooManyRequests, hit("/u1", "10.0.0.3"))
	// A different user has an independent bucket.
	assert.Equal(t, http.StatusOK, hit("/u2", "10.0.0.1"))
}

// Unauthenticated requests (no claims) fall back to per-IP keying so the
// middleware is still safe if mounted before auth.
func TestStrictPerActor_FallsBackToIPWhenAnonymous(t *testing.T) {
	t.Parallel()

	rl := middleware.NewRateLimiter(middleware.RateLimiterConfig{
		StrictRate:  rate.Limit(1),
		StrictBurst: 1,
	})
	r := gin.New()
	r.POST("/x", rl.StrictPerActor(), func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	hit := func(ip string) int {
		req := httptest.NewRequest(http.MethodPost, "/x", nil)
		req.RemoteAddr = ip + ":1000"
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}
	assert.Equal(t, http.StatusOK, hit("10.0.0.1"))
	assert.Equal(t, http.StatusTooManyRequests, hit("10.0.0.1"))
	assert.Equal(t, http.StatusOK, hit("10.0.0.2")) // distinct IP → own bucket
}

func TestRateLimit_StrictBucketIsIndependent(t *testing.T) {
	t.Parallel()

	rl := middleware.NewRateLimiter(middleware.RateLimiterConfig{
		DefaultRate:  rate.Limit(100), // effectively unlimited
		DefaultBurst: 100,
		StrictRate:   rate.Limit(1),
		StrictBurst:  1,
	})
	r := gin.New()
	r.GET("/default", rl.Default(), func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	r.POST("/strict", rl.Strict(), func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	hit := func(method, path string) int {
		req := httptest.NewRequest(method, path, nil)
		req.RemoteAddr = "10.0.0.1:1000"
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}

	// /default stays open under its own burst.
	assert.Equal(t, http.StatusOK, hit(http.MethodGet, "/default"))
	// /strict: first pass.
	assert.Equal(t, http.StatusOK, hit(http.MethodPost, "/strict"))
	// /strict: second blocked — the strict bucket is not the same as default.
	assert.Equal(t, http.StatusTooManyRequests, hit(http.MethodPost, "/strict"))
	// /default still works — separate bucket.
	assert.Equal(t, http.StatusOK, hit(http.MethodGet, "/default"))
}

// KratosFlows gates POST submissions to /.ory self-service login + recovery per
// IP, but passes flow-init GETs, CSRF fetches, and non-credential POSTs (JAB-4).
func TestKratosFlows_GatesCredentialPostsOnly(t *testing.T) {
	rl := middleware.NewRateLimiter(middleware.RateLimiterConfig{
		DefaultRate: 1000, DefaultBurst: 1000,
		KratosRate: 0, KratosBurst: 3, // 3 immediate, no refill in-test
	})
	mw := rl.KratosFlows(nil)

	run := func(method, path, ip string) int {
		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Request.Header.Set("X-Forwarded-For", ip); c.Next() })
		r.Any("/*any", mw, func(c *gin.Context) { c.Status(http.StatusOK) })
		req := httptest.NewRequest(method, path, nil)
		// Distinct peers, not distinct headers: clientIP only honours
		// forwarding headers from a peer that presented credentials (the
		// unix socket nginx uses). A header-only "IP" is exactly what an
		// attacker would rotate to evade this limiter, so it must not key it.
		req.RemoteAddr = ip + ":54321"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}

	// POST login: first 3 (burst) pass, 4th throttled.
	for i := 0; i < 3; i++ {
		if code := run(http.MethodPost, "/.ory/self-service/login?flow=x", "10.0.0.1"); code != http.StatusOK {
			t.Fatalf("login POST #%d = %d; want 200 within burst", i+1, code)
		}
	}
	if code := run(http.MethodPost, "/.ory/self-service/login?flow=x", "10.0.0.1"); code != http.StatusTooManyRequests {
		t.Errorf("login POST over burst = %d; want 429", code)
	}

	// A different IP has its own bucket.
	if code := run(http.MethodPost, "/.ory/self-service/login?flow=x", "10.0.0.2"); code != http.StatusOK {
		t.Errorf("login POST from fresh IP = %d; want 200", code)
	}

	// Flow-init GET is never gated (login must still work).
	for i := 0; i < 10; i++ {
		if code := run(http.MethodGet, "/.ory/self-service/login/browser", "10.0.0.1"); code != http.StatusOK {
			t.Fatalf("login init GET = %d; want 200 (never gated)", code)
		}
	}

	// Non-credential POST (e.g. logout) passes.
	for i := 0; i < 10; i++ {
		if code := run(http.MethodPost, "/.ory/self-service/logout", "10.0.0.1"); code != http.StatusOK {
			t.Fatalf("logout POST = %d; want 200 (not a gated flow)", code)
		}
	}
}

// Recovery POSTs are gated too (enumeration protection), sharing the tier.
func TestKratosFlows_GatesRecovery(t *testing.T) {
	rl := middleware.NewRateLimiter(middleware.RateLimiterConfig{DefaultRate: 1000, DefaultBurst: 1000, KratosRate: 0, KratosBurst: 2})
	mw := rl.KratosFlows(nil)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Any("/*any", mw, func(c *gin.Context) { c.Status(http.StatusOK) })
	call := func() int {
		req := httptest.NewRequest(http.MethodPost, "/.ory/self-service/recovery?flow=y", nil)
		req.Header.Set("X-Forwarded-For", "10.0.0.9")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	if call() != http.StatusOK || call() != http.StatusOK {
		t.Fatal("first 2 recovery POSTs should pass")
	}
	if call() != http.StatusTooManyRequests {
		t.Error("3rd recovery POST should be 429")
	}
}
