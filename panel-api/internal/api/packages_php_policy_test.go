package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1701: the package API stores the PHP settings policy in its canonical
// form and refuses a policy the catalog does not allow.
func TestPackageCreate_PHPSettingsPolicyIsNormalized(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &packageHandler{cfg: PackageHandlerConfig{Repo: &mockPackageRepo{}}}
	got := createPackageForTest(t, h, `{"name":"p","php_settings_policy":"{\"post_max_size\":\"admin_only\",\"memory_limit\":\"admin_only\"}"}`)
	require.Equal(t, `{"memory_limit":"admin_only","post_max_size":"admin_only"}`, got["php_settings_policy"])
}

func TestPackageCreate_RefusesAPolicyThatWeakensTheFloor(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &packageHandler{cfg: PackageHandlerConfig{Repo: &mockPackageRepo{}}}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/packages",
		bytes.NewReader([]byte(`{"name":"p","php_settings_policy":"{\"open_basedir\":\"tenant_allowed\"}"}`)))
	c.Request.Header.Set("Content-Type", "application/json")
	h.create(c)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "invalid_php_settings_policy")
}

func TestPackageUpdate_PHPSettingsPolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &mockPackageRepo{packages: map[string]*models.HostingPackage{"p1": {ID: "p1", Name: "p"}}}
	h := &packageHandler{cfg: PackageHandlerConfig{Repo: repo}}

	rec := updatePackageForTest(t, h, "p1", `{"php_settings_policy":"{\"memory_limit\":\"admin_only\"}"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, `{"memory_limit":"admin_only"}`, repo.packages["p1"].PHPSettingsPolicy)

	rec = updatePackageForTest(t, h, "p1", `{"php_settings_policy":"{\"memory_limit\":\"tenant_privileged\"}"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Equal(t, `{"memory_limit":"admin_only"}`, repo.packages["p1"].PHPSettingsPolicy, "a refused policy must not be stored")

	// Omitting the field leaves the stored policy alone.
	rec = updatePackageForTest(t, h, "p1", `{"name":"renamed"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, `{"memory_limit":"admin_only"}`, repo.packages["p1"].PHPSettingsPolicy)
}
