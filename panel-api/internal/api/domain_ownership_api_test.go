package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// ownershipStub is a DomainOwnershipRepository whose only live method is the
// proof-required switch; the others panic if a test reaches them.
type ownershipStub struct {
	repository.DomainOwnershipRepository
	require bool
}

func (s ownershipStub) GetSettings(context.Context) (models.DomainOwnershipSettings, error) {
	return models.DomainOwnershipSettings{ID: 1, RequireProof: s.require}, nil
}

// GH #1816 / ADR-0170 decision 1: a billing system's domain is stored
// pending unless its token holds assert:domain_ownership — which no wildcard,
// write:* included, implies.
func TestAutomationUserCreate_Domain_OwnershipFollowsTheAssertScope(t *testing.T) {
	cases := []struct {
		name       string
		scopes     []string
		wantStatus string
		wantMethod string
	}{
		{"write scopes only", []string{"write:users", "write:domains"}, models.OwnershipPending, ""},
		{"write wildcard does not imply the assertion", []string{"write:*"}, models.OwnershipPending, ""},
		{"assert scope vouches for the name",
			[]string{"write:users", "write:domains", models.AutomationScopeAssertDomainOwnership},
			models.OwnershipVerified, models.OwnershipMethodAutomation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			users := newAbUsers()
			dom := newDCDomains()
			cfg := billingCfgDom(users, dom)
			cfg.DomainCreate.DomainOwnership = ownershipStub{require: true}
			body := `{"email":"a@example.com","password":"longenough1","username":"buyer","domain":"shop.example.com"}`
			tok := billingTok(tc.scopes...)
			r := abRouterWithBody(cfg, tok, body)
			w := abReq(r, http.MethodPost, "/users", body, tok)
			if w.Code != http.StatusCreated {
				t.Fatalf("want 201, got %d: %s", w.Code, w.Body.String())
			}
			if len(dom.created) != 1 {
				t.Fatalf("want one domain, got %d", len(dom.created))
			}
			got := dom.created[0].OwnershipState
			if got.OwnershipStatus != tc.wantStatus || got.OwnershipMethod != tc.wantMethod {
				t.Fatalf("ownership = %s/%s, want %s/%s", got.OwnershipStatus, got.OwnershipMethod, tc.wantStatus, tc.wantMethod)
			}
		})
	}
}

// The assertion scope is a recognised, separately minted scope.
func TestAutomationScopes_AssertDomainOwnershipIsItsOwnFamily(t *testing.T) {
	if !models.IsAllowedAutomationScope(models.AutomationScopeAssertDomainOwnership) {
		t.Fatal("assert:domain_ownership must be an allowed scope")
	}
	for _, wild := range []string{"write:*", "read:*", "delete:*"} {
		if (models.AutomationScopes{wild}).Has(models.AutomationScopeAssertDomainOwnership) {
			t.Fatalf("%s must not imply assert:domain_ownership", wild)
		}
	}
}

// GH #1816: the GUI door. A tenant's own create is pending; an admin's create
// is verified (method admin); with proof switched off it is verified
// (policy_off).
func TestCreateDomainOp_OwnershipByActor(t *testing.T) {
	uname := "alice"
	owner := &models.User{ID: "u-alice", Email: "alice@example.com", Username: &uname}
	cases := []struct {
		name       string
		require    bool
		admin      bool
		wantStatus string
		wantMethod string
	}{
		{"tenant create is pending", true, false, models.OwnershipPending, ""},
		{"admin create is verified", true, true, models.OwnershipVerified, models.OwnershipMethodAdmin},
		{"proof off verifies a tenant create", false, false, models.OwnershipVerified, models.OwnershipMethodPolicyOff},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &domainHandler{cfg: DomainHandlerConfig{
				Users: newAbUsers(owner), Domains: newDCDomains(),
				DomainOwnership: ownershipStub{require: tc.require},
			}}
			d, oerr := createDomainOp(context.Background(), h, createDomainInput{
				OwnerID: owner.ID, Name: "shop.example.com", ActorIsAdmin: tc.admin,
				MailProvider: models.MailProviderNone,
			})
			if oerr != nil {
				t.Fatalf("create: %v", oerr)
			}
			if d.OwnershipStatus != tc.wantStatus || d.OwnershipMethod != tc.wantMethod {
				t.Fatalf("ownership = %s/%s, want %s/%s", d.OwnershipStatus, d.OwnershipMethod, tc.wantStatus, tc.wantMethod)
			}
			if tc.wantStatus == models.OwnershipPending && len(d.OwnershipToken) != 64 {
				t.Fatalf("a pending create must carry a 64-hex challenge token, got %q", d.OwnershipToken)
			}
		})
	}
}

