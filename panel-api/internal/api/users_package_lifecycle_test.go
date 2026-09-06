package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// fakeLimitsReconciler structurally satisfies the users handler's reconciler
// interface. ReconcileUserLimits records the call count and signals `fired`
// (SetPackage dispatches it in a background goroutine, so the tests sync on the
// channel before asserting). ReconcileSSHKeysForUser is unused here.
type fakeLimitsReconciler struct {
	mu    sync.Mutex
	n     int
	fired chan struct{}
}

func (f *fakeLimitsReconciler) ReconcileUserLimits(_ context.Context) {
	f.mu.Lock()
	f.n++
	f.mu.Unlock()
	select {
	case f.fired <- struct{}{}:
	default:
	}
}
func (f *fakeLimitsReconciler) ReconcileSSHKeysForUser(_ context.Context, _ string) error { return nil }
func (f *fakeLimitsReconciler) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

func newFakeReconciler() *fakeLimitsReconciler {
	return &fakeLimitsReconciler{fired: make(chan struct{}, 1)}
}

// buildRouterWithReconciler mirrors buildRouterWithPackages but also wires a
// reconciler so the limits-reconcile dispatch is observable.
func buildRouterWithReconciler(
	repo repository.UserRepository,
	pkgRepo repository.PackageRepository,
	rec *fakeLimitsReconciler,
	claims *auth.AccessClaims,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1", func(c *gin.Context) {
		if claims != nil {
			ginctx.SetClaims(c, claims)
		}
		c.Next()
	})
	api.RegisterUserRoutes(g, api.UserHandlerConfig{
		Repo:       repo,
		Packages:   pkgRepo,
		Reconciler: rec,
		BcryptCost: bcrypt.MinCost,
	})
	return r
}

// expectReconcile asserts whether the limits reconcile fired. When want is
// true it blocks (with a generous timeout) for the background goroutine; when
// false it waits a short grace and asserts nothing fired.
func expectReconcile(t *testing.T, rec *fakeLimitsReconciler, want bool) {
	t.Helper()
	if want {
		select {
		case <-rec.fired:
		case <-time.After(2 * time.Second):
			t.Fatal("expected a limits reconcile, got none")
		}
		assert.Equal(t, 1, rec.count(), "limits reconcile must fire exactly once")
	} else {
		select {
		case <-rec.fired:
			t.Fatal("expected NO limits reconcile, but one fired")
		case <-time.After(150 * time.Millisecond):
		}
		assert.Equal(t, 0, rec.count(), "no limits reconcile expected")
	}
}

// TestUsers_Patch_PackageAssign_ReconcilesOnce: assigning a package routes
// through userops.SetPackage, which schedules the limits reconcile exactly once.
func TestUsers_Patch_PackageAssign_ReconcilesOnce(t *testing.T) {
	repo := newMemUserRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	target := makeUser(t, "u@example.com", false, "password01")
	repo.seed(admin)
	repo.seed(target)
	pkgRepo := newMemPackageRepo()
	pkg := &models.HostingPackage{ID: ids.NewULID(), Name: "Premium"}
	pkgRepo.seed(pkg)
	rec := newFakeReconciler()

	r := buildRouterWithReconciler(repo, pkgRepo, rec, &auth.AccessClaims{UserID: admin.ID, IsAdmin: true})
	w := doJSON(t, r, http.MethodPatch, "/api/v1/users/"+target.ID, map[string]any{"package_id": pkg.ID})
	require.Equal(t, http.StatusOK, w.Code)
	expectReconcile(t, rec, true)
}

// TestUsers_Patch_PackageReplace_ReconcilesOnce: replacing one package with a
// different one is still a single reconcile.
func TestUsers_Patch_PackageReplace_ReconcilesOnce(t *testing.T) {
	repo := newMemUserRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	pkgOld := ids.NewULID()
	target := makeUser(t, "u@example.com", false, "password01")
	target.PackageID = &pkgOld
	repo.seed(admin)
	repo.seed(target)
	pkgRepo := newMemPackageRepo()
	pkgRepo.seed(&models.HostingPackage{ID: pkgOld, Name: "Old"})
	pkgNew := &models.HostingPackage{ID: ids.NewULID(), Name: "New"}
	pkgRepo.seed(pkgNew)
	rec := newFakeReconciler()

	r := buildRouterWithReconciler(repo, pkgRepo, rec, &auth.AccessClaims{UserID: admin.ID, IsAdmin: true})
	w := doJSON(t, r, http.MethodPatch, "/api/v1/users/"+target.ID, map[string]any{"package_id": pkgNew.ID})
	require.Equal(t, http.StatusOK, w.Code)
	expectReconcile(t, rec, true)
}

