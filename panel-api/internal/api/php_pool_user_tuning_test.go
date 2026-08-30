package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type mockModesRepo struct{}

func (mockModesRepo) GetAll(context.Context) ([]models.PHPPerformanceMode, error) { return nil, nil }
func (mockModesRepo) Get(context.Context, string) (*models.PHPPerformanceMode, error) {
	return nil, repository.ErrNotFound
}
func (mockModesRepo) Update(context.Context, *models.PHPPerformanceMode) error { return nil }
func (mockModesRepo) EnsureDefaults(context.Context) (int, error)              { return 0, nil }

func setupTuningRouter(
	t *testing.T,
	pools []*models.PHPPool,
	agentFn func(ctx context.Context, command string, params any) (json.RawMessage, error),
) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1"})
		c.Next()
	})
	pkgID := "pkg1"
	users := &mockUserRepo{users: map[string]*models.User{"u1": {ID: "u1", PackageID: &pkgID}}}
	packages := &mockPackageRepo{packages: map[string]*models.HostingPackage{
		"pkg1": {ID: "pkg1", FpmUserCanEdit: true},
	}}
	pr := newMockPHPPoolRepo()
	for _, p := range pools {
		pr.Create(context.Background(), p)
	}
	RegisterPHPUserTuningRoutes(r.Group("/api/v1"), PHPUserTuningHandlerConfig{
		Agent:               &mockAgent{callFn: agentFn},
		Users:               users,
		Packages:            packages,
		PHPPools:            pr,
		PHPPoolIniOverrides: mockPHPPoolIniOverrideRepo{},
		Modes:               mockModesRepo{},
	})
	return r
}

func tuningVersions(t *testing.T, r *gin.Engine) []struct {
	PHPVersion string `json:"php_version"`
	IsDefault  bool   `json:"is_default"`
} {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me/php-pool-tuning", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Pools []struct {
			PHPVersion string `json:"php_version"`
			IsDefault  bool   `json:"is_default"`
		} `json:"pools"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body.Pools
}

func versionsInstalled(versions ...string) func(context.Context, string, any) (json.RawMessage, error) {
	return func(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
		if cmd == "php.version.list" {
			b, _ := json.Marshal(map[string][]string{"versions": versions})
			return b, nil
		}
		return json.RawMessage(`{}`), nil
	}
}

func TestTuning_HidesUninstalledVersionPools(t *testing.T) {
	// A pool row survives its PHP version being uninstalled; the tuning list must
	// not surface it. Only 8.4 is installed, so the orphaned 8.5 pool is dropped
	// and 8.4 (the sole survivor) carries is_default.
	pools := []*models.PHPPool{
		{ID: "p84", UserID: "u1", PHPVersion: "8.4"},
		{ID: "p85", UserID: "u1", PHPVersion: "8.5"},
	}
	got := tuningVersions(t, setupTuningRouter(t, pools, versionsInstalled("8.4")))
	if len(got) != 1 || got[0].PHPVersion != "8.4" {
		t.Fatalf("want only 8.4, got %+v", got)
	}
	if !got[0].IsDefault {
		t.Fatalf("the sole surviving pool must be is_default, got %+v", got[0])
	}
}

func TestTuning_AgentDownReturnsAllPools(t *testing.T) {
	// Fail-open: an agent hiccup must not blank the card — every pool is returned.
	pools := []*models.PHPPool{
		{ID: "p84", UserID: "u1", PHPVersion: "8.4"},
		{ID: "p85", UserID: "u1", PHPVersion: "8.5"},
	}
	agentFn := func(context.Context, string, any) (json.RawMessage, error) {
		return nil, context.DeadlineExceeded
	}
	got := tuningVersions(t, setupTuningRouter(t, pools, agentFn))
	seen := map[string]bool{}
	defaults := 0
	for _, p := range got {
		seen[p.PHPVersion] = true
		if p.IsDefault {
			defaults++
		}
	}
	if !seen["8.4"] || !seen["8.5"] || len(got) != 2 {
		t.Fatalf("want both 8.4 and 8.5 (fail-open), got %+v", got)
	}
	if defaults != 1 {
		t.Fatalf("exactly one pool must be is_default, got %d", defaults)
	}
}

func TestTuning_NoInstalledVersionsLeaves_NoPanic(t *testing.T) {
	// None of the user's pool versions are installed → empty list, no panic.
	pools := []*models.PHPPool{{ID: "p85", UserID: "u1", PHPVersion: "8.5"}}
	got := tuningVersions(t, setupTuningRouter(t, pools, versionsInstalled("9.9")))
	if len(got) != 0 {
		t.Fatalf("want no pools, got %+v", got)
	}
}