// A nil ownership store (an unwired panel) fails closed: proof required.
func TestCreateDomainOp_NilOwnershipStoreFailsClosed(t *testing.T) {
	uname := "alice"
	owner := &models.User{ID: "u-alice", Email: "alice@example.com", Username: &uname}
	h := &domainHandler{cfg: DomainHandlerConfig{Users: newAbUsers(owner), Domains: newDCDomains()}}
	d, oerr := createDomainOp(context.Background(), h, createDomainInput{
		OwnerID: owner.ID, Name: "shop.example.com", MailProvider: models.MailProviderNone,
	})
	if oerr != nil {
		t.Fatalf("create: %v", oerr)
	}
	if d.OwnershipStatus != models.OwnershipPending {
		t.Fatalf("an unwired ownership store must store a tenant create pending, got %q", d.OwnershipStatus)
	}
	if repository.NewDomainOwnershipRepository(nil) != nil {
		t.Fatal("a nil db must yield a nil ownership store, never one that panics")
	}
}

// GH #1816: a docker-app install that auto-creates its domain makes the same
// ownership decision as a domain create.
func TestStampDockerDomainOwnership(t *testing.T) {
	verifiedParent := &models.Domain{ID: "p1", UserID: "u1", Name: "example.com",
		OwnershipState: models.OwnershipState{OwnershipStatus: models.OwnershipVerified}}
	domains := &fakeDomainRepo{byName: map[string]*models.Domain{"example.com": verifiedParent}}
	cases := []struct {
		name, domain string
		admin        bool
		wantStatus   string
		wantMethod   string
	}{
		{"tenant, unproven name", "app.other.test", false, models.OwnershipPending, ""},
		{"tenant, under own verified parent", "app.example.com", false, models.OwnershipVerified, models.OwnershipMethodParent},
		{"admin install", "app.other.test", true, models.OwnershipVerified, models.OwnershipMethodAdmin},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dom := &models.Domain{ID: "d1", UserID: "u1", Name: tc.domain}
			if err := stampDockerDomainOwnership(context.Background(), domains, ownershipStub{require: true}, dom, tc.admin); err != nil {
				t.Fatal(err)
			}
			if dom.OwnershipStatus != tc.wantStatus || dom.OwnershipMethod != tc.wantMethod {
				t.Fatalf("ownership = %s/%s, want %s/%s", dom.OwnershipStatus, dom.OwnershipMethod, tc.wantStatus, tc.wantMethod)
			}
		})
	}
}

// Every docker-app domain auto-create records the ownership decision before
// the row is inserted: a row inserted without it would be stored pending by
// the column default, but with no challenge token to prove it with.
func TestDockerAppDomainCreatesStampOwnership(t *testing.T) {
	for _, file := range []string{"docker_apps.go", "docker_apps_user.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		s := string(src)
		creates := strings.Count(s, "h.cfg.Domains.Create(ctx, dom)")
		if creates == 0 {
			t.Fatalf("%s: expected a docker-app domain auto-create", file)
		}
		for i, rest := 0, s; i < creates; i++ {
			at := strings.Index(rest, "h.cfg.Domains.Create(ctx, dom)")
			before := rest[:at]
			stamp := strings.LastIndex(before, "stampDockerDomainOwnership(")
			literal := strings.LastIndex(before, "dom := &models.Domain{")
			if stamp < 0 || stamp < literal {
				t.Fatalf("%s: domain create #%d is not preceded by stampDockerDomainOwnership", file, i+1)
			}
			rest = rest[at+1:]
		}
	}
}

type attachDomains struct {
	repository.DomainRepository
	dom *models.Domain
	set int
}

