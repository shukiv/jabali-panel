package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// Stalwart 1.0 caches recipient lookups: a deleted alias kept getting 250 until
// its caches were dropped (seen on 1.0.2). Every change that takes an address
// away from where its mail went asks the agent to drop them, after the change.

const flushVerb = "mail.auth_cache.flush"

func lastCall(ag *mgAgentFake) string {
	if len(ag.calls) == 0 {
		return ""
	}
	return ag.calls[len(ag.calls)-1]
}

// --- forwarders ---

func (f *fwFakeForwarders) FindByID(_ context.Context, id string) (*models.EmailForwarder, error) {
	for i := range f.rows {
		if f.rows[i].ID == id {
			return &f.rows[i], nil
		}
	}
	return nil, errors.New("not found")
}

func (f *fwFakeForwarders) Delete(_ context.Context, id string) error {
	for i := range f.rows {
		if f.rows[i].ID == id {
			f.rows = append(f.rows[:i], f.rows[i+1:]...)
			return nil
		}
	}
	return nil
}

func TestForwarderDelete_FlushesMailCaches(t *testing.T) {
	mbID, lp := "mb1", "sales"
	ag := &mgAgentFake{}
	fw := &fwFakeForwarders{rows: []models.EmailForwarder{{ID: "fw1", MailboxID: &mbID, Type: "alias", LocalPart: &lp, Enabled: true}}}
	h := &forwarderHandler{cfg: MailboxForwarderHandlerConfig{
		Mailboxes:  fwFakeMailboxes{mb: &models.Mailbox{ID: "mb1", LocalPart: "joe", DomainID: "dom1"}},
		Domains:    fwFakeDomains{dom: &models.Domain{ID: "dom1", Name: "example.com", UserID: "u1"}},
		Forwarders: fw,
		Agent:      ag,
	}}

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: "fw1"}}
	c.Request = httptest.NewRequest(http.MethodDelete, "/forwarders/fw1", nil)
	ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1", IsAdmin: true})
	h.del(c)

	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	require.Empty(t, fw.rows, "forwarder row not deleted")
	require.Equal(t, flushVerb, lastCall(ag), "calls=%v", ag.calls)
}

// --- mail groups ---

func (f *mgGroupsFake) Delete(context.Context, string) error { return nil }

func TestMailGroupRemoveMember_FlushesMailCaches(t *testing.T) {
	for _, kind := range []string{"distribution", "resource"} {
		t.Run(kind, func(t *testing.T) {
			fx := newMGFixture(t, kind, false, 1)
			w := do(t, fx.router, "DELETE", "/api/v1/mailgroups/grp1/members/mb1", nil)
			require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
			require.Equal(t, flushVerb, lastCall(fx.ag), "calls=%v", fx.ag.calls)
		})
	}
}

func TestMailGroupDelete_FlushesMailCaches(t *testing.T) {
	fx := newMGFixture(t, "distribution", false, 0)
	w := do(t, fx.router, "DELETE", "/api/v1/mailgroups/grp1", nil)
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	require.Equal(t, []string{"mailgroup.delete", flushVerb}, fx.ag.calls)
}

func TestMailGroupSetMembers_FlushesOnlyWhenAMemberLeaves(t *testing.T) {
	t.Run("member added", func(t *testing.T) {
		fx := newMGFixture(t, "resource", false, 2)
		w := do(t, fx.router, "PUT", "/api/v1/mailgroups/grp1/members", map[string]any{"mailbox_ids": []string{"mb1"}})
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.NotContains(t, fx.ag.calls, flushVerb, "adding a member needs no flush")
	})
	t.Run("member left", func(t *testing.T) {
		fx := newMGFixture(t, "resource", false, 2)
		fx.groups.memberIDs = []string{"mb1", "mb2"}
		fx.groups.memberEmails = []string{"user1@example.org", "user2@example.org"}
		w := do(t, fx.router, "PUT", "/api/v1/mailgroups/grp1/members", map[string]any{"mailbox_ids": []string{"mb1"}})
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Equal(t, flushVerb, lastCall(fx.ag), "calls=%v", fx.ag.calls)
	})
}

// --- catch-all ---

func catchallRouterWithAgent(dom *models.Domain, ag *mgAgentFake) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: dom.UserID, IsAdmin: true})
		c.Next()
	})
	repo := newMockDomainRepo()
	repo.domains[dom.ID] = dom
	RegisterDomainCatchallRoutes(v1, DomainCatchallHandlerConfig{Domains: repo, Agent: ag})
	return r
}

func TestCatchallChange_FlushesMailCaches(t *testing.T) {
	t.Run("set", func(t *testing.T) {
		ag := &mgAgentFake{}
		r := catchallRouterWithAgent(&models.Domain{ID: "d1", UserID: "u1", Name: "x.com", EmailEnabled: true}, ag)
		w := putCatchall(r, "me@gmail.com")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Equal(t, []string{"domain.catchall_set", flushVerb}, ag.calls)
	})
	t.Run("clear", func(t *testing.T) {
		ag := &mgAgentFake{}
		r := catchallRouterWithAgent(&models.Domain{ID: "d1", UserID: "u1", Name: "x.com", EmailEnabled: true}, ag)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/v1/domains/d1/catchall", nil))
		require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
		require.Equal(t, []string{"domain.catchall_clear", flushVerb}, ag.calls)
	})
}
