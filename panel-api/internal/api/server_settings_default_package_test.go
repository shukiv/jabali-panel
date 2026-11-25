package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// The default package for migrations is a hosting package id, checked against
// the packages table before it is saved; "" clears it.

type fakeSettingsPackages map[string]error

func (f fakeSettingsPackages) FindByID(_ context.Context, id string) (*models.HostingPackage, error) {
	if err, ok := f[id]; ok {
		if err != nil {
			return nil, err
		}
		return &models.HostingPackage{ID: id}, nil
	}
	return nil, repository.ErrNotFound
}

func defaultPackagePatch(t *testing.T, repo *mockServerSettingsRepo, pkgs SettingsPackageFinder, value string) int {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "test-admin", IsAdmin: true})
		c.Next()
	})
	cfg := ServerSettingsHandlerConfig{Repo: repo, Agent: agent.NewMockClient(), Log: slog.Default()}
	if pkgs != nil {
		cfg.Packages = pkgs
	}
	RegisterServerSettingsRoutes(v1, cfg)
	body, _ := json.Marshal(map[string]any{"default_package_id": value})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec.Code
}

func TestServerSettingsPatch_DefaultPackage(t *testing.T) {
	const tier3 = "01TIER3PKG0000000000000000"
	pkgs := fakeSettingsPackages{tier3: nil, "01BROKEN000000000000000000": errors.New("db down")}
	old := "01OLDPKG000000000000000000"
	fresh := func() *mockServerSettingsRepo {
		return &mockServerSettingsRepo{getResult: &models.ServerSettings{ID: 1, SSHPort: 22, DefaultPackageID: &old}}
	}

	t.Run("known package saves", func(t *testing.T) {
		repo := fresh()
		require.Equal(t, http.StatusOK, defaultPackagePatch(t, repo, pkgs, " "+tier3+" "))
		require.NotNil(t, repo.getResult.DefaultPackageID)
		require.Equal(t, tier3, *repo.getResult.DefaultPackageID)
	})
	t.Run("unknown package is refused and nothing changes", func(t *testing.T) {
		repo := fresh()
		require.Equal(t, http.StatusUnprocessableEntity, defaultPackagePatch(t, repo, pkgs, "01NOSUCHPKG000000000000000"))
		require.Equal(t, old, *repo.getResult.DefaultPackageID)
	})
	t.Run("lookup error is a 500", func(t *testing.T) {
		repo := fresh()
		require.Equal(t, http.StatusInternalServerError, defaultPackagePatch(t, repo, pkgs, "01BROKEN000000000000000000"))
		require.Equal(t, old, *repo.getResult.DefaultPackageID)
	})
	t.Run("empty clears", func(t *testing.T) {
		repo := fresh()
		require.Equal(t, http.StatusOK, defaultPackagePatch(t, repo, pkgs, ""))
		require.Nil(t, repo.getResult.DefaultPackageID)
	})
	t.Run("no package lookup wired refuses a value", func(t *testing.T) {
		repo := fresh()
		require.Equal(t, http.StatusServiceUnavailable, defaultPackagePatch(t, repo, nil, tier3))
		require.Equal(t, old, *repo.getResult.DefaultPackageID)
	})
}
