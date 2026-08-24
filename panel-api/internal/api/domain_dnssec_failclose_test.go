package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

type dnssecRawAgent struct {
	raw string
	err error
}

func (a dnssecRawAgent) Call(_ context.Context, _ string, _ any) (json.RawMessage, error) {
	if a.err != nil {
		return nil, a.err
	}
	return json.RawMessage(a.raw), nil
}

type dnssecFakeKeys struct{}

func (dnssecFakeKeys) ListByDomainID(context.Context, string) ([]models.DomainDNSSECKey, error) {
	return nil, nil
}
func (dnssecFakeKeys) ReplaceAll(context.Context, string, []models.DomainDNSSECKey) error { return nil }
func (dnssecFakeKeys) DeleteAllForDomain(context.Context, string) error                   { return nil }

func dnssecUpdateRouter(dom *models.Domain, ag *dnssecRawAgent) (*gin.Engine, *mockDomainRepo) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	repo := newMockDomainRepo()
	repo.domains[dom.ID] = dom
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: dom.UserID, IsAdmin: true})
		c.Next()
	})
	RegisterDomainDNSSECRoutes(v1, DomainDNSSECHandlerConfig{Agent: ag, Domains: repo, Keys: dnssecFakeKeys{}})
	return r, repo
}

func putEnableDNSSEC(r *gin.Engine, id string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPut, "/api/v1/domains/"+id+"/dnssec", strings.NewReader(`{"enabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// JAB-322: an enable whose agent reply is malformed or ok=false must leave
// dnssec_enabled UNCHANGED (fail closed) and return 502 — the old code flipped
// the flag regardless of the reply.
func TestDNSSECUpdate_MalformedReply_LeavesFlagUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"malformed json", `{"ok":true,"keys":`},
		{"ok false", `{"ok":false}`},
		{"garbage", `not json`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dom := &models.Domain{ID: "d1", Name: "ex.com", UserID: "u1", DNSSECEnabled: false}
			r, repo := dnssecUpdateRouter(dom, &dnssecRawAgent{raw: tc.raw})
			w := putEnableDNSSEC(r, "d1")

			if w.Code != http.StatusBadGateway {
				t.Errorf("want 502 on a bad reply, got %d", w.Code)
			}
			if repo.domains["d1"].DNSSECEnabled {
				t.Error("dnssec_enabled must stay false when the agent reply is not a clean success (fail closed)")
			}
		})
	}
}

func TestDNSSECUpdate_OkReply_FlipsFlag(t *testing.T) {
	dom := &models.Domain{ID: "d1", Name: "ex.com", UserID: "u1", DNSSECEnabled: false}
	r, repo := dnssecUpdateRouter(dom, &dnssecRawAgent{raw: `{"ok":true,"keys":[{"key_tag":1,"key_type":"KSK","algorithm":13}]}`})
	w := putEnableDNSSEC(r, "d1")

	if w.Code != http.StatusOK {
		t.Fatalf("want 200 on a clean enable, got %d (%s)", w.Code, w.Body.String())
	}
	if !repo.domains["d1"].DNSSECEnabled {
		t.Error("dnssec_enabled must be true after a clean ok=true reply")
	}
}
