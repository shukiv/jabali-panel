package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type mgGroupsFake struct {
	repository.MailGroupRepository
	group *models.MailGroup
}

func (f *mgGroupsFake) FindByID(context.Context, string) (*models.MailGroup, error) {
	return f.group, nil
}
func (f *mgGroupsFake) ListMemberEmails(context.Context, string) ([]string, error) { return nil, nil }
func (f *mgGroupsFake) SetMembers(context.Context, string, []string) error         { return nil }
func (f *mgGroupsFake) AddMember(context.Context, string, string) error            { return nil }
func (f *mgGroupsFake) RemoveMember(context.Context, string, string) error         { return nil }

type mgMbFake struct {
	repository.MailboxRepository
	byID map[string]*models.Mailbox
}

func (f *mgMbFake) FindByID(_ context.Context, id string) (*models.Mailbox, error) {
	return f.byID[id], nil
}

func mgRouter(t *testing.T, kind string, ag *srAgentFake) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "user1", IsAdmin: false})
		c.Next()
	})
	groups := &mgGroupsFake{group: &models.MailGroup{ID: "grp1", DomainID: "dom1", EmailCached: "team@example.org", GroupKind: kind}}
	mb := &mgMbFake{byID: map[string]*models.Mailbox{"mb1": {ID: "mb1", DomainID: "dom1", EmailCached: "alice@example.org"}}}
	dom := &srDomFake{dom: &models.Domain{ID: "dom1", UserID: "user1", Name: "example.org"}}
	RegisterMailGroupRoutes(v1, MailGroupHandlerConfig{Groups: groups, Mailboxes: mb, Domains: dom, Agent: ag})
	return r
}

// GH #235: a distribution group is a mailing list — setMembers must NOT project
// memberGroupIds (no shared collections); a resource group must.
func TestSetMembers_DistributionSkipsMemberGroupIds(t *testing.T) {
	ag := &srAgentFake{}
	r := mgRouter(t, "distribution", ag)
	w := do(t, r, "PUT", "/api/v1/mailgroups/grp1/members", map[string]any{"mailbox_ids": []string{"mb1"}})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NotContains(t, ag.calls, "mailgroup.members_set", "distribution group must not project membership")
}

func TestSetMembers_ResourceProjectsMemberGroupIds(t *testing.T) {
	ag := &srAgentFake{}
	r := mgRouter(t, "resource", ag)
	w := do(t, r, "PUT", "/api/v1/mailgroups/grp1/members", map[string]any{"mailbox_ids": []string{"mb1"}})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, ag.calls, "mailgroup.members_set", "resource group must project membership")
}

// GH #238: removeMember drops one mailbox. Distribution groups don't project to
// the agent; resource (shared) groups do, with the mailbox in remove_emails.
func TestRemoveMember_DistributionNoProjection(t *testing.T) {
	ag := &srAgentFake{}
	r := mgRouter(t, "distribution", ag)
	w := do(t, r, "DELETE", "/api/v1/mailgroups/grp1/members/mb1", nil)
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	require.NotContains(t, ag.calls, "mailgroup.members_set")
}

func TestRemoveMember_ResourceProjectsRemoval(t *testing.T) {
	ag := &srAgentFake{}
	r := mgRouter(t, "resource", ag)
	w := do(t, r, "DELETE", "/api/v1/mailgroups/grp1/members/mb1", nil)
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	require.Contains(t, ag.calls, "mailgroup.members_set")
}
