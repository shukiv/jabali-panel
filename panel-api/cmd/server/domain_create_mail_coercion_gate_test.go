package main

import (
	"regexp"
	"strings"
	"testing"
)

// TestCLICreateDomain_RunsMailModuleCoercion source-pins that the CLI create
// feeds the server's mail-module state into the shared posture resolution
// (domainops.ResolveMailPosture), read through the fail-open
// domainops.MailModuleEnabled over the real settings repository. createDomainDirect
// calls initConfig / initDB and is not behaviorally testable in a unit test, so
// this source-pin is the load-bearing guard against the CLI reverting to
// persisting Jabali mail on a server whose mail module is off (JAB-279 /
// GH #1409). The coercion itself — after the template posture, before the mail
// flags — is proven by domainops.TestResolveMailPosture; the REST wiring by
// TestCreateDomainOp_MailModuleCoercion.
func TestCLICreateDomain_RunsMailModuleCoercion(t *testing.T) {
	src := stripLineComments(readGoSource(t, "cli_create.go"))

	// It must READ the server mail-module flag — a coercion fed a hard-coded
	// bool would never coerce (or would always coerce).
	if !regexp.MustCompile(`MailModuleEnabled:\s+domainops\.MailModuleEnabled\(ctx, serverSettingsRepoFromDB\(\)\),`).MatchString(src) {
		t.Error("CLI create must feed the posture resolution the mail-module state read from the server settings (MailModuleEnabled: domainops.MailModuleEnabled(ctx, serverSettingsRepoFromDB()))")
	}

	// The module owns the provider rules and the mail flags; an inline copy in
	// the adapter would be free to drift from REST again.
	for _, inline := range []string{
		"models.ValidMailProvider(",
		"models.DeriveMailFlags(",
		"domainops.MailProviderForServer(",
		".MailEnabled",
	} {
		if strings.Contains(src, inline) {
			t.Errorf("CLI create must not resolve the mail posture inline (found %q); route through domainops", inline)
		}
	}
}
