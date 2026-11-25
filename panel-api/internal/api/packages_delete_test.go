package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type deleteErrPackageRepo struct {
	mockPackageRepo
	err error
}

func (m *deleteErrPackageRepo) Delete(context.Context, string) error { return m.err }

// A package that users are on is not deleted: 409 package_in_use with the
// count, so the admin knows to move them first.
func TestPackageDelete_RefusedWhileUsersAreOnIt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, c := range []struct {
		name     string
		err      error
		code     int
		apiError string
	}{
		{"in use", &repository.PackageInUseError{Users: 3}, http.StatusConflict, "package_in_use"},
		{"missing", repository.ErrNotFound, http.StatusNotFound, "not_found"},
		{"deleted", nil, http.StatusNoContent, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := &packageHandler{cfg: PackageHandlerConfig{Repo: &deleteErrPackageRepo{err: c.err}}}
			r := gin.New()
			r.DELETE("/packages/:id", h.delete)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/packages/01HXPKG0000000000000000000", nil))
			require.Equal(t, c.code, rec.Code, rec.Body.String())
			if c.apiError == "" {
				return
			}
			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			require.Equal(t, c.apiError, body["error"])
			if c.code == http.StatusConflict {
				require.EqualValues(t, 3, body["users"])
				require.Contains(t, body["detail"], "move them to another package first")
			}
		})
	}
}
