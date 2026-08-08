package api

import (
	"context"
	"net/http"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// JAB-233 — account-create with an optional primary domain. These exercise the
// userCreateHandler domain path end-to-end against createDomainOp, using a
// minimal fake domain repo. The extraction itself (createDomainOp == the GUI
// orchestration) is covered by the unchanged domain regression suite.

type dcDomains struct {
	repository.DomainRepository
	byName    map[string]*models.Domain
	createErr error
	created   []*models.Domain
}

func newDCDomains() *dcDomains { return &dcDomains{byName: map[string]*models.Domain{}} }

func (r *dcDomains) FindByName(_ context.Context, name string) (*models.Domain, error) {
	if d, ok := r.byName[name]; ok {
		return d, nil
	}
	return nil, repository.ErrNotFound
}

func (r *dcDomains) CountByUserID(_ context.Context, _ string) (int64, error) { return 0, nil }

func (r *dcDomains) Create(_ context.Context, d *models.Domain) error {
	if r.createErr != nil {
		return r.createErr
	}
	r.byName[d.Name] = d
	r.created = append(r.created, d)
	return nil
}

func billingCfgDom(users *abUsers, dom repository.DomainRepository) AutomationConfig {
	cfg := billingCfg(users)
	cfg.DomainCreate = DomainHandlerConfig{Users: users, Domains: dom}
	return cfg
}

// scope gate: a token WITHOUT write:domains + a domain must 403 before any
// account is created.
func TestAutomationUserCreate_Domain_ScopeGate403(t *testing.T) {
	users := newAbUsers()
	cfg := billingCfgDom(users, newDCDomains())
	body := `{"email":"a@example.com","password":"longenough1","username":"buyer","domain":"shop.example.com"}`
	tok := billingTok("write:users") // note: no write:domains
	r := abRouterWithBody(cfg, tok, body)
	w := abReq(r, http.MethodPost, "/users", body, tok)
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d: %s", w.Code, w.Body.String())
	}
	if decodeBody(t, w)["error"] != "scope_denied" {
		t.Fatalf("want scope_denied, got %v", decodeBody(t, w))
	}
	if len(users.created) != 0 {
		t.Fatalf("no account must be created when the domain scope is missing, got %d", len(users.created))
	}
}

// invalid domain fails fast (400) before the account exists.
func TestAutomationUserCreate_Domain_Invalid400(t *testing.T) {
	users := newAbUsers()
	cfg := billingCfgDom(users, newDCDomains())
	body := `{"email":"a@example.com","password":"longenough1","username":"buyer","domain":"not a domain!!"}`
	tok := billingTok("write:users", "write:domains")
	r := abRouterWithBody(cfg, tok, body)
	w := abReq(r, http.MethodPost, "/users", body, tok)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", w.Code, w.Body.String())
	}
	if len(users.created) != 0 {
		t.Fatalf("no account must be created for an invalid domain, got %d", len(users.created))
	}
}

// domain already owned by a DIFFERENT user → 409, fail-fast (no account).
func TestAutomationUserCreate_Domain_TakenByOther409(t *testing.T) {
	users := newAbUsers()
	dom := newDCDomains()
	dom.byName["taken.example.com"] = &models.Domain{ID: "d-other", UserID: "someone-else", Name: "taken.example.com"}
	cfg := billingCfgDom(users, dom)
	body := `{"email":"a@example.com","password":"longenough1","username":"buyer","domain":"taken.example.com"}`
	tok := billingTok("write:users", "write:domains")
	r := abRouterWithBody(cfg, tok, body)
	w := abReq(r, http.MethodPost, "/users", body, tok)
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", w.Code, w.Body.String())
	}
	if len(users.created) != 0 {
		t.Fatalf("no account must be created when the domain is taken, got %d", len(users.created))
	}
}

// happy path: account + primary domain created; envelope carries domain_id.
func TestAutomationUserCreate_Domain_HappyPath(t *testing.T) {
	users := newAbUsers()
	dom := newDCDomains()
	cfg := billingCfgDom(users, dom)
	body := `{"email":"a@example.com","password":"longenough1","username":"buyer","domain":"shop.example.com"}`
	tok := billingTok("write:users", "write:domains")
	r := abRouterWithBody(cfg, tok, body)
	w := abReq(r, http.MethodPost, "/users", body, tok)
	if w.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", w.Code, w.Body.String())
	}
	resp := decodeBody(t, w)
	if resp["status"] != "created" || resp["user_id"] == "" {
		t.Fatalf("bad envelope: %v", resp)
	}
	did, _ := resp["domain_id"].(string)
	if did == "" {
		t.Fatalf("expected domain_id in the response, got %v", resp)
	}
	if resp["domain_warning"] != nil {
		t.Fatalf("no domain_warning expected on success, got %v", resp["domain_warning"])
	}
	if len(dom.created) != 1 || dom.created[0].Name != "shop.example.com" {
		t.Fatalf("expected one domain created for shop.example.com, got %+v", dom.created)
	}
	if dom.created[0].UserID != resp["user_id"] {
		t.Fatalf("domain owner %q != created user %v", dom.created[0].UserID, resp["user_id"])
	}
}

// domain create failing AFTER the account exists → 201 + domain_warning, no rollback.
func TestAutomationUserCreate_Domain_PostCreateFailureWarns(t *testing.T) {
	users := newAbUsers()
	dom := newDCDomains()
	dom.createErr = repository.ErrConflict // e.g. insert race
	cfg := billingCfgDom(users, dom)
	body := `{"email":"a@example.com","password":"longenough1","username":"buyer","domain":"shop.example.com"}`
	tok := billingTok("write:users", "write:domains")
	r := abRouterWithBody(cfg, tok, body)
	w := abReq(r, http.MethodPost, "/users", body, tok)
	if w.Code != http.StatusCreated {
		t.Fatalf("want 201 (account still created), got %d: %s", w.Code, w.Body.String())
	}
	resp := decodeBody(t, w)
	if resp["status"] != "created" || resp["user_id"] == "" {
		t.Fatalf("account must still be created, got %v", resp)
	}
	if resp["domain_id"] != nil {
		t.Fatalf("no domain_id expected on domain failure, got %v", resp["domain_id"])
	}
	if warn, _ := resp["domain_warning"].(string); warn == "" {
		t.Fatalf("expected a domain_warning on post-create failure, got %v", resp)
	}
	if len(users.created) != 1 {
		t.Fatalf("account must exist despite the domain failure, got %d", len(users.created))
	}
}