func (f *attachDomains) FindByID(context.Context, string) (*models.Domain, error) { return f.dom, nil }
func (f *attachDomains) SetSharedCertificate(context.Context, string, *string, string) error {
	f.set++
	return nil
}

// GH #1816 / ADR-0170: a pending domain cannot be attached to a CA-issued
// shared certificate, even by an admin; a verified one can.
func TestAttachSharedCert_PendingOwnership409(t *testing.T) {
	wild := `["*.example.com"]`
	certs := &fakeSharedRepo{byID: map[string]*models.SharedCertificate{"c1": {ID: "c1", SANs: &wild}}}
	for _, tc := range []struct {
		status   string
		wantCode int
		wantSet  int
	}{
		{models.OwnershipPending, http.StatusConflict, 0},
		{models.OwnershipVerified, http.StatusOK, 1},
	} {
		doms := &attachDomains{dom: &models.Domain{ID: "d1", UserID: "u1", Name: "shop.example.com",
			OwnershipState: models.OwnershipState{OwnershipStatus: tc.status}}}
		h := newSSLHandler(SSLHandlerConfig{SharedCerts: certs, Domains: doms})
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/api/v1/domains/d1/ssl/shared",
			strings.NewReader(`{"shared_certificate_id":"c1"}`))
		c.Params = gin.Params{{Key: "id", Value: "d1"}}
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "admin", IsAdmin: true})
		h.attachSharedCert(c)
		if w.Code != tc.wantCode || doms.set != tc.wantSet {
			t.Fatalf("%s: got %d (attached %d), want %d (attached %d): %s", tc.status, w.Code, doms.set, tc.wantCode, tc.wantSet, w.Body.String())
		}
	}
}

type renameOwnershipDomains struct {
	repository.DomainRepository
	d *models.Domain
}

func (f renameOwnershipDomains) FindByID(context.Context, string) (*models.Domain, error) {
	return f.d, nil
}
func (f renameOwnershipDomains) FindByName(context.Context, string) (*models.Domain, error) {
	return nil, repository.ErrNotFound
}
func (f renameOwnershipDomains) FindStrictSubdomains(context.Context, string) ([]models.Domain, error) {
	return nil, nil
}

// recordingOwnership records the ownership writes a door makes, in order.
type recordingOwnership struct {
	ownershipStub
	calls []string
	token string
}

func (r *recordingOwnership) MarkDomainPending(_ context.Context, id, token string, _ time.Time, fromVerified, clearVerifiedAt bool) (bool, error) {
	r.token = token
	r.calls = append(r.calls, fmt.Sprintf("pending %s from_verified=%v clear=%v", id, fromVerified, clearVerifiedAt))
	return true, nil
}
func (r *recordingOwnership) MarkDomainVerified(_ context.Context, id, method, expectToken string, _ time.Time) (bool, error) {
	r.calls = append(r.calls, fmt.Sprintf("verified %s %s token_matches=%v", id, method, expectToken == r.token))
	return true, nil
}

func tenantRename(t *testing.T, d *models.Domain, own repository.DomainOwnershipRepository, newName string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := &domainHandler{cfg: DomainHandlerConfig{
		Domains:          renameOwnershipDomains{d: d},
		WebDomainAliases: aliasTestAliases{},
		ServerSettings:   aliasTestSettings{hostname: "panel.host.com"},
		DomainOwnership:  own,
	}}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1"})
		c.Next()
	})
	r.POST("/domains/:id/rename", h.rename)
	req := httptest.NewRequest(http.MethodPost, "/domains/d1/rename", strings.NewReader(`{"name":"`+newName+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// GH #1816: a tenant rename to an unproven name writes pending BEFORE the
// row is renamed; when the rename then fails (no rename dependencies are
// wired here), the old name's verified state is restored.
func TestDomainRename_UnprovenNamePendingFirstThenRestored(t *testing.T) {
	own := &recordingOwnership{ownershipStub: ownershipStub{require: true}}
	d := &models.Domain{ID: "d1", Name: "old.example.org", UserID: "u1",
		OwnershipState: models.OwnershipState{OwnershipStatus: models.OwnershipVerified, OwnershipMethod: models.OwnershipMethodDNSTXT}}
	rec := tenantRename(t, d, own, "new.example.net")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want the rename itself to fail unwired (503), got %d %s", rec.Code, rec.Body.String())
	}
	want := []string{
		"pending d1 from_verified=false clear=true",
		"verified d1 " + models.OwnershipMethodDNSTXT + " token_matches=true",
	}
	if strings.Join(own.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("ownership writes = %q, want %q", own.calls, want)
	}
}

// GH #1816: an unproven new name is refused for a domain whose mail is
// registered, before anything is written.
func TestDomainRename_UnprovenNameWithMail409(t *testing.T) {
	own := &recordingOwnership{ownershipStub: ownershipStub{require: true}}
	selector := "jabali"
	d := &models.Domain{ID: "d1", Name: "old.example.org", UserID: "u1", DkimSelector: &selector,
		OwnershipState: models.OwnershipState{OwnershipStatus: models.OwnershipVerified}}
	rec := tenantRename(t, d, own, "new.example.net")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "rename_requires_proven_name") {
		t.Fatalf("want 409 rename_requires_proven_name, got %d %s", rec.Code, rec.Body.String())
	}
	if len(own.calls) != 0 {
		t.Fatalf("no ownership write may happen on a refused rename, got %q", own.calls)
	}
}

