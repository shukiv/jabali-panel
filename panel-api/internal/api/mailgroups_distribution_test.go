package api

import (
	"context"
	"encoding/json"
	"fmt"
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
	group       *models.MailGroup
	memberIDs   []string // current mail_group_members rows
	deliverable []string // members that can receive mail

	setMembersCalled    bool
	addMemberCalled     bool
	internalOnlyWritten bool
	createdGroup        *models.MailGroup
}

func (f *mgGroupsFake) FindByID(context.Context, string) (*models.MailGroup, error) {
	return f.group, nil
}
func (f *mgGroupsFake) ListMemberEmails(context.Context, string) ([]string, error) { return nil, nil }
func (f *mgGroupsFake) ListMemberMailboxIDs(context.Context, string) ([]string, error) {
	return f.memberIDs, nil
}
func (f *mgGroupsFake) ListDeliverableMemberEmails(context.Context, string) ([]string, error) {
	return f.deliverable, nil
}
func (f *mgGroupsFake) SetMembers(context.Context, string, []string) error {
	f.setMembersCalled = true
	return nil
}
func (f *mgGroupsFake) AddMember(context.Context, string, string) error {
	f.addMemberCalled = true
	return nil
}
func (f *mgGroupsFake) RemoveMember(context.Context, string, string) error { return nil }
func (f *mgGroupsFake) UpdateInternalOnly(_ context.Context, _ string, v bool) error {
	f.internalOnlyWritten = true
	f.group.InternalOnly = v
	return nil
}
func (f *mgGroupsFake) ExistsByDomainAndLocalPart(context.Context, string, string) (bool, error) {
	return false, nil
}
func (f *mgGroupsFake) Create(_ context.Context, g *models.MailGroup) error {
	f.createdGroup = g
	return nil
}

type mgMbFake struct {
	repository.MailboxRepository
	byID map[string]*models.Mailbox
}

func (f *mgMbFake) FindByID(_ context.Context, id string) (*models.Mailbox, error) {
	mb, ok := f.byID[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return mb, nil
}
func (f *mgMbFake) ExistsByDomainAndLocalPart(context.Context, string, string) (bool, error) {
	return false, nil
}

// mgAgentFake records every agent command with its params.
type mgAgentFake struct {
	calls  []string
	params []map[string]any
}

func (a *mgAgentFake) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	a.calls = append(a.calls, cmd)
	b, _ := json.Marshal(params)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	a.params = append(a.params, m)
	return json.RawMessage(`{"ok":true}`), nil
}

// lastParams returns the params of the last call of cmd, or nil.
func (a *mgAgentFake) lastParams(cmd string) map[string]any {
	for i := len(a.calls) - 1; i >= 0; i-- {
		if a.calls[i] == cmd {
			return a.params[i]
		}
	}
	return nil
}

type mgFixture struct {
	groups *mgGroupsFake
	ag     *mgAgentFake
	router *gin.Engine
}

// newMGFixture wires the mail group routes over fakes. mailboxes mb1..mbN
// exist in the group's domain.
func newMGFixture(t *testing.T, kind string, internalOnly bool, mailboxes int) *mgFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "user1", IsAdmin: false})
		c.Next()
	})
	groups := &mgGroupsFake{group: &models.MailGroup{
		ID: "grp1", DomainID: "dom1", EmailCached: "team@example.org", GroupKind: kind, InternalOnly: internalOnly,
	}}
	mb := &mgMbFake{byID: map[string]*models.Mailbox{}}
	for i := 1; i <= mailboxes; i++ {
		id := fmt.Sprintf("mb%d", i)
		mb.byID[id] = &models.Mailbox{ID: id, DomainID: "dom1", EmailCached: fmt.Sprintf("user%d@example.org", i)}
	}
	dom := &srDomFake{dom: &models.Domain{ID: "dom1", UserID: "user1", Name: "example.org", EmailEnabled: true}}
	ag := &mgAgentFake{}
	RegisterMailGroupRoutes(v1, MailGroupHandlerConfig{Groups: groups, Mailboxes: mb, Domains: dom, Agent: ag})
	return &mgFixture{groups: groups, ag: ag, router: r}
}

func mailboxIDs(n int) []string {
	out := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, fmt.Sprintf("mb%d", i))
	}
	return out
}

// requireDistributionApply asserts a mailgroup.apply carrying the group kind
// and the deliverable member set was dispatched — the projection that makes a
// distribution list fan out (GH #1818) — and no memberGroupIds projection.
func requireDistributionApply(t *testing.T, ag *mgAgentFake, wantMembers []any) {
	t.Helper()
	p := ag.lastParams("mailgroup.apply")
	require.NotNil(t, p, "distribution change must re-apply the list; calls=%v", ag.calls)
	require.Equal(t, "distribution", p["group_kind"])
	require.Equal(t, wantMembers, p["member_emails"])
	require.NotContains(t, ag.calls, "mailgroup.members_set", "a distribution list must not link members to a shared inbox")
}

func TestSetMembers_DistributionAppliesMemberList(t *testing.T) {
	fx := newMGFixture(t, "distribution", false, 1)
	fx.groups.deliverable = []string{"user1@example.org"}
	w := do(t, fx.router, "PUT", "/api/v1/mailgroups/grp1/members", map[string]any{"mailbox_ids": []string{"mb1"}})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	requireDistributionApply(t, fx.ag, []any{"user1@example.org"})
}

