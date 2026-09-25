package main

import (
	"regexp"
	"strings"
	"testing"
)

// TestCLICreateDomain_WiresDNSTemplate source-pins that `jabali domain create`
// honours --dns-template the same way the HTTP createDomainOp honours
// dns_template_id (GH #1627). createDomainDirect calls initConfig / initDB and
// is not behaviorally testable in a unit test (same as the JAB-279 docroot
// gate), so this source-pin is the load-bearing guard that the CLI keeps the
// wiring: the template is resolved by the shared domainops.ResolveMailPosture
// against the real template repository, and the resolved 'custom' posture and
// template id are what the row stores, so the reconciler seeds the template's
// records into the fresh zone. The posture rules themselves (existence check,
// exclusivity, DNS requirement, 'custom' override) are proven by
// domainops.TestResolveMailPosture.
func TestCLICreateDomain_WiresDNSTemplate(t *testing.T) {
	src := stripLineComments(readGoSource(t, "cli_create.go"))

	const resolve = "domainops.ResolveMailPosture(ctx, repository.NewDNSTemplateRepository(sharedDB), domainops.MailPostureInput{"
	resolveIdx := strings.Index(src, resolve)
	if resolveIdx < 0 {
		t.Fatalf("CLI create must resolve the mail posture against the DNS-template repository (%s)", resolve)
	}
	if !regexp.MustCompile(`DNSTemplateID:\s+in\.DNSTemplateID,`).MatchString(src) {
		t.Error("CLI create must pass --dns-template into the posture resolution (DNSTemplateID: in.DNSTemplateID)")
	}
	if !regexp.MustCompile(`DNSEnabled:\s+dnsEnabled,`).MatchString(src) {
		t.Error("CLI create must pass the --manage-dns state into the posture resolution (DNSEnabled: dnsEnabled)")
	}

	// The row stores the resolved posture: provider and template id.
	if !regexp.MustCompile(`MailProvider:\s+posture\.Provider,`).MatchString(src) {
		t.Error("CLI create must store the resolved provider (MailProvider: posture.Provider)")
	}
	if !regexp.MustCompile(`MailTemplateID:\s+posture\.TemplateID,`).MatchString(src) {
		t.Error("CLI create must record the template id on the domain row (MailTemplateID: posture.TemplateID)")
	}

	// The mail flags derive from the resolved posture, so a template domain's
	// external flags derive from 'custom', not from jabali.
	matrixIdx := strings.Index(src, "domainops.ResolveServiceMatrix(domainops.ServiceMatrixInput{")
	if matrixIdx < 0 || matrixIdx < resolveIdx {
		t.Error("the service matrix must be resolved AFTER the mail posture")
	}
	if !regexp.MustCompile(`MailProvider:\s+posture\.Provider,\s*\}\)`).MatchString(src) {
		t.Error("the service matrix must derive the mail flags from the resolved provider (MailProvider: posture.Provider)")
	}
}
