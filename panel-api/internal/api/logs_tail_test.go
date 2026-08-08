package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// fakeDomains implements DomainRepository by embedding the interface (so every
// method exists) and overriding only FindByID — the sole method tail uses.
type fakeDomains struct {
	repository.DomainRepository
	d   *models.Domain
	err error
}

func (f fakeDomains) FindByID(_ context.Context, _ string) (*models.Domain, error) {
	return f.d, f.err
}

func tailRouter(t *testing.T, userID string, isAdmin bool, domains repository.DomainRepository) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: userID, IsAdmin: isAdmin})
		c.Next()
	})
	RegisterLogRoutes(v1, LogHandlerConfig{Domains: domains})
	return r
}

func TestLogTail(t *testing.T) {
	owner := "01OWNER0000000000000000000"
	dom := &models.Domain{ID: "01DOMAIN000000000000000000", UserID: owner, Name: "example.com"}

	cases := []struct {
		name    string
		user    string
		isAdmin bool
		domains repository.DomainRepository
		query   string
		want    int
	}{
		{"bad log_type", owner, false, fakeDomains{d: dom}, "?domain_id=x&log_type=goaccess", http.StatusBadRequest},
		{"missing domain_id", owner, false, fakeDomains{d: dom}, "?log_type=access", http.StatusBadRequest},
		{"domain not found", owner, false, fakeDomains{err: repository.ErrNotFound}, "?domain_id=x&log_type=access", http.StatusNotFound},
		{"cross-user is 404", "01OTHER0000000000000000000", false, fakeDomains{d: dom}, "?domain_id=x&log_type=access", http.StatusNotFound},
		// Owner: ownership passes; the log file doesn't exist in the test env, so
		// the handler returns 200 with an empty snapshot rather than a 500.
		{"owner gets 200 empty", owner, false, fakeDomains{d: dom}, "?domain_id=x&log_type=access&lines=10", http.StatusOK},
		{"admin any domain 200", "01ADMIN0000000000000000000", true, fakeDomains{d: dom}, "?domain_id=x&log_type=error", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := tailRouter(t, tc.user, tc.isAdmin, tc.domains)
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/logs/tail"+tc.query, nil)
			r.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d (body: %s)", w.Code, tc.want, strings.TrimSpace(w.Body.String()))
			}
		})
	}
}
