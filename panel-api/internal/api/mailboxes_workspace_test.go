package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// TestListWorkspace_ForcesOwnerScopeFromClaims: GET /me/mailboxes scopes the
// directory query to the caller's own user id (from the session), parses the
// page params, and echoes the paginated envelope.
func TestListWorkspace_ForcesOwnerScopeFromClaims(t *testing.T) {
	fake := &dirCaptureMbxRepo{
		rows: []repository.MailboxWithDomain{{
			Mailbox:    models.Mailbox{ID: "mb1", EmailCached: "alice@example.com"},
			DomainName: "example.com", OwnerUserID: "u1", UserUsername: "alice",
		}},
		total: 42,
	}
	h := &mailboxHandler{cfg: MailboxHandlerConfig{Mailboxes: fake}}
	c, w := newAdminMailboxTestCtx("/me/mailboxes?page=2&page_size=10&q=ali&sort=domain&order=desc")
	ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1", IsAdmin: false})

	h.listWorkspace(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "u1", fake.gotOwner, "owner scope must come from the session")
	require.Equal(t, 10, fake.gotOpts.Limit)
	require.Equal(t, 10, fake.gotOpts.Offset) // (2-1)*10
	require.Equal(t, "ali", fake.gotOpts.Search)
	require.Equal(t, "domain", fake.gotOpts.Sort)
	require.Equal(t, "desc", fake.gotOpts.Order)

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.EqualValues(t, 42, body["total"])
	require.EqualValues(t, 2, body["page"])
	require.EqualValues(t, 10, body["page_size"])
	require.Len(t, body["data"], 1)

	// The row must not leak owner columns — a self-scoped list has no use for them.
	row := body["data"].([]any)[0].(map[string]any)
	require.Equal(t, "example.com", row["domain_name"])
	_, hasOwner := row["owner_user_id"]
	require.False(t, hasOwner, "workspace rows must not carry owner_user_id")
}

// TestListWorkspace_IgnoresClientOwnerParam is the isolation guard: a tenant
// cannot widen the query to another user's mailboxes by passing ?user_id. The
// owner forwarded to the repo stays the session's user id, never the parameter.
// Falsify by making listWorkspace read c.Query("user_id") → gotOwner becomes u2.
func TestListWorkspace_IgnoresClientOwnerParam(t *testing.T) {
	fake := &dirCaptureMbxRepo{}
	h := &mailboxHandler{cfg: MailboxHandlerConfig{Mailboxes: fake}}
	c, w := newAdminMailboxTestCtx("/me/mailboxes?user_id=u2&owner=u2")
	ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1", IsAdmin: false})

	h.listWorkspace(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "u1", fake.gotOwner, "a client-supplied user_id must never widen the owner scope")
}

// TestListWorkspace_Unauthenticated: without a session the handler returns 401
// and never touches the repo.
func TestListWorkspace_Unauthenticated(t *testing.T) {
	fake := &dirCaptureMbxRepo{}
	h := &mailboxHandler{cfg: MailboxHandlerConfig{Mailboxes: fake}}
	c, w := newAdminMailboxTestCtx("/me/mailboxes")
	// No SetClaims.

	h.listWorkspace(c)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Empty(t, fake.gotOwner)
	require.Zero(t, fake.gotOpts.Limit)
}

// TestListWorkspace_AlwaysPaginated: unlike listAllAdmin, the workspace list is
// always bounded (the tab is its only consumer), so even with no page params
// the repo gets a bounded read and the envelope carries page/page_size — no
// unbounded response can silently truncate at the client.
func TestListWorkspace_AlwaysPaginated(t *testing.T) {
	fake := &dirCaptureMbxRepo{total: 3}
	h := &mailboxHandler{cfg: MailboxHandlerConfig{Mailboxes: fake}}
	c, w := newAdminMailboxTestCtx("/me/mailboxes")
	ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1"})

	h.listWorkspace(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, defaultMailboxesPageSize, fake.gotOpts.Limit, "workspace read must stay bounded")
	require.Zero(t, fake.gotOpts.Offset)

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.EqualValues(t, 1, body["page"])
	require.EqualValues(t, defaultMailboxesPageSize, body["page_size"])
}
