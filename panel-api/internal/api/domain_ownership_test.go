package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ownershipops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type ownActions struct {
	calls   []string
	changed bool
	err     error
	result  string
}

func (a *ownActions) CheckDomain(_ context.Context, d *models.Domain) (string, error) {
	a.calls = append(a.calls, "check "+d.ID)
	return a.result, a.err
}
func (a *ownActions) CheckAlias(_ context.Context, al *models.WebDomainAlias) (string, error) {
	a.calls = append(a.calls, "check-alias "+al.ID)
	return a.result, a.err
}
func (a *ownActions) ApproveDomain(_ context.Context, id string) (bool, error) {
	a.calls = append(a.calls, "approve "+id)
	return a.changed, a.err
}
func (a *ownActions) RevokeDomain(_ context.Context, id string) (bool, error) {
	a.calls = append(a.calls, "revoke "+id)
	return a.changed, a.err
}
func (a *ownActions) ApproveAlias(_ context.Context, id string) (bool, error) {
	a.calls = append(a.calls, "approve-alias "+id)
	return a.changed, a.err
}
func (a *ownActions) RevokeAlias(_ context.Context, id string) (bool, error) {
	a.calls = append(a.calls, "revoke-alias "+id)
	return a.changed, a.err
}

type ownDomains struct {
	repository.DomainRepository
	rows map[string]*models.Domain
}

func (f ownDomains) FindByID(_ context.Context, id string) (*models.Domain, error) {
	d, ok := f.rows[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	c := *d
	return &c, nil
}

type ownAliases struct {
	repository.WebDomainAliasRepository
	rows map[string]*models.WebDomainAlias
}

func (f ownAliases) FindByID(_ context.Context, id string) (*models.WebDomainAlias, error) {
	a, ok := f.rows[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	c := *a
	return &c, nil
}

type ownStore struct {
	repository.DomainOwnershipRepository
	require bool
	setBy   string
}

func (s *ownStore) GetSettings(context.Context) (models.DomainOwnershipSettings, error) {
	return models.DomainOwnershipSettings{ID: 1, RequireProof: s.require, UpdatedBy: s.setBy}, nil
}

func (s *ownStore) SetRequireProof(_ context.Context, require bool, by string, _ time.Time) error {
	s.require, s.setBy = require, by
	return nil
}

type recAudit struct{ actions []string }

func (r *recAudit) Record(e *models.AuditEvent) { r.actions = append(r.actions, e.Action) }

type ownershipAPI struct {
	actions *ownActions
	store   *ownStore
	audit   *recAudit
	router  *gin.Engine
}

func newOwnershipAPI(t *testing.T, userID string, isAdmin bool) *ownershipAPI {
	t.Helper()
	gin.SetMode(gin.TestMode)
	o := &ownershipAPI{actions: &ownActions{result: models.OwnershipResultNotFound}, store: &ownStore{require: true}, audit: &recAudit{}}
	pending := models.OwnershipState{OwnershipStatus: models.OwnershipPending, OwnershipToken: "abc123"}
	domains := ownDomains{rows: map[string]*models.Domain{
		"d1": {ID: "d1", Name: "example.com", UserID: "u1", OwnershipState: pending},
		"d2": {ID: "d2", Name: "other.com", UserID: "u2", OwnershipState: pending},
	}}
	aliases := ownAliases{rows: map[string]*models.WebDomainAlias{
		"a1": {ID: "a1", DomainID: "d1", Hostname: "www.example.com", OwnershipState: pending},
		"a2": {ID: "a2", DomainID: "d2", Hostname: "www.other.com", OwnershipState: pending},
	}}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: userID, IsAdmin: isAdmin})
		c.Next()
	})
	RegisterDomainOwnershipRoutes(r.Group("/api/v1"), DomainOwnershipHandlerConfig{
		Domains: domains, Aliases: aliases, Store: o.store, Actions: o.actions, Audit: o.audit,
	})
	o.router = r
	return o
}

func (o *ownershipAPI) do(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	o.router.ServeHTTP(w, req)
	return w
}

// GH #1816: the owner sees the record to publish and can ask for a check;
// another tenant's domain is a 404 and never reaches the resolvers.
func TestDomainOwnershipAPI_VerifyIsOwnerOnly(t *testing.T) {
	o := newOwnershipAPI(t, "u1", false)

	w := o.do(http.MethodGet, "/api/v1/domains/d1/ownership", "")
	if w.Code != http.StatusOK {
		t.Fatalf("get: %d %s", w.Code, w.Body.String())
	}
	var view ownershipView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.ChallengeName != "_jabali-challenge.example.com" || view.ChallengeValue != "jabali-verify=abc123" || view.Status != models.OwnershipPending {
		t.Fatalf("view = %+v", view)
	}

	if w := o.do(http.MethodPost, "/api/v1/domains/d1/ownership/verify", ""); w.Code != http.StatusOK {
		t.Fatalf("verify own domain: %d %s", w.Code, w.Body.String())
	}
	for _, path := range []string{"/api/v1/domains/d2/ownership", "/api/v1/domains/d2/ownership/verify", "/api/v1/domains/d2/aliases/a2/ownership/verify"} {
		method := http.MethodPost
		if strings.HasSuffix(path, "/ownership") {
			method = http.MethodGet
		}
		if w := o.do(method, path, ""); w.Code != http.StatusNotFound {
			t.Errorf("%s %s by another tenant: %d, want 404", method, path, w.Code)
		}
	}
	// An alias of another domain is not reachable through the caller's own.
	if w := o.do(http.MethodPost, "/api/v1/domains/d1/aliases/a2/ownership/verify", ""); w.Code != http.StatusNotFound {
		t.Errorf("foreign alias through own domain: %d, want 404", w.Code)
	}
	if len(o.actions.calls) != 1 || o.actions.calls[0] != "check d1" {
		t.Fatalf("only the owner's own check may run: %v", o.actions.calls)
	}
}

