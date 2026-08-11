package reconciler

import (
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func strp(s string) *string { return &s }

func emailRow(name string) repository.SSLCertificateWithDomain {
	return repository.SSLCertificateWithDomain{
		ID: "c-" + name, DomainID: "d-" + name, DomainName: name,
		Status: models.SSLStatusIssued, CertPath: strp("/etc/letsencrypt/live/" + name + "/fullchain.pem"),
		SSLMode: models.SSLModeLE, EmailEnabled: true,
	}
}

func TestSSLSANDriftMissing(t *testing.T) {
	row := emailRow("example.com")

	// Missing autodiscover — the motivating case.
	miss := sslSANDriftMissing(row, []string{"example.com", "mail.example.com", "autoconfig.example.com"})
	if len(miss) != 1 || miss[0] != "autodiscover.example.com" {
		t.Fatalf("missing = %v, want [autodiscover.example.com]", miss)
	}

	// Complete cert — no drift.
	if m := sslSANDriftMissing(row, []string{"example.com", "mail.example.com", "autoconfig.example.com", "autodiscover.example.com"}); m != nil {
		t.Errorf("complete cert flagged drift: %v", m)
	}

	// Wildcard cert — assumed to cover the helpers, never churned.
	if m := sslSANDriftMissing(row, []string{"example.com", "*.example.com"}); m != nil {
		t.Errorf("wildcard cert flagged drift: %v", m)
	}

	// Case-insensitive match.
	if m := sslSANDriftMissing(row, []string{"example.com", "Mail.Example.COM", "AUTOCONFIG.example.com", "autodiscover.example.com"}); m != nil {
		t.Errorf("case difference flagged drift: %v", m)
	}

	// SkipAutoSAN → no helper SANs desired → no drift.
	skip := row
	skip.SkipAutoSAN = true
	if m := sslSANDriftMissing(skip, []string{"example.com"}); m != nil {
		t.Errorf("SkipAutoSAN flagged drift: %v", m)
	}

	// Email disabled → no mail helpers desired → no drift even with a bare cert.
	nomail := row
	nomail.EmailEnabled = false
	if m := sslSANDriftMissing(nomail, []string{"example.com"}); m != nil {
		t.Errorf("email-disabled flagged drift: %v", m)
	}
}

func TestSSLSANDriftEligible(t *testing.T) {
	base := emailRow("example.com")
	if !sslSANDriftEligible(base) {
		t.Error("issued LE cert should be eligible")
	}

	// Empty mode = legacy LE — eligible.
	empty := base
	empty.SSLMode = ""
	if !sslSANDriftEligible(empty) {
		t.Error("legacy empty-mode cert should be eligible")
	}

	// Non-LE modes MUST be skipped — certbot must never touch them (#738/#745).
	for _, mode := range []string{models.SSLModeCustom, models.SSLModeSelf, "shared", models.SSLModeNone} {
		r := base
		r.SSLMode = mode
		if sslSANDriftEligible(r) {
			t.Errorf("mode %q must NOT be eligible", mode)
		}
	}

	// Not issued, or no cert file — skipped.
	pending := base
	pending.Status = models.SSLStatusPending
	if sslSANDriftEligible(pending) {
		t.Error("pending cert must not be eligible")
	}
	nofile := base
	nofile.CertPath = nil
	if sslSANDriftEligible(nofile) {
		t.Error("cert without a file must not be eligible")
	}
}

func TestSANDriftCooldown(t *testing.T) {
	r := &Reconciler{}
	now := time.Now().UTC()

	if !r.sanDriftCooldownPassed("c1", now) {
		t.Error("first check should pass (never attempted)")
	}
	r.markSANDriftAttempt("c1", now)
	if r.sanDriftCooldownPassed("c1", now.Add(time.Hour)) {
		t.Error("within cooldown should NOT pass")
	}
	if !r.sanDriftCooldownPassed("c1", now.Add(sslSANDriftCooldown+time.Minute)) {
		t.Error("after cooldown should pass again")
	}
	// A different cert is independent.
	if !r.sanDriftCooldownPassed("c2", now.Add(time.Hour)) {
		t.Error("unrelated cert should pass")
	}
}
