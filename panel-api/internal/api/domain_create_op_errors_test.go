package api

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// JAB-279 AC1: the create orchestration lives in domainops.Create and this
// door maps its rejections back to HTTP. These cases pin the wire shape of the
// rejections no other create test reaches, plus the order in which the first
// two faults of a request are reported. They pass unchanged on the door as it
// was before the module owned the orchestration.

type opErrDomains struct {
	*dcDomains
	nameErr error
}

func (r *opErrDomains) FindByName(ctx context.Context, name string) (*models.Domain, error) {
	if r.nameErr != nil {
		return nil, r.nameErr
	}
	return r.dcDomains.FindByName(ctx, name)
}

type opErrUsers struct {
	repository.UserRepository
	err error
}

func (u opErrUsers) FindByID(context.Context, string) (*models.User, error) { return nil, u.err }

type opErrWebTemplates struct {
	repository.WebTemplateRepository
}

func (opErrWebTemplates) FindByID(context.Context, string) (*models.WebTemplate, error) {
	return nil, errors.New("db down")
}

func TestCreateDomainOp_RejectionWireShape(t *testing.T) {
	uname := "alice"
	owner := &models.User{ID: "u-alice", Email: "alice@example.com", Username: &uname}

	cases := []struct {
		name   string
		cfg    func(*DomainHandlerConfig, *opErrDomains)
		in     createDomainInput
		status int
		code   string
		detail string
	}{
		{
			name:   "invalid name",
			in:     createDomainInput{OwnerID: owner.ID, Name: "localhost"},
			status: http.StatusBadRequest, code: "invalid_domain_name",
			detail: "domain name is not a valid FQDN (requires at least two labels and 2+ letter TLD)",
		},
		{
			name: "name held by another domain's alias",
			cfg: func(c *DomainHandlerConfig, _ *opErrDomains) {
				c.WebDomainAliases = aliasTestAliases{taken: map[string]bool{"www.shop.example.com": true}}
			},
			in:     createDomainInput{OwnerID: owner.ID, Name: "shop.example.com"},
			status: http.StatusConflict, code: "domain_conflicts_alias",
			detail: "the name www.shop.example.com is already used as an alias of another domain",
		},
		{
			name: "an alias clash is reported before a missing owner",
			cfg: func(c *DomainHandlerConfig, _ *opErrDomains) {
				c.WebDomainAliases = aliasTestAliases{taken: map[string]bool{"shop.example.com": true}}
			},
			in:     createDomainInput{Name: "shop.example.com"},
			status: http.StatusConflict, code: "domain_conflicts_alias",
			detail: "the name shop.example.com is already used as an alias of another domain",
		},
		{
			name:   "missing owner",
			in:     createDomainInput{Name: "shop.example.com"},
			status: http.StatusBadRequest, code: "user_id is required",
		},
		{
			name:   "cross-tenant lookup failure fails closed",
			cfg:    func(_ *DomainHandlerConfig, d *opErrDomains) { d.nameErr = errors.New("db down") },
			in:     createDomainInput{OwnerID: owner.ID, Name: "shop.example.com"},
			status: http.StatusInternalServerError, code: "db_suffix_lookup",
			detail: "could not verify the domain name against existing domains",
		},
		{
			name:   "unknown owner",
			in:     createDomainInput{OwnerID: "u-ghost", Name: "shop.example.com"},
			status: http.StatusBadRequest, code: "user not found",
		},
		{
			name:   "owner lookup failure",
			cfg:    func(c *DomainHandlerConfig, _ *opErrDomains) { c.Users = opErrUsers{err: errors.New("db down")} },
			in:     createDomainInput{OwnerID: owner.ID, Name: "shop.example.com"},
			status: http.StatusInternalServerError, code: "internal",
		},
		{
			name:   "web template lookup failure",
			cfg:    func(c *DomainHandlerConfig, _ *opErrDomains) { c.WebTemplates = opErrWebTemplates{} },
			in:     createDomainInput{OwnerID: owner.ID, Name: "shop.example.com", ActorIsAdmin: true, WebTemplateID: "t1"},
			status: http.StatusInternalServerError, code: "web_template_lookup_failed",
		},
		{
			name:   "invalid M365 tenant",
			in:     createDomainInput{OwnerID: owner.ID, Name: "shop.example.com", M365Onmicrosoft: "not a label"},
			status: http.StatusBadRequest, code: "invalid_m365_onmicrosoft",
			detail: `invalid Microsoft 365 tenant "not a label" (expected a label like 'contoso' or 'contoso.onmicrosoft.com')`,
		},
		{
			name:   "invalid Google DKIM value",
			in:     createDomainInput{OwnerID: owner.ID, Name: "shop.example.com", GoogleDKIM: `v=DKIM1; p="x"`},
			status: http.StatusBadRequest, code: "invalid_google_dkim",
			detail: "Google DKIM value contains an illegal character",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dom := &opErrDomains{dcDomains: newDCDomains()}
			cfg := DomainHandlerConfig{Users: newAbUsers(owner), Domains: dom}
			if tc.cfg != nil {
				tc.cfg(&cfg, dom)
			}
			tc.in.MailProvider = models.MailProviderNone
			tc.in.SkipInlineSSL = true

			d, oerr := createDomainOp(context.Background(), &domainHandler{cfg: cfg}, tc.in)
			if d != nil || oerr == nil {
				t.Fatalf("want a rejection, got domain %v", d)
			}
			if oerr.Status != tc.status || oerr.Code != tc.code || oerr.Detail != tc.detail {
				t.Fatalf("want %d %q %q, got %d %q %q", tc.status, tc.code, tc.detail, oerr.Status, oerr.Code, oerr.Detail)
			}
			if len(dom.created) != 0 {
				t.Fatalf("a rejected create must store nothing, got %d rows", len(dom.created))
			}
		})
	}
}
