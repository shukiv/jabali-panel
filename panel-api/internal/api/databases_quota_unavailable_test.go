package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dbops"
)

// A database create whose package limits can't be loaded answers 503, not a
// generic 500 and not a create.
func TestDbopsRESTError_QuotaUnavailableIs503(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	dbopsRESTError(c, fmt.Errorf("%w: db down", dbops.ErrQuotaUnavailable))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "quota_unavailable") {
		t.Fatalf("want 503 quota_unavailable, got %d: %s", w.Code, w.Body.String())
	}
}
