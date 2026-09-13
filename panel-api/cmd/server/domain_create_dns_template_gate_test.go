package main

import (
	"strings"
	"testing"
)

// TestCLICreateDomain_WiresDNSTemplate source-pins that `jabali domain create`
// honours --dns-template the same way the HTTP createDomainOp honours
// dns_template_id (GH #1627). createDomainDirect calls initConfig / initDB and
// is not behaviorally testable in a unit test (same as the JAB-279 docroot
// gate), so this source-pin is the load-bearing guard that the CLI keeps the
// wiring: validate the template exists, override the mail posture to the
// external 'custom' value, and record MailTemplateID so the reconciler seeds
// the template's records into the fresh zone.
func TestCLICreateDomain_WiresDNSTemplate(t *testing.T) {
	src := readGoSource(t, "cli_create.go")

	// The template must be validated against the repository before it is trusted —
	// an unknown id must fail the create, not silently produce an inert domain.
	if !strings.Contains(src, "repository.NewDNSTemplateRepository(sharedDB).FindByID(ctx, tmplID)") {
		t.Error("CLI create must validate --dns-template against the repository (repository.NewDNSTemplateRepository(sharedDB).FindByID(ctx, tmplID))")
	}
	// A chosen template overrides the posture to external 'custom' (mirrors the op).
	if !strings.Contains(src, "mailProvider = models.MailProviderCustom") {
		t.Error("CLI create must override the mail posture to custom when a DNS template is chosen (mailProvider = models.MailProviderCustom)")
	}
	// The template id must be recorded on the row so the reconciler seeds it.
	if !strings.Contains(src, "mailTemplateID = &tmplID") ||
		!strings.Contains(src, "MailTemplateID: mailTemplateID") {
		t.Error("CLI create must record the template id on the domain row (mailTemplateID = &tmplID; MailTemplateID: mailTemplateID)")
	}
	// The posture override must run BEFORE DeriveMailFlags so the external mail
	// flags (email off, skip SAN) are derived from 'custom', not from jabali.
	tmplIdx := strings.Index(src, "mailProvider = models.MailProviderCustom")
	deriveIdx := strings.Index(src, "models.DeriveMailFlags(mailProvider)")
	if tmplIdx < 0 || deriveIdx < 0 || tmplIdx > deriveIdx {
		t.Error("the DNS-template posture override must precede DeriveMailFlags so external mail flags derive from 'custom'")
	}
}
