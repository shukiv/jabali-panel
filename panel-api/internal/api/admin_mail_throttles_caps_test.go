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

type capsThrottleRepo struct {
	repository.MailOutboundPolicyRepository
	row     *models.MailOutboundPolicy
	created int
	updated int
}

func (r *capsThrottleRepo) Create(context.Context, *models.MailOutboundPolicy) error {
	r.created++
	return nil
}

func (r *capsThrottleRepo) FindByID(context.Context, string) (*models.MailOutboundPolicy, error) {
	return r.row, nil
}

func (r *capsThrottleRepo) Update(context.Context, *models.MailOutboundPolicy) error {
	r.updated++
	return nil
}

func throttleCapsRouter(repo *capsThrottleRepo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "adm", IsAdmin: true})
		c.Next()
	})
	RegisterAdminMailThrottlesRoutes(r.Group(""), AdminMailThrottlesHandlerConfig{Policies: repo})
	return r
}

func sendThrottle(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

// Stalwart refuses a throttle over 1,000,000 messages per window
// (validationFailed, MaxValue). A row with a larger cap would be saved, shown
// as a cap in force, and never reach Stalwart.
func TestMailThrottleCaps_OverStalwartsMaximumAreRejected(t *testing.T) {
	for _, body := range []string{
		`{"scope":"global","max_per_hour":1000001,"max_per_day":0}`,
		`{"scope":"global","max_per_hour":0,"max_per_day":10000000}`,
	} {
		repo := &capsThrottleRepo{row: &models.MailOutboundPolicy{ID: "p1", Scope: models.OutboundScopeGlobal, MaxPerHour: 10, Enabled: true}}
		r := throttleCapsRouter(repo)
		if w := sendThrottle(r, http.MethodPost, "/admin/mail/throttles", body); w.Code != http.StatusBadRequest {
			t.Errorf("POST %s -> %d %s, want 400", body, w.Code, w.Body)
		}
		if w := sendThrottle(r, http.MethodPut, "/admin/mail/throttles/p1", body); w.Code != http.StatusBadRequest {
			t.Errorf("PUT %s -> %d %s, want 400", body, w.Code, w.Body)
		}
		if repo.created != 0 || repo.updated != 0 || repo.row.MaxPerHour != 10 {
			t.Errorf("%s was stored: created=%d updated=%d row=%+v", body, repo.created, repo.updated, repo.row)
		}
	}
}

func TestMailThrottleCaps_StalwartsMaximumIsAccepted(t *testing.T) {
	body := `{"scope":"global","max_per_hour":1000000,"max_per_day":1000000}`
	repo := &capsThrottleRepo{row: &models.MailOutboundPolicy{ID: "p1", Scope: models.OutboundScopeGlobal, Enabled: true}}
	r := throttleCapsRouter(repo)
	if w := sendThrottle(r, http.MethodPost, "/admin/mail/throttles", body); w.Code != http.StatusCreated {
		t.Errorf("POST -> %d %s, want 201", w.Code, w.Body)
	}
	if w := sendThrottle(r, http.MethodPut, "/admin/mail/throttles/p1", body); w.Code != http.StatusOK {
		t.Errorf("PUT -> %d %s, want 200", w.Code, w.Body)
	}
}
