package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// php_pool_delete_slug_test.go — deleting a pool removes THAT pool on the host.
// php.pool.remove with only a username removes the user's default pool (slug ==
// username), so deleting a per-version pool (GH #329) took down the pool
// serving the user's default sites until the reconciler re-applied it, and left
// the deleted pool running with no row for anything to reap.

type poolListErrRepo struct{ *mockPHPPoolRepo }

func (poolListErrRepo) ListByUserID(context.Context, string) ([]models.PHPPool, error) {
	return nil, errors.New("db down")
}

func poolDeleteRouter(t *testing.T, pools repository.PHPPoolRepository, removes chan<- map[string]any) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "admin1", IsAdmin: true})
		c.Next()
	})
	uname := "alice"
	RegisterPHPPoolRoutes(r.Group("/api/v1"), PHPPoolHandlerConfig{
		PHPPools:            pools,
		PHPPoolIniOverrides: newStatefulOverrideRepo(),
		Users:               &mockUserRepo{users: map[string]*models.User{"u1": {ID: "u1", Username: &uname}}},
		Packages:            &mockPackageRepo{packages: map[string]*models.HostingPackage{}},
		Agent: &mockAgent{callFn: func(_ context.Context, command string, params any) (json.RawMessage, error) {
			if command == "php.pool.remove" {
				b, _ := json.Marshal(params)
				var m map[string]any
				_ = json.Unmarshal(b, &m)
				removes <- m
			}
			return json.RawMessage(`{}`), nil
		}},
	})
	return r
}

func TestPHPPoolDelete_RemovesThatPoolOnTheHost(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		pools    []models.PHPPool
		delete   string
		wantSlug string
	}{
		"a per-version pool, not the default": {
			pools: []models.PHPPool{
				{ID: "p84", UserID: "u1", PHPVersion: "8.4", CreatedAt: t0},
				{ID: "p83", UserID: "u1", PHPVersion: "8.3", CreatedAt: t0.Add(time.Hour)},
			},
			delete: "p83", wantSlug: "alice-php8.3",
		},
		"the only pool is the default": {
			pools:  []models.PHPPool{{ID: "p84", UserID: "u1", PHPVersion: "8.4", CreatedAt: t0}},
			delete: "p84", wantSlug: "alice",
		},
	} {
		t.Run(name, func(t *testing.T) {
			pools := newMockPHPPoolRepo()
			for i := range tc.pools {
				p := tc.pools[i]
				_ = pools.Create(context.Background(), &p)
			}
			removes := make(chan map[string]any, 4)
			r := poolDeleteRouter(t, pools, removes)
			if w := iniOverrideReq(t, r, http.MethodDelete, "/api/v1/php-pools/"+tc.delete, ""); w.Code != http.StatusNoContent {
				t.Fatalf("delete: %d %s", w.Code, w.Body.String())
			}
			select {
			case got := <-removes:
				if got["username"] != "alice" || got["slug"] != tc.wantSlug {
					t.Fatalf("php.pool.remove got %v, want username alice, slug %s", got, tc.wantSlug)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("php.pool.remove was never called")
			}
		})
	}
}

// Without the user's pool list the slug is unknown: refuse before deleting
// anything rather than remove a guessed pool on the host.
func TestPHPPoolDelete_RefusesWhenThePoolCannotBeIdentified(t *testing.T) {
	pools := newMockPHPPoolRepo()
	_ = pools.Create(context.Background(), &models.PHPPool{ID: "p83", UserID: "u1", PHPVersion: "8.3"})
	removes := make(chan map[string]any, 4)
	r := poolDeleteRouter(t, poolListErrRepo{pools}, removes)
	if w := iniOverrideReq(t, r, http.MethodDelete, "/api/v1/php-pools/p83", ""); w.Code != http.StatusInternalServerError {
		t.Fatalf("delete: %d %s, want 500", w.Code, w.Body.String())
	}
	if _, err := pools.FindByID(context.Background(), "p83"); err != nil {
		t.Fatalf("pool row deleted: %v", err)
	}
	select {
	case got := <-removes:
		t.Fatalf("php.pool.remove called with %v", got)
	case <-time.After(200 * time.Millisecond):
	}
}
