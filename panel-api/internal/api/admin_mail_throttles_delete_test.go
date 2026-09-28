package api

import (
	"context"
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

func (r *delThrottleRepo) FindByID(context.Context, string) (*models.MailOutboundPolicy, error) {
	return r.row, nil
}
func (r *delThrottleRepo) Delete(context.Context, string) error {
	r.deleted = true
	return nil
}

type recordingThrottleClient struct{ deleted []string }

func (c *recordingThrottleClient) Delete(_ context.Context, _ string, id string) error {
	c.deleted = append(c.deleted, id)
	return nil
}

// A row with both caps owns two Stalwart throttles. Deleting the row must
// remove both: once the row is gone the reconciler can no longer find the
// daily one, and before this fix it kept capping that sender's mail with no
// row left in the panel to show or remove it.
func TestMailThrottleDelete_RemovesHourlyAndDailyThrottles(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &delThrottleRepo{row: &models.MailOutboundPolicy{
		ID: "p1", Scope: models.OutboundScopeGlobal, MaxPerHour: 100, MaxPerDay: 1000,
		Enabled: true, StalwartID: "hourly-1", StalwartIDDaily: "daily-1",
	}}
	client := &recordingThrottleClient{}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "adm", IsAdmin: true})
		c.Next()
	})
	RegisterAdminMailThrottlesRoutes(r.Group(""), AdminMailThrottlesHandlerConfig{Policies: repo, ThrottleClient: client})

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
