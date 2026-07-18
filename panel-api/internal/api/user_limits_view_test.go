package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// JAB-12: the admin Limits card needs the usage endpoint to expose the package
// limits and the raw override row alongside the resolved effective bundle so it
// can label each field's source (inherited vs explicit override, incl. an
// explicit-unlimited &0). These fakes drive that response shape.

type ulUserRepo struct {
	repository.UserRepository
	u *models.User
}

func (r *ulUserRepo) FindByID(context.Context, string) (*models.User, error) { return r.u, nil }

type ulPackageRepo struct {
	repository.PackageRepository
	p *models.HostingPackage
}

func (r *ulPackageRepo) FindByID(context.Context, string) (*models.HostingPackage, error) {
	return r.p, nil
}

type ulOverrideRepo struct {
	repository.UserLimitOverrideRepository
	o *models.UserLimitOverride
}

func (r *ulOverrideRepo) FindByUserID(context.Context, string) (*models.UserLimitOverride, error) {
	if r.o == nil {
		return nil, repository.ErrNotFound
	}
	return r.o, nil
}

func u32(v uint32) *uint32 { return &v }

func serveUsage(t *testing.T, cfg UserLimitsHandlerConfig) usageResponse {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1", IsAdmin: true})
		c.Next()
	})
	RegisterUserLimitsRoutes(v1, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/u1/usage", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d %s", rec.Code, rec.Body.String())
	}
	var resp usageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

func TestUsage_ExposesPackageAndOverride(t *testing.T) {
	uname := "alice"
	pkgID := "pkg1"
	cfg := UserLimitsHandlerConfig{
		Users:    &ulUserRepo{u: &models.User{ID: "u1", Username: &uname, PackageID: &pkgID}},
		Packages: &ulPackageRepo{p: &models.HostingPackage{ID: pkgID, MemoryLimitMB: 512, MaxTasks: 100}},
		// Override raises memory to 1024 and pins CPU to 0 (explicit unlimited).
		LimitOverrides: &ulOverrideRepo{o: &models.UserLimitOverride{
			UserID:          "u1",
			MemoryLimitMB:   u32(1024),
			CPUQuotaPercent: u32(0),
		}},
	}
	resp := serveUsage(t, cfg)

	if resp.Package == nil {
		t.Fatal("package limits must be present when the user has a package")
	}
	if resp.Package.MemoryLimitMB != 512 || resp.Package.MaxTasks != 100 {
		t.Errorf("package view wrong: %+v", resp.Package)
	}
	if resp.Override == nil || resp.Override.MemoryLimitMB == nil || *resp.Override.MemoryLimitMB != 1024 {
		t.Fatalf("override memory must be the explicit 1024, got %+v", resp.Override)
	}
	// Override beats package: effective memory is the override 1024, not 512.
	if resp.Effective.MemoryLimitMB != 1024 {
		t.Errorf("effective memory: want 1024 (override wins), got %d", resp.Effective.MemoryLimitMB)
	}
	// MaxTasks has no override → inherits the package value.
	if resp.Effective.MaxTasks != 100 {
		t.Errorf("effective max_tasks: want 100 (inherited), got %d", resp.Effective.MaxTasks)
	}
	// CPU override is an explicit &0 (unlimited): the row must carry a non-nil
	// pointer to 0, so the GUI shows "explicit unlimited", not "inherited".
	if resp.Override.CPUQuotaPercent == nil || *resp.Override.CPUQuotaPercent != 0 {
		t.Errorf("cpu override must be explicit &0, got %+v", resp.Override.CPUQuotaPercent)
	}
}

func TestUsage_NoPackageNoOverride(t *testing.T) {
	uname := "bob"
	cfg := UserLimitsHandlerConfig{
		Users:          &ulUserRepo{u: &models.User{ID: "u1", Username: &uname}},
		Packages:       &ulPackageRepo{},
		LimitOverrides: &ulOverrideRepo{},
	}
	resp := serveUsage(t, cfg)
	if resp.Package != nil {
		t.Errorf("no package → package must be nil, got %+v", resp.Package)
	}
	if resp.Override != nil {
		t.Errorf("no override → override must be nil, got %+v", resp.Override)
	}
}