func TestDomainOwnershipAPI_VerifyIsRateLimitedPerUser(t *testing.T) {
	o := newOwnershipAPI(t, "u1", false)
	for i := 0; i < verifyPerMinute; i++ {
		if w := o.do(http.MethodPost, "/api/v1/domains/d1/ownership/verify", ""); w.Code != http.StatusOK {
			t.Fatalf("call %d: %d", i, w.Code)
		}
	}
	if w := o.do(http.MethodPost, "/api/v1/domains/d1/ownership/verify", ""); w.Code != http.StatusTooManyRequests {
		t.Fatalf("call over the limit: %d, want 429", w.Code)
	}
	if len(o.actions.calls) != verifyPerMinute {
		t.Fatalf("a limited call must not check: %d checks", len(o.actions.calls))
	}
}

func TestDomainOwnershipAPI_AdminRoutesRefuseTenants(t *testing.T) {
	o := newOwnershipAPI(t, "u1", false)
	for _, rt := range [][2]string{
		{http.MethodPost, "/api/v1/admin/domain-ownership/domains/d1/approve"},
		{http.MethodPost, "/api/v1/admin/domain-ownership/domains/d1/revoke"},
		{http.MethodPost, "/api/v1/admin/domain-ownership/aliases/a1/approve"},
		{http.MethodPost, "/api/v1/admin/domain-ownership/aliases/a1/revoke"},
		{http.MethodGet, "/api/v1/admin/domain-ownership/pending"},
		{http.MethodPut, "/api/v1/admin/domain-ownership/settings"},
	} {
		if w := o.do(rt[0], rt[1], `{"require_proof":false}`); w.Code != http.StatusForbidden {
			t.Errorf("%s %s by a tenant: %d, want 403", rt[0], rt[1], w.Code)
		}
	}
	if len(o.actions.calls) != 0 || !o.store.require {
		t.Fatalf("a tenant changed state: %v require=%v", o.actions.calls, o.store.require)
	}
}

func TestDomainOwnershipAPI_ApproveAndRevoke(t *testing.T) {
	o := newOwnershipAPI(t, "admin1", true)

	o.actions.changed = true
	if w := o.do(http.MethodPost, "/api/v1/admin/domain-ownership/domains/d1/approve", ""); w.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
	o.actions.changed = false
	if w := o.do(http.MethodPost, "/api/v1/admin/domain-ownership/domains/d1/approve", ""); w.Code != http.StatusConflict {
		t.Fatalf("approve of a verified row: %d, want 409", w.Code)
	}
	if len(o.audit.actions) != 1 || o.audit.actions[0] != "domain.ownership.approve" {
		t.Fatalf("only the approval that changed the row is audited: %v", o.audit.actions)
	}

	o.actions.err = ownershipops.ErrPanelPrimary
	w := o.do(http.MethodPost, "/api/v1/admin/domain-ownership/domains/d1/revoke", "")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "panel_primary_protected") {
		t.Fatalf("panel row revoke: %d %s", w.Code, w.Body.String())
	}
	o.actions.err = ownershipops.ErrDockerAppDomain
	w = o.do(http.MethodPost, "/api/v1/admin/domain-ownership/domains/d1/revoke", "")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "docker_app_domain") {
		t.Fatalf("docker-app revoke: %d %s", w.Code, w.Body.String())
	}

	// The row was revoked but the cascade did not finish: audited, and the
	// admin is told to repeat it.
	o.actions.changed, o.actions.err = true, errors.New("db blip")
	w = o.do(http.MethodPost, "/api/v1/admin/domain-ownership/domains/d1/revoke", "")
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "cascade_incomplete") {
		t.Fatalf("half cascade: %d %s", w.Code, w.Body.String())
	}
	if o.audit.actions[len(o.audit.actions)-1] != "domain.ownership.revoke" {
		t.Fatalf("a revoke that changed the row is audited: %v", o.audit.actions)
	}
}

func TestDomainOwnershipAPI_SettingsNeedABoolAndWarnWhenOff(t *testing.T) {
	o := newOwnershipAPI(t, "admin1", true)
	if w := o.do(http.MethodPut, "/api/v1/admin/domain-ownership/settings", `{}`); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing require_proof: %d, want 422", w.Code)
	}
	if !o.store.require {
		t.Fatal("an invalid body must not switch proof off")
	}
	w := o.do(http.MethodPut, "/api/v1/admin/domain-ownership/settings", `{"require_proof":false}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"warning"`) {
		t.Fatalf("switch off: %d %s", w.Code, w.Body.String())
	}
	if o.store.require || o.store.setBy != "admin1" {
		t.Fatalf("store = %+v", o.store)
	}
	w = o.do(http.MethodPut, "/api/v1/admin/domain-ownership/settings", `{"require_proof":true}`)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"warning"`) {
		t.Fatalf("switch on: %d %s", w.Code, w.Body.String())
	}
	if len(o.audit.actions) != 2 || o.audit.actions[0] != "domain.ownership.policy" {
		t.Fatalf("both switches are audited: %v", o.audit.actions)
	}
}
