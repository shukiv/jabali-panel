package api

import (
	"context"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1627: buildTemplate is the admin-create validation boundary. Each record
// runs through the SAME ValidateDNSRecord the tenant DNS API uses, so a
// template can never carry a record the record API would reject. The {domain}
// token is a literal at this stage (no whitespace) and passes for MX/CNAME/TXT.
func TestBuildTemplate(t *testing.T) {
	t.Run("valid template normalises records", func(t *testing.T) {
		tmpl, status, code, _ := buildTemplate(dnsTemplateInput{
			Name:        "  Acme SaaS  ",
			Description: "external mail + verification",
			Records: []dnsTemplateRecordInput{
				{Name: "@", Type: "mx", Content: "mail.{domain}", Priority: 10},
				{Name: "@", Type: "TXT", Content: `"v=spf1 include:acme.example -all"`},
				{Name: "autoconfig", Type: "cname", Content: "config.acme.example"},
			},
		})
		if status != 0 {
			t.Fatalf("expected accept, got status=%d code=%q", status, code)
		}
		if tmpl.Name != "Acme SaaS" {
			t.Errorf("name not trimmed: %q", tmpl.Name)
		}
		if len(tmpl.Records) != 3 {
			t.Fatalf("records = %d, want 3", len(tmpl.Records))
		}
		if tmpl.Records[0].Type != "MX" {
			t.Errorf("type not upper-normalised: %q", tmpl.Records[0].Type)
		}
		if tmpl.Records[0].Content != "mail.{domain}" {
			t.Errorf("{domain} token must survive validation literally, got %q", tmpl.Records[0].Content)
		}
		for i := range tmpl.Records {
			if tmpl.Records[i].ID == "" {
				t.Errorf("record %d got no id", i)
			}
		}
	})

	cases := []struct {
		name string
		in   dnsTemplateInput
		code string
	}{
		{"empty name", dnsTemplateInput{Name: "  "}, "name_required"},
		{
			"bad record: A with non-IP",
			dnsTemplateInput{Name: "t", Records: []dnsTemplateRecordInput{{Name: "@", Type: "A", Content: "not-an-ip"}}},
			"invalid_record",
		},
		{
			"bad record: unknown type",
			dnsTemplateInput{Name: "t", Records: []dnsTemplateRecordInput{{Name: "@", Type: "ZZZ", Content: "x"}}},
			"invalid_record",
		},
		{
			"bad record: empty name",
			dnsTemplateInput{Name: "t", Records: []dnsTemplateRecordInput{{Name: "", Type: "TXT", Content: "x"}}},
			"invalid_record",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, status, code, _ := buildTemplate(tc.in)
			if code != tc.code {
				t.Fatalf("code = %q (status %d), want %q", code, status, tc.code)
			}
			if status == 0 {
				t.Error("rejection must carry a non-zero status")
			}
		})
	}

	t.Run("too many records", func(t *testing.T) {
		recs := make([]dnsTemplateRecordInput, dnsTemplateMaxRecords+1)
		for i := range recs {
			recs[i] = dnsTemplateRecordInput{Name: "@", Type: "TXT", Content: "x"}
		}
		_, _, code, _ := buildTemplate(dnsTemplateInput{Name: "t", Records: recs})
		if code != "too_many_records" {
			t.Fatalf("code = %q, want too_many_records", code)
		}
	})
}

// fakeDNSTemplates is a minimal DNSTemplateRepository for the createDomainOp
// gating tests — only FindByID is exercised.
type fakeDNSTemplates struct {
	repository.DNSTemplateRepository
	byID map[string]*models.DNSTemplate
}

func (f fakeDNSTemplates) FindByID(_ context.Context, id string) (*models.DNSTemplate, error) {
	if t, ok := f.byID[id]; ok {
		return t, nil
	}
	return nil, repository.ErrNotFound
}

// GH #1627: selecting a custom DNS template at create flips the domain to the
// external 'custom' mail posture, records mail_template_id (read once by the
// reconciler to seed the template), and is fail-closed on every misuse.
func TestCreateDomainOp_DNSTemplate(t *testing.T) {
	uname := "alice"
	owner := &models.User{ID: "u-alice", Email: "alice@example.com", Username: &uname}
	const tmplID = "tmpl-01"

	newH := func(withTemplates bool) *domainHandler {
		cfg := DomainHandlerConfig{Users: newAbUsers(owner), Domains: newDCDomains()}
		if withTemplates {
			cfg.DNSTemplates = fakeDNSTemplates{byID: map[string]*models.DNSTemplate{
				tmplID: {ID: tmplID, Name: "Acme SaaS"},
			}}
		}
		return &domainHandler{cfg: cfg}
	}

	t.Run("template selected → custom posture + template id + external mail", func(t *testing.T) {
		d, oerr := createDomainOp(context.Background(), newH(true), createDomainInput{
			OwnerID:       owner.ID,
			Name:          "shop.example.com",
			DNSTemplateID: tmplID,
		})
		if oerr != nil {
			t.Fatalf("unexpected error: %v (%s)", oerr.Code, oerr.Detail)
		}
		if d.MailProvider != models.MailProviderCustom {
			t.Errorf("MailProvider = %q, want custom", d.MailProvider)
		}
		if d.MailTemplateID == nil || *d.MailTemplateID != tmplID {
			t.Errorf("MailTemplateID = %v, want %q", d.MailTemplateID, tmplID)
		}
		if d.EmailEnabled {
			t.Error("a custom-template domain is external — EmailEnabled must be false")
		}
		if !d.SkipAutoSAN {
			t.Error("a custom-template domain must skip Jabali auto-SANs")
		}
	})

	rejections := []struct {
		name string
		in   createDomainInput
		code string
	}{
		{
			"caller-supplied custom provider is reserved",
			createDomainInput{OwnerID: owner.ID, Name: "a.example.com", MailProvider: models.MailProviderCustom},
			"mail_provider_custom_reserved",
		},
		{
			"template + explicit provider is exclusive",
			createDomainInput{OwnerID: owner.ID, Name: "b.example.com", DNSTemplateID: tmplID, MailProvider: models.MailProviderM365},
			"template_and_provider_exclusive",
		},
		{
			"template requires panel-hosted DNS",
			createDomainInput{OwnerID: owner.ID, Name: "c.example.com", DNSTemplateID: tmplID, DNSDisabled: true},
			"template_requires_dns",
		},
		{
			"unknown template id",
			createDomainInput{OwnerID: owner.ID, Name: "d.example.com", DNSTemplateID: "does-not-exist"},
			"unknown_dns_template",
		},
	}
	for _, tc := range rejections {
		t.Run(tc.name, func(t *testing.T) {
			_, oerr := createDomainOp(context.Background(), newH(true), tc.in)
			if oerr == nil {
				t.Fatalf("expected rejection %q, got success", tc.code)
			}
			if oerr.Code != tc.code {
				t.Fatalf("code = %q, want %q", oerr.Code, tc.code)
			}
		})
	}

	t.Run("template selected but feature unwired → 503 fail-closed", func(t *testing.T) {
		_, oerr := createDomainOp(context.Background(), newH(false), createDomainInput{
			OwnerID:       owner.ID,
			Name:          "e.example.com",
			DNSTemplateID: tmplID,
		})
		if oerr == nil || oerr.Code != "dns_templates_unavailable" {
			t.Fatalf("want dns_templates_unavailable, got %v", oerr)
		}
	})
}
