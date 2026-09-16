package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// arBulkFake records the user id the bulk autoresponder handler scopes by and
// returns a fixed row set. It embeds the interface so only the one method
// matters (the bulk route touches no other repo).
type arBulkFake struct {
	repository.EmailAutoresponderRepository
	gotUserID string
	rows      []models.EmailAutoresponder
}

func (f *arBulkFake) ListByUserID(_ context.Context, userID string) ([]models.EmailAutoresponder, error) {
	f.gotUserID = userID
	return f.rows, nil
}

func autorespondersBulkRouter(t *testing.T, claims *auth.AccessClaims, ars repository.EmailAutoresponderRepository) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		if claims != nil {
			ginctx.SetClaims(c, claims)
		}
		c.Next()
	})
	RegisterMailboxAutoresponderRoutes(v1, MailboxAutoresponderHandlerConfig{Autoresponders: ars})
	return r
}

// JAB-370 Selection: the owner-scoped bulk autoresponders endpoint takes the
// owner from the Kratos session claims and never widens — even for an admin.
// The tab's mailbox rows come from /me/mailboxes (owner-scoped for admins too),
// so the autoresponder projection must match, or the client-side
// mailbox->autoresponder join receives another tenant's rows. This mirrors
// listWorkspace's isolation (there is NO admin cross-tenant branch, unlike the
// per-domain listByDomain handler).
func TestListAutorespondersAll_ForcesOwnerScopeFromClaims(t *testing.T) {
	fake := &arBulkFake{rows: []models.EmailAutoresponder{
		{MailboxID: "mb1", Enabled: true},
		{MailboxID: "mb2", Enabled: false},
	}}
	// Admin caller — must STILL be scoped to their own user id, never widened.
	r := autorespondersBulkRouter(t, &auth.AccessClaims{UserID: "owner1", IsAdmin: true}, fake)

	w := do(t, r, "GET", "/api/v1/mail/autoresponders", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "owner1", fake.gotUserID,
		"handler must scope by the session owner id, never widen for admin")

	var resp struct {
		Data map[string]struct {
			MailboxID string `json:"mailbox_id"`
			Enabled   bool   `json:"enabled"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 2, "rows keyed per mailbox")
	require.True(t, resp.Data["mb1"].Enabled)
	require.False(t, resp.Data["mb2"].Enabled)
}

// A caller with no session claims is rejected, not served an empty/global set.
func TestListAutorespondersAll_RejectsMissingClaims(t *testing.T) {
	fake := &arBulkFake{}
	r := autorespondersBulkRouter(t, nil, fake)

	w := do(t, r, "GET", "/api/v1/mail/autoresponders", nil)
	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	require.Empty(t, fake.gotUserID, "repo must not be queried without an owner")
}
