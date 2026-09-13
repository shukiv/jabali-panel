package main

import (
	"strings"
	"testing"
)

// TestCLICreateDomain_RunsMailModuleCoercion source-pins that the CLI create runs
// the shared mail-module coercion (domainops.MailProviderForServer) after it
// resolves the DNS-template posture and before it derives the mail flags, reading
// the server settings first. createDomainDirect calls initConfig / initDB and is
// not behaviorally testable in a unit test, so this source-pin is the load-bearing
// guard against the CLI reverting to persisting Jabali mail on a server whose mail
// module is off (JAB-279 / GH #1409). The leaf's own behavior is proven by
// domainops.TestMailProviderForServer; the REST wiring by
// TestCreateDomainOp_MailModuleCoercion.
func TestCLICreateDomain_RunsMailModuleCoercion(t *testing.T) {
	src := stripLineComments(readGoSource(t, "cli_create.go"))

	const call = "domainops.MailProviderForServer(mailProvider, mailModuleEnabled)"
	callIdx := strings.Index(src, call)
	if callIdx < 0 {
		t.Fatalf("CLI create must coerce the mail provider via %q", call)
	}

	// It must READ the server mail-module flag — a coercion that hard-codes the
	// bool would never coerce (or would always coerce). The read mirrors the REST
	// path and fails open on an unreadable row.
	if !strings.Contains(src, "serverSettingsRepoFromDB().Get(ctx)") {
		t.Error("CLI create must read the server settings to decide the mail-module coercion")
	}

	// Ordering: after the DNS-template block sets the 'custom' posture, so a
	// template-created domain is not coerced.
	tmplIdx := strings.Index(src, "mailTemplateID = &tmplID")
	if tmplIdx < 0 {
		t.Fatal("expected the DNS-template posture assignment in the CLI create path")
	}
	if callIdx < tmplIdx {
		t.Error("mail-module coercion must run AFTER the DNS-template block")
	}

	// Ordering: before DeriveMailFlags, so email_enabled reflects the coerced
	// provider (and the no-service check sees the post-coercion mail state), the
	// same precedence createDomainOp uses.
	deriveIdx := strings.Index(src, "models.DeriveMailFlags(mailProvider)")
	if deriveIdx < 0 {
		t.Fatal("expected models.DeriveMailFlags(mailProvider) in the CLI create path")
	}
	if callIdx > deriveIdx {
		t.Error("mail-module coercion must run BEFORE models.DeriveMailFlags")
	}
}
