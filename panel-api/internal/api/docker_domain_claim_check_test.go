package api

import (
	"context"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// DockerDomainClaimCheck is the gin-free form of the checks the API runs on a
// name a Docker app's new domain row will claim. The CLI doors call it.

func TestDockerDomainClaimCheck_ACleanNameIsNil(t *testing.T) {
	h, _, _ := tenantEditHandler(t)
	settings := &fakeSettingsRepo{s: &models.ServerSettings{Hostname: "panel.example.com"}}
	err := DockerDomainClaimCheck(context.Background(), aliasHit{host: "shop.example.com"}, settings, h.cfg.Domains, "wiki.example.com", "u1", false)
	// A nil *dockerEditError inside an error is not nil: callers test err != nil.
	if err != nil {
		t.Fatalf("a clean name was refused: %v", err)
	}
}

func TestDockerDomainClaimCheck_Refusals(t *testing.T) {
	h, _, _ := tenantEditHandler(t)
	h.cfg.Domains.(guardDomainRepo).subs["corp.example.com"] = []models.Domain{{ID: "x", UserID: "u2", Name: "mail.corp.example.com"}}
	settings := &fakeSettingsRepo{s: &models.ServerSettings{Hostname: "panel.example.com"}}
	aliases := aliasHit{host: "shop.example.com"}
	ctx := context.Background()

	wantEditRefused(t, DockerDomainClaimCheck(ctx, aliases, settings, h.cfg.Domains, "shop.example.com", "u1", true), "domain_conflicts_alias")
	wantEditRefused(t, DockerDomainClaimCheck(ctx, aliases, settings, h.cfg.Domains, "panel.example.com", "u1", true), "domain_conflicts_mail_hostname")
	wantEditRefused(t, DockerDomainClaimCheck(ctx, aliases, &settingsDown{}, h.cfg.Domains, "wiki.example.com", "u1", true), "db_mail_hostname_lookup")
	wantEditRefused(t, DockerDomainClaimCheck(ctx, aliases, settings, h.cfg.Domains, "corp.example.com", "u1", false), "domain_conflicts_tenant")
	// The cross-tenant check is for tenants; an administrator may wrap a zone.
	if err := DockerDomainClaimCheck(ctx, aliases, settings, h.cfg.Domains, "corp.example.com", "u1", true); err != nil {
		t.Fatalf("admin refused over another tenant's subdomain: %v", err)
	}
}

// aliasesDown and subsDown can't be read.
type aliasesDown struct{ aliasHit }

func (aliasesDown) FindByHostname(context.Context, string) (*models.WebDomainAlias, error) {
	return nil, errors.New("database is down")
}

type subsDown struct{ guardDomainRepo }

func (subsDown) FindStrictSubdomains(context.Context, string) ([]models.Domain, error) {
	return nil, errors.New("database is down")
}

// A lookup that fails refuses the name; it never reads as "no clash".
func TestDockerDomainClaimCheck_AnUnreadableLookupFailsClosed(t *testing.T) {
	h, _, _ := tenantEditHandler(t)
	settings := &fakeSettingsRepo{s: &models.ServerSettings{Hostname: "panel.example.com"}}
	ctx := context.Background()
	wantEditRefused(t, DockerDomainClaimCheck(ctx, aliasesDown{}, settings, h.cfg.Domains, "wiki.example.com", "u1", true), "db_alias_lookup")
	down := subsDown{h.cfg.Domains.(guardDomainRepo)}
	wantEditRefused(t, DockerDomainClaimCheck(ctx, aliasHit{}, settings, down, "wiki.example.com", "u1", false), "db_suffix_lookup")
}
