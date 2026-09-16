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
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// mgMembershipsFake records the user id the bulk handler scopes by and returns
// a fixed edge set. It embeds the interface so only the one method matters.
type mgMembershipsFake struct {
	repository.MailGroupRepository
	gotUserID string
	rows      []repository.MailboxGroupMembership
}

func (f *mgMembershipsFake) ListMembershipsByUserID(_ context.Context, userID string) ([]repository.MailboxGroupMembership, error) {
	f.gotUserID = userID
	return f.rows, nil
}

func membershipsBulkRouter(t *testing.T, claims *auth.AccessClaims, groups repository.MailGroupRepository) *gin.Engine {
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
	RegisterMailGroupRoutes(v1, MailGroupHandlerConfig{Groups: groups})
	return r
}

// JAB-370 Selection: the owner-scoped bulk memberships endpoint takes the owner
// from the Kratos session claims and never widens — even for an admin. The tab's
// mailbox rows come from /me/mailboxes (owner-scoped for admins too), so the
// membership projection must match, or the client-side mailbox->groups join
// receives another tenant's group data. This mirrors listWorkspace's isolation
// (there is NO admin cross-tenant branch, unlike the forwarders bulk list).
func TestListMembershipsAll_ForcesOwnerScopeFromClaims(t *testing.T) {
	fake := &mgMembershipsFake{rows: []repository.MailboxGroupMembership{
		{MailboxID: "mb1", GroupID: "g1", GroupName: "Team", GroupEmail: "team@one.test"},
		{MailboxID: "mb1", GroupID: "g2", GroupName: "Ops", GroupEmail: "ops@one.test"},
		{MailboxID: "mb2", GroupID: "g1", GroupName: "Team", GroupEmail: "team@one.test"},
	}}
	// Admin caller — must STILL be scoped to their own user id, never widened.
	r := membershipsBulkRouter(t, &auth.AccessClaims{UserID: "owner1", IsAdmin: true}, fake)

	w := do(t, r, "GET", "/api/v1/mail/mailbox-group-memberships", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "owner1", fake.gotUserID,
		"handler must scope by the session owner id, never widen for admin")

	var resp struct {
		Data map[string][]repository.MailboxGroupMembership `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data["mb1"], 2, "edges grouped per mailbox")
	require.Len(t, resp.Data["mb2"], 1)
}

// A caller with no session claims is rejected, not served an empty/global set.
func TestListMembershipsAll_RejectsMissingClaims(t *testing.T) {
	fake := &mgMembershipsFake{}
	r := membershipsBulkRouter(t, nil, fake)

	w := do(t, r, "GET", "/api/v1/mail/mailbox-group-memberships", nil)
	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	require.Empty(t, fake.gotUserID, "repo must not be queried without an owner")
}
