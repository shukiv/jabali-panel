package main

import (
	"os"
	"strings"
	"testing"
)

// The domain email enable/disable lifecycle was consolidated into
// internal/domainmailops (JAB-288): the CLI's former enableDomainEmailDirect /
// disableDomainEmailDirect / syncEmailDNSOnEnableDirect /
// deleteEmailDNSOnDisableDirect copies are gone, and the behavioral matrix
// (agent provisioning, DKIM validation, DB state, M6 DNS sync, conflicts) now
// lives in internal/domainmailops/domainmailops_test.go, exercised once for
// both adapters.
//
// These source-pins keep the CLI commands routed through the shared module so a
// future edit can't reintroduce a divergent CLI copy. cmd/server has no
// seeded-DB fixture, so this follows the repo's source-pin precedent
// (domain_directory_privacy_cmd_containment_test.go, log_cmd_scope_test.go).

func TestDomainEmailCLI_RoutesThroughSharedModule(t *testing.T) {
	src, err := os.ReadFile("domain_email_cmd.go")
	if err != nil {
		t.Fatalf("read domain_email_cmd.go: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, "domainmailops.Enable(") {
		t.Error("email-enable must route through domainmailops.Enable")
	}
	if !strings.Contains(s, "domainmailops.Disable(") {
		t.Error("email-disable must route through domainmailops.Disable")
	}
}

// TestDomainEmailCLI_NoSSLScheduleWired guards the deliberate choice that the
// CLI passes no SSL deps (Deps.SSLCerts / SSLReconciler stay nil): a
// short-lived CLI process has no running reconciler, and ReconcileSSLSANDrift
// converges the mail SANs on its next pass. If a future edit wires SSL deps
// here it becomes a live-cert behavior change that needs box validation, so
// pin the current intent.
func TestDomainEmailCLI_NoSSLScheduleWired(t *testing.T) {
	src, err := os.ReadFile("domain_email_cmd.go")
	if err != nil {
		t.Fatalf("read domain_email_cmd.go: %v", err)
	}
	s := string(src)
	if strings.Contains(s, "SSLCerts:") || strings.Contains(s, "SSLReconciler:") {
		t.Error("CLI deps must not wire SSL flip (see file header); that is a box-verified behavior change")
	}
}

// TestDKIMRotateCLI_RoutesThroughSharedModule pins the DKIM-rotate command to
// the shared lifecycle (JAB-286): the former inline agent-call / unmarshal /
// UpdateEmailState copy is gone, and the behavioral matrix now lives once in
// internal/domainmailops/domainmailops_test.go. cmd/server has no seeded-DB
// fixture, so this follows the repo's source-pin precedent.
func TestDKIMRotateCLI_RoutesThroughSharedModule(t *testing.T) {
	src, err := os.ReadFile("domain_email_dkim_rotate_cmd.go")
	if err != nil {
		t.Fatalf("read domain_email_dkim_rotate_cmd.go: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, "domainmailops.RotateDKIM(") {
		t.Error("email-dkim-rotate must route through domainmailops.RotateDKIM")
	}
	// Direct persistence must not have crept back in — the module owns the
	// UpdateEmailState write (the doc comment may still name the agent verb).
	if strings.Contains(s, "UpdateEmailState(") {
		t.Error("DKIM-rotate persistence must live in the shared module, not inline in the CLI")
	}
}
