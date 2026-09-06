package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// dirCaptureMbxRepo records what listAllAdmin forwards to ListDirectoryPage so
// the handler tests can assert the paginate-only-when-asked contract and the
// #483 owner scope without a real DB. Embeds the interface — only
// ListDirectoryPage is exercised.
type dirCaptureMbxRepo struct {
	repository.MailboxRepository
	gotOpts  repository.ListOptions
	gotOwner string
	rows     []repository.MailboxWithDomain
	total    int64
}

func (r *dirCaptureMbxRepo) ListDirectoryPage(_ context.Context, opts repository.ListOptions, owner string) ([]repository.MailboxWithDomain, int64, error) {
	r.gotOpts = opts
	r.gotOwner = owner
	return r.rows, r.total, nil
}

func newAdminMailboxTestCtx(target string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	return c, w
}

// TestListAllAdmin_Paginated: a request with ?page/?page_size forwards a bounded
// ListOptions and echoes page/page_size in the envelope alongside the
// authoritative server-side total.
func TestListAllAdmin_Paginated(t *testing.T) {
	fake := &dirCaptureMbxRepo{
		rows: []repository.MailboxWithDomain{{
			Mailbox:    models.Mailbox{ID: "mb1", EmailCached: "a@example.com"},
			DomainName: "example.com", OwnerUserID: "u1", UserUsername: "bob",
		}},
		total: 137,
	}
	h := &mailboxHandler{cfg: MailboxHandlerConfig{Mailboxes: fake}}
	c, w := newAdminMailboxTestCtx("/admin/mailboxes?page=3&page_size=10&q=ali&sort=domain&order=desc")

	h.listAllAdmin(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 10, fake.gotOpts.Limit)
	require.Equal(t, 20, fake.gotOpts.Offset) // (3-1)*10
	require.Equal(t, "ali", fake.gotOpts.Search)
	require.Equal(t, "domain", fake.gotOpts.Sort)
	require.Equal(t, "desc", fake.gotOpts.Order)
	require.Empty(t, fake.gotOwner)

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.EqualValues(t, 137, body["total"])
	require.EqualValues(t, 3, body["page"])
	require.EqualValues(t, 10, body["page_size"])
	require.Len(t, body["data"], 1)
}

// TestListAllAdmin_UnboundedWhenNoPageParams: with neither page nor page_size,
// the handler asks for an unbounded read (Limit 0) and omits page/page_size —
// the historical shape the admin user-overview consumes.
func TestListAllAdmin_UnboundedWhenNoPageParams(t *testing.T) {
	fake := &dirCaptureMbxRepo{total: 0}
	h := &mailboxHandler{cfg: MailboxHandlerConfig{Mailboxes: fake}}
	c, w := newAdminMailboxTestCtx("/admin/mailboxes")

	h.listAllAdmin(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.Zero(t, fake.gotOpts.Limit)
	require.Zero(t, fake.gotOpts.Offset)

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	_, hasPage := body["page"]
	_, hasSize := body["page_size"]
	require.False(t, hasPage, "unbounded response must not carry page")
	require.False(t, hasSize, "unbounded response must not carry page_size")
	require.Contains(t, body, "total")
}

// TestListAllAdmin_OwnerScope: a valid ?user_id is forwarded as the owner scope
// (#483); the read stays unbounded because no page params are present.
func TestListAllAdmin_OwnerScope(t *testing.T) {
	const ulid = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	fake := &dirCaptureMbxRepo{}
	h := &mailboxHandler{cfg: MailboxHandlerConfig{Mailboxes: fake}}
	c, w := newAdminMailboxTestCtx("/admin/mailboxes?user_id=" + ulid)

	h.listAllAdmin(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, ulid, fake.gotOwner)
	require.Zero(t, fake.gotOpts.Limit)
}

// TestListAllAdmin_InvalidOwner: a malformed ?user_id is rejected 400 before any
// repo call.
func TestListAllAdmin_InvalidOwner(t *testing.T) {
	fake := &dirCaptureMbxRepo{}
	h := &mailboxHandler{cfg: MailboxHandlerConfig{Mailboxes: fake}}
	c, w := newAdminMailboxTestCtx("/admin/mailboxes?user_id=not-a-ulid")

	h.listAllAdmin(c)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Empty(t, fake.gotOwner)
	require.Zero(t, fake.gotOpts.Limit)
}
