package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type delThrottleRepo struct {
	repository.MailOutboundPolicyRepository
	row     *models.MailOutboundPolicy
	deleted bool
}

func (r *delThrottleRepo) Update(_ context.Context, p *models.MailOutboundPolicy) error {
	r.row.Enabled = p.Enabled
	return nil
}

func (r *delThrottleRepo) FindByID(context.Context, string) (*models.MailOutboundPolicy, error) {
	return r.row, nil
}
func (r *delThrottleRepo) Delete(context.Context, string) error {
	r.deleted = true
	return nil
}

type recordingThrottleClient struct {
	deleted []string
	failOn  string
}

func (c *recordingThrottleClient) Delete(_ context.Context, id string) error {
	if id == c.failOn {
		return errors.New("stalwart-cli delete: connection refused")
	}
	c.deleted = append(c.deleted, id)
	return nil
}

func throttleDeleteRouter(repo *delThrottleRepo, client *recordingThrottleClient) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "adm", IsAdmin: true})
		c.Next()
	})
	RegisterAdminMailThrottlesRoutes(r.Group(""), AdminMailThrottlesHandlerConfig{Policies: repo, ThrottleClient: client})
	return r
}

// A row with both caps owns two Stalwart throttles. Deleting the row must
// remove both: once the row is gone the reconciler can no longer find the
// daily one, and before this fix it kept capping that sender's mail with no
// row left in the panel to show or remove it.
func TestMailThrottleDelete_RemovesHourlyAndDailyThrottles(t *testing.T) {
	repo := &delThrottleRepo{row: &models.MailOutboundPolicy{
		ID: "p1", Scope: models.OutboundScopeGlobal, MaxPerHour: 100, MaxPerDay: 1000,
		Enabled: true, StalwartID: "hourly-1", StalwartIDDaily: "daily-1",
	}}
	client := &recordingThrottleClient{}
	r := throttleDeleteRouter(repo, client)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/admin/mail/throttles/p1", nil))

	if w.Code != http.StatusNoContent {
		t.Fatalf("status %d, want 204", w.Code)
	}
	if !repo.deleted {
		t.Fatal("the row was not deleted")
	}
	sort.Strings(client.deleted)
	if len(client.deleted) != 2 || client.deleted[0] != "daily-1" || client.deleted[1] != "hourly-1" {
		t.Fatalf("Stalwart deletes = %v, want [daily-1 hourly-1]", client.deleted)
	}
}

// If Stalwart cannot remove a throttle, deleting the row anyway would leave
// a cap on the sender that the panel no longer shows (the old handler ignored
// the error). The row stays, disabled, so the reconciler keeps retrying the
// removal and the admin sees why.
func TestMailThrottleDelete_KeepsTheRowWhenStalwartCannotRemoveAThrottle(t *testing.T) {
	repo := &delThrottleRepo{row: &models.MailOutboundPolicy{
		ID: "p1", Scope: models.OutboundScopeGlobal, MaxPerHour: 100, MaxPerDay: 1000,
		Enabled: true, StalwartID: "hourly-1", StalwartIDDaily: "daily-1",
	}}
	client := &recordingThrottleClient{failOn: "daily-1"}
	r := throttleDeleteRouter(repo, client)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/admin/mail/throttles/p1", nil))

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502; body %s", w.Code, w.Body.String())
	}
	if repo.deleted {
		t.Fatal("the row was deleted although its daily throttle is still in Stalwart")
	}
	if repo.row.Enabled {
		t.Fatal("the row was left enabled; the reconciler would put the hourly throttle back")
	}
}