type aliasOwnershipDomains struct {
	aliasTestDomains
	byID *models.Domain
}

func (f aliasOwnershipDomains) FindByID(context.Context, string) (*models.Domain, error) {
	return f.byID, nil
}

type aliasOwnershipAliases struct {
	aliasTestAliases
	created []*models.WebDomainAlias
}

func (f *aliasOwnershipAliases) Create(_ context.Context, a *models.WebDomainAlias) error {
	f.created = append(f.created, a)
	return nil
}

// GH #1816 / ADR-0170: a new web alias gets the create-time decision against
// the domain's owner; a pending alias carries its own challenge token.
func TestAliasCreate_Ownership(t *testing.T) {
	dom := &models.Domain{ID: "d1", Name: "example.com", UserID: "u1",
		OwnershipState: models.OwnershipState{OwnershipStatus: models.OwnershipVerified}}
	cases := []struct {
		name, host string
		admin      bool
		wantStatus string
		wantMethod string
	}{
		{"a tenant's foreign name is pending", "shop.brandy.io", false, models.OwnershipPending, ""},
		{"a subdomain of the owner's verified domain is verified", "blog.example.com", false, models.OwnershipVerified, models.OwnershipMethodParent},
		{"an admin's alias is verified", "shop.brandy.io", true, models.OwnershipVerified, models.OwnershipMethodAdmin},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			aliases := &aliasOwnershipAliases{}
			h := &domainAliasHandler{cfg: DomainAliasHandlerConfig{
				Domains:   aliasOwnershipDomains{aliasTestDomains: aliasTestDomains{names: map[string]*models.Domain{"example.com": dom}}, byID: dom},
				Aliases:   aliases,
				Settings:  aliasTestSettings{hostname: "panel.host.com"},
				Ownership: ownershipStub{require: true},
			}}
			gin.SetMode(gin.TestMode)
			r := gin.New()
			r.Use(func(c *gin.Context) {
				ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1", IsAdmin: tc.admin})
				c.Next()
			})
			r.POST("/domains/:id/aliases", h.create)
			req := httptest.NewRequest(http.MethodPost, "/domains/d1/aliases", strings.NewReader(`{"hostname":"`+tc.host+`"}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != http.StatusCreated || len(aliases.created) != 1 {
				t.Fatalf("want 201 and one alias, got %d %s", rec.Code, rec.Body.String())
			}
			got := aliases.created[0].OwnershipState
			if got.OwnershipStatus != tc.wantStatus || got.OwnershipMethod != tc.wantMethod || len(got.OwnershipToken) != 64 {
				t.Fatalf("alias ownership = %s/%s token=%d, want %s/%s", got.OwnershipStatus, got.OwnershipMethod, len(got.OwnershipToken), tc.wantStatus, tc.wantMethod)
			}
			if !strings.Contains(rec.Body.String(), `"ownership_status":"`+tc.wantStatus+`"`) {
				t.Fatalf("the response must carry the alias ownership status: %s", rec.Body.String())
			}
		})
	}
}