func TestSetMembers_ResourceProjectsMemberGroupIds(t *testing.T) {
	fx := newMGFixture(t, "resource", false, 1)
	w := do(t, fx.router, "PUT", "/api/v1/mailgroups/grp1/members", map[string]any{"mailbox_ids": []string{"mb1"}})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, fx.ag.calls, "mailgroup.members_set", "resource group must project membership")
}

func TestAddMember_DistributionAppliesMemberList(t *testing.T) {
	fx := newMGFixture(t, "distribution", false, 1)
	fx.groups.deliverable = []string{"user1@example.org"}
	w := do(t, fx.router, "POST", "/api/v1/mailgroups/grp1/members/mb1", nil)
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	requireDistributionApply(t, fx.ag, []any{"user1@example.org"})
}

func TestRemoveMember_DistributionAppliesMemberList(t *testing.T) {
	fx := newMGFixture(t, "distribution", false, 1)
	fx.groups.deliverable = []string{}
	w := do(t, fx.router, "DELETE", "/api/v1/mailgroups/grp1/members/mb1", nil)
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	requireDistributionApply(t, fx.ag, []any{})
}

func TestRemoveMember_ResourceProjectsRemoval(t *testing.T) {
	fx := newMGFixture(t, "resource", false, 1)
	w := do(t, fx.router, "DELETE", "/api/v1/mailgroups/grp1/members/mb1", nil)
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	require.Contains(t, fx.ag.calls, "mailgroup.members_set")
}

func TestCreate_DistributionSendsKind(t *testing.T) {
	fx := newMGFixture(t, "distribution", false, 0)
	w := do(t, fx.router, "POST", "/api/v1/domains/dom1/mailgroups", map[string]any{"name": "sales", "group_kind": "distribution"})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	p := fx.ag.lastParams("mailgroup.apply")
	require.NotNil(t, p)
	require.Equal(t, "distribution", p["group_kind"])
	require.Equal(t, "sales@example.org", p["email"])
	require.Equal(t, []any{}, p["member_emails"])
}

// An internal-only distribution list delivers through one Sieve redirect per
// member and Stalwart allows 20, so the panel refuses a 21st member up front
// instead of letting members past the limit silently receive nothing.
func TestSetMembers_InternalOnlyDistributionOverCapRejected(t *testing.T) {
	fx := newMGFixture(t, "distribution", true, 21)
	w := do(t, fx.router, "PUT", "/api/v1/mailgroups/grp1/members", map[string]any{"mailbox_ids": mailboxIDs(21)})
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "too_many_members")
	require.False(t, fx.groups.setMembersCalled, "over-cap member set must not be saved")
	require.Empty(t, fx.ag.calls)
}

func TestSetMembers_InternalOnlyDistributionAtCapAllowed(t *testing.T) {
	fx := newMGFixture(t, "distribution", true, 20)
	w := do(t, fx.router, "PUT", "/api/v1/mailgroups/grp1/members", map[string]any{"mailbox_ids": mailboxIDs(20)})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.True(t, fx.groups.setMembersCalled)
}

func TestSetMembers_PlainDistributionHasNoCap(t *testing.T) {
	fx := newMGFixture(t, "distribution", false, 25)
	w := do(t, fx.router, "PUT", "/api/v1/mailgroups/grp1/members", map[string]any{"mailbox_ids": mailboxIDs(25)})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

func TestAddMember_InternalOnlyDistributionOverCapRejected(t *testing.T) {
	fx := newMGFixture(t, "distribution", true, 21)
	fx.groups.memberIDs = mailboxIDs(20)
	w := do(t, fx.router, "POST", "/api/v1/mailgroups/grp1/members/mb21", nil)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	require.False(t, fx.groups.addMemberCalled)
}

func TestAddMember_ExistingMemberAtCapAllowed(t *testing.T) {
	fx := newMGFixture(t, "distribution", true, 20)
	fx.groups.memberIDs = mailboxIDs(20)
	w := do(t, fx.router, "POST", "/api/v1/mailgroups/grp1/members/mb3", nil)
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
}

func TestUpdate_EnableInternalOnlyOverCapRejected(t *testing.T) {
	fx := newMGFixture(t, "distribution", false, 21)
	fx.groups.memberIDs = mailboxIDs(21)
	w := do(t, fx.router, "PATCH", "/api/v1/mailgroups/grp1", map[string]any{"internal_only": true})
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	require.False(t, fx.groups.internalOnlyWritten, "the toggle must not be saved")
	require.Empty(t, fx.ag.calls)
}

func TestUpdate_EnableInternalOnlyDistributionReapplies(t *testing.T) {
	fx := newMGFixture(t, "distribution", false, 2)
	fx.groups.memberIDs = mailboxIDs(2)
	fx.groups.deliverable = []string{"user1@example.org", "user2@example.org"}
	w := do(t, fx.router, "PATCH", "/api/v1/mailgroups/grp1", map[string]any{"internal_only": true})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	requireDistributionApply(t, fx.ag, []any{"user1@example.org", "user2@example.org"})
	require.Equal(t, true, fx.ag.lastParams("mailgroup.apply")["internal_only"])
}