// TestUsers_Patch_PackageUnchanged_NoReconcile: re-assigning the SAME package
// persists but does not reconcile (SetPackage's next==prev guard) — the case
// the old inline "newPackageID != prevPackageID" also had to get right.
func TestUsers_Patch_PackageUnchanged_NoReconcile(t *testing.T) {
	repo := newMemUserRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	pkgID := ids.NewULID()
	target := makeUser(t, "u@example.com", false, "password01")
	target.PackageID = &pkgID
	repo.seed(admin)
	repo.seed(target)
	pkgRepo := newMemPackageRepo()
	pkgRepo.seed(&models.HostingPackage{ID: pkgID, Name: "Same"})
	rec := newFakeReconciler()

	r := buildRouterWithReconciler(repo, pkgRepo, rec, &auth.AccessClaims{UserID: admin.ID, IsAdmin: true})
	w := doJSON(t, r, http.MethodPatch, "/api/v1/users/"+target.ID, map[string]any{"package_id": pkgID})
	require.Equal(t, http.StatusOK, w.Code)
	expectReconcile(t, rec, false)
}

// TestUsers_Patch_PackageRemove_ReconcilesOnce: clearing the package (empty
// string) is a change, so it reconciles once — completing the assign/replace/
// remove parity matrix.
func TestUsers_Patch_PackageRemove_ReconcilesOnce(t *testing.T) {
	repo := newMemUserRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	pkgID := ids.NewULID()
	target := makeUser(t, "u@example.com", false, "password01")
	target.PackageID = &pkgID
	repo.seed(admin)
	repo.seed(target)
	pkgRepo := newMemPackageRepo()
	pkgRepo.seed(&models.HostingPackage{ID: pkgID, Name: "ToRemove"})
	rec := newFakeReconciler()

	r := buildRouterWithReconciler(repo, pkgRepo, rec, &auth.AccessClaims{UserID: admin.ID, IsAdmin: true})
	w := doJSON(t, r, http.MethodPatch, "/api/v1/users/"+target.ID, map[string]any{"package_id": ""})
	require.Equal(t, http.StatusOK, w.Code)

	var out models.User
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	require.Nil(t, out.PackageID)
	expectReconcile(t, rec, true)
}

// TestUsers_Patch_EmailOnly_NoReconcile: a non-package field change never
// touches the limits reconciler.
func TestUsers_Patch_EmailOnly_NoReconcile(t *testing.T) {
	repo := newMemUserRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	target := makeUser(t, "u@example.com", false, "password01")
	repo.seed(admin)
	repo.seed(target)
	pkgRepo := newMemPackageRepo()
	rec := newFakeReconciler()

	r := buildRouterWithReconciler(repo, pkgRepo, rec, &auth.AccessClaims{UserID: admin.ID, IsAdmin: true})
	w := doJSON(t, r, http.MethodPatch, "/api/v1/users/"+target.ID, map[string]any{"email": "new@example.com"})
	require.Equal(t, http.StatusOK, w.Code)
	expectReconcile(t, rec, false)
}

// TestUsers_Patch_PackageAndEmailTogether: a single PATCH carrying both a
// package change and an unrelated field persists BOTH — the package change
// routes through SetPackage, whose Update writes the whole row (including the
// email applied before it).
func TestUsers_Patch_PackageAndEmailTogether(t *testing.T) {
	repo := newMemUserRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	target := makeUser(t, "u@example.com", false, "password01")
	repo.seed(admin)
	repo.seed(target)
	pkgRepo := newMemPackageRepo()
	pkg := &models.HostingPackage{ID: ids.NewULID(), Name: "Premium"}
	pkgRepo.seed(pkg)
	rec := newFakeReconciler()

	r := buildRouterWithReconciler(repo, pkgRepo, rec, &auth.AccessClaims{UserID: admin.ID, IsAdmin: true})
	w := doJSON(t, r, http.MethodPatch, "/api/v1/users/"+target.ID, map[string]any{
		"email":      "moved@example.com",
		"package_id": pkg.ID,
	})
	require.Equal(t, http.StatusOK, w.Code)

	var out models.User
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	require.NotNil(t, out.PackageID)
	assert.Equal(t, pkg.ID, *out.PackageID)
	assert.Equal(t, "moved@example.com", out.Email)
	expectReconcile(t, rec, true)
}
