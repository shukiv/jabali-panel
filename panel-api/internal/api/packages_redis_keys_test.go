package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #2165: a package sets how many keys one Redis key browser call may list
// or delete, 100 to 5000. A create that omits it gets 500, what every package
// had before; 0 is not "unlimited".
func TestPackageCreate_RedisKeysPerCall(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &packageHandler{cfg: PackageHandlerConfig{Repo: &mockPackageRepo{}}}

	got := createPackageForTest(t, h, `{"name":"omit"}`)
	require.EqualValues(t, 500, got["redis_keys_per_call"], "an omitted value is the default")

	got = createPackageForTest(t, h, `{"name":"big","redis_keys_per_call":2000}`)
	require.EqualValues(t, 2000, got["redis_keys_per_call"])

	for _, v := range []string{"0", "99", "5001", "-1"} {
		rec := createPackageRecorder(t, h, `{"name":"bad","redis_keys_per_call":`+v+`}`)
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "redis_keys_per_call=%s: %s", v, rec.Body.String())
		require.True(t, strings.Contains(rec.Body.String(), "redis_keys_per_call"), rec.Body.String())
	}
}

func TestPackageUpdate_RedisKeysPerCall(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &mockPackageRepo{packages: map[string]*models.HostingPackage{
		"p1": {ID: "p1", Name: "p1", RedisKeysPerCall: 500},
	}}
	h := &packageHandler{cfg: PackageHandlerConfig{Repo: repo}}

	rec := updatePackageForTest(t, h, "p1", `{"redis_keys_per_call":3000}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 3000, repo.packages["p1"].RedisKeysPerCall)

	rec = updatePackageForTest(t, h, "p1", `{"name":"renamed"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 3000, repo.packages["p1"].RedisKeysPerCall, "a PATCH that omits it keeps it")

	for _, v := range []string{"0", "50", "9000"} {
		rec = updatePackageForTest(t, h, "p1", `{"redis_keys_per_call":`+v+`}`)
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "redis_keys_per_call=%s: %s", v, rec.Body.String())
	}
}
