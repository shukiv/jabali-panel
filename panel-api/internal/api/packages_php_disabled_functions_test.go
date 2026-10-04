package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1701: a hosting package's disabled PHP functions. The list is the source
// of truth; php_exec_enabled is derived from it and written with it.

// poolReapplyRecorder records per-user pool reapplies from the package fan-out.
type poolReapplyRecorder struct {
	fakeWebmailPkgReconciler
	reapplied chan string
}

func (r *poolReapplyRecorder) ReapplyPHPPoolForUser(_ context.Context, userID string) error {
	r.reapplied <- userID
	return nil
}

type pkgMembersRepo struct {
	mockUserRepo
	list []models.User
}

func (r *pkgMembersRepo) List(context.Context, repository.ListOptions) ([]models.User, int64, error) {
	return r.list, int64(len(r.list)), nil
}

func newDisabledFnsHandler(pkg *models.HostingPackage) (*packageHandler, *poolReapplyRecorder) {
	pkgID := pkg.ID
	rec := &poolReapplyRecorder{reapplied: make(chan string, 4)}
	users := &pkgMembersRepo{list: []models.User{{ID: "u1", PackageID: &pkgID}}}
	h := &packageHandler{cfg: PackageHandlerConfig{
		Repo:       &mockPackageRepo{packages: map[string]*models.HostingPackage{pkgID: pkg}},
		Users:      users,
		Reconciler: rec,
	}}
	return h, rec
}

func expectReapply(t *testing.T, rec *poolReapplyRecorder, want bool) {
	t.Helper()
	select {
	case <-rec.reapplied:
		if !want {
			t.Fatal("pools were re-rendered although the effective list did not change")
		}
	case <-time.After(300 * time.Millisecond):
		if want {
			t.Fatal("expected the package's pools to be re-rendered")
		}
	}
}

func TestPackageCreate_PHPDisabledFunctions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &packageHandler{cfg: PackageHandlerConfig{Repo: &mockPackageRepo{}}}

	got := createPackageForTest(t, h, `{"name":"default"}`)
	require.Nil(t, got["php_disabled_functions"], "omitted = the lockdown default")
	require.Equal(t, false, got["php_exec_enabled"])

	// The legacy toggle alone still works: nothing disabled.
	got = createPackageForTest(t, h, `{"name":"legacy","php_exec_enabled":true}`)
	require.Equal(t, "", got["php_disabled_functions"])
	require.Equal(t, true, got["php_exec_enabled"])

	// A list is canonicalised, and wins over the toggle.
	got = createPackageForTest(t, h, `{"name":"list","php_exec_enabled":true,"php_disabled_functions":"MAIL exec"}`)
	require.Equal(t, "exec,mail", got["php_disabled_functions"])
	require.Equal(t, false, got["php_exec_enabled"], "exec is still disabled")

	// Exactly the lockdown is stored as the default.
	got = createPackageForTest(t, h, `{"name":"lock","php_disabled_functions":"`+strings.Join(models.PHPLockdownFunctions, ",")+`"}`)
	require.Nil(t, got["php_disabled_functions"])
}

func TestPackageCreate_PHPDisabledFunctionsRejectsBadName(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &packageHandler{cfg: PackageHandlerConfig{Repo: &mockPackageRepo{}}}
	for _, body := range []string{
		`{"name":"x","php_disabled_functions":"exec;id"}`,
		`{"name":"x","php_disabled_functions":"exec\nuser = root"}`,
	} {
		rec := createPackageRecorder(t, h, body)
		require.Equal(t, http.StatusBadRequest, rec.Code, body)
		require.Contains(t, rec.Body.String(), "invalid_php_disabled_functions")
	}
}

func TestPackageUpdate_PHPDisabledFunctions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pkg := &models.HostingPackage{ID: "p1", Name: "p1"}
	h, rec := newDisabledFnsHandler(pkg)

	// Allow shell_exec only: a list edit re-renders the pools.
	only := "exec,passthru,system,proc_open,popen,pcntl_exec,pcntl_fork,proc_nice,dl"
	resp := updatePackageForTest(t, h, "p1", `{"php_disabled_functions":"`+only+`"}`)
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	require.Equal(t, only, *pkg.PHPDisabledFunctions)
	require.False(t, pkg.PHPExecEnabled)
	expectReapply(t, rec, true)

	// The same list again: no re-render.
	resp = updatePackageForTest(t, h, "p1", `{"php_disabled_functions":"`+only+`"}`)
	require.Equal(t, http.StatusOK, resp.Code)
	expectReapply(t, rec, false)

	// The legacy toggle allows the rest of the lockdown and keeps extras.
	pkg.PHPDisabledFunctions = strp("exec,mail")
	resp = updatePackageForTest(t, h, "p1", `{"php_exec_enabled":true}`)
	require.Equal(t, http.StatusOK, resp.Code)
	require.Equal(t, "mail", *pkg.PHPDisabledFunctions)
	require.True(t, pkg.PHPExecEnabled)
	expectReapply(t, rec, true)

	// When both are sent the list wins.
	resp = updatePackageForTest(t, h, "p1", `{"php_exec_enabled":true,"php_disabled_functions":"exec"}`)
	require.Equal(t, http.StatusOK, resp.Code)
	require.Equal(t, "exec", *pkg.PHPDisabledFunctions)
	require.False(t, pkg.PHPExecEnabled)
	expectReapply(t, rec, true)

	// null resets to the lockdown default.
	resp = updatePackageForTest(t, h, "p1", `{"php_disabled_functions":null}`)
	require.Equal(t, http.StatusOK, resp.Code)
	require.Nil(t, pkg.PHPDisabledFunctions)
	require.False(t, pkg.PHPExecEnabled)
	expectReapply(t, rec, true)

	// A PATCH that does not mention either field leaves the list alone.
	resp = updatePackageForTest(t, h, "p1", `{"name":"renamed"}`)
	require.Equal(t, http.StatusOK, resp.Code)
	require.Nil(t, pkg.PHPDisabledFunctions)
	expectReapply(t, rec, false)

	for _, body := range []string{`{"php_disabled_functions":42}`, `{"php_disabled_functions":"exec;id"}`} {
		resp = updatePackageForTest(t, h, "p1", body)
		require.Equal(t, http.StatusBadRequest, resp.Code, body)
	}
}

// A package saved by an older binary (php_exec_enabled set, no list) keeps its
// opt-out: an unrelated edit does not re-render or relock it.
func TestPackageUpdate_LegacyOptOutRowUntouched(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pkg := &models.HostingPackage{ID: "p1", Name: "p1", PHPExecEnabled: true}
	h, rec := newDisabledFnsHandler(pkg)

	resp := updatePackageForTest(t, h, "p1", `{"name":"renamed"}`)
	require.Equal(t, http.StatusOK, resp.Code)
	require.True(t, pkg.PHPExecEnabled)
	require.Empty(t, models.EffectivePHPDisabledFunctions(pkg))
	expectReapply(t, rec, false)
}

func createPackageRecorder(t *testing.T, h *packageHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/packages", bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	h.create(c)
	return rec
}
